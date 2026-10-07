package server

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

// gateLLMService is the predictable model, except that a request whose last
// user text is held waits until that text is released.
type gateLLMService struct {
	llm.Service
	mu       sync.Mutex
	gates    map[string]chan struct{}
	started  map[string]chan struct{}
	requests map[string]*llm.Request
}

func newGateLLMService(held ...string) *gateLLMService {
	s := &gateLLMService{Service: predictable.NewService(), gates: map[string]chan struct{}{}, started: map[string]chan struct{}{}, requests: map[string]*llm.Request{}}
	for _, text := range held {
		s.gates[text] = make(chan struct{})
		s.started[text] = make(chan struct{})
	}
	return s
}

func (s *gateLLMService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	text := lastRequestUserText(request)
	s.mu.Lock()
	for held := range s.gates {
		if (strings.HasPrefix(text, "<external_message sequence_id=\"") && strings.HasSuffix(text, "\n"+held+"\n</external_message>")) ||
			(strings.HasPrefix(text, "<user_message sequence_id=\"") && strings.HasSuffix(text, "\n"+held+"\n</user_message>")) {
			text = held
			break
		}
	}
	gate, started := s.gates[text], s.started[text]
	s.requests[text] = request
	if started != nil {
		close(started)
		delete(s.started, text)
	}
	s.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.Service.Do(ctx, request)
}

