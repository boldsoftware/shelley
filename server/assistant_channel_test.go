package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// assistantChannelTest is a debug-channel server in workspace mode, whose
// Assistant takes texted messages.
type assistantChannelTest struct {
	t           *testing.T
	server      *Server
	database    *db.DB
	h           http.Handler
	assistantID string
}

func newAssistantChannelTest(t *testing.T) *assistantChannelTest {
	t.Helper()
	server, database, h, _ := debugChannelTestServer(t)
	setWorkspaceFlag(t, server, "true")
	id := server.assistantID()
	return &assistantChannelTest{t: t, server: server, database: database, h: h, assistantID: id}
}

func (a *assistantChannelTest) waitIdle() {
	a.t.Helper()
	waitFor(a.t, 5*time.Second, func() bool {
		c, err := a.database.GetConversationByID(a.t.Context(), a.assistantID)
		return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
	})
}

func messageUserText(t *testing.T, in claudetool.MessageUserInput) string {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return "message_user: " + string(b)
}

// text delivers text from debug chat chat and returns its channel id.
func (a *assistantChannelTest) text(chat, text string) string {
	a.t.Helper()
	w := postJSON(a.t, a.h, "/api/debug/channels/"+chat+"/receive", map[string]string{"text": text})
	if w.Code != http.StatusOK {
		a.t.Fatalf("receive: status %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		a.t.Fatal(err)
	}
	a.waitIdle()
	return out.MessageID
}

// typed sends text to the Assistant as the UI does.
func (a *assistantChannelTest) typed(text string) {
	a.t.Helper()
	conv, err := a.database.GetConversationByID(a.t.Context(), a.assistantID)
	if err != nil {
		a.t.Fatal(err)
	}
	body, _ := json.Marshal(ChatRequest{Message: text, Model: *conv.Model})
	req := httptest.NewRequest("POST", "/api/conversation/"+a.assistantID+"/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.server.handleChatConversation(w, req, a.assistantID)
	if w.Code != http.StatusAccepted {
		a.t.Fatalf("chat: status %d: %s", w.Code, w.Body.String())
	}
	a.waitIdle()
}

// sent returns the debug chat's outgoing events: "out:<reply_to>:<text>"
// and "react:<message_id>:<reaction>".
func (a *assistantChannelTest) sent(chat string) []string {
	d := &a.server.channelDebug
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	if c := d.chats[chat]; c != nil {
		for _, e := range c.Events {
			switch e.Kind {
			case "out":
				out = append(out, "out:"+e.ReplyTo+":"+e.Text)
			case "react":
				out = append(out, "react:"+e.MessageID+":"+e.Text)
			}
		}
	}
	return out
}

func (a *assistantChannelTest) sequenceOf(externalID string) int64 {
	a.t.Helper()
	typed, err := a.database.ListTypedUserMessages(a.t.Context(), a.assistantID)
	if err != nil {
		a.t.Fatal(err)
	}
	for _, m := range typed {
		if m.ExternalMessageID != nil && *m.ExternalMessageID == externalID {
			return m.SequenceID
		}
	}
	a.t.Fatalf("no message %s in the Assistant", externalID)
	return 0
}

// Texted messages from every chat go to the Assistant, and only what
// answers one is texted, to the chat it came from.
func TestTextedMessagesGoToAssistant(t *testing.T) {
	t.Parallel()
	a := newAssistantChannelTest(t)

	aID := a.text("debug-a", messageUserText(t, claudetool.MessageUserInput{Text: "hi a"}))
	bID := a.text("debug-b", messageUserText(t, claudetool.MessageUserInput{Text: "hi b"}))
	got := channelUserMessages(t, a.database, a.assistantID)
	if len(got) != 2 || !strings.HasPrefix(got[0], aID+": ") || !strings.HasPrefix(got[1], bID+": ") {
		t.Fatalf("Assistant user messages = %q", got)
	}
	if s := a.sent("debug-a"); !slices.Equal(s, []string{"out::hi a"}) {
		t.Fatalf("chat a = %q", s)
	}
	if s := a.sent("debug-b"); !slices.Equal(s, []string{"out::hi b"}) {
		t.Fatalf("chat b = %q", s)
	}

	// From a turn the UI started, only replies to texted messages are
	// texted, to their own chat.
	aSeq, bSeq := a.sequenceOf(aID), a.sequenceOf(bID)
	a.typed(messageUserText(t, claudetool.MessageUserInput{Text: "ui only"}))
	a.typed(messageUserText(t, claudetool.MessageUserInput{Text: "to a", ReplyTo: aSeq}))
	a.typed(messageUserText(t, claudetool.MessageUserInput{ReplyTo: bSeq, Reaction: "👍"}))
	a.typed(messageUserText(t, claudetool.MessageUserInput{ReplyTo: a.lastTypedSequence(), Reaction: "👍"}))
	if s := a.sent("debug-a"); !slices.Equal(s, []string{"out::hi a", "out:" + aID + ":to a"}) {
		t.Fatalf("chat a = %q", s)
	}
	if s := a.sent("debug-b"); !slices.Equal(s, []string{"out::hi b", "react:" + bID + ":like"}) {
		t.Fatalf("chat b = %q", s)
	}

	// A retried delivery is dropped.
	endpoint := a.server.debugChannelEndpoint("debug-b")
	ev := channelEvent{Type: "message", ID: bID, ChatID: "debug-b", Text: "again"}
	if err := a.server.receiveChannelEvent(t.Context(), endpoint, ev); err != nil {
		t.Fatal(err)
	}
	if got := channelUserMessages(t, a.database, a.assistantID); len(got) != 6 {
		t.Fatalf("after retry, Assistant user messages = %q", got)
	}
	// A texted message without an id could not be answered.
	ev.ID = ""
	if err := a.server.receiveChannelEvent(t.Context(), endpoint, ev); !errors.Is(err, errChannelEventNoID) {
		t.Fatalf("event without id: %v", err)
	}
}

func (a *assistantChannelTest) lastTypedSequence() int64 {
	a.t.Helper()
	typed, err := a.database.ListTypedUserMessages(a.t.Context(), a.assistantID)
	if err != nil || len(typed) == 0 {
		a.t.Fatalf("typed messages: %v", err)
	}
	return typed[0].SequenceID
}

// Without reply_to, a message answers the latest user message: a texted one
// is texted back, but a later notice (a subagent's report, a finished job)
// or typed message is not.
func TestAssistantChatAnswersLatestUserMessage(t *testing.T) {
	t.Parallel()
	a := newAssistantChannelTest(t)
	a.text("debug-c", "hello")
	chat := assistantChat{s: a.server, conversationID: a.assistantID}
	if err := chat.Send(t.Context(), "answer", nil, claudetool.UserMessage{}); err != nil {
		t.Fatal(err)
	}
	notice := llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Background job finished"}}}
	if _, err := a.database.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: a.assistantID, Type: db.MessageTypeUser, LLMData: notice, UserData: map[string]string{"background_job_id": "j1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := chat.Send(t.Context(), "job done", nil, claudetool.UserMessage{}); err != nil {
		t.Fatal(err)
	}
	if s := a.sent("debug-c"); !slices.Equal(s, []string{"out::answer"}) {
		t.Fatalf("chat c = %q", s)
	}
}

// A web chat's messages go to the Assistant too, and the Assistant is told
// where texts arrive.
func TestWebChatGoesToAssistant(t *testing.T) {
	t.Parallel()
	a := newAssistantChannelTest(t)
	ev := channelEvent{Type: "message", ID: "w1", ChatID: "web-x", Web: true, Text: "hi"}
	if err := a.server.receiveChannelEvent(t.Context(), "", ev); err != nil {
		t.Fatal(err)
	}
	if route, err := a.database.GetChannelRoute(t.Context(), a.assistantID, "w1"); err != nil || route.ChatID != "web-x" || route.Endpoint != "" {
		t.Fatalf("web chat route = %+v, %v", route, err)
	}
	prompt, err := a.server.workspaceInstructions(t.Context(), a.assistantID)
	if err != nil || !strings.Contains(prompt, "arrive in this conversation as external_message") {
		t.Fatalf("Assistant prompt lacks texts: %v", err)
	}
}

// A texted chat's conversation from before the Assistant becomes it,
// instead of a new one, when the Assistant is first needed: at startup, or
// for the first texted message, whatever the workspace flag. Its history
// stays, it answers the chat's earlier messages, and new texts from any
// chat go to it.
func TestTextedConversationBecomesAssistant(t *testing.T) {
	for _, trigger := range []string{"startup", "text"} {
		t.Run(trigger, func(t *testing.T) {
			t.Parallel()
			server, database, h, _ := debugChannelTestServer(t)
			// Such a conversation answered its chat with message_user.
			opts := db.ConversationOptions{ToolOverrides: map[string]string{claudetool.MessageUserName: "on"}}
			texted, err := database.CreateChannelConversation(t.Context(), "debug-p", server.debugChannelEndpoint("debug-p"), "predictable", opts)
			if err != nil {
				t.Fatal(err)
			}
			before, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
				ConversationID: texted.ConversationID, Type: db.MessageTypeUser, LLMData: llm.UserStringMessage("before"), ExternalMessageID: "p1",
			})
			if err != nil {
				t.Fatal(err)
			}
			a := &assistantChannelTest{t: t, server: server, database: database, h: h, assistantID: texted.ConversationID}
			if trigger == "startup" {
				if err := server.loadWorkspace(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			nextID := a.text("debug-q", messageUserText(t, claudetool.MessageUserInput{Text: "hi q"}))
			if server.assistantID() != texted.ConversationID {
				t.Fatalf("Assistant = %q, want the texted conversation %q", server.assistantID(), texted.ConversationID)
			}
			conv, err := database.GetConversationByID(t.Context(), texted.ConversationID)
			if err != nil {
				t.Fatal(err)
			}
			if conv.Slug == nil || *conv.Slug != db.AssistantSlug || conv.ExternalConversationID != nil || conv.ExternalEndpoint != nil {
				t.Fatalf("promoted conversation: slug %v, chat %v, endpoint %v", conv.Slug, conv.ExternalConversationID, conv.ExternalEndpoint)
			}

			// The chat's earlier message is answered in the chat; a typed one is not.
			a.typed(messageUserText(t, claudetool.MessageUserInput{Text: "to p", ReplyTo: before.SequenceID}))
			a.typed(messageUserText(t, claudetool.MessageUserInput{Text: "ui only"}))
			if s := a.sent("debug-p"); !slices.Equal(s, []string{"out:p1:to p"}) {
				t.Fatalf("chat p = %q", s)
			}
			if s := a.sent("debug-q"); !slices.Equal(s, []string{"out::hi q"}) {
				t.Fatalf("chat q = %q", s)
			}
			if got := channelUserMessages(t, database, texted.ConversationID); len(got) != 4 || got[0] != "p1: before" || !strings.HasPrefix(got[1], nextID+": ") {
				t.Fatalf("Assistant user messages = %q", got)
			}
		})
	}
}
