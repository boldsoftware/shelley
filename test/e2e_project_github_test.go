package test

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Ported from ui/e2e/project-github.spec.ts, which ran on a Pixel 5. The
// exe.dev account is that of an exe-dev integration with full control, seen
// through this server's exe.dev API, which the test mocks.

const projectGithubSetupURL = "https://exe.dev/github/setup"

// projectGithubRepos is the spec's manyRepos: acme/web and 90 more of
// acme's, and 30 of ann's.
func projectGithubRepos() []map[string]any {
	repos := func(owner string, n int) []map[string]any {
		var out []map[string]any
		for i := range n {
			out = append(out, map[string]any{"full_name": fmt.Sprintf("%s/repo-%03d", owner, i), "private": i%3 == 0})
		}
		return out
	}
	acme := append([]map[string]any{{"full_name": "acme/web", "private": true}}, repos("acme", 90)...)
	return []map[string]any{
		{"account": "acme", "github_login": "ann", "error": "", "repos": acme},
		{"account": "ann", "github_login": "ann", "error": "", "repos": repos("ann", 30)},
	}
}

// projectGithubView is the spec's view: the account as the project sees
// it, the integrations named in added added to it.
func projectGithubView(added []string) map[string]any {
	ig := func(name, typ string, extra map[string]any) map[string]any {
		on := slices.Contains(added, name)
		attachments := []string{}
		if on {
			attachments = []string{"tag:shelley-p"}
		}
		m := map[string]any{
			"name": name, "type": typ, "team": false, "title": "", "comment": "",
			"repositories": []string{}, "attachments": attachments, "added": on, "reaches": on,
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	return map[string]any{
		"integration": "full-exe",
		"tag":         "shelley-p",
		"vms":         []any{map[string]any{"vm_name": "host-vm", "found": true, "tags": []string{}, "tagged": len(added) > 0}},
		"integrations": []any{
			ig("clickhouse", "http-proxy", map[string]any{"comment": "Analytics proxy"}),
			ig("claude", "llm", map[string]any{"title": "Anthropic"}),
			ig("ci", "http-proxy", map[string]any{"team": true, "attachments": []string{"tag:buildkite"}}),
			ig("acme-web", "github", map[string]any{"repositories": []string{"acme/web"}, "attachments": []string{"vm:other"}}),
		},
		"error":      "",
		"manage_url": "https://exe.dev/integrations",
	}
}

// projectGithubMock is the spec's mockExeDev: it mocks the server's
// exe.dev endpoints. Call "connect-github" connects GitHub (if it was
// not); call "changes" returns the account changes the editor asked for,
// as JSON; call "account-reads" counts the reads of the GitHub accounts
// and of the project's exe.dev account.
func (s *shelley) projectGithubMock(connected, restricted bool) func() []map[string]any {
	var mu sync.Mutex
	changes := []map[string]any{}
	added := []string{}
	reads := s.counter("account-reads")
	s.call("connect-github", func() string {
		mu.Lock()
		defer mu.Unlock()
		connected = true
		return "connected"
	})
	s.call("changes", func() string {
		mu.Lock()
		defer mu.Unlock()
		return mustJSON(changes)
	})
	s.route("/api/exe-dev-integrations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"integrations": []string{"full-exe"}, "default": "full-exe"})
	})
	s.route("/api/github-repos", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"repos":       []any{map[string]any{"integration": "host-gh", "repository": "acme/host", "team": true}},
			"connect_url": "https://exe.dev/integrations/add?service=github",
		})
	})
	const refusal = "tag-scoped SSH keys cannot list integrations"
	s.route("/api/github-accounts", func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.URL.RawQuery == "" {
			// The spec's route matched only with a query.
			s.pass.ServeHTTP(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case restricted:
			writeJSON(w, map[string]any{"accounts": nil, "error": refusal, "setup_url": projectGithubSetupURL})
		case connected:
			writeJSON(w, map[string]any{"accounts": projectGithubRepos(), "error": "", "setup_url": projectGithubSetupURL})
		default:
			writeJSON(w, map[string]any{"accounts": []any{}, "error": "", "setup_url": projectGithubSetupURL})
		}
	})
	account := func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "GET" {
			if restricted {
				v := projectGithubView(nil)
				v["integrations"] = []any{}
				v["error"] = refusal
				writeJSON(w, v)
				return
			}
			writeJSON(w, projectGithubView(added))
			return
		}
		var change map[string]any
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &change); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		changes = append(changes, change)
		if change["action"] == "add" {
			name, _ := change["name"].(string)
			added = append(added, name)
		}
		if change["action"] == "github" {
			repo, _ := change["repository"].(string)
			integration := strings.Replace(repo, "/", "-", 1)
			added = append(added, integration)
			writeJSON(w, map[string]any{"integration": integration, "repository": repo})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
	s.route("/api/projects/{name}/exe-account", account)
	s.route("/api/projects/{name}/exe-account/", account)
	return func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(changes)
	}
}

