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
	"reflect"
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

// chatAssistant returns the conversation chat's latest message went to,
// failing unless it is the Assistant.
func chatAssistant(t *testing.T, s *Server, chatID string) string {
	t.Helper()
	route, err := s.db.LatestChannelRoute(t.Context(), chatID)
	if err != nil {
		t.Fatalf("chat %s route: %v", chatID, err)
	}
	if id := s.assistantID(); id == "" || route.ConversationID != id {
		t.Fatalf("chat %s went to %s, not the Assistant %q", chatID, route.ConversationID, id)
	}
	return route.ConversationID
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

// The Assistant a chat's first message makes uses the Assistant profile,
// not the user's default (unless it was deleted), except that message_user
// must stay on to answer the chat. It takes the profile's edits, but not
// its deletion.
func TestChannelMessageAssistantProfile(t *testing.T) {
	for _, web := range []bool{false, true} {
		for _, tc := range []struct {
			name      string
			assistant bool
		}{
			{name: "Assistant", assistant: true},
			{name: "deleted Assistant"},
		} {
			t.Run(fmt.Sprintf("web=%t/%s", web, tc.name), func(t *testing.T) {
				t.Parallel()
				e := newProfileEnv(t)
				t.Cleanup(func() { stopActiveConversationLoops(e.srv) })
				e.want(201, "POST", "/api/profiles", `{"name":"Other","model":"model-b","thinking_level":"low","tool_overrides":{"message_user":"off","bash":"off"},"compact_nudge_tokens":54321,"system_prompt":"Unrelated default profile."}`)
				e.want(200, "POST", "/api/profiles/Other/default", "")
				if tc.assistant {
					e.want(200, "PUT", "/api/profiles/Assistant", `{"model":"model-b","thinking_level":"high","tool_overrides":{"message_user":"off","bash":"off","compact_in_place":"on"},"compact_nudge_tokens":12345,"system_prompt":"Channel assistant in {{.WorkingDirectory}}."}`)
				} else {
					e.want(204, "DELETE", "/api/profiles/Assistant", "")
				}

				// Without the Assistant profile, the Assistant starts from
				// the default one.
				want := ConversationSettings{Profile: "Other", Settings: Settings{
					Model: "model-b", ThinkingLevel: "low",
					ToolOverrides:      map[string]string{"message_user": "on", "bash": "off"},
					CompactNudgeTokens: 54321,
					SystemPrompt:       "Unrelated default profile.",
				}}
				promptText := "Unrelated default profile."
				thinking := llm.ThinkingLevelLow
				if tc.assistant {
					want = ConversationSettings{Profile: "Assistant", Settings: Settings{
						Model: "model-b", ThinkingLevel: "high",
						ToolOverrides:      map[string]string{"message_user": "on", "bash": "off", "compact_in_place": "on"},
						CompactNudgeTokens: 12345,
						SystemPrompt:       "Channel assistant in {{.WorkingDirectory}}.",
					}}
					promptText, thinking = "Channel assistant in ", llm.ThinkingLevelHigh
				}
				tools := []string{"compact_in_place"}
				notTools := []string{"bash"}
				send := func(id string) {
					t.Helper()
					body, err := json.Marshal(channelEvent{Type: "message", ID: id, ChatID: "profile-chat", Web: web, Text: "hello"})
					if err != nil {
						t.Fatal(err)
					}
					e.want(204, "POST", "/api/channels/messages", string(body))
				}
				send("first")
				assistantID := chatAssistant(t, e.srv, "profile-chat")
				checkSettings := func() {
					t.Helper()
					stored, err := e.database.GetConversationByID(t.Context(), assistantID)
					if err != nil {
						t.Fatal(err)
					}
					if got := conversationSettings(*stored); !reflect.DeepEqual(got, want) {
						t.Errorf("persisted settings = %+v, want %+v", got, want)
					}
				}
				checkRequest := func(before int) {
					t.Helper()
					e.waitTurn(assistantID, before)
					req := e.lastTurnRequest()
					if !hasTool(req, claudetool.MessageUserName) {
						t.Error("channel request must offer message_user even when the profile disables it")
					}
					for _, name := range tools {
						if tc.assistant && !hasTool(req, name) {
							t.Errorf("channel request lacks Assistant's %s", name)
						}
					}
					for _, name := range notTools {
						if tc.assistant && hasTool(req, name) {
							t.Errorf("channel request has %s, which Assistant turns off", name)
						}
					}
					if req.ThinkingLevel != thinking {
						t.Errorf("request thinking = %v, want %v", req.ThinkingLevel, thinking)
					}
					if got := systemText(req); !strings.Contains(got, promptText) || strings.Contains(got, "{{.WorkingDirectory}}") {
						t.Errorf("request prompt starts %.100q, want rendered %q", got, promptText)
					}
				}
				checkSettings()
				checkRequest(0)
				if !tc.assistant {
					return
				}
				// Editing the profile changes the Assistant; deleting it
				// does not.
				for _, mutation := range []struct {
					method string
					status int
					body   string
				}{
					{"PUT", 200, `{"model":"model-a","thinking_level":"low","tool_overrides":{"bash":"on"},"compact_nudge_tokens":67890,"system_prompt":"Edited Assistant."}`},
					{"DELETE", 204, ""},
				} {
					e.want(mutation.status, mutation.method, "/api/profiles/Assistant", mutation.body)
					if mutation.method == "PUT" {
						want.Settings = Settings{
							Model: "model-a", ThinkingLevel: "low",
							ToolOverrides:      map[string]string{"message_user": "on", "bash": "on"},
							CompactNudgeTokens: 67890, SystemPrompt: "Edited Assistant.",
						}
						promptText, thinking = "Edited Assistant.", llm.ThinkingLevelLow
						tools, notTools = []string{"bash"}, nil
					}
					before := len(listMessages(t, e.database, assistantID))
					send(mutation.method)
					checkSettings()
					checkRequest(before)
				}
			})
		}
	}
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
	assistantID := chatAssistant(t, server, chat)
	typed, err := database.ListTypedUserMessages(t.Context(), assistantID)
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
	if got := channelUserMessages(t, database, assistantID); !slices.Equal(got, []string{id + ": " + text}) {
		t.Fatalf("stored channel message = %q, want original text", got)
	}

	manager, err := server.getOrCreateConversationManager(t.Context(), assistantID, "")
	if err != nil {
		t.Fatal(err)
	}
	var rows []generated.Message
	if err := database.Queries(t.Context(), func(q *generated.Queries) (err error) {
		rows, err = q.ListMessagesForContext(t.Context(), assistantID)
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
			server, database, _ := channelTestServer(t, gate)
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			releaseTool := func() { once.Do(func() { close(release) }) }
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/typing" {
					fmt.Fprint(w, `{"ok":true}`)
					return
				}
				if r.URL.Path != "/send" {
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
				close(started)
				<-release
				fmt.Fprint(w, `{"message_id":"reply-1"}`)
			}))
			t.Cleanup(endpoint.Close)
			t.Cleanup(releaseTool)
			deliver := func(id, text string) {
				t.Helper()
				if err := server.receiveChannelEvent(t.Context(), endpoint.URL, channelEvent{Type: "message", ID: id, ChatID: "chat-tool", Text: text}); err != nil {
					t.Fatalf("deliver %s: %v", id, err)
				}
			}
			prompt := `message_user: {"text":"working"}`
			deliver("m1", prompt)

			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("tool did not start")
			}
			assistantID := chatAssistant(t, server, "chat-tool")
			manager, err := server.getOrCreateConversationManager(t.Context(), assistantID, "")
			if err != nil {
				t.Fatal(err)
			}
			if queuedUI {
				if err := manager.QueueMessage(t.Context(), "predictable", llm.UserStringMessage("from the UI")); err != nil {
					t.Fatal(err)
				}
			}
			deliver("m2", "second")
			deliver("m3", "third")
			deliver("m2", "second")
			queued, err := database.GetQueuedMessages(t.Context(), assistantID)
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
			if got := channelUserMessages(t, database, assistantID); len(got) != 1 {
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
			for _, m := range listMessages(t, database, assistantID) {
				if m.Type == string(db.MessageTypeAgent) && storedLLMMessage(t, &m).EndOfTurn {
					t.Fatal("webhook message waited until the turn ended")
				}
			}
			want := []string{"m1: " + prompt, "m2: second", "m3: third"}
			if got := channelUserMessages(t, database, assistantID); !slices.Equal(got, want) {
				t.Fatalf("user messages = %v, want %v", got, want)
			}
			gate.release("third")
			if queuedUI {
				gate.waitStarted(t, "from the UI")
				gate.release("from the UI")
			}
			waitFor(t, 5*time.Second, func() bool {
				c, err := database.GetConversationByID(t.Context(), assistantID)
				return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
			})
		})
	}
}

