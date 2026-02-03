package claudetool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"shelley.exe.dev/claudetool/bashkit"
	"shelley.exe.dev/llm"
)

// PermissionCallback is a function type for checking if a command is allowed to run
type PermissionCallback func(command string) error

// BashTool specifies an llm.Tool for executing shell commands.
type BashTool struct {
	// CheckPermission is called before running any command, if set
	CheckPermission PermissionCallback
	// EnableJITInstall enables just-in-time tool installation for missing commands
	EnableJITInstall bool
	// Timeouts holds the configurable timeout values (uses defaults if nil)
	Timeouts *Timeouts
	// WorkingDir is the shared mutable working directory.
	WorkingDir *MutableWorkingDir
	// LLMProvider provides access to LLM services for tool validation
	LLMProvider LLMServiceProvider
	// ConversationID is the ID of the conversation this tool belongs to.
	// It is exposed to invoked commands via SHELLEY_CONVERSATION_ID.
	ConversationID string
}

const (
	EnableBashToolJITInstall = true
	NoBashToolJITInstall     = false

	DefaultFastTimeout       = 30 * time.Second
	DefaultSlowTimeout       = 15 * time.Minute
	DefaultBackgroundTimeout = 24 * time.Hour
)

// Timeouts holds the configurable timeout values for bash commands.
type Timeouts struct {
	Fast       time.Duration // regular commands (e.g., ls, echo, simple scripts)
	Slow       time.Duration // commands that may reasonably take longer (e.g., downloads, builds, tests)
	Background time.Duration // background commands (e.g., servers, long-running processes)
}

// Fast returns t's fast timeout, or DefaultFastTimeout if t is nil.
func (t *Timeouts) fast() time.Duration {
	if t == nil {
		return DefaultFastTimeout
	}
	return t.Fast
}

// Slow returns t's slow timeout, or DefaultSlowTimeout if t is nil.
func (t *Timeouts) slow() time.Duration {
	if t == nil {
		return DefaultSlowTimeout
	}
	return t.Slow
}

// Background returns t's background timeout, or DefaultBackgroundTimeout if t is nil.
func (t *Timeouts) background() time.Duration {
	if t == nil {
		return DefaultBackgroundTimeout
	}
	return t.Background
}

// Tool returns an llm.Tool based on b.
func (b *BashTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        bashName,
		Description: strings.TrimSpace(bashDescription),
		InputSchema: llm.MustSchema(bashInputSchema),
		Run:         b.Run,
	}
}

// getWorkingDir returns the current working directory.
func (b *BashTool) getWorkingDir() string {
	return b.WorkingDir.Get()
}

