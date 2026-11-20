package server

// Content generators for synthetic (lorem ipsum) conversations. These build
// realistic-looking message bodies and tool calls/results with the exact
// display-data shapes the Vue tool components expect, so a generated
// conversation renders every tool renderer.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/llm"
)

const loremWords = "lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt ut labore et dolore magna aliqua enim ad minim veniam quis nostrud exercitation ullamco laboris nisi aliquip ex ea commodo consequat"

var loremVocab = strings.Fields(loremWords)

// lorem returns n pseudo-random lorem words, deterministic in seed so
// regenerating the same conversation size yields the same text.
func lorem(seed, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		w := loremVocab[(seed*7+i*13)%len(loremVocab)]
		if i == 0 {
			w = strings.ToUpper(w[:1]) + w[1:]
		}
		b.WriteString(w)
	}
	b.WriteByte('.')
	return b.String()
}

func (g *loremGen) userMessage(i int) llm.Message {
	return llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type: llm.ContentTypeText,
			Text: fmt.Sprintf("Turn %d: %s\n\nPlease investigate and fix. %s", i+1, lorem(i, 12), lorem(i+1, 20)),
		}},
	}
}

func (g *loremGen) thinkingText(i int) string {
	return fmt.Sprintf("Let me think about turn %d. %s\n\n%s\n\n%s",
		i+1, lorem(i+2, 30), lorem(i+3, 25), lorem(i+4, 18))
}

func (g *loremGen) agentIntro(i int) string {
	return fmt.Sprintf("I'll work on turn %d. %s Here's my plan:\n\n1. %s\n2. %s\n3. %s",
		i+1, lorem(i+5, 10), lorem(i+6, 6), lorem(i+7, 6), lorem(i+8, 6))
}

func (g *loremGen) agentSummary(i int) string {
	return fmt.Sprintf("Done with turn %d. %s\n\n```go\nfunc handler%d() error {\n\treturn nil // %s\n}\n```\n\n%s",
		i+1, lorem(i+9, 14), i, lorem(i+10, 4), lorem(i+11, 22))
}

func (g *loremGen) usage(i int, endOfTurn bool) llm.Usage {
	base := uint64(500 + i*37%4000)
	out := uint64(120 + i*29%1500)
	if endOfTurn {
		out += 200
	}
	start := g.tick(500 * 1e6) // 0.5s
	end := g.tick(2 * 1e9)     // 2s
	return llm.Usage{
		InputTokens:              base,
		CacheCreationInputTokens: uint64(2000 + i*11%6000),
		CacheReadInputTokens:     uint64(i * 137 % 40000),
		OutputTokens:             out,
		CostUSD:                  float64(base+out) / 1e6 * 3.0,
		Model:                    g.model,
		URL:                      "https://api.anthropic.com/v1/messages",
		StartTime:                &start,
		EndTime:                  &end,
	}
}

// toolCall bundles a tool_use content block with its synthetic result and
// display data for a single call.
type toolCall struct {
	use     llm.Content
	result  []llm.Content
	display any
	isError bool
}

func toolUseID(i, k int) string {
	return fmt.Sprintf("toolu_lorem_%d_%d", i, k)
}

