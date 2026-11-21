package test

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"shelley.exe.dev/server/resource"

	lazycue "github.com/boldsoftware/shelley/lazycue"
)

// The e2e tests are browser tests against a real shelley binary. Every
// attempt at one (lazycue's cached run, its retries, an agent's session and
// the re-check of the agent's script) gets a server of its own: a fresh
// database, home directory and git working directory, behind a proxy the
// test can intercept requests on. A test's setup function prepares that
// world afresh for each attempt.

// e2eModuleDir is the shelley module, set by TestMain; initE2E fills in
// the rest when the first e2e test runs: e2eBin is the shelley binary the
// tests run, e2eRoot the directory their servers live in, e2eLive the fake
// live-session provider and e2eDB a migrated database each server starts
// from.
var (
	e2eModuleDir string

	e2eOnce sync.Once
	e2eErr  error
	e2eBin  string
	e2eRoot string
	e2eLive string
	e2eDB   string
	e2eStop func()

	e2eMu        sync.Mutex
	e2eHarnesses []*lazycue.Harness
	e2eServers   = map[*shelley]bool{} // not yet closed
	e2eStopping  bool                  // no new servers
)

// initE2E prepares for the e2e tests once.
func initE2E() error {
	e2eOnce.Do(func() { e2eErr = startE2E() })
	return e2eErr
}

