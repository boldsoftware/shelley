package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/gitstate"
)

// conversationListGitCacheTTL bounds how long we'll trust a cache entry without
// any cheap revalidation. The fingerprint check below catches commits,
// checkouts, and resets immediately; the TTL is just a safety net for state
// the fingerprint doesn't cover (e.g. external worktree relocations).
const conversationListGitCacheTTL = 5 * time.Minute

type conversationListGitCacheEntry struct {
	state       *gitstate.GitState
	worktree    string
	expiresAt   time.Time
	gitDir      string // resolved .git directory (may differ from worktree/.git for linked worktrees)
	fingerprint string // cheap signature of HEAD + logs/HEAD + packed-refs
}

type conversationListGitCache struct {
	mu      sync.Mutex
	entries map[string]conversationListGitCacheEntry
}

func newConversationListGitCache() *conversationListGitCache {
	return &conversationListGitCache{entries: make(map[string]conversationListGitCacheEntry)}
}

// get returns the cached entry for cwd if it exists, has not expired, and the
// underlying git fingerprint is unchanged. A stale or invalidated entry is
// evicted so the caller can refresh it.
func (c *conversationListGitCache) get(cwd string, now time.Time) (conversationListGitCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[cwd]
	if !ok {
		return conversationListGitCacheEntry{}, false
	}
	if !now.Before(entry.expiresAt) {
		delete(c.entries, cwd)
		return conversationListGitCacheEntry{}, false
	}
	// Non-repo entries have no fingerprint; trust them until TTL.
	if entry.gitDir != "" && gitFingerprint(entry.gitDir) != entry.fingerprint {
		delete(c.entries, cwd)
		return conversationListGitCacheEntry{}, false
	}
	return entry, true
}

func (c *conversationListGitCache) set(cwd string, entry conversationListGitCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[cwd] = entry
}

func (c *conversationListGitCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]conversationListGitCacheEntry)
}

// resolveGitDir returns the .git directory backing the given worktree. For
// regular repos this is <worktree>/.git; for linked worktrees the .git file
// points at .git/worktrees/<name> in the main repo.
func resolveGitDir(worktree string) (string, error) {
	dotgit := filepath.Join(worktree, ".git")
	fi, err := os.Stat(dotgit)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return dotgit, nil
	}
	data, err := os.ReadFile(dotgit)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return "", fmt.Errorf("unexpected .git file contents: %q", line)
	}
	p := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(dotgit), p)
	}
	return filepath.Clean(p), nil
}

// gitFingerprint produces a cheap signature that changes whenever HEAD moves.
// It reads <gitDir>/HEAD and, if HEAD is a symbolic ref, the file that ref
// points to (loose or packed). No subprocess, at most two tiny file reads.
func gitFingerprint(gitDir string) string {
	if gitDir == "" {
		return ""
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(head))
	if !strings.HasPrefix(line, "ref:") {
		// Detached HEAD: the commit hash is right there.
		return line
	}
	ref := strings.TrimSpace(strings.TrimPrefix(line, "ref:"))
	// Loose refs live under the per-worktree gitDir for HEAD; everything
	// else (incl. packed-refs) is in the common dir for linked worktrees.
	commonDir := gitDir
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		cd := strings.TrimSpace(string(data))
		if !filepath.IsAbs(cd) {
			cd = filepath.Join(gitDir, cd)
		}
		commonDir = filepath.Clean(cd)
	}
	if data, err := os.ReadFile(filepath.Join(commonDir, ref)); err == nil {
		return line + "|" + strings.TrimSpace(string(data))
	}
	// Ref isn't loose; look it up in packed-refs.
	if data, err := os.ReadFile(filepath.Join(commonDir, "packed-refs")); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "^") {
				continue
			}
			if sp := strings.IndexByte(l, ' '); sp > 0 && l[sp+1:] == ref {
				return line + "|" + l[:sp]
			}
		}
	}
	return line
}