// Every chat's messages go to the Assistant, made by the first.
func TestChannelMessagesDeliverToAssistant(t *testing.T) {
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
	assistantID := chatAssistant(t, server, "chat-1")
	if route, err := database.GetChannelRoute(t.Context(), assistantID, "m1"); err != nil || route.ChatID != "chat-1" || route.Endpoint != "https://messages.int.exe.xyz" {
		t.Fatalf("route = %+v, %v, want chat-1's at the messages integration", route, err)
	}
	gate.waitStarted(t, "first")

	// While the agent works, a message waits for the next model request;
	// retries of both recorded and queued messages are dropped.
	deliver(channelEvent{Type: "message", ID: "m2", ChatID: "chat-1", Text: "second"})
	deliver(channelEvent{Type: "message", ID: "m1", ChatID: "chat-1", Text: "first"})
	deliver(channelEvent{Type: "message", ID: "m2", ChatID: "chat-1", Text: "second"})
	if got := strings.Join(channelUserMessages(t, database, assistantID), "\n"); got != "m1: first" {
		t.Fatalf("user messages mid-turn:\n%s", got)
	}

	// Channel messages are injectable even behind a queued UI message.
	manager, err := server.getOrCreateConversationManager(t.Context(), assistantID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.QueueMessage(t.Context(), "predictable", llm.UserStringMessage("from the UI")); err != nil {
		t.Fatal(err)
	}
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "queued image")
	}))
	t.Cleanup(media.Close)
	photo := channelEvent{
		Type: "message", ID: "m3", ChatID: "chat-1", Text: "third",
		Attachments: []channelAttachment{{URL: media.URL + "/queued.png"}},
	}
	deliver(photo)
	// Queued retries must also deduplicate before fetching provider media.
	media.Close()
	deliver(photo)
	queued, err := database.GetQueuedMessages(t.Context(), assistantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 3 || queued[0].ExternalMessageID != "m2" || !queued[0].Inject ||
		queued[1].Inject || queued[2].ExternalMessageID != "m3" || !queued[2].Inject {
		t.Fatalf("queue = %+v, want injectable m2, the UI message, and injectable m3", queued)
	}
	var queuedPhoto llm.Message
	if err := json.Unmarshal(queued[2].Llm, &queuedPhoto); err != nil {
		t.Fatal(err)
	}
	photoText := messageText(queuedPhoto)
	if !strings.HasPrefix(photoText, "third\n\n[") || !strings.HasSuffix(photoText, "]") {
		t.Fatalf("queued attachment lost: %q", photoText)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(photoText, "third\n\n["), "]")
	t.Cleanup(func() { os.Remove(path) })
	if content, err := os.ReadFile(path); err != nil || string(content) != "queued image" {
		t.Fatalf("queued image = %q, %v", content, err)
	}

	gate.release("first")
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), assistantID)
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
		c, err := database.GetConversationByID(t.Context(), assistantID)
		return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
	})

	got := strings.Join(channelUserMessages(t, database, assistantID), "\n")
	if want := "m1: first\nm2: second\n: from the UI\nm3: " + photoText + "\nm4: fourth"; got != want {
		t.Fatalf("user messages:\n%s\nwant:\n%s", got, want)
	}
	var sawQueued, sawUI bool
	for _, req := range gate.Service.(*predictable.Service).GetRecentRequests() {
		for _, msg := range req.Messages {
			if msg.Role != llm.MessageRoleUser {
				continue
			}
			sawQueued = sawQueued || strings.HasPrefix(messageText(msg), "<external_message sequence_id=\"") &&
				strings.HasSuffix(messageText(msg), "\n"+photoText+"\n</external_message>")
			sawUI = sawUI || strings.HasPrefix(messageText(msg), "<user_message sequence_id=\"") &&
				strings.HasSuffix(messageText(msg), "\nfrom the UI\n</user_message>")
		}
	}
	if !sawQueued || !sawUI {
		t.Fatalf("queued channel and UI messages in LLM requests: channel=%v UI=%v", sawQueued, sawUI)
	}

	// Another chat's messages go to the Assistant too.
	deliver(channelEvent{Type: "message", ID: "m7", ChatID: "chat-2", Text: "other chat"})
	chatAssistant(t, server, "chat-2")
	waitFor(t, 5*time.Second, func() bool {
		got := channelUserMessages(t, database, assistantID)
		return got[len(got)-1] == "m7: other chat"
	})
}