func rawInput(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// toolBatch returns the set of tool calls for turn i. It cycles through all
// tool kinds so, across a conversation, every tool renderer is exercised.
// The batch size varies per turn to produce coalescing of multiple tool
// pills.
func (g *loremGen) toolBatch(i int) []toolCall {
	// Each entry is a builder; we rotate the starting point per turn and
	// take a variable-length slice so batches differ.
	builders := []func(i, k int) toolCall{
		g.bashCall,
		g.patchCall,
		g.changeDirCall,
		g.subagentCall,
		g.webSearchCall,
		g.browserCall,
		g.llmOneShotCall,
		g.outputIframeCall,
		g.genericCall,
		g.errorToolCall,
	}
	n := 1 + i%4 // 1..4 tool calls per turn
	out := make([]toolCall, 0, n)
	for k := 0; k < n; k++ {
		b := builders[(i+k)%len(builders)]
		out = append(out, b(i, k))
	}
	return out
}

func textResult(s string) []llm.Content {
	return []llm.Content{{Type: llm.ContentTypeText, Text: s}}
}

func (g *loremGen) bashCall(i, k int) toolCall {
	cmd := fmt.Sprintf("grep -rn %q server/ | head -20", loremVocab[i%len(loremVocab)])
	out := strings.Builder{}
	for j := 0; j < 8; j++ {
		fmt.Fprintf(&out, "server/file%d.go:%d:\t%s\n", j, i*10+j, lorem(i+j, 8))
	}
	return toolCall{
		use: llm.Content{
			Type: llm.ContentTypeToolUse, ID: toolUseID(i, k),
			ToolName: "bash", ToolInput: rawInput(map[string]any{"command": cmd}),
		},
		result:  textResult(out.String()),
		display: claudetool.BashDisplayData{WorkingDir: g.cwd},
	}
}

func (g *loremGen) patchCall(i, k int) toolCall {
	path := fmt.Sprintf("server/handler_%d.go", i%20)
	diff := g.unifiedDiff(path, i)
	return toolCall{
		use: llm.Content{
			Type: llm.ContentTypeToolUse, ID: toolUseID(i, k),
			ToolName: "patch",
			ToolInput: rawInput(map[string]any{
				"path": path,
				"patches": []map[string]any{{
					"operation": "replace",
					"oldText":   fmt.Sprintf("// old line %d", i),
					"newText":   fmt.Sprintf("// new line %d: %s", i, lorem(i, 5)),
				}},
			}),
		},
		result:  textResult("<patches_applied>all</patches_applied>\n"),
		display: claudetool.PatchDisplayData{Path: path, Diff: diff},
	}
}

// unifiedDiff synthesizes a small unified diff so the PatchTool diff
// renderer has realistic input.
func (g *loremGen) unifiedDiff(path string, i int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)
	fmt.Fprintf(&b, "@@ -%d,6 +%d,7 @@ func handler%d() {\n", i+1, i+1, i)
	fmt.Fprintf(&b, " \tctx := context.Background()\n")
	fmt.Fprintf(&b, " \t// %s\n", lorem(i, 6))
	fmt.Fprintf(&b, "-\treturn oldValue // %s\n", lorem(i+1, 4))
	fmt.Fprintf(&b, "+\treturn newValue // %s\n", lorem(i+2, 4))
	fmt.Fprintf(&b, "+\t// added: %s\n", lorem(i+3, 5))
	fmt.Fprintf(&b, " \t}\n")
	return b.String()
}

func (g *loremGen) changeDirCall(i, k int) toolCall {
	dir := fmt.Sprintf("%s/pkg/mod%d", g.cwd, i%7)
	return toolCall{
		use: llm.Content{
			Type: llm.ContentTypeToolUse, ID: toolUseID(i, k),
			ToolName: "change_dir", ToolInput: rawInput(map[string]any{"path": dir}),
		},
		result: textResult("Changed working directory to: " + dir),
	}
}

func (g *loremGen) subagentCall(i, k int) toolCall {
	slug := fmt.Sprintf("worker-%d-%d", i, k)
	subID := "c" + strconv.Itoa(100000+i*10+k)
	return toolCall{
		use: llm.Content{
			Type: llm.ContentTypeToolUse, ID: toolUseID(i, k),
			ToolName: "subagent",
			ToolInput: rawInput(map[string]any{
				"slug":   slug,
				"prompt": "Investigate " + lorem(i, 10),
			}),
		},
		result:  textResult(fmt.Sprintf("Subagent '%s' response:\n%s", slug, lorem(i, 25))),
		display: claudetool.SubagentDisplayData{Slug: slug, ConversationID: subID},
	}
}

func (g *loremGen) webSearchCall(i, k int) toolCall {
	return toolCall{
		use: llm.Content{
			Type: llm.ContentTypeToolUse, ID: toolUseID(i, k),
			ToolName:  "web_search",
			ToolInput: rawInput(map[string]any{"query": lorem(i, 4)}),
		},
		result: []llm.Content{
			{Type: llm.ContentTypeText, Text: "Found results for " + lorem(i, 4)},
			{Type: llm.ContentTypeText, Title: "Result " + strconv.Itoa(i), URL: fmt.Sprintf("https://example.com/%d", i), Text: lorem(i, 15)},
		},
	}
}

func (g *loremGen) browserCall(i, k int) toolCall {
	return toolCall{
		use: llm.Content{
			Type: llm.ContentTypeToolUse, ID: toolUseID(i, k),
			ToolName: "browser",
			ToolInput: rawInput(map[string]any{
				"action":     "eval",
				"expression": "document.title",
			}),
		},
		result: textResult("\"Example Page " + strconv.Itoa(i) + "\""),
	}
}

func (g *loremGen) llmOneShotCall(i, k int) toolCall {
	return toolCall{
		use: llm.Content{
			Type: llm.ContentTypeToolUse, ID: toolUseID(i, k),
			ToolName: "llm_one_shot",
			ToolInput: rawInput(map[string]any{
				"prompt_files": []string{"summary.txt"},
				"model":        g.model,
			}),
		},
		result: textResult(lorem(i, 30)),
	}
}

