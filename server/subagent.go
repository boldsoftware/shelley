package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

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
func (r *SubagentRunner) RunSubagent(ctx context.Context, conversationID, prompt, modelID, reasoning string) (string, error) {
	s := r.server
	conv, convErr := s.db.GetConversationByID(ctx, conversationID)
	if convErr == nil {
		if _, ok := db.ManagedBtwReaderIdentity(*conv); ok {
			return "", fmt.Errorf("BTW reader %s cannot be used as delegated subagent work", conversationID)
		}
	}

	// Without a requested model, an existing subagent keeps its own model
	// and a new one inherits the parent's, then the server default
	// (preferring a ready model from the catalog; see effectiveDefaultModel).
	if modelID == "" {
		var err error
		modelID, err = s.inheritedSubagentModel(ctx, conversationID)
		if err != nil {
			return "", err
		}
	}

	// Run new-conversation hook for newly created subagent conversations.
	// We detect "new" by checking if the manager already exists.
	s.mu.Lock()
	_, alreadyActive := s.activeConversations[conversationID]
	s.mu.Unlock()
	if !alreadyActive {
		if convErr != nil {
			s.logger.Error("Failed to get conversation for new-conversation hook", "error", convErr, "conversationID", conversationID)
		} else if isManagedChild(*conv) {
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

	manager, err := s.getOrCreateConversationManager(ctx, conversationID, "")
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
	// New delegated work after Stop can report back to its parent again.
	if convErr == nil && isDelegatedSubagent(*conv) {
		s.resumeParentCompletions(*conv.ParentConversationID)
	}

	// The subagent's manager marks the turn this message starts or joins as
	// the parent's, so an unreported turn end is reported for it.
	ctx = contextWithParentDelegation(ctx)

	// A busy subagent receives the message at its current turn's next LLM
	// round, without interrupting the turn (see InjectMessage).
	if manager.IsAgentWorking() {
		if err := manager.InjectMessage(ctx, s, modelID, userMessage); err != nil {
			return "", fmt.Errorf("failed to send message to busy subagent: %w", err)
		}
		return "message sent into the subagent's current turn; it reports back with message_parent.", nil
	}
	if _, err := manager.AcceptUserMessage(ctx, llmService, modelID, userMessage); err != nil {
		return "", fmt.Errorf("failed to accept user message: %w", err)
	}
	return "message sent; the subagent works in the background and reports back with message_parent.", nil
}

// inheritedSubagentModel returns the model of the subagent conversation, else
// of its parent, else the server default.
func (s *Server) inheritedSubagentModel(ctx context.Context, conversationID string) (string, error) {
	conv, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		return "", err
	}
	if conv.Model != nil && *conv.Model != "" {
		return *conv.Model, nil
	}
	if conv.ParentConversationID != nil {
		parent, err := s.db.GetConversationByID(ctx, *conv.ParentConversationID)
		if err != nil {
			return "", err
		}
		if parent.Model != nil && *parent.Model != "" {
			return *parent.Model, nil
		}
	}
	return s.effectiveDefaultModel(s.getModelList()), nil
}

// MessageParent implements claudetool.ParentMessenger.
func (r *SubagentRunner) MessageParent(ctx context.Context, conversationID, text string, final bool) error {
	s := r.server
	conv, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		return fmt.Errorf("load conversation: %w", err)
	}
	if err := s.messageParent(ctx, *conv, text); err != nil {
		return err
	}
	s.mu.Lock()
	manager := s.activeConversations[conversationID]
	s.mu.Unlock()
	if manager != nil {
		manager.noteMessageParent(final)
	}
	return nil
}

// parentDelegationContextKey marks a message RunSubagent sends to a subagent.
type parentDelegationContextKey struct{}

func contextWithParentDelegation(ctx context.Context) context.Context {
	return context.WithValue(ctx, parentDelegationContextKey{}, true)
}

func isParentDelegation(ctx context.Context) bool {
	delegated, _ := ctx.Value(parentDelegationContextKey{}).(bool)
	return delegated
}

