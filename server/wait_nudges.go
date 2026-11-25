package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// Wait nudges keep a conversation from waiting forever on delegated work
// that stalled. While a background job runs, or a subagent the conversation
// waits on stays silent, the conversation hears about it now and then, the
// way it hears that a job exited: an idle conversation starts a turn, and a
// busy one takes the nudge at its next model request. The agent can check on
// the work, stop it, or keep waiting.
//
// Nothing nudges a conversation the user stopped, or its subagents, until
// the user starts it again; Shelley forgets that on restart, as it does for
// subagent reports.
//
// A parent hears about a subagent that owes it a report and has been silent
// mid-turn, stuck in a tool call or model request, where no message reaches
// it. An idle subagent waiting on work of its own needs no word from its
// parent: that work nudges the subagent, which can act on it.

// waitNudgeSchedule is when nudges come: the first this long after the wait
// began, and each later one twice as long after it as the one before. A job
// running for good hears at 60, 120, 240, 480, ... minutes.
var waitNudgeSchedule = struct {
	// Job: since a background job started.
	Job time.Duration
	// Subagent: since a working subagent's latest message.
	Subagent time.Duration
	// Poll is how often Shelley looks for nudges due.
	Poll time.Duration
}{
	Job:      time.Hour,
	Subagent: time.Hour,
	Poll:     time.Minute,
}

// nudgesDue is how many nudges are due elapsed into a wait whose first
// nudge comes after first.
func nudgesDue(first, elapsed time.Duration) int64 {
	var n int64
	for d := first; d > 0 && d <= elapsed; d *= 2 {
		n++
	}
	return n
}

// waitNudgeKind is what a conversation waits on.
type waitNudgeKind string

const (
	waitNudgeJob      waitNudgeKind = "background_job"
	waitNudgeSubagent waitNudgeKind = "subagent"
)

// waitNudgeUserData marks a wait nudge; it is not from a human or another
// conversation.
type waitNudgeUserData struct {
	Kind waitNudgeKind `json:"wait_nudge"`
	// Subject is the job's ID, or the subagent's conversation ID.
	Subject string `json:"subject"`
	// Slug is the subagent's.
	Slug string `json:"slug,omitempty"`
	// Text duplicates the message for full-text search, as in
	// senderMessageUserData.
	Text string `json:"Text"`
}

// waitedOn is delegated work a conversation waits on, without news since.
type waitedOn struct {
	conversationID string // the one waiting, which is nudged
	kind           waitNudgeKind
	subject        string
	slug           string
	since          time.Time
}

func (s *Server) waitNudgeRoutine() {
	ticker := time.NewTicker(waitNudgeSchedule.Poll)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.sendWaitNudges(context.Background(), time.Now())
		case <-s.shutdownCh:
			return
		}
	}
}

// sendWaitNudges sends the nudges due at now.
func (s *Server) sendWaitNudges(ctx context.Context, now time.Time) {
	if err := s.nudgeAboutJobs(ctx, now); err != nil {
		s.logger.Error("Failed to nudge about background jobs", "error", err)
	}
	if err := s.nudgeAboutSubagents(ctx, now); err != nil {
		s.logger.Error("Failed to nudge about subagents", "error", err)
	}
}

// nudgeAboutJobs nudges conversations about their jobs still running.
func (s *Server) nudgeAboutJobs(ctx context.Context, now time.Time) error {
	var rows []generated.BackgroundJob
	err := s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		rows, err = q.ListAllRunningBackgroundJobs(ctx)
		return err
	})
	if err != nil {
		return fmt.Errorf("list running background jobs: %w", err)
	}
	for _, r := range rows {
		job := backgroundJobFromRow(r)
		if err := s.nudgeAboutJob(ctx, now, job); err != nil {
			s.logger.Error("Failed to nudge about background job", "job", job.ID, "conversation", job.ConversationID, "error", err)
		}
	}
	return nil
}

