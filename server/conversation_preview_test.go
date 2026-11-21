package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// writeAgentMsg writes an agent message with the given content blocks.
func writeAgentMsg(t *testing.T, database *db.DB, convID string, content []llm.Content) {
	t.Helper()
	_, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: convID,
		Type:           db.MessageTypeAgent,
		LLMData: llm.Message{
			Role:    llm.MessageRoleAssistant,
			Content: content,
		},
	})
	if err != nil {
		t.Fatalf("CreateMessage(%s): %v", convID, err)
	}
}

func previewTextBlock(s string) llm.Content {
	return llm.Content{Type: llm.ContentTypeText, Text: s}
}

func previewToolBlock(name string) llm.Content {
	return llm.Content{Type: llm.ContentTypeToolUse, ToolName: name}
}

// TestConversationListPreview exercises the SQL-side preview extraction that
// the conversation-list query reads from the conversation_previews view: it
// must pick the most recent agent message that has a reply block, and
// within that message use the LAST non-empty reply block (the
// summary/conclusion in tool-calling turns). It also checks the tool-only
// fallback and that a preview carries a timestamp.
func TestConversationListPreview(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	ctx := t.Context()

	// Conversation A: last agent message mixes text + tool calls. The final
	// text block should win over the earlier one.
	convA, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writeAgentMsg(t, database, convA.ConversationID, []llm.Content{previewTextBlock("first answer")})
	writeAgentMsg(t, database, convA.ConversationID, []llm.Content{
		previewTextBlock("intermediate thought"),
		previewToolBlock("bash"),
		previewTextBlock("final summary"),
	})

	// Conversation B: the newest agent message is tool-only (no text), so the
	// preview must fall back to the previous message's text block.
	convB, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writeAgentMsg(t, database, convB.ConversationID, []llm.Content{previewTextBlock("earlier text")})
	writeAgentMsg(t, database, convB.ConversationID, []llm.Content{previewToolBlock("bash")})

	// Conversation C: only tool calls, ever — no preview at all.
	convC, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writeAgentMsg(t, database, convC.ConversationID, []llm.Content{previewToolBlock("bash")})

	// Conversation D: a very long agent reply. The preview is capped in SQL
	// (substr ..., 1, 300) so we don't haul multi-KB replies across the driver
	// for a one-line UI field.
	convD, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	longText := strings.Repeat("x", 5000)
	writeAgentMsg(t, database, convD.ConversationID, []llm.Content{previewTextBlock(longText)})

	// Drive the actual conversation-list path: the preview/preview_updated_at
	// columns are computed inside the list query itself, then copied onto each
	// ConversationWithState by decorateConversations. Reading the list back is
	// exactly what /api/conversations serves.
	list, err := srv.conversationListWithStateInternal(ctx, 100, 0, "", false, true)
	if err != nil {
		t.Fatalf("conversationListWithStateInternal: %v", err)
	}
	byID := map[string]ConversationWithState{}
	for _, cws := range list {
		byID[cws.ConversationID] = cws
	}

	if got := byID[convA.ConversationID].Preview; got != "final summary" {
		t.Errorf("convA preview = %q, want %q", got, "final summary")
	}
	if got := byID[convB.ConversationID].Preview; got != "earlier text" {
		t.Errorf("convB preview = %q, want %q", got, "earlier text")
	}
	if got := byID[convC.ConversationID].Preview; got != "" {
		t.Errorf("convC should have no preview, got %q", got)
	}
	if byID[convA.ConversationID].PreviewUpdatedAt == "" {
		t.Errorf("convA preview should carry an updatedAt timestamp")
	}
	// The preview message's max sequence_id is surfaced too.
	if byID[convA.ConversationID].MaxSequenceID == 0 {
		t.Errorf("convA should carry a non-zero max_sequence_id")
	}
	// convD's long preview is truncated to the SQL cap (300 chars) and still
	// carries a timestamp.
	if got := byID[convD.ConversationID].Preview; len(got) != 300 {
		t.Errorf("convD preview len = %d, want 300 (truncated)", len(got))
	}
	if byID[convD.ConversationID].PreviewUpdatedAt == "" {
		t.Errorf("convD preview should carry an updatedAt timestamp")
	}
}

// TestConversationListPatchStreamPreviewsMessageUser checks the live path: an
// agent reply sent with message_user, as the Assistant and channel
// conversations reply, reaches /api/stream2 subscribers as a preview patch.
func TestConversationListPatchStreamPreviewsMessageUser(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writeAgentMsg(t, database, conv.ConversationID, []llm.Content{previewTextBlock("plain reply")})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream2", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		srv.handleStream(rec, req)
		close(done)
	}()
	initial := waitForPatchEventAfter(t, rec, "")
	state := mustApplyPatch(t, []ConversationWithState{}, initial.Patch)
	if len(state) != 1 || state[0].Preview != "plain reply" {
		t.Fatalf("initial state = %+v, want one conversation previewing %q", state, "plain reply")
	}

	input, err := json.Marshal(claudetool.MessageUserInput{Text: "texted reply", ReplyTo: 3})
	if err != nil {
		t.Fatal(err)
	}
	writeAgentMsg(t, database, conv.ConversationID, []llm.Content{
		{Type: llm.ContentTypeToolUse, ToolName: claudetool.MessageUserName, ToolInput: input},
	})
	update := waitForPatchEventAfter(t, rec, initial.NewHash)
	state = mustApplyPatch(t, state, update.Patch)
	verifyHash(t, state, update.NewHash)
	if state[0].Preview != "texted reply" {
		t.Errorf("preview after message_user = %q, want %q (patch %s)", state[0].Preview, "texted reply", patchSummary(update.Patch))
	}
	cancel()
	<-done
}
