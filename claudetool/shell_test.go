package claudetool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

func runShell(t *testing.T, tool *ShellTool, input string, timeout time.Duration) (string, *ShellDisplayData, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out := tool.Tool().Run(ctx, json.RawMessage(input))
	var disp *ShellDisplayData
	if d, ok := out.Display.(ShellDisplayData); ok {
		disp = &d
	}
	if out.Error != nil {
		return "", disp, out.Error
	}
	if len(out.LLMContent) == 0 {
		return "", disp, nil
	}
	return out.LLMContent[0].Text, disp, nil
}

func newTestShell(t *testing.T) *ShellTool {
	t.Helper()
	td := t.TempDir()
	return &ShellTool{
		WorkingDir:    NewMutableWorkingDir("/"),
		BackgroundCtx: context.Background(),
		DefaultYield:  2 * time.Second,
		MaxYield:      10 * time.Second,
		TempDir:       td,
	}
}

func TestShellQuickCommand(t *testing.T) {
	s := newTestShell(t)
	out, disp, err := runShell(t, s, `{"command":"echo hello"}`, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("expected hello in output, got %q", out)
	}
	if disp == nil || disp.Yielded {
		t.Errorf("expected non-yielded result, got %+v", disp)
	}
	if disp.LogPath == "" || disp.PID == 0 {
		t.Errorf("expected pid and log path, got %+v", disp)
	}
}