func (s *Server) nudgeAboutJob(ctx context.Context, now time.Time, job claudetool.BackgroundJob) error {
	w := waitedOn{conversationID: job.ConversationID, kind: waitNudgeJob, subject: job.ID, since: job.StartedAt}
	due, err := s.waitNudgesDue(ctx, now, w, waitNudgeSchedule.Job)
	if err != nil || due == 0 {
		return err
	}
	// It may have exited without being marked yet.
	if _, err := os.Stat(job.ExitPath); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if held, err := s.heldLine(ctx, job.ConversationID); err != nil || held {
		return s.skipWaitNudges(ctx, w, due, err)
	}
	tail, err := s.secrets.Redact(ctx, job.Tail())
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Background job %s is still running after %s.\n", job.ID, roughDuration(now.Sub(job.StartedAt)))
	fmt.Fprintf(&b, "Command: %s\n", shortLine(job.Command, 200))
	fmt.Fprintf(&b, "Log: %s", job.LogPath)
	switch fi, err := os.Stat(job.LogPath); {
	case err != nil:
		b.WriteString(" (unreadable)")
	case fi.Size() == 0:
		b.WriteString(" (empty)")
	default:
		fmt.Fprintf(&b, " (%d bytes, last written %s ago)", fi.Size(), roughDuration(now.Sub(fi.ModTime())))
	}
	if tail != "" {
		b.WriteString("\n\n" + tail)
	}
	fmt.Fprintf(&b, "\n\nIf it looks stuck, check on it, or kill it with `kill -- -%d`. Otherwise keep waiting: its exit will wake you, and if it still runs at %s, you will hear again.",
		job.PID, roughDuration(waitNudgeSchedule.Job<<due))
	return s.sendWaitNudge(ctx, w, due, b.String())
}

// nudgeAboutSubagents nudges parents about working subagents gone silent.
func (s *Server) nudgeAboutSubagents(ctx context.Context, now time.Time) error {
	var working []generated.Conversation
	err := s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		working, err = q.ListWorkingSubagents(ctx)
		return err
	})
	if err != nil {
		return fmt.Errorf("list working subagents: %w", err)
	}
	for _, c := range working {
		if !isDelegatedSubagent(c) {
			continue
		}
		if err := s.nudgeAboutSubagent(ctx, now, c); err != nil {
			s.logger.Error("Failed to nudge about subagent", "conversationID", c.ConversationID, "error", err)
		}
	}
	return nil
}

// nudgeAboutSubagent nudges the parent of c, a working subagent, if c owes
// it a report and has been silent for long enough.
func (s *Server) nudgeAboutSubagent(ctx context.Context, now time.Time, c generated.Conversation) error {
	silentSince := c.CreatedAt
	var latest *generated.Message
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		m, err := q.GetLatestTurnMessage(ctx, c.ConversationID)
		if err == nil {
			latest, silentSince = &m, m.CreatedAt
		}
		return err
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load latest message: %w", err)
	}
	// A message from its parent, queued behind the turn that holds it, is
	// owed a report, and the silence counts from it.
	var asked time.Time
	for _, qm := range db.ParseQueuedMessages(c.QueuedMessages) {
		if data, ok, _ := parseSenderMessageUserData(qm.UserData); ok && data.SenderRelationship == senderRelationshipParent && qm.CreatedAt.After(asked) {
			asked = qm.CreatedAt
		}
	}
	since := silentSince
	if asked.After(since) {
		since = asked
	}
	slug := derefString(c.Slug)
	w := waitedOn{conversationID: *c.ParentConversationID, kind: waitNudgeSubagent, subject: c.ConversationID, slug: slug, since: since}
	due, err := s.waitNudgesDue(ctx, now, w, waitNudgeSchedule.Subagent)
	if err != nil || due == 0 {
		return err
	}
	if held, err := s.heldLine(ctx, c.ConversationID); err != nil || held {
		return s.skipWaitNudges(ctx, w, due, err)
	}
	if asked.IsZero() {
		msgs, err := s.db.ListMessages(ctx, c.ConversationID)
		if err != nil {
			return fmt.Errorf("list subagent messages: %w", err)
		}
		if !owesReport(msgs) {
			return s.skipWaitNudges(ctx, w, due, nil)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Subagent %s is mid-turn and has been silent for %s; it owes you a report.", slug, roughDuration(now.Sub(silentSince)))
	if !asked.IsZero() {
		fmt.Fprintf(&b, " Your message of %s ago is still queued behind its turn.", roughDuration(now.Sub(asked)))
	}
	if latest != nil {
		preview, err := s.secrets.Redact(ctx, messagePreview(*latest))
		if err != nil {
			return err
		}
		label := "Its latest message (" + latest.Type + ")"
		if fromParent(*latest) {
			label = "Your latest message to it"
		}
		fmt.Fprintf(&b, "\n%s: %s", label, preview)
	}
	fmt.Fprintf(&b, "\n\nIt may be stuck in a tool call or model request; a message to it waits until that returns. If it looks stuck, see what it is doing in conversation %s: a hung command of its can be found and killed, or tell the user. Otherwise keep waiting: its report will wake you, and if it is still silent at %s, you will hear again.",
		c.ConversationID, roughDuration(waitNudgeSchedule.Subagent<<due))
	return s.sendWaitNudge(ctx, w, due, b.String())
}

// heldLine reports whether conversationID or an ancestor is archived, or
// was stopped by the user and not started again: archiving or stopping a
// conversation holds its subagents too.
func (s *Server) heldLine(ctx context.Context, conversationID string) (bool, error) {
	for id := &conversationID; id != nil; {
		s.mu.Lock()
		stopped := s.stoppedParents[*id]
		s.mu.Unlock()
		if stopped {
			return true, nil
		}
		conv, err := s.db.GetConversationByID(ctx, *id)
		if err != nil {
			return false, fmt.Errorf("load conversation: %w", err)
		}
		if conv.Archived {
			return true, nil
		}
		id = conv.ParentConversationID
	}
	return false, nil
}

// skipWaitNudges records the nudges due about w as sent, though they were
// not, unless err: the reason to skip them lasts, and checking it again each
// poll would cost reads for nothing.
func (s *Server) skipWaitNudges(ctx context.Context, w waitedOn, due int64, err error) error {
	if err != nil {
		return err
	}
	return s.recordWaitNudges(ctx, w, due)
}

// waitNudgesDue is how many nudges about w are due at now, when the first
// comes after first; zero if they all went out already.
func (s *Server) waitNudgesDue(ctx context.Context, now time.Time, w waitedOn, first time.Duration) (int64, error) {
	due := nudgesDue(first, now.Sub(w.since))
	if due == 0 {
		return 0, nil
	}
	var row generated.WaitNudge
	err := s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		row, err = q.GetWaitNudge(ctx, generated.GetWaitNudgeParams{ConversationID: w.conversationID, Kind: string(w.kind), Subject: w.subject})
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return due, nil
	}
	if err != nil {
		return 0, fmt.Errorf("load wait nudge: %w", err)
	}
	if row.Since.Equal(waitNudgeSince(w)) && row.Sent >= due {
		return 0, nil
	}
	return due, nil
}

