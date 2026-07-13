package server

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSystemPromptIncludesCwdGuidanceFiles verifies that AGENTS.md from the working directory
// is included in the generated system prompt.
func TestSystemPromptIncludesCwdGuidanceFiles(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	// Create a temp dir with a git repo
	tmpDir, err := os.MkdirTemp("", "shelley_git_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize a git repo in the temp dir. Set explicit author/committer
	// identity so the test does not depend on host git config (CI machines
	// often lack a default user.email and git refuses to auto-detect one).
	gitEnv := append(
		os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
	)
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	cmd.Env = gitEnv
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "--allow-empty", "--no-verify", "-m", "initial")
	cmd.Dir = tmpDir
	cmd.Env = gitEnv
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

	t.Setenv("HOME", tmpHome)

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
	t.Parallel()
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

func TestRunHookNoHook(t *testing.T) {
	// With no hook file, runHook returns the prompt unchanged.
	t.Setenv("HOME", t.TempDir())
	result, err := runHook("system-prompt", "original prompt")
	if err != nil {
		t.Fatal(err)
	}
	if result != "original prompt" {
		t.Errorf("expected original prompt, got %q", result)
	}
}

func TestRunHookModifiesPrompt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a hook that prepends "HOOKED: " to the first line
	hookPath := filepath.Join(hookDir, "system-prompt")
	script := "#!/bin/sh\nread input\necho \"HOOKED: $input\"\n"
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := runHook("system-prompt", "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if result != "HOOKED: hello world\n" {
		t.Errorf("expected hooked output, got %q", result)
	}
}

func TestRunHookNonExecutable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a hook file but make it non-executable
	hookPath := filepath.Join(hookDir, "system-prompt")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := runHook("system-prompt", "original")
	if err != nil {
		t.Fatal(err)
	}
	if result != "original" {
		t.Errorf("non-executable hook should be ignored, got %q", result)
	}
}

func TestRunHookFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a hook that exits non-zero
	hookPath := filepath.Join(hookDir, "system-prompt")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := runHook("system-prompt", "original")
	if err == nil {
		t.Fatal("expected error from failing hook")
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("error should mention failure, got: %v", err)
	}
}

func TestRunHookEmptyOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a hook that outputs nothing
	hookPath := filepath.Join(hookDir, "system-prompt")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := runHook("system-prompt", "original")
	if err == nil {
		t.Fatal("expected error from empty-output hook")
	}
	if !strings.Contains(err.Error(), "empty output") {
		t.Errorf("error should mention empty output, got: %v", err)
	}
}

func TestRunHookInvalidName(t *testing.T) {
	_, err := runHook("../evil", "prompt")
	if err == nil {
		t.Fatal("expected error for path-traversal hook name")
	}
}

func TestRunHookReceivesFullPrompt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a hook that passes stdin through to stdout (cat)
	hookPath := filepath.Join(hookDir, "system-prompt")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	multiline := "line1\nline2\nline3\n"
	result, err := runHook("system-prompt", multiline)
	if err != nil {
		t.Fatal(err)
	}
	if result != multiline {
		t.Errorf("cat hook should pass through input unchanged\ngot:  %q\nwant: %q", result, multiline)
	}
}

func TestRunNewConversationHookNoHook(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	result, _ := RunNewConversationHook(NewConversationHookInput{
		Prompt: "hello",
		Model:  "test-model",
		Cwd:    "/original/dir",
		Readonly: NewConversationReadonly{
			ConversationID: "conv-123",
		},
	})
	if result.Cwd != "/original/dir" {
		t.Errorf("expected /original/dir, got %q", result.Cwd)
	}
	if result.Prompt != "hello" {
		t.Errorf("expected hello, got %q", result.Prompt)
	}
	if result.Model != "test-model" {
		t.Errorf("expected test-model, got %q", result.Model)
	}
}

func TestRunNewConversationHookOverridesCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a hook that returns a new cwd
	hookPath := filepath.Join(hookDir, "new-conversation")
	script := `#!/bin/sh
echo '{"cwd": "/new/worktree"}'`
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	result, _ := RunNewConversationHook(NewConversationHookInput{
		Prompt: "hello",
		Model:  "test-model",
		Cwd:    "/original/dir",
	})
	if result.Cwd != "/new/worktree" {
		t.Errorf("expected /new/worktree, got %q", result.Cwd)
	}
	if result.Prompt != "hello" {
		t.Errorf("prompt should be unchanged, got %q", result.Prompt)
	}
}

func TestRunNewConversationHookOverridesAllMutableFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(hookDir, "new-conversation")
	script := `#!/bin/sh
echo '{"prompt": "modified prompt", "model": "new-model", "cwd": "/new/dir", "slug": "hook-slug"}'`
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	result, _ := RunNewConversationHook(NewConversationHookInput{
		Prompt: "original prompt",
		Model:  "original-model",
		Cwd:    "/original/dir",
	})
	if result.Prompt != "modified prompt" {
		t.Errorf("expected modified prompt, got %q", result.Prompt)
	}
	if result.Model != "new-model" {
		t.Errorf("expected new-model, got %q", result.Model)
	}
	if result.Cwd != "/new/dir" {
		t.Errorf("expected /new/dir, got %q", result.Cwd)
	}
	if result.Slug != "hook-slug" {
		t.Errorf("expected hook-slug, got %q", result.Slug)
	}
}

func TestRunNewConversationHookSlugOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(hookDir, "new-conversation")
	script := `#!/bin/sh
echo '{"slug": "my-slug"}'`
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	result, _ := RunNewConversationHook(NewConversationHookInput{
		Prompt: "original prompt",
		Model:  "original-model",
		Cwd:    "/original/dir",
	})
	if result.Slug != "my-slug" {
		t.Errorf("expected my-slug, got %q", result.Slug)
	}
	// Other fields should be unchanged.
	if result.Prompt != "original prompt" {
		t.Errorf("expected original prompt, got %q", result.Prompt)
	}
	if result.Model != "original-model" {
		t.Errorf("expected original-model, got %q", result.Model)
	}
	if result.Cwd != "/original/dir" {
		t.Errorf("expected /original/dir, got %q", result.Cwd)
	}
}

func TestRunNewConversationHookEmptyOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a hook that outputs nothing (no-op)
	hookPath := filepath.Join(hookDir, "new-conversation")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, _ := RunNewConversationHook(NewConversationHookInput{
		Prompt: "hello",
		Model:  "test-model",
		Cwd:    "/original/dir",
	})
	if result.Cwd != "/original/dir" {
		t.Errorf("expected /original/dir, got %q", result.Cwd)
	}
}

func TestRunNewConversationHookReceivesJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a hook that saves stdin to a file so we can inspect it
	dumpFile := filepath.Join(home, "hook-input.json")
	hookPath := filepath.Join(hookDir, "new-conversation")
	script := "#!/bin/sh\ncat > " + dumpFile + "\n"
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	_, _ = RunNewConversationHook(NewConversationHookInput{
		Prompt: "build me a thing",
		Model:  "claude-sonnet",
		Cwd:    "/home/user/project",
		Readonly: NewConversationReadonly{
			ConversationID: "conv-456",
			IsSubagent:     true,
			ParentID:       "conv-parent",
			IsOrchestrator: true,
		},
	})

	// Read and verify the JSON that was passed to the hook
	data, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("failed to read hook input: %v", err)
	}

	input := string(data)
	// Mutable fields at top level
	for _, expected := range []string{
		`"prompt":"build me a thing"`,
		`"model":"claude-sonnet"`,
		`"cwd":"/home/user/project"`,
	} {
		if !strings.Contains(input, expected) {
			t.Errorf("hook input missing %q\ngot: %s", expected, input)
		}
	}
	// Readonly fields nested under "readonly"
	for _, expected := range []string{
		`"conversation_id":"conv-456"`,
		`"is_subagent":true`,
		`"parent_id":"conv-parent"`,
		`"is_orchestrator":true`,
	} {
		if !strings.Contains(input, expected) {
			t.Errorf("hook input missing %q\ngot: %s", expected, input)
		}
	}
	// Verify the readonly block exists
	if !strings.Contains(input, `"readonly":{`) {
		t.Errorf("hook input should have a 'readonly' block\ngot: %s", input)
	}
}

func TestRunNewConversationHookFailureReturnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(hookDir, "new-conversation")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := RunNewConversationHook(NewConversationHookInput{
		Prompt: "hello",
		Model:  "my-model",
		Cwd:    "/original/dir",
	})
	if err == nil {
		t.Fatal("expected error from failing hook, got nil")
	}
	// Originals are still returned so the caller can fall back if it wants.
	if result.Cwd != "/original/dir" || result.Prompt != "hello" || result.Model != "my-model" {
		t.Errorf("expected originals returned alongside error, got %+v", result)
	}
}

func TestRunNewConversationHookInvalidJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a hook that returns invalid JSON
	hookPath := filepath.Join(hookDir, "new-conversation")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho 'not json'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := RunNewConversationHook(NewConversationHookInput{
		Cwd: "/original/dir",
	})
	if err == nil {
		t.Fatal("expected error on invalid JSON, got nil")
	}
	if result.Cwd != "/original/dir" {
		t.Errorf("expected /original/dir alongside error, got %q", result.Cwd)
	}
}

func TestRunNewConversationHookNonExecutable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a hook file but make it non-executable
	hookPath := filepath.Join(hookDir, "new-conversation")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, _ := RunNewConversationHook(NewConversationHookInput{
		Cwd: "/original/dir",
	})
	if result.Cwd != "/original/dir" {
		t.Errorf("expected /original/dir for non-executable hook, got %q", result.Cwd)
	}
}

func TestRunNewConversationHookPartialOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Hook only overrides model, leaving prompt and cwd unchanged
	hookPath := filepath.Join(hookDir, "new-conversation")
	script := `#!/bin/sh
echo '{"model": "better-model"}'`
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	result, _ := RunNewConversationHook(NewConversationHookInput{
		Prompt: "keep this",
		Model:  "original-model",
		Cwd:    "/keep/this/too",
	})
	if result.Prompt != "keep this" {
		t.Errorf("prompt should be unchanged, got %q", result.Prompt)
	}
	if result.Model != "better-model" {
		t.Errorf("expected better-model, got %q", result.Model)
	}
	if result.Cwd != "/keep/this/too" {
		t.Errorf("cwd should be unchanged, got %q", result.Cwd)
	}
}

