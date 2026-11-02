package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/mcp/mcptest"
	"shelley.exe.dev/version"
)

const requiredHeader = "X-Exedev-Userid"

// newOAuthEnv is a Shelley that requires requiredHeader, with "linear", an
// OAuth-protected MCP server, registered.
func newOAuthEnv(t *testing.T) (*mcpEnv, *mcptest.AuthServer) {
	e := newMCPEnv(t, requiredHeader)
	as := mcptest.NewAuthServer(t)
	e.add("linear", as.MCP.URL+"/mcp")
	return e, as
}

// navigate sends a browser's GET for path, with headers, and returns the
// response, without following redirects.
func (e *mcpEnv) navigate(rawURL string, headers ...string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set(requiredHeader, "user")
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// browse follows a browser through the login link, the authorization
// server, and the callback, and returns the query of Shelley's final
// redirect to "/".
func (e *mcpEnv) browse(path string, headers ...string) url.Values {
	e.t.Helper()
	u := e.ts.URL + path
	for range 5 {
		resp := e.navigate(u, headers...)
		loc, err := resp.Location()
		if err != nil {
			e.t.Fatalf("GET %s: %d without a redirect", u, resp.StatusCode)
		}
		if loc.Path == "/" {
			return loc.Query()
		}
		u = loc.String()
	}
	e.t.Fatal("too many redirects")
	return nil
}

func (e *mcpEnv) login() {
	e.t.Helper()
	if q := e.browse("/mcp/login/linear"); q.Encode() != "mcp_login=linear" {
		e.t.Fatalf("login: %v", q)
	}
}

func (e *mcpEnv) wantAuth(want string) {
	e.t.Helper()
	var servers []struct{ Name, Auth string }
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/mcp/servers", "", "")), &servers); err != nil {
		e.t.Fatal(err)
	}
	for _, s := range servers {
		if s.Name == "linear" && s.Auth != want {
			e.t.Fatalf("auth %q, want %q", s.Auth, want)
		}
	}
}

func (e *mcpEnv) wantLoginRequired() {
	e.t.Helper()
	want := fmt.Sprintf("MCP server \"linear\" needs you to log in: open %s\n", e.srv.mcpLoginURL("linear"))
	if b := e.want(401, "POST", "/api/mcp/servers/linear/call", "", `{"tool":"echo"}`); b != want {
		e.t.Fatalf("call: %q, want %q", b, want)
	}
	e.wantAuth("login_required")
}

func (e *mcpEnv) wantEvent(kind string) {
	e.t.Helper()
	for _, ev := range e.srv.mcp.Snapshot().Events {
		if ev.Kind == kind {
			return
		}
	}
	e.t.Fatalf("no %s event", kind)
}

// restart replaces the Shelley server with a new one on the same database.
func (e *mcpEnv) restart() {
	e.srv.CloseMCP()
	e.srv = NewServer(e.db, &testLLMManager{service: predictable.NewService()}, claudetool.ToolSetConfig{},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})), true, "predictable", e.header)
	e.srv.hooksDir = e.t.TempDir()
	mux := http.NewServeMux()
	e.srv.RegisterRoutes(mux)
	e.ts.Config.Handler = e.srv.tcpHandler(mux)
	e.t.Cleanup(e.srv.CloseMCP)
}

func TestMCPOAuthLogin(t *testing.T) {
	e, as := newOAuthEnv(t)
	e.wantAuth("")
	e.wantLoginRequired()
	e.login()
	e.wantAuth("logged_in")
	if got := e.call("linear", "c1", "echo", `{"text":"hi"}`); got != "hi" {
		t.Fatalf("call: %q", got)
	}
	e.wantEvent("login_completed")
	e.restart()
	if got := e.call("linear", "c2", "echo", `{"text":"again"}`); got != "again" {
		t.Fatalf("call after a restart: %q", got)
	}
	if as.Stats().Registrations != 1 {
		t.Fatalf("stats %+v", as.Stats())
	}
	// Logging in again starts new sessions, with the new login.
	session := e.call("linear", "c2", "session", "{}")
	e.login()
	if e.call("linear", "c2", "session", "{}") == session {
		t.Fatal("kept the session")
	}
}

