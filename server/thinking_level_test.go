package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

func TestUpdateThinkingLevel(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	body, _ := json.Marshal(UpdateThinkingLevelRequest{ThinkingLevel: "high"})
	req := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleUpdateThinkingLevel(w, req, conversation.ConversationID)

	if w.Code != http.StatusOK {
		t.Fatalf("update thinking level: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	conv, err := database.GetConversationByID(context.Background(), conversation.ConversationID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	opts := db.ParseConversationOptions(conv.ConversationOptions)
	if opts.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want high", opts.ThinkingLevel)
	}
}

func TestUpdateThinkingLevelInvalid(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	body, _ := json.Marshal(UpdateThinkingLevelRequest{ThinkingLevel: "maximum"})
	req := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleUpdateThinkingLevel(w, req, conversation.ConversationID)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid thinking level: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateThinkingLevelNoOp(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{ThinkingLevel: "medium"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	body, _ := json.Marshal(UpdateThinkingLevelRequest{ThinkingLevel: "medium"})
	req := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleUpdateThinkingLevel(w, req, conversation.ConversationID)

	if w.Code != http.StatusOK {
		t.Fatalf("no-op thinking level: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["changed"] != false {
		t.Fatalf("changed = %v, want false", resp["changed"])
	}
}
