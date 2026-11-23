package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/llm"
)

func readCodeTestRequireRG(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skipf("ripgrep unavailable: %v", err)
	}
}

func readCodeTestRun(t *testing.T, ctx context.Context, dir string, in readCodeInput) llm.ToolOut {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return readCodeTool(NewMutableWorkingDir(dir)).Run(ctx, b)
}

func readCodeTestText(out llm.ToolOut) string {
	var b strings.Builder
	for _, c := range out.LLMContent {
		b.WriteString(c.Text)
	}
	if out.Error != nil {
		b.WriteString(out.Error.Error())
	}
	return b.String()
}

func readCodeTestWrite(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readCodeTestSaved(t *testing.T, text string) string {
	t.Helper()
	match := regexp.MustCompile(`Saved result: ([^\]\n]+)`).FindStringSubmatch(text)
	if len(match) != 2 || !filepath.IsAbs(match[1]) {
		t.Fatalf("missing absolute saved path: %s", text)
	}
	return match[1]
}

func readCodeTestResultsDir(t *testing.T) {
	t.Helper()
	parent := t.TempDir()
	old := readCodeResultsDir
	readCodeResultsDir = sync.OnceValues(func() (string, error) {
		return os.MkdirTemp(parent, "shelley-read-code-results-")
	})
	t.Cleanup(func() { readCodeResultsDir = old })
}

func TestReadCodeSearchAndRead(t *testing.T) {
	readCodeTestRequireRG(t)
	dir := t.TempDir()
	readCodeTestWrite(t, dir, "a.go", "before\nAlpha.one\nafter\nalphaXone\n")
	readCodeTestWrite(t, dir, "b.txt", "Alpha.one\n")
	in := readCodeInput{Searches: []readCodeSearchRequest{
		{Query: "Alpha.one", Glob: "*.go", Context: 1},
		{Query: `alpha.one`, Regex: true, Path: "a.go"},
	}, Reads: []readCodeReadRequest{{Path: "a.go", From: 2, To: 3}}}
	out := readCodeTestRun(t, t.Context(), dir, in)
	if out.Error != nil {
		t.Fatal(out.Error)
	}
	text := readCodeTestText(out)
	for _, s := range []string{"before", "Alpha.one", "after", "alphaXone", "Read \"a.go\"", "2 │ Alpha.one", "3 │ after"} {
		if !strings.Contains(text, s) {
			t.Errorf("missing %q in %s", s, text)
		}
	}
	first := strings.SplitN(text, "Search \"alpha.one\"", 2)
	if len(first) != 2 || strings.Contains(first[0], "b.txt") || strings.Contains(first[0], "alphaXone") {
		t.Fatalf("literal/glob/context: %s", text)
	}
	if strings.Index(text, "Read \"a.go\"") < strings.Index(text, "Search \"alpha.one\"") {
		t.Fatal("reads ran before searches")
	}
}

func TestReadCodeIndependentBudgets(t *testing.T) {
	readCodeTestRequireRG(t)
	dir := t.TempDir()
	line := "needle " + strings.Repeat("x", 90) + "\n"
	readCodeTestWrite(t, dir, "data.txt", strings.Repeat(line, 55))
	out := readCodeTestRun(t, t.Context(), dir, readCodeInput{Searches: []readCodeSearchRequest{{Query: "needle"}}, Reads: []readCodeReadRequest{{Path: "data.txt", From: 1, To: 55}}})
	text := readCodeTestText(out)
	if out.Error != nil || strings.Contains(text, "Saved result:") || strings.Count(text, "needle") < 110 {
		t.Fatalf("budgets not independent: %s", text)
	}
}

func TestReadCodeSavedResults(t *testing.T) {
	readCodeTestResultsDir(t)
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 150; i++ {
		fmt.Fprintf(&b, "needle %03d %s\n", i, strings.Repeat("x", 100))
	}
	readCodeTestWrite(t, dir, "data.txt", b.String())
	for _, tc := range []struct {
		name string
		in   readCodeInput
	}{
		{"search", readCodeInput{Searches: []readCodeSearchRequest{{Query: "needle"}}}},
		{"read", readCodeInput{Reads: []readCodeReadRequest{{Path: "data.txt", From: 1, To: 150}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.in.Searches) > 0 {
				readCodeTestRequireRG(t)
			}
			out := readCodeTestRun(t, t.Context(), dir, tc.in)
			if out.Error != nil {
				t.Fatal(out.Error)
			}
			text := readCodeTestText(out)
			if strings.Contains(text, "needle 150") {
				t.Fatal("overflow leaked into preview")
			}
			path := readCodeTestSaved(t, text)
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(saved), "needle 001") || !strings.Contains(string(saved), "needle 150") {
				t.Fatal("saved result is incomplete")
			}
			read := readCodeTestRun(t, t.Context(), dir, readCodeInput{Reads: []readCodeReadRequest{{Path: path, From: 150, To: 151}}})
			if read.Error != nil || !strings.Contains(readCodeTestText(read), "needle 150") {
				t.Fatalf("saved read: %s", readCodeTestText(read))
			}
		})
	}
}

