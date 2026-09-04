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

	"github.com/josharian/sockpath"
	"shelley.exe.dev/committour"
)

// TestGetGitRoot tests the getGitRoot function
func TestGetGitRoot(t *testing.T) {
	t.Parallel()
	// Create a temporary directory for testing
	tempDir := sockpath.TempDir(t)

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
	tempDir := sockpath.TempDir(t)

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

func TestHandleGitDiffsHasTour(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	dir := setupTestGitRepo(t)

	firstCmd := exec.Command("git", "rev-parse", "HEAD")
	firstCmd.Dir = dir
	firstOut, err := firstCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	first := strings.TrimSpace(string(firstOut))

	if err := os.WriteFile(filepath.Join(dir, "second.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, dir, "add", "second.txt")
	testGit(t, dir, "commit", "--no-verify", "-m", "Second commit")
	secondCmd := exec.Command("git", "rev-parse", "HEAD")
	secondCmd.Dir = dir
	secondOut, err := secondCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	second := strings.TrimSpace(string(secondOut))

	if err := committour.WriteNote(dir, first, []byte(`{"version":1,"chunks":[]}`)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/git/diffs?cwd=%s", dir), nil)
	w := httptest.NewRecorder()
	h.server.handleGitDiffs(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleGitDiffs: %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Diffs []map[string]any `json:"diffs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	foundFirst, foundSecond := false, false
	for _, diff := range response.Diffs {
		switch diff["id"] {
		case first:
			foundFirst = true
			if diff["hasTour"] != true {
				t.Errorf("annotated commit hasTour = %v", diff["hasTour"])
			}
		case second:
			foundSecond = true
			if _, ok := diff["hasTour"]; ok {
				t.Errorf("unannotated commit hasTour = %v, want omitted", diff["hasTour"])
			}
		}
	}
	if !foundFirst || !foundSecond {
		t.Fatalf("commits found: annotated=%v unannotated=%v", foundFirst, foundSecond)
	}
}

func TestHandleGitTour(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	dir := setupTestGitRepo(t)
	hash := strings.TrimSpace(testGitOutput(t, dir, "rev-parse", "HEAD"))
	_, fragments, err := committour.Chunks(dir, hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 1 {
		t.Fatalf("fragments = %d, want 1", len(fragments))
	}

	request := func(ref string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/git/tour?cwd=%s&hash=%s", dir, ref), nil)
		w := httptest.NewRecorder()
		h.server.handleGitTour(w, req)
		return w
	}

	t.Run("no note", func(t *testing.T) {
		w := request(hash)
		if w.Code != http.StatusNotFound || w.Body.String() != "{\"error\":\"no tour\"}\n" {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
		if w.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("content type = %q", w.Header().Get("Content-Type"))
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		if err := committour.WriteNote(dir, hash, []byte(`not JSON`)); err != nil {
			t.Fatal(err)
		}
		w := request(hash)
		if w.Code != http.StatusNotFound || w.Body.String() != "{\"error\":\"no tour\"}\n" {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("verification failure", func(t *testing.T) {
		stale := strings.Replace(fragments[0], "+Hello, World!", "+stale", 1)
		note, err := json.Marshal(committour.Tour{Version: 1, Chunks: []committour.TourChunk{{Patch: stale, Comment: "old"}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := committour.WriteNote(dir, hash, note); err != nil {
			t.Fatal(err)
		}
		w := request(hash)
		if w.Code != http.StatusNotFound || w.Body.String() != "{\"error\":\"no tour\"}\n" {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("verified tour", func(t *testing.T) {
		note, err := json.Marshal(committour.Tour{
			Version: 1,
			Title:   "Initial tour",
			Intro:   "Welcome",
			Chunks:  []committour.TourChunk{{Patch: fragments[0], Comment: "Start here"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := committour.WriteNote(dir, hash, note); err != nil {
			t.Fatal(err)
		}
		w := request(hash[:8])
		if w.Code != http.StatusOK {
			t.Fatalf("got %d: %s", w.Code, w.Body.String())
		}
		var response GitTourResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Hash != hash {
			t.Errorf("hash = %q, want %q", response.Hash, hash)
		}
		if string(response.Tour) != string(note) {
			t.Errorf("tour = %s, want %s", response.Tour, note)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["chunks"]; ok {
			t.Fatalf("response still contains chunks: %s", w.Body.String())
		}
	})

	t.Run("chunk-ref note served resolved", func(t *testing.T) {
		ref := 0
		note, err := json.Marshal(committour.Tour{
			Version: 1,
			Chunks:  []committour.TourChunk{{Ref: &ref, Comment: "by reference"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := committour.WriteNote(dir, hash, note); err != nil {
			t.Fatal(err)
		}
		w := request(hash[:8])
		if w.Code != http.StatusOK {
			t.Fatalf("got %d: %s", w.Code, w.Body.String())
		}
		var response GitTourResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		tour, err := committour.ParseTour(response.Tour)
		if err != nil {
			t.Fatal(err)
		}
		if len(tour.Chunks) != 1 || tour.Chunks[0].Ref != nil || tour.Chunks[0].Patch != fragments[0] {
			t.Fatalf("served tour not resolved: %s", response.Tour)
		}
	})
}

func testGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// testGit runs a git command in dir, failing the test on error.
func testGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestHandleGitDiffsLongBranch verifies that all commits ahead of the
// upstream merge-base are returned (not capped at 20) and that the
// merge-base commit itself is included and flagged.
func TestHandleGitDiffsLongBranch(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	testGit(t, dir, "init", "-b", "main")
	testGit(t, dir, "config", "user.name", "Test User")
	testGit(t, dir, "config", "user.email", "test@example.com")

	writeAndCommit := func(name, content, msg string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		testGit(t, dir, "add", name)
		testGit(t, dir, "commit", "--no-verify", "-m", msg)
	}

	writeAndCommit("base.txt", "base\n", "base commit")
	testGit(t, dir, "checkout", "-b", "feature")
	// Local upstream: feature tracks main, so @{upstream} resolves.
	testGit(t, dir, "branch", "--set-upstream-to=main")

	const ahead = 25 // more than the old hard-coded git log -20
	for i := 0; i < ahead; i++ {
		writeAndCommit(fmt.Sprintf("f%d.txt", i), "line1\nline2\n", fmt.Sprintf("feature commit %d", i))
	}
	// Empty commit: header with no numstat block must not break parsing.
	testGit(t, dir, "commit", "--no-verify", "--allow-empty", "-m", "empty commit")

	req := httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs?cwd=%s", dir), nil)
	w := httptest.NewRecorder()
	h.server.handleGitDiffs(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Diffs []GitDiffInfo `json:"diffs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	var commits []GitDiffInfo
	for _, d := range response.Diffs {
		if d.ID != "working" {
			commits = append(commits, d)
		}
	}
	// All commits ahead of the merge-base (including the empty one) plus
	// the merge-base itself.
	if len(commits) < ahead+2 {
		t.Fatalf("expected at least %d commits, got %d", ahead+2, len(commits))
	}
	mbIdx := -1
	for i, c := range commits {
		if c.IsMergeBase {
			mbIdx = i
			break
		}
	}
	if mbIdx != ahead+1 {
		t.Errorf("expected merge-base at index %d, got %d", ahead+1, mbIdx)
	}
	if mbIdx >= 0 && commits[mbIdx].Message != "base commit" {
		t.Errorf("expected merge-base message %q, got %q", "base commit", commits[mbIdx].Message)
	}
	// The empty commit parses cleanly with zero stats.
	if commits[0].Message != "empty commit" || commits[0].FilesCount != 0 || commits[0].Additions != 0 {
		t.Errorf("expected empty commit with zero stats at top, got %q +%d/%d files",
			commits[0].Message, commits[0].Additions, commits[0].FilesCount)
	}
	// Diffstats must be populated for regular commits.
	top := commits[1]
	if top.Additions != 2 || top.FilesCount != 1 {
		t.Errorf("expected commit stats +2/1 file, got +%d/%d files", top.Additions, top.FilesCount)
	}
}

// TestHandleGitDiffsMergeCommitStats verifies merge commits report a
// first-parent diffstat.
func TestHandleGitDiffsMergeCommitStats(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	testGit(t, dir, "init", "-b", "main")
	testGit(t, dir, "config", "user.name", "Test User")
	testGit(t, dir, "config", "user.email", "test@example.com")

	writeAndCommit := func(name, content, msg string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		testGit(t, dir, "add", name)
		testGit(t, dir, "commit", "--no-verify", "-m", msg)
	}

	writeAndCommit("base.txt", "base\n", "base commit")
	testGit(t, dir, "checkout", "-b", "side")
	writeAndCommit("side.txt", "side1\nside2\nside3\n", "side commit")
	testGit(t, dir, "checkout", "main")
	writeAndCommit("main.txt", "main\n", "main commit")
	testGit(t, dir, "merge", "--no-ff", "--no-verify", "-m", "merge side", "side")

	req := httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs?cwd=%s", dir), nil)
	w := httptest.NewRecorder()
	h.server.handleGitDiffs(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Diffs []GitDiffInfo `json:"diffs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	var merge *GitDiffInfo
	for i := range response.Diffs {
		if response.Diffs[i].Message == "merge side" {
			merge = &response.Diffs[i]
			break
		}
	}
	if merge == nil {
		t.Fatal("merge commit not found in diffs")
	}
	// First-parent diff of the merge brings in side.txt (+3, 1 file).
	if merge.Additions != 3 || merge.FilesCount != 1 {
		t.Errorf("expected merge stats +3/1 file, got +%d/%d files", merge.Additions, merge.FilesCount)
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

// TestHandleGitDiffFiles_UntrackedFiles verifies that brand-new (untracked)
// files show up in the working-changes file list as "added", and that deleted
// files show up as "deleted". `git diff --name-status HEAD` omits untracked
// files, so the handler must merge in `git status` output.
func TestHandleGitDiffFiles_UntrackedFiles(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

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

	// Commit a couple of tracked files.
	os.WriteFile(filepath.Join(tempDir, "kept.txt"), []byte("keep\n"), 0o644)
	os.WriteFile(filepath.Join(tempDir, "gone.txt"), []byte("gone\n"), 0o644)
	run("git", "add", "kept.txt", "gone.txt")
	run("git", "commit", "-m", "init\n\nPrompt: test")

	// Working changes: modify kept.txt, delete gone.txt, add a brand-new
	// untracked file.
	os.WriteFile(filepath.Join(tempDir, "kept.txt"), []byte("keep\nmore\n"), 0o644)
	run("git", "rm", "gone.txt")
	os.WriteFile(filepath.Join(tempDir, "brand_new.txt"), []byte("a\nb\nc\n"), 0o644)

	req := httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs/working/files?cwd=%s", tempDir), nil)
	w := httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var files []GitFileInfo
	if err := json.Unmarshal(w.Body.Bytes(), &files); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	byPath := map[string]GitFileInfo{}
	for _, f := range files {
		byPath[f.Path] = f
	}

	// The untracked file must be present and marked added with a +3 numstat.
	newFile, ok := byPath["brand_new.txt"]
	if !ok {
		t.Fatalf("untracked file brand_new.txt missing from working diff; got %+v", files)
	}
	if newFile.Status != "added" {
		t.Errorf("expected brand_new.txt status added, got %q", newFile.Status)
	}
	if newFile.Additions != 3 {
		t.Errorf("expected brand_new.txt additions 3, got %d", newFile.Additions)
	}

	// The deleted file must be present and marked deleted.
	goneFile, ok := byPath["gone.txt"]
	if !ok {
		t.Fatalf("deleted file gone.txt missing from working diff; got %+v", files)
	}
	if goneFile.Status != "deleted" {
		t.Errorf("expected gone.txt status deleted, got %q", goneFile.Status)
	}

	// The modified file must still be present and marked modified.
	keptFile, ok := byPath["kept.txt"]
	if !ok {
		t.Fatalf("modified file kept.txt missing from working diff; got %+v", files)
	}
	if keptFile.Status != "modified" {
		t.Errorf("expected kept.txt status modified, got %q", keptFile.Status)
	}

	// No duplicates.
	if len(files) != 3 {
		t.Errorf("expected exactly 3 files, got %d: %+v", len(files), files)
	}
}

// TestHandleGitDiffFiles_FilenamesWithSpaces verifies that committed files
// whose names contain spaces are listed with their full path. The handler used
// to split --name-status lines on any whitespace, which truncated
// "my file.txt" to "my" and dropped its numstat, making it effectively
// invisible in the diff viewer even though it was part of the commit.
func TestHandleGitDiffFiles_FilenamesWithSpaces(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

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

	os.WriteFile(filepath.Join(tempDir, "my file.txt"), []byte("one\n"), 0o644)
	os.WriteFile(filepath.Join(tempDir, "normal.txt"), []byte("x\n"), 0o644)
	run("git", "add", "my file.txt", "normal.txt")
	run("git", "commit", "-m", "base\n\nPrompt: test")

	// Second commit modifies both files.
	os.WriteFile(filepath.Join(tempDir, "my file.txt"), []byte("one\ntwo\n"), 0o644)
	os.WriteFile(filepath.Join(tempDir, "normal.txt"), []byte("x\ny\n"), 0o644)
	run("git", "add", "my file.txt", "normal.txt")
	run("git", "commit", "-m", "edit\n\nPrompt: test")
	head := run("git", "rev-parse", "HEAD")

	// Inspect the changes introduced by HEAD itself (to=self).
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs/%s/files?cwd=%s&to=self", head, tempDir), nil)
	w := httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var files []GitFileInfo
	if err := json.Unmarshal(w.Body.Bytes(), &files); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	byPath := map[string]GitFileInfo{}
	for _, f := range files {
		byPath[f.Path] = f
	}

	spaced, ok := byPath["my file.txt"]
	if !ok {
		t.Fatalf("committed file \"my file.txt\" missing or path-mangled; got %+v", files)
	}
	if spaced.Status != "modified" {
		t.Errorf("expected \"my file.txt\" status modified, got %q", spaced.Status)
	}
	if spaced.Additions != 1 {
		t.Errorf("expected \"my file.txt\" additions 1, got %d", spaced.Additions)
	}
	if _, ok := byPath["my"]; ok {
		t.Errorf("path was truncated at the space: got bogus entry %q", "my")
	}
	if len(files) != 2 {
		t.Errorf("expected exactly 2 files, got %d: %+v", len(files), files)
	}
}

// TestHandleGitDiffFiles_RenamedFile verifies that a committed rename is listed
// under the new path. The --name-status rename line is "R100\told\tnew"; the
// old whitespace-splitting parser produced a bogus status and the wrong path.
func TestHandleGitDiffFiles_RenamedFile(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

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

	os.WriteFile(filepath.Join(tempDir, "old.txt"), []byte("content\n"), 0o644)
	run("git", "add", "old.txt")
	run("git", "commit", "-m", "base\n\nPrompt: test")

	run("git", "mv", "old.txt", "new name.txt")
	run("git", "commit", "-m", "rename\n\nPrompt: test")
	head := run("git", "rev-parse", "HEAD")

	req := httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs/%s/files?cwd=%s&to=self", head, tempDir), nil)
	w := httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var files []GitFileInfo
	if err := json.Unmarshal(w.Body.Bytes(), &files); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected exactly 1 file for a rename, got %d: %+v", len(files), files)
	}
	if files[0].Path != "new name.txt" {
		t.Errorf("expected renamed file path \"new name.txt\", got %q", files[0].Path)
	}
}

// TestParseNameStatusZ covers the NUL-separated --name-status -z parser,
// including renames (two paths) and filenames containing spaces and tabs.
func TestParseNameStatusZ(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []nameStatusEntry
	}{
		{"empty", "", nil},
		{
			"simple",
			"M\x00normal.txt\x00",
			[]nameStatusEntry{{code: "M", path: "normal.txt"}},
		},
		{
			"space in name",
			"M\x00my file.txt\x00",
			[]nameStatusEntry{{code: "M", path: "my file.txt"}},
		},
		{
			"tab in name",
			"A\x00has\ttab.txt\x00",
			[]nameStatusEntry{{code: "A", path: "has\ttab.txt"}},
		},
		{
			"rename surfaces destination",
			"R100\x00old.txt\x00new name.txt\x00",
			[]nameStatusEntry{{code: "R100", path: "new name.txt"}},
		},
		{
			"copy surfaces destination",
			"C75\x00src.txt\x00copy.txt\x00",
			[]nameStatusEntry{{code: "C75", path: "copy.txt"}},
		},
		{
			"multiple mixed",
			"M\x00a.txt\x00R100\x00b.txt\x00c d.txt\x00D\x00e.txt\x00",
			[]nameStatusEntry{
				{code: "M", path: "a.txt"},
				{code: "R100", path: "c d.txt"},
				{code: "D", path: "e.txt"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseNameStatusZ(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parseNameStatusZ(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("entry %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseNumstatZ covers the per-file numstat parser, including binary files
// (which report "-") and paths containing spaces.
func TestParseNumstatZ(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		in             string
		wantAdd, wantD int
	}{
		{"empty", "", 0, 0},
		{"simple", "3\t1\tfile.txt\x00", 3, 1},
		{"space in path", "2\t0\tmy file.txt\x00", 2, 0},
		{"binary", "-\t-\timage.png\x00", 0, 0},
		{"zero changes", "0\t0\tempty.txt\x00", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			add, del := parseNumstatZ(tc.in)
			if add != tc.wantAdd || del != tc.wantD {
				t.Errorf("parseNumstatZ(%q) = (%d, %d), want (%d, %d)", tc.in, add, del, tc.wantAdd, tc.wantD)
			}
		})
	}
}
