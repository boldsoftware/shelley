package claudetool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockCheckingSubagentRunner struct {
	tool *SubagentTool
	slug string
}

func (r *lockCheckingSubagentRunner) RunSubagent(context.Context, string, string, bool, time.Duration, string, string) (string, error) {
	value, ok := r.tool.slugLocks.Load(r.slug)
	if !ok {
		return "", errors.New("slug lock was not created")
	}
	lock := value.(*sync.Mutex)
	if lock.TryLock() {
		lock.Unlock()
		return "", errors.New("slug lock was not held while running subagent")
	}
	return "ok", nil
}

func TestSubagentToolRunHoldsSlugLock(t *testing.T) {
	tool := &SubagentTool{
		DB:                   newMockSubagentDB(),
		ParentConversationID: "parent-123",
		WorkingDir:           NewMutableWorkingDir("/tmp"),
	}
	tool.Runner = &lockCheckingSubagentRunner{tool: tool, slug: "same-slug"}

	defined := tool.Tool()
	input, err := json.Marshal(subagentInput{Slug: "same-slug", Prompt: "do something"})
	if err != nil {
		t.Fatal(err)
	}
	if result := defined.Run(context.Background(), input); result.Error != nil {
		t.Fatalf("subagent run failed: %v", result.Error)
	}
}

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
	response      string
	err           error
	lastModelID   string // Capture for assertions
	lastReasoning string // Capture for assertions
}

func (m *mockSubagentRunner) RunSubagent(ctx context.Context, conversationID, prompt string, wait bool, timeout time.Duration, modelID, reasoning string) (string, error) {
	m.lastModelID = modelID
	m.lastReasoning = reasoning
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
		ModelID:              "claude-opus-4-20250514",
	}

	input := subagentInput{
		Slug:   "test-task",
		Prompt: "Do something useful",
	}
	inputJSON, _ := json.Marshal(input)

	result := tool.Tool().Run(context.Background(), inputJSON)
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
		result := tool.Tool().Run(context.Background(), inputJSON)
		if result.Error == nil {
			t.Error("expected error for empty slug")
		}
	})

	// Test empty prompt
	t.Run("empty prompt", func(t *testing.T) {
		input := subagentInput{Slug: "test", Prompt: ""}
		inputJSON, _ := json.Marshal(input)
		result := tool.Tool().Run(context.Background(), inputJSON)
		if result.Error == nil {
			t.Error("expected error for empty prompt")
		}
	})

	// Test invalid slug (only special chars)
	t.Run("invalid slug", func(t *testing.T) {
		input := subagentInput{Slug: "@#$%", Prompt: "test"}
		inputJSON, _ := json.Marshal(input)
		result := tool.Tool().Run(context.Background(), inputJSON)
		if result.Error == nil {
			t.Error("expected error for invalid slug")
		}
	})
}

func TestSubagentTool_InheritsModel(t *testing.T) {
	wd := NewMutableWorkingDir("/tmp")
	db := newMockSubagentDB()
	runner := &mockSubagentRunner{response: "OK"}

	tool := &SubagentTool{
		DB:                   db,
		ParentConversationID: "parent-123",
		WorkingDir:           wd,
		Runner:               runner,
		ModelID:              "claude-sonnet-4-6",
	}

	input := subagentInput{Slug: "test", Prompt: "do something"}
	inputJSON, _ := json.Marshal(input)
	tool.Tool().Run(context.Background(), inputJSON)

	if runner.lastModelID != "claude-sonnet-4-6" {
		t.Errorf("expected model 'claude-sonnet-4-6', got %q", runner.lastModelID)
	}
}

func TestSubagentTool_ModelOverride(t *testing.T) {
	wd := NewMutableWorkingDir("/tmp")
	db := newMockSubagentDB()
	runner := &mockSubagentRunner{response: "OK"}

	tool := &SubagentTool{
		DB:                   db,
		ParentConversationID: "parent-123",
		WorkingDir:           wd,
		Runner:               runner,
		ModelID:              "claude-sonnet-4-6",
		AvailableModels: []AvailableModel{
			{ID: "claude-sonnet-4-6"},
			{ID: "claude-haiku-4.5", DisplayName: "Claude Haiku 4.5"},
		},
	}

	// Verify the tool schema includes model enum
	llmTool := tool.Tool()
	schemaJSON, _ := json.Marshal(llmTool.InputSchema)
	schemaStr := string(schemaJSON)
	if !strings.Contains(schemaStr, "claude-haiku-4.5") {
		t.Errorf("expected schema to contain model enum, got %s", schemaStr)
	}

	// Verify the description includes available models
	if !strings.Contains(llmTool.Description, "claude-haiku-4.5 (Claude Haiku 4.5)") {
		t.Errorf("expected description to list model with display name, got %s", llmTool.Description)
	}
	if !strings.Contains(llmTool.Description, "claude-sonnet-4-6") {
		t.Errorf("expected description to list model without display name suffix, got %s", llmTool.Description)
	}
	// sonnet has no display name, so it should NOT have parentheses
	if strings.Contains(llmTool.Description, "claude-sonnet-4-6 (") {
		t.Errorf("expected no display name suffix for sonnet, got %s", llmTool.Description)
	}

	// Override model
	input := subagentInput{Slug: "test", Prompt: "do something", Model: "claude-haiku-4.5"}
	inputJSON, _ := json.Marshal(input)
	tool.Tool().Run(context.Background(), inputJSON)

	if runner.lastModelID != "claude-haiku-4.5" {
		t.Errorf("expected model 'claude-haiku-4.5', got %q", runner.lastModelID)
	}
}

