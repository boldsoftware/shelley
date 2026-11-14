package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

// slackIntegrationStub records the calls of a slack-app integration.
type slackIntegrationStub struct {
	*httptest.Server
	mu    sync.Mutex
	calls []map[string]any
}

func newSlackIntegrationStub(t *testing.T) *slackIntegrationStub {
	s := &slackIntegrationStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		in["path"] = r.URL.Path
		s.mu.Lock()
		s.calls = append(s.calls, in)
		s.mu.Unlock()
		w.Write([]byte(`{"message_id":"9.9"}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *slackIntegrationStub) taken() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := s.calls
	s.calls = nil
	return calls
}

// Slack events go to the Assistant, made by the first, which answers in
// Slack with the Slack tools, through the slack-app integration the events
// came through. message_user doesn't answer Slack.
func TestSlackEventsGoToAssistant(t *testing.T) {
	t.Parallel()
	model := &turnRecordingService{Service: predictable.NewService()}
	server, database, _ := channelTestServer(t, model)
	integration := newSlackIntegrationStub(t)
	var id string
	// deliver delivers ev and returns its sequence id.
	deliver := func(ev slackEvent) int64 {
		t.Helper()
		ev.Kind, ev.TS, ev.Channel, ev.ChannelName, ev.User, ev.UserName = "message", ev.ID, "C1", "#temp", "U1", "phil"
		if err := server.receiveSlackEvent(t.Context(), integration.URL, ev); err != nil {
			t.Fatalf("deliver: %v", err)
		}
		id = chatAssistant(t, server, slackChatPrefix+ev.Channel+"/"+ev.ThreadTS)
		waitFor(t, 10*time.Second, func() bool {
			c, err := database.GetConversationByID(t.Context(), id)
			return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
		})
		return sequenceOfMessage(t, database, id, slackMessageID(t, database, id, slackChatPrefix+"C1/"+ev.ThreadTS+"/"+ev.ID))
	}
	sent := func(want ...map[string]any) {
		t.Helper()
		if got := integration.taken(); !slices.EqualFunc(got, want, maps.Equal) {
			t.Fatalf("sent %v, want %v", got, want)
		}
	}

	// Slack text reaches the model escaped as XML.
	first := deliver(slackEvent{ID: "1.0", ThreadTS: "1.0", Text: "hello"})
	conv, err := database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if conv.Slug == nil || *conv.Slug != db.AssistantSlug || conv.ExternalConversationID != nil {
		t.Fatalf("Slack's conversation is not the Assistant: %+v", conv)
	}
	tag := `channel="C1" channel_name="#temp" thread_ts="1.0" ts="1.0" user="U1" user_name="phil" mentions_you="false"`
	if msgs := channelUserMessages(t, database, id); !slices.Equal(msgs, []string{"slack:C1/1.0/1.0: <slack_message " + tag + ">\nhello\n</slack_message>"}) {
		t.Fatalf("messages = %q", msgs)
	}
	model.mu.Lock()
	seen := lastRequestUserText(model.turns[len(model.turns)-1])
	model.mu.Unlock()
	if want := fmt.Sprintf(`<slack_message sequence_id="%d" %s>`+"\nhello\n</slack_message>", first, tag); seen != want {
		t.Fatalf("the model saw %q, want %q", seen, want)
	}

	// The Slack tools post as the model wrote, linking to the thread's
	// latest event, or to the conversation for a new thread.
	u := server.conversationURL(db.AssistantSlug)
	deliver(slackEvent{ID: "2.0", ThreadTS: "1.0", Text: `send_to_thread: {"channel":"C1","thread_ts":"1.0","text":"news for <@U1> &amp; <#C3>"}`})
	sent(map[string]any{"path": "/send", "channel": "C1", "thread_ts": "1.0", "text": "news for <@U1> &amp; <#C3>", "link": u + "?message_id=" + slackMessageID(t, database, id, "slack:C1/1.0/2.0")})
	deliver(slackEvent{ID: "3.0", ThreadTS: "1.0", Text: `send_to_channel: {"channel":"C2","thread_ts":"ignored","text":"new"}`})
	sent(map[string]any{"path": "/send", "channel": "C2", "text": "new", "link": u})
	deliver(slackEvent{ID: "4.0", ThreadTS: "1.0", Text: `add_reaction: {"channel":"C1","ts":"4.0","emoji":":eyes:"}`})
	sent(map[string]any{"path": "/react", "channel": "C1", "ts": "4.0", "emoji": "eyes"})

	// message_user refuses to answer a Slack event, named or the latest,
	// as a mistake in the call, not a failure in the chat.
	c := &compactTestConversation{t: t, database: database, id: id}
	deliver(slackEvent{ID: "5.0", ThreadTS: "1.0", Text: messageUserText(t, claudetool.MessageUserInput{Text: "lost", ReplyTo: first, EndTurn: true})})
	if _, r := c.lastMessageUserResult(); !r.ToolError || !strings.Contains(r.ToolResult[0].Text, "is a Slack event: answer it in Slack with send_to_thread") {
		t.Fatalf("answer to a Slack event: %+v", r)
	}
	deliver(slackEvent{ID: "6.0", ThreadTS: "1.0", Text: messageUserText(t, claudetool.MessageUserInput{Text: "replied in Slack", EndTurn: true})})
	if _, r := c.lastMessageUserResult(); !r.ToolError || !strings.HasPrefix(r.ToolResult[0].Text, "the latest message is a Slack event") || messageUserDisplay(t, r).ChatFailed {
		t.Fatalf("answer naming no event: %+v", r)
	}
	sent()

	// A retry is dropped.
	deliver(slackEvent{ID: "1.0", ThreadTS: "1.0", Text: "hello"})
	if got := channelUserMessages(t, database, id); len(got) != 6 {
		t.Fatalf("after a retry, messages = %q", got)
	}
	// Another thread's events go to the same Assistant.
	assistantID := id
	deliver(slackEvent{ID: "7.0", ThreadTS: "7.0", Text: "hello"})
	if id != assistantID {
		t.Fatalf("another thread went to %s, not the Assistant %s", id, assistantID)
	}
}

// Texts and Slack interleave in the Assistant: the Slack tools post where
// the latest Slack event came from, even in a text's turn, and message_user
// answers a text after a Slack event only by naming it.
func TestSlackBetweenTexts(t *testing.T) {
	t.Parallel()
	a := newAssistantChannelTest(t)
	integration := newSlackIntegrationStub(t)
	slack := func(text string) {
		t.Helper()
		if err := a.server.receiveSlackEvent(t.Context(), integration.URL, slackEvent{Kind: "message", ID: text, Channel: "C1", TS: "1.0", ThreadTS: "1.0", Text: text}); err != nil {
			t.Fatal(err)
		}
		a.waitIdle()
	}
	slack("hi")
	textID := a.text("debug-a", `send_to_thread: {"channel":"C1","thread_ts":"1.0","text":"from a text's turn"}`)
	if got := integration.taken(); len(got) != 1 || got[0]["text"] != "from a text's turn" {
		t.Fatalf("Slack got %v", got)
	}
	slack(messageUserText(t, claudetool.MessageUserInput{Text: "replied in Slack", EndTurn: true}))
	slack(messageUserText(t, claudetool.MessageUserInput{Text: "to the phone", ReplyTo: a.sequenceOf(textID), EndTurn: true}))
	if s := a.sent("debug-a"); !slices.Equal(s, []string{"out:" + textID + ":to the phone"}) {
		t.Fatalf("chat a = %q", s)
	}
	if got := integration.taken(); len(got) != 0 {
		t.Fatalf("Slack got %v", got)
	}
}

// Every Assistant has the Slack tools, even one whose settings predate
// them.
func TestSlackToolsInEveryAssistant(t *testing.T) {
	t.Parallel()
	model := &turnRecordingService{Service: predictable.NewService()}
	server, database, _ := channelTestServer(t, model)
	id, err := server.ensureAssistant(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateConversationOptions(t.Context(), id, db.ConversationOptions{ToolOverrides: map[string]string{claudetool.MessageUserName: "on"}}); err != nil {
		t.Fatal(err)
	}
	if err := server.receiveSlackEvent(t.Context(), "http://slack.invalid", slackEvent{Kind: "message", ID: "1.0", Channel: "C1", TS: "1.0", ThreadTS: "1.0", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		model.mu.Lock()
		defer model.mu.Unlock()
		return len(model.turns) > 0
	})
	model.mu.Lock()
	defer model.mu.Unlock()
	tools := map[string]bool{}
	for _, tool := range model.turns[0].Tools {
		tools[tool.Name] = true
	}
	for _, name := range []string{claudetool.MessageUserName, claudetool.SendToThreadName, claudetool.SendToChannelName, claudetool.AddReactionName} {
		if !tools[name] {
			t.Errorf("the Assistant lacks %s", name)
		}
	}
}

func slackMessageID(t *testing.T, database *db.DB, conversationID, externalID string) string {
	t.Helper()
	var id string
	err := database.Queries(t.Context(), func(q *generated.Queries) (err error) {
		id, err = q.LatestExternalMessageWithPrefix(t.Context(), generated.LatestExternalMessageWithPrefixParams{ConversationID: conversationID, Prefix: externalID})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func sequenceOfMessage(t *testing.T, database *db.DB, conversationID, messageID string) int64 {
	t.Helper()
	typed, err := database.ListTypedUserMessages(t.Context(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range typed {
		if m.MessageID == messageID {
			return m.SequenceID
		}
	}
	t.Fatalf("no typed message %s", messageID)
	return 0
}

// Reactions and clicks are tagged by kind; a message's files are saved and
// referenced by path. An event missing its thread is refused.
func TestSlackEventKinds(t *testing.T) {
	t.Parallel()
	server, database, h := channelTestServer(t, nil)
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("log line")) }))
	t.Cleanup(files.Close)
	for _, ev := range []slackEvent{
		{Kind: "message", ID: "1.0", TS: "1.0", Text: "see file", Files: []channelAttachment{{URL: files.URL + "/files-pri/T-F/build.log", Filename: "build.log"}}},
		{Kind: "reaction", ID: "reaction:2.0", TS: "1.5", Reaction: "thumbsup", Text: "Deploy?"},
		{Kind: "action", ID: "action:3.0", TS: "1.5", Text: `Clicked "Deploy"`},
	} {
		ev.Channel, ev.ThreadTS, ev.User = "C1", "1.0", "U1"
		if w := postJSON(t, h, "/api/channels/slack", ev); w.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", ev.Kind, w.Code, w.Body.String())
		}
		waitFor(t, 10*time.Second, func() bool {
			c, err := database.GetConversationByID(t.Context(), server.assistantID())
			return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
		})
	}
	if w := postJSON(t, h, "/api/channels/slack", slackEvent{Kind: "message", ID: "4.0", Channel: "C1", TS: "4.0", Text: "hi"}); w.Code != http.StatusBadRequest {
		t.Fatalf("event without thread_ts: %d %s", w.Code, w.Body.String())
	}
	id := chatAssistant(t, server, "slack:C1/1.0")
	msgs := channelUserMessages(t, database, id)
	if len(msgs) != 3 ||
		!regexp.MustCompile(`see file\n\n\[/\S*/build\S*\.log\]`).MatchString(msgs[0]) ||
		!strings.HasPrefix(msgs[1], `slack:C1/1.0/reaction:2.0: <slack_reaction channel="C1" channel_name="" thread_ts="1.0" ts="1.5" user="U1" user_name="" emoji="thumbsup">`) ||
		!strings.HasPrefix(msgs[2], `slack:C1/1.0/action:3.0: <slack_action `) {
		t.Fatalf("messages = %q", msgs)
	}
}

// The Slack tools post through the integration the latest Slack event came
// through, and only once one has. The gateway's refusals, like its 403 for
// a channel the VM may not post in, sent nothing; its failures after
// calling Slack may have.
func TestSlackPostSaysWhetherRefused(t *testing.T) {
	t.Parallel()
	server, database, _ := channelTestServer(t, nil)
	status := map[string]int{
		"forbidden": 403, "not_attached": 403, "integration_not_found": 403, "box_not_found": 404,
		"not_linked": 409, "uninstalled": 409, "bad_request": 400,
		"upstream_error": 502, "internal_error": 500,
	}
	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in claudetool.SlackPost
		json.NewDecoder(r.Body).Decode(&in)
		w.WriteHeader(status[in.Text])
		fmt.Fprintf(w, `{"error":%q,"message":"no"}`, in.Text)
	}))
	t.Cleanup(integration.Close)
	id, err := server.ensureAssistant(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	chat := slackChat{s: server, conversationID: id}
	if _, err := chat.Send(t.Context(), claudetool.SlackPost{Channel: "C1", Text: "hi"}); err == nil || !strings.Contains(err.Error(), "nothing was sent: no Slack event has arrived") {
		t.Fatalf("before any Slack event: %v", err)
	}
	if err := server.receiveSlackEvent(t.Context(), integration.URL, slackEvent{Kind: "message", ID: "1.0", Channel: "C1", TS: "1.0", ThreadTS: "1.0", User: "U1", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), id)
		return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
	})
	for code := range status {
		want := "refused"
		if status[code] >= 500 {
			want = "may have been delivered"
		}
		if _, err := chat.Send(t.Context(), claudetool.SlackPost{Channel: "C1", Text: code}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", code, err, want)
		}
	}
}

// The model sees a stored Slack event under its own tag, with its sequence
// id; anything else from a chat is an external_message.
func TestSlackEventForTheModel(t *testing.T) {
	t.Parallel()
	stored := slackEvent{Kind: "reaction", Channel: "C1", ThreadTS: "1.0", TS: "1.5", User: "U1", UserName: `a"b`, Reaction: "tada"}.tagged("x < y")
	for _, tc := range []struct{ externalID, text, want string }{
		{"slack:C1/1.0/reaction:2", stored, `<slack_reaction sequence_id="7" channel="C1" channel_name="" thread_ts="1.0" ts="1.5" user="U1" user_name="a&#34;b" emoji="tada">` + "\nx &lt; y\n</slack_reaction>"},
		// Only a Slack chat's messages are Slack events.
		{"chat/1", stored, "<external_message sequence_id=\"7\">\n" + strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(stored) + "\n</external_message>"},
		{"slack:C1/1.0/3", "<slack_message>\nhi\n</slack_reaction>", "<external_message sequence_id=\"7\">\n&lt;slack_message&gt;\nhi\n&lt;/slack_reaction&gt;\n</external_message>"},
	} {
		got, err := messageWithSequenceID(llm.UserStringMessage(tc.text), tc.externalID, 7)
		if err != nil {
			t.Fatal(err)
		}
		if text := messageText(got); text != tc.want {
			t.Errorf("%s: got %q, want %q", tc.externalID, text, tc.want)
		}
	}
}

// A Slack post links to its conversation by id when it has no slug, as
// the Assistant does while another conversation holds "assistant".
func TestSlackLinkWithoutSlug(t *testing.T) {
	t.Parallel()
	server, database, _ := channelTestServer(t, nil)
	model := "predictable"
	c, err := database.CreateConversation(t.Context(), nil, true, nil, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	link, err := slackChat{s: server, conversationID: c.ConversationID}.link(t.Context(), "C1", "1.0")
	if err != nil || link != server.conversationURL(c.ConversationID) {
		t.Fatalf("link = %q, %v; want %q", link, err, server.conversationURL(c.ConversationID))
	}
}
