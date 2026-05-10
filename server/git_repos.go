package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// GitRepoInfo describes a git repository discovered on disk.
type GitRepoInfo struct {
	Path     string `json:"path"`
	Branch   string `json:"branch,omitempty"`
	Worktree bool   `json:"worktree,omitempty"`
	// LastActivity is unix seconds of the most recent activity inside the
	// repo's gitdir, approximated by max(mtime) over HEAD, index,
	// FETCH_HEAD. 0 when none of those could be stat'd.
	LastActivity int64 `json:"last_activity,omitempty"`
}

// GitReposResponse is the response from /api/git-repos.
type GitReposResponse struct {
	Repos     []GitRepoInfo `json:"repos"`
	Roots     []string      `json:"roots"`
	Truncated bool          `json:"truncated,omitempty"`
	ElapsedMs int64         `json:"elapsed_ms"`
}

// Names we never descend into. Heavy build artifacts, caches, and other
// places git repos basically never live. The list is deliberately short:
// false negatives are recoverable (use the cwd input directly); false
// positives just slow the crawl down.
var crawlSkipNames = map[string]struct{}{
	"node_modules":  {},
	".git":          {},
	".hg":           {},
	".svn":          {},
	"vendor":        {},
	"target":        {},
	"build":         {},
	"dist":          {},
	"out":           {},
	".next":         {},
	".nuxt":         {},
	".cache":        {},
	".gradle":       {},
	".idea":         {},
	".vscode":       {},
	"__pycache__":   {},
	".venv":         {},
	"venv":          {},
	"env":           {},
	".tox":          {},
	".mypy_cache":   {},
	".pytest_cache": {},
	"Library":       {}, // macOS user Library
	"Trash":         {},
	".Trash":        {},
	".local":        {},
	"go-build":      {},
	".rustup":       {},
	".cargo":        {},
	".npm":          {},
	".pnpm-store":   {},
	".yarn":         {},
	".docker":       {},
	"Pods":          {},
	".DerivedData":  {},
	"DerivedData":   {},
	"Pictures":      {},
	"Music":         {},
	"Videos":        {},
	"Downloads":     {},
	".m2":           {},
	".bundle":       {},
	".pyenv":        {},
	".nvm":          {},
	".rbenv":        {},
}

const (
	gitRepoCrawlBudget     = 4 * time.Second
	gitRepoCrawlMaxResults = 2000
	gitRepoCrawlMaxDepth   = 8
)

// dirReader abstracts directory listing so the crawler can be exercised
// against an in-memory fstest.MapFS in tests. Real callers pass osDirReader
// which uses os.ReadDir (which returns []fs.DirEntry).
type dirReader func(absPath string) ([]fs.DirEntry, error)

func osDirReader(p string) ([]fs.DirEntry, error) { return os.ReadDir(p) }

// crawlGitRepos walks the filesystem starting from roots with bounded
// concurrency and returns every git working tree it finds. It does not
// descend into a repo once found — submodules and nested repos are not
// enumerated. The goal is "pick a working directory", not exhaustive
// enumeration.
func crawlGitRepos(ctx context.Context, roots []string) ([]GitRepoInfo, bool) {
	return crawlGitReposWithReader(ctx, roots, osDirReader, inspectGitRepo)
}

