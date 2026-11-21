package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

// gatingTestLLM errors until released, then blocks each Do on a gate channel so
// the test can hold the retried turn open (no new bottom message appended)
// while it issues a second retry.
type gatingTestLLM struct {
	inner llm.Service
	mu    sync.Mutex
	err   error
	gate  chan struct{} // when non-nil, Do blocks on it after err clears
}

func (g *gatingTestLLM) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	g.mu.Lock()
	err := g.err
	gate := g.gate
	g.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.inner.Do(ctx, req)
}
func (g *gatingTestLLM) Provider() string       { return g.inner.Provider() }
func (g *gatingTestLLM) MaxImageDimension() int { return g.inner.MaxImageDimension() }
func (g *gatingTestLLM) MaxImageBytes() int     { return g.inner.MaxImageBytes() }
func (g *gatingTestLLM) SupportsImages() bool   { return g.inner.SupportsImages() }

// TestRetryDoubleClickDeduped verifies that a second retry POST for the SAME
// bottom error message is rejected (MAJOR 6) — without mutating the error row.
func TestRetryDoubleClickDeduped(t *testing.T) {
	t.Parallel()
	synctest.Test(t, testRetryDoubleClickDeduped)
}

func testRetryDoubleClickDeduped(t *testing.T) {
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := predictable.NewService()
	gate := make(chan struct{})
	gllm := &gatingTestLLM{inner: ps, err: fmt.Errorf("connection error: EOF"), gate: gate}

	svr := NewServer(database, &testLLMManager{service: gllm},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		true, "predictable", "")
	if svr.terminals != nil {
		svr.terminals.SetSpawner(InProcessSpawner)
	}
	defer stopActiveConversationLoops(svr)

	ctx := t.Context()
	conversation, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	convID := conversation.ConversationID

	sendChat(t, svr, convID, "hello", false)

	// Wait for the retryable error message + idle.
	waitFor(t, 10*time.Second, func() bool {
		var msgs []generated.Message
		database.Queries(ctx, func(q *generated.Queries) error {
			var e error
			msgs, e = q.ListMessages(ctx, convID)
			return e
		})
		for _, m := range msgs {
			if m.Type == string(db.MessageTypeError) {
				return true
			}
		}
		return false
	})
	waitFor(t, 5*time.Second, func() bool { return !svr.IsAgentWorking(convID) })

	mgr, err := svr.getOrCreateConversationManager(ctx, convID, "")
	if err != nil {
		t.Fatalf("getOrCreateConversationManager: %v", err)
	}

	// Recover upstream but keep the gate closed so the retried turn blocks in
	// Do without appending a new bottom message.
	gllm.mu.Lock()
	gllm.err = nil
	gllm.mu.Unlock()

	// First retry: kicks the loop.
	if err := mgr.RetryLastLLMRequest(ctx); err != nil {
		t.Fatalf("first retry: unexpected error %v", err)
	}

	// Second retry (double-click) for the SAME bottom error must be rejected.
	err = mgr.RetryLastLLMRequest(ctx)
	if !errors.Is(err, errRetryNotApplicable) {
		t.Fatalf("second retry: want errRetryNotApplicable, got %v", err)
	}

	// Release the gate and let the retried turn run to completion before
	// the deferred loop teardown.
	close(gate)
	synctest.Wait()
}

// An error recorded mid-turn, as a subagent's is before its report to the
// parent, is neither retried nor continued past until the turn ends.
func TestRetryWaitsForTurnEnd(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure llm.Message
		handle  func(*Server, http.ResponseWriter, *http.Request, string)
		body    string
	}{
		{
			name: "retry",
			failure: llm.Message{
				Role:           llm.MessageRoleAssistant,
				Content:        []llm.Content{{Type: llm.ContentTypeText, Text: "LLM request failed: boom"}},
				ErrorType:      llm.ErrorTypeLLMRequest,
				ErrorRetryable: true,
			},
			handle: (*Server).handleRetryConversation,
		},
		{
			name: "continue after refusal",
			failure: llm.Message{
				Role:      llm.MessageRoleAssistant,
				Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "[declined]"}},
				ErrorType: llm.ErrorTypeRefusal,
			},
			handle: (*Server).handleContinueConversation,
			body:   `{"model":"predictable"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _, held, conv := newBtwTest(t)
			requireBtwStatus(t, postBtwChat(t, server, conv.ConversationID, ChatRequest{Message: "report", Model: "predictable"}), http.StatusAccepted)
			call := held.waitCall(t, "report")
			if err := server.recordMessage(t.Context(), conv.ConversationID, tc.failure, "", llm.Usage{}, nil); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			tc.handle(server, w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body)), conv.ConversationID)
			if !strings.Contains(w.Body.String(), "not_applicable") {
				t.Fatalf("%s mid-turn: %d %s, want not_applicable", tc.name, w.Code, strings.TrimSpace(w.Body.String()))
			}
			releaseAndWaitIdle(t, server, conv.ConversationID, call)
		})
	}
}

// A turn that starts while Continue waits for the loop lifecycle lock, past
// its first check that the agent is idle, isn't joined by a second request.
func TestContinueAfterRefusalRacingDrain(t *testing.T) {
	server, database, held, conv := newBtwTest(t)
	ctx := t.Context()
	manager, err := server.getOrCreateConversationManager(ctx, conv.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	// Hold the drain of a queued message where it starts its turn: holding
	// the lifecycle lock, with the agent not yet working.
	draining, release := make(chan struct{}), make(chan struct{})
	releaseDrain := sync.OnceFunc(func() { close(release) })
	defer releaseDrain()
	record := manager.recordDrainedQueued
	manager.recordDrainedQueued = func(ctx context.Context, qm db.QueuedMessage, messages []llm.Message) (int64, error) {
		close(draining)
		<-release
		return record(ctx, qm, messages)
	}
	requireBtwStatus(t, postBtwChat(t, server, conv.ConversationID, ChatRequest{Message: "refusal", Model: "predictable"}), http.StatusAccepted)
	releaseAndWaitIdle(t, server, conv.ConversationID, held.waitCall(t, "refusal"))
	requireBtwStatus(t, postBtwChat(t, server, conv.ConversationID, ChatRequest{Message: "drained", Model: "predictable", Queue: true}), http.StatusAccepted)
	<-draining

	w := httptest.NewRecorder()
	continued := make(chan struct{})
	go func() {
		defer close(continued)
		server.handleContinueConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"model":"predictable"}`)), conv.ConversationID)
	}()
	waitLockingIn(t, "(*ConversationManager).ContinueAfterRefusal")
	releaseDrain()
	<-continued
	if !strings.Contains(w.Body.String(), "not_applicable") {
		t.Errorf("continue while the drained turn starts: %d %s, want not_applicable", w.Code, strings.TrimSpace(w.Body.String()))
	}
	releaseAndWaitIdle(t, server, conv.ConversationID, held.waitCall(t, "drained"))
	held.mu.Lock()
	again := len(held.calls["drained"])
	held.mu.Unlock()
	if again != 0 {
		t.Errorf("%d more requests for the drained turn", again)
	}
	requireTurnsEndOnce(t, database, conv.ConversationID, 1)
}
