package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/subpub"
)

func alwaysPublishStream() bool { return true }

// TestStreamFlusherAssignsMonotonicSeq verifies that each partial update the
// streamFlusher broadcasts carries a monotonically increasing per-conversation
// sequence number, regardless of whether the delta is text (batched) or a
// non-text delta (broadcast immediately). Clients use this to detect dropped
// or out-of-order partial updates.
func TestStreamFlusherAssignsMonotonicSeq(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	manager, err := server.getOrCreateConversationManager(context.Background(), conversation.ConversationID, "")
	if err != nil {
		t.Fatalf("failed to get conversation manager: %v", err)
	}

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	next := manager.subpub.Subscribe(subCtx, -1)

	seqs := make(chan int64, 16)
	go func() {
		for {
			data, ok := next()
			if !ok {
				return
			}
			if data.StreamDelta != nil {
				seqs <- data.StreamDelta.Seq
			}
		}
	}()

	// Use a long interval so the periodic timer never fires on its own; only
	// explicit Flushes and kind-change boundaries emit deltas. This keeps the
	// expected sequence deterministic.
	sf := newStreamFlusher(manager, time.Hour, alwaysPublishStream)

	// Thinking deltas are batched like text; pushing a delta of a different
	// kind flushes the previous buffer first.
	sf.Push(llm.StreamDelta{Type: "thinking", Text: "hmm", Index: 0})
	// This text delta forces the buffered thinking delta out (seq 1).
	sf.Push(llm.StreamDelta{Type: "text", Text: "hello ", Index: 1})
	sf.Push(llm.StreamDelta{Type: "text", Text: "world", Index: 1})
	// Explicit flush emits the combined text delta (seq 2).
	sf.Flush()
	// Another thinking delta, emitted by the final flush (seq 3).
	sf.Push(llm.StreamDelta{Type: "thinking", Text: "done", Index: 0})
	sf.Flush()

	var got []int64
	for len(got) < 3 {
		select {
		case s := <-seqs:
			got = append(got, s)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for stream deltas; got %v", got)
		}
	}

	want := []int64{1, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("seq[%d] = %d, want %d (all: %v)", i, got[i], want[i], got)
		}
	}
}

