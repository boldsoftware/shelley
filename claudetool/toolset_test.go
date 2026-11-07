package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/oai"
)

// Mock LLM provider for testing
type mockLLMProvider struct{}

type mockService struct{}

func (m *mockService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: llm.TextContent("test response")}, nil
}

func (m *mockService) Provider() string { return "" }

func (m *mockService) MaxImageDimension() int {
	return 0
}

func (m *mockService) MaxImageBytes() int {
	return 0
}

func (m *mockLLMProvider) GetService(modelID string) (llm.Service, error) {
	return &mockService{}, nil
}

func (m *mockLLMProvider) GetAvailableModels() []string {
	return []string{"test-model"}
}

func (m *mockLLMProvider) GetWorkhorseService(modelID string) (llm.Service, error) {
	return m.GetService(modelID)
}

func (m *mockService) SupportsImages() bool { return true }

func TestNewToolSet_DefaultWorkingDir(t *testing.T) {
	provider := &mockLLMProvider{}

	// Test with empty working dir (should default to $HOME)
	cfg := ToolSetConfig{
		LLMProvider: provider,
		ModelID:     "test-model",
		WorkingDir:  "",
	}

	ctx := t.Context()
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
		ts := NewToolSet(t.Context(), cfg)
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
		ts := NewToolSet(t.Context(), cfg)
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
		ts := NewToolSet(t.Context(), cfg)
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
		ts := NewToolSet(t.Context(), cfg)
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
		ts := NewToolSet(t.Context(), cfg)
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
		ts := NewToolSet(t.Context(), cfg)
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
		ts := NewToolSet(t.Context(), cfg)
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
	ts := NewToolSet(t.Context(), cfg)
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
	noBrowserTS := NewToolSet(t.Context(), noBrowserCfg)
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
	noSubagentTS := NewToolSet(t.Context(), noSubagentCfg)
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

	models := []AvailableModel{{Name: "model-a", ID: "model-a"}}
	calls := 0
	cfg := ToolSetConfig{
		LLMProvider:          provider,
		ModelID:              "test-model",
		WorkingDir:           "/test",
		SubagentRunner:       runner,
		SubagentDB:           db,
		ParentConversationID: "parent",
		BuildAvailableModels: func(parentModelID string) []AvailableModel {
			calls++
			if parentModelID != "test-model" {
				t.Fatalf("BuildAvailableModels parent model = %q, want test-model", parentModelID)
			}
			out := make([]AvailableModel, len(models))
			copy(out, models)
			return out
		},
	}

	findSubagentSchema := func(ts *ToolSet) string {
		for _, tool := range ts.Tools() {
			if tool.Name == "subagent" {
				return string(tool.InputSchema)
			}
		}
		return ""
	}

	ts1 := NewToolSet(t.Context(), cfg)
	schema1 := findSubagentSchema(ts1)
	if schema1 == "" {
		t.Fatal("expected subagent tool in first ToolSet")
	}
	if !strings.Contains(schema1, "model-a") {
		t.Errorf("expected first schema to include model-a, got: %s", schema1)
	}

	// Simulate a custom model being added at runtime.
	models = append(models, AvailableModel{Name: "model-b", ID: "model-b"})

	ts2 := NewToolSet(t.Context(), cfg)
	schema2 := findSubagentSchema(ts2)
	if schema2 == "" {
		t.Fatal("expected subagent tool in second ToolSet")
	}
	if !strings.Contains(schema2, "model-b") {
		t.Errorf("expected second schema to include model-b, got: %s", schema2)
	}
	if calls != 2 {
		t.Errorf("expected BuildAvailableModels to be invoked once per ToolSet, got %d calls", calls)
	}

	// When BuildAvailableModels is nil, fall back to LLMProvider.GetAvailableModels.
	cfgNoBuilder := cfg
	cfgNoBuilder.BuildAvailableModels = nil
	ts3 := NewToolSet(t.Context(), cfgNoBuilder)
	schema3 := findSubagentSchema(ts3)
	if schema3 == "" {
		t.Fatal("expected subagent tool when falling back to LLMProvider")
	}
	if !strings.Contains(schema3, "test-model") {
		t.Errorf("expected fallback schema to include the provider's model, got: %s", schema3)
	}
}

