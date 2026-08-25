package claudetool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/llm"
)

func TestPatchToolAliasesSharePathLock(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "file.txt")
	if err := os.WriteFile(path, []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(tempDir, "symlink.txt")
	if err := os.Symlink(path, symlink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	hardlink := filepath.Join(tempDir, "hardlink.txt")
	if err := os.Link(path, hardlink); err != nil {
		t.Skipf("hard link unavailable: %v", err)
	}

	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	for _, alias := range []string{path, symlink, hardlink} {
		unlock := patch.lockPath(alias)
		unlock()
	}

	created := filepath.Join(tempDir, "created.txt")
	unlock := patch.lockPath(created)
	if err := os.WriteFile(created, []byte("created"), 0o600); err != nil {
		t.Fatal(err)
	}
	unlock()
	createdHardlink := filepath.Join(tempDir, "created-hardlink.txt")
	if err := os.Link(created, createdHardlink); err != nil {
		t.Skipf("hard link unavailable: %v", err)
	}
	unlock = patch.lockPath(createdHardlink)
	unlock()

	patch.pathLocksMu.Lock()
	defer patch.pathLocksMu.Unlock()
	if len(patch.pathLocks) != 2 {
		t.Fatalf("aliases created %d path locks, want 2", len(patch.pathLocks))
	}
}

func TestPatchToolConcurrentSameFilePreservesIndependentEdits(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	path := filepath.Join(tempDir, "shared.txt")
	var original strings.Builder
	for i := range 40 {
		fmt.Fprintf(&original, "[token-%d]\n", i)
	}
	if err := os.WriteFile(path, []byte(original.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 40)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			<-start
			input := PatchInput{
				Path: path,
				Patches: []PatchRequest{{
					Operation: "replace",
					OldText:   fmt.Sprintf("[token-%d]", i),
					NewText:   fmt.Sprintf("[done-%d]", i),
				}},
			}
			if result := patch.runInput(context.Background(), input); result.Error != nil {
				errs <- result.Error
			}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		if !strings.Contains(string(content), fmt.Sprintf("[done-%d]", i)) {
			t.Errorf("concurrent edit %d was lost", i)
		}
	}
}

func TestPatchToolConcurrentSharedClipboardTransactions(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	errs := make(chan error, 40)
	start := make(chan struct{})

	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			name := fmt.Sprintf("value-%d", i)
			path := filepath.Join(tempDir, fmt.Sprintf("shared-clipboard-%d.txt", i))
			if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
				errs <- err
				return
			}
			input := PatchInput{
				Path: path,
				Patches: []PatchRequest{
					{Operation: "replace", OldText: name, NewText: "updated", ToClipboard: "shared"},
					{Operation: "append_eof", FromClipboard: "shared"},
				},
			}
			<-start
			if result := patch.runInput(context.Background(), input); result.Error != nil {
				errs <- result.Error
				return
			}
			content, err := os.ReadFile(path)
			if err != nil {
				errs <- err
				return
			}
			if got, want := string(content), "updated"+name; got != want {
				errs <- fmt.Errorf("%s = %q, want %q", path, got, want)
			}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestPatchToolConcurrentClipboardAccess(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	errs := make(chan error, 20)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			name := fmt.Sprintf("item-%d", i)
			source := filepath.Join(tempDir, name+"-source.txt")
			if err := os.WriteFile(source, []byte(name), 0o600); err != nil {
				errs <- err
				return
			}
			copyInput := PatchInput{
				Path: source,
				Patches: []PatchRequest{{
					Operation:   "replace",
					OldText:     name,
					NewText:     "updated",
					ToClipboard: name,
				}},
			}
			if result := patch.runInput(context.Background(), copyInput); result.Error != nil {
				errs <- result.Error
				return
			}

			pasteInput := PatchInput{
				Path: filepath.Join(tempDir, name+"-dest.txt"),
				Patches: []PatchRequest{{
					Operation:     "overwrite",
					FromClipboard: name,
				}},
			}
			if result := patch.runInput(context.Background(), pasteInput); result.Error != nil {
				errs <- result.Error
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestPatchTool_BasicOperations(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	// Test overwrite operation (creates new file)
	testFile := filepath.Join(tempDir, "test.txt")
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "Hello World\n",
		}},
	}

	result := patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("overwrite failed: %v", result.Error)
	}

	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(content) != "Hello World\n" {
		t.Errorf("expected 'Hello World\\n', got %q", string(content))
	}

	// Test replace operation
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "World",
		NewText:   "Patch",
	}}

	result = patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("replace failed: %v", result.Error)
	}

	content, _ = os.ReadFile(testFile)
	if string(content) != "Hello Patch\n" {
		t.Errorf("expected 'Hello Patch\\n', got %q", string(content))
	}

	// Test append_eof operation
	input.Patches = []PatchRequest{{
		Operation: "append_eof",
		NewText:   "Appended line\n",
	}}

	result = patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("append_eof failed: %v", result.Error)
	}

	content, _ = os.ReadFile(testFile)
	expected := "Hello Patch\nAppended line\n"
	if string(content) != expected {
		t.Errorf("expected %q, got %q", expected, string(content))
	}

	// Test prepend_bof operation
	input.Patches = []PatchRequest{{
		Operation: "prepend_bof",
		NewText:   "Prepended line\n",
	}}

	result = patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("prepend_bof failed: %v", result.Error)
	}

	content, _ = os.ReadFile(testFile)
	expected = "Prepended line\nHello Patch\nAppended line\n"
	if string(content) != expected {
		t.Errorf("expected %q, got %q", expected, string(content))
	}
}