// waitStarted waits for the held request for text to reach the model.
func (s *gateLLMService) waitStarted(t *testing.T, text string) *llm.Request {
	t.Helper()
	s.mu.Lock()
	started := s.started[text]
	request := s.requests[text]
	s.mu.Unlock()
	if started == nil {
		return request
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for LLM call %q", text)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[text]
}

func (s *gateLLMService) release(text string) { close(s.gates[text]) }

func channelTestServer(t *testing.T, service llm.Service) (*Server, *db.DB, http.Handler) {
	t.Helper()
	server, database, _ := newTestServer(t)
	if service != nil {
		server.llmManager = &testLLMManager{service: service}
	}
	t.Cleanup(func() { stopActiveConversationLoops(server) })
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	return server, database, mux
}

// debugChannelTestServer is a channelTestServer listening where debug
// chats' endpoints point.
func debugChannelTestServer(t *testing.T) (*Server, *db.DB, http.Handler, *httptest.Server) {
	t.Helper()
	server, database, h := channelTestServer(t, nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if server.listenPort, err = strconv.Atoi(u.Port()); err != nil {
		t.Fatal(err)
	}
	return server, database, h, ts
}

func postJSON(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func conversationByExternalID(t *testing.T, database *db.DB, chatID string) (generated.Conversation, error) {
	t.Helper()
	var conv generated.Conversation
	err := database.Queries(t.Context(), func(q *generated.Queries) (err error) {
		conv, err = q.GetConversationByExternalID(t.Context(), &chatID)
		return err
	})
	return conv, err
}

// channelUserMessages returns the conversation's user messages as
// "external id: text".
func channelUserMessages(t *testing.T, database *db.DB, conversationID string) []string {
	t.Helper()
	var messages []generated.Message
	err := database.Queries(t.Context(), func(q *generated.Queries) (err error) {
		messages, err = q.ListMessages(t.Context(), conversationID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range messages {
		if m.Type != string(db.MessageTypeUser) || m.LlmData == nil {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &msg); err != nil {
			t.Fatal(err)
		}
		if len(msg.Content) == 0 || msg.Content[0].Type != llm.ContentTypeText {
			continue
		}
		id := ""
		if m.ExternalMessageID != nil {
			id = *m.ExternalMessageID
		}
		out = append(out, id+": "+msg.Content[0].Text)
	}
	return out
}

func TestChannelMessageExternalIDOnlyInLLM(t *testing.T) {
	t.Parallel()
	model := predictable.NewService()
	server, database, h := channelTestServer(t, model)
	const chat, id, text = "chat-model", `m<&"`, "hi <tag> & team"
	if w := postJSON(t, h, "/api/channels/messages", channelEvent{
		Type: "message", ID: id, ChatID: chat, Sender: "+15550100", Text: text,
	}); w.Code != http.StatusNoContent {
		t.Fatalf("receive: %d %s", w.Code, w.Body.String())
	}
	conv, err := conversationByExternalID(t, database, chat)
	if err != nil {
		t.Fatal(err)
	}
	typed, err := database.ListTypedUserMessages(t.Context(), conv.ConversationID)
	if err != nil || len(typed) != 1 {
		t.Fatalf("typed user messages = %d: %v", len(typed), err)
	}
	want := fmt.Sprintf("<external_message sequence_id=\"%d\">\nhi &lt;tag&gt; &amp; team\n</external_message>", typed[0].SequenceID)
	var live *llm.Request
	waitFor(t, 5*time.Second, func() bool {
		for _, req := range model.GetRecentRequests() {
			if hasTool(req, claudetool.MessageUserName) {
				live = req
				return true
			}
		}
		return false
	})
	if got := lastRequestUserText(live); got != want {
		t.Fatalf("live LLM message = %q, want %q", got, want)
	}
	if got := channelUserMessages(t, database, conv.ConversationID); !slices.Equal(got, []string{id + ": " + text}) {
		t.Fatalf("stored channel message = %q, want original text", got)
	}

	manager, err := server.getOrCreateConversationManager(t.Context(), conv.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	var rows []generated.Message
	if err := database.Queries(t.Context(), func(q *generated.Queries) (err error) {
		rows, err = q.ListMessagesForContext(t.Context(), conv.ConversationID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	history, _, err := manager.partitionMessages(rows)
	if err != nil {
		t.Fatal(err)
	}
	var rehydrated string
	for _, msg := range history {
		if msg.Role == llm.MessageRoleUser && strings.Contains(messageText(msg), "external_message") {
			rehydrated = messageText(msg)
		}
	}
	if got := rehydrated; got != want {
		t.Fatalf("rehydrated LLM message = %q, want %q", got, want)
	}
}

func TestChannelMessageInjectsAfterCurrentTool(t *testing.T) {
	for _, queuedUI := range []bool{false, true} {
		t.Run(fmt.Sprintf("queuedUI=%t", queuedUI), func(t *testing.T) {
			t.Parallel()
			gate := newGateLLMService("third", "from the UI")
			server, database, h := channelTestServer(t, gate)
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			releaseTool := func() { once.Do(func() { close(release) }) }
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/send" {
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
				close(started)
				<-release
				fmt.Fprint(w, `{"message_id":"reply-1"}`)
			}))
			t.Cleanup(endpoint.Close)
			t.Cleanup(releaseTool)
			conv, err := server.channelConversation(t.Context(), endpoint.URL, "chat-tool", "")
			if err != nil {
				t.Fatal(err)
			}
			deliver := func(id, text string) {
				t.Helper()
				if w := postJSON(t, h, "/api/channels/messages", channelEvent{Type: "message", ID: id, ChatID: "chat-tool", Text: text}); w.Code != http.StatusNoContent {
					t.Fatalf("deliver %s: status %d: %s", id, w.Code, w.Body.String())
				}
			}
			prompt := `message_user: {"text":"working"}`
			deliver("m1", prompt)

			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("tool did not start")
			}
			manager, err := server.getOrCreateConversationManager(t.Context(), conv.ConversationID, "")
			if err != nil {
				t.Fatal(err)
			}
			if queuedUI {
				if err := manager.QueueMessage(t.Context(), server, "predictable", llm.UserStringMessage("from the UI")); err != nil {
					t.Fatal(err)
				}
			}
			deliver("m2", "second")
			deliver("m3", "third")
			deliver("m2", "second")
			queued, err := database.GetQueuedMessages(t.Context(), conv.ConversationID)
			if err != nil {
				t.Fatal(err)
			}
			offset := 0
			if queuedUI {
				offset = 1
			}
			if len(queued) != offset+2 || !queued[offset].Inject || !queued[offset+1].Inject {
				t.Fatalf("webhook messages must wait for the tool as injectable entries: %+v", queued)
			}
			if got := channelUserMessages(t, database, conv.ConversationID); len(got) != 1 {
				t.Fatalf("messages recorded before tool finished: %v", got)
			}
			releaseTool()
			next := gate.waitStarted(t, "third")
			messages := next.Messages
			if len(messages) < 3 {
				t.Fatalf("next request lacks tool result and incoming messages: %+v", messages)
			}
			tail := messages[len(messages)-3:]
			if tail[0].Content[0].Type != llm.ContentTypeToolResult || tail[0].Content[0].ToolError ||
				!strings.HasSuffix(messageText(tail[1]), "\nsecond\n</external_message>") ||
				!strings.HasSuffix(messageText(tail[2]), "\nthird\n</external_message>") {
				t.Fatalf("next request must contain tool result followed by webhook messages: %+v", tail)
			}
			for _, m := range listMessages(t, database, conv.ConversationID) {
				if m.Type == string(db.MessageTypeAgent) && storedLLMMessage(t, &m).EndOfTurn {
					t.Fatal("webhook message waited until the turn ended")
				}
			}
			want := []string{"m1: " + prompt, "m2: second", "m3: third"}
			if got := channelUserMessages(t, database, conv.ConversationID); !slices.Equal(got, want) {
				t.Fatalf("user messages = %v, want %v", got, want)
			}
			gate.release("third")
			if queuedUI {
				gate.waitStarted(t, "from the UI")
				gate.release("from the UI")
			}
			waitFor(t, 5*time.Second, func() bool {
				c, err := database.GetConversationByID(t.Context(), conv.ConversationID)
				return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
			})
		})
	}
}

func TestChannelMessageDeliversToChatConversation(t *testing.T) {
	t.Parallel()
	gate := newGateLLMService("first")
	server, database, h := channelTestServer(t, gate)
	deliver := func(ev channelEvent) {
		t.Helper()
		if w := postJSON(t, h, "/api/channels/messages", ev); w.Code != http.StatusNoContent {
			t.Fatalf("deliver %+v: status %d: %s", ev, w.Code, w.Body.String())
		}
	}

	deliver(channelEvent{Type: "message", ID: "m1", ChatID: "chat-1", Sender: "+15550100", Text: "first"})
	conv, err := conversationByExternalID(t, database, "chat-1")
	if err != nil {
		t.Fatal(err)
	}
	if conv.ExternalEndpoint == nil || *conv.ExternalEndpoint != "https://messages.int.exe.xyz" {
		t.Fatalf("external_endpoint = %v, want the messages integration", conv.ExternalEndpoint)
	}
	gate.waitStarted(t, "first")

	// While the agent works, a message waits for the next model request;
	// retries of both recorded and queued messages are dropped.
	deliver(channelEvent{Type: "message", ID: "m2", ChatID: "chat-1", Text: "second"})
	deliver(channelEvent{Type: "message", ID: "m1", ChatID: "chat-1", Text: "first"})
	deliver(channelEvent{Type: "message", ID: "m2", ChatID: "chat-1", Text: "second"})
	if got := strings.Join(channelUserMessages(t, database, conv.ConversationID), "\n"); got != "m1: first" {
		t.Fatalf("user messages mid-turn:\n%s", got)
	}

	// Channel messages are injectable even behind a queued UI message.
	manager, err := server.getOrCreateConversationManager(t.Context(), conv.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.QueueMessage(t.Context(), server, "predictable", llm.UserStringMessage("from the UI")); err != nil {
		t.Fatal(err)
	}
	deliver(channelEvent{Type: "message", ID: "m3", ChatID: "chat-1", Text: "third"})
	deliver(channelEvent{Type: "message", ID: "m3", ChatID: "chat-1", Text: "third"})
	queued, err := database.GetQueuedMessages(t.Context(), conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 3 || queued[0].ExternalMessageID != "m2" || !queued[0].Inject ||
		queued[1].Inject || queued[2].ExternalMessageID != "m3" || !queued[2].Inject {
		t.Fatalf("queue = %+v, want injectable m2, the UI message, and injectable m3", queued)
	}

	gate.release("first")
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), conv.ConversationID)
		return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
	})
	deliver(channelEvent{Type: "message", ID: "m3", ChatID: "chat-1", Text: "third"})
	deliver(channelEvent{Type: "message", ID: "m4", ChatID: "chat-1", Text: "fourth"})
	// Reactions and empty messages are not delivered yet.
	deliver(channelEvent{Type: "reaction", ChatID: "chat-1"})
	deliver(channelEvent{Type: "message", ID: "m5", ChatID: "chat-1", Text: " "})
	// An opt-out is exed's business; replies would be refused.
	deliver(channelEvent{Type: "message", ID: "m6", ChatID: "chat-1", Text: "STOP", OptOut: true})
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), conv.ConversationID)
		return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
	})

	got := strings.Join(channelUserMessages(t, database, conv.ConversationID), "\n")
	if want := "m1: first\nm2: second\n: from the UI\nm3: third\nm4: fourth"; got != want {
		t.Fatalf("user messages:\n%s\nwant:\n%s", got, want)
	}
	var sawQueued, sawUI bool
	for _, req := range gate.Service.(*predictable.Service).GetRecentRequests() {
		for _, msg := range req.Messages {
			if msg.Role != llm.MessageRoleUser {
				continue
			}
			sawQueued = sawQueued || strings.HasPrefix(messageText(msg), "<external_message sequence_id=\"") &&
				strings.HasSuffix(messageText(msg), "\nthird\n</external_message>")
			sawUI = sawUI || strings.HasPrefix(messageText(msg), "<user_message sequence_id=\"") &&
				strings.HasSuffix(messageText(msg), "\nfrom the UI\n</user_message>")
		}
	}
	if !sawQueued || !sawUI {
		t.Fatalf("queued channel and UI messages in LLM requests: channel=%v UI=%v", sawQueued, sawUI)
	}

	// Another chat gets its own conversation.
	deliver(channelEvent{Type: "message", ID: "m1", ChatID: "chat-2", Text: "other chat"})
	other, err := conversationByExternalID(t, database, "chat-2")
	if err != nil {
		t.Fatal(err)
	}
	if other.ConversationID == conv.ConversationID {
		t.Fatal("chat-2 was delivered to chat-1's conversation")
	}
}