func (g *loremGen) outputIframeCall(i, k int) toolCall {
	return toolCall{
		use: llm.Content{
			Type: llm.ContentTypeToolUse, ID: toolUseID(i, k),
			ToolName: "output_html",
			ToolInput: rawInput(map[string]any{
				"path":  fmt.Sprintf("chart_%d.html", i),
				"title": "Chart " + strconv.Itoa(i),
			}),
		},
		result: textResult("Displayed HTML to the user."),
	}
}

// genericCall exercises the GenericTool fallback renderer with an
// unrecognized tool name.
func (g *loremGen) genericCall(i, k int) toolCall {
	return toolCall{
		use: llm.Content{
			Type: llm.ContentTypeToolUse, ID: toolUseID(i, k),
			ToolName:  "custom_tool_" + strconv.Itoa(i%3),
			ToolInput: rawInput(map[string]any{"arg": lorem(i, 5), "n": i}),
		},
		result: textResult(lorem(i, 12)),
	}
}

// errorToolCall exercises the error path of a tool result.
func (g *loremGen) errorToolCall(i, k int) toolCall {
	return toolCall{
		use: llm.Content{
			Type: llm.ContentTypeToolUse, ID: toolUseID(i, k),
			ToolName:  "bash",
			ToolInput: rawInput(map[string]any{"command": "false # " + lorem(i, 3)}),
		},
		result:  textResult(fmt.Sprintf("command failed with exit code 1: %s", lorem(i, 8))),
		isError: true,
		display: claudetool.BashDisplayData{WorkingDir: g.cwd},
	}
}

// --- marker messages ---

func (g *loremGen) systemMessage() llm.Message {
	return llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type: llm.ContentTypeText,
			Text: "You are Shelley, a coding agent.\n\nWorking directory: " + g.cwd + "\n\n" + lorem(0, 40),
		}},
	}
}

func (g *loremGen) gitInfoMessage() llm.Message {
	return llm.Message{
		Role:    llm.MessageRoleAssistant,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: g.gitInfoText()}},
	}
}

func (g *loremGen) gitInfoText() string {
	return fmt.Sprintf("~/exe-loremipsum (loremipsum) now at %08x \"%s\"", g.clock.Unix()&0xffffffff, lorem(int(g.clock.Unix())%97, 6))
}

func (g *loremGen) gitInfoUserData() any {
	return GitInfoUserData{
		Worktree: g.cwd,
		Branch:   "loremipsum",
		Commit:   fmt.Sprintf("%08x", g.clock.Unix()&0xffffffff),
		Subject:  lorem(int(g.clock.Unix())%97, 6),
		Text:     g.gitInfoText(),
	}
}

func (g *loremGen) warningMessage() llm.Message {
	// Warnings are user-visible only; content is carried in user_data.
	return llm.Message{ExcludedFromContext: true}
}

func (g *loremGen) warningText(i int) string {
	return fmt.Sprintf("Warning on turn %d: %s", i+1, lorem(i, 10))
}

func (g *loremGen) errorMessage(i int) llm.Message {
	return llm.Message{
		Role:           llm.MessageRoleAssistant,
		Content:        []llm.Content{{Type: llm.ContentTypeText, Text: "LLM request failed: " + lorem(i, 8)}},
		ErrorType:      llm.ErrorTypeLLMRequest,
		ErrorRetryable: true,
		EndOfTurn:      true,
	}
}

func (g *loremGen) modelChangeMessage(i int) llm.Message {
	from, to := g.model, "claude-sonnet-4-5-20250929"
	if i%2 == 0 {
		from, to = to, g.model
	}
	return llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{{
			Type: llm.ContentTypeText,
			Text: fmt.Sprintf("Switched model from %s to %s", from, to),
		}},
		ExcludedFromContext: true,
	}
}

func (g *loremGen) modelChangeUserData(i int) any {
	from, to := g.model, "claude-sonnet-4-5-20250929"
	fromD, toD := "Claude Opus 4.5", "Claude Sonnet 4.5"
	if i%2 == 0 {
		from, to = to, g.model
		fromD, toD = toD, fromD
	}
	return ModelChangeUserData{
		From: from, To: to,
		FromDisplay: fromD, ToDisplay: toD,
		ReasoningFrom: "medium", ReasoningTo: "high",
		Text: fmt.Sprintf("Switched model from %s to %s", fromD, toD),
	}
}
