package test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

// Ported from ui/e2e/vm-settings.spec.ts.

// The prod tab's VM view: a Settings tab, first, and a Page tab, whose
// changes go through Shelley's typed API. exe.dev itself is faked: no real
// VM changes.

// vmSettingsSetup turns on the VM views and fakes the VM "web" and its page.
func vmSettingsSetup(s *shelley, tags []string) {
	s.flag("workspace", true)
	s.flag("dev-prod-tabs", true)
	vm := map[string]any{
		"name":        "web",
		"emoji":       "🌐",
		"status":      "running",
		"url":         "https://web.exe.test",
		"ssh_command": "ssh web.exe.test",
		"public":      false,
		"comment":     "",
		"tags":        tags,
		"role":        "",
		"integration": "acme-exe",
	}
	s.route("/api/exe-vms", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"vms": []any{vm}})
	})
	s.route("/api/exe-vms/web/frame", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"blocked": ""})
	})
	s.host("https://web.exe.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("the web page"))
	}))
}

func TestE2EVmSettingsTabAndConfirmedChanges(t *testing.T) {
	e2e(t, useful(0), `A VM opens on its Settings tab, beside its Page tab, and settings changes confirm first. Workspace mode and the VM tabs are on; the screen is the default 393x727 phone. exe.dev is faked: the VM "web" (https://web.exe.test) is running in Los Angeles, and its integration key may not add domains ("Its key reaches only VMs tagged scope."). Call "changes" returns, as a JSON list, the settings changes the page has sent the server so far; call "change count" returns how many. Name elements by ARIA role and accessible name, as Playwright's getByRole does. "The tabs" are the buttons inside the navigation element named "VM tabs". Elements named by a test id are those with that data-testid. Perform the steps in order. 1. Navigate to /vm/web. "view-tab-settings" should have the attribute aria-current="page", and the first of the tabs should have data-testid exactly "view-tab-settings". 2. "vm-status" should contain the text "running", "vm-denied" should contain "tagged scope", and "vm-domain-add" should be disabled. 3. Click "vm-comment-edit" and fill "vm-change-value" with "staging". Call "changes": it must return [] (nothing changes before confirming). 4. Click "vm-change-confirm". "vm-comment" should have exactly the text "staging", and call "changes" must return [{"kind":"comment","value":"staging"}]. 5. Making it public, from the Share dialog, warns first: click "vm-share", then "vm-visibility-toggle". The text "Anyone on the internet can reach https://web.exe.test" should be visible. Press Escape twice. Call "change count": it must return "1". 6. Delete wants the VM's name typed: click "vm-delete" and fill "vm-change-typed" with "we": "vm-change-confirm" should be disabled. Fill "vm-change-typed" with "web": "vm-change-confirm" should be enabled. Press Escape. Call "change count": it must return "1". 7. Click "view-tab-page": the URL path should become exactly /vm/web/page, and "vm-frame" should be visible. 8. Click "view-tab-settings": the URL path should become exactly /vm/web, and "vm-status" should be visible.`, func(s *shelley) {
		vmSettingsSetup(s, []string{"scope"})
		var mu sync.Mutex
		settings := map[string]any{
			"vm_name":               "web",
			"emoji":                 "🌐",
			"status":                "running",
			"region_display":        "Los Angeles, USA",
			"image":                 "exeuntu",
			"pool_name":             "",
			"created_at":            "2026-11-01T00:00:00Z",
			"comment":               "",
			"tags":                  []string{"scope"},
			"https_url":             "https://web.exe.test",
			"ssh_command":           "ssh web.exe.test",
			"shelley_url":           "",
			"terminal_url":          "",
			"allocated_cpus":        2,
			"memory_capacity_bytes": 8 << 30,
			"disk_capacity_bytes":   25 << 30,
			"proxy_port":            8000,
			"proxy_share":           "private",
			"sharing":               map[string]any{"named_user_shares": nil, "team_shared": false},
			"share_links":           nil,
			"domains":               nil,
			"egress_policy":         "all",
			"email_receive_enabled": false,
			"email_address":         "",
			"integration":           "acme-exe",
			"scope_tag":             "scope",
			"role":                  "",
			"denied":                map[string]any{"domain-add": "Its key reaches only VMs tagged scope."},
		}
		changes := []any{}
		s.route("/api/exe-vms/web/settings", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if r.Method == "POST" {
				change := object(readBody(r))
				changes = append(changes, change)
				settings["comment"] = change["value"]
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeJSON(w, settings)
		})
		s.call("changes", func() string {
			mu.Lock()
			defer mu.Unlock()
			return mustJSON(changes)
		})
		s.call("change count", func() string {
			mu.Lock()
			defer mu.Unlock()
			return mustJSON(len(changes))
		})
	})
}

