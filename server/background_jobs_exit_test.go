package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
)

func recordStaleBackgroundJob(t *testing.T, s *Server) claudetool.BackgroundJob {
	t.Helper()
	conv, err := s.db.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	job := claudetool.BackgroundJob{
		ID: "stale", ConversationID: conv.ConversationID,
		PID: os.Getpid(), StartTime: 1, StartedAt: time.Now(),
	}
	if err := s.recordBackgroundJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	return job
}

func stopBackgroundJob(t *testing.T, s *Server, job claudetool.BackgroundJob) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleKillBackgroundJob(w, httptest.NewRequest(http.MethodPost, "/", nil), job.ConversationID, job.ID)
	return w
}

func TestBackgroundJobExitWriteFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, database, _ := newTestServer(t)
		defer stopActiveConversationLoops(s)
		job := recordStaleBackgroundJob(t, s)
		// Fail the commit after the UPDATE returns its row.
		if err := database.Pool().Exec(t.Context(), `
			CREATE TABLE exit_parent (id INTEGER PRIMARY KEY);
			CREATE TABLE exit_child (id INTEGER REFERENCES exit_parent(id) DEFERRABLE INITIALLY DEFERRED);
			CREATE TRIGGER fail_job_exit AFTER UPDATE OF exited ON background_jobs
			BEGIN INSERT INTO exit_child VALUES (1); END;
		`); err != nil {
			t.Fatal(err)
		}
		if w := stopBackgroundJob(t, s, job); w.Code != http.StatusInternalServerError {
			t.Fatalf("stop: %d %s, want 500", w.Code, w.Body.String())
		}
		if err := s.recoverBackgroundJobs(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if n := listedRunningJobs(t, s, job.ConversationID); n != 1 {
			t.Fatalf("running jobs = %d, want 1 after rollback", n)
		}
		if notices := backgroundJobNotices(t, database, job.ConversationID); len(notices) != 0 {
			t.Fatalf("failed commit sent notices: %+v", notices)
		}
		if err := database.Pool().Exec(t.Context(), "DROP TRIGGER fail_job_exit"); err != nil {
			t.Fatal(err)
		}
		if w := stopBackgroundJob(t, s, job); w.Code != http.StatusNoContent {
			t.Fatalf("retry stop: %d %s", w.Code, w.Body.String())
		}
		synctest.Wait()
		if n := listedRunningJobs(t, s, job.ConversationID); n != 0 {
			t.Fatalf("running jobs = %d after retry, want 0", n)
		}
		if notices := backgroundJobNotices(t, database, job.ConversationID); len(notices) != 1 {
			t.Fatalf("retry notices = %+v, want one", notices)
		}
	})
}

func TestBackgroundJobExitDoesNotWaitForConversation(t *testing.T) {
	for _, action := range []string{"stop", "recover"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, database, _ := newTestServer(t)
				defer stopActiveConversationLoops(s)
				job := recordStaleBackgroundJob(t, s)
				cm, err := s.getOrCreateConversationManager(t.Context(), job.ConversationID, "")
				if err != nil {
					t.Fatal(err)
				}
				// Hold the queue, as ApplySettings does, so delivery waits.
				cm.mu.Lock()
				cm.changingSettings = true
				cm.mu.Unlock()
				release := func() {
					cm.mu.Lock()
					cm.changingSettings = false
					cm.mu.Unlock()
					cm.drainQueueIfIdle()
				}
				defer func() {
					if release != nil {
						release()
					}
				}()

				done := make(chan error, 1)
				go func() {
					if action == "recover" {
						done <- s.recoverBackgroundJobs(t.Context())
					} else {
						w := stopBackgroundJob(t, s, job)
						if w.Code != http.StatusNoContent {
							t.Errorf("stop: %d %s", w.Code, w.Body.String())
						}
						done <- nil
					}
				}()
				synctest.Wait()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				default:
					t.Fatal("job cleanup blocked on its conversation")
				}
				if n := listedRunningJobs(t, s, job.ConversationID); n != 0 {
					t.Fatalf("running jobs = %d, want 0 before delivery", n)
				}
				if notices := backgroundJobNotices(t, database, job.ConversationID); len(notices) != 0 {
					t.Fatalf("delivery bypassed the conversation gate: %+v", notices)
				}
				release()
				release = nil
				synctest.Wait()
				if notices := backgroundJobNotices(t, database, job.ConversationID); len(notices) != 1 {
					t.Fatalf("notices = %+v, want one after release", notices)
				}
			})
		})
	}
}

func TestBackgroundJobConcurrentExitReportsOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, database, _ := newTestServer(t)
		defer stopActiveConversationLoops(s)
		job := recordStaleBackgroundJob(t, s)
		// Startup recovery's watcher races Stop and other reports.
		if err := s.recoverBackgroundJobs(t.Context()); err != nil {
			t.Fatal(err)
		}
		go func() {
			w := stopBackgroundJob(t, s, job)
			if w.Code != http.StatusNoContent && w.Code != http.StatusNotFound {
				t.Errorf("stop: %d %s", w.Code, w.Body.String())
			}
		}()
		for range 4 {
			go func() {
				if err := s.reportBackgroundJobExit(t.Context(), job); err != nil {
					t.Error(err)
				}
			}()
		}
		synctest.Wait()
		if n := listedRunningJobs(t, s, job.ConversationID); n != 0 {
			t.Fatalf("running jobs = %d, want 0", n)
		}
		if notices := backgroundJobNotices(t, database, job.ConversationID); len(notices) != 1 {
			t.Fatalf("notices = %+v, want exactly one", notices)
		}
		if jobs := unfinishedBackgroundJobs(t, database); len(jobs) != 0 {
			t.Fatalf("unfinished jobs = %+v", jobs)
		}
	})
}

// A Shelley predating the exited column reports jobs without marking them
// exited, so after an upgrade they look running. Clearing them must not
// report them again.
func TestBackgroundJobExitAfterNoticeIsSilent(t *testing.T) {
	for _, action := range []string{"stop", "recover"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, database, _ := newTestServer(t)
				defer stopActiveConversationLoops(s)
				job := recordStaleBackgroundJob(t, s)
				// What such a Shelley ran on reporting a job.
				if err := database.Pool().Exec(t.Context(), "UPDATE background_jobs SET notified = TRUE WHERE job_id = ?", job.ID); err != nil {
					t.Fatal(err)
				}
				if action == "recover" {
					if err := s.recoverBackgroundJobs(t.Context()); err != nil {
						t.Fatal(err)
					}
				} else if w := stopBackgroundJob(t, s, job); w.Code != http.StatusNoContent {
					t.Fatalf("stop: %d %s", w.Code, w.Body.String())
				}
				synctest.Wait()
				if n := listedRunningJobs(t, s, job.ConversationID); n != 0 {
					t.Fatalf("running jobs = %d, want 0", n)
				}
				if notices := backgroundJobNotices(t, database, job.ConversationID); len(notices) != 0 {
					t.Fatalf("reported a job again: %+v", notices)
				}
			})
		})
	}
}
