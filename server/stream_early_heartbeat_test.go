package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/stream2?conversation="+conv.ConversationID+"&conversation_list_hash="+currentHash, nil,
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
	if len(first.Messages) != 0 || first.Conversation != nil || first.ConversationListPatch != nil {
		t.Fatalf("early heartbeat should be bare, got %+v", first)
	}

	cancel()
	<-done
}

// TestConversationListOnlyStreamFlushesEarlyHeartbeat verifies that a caught-up
// list-only stream still commits its headers immediately. Android bounds the
// response-header wait, so leaving this path silent until the 30s periodic
// heartbeat makes a healthy resume indistinguishable from a hung proxy.
func TestConversationListOnlyStreamFlushesEarlyHeartbeat(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)
	if err := server.conversationListStream.recompute(context.Background()); err != nil {
		t.Fatal(err)
	}
	currentHash := server.conversationListStream.currentHash

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream2?conversation_list_hash="+currentHash, nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		server.handleStream(rec, req)
		close(done)
	}()

	select {
	case <-rec.flushed:
	case <-time.After(2 * time.Second):
		t.Fatalf("list-only stream did not flush an early heartbeat; body=%q", rec.getString())
	}

	parts := strings.SplitN(rec.getString(), "\n\n", 2)
	if len(parts) < 1 || !strings.HasPrefix(parts[0], "data: ") {
		t.Fatalf("expected first chunk to start with 'data: ', got %q", rec.getString())
	}
	var first StreamResponse
	if err := json.Unmarshal([]byte(strings.TrimPrefix(parts[0], "data: ")), &first); err != nil {
		t.Fatalf("unmarshal first chunk: %v; body=%q", err, rec.getString())
	}
	if !first.Heartbeat || len(first.Messages) != 0 || first.Conversation != nil || first.ConversationListPatch != nil {
		t.Fatalf("list-only first chunk should be a bare heartbeat, got %+v", first)
	}

	cancel()
	<-done
}

type queueLogWriter struct {
	mu                   sync.Mutex
	logs                 bytes.Buffer
	updatesQueueFull     chan struct{}
	subscriberQueueFull  chan struct{}
	updatesQueueFullOnce sync.Once
	subscriberFullOnce   sync.Once
}

func newQueueLogWriter() *queueLogWriter {
	return &queueLogWriter{
		updatesQueueFull:    make(chan struct{}),
		subscriberQueueFull: make(chan struct{}),
	}
}

func (w *queueLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if bytes.Contains(p, []byte("SSE updates queue saturated; subscriber may be disconnected")) {
		w.updatesQueueFullOnce.Do(func() { close(w.updatesQueueFull) })
	}
	if bytes.Contains(p, []byte("SSE subscriber queue full;")) {
		w.subscriberFullOnce.Do(func() { close(w.subscriberQueueFull) })
	}
	return w.logs.Write(p)
}

func (w *queueLogWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.logs.String()
}

func TestStreamUpdatesQueueLogsWhenFull(t *testing.T) {
	if streamUpdatesQueueCapacity != 200 {
		t.Fatalf("streamUpdatesQueueCapacity = %d, want 200", streamUpdatesQueueCapacity)
	}
	logs := newQueueLogWriter()
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &Server{logger: logger}
	updates := server.newStreamUpdatesQueue(ctx, "test-conversation")
	for range streamUpdatesQueueCapacity {
		if !updates.enqueue(ctx, StreamResponse{Heartbeat: true}) {
			t.Fatal("enqueue failed before queue filled")
		}
	}

	enqueued := make(chan bool, 1)
	go func() {
		enqueued <- updates.enqueue(ctx, StreamResponse{Heartbeat: true})
	}()
	select {
	case <-logs.updatesQueueFull:
	case <-time.After(2 * time.Second):
		t.Fatalf("updates queue fill was not logged:\n%s", logs.String())
	}
	cancel()
	if <-enqueued {
		t.Fatal("enqueue succeeded despite a full queue")
	}
}

// blockingStreamWriter models a browser/proxy that stops reading an SSE
// response long enough for a subscriber buffer to overflow.
type blockingStreamWriter struct {
	header      http.Header
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
	deadlineMu  sync.Mutex
	deadlines   []time.Time
}