func TestChannelMessageRejectsBadEvents(t *testing.T) {
	t.Parallel()
	_, _, h := channelTestServer(t, nil)
	if w := postJSON(t, h, "/api/channels/messages", channelEvent{Type: "message", ID: "m1", Text: "hi"}); w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/channels/messages", strings.NewReader("{"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: status %d, want 400", w.Code)
	}
	// Debug chat ids belong to /debug/channels, whose chats reply to the
	// emulated gateway.
	if w := postJSON(t, h, "/api/channels/messages", channelEvent{Type: "message", ID: "m1", ChatID: "debug-x", Text: "hi"}); w.Code != http.StatusBadRequest {
		t.Fatalf("debug chat id: status %d, want 400", w.Code)
	}
}

// The debug page's chats run the whole loop over HTTP: the phone's text
// lands in the chat's conversation, and the conversation's replies reach the
// emulated gateway, which refuses them like exed's.
func TestDebugChannelRoundTrip(t *testing.T) {
	t.Parallel()
	server, database, h, ts := debugChannelTestServer(t)
	port := strconv.Itoa(server.listenPort)

	const chat = "debug-abc"
	if w := postJSON(t, h, "/api/debug/channels/"+chat+"/receive", map[string]string{"text": "hello"}); w.Code != http.StatusOK {
		t.Fatalf("receive: status %d: %s", w.Code, w.Body.String())
	}
	conv, err := conversationByExternalID(t, database, chat)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "http://localhost:" + port + "/debug/channels/gateway/" + chat + "/"
	if conv.ExternalEndpoint == nil || !strings.HasPrefix(*conv.ExternalEndpoint, prefix) {
		t.Fatalf("external_endpoint = %v, want %s<token>", conv.ExternalEndpoint, prefix)
	}
	endpoint := *conv.ExternalEndpoint
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), conv.ConversationID)
		return err == nil && !c.AgentWorking
	})
	if got := channelUserMessages(t, database, conv.ConversationID); len(got) != 1 || !strings.HasPrefix(got[0], "debug-in-") || !strings.HasSuffix(got[0], "-1: hello") {
		t.Fatalf("user messages = %q", got)
	}

	send := func(text string) error {
		_, err := server.sendChannelMessage(t.Context(), conv.ConversationID, text, "")
		return err
	}
	id, err := server.sendChannelMessage(t.Context(), conv.ConversationID, "hi back", "")
	if err != nil || !strings.HasPrefix(id, "debug-out-") || !strings.HasSuffix(id, "-2") {
		t.Fatalf("send = %q, %v", id, err)
	}
	// The page's reply box takes the same path.
	if w := postJSON(t, h, "/api/debug/channels/"+chat+"/send", map[string]string{"text": "via page"}); w.Code != http.StatusOK {
		t.Fatalf("page send: status %d: %s", w.Code, w.Body.String())
	}
	for i := range 3 {
		if err := send("more " + strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	var refusal *channelSendError
	if err := send("too many"); !errors.As(err, &refusal) || refusal.Status != http.StatusTooManyRequests || refusal.Code != "unanswered" {
		t.Fatalf("sixth unanswered send: %v", err)
	}
	if w := postJSON(t, h, "/api/debug/channels/"+chat+"/send", map[string]string{"text": "too many"}); w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"unanswered"`) {
		t.Fatalf("page send past the limit: status %d: %s", w.Code, w.Body.String())
	}

	// Writing back lifts the limit.
	if w := postJSON(t, h, "/api/debug/channels/"+chat+"/receive", map[string]string{"text": "ok"}); w.Code != http.StatusOK {
		t.Fatalf("receive: status %d: %s", w.Code, w.Body.String())
	}
	if err := send("answered"); err != nil {
		t.Fatal(err)
	}

	// Imposed refusals: line_paused spares typing; opted_out refuses it.
	gateway := func(method, base, action string) int {
		req, err := http.NewRequestWithContext(t.Context(), method, ts.URL+strings.TrimPrefix(base, "http://localhost:"+port)+"/"+action, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	refuse := func(code string) {
		t.Helper()
		if w := postJSON(t, h, "/api/debug/channels/"+chat+"/refuse", map[string]string{"code": code}); w.Code != http.StatusNoContent {
			t.Fatalf("refuse %q: status %d", code, w.Code)
		}
	}
	refuse("line_paused")
	if err := send("paused"); !errors.As(err, &refusal) || refusal.Code != "line_paused" {
		t.Fatalf("send while paused: %v", err)
	}
	if got := gateway(http.MethodPost, endpoint, "typing"); got != http.StatusOK {
		t.Fatalf("typing while paused: %d", got)
	}
	refuse("opted_out")
	if got := gateway(http.MethodDelete, endpoint, "typing"); got != http.StatusForbidden {
		t.Fatalf("typing while opted out: %d", got)
	}
	refuse("")
	if got := gateway(http.MethodPost, endpoint, "read"); got != http.StatusOK {
		t.Fatalf("read: %d", got)
	}
	if w := postJSON(t, h, "/api/debug/channels/"+chat+"/refuse", map[string]string{"code": "bogus"}); w.Code != http.StatusBadRequest {
		t.Fatalf("bogus refusal: status %d", w.Code)
	}

	// Only the stored endpoint's token opens the gateway, and the page
	// only reaches debug chats.
	if got := gateway(http.MethodPost, prefix+"forged", "read"); got != http.StatusNotFound {
		t.Fatalf("read with a forged token: %d", got)
	}
	if w := postJSON(t, h, "/api/debug/channels/chat-real/receive", map[string]string{"text": "hi"}); w.Code != http.StatusNotFound {
		t.Fatalf("receive on a non-debug chat: status %d", w.Code)
	}
	if w := postJSON(t, h, "/api/debug/channels/chat-real/send", map[string]string{"text": "hi"}); w.Code != http.StatusNotFound {
		t.Fatalf("send on a non-debug chat: status %d", w.Code)
	}

	// A chat refused before its first message lists with no events, not
	// null ones.
	if w := postJSON(t, h, "/api/debug/channels/debug-empty/refuse", map[string]string{"code": "opted_out"}); w.Code != http.StatusNoContent {
		t.Fatalf("refuse: status %d", w.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/debug/channels", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"events":[]`) {
		t.Fatalf("list: %s", w.Body.String())
	}
	var chats []debugChatView
	if err := json.Unmarshal(w.Body.Bytes(), &chats); err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(chats, func(a, b debugChatView) int { return len(b.Events) - len(a.Events) })
	if len(chats) != 2 || chats[0].ConversationID != conv.ConversationID {
		t.Fatalf("chats = %+v", chats)
	}
	var kinds []string
	for _, e := range chats[0].Events {
		kinds = append(kinds, e.Kind)
	}
	if got, want := strings.Join(kinds, " "), "in out out out out out refused refused in out refused refused read"; got != want {
		t.Fatalf("events = %s\nwant     %s", got, want)
	}
}

