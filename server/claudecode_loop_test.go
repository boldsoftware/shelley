package server

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/llm"
)

func TestCCDescriptionToSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Run echo command", "run-echo-command"},
		{"  multiple   spaces  ", "multiple-spaces"},
		{"under_score test", "under-score-test"},
		{"", ""},
		{"123 numeric", "123-numeric"},
		{"Already-kebab", "already-kebab"},
		// Long description capped at 40 chars
		{"A very long description that exceeds the maximum allowed length for slugs", "a-very-long-description-that-exceeds-the"},
	}
	for _, tc := range tests {
		got := ccDescriptionToSlug(tc.input)
		if got != tc.want {
			t.Errorf("ccDescriptionToSlug(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestClaudeCodeLoop_ProcessStream feeds the captured CC stream-json testdata through
// processStream and verifies that:
//   - top-level assistant/user messages are recorded to the main conversation
//   - subagent messages (parent_tool_use_id != "") are routed to a separate conversation
//     via ccSubagentBridge
//   - the slug is derived from the Task tool's description
func TestClaudeCodeLoop_ProcessStream_SubagentBridge(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	conv, err := database.CreateConversation(ctx, nil, true, nil, nil)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Capture messages recorded to each conversation.
	var mainMessages []llm.Message
	var subMessages []llm.Message

	mainCM := NewConversationManager(
		conv.ConversationID, database, logger,
		claudetool.ToolSetConfig{},
		func(_ context.Context, msg llm.Message, _ llm.Usage) error {
			mainMessages = append(mainMessages, msg)
			return nil
		},
		nil, "",
	)
	mainCM.cwd = t.TempDir()

	var capturedSlug string
	mainCM.ccSubagentBridge = func(ctx context.Context, slug, parentID, cwd string) (*ConversationManager, error) {
		capturedSlug = slug
		subConv, err := database.CreateSubagentConversation(ctx, slug, parentID, &cwd)
		if err != nil {
			return nil, err
		}
		subCM := NewConversationManager(
			subConv.ConversationID, database, logger,
			claudetool.ToolSetConfig{},
			func(_ context.Context, msg llm.Message, _ llm.Usage) error {
				subMessages = append(subMessages, msg)
				return nil
			},
			nil, "",
		)
		return subCM, nil
	}

	f, err := os.Open("testdata/cc_subagent_stream.json")
	if err != nil {
		t.Fatalf("open testdata: %v", err)
	}
	defer f.Close()

	loop := &claudeCodeLoop{cm: mainCM}
	if err := loop.processStream(ctx, f); err != nil {
		t.Fatalf("processStream: %v", err)
	}

	// --- Slug ---
	if capturedSlug != "run-echo-command" {
		t.Errorf("subagent slug = %q, want %q", capturedSlug, "run-echo-command")
	}

	// --- Main conversation: 3 messages ---
	// 1. assistant: thinking + Task tool_use (two stream events merged by message ID)
	// 2. user:      Task tool_result (the subagent's return value)
	// 3. assistant: final text response (end-of-turn)
	if len(mainMessages) != 3 {
		t.Fatalf("main messages: got %d, want 3", len(mainMessages))
	}

	// msg 0: assistant with thinking + Task tool_use
	m0 := mainMessages[0]
	if m0.Role != llm.MessageRoleAssistant {
		t.Errorf("main[0] role = %v, want assistant", m0.Role)
	}
	if len(m0.Content) != 2 {
		t.Errorf("main[0] content blocks = %d, want 2 (thinking + tool_use)", len(m0.Content))
	}
	hasTaskUse := false
	for _, c := range m0.Content {
		if c.Type == llm.ContentTypeToolUse && c.ToolName == "Task" {
			hasTaskUse = true
		}
	}
	if !hasTaskUse {
		t.Error("main[0] should contain a Task tool_use block")
	}
	if m0.EndOfTurn {
		t.Error("main[0] EndOfTurn should be false (has tool_use)")
	}

	// msg 1: user with Task tool_result
	m1 := mainMessages[1]
	if m1.Role != llm.MessageRoleUser {
		t.Errorf("main[1] role = %v, want user", m1.Role)
	}
	hasTaskResult := false
	for _, c := range m1.Content {
		if c.Type == llm.ContentTypeToolResult {
			hasTaskResult = true
		}
	}
	if !hasTaskResult {
		t.Error("main[1] should contain a Task tool_result block")
	}

	// msg 2: assistant with final text, end-of-turn
	m2 := mainMessages[2]
	if m2.Role != llm.MessageRoleAssistant {
		t.Errorf("main[2] role = %v, want assistant", m2.Role)
	}
	if !m2.EndOfTurn {
		t.Error("main[2] EndOfTurn should be true (text only, no tool_use)")
	}
	hasText := false
	for _, c := range m2.Content {
		if c.Type == llm.ContentTypeText && c.Text != "" {
			hasText = true
		}
	}
	if !hasText {
		t.Error("main[2] should contain non-empty text")
	}

	// --- Subagent conversation: 3 messages ---
	// 1. user:      initial prompt forwarded to the subagent
	// 2. assistant: Bash tool_use
	// 3. user:      Bash tool_result
	if len(subMessages) != 3 {
		t.Fatalf("subagent messages: got %d, want 3", len(subMessages))
	}

	// sub msg 0: user prompt
	s0 := subMessages[0]
	if s0.Role != llm.MessageRoleUser {
		t.Errorf("sub[0] role = %v, want user", s0.Role)
	}
	hasPromptText := false
	for _, c := range s0.Content {
		if c.Type == llm.ContentTypeText && c.Text != "" {
			hasPromptText = true
		}
	}
	if !hasPromptText {
		t.Error("sub[0] should contain the prompt text")
	}

	// sub msg 1: assistant Bash tool_use
	s1 := subMessages[1]
	if s1.Role != llm.MessageRoleAssistant {
		t.Errorf("sub[1] role = %v, want assistant", s1.Role)
	}
	hasBash := false
	for _, c := range s1.Content {
		if c.Type == llm.ContentTypeToolUse && c.ToolName == "Bash" {
			hasBash = true
		}
	}
	if !hasBash {
		t.Error("sub[1] should contain a Bash tool_use block")
	}
	if s1.EndOfTurn {
		t.Error("sub[1] EndOfTurn should be false (has tool_use)")
	}

	// sub msg 2: user Bash tool_result
	s2 := subMessages[2]
	if s2.Role != llm.MessageRoleUser {
		t.Errorf("sub[2] role = %v, want user", s2.Role)
	}
	hasBashResult := false
	for _, c := range s2.Content {
		if c.Type == llm.ContentTypeToolResult {
			hasBashResult = true
		}
	}
	if !hasBashResult {
		t.Error("sub[2] should contain a Bash tool_result block")
	}
}
