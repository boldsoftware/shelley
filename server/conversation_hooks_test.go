package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"shelley.exe.dev/db"
)

func TestRegisterConversationHookDedupes(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
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

	updated, err := database.GetConversationByID(context.Background(), conversation.ConversationID)
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

func TestEndOfTurnHooksPersistAfterRead(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{
		EndOfTurnHooks: []db.ConversationHook{{URL: "http://notify.int.exe.xyz/"}},
	})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}

	manager, err := server.getOrCreateConversationManager(context.Background(), conversation.ConversationID, "")
	if err != nil {
		t.Fatalf("failed to get manager: %v", err)
	}

	hooks, err := manager.EndOfTurnHooks(context.Background())
	if err != nil {
		t.Fatalf("failed to load hooks: %v", err)
	}
	if len(hooks) != 1 || hooks[0].URL != "http://notify.int.exe.xyz/" {
		t.Fatalf("hooks = %#v", hooks)
	}

	updated, err := database.GetConversationByID(context.Background(), conversation.ConversationID)
	if err != nil {
		t.Fatalf("failed to load conversation: %v", err)
	}
	opts := db.ParseConversationOptions(updated.ConversationOptions)
	if len(opts.EndOfTurnHooks) != 1 {
		t.Fatalf("expected hook to persist, got %#v", opts.EndOfTurnHooks)
	}
}
