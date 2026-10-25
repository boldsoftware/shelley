package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/commitnotes"
)

func TestHighlightsCLI(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	binary := shelleyBinary(t)

	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{
			"-C", repo,
			"-c", "core.hooksPath=/dev/null",
			"-c", "user.name=Test",
			"-c", "user.email=test@example.com",
		}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	for name, content := range map[string]string{"a.txt": "a\n", "b.txt": "b1\nb2\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("commit", "-q", "-m", "add", "-m", "Prompt: highlights CLI fixture")

	run := func(wantExit int, args ...string) string {
		t.Helper()
		out, err := exec.Command(binary, args...).CombinedOutput()
		exit := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			exit = exitErr.ExitCode()
		} else if err != nil {
			t.Fatalf("shelley %v: %v\n%s", args, err, out)
		}
		if exit != wantExit {
			t.Fatalf("shelley %v exited %d, want %d\n%s", args, exit, wantExit, out)
		}
		return string(out)
	}

	var scaffold commitnotes.Highlights
	if err := json.Unmarshal([]byte(run(0, "highlights", "scaffold", "-C", repo, "HEAD")), &scaffold); err != nil {
		t.Fatal(err)
	}
	if strings.Join(scaffold.Files, ",") != "a.txt,b.txt" || scaffold.Highlights == nil {
		t.Fatalf("scaffold = %+v", scaffold)
	}

	file := filepath.Join(tempDir, "highlights.json")
	writeJSON := func(h commitnotes.Highlights) {
		t.Helper()
		data, err := json.Marshal(h)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(commitnotes.Highlights{Version: 1, Files: []string{"b.txt"}})
	if out := run(1, "highlights", "attach", "-C", repo, "HEAD", file); !strings.Contains(out, `files is missing "a.txt"`) {
		t.Fatalf("attach of incomplete highlights = %q", out)
	}
	run(1, "highlights", "show", "-C", repo, "HEAD")

	want := commitnotes.Highlights{
		Version:    1,
		Files:      []string{"b.txt", "a.txt"},
		Highlights: []commitnotes.Highlight{{File: "b.txt", Line: 2, Comment: "a -> b && c < d"}},
	}
	writeJSON(want)
	run(0, "highlights", "verify", "-C", repo, "HEAD", file)
	run(1, "highlights", "show", "-C", repo, "HEAD")
	run(0, "highlights", "attach", "-C", repo, "HEAD", file)
	got, err := commitnotes.ParseHighlights([]byte(run(0, "highlights", "show", "-C", repo, "HEAD")))
	if err != nil {
		t.Fatal(err)
	}
	// Comments are stored readable: code often has <, > and &.
	if note := run(0, "highlights", "show", "-C", repo, "HEAD"); !strings.Contains(note, "a -> b && c < d") {
		t.Fatalf("show escapes the comment: %q", note)
	}
	if strings.Join(got.Files, ",") != "b.txt,a.txt" || len(got.Highlights) != 1 || got.Highlights[0] != want.Highlights[0] {
		t.Fatalf("show = %+v", got)
	}

	// The note is stored canonically, with omitted lists as empty arrays.
	git("commit", "-q", "--allow-empty", "-m", "empty")
	if err := os.WriteFile(file, []byte(`{"version": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run(0, "highlights", "attach", "-C", repo, "HEAD", file)
	if out := run(0, "highlights", "show", "-C", repo, "HEAD"); !strings.Contains(out, `"files": []`) || !strings.Contains(out, `"highlights": []`) {
		t.Fatalf("show of empty highlights = %q", out)
	}
}
