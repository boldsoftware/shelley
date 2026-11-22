package lazycue

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// Useful is how many times a test has been useful: how many of its failures
// caught a real bug, rather than a flake or an intended change the test had
// to follow. Harness.Test takes one for every test, and a failing test asks
// whoever fixes the bug it caught to increment its count in the fixing
// commit. Self-healing tests are only worth having if they catch bugs; these
// counts are how we find out whether they do.
//
// A count is a literal written once per test, at its own place in the
// source. Harness.Test reports tests that share one.
type Useful struct {
	Times int
	// File and Line are where Times is written, for a failure to point
	// at. TimesUseful fills them in.
	File string
	Line int
}

// TimesUseful returns a Useful of n, located where it is called:
//
//	app.Test(t, lazycue.TimesUseful(0), "Navigate to / and ...")
//
// Calling it through a func variable (var useful = lazycue.TimesUseful)
// locates it at that call too.
func TimesUseful(n int) Useful {
	_, file, line, _ := runtime.Caller(1)
	return Useful{Times: n, File: file, Line: line}
}

// where is the count's position, its file relative to the root of the git
// repository holding it ("shelley/test/e2e_smoke_test.go:8"), or "" if
// unknown.
func (u Useful) where() string {
	if u.File == "" {
		return ""
	}
	return repoRelative(u.File) + ":" + strconv.Itoa(u.Line)
}

// repoRelative returns file relative to the root of the git repository that
// holds it, or file itself if it can't tell.
func repoRelative(file string) string {
	file = untrim(file)
	if !filepath.IsAbs(file) {
		return file
	}
	for dir := filepath.Dir(file); ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			if rel, err := filepath.Rel(dir, file); err == nil {
				return rel
			}
			return file
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return file
		}
		dir = parent
	}
}

// usefulOwners maps where each count is written to the test that has it, to
// catch tests that share one.
var usefulOwners sync.Map

// asksUseful reports whether a result is a failure that may have caught a
// bug: its steps ran against the app, and failed. A test with no script and
// no agent to write one has not tested anything.
func (r *TestResult) asksUseful() bool {
	return !r.Pass && len(r.Steps) > 0
}

// usefulHint is what a failed test adds to its error: a request to count the
// failure if it caught a real bug. at is where the count is written (see
// Useful.where).
func usefulHint(times int, at string) string {
	unit := "times"
	if times == 1 {
		unit = "time"
	}
	where := ""
	if at != "" {
		where = " at " + at
	}
	return fmt.Sprintf("Useful? This test has been useful %d %s so far. "+
		"If this failure caught a real bug, even one in your own uncommitted change, "+
		"rather than a flake or an intended change the test should follow, "+
		"increment the count from %d to %d%s in the commit that fixes the bug (every test that caught it counts). "+
		"The counts tell us whether LazyCue tests earn their keep.", times, unit, times, times+1, where)
}

// startDir is the directory the process started in: for go test, that of the
// package under test, before a TestMain can leave it.
var startDir, _ = os.Getwd()

// startModule is the root and path of the Go module holding startDir, or ""s.
var startModule = sync.OnceValues(func() (root, path string) {
	for dir := startDir; dir != ""; {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			for line := range strings.Lines(string(data)) {
				if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module"); ok {
					rest, _, _ = strings.Cut(rest, "//")
					return dir, strings.Trim(strings.TrimSpace(rest), `"`)
				}
			}
			return "", ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", ""
})

// untrim maps a file that -trimpath recorded as <module path>/<path in
// module> back to where it is, if it is in startDir's module. CI builds with
// -trimpath.
func untrim(file string) string {
	if filepath.IsAbs(file) {
		return file
	}
	root, path := startModule()
	if rest, ok := strings.CutPrefix(file, path+"/"); ok && root != "" && path != "" {
		return filepath.Join(root, filepath.FromSlash(rest))
	}
	return file
}
