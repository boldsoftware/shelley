package claudetool

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

func TestIsStrongModel(t *testing.T) {
	tests := []struct {
		modelID  string
		expected bool
	}{
		{"claude-3-sonnet-20240229", true},
		{"claude-3-opus-20240229", true},
		{"claude-3-haiku-20240307", false},
		{"Sonnet Model", true},
		{"OPUS Model", true},
		{"haiku model", false},
		{"other-model", false},
		{"", false},
	}

	for _, test := range tests {
		result := isStrongModel(test.modelID)
		if result != test.expected {
			t.Errorf("isStrongModel(%q) = %v, expected %v", test.modelID, result, test.expected)
		}
	}
}

func TestNewToolSet(t *testing.T) {
	provider := &mockLLMProvider{}

	cfg := ToolSetConfig{
		LLMProvider: provider,
		ModelID:     "test-model",
		WorkingDir:  "/test",
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	if ts == nil {
		t.Fatal("NewToolSet returned nil")
	}

	if ts.wd == nil {
		t.Error("Working directory not initialized")
	}

	if ts.tools == nil {
		t.Error("Tools not initialized")
	}
}

func TestToolSet_Tools(t *testing.T) {
	provider := &mockLLMProvider{}

	cfg := ToolSetConfig{
		LLMProvider: provider,
		ModelID:     "test-model",
		WorkingDir:  "/test",
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	tools := ts.Tools()
	if tools == nil {
		t.Fatal("Tools() returned nil")
	}

	if len(tools) == 0 {
		t.Error("expected at least one tool")
	}
}

func TestToolSet_WorkingDir(t *testing.T) {
	provider := &mockLLMProvider{}

	cfg := ToolSetConfig{
		LLMProvider: provider,
		ModelID:     "test-model",
		WorkingDir:  "/test",
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	wd := ts.WorkingDir()
	if wd == nil {
		t.Fatal("WorkingDir() returned nil")
	}

	if wd.Get() != "/test" {
		t.Errorf("expected working dir '/test', got %q", wd.Get())
	}
}

func TestToolSet_Cleanup(t *testing.T) {
	provider := &mockLLMProvider{}

	cfg := ToolSetConfig{
		LLMProvider: provider,
		ModelID:     "test-model",
		WorkingDir:  "/test",
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	// Cleanup should not panic
	ts.Cleanup()
}

func TestNewToolSet_DefaultWorkingDir(t *testing.T) {
	provider := &mockLLMProvider{}

	// Test with empty working dir (should default to $HOME)
	cfg := ToolSetConfig{
		LLMProvider: provider,
		ModelID:     "test-model",
		WorkingDir:  "",
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	wd := ts.WorkingDir()
	if wd.Get() != home {
		t.Errorf("expected default working dir %q, got %q", home, wd.Get())
	}
}

func TestNewToolSet_WithBrowser(t *testing.T) {
	provider := &mockLLMProvider{}

	cfg := ToolSetConfig{
		LLMProvider:   provider,
		ModelID:       "test-model",
		WorkingDir:    "/test",
		EnableBrowser: true,
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	if ts == nil {
		t.Fatal("NewToolSet returned nil")
	}

	if ts.wd == nil {
		t.Error("Working directory not initialized")
	}

	if ts.tools == nil {
		t.Error("Tools not initialized")
	}
}

func TestNewToolSet_SubagentDepthLimit(t *testing.T) {
	provider := &mockLLMProvider{}
	db := newMockSubagentDB()
	runner := &mockSubagentRunner{response: "ok"}

	hasSubagentTool := func(ts *ToolSet) bool {
		for _, tool := range ts.Tools() {
			if tool.Name == "subagent" {
				return true
			}
		}
		return false
	}

	// Depth 0, MaxDepth 1 -> should have subagent tool
	t.Run("depth 0 max 1 has subagent", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider:          provider,
			ModelID:              "test-model",
			WorkingDir:           "/test",
			SubagentRunner:       runner,
			SubagentDB:           db,
			ParentConversationID: "parent-123",
			SubagentDepth:        0,
			MaxSubagentDepth:     1,
		}
		ts := NewToolSet(context.Background(), cfg)
		if !hasSubagentTool(ts) {
			t.Error("expected subagent tool at depth 0 with max 1")
		}
	})

	// Depth 1, MaxDepth 1 -> should NOT have subagent tool
	t.Run("depth 1 max 1 no subagent", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider:          provider,
			ModelID:              "test-model",
			WorkingDir:           "/test",
			SubagentRunner:       runner,
			SubagentDB:           db,
			ParentConversationID: "parent-123",
			SubagentDepth:        1,
			MaxSubagentDepth:     1,
		}
		ts := NewToolSet(context.Background(), cfg)
		if hasSubagentTool(ts) {
			t.Error("expected no subagent tool at depth 1 with max 1")
		}
	})

	// Depth 0, MaxDepth 0 (unlimited) -> should have subagent tool
	t.Run("depth 0 max 0 unlimited has subagent", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider:          provider,
			ModelID:              "test-model",
			WorkingDir:           "/test",
			SubagentRunner:       runner,
			SubagentDB:           db,
			ParentConversationID: "parent-123",
			SubagentDepth:        0,
			MaxSubagentDepth:     0,
		}
		ts := NewToolSet(context.Background(), cfg)
		if !hasSubagentTool(ts) {
			t.Error("expected subagent tool at depth 0 with unlimited max")
		}
	})

	// Depth 5, MaxDepth 0 (unlimited) -> should have subagent tool
	t.Run("depth 5 max 0 unlimited has subagent", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider:          provider,
			ModelID:              "test-model",
			WorkingDir:           "/test",
			SubagentRunner:       runner,
			SubagentDB:           db,
			ParentConversationID: "parent-123",
			SubagentDepth:        5,
			MaxSubagentDepth:     0,
		}
		ts := NewToolSet(context.Background(), cfg)
		if !hasSubagentTool(ts) {
			t.Error("expected subagent tool at depth 5 with unlimited max")
		}
	})

	// No SubagentRunner -> should NOT have subagent tool regardless of depth
	t.Run("no runner no subagent", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider:          provider,
			ModelID:              "test-model",
			WorkingDir:           "/test",
			ParentConversationID: "parent-123",
			SubagentDepth:        0,
			MaxSubagentDepth:     1,
		}
		ts := NewToolSet(context.Background(), cfg)
		if hasSubagentTool(ts) {
			t.Error("expected no subagent tool without runner")
		}
	})

	// Depth 2, MaxDepth 3 -> should have subagent tool
	t.Run("depth 2 max 3 has subagent", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider:          provider,
			ModelID:              "test-model",
			WorkingDir:           "/test",
			SubagentRunner:       runner,
			SubagentDB:           db,
			ParentConversationID: "parent-123",
			SubagentDepth:        2,
			MaxSubagentDepth:     3,
		}
		ts := NewToolSet(context.Background(), cfg)
		if !hasSubagentTool(ts) {
			t.Error("expected subagent tool at depth 2 with max 3")
		}
	})

	// Depth 3, MaxDepth 3 -> should NOT have subagent tool
	t.Run("depth 3 max 3 no subagent", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider:          provider,
			ModelID:              "test-model",
			WorkingDir:           "/test",
			SubagentRunner:       runner,
			SubagentDB:           db,
			ParentConversationID: "parent-123",
			SubagentDepth:        3,
			MaxSubagentDepth:     3,
		}
		ts := NewToolSet(context.Background(), cfg)
		if hasSubagentTool(ts) {
			t.Error("expected no subagent tool at depth 3 with max 3")
		}
	})
}