func TestSubagentTool_ModelOverride_InvalidModel(t *testing.T) {
	wd := NewMutableWorkingDir("/tmp")
	db := newMockSubagentDB()
	runner := &mockSubagentRunner{response: "OK"}

	tool := &SubagentTool{
		DB:                   db,
		ParentConversationID: "parent-123",
		WorkingDir:           wd,
		Runner:               runner,
		ModelID:              "claude-sonnet-4-6",
		AvailableModels: []AvailableModel{
			{ID: "claude-sonnet-4-6"},
			{ID: "claude-haiku-4.5"},
		},
	}

	input := subagentInput{Slug: "test", Prompt: "do something", Model: "nonexistent-model"}
	inputJSON, _ := json.Marshal(input)
	result := tool.Tool().Run(context.Background(), inputJSON)
	if result.Error == nil {
		t.Fatal("expected error for invalid model")
	}
	if !strings.Contains(result.Error.Error(), "nonexistent-model") {
		t.Errorf("expected error to mention invalid model, got %v", result.Error)
	}
	if !strings.Contains(result.Error.Error(), "claude-sonnet-4-6") {
		t.Errorf("expected error to list available models, got %v", result.Error)
	}
}

func TestSubagentTool_NoModels(t *testing.T) {
	// When no available models, schema should not have model enum
	tool := &SubagentTool{
		DB:                   newMockSubagentDB(),
		ParentConversationID: "parent-123",
		WorkingDir:           NewMutableWorkingDir("/tmp"),
		Runner:               &mockSubagentRunner{response: "OK"},
		ModelID:              "some-model",
	}

	llmTool := tool.Tool()
	schemaJSON, _ := json.Marshal(llmTool.InputSchema)
	schemaStr := string(schemaJSON)
	// The model property is only present when models are available; make sure
	// the model enum specifically is absent (the reasoning enum is always
	// present, so we can't just check for the substring "enum").
	if strings.Contains(schemaStr, `"model"`) {
		t.Errorf("expected no model property in schema when no available models, got %s", schemaStr)
	}
	if strings.Contains(llmTool.Description, "Available models") {
		t.Errorf("expected no model list in description when no available models")
	}
}

func TestSubagentTool_InheritsReasoning(t *testing.T) {
	tool := &SubagentTool{
		DB:                   newMockSubagentDB(),
		ParentConversationID: "parent-123",
		WorkingDir:           NewMutableWorkingDir("/tmp"),
		Runner:               &mockSubagentRunner{response: "OK"},
		ParentReasoning:      "high",
	}

	runner := tool.Runner.(*mockSubagentRunner)
	input := subagentInput{Slug: "test", Prompt: "do something"}
	inputJSON, _ := json.Marshal(input)
	tool.Tool().Run(context.Background(), inputJSON)

	if runner.lastReasoning != "high" {
		t.Errorf("expected inherited reasoning 'high', got %q", runner.lastReasoning)
	}
}

func TestSubagentTool_ReasoningOverride(t *testing.T) {
	tool := &SubagentTool{
		DB:                   newMockSubagentDB(),
		ParentConversationID: "parent-123",
		WorkingDir:           NewMutableWorkingDir("/tmp"),
		Runner:               &mockSubagentRunner{response: "OK"},
		ParentReasoning:      "high",
	}

	// The reasoning enum is always present in the schema.
	schemaJSON, _ := json.Marshal(tool.Tool().InputSchema)
	if !strings.Contains(string(schemaJSON), `"reasoning"`) {
		t.Errorf("expected reasoning property in schema, got %s", schemaJSON)
	}

	runner := tool.Runner.(*mockSubagentRunner)
	input := subagentInput{Slug: "test", Prompt: "do something", Reasoning: "max"}
	inputJSON, _ := json.Marshal(input)
	tool.Tool().Run(context.Background(), inputJSON)

	if runner.lastReasoning != "max" {
		t.Errorf("expected reasoning override 'max', got %q", runner.lastReasoning)
	}
}

func TestSubagentTool_ReasoningOverride_Invalid(t *testing.T) {
	tool := &SubagentTool{
		DB:                   newMockSubagentDB(),
		ParentConversationID: "parent-123",
		WorkingDir:           NewMutableWorkingDir("/tmp"),
		Runner:               &mockSubagentRunner{response: "OK"},
	}

	input := subagentInput{Slug: "test", Prompt: "do something", Reasoning: "turbo"}
	inputJSON, _ := json.Marshal(input)
	result := tool.Tool().Run(context.Background(), inputJSON)
	if result.Error == nil {
		t.Fatal("expected error for invalid reasoning level")
	}
	if !strings.Contains(result.Error.Error(), "turbo") {
		t.Errorf("expected error to mention invalid level, got %v", result.Error)
	}
}
