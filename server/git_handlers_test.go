package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGetGitRoot tests the getGitRoot function
func TestGetGitRoot(t *testing.T) {
	t.Parallel()
	// Create a temporary directory for testing
	tempDir := t.TempDir()

	// Test with non-git directory
	_, err := getGitRoot(tempDir)
	if err == nil {
		t.Error("expected error for non-git directory, got nil")
	}

	// Create a git repository
	gitDir := filepath.Join(tempDir, "repo")
	err = os.MkdirAll(gitDir, 0o755)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = gitDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = gitDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = gitDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	// Test with git directory
	root, err := getGitRoot(gitDir)
	if err != nil {
		t.Errorf("unexpected error for git directory: %v", err)
	}
	if root != gitDir {
		t.Errorf("expected root %s, got %s", gitDir, root)
	}

	// Test with subdirectory of git directory
	subDir := filepath.Join(gitDir, "subdir")
	err = os.MkdirAll(subDir, 0o755)
	if err != nil {
		t.Fatal(err)
	}

	root, err = getGitRoot(subDir)
	if err != nil {
		t.Errorf("unexpected error for git subdirectory: %v", err)
	}
	if root != gitDir {
		t.Errorf("expected root %s, got %s", gitDir, root)
	}
}

// TestParseDiffStat tests the parseDiffStat function
func TestParseDiffStat(t *testing.T) {
	t.Parallel()
	// Test empty output
	additions, deletions, filesCount := parseDiffStat("")
	if additions != 0 || deletions != 0 || filesCount != 0 {
		t.Errorf("expected 0,0,0 for empty output, got %d,%d,%d", additions, deletions, filesCount)
	}

	// Test single file
	output := "5\t3\tfile1.txt\n"
	additions, deletions, filesCount = parseDiffStat(output)
	if additions != 5 || deletions != 3 || filesCount != 1 {
		t.Errorf("expected 5,3,1 for single file, got %d,%d,%d", additions, deletions, filesCount)
	}

	// Test multiple files
	output = "5\t3\tfile1.txt\n10\t2\tfile2.txt\n"
	additions, deletions, filesCount = parseDiffStat(output)
	if additions != 15 || deletions != 5 || filesCount != 2 {
		t.Errorf("expected 15,5,2 for multiple files, got %d,%d,%d", additions, deletions, filesCount)
	}

	// Test file with additions only
	output = "5\t0\tfile1.txt\n"
	additions, deletions, filesCount = parseDiffStat(output)
	if additions != 5 || deletions != 0 || filesCount != 1 {
		t.Errorf("expected 5,0,1 for additions only, got %d,%d,%d", additions, deletions, filesCount)
	}

	// Test file with deletions only
	output = "0\t3\tfile1.txt\n"
	additions, deletions, filesCount = parseDiffStat(output)
	if additions != 0 || deletions != 3 || filesCount != 1 {
		t.Errorf("expected 0,3,1 for deletions only, got %d,%d,%d", additions, deletions, filesCount)
	}

	// Test file with binary content (represented as -)
	output = "-\t-\tfile1.bin\n"
	additions, deletions, filesCount = parseDiffStat(output)
	if additions != 0 || deletions != 0 || filesCount != 1 {
		t.Errorf("expected 0,0,1 for binary file, got %d,%d,%d", additions, deletions, filesCount)
	}
}

