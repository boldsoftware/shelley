package claudetool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestReadToolBasic(t *testing.T) {
	dir := t.TempDir()
	content := "package main\n\nfunc main() {}\n"
	writeTestFile(t, dir, "test.go", content)

	wd := &MutableWorkingDir{}
	wd.Set(dir)
	tool := &ReadTool{WorkingDir: wd}

	input := map[string]any{"path": "test.go"}
	m, _ := json.Marshal(input)
	out := tool.Run(context.Background(), m)
	if out.Error != nil {
		t.Fatalf("read failed: %v", out.Error)
	}

	text := out.LLMContent[0].Text
	if !strings.Contains(text, "1#") {
		t.Fatalf("expected hashline prefix, got:\n%s", text)
	}
	if !strings.Contains(text, "package main") {
		t.Fatalf("expected file content, got:\n%s", text)
	}
	if !strings.Contains(text, "file:") {
		t.Fatalf("expected file header, got:\n%s", text)
	}
}

func TestReadToolRange(t *testing.T) {
	dir := t.TempDir()
	content := "line1\nline2\nline3\nline4\nline5"
	writeTestFile(t, dir, "test.txt", content)

	wd := &MutableWorkingDir{}
	wd.Set(dir)
	tool := &ReadTool{WorkingDir: wd}

	input := map[string]any{"path": "test.txt", "startLine": 2, "endLine": 4}
	m, _ := json.Marshal(input)
	out := tool.Run(context.Background(), m)
	if out.Error != nil {
		t.Fatalf("read failed: %v", out.Error)
	}

	text := out.LLMContent[0].Text
	if !strings.Contains(text, "2#") {
		t.Fatalf("expected line 2 hashline, got:\n%s", text)
	}
	if !strings.Contains(text, "line2") {
		t.Fatalf("expected 'line2', got:\n%s", text)
	}
	if strings.Contains(text, "line5") {
		t.Fatalf("should not contain line5, got:\n%s", text)
	}
	if !strings.Contains(text, "lines: 2-4 of 5") {
		t.Fatalf("expected range header, got:\n%s", text)
	}
}

func TestReadToolMissingFile(t *testing.T) {
	dir := t.TempDir()
	wd := &MutableWorkingDir{}
	wd.Set(dir)
	tool := &ReadTool{WorkingDir: wd}

	input := map[string]any{"path": "nonexistent.txt"}
	m, _ := json.Marshal(input)
	out := tool.Run(context.Background(), m)
	if out.Error == nil {
		t.Fatal("expected error for missing file")
	}
}
