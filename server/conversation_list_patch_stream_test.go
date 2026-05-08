package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

func TestConversationListPatchStreamInitialResetAndNewConversation(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/conversations/stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		server.handleConversationListStream(rec, req)
		close(done)
	}()

	initial := waitForPatchEventAfter(t, rec, "")
	if !initial.Reset || initial.OldHash != nil || initial.NewHash == "" {
		t.Fatalf("bad initial event: %+v", initial)
	}
	state := []ConversationWithState{}
	state = mustApplyPatch(t, state, initial.Patch)
	if len(state) != 0 {
		t.Fatalf("expected empty initial state, got %d", len(state))
	}

	slug := "stream-test"
	if _, err := database.CreateConversation(context.Background(), &slug, true, nil, nil, db.ConversationOptions{}); err != nil {
		t.Fatal(err)
	}
	server.publishConversationListUpdate(ConversationListUpdate{Type: "update"})

	update := waitForPatchEventAfter(t, rec, initial.NewHash)
	if update.OldHash == nil || *update.OldHash != initial.NewHash || update.NewHash == initial.NewHash {
		t.Fatalf("bad update hashes: %+v", update)
	}
	if update.Reset {
		t.Fatalf("expected granular update, got reset")
	}
	if len(update.Patch) != 1 || update.Patch[0].Op != "add" || update.Patch[0].Path != "/0" {
		t.Fatalf("expected single add op, got %+v", update.Patch)
	}
	state = mustApplyPatch(t, state, update.Patch)
	if len(state) != 1 || state[0].Slug == nil || *state[0].Slug != slug {
		t.Fatalf("unexpected state: %+v", state)
	}
	verifyHash(t, state, update.NewHash)

	cancel()
	<-done
}

func TestConversationListPatchStreamReplaysHistoryFromOldHash(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/conversations/stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		server.handleConversationListStream(rec, req)
		close(done)
	}()
	initial := waitForPatchEventAfter(t, rec, "")
	lastHash := initial.NewHash
	for _, slug := range []string{"one", "two"} {
		if _, err := database.CreateConversation(context.Background(), &slug, true, nil, nil, db.ConversationOptions{}); err != nil {
			t.Fatal(err)
		}
		server.publishConversationListUpdate(ConversationListUpdate{Type: "update"})
		lastHash = waitForPatchEventAfter(t, rec, lastHash).NewHash
	}
	cancel()
	<-done

	replayCtx, replayCancel := context.WithCancel(context.Background())
	defer replayCancel()
	replayRec := newFlusherRecorder()
	replayReq := httptest.NewRequest(http.MethodGet, "/api/conversations/stream?old_hash="+initial.NewHash, nil).WithContext(replayCtx)
	replayDone := make(chan struct{})
	go func() {
		server.handleConversationListStream(replayRec, replayReq)
		close(replayDone)
	}()
	first := waitForPatchEventAfter(t, replayRec, initial.NewHash)
	second := waitForPatchEventAfter(t, replayRec, first.NewHash)
	if first.Reset || second.Reset {
		t.Fatalf("expected non-reset replay, got %+v then %+v", first, second)
	}
	if first.OldHash == nil || *first.OldHash != initial.NewHash {
		t.Fatalf("first replay should start at original hash; got %+v", first)
	}
	if second.OldHash == nil || *second.OldHash != first.NewHash {
		t.Fatalf("second replay must chain off first; got %+v", second)
	}
	state := []ConversationWithState{}
	state = mustApplyPatch(t, state, first.Patch)
	state = mustApplyPatch(t, state, second.Patch)
	if len(state) != 2 {
		t.Fatalf("expected 2 conversations after replay, got %d", len(state))
	}
	verifyHash(t, state, second.NewHash)
	replayCancel()
	<-replayDone
}

func TestConversationListPatchStreamUnknownHashStartsOver(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	slug := "existing"
	if _, err := database.CreateConversation(context.Background(), &slug, true, nil, nil, db.ConversationOptions{}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/conversations/stream?old_hash=bogus", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		server.handleConversationListStream(rec, req)
		close(done)
	}()
	event := waitForPatchEventAfter(t, rec, "")
	if !event.Reset || event.OldHash == nil || *event.OldHash != "bogus" {
		t.Fatalf("expected reset from bogus hash, got %+v", event)
	}
	state := []ConversationWithState{}
	state = mustApplyPatch(t, state, event.Patch)
	if len(state) != 1 || state[0].Slug == nil || *state[0].Slug != slug {
		t.Fatalf("unexpected reset state: %+v", state)
	}
	cancel()
	<-done
}