// A restarted Shelley forgets debug chats but not their conversations; the
// chat's new messages must not pass for retries of the old ones.
func TestDebugChannelIDsSurviveRestart(t *testing.T) {
	t.Parallel()
	server, database, h := channelTestServer(t, nil)
	const chat = "debug-restart"
	receive := func(text string) {
		t.Helper()
		if w := postJSON(t, h, "/api/debug/channels/"+chat+"/receive", map[string]string{"text": text}); w.Code != http.StatusOK {
			t.Fatalf("receive: status %d: %s", w.Code, w.Body.String())
		}
	}
	receive("before")
	conv, err := conversationByExternalID(t, database, chat)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), conv.ConversationID)
		return err == nil && !c.AgentWorking
	})
	d := &server.channelDebug
	d.mu.Lock()
	d.chats, d.seq, d.run = nil, 0, ""
	d.mu.Unlock()
	receive("after")
	waitFor(t, 5*time.Second, func() bool {
		return len(channelUserMessages(t, database, conv.ConversationID)) == 2
	})
}

// Messages queued in a channel conversation nothing is draining, as after a
// restart, are drained at startup and when the chat retries one of them.
func TestChannelQueuesDrainWithoutTheUI(t *testing.T) {
	t.Parallel()
	server, database, h := channelTestServer(t, nil)
	queue := func(chatID, id, text string) string {
		t.Helper()
		conv, err := database.CreateChannelConversation(t.Context(), chatID, "https://messages.int.exe.xyz", "predictable", db.ConversationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		llmJSON, err := json.Marshal(llm.UserStringMessage(text))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.AppendQueuedMessage(t.Context(), conv.ConversationID, db.QueuedMessage{
			ID: id, Llm: llmJSON, CreatedAt: time.Now().UTC(), Model: "predictable", ExternalMessageID: id,
		}); err != nil {
			t.Fatal(err)
		}
		return conv.ConversationID
	}
	drained := func(conversationID, want string) {
		t.Helper()
		waitFor(t, 5*time.Second, func() bool {
			c, err := database.GetConversationByID(t.Context(), conversationID)
			return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
		})
		if got := strings.Join(channelUserMessages(t, database, conversationID), "\n"); got != want {
			t.Fatalf("user messages = %q, want %q", got, want)
		}
	}

	startup := queue("chat-startup", "q1", "left queued")
	server.recoverChannelQueues(t.Context())
	drained(startup, "q1: left queued")

	retried := queue("chat-retry", "q2", "retried")
	if w := postJSON(t, h, "/api/channels/messages", channelEvent{Type: "message", ID: "q2", ChatID: "chat-retry", Text: "retried"}); w.Code != http.StatusNoContent {
		t.Fatalf("retry: status %d: %s", w.Code, w.Body.String())
	}
	drained(retried, "q2: retried")
}

func TestSendChannelMessageNeedsBoundConversation(t *testing.T) {
	t.Parallel()
	server, database, _ := channelTestServer(t, nil)
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.sendChannelMessage(t.Context(), conv.ConversationID, "hi", ""); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("send from unbound conversation: %v", err)
	}
}