// turnEndReportLimit caps the reply a turn-end report carries; the parent
// can read the rest in the subagent's conversation.
const turnEndReportLimit = 4000

// reportUnreportedTurnEnd sends a delegated subagent's parent the final
// report the subagent owed it but did not send: the turn the parent started
// ended with a plain reply (or an error) instead of
// message_parent(end_turn=true). Without it the parent is never woken. The
// report goes through messageParent, as message_parent's would.
//
// Not reported: a turn the user drove in the subagent's conversation, a
// turn that sent its final report, a turn only paused for a background job
// (its exit notice resumes the turn; the report comes when that one ends),
// a cancelled turn, and a turn whose parent was stopped.
func (s *Server) reportUnreportedTurnEnd(ctx context.Context, manager *ConversationManager, end llm.Message, kind db.MessageType) {
	ctx = context.WithoutCancel(ctx)
	conversationID := manager.conversationID
	// An excluded end-of-turn row is a marker, not a reply: the one
	// message_parent(end_turn=true) writes after its report, or the raw
	// response recorded before a refusal or truncation error row, which
	// ends the turn next and is reported then.
	if end.ExcludedFromContext {
		if manager.delegatedTurnFinalReported() {
			manager.endDelegatedTurn(false)
		}
		return
	}
	cancelled := messageText(end) == "[Operation cancelled]"
	paused := false
	if !cancelled {
		jobs, err := s.runningBackgroundJobsOf(ctx, conversationID)
		if err != nil {
			s.logger.Error("Failed to list a subagent's background jobs at turn end", "conversationID", conversationID, "error", err)
		}
		paused = len(jobs) > 0
	}
	state := manager.endDelegatedTurn(paused)
	if cancelled || paused || !state.parentStarted || state.finalReported {
		return
	}
	conv, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to load a subagent at turn end", "conversationID", conversationID, "error", err)
		return
	}
	if !isDelegatedSubagent(*conv) {
		return
	}
	parentID := *conv.ParentConversationID
	s.mu.Lock()
	stopped := s.stoppedParents[parentID]
	s.mu.Unlock()
	if stopped {
		s.logger.Info("Subagent turn ended without a report; its parent is stopped", "conversationID", conversationID, "parentID", parentID)
		return
	}
	text := turnEndReport(derefString(conv.Slug), end, kind, state)
	if err := s.messageParent(ctx, *conv, text); err != nil {
		s.logger.Error("Failed to report a subagent's turn end to its parent", "conversationID", conversationID, "parentID", parentID, "error", err)
		return
	}
	s.logger.Info("Reported a subagent's unreported turn end to its parent", "conversationID", conversationID, "parentID", parentID, "kind", kind)
}

// turnEndReport is the report sent for a delegated turn that ended without
// one: a line saying so, then the subagent's last reply or error.
func turnEndReport(slug string, end llm.Message, kind db.MessageType, state delegatedTurnState) string {
	var b strings.Builder
	switch {
	case kind == db.MessageTypeError:
		fmt.Fprintf(&b, "[Subagent %q ended its turn with an error and no final report; the error follows.]", slug)
	case state.progressReports > 0:
		fmt.Fprintf(&b, "[Subagent %q ended its turn without a final report; its last message_parent was a progress report. Its last reply follows.]", slug)
	default:
		fmt.Fprintf(&b, "[Subagent %q ended its turn without a final report; its last reply follows.]", slug)
	}
	text := strings.TrimSpace(messageText(end))
	if text == "" {
		text = "(the reply has no text; inspect the subagent's conversation)"
	}
	if len(text) > turnEndReportLimit {
		cut := turnEndReportLimit
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + fmt.Sprintf("\n[... truncated; %d more characters in the subagent's conversation]", utf8.RuneCountInString(text[cut:]))
	}
	b.WriteString("\n")
	b.WriteString(text)
	return b.String()
}

// isDelegatedSubagent reports whether conv is a subagent created by the
// subagent tool, as opposed to a /btw reader or an internal worker
// (transcription, commit tour).
func isDelegatedSubagent(conv generated.Conversation) bool {
	return conversationRoleOf(conv) == roleSubagent
}

