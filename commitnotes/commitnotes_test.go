package commitnotes

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func gitRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{
			"-C", dir,
			"-c", "core.hooksPath=/dev/null",
			"-c", "user.name=Test",
			"-c", "user.email=test@example.com",
			"-c", "commit.gpgsign=false",
		}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	git("config", "core.hooksPath", "/dev/null")
	return dir, git
}

func write(t *testing.T, dir, name string, content []byte) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, git func(...string) string, message string) string {
	t.Helper()
	git("add", "-A")
	git("commit", "-q", "--allow-empty", "-m", message, "-m", "Prompt: commitnotes test fixture")
	return strings.TrimSpace(git("rev-parse", "HEAD"))
}

func numbered(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func TestNotesRoundTrip(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "a.txt", []byte("a\n"))
	first := commit(t, git, "first")
	write(t, dir, "a.txt", []byte("b\n"))
	second := commit(t, git, "second")

	if _, err := ReadHighlights(dir, first); !errors.Is(err, ErrNoNote) {
		t.Fatalf("ReadHighlights error = %v", err)
	}
	notes, err := ListHighlights(dir)
	if err != nil || len(notes) != 0 {
		t.Fatalf("ListHighlights = %v, %v", notes, err)
	}
	payload := []byte(`{"version":1,"files":["a.txt"],"highlights":[]}`)
	if err := WriteHighlights(dir, first, payload); err != nil {
		t.Fatal(err)
	}
	got, err := ReadHighlights(dir, first)
	if err != nil || strings.TrimSpace(string(got)) != string(payload) {
		t.Fatalf("ReadHighlights = %q, %v", got, err)
	}
	notes, err = ListHighlights(dir)
	if err != nil || len(notes) != 1 || !notes[first] || notes[second] {
		t.Fatalf("ListHighlights = %v, %v", notes, err)
	}
	if refs := git("for-each-ref", "--format=%(refname)", "refs/notes/"); refs != "refs/notes/shelley-highlights\n" {
		t.Errorf("notes refs = %q", refs)
	}
}

func TestGitEnvIgnoresRepoOverrides(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "f.txt", []byte("a\n"))
	hash := commit(t, git, "base")

	other, otherGit := gitRepo(t)
	write(t, other, "g.txt", []byte("b\n"))
	commit(t, otherGit, "other")

	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	gotHash, err := CommitHash(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != hash {
		t.Fatalf("GIT_DIR override leaked: got %s, want %s", gotHash, hash)
	}
	h, err := HighlightsScaffold(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"f.txt"}; !slices.Equal(h.Files, want) {
		t.Fatalf("scaffold files = %q, want %q", h.Files, want)
	}
}

func TestWriteNoteConcurrent(t *testing.T) {
	dir, git := gitRepo(t)
	var commits []string
	for i := range 12 {
		write(t, dir, "f.txt", []byte(fmt.Sprintf("%d\n", i)))
		commits = append(commits, commit(t, git, fmt.Sprintf("c%d", i)))
	}
	errs := make(chan error, len(commits))
	for i := range commits {
		go func() { errs <- WriteHighlights(dir, commits[i], []byte(fmt.Sprintf("note %d", i))) }()
	}
	for range commits {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for i := range commits {
		if note, err := ReadHighlights(dir, commits[i]); err != nil || strings.TrimSpace(string(note)) != fmt.Sprintf("note %d", i) {
			t.Errorf("note %d = %q, %v", i, note, err)
		}
	}
	if refs := git("for-each-ref", "refs/notes/"); strings.Count(refs, "\n") != 1 {
		t.Errorf("leftover notes refs:\n%s", refs)
	}
}