func TestPatchTool_ClipboardOperations(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "clipboard.txt")

	// Create initial content
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "function original() {\n    return 'original';\n}\n",
		}},
	}

	result := patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("initial overwrite failed: %v", result.Error)
	}

	// Test toClipboard operation
	input.Patches = []PatchRequest{{
		Operation:   "replace",
		OldText:     "function original() {\n    return 'original';\n}",
		NewText:     "function renamed() {\n    return 'renamed';\n}",
		ToClipboard: "saved_func",
	}}

	result = patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("toClipboard failed: %v", result.Error)
	}

	// Test fromClipboard operation
	input.Patches = []PatchRequest{{
		Operation:     "append_eof",
		FromClipboard: "saved_func",
	}}

	result = patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("fromClipboard failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	if !strings.Contains(string(content), "function original()") {
		t.Error("clipboard content not restored properly")
	}
}

func TestPatchTool_IndentationAdjustment(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "indent.go")

	// Create file with tab indentation
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "package main\n\nfunc main() {\n\tif true {\n\t\t// placeholder\n\t}\n}\n",
		}},
	}

	result := patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("initial setup failed: %v", result.Error)
	}

	// Test indentation adjustment: convert spaces to tabs
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "// placeholder",
		NewText:   "    fmt.Println(\"hello\")\n    fmt.Println(\"world\")",
		Reindent: &Reindent{
			Strip: "    ",
			Add:   "\t\t",
		},
	}}

	result = patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("indentation adjustment failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	expected := "\t\tfmt.Println(\"hello\")\n\t\tfmt.Println(\"world\")"
	if !strings.Contains(string(content), expected) {
		t.Errorf("indentation not adjusted correctly, got:\n%s", string(content))
	}
}

func TestPatchTool_FuzzyMatching(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "fuzzy.go")

	// Create Go file with specific indentation
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "package main\n\nfunc test() {\n\tif condition {\n\t\tfmt.Println(\"hello\")\n\t\tfmt.Println(\"world\")\n\t}\n}\n",
		}},
	}

	result := patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("initial setup failed: %v", result.Error)
	}

	// Test fuzzy matching with different whitespace
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "if condition {\n        fmt.Println(\"hello\")\n        fmt.Println(\"world\")\n    }", // spaces instead of tabs
		NewText:   "if condition {\n\t\tfmt.Println(\"modified\")\n\t}",
	}}

	result = patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("fuzzy matching failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	if !strings.Contains(string(content), "modified") {
		t.Error("fuzzy matching did not work")
	}
}