func startE2E() (err error) {
	sweepE2E()
	// Short: unix sockets live in here, and their paths are limited to
	// ~100 bytes. The pid lets sweepE2E tell a dead run's from a live one's.
	e2eRoot, err = os.MkdirTemp("", fmt.Sprintf("se2e-%d-", os.Getpid()))
	if err != nil {
		return err
	}
	// Below resource.DiskThreshold free, shelley shows a low-disk notice on
	// every page, which moves the layout under test. Leave the run room for
	// its own files.
	var fs unix.Statfs_t
	if err := unix.Statfs(e2eRoot, &fs); err != nil {
		return err
	}
	if free := fs.Bavail * uint64(fs.Bsize); free < 2*resource.DiskThreshold {
		return fmt.Errorf("only %d MB free in %s; shelley's low-disk notice (below %d MB) would change every page", free>>20, e2eRoot, resource.DiskThreshold>>20)
	}
	// Servers are in process groups of their own, which miss the terminal's
	// interrupt.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		stopE2E()
		os.Exit(1)
	}()
	e2eBin = os.Getenv("SHELLEY_BIN")
	if e2eBin == "" {
		e2eBin = filepath.Join(e2eRoot, "shelley")
		cmd := exec.Command("go", "build", "-o", e2eBin, "./cmd/shelley")
		cmd.Dir = e2eModuleDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("build shelley: %w\n%s", err, out)
		}
	}
	var sessions atomic.Int64
	live := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"session":{"id":"live_%d"},"transport":{"type":"webrtc","sdp":"v=0"}}`, sessions.Add(1))
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	go live.Serve(ln)
	e2eStop = func() { live.Close() }
	e2eLive = "http://" + ln.Addr().String() + "/v1/live/sessions"

	// Migrating a new database takes most of a server's start.
	sh, err := newShelley(context.Background())
	if err != nil {
		return err
	}
	defer sh.close()
	if err := sh.try(sh.start); err != nil {
		return fmt.Errorf("start shelley: %w\nserver log:\n%s", err, sh.logTail())
	}
	sh.stop()
	e2eDB = filepath.Join(e2eRoot, "template.db")
	for _, suffix := range []string{"", "-wal"} {
		b, err := os.ReadFile(filepath.Join(sh.dir, "test.db") + suffix)
		if errors.Is(err, os.ErrNotExist) && suffix != "" {
			continue
		}
		if err == nil {
			err = os.WriteFile(e2eDB+suffix, b, 0o600)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// stopE2E closes the servers still running and removes what the e2e tests
// left behind.
func stopE2E() {
	e2eMu.Lock()
	e2eStopping = true
	servers := slices.Collect(maps.Keys(e2eServers))
	e2eMu.Unlock()
	for _, s := range servers {
		s.close()
	}
	if e2eStop != nil {
		e2eStop()
	}
	if e2eRoot != "" {
		killTerminalSessions(e2eRoot)
		killStrays(e2eRoot)
		os.RemoveAll(e2eRoot)
	}
}

// sweepE2E removes what runs that died without stopE2E (a test timeout, a
// kill) left behind: their terminal sessions and directories. On Linux their
// servers died with them (see serverProcAttr).
func sweepE2E() {
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), "se2e-*-*"))
	for _, dir := range dirs {
		pid, err := strconv.Atoi(strings.Split(filepath.Base(dir), "-")[1])
		if err != nil || syscall.Kill(pid, 0) != syscall.ESRCH {
			continue
		}
		killTerminalSessions(dir)
		killStrays(dir)
		os.RemoveAll(dir)
	}
}

// e2eResults is every e2e test's lazycue result, for the report.
func e2eResults() []*lazycue.TestResult {
	e2eMu.Lock()
	defer e2eMu.Unlock()
	var results []*lazycue.TestResult
	for _, h := range e2eHarnesses {
		results = append(results, h.Results()...)
	}
	return results
}

// e2e runs a browser test of description against a fresh shelley that setup
// (which may be nil) prepares for every attempt.
func e2e(t *testing.T, description string, setup func(s *shelley)) {
	t.Helper()
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run the LazyCue browser integration tests")
	}
	t.Parallel()
	if err := initE2E(); err != nil {
		t.Fatal(err)
	}
	opts := appOpts
	opts.BaseURL = ""
	opts.Setup = func(ctx context.Context) (*lazycue.Env, error) {
		env, err := setUp(ctx, setup)
		if err != nil && ctx.Err() == nil {
			// Playwright retried a test whose setup failed; so does this,
			// once.
			t.Logf("setup failed, retrying: %v", err)
			env, err = setUp(ctx, setup)
		}
		return env, err
	}
	h := lazycue.New(opts)
	e2eMu.Lock()
	e2eHarnesses = append(e2eHarnesses, h)
	e2eMu.Unlock()
	t.Cleanup(writeSummary)
	h.Test(t, description)
}

// setUp starts a shelley that setup prepares.
func setUp(ctx context.Context, setup func(s *shelley)) (*lazycue.Env, error) {
	sh, err := newShelley(ctx)
	if err != nil {
		return nil, err
	}
	if err := sh.try(func() {
		if setup != nil {
			setup(sh)
		}
		sh.start()
	}); err != nil {
		if tail := sh.logTail(); tail != "" {
			err = fmt.Errorf("%w\nserver log:\n%s", err, tail)
		}
		sh.close()
		return nil, err
	}
	return sh.env(), nil
}

// setupError carries a failure out of a setup function, which reports
// failures by panicking so that it reads as a straight line.
type setupError struct{ err error }

// shelley is the server an attempt at an e2e test runs against, and what its
// setup function prepares.
type shelley struct {
	ctx       context.Context
	dir       string // holds everything below
	cwd       string // the server's working directory: a git repository
	home      string
	serverEnv []string
	launch    lazycue.Launch
	vars      map[string]string
	calls     map[string]func(ctx context.Context) (string, error)

	mux       *http.ServeMux
	pass      http.Handler    // passes a request on to the server
	transport *http.Transport // talks to the server
	proxy     *http.Server
	url       string // the proxy, which the browser talks to
	backend   string // the server itself
	closed    sync.Once

	mu       sync.Mutex
	cmd      *exec.Cmd // until stopped
	exited   chan struct{}
	stopped  bool
	headers  http.Header
	requests []string
}

func newShelley(ctx context.Context) (*shelley, error) {
	dir, err := os.MkdirTemp(e2eRoot, "")
	if err != nil {
		return nil, err
	}
	s := &shelley{
		ctx:       ctx,
		dir:       dir,
		cwd:       filepath.Join(dir, "cwd"),
		home:      filepath.Join(dir, "home"),
		vars:      map[string]string{},
		calls:     map[string]func(ctx context.Context) (string, error){},
		mux:       http.NewServeMux(),
		transport: http.DefaultTransport.(*http.Transport).Clone(),
		headers:   http.Header{},
	}
	e2eMu.Lock()
	if e2eStopping {
		e2eMu.Unlock()
		os.RemoveAll(dir)
		return nil, errors.New("the e2e tests are stopping")
	}
	e2eServers[s] = true
	e2eMu.Unlock()
	s.vars["CWD"] = s.cwd
	s.vars["HOME"] = s.home
	if err := s.try(func() {
		s.mkdir(s.cwd)
		s.mkdir(s.home)
		s.git("init", "--quiet", "--initial-branch=main")
		s.write("example.txt", "before\n")
		s.git("add", "example.txt")
		s.git("commit", "--quiet", "-m", "Initial test fixture")
		s.write("example.txt", "after\n")
		s.git("commit", "--quiet", "-am", "Update test fixture")
	}); err != nil {
		s.close()
		return nil, err
	}
	// /new starts in the fixture directory. Otherwise it falls back to the
	// most recent conversation's, which may be one the test deleted.
	s.initScript(fmt.Sprintf(`if (!localStorage.getItem('shelley_selected_cwd')) localStorage.setItem('shelley_selected_cwd', %q);`, s.cwd))
	return s, nil
}

// try runs f, returning the error a helper in it panicked with, or any
// other panic as an error.
func (s *shelley) try(f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if se, ok := r.(setupError); ok {
				err = se.err
			} else {
				err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
			}
		}
	}()
	f()
	return nil
}

// fail aborts the setup function.
func (s *shelley) fail(format string, args ...any) {
	panic(setupError{fmt.Errorf(format, args...)})
}

func (s *shelley) check(err error) {
	if err != nil {
		panic(setupError{err})
	}
}

// env is the lazycue environment for the started app.
func (s *shelley) env() *lazycue.Env {
	return &lazycue.Env{
		BaseURL: s.url,
		Launch:  s.launch,
		Vars:    s.vars,
		Calls:   s.calls,
		Close:   s.close,
	}
}

// path resolves name against the working directory.
func (s *shelley) path(name string) string {
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(s.cwd, name)
}

func (s *shelley) mkdir(name string) string {
	p := s.path(name)
	s.check(os.MkdirAll(p, 0o755))
	return p
}

// write writes a file, relative to the working directory unless absolute.
func (s *shelley) write(name, content string) string {
	p := s.path(name)
	s.check(os.MkdirAll(filepath.Dir(p), 0o755))
	s.check(os.WriteFile(p, []byte(content), 0o644))
	return p
}

// read reads a file, relative to the working directory unless absolute.
func (s *shelley) read(name string) string {
	b, err := os.ReadFile(s.path(name))
	s.check(err)
	return string(b)
}

// git runs git in the working directory, hermetically.
func (s *shelley) git(args ...string) string {
	return s.gitIn(s.cwd, args...)
}

// gitIn runs git in dir, hermetically.
func (s *shelley) gitIn(dir string, args ...string) string {
	cmd := exec.CommandContext(s.ctx, "git", append([]string{
		"-C", dir, "-c", "user.name=Shelley Test", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
	}, args...)...)
	cmd.Env = hermeticGitEnv(append(os.Environ(), "HOME="+s.home))
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.fail("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// hermeticGitEnv strips from env what would make git read the runner's
// configuration or another repository.
func hermeticGitEnv(env []string) []string {
	env = slices.DeleteFunc(env, func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "EMAIL", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_AUTHOR_DATE", "GIT_AUTHOR_EMAIL",
			"GIT_AUTHOR_NAME", "GIT_COMMITTER_DATE", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_NAME",
			"GIT_COMMON_DIR", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GIT_DIR", "GIT_INDEX_FILE",
			"GIT_NAMESPACE", "GIT_OBJECT_DIRECTORY", "GIT_TEMPLATE_DIR", "GIT_WORK_TREE",
			"GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM":
			return true
		}
		return strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
	})
	return append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
}

// setenv sets server environment variables, "NAME=value", before it starts.
func (s *shelley) setenv(kv ...string) {
	if s.running() {
		s.fail("setenv after the server started")
	}
	s.serverEnv = append(s.serverEnv, kv...)
}

// v makes {{name}} in the test's steps stand for value. A call may set one
// for the steps after it.
func (s *shelley) v(name, value string) {
	s.vars[name] = value
}

// call lets a call step named name run f on the host. f may use the
// helpers that fail setup; there they fail the step.
func (s *shelley) call(name string, f func() string) {
	s.calls[name] = func(context.Context) (got string, err error) {
		err = s.try(func() { got = f() })
		return got, err
	}
}

// initScript runs js in every page before the page's own scripts. Scripts
// that throw (localStorage, say, in a frame with an opaque origin) do not
// stop the ones after them.
func (s *shelley) initScript(js string) {
	s.launch.InitScript += "try {\n" + js + "\n} catch (e) { console.error('init script:', e); }\n"
}

// pageFlag sets a feature flag in the page's localStorage, as the
// browser-side flag overrides do.
func (s *shelley) pageFlag(name string, value any) {
	s.initScript(fmt.Sprintf(`localStorage.setItem(%q, %q);`, "ff:"+name, mustJSON(value)))
}

// patchInit merges patch into window.__SHELLEY_INIT__ as the page sets it.
func (s *shelley) patchInit(patch map[string]any) {
	s.initScript(fmt.Sprintf(`const patch = %s;
