package claudetool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"shelley.exe.dev/claudetool/bashkit"
	"shelley.exe.dev/gitstate"
	"shelley.exe.dev/llm"

	"mvdan.cc/sh/v3/syntax"
)

// PermissionCallback is a function type for checking if a command is allowed to run
type PermissionCallback func(command string) error

// BashTool specifies an llm.Tool for executing shell commands.
type BashTool struct {
	// CheckPermission is called before running any command, if set
	CheckPermission PermissionCallback
	// EnableJITInstall enables just-in-time tool installation for missing commands
	EnableJITInstall bool
	// Jobs is told about commands that outlive BackgroundAfter.
	Jobs BackgroundJobs
	// BackgroundAfter is how long a command runs before it is moved to the
	// background; zero means DefaultBashBackgroundAfter.
	BackgroundAfter time.Duration
	// WorkingDir is the shared mutable working directory.
	WorkingDir *MutableWorkingDir
	// GitTracker, if set, is told about the directories commands run git in.
	GitTracker *gitstate.Tracker
	// LLMProvider provides access to LLM services for tool validation
	LLMProvider LLMServiceProvider
	// ModelID is the conversation's model, used to pick a workhorse model
	// for tool validation.
	ModelID string
	// Env holds the conversation context exposed to invoked commands as
	// SHELLEY_* environment variables.
	Env ShelleyEnv

	// cdHinted records that the chained-cd hint has been shown. The hint
	// is shown at most once per BashTool, that is, once per conversation:
	// repeating it does not change model behavior, it only adds noise.
	cdHinted atomic.Bool
}

const (
	EnableBashToolJITInstall = true
	NoBashToolJITInstall     = false
)

// DefaultBashBackgroundAfter is how long a bash command runs in the
// foreground before it is moved to the background.
const DefaultBashBackgroundAfter = 60 * time.Second

func (b *BashTool) backgroundAfter() time.Duration {
	if b.BackgroundAfter == 0 {
		return DefaultBashBackgroundAfter
	}
	return b.BackgroundAfter
}

// Tool returns an llm.Tool based on b.
func (b *BashTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        bashName,
		Description: strings.TrimSpace(bashDescription),
		InputSchema: llm.MustSchema(bashInputSchema),
		Run:         llm.RunJSON(b.run),
	}
}

// getWorkingDir returns the current working directory.
func (b *BashTool) getWorkingDir() string {
	return b.WorkingDir.Get()
}

