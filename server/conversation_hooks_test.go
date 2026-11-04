package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"shelley.exe.dev/db"
)

func TestRegisterConversationHookDedupes(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}

	body, err := json.Marshal(RegisterConversationHookRequest{URL: "http://notify.int.exe.xyz/"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		req := httptest.NewRequest("POST", "/api/conversation/"+conversation.ConversationID+"/hooks", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.handleRegisterConversationHook(w, req, conversation.ConversationID)
		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}
	}

	updated, err := database.GetConversationByID(t.Context(), conversation.ConversationID)
	if err != nil {
		t.Fatalf("failed to load conversation: %v", err)
	}
	opts := db.ParseConversationOptions(updated.ConversationOptions)
	if len(opts.EndOfTurnHooks) != 1 {
		t.Fatalf("len(EndOfTurnHooks) = %d, want 1", len(opts.EndOfTurnHooks))
	}
	if opts.EndOfTurnHooks[0].URL != "http://notify.int.exe.xyz/" {
		t.Fatalf("hook URL = %q", opts.EndOfTurnHooks[0].URL)
	}
}
