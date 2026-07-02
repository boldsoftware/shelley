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

// A draft carries conversation_options (e.g. thinking_level) chosen before the
// first message is sent. Promoting the draft via POST /chat must preserve
// those options; otherwise the promoted conversation loses its thinking_level
// and reasoning is silently disabled for adaptive models. See the
// "reasoning_level isn't persisted" report.
func TestPromoteDraftPreservesStoredThinkingLevel(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	opts := db.ConversationOptions{ThinkingLevel: "high"}
	draft, err := database.CreateDraftConversation(context.Background(), nil, nil, opts, "my draft")
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	id := draft.ConversationID

	// Promote via chat WITHOUT conversation_options: the stored options win.
	chatReq := ChatRequest{Message: "hello", Model: "predictable"}
	body, _ := json.Marshal(chatReq)
	promoteAndCheck(t, server, database, id, body, "high")
}

// The send that promotes a draft may itself carry conversation_options that
// reflect the user's current selection (they can toggle the thinking level
// after the draft was autosaved). Those send-time options must win, so a
// fresh-chat flow — autosaved draft born with empty options, then a send that
// carries thinking_level — persists the level rather than dropping it. This is
// the exact fresh-VM/fresh-chat repro from the bug report.
func TestPromoteDraftAppliesRequestThinkingLevel(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	// Draft born WITHOUT options, as the web UI's autosave createDraft does.
	draft, err := database.CreateDraftConversation(context.Background(), nil, nil, db.ConversationOptions{}, "my draft")
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	id := draft.ConversationID

	chatReq := ChatRequest{
		Message:             "hello",
		Model:               "predictable",
		ConversationOptions: &db.ConversationOptions{ThinkingLevel: "high"},
	}
	body, _ := json.Marshal(chatReq)
	promoteAndCheck(t, server, database, id, body, "high")
}

func promoteAndCheck(t *testing.T, server *Server, database *db.DB, id string, body []byte, want string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/conversation/"+id+"/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleChatConversation(w, req, id)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	got, err := database.GetConversationByID(context.Background(), id)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	parsed := db.ParseConversationOptions(got.ConversationOptions)
	if parsed.ThinkingLevel != want {
		t.Fatalf("thinking_level after promote: got %q, want %q (raw=%q)",
			parsed.ThinkingLevel, want, got.ConversationOptions)
	}
}
