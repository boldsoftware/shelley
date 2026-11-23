package lazycue

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// A cached script that fails once and passes on a retry passes, but its
// result and summary say it was retried, and why, so flakes don't hide.
func TestHarnessRecordsRetries(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	t.Parallel()
	var pages atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && pages.Add(1) > 1 {
			_, _ = w.Write([]byte("<h1>Hello</h1>"))
		}
	}))
	t.Cleanup(ts.Close)
	description := "The page has a heading."
	// Should every run fail, the heal fails fast rather than run an agent.
	h := New(Options{BaseURL: ts.URL, CacheDir: t.TempDir(), AnthropicBaseURL: "http://127.0.0.1:1"})
	if err := SaveCachedTest(h.opts.CacheDir, description, []byte(`[{"action":"navigate","url":"/"},{"action":"assert_visible","selector":"h1","timeout":"100ms"}]`), 1, nil); err != nil {
		t.Fatal(err)
	}
	tb := &logTB{TB: t}
	h.Test(tb, Useful{}, description)
	if !strings.Contains(tb.logs, "🔁 flaky: passed on retry 1; first failure: step 1 (assert_visible)") {
		t.Errorf("log %q does not say the test was flaky", tb.logs)
	}
	r := h.Results()
	if len(r) != 1 {
		t.Fatalf("%d results, want 1", len(r))
	}
	if !r[0].Pass || r[0].Mode != RunModeCached || r[0].Retries != 1 ||
		!strings.HasPrefix(r[0].RetryError, "step 1 (assert_visible)") {
		t.Fatalf("result %+v, want a cached pass after 1 retry, with the first run's failure", *r[0])
	}
	s := Summarize(r)
	if s.Retried != 1 || s.Tests[0].Retries != 1 || s.Tests[0].RetryError != r[0].RetryError {
		t.Errorf("summary %+v, want 1 retried test and its retry and failure", s)
	}
}

// logTB records what a test logs.
type logTB struct {
	testing.TB
	logs string
}

func (l *logTB) Logf(format string, args ...any) {
	l.TB.Helper()
	l.logs += fmt.Sprintf(format, args...)
	l.TB.Logf(format, args...)
}
