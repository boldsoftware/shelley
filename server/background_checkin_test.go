package server

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func TestBackgroundCheckInSchedule(t *testing.T) {
	for _, tc := range []struct{ elapsed, want time.Duration }{
		{0, time.Hour},
		{time.Hour - time.Nanosecond, time.Hour},
		{time.Hour, 2 * time.Hour},
		{2 * time.Hour, 4 * time.Hour},
		{4 * time.Hour, 8 * time.Hour},
		{8 * time.Hour, 16 * time.Hour},
		{16 * time.Hour, 24 * time.Hour},
		{24 * time.Hour, 48 * time.Hour},
		{49 * time.Hour, 72 * time.Hour},
	} {
		if got := nextBackgroundCheck(tc.elapsed); got != tc.want {
			t.Errorf("nextBackgroundCheck(%v) = %v, want %v", tc.elapsed, got, tc.want)
		}
	}
}

func TestBackgroundCheckInQueuedDedupAndExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, database, _ := newTestServer(t)
		defer stopActiveConversationLoops(s)
		job := recordStaleBackgroundJob(t, s)
		cm, err := s.getOrCreateConversationManager(t.Context(), job.ConversationID, "")
		if err != nil {
			t.Fatal(err)
		}
		cm.mu.Lock()
		cm.changingSettings = true
		cm.mu.Unlock()
		exited := make(chan struct{})
		defer func() {
			select {
			case <-exited:
			default:
				close(exited)
			}
		}()
		s.watchBackgroundJob(job, exited)
		synctest.Wait()
		<-time.After(time.Hour)
		synctest.Wait()
		queued, err := database.GetQueuedMessages(t.Context(), job.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		if len(queued) != 1 || !queued[0].Inject {
			t.Fatalf("first check-in queue = %+v", queued)
		}
		<-time.After(time.Hour)
		synctest.Wait()
		queued, err = database.GetQueuedMessages(t.Context(), job.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		if len(queued) != 1 {
			t.Fatalf("check-ins should deduplicate: %+v", queued)
		}
		close(exited)
		synctest.Wait()
		queued, err = database.GetQueuedMessages(t.Context(), job.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		if len(queued) != 2 {
			t.Fatalf("check-in and exit notice = %+v", queued)
		}
		cm.loopLifecycleMu.Lock()
		kept, err := cm.filterBackgroundCheckIns(t.Context(), queued)
		cm.loopLifecycleMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if len(kept) != 1 || kept[0].ID != queued[1].ID {
			t.Fatalf("stale check-in not removed, or exit removed: %+v", kept)
		}
		queued, err = database.GetQueuedMessages(t.Context(), job.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		if len(queued) != 1 || queued[0].ID != kept[0].ID {
			t.Fatalf("persisted queue = %+v", queued)
		}
		cm.mu.Lock()
		cm.changingSettings = false
		cm.mu.Unlock()
		cm.drainQueueIfIdle()
		synctest.Wait()
		notices := backgroundJobNotices(t, database, job.ConversationID)
		if len(notices) != 1 || notices[0].CheckIn {
			t.Fatalf("delivered notices = %+v", notices)
		}
	})
}

func TestBackgroundCheckInInjectionAndGuard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, database, service := newTestServer(t)
		defer stopActiveConversationLoops(s)
		job := recordStaleBackgroundJob(t, s)
		if err := s.nudgeBackgroundJob(job); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		notices := backgroundJobNotices(t, database, job.ConversationID)
		if len(notices) != 1 || !notices[0].CheckIn || notices[0].BackgroundJobID != job.ID {
			t.Fatalf("notices = %+v", notices)
		}
		found := false
		for _, req := range service.GetRecentRequests() {
			for _, msg := range req.Messages {
				if msg.Role == llm.MessageRoleUser && strings.Contains(messageText(msg), `<background_job id="`+job.ID+`">`) {
					found = true
				}
			}
		}
		if !found {
			t.Fatal("check-in was not attributed in model request")
		}
		s.mu.Lock()
		s.stoppedParents[job.ConversationID] = true
		s.mu.Unlock()
		if err := s.nudgeBackgroundJob(job); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := backgroundJobNotices(t, database, job.ConversationID); len(got) != 1 {
			t.Fatalf("stopped conversation sent check-in: %+v", got)
		}
		s.mu.Lock()
		delete(s.stoppedParents, job.ConversationID)
		s.mu.Unlock()
		if err := database.QueriesTx(t.Context(), func(q *generated.Queries) error { _, err := q.MarkBackgroundJobExited(t.Context(), job.ID); return err }); err != nil {
			t.Fatal(err)
		}
		if err := s.nudgeBackgroundJob(job); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := backgroundJobNotices(t, database, job.ConversationID); len(got) != 1 {
			t.Fatalf("exited job sent check-in: %+v", got)
		}
	})
}
