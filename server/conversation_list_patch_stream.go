package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	// recomputeMu serializes recompute() invocations so concurrent commit
	// hooks produce events in a consistent oldHash->newHash chain. Without
	// this, two concurrent recomputes can race after their DB reads and
	// publish events out of order, leaving subscribers with a stale view.
	recomputeMu sync.Mutex

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
	// Serialize: the DB read below and the subsequent currentHash/currentList
	// update must be atomic relative to other recomputes, otherwise a slower
	// reader can overwrite a fresher snapshot and emit a patch that walks
	// state backwards.
	cls.recomputeMu.Lock()
	defer cls.recomputeMu.Unlock()

	nextList, err := cls.server.conversationListWithStateInternal(ctx, 5000, 0, "", false, true)
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

// snapshot returns the current list and its hash, recomputing first to make
// sure the value reflects the latest committed state. Callers use this to
// seed clients before they subscribe.
func (cls *conversationListStream) snapshot(ctx context.Context) ([]ConversationWithState, string, error) {
	if err := cls.recompute(ctx); err != nil {
		return nil, "", err
	}
	cls.mu.Lock()
	defer cls.mu.Unlock()
	list := append([]ConversationWithState(nil), cls.currentList...)
	return list, cls.currentHash, nil
}

func (cls *conversationListStream) debugHistory() []ConversationListPatchEvent {
	cls.mu.Lock()
	defer cls.mu.Unlock()
	return append([]ConversationListPatchEvent(nil), cls.history...)
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