// Visiting the link again while a login is pending (a double click, a
// link preview) sends the browser to the same authorization URL, so
// whichever visit the user completes logs in.
func TestMCPOAuthLoginLink(t *testing.T) {
	e, as := newOAuthEnv(t)
	if code, _ := e.do("HEAD", "/mcp/login/linear", "", ""); code != http.StatusOK || as.Stats().Registrations != 0 {
		t.Fatalf("HEAD: %d, %+v", code, as.Stats())
	}
	first := e.navigate(e.ts.URL + "/mcp/login/linear")
	if first.Header.Get("Cache-Control") != "no-store" || first.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("headers %v", first.Header)
	}
	authURL := first.Header.Get("Location")
	if !strings.HasPrefix(authURL, as.URL+"/authorize?") || as.Stats().Registrations != 1 {
		t.Fatalf("authorization URL %q, %+v", authURL, as.Stats())
	}
	if again := e.navigate(e.ts.URL + "/mcp/login/linear").Header.Get("Location"); again != authURL {
		t.Fatalf("second visit: %q", again)
	}
	callback := e.navigate(authURL).Header.Get("Location")
	if loc := e.navigate(callback).Header.Get("Location"); loc != "/?mcp_login=linear" {
		t.Fatalf("callback: %q", loc)
	}
	e.wantAuth("logged_in")
	want := "This login expired or was already used. Start it again from the MCP Servers dialog or the agent's link."
	if loc, _ := e.navigate(callback).Location(); loc.RawQuery != (url.Values{"mcp_error": {want}}).Encode() {
		t.Fatalf("callback again: %q", loc)
	}
	// A visit through another host can't reuse a pending login: its
	// redirect URI leads back to the first host.
	a := e.navigate(e.ts.URL + "/mcp/login/linear").Header.Get("Location")
	b := e.navigate(e.ts.URL+"/mcp/login/linear", "X-Forwarded-Host", "vm.exe.xyz").Header.Get("Location")
	if a == b || !strings.Contains(b, url.Values{"redirect_uri": {"http://vm.exe.xyz/mcp/oauth/callback"}}.Encode()) {
		t.Fatalf("visits through two hosts: %q, %q", a, b)
	}

	e.header = ""
	for _, path := range []string{"/mcp/login/linear", "/mcp/oauth/callback", "/debug/mcp"} {
		if code, _ := e.do("GET", path, "", ""); code != http.StatusForbidden {
			t.Errorf("%s without %s: %d", path, requiredHeader, code)
		}
	}
}

func TestMCPOAuthRegistrationRefused(t *testing.T) {
	e, as := newOAuthEnv(t)
	as.RefuseRegistration()
	q := e.browse("/mcp/login/linear", "X-Forwarded-Proto", "https", "X-Forwarded-Host", "vm.exe.xyz, proxy")
	want := "logging in to linear: the authorization server refused to register Shelley as a client (403 Forbidden). " +
		"Some servers only accept clients they have approved. The redirect URI was https://vm.exe.xyz/mcp/oauth/callback."
	if q.Get("mcp_login") != "linear" || q.Get("mcp_error") != want {
		t.Fatalf("got %v", q)
	}
	e.wantEvent("login_failed")
}

