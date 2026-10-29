package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"shelley.exe.dev/db"
)

// Shelley restarts into an installed build by itself, exactly as the user's
// Restart Shelley click does, once nothing is working and the user has left
// the UI alone for autoRestartIdle. Each build is tried at most once, and
// not at all once the user cancelled it.
const (
	autoRestartIdle = 30 * time.Second
	// autoRestartInterval is how often a planned restart that waits for work
	// to finish checks again.
	autoRestartInterval = 5 * time.Second
)

// autoRestartPlan is the automatic restart into Target, the newest installed
// build: State is "pending" (once the user is idle) or "busy" (waits for
// work to finish).
type autoRestartPlan struct {
	Target db.Customization
	State  string
	Reason string
}

// noteUserActivity records real user activity: a UI heartbeat, a user's API
// call, or an inbound text.
func (s *Server) noteUserActivity() {
	s.customizations.activity.Store(time.Now().UnixNano())
}

// trackUserActivity counts every mutating request on the TCP listener as
// activity: the user's clients, and agents when Shelley has no Unix socket
// (harmless: an agent's turn blocks the restart anyway).
func (s *Server) trackUserActivity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			s.noteUserActivity()
		}
		next.ServeHTTP(w, r)
	})
}

// autoRestartIn is how much longer the user must stay idle.
func (s *Server) autoRestartIn() time.Duration {
	return time.Until(time.Unix(0, s.customizations.activity.Load()).Add(autoRestartIdle))
}

// autoRestartBusy names the work an automatic restart must not interrupt:
// any conversation turn (the Assistant's and subagents' included), a conversation
// starting, a project action, or a customization verification.
func (s *Server) autoRestartBusy(ctx context.Context) (string, error) {
	if !s.isServerIdle() {
		return "a conversation is working", nil
	}
	s.workspace.mu.Lock()
	starting := len(s.workspace.initializing) != 0
	s.workspace.mu.Unlock()
	if starting {
		return "a task is starting", nil
	}
	if running, err := s.db.HasAnyRunningAction(ctx); err != nil || running {
		return "a task action is running", err
	}
	if verifying, err := s.db.HasVerifyingCustomization(ctx); err != nil || verifying {
		return "a Shelley build is verifying", err
	}
	return "", nil
}

// evaluateAutoRestart plans the automatic restart into the newest installed
// build, or returns nil when there is none to make.
func (s *Server) evaluateAutoRestart(ctx context.Context) (*autoRestartPlan, error) {
	if s.customizations.deps.invocationID() == "" {
		return nil, nil // Restarting needs systemd, as for the manual restart.
	}
	states, err := s.db.ListCustomizations(ctx)
	if err != nil {
		return nil, err
	}
	var target *db.Customization
	for i := range states {
		state := &states[i]
		if state.Status == "installed" && !state.Archived && state.InstalledAt != nil &&
			(target == nil || state.InstalledAt.After(*target.InstalledAt)) {
			target = state
		}
	}
	if target == nil {
		return nil, nil
	}
	event, err := s.db.LastCustomizationAutoRestart(ctx, target.ArtifactSHA256)
	if err != nil {
		return nil, err
	}
	if event != "" {
		return nil, nil // Cancelled, or already tried: never restart into the same build twice.
	}
	busy, err := s.autoRestartBusy(ctx)
	if err != nil {
		return nil, err
	}
	if busy != "" {
		return &autoRestartPlan{Target: *target, State: "busy", Reason: busy}, nil
	}
	return &autoRestartPlan{Target: *target, State: "pending"}, nil
}

func (s *Server) refreshAutoRestartPlan(ctx context.Context) (*autoRestartPlan, error) {
	plan, err := s.evaluateAutoRestart(ctx)
	if err != nil {
		return nil, err
	}
	s.customizations.autoRestart.Store(plan)
	return plan, nil
}

// startAutoRestart starts the worker that restarts into installed builds. It
// waits for kickAutoRestart (an install, or customization starting in
// workspace mode), then evaluates again while a restart is planned.
func (s *Server) startAutoRestart() {
	s.workspace.workers.Add(1)
	go func() {
		defer s.workspace.workers.Done()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-s.shutdownCh
			cancel()
		}()
		var timer <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.customizations.kick:
			case <-timer:
			}
			scheduled, wait := s.autoRestartTick(ctx)
			if scheduled {
				return
			}
			timer = nil
			if wait > 0 {
				timer = time.After(wait)
			}
		}
	}()
}

func (s *Server) kickAutoRestart() {
	select {
	case s.customizations.kick <- struct{}{}:
	default:
	}
}