const prior = Object.getOwnPropertyDescriptor(window, '__SHELLEY_INIT__');
let init;
Object.defineProperty(window, '__SHELLEY_INIT__', {
  configurable: true,
  get: () => init,
  set: (value) => {
    prior?.set?.(value);
    init = { ...(prior?.get?.() ?? value), ...patch };
  },
});`, mustJSON(patch)))
}

//go:embed testdata/clock.js
var clockJS string

// clock installs a fake clock, as Playwright's page.clock.install does,
// starting at start (now if zero). Steps drive it with window.__clock; see
// testdata/clock.js.
func (s *shelley) clock(start time.Time) {
	at := "undefined"
	if !start.IsZero() {
		at = strconv.FormatInt(start.UnixMilli(), 10)
	}
	s.initScript("window.__CLOCK_START__ = " + at + ";\n" + clockJS)
}

// device sets the device the browser emulates; the default is a Pixel 5.
func (s *shelley) device(d lazycue.Device) {
	s.launch.Device = &d
}

// desktop is a desktop browser window, Playwright's default viewport.
var desktop = lazycue.Device{Width: 1280, Height: 720, Scale: 1}

// viewport resizes the default Pixel 5's viewport, keeping its touch screen
// and scale, as Playwright's viewport option does.
func (s *shelley) viewport(width, height int) {
	d := lazycue.Pixel5
	d.Width, d.Height = width, height
	s.device(d)
}

// media sets a CSS media feature; an empty value drops a default.
func (s *shelley) media(feature, value string) {
	if s.launch.Media == nil {
		s.launch.Media = map[string]string{}
	}
	s.launch.Media[feature] = value
}

// timezone sets the browser's time zone.
func (s *shelley) timezone(tz string) {
	s.launch.Timezone = tz
}

// header adds a header to every request the browser sends the server.
func (s *shelley) header(name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headers.Set(name, value)
}

// route has handler answer the browser's requests matching pattern (an
// http.ServeMux pattern) instead of the server. handler may pass a request
// on with s.pass, or forward it with s.forward and answer itself.
func (s *shelley) route(pattern string, handler http.HandlerFunc) {
	s.mux.HandleFunc(pattern, handler)
}

// host has handler answer the browser's requests to another origin, such
// as "https://docs.example", in every tab including popups.
func (s *shelley) host(origin string, handler http.Handler) {
	if s.launch.Hosts == nil {
		s.launch.Hosts = map[string]http.Handler{}
	}
	s.launch.Hosts[origin] = handler
}

// requestLog is the method and URI of every request the browser sent, in
// order.
func (s *shelley) requestLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// forward sends r on to the server and returns its response, decompressed.
func (s *shelley) forward(r *http.Request) (*http.Response, error) {
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.URL.Scheme = "http"
	out.URL.Host = strings.TrimPrefix(s.backend, "http://")
	out.Header.Del("Accept-Encoding")
	s.addHeaders(out.Header)
	return s.transport.RoundTrip(out)
}

// fetchJSON forwards r to the server and decodes its JSON response into out,
// for a route that changes what the server says.
func (s *shelley) fetchJSON(r *http.Request, out any) error {
	resp, err := s.forward(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s", r.Method, r.URL, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// writeJSON answers a request with v.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *shelley) addHeaders(h http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.headers {
		h[k] = v
	}
}

// abort answers a request by dropping the connection, as a network failure
// would.
func abort(w http.ResponseWriter, r *http.Request) {
	panic(http.ErrAbortHandler)
}

// start starts the server, if it has not yet started. Helpers that talk to
// it start it, so setup can prepare files and environment first.
func (s *shelley) start() {
	s.mu.Lock()
	running, stopped := s.cmd != nil, s.stopped
	s.mu.Unlock()
	if running {
		return
	}
	if stopped {
		s.fail("the server has stopped")
	}
	log, err := os.Create(filepath.Join(s.dir, "server.log"))
	s.check(err)
	defer log.Close()
	if e2eDB != "" {
		for _, suffix := range []string{"", "-wal"} {
			if b, err := os.ReadFile(e2eDB + suffix); err == nil {
				s.check(os.WriteFile(filepath.Join(s.dir, "test.db")+suffix, b, 0o600))
			}
		}
	}
	portFile := filepath.Join(s.dir, "port")
	cmd := exec.Command(e2eBin, "--predictable-only", "--db", filepath.Join(s.dir, "test.db"),
		"serve", "--port", "0", "--port-file", portFile, "--socket", filepath.Join(s.dir, "client.sock"))
	cmd.Dir = s.cwd
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Env = append(hermeticGitEnv(os.Environ()),
		"HOME="+s.home,
		"PWD="+s.cwd,
		"PREDICTABLE_DELAY_MS=20",
		"SHELLEY_LIVE_SESSION_URL="+e2eLive,
		"SHELLEY_SKIP_VERSION_CHECK=true",
	)
	cmd.Env = append(cmd.Env, s.serverEnv...)
	cmd.SysProcAttr = serverProcAttr()
	s.check(cmd.Start())
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	s.mu.Lock()
	stopped = s.stopped
	if !stopped {
		s.cmd, s.exited = cmd, exited
	}
	s.mu.Unlock()
	if stopped {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-exited
		s.fail("stopped while starting")
	}

	var port string
	deadline := time.Now().Add(30 * time.Second)
	for {
		if b, err := os.ReadFile(portFile); err == nil && bytes.HasSuffix(b, []byte("\n")) {
			port = strings.TrimSpace(string(b))
			break
		}
		select {
		case <-exited:
			s.fail("shelley exited during startup")
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			s.fail("shelley did not write its port within 30s")
		}
	}
	s.backend = "http://127.0.0.1:" + port

	target, err := url.Parse(s.backend)
	s.check(err)
	s.pass = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = r.In.Host
			s.addHeaders(r.Out.Header)
		},
		Transport:     s.transport,
		FlushInterval: -1,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	s.check(err)
	proxy := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
		s.mu.Unlock()
		if _, pattern := s.mux.Handler(r); pattern != "" {
			s.mux.ServeHTTP(w, r)
			return
		}
		s.pass.ServeHTTP(w, r)
	})}
	s.mu.Lock()
	stopped = s.stopped
	if !stopped {
		s.proxy = proxy
	}
	s.mu.Unlock()
	if stopped {
		ln.Close()
		s.fail("stopped while starting")
	}
	go proxy.Serve(ln)
	s.url = "http://" + ln.Addr().String()

	// Next-prompt suggestions would race the tests' own prompts.
	s.flag("next-prompt-suggestions", false)
}

// logTail is the end of the server's log.
func (s *shelley) logTail() string {
	b, _ := os.ReadFile(filepath.Join(s.dir, "server.log"))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.Join(lines[max(0, len(lines)-20):], "\n")
}

// close stops the server and removes everything the attempt made.
func (s *shelley) close() {
	s.closed.Do(func() {
		e2eMu.Lock()
		delete(e2eServers, s)
		e2eMu.Unlock()
		s.stop()
		killTerminalSessions(s.dir)
		killStrays(s.dir)
		os.RemoveAll(s.dir)
	})
}

// running reports whether the server has started and not yet stopped.
func (s *shelley) running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmd != nil
}

// stop stops the server and the proxy in front of it. A start still under
// way stops itself.
func (s *shelley) stop() {
	s.mu.Lock()
	cmd, exited, proxy := s.cmd, s.exited, s.proxy
	s.cmd, s.proxy, s.stopped = nil, nil, true
	s.mu.Unlock()
	if proxy != nil {
		proxy.Close()
	}
	// The server waits for idle connections to close as it shuts down.
	s.transport.CloseIdleConnections()
	if cmd != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-exited
		}
	}
}

// killTerminalSessions kills the terminal sessions servers under dir left
// running: exe-scroll session servers detach from shelley, and the shells
// they run are in sessions of their own.
func killTerminalSessions(dir string) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,pgid=,sess=,command=").Output()
	if err != nil {
		return
	}
	groups, servers := terminalKillTargets(string(out), dir)
	for _, g := range groups {
		syscall.Kill(-g, syscall.SIGKILL)
	}
	for _, pid := range servers {
		syscall.Kill(pid, syscall.SIGKILL)
	}
}

// terminalKillTargets reads ps rows, "pid ppid pgid sess command", for
// what killTerminalSessions kills: the process groups of the sessions that
// exe-scroll session servers under dir run, and the servers. sess is
// opaque: Darwin's ps prints it as an address in hex.
func terminalKillTargets(ps, dir string) (groups, servers []int) {
	type proc struct {
		pid, ppid, pgid int
		sess, command   string
	}
	var procs []proc
	line := regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+(\d+)\s+(\S+)\s+(.*)$`)
	for _, l := range strings.Split(ps, "\n") {
		m := line.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		pid, _ := strconv.Atoi(m[1])
		ppid, _ := strconv.Atoi(m[2])
		pgid, _ := strconv.Atoi(m[3])
		procs = append(procs, proc{pid, ppid, pgid, m[4], m[5]})
	}
	serverSess := map[int]string{} // pid -> session
	for _, p := range procs {
		if strings.HasPrefix(p.command, "exe-scroll: session "+dir+"/") {
			serverSess[p.pid] = p.sess
		}
	}
	sessions := map[string]bool{}
	for _, p := range procs {
		if sess, ok := serverSess[p.ppid]; ok && p.pid == p.pgid && p.sess != sess {
			sessions[p.sess] = true
		}
	}
	groupSet := map[int]bool{}
	for _, p := range procs {
		if sessions[p.sess] {
			groupSet[p.pgid] = true
		}
	}
	return slices.Sorted(maps.Keys(groupSet)), slices.Sorted(maps.Keys(serverSess))
}

