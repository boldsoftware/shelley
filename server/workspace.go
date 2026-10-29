package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/ui"
)

// mu serializes starting an agent turn with project moves.
type workspaceState struct {
	mu sync.Mutex
	// New conversations are fenced until their first-message pipeline has
	// either accepted the turn or returned an error.
	initializing map[string]bool

	// assistantID is the Assistant's id, "" outside workspace mode, as of
	// the last ensureWorkspace.
	assistantID atomic.Pointer[string]
}

// ensureWorkspace, in workspace mode, creates the Assistant if there is
// none yet. It returns the Assistant's id, or "" outside workspace mode,
// and keeps it for assistantID. It runs at startup and when the workspace
// flag changes, so requests never create the Assistant.
func (s *Server) ensureWorkspace(ctx context.Context) (id string, err error) {
	defer func() {
		if err == nil {
			s.workspace.assistantID.Store(&id)
		}
	}()
	// Initialization belongs to the server, not to the first HTTP client's
	// lifetime. An aborted request must not poison the workspace forever.
	ctx = context.WithoutCancel(ctx)
	if on, err := s.db.WorkspaceMode(ctx); err != nil || !on {
		return "", err
	}
	if id, err := s.db.GetAssistantID(ctx); !errors.Is(err, sql.ErrNoRows) {
		return id, err
	}
	// The workspace Assistant texts like the shipped Assistant profile and
	// follows its edits until its own settings change.
	profile := "Assistant"
	settings, err := s.newConversationSettings(ctx, SettingsChange{Profile: &profile}, s.getModelList())
	if err != nil {
		return "", fmt.Errorf("workspace Assistant settings: %w", err)
	}
	var opts db.ConversationOptions
	settings.applyTo(&opts)
	return s.db.EnsureWorkspace(ctx, settings.Model, opts)
}

// assistantID returns the Assistant's id, or "" outside workspace mode.
func (s *Server) assistantID() string {
	if id := s.workspace.assistantID.Load(); id != nil {
		return *id
	}
	return ""
}

// isAssistant reports whether conversationID is the workspace Assistant.
func (s *Server) isAssistant(conversationID string) bool {
	id := s.assistantID()
	return id != "" && id == conversationID
}

// workspaceFlagChanged loads the workspace for the new value of its flag,
// and has open pages reload (see uiBuild), since the page reads the mode
// only when it loads.
func (s *Server) workspaceFlagChanged(ctx context.Context) error {
	if _, err := s.ensureWorkspace(ctx); err != nil {
		return err
	}
	if s.streamPub != nil {
		s.streamPub.Broadcast(StreamResponse{UIBuild: s.uiBuild()})
	}
	return nil
}

// uiBuild is the page's build: the UI's, and whether it is in workspace
// mode, so toggling the mode makes open pages stale.
func (s *Server) uiBuild() string {
	if s.assistantID() != "" {
		return ui.BuildID() + "+workspace"
	}
	return ui.BuildID()
}

func (s *Server) registerWorkspaceRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/workspace", s.handleWorkspace)
	api.HandleFunc("POST /api/projects", s.handleCreateProject)
	api.HandleFunc("PUT /api/projects/{name}", s.handleUpdateProject)
	api.HandleFunc("DELETE /api/projects/{name}", s.handleDeleteProject)
	api.HandleFunc("PUT /api/conversation/{id}/project", s.handleMoveConversation)
}

func (s *Server) notifyWorkspaceChanged() {
	if s.streamPub != nil {
		s.streamPub.Broadcast(StreamResponse{WorkspaceChanged: true})
	}
}

// inProject reports whether conversation id is in a project (or is the
// Assistant). The workspace lists those, so creating, archiving,
// unarchiving or deleting one changes it.
func (s *Server) inProject(ctx context.Context, id string) (bool, error) {
	_, err := s.db.GetWorkspaceConversation(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Server) workspaceHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "Conversation or project not found", http.StatusNotFound)
	case errors.Is(err, db.ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, db.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		s.internalError(w, "Workspace operation failed", err)
	}
}

func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	workspace, err := s.db.GetWorkspace(r.Context())
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workspace)
}

