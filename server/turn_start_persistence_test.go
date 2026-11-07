package server

import (
	"context"
	"errors"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func TestAcceptUserMessageRejectsUnpersistedTurn(t *testing.T) {
	t.Parallel()
	server, database, service := newTestServer(t)
	defer stopActiveConversationLoops(server)
	ctx := t.Context()

	conversation, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	manager, err := server.getOrCreateConversationManager(ctx, conversation.ConversationID, "")
	if err != nil {
		t.Fatalf("getOrCreateConversationManager: %v", err)
	}

	drainRequested := make(chan struct{}, 1)
	manager.onTurnStartRejected = func() { drainRequested <- struct{}{} }
	recordStarted := make(chan struct{})
	finishRecord := make(chan struct{})
	manager.mu.Lock()
	manager.recordTurnStartMessage = func(context.Context, llm.Message, llm.Usage, []llm.PurposedUsage) (*generated.Message, error) {
		close(recordStarted)
		<-finishRecord
		return nil, context.Canceled
	}
	manager.mu.Unlock()

	type result struct {
		first bool
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		first, err := manager.AcceptUserMessage(
			ctx,
			service,
			"predictable",
			llm.UserStringMessage("must be persisted before it runs"),
		)
		resultCh <- result{first: first, err: err}
	}()

	<-recordStarted
	reservedWorking := manager.IsAgentWorking()
	if _, err := database.AppendQueuedMessage(ctx, conversation.ConversationID, db.QueuedMessage{ID: "queued-meanwhile", Llm: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	close(finishRecord)
	got := <-resultCh
	if !reservedWorking {
		t.Fatal("turn was not reserved as working while its user message was being persisted")
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("AcceptUserMessage error = %v, want context.Canceled", got.err)
	}
	if got.first {
		t.Fatal("failed turn reported accepted")
	}
	if manager.IsAgentWorking() {
		t.Fatal("agent stayed working after the user message failed to persist")
	}
	select {
	case <-drainRequested:
	default:
		t.Fatal("pending batch was stranded when the failed turn returned to idle")
	}

	manager.mu.Lock()
	loopInstance := manager.loop
	manager.mu.Unlock()
	if loopInstance != nil {
		t.Fatal("failed first turn left an idle conversation loop installed")
	}
	if gotModel := manager.GetModel(); gotModel != "predictable" {
		t.Fatalf("failed turn lost hydrated model: got %q, want predictable", gotModel)
	}

	messages, err := database.ListMessages(ctx, conversation.ConversationID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, message := range messages {
		if message.Type == string(db.MessageTypeUser) || message.Type == string(db.MessageTypeAgent) {
			t.Fatalf("unpersisted turn still ran: type=%s sequence=%d", message.Type, message.SequenceID)
		}
	}
}

func TestAcceptUserMessageClearsStaleWorkingWithoutPriorLoop(t *testing.T) {
	t.Parallel()
	server, database, service := newTestServer(t)
	defer stopActiveConversationLoops(server)
	ctx := t.Context()

	model := "predictable"
	conversation, err := database.CreateConversation(ctx, nil, true, nil, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if err := database.SetConversationAgentWorking(ctx, conversation.ConversationID, true); err != nil {
		t.Fatalf("SetConversationAgentWorking: %v", err)
	}
	manager, err := server.getOrCreateConversationManager(ctx, conversation.ConversationID, "")
	if err != nil {
		t.Fatalf("getOrCreateConversationManager: %v", err)
	}
	if !manager.IsAgentWorking() {
		t.Fatal("hydration did not restore the stale working flag")
	}

	manager.mu.Lock()
	manager.recordTurnStartMessage = func(context.Context, llm.Message, llm.Usage, []llm.PurposedUsage) (*generated.Message, error) {
		return nil, context.Canceled
	}
	manager.mu.Unlock()

	if _, err := manager.AcceptUserMessage(ctx, service, model, llm.UserStringMessage("retry")); !errors.Is(err, context.Canceled) {
		t.Fatalf("AcceptUserMessage error = %v, want context.Canceled", err)
	}
	if manager.IsAgentWorking() {
		t.Fatal("failed send preserved working=true despite having no prior loop")
	}
	manager.mu.Lock()
	loopInstance := manager.loop
	manager.mu.Unlock()
	if loopInstance != nil {
		t.Fatal("failed send left an idle loop installed")
	}
	updated, err := database.GetConversationByID(ctx, conversation.ConversationID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if updated.AgentWorking {
		t.Fatal("failed send left persisted agent_working=true")
	}
}
