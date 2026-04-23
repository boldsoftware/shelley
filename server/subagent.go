package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"shelley.exe.dev/claudetool"
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
func (r *SubagentRunner) RunSubagent(ctx context.Context, conversationID, prompt string, wait bool, timeout time.Duration, modelID string) (string, error) {
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
			hookResult := RunNewConversationHook(NewConversationHookInput{
				Prompt: prompt,
				Model:  modelID,
				Cwd:    derefString(conv.Cwd),
				Readonly: NewConversationReadonly{
					ConversationID: conversationID,
					IsSubagent:     true,
					ParentID:       *conv.ParentConversationID,
				},
			})
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

	// Use the parent's model if provided, otherwise fall back to server default
	if modelID == "" {
		modelID = s.defaultModel
	}
	if modelID == "" && s.predictableOnly {
		modelID = "predictable"
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

	// If the subagent is currently working, stop it first before sending new message
	if manager.IsAgentWorking() {
		s.logger.Info("Subagent is working, stopping before sending new message", "conversationID", conversationID)
		if err := manager.CancelConversation(ctx); err != nil {
			s.logger.Error("Failed to cancel subagent conversation", "error", err)
			// Continue anyway - we still want to send the new message
		}
		// Re-hydrate the manager after cancellation
		if err := manager.Hydrate(ctx); err != nil {
			return "", fmt.Errorf("failed to hydrate after cancellation: %w", err)
		}
	}

	// Create user message
	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: prompt}},
	}

	// Accept the user message (this starts processing)
	_, err = manager.AcceptUserMessage(ctx, llmService, modelID, userMessage)
	if err != nil {
		return "", fmt.Errorf("failed to accept user message: %w", err)
	}

	if !wait {
		return fmt.Sprintf("Subagent started processing. Conversation ID: %s", conversationID), nil
	}

	// Wait for the agent to finish (or timeout)
	return r.waitForResponse(ctx, conversationID, modelID, llmService, timeout)
}

func (r *SubagentRunner) waitForResponse(ctx context.Context, conversationID, modelID string, llmService llm.Service, timeout time.Duration) (string, error) {
	s := r.server

	deadline := time.Now().Add(timeout)
	pollInterval := 500 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			// Timeout reached - generate a progress summary
			return r.generateProgressSummary(ctx, conversationID, modelID, llmService)
		}

		// Check if agent is still working
		working, err := r.isAgentWorking(ctx, conversationID)
		if err != nil {
			return "", fmt.Errorf("failed to check agent status: %w", err)
		}

		if !working {
			// Agent is done, get the last message
			return r.getLastAssistantResponse(ctx, conversationID)
		}

		// Wait before polling again
		select {
		case <-ctx.Done():
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
	s := r.server

	// Get the latest message
	msg, err := s.db.GetLatestMessage(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("failed to get latest message: %w", err)
	}

	// Extract text content
	if msg.LlmData == nil {
		return "", nil
	}

	var llmMsg llm.Message
	if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
		return "", fmt.Errorf("failed to parse message: %w", err)
	}

	var texts []string
	for _, content := range llmMsg.Content {
		if content.Type == llm.ContentTypeText && content.Text != "" {
			texts = append(texts, content.Text)
		}
	}

	return strings.Join(texts, "\n"), nil
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
