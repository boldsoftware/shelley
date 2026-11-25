package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// backgroundJobNotices returns the conversation's background job notices.
func backgroundJobNotices(t *testing.T, database *db.DB, conversationID string) []backgroundJobUserData {
	t.Helper()
	msgs, err := database.ListMessages(t.Context(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	var out []backgroundJobUserData
	for _, m := range msgs {
		if m.Type != string(db.MessageTypeUser) || m.UserData == nil {
			continue
		}
		var data backgroundJobUserData
		if err := json.Unmarshal([]byte(*m.UserData), &data); err != nil {
			t.Fatal(err)
		}
		if data.BackgroundJobID != "" {
			out = append(out, data)
		}
	}
	return out
}

// A command that outlives the foreground threshold returns a backgrounded
// result, and its exit later wakes the idle conversation with exactly one
// attributed notice.
func TestBashBackgroundJobNotifiesConversation(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // keep the job log out of /tmp
	server, database, llmSvc := newTestServer(t)
	defer stopActiveConversationLoops(server)
	server.toolSetConfig.BashBackgroundAfter = 50 * time.Millisecond

	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	gate := filepath.Join(t.TempDir(), "gate")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}

	postChatMessage(t, server, id, "bash: echo started; read -r _ < "+gate+"; echo finished")
	waitForMessageContaining(t, database, id, "moved to background job", 10*time.Second)
	waitForIdle(t, server, id)
	if n := len(backgroundJobNotices(t, database, id)); n != 0 {
		t.Fatalf("%d notices before the job exited", n)
	}

	if err := os.WriteFile(gate, []byte("go\n"), 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { return len(backgroundJobNotices(t, database, id)) == 1 })
	notice := backgroundJobNotices(t, database, id)[0]
	if !strings.Contains(notice.Text, "finished: exit 0") || !strings.Contains(notice.Text, "started\nfinished") {
		t.Errorf("notice = %q", notice.Text)
	}
	if notice.Command != "echo started; read -r _ < "+gate+"; echo finished" || notice.ExitCode == nil || *notice.ExitCode != 0 ||
		notice.Duration == "" || notice.LogPath == "" || notice.Tail != "started\nfinished" {
		t.Errorf("notice = %+v", notice)
	}
	// The notice starts a turn in which the model sees it attributed.
	waitFor(t, 10*time.Second, func() bool {
		for _, req := range llmSvc.GetRecentRequests() {
			for _, msg := range req.Messages {
				if msg.Role == llm.MessageRoleUser && strings.Contains(messageText(msg), `<background_job id="`+notice.BackgroundJobID+`">`) {
					return true
				}
			}
		}
		return false
	})
	waitForIdle(t, server, id)
	waitForMessageContaining(t, database, id, "Noted: the background job finished.", 10*time.Second)
	if n := len(backgroundJobNotices(t, database, id)); n != 1 {
		t.Fatalf("%d notices, want 1", n)
	}
	if jobs := unfinishedBackgroundJobs(t, database); len(jobs) != 0 {
		t.Fatalf("unfinished jobs after notice: %+v", jobs)
	}
}

func unfinishedBackgroundJobs(t *testing.T, database *db.DB) []generated.BackgroundJob {
	t.Helper()
	var rows []generated.BackgroundJob
	err := database.Queries(t.Context(), func(q *generated.Queries) error {
		var err error
		rows, err = q.ListUnfinishedBackgroundJobs(t.Context())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// stashedJobs is a BackgroundJobs that only hands jobs to the test, like a
// Shelley process that backgrounded them and then died before reporting.
type stashedJobs chan backgroundedJob

type backgroundedJob struct {
	job    claudetool.BackgroundJob
	exited <-chan struct{}
}

func (j stashedJobs) Background(ctx context.Context, job claudetool.BackgroundJob, exited <-chan struct{}) error {
	j <- backgroundedJob{job, exited}
	return nil
}

// After a restart, every job the previous process backgrounded but did not
// report is reported exactly once: at once if it was seen to exit, finished
// or vanished meanwhile, and when it exits if it is still running.
func TestBackgroundJobRecoveryAfterRestart(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // keep job logs out of /tmp
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID

	// The previous process: real jobs, recorded but never reported.
	stash := make(stashedJobs, 1)
	bash := (&claudetool.BashTool{
		WorkingDir:      claudetool.NewMutableWorkingDir(t.TempDir()),
		Env:             claudetool.ShelleyEnv{ConversationID: id},
		Jobs:            stash,
		BackgroundAfter: time.Millisecond,
	}).Tool()
	start := func(name string) (backgroundedJob, string) {
		t.Helper()
		gate := filepath.Join(t.TempDir(), "gate")
		if err := syscall.Mkfifo(gate, 0o600); err != nil {
			t.Fatal(err)
		}
		input, _ := json.Marshal(map[string]string{"command": "read -r _ < " + gate + "; echo " + name})
		if out := bash.Run(t.Context(), input); out.Error != nil {
			t.Fatal(out.Error)
		}
		bg := <-stash
		if err := server.recordBackgroundJob(t.Context(), bg.job); err != nil {
			t.Fatal(err)
		}
		return bg, gate
	}
	release := func(gate string) {
		t.Helper()
		if err := os.WriteFile(gate, []byte("go\n"), 0); err != nil {
			t.Fatal(err)
		}
	}
	finished, gate := start("finished-output")
	release(gate)
	<-finished.exited
	lost, gate := start("lost-output")
	release(gate)
	<-lost.exited
	if err := os.Remove(lost.job.ExitPath); err != nil {
		t.Fatal(err)
	}
	running, runningGate := start("running-output")
	// Seen to exit but not reported, so not watched again. (Its process is in
	// fact still running, which shows that.)
	unreported, unreportedGate := start("unreported-output")
	defer func() {
		release(unreportedGate)
		<-unreported.exited // before TempDir cleanup races its exit file
	}()
	if err := database.QueriesTx(t.Context(), func(q *generated.Queries) error {
		_, err := q.MarkBackgroundJobExited(t.Context(), unreported.job.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	noticeFor := func(job claudetool.BackgroundJob) backgroundJobUserData {
		for _, n := range backgroundJobNotices(t, database, id) {
			if n.BackgroundJobID == job.ID {
				return n
			}
		}
		return backgroundJobUserData{}
	}
	// What the previous process recorded is all there is to know: the
	// conversation list counts each job as running until it is seen to exit.
	if n := listedRunningJobs(t, server, id); n != 3 {
		t.Fatalf("listed %d running jobs before recovery, want 3", n)
	}
	if err := server.recoverBackgroundJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { return len(backgroundJobNotices(t, database, id)) == 3 })
	if noticeFor(unreported.job).Text == "" {
		t.Error("no notice for the job seen to exit")
	}
	if n := listedRunningJobs(t, server, id); n != 1 {
		t.Fatalf("listed %d running jobs after recovery, want 1", n)
	}
	if got := noticeFor(finished.job).Text; !strings.Contains(got, "finished: exit 0") || !strings.Contains(got, "finished-output") {
		t.Errorf("finished notice = %q", got)
	}
	if got := noticeFor(lost.job); !strings.Contains(got.Text, "lost (host rebooted or killed)") || got.ExitCode != nil || got.Duration != "" || got.Tail != "lost-output" {
		t.Errorf("lost notice = %+v", got)
	}

	release(runningGate)
	waitFor(t, 10*time.Second, func() bool { return noticeFor(running.job).Text != "" })
	if got := noticeFor(running.job).Text; !strings.Contains(got, "finished: exit 0") || !strings.Contains(got, "running-output") {
		t.Errorf("running notice = %q", got)
	}
	waitFor(t, 10*time.Second, func() bool { return len(unfinishedBackgroundJobs(t, database)) == 0 })
	if n := listedRunningJobs(t, server, id); n != 0 {
		t.Fatalf("listed %d running jobs after every exit, want 0", n)
	}

	// Another restart reports nothing again.
	if err := server.recoverBackgroundJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForIdle(t, server, id)
	if n := len(backgroundJobNotices(t, database, id)); n != 4 {
		t.Fatalf("%d notices, want 4", n)
	}
}

// listedRunningJobs returns the conversation list's running background job
// count for conversationID.
func listedRunningJobs(t *testing.T, server *Server, conversationID string) int64 {
	t.Helper()
	list, err := server.conversationListWithStateInternal(t.Context(), 100, 0, "", false, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.ConversationID == conversationID {
			return c.RunningBackgroundJobs
		}
	}
	t.Fatalf("conversation %s not listed", conversationID)
	return 0
}

// waitForListJobCount reads conversation list patch events until one sets
// the running background job count of the conversation at index 0 to want.
func waitForListJobCount(t *testing.T, next func() (ConversationListPatchEvent, bool), want int) {
	t.Helper()
	for {
		ev, ok := next()
		if !ok {
			t.Fatalf("list stream ended before running_background_jobs became %d", want)
		}
		for _, op := range ev.Patch {
			if op.Path == "/0/running_background_jobs" && string(op.Value) == strconv.Itoa(want) {
				return
			}
		}
	}
}

// The conversation list counts a conversation's running background jobs;
// killing one through the API ends it with exactly one notice and drops
// the count.
func TestBackgroundJobsListedAndKilled(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // keep the job log out of /tmp
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	call := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}

	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	_, next, release, err := server.conversationListStream.connect(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	gate := filepath.Join(t.TempDir(), "gate")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	postChatMessage(t, server, id, "bash-bg: echo started; read -r _ < "+gate)
	waitForListJobCount(t, next, 1)

	waitFor(t, 10*time.Second, func() bool {
		w := call("GET", "/api/conversation/"+id+"/background-jobs")
		var listed []BackgroundJobInfo
		return json.NewDecoder(w.Body).Decode(&listed) == nil &&
			len(listed) == 1 && strings.Contains(listed[0].Tail, "started")
	})
	w := call("GET", "/api/conversation/"+id+"/background-jobs")
	var jobs []BackgroundJobInfo
	if err := json.NewDecoder(w.Body).Decode(&jobs); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	if len(jobs) != 1 || !strings.Contains(jobs[0].Command, gate) || !strings.Contains(jobs[0].Tail, "started") {
		t.Fatalf("background jobs = %+v", jobs)
	}
	if strings.Contains(w.Body.String(), "pgid") || strings.Contains(w.Body.String(), "log_path") {
		t.Fatalf("list exposes internal job details: %s", w.Body.String())
	}
	job := jobs[0]

	if w := call("POST", "/api/conversation/"+id+"/background-jobs/"+job.JobID+"/kill"); w.Code != http.StatusNoContent {
		t.Fatalf("kill: %d %s", w.Code, w.Body.String())
	}
	waitForListJobCount(t, next, 0)
	waitFor(t, 10*time.Second, func() bool { return len(backgroundJobNotices(t, database, id)) == 1 })
	if notice := backgroundJobNotices(t, database, id)[0]; notice.BackgroundJobID != job.JobID || !strings.Contains(notice.Text, "finished: exit 143") {
		t.Errorf("notice = %+v", notice)
	}

	// The job is gone now, and an unknown job never existed.
	for _, jobID := range []string{job.JobID, "unknown"} {
		if w := call("POST", "/api/conversation/"+id+"/background-jobs/"+jobID+"/kill"); w.Code != http.StatusNotFound {
			t.Errorf("kill %s: %d %s, want 404", jobID, w.Code, w.Body.String())
		}
	}
	waitForIdle(t, server, id)
	if n := len(backgroundJobNotices(t, database, id)); n != 1 {
		t.Fatalf("%d notices, want 1", n)
	}
}

// A job whose PID now names another process is never signalled; stopping
// it reports it lost, once.
func TestKillBackgroundJobWithReusedPID(t *testing.T) {
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	job := claudetool.BackgroundJob{ID: "stale", ConversationID: id, PID: os.Getpid(), StartTime: 1}
	if err := server.recordBackgroundJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}

	kill := func() int {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/conversation/"+id+"/background-jobs/stale/kill", nil))
		return w.Code
	}
	if code := kill(); code != http.StatusNoContent {
		t.Fatalf("kill: %d, want 204", code)
	}
	if code := kill(); code != http.StatusNotFound {
		t.Fatalf("second kill: %d, want 404", code)
	}
	if n := listedRunningJobs(t, server, id); n != 0 {
		t.Fatalf("listed %d running jobs, want 0", n)
	}
	waitFor(t, 10*time.Second, func() bool { return len(backgroundJobNotices(t, database, id)) > 0 })
	waitForIdle(t, server, id)
	notices := backgroundJobNotices(t, database, id)
	if len(notices) != 1 || !strings.Contains(notices[0].Text, "lost") {
		t.Fatalf("notices = %+v, want one lost notice", notices)
	}
}
