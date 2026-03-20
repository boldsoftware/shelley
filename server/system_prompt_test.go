package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSystemPromptIncludesCwdGuidanceFiles verifies that AGENTS.md from the working directory
// is included in the generated system prompt.
func TestSystemPromptIncludesCwdGuidanceFiles(t *testing.T) {
	// Create a temp directory to serve as our "context directory"
	tmpDir, err := os.MkdirTemp("", "shelley_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create an AGENTS.md file in the temp directory
	agentsContent := "TEST_UNIQUE_CONTENT_12345: Always use Go for everything."
	agentsFile := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(agentsFile, []byte(agentsContent), 0o644); err != nil {
		t.Fatalf("failed to write AGENTS.md: %v", err)
	}

	// Generate system prompt for this directory
	prompt, err := GenerateSystemPrompt(tmpDir)
	if err != nil {
		t.Fatalf("GenerateSystemPrompt failed: %v", err)
	}

	// Verify the unique content from AGENTS.md is included in the prompt
	if !strings.Contains(prompt, "TEST_UNIQUE_CONTENT_12345") {
		t.Errorf("system prompt should contain content from AGENTS.md in the working directory")
		t.Logf("AGENTS.md content: %s", agentsContent)
		t.Logf("Generated prompt (first 2000 chars): %s", prompt[:min(len(prompt), 2000)])
	}

	// Verify the file path is mentioned in guidance section
	if !strings.Contains(prompt, agentsFile) {
		t.Errorf("system prompt should reference the AGENTS.md file path")
	}
}

// TestSystemPromptEmptyCwdFallsBackToCurrentDir verifies that an empty workingDir
// causes GenerateSystemPrompt to use the current directory.
func TestSystemPromptEmptyCwdFallsBackToCurrentDir(t *testing.T) {
	// Get current directory for comparison
	currentDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get current directory: %v", err)
	}

	// Generate system prompt with empty workingDir
	prompt, err := GenerateSystemPrompt("")
	if err != nil {
		t.Fatalf("GenerateSystemPrompt failed: %v", err)
	}

	// Verify the current directory is mentioned in the prompt
	if !strings.Contains(prompt, currentDir) {
		t.Errorf("system prompt should contain current directory when cwd is empty")
	}
}