func TestToolDescriptions(t *testing.T) {
	// Full config: browser + subagent enabled
	provider := &mockLLMProvider{}
	cfg := ToolSetConfig{
		LLMProvider:          provider,
		ModelID:              "claude-3-sonnet",
		WorkingDir:           "/test",
		EnableBrowser:        true,
		SubagentRunner:       &mockSubagentRunner{},
		SubagentDB:           &mockSubagentDB{},
		ParentConversationID: "parent-123",
	}
	ts := NewToolSet(context.Background(), cfg)
	if len(ts.Tools()) == 0 {
		t.Fatal("NewToolSet returned no tools")
	}

	// Verify all tools have names and descriptions
	for _, tool := range ts.Tools() {
		if tool.Name == "" {
			t.Error("tool has empty name")
		}
		if tool.Description == "" {
			t.Errorf("tool %q has empty description", tool.Name)
		}
	}

	// Without browser: should not include browser/read_image
	noBrowserCfg := ToolSetConfig{
		LLMProvider:          provider,
		ModelID:              "claude-3-sonnet",
		WorkingDir:           "/test",
		EnableBrowser:        false,
		SubagentRunner:       &mockSubagentRunner{},
		SubagentDB:           &mockSubagentDB{},
		ParentConversationID: "parent-123",
	}
	noBrowserTS := NewToolSet(context.Background(), noBrowserCfg)
	for _, tool := range noBrowserTS.Tools() {
		if tool.Name == "browser" || tool.Name == "read_image" {
			t.Errorf("browser-disabled config should not include tool %q", tool.Name)
		}
	}

	// Without subagent: should not include subagent
	noSubagentCfg := ToolSetConfig{
		LLMProvider:   provider,
		ModelID:       "claude-3-sonnet",
		WorkingDir:    "/test",
		EnableBrowser: true,
	}
	noSubagentTS := NewToolSet(context.Background(), noSubagentCfg)
	for _, tool := range noSubagentTS.Tools() {
		if tool.Name == "subagent" {
			t.Error("subagent-disabled config should not include subagent tool")
		}
	}
}

