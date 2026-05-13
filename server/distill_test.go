package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func TestStartNewGenerationFiltersContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		h.NewConversation("old context", "")
		h.WaitResponse()
		ctx := context.Background()
		convID := h.convID

		oldMsgs, err := h.db.ListMessagesForContext(ctx, convID)
		if err != nil {
			t.Fatalf("failed to list old context: %v", err)
		}
		if len(oldMsgs) == 0 {
			t.Fatal("expected old generation messages in context before generation bump")
		}

		conversation, err := h.server.startNewGeneration(ctx, convID)
		if err != nil {
			t.Fatalf("failed to start new generation: %v", err)
		}
		if conversation.CurrentGeneration != 2 {
			t.Fatalf("expected current_generation=2, got %d", conversation.CurrentGeneration)
		}

		afterBump, err := h.db.ListMessagesForContext(ctx, convID)
		if err != nil {
			t.Fatalf("failed to list context after bump: %v", err)
		}
		if len(afterBump) != 1 {
			t.Fatalf("expected 1 generation 2 message (system prompt), got %d", len(afterBump))
		}
		if afterBump[0].Type != string(db.MessageTypeSystem) {
			t.Fatalf("expected new generation context to start with a system prompt, got type %q", afterBump[0].Type)
		}
		if afterBump[0].Generation != 2 {
			t.Fatalf("expected new system prompt at generation 2, got %d", afterBump[0].Generation)
		}

		_, err = h.db.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: convID,
			Type:           db.MessageTypeUser,
			LLMData: llm.Message{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{{Type: llm.ContentTypeText, Text: "new context"}},
			},
		})
		if err != nil {
			t.Fatalf("failed to create new generation message: %v", err)
		}

		newMsgs, err := h.db.ListMessagesForContext(ctx, convID)
		if err != nil {
			t.Fatalf("failed to list new context: %v", err)
		}
		if len(newMsgs) != 2 {
			t.Fatalf("expected system prompt + new user message in context, got %d messages", len(newMsgs))
		}
		for _, msg := range newMsgs {
			if msg.Generation != 2 {
				t.Fatalf("expected only generation 2 messages, got %+v", msg)
			}
		}
	})
}

// TestStartNewGenerationPreservesSlug verifies that starting a new generation
// does NOT cause the conversation's slug to be regenerated/overwritten.
// The slug is part of the conversation's identity (URL etc.) and should be
// stable across compaction.
func TestStartNewGenerationPreservesSlug(t *testing.T) {
	h := NewTestHarness(t)
	h.NewConversation("first message", "")
	h.WaitResponse()
	ctx := context.Background()
	convID := h.convID

	// Pin a known slug so we can detect any overwrite. Real first-message
	// flow generates one asynchronously, but we want a deterministic value.
	pinned := "pinned-slug"
	if _, err := h.db.UpdateConversationSlug(ctx, convID, pinned); err != nil {
		t.Fatalf("failed to set slug: %v", err)
	}

	// Bump generation, as the UI "compact" / "new generation" button does.
	if _, err := h.server.startNewGeneration(ctx, convID); err != nil {
		t.Fatalf("startNewGeneration: %v", err)
	}

	// Send a message after the generation bump. The handler will see this
	// as a "first message" (hasConversationEvents was cleared by ResetLoop)
	// and kick off async slug generation. The slug must not change.
	h.Chat("new gen first message")
	h.WaitResponse()

	// Poll briefly to give the async slug goroutine a chance to (incorrectly)
	// overwrite the slug. The slug-generation goroutine has a 15s timeout and
	// runs asynchronously; polling is the same pattern used elsewhere here.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fresh, err := h.db.GetConversationByID(ctx, convID)
		if err != nil {
			t.Fatalf("GetConversationByID: %v", err)
		}
		if fresh.Slug != nil && *fresh.Slug != pinned {
			t.Fatalf("slug after new generation = %q, want %q (slug must be preserved across generations)", *fresh.Slug, pinned)
		}
		time.Sleep(20 * time.Millisecond)
	}

	fresh, err := h.db.GetConversationByID(ctx, convID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if fresh.Slug == nil || *fresh.Slug != pinned {
		got := "<nil>"
		if fresh.Slug != nil {
			got = *fresh.Slug
		}
		t.Errorf("slug after new generation = %q, want %q", got, pinned)
	}
}

