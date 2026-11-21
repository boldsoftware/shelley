package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"testing"
)

// Ported from ui/e2e/projects.spec.ts, which ran on a Pixel 5. The spec's
// random project names are fixed here, since every test has its own
// server.

// projectsOpen is the spec's openProjects and the names the tests use.
const projectsOpen = `To "open the projects" (the drawer grouped by project, where projects are managed): click the button "Open conversations", click the button "View options", click {"selector": ".group-by-menu-item", "has_text": "Project", "exact": true}, then check that the button "New project" is visible. "The group P" is the drawer's project group {"role": "region", "name": P, "exact": true}, which is the element section[aria-label='P']; its link is "section[aria-label='P'] a.project-select". "The editor" is {"role": "region", "name": "Edit project", "exact": true} (the element section[aria-label='Edit project']); a field "labelled L" in it is {"within": {"selector": "section[aria-label='Edit project']"}, "role": "textbox", "name": L, "exact": true}, and its button B is {"within": {"selector": "section[aria-label='Edit project']"}, "role": "button", "name": B, "exact": true}. "The view" is data-testid "project-view". "The AGENTS.md label" is "AGENTS.md (instructions used by every project conversation)". `

// projectsWorkspace answers the tests' questions about /api/workspace.
type projectsWorkspace struct {
	Projects []struct {
		Name         string          `json:"name"`
		Instructions string          `json:"instructions"`
		VM           json.RawMessage `json:"vm"`
	} `json:"projects"`
	Conversations []struct {
		ID      string `json:"conversation_id"`
		Project string `json:"project"`
	} `json:"conversations"`
}

func (s *shelley) projectsWorkspace() projectsWorkspace {
	var w projectsWorkspace
	s.api("GET", "/api/workspace", nil, &w)
	return w
}

// projectsNames registers call "projects": the sorted names of the
// projects.
func (s *shelley) projectsNames() {
	s.call("projects", func() string {
		names := []string{}
		for _, p := range s.projectsWorkspace().Projects {
			names = append(names, p.Name)
		}
		slices.Sort(names)
		return mustJSON(names)
	})
}

// projectsCreate makes a project in the test's directory, as the spec's
// POSTs to /api/projects did, and returns the server's project.
func (s *shelley) projectsCreate(name, instructions string) map[string]any {
	var p map[string]any
	s.api("POST", "/api/projects", map[string]any{"name": name, "instructions": instructions, "cwd": s.cwd}, &p)
	return p
}

