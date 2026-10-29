package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func newReportingConversationFixture(t *testing.T, command string, archive bool) (*Server, *db.DB, string) {
	t.Helper()
	s, database, id, _ := newActionFixture(t, command, archive)
	return s, database, id
}

func runReportingAction(t *testing.T, s *Server, id string) db.ActionRun {
	t.Helper()
	run := workspaceResponse[db.ActionRun](t, workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/run", ""), 202)
	waitForActions(t, s)
	waitForIdle(t, s, id)
	return run
}

// awaitActionReports waits for the conversation to record n reports and
// finish the turn they start.
func awaitActionReports(t *testing.T, s *Server, id string, n int) []actionUserData {
	t.Helper()
	waitFor(t, 10*time.Second, func() bool { return len(actionReports(t, s.db, id)) >= n })
	waitForIdle(t, s, id)
	return actionReports(t, s.db, id)
}

// actionReports returns the conversation's report messages.
func actionReports(t *testing.T, database *db.DB, id string) []actionUserData {
	t.Helper()
	messages, err := database.ListMessages(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	var reports []actionUserData
	for _, message := range messages {
		if message.UserData == nil {
			continue
		}
		var data actionUserData
		if err := json.Unmarshal([]byte(*message.UserData), &data); err == nil && data.ActionRunID != "" {
			if message.Type != string(db.MessageTypeUser) {
				t.Fatalf("report message: %+v", message)
			}
			reports = append(reports, data)
		}
	}
	return reports
}

func TestActionFailureReportsToAgentOnce(t *testing.T) {
	t.Parallel()
	s, database, id := newReportingConversationFixture(t, "printf 'progress 1%%\\rprogress 99%%\\n\\033[31mcommit is authored by exe.dev user\\033[0m\\n' >&2; exit 3", true)
	run := runReportingAction(t, s, id)
	reports := awaitActionReports(t, s, id, 1)
	if len(reports) != 1 {
		t.Fatalf("reports: %+v", reports)
	}
	report := reports[0]
	if report.ActionRunID != run.ID || report.Status != "failed" ||
		report.ExitCode == nil || *report.ExitCode != 3 || report.Command == "" {
		t.Fatalf("report: %+v", report)
	}
	for _, want := range []string{"failed with exit code 3", "progress 99%\ncommit is authored by exe.dev user\n</output>", "Investigate the failure", "Don't run this action"} {
		if !strings.Contains(report.Text, want) {
			t.Fatalf("report text lacks %q:\n%s", want, report.Text)
		}
	}
	if output := report.Text[strings.Index(report.Text, "<output>"):]; strings.Contains(output, "progress 1%") || strings.Contains(output, "\x1b") || strings.Contains(report.Text, "Error: exit status") {
		t.Fatalf("report text keeps terminal noise:\n%s", report.Text)
	}
	stored, err := database.GetActionRun(t.Context(), run.ID)
	if err != nil || stored.Report != "sent" {
		t.Fatalf("report state: %+v %v", stored, err)
	}
	// Retries (the worker, a restart) never repeat a report.
	if err := s.advanceActionReport(t.Context(), stored); err != nil {
		t.Fatal(err)
	}
	s.advanceActionReports(t.Context())
	if reports := actionReports(t, database, id); len(reports) != 1 {
		t.Fatalf("duplicate reports: %+v", reports)
	}
}

func TestActionArchivedSuccessSendsNothing(t *testing.T) {
	t.Parallel()
	s, database, id := newReportingConversationFixture(t, "echo landed", true)
	run := runReportingAction(t, s, id)
	if reports := actionReports(t, database, id); len(reports) != 0 {
		t.Fatalf("reports: %+v", reports)
	}
	stored, err := database.GetActionRun(t.Context(), run.ID)
	if err != nil || stored.Report != "skipped" || !stored.Archived {
		t.Fatalf("report state: %+v %v", stored.Report, err)
	}
	conversation, err := database.GetConversationByID(t.Context(), id)
	if err != nil || !conversation.Archived {
		t.Fatalf("archive: %+v %v", conversation, err)
	}
}

func TestActionSuccessReportsWhenKept(t *testing.T) {
	t.Parallel()
	s, _, id := newReportingConversationFixture(t, "echo tests passed", false)
	runReportingAction(t, s, id)
	reports := awaitActionReports(t, s, id, 1)
	if len(reports) != 1 || reports[0].Status != "succeeded" || !strings.Contains(reports[0].Text, "it succeeded (exit 0)") ||
		!strings.Contains(reports[0].Text, "Briefly tell the user") {
		t.Fatalf("reports: %+v", reports)
	}
}

func TestSilentActionSendsNothing(t *testing.T) {
	t.Parallel()
	s, database, id, _ := newActionFixture(t, "exit 1", false)
	metadata, err := database.GetWorkspaceConversation(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	project, err := database.GetProject(t.Context(), metadata.Project)
	if err != nil {
		t.Fatal(err)
	}
	project.Actions[0].Silent = true
	if err := database.UpdateProject(t.Context(), project.Name, *project); err != nil {
		t.Fatal(err)
	}
	run := runReportingAction(t, s, id)
	if reports := actionReports(t, database, id); len(reports) != 0 {
		t.Fatalf("reports: %+v", reports)
	}
	stored, err := database.GetActionRun(t.Context(), run.ID)
	if err != nil || stored.Command != "exit 1" || stored.Report != "skipped" {
		t.Fatalf("run: %+v %v", stored, err)
	}
}

func TestActionInterruptedByRestartReports(t *testing.T) {
	t.Parallel()
	s, database, id := newReportingConversationFixture(t, "make release", false)
	run, _, _, err := database.BeginAction(t.Context(), id, "run")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.InterruptActionRuns(t.Context()); err != nil { // as at startup
		t.Fatal(err)
	}
	s.advanceActionReports(t.Context())
	reports := awaitActionReports(t, s, id, 1)
	if len(reports) != 1 || reports[0].ActionRunID != run.ID || reports[0].Status != "interrupted" ||
		!strings.Contains(reports[0].Text, "Its outcome is unknown") {
		t.Fatalf("reports: %+v", reports)
	}
}

func TestOutputTail(t *testing.T) {
	var b strings.Builder
	for i := range 300 {
		b.WriteString(strings.Repeat("x", i%3) + "\n")
	}
	tail, truncated := textTail(b.String(), reportTailLines, reportTailBytes)
	if !truncated || strings.Count(tail, "\n") != reportTailLines-1 {
		t.Fatalf("line bound: %d %v", strings.Count(tail, "\n"), truncated)
	}
	tail, truncated = textTail(strings.Repeat("y", 40<<10)+"\nlast\n", reportTailLines, reportTailBytes)
	if !truncated || tail != "last" {
		t.Fatalf("byte bound: %q %v", tail[:min(len(tail), 20)], truncated)
	}
	if tail, truncated := textTail("a\r\r\nb 10%\rb 100%\n", reportTailLines, reportTailBytes); truncated || tail != "a\nb 100%" {
		t.Fatalf("carriage returns: %q", tail)
	}
}

// A report queued before a restart is delivered at startup.
func TestQueuedActionReportRecovers(t *testing.T) {
	t.Parallel()
	s, database, id := newReportingConversationFixture(t, "exit 1", false)
	run, _, _, err := database.BeginAction(t.Context(), id, "run")
	if err != nil {
		t.Fatal(err)
	}
	code := 1
	if err := database.FinishAction(t.Context(), run.ID, &code, ""); err != nil {
		t.Fatal(err)
	}
	userData, err := json.Marshal(actionUserData{ActionRunID: run.ID, Report: "result", Text: "left queued"})
	if err != nil {
		t.Fatal(err)
	}
	llmJSON, err := json.Marshal(llm.UserStringMessage("left queued"))
	if err != nil {
		t.Fatal(err)
	}
	if queued, err := database.QueueActionReport(t.Context(), run.ID, db.QueuedMessage{
		ID: "report", Llm: llmJSON, CreatedAt: time.Now().UTC(), Model: "predictable", UserData: userData, Inject: true,
	}); err != nil || !queued {
		t.Fatalf("queue: %v %v", queued, err)
	}
	s.recoverChannelQueues(t.Context())
	if reports := awaitActionReports(t, s, id, 1); len(reports) != 1 || reports[0].Text != "left queued" {
		t.Fatalf("reports: %+v", reports)
	}
}