func TestPatchTool_ErrorCases(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "error.txt")

	// Test replace operation on non-existent file
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "replace",
			OldText:   "something",
			NewText:   "else",
		}},
	}

	result := patch.runInput(ctx, input)
	if result.Error == nil {
		t.Error("expected error for replace on non-existent file")
	}

	// Create file with duplicate text
	input.Patches = []PatchRequest{{
		Operation: "overwrite",
		NewText:   "duplicate\nduplicate\n",
	}}

	result = patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("failed to create test file: %v", result.Error)
	}

	// Test non-unique text
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "duplicate",
		NewText:   "unique",
	}}

	result = patch.runInput(ctx, input)
	if result.Error == nil || !strings.Contains(result.Error.Error(), "not unique") {
		t.Error("expected non-unique error")
	}

	// Test missing text
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "nonexistent",
		NewText:   "something",
	}}

	result = patch.runInput(ctx, input)
	if result.Error == nil || !strings.Contains(result.Error.Error(), "not found") {
		t.Error("expected not found error")
	}

	// Test invalid clipboard reference
	input.Patches = []PatchRequest{{
		Operation:     "append_eof",
		FromClipboard: "nonexistent",
	}}

	result = patch.runInput(ctx, input)
	if result.Error == nil || !strings.Contains(result.Error.Error(), "clipboard") {
		t.Error("expected clipboard error")
	}
}

func TestPatchTool_AutogeneratedDetection(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "generated.go")

	// Create autogenerated file
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "// Code generated by tool. DO NOT EDIT.\npackage main\n\nfunc generated() {}\n",
		}},
	}

	result := patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("failed to create generated file: %v", result.Error)
	}

	// Test patching autogenerated file (should warn but work)
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "func generated() {}",
		NewText:   "func modified() {}",
	}}

	result = patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("patching generated file failed: %v", result.Error)
	}

	if len(result.LLMContent) == 0 || !strings.Contains(result.LLMContent[0].Text, "autogenerated") {
		t.Error("expected autogenerated warning")
	}
}

func TestPatchTool_MultiplePatches(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "multi.go")
	var result llm.ToolOut

	// Apply multiple patches - first create file, then modify
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "package main\n\nfunc first() {\n\tprintln(\"first\")\n}\n\nfunc second() {\n\tprintln(\"second\")\n}\n",
		}},
	}

	result = patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("failed to create initial file: %v", result.Error)
	}

	// Now apply multiple patches in one call
	input.Patches = []PatchRequest{
		{
			Operation: "replace",
			OldText:   "println(\"first\")",
			NewText:   "println(\"ONE\")",
		},
		{
			Operation: "replace",
			OldText:   "println(\"second\")",
			NewText:   "println(\"TWO\")",
		},
		{
			Operation: "append_eof",
			NewText:   "\n// Multiple patches applied\n",
		},
	}

	result = patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("multiple patches failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	contentStr := string(content)
	if !strings.Contains(contentStr, "ONE") || !strings.Contains(contentStr, "TWO") {
		t.Error("multiple patches not applied correctly")
	}
	if !strings.Contains(contentStr, "Multiple patches applied") {
		t.Error("append_eof in multiple patches not applied")
	}
}

func TestPatchTool_CopyRecipe(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "copy.txt")

	// Create initial content
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "original text",
		}},
	}

	result := patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("failed to create file: %v", result.Error)
	}

	// Test copy recipe (toClipboard + fromClipboard with same name)
	input.Patches = []PatchRequest{{
		Operation:     "replace",
		OldText:       "original text",
		NewText:       "replaced text",
		ToClipboard:   "copy_test",
		FromClipboard: "copy_test",
	}}

	result = patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("copy recipe failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	// The copy recipe should preserve the original text
	if string(content) != "original text" {
		t.Errorf("copy recipe failed, expected 'original text', got %q", string(content))
	}
}

func TestPatchTool_RelativePaths(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	// Test relative path resolution
	input := PatchInput{
		Path: "relative.txt", // relative path
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "relative path test\n",
		}},
	}

	result := patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("relative path failed: %v", result.Error)
	}

	// Check file was created in correct location
	expectedPath := filepath.Join(tempDir, "relative.txt")
	content, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("file not created at expected path: %v", err)
	}
	if string(content) != "relative path test\n" {
		t.Error("relative path file content incorrect")
	}
}