// projectGithubNew makes the project the spec's newProject did, with no
// directory, and returns its name.
func (s *shelley) projectGithubNew() string {
	const name = "GitHub 4f2a9c1e"
	s.shelleyAPI("POST", "/api/projects", map[string]any{"name": name, "instructions": "", "cwd": ""}, nil)
	s.v("NAME", name)
	return name
}

// projectGithubProject returns field of project name in /api/workspace,
// as JSON.
func (s *shelley) projectGithubProject(name, field string) string {
	var w struct {
		Projects []map[string]any `json:"projects"`
	}
	s.shelleyAPI("GET", "/api/workspace", nil, &w)
	for _, p := range w.Projects {
		if p["name"] == name {
			return mustJSON(p[field])
		}
	}
	s.fail("no project %q", name)
	return ""
}

// projectGithubContains reports whether got has every key of want, with
// deeply equal values, as Playwright's expect.objectContaining did.
func projectGithubContains(got, want map[string]any) bool {
	for k, v := range want {
		g, ok := got[k]
		if !ok || !reflect.DeepEqual(g, v) {
			return false
		}
	}
	return true
}

// projectGithubJSON is v as JSON-decoded values, for DeepEqual.
func projectGithubJSON(v any) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(mustJSON(v)), &m); err != nil {
		panic(err)
	}
	return m
}

// projectGithubEditor is how the tests reach and name the project editor.
const projectGithubEditor = `"The editor" is {"role": "region", "name": "Edit project", "exact": true}, the element section[aria-label='Edit project']; things "in the editor" are scoped to it (a selector after "section[aria-label='Edit project'] ", or {"within": {"selector": "section[aria-label='Edit project']"}, ...} for a role). To "open the editor of P": click "section[aria-label='P'] a.project-select", then click {"within": {"selector": "[data-testid='project-view']", "role": "navigation"}, "role": "button", "name": "Settings", "exact": true}. `

func TestE2EProjectGithubStockMode(t *testing.T) {
	e2e(t, useful(0), `stock mode keeps the editor's host repositories, without the exe.dev account. Project {{NAME}} exists. The test mocks the server's exe.dev endpoints: the host's repositories are acme/host; call "account-reads" counts the page's reads of the GitHub accounts and of the project's exe.dev account; call "changes" returns the account changes the editor asked for. `+projectGithubEditor+drawerDirect+drawerSteps+`1. Navigate to /. 2. Click the button "Open conversations", then the button "View options", then {"selector": ".group-by-menu-item", "has_text": "Project", "exact": true}. 3. Open the editor of {{NAME}}. 4. The texts of "section[aria-label='Edit project'] .project-section-heading h3" are exactly ["Repositories", "Conversations run on", "Commit identity", "Actions", "Position in the sidebar"]. 5. {"selector": "section[aria-label='Edit project'] .project-choice", "has_text": "acme/host"} is visible. 6. There is no "section[aria-label='Edit project'] [data-testid='project-integrations']" (assert_count 0). 7. Call "account-reads" with expect "0". 8. Call "changes" with expect "[]".`, func(s *shelley) {
		s.projectGithubNew()
		s.projectGithubMock(true, false)
	})
}