// TestSystemPromptDetectsGitInWorkingDir verifies that the system prompt
// correctly detects a git repo in the specified working directory, not the
// process's cwd. Regression test for https://github.com/boldsoftware/shelley/issues/71
func TestSystemPromptDetectsGitInWorkingDir(t *testing.T) {
	// Create a temp dir with a git repo
	tmpDir, err := os.MkdirTemp("", "shelley_git_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize a git repo in the temp dir
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "--allow-empty", "--no-verify", "-m", "initial")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}

	// Generate system prompt for the git repo directory
	prompt, err := GenerateSystemPrompt(tmpDir)
	if err != nil {
		t.Fatalf("GenerateSystemPrompt failed: %v", err)
	}

	// The prompt should say "Git root:" not "Not in a git repository"
	if strings.Contains(prompt, "Not in a git repository") {
		t.Errorf("system prompt incorrectly says 'Not in a git repository' for a directory that is a git repo")
	}
	if !strings.Contains(prompt, "Git root:") {
		t.Errorf("system prompt should contain 'Git root:' for a git repo directory")
	}
	if !strings.Contains(prompt, tmpDir) {
		t.Errorf("system prompt should reference the git root directory %s", tmpDir)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestSystemPromptIncludesSkillsFromAnyWorkingDir verifies that user-level
// skills (e.g. from ~/.config/agents/skills) appear in the system prompt
// regardless of the conversation's working directory.
// Regression test for https://github.com/boldsoftware/shelley/issues/83
func TestSystemPromptIncludesSkillsFromAnyWorkingDir(t *testing.T) {
	// Create a fake home with a skill
	tmpHome := t.TempDir()
	skillDir := filepath.Join(tmpHome, ".config", "agents", "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: test-skill\ndescription: A test skill for issue 83.\n---\nInstructions.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	// Generate system prompt from a directory completely unrelated to home
	unrelatedDir := t.TempDir()
	prompt, err := GenerateSystemPrompt(unrelatedDir)
	if err != nil {
		t.Fatalf("GenerateSystemPrompt failed: %v", err)
	}

	if !strings.Contains(prompt, "test-skill") {
		t.Error("system prompt should contain skill 'test-skill' even when working dir is unrelated to home")
	}
	if !strings.Contains(prompt, "A test skill for issue 83.") {
		t.Error("system prompt should contain the skill description")
	}
}

func TestSystemPromptIncludesUserEmail(t *testing.T) {
	tmpDir := t.TempDir()

	// Without email, no email line in prompt
	prompt, err := GenerateSystemPrompt(tmpDir)
	if err != nil {
		t.Fatalf("GenerateSystemPrompt failed: %v", err)
	}
	if strings.Contains(prompt, "exe.dev email") {
		t.Error("system prompt should not mention email when none is provided")
	}

	// With email, it should appear
	prompt, err = GenerateSystemPrompt(tmpDir, WithUserEmail("alice@example.com"))
	if err != nil {
		t.Fatalf("GenerateSystemPrompt with email failed: %v", err)
	}
	if !strings.Contains(prompt, "alice@example.com") {
		t.Error("system prompt should contain the user email when provided")
	}
}

// TestSystemPromptDeduplicatesIdenticalGuidanceFiles verifies that when multiple
// user-level AGENTS.md files have identical content (or are symlinks to the same
// file), only one copy appears in the system prompt.
func TestSystemPromptDeduplicatesIdenticalGuidanceFiles(t *testing.T) {
	// Create a fake home with two AGENTS.md locations containing the same content
	tmpHome := t.TempDir()

	configShelley := filepath.Join(tmpHome, ".config", "shelley")
	dotShelley := filepath.Join(tmpHome, ".shelley")
	if err := os.MkdirAll(configShelley, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dotShelley, 0o755); err != nil {
		t.Fatal(err)
	}

	agentsContent := "DEDUP_TEST_MARKER: identical content in both files"
	if err := os.WriteFile(filepath.Join(configShelley, "AGENTS.md"), []byte(agentsContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotShelley, "AGENTS.md"), []byte(agentsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", tmpHome)

	unrelatedDir := t.TempDir()
	prompt, err := GenerateSystemPrompt(unrelatedDir)
	if err != nil {
		t.Fatalf("GenerateSystemPrompt failed: %v", err)
	}

	// The marker should appear exactly once
	count := strings.Count(prompt, "DEDUP_TEST_MARKER")
	if count != 1 {
		t.Errorf("expected DEDUP_TEST_MARKER to appear exactly 1 time, got %d", count)
	}
}

// TestSystemPromptDeduplicatesSymlinkedGuidanceFiles verifies that symlinked
// AGENTS.md files are deduplicated by resolved path.
func TestSystemPromptDeduplicatesSymlinkedGuidanceFiles(t *testing.T) {
	tmpHome := t.TempDir()

	configShelley := filepath.Join(tmpHome, ".config", "shelley")
	dotShelley := filepath.Join(tmpHome, ".shelley")
	if err := os.MkdirAll(configShelley, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dotShelley, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write the canonical file
	agentsContent := "SYMLINK_DEDUP_MARKER: the one true agents file"
	canonicalPath := filepath.Join(dotShelley, "AGENTS.md")
	if err := os.WriteFile(canonicalPath, []byte(agentsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Symlink the other location to the canonical file
	symlinkPath := filepath.Join(configShelley, "AGENTS.md")
	if err := os.Symlink(canonicalPath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", tmpHome)

	unrelatedDir := t.TempDir()
	prompt, err := GenerateSystemPrompt(unrelatedDir)
	if err != nil {
		t.Fatalf("GenerateSystemPrompt failed: %v", err)
	}

	// The marker should appear exactly once
	count := strings.Count(prompt, "SYMLINK_DEDUP_MARKER")
	if count != 1 {
		t.Errorf("expected SYMLINK_DEDUP_MARKER to appear exactly 1 time, got %d", count)
	}
}