// Benchmark basic patch operations
func BenchmarkPatchTool_BasicOperations(b *testing.B) {
	tempDir := b.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "bench.go")
	initialContent := "package main\n\nfunc test() {\n\tfor i := 0; i < 100; i++ {\n\t\tfmt.Println(i)\n\t}\n}\n"

	// Setup
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   initialContent,
		}},
	}
	patch.runInput(ctx, input)

	for b.Loop() {
		// Benchmark replace operation
		input.Patches = []PatchRequest{{
			Operation: "replace",
			OldText:   "fmt.Println(i)",
			NewText:   "fmt.Printf(\"%d\\n\", i)",
		}}

		result := patch.runInput(ctx, input)
		if result.Error != nil {
			b.Fatalf("benchmark failed: %v", result.Error)
		}

		// Reset for next iteration
		input.Patches = []PatchRequest{{
			Operation: "replace",
			OldText:   "fmt.Printf(\"%d\\n\", i)",
			NewText:   "fmt.Println(i)",
		}}
		patch.runInput(ctx, input)
	}
}

func TestPatchTool_CallbackFunction(t *testing.T) {
	tempDir := t.TempDir()
	callbackCalled := false
	var capturedInput PatchInput
	var capturedOutput llm.ToolOut

	patch := &PatchTool{
		WorkingDir: NewMutableWorkingDir(tempDir),
		Callback: func(input PatchInput, output llm.ToolOut) llm.ToolOut {
			callbackCalled = true
			capturedInput = input
			capturedOutput = output
			// Modify the output
			output.LLMContent = llm.TextContent("Modified by callback")
			return output
		},
	}

	ctx := context.Background()
	testFile := filepath.Join(tempDir, "callback.txt")

	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "callback test",
		}},
	}

	result := patch.runInput(ctx, input)

	if !callbackCalled {
		t.Error("callback was not called")
	}

	if capturedInput.Path != testFile {
		t.Error("callback did not receive correct input")
	}

	if len(result.LLMContent) == 0 || result.LLMContent[0].Text != "Modified by callback" {
		t.Error("callback did not modify output correctly")
	}

	if capturedOutput.Error != nil {
		t.Errorf("callback received error: %v", capturedOutput.Error)
	}
}

func TestPatchTool_DisplayDataContainsUnifiedDiffOnly(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "display.txt")
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "before\n",
		}},
	}

	result := patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("initial overwrite failed: %v", result.Error)
	}

	input = PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "replace",
			OldText:   "before",
			NewText:   "after",
		}},
	}
	result = patch.runInput(ctx, input)
	if result.Error != nil {
		t.Fatalf("replace failed: %v", result.Error)
	}

	display, ok := result.Display.(PatchDisplayData)
	if !ok {
		t.Fatalf("expected PatchDisplayData display payload, got %T", result.Display)
	}
	if display.Path != testFile {
		t.Fatalf("display path = %q, want %q", display.Path, testFile)
	}
	if display.Diff == "" {
		t.Fatal("display diff should not be empty")
	}
	if !strings.Contains(display.Diff, "@@") {
		t.Fatalf("display diff does not look like unified diff: %q", display.Diff)
	}

	displayJSON, err := json.Marshal(display)
	if err != nil {
		t.Fatalf("failed to marshal display payload: %v", err)
	}
	if strings.Contains(string(displayJSON), "oldContent") {
		t.Fatalf("display payload should not include oldContent: %s", string(displayJSON))
	}
	if strings.Contains(string(displayJSON), "newContent") {
		t.Fatalf("display payload should not include newContent: %s", string(displayJSON))
	}
}

