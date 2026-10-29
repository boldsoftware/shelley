package server

import (
	"context"
	"sync"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/server/notifications"
)

// recordingChannel is a notifications.Channel that records every event it
// receives, so tests can assert whether an end-of-turn notification fired.
type recordingChannel struct {
	mu     sync.Mutex
	events []notifications.Event
}

func (c *recordingChannel) Name() string { return "recording" }

func (c *recordingChannel) Send(ctx context.Context, event notifications.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return nil
}

func (c *recordingChannel) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func TestPublishConversationStateHonorsDisableNotifications(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		opts    db.ConversationOptions
		wantHit bool
	}{
		{"default notifies", db.ConversationOptions{}, true},
		{"disabled suppresses", db.ConversationOptions{DisableNotifications: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, database, _ := newTestServer(t)
			ch := &recordingChannel{}
			server.RegisterNotificationChannel(ch)

			conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, tc.opts)
			if err != nil {
				t.Fatalf("failed to create conversation: %v", err)
			}

			// Simulate the agent finishing a turn.
			server.publishConversationState(ConversationState{
				ConversationID: conversation.ConversationID,
				Working:        false,
				Model:          "predictable",
			})

			got := ch.count() > 0
			if got != tc.wantHit {
				t.Fatalf("notification fired = %v, want %v (events=%d)", got, tc.wantHit, ch.count())
			}
		})
	}
}

func TestPublishConversationStateWaitsForDelegatedWork(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		setup   func(t *testing.T, s *Server, database *db.DB, conversationID string)
		wantHit bool
	}{
		{"idle subagent notifies", func(t *testing.T, s *Server, database *db.DB, id string) {
			if _, err := database.CreateSubagentConversation(t.Context(), "helper", id, nil); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"working subagent suppresses", func(t *testing.T, s *Server, database *db.DB, id string) {
			child, err := database.CreateSubagentConversation(t.Context(), "helper", id, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.SetConversationAgentWorking(t.Context(), child.ConversationID, true); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"running background job suppresses", func(t *testing.T, s *Server, database *db.DB, id string) {
			if err := s.recordBackgroundJob(t.Context(), claudetool.BackgroundJob{ID: "job1", ConversationID: id}); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"exited background job notifies", func(t *testing.T, s *Server, database *db.DB, id string) {
			if err := s.recordBackgroundJob(t.Context(), claudetool.BackgroundJob{ID: "job1", ConversationID: id}); err != nil {
				t.Fatal(err)
			}
			if err := database.QueriesTx(t.Context(), func(q *generated.Queries) error {
				_, err := q.MarkBackgroundJobExited(t.Context(), "job1")
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, database, _ := newTestServer(t)
			ch := &recordingChannel{}
			server.RegisterNotificationChannel(ch)

			conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
			if err != nil {
				t.Fatalf("failed to create conversation: %v", err)
			}
			tc.setup(t, server, database, conversation.ConversationID)

			server.publishConversationState(ConversationState{
				ConversationID: conversation.ConversationID,
				Working:        false,
				Model:          "predictable",
			})

			got := ch.count() > 0
			if got != tc.wantHit {
				t.Fatalf("notification fired = %v, want %v (events=%d)", got, tc.wantHit, ch.count())
			}
		})
	}
}

func TestPublishConversationStateSkipsAssistant(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ch := &recordingChannel{}
	server.RegisterNotificationChannel(ch)

	db.EnableWorkspaceMode(t, server.db)
	assistantID, err := server.ensureWorkspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}

	server.publishConversationState(ConversationState{ConversationID: assistantID, Model: "predictable"})
	if n := ch.count(); n != 0 {
		t.Fatalf("Assistant turn end notified %d times", n)
	}
	server.publishConversationState(ConversationState{ConversationID: conv.ConversationID, Model: "predictable"})
	if n := ch.count(); n != 1 {
		t.Fatalf("project conversation turn end notified %d times, want 1", n)
	}
}
