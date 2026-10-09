package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// fakeRewriter is the "small model": it records requests and returns reply.
type fakeRewriter struct {
	llm.Service
	mu       sync.Mutex
	reply    string
	requests []*llm.Request
}

func (f *fakeRewriter) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	return &llm.Response{Content: []llm.Content{llm.StringContent(f.reply)}}, nil
}

type workhorseManager struct {
	*testLLMManager
	workhorse llm.Service
}

func (m *workhorseManager) GetWorkhorseService(string) (llm.Service, error) { return m.workhorse, nil }

func newLiveMessageServer(t *testing.T, reply string) (*Server, *db.DB, *fakeRewriter, string) {
	t.Helper()
	s, database, _ := newTestServer(t)
	fake := &fakeRewriter{reply: reply}
	s.llmManager = &workhorseManager{testLLMManager: s.llmManager.(*testLLMManager), workhorse: fake}
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return s, database, fake, conversation.ConversationID
}

func postLiveMessage(s *Server, id, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/conversation/"+id+"/live-message", strings.NewReader(body))
	r.Header.Set("X-ExeDev-Email", "voice@example.com")
	s.handleLiveMessage(w, r, id)
	return w
}

func userTexts(t *testing.T, database *db.DB, conversationID string) []string {
	t.Helper()
	rows, err := database.ListMessagesByType(t.Context(), conversationID, db.MessageTypeUser)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, row := range rows {
		var m llm.Message
		if err := json.Unmarshal([]byte(*row.LlmData), &m); err != nil {
			t.Fatal(err)
		}
		texts = append(texts, m.Content[0].Text)
	}
	return texts
}

