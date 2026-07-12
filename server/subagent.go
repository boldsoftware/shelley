package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// SubagentRunner implements claudetool.SubagentRunner.
type SubagentRunner struct {
	server *Server
}

// NewSubagentRunner creates a new SubagentRunner.
func NewSubagentRunner(s *Server) *SubagentRunner {
	return &SubagentRunner{server: s}
}

// RunSubagent implements claudetool.SubagentRunner.
func (r *SubagentRunner) RunSubagent(ctx context.Context, conversationID, prompt string, wait bool, timeout time.Duration, modelID, reasoning string) (string, error) {
	s := r.server

	// Notify the UI about the subagent conversation.
	// This ensures the sidebar shows the subagent even if it's a newly created conversation.
	go r.notifySubagentConversation(ctx, conversationID)

	// Run new-conversation hook for newly created subagent conversations.
	// We detect "new" by checking if the manager already exists.
	s.mu.Lock()
	_, alreadyActive := s.activeConversations[conversationID]
	s.mu.Unlock()
	if !alreadyActive {
		conv, convErr := s.db.GetConversationByID(ctx, conversationID)
		if convErr != nil {
			s.logger.Error("Failed to get conversation for new-conversation hook", "error", convErr, "conversationID", conversationID)
		} else if conv.ParentConversationID != nil {
			hookResult, hookErr := RunNewConversationHookIn(s.hooksDir, NewConversationHookInput{
				Prompt: prompt,
				Model:  modelID,
				Cwd:    derefString(conv.Cwd),
				Readonly: NewConversationReadonly{
					ConversationID: conversationID,
					IsSubagent:     true,
					ParentID:       *conv.ParentConversationID,
				},
			})
			if hookErr != nil {
				return "", fmt.Errorf("new-conversation hook: %w", hookErr)
			}
			if hookResult.Cwd != derefString(conv.Cwd) {
				if err := s.db.UpdateConversationCwd(ctx, conversationID, hookResult.Cwd); err != nil {
					s.logger.Error("Failed to update subagent cwd from hook", "error", err)
				}
			}
			if hookResult.Prompt != prompt {
				prompt = hookResult.Prompt
			}
			if hookResult.Model != modelID {
				if _, svcErr := s.llmManager.GetService(hookResult.Model); svcErr != nil {
					s.logger.Error("Hook returned unsupported model, keeping original", "hookModel", hookResult.Model, "error", svcErr)
				} else {
					modelID = hookResult.Model
				}
			}
		}
	}

	// Get or create conversation manager for the subagent, with incremented depth
	manager, err := s.getOrCreateSubagentConversationManager(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("failed to get conversation manager: %w", err)
	}

	// Apply the requested reasoning level (inherited from the parent when the
	// caller didn't specify one). Must happen before AcceptUserMessage, which
	// builds the loop from the conversation's stored options. Empty reasoning
	// is a no-op, leaving the subagent's existing/default level intact.
	//
	// Fail loudly rather than run the subagent at the wrong reasoning level
	// while reporting success: the level is part of what the caller asked for.
	if err := manager.SetThinkingLevel(ctx, reasoning); err != nil {
		return "", fmt.Errorf("failed to set subagent reasoning level %q: %w", reasoning, err)
	}

	// Use the parent's model if provided, otherwise fall back to server
	// default (preferring a ready model from the catalog; see
	// effectiveDefaultModel).
	if modelID == "" {
		modelID = s.effectiveDefaultModel(s.getModelList())
	}

	// Persist model on the subagent conversation record
	// UpdateConversationModel only sets the model if it's NULL, so this is safe for re-sends
	if modelID != "" {
		if err := s.db.UpdateConversationModel(ctx, conversationID, modelID); err != nil {
			s.logger.Warn("Failed to persist model on subagent conversation", "error", err, "conversationID", conversationID)
		}
	}

	// Get LLM service
	llmService, err := s.llmManager.GetService(modelID)
	if err != nil {
		return "", fmt.Errorf("failed to get LLM service: %w", err)
	}

	// Create user message
	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: prompt}},
	}

	deadline := time.Now().Add(timeout)

	// Sending to a busy subagent must NOT interrupt its current turn. We used
	// to CancelConversation here, which discarded the subagent's in-flight
	// work AND mis-reported the cancellation to the parent as a completion.
	// Now we leave the current turn running:
	//
	//   - wait=false: queue the message (delivered after the current turn via
	//     the normal pending-batch drain) and return immediately.
	//   - wait=true: register the synchronous-waiter slot first (so the
	//     current turn's completion does not fire a spurious async
	//     notification while we are blocked), then wait for the current turn
	//     to finish before sending our follow-up.
	if !wait {
		if manager.IsAgentWorking() {
			if err := manager.QueueMessage(ctx, s, modelID, userMessage); err != nil {
				return "", fmt.Errorf("failed to queue message for busy subagent: %w", err)
			}
			return fmt.Sprintf("Subagent is busy; message queued and will be processed after its current turn. Conversation ID: %s", conversationID), nil
		}
		if _, err := manager.AcceptUserMessage(ctx, llmService, modelID, userMessage); err != nil {
			return "", fmt.Errorf("failed to accept user message: %w", err)
		}
		return fmt.Sprintf("Subagent started processing. Conversation ID: %s", conversationID), nil
	}

	// wait=true. Register a synchronous-waiter slot on the subagent manager
	// BEFORE doing anything that could trigger a working→idle transition. The
	// slot suppresses the subagent's async onDone notification: while it is
	// held, this tool call is responsible for delivering the response. It
	// covers both the current in-flight turn (if any) finishing while we wait
	// and our own follow-up turn completing.
	manager.registerSubagentWaiter()

	// Let any in-flight turn finish on its own before we send (no cancel).
	if manager.IsAgentWorking() {
		s.logger.Info("Subagent is working; waiting for its current turn to finish before sending", "conversationID", conversationID)
		done, err := r.waitForIdle(ctx, manager, conversationID, deadline)
		if err != nil {
			r.endWait(manager, conversationID, false)
			return "", err
		}
		if !done {
			// Deadline hit while the current turn was still running. Return a
			// progress summary; the subagent keeps working and its eventual
			// completion is delivered asynchronously (endWait handles a finish
			// that was suppressed while we held the slot).
			r.endWait(manager, conversationID, false)
			return r.generateProgressSummary(ctx, conversationID, modelID, llmService)
		}
		// The in-flight turn finished while we held the slot, so its onDone was
		// suppressed and recorded as a pending suppressed-finish. That is the
		// turn we deliberately waited out; our follow-up supersedes it. Clear
		// the flag so a later timeout on OUR turn (in waitForResponse) doesn't
		// misattribute that stale finish to the follow-up and fire a premature
		// duplicate notification.
		manager.consumeSuppressedFinish()
	}

	// Accept the follow-up message (this starts a fresh turn).
	if _, err = manager.AcceptUserMessage(ctx, llmService, modelID, userMessage); err != nil {
		// Release the slot like any other non-delivery exit; endWait also
		// fires the async completion if a finish was suppressed while we
		// held it (symmetry with the timeout/cancel paths).
		r.endWait(manager, conversationID, false)
		return "", fmt.Errorf("failed to accept user message: %w", err)
	}

	// Wait for the agent to finish (or timeout). waitForResponse owns the
	// synchronous-waiter slot registered above and releases it on every exit.
	return r.waitForResponse(ctx, manager, conversationID, modelID, llmService, deadline)
}