// isNoTrailerSet checks if user has disabled co-author trailer via git config.
func isNoTrailerSet() bool {
	out, err := exec.Command("git", "config", "--get", "shelley.no-trailer").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

const (
	bashName        = "bash"
	bashDescription = "Executes shell commands via bash --login -c, returning combined stdout/stderr.\n" +
		"For commands like `cd app && make`, call change_dir with path `app`, then bash `make` without cd. " +
		"These calls can share a response: change_dir completes before the following parallel group starts. " +
		"Apply this before initial inspections and tests too, including README recipes. " +
		"Skip change_dir if already there.\n\n" +
		`Shell state (cwd, variables, aliases) does not persist.
Commands already start in the working directory; omit redundant cd.
For 'cd dir && ...' or 'cd dir; ...', call change_dir first,
then run the remaining command directly.

After 60s, commands move to the background. A background command's output
arrives as a new message when it exits; never poll or sleep for it. While it
runs, only do work that doesn't need its output; otherwise end your turn.
Do not use &, nohup, or disown.

For servers and watch modes, which never exit, use tmux.

For delayed wakeups or scheduled tasks, use the schedule skill.

Destructive commands (deleting .git, home directories, broad wildcards, etc) require
explicit paths and user confirmation.

Keep commands under a dozen lines, excluding file contents. For complex scripts,
write a file and run it; both can share one call.
`
	// If you modify this, update the termui template for prettier rendering.
	bashInputSchema = `
{
  "type": "object",
  "required": ["command"],
  "properties": {
    "command": {
      "type": "string",
      "description": "Shell to execute"
    },
    "background": {
      "type": "boolean",
      "description": "Start in the background immediately instead of after 60s"
    }
  }
}
`
)

type bashInput struct {
	Command    string `json:"command"`
	Background bool   `json:"background,omitempty"`
}

// BashDisplayData is the display data sent to the UI for bash tool results.
type BashDisplayData struct {
	WorkingDir string `json:"workingDir"`
	ExitCode   *int   `json:"exitCode,omitempty"`
	// Background is set when the command was moved to the background.
	Background *BashBackgroundDisplay `json:"background,omitempty"`
}

// BashBackgroundDisplay identifies a backgrounded command in the UI.
type BashBackgroundDisplay struct {
	JobID   string `json:"jobId"`
	LogPath string `json:"logPath"`
	PGID    int    `json:"pgid"`
}

func (b *BashTool) run(ctx context.Context, req bashInput) llm.ToolOut {
	// Check that the working directory exists
	wd := b.getWorkingDir()
	if _, err := os.Stat(wd); err != nil {
		if os.IsNotExist(err) {
			return llm.ErrorfToolOut("working directory does not exist: %s (use change_dir to switch to a valid directory)", wd)
		}
		return llm.ErrorfToolOut("cannot access working directory %s: %w", wd, err)
	}

	// do a quick permissions check (NOT a security barrier)
	err := bashkit.Check(req.Command)
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Custom permission callback if set
	if b.CheckPermission != nil {
		if err := b.CheckPermission(req.Command); err != nil {
			return llm.ErrorToolOut(err)
		}
	}

	// Check for missing tools and try to install them if needed, best effort only
	if b.EnableJITInstall {
		err := b.checkAndInstallMissingTools(ctx, req.Command)
		if err != nil {
			slog.DebugContext(ctx, "failed to auto-install missing tools", "error", err)
		}
	}

	// Add co-author trailer to git commits unless user has disabled it
	if !isNoTrailerSet() {
		req.Command = bashkit.AddCoauthorTrailer(req.Command, "Co-authored-by: Shelley <shelley@exe.dev>")
	}

	display := BashDisplayData{WorkingDir: wd}

	res, execErr := b.executeBashInDir(ctx, req, wd)
	if execErr != nil {
		var exitErr *exec.ExitError
		if errors.As(execErr, &exitErr) && exitErr.ProcessState.Exited() {
			exitCode := exitErr.ExitCode()
			display.ExitCode = &exitCode
		}
		toolOut := llm.ErrorToolOut(execErr)
		toolOut.Display = display
		return toolOut
	}
	out := res.out
	if res.job != nil {
		display.Background = &BashBackgroundDisplay{JobID: res.job.ID, LogPath: res.job.LogPath, PGID: res.job.PID}
		return llm.ToolOut{LLMContent: llm.TextContent(out), Display: display}
	}
	exitCode := 0
	display.ExitCode = &exitCode
	if chainedCdLeavesDir(bashkit.ChainedCdPaths(req.Command), wd) && b.cdHinted.CompareAndSwap(false, true) {
		out = chainedCdHint(wd) + "\n\n" + out
	}
	return llm.ToolOut{LLMContent: llm.TextContent(out), Display: display}
}

// touchGitDirs tells the git tracker about the directories command runs git
// in, starting from workingDir. Call release once the command exits.
func (b *BashTool) touchGitDirs(command, workingDir string) (release func()) {
	if b.GitTracker == nil {
		return func() {}
	}
	home, _ := os.UserHomeDir() // without one, ~ paths are unknown
	var releases []func()
	for _, dir := range bashkit.GitDirs(command, workingDir, home) {
		releases = append(releases, b.GitTracker.Touch(dir))
	}
	return func() {
		for _, release := range releases {
			release()
		}
	}
}

// chainedCdLeavesDir reports whether any chained `cd <path>` in a command
// (see bashkit.ChainedCdPaths) targets a directory other than workingDir.
// A `cd` to the current directory is a harmless no-op and gets no hint.
// A non-literal path (reported as "") is assumed to leave the directory.
func chainedCdLeavesDir(paths []string, workingDir string) bool {
	for _, p := range paths {
		if p == "" || !cdPathIsCurrentDir(p, workingDir) {
			return true
		}
	}
	return false
}

// chainedCdHint tells the model the one fact it can act on: the cwd it
// will see on the next call, and how to change it.
func chainedCdHint(workingDir string) string {
	return "[shelley: `cd` inside a bash call does not persist; the working directory is still " + workingDir + ". Use change_dir to move.]"
}

func cdPathIsCurrentDir(path, workingDir string) bool {
	if !filepath.IsAbs(path) {
		path = filepath.Join(workingDir, path)
	}
	return filepath.Clean(path) == filepath.Clean(workingDir)
}

const (
	largeOutputThreshold = 50 * 1024 // 50KB - threshold for saving to file
	firstLinesCount      = 2
	lastLinesCount       = 5
	maxLineLength        = 200 // truncate displayed lines to this length
)

// makeBashCommand returns a job wrapper (see bashJobWrapper) that runs
// command in its own process group, recording its exit status in exitPath
// and its combined output in log. It is deliberately not tied to a
// context: a backgrounded job must outlive the tool call and Shelley.
func (b *BashTool) makeBashCommand(command, wd string, log *os.File, exitPath string) *exec.Cmd {
	cmd := exec.Command("bash", "-c", bashJobWrapper, "shelley-job", command, exitPath)
	cmd.Dir = wd
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Strip any inherited SHELLEY_* vars so we control them explicitly below.
	env := stripShelleyEnv(os.Environ())
	env = append(env, "SKETCH=1")          // signal that this has been run by Sketch, sometimes useful for scripts
	env = append(env, "EDITOR=/bin/false") // interactive editors won't work
	env = append(env, `GIT_SEQUENCE_EDITOR=echo "To do an interactive rebase, run it in a tmux session." && exit 1`)
	env = append(env, b.Env.Environ(cmd.Dir)...)
	cmd.Env = env
	return cmd
}

const (
	// progressMaxBytes is the maximum bytes of output kept in the progress tail buffer.
	progressMaxBytes = 10 * 1024
)

// bashResult is the outcome of a command that finished in the foreground
// (job == nil) or was moved to the background (job != nil).
type bashResult struct {
	out string
	job *BackgroundJob
}

func (b *BashTool) executeBash(ctx context.Context, req bashInput) (bashResult, error) {
	return b.executeBashInDir(ctx, req, b.getWorkingDir())
}

func (b *BashTool) executeBashInDir(ctx context.Context, req bashInput, wd string) (bashResult, error) {
	dir := bashJobDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return bashResult{}, fmt.Errorf("create job dir: %w", err)
	}
	job := BackgroundJob{
		ID:             newBashJobID(),
		ConversationID: b.Env.ConversationID,
		ToolUseID:      ToolUseID(ctx),
		Command:        req.Command,
	}
	job.LogPath = filepath.Join(dir, job.ID+".log")
	job.ExitPath = filepath.Join(dir, job.ID+".exit")
	log, err := os.OpenFile(job.LogPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return bashResult{}, fmt.Errorf("create job log: %w", err)
	}
	cmd := b.makeBashCommand(req.Command, wd, log, job.ExitPath)
	// The git tracker watches where the command runs git until it exits,
	// even if that is after it moves to the background.
	releaseGitDirs := b.touchGitDirs(req.Command, wd)
	job.StartedAt = time.Now()
	err = cmd.Start()
	log.Close() // the job holds its own descriptor
	if err != nil {
		releaseGitDirs()
		os.Remove(job.LogPath)
		return bashResult{}, fmt.Errorf("command failed: %w", err)
	}
	job.PID = cmd.Process.Pid
	// Read the start time before anything reaps the wrapper, so even a
	// command that exits immediately can still be recorded as a job.
	job.StartTime, err = processStartTime(job.PID)
	if err != nil {
		syscall.Kill(-job.PID, syscall.SIGKILL)
		cmd.Wait()
		releaseGitDirs()
		return bashResult{}, fmt.Errorf("job %s (PGID %d): %w", job.ID, job.PID, err)
	}
	exited := make(chan struct{})
	go waitBashJob(cmd, job.ExitPath, releaseGitDirs, exited)
	if req.Background {
		return b.background(ctx, job, exited, "Started as")
	}

	stopProgress := func() {}
	if progressFn := GetToolProgress(ctx); progressFn != nil && job.ToolUseID != "" {
		stop, done := make(chan struct{}), make(chan struct{})
		go logProgressLoop(progressFn, job.ToolUseID, bashName, job.LogPath, stop, done)
		stopProgress = func() {
			close(stop)
			<-done
		}
	}

	timer := time.NewTimer(b.backgroundAfter())
	defer timer.Stop()
	select {
	case <-exited:
	case <-ctx.Done():
		syscall.Kill(-job.PID, syscall.SIGKILL)
		<-exited
	case <-timer.C:
		stopProgress()
		return b.background(ctx, job, exited, fmt.Sprintf("Still running after %s; moved to", b.backgroundAfter()))
	}
	stopProgress()

	content, readErr := os.ReadFile(job.LogPath)
	os.Remove(job.LogPath)
	os.Remove(job.ExitPath)
	if readErr != nil {
		return bashResult{}, fmt.Errorf("read command output: %w", readErr)
	}
	out, err := formatForegroundBashOutput(string(content))
	if err != nil {
		return bashResult{}, err
	}
	if ctx.Err() != nil {
		// Preserve the cancellation cause so the loop can classify this as
		// an interrupted tool rather than an ordinary failure.
		return bashResult{}, fmt.Errorf("[command cancelled: %w]\n%s", ctx.Err(), out)
	}
	if !cmd.ProcessState.Success() {
		return bashResult{}, fmt.Errorf("[command failed: %w]\n%s", &exec.ExitError{ProcessState: cmd.ProcessState}, out)
	}
	return bashResult{out: out}, nil
}

