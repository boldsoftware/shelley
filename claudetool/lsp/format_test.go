package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatDefinitionEmpty(t *testing.T) {
	result := formatDefinition(nil, "/tmp")
	if result != "No definition found." {
		t.Errorf("got %q, want %q", result, "No definition found.")
	}
}

func TestFormatDefinitionSingle(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "main.go")
	os.WriteFile(testFile, []byte("package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"), 0o644)

	locations := []Location{
		{
			URI: fileURI(testFile),
			Range: Range{
				Start: Position{Line: 2, Character: 5},
				End:   Position{Line: 2, Character: 9},
			},
		},
	}

	result := formatDefinition(locations, dir)
	if !strings.Contains(result, "main.go:3") {
		t.Errorf("expected result to contain 'main.go:3' (1-based line), got:\n%s", result)
	}
	if !strings.Contains(result, "func main()") {
		t.Errorf("expected result to contain source context, got:\n%s", result)
	}
}

func TestFormatReferencesEmpty(t *testing.T) {
	result := formatReferences(nil, "/tmp")
	if result != "No references found." {
		t.Errorf("got %q, want %q", result, "No references found.")
	}
}

func TestFormatReferencesGroupedByFile(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "a.go")
	file2 := filepath.Join(dir, "b.go")
	os.WriteFile(file1, []byte("package main\n\nvar x = 1\n"), 0o644)
	os.WriteFile(file2, []byte("package main\n\nvar y = x\n"), 0o644)

	locations := []Location{
		{URI: fileURI(file1), Range: Range{Start: Position{Line: 2, Character: 4}}},
		{URI: fileURI(file2), Range: Range{Start: Position{Line: 2, Character: 8}}},
	}

	result := formatReferences(locations, dir)
	if !strings.Contains(result, "a.go") {
		t.Errorf("expected result to contain a.go, got:\n%s", result)
	}
	if !strings.Contains(result, "b.go") {
		t.Errorf("expected result to contain b.go, got:\n%s", result)
	}
	if !strings.Contains(result, "2 reference") {
		t.Errorf("expected result to mention 2 references, got:\n%s", result)
	}
}

func TestFormatReferenceTruncation(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.go")
	os.WriteFile(testFile, []byte("package main\n"), 0o644)

	// Create more than maxReferences locations
	var locations []Location
	for i := 0; i < maxReferences+10; i++ {
		locations = append(locations, Location{
			URI: fileURI(testFile),
			Range: Range{
				Start: Position{Line: 0, Character: 0},
			},
		})
	}

	result := formatReferences(locations, dir)
	if !strings.Contains(result, "showing first 50") {
		t.Errorf("expected truncation message, got:\n%s", result)
	}
}

func TestFormatHoverEmpty(t *testing.T) {
	result := formatHover(nil)
	if result != "No hover information available." {
		t.Errorf("got %q", result)
	}

	result = formatHover(&Hover{Contents: MarkupContent{}})
	if result != "No hover information available." {
		t.Errorf("got %q", result)
	}
}

func TestFormatHoverWithContent(t *testing.T) {
	hover := &Hover{
		Contents: MarkupContent{
			Kind:  "markdown",
			Value: "```go\nfunc Println(a ...any) (n int, err error)\n```\nPrintln formats using the default formats...",
		},
	}
	result := formatHover(hover)
	if !strings.Contains(result, "func Println") {
		t.Errorf("expected hover content, got:\n%s", result)
	}
}

func TestFormatSymbolsEmpty(t *testing.T) {
	result := formatSymbols(nil, "/tmp")
	if result != "No symbols found." {
		t.Errorf("got %q", result)
	}
}

func TestFormatSymbols(t *testing.T) {
	symbols := []SymbolInformation{
		{
			Name:     "ProcessOneTurn",
			Kind:     SymbolKindFunction,
			Location: Location{URI: fileURI("/project/loop/loop.go"), Range: Range{Start: Position{Line: 99}}},
		},
		{
			Name:          "Run",
			Kind:          SymbolKindMethod,
			ContainerName: "Server",
			Location:      Location{URI: fileURI("/project/server/server.go"), Range: Range{Start: Position{Line: 49}}},
		},
	}

	result := formatSymbols(symbols, "/project")
	if !strings.Contains(result, "ProcessOneTurn") {
		t.Errorf("expected ProcessOneTurn in output, got:\n%s", result)
	}
	if !strings.Contains(result, "Function") {
		t.Errorf("expected Function kind, got:\n%s", result)
	}
	if !strings.Contains(result, "loop/loop.go:100") {
		t.Errorf("expected 1-based line number, got:\n%s", result)
	}
	if !strings.Contains(result, "in Server") {
		t.Errorf("expected container name, got:\n%s", result)
	}
}

func TestRelativePath(t *testing.T) {
	tests := []struct {
		path string
		wd   string
		want string
	}{
		{"/project/src/main.go", "/project", "src/main.go"},
		{"/other/file.go", "/project", "../other/file.go"},
		{"/project/file.go", "/project", "file.go"},
	}
	for _, tt := range tests {
		got := relativePath(tt.path, tt.wd)
		if got != tt.want {
			t.Errorf("relativePath(%q, %q) = %q, want %q", tt.path, tt.wd, got, tt.want)
		}
	}
}

func TestFileURI(t *testing.T) {
	got := fileURI("/home/user/file.go")
	if !strings.HasPrefix(got, "file://") {
		t.Errorf("fileURI should start with file://, got %q", got)
	}
	if !strings.Contains(got, "file.go") {
		t.Errorf("fileURI should contain filename, got %q", got)
	}
}

func TestFilePathFromURI(t *testing.T) {
	// Round-trip test
	original := "/home/user/project/main.go"
	uri := fileURI(original)
	back := filePathFromURI(uri)
	if back != original {
		t.Errorf("round trip: %q -> %q -> %q", original, uri, back)
	}
}

func TestLanguageID(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"app.ts", "typescript"},
		{"app.tsx", "typescriptreact"},
		{"app.js", "javascript"},
		{"app.jsx", "javascriptreact"},
		{"script.py", "python"},
		{"main.rs", "rust"},
		{"Main.java", "java"},
		{"readme.txt", "plaintext"},
	}
	for _, tt := range tests {
		got := languageID(tt.path)
		if got != tt.want {
			t.Errorf("languageID(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestSymbolKindName(t *testing.T) {
	if got := SymbolKindName(SymbolKindFunction); got != "Function" {
		t.Errorf("SymbolKindName(Function) = %q", got)
	}
	if got := SymbolKindName(SymbolKindStruct); got != "Struct" {
		t.Errorf("SymbolKindName(Struct) = %q", got)
	}
	if got := SymbolKindName(SymbolKind(999)); got != "Unknown" {
		t.Errorf("SymbolKindName(999) = %q", got)
	}
}
