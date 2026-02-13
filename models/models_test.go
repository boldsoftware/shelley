package models

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"shelley.exe.dev/llm"
)

func TestAll(t *testing.T) {
	models := All()
	if len(models) == 0 {
		t.Fatal("expected at least one model")
	}

	// Verify all models have required fields
	for _, m := range models {
		if m.ID == "" {
			t.Errorf("model missing ID")
		}
		if m.Provider == "" {
			t.Errorf("model %s missing Provider", m.ID)
		}
		if m.Factory == nil {
			t.Errorf("model %s missing Factory", m.ID)
		}
	}
}

func TestByID(t *testing.T) {
	tests := []struct {
		id      string
		wantID  string
		wantNil bool
	}{
		{id: "gpt-oss-20b-fireworks", wantID: "gpt-oss-20b-fireworks", wantNil: false},
		{id: "gpt-5.2-codex", wantID: "gpt-5.2-codex", wantNil: false},
		{id: "claude-sonnet-4.5", wantID: "claude-sonnet-4.5", wantNil: false},
		{id: "claude-haiku-4.5", wantID: "claude-haiku-4.5", wantNil: false},
		{id: "claude-opus-4.5", wantID: "claude-opus-4.5", wantNil: false},
		{id: "claude-opus-4.6", wantID: "claude-opus-4.6", wantNil: false},
		{id: "nonexistent", wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			m := ByID(tt.id)
			if tt.wantNil {
				if m != nil {
					t.Errorf("ByID(%q) = %v, want nil", tt.id, m)
				}
			} else {
				if m == nil {
					t.Fatalf("ByID(%q) = nil, want non-nil", tt.id)
				}
				if m.ID != tt.wantID {
					t.Errorf("ByID(%q).ID = %q, want %q", tt.id, m.ID, tt.wantID)
				}
			}
		})
	}
}

func TestDefault(t *testing.T) {
	d := Default()
	if d.ID != "claude-opus-4.6" {
		t.Errorf("Default().ID = %q, want %q", d.ID, "claude-opus-4.6")
	}
}

func TestIDs(t *testing.T) {
	ids := IDs()
	if len(ids) == 0 {
		t.Fatal("expected at least one model ID")
	}

	// Verify all IDs are unique
	seen := make(map[string]bool)
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate model ID: %s", id)
		}
		seen[id] = true
	}
}

func TestFactory(t *testing.T) {
	// Test that we can create services with empty config (should fail for most models)
	cfg := &Config{}

	// Predictable should work without any config
	m := ByID("predictable")
	if m == nil {
		t.Fatal("predictable model not found")
	}

	svc, err := m.Factory(cfg, nil)
	if err != nil {
		t.Fatalf("predictable Factory() failed: %v", err)
	}
	if svc == nil {
		t.Fatal("predictable Factory() returned nil service")
	}
}

func TestManagerGetAvailableModelsOrder(t *testing.T) {
	// Test that GetAvailableModels returns models in consistent order
	cfg := &Config{}

	// Create manager - should only have predictable model since no API keys
	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	// Get available models multiple times
	firstCall := manager.GetAvailableModels()
	secondCall := manager.GetAvailableModels()
	thirdCall := manager.GetAvailableModels()

	// Should return at least predictable model
	if len(firstCall) == 0 {
		t.Fatal("expected at least one model")
	}

	// All calls should return identical order
	if len(firstCall) != len(secondCall) || len(firstCall) != len(thirdCall) {
		t.Errorf("calls returned different lengths: %d, %d, %d", len(firstCall), len(secondCall), len(thirdCall))
	}

	for i := range firstCall {
		if firstCall[i] != secondCall[i] {
			t.Errorf("call 1 and 2 differ at index %d: %q vs %q", i, firstCall[i], secondCall[i])
		}
		if firstCall[i] != thirdCall[i] {
			t.Errorf("call 1 and 3 differ at index %d: %q vs %q", i, firstCall[i], thirdCall[i])
		}
	}
}