func TestReadCodeOutputCap(t *testing.T) {
	readCodeTestRequireRG(t)
	readCodeTestResultsDir(t)
	dir := t.TempDir()
	readCodeTestWrite(t, dir, "large.txt", strings.Repeat("needle "+strings.Repeat("x", 470)+"\n", 2400))
	out := readCodeTestRun(t, t.Context(), dir, readCodeInput{Searches: []readCodeSearchRequest{{Query: "needle"}}})
	text := readCodeTestText(out)
	if out.Error != nil || !strings.Contains(text, "Result incomplete") {
		t.Fatalf("expected incomplete result: %s", text)
	}
	data, err := os.ReadFile(readCodeTestSaved(t, text))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 1<<20 || len(data) < 1<<20-1000 {
		t.Fatalf("saved size %d", len(data))
	}
}

func TestReadCodeErrors(t *testing.T) {
	dir := t.TempDir()
	readCodeTestWrite(t, dir, "ok.txt", "good\n")
	outside := t.TempDir()
	readCodeTestWrite(t, outside, "secret", "secret\n")
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		in   readCodeInput
	}{
		{"missing search", readCodeInput{Searches: []readCodeSearchRequest{{Query: "x", Path: "missing"}}}},
		{"missing read", readCodeInput{Reads: []readCodeReadRequest{{Path: "missing", From: 1, To: 1}}}},
		{"bad regex", readCodeInput{Searches: []readCodeSearchRequest{{Query: "[", Regex: true}}}},
		{"unsafe search", readCodeInput{Searches: []readCodeSearchRequest{{Query: "x", Path: "../outside"}}}},
		{"unsafe read", readCodeInput{Reads: []readCodeReadRequest{{Path: "../outside", From: 1, To: 1}}}},
		{"absolute read", readCodeInput{Reads: []readCodeReadRequest{{Path: filepath.Join(outside, "secret"), From: 1, To: 1}}}},
		{"symlink search", readCodeInput{Searches: []readCodeSearchRequest{{Query: "secret", Path: "escape/secret"}}}},
		{"symlink read", readCodeInput{Reads: []readCodeReadRequest{{Path: "escape/secret", From: 1, To: 1}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "bad regex" {
				readCodeTestRequireRG(t)
			}
			tc.in.Reads = append(tc.in.Reads, readCodeReadRequest{Path: "ok.txt", From: 1, To: 1})
			out := readCodeTestRun(t, t.Context(), dir, tc.in)
			text := readCodeTestText(out)
			if out.Error == nil || !strings.Contains(text, "good") || strings.Contains(text, "1 │ secret") {
				t.Fatalf("error or independent result missing: %s", text)
			}
		})
	}
}

