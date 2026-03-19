package claudetool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCLISubagentTool_Tool(t *testing.T) {
	tool := &CLISubagentTool{
		CLIAgent:   "claude-cli",
		WorkingDir: NewMutableWorkingDir("/tmp"),
	}

	llmTool := tool.Tool()

	// Verify tool name matches the native subagent
	if llmTool.Name != subagentName {
		t.Errorf("expected tool name %q, got %q", subagentName, llmTool.Name)
	}

	// Verify description mentions Claude CLI
	if !strings.Contains(llmTool.Description, "Claude CLI") {
		t.Error("expected description to mention Claude CLI")
	}

	// Verify input schema has required fields
	schemaJSON, _ := json.Marshal(llmTool.InputSchema)
	schemaStr := string(schemaJSON)
	if !strings.Contains(schemaStr, "slug") {
		t.Error("expected schema to contain 'slug'")
	}
	if !strings.Contains(schemaStr, "prompt") {
		t.Error("expected schema to contain 'prompt'")
	}
	if !strings.Contains(schemaStr, "timeout_seconds") {
		t.Error("expected schema to contain 'timeout_seconds'")
	}
}

func TestCLISubagentTool_CodexDescription(t *testing.T) {
	tool := &CLISubagentTool{
		CLIAgent:   "codex-cli",
		WorkingDir: NewMutableWorkingDir("/tmp"),
	}

	llmTool := tool.Tool()
	if !strings.Contains(llmTool.Description, "Codex CLI") {
		t.Error("expected description to mention Codex CLI")
	}
}

func TestCLISubagentTool_Validation(t *testing.T) {
	tool := &CLISubagentTool{
		CLIAgent:   "claude-cli",
		WorkingDir: NewMutableWorkingDir("/tmp"),
	}

	// Test empty slug
	t.Run("empty slug", func(t *testing.T) {
		input := cliSubagentInput{Slug: "", Prompt: "test"}
		inputJSON, _ := json.Marshal(input)
		result := tool.Run(context.Background(), inputJSON)
		if result.Error == nil {
			t.Error("expected error for empty slug")
		}
	})

	// Test empty prompt
	t.Run("empty prompt", func(t *testing.T) {
		input := cliSubagentInput{Slug: "test", Prompt: ""}
		inputJSON, _ := json.Marshal(input)
		result := tool.Run(context.Background(), inputJSON)
		if result.Error == nil {
			t.Error("expected error for empty prompt")
		}
	})

	// Test invalid slug (only special chars)
	t.Run("invalid slug", func(t *testing.T) {
		input := cliSubagentInput{Slug: "@#$%", Prompt: "test"}
		inputJSON, _ := json.Marshal(input)
		result := tool.Run(context.Background(), inputJSON)
		if result.Error == nil {
			t.Error("expected error for invalid slug")
		}
	})
}

func TestCLISubagentTool_UnsupportedAgent(t *testing.T) {
	tool := &CLISubagentTool{
		CLIAgent:   "unknown-agent",
		WorkingDir: NewMutableWorkingDir("/tmp"),
	}

	input := cliSubagentInput{Slug: "test", Prompt: "do something"}
	inputJSON, _ := json.Marshal(input)
	result := tool.Run(context.Background(), inputJSON)
	if result.Error == nil {
		t.Error("expected error for unsupported agent")
	}
	if !strings.Contains(result.Error.Error(), "unsupported CLI agent") {
		t.Errorf("expected 'unsupported CLI agent' error, got: %v", result.Error)
	}
}

func TestCLISubagentTool_RunEcho(t *testing.T) {
	// Test with a real command (echo) to verify the process execution works.
	// We use claude-cli agent type but the binary won't be found;
	// instead, test that the error handling works for missing binaries.
	tool := &CLISubagentTool{
		CLIAgent:   "claude-cli",
		WorkingDir: NewMutableWorkingDir("/tmp"),
	}

	input := cliSubagentInput{Slug: "test-echo", Prompt: "hello world", TimeoutSeconds: 5}
	inputJSON, _ := json.Marshal(input)
	result := tool.Run(context.Background(), inputJSON)

	// If claude is not installed, we should get an error result (not a tool failure)
	// because we handle non-zero exit codes gracefully
	if result.Error != nil {
		// Error from exec (e.g. binary not found) - this is OK for the test
		// The tool should still return an error result, not panic
		t.Logf("Got expected error (claude not in PATH): %v", result.Error)
	} else {
		// claude is actually installed - we should get output
		if len(result.LLMContent) == 0 {
			t.Error("expected LLM content")
		}
		t.Logf("Got result: %s", result.LLMContent[0].Text)
	}

	// Verify display data is present
	if result.Display != nil {
		display, ok := result.Display.(CLISubagentDisplayData)
		if !ok {
			t.Errorf("expected CLISubagentDisplayData, got %T", result.Display)
		} else {
			if display.Slug != "test-echo" {
				t.Errorf("expected slug 'test-echo', got %q", display.Slug)
			}
			if display.CLIAgent != "claude-cli" {
				t.Errorf("expected cli_agent 'claude-cli', got %q", display.CLIAgent)
			}
		}
	}
}