// vmSettingsTerminalMessage is a message the page sent a terminal session.
type vmSettingsTerminalMessage struct {
	Session string
	Type    string `json:"type"`
	Cols    int    `json:"cols"`
	Data    string `json:"data"`
}

func TestE2EVmSettingsTerminalSessions(t *testing.T) {
	e2e(t, useful(0), `A VM's Terminal tab attaches to its web terminal sessions, until the VM's view closes. Workspace mode and the VM tabs are on; the screen is the default 393x727 phone. exe.dev is faked: the VM "web" is running, its settings fail to load, and it lists the web terminal sessions "ios-1" and "coral" (closing one ends it). The fake terminal server answers each session's "init" message with the output "$ ". Calls: "open" returns the sessions with a terminal connection open now, as a JSON list sorted alphabetically except that the session the page created (once there is one) comes last; "first message" returns the type, session and whether cols is over 20 of the first message any session got, as JSON ("none" before one); "inits" returns the sessions that got an "init" message, in order, as a JSON list; "coral got ls" and "coral got pwd" say whether the input data the page sent session coral, joined, contains "ls" (resp. "pwd"), "true" or "false"; "new session" returns "ok" once a session other than ios-1 and coral has connected, and makes {{MINTED}} its name; "relist" makes the fake list the sessions ios-1, coral and {{MINTED}}; "closed" returns the sessions the page has ended, as a JSON list. Name elements by ARIA role and accessible name, as Playwright's getByRole does. "The tabs" are the buttons inside the navigation element named "VM tabs"; "the session tabs" are the elements with data-testid "vm-terminal-session"; "the terminals" are the elements with data-testid "vm-terminal". Keys are typed key by key into the focused element. Perform the steps in order. 1. Navigate to /vm/web/page. There should be exactly 3 tabs, and the last should have data-testid exactly "view-tab-terminal". Unopened, there is no shell: there are no terminals (count 0). 2. Click data-testid "view-tab-terminal": the URL path should become exactly /vm/web/terminal, and the element with data-testid "vm-terminal" should be visible. 3. The session tabs' texts should be exactly "ios-1", "coral", in that order, and the last should have aria-selected="true" (the newest is attached). 4. Call "first message" until it returns {"cols_over_20":true,"session":"coral","type":"init"}. 5. Type "ls"; call "coral got ls" until it returns "true". 6. Picking another session attaches to it too, showing only it: click the first session tab. Call "open" until it returns ["coral","ios-1"]. There should be exactly 2 terminals, and exactly 1 of them visible. 7. Switching back reuses the first connection: click the second session tab, type "pwd", and call "coral got pwd" until it returns "true". Call "inits": it must return ["coral","ios-1"]. 8. A new session gets a color name, as the web terminal's do: click data-testid "vm-terminal-new". There should be exactly 3 session tabs, and the last one's trimmed text must match /^[a-z]+$/. Call "new session" until it returns "ok"; the last session tab's trimmed text must be exactly "{{MINTED}}". Call "open" until it returns ["coral","ios-1","{{MINTED}}"]. 9. Another tab keeps the sessions attached; leaving the VM detaches them: click data-testid "view-tab-page". No terminal should be visible (visible count 0), and call "open" must return ["coral","ios-1","{{MINTED}}"]. Navigate to /: call "open" until it returns []. 10. Coming back attaches to the session last attached, while it lasts: call "relist", then navigate to /vm/web/terminal. The last session tab should have aria-selected="true", and call "open" should return ["{{MINTED}}"]. 11. Closing it ends it and attaches to the newest left: click the last element with data-testid "vm-terminal-close". The session tabs' texts should be exactly "ios-1", "coral"; call "closed" must return ["{{MINTED}}"]; call "open" should return ["coral"].`, func(s *shelley) {
		vmSettingsSetup(s, []string{})
		s.route("/api/exe-vms/web/settings", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		})
		var mu sync.Mutex
		live := []string{"ios-1", "coral"}
		closed := []string{}
		var messages []vmSettingsTerminalMessage
		var open []string
		minted := func() string {
			for _, o := range open {
				if o != "ios-1" && o != "coral" {
					return o
				}
			}
			return ""
		}
		s.route("/api/exe-vms/web/terminal-sessions", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			writeJSON(w, map[string]any{"sessions": live})
		})
		s.route("/api/exe-vms/web/terminal-sessions/{session}", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			session := r.PathValue("session")
			if r.Method != "DELETE" {
				closed = append(closed, r.Method+" "+session)
			} else {
				closed = append(closed, session)
			}
			live = slices.DeleteFunc(live, func(l string) bool { return l == session })
			w.WriteHeader(http.StatusNoContent)
		})
		s.route("/api/exe-vms/web/terminal", func(w http.ResponseWriter, r *http.Request) {
			session := r.URL.Query().Get("session")
			c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			defer c.CloseNow()
			mu.Lock()
			open = append(open, session)
			mu.Unlock()
			defer func() {
				mu.Lock()
				defer mu.Unlock()
				if i := slices.Index(open, session); i >= 0 {
					open = slices.Delete(open, i, i+1)
				}
			}()
			for {
				_, raw, err := c.Read(r.Context())
				if err != nil {
					return
				}
				msg := vmSettingsTerminalMessage{Session: session}
				json.Unmarshal(raw, &msg)
				mu.Lock()
				messages = append(messages, msg)
				mu.Unlock()
				if msg.Type == "init" {
					out := mustJSON(map[string]string{"type": "output", "data": base64.StdEncoding.EncodeToString([]byte("$ "))})
					if c.Write(r.Context(), websocket.MessageText, []byte(out)) != nil {
						return
					}
				}
			}
		})
		s.call("open", func() string {
			mu.Lock()
			defer mu.Unlock()
			m := minted()
			got := slices.DeleteFunc(slices.Clone(open), func(o string) bool { return o == m })
			slices.Sort(got)
			if m != "" {
				got = append(got, m)
			}
			return mustJSON(got)
		})
		s.call("first message", func() string {
			mu.Lock()
			defer mu.Unlock()
			if len(messages) == 0 {
				return "none"
			}
			m := messages[0]
			return mustJSON(map[string]any{"type": m.Type, "session": m.Session, "cols_over_20": m.Cols > 20})
		})
		s.call("inits", func() string {
			mu.Lock()
			defer mu.Unlock()
			got := []string{}
			for _, m := range messages {
				if m.Type == "init" {
					got = append(got, m.Session)
				}
			}
			return mustJSON(got)
		})
		input := func(want string) func() string {
			return func() string {
				mu.Lock()
				defer mu.Unlock()
				var sb strings.Builder
				for _, m := range messages {
					if m.Session == "coral" {
						sb.WriteString(m.Data)
					}
				}
				return mustJSON(strings.Contains(sb.String(), want))
			}
		}
		s.call("coral got ls", input("ls"))
		s.call("coral got pwd", input("pwd"))
		s.call("new session", func() string {
			mu.Lock()
			defer mu.Unlock()
			m := minted()
			if m == "" {
				return "none yet"
			}
			s.v("MINTED", m)
			return "ok"
		})
		s.call("relist", func() string {
			mu.Lock()
			defer mu.Unlock()
			live = []string{"ios-1", "coral", s.vars["MINTED"]}
			return "relisted"
		})
		s.call("closed", func() string {
			mu.Lock()
			defer mu.Unlock()
			return mustJSON(closed)
		})
	})
}
