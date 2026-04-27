package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"shelley.exe.dev/claudetool/bashkit"
	"shelley.exe.dev/llm"
)

// ShellTool is a successor to BashTool that does not unconditionally kill
// commands on timeout. Instead, if the command does not finish within
// yield_time_seconds, the tool returns to the LLM with the tail of output,
// the PID of the still-running process, and the path to a temp log file the
// agent can use (via the bash tool) to poll, wait, or kill.
//
// This lets the agent supervise long-running but bounded jobs (builds,
// tests, big rsync) without committing the whole tool call to a hard timeout.
type ShellTool struct {
	// CheckPermission is called before running any command, if set.
	CheckPermission PermissionCallback
	// EnableJITInstall enables just-in-time tool installation for missing commands.
	EnableJITInstall bool
	// WorkingDir is the shared mutable working directory.
	WorkingDir *MutableWorkingDir
	// LLMProvider provides access to LLM services for tool validation.
	LLMProvider LLMServiceProvider
	// ConversationID is exposed to invoked commands via SHELLEY_CONVERSATION_ID.
	ConversationID string
	// BackgroundCtx is the long-lived context that owns spawned processes.
	// When nil, defaults to context.Background(): yielded jobs survive the
	// per-call ctx ending. Set this to a server- or conversation-lifetime
	// context to have shelley reap background jobs on shutdown.
	BackgroundCtx context.Context
	// DefaultYield is the default yield_time_seconds (default 30s).
	DefaultYield time.Duration
	// MaxYield is the upper cap on yield_time_seconds (default 10m).
	MaxYield time.Duration
	// TempDir overrides the directory used for log files (default os.TempDir).
	TempDir string
}

const (
	shellName         = "shell"
	shellDefaultYield = 30 * time.Second
	shellMaxYield     = 10 * time.Minute
	shellMinYield     = 1 * time.Second
	shellTailBytes    = 8 * 1024
	shellWaitDelay    = 15 * time.Second
)

const shellDescription = `Executes shell commands via bash --login -c, returning combined stdout/stderr.
State (cwd, vars, aliases) does not persist between calls; use change_dir for cwd.

If the command does not finish within yield_time_seconds, this tool returns
the output so far, the PID, and the log file path; the process keeps running
in the background.

For long-lived processes (servers, watchers), prefer tmux.

Destructive commands (deleting .git, home dirs, broad wildcards) require
explicit paths and user confirmation.

Keep commands under 60k tokens; for complex scripts, write a file and run it.
`

// shellInputSchema is built dynamically so the description reflects the
// configured default and max yield times.
func (s *ShellTool) inputSchema() string {
	def := secondsCeil(s.defaultYield())
	maxs := secondsCeil(s.maxYield())
	if maxs < 1 {
		maxs = 1
	}
	if def < 1 {
		def = 1
	}
	if def > maxs {
		def = maxs
	}
	return fmt.Sprintf(`{
  "type": "object",
  "required": ["command"],
  "properties": {
    "command": {
      "type": "string",
      "description": "Shell command to execute"
    },
    "yield_time_seconds": {
      "type": "integer",
      "description": "Seconds to wait synchronously before yielding control while the command continues running in the background. Default %d. Maximum %d.",
      "minimum": 1,
      "maximum": %d
    }
  }
}`, def, maxs, maxs)
}

type shellInput struct {
	Command          string `json:"command"`
	YieldTimeSeconds int    `json:"yield_time_seconds,omitempty"`
}

// ShellDisplayData is the display data sent to the UI for shell tool results.
type ShellDisplayData struct {
	WorkingDir string `json:"workingDir"`
	PID        int    `json:"pid,omitempty"`
	LogPath    string `json:"logPath,omitempty"`
	Yielded    bool   `json:"yielded,omitempty"`
}

// secondsCeil rounds a duration up to whole seconds.
func secondsCeil(d time.Duration) int {
	return int((d + time.Second - 1) / time.Second)
}

func (s *ShellTool) defaultYield() time.Duration {
	if s.DefaultYield > 0 {
		return s.DefaultYield
	}
	return shellDefaultYield
}

func (s *ShellTool) maxYield() time.Duration {
	if s.MaxYield > 0 {
		return s.MaxYield
	}
	return shellMaxYield
}

func (s *ShellTool) tempDir() string {
	if s.TempDir != "" {
		return s.TempDir
	}
	return os.TempDir()
}

