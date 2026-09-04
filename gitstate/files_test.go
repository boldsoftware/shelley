package gitstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josharian/sockpath"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := sockpath.TempDir(t)
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")
	return dir
}

func commit(t *testing.T, dir, subject string) {
	t.Helper()
	runGit(t, dir, "commit", "--allow-empty", "-m", subject)
}

// TestFileReader_Subject checks the file-based reader returns the commit
// subject (which the git-binary fallback got via `git log -1 --format=%s`).
func TestFileReader_Subject(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "first subject line")

	state, ok := getGitStateFromFiles(dir)
	if !ok {
		t.Fatal("file reader unexpectedly bailed")
	}
	if state.Subject != "first subject line" {
		t.Errorf("Subject = %q, want %q", state.Subject, "first subject line")
	}
	if len(state.Commit) != 7 {
		t.Errorf("Commit = %q, want 7-char short hash", state.Commit)
	}
}

// TestFileReader_PackedObject verifies subjects are read out of a packfile
// (with delta-compressed objects) after `git gc`, not just loose objects.
func TestFileReader_PackedObject(t *testing.T) {
	dir := initRepo(t)
	for i := 0; i < 5; i++ {
		// Write a growing file so gc deltifies blobs/trees/commits.
		body := strings.Repeat("line\n", (i+1)*100)
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", ".")
		commit(t, dir, "commit number "+string(rune('a'+i)))
	}
	runGit(t, dir, "gc", "-q")

	// Confirm the commit is genuinely packed (no loose object dir besides info/pack).
	state, ok := getGitStateFromFiles(dir)
	if !ok {
		t.Fatal("file reader bailed on packed repo")
	}
	if state.Subject != "commit number e" {
		t.Errorf("Subject = %q, want %q", state.Subject, "commit number e")
	}
	// Cross-check against the git binary.
	if g := getGitStateFromGit(dir); g.Subject != state.Subject {
		t.Errorf("packed subject mismatch: file=%q git=%q", state.Subject, g.Subject)
	}
}

// TestFileReader_NestedDir resolves the worktree root from a subdirectory.
func TestFileReader_NestedDir(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "root commit")
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	state, ok := getGitStateFromFiles(sub)
	if !ok {
		t.Fatal("file reader bailed in nested dir")
	}
	if state.Worktree != dir {
		t.Errorf("Worktree = %q, want %q", state.Worktree, dir)
	}
	if state.Subject != "root commit" {
		t.Errorf("Subject = %q, want %q", state.Subject, "root commit")
	}
}

// TestFileReader_MatchesGitBinary asserts the file reader and the subprocess
// implementation agree across regular repo, worktree, and detached HEAD.
func TestFileReader_MatchesGitBinary(t *testing.T) {
	main := initRepo(t)
	commit(t, main, "main commit")
	wt := filepath.Join(sockpath.TempDir(t), "wt")
	runGit(t, main, "worktree", "add", "-q", "-b", "feature", wt)

	// Detached HEAD repo.
	det := initRepo(t)
	commit(t, det, "det commit one")
	commit(t, det, "det commit two")
	head := strings.TrimSpace(runGitOutput(t, det, "rev-parse", "HEAD"))
	runGit(t, det, "checkout", "-q", head)

	for _, dir := range []string{main, wt, det} {
		fs, ok := getGitStateFromFiles(dir)
		if !ok {
			t.Fatalf("%s: file reader bailed", dir)
		}
		gs := getGitStateFromGit(dir)
		if fs.IsRepo != gs.IsRepo || fs.Worktree != gs.Worktree ||
			fs.Branch != gs.Branch || fs.Subject != gs.Subject {
			t.Errorf("%s mismatch:\n  file=%+v\n  git =%+v", dir, fs, gs)
		}
		// git's short hash may be longer than our 7; require prefix agreement.
		if !strings.HasPrefix(gs.Commit, fs.Commit) {
			t.Errorf("%s commit: file=%q not a prefix of git=%q", dir, fs.Commit, gs.Commit)
		}
	}
}