// A chat's simultaneous first messages create one conversation and are all
// delivered.
func TestChannelConcurrentFirstMessages(t *testing.T) {
	t.Parallel()
	_, database, h := channelTestServer(t, nil)
	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		wg.Go(func() {
			ev := channelEvent{Type: "message", ID: fmt.Sprintf("m%d", i), ChatID: "chat-race", Text: fmt.Sprintf("hello %d", i)}
			codes[i] = postJSON(t, h, "/api/channels/messages", ev).Code
		})
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusNoContent {
			t.Fatalf("delivery %d: status %d", i, code)
		}
	}
	conv, err := conversationByExternalID(t, database, "chat-race")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), conv.ConversationID)
		return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
	})
	if got := channelUserMessages(t, database, conv.ConversationID); len(got) != n {
		t.Fatalf("user messages = %q", got)
	}
}

// A delivery waiting for its chat's lock gives up with its request, and the
// chat's lock goes away with its last user.
func TestLockChannelChat(t *testing.T) {
	t.Parallel()
	server, _, _ := channelTestServer(t, nil)
	unlock, err := server.lockChannelChat(t.Context(), "chat-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := server.lockChannelChat(ctx, "chat-1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting lock: %v", err)
	}
	unlock()
	server.mu.Lock()
	left := len(server.channelChatLocks)
	server.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d chat locks left", left)
	}
}

// During distillation, a message waits in the queue for the new generation.
func TestChannelMessageWaitsForDistillation(t *testing.T) {
	t.Parallel()
	server, database, h := channelTestServer(t, nil)
	deliver := func(id, text string) {
		t.Helper()
		if w := postJSON(t, h, "/api/channels/messages", channelEvent{Type: "message", ID: id, ChatID: "chat-1", Text: text}); w.Code != http.StatusNoContent {
			t.Fatalf("deliver %s: status %d: %s", id, w.Code, w.Body.String())
		}
	}
	idle := func(conversationID string) {
		t.Helper()
		waitFor(t, 5*time.Second, func() bool {
			c, err := database.GetConversationByID(t.Context(), conversationID)
			return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
		})
	}
	deliver("m1", "first")
	conv, err := conversationByExternalID(t, database, "chat-1")
	if err != nil {
		t.Fatal(err)
	}
	idle(conv.ConversationID)
	manager, err := server.getOrCreateConversationManager(t.Context(), conv.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !manager.BeginDistillingSetup() {
		t.Fatal("distillation already running")
	}
	manager.FinishDistillingSetup()
	deliver("m2", "second")
	queued, err := database.GetQueuedMessages(t.Context(), conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].ExternalMessageID != "m2" {
		t.Fatalf("queue during distillation = %+v", queued)
	}
	manager.SetDistilling(false)
	manager.drainQueueIfIdle(server)
	idle(conv.ConversationID)
	if got := strings.Join(channelUserMessages(t, database, conv.ConversationID), "\n"); got != "m1: first\nm2: second" {
		t.Fatalf("user messages:\n%s", got)
	}
}

// unavailableLLM serves no model.
type unavailableLLM struct{ testLLMManager }

func (*unavailableLLM) GetService(string) (llm.Service, error) {
	return nil, errors.New("model unavailable")
}

// A chat whose first message cannot reach a model gets no conversation, so
// the failed deliveries leave nothing behind.
func TestChannelMessageWithoutModelCreatesNoConversation(t *testing.T) {
	t.Parallel()
	server, database, h := channelTestServer(t, nil)
	server.llmManager = &unavailableLLM{}
	if w := postJSON(t, h, "/api/channels/messages", channelEvent{Type: "message", ID: "m1", ChatID: "chat-1", Text: "hi"}); w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", w.Code)
	}
	if _, err := conversationByExternalID(t, database, "chat-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("conversation lookup: %v", err)
	}
}

