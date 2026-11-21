package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func TestStartNewGenerationFiltersContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		h.NewConversation("old context", "")
		h.WaitResponse()
		ctx := t.Context()
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

// TestStartNewGenerationPreservesSlug verifies that the actual post-generation
// chat path cannot replace the conversation's existing slug.
func TestStartNewGenerationPreservesSlug(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)
		ctx := t.Context()
		pinned := "pinned-slug"
		model := "predictable"
		conversation, err := h.db.CreateConversation(ctx, &pinned, true, nil, &model, db.ConversationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		h.convID = conversation.ConversationID

		if _, err := h.server.startNewGeneration(ctx, h.convID); err != nil {
			t.Fatalf("startNewGeneration: %v", err)
		}
		h.Chat("new gen first message")
		h.WaitResponse()
		synctest.Wait()

		fresh, err := h.db.GetConversationByID(ctx, h.convID)
		if err != nil {
			t.Fatal(err)
		}
		if fresh.Slug == nil || *fresh.Slug != pinned {
			t.Fatalf("slug after new generation = %v, want %q", fresh.Slug, pinned)
		}
	})
}

// A client that goes away mid-request must not leave the generation bumped but
// unhydrated. The bump commits in its own transaction, so if hydration then
// inherits the (now cancelled) request context and fails, the conversation is
// left on a new generation with no system prompt and /clear returns 500. Seen
// in CI as `hydrate after generation bump: failed to store system prompt:
// context canceled`, which a loaded host makes easy to hit because it widens
// the window between the bump and the write.
func TestStartNewGenerationSurvivesClientDisconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		h.NewConversation("before clear", "")
		h.WaitResponse()
		convID := h.convID

		// A context already cancelled, standing in for a client that
		// disconnected between the bump and hydration.
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		conversation, err := h.server.startNewGeneration(ctx, convID)
		if err != nil {
			t.Fatalf("startNewGeneration with a cancelled client context: %v", err)
		}
		if conversation.CurrentGeneration != 2 {
			t.Fatalf("expected current_generation=2, got %d", conversation.CurrentGeneration)
		}

		// The new generation must have its system prompt, or the next turn runs
		// without one.
		msgs, err := h.db.ListMessagesForContext(t.Context(), convID)
		if err != nil {
			t.Fatalf("failed to list context: %v", err)
		}
		var system int
		for _, m := range msgs {
			if m.Type == string(db.MessageTypeSystem) {
				system++
			}
		}
		if system == 0 {
			t.Fatal("new generation has no system prompt after the client disconnected")
		}
	})
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

// /clear during a turn ends the turn in the old generation, and sends what
// was queued behind it in the new one.
func TestStartNewGenerationDuringTurn(t *testing.T) {
	server, database, held, conv := newBtwTest(t)
	requireBtwStatus(t, postBtwChat(t, server, conv.ConversationID, ChatRequest{Message: "long job", Model: "predictable"}), http.StatusAccepted)
	held.waitCall(t, "long job")
	requireBtwStatus(t, postBtwChat(t, server, conv.ConversationID, ChatRequest{Message: "queued", Model: "predictable", Queue: true}), http.StatusAccepted)
	if _, err := server.startNewGeneration(t.Context(), conv.ConversationID); err != nil {
		t.Fatal(err)
	}
	call := held.waitCall(t, "queued")
	if n := len(call.request.Messages); n != 1 {
		t.Errorf("queued message went out with %d messages of context, want just itself", n)
	}
	releaseAndWaitIdle(t, server, conv.ConversationID, call)
	requireTurnsEndOnce(t, database, conv.ConversationID, 2)
}

