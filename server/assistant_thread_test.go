package server

import (
	"context"
	"encoding/json"
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

// call makes an HTTP request of h and returns the response, failing unless
// its status is want.
func call(t *testing.T, h http.Handler, want int, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

// workspaceChanged reports whether change has workspace_changed broadcast,
// waiting a while for it.
func workspaceChanged(t *testing.T, s *Server, change func()) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	next := s.streamPub.Subscribe(ctx, -1)
	change()
	for event, ok := next(); ok; event, ok = next() {
		if event.WorkspaceChanged {
			return true
		}
	}
	return false
}

// listedAssistant applies the rule a client reads the Assistant from the
// conversation list with: the most recently created conversation with the
// Assistant profile. The list has only top-level, unarchived conversations.
func listedAssistant(t *testing.T, h http.Handler) string {
	t.Helper()
	var rows []ConversationWithState
	if err := json.Unmarshal(call(t, h, http.StatusOK, "GET", "/api/conversations", "").Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	var newest *ConversationWithState
	for i, row := range rows {
		if db.ParseConversationOptions(row.ConversationOptions).Profile == db.AssistantProfile && !row.IsDraft &&
			(newest == nil || row.CreatedAt.After(newest.CreatedAt)) {
			newest = &rows[i]
		}
	}
	if newest == nil {
		return ""
	}
	return newest.ConversationID
}

// A client starts a new Assistant by creating a conversation with the
// Assistant profile, whatever the workspace flag, and finds it in the
// conversation list. Texts go to it from then on, it is named like any
// conversation, and clients are told. The old Assistant is an ordinary
// conversation that still answers the texts it took, and can be archived
// and deleted; the Assistant can't be.
func TestNewAssistantThread(t *testing.T) {
	for _, workspace := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "workspace"}[workspace], func(t *testing.T) {
			t.Parallel()
			server, database, h, _ := debugChannelTestServer(t)
			t.Cleanup(func() { stopActiveConversationLoops(server) })
			if workspace {
				setWorkspaceFlag(t, server, "true")
			}
			a := &assistantChannelTest{t: t, server: server, database: database, h: h, assistantID: currentAssistant(t, server)}

			// Without one, a text makes the Assistant, and clients are told.
			var first string
			receive := func() {
				w := postJSON(t, h, "/api/debug/channels/debug-p/receive", map[string]string{"text": messageUserText(t, claudetool.MessageUserInput{Text: "hi p"})})
				if w.Code != http.StatusOK {
					t.Fatalf("receive: status %d: %s", w.Code, w.Body.String())
				}
				var out struct {
					MessageID string `json:"message_id"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				first = out.MessageID
			}
			if workspace {
				receive()
			} else if !workspaceChanged(t, server, receive) {
				t.Fatal("no workspace_changed for the first Assistant")
			}
			old := chatAssistant(t, server, "debug-p")
			if workspace && old != a.assistantID {
				t.Fatalf("the text went to %s, not workspace mode's Assistant %s", old, a.assistantID)
			}
			a.assistantID = old
			a.waitIdle()
			firstSeq := a.sequenceOf(first)
			if got := listedAssistant(t, h); got != old {
				t.Fatalf("the list's Assistant is %q, want %q", got, old)
			}

			// Time passes: the list's created_at has whole seconds.
			if err := database.Pool().Exec(t.Context(), "UPDATE conversations SET created_at = datetime(created_at, '-1 minute') WHERE conversation_id = ?", old); err != nil {
				t.Fatal(err)
			}

			// A new conversation with the Assistant profile is the new Assistant.
			var created struct {
				ConversationID string `json:"conversation_id"`
			}
			if !workspaceChanged(t, server, func() {
				w := call(t, h, http.StatusCreated, "POST", "/api/conversations/new",
					`{"message":"plan my week","cwd":"`+t.TempDir()+`","conversation_options":{"profile":"Assistant"}}`)
				if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
					t.Fatal(err)
				}
			}) {
				t.Fatal("no workspace_changed for the new Assistant")
			}
			newID := created.ConversationID
			if newID == "" || newID == old {
				t.Fatalf("new conversation %q, old Assistant %q", newID, old)
			}
			if got := currentAssistant(t, server); got != newID {
				t.Fatalf("Assistant = %q, want the new %q", got, newID)
			}
			if got := listedAssistant(t, h); got != newID {
				t.Fatalf("the list's Assistant is %q, want %q", got, newID)
			}
			var ws db.Workspace
			if err := json.Unmarshal(call(t, h, http.StatusOK, "GET", "/api/workspace", "").Body.Bytes(), &ws); err != nil || ws.AssistantID != newID {
				t.Fatalf("GET /api/workspace assistant_id = %q, %v; want %q", ws.AssistantID, err, newID)
			}

			// Texts, from old chats and new, go to it.
			a.assistantID = newID
			a.waitIdle()
			a.text("debug-p", messageUserText(t, claudetool.MessageUserInput{Text: "again"}))
			a.text("debug-q", messageUserText(t, claudetool.MessageUserInput{Text: "hi q"}))
			for _, chat := range []string{"debug-p", "debug-q"} {
				if got := chatAssistant(t, server, chat); got != newID {
					t.Fatalf("%s went to %s, not the new Assistant", chat, got)
				}
			}
			// It is named after its first message.
			waitFor(t, 10*time.Second, func() bool {
				c, err := database.GetConversationByID(t.Context(), newID)
				return err == nil && c.Slug != nil
			})

			// The old one keeps answering the texts it took, even once Shelley
			// lets go of it.
			server.stopAllConversations(t.Context())
			was := &assistantChannelTest{t: t, server: server, database: database, h: h, assistantID: old}
			was.typed(messageUserText(t, claudetool.MessageUserInput{Text: "late answer", ReplyTo: firstSeq}))
			if got := a.sent("debug-p"); len(got) == 0 || got[len(got)-1] != "out:"+first+":late answer" {
				t.Fatalf("debug-p got %q", got)
			}

			// The Assistant stays; the old one can go.
			call(t, h, http.StatusConflict, "POST", "/api/conversation/"+newID+"/archive", "")
			call(t, h, http.StatusConflict, "POST", "/api/conversation/"+newID+"/delete", "")
			call(t, h, http.StatusOK, "POST", "/api/conversation/"+newID+"/rename", `{"slug":"my-assistant"}`)
			call(t, h, http.StatusOK, "POST", "/api/conversation/"+old+"/archive", "")
			call(t, h, http.StatusOK, "POST", "/api/conversation/"+old+"/delete", "")
			if got := currentAssistant(t, server); got != newID {
				t.Fatalf("Assistant = %q after the old one went, want %q", got, newID)
			}
		})
	}
}

// Once a new Assistant thread starts, the old one keeps the texts it took:
// a chat's retry of one is not delivered again, to the new one, and one
// left queued in the old one is drained at startup.
func TestNewAssistantThreadKeepsOldTexts(t *testing.T) {
	t.Parallel()
	server, database, h := channelTestServer(t, nil)
	text := func(id string) {
		t.Helper()
		ev := channelEvent{Type: "message", ID: id, ChatID: "chat-old", Text: "hello " + id}
		if w := postJSON(t, h, "/api/channels/messages", ev); w.Code != http.StatusNoContent {
			t.Fatalf("text %s: status %d: %s", id, w.Code, w.Body.String())
		}
	}
	idle := func(id string) {
		t.Helper()
		waitFor(t, 10*time.Second, func() bool {
			c, err := database.GetConversationByID(t.Context(), id)
			return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
		})
	}
	texts := func(id string, want ...string) {
		t.Helper()
		var got []string
		for _, m := range channelUserMessages(t, database, id) {
			if !strings.HasPrefix(m, ": ") {
				got = append(got, m)
			}
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s's texts = %q, want %q", id, got, want)
		}
	}

	text("m1")
	old := chatAssistant(t, server, "chat-old")
	idle(old)
	if err := database.Pool().Exec(t.Context(), "UPDATE conversations SET created_at = datetime(created_at, '-1 minute') WHERE conversation_id = ?", old); err != nil {
		t.Fatal(err)
	}
	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	w := call(t, h, http.StatusCreated, "POST", "/api/conversations/new",
		`{"message":"plan my week","cwd":"`+t.TempDir()+`","conversation_options":{"profile":"Assistant"}}`)
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if got := currentAssistant(t, server); got != created.ConversationID {
		t.Fatalf("Assistant = %q, want the new %q", got, created.ConversationID)
	}
	idle(created.ConversationID)

	text("m1")
	idle(created.ConversationID)
	texts(old, "m1: hello m1")
	texts(created.ConversationID)

	llmJSON, err := json.Marshal(llm.UserStringMessage("hello q1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AppendQueuedMessage(t.Context(), old, db.QueuedMessage{
		ID: "q1", Llm: llmJSON, CreatedAt: time.Now().UTC(), Model: "predictable", ExternalMessageID: "q1",
	}); err != nil {
		t.Fatal(err)
	}
	server.recoverChannelQueues(t.Context())
	idle(old)
	texts(old, "m1: hello m1", "q1: hello q1")
}

// Switching a conversation to or from the Assistant profile gives its
// loop the texting and Slack tools or takes them away, even when only the
// label changes. Only a former Assistant can switch away.
func TestAssistantLabelChangesTools(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	t.Cleanup(func() { stopActiveConversationLoops(e.srv) })
	settings := `"model":"model-b","tool_overrides":{"message_user":"on","send_to_thread":"on","send_to_channel":"on","add_reaction":"on"}`
	e.want(200, "PUT", "/api/profiles/Assistant", `{`+settings+`}`)
	e.want(201, "POST", "/api/profiles", `{"name":"Twin",`+settings+`}`)
	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	body := e.want(201, "POST", "/api/conversations/new", `{"message":"hello","cwd":"`+t.TempDir()+`","conversation_options":{"profile":"Twin"}}`)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	id := created.ConversationID
	e.waitTurn(id, 0)
	slack := func(want bool) {
		t.Helper()
		if got := hasTool(e.lastTurnRequest(), claudetool.SendToThreadName); got != want {
			t.Fatalf("Slack tools: %v, want %v", got, want)
		}
	}
	turn := func() {
		t.Helper()
		n := len(listMessages(t, e.database, id))
		e.want(202, "POST", "/api/conversation/"+id+"/chat", `{"message":"again"}`)
		e.waitTurn(id, n)
	}
	slack(false)

	n := len(listMessages(t, e.database, id))
	e.want(200, "POST", "/api/conversation/"+id+"/settings", `{"profile":"Assistant"}`)
	if msgs := listMessages(t, e.database, id); len(msgs) != n+1 || !strings.Contains(*msgs[n].UserData, `"text":"Profile changed to Assistant."`) {
		t.Fatalf("not just relabelled: %s", *msgs[len(msgs)-1].UserData)
	}
	if got := currentAssistant(t, e.srv); got != id {
		t.Fatalf("Assistant = %q, want %q", got, id)
	}
	turn()
	slack(true)

	if err := e.database.Pool().Exec(t.Context(), "UPDATE conversations SET created_at = datetime(created_at, '-1 minute') WHERE conversation_id = ?", id); err != nil {
		t.Fatal(err)
	}
	body = e.want(201, "POST", "/api/conversations/new", `{"message":"hello","cwd":"`+t.TempDir()+`","conversation_options":{"profile":"Assistant"}}`)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(created.ConversationID, 0)
	e.want(200, "POST", "/api/conversation/"+id+"/settings", `{"profile":"Twin"}`)
	turn()
	slack(false)
}

// The Assistant profile marks the Assistant, so it can't be the default,
// which every new conversation would take, and the Assistant can't switch
// away from it, as it can't be archived; earlier ones can. Without the
// profile, a new Assistant starts from the default's settings, as its own.
func TestAssistantProfileRules(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	t.Cleanup(func() { stopActiveConversationLoops(e.srv) })
	e.want(409, "POST", "/api/profiles/Assistant/default", "")
	start := func() string {
		t.Helper()
		var created struct {
			ConversationID string `json:"conversation_id"`
		}
		body := e.want(201, "POST", "/api/conversations/new", `{"message":"hello","cwd":"`+t.TempDir()+`","conversation_options":{"profile":"Assistant"}}`)
		if err := json.Unmarshal([]byte(body), &created); err != nil {
			t.Fatal(err)
		}
		e.waitTurn(created.ConversationID, 0)
		if got := currentAssistant(t, e.srv); got != created.ConversationID {
			t.Fatalf("Assistant = %q, want %q", got, created.ConversationID)
		}
		return created.ConversationID
	}
	old := start()
	e.want(409, "POST", "/api/conversation/"+old+"/settings", `{"profile":"Default"}`)
	e.want(200, "POST", "/api/conversation/"+old+"/settings", `{"model":"model-b"}`)

	// The tools the Assistant answers with are off by default, and on in
	// the Assistant profile, as settings like any other.
	var tools struct {
		Tools []struct {
			Name      string `json:"name"`
			DefaultOn bool   `json:"default_on"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/tools", "")), &tools); err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if slices.Contains(db.AssistantTools, tool.Name) && tool.DefaultOn {
			t.Errorf("%s is on by default", tool.Name)
		}
	}
	assistantTools := func(what string, overrides map[string]string) {
		t.Helper()
		for _, name := range db.AssistantTools {
			if overrides[name] != "on" {
				t.Fatalf("%s: %s is %q, want on (%v)", what, name, overrides[name], overrides)
			}
		}
	}

	// Switching into the profile makes an Assistant, with the profile's
	// settings.
	var other struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/conversations/new", `{"message":"hello","cwd":"`+t.TempDir()+`"}`)), &other); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(other.ConversationID, 0)
	var switched ConversationSettings
	if err := json.Unmarshal([]byte(e.want(200, "POST", "/api/conversation/"+other.ConversationID+"/settings", `{"profile":"Assistant"}`)), &switched); err != nil {
		t.Fatal(err)
	}
	assistantTools("switched in", switched.ToolOverrides)
	if got := currentAssistant(t, e.srv); got != other.ConversationID {
		t.Fatalf("switched in: Assistant = %q, want %q", got, other.ConversationID)
	}

	if err := e.database.Pool().Exec(t.Context(), "UPDATE conversations SET created_at = datetime(created_at, '-1 minute') WHERE conversation_id = ?", old); err != nil {
		t.Fatal(err)
	}
	e.want(204, "DELETE", "/api/profiles/Assistant", "")
	// A project's conversations have its name as their profile.
	e.want(400, "POST", "/api/projects", `{"name":"Assistant"}`)
	e.want(201, "POST", "/api/projects", `{"name":"Mine"}`)
	e.want(400, "PUT", "/api/projects/Mine", `{"name":"Assistant"}`)
	id := start()
	c, err := e.database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got := conversationSettings(*c); got.Profile != db.AssistantProfile || !got.Edited {
		t.Fatalf("new Assistant without the profile: %+v", got)
	}
	assistantTools("new Assistant without the profile", conversationSettings(*c).ToolOverrides)
	e.want(200, "POST", "/api/conversation/"+old+"/settings", `{"profile":"Default"}`)
}
