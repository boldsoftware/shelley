package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"shelley.exe.dev/db/generated"
)

// SwitchModelRequest is the body for POST /conversation/<id>/switch-model.
type SwitchModelRequest struct {
	Model string `json:"model"`
}

// handleSwitchModel handles POST /conversation/<id>/switch-model.
// It switches the model for an existing conversation without starting a new
// generation. The full conversation history is preserved — only the LLM
// service changes. This is analogous to what distill-new-generation does when
// the caller passes a different model, but without summarizing or compacting
// the history.
//
// The handler:
//  1. Validates the requested model exists.
//  2. Resets the in-memory conversation loop (stops any in-flight work).
//  3. Updates the persisted model in the database.
//  4. Re-hydrates the conversation manager so the next message uses the new model.
//  5. Broadcasts the change to SSE subscribers so the UI updates.
func (s *Server) handleSwitchModel(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	var req SwitchModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	// Validate the model exists and we can create a service for it.
	if _, err := s.llmManager.GetService(req.Model); err != nil {
		s.logger.Error("Unsupported model for switch", "model", req.Model, "error", err)
		http.Error(w, fmt.Sprintf("Unsupported model: %s", req.Model), http.StatusBadRequest)
		return
	}

	// Check the conversation exists.
	existing, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}

	// If already using this model, no-op.
	if existing.Model != nil && *existing.Model == req.Model {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":          "ok",
			"conversation_id": conversationID,
			"model":           req.Model,
			"changed":         false,
		})
		return
	}

	// Get the active conversation manager (if any).
	s.mu.Lock()
	manager, hasManager := s.activeConversations[conversationID]
	s.mu.Unlock()

	if hasManager {
		// If the agent is currently working, cancel it first. This mirrors
		// what the cancel handler does before resetting.
		if manager.IsAgentWorking() {
			if err := manager.CancelConversation(ctx); err != nil {
				s.logger.Warn("Failed to cancel active conversation during model switch",
					"conversationID", conversationID, "error", err)
				// Non-fatal: ResetLoop will force-stop it anyway.
			}
		}

		// Reset the loop. This tears down the in-memory LLM loop, clears
		// the cached modelID, and forces re-hydration on the next message
		// — exactly what distillation does.
		manager.ResetLoop()
	}

	// Persist the new model.
	if err := s.db.ForceUpdateConversationModel(ctx, conversationID, req.Model); err != nil {
		s.logger.Error("Failed to update model", "conversationID", conversationID, "model", req.Model, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Re-fetch conversation to pick up the model change.
	conversation, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to re-fetch conversation after model switch", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Re-hydrate so the manager is ready for the next message with the new model.
	if hasManager {
		if err := manager.Hydrate(ctx); err != nil {
			s.logger.Error("Failed to hydrate after model switch", "error", err)
			// Non-fatal: the next AcceptUserMessage will trigger hydration anyway.
		}
	}

	// Insert a system-visible note so the user (and the LLM on future turns)
	// can see that the model changed.
	s.recordModelSwitchNote(ctx, conversationID, existing, conversation)

	// Broadcast the updated conversation to SSE subscribers.
	if hasManager {
		manager.broadcastStream(StreamResponse{Conversation: conversation})
	}
	s.publishConversationListUpdate(ConversationListUpdate{Type: "update", Conversation: conversation})

	s.logger.Info("Switched conversation model",
		"conversationID", conversationID,
		"from", ptrStringOr(existing.Model, "<none>"),
		"to", req.Model,
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "ok",
		"conversation_id": conversationID,
		"model":           req.Model,
		"changed":         true,
	})
}

// recordModelSwitchNote inserts a warning message into the conversation noting
// the model switch. This is a user-visible warning (not sent to the LLM).
// The LLM doesn't need to be told — it will simply process the full history
// with whatever model service is now active.
func (s *Server) recordModelSwitchNote(ctx context.Context, conversationID string, old, new *generated.Conversation) {
	oldModel := ptrStringOr(old.Model, "unknown")
	newModel := ptrStringOr(new.Model, "unknown")
	text := fmt.Sprintf("Model switched from %s to %s. Full conversation history preserved.", oldModel, newModel)

	result, err := s.db.CreateWarningMessage(ctx, conversationID, text, 100, "")
	if err != nil {
		s.logger.Error("Failed to record model switch note", "error", err)
		return
	}
	if result.Suppressed {
		return
	}

	// Broadcast the warning message to SSE subscribers.
	go s.notifySubscribersNewMessage(ctx, conversationID, result.Message)
}

func ptrStringOr(p *string, fallback string) string {
	if p != nil && *p != "" {
		return *p
	}
	return fallback
}
