package claudetool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"shelley.exe.dev/llm"
)

// CLISubagentTool delegates work to an external CLI agent (claude or codex).
type CLISubagentTool struct {
	CLIAgent   string // "claude-cli" or "codex-cli"
	WorkingDir *MutableWorkingDir
}

const (
	cliSubagentDefaultTimeout = 5 * time.Minute
	cliSubagentMaxTimeout     = 30 * time.Minute
)

type cliSubagentInput struct {
	Slug           string `json:"slug"`
	Prompt         string `json:"prompt"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

func (t *CLISubagentTool) cliAgentDisplayName() string {
	switch t.CLIAgent {
	case "claude-cli":
		return "Claude CLI"
	case "codex-cli":
		return "Codex CLI"
	default:
		return t.CLIAgent
	}
}

func (t *CLISubagentTool) description() string {
	agentName := t.cliAgentDisplayName()
	return fmt.Sprintf(`Delegate a task to a %s subagent.

The subagent is an independent %s process that runs in the conversation's
working directory. It has full access to the filesystem and can run commands.
Use subagents for:
- Long-running tasks that you want to delegate
- Parallel exploration of different approaches
- Breaking down complex problems into independent pieces

The subagent has NO context beyond what you put in the prompt, so share
the "why" alongside the "what". Convey intent, nuance, and operational
details — not just prescriptive instructions.

The tool returns the subagent's stdout output when it completes.
The slug parameter is for display/identification purposes only.`, agentName, agentName)
}

func (t *CLISubagentTool) inputSchema() string {
	return `{
  "type": "object",
  "required": ["slug", "prompt"],
  "properties": {
    "slug": {
      "type": "string",
      "description": "A short identifier for this subagent task (e.g., 'research-api', 'test-runner')"
    },
    "prompt": {
      "type": "string",
      "description": "The prompt to send to the CLI agent"
    },
    "timeout_seconds": {
      "type": "integer",
      "description": "How long to wait for the agent to finish (default: 300, max: 1800)"
    }
  }
}`
}

// Tool returns an llm.Tool for the CLI subagent functionality.
func (t *CLISubagentTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        subagentName, // same name as native subagent — replaces it
		Description: t.description(),
		InputSchema: llm.MustSchema(t.inputSchema()),
		Run:         t.Run,
	}
}

func (t *CLISubagentTool) Run(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var req cliSubagentInput
	if err := json.Unmarshal(m, &req); err != nil {
		return llm.ErrorfToolOut("failed to parse CLI subagent input: %w", err)
	}

	// Validate inputs
	if req.Slug == "" {
		return llm.ErrorfToolOut("slug is required")
	}
	req.Slug = sanitizeSlug(req.Slug)
	if req.Slug == "" {
		return llm.ErrorfToolOut("slug must contain alphanumeric characters")
	}

	if req.Prompt == "" {
		return llm.ErrorfToolOut("prompt is required")
	}

	// Determine timeout
	timeout := cliSubagentDefaultTimeout
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
		if timeout > cliSubagentMaxTimeout {
			timeout = cliSubagentMaxTimeout
		}
	}

	// Build command based on CLI agent type
	var cmdName string
	var cmdArgs []string
	switch t.CLIAgent {
	case "claude-cli":
		cmdName = "claude"
		cmdArgs = []string{"-p", "--dangerously-skip-permissions", "--output-format", "text", "--", req.Prompt}
	case "codex-cli":
		cmdName = "codex"
		cmdArgs = []string{"exec", "--full-auto", req.Prompt}
	default:
		return llm.ErrorfToolOut("unsupported CLI agent: %s", t.CLIAgent)
	}

	// Create context with timeout
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, cmdName, cmdArgs...)
	cmd.Dir = t.WorkingDir.Get()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	slog.Info("Running CLI subagent",
		"agent", t.CLIAgent,
		"slug", req.Slug,
		"command", cmdName,
		"args", cmdArgs,
		"working_dir", cmd.Dir,
		"timeout", timeout,
	)

	err := cmd.Run()

	// Log stderr if any
	if stderr.Len() > 0 {
		slog.Warn("CLI subagent stderr",
			"agent", t.CLIAgent,
			"slug", req.Slug,
			"stderr", stderr.String(),
		)
	}

	// Handle errors
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			// Timeout — return partial output as error result
			partial := stdout.String()
			if partial == "" {
				partial = "(no output)"
			}
			return llm.ToolOut{
				LLMContent: llm.TextContent(fmt.Sprintf("Subagent '%s' timed out after %s.\nPartial output:\n%s", req.Slug, timeout, partial)),
				Display: CLISubagentDisplayData{
					Slug:     req.Slug,
					CLIAgent: t.CLIAgent,
					Status:   "timeout",
				},
			}
		}

		if cmdCtx.Err() == context.Canceled {
			return llm.ToolOut{
				LLMContent: llm.TextContent(fmt.Sprintf("Subagent '%s' was cancelled.", req.Slug)),
				Display: CLISubagentDisplayData{
					Slug:     req.Slug,
					CLIAgent: t.CLIAgent,
					Status:   "cancelled",
				},
			}
		}

		// Non-zero exit code — return stdout + error info as the result (not a tool failure)
		output := stdout.String()
		if output == "" {
			output = "(no output)"
		}
		errMsg := err.Error()
		if stderr.Len() > 0 {
			errMsg += "\nstderr: " + stderr.String()
		}
		return llm.ToolOut{
			LLMContent: llm.TextContent(fmt.Sprintf("Subagent '%s' exited with error: %s\nOutput:\n%s", req.Slug, errMsg, output)),
			Display: CLISubagentDisplayData{
				Slug:     req.Slug,
				CLIAgent: t.CLIAgent,
				Status:   "error",
			},
		}
	}

	// Success
	result := stdout.String()
	if result == "" {
		result = "(no output)"
	}

	return llm.ToolOut{
		LLMContent: llm.TextContent(fmt.Sprintf("Subagent '%s' completed successfully:\n%s", req.Slug, result)),
		Display: CLISubagentDisplayData{
			Slug:     req.Slug,
			CLIAgent: t.CLIAgent,
			Status:   "completed",
		},
	}
}

// CLISubagentDisplayData is the display data sent to the UI for CLI subagent tool results.
type CLISubagentDisplayData struct {
	Slug     string `json:"slug"`
	CLIAgent string `json:"cli_agent"`
	Status   string `json:"status"` // "completed", "error", "timeout", "cancelled"
}
