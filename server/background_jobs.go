package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// backgroundJobs implements claudetool.BackgroundJobs: when a backgrounded
// bash command exits, it tells the command's conversation. Jobs live in the
// database: a job is running until it is seen to exit, and one outliving
// this process is still reported after a restart (see recoverBackgroundJobs).
type backgroundJobs struct {
	server *Server
}

var _ claudetool.BackgroundJobs = backgroundJobs{}

// Background implements claudetool.BackgroundJobs.
func (b backgroundJobs) Background(ctx context.Context, job claudetool.BackgroundJob, exited <-chan struct{}) error {
	if err := b.server.recordBackgroundJob(ctx, job); err != nil {
		return err
	}
	b.server.watchBackgroundJob(job, exited)
	return nil
}

// recordBackgroundJob stores job as running.
func (s *Server) recordBackgroundJob(ctx context.Context, job claudetool.BackgroundJob) error {
	err := s.db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.InsertBackgroundJob(ctx, generated.InsertBackgroundJobParams{
			JobID:            job.ID,
			ConversationID:   job.ConversationID,
			ToolUseID:        job.ToolUseID,
			Command:          job.Command,
			Pid:              int64(job.PID),
			ProcessStartTime: int64(job.StartTime),
			LogPath:          job.LogPath,
			ExitPath:         job.ExitPath,
			StartedAt:        job.StartedAt.UTC(),
		})
	})
	if err != nil {
		return fmt.Errorf("record background job: %w", err)
	}
	return nil
}

// recoverBackgroundJobs finishes the jobs a previous Shelley process left
// unfinished: exited jobs are reported now, running ones when they exit. A
// job that cannot be watched stays running until the next startup, or a
// Stop once its process is gone.
func (s *Server) recoverBackgroundJobs(ctx context.Context) error {
	var rows []generated.BackgroundJob
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		rows, err = q.ListUnfinishedBackgroundJobs(ctx)
		return err
	})
	if err != nil {
		return fmt.Errorf("list background jobs: %w", err)
	}
	for _, r := range rows {
		job := backgroundJobFromRow(r)
		if r.Exited {
			go s.notifyBackgroundJobExit(job)
			continue
		}
		exited, err := job.Exited()
		if err != nil {
			s.logger.Error("Failed to watch background job", "job", job.ID, "conversation", job.ConversationID, "error", err)
			continue
		}
		s.watchBackgroundJob(job, exited)
	}
	return nil
}

func backgroundJobFromRow(r generated.BackgroundJob) claudetool.BackgroundJob {
	return claudetool.BackgroundJob{
		ID:             r.JobID,
		ConversationID: r.ConversationID,
		ToolUseID:      r.ToolUseID,
		Command:        r.Command,
		PID:            int(r.Pid),
		StartTime:      uint64(r.ProcessStartTime),
		LogPath:        r.LogPath,
		ExitPath:       r.ExitPath,
		StartedAt:      r.StartedAt,
	}
}

// watchBackgroundJob reports job once it exits.
func (s *Server) watchBackgroundJob(job claudetool.BackgroundJob, exited <-chan struct{}) {
	go func() {
		<-exited
		if err := s.reportBackgroundJobExit(context.Background(), job); err != nil {
			s.logger.Error("Failed to record background job exit", "job", job.ID, "conversation", job.ConversationID, "error", err)
		}
	}()
}

// runningBackgroundJobsOf returns conversationID's running jobs, oldest first.
func (s *Server) runningBackgroundJobsOf(ctx context.Context, conversationID string) ([]claudetool.BackgroundJob, error) {
	var rows []generated.BackgroundJob
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		rows, err = q.ListRunningBackgroundJobs(ctx, conversationID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list running background jobs: %w", err)
	}
	jobs := make([]claudetool.BackgroundJob, len(rows))
	for i, r := range rows {
		jobs[i] = backgroundJobFromRow(r)
	}
	return jobs, nil
}

