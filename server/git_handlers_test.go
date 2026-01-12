package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGetGitRoot tests the getGitRoot function
func TestGetGitRoot(t *testing.T) {
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
	h := NewTestHarness(t)
	defer h.Close()

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
	h := NewTestHarness(t)
	defer h.Close()

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
	h := NewTestHarness(t)
	defer h.Close()

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
