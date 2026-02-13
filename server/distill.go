package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/slug"
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

// handleDistillConversation handles POST /api/conversations/distill
// Creates a new conversation and uses an LLM to distill the source conversation
// into an operational summary as the initial user message.
func (s *Server) handleDistillConversation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	var req ContinueConversationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.SourceConversationID == "" {
		http.Error(w, "source_conversation_id is required", http.StatusBadRequest)
		return
	}

	// Get source conversation
	sourceConv, err := s.db.GetConversationByID(ctx, req.SourceConversationID)
	if err != nil {
		s.logger.Error("Failed to get source conversation", "conversationID", req.SourceConversationID, "error", err)
		http.Error(w, "Source conversation not found", http.StatusNotFound)
		return
	}

	// Get messages from source conversation
	messages, err := s.db.ListMessages(ctx, req.SourceConversationID)
	if err != nil {
		s.logger.Error("Failed to get messages", "conversationID", req.SourceConversationID, "error", err)
		http.Error(w, "Failed to get messages", http.StatusInternalServerError)
		return
	}

	// Determine model to use
	modelID := req.Model
	if modelID == "" && sourceConv.Model != nil {
		modelID = *sourceConv.Model
	}
	if modelID == "" {
		modelID = "gpt-oss-20b-fireworks"
	}

	// Create new conversation
	var cwdPtr *string
	if req.Cwd != "" {
		cwdPtr = &req.Cwd
	} else if sourceConv.Cwd != nil {
		cwdPtr = sourceConv.Cwd
	}
	conversation, err := s.db.CreateConversation(ctx, nil, true, cwdPtr, &modelID)
	if err != nil {
		s.logger.Error("Failed to create conversation", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	conversationID := conversation.ConversationID

	// Notify conversation list subscribers
	go s.publishConversationListUpdate(ConversationListUpdate{
		Type:         "update",
		Conversation: conversation,
	})

	// Insert a status message indicating distillation is in progress
	sourceSlug := "unknown"
	if sourceConv.Slug != nil {
		sourceSlug = *sourceConv.Slug
	}
	statusUserData := map[string]string{
		"distill_status": "in_progress",
		"source_slug":    sourceSlug,
	}
	_, err = s.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID:      conversationID,
		Type:                db.MessageTypeSystem,
		UserData:            statusUserData,
		ExcludedFromContext: true,
	})
	if err != nil {
		s.logger.Error("Failed to create status message", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Notify subscribers about the status message
	go s.notifySubscribers(context.WithoutCancel(ctx), conversationID)

	// Run distillation in background
	ctxNoCancel := context.WithoutCancel(ctx)
	go func() {
		s.runDistillation(ctxNoCancel, conversationID, sourceSlug, modelID, messages)
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "created",
		"conversation_id": conversationID,
	})
}

// runDistillation performs the LLM-based distillation and inserts the result.
func (s *Server) runDistillation(ctx context.Context, conversationID, sourceSlug, modelID string, messages []generated.Message) {
	logger := s.logger.With("conversationID", conversationID, "sourceSlug", sourceSlug)

	// Build the transcript for the LLM
	transcript := buildDistillTranscript(sourceSlug, messages)

	// Get LLM service
	svc, err := s.llmManager.GetService(modelID)
	if err != nil {
		logger.Error("Failed to get LLM service for distillation", "model", modelID, "error", err)
		s.insertDistillError(ctx, conversationID, fmt.Sprintf("Failed to get model %q: %v", modelID, err))
		return
	}

	// Make the LLM call
	distillCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	resp, err := svc.Do(distillCtx, &llm.Request{
		System: []llm.SystemContent{
			{Text: distillSystemPrompt, Type: "text"},
		},
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: transcript},
				},
			},
		},
	})
	if err != nil {
		logger.Error("LLM distillation failed", "error", err)
		s.insertDistillError(ctx, conversationID, fmt.Sprintf("Distillation failed: %v", err))
		return
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
		return
	}

	logger.Info("Distillation complete", "output_length", len(distilledText))

	// Update the status message to "complete"
	s.updateDistillStatus(ctx, conversationID, "complete")

	// Insert the distilled content as a user message
	userMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: distilledText},
		},
	}
	if err := s.recordMessage(ctx, conversationID, userMessage, llm.Usage{}); err != nil {
		logger.Error("Failed to record distilled message", "error", err)
		return
	}

	// Generate slug for the new conversation
	slugCtx, slugCancel := context.WithTimeout(ctx, 15*time.Second)
	defer slugCancel()
	_, err = slug.GenerateSlug(slugCtx, s.llmManager, s.db, s.logger, conversationID, distilledText, modelID)
	if err != nil {
		logger.Warn("Failed to generate slug", "error", err)
	} else {
		go s.notifySubscribers(ctx, conversationID)
	}
}

// insertDistillError updates status to error and inserts an error message.
func (s *Server) insertDistillError(ctx context.Context, conversationID, errMsg string) {
	s.updateDistillStatus(ctx, conversationID, "error")

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

// updateDistillStatus updates the system status message in a conversation.
func (s *Server) updateDistillStatus(ctx context.Context, conversationID, status string) {
	// Find the system message with distill_status
	messages, err := s.db.ListMessagesByType(ctx, conversationID, db.MessageTypeSystem)
	if err != nil {
		s.logger.Error("Failed to list system messages", "conversationID", conversationID, "error", err)
		return
	}

	for _, msg := range messages {
		if msg.UserData == nil {
			continue
		}
		var userData map[string]string
		if err := json.Unmarshal([]byte(*msg.UserData), &userData); err != nil {
			continue
		}
		if userData["distill_status"] != "" {
			// Update user_data with new status
			userData["distill_status"] = status
			newData, err := json.Marshal(userData)
			if err != nil {
				s.logger.Error("Failed to marshal distill status", "error", err)
				return
			}
			newDataStr := string(newData)
			if err := s.db.UpdateMessageUserData(ctx, msg.MessageID, &newDataStr); err != nil {
				s.logger.Error("Failed to update distill status", "messageID", msg.MessageID, "error", err)
			}
			// Re-fetch the updated message and push it to SSE subscribers
			// so the client sees the status change (spinner → complete).
			updatedMsg, err := s.db.GetMessageByID(ctx, msg.MessageID)
			if err == nil {
				go s.notifySubscribersNewMessage(ctx, conversationID, updatedMsg)
			}
			return
		}
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