// jobWaitGuidance is blunt because models tend to `sleep N; tail log`
// instead of ending their turn, which delays the completion notice.
const jobWaitGuidance = `Its output will arrive as a new message that wakes you when the job exits.
What to do now:
- Need its output to continue? END YOUR TURN NOW. Write one line like "Waiting for the job." and stop.
- Have work that doesn't need its output? Do that, then end your turn.
NEVER wait on it. Until it finishes:
- NO sleep, for any duration (` + "`sleep 45; tail <log>`" + ` is exactly the wrong move)
- NO tail, cat, or grep of its log to see if it's done
- NO checking on it with ps, ls, or a loop
After it finishes, read the log freely. Any command you run keeps you busy, so the job's completion cannot wake you until it returns.`

// background hands the still-running job to b.Jobs, which reports its
// completion to the conversation, and describes it for the model.
// how leads the description, e.g. "Started as" or "Still running after 1m0s; moved to".
func (b *BashTool) background(ctx context.Context, job BackgroundJob, exited <-chan struct{}, how string) (bashResult, error) {
	if err := b.Jobs.Background(context.WithoutCancel(ctx), job, exited); err != nil {
		return bashResult{}, fmt.Errorf("background job %s (PGID %d, log %s): %w", job.ID, job.PID, job.LogPath, err)
	}
	var out strings.Builder
	if tail := logTail(job.LogPath, jobNoticeTailLines); tail != "" {
		out.WriteString(tail + "\n")
	}
	fmt.Fprintf(&out, "[%s background job %s (PGID %d). Log: %s\n", how, job.ID, job.PID, job.LogPath)
	out.WriteString(jobWaitGuidance)
	fmt.Fprintf(&out, " Cancel with `kill -- -%d`.]", job.PID)
	return bashResult{out: out.String(), job: &job}, nil
}

