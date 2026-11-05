// Package commitnotes stores Shelley's annotations of Git commits as git notes.
package commitnotes

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

// Repo makes a git command with args that runs in a repository, which may
// be on another machine. Errors are matched on their English text, so the
// command must run git with LC_ALL=C.
type Repo func(args ...string) *exec.Cmd

// Local is the repository at dir on this machine. Repo-locating variables
// are stripped from git's environment so dir always wins.
func Local(dir string) Repo {
	return func(args ...string) *exec.Cmd {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = gitEnv()
		return cmd
	}
}

// highlightsNotesRef is the notes ref holding commit highlights.
const highlightsNotesRef = "shelley-highlights"

// CommitHash resolves rev to a full commit hash.
func CommitHash(repo Repo, rev string) (string, error) {
	hashBytes, err := gitOutput(repo, nil, "rev-parse", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	hash := strings.TrimSpace(string(hashBytes))
	if hash == "" || strings.ContainsAny(hash, "\r\n") {
		return "", fmt.Errorf("git rev-parse returned invalid hash %q", hash)
	}
	return hash, nil
}

// ErrNoNote reports that a commit has no note under the requested ref.
var ErrNoNote = errors.New("no note")

func readNote(repo Repo, ref, commit string) ([]byte, error) {
	data, err := gitOutput(repo, nil, "notes", "--ref="+ref, "show", commit)
	if err != nil {
		if strings.Contains(err.Error(), "no note found") {
			return nil, fmt.Errorf("%w for %s", ErrNoNote, commit)
		}
		return nil, err
	}
	return data, nil
}

func writeNote(repo Repo, ref, commit string, data []byte) error {
	return updateNotes(repo, ref, commit, data)
}

// updateNotes atomically adds note as commit's note in the notes ref
// named notes.
// git notes overwrites its ref without checking the old value, so
// concurrent writers (parallel subagents annotating different commits)
// would silently drop each other's notes. Instead the edits run against a
// private copy of the ref, which is then compare-and-swapped in; on
// contention the whole update is redone from the new value.
func updateNotes(repo Repo, notes, commit string, note []byte) error {
	deadline := time.Now().Add(15 * time.Second)
	backoff := 10 * time.Millisecond
	for {
		err := tryUpdateNotes(repo, notes, commit, note)
		if err == nil || time.Now().After(deadline) || !strings.Contains(err.Error(), "cannot lock ref") {
			return err
		}
		// Jitter keeps writers that lost the same round from colliding again.
		time.Sleep(backoff/2 + rand.N(backoff))
		backoff = min(2*backoff, time.Second)
	}
}

// tmpNotesRefs numbers this process's private notes refs.
var tmpNotesRefs atomic.Int64

func tryUpdateNotes(repo Repo, notes, commit string, note []byte) error {
	ref := "refs/notes/" + notes
	out, err := gitOutput(repo, nil, "for-each-ref", "--format=%(objectname)", ref)
	if err != nil {
		return err
	}
	old := strings.TrimSpace(string(out))
	// Unique even against refs left behind by a crashed process that had
	// this pid.
	tmp := fmt.Sprintf("%s-tmp-%d-%d-%d", notes, os.Getpid(), time.Now().UnixNano(), tmpNotesRefs.Add(1))
	defer gitOutput(repo, nil, "update-ref", "-d", "refs/notes/"+tmp)
	if old != "" {
		if _, err := gitOutput(repo, nil, "update-ref", "refs/notes/"+tmp, old); err != nil {
			return err
		}
	}
	if _, err := gitOutput(repo, note, "notes", "--ref="+tmp, "add", "-f", "-F", "-", commit); err != nil {
		return err
	}
	out, err = gitOutput(repo, nil, "rev-parse", "refs/notes/"+tmp)
	if err != nil {
		return err
	}
	updated := strings.TrimSpace(string(out))
	// If something deleted the private ref midway, the edits started from an
	// empty tree; swapping that in would drop every other note.
	if old != "" {
		if _, err := gitOutput(repo, nil, "merge-base", "--is-ancestor", old, updated); err != nil {
			return fmt.Errorf("notes update lost its base %s: %w", old, err)
		}
	}
	// An empty old value requires that the ref still not exist.
	_, err = gitOutput(repo, nil, "update-ref", ref, updated, old)
	return err
}

func listNotes(repo Repo, ref string) (map[string]bool, error) {
	output, err := gitOutput(repo, nil, "notes", "--ref="+ref, "list")
	if err != nil {
		return nil, err
	}
	notes := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid git notes list line %q", line)
		}
		notes[fields[1]] = true
	}
	return notes, nil
}

// gitOutput runs git args in repo, with stdin as its input.
func gitOutput(repo Repo, stdin []byte, args ...string) ([]byte, error) {
	cmd := repo(args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return output, nil
}

func gitEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		switch name {
		case "GIT_INDEX_FILE", "GIT_DIR", "GIT_WORK_TREE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
			continue
		}
		env = append(env, value)
	}
	// Errors are matched on their English text.
	return append(env, "LC_ALL=C")
}