// do sends the server an API request with a JSON body (nil for none) and
// returns the response body, failing on an error status.
func (s *shelley) do(method, path string, body any) []byte {
	var r io.Reader
	if body != nil {
		r = strings.NewReader(mustJSON(body))
	}
	return s.send(method, path, "application/json", r)
}

// postForm posts a form to the server and returns the response body,
// failing on an error status.
func (s *shelley) postForm(path string, form url.Values) []byte {
	return s.send("POST", path, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
}

func (s *shelley) send(method, path, contentType string, body io.Reader) []byte {
	s.start()
	req, err := http.NewRequestWithContext(s.ctx, method, s.backend+path, body)
	s.check(err)
	req.Header.Set("Content-Type", contentType)
	s.addHeaders(req.Header)
	resp, err := (&http.Client{Transport: s.transport}).Do(req)
	s.check(err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	s.check(err)
	if resp.StatusCode/100 != 2 {
		s.fail("%s %s: %s: %s", method, path, resp.Status, b)
	}
	return b
}

// api is do, decoding the response into out (nil to discard it).
func (s *shelley) api(method, path string, body, out any) {
	b := s.do(method, path, body)
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			s.fail("%s %s: decode %s: %v", method, path, b, err)
		}
	}
}

// flag sets a server feature flag.
func (s *shelley) flag(name string, value any) {
	s.api("POST", "/feature-flags", map[string]any{"name": name, "value": value}, nil)
}

