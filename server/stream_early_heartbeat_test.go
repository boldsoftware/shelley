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

// TestConversationStreamFlushesEarlyHeartbeat verifies that opening a per-conversation
// stream produces an SSE flush *before* any blocking work (Hydrate / DB reads / list
// recompute) so clients (and tests) never wait on the first byte.
//
// Without the early heartbeat, the first flush blocks on Hydrate + the conversation
// list snapshot, which on a cold cache shells out to git and walks the working tree.
// On loaded CI workers that has timed out the historical 2s test ceiling.
func TestConversationStreamFlushesEarlyHeartbeat(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conv, err := database.CreateConversation(context.Background(), strPtr("early-hb"), true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Prime the conversation list snapshot and capture its hash so the stream
	// has no list replay to emit — isolating the per-conversation first-flush.
	if err := server.conversationListStream.recompute(context.Background()); err != nil {
		t.Fatal(err)
	}
	currentHash := server.conversationListStream.currentHash

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/stream?conversation="+conv.ConversationID+"&conversation_list_hash="+currentHash, nil,
	).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		server.handleStream(rec, req)
		close(done)
	}()

	// Wait for the first flush. With the early heartbeat in place this should
	// complete almost instantly; the timeout exists purely to fail loudly.
	select {
	case <-rec.flushed:
	case <-time.After(2 * time.Second):
		t.Fatalf("no flush within 2s; body=%q", rec.getString())
	}

	// The first emitted SSE message must be a bare heartbeat (no Messages,
	// no Conversation, no list patch) — proving the flush happened before
	// Hydrate's slow paths populated the response.
	body := rec.getString()
	parts := strings.SplitN(body, "\n\n", 2)
	if len(parts) < 1 || !strings.HasPrefix(parts[0], "data: ") {
		t.Fatalf("expected first chunk to start with 'data: ', got %q", body)
	}
	var first StreamResponse
	if err := json.Unmarshal([]byte(strings.TrimPrefix(parts[0], "data: ")), &first); err != nil {
		t.Fatalf("unmarshal first chunk: %v; body=%q", err, body)
	}
	if !first.Heartbeat {
		t.Fatalf("first chunk should be a heartbeat, got %+v", first)
	}
	if len(first.Messages) != 0 || first.Conversation.ConversationID != "" || first.ConversationListPatch != nil {
		t.Fatalf("early heartbeat should be bare, got %+v", first)
	}

	cancel()
	<-done
}

// TestConversationListOnlyStreamDoesNotSendEarlyHeartbeat preserves the
// existing contract for the list-only stream: when the client supplies a
// conversation_list_hash that matches the current snapshot and no conversation,
// the server stays silent until something actually changes.
func TestConversationListOnlyStreamDoesNotSendEarlyHeartbeat(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)
	if err := server.conversationListStream.recompute(context.Background()); err != nil {
		t.Fatal(err)
	}
	currentHash := server.conversationListStream.currentHash

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream?conversation_list_hash="+currentHash, nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		server.handleStream(rec, req)
		close(done)
	}()

	select {
	case <-rec.flushed:
		t.Fatalf("list-only stream should be silent until a change; got body=%q", rec.getString())
	case <-time.After(150 * time.Millisecond):
	}

	cancel()
	<-done
}
