package server

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// A project action that reports to the agent (any but a silent one)
// queues its result in the run's conversation when it finishes, so the
// agent works on a failure. Reports are durable queue items (taken at the
// next model request of a busy turn; an idle conversation starts a turn) whose
// user_data names the run, so the UI renders them as the run's card.
//
//   - A failed or interrupted run reports at once, framed as a problem to
//     investigate, never to rerun: actions run only when the user asks.
//   - A successful run that archived its conversation sends nothing: a turn would
//     only delay the archive. One that kept its conversation (no archive, or newer
//     pending work) reports at once.
//
// The worker does not depend on an open browser, and resumes after a restart.

const (
	reportTailLines = 200
	reportTailBytes = 16 << 10
	// reportRetry is when the worker tries again after an error.
	reportRetry = time.Minute
)

// actionUserData marks a report of a project action run: a user message to
// the model, the run's card to the reader.
type actionUserData struct {
	ActionRunID string `json:"action_run_id"`
	// Report is "result".
	Report   string `json:"action_report"`
	Label    string `json:"label"`
	Command  string `json:"command"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code,omitempty"`
	// Text duplicates the message for full-text search, as in
	// senderMessageUserData.
	Text string `json:"Text"`
}

func (s *Server) pokeActionReports() {
	select {
	case s.workspace.reports <- struct{}{}:
	default:
	}
}

// startActionReports runs the report worker until shutdown: it sends the
// reports due at startup and whenever poked, and retries after errors.
// Reports queued before a restart drain with recoverChannelQueues.
func (s *Server) startActionReports() {
	s.workspace.workers.Add(1)
	go func() {
		defer s.workspace.workers.Done()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-s.shutdownCh
			cancel()
		}()
		for {
			var retry <-chan time.Time
			if !s.advanceActionReports(ctx) {
				retry = time.After(reportRetry)
			}
			select {
			case <-ctx.Done():
				return
			case <-s.workspace.reports:
			case <-retry:
			}
		}
	}()
}

// advanceActionReports sends due reports and reports whether all went well.
func (s *Server) advanceActionReports(ctx context.Context) bool {
	runs, err := s.db.ListDueActionReports(ctx)
	if err != nil {
		s.logger.Error("Action reports: list", "error", err)
		return false
	}
	ok := true
	for _, run := range runs {
		if err := s.advanceActionReport(ctx, &run); err != nil {
			s.logger.Error("Action report failed", "runID", run.ID, "error", err)
			ok = false
		}
	}
	return ok
}

// advanceActionReport sends or skips run's due report. It is idempotent:
// the database decides each report once.
func (s *Server) advanceActionReport(ctx context.Context, run *db.ActionRun) error {
	if run.Status == "running" || run.Report != "pending" {
		return nil
	}
	conversation, err := s.db.GetConversationByID(ctx, run.ConversationID)
	if err != nil {
		return err
	}
	if conversation.Archived {
		// Archived on success (nothing to report), or by the user: leave it alone.
		_, err := s.db.SkipActionReport(ctx, run.ID)
		return err
	}
	return s.queueActionReport(ctx, run)
}

// queueActionReport queues the report like any injected message, but
// records it sent in the same transaction.
func (s *Server) queueActionReport(ctx context.Context, run *db.ActionRun) error {
	cm, err := s.getOrCreateConversationManager(ctx, run.ConversationID, "")
	if err != nil {
		return err
	}
	data := actionUserData{
		ActionRunID: run.ID, Report: "result", Label: run.Label, Command: run.Command,
		Status: run.Status, ExitCode: run.ExitCode, Text: actionResultText(run),
	}
	llmJSON, err := json.Marshal(llm.UserStringMessage(data.Text))
	if err != nil {
		return err
	}
	userData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	cm.mu.Lock()
	modelID := cm.modelID
	cm.mu.Unlock()
	qm := db.QueuedMessage{
		ID: uuid.New().String(), Llm: llmJSON, CreatedAt: time.Now().UTC(),
		Model: modelID, UserData: userData, Inject: true,
	}
	cm.cancelIdleCompaction()
	cm.loopLifecycleMu.Lock()
	cm.waitForLoopTeardownLocked()
	queued, err := s.db.QueueActionReport(ctx, run.ID, qm)
	cm.loopLifecycleMu.Unlock()
	if err != nil || !queued {
		return err
	}
	cm.logger.Info("Queued action report", "runID", run.ID, "queued_id", qm.ID)
	cm.Touch()
	go s.notifySubscribers(context.WithoutCancel(ctx), run.ConversationID)
	cm.drainQueueIfIdle(s)
	return nil
}

var terminalEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// textTail is the end of output as a terminal would show it, at most
// maxLines and maxBytes, and whether earlier output was left out.
func textTail(output string, maxLines, maxBytes int) (string, bool) {
	output = terminalEscape.ReplaceAllString(output, "")
	lines := strings.Split(strings.TrimRight(output, "\r\n"), "\n")
	for i, line := range lines {
		line = strings.TrimRight(line, "\r")
		lines[i] = line[strings.LastIndexByte(line, '\r')+1:]
	}
	truncated := false
	if len(lines) > maxLines {
		lines, truncated = lines[len(lines)-maxLines:], true
	}
	tail := strings.Join(lines, "\n")
	if len(tail) > maxBytes {
		tail, truncated = tail[len(tail)-maxBytes:], true
		if i := strings.IndexByte(tail, '\n'); i >= 0 {
			tail = tail[i+1:]
		}
	}
	return strings.ToValidUTF8(tail, "\uFFFD"), truncated
}

func actionName(run *db.ActionRun) string {
	if run.Command == run.Label {
		return fmt.Sprintf("`%s`", run.Command)
	}
	return fmt.Sprintf("%q (`%s`)", run.Label, run.Command)
}

const noRerunNote = "Don't run this action or its command again yourself unless the user asks; they rerun it with its button."

func actionResultText(run *db.ActionRun) string {
	var b strings.Builder
	name := actionName(run)
	switch run.Status {
	case "succeeded":
		fmt.Fprintf(&b, "The user ran project action %s; it succeeded (exit 0).\n", name)
	case "interrupted":
		fmt.Fprintf(&b, "The user ran project action %s; it was interrupted: %s\n", name, run.Error)
	default:
		fmt.Fprintf(&b, "The user ran project action %s; it failed", name)
		if run.ExitCode != nil {
			fmt.Fprintf(&b, " with exit code %d", *run.ExitCode)
		}
		b.WriteString(".\n")
		if run.Error != "" && !strings.HasPrefix(run.Error, "exit status ") {
			fmt.Fprintf(&b, "Error: %s\n", run.Error)
		}
	}
	tail, truncated := textTail(run.Output, reportTailLines, reportTailBytes)
	switch {
	case tail == "":
		b.WriteString("It printed no output.\n")
	case truncated:
		fmt.Fprintf(&b, "End of its output (earlier output omitted; at most %d lines or %d KB shown):\n", reportTailLines, reportTailBytes>>10)
	default:
		b.WriteString("Its output:\n")
	}
	if tail != "" {
		fmt.Fprintf(&b, "<output>\n%s\n</output>\n", tail)
	}
	switch {
	case run.Status == "interrupted":
		b.WriteString("Its outcome is unknown. Check what it did (e.g. whether a push landed) and tell the user. " + noRerunNote)
	case run.Status == "succeeded":
		b.WriteString("Briefly tell the user the result.")
	default:
		b.WriteString("Investigate the failure and fix what you can, then tell the user what happened and what you changed. " + noRerunNote)
	}
	return b.String()
}

// reportAction sends a just-finished run's report; the worker retries it
// on failure.
func (s *Server) reportAction(ctx context.Context, runID string) {
	err := func() error {
		run, err := s.db.GetActionRun(ctx, runID)
		if err != nil {
			return err
		}
		return s.advanceActionReport(ctx, run)
	}()
	if err != nil {
		s.logger.Error("Action report failed; the report worker retries", "runID", runID, "error", err)
	}
	s.pokeActionReports()
}
