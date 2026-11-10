package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/mcp"
	"shelley.exe.dev/mcp/mcptest"
	"shelley.exe.dev/version"
)

// mcpEnv is a Shelley server reached through its TCP middleware.
type mcpEnv struct {
	t      *testing.T
	db     *db.DB
	srv    *Server
	ts     *httptest.Server
	header string // the header Shelley requires, which requests carry
}

func newMCPEnv(t *testing.T, requireHeader string) *mcpEnv {
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	e := &mcpEnv{t: t, db: database, header: requireHeader}
	e.srv = NewServer(database, &testLLMManager{service: predictable.NewService()}, claudetool.ToolSetConfig{},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})), true, "predictable", requireHeader)
	e.srv.hooksDir = t.TempDir()
	mux := http.NewServeMux()
	e.srv.RegisterRoutes(mux)
	e.ts = httptest.NewServer(e.srv.tcpHandler(mux))
	t.Cleanup(e.ts.Close)
	t.Cleanup(e.srv.CloseMCP) // first: ts.Close waits for requests
	return e
}

// do sends an API request from the conversation scope ("" for none).
func (e *mcpEnv) do(method, path, scope, body string) (int, string) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if scope != "" {
		req.Header.Set("X-Shelley-Conversation-Id", scope)
	}
	if e.header != "" {
		req.Header.Set(e.header, "user")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func (e *mcpEnv) want(code int, method, path, scope, body string) string {
	e.t.Helper()
	got, b := e.do(method, path, scope, body)
	if got != code {
		e.t.Fatalf("%s %s: %d %s, want %d", method, path, got, b, code)
	}
	return b
}

func (e *mcpEnv) add(name, url string) {
	e.t.Helper()
	e.want(200, "POST", "/api/mcp/servers", "", fmt.Sprintf(`{"name":%q,"url":%q,"headers":{"X-Test":"yes"}}`, name, url))
}

// call calls tool and returns the text of its result.
func (e *mcpEnv) call(server, scope, tool, args string) string {
	e.t.Helper()
	b := e.want(200, "POST", "/api/mcp/servers/"+server+"/call", scope, fmt.Sprintf(`{"tool":%q,"arguments":%s}`, tool, args))
	var res struct {
		Content []struct{ Text string }
	}
	if err := json.Unmarshal([]byte(b), &res); err != nil || len(res.Content) != 1 {
		e.t.Fatalf("result %s: %v", b, err)
	}
	return res.Content[0].Text
}

func TestMCPServersAPI(t *testing.T) {
	e := newMCPEnv(t, "")
	for body, want := range map[string]string{
		`{"name":"a.b","url":"http://x"}`:                             `invalid name "a.b"`,
		`{"name":"a","url":"ftp://x"}`:                                `invalid URL "ftp://x"`,
		`{"name":"a","url":"http://x","headers":{"A":"1","a":"2"}}`:   `duplicate header`,
		`{"name":"a","url":"http://x","headers":{"Bad Name":"1"}}`:    `invalid header "Bad Name"`,
		`{"name":"a","url":"http://x","headers":{"Ok":"line\nfeed"}}`: `invalid header "Ok"`,
	} {
		if b := e.want(400, "POST", "/api/mcp/servers", "", body); !strings.Contains(b, want) {
			t.Errorf("%s: %q, want %q", body, b, want)
		}
	}
	e.want(200, "POST", "/api/mcp/servers", "", `{"name":"keyed","url":"http://k","headers":{"authorization":"Bearer k"}}`)
	e.want(200, "POST", "/api/mcp/servers", "", `{"name":"open","url":"http://o","description":"d"}`)
	e.want(400, "POST", "/api/mcp/servers", "", `{"name":"open","url":"http://o"}`)
	e.want(400, "PUT", "/api/mcp/servers/open", "", `{"name":"other","url":"http://o"}`)
	e.want(404, "PUT", "/api/mcp/servers/nope", "", `{"url":"http://o"}`)
	e.want(200, "PUT", "/api/mcp/servers/open", "", `{"url":"http://o2","description":"d2","auth":"","login_url":"x"}`)

	var servers []map[string]any
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/mcp/servers", "", "")), &servers); err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers[0]["name"] != "keyed" || servers[0]["login_url"] != nil ||
		servers[1]["url"] != "http://o2" || servers[1]["description"] != "d2" || servers[1]["auth"] != "" ||
		servers[1]["login_url"] != e.srv.mcpLoginURL("open") {
		t.Fatalf("servers: %v", servers)
	}
	e.want(204, "DELETE", "/api/mcp/servers/open", "", "")
	e.want(404, "DELETE", "/api/mcp/servers/open", "", "")
	e.want(404, "GET", "/api/mcp/servers/open/tools", "", "")
}

func TestMCPIntegrations(t *testing.T) {
	env, err := exeenv.New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	discover := func(status int, body string) ([]mcpServerJSON, error) {
		integrations, err := listIntegrationsIn(t.Context(), env, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != env.ReflectionURL()+"/integrations" {
				t.Fatalf("unexpected reflection request: %s", req.URL)
			}
			return testHTTPResponse(status, body), nil
		})})
		return mcpIntegrationsOf(env, integrations), err
	}
	integrations, err := discover(200, `{"integrations":[
		{"name":"kagi-mcp","type":"catalog","attributes":["type:mcp"]},
		{"name":"remote","type":"catalog","team":true,"comment":"memes","attributes":["type:mcp"]},
		{"name":"taken","type":"catalog","attributes":["type:mcp"]},
		{"name":"other","type":"http-proxy"}
	]}`)
	if err != nil || len(integrations) != 3 {
		t.Fatalf("integrations: %+v, %v", integrations, err)
	}
	if ig := integrations[1]; ig.Name != "remote" || ig.URL != "https://remote.team.example.test/mcp" || ig.Description != "memes" || ig.Integration != "remote" || ig.LoginURL != "" {
		t.Fatalf("remote: %+v", ig)
	}
	if got, err := discover(403, ""); got != nil || err != nil {
		t.Fatalf("detached reflection: %+v, %v", got, err)
	}
	if _, err := discover(503, ""); err == nil {
		t.Fatal("reflection outage: no error")
	}

	registered := func(name, url string) mcpServerJSON {
		return mcpServerJSON{Server: mcp.Server{Name: name, URL: url}, Auth: "login_required", LoginURL: "/mcp/login/" + name}
	}
	merged := mergeMCPIntegrations([]mcpServerJSON{
		registered("kagi", "https://kagi-mcp.int.example.test/mcp?tools=search"),
		registered("taken", "https://taken.example.test/mcp"),
		registered("lookalike", "https://kagi-mcp.int.example.test.evil.test/mcp"),
	}, integrations)
	var names []string
	for _, s := range merged {
		names = append(names, s.Name+":"+s.Integration+":"+s.LoginURL)
	}
	want := []string{"kagi:kagi-mcp:", "taken::/mcp/login/taken", "lookalike::/mcp/login/lookalike", "remote:remote:"}
	if !slices.Equal(names, want) {
		t.Fatalf("merged:\n%q\nwant:\n%q", names, want)
	}
}

