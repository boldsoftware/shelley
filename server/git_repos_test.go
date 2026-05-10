package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

func mkRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", "-b", "main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
}

func TestCrawlGitRepos(t *testing.T) {
	root := t.TempDir()
	// Three repos at varying depths.
	mkRepo(t, filepath.Join(root, "a"))
	mkRepo(t, filepath.Join(root, "nested", "b"))
	mkRepo(t, filepath.Join(root, "nested", "deeper", "c"))
	// A non-repo dir.
	if err := os.MkdirAll(filepath.Join(root, "not-a-repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	// node_modules deep inside should be skipped (and not visited).
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkRepo(t, filepath.Join(root, "node_modules", "x", "should-be-skipped"))
	// Dot-directory below root should be skipped.
	mkRepo(t, filepath.Join(root, ".cache", "dotrepo"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repos, truncated := crawlGitRepos(ctx, []string{root})
	if truncated {
		t.Errorf("unexpected truncation")
	}
	paths := make([]string, len(repos))
	for i, r := range repos {
		paths[i] = r.Path
	}
	want := []string{
		filepath.Join(root, "a"),
		filepath.Join(root, "nested", "b"),
		filepath.Join(root, "nested", "deeper", "c"),
	}
	for _, w := range want {
		if !slices.Contains(paths, w) {
			t.Errorf("missing repo %q in %v", w, paths)
		}
	}
	for _, p := range paths {
		if filepath.Base(p) == "should-be-skipped" {
			t.Errorf("crawler descended into node_modules: %q", p)
		}
		if filepath.Base(p) == "dotrepo" {
			t.Errorf("crawler descended into dot-dir: %q", p)
		}
	}
	// Branch should be reported as "main".
	for _, r := range repos {
		if r.Branch != "main" {
			t.Errorf("repo %q branch=%q, want main", r.Path, r.Branch)
		}
	}
}

func TestGitInfoFastWorktree(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "main")
	mkRepo(t, repo)
	// Make at least one commit so worktree add doesn't complain.
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", repo, "add", "f"},
		{"-C", repo, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-qm", "."},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	wt := filepath.Join(root, "wt")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "feature", wt).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	branch, isRepo, isWorktree := gitInfoFast(wt)
	if !isRepo {
		t.Fatalf("worktree not detected as repo")
	}
	if !isWorktree {
		t.Errorf("worktree not marked as worktree")
	}
	if branch != "feature" {
		t.Errorf("branch=%q want feature", branch)
	}
}

func TestHandleGitReposJSON(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "a"))
	mkRepo(t, filepath.Join(root, "b"))

	s := &Server{}
	req := httptest.NewRequest("GET", "/api/git/repos?root="+root, nil)
	rr := httptest.NewRecorder()
	s.handleGitRepos(rr, req)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp GitReposResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Repos) != 2 {
		t.Errorf("got %d repos, want 2: %+v", len(resp.Repos), resp.Repos)
	}
	if len(resp.Roots) != 1 || resp.Roots[0] != root {
		t.Errorf("roots=%v", resp.Roots)
	}
}

// TestCrawlGitReposConcurrent exercises the bounded-concurrency walker
// against an in-memory tree. It also asserts the crawler never descends
// into a repo and never visits a skip-listed directory.
func TestCrawlGitReposConcurrent(t *testing.T) {
	mapFS := fstest.MapFS{}
	repoPaths := map[string]bool{}
	for i := 0; i < 50; i++ {
		top := "top" + itoa(i)
		for j := 0; j < 10; j++ {
			child := top + "/c" + itoa(j)
			mapFS[child] = &fstest.MapFile{Mode: fs.ModeDir}
			if j%2 == 0 {
				repoPaths["/root/"+child] = true
			}
		}
	}
	mapFS["top0/node_modules"] = &fstest.MapFile{Mode: fs.ModeDir}
	mapFS["top0/node_modules/skipme"] = &fstest.MapFile{Mode: fs.ModeDir}

	var visits atomic.Int64
	reader := func(absPath string) ([]fs.DirEntry, error) {
		visits.Add(1)
		rel := strings.TrimPrefix(absPath, "/root")
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			rel = "."
		}
		if strings.Contains(absPath, "node_modules") {
			t.Errorf("crawler should not have read %q", absPath)
		}
		return mapFS.ReadDir(rel)
	}
	inspect := func(absPath string) (GitRepoInfo, bool) {
		if repoPaths[absPath] {
			return GitRepoInfo{Branch: "main"}, true
		}
		return GitRepoInfo{}, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repos, truncated := crawlGitReposWithReader(ctx, []string{"/root"}, reader, inspect)
	if truncated {
		t.Errorf("unexpected truncation")
	}
	if len(repos) != len(repoPaths) {
		t.Errorf("got %d repos, want %d", len(repos), len(repoPaths))
	}
	got := make([]string, len(repos))
	for i, r := range repos {
		got[i] = r.Path
	}
	sort.Strings(got)
	for p := range repoPaths {
		if !slices.Contains(got, p) {
			t.Errorf("missing %q", p)
		}
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var buf [10]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = digits[i%10]
		i /= 10
	}
	return string(buf[pos:])
}

func TestInspectGitRepoLastActivity(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, root)
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", root, "add", "f"},
		{"-C", root, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-qm", "."},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	info, ok := inspectGitRepo(root)
	if !ok {
		t.Fatal("not detected as repo")
	}
	if info.LastActivity == 0 {
		t.Fatalf("expected non-zero last_activity")
	}
	delta := time.Now().Unix() - info.LastActivity
	if delta < 0 || delta > 60 {
		t.Errorf("last_activity=%d (delta=%ds) not recent", info.LastActivity, delta)
	}
}
