package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/models"
)

type slugDeadlineService struct {
	llm.Service
	seen chan context.Context
}

func (s *slugDeadlineService) Do(ctx context.Context, _ *llm.Request) (*llm.Response, error) {
	s.seen <- ctx
	return nil, errors.New("deadline recorded")
}

func TestGenerateSlugAsyncDeadline(t *testing.T) {
	svr, database, _ := newTestServer(t)
	id := seedConversation(t, database, 0)
	service := &slugDeadlineService{Service: predictable.NewService(), seen: make(chan context.Context, 1)}
	svr.llmManager = &testLLMManager{service: service}
	before := time.Now()
	svr.generateSlugAsync(id, "hello", "predictable")
	select {
	case ctx := <-service.seen:
		after := time.Now()
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("slug request has no deadline")
		}
		if deadline.Before(before.Add(30*time.Second)) || deadline.After(after.Add(30*time.Second)) {
			t.Fatalf("deadline = %v, want 30 seconds from request start", deadline)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("slug request never started")
	}
}

// newUsageCollectingServer builds a Server whose LLM goes through a real
// models.Manager. The shared newTestServer hands out the predictable service
// unwrapped, so the production wrapper that feeds llmhttp usage collectors is
// missing and no indirect usage is ever recorded — which would make the
// assertions below vacuous. Only this test needs the real path, and the shared
// stub is deliberately permissive about model names (many tests depend on
// that), so this stays local rather than changing the default.
func newUsageCollectingServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	mgr, err := models.NewManager(&models.Config{
		Models: []models.Built{{
			ID:       "predictable",
			Provider: models.ProviderBuiltIn,
			Source:   "test",
			Service:  predictable.NewService(),
		}},
		Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	svr := NewServer(database, mgr, claudetool.ToolSetConfig{EnableBrowser: false},
		logger, true, "predictable", "")
	svr.hooksDir = t.TempDir()
	if svr.terminals != nil {
		svr.terminals.SetSpawner(InProcessSpawner)
	}
	return svr, database
}

// TestSlugUsageAppendsRatherThanMutating pins the append-only contract for
// message rows, end-to-end through the real HTTP handler.
//
// Slug generation makes an LLM call whose cost has to be recorded somewhere. It
// used to be UPDATEd onto the conversation's first user message after that
// message had already been written and streamed, which broke the invariant the
// rest of the system relies on: a message row never changes once written.
// Downstream, the browser caches messages keyed by (conversation_id,
// sequence_id) and refreshes only by fetching the tail, forks copy message
// rows, and the stream contract is that a sequence_id is delivered exactly
// once. An after-the-fact mutation is invisible to all three.
//
// Now the cost rides on a freshly appended slug marker. So the assertion is not
// "no message carries slug usage" but the stronger, more direct one: no row that
// existed before slug generation finished may differ afterwards.
//
// That the usage actually lands on the marker is covered by
// slug.TestGenerateSlug_UsageOnAppendedMarker, which drives a real
// models.Manager (this harness hands out the predictable service unwrapped, so
// the loggingService that feeds the usage collector isn't in the path).
func TestSlugUsageAppendsRatherThanMutating(t *testing.T) {
	t.Parallel()
	svr, database := newUsageCollectingServer(t)
	ctx := t.Context()

	body, _ := json.Marshal(ChatRequest{Message: "hello", Model: "predictable"})
	req := httptest.NewRequest("POST", "/api/conversations/new", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svr.handleNewConversation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("new conversation: status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	convID := resp.ConversationID

	type snapshot struct {
		otherUsage string
		usage      string
		llmData    string
	}
	deref := func(s *string) string {
		if s == nil {
			return "<nil>"
		}
		return *s
	}

	// Snapshot every row that exists before slug generation completes.
	before := map[string]snapshot{}
	waitFor(t, 10*time.Second, func() bool {
		messages, err := database.ListMessages(ctx, convID)
		if err != nil {
			return false
		}
		for _, m := range messages {
			if m.Type == string(db.MessageTypeSlug) {
				continue
			}
			if _, seen := before[m.MessageID]; !seen {
				before[m.MessageID] = snapshot{deref(m.OtherUsageData), deref(m.UsageData), deref(m.LlmData)}
			}
		}
		conv, err := database.GetConversationByID(ctx, convID)
		return err == nil && conv != nil && conv.Slug != nil && *conv.Slug != ""
	})
	if len(before) == 0 {
		t.Fatal("no messages recorded before slug generation finished")
	}

	messages, err := database.ListMessages(ctx, convID)
	if err != nil {
		t.Fatal(err)
	}
	sawMarker := false
	for _, m := range messages {
		if m.Type == string(db.MessageTypeSlug) {
			sawMarker = true
			continue
		}
		prev, existed := before[m.MessageID]
		if !existed {
			continue // appended while we were waiting; fine, appends are legal
		}
		now := snapshot{deref(m.OtherUsageData), deref(m.UsageData), deref(m.LlmData)}
		if now != prev {
			t.Errorf("message %s (seq %d) changed after being published:\n before: %+v\n after:  %+v\n"+
				"message rows are append-only; record new data by appending a row",
				m.MessageID, m.SequenceID, prev, now)
		}
	}
	if !sawMarker {
		t.Error("no slug marker was appended, so this test proved nothing; did slug usage stop being recorded?")
	}
}

// TestRetryWorksWithTrailingSlugMarker covers the ordering hazard the slug
// marker introduces. Slug generation races the first turn, so its marker can be
// appended AFTER an error message (observed roughly one run in three against a
// real server). handleRetry, handleContinue, RetryLastError and ContinueOnModel
// all gate on the bottom message being an error, so if a trailing marker counted
// as the bottom message, the Retry button would silently stop working.
func TestRetryWorksWithTrailingSlugMarker(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := predictable.NewService()
	switchable := &switchableTestLLM{inner: ps, err: fmt.Errorf("connection error: EOF")}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	svr := NewServer(database, &testLLMManager{service: switchable},
		claudetool.ToolSetConfig{EnableBrowser: false}, logger, true, "predictable", "")
	if svr.terminals != nil {
		svr.terminals.SetSpawner(InProcessSpawner)
	}
	ctx := t.Context()

	conversation, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	convID := conversation.ConversationID

	body, _ := json.Marshal(ChatRequest{Message: "hello", Model: "predictable"})
	req := httptest.NewRequest("POST", "/api/conversation/"+convID+"/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	svr.handleChatConversation(httptest.NewRecorder(), req, convID)

	waitFor(t, 10*time.Second, func() bool {
		messages, err := database.ListMessages(ctx, convID)
		if err != nil {
			return false
		}
		for _, m := range messages {
			if m.Type == string(db.MessageTypeError) {
				return true
			}
		}
		return false
	})
	waitFor(t, 5*time.Second, func() bool { return !svr.IsAgentWorking(convID) })

	// Force the hazardous ordering deterministically: marker last.
	if _, err := database.CreateUsageMessage(ctx, convID, []llm.PurposedUsage{
		{Purpose: "slug", Usage: llm.Usage{InputTokens: 20, Model: "predictable-v1"}},
	}); err != nil {
		t.Fatal(err)
	}
	messages, err := database.ListMessages(ctx, convID)
	if err != nil {
		t.Fatal(err)
	}
	if last := messages[len(messages)-1]; last.Type != string(db.MessageTypeSlug) {
		t.Fatalf("setup: last message is %q, want the slug marker", last.Type)
	}

	switchable.setErr(nil)
	retryW := httptest.NewRecorder()
	svr.handleRetryConversation(retryW, httptest.NewRequest("POST", "/api/conversation/"+convID+"/retry", nil), convID)
	if retryW.Code != http.StatusAccepted {
		t.Fatalf("retry: status %d: %s", retryW.Code, retryW.Body.String())
	}
	// "accepted" alone isn't enough: the handler answers 202 with
	// {"status":"not_applicable"} when it thinks there's no error to retry.
	if strings.Contains(retryW.Body.String(), "not_applicable") {
		t.Fatalf("retry reported not_applicable: a trailing slug marker hid the retryable error (%s)", retryW.Body.String())
	}

	waitFor(t, 10*time.Second, func() bool {
		messages, err := database.ListMessages(ctx, convID)
		if err != nil {
			return false
		}
		for _, m := range messages {
			if m.Type == string(db.MessageTypeAgent) {
				return true
			}
		}
		return false
	})
}

// TestSlugMarkerReachesUnifiedStream asserts the slug marker is delivered on
// /api/stream2, the stream the web UI actually listens to.
//
// The marker consumes a real sequence_id. If it is written but never published,
// the client's view of the sequence space acquires a hole — seq N (the marker)
// missing while N+1 arrives — and the message cache correctly treats that skip
// as a lost middle and forfeits its cached history, forcing a full re-download.
// That is the same bug that TestWarningMessageReachesUnifiedStream guards for
// warnings, and it is easy to reintroduce here because slug generation runs in a
// detached goroutine whose only notification used to be the metadata-only
// notifySubscribers (which sends Messages: nil).
func TestSlugMarkerReachesUnifiedStream(t *testing.T) {
	t.Parallel()
	svr, _ := newUsageCollectingServer(t)

	// Subscribe the way handleStream does, before the conversation exists.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	next := svr.streamPub.Subscribe(ctx, -1)

	body, _ := json.Marshal(ChatRequest{Message: "hello", Model: "predictable"})
	req := httptest.NewRequest("POST", "/api/conversations/new", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svr.handleNewConversation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("new conversation: status %d: %s", w.Code, w.Body.String())
	}

	type frame struct {
		data StreamResponse
		ok   bool
	}
	frames := make(chan frame)
	go func() {
		for {
			d, ok := next()
			frames <- frame{d, ok}
			if !ok {
				return
			}
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case f := <-frames:
			if !f.ok {
				t.Fatal("stream closed before the slug marker arrived")
			}
			for _, m := range f.data.Messages {
				if m.Type != string(db.MessageTypeSlug) {
					continue
				}
				if m.SequenceID <= 0 {
					t.Errorf("slug marker has sequence_id %d", m.SequenceID)
				}
				// It must carry the usage; an empty marker would be pointless.
				if m.OtherUsageData == nil || !strings.Contains(*m.OtherUsageData, "\"slug\"") {
					t.Errorf("slug marker other_usage_data = %v, want the slug usage entry", m.OtherUsageData)
				}
				return // delivered
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("slug marker never arrived on the unified stream; GenerateSlug's caller must " +
				"publish it with notifySubscribers and the marker message")
		}
	}
}