// A chat's conversation answers with message_user: its messages and
// reactions reach the chat, and what the chat cannot take is an error the
// agent sees.
func TestChannelConversationRepliesWithMessageUser(t *testing.T) {
	t.Parallel()
	server, database, h, _ := debugChannelTestServer(t)
	const chat = "debug-reply"
	var conversationID string
	receive := func(text string) {
		t.Helper()
		if w := postJSON(t, h, "/api/debug/channels/"+chat+"/receive", map[string]string{"text": text}); w.Code != http.StatusOK {
			t.Fatalf("receive: status %d: %s", w.Code, w.Body.String())
		}
		if conversationID == "" {
			conv, err := conversationByExternalID(t, database, chat)
			if err != nil {
				t.Fatal(err)
			}
			conversationID = conv.ConversationID
		}
		waitFor(t, 5*time.Second, func() bool {
			c, err := database.GetConversationByID(t.Context(), conversationID)
			return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
		})
	}
	call := func(in claudetool.MessageUserInput) {
		t.Helper()
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		receive("message_user: " + string(b))
	}
	events := func() []debugChatEvent {
		d := &server.channelDebug
		d.mu.Lock()
		defer d.mu.Unlock()
		return slices.Clone(d.chats[chat].Events)
	}
	lastResult := func() llm.Content {
		t.Helper()
		c := &compactTestConversation{t: t, database: database, id: conversationID}
		_, result := c.lastMessageUserResult()
		return result
	}

	// Channel conversations have message_user on.
	call(claudetool.MessageUserInput{Text: "Hello from Shelley"})
	evs := events()
	if last := evs[len(evs)-1]; last.Kind != "out" || last.Text != "Hello from Shelley" {
		t.Fatalf("last event = %+v", last)
	}
	if evs[len(evs)-1].ReplyTo != "" {
		t.Fatalf("unthreaded send has reply target: %+v", evs[len(evs)-1])
	}
	firstIn := evs[0].MessageID
	typed, err := database.ListTypedUserMessages(t.Context(), conversationID)
	if err != nil || len(typed) != 1 {
		t.Fatalf("typed user messages = %d: %v", len(typed), err)
	}
	firstSeq := typed[0].SequenceID

	// A reply targets the chat's message by its id; a reaction lands there
	// as a tapback if it is one.
	call(claudetool.MessageUserInput{Text: "Sure", ReplyTo: firstSeq, Reaction: "👍"})
	evs = events()
	if got := evs[len(evs)-2:]; got[0].Kind != "out" || got[0].Text != "Sure" || got[0].ReplyTo != firstIn || got[1].Kind != "react" || got[1].MessageID != firstIn || got[1].Text != "like" {
		t.Fatalf("events = %+v", got)
	}
	for emoji, want := range map[string]string{"🎉": "🎉", "‼️": "emphasize", "❤️": "love"} {
		call(claudetool.MessageUserInput{ReplyTo: firstSeq, Reaction: emoji})
		if evs = events(); evs[len(evs)-1].Text != want {
			t.Fatalf("reaction %s = %+v, want %s", emoji, evs[len(evs)-1], want)
		}
	}
	conv, err := database.GetConversationByID(t.Context(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(*conv.ExternalEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if w := postJSON(t, h, endpoint.Path+"/react", map[string]string{"message_id": firstIn, "reaction": "like"}); w.Code != http.StatusOK {
		t.Fatalf("debug gateway message_id reaction: %d %s", w.Code, w.Body.String())
	}
	if evs = events(); evs[len(evs)-1].MessageID != firstIn {
		t.Fatalf("debug gateway reaction target: %+v", evs[len(evs)-1])
	}
	if w := postJSON(t, h, endpoint.Path+"/react", map[string]string{"reply_to": firstIn, "reaction": "like"}); w.Code != http.StatusBadRequest {
		t.Fatalf("reaction reply_to target: status %d, want 400", w.Code)
	}

	// The chat takes no attachments, and says so before sending anything.
	file := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(events())
	call(claudetool.MessageUserInput{Text: "see attached", Attachments: []string{file}})
	if r := lastResult(); !r.ToolError || !strings.Contains(r.ToolResult[0].Text, "attachments") {
		t.Fatalf("attachment result = %+v", r)
	}

	// A refusal reaches the agent.
	if w := postJSON(t, h, "/api/debug/channels/"+chat+"/refuse", map[string]string{"code": "line_paused"}); w.Code != http.StatusNoContent {
		t.Fatalf("refuse: status %d", w.Code)
	}
	call(claudetool.MessageUserInput{Text: "anyone there?"})
	if r := lastResult(); !r.ToolError || !strings.Contains(r.ToolResult[0].Text, "line_paused") || !messageUserDisplay(t, r).ChatFailed {
		t.Fatalf("refused result = %+v", r)
	}
	for _, e := range events()[before:] {
		if e.Kind == "out" {
			t.Fatalf("delivered despite refusal: %+v", e)
		}
	}
}

// The agent learns whether a failed send surely did not reach the chat: an
// endpoint's refusal is certain, but a call cut off after the endpoint got
// it may have delivered, so resending could duplicate.
func TestChannelChatSaysWhetherDeliveryFailed(t *testing.T) {
	t.Parallel()
	server, database, _ := channelTestServer(t, nil)
	got := make(chan string, 1)
	release := make(chan struct{})
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/refuse/send":
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":"line_paused","message":"paused"}`)
		case "/upstream/send":
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, `{"error":"upstream_error","message":"messaging provider unreachable"}`)
		case "/upstream-refused/send":
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"error":"upstream_error","message":"{\"code\":2001}"}`)
		case "/internal/send":
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":"internal_error","message":"internal error"}`)
		case "/hang/send":
			// Got it, but the answer is lost.
			got <- r.URL.Path
			<-release
		default:
			t.Errorf("unexpected call %s", r.URL.Path)
		}
	}))
	t.Cleanup(endpoint.Close)
	t.Cleanup(func() { close(release) })
	chat := func(name string) channelChat {
		conv, err := database.CreateChannelConversation(t.Context(), "chat-"+name, endpoint.URL+"/"+name, "predictable", db.ConversationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return channelChat{s: server, conversationID: conv.ConversationID}
	}

	refuse := chat("refuse")
	if err := refuse.Send(t.Context(), "hi", claudetool.UserMessage{}); err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "line_paused") {
		t.Fatalf("refused send: %v", err)
	}
	for name, want := range map[string]string{"upstream": "may have been delivered", "internal": "may have been delivered", "upstream-refused": "refused"} {
		if err := chat(name).Send(t.Context(), "hi", claudetool.UserMessage{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v, want %q", name, err, want)
		}
	}
	// A reaction to a message typed in Shelley's UI calls nothing.
	if err := refuse.React(t.Context(), claudetool.UserMessage{ID: "typed"}, "👍"); err != nil {
		t.Fatalf("reaction to a UI message: %v", err)
	}
	unbound, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := (channelChat{s: server, conversationID: unbound.ConversationID}).Send(t.Context(), "hi", claudetool.UserMessage{}); err == nil || !strings.Contains(err.Error(), "nothing was sent") {
		t.Fatalf("send from unbound conversation: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-got
		cancel()
	}()
	if err := chat("hang").Send(ctx, "hi", claudetool.UserMessage{}); err == nil || !strings.Contains(err.Error(), "may have been delivered") {
		t.Fatalf("cancelled send: %v", err)
	}
}

// A web chat's conversation keeps message_user's messages, and the web page
// reads them from the chat's stream.
func TestWebChatRoundTrip(t *testing.T) {
	t.Parallel()
	_, database, h := channelTestServer(t, nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	const chat = "web-usr123"

	resp, err := http.Get(ts.URL + "/api/channels/messages/" + chat + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stream before the first message: status %d, want 204", resp.StatusCode)
	}

	if w := postJSON(t, h, "/api/channels/messages", channelEvent{Type: "message", ID: "m1", ChatID: chat, Web: true, Text: `message_user: {"text":"hello from the VM"}`}); w.Code != http.StatusNoContent {
		t.Fatalf("deliver: status %d: %s", w.Code, w.Body.String())
	}
	conv, err := conversationByExternalID(t, database, chat)
	if err != nil {
		t.Fatal(err)
	}
	if conv.ExternalEndpoint != nil {
		t.Fatalf("external_endpoint = %q, want none", *conv.ExternalEndpoint)
	}
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), conv.ConversationID)
		return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
	})
	c := &compactTestConversation{t: t, database: database, id: conv.ConversationID}
	if _, r := c.lastMessageUserResult(); r.ToolError {
		t.Fatalf("message_user result = %+v", r)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/channels/messages/"+chat+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var sr StreamResponse
		if err := json.Unmarshal([]byte(data), &sr); err != nil {
			t.Fatal(err)
		}
		if sr.ConversationID != conv.ConversationID && len(sr.Messages) > 0 {
			t.Fatalf("stream of conversation %s, want %s", sr.ConversationID, conv.ConversationID)
		}
		for _, m := range sr.Messages {
			if m.LlmData != nil && strings.Contains(*m.LlmData, "hello from the VM") && m.Type == string(db.MessageTypeAgent) {
				return
			}
		}
	}
	t.Fatalf("stream ended without the reply: %v", scanner.Err())
}