// formatForegroundBashOutput formats the output of a foreground bash command for display to the agent.
// If output exceeds largeOutputThreshold, it saves to a file and returns a summary.
func formatForegroundBashOutput(out string) (string, error) {
	if len(out) <= largeOutputThreshold {
		return out, nil
	}

	// Save full output to a temp file
	tmpDir, err := os.MkdirTemp("", "shelley-output-")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir for large output: %w", err)
	}

	outFile := filepath.Join(tmpDir, "output")
	if err := os.WriteFile(outFile, []byte(out), 0o644); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("failed to write large output to file: %w", err)
	}

	// Split into lines
	lines := strings.Split(out, "\n")

	// If fewer than 3 lines total, likely binary or single-line output
	if len(lines) < 3 {
		return fmt.Sprintf("[output too large (%s, %d lines), saved to: %s]",
			humanizeBytes(len(out)), len(lines), outFile), nil
	}

	var result strings.Builder
	fmt.Fprintf(&result, "[output too large (%s, %d lines), saved to: %s]\n\n",
		humanizeBytes(len(out)), len(lines), outFile)

	// First N lines
	result.WriteString("First lines:\n")
	firstN := min(firstLinesCount, len(lines))
	for i := range firstN {
		fmt.Fprintf(&result, "%5d: %s\n", i+1, truncateLine(lines[i]))
	}

	// Last N lines
	result.WriteString("\n...\n\nLast lines:\n")
	startIdx := max(0, len(lines)-lastLinesCount)
	for i := startIdx; i < len(lines); i++ {
		fmt.Fprintf(&result, "%5d: %s\n", i+1, truncateLine(lines[i]))
	}

	return result.String(), nil
}