// waitForIdle blocks until the subagent's current turn finishes (returns
// done=true), the deadline passes (done=false), or ctx is cancelled (err).
// It does not send anything; it only waits for an in-flight turn to end so a
// follow-up can be sent without interrupting work.
func (r *SubagentRunner) waitForIdle(ctx context.Context, manager *ConversationManager, conversationID string, deadline time.Time) (done bool, err error) {
	pollInterval := 500 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}
		if !manager.IsAgentWorking() {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// endWait releases this call's synchronous-waiter slot and, if a working→idle
// transition was suppressed while we held it but we are NOT delivering the
// result (delivered=false), fires the async completion notification that
// onDone would otherwise have fired. This covers the timeout and
// context-cancellation paths, where the tool's return value is not the
// subagent's final answer.
func (r *SubagentRunner) endWait(manager *ConversationManager, conversationID string, delivered bool) {
	if manager.finishSubagentWait(delivered) {
		go r.server.notifyParentSubagentDone(conversationID)
	}
}

func (r *SubagentRunner) waitForResponse(ctx context.Context, manager *ConversationManager, conversationID, modelID string, llmService llm.Service, deadline time.Time) (string, error) {
	s := r.server

	pollInterval := 500 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			r.endWait(manager, conversationID, false)
			return "", ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			// Timeout reached: we return only a progress summary, not the final
			// answer. Release the slot as a non-delivery; if the subagent has
			// already finished (its onDone was suppressed while we held the
			// slot), endWait fires the async completion so the parent still
			// learns the outcome. If it is still working, a later onDone fires
			// normally now that the slot is freed.
			r.endWait(manager, conversationID, false)
			return r.generateProgressSummary(ctx, conversationID, modelID, llmService)
		}

		// Check if agent is still working
		working, err := r.isAgentWorking(ctx, conversationID)
		if err != nil {
			r.endWait(manager, conversationID, false)
			return "", fmt.Errorf("failed to check agent status: %w", err)
		}

		if !working {
			// Agent is done and this wait=true call returns the final answer
			// synchronously, so we are the delivery path: release the slot as
			// delivered=true and suppress any async duplicate.
			r.endWait(manager, conversationID, true)
			return r.getLastAssistantResponse(ctx, conversationID)
		}

		// Wait before polling again
		select {
		case <-ctx.Done():
			r.endWait(manager, conversationID, false)
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}

		// Don't hog the conversation manager mutex
		s.mu.Lock()
		if mgr, ok := s.activeConversations[conversationID]; ok {
			mgr.Touch()
		}
		s.mu.Unlock()
	}
}