func TestChannelMessageRejectsBadEvents(t *testing.T) {
	t.Parallel()
	_, _, h := channelTestServer(t, nil)
	if w := postJSON(t, h, "/api/channels/messages", channelEvent{Type: "message", ID: "m1", Text: "hi"}); w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
	// Replies to a message without an id could not be routed back.
	if w := postJSON(t, h, "/api/channels/messages", channelEvent{Type: "message", ChatID: "chat-1", Text: "hi"}); w.Code != http.StatusBadRequest {
		t.Fatalf("no id: status %d, want 400", w.Code)
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
// lands in the Assistant, and replies reach the emulated gateway, which
// refuses them like exed's.
func TestDebugChannelRoundTrip(t *testing.T) {
	t.Parallel()
	server, database, h, ts := debugChannelTestServer(t)
	port := strconv.Itoa(server.listenPort)

	const chat = "debug-abc"
	if w := postJSON(t, h, "/api/debug/channels/"+chat+"/receive", map[string]string{"text": "hello"}); w.Code != http.StatusOK {
		t.Fatalf("receive: status %d: %s", w.Code, w.Body.String())
	}
	assistantID := chatAssistant(t, server, chat)
	route, err := database.LatestChannelRoute(t.Context(), chat)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "http://localhost:" + port + "/debug/channels/gateway/" + chat + "/"
	if !strings.HasPrefix(route.Endpoint, prefix) {
		t.Fatalf("endpoint = %q, want %s<token>", route.Endpoint, prefix)
	}
	endpoint := route.Endpoint
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), assistantID)
		return err == nil && !c.AgentWorking
	})
	if got := channelUserMessages(t, database, assistantID); len(got) != 1 || !strings.HasPrefix(got[0], "debug-in-") || !strings.HasSuffix(got[0], "-1: hello") {
		t.Fatalf("user messages = %q", got)
	}

	send := func(text string) error {
		_, err := server.sendChannelEndpointMessage(t.Context(), endpoint, text, "", nil)
		return err
	}
	id, err := server.sendChannelEndpointMessage(t.Context(), endpoint, "hi back", "", nil)
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
	if len(chats) != 2 || chats[0].ConversationID != assistantID {
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

// A restarted Shelley forgets debug chats but not the Assistant; the chat's
// new messages must not pass for retries of the old ones.
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
	assistantID := chatAssistant(t, server, chat)
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), assistantID)
		return err == nil && !c.AgentWorking
	})
	d := &server.channelDebug
	d.mu.Lock()
	d.chats, d.seq, d.run = nil, 0, ""
	d.mu.Unlock()
	receive("after")
	waitFor(t, 5*time.Second, func() bool {
		return len(channelUserMessages(t, database, assistantID)) == 2
	})
}

