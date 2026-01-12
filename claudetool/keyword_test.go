package claudetool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"shelley.exe.dev/llm"
)

// Mock LLM provider for testing
type mockLLMProvider struct{}

type mockService struct{}

func (m *mockService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: llm.TextContent("test response")}, nil
}

func (m *mockService) TokenContextWindow() int {
	return 4096
}

func (m *mockService) MaxImageDimension() int {
	return 0
}

func (m *mockLLMProvider) GetService(modelID string) (llm.Service, error) {
	return &mockService{}, nil
}

func (m *mockLLMProvider) GetAvailableModels() []string {
	return []string{"test-model"}
}

func TestNewKeywordTool(t *testing.T) {
	provider := &mockLLMProvider{}
	tool := NewKeywordTool(provider)

	if tool == nil {
		t.Fatal("NewKeywordTool returned nil")
	}
}

func TestNewKeywordToolWithWorkingDir(t *testing.T) {
	provider := &mockLLMProvider{}
	wd := NewMutableWorkingDir("/test")
	tool := NewKeywordToolWithWorkingDir(provider, wd)

	if tool == nil {
		t.Fatal("NewKeywordToolWithWorkingDir returned nil")
	}

	if tool.workingDir != wd {
		t.Error("workingDir not set correctly")
	}
}

func TestKeywordTool_Tool(t *testing.T) {
	provider := &mockLLMProvider{}
	keywordTool := NewKeywordTool(provider)
	tool := keywordTool.Tool()

	if tool == nil {
		t.Fatal("Tool() returned nil")
	}

	if tool.Name != keywordName {
		t.Errorf("expected name %q, got %q", keywordName, tool.Name)
	}

	if tool.Description != keywordDescription {
		t.Errorf("expected description %q, got %q", keywordDescription, tool.Description)
	}

	if tool.Run == nil {
		t.Error("Run function not set")
	}
}

func TestFindRepoRoot(t *testing.T) {
	// Create a temp directory structure
	tmpDir := t.TempDir()

	// Test when not in a git repo (should fail)
	_, err := FindRepoRoot(tmpDir)
	if err == nil {
		t.Error("expected error when not in git repo")
	}

	// Initialize a git repo properly
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Skip("git not available, skipping test")
	}

	// Test when in a git repo (should succeed)
	root, err := FindRepoRoot(tmpDir)
	if err != nil {
		t.Errorf("unexpected error when in git repo: %v", err)
	}

	if root != tmpDir {
		t.Errorf("expected root %q, got %q", tmpDir, root)
	}
}

func TestKeywordRun(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed, skipping test")
	}

	// Create a temp directory with some files
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "This is a test file with some content for keyword search testing."
	if err := os.WriteFile(testFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := &mockLLMProvider{}
	wd := NewMutableWorkingDir(tmpDir)
	keywordTool := NewKeywordToolWithWorkingDir(provider, wd)

	// Test with valid input
	input := keywordInput{
		Query:       "what files exist in this project",
		SearchTerms: []string{"test", "file"},
	}
	inputBytes, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	result := keywordTool.keywordRun(context.Background(), inputBytes)

	if result.Error != nil {
		t.Errorf("unexpected error: %v", result.Error)
	}

	if len(result.LLMContent) == 0 {
		t.Error("expected LLM content")
	}
}
