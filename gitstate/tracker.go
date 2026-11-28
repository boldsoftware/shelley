package gitstate

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// Tracker detects git state changes in a conversation's repositories: the
// one holding its working directory, and those its commands ran git in,
// which the working directory need not contain.
type Tracker struct {
	cwd func() string

	mu sync.Mutex
	// holds maps each watched directory, one git ran in or the repository
	// holding it, to the number of running commands that touched it. It is
	// watched until the first check after that is zero.
	holds map[string]int
	// states maps each worktree to its state when last reported or, if
	// never reported, first seen.
	states map[string]*GitState
}

// NewTracker returns a Tracker for the working directory that cwd returns.
func NewTracker(cwd func() string) *Tracker {
	t := &Tracker{cwd: cwd, holds: map[string]int{}, states: map[string]*GitState{}}
	if state := stateOf(cwd()); state.IsRepo {
		t.states[state.Worktree] = state
	}
	return t
}

// stateOf returns the git state of dir, resolving symlinks so that each
// repository has one worktree path, spelled as git spells it.
func stateOf(dir string) *GitState {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return GetGitState(dir)
}

// Touch notes that a command that runs git in dir, which need not exist
// yet, is about to start. Call release once it exits.
func (t *Tracker) Touch(dir string) (release func()) {
	// Watch dir, which may become a repository, and the one holding it,
	// which outlives dir.
	dirs := []string{filepath.Clean(dir)}
	state := stateOf(dir)
	if state.IsRepo && state.Worktree != dirs[0] {
		dirs = append(dirs, state.Worktree)
	}
	cwd := stateOf(t.cwd())
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, watched := t.holds[state.Worktree]; state.IsRepo && !watched && state.Worktree != cwd.Worktree {
		// Changes made since it was last watched are not the conversation's.
		t.states[state.Worktree] = state
	}
	for _, d := range dirs {
		t.holds[d]++
	}
	return sync.OnceFunc(func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		for _, d := range dirs {
			t.holds[d]--
		}
	})
}

// Changes returns the states of the watched repositories, and the one
// holding the working directory, that differ from their state when last
// reported or first seen. A repository is new, and so reported, when the
// working directory moves into it or a watched directory becomes it.
func (t *Tracker) Changes() []*GitState {
	t.mu.Lock()
	defer t.mu.Unlock()
	var changes []*GitState
	report := func(state *GitState) {
		if last := t.states[state.Worktree]; state.IsRepo && !last.Equal(state) {
			t.states[state.Worktree] = state
			changes = append(changes, state)
		}
	}
	report(stateOf(t.cwd()))
	for _, dir := range slices.Sorted(maps.Keys(t.holds)) {
		// A missing directory would look up the repository around it.
		if _, err := os.Stat(dir); err == nil {
			report(stateOf(dir))
		}
		if t.holds[dir] == 0 {
			delete(t.holds, dir)
		}
	}
	return changes
}
