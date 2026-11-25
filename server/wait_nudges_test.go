package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func TestNudgesDue(t *testing.T) {
	for _, tc := range []struct {
		elapsed time.Duration
		want    int64
	}{
		{59 * time.Minute, 0},
		{60 * time.Minute, 1},
		{119 * time.Minute, 1},
		{120 * time.Minute, 2},
		{240 * time.Minute, 3},
		{479 * time.Minute, 3},
		{480 * time.Minute, 4},
		{math.MaxInt64, 22}, // and no overflow
	} {
		if got := nudgesDue(time.Hour, tc.elapsed); got != tc.want {
			t.Errorf("nudgesDue(1h, %s) = %d, want %d", tc.elapsed, got, tc.want)
		}
	}
	if got := nudgesDue(0, time.Hour); got != 0 {
		t.Errorf("nudgesDue(0, 1h) = %d, want 0", got)
	}
}

// waitNudges returns the wait nudges delivered to conversationID.
func waitNudges(t *testing.T, database *db.DB, conversationID string) []waitNudgeUserData {
	t.Helper()
	var out []waitNudgeUserData
	for _, m := range listMessages(t, database, conversationID) {
		if m.Type != string(db.MessageTypeUser) || m.UserData == nil {
			continue
		}
		var data waitNudgeUserData
		if err := json.Unmarshal([]byte(*m.UserData), &data); err != nil {
			t.Fatal(err)
		}
		if data.Kind != "" {
			out = append(out, data)
		}
	}
	return out
}

// sweepWaitNudges sends the nudges due at now and waits for conversationID
// to take them.
func sweepWaitNudges(t *testing.T, s *Server, conversationID string, now time.Time) {
	t.Helper()
	s.sendWaitNudges(t.Context(), now)
	cm, err := s.getOrCreateConversationManager(t.Context(), conversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		q, err := s.db.GetQueuedMessages(t.Context(), conversationID)
		return err == nil && len(q) == 0 && !cm.IsAgentWorking()
	})
}