func TestE2EProjectGithubConnectAndAdd(t *testing.T) {
	e2e(t, useful(0), `connect GitHub, find and add repositories and integrations. The server runs in workspace mode. Project {{NAME}} exists. The test mocks the server's exe.dev endpoints: GitHub is not connected until call "connect-github", and then lists 121 repositories (acme/web and acme/repo-000 to acme/repo-089 of the owner acme, ann/repo-000 to ann/repo-029 of ann); the account's integrations are ci, claude, clickhouse and acme-web (for acme/web). Call "changes-match" says "ok" if the account changes the editor asked for are as expected (otherwise it returns them); call "repos" returns the saved repositories of {{NAME}}. `+projectGithubEditor+drawerDirect+drawerSteps+`"The rows" are "section[aria-label='Edit project'] [data-testid='github-repo-list'] .project-repo-row". "The search" is {"within": {"selector": "section[aria-label='Edit project']"}, "role": "searchbox", "name": "Search repositories"} (an input type=search with that aria-label); "the owner" is {"within": {"selector": "section[aria-label='Edit project']"}, "role": "combobox", "name": "Owner"} (a select). "The first row" is "section[aria-label='Edit project'] [data-testid='github-repo-list'] > .project-repo-row:nth-child(1)" and "the second row" the same with :nth-child(2); what a row contains is checked with assert_text_contains. "The connect link" is {"within": {"selector": "section[aria-label='Edit project']"}, "role": "link", "name": "Connect GitHub"}. "The clickhouse integration" is {"selector": "section[aria-label='Edit project'] [data-testid='project-integrations'] .project-repo-row", "has_text": "clickhouse"}. 1. Navigate to /. 2. Click the button "Open tasks". 3. Open the editor of {{NAME}}. 4. Where tasks run comes first, then GitHub, then the other integrations: the texts of "section[aria-label='Edit project'] .project-section-heading h3" are exactly ["Tasks run on", "GitHub repositories", "Integrations", "Commit identity", "Actions", "Position in the sidebar"]. 5. The connect link's href is exactly "`+projectGithubSetupURL+`" (assert_attribute). 6. Keep the link from leaving: eval (no expect) of (() => { const a = [...document.querySelectorAll("section[aria-label='Edit project'] a")].find((a) => a.textContent.includes("Connect GitHub")); a.addEventListener("click", (e) => e.preventDefault()); return "prevented"; })(). 7. Click the connect link. 8. Call "connect-github" (no expect: it acts once). 9. The user comes back: eval (no expect) of (() => { document.dispatchEvent(new Event("visibilitychange")); return "dispatched"; })(). 10. There are exactly 50 rows (assert_count). 11. Click {"within": {"selector": "section[aria-label='Edit project']"}, "role": "button", "name": "Show more (71 left)"}. 12. There are exactly 100 rows. 13. The search's placeholder is exactly "Search 121 repositories" (assert_attribute). 14. Fill the owner with "ann" (fill sets a select by option value). 15. There are exactly 30 rows. 16. Fill the owner with "". 17. Fill the search with "web". 18. There is exactly 1 row. 19. The first row contains "Integration acme-web: adding attaches it to the project's VMs". 20. Click {"within": {"selector": "section[aria-label='Edit project'] [data-testid='github-repo-list'] > .project-repo-row:nth-child(1)"}, "role": "button", "name": "Add"} (the first row's). 21. {"within": {"selector": "section[aria-label='Edit project'] [data-testid='github-repo-list'] > .project-repo-row:nth-child(1)"}, "role": "button", "name": "Remove"} is visible. 22. Fill the search with "acme/repo-007". 23. The first row contains "No integration yet: adding creates one". 24. Click the first row's button "Add" as in step 20. 25. Fill the search with "". 26. The project's repositories come first: the first row contains "acme/repo-007" and the second row "acme/web". 27. The texts of "section[aria-label='Edit project'] [data-testid='project-integrations'] .project-repo-row strong" are exactly ["ci", "claude", "clickhouse"]. 28. Click {"within": {"selector": "section[aria-label='Edit project'] [data-testid='project-integrations'] .project-repo-row", "has_text": "clickhouse"}, "role": "button", "name": "Add"}. 29. The clickhouse integration contains "Added to the project's VMs" (assert_text_contains). 30. Call "changes-match" with expect "ok". 31. Set the viewport to 1100x2000 (set_viewport, keeping the device's scale, mobile and touch). 32. Click the editor's {"role": "button", "name": "Save project", "exact": true}. 33. {"selector": "section[aria-label='Edit project'] *", "has_text": "Saved.", "exact": true} is visible. 34. Call "repos" with expect '[{"integration":"acme-web","repository":"acme/web"},{"integration":"acme-repo-007","repository":"acme/repo-007"}]'.`, func(s *shelley) {
		s.flag("workspace", true)
		name := s.projectGithubNew()
		changes := s.projectGithubMock(false, false)
		want := []map[string]any{
			{"action": "github", "repository": "acme/web", "mode": "host"},
			{"action": "github", "repository": "acme/repo-007"},
			{"action": "add", "name": "clickhouse", "team": false},
		}
		s.call("changes-match", func() string {
			got := changes()
			ok := len(got) == len(want)
			for i := 0; ok && i < len(got); i++ {
				ok = projectGithubContains(got[i], projectGithubJSON(want[i]))
			}
			if ok {
				return "ok"
			}
			return mustJSON(got)
		})
		s.call("repos", func() string { return s.projectGithubProject(name, "repos") })
	})
}

