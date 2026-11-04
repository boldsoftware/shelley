package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
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

// delegate creates a subagent of parentID with a prompt from it, which the
// subagent owes a report on.
func delegate(t *testing.T, database *db.DB, parentID string) *generated.Conversation {
	t.Helper()
	child, err := database.CreateSubagentConversation(t.Context(), "helper", parentID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: child.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        llm.UserStringMessage("help"),
		UserData:       senderMessageUserData{SenderConversationID: parentID, SenderRelationship: senderRelationshipParent, Text: "help"},
	}); err != nil {
		t.Fatal(err)
	}
	return child
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
		// The job has exited but its notice, which starts the next turn, is
		// not queued yet.
		{"exited background job awaiting its notice suppresses", func(t *testing.T, s *Server, database *db.DB, id string) {
			if err := s.recordBackgroundJob(t.Context(), claudetool.BackgroundJob{ID: "job1", ConversationID: id}); err != nil {
				t.Fatal(err)
			}
			if err := database.QueriesTx(t.Context(), func(q *generated.Queries) error {
				_, err := q.MarkBackgroundJobExited(t.Context(), "job1")
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"reported background job notifies", func(t *testing.T, s *Server, database *db.DB, id string) {
			if err := s.recordBackgroundJob(t.Context(), claudetool.BackgroundJob{ID: "job1", ConversationID: id}); err != nil {
				t.Fatal(err)
			}
			if err := database.QueriesTx(t.Context(), func(q *generated.Queries) error {
				return q.MarkBackgroundJobNotified(t.Context(), "job1")
			}); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"queued message suppresses", func(t *testing.T, s *Server, database *db.DB, id string) {
			if _, err := s.getOrCreateConversationManager(t.Context(), id, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := database.AppendQueuedMessage(t.Context(), id, db.QueuedMessage{ID: "q1", Llm: []byte(`{"Role":0}`)}); err != nil {
				t.Fatal(err)
			}
		}, false},
		// Nothing loads it to drain the queue.
		{"queued message of an unloaded conversation notifies", func(t *testing.T, s *Server, database *db.DB, id string) {
			if _, err := database.AppendQueuedMessage(t.Context(), id, db.QueuedMessage{ID: "q1", Llm: []byte(`{"Role":0}`)}); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"transcription in progress suppresses", func(t *testing.T, s *Server, database *db.DB, id string) {
			qm := db.QueuedMessage{ID: "q1", Kind: db.QueuedMessageKindTranscription, State: db.QueuedMessageStateWorking}
			if _, err := database.AppendQueuedMessage(t.Context(), id, qm); err != nil {
				t.Fatal(err)
			}
		}, false},
		// A drained message already started the next turn.
		{"working again suppresses", func(t *testing.T, s *Server, database *db.DB, id string) {
			if err := database.SetConversationAgentWorking(t.Context(), id, true); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"failed transcription blocking the queue notifies", func(t *testing.T, s *Server, database *db.DB, id string) {
			qm := db.QueuedMessage{ID: "q1", Kind: db.QueuedMessageKindTranscription, State: db.QueuedMessageStateFailed}
			if _, err := database.AppendQueuedMessage(t.Context(), id, qm); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"idle subagent awaiting its own job suppresses", func(t *testing.T, s *Server, database *db.DB, id string) {
			child := delegate(t, database, id)
			if err := s.recordBackgroundJob(t.Context(), claudetool.BackgroundJob{ID: "job1", ConversationID: child.ConversationID}); err != nil {
				t.Fatal(err)
			}
		}, false},
		// It reported back, or was never asked: a job it keeps running, such
		// as a dev server, need not wake the parent.
		{"subagent owing no report with a job running notifies", func(t *testing.T, s *Server, database *db.DB, id string) {
			child, err := database.CreateSubagentConversation(t.Context(), "helper", id, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.recordBackgroundJob(t.Context(), claudetool.BackgroundJob{ID: "job1", ConversationID: child.ConversationID}); err != nil {
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

	setWorkspaceFlag(t, server, "true")
	assistantID := server.assistantID()
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

// A turn end held back for pending work is announced once that work runs out
// without starting another turn, and not when another turn started.
func TestOwedTurnEndIsAnnouncedOnceIdle(t *testing.T) {
	t.Parallel()

	markNotified := func(t *testing.T, database *db.DB, jobID string) {
		t.Helper()
		if err := database.QueriesTx(t.Context(), func(q *generated.Queries) error {
			return q.MarkBackgroundJobNotified(t.Context(), jobID)
		}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		// pend leaves work that holds back conversationID's turn end;
		// resolve ends it.
		pend, resolve func(t *testing.T, s *Server, database *db.DB, id string)
		want          int
	}{
		{
			name: "job notice answered before the job counted as delivered",
			pend: func(t *testing.T, s *Server, database *db.DB, id string) {
				if err := s.recordBackgroundJob(t.Context(), claudetool.BackgroundJob{ID: "job1", ConversationID: id}); err != nil {
					t.Fatal(err)
				}
			},
			resolve: func(t *testing.T, s *Server, database *db.DB, id string) {
				markNotified(t, database, "job1")
				s.settleTurnEnds(id)
			},
			want: 1,
		},
		{
			name: "subagent done with its job without reporting",
			pend: func(t *testing.T, s *Server, database *db.DB, id string) {
				child := delegate(t, database, id)
				if err := s.recordBackgroundJob(t.Context(), claudetool.BackgroundJob{ID: "job1", ConversationID: child.ConversationID}); err != nil {
					t.Fatal(err)
				}
			},
			resolve: func(t *testing.T, s *Server, database *db.DB, id string) {
				markNotified(t, database, "job1")
				children, err := database.GetSubagents(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				// The subagent's turn answering the notice ends.
				s.publishConversationState(ConversationState{ConversationID: children[0].ConversationID, Model: "predictable"})
			},
			want: 1,
		},
		{
			name: "transcription failed",
			pend: func(t *testing.T, s *Server, database *db.DB, id string) {
				qm := db.QueuedMessage{ID: "q1", Kind: db.QueuedMessageKindTranscription, State: db.QueuedMessageStateWorking, Transcription: &db.QueuedTranscription{MediaPath: "talk.m4a"}}
				if _, err := database.AppendQueuedMessage(t.Context(), id, qm); err != nil {
					t.Fatal(err)
				}
				s.transcriptionMu.Lock()
				s.transcriptionJobs["q1"] = transcriptionJob{parentID: id, attemptID: "a1", cancel: func() {}, done: make(chan struct{})}
				s.transcriptionMu.Unlock()
			},
			resolve: func(t *testing.T, s *Server, database *db.DB, id string) {
				s.failQueuedTranscription(id, "q1", "a1", nil, errors.New("no speech"))
			},
			want: 1,
		},
		{
			name: "queued message cannot start",
			pend: func(t *testing.T, s *Server, database *db.DB, id string) {
				// Any failure to start a turn will do.
				qm := db.QueuedMessage{ID: "q1", Llm: []byte(`"not a message"`)}
				if _, err := database.AppendQueuedMessage(t.Context(), id, qm); err != nil {
					t.Fatal(err)
				}
			},
			resolve: func(t *testing.T, s *Server, database *db.DB, id string) {
				cm, err := s.getOrCreateConversationManager(t.Context(), id, "")
				if err != nil {
					t.Fatal(err)
				}
				cm.drainQueueIfIdle(s)
			},
			want: 1,
		},
		{
			name: "queue cleared",
			pend: func(t *testing.T, s *Server, database *db.DB, id string) {
				if _, err := database.AppendQueuedMessage(t.Context(), id, db.QueuedMessage{ID: "q1", Llm: []byte(`{"Role":0}`)}); err != nil {
					t.Fatal(err)
				}
			},
			resolve: func(t *testing.T, s *Server, database *db.DB, id string) {
				if _, err := database.ClearQueuedMessages(t.Context(), id); err != nil {
					t.Fatal(err)
				}
				s.settleTurnEnds(id)
			},
			want: 1,
		},
		{
			name: "conversation unloaded",
			pend: func(t *testing.T, s *Server, database *db.DB, id string) {
				if _, err := database.AppendQueuedMessage(t.Context(), id, db.QueuedMessage{ID: "q1", Llm: []byte(`{"Role":0}`)}); err != nil {
					t.Fatal(err)
				}
			},
			resolve: func(t *testing.T, s *Server, database *db.DB, id string) {
				cm, err := s.getOrCreateConversationManager(t.Context(), id, "")
				if err != nil {
					t.Fatal(err)
				}
				cm.mu.Lock()
				cm.lastActivity = time.Now().Add(-time.Hour)
				cm.mu.Unlock()
				s.Cleanup()
			},
			want: 1,
		},
		{
			name: "queued message cancelled",
			pend: func(t *testing.T, s *Server, database *db.DB, id string) {
				if _, err := database.AppendQueuedMessage(t.Context(), id, db.QueuedMessage{ID: "q1", Llm: []byte(`{"Role":0}`)}); err != nil {
					t.Fatal(err)
				}
			},
			resolve: func(t *testing.T, s *Server, database *db.DB, id string) {
				w := httptest.NewRecorder()
				s.handleCancelQueued(w, httptest.NewRequest(http.MethodPost, "/api/conversation/"+id+"/cancel-queued?queued_id=q1", nil), id)
				if w.Code != http.StatusOK {
					t.Fatalf("cancel queued: %d %s", w.Code, w.Body.String())
				}
			},
			want: 1,
		},
		{
			name: "stopped",
			pend: func(t *testing.T, s *Server, database *db.DB, id string) {
				if _, err := database.AppendQueuedMessage(t.Context(), id, db.QueuedMessage{ID: "q1", Llm: []byte(`{"Role":0}`)}); err != nil {
					t.Fatal(err)
				}
			},
			resolve: func(t *testing.T, s *Server, database *db.DB, id string) {
				w := httptest.NewRecorder()
				s.handleCancelConversation(w, httptest.NewRequest(http.MethodPost, "/api/conversation/"+id+"/cancel", nil), id)
				if w.Code != http.StatusOK {
					t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
				}
			},
			want: 1,
		},
		{
			name: "another turn started",
			pend: func(t *testing.T, s *Server, database *db.DB, id string) {
				if err := s.recordBackgroundJob(t.Context(), claudetool.BackgroundJob{ID: "job1", ConversationID: id}); err != nil {
					t.Fatal(err)
				}
			},
			resolve: func(t *testing.T, s *Server, database *db.DB, id string) {
				s.publishConversationState(ConversationState{ConversationID: id, Working: true, Model: "predictable"})
				markNotified(t, database, "job1")
				s.settleTurnEnds(id)
			},
			want: 0,
		},
		{
			name: "notifications disabled meanwhile",
			pend: func(t *testing.T, s *Server, database *db.DB, id string) {
				if err := s.recordBackgroundJob(t.Context(), claudetool.BackgroundJob{ID: "job1", ConversationID: id}); err != nil {
					t.Fatal(err)
				}
			},
			resolve: func(t *testing.T, s *Server, database *db.DB, id string) {
				if err := database.UpdateConversationOptions(t.Context(), id, db.ConversationOptions{DisableNotifications: true}); err != nil {
					t.Fatal(err)
				}
				markNotified(t, database, "job1")
				s.settleTurnEnds(id)
			},
			want: 0,
		},
		{
			name: "still waiting",
			pend: func(t *testing.T, s *Server, database *db.DB, id string) {
				if err := s.recordBackgroundJob(t.Context(), claudetool.BackgroundJob{ID: "job1", ConversationID: id}); err != nil {
					t.Fatal(err)
				}
			},
			resolve: func(t *testing.T, s *Server, database *db.DB, id string) {
				s.settleTurnEnds(id)
			},
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, database, _ := newTestServer(t)
			ch := &recordingChannel{}
			server.RegisterNotificationChannel(ch)
			conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
			if err != nil {
				t.Fatal(err)
			}
			id := conversation.ConversationID
			// The turn ending is the manager's.
			if _, err := server.getOrCreateConversationManager(t.Context(), id, ""); err != nil {
				t.Fatal(err)
			}

			tc.pend(t, server, database, id)
			server.publishConversationState(ConversationState{ConversationID: id, Model: "predictable"})
			if n := ch.count(); n != 0 {
				t.Fatalf("turn end with pending work notified %d times", n)
			}
			tc.resolve(t, server, database, id)
			// Some triggers settle in the background.
			waitFor(t, 10*time.Second, func() bool { return ch.count() >= tc.want })
			if n := ch.count(); n != tc.want {
				t.Fatalf("notified %d times, want %d", n, tc.want)
			}
			// Announced at most once.
			server.settleTurnEnds(id)
			if n := ch.count(); n != tc.want {
				t.Fatalf("notified %d times after settling again, want %d", n, tc.want)
			}
		})
	}
}

// The incident: a background job exits while the turn that started it writes
// its final reply. That turn is announced only after the turn answering the
// job's notice, not while the notice waits unanswered.
func TestBackgroundJobTurnEndIsAnnouncedAfterItsNotice(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // keep the job log out of /tmp
	server, database, llmSvc := newTestServer(t)
	defer stopActiveConversationLoops(server)
	server.toolSetConfig.BashBackgroundAfter = 50 * time.Millisecond
	ch := &recordingChannel{}
	server.RegisterNotificationChannel(ch)
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	gate := filepath.Join(t.TempDir(), "gate")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	cm, err := server.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	hold := func(held bool) {
		cm.mu.Lock()
		cm.changingSettings = held
		cm.mu.Unlock()
	}

	llmSvc.SetResponseDelay(500 * time.Millisecond)
	postChatMessage(t, server, id, "bash: read -r _ < "+gate)
	waitForMessageContaining(t, database, id, "moved to background job", 10*time.Second)
	// Hold the queue, as ApplySettings does, so the notice waits unanswered
	// rather than joining the final reply's turn.
	hold(true)
	if err := os.WriteFile(gate, []byte("go\n"), 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { return len(unfinishedBackgroundJobs(t, database)) == 0 })
	waitForIdle(t, server, id)
	llmSvc.SetResponseDelay(0)
	server.settleTurnEnds(id)
	if n := ch.count(); n != 0 {
		t.Fatalf("announced %d times before the notice was answered", n)
	}

	hold(false)
	cm.drainQueueIfIdle(server)
	waitFor(t, 10*time.Second, func() bool { return ch.count() > 0 })
	if notices := backgroundJobNotices(t, database, id); len(notices) != 1 {
		t.Fatalf("notices = %+v, want 1", notices)
	}
	if n := ch.count(); n != 1 {
		t.Fatalf("announced %d times, want 1", n)
	}
}

// A job's notice retries a queue that failed to drain, so a turn end waiting
// on the job waits for the retry too.
func TestJobNoticeRetriesUndrainableQueue(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(s)
	ch := &recordingChannel{}
	s.RegisterNotificationChannel(ch)
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	cm, err := s.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	job := claudetool.BackgroundJob{ID: "job1", ConversationID: id}
	if err := s.recordBackgroundJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	qm := db.QueuedMessage{ID: "q1", Model: "predictable", Llm: []byte(`{"Role":0,"Content":[{"Type":2,"Text":"echo: queued"}]}`)}
	if _, err := database.AppendQueuedMessage(t.Context(), id, qm); err != nil {
		t.Fatal(err)
	}
	s.publishConversationState(ConversationState{ConversationID: id, Model: "predictable"})
	available := s.llmManager
	s.llmManager = &unavailableLLM{}
	<-cm.drainPendingMessages(s)
	s.llmManager = available

	// Hold the retry to look in between.
	cm.mu.Lock()
	cm.changingSettings = true
	cm.mu.Unlock()
	if err := database.QueriesTx(t.Context(), func(q *generated.Queries) error {
		_, err := q.MarkBackgroundJobExited(t.Context(), job.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s.notifyBackgroundJobExit(job)
	if n := ch.count(); n != 0 {
		t.Fatalf("announced %d times before the retry", n)
	}
	cm.mu.Lock()
	cm.changingSettings = false
	cm.mu.Unlock()
	cm.drainQueueIfIdle(s)
	waitFor(t, 10*time.Second, func() bool { return ch.count() > 0 })
	waitForIdle(t, s, id)
	if n := ch.count(); n != 1 {
		t.Fatalf("announced %d times, want 1", n)
	}
}

// A queue held back by a running action drains once the action finishes, so
// a turn end waiting on the queue waits for that.
func TestQueueHeldByActionDrainsAfterIt(t *testing.T) {
	t.Parallel()
	s, database, id := newReportingConversationFixture(t, "true", false)
	defer stopActiveConversationLoops(s)
	ch := &recordingChannel{}
	s.RegisterNotificationChannel(ch)
	cm, err := s.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	run, _, _, err := database.BeginAction(t.Context(), id, "run")
	if err != nil {
		t.Fatal(err)
	}
	qm := db.QueuedMessage{ID: "q1", Model: "predictable", Llm: []byte(`{"Role":0,"Content":[{"Type":2,"Text":"echo: queued"}]}`)}
	if _, err := database.AppendQueuedMessage(t.Context(), id, qm); err != nil {
		t.Fatal(err)
	}
	s.publishConversationState(ConversationState{ConversationID: id, Model: "predictable"})
	<-cm.drainPendingMessages(s)
	s.settleTurnEnds(id)
	if n := ch.count(); n != 0 {
		t.Fatalf("announced %d times while the action runs", n)
	}
	code := 0
	if err := database.FinishAction(t.Context(), run.ID, &code, ""); err != nil {
		t.Fatal(err)
	}
	cm.drainPendingMessages(s) // as finishAction does
	waitFor(t, 10*time.Second, func() bool { return ch.count() > 0 })
	waitForIdle(t, s, id)
	if n := ch.count(); n != 1 {
		t.Fatalf("announced %d times, want 1", n)
	}
}

// A loop that fails mid-turn records no turn end, which would drain the
// queue, so the turn end does not wait on it.
func TestFailedLoopLeavesQueueToUser(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	ch := &recordingChannel{}
	s.RegisterNotificationChannel(ch)
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cm, err := s.getOrCreateConversationManager(t.Context(), conv.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	failed := loop.NewLoop(loop.Config{})
	cm.mu.Lock()
	cm.loop = failed
	cm.loopCancel = func() {}
	cm.loopGeneration = 1
	cm.modelID = "predictable"
	cm.mu.Unlock()
	cm.SetAgentWorking(true)
	if _, err := database.AppendQueuedMessage(t.Context(), conv.ConversationID, db.QueuedMessage{ID: "q1", Llm: []byte(`{"Role":0}`)}); err != nil {
		t.Fatal(err)
	}
	cm.handleFatalLoopExit(failed, 1)
	if n := ch.count(); n != 1 {
		t.Fatalf("announced %d times, want 1", n)
	}
}

// A queued message that failed to start is retried when the next turn ends,
// so that turn's end waits for it.
func TestUndrainableQueueIsRetriedAfterTheNextTurn(t *testing.T) {
	s, database, held, conv := newBtwTest(t)
	ch := &recordingChannel{}
	s.RegisterNotificationChannel(ch)
	cm, err := s.getOrCreateConversationManager(t.Context(), conv.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	qm := db.QueuedMessage{ID: "q1", Model: "predictable", Llm: []byte(`{"Role":0,"Content":[{"Type":2,"Text":"echo: queued"}]}`)}
	if _, err := database.AppendQueuedMessage(t.Context(), conv.ConversationID, qm); err != nil {
		t.Fatal(err)
	}
	s.llmManager = &unavailableLLM{}
	<-cm.drainPendingMessages(s)
	s.llmManager = &testLLMManager{service: held}

	if _, err := cm.AcceptUserMessage(t.Context(), held, "predictable", llm.UserStringMessage("echo: first")); err != nil {
		t.Fatal(err)
	}
	held.waitCall(t, "echo: first").Release()
	queued := held.waitCall(t, "echo: queued")
	if n := ch.count(); n != 0 {
		t.Fatalf("announced %d times before the retried message was answered", n)
	}
	queued.Release()
	waitFor(t, 10*time.Second, func() bool { return ch.count() > 0 })
	waitForIdle(t, s, conv.ConversationID)
	if n := ch.count(); n != 1 {
		t.Fatalf("announced %d times, want 1", n)
	}
}

// endsFirstTurn ends the turn answering "echo: first" as kind says.
type endsFirstTurn struct {
	llm.Service
	kind string
}

func (s endsFirstTurn) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	if lastRequestUserText(req) != "echo: first" {
		return s.Service.Do(ctx, req)
	}
	switch s.kind {
	case "error":
		return nil, errors.New("invalid request")
	case "refusal":
		return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonRefusal}, nil
	case "truncation":
		return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonMaxTokens, Content: llm.TextContent("partial")}, nil
	}
	return s.Service.Do(ctx, req)
}

// A message sent while the model answers the one before runs in a turn of
// its own once that turn ends, however it ends. The conversation is working
// in the meantime, and only the second turn's end is announced.
func TestMessageSentDuringFinalRequestDelaysTheAnnouncement(t *testing.T) {
	for _, kind := range []string{"reply", "error", "refusal", "truncation"} {
		t.Run(kind, func(t *testing.T) {
			s, _, held, conv := newBtwTest(t)
			ch := &recordingChannel{}
			s.RegisterNotificationChannel(ch)
			held.Service = endsFirstTurn{Service: held.Service, kind: kind}
			cm, err := s.getOrCreateConversationManager(t.Context(), conv.ConversationID, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cm.AcceptUserMessage(t.Context(), held, "predictable", llm.UserStringMessage("echo: first")); err != nil {
				t.Fatal(err)
			}
			first := held.waitCall(t, "echo: first")
			if _, err := cm.AcceptUserMessage(t.Context(), held, "predictable", llm.UserStringMessage("echo: second")); err != nil {
				t.Fatal(err)
			}
			first.Release()
			second := held.waitCall(t, "echo: second")
			if n := ch.count(); n != 0 || !cm.IsAgentWorking() {
				t.Fatalf("announced %d times, working = %v, while the second message is answered", n, cm.IsAgentWorking())
			}
			second.Release()
			waitFor(t, 10*time.Second, func() bool { return ch.count() > 0 })
			waitForIdle(t, s, conv.ConversationID)
			if n := ch.count(); n != 1 {
				t.Fatalf("announced %d times, want 1", n)
			}
			if got := ch.events[0].Payload.(notifications.AgentDonePayload).FinalResponse; !strings.Contains(got, "second") {
				t.Fatalf("announced %q", got)
			}
		})
	}
}
