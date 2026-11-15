package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/mcp/mcptest"
	"shelley.exe.dev/models"
	"shelley.exe.dev/modelsources"
	"shelley.exe.dev/server"
)

// mcpEnv is a real Shelley server on a Unix socket, used through the real
// `shelley mcp` code.
type mcpEnv struct {
	t    *testing.T
	sock string

	mu    sync.Mutex
	paths []string // of the requests to the server
}

func newMCPEnv(t *testing.T) *mcpEnv {
	t.Setenv("SHELLEY_CONVERSATION_ID", "")
	t.Setenv("SHELLEY_SOCKET", "")
	t.Setenv("TMPDIR", t.TempDir()) // for the files call saves

	database, cleanup := db.NewTestDB(t)
	t.Cleanup(cleanup)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	llmManager := server.NewLLMServiceManager(&server.LLMConfig{
		Models: modelsources.Build(models.All(), []modelsources.Source{modelsources.Predictable()}, nil, logger),
		Logger: logger,
	})
	svr := server.NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "predictable", "")
	mux := http.NewServeMux()
	svr.RegisterRoutes(mux)
	e := &mcpEnv{t: t}

	// t.TempDir paths can be too long for a Unix socket.
	dir, err := os.MkdirTemp("", "mcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	e.sock = filepath.Join(dir, "s")
	ln, err := net.Listen("unix", e.sock)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.paths = append(e.paths, r.Method+" "+r.URL.Path)
		e.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	ts.Listener = ln
	ts.Start()
	t.Cleanup(ts.Close)
	t.Cleanup(svr.CloseMCP) // before ts.Close, which waits for calls
	return e
}

// run runs `shelley mcp -url ... args...`.
func (e *mcpEnv) run(stdin string, args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer
	code = runMCP(append([]string{"-url", "unix://" + e.sock}, args...), strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

// ok runs `shelley mcp` and returns its output, requiring success.
func (e *mcpEnv) ok(args ...string) string {
	e.t.Helper()
	out, errOut, code := e.run("", args...)
	if code != 0 {
		e.t.Fatalf("shelley mcp %q: exit %d\nstdout: %s\nstderr: %s", args, code, out, errOut)
	}
	return out
}

// fail runs `shelley mcp`, requires the exit status code, and returns
// stderr.
func (e *mcpEnv) fail(code int, args ...string) string {
	e.t.Helper()
	out, errOut, got := e.run("", args...)
	if got != code {
		e.t.Fatalf("shelley mcp %q: exit %d, want %d\nstdout: %s\nstderr: %s", args, got, code, out, errOut)
	}
	return errOut
}

// requests returns the requests to the server since the last call.
func (e *mcpEnv) requests() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.paths
	e.paths = nil
	return p
}

func requireContains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("output does not contain %q:\n%s", want, got)
		}
	}
}

func TestMCPServers(t *testing.T) {
	e := newMCPEnv(t)
	if out := e.ok("list"); out != "NAME  AUTH  URL  DESCRIPTION\n" {
		t.Fatalf("list: %q", out)
	}
	// Flags may come before or after arguments.
	e.ok("add", "web", "https://example.com/mcp", "-d", "A web server", "-H", "Authorization: Bearer s3cret")
	e.ok("add", "-H", "X-Key: hunter2", "other", "https://example.org/mcp")
	out := e.ok("list")
	if want := "NAME   AUTH  URL                      DESCRIPTION\n" +
		"other  -     https://example.org/mcp  \n" +
		"web    -     https://example.com/mcp  A web server\n"; out != want {
		t.Fatalf("list:\n%s\nwant:\n%s", out, want)
	}
	out = e.ok("list", "-json")
	if strings.Contains(out, "s3cret") || strings.Contains(out, "hunter2") {
		t.Fatalf("list -json printed a header value:\n%s", out)
	}
	var servers []mcpServer
	if err := json.Unmarshal([]byte(out), &servers); err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers[1] != (mcpServer{Name: "web", URL: "https://example.com/mcp", Description: "A web server"}) ||
		!strings.HasSuffix(servers[0].LoginURL, "/mcp/login/other") {
		t.Fatalf("list -json: %+v", servers)
	}

	requireContains(t, e.fail(1, "add", "web", "https://example.com/x"), "shelley mcp: ", "web")
	requireContains(t, e.fail(1, "auth", "web"), `MCP server "web" doesn't use Shelley's OAuth login`)
	e.ok("rm", "web")
	if errOut := e.fail(1, "rm", "web"); !strings.HasPrefix(errOut, "shelley mcp: ") {
		t.Fatalf("rm web again: %q", errOut)
	}

	for _, args := range [][]string{
		{},
		{"bogus"},
		{"add", "x"},
		{"add", "x", "-H", "no colon", "https://example.com"},
		{"list", "a", "b"},
		{"list", "-nope"},
		{"call", "other"},
		{"call", "other.x", "-", "a=1"},
		{"rm"},
	} {
		requireContains(t, e.fail(2, args...), "shelley mcp: ", "Usage: shelley mcp")
	}
	if out, errOut, code := e.run("", "call", "-h"); code != 0 || !strings.HasPrefix(out, "Usage: shelley mcp") || errOut != "" {
		t.Fatalf("call -h: %d %q %q", code, out, errOut)
	}

	// Shelley tells the commands it runs where its socket is.
	t.Setenv("SHELLEY_SOCKET", e.sock)
	var stdout, stderr bytes.Buffer
	if code := runMCP([]string{"list"}, nil, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "other") {
		t.Fatalf("list with $SHELLEY_SOCKET: %d %q %q", code, stdout.String(), stderr.String())
	}
}

