package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

// A turn the chat started in the Assistant first shows Shelley typing there.
// A message typed in Shelley's UI is answered there only: no typing, no
// send.
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
	ev := channelEvent{Type: "message", ID: "m1", ChatID: "chat-typing", Text: `message_user: {"text":"on it"}`}
	if err := server.receiveChannelEvent(t.Context(), endpoint.URL, ev); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	assistantID := chatAssistant(t, server, "chat-typing")
	idle := func(messages int) {
		t.Helper()
		waitFor(t, 5*time.Second, func() bool {
			c, err := database.GetConversationByID(t.Context(), assistantID)
			return err == nil && !c.AgentWorking && len(channelUserMessages(t, database, assistantID)) == messages
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

	path := "/api/conversation/" + assistantID + "/chat"
	if w := postJSON(t, h, path, ChatRequest{Message: `message_user: {"text":"for the UI"}`, Model: "predictable"}); w.Code != http.StatusAccepted {
		t.Fatalf("UI message: status %d: %s", w.Code, w.Body.String())
	}
	idle(2)
	c := &compactTestConversation{t: t, database: database, id: assistantID}
	if _, r := c.lastMessageUserResult(); r.ToolError {
		t.Fatalf("UI reply result = %+v", r)
	}
	if !slices.Equal(got(), want) {
		t.Fatalf("a UI message reached the chat: calls = %v, want %v", got(), want)
	}
}
