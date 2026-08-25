package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

// TestPromotedDraftAnnouncesSystemPrompt pins the stream contract for draft
// promotion: a client watching the promotion must be told about EVERY message
// row the promotion created, including the system prompt at sequence 1.
//
// The system prompt is created lazily by Hydrate and deliberately does not
// broadcast or bump updated_at (see convo.go createSystemPrompt) — correct for
// ordinary hydration, but promotion makes it user-visible. The is_draft flip is
// announced before Hydrate runs, so a client that refetches on that signal sees
// an empty conversation; if sequence 1 is then never announced, the only row the
// client ever hears about is the user's message at sequence 2 and its history
// silently begins at 2.
//
// Asserting on the DB would be vacuous — the row is written either way. The
// assertion has to be on what a SUBSCRIBER receives, which is what regressed.
func TestPromotedDraftAnnouncesSystemPrompt(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := context.Background()

	model := "predictable"
	draft, err := database.CreateDraftConversation(ctx, nil, &model, db.ConversationOptions{}, "hello")
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	conversationID := draft.ConversationID
	if !draft.IsDraft {
		t.Fatal("conversation should start as a draft")
	}

	// Subscribe BEFORE the promoting send, the way a browser sitting on the
	// draft does. Registering the manager here also mirrors that: a client
	// viewing the draft already holds a stream.
	manager, err := server.getOrCreateConversationManager(ctx, conversationID, "")
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	next := manager.subpub.Subscribe(subCtx, -1)

	seen := make(chan int64, 32)
	go func() {
		for {
			data, ok := next()
			if !ok {
				return
			}
			for _, m := range data.Messages {
				if m.Type == string(db.MessageTypeSystem) {
					seen <- m.SequenceID
				}
			}
		}
	}()

	// Promote by sending the first message.
	body, _ := json.Marshal(ChatRequest{Message: "hello", Model: model})
	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleChatConversation(w, req, conversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("promoting send: status=%d body=%s", w.Code, w.Body.String())
	}

	select {
	case seq := <-seen:
		if seq != 1 {
			t.Fatalf("system prompt announced at sequence %d, want 1", seq)
		}
	case <-time.After(5 * time.Second):
		// Distinguish "never created" from "created but never announced" so a
		// failure points at the right layer.
		msgs, lerr := database.ListMessages(ctx, conversationID)
		if lerr != nil {
			t.Fatalf("system prompt was not announced to subscribers; listing messages failed: %v", lerr)
		}
		for _, m := range msgs {
			if m.Type == string(db.MessageTypeSystem) {
				t.Fatalf("system prompt exists in the DB at sequence %d but was never announced to subscribers: a client that refetched on the is_draft flip will render a history starting at sequence 2", m.SequenceID)
			}
		}
		t.Fatal("promotion created no system prompt at all")
	}
}
