package server

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// These tests cover the report sent for a delegated subagent's turn that
// ends without message_parent(end_turn=true): see reportUnreportedTurnEnd.

// subagentMessages returns the parent's messages attributed to a subagent,
// in order, as their raw text.
func subagentMessages(t *testing.T, database *db.DB, parentID string) []string {
	t.Helper()
	var out []string
	for _, m := range listMessages(t, database, parentID) {
		if m.Type != string(db.MessageTypeUser) || m.UserData == nil {
			continue
		}
		data, ok, err := parseSenderMessageUserData([]byte(*m.UserData))
		if err != nil || !ok {
			continue
		}
		out = append(out, data.Text)
	}
	return out
}

// waitSubagentMessages waits for the parent to hold n messages from its
// subagents and returns them.
func waitSubagentMessages(t *testing.T, database *db.DB, parentID string, n int) []string {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool { return len(subagentMessages(t, database, parentID)) >= n })
	return subagentMessages(t, database, parentID)
}

// settleSubagentMessages waits a moment for a report that should not come
// and returns the parent's messages from its subagents, with its queue.
func settleSubagentMessages(t *testing.T, database *db.DB, parentID string) []string {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	msgs := subagentMessages(t, database, parentID)
	for _, qm := range queuedMessages(t, database, parentID) {
		msgs = append(msgs, string(qm.Llm))
	}
	return msgs
}

func wrappedSubagentMessage(childID, slug, text string) string {
	return `<subagent_message conversation_id="` + childID + `" slug="` + slug + `">` + "\n" + text + "\n</subagent_message>"
}

// A turn the parent started that ends with a plain reply is reported to the
// parent as the subagent's final report, carrying the reply; a turn the user
// drove in the subagent's conversation is not.
func TestSubagentTurnEndReportsParentStartedTurn(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	ctx := t.Context()
	child, err := database.CreateSubagentConversation(ctx, "quiet-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSubagentRunner(server).RunSubagent(ctx, child.ConversationID, "echo: delegated turn", "predictable", ""); err != nil {
		t.Fatal(err)
	}
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, "echo: delegated turn"))

	report := `[Subagent "quiet-child" ended its turn without a final report; its last reply follows.]` + "\ndelegated turn"
	releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, wrappedSubagentMessage(child.ConversationID, "quiet-child", report)))
	message := userMessageContaining(t, database, parent.ConversationID, "delegated turn")
	if message == nil {
		t.Fatal("turn-end report not persisted in the parent")
	}
	assertSenderSource(t, storedUserData(t, message), child.ConversationID, "quiet-child", "subagent", report)

	// The user drives the child directly: its turn end is the user's business.
	response := postBtwChat(t, server, child.ConversationID,
		ChatRequest{Message: "echo: manual child turn", Model: "predictable"})
	if response.Code != 202 {
		t.Fatalf("manual child turn status=%d body=%s", response.Code, response.Body.String())
	}
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, "echo: manual child turn"))
	if got := settleSubagentMessages(t, database, parent.ConversationID); len(got) != 1 {
		t.Fatalf("user-driven child turn woke the parent: %q", got)
	}

	// The parent asks again: that turn is reported again.
	if _, err := NewSubagentRunner(server).RunSubagent(ctx, child.ConversationID, "echo: second delegated turn", "predictable", ""); err != nil {
		t.Fatal(err)
	}
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, "echo: second delegated turn"))
	report = `[Subagent "quiet-child" ended its turn without a final report; its last reply follows.]` + "\nsecond delegated turn"
	releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, wrappedSubagentMessage(child.ConversationID, "quiet-child", report)))
	if got := subagentMessages(t, database, parent.ConversationID); len(got) != 2 || got[1] != report {
		t.Fatalf("parent messages = %q", got)
	}
}

// A turn that sent its final report with message_parent(end_turn=true) is
// not reported again when it ends.
func TestSubagentTurnEndAfterFinalReportIsNotReported(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	ctx := t.Context()
	child, err := database.CreateSubagentConversation(ctx, "reporter", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "message_parent_final: finished task"
	if _, err := NewSubagentRunner(server).RunSubagent(ctx, child.ConversationID, prompt, "predictable", ""); err != nil {
		t.Fatal(err)
	}
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, prompt))
	releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, wrappedSubagentMessage(child.ConversationID, "reporter", "finished task")))
	if got := settleSubagentMessages(t, database, parent.ConversationID); len(got) != 1 || got[0] != "finished task" {
		t.Fatalf("parent messages = %q, want the final report only", got)
	}
}

// A progress report is not a final report: a turn that sent one and then
// ended with a plain reply is reported, and the report says so.
func TestSubagentTurnEndAfterProgressReportIsReported(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	ctx := t.Context()
	child, err := database.CreateSubagentConversation(ctx, "progress", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "message_parent: halfway"
	if _, err := NewSubagentRunner(server).RunSubagent(ctx, child.ConversationID, prompt, "predictable", ""); err != nil {
		t.Fatal(err)
	}
	held.waitCall(t, prompt).Release()
	releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, wrappedSubagentMessage(child.ConversationID, "progress", "halfway")))
	// The child's tool-result round ends its turn with a plain reply.
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, prompt))
	report := `[Subagent "progress" ended its turn without a final report; its last message_parent was a progress report. Its last reply follows.]` + "\nDone."
	releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, wrappedSubagentMessage(child.ConversationID, "progress", report)))
	if got := subagentMessages(t, database, parent.ConversationID); len(got) != 2 || got[0] != "halfway" || got[1] != report {
		t.Fatalf("parent messages = %q", got)
	}
}

