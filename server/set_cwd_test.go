package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
)

// postSetCwd asks the server to move a conversation to dir.
func postSetCwd(t *testing.T, s *Server, conversationID, dir string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"cwd": dir})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversationID+"/cwd", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleSetConversationCwd(w, req, conversationID)
	return w
}

// The directory shown in the status bar is a control: picking a new one moves
// the conversation there. It has to land in three places at once, or the agent
// and the user disagree about where they are — the conversation row (what the
// UI reads and what a future loop is built from), the live toolset (what the
// next bash command actually uses), and the message log (what the agent reads).
func TestSetConversationCwd(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	start := t.TempDir()
	dest := t.TempDir()
	conversation, err := database.CreateConversation(context.Background(), nil, true, &start, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conversation.ConversationID

	// A live manager, as if the user had already sent a turn: this is the case
	// where a stale in-memory cwd would outlive the database write.
	manager, err := server.getOrCreateConversationManager(context.Background(), id, "")
	if err != nil {
		t.Fatal(err)
	}

	if w := postSetCwd(t, server, id, dest); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// 1. Persisted, so a reload and the conversation list agree.
	got, err := database.GetConversationByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cwd == nil || *got.Cwd != dest {
		t.Errorf("conversation cwd = %v, want %q", got.Cwd, dest)
	}

	// 2. The live manager moved too, so the next turn's loop is built there.
	if cwd := manager.Cwd(); cwd != dest {
		t.Errorf("manager cwd = %q, want %q", cwd, dest)
	}

	// 3. The agent is told, in a message it will actually read. A marker
	// excluded from context would leave it running commands against a
	// directory it believes is still the old one.
	messages, err := database.ListMessages(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var found *string
	for i := range messages {
		m := messages[i]
		if m.Type == string(db.MessageTypeUser) && m.LlmData != nil && strings.Contains(*m.LlmData, dest) {
			if m.ExcludedFromContext {
				t.Errorf("cwd notice is excluded from context; the agent would never see it")
			}
			found = m.LlmData
			break
		}
	}
	if found == nil {
		t.Fatalf("no in-context message mentioning %q; messages=%d", dest, len(messages))
	}
}

// The notice has to reach the LIVE loop, not just the database.
//
// A loop keeps its own in-memory history and only rebuilds it from the DB when
// it is created; between turns it survives, so a row written straight to the DB
// is invisible to the model until something drops the loop (a model switch, a
// compaction, a 30-minute eviction). Persisting alone would mean the user sees
// the notice in the transcript, the tools really have moved, and the agent is
// told nothing — the exact disagreement this endpoint exists to prevent, made
// harder to spot because the transcript looks right.
func TestSetConversationCwdNoticeReachesLiveLoop(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	start := t.TempDir()
	dest := t.TempDir()
	conversation, err := database.CreateConversation(context.Background(), nil, true, &start, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conversation.ConversationID

	// A real turn, so there is a live loop with history to go stale.
	postChatMessage(t, server, id, "bash: pwd")
	waitForBashResult(t, database, id, start, 10*time.Second)
	waitForIdle(t, server, id)

	if w := postSetCwd(t, server, id, dest); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	manager, err := server.getOrCreateConversationManager(context.Background(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	liveLoop := manager.loop
	manager.mu.Unlock()
	if liveLoop == nil {
		t.Fatal("no live loop after a completed turn; this test would prove nothing")
	}

	for _, msg := range liveLoop.GetHistory() {
		for _, c := range msg.Content {
			if strings.Contains(c.Text, dest) {
				return
			}
		}
	}
	t.Fatalf("cwd notice is absent from the live loop's history: the next turn's"+
		" LLM request would still describe %q as the working directory", start)
}

// newRecordingTestServer builds a test server whose LLM calls are recorded, so
// a test can assert on the requests the agent actually sent.
func newRecordingTestServer(t *testing.T) (*Server, *db.DB, *recordingService) {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	rec := &recordingService{Service: loop.NewPredictableService()}
	server := NewServer(database, &testLLMManager{service: rec},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		true, "predictable", "")
	server.hooksDir = t.TempDir()
	return server, database, rec
}

// recordingService wraps an llm.Service and remembers the messages and system
// prompt of every request that passes through, so a test can assert on what the
// model was actually sent rather than on internal state that merely implies it.
type recordingService struct {
	llm.Service
	mu       sync.Mutex
	requests [][]llm.Message
	systems  []string
}

func (r *recordingService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	var system strings.Builder
	for _, sc := range req.System {
		system.WriteString(sc.Text)
	}
	r.mu.Lock()
	r.requests = append(r.requests, append([]llm.Message(nil), req.Messages...))
	r.systems = append(r.systems, system.String())
	r.mu.Unlock()
	return r.Service.Do(ctx, req)
}

// messagesMention reports whether any of the messages contains needle, in text
// or in tool-result content.
func messagesMention(messages []llm.Message, needle string) bool {
	for _, m := range messages {
		for _, c := range m.Content {
			if strings.Contains(c.Text, needle) {
				return true
			}
			for _, tr := range c.ToolResult {
				if strings.Contains(tr.Text, needle) {
					return true
				}
			}
		}
	}
	return false
}

// turnIndex finds the recorded request for the turn the given user message
// drove. Callers must hold r.mu.
//
// Two things make the obvious implementations wrong. "The last request" picks up
// the conversation-naming call, which fires separately and lands last on a first
// send. And a substring search for the user's text matches that same slug call,
// because its prompt quotes the conversation. So the anchor is a user-role
// message whose entire text IS the message sent — which only the real turn has.
func (r *recordingService) turnIndex(t *testing.T, userText string) int {
	t.Helper()
	for i := len(r.requests) - 1; i >= 0; i-- {
		for j := range r.requests[i] {
			m := &r.requests[i][j]
			if m.Role != llm.MessageRoleUser {
				continue
			}
			if strings.TrimSpace(messageTextContent(m)) == strings.TrimSpace(userText) {
				return i
			}
		}
	}
	t.Fatalf("no recorded LLM request was the turn for user message %q (recorded %d requests)", userText, len(r.requests))
	return -1
}

// turnSystemPrompt returns the system prompt sent with the turn the given user
// message drove.
func (r *recordingService) turnSystemPrompt(t *testing.T, userText string) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.systems[r.turnIndex(t, userText)]
}

// turnMentioning reports whether the turn the given user message drove also
// mentioned needle.
func (r *recordingService) turnMentioning(t *testing.T, userText, needle string) bool {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	return messagesMention(r.requests[r.turnIndex(t, userText)], needle)
}

// The end of the chain the test above starts: the notice must be in the LLM
// request the next turn actually sends. Everything else — the DB row, the loop's
// history — is only a means to this.
func TestSetConversationCwdNoticeReachesNextLLMRequest(t *testing.T) {
	t.Parallel()
	server, database, rec := newRecordingTestServer(t)

	start := t.TempDir()
	dest := t.TempDir()
	conversation, err := database.CreateConversation(context.Background(), nil, true, &start, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conversation.ConversationID

	postChatMessage(t, server, id, "bash: pwd")
	waitForBashResult(t, database, id, start, 10*time.Second)
	waitForIdle(t, server, id)

	if w := postSetCwd(t, server, id, dest); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The move itself must not have provoked a turn: the notice informs the
	// next request, it does not ask the agent for anything.
	manager, err := server.getOrCreateConversationManager(context.Background(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	if manager.IsAgentWorking() {
		t.Error("changing the directory started a turn on its own")
	}

	postChatMessage(t, server, id, "hello")
	waitForIdle(t, server, id)

	if !rec.turnMentioning(t, "hello", dest) {
		t.Errorf("the next LLM request never mentioned the new directory %q", dest)
	}
}

// The toolset check above is about cm.cwd; this is the behavior that actually
// matters. A conversation that has already run a turn has a live toolset whose
// working directory was baked in at loop build time, so a cwd change that only
// writes the database would leave the next bash command running in the old
// directory — while the UI, and the agent's own notice, both claim otherwise.
func TestSetConversationCwdRedirectsBash(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	start := t.TempDir()
	dest := t.TempDir()
	conversation, err := database.CreateConversation(context.Background(), nil, true, &start, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conversation.ConversationID

	// First turn: builds the loop and the toolset, pinned to `start`.
	postChatMessage(t, server, id, "bash: pwd")
	waitForBashResult(t, database, id, start, 10*time.Second)
	waitForIdle(t, server, id)

	if w := postSetCwd(t, server, id, dest); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Second turn, same live toolset: it must land in the new directory.
	postChatMessage(t, server, id, "bash: pwd")
	waitForBashResult(t, database, id, dest, 10*time.Second)
}

// waitForIdle waits for the conversation's turn to finish.
//
// A bash tool result landing does NOT mean the turn is over: the agent still has
// to send its closing message, and agent_working stays true until it does. Tests
// that act on the conversation after a turn (as a user would) have to wait for
// this, or they race the very mid-turn guard they aren't trying to test.
func waitForIdle(t *testing.T, s *Server, conversationID string) {
	t.Helper()
	manager, err := s.getOrCreateConversationManager(context.Background(), conversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { return !manager.IsAgentWorking() })
}

// postChatMessage sends a chat turn and asserts it was accepted.
func postChatMessage(t *testing.T, s *Server, conversationID, message string) {
	t.Helper()
	body, err := json.Marshal(ChatRequest{Message: message, Model: "predictable"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleChatConversation(w, req, conversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("chat %q: expected 202, got %d: %s", message, w.Code, w.Body.String())
	}
}

// A cwd that isn't a usable directory must be refused before anything moves:
// a half-applied change (row updated, tools not) is worse than none.
func TestSetConversationCwdRejectsBadPaths(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	start := t.TempDir()
	file := filepath.Join(start, "regular-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		cwd  string
	}{
		{"missing", filepath.Join(start, "no-such-dir")},
		{"not a directory", file},
		{"empty", ""},
		{"relative", "./somewhere"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conversation, err := database.CreateConversation(context.Background(), nil, true, &start, nil, db.ConversationOptions{})
			if err != nil {
				t.Fatal(err)
			}
			id := conversation.ConversationID

			w := postSetCwd(t, server, id, tc.cwd)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
			got, err := database.GetConversationByID(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Cwd == nil || *got.Cwd != start {
				t.Errorf("cwd moved on a rejected request: %v", got.Cwd)
			}
		})
	}
}

// The three writes SetCwd makes are not atomic together, so concurrent moves
// must not interleave into a state where the conversation row says one
// directory and the live toolset is in another. Worth pinning under -race:
// the readout and the agent disagreeing is exactly the bug this whole endpoint
// exists to avoid.
func TestSetConversationCwdConcurrent(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	start := t.TempDir()
	conversation, err := database.CreateConversation(context.Background(), nil, true, &start, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conversation.ConversationID

	// A live toolset, so the racing writes have all three destinations in play.
	postChatMessage(t, server, id, "bash: pwd")
	waitForBashResult(t, database, id, start, 10*time.Second)
	waitForIdle(t, server, id)

	// All four must be accepted — mid-turn refusals would make "whichever won"
	// vacuous, so waitForIdle above is load-bearing, not hygiene.
	dirs := []string{t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()}
	var wg sync.WaitGroup
	for _, dir := range dirs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := postSetCwd(t, server, id, dir); w.Code != http.StatusOK {
				t.Errorf("move to %s: expected 200, got %d: %s", dir, w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()

	manager, err := server.getOrCreateConversationManager(context.Background(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := database.GetConversationByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cwd == nil {
		t.Fatal("cwd is unset after concurrent moves")
	}
	// Whichever won, everything must agree on it.
	if *got.Cwd != manager.Cwd() {
		t.Errorf("row cwd %q != manager cwd %q", *got.Cwd, manager.Cwd())
	}
	if !slices.Contains(dirs, *got.Cwd) {
		t.Errorf("cwd %q is none of the requested directories", *got.Cwd)
	}

	// And the tools are actually there, not in some earlier winner's directory.
	postChatMessage(t, server, id, "bash: pwd")
	waitForBashResult(t, database, id, *got.Cwd, 10*time.Second)
}

// A draft's cwd is client state until the first send, and moving it through
// here would hydrate the conversation early — writing a system prompt pinned to
// the directory the user is trying to leave.
func TestSetConversationCwdRefusesDrafts(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	start := t.TempDir()
	dest := t.TempDir()
	conversation, err := database.CreateDraftConversation(context.Background(), &start, nil, db.ConversationOptions{}, "")
	if err != nil {
		t.Fatal(err)
	}
	id := conversation.ConversationID

	if w := postSetCwd(t, server, id, dest); w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a draft, got %d: %s", w.Code, w.Body.String())
	}

	// Nothing was hydrated on its behalf: no system prompt, no messages at all.
	messages, err := database.ListMessages(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Errorf("draft gained %d message(s) from a refused cwd change", len(messages))
	}
}

// Moving while the agent is mid-turn would change the ground under a running
// bash command, so it is refused rather than silently racing.
func TestSetConversationCwdRefusesWhileWorking(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	start := t.TempDir()
	dest := t.TempDir()
	conversation, err := database.CreateConversation(context.Background(), nil, true, &start, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conversation.ConversationID

	chatBody, _ := json.Marshal(ChatRequest{Message: "bash: sleep 5", Model: "predictable"})
	req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+id+"/chat", strings.NewReader(string(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	server.handleChatConversation(httptest.NewRecorder(), req, id)

	manager, err := server.getOrCreateConversationManager(context.Background(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, manager.IsAgentWorking)

	if w := postSetCwd(t, server, id, dest); w.Code != http.StatusConflict {
		t.Fatalf("expected 409 while the agent works, got %d: %s", w.Code, w.Body.String())
	}
	got, err := database.GetConversationByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cwd == nil || *got.Cwd != start {
		t.Errorf("cwd moved while the agent was working: %v", got.Cwd)
	}
}

// A non-draft conversation that has never been sent to has no system prompt
// yet, so the SetCwd handler's getOrCreateConversationManager is what writes
// one — pinned to the directory being LEFT, since the row hasn't moved. This
// pins down what the agent ends up believing in that case.
//
// Note this path has no live loop yet, so the notice reaches the model by DB
// hydration rather than by AppendHistory: it covers the other half of the
// delivery story from TestSetConversationCwdNoticeReachesLiveLoop.
func TestSetConversationCwdBeforeFirstSend(t *testing.T) {
	t.Parallel()
	server, database, rec := newRecordingTestServer(t)

	start := t.TempDir()
	dest := t.TempDir()
	// userInitiated=true, isDraft=false: gets a full system prompt on hydrate.
	conversation, err := database.CreateConversation(context.Background(), nil, true, &start, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conversation.ConversationID

	if w := postSetCwd(t, server, id, dest); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Pin the premise that makes this test worth having, rather than trusting the
	// comment above: with no loop yet, the notice can only reach the model by DB
	// hydration. If something later made SetCwd build a loop eagerly, this would
	// quietly become a duplicate of TestSetConversationCwdNoticeReachesNextLLMRequest
	// while still claiming to cover the other delivery path.
	premise, err := server.getOrCreateConversationManager(context.Background(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	premise.mu.Lock()
	liveLoop := premise.loop
	premise.mu.Unlock()
	if liveLoop != nil {
		t.Fatal("a loop already exists before the first send; this test no longer covers the DB-hydration delivery path")
	}

	postChatMessage(t, server, id, "hello")
	waitForIdle(t, server, id)

	// Whatever the system prompt says, the request as a whole must leave the
	// agent in no doubt about where it now is.
	if !rec.turnMentioning(t, "hello", dest) {
		t.Errorf("request never mentioned the new directory %q", dest)
	}
	// Moving before the first send makes the move itself trigger the hydrate that
	// writes the system prompt, so that prompt names the directory being LEFT.
	// That sounds alarming and isn't: a system prompt is written once, at the
	// first send, and names whatever the cwd was then — so ANY later move leaves
	// it equally stale. The notice is what reconciles the two, in this path and
	// in the ordinary one alike. Asserting that keeps the two paths honest about
	// being the same shape, and would catch a future change that started relying
	// on the system prompt to carry the current directory.
	system := rec.turnSystemPrompt(t, "hello")
	if !strings.Contains(system, start) {
		t.Errorf("expected the system prompt to name the directory it was written in (%q)", start)
	}
	if strings.Contains(system, dest) {
		t.Errorf("system prompt unexpectedly names the new directory %q; if system prompts now track "+
			"the live cwd, the notice may be redundant here", dest)
	}
}