// truncateLine truncates a line to maxLineLength characters, appending "..." if truncated.
func truncateLine(line string) string {
	if len(line) <= maxLineLength {
		return line
	}
	return line[:maxLineLength] + "..."
}

func humanizeBytes(bytes int) string {
	switch {
	case bytes < 4*1024:
		return fmt.Sprintf("%dB", bytes)
	case bytes < 1024*1024:
		kb := int(math.Round(float64(bytes) / 1024.0))
		return fmt.Sprintf("%dkB", kb)
	case bytes < 1024*1024*1024:
		mb := int(math.Round(float64(bytes) / (1024.0 * 1024.0)))
		return fmt.Sprintf("%dMB", mb)
	}
	return "more than 1GB"
}

// shellHasCommand checks whether a command is available inside the
// same bash --login environment that the bash tool uses to run commands.
// This accounts for version managers (uv, mise, direnv, …) that only
// add entries to PATH after shell startup scripts are sourced.
func shellHasCommand(ctx context.Context, name string) bool {
	quoted, err := syntax.Quote(name, syntax.LangBash)
	if err != nil {
		return false
	}
	cmd := exec.CommandContext(ctx, "bash", "--login", "-c", "command -v "+quoted)
	cmd.Stdin = nil
	return cmd.Run() == nil
}

// checkAndInstallMissingTools analyzes a bash command and attempts to automatically install any missing tools.
func (b *BashTool) checkAndInstallMissingTools(ctx context.Context, command string) error {
	commands, err := bashkit.ExtractCommands(command)
	if err != nil {
		return err
	}

	autoInstallMu.Lock()
	defer autoInstallMu.Unlock()

	var missing []string
	for _, cmd := range commands {
		if doNotAttemptToolInstall[cmd] {
			continue
		}
		if shellHasCommand(ctx, cmd) {
			doNotAttemptToolInstall[cmd] = true // spare future checks
			continue
		}
		missing = append(missing, cmd)
	}

	if len(missing) == 0 {
		return nil
	}

	for _, cmd := range missing {
		err := b.installTool(ctx, cmd)
		if err != nil {
			slog.WarnContext(ctx, "failed to install tool", "tool", cmd, "error", err)
		}
		doNotAttemptToolInstall[cmd] = true // either it's installed or it's not--either way, we're done with it
	}
	return nil
}

// Command safety check cache to avoid repeated LLM calls
var (
	autoInstallMu           sync.Mutex
	doNotAttemptToolInstall = make(map[string]bool) // set to true if the tool should not be auto-installed
)

// autodetectPackageManager returns the first package‑manager binary
// found in the login shell environment, or an empty string if none are present.
func autodetectPackageManager() string {
	// TODO: cache this result with a sync.OnceValue

	managers := []string{
		"apt", "apt-get", // Debian/Ubuntu
		"brew", "port", // macOS (Homebrew / MacPorts)
		"apk",        // Alpine
		"yum", "dnf", // RHEL/Fedora
		"pacman",          // Arch
		"zypper",          // openSUSE
		"xbps-install",    // Void
		"emerge",          // Gentoo
		"nix-env", "guix", // NixOS / Guix
		"pkg",      // FreeBSD
		"slackpkg", // Slackware
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, m := range managers {
		if shellHasCommand(ctx, m) {
			return m
		}
	}
	return ""
}

// installTool attempts to install a single missing tool using LLM validation and system package manager.
func (b *BashTool) installTool(ctx context.Context, cmd string) error {
	slog.InfoContext(ctx, "attempting to install tool", "tool", cmd)

	packageManager := autodetectPackageManager()
	if packageManager == "" {
		return fmt.Errorf("no known package manager found in PATH")
	}
	// Use LLM to validate and get package name
	if b.LLMProvider == nil {
		return fmt.Errorf("no LLM provider available for tool validation")
	}

	query := fmt.Sprintf(`Do you know this command/package/tool? Is it legitimate, clearly non-harmful, and commonly used? Can it be installed with package manager %s?

Command: %s

- YES: Respond ONLY with the package name used to install it
- NO or UNSURE: Respond ONLY with the word NO`, packageManager, cmd)

	req := &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{llm.StringContent(query)},
		}},
		System: []llm.SystemContent{{
			Type: "text",
			Text: "You are an expert in software developer tools.",
		}},
	}

	svc, err := b.LLMProvider.GetWorkhorseService(b.ModelID)
	if err != nil {
		return fmt.Errorf("failed to validate tool with LLM: %w", err)
	}
	resp, err := svc.Do(llm.WithPurpose(ctx, "tool_install"), req)
	if err != nil {
		return fmt.Errorf("failed to validate tool with LLM: %w", err)
	}

	response := strings.TrimSpace(llm.FirstText(resp))
	if response == "" {
		return fmt.Errorf("empty response from LLM for tool validation")
	}
	if response == "NO" || response == "UNSURE" {
		slog.InfoContext(ctx, "tool installation declined by LLM", "tool", cmd, "response", response)
		return fmt.Errorf("tool %s not approved for installation", cmd)
	}

	packageName := strings.TrimSpace(response)
	if packageName == "" {
		return fmt.Errorf("no package name provided for tool %s", cmd)
	}

	return b.installPackage(ctx, cmd, packageName, packageManager)
}

