package claudetool

import (
	"context"
	"testing"
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

	// Test with empty working dir (should default to "/")
	cfg := ToolSetConfig{
		LLMProvider: provider,
		ModelID:     "test-model",
		WorkingDir:  "",
	}

	ctx := context.Background()
	ts := NewToolSet(ctx, cfg)

	wd := ts.WorkingDir()
	if wd.Get() != "/" {
		t.Errorf("expected default working dir '/', got %q", wd.Get())
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