func (r *SubagentRunner) isAgentWorking(ctx context.Context, conversationID string) (bool, error) {
	s := r.server

	// Get the conversation manager - it tracks the working state
	s.mu.Lock()
	mgr, ok := s.activeConversations[conversationID]
	s.mu.Unlock()

	if !ok {
		// No active manager means the agent is not working
		return false, nil
	}

	return mgr.IsAgentWorking(), nil
}

func (r *SubagentRunner) getLastAssistantResponse(ctx context.Context, conversationID string) (string, error) {
	return r.server.lastAgentText(ctx, conversationID)
}

// generateProgressSummary makes a non-conversation LLM call to summarize the subagent's progress.
// This is called when the timeout is reached and the subagent is still working.
func (r *SubagentRunner) generateProgressSummary(ctx context.Context, conversationID, modelID string, llmService llm.Service) (string, error) {
	s := r.server

	// Get the conversation messages
	var messages []generated.Message
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		messages, err = q.ListMessages(ctx, conversationID)
		return err
	})
	if err != nil {
		s.logger.Error("Failed to get messages for progress summary", "error", err)
		return "[Subagent is still working (timeout reached). Failed to generate progress summary.]", nil
	}

	if len(messages) == 0 {
		return "[Subagent is still working (timeout reached). No messages yet.]", nil
	}

	// Build a summary of the conversation for the LLM
	conversationSummary := r.buildConversationSummary(messages)

	// Make a non-conversation LLM call to summarize progress
	summaryPrompt := `You are summarizing the current progress of a subagent task for a parent agent.

The subagent was given a task and has been working on it, but the timeout was reached before it completed.
Below is the conversation history showing what the subagent has done so far.

Please provide a brief, actionable summary (2-4 sentences) that tells the parent agent:
1. What the subagent has accomplished so far
2. What it appears to be currently working on
3. Whether it seems to be making progress or stuck

Conversation history:
` + conversationSummary + `

Provide your summary now:`

	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{{Type: llm.ContentTypeText, Text: summaryPrompt}},
			},
		},
	}

	// Use a short timeout for the summary call
	summaryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := llmService.Do(summaryCtx, req)
	if err != nil {
		s.logger.Error("Failed to generate progress summary via LLM", "error", err)
		return "[Subagent is still working (timeout reached). Failed to generate progress summary.]", nil
	}

	// Extract the summary text
	var summaryText string
	for _, content := range resp.Content {
		if content.Type == llm.ContentTypeText && content.Text != "" {
			summaryText = content.Text
			break
		}
	}

	if summaryText == "" {
		return "[Subagent is still working (timeout reached). No summary available.]", nil
	}

	return fmt.Sprintf("[Subagent is still working (timeout reached). Progress summary:]\n%s", summaryText), nil
}

