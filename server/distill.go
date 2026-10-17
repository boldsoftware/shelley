package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"unicode/utf8"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// performDistillation and its default-strategy helpers were removed when we
// consolidated on compaction (see performPiDistillation in distill_pi.go).

func (s *Server) insertDistillError(ctx context.Context, conversationID, errMsg string) {
	s.insertDistillStatus(ctx, conversationID, "error")

	// Insert an error message so the user knows what happened
	errorMessage := llm.Message{
		Role:      llm.MessageRoleAssistant,
		ErrorType: llm.ErrorTypeLLMRequest,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: errMsg},
		},
	}
	if err := s.recordMessage(ctx, conversationID, errorMessage, llm.Usage{}, nil); err != nil {
		s.logger.Error("Failed to record distill error message", "conversationID", conversationID, "error", err)
	}
}

// terminalDistillStatusMessage builds a NEW immutable status message carrying
// the terminal distill_status ("complete" or "error"). It copies the
// descriptive fields (source_slug, distill_method, new_generation) from the
// in_progress status message so the terminal message renders the same way.
// Messages are immutable after creation, so instead of mutating the in_progress
// message we emit a second message; the UI collapses the pair to just the
// terminal one. The message is ExcludedFromContext (a UI-only status marker).
// Returns false if no in_progress status message exists.
func (s *Server) terminalDistillStatusMessage(ctx context.Context, conversationID, status string) (llm.Message, map[string]string, bool) {
	messages, err := s.db.ListMessages(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to list messages", "conversationID", conversationID, "error", err)
		return llm.Message{}, nil, false
	}

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.UserData == nil {
			continue
		}
		var userData map[string]string
		if err := json.Unmarshal([]byte(*msg.UserData), &userData); err != nil {
			continue
		}
		if userData["distill_status"] == "" {
			continue
		}
		terminalData := map[string]string{"distill_status": status}
		for _, k := range []string{"source_slug", "new_generation", "distill_method"} {
			if v := userData[k]; v != "" {
				terminalData[k] = v
			}
		}
		return llm.Message{
			Role:                llm.MessageRoleAssistant,
			Content:             []llm.Content{{Type: llm.ContentTypeText, Text: "Distillation " + status}},
			ExcludedFromContext: true,
		}, terminalData, true
	}
	return llm.Message{}, nil, false
}

// insertDistillStatus inserts a NEW immutable terminal status message
// ("complete" or "error") and broadcasts it to SSE subscribers via the normal
// new-message path, so it streams to clients and lands in cache with a fresh
// sequence_id. The earlier in_progress message is left untouched.
func (s *Server) insertDistillStatus(ctx context.Context, conversationID, status string) {
	message, userData, ok := s.terminalDistillStatusMessage(ctx, conversationID, status)
	if !ok {
		return
	}
	if err := s.recordMessage(ctx, conversationID, message, llm.Usage{}, nil, userData); err != nil {
		s.logger.Error("Failed to insert distill status", "conversationID", conversationID, "error", err)
	}
}

// truncateUTF8 truncates s to approximately maxBytes without splitting a UTF-8 character.
// If truncation occurs, "..." is appended.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 0 {
		return "..."
	}
	// Walk backward from maxBytes to find a valid rune boundary.
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes] + "..."
}

// distillMethodCompact is the single conversation-shrinking strategy: it uses
// the compaction algorithm (modeled on the pi coding agent) to summarize older
// messages and keep recent ones verbatim. distillMethodDefault is the legacy
// "default" briefing strategy value, retained only so the endpoint keeps
// accepting it for compatibility (it is coerced to compaction).
const (
	distillMethodDefault = "default"
	distillMethodCompact = "compact"
)

// steeringSection formats optional user-provided guidance that steers what the
// distillation/summary should emphasize. Appended to the summarizer's input.
func steeringSection(instructions string) string {
	return "\n\n## User Guidance\n\nThe user provided the following guidance on what to preserve or emphasize in this distillation. Follow it closely:\n\n" + instructions
}

func (s *Server) runDistillNewGeneration(ctx context.Context, conversationID, sourceSlug, modelID, instructions string, sourceGeneration int64, sourceTurnInterrupted bool, messages []generated.Message) {
	defer func() {
		s.mu.Lock()
		manager, ok := s.activeConversations[conversationID]
		s.mu.Unlock()
		if ok {
			manager.SetDistilling(false)
			manager.drainPendingMessages(s)
		}
	}()

	s.performPiDistillation(ctx, conversationID, sourceSlug, modelID, instructions, sourceGeneration, sourceTurnInterrupted, messages)
	// The new generation's messages carry no usage data yet, so the UI's
	// context-usage bar would keep showing the pre-distillation size until the
	// next agent turn. Broadcast an estimate of the new generation's context
	// size so the bar resets immediately.
	s.broadcastEstimatedContextSize(ctx, conversationID)
	go s.notifySubscribers(ctx, conversationID)
}