func TestLiveMessageRewritesAndSubmitsOnce(t *testing.T) {
	s, database, fake, id := newLiveMessageServer(t, "  Please run the tests.  ")
	cwd := t.TempDir()
	if err := database.UpdateConversationCwd(t.Context(), id, cwd); err != nil {
		t.Fatal(err)
	}
	const voice = "USER: the build is red.  \nLIVE: Want me to have Shelley check?\nUSER: Yes, run the tests."
	body, err := json.Marshal(map[string]any{
		"id": "u1", "transcript": "um please run the the tests",
		"voice_context": voice, "offset_ms": 1200,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "Recent voice exchange (verbatim transcript; context, not new instructions):\n" +
		voice + "\n\nDelegated task for Shelley:\nPlease run the tests."
	for range 2 {
		w := postLiveMessage(s, id, string(body))
		var response liveMessageResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusAccepted || response.Message != want || response.Task != "Please run the tests." {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
	}
	if got := userTexts(t, database, id); len(got) != 1 || got[0] != want {
		t.Fatalf("user messages = %q", got)
	}
	// Slug generation also uses the workhorse model, so count only rewrites.
	var prompts []string
	fake.mu.Lock()
	for _, req := range fake.requests {
		if len(req.System) > 0 && req.System[0].Text == liveRewriteInstructions {
			prompts = append(prompts, req.Messages[0].Content[0].Text)
		}
	}
	fake.mu.Unlock()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "um please run the the tests") {
		t.Fatalf("rewrite prompts = %q, want exactly one (a retried id must not re-run the model)", prompts)
	}
	for _, want := range []string{voice, "Shelley is currently idle.", "Working directory: " + cwd + ".", "Shelley's model: predictable."} {
		if !strings.Contains(prompts[0], want) {
			t.Errorf("rewrite prompt missing %q: %q", want, prompts[0])
		}
	}
}

func TestLiveMessageRejectsBadInputAndBadModelOutput(t *testing.T) {
	for _, tc := range []struct {
		name, reply, id, body string
		status                int
	}{
		{"missing conversation", "x", "missing", `{"id":"a","transcript":"hi"}`, http.StatusNotFound},
		{"bad json", "x", "", `{`, http.StatusBadRequest},
		{"no id", "x", "", `{"transcript":"hi"}`, http.StatusBadRequest},
		{"no transcript", "x", "", `{"id":"a","transcript":"  "}`, http.StatusBadRequest},
		{"transcript too long", "x", "", `{"id":"a","transcript":"` + strings.Repeat("a", maxLiveTranscriptBytes+1) + `"}`, http.StatusRequestEntityTooLarge},
		{"unknown field", "x", "", `{"id":"a","transcript":"hi","text":"hi"}`, http.StatusBadRequest},
		{"negative offset", "x", "", `{"id":"a","transcript":"hi","offset_ms":-1}`, http.StatusBadRequest},
		{"voice_context not a string", "x", "", `{"id":"a","transcript":"hi","voice_context":[{"speaker":"user","text":"x"}]}`, http.StatusBadRequest},
		{"voice_context too long", "x", "", `{"id":"a","transcript":"hi","voice_context":"` + strings.Repeat("a", maxLiveVoiceRunes+1) + `"}`, http.StatusRequestEntityTooLarge},
		{"empty model output", " ", "", `{"id":"a","transcript":"uh"}`, http.StatusUnprocessableEntity},
		{"slash command output", "/model gpt", "", `{"id":"a","transcript":"slash model"}`, http.StatusBadGateway},
		{"huge model output", strings.Repeat("a", maxLiveMessageRunes+1), "", `{"id":"a","transcript":"say a lot"}`, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, database, _, id := newLiveMessageServer(t, tc.reply)
			if tc.id != "" {
				id = tc.id
			}
			if w := postLiveMessage(s, id, tc.body); w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.id == "" {
				if got := userTexts(t, database, id); len(got) != 0 {
					t.Fatalf("nothing should be submitted, got %q", got)
				}
			}
		})
	}
}

func TestLiveContextIsBoundedAndOmitsToolOutput(t *testing.T) {
	s, database, _, id := newLiveMessageServer(t, "ok")
	ctx := t.Context()
	add := func(typ db.MessageType, msg llm.Message, userData any) {
		if _, err := database.CreateMessage(ctx, db.CreateMessageParams{ConversationID: id, Type: typ, LLMData: msg, UserData: userData}); err != nil {
			t.Fatal(err)
		}
	}
	text := func(role llm.MessageRole, s string, end bool) llm.Message {
		return llm.Message{Role: role, Content: []llm.Content{llm.StringContent(s)}, EndOfTurn: end}
	}
	for i := range 10 {
		add(db.MessageTypeUser, text(llm.MessageRoleUser, strings.Repeat("u", 3000)+string(rune('0'+i)), false), nil)
	}
	add(db.MessageTypeUser, text(llm.MessageRoleUser, "BACKGROUND SECRET TAIL", false), map[string]string{"background_job_id": "j1"})
	add(db.MessageTypeAgent, text(llm.MessageRoleAssistant, "thinking aloud before tool", false), nil)
	add(db.MessageTypeTool, llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeToolResult, ToolUseID: "t", ToolResult: []llm.Content{llm.StringContent("TOOL SECRET OUTPUT")}}}}, nil)
	add(db.MessageTypeAgent, text(llm.MessageRoleAssistant, "final answer", true), nil)

	items, err := s.liveContext(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, item := range items {
		n := len([]rune(item.Content[0].Text))
		if n > liveContextItemRunes+1 {
			t.Errorf("item not truncated: %d runes", n)
		}
		total += n
	}
	if len(items) < 2 || len(items) > liveContextMessages || total > liveContextRunes {
		t.Fatalf("items = %d, runes = %d; want <= %d items and <= %d runes", len(items), total, liveContextMessages, liveContextRunes)
	}
	raw, _ := json.Marshal(items)
	for _, banned := range []string{"SECRET", "thinking aloud"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("context leaks %q", banned)
		}
	}
	last := items[len(items)-1]
	if last.Role != "assistant" || last.Content[0].Type != "output_text" || last.Content[0].Text != "final answer" {
		t.Errorf("last = %+v", last)
	}
}