func TestMCPToolsAndCalls(t *testing.T) {
	e := newMCPEnv(t, "")
	f := mcptest.NewServer(t)
	e.add("t", f.URL)

	var tools struct {
		Server struct{ Name, Title, Version, Instructions string }
		Tools  []struct{ Name string }
	}
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/mcp/servers/t/tools", "", "")), &tools); err != nil {
		t.Fatal(err)
	}
	if s := tools.Server; s.Name != "mcptest" || s.Title != "MCP Test" || s.Version != "1.0" || s.Instructions != "Test fixture." || len(tools.Tools) != 7 {
		t.Fatalf("tools: %+v", tools)
	}
	if got := e.call("t", "", "echo", `{"text":"hi"}`); got != "hi" {
		t.Fatalf("echo: %q", got)
	}
	if got := e.call("t", "", "header", `{"name":"X-Test"}`); got != "yes" {
		t.Fatalf("static header: %q", got)
	}
	if got := e.call("t", "", "header", `{"name":"User-Agent"}`); got != version.UserAgent() {
		t.Fatalf("User-Agent: %q", got)
	}
	// A configured User-Agent wins.
	e.want(200, "PUT", "/api/mcp/servers/t", "", fmt.Sprintf(`{"url":%q,"headers":{"User-Agent":"custom"}}`, f.URL))
	if got := e.call("t", "", "header", `{"name":"User-Agent"}`); got != "custom" {
		t.Fatalf("configured User-Agent: %q", got)
	}
	if b := e.want(200, "POST", "/api/mcp/servers/t/call", "", `{"tool":"fail"}`); !strings.Contains(b, `"isError":true`) {
		t.Fatalf("fail: %s", b)
	}
	e.want(400, "POST", "/api/mcp/servers/t/call", "", `{"arguments":{}}`)
	e.want(404, "POST", "/api/mcp/servers/nope/call", "", `{"tool":"echo"}`)

	e.add("down", "http://127.0.0.1:1/mcp")
	if b := e.want(502, "GET", "/api/mcp/servers/down/tools", "", ""); !strings.Contains(b, `connecting to MCP server "down"`) {
		t.Fatalf("down: %s", b)
	}
	// Another origin would get the static headers too.
	moved := httptest.NewServer(http.RedirectHandler(f.URL, http.StatusTemporaryRedirect))
	t.Cleanup(moved.Close)
	e.add("moved", moved.URL)
	if b := e.want(502, "GET", "/api/mcp/servers/moved/tools", "", ""); !strings.Contains(b, "refusing redirect") {
		t.Fatalf("moved: %s", b)
	}
}