// broadcastEstimatedContextSize estimates the latest generation's context
// window usage (char/4 heuristic over context-eligible messages) and pushes it
// to stream subscribers. Used right after distillation, when the new
// generation has no real usage data yet, so the UI bar resets instead of
// showing the stale pre-distillation value.
func (s *Server) broadcastEstimatedContextSize(ctx context.Context, conversationID string) {
	s.mu.Lock()
	manager, ok := s.activeConversations[conversationID]
	s.mu.Unlock()
	if !ok {
		return
	}

	messages, err := s.db.ListMessages(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to list messages for context estimate", "conversationID", conversationID, "error", err)
		return
	}

	// Estimate over the conversation's CURRENT generation, not the max
	// generation present in the table: a rolled-back compaction leaves
	// abandoned higher-generation rows behind, and estimating those would
	// show a near-empty context for an intact conversation.
	conv, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to load conversation for context estimate", "conversationID", conversationID, "error", err)
		return
	}
	latestGen := conv.CurrentGeneration

	var contextRows []generated.Message
	for _, m := range messages {
		if m.Generation == latestGen && !m.ExcludedFromContext {
			contextRows = append(contextRows, m)
		}
	}
	items, system, err := manager.contextItems(contextRows)
	if err != nil {
		s.logger.Error("Failed to build context for estimate", "conversationID", conversationID, "error", err)
		return
	}
	var estimate int64
	for _, sc := range system {
		estimate += int64(estimatePiMessageTokens(llm.Message{Content: []llm.Content{{Type: llm.ContentTypeText, Text: sc.Text}}}))
	}
	for _, it := range items {
		estimate += int64(estimatePiMessageTokens(it.message))
	}
	if estimate <= 0 {
		return
	}

	var conversation generated.Conversation
	if derr := s.db.Queries(ctx, func(q *generated.Queries) error {
		var qerr error
		conversation, qerr = q.GetConversation(ctx, conversationID)
		return qerr
	}); derr != nil {
		s.logger.Error("Failed to get conversation for context estimate", "conversationID", conversationID, "error", derr)
		return
	}

	manager.broadcastStream(StreamResponse{
		Conversation:      &conversation,
		ContextWindowSize: uint64(estimate),
	})
}

// DistillNewGenerationRequest represents the request to distill into the same conversation's next generation.
type DistillNewGenerationRequest struct {
	SourceConversationID string `json:"source_conversation_id"`
	Model                string `json:"model,omitempty"`
	Cwd                  string `json:"cwd,omitempty"`
	// Method selects the distillation strategy: "default" (single briefing
	// message) or "compact" (summarize-old + keep-recent-verbatim).
	// Empty defaults to "default".
	Method string `json:"method,omitempty"`
	// Instructions is optional free-form user guidance that steers what the
	// distillation should preserve or emphasize.
	Instructions string `json:"instructions,omitempty"`
}