// turnRecordingService is the predictable model, recording the requests of
// agent turns (which offer tools, unlike slug generation).
type turnRecordingService struct {
	llm.Service
	mu    sync.Mutex
	turns []*llm.Request
}

func (s *turnRecordingService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	if len(request.Tools) > 0 {
		s.mu.Lock()
		s.turns = append(s.turns, request)
		s.mu.Unlock()
	}
	return s.Service.Do(ctx, request)
}

// The message that starts a chat's conversation configures it: its system
// prompt replaces Shelley's, and its first reply answers it in place of the
// agent. A later message's are ignored. The user follows the conversation
// in the chat, so it sends no notifications.
func TestChannelFirstMessageConfiguresConversation(t *testing.T) {
	t.Parallel()
	server, database, h, _ := debugChannelTestServer(t)
	model := &turnRecordingService{Service: predictable.NewService()}
	server.llmManager = &testLLMManager{service: model}
	const chat = "debug-first"
	const welcome = "You made it! I’m Shelley."
	receive := func(body map[string]string) string {
		t.Helper()
		w := postJSON(t, h, "/api/debug/channels/"+chat+"/receive", body)
		if w.Code != http.StatusOK {
			t.Fatalf("receive: status %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.MessageID
	}
	hey := map[string]string{"text": "Hey", "system_prompt": "You are texting.", "first_reaction": "❤️", "first_reply": welcome}

	heyID := receive(hey)
	server.channelDebug.mu.Lock()
	evs := slices.Clone(server.channelDebug.chats[chat].Events)
	server.channelDebug.mu.Unlock()
	if len(evs) != 3 || evs[1].Kind != "react" || evs[1].Text != "love" || evs[1].MessageID != heyID || evs[2].Kind != "out" || evs[2].Text != welcome || evs[2].ReplyTo != "" {
		t.Fatalf("chat events = %+v", evs)
	}
	conv, err := conversationByExternalID(t, database, chat)
	if err != nil {
		t.Fatal(err)
	}
	if conv.AgentWorking {
		t.Fatal("agent working after the first reply")
	}
	if opts := db.ParseConversationOptions(conv.ConversationOptions); opts.SystemPrompt != "You are texting." {
		t.Fatalf("options = %+v", opts)
	}
	notified := &recordingChannel{}
	server.RegisterNotificationChannel(notified)
	server.publishConversationState(ConversationState{ConversationID: conv.ConversationID, Model: "predictable"})
	if notified.count() != 0 {
		t.Fatal("channel conversation notified")
	}

	hey["system_prompt"] = "Ignored."
	receive(hey)
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), conv.ConversationID)
		return err == nil && !c.AgentWorking
	})
	model.mu.Lock()
	turns := slices.Clone(model.turns)
	model.mu.Unlock()
	if len(turns) != 1 {
		t.Fatalf("agent turns = %d, want 1: the second message's", len(turns))
	}
	req := turns[0]
	if len(req.System) == 0 || req.System[0].Text != "You are texting." {
		t.Fatalf("system = %+v", req.System)
	}
	// The agent sees the first reply as its own message_user call.
	var history []string
	for _, m := range req.Messages {
		role := strings.ToLower(strings.TrimPrefix(m.Role.String(), "MessageRole"))
		for _, c := range m.Content {
			switch c.Type {
			case llm.ContentTypeText:
				history = append(history, role+" "+c.Text)
			case llm.ContentTypeToolUse:
				history = append(history, role+" "+c.ToolName+" "+string(c.ToolInput))
			case llm.ContentTypeToolResult:
				history = append(history, fmt.Sprintf("%s result error=%v %s", role, c.ToolError, c.ToolResult[0].Text))
			}
		}
	}
	// The second message's sequence_id depends on when the slug lands.
	if n := len(history); n > 0 {
		history[n-1] = regexp.MustCompile(`sequence_id="\d+"`).ReplaceAllString(history[n-1], `sequence_id="N"`)
	}
	want := []string{
		"user <external_message sequence_id=\"2\">\nHey\n</external_message>",
		`assistant message_user {"reply_to":2,"reaction":"❤️"}`,
		`user result error=false Reacted ❤️ to the user's message "Hey".`,
		`assistant message_user {"text":"You made it! I’m Shelley.","end_turn":true}`,
		"user result error=false Sent.",
		"user <external_message sequence_id=\"N\">\nHey\n</external_message>",
	}
	if got := strings.Join(history, "\n"); got != strings.Join(want, "\n") {
		t.Fatalf("history:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}

	// A new generation starts the context afresh, not the conversation.
	if _, err := server.startNewGeneration(t.Context(), conv.ConversationID); err != nil {
		t.Fatal(err)
	}
	receive(hey)
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), conv.ConversationID)
		return err == nil && !c.AgentWorking
	})
	model.mu.Lock()
	turns = slices.Clone(model.turns)
	model.mu.Unlock()
	if len(turns) != 2 || turns[1].System[0].Text != "You are texting." {
		t.Fatalf("agent turns = %d, want 2 under the conversation's system prompt", len(turns))
	}
}

