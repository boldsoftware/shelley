// Package gitstate provides utilities for tracking git repository state.
package gitstate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitState represents the current state of a git repository.
type GitState struct {
	// Worktree is the absolute path to the worktree root.
	// For regular repos, this is the same as the git root.
	// For worktrees, this is the worktree directory.
	Worktree string

	// Branch is the current branch name, or empty if detached HEAD.
	Branch string

	// Commit is the current commit hash (short form).
	Commit string

	// Subject is the commit message subject line.
	Subject string

	// IsRepo is true if the directory is inside a git repository.
	IsRepo bool
}

// GetGitState returns the git state for the given directory.
// If dir is empty, uses the current working directory.
//
// It reads the repository's files directly (no git subprocess), which is fast
// enough to call inline while building the conversation list -- spawning git
// per repo cost ~15ms each and seconds across the dozens of repos a long-lived
// install accumulates. If the on-disk layout is something the lightweight
// reader doesn't understand, it falls back to the git binary so the result is
// always correct.
func GetGitState(dir string) *GitState {
	if state, ok := getGitStateFromFiles(dir); ok {
		return state
	}
	return getGitStateFromGit(dir)
}

// getGitStateFromFiles computes the git state by reading .git directly. The
// bool result is false when the directory isn't recognisable as a repo via
// files or the layout needs the git binary (e.g. an unsupported ref form);
// callers then fall back to getGitStateFromGit. A directory that is genuinely
// not a repo returns (non-repo state, true) so we don't waste a subprocess
// confirming it.
func getGitStateFromFiles(dir string) (*GitState, bool) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, false
		}
		dir = wd
	}
	worktree, gitDir, ok := findWorktree(dir)
	if !ok {
		return &GitState{}, true // definitively not in a repo
	}
	commonDir := resolveCommonDir(gitDir)

	branch, commit, ok := resolveHead(gitDir, commonDir)
	if !ok {
		return nil, false
	}
	state := &GitState{IsRepo: true, Worktree: worktree, Branch: branch}
	if commit != "" {
		state.Commit = shortHash(commit)
		if subject, err := readCommitSubject(commonDir, commit); err == nil {
			state.Subject = subject
		} else {
			return nil, false // couldn't decode the commit; let git handle it
		}
	}
	return state, true
}

// shortHash abbreviates a full SHA-1 to git's default 7-character short form.
func shortHash(full string) string {
	if len(full) >= 7 {
		return full[:7]
	}
	return full
}

// findWorktree walks up from dir to the worktree root (the directory holding
// .git) and returns it along with the resolved .git directory.
func findWorktree(dir string) (worktree, gitDir string, ok bool) {
	dir = filepath.Clean(dir)
	for {
		dotgit := filepath.Join(dir, ".git")
		if fi, err := os.Stat(dotgit); err == nil {
			if fi.IsDir() {
				return dir, dotgit, true
			}
			if gd, err := gitDirFromFile(dotgit); err == nil {
				return dir, gd, true
			}
			return "", "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		dir = parent
	}
}

// gitDirFromFile resolves the "gitdir: <path>" pointer in a linked worktree's
// .git file to an absolute path.
func gitDirFromFile(dotgit string) (string, error) {
	data, err := os.ReadFile(dotgit)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(data))
	p, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", errors.New("unexpected .git file contents")
	}
	p = strings.TrimSpace(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(dotgit), p)
	}
	return filepath.Clean(p), nil
}

// resolveCommonDir returns the shared object/refs directory for gitDir. For a
// linked worktree, gitDir/commondir points at the main repo's .git; loose
// objects and packed-refs live there, while HEAD is per-worktree in gitDir.
func resolveCommonDir(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	cd := strings.TrimSpace(string(data))
	if !filepath.IsAbs(cd) {
		cd = filepath.Join(gitDir, cd)
	}
	return filepath.Clean(cd)
}

