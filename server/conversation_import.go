package server

import (
	"fmt"
	"net/http"
	"path/filepath"

	"shelley.exe.dev/db"
	"shelley.exe.dev/slug"
)

// POST /api/conversations/import copies conversations from another Shelley's
// database (a read-only copy on this host, e.g. fetched over SSH) into a
// project, with their full history and subagents, so they can
// continue here. The source is never modified.
type importRequest struct {
	SourceDB      string `json:"source_db"`
	Project       string `json:"project"`
	Conversations []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"conversations"`
}

func (s *Server) handleImportConversations(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	if !filepath.IsAbs(req.SourceDB) || req.Project == "" || len(req.Conversations) == 0 {
		http.Error(w, "source_db (absolute path), project, and conversations are required", http.StatusBadRequest)
		return
	}
	ids := make([]string, len(req.Conversations))
	for i, c := range req.Conversations {
		ids[i] = c.ID
	}
	read, err := db.ReadConversationsForImport(r.Context(), req.SourceDB, ids)
	if err != nil {
		s.workspaceHTTPError(w, fmt.Errorf("read %s: %w", req.SourceDB, err))
		return
	}
	imports := make([]db.ImportConversation, len(read))
	for i, c := range read {
		imports[i] = db.ImportConversation{Title: slug.Sanitize(req.Conversations[i].Title), Conversation: c}
	}
	s.workspace.mu.Lock()
	err = s.projectDeleting(req.Project)
	if err == nil {
		err = s.db.ImportConversations(r.Context(), req.Project, imports)
	}
	s.workspace.mu.Unlock()
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	s.notifyWorkspaceChanged()
	for _, id := range ids {
		s.prepareConversationVM(id)
	}
	imported := []map[string]any{}
	for _, id := range ids {
		c, err := s.db.GetConversationByID(r.Context(), id)
		if err != nil {
			s.workspaceHTTPError(w, err)
			return
		}
		imported = append(imported, map[string]any{"conversation_id": id, "slug": c.Slug})
	}
	writeJSON(w, http.StatusCreated, map[string]any{"imported": imported})
}