// messageParent queues text in the parent of the delegated subagent conv. It
// is stored as a user row whose user_data names the subagent, so the parent
// model sees it wrapped in <subagent_message> and the UI attributes it. A busy
// parent takes it at its next LLM request; an idle parent starts a turn.
func (s *Server) messageParent(ctx context.Context, conv generated.Conversation, text string) error {
	if !isDelegatedSubagent(conv) {
		return fmt.Errorf("conversation %s is not a subagent", conv.ConversationID)
	}
	s.mu.Lock()
	stopped := s.stoppedParents[*conv.ParentConversationID]
	s.mu.Unlock()
	if stopped {
		return fmt.Errorf("parent conversation %s was stopped", *conv.ParentConversationID)
	}
	parent, err := s.getOrCreateConversationManager(ctx, *conv.ParentConversationID, "")
	if err != nil {
		return fmt.Errorf("load parent conversation: %w", err)
	}
	parent.mu.Lock()
	modelID := parent.modelID
	parent.mu.Unlock()
	ctx = contextWithTurnUserData(ctx, senderMessageUserData{
		SenderConversationID: conv.ConversationID,
		SenderSlug:           derefString(conv.Slug),
		SenderRelationship:   senderRelationshipSubagent,
		Text:                 text,
	})
	return parent.InjectMessage(ctx, s, modelID, llm.UserStringMessage(text))
}

// Ensure SubagentRunner implements claudetool.SubagentRunner.
var (
	_ claudetool.SubagentRunner  = (*SubagentRunner)(nil)
	_ claudetool.ParentMessenger = (*SubagentRunner)(nil)
)

// cancelSubagentTree cancels the active turns of all subagent conversations
// beneath parentID (children, grandchildren, ...). When the user cancels a
// conversation they mean "stop all of this work", including work delegated to
// subagents — leaving those running would waste tokens on results nobody will
// consume (the parent's tool call awaiting them was just torn down).
//
// Only actively-working subagents are cancelled: an idle manager may still
// hold a hydrated loop, and CancelConversation would record a spurious
// "[Operation cancelled]" end-of-turn message on a turn that already
// finished.
func (s *Server) cancelSubagentTree(ctx context.Context, parentID string) {
	visited := map[string]bool{parentID: true}
	queue := []string{parentID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]

		children, err := s.db.GetSubagents(ctx, id)
		if err != nil {
			s.logger.Error("Failed to list subagents for cancellation", "conversationID", id, "error", err)
			continue
		}
		for _, child := range children {
			if !isManagedChild(child) {
				continue
			}
			kind := db.ParseConversationOptions(child.ConversationOptions).Kind
			if isBtwReader(child) || kind == commitTourKind {
				// Detached work is independent of parent-turn cancellation. Do not
				// cancel it or traverse through it.
				continue
			}
			if visited[child.ConversationID] {
				continue
			}
			visited[child.ConversationID] = true
			queue = append(queue, child.ConversationID)

			s.mu.Lock()
			mgr, active := s.activeConversations[child.ConversationID]
			s.mu.Unlock()
			if !active || !mgr.IsAgentWorking() {
				continue
			}
			if err := mgr.CancelConversation(ctx); err != nil {
				s.logger.Error("Failed to cancel subagent conversation", "conversationID", child.ConversationID, "parent", id, "error", err)
				continue
			}
			s.logger.Info("Cancelled subagent conversation", "conversationID", child.ConversationID, "parent", id)
		}
	}
}

// handleGetSubagents returns the list of subagents for a conversation.
func (s *Server) handleGetSubagents(w http.ResponseWriter, r *http.Request, conversationID string) {
	subagents, err := s.db.GetSubagents(r.Context(), conversationID)
	if err != nil {
		s.logger.Error("Failed to get subagents", "conversationID", conversationID, "error", err)
		http.Error(w, "Failed to get subagents", 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(subagents)
}