// TestStreamFlusherBatchesThinkingDeltas verifies that thinking deltas are
// coalesced like text deltas rather than broadcast one-per-token. Reasoning
// models emit thinking deltas at the same rate as text; broadcasting each
// individually flooded the bounded subpub queues and force-disconnected SSE
// subscribers (the "frozen UI while the agent keeps working" bug).
func TestStreamFlusherBatchesThinkingDeltas(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	manager, err := server.getOrCreateConversationManager(context.Background(), conversation.ConversationID, "")
	if err != nil {
		t.Fatalf("failed to get conversation manager: %v", err)
	}

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	next := manager.subpub.Subscribe(subCtx, -1)

	deltas := make(chan llm.StreamDelta, 16)
	go func() {
		for {
			data, ok := next()
			if !ok {
				return
			}
			if data.StreamDelta != nil {
				deltas <- *data.StreamDelta
			}
		}
	}()

	// Long interval: only explicit Flushes and kind-change boundaries emit.
	sf := newStreamFlusher(manager, time.Hour, alwaysPublishStream)

	// A burst of thinking deltas must coalesce into ONE broadcast.
	sf.Push(llm.StreamDelta{Type: "thinking", Text: "a", Index: 0})
	sf.Push(llm.StreamDelta{Type: "thinking", Text: "b", Index: 0})
	sf.Push(llm.StreamDelta{Type: "thinking", Text: "c", Index: 0})
	// A text delta forces the thinking buffer out first (boundary flush),
	// then accumulates itself.
	sf.Push(llm.StreamDelta{Type: "text", Text: "hi", Index: 1})
	sf.Flush()

	var got []llm.StreamDelta
	for len(got) < 2 {
		select {
		case d := <-deltas:
			got = append(got, d)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for stream deltas; got %+v", got)
		}
	}

	if got[0].Type != "thinking" || got[0].Text != "abc" || got[0].Index != 0 {
		t.Fatalf("first delta = %+v, want coalesced thinking delta {Type: thinking, Text: abc, Index: 0}", got[0])
	}
	if got[1].Type != "text" || got[1].Text != "hi" || got[1].Index != 1 {
		t.Fatalf("second delta = %+v, want {Type: text, Text: hi, Index: 1}", got[1])
	}
	// Nothing further should arrive: three thinking pushes must NOT have
	// produced three broadcasts.
	select {
	case d := <-deltas:
		t.Fatalf("unexpected extra delta: %+v", d)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestThinkingDeltaFloodDoesNotDisconnectSubscriber reproduces the
// production freeze: a reasoning model emits thinking deltas one per token
// (hundreds/second); un-batched, each becomes its own broadcast, and a
// subscriber whose client has stalled (TCP backpressure, background tab,
// slow proxy hop) exhausts its bounded queue almost immediately. SubPub then
// force-disconnects it, which on /api/stream2 ends the SSE response — the
// browser drops into a reconnect/backfill loop and the UI looks frozen while
// the agent keeps working ("SSE subscriber queue full; reconnecting" in the
// server logs).
//
// The un-drained subscription below is exactly the server-side view of a
// stalled client. Pre-fix, pushing SubscriberQueueCapacity+ thinking deltas
// disconnected it deterministically (one broadcast per delta); with batching
// the flood coalesces into a handful of broadcasts and the subscriber
// survives.
func TestThinkingDeltaFloodDoesNotDisconnectSubscriber(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	manager, err := server.getOrCreateConversationManager(context.Background(), conversation.ConversationID, "")
	if err != nil {
		t.Fatalf("failed to get conversation manager: %v", err)
	}

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	// Subscribe and never drain: a stalled client.
	_, status := manager.subpub.SubscribeWithStatus(subCtx, -1)

	sf := newStreamFlusher(manager, 50*time.Millisecond, alwaysPublishStream)
	for range subpub.SubscriberQueueCapacity + 50 {
		sf.Push(llm.StreamDelta{Type: "thinking", Text: "t", Index: 0})
	}
	sf.Flush()

	if status.FellBehind() {
		t.Fatal("thinking-delta flood disconnected an un-drained subscriber; deltas are being broadcast per-token instead of batched")
	}
}

// TestUnifiedStreamSurvivesThinkingFlood is the end-to-end reproduction of
// the production freeze report (support ticket: "Shelley randomly freezes
// mid-task, tokens keep accumulating, no response" on a reasoning model).
//
// Setup mirrors production: a real /api/stream2 handler whose client has
// stalled (blockingStreamWriter never returns from Write — a background tab,
// a slow proxy hop, TCP backpressure), and a reasoning-model turn delivering
// a burst of per-token thinking deltas through the conversation's
// streamFlusher.
//
// Pre-fix, each thinking delta was broadcast individually: a ~2000-token
// thinking burst put ~2000 events into the bounded queues (200 + 200),
// overflowed them, and SubPub force-disconnected the subscriber, which tears
// down the SSE response ("SSE subscriber queue full; reconnecting"). The
// browser then reconnects, backfills, stalls again on the next thinking
// burst — the visible "frozen while working" loop. With batching, the same
// burst coalesces into a handful of flushes and the stream survives.
func TestUnifiedStreamSurvivesThinkingFlood(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	logs := newQueueLogWriter()
	server.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Create the conversation and manager BEFORE capturing the list hash:
	// their commits change the conversation list, and a stale hash would make
	// the handler block writing the initial list replay before it ever
	// reaches its streamPub subscription — wedging the test upstream of the
	// code under test.
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	manager, err := server.getOrCreateConversationManager(context.Background(), conversation.ConversationID, "")
	if err != nil {
		t.Fatalf("failed to get conversation manager: %v", err)
	}
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

	// Broadcast slowly until the handler has subscribed and blocks writing
	// its first event (same pacing rationale as
	// TestUnifiedStreamClosesWhenItsSubscriptionFallsBehind).
	setupTicker := time.NewTicker(10 * time.Millisecond)
	defer setupTicker.Stop()
	setupTimeout := time.NewTimer(2 * time.Second)
	defer setupTimeout.Stop()
setup:
	for {
		select {
		case <-w.entered:
			break setup
		case <-setupTicker.C:
			server.streamPub.Broadcast(StreamResponse{Heartbeat: true})
		case <-setupTimeout.C:
			w.releaseWrite()
			cancel()
			<-done
			t.Fatal("stream never started writing")
		}
	}

	// A reasoning-model thinking burst: ~2000 per-token deltas delivered as
	// fast as a provider SSE stream hands them to OnStream. Well beyond the
	// combined bounded-queue capacity (~400) if broadcast per-token; a
	// handful of flushes if batched.
	sf := newStreamFlusher(manager, 50*time.Millisecond, alwaysPublishStream)
	for range 2000 {
		sf.Push(llm.StreamDelta{Type: "thinking", Text: "tok ", Index: 0})
	}
	sf.Flush()

	// The stream must still be open: no forced teardown, no queue-full log.
	select {
	case <-done:
		t.Fatalf("unified stream was torn down by a thinking-delta flood; logs:\n%s", logs.String())
	case <-time.After(300 * time.Millisecond):
	}
	select {
	case <-logs.subscriberQueueFull:
		t.Fatalf("subscriber queue overflowed during thinking flood:\n%s", logs.String())
	default:
	}

	w.releaseWrite()
	cancel()
	<-done
}
