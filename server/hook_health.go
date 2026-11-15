package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const hookHealthSettingKey = "internal.end_of_turn_hook_health"

type HookHealthStatus struct {
	EpisodeID      uint64    `json:"episode_id"`
	Revision       uint64    `json:"revision"`
	Active         bool      `json:"active"`
	Dismissed      bool      `json:"dismissed"`
	Failures       uint64    `json:"failures"`
	LastFailureAt  time.Time `json:"last_failure_at,omitempty"`
	ConversationID string    `json:"conversation_id,omitempty"`
}

type hookHealth struct {
	server *Server
	mu     sync.Mutex
	status HookHealthStatus
}

func (s *Server) initHookHealth(ctx context.Context) error {
	h := s.hookHealth
	h.mu.Lock()
	defer h.mu.Unlock()
	value, err := s.db.GetSetting(ctx, hookHealthSettingKey)
	if err != nil {
		return fmt.Errorf("load end-of-turn hook health: %w", err)
	}
	if value != "" {
		if err := json.Unmarshal([]byte(value), &h.status); err != nil {
			return fmt.Errorf("decode end-of-turn hook health: %w", err)
		}
	}
	path, err := findHookIn(s.hooksDir, hookEndOfTurn)
	if err != nil {
		s.logger.Error("Failed to inspect end-of-turn hook", "error", err)
		return nil
	}
	if path == "" && h.status.Active {
		h.status.Active = false
		h.status.Dismissed = false
		h.status.Failures = 0
		h.status.ConversationID = ""
		h.status.LastFailureAt = time.Time{}
		h.status.Revision++
		if err := h.persist(ctx, h.status); err != nil {
			s.logger.Error("Failed to persist end-of-turn hook health", "error", err)
		}
	}
	return nil
}

func (h *hookHealth) complete(ctx context.Context, conversationID string, runErr error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	next := h.status
	if runErr != nil {
		if !next.Active {
			next.EpisodeID++
			next.Failures = 0
			next.Dismissed = false
		}
		next.Active = true
		next.Failures++
		next.LastFailureAt = time.Now().UTC()
		next.ConversationID = conversationID
	} else {
		if !next.Active {
			return
		}
		next.Active = false
		next.Dismissed = false
		next.Failures = 0
		next.LastFailureAt = time.Time{}
		next.ConversationID = ""
	}
	next.Revision++
	if err := h.persist(ctx, next); err != nil {
		h.server.logger.Error("Failed to persist end-of-turn hook health", "error", err)
	}
	h.status = next
	h.server.streamPub.Broadcast(StreamResponse{HookHealth: &next})
}

func (h *hookHealth) persist(ctx context.Context, status HookHealthStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	if err := h.server.db.SetSetting(ctx, hookHealthSettingKey, string(data)); err != nil {
		return fmt.Errorf("persist end-of-turn hook health: %w", err)
	}
	return nil
}

func (h *hookHealth) dismiss(ctx context.Context, episodeID uint64) (HookHealthStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.status.Active && !h.status.Dismissed && h.status.EpisodeID == episodeID {
		next := h.status
		next.Dismissed = true
		next.Revision++
		if err := h.persist(ctx, next); err != nil {
			return h.status, err
		}
		h.status = next
		h.server.streamPub.Broadcast(StreamResponse{HookHealth: &next})
	}
	return h.status, nil
}

func (s *Server) handleDismissHookHealth(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EpisodeID uint64 `json:"episode_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EpisodeID == 0 {
		http.Error(w, "A positive episode_id is required", http.StatusBadRequest)
		return
	}
	status, err := s.hookHealth.dismiss(r.Context(), req.EpisodeID)
	if err != nil {
		s.internalError(w, "Failed to dismiss hook health warning", err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