func newBlockingStreamWriter() *blockingStreamWriter {
	return &blockingStreamWriter{
		header:  make(http.Header),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (w *blockingStreamWriter) Header() http.Header { return w.header }
func (w *blockingStreamWriter) WriteHeader(int)     {}
func (w *blockingStreamWriter) Flush()              {}
func (w *blockingStreamWriter) Write(p []byte) (int, error) {
	w.enteredOnce.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func (w *blockingStreamWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlineMu.Lock()
	w.deadlines = append(w.deadlines, deadline)
	w.deadlineMu.Unlock()
	if !deadline.IsZero() {
		w.releaseWrite()
	}
	return nil
}

func (w *blockingStreamWriter) recordedDeadlines() []time.Time {
	w.deadlineMu.Lock()
	defer w.deadlineMu.Unlock()
	return append([]time.Time(nil), w.deadlines...)
}

func (w *blockingStreamWriter) releaseWrite() {
	w.releaseOnce.Do(func() { close(w.release) })
}

// TestUnifiedStreamClosesWhenItsSubscriptionFallsBehind verifies that a
// dropped streamPub subscription tears down the HTTP response. If the handler
// instead keeps sending local heartbeats, EventSource believes the connection
// is healthy forever even though all conversation updates have stopped. The
// browser can only recover missed messages after the response closes and it
// reconnects.
func TestUnifiedStreamClosesWhenItsSubscriptionFallsBehind(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)
	logs := newQueueLogWriter()
	server.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := server.conversationListStream.recompute(context.Background()); err != nil {
		t.Fatal(err)
	}
	currentHash := server.conversationListStream.currentHash

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newBlockingStreamWriter()
	req := httptest.NewRequest(http.MethodGet, "/api/stream2?conversation_list_hash="+currentHash, nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		server.handleStream(w, req)
		close(done)
	}()

	// The caught-up stream writes its early heartbeat immediately. Wait for that
	// write to block, then overflow streamPub. This is also an ordering check:
	// the handler must subscribe before writing the heartbeat, or the burst has
	// no subscriber to drop and the response stays falsely healthy forever.
	select {
	case <-w.entered:
	case <-time.After(2 * time.Second):
		w.releaseWrite()
		cancel()
		<-done
		t.Fatal("stream never started writing")
	}

	// With the response blocked, this burst exceeds both bounded queues and
	// disconnects the subscriber to preserve publisher liveness.
	for range 1000 {
		server.streamPub.Broadcast(StreamResponse{Heartbeat: true})
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		w.releaseWrite()
		cancel()
		<-done
		t.Fatal("unified stream stayed open after its streamPub subscription was dropped")
	}

	deadlines := w.recordedDeadlines()
	if len(deadlines) != 2 || deadlines[0].IsZero() || !deadlines[1].IsZero() {
		t.Fatalf("write deadlines = %v, want forced deadline followed by clear", deadlines)
	}
	select {
	case <-logs.subscriberQueueFull:
	default:
		t.Fatalf("subscriber queue fill was not logged:\n%s", logs.String())
	}
}

func TestLegacyStreamLogsAndStaysOpenWhenSubscriptionFallsBehind(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	logs := newQueueLogWriter()
	server.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	conversation, err := database.CreateConversation(context.Background(), strPtr("legacy-stream-overflow"), true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := server.getOrCreateConversationManager(context.Background(), conversation.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := newBlockingStreamWriter()
	req := httptest.NewRequest(http.MethodGet, "/api/conversation/"+conversation.ConversationID+"/stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		server.handleStreamConversation(w, req, conversation.ConversationID)
		close(done)
	}()
	defer func() {
		w.releaseWrite()
		cancel()
		<-done
	}()

	select {
	case <-w.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("legacy stream never started writing")
	}
	for range 1000 {
		manager.subpub.Broadcast(StreamResponse{Heartbeat: true})
	}
	select {
	case <-logs.subscriberQueueFull:
	case <-time.After(2 * time.Second):
		t.Fatalf("subscriber queue fill was not logged:\n%s", logs.String())
	}
	select {
	case <-done:
		t.Fatal("legacy stream closed even though one-shot clients cannot reconnect")
	default:
	}
	if deadlines := w.recordedDeadlines(); len(deadlines) != 0 {
		t.Fatalf("legacy stream write deadlines = %v, want none", deadlines)
	}
}
