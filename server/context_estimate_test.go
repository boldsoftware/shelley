package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

func textMsg(role llm.MessageRole, text string) llm.Message {
	return llm.Message{Role: role, Content: []llm.Content{{Type: llm.ContentTypeText, Text: text}}}
}

func toolResultMsg(text string) llm.Message {
	return llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type:       llm.ContentTypeToolResult,
			ToolUseID:  "t1",
			ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: text}},
		}},
	}
}

func TestFindPiCutPointNeverLandsOnToolResult(t *testing.T) {
	// big strings so each message is ~ many tokens
	big := strings.Repeat("x", 4000) // ~1000 tokens
	msgs := []llm.Message{
		textMsg(llm.MessageRoleUser, big),      // 0
		textMsg(llm.MessageRoleAssistant, big), // 1 (tool_use elided; text only)
		toolResultMsg(big),                     // 2 - NOT a valid cut point
		textMsg(llm.MessageRoleAssistant, big), // 3
		textMsg(llm.MessageRoleUser, big),      // 4
	}

	// keepRecentTokens chosen so the walk-back crosses the tool result.
	cut := findPiCutPoint(msgs, 2500, piTokenCounter)
	if cut < 0 || cut >= len(msgs) {
		t.Fatalf("cut out of range: %d", cut)
	}
	if isToolResultMessage(msgs[cut]) {
		t.Fatalf("cut landed on a tool_result message at index %d", cut)
	}
}

func TestFindPiCutPointKeepsAllWhenSmall(t *testing.T) {
	msgs := []llm.Message{
		textMsg(llm.MessageRoleUser, "hi"),
		textMsg(llm.MessageRoleAssistant, "hello"),
	}
	// Plenty of budget -> nothing summarized, cut at start.
	if cut := findPiCutPoint(msgs, 20000, piTokenCounter); cut != 0 {
		t.Fatalf("expected cut=0 (keep all), got %d", cut)
	}
}

func TestTokenCounterCountsImages(t *testing.T) {
	msg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeText,
				MediaType: "image/png",
				Data:      strings.Repeat("a", 400_000),
			},
			{
				Type: llm.ContentTypeToolResult,
				ToolResult: []llm.Content{{
					Type:      llm.ContentTypeText,
					MediaType: "image/jpeg",
					Data:      strings.Repeat("b", 400),
				}},
			},
		},
	}

	// An image costs about the same however big its data is.
	if got := piTokenCounter.message(msg); got != 2*piImageTokens {
		t.Fatalf("estimated tokens = %d, want %d", got, 2*piImageTokens)
	}
}

func TestFindPiCutPointKeepsHugeLastToolResultWithItsCall(t *testing.T) {
	msgs := []llm.Message{
		textMsg(llm.MessageRoleUser, "first"),
		textMsg(llm.MessageRoleAssistant, "ok"),
		textMsg(llm.MessageRoleUser, "second"),
		{Role: llm.MessageRoleAssistant, Content: []llm.Content{{ID: "t1", Type: llm.ContentTypeToolUse, ToolName: "bash", ToolInput: json.RawMessage(`{}`)}}},
		toolResultMsg(strings.Repeat("x", 100_000)),
	}
	// The result alone fills the budget and no cut point follows it: the
	// call stays with it and everything before is summarized.
	if cut := findPiCutPoint(msgs, 20_000, piTokenCounter); cut != 3 {
		t.Fatalf("cut = %d, want 3", cut)
	}
}

func TestFindPiCutPointExcludesImageHeavyHistory(t *testing.T) {
	msgs := []llm.Message{textMsg(llm.MessageRoleUser, "inspect the pages")}
	for i := range 20 {
		id := fmt.Sprintf("screenshot-%d", i)
		msgs = append(msgs, llm.Message{
			Role: llm.MessageRoleAssistant,
			Content: []llm.Content{{
				ID:        id,
				Type:      llm.ContentTypeToolUse,
				ToolName:  "browser",
				ToolInput: json.RawMessage(`{"action":"screenshot"}`),
			}},
		}, llm.Message{
			Role: llm.MessageRoleUser,
			Content: []llm.Content{{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: id,
				ToolResult: []llm.Content{{
					Type:      llm.ContentTypeText,
					MediaType: "image/png",
					Data:      strings.Repeat("a", 100_000),
				}},
			}},
		})
	}
	msgs = append(msgs, textMsg(llm.MessageRoleAssistant, "The pages look correct."))

	// 20k tokens hold 16 screenshots: the oldest are summarized.
	if cut := findPiCutPoint(msgs, 20_000, piTokenCounter); cut != 9 {
		t.Fatalf("cut = %d, want 9", cut)
	}
}

// TestTokenCounterFor: estimates follow the model's tokenizer, measured on a
// mix of code, prose and command output.
func TestTokenCounterFor(t *testing.T) {
	msg := textMsg(llm.MessageRoleUser, strings.Repeat("a", 1000))
	for model, want := range map[string]int{
		"claude-opus-5.5":   432, // 2.32 chars/token
		"claude-opus-4.7":   432,
		"claude-opus-4.5":   341, // 2.94
		"claude-sonnet-4.5": 341,
		"gpt-5.5":           278, // 3.61
		"gemini-3.1-pro":    250, // unmeasured: 4
		"predictable":       250,
		"no-such-model":     250,
	} {
		if got := tokenCounterFor(model).message(msg); got != want {
			t.Errorf("%s: %d tokens, want %d", model, got, want)
		}
	}
}

// TestTokenCounterOverhead: the context is more than its history; the system
// prompt and the tool definitions are sent with every request.
func TestTokenCounterOverhead(t *testing.T) {
	system := []llm.SystemContent{{Text: strings.Repeat("s", 400)}}
	tools := []*llm.Tool{{Name: "bash", Description: strings.Repeat("d", 392), InputSchema: json.RawMessage(`{"type":"object"}`)}}
	// 400 system chars, 4+392+17 tool chars, at 4 chars per token.
	if got, want := piTokenCounter.overhead(system, tools), 100+104; got != want {
		t.Fatalf("overhead = %d tokens, want %d", got, want)
	}
}