// installPackage handles the actual package installation
func (b *BashTool) installPackage(ctx context.Context, cmd, packageName, packageManager string) error {
	// Install the package (with update command first if needed)
	// TODO: these invocations create zombies when we are PID 1.
	// We should give them the same zombie-reaping treatment as above,
	// if/when we care enough to put in the effort. Not today.
	var updateCmd, installCmd string
	switch packageManager {
	case "apt", "apt-get":
		updateCmd = fmt.Sprintf("sudo %s update", packageManager)
		installCmd = fmt.Sprintf("sudo %s install -y %s", packageManager, packageName)
	case "brew":
		// brew handles updates automatically, no explicit update needed
		installCmd = fmt.Sprintf("brew install %s", packageName)
	case "apk":
		updateCmd = "sudo apk update"
		installCmd = fmt.Sprintf("sudo apk add %s", packageName)
	case "yum", "dnf":
		// For yum/dnf, we don't need a separate update command as the package cache is usually fresh enough
		// and install will fetch the latest available packages
		installCmd = fmt.Sprintf("sudo %s install -y %s", packageManager, packageName)
	case "pacman":
		updateCmd = "sudo pacman -Sy"
		installCmd = fmt.Sprintf("sudo pacman -S --noconfirm %s", packageName)
	case "zypper":
		updateCmd = "sudo zypper refresh"
		installCmd = fmt.Sprintf("sudo zypper install -y %s", packageName)
	case "xbps-install":
		updateCmd = "sudo xbps-install -S"
		installCmd = fmt.Sprintf("sudo xbps-install -y %s", packageName)
	case "emerge":
		// Note: emerge --sync is expensive, so we skip it for JIT installs
		// Users should manually sync if needed
		installCmd = fmt.Sprintf("sudo emerge %s", packageName)
	case "nix-env":
		// nix-env doesn't require explicit updates for JIT installs
		installCmd = fmt.Sprintf("nix-env -i %s", packageName)
	case "guix":
		// guix doesn't require explicit updates for JIT installs
		installCmd = fmt.Sprintf("guix install %s", packageName)
	case "pkg":
		updateCmd = "sudo pkg update"
		installCmd = fmt.Sprintf("sudo pkg install -y %s", packageName)
	case "slackpkg":
		updateCmd = "sudo slackpkg update"
		installCmd = fmt.Sprintf("sudo slackpkg install %s", packageName)
	default:
		return fmt.Errorf("unsupported package manager: %s", packageManager)
	}

	slog.InfoContext(ctx, "installing tool", "tool", cmd, "package", packageName, "update_command", updateCmd, "install_command", installCmd)

	// Execute the update command first if needed
	if updateCmd != "" {
		slog.InfoContext(ctx, "updating package cache", "command", updateCmd)
		updateCmdExec := exec.CommandContext(ctx, "sh", "-c", updateCmd)
		updateOutput, err := updateCmdExec.CombinedOutput()
		if err != nil {
			slog.WarnContext(ctx, "package cache update failed, proceeding with install anyway", "error", err, "output", string(updateOutput))
		}
	}

	// Execute the install command
	cmdExec := exec.CommandContext(ctx, "sh", "-c", installCmd)
	output, err := cmdExec.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install %s: %w\nOutput: %s", packageName, err, string(output))
	}

	slog.InfoContext(ctx, "tool installation successful", "tool", cmd, "package", packageName)
	return nil
}