func sentWaitNudges(t *testing.T, database *db.DB, conversationID string, kind waitNudgeKind, subject string) int64 {
	t.Helper()
	var row generated.WaitNudge
	err := database.Queries(t.Context(), func(q *generated.Queries) (err error) {
		row, err = q.GetWaitNudge(t.Context(), generated.GetWaitNudgeParams{ConversationID: conversationID, Kind: string(kind), Subject: subject})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return row.Sent
}

// A download that stalls keeps its job running with nothing new in its log.
// Its conversation hears about it an hour in, and again at two, four, ...
// hours, but never twice for one step, nor once the job has exited.
func TestWaitNudgeStalledDownload(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // keep the job log out of /tmp
	server, database, llmSvc := newTestServer(t)
	defer stopActiveConversationLoops(server)

	// A server that accepts the download and never sends a byte.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	conns := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			conns <- c
		}
	}()
	command := fmt.Sprintf("echo downloading; exec 3<>/dev/tcp/127.0.0.1/%d; cat <&3", ln.Addr().(*net.TCPAddr).Port)

	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	postChatMessage(t, server, id, "bash-bg: "+command)
	waitForMessageContaining(t, database, id, "background job", 10*time.Second)
	waitForIdle(t, server, id)
	conn := <-conns
	jobs, err := server.runningBackgroundJobsOf(t.Context(), id)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("running jobs = %v, %v", jobs, err)
	}
	job := jobs[0]

	sweepWaitNudges(t, server, id, job.StartedAt.Add(59*time.Minute))
	if n := len(waitNudges(t, database, id)); n != 0 {
		t.Fatalf("%d nudges before an hour", n)
	}
	sweepWaitNudges(t, server, id, job.StartedAt.Add(61*time.Minute))
	nudges := waitNudges(t, database, id)
	if len(nudges) != 1 {
		t.Fatalf("%d nudges after an hour, want 1", len(nudges))
	}
	n := nudges[0]
	for _, want := range []string{
		"Background job " + job.ID + " is still running after 1h1m.",
		"Command: " + command,
		"Log: " + job.LogPath + " (" + fmt.Sprint(len("downloading\n")) + " bytes, last written 1h1m ago)",
		"\n\ndownloading\n\n",
		fmt.Sprintf("kill it with `kill -- -%d`", job.PID),
		"if it still runs at 2h0m, you will hear again.",
	} {
		if !strings.Contains(n.Text, want) {
			t.Errorf("nudge lacks %q:\n%s", want, n.Text)
		}
	}
	if n.Kind != waitNudgeJob || n.Subject != job.ID {
		t.Errorf("nudge = %+v", n)
	}
	// The model sees it attributed.
	sawTag := false
	for _, req := range llmSvc.GetRecentRequests() {
		for _, msg := range req.Messages {
			if msg.Role == llm.MessageRoleUser && strings.Contains(messageText(msg), `<wait_nudge kind="background_job" id="`+job.ID+`">`) {
				sawTag = true
			}
		}
	}
	if !sawTag {
		t.Error("the model did not see the nudge")
	}

	// One nudge per step, and one for steps missed together.
	sweepWaitNudges(t, server, id, job.StartedAt.Add(62*time.Minute))
	sweepWaitNudges(t, server, id, job.StartedAt.Add(5*time.Hour))
	sweepWaitNudges(t, server, id, job.StartedAt.Add(5*time.Hour+time.Minute))
	if n := len(waitNudges(t, database, id)); n != 2 {
		t.Fatalf("%d nudges after 5h, want 2", n)
	}
	if sent := sentWaitNudges(t, database, id, waitNudgeJob, job.ID); sent != 3 {
		t.Errorf("recorded %d nudges sent at 5h, want 3 (1h, 2h, 4h)", sent)
	}

	// Once the download ends, its exit is all the conversation hears.
	conn.Write([]byte("done\n"))
	conn.Close()
	waitFor(t, 10*time.Second, func() bool { return len(backgroundJobNotices(t, database, id)) == 1 })
	waitForIdle(t, server, id)
	sweepWaitNudges(t, server, id, job.StartedAt.Add(10*time.Hour))
	if n := len(waitNudges(t, database, id)); n != 2 {
		t.Fatalf("%d nudges after the job exited, want 2", n)
	}
}

