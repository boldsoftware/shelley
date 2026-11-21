package server

import (
	"context"
	"sync"
)

// assistantWatch tells clients, with workspace_changed, when the Assistant
// changes. Any commit may change it, by creating, archiving, or relabelling
// a conversation with the Assistant profile, so each commit schedules a
// check, coalesced with the others while one runs.
type assistantWatch struct {
	mu       sync.Mutex
	dirty    bool
	checking bool
	// id is the Assistant's id as of the last check, under mu.
	id string
}

// watchAssistant starts telling clients when the Assistant changes from
// what it is now.
func (s *Server) watchAssistant() {
	id, err := s.assistantID(context.Background())
	if err != nil {
		s.logger.Warn("Failed to read the Assistant", "error", err)
	}
	w := &s.workspace.assistant
	w.mu.Lock()
	w.id = id
	w.mu.Unlock()
	s.db.Pool().OnCommit(s.assistantMayHaveChanged)
}

func (s *Server) assistantMayHaveChanged() {
	w := &s.workspace.assistant
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dirty = true
	if !w.checking {
		w.checking = true
		go s.checkAssistant()
	}
}

func (s *Server) checkAssistant() {
	w := &s.workspace.assistant
	for {
		w.mu.Lock()
		if !w.dirty {
			w.checking = false
			w.mu.Unlock()
			return
		}
		w.dirty = false
		w.mu.Unlock()

		id, err := s.assistantID(context.Background())
		if err != nil {
			s.logger.Warn("Failed to check for a new Assistant", "error", err)
			continue
		}
		w.mu.Lock()
		changed := id != w.id
		w.id = id
		w.mu.Unlock()
		if changed {
			s.notifyWorkspaceChanged()
		}
	}
}
