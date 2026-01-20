package claudetool

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// mockSubagentDB implements SubagentDB for testing.
type mockSubagentDB struct {
	conversations map[string]string // slug -> conversationID
}

func newMockSubagentDB() *mockSubagentDB {
	return &mockSubagentDB{
		conversations: make(map[string]string),
	}
}

func (m *mockSubagentDB) GetOrCreateSubagentConversation(ctx context.Context, slug, parentID, cwd string) (string, string, error) {
	key := parentID + ":" + slug
	if id, ok := m.conversations[key]; ok {
		return id, slug, nil
	}
	id := "subagent-" + slug
	m.conversations[key] = id
	return id, slug, nil
}

// mockSubagentRunner implements SubagentRunner for testing.
type mockSubagentRunner struct {
	response string
	err      error
}

func (m *mockSubagentRunner) RunSubagent(ctx context.Context, conversationID, prompt string, wait bool, timeout time.Duration) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}

func TestSubagentTool_SanitizeSlug(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"test-slug", "test-slug"},
		{"Test Slug", "test-slug"},
		{"test_slug", "test-slug"},
		{"test--slug", "test-slug"},
		{"-test-slug-", "test-slug"},
		{"test@slug!", "testslug"},
		{"123-abc", "123-abc"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := sanitizeSlug(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeSlug(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSubagentTool_Run(t *testing.T) {
	wd := NewMutableWorkingDir("/tmp")
	db := newMockSubagentDB()
	runner := &mockSubagentRunner{response: "Task completed successfully"}

	tool := &SubagentTool{
		DB:                   db,
		ParentConversationID: "parent-123",
		WorkingDir:           wd,
		Runner:               runner,
	}

	input := subagentInput{
		Slug:   "test-task",
		Prompt: "Do something useful",
	}
	inputJSON, _ := json.Marshal(input)

	result := tool.Run(context.Background(), inputJSON)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	if len(result.LLMContent) == 0 {
		t.Fatal("expected LLM content")
	}

	if result.LLMContent[0].Text == "" {
		t.Error("expected non-empty response text")
	}

	// Check display data
	if result.Display == nil {
		t.Error("expected display data")
	}
	displayData, ok := result.Display.(SubagentDisplayData)
	if !ok {
		t.Error("display data should be SubagentDisplayData")
	}
	if displayData.Slug != "test-task" {
		t.Errorf("expected slug 'test-task', got %q", displayData.Slug)
	}
}

func TestSubagentTool_Validation(t *testing.T) {
	wd := NewMutableWorkingDir("/tmp")
	db := newMockSubagentDB()
	runner := &mockSubagentRunner{response: "OK"}

	tool := &SubagentTool{
		DB:                   db,
		ParentConversationID: "parent-123",
		WorkingDir:           wd,
		Runner:               runner,
	}

	// Test empty slug
	t.Run("empty slug", func(t *testing.T) {
		input := subagentInput{Slug: "", Prompt: "test"}
		inputJSON, _ := json.Marshal(input)
		result := tool.Run(context.Background(), inputJSON)
		if result.Error == nil {
			t.Error("expected error for empty slug")
		}
	})

	// Test empty prompt
	t.Run("empty prompt", func(t *testing.T) {
		input := subagentInput{Slug: "test", Prompt: ""}
		inputJSON, _ := json.Marshal(input)
		result := tool.Run(context.Background(), inputJSON)
		if result.Error == nil {
			t.Error("expected error for empty prompt")
		}
	})

	// Test invalid slug (only special chars)
	t.Run("invalid slug", func(t *testing.T) {
		input := subagentInput{Slug: "@#$%", Prompt: "test"}
		inputJSON, _ := json.Marshal(input)
		result := tool.Run(context.Background(), inputJSON)
		if result.Error == nil {
			t.Error("expected error for invalid slug")
		}
	})
}
