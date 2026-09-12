package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// makeForkTestMessage builds a minimal user message with the given text.
func makeForkTestMessage(text string) llm.Message {
	return llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: text}},
	}
}

// seedForkConversation creates a conversation and records three user messages,
// returning the conversation ID and the recorded messages in order.
func seedForkConversation(t *testing.T, database *db.DB) (string, []generated.Message) {
	t.Helper()
	ctx := t.Context()
	model := "predictable"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	for _, text := range []string{"first", "second", "third"} {
		if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           db.MessageTypeUser,
			LLMData:        makeForkTestMessage(text),
		}); err != nil {
			t.Fatalf("create message %q: %v", text, err)
		}
	}
	msgs, err := database.ListMessages(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	return conv.ConversationID, msgs
}

// TestForkConversationCopiesUpToCutoff verifies the DB-level fork copies only
// messages up to and including the cutoff sequence_id into a fresh conversation.
func TestForkConversationCopiesUpToCutoff(t *testing.T) {
	t.Parallel()
	_, database, _ := newTestServer(t)
	ctx := t.Context()
	sourceID, msgs := seedForkConversation(t, database)
	source, err := database.UpdateConversationTags(ctx, sourceID, []string{"bug", "cheap"})
	if err != nil {
		t.Fatalf("tag source: %v", err)
	}

	forked, err := database.ForkConversation(ctx, sourceID, msgs[1].SequenceID)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if forked.ConversationID == sourceID {
		t.Fatal("forked conversation should have a new id")
	}
	if forked.ParentConversationID != nil {
		t.Fatal("forked conversation should be top-level (no parent)")
	}
	if forked.Tags != source.Tags {
		t.Fatalf("forked tags = %s, want %s", forked.Tags, source.Tags)
	}

	copied, err := database.ListMessages(ctx, forked.ConversationID)
	if err != nil {
		t.Fatalf("list forked messages: %v", err)
	}
	if len(copied) != 2 {
		t.Fatalf("expected 2 copied messages, got %d", len(copied))
	}
	srcMsgs, err := database.ListMessages(ctx, sourceID)
	if err != nil {
		t.Fatalf("list source messages: %v", err)
	}
	if len(srcMsgs) != 3 {
		t.Fatalf("source should still have 3 messages, got %d", len(srcMsgs))
	}
	if copied[0].MessageID == msgs[0].MessageID {
		t.Fatal("copied message should have a new message_id")
	}
	// Each copy records the source message it came from so usage data can be
	// de-duplicated (forked copies carry identical usage values and would
	// otherwise double-count). Source messages have no provenance.
	for i, c := range copied {
		if c.ForkedFromMessageID == nil {
			t.Fatalf("copied message %d should record forked_from_message_id", i)
		}
		if *c.ForkedFromMessageID != msgs[i].MessageID {
			t.Fatalf("copied message %d forked_from = %q, want %q", i, *c.ForkedFromMessageID, msgs[i].MessageID)
		}
	}
	for i, m := range srcMsgs {
		if m.ForkedFromMessageID != nil {
			t.Fatalf("source message %d should have nil forked_from_message_id, got %q", i, *m.ForkedFromMessageID)
		}
	}
}