// resolveHead reads HEAD and returns the branch (empty if detached) and the
// full commit hash it points at. ok is false if HEAD can't be resolved to a
// commit via files (e.g. an unborn branch), so the caller falls back to git.
func resolveHead(gitDir, commonDir string) (branch, commit string, ok bool) {
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", "", false
	}
	line := strings.TrimSpace(string(head))
	ref, isSymbolic := strings.CutPrefix(line, "ref:")
	if !isSymbolic {
		// Detached HEAD: the line is the commit hash itself.
		if isHexHash(line) {
			return "", line, true
		}
		return "", "", false
	}
	ref = strings.TrimSpace(ref)
	branch = strings.TrimPrefix(ref, "refs/heads/")
	commit, ok = resolveRef(gitDir, commonDir, ref)
	if !ok {
		// An unborn branch (ref points nowhere yet) has no commit; treat that
		// as a repo with no commit rather than failing over to git.
		return branch, "", true
	}
	return branch, commit, true
}

// resolveRef resolves a ref name to a full commit hash, checking the loose ref
// file first (per-worktree HEAD refs may live in gitDir) and then packed-refs.
func resolveRef(gitDir, commonDir, ref string) (string, bool) {
	for _, base := range []string{gitDir, commonDir} {
		if data, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(ref))); err == nil {
			h := strings.TrimSpace(string(data))
			if isHexHash(h) {
				return h, true
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(commonDir, "packed-refs")); err == nil {
		for l := range strings.SplitSeq(string(data), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || l[0] == '#' || l[0] == '^' {
				continue
			}
			if sp := strings.IndexByte(l, ' '); sp > 0 && l[sp+1:] == ref {
				if h := l[:sp]; isHexHash(h) {
					return strings.Clone(h), true
				}
			}
		}
	}
	return "", false
}

// isHexHash reports whether s is a 40-character lowercase-or-uppercase hex
// SHA-1.
func isHexHash(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// getGitStateFromGit is the subprocess implementation, used as a fallback when
// the file-based reader can't interpret the repository layout.
func getGitStateFromGit(dir string) *GitState {
	state := &GitState{}

	// Get the worktree root (this works for both regular repos and worktrees)
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.Output()
	if err != nil {
		// Not in a git repository
		return state
	}
	state.IsRepo = true
	state.Worktree = strings.TrimSpace(string(output))

	// Get the current commit hash (short form)
	cmd = exec.Command("git", "rev-parse", "--short", "HEAD")
	if dir != "" {
		cmd.Dir = dir
	}
	output, err = cmd.Output()
	if err == nil {
		state.Commit = strings.TrimSpace(string(output))
	}

	// Get the commit subject line
	cmd = exec.Command("git", "log", "-1", "--format=%s")
	if dir != "" {
		cmd.Dir = dir
	}
	output, err = cmd.Output()
	if err == nil {
		state.Subject = strings.TrimSpace(string(output))
	}

	// Get the current branch name
	// First try symbolic-ref for normal branches
	cmd = exec.Command("git", "symbolic-ref", "--short", "HEAD")
	if dir != "" {
		cmd.Dir = dir
	}
	output, err = cmd.Output()
	if err == nil {
		state.Branch = strings.TrimSpace(string(output))
	}
	// If symbolic-ref fails, we're in detached HEAD state - branch stays empty

	return state
}

// Equal reports whether g and other represent the same git state.
func (g *GitState) Equal(other *GitState) bool {
	if g == nil && other == nil {
		return true
	}
	if g == nil || other == nil {
		return false
	}
	return g.Worktree == other.Worktree &&
		g.Branch == other.Branch &&
		g.Commit == other.Commit &&
		g.Subject == other.Subject &&
		g.IsRepo == other.IsRepo
}

// tildeReplace replaces the home directory prefix with ~ for display.
func tildeReplace(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// String returns a human-readable description of the git state change.
// It's designed to be shown to users, not the LLM.
func (g *GitState) String() string {
	if g == nil || !g.IsRepo {
		return ""
	}

	worktreePath := tildeReplace(g.Worktree)
	subject := g.Subject
	if len(subject) > 50 {
		subject = subject[:47] + "..."
	}

	if g.Branch != "" {
		return worktreePath + " (" + g.Branch + ") now at " + g.Commit + " \"" + subject + "\""
	}
	return worktreePath + " (detached) now at " + g.Commit + " \"" + subject + "\""
}