// A turn that starts after /clear checked that the agent is idle, here a
// queued message draining, makes /clear decline with a conflict rather than
// abandon the turn in the old generation.
func TestStartNewGenerationRefusesTurnStartedMeanwhile(t *testing.T) {
	server, database, held, conv := newBtwTest(t)
	manager, err := server.getOrCreateConversationManager(t.Context(), conv.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	// Hold the drain of a queued message where it starts its turn: holding
	// the lifecycle lock, with the agent not yet working.
	draining, release := make(chan struct{}), make(chan struct{})
	releaseDrain := sync.OnceFunc(func() { close(release) })
	defer releaseDrain()
	record := manager.recordDrainedQueued
	manager.recordDrainedQueued = func(ctx context.Context, qm db.QueuedMessage, messages []llm.Message) (int64, error) {
		close(draining)
		<-release
		return record(ctx, qm, messages)
	}
	requireBtwStatus(t, postBtwChat(t, server, conv.ConversationID, ChatRequest{Message: "drained", Model: "predictable", Queue: true}), http.StatusAccepted)
	<-draining

	w := httptest.NewRecorder()
	cleared := make(chan struct{})
	go func() {
		defer close(cleared)
		server.handleStartNewGeneration(w, httptest.NewRequest(http.MethodPost, "/", nil), conv.ConversationID)
	}()
	waitLockingIn(t, "(*ConversationManager).resetLoopAfter")
	releaseDrain()
	<-cleared
	if w.Code != http.StatusConflict {
		t.Errorf("/clear while the drained turn starts: %d %s, want 409", w.Code, strings.TrimSpace(w.Body.String()))
	}
	releaseAndWaitIdle(t, server, conv.ConversationID, held.waitCall(t, "drained"))
	requireTurnsEndOnce(t, database, conv.ConversationID, 1)
}

// A turn that ends on its own while /clear waits to stop it isn't ended a
// second time.
func TestStartNewGenerationAfterTurnEnds(t *testing.T) {
	server, database, held, conv := newBtwTest(t)
	requireBtwStatus(t, postBtwChat(t, server, conv.ConversationID, ChatRequest{Message: "job", Model: "predictable"}), http.StatusAccepted)
	call := held.waitCall(t, "job")
	server.mu.Lock()
	manager := server.activeConversations[conv.ConversationID]
	server.mu.Unlock()
	// Keep /clear from stopping the turn until the turn has ended.
	manager.loopLifecycleMu.Lock()
	unlock := sync.OnceFunc(manager.loopLifecycleMu.Unlock)
	defer unlock()
	defer call.Release()
	cleared := make(chan error, 1)
	go func() {
		_, err := server.startNewGeneration(context.Background(), conv.ConversationID)
		cleared <- err
	}()
	waitLockingIn(t, "(*ConversationManager).cancelConversation")
	releaseAndWaitIdle(t, server, conv.ConversationID, call)
	unlock()
	if err := <-cleared; err != nil {
		t.Fatal(err)
	}
	requireTurnsEndOnce(t, database, conv.ConversationID, 2)
}

// waitLockingIn waits for a goroutine to park acquiring a mutex directly in
// fn, a method or function of this package.
func waitLockingIn(t *testing.T, fn string) {
	t.Helper()
	locking := regexp.MustCompile(`\nsync\.\(\*Mutex\)\.Lock\([^\n]*\)\n\t[^\n]*\nshelley\.exe\.dev/server\.` + regexp.QuoteMeta(fn) + `\(`)
	buf := make([]byte, 1<<20)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		n := runtime.Stack(buf, true)
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(g, " [sync.Mutex.Lock") && locking.MatchString(g) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing parked on a lock in %s", fn)
		}
	}
}

// requireTurnsEndOnce checks that the conversation has the given number of
// generations, each starting with its system prompt, and that every turn
// ended exactly once.
func requireTurnsEndOnce(t *testing.T, database *db.DB, conversationID string, generations int64) {
	t.Helper()
	conv, err := database.GetConversationByID(t.Context(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.AgentWorking || conv.CurrentGeneration != generations {
		t.Errorf("working=%v generation=%d, want idle in generation %d", conv.AgentWorking, conv.CurrentGeneration, generations)
	}
	msgs := listMessages(t, database, conversationID)
	ends := -1 // ends of the current turn; -1 before the first
	for i, m := range msgs {
		if i == 0 || m.Generation != msgs[i-1].Generation {
			if m.Type != string(db.MessageTypeSystem) {
				t.Errorf("generation %d starts with a %s message", m.Generation, m.Type)
			}
		}
		switch {
		case m.Type == string(db.MessageTypeUser):
			if ends == 0 {
				t.Errorf("turn before message %d never ended", m.SequenceID)
			}
			ends = 0
		case isAgentEndOfTurn(&m):
			if ends++; ends > 1 {
				t.Errorf("message %d ends a turn that had already ended", m.SequenceID)
			}
		}
	}
	if ends == 0 {
		t.Errorf("last turn never ended")
	}
	if t.Failed() {
		for _, m := range msgs {
			t.Logf("gen=%d seq=%d type=%s end=%v", m.Generation, m.SequenceID, m.Type, isAgentEndOfTurn(&m))
		}
	}
}