// TestHandleForkConversationByMessageID drives the HTTP handler and confirms it
// returns the new conversation and copies the right prefix.
func TestHandleForkConversationByMessageID(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := t.Context()
	sourceID, msgs := seedForkConversation(t, database)
	source, err := database.UpdateConversationTags(ctx, sourceID, []string{"bug", "cheap"})
	if err != nil {
		t.Fatalf("tag source: %v", err)
	}

	body, _ := json.Marshal(ForkRequest{MessageID: msgs[1].MessageID})
	req := httptest.NewRequest("POST", "/api/conversation/"+sourceID+"/fork", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleForkConversation(w, req, sourceID)

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var forked generated.Conversation
	if err := json.Unmarshal(w.Body.Bytes(), &forked); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if forked.ConversationID == sourceID || forked.ConversationID == "" {
		t.Fatalf("unexpected forked id %q", forked.ConversationID)
	}
	if forked.Slug == nil || *forked.Slug == "" {
		t.Fatal("forked conversation should have a slug")
	}
	if forked.Tags != source.Tags {
		t.Fatalf("forked tags = %s, want %s", forked.Tags, source.Tags)
	}
	copied, err := database.ListMessages(ctx, forked.ConversationID)
	if err != nil {
		t.Fatalf("list forked messages: %v", err)
	}
	if len(copied) != 2 {
		t.Fatalf("expected 2 copied messages, got %d", len(copied))
	}
}

// TestHandleForkConversationDefaultsToWholeConversation confirms that a fork
// request with no cutoff copies the entire conversation.
func TestHandleForkConversationDefaultsToWholeConversation(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := t.Context()
	sourceID, _ := seedForkConversation(t, database)

	req := httptest.NewRequest("POST", "/api/conversation/"+sourceID+"/fork", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleForkConversation(w, req, sourceID)

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var forked generated.Conversation
	if err := json.Unmarshal(w.Body.Bytes(), &forked); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	copied, err := database.ListMessages(ctx, forked.ConversationID)
	if err != nil {
		t.Fatalf("list forked messages: %v", err)
	}
	if len(copied) != 3 {
		t.Fatalf("expected 3 copied messages, got %d", len(copied))
	}
}

// TestForkConversationOnlyCopiesCurrentGeneration verifies that forking copies
// only the source's current generation, renumbered to generation 1, and that
// the forked conversation starts at generation 1.
func TestForkConversationOnlyCopiesCurrentGeneration(t *testing.T) {
	t.Parallel()
	_, database, _ := newTestServer(t)
	ctx := t.Context()
	sourceID, _ := seedForkConversation(t, database)

	// Bump the source to generation 2 and add a message there.
	if err := database.QueriesTx(ctx, func(q *generated.Queries) error {
		_, err := q.IncrementConversationGeneration(ctx, sourceID)
		return err
	}); err != nil {
		t.Fatalf("increment generation: %v", err)
	}
	if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: sourceID,
		Type:           db.MessageTypeUser,
		LLMData:        makeForkTestMessage("gen2 message"),
	}); err != nil {
		t.Fatalf("create gen2 message: %v", err)
	}

	// Fork the whole conversation.
	latest, err := database.GetLatestActionableMessage(ctx, sourceID)
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	forked, err := database.ForkConversation(ctx, sourceID, latest.SequenceID)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	if forked.CurrentGeneration != 1 {
		t.Fatalf("forked conversation should start at generation 1, got %d", forked.CurrentGeneration)
	}

	copied, err := database.ListMessages(ctx, forked.ConversationID)
	if err != nil {
		t.Fatalf("list forked messages: %v", err)
	}
	// Only the single generation-2 message should have been copied.
	if len(copied) != 1 {
		t.Fatalf("expected 1 copied message (current generation only), got %d", len(copied))
	}
	for _, m := range copied {
		if m.Generation != 1 {
			t.Fatalf("copied message generation should be 1, got %d", m.Generation)
		}
	}
}

// TestForkConversationAtOlderGenerationMessage verifies that forking at a
// message from a PREVIOUS generation copies that generation up to the cutoff,
// rather than producing an empty fork (the fork point predates the current
// generation, so filtering by current_generation would match nothing).
func TestForkConversationAtOlderGenerationMessage(t *testing.T) {
	t.Parallel()
	_, database, _ := newTestServer(t)
	ctx := t.Context()
	sourceID, gen1Msgs := seedForkConversation(t, database)

	// Bump the source to generation 2 and add a message there.
	if err := database.QueriesTx(ctx, func(q *generated.Queries) error {
		_, err := q.IncrementConversationGeneration(ctx, sourceID)
		return err
	}); err != nil {
		t.Fatalf("increment generation: %v", err)
	}
	if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: sourceID,
		Type:           db.MessageTypeUser,
		LLMData:        makeForkTestMessage("gen2 message"),
	}); err != nil {
		t.Fatalf("create gen2 message: %v", err)
	}

	// Fork at the second generation-1 message.
	forked, err := database.ForkConversation(ctx, sourceID, gen1Msgs[1].SequenceID)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	copied, err := database.ListMessages(ctx, forked.ConversationID)
	if err != nil {
		t.Fatalf("list forked messages: %v", err)
	}
	if len(copied) != 2 {
		t.Fatalf("expected 2 copied generation-1 messages, got %d", len(copied))
	}
	for i, m := range copied {
		if m.Generation != 1 {
			t.Fatalf("copied message %d generation = %d, want 1", i, m.Generation)
		}
		if *m.ForkedFromMessageID != gen1Msgs[i].MessageID {
			t.Fatalf("copied message %d forked_from = %q, want %q", i, *m.ForkedFromMessageID, gen1Msgs[i].MessageID)
		}
	}
}
