package claudetool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"shelley.exe.dev/llm"
)

// BackgroundJob is a bash command that outlived its tool call. Its process
// is a small wrapper (see bashJobWrapper) that writes the command's exit
// status to ExitPath, so the status is recoverable even when Shelley is not
// the wrapper's parent.
type BackgroundJob struct {
	ID             string
	ConversationID string
	ToolUseID      string
	Command        string
	// PID is the wrapper's PID, which is also the job's process group ID.
	PID int
	// StartTime is an OS-specific process start token. It distinguishes
	// the wrapper from a later process that reuses its PID.
	StartTime uint64
	LogPath   string
	ExitPath  string
	StartedAt time.Time
}

// BackgroundJobs is told about every command bash moves to the background.
// exited is closed once the job has exited and its exit file is written.
// Implementations report the job's completion to its conversation.
type BackgroundJobs interface {
	Background(ctx context.Context, job BackgroundJob, exited <-chan struct{}) error
}

// bashJobWrapper runs $1 and atomically records its exit status in $2. It
// traps (rather than ignores) the polite termination signals so that
// `kill -- -PGID` stops the command but not the wrapper, which then records
// the status; children still get default signal dispositions.
const bashJobWrapper = `trap : HUP INT TERM
bash --login -c "$1"
s=$?
printf '%d\n' "$s" >"$2.tmp" && mv -f -- "$2.tmp" "$2"
exit "$s"`

// bashJobDir returns the directory holding job logs and exit files.
func bashJobDir() string {
	return filepath.Join(os.TempDir(), "shelley-jobs")
}

// newBashJobID returns a short random job ID.
func newBashJobID() string {
	var b [4]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// writeJobExitFile records status in path atomically, for wrappers that
// died before recording it themselves (SIGKILL).
func writeJobExitFile(path string, state *os.ProcessState) error {
	status := state.ExitCode()
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		status = 128 + int(ws.Signal())
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(status)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// waitBashJob waits for cmd, job's started wrapper, ensures its exit file
// exists, appends watch's note, if any, to its log, calls release, and
// closes exited. watch may be nil.
func waitBashJob(cmd *exec.Cmd, job BackgroundJob, watch *resourceWatch, release func(), exited chan<- struct{}) {
	defer close(exited)
	defer release()
	cmd.Wait()
	if _, err := os.Stat(job.ExitPath); errors.Is(err, os.ErrNotExist) {
		writeJobExitFile(job.ExitPath, cmd.ProcessState)
	}
	if note := watch.finish(); note != "" {
		if log, err := os.OpenFile(job.LogPath, os.O_WRONLY|os.O_APPEND, 0); err == nil {
			fmt.Fprintf(log, "\n%s\n", note)
			log.Close()
		}
	}
}

// ErrBackgroundJobGone reports that a job's wrapper has exited, or that
// its PID now names another process.
var ErrBackgroundJobGone = errors.New("background job is no longer running")

const jobNoticeTailLines = 20

// BackgroundJobOutcome is how a BackgroundJob ended, as reported to its
// conversation once it has exited.
type BackgroundJobOutcome struct {
	Job BackgroundJob
	// ExitCode is the command's exit status, or nil if the job was lost:
	// it left no exit status because the host rebooted or it was killed.
	ExitCode *int
	// Elapsed is the job's run time; it is zero for a lost job.
	Elapsed time.Duration
	// Tail is the last lines of the job's log.
	Tail string
}

// Outcome reads j's exit status, run time, and log tail.
func (j BackgroundJob) Outcome() BackgroundJobOutcome {
	o := BackgroundJobOutcome{Job: j, Tail: j.Tail()}
	status, err := os.ReadFile(j.ExitPath)
	if err != nil {
		return o
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(status)))
	if err != nil {
		return o
	}
	o.ExitCode = &code
	if fi, err := os.Stat(j.ExitPath); err == nil {
		o.Elapsed = fi.ModTime().Sub(j.StartedAt).Round(time.Second)
	}
	return o
}

// Tail returns the same bounded log excerpt shown when this job finishes.
func (j BackgroundJob) Tail() string {
	return strings.TrimRight(logTail(j.LogPath, jobNoticeTailLines), "\n")
}

// Notice describes o for the job's conversation: its exit status, run
// time, log path, and the tail of its output.
func (o BackgroundJobOutcome) Notice() string {
	var b strings.Builder
	j := o.Job
	if o.ExitCode != nil {
		fmt.Fprintf(&b, "Background job %s finished: exit %d, %s. Log: %s\n", j.ID, *o.ExitCode, o.Elapsed, j.LogPath)
	} else {
		fmt.Fprintf(&b, "Background job %s lost (host rebooted or killed). Log: %s\n", j.ID, j.LogPath)
	}
	fmt.Fprintf(&b, "Command: %s\n", truncateLine(firstLine(j.Command)))
	if o.Tail != "" {
		b.WriteString("\n" + o.Tail)
	}
	return strings.TrimRight(b.String(), "\n")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}

// logTail returns at most the last n lines of the log at path, each
// truncated to maxLineLength.
func logTail(path string, n int) string {
	tail := strings.TrimRight(readTailString(path, int64(n*(maxLineLength+1))), "\n")
	if tail == "" {
		return ""
	}
	lines := strings.Split(tail, "\n")
	lines = lines[max(0, len(lines)-n):]
	for i, l := range lines {
		lines[i] = truncateLine(l)
	}
	return strings.Join(lines, "\n") + "\n"
}

// readTailString returns up to maxBytes from the end of the file (best-effort).
// Errors may be displayed in the drawer; never include the log path in them.
func readTailString(path string, maxBytes int64) string {
	f, err := os.Open(path)
	if err != nil {
		return "(could not open job output)"
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "(could not stat job output)"
	}
	size := st.Size()
	if size == 0 {
		return ""
	}
	start := int64(0)
	truncated := false
	if size > maxBytes {
		start = size - maxBytes
		truncated = true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "(could not seek job output)"
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "(could not read job output)"
	}
	out := string(b)
	if truncated {
		// Drop a possibly-partial first line.
		if i := strings.IndexByte(out, '\n'); i >= 0 && i < len(out)-1 {
			out = out[i+1:]
		}
	}
	return out
}

const logProgressInterval = 500 * time.Millisecond

// logProgressLoop reports the tail of the log at path to progress every
// logProgressInterval while it changes, and once more when stop closes.
func logProgressLoop(progress llm.ToolProgressFunc, toolID, toolName, path string, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(logProgressInterval)
	defer ticker.Stop()
	last := ""
	emit := func() {
		t := readTailString(path, progressMaxBytes)
		if t != last {
			last = t
			progress(llm.ToolProgress{
				ToolUseID: toolID,
				ToolName:  toolName,
				Output:    t,
			})
		}
	}
	for {
		select {
		case <-stop:
			emit()
			return
		case <-ticker.C:
			emit()
		}
	}
}
