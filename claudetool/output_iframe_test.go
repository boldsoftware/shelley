package claudetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutputIframeRun(t *testing.T) {
	// Create a temp directory for test files
	tmpDir := t.TempDir()

	// Create test HTML files
	htmlFile := filepath.Join(tmpDir, "test.html")
	if err := os.WriteFile(htmlFile, []byte("<html><head></head><body><h1>Hello</h1></body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	chartFile := filepath.Join(tmpDir, "chart.html")
	if err := os.WriteFile(chartFile, []byte("<html><head></head><body><div>Chart</div></body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a data file
	dataFile := filepath.Join(tmpDir, "data.json")
	if err := os.WriteFile(dataFile, []byte(`{"values": [1, 2, 3]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a CSS file
	cssFile := filepath.Join(tmpDir, "styles.css")
	if err := os.WriteFile(cssFile, []byte("body { color: red; }"), 0o644); err != nil {
		t.Fatal(err)
	}

	workingDir := &MutableWorkingDir{}
	workingDir.Set(tmpDir)

	tool := &OutputIframeTool{WorkingDir: workingDir}

	tests := []struct {
		name         string
		input        map[string]any
		wantErr      bool
		wantTitle    string
		wantFilename string
		wantFiles    int
		checkHTML    func(html string) bool
	}{
		{
			name: "basic html file",
			input: map[string]any{
				"path": "test.html",
			},
			wantErr:      false,
			wantTitle:    "",
			wantFilename: "test.html",
			wantFiles:    0,
		},
		{
			name: "html with title",
			input: map[string]any{
				"path":  "chart.html",
				"title": "My Chart",
			},
			wantErr:      false,
			wantTitle:    "My Chart",
			wantFilename: "chart.html",
			wantFiles:    0,
		},
		{
			name: "html with data file",
			input: map[string]any{
				"path":  "chart.html",
				"title": "Chart with Data",
				"files": map[string]any{
					"data.json": "data.json",
				},
			},
			wantErr:      false,
			wantTitle:    "Chart with Data",
			wantFilename: "chart.html",
			wantFiles:    1,
			checkHTML: func(html string) bool {
				return strings.Contains(html, "window.__FILES__") &&
					strings.Contains(html, "data.json")
			},
		},
		{
			name: "html with multiple files",
			input: map[string]any{
				"path":  "chart.html",
				"title": "Styled Chart",
				"files": map[string]any{
					"data.json":  "data.json",
					"styles.css": "styles.css",
				},
			},
			wantErr:      false,
			wantTitle:    "Styled Chart",
			wantFilename: "chart.html",
			wantFiles:    2,
			checkHTML: func(html string) bool {
				return strings.Contains(html, "window.__FILES__") &&
					strings.Contains(html, "<style data-file=\"styles.css\">") &&
					strings.Contains(html, "body { color: red; }")
			},
		},
		{
			name: "absolute path",
			input: map[string]any{
				"path": htmlFile,
			},
			wantErr:      false,
			wantFilename: "test.html",
			wantFiles:    0,
		},
		{
			name: "empty path",
			input: map[string]any{
				"path": "",
			},
			wantErr: true,
		},
		{
			name:    "missing path",
			input:   map[string]any{},
			wantErr: true,
		},
		{
			name: "nonexistent file",
			input: map[string]any{
				"path": "nonexistent.html",
			},
			wantErr: true,
		},
		{
			name: "nonexistent data file",
			input: map[string]any{
				"path": "chart.html",
				"files": map[string]any{
					"missing.json": "missing.json",
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("failed to marshal input: %v", err)
			}

			result := tool.Tool().Run(context.Background(), inputJSON)

			if tt.wantErr {
				if result.Error == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if result.Error != nil {
				t.Errorf("unexpected error: %v", result.Error)
				return
			}

			if len(result.LLMContent) != 1 || result.LLMContent[0].Text != "displayed" {
				t.Errorf("expected LLMContent [displayed], got %v", result.LLMContent)
			}

			display, ok := result.Display.(OutputIframeDisplay)
			if !ok {
				t.Errorf("expected Display to be OutputIframeDisplay, got %T", result.Display)
				return
			}

			if display.Type != "output_iframe" {
				t.Errorf("expected Type 'output_iframe', got %q", display.Type)
			}

			if display.Title != tt.wantTitle {
				t.Errorf("expected Title %q, got %q", tt.wantTitle, display.Title)
			}

			if display.Filename != tt.wantFilename {
				t.Errorf("expected Filename %q, got %q", tt.wantFilename, display.Filename)
			}

			if len(display.Files) != tt.wantFiles {
				t.Errorf("expected %d files, got %d", tt.wantFiles, len(display.Files))
			}

			if tt.checkHTML != nil && !tt.checkHTML(display.HTML) {
				t.Errorf("HTML check failed, got: %s", display.HTML)
			}
		})
	}
}

func TestDetectFileType(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"data.json", "json"},
		{"styles.css", "css"},
		{"script.js", "js"},
		{"data.csv", "csv"},
		{"readme.txt", "text"},
		{"unknown", "text"},
		{"DATA.JSON", "json"}, // case insensitive
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := detectFileType(tt.filename)
			if got != tt.want {
				t.Errorf("detectFileType(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestEscapeJSString(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"hello\nworld", "hello\\nworld"},
		{`say "hi"`, `say \"hi\"`},
		{"back\\slash", "back\\\\slash"},
		{"tab\there", "tab\\there"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeJSString(tt.input)
			if got != tt.want {
				t.Errorf("escapeJSString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestInjectFiles(t *testing.T) {
	tests := []struct {
		name     string
		html     string
		files    []EmbeddedFile
		contains []string
	}{
		{
			name:     "no files",
			html:     "<html><head></head><body></body></html>",
			files:    nil,
			contains: []string{"<html><head></head><body></body></html>"},
		},
		{
			name: "inject json file",
			html: "<html><head></head><body></body></html>",
			files: []EmbeddedFile{
				{Name: "data.json", Content: `{"x": 1}`, Type: "json"},
			},
			contains: []string{"window.__FILES__", "data.json"},
		},
		{
			name: "inject css file",
			html: "<html><head></head><body></body></html>",
			files: []EmbeddedFile{
				{Name: "styles.css", Content: "body { color: red; }", Type: "css"},
			},
			contains: []string{"<style data-file=\"styles.css\">", "body { color: red; }"},
		},
		{
			name: "inject js file",
			html: "<html><head></head><body></body></html>",
			files: []EmbeddedFile{
				{Name: "app.js", Content: "console.log('hi');", Type: "js"},
			},
			contains: []string{"<script data-file=\"app.js\">", "console.log('hi');"},
		},
		{
			name: "html without head tag",
			html: "<html><body>content</body></html>",
			files: []EmbeddedFile{
				{Name: "data.json", Content: `{}`, Type: "json"},
			},
			contains: []string{"<head>", "</head>", "window.__FILES__"},
		},
		{
			name: "plain html without tags",
			html: "<div>content</div>",
			files: []EmbeddedFile{
				{Name: "data.json", Content: `{}`, Type: "json"},
			},
			contains: []string{"window.__FILES__", "<div>content</div>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := injectFiles(tt.html, tt.files)
			for _, s := range tt.contains {
				if !strings.Contains(result, s) {
					t.Errorf("expected result to contain %q, got: %s", s, result)
				}
			}
		})
	}
}

func TestOutputIframeToolSchema(t *testing.T) {
	workingDir := &MutableWorkingDir{}
	workingDir.Set("/tmp")
	tool := &OutputIframeTool{WorkingDir: workingDir}
	llmTool := tool.Tool()

	if llmTool.Name != "output_iframe" {
		t.Errorf("expected name 'output_iframe', got %q", llmTool.Name)
	}

	if llmTool.Run == nil {
		t.Error("expected Run function to be set")
	}

	if len(llmTool.InputSchema) == 0 {
		t.Error("expected InputSchema to be set")
	}

	// Verify schema contains files property
	if !strings.Contains(string(llmTool.InputSchema), "files") {
		t.Error("expected InputSchema to contain 'files' property")
	}
}
