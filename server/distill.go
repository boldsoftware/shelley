package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

const distillSystemPrompt = `You are a conversation distillation engine for Shelley, an AI coding assistant.

You will receive a full conversation transcript between a user and Shelley. The transcript includes user messages, agent responses, tool calls (bash, patch, browser, keyword_search, etc.), and tool results.

Your job is to produce an OPERATIONAL DISTILLATION — not a narrative summary. The output will become the opening user message in a brand-new continuation conversation. It must give the new Shelley instance everything it needs to pick up the work seamlessly.

Write the distillation AS IF you are the user briefing a fresh Shelley instance. Use second person: "You were working on...", "You created...", "The approach is...".

## Output Format

Produce exactly this structure (no markdown code fences around the whole output, no meta-commentary):

This is a continuation of conversation "SLUG_HERE".

WRITE 2-6 SENTENCES HERE describing what was being worked on, what state things are in, and what the immediate next steps or open tasks are. Be concrete — name files, describe the current approach, note where things left off. This is a situational briefing, not a history. Write the sentences directly with no wrapper tags.

## Retained Facts

- fact 1
- fact 2
- ...

The "## Retained Facts" section IS part of the output. The instructions below are NOT part of the output.

Each fact bullet should be a single concrete, referenceable fact. Aim for 10-40 bullets depending on conversation length. Include:

- File paths and roles (full paths, what each file does)
- Decisions and rationale ("X because Y")
- Current task state (done, in progress, blocked, next)
- User preferences and corrections (style choices, explicit instructions)
- Specific values (URLs, ports, config paths, env vars, schemas, version numbers, commands)
- Error resolutions (problem + fix, not the debugging journey)
- Working directory and git state
- Dependencies and tooling
- Interfaces and contracts (signatures, API shapes, types)
- Constraints and gotchas (limitations, workarounds)

EXCISE: dead-end debugging (keep only final fix), verbose tool output (keep only findings), abandoned tangents (unless the reason matters), greetings/filler, already-resolved questions (keep only conclusions), redundant info, thinking blocks, intermediate file states that were later overwritten.

Compression: recent activity (~last 20%) gets more detail; older activity compresses to conclusions. Short conversations (< 20 messages) preserve more. Long conversations (> 100 messages) aggressively compress old activity. Total output: 500-2000 words. When in doubt, keep it.`

// performDistillation does the LLM call and inserts the distilled message.
// Returns the distilled text, or empty string on error (errors are logged and
// a distill error is inserted into the conversation).
func (s *Server) performDistillation(ctx context.Context, conversationID, sourceSlug, modelID, instructions string, messages []generated.Message) string {
	logger := s.logger.With("conversationID", conversationID, "sourceSlug", sourceSlug)

	// Build the transcript for the LLM
	transcript := buildDistillTranscript(sourceSlug, messages)

	// Get LLM service
	svc, err := s.llmManager.GetService(modelID)
	if err != nil {
		logger.Error("Failed to get LLM service for distillation", "model", modelID, "error", err)
		s.insertDistillError(ctx, conversationID, fmt.Sprintf("Failed to get model %q: %v", modelID, err))
		return ""
	}

	// Make the LLM call
	distillCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	// TODO: consider disabling thinking for distillation requests to reduce
	// cost and latency — it's a simple summarization task.
	userText := transcript
	if steer := strings.TrimSpace(instructions); steer != "" {
		userText += steeringSection(steer)
	}
	resp, err := svc.Do(distillCtx, &llm.Request{
		System: []llm.SystemContent{
			{Text: distillSystemPrompt, Type: "text"},
		},
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: userText},
				},
			},
		},
	})
	if err != nil {
		logger.Error("LLM distillation failed", "error", err)
		s.insertDistillError(ctx, conversationID, fmt.Sprintf("Distillation failed: %v", err))
		return ""
	}

	// Extract text from response
	var distilledText string
	for _, content := range resp.Content {
		if content.Type == llm.ContentTypeText {
			distilledText += content.Text
		}
	}

	if distilledText == "" {
		logger.Error("LLM returned empty distillation")
		s.insertDistillError(ctx, conversationID, "Distillation returned empty result")
		return ""
	}

	logger.Info("Distillation complete", "output_length", len(distilledText))

	distillFilePath, err := writeDistillationTempFile(conversationID, distilledText)
	if err != nil {
		logger.Error("Failed to write distillation temp file", "error", err)
		s.insertDistillError(ctx, conversationID, fmt.Sprintf("Failed to write distillation temp file: %v", err))
		return ""
	}

	// Update the status message to "complete"
	s.insertDistillStatus(ctx, conversationID, "complete")

	// Insert a user-visible message that refers to the editable temp file while
	// retaining the distillation text in user_data for UI display and context.
	userMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: distillationMessageText(distillFilePath)},
		},
	}
	userData := map[string]string{
		"distilled":             "true",
		"distillation_file":     distillFilePath,
		"distillation_content":  distilledText,
		"distillation_editable": "true",
	}
	if err := s.recordMessage(ctx, conversationID, userMessage, llm.Usage{}, userData); err != nil {
		logger.Error("Failed to record distilled message", "error", err)
		return ""
	}

	return distilledText
}