// Texted messages queued in the Assistant with nothing draining them, as
// after a restart, are drained at startup and when the chat retries one of
// them.
func TestChannelQueuesDrainWithoutTheUI(t *testing.T) {
	t.Parallel()
	server, database, h := channelTestServer(t, nil)
	assistantID, err := server.ensureAssistant(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	queue := func(id, text string) {
		t.Helper()
		llmJSON, err := json.Marshal(llm.UserStringMessage(text))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.AppendQueuedMessage(t.Context(), assistantID, db.QueuedMessage{
			ID: id, Llm: llmJSON, CreatedAt: time.Now().UTC(), Model: "predictable", ExternalMessageID: id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	drained := func(want string) {
		t.Helper()
		waitFor(t, 5*time.Second, func() bool {
			c, err := database.GetConversationByID(t.Context(), assistantID)
			return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
		})
		if got := strings.Join(channelUserMessages(t, database, assistantID), "\n"); got != want {
			t.Fatalf("user messages = %q, want %q", got, want)
		}
	}

	queue("q1", "left queued")
	server.recoverChannelQueues(t.Context())
	drained("q1: left queued")

	queue("q2", "retried")
	if w := postJSON(t, h, "/api/channels/messages", channelEvent{Type: "message", ID: "q2", ChatID: "chat-retry", Text: "retried"}); w.Code != http.StatusNoContent {
		t.Fatalf("retry: status %d: %s", w.Code, w.Body.String())
	}
	drained("q1: left queued\nq2: retried")
}

// Simultaneous first messages, from one chat or several, make one
// Assistant and are all delivered to it.
func TestChannelConcurrentFirstMessages(t *testing.T) {
	t.Parallel()
	server, database, h := channelTestServer(t, nil)
	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		wg.Go(func() {
			ev := channelEvent{Type: "message", ID: fmt.Sprintf("m%d", i), ChatID: fmt.Sprintf("chat-race-%d", i%3), Text: fmt.Sprintf("hello %d", i)}
			codes[i] = postJSON(t, h, "/api/channels/messages", ev).Code
		})
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusNoContent {
			t.Fatalf("delivery %d: status %d", i, code)
		}
	}
	assistantID := server.assistantID()
	for i := range n {
		if route, err := database.GetChannelRoute(t.Context(), assistantID, fmt.Sprintf("m%d", i)); err != nil || route.ChatID != fmt.Sprintf("chat-race-%d", i%3) {
			t.Fatalf("m%d route in the Assistant = %+v, %v", i, route, err)
		}
	}
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), assistantID)
		return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
	})
	if got := channelUserMessages(t, database, assistantID); len(got) != n {
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

// unavailableLLM serves no model.
type unavailableLLM struct{ testLLMManager }

func (*unavailableLLM) GetService(string) (llm.Service, error) {
	return nil, errors.New("model unavailable")
}

// A message that cannot reach a model fails its delivery and leaves no
// trace of it, so that exed's retry delivers it.
func TestChannelMessageWithoutModelIsRetried(t *testing.T) {
	t.Parallel()
	server, database, h := channelTestServer(t, nil)
	working := server.llmManager
	server.llmManager = &unavailableLLM{}
	ev := channelEvent{Type: "message", ID: "m1", ChatID: "chat-1", Text: "hi"}
	if w := postJSON(t, h, "/api/channels/messages", ev); w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", w.Code)
	}
	if _, err := database.LatestChannelRoute(t.Context(), "chat-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("route of the failed delivery: %v", err)
	}
	server.llmManager = working
	if w := postJSON(t, h, "/api/channels/messages", ev); w.Code != http.StatusNoContent {
		t.Fatalf("retry: status %d: %s", w.Code, w.Body.String())
	}
	if got := channelUserMessages(t, database, chatAssistant(t, server, "chat-1")); !slices.Equal(got, []string{"m1: hi"}) {
		t.Fatalf("user messages = %q", got)
	}
}