// Sessions are per conversation, and end with restart, edits, or the
// server forgetting them.
func TestMCPSessions(t *testing.T) {
	e := newMCPEnv(t, "")
	f := mcptest.NewServer(t)
	e.add("t", f.URL)
	session := func(scope string) string { return e.call("t", scope, "session", "{}") }

	a := session("a")
	if session("a") != a || session("b") == a || session("") == a {
		t.Fatal("sessions aren't per conversation")
	}
	e.want(204, "POST", "/api/mcp/servers/t/restart", "a", "")
	a2 := session("a")
	if a2 == a {
		t.Fatal("restart kept the session")
	}
	e.want(200, "PUT", "/api/mcp/servers/t", "", fmt.Sprintf(`{"url":%q,"headers":{"X-Test":"no"}}`, f.URL))
	if session("a") == a2 || e.call("t", "a", "header", `{"name":"X-Test"}`) != "no" {
		t.Fatal("an edit kept the session")
	}
	a3 := session("a")
	f.EndSessions()
	if b := e.want(502, "POST", "/api/mcp/servers/t/call", "a", `{"tool":"session"}`); !strings.Contains(b, "session not found") {
		t.Fatalf("forgotten session: %s", b)
	}
	a4 := session("a")
	if a4 == a3 {
		t.Fatal("kept a session the server forgot")
	}
	f.EndSessionsLikePython()
	e.want(502, "POST", "/api/mcp/servers/t/call", "a", `{"tool":"session"}`)
	if session("a") == a4 {
		t.Fatal("kept a session a Python server forgot")
	}
}

func TestMCPCallTimeout(t *testing.T) {
	e := newMCPEnv(t, "")
	f := mcptest.NewServer(t)
	e.add("t", f.URL)
	b := e.want(504, "POST", "/api/mcp/servers/t/call", "", `{"tool":"block","timeout_ms":50}`)
	if b != "calling t.block: timed out after 50ms; the tool may still have run\n" {
		t.Fatalf("timeout: %q", b)
	}
	if got := e.call("t", "", "echo", `{"text":"still"}`); got != "still" {
		t.Fatalf("after timeout: %q", got)
	}
}

// Restarting a session ends its calls in flight.
func TestMCPRestartDuringCall(t *testing.T) {
	e := newMCPEnv(t, "")
	f := mcptest.NewServer(t)
	e.add("t", f.URL)
	done := make(chan string)
	go func() {
		_, b := e.do("POST", "/api/mcp/servers/t/call", "a", `{"tool":"block"}`)
		done <- b
	}()
	<-f.Blocked
	if s := e.srv.mcp.Snapshot().Sessions; len(s) != 1 || s[0].InFlight != 1 {
		t.Fatalf("sessions: %+v", s)
	}
	e.want(204, "POST", "/api/mcp/servers/t/restart", "a", "")
	if b := <-done; b != "calling t.block: the session was closed (restarted)\n" {
		t.Fatalf("call: %q", b)
	}
}