// reportBackgroundJobExit records that job exited and, unless another
// caller already did or the job was already reported, schedules its
// notice. Shelleys predating the exited column report jobs without
// marking them exited. A failed write is retried at the next startup or
// Stop; until then, the job still counts as running.
func (s *Server) reportBackgroundJobExit(ctx context.Context, job claudetool.BackgroundJob) error {
	notify := false
	err := s.db.QueriesTx(ctx, func(q *generated.Queries) error {
		notified, err := q.MarkBackgroundJobExited(ctx, job.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		notify = !notified
		return nil
	})
	if err != nil {
		return fmt.Errorf("record background job %s exit: %w", job.ID, err)
	}
	if notify {
		go s.notifyBackgroundJobExit(job)
	}
	return nil
}

// notifyBackgroundJobExit queues job's notice in its conversation and
// records that it did. A failure is retried at the next startup; until then
// the conversation waits on the job, and its turn ends are not announced.
func (s *Server) notifyBackgroundJobExit(job claudetool.BackgroundJob) {
	ctx := context.Background()
	err := s.deliverBackgroundJobNotice(ctx, job)
	if err == nil {
		err = s.db.QueriesTx(ctx, func(q *generated.Queries) error {
			return q.MarkBackgroundJobNotified(ctx, job.ID)
		})
	}
	if err != nil {
		s.logger.Error("Failed to deliver background job notice", "job", job.ID, "conversation", job.ConversationID, "error", err)
		return
	}
	// A turn that ended before the job counted as delivered waited on it,
	// even if that turn was the one answering the notice.
	s.settleTurnEnds(job.ConversationID)
}

// deliverBackgroundJobNotice queues job's notice as a user row whose
// user_data names the job, so the model sees it wrapped in
// <background_job> and the UI attributes it. A busy conversation takes it
// at its next LLM request; an idle one starts a turn.
func (s *Server) deliverBackgroundJobNotice(ctx context.Context, job claudetool.BackgroundJob) error {
	cm, err := s.getOrCreateConversationManager(ctx, job.ConversationID, "")
	if err != nil {
		return fmt.Errorf("load conversation: %w", err)
	}
	cm.mu.Lock()
	modelID := cm.modelID
	cm.mu.Unlock()
	outcome := job.Outcome()
	// The notice carries the job's output to the model.
	if outcome.Tail, err = s.secrets.Redact(ctx, outcome.Tail); err != nil {
		return err
	}
	text := outcome.Notice()
	data := backgroundJobUserData{
		BackgroundJobID: job.ID,
		Command:         job.Command,
		ExitCode:        outcome.ExitCode,
		LogPath:         job.LogPath,
		Tail:            outcome.Tail,
		Text:            text,
	}
	if outcome.ExitCode != nil {
		data.Duration = outcome.Elapsed.String()
	}
	ctx = contextWithTurnUserData(ctx, data)
	return cm.InjectMessage(ctx, modelID, llm.UserStringMessage(text))
}

// BackgroundJobInfo describes a running background job to the UI.
type BackgroundJobInfo struct {
	JobID     string    `json:"job_id"`
	Command   string    `json:"command"`
	Tail      string    `json:"tail"`
	StartedAt time.Time `json:"started_at"`
}

// handleListBackgroundJobs handles GET /api/conversation/{id}/background-jobs.
func (s *Server) handleListBackgroundJobs(w http.ResponseWriter, r *http.Request, conversationID string) {
	jobs, err := s.runningBackgroundJobsOf(r.Context(), conversationID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := []BackgroundJobInfo{}
	for _, j := range jobs {
		out = append(out, BackgroundJobInfo{JobID: j.ID, Command: j.Command, Tail: j.Tail(), StartedAt: j.StartedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleKillBackgroundJob handles
// POST /api/conversation/{id}/background-jobs/{jobID}/kill. It sends
// SIGTERM to the job's process group; the job's exit then reports through
// the usual notice. A job already gone is reported now.
func (s *Server) handleKillBackgroundJob(w http.ResponseWriter, r *http.Request, conversationID, jobID string) {
	jobs, err := s.runningBackgroundJobsOf(r.Context(), conversationID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	i := slices.IndexFunc(jobs, func(j claudetool.BackgroundJob) bool { return j.ID == jobID })
	if i < 0 {
		http.Error(w, fmt.Sprintf("no running background job %s in this conversation", jobID), http.StatusNotFound)
		return
	}
	err = jobs[i].Kill()
	if errors.Is(err, claudetool.ErrBackgroundJobGone) {
		if err := s.reportBackgroundJobExit(r.Context(), jobs[i]); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.logger.Error("Failed to kill background job", "job", jobID, "conversation", conversationID, "error", err)
		http.Error(w, fmt.Sprintf("kill background job %s: %v", jobID, err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