// autoRestartTick restarts into the planned build once the user is idle. It
// reports whether the restart was scheduled, and otherwise when to evaluate
// again (0: not until kicked).
func (s *Server) autoRestartTick(ctx context.Context) (bool, time.Duration) {
	if s.assistantID() == "" {
		s.customizations.autoRestart.Store(nil)
		return false, 0
	}
	if err := s.ensureCustomizations(ctx); err != nil {
		s.logger.Error("Auto-restart evaluation failed", "error", err)
		return false, 0
	}
	s.customizations.autoMu.Lock()
	defer s.customizations.autoMu.Unlock()
	plan, err := s.refreshAutoRestartPlan(ctx)
	if err != nil {
		s.logger.Error("Auto-restart evaluation failed", "error", err)
		return false, autoRestartInterval
	}
	switch {
	case plan == nil:
		return false, 0
	case plan.State == "busy":
		return false, autoRestartInterval
	case s.autoRestartIn() > 0:
		return false, s.autoRestartIn()
	}
	// The restart fence stops new project actions and verifications; then every
	// condition is checked again, so none can start between check and exit.
	if err := s.reserveRestart(); err != nil {
		s.logger.Info("Auto-restart deferred", "error", err)
		return false, autoRestartInterval
	}
	current, err := s.refreshAutoRestartPlan(ctx)
	if err != nil || current == nil || current.State != "pending" || s.autoRestartIn() > 0 ||
		current.Target.ArtifactSHA256 != plan.Target.ArtifactSHA256 {
		s.releaseRestart()
		if err != nil {
			s.logger.Error("Auto-restart evaluation failed", "error", err)
		}
		return false, autoRestartInterval
	}
	target := current.Target
	event := db.CustomizationAutoRestart{ConversationID: target.ConversationID, Commit: target.Commit, ArtifactSHA256: target.ArtifactSHA256, Event: "attempted"}
	if err := s.db.RecordCustomizationAutoRestart(ctx, event); err != nil {
		s.releaseRestart()
		s.logger.Error("Auto-restart not recorded; not restarting", "error", err)
		return false, autoRestartInterval
	}
	s.logger.Info("Auto-restarting Shelley into installed build: all turns done and the user is idle",
		"conversationID", target.ConversationID, "commit", target.Commit, "artifact_sha256", target.ArtifactSHA256)
	if _, err := s.restartCustomization(ctx, target.ConversationID, target.Commit, target.ArtifactSHA256); err != nil {
		s.releaseRestart()
		s.logger.Error("Auto-restart failed; not retrying this build", "conversationID", target.ConversationID, "commit", target.Commit, "error", err)
		event.Event, event.Error = "failed", err.Error()
		if err := s.db.RecordCustomizationAutoRestart(context.WithoutCancel(ctx), event); err != nil {
			s.logger.Error("Auto-restart failure not recorded", "error", err)
		}
		s.customizations.autoRestart.Store(nil)
		return false, 0
	}
	s.scheduleExit(restartExitCode)
	return true, 0
}

// reserveRestart takes the restart fence for a customization restart.
// Unlike /exit's prepareRestart it refuses while another restart is under
// way, so that releasing the fence after a failure never releases another's.
func (s *Server) reserveRestart() error {
	s.workspace.mu.Lock()
	defer s.workspace.mu.Unlock()
	if s.workspace.restarting {
		return fmt.Errorf("%w: server is already restarting", db.ErrConflict)
	}
	s.workspace.restarting = true
	return nil
}

// autoRestartResponse describes the planned automatic restart on its build's
// installed conversations.
func (s *Server) autoRestartResponse(state *db.Customization) {
	plan := s.customizations.autoRestart.Load()
	if plan == nil || state.Status != "installed" || state.Archived || state.ArtifactSHA256 != plan.Target.ArtifactSHA256 {
		return
	}
	state.AutoRestart = plan.State
	if plan.State == "pending" {
		in := max(s.autoRestartIn(), 0).Milliseconds()
		state.AutoRestartInMS = &in
	}
}

// handleCancelAutoRestart turns the automatic restart off for one build; the
// user can still click Restart Shelley, and a newer build plans another.
func (s *Server) handleCancelAutoRestart(w http.ResponseWriter, r *http.Request) {
	if !s.customizationReady(w, r) {
		return
	}
	var body struct {
		ArtifactSHA256 string `json:"artifact_sha256"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !decodeStrict(w, r, &body) {
		return
	}
	s.customizations.autoMu.Lock()
	defer s.customizations.autoMu.Unlock()
	state, err := s.db.GetCustomization(r.Context(), r.PathValue("id"))
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	if state.Status != "installed" || state.ArtifactSHA256 != body.ArtifactSHA256 {
		s.workspaceHTTPError(w, fmt.Errorf("%w: this build is not installed; refresh", db.ErrConflict))
		return
	}
	err = s.db.RecordCustomizationAutoRestart(r.Context(), db.CustomizationAutoRestart{
		ConversationID: state.ConversationID, Commit: state.Commit, ArtifactSHA256: state.ArtifactSHA256, Event: "cancelled",
	})
	if err == nil {
		_, err = s.refreshAutoRestartPlan(r.Context())
	}
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	s.logger.Info("Auto-restart cancelled by the user", "conversationID", state.ConversationID, "commit", state.Commit)
	s.notifyWorkspaceChanged()
	s.customizationResponse(state)
	s.autoRestartResponse(state)
	writeJSON(w, http.StatusOK, state)
}
