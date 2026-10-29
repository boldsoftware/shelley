package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/ui"
)

// mu serializes starting an agent turn with project moves and action
// reservation. The DB remains the source of truth for action status.
type workspaceState struct {
	mu      sync.Mutex
	workers sync.WaitGroup
	// stops stops the runs this process executes, by run id.
	stops map[string]chan struct{}
	// reports wakes the action report worker.
	reports chan struct{}
	// Restart endpoints reserve this fence before delayed exit or exec.
	restarting bool
	// New conversations are fenced until their first-message pipeline has
	// either accepted the turn or returned an error.
	initializing map[string]bool
	// Archive, delete, rename and cwd changes in progress, by conversation
	// id. Actions do not start on such a conversation, as these operations
	// checked for running actions before doing their work unlocked.
	mutating map[string]int

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
	api.HandleFunc("POST /api/conversation/{id}/actions/{action_id}", s.handleRunAction)
	api.HandleFunc("GET /api/action-runs/{id}", s.handleGetActionRun)
	api.HandleFunc("POST /api/action-runs/{id}/cancel", s.handleCancelActionRun)
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
		http.Error(w, "Conversation, project, or action not found", http.StatusNotFound)
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

func (s *Server) handleRunAction(w http.ResponseWriter, r *http.Request) {
	// db.BeginAction refuses the Assistant and conversations outside projects.
	id := r.PathValue("id")
	manager, err := s.getOrCreateConversationManager(r.Context(), id, r.Header.Get("X-ExeDev-Email"))
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	// Manager creation can hydrate a custom system-prompt hook, which may
	// itself use the workspace API. Never hold the workspace mutex across it.
	s.workspace.mu.Lock()
	defer s.workspace.mu.Unlock()
	if s.workspace.restarting {
		http.Error(w, "Server is restarting", http.StatusServiceUnavailable)
		return
	}
	select {
	case <-s.shutdownCh:
		http.Error(w, "Server is shutting down", http.StatusServiceUnavailable)
		return
	default:
	}
	if s.workspace.initializing[id] || s.workspace.mutating[id] > 0 {
		s.workspaceHTTPError(w, db.ErrConflict)
		return
	}
	run, action, cwd, err := s.reserveAction(r.Context(), manager, id, r.PathValue("action_id"))
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	if s.workspace.stops == nil {
		s.workspace.stops = map[string]chan struct{}{}
	}
	stop := make(chan struct{})
	s.workspace.stops[run.ID] = stop
	s.notifyWorkspaceChanged()
	// The goroutine intentionally does not inherit the HTTP request context.
	// Its first durable reservation already prevents conversation mutation.
	userEmail := r.Header.Get("X-ExeDev-Email")
	s.workspace.workers.Add(1)
	go func() {
		defer s.workspace.workers.Done()
		if err := s.executeAction(run.ID, action.Command, cwd, userEmail, stop); err != nil {
			s.logger.Error("Action execution failed", "runID", run.ID, "error", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) reserveAction(ctx context.Context, manager *ConversationManager, id, actionID string) (*db.ActionRun, *db.ProjectAction, string, error) {
	manager.loopLifecycleMu.Lock()
	defer manager.loopLifecycleMu.Unlock()
	if manager.IsAgentWorking() {
		return nil, nil, "", db.ErrConflict
	}
	conversation, err := s.db.GetConversationByID(ctx, id)
	if err != nil {
		return nil, nil, "", err
	}
	if err := validateConversationDirectory(derefString(conversation.Cwd)); err != nil {
		return nil, nil, "", err
	}
	return s.db.BeginAction(ctx, id, actionID)
}

// Called after shutdownCh is closed, before closing the database. Fence new
// reservations, but release mu before waiting: a finishing worker can still
// be completing a callback that uses the workspace API.
func (s *Server) waitActions(ctx context.Context) error {
	s.workspace.mu.Lock()
	s.workspace.restarting = true
	s.workspace.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.workspace.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// actionOutput buffers a run's output, which flush persists: a chatty
// command must not write the database at every line.
type actionOutput struct {
	db      *db.DB
	id      string
	mu      sync.Mutex
	pending []byte
	err     error
	onFail  func()
}

const (
	// actionFlush is how often a running command's output is persisted.
	actionFlush = 500 * time.Millisecond
	// actionWaitDelay is how long a command's background processes may hold
	// its output open once it exits.
	actionWaitDelay = 2 * time.Second
	actionShutdown  = "Server stopped; command was terminated. Nothing was archived."
)

func (out *actionOutput) Write(p []byte) (int, error) {
	out.mu.Lock()
	defer out.mu.Unlock()
	if out.err != nil {
		return 0, out.err
	}
	out.pending = append(out.pending, p...)
	// Only the last MaxActionOutput bytes are kept.
	if len(out.pending) > 2*db.MaxActionOutput {
		out.pending = append(out.pending[:0], out.pending[len(out.pending)-db.MaxActionOutput:]...)
	}
	return len(p), nil
}

func (out *actionOutput) flush() {
	out.mu.Lock()
	defer out.mu.Unlock()
	if out.err != nil || len(out.pending) == 0 {
		return
	}
	if err := out.db.AppendActionOutput(context.Background(), out.id, string(out.pending)); err != nil {
		out.err = err
		out.onFail()
		return
	}
	out.pending = out.pending[:0]
}

// executeAction is synchronous so tests can await completion without
// polling or sleeping; the HTTP handler calls it in a detached goroutine.
// Closing stop kills the command.
func (s *Server) executeAction(runID, command, cwd, userEmail string, stop <-chan struct{}) error {
	defer func() {
		s.workspace.mu.Lock()
		delete(s.workspace.stops, runID)
		s.workspace.mu.Unlock()
	}()
	ctx := context.Background()
	run, err := s.db.GetActionRun(ctx, runID)
	if err != nil {
		return err
	}
	conversation, err := s.db.GetConversationByID(ctx, run.ConversationID)
	if err != nil {
		return err
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = cwd
	cmd.Env = actionEnvironment(os.Environ(), buildTerminalEnv(
		run.ConversationID, derefString(conversation.Slug), derefString(conversation.Model),
		userEmail, cwd, s.listenPort, s.socketPath))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = actionWaitDelay
	kill := func() {
		// Only this command's process group, never a name-wide pkill.
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	output := &actionOutput{db: s.db, id: runID, onFail: kill}
	cmd.Stdout, cmd.Stderr = output, output
	err = cmd.Start()
	interrupted := ""
	if err == nil {
		done := make(chan struct{})
		reason := make(chan string, 1)
		go func() {
			ticker := time.NewTicker(actionFlush)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					output.flush()
					continue
				case <-s.shutdownCh:
					kill()
					reason <- actionShutdown
				case <-stop:
					kill()
					reason <- "Stopped by the user; command was terminated. Nothing was archived."
				case <-done:
					reason <- ""
				}
				return
			}
		}()
		err = cmd.Wait()
		close(done)
		interrupted = <-reason
		output.flush()
	}
	if interrupted != "" {
		if err := s.db.InterruptActionRun(ctx, runID, interrupted); err != nil {
			return err
		}
		s.notifyWorkspaceChanged()
		if interrupted != actionShutdown {
			// After a shutdown, the next process reports it.
			s.reportAction(ctx, runID)
		}
		return nil
	}
	var exitCode *int
	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		exitCode = &code
	}
	runError := ""
	// A background process holding the output open (ErrWaitDelay) does not
	// fail a command that exited 0.
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		runError = err.Error()
	}
	if output.err != nil {
		runError = fmt.Sprintf("persisting action output: %v", output.err)
	}
	if err := s.db.FinishAction(ctx, runID, exitCode, runError); err != nil {
		return err
	}
	s.notifyWorkspaceChanged()
	s.reportAction(ctx, runID)
	// Existing upstream producers may have queued work while the command
	// ran. Completion releases the durable fence before draining that work.
	s.mu.Lock()
	manager := s.activeConversations[run.ConversationID]
	s.mu.Unlock()
	if manager != nil {
		conversation, err := s.db.GetConversationByID(ctx, run.ConversationID)
		if err != nil {
			return err
		}
		if conversation.Archived {
			manager.cancelIdleCompaction()
		} else {
			manager.drainPendingMessages(s)
		}
	}
	return nil
}

func actionEnvironment(inherited, shelley []string) []string {
	out := make([]string, 0, len(inherited)+len(shelley))
	for _, value := range inherited {
		if !strings.HasPrefix(value, "SHELLEY_") {
			out = append(out, value)
		}
	}
	return append(out, shelley...)
}

func (s *Server) handleGetActionRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.db.GetActionRun(r.Context(), r.PathValue("id"))
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// handleCancelActionRun stops a run this process executes: it kills the
// command's process group, and the run ends interrupted.
func (s *Server) handleCancelActionRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.workspace.mu.Lock()
	stop, ok := s.workspace.stops[id]
	if ok {
		delete(s.workspace.stops, id)
		close(stop)
	}
	s.workspace.mu.Unlock()
	if ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, err := s.db.GetActionRun(r.Context(), id); err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	http.Error(w, "The action is not running", http.StatusConflict)
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