func TestNewToolSetUsesParentAwareModelChoices(t *testing.T) {
	cfg := ToolSetConfig{
		LLMProvider:          &mockLLMProvider{},
		ModelID:              "parent-model",
		WorkingDir:           "/test",
		SubagentRunner:       &mockSubagentRunner{},
		SubagentDB:           &mockSubagentDB{},
		ParentConversationID: "parent",
		BuildAvailableModels: func(parentModelID string) []AvailableModel {
			if parentModelID != "parent-model" {
				t.Fatalf("BuildAvailableModels parent model = %q, want parent-model", parentModelID)
			}
			return []AvailableModel{{Name: "parent-route-model", ID: "parent-route-model"}}
		},
	}

	ts := NewToolSet(t.Context(), cfg)
	schemas := map[string]string{}
	for _, tool := range ts.Tools() {
		schemas[tool.Name] = string(tool.InputSchema)
	}
	if !strings.Contains(schemas["subagent"], "parent-route-model") {
		t.Errorf("subagent schema = %q, want parent-route-model", schemas["subagent"])
	}
	if !strings.Contains(schemas["llm_one_shot"], "parent-route-model") {
		t.Errorf("llm_one_shot schema = %q, want parent-route-model", schemas["llm_one_shot"])
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
// Both anthropic and OpenAI-flavored services are returned as
// mockServiceWithWebSearch so they satisfy ServerSideWebSearchCapable
// (mirroring genuine Claude models and oai.ResponsesService).
type mockLLMProviderWithProviders struct {
	providers map[string]string
}

func (m *mockLLMProviderWithProviders) GetService(modelID string) (llm.Service, error) {
	p := m.providers[modelID]
	if p == "" {
		return nil, fmt.Errorf("unknown model: %s", modelID)
	}
	if p == "openai" || p == "anthropic" {
		return &mockServiceWithWebSearch{mockServiceWithProvider: mockServiceWithProvider{provider: p}}, nil
	}
	return &mockServiceWithProvider{provider: p}, nil
}

func (m *mockLLMProviderWithProviders) GetAvailableModels() []string {
	return nil
}

func (m *mockLLMProviderWithProviders) GetWorkhorseService(modelID string) (llm.Service, error) {
	return m.GetService(modelID)
}

// plainOpenAIProvider returns a mockServiceWithProvider (no web search
// capability) reporting provider "openai".
type plainOpenAIProvider struct{}

func (p *plainOpenAIProvider) GetService(modelID string) (llm.Service, error) {
	return &mockServiceWithProvider{provider: "openai"}, nil
}
func (p *plainOpenAIProvider) GetAvailableModels() []string { return nil }
func (p *plainOpenAIProvider) GetWorkhorseService(modelID string) (llm.Service, error) {
	return p.GetService(modelID)
}

// plainAnthropicProvider returns a mockServiceWithProvider (no web search
// capability) reporting provider "anthropic". This mirrors a non-Claude model
// reached over the Anthropic Messages wire protocol (e.g. a third-party model
// an LLM integration serves via anthropic_messages): it reports provider
// "anthropic" but cannot run the Anthropic server-side web_search tool.
type plainAnthropicProvider struct{}

func (p *plainAnthropicProvider) GetService(modelID string) (llm.Service, error) {
	return &mockServiceWithProvider{provider: "anthropic"}, nil
}
func (p *plainAnthropicProvider) GetAvailableModels() []string { return nil }
func (p *plainAnthropicProvider) GetWorkhorseService(modelID string) (llm.Service, error) {
	return p.GetService(modelID)
}

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
			ts := NewToolSet(t.Context(), cfg)
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
		ts := NewToolSet(t.Context(), cfg)
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
		ts := NewToolSet(t.Context(), cfg)
		if hasWebSearchTool(ts) {
			t.Error("expected no web_search tool for a chat-completions openai service")
		}
	})

	// A non-Claude model reached over the Anthropic Messages wire protocol
	// (e.g. a third-party model an LLM integration serves via anthropic_messages)
	// reports provider "anthropic" but cannot run the Anthropic server-side
	// web_search tool. Sending it would produce a 400 Bad Request (issue #242),
	// so it must NOT get a web_search tool.
	t.Run("anthropic-protocol non-claude service has no web_search", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider: &plainAnthropicProvider{},
			ModelID:     "third-party-model",
			WorkingDir:  "/test",
		}
		ts := NewToolSet(t.Context(), cfg)
		if hasWebSearchTool(ts) {
			t.Error("expected no web_search tool for a non-Claude anthropic-protocol service")
		}
	})

	// Unknown model should NOT have web_search tool
	t.Run("unknown model has no web_search", func(t *testing.T) {
		cfg := ToolSetConfig{
			LLMProvider: provider,
			ModelID:     "unknown-model",
			WorkingDir:  "/test",
		}
		ts := NewToolSet(t.Context(), cfg)
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
		ts := NewToolSet(t.Context(), cfg)
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
		ts := NewToolSet(t.Context(), cfg)
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
		ts := NewToolSet(t.Context(), cfg)
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

type rawPatchService struct{ mockService }

func (*rawPatchService) Provider() string     { return "openai" }
func (*rawPatchService) PatchProfile() string { return "codex_apply_patch" }

type rawPatchProvider struct{}

func (*rawPatchProvider) GetService(string) (llm.Service, error) { return &rawPatchService{}, nil }
func (*rawPatchProvider) GetAvailableModels() []string           { return []string{"test"} }
func (p *rawPatchProvider) GetWorkhorseService(modelID string) (llm.Service, error) {
	return p.GetService(modelID)
}

type patchProfileProvider struct {
	mockLLMProvider
	service llm.Service
}

func (p *patchProfileProvider) GetService(string) (llm.Service, error) {
	return p.service, nil
}

func TestNewToolSetPatchStrategy(t *testing.T) {
	for _, tt := range []struct {
		name, want string
		service    llm.Service
		simple     bool
	}{
		{name: "native apply_patch", service: &oai.ResponsesService{ProviderName: "openai", Model: oai.GPT6Sol}, want: "apply_patch"},
		{name: "GLM 5.3", service: &oai.Service{Model: oai.GLM53Fireworks}, want: "patch", simple: true},
		{name: "GLM flash", service: &oai.Service{Model: oai.GLM53FlashFireworks}, want: "patch", simple: true},
		{name: "GLM responses", service: &oai.ResponsesService{ProviderName: "fireworks", Model: oai.GLM53Fireworks}, want: "patch", simple: true},
		{name: "GLM messages", service: &ant.Service{Model: "z-ai/GLM-5.3"}, want: "patch", simple: true},
		{name: "Claude", service: &ant.Service{Model: ant.Claude55Sonnet}, want: "patch"},
		{name: "other chat model", service: &oai.Service{Model: oai.Model{ModelName: "deepseek-v4"}}, want: "patch"},
		{name: "other responses model", service: &oai.ResponsesService{Model: oai.Model{ModelName: "other"}}, want: "patch"},
		{name: "unknown service", service: &mockService{}, want: "patch"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ts := NewToolSet(t.Context(), ToolSetConfig{
				LLMProvider: &patchProfileProvider{service: tt.service},
				ModelID:     "custom-alias",
			})
			defer ts.Cleanup()
			var patch *llm.Tool
			for _, tool := range ts.Tools() {
				if tool.Name == "patch" || tool.Name == "apply_patch" {
					if patch != nil {
						t.Fatal("multiple patch tools exposed")
					}
					patch = tool
				}
			}
			if patch == nil || patch.Name != tt.want {
				t.Fatalf("patch tool = %+v, want %q", patch, tt.want)
			}
			if patch.Name == "patch" {
				var schema struct {
					Properties map[string]json.RawMessage `json:"properties"`
				}
				if err := json.Unmarshal(patch.InputSchema, &schema); err != nil {
					t.Fatal(err)
				}
				_, hasEdits := schema.Properties["edits"]
				if hasEdits != tt.simple {
					t.Fatalf("edits present = %v, want %v", hasEdits, tt.simple)
				}
				if _, hasPatches := schema.Properties["patches"]; hasPatches == tt.simple {
					t.Fatalf("patches present = %v, want %v", hasPatches, !tt.simple)
				}
			}
		})
	}
}

func TestNewToolSetPatchStrategyUnsupportedService(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provider LLMServiceProvider
	}{
		{name: "no provider"},
		{name: "unknown service", provider: &mockLLMProvider{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ts := NewToolSet(t.Context(), ToolSetConfig{
				LLMProvider: tt.provider,
				ModelID:     "glm-looking-alias",
			})
			defer ts.Cleanup()
			for _, tool := range ts.Tools() {
				if tool.Name == "apply_patch" {
					t.Fatal("unsupported service received apply_patch")
				}
				if tool.Name == "patch" {
					var schema struct {
						Properties map[string]json.RawMessage `json:"properties"`
					}
					if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
						t.Fatal(err)
					}
					if _, ok := schema.Properties["patches"]; !ok {
						t.Fatal("unsupported service missing nested patch schema")
					}
					return
				}
			}
			t.Fatal("patch tool not found")
		})
	}
}

func TestNewToolSetApplyPatchRespectsOverrides(t *testing.T) {
	for _, tt := range []struct {
		name       string
		overrides  map[string]string
		disableAll bool
		want       bool
	}{
		{name: "patch off", overrides: map[string]string{"patch": "off"}},
		{name: "disable all", disableAll: true},
		{name: "patch on overrides disable all", overrides: map[string]string{"patch": "on"}, disableAll: true, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ts := NewToolSet(t.Context(), ToolSetConfig{
				LLMProvider:     &rawPatchProvider{},
				ModelID:         "test",
				ToolOverrides:   tt.overrides,
				DisableAllTools: tt.disableAll,
			})
			defer ts.Cleanup()
			for _, tool := range ts.Tools() {
				if tool.Name == "apply_patch" {
					if !tt.want {
						t.Fatal("apply_patch exposed despite patch being disabled")
					}
					return
				}
			}
			if tt.want {
				t.Fatal("apply_patch missing despite patch being enabled")
			}
		})
	}
}
