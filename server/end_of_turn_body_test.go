package server

import (
	"encoding/json"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func agentMsg(t *testing.T, contents ...llm.Content) generated.Message {
	t.Helper()
	m := llm.Message{Content: contents}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	return generated.Message{Type: string(db.MessageTypeAgent), LlmData: &s}
}

func TestFinalResponseBody_PrefersTextFromLatest(t *testing.T) {
	// newest first; latest is text
	msgs := []generated.Message{
		agentMsg(t, llm.Content{Type: llm.ContentTypeText, Text: "Done!"}),
		agentMsg(t, llm.Content{Type: llm.ContentTypeText, Text: "older"}),
	}
	if got := finalResponseBody(msgs); got != "Done!" {
		t.Errorf("got %q, want %q", got, "Done!")
	}
}

func TestFinalResponseBody_SkipsToolOnlyTail(t *testing.T) {
	// Newest: tool-only. Older: has text. Should fall back to older text.
	toolOnly := agentMsg(t,
		llm.Content{
			Type: llm.ContentTypeToolUse, ToolName: "bash",
			ToolInput: json.RawMessage(`{"command":"git status"}`),
		},
	)
	textOlder := agentMsg(t,
		llm.Content{Type: llm.ContentTypeText, Text: "All good, ready for review."},
	)
	got := finalResponseBody([]generated.Message{toolOnly, textOlder})
	if got != "All good, ready for review." {
		t.Errorf("got %q", got)
	}
}

func TestFinalResponseBody_SummarizesToolWhenNoText(t *testing.T) {
	msgs := []generated.Message{
		agentMsg(t,
			llm.Content{
				Type: llm.ContentTypeToolUse, ToolName: "bash",
				ToolInput: json.RawMessage(`{"command":"git status"}`),
			},
		),
	}
	got := finalResponseBody(msgs)
	want := "Ran bash: git status"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFinalResponseBody_SummarizesPatchTool(t *testing.T) {
	msgs := []generated.Message{
		agentMsg(t,
			llm.Content{
				Type: llm.ContentTypeToolUse, ToolName: "patch",
				ToolInput: json.RawMessage(`{"path":"server/server.go","patches":[]}`),
			},
		),
	}
	got := finalResponseBody(msgs)
	if got != "Ran patch: server/server.go" {
		t.Errorf("got %q", got)
	}
}

func TestFinalResponseBody_Empty(t *testing.T) {
	if got := finalResponseBody(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestFinalResponseBody_MultilineCommandFirstLine(t *testing.T) {
	msgs := []generated.Message{
		agentMsg(t,
			llm.Content{
				Type: llm.ContentTypeToolUse, ToolName: "bash",
				ToolInput: json.RawMessage(`{"command":"\n  set -e\n  echo hi\n"}`),
			},
		),
	}
	got := finalResponseBody(msgs)
	if got != "Ran bash: set -e" {
		t.Errorf("got %q", got)
	}
}

func TestPushTitleAndSubtitle(t *testing.T) {
	tests := []struct {
		host, slug      string
		wantTitle, want string
	}{
		{"phil-dev.exe.xyz", "fix-the-bug", "fix-the-bug", "phil-dev.exe.xyz"},
		{"", "fix-the-bug", "fix-the-bug", ""},
		{"phil-dev.exe.xyz", "", "phil-dev.exe.xyz", ""},
		{"", "", "Shelley", ""},
	}
	for _, tc := range tests {
		gotT, gotS := pushTitleAndSubtitle(tc.host, tc.slug)
		if gotT != tc.wantTitle || gotS != tc.want {
			t.Errorf("pushTitleAndSubtitle(%q, %q) = (%q, %q); want (%q, %q)", tc.host, tc.slug, gotT, gotS, tc.wantTitle, tc.want)
		}
	}
}
