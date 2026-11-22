package test

import (
	"maps"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/mcp-servers.spec.ts.

// mcpServersList is the fake server list: an integration-managed server, one
// with a static key, one that needs an OAuth login, and one logged in.
func mcpServersList() []map[string]any {
	return []map[string]any{
		{
			"name":        "kagi",
			"url":         "https://kagi-mcp.int.exe.xyz/mcp",
			"description": "Integration-managed authentication",
			"headers":     map[string]any{},
			"auth":        "",
			"integration": "kagi-mcp",
		},
		{
			"name":        "keyed",
			"url":         "https://keyed.example.test/mcp",
			"description": "Static API key",
			"headers":     map[string]any{"Authorization": "Bearer key"},
			"auth":        "",
		},
		{
			"name":        "oauth",
			"url":         "https://oauth.example.test/mcp",
			"description": "Direct OAuth",
			"headers":     map[string]any{},
			"auth":        "login_required",
			"login_url":   "/mcp/login/oauth",
		},
		{
			"name":        "logged-in",
			"url":         "https://logged-in.example.test/mcp",
			"description": "Direct OAuth with a stored login",
			"headers":     map[string]any{},
			"auth":        "logged_in",
			"login_url":   "/mcp/login/logged-in",
		},
	}
}

// mcpServersWith is server i of the list with fields replaced.
func mcpServersWith(i int, fields map[string]any) map[string]any {
	m := mcpServersList()[i]
	maps.Copy(m, fields)
	return m
}

// mcpServersOpen says how a description opens the MCP servers panel.
const mcpServersOpen = `The screen is the default 393x727 phone. Name elements by ARIA role and accessible name, as Playwright's getByRole does; "named X" means the name is exactly X. To "open the panel": navigate to /new, press Control+k, fill ".command-palette-input" with "MCP Servers", and click the first ".command-palette-item"; "the panel", the dialog named "MCP Servers" (the element [role="dialog"]:has(.modal-xwide)), should be visible. `

func TestE2EMcpServersIntegrationManagedNoControls(t *testing.T) {
	e2e(t, useful(0), `Integration-managed MCP servers have no login, edit, or delete controls. `+mcpServersOpen+`The server lists four MCP servers: "kagi" (managed by the integration kagi-mcp), "keyed" (a static API key), "oauth" (needs a login at /mcp/login/oauth) and "logged-in" (logged in). Perform the steps in order. 1. Open the panel. 2. For each of "kagi" and "keyed": the row (role "row") in the panel that holds an element whose text is exactly that name should be visible, and it must hold no link named "Sign in" (count 0). 3. In the panel, the link named "Sign in" (exactly one) should have the attribute href="/mcp/login/oauth". 4. The panel's button named "Log out" should be visible. 5. The panel must have no button named "Delete kagi" (count 0) and no button named "Edit kagi" (count 0). 6. Its buttons named "Delete keyed" and "Delete oauth" should be visible.`, func(s *shelley) {
		s.route("/api/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, mcpServersList())
		})
	})
}

func TestE2EMcpServersRefreshClearsErrors(t *testing.T) {
	e2e(t, useful(0), `Refreshing the MCP servers panel clears errors and reloads the servers with a loading state. `+mcpServersOpen+`At first the server fails to list MCP servers (502, "Integration discovery failed"). Call "hold" makes later listings wait; call "release" lets them answer the servers "kagi" and "keyed". "The refresh button" is the panel's button named "Refresh". Perform the steps in order. 1. Open the panel. 2. The panel's element with role "alert" should contain the text "Integration discovery failed". 3. Call "hold", then click the refresh button. 4. The refresh button should be disabled, the panel should have no element with role "alert" (count 0), and the panel's ".models-loading" should be visible. 5. Call "release". 6. The element in the panel whose text is exactly "kagi" should be visible, and the refresh button should be enabled. 7. There must be no column header (role "columnheader") named "Login" in the panel (count 0).`, func(s *shelley) {
		var held atomic.Bool
		release := s.gate("release")
		s.call("hold", func() string {
			held.Store(true)
			return "holding"
		})
		s.route("/api/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
			if !held.Load() {
				http.Error(w, "Integration discovery failed", http.StatusBadGateway)
				return
			}
			if !release.wait(r) {
				return
			}
			writeJSON(w, mcpServersList()[:2])
		})
	})
}

func TestE2EMcpServersRefreshDropsPendingDeletion(t *testing.T) {
	e2e(t, useful(0), `Refreshing the MCP servers panel removes a pending deletion when an integration takes ownership of the server. `+mcpServersOpen+`The server lists one MCP server, "kagi", not managed by an integration; call "manage" makes the integration kagi-mcp manage it from then on. Perform the steps in order. 1. Open the panel. 2. Click the panel's button named "Delete kagi": its button named "Delete" should be visible. 3. Call "manage", then click the panel's button named "Refresh". 4. The element in the panel whose text is exactly "kagi" should be visible, and the panel must have no button whose name starts with "Delete" (count 0).`, func(s *shelley) {
		var managed atomic.Bool
		s.call("manage", func() string {
			managed.Store(true)
			return "managed"
		})
		s.route("/api/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
			server := mcpServersWith(0, nil)
			if !managed.Load() {
				delete(server, "integration")
			}
			writeJSON(w, []any{server})
		})
	})
}