// buildConversationSummary creates a text summary of the conversation messages for the LLM.
func (r *SubagentRunner) buildConversationSummary(messages []generated.Message) string {
	var sb strings.Builder

	for _, msg := range messages {
		// Skip system messages
		if msg.Type == "system" {
			continue
		}

		if msg.LlmData == nil {
			continue
		}

		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			continue
		}

		roleStr := "User"
		if llmMsg.Role == llm.MessageRoleAssistant {
			roleStr = "Assistant"
		}

		for _, content := range llmMsg.Content {
			switch content.Type {
			case llm.ContentTypeText:
				if content.Text != "" {
					// Truncate very long text
					text := content.Text
					if len(text) > 500 {
						text = text[:500] + "...[truncated]"
					}
					sb.WriteString(fmt.Sprintf("[%s]: %s\n\n", roleStr, text))
				}
			case llm.ContentTypeToolUse:
				// Truncate tool input if long
				inputStr := string(content.ToolInput)
				if len(inputStr) > 200 {
					inputStr = inputStr[:200] + "...[truncated]"
				}
				sb.WriteString(fmt.Sprintf("[%s used tool %s]: %s\n\n", roleStr, content.ToolName, inputStr))
			case llm.ContentTypeToolResult:
				// Summarize tool results
				resultText := ""
				for _, r := range content.ToolResult {
					if r.Type == llm.ContentTypeText && r.Text != "" {
						resultText = r.Text
						break
					}
				}
				if len(resultText) > 300 {
					resultText = resultText[:300] + "...[truncated]"
				}
				errorStr := ""
				if content.ToolError {
					errorStr = " (error)"
				}
				sb.WriteString(fmt.Sprintf("[Tool result%s]: %s\n\n", errorStr, resultText))
			}
		}
	}

	// Limit total size
	result := sb.String()
	if len(result) > 8000 {
		// Keep the last 8000 chars (most recent activity)
		result = "...[earlier messages truncated]...\n" + result[len(result)-8000:]
	}

	return result
}

// notifySubagentConversation fetches the subagent conversation and publishes it
// to all SSE streams so the UI can update the sidebar.
func (r *SubagentRunner) notifySubagentConversation(ctx context.Context, conversationID string) {
	s := r.server

	// Fetch the conversation from the database
	var conv generated.Conversation
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		conv, err = q.GetConversation(ctx, conversationID)
		return err
	})
	if err != nil {
		s.logger.Error("Failed to get subagent conversation for notification", "error", err, "conversationID", conversationID)
		return
	}

	// Only notify if this is actually a subagent (has parent)
	if conv.ParentConversationID == nil {
		return
	}

	// Publish the subagent conversation to all active streams
	s.publishConversationListUpdate(ConversationListUpdate{
		Type:         "update",
		Conversation: &conv,
	})

	s.logger.Debug("Notified UI about subagent conversation",
		"conversationID", conversationID,
		"parentID", *conv.ParentConversationID,
		"slug", conv.Slug)
}