// TestNewToolSet_BuildAvailableModelsFreshOnEachCall verifies that the
// available-model list is resolved fresh each time a ToolSet is built. This
// matters because subagents inherit the list, and users expect newly added
// custom models to show up in new conversations without restarting the
// server. Regression test for issue #195.
func TestNewToolSet_BuildAvailableModelsFreshOnEachCall(t *testing.T) {
	provider := &mockLLMProvider{}
	db := newMockSubagentDB()
	runner := &mockSubagentRunner{response: "ok"}

	models := []AvailableModel{{ID: "model-a"}}
	calls := 0
	cfg := ToolSetConfig{
		LLMProvider:          provider,
		ModelID:              "test-model",
		WorkingDir:           "/test",
		SubagentRunner:       runner,
		SubagentDB:           db,
		ParentConversationID: "parent",
		BuildAvailableModels: func() []AvailableModel {
			calls++
			out := make([]AvailableModel, len(models))
			copy(out, models)
			return out
		},
	}

	findSubagent := func(ts *ToolSet) string {
		for _, tool := range ts.Tools() {
			if tool.Name == "subagent" {
				return tool.Description
			}
		}
		return ""
	}

	ts1 := NewToolSet(context.Background(), cfg)
	desc1 := findSubagent(ts1)
	if desc1 == "" {
		t.Fatal("expected subagent tool in first ToolSet")
	}
	if !strings.Contains(desc1, "model-a") {
		t.Errorf("expected first description to mention model-a, got: %s", desc1)
	}

	// Simulate a custom model being added at runtime.
	models = append(models, AvailableModel{ID: "model-b", DisplayName: "Model B"})

	ts2 := NewToolSet(context.Background(), cfg)
	desc2 := findSubagent(ts2)
	if desc2 == "" {
		t.Fatal("expected subagent tool in second ToolSet")
	}
	if !strings.Contains(desc2, "model-b") {
		t.Errorf("expected second description to pick up model-b, got: %s", desc2)
	}
	if calls != 2 {
		t.Errorf("expected BuildAvailableModels to be invoked once per ToolSet, got %d calls", calls)
	}

	// When BuildAvailableModels is nil, fall back to LLMProvider.GetAvailableModels.
	cfgNoBuilder := cfg
	cfgNoBuilder.BuildAvailableModels = nil
	ts3 := NewToolSet(context.Background(), cfgNoBuilder)
	desc3 := findSubagent(ts3)
	if desc3 == "" {
		t.Fatal("expected subagent tool when falling back to LLMProvider")
	}
	// mockLLMProvider.GetAvailableModels returns nothing useful by default,
	// but the description should at least be non-empty and not panic.
	if !strings.Contains(desc3, "subagent") {
		t.Errorf("expected fallback description to mention subagents, got: %s", desc3)
	}
}

// mockServiceWithProvider is a mock llm.Service that returns a configurable provider.
type mockServiceWithProvider struct {
	mockService
	provider string
}

func (m *mockServiceWithProvider) Provider() string { return m.provider }

// mockServiceWithWebSearch is mockServiceWithProvider plus the optional
// ServerSideWebSearchCapable marker interface (e.g. for OpenAI Responses API).
type mockServiceWithWebSearch struct {
	mockServiceWithProvider
}

func (m *mockServiceWithWebSearch) SupportsServerSideWebSearch() bool { return true }

// mockLLMProviderWithProviders is a mock that maps model IDs to providers.
// OpenAI-flavored services are returned as mockServiceWithWebSearch so they
// satisfy ServerSideWebSearchCapable (mirroring oai.ResponsesService).
type mockLLMProviderWithProviders struct {
	providers map[string]string
}

func (m *mockLLMProviderWithProviders) GetService(modelID string) (llm.Service, error) {
	p := m.providers[modelID]
	if p == "" {
		return nil, fmt.Errorf("unknown model: %s", modelID)
	}
	if p == "openai" {
		return &mockServiceWithWebSearch{mockServiceWithProvider: mockServiceWithProvider{provider: p}}, nil
	}
	return &mockServiceWithProvider{provider: p}, nil
}

func (m *mockLLMProviderWithProviders) GetAvailableModels() []string {
	return nil
}

// plainOpenAIProvider returns a mockServiceWithProvider (no web search
// capability) reporting provider "openai".
type plainOpenAIProvider struct{}