func TestMCPList(t *testing.T) {
	e := newMCPEnv(t)
	f := mcptest.NewServer(t)
	e.ok("add", "f", f.URL)

	var tools struct {
		Server map[string]any
		Tools  []map[string]any
	}
	if err := json.Unmarshal([]byte(e.ok("list", "-json", "f")), &tools); err != nil {
		t.Fatal(err)
	}
	if tools.Server["name"] != "mcptest" || len(tools.Tools) == 0 {
		t.Fatalf("list -json f: %v", tools)
	}
	echo := "/**\n * Echo text back.\n * Second line.\n * @readOnly\n * @param text the text to echo\n */\nfunction echo(text: string);\n"
	requireContains(t, e.ok("list", "f"), fmt.Sprintf("f: MCP Test (v1.0), %d tools\n// Test fixture.\n\n", len(tools.Tools)), "\n\n"+echo,
		"\n\n/**\n * Add a and b.\n */\nfunction add(a: number, b: number);\n")
	if out := e.ok("list", "f.echo"); out != echo {
		t.Fatalf("list f.echo: %q", out)
	}
	requireContains(t, e.ok("list", "f.add", "-schema"), "function add(a: number, b: number);\n\n{\n  \"inputSchema\": {", "\"outputSchema\": {")

	var tool map[string]any
	if err := json.Unmarshal([]byte(e.ok("list", "-json", "f.echo")), &tool); err != nil || tool["name"] != "echo" {
		t.Fatalf("list -json f.echo: %v %v", tool, err)
	}

	requireContains(t, e.fail(1, "list", "f.nope"), `MCP server "f" has no tool "nope"`)
	requireContains(t, e.fail(1, "list", "nope"), "shelley mcp: ", "nope")
}

func TestMCPSearch(t *testing.T) {
	e := newMCPEnv(t)
	f := mcptest.NewServer(t)
	g := mcptest.NewServer(t)
	e.ok("add", "f", f.URL)
	e.ok("add", "g", g.URL)

	// A term in a description matches, across every server, with the full tool
	// documentation and a server heading.
	out := e.ok("search", "echo")
	requireContains(t, out, "# f\n\nf.echo\n", "Echo text back.", "# g\n\ng.echo\n")
	if strings.Contains(out, "function add(") {
		t.Fatalf("search echo returned add: %q", out)
	}

	// A leading server name limits the search to that server.
	out = e.ok("search", "f", "echo")
	if !strings.Contains(out, "f.echo") || strings.Contains(out, "g.echo") {
		t.Fatalf("search f echo: %q", out)
	}

	// Every term must match (AND).
	if out := e.ok("search", "echo", "nonesuch"); !strings.Contains(out, "no tools match") {
		t.Fatalf("search echo nonesuch: %q", out)
	}

	// -json returns the matches as an array of {server, tool}.
	var matches []struct {
		Server string         `json:"server"`
		Tool   map[string]any `json:"tool"`
	}
	if err := json.Unmarshal([]byte(e.ok("search", "-json", "add")), &matches); err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].Tool["name"] != "add" {
		t.Fatalf("search -json add: %v", matches)
	}
}

