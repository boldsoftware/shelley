package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

// oneShotMockService returns a canned response.
type oneShotMockService struct {
	response string
	onDo     func(*llm.Request)
}

func (m *oneShotMockService) Do(_ context.Context, req *llm.Request) (*llm.Response, error) {
	if m.onDo != nil {
		m.onDo(req)
	}
	return &llm.Response{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: m.response},
		},
		Usage: llm.Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}

func (m *oneShotMockService) TokenContextWindow() int { return 100000 }
func (m *oneShotMockService) MaxImageDimension() int  { return 0 }

// oneShotMockProvider implements LLMServiceProvider with configurable services.
type oneShotMockProvider struct {
	services map[string]llm.Service
}

func (p *oneShotMockProvider) GetService(modelID string) (llm.Service, error) {
	svc, ok := p.services[modelID]
	if !ok {
		return nil, fmt.Errorf("unknown model: %s", modelID)
	}
	return svc, nil
}

func (p *oneShotMockProvider) GetAvailableModels() []string {
	var models []string
	for id := range p.services {
		models = append(models, id)
	}
	return models
}

func TestLLMOneShotShortResult(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("What is 2+2?"), 0o644)

	provider := &oneShotMockProvider{
		services: map[string]llm.Service{
			"test-model": &oneShotMockService{response: "4"},
		},
	}

	tool := &LLMOneShotTool{
		LLMProvider:     provider,
		ModelID:         "test-model",
		WorkingDir:      NewMutableWorkingDir(dir),
		AvailableModels: []AvailableModel{{ID: "test-model"}},
	}

	input, _ := json.Marshal(llmOneShotInput{PromptFile: "prompt.txt"})
	result := tool.Tool().Run(context.Background(), input)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	text := result.LLMContent[0].Text
	if !strings.HasPrefix(text, "4") {
		t.Errorf("expected result to start with '4', got: %s", text)
	}
	if !strings.Contains(text, "test-model") {
		t.Errorf("expected result to contain model name, got: %s", text)
	}
}

func TestLLMOneShotLongResult(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("Generate a long story"), 0o644)

	longResponse := strings.Repeat("word ", 1000) // ~5000 chars

	provider := &oneShotMockProvider{
		services: map[string]llm.Service{
			"test-model": &oneShotMockService{response: longResponse},
		},
	}

	tool := &LLMOneShotTool{
		LLMProvider:     provider,
		ModelID:         "test-model",
		WorkingDir:      NewMutableWorkingDir(dir),
		AvailableModels: []AvailableModel{{ID: "test-model"}},
	}

	input, _ := json.Marshal(llmOneShotInput{PromptFile: "prompt.txt"})
	result := tool.Tool().Run(context.Background(), input)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	text := result.LLMContent[0].Text
	if !strings.Contains(text, "Response written to") {
		t.Errorf("expected file output message, got: %s", text)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "llm-result-*.txt"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 result file, found %d", len(matches))
	}
	content, _ := os.ReadFile(matches[0])
	if string(content) != longResponse {
		t.Errorf("file content mismatch")
	}
}

func TestLLMOneShotExplicitOutputFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("Hello"), 0o644)

	provider := &oneShotMockProvider{
		services: map[string]llm.Service{
			"test-model": &oneShotMockService{response: "Hi"},
		},
	}

	tool := &LLMOneShotTool{
		LLMProvider:     provider,
		ModelID:         "test-model",
		WorkingDir:      NewMutableWorkingDir(dir),
		AvailableModels: []AvailableModel{{ID: "test-model"}},
	}

	input, _ := json.Marshal(llmOneShotInput{PromptFile: "prompt.txt", OutputFile: "output.txt"})
	result := tool.Tool().Run(context.Background(), input)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	outputPath := filepath.Join(dir, "output.txt")
	text := result.LLMContent[0].Text
	if !strings.Contains(text, outputPath) {
		t.Errorf("expected output path in response, got: %s", text)
	}

	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	if string(content) != "Hi" {
		t.Errorf("expected 'Hi', got: %s", string(content))
	}
}

func TestLLMOneShotAlternateModel(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("Hello"), 0o644)

	provider := &oneShotMockProvider{
		services: map[string]llm.Service{
			"default-model": &oneShotMockService{response: "from default"},
			"other-model":   &oneShotMockService{response: "from other"},
		},
	}

	tool := &LLMOneShotTool{
		LLMProvider: provider,
		ModelID:     "default-model",
		WorkingDir:  NewMutableWorkingDir(dir),
		AvailableModels: []AvailableModel{
			{ID: "default-model"},
			{ID: "other-model"},
		},
	}

	input, _ := json.Marshal(llmOneShotInput{PromptFile: "prompt.txt", Model: "other-model"})
	result := tool.Tool().Run(context.Background(), input)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	text := result.LLMContent[0].Text
	if !strings.Contains(text, "from other") {
		t.Errorf("expected 'from other', got: %s", text)
	}
	if !strings.Contains(text, "other-model") {
		t.Errorf("expected model name in usage, got: %s", text)
	}
}

