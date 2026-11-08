package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHandleGitMoved(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	dir, run, write := gitRepo(t)
	// Settings that would change git's answer if the handler let them:
	// textconv would number every line, and colorMovedWS would count
	// reindented code as moved.
	run("config", "diff.numbered.textconv", "cat -n")
	run("config", "diff.colorMovedWS", "ignore-all-space")
	write(".git/info/attributes", "*.go diff=numbered\n")
	first := "func first() {\n\tdoTheFirstThing()\n}\n"
	second := "func second() {\n\tdoTheSecondThing()\n}\n"
	third := "func third() {\n\tdoTheThirdThing()\n}\n"
	write("a.go", "package a\n\n"+first+"\n"+second+"\n"+third)
	write("b.go", "package b\n")
	run("add", "a.go", "b.go")
	run("commit", "-qm", "base")
	// Move first() into an existing file, second() into a new one whose
	// name git quotes, and third() reindented, which isn't a move.
	write("a.go", "package a\n")
	write("b.go", "package b\n\n"+first)
	write("nouveau/café.go", "package nouveau\n\n"+second)
	write("c.go", "package c\n\n"+strings.ReplaceAll(third, "\t", "    "))
	run("add", "a.go", "b.go", "nouveau/café.go", "c.go")
	run("commit", "-qm", "move")

	got := getMoved(t, h, dir)
	// Each block takes the blank line before it.
	want := []GitMovedFile{
		{Path: "a.go", Old: []GitMovedBlock{
			{Line: 2, Count: 4, OtherPath: "b.go", OtherLine: 2},
			{Line: 6, Count: 4, OtherPath: "nouveau/café.go", OtherLine: 2},
		}},
		{Path: "b.go", New: []GitMovedBlock{{Line: 2, Count: 4, OtherPath: "a.go", OtherLine: 2}}},
		{Path: "nouveau/café.go", New: []GitMovedBlock{{Line: 2, Count: 4, OtherPath: "a.go", OtherLine: 6}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// TestParseMovedDiffZebra: adjacent blocks are told apart by their
// stripes, and each finds its own copy. A color in a line's text doesn't
// count.
func TestParseMovedDiffZebra(t *testing.T) {
	out := "diff --git a/x b/x\n--- a/x\n+++ /dev/null\n@@ -1,5 +0,0 @@\n" +
		"\x1b[38;5;19m-A\n\x1b[38;5;19m-B\n\x1b[38;5;21m-C\n\x1b[38;5;21m-D\n-plain \x1b[38;5;19m\n" +
		"diff --git a/y b/y\n--- /dev/null\n+++ b/y\n@@ -0,0 +1,4 @@\n" +
		"\x1b[38;5;20m+C\n\x1b[38;5;20m+D\n\x1b[38;5;22m+A\n\x1b[38;5;22m+B\n"
	want := []GitMovedFile{
		{Path: "x", Old: []GitMovedBlock{
			{Line: 1, Count: 2, OtherPath: "y", OtherLine: 3},
			{Line: 3, Count: 2, OtherPath: "y", OtherLine: 1},
		}},
		{Path: "y", New: []GitMovedBlock{
			{Line: 1, Count: 2, OtherPath: "x", OtherLine: 3},
			{Line: 3, Count: 2, OtherPath: "x", OtherLine: 1},
		}},
	}
	if got := parseMovedDiff([]byte(out)); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// gitRepo is a test repository, with helpers to run git in it and write
// files.
func gitRepo(t *testing.T) (dir string, run func(...string), write func(name, content string)) {
	dir = setupTestGitRepo(t)
	run = func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write = func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, run, write
}

func getMoved(t *testing.T, h *TestHarness, dir string) []GitMovedFile {
	t.Helper()
	q := url.Values{"cwd": {dir}, "diff": {"HEAD"}, "to": {"self"}}
	w := httptest.NewRecorder()
	h.server.handleGitMoved(w, httptest.NewRequest("GET", "/api/git/moved?"+q.Encode(), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var got []GitMovedFile
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// TestHandleGitMovedConfig: the answer is the same whatever the repo's
// config, for paths git pads (spaces) and quotes (non-ASCII) too.
func TestHandleGitMovedConfig(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	dir, run, write := gitRepo(t)
	first := "func first() {\n\tdoTheFirstThing()\n}\n"
	second := "func second() {\n\tdoTheSecondThing()\n}\n"
	write("a.go", "package a\n\nvar keep = 1\n\n"+first+"\n"+second)
	run("add", "a.go")
	run("commit", "-qm", "base")
	write("a.go", "package a\n\nvar keep = 2\n\n")
	write("new file.go", "package a\n\n"+first)
	write("café x.go", "package a\n\n"+second)
	run("add", "a.go", "new file.go", "café x.go")
	run("commit", "-qm", "move")

	want := getMoved(t, h, dir)
	paths := map[string]bool{}
	for _, f := range want {
		paths[f.Path] = true
		for _, b := range append(f.Old, f.New...) {
			if !paths[b.OtherPath] && b.OtherPath != "a.go" && b.OtherPath != "new file.go" && b.OtherPath != "café x.go" {
				t.Errorf("%s:%d: counterpart %q", f.Path, b.Line, b.OtherPath)
			}
		}
	}
	if !reflect.DeepEqual(paths, map[string]bool{"a.go": true, "new file.go": true, "café x.go": true}) {
		t.Fatalf("paths %v in %+v", paths, want)
	}
	// Blank context lines lose their space, and plain changes take the
	// colors kept for moved lines.
	run("config", "diff.suppressBlankEmpty", "true")
	run("config", "color.diff.old", "19")
	run("config", "color.diff.new", "20")
	run("config", "color.diff.context", "21")
	run("config", "core.quotePath", "false")
	if got := getMoved(t, h, dir); !reflect.DeepEqual(got, want) {
		t.Errorf("with config: %+v\nwithout: %+v", got, want)
	}
}

// TestParseMovedDiffPartialMatch: a block only partly found on the other
// side has no counterpart, rather than a wrong one.
func TestParseMovedDiffPartialMatch(t *testing.T) {
	out := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n" +
		"\x1b[38;5;19m-A\n\x1b[38;5;19m-B\n\x1b[38;5;20m+A\n\x1b[38;5;20m+C\n"
	want := []GitMovedFile{{
		Path: "x",
		Old:  []GitMovedBlock{{Line: 1, Count: 2}},
		New:  []GitMovedBlock{{Line: 1, Count: 2}},
	}}
	if got := parseMovedDiff([]byte(out)); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// TestParseMovedDiffRepetitive: matching repeated lines takes bounded
// work. Each candidate here matches all but the block's last line.
func TestParseMovedDiffRepetitive(t *testing.T) {
	const n = 20000
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,%d +1,%d @@\n", n, n)
	for i := range n {
		if i < n-1 {
			b.WriteString("\x1b[38;5;19m-same line of code\n")
		} else {
			b.WriteString("\x1b[38;5;19m-the end\n")
		}
	}
	for range n {
		b.WriteString("\x1b[38;5;20m+same line of code\n")
	}
	got := parseMovedDiff([]byte(b.String()))
	if len(got) != 1 || len(got[0].Old) != 1 || got[0].Old[0].OtherPath != "" {
		t.Errorf("got %+v", got)
	}
}

// TestHandleGitMovedSubmoduleHelpers: with diff.submodule=diff, git would
// diff inside a submodule, running the external diff configured there.
func TestHandleGitMovedSubmoduleHelpers(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	sub, subRun, subWrite := gitRepo(t)
	subWrite("s.txt", "one\n")
	subRun("add", "s.txt")
	subRun("commit", "-qm", "one")
	dir, run, _ := gitRepo(t)
	run("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
	run("commit", "-qm", "add sub")
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "helper.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(dir, "sub")
	run("-C", subDir, "config", "diff.external", script)
	if err := os.WriteFile(filepath.Join(subDir, "s.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-C", subDir, "-c", "user.name=x", "-c", "user.email=x@x", "commit", "-qam", "two")
	run("add", "sub")
	run("commit", "-qm", "bump sub")
	run("config", "diff.submodule", "diff")

	getMoved(t, h, dir)
	if _, err := os.Stat(marker); err == nil {
		t.Error("ran the submodule's external diff")
	}
}

// TestOutputBounded: a git that runs too long or says too much is
// stopped.
func TestOutputBounded(t *testing.T) {
	t.Parallel()
	out, err := outputBounded(t.Context(), exec.Command("echo", "hi"), 10)
	if err != nil || string(out) != "hi\n" {
		t.Errorf("echo: %q, %v", out, err)
	}
	if _, err := outputBounded(t.Context(), exec.Command("yes"), 1<<20); err == nil {
		t.Error("yes: no error")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := outputBounded(ctx, exec.Command("sleep", "10"), 10); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("sleep: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("sleep ran %v", d)
	}
}