// Expired tokens are refreshed, and the new tokens saved; a refresh the
// authorization server refuses means logging in again.
func TestMCPOAuthRefresh(t *testing.T) {
	e, as := newOAuthEnv(t)
	as.SetExpiresIn(1) // within oauth2's expiry margin: expired at once
	e.login()
	e.call("linear", "", "echo", `{"text":"a"}`)
	e.restart() // with the token saved after the refresh
	e.call("linear", "", "echo", `{"text":"b"}`)
	if n := as.Stats().Refreshes; n < 2 {
		t.Fatalf("%d refreshes", n)
	}
	e.wantEvent("token_refreshed")
	// Logging in, refreshing, and calling tools all identify as Shelley.
	if uas := as.Stats().UserAgents; !slices.Equal(uas, []string{version.UserAgent()}) {
		t.Fatalf("User-Agents %q", uas)
	}
	as.RevokeRefreshTokens()
	e.wantLoginRequired()
	e.wantEvent("refresh_failed")
}

// Servers that don't say when tokens expire reject them with a 401; that
// forces a refresh, unless the server rejects new tokens too.
func TestMCPOAuthUnauthorized(t *testing.T) {
	e, as := newOAuthEnv(t)
	as.SetExpiresIn(0)
	e.login()
	e.call("linear", "", "echo", `{"text":"a"}`)
	as.RevokeAccessTokens()
	if got := e.call("linear", "", "echo", `{"text":"b"}`); got != "b" || as.Stats().Refreshes != 1 {
		t.Fatalf("call %q, %+v", got, as.Stats())
	}
	as.RejectTokens()
	e.wantLoginRequired()
}

// Logging out, changing the URL, or adding an Authorization header drop
// the login.
func TestMCPOAuthLogout(t *testing.T) {
	e, as := newOAuthEnv(t)
	e.login()
	e.want(204, "POST", "/api/mcp/servers/linear/logout", "", "")
	e.wantAuth("")
	e.wantLoginRequired()
	e.wantEvent("logged_out")

	put := func(body string) { e.want(200, "PUT", "/api/mcp/servers/linear", "", body) }
	e.login()
	// Editing only the path or query keeps the login: its origin is unchanged.
	put(fmt.Sprintf(`{"url":%q}`, as.MCP.URL+"/mcp?tools=all"))
	e.wantAuth("logged_in")
	put(fmt.Sprintf(`{"url":%q}`, as.MCP.URL+"/mcp"))
	e.wantAuth("logged_in")
	// Changing the host drops it: the login was for the old origin.
	put(fmt.Sprintf(`{"url":%q}`, mcptest.NewServer(t).URL))
	e.wantAuth("")
	put(fmt.Sprintf(`{"url":%q}`, as.MCP.URL+"/mcp"))
	e.login()
	put(fmt.Sprintf(`{"url":%q,"headers":{"Authorization":"Bearer x"}}`, as.MCP.URL+"/mcp"))
	put(fmt.Sprintf(`{"url":%q}`, as.MCP.URL+"/mcp"))
	e.wantAuth("")
	e.wantLoginRequired()
}

// /debug/mcp shows servers, logins, sessions and events, but no secrets,
// arguments or results.
func TestDebugMCP(t *testing.T) {
	e, _ := newOAuthEnv(t)
	e.want(200, "POST", "/api/mcp/servers", "", `{"name":"keyed","url":"http://127.0.0.1:1","headers":{"Authorization":"Bearer s3cr3t"}}`)
	e.login()
	e.call("linear", "conv1", "echo", `{"text":"the-argument"}`)
	var row generated.McpOauth
	if err := e.db.Queries(t.Context(), func(q *generated.Queries) (err error) {
		row, err = q.GetMCPOAuth(t.Context(), "linear")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var tok oauth2.Token
	if err := json.Unmarshal([]byte(row.Token), &tok); err != nil {
		t.Fatal(err)
	}
	page := e.want(200, "GET", "/debug/mcp", "", "")
	for _, want := range []string{"keyed", "Authorization", "linear", "X-Test", "logged_in", "conv1", "connected", "login_completed"} {
		if !strings.Contains(page, want) {
			t.Errorf("no %q", want)
		}
	}
	for _, secret := range []string{"s3cr3t", "the-argument", tok.AccessToken, tok.RefreshToken} {
		if strings.Contains(page, secret) {
			t.Errorf("shows %q", secret)
		}
	}
}