func (s *ShellTool) backgroundCtx() context.Context {
	if s.BackgroundCtx != nil {
		return s.BackgroundCtx
	}
	return context.Background()
}

func (s *ShellTool) yieldDuration(req shellInput) time.Duration {
	d := s.defaultYield()
	if req.YieldTimeSeconds > 0 {
		d = time.Duration(req.YieldTimeSeconds) * time.Second
	}
	if d < shellMinYield {
		d = shellMinYield
	}
	if d > s.maxYield() {
		d = s.maxYield()
	}
	return d
}

// Tool returns an llm.Tool based on s.
func (s *ShellTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        shellName,
		Description: strings.TrimSpace(shellDescription),
		InputSchema: llm.MustSchema(s.inputSchema()),
		Run:         s.Run,
	}
}

func (s *ShellTool) Run(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var req shellInput
	if err := json.Unmarshal(m, &req); err != nil {
		return llm.ErrorfToolOut("failed to unmarshal shell command input: %w", err)
	}

	wd := s.WorkingDir.Get()
	if _, err := os.Stat(wd); err != nil {
		if os.IsNotExist(err) {
			return llm.ErrorfToolOut("working directory does not exist: %s (use change_dir to switch to a valid directory)", wd)
		}
		return llm.ErrorfToolOut("cannot access working directory %s: %w", wd, err)
	}

	if err := bashkit.Check(req.Command); err != nil {
		return llm.ErrorToolOut(err)
	}
	if s.CheckPermission != nil {
		if err := s.CheckPermission(req.Command); err != nil {
			return llm.ErrorToolOut(err)
		}
	}

	if s.EnableJITInstall {
		// Reuse the bash JIT installer; it operates per-command and is
		// independent of the BashTool struct beyond the LLM provider.
		bt := &BashTool{LLMProvider: s.LLMProvider}
		if err := bt.checkAndInstallMissingTools(ctx, req.Command); err != nil {
			slog.DebugContext(ctx, "failed to auto-install missing tools", "error", err)
		}
	}

	if !isNoTrailerSet() {
		req.Command = bashkit.AddCoauthorTrailer(req.Command, "Co-authored-by: Shelley <shelley@exe.dev>")
	}

	yield := s.yieldDuration(req)

	// Open a temp log file. We want a deterministic, pid-based name so the
	// agent has a stable path to reference; create with CreateTemp first so
	// we don't race for a name, then rename to a pid-based path after Start.
	tmpFile, err := os.CreateTemp(s.tempDir(), "shelley-shell-*.log")
	if err != nil {
		return llm.ErrorfToolOut("failed to create log file: %w", err)
	}
	logPath := tmpFile.Name()

	cmd := exec.CommandContext(s.backgroundCtx(), "bash", "--login", "-c", req.Command)
	cmd.Dir = wd
	cmd.Stdin = nil
	cmd.Stdout = tmpFile
	cmd.Stderr = tmpFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = shellWaitDelay

	env := slices.DeleteFunc(os.Environ(), func(s string) bool {
		return strings.HasPrefix(s, "SHELLEY_CONVERSATION_ID=")
	})
	env = append(env,
		"SKETCH=1",
		"EDITOR=/bin/false",
		`GIT_SEQUENCE_EDITOR=echo "To do an interactive rebase, run it in a tmux session." && exit 1`,
	)
	if s.ConversationID != "" {
		env = append(env, "SHELLEY_CONVERSATION_ID="+s.ConversationID)
	}
	cmd.Env = env

	if err := cmd.Start(); err != nil {
		tmpFile.Close()
		os.Remove(logPath)
		return llm.ErrorfToolOut("command failed to start: %w", err)
	}
	pid := cmd.Process.Pid
	pgid := pid // Setpgid: true => pgid == pid

	// Rename to a pid-based path. The fd inside the child is unaffected.
	pidPath := filepath.Join(filepath.Dir(logPath), fmt.Sprintf("shelley-shell-%d.log", pid))
	if err := os.Rename(logPath, pidPath); err == nil {
		logPath = pidPath
	}

	// The child has its own dup of the fd; we no longer need ours.
	tmpFile.Close()

	// Live tail to UI while we wait.
	progressFn := GetToolProgress(ctx)
	toolID, _ := ctx.Value(toolUseIDCtxKey).(string)
	progressDone := make(chan struct{})
	progressStop := make(chan struct{})
	if progressFn != nil && toolID != "" {
		go shellProgressLoop(progressFn, toolID, logPath, progressStop, progressDone)
	} else {
		close(progressDone)
	}
	stopProgress := func() {
		select {
		case <-progressStop:
		default:
			close(progressStop)
		}
		<-progressDone
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	timer := time.NewTimer(yield)
	defer timer.Stop()

	display := ShellDisplayData{WorkingDir: wd, PID: pid, LogPath: logPath}

	select {
	case waitErr := <-done:
		stopProgress()
		out, ferr := readAndFormatShellOutput(logPath)
		if ferr != nil {
			return llm.ErrorfToolOut("failed to read shell output: %w", ferr)
		}
		if waitErr != nil {
			return llm.ErrorToolOut(fmt.Errorf("[command failed: %w]\n%s", waitErr, out))
		}
		return llm.ToolOut{LLMContent: llm.TextContent(out), Display: display}

	case <-ctx.Done():
		// Per-call cancel: kill the pgroup and wait briefly for cleanup.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		stopProgress()
		out, _ := readAndFormatShellOutput(logPath)
		return llm.ErrorToolOut(fmt.Errorf("[command cancelled: %w]\n%s", ctx.Err(), out))

	case <-timer.C:
		stopProgress()
		display.Yielded = true
		tail := readTailString(logPath, shellTailBytes)
		payload := buildYieldPayload(req.Command, pid, pgid, logPath, tail, yield)
		// After the yielded process eventually exits, schedule the log file
		// for deletion. The grace period is generous so agents that come
		// back later (after polling, waiting, or working on something else)
		// can still read it.
		go func() {
			<-done
			select {
			case <-time.After(15 * time.Minute):
			case <-s.backgroundCtx().Done():
			}
			_ = os.Remove(logPath)
		}()
		return llm.ToolOut{LLMContent: llm.TextContent(payload), Display: display}
	}
}

// readTailString returns up to maxBytes from the end of the file (best-effort).
func readTailString(path string, maxBytes int64) string {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Sprintf("(could not open log: %v)", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return fmt.Sprintf("(could not stat log: %v)", err)
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
		return fmt.Sprintf("(could not seek log: %v)", err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return fmt.Sprintf("(could not read log: %v)", err)
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

// readAndFormatShellOutput reads the full log and runs it through the
// large-output formatter shared with the bash tool.
func readAndFormatShellOutput(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return formatForegroundBashOutput(string(b))
}

func buildYieldPayload(command string, pid, pgid int, logPath, tail string, yield time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[shell yielded after %s; command still running in the background]\n", yield)
	fmt.Fprintf(&b, "PID: %d\n", pid)
	fmt.Fprintf(&b, "Process group: %d\n", pgid)
	fmt.Fprintf(&b, "Output log:    %s\n", logPath)
	b.WriteString("\n--- last output ---\n")
	if tail == "" {
		b.WriteString("(no output yet)\n")
	} else {
		b.WriteString(tail)
		if !strings.HasSuffix(tail, "\n") {
			b.WriteByte('\n')
		}
	}
	b.WriteString("--- end output ---\n\n")
	b.WriteString("To wait for completion and read latest output, use bash (adjust seconds=… as needed):\n")
	fmt.Fprintf(&b, "  seconds=60; for i in $(seq 1 $seconds); do kill -0 %d 2>/dev/null || break; sleep 1; done; "+
		"kill -0 %d 2>/dev/null && echo STILL_RUNNING || echo EXITED; tail -c 16384 %s\n\n",
		pid, pid, logPath)
	b.WriteString("To check status without waiting:\n")
	fmt.Fprintf(&b, "  kill -0 %d 2>/dev/null && echo running || echo exited; tail -c 8192 %s\n\n", pid, logPath)
	b.WriteString("To kill the process and its children:\n")
	fmt.Fprintf(&b, "  kill -TERM -- -%d; sleep 1; kill -KILL -- -%d 2>/dev/null; true\n\n", pgid, pgid)
	b.WriteString("For long-lived processes (servers, watchers), prefer tmux over yielding shell calls.\n")
	return b.String()
}

const shellProgressInterval = 500 * time.Millisecond

func shellProgressLoop(progress llm.ToolProgressFunc, toolID, path string, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(shellProgressInterval)
	defer ticker.Stop()
	last := ""
	emit := func() {
		t := readTailString(path, progressMaxBytes)
		if t != last {
			last = t
			progress(llm.ToolProgress{
				ToolUseID: toolID,
				ToolName:  shellName,
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
