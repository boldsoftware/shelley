package lazycue

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// AgentMode specifies whether the agent should generate new steps or fix existing ones.
type AgentMode int

const (
	AgentModeGenerate AgentMode = iota
	AgentModeFix
)

// AgentConfig configures an agent run.
type AgentConfig struct {
	Mode             AgentMode
	Description      string
	PreviousSteps    []byte // JSON of previous steps (fix mode)
	PreviousError    string // Error from previous run (fix mode)
	CacheFilePath    string // Path to the cached steps JSON for this test (fix mode); lets the agent inspect its git history for prior flakiness
	Browser          *Browser
	BaseURL          string
	Model            string
	AnthropicBaseURL string
	AnthropicAPIKey  string
	RepoRoot         string
	Verbose          bool
}

// AgentResult is the result of an agent run.
type AgentResult struct {
	Success        bool
	Error          string
	StepsJSON      []byte
	StepResults    []StepResult
	ScreenshotPath string
	InputTokens    int
	OutputTokens   int
	// Effort diagnostics, surfaced so a human can see how hard the agent
	// worked (e.g. did a heal converge in 1-2 turns, or thrash to the limit?).
	Turns       int  // number of API round trips the agent took
	HitMaxTurns bool // true if the agent ran out of turns (maxAgentTurns)
	ToolCalls   int  // total tool_use calls the agent issued (run_steps/screenshot/git_command)
}

const maxAgentTurns = 25

// agentBudget bounds the total wall-clock time a single heal/generate agent run
// may consume. It is kept well under the `go test` package timeout (10m by
// default) so that a slow or stuck heal fails its one test gracefully (via a
// context-deadline error that surfaces as t.Fatal) instead of running until the
// package deadline and panicking with "test timed out", which aborts every
// other test in the package too.
const agentBudget = 5 * time.Minute

// anthropicCallTimeout bounds a single LLM HTTP request. http.DefaultClient has
// no timeout, so without this a hung request could block a heal indefinitely
// (until agentBudget, or formerly the package deadline).
const anthropicCallTimeout = 90 * time.Second

// anthropicMaxAttempts is the number of times callAnthropic will try a single
// LLM request before giving up. Transient failures (network errors, per-request
// timeouts, 429s, and 5xx responses) are common against the live Anthropic API
// in CI and would otherwise fail an entire heal — and thus a queue build — on a
// single hiccup. Retries are bounded by the surrounding agentBudget context.
const anthropicMaxAttempts = 3

// anthropicRetryBackoff is the base delay between retry attempts; it grows
// linearly with the attempt number (attempt*base) and is capped well under
// agentBudget so a retry chain can never exhaust it on sleeping alone. It is a
// var (not a const) so tests can shrink it to avoid real sleeps.
var anthropicRetryBackoff = 2 * time.Second

// --- Anthropic API types ---

type apiRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system"`
	Messages  []apiMessage `json:"messages"`
	Tools     []apiTool    `json:"tools"`
}

type apiMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []apiContentBlock
}

type apiContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   interface{}     `json:"content,omitempty"` // string or []apiContentBlock for tool_result
	Source    *apiImageSource `json:"source,omitempty"`
}

type apiImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type apiTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type apiUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type apiResponse struct {
	ID         string            `json:"id"`
	Content    []apiContentBlock `json:"content"`
	StopReason string            `json:"stop_reason"`
	Error      *apiError         `json:"error,omitempty"`
	Usage      apiUsage          `json:"usage"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// --- Tool input types ---

type runStepsInput struct {
	Steps json.RawMessage `json:"steps"`
	// Final marks this as the complete test to save (not an exploratory probe).
	// When true and all steps pass, the harness caches exactly these steps.
	Final bool `json:"final,omitempty"`
}

type screenshotInput struct{}

type gitCommandInput struct {
	Command string `json:"command"`
}

// --- Agent implementation ---

func buildSystemPrompt() string {
	return `You are a browser test-writing agent. You write DSL test steps (JSON arrays) to test a web application.

Available DSL actions:
- navigate: {"action": "navigate", "url": "/path"} - Navigate to URL (relative to base)
- wait_visible: {"action": "wait_visible", "selector": "...", "timeout": "10s"} - Wait for element to be visible
- wait_hidden: {"action": "wait_hidden", "selector": "...", "timeout": "10s"} - Wait for element to be hidden
- wait_text: {"action": "wait_text", "text": "...", "timeout": "30s"} - Wait for text in page body
- wait_text_gone: {"action": "wait_text_gone", "text": "...", "timeout": "10s"} - Wait for text to disappear
- fill: {"action": "fill", "selector": "...", "value": "..."} - Fill input/textarea (React compatible)
- click: {"action": "click", "selector": "..."} - Click element
- press_key: {"action": "press_key", "key": "Enter", "modifiers": "Shift"} - Press one key on the focused element. "key" is a KeyboardEvent.key name (Enter, Tab, Escape, Backspace, Delete, ArrowUp/Down/Left/Right, Home, End, PageUp, PageDown, F1-F12, Insert), "Space", or a single character. Optional modifiers: Shift, Control, Alt, Meta (combine with "+"). Focus the target first (e.g. click it); fill does not focus.
- screenshot: {"action": "screenshot"} - Take screenshot
- eval: {"action": "eval", "expression": "...", "expect": "..."} - Evaluate JS; if "expect" is set, assert the stringified result equals it. The run_steps result echoes the evaluated value back to you ("=> <value>"), so use eval WITHOUT expect to probe page state (selectors, scrollHeight, classes, etc.) while developing the test.
- assert_visible: {"action": "assert_visible", "selector": "..."} - Assert element is visible
- assert_not_visible: {"action": "assert_not_visible", "selector": "..."} - Assert element is not visible
- assert_text: {"action": "assert_text", "selector": "...", "text": "..."} - Assert exact text content
- assert_text_contains: {"action": "assert_text_contains", "selector": "...", "text": "..."} - Assert text contains substring
- assert_attribute: {"action": "assert_attribute", "selector": "...", "attribute": "...", "value": "..."}
- wait_url: {"action": "wait_url", "text": "...", "timeout": "10s"} (substring) or {"action": "wait_url", "value": "..."} (exact) - Wait for the browser URL to match. Use this (not assert_url) when a click triggers an async SPA route change, e.g. /new -> /c/<slug>.
- assert_url: {"action": "assert_url", "value": "..."} or {"action": "assert_url", "text": "..."} for contains - Asserts the URL immediately (no waiting).
- assert_title: {"action": "assert_title", "text": "..."}
- assert_count: {"action": "assert_count", "selector": "...", "count": 3}
- sleep: {"action": "sleep", "timeout": "1s"}

WORKFLOW:
1. Start by navigating to the appropriate page.
2. Use wait_visible and wait_text with appropriate timeouts before asserting.
3. Use the run_steps tool to test your DSL steps. Review the results and fix any failures.
4. Use screenshot to see what the page looks like if you're unsure about selectors or page state.
5. Use git_command to explore the codebase (grep for selectors, data-testid attributes, etc.) when you need to discover the app's structure.
6. Your FINAL call to run_steps must be the COMPLETE, passing test that exercises every part of the description. Submit it by setting "final": true on that run_steps call (exploratory probes must NOT set final). The harness caches exactly the steps from your final submission, so it must contain all the assertions — never submit a bare navigation/probe as final.
7. Minimize the number of tool calls. Aim for 1-3 run_steps calls total.

CRITICAL: WHEN FIXING A FAILING TEST:
- The test DESCRIPTION is the source of truth for what the application SHOULD do.
- If the description says "the title should be X" and the page shows "Y", that means the APPLICATION IS BROKEN, not the test.
- Only fix MECHANICAL issues: wrong selectors, missing waits, timing issues, wrong CSS selectors.
- NEVER change what the test asserts to match broken application behavior.
- If the application doesn't match the description, the test SHOULD fail. Report it as a genuine failure.
- If you determine the app is genuinely broken (not matching the description), output an empty steps array and explain the failure.

DISTINGUISHING FLAKES FROM GENUINE FAILURES:
Many heal requests are triggered not by a broken app but by a LOAD-INDUCED FLAKE:
under heavy CI parallelism the box is so starved that an element appears later
than a wait_* timeout allows, even though the same steps pass quickly when the
machine is idle. Treat an error as a likely flake when it is timeout-shaped
(e.g. "wait_visible/wait_text timed out", an element that intermittently doesn't
appear, an assert that fails only on a transient/empty state) AND the steps
themselves look correct for the description.
- A timeout-shaped error is NOT automatically a flake. The real discriminator is
  "appears LATE" vs "never appears / appears WRONG". Before deciding it's a flake
  you MUST confirm the app actually does the right thing now: navigate and use
  screenshot / eval (without expect) to observe the live page, and verify the
  awaited element/text is genuinely present and CORRECT per the description, just
  slow. If it is absent or shows the wrong content even after waiting, that is a
  GENUINE FAILURE (a real regression often looks timeout-shaped because the
  element never appears) — do not paper over it by raising timeouts.
- INVESTIGATE BEFORE EDITING. Use git_command to read the cached test's history:
  'git log --oneline -- <cache-file>' and 'git log -p -- <cache-file>'. Repeated
  past edits that only bumped timeouts or swapped a sleep for a wait are a strong
  signal this test flakes under load.
- Once you have CONFIRMED the behavior is correct-but-slow, the right fix is to
  make the SAME steps MORE ROBUST, never to weaken assertions: raise wait_*
  timeouts generously (e.g. 30s -> 60s), replace bare sleeps or implicit waits
  with explicit wait_visible/wait_text on the element you are about to assert,
  and wait for the precise post-condition (specific text/element/url) rather
  than a coarse one. Keep every assertion in the description intact.
- Conclude "genuine app failure" (empty steps array, with an explanation)
  whenever the live page does not match the description — including when an
  element never appears or shows wrong content despite waiting. When in doubt
  between flake and regression, prefer reporting the failure over masking it.`
}

func buildGenerateUserPrompt(description string) string {
	return fmt.Sprintf(`Write DSL test steps for this behavior:

%s

Write the complete test as a single run_steps call. If the description mentions specific selectors or page structure you're unsure about, use screenshot or git_command to discover them first.`, description)
}