// A turn that ends with a background job still running is only paused: its
// exit notice resumes the turn, and the report comes when that one ends.
func TestSubagentTurnEndWaitingOnBackgroundJobIsNotReported(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // keep the job log out of /tmp
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(ctx, "runner", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(t.TempDir(), "gate")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSubagentRunner(server).RunSubagent(ctx, child.ConversationID, "bash-bg: echo started; read -r _ < "+gate, "predictable", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		return countByType(listMessages(t, database, child.ConversationID), db.MessageTypeAgent) == 2 && !server.IsAgentWorking(child.ConversationID)
	})
	if n := listedRunningJobs(t, server, child.ConversationID); n != 1 {
		t.Fatalf("%d running jobs, want 1", n)
	}
	if got := settleSubagentMessages(t, database, parent.ConversationID); len(got) != 0 {
		t.Fatalf("turn paused on a background job woke the parent: %q", got)
	}

	// The job exits; its notice resumes the turn, which ends with a reply.
	if err := os.WriteFile(gate, []byte("go\n"), 0); err != nil {
		t.Fatal(err)
	}
	got := waitSubagentMessages(t, database, parent.ConversationID, 1)
	if len(got) != 1 || !strings.HasPrefix(got[0], `[Subagent "runner" ended its turn without a final report; its last reply follows.]`+"\n") {
		t.Fatalf("parent messages = %q", got)
	}
	if n := len(backgroundJobNotices(t, database, child.ConversationID)); n != 1 {
		t.Fatalf("%d job notices in the child, want 1", n)
	}
}

// A cancelled turn is not reported; the next delegated turn is.
func TestSubagentTurnCancelledIsNotReported(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	ctx := t.Context()
	child, err := database.CreateSubagentConversation(ctx, "cancelled", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSubagentRunner(server).RunSubagent(ctx, child.ConversationID, "echo: cancelled turn", "predictable", ""); err != nil {
		t.Fatal(err)
	}
	held.waitCall(t, "echo: cancelled turn") // the turn is in flight
	cancelConversation(t, server, child.ConversationID)
	if !hasCancelledEndOfTurn(t, database, child.ConversationID) {
		t.Fatal("child turn was not cancelled")
	}
	if got := settleSubagentMessages(t, database, parent.ConversationID); len(got) != 0 {
		t.Fatalf("cancelled child turn woke the parent: %q", got)
	}

	if _, err := NewSubagentRunner(server).RunSubagent(ctx, child.ConversationID, "echo: next turn", "predictable", ""); err != nil {
		t.Fatal(err)
	}
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, "echo: next turn"))
	report := `[Subagent "cancelled" ended its turn without a final report; its last reply follows.]` + "\nnext turn"
	releaseAndWaitIdle(t, server, parent.ConversationID, held.waitCall(t, wrappedSubagentMessage(child.ConversationID, "cancelled", report)))
	if got := subagentMessages(t, database, parent.ConversationID); len(got) != 1 || got[0] != report {
		t.Fatalf("parent messages = %q", got)
	}
}

// A delegated turn that ends in an error is reported as such, with the error.
func TestSubagentTurnEndWithErrorIsReported(t *testing.T) {
	server, database, held, parent := newBtwTest(t)
	ctx := t.Context()
	child, err := database.CreateSubagentConversation(ctx, "refused", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSubagentRunner(server).RunSubagent(ctx, child.ConversationID, "refusal", "predictable", ""); err != nil {
		t.Fatal(err)
	}
	releaseAndWaitIdle(t, server, child.ConversationID, held.waitCall(t, "refusal"))
	got := waitSubagentMessages(t, database, parent.ConversationID, 1)
	if len(got) != 1 || !strings.HasPrefix(got[0], `[Subagent "refused" ended its turn with an error and no final report; the error follows.]`+"\n") {
		t.Fatalf("parent messages = %q", got)
	}
	visibleError := false
	for _, m := range listMessages(t, database, child.ConversationID) {
		visibleError = visibleError || m.Type == string(db.MessageTypeError)
	}
	if !visibleError {
		t.Fatal("child has no visible error")
	}
}

// A long reply is cut to turnEndReportLimit and says so.
func TestTurnEndReportTruncates(t *testing.T) {
	long := strings.Repeat("é", turnEndReportLimit) // 2 bytes each: cut mid-rune territory
	report := turnEndReport("big", llm.UserStringMessage(long), db.MessageTypeAgent, delegatedTurnState{parentStarted: true})
	if !strings.HasPrefix(report, `[Subagent "big" ended its turn without a final report; its last reply follows.]`+"\n") {
		t.Fatalf("report = %q", report)
	}
	if !strings.Contains(report, "[... truncated; ") || !strings.Contains(report, " more characters in the subagent's conversation]") {
		t.Fatalf("report lacks the truncation note: %q", report[len(report)-100:])
	}
	if !utf8.ValidString(report) || len(report) > turnEndReportLimit+200 {
		t.Fatalf("report cut badly: len=%d", len(report))
	}
	if short := turnEndReport("small", llm.UserStringMessage(" fine \n"), db.MessageTypeAgent, delegatedTurnState{}); short != `[Subagent "small" ended its turn without a final report; its last reply follows.]`+"\nfine" {
		t.Fatalf("short report = %q", short)
	}
}
