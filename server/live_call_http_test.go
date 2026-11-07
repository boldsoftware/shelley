package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// liveProvider stands in for GPT-Live's session endpoint, minting a new
// session id per call.
func (e *liveMessageEnv) liveProvider() {
	var n atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"session":{"id":"live_%d"},"transport":{"type":"webrtc","sdp":"answer"}}`, n.Add(1))
	}))
	e.t.Cleanup(provider.Close)
	e.s.liveSessionURL, e.s.liveSessionClient = provider.URL, provider.Client()
}

func (e *liveMessageEnv) do(method, path, body string) (int, string) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("X-ExeDev-Email", "voice@example.com")
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// liveCallClient is a client's side of a call: the instructions it got.
type liveCallClient struct {
	e            *liveMessageEnv
	id, session  string
	path         string // /api/conversation/<id>/live-session/<session>
	instructions chan string
	ended        chan struct{} // closes when the latest stream ends
}

// startCall starts a call and attaches to its instruction stream.
func (e *liveMessageEnv) startCall(conversationID, settings string) *liveCallClient {
	e.t.Helper()
	code, body := e.post("/api/conversation/"+conversationID+"/live-session", `{"sdp":"v=0\r\nm=audio test"`+settings+`}`)
	var session struct {
		Session struct{ ID string } `json:"session"`
	}
	if err := json.Unmarshal([]byte(body), &session); code != http.StatusCreated || err != nil {
		e.t.Fatalf("live-session: %d %s", code, body)
	}
	c := &liveCallClient{
		e: e, id: conversationID, session: session.Session.ID,
		path:         "/api/conversation/" + conversationID + "/live-session/" + session.Session.ID,
		instructions: make(chan string, 100),
	}
	c.attach("")
	return c
}

func (c *liveCallClient) attach(query string) {
	c.e.t.Helper()
	ctx, cancel := context.WithCancel(c.e.t.Context())
	c.e.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.e.ts.URL+c.path+"/stream"+query, nil)
	resp, err := c.e.ts.Client().Do(req)
	if err != nil || resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		c.e.t.Fatalf("stream: %v %+v", err, resp)
	}
	ended := make(chan struct{})
	c.ended = ended
	go func() {
		defer close(ended)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
				c.instructions <- describeLiveInstruction(c.e.t, []byte(data))
			}
		}
	}()
}

func (c *liveCallClient) events(events ...string) {
	c.e.t.Helper()
	if code, body := c.e.post(c.path+"/events", `{"events":[`+strings.Join(events, ",")+`]}`); code != http.StatusNoContent {
		c.e.t.Fatalf("events: %d %s", code, body)
	}
}

// expect waits for the next instructions.
func (c *liveCallClient) expect(want ...string) {
	c.e.t.Helper()
	for _, w := range want {
		select {
		case got := <-c.instructions:
			if !strings.HasPrefix(got, w) {
				c.e.t.Fatalf("instruction %q, want %q…", got, w)
			}
		case <-time.After(10 * time.Second):
			c.e.t.Fatalf("no instruction; want %q", w)
		}
	}
}

// A call relays Live's events; the server decides the task, delivers it
// through chat, and tells Live what came of it and what the backend says.
func TestLiveCallOverHTTP(t *testing.T) {
	t.Parallel()
	e := newLiveMessageEnv(t)
	e.liveProvider()
	slug := "voice-call"
	conv, err := e.database.CreateConversation(t.Context(), &slug, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	if _, err := e.database.CreateMessage(t.Context(), db.CreateMessageParams{ConversationID: id, Type: db.MessageTypeUser, LLMData: llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{llm.StringContent("before the call")}}}); err != nil {
		t.Fatal(err)
	}

	// The probe, and calls the server doesn't have.
	base := "/api/conversation/" + id + "/live-session/"
	for _, tt := range []struct {
		method, path, body string
		code               int
		reply              string
	}{
		{"POST", base + "probe/events", "", 400, "Invalid live events"},
		{"POST", base + "probe/events", `{"events":[]}`, 400, "Invalid live events"},
		{"POST", base + "probe/events", `{"events":["text"]}`, 400, "Invalid live events"},
		{"POST", base + "probe/events", `{"events":[{"type":"session.started"}]}`, 404, "Live call not found"},
		{"GET", base + "probe/stream", "", 404, "Live call not found"},
		{"DELETE", base + "probe", "", 204, ""},
	} {
		if code, body := e.do(tt.method, tt.path, tt.body); code != tt.code || body != tt.reply {
			t.Errorf("%s %s %s = %d %q", tt.method, tt.path, tt.body, code, body)
		}
	}

	call := e.startCall(id, "")
	if code, _ := e.do("GET", "/api/conversation/other"+strings.TrimPrefix(call.path, "/api/conversation/"+id)+"/stream", ""); code != 404 {
		t.Errorf("another conversation's call: %d", code)
	}
	call.events(
		`{"type":"session.started"}`,
		`{"type":"session.output_transcript.delta","delta":"Hi!","start_ms":0,"end_ms":400}`,
		`{"type":"session.input_transcript.delta","delta":"Run the","start_ms":1000,"end_ms":1500}`,
		`{"type":"session.delegation.created","delegation":{"id":"d1","target":"client"},"offset_ms":1500}`,
	)
	call.events(`{"type":"session.input_transcript.delta","delta":" tests.","start_ms":1500,"end_ms":1900}`)
	call.expect("activity: true")
	// The request points into the call's transcript, saved by now.
	message := "Spoken request:\nRun the tests." + exchange(1, 3, "LIVE: Hi!\nUSER: Run the tests.") + liveTranscriptReference(1)
	turn := e.held.waitCall(t, message)
	call.expect("error: null", "handoff 3:", "thinking [d1]: The backend accepted this task: Run the tests.", "activity: false")
	e.finish(id, turn)
	call.expect("commentary [d1]: Backend reply: ")
	call.events(`{"type":"session.output_transcript.delta","delta":"Got it.","start_ms":2000,"end_ms":2400}`)

	// What the user types reaches Live, as does its reply.
	if code, body := e.post("/api/conversation/"+id+"/chat", `{"message":"also lint it"}`); code != http.StatusAccepted {
		t.Fatal(code, body)
	}
	e.finish(id, e.held.waitCall(t, "also lint it"))
	call.expect("thinking [-]: New typed message to the backend: also lint it", "commentary [-]: Backend reply: ")

	// A stream that reconnects picks up after what it handled.
	first := call.ended
	call.attach("?after=6")
	<-first // the newer stream replaces it
	call.expect("thinking [-]: New typed message", "commentary [-]: Backend reply: ")

	if code, _ := e.do("DELETE", call.path, ""); code != http.StatusNoContent {
		t.Errorf("hang up: %d", code)
	}
	<-call.ended
	// The whole call stays readable.
	if _, got := e.do("GET", "/api/conversation/"+id+"/call/1/transcript", ""); got != "1 Live: Hi!\n3 User: Run the tests.\n4 Live: Got it." {
		t.Errorf("transcript = %q", got)
	}
	if code, body := e.post(call.path+"/events", `{"events":[{"type":"session.started"}]}`); code != http.StatusNotFound {
		t.Errorf("events after hang-up: %d %s", code, body)
	}
	// Deleting the conversation deletes its calls' transcripts.
	if code, body := e.post("/api/conversation/"+id+"/delete", ""); code != http.StatusOK {
		t.Fatalf("delete: %d %s", code, body)
	}
	if code, got := e.do("GET", "/api/conversation/"+id+"/call/1/transcript", ""); code != http.StatusOK || got != "" {
		t.Errorf("transcript after delete = %d %q", code, got)
	}
}

// A call in a blank draft promotes it with the composer's selection on its
// first task, as the first typed send would.
func TestLiveCallStartsADraft(t *testing.T) {
	t.Parallel()
	e := newLiveMessageEnv(t)
	e.liveProvider()
	if code, body := e.post("/api/profiles", `{"name":"Personal","model":"predictable","system_prompt":"Be a helpful personal assistant.","tool_overrides":{"bash":"off"}}`); code != http.StatusCreated {
		t.Fatal(code, body)
	}
	draft, err := e.database.CreateDraftConversation(t.Context(), nil, nil, db.ConversationOptions{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.database.UpdateConversationSlug(t.Context(), draft.ConversationID, "voice-draft"); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	call := e.startCall(draft.ConversationID, `,"model":"predictable","cwd":"`+cwd+`","conversation_options":{"profile":"Personal","thinking_level":"high"}`)
	call.events(
		`{"type":"session.input_transcript.delta","delta":"inspect the project","start_ms":0,"end_ms":900}`,
		`{"type":"session.delegation.created","delegation":{"id":"d1","target":"client"},"offset_ms":900}`,
	)
	turn := e.held.waitCall(t, "Spoken request:\ninspect the project"+exchange(1, 1, "USER: inspect the project")+liveTranscriptReference(1))
	if !strings.Contains(systemText(turn.request), "Be a helpful personal assistant.") {
		t.Error("main backend lost the profile's system prompt")
	}
	// The reply can arrive while the delivery is still in flight, ahead of
	// "activity: false", so finish the turn only once the call is idle.
	call.expect("activity: true", "error: null", "handoff 1:", "thinking [d1]: The backend accepted this task", "activity: false")
	e.finish(draft.ConversationID, turn)
	call.expect("commentary [d1]: Backend reply: ")
	row, err := e.database.GetConversationByID(t.Context(), draft.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	options := db.ParseConversationOptions(row.ConversationOptions)
	if row.IsDraft || derefString(row.Cwd) != cwd || derefString(row.Model) != "predictable" || options.Profile != "Personal" || options.ToolOverrides["bash"] != "off" || options.ThinkingLevel != "high" {
		t.Fatalf("first send settings: %+v options=%+v", row, options)
	}

	// Live closing the session ends the call.
	call.events(`{"type":"session.closed"}`)
	<-call.ended
}

func TestLiveCallSendsCompleteFreshExchange(t *testing.T) {
	t.Parallel()
	e := newLiveMessageEnv(t)
	e.liveProvider()
	slug := "voice-complete-exchange"
	conv, err := e.database.CreateConversation(t.Context(), &slug, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	call := e.startCall(conv.ConversationID, "")
	response := strings.Repeat("Complete Live response. ", 300) + "Finished."
	call.events(
		`{"type":"session.input_transcript.delta","delta":"Run tests","start_ms":0,"end_ms":500}`,
		fmt.Sprintf(`{"type":"session.output_transcript.delta","delta":%q,"start_ms":600,"end_ms":7000}`, response),
		`{"type":"session.delegation.created","delegation":{"id":"d1","target":"client"},"offset_ms":7000}`,
	)
	message := "Spoken request:\nRun tests" + exchange(1, 2, "USER: Run tests\nLIVE: "+response) + liveTranscriptReference(1)
	turn := e.held.waitCall(t, message)
	call.expect("activity: true", "error: null", "handoff 2:", "thinking [d1]: The backend accepted this task", "activity: false")
	e.finish(conv.ConversationID, turn)
	call.expect("commentary [d1]: Backend reply: ")
	if code, body := e.do("DELETE", call.path, ""); code != http.StatusNoContent {
		t.Fatal(code, body)
	}
	<-call.ended
}

func TestLiveTranscriptLateCaptionStaysForNextHandoff(t *testing.T) {
	t.Parallel()
	e := newLiveMessageEnv(t)
	e.liveProvider()
	slug := "voice-late-caption"
	conv, err := e.database.CreateConversation(t.Context(), &slug, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	call := e.startCall(conv.ConversationID, "")
	call.events(
		`{"type":"session.input_transcript.delta","delta":"Cap the panel","start_ms":0,"end_ms":500}`,
		`{"type":"session.output_transcript.delta","delta":"I'll make sure the live chat","start_ms":600,"end_ms":2000}`,
		`{"type":"session.delegation.created","delegation":{"id":"d1","target":"client"},"offset_ms":2000}`,
	)
	message := "Spoken request:\nCap the panel" + exchange(1, 2, "USER: Cap the panel\nLIVE: I'll make sure the live chat") + liveTranscriptReference(1)
	turn := e.held.waitCall(t, message)
	call.expect("activity: true", "error: null", "handoff 2:", "thinking [d1]: The backend accepted this task", "activity: false")

	call.events(
		`{"type":"session.output_transcript.delta","delta":" panel doesn't get too tall.","start_ms":2000,"end_ms":3000}`,
		`{"type":"session.input_transcript.delta","delta":"Run tests","start_ms":4000,"end_ms":4500}`,
	)
	metadata, err := e.database.ListMessagesByType(t.Context(), conv.ConversationID, db.MessageTypeLiveTranscript)
	if err != nil || len(metadata) != 0 {
		t.Fatalf("late caption created metadata: %+v, %v", metadata, err)
	}
	users, err := e.database.ListMessagesByType(t.Context(), conv.ConversationID, db.MessageTypeUser)
	if err != nil || len(users) != 1 {
		t.Fatalf("late caption dispatched another task: %+v, %v", users, err)
	}
	var original llm.Message
	if err := json.Unmarshal([]byte(*users[0].LlmData), &original); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, "original request is immutable", liveMessageText(original), message)

	e.finish(conv.ConversationID, turn)
	call.events(`{"type":"session.delegation.created","delegation":{"id":"d2","target":"client"},"offset_ms":4500}`)
	nextMessage := "Spoken request:\nRun tests" + exchange(1, 4, "USER: Cap the panel\nLIVE: I'll make sure the live chat\nLIVE: panel doesn't get too tall.\nUSER: Run tests") + liveTranscriptReference(1)
	next := e.held.waitCall(t, nextMessage)
	e.finish(conv.ConversationID, next)
	if code, body := e.do("DELETE", call.path, ""); code != http.StatusNoContent {
		t.Fatal(code, body)
	}
	<-call.ended
}

func TestLiveDeliveryIdentitySurvivesQueueDrainAsTypedUserWork(t *testing.T) {
	t.Parallel()
	e := newLiveMessageEnv(t)
	conv, err := e.database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := e.s.getOrCreateConversationManager(t.Context(), conv.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	manager.SetAgentWorking(true)
	deliveryID := uuid.NewString()
	e.s.mu.Lock()
	e.s.stoppedParents[conv.ConversationID] = true
	e.s.mu.Unlock()
	ctx := contextWithTurnUserData(t.Context(), liveDeliveryUserData{DeliveryID: deliveryID})
	message := llm.UserStringMessage("Spoken request:\nRun tests")
	if err := manager.QueueMessage(ctx, "predictable", message); err != nil {
		t.Fatal(err)
	}
	queued, err := e.database.GetQueuedMessages(t.Context(), conv.ConversationID)
	if err != nil || len(queued) != 1 || liveDeliveryIDFromUserData(queued[0].UserData) != deliveryID {
		t.Fatalf("queued delivery identity = %+v, %v", queued, err)
	}
	if !typedUserForLLM(message, queued[0].UserData) {
		t.Fatal("queued Live delivery stopped being an ordinary typed user turn")
	}
	e.s.mu.Lock()
	reopened := !e.s.stoppedParents[conv.ConversationID]
	e.s.mu.Unlock()
	if !reopened {
		t.Fatal("queued Live delivery did not reopen parent completions")
	}
	manager.SetAgentWorking(false)
	<-manager.drainPendingMessages()
	rows, err := e.database.ListTypedUserMessages(t.Context(), conv.ConversationID)
	if err != nil || len(rows) != 1 || liveDeliveryIDFromUserData([]byte(derefString(rows[0].UserData))) != deliveryID {
		t.Fatalf("saved typed delivery identity = %+v, %v", rows, err)
	}
	if _, ok, err := (userMessageFinder{db: e.database, conversationID: conv.ConversationID}).FindUserMessageBySequence(t.Context(), rows[0].SequenceID); err != nil || !ok {
		t.Fatalf("Live delivery lost reply-target semantics: %v, %v", ok, err)
	}
}

func TestLiveDeliveryImmediateModelInputHasSequenceID(t *testing.T) {
	t.Parallel()
	e := newLiveMessageEnv(t)
	conv, err := e.database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{
		ToolOverrides: map[string]string{"message_user": "on"},
	})
	if err != nil {
		t.Fatal(err)
	}
	code, receipt := e.post("/api/conversation/"+conv.ConversationID+"/live-message", `{"transcript":"Run tests"}`)
	var chat struct{ Message string }
	if err := json.Unmarshal([]byte(receipt), &chat); err != nil || code != http.StatusAccepted {
		t.Fatalf("delegation: %d %s", code, receipt)
	}
	rows, err := e.database.ListMessagesByType(t.Context(), conv.ConversationID, db.MessageTypeUser)
	if err != nil || len(rows) != 1 {
		t.Fatalf("stored user messages: %d, %v", len(rows), err)
	}
	var stored llm.Message
	if err := json.Unmarshal([]byte(*rows[0].LlmData), &stored); err != nil {
		t.Fatal(err)
	}
	if messageText(stored) != chat.Message {
		t.Fatal("sequence wrapper changed the stored request")
	}
	wrapped, err := messageWithSequenceID(stored, "", rows[0].SequenceID)
	if err != nil {
		t.Fatal(err)
	}
	call := e.held.waitCall(t, messageText(wrapped))
	e.finish(conv.ConversationID, call)
}