// projectsGo routes GET /test/go/NAME to a redirect to where() (a path
// the test learns only as it runs), for a navigate step to follow.
func (s *shelley) projectsGo(name string, where func() string) {
	s.route("GET /test/go/"+name, func(w http.ResponseWriter, r *http.Request) {
		to := where()
		if to == "" {
			http.Error(w, "not yet", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, to, http.StatusFound)
	})
}

func TestE2EProjectsCreateEditDelete(t *testing.T) {
	e2e(t, `create, edit and delete a project; start a conversation in it. Conversation {{PLAIN}} exists, and there are no projects yet. Call "projects" returns the sorted names of the projects; call "publish-vm" the VM (JSON) of project "{{NAME}}"; call "notes-instructions" the instructions of project "{{NAME}} notes". Call "start-notes" starts a conversation in project "{{NAME}} notes" through the API, titled "Publish release notes" with the message "Hello" (the composer has no project picker outside workspace mode), and navigating to /test/go/started then opens it. Call "archive-notes" archives the conversation of project "{{NAME}} notes" through the API. `+projectsOpen+drawerDirect+drawerSteps+`"The name field" is {"within": {"selector": "[data-testid='new-project-row']"}, "role": "textbox", "name": "New project", "exact": true}. "The project picker" is {"role": "combobox", "name": "Conversation project", "exact": true} (the select #current-conversation-project), and "its chosen option" is "#current-conversation-project option:checked". "The Rename field" is {"within": {"selector": "[data-testid='project-view']"}, "role": "textbox", "name": "Rename", "exact": true}, and "the title button T" is {"within": {"selector": "[data-testid='project-view']"}, "role": "button", "name": T, "exact": true}. "The delete dialog" is {"role": "dialog", "name": "Delete project?", "exact": true}. 1. Navigate to /c/{{PLAIN}}; data-testid "message-input" is enabled (assert_enabled). 2. Without projects there is no project picker (assert_count 0). 3. Open the projects. 4. Click the button "New project"; the name field is focused (assert_focused). 5. Fill the name field with "Dropped" and press Escape (press_key). 6. There is no name field (assert_count 0). 7. Click the button "New project", then blur the field: eval document.querySelector("[data-testid='new-project-row'] input[aria-label='New project']").blur() (no expect). 8. There is no name field. 9. Click the button "New project", fill the name field with "a/b" and press Enter. 10. {"within": {"selector": "[data-testid='new-project-row']"}, "role": "alert"} reads exactly "The name can't contain / or control characters." (assert_text). 11. Fill the name field with "{{NAME}}", then blur it as in step 7 (leaving the field creates the project). 12. There is no name field. 13. The group {{NAME}} is visible. 14. Call "projects" with expect '["{{NAME}}"]' (nothing named Dropped). 15. Call "publish-vm" with expect "null" (naming a project creates no VM). 16. Click the link of the group {{NAME}}. 17. The URL ends in /project/<digits>/settings: eval with expect "true" of String(/\/project\/\d+\/settings$/.test(location.href)). 18. The buttons of the view's navigation are exactly ["Settings"]: assert_texts of {"within": {"selector": "[data-testid='project-view']", "role": "navigation"}, "role": "button"}. 19. The editor's button "Delete project…" is enabled (assert_enabled). 20. Fill the editor's field labelled with the AGENTS.md label with "Keep release notes brief.". 21. Click the title button {{NAME}}; the Rename field is focused. 22. Fill the Rename field with "Discarded" and press Escape. 23. The editor is visible. 24. Click the title button {{NAME}}, fill the Rename field with "{{NAME}} notes" and press Enter. 25. The title button "{{NAME}} notes" is visible. 26. Call "notes-instructions" with expect "" (the rename saved nothing else), and call "projects" with expect '["{{NAME}} notes"]'. 27. Fill the editor's field labelled "Working directory" with "{{CWD}}". 28. Outside workspace mode a project has no page: there is no {"selector": "section[aria-label='Edit project'] label", "has_text": "Project page", "exact": true} (assert_count 0), while the same with has_text "Working directory" is visible. 29. Click the editor's button "Save project". 30. {"selector": "section[aria-label='Edit project'] *", "has_text": "Saved.", "exact": true} is visible. 31. Reload the page (reload action). 32. The value of the editor's field labelled with the AGENTS.md label is exactly "Keep release notes brief." (assert_value). 33. Call "start-notes" (no expect: it acts once), then navigate to /test/go/started. 34. The project picker is visible, and its chosen option reads exactly "{{NAME}} notes". 35. Click the button "Edit conversation project". 36. The value of the editor's field labelled with the AGENTS.md label is exactly "Keep release notes brief.". 37. Go back (go_back). 38. The editor is not visible (assert_not_visible). 39. Navigate to /c/{{PLAIN}}. 40. Now that a project exists, the project picker is visible, and its chosen option reads exactly "No project". 41. Call "archive-notes" (no expect: it acts once). 42. Open the projects. 43. The group "{{NAME}} notes" is visible. 44. Click its link. 45. Click the editor's button "Delete project…". 46. The delete dialog is visible. 47. {"within": {"role": "dialog", "name": "Delete project?", "exact": true}, "selector": "[data-testid='project-delete-effects']"} reads exactly "Its archived conversation moves to No project.". 48. Click {"within": {"role": "dialog", "name": "Delete project?", "exact": true}, "role": "button", "name": "Delete project", "exact": true}. 49. The delete dialog is not visible. 50. There is no group "{{NAME}} notes" (assert_count 0). 51. Call "projects" with expect "[]".`, func(s *shelley) {
		name := "Publish 4f2a9c1e"
		notes := name + " notes"
		plain := s.conversation("Hello", "")
		s.v("PLAIN", plain.ID)
		s.v("NAME", name)
		s.v("CWD", s.cwd)
		s.projectsNames()
		find := func(n string) int {
			w := s.projectsWorkspace()
			for i, p := range w.Projects {
				if p.Name == n {
					return i
				}
			}
			s.fail("no project %q", n)
			return -1
		}
		s.call("publish-vm", func() string {
			return string(s.projectsWorkspace().Projects[find(name)].VM)
		})
		s.call("notes-instructions", func() string {
			return s.projectsWorkspace().Projects[find(notes)].Instructions
		})
		var mu sync.Mutex
		started := ""
		s.call("start-notes", func() string {
			var created struct {
				ID string `json:"conversation_id"`
			}
			s.api("POST", "/api/conversations/new", map[string]any{"project": notes, "title": "Publish release notes", "message": "Hello"}, &created)
			mu.Lock()
			defer mu.Unlock()
			started = created.ID
			return "started"
		})
		s.projectsGo("started", func() string {
			mu.Lock()
			defer mu.Unlock()
			if started == "" {
				return ""
			}
			return "/c/" + started
		})
		s.call("archive-notes", func() string {
			for _, c := range s.projectsWorkspace().Conversations {
				if c.Project == notes {
					s.api("POST", "/api/conversation/"+c.ID+"/archive", nil, nil)
					return "archived"
				}
			}
			s.fail("no conversation in %q", notes)
			return ""
		})
	})
}

func TestE2EProjectsEditsArriveThroughSSE(t *testing.T) {
	e2e(t, `project edits from elsewhere arrive through SSE. Conversation {{PLAIN}} (slug {{PLAIN_SLUG}}) exists. Call "create-agent" creates project "{{NAME}}" through the API, and call "rename-agent" renames it "{{NAME}} renamed" through the API. `+projectsOpen+drawerDirect+drawerSteps+`1. Navigate to /c/{{PLAIN}}. 2. Open the projects. 3. Call "create-agent" (no expect: it acts once). 4. The button "New conversation in {{NAME}}" is visible. 5. Call "rename-agent" (no expect: it acts once). 6. The button "New conversation in {{NAME}} renamed" is visible. 7. The page stayed on the conversation: eval with expect "true" of String(/\/c\/({{PLAIN_SLUG}}|{{PLAIN}})$/.test(location.href)).`, func(s *shelley) {
		name := "Agent 4f2a9c1e"
		plain := s.conversation("Hello", "")
		s.v("PLAIN", plain.ID)
		s.v("PLAIN_SLUG", plain.Slug)
		s.v("NAME", name)
		var mu sync.Mutex
		var project map[string]any
		s.call("create-agent", func() string {
			p := s.projectsCreate(name, "Created outside this tab")
			mu.Lock()
			defer mu.Unlock()
			project = p
			return "created"
		})
		s.call("rename-agent", func() string {
			mu.Lock()
			defer mu.Unlock()
			if project == nil {
				s.fail("no project yet")
			}
			project["name"] = name + " renamed"
			s.api("PUT", "/api/projects/"+url.PathEscape(name), project, nil)
			return "renamed"
		})
	})
}

func TestE2EProjectsTitleRenamesInPlace(t *testing.T) {
	e2e(t, `a conversation's title renames it in place, in a bar as tall as a project's. Conversation {{ID}} has the slug {{SLUG}}. Call "slug" returns its slug from the server. Call "create-bar" creates project "{{NAME}}" through the API, and navigating to /test/go/bar then opens its settings (/project/<id>/settings). `+drawerDirect+drawerSteps+`"The header" is the first ".header". "The title button T" is {"within": {"selector": ".header"}, "role": "button", "name": T, "exact": true}, and "the Rename field" {"within": {"selector": ".header"}, "role": "textbox", "name": "Rename", "exact": true}. 1. Navigate to /c/{{ID}}. 2. Click the title button {{SLUG}}. 3. The Rename field is focused (assert_focused), and its value is exactly "{{SLUG}}" (assert_value). 4. Fill the Rename field with "Discarded" and press Escape (press_key). 5. There is no Rename field (assert_count 0). 6. Click the title button {{SLUG}}, fill the Rename field with "titled 4f2a9c1e!" and press Enter. 7. The title button "titled-4f2a9c1e" is visible. 8. Call "slug" with expect "titled-4f2a9c1e". 9. Note the header's height, once: eval (no expect) of (() => { const h = document.querySelector(".header").getBoundingClientRect().height; sessionStorage.setItem("lcConversationBar", String(h)); return String(h); })(). 10. Call "create-bar" (no expect: it acts once), then navigate to /test/go/bar. 11. {"within": {"selector": "[data-testid='project-view']"}, "role": "button", "name": "{{NAME}}", "exact": true} is visible. 12. The view's header is exactly as tall, read once: eval (no expect) of (() => { const bar = Number(sessionStorage.getItem("lcConversationBar")); const headers = document.querySelectorAll("[data-testid='project-view'] .header"); if (headers.length !== 1) throw new Error("project view headers: " + headers.length); const h = headers[0].getBoundingClientRect().height; if (!bar || h !== bar) throw new Error("project bar " + h + ", conversation bar " + bar); return String(h); })().`, func(s *shelley) {
		c := s.conversation("Hello", "")
		s.v("ID", c.ID)
		s.v("SLUG", c.Slug)
		name := "Bar 4f2a9c1e"
		s.v("NAME", name)
		s.call("slug", func() string {
			var got struct {
				Conversation struct {
					Slug string `json:"slug"`
				} `json:"conversation"`
			}
			s.api("GET", "/api/conversation/"+c.ID, nil, &got)
			return got.Conversation.Slug
		})
		var mu sync.Mutex
		bar := ""
		s.call("create-bar", func() string {
			p := s.projectsCreate(name, "")
			id, ok := p["id"].(float64)
			if !ok {
				s.fail("project without an id: %v", p)
			}
			mu.Lock()
			defer mu.Unlock()
			bar = fmt.Sprintf("/project/%d/settings", int64(id))
			return "created"
		})
		s.projectsGo("bar", func() string {
			mu.Lock()
			defer mu.Unlock()
			return bar
		})
	})
}

func TestE2EProjectsDeleteWaitsMoveToProject(t *testing.T) {
	e2e(t, `project settings and conversation menu: delete waits for an empty project; move to project. Projects "{{FROM}}" and "{{TO}}" exist, and conversation {{ID}} (titled "Move me 4f2a9c1e") is in {{FROM}}. `+projectsOpen+drawerDirect+drawerSteps+`"The row" is ".conversation-item[data-conversation-id='{{ID}}']"; "the row in P" is "section[aria-label='P'] .conversation-item[data-conversation-id='{{ID}}']". "The toggle of P" is "section[aria-label='P'] .project-toggle". 1. Navigate to /c/{{ID}}. 2. Open the projects. 3. The row in {{FROM}} is visible. 4. There is no ".conversation-item[data-conversation-id='{{ID}}'] .working-indicator" (assert_count 0). 5. Empty projects start folded: the toggle of {{FROM}} has aria-expanded exactly "true", and the toggle of {{TO}} "false" (assert_attribute). 6. Click the toggle of {{TO}}; its aria-expanded is "true". 7. Click the toggle of {{TO}}; its aria-expanded is "false". 8. Click the link "section[aria-label='{{FROM}}'] a.project-select". 9. {"within": {"selector": "section[aria-label='Edit project']"}, "role": "button", "name": "Delete project…"} is disabled (assert_disabled). 10. The editor contains the text "Archive or move its active conversation first" (assert_text_contains on "section[aria-label='Edit project']"). 11. Go back (go_back). 12. Open the projects. 13. Click {"selector": ".conversation-item[data-conversation-id='{{ID}}']", "role": "button", "name": "Actions", "exact": true}. 14. Click {"within": {"selector": "[data-testid='conversation-actions-menu']"}, "role": "menuitem", "name": "Move to project"}. 15. Click {"within": {"selector": "[data-testid='conversation-actions-menu']"}, "role": "menuitem", "name": "{{TO}}", "exact": true}. 16. The row in {{TO}} is visible. 17. There is no row in {{FROM}} (assert_count 0). 18. The first conversation unfolds its project, the emptied one folds: the toggle of {{TO}} has aria-expanded "true", and the toggle of {{FROM}} "false". 19. An explicit choice outlasts emptiness: click the toggle of {{FROM}}; its aria-expanded is "true". 20. Click the toggle of {{TO}}. 21. There is no row in {{TO}} (assert_count 0). 22. The toggle of {{TO}} has aria-expanded "false".`, func(s *shelley) {
		from, to := "From 4f2a9c1e", "To 4f2a9c1e"
		s.v("FROM", from)
		s.v("TO", to)
		source := s.projectsCreate(from, "")
		s.projectsCreate(to, "")
		var created struct {
			ID string `json:"conversation_id"`
		}
		s.api("POST", "/api/conversations/new", map[string]any{"project": source["name"], "title": "Move me 4f2a9c1e", "message": "Hello"}, &created)
		s.v("ID", created.ID)
	})
}