// A missing directory is always an actionable error. In particular, never
// substitute the server's cwd for an unconfigured repository project.
func validateConversationDirectory(cwd string) error {
	if cwd == "" {
		return fmt.Errorf("%w: configure a working directory for this project first", db.ErrInvalid)
	}
	if !filepath.IsAbs(cwd) {
		return fmt.Errorf("%w: working directory must be an absolute path", db.ErrInvalid)
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return fmt.Errorf("%w: working directory: %v", db.ErrInvalid, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: working directory is not a directory", db.ErrInvalid)
	}
	return nil
}

// projectRequest is the body of POST and PUT /api/projects: the project,
// and on creation the profile whose settings its own start as a copy of.
type projectRequest struct {
	db.Project
	CopySettingsFrom string `json:"copy_settings_from"`
}

func (s *Server) decodeProject(w http.ResponseWriter, r *http.Request) (*projectRequest, bool) {
	var req projectRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !decodeStrict(w, r, &req) {
		return nil, false
	}
	if req.Cwd != "" {
		if err := validateConversationDirectory(req.Cwd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return nil, false
		}
	}
	return &req, true
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeProject(w, r)
	if !ok {
		return
	}
	created, err := s.db.CreateProject(r.Context(), req.Project, req.CopySettingsFrom)
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	s.notifyWorkspaceChanged()
	s.writeSavedProject(w, r, created.Name, http.StatusCreated)
}

// writeSavedProject responds with the project as saved.
func (s *Server) writeSavedProject(w http.ResponseWriter, r *http.Request, name string, status int) {
	project, err := s.db.GetProject(r.Context(), name)
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	writeJSON(w, status, project)
}

// handleUpdateProject replaces a project's data; a different name renames
// it, and its profile.
func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeProject(w, r)
	if !ok {
		return
	}
	if req.CopySettingsFrom != "" {
		http.Error(w, "copy_settings_from only applies to new projects; edit the profile's settings instead", http.StatusBadRequest)
		return
	}
	if err := s.db.UpdateProject(r.Context(), r.PathValue("name"), req.Project); err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	s.notifyWorkspaceChanged()
	s.writeSavedProject(w, r, strings.TrimSpace(req.Name), http.StatusOK)
}

// handleDeleteProject deletes a project and its profile; see db.DeleteProject.
func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	s.workspace.mu.Lock()
	defer s.workspace.mu.Unlock()
	if err := s.db.DeleteProject(r.Context(), r.PathValue("name")); err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	s.notifyWorkspaceChanged()
	w.WriteHeader(http.StatusNoContent)
}

// handleMoveConversation moves an idle conversation to another project; see db.MoveConversation.
func (s *Server) handleMoveConversation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	if req.Project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}
	// db.MoveConversation refuses the Assistant.
	id := r.PathValue("id")
	manager, err := s.getOrCreateConversationManager(r.Context(), id, r.Header.Get("X-ExeDev-Email"))
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	s.workspace.mu.Lock()
	defer s.workspace.mu.Unlock()
	if s.workspace.initializing[id] {
		s.workspaceHTTPError(w, db.ErrConflict)
		return
	}
	// The loop restarts at the next turn, with the project's instructions
	// (see projectService).
	err = manager.resetLoopAfter(false, func() error {
		if manager.IsAgentWorking() {
			return db.ErrConflict
		}
		if err := s.db.MoveConversation(r.Context(), id, req.Project); err != nil {
			return err
		}
		return manager.reloadOptions(r.Context())
	})
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	s.notifyWorkspaceChanged()
	metadata, err := s.db.GetWorkspaceConversation(r.Context(), id)
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metadata)
}

// reloadOptions takes the conversation's stored options, after a change made
// in the database (a project move).
func (cm *ConversationManager) reloadOptions(ctx context.Context) error {
	conversation, err := cm.db.GetConversationByID(ctx, cm.conversationID)
	if err != nil {
		return err
	}
	cm.mu.Lock()
	cm.conversationOptions = db.ParseConversationOptions(conversation.ConversationOptions)
	cm.mu.Unlock()
	return nil
}

// checkProfileSwitch refuses a settings change that would take a
// conversation into or out of a project by switching its profile: it
// moves (handleMoveConversation), with its checks.
func (s *Server) checkProfileSwitch(ctx context.Context, conv generated.Conversation, change SettingsChange) error {
	from := db.ParseConversationOptions(conv.ConversationOptions).Profile
	if change.Profile == nil || *change.Profile == from {
		return nil
	}
	for _, name := range []string{from, *change.Profile} {
		if _, err := s.db.GetProject(ctx, name); err == nil {
			return fmt.Errorf("%w: conversations don't switch into or out of project %q; move the conversation to another project instead", db.ErrConflict, name)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}
