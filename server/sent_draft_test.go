package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// startedWithText creates a started conversation whose composer has saved
// text, and returns it with the stamp a client synced to that text sends.
func startedWithText(t *testing.T, server *Server, database *db.DB, text string) (*generated.Conversation, string) {
	t.Helper()
	model := "test-model"
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if w := draftPut(t, server, conv.ConversationID, map[string]string{"draft": text}); w.Code != http.StatusOK {
		t.Fatalf("save text: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got := reloadConversation(t, database, conv.ConversationID)
	if got.DraftUpdatedAt == nil {
		t.Fatalf("saving text left draft_updated_at unset")
	}
	return got, got.DraftUpdatedAt.Format(time.RFC3339Nano)
}

func reloadConversation(t *testing.T, database *db.DB, id string) *generated.Conversation {
	t.Helper()
	got, err := database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	return got
}

func hasUserMessage(t *testing.T, database *db.DB, id string) bool {
	t.Helper()
	for _, m := range listMessages(t, database, id) {
		if m.Type == string(db.MessageTypeUser) {
			return true
		}
	}
	return false
}

// A send from the composer clears the text it was composed from, in the
// same write that records the message, whether it starts a turn, is queued,
// or is a command that records no user message.
func TestChatClearsSentDraft(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		req  ChatRequest
	}{
		{"turn", ChatRequest{Message: "echo: hi"}},
		{"queued", ChatRequest{Message: "echo: hi", Queue: true}},
		{"command", ChatRequest{Message: "/model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, database, _ := newTestServer(t)
			conv, stamp := startedWithText(t, server, database, tc.req.Message)
			tc.req.DraftUpdatedAt = &stamp
			if w := chatPost(t, server, conv.ConversationID, tc.req); w.Code != http.StatusAccepted {
				t.Fatalf("send: expected 202, got %d: %s", w.Code, w.Body.String())
			}
			got := reloadConversation(t, database, conv.ConversationID)
			if got.Draft != "" {
				t.Fatalf("sent text not cleared: %q", got.Draft)
			}
			if got.IsDraft {
				t.Fatalf("started conversation became a draft")
			}
			if got.DraftUpdatedAt == nil || got.DraftUpdatedAt.Before(*conv.DraftUpdatedAt) {
				t.Fatalf("clear did not stamp draft_updated_at: %v", got.DraftUpdatedAt)
			}
		})
	}
}

// The message (recorded, or queued) and the cleared text land in one
// write: no reader sees the message while the text it came from still
// waits to be sent. These call the manager directly, so the handler's
// after-the-fact clear for commands doesn't run.
func TestSentDraftClearsWithMessage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		send func(ctx context.Context, m *ConversationManager, service llm.Service) error
	}{
		{"accept", func(ctx context.Context, m *ConversationManager, service llm.Service) error {
			_, err := m.AcceptUserMessage(ctx, service, "test-model", llm.UserStringMessage("echo: hi"))
			return err
		}},
		{"queue", func(ctx context.Context, m *ConversationManager, _ llm.Service) error {
			return m.QueueMessage(ctx, "test-model", llm.UserStringMessage("echo: hi"))
		}},
		{"transcription", func(ctx context.Context, m *ConversationManager, _ llm.Service) error {
			_, err := m.QueueTranscription(ctx, db.QueuedMessage{
				ID: "audio", Kind: db.QueuedMessageKindTranscription, State: db.QueuedMessageStateWorking,
				CreatedAt: time.Now(), Model: "test-model",
				Transcription: &db.QueuedTranscription{MediaPath: "/unused/audio.webm"},
			})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, database, _ := newTestServer(t)
			conv, _ := startedWithText(t, server, database, "echo: hi")
			service, err := server.llmManager.GetService("test-model")
			if err != nil {
				t.Fatal(err)
			}
			manager, err := server.getOrCreateConversationManager(t.Context(), conv.ConversationID, "")
			if err != nil {
				t.Fatal(err)
			}
			ctx, _ := contextWithSentDraft(t.Context(), conv.ConversationID, db.SentDraft{At: conv.DraftUpdatedAt})
			if err := tc.send(ctx, manager, service); err != nil {
				t.Fatalf("send: %v", err)
			}
			if got := reloadConversation(t, database, conv.ConversationID); got.Draft != "" {
				t.Fatalf("sent text not cleared: %q", got.Draft)
			}
		})
	}
}

// Only the text the message was composed from is cleared: a send that
// names an older version (another client typed since), names none, or
// omits the field (a push reply, the CLI, an agent) leaves it.
func TestChatKeepsDraftItDidNotSend(t *testing.T) {
	t.Parallel()
	older := "2001-02-03T04:05:06Z"
	empty := ""
	for _, tc := range []struct {
		name  string
		stamp *string
	}{
		{"older", &older},
		{"none", &empty},
		{"omitted", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, database, _ := newTestServer(t)
			conv, _ := startedWithText(t, server, database, "typed elsewhere")
			req := ChatRequest{Message: "echo: reply", DraftUpdatedAt: tc.stamp}
			if w := chatPost(t, server, conv.ConversationID, req); w.Code != http.StatusAccepted {
				t.Fatalf("send: expected 202, got %d: %s", w.Code, w.Body.String())
			}
			got := reloadConversation(t, database, conv.ConversationID)
			if got.Draft != "typed elsewhere" || !got.DraftUpdatedAt.Equal(*conv.DraftUpdatedAt) {
				t.Fatalf("unsent text changed: %q at %v", got.Draft, got.DraftUpdatedAt)
			}
		})
	}
}