// recordFakeJob records a job running in conversationID since startedAt,
// with no process behind it: nudges read only its record and files.
func recordFakeJob(t *testing.T, s *Server, conversationID string, startedAt time.Time) claudetool.BackgroundJob {
	t.Helper()
	dir := t.TempDir()
	job := claudetool.BackgroundJob{
		ID:             "job-" + conversationID,
		ConversationID: conversationID,
		Command:        "sleep infinity",
		PID:            1 << 22,
		LogPath:        filepath.Join(dir, "log"),
		ExitPath:       filepath.Join(dir, "exit"),
		StartedAt:      startedAt,
	}
	if err := os.WriteFile(job.LogPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.recordBackgroundJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	return job
}

// Jobs of archived or stopped conversations, or of their subagents, and
// jobs that exited are not nudged about; a nudge still queued stands for later ones,
// and is taken after a restart; what was sent survives the restart; and a
// sweep with nothing due writes nothing.
func TestWaitNudgeJobSkips(t *testing.T) {
	server, database, ps := newTestServer(t)
	ctx := t.Context()
	newConv := func() string {
		c, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return c.ConversationID
	}
	start := time.Now()
	later := start.Add(61 * time.Minute)

	archived := newConv()
	archivedJob := recordFakeJob(t, server, archived, start)
	if _, err := database.ArchiveConversation(ctx, archived); err != nil {
		t.Fatal(err)
	}
	sub, err := database.CreateSubagentConversation(ctx, "sub", archived, nil)
	if err != nil {
		t.Fatal(err)
	}
	recordFakeJob(t, server, sub.ConversationID, start)
	exited := newConv()
	if err := os.WriteFile(recordFakeJob(t, server, exited, start).ExitPath, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	queued := newConv()
	queuedJob := recordFakeJob(t, server, queued, start)
	data, _ := json.Marshal(waitNudgeUserData{Kind: waitNudgeJob, Subject: queuedJob.ID, Text: "earlier nudge"})
	msg, _ := json.Marshal(llm.UserStringMessage("earlier nudge"))
	if _, err := database.AppendQueuedMessage(ctx, queued, db.QueuedMessage{ID: "q1", Llm: msg, CreatedAt: start, Model: "predictable", UserData: data, Inject: true}, db.SentDraft{}); err != nil {
		t.Fatal(err)
	}
	stopped := newConv()
	stoppedJob := recordFakeJob(t, server, stopped, start)
	server.mu.Lock()
	server.stoppedParents[stopped] = true
	server.mu.Unlock()
	stoppedSub, err := database.CreateSubagentConversation(ctx, "stopped-sub", stopped, nil)
	if err != nil {
		t.Fatal(err)
	}
	recordFakeJob(t, server, stoppedSub.ConversationID, start)
	running := newConv()
	runningJob := recordFakeJob(t, server, running, start)

	sweepWaitNudges(t, server, running, later)
	for _, id := range []string{archived, sub.ConversationID, exited, queued, stopped, stoppedSub.ConversationID} {
		if n := len(waitNudges(t, database, id)); n != 0 {
			t.Errorf("%d nudges in %s", n, id)
		}
	}
	if q, err := database.GetQueuedMessages(ctx, queued); err != nil || len(q) != 1 {
		t.Errorf("queue = %v, %v; want the earlier nudge alone", q, err)
	}
	if sent := sentWaitNudges(t, database, queued, waitNudgeJob, queuedJob.ID); sent != 1 {
		t.Errorf("recorded %d nudges for the queued one, want 1", sent)
	}
	if sent := sentWaitNudges(t, database, archived, waitNudgeJob, archivedJob.ID); sent != 1 {
		t.Errorf("recorded %d nudges skipped in the archived conversation, want 1", sent)
	}
	if sent := sentWaitNudges(t, database, stopped, waitNudgeJob, stoppedJob.ID); sent != 1 {
		t.Errorf("recorded %d nudges skipped in the stopped conversation, want 1", sent)
	}
	var commits atomic.Int64
	database.Pool().OnCommit(func() { commits.Add(1) })
	server.sendWaitNudges(ctx, later.Add(time.Minute))
	if commits := commits.Load(); commits != 0 {
		t.Errorf("a sweep with nothing due made %d commits", commits)
	}
	if n := waitNudges(t, database, running); len(n) != 1 || !strings.Contains(n[0].Text, "(empty)") {
		t.Fatalf("nudges = %+v, want one about an empty log", n)
	}

	// A restarted Shelley neither repeats a nudge nor skips one.
	stopActiveConversationLoops(server)
	restarted := NewServer(database, &testLLMManager{service: ps}, claudetool.ToolSetConfig{},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})), true, "predictable", "")
	restarted.hooksDir = t.TempDir()
	defer stopActiveConversationLoops(restarted)
	restarted.recoverUnwatchedQueues(ctx)
	waitFor(t, 10*time.Second, func() bool { return len(waitNudges(t, database, queued)) == 1 })
	waitForIdle(t, restarted, queued)
	sweepWaitNudges(t, restarted, running, later.Add(time.Minute))
	if n := len(waitNudges(t, database, running)); n != 1 {
		t.Fatalf("%d nudges after restart, want 1", n)
	}
	sweepWaitNudges(t, restarted, running, start.Add(2*time.Hour))
	if n := len(waitNudges(t, database, running)); n != 2 {
		t.Fatalf("%d nudges at 2h, want 2", n)
	}
	if sent := sentWaitNudges(t, database, running, waitNudgeJob, runningJob.ID); sent != 2 {
		t.Errorf("recorded %d nudges, want 2", sent)
	}
}

