package lazycue

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// useful is how the tests that use TimesUseful a lot call it.
var useful = TimesUseful

func TestTimesUsefulLocatesItsCaller(t *testing.T) {
	_, file, line, _ := runtime.Caller(0)
	direct, viaVar := TimesUseful(2), useful(3)
	if direct != (Useful{Times: 2, File: file, Line: line + 1}) {
		t.Errorf("TimesUseful(2) = %+v, want 2 at %s:%d", direct, file, line+1)
	}
	if viaVar != (Useful{Times: 3, File: file, Line: line + 1}) {
		t.Errorf("useful(3) = %+v, want 3 at %s:%d", viaVar, file, line+1)
	}
}

func TestUsefulWhere(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repo, "app", "test", "x_test.go")
	for _, tc := range []struct {
		u    Useful
		want string
	}{
		{Useful{Times: 1, File: file, Line: 12}, "app/test/x_test.go:12"},
		{Useful{Times: 1, File: "/no/repo/x_test.go", Line: 3}, "/no/repo/x_test.go:3"},
		// -trimpath, outside this module
		{Useful{Times: 1, File: "example.com/app/test/x_test.go", Line: 3}, "example.com/app/test/x_test.go:3"},
		{Useful{Times: 1}, ""},
	} {
		if got := tc.u.where(); got != tc.want {
			t.Errorf("%+v.where() = %q, want %q", tc.u, got, tc.want)
		}
	}
}

// Under -trimpath, as in CI, a count in this module is still found where it
// is in the repository.
func TestUsefulWhereTrimmed(t *testing.T) {
	root, path := startModule()
	if path != "github.com/boldsoftware/shelley/lazycue" {
		t.Fatalf("module %q at %q, want github.com/boldsoftware/shelley/lazycue", path, root)
	}
	abs := filepath.Join(root, "useful_test.go")
	trimmed := Useful{Times: 1, File: path + "/useful_test.go", Line: 5}
	if got, want := trimmed.where(), (Useful{Times: 1, File: abs, Line: 5}).where(); got != want {
		t.Errorf("trimmed where() = %q, want %q", got, want)
	}
	if got := trimmed.where(); !strings.HasSuffix(got, "lazycue/useful_test.go:5") || filepath.IsAbs(got) {
		t.Errorf("trimmed where() = %q, want a repository-relative path ending lazycue/useful_test.go:5", got)
	}
}

func TestUsefulHint(t *testing.T) {
	for _, tc := range []struct {
		times int
		at    string
		want  []string
	}{
		{0, "test/x_test.go:8", []string{"useful 0 times so far.", "from 0 to 1 at test/x_test.go:8 in the commit that fixes the bug"}},
		{1, "test/x_test.go:8", []string{"useful 1 time so far.", "from 1 to 2 at test/x_test.go:8 in"}},
		{2, "", []string{"useful 2 times so far.", "from 2 to 3 in"}},
	} {
		got := usefulHint(tc.times, tc.at)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("usefulHint(%d, %q) = %q, want it to contain %q", tc.times, tc.at, got, want)
			}
		}
	}
}

// fatalTB records a test's failures rather than failing.
type fatalTB struct {
	testing.TB
	fatal, errors string
}

func (f *fatalTB) Helper() {}

func (f *fatalTB) Fatalf(format string, args ...any) { f.fatal = fmt.Sprintf(format, args...) }

func (f *fatalTB) Errorf(format string, args ...any) { f.errors += fmt.Sprintf(format, args...) }

// A test whose steps fail says how useful it has been and asks for the count
// to go up if it caught a bug; its result and summary record the count.
func TestHarnessFailureAsksForUsefulCount(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	prev := agentRunsLeft.Swap(0) // the cached script fails, and nothing heals it
	t.Cleanup(func() { agentRunsLeft.Store(prev) })
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(ts.Close)
	description := "The page has a heading."
	h := New(Options{BaseURL: ts.URL, CacheDir: t.TempDir()})
	if err := SaveCachedTest(h.opts.CacheDir, description, []byte(`[{"action":"navigate","url":"/"},{"action":"assert_visible","selector":"h1","timeout":"100ms"}]`), 1, nil); err != nil {
		t.Fatal(err)
	}
	tb := &fatalTB{TB: t}
	h.Test(tb, Useful{Times: 4, File: "/no/repo/x_test.go", Line: 7}, description)
	if !strings.Contains(tb.fatal, "lazycue: test failed: step 1 (assert_visible)") ||
		!strings.Contains(tb.fatal, "useful 4 times so far") ||
		!strings.Contains(tb.fatal, "from 4 to 5 at /no/repo/x_test.go:7") {
		t.Errorf("failure %q does not give the reason and the useful count", tb.fatal)
	}
	results := h.Results()
	if len(results) != 1 || results[0].Useful != 4 || results[0].UsefulAt != "/no/repo/x_test.go:7" {
		t.Fatalf("results %+v, want one with 4 useful at /no/repo/x_test.go:7", results)
	}
	if s := Summarize(results); !s.Tests[0].AskUseful || s.Useful != 4 {
		t.Errorf("summary %+v, want it to ask about the failure and total 4 useful", s)
	}
}