func TestChatRejectsBadDraftUpdatedAt(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	conv, _ := startedWithText(t, server, database, "text")
	bad := "yesterday"
	w := chatPost(t, server, conv.ConversationID, ChatRequest{Message: "echo: hi", DraftUpdatedAt: &bad})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if hasUserMessage(t, database, conv.ConversationID) {
		t.Fatalf("rejected send recorded a message")
	}
}

// A draft's promoting send clears its text as before unless the sender
// says the message consumed other text: a recording sent to a draft
// leaves the typed text for the next message.
func TestPromoteDraftClearsOnlySentText(t *testing.T) {
	t.Parallel()
	empty := ""
	older := "2001-02-03T04:05:06Z"
	for _, tc := range []struct {
		name  string
		stamp func(conv *generated.Conversation) *string
		want  string
	}{
		{"omitted", func(*generated.Conversation) *string { return nil }, ""},
		{"current", func(c *generated.Conversation) *string {
			s := c.DraftUpdatedAt.Format(time.RFC3339Nano)
			return &s
		}, ""},
		{"none", func(*generated.Conversation) *string { return &empty }, "typed"},
		{"older", func(*generated.Conversation) *string { return &older }, "typed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, database, _ := newTestServer(t)
			model := "test-model"
			draft, err := database.CreateDraftConversation(t.Context(), nil, &model, db.ConversationOptions{}, "typed")
			if err != nil {
				t.Fatal(err)
			}
			if draft.DraftUpdatedAt == nil {
				t.Fatalf("new draft has no draft_updated_at")
			}
			req := ChatRequest{Message: "echo: other", DraftUpdatedAt: tc.stamp(draft)}
			if w := chatPost(t, server, draft.ConversationID, req); w.Code != http.StatusAccepted {
				t.Fatalf("send: expected 202, got %d: %s", w.Code, w.Body.String())
			}
			got := reloadConversation(t, database, draft.ConversationID)
			if got.IsDraft || got.Draft != tc.want {
				t.Fatalf("after promote: is_draft=%v draft=%q, want draft %q", got.IsDraft, got.Draft, tc.want)
			}
		})
	}
}

// A send takes the text it consumed once, for its own conversation: a
// later write sharing its context (a subagent, a second message) clears
// nothing.
func TestSentDraftClaimTakenOnce(t *testing.T) {
	t.Parallel()
	at := time.Now()
	ctx, claim := contextWithSentDraft(t.Context(), "c1", db.SentDraft{At: &at})
	if got := takeSentDraft(ctx, "c2"); got != (db.SentDraft{}) {
		t.Fatalf("another conversation took the claim: %+v", got)
	}
	if got := takeSentDraft(ctx, "c1"); got.At != &at {
		t.Fatalf("first take: %+v", got)
	}
	if got := claim.take("c1"); got != (db.SentDraft{}) {
		t.Fatalf("second take: %+v", got)
	}
	if got := takeSentDraft(t.Context(), "c1"); got != (db.SentDraft{}) {
		t.Fatalf("context without a claim: %+v", got)
	}
}

// A promoting send that fails before its message is recorded leaves the
// text it would have consumed.
func TestFailedPromotingSendKeepsText(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	hook := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(filepath.Join(server.hooksDir, hookChatMessage), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	model := "test-model"
	draft, err := database.CreateDraftConversation(t.Context(), nil, &model, db.ConversationOptions{}, "typed")
	if err != nil {
		t.Fatal(err)
	}
	if w := chatPost(t, server, draft.ConversationID, ChatRequest{Message: "typed"}); w.Code != http.StatusInternalServerError {
		t.Fatalf("send: expected 500 from the hook, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadConversation(t, database, draft.ConversationID); got.Draft != "typed" {
		t.Fatalf("failed send lost the text: is_draft=%v draft=%q", got.IsDraft, got.Draft)
	}
}

// A send that doesn't name what it consumed takes the text its promotion
// found, not text saved after it while the send was still on its way.
func TestLegacyPromotingSendKeepsLaterText(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	dir := t.TempDir()
	held, release := filepath.Join(dir, "held"), filepath.Join(dir, "release")
	for _, fifo := range []string{held, release} {
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	hook := fmt.Sprintf("#!/bin/sh\ncat >/dev/null\necho held > %q\nread x < %q\nprintf '%%s\\n' '{\"message\":\"echo: typed\"}'\n", held, release)
	if err := os.WriteFile(filepath.Join(server.hooksDir, hookChatMessage), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	model := "test-model"
	draft, err := database.CreateDraftConversation(t.Context(), nil, &model, db.ConversationOptions{}, "typed")
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan *httptest.ResponseRecorder)
	go func() {
		req := httptest.NewRequest("POST", "/api/conversation/"+draft.ConversationID+"/chat", strings.NewReader(`{"message":"typed"}`))
		w := httptest.NewRecorder()
		server.handleChatConversation(w, req, draft.ConversationID)
		sent <- w
	}()
	// The hook runs after the promotion.
	if _, err := os.ReadFile(held); err != nil {
		t.Fatal(err)
	}
	if w := draftPut(t, server, draft.ConversationID, map[string]string{"draft": "typed after"}); w.Code != http.StatusOK {
		t.Fatalf("save text: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if err := os.WriteFile(release, []byte("go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := <-sent; w.Code != http.StatusAccepted {
		t.Fatalf("send: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadConversation(t, database, draft.ConversationID); got.Draft != "typed after" {
		t.Fatalf("send cleared text saved after it promoted the draft: %q", got.Draft)
	}
}