func buildFixUserPrompt(description string, previousSteps []byte, previousError, cacheFilePath string) string {
	historyHint := ""
	if cacheFilePath != "" {
		historyHint = fmt.Sprintf(`

This test's cached steps live at: %s
Before editing, check whether this test has a history of flaking under load:
  git log --oneline -- %s
  git log -p -- %s
A history of timeout-only bumps is strong evidence the failure is a load flake,
not a broken app.`, cacheFilePath, cacheFilePath, cacheFilePath)
	}
	return fmt.Sprintf(`A previously generated DSL test is failing. Fix it.

Test description: %s

Previous DSL steps:
%s

Error:
%s

INSTRUCTIONS:
- The test DESCRIPTION is the source of truth for what the app SHOULD do.
- First decide: is this a LOAD-INDUCED FLAKE or a GENUINE app failure?
  A timeout-shaped error (wait_* timed out, an element that intermittently
  doesn't appear) can be either: "appears late" (flake) or "never appears /
  appears wrong" (real regression). Observe the LIVE page (navigate + screenshot
  / eval) and confirm the awaited element/text is actually present and correct
  per the description before calling it a flake.%s
- For a flake: keep every assertion, and make the same steps more robust —
  raise wait_* timeouts generously and wait explicitly on the precise element
  you are about to assert. Do NOT weaken assertions.
- For a mechanical bug: fix wrong selectors / missing waits only.
- NEVER change assertions to match broken application behavior.
- If the app genuinely doesn't match the description (e.g., wrong title, missing
  text, and robustness changes can't fix it), return an empty steps array [] and
  explain the failure.
- Use screenshots to verify the current state, then decide.`, description, string(previousSteps), previousError, historyHint)
}

