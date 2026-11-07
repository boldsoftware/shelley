package server

import (
	"context"
	"net/http"
	"os"
	"time"
)

// Once a new binary is installed over the running one, Shelley restarts
// into it by itself when nothing is working and the user has left the UI
// alone for idleRestartAfter, as POST /exit?resume=true would.
const (
	idleRestartAfter    = 10 * time.Second
	idleRestartInterval = time.Second
)

// defaultRestartPending reports whether a restart would run a new binary,
// and systemd would start it again.
func defaultRestartPending() bool {
	if os.Getenv("INVOCATION_ID") == "" {
		return false
	}
	executable, err := upgradeTarget()
	return err == nil && restartPending(executable)
}

func (s *Server) noteUserActivity() {
	s.lastUserActivity.Store(time.Now().UnixNano())
}

// handleActivity is the UI's heartbeat while the user interacts with it.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	s.noteUserActivity()
	s.publishCustomizations() // The restart deadline moved.
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) idleRestartRoutine() {
	ticker := time.NewTicker(idleRestartInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if s.idleRestartTick(context.Background()) {
				return
			}
			s.publishCustomizations() // Work may have started or finished.
		case <-s.shutdownCh:
			return
		}
	}
}

// idleRestartIn is how much longer the user must stay idle.
func (s *Server) idleRestartIn() time.Duration {
	return time.Until(time.Unix(0, s.lastUserActivity.Load()).Add(idleRestartAfter))
}

// restartBusy reports whether a turn runs or a task is starting; a running
// action makes prepareRestart refuse.
func (s *Server) restartBusy() bool {
	if !s.isServerIdle() {
		return true
	}
	s.workspace.mu.Lock()
	defer s.workspace.mu.Unlock()
	return len(s.workspace.initializing) != 0
}

// idleRestartTick restarts into an installed binary if the user is idle and
// no turn, starting task or action is running. It reports whether it did.
func (s *Server) idleRestartTick(ctx context.Context) bool {
	if !s.restartPendingNow() || s.idleRestartIn() > 0 || s.restartBusy() || s.autoRestartCancelled() {
		return false
	}
	if err := s.prepareRestart(ctx); err != nil {
		return false // An action is running.
	}
	if err := s.markResumeAfterRestart(ctx); err != nil {
		s.releaseRestart()
		s.logger.Error("Idle restart: failed to mark conversations for resume", "error", err)
		return false
	}
	s.logger.Info("Restarting into the installed Shelley binary: the user is idle")
	s.scheduleExit(restartExitCode)
	return true
}