func (p *plainOpenAIProvider) GetService(modelID string) (llm.Service, error) {
	return &mockServiceWithProvider{provider: "openai"}, nil
}
func (p *plainOpenAIProvider) GetAvailableModels() []string { return nil }

func TestNewToolSet_WebSearchForAnthropicModels(t *testing.T) {
	provider := &mockLLMProviderWithProviders{
		providers: map[string]string{
			"claude-sonnet-4.5": "anthropic",
			"claude-opus-4.6":   "anthropic",
			"claude-haiku-4.5":  "anthropic",
			"gpt-5.3-codex":     "openai",
		},
	}

	hasWebSearchToolOfType := func(ts *ToolSet, toolType string) bool {
		for _, tool := range ts.Tools() {
			if tool.Name == "web_search" && tool.Type == toolType {
				return true
			}
		}
		return false
	}
	hasWebSearchTool := func(ts *ToolSet) bool {
		for _, tool := range ts.Tools() {
			if tool.Name == "web_search" {
				return true
			}
		}
		return false
	}

	// Anthropic models should have the Anthropic-flavored web_search tool
	for _, modelID := range []string{"claude-sonnet-4.5", "claude-opus-4.6", "claude-haiku-4.5"} {
		t.Run(modelID+" has web_search", func(t *testing.T) {
			cfg := ToolSetConfig{
				LLMProvider: provider,
				ModelID:     modelID,
				WorkingDir:  "/test",
			}
			ts := NewToolSet(context.Background(), cfg)
			if !hasWebSearchToolOfType(ts, "web_search_20250305") {
				t.Errorf("expected anthropic web_search tool for %s", modelID)
			}
		})
	}

	// OpenAI models should have the OpenAI-flavored web_search tool (only
	// when the service is the Responses-API-backed one).
	t.Run("openai responses has web_search", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider: provider,
			ModelID:     "gpt-5.3-codex",
			WorkingDir:  "/test",
		}
		ts := NewToolSet(context.Background(), cfg)
		if !hasWebSearchToolOfType(ts, "web_search") {
			t.Error("expected web_search tool for OpenAI Responses model")
		}
	})

	// OpenAI-compatible Chat Completions services (which don't support web
	// search) should NOT get a web_search tool.
	t.Run("openai chat-completions service has no web_search", func(t *testing.T) {
		// Build a provider that returns a plain openai service WITHOUT the
		// ServerSideWebSearchCapable marker interface.
		plainProvider := &plainOpenAIProvider{}
		cfg := ToolSetConfig{
			LLMProvider: plainProvider,
			ModelID:     "openai-chat",
			WorkingDir:  "/test",
		}
		ts := NewToolSet(context.Background(), cfg)
		if hasWebSearchTool(ts) {
			t.Error("expected no web_search tool for a chat-completions openai service")
		}
	})

	// Unknown model should NOT have web_search tool
	t.Run("unknown model has no web_search", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider: provider,
			ModelID:     "unknown-model",
			WorkingDir:  "/test",
		}
		ts := NewToolSet(context.Background(), cfg)
		if hasWebSearchTool(ts) {
			t.Error("expected no web_search tool for unknown model")
		}
	})

	// Empty model should NOT have web_search tool
	t.Run("empty model has no web_search", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider: provider,
			ModelID:     "",
			WorkingDir:  "/test",
		}
		ts := NewToolSet(context.Background(), cfg)
		if hasWebSearchTool(ts) {
			t.Error("expected no web_search tool for empty model ID")
		}
	})

	// Nil LLMProvider should NOT have web_search tool
	t.Run("nil provider has no web_search", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider: nil,
			ModelID:     "claude-sonnet-4.5",
			WorkingDir:  "/test",
		}
		ts := NewToolSet(context.Background(), cfg)
		if hasWebSearchTool(ts) {
			t.Error("expected no web_search tool with nil provider")
		}
	})

	// Server-side tool should have no Run function, no InputSchema, no Description
	t.Run("web_search tool properties", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider: provider,
			ModelID:     "claude-sonnet-4.5",
			WorkingDir:  "/test",
		}
		ts := NewToolSet(context.Background(), cfg)
		for _, tool := range ts.Tools() {
			if tool.Name == "web_search" {
				if tool.Run != nil {
					t.Error("server-side tool should have nil Run function")
				}
				if tool.InputSchema != nil {
					t.Error("server-side tool should have nil InputSchema")
				}
				if tool.Description != "" {
					t.Error("server-side tool should have empty Description")
				}
				if !tool.ServerSide {
					t.Error("server-side tool should have ServerSide=true")
				}
				return
			}
		}
		t.Error("web_search tool not found")
	})
}