func TestPatchToolRejectsInvalidSimpleContractBeforeFilesystemAccess(t *testing.T) {
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(t.TempDir()), Profile: "simple"}
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "missing path", input: `{"edits":[{"oldText":"a","newText":"b"}]}`, want: "patch path is required"},
		{name: "empty path", input: `{"path":"","edits":[{"oldText":"a","newText":"b"}]}`, want: "patch path is required"},
		{name: "missing edits and append", input: `{"path":"file.txt"}`, want: "no patches provided"},
		{name: "missing old text", input: `{"path":"file.txt","edits":[{"newText":"x"}]}`, want: "oldText is required"},
		{name: "full patches", input: `{"path":"file.txt","patches":[{"operation":"overwrite","newText":"x"}]}`, want: `unknown field "patches"`},
		{name: "flat operation", input: `{"path":"file.txt","operation":"replace","oldText":"a","newText":"b"}`, want: `unknown field "operation"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := patch.Run(context.Background(), json.RawMessage(tt.input))
			if result.Error == nil || !strings.Contains(result.Error.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", result.Error, tt.want)
			}
		})
	}
}

func TestPatchToolExposesAndAcceptsSimpleInput(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "simple.txt"), []byte("alpha beta gamma"), 0o640); err != nil {
		t.Fatal(err)
	}
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir), Profile: "simple"}
	tool := patch.Tool()
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"path", "edits", "append"} {
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("simple schema missing %q", name)
		}
	}
	for _, name := range []string{"patches", "operation", "oldText", "newText"} {
		if _, ok := schema.Properties[name]; ok {
			t.Errorf("simple schema exposes %q", name)
		}
	}

	result := tool.Run(context.Background(), json.RawMessage(`{"path":"simple.txt","edits":[{"oldText":"alpha","newText":"A"},{"oldText":"gamma","newText":"G"}]}`))
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	content, err := os.ReadFile(filepath.Join(tempDir, "simple.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), "A beta G"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	info, err := os.Stat(filepath.Join(tempDir, "simple.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
}

func TestSimplePatchAppendsWithoutUniqueAnchor(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "simple.txt")
	if err := os.WriteFile(path, []byte("same\nmiddle\nsame\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := (&PatchTool{WorkingDir: NewMutableWorkingDir(tempDir), Profile: "simple"}).Tool()
	result := tool.Run(context.Background(), json.RawMessage(`{"path":"simple.txt","append":"appended\n"}`))
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), "same\nmiddle\nsame\nappended\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestSimplePatchDoesNotAppendWhenEditFails(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "simple.txt")
	original := "same\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := (&PatchTool{WorkingDir: NewMutableWorkingDir(tempDir), Profile: "simple"}).Tool()
	result := tool.Run(context.Background(), json.RawMessage(`{"path":"simple.txt","edits":[{"oldText":"missing","newText":"changed"}],"append":"appended\n"}`))
	if result.Error == nil {
		t.Fatal("expected failed edit")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("partial mutation = %q", content)
	}
}

func TestClassifyPatchError(t *testing.T) {
	tests := []struct {
		err  string
		want string
	}{
		{"old text not found:\nmissing", "old_text_not_found"},
		{"old text not unique:\nrepeated", "old_text_not_unique"},
		{`apply_patch update for "example.go" matched 0 locations`, "old_text_not_found"},
		{`apply_patch update for "example.go" matched 3 locations at lines 1, 3, 5`, "old_text_not_unique"},
		{`file "missing" does not exist`, "path_not_found"},
		{`failed to read file "dir": is a directory`, "path_read_failed"},
		{"unrecognized operation", "execution_failed"},
	}
	for _, tt := range tests {
		if got := classifyPatchError(errors.New(tt.err)); got != tt.want {
			t.Errorf("classifyPatchError(%q) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestApplyPatchProfileToolAndExecution(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir), Profile: "codex_apply_patch"}
	tool := patch.Tool()
	if tool.Name != "apply_patch" || tool.CustomGrammar == "" {
		t.Fatalf("tool = name %q grammar %q", tool.Name, tool.CustomGrammar)
	}
	for _, want := range []string{
		`beginning with "*** Begin Patch" and ending with "*** End Patch"`,
		`Context text after its one-character marker must match the file verbatim, including leading spaces and tabs.`,
		`up to 3 unchanged lines before and after the edit when available`,
		`unless fewer lines already include a unique structural anchor`,
		`a parse or match failure rejects the entire patch without changing files`,
		`For "matched 0 locations," reread the current file and retry with exact current context`,
	} {
		if !strings.Contains(tool.Description, want) {
			t.Errorf("apply_patch description missing guidance %q", want)
		}
	}
	if err := os.WriteFile(filepath.Join(tempDir, "edit.txt"), []byte("before\nkeep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := `*** Begin Patch
*** Update File: edit.txt
@@
-before
+after
 keep
*** Add File: added.txt
+new file
*** End Patch
`
	raw, _ := json.Marshal(applyPatchInput{Input: input})
	result := tool.Run(context.Background(), raw)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	for path, want := range map[string]string{"edit.txt": "after\nkeep\n", "added.txt": "new file\n"} {
		got, err := os.ReadFile(filepath.Join(tempDir, path))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestApplyPatchMatchErrorExplainsMissingContext(t *testing.T) {
	err := applyPatchMatchError("example.go", "current\ncontents\n", "stale\ncontents\n")
	for _, want := range []string{
		`apply_patch update for "example.go" matched 0 locations`,
		"The context must match exactly, including whitespace.",
		"Reread the current file and retry with context copied from it.",
		"No files were changed",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestApplyPatchMatchErrorIdentifiesAmbiguousLines(t *testing.T) {
	err := applyPatchMatchError("example.go", "same\nother\nsame\nmore\nsame\n", "same\n")
	for _, want := range []string{
		`apply_patch update for "example.go" matched 3 locations at lines 1, 3, 5`,
		"Include more surrounding unchanged lines so the context identifies one location.",
		"No files were changed",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestApplyPatchValidatesAllFilesBeforeWriting(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir), Profile: "codex_apply_patch"}
	if err := os.WriteFile(filepath.Join(tempDir, "first.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := `*** Begin Patch
*** Update File: first.txt
@@
-before
+after
*** Update File: missing.txt
@@
-nope
+changed
*** End Patch`
	raw, _ := json.Marshal(applyPatchInput{Input: input})
	if result := patch.Tool().Run(context.Background(), raw); result.Error == nil {
		t.Fatal("expected validation error")
	}
	got, err := os.ReadFile(filepath.Join(tempDir, "first.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "before\n" {
		t.Fatalf("first file mutated before validation completed: %q", got)
	}
}

func TestApplyPatchDeletesFile(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "delete.txt")
	if err := os.WriteFile(path, []byte("remove me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir), Profile: "codex_apply_patch"}
	raw, _ := json.Marshal(applyPatchInput{Input: `*** Begin Patch
*** Delete File: delete.txt
*** End Patch`})
	result := patch.Tool().Run(context.Background(), raw)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted file stat error = %v", err)
	}
}

func TestApplyPatchRejectsMalformedInput(t *testing.T) {
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(t.TempDir()), Profile: "codex_apply_patch"}
	for _, input := range []string{
		"*** Add File: missing-begin.txt\n+content\n*** End Patch",
		"*** Begin Patch\n*** Update File: empty.txt\n*** End Patch",
		"*** Begin Patch\n*** Move to: moved.txt\n*** End Patch",
	} {
		raw, _ := json.Marshal(applyPatchInput{Input: input})
		if result := patch.Tool().Run(context.Background(), raw); result.Error == nil {
			t.Errorf("input %q unexpectedly succeeded", input)
		}
	}
}

func TestPatchToolExposesAndAcceptsNestedInput(t *testing.T) {
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(t.TempDir()), Profile: "nested"}
	tool := patch.Tool()
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties["patches"]; !ok {
		t.Fatal("nested schema missing patches")
	}
	if _, ok := schema.Properties["operation"]; ok {
		t.Fatal("nested schema exposes top-level operation")
	}
	result := tool.Run(context.Background(), json.RawMessage(`{"path":"nested.txt","patches":[{"operation":"overwrite","newText":"hello"}]}`))
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	content, err := os.ReadFile(filepath.Join(patch.getWorkingDir(), "nested.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello" {
		t.Fatalf("content = %q", content)
	}
}

func TestSimplePatchFailureDoesNotWrite(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "simple.txt")
	original := "alpha beta\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := (&PatchTool{WorkingDir: NewMutableWorkingDir(tempDir), Profile: "simple"}).Tool()
	result := tool.Run(context.Background(), json.RawMessage(`{"path":"simple.txt","edits":[{"oldText":"alpha","newText":"A"},{"oldText":"missing","newText":"M"}]}`))
	if result.Error == nil {
		t.Fatal("expected failed edit")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("partial mutation = %q", content)
	}
}

func TestSimplePatchRejectsOverlaps(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "simple.txt")
	if err := os.WriteFile(path, []byte("abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := (&PatchTool{WorkingDir: NewMutableWorkingDir(tempDir), Profile: "simple"}).Tool()
	result := tool.Run(context.Background(), json.RawMessage(`{"path":"simple.txt","edits":[{"oldText":"abcd","newText":"A"},{"oldText":"cdef","newText":"C"}]}`))
	if result.Error == nil || !strings.Contains(result.Error.Error(), "overlapping edits") {
		t.Fatalf("error = %v", result.Error)
	}
}