// setupTestGitRepo creates a temporary git repository with some content for testing
func setupTestGitRepo(t *testing.T) string {
	// Create a temporary directory for testing
	tempDir := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tempDir
	err := cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tempDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = tempDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	// Create and commit a file
	filePath := filepath.Join(tempDir, "test.txt")
	content := "Hello, World!\n"
	err = os.WriteFile(filePath, []byte(content), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "add", "test.txt")
	cmd.Dir = tempDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit\n\nPrompt: Initial test commit for git handlers test", "--author=Test <test@example.com>")
	cmd.Dir = tempDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("git commit failed: %v", err)
		t.Logf("git commit output: %s", string(output))
		t.Fatal(err)
	}

	// Modify the file (staged changes)
	newContent := "Hello, World!\nModified content\n"
	err = os.WriteFile(filePath, []byte(newContent), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "add", "test.txt")
	cmd.Dir = tempDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	// Modify the file again (unstaged changes)
	unstagedContent := "Hello, World!\nModified content\nMore changes\n"
	err = os.WriteFile(filePath, []byte(unstagedContent), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	// Create another file (untracked)
	untrackedPath := filepath.Join(tempDir, "untracked.txt")
	untrackedContent := "Untracked file\n"
	err = os.WriteFile(untrackedPath, []byte(untrackedContent), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	return tempDir
}

// TestHandleGitDiffs tests the handleGitDiffs function
func TestHandleGitDiffs(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	// Test with non-git directory
	req := httptest.NewRequest("GET", "/api/git/diffs?cwd=/tmp", nil)
	w := httptest.NewRecorder()
	h.server.handleGitDiffs(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for non-git directory, got %d", w.Code)
	}

	// Setup a test git repository
	gitDir := setupTestGitRepo(t)

	// Test with valid git directory
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs?cwd=%s", gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitDiffs(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for git directory, got %d: %s", w.Code, w.Body.String())
	}

	// Check response content type
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected content-type application/json, got %s", w.Header().Get("Content-Type"))
	}

	// Parse response
	var response struct {
		Diffs   []GitDiffInfo `json:"diffs"`
		GitRoot string        `json:"gitRoot"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Check that we have at least one diff (working changes)
	if len(response.Diffs) == 0 {
		t.Error("expected at least one diff (working changes)")
	}

	// Check that the first diff is working changes
	if len(response.Diffs) > 0 {
		diff := response.Diffs[0]
		if diff.ID != "working" {
			t.Errorf("expected first diff ID to be 'working', got %s", diff.ID)
		}
		if diff.Message != "Working Changes" {
			t.Errorf("expected first diff message to be 'Working Changes', got %s", diff.Message)
		}
	}

	// Check that git root is correct
	if response.GitRoot != gitDir {
		t.Errorf("expected git root %s, got %s", gitDir, response.GitRoot)
	}

	// Test with subdirectory of git directory
	subDir := filepath.Join(gitDir, "subdir")
	err = os.MkdirAll(subDir, 0o755)
	if err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs?cwd=%s", subDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitDiffs(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for git subdirectory, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleGitDiffFiles tests the handleGitDiffFiles function
func TestHandleGitDiffFiles(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	// Setup a test git repository
	gitDir := setupTestGitRepo(t)

	// Test with invalid method
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/git/diffs/working/files?cwd=%s", gitDir), nil)
	w := httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405 for invalid method, got %d", w.Code)
	}

	// Test with invalid path
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs/working?cwd=%s", gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid path, got %d", w.Code)
	}

	// Test with non-git directory
	req = httptest.NewRequest("GET", "/api/git/diffs/working/files?cwd=/tmp", nil)
	w = httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for non-git directory, got %d", w.Code)
	}

	// Test with working changes
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs/working/files?cwd=%s", gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for working changes, got %d: %s", w.Code, w.Body.String())
	}

	// Check response content type
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected content-type application/json, got %s", w.Header().Get("Content-Type"))
	}

	// Parse response
	var files []GitFileInfo
	err := json.Unmarshal(w.Body.Bytes(), &files)
	if err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Check that we have at least one file
	if len(files) == 0 {
		t.Error("expected at least one file in working changes")
	}

	// Check file information
	if len(files) > 0 {
		file := files[0]
		if file.Path != "test.txt" {
			t.Errorf("expected file path test.txt, got %s", file.Path)
		}
		if file.Status != "modified" {
			t.Errorf("expected file status modified, got %s", file.Status)
		}
	}
}

// TestHandleGitFileDiff tests the handleGitFileDiff function
func TestHandleGitFileDiff(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	// Setup a test git repository
	gitDir := setupTestGitRepo(t)

	// Test with invalid method
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/git/file-diff/working/test.txt?cwd=%s", gitDir), nil)
	w := httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405 for invalid method, got %d", w.Code)
	}

	// Test with invalid path (missing diff ID)
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/file-diff/test.txt?cwd=%s", gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid path, got %d", w.Code)
	}

	// Test with non-git directory
	req = httptest.NewRequest("GET", "/api/git/file-diff/working/test.txt?cwd=/tmp", nil)
	w = httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for non-git directory, got %d", w.Code)
	}

	// Test with working changes
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/file-diff/working/test.txt?cwd=%s", gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for working changes, got %d: %s", w.Code, w.Body.String())
	}

	// Check response content type
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected content-type application/json, got %s", w.Header().Get("Content-Type"))
	}

	// Parse response
	var fileDiff GitFileDiff
	err := json.Unmarshal(w.Body.Bytes(), &fileDiff)
	if err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Check file information
	if fileDiff.Path != "test.txt" {
		t.Errorf("expected file path test.txt, got %s", fileDiff.Path)
	}

	// Check that we have content
	if fileDiff.OldContent == "" {
		t.Error("expected old content")
	}

	if fileDiff.NewContent == "" {
		t.Error("expected new content")
	}

	// Test with path traversal attempt (should be blocked)
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/file-diff/working/../etc/passwd?cwd=%s", gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for path traversal attempt, got %d", w.Code)
	}
}

// TestCumulativeDiff verifies that selecting a commit shows cumulative changes
// from that commit's parent through the current working tree state.
func TestCumulativeDiff(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	// Create a repo with multiple commits and working changes
	tempDir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tempDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	run("git", "init")
	run("git", "config", "user.name", "Test")
	run("git", "config", "user.email", "test@test.com")

	// Commit 1: create file with "line1"
	os.WriteFile(filepath.Join(tempDir, "f.txt"), []byte("line1\n"), 0o644)
	run("git", "add", "f.txt")
	run("git", "commit", "-m", "commit1\n\nPrompt: test")
	commit1 := run("git", "rev-parse", "HEAD")

	// Commit 2: append "line2"
	os.WriteFile(filepath.Join(tempDir, "f.txt"), []byte("line1\nline2\n"), 0o644)
	run("git", "add", "f.txt")
	run("git", "commit", "-m", "commit2\n\nPrompt: test")
	commit2 := run("git", "rev-parse", "HEAD")

	// Working tree: append "line3"
	os.WriteFile(filepath.Join(tempDir, "f.txt"), []byte("line1\nline2\nline3\n"), 0o644)

	// When selecting commit2, the diff should show changes from commit1 (parent of commit2)
	// through the current working tree, i.e., old="line1\n", new="line1\nline2\nline3\n"

	// Test file list: selecting commit2 should include f.txt (changed from commit1's parent to working tree)
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs/%s/files?cwd=%s",
		run("git", "rev-parse", "HEAD"), tempDir), nil)
	w := httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var files []GitFileInfo
	if err := json.Unmarshal(w.Body.Bytes(), &files); err != nil {
		t.Fatalf("failed to unmarshal files: %v", err)
	}
	if len(files) != 1 || files[0].Path != "f.txt" {
		t.Fatalf("expected [f.txt], got %+v", files)
	}

	// Test file diff content for commit1 (oldest commit):
	// parent is empty tree, so old="", new=current working tree
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/file-diff/%s/f.txt?cwd=%s", commit1, tempDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var diff1 GitFileDiff
	if err := json.Unmarshal(w.Body.Bytes(), &diff1); err != nil {
		t.Fatalf("failed to unmarshal diff1: %v", err)
	}
	if diff1.OldContent != "" {
		t.Errorf("commit1 old content: expected empty (root commit), got %q", diff1.OldContent)
	}
	if diff1.NewContent != "line1\nline2\nline3\n" {
		t.Errorf("commit1 new content: expected working tree content, got %q", diff1.NewContent)
	}

	// Test file diff for commit2: old=content before commit2 (i.e. "line1\n"),
	// new=current working tree ("line1\nline2\nline3\n")
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/file-diff/%s/f.txt?cwd=%s", commit2, tempDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var diff2 GitFileDiff
	if err := json.Unmarshal(w.Body.Bytes(), &diff2); err != nil {
		t.Fatalf("failed to unmarshal diff2: %v", err)
	}
	if diff2.OldContent != "line1\n" {
		t.Errorf("commit2 old content: expected %q, got %q", "line1\n", diff2.OldContent)
	}
	if diff2.NewContent != "line1\nline2\nline3\n" {
		t.Errorf("commit2 new content: expected working tree content %q, got %q", "line1\nline2\nline3\n", diff2.NewContent)
	}
}

// setupRootCommitRepo creates a git repo with only a single (root) commit.
func setupRootCommitRepo(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()

	for _, args := range [][]string{
		{"init"},
		{"config", "user.name", "Test User"},
		{"config", "user.email", "test@example.com"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = tempDir
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
	}

	// Create files and commit
	err := os.WriteFile(filepath.Join(tempDir, "hello.txt"), []byte("hello world\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(tempDir, "readme.md"), []byte("# Test\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "add", "hello.txt", "readme.md")
	cmd.Dir = tempDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit\n\nPrompt: test", "--author=Test <test@example.com>")
	cmd.Dir = tempDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	return tempDir
}

func TestRootCommitDiffs(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	gitDir := setupRootCommitRepo(t)

	// Get the commit hash
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = gitDir
	hashBytes, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	commitHash := string(hashBytes[:len(hashBytes)-1]) // trim newline

	// handleGitDiffs should list the root commit with correct stats
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs?cwd=%s", gitDir), nil)
	w := httptest.NewRecorder()
	h.server.handleGitDiffs(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleGitDiffs: %d: %s", w.Code, w.Body.String())
	}

	var diffsResp struct {
		Diffs []GitDiffInfo `json:"diffs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &diffsResp); err != nil {
		t.Fatal(err)
	}
	// Should have working + 1 commit
	if len(diffsResp.Diffs) != 2 {
		t.Fatalf("expected 2 diffs, got %d", len(diffsResp.Diffs))
	}
	commitDiff := diffsResp.Diffs[1]
	if commitDiff.FilesCount != 2 {
		t.Errorf("expected 2 files in root commit, got %d", commitDiff.FilesCount)
	}
	if commitDiff.Additions != 2 {
		t.Errorf("expected 2 additions in root commit, got %d", commitDiff.Additions)
	}

	// handleGitDiffFiles should list files from root commit
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs/%s/files?cwd=%s", commitHash, gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleGitDiffFiles: %d: %s", w.Code, w.Body.String())
	}

	var files []GitFileInfo
	if err := json.Unmarshal(w.Body.Bytes(), &files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	for _, f := range files {
		if f.Status != "added" {
			t.Errorf("expected status 'added' for %s in root commit, got %s", f.Path, f.Status)
		}
	}

	// handleGitFileDiff should return empty old content and correct new content
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/file-diff/%s/hello.txt?cwd=%s", commitHash, gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleGitFileDiff: %d: %s", w.Code, w.Body.String())
	}

	var fileDiff GitFileDiff
	if err := json.Unmarshal(w.Body.Bytes(), &fileDiff); err != nil {
		t.Fatal(err)
	}
	if fileDiff.OldContent != "" {
		t.Errorf("expected empty old content for root commit, got %q", fileDiff.OldContent)
	}
	if fileDiff.NewContent != "hello world\n" {
		t.Errorf("expected 'hello world\n' as new content, got %q", fileDiff.NewContent)
	}

	// to=self on a root commit: parent is the empty tree, head is the
	// commit itself, so the diff is the full commit with no working-tree
	// noise.
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs/%s/files?cwd=%s&to=self", commitHash, gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleGitDiffFiles to=self on root: %d: %s", w.Code, w.Body.String())
	}
	var selfFiles []GitFileInfo
	if err := json.Unmarshal(w.Body.Bytes(), &selfFiles); err != nil {
		t.Fatal(err)
	}
	if len(selfFiles) != 2 {
		t.Errorf("expected 2 files for root commit to=self, got %d", len(selfFiles))
	}

	// Reject malicious `to` values that look like git flags.
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs/%s/files?cwd=%s&to=--output=/tmp/x", commitHash, gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for to=--output=, got %d", w.Code)
	}
}

