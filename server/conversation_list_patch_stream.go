package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const conversationListPatchHistoryLimit = 100

// conversationListPatchOp is one RFC 6902 JSON Patch operation.
type conversationListPatchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	From  string          `json:"from,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

// ConversationListPatchEvent is a single update broadcast to listeners.
// Clients identify their starting state via OldHash and verify the resulting
// state via NewHash. Reset events carry a single replace op for the root.
type ConversationListPatchEvent struct {
	OldHash *string                   `json:"old_hash"`
	NewHash string                    `json:"new_hash"`
	Patch   []conversationListPatchOp `json:"patch"`
	At      time.Time                 `json:"at"`
	Reset   bool                      `json:"reset,omitempty"`
}

type conversationListStream struct {
	server *Server

	mu          sync.Mutex
	cond        *sync.Cond
	listeners   int
	currentList []ConversationWithState
	currentHash string
	history     []ConversationListPatchEvent
}

func newConversationListStream(server *Server) *conversationListStream {
	cls := &conversationListStream{server: server}
	cls.cond = sync.NewCond(&cls.mu)
	return cls
}

func (cls *conversationListStream) hasListeners() bool {
	cls.mu.Lock()
	defer cls.mu.Unlock()
	return cls.listeners > 0
}

func (cls *conversationListStream) notify(ctx context.Context) error {
	if !cls.hasListeners() {
		return nil
	}
	return cls.recompute(ctx)
}

func (cls *conversationListStream) recompute(ctx context.Context) error {
	nextList, err := cls.server.conversationListWithState(ctx, 5000, 0, "", false)
	if err != nil {
		return err
	}
	nextHash, err := hashList(nextList)
	if err != nil {
		return err
	}

	cls.mu.Lock()
	defer cls.mu.Unlock()
	if cls.currentHash == nextHash {
		return nil
	}

	patch, err := computeListPatch(cls.currentList, nextList)
	if err != nil {
		return err
	}

	oldHash := cls.currentHash
	var oldHashPtr *string
	if oldHash != "" {
		oldHashCopy := oldHash
		oldHashPtr = &oldHashCopy
	}
	event := ConversationListPatchEvent{
		OldHash: oldHashPtr,
		NewHash: nextHash,
		Patch:   patch,
		At:      time.Now(),
	}
	cls.currentList = nextList
	cls.currentHash = nextHash
	cls.history = append(cls.history, event)
	if len(cls.history) > conversationListPatchHistoryLimit {
		cls.history = cls.history[len(cls.history)-conversationListPatchHistoryLimit:]
	}
	cls.cond.Broadcast()
	return nil
}

func hashList(list []ConversationWithState) (string, error) {
	raw, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func resetEvent(currentList []ConversationWithState, currentHash, oldHash string) (ConversationListPatchEvent, error) {
	rootValue, err := json.Marshal(currentList)
	if err != nil {
		return ConversationListPatchEvent{}, err
	}
	if currentList == nil {
		// Ensure null becomes []
		rootValue = []byte("[]")
	}
	var oldHashPtr *string
	if oldHash != "" {
		oldHashCopy := oldHash
		oldHashPtr = &oldHashCopy
	}
	return ConversationListPatchEvent{
		OldHash: oldHashPtr,
		NewHash: currentHash,
		Patch: []conversationListPatchOp{{
			Op:    "replace",
			Path:  "",
			Value: rootValue,
		}},
		At:    time.Now(),
		Reset: true,
	}, nil
}

func (cls *conversationListStream) connect(ctx context.Context, oldHash string) ([]ConversationListPatchEvent, func() (ConversationListPatchEvent, bool), func(), error) {
	cls.mu.Lock()
	cls.listeners++
	cls.mu.Unlock()

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			cls.mu.Lock()
			defer cls.mu.Unlock()
			if cls.listeners > 0 {
				cls.listeners--
			}
			cls.cond.Broadcast()
		})
	}

	go func() {
		<-ctx.Done()
		release()
	}()

	if err := cls.recompute(ctx); err != nil {
		release()
		return nil, nil, nil, err
	}

	cls.mu.Lock()
	initial, startIdx, err := cls.initialEventsLocked(oldHash)
	cls.mu.Unlock()
	if err != nil {
		release()
		return nil, nil, nil, err
	}

	next := func() (ConversationListPatchEvent, bool) {
		cls.mu.Lock()
		defer cls.mu.Unlock()
		for {
			if ctx.Err() != nil {
				return ConversationListPatchEvent{}, false
			}
			if startIdx < len(cls.history) {
				event := cls.history[startIdx]
				startIdx++
				return event, true
			}
			cls.cond.Wait()
		}
	}
	return initial, next, release, nil
}

// initialEventsLocked decides how to seed a newly connected client.
// Cases:
//   - No old hash: send a reset to the current state.
//   - Old hash equals current: nothing to send; future events stream live.
//   - Old hash matches a known event boundary: replay forward from there.
//   - Otherwise: reset.
func (cls *conversationListStream) initialEventsLocked(oldHash string) ([]ConversationListPatchEvent, int, error) {
	if oldHash != "" && oldHash == cls.currentHash {
		return nil, len(cls.history), nil
	}
	if oldHash != "" {
		for i, event := range cls.history {
			if event.OldHash != nil && *event.OldHash == oldHash {
				replay := append([]ConversationListPatchEvent(nil), cls.history[i:]...)
				return replay, len(cls.history), nil
			}
		}
	}
	reset, err := resetEvent(cls.currentList, cls.currentHash, oldHash)
	if err != nil {
		return nil, 0, err
	}
	return []ConversationListPatchEvent{reset}, len(cls.history), nil
}

func (cls *conversationListStream) debugHistory() []ConversationListPatchEvent {
	cls.mu.Lock()
	defer cls.mu.Unlock()
	return append([]ConversationListPatchEvent(nil), cls.history...)
}

func (s *Server) handleConversationListStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	initial, next, release, err := s.conversationListStream.connect(r.Context(), r.URL.Query().Get("old_hash"))
	if err != nil {
		s.logger.Error("failed to initialize conversation list stream", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer release()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	writeEvent := func(event ConversationListPatchEvent) bool {
		if err := writeSSEJSON(w, "patch", event); err != nil {
			s.logger.Debug("conversation list stream write failed", "error", err)
			return false
		}
		flusher.Flush()
		return true
	}
	for _, event := range initial {
		if !writeEvent(event) {
			return
		}
	}
	for {
		event, ok := next()
		if !ok {
			return
		}
		if !writeEvent(event) {
			return
		}
	}
}

func writeSSEJSON(w http.ResponseWriter, event string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
	return err
}

func (s *Server) handleDebugConversationStreamHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.conversationListStream.debugHistory()); err != nil {
		s.logger.Debug("failed to write conversation stream history", "error", err)
	}
}
