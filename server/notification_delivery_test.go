package server

import (
	"context"
	"testing"
	"testing/synctest"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/server/notifications"
)

type notificationChannelFunc func(context.Context, notifications.Event) error

func (f notificationChannelFunc) Name() string { return "test" }
func (f notificationChannelFunc) Send(ctx context.Context, event notifications.Event) error {
	return f(ctx, event)
}

func TestNotificationDeliveryDoesNotDelayCompletion(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s, database, _ := newTestServer(t)
		started := make(chan notifications.Event, 1)
		release := make(chan struct{})
		defer close(release)
		s.RegisterNotificationChannel(notificationChannelFunc(func(_ context.Context, event notifications.Event) error {
			started <- event
			<-release
			return nil
		}))
		conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           db.MessageTypeAgent,
			LLMData:        llm.Message{Role: llm.MessageRoleAssistant, Content: llm.TextContent("finished"), EndOfTurn: true},
		}); err != nil {
			t.Fatal(err)
		}
		next := s.streamPub.Subscribe(t.Context(), -1)
		published := make(chan struct{})
		go func() {
			s.publishConversationState(ConversationState{ConversationID: conv.ConversationID, Model: "predictable"})
			close(published)
		}()
		synctest.Wait()
		select {
		case <-published:
		default:
			t.Fatal("completion blocked on notification delivery")
		}
		stream, ok := next()
		if !ok || stream.NotificationEvent == nil || stream.NotificationEvent.ConversationID != conv.ConversationID ||
			stream.ConversationState == nil || stream.ConversationState.Working {
			t.Fatalf("completion event = %+v, ok=%v", stream, ok)
		}
		select {
		case event := <-started:
			payload := event.Payload.(notifications.AgentDonePayload)
			if event.ConversationID != conv.ConversationID || payload.FinalResponse != "finished" || payload.Model != "predictable" {
				t.Fatalf("delivered event = %+v", event)
			}
		default:
			t.Fatal("notification delivery never started")
		}
	})
}
