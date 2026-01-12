package claudetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestChangeDirTool(t *testing.T) {
	// Create a temp directory structure
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	wd := NewMutableWorkingDir(tmpDir)
	tool := &ChangeDirTool{WorkingDir: wd}

	t.Run("change to absolute path", func(t *testing.T) {
		// Reset
		wd.Set(tmpDir)

		input, _ := json.Marshal(changeDirInput{Path: subDir})
		result := tool.Run(context.Background(), input)

		if result.Error != nil {
			t.Fatalf("unexpected error: %v", result.Error)
		}

		if wd.Get() != subDir {
			t.Errorf("expected working dir %q, got %q", subDir, wd.Get())
		}
	})

	t.Run("change to relative path", func(t *testing.T) {
		// Reset
		wd.Set(tmpDir)

		input, _ := json.Marshal(changeDirInput{Path: "subdir"})
		result := tool.Run(context.Background(), input)

		if result.Error != nil {
			t.Fatalf("unexpected error: %v", result.Error)
		}

		if wd.Get() != subDir {
			t.Errorf("expected working dir %q, got %q", subDir, wd.Get())
		}
	})

	t.Run("change to parent directory", func(t *testing.T) {
		wd.Set(subDir)

		input, _ := json.Marshal(changeDirInput{Path: ".."})
		result := tool.Run(context.Background(), input)

		if result.Error != nil {
			t.Fatalf("unexpected error: %v", result.Error)
		}

		if wd.Get() != tmpDir {
			t.Errorf("expected working dir %q, got %q", tmpDir, wd.Get())
		}
	})

	t.Run("error on non-existent path", func(t *testing.T) {
		wd.Set(tmpDir)

		input, _ := json.Marshal(changeDirInput{Path: "/nonexistent/path"})
		result := tool.Run(context.Background(), input)

		if result.Error == nil {
			t.Fatal("expected error for non-existent path")
		}
	})

	t.Run("error on file path", func(t *testing.T) {
		// Create a file
		filePath := filepath.Join(tmpDir, "file.txt")
		if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}

		wd.Set(tmpDir)

		input, _ := json.Marshal(changeDirInput{Path: filePath})
		result := tool.Run(context.Background(), input)

		if result.Error == nil {
			t.Fatal("expected error for file path")
		}
	})

	t.Run("OnChange callback is called", func(t *testing.T) {
		wd.Set(tmpDir)

		var callbackDir string
		toolWithCallback := &ChangeDirTool{
			WorkingDir: wd,
			OnChange: func(newDir string) {
				callbackDir = newDir
			},
		}

		input, _ := json.Marshal(changeDirInput{Path: subDir})
		result := toolWithCallback.Run(context.Background(), input)

		if result.Error != nil {
			t.Fatalf("unexpected error: %v", result.Error)
		}

		if callbackDir != subDir {
			t.Errorf("expected callback dir %q, got %q", subDir, callbackDir)
		}
	})
}

func TestChangeDirWithBash(t *testing.T) {
	// Create a temp directory structure
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a file in subdir
	testFile := filepath.Join(subDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	wd := NewMutableWorkingDir(tmpDir)
	changeDirTool := &ChangeDirTool{WorkingDir: wd}
	bashTool := &BashTool{WorkingDir: wd}

	ctx := context.Background()

	// Run pwd to verify starting directory
	input, _ := json.Marshal(bashInput{Command: "pwd"})
	result := bashTool.Run(ctx, input)
	if result.Error != nil {
		t.Fatalf("bash pwd failed: %v", result.Error)
	}

	// Change directory
	cdInput, _ := json.Marshal(changeDirInput{Path: subDir})
	result = changeDirTool.Run(ctx, cdInput)
	if result.Error != nil {
		t.Fatalf("change_dir failed: %v", result.Error)
	}

	// Run ls - should now see test.txt
	result = bashTool.Run(ctx, json.RawMessage(`{"command": "ls"}`))
	if result.Error != nil {
		t.Fatalf("bash ls failed: %v", result.Error)
	}

	// Verify we can see test.txt (indicating we're in subdir)
	if len(result.LLMContent) == 0 {
		t.Fatal("expected output from ls")
	}
	output := result.LLMContent[0].Text
	if output == "" {
		t.Fatal("expected non-empty output from ls")
	}
	// The output should contain "test.txt"
	if !contains(output, "test.txt") {
		t.Errorf("expected ls output to contain 'test.txt', got: %q", output)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestBashToolMissingWorkingDir(t *testing.T) {
	// Create a temp directory, then remove it
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	wd := NewMutableWorkingDir(subDir)
	bashTool := &BashTool{WorkingDir: wd}

	// Remove the directory
	if err := os.RemoveAll(subDir); err != nil {
		t.Fatal(err)
	}

	// Try to run a command - should get a clear error
	input, _ := json.Marshal(bashInput{Command: "ls"})
	result := bashTool.Run(context.Background(), input)

	if result.Error == nil {
		t.Fatal("expected error when working directory doesn't exist")
	}

	errStr := result.Error.Error()
	if !contains(errStr, "working directory does not exist") {
		t.Errorf("expected error about missing working directory, got: %s", errStr)
	}
	if !contains(errStr, "change_dir") {
		t.Errorf("expected error to mention change_dir tool, got: %s", errStr)
	}
}

func TestChangeDirTool_Method(t *testing.T) {
	wd := NewMutableWorkingDir("/test")
	tool := &ChangeDirTool{WorkingDir: wd}
	llmTool := tool.Tool()

	if llmTool == nil {
		t.Fatal("Tool() returned nil")
	}

	if llmTool.Name != changeDirName {
		t.Errorf("expected name %q, got %q", changeDirName, llmTool.Name)
	}

	if llmTool.Description != changeDirDescription {
		t.Errorf("expected description %q, got %q", changeDirDescription, llmTool.Description)
	}

	if llmTool.Run == nil {
		t.Error("Run function not set")
	}
}