// waitNudgeSince is w.since as stored.
func waitNudgeSince(w waitedOn) time.Time {
	return w.since.UTC().Truncate(time.Second)
}

// sendWaitNudge queues text in w's conversation and records that the nudges
// due about w went out. Nudges do not pile up in a conversation that does not
// take them: one about w still queued stands for the rest.
func (s *Server) sendWaitNudge(ctx context.Context, w waitedOn, due int64, text string) error {
	conv, err := s.db.GetConversationByID(ctx, w.conversationID)
	if err != nil {
		return fmt.Errorf("load conversation: %w", err)
	}
	queued := slices.ContainsFunc(db.ParseQueuedMessages(conv.QueuedMessages), func(qm db.QueuedMessage) bool {
		var data waitNudgeUserData
		return json.Unmarshal(qm.UserData, &data) == nil && data.Kind == w.kind && data.Subject == w.subject
	})
	if !queued {
		cm, err := s.getOrCreateConversationManager(ctx, w.conversationID, "")
		if err != nil {
			return fmt.Errorf("load conversation: %w", err)
		}
		cm.mu.Lock()
		modelID := cm.modelID
		cm.mu.Unlock()
		data := waitNudgeUserData{Kind: w.kind, Subject: w.subject, Slug: w.slug, Text: text}
		if err := cm.InjectMessage(contextWithTurnUserData(ctx, data), modelID, llm.UserStringMessage(text)); err != nil {
			return err
		}
	}
	return s.recordWaitNudges(ctx, w, due)
}

// recordWaitNudges records that due nudges about w went out.
func (s *Server) recordWaitNudges(ctx context.Context, w waitedOn, due int64) error {
	return s.db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.RecordWaitNudge(ctx, generated.RecordWaitNudgeParams{
			ConversationID: w.conversationID,
			Kind:           string(w.kind),
			Subject:        w.subject,
			Since:          waitNudgeSince(w),
			Sent:           due,
		})
	})
}

// messagePreview is the start of m's text, or else the tools it calls.
func messagePreview(m generated.Message) string {
	msg, err := convertToLLMMessage(m)
	if err != nil {
		return "(unreadable)"
	}
	text := messageText(msg)
	if text == "" {
		var parts []string
		for _, c := range msg.Content {
			switch c.Type {
			case llm.ContentTypeToolUse:
				parts = append(parts, "calls "+c.ToolName+" "+string(c.ToolInput))
			case llm.ContentTypeToolResult:
				parts = append(parts, "a tool result")
			}
		}
		text = strings.Join(parts, "; ")
	}
	return truncateRunes(strings.Join(strings.Fields(text), " "), 300)
}

// roughDuration is d to the minute, or to the second under a minute.
func roughDuration(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
}