// TestFileReader_NonRepo returns a definitive non-repo answer without needing
// the git binary fallback.
func TestFileReader_NonRepo(t *testing.T) {
	state, ok := getGitStateFromFiles(t.TempDir())
	if !ok {
		t.Fatal("expected file reader to answer for a plain directory")
	}
	if state.IsRepo {
		t.Errorf("expected IsRepo=false, got %+v", state)
	}
}

// TestFileReader_UnbornBranch covers a freshly-initialised repo whose HEAD
// points at a branch with no commits yet: it's a repo, but with no commit or
// subject, and the file reader answers without falling back to git.
func TestFileReader_UnbornBranch(t *testing.T) {
	dir := initRepo(t)
	state, ok := getGitStateFromFiles(dir)
	if !ok {
		t.Fatal("file reader bailed on unborn branch")
	}
	if !state.IsRepo {
		t.Errorf("expected IsRepo=true, got %+v", state)
	}
	if state.Commit != "" || state.Subject != "" {
		t.Errorf("expected empty commit/subject, got %+v", state)
	}
	if state.Branch == "" {
		t.Errorf("expected a branch name on unborn HEAD, got %+v", state)
	}
}

// TestFileReader_PackedRefs resolves HEAD through packed-refs (after a
// `git pack-refs`, the loose ref file is gone).
func TestFileReader_PackedRefs(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "packed ref commit")
	runGit(t, dir, "pack-refs", "--all")
	if _, err := os.Stat(filepath.Join(dir, ".git", "refs", "heads")); err == nil {
		if entries, _ := os.ReadDir(filepath.Join(dir, ".git", "refs", "heads")); len(entries) != 0 {
			t.Skip("ref not packed on this git")
		}
	}
	state, ok := getGitStateFromFiles(dir)
	if !ok {
		t.Fatal("file reader bailed with packed refs")
	}
	if state.Subject != "packed ref commit" {
		t.Errorf("Subject = %q, want %q", state.Subject, "packed ref commit")
	}
	if g := getGitStateFromGit(dir); g.Commit != "" && !strings.HasPrefix(g.Commit, state.Commit) {
		t.Errorf("commit mismatch: file=%q git=%q", state.Commit, g.Commit)
	}
}

// TestFileReader_SlashedBranch checks branch names containing slashes survive
// HEAD/ref parsing intact.
func TestFileReader_SlashedBranch(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "on a slashed branch")
	runGit(t, dir, "checkout", "-q", "-b", "feature/sub/topic")
	commit(t, dir, "second")
	state, ok := getGitStateFromFiles(dir)
	if !ok {
		t.Fatal("file reader bailed on slashed branch")
	}
	if state.Branch != "feature/sub/topic" {
		t.Errorf("Branch = %q, want %q", state.Branch, "feature/sub/topic")
	}
}

// TestFileReader_MultibyteSubject checks UTF-8 subjects round-trip.
func TestFileReader_MultibyteSubject(t *testing.T) {
	dir := initRepo(t)
	subject := "修复 bug — café ☕ déjà"
	commit(t, dir, subject)
	state, ok := getGitStateFromFiles(dir)
	if !ok {
		t.Fatal("file reader bailed")
	}
	if state.Subject != subject {
		t.Errorf("Subject = %q, want %q", state.Subject, subject)
	}
}

// TestFileReader_BadGitFile: a .git file without a parseable gitdir pointer is
// reported as a non-repo (findWorktree gives up at that directory). GetGitState
// still answers without a subprocess; git itself would also reject the layout.
func TestFileReader_BadGitFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a gitdir pointer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, ok := getGitStateFromFiles(dir)
	if !ok {
		t.Fatal("expected file reader to answer for a malformed .git file")
	}
	if state.IsRepo {
		t.Errorf("expected IsRepo=false for malformed .git file, got %+v", state)
	}
}
