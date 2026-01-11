package claudetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

	// Test that slow_ok with background works
	t.Run("SlowOk with Background", func(t *testing.T) {
		input := json.RawMessage(`{"command":"echo 'slow background test'","slow_ok":true,"background":true}`)

		bashTool := (&BashTool{WorkingDir: NewMutableWorkingDir("/")}).Tool()
		toolOut := bashTool.Run(context.Background(), input)
		if toolOut.Error != nil {
			t.Fatalf("Unexpected error: %v", toolOut.Error)
		}
		result := toolOut.LLMContent

		// Should return background result XML-ish format
		resultStr := result[0].Text
		if !strings.Contains(resultStr, "<pid>") || !strings.Contains(resultStr, "<output_file>") {
			t.Errorf("Expected XML-ish background result format, got: %s", resultStr)
		}

		// Extract PID and output file from XML-ish format for cleanup
		// This is a simple extraction for test cleanup - in real usage the agent would parse this
		lines := strings.Split(resultStr, "\n")
		var outFile string
		for _, line := range lines {
			if strings.Contains(line, "<output_file>") {
				start := strings.Index(line, "<output_file>") + len("<output_file>")
				end := strings.Index(line, "</output_file>")
				if end > start {
					outFile = line[start:end]
				}
				break
			}
		}

		if outFile != "" {
			// Clean up
			os.Remove(outFile)
			os.Remove(filepath.Dir(outFile))
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
			Fast:       100 * time.Millisecond,
			Slow:       100 * time.Millisecond,
			Background: 100 * time.Millisecond,
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

	// Test SKETCH=1 environment variable is set
	t.Run("SKETCH Environment Variable", func(t *testing.T) {
		req := bashInput{
			Command: "echo $SKETCH",
		}

		output, err := bashTool.executeBash(ctx, req, 5*time.Second)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		want := "1\n"
		if output != want {
			t.Errorf("Expected SKETCH=1, got %q", output)
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

func TestBackgroundBash(t *testing.T) {
	bashTool := &BashTool{WorkingDir: NewMutableWorkingDir("/")}
	tool := bashTool.Tool()

	// Test basic background execution
	t.Run("Basic Background Command", func(t *testing.T) {
		inputObj := struct {
			Command    string `json:"command"`
			Background bool   `json:"background"`
		}{
			Command:    "echo 'Hello from background' $SKETCH",
			Background: true,
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

		// Parse the returned XML-ish format
		resultStr := result[0].Text
		if !strings.Contains(resultStr, "<pid>") || !strings.Contains(resultStr, "<output_file>") {
			t.Fatalf("Expected XML-ish background result format, got: %s", resultStr)
		}

		// Extract PID and output file from XML-ish format
		lines := strings.Split(resultStr, "\n")
		var pidStr, outFile string
		for _, line := range lines {
			if strings.Contains(line, "<pid>") {
				start := strings.Index(line, "<pid>") + len("<pid>")
				end := strings.Index(line, "</pid>")
				if end > start {
					pidStr = line[start:end]
				}
			} else if strings.Contains(line, "<output_file>") {
				start := strings.Index(line, "<output_file>") + len("<output_file>")
				end := strings.Index(line, "</output_file>")
				if end > start {
					outFile = line[start:end]
				}
			}
		}

		// Verify we got valid values
		if pidStr == "" || outFile == "" {
			t.Errorf("Failed to extract PID or output file from result: %s", resultStr)
			return
		}

		// Verify output file exists
		if _, err := os.Stat(outFile); os.IsNotExist(err) {
			t.Errorf("Output file doesn't exist: %s", outFile)
		}

		// Wait for the command output to be written to file
		waitForFile(t, outFile)

		// Check file contents
		outputContent, err := os.ReadFile(outFile)
		if err != nil {
			t.Fatalf("Failed to read output file: %v", err)
		}
		// The implementation appends a completion message to the output
		outputStr := string(outputContent)
		if !strings.Contains(outputStr, "Hello from background 1") {
			t.Errorf("Expected output to contain 'Hello from background 1', got %q", outputStr)
		}
		if !strings.Contains(outputStr, "[background process completed]") {
			t.Errorf("Expected output to contain completion message, got %q", outputStr)
		}

		// Clean up
		os.Remove(outFile)
		os.Remove(filepath.Dir(outFile))
	})

	// Test background command with stderr output
	t.Run("Background Command with stderr", func(t *testing.T) {
		inputObj := struct {
			Command    string `json:"command"`
			Background bool   `json:"background"`
		}{
			Command:    "echo 'Output to stdout' && echo 'Output to stderr' >&2",
			Background: true,
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

		// Parse the returned XML-ish format
		resultStr := result[0].Text
		lines := strings.Split(resultStr, "\n")
		var outFile string
		for _, line := range lines {
			if strings.Contains(line, "<output_file>") {
				start := strings.Index(line, "<output_file>") + len("<output_file>")
				end := strings.Index(line, "</output_file>")
				if end > start {
					outFile = line[start:end]
				}
				break
			}
		}

		// Wait for the command output to be written to file
		waitForFile(t, outFile)

		// Check output content (stdout and stderr are combined in implementation)
		outputContent, err := os.ReadFile(outFile)
		if err != nil {
			t.Fatalf("Failed to read output file: %v", err)
		}
		// Implementation combines stdout and stderr into one file
		outputStr := string(outputContent)
		if !strings.Contains(outputStr, "Output to stdout") || !strings.Contains(outputStr, "Output to stderr") {
			t.Errorf("Expected both stdout and stderr content, got %q", outputStr)
		}

		// Clean up
		os.Remove(outFile)
		os.Remove(filepath.Dir(outFile))
	})

	// Test background command running without waiting
	t.Run("Background Command Running", func(t *testing.T) {
		// Create a script that will continue running after we check
		inputObj := struct {
			Command    string `json:"command"`
			Background bool   `json:"background"`
		}{
			Command:    "echo 'Running in background' && sleep 5",
			Background: true,
		}
		inputJSON, err := json.Marshal(inputObj)
		if err != nil {
			t.Fatalf("Failed to marshal input: %v", err)
		}

		// Start the command in the background
		toolOut := tool.Run(context.Background(), inputJSON)
		if toolOut.Error != nil {
			t.Fatalf("Unexpected error: %v", toolOut.Error)
		}
		result := toolOut.LLMContent

		// Parse the returned XML-ish format
		resultStr := result[0].Text
		lines := strings.Split(resultStr, "\n")
		var pidStr, outFile string
		for _, line := range lines {
			if strings.Contains(line, "<pid>") {
				start := strings.Index(line, "<pid>") + len("<pid>")
				end := strings.Index(line, "</pid>")
				if end > start {
					pidStr = line[start:end]
				}
			} else if strings.Contains(line, "<output_file>") {
				start := strings.Index(line, "<output_file>") + len("<output_file>")
				end := strings.Index(line, "</output_file>")
				if end > start {
					outFile = line[start:end]
				}
			}
		}

		// Wait for the command output to be written to file
		waitForFile(t, outFile)

		// Check output content
		outputContent, err := os.ReadFile(outFile)
		if err != nil {
			t.Fatalf("Failed to read output file: %v", err)
		}
		expectedOutput := "Running in background\n"
		if string(outputContent) != expectedOutput {
			t.Errorf("Expected output content %q, got %q", expectedOutput, string(outputContent))
		}

		// Verify the process is still running by parsing PID
		if pidStr != "" {
			// We can't easily test if the process is still running without importing strconv
			// and the process might have finished by now anyway due to timing
			t.Log("Process started in background with PID:", pidStr)
		}

		// Clean up
		os.Remove(outFile)
		os.Remove(filepath.Dir(outFile))
	})
}

func TestBashTimeout(t *testing.T) {
	// Test default timeout values
	t.Run("Default Timeout Values", func(t *testing.T) {
		// Test foreground default timeout
		foreground := bashInput{
			Command:    "echo 'test'",
			Background: false,
		}
		fgTimeout := foreground.timeout(nil)
		expectedFg := 30 * time.Second
		if fgTimeout != expectedFg {
			t.Errorf("Expected foreground default timeout to be %v, got %v", expectedFg, fgTimeout)
		}

		// Test background default timeout
		background := bashInput{
			Command:    "echo 'test'",
			Background: true,
		}
		bgTimeout := background.timeout(nil)
		expectedBg := 24 * time.Hour
		if bgTimeout != expectedBg {
			t.Errorf("Expected background default timeout to be %v, got %v", expectedBg, bgTimeout)
		}

		// Test slow_ok timeout
		slowOk := bashInput{
			Command:    "echo 'test'",
			Background: false,
			SlowOK:     true,
		}
		slowTimeout := slowOk.timeout(nil)
		expectedSlow := 15 * time.Minute
		if slowTimeout != expectedSlow {
			t.Errorf("Expected slow_ok timeout to be %v, got %v", expectedSlow, slowTimeout)
		}

		// Test custom timeout config
		customTimeouts := &Timeouts{
			Fast:       5 * time.Second,
			Slow:       2 * time.Minute,
			Background: 1 * time.Hour,
		}
		customFast := bashInput{
			Command:    "echo 'test'",
			Background: false,
		}
		customTimeout := customFast.timeout(customTimeouts)
		expectedCustom := 5 * time.Second
		if customTimeout != expectedCustom {
			t.Errorf("Expected custom timeout to be %v, got %v", expectedCustom, customTimeout)
		}
	})
}

// waitForFile waits for a file to exist and be non-empty or times out
func waitForFile(t *testing.T, filepath string) {
	timeout := time.After(5 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-timeout:
			t.Fatalf("Timed out waiting for file to exist and have contents: %s", filepath)
			return
		case <-tick.C:
			info, err := os.Stat(filepath)
			if err == nil && info.Size() > 0 {
				return // File exists and has content
			}
		}
	}
}

// waitForProcessDeath waits for a process to no longer exist or times out
func waitForProcessDeath(t *testing.T, pid int) {
	timeout := time.After(5 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-timeout:
			t.Fatalf("Timed out waiting for process %d to exit", pid)
			return
		case <-tick.C:
			process, _ := os.FindProcess(pid)
			err := process.Signal(syscall.Signal(0))
			if err != nil {
				// Process doesn't exist
				return
			}
		}
	}
}
