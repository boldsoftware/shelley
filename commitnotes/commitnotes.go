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

// highlightsNotesRef is the notes ref holding commit highlights.
const highlightsNotesRef = "shelley-highlights"

// CommitHash resolves rev to a full commit hash.
func CommitHash(dir, rev string) (string, error) {
	hashBytes, err := gitOutput(dir, "rev-parse", rev+"^{commit}")
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

func readNote(dir, ref, commit string) ([]byte, error) {
	data, err := gitOutput(dir, "notes", "--ref="+ref, "show", commit)
	if err != nil {
		if strings.Contains(err.Error(), "no note found") {
			return nil, fmt.Errorf("%w for %s", ErrNoNote, commit)
		}
		return nil, err
	}
	return data, nil
}

func writeNote(dir, ref, commit string, data []byte) error {
	file, err := os.CreateTemp("", "shelley-note-*.json")
	if err != nil {
		return fmt.Errorf("create note file: %w", err)
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write note file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close note file: %w", err)
	}
	return updateNotes(dir, ref, commit, name)
}

// updateNotes atomically adds noteFile as commit's note in the notes ref
// named notes.
// git notes overwrites its ref without checking the old value, so
// concurrent writers (parallel subagents annotating different commits)
// would silently drop each other's notes. Instead the edits run against a
// private copy of the ref, which is then compare-and-swapped in; on
// contention the whole update is redone from the new value.
func updateNotes(dir, notes, commit, noteFile string) error {
	deadline := time.Now().Add(15 * time.Second)
	backoff := 10 * time.Millisecond
	for {
		err := tryUpdateNotes(dir, notes, commit, noteFile)
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

func tryUpdateNotes(dir, notes, commit, noteFile string) error {
	ref := "refs/notes/" + notes
	out, err := gitOutput(dir, "for-each-ref", "--format=%(objectname)", ref)
	if err != nil {
		return err
	}
	old := strings.TrimSpace(string(out))
	// Unique even against refs left behind by a crashed process that had
	// this pid.
	tmp := fmt.Sprintf("%s-tmp-%d-%d-%d", notes, os.Getpid(), time.Now().UnixNano(), tmpNotesRefs.Add(1))
	defer gitOutput(dir, "update-ref", "-d", "refs/notes/"+tmp)
	if old != "" {
		if _, err := gitOutput(dir, "update-ref", "refs/notes/"+tmp, old); err != nil {
			return err
		}
	}
	if _, err := gitOutput(dir, "notes", "--ref="+tmp, "add", "-f", "-F", noteFile, commit); err != nil {
		return err
	}
	out, err = gitOutput(dir, "rev-parse", "refs/notes/"+tmp)
	if err != nil {
		return err
	}
	updated := strings.TrimSpace(string(out))
	// If something deleted the private ref midway, the edits started from an
	// empty tree; swapping that in would drop every other note.
	if old != "" {
		if _, err := gitOutput(dir, "merge-base", "--is-ancestor", old, updated); err != nil {
			return fmt.Errorf("notes update lost its base %s: %w", old, err)
		}
	}
	// An empty old value requires that the ref still not exist.
	_, err = gitOutput(dir, "update-ref", ref, updated, old)
	return err
}

func listNotes(dir, ref string) (map[string]bool, error) {
	output, err := gitOutput(dir, "notes", "--ref="+ref, "list")
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

// gitOutput runs git -C dir with a sanitized environment: repo-locating
// variables are stripped so dir always wins.
func gitOutput(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv()
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