func TestShellYieldsOnLongCommand(t *testing.T) {
	s := newTestShell(t)
	s.DefaultYield = 500 * time.Millisecond
	// Print something quickly so the tail is non-empty, then sleep.
	out, disp, err := runShell(t, s,
		`{"command":"echo starting; sleep 5; echo done"}`,
		15*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if disp == nil || !disp.Yielded {
		t.Fatalf("expected yielded result, got %+v", disp)
	}
	if disp.PID == 0 {
		t.Fatalf("expected pid, got 0")
	}
	if !strings.Contains(out, "still running") {
		t.Errorf("expected 'still running' in payload, got %q", out)
	}
	if !strings.Contains(out, "starting") {
		t.Errorf("expected tail to include 'starting', got %q", out)
	}
	if !strings.Contains(out, disp.LogPath) {
		t.Errorf("expected log path %s in payload, got %q", disp.LogPath, out)
	}

	// Clean up: kill the process group.
	defer func() {
		_ = syscall.Kill(-disp.PID, syscall.SIGKILL)
	}()

	// Process should still be alive.
	if err := syscall.Kill(disp.PID, 0); err != nil {
		t.Errorf("expected pid %d alive after yield, got: %v", disp.PID, err)
	}

	// Wait for it to finish naturally; check log grew.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(disp.PID, 0) != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	b, err := os.ReadFile(disp.LogPath)
	if err != nil {
		t.Fatalf("could not read log: %v", err)
	}
	if !strings.Contains(string(b), "done") {
		t.Errorf("expected log to contain 'done' after process completed, got %q", string(b))
	}
}

func TestShellExplicitYieldTimeSeconds(t *testing.T) {
	s := newTestShell(t)
	start := time.Now()
	_, disp, err := runShell(t, s,
		`{"command":"sleep 5","yield_time_seconds":1}`,
		10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Errorf("yield_time_seconds=1 took too long: %s", elapsed)
	}
	if disp == nil || !disp.Yielded {
		t.Errorf("expected yield, got %+v", disp)
	}
	defer func() {
		if disp != nil {
			_ = syscall.Kill(-disp.PID, syscall.SIGKILL)
		}
	}()
}

func TestShellYieldTimeCappedAtMax(t *testing.T) {
	s := newTestShell(t)
	s.MaxYield = 500 * time.Millisecond
	start := time.Now()
	_, disp, err := runShell(t, s,
		`{"command":"sleep 10","yield_time_seconds":3600}`,
		10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("expected cap to apply, took %s", time.Since(start))
	}
	if disp == nil || !disp.Yielded {
		t.Errorf("expected yield, got %+v", disp)
	}
	defer func() {
		if disp != nil {
			_ = syscall.Kill(-disp.PID, syscall.SIGKILL)
		}
	}()
}

func TestShellFailingCommand(t *testing.T) {
	s := newTestShell(t)
	_, _, err := runShell(t, s, `{"command":"false"}`, 5*time.Second)
	if err == nil {
		t.Fatalf("expected error for failing command")
	}
	if !strings.Contains(err.Error(), "command failed") {
		t.Errorf("expected 'command failed' message, got %v", err)
	}
}

func TestShellWorkingDirMissing(t *testing.T) {
	s := newTestShell(t)
	s.WorkingDir = NewMutableWorkingDir("/this/does/not/exist/shelley/test")
	_, _, err := runShell(t, s, `{"command":"echo x"}`, 5*time.Second)
	if err == nil {
		t.Fatalf("expected error for missing working dir")
	}
}

func TestShellContextCancelKills(t *testing.T) {
	s := newTestShell(t)
	s.DefaultYield = 30 * time.Second // would otherwise yield slowly

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	out := s.Tool().Run(ctx, json.RawMessage(`{"command":"sleep 30"}`))
	if out.Error == nil {
		t.Fatalf("expected error after cancel")
	}
	if !strings.Contains(out.Error.Error(), "cancelled") {
		t.Errorf("expected cancelled in error, got %v", out.Error)
	}
}

// extractSnippet returns the indented snippet lines following marker in payload,
// joined by newlines, stopping at the next blank line.
func extractSnippet(t *testing.T, payload, marker string) string {
	t.Helper()
	idx := strings.Index(payload, marker)
	if idx < 0 {
		t.Fatalf("marker %q not found in payload:\n%s", marker, payload)
	}
	rest := payload[idx+len(marker):]
	// Skip leading newline after marker.
	rest = strings.TrimLeft(rest, "\n")
	var lines []string
	for _, line := range strings.Split(rest, "\n") {
		if strings.TrimSpace(line) == "" {
			break
		}
		lines = append(lines, strings.TrimSpace(line))
	}
	if len(lines) == 0 {
		t.Fatalf("no snippet lines after marker %q", marker)
	}
	return strings.Join(lines, "\n")
}

func TestShellProgressLoop(t *testing.T) {
	s := newTestShell(t)
	s.DefaultYield = 5 * time.Second

	var mu sync.Mutex
	var updates []string
	progress := func(p llm.ToolProgress) {
		mu.Lock()
		defer mu.Unlock()
		updates = append(updates, p.Output)
		if p.ToolName != "shell" {
			t.Errorf("unexpected tool name: %s", p.ToolName)
		}
		if p.ToolUseID != "tool-use-123" {
			t.Errorf("unexpected tool use id: %s", p.ToolUseID)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = WithToolProgress(ctx, progress)
	ctx = WithToolUseID(ctx, "tool-use-123")

	out := s.Tool().Run(ctx, json.RawMessage(
		`{"command":"echo first; sleep 1; echo second"}`))
	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) == 0 {
		t.Fatalf("expected progress updates, got none")
	}
	// At least one update should contain partial output ("first") before final.
	sawFirstAlone := false
	for _, u := range updates {
		if strings.Contains(u, "first") && !strings.Contains(u, "second") {
			sawFirstAlone = true
			break
		}
	}
	if !sawFirstAlone {
		t.Errorf("expected at least one progress update with 'first' before 'second'; got %v", updates)
	}
}

func TestShellYieldedKillSnippetWorks(t *testing.T) {
	s := newTestShell(t)
	s.DefaultYield = 500 * time.Millisecond

	payload, disp, err := runShell(t, s,
		`{"command":"trap 'echo TERMED; exit' TERM; sleep 30"}`,
		10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if disp == nil || !disp.Yielded {
		t.Fatalf("expected yield, got %+v", disp)
	}
	defer func() {
		_ = syscall.Kill(-disp.PID, syscall.SIGKILL)
	}()

	killSnippet := extractSnippet(t, payload, "To kill the process and its children:")
	t.Logf("kill snippet: %s", killSnippet)
	cmd := exec.Command("bash", "-c", killSnippet)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kill snippet failed: %v\noutput: %s", err, out)
	}

	// Wait for the process to die. kill -KILL eventually wins.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(disp.PID, 0); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("process %d still alive after kill snippet", disp.PID)
}

func TestShellYieldedStatusSnippetWorks(t *testing.T) {
	s := newTestShell(t)
	s.DefaultYield = 500 * time.Millisecond

	payload, disp, err := runShell(t, s,
		`{"command":"echo hi; sleep 10"}`,
		10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if disp == nil || !disp.Yielded {
		t.Fatalf("expected yield, got %+v", disp)
	}
	defer func() {
		_ = syscall.Kill(-disp.PID, syscall.SIGKILL)
	}()

	statusSnippet := extractSnippet(t, payload, "To check status without waiting:")
	t.Logf("status snippet: %s", statusSnippet)
	cmd := exec.Command("bash", "-c", statusSnippet)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status snippet failed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), "running") && !strings.Contains(string(out), "exited") {
		t.Errorf("expected status output to contain 'running' or 'exited', got %q", out)
	}
	// Tail of log should also be present.
	if !strings.Contains(string(out), "hi") {
		t.Errorf("expected status output to contain log tail 'hi', got %q", out)
	}
}

func TestShellRejectsBashkitCheckedCommand(t *testing.T) {
	s := newTestShell(t)
	_, disp, err := runShell(t, s, `{"command":"rm -rf /"}`, 5*time.Second)
	if err == nil {
		t.Fatalf("expected bashkit.Check to reject 'rm -rf /'")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("expected permission denied error from bashkit, got %v", err)
	}
	// Ensure no process was spawned.
	if disp != nil && disp.PID != 0 {
		t.Errorf("expected no PID for rejected command, got %+v", disp)
	}
}

func TestShellLargeOutputSummarized(t *testing.T) {
	s := newTestShell(t)
	s.DefaultYield = 30 * time.Second

	// Print > 50KB and exit promptly. yes + head -c is fast and deterministic.
	cmdJSON := `{"command":"head -c 80000 /dev/urandom | base64 -w 0; echo"}`
	out, disp, err := runShell(t, s, cmdJSON, 15*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if disp == nil || disp.Yielded {
		t.Fatalf("expected non-yielded result, got %+v", disp)
	}
	if !strings.Contains(out, "output too large") {
		t.Errorf("expected 'output too large' marker in output, got: %.200s", out)
	}
	if !strings.Contains(out, "saved to:") {
		t.Errorf("expected 'saved to:' path in output, got: %.200s", out)
	}
}

func TestShellCoauthorTrailerIntegration(t *testing.T) {
	// Exercise the AddCoauthorTrailer code path with a 'git commit' style
	// command. We don't want a real commit, so we run it in a non-git dir;
	// the command will fail with 'not a git repository' but the trailer code
	// should run without panicking.
	s := newTestShell(t)
	s.WorkingDir = NewMutableWorkingDir(t.TempDir())
	// 'git commit -m foo' will fail (no repo) but exercise trailer logic.
	_, _, err := runShell(t, s,
		`{"command":"git commit -m 'test message'"}`,
		5*time.Second)
	// We expect a failure because there's no repo, but the trailer path
	// should have run without panicking. Either outcome is acceptable —
	// what we're really testing is that the code path doesn't crash.
	if err != nil && !strings.Contains(err.Error(), "command failed") {
		t.Errorf("unexpected error shape: %v", err)
	}
}