// notifyParentSubagentDone enqueues a synthetic tool_use/tool_result pair
// onto the parent conversation's pending-batch queue when a subagent
// finishes, so the parent agent knows to check the results. Inspired by
// boldsoftware/shelley#200.
//
// All scheduling — wait for the current turn to end, wait for distillation
// setup to complete, cooperate with user-typed messages — is handled by the
// same drainPendingMessages path that user messages already use. We just
// drop a batch onto the queue and trust that machinery.
//
// Deciding WHETHER a notification is owed is no longer this function's job.
// The synchronous-waiter slot on the subagent manager (see
// ConversationManager.subagentWaitOwners and SetAgentWorking) is the single
// authority: this function is only ever invoked from the two paths that have
// already determined a notification is genuinely needed —
//  1. onDone, when the subagent finished with no synchronous waiter holding a
//     slot, and
//  2. SubagentRunner.endWait, when a synchronous waiter gave up (timeout /
//     cancellation) without delivering the result it suppressed.
//
// Both are keyed by the immutable conversation ID, so the old, slug-fragile
// history parsing that duplicated wait=true responses is gone.
func (s *Server) notifyParentSubagentDone(subagentConversationID string) {
	ctx := context.Background()

	var conv generated.Conversation
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		conv, err = q.GetConversation(ctx, subagentConversationID)
		return err
	})
	if err != nil || conv.ParentConversationID == nil {
		return
	}

	parentID := *conv.ParentConversationID
	slug := "unknown"
	if conv.Slug != nil {
		slug = *conv.Slug
	}

	// Get or (re)create the parent's manager. The parent may have been
	// evicted from activeConversations by the periodic Cleanup while it sat
	// idle (or blocked — pre-fix — inside this very subagent's tool call)
	// waiting for the subagent to finish. Bailing out here silently dropped
	// the completion and the parent hung until the user typed something.
	// getOrCreateConversationManager hydrates from the DB, and the
	// pending-batch drain below re-establishes the loop, mirroring how a
	// user message wakes a parked conversation.
	parentManager, err := s.getOrCreateConversationManager(ctx, parentID, "")
	if err != nil {
		s.logger.Error("Failed to get parent manager for subagent-done notification",
			"parent", parentID, "subagent", subagentConversationID, "error", err)
		return
	}

	parentManager.mu.Lock()
	parentModelID := parentManager.modelID
	parentManager.mu.Unlock()

	response, err := s.lastAgentText(ctx, subagentConversationID)
	if err != nil || response == "" {
		response = "(no textual response)"
	}
	// Cap the subagent text we splice into the parent's history. A runaway
	// subagent reply shouldn't dominate the parent's context window; the
	// parent can always read the full subagent conversation via the
	// dedicated subagent view.
	if len(response) > 500 {
		response = response[:500] + "..."
	}

	// Splice in a synthetic tool_use/tool_result pair as if the parent had
	// just called the subagent tool with wait=true. This gives the LLM the
	// information it needs in the tool_result channel (weaker prompt
	// authority than user-voice), avoids the extra round trip that a
	// "please call the subagent tool" nudge would require, and the result
	// is clearly attributed to the subagent.
	toolUseID := fmt.Sprintf("sa_done_%s", uuid.New().String())
	toolInput, _ := json.Marshal(map[string]any{
		"slug":   slug,
		"prompt": "(asynchronous completion notification)",
		"wait":   true,
	})
	assistantMsg := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{{
			Type:      llm.ContentTypeToolUse,
			ID:        toolUseID,
			ToolName:  "subagent",
			ToolInput: toolInput,
			Display: claudetool.SubagentDisplayData{
				Slug:           slug,
				ConversationID: subagentConversationID,
			},
		}},
	}
	toolResultMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type:      llm.ContentTypeToolResult,
			ToolUseID: toolUseID,
			ToolResult: []llm.Content{{
				Type: llm.ContentTypeText,
				Text: fmt.Sprintf(
					"[Subagent %q has finished asynchronously. "+
						"This tool call was synthesized by the system to surface the "+
						"result; you did not invoke it yourself. Please briefly acknowledge "+
						"the subagent's outcome to the user and decide whether any follow-up "+
						"work is needed.]\n\nSubagent response:\n%s",
					slug, response,
				),
			}},
			Display: claudetool.SubagentDisplayData{
				Slug:           slug,
				ConversationID: subagentConversationID,
			},
		}},
	}

	modelID := parentModelID
	if modelID == "" {
		modelID = s.defaultModel
	}

	// Enqueue onto the parent's pending-batch queue. drainPendingMessages
	// handles persistence, loop start/wake, and serialization with both
	// distillation and other queued work. We don't need to read or touch
	// the parent's agentWorking/distilling/loop state ourselves — the queue
	// is the single point of coordination.
	parentManager.EnqueueSubagentDone(s, modelID, subagentConversationID, assistantMsg, toolResultMsg)
	s.logger.Info("Queued subagent-done notification for parent", "subagent", slug, "parent", parentID)
}

// lastAgentText returns the concatenated text content of the most recent
// type=agent message in a conversation — specifically the latest such
// message, skipping non-agent rows (gitinfo, user, tool, system, error)
// that may have been appended after it.
//
// In particular gitinfo messages carry assistant-role llm_data and would
// otherwise be returned as "the subagent's response" when they're really
// user-visible git state notes Shelley itself injected.
//
// If the latest agent message has no text content (e.g. it's a pure
// tool_use), returns "" — we don't walk further back, because earlier
// agent turns are stale: their text was already conveyed via prior
// notifications or tool returns.
func (s *Server) lastAgentText(ctx context.Context, conversationID string) (string, error) {
	msgs, err := s.db.ListMessages(ctx, conversationID)
	if err != nil {
		return "", err
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Type != string(db.MessageTypeAgent) {
			continue
		}
		if m.LlmData == nil {
			return "", nil
		}
		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &llmMsg); err != nil {
			return "", err
		}
		var texts []string
		for _, content := range llmMsg.Content {
			if content.Type == llm.ContentTypeText && content.Text != "" {
				texts = append(texts, content.Text)
			}
		}
		return strings.Join(texts, "\n"), nil
	}
	return "", nil
}

// Ensure SubagentRunner implements claudetool.SubagentRunner.
var _ claudetool.SubagentRunner = (*SubagentRunner)(nil)

// handleGetSubagents returns the list of subagents for a conversation.
func (s *Server) handleGetSubagents(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	subagents, err := s.db.GetSubagents(r.Context(), conversationID)
	if err != nil {
		s.logger.Error("Failed to get subagents", "conversationID", conversationID, "error", err)
		http.Error(w, "Failed to get subagents", 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(subagents)
}
