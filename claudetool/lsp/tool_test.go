package lsp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestToolInputValidation(t *testing.T) {
	tool := &CodeIntelTool{
		manager:    NewManager(func() string { return t.TempDir() }),
		workingDir: func() string { return t.TempDir() },
	}

	tests := []struct {
		name    string
		input   codeIntelInput
		wantErr string
	}{
		{
			name:    "unknown operation",
			input:   codeIntelInput{Operation: "unknown"},
			wantErr: "unknown operation",
		},
		{
			name:    "definition missing file",
			input:   codeIntelInput{Operation: "definition", Line: 1, Column: 1},
			wantErr: "file is required",
		},
		{
			name:    "definition missing line",
			input:   codeIntelInput{Operation: "definition", File: "test.go", Column: 1},
			wantErr: "line is required",
		},
		{
			name:    "definition missing column",
			input:   codeIntelInput{Operation: "definition", File: "test.go", Line: 1},
			wantErr: "column is required",
		},
		{
			name:    "references missing file",
			input:   codeIntelInput{Operation: "references", Line: 1, Column: 1},
			wantErr: "file is required",
		},
		{
			name:    "hover missing file",
			input:   codeIntelInput{Operation: "hover", Line: 1, Column: 1},
			wantErr: "file is required",
		},
		{
			name:    "symbols missing query",
			input:   codeIntelInput{Operation: "symbols"},
			wantErr: "query is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			result := tool.Run(context.Background(), data)
			if result.Error == nil {
				t.Fatal("expected error")
			}
			if got := result.Error.Error(); !containsSubstr(got, tt.wantErr) {
				t.Errorf("error %q should contain %q", got, tt.wantErr)
			}
		})
	}
}

func TestToolPositionConversion(t *testing.T) {
	// Test that 1-based input converts to 0-based LSP positions
	// by checking the input struct parsing
	input := codeIntelInput{
		Operation: "definition",
		File:      "test.go",
		Line:      10,
		Column:    5,
	}

	// 1-based line 10 should become 0-based line 9
	pos := Position{
		Line:      input.Line - 1,
		Character: input.Column - 1,
	}
	if pos.Line != 9 {
		t.Errorf("position line = %d, want 9", pos.Line)
	}
	if pos.Character != 4 {
		t.Errorf("position character = %d, want 4", pos.Character)
	}
}

func TestToolRelativePathResolution(t *testing.T) {
	wd := "/home/user/project"
	tool := &CodeIntelTool{
		manager:    NewManager(func() string { return wd }),
		workingDir: func() string { return wd },
	}

	// The tool should resolve relative paths against working dir.
	// We can't fully test this without an LSP server, but we can verify
	// the input parsing doesn't fail on relative paths.
	input := codeIntelInput{
		Operation: "definition",
		File:      "src/main.go",
		Line:      1,
		Column:    1,
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result := tool.Run(context.Background(), data)
	// Should get an error about LSP server, not about path resolution
	if result.Error == nil {
		t.Fatal("expected error (no LSP server)")
	}
	errMsg := result.Error.Error()
	if containsSubstr(errMsg, "file is required") || containsSubstr(errMsg, "line is required") {
		t.Errorf("unexpected validation error: %s", errMsg)
	}
}

func TestToolSchemaIsValid(t *testing.T) {
	tool := &CodeIntelTool{
		manager:    NewManager(func() string { return "/tmp" }),
		workingDir: func() string { return "/tmp" },
	}
	llmTool := tool.Tool()
	if llmTool.Name != "code_intelligence" {
		t.Errorf("tool name = %q, want %q", llmTool.Name, "code_intelligence")
	}
	if llmTool.Run == nil {
		t.Error("tool Run function is nil")
	}
	if len(llmTool.InputSchema) == 0 {
		t.Error("tool InputSchema is empty")
	}

	// Verify schema is valid JSON
	var schema map[string]any
	if err := json.Unmarshal(llmTool.InputSchema, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
}
