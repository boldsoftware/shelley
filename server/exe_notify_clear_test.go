package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/server/notifications"
)

// TestExePushClearedWhenUserContinues checks that a conversation's
// turn-end push carries its thread, and that the user's next message, and
// only the first one after a push, clears that thread at the gateway.
func TestExePushClearedWhenUserContinues(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	var mu sync.Mutex
	var gateway []map[string]any
	s.hookClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == exeNotifyGatewayURL {
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("decode gateway request: %v", err)
			}
			mu.Lock()
			gateway = append(gateway, body)
			mu.Unlock()
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"success":true,"sent":1}`)), Header: make(http.Header)}, nil
	})}
	gatewayRequests := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), gateway...)
	}

	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cid := conv.ConversationID
	thread := "shelley/" + cid
	push := func() {
		s.sendEndOfTurnHook(t.Context(), db.ConversationHook{URL: exeNotifyGatewayURL}, notifications.Event{
			Type:           notifications.EventAgentDone,
			ConversationID: cid,
			Timestamp:      time.Now(),
			Payload:        notifications.AgentDonePayload{Hostname: "vm.exe.xyz", ConversationTitle: "slug", VMName: "vm"},
		})
	}
	chat := func(msg string) {
		body, _ := json.Marshal(ChatRequest{Message: msg, Model: "predictable"})
		req := httptest.NewRequest("POST", "/api/conversation/"+cid+"/chat", strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		s.handleChatConversation(w, req, cid)
		if w.Code != http.StatusAccepted {
			t.Fatalf("chat: %d %s", w.Code, w.Body)
		}
		waitFor(t, 5*time.Second, func() bool { return !s.IsAgentWorking(cid) })
	}

	push()
	if got := gatewayRequests(); len(got) != 1 || got[0]["thread_id"] != thread || got[0]["title"] == nil {
		t.Fatalf("push: %v", got)
	}
	chat("echo: one")
	waitFor(t, 5*time.Second, func() bool { return len(gatewayRequests()) == 2 })
	if got := gatewayRequests()[1]; got["clear"] != true || got["thread_id"] != thread || len(got) != 2 {
		t.Fatalf("clear: %v", got)
	}

	// Nothing pushed since that clear: the next message clears nothing.
	chat("echo: two")
	push()
	chat("echo: three")
	waitFor(t, 5*time.Second, func() bool { return len(gatewayRequests()) == 4 })
	got := gatewayRequests()
	if got[2]["title"] == nil || got[3]["clear"] != true {
		t.Fatalf("want push then clear, got %v", got[2:])
	}
}