func TestE2EProjectGithubRestricted(t *testing.T) {
	e2e(t, useful(0), `a restricted exe.dev integration explains itself and falls back. The server runs in workspace mode. Project {{NAME}} exists. The test mocks the server's exe.dev endpoints: the exe-dev integration full-exe is restricted ("tag-scoped SSH keys cannot list integrations"), and the host's repositories are acme/host. `+projectGithubEditor+drawerDirect+drawerSteps+`1. Navigate to /. 2. Click the button "Open tasks". 3. Open the editor of {{NAME}}. 4. "section[aria-label='Edit project'] .project-notice" (the first one, which is what a selector picks out) contains "but through full-exe, tag-scoped SSH keys cannot list integrations" (assert_text_contains). 5. {"selector": "section[aria-label='Edit project'] .project-choice", "has_text": "acme/host"} is visible. 6. {"within": {"selector": "section[aria-label='Edit project']"}, "role": "link", "name": "Connect a GitHub repository on exe.dev"} is visible. 7. "section[aria-label='Edit project'] [data-testid='project-integrations']" contains "can't list your account's integrations" (assert_text_contains).`, func(s *shelley) {
		s.flag("workspace", true)
		s.projectGithubNew()
		s.projectGithubMock(true, true)
	})
}

func TestE2EProjectGithubOnboarding(t *testing.T) {
	e2e(t, useful(0), `first-run onboarding starts a project for the picked repositories. The server runs in workspace mode, with the onboarding card showing and no default commit identity yet. The test mocks the server's exe.dev endpoints (GitHub connected, with acme/repo-007 among its repositories) and the commit identity suggestions ("Ann Example", ann@example.com); call "suggestion-logins" returns the distinct GitHub logins the suggestions were asked for. It answers the project saves itself: call "saved-match" says "ok" if the saves were as expected (otherwise it returns them), and call "identities" returns the commit identities saved as the default. `+drawerDirect+drawerSteps+`1. Navigate to /. 2. Click data-testid "onboarding-github". 3. Fill {"role": "searchbox", "name": "Search repositories"} (an input type=search with that aria-label) with "acme/repo-007". 4. Click {"within": {"selector": "[data-testid='github-repo-list']"}, "role": "button", "name": "Add"}. 5. It asks who the tasks commit as, prefilled from GitHub: the value of data-testid "commit-identity-name" is exactly "Ann Example" and that of data-testid "commit-identity-email" exactly "ann@example.com" (assert_value). 6. Fill data-testid "commit-identity-name" with "". 7. Data-testid "github-start" is disabled (assert_disabled). 8. Fill data-testid "commit-identity-name" with "Ann E.". 9. Click data-testid "github-start". 10. The URL path is exactly "/c/setup-id" (assert_url with value). 11. Call "identities" with expect '[{"email":"ann@example.com","name":"Ann E."}]'. 12. Call "saved-match" with expect "ok". 13. Call "suggestion-logins" with expect '[["ann"]]'.`, func(s *shelley) {
		s.flag("workspace", true)
		s.projectGithubMock(true, false)
		s.route("GET /api/workspace", func(w http.ResponseWriter, r *http.Request) {
			var ws map[string]any
			if err := s.fetchJSON(r, &ws); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			ws["onboarding_dismissed"] = false
			ws["commit_identity"] = map[string]any{"name": "", "email": ""}
			writeJSON(w, ws)
		})
		var mu sync.Mutex
		logins := []string{}
		s.route("/api/commit-identity/suggestions", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			got := mustJSON(r.URL.Query()["login"])
			if !slices.Contains(logins, got) {
				logins = append(logins, got)
			}
			mu.Unlock()
			writeJSON(w, map[string]any{
				"names": []string{"Ann Example"},
				"emails": []any{
					map[string]any{"email": "ann@example.com", "source": "github"},
					map[string]any{"email": "9+ann@users.noreply.github.com", "source": "github_noreply"},
				},
				"errors": []string{},
			})
		})
		s.call("suggestion-logins", func() string {
			mu.Lock()
			defer mu.Unlock()
			return "[" + strings.Join(logins, ",") + "]"
		})
		identities := []any{}
		s.route("/api/commit-identity", func(w http.ResponseWriter, r *http.Request) {
			var body any
			json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			identities = append(identities, body)
			mu.Unlock()
			writeJSON(w, body)
		})
		s.call("identities", func() string {
			mu.Lock()
			defer mu.Unlock()
			return mustJSON(identities)
		})
		saved := []map[string]any{}
		s.route("/api/projects", func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			saved = append(saved, body)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(body)
		})
		s.route("/api/projects/repo-007", func(w http.ResponseWriter, r *http.Request) {
			var project map[string]any
			json.NewDecoder(r.Body).Decode(&project)
			mu.Lock()
			saved = append(saved, project)
			mu.Unlock()
			out := maps.Clone(project)
			vm := map[string]any{}
			if v, ok := project["vm"].(map[string]any); ok {
				vm = maps.Clone(v)
			}
			vm["setup_conversation_id"] = "setup-id"
			out["vm"] = vm
			writeJSON(w, out)
		})
		first := projectGithubJSON(map[string]any{"name": "repo-007", "instructions": "", "cwd": "", "actions": []any{}, "vm": nil, "repos": []any{}})
		second := projectGithubJSON(map[string]any{
			"name": "repo-007",
			"cwd":  "/home/exedev/repo-007",
			"vm": map[string]any{
				"base": "", "managed_base_vm": true, "shared_vm": "", "integration": "full-exe", "setup_conversation_id": "",
			},
			"repos":           []any{map[string]any{"integration": "acme-repo-007", "repository": "acme/repo-007"}},
			"commit_identity": map[string]any{"name": "", "email": ""},
		})
		s.call("saved-match", func() string {
			mu.Lock()
			defer mu.Unlock()
			if len(saved) == 2 && reflect.DeepEqual(saved[0], first) && projectGithubContains(saved[1], second) {
				return "ok"
			}
			return mustJSON(saved)
		})
	})
}