func crawlGitReposWithReader(
	ctx context.Context,
	roots []string,
	readDir dirReader,
	inspect func(string) (GitRepoInfo, bool),
) ([]GitRepoInfo, bool) {
	workers := runtime.GOMAXPROCS(0) * 2
	if workers < 4 {
		workers = 4
	}
	if workers > 32 {
		workers = 32
	}
	sem := make(chan struct{}, workers)

	var (
		mu        sync.Mutex
		out       []GitRepoInfo
		seen      = make(map[string]struct{})
		truncated bool
		wg        sync.WaitGroup
	)

	// addRepo returns false when we've hit the result cap and the caller
	// should stop. It also de-duplicates across roots.
	addRepo := func(repo GitRepoInfo) bool {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := seen[repo.Path]; ok {
			return true
		}
		if len(out) >= gitRepoCrawlMaxResults {
			truncated = true
			return false
		}
		seen[repo.Path] = struct{}{}
		out = append(out, repo)
		return true
	}

	var visit func(p string, depth int)
	visit = func(p string, depth int) {
		defer wg.Done()
		if ctx.Err() != nil {
			return
		}
		// Bound concurrency; release before recursing into children so we
		// don't sit on a slot while spawning child work.
		sem <- struct{}{}
		info, isRepo := inspect(p)
		var entries []fs.DirEntry
		var err error
		if !isRepo {
			entries, err = readDir(p)
		}
		<-sem

		if isRepo {
			info.Path = p
			addRepo(info)
			return // never descend into a repo
		}
		if err != nil {
			return // unreadable, permission denied, etc.
		}
		if depth >= gitRepoCrawlMaxDepth {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if _, skip := crawlSkipNames[name]; skip {
				continue
			}
			if strings.HasPrefix(name, ".") {
				// Dot-dirs below the root are usually caches and almost never
				// hold working trees. Skipping them is the single biggest win.
				continue
			}
			// Cheap cap check: don't spawn more work once we've already
			// hit the result limit.
			mu.Lock()
			done := len(out) >= gitRepoCrawlMaxResults
			if done {
				truncated = true
			}
			mu.Unlock()
			if done {
				return
			}
			wg.Add(1)
			go visit(filepath.Join(p, name), depth+1)
		}
	}

	for _, r := range roots {
		wg.Add(1)
		go visit(r, 0)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		mu.Lock()
		truncated = true
		mu.Unlock()
		<-done // let in-flight goroutines drain; they check ctx and bail fast
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, truncated
}

// gitInfoFast preserves the older signature for tests that only care
// about repo-ness / branch / worktree.
func gitInfoFast(dirPath string) (branch string, isRepo, isWorktree bool) {
	info, ok := inspectGitRepo(dirPath)
	if !ok {
		return "", false, false
	}
	return info.Branch, true, info.Worktree
}

// inspectGitRepo stats .git, parses HEAD, and returns the repo metadata
// we surface in the picker. Cheap: a Lstat plus at most three more
// stats and one short file read. No git fork.
func inspectGitRepo(dirPath string) (GitRepoInfo, bool) {
	gitPath := filepath.Join(dirPath, ".git")
	fi, err := os.Lstat(gitPath)
	if err != nil {
		return GitRepoInfo{}, false
	}
	info := GitRepoInfo{Path: dirPath}
	var gitDir string
	switch {
	case fi.IsDir():
		gitDir = gitPath
	case fi.Mode().IsRegular():
		// Linked worktree: .git is a file containing "gitdir: /absolute/path".
		data, err := os.ReadFile(gitPath)
		if err != nil {
			return GitRepoInfo{}, false
		}
		line := strings.TrimSpace(string(data))
		if !strings.HasPrefix(line, "gitdir:") {
			return GitRepoInfo{}, false
		}
		info.Worktree = true
		gitDir = strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(dirPath, gitDir)
		}
	default:
		return GitRepoInfo{}, false
	}
	// Read HEAD to find current branch.
	headPath := filepath.Join(gitDir, "HEAD")
	if data, err := os.ReadFile(headPath); err == nil {
		s := strings.TrimSpace(string(data))
		if strings.HasPrefix(s, "ref: refs/heads/") {
			info.Branch = strings.TrimPrefix(s, "ref: refs/heads/")
		} else if len(s) >= 7 {
			info.Branch = s[:7] // detached HEAD: show short sha
		}
	}
	// Most recent activity = max mtime of HEAD (commits/checkouts/resets),
	// index (add/rm/commit), FETCH_HEAD (fetches/pulls). HEAD is in the
	// per-worktree gitDir; index lives in the worktree gitDir; FETCH_HEAD
	// in the common dir, but for linked worktrees git still updates a
	// FETCH_HEAD under the per-worktree gitdir on fetch. Stat what we can.
	var best time.Time
	for _, name := range [...]string{"HEAD", "index", "FETCH_HEAD"} {
		if st, err := os.Stat(filepath.Join(gitDir, name)); err == nil {
			if m := st.ModTime(); m.After(best) {
				best = m
			}
		}
	}
	if !best.IsZero() {
		info.LastActivity = best.Unix()
	}
	return info, true
}

// handleGitRepos crawls the filesystem (under $HOME by default) for git
// repositories and returns them as a flat list, so the UI can present a
// nice fuzzy-filterable picker instead of a tree.
func (s *Server) handleGitRepos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	start := time.Now()

	// Roots: ?root=... can repeat. Default is the user's home directory.
	roots := r.URL.Query()["root"]
	if len(roots) == 0 {
		if home, err := os.UserHomeDir(); err == nil {
			roots = []string{home}
		} else {
			roots = []string{"/"}
		}
	}
	// Clean + dedupe roots, drop unreadable entries.
	cleaned := make([]string, 0, len(roots))
	seenRoot := map[string]bool{}
	for _, root := range roots {
		root = filepath.Clean(root)
		if seenRoot[root] {
			continue
		}
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		seenRoot[root] = true
		cleaned = append(cleaned, root)
	}

	ctx, cancel := context.WithTimeout(r.Context(), gitRepoCrawlBudget)
	defer cancel()

	repos, truncated := crawlGitRepos(ctx, cleaned)

	resp := GitReposResponse{
		Repos:     repos,
		Roots:     cleaned,
		Truncated: truncated,
		ElapsedMs: time.Since(start).Milliseconds(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