func TestChatDuringDistillationQueuesEvenWithoutClientQueueFlag(t *testing.T) {
	h := NewTestHarness(t)
	h.NewConversation("before distill", "")
	h.WaitResponse()
	ctx := context.Background()

	manager, err := h.server.getOrCreateConversationManager(ctx, h.convID, "")
	if err != nil {
		t.Fatalf("failed to get manager: %v", err)
	}
	manager.SetDistilling(true)
	defer manager.SetDistilling(false)

	body, err := json.Marshal(ChatRequest{Message: "first message after distill click", Model: "predictable"})
	if err != nil {
		t.Fatalf("marshal chat request: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/conversation/"+h.convID+"/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.server.handleChatConversation(w, req, h.convID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "queued") {
		t.Fatalf("expected queued response, got %s", w.Body.String())
	}

	messages, err := h.db.ListMessages(ctx, h.convID)
	if err != nil {
		t.Fatalf("failed to list messages: %v", err)
	}
	var queued *generated.Message
	for i := range messages {
		msg := messages[i]
		if msg.Type != string(db.MessageTypeUser) || msg.UserData == nil {
			continue
		}
		var userData map[string]bool
		if err := json.Unmarshal([]byte(*msg.UserData), &userData); err != nil {
			continue
		}
		if userData["queued"] {
			queued = &msg
		}
	}
	if queued == nil {
		t.Fatal("expected non-queue chat during distillation to be recorded as queued")
	}
	if !queued.ExcludedFromContext {
		t.Fatal("expected queued message excluded from context until distillation finishes")
	}
}

// TestDistillNewGenerationResetsContextWindow verifies that after a
// distill-into-new-generation, the reported context window size is calculated
// only from the new generation. Otherwise the token bar would continue to
// display the previous generation's (much larger) usage until the next
// message round-trip.
func TestDistillNewGenerationResetsContextWindow(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)

		// Build up some context in the source conversation.
		h.NewConversation("echo hello world", "")
		h.WaitResponse()
		synctest.Wait()
		h.Chat("echo another message")
		h.WaitResponse()
		synctest.Wait()
		sourceConvID := h.convID

		beforeSize := h.GetContextWindowSize()
		if beforeSize == 0 {
			t.Fatal("expected non-zero context window before distill")
		}

		// Distill into a new generation of the same conversation.
		reqBody := DistillNewGenerationRequest{
			SourceConversationID: sourceConvID,
			Model:                "predictable",
		}
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/api/conversations/distill-new-generation", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.server.handleDistillNewGeneration(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
		}

		waitForConversationDistillingToClear(t, h.server, sourceConvID)

		// The distilled message itself is recorded with empty usage, so the
		// context window for the new generation should be 0 — not the prior
		// generation's value.
		afterSize := h.GetContextWindowSize()
		if afterSize != 0 {
			t.Errorf("context window after distill-new-generation = %d, want 0 (prior gen=%d)", afterSize, beforeSize)
		}
	})
}

func waitForConversationDistillingToClear(t *testing.T, server *Server, convID string) {
	t.Helper()

	server.mu.Lock()
	manager, ok := server.activeConversations[convID]
	server.mu.Unlock()
	if !ok {
		t.Fatalf("expected active conversation manager for %s", convID)
	}

	synctest.Wait()

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.distilling {
		t.Fatalf("expected distilling=false for %s after synctest wait", convID)
	}
}

func stopActiveConversationLoops(server *Server) {
	server.mu.Lock()
	managers := make([]*ConversationManager, 0, len(server.activeConversations))
	for _, manager := range server.activeConversations {
		managers = append(managers, manager)
	}
	server.mu.Unlock()

	for _, manager := range managers {
		manager.stopLoop()
	}
}