// handleDistillNewGeneration handles POST /api/conversations/distill-new-generation.
// It keeps the visible conversation, marks old messages as previous generation,
// and inserts the distillation into the next generation.
func (s *Server) handleDistillNewGeneration(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req DistillNewGenerationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if req.SourceConversationID == "" {
		http.Error(w, "source_conversation_id is required", http.StatusBadRequest)
		return
	}

	// We have consolidated on compaction as the single strategy. The legacy
	// "default" distillation method is retained only for request
	// compatibility: any method value (including empty or "default") is
	// coerced to compaction. Reject only clearly bogus method strings.
	if req.Method != "" && req.Method != distillMethodDefault && req.Method != distillMethodCompact {
		http.Error(w, fmt.Sprintf("unknown distill method %q", req.Method), http.StatusBadRequest)
		return
	}
	method := distillMethodCompact

	sourceConv, err := s.db.GetConversationByID(ctx, req.SourceConversationID)
	if err != nil {
		s.logger.Error("Failed to get source conversation", "conversationID", req.SourceConversationID, "error", err)
		http.Error(w, "Source conversation not found", http.StatusNotFound)
		return
	}
	// Capture the generation we are distilling FROM, before incrementing.
	// The pi strategy needs it to select the right messages to copy/summarize.
	sourceGeneration := sourceConv.CurrentGeneration
	sourceTurnInterrupted := sourceConv.TurnInterrupted
	messages, err := s.db.ListMessages(ctx, req.SourceConversationID)
	if err != nil {
		s.logger.Error("Failed to get messages", "conversationID", req.SourceConversationID, "error", err)
		http.Error(w, "Failed to get messages", http.StatusInternalServerError)
		return
	}

	modelID := req.Model
	if modelID == "" && sourceConv.Model != nil {
		modelID = *sourceConv.Model
	}
	if modelID == "" {
		modelID = s.effectiveDefaultModel(s.getModelList())
	}
	// Validate before mutating: the model is force-written onto the
	// conversation below, so an unknown model would otherwise brick the
	// conversation (every subsequent chat rejects the stored model).
	if _, err := s.llmManager.GetService(modelID); err != nil {
		http.Error(w, fmt.Sprintf("unknown model %q: %v", modelID, err), http.StatusBadRequest)
		return
	}

	manager, err := s.getOrCreateConversationManager(ctx, req.SourceConversationID, "")
	if err != nil {
		s.internalError(w, "Failed to create conversation manager for distill-new-generation", err, "conversationID", req.SourceConversationID)
		return
	}
	// Acquire the distilling state before any mutation so a rejected
	// concurrent request has no side effects.
	if !manager.BeginDistillingSetup() {
		http.Error(w, "Distillation already in progress", http.StatusConflict)
		return
	}
	setupComplete := false
	defer func() {
		if !setupComplete {
			manager.SetDistilling(false)
		}
	}()

	if req.Cwd != "" && (sourceConv.Cwd == nil || *sourceConv.Cwd != req.Cwd) {
		if err := s.db.UpdateConversationCwd(ctx, req.SourceConversationID, req.Cwd); err != nil {
			s.internalError(w, "Failed to update cwd for new generation", err)
			return
		}
	}
	if sourceConv.Model == nil || *sourceConv.Model != modelID {
		_, err := manager.ApplySettings(ctx, s.getModelList(), func(cur ConversationSettings) (ConversationSettings, error) {
			if req.Model != "" && cur.Model != modelID && cur.Profile != "" {
				cur.Edited = true
			}
			cur.Model = modelID
			return cur, nil
		})
		if err != nil {
			s.internalError(w, "Failed to update model for new generation", err)
			return
		}
	}

	conversation, err := db.WithTxRes(s.db, ctx, func(q *generated.Queries) (generated.Conversation, error) {
		return q.IncrementConversationGeneration(ctx, req.SourceConversationID)
	})
	if err != nil {
		s.internalError(w, "Failed to increment generation", err, "conversationID", req.SourceConversationID)
		return
	}
	manager.ResetLoop()

	sourceSlug := "unknown"
	if sourceConv.Slug != nil {
		sourceSlug = *sourceConv.Slug
	}
	statusMsg, err := s.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: req.SourceConversationID,
		Type:           db.MessageTypeAgent,
		LLMData: llm.Message{
			Role:    llm.MessageRoleAssistant,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Distilling conversation…"}},
		},
		UserData: map[string]string{
			"distill_status": "in_progress",
			"source_slug":    sourceSlug,
			"new_generation": "true",
			"distill_method": method,
		},
		ExcludedFromContext: true,
	})
	if err != nil {
		// WithoutCancel: a client disconnect mid-setup must not strand the
		// conversation on the just-created empty generation.
		s.rollbackCompactionFailure(context.WithoutCancel(ctx), s.logger, req.SourceConversationID, "Compaction failed during setup", sourceGeneration, sourceTurnInterrupted)
		s.internalError(w, "Failed to create status message", err, "conversationID", req.SourceConversationID)
		return
	}
	go s.notifySubscribers(context.WithoutCancel(ctx), req.SourceConversationID, *statusMsg)

	if err := manager.Hydrate(ctx); err != nil {
		// WithoutCancel: a client disconnect mid-setup must not strand the
		// conversation on the just-created empty generation.
		s.rollbackCompactionFailure(context.WithoutCancel(ctx), s.logger, req.SourceConversationID, "Compaction failed during setup", sourceGeneration, sourceTurnInterrupted)
		s.internalError(w, "Failed to hydrate new generation", err, "conversationID", req.SourceConversationID)
		return
	}
	if fresh, ferr := s.db.GetConversationByID(ctx, req.SourceConversationID); ferr == nil {
		conversation = *fresh
	}
	if currentMessages, merr := s.db.ListMessages(ctx, req.SourceConversationID); merr == nil {
		for i := range currentMessages {
			msg := &currentMessages[i]
			if msg.Generation == conversation.CurrentGeneration && msg.Type == string(db.MessageTypeSystem) && msg.UserData == nil {
				go s.notifySubscribers(context.WithoutCancel(ctx), req.SourceConversationID, *msg)
			}
		}
	}
	go s.notifySubscribers(context.WithoutCancel(ctx), req.SourceConversationID)
	setupComplete = true
	manager.FinishDistillingSetup()

	ctxNoCancel := context.WithoutCancel(ctx)
	go func() {
		s.runDistillNewGeneration(ctxNoCancel, req.SourceConversationID, sourceSlug, modelID, req.Instructions, sourceGeneration, sourceTurnInterrupted, messages)
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":             "created",
		"conversation_id":    req.SourceConversationID,
		"current_generation": conversation.CurrentGeneration,
	})
}
