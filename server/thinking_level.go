package server

import (
	"encoding/json"
	"net/http"

	"shelley.exe.dev/db"
)

type UpdateThinkingLevelRequest struct {
	ThinkingLevel string `json:"thinking_level"`
}

func (s *Server) handleUpdateThinkingLevel(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req UpdateThinkingLevelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if msg := validateConversationOptions(db.ConversationOptions{ThinkingLevel: req.ThinkingLevel}); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	conversation, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}

	opts := db.ParseConversationOptions(conversation.ConversationOptions)
	if opts.ThinkingLevel == req.ThinkingLevel {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":          "ok",
			"conversation_id": conversationID,
			"thinking_level":  req.ThinkingLevel,
			"changed":         false,
		})
		return
	}

	s.mu.Lock()
	manager, hasManager := s.activeConversations[conversationID]
	s.mu.Unlock()
	if hasManager && manager.IsAgentWorking() {
		http.Error(w, "Cannot change reasoning level while agent is working", http.StatusConflict)
		return
	}

	opts.ThinkingLevel = req.ThinkingLevel
	if err := s.db.UpdateConversationOptions(ctx, conversationID, opts); err != nil {
		s.logger.Error("Failed to update thinking level", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if hasManager {
		manager.ResetLoop()
		if err := manager.Hydrate(ctx); err != nil {
			s.logger.Error("Failed to hydrate after thinking level update", "conversationID", conversationID, "error", err)
		}
	}

	conversation, err = s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to re-fetch conversation after thinking level update", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if hasManager {
		manager.broadcastStream(StreamResponse{Conversation: conversation})
	}
	s.publishConversationListUpdate(ConversationListUpdate{Type: "update", Conversation: conversation})

	s.logger.Info("Updated conversation thinking level", "conversationID", conversationID, "thinkingLevel", req.ThinkingLevel)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "ok",
		"conversation_id": conversationID,
		"thinking_level":  req.ThinkingLevel,
		"changed":         true,
	})
}
