package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

// A turn started from the chat first shows Shelley typing there. A message
// typed in Shelley's UI is answered there only: no typing, no send.
func TestChannelTypingAtTurnStart(t *testing.T) {
	t.Parallel()
	server, database, h := channelTestServer(t, nil)
	var mu sync.Mutex
	var calls []string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Write([]byte(`{"message_id":"out-1"}`))
	}))
	t.Cleanup(endpoint.Close)
	conv, err := server.channelConversation(t.Context(), endpoint.URL, "chat-typing", "")
	if err != nil {
		t.Fatal(err)
	}
	ev := channelEvent{Type: "message", ID: "m1", ChatID: "chat-typing", Text: `message_user: {"text":"on it"}`}
	if w := postJSON(t, h, "/api/channels/messages", ev); w.Code != http.StatusNoContent {
		t.Fatalf("deliver: status %d: %s", w.Code, w.Body.String())
	}
	idle := func(messages int) {
		t.Helper()
		waitFor(t, 5*time.Second, func() bool {
			c, err := database.GetConversationByID(t.Context(), conv.ConversationID)
			return err == nil && !c.AgentWorking && len(channelUserMessages(t, database, conv.ConversationID)) == messages
		})
	}
	got := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
	idle(1)
	want := []string{"POST /typing", "POST /send"}
	if !slices.Equal(got(), want) {
		t.Fatalf("calls = %v, want %v", got(), want)
	}

	path := "/api/conversation/" + conv.ConversationID + "/chat"
	if w := postJSON(t, h, path, ChatRequest{Message: `message_user: {"text":"for the UI"}`, Model: "predictable"}); w.Code != http.StatusAccepted {
		t.Fatalf("UI message: status %d: %s", w.Code, w.Body.String())
	}
	idle(2)
	c := &compactTestConversation{t: t, database: database, id: conv.ConversationID}
	if _, r := c.lastMessageUserResult(); r.ToolError {
		t.Fatalf("UI reply result = %+v", r)
	}
	if !slices.Equal(got(), want) {
		t.Fatalf("a UI message reached the chat: calls = %v, want %v", got(), want)
	}
}