func TestManagerGetAvailableModelsMatchesAllOrder(t *testing.T) {
	// Test that available models are returned in the same order as All()
	cfg := &Config{
		AnthropicAPIKey: "test-key",
		OpenAIAPIKey:    "test-key",
		GeminiAPIKey:    "test-key",
		FireworksAPIKey: "test-key",
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	available := manager.GetAvailableModels()
	all := All()

	// Build expected order from All()
	var expected []string
	for _, m := range all {
		if manager.HasModel(m.ID) {
			expected = append(expected, m.ID)
		}
	}

	// Should match
	if len(available) != len(expected) {
		t.Fatalf("available models count %d != expected count %d", len(available), len(expected))
	}

	for i := range available {
		if available[i] != expected[i] {
			t.Errorf("model at index %d: got %q, want %q", i, available[i], expected[i])
		}
	}
}

func TestLoggingService(t *testing.T) {
	// Create a mock service for testing
	mockService := &mockLLMService{}
	logger := slog.Default()

	loggingSvc := &loggingService{
		service:  mockService,
		logger:   logger,
		modelID:  "test-model",
		provider: ProviderBuiltIn,
	}

	// Test Do method
	ctx := context.Background()
	request := &llm.Request{
		Messages: []llm.Message{
			llm.UserStringMessage("Hello"),
		},
	}

	response, err := loggingSvc.Do(ctx, request)
	if err != nil {
		t.Errorf("Do returned unexpected error: %v", err)
	}

	if response == nil {
		t.Error("Do returned nil response")
	}

	// Test TokenContextWindow
	window := loggingSvc.TokenContextWindow()
	if window != mockService.TokenContextWindow() {
		t.Errorf("TokenContextWindow returned %d, expected %d", window, mockService.TokenContextWindow())
	}

	// Test MaxImageDimension
	dimension := loggingSvc.MaxImageDimension()
	if dimension != mockService.MaxImageDimension() {
		t.Errorf("MaxImageDimension returned %d, expected %d", dimension, mockService.MaxImageDimension())
	}

	// Test UseSimplifiedPatch
	useSimplified := loggingSvc.UseSimplifiedPatch()
	if useSimplified != mockService.UseSimplifiedPatch() {
		t.Errorf("UseSimplifiedPatch returned %t, expected %t", useSimplified, mockService.UseSimplifiedPatch())
	}
}

// mockLLMService implements llm.Service for testing
type mockLLMService struct {
	tokenContextWindow int
	maxImageDimension  int
	useSimplifiedPatch bool
}

func (m *mockLLMService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	return &llm.Response{
		Content: llm.TextContent("Hello, world!"),
		Usage: llm.Usage{
			InputTokens:  10,
			OutputTokens: 5,
			CostUSD:      0.001,
		},
	}, nil
}

func (m *mockLLMService) TokenContextWindow() int {
	if m.tokenContextWindow == 0 {
		return 4096
	}
	return m.tokenContextWindow
}

func (m *mockLLMService) MaxImageDimension() int {
	if m.maxImageDimension == 0 {
		return 2048
	}
	return m.maxImageDimension
}

func (m *mockLLMService) UseSimplifiedPatch() bool {
	return m.useSimplifiedPatch
}

func TestManagerGetService(t *testing.T) {
	// Test with predictable model (no API keys needed)
	cfg := &Config{}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	// Test getting predictable service (should work)
	svc, err := manager.GetService("predictable")
	if err != nil {
		t.Errorf("GetService('predictable') failed: %v", err)
	}
	if svc == nil {
		t.Error("GetService('predictable') returned nil service")
	}

	// Test getting non-existent service
	_, err = manager.GetService("non-existent-model")
	if err == nil {
		t.Error("GetService('non-existent-model') should have failed but didn't")
	}
}

func TestManagerHasModel(t *testing.T) {
	cfg := &Config{}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	// Should have predictable model
	if !manager.HasModel("predictable") {
		t.Error("HasModel('predictable') should return true")
	}

	// Should not have models requiring API keys
	if manager.HasModel("claude-opus-4.6") {
		t.Error("HasModel('claude-opus-4.6') should return false without API key")
	}

	// Should not have non-existent model
	if manager.HasModel("non-existent-model") {
		t.Error("HasModel('non-existent-model') should return false")
	}
}

func TestConfigGetURLMethods(t *testing.T) {
	// Test getGeminiURL with no gateway
	cfg := &Config{}
	if cfg.getGeminiURL() != "" {
		t.Errorf("getGeminiURL with no gateway should return empty string, got %q", cfg.getGeminiURL())
	}

	// Test getGeminiURL with gateway
	cfg.Gateway = "https://gateway.example.com"
	expected := "https://gateway.example.com/_/gateway/gemini/v1/models/generate"
	if cfg.getGeminiURL() != expected {
		t.Errorf("getGeminiURL with gateway should return %q, got %q", expected, cfg.getGeminiURL())
	}

	// Test other URL methods for completeness
	if cfg.getAnthropicURL() != "https://gateway.example.com/_/gateway/anthropic/v1/messages" {
		t.Error("getAnthropicURL did not return expected URL with gateway")
	}

	if cfg.getOpenAIURL() != "https://gateway.example.com/_/gateway/openai/v1" {
		t.Error("getOpenAIURL did not return expected URL with gateway")
	}

	if cfg.getFireworksURL() != "https://gateway.example.com/_/gateway/fireworks/inference/v1" {
		t.Error("getFireworksURL did not return expected URL with gateway")
	}
}

func TestUseSimplifiedPatch(t *testing.T) {
	// Test with a service that doesn't implement SimplifiedPatcher
	mockService := &mockLLMService{}
	logger := slog.Default()

	loggingSvc := &loggingService{
		service:  mockService,
		logger:   logger,
		modelID:  "test-model",
		provider: ProviderBuiltIn,
	}

	// Should return false since mockService doesn't implement SimplifiedPatcher
	result := loggingSvc.UseSimplifiedPatch()
	if result != false {
		t.Errorf("UseSimplifiedPatch should return false for non-SimplifiedPatcher, got %t", result)
	}

	// Test with a service that implements SimplifiedPatcher
	mockSimplifiedService := &mockSimplifiedLLMService{useSimplified: true}
	loggingSvc2 := &loggingService{
		service:  mockSimplifiedService,
		logger:   logger,
		modelID:  "test-model-2",
		provider: ProviderBuiltIn,
	}

	// Should return true since mockSimplifiedService implements SimplifiedPatcher and returns true
	result = loggingSvc2.UseSimplifiedPatch()
	if result != true {
		t.Errorf("UseSimplifiedPatch should return true for SimplifiedPatcher returning true, got %t", result)
	}
}

// mockSimplifiedLLMService implements llm.Service and llm.SimplifiedPatcher for testing
type mockSimplifiedLLMService struct {
	mockLLMService
	useSimplified bool
}

func (m *mockSimplifiedLLMService) UseSimplifiedPatch() bool {
	return m.useSimplified
}

func TestHTTPClientPassedToFactory(t *testing.T) {
	// Test that HTTP client is passed to factory and used by services
	cfg := &Config{
		AnthropicAPIKey: "test-key",
	}

	// Create a custom HTTP client
	customClient := &http.Client{}

	// Test that claude factory accepts HTTP client
	m := ByID("claude-opus-4.5")
	if m == nil {
		t.Fatal("claude-opus-4.5 model not found")
	}

	svc, err := m.Factory(cfg, customClient)
	if err != nil {
		t.Fatalf("Factory with custom HTTP client failed: %v", err)
	}
	if svc == nil {
		t.Fatal("Factory returned nil service")
	}
}

func TestGetModelSource(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		modelID string
		want    string
	}{
		{
			name:    "anthropic with env var only",
			cfg:     &Config{AnthropicAPIKey: "test-key"},
			modelID: "claude-opus-4.6",
			want:    "$ANTHROPIC_API_KEY",
		},
		{
			name:    "anthropic with gateway implicit key",
			cfg:     &Config{Gateway: "https://gateway.example.com", AnthropicAPIKey: "implicit"},
			modelID: "claude-opus-4.6",
			want:    "exe.dev gateway",
		},
		{
			name:    "anthropic with gateway but explicit key",
			cfg:     &Config{Gateway: "https://gateway.example.com", AnthropicAPIKey: "actual-key"},
			modelID: "claude-opus-4.6",
			want:    "$ANTHROPIC_API_KEY",
		},
		{
			name:    "fireworks with env var only",
			cfg:     &Config{FireworksAPIKey: "test-key"},
			modelID: "gpt-oss-20b-fireworks",
			want:    "$FIREWORKS_API_KEY",
		},
		{
			name:    "fireworks with gateway implicit key",
			cfg:     &Config{Gateway: "https://gateway.example.com", FireworksAPIKey: "implicit"},
			modelID: "gpt-oss-20b-fireworks",
			want:    "exe.dev gateway",
		},
		{
			name:    "openai with env var only",
			cfg:     &Config{OpenAIAPIKey: "test-key"},
			modelID: "gpt-5.2-codex",
			want:    "$OPENAI_API_KEY",
		},
		{
			name:    "gemini with env var only",
			cfg:     &Config{GeminiAPIKey: "test-key"},
			modelID: "gemini-3-pro",
			want:    "$GEMINI_API_KEY",
		},
		{
			name:    "predictable has no source",
			cfg:     &Config{},
			modelID: "predictable",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, err := NewManager(tt.cfg)
			if err != nil {
				t.Fatalf("NewManager failed: %v", err)
			}

			info := manager.GetModelInfo(tt.modelID)
			if info == nil {
				t.Fatalf("GetModelInfo(%q) returned nil", tt.modelID)
			}
			if info.Source != tt.want {
				t.Errorf("GetModelInfo(%q).Source = %q, want %q", tt.modelID, info.Source, tt.want)
			}
		})
	}
}

func TestGetAvailableModelsUnion(t *testing.T) {
	// Test that GetAvailableModels returns both built-in and custom models
	// This test just verifies the union behavior with built-in models only
	// (testing with custom models requires a database)
	cfg := &Config{
		AnthropicAPIKey: "test-key",
		FireworksAPIKey: "test-key",
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	models := manager.GetAvailableModels()

	// Should have anthropic models and fireworks models, plus predictable
	expectedModels := []string{"claude-opus-4.6", "claude-opus-4.5", "gpt-oss-20b-fireworks", "glm-4p6-fireworks", "claude-sonnet-4.5", "claude-haiku-4.5", "predictable"}
	for _, expected := range expectedModels {
		found := false
		for _, m := range models {
			if m == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected model %q not found in available models: %v", expected, models)
		}
	}
}