// isNoTrailerSet checks if user has disabled co-author trailer via git config.
func (b *BashTool) isNoTrailerSet() bool {
	out, err := exec.Command("git", "config", "--get", "shelley.no-trailer").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

const (
	bashName        = "bash"
	bashDescription = `Executes shell commands via bash -c, returning combined stdout/stderr.
Bash state changes (working dir, variables, aliases) don't persist between calls.

With background=true, returns immediately, with output redirected to a file.
Use background for servers/demos that need to stay running.

MUST set slow_ok=true for potentially slow commands: builds, downloads,
installs, tests, or any other substantive operation.

Avoid overly destructive cleanup commands. Commands that could delete .git
directories, home directories, or use broad wildcards require explicit paths.
Confirm with the user before running destructive operations.

To change the working directory persistently, use the change_dir tool.

IMPORTANT: Keep commands concise. The command input must be less than 60k tokens.
For complex scripts, write them to a file first and then execute the file.
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
    "slow_ok": {
      "type": "boolean",
      "description": "Use extended timeout"
    },
    "background": {
      "type": "boolean",
      "description": "Execute in background"
    }
  }
}
`
)

type bashInput struct {
	Command    string `json:"command"`
	SlowOK     bool   `json:"slow_ok,omitempty"`
	Background bool   `json:"background,omitempty"`
}

// BashDisplayData is the display data sent to the UI for bash tool results.
type BashDisplayData struct {
	WorkingDir string `json:"workingDir"`
}

type BackgroundResult struct {
	PID     int
	OutFile string
}

func (r *BackgroundResult) XMLish() string {
	return fmt.Sprintf("<pid>%d</pid>\n<output_file>%s</output_file>\n<reminder>To stop the process: `kill -9 -%d`</reminder>\n",
		r.PID, r.OutFile, r.PID)
}

func (i *bashInput) timeout(t *Timeouts) time.Duration {
	switch {
	case i.Background:
		return t.background()
	case i.SlowOK:
		return t.slow()
	default:
		return t.fast()
	}
}

func (b *BashTool) Run(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var req bashInput
	if err := json.Unmarshal(m, &req); err != nil {
		return llm.ErrorfToolOut("failed to unmarshal bash command input: %w", err)
	}

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
	if !b.isNoTrailerSet() {
		req.Command = bashkit.AddCoauthorTrailer(req.Command, "Co-authored-by: Shelley <shelley@exe.dev>")
	}

	timeout := req.timeout(b.Timeouts)

	display := BashDisplayData{WorkingDir: wd}

	// If Background is set to true, use executeBackgroundBash
	if req.Background {
		result, err := b.executeBackgroundBash(ctx, req, timeout)
		if err != nil {
			return llm.ErrorToolOut(err)
		}
		return llm.ToolOut{LLMContent: llm.TextContent(result.XMLish()), Display: display}
	}

	// For foreground commands, use executeBash
	out, execErr := b.executeBash(ctx, req, timeout)
	if execErr != nil {
		return llm.ErrorToolOut(execErr)
	}
	return llm.ToolOut{LLMContent: llm.TextContent(out), Display: display}
}

const (
	largeOutputThreshold = 50 * 1024 // 50KB - threshold for saving to file
	firstLinesCount      = 2
	lastLinesCount       = 5
	maxLineLength        = 200 // truncate displayed lines to this length
)

func (b *BashTool) makeBashCommand(ctx context.Context, command string, out io.Writer) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	// Use shared WorkingDir if available, then context, then Pwd fallback
	cmd.Dir = b.getWorkingDir()
	cmd.Stdin = nil
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // set up for killing the process group
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			// Process hasn't started yet.
			// Not sure whether this is possible in practice,
			// but it is possible in theory, and it doesn't hurt to handle it gracefully.
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // kill entire process group
	}
	cmd.WaitDelay = 15 * time.Second // prevent indefinite hangs when child processes keep pipes open
	// Remove SKETCH_MODEL_URL, SKETCH_PUB_KEY, SKETCH_MODEL_API_KEY,
	// and any other future SKETCH_ goodies from the environment.
	// ...except for SKETCH_PROXY_ID, which is intentionally available.
	env := slices.DeleteFunc(os.Environ(), func(s string) bool {
		return strings.HasPrefix(s, "SKETCH_") && s != "SKETCH_PROXY_ID"
	})
	env = append(env, "SKETCH=1")          // signal that this has been run by Sketch, sometimes useful for scripts
	env = append(env, "EDITOR=/bin/false") // interactive editors won't work
	if b.ConversationID != "" {
		env = append(env, "SHELLEY_CONVERSATION_ID="+b.ConversationID)
	}
	cmd.Env = env
	return cmd
}

func cmdWait(cmd *exec.Cmd) error {
	err := cmd.Wait()
	// We used to kill the process group here, but it's not clear that
	// this is correct in the case of self-daemonizing processes,
	// and I encountered issues where daemons that I tried to run
	// as background tasks would mysteriously exit.
	return err
}