func TestE2EMcpServersPublicNeedsNoLogin(t *testing.T) {
	e2e(t, useful(0), `Public MCP servers need no manual connection or login. `+mcpServersOpen+`The server lists one MCP server, "oauth", which needs no authentication, and has no tools. Call "probes" returns how many times the page has asked for its tools. Perform the steps in order. 1. Open the panel. 2. The panel must have no link named "Sign in" (count 0), no column header (role "columnheader") named "Login" (count 0), and no button named "Connect" (count 0). Its button named "Tools" should be visible. 3. Call "probes": it must return "0". 4. Click the panel's button named "Tools". The dialog named "oauth tools" should contain the text "This server has no tools.", and call "probes" must return "1".`, func(s *shelley) {
		s.route("/api/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []any{mcpServersWith(2, map[string]any{"auth": ""})})
		})
		probes := s.counter("probes")
		s.route("/api/mcp/servers/oauth/tools", func(w http.ResponseWriter, r *http.Request) {
			probes.Add(1)
			writeJSON(w, map[string]any{"tools": []any{}})
		})
	})
}

func TestE2EMcpServersSignInAfterChallenge(t *testing.T) {
	e2e(t, useful(0), `MCP sign-in appears only after an authentication challenge. `+mcpServersOpen+`The server lists one MCP server, "oauth", with no authentication state yet; call "challenge" makes it report that it needs a login at /mcp/login/oauth. Perform the steps in order. 1. Open the panel. 2. The panel must have no link named "Sign in" (count 0). 3. Call "challenge", then click the panel's button named "Refresh". 4. The panel's link named "Sign in" should have the attribute href="/mcp/login/oauth".`, func(s *shelley) {
		var challenged atomic.Bool
		s.call("challenge", func() string {
			challenged.Store(true)
			return "challenged"
		})
		s.route("/api/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
			auth := ""
			if challenged.Load() {
				auth = "login_required"
			}
			writeJSON(w, []any{mcpServersWith(2, map[string]any{"auth": auth})})
		})
	})
}

// mcpServersToolsRetry is the test that an authentication action shows in
// the tools dialog and that tools load again on returning to the page.
func mcpServersToolsRetry(t *testing.T, useful lazycue.Useful, index int, label, href string) {
	server := mcpServersWith(index, map[string]any{"auth": ""})
	name := server["name"].(string)
	e2e(t, useful, `In the MCP tools dialog, "`+label+`" is actionable, and tools load again when the page regains focus after authentication. `+mcpServersOpen+`The server lists one MCP server, "`+name+`". Asking for its tools fails with 401 "MCP server needs authentication: /mcp/login/`+name+`" until call "authenticate"; after that it answers one tool, "ready_tool". Call "probes" returns how many times the page has asked for its tools. "The tools dialog" is the dialog named "`+name+` tools" (the element [role="dialog"]:has(.modal-wide)); "the action" is its link named "`+label+`". Perform the steps in order. 1. Open the panel and click its button named "Tools". 2. The action should have the attribute href="`+href+`" and the attribute target="_blank". 3. The tools dialog should have no element with role "alert" (count 0), and must not contain the text "/mcp/login/". 4. Call "probes": it must return "1". 5. Call "authenticate", then eval window.dispatchEvent(new Event("focus")). 6. The element in the tools dialog whose text is exactly "ready_tool" should be visible, and there should be no action (count 0). 7. Call "probes": it must return "2".`, func(s *shelley) {
		s.route("/api/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []any{server})
		})
		var authenticated atomic.Bool
		s.call("authenticate", func() string {
			authenticated.Store(true)
			return "authenticated"
		})
		probes := s.counter("probes")
		s.route("/api/mcp/servers/"+name+"/tools", func(w http.ResponseWriter, r *http.Request) {
			probes.Add(1)
			if !authenticated.Load() {
				http.Error(w, "MCP server needs authentication: /mcp/login/"+name, http.StatusUnauthorized)
				return
			}
			writeJSON(w, map[string]any{"tools": []any{map[string]any{"name": "ready_tool", "inputSchema": map[string]any{"properties": map[string]any{}}}}})
		})
	})
}

func TestE2EMcpServersManageOnExeDevRetriesTools(t *testing.T) {
	mcpServersToolsRetry(t, useful(0), 0, "Manage on exe.dev", "https://exe.dev/integrations")
}

func TestE2EMcpServersSignInRetriesTools(t *testing.T) {
	mcpServersToolsRetry(t, useful(0), 2, "Sign in", "/mcp/login/oauth")
}

// mcpServersToolsError is the test that a tools error with no known
// authentication action shows as it came.
func mcpServersToolsError(t *testing.T, useful lazycue.Useful, status, index int) {
	server := mcpServersList()[index]
	name := server["name"].(string)
	e2e(t, useful, `The MCP tools dialog shows an HTTP error that has no known authentication action as it came. `+mcpServersOpen+`The server lists one MCP server, "`+name+`"; asking for its tools fails with HTTP `+strconv.Itoa(status)+` "Remote server failed". "The tools dialog" is the dialog named "`+name+` tools" (the element [role="dialog"]:has(.modal-wide)). Perform the steps in order. 1. Open the panel and click its button named "Tools". 2. The tools dialog's element with role "alert" should contain the text "Remote server failed". 3. The tools dialog must have no link at all (role "link", count 0).`, func(s *shelley) {
		s.route("/api/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []any{server})
		})
		s.route("/api/mcp/servers/"+name+"/tools", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Remote server failed", status)
		})
	})
}

func TestE2EMcpServersToolsKeep502(t *testing.T) { mcpServersToolsError(t, useful(0), 502, 2) }

func TestE2EMcpServersToolsKeep401(t *testing.T) { mcpServersToolsError(t, useful(0), 401, 1) }