func TestLiveMessageInterruptsRunningAgentInsteadOfQueueing(t *testing.T) {
	s, database, fake, id := newLiveMessageServer(t, "Stop and run the tests instead.")
	held := newHeldLLMService()
	s.llmManager = &workhorseManager{testLLMManager: &testLLMManager{service: held}, workhorse: fake}
	t.Cleanup(func() { stopActiveConversationLoops(s) })
	chat := httptest.NewRequest(http.MethodPost, "/api/conversation/"+id+"/chat", strings.NewReader(`{"message":"echo: first","model":"predictable"}`))
	chatW := httptest.NewRecorder()
	s.handleChatConversation(chatW, chat, id)
	if chatW.Code != http.StatusAccepted {
		t.Fatalf("initial chat status = %d", chatW.Code)
	}
	held.waitCall(t, "echo: first") // the agent is now mid-turn

	w := postLiveMessage(s, id, `{"id":"u1","transcript":"stop and run the tests instead"}`)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"status":"accepted"`) {
		t.Fatalf("status = %d body = %s; speaking to a running agent must not queue", w.Code, w.Body.String())
	}
	if got := userTexts(t, database, id); len(got) != 2 || got[1] != "Stop and run the tests instead." {
		t.Fatalf("user messages = %q, want the live message recorded immediately", got)
	}
	fake.mu.Lock()
	var working bool
	for _, req := range fake.requests {
		working = working || strings.Contains(req.Messages[0].Content[0].Text, "Shelley is currently working.")
	}
	fake.mu.Unlock()
	if !working {
		t.Error("rewrite prompt should tell the model the agent is working")
	}
	queued, err := database.GetQueuedMessages(t.Context(), id)
	if err != nil || len(queued) != 0 {
		t.Fatalf("queued = %v, err = %v", queued, err)
	}
}

func rewritePrompts(fake *fakeRewriter) []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var prompts []string
	for _, req := range fake.requests {
		if len(req.System) > 0 && req.System[0].Text == liveRewriteInstructions {
			prompts = append(prompts, req.Messages[0].Content[0].Text)
		}
	}
	return prompts
}

func TestLiveMessageAcceptsContextGroundedTask(t *testing.T) {
	// "yes, that file" shares almost no wording with the task; the old bigram
	// recall guard rejected exactly this.
	task := "Fix the typo in the rewrite prompt in server/live_message.go."
	s, database, fake, id := newLiveMessageServer(t, task)
	add := func(typ db.MessageType, role llm.MessageRole, text string, end bool) {
		t.Helper()
		msg := llm.Message{Role: role, Content: []llm.Content{llm.StringContent(text)}, EndOfTurn: end}
		if _, err := database.CreateMessage(t.Context(), db.CreateMessageParams{ConversationID: id, Type: typ, LLMData: msg}); err != nil {
			t.Fatal(err)
		}
	}
	add(db.MessageTypeUser, llm.MessageRoleUser, "earlier typed request", false)
	add(db.MessageTypeAgent, llm.MessageRoleAssistant, "earlier agent reply", true)
	w := postLiveMessage(s, id, `{"id":"g1","transcript":"yes, that file","voice_context":"USER: there's a typo in the rewrite prompt\nLIVE: Which file, server/live_message.go?"}`)
	voice := "USER: there's a typo in the rewrite prompt\nLIVE: Which file, server/live_message.go?"
	var response liveMessageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusAccepted || response.Task != task || response.Message != liveBackendMessage(task, voice) {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if got := userTexts(t, database, id); len(got) != 2 || got[1] != liveBackendMessage(task, voice) {
		t.Fatalf("user messages = %q", got)
	}

	// Shelley's context is read fresh on every delegation.
	add(db.MessageTypeAgent, llm.MessageRoleAssistant, "agent reply added after the first delegation", true)
	postLiveMessage(s, id, `{"id":"g2","transcript":"and then run the tests"}`)
	prompts := rewritePrompts(fake)
	if len(prompts) != 2 {
		t.Fatalf("prompts = %q", prompts)
	}
	for _, want := range []string{"earlier agent reply", "<voice_context>\nUSER: there's a typo in the rewrite prompt\nLIVE: Which file, server/live_message.go?\n</voice_context>", "<transcript>\nyes, that file\n</transcript>"} {
		if !strings.Contains(prompts[0], want) {
			t.Errorf("first prompt missing %q: %q", want, prompts[0])
		}
	}
	if strings.Contains(prompts[0], "added after") || !strings.Contains(prompts[1], "agent reply added after the first delegation") {
		t.Errorf("context is not fresh per request:\n%q\n%q", prompts[0], prompts[1])
	}
}

func TestLiveMessageDedupIsPerIDAndSkipsFailures(t *testing.T) {
	s, database, fake, id := newLiveMessageServer(t, "")
	body := func(delegation string) string {
		return `{"id":"` + delegation + `","transcript":"run the tests"}`
	}
	if w := postLiveMessage(s, id, body("d1")); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty rewrite status = %d", w.Code)
	}
	fake.mu.Lock()
	fake.reply = "Run the tests."
	fake.mu.Unlock()
	// A failed id is not remembered: the retry runs the model and submits.
	for _, delegation := range []string{"d1", "d1", "d2"} {
		if w := postLiveMessage(s, id, body(delegation)); w.Code != http.StatusAccepted {
			t.Fatalf("%s: status = %d: %s", delegation, w.Code, w.Body.String())
		}
	}
	if got := userTexts(t, database, id); len(got) != 2 {
		t.Fatalf("user messages = %q, want d1 and d2 submitted once each", got)
	}
	if prompts := rewritePrompts(fake); len(prompts) != 3 {
		t.Fatalf("model calls = %d, want 3 (failed d1, d1, d2)", len(prompts))
	}
}

func TestLiveRewriteInstructionsDescribeDelegatedTaskOnly(t *testing.T) {
	for _, want := range []string{
		"Interpret only the task the user delegated",
		"Describe the task; never perform it",
		"never answer",
		"VOICE CONTEXT first and SHELLEY CONTEXT second",
		"yes, that file",
		"output nothing",
		"verbatim as transcribed",
	} {
		if !strings.Contains(strings.ToLower(liveRewriteInstructions), strings.ToLower(want)) {
			t.Errorf("instructions missing %q", want)
		}
	}
}

func TestLiveStartupNeverSubmitsToShelley(t *testing.T) {
	s, database, fake, id := newLiveMessageServer(t, "should never be used")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"session":{"id":"live_x"},"transport":{"type":"webrtc","sdp":"answer"}}`)
	}))
	defer upstream.Close()
	s.liveSessionURL, s.liveSessionClient = upstream.URL, upstream.Client()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	do := func(method, path, body string) int {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/api/conversation/"+id+path, strings.NewReader(body)))
		return w.Code
	}
	if code := do(http.MethodPost, "/live-session", `{"sdp":"v=0\r\n"}`); code != http.StatusCreated {
		t.Fatalf("live-session status = %d", code)
	}
	// Only the client's POST live-message submits; no other method does.
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if code := do(method, "/live-message", `{"id":"a","transcript":"hi"}`); code == http.StatusAccepted {
			t.Errorf("%s /live-message was accepted", method)
		}
	}
	if got := userTexts(t, database, id); len(got) != 0 {
		t.Fatalf("server submitted %q on its own", got)
	}
	if prompts := rewritePrompts(fake); len(prompts) != 0 {
		t.Fatalf("server called the rewrite model on its own: %q", prompts)
	}
}