// TestSubagentSystemPromptIncludesSkills verifies that skills are included
// in subagent system prompts.
func TestSubagentSystemPromptIncludesSkills(t *testing.T) {
	t.Parallel()
	// Create a temp directory with a .skills directory
	tmpDir, err := os.MkdirTemp("", "shelley_subagent_skills_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize a git repo (skills discovery works better in git repos)
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Create a .skills directory with a test skill
	skillsDir := filepath.Join(tmpDir, ".skills")
	if err = os.Mkdir(skillsDir, 0o755); err != nil {
		t.Fatalf("failed to create .skills dir: %v", err)
	}

	// Create a test skill directory and file
	testSkillDir := filepath.Join(skillsDir, "test-skill")
	if err := os.Mkdir(testSkillDir, 0o755); err != nil {
		t.Fatalf("failed to create test-skill dir: %v", err)
	}

	skillContent := `---
name: test-skill
description: A test skill for verification
---
This is a test skill.
`
	skillFile := filepath.Join(testSkillDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte(skillContent), 0o644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	// Generate subagent system prompt
	prompt, err := GenerateSubagentSystemPrompt(tmpDir, "parent-conv-id")
	if err != nil {
		t.Fatalf("GenerateSubagentSystemPrompt failed: %v", err)
	}

	// Verify the skills section is present
	if !strings.Contains(prompt, "<skills>") {
		t.Errorf("subagent prompt should contain <skills> section")
		t.Logf("Prompt: %s", prompt)
	}
	if !strings.Contains(prompt, "test-skill") {
		t.Errorf("subagent prompt should contain the test skill name")
		t.Logf("Prompt: %s", prompt)
	}
	if !strings.Contains(prompt, "A test skill for verification") {
		t.Errorf("subagent prompt should contain the test skill description")
	}
	if !strings.Contains(prompt, "Skills extend your capabilities") {
		t.Errorf("subagent prompt should contain skills introduction text")
	}
}

// TestOrchestratorSubagentSystemPromptIncludesSkills verifies that skills are
// included in orchestrator subagent system prompts.
func TestOrchestratorSubagentSystemPromptIncludesSkills(t *testing.T) {
	t.Parallel()
	// Create a temp directory with a .skills directory
	tmpDir, err := os.MkdirTemp("", "shelley_orch_subagent_skills_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize a git repo (skills discovery works better in git repos)
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Create a .skills directory with a test skill
	skillsDir := filepath.Join(tmpDir, ".skills")
	if err := os.Mkdir(skillsDir, 0o755); err != nil {
		t.Fatalf("failed to create .skills dir: %v", err)
	}

	// Create a test skill directory and file
	orchSkillDir := filepath.Join(skillsDir, "orchestrator-test-skill")
	if err := os.Mkdir(orchSkillDir, 0o755); err != nil {
		t.Fatalf("failed to create orchestrator-test-skill dir: %v", err)
	}

	skillContent := `---
name: orchestrator-test-skill
description: An orchestrator test skill
---
This is a test skill for orchestrators.
`
	skillFile := filepath.Join(orchSkillDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte(skillContent), 0o644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	// Generate orchestrator subagent system prompt
	prompt, err := GenerateOrchestratorSubagentSystemPrompt(tmpDir, "parent-conv-id")
	if err != nil {
		t.Fatalf("GenerateOrchestratorSubagentSystemPrompt failed: %v", err)
	}

	// Verify the skills section is present
	if !strings.Contains(prompt, "<skills>") {
		t.Errorf("orchestrator subagent prompt should contain <skills> section")
	}
	if !strings.Contains(prompt, "orchestrator-test-skill") {
		t.Errorf("orchestrator subagent prompt should contain the test skill name")
	}
	if !strings.Contains(prompt, "An orchestrator test skill") {
		t.Errorf("orchestrator subagent prompt should contain the test skill description")
	}
}

func TestNewConversationHookAppliesSlug(t *testing.T) {
	h := NewTestHarness(t)

	// Install the hook into the harness's isolated hooks dir rather than
	// $HOME: newTestServer points hooksDir at a private temp dir so tests
	// never pick up the developer's real ~/.config/shelley/hooks.
	hookPath := filepath.Join(h.server.hooksDir, "new-conversation")
	script := `#!/bin/sh
echo '{"slug": "Hello World!"}'`
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	h.NewConversation("first message", "")

	// Read back the conversation; slug should have been applied + sanitized.
	conv, err := h.db.GetConversationByID(context.Background(), h.convID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if conv.Slug == nil {
		t.Fatalf("slug not set on conversation")
	}
	if *conv.Slug != "hello-world" {
		t.Errorf("slug = %q, want %q", *conv.Slug, "hello-world")
	}
}

func TestRunEndOfTurnHookNoHook(t *testing.T) {
	// Should be a no-op and not panic.
	_ = RunEndOfTurnHookIn(t.TempDir(), EndOfTurnHookInput{ConversationID: "abc"})
}

func TestRunEndOfTurnHookReceivesJSON(t *testing.T) {
	// Use an explicit per-test hooks dir rather than t.Setenv("HOME"):
	// $HOME is process-wide and other tests in this package fire the
	// end-of-turn hook in background goroutines (via RunEndOfTurnHook
	// at end-of-turn in predictable-model conversations). Those would
	// race with this test's hook script and clobber dumpFile.
	hooksDir := t.TempDir()
	dumpFile := filepath.Join(t.TempDir(), "end-of-turn.json")
	hookPath := filepath.Join(hooksDir, "end-of-turn")
	script := "#!/bin/sh\ncat > " + dumpFile + "\n"
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	_ = RunEndOfTurnHookIn(hooksDir, EndOfTurnHookInput{
		Type:            "end_of_turn",
		ConversationID:  "conv-789",
		Hostname:        "phil-dev",
		Model:           "claude-sonnet",
		Slug:            "my-slug",
		ConversationURL: "https://phil-dev.exe.xyz/c/my-slug",
		VMName:          "phil-dev",
		FinalResponse:   "all done",
	})

	data, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("failed to read hook input: %v", err)
	}
	input := string(data)
	for _, expected := range []string{
		`"type":"end_of_turn"`,
		`"conversation_id":"conv-789"`,
		`"hostname":"phil-dev"`,
		`"model":"claude-sonnet"`,
		`"slug":"my-slug"`,
		`"conversation_url":"https://phil-dev.exe.xyz/c/my-slug"`,
		`"vm_name":"phil-dev"`,
		`"final_response":"all done"`,
	} {
		if !strings.Contains(input, expected) {
			t.Errorf("hook input missing %q\ngot: %s", expected, input)
		}
	}
}

func TestRunEndOfTurnHookFailureReturnsError(t *testing.T) {
	hooksDir := t.TempDir()
	hookPath := filepath.Join(hooksDir, "end-of-turn")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RunEndOfTurnHookIn(hooksDir, EndOfTurnHookInput{ConversationID: "abc"}); err == nil {
		t.Fatal("expected error from failing hook, got nil")
	}
}

func TestExeDevDefaultPortUsesInjectableClient(t *testing.T) {
	oldClient := exeDevDefaultPortHTTPClient
	t.Cleanup(func() { exeDevDefaultPortHTTPClient = oldClient })

	exeDevDefaultPortHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://reflection.int.exe.xyz/default_port" {
			t.Fatalf("unexpected URL %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"default_port":8123}`)),
			Header:     make(http.Header),
		}, nil
	})}

	if got := exeDevDefaultPort(); got != 8123 {
		t.Fatalf("exeDevDefaultPort() = %d, want 8123", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRunNewConversationHookReceivesHeaders(t *testing.T) {
	hooksDir := t.TempDir()
	dumpFile := filepath.Join(t.TempDir(), "new-conv.json")
	hookPath := filepath.Join(hooksDir, "new-conversation")
	script := "#!/bin/sh\ncat > " + dumpFile + "\n"
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	_, _ = RunNewConversationHookIn(hooksDir, NewConversationHookInput{
		Prompt: "hello",
		Readonly: NewConversationReadonly{
			ConversationID: "conv-1",
			Headers: [][2]string{
				{"X-Custom", "a"},
				{"X-Custom", "b"},
				{"X-ExeDev-Email", "user@example.com"},
			},
		},
	})

	data, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("failed to read hook input: %v", err)
	}
	input := string(data)
	for _, expected := range []string{
		`"headers":`,
		`["X-Custom","a"]`,
		`["X-Custom","b"]`,
		`["X-ExeDev-Email","user@example.com"]`,
	} {
		if !strings.Contains(input, expected) {
			t.Errorf("hook input missing %q\ngot: %s", expected, input)
		}
	}
}

func TestRunChatMessageHookNoHook(t *testing.T) {
	got, _ := RunChatMessageHookIn(t.TempDir(), ChatMessageHookInput{Message: "original"})
	if got != "original" {
		t.Errorf("expected unchanged message, got %q", got)
	}
}

func TestRunChatMessageHookRewrites(t *testing.T) {
	hooksDir := t.TempDir()
	hookPath := filepath.Join(hooksDir, "chat-message")
	script := `#!/bin/sh
echo '{"message": "rewritten"}'`
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	got, _ := RunChatMessageHookIn(hooksDir, ChatMessageHookInput{
		Message:  "original",
		Readonly: ChatMessageReadonly{ConversationID: "c1"},
	})
	if got != "rewritten" {
		t.Errorf("expected rewritten, got %q", got)
	}
}

func TestRunChatMessageHookReceivesContext(t *testing.T) {
	hooksDir := t.TempDir()
	dumpFile := filepath.Join(t.TempDir(), "chat-message.json")
	hookPath := filepath.Join(hooksDir, "chat-message")
	script := "#!/bin/sh\ncat > " + dumpFile + "\n"
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	_, _ = RunChatMessageHookIn(hooksDir, ChatMessageHookInput{
		Message: "hello",
		Readonly: ChatMessageReadonly{
			ConversationID: "conv-42",
			Model:          "claude-sonnet",
			ReasoningLevel: "high",
			Queued:         true,
			Headers:        [][2]string{{"X-Hdr", "v"}},
		},
	})

	data, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("failed to read hook input: %v", err)
	}
	input := string(data)
	for _, expected := range []string{
		`"message":"hello"`,
		`"conversation_id":"conv-42"`,
		`"model":"claude-sonnet"`,
		`"reasoning_level":"high"`,
		`"queued":true`,
		`["X-Hdr","v"]`,
	} {
		if !strings.Contains(input, expected) {
			t.Errorf("hook input missing %q\ngot: %s", expected, input)
		}
	}
}

func TestRunChatMessageHookInvalidJSONReturnsError(t *testing.T) {
	hooksDir := t.TempDir()
	hookPath := filepath.Join(hooksDir, "chat-message")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho 'not json'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := RunChatMessageHookIn(hooksDir, ChatMessageHookInput{Message: "keep"})
	if err == nil {
		t.Fatal("expected error on invalid JSON, got nil")
	}
	if got != "keep" {
		t.Errorf("expected original message returned alongside error, got %q", got)
	}
}

func TestRunChatMessageHookFailureReturnsError(t *testing.T) {
	hooksDir := t.TempDir()
	hookPath := filepath.Join(hooksDir, "chat-message")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := RunChatMessageHookIn(hooksDir, ChatMessageHookInput{Message: "keep"})
	if err == nil {
		t.Fatal("expected error from failing hook, got nil")
	}
	if got != "keep" {
		t.Errorf("expected original message returned alongside error, got %q", got)
	}
}

func TestHookHeadersStripsAuthSecrets(t *testing.T) {
	in := http.Header{}
	in.Set("X-ExeDev-Email", "user@example.com")
	in.Add("Cookie", "session=secret")
	in.Add("Set-Cookie", "k=v")
	in.Set("Authorization", "Bearer x")
	in.Set("Proxy-Authorization", "Bearer y")
	in.Set("User-Agent", "curl/8")

	out := HookHeaders(in)
	seen := map[string][]string{}
	for _, p := range out {
		seen[p[0]] = append(seen[p[0]], p[1])
	}
	for _, k := range []string{"Cookie", "Set-Cookie", "Authorization", "Proxy-Authorization"} {
		if _, ok := seen[k]; ok {
			t.Errorf("%s should be stripped: %v", k, out)
		}
	}
	if got := seen["X-Exedev-Email"]; len(got) != 1 || got[0] != "user@example.com" {
		t.Errorf("X-ExeDev-Email missing or wrong: %v", out)
	}
	if got := seen["User-Agent"]; len(got) != 1 || got[0] != "curl/8" {
		t.Errorf("User-Agent missing or wrong: %v", out)
	}
	// Result should be sorted by name.
	for i := 1; i < len(out); i++ {
		if out[i-1][0] > out[i][0] {
			t.Errorf("HookHeaders output not sorted: %v", out)
			break
		}
	}
}

func TestHookHeadersEmptyReturnsNil(t *testing.T) {
	if HookHeaders(nil) != nil {
		t.Errorf("expected nil for nil input")
	}
	onlySecrets := http.Header{}
	onlySecrets.Set("Cookie", "x=y")
	onlySecrets.Set("Authorization", "Bearer z")
	if HookHeaders(onlySecrets) != nil {
		t.Errorf("expected nil when only auth secrets present")
	}
}
