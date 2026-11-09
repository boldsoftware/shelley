package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

func (s *Server) requireReadUser(w http.ResponseWriter, r *http.Request) bool {
	if s.requireHeader != "" && s.userIDFromRequest(r) == "" {
		http.Error(w, "Authenticated user required", http.StatusUnauthorized)
		return false
	}
	return true
}

func (s *Server) handleConversationRead(w http.ResponseWriter, r *http.Request, conversationID string) {
	if !s.requireReadUser(w, r) {
		return
	}
	var request struct {
		ReadAt int64 `json:"read_at"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "read_at is required", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "Expected one JSON object", http.StatusBadRequest)
		return
	}
	userID := s.userIDFromRequest(r)
	readAt, err := s.db.AcknowledgeConversation(r.Context(), userID, conversationID, request.ReadAt)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			http.Error(w, "Conversation not found", http.StatusNotFound)
		case errors.Is(err, db.ErrInvalidReadTime):
			http.Error(w, db.ErrInvalidReadTime.Error(), http.StatusBadRequest)
		default:
			s.logger.Error("failed to acknowledge conversation", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}
	event := generated.ConversationReadMarkersRow{ConversationID: conversationID, ReadAt: readAt}
	s.streamPub.Broadcast(StreamResponse{ConversationRead: &event, readUserID: userID})
	writeJSON(w, http.StatusOK, event)
}

func (s *Server) streamEventVisible(r *http.Request, event StreamResponse) bool {
	return event.ConversationRead == nil || (s.requireHeader == "" || s.userIDFromRequest(r) != "") && event.readUserID == s.userIDFromRequest(r)
}

func (s *Server) personalizeConversationReads(ctx context.Context, conversations []ConversationWithState, userID string) error {
	reads, err := s.db.ConversationReads(ctx, userID)
	if err != nil {
		return err
	}
	positions := make(map[string]int64, len(reads.Receipts))
	for _, receipt := range reads.Receipts {
		positions[receipt.ConversationID] = receipt.ReadAt
	}
	for i := range conversations {
		c := &conversations[i]
		readAt := max(positions[c.ConversationID], reads.FirstSeenAt)
		unread := c.LastAttentionAt > readAt
		c.ReadAt = &readAt
		c.Unread = &unread
	}
	return nil
}