func TestConversationListPatchStreamWorkingState(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	conv, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := server.getOrCreateConversationManager(context.Background(), conv.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/conversations/stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		server.handleConversationListStream(rec, req)
		close(done)
	}()
	state := []ConversationWithState{}
	initialEvent := waitForPatchEventAfter(t, rec, "")
	state = mustApplyPatch(t, state, initialEvent.Patch)
	if len(state) != 1 || state[0].Working {
		t.Fatalf("expected idle state: %+v", state)
	}

	manager.SetAgentWorking(true)
	change := waitForPatchEventAfter(t, rec, initialEvent.NewHash)
	if change.Reset {
		t.Fatalf("expected granular working-state patch, got reset")
	}
	if len(change.Patch) != 1 || change.Patch[0].Op != "replace" || change.Patch[0].Path != "/0/working" {
		t.Fatalf("expected single replace of /0/working, got %+v", change.Patch)
	}
	state = mustApplyPatch(t, state, change.Patch)
	if !state[0].Working {
		t.Fatalf("expected working=true after patch")
	}
	verifyHash(t, state, change.NewHash)
	cancel()
	<-done
}

func TestConversationListPatchStreamRemovesAndReorders(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	a, err := database.CreateConversation(context.Background(), strPtr("a"), true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateConversation(context.Background(), strPtr("b"), true, nil, nil, db.ConversationOptions{}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/conversations/stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { server.handleConversationListStream(rec, req); close(done) }()

	state := []ConversationWithState{}
	initial := waitForPatchEventAfter(t, rec, "")
	state = mustApplyPatch(t, state, initial.Patch)
	if len(state) != 2 {
		t.Fatalf("want 2 initial entries, got %d", len(state))
	}

	if err := database.DeleteConversation(context.Background(), a.ConversationID); err != nil {
		t.Fatal(err)
	}
	server.publishConversationListUpdate(ConversationListUpdate{Type: "delete"})
	ev := waitForPatchEventAfter(t, rec, initial.NewHash)
	if ev.Reset {
		t.Fatalf("expected granular patch, got reset")
	}
	hasRemove := false
	for _, op := range ev.Patch {
		if op.Op == "remove" {
			hasRemove = true
		}
	}
	if !hasRemove {
		t.Fatalf("expected a remove op, got %+v", ev.Patch)
	}
	state = mustApplyPatch(t, state, ev.Patch)
	if len(state) != 1 {
		t.Fatalf("want 1 entry after delete, got %d", len(state))
	}
	verifyHash(t, state, ev.NewHash)
	cancel()
	<-done
}

func TestConversationListPatchStreamCurrentHashSkipsInitial(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)
	// Prime current state.
	if err := server.conversationListStream.recompute(context.Background()); err != nil {
		t.Fatal(err)
	}
	currentHash := server.conversationListStream.currentHash

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/conversations/stream?old_hash="+currentHash, nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { server.handleConversationListStream(rec, req); close(done) }()

	select {
	case <-rec.flushed:
		t.Fatalf("did not expect any events; body=%s", rec.getString())
	case <-time.After(150 * time.Millisecond):
	}
	cancel()
	<-done
}

func TestConversationListPatchStreamHistoryEndpoint(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _, release, err := server.conversationListStream.connect(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{}); err != nil {
		t.Fatal(err)
	}
	server.publishConversationListUpdate(ConversationListUpdate{Type: "update"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/conversation-stream/history", nil)
	server.handleDebugConversationStreamHistory(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var events []ConversationListPatchEvent
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected history")
	}
}

func waitForPatchEventAfter(t *testing.T, rec *flusherRecorder, prevHash string) ConversationListPatchEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range parsePatchEvents(rec.getString()) {
			if (prevHash == "" && (ev.OldHash == nil || *ev.OldHash == "" || ev.Reset)) ||
				(prevHash != "" && ev.OldHash != nil && *ev.OldHash == prevHash) {
				return ev
			}
		}
		select {
		case <-rec.flushed:
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for patch event after %q; body=%s", prevHash, rec.getString())
	return ConversationListPatchEvent{}
}

func parsePatchEvents(body string) []ConversationListPatchEvent {
	var events []ConversationListPatchEvent
	for _, part := range strings.Split(body, "\n\n") {
		if !strings.HasPrefix(part, "event: patch\n") {
			continue
		}
		for _, line := range strings.Split(part, "\n") {
			if strings.HasPrefix(line, "data: ") {
				var ev ConversationListPatchEvent
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err == nil {
					events = append(events, ev)
				}
			}
		}
	}
	return events
}

func mustApplyPatch(t *testing.T, state []ConversationWithState, patch []conversationListPatchOp) []ConversationWithState {
	t.Helper()
	out, err := applyTestPatch(state, patch)
	if err != nil {
		t.Fatalf("apply patch: %v (ops=%+v)", err, patch)
	}
	return out
}

func verifyHash(t *testing.T, state []ConversationWithState, want string) {
	t.Helper()
	got, err := hashList(state)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("hash mismatch after applying patch: got %s want %s", got, want)
	}
}

func strPtr(s string) *string { return &s }