// convo is a conversation a test created.
type convo struct {
	ID   string
	Slug string
}

// conversation starts a conversation in the working directory with message
// to the predictable model and waits for its reply. A non-empty slug renames
// it; otherwise it keeps the slug the server gave it.
func (s *shelley) conversation(message, slug string) convo {
	return s.conversationWith(map[string]any{"message": message, "model": "predictable", "cwd": s.cwd}, slug)
}

// conversationWith is conversation with the request body spelled out, for
// another directory or conversation options.
func (s *shelley) conversationWith(body map[string]any, slug string) convo {
	var created struct {
		ID string `json:"conversation_id"`
	}
	s.api("POST", "/api/conversations/new", body, &created)
	c := convo{ID: created.ID}
	s.poll(30*time.Second, func() error {
		var got struct {
			Messages     []apiMessage `json:"messages"`
			Conversation struct {
				Slug string `json:"slug"`
			} `json:"conversation"`
		}
		s.api("GET", "/api/conversation/"+c.ID, nil, &got)
		if !slices.ContainsFunc(got.Messages, apiMessage.endsTurn) {
			return errors.New("no reply yet")
		}
		if got.Conversation.Slug == "" {
			return errors.New("no slug yet")
		}
		c.Slug = got.Conversation.Slug
		return nil
	})
	if slug != "" {
		s.api("POST", "/api/conversation/"+c.ID+"/rename", map[string]string{"slug": slug}, nil)
		c.Slug = slug
	}
	return c
}

// apiMessage is what the tests read of a message the API returns.
type apiMessage struct {
	Type      string `json:"type"`
	EndOfTurn bool   `json:"end_of_turn"`
}

func (m apiMessage) endsTurn() bool {
	return m.Type == "agent" && m.EndOfTurn
}

// poll runs f until it returns nil, failing setup with its last error after
// timeout.
func (s *shelley) poll(timeout time.Duration, f func() error) {
	deadline := time.Now().Add(timeout)
	for {
		err := f()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			s.fail("after %s: %v", timeout, err)
		}
		select {
		case <-s.ctx.Done():
			s.fail("%v: %v", s.ctx.Err(), err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