// The Assistant answers a chat with message_user: its messages and
// reactions reach the chat, and what the chat cannot take is an error the
// agent sees.
func TestAssistantRepliesWithMessageUser(t *testing.T) {
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
			conversationID = chatAssistant(t, server, chat)
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

	// The Assistant has message_user on.
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
	route, err := database.LatestChannelRoute(t.Context(), chat)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(route.Endpoint)
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

	// A missing attachment must not send even the caption.
	file := filepath.Join(t.TempDir(), "missing.png")
	before := len(events())
	call(claudetool.MessageUserInput{Text: "see attached", Attachments: []string{file}})
	if r := lastResult(); !r.ToolError || !strings.Contains(r.ToolResult[0].Text, "attachment") {
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
	assistantID, err := server.ensureAssistant(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	chat := assistantChat{s: server, conversationID: assistantID}
	// texted returns a message texted from a chat whose endpoint is name.
	texted := func(name string) claudetool.UserMessage {
		t.Helper()
		id := "in-" + name
		if err := database.RecordChannelRoute(t.Context(), db.ChannelRoute{ConversationID: assistantID, ExternalMessageID: id, ChatID: "chat-" + name, Endpoint: endpoint.URL + "/" + name}); err != nil {
			t.Fatal(err)
		}
		return claudetool.UserMessage{ID: id, SequenceID: 1, ExternalID: id}
	}

	if err := chat.Send(t.Context(), "hi", nil, texted("refuse")); err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "line_paused") {
		t.Fatalf("refused send: %v", err)
	}
	for name, want := range map[string]string{"upstream": "may have been delivered", "internal": "may have been delivered", "upstream-refused": "refused"} {
		if err := chat.Send(t.Context(), "hi", nil, texted(name)); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v, want %q", name, err, want)
		}
	}
	// A reply or reaction to a message typed in Shelley's UI calls nothing.
	typed := claudetool.UserMessage{ID: "typed", SequenceID: 2}
	if err := chat.React(t.Context(), typed, "👍"); err != nil {
		t.Fatalf("reaction to a UI message: %v", err)
	}
	if err := chat.Send(t.Context(), "hi", nil, typed); err != nil {
		t.Fatalf("reply to a UI message: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-got
		cancel()
	}()
	if err := chat.Send(ctx, "hi", nil, texted("hang")); err == nil || !strings.Contains(err.Error(), "may have been delivered") {
		t.Fatalf("cancelled send: %v", err)
	}
}

// A web chat's messages go to the Assistant, which keeps message_user's
// answers to them, and the web page reads them from the chat's stream: the
// Assistant's.
func TestWebChatRoundTrip(t *testing.T) {
	t.Parallel()
	server, database, h := channelTestServer(t, nil)
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
	assistantID := chatAssistant(t, server, chat)
	if route, err := database.LatestChannelRoute(t.Context(), chat); err != nil || route.Endpoint != "" {
		t.Fatalf("route = %+v, %v, want no endpoint", route, err)
	}
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), assistantID)
		return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
	})
	c := &compactTestConversation{t: t, database: database, id: assistantID}
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
		if sr.ConversationID != assistantID && len(sr.Messages) > 0 {
			t.Fatalf("stream of conversation %s, want %s", sr.ConversationID, assistantID)
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

// The first reaction and reply of the message that starts the Assistant
// answer it in place of the agent. A later message's, from any chat, are
// ignored. The user follows the Assistant in their chats, so it sends no
// notifications.
func TestChannelFirstMessageAnswersTheAssistantsFirst(t *testing.T) {
	t.Parallel()
	server, database, h, _ := debugChannelTestServer(t)
	model := &turnRecordingService{Service: predictable.NewService()}
	server.llmManager = &testLLMManager{service: model}
	const chat = "debug-first"
	const welcome = "You made it! I’m Shelley."
	receive := func(chat string, body map[string]string) string {
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
	hey := map[string]string{"text": "Hey", "first_reaction": "❤️", "first_reply": welcome}

	heyID := receive(chat, hey)
	server.channelDebug.mu.Lock()
	evs := slices.Clone(server.channelDebug.chats[chat].Events)
	server.channelDebug.mu.Unlock()
	if len(evs) != 3 || evs[1].Kind != "react" || evs[1].Text != "love" || evs[1].MessageID != heyID || evs[2].Kind != "out" || evs[2].Text != welcome || evs[2].ReplyTo != "" {
		t.Fatalf("chat events = %+v", evs)
	}
	assistantID := chatAssistant(t, server, chat)
	if conv, err := database.GetConversationByID(t.Context(), assistantID); err != nil || conv.AgentWorking {
		t.Fatalf("agent working after the first reply: %v", err)
	}
	notified := &recordingChannel{}
	server.RegisterNotificationChannel(notified)
	server.publishConversationState(ConversationState{ConversationID: assistantID, Model: "predictable"})
	if notified.count() != 0 {
		t.Fatal("Assistant notified")
	}

	// Another chat's first message is not the Assistant's.
	receive("debug-second", hey)
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), assistantID)
		return err == nil && !c.AgentWorking
	})
	server.channelDebug.mu.Lock()
	evs = slices.Clone(server.channelDebug.chats["debug-second"].Events)
	server.channelDebug.mu.Unlock()
	if len(evs) != 1 || evs[0].Kind != "in" {
		t.Fatalf("second chat's events = %+v, want only its message", evs)
	}
	model.mu.Lock()
	turns := slices.Clone(model.turns)
	model.mu.Unlock()
	if len(turns) != 1 {
		t.Fatalf("agent turns = %d, want 1: the second message's", len(turns))
	}
	req := turns[0]
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
	if _, err := server.startNewGeneration(t.Context(), assistantID); err != nil {
		t.Fatal(err)
	}
	receive(chat, hey)
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), assistantID)
		return err == nil && !c.AgentWorking
	})
	model.mu.Lock()
	turns = slices.Clone(model.turns)
	model.mu.Unlock()
	if len(turns) != 2 {
		t.Fatalf("agent turns = %d, want 2", len(turns))
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
	c := &compactTestConversation{t: t, database: database, id: chatAssistant(t, server, "chat-gone")}
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
	server, database, h, _ := debugChannelTestServer(t)
	const chat = "debug-refused"
	if w := postJSON(t, h, "/api/debug/channels/"+chat+"/refuse", map[string]string{"code": "line_paused"}); w.Code != http.StatusNoContent {
		t.Fatalf("refuse: status %d", w.Code)
	}
	if w := postJSON(t, h, "/api/debug/channels/"+chat+"/receive", map[string]string{"text": "Hey", "first_reply": "Welcome"}); w.Code != http.StatusOK {
		t.Fatalf("receive: status %d: %s", w.Code, w.Body.String())
	}
	assistantID := chatAssistant(t, server, chat)
	if conv, err := database.GetConversationByID(t.Context(), assistantID); err != nil || conv.AgentWorking {
		t.Fatalf("agent working after the first reply: %v", err)
	}
	c := &compactTestConversation{t: t, database: database, id: assistantID}
	if _, r := c.lastMessageUserResult(); !r.ToolError || !strings.Contains(r.ToolResult[0].Text, "line_paused") || !messageUserDisplay(t, r).ChatFailed {
		t.Fatalf("result = %+v", r)
	}
}

// A web chat's first message starts the Assistant with a first reply too,
// kept in the conversation.
func TestWebChatFirstReply(t *testing.T) {
	t.Parallel()
	server, database, _ := channelTestServer(t, nil)
	ev := channelEvent{Type: "message", ID: "m1", ChatID: "web-first", Web: true, Text: "Hey", FirstReaction: "❤️", FirstReply: "Welcome"}
	if err := server.receiveChannelEvent(t.Context(), "", ev); err != nil {
		t.Fatal(err)
	}
	c := &compactTestConversation{t: t, database: database, id: chatAssistant(t, server, ev.ChatID)}
	if _, r := c.lastMessageUserResult(); r.ToolError || r.ToolResult[0].Text != "Sent." {
		t.Fatalf("result = %+v", r)
	}
}