func TestE2EProjectGithubCommitIdentity(t *testing.T) {
	e2e(t, useful(0), `a project's settings give it its own commit identity. The server runs in workspace mode. Project {{NAME}} exists. The test mocks the server's exe.dev endpoints (GitHub connected) and the commit identity suggestions (none). Call "commit-identity" returns the saved commit identity of {{NAME}}. `+projectGithubEditor+drawerDirect+drawerSteps+`1. Navigate to /. 2. Click the button "Open tasks". 3. Open the editor of {{NAME}}. 4. The checkbox data-testid "commit-identity-default" in the editor is checked: eval with expect "true" of String(document.querySelector("section[aria-label='Edit project'] [data-testid='commit-identity-default']").checked). 5. Uncheck it: click it, then the same eval has expect "false". 6. Fill "section[aria-label='Edit project'] [data-testid='commit-identity-name']" with "Grace Hopper" and "section[aria-label='Edit project'] [data-testid='commit-identity-email']" with "grace@example.com". 7. Set the viewport to 1100x1400 (set_viewport, keeping the device's scale, mobile and touch). 8. Click {"role": "button", "name": "Save project"} (page-wide, not exact). 9. {"within": {"selector": "section[aria-label='Edit project']"}, "role": "status"} contains "Saved" (assert_text_contains). 10. Call "commit-identity" with expect '{"email":"grace@example.com","name":"Grace Hopper"}'.`, func(s *shelley) {
		s.flag("workspace", true)
		name := s.projectGithubNew()
		s.projectGithubMock(true, false)
		s.route("/api/commit-identity/suggestions", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"names": []string{}, "emails": []any{}, "errors": []string{}})
		})
		s.call("commit-identity", func() string { return s.projectGithubProject(name, "commit_identity") })
	})
}