func TestLLMOneShotUnknownModel(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("Hello"), 0o644)

	provider := &oneShotMockProvider{
		services: map[string]llm.Service{
			"test-model": &oneShotMockService{response: "ok"},
		},
	}

	tool := &LLMOneShotTool{
		LLMProvider:     provider,
		ModelID:         "test-model",
		WorkingDir:      NewMutableWorkingDir(dir),
		AvailableModels: []AvailableModel{{ID: "test-model"}},
	}

	input, _ := json.Marshal(llmOneShotInput{PromptFile: "prompt.txt", Model: "bogus-model"})
	result := tool.Tool().Run(context.Background(), input)

	if result.Error == nil {
		t.Fatal("expected error for unknown model")
	}
	if !strings.Contains(result.Error.Error(), "unknown model") {
		t.Errorf("expected unknown model error, got: %v", result.Error)
	}
}

func TestLLMOneShotMissingFile(t *testing.T) {
	dir := t.TempDir()

	provider := &oneShotMockProvider{
		services: map[string]llm.Service{
			"test-model": &oneShotMockService{response: "ok"},
		},
	}

	tool := &LLMOneShotTool{
		LLMProvider:     provider,
		ModelID:         "test-model",
		WorkingDir:      NewMutableWorkingDir(dir),
		AvailableModels: []AvailableModel{{ID: "test-model"}},
	}

	input, _ := json.Marshal(llmOneShotInput{PromptFile: "nonexistent.txt"})
	result := tool.Tool().Run(context.Background(), input)

	if result.Error == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(result.Error.Error(), "failed to read prompt file") {
		t.Errorf("expected read error, got: %v", result.Error)
	}
}

func TestLLMOneShotEmptyPrompt(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("  \n  "), 0o644)

	provider := &oneShotMockProvider{
		services: map[string]llm.Service{
			"test-model": &oneShotMockService{response: "ok"},
		},
	}

	tool := &LLMOneShotTool{
		LLMProvider:     provider,
		ModelID:         "test-model",
		WorkingDir:      NewMutableWorkingDir(dir),
		AvailableModels: []AvailableModel{{ID: "test-model"}},
	}

	input, _ := json.Marshal(llmOneShotInput{PromptFile: "prompt.txt"})
	result := tool.Tool().Run(context.Background(), input)

	if result.Error == nil {
		t.Fatal("expected error for empty prompt")
	}
	if !strings.Contains(result.Error.Error(), "prompt file is empty") {
		t.Errorf("expected empty prompt error, got: %v", result.Error)
	}
}

func TestLLMOneShotToolDescription(t *testing.T) {
	tool := &LLMOneShotTool{
		LLMProvider: &oneShotMockProvider{},
		ModelID:     "model-a",
		WorkingDir:  NewMutableWorkingDir("/tmp"),
		AvailableModels: []AvailableModel{
			{ID: "model-a"},
			{ID: "model-b", DisplayName: "Model B (fancy)"},
		},
	}

	llmTool := tool.Tool()
	if !strings.Contains(llmTool.Description, "- model-a") {
		t.Errorf("expected model-a in description, got: %s", llmTool.Description)
	}
	if !strings.Contains(llmTool.Description, "- model-b (Model B (fancy))") {
		t.Errorf("expected model-b with display name in description, got: %s", llmTool.Description)
	}
}

func TestLLMOneShotToolSchemaEnum(t *testing.T) {
	tool := &LLMOneShotTool{
		LLMProvider: &oneShotMockProvider{},
		ModelID:     "model-a",
		WorkingDir:  NewMutableWorkingDir("/tmp"),
		AvailableModels: []AvailableModel{
			{ID: "model-a"},
			{ID: "model-b"},
		},
	}

	llmTool := tool.Tool()
	schema := string(llmTool.InputSchema)
	if !strings.Contains(schema, `"enum"`) {
		t.Errorf("expected enum in schema, got: %s", schema)
	}
	if !strings.Contains(schema, `"model-a"`) || !strings.Contains(schema, `"model-b"`) {
		t.Errorf("expected model IDs in enum, got: %s", schema)
	}
}

func TestLLMOneShotToolSchemaNoEnum(t *testing.T) {
	tool := &LLMOneShotTool{
		LLMProvider: &oneShotMockProvider{},
		ModelID:     "model-a",
		WorkingDir:  NewMutableWorkingDir("/tmp"),
	}

	llmTool := tool.Tool()
	schema := string(llmTool.InputSchema)
	if strings.Contains(schema, `"enum"`) {
		t.Errorf("expected no enum in schema when no available models, got: %s", schema)
	}
	if strings.Contains(schema, `"model"`) {
		t.Errorf("expected no model property when no available models, got: %s", schema)
	}
}

func TestLLMOneShotSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("Hello"), 0o644)

	var capturedReq *llm.Request
	svc := &oneShotMockService{
		response: "response",
		onDo: func(req *llm.Request) {
			capturedReq = req
		},
	}

	provider := &oneShotMockProvider{
		services: map[string]llm.Service{"test-model": svc},
	}

	tool := &LLMOneShotTool{
		LLMProvider:     provider,
		ModelID:         "test-model",
		WorkingDir:      NewMutableWorkingDir(dir),
		AvailableModels: []AvailableModel{{ID: "test-model"}},
	}

	input, _ := json.Marshal(llmOneShotInput{PromptFile: "prompt.txt", SystemPrompt: "You are a pirate."})
	result := tool.Tool().Run(context.Background(), input)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if capturedReq == nil {
		t.Fatal("request not captured")
	}
	if len(capturedReq.System) != 1 || capturedReq.System[0].Text != "You are a pirate." {
		t.Errorf("expected system prompt, got: %+v", capturedReq.System)
	}
}
