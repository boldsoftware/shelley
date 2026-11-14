package server

import (
	"database/sql"
	"errors"
	"net/http"
)

func (s *Server) handleConversationThemes(w http.ResponseWriter, r *http.Request) {
	themes, err := s.db.GetConversationThemes(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Error("failed to load conversation themes", "error", err)
		http.Error(w, "Failed to load conversation themes", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, themes)
}

func (s *Server) handleConversationThemesIndex(w http.ResponseWriter, r *http.Request) {

	themes, err := s.db.ListConversationThemes(r.Context())
	if err != nil {
		s.logger.Error("failed to load conversation themes index", "error", err)
		http.Error(w, "Failed to load conversation themes", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, themes)
}