// A first reply goes out once, even if the delivery's caller gives up on
// it: the message is recorded, so a retry would not bring it back.
func TestChannelFirstReplyOutlivesTheDelivery(t *testing.T) {
	t.Parallel()
	server, database, _ := channelTestServer(t, nil)
	got := make(chan struct{})
	release := make(chan struct{})
	var sends atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sends.Add(1) == 1 {
			close(got)
			<-release
		}
		fmt.Fprint(w, `{"message_id":"out-1"}`)
	}))
	t.Cleanup(endpoint.Close)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-got
		cancel()
		close(release)
	}()
	ev := channelEvent{Type: "message", ID: "in-1", ChatID: "chat-gone", Text: "Hey", FirstReply: "Welcome"}
	if err := server.receiveChannelEvent(ctx, endpoint.URL, ev); err != nil {
		t.Fatal(err)
	}
	conv, err := conversationByExternalID(t, database, "chat-gone")
	if err != nil {
		t.Fatal(err)
	}
	c := &compactTestConversation{t: t, database: database, id: conv.ConversationID}
	if _, r := c.lastMessageUserResult(); r.ToolError {
		t.Fatalf("result = %+v", r)
	}
	if err := server.receiveChannelEvent(t.Context(), endpoint.URL, ev); err != nil {
		t.Fatal(err)
	}
	if n := sends.Load(); n != 1 {
		t.Fatalf("sends = %d after a retry, want 1", n)
	}
}

// A first reply the chat refuses is recorded as the agent's failed call,
// and the turn ends: no agent is there to try again.
func TestChannelFirstReplyRefused(t *testing.T) {
	t.Parallel()
	_, database, h, _ := debugChannelTestServer(t)
	const chat = "debug-refused"
	if w := postJSON(t, h, "/api/debug/channels/"+chat+"/refuse", map[string]string{"code": "line_paused"}); w.Code != http.StatusNoContent {
		t.Fatalf("refuse: status %d", w.Code)
	}
	if w := postJSON(t, h, "/api/debug/channels/"+chat+"/receive", map[string]string{"text": "Hey", "first_reply": "Welcome"}); w.Code != http.StatusOK {
		t.Fatalf("receive: status %d: %s", w.Code, w.Body.String())
	}
	conv, err := conversationByExternalID(t, database, chat)
	if err != nil {
		t.Fatal(err)
	}
	if conv.AgentWorking {
		t.Fatal("agent working after the first reply")
	}
	c := &compactTestConversation{t: t, database: database, id: conv.ConversationID}
	if _, r := c.lastMessageUserResult(); !r.ToolError || !strings.Contains(r.ToolResult[0].Text, "line_paused") || !messageUserDisplay(t, r).ChatFailed {
		t.Fatalf("result = %+v", r)
	}
}

// A web chat takes a first reply too, kept in the conversation, and like
// any chat it notifies no one.
func TestWebChatFirstReply(t *testing.T) {
	t.Parallel()
	server, database, _ := channelTestServer(t, nil)
	ev := channelEvent{Type: "message", ID: "m1", ChatID: "web-first", Web: true, Text: "Hey", FirstReaction: "❤️", FirstReply: "Welcome"}
	if err := server.receiveChannelEvent(t.Context(), "", ev); err != nil {
		t.Fatal(err)
	}
	conv, err := conversationByExternalID(t, database, ev.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	c := &compactTestConversation{t: t, database: database, id: conv.ConversationID}
	if _, r := c.lastMessageUserResult(); r.ToolError || r.ToolResult[0].Text != "Sent." {
		t.Fatalf("result = %+v", r)
	}
	notified := &recordingChannel{}
	server.RegisterNotificationChannel(notified)
	server.publishConversationState(ConversationState{ConversationID: conv.ConversationID, Model: "predictable"})
	if notified.count() != 0 {
		t.Fatal("web chat notified")
	}
}