// TestHandleGitDiffFiles_ToParam exercises the optional `to` query param that
// scopes the right-hand side of the diff (working tree / self / arbitrary hash).
func TestHandleGitDiffFiles_ToParam(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	gitDir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = gitDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	run("init")
	run("config", "user.name", "Test")
	run("config", "user.email", "test@example.com")

	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(gitDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	commit := func(msg string) {
		run("commit", "-m", msg+"\n\nPrompt: test")
	}
	write("a.txt", "a1\n")
	run("add", ".")
	commit("c1")
	write("b.txt", "b1\n")
	run("add", ".")
	commit("c2")
	write("c.txt", "c1\n")
	run("add", ".")
	commit("c3")
	// Working-tree-only modification of a tracked file.
	write("a.txt", "a1\nworking\n")

	revList := func() []string {
		cmd := exec.Command("git", "rev-list", "HEAD")
		cmd.Dir = gitDir
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(out)), "\n")
	}
	hashes := revList() // [c3, c2, c1] newest first
	c1, c2, c3 := hashes[2], hashes[1], hashes[0]

	getFiles := func(diffID, to string) []string {
		url := fmt.Sprintf("/api/git/diffs/%s/files?cwd=%s", diffID, gitDir)
		if to != "" {
			url += "&to=" + to
		}
		req := httptest.NewRequest("GET", url, nil)
		w := httptest.NewRecorder()
		h.server.handleGitDiffFiles(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("diffs/%s/files to=%q: %d: %s", diffID, to, w.Code, w.Body.String())
		}
		var files []GitFileInfo
		if err := json.Unmarshal(w.Body.Bytes(), &files); err != nil {
			t.Fatal(err)
		}
		paths := make([]string, len(files))
		for i, f := range files {
			paths[i] = f.Path
		}
		return paths
	}

	// Default: from c2 through working tree (a.txt picks up the working
	// modification because we compare c2's parent—c1—to the working tree).
	got := getFiles(c2, "")
	want := []string{"a.txt", "b.txt", "c.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("default: got %v, want %v", got, want)
	}

	// Self: only c2 itself.
	got = getFiles(c2, "self")
	want = []string{"b.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("self: got %v, want %v", got, want)
	}

	// Range starting at c1 through c3 (parent(c1)..c3 — includes a.txt).
	got = getFiles(c1, c3)
	want = []string{"a.txt", "b.txt", "c.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("range: got %v, want %v", got, want)
	}

	// commit-messages with to=self returns just the from commit.
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/git/commit-messages?cwd=%s&from=%s&to=self", gitDir, c2), nil)
	w := httptest.NewRecorder()
	h.server.handleGitCommitMessages(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("commit-messages: %d: %s", w.Code, w.Body.String())
	}
	var msgs []CommitMessage
	if err := json.Unmarshal(w.Body.Bytes(), &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Hash != c2 {
		t.Errorf("commit-messages self: got %+v, want only %s", msgs, c2)
	}

	// commit-messages default: c1 from..HEAD includes c2, c3 + c1.
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/commit-messages?cwd=%s&from=%s", gitDir, c1), nil)
	w = httptest.NewRecorder()
	h.server.handleGitCommitMessages(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("commit-messages default: %d", w.Code)
	}
	msgs = nil
	if err := json.Unmarshal(w.Body.Bytes(), &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Errorf("commit-messages default: expected 3 commits, got %d", len(msgs))
	}

	// file-diff for a.txt at c2 with to=self should yield the committed
	// content at c2 ("a1\n"), not the working-tree modification.
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/file-diff/%s/a.txt?cwd=%s&to=self", c2, gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("file-diff: %d: %s", w.Code, w.Body.String())
	}
	var fd GitFileDiff
	if err := json.Unmarshal(w.Body.Bytes(), &fd); err != nil {
		t.Fatal(err)
	}
	if fd.NewContent != "a1\n" {
		t.Errorf("file-diff to=self for a.txt at c2: got %q, want %q", fd.NewContent, "a1\n")
	}
	// And without `to`, it should reflect the working-tree change.
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/file-diff/%s/a.txt?cwd=%s", c2, gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("file-diff default: %d", w.Code)
	}
	fd = GitFileDiff{}
	if err := json.Unmarshal(w.Body.Bytes(), &fd); err != nil {
		t.Fatal(err)
	}
	if fd.NewContent != "a1\nworking\n" {
		t.Errorf("file-diff default for a.txt: got %q, want working version", fd.NewContent)
	}
}

