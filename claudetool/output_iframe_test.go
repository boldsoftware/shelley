package claudetool

import (
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

	// Create a binary (non-UTF-8) file to exercise the validity guard.
	binFile := filepath.Join(tmpDir, "blob.bin")
	if err := os.WriteFile(binFile, []byte{0x00, 0xff, 0xfe, 0x88, 0x99, 0x3c, 0x68}, 0o644); err != nil {
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
		{
			// A binary file (e.g. an image accidentally passed as the HTML
			// path) must be rejected rather than stored as U+FFFD mojibake.
			name: "binary html file",
			input: map[string]any{
				"path": "blob.bin",
			},
			wantErr: true,
		},
		{
			name: "binary bundled file",
			input: map[string]any{
				"path": "chart.html",
				"files": map[string]any{
					"blob.bin": "blob.bin",
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

			result := tool.Tool().Run(t.Context(), inputJSON)

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

func TestOutputIframeLibraries(t *testing.T) {
	tmpDir := t.TempDir()
	htmlFile := filepath.Join(tmpDir, "test.html")
	if err := os.WriteFile(htmlFile, []byte("<html><head></head><body></body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	wd := &MutableWorkingDir{}
	wd.Set(tmpDir)
	tool := &OutputIframeTool{WorkingDir: wd}

	t.Run("valid library is recorded", func(t *testing.T) {
		in, _ := json.Marshal(map[string]any{
			"path":      "test.html",
			"libraries": []string{"excalidraw"},
		})
		res := tool.Tool().Run(t.Context(), in)
		if res.Error != nil {
			t.Fatalf("unexpected error: %v", res.Error)
		}
		disp, ok := res.Display.(OutputIframeDisplay)
		if !ok {
			t.Fatalf("bad display type %T", res.Display)
		}
		if len(disp.Libraries) != 1 || disp.Libraries[0] != "excalidraw" {
			t.Errorf("expected libraries=[excalidraw], got %v", disp.Libraries)
		}
		// The bundle bytes must NOT have been read into the display —
		// that's the whole point of the libraries channel.
		if strings.Contains(disp.HTML, "window.__FILES__[\"skill.js\"]") {
			t.Errorf("library bytes leaked into HTML")
		}
	})

	t.Run("unknown library rejected", func(t *testing.T) {
		in, _ := json.Marshal(map[string]any{
			"path":      "test.html",
			"libraries": []string{"not-a-real-lib"},
		})
		res := tool.Tool().Run(t.Context(), in)
		if res.Error == nil {
			t.Errorf("expected error for unknown library, got none")
		}
	})

	t.Run("duplicates collapsed", func(t *testing.T) {
		in, _ := json.Marshal(map[string]any{
			"path":      "test.html",
			"libraries": []string{"excalidraw", "excalidraw"},
		})
		res := tool.Tool().Run(t.Context(), in)
		if res.Error != nil {
			t.Fatalf("unexpected error: %v", res.Error)
		}
		disp := res.Display.(OutputIframeDisplay)
		if len(disp.Libraries) != 1 {
			t.Errorf("expected dedupe to 1, got %v", disp.Libraries)
		}
	})
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
		// HTML script-tag escapes: `</`, `<!--`, and `</script>` substrings
		// inside the literal must not allow the host <script> to close early
		// or trip HTML5's script-data-double-escape state.
		{"</script>", "<\\/script>"},
		{"x<!--y", "x<\\!--y"},
		{"a<b", "a<b"},
		// U+2028 / U+2029 are valid JSON but terminate JS string literals.
		{"a\u2028b", "a\\u2028b"},
		{"a\u2029b", "a\\u2029b"},
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
			name: "inject js file as raw string in __FILES__",
			html: "<html><head></head><body></body></html>",
			files: []EmbeddedFile{
				{Name: "app.js", Content: "console.log('hi');", Type: "js"},
			},
			contains: []string{"window.__FILES__[\"app.js\"]", "console.log('hi');"},
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

// TestLibraryPathsInSync guards against the LIBRARY_PATHS map in
// ui/src/vue/components/tools/OutputIframeTool.vue drifting from
// allowedLibraries. If they fall out of sync, the Go tool will accept a name
// the UI can't fetch, leaving window.__LIBS__ unresolved.
func TestLibraryPathsInSync(t *testing.T) {
	data, err := os.ReadFile("../ui/src/vue/components/tools/OutputIframeTool.vue")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	start := strings.Index(src, "const LIBRARY_PATHS")
	if start < 0 {
		t.Fatal("LIBRARY_PATHS not found in OutputIframeTool.vue")
	}
	end := strings.Index(src[start:], "};")
	if end < 0 {
		t.Fatal("LIBRARY_PATHS terminator not found")
	}
	block := src[start : start+end]
	for name, path := range allowedLibraries {
		needle := name + ": \"" + path + "\""
		if !strings.Contains(block, needle) {
			t.Errorf("LIBRARY_PATHS missing entry for %q -> %q\nblock: %s", name, path, block)
		}
	}
	// Also sanity-check there are no extra entries by counting commas+entries.
	lines := strings.Split(block, "\n")
	entries := 0
	for _, ln := range lines {
		if strings.Contains(ln, ":") && strings.Contains(ln, "/static/") {
			entries++
		}
	}
	if entries != len(allowedLibraries) {
		t.Errorf("LIBRARY_PATHS has %d entries, allowedLibraries has %d", entries, len(allowedLibraries))
	}
}

func TestOutputHTMLRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "v.html"), []byte("<p>hi</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin"), []byte{0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	wd := &MutableWorkingDir{}
	wd.Set(dir)
	tool := (&OutputHTMLTool{WorkingDir: wd}).Tool()
	res := tool.Run(t.Context(), json.RawMessage(`{"path":"v.html","title":"V","libraries":["excalidraw"]}`))
	d, ok := res.Display.(OutputIframeDisplay)
	if res.Error != nil || !ok || d.Type != "output_iframe" || d.HTML != "<p>hi</p>" || d.Title != "V" || len(d.Libraries) != 1 {
		t.Fatalf("output_html: %+v %v", res.Display, res.Error)
	}
	res = tool.Run(t.Context(), json.RawMessage(`{"path":"bin"}`))
	if res.Error == nil || !strings.Contains(res.Error.Error(), "output_html expects") {
		t.Fatalf("binary: %v", res.Error)
	}
}
