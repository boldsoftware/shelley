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
)

// Slack events go to one Slack Assistant conversation, and its Slack tools
// post to its endpoint.
func TestSlackAssistantRoundTrip(t *testing.T) {
	t.Parallel()
	server, database, _ := channelTestServer(t, nil)
	var mu sync.Mutex
	var sent []map[string]any
	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		if r.URL.Path != "/send" || json.NewDecoder(r.Body).Decode(&in) != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		mu.Lock()
		sent = append(sent, in)
		mu.Unlock()
		w.Write([]byte(`{"message_id":"9.9"}`))
	}))
	t.Cleanup(integration.Close)

	deliver := func(ts, text string) string {
		t.Helper()
		ev := slackEvent{Kind: "message", ID: ts, Channel: "C1", ChannelName: "#temp", TS: ts, ThreadTS: "1.0", User: "U1", UserName: "phil", Text: text}
		id, err := server.receiveSlackEvent(t.Context(), integration.URL, ev)
		if err != nil {
			t.Fatalf("deliver: %v", err)
		}
		waitFor(t, 10*time.Second, func() bool {
			c, err := database.GetConversationByID(t.Context(), id)
			return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
		})
		return id
	}

	id := deliver("1.0", `send_to_thread: {"channel":"C1","thread_ts":"1.0","text":"hi"}`)
	if again := deliver("1.0", "retry"); again != id {
		t.Fatalf("retry went to %s, not %s", again, id)
	}
	if other := deliver("2.0", `send_to_channel: {"channel":"C2","thread_ts":"ignored","text":"news"}`); other != id {
		t.Fatalf("second message went to %s, not %s", other, id)
	}
	mu.Lock()
	got := slices.Clone(sent)
	mu.Unlock()
	msgs := channelUserMessages(t, database, id)
	if len(msgs) != 2 || !strings.HasPrefix(msgs[0], "C1/1.0/1.0: <slack_message channel=\"C1\" channel_name=\"#temp\"") {
		t.Fatalf("messages = %q", msgs)
	}
	// Replies link to the thread's latest event; new threads to the conversation.
	firstID := channelMessageID(t, database, id, "C1/1.0/1.0")
	u := server.conversationURL("slack-app")
	want := []map[string]any{
		{"channel": "C1", "thread_ts": "1.0", "text": "hi", "link": u + "?message_id=" + firstID},
		{"channel": "C2", "text": "news", "link": u},
	}
	if len(got) != 2 || !maps.Equal(got[0], want[0]) || !maps.Equal(got[1], want[1]) {
		t.Fatalf("sent = %v, want %v", got, want)
	}
	conv, err := database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if conv.Slug == nil || *conv.Slug != "slack-app" {
		t.Fatalf("slug = %v", conv.Slug)
	}
}

func channelMessageID(t *testing.T, database *db.DB, conversationID, externalID string) string {
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

// Reactions and clicks are tagged by kind; a message's files are saved and
// referenced by path.
func TestSlackEventKinds(t *testing.T) {
	t.Parallel()
	_, database, h := channelTestServer(t, nil)
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("log line")) }))
	t.Cleanup(files.Close)
	var id string
	for _, ev := range []slackEvent{
		{Kind: "message", ID: "1.0", TS: "1.0", Text: "see file", Files: []channelAttachment{{URL: files.URL + "/files-pri/T-F/build.log", Filename: "build.log"}}},
		{Kind: "reaction", ID: "reaction:2.0", TS: "1.5", Reaction: "thumbsup", Text: "Deploy?"},
		{Kind: "action", ID: "action:3.0", TS: "1.5", Text: `Clicked "Deploy"`},
	} {
		ev.Channel, ev.ThreadTS, ev.User = "C1", "1.0", "U1"
		w := postJSON(t, h, "/api/channels/slack", ev)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", ev.Kind, w.Code, w.Body.String())
		}
		var out struct {
			ConversationID string `json:"conversation_id"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		id = out.ConversationID
		waitFor(t, 10*time.Second, func() bool {
			c, err := database.GetConversationByID(t.Context(), id)
			return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
		})
	}
	msgs := channelUserMessages(t, database, id)
	if len(msgs) != 3 ||
		!regexp.MustCompile(`see file\n\n\[/\S*/build\S*\.log\]`).MatchString(msgs[0]) ||
		!strings.HasPrefix(msgs[1], `C1/1.0/reaction:2.0: <slack_reaction channel="C1" channel_name="" thread_ts="1.0" ts="1.5" user="U1" user_name="" emoji="thumbsup">`) ||
		!strings.HasPrefix(msgs[2], `C1/1.0/action:3.0: <slack_action `) {
		t.Fatalf("messages = %q", msgs)
	}
}

// Replies go to the slack-app integration.
func TestSlackEventRepliesThroughIntegration(t *testing.T) {
	t.Parallel()
	_, database, h := channelTestServer(t, nil)
	ev := slackEvent{Kind: "message", ID: "1.0", Channel: "D1", ChannelName: "DM", TS: "1.0", ThreadTS: "1.0", User: "U1", Text: "hi"}
	w := postJSON(t, h, "/api/channels/slack", ev)
	if w.Code != http.StatusOK {
		t.Fatalf("deliver: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		ConversationID string `json:"conversation_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	conv, err := database.GetConversationByID(t.Context(), out.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.ExternalEndpoint == nil || *conv.ExternalEndpoint != "https://slack-app.int.exe.xyz" {
		t.Fatalf("external_endpoint = %v, want the slack-app integration", conv.ExternalEndpoint)
	}
}

// The slack-app gateway's refusals, like its 403 for a channel the VM may
// not post in, sent nothing; its failures after calling Slack may have.
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
	id, err := server.receiveSlackEvent(t.Context(), integration.URL, slackEvent{Kind: "message", ID: "1.0", Channel: "C1", TS: "1.0", ThreadTS: "1.0", User: "U1", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		c, err := database.GetConversationByID(t.Context(), id)
		return err == nil && !c.AgentWorking && c.QueuedMessages == "[]"
	})
	chat := slackChat{s: server, conversationID: id}
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