func TestMCPCall(t *testing.T) {
	e := newMCPEnv(t)
	f := mcptest.NewServer(t)
	e.ok("add", "f", f.URL)

	if out := e.ok("call", "f.echo", `text={"a": 1}`); out != "{\"a\": 1}\n" {
		t.Fatalf("call f.echo: %q", out)
	}
	// Arguments on stdin go to the server without fetching the tools.
	e.requests()
	if out, errOut, code := e.run(`{"text":"from stdin"}`, "call", "f.echo", "-"); code != 0 || out != "from stdin\n" {
		t.Fatalf("call with stdin: %d %q %q", code, out, errOut)
	}
	if r := e.requests(); !slices.Equal(r, []string{"POST /api/mcp/servers/f/call"}) {
		t.Fatalf("requests: %q", r)
	}
	if _, errOut, code := e.run(`[1]`, "call", "f.echo", "-"); code != 1 || !strings.Contains(errOut, "JSON object") {
		t.Fatalf("call with an array on stdin: %d %q", code, errOut)
	}

	// Images are saved to files.
	out := e.ok("call", "f.image")
	m := regexp.MustCompile(`^\[image saved to (\S+\.png) \(image/png, \d+ B\)\]\n$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("call f.image: %q", out)
	}
	if b, err := os.ReadFile(m[1]); err != nil || !bytes.Equal(b, mcptest.PNG) {
		t.Fatalf("saved image: %q %v", b, err)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(e.ok("call", "f.echo", "text=hi", "-json")), &res); err != nil || res["content"] == nil {
		t.Fatalf("call -json: %v %v", res, err)
	}

	// Arguments are checked before calling.
	if errOut := e.fail(1, "call", "f.echo", "txt=hi"); errOut != "shelley mcp: f.echo has no parameter \"txt\"\nfunction echo(text: string);\n" {
		t.Fatalf("unknown parameter: %q", errOut)
	}
	requireContains(t, e.fail(1, "call", "f.nope", "a=1"), `MCP server "f" has no tool "nope"`)

	// Structured content is printed unless a text block holds it.
	if out := e.ok("call", "f.add", "a=2", "b=3"); out != "{\"sum\":5}\n" {
		t.Fatalf("call f.add: %q", out)
	}
	// A tool's error goes to stderr.
	if out, errOut, code := e.run("", "call", "f.fail"); code != 1 || out != "" || errOut != "it failed\n" {
		t.Fatalf("call f.fail: %d %q %q", code, out, errOut)
	}
	if out, _, code := e.run("", "call", "f.fail", "-json"); code != 1 || !strings.Contains(out, `"isError": true`) {
		t.Fatalf("call f.fail -json: %d %q", code, out)
	}

	// Calls use the caller's conversation's session, which restart ends.
	none := e.ok("call", "f.session")
	t.Setenv("SHELLEY_CONVERSATION_ID", "c")
	conv := e.ok("call", "f.session")
	e.ok("restart", "f")
	if conv == none || e.ok("call", "f.session") == conv {
		t.Fatalf("sessions: %q without a conversation, %q in one, restarted", none, conv)
	}

	// A call that times out may still have run.
	requireContains(t, e.fail(1, "call", "f.block", "-timeout", "50ms"), "shelley mcp: ", "may still have run")
}

func TestMCPLoginRequired(t *testing.T) {
	e := newMCPEnv(t)
	locked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(locked.Close)
	e.ok("add", "locked", locked.URL)

	out := e.ok("auth", "locked")
	link := strings.TrimPrefix(strings.Split(out, "\n")[1], "Log in: ")
	// The login link is root-relative so it resolves against whatever host the
	// user reached Shelley on (see mcpLoginURL).
	if out != "locked: not logged in\nLog in: /mcp/login/locked\n" {
		t.Fatalf("auth: %q", out)
	}
	want := "shelley mcp: MCP server \"locked\" needs authentication. [Sign in](" + link + "), then retry.\n"
	if errOut := e.fail(1, "call", "locked.echo", "text=x"); errOut != want {
		t.Fatalf("call: %q, want %q", errOut, want)
	}
	if out := e.ok("auth", "locked"); out != "locked: login required\nLog in: "+link+"\n" {
		t.Fatalf("auth after a 401: %q", out)
	}
	requireContains(t, e.ok("list"), "login required")
}