// Conversations export the absolute path of the socket the server listens
// on, which differs from the requested one when another server holds it.
func TestShelleySocketEnv(t *testing.T) {
	dir, err := os.MkdirTemp("", "sock") // t.TempDir paths can exceed the socket path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Chdir(dir)
	other, err := net.Listen("unix", "shelley.sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })

	h := NewTestHarness(t)
	ln, err := h.server.listenSocket("shelley.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	want := filepath.Join(dir, "shelley-2.sock")
	h.NewConversation(`bash: printf %s "$SHELLEY_SOCKET"`, t.TempDir())
	if got := strings.TrimSpace(h.WaitToolResult()); got != want {
		t.Fatalf("SHELLEY_SOCKET=%q, want %q", got, want)
	}
}

func TestMCPNoticeProvenanceTag(t *testing.T) {
	raw, err := json.Marshal(mcpNoticeUserData{MCPServerChange: "added", ServerName: "linear", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	tag, attrs, err := provenanceTag(raw)
	if err != nil {
		t.Fatal(err)
	}
	if tag != "mcp_servers_changed" {
		t.Fatalf("tag = %q", tag)
	}
	got := map[string]string{}
	for _, a := range attrs {
		got[a.Name.Local] = a.Value
	}
	if got["server"] != "linear" || got["change"] != "added" {
		t.Fatalf("attrs = %v", got)
	}

	// The notice reaches the model wrapped, with its text escaped.
	msg, err := messageWithSenderProvenance(llm.UserStringMessage("added <linear>"), raw)
	if err != nil {
		t.Fatal(err)
	}
	text := msg.Content[0].Text
	if !strings.Contains(text, `<mcp_servers_changed server="linear" change="added">`) ||
		!strings.Contains(text, "&lt;linear&gt;") ||
		!strings.HasSuffix(text, "</mcp_servers_changed>") {
		t.Fatalf("wrapped text = %q", text)
	}
}

// TestMCPNoticeInjectedWhileWorking verifies that adding an MCP server while an
// agent is working injects a notice the model sees, wrapped in
// <mcp_servers_changed>.
func TestMCPNoticeInjectedWhileWorking(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server, database, _ := newTestServer(t)
	held := newHeldLLMService()
	server.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(server) })

	slug := "mcp-notice"
	conversation, err := database.CreateConversation(t.Context(), &slug, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Start a turn; the held service blocks the first call, so the agent is
	// working when we add the server.
	w := httptest.NewRecorder()
	server.handleChatConversation(w, httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"message":"start","model":"predictable"}`)), conversation.ConversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("chat status = %d: %s", w.Code, w.Body.String())
	}
	first := held.waitCall(t, "start")

	// Add a server while the agent is working.
	addW := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/servers",
		strings.NewReader(`{"name":"linear","url":"https://example.com/mcp","description":"issues"}`))
	server.handleCreateMCPServer(addW, req)
	if addW.Code != http.StatusOK {
		t.Fatalf("create status = %d: %s", addW.Code, addW.Body.String())
	}

	// handleCreateMCPServer notifies active conversations from a background
	// goroutine, so the notice is queued asynchronously. Wait until it is
	// actually queued before releasing the first call. Otherwise the turn can
	// end (the "start" response is final text, no tool round) before the
	// goroutine runs its IsAgentWorking check, which skips the notice entirely
	// and makes this test flaky (issue #345). While first is held the turn stays
	// working, so the injected notice sits in the queue until we release it.
	waitFor(t, 5*time.Second, func() bool {
		queued, err := database.GetQueuedMessages(t.Context(), conversation.ConversationID)
		if err != nil {
			return false
		}
		for _, qm := range queued {
			var n mcpNoticeUserData
			if json.Unmarshal(qm.UserData, &n) == nil && n.MCPServerChange == "added" && n.ServerName == "linear" {
				return true
			}
		}
		return false
	})

	// Let the turn run; the injected notice reaches a later model request.
	first.Release()
	deadline := time.After(5 * time.Second)
	for {
		held.mu.Lock()
		var found bool
		for _, calls := range held.calls {
			for _, c := range calls {
				if requestHasText(c.request, `<mcp_servers_changed server="linear" change="added">`) {
					found = true
				}
				c.Release() // keep the turn progressing so the drained notice runs
			}
		}
		changed := held.changed
		held.mu.Unlock()
		if found {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("notice did not reach the model")
		}
	}
}