func buildTools() []apiTool {
	return []apiTool{
		{
			Name:        "run_steps",
			Description: "Execute an array of DSL test steps against the browser. Returns structured results showing which step passed/failed and why. The browser is reset to a clean state before execution. Use this to test your generated DSL. When you have the COMPLETE test that exercises everything in the description and it passes, call run_steps one last time with \"final\": true to submit it for caching.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"steps": {
						"type": "array",
						"description": "Array of DSL step objects to execute",
						"items": { "type": "object" }
					},
					"final": {
						"type": "boolean",
						"description": "Set true only when these steps are the complete, final test to save. Do not set on exploratory probes."
					}
				},
				"required": ["steps"]
			}`),
		},
		{
			Name:        "screenshot",
			Description: "Take a screenshot of the current page state. Returns the screenshot as a base64-encoded PNG image. Use this to see what the page looks like.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {},
				"required": []
			}`),
		},
		{
			Name:        "git_command",
			Description: "Run a read-only git command in the repository root. Use for: git grep, git ls-files, git show, git log, git diff. Helps you understand the codebase to write better tests.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {
						"type": "string",
						"description": "The git command to run (e.g., 'git grep data-testid', 'git ls-files src/')"
					}
				},
				"required": ["command"]
			}`),
		},
	}
}

// RunAgent executes the LLM agent loop to generate or fix DSL test steps.
func RunAgent(ctx context.Context, cfg *AgentConfig) (*AgentResult, error) {
	logf := func(format string, args ...any) {
		if cfg.Verbose {
			log.Printf("[agent] "+format, args...)
		}
	}

	// Bound the whole agent run so a stuck heal can't run until the package
	// test deadline and panic (taking the rest of the package down with it).
	ctx, cancel := context.WithTimeout(ctx, agentBudget)
	defer cancel()

	systemPrompt := buildSystemPrompt()
	var userPrompt string
	if cfg.Mode == AgentModeFix {
		userPrompt = buildFixUserPrompt(cfg.Description, cfg.PreviousSteps, cfg.PreviousError, cfg.CacheFilePath)
	} else {
		userPrompt = buildGenerateUserPrompt(cfg.Description)
	}

	messages := []apiMessage{
		{Role: "user", Content: userPrompt},
	}

	tools := buildTools()

	var lastStepsJSON []byte
	var lastStepResults []StepResult
	// finalStepsJSON / finalStepResults hold the run_steps call the agent
	// explicitly marked as its complete test ("// FINAL"). Exploratory probes
	// must not be cached, so we only accept these as the saved test.
	var finalStepsJSON []byte
	var finalStepResults []StepResult
	var nudgedForFinal bool
	var lastError string
	var genuineFailure bool // set when agent determines the APP is broken, not the test
	var totalInputTokens, totalOutputTokens int
	// Effort accounting for heal diagnostics: how many API round trips and tool
	// calls the agent took, and whether it exhausted its turn budget.
	var turnsTaken, toolCallCount int
	finish := func(r *AgentResult) (*AgentResult, error) {
		r.InputTokens = totalInputTokens
		r.OutputTokens = totalOutputTokens
		r.Turns = turnsTaken
		r.ToolCalls = toolCallCount
		r.HitMaxTurns = turnsTaken >= maxAgentTurns
		return r, nil
	}

	for turn := 0; turn < maxAgentTurns; turn++ {
		turnsTaken = turn + 1
		logf("turn %d", turn)

		resp, err := callAnthropic(ctx, cfg, systemPrompt, messages, tools)
		if err != nil {
			return nil, fmt.Errorf("anthropic API call: %w", err)
		}

		if resp.Error != nil {
			return nil, fmt.Errorf("anthropic error: %s: %s", resp.Error.Type, resp.Error.Message)
		}

		totalInputTokens += resp.Usage.InputTokens
		totalOutputTokens += resp.Usage.OutputTokens

		// Check for tool_use blocks. Track whether this assistant turn declared
		// its run_steps as the FINAL, complete test (vs. an exploratory probe).
		var toolUses []apiContentBlock
		var turnIsFinal bool
		for _, block := range resp.Content {
			if block.Type == "tool_use" {
				toolUses = append(toolUses, block)
			}
			if block.Type == "text" {
				logf("assistant: %s", truncate(block.Text, 200))
				if strings.Contains(block.Text, "// FINAL") {
					turnIsFinal = true
				}
			}
		}

		// Check text blocks for genuine failure signals.
		for _, block := range resp.Content {
			if block.Type == "text" && isGenuineFailureSignal(block.Text) {
				logf("agent detected genuine application failure")
				genuineFailure = true
				if lastError == "" {
					lastError = block.Text
				}
			}
		}

		if len(toolUses) == 0 {
			// No tool calls — agent is done.
			if genuineFailure {
				errMsg := lastError
				if errMsg == "" {
					errMsg = "agent determined the application is broken"
				}
				return finish(&AgentResult{
					Success:     false,
					Error:       errMsg,
					StepsJSON:   lastStepsJSON,
					StepResults: lastStepResults,
				})
			}
			// Only accept a run_steps call the agent explicitly marked "// FINAL"
			// as the saved test. This prevents caching an exploratory probe that
			// happened to be the agent's last run_steps but doesn't actually test
			// the described behavior.
			if finalStepsJSON != nil && allPassed(finalStepResults) {
				return finish(&AgentResult{
					Success:     true,
					StepsJSON:   finalStepsJSON,
					StepResults: finalStepResults,
				})
			}
			// The agent stopped without a passing FINAL test. Nudge it once to
			// produce one (it may have only run exploratory probes), then fail.
			if !nudgedForFinal {
				nudgedForFinal = true
				messages = append(messages, apiMessage{
					Role: "user",
					Content: []apiContentBlock{{
						Type: "text",
						Text: "You have not yet produced a complete, passing test. Do NOT stop on an exploratory probe. Write the FULL test that exercises everything in the description (including every assertion), output the text \"// FINAL\" on its own, and call run_steps with the complete step list. If you believe the application is genuinely broken, say so explicitly instead.",
					}},
				})
				continue
			}
			errMsg := lastError
			if errMsg == "" {
				for _, block := range resp.Content {
					if block.Type == "text" {
						errMsg = block.Text
						break
					}
				}
			}
			if errMsg == "" {
				errMsg = "agent stopped without producing passing test"
			}
			return finish(&AgentResult{
				Success:     false,
				Error:       errMsg,
				StepsJSON:   lastStepsJSON,
				StepResults: lastStepResults,
			})
		}

		// Append assistant response to messages.
		messages = append(messages, apiMessage{
			Role:    "assistant",
			Content: resp.Content,
		})

		// Process tool calls.
		var toolResults []apiContentBlock
		var finalSubmittedThisTurn bool
		toolCallCount += len(toolUses)
		for _, tu := range toolUses {
			switch tu.Name {
			case "run_steps":
				var input runStepsInput
				if err := json.Unmarshal(tu.Input, &input); err != nil {
					toolResults = append(toolResults, makeToolResult(tu.ID, fmt.Sprintf("Error parsing input: %v", err)))
					continue
				}

				steps, err := ParseSteps(input.Steps)
				if err != nil {
					toolResults = append(toolResults, makeToolResult(tu.ID, fmt.Sprintf("Error parsing steps: %v", err)))
					continue
				}

				logf("tool: run_steps (%d steps)", len(steps))
				for si, s := range steps {
					logf("  [%d] %s", si, StepSummary(s))
				}

				results, execErr := cfg.Browser.ExecuteSteps(ctx, cfg.BaseURL, steps)
				lastStepResults = results
				lastStepsJSON = input.Steps
				// A call is the final test if the agent set the explicit `final`
				// flag on the tool call OR wrote the legacy "// FINAL" marker.
				if input.Final || turnIsFinal {
					finalStepResults = results
					finalStepsJSON = input.Steps
					finalSubmittedThisTurn = true
				}

				// Build result summary.
				var sb strings.Builder
				for i, r := range results {
					status := "PASS"
					if !r.Pass {
						status = "FAIL"
					}
					sb.WriteString(fmt.Sprintf("Step %d [%s] %s (%s)", i, r.Action, status, r.Duration.Round(time.Millisecond)))
					if r.Error != "" {
						sb.WriteString(": " + r.Error)
					}
					if r.Output != "" {
						// Surface eval results so the agent can read the value it
						// probed for. Truncate to keep tool results compact.
						sb.WriteString(" => " + truncateArg(r.Output, 200))
					}
					sb.WriteString("\n")
				}

				if execErr != nil {
					lastError = execErr.Error()
					sb.WriteString("\nOverall: FAILED - " + execErr.Error())
				} else {
					sb.WriteString("\nOverall: ALL STEPS PASSED")
				}

				toolResults = append(toolResults, makeToolResult(tu.ID, sb.String()))

			case "screenshot":
				logf("tool: screenshot")
				png, err := cfg.Browser.Screenshot(ctx)
				if err != nil {
					toolResults = append(toolResults, makeToolResult(tu.ID, fmt.Sprintf("Screenshot failed: %v", err)))
					continue
				}
				b64 := base64.StdEncoding.EncodeToString(png)
				toolResults = append(toolResults, apiContentBlock{
					Type:      "tool_result",
					ToolUseID: tu.ID,
					Content: []apiContentBlock{
						{
							Type: "image",
							Source: &apiImageSource{
								Type:      "base64",
								MediaType: "image/png",
								Data:      b64,
							},
						},
					},
				})

			case "git_command":
				var input gitCommandInput
				if err := json.Unmarshal(tu.Input, &input); err != nil {
					toolResults = append(toolResults, makeToolResult(tu.ID, fmt.Sprintf("Error parsing input: %v", err)))
					continue
				}

				logf("tool: git_command %s", input.Command)
				result := executeGitCommand(cfg.RepoRoot, input.Command)
				toolResults = append(toolResults, makeToolResult(tu.ID, result))

			default:
				logf("tool: %s (unknown)", tu.Name)
				toolResults = append(toolResults, makeToolResult(tu.ID, fmt.Sprintf("Unknown tool: %s", tu.Name)))
			}
		}

		messages = append(messages, apiMessage{
			Role:    "user",
			Content: toolResults,
		})

		// The agent submitted a final test (via the run_steps `final` flag or
		// "// FINAL") this turn and every step passed: accept it immediately
		// without burning another round trip waiting for an end-of-turn message.
		if (turnIsFinal || finalSubmittedThisTurn) && finalStepsJSON != nil && allPassed(finalStepResults) {
			return finish(&AgentResult{
				Success:     true,
				StepsJSON:   finalStepsJSON,
				StepResults: finalStepResults,
			})
		}

		// If genuine failure was detected, stop immediately.
		if genuineFailure {
			errMsg := lastError
			if errMsg == "" {
				errMsg = "agent determined the application is broken"
			}
			return finish(&AgentResult{
				Success:     false,
				Error:       errMsg,
				StepsJSON:   lastStepsJSON,
				StepResults: lastStepResults,
			})
		}

		// If the agent marked a complete (FINAL) test that passed all steps,
		// we're done. Exploratory probes are never accepted as the saved test.
		if finalStepsJSON != nil && allPassed(finalStepResults) {
			if resp.StopReason == "end_turn" {
				return finish(&AgentResult{
					Success:     true,
					StepsJSON:   finalStepsJSON,
					StepResults: finalStepResults,
				})
			}
			// Continue to let agent confirm/finalize.
		}
	}

	// Exhausted turns: only accept a passing FINAL test.
	if finalStepsJSON != nil && allPassed(finalStepResults) {
		return finish(&AgentResult{
			Success:     true,
			StepsJSON:   finalStepsJSON,
			StepResults: finalStepResults,
		})
	}

	return finish(&AgentResult{
		Success:     false,
		Error:       "agent exhausted maximum turns",
		StepsJSON:   lastStepsJSON,
		StepResults: lastStepResults,
	})
}

func allPassed(results []StepResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, r := range results {
		if !r.Pass {
			return false
		}
	}
	return true
}

func makeToolResult(id, text string) apiContentBlock {
	return apiContentBlock{
		Type:      "tool_result",
		ToolUseID: id,
		Content:   text,
	}
}

// gitExec runs a git command in the given directory and returns trimmed stdout.
func gitExec(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(args, " "), err, string(ee.Stderr))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func executeGitCommand(repoRoot, command string) string {
	normalized := strings.TrimSpace(command)
	if !strings.HasPrefix(normalized, "git ") {
		return "Error: Only git commands are allowed"
	}

	allowedSubcommands := []string{"grep", "ls-files", "show", "log", "diff", "cat-file", "rev-parse", "for-each-ref"}
	parts := strings.Fields(normalized)
	if len(parts) < 2 {
		return "Error: Invalid git command"
	}
	subcommand := parts[1]

	allowed := false
	for _, a := range allowedSubcommands {
		if subcommand == a {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Sprintf("Error: Only these git subcommands are allowed: %s", strings.Join(allowedSubcommands, ", "))
	}

	// Execute the command (strip "git " prefix and pass args).
	args := parts[1:]
	out, err := gitExec(repoRoot, args...)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}

	if out == "" {
		return "(empty output)"
	}

	// Truncate long output.
	const maxLen = 8000
	if len(out) > maxLen {
		return out[:maxLen] + fmt.Sprintf("\n... (truncated, %d total chars)", len(out))
	}

	return out
}

// callAnthropic issues an LLM request, retrying transient failures (network
// errors, per-request timeouts, 429, and 5xx) up to anthropicMaxAttempts times
// with a linear backoff. Non-retryable failures (e.g. 4xx other than 429, or a
// cancelled parent context) return immediately. All sleeping respects ctx so
// the surrounding agent budget and test cancellation still win.
func callAnthropic(ctx context.Context, cfg *AgentConfig, systemPrompt string, messages []apiMessage, tools []apiTool) (*apiResponse, error) {
	var lastErr error
	for attempt := 1; attempt <= anthropicMaxAttempts; attempt++ {
		resp, err := callAnthropicOnce(ctx, cfg, systemPrompt, messages, tools)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		// Stop early if the parent context is done (agent budget exhausted or
		// the test was cancelled) or the error isn't worth retrying.
		if ctx.Err() != nil || !isRetryableAnthropicErr(err) || attempt == anthropicMaxAttempts {
			break
		}
		sleep := time.Duration(attempt) * anthropicRetryBackoff
		if cfg.Verbose {
			log.Printf("[agent] anthropic request failed (attempt %d/%d), retrying in %s: %v", attempt, anthropicMaxAttempts, sleep, err)
		}
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return nil, fmt.Errorf("anthropic API call: %w (last error: %v)", ctx.Err(), lastErr)
		}
	}
	return nil, lastErr
}

// isRetryableAnthropicErr reports whether an error from callAnthropicOnce is
// worth retrying. Transport errors (DNS, connection reset, EOF) and per-request
// deadline timeouts are transient, as are HTTP 429 and 5xx responses. A
// cancelled parent context is never retryable.
func isRetryableAnthropicErr(err error) bool {
	if err == nil {
		return false
	}
	// A cancelled or expired parent context should not be retried; only the
	// per-request timeout (a fresh deadline.Exceeded with the parent still live)
	// is handled by the ctx.Err() check in the caller.
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var apiErr *anthropicStatusError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
	}
	// Default: treat unknown errors (transport-level) as retryable, since the
	// dominant CI failure mode is a flaky network to api.anthropic.com.
	return true
}

// anthropicStatusError carries a non-200 HTTP status so the retry logic can
// distinguish retryable (429, 5xx) from terminal (other 4xx) responses.
type anthropicStatusError struct {
	StatusCode int
	Body       string
}

func (e *anthropicStatusError) Error() string {
	return fmt.Sprintf("API returned %d: %s", e.StatusCode, e.Body)
}

func callAnthropicOnce(ctx context.Context, cfg *AgentConfig, systemPrompt string, messages []apiMessage, tools []apiTool) (*apiResponse, error) {
	reqBody := apiRequest{
		Model:     cfg.Model,
		MaxTokens: 8192,
		System:    systemPrompt,
		Messages:  messages,
		Tools:     tools,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Bound each request so a hung connection can't stall the whole agent.
	// Derived from ctx so the agent budget (and test cancellation) still win.
	ctx, cancel := context.WithTimeout(ctx, anthropicCallTimeout)
	defer cancel()

	url := strings.TrimRight(cfg.AnthropicBaseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cfg.AnthropicAPIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != 200 {
		return nil, &anthropicStatusError{StatusCode: httpResp.StatusCode, Body: string(respBody)}
	}

	var resp apiResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &resp, nil
}

// isGenuineFailureSignal checks if the agent's text output indicates the
// APPLICATION is genuinely broken (not a mechanical test issue).
func isGenuineFailureSignal(text string) bool {
	lower := strings.ToLower(text)
	signals := []string{
		"genuine application failure",
		"genuine failure",
		"application is broken",
		"app is broken",
		"the application does not match",
		"not a mechanical",
		"not a test issue",
		"the app is genuinely broken",
		"this is a real bug",
		"this is a genuine bug",
	}
	for _, s := range signals {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