func distillationMessageText(path string) string {
	return fmt.Sprintf("Distillation written to %s", path)
}

func writeDistillationTempFile(conversationID, content string) (string, error) {
	dir := filepath.Join(os.TempDir(), "shelley-distillations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	cleanupOldDistillationTempFiles(dir, 7*24*time.Hour)
	file, err := os.CreateTemp(dir, conversationID+"-*.md")
	if err != nil {
		return "", err
	}
	path := file.Name()
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		return "", err
	}
	return path, nil
}

func cleanupOldDistillationTempFiles(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.IsDir() || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

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
	if err := s.recordMessage(ctx, conversationID, errorMessage, llm.Usage{}); err != nil {
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
	if err := s.recordMessage(ctx, conversationID, message, llm.Usage{}, userData); err != nil {
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

// buildDistillTranscript builds a full conversation transcript for the LLM to distill.
func buildDistillTranscript(sourceSlug string, messages []generated.Message) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Conversation slug: %q\n\n", sourceSlug))

	for _, msg := range messages {
		if msg.Type != string(db.MessageTypeUser) && msg.Type != string(db.MessageTypeAgent) {
			continue
		}
		if msg.LlmData == nil {
			continue
		}

		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			continue
		}

		var role string
		if msg.Type == string(db.MessageTypeUser) {
			role = "User"
		} else {
			role = "Agent"
		}

		for _, content := range llmMsg.Content {
			switch content.Type {
			case llm.ContentTypeText:
				if content.Text != "" {
					text := truncateUTF8(content.Text, 2000)
					sb.WriteString(fmt.Sprintf("%s: %s\n\n", role, text))
				}
			case llm.ContentTypeToolUse:
				inputStr := truncateUTF8(string(content.ToolInput), 500)
				sb.WriteString(fmt.Sprintf("%s: [Tool: %s] %s\n\n", role, content.ToolName, inputStr))
			case llm.ContentTypeToolResult:
				var resultText string
				for _, res := range content.ToolResult {
					if res.Type == llm.ContentTypeText && res.Text != "" {
						resultText = res.Text
						break
					}
				}
				resultText = truncateUTF8(resultText, 500)
				if resultText != "" {
					errStr := ""
					if content.ToolError {
						errStr = " (error)"
					}
					sb.WriteString(fmt.Sprintf("%s: [Tool Result%s] %s\n\n", role, errStr, resultText))
				}
			case llm.ContentTypeThinking:
				// Skip thinking blocks
			}
		}
	}

	return sb.String()
}

// distillMethodDefault collapses the whole conversation into a single briefing
// message. distillMethodCompact uses the compaction algorithm (modeled on the
// pi coding agent): summarize older messages, keep recent ones verbatim.
const (
	distillMethodDefault = "default"
	distillMethodCompact = "compact"
)

// steeringSection formats optional user-provided guidance that steers what the
// distillation/summary should emphasize. Appended to the summarizer's input.
func steeringSection(instructions string) string {
	return "\n\n## User Guidance\n\nThe user provided the following guidance on what to preserve or emphasize in this distillation. Follow it closely:\n\n" + instructions
}

func (s *Server) runDistillNewGeneration(ctx context.Context, conversationID, sourceSlug, modelID, method, instructions string, sourceGeneration int64, messages []generated.Message) {
	defer func() {
		s.mu.Lock()
		manager, ok := s.activeConversations[conversationID]
		s.mu.Unlock()
		if ok {
			manager.SetDistilling(false)
			manager.drainPendingMessages(s)
		}
	}()

	if method == distillMethodCompact {
		s.performPiDistillation(ctx, conversationID, sourceSlug, modelID, instructions, sourceGeneration, messages)
	} else {
		s.performDistillation(ctx, conversationID, sourceSlug, modelID, instructions, messages)
	}
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

	var latestGen int64
	for i := range messages {
		if messages[i].Generation > latestGen {
			latestGen = messages[i].Generation
		}
	}

	var estimate int64
	for i := range messages {
		m := messages[i]
		if m.Generation != latestGen || m.ExcludedFromContext {
			continue
		}
		llmMsg, cerr := convertToLLMMessage(m)
		if cerr != nil {
			continue
		}
		estimate += int64(estimatePiMessageTokens(llmMsg))
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
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

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

	method := req.Method
	if method == "" {
		method = distillMethodDefault
	}
	if method != distillMethodDefault && method != distillMethodCompact {
		http.Error(w, fmt.Sprintf("unknown distill method %q", method), http.StatusBadRequest)
		return
	}

	sourceConv, err := s.db.GetConversationByID(ctx, req.SourceConversationID)
	if err != nil {
		s.logger.Error("Failed to get source conversation", "conversationID", req.SourceConversationID, "error", err)
		http.Error(w, "Source conversation not found", http.StatusNotFound)
		return
	}
	// Capture the generation we are distilling FROM, before incrementing.
	// The pi strategy needs it to select the right messages to copy/summarize.
	sourceGeneration := sourceConv.CurrentGeneration
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

	if req.Cwd != "" && (sourceConv.Cwd == nil || *sourceConv.Cwd != req.Cwd) {
		if err := s.db.UpdateConversationCwd(ctx, req.SourceConversationID, req.Cwd); err != nil {
			s.logger.Error("Failed to update cwd for new generation", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}
	if sourceConv.Model == nil || *sourceConv.Model != modelID {
		if err := s.db.ForceUpdateConversationModel(ctx, req.SourceConversationID, modelID); err != nil {
			s.logger.Error("Failed to update model for new generation", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}

	manager, err := s.getOrCreateConversationManager(ctx, req.SourceConversationID, "")
	if err != nil {
		s.logger.Error("Failed to create conversation manager for distill-new-generation", "conversationID", req.SourceConversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	manager.BeginDistillingSetup()
	setupComplete := false
	defer func() {
		if !setupComplete {
			manager.SetDistilling(false)
		}
	}()

	conversation, err := db.WithTxRes(s.db, ctx, func(q *generated.Queries) (generated.Conversation, error) {
		return q.IncrementConversationGeneration(ctx, req.SourceConversationID)
	})
	if err != nil {
		s.logger.Error("Failed to increment generation", "conversationID", req.SourceConversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
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
		s.logger.Error("Failed to create status message", "conversationID", req.SourceConversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	go s.notifySubscribersNewMessage(context.WithoutCancel(ctx), req.SourceConversationID, statusMsg)

	if err := manager.Hydrate(ctx); err != nil {
		s.logger.Error("Failed to hydrate new generation", "conversationID", req.SourceConversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if fresh, ferr := s.db.GetConversationByID(ctx, req.SourceConversationID); ferr == nil {
		conversation = *fresh
	}
	if currentMessages, merr := s.db.ListMessages(ctx, req.SourceConversationID); merr == nil {
		for i := range currentMessages {
			msg := &currentMessages[i]
			if msg.Generation == conversation.CurrentGeneration && msg.Type == string(db.MessageTypeSystem) && msg.UserData == nil {
				go s.notifySubscribersNewMessage(context.WithoutCancel(ctx), req.SourceConversationID, msg)
			}
		}
	}
	go s.notifySubscribers(context.WithoutCancel(ctx), req.SourceConversationID)
	setupComplete = true
	manager.FinishDistillingSetup()

	ctxNoCancel := context.WithoutCancel(ctx)
	go func() {
		s.runDistillNewGeneration(ctxNoCancel, req.SourceConversationID, sourceSlug, modelID, method, req.Instructions, sourceGeneration, messages)
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":             "created",
		"conversation_id":    req.SourceConversationID,
		"current_generation": conversation.CurrentGeneration,
	})
}
