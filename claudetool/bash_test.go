package claudetool

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestBashSlowOk(t *testing.T) {
	// Test that slow_ok flag is properly handled
	t.Run("SlowOk Flag", func(t *testing.T) {
		input := json.RawMessage(`{"command":"echo 'slow test'","slow_ok":true}`)

		bashTool := (&BashTool{WorkingDir: NewMutableWorkingDir("/")}).Tool()
		toolOut := bashTool.Run(context.Background(), input)
		if toolOut.Error != nil {
			t.Fatalf("Unexpected error: %v", toolOut.Error)
		}
		result := toolOut.LLMContent

		expected := "slow test\n"
		if len(result) == 0 || result[0].Text != expected {
			t.Errorf("Expected %q, got %q", expected, result[0].Text)
		}
	})
}

func TestBashTool(t *testing.T) {
	bashTool := &BashTool{WorkingDir: NewMutableWorkingDir("/")}
	tool := bashTool.Tool()

	// Test basic functionality
	t.Run("Basic Command", func(t *testing.T) {
		input := json.RawMessage(`{"command":"echo 'Hello, world!'"}`)

		toolOut := tool.Run(context.Background(), input)
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

		toolOut := tool.Run(context.Background(), input)
		if toolOut.Error != nil {
			t.Fatalf("Unexpected error: %v", toolOut.Error)
		}
		result := toolOut.LLMContent

		expected := "foobar"
		if len(result) == 0 || result[0].Text != expected {
			t.Errorf("Expected %q, got %q", expected, result[0].Text)
		}
	})

	// Test with slow_ok parameter
	t.Run("With SlowOK", func(t *testing.T) {
		inputObj := struct {
			Command string `json:"command"`
			SlowOK  bool   `json:"slow_ok"`
		}{
			Command: "sleep 0.1 && echo 'Completed'",
			SlowOK:  true,
		}
		inputJSON, err := json.Marshal(inputObj)
		if err != nil {
			t.Fatalf("Failed to marshal input: %v", err)
		}

		toolOut := tool.Run(context.Background(), inputJSON)
		if toolOut.Error != nil {
			t.Fatalf("Unexpected error: %v", toolOut.Error)
		}
		result := toolOut.LLMContent

		expected := "Completed\n"
		if len(result) == 0 || result[0].Text != expected {
			t.Errorf("Expected %q, got %q", expected, result[0].Text)
		}
	})

	// Test command timeout with custom timeout config
	t.Run("Command Timeout", func(t *testing.T) {
		// Use a custom BashTool with very short timeout
		customTimeouts := &Timeouts{
			Fast: 100 * time.Millisecond,
			Slow: 100 * time.Millisecond,
		}
		customBash := &BashTool{
			WorkingDir: NewMutableWorkingDir("/"),
			Timeouts:   customTimeouts,
		}
		tool := customBash.Tool()

		input := json.RawMessage(`{"command":"sleep 0.5 && echo 'Should not see this'"}`)

		toolOut := tool.Run(context.Background(), input)
		if toolOut.Error == nil {
			t.Errorf("Expected timeout error, got none")
		} else if !strings.Contains(toolOut.Error.Error(), "timed out") {
			t.Errorf("Expected timeout error, got: %v", toolOut.Error)
		}
	})

	// Test command that fails
	t.Run("Failed Command", func(t *testing.T) {
		input := json.RawMessage(`{"command":"exit 1"}`)

		toolOut := tool.Run(context.Background(), input)
		if toolOut.Error == nil {
			t.Errorf("Expected error for failed command, got none")
		}
	})

	// Test invalid input
	t.Run("Invalid JSON Input", func(t *testing.T) {
		input := json.RawMessage(`{"command":123}`) // Invalid JSON (command must be string)

		toolOut := tool.Run(context.Background(), input)
		if toolOut.Error == nil {
			t.Errorf("Expected error for invalid input, got none")
		}
	})
}

func TestExecuteBash(t *testing.T) {
	ctx := context.Background()
	bashTool := &BashTool{WorkingDir: NewMutableWorkingDir("/")}

	// Test successful command
	t.Run("Successful Command", func(t *testing.T) {
		req := bashInput{
			Command: "echo 'Success'",
		}

		output, err := bashTool.executeBash(ctx, req, 5*time.Second)
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
			WorkingDir:     NewMutableWorkingDir("/"),
			ConversationID: "test-conv-123",
		}
		req := bashInput{
			Command: "echo $SHELLEY_CONVERSATION_ID",
		}

		output, err := bashWithConvID.executeBash(ctx, req, 5*time.Second)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		want := "test-conv-123\n"
		if output != want {
			t.Errorf("Expected SHELLEY_CONVERSATION_ID=test-conv-123, got %q", output)
		}
	})

	// Test SHELLEY_CONVERSATION_ID is not set when not configured
	t.Run("SHELLEY_CONVERSATION_ID Not Set When Empty", func(t *testing.T) {
		req := bashInput{
			Command: "echo \"conv_id:$SHELLEY_CONVERSATION_ID:\"",
		}

		output, err := bashTool.executeBash(ctx, req, 5*time.Second)
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

		output, err := bashTool.executeBash(ctx, req, 5*time.Second)
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

		output, err := bashTool.executeBash(ctx, req, 5*time.Second)
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

		_, err := bashTool.executeBash(ctx, req, 5*time.Second)
		if err == nil {
			t.Errorf("Expected error for failed command, got none")
		} else if !strings.Contains(err.Error(), "Error message") {
			t.Errorf("Expected stderr in error message, got: %v", err)
		}
	})

	// Test timeout
	t.Run("Command Timeout", func(t *testing.T) {
		req := bashInput{
			Command: "sleep 1 && echo 'Should not see this'",
		}

		start := time.Now()
		_, err := bashTool.executeBash(ctx, req, 100*time.Millisecond)
		elapsed := time.Since(start)

		// Command should time out after ~100ms, not wait for full 1 second
		if elapsed >= 1*time.Second {
			t.Errorf("Command did not respect timeout, took %v", elapsed)
		}

		if err == nil {
			t.Errorf("Expected timeout error, got none")
		} else if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("Expected timeout error, got: %v", err)
		}
	})
}

func TestBashTimeout(t *testing.T) {
	// Test default timeout values
	t.Run("Default Timeout Values", func(t *testing.T) {
		// Test foreground default timeout
		foreground := bashInput{
			Command: "echo 'test'",
		}
		fgTimeout := foreground.timeout(nil)
		expectedFg := 30 * time.Second
		if fgTimeout != expectedFg {
			t.Errorf("Expected foreground default timeout to be %v, got %v", expectedFg, fgTimeout)
		}

		// Test slow_ok timeout
		slowOk := bashInput{
			Command: "echo 'test'",
			SlowOK:  true,
		}
		slowTimeout := slowOk.timeout(nil)
		expectedSlow := 15 * time.Minute
		if slowTimeout != expectedSlow {
			t.Errorf("Expected slow_ok timeout to be %v, got %v", expectedSlow, slowTimeout)
		}

		// Test custom timeout config
		customTimeouts := &Timeouts{
			Fast: 5 * time.Second,
			Slow: 2 * time.Minute,
		}
		customFast := bashInput{
			Command: "echo 'test'",
		}
		customTimeout := customFast.timeout(customTimeouts)
		expectedCustom := 5 * time.Second
		if customTimeout != expectedCustom {
			t.Errorf("Expected custom timeout to be %v, got %v", expectedCustom, customTimeout)
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
	ctx := context.Background()

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