func TestParseDecorations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"main", []string{"main"}},
		{"HEAD -> main", []string{"HEAD", "main"}},
		{"HEAD -> main, origin/main", []string{"HEAD", "main", "origin/main"}},
		{"tag: v1.2.3, refs/stash", []string{"v1.2.3", "refs/stash"}},
		{"  HEAD -> main ,  origin/main  ", []string{"HEAD", "main", "origin/main"}},
	}
	for _, tc := range cases {
		got := parseDecorations(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("parseDecorations(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseDecorations(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// TestHandleGitDiffs_RefsAndMergeBase verifies the diff list includes
// decorating refs (HEAD, branch) and the merge-base flag when an
// upstream is configured.
func TestHandleGitDiffs_RefsAndMergeBase(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	gitDir := setupTestGitRepo(t)

	req := httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs?cwd=%s", gitDir), nil)
	w := httptest.NewRecorder()
	h.server.handleGitDiffs(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Diffs []GitDiffInfo `json:"diffs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	// Find the commit row (skipping the working-changes row).
	var commit *GitDiffInfo
	for i := range response.Diffs {
		if response.Diffs[i].ID != "working" {
			commit = &response.Diffs[i]
			break
		}
	}
	if commit == nil {
		t.Fatal("no commit in diffs")
	}
	foundHead := false
	for _, ref := range commit.Refs {
		if ref == "HEAD" {
			foundHead = true
		}
	}
	if !foundHead {
		t.Errorf("expected HEAD in refs, got %v", commit.Refs)
	}
	// IsMergeBase should be false because the test repo has no upstream.
	if commit.IsMergeBase {
		t.Errorf("expected IsMergeBase=false (no upstream), got true")
	}
}
