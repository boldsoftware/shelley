package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"
)

// customization is a task's latest scripts/install-custom run, as the
// script reports it. It lives in memory: a restart runs the installed
// build, after which there is nothing left to show.
type customization struct {
	ConversationID string    `json:"conversation_id"`
	Status         string    `json:"status"` // verifying, failed, installed or restarting
	Commit         string    `json:"commit"`
	Error          string    `json:"error"`
	UpdatedAt      time.Time `json:"updated_at"`
	// The fields below are computed for each response.
	RunningUnderSystemd bool `json:"running_under_systemd"`
	// AutoRestart is pending (in AutoRestartInMS more of user idleness) or
	// busy (once work finishes) for the installed build.
	AutoRestart     string `json:"auto_restart,omitempty"`
	AutoRestartInMS *int64 `json:"auto_restart_in_ms,omitempty"`

	cancelled bool // The user turned the automatic restart off.
}

type customizationState struct {
	mu      sync.Mutex
	entries map[string]*customization
	// published is what the stream last got, to send only changes.
	published string
}

// handleReportCustomization records a step of a task's install-custom run.
func (s *Server) handleReportCustomization(w http.ResponseWriter, r *http.Request) {
	var req struct{ Status, Commit, Error string }
	if !decodeJSON(w, r, &req) {
		return
	}
	switch req.Status {
	case "verifying", "failed", "installed":
	default:
		http.Error(w, "status must be verifying, failed or installed", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	s.customizations.mu.Lock()
	if s.customizations.entries == nil {
		s.customizations.entries = map[string]*customization{}
	}
	if req.Status == "installed" {
		// The new binary replaced every other installed one.
		for other, entry := range s.customizations.entries {
			if entry.Status == "installed" {
				delete(s.customizations.entries, other)
			}
		}
	}
	entry := &customization{ConversationID: id, Status: req.Status, Commit: req.Commit, Error: req.Error, UpdatedAt: time.Now()}
	s.customizations.entries[id] = entry
	out := s.customizationView(entry)
	s.customizations.mu.Unlock()
	s.publishCustomizations()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListCustomizations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, *s.customizationList())
}

func (s *Server) customizationList() *[]customization {
	s.customizations.mu.Lock()
	list := make([]customization, 0, len(s.customizations.entries))
	for _, entry := range s.customizations.entries {
		list = append(list, s.customizationView(entry))
	}
	s.customizations.mu.Unlock()
	slices.SortFunc(list, func(a, b customization) int { return a.UpdatedAt.Compare(b.UpdatedAt) })
	return &list
}

// publishCustomizations sends the states to the stream if they changed
// since it last did: a report, a cancel, a restart, work starting or
// finishing, or user activity moving the restart deadline.
func (s *Server) publishCustomizations() {
	list := s.customizationList()
	// Compare deadlines, not the time left, which changes every call.
	type plan struct {
		customization
		Deadline int64
	}
	plans := make([]plan, len(*list))
	for i, entry := range *list {
		plans[i] = plan{entry, 0}
		if entry.AutoRestartInMS != nil {
			plans[i].AutoRestartInMS = nil
			plans[i].Deadline = s.lastUserActivity.Load()
		}
	}
	key, _ := json.Marshal(plans)
	s.customizations.mu.Lock()
	changed := s.customizations.published != string(key)
	s.customizations.published = string(key)
	s.customizations.mu.Unlock()
	if changed && s.streamPub != nil {
		s.streamPub.Broadcast(StreamResponse{Customizations: list})
	}
}

// installedCustomization returns conversation id's installed build. The
// caller holds customizations.mu.
func (s *Server) installedCustomization(id string) (*customization, error) {
	entry := s.customizations.entries[id]
	if entry == nil || entry.Status != "installed" {
		return nil, fmt.Errorf("this task has no installed build")
	}
	return entry, nil
}

// handleRestartCustomization restarts into the task's installed build, as
// POST /exit?resume=true does.
func (s *Server) handleRestartCustomization(w http.ResponseWriter, r *http.Request) {
	var req struct{ Commit string }
	if !decodeJSON(w, r, &req) {
		return
	}
	s.customizations.mu.Lock()
	defer s.customizations.mu.Unlock()
	entry, err := s.installedCustomization(r.PathValue("id"))
	if err == nil && entry.Commit != req.Commit {
		err = fmt.Errorf("another build was installed since")
	}
	if err == nil && !s.restartPendingNow() {
		err = fmt.Errorf("the installed build can't be restarted into; restart Shelley by hand")
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := s.prepareRestart(r.Context()); err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	if err := s.markResumeAfterRestart(r.Context()); err != nil {
		s.releaseRestart()
		s.internalError(w, "Failed to prepare conversations for restart", err)
		return
	}
	entry.Status = "restarting"
	out := s.customizationView(entry)
	s.customizations.mu.Unlock()
	s.publishCustomizations()
	s.customizations.mu.Lock()
	writeJSON(w, http.StatusOK, out)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	s.scheduleExit(restartExitCode)
}

// handleCancelAutoRestart turns the automatic restart off for the task's
// installed build; Restart Shelley still works, and a newer install plans
// another.
func (s *Server) handleCancelAutoRestart(w http.ResponseWriter, r *http.Request) {
	s.customizations.mu.Lock()
	defer s.customizations.mu.Unlock()
	entry, err := s.installedCustomization(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	entry.cancelled = true
	out := s.customizationView(entry)
	s.customizations.mu.Unlock()
	s.publishCustomizations()
	s.customizations.mu.Lock()
	writeJSON(w, http.StatusOK, out)
}

// autoRestartCancelled reports whether the user turned the automatic restart
// off for the installed build.
func (s *Server) autoRestartCancelled() bool {
	s.customizations.mu.Lock()
	defer s.customizations.mu.Unlock()
	for _, entry := range s.customizations.entries {
		if entry.Status == "installed" && entry.cancelled {
			return true
		}
	}
	return false
}

// customizationView adds the computed fields. The caller holds
// customizations.mu.
func (s *Server) customizationView(entry *customization) customization {
	out := *entry
	out.RunningUnderSystemd = os.Getenv("INVOCATION_ID") != ""
	if entry.Status != "installed" || entry.cancelled || !s.restartPendingNow() {
		return out
	}
	if s.restartBusy() {
		out.AutoRestart = "busy"
		return out
	}
	out.AutoRestart = "pending"
	in := max(s.idleRestartIn(), 0).Milliseconds()
	out.AutoRestartInMS = &in
	return out
}