func TestReadCodeCanceledAndRequestLimit(t *testing.T) {
	dir := t.TempDir()
	readCodeTestWrite(t, dir, "ok.txt", "good\n")
	t.Run("canceled search", func(t *testing.T) {
		readCodeTestRequireRG(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		out := readCodeTestRun(t, ctx, dir, readCodeInput{Searches: []readCodeSearchRequest{{Query: "good"}}})
		if out.Error == nil || !strings.Contains(readCodeTestText(out), "canceled") {
			t.Fatalf("canceled: %s", readCodeTestText(out))
		}
	})
	reads := make([]readCodeReadRequest, 8)
	for i := range reads {
		reads[i] = readCodeReadRequest{Path: "ok.txt", From: 1, To: 1}
	}
	out := readCodeTestRun(t, t.Context(), dir, readCodeInput{Reads: reads})
	if out.Error != nil || strings.Count(readCodeTestText(out), "1 │ good") != 8 {
		t.Fatalf("eight requests: %s", readCodeTestText(out))
	}
	out = readCodeTestRun(t, t.Context(), dir, readCodeInput{Searches: []readCodeSearchRequest{{Query: "good"}}, Reads: reads})
	if out.Error == nil {
		t.Fatal("accepted nine combined requests")
	}
	if out = readCodeTestRun(t, t.Context(), dir, readCodeInput{}); out.Error == nil {
		t.Fatal("accepted empty batch")
	}
}

func TestReadCodeSaveFailure(t *testing.T) {
	readCodeTestResultsDir(t)
	tmp, err := readCodeResultsDir()
	if err != nil {
		t.Fatal(err)
	}
	readCodeTestWrite(t, tmp, "not-a-directory", "not a directory")
	readCodeResultsDir = func() (string, error) { return filepath.Join(tmp, "not-a-directory"), nil }
	dir := t.TempDir()
	readCodeTestWrite(t, dir, "large.txt", strings.Repeat("needle "+strings.Repeat("x", 100)+"\n", 150))
	readCodeTestWrite(t, dir, "ok.txt", "still readable\n")
	out := readCodeTestRun(t, t.Context(), dir, readCodeInput{
		Reads: []readCodeReadRequest{{Path: "large.txt", From: 1, To: 150}, {Path: "ok.txt", From: 1, To: 1}},
	})
	if out.Error == nil || !strings.Contains(out.Error.Error(), "Error saving oversized result") ||
		!strings.Contains(out.Error.Error(), "still readable") {
		t.Fatalf("save failure lost batch results: %s", readCodeTestText(out))
	}
}

func TestReadCodeSymlinkDotDot(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "inner", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inner/nested", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	readCodeTestWrite(t, dir, "target.txt", "wrong target\n")
	readCodeTestWrite(t, filepath.Join(dir, "inner"), "target.txt", "expected target\n")
	path := "link/../target.txt"
	for _, tc := range []struct {
		name string
		in   readCodeInput
	}{
		{"search", readCodeInput{Searches: []readCodeSearchRequest{{Query: "target", Path: path}}}},
		{"read", readCodeInput{Reads: []readCodeReadRequest{{Path: path, From: 1, To: 1}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.in.Searches) > 0 {
				readCodeTestRequireRG(t)
			}
			out := readCodeTestRun(t, t.Context(), dir, tc.in)
			text := readCodeTestText(out)
			if out.Error != nil || strings.Count(text, "expected target") != 1 || strings.Contains(text, "wrong target") ||
				strings.Count(text, "File: "+path) != 1 {
				t.Fatalf("wrong target or path: %s", text)
			}
		})
	}
}

func TestReadCodePrivateResultsDirectory(t *testing.T) {
	readCodeTestResultsDir(t)
	dir, err := readCodeResultsDir()
	if err != nil {
		t.Fatal(err)
	}
	again, err := readCodeResultsDir()
	if err != nil || again != dir {
		t.Fatalf("result directory changed: %s, %s, %v", dir, again, err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("result directory is not private: %v, %v", info, err)
	}
	other := filepath.Join(filepath.Dir(dir), "shelley-read-code-results")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	readCodeTestWrite(t, other, "secret.txt", "unrelated contents\n")
	link := filepath.Join(filepath.Dir(dir), "shelley-read-code-results-link")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(other, "secret.txt"), filepath.Join(link, "secret.txt")} {
		out := readCodeTestRun(t, t.Context(), t.TempDir(), readCodeInput{
			Reads: []readCodeReadRequest{{Path: path, From: 1, To: 1}},
		})
		if out.Error == nil || strings.Contains(out.Error.Error(), "unrelated contents") {
			t.Fatalf("trusted unrelated saved result path: %s", readCodeTestText(out))
		}
	}
}

func TestReadCodeSearchCallerCancellation(t *testing.T) {
	readCodeTestRequireRG(t)
	dir := t.TempDir()
	readCodeTestWrite(t, dir, "long.txt", strings.Repeat("needle ", 150)+"\n")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err = readCodeSearch(ctx, root, dir, readCodeSearchRequest{Query: "needle", Path: "long.txt"}, func(string) bool {
		cancel()
		return true
	})
	if err != context.Canceled {
		t.Fatalf("caller cancellation was swallowed: %v", err)
	}
}
