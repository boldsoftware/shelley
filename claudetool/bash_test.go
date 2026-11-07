package claudetool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/josharian/sockpath"
	"shelley.exe.dev/gitstate"
)

func TestBashToolExitCode(t *testing.T) {
	workingDir := t.TempDir()
	tool := (&BashTool{WorkingDir: NewMutableWorkingDir(workingDir)}).Tool()

	t.Run("success", func(t *testing.T) {
		out := tool.Run(t.Context(), json.RawMessage(`{"command":"printf unchanged"}`))
		if out.Error != nil {
			t.Fatalf("Run() error = %v", out.Error)
		}
		if got := out.LLMContent[0].Text; got != "unchanged" {
			t.Fatalf("LLM output = %q, want %q", got, "unchanged")
		}
		display := bashDisplayData(t, out.Display)
		if display.WorkingDir != workingDir {
			t.Errorf("WorkingDir = %q, want %q", display.WorkingDir, workingDir)
		}
		if display.ExitCode == nil || *display.ExitCode != 0 {
			t.Errorf("ExitCode = %v, want 0", display.ExitCode)
		}
		encoded, err := json.Marshal(display)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"exitCode":0`) {
			t.Errorf("marshaled display = %s, want exitCode 0", encoded)
		}
	})

	t.Run("nonzero exit", func(t *testing.T) {
		out := tool.Run(t.Context(), json.RawMessage(`{"command":"printf unchanged; exit 7"}`))
		if out.Error == nil {
			t.Fatal("Run() error = nil, want non-nil")
		}
		if got, want := out.Error.Error(), "[command failed: exit status 7]\nunchanged"; got != want {
			t.Fatalf("error = %q, want %q", got, want)
		}
		display := bashDisplayData(t, out.Display)
		if display.WorkingDir != workingDir {
			t.Errorf("WorkingDir = %q, want %q", display.WorkingDir, workingDir)
		}
		if display.ExitCode == nil || *display.ExitCode != 7 {
			t.Errorf("ExitCode = %v, want 7", display.ExitCode)
		}
	})

	t.Run("cancelled before execution", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		out := tool.Run(ctx, json.RawMessage(`{"command":"printf should-not-run"}`))
		if out.Error == nil {
			t.Fatal("Run() error = nil, want non-nil")
		}
		display := bashDisplayData(t, out.Display)
		if display.WorkingDir != workingDir {
			t.Errorf("WorkingDir = %q, want %q", display.WorkingDir, workingDir)
		}
		if display.ExitCode != nil {
			t.Errorf("ExitCode = %v, want unknown", *display.ExitCode)
		}
		encoded, err := json.Marshal(display)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "exitCode") {
			t.Errorf("marshaled display = %s, want exitCode omitted", encoded)
		}
	})

	t.Run("signal", func(t *testing.T) {
		out := tool.Run(t.Context(), json.RawMessage(`{"command":"kill -TERM $$"}`))
		if out.Error == nil {
			t.Fatal("Run() error = nil, want non-nil")
		}
		display := bashDisplayData(t, out.Display)
		if display.ExitCode == nil || *display.ExitCode != 128+15 {
			t.Errorf("ExitCode = %v, want 143 (shell convention for SIGTERM)", display.ExitCode)
		}
	})
}

func bashDisplayData(t *testing.T, value any) BashDisplayData {
	t.Helper()
	display, ok := value.(BashDisplayData)
	if !ok {
		t.Fatalf("Display = %T, want BashDisplayData", value)
	}
	return display
}

func TestBashTool(t *testing.T) {
	bashTool := &BashTool{WorkingDir: NewMutableWorkingDir("/")}
	tool := bashTool.Tool()

	// Test basic functionality
	t.Run("Basic Command", func(t *testing.T) {
		input := json.RawMessage(`{"command":"echo 'Hello, world!'"}`)

		toolOut := tool.Run(t.Context(), input)
		if toolOut.Error != nil {
			t.Fatalf("Unexpected error: %v", toolOut.Error)
		}
		result := toolOut.LLMContent

		expected := "Hello, world!\n"
		if len(result) == 0 || result[0].Text != expected {
			t.Errorf("Expected %q, got %q", expected, result[0].Text)
		}

		// Verify Display data contains working directory
		display, ok := toolOut.Display.(BashDisplayData)
		if !ok {
			t.Fatalf("Expected Display to be BashDisplayData, got %T", toolOut.Display)
		}
		if display.WorkingDir != "/" {
			t.Errorf("Expected WorkingDir to be '/', got %q", display.WorkingDir)
		}
	})

	// Test with arguments
	t.Run("Command With Arguments", func(t *testing.T) {
		input := json.RawMessage(`{"command":"echo -n foo && echo -n bar"}`)

		toolOut := tool.Run(t.Context(), input)
		if toolOut.Error != nil {
			t.Fatalf("Unexpected error: %v", toolOut.Error)
		}
		result := toolOut.LLMContent

		expected := "foobar"
		if len(result) == 0 || result[0].Text != expected {
			t.Errorf("Expected %q, got %q", expected, result[0].Text)
		}
	})

	// Test command that fails
	t.Run("Failed Command", func(t *testing.T) {
		input := json.RawMessage(`{"command":"exit 1"}`)

		toolOut := tool.Run(t.Context(), input)
		if toolOut.Error == nil {
			t.Errorf("Expected error for failed command, got none")
		}
	})

	// Test invalid input
	t.Run("Invalid JSON Input", func(t *testing.T) {
		input := json.RawMessage(`{"command":123}`) // Invalid JSON (command must be string)

		toolOut := tool.Run(t.Context(), input)
		if toolOut.Error == nil {
			t.Errorf("Expected error for invalid input, got none")
		}
	})
}

func TestChainedCdLeavesDir(t *testing.T) {
	const wd = "/work/project"
	tests := []struct {
		name  string
		paths []string
		want  bool
	}{
		{name: "no chained cd", paths: nil, want: false},
		{name: "current directory relative path", paths: []string{"."}, want: false},
		{name: "current directory absolute path", paths: []string{"/work/project"}, want: false},
		{name: "different directory", paths: []string{"/tmp"}, want: true},
		{name: "current then different", paths: []string{".", "/tmp"}, want: true},
		{name: "non-literal path", paths: []string{""}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := chainedCdLeavesDir(tc.paths, wd); got != tc.want {
				t.Errorf("chainedCdLeavesDir(%q, %q) = %v, want %v", tc.paths, wd, got, tc.want)
			}
		})
	}
}

// TestBashTouchesGitDirs checks that the bash tool tells its git tracker
// where commands run git, resolving ~ and paths relative to its directory.
func TestBashTouchesGitDirs(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	wd := filepath.Join(root, "wd")
	relative, tilde := filepath.Join(wd, "relative"), filepath.Join(root, "home", "tilde")
	for _, dir := range []string{relative, tilde} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "T"}, {"commit", "-q", "--no-verify", "--allow-empty", "-m", "initial"}} {
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, out)
			}
		}
	}
	mwd := NewMutableWorkingDir(wd)
	tracker := gitstate.NewTracker(mwd.Get)
	jobs := make(recordingJobs, 1)
	tool := (&BashTool{WorkingDir: mwd, GitTracker: tracker, Jobs: jobs}).Tool()
	run := func(req bashInput) {
		t.Helper()
		input, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		if out := tool.Run(t.Context(), input); out.Error != nil {
			t.Fatalf("run bash tool: %v", out.Error)
		}
	}
	wantChanges := func(want ...string) {
		t.Helper()
		var got []string
		for _, state := range tracker.Changes() {
			got = append(got, state.Worktree+" "+state.Subject)
		}
		if !slices.Equal(got, want) {
			t.Errorf("changes = %q, want %q", got, want)
		}
	}

	run(bashInput{Command: "cd relative && git commit -q --no-verify --allow-empty -m one && git -C ~/tilde commit -q --no-verify --allow-empty -m two"})
	wantChanges(tilde+" two", relative+" one")
	// Once its commands have exited, others' commits are not reported.
	if out, err := exec.Command("git", "-C", relative, "commit", "-q", "--no-verify", "--allow-empty", "-m", "external").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	wantChanges()

	// A background command's repositories are watched until it exits.
	g := newGate(t)
	run(bashInput{Command: "cd relative && " + g.wait() + " && git commit -q --no-verify --allow-empty -m later", Background: true})
	bg := <-jobs
	wantChanges()
	g.release(t)
	waitClosed(t, bg.exited, "background commit")
	wantChanges(relative + " later")
}

func TestBashChainedCdHint(t *testing.T) {
	wd := t.TempDir()
	tool := (&BashTool{WorkingDir: NewMutableWorkingDir(wd)}).Tool()
	run := func(t *testing.T, command string) string {
		t.Helper()
		input, err := json.Marshal(bashInput{Command: command})
		if err != nil {
			t.Fatalf("marshal input: %v", err)
		}
		out := tool.Run(t.Context(), input)
		if out.Error != nil {
			t.Fatalf("run bash tool: %v", out.Error)
		}
		return out.LLMContent[0].Text
	}

	if got := run(t, "cd . && printf done"); strings.Contains(got, "[shelley:") {
		t.Errorf("cd to the current directory produced a hint: %q", got)
	}
	got := run(t, "cd / && printf done")
	for _, want := range []string{"change_dir", wd} {
		if !strings.Contains(got, want) {
			t.Errorf("first chained cd output %q does not contain %q", got, want)
		}
	}
	if got := run(t, "cd / && printf done"); strings.Contains(got, "[shelley:") {
		t.Errorf("second chained cd repeated the hint: %q", got)
	}
}

func TestExecuteBashInDirUsesSnapshot(t *testing.T) {
	original := sockpath.TempDir(t)
	bashTool := &BashTool{WorkingDir: NewMutableWorkingDir(original)}
	snapshot := bashTool.WorkingDir.Get()
	bashTool.WorkingDir.Set(t.TempDir())

	res, err := bashTool.executeBashInDir(t.Context(), bashInput{Command: "pwd"}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(res.out); got != original {
		t.Fatalf("pwd = %q, want snapshotted directory %q", got, original)
	}
}

func TestExecuteBash(t *testing.T) {
	ctx := t.Context()
	bashTool := &BashTool{WorkingDir: NewMutableWorkingDir("/")}

	// Test successful command
	t.Run("Successful Command", func(t *testing.T) {
		req := bashInput{
			Command: "echo 'Success'",
		}

		res, err := bashTool.executeBashInDir(ctx, req, bashTool.WorkingDir.Get())
		output := res.out
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		want := "Success\n"
		if output != want {
			t.Errorf("Expected %q, got %q", want, output)
		}
	})

	// Test SHELLEY_CONVERSATION_ID environment variable is set when configured
	t.Run("SHELLEY_CONVERSATION_ID Environment Variable", func(t *testing.T) {
		bashWithConvID := &BashTool{
			WorkingDir: NewMutableWorkingDir("/"),
			Env:        ShelleyEnv{ConversationID: "test-conv-123"},
		}
		req := bashInput{
			Command: "echo $SHELLEY_CONVERSATION_ID",
		}

		res, err := bashWithConvID.executeBashInDir(ctx, req, bashWithConvID.WorkingDir.Get())
		output := res.out
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		want := "test-conv-123\n"
		if output != want {
			t.Errorf("Expected SHELLEY_CONVERSATION_ID=test-conv-123, got %q", output)
		}
	})

	// The bash tool should inject the full SHELLEY_* environment, matching the
	// vars exposed to interactive "!" terminals.
	t.Run("Full Shelley Environment", func(t *testing.T) {
		bashWithEnv := &BashTool{
			WorkingDir: NewMutableWorkingDir("/"),
			Env: ShelleyEnv{
				ConversationID:   "cid",
				ConversationSlug: "slug",
				Model:            "predictable",
				UserEmail:        "x@y.z",
				Port:             4242,
			},
		}
		req := bashInput{
			Command: `printf '%s|%s|%s|%s|%s|%s' "$SHELLEY_CONVERSATION_ID" "$SHELLEY_CONVERSATION_SLUG" "$SHELLEY_MODEL" "$SHELLEY_USER_EMAIL" "$SHELLEY_PORT" "$SHELLEY_URL"`,
		}
		res, err := bashWithEnv.executeBashInDir(ctx, req, bashWithEnv.WorkingDir.Get())
		output := res.out
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		want := "cid|slug|predictable|x@y.z|4242|http://localhost:4242"
		if output != want {
			t.Errorf("Expected %q, got %q", want, output)
		}
	})

	// Test SHELLEY_CONVERSATION_ID is not set when not configured
	t.Run("SHELLEY_CONVERSATION_ID Not Set When Empty", func(t *testing.T) {
		req := bashInput{
			Command: "echo \"conv_id:$SHELLEY_CONVERSATION_ID:\"",
		}

		res, err := bashTool.executeBashInDir(ctx, req, bashTool.WorkingDir.Get())
		output := res.out
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Should be empty since ConversationID is not set on bashTool
		want := "conv_id::\n"
		if output != want {
			t.Errorf("Expected empty SHELLEY_CONVERSATION_ID, got %q", output)
		}
	})

	// Test that bash runs as a login shell (sources user profile)
	t.Run("Login Shell", func(t *testing.T) {
		req := bashInput{
			Command: "shopt login_shell | grep -q on && echo login",
		}

		res, err := bashTool.executeBashInDir(ctx, req, bashTool.WorkingDir.Get())
		output := res.out
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		want := "login\n"
		if output != want {
			t.Errorf("Expected bash to run as login shell, got %q", output)
		}
	})

	// Test command with output to stderr
	t.Run("Command with stderr", func(t *testing.T) {
		req := bashInput{
			Command: "echo 'Error message' >&2 && echo 'Success'",
		}

		res, err := bashTool.executeBashInDir(ctx, req, bashTool.WorkingDir.Get())
		output := res.out
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		want := "Error message\nSuccess\n"
		if output != want {
			t.Errorf("Expected %q, got %q", want, output)
		}
	})

	// Test command that fails with stderr
	t.Run("Failed Command with stderr", func(t *testing.T) {
		req := bashInput{
			Command: "echo 'Error message' >&2 && exit 1",
		}

		_, err := bashTool.executeBashInDir(ctx, req, bashTool.WorkingDir.Get())
		if err == nil {
			t.Errorf("Expected error for failed command, got none")
		} else if !strings.Contains(err.Error(), "Error message") {
			t.Errorf("Expected stderr in error message, got: %v", err)
		}
	})
}

func TestFormatForegroundBashOutput(t *testing.T) {
	// Test small output (under threshold) - should pass through unchanged
	t.Run("Small Output", func(t *testing.T) {
		smallOutput := "line 1\nline 2\nline 3\n"
		result, err := formatForegroundBashOutput(smallOutput)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result != smallOutput {
			t.Errorf("Expected small output to pass through unchanged, got %q", result)
		}
	})

	// Test large output (over 50KB) - should save to file and return summary
	t.Run("Large Output With Lines", func(t *testing.T) {
		// Generate output > 50KB with many lines
		var lines []string
		for i := 1; i <= 1000; i++ {
			lines = append(lines, strings.Repeat("x", 60)+" line "+string(rune('0'+i%10)))
		}
		largeOutput := strings.Join(lines, "\n")
		if len(largeOutput) < largeOutputThreshold {
			t.Fatalf("Test setup error: output is only %d bytes, need > %d", len(largeOutput), largeOutputThreshold)
		}

		result, err := formatForegroundBashOutput(largeOutput)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Should mention the file
		if !strings.Contains(result, "saved to:") {
			t.Errorf("Expected result to mention saved file, got:\n%s", result)
		}

		// Should have first 2 lines numbered
		if !strings.Contains(result, "    1:") || !strings.Contains(result, "    2:") {
			t.Errorf("Expected first 2 numbered lines, got:\n%s", result)
		}

		// Should have last 5 lines numbered
		if !strings.Contains(result, "  996:") || !strings.Contains(result, " 1000:") {
			t.Errorf("Expected last 5 numbered lines, got:\n%s", result)
		}

		t.Logf("Large output result:\n%s", result)
	})

	// Test large output with few/no lines (binary-like)
	t.Run("Large Output No Lines", func(t *testing.T) {
		// Generate > 50KB of data with no newlines
		largeOutput := strings.Repeat("x", largeOutputThreshold+1000)

		result, err := formatForegroundBashOutput(largeOutput)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Should mention the file
		if !strings.Contains(result, "saved to:") {
			t.Errorf("Expected result to mention saved file, got:\n%s", result)
		}

		// Should indicate line count
		if !strings.Contains(result, "1 lines") {
			t.Errorf("Expected result to indicate line count, got:\n%s", result)
		}

		t.Logf("Large binary-like output result:\n%s", result)
	})

	// Test large output with very long lines (e.g., minified JS)
	t.Run("Large Output With Long Lines", func(t *testing.T) {
		// Generate output > 50KB with few very long lines
		longLine := strings.Repeat("abcdefghij", 1000) // 10KB per line
		lines := []string{longLine, longLine, longLine, longLine, longLine, longLine}
		largeOutput := strings.Join(lines, "\n")
		if len(largeOutput) < largeOutputThreshold {
			t.Fatalf("Test setup error: output is only %d bytes, need > %d", len(largeOutput), largeOutputThreshold)
		}

		result, err := formatForegroundBashOutput(largeOutput)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Result should be reasonable size (not blow up context)
		if len(result) > 4096 {
			t.Errorf("Expected truncated result < 4KB, got %d bytes:\n%s", len(result), result)
		}

		// Should mention the file
		if !strings.Contains(result, "saved to:") {
			t.Errorf("Expected result to mention saved file, got:\n%s", result)
		}

		// Lines should be truncated
		if !strings.Contains(result, "...") {
			t.Errorf("Expected truncated lines with '...', got:\n%s", result)
		}

		t.Logf("Large output with long lines result:\n%s", result)
	})
}

func TestIsNoTrailerSet(t *testing.T) {
	// Test when config is not set (default)
	t.Run("Default No Config", func(t *testing.T) {
		if isNoTrailerSet() {
			t.Error("Expected isNoTrailerSet() to be false when not configured")
		}
	})

	// Test when config is set to true
	t.Run("Config Set True", func(t *testing.T) {
		// Set the global config
		cmd := exec.Command("git", "config", "--global", "shelley.no-trailer", "true")
		if err := cmd.Run(); err != nil {
			t.Skipf("Could not set git config: %v", err)
		}
		defer exec.Command("git", "config", "--global", "--unset", "shelley.no-trailer").Run()

		if !isNoTrailerSet() {
			t.Error("Expected isNoTrailerSet() to be true when shelley.no-trailer=true")
		}
	})

	// Test when config is set to false
	t.Run("Config Set False", func(t *testing.T) {
		cmd := exec.Command("git", "config", "--global", "shelley.no-trailer", "false")
		if err := cmd.Run(); err != nil {
			t.Skipf("Could not set git config: %v", err)
		}
		defer exec.Command("git", "config", "--global", "--unset", "shelley.no-trailer").Run()

		if isNoTrailerSet() {
			t.Error("Expected isNoTrailerSet() to be false when shelley.no-trailer=false")
		}
	})
}

func TestShellHasCommand(t *testing.T) {
	ctx := t.Context()

	// bash is always available since we run tests via bash
	if !shellHasCommand(ctx, "bash") {
		t.Error("expected bash to be found")
	}

	// A non-existent command should not be found
	if shellHasCommand(ctx, "definitely-not-a-real-command-xyz123") {
		t.Error("expected non-existent command to not be found")
	}

	// Cancelled context should return false
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if shellHasCommand(canceled, "bash") {
		t.Error("expected cancelled context to return false")
	}
}