// A cached script that fails and can't be healed, its agent erring, is still
// a failure that may have caught a bug.
func TestHarnessFailedHealAsksForUsefulCount(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	prev := agentRunsLeft.Swap(1)
	t.Cleanup(func() { agentRunsLeft.Store(prev) })
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(ts.Close)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"no"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(api.Close)
	description := "The page has a heading."
	h := New(Options{BaseURL: ts.URL, CacheDir: t.TempDir(), AnthropicBaseURL: api.URL, AnthropicAPIKey: "test"})
	if err := SaveCachedTest(h.opts.CacheDir, description, []byte(`[{"action":"navigate","url":"/"},{"action":"assert_visible","selector":"h1","timeout":"100ms"}]`), 1, nil); err != nil {
		t.Fatal(err)
	}
	tb := &fatalTB{TB: t}
	h.Test(tb, Useful{Times: 0, File: "/no/repo/x_test.go", Line: 30}, description)
	if !strings.Contains(tb.fatal, "step 1 (assert_visible)") || !strings.Contains(tb.fatal, "heal failed") ||
		!strings.Contains(tb.fatal, "from 0 to 1 at /no/repo/x_test.go:30") {
		t.Errorf("failure %q, want the cached failure, the failed heal and the useful count", tb.fatal)
	}
}

// Failures that tested nothing, with no script and no agent to write one, or
// with the app failing to start, don't ask whether they caught a bug. They are
// recorded all the same.
func TestHarnessUntestedFailuresDoNotAsk(t *testing.T) {
	prev := agentRunsLeft.Swap(0)
	t.Cleanup(func() { agentRunsLeft.Store(prev) })
	description := "The page says hello."
	t.Run("no script", func(t *testing.T) {
		h := New(Options{BaseURL: "http://127.0.0.1:1", CacheDir: t.TempDir()})
		tb := &fatalTB{TB: t}
		h.Test(tb, Useful{Times: 1, File: "/no/repo/x_test.go", Line: 20}, description)
		if !strings.Contains(tb.fatal, "no cached script") || strings.Contains(tb.fatal, "Useful?") {
			t.Errorf("failure %q, want one for the missing script that does not ask about usefulness", tb.fatal)
		}
		if r := h.Results(); len(r) != 1 || r[0].Pass || r[0].Useful != 1 || Summarize(r).Tests[0].AskUseful {
			t.Errorf("results %+v, want one failure with 1 useful, not asking", r)
		}
	})
	t.Run("no app", func(t *testing.T) {
		opts := Options{
			CacheDir: t.TempDir(),
			Setup:    func(context.Context) (*Env, error) { return nil, errors.New("no app today") },
		}
		if err := SaveCachedTest(opts.CacheDir, description, []byte(`[{"action":"navigate","url":"/"}]`), 1, nil); err != nil {
			t.Fatal(err)
		}
		h := New(opts)
		tb := &fatalTB{TB: t}
		h.Test(tb, Useful{Times: 1, File: "/no/repo/x_test.go", Line: 21}, description)
		if !strings.Contains(tb.fatal, "no app today") || strings.Contains(tb.fatal, "Useful?") {
			t.Errorf("failure %q, want the error, not asking about usefulness", tb.fatal)
		}
		r := h.Results()
		if len(r) != 1 || r[0].Pass || r[0].Mode != RunModeError || r[0].Useful != 1 || !strings.Contains(r[0].Error, "no app today") {
			t.Fatalf("results %+v, want one error with 1 useful", r)
		}
	})
}

// Two tests may not share a count: one test's catches would be the other's.
func TestHarnessReportsSharedCounts(t *testing.T) {
	prev := agentRunsLeft.Swap(0)
	t.Cleanup(func() { agentRunsLeft.Store(prev) })
	h := New(Options{BaseURL: "http://127.0.0.1:1", CacheDir: t.TempDir()})
	shared := Useful{Times: 0, File: "/no/repo/shared_test.go", Line: 3}
	t.Cleanup(func() { usefulOwners.Delete(shared.where()) })
	run := func(name string) string {
		var errs string
		t.Run(name, func(t *testing.T) {
			tb := &fatalTB{TB: t}
			h.Test(tb, shared, "Anything.")
			errs = tb.errors
		})
		return errs
	}
	if errs := run("first"); errs != "" {
		t.Errorf("first test with the count: %q, want no complaint", errs)
	}
	if errs := run("first"); errs != "" {
		t.Errorf("first test again: %q, want no complaint", errs)
	}
	// Subtests count as their Test function, so another Test function
	// has to own the count from outside.
	usefulOwners.Store("/no/repo/shared_test.go:3", "TestSomethingElse")
	if errs := run("second"); !strings.Contains(errs, "TestSomethingElse and TestHarnessReportsSharedCounts share the useful count at /no/repo/shared_test.go:3") {
		t.Errorf("a second test with the count: %q, want a complaint naming both", errs)
	}
}

// Tests that share a count, like the subtests of one call in a loop, add it
// to the summary's total once.
func TestSummaryUseful(t *testing.T) {
	s := Summarize([]*TestResult{
		{Name: "TestA/narrow", Useful: 2, UsefulAt: "a_test.go:5"},
		{Name: "TestA/wide", Useful: 2, UsefulAt: "a_test.go:5"},
		{Name: "TestB", Useful: 1, UsefulAt: "b_test.go:9"},
		{Name: "TestC", Useful: 3},
		{Name: "TestD", Useful: 3},
	})
	if s.Useful != 9 {
		t.Errorf("summary useful = %d, want 9", s.Useful)
	}
	if s.Tests[0].Useful != 2 || s.Tests[0].UsefulAt != "a_test.go:5" {
		t.Errorf("first test summary %+v, want 2 useful at a_test.go:5", s.Tests[0])
	}
}