// A parent hears about a subagent that owes it a report and has been
// silent mid-turn for an hour, and again at two hours; a new message starts
// the silence over. A parent owed nothing, archived or stopped hears
// nothing, nor does one whose subagent is idle.
func TestWaitNudgeSilentSubagent(t *testing.T) {
	f := newSubagentDoneFixture(t, "Earlier reply.")
	defer stopActiveConversationLoops(f.server)
	ctx := t.Context()
	start := time.Now()
	parentNudges := func() []waitNudgeUserData { return waitNudges(t, f.database, f.parentID) }
	addMessage := func(typ db.MessageType, msg llm.Message, userData any) {
		t.Helper()
		if _, err := f.database.CreateMessage(ctx, db.CreateMessageParams{ConversationID: f.subagentID, Type: typ, LLMData: msg, UserData: userData}); err != nil {
			t.Fatal(err)
		}
	}
	setWorking := func(working bool) {
		t.Helper()
		if err := f.database.SetConversationAgentWorking(ctx, f.subagentID, working); err != nil {
			t.Fatal(err)
		}
	}

	// Working, but owing nothing. (Its reply is a minute old, so the
	// messages below, though written within a second, are news.)
	if err := f.database.Pool().Exec(ctx, "UPDATE messages SET created_at = datetime(created_at, '-1 minute') WHERE conversation_id = ?", f.subagentID); err != nil {
		t.Fatal(err)
	}
	setWorking(true)
	sweepWaitNudges(t, f.server, f.parentID, start.Add(61*time.Minute))
	if n := len(parentNudges()); n != 0 {
		t.Fatalf("%d nudges about a subagent owing nothing", n)
	}

	// Asked for a report, it goes silent mid-turn.
	addMessage(db.MessageTypeUser, llm.UserStringMessage("Run the full suite."),
		senderMessageUserData{SenderConversationID: f.parentID, SenderRelationship: senderRelationshipParent, Text: "Run the full suite."})
	addMessage(db.MessageTypeAgent, llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Running the full suite now."}}}, nil)
	sweepWaitNudges(t, f.server, f.parentID, start.Add(59*time.Minute))
	if n := len(parentNudges()); n != 0 {
		t.Fatalf("%d nudges before an hour", n)
	}
	sweepWaitNudges(t, f.server, f.parentID, start.Add(61*time.Minute))
	sweepWaitNudges(t, f.server, f.parentID, start.Add(62*time.Minute))
	nudges := parentNudges()
	if len(nudges) != 1 {
		t.Fatalf("%d nudges after an hour, want 1", len(nudges))
	}
	n := nudges[0]
	for _, want := range []string{
		"Subagent " + f.subSlug + " is mid-turn and has been silent for 1h1m; it owes you a report.",
		"Its latest message (agent): Running the full suite now.",
		"in conversation " + f.subagentID + ":",
		"if it is still silent at 2h0m, you will hear again.",
	} {
		if !strings.Contains(n.Text, want) {
			t.Errorf("nudge lacks %q:\n%s", want, n.Text)
		}
	}
	if n.Kind != waitNudgeSubagent || n.Subject != f.subagentID || n.Slug != f.subSlug {
		t.Errorf("nudge = %+v", n)
	}

	// Idle, it is nudged by what it waits on, not its parent.
	setWorking(false)
	sweepWaitNudges(t, f.server, f.parentID, start.Add(2*time.Hour+time.Minute))
	if n := len(parentNudges()); n != 1 {
		t.Fatalf("%d nudges about an idle subagent, want 1", n)
	}
	setWorking(true)
	sweepWaitNudges(t, f.server, f.parentID, start.Add(2*time.Hour+time.Minute))
	if n := len(parentNudges()); n != 2 {
		t.Fatalf("%d nudges at 2h, want 2", n)
	}

	// A message at 2h30m starts the silence over.
	addMessage(db.MessageTypeAgent, llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Still going."}}}, nil)
	if err := f.database.Pool().Exec(ctx, "UPDATE messages SET created_at = datetime(created_at, '+150 minutes') WHERE conversation_id = ? AND sequence_id = (SELECT MAX(sequence_id) FROM messages WHERE conversation_id = ?)", f.subagentID, f.subagentID); err != nil {
		t.Fatal(err)
	}
	sweepWaitNudges(t, f.server, f.parentID, start.Add(3*time.Hour+29*time.Minute))
	if n := len(parentNudges()); n != 2 {
		t.Fatalf("%d nudges within an hour of a new message, want 2", n)
	}
	sweepWaitNudges(t, f.server, f.parentID, start.Add(3*time.Hour+31*time.Minute))
	if n := parentNudges(); len(n) != 3 || !strings.Contains(n[2].Text, "Still going.") {
		t.Fatalf("nudges = %+v, want a third about the new message", n)
	}

	// Stopped at 4h30m, then archived at 6h30m.
	f.server.mu.Lock()
	f.server.stoppedParents[f.parentID] = true
	f.server.mu.Unlock()
	sweepWaitNudges(t, f.server, f.parentID, start.Add(4*time.Hour+31*time.Minute))
	f.server.mu.Lock()
	delete(f.server.stoppedParents, f.parentID)
	f.server.mu.Unlock()
	if _, err := f.database.ArchiveConversation(ctx, f.parentID); err != nil {
		t.Fatal(err)
	}
	sweepWaitNudges(t, f.server, f.parentID, start.Add(6*time.Hour+31*time.Minute))
	if n := len(parentNudges()); n != 3 {
		t.Fatalf("%d nudges, want 3", n)
	}
	if sent := sentWaitNudges(t, f.database, f.parentID, waitNudgeSubagent, f.subagentID); sent != 3 {
		t.Errorf("recorded %d nudges, want 3: skipped ones count", sent)
	}
}

// A parent's message queued behind a stuck turn is owed a report, and the
// silence counts from it.
func TestWaitNudgeQueuedDelegation(t *testing.T) {
	f := newSubagentDoneFixture(t, "Done with the first task.")
	defer stopActiveConversationLoops(f.server)
	ctx := t.Context()
	start := time.Now()
	if err := f.database.Pool().Exec(ctx, "UPDATE messages SET created_at = datetime(created_at, '-1 minute') WHERE conversation_id = ?", f.subagentID); err != nil {
		t.Fatal(err)
	}
	if err := f.database.SetConversationAgentWorking(ctx, f.subagentID, true); err != nil {
		t.Fatal(err)
	}
	sweepWaitNudges(t, f.server, f.parentID, start.Add(61*time.Minute))

	data, _ := json.Marshal(senderMessageUserData{SenderConversationID: f.parentID, SenderRelationship: senderRelationshipParent, Text: "Now the second."})
	msg, _ := json.Marshal(llm.UserStringMessage("Now the second."))
	if _, err := f.database.AppendQueuedMessage(ctx, f.subagentID, db.QueuedMessage{ID: "q1", Llm: msg, CreatedAt: start.Add(2 * time.Hour), Model: "predictable", UserData: data}, db.SentDraft{}); err != nil {
		t.Fatal(err)
	}
	sweepWaitNudges(t, f.server, f.parentID, start.Add(2*time.Hour+59*time.Minute))
	if n := len(waitNudges(t, f.database, f.parentID)); n != 0 {
		t.Fatalf("%d nudges within an hour of the parent's message", n)
	}
	sweepWaitNudges(t, f.server, f.parentID, start.Add(3*time.Hour+time.Minute))
	if n := waitNudges(t, f.database, f.parentID); len(n) != 1 || !strings.Contains(n[0].Text, "silent for 3h2m; it owes you a report. Your message of 1h1m ago is still queued behind its turn.") {
		t.Fatalf("nudges = %+v, want one an hour after the parent's message", n)
	}
}
