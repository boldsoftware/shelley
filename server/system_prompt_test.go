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
	result := RunNewConversationHook(NewConversationHookInput{
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

	result := RunNewConversationHook(NewConversationHookInput{
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
echo '{"prompt": "modified prompt", "model": "new-model", "cwd": "/new/dir"}'`
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	result := RunNewConversationHook(NewConversationHookInput{
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

	result := RunNewConversationHook(NewConversationHookInput{
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

	RunNewConversationHook(NewConversationHookInput{
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

func TestRunNewConversationHookFailureReturnsOriginals(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hookDir := filepath.Join(home, ".config", "shelley", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a hook that fails
	hookPath := filepath.Join(hookDir, "new-conversation")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	result := RunNewConversationHook(NewConversationHookInput{
		Prompt: "hello",
		Model:  "my-model",
		Cwd:    "/original/dir",
	})
	if result.Cwd != "/original/dir" {
		t.Errorf("expected /original/dir on failure, got %q", result.Cwd)
	}
	if result.Prompt != "hello" {
		t.Errorf("expected hello on failure, got %q", result.Prompt)
	}
	if result.Model != "my-model" {
		t.Errorf("expected my-model on failure, got %q", result.Model)
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

	result := RunNewConversationHook(NewConversationHookInput{
		Cwd: "/original/dir",
	})
	if result.Cwd != "/original/dir" {
		t.Errorf("expected /original/dir on invalid JSON, got %q", result.Cwd)
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

	result := RunNewConversationHook(NewConversationHookInput{
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

	result := RunNewConversationHook(NewConversationHookInput{
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