func (b *BashTool) executeBash(ctx context.Context, req bashInput, timeout time.Duration) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	output := new(bytes.Buffer)
	cmd := b.makeBashCommand(execCtx, req.Command, output)
	// TODO: maybe detect simple interactive git rebase commands and auto-background them?
	// Would need to hint to the agent what is happening.
	// We might also be able to do this for other simple interactive commands that use EDITOR.
	cmd.Env = append(cmd.Env, `GIT_SEQUENCE_EDITOR=echo "To do an interactive rebase, run it as a background task and check the output file." && exit 1`)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("command failed: %w", err)
	}

	err := cmdWait(cmd)

	out, formatErr := formatForegroundBashOutput(output.String())
	if formatErr != nil {
		return "", formatErr
	}

	if execCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("[command timed out after %s, showing output until timeout]\n%s", timeout, out)
	}
	if err != nil {
		return "", fmt.Errorf("[command failed: %w]\n%s", err, out)
	}

	return out, nil
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
	result.WriteString(fmt.Sprintf("[output too large (%s, %d lines), saved to: %s]\n\n",
		humanizeBytes(len(out)), len(lines), outFile))

	// First N lines
	result.WriteString("First lines:\n")
	firstN := min(firstLinesCount, len(lines))
	for i := 0; i < firstN; i++ {
		result.WriteString(fmt.Sprintf("%5d: %s\n", i+1, truncateLine(lines[i])))
	}

	// Last N lines
	result.WriteString("\n...\n\nLast lines:\n")
	startIdx := max(0, len(lines)-lastLinesCount)
	for i := startIdx; i < len(lines); i++ {
		result.WriteString(fmt.Sprintf("%5d: %s\n", i+1, truncateLine(lines[i])))
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

// executeBackgroundBash executes a command in the background and returns the pid and output file locations
func (b *BashTool) executeBackgroundBash(ctx context.Context, req bashInput, timeout time.Duration) (*BackgroundResult, error) {
	// Create temp output files
	tmpDir, err := os.MkdirTemp("", "sketch-bg-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	// We can't really clean up tempDir, because we have no idea
	// how far into the future the agent might want to read the output.

	outFile := filepath.Join(tmpDir, "output")
	out, err := os.Create(outFile)
	if err != nil {
		return nil, fmt.Errorf("failed to create output file: %w", err)
	}

	execCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout) // detach from tool use context
	cmd := b.makeBashCommand(execCtx, req.Command, out)
	cmd.Env = append(cmd.Env, `GIT_SEQUENCE_EDITOR=python3 -c "import os, sys, signal, threading; print(f\"Send USR1 to pid {os.getpid()} after editing {sys.argv[1]}\", flush=True); signal.signal(signal.SIGUSR1, lambda *_: sys.exit(0)); threading.Event().wait()"`)

	if err := cmd.Start(); err != nil {
		cancel()
		out.Close()
		os.RemoveAll(tmpDir) // clean up temp dir -- didn't start means we don't need the output
		return nil, fmt.Errorf("failed to start background command: %w", err)
	}

	// Wait for completion in the background, then do cleanup.
	go func() {
		err := cmdWait(cmd)
		// Leave a note to the agent so that it knows that the process has finished.
		if err != nil {
			fmt.Fprintf(out, "\n\n[background process failed: %v]\n", err)
		} else {
			fmt.Fprintf(out, "\n\n[background process completed]\n")
		}
		out.Close()
		cancel()
	}()

	return &BackgroundResult{
		PID:     cmd.Process.Pid,
		OutFile: outFile,
	}, nil
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
		_, err := exec.LookPath(cmd)
		if err == nil {
			doNotAttemptToolInstall[cmd] = true // spare future LookPath calls
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
// found in PATH, or an empty string if none are present.
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

	for _, m := range managers {
		if _, err := exec.LookPath(m); err == nil {
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
	llmService, err := b.selectBestLLM()
	if err != nil {
		return fmt.Errorf("failed to get LLM service for tool validation: %w", err)
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

	resp, err := llmService.Do(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to validate tool with LLM: %w", err)
	}

	if len(resp.Content) == 0 {
		return fmt.Errorf("empty response from LLM for tool validation")
	}

	response := strings.TrimSpace(resp.Content[0].Text)
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

// selectBestLLM selects the best available LLM service for bash tool validation
func (b *BashTool) selectBestLLM() (llm.Service, error) {
	if b.LLMProvider == nil {
		return nil, fmt.Errorf("no LLM provider available")
	}

	// Preferred models in order of preference for tool validation (fast, cheap models preferred)
	preferredModels := []string{"qwen3-coder-fireworks", "gpt-5-thinking-mini", "gpt5-mini", "claude-sonnet-4.5", "predictable"}

	for _, model := range preferredModels {
		svc, err := b.LLMProvider.GetService(model)
		if err == nil {
			return svc, nil
		}
	}

	// If no preferred model is available, try any available model
	available := b.LLMProvider.GetAvailableModels()
	if len(available) > 0 {
		return b.LLMProvider.GetService(available[0])
	}

	return nil, fmt.Errorf("no LLM services available")
}
