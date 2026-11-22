package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

	lazycue "github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/workspace.spec.ts. Every test runs in workspace mode
// (the "workspace" feature flag), as the spec's beforeEach turned it on.

// workspaceHow says how a step opens the drawer (the spec's openWorkspace),
// reloads, opens a project's settings (openProjectSettings) and edits a
// project (editProject).
const workspaceHow = workspaceLoad + `To "open the drawer": click the button named exactly "Open tasks" if it is visible (on a phone it is whenever the drawer is closed: it is the header's top-left button; the drawer's own "Close tasks" button stays in the page while the drawer is off-canvas, so it says nothing about whether the drawer is open); the element with data-testid "assistant-row" should then be visible within the viewport. To "reload": the reload action. To "open the settings of" a project P: click "section[aria-label=\"P\"] a.project-select", then, in the element with data-testid "project-view", click the button named exactly "Settings" in its navigation (its <nav> element; click it even when its tab already shows); "the editor" is the region (role "region"; a <section> with that aria-label) named exactly "Edit project". To "edit project P" with a change: eval sessionStorage.setItem("editFrom", location.pathname) to remember the page; open the drawer; open the settings of P; make the change in the editor; click the editor's button named exactly "Save project"; the editor's text "Saved." should be visible; then return to the remembered page with a full load: eval (() => { window.__leaving = 1; location.assign(sessionStorage.getItem("editFrom")); return "leaving"; })(), expecting "leaving", then eval "window.__leaving === undefined && document.readyState === 'complete'" until it returns true. `

// workspaceQueryHow says how a step sets the search query
// (fillConversationQuery and clearConversationQuery).
const workspaceQueryHow = `"The search editor" is the element with data-testid "conversation-query-editor". To "set the query" to Q: eval the search editor's focus(); press "a" with the Control modifier; insert Q with the insert_text action; the search editor's data-query-value attribute should then be exactly Q. To "clear the query": eval its focus(); press "a" with the Control modifier; press "Backspace"; its data-query-value should then be exactly "". `

// workspaceProject creates a project through the API, as the spec did, and
// returns the server's copy of it.
func (s *shelley) workspaceProject(name string) map[string]any {
	var project map[string]any
	s.shelleyAPI("POST", "/api/projects", map[string]any{
		"name": name, "instructions": "", "cwd": s.cwd, "actions": []any{}, "repos": []any{}, "vm": nil,
	}, &project)
	return project
}

// workspaceTask starts a task in a project through the API, as the spec
// did, and returns its id.
func (s *shelley) workspaceTask(project, title, message string) string {
	var created struct {
		ID string `json:"conversation_id"`
	}
	s.shelleyAPI("POST", "/api/conversations/new", map[string]any{"project": project, "title": title, "message": message}, &created)
	return created.ID
}

type workspaceState struct {
	AssistantID         string `json:"assistant_id"`
	OnboardingDismissed bool   `json:"onboarding_dismissed"`
	Projects            []struct {
		ID   json.Number `json:"id"`
		Name string      `json:"name"`
	} `json:"projects"`
	Conversations []struct {
		ID      string `json:"conversation_id"`
		Project string `json:"project"`
	} `json:"conversations"`
}

func (s *shelley) workspace() workspaceState {
	var w workspaceState
	s.shelleyAPI("GET", "/api/workspace", nil, &w)
	return w
}

func (w workspaceState) projectNames() []string {
	var names []string
	for _, p := range w.Projects {
		names = append(names, p.Name)
	}
	return names
}

func TestE2EWorkspaceDevProdTabsProdLinksSetup(t *testing.T) {
	const setupURL = "https://exe.dev/integrations/add?service=exe-dev&attach=vm%3Ahost&source=shelley"
	e2e(t, useful(0), `The dev-prod-tabs flag adds a prod tab that links to exe.dev integration setup. The server runs in workspace mode with the "dev-prod-tabs" flag on, on a host without an exe.dev integration (as in CI): its VM list is empty, with the setup URL "`+setupURL+`". `+workspaceHow+`Perform the steps in order. 1. Navigate to /. 2. Open the drawer. 3. Click the element with data-testid "nav-tab-prod". 4. The element with data-testid "assistant-row" should be visible (prod keeps the pinned Assistant above its VM list), with an href of exactly "/" and exactly 0 buttons inside it. 5. The link named "Set up an exe.dev integration" should have an href of exactly "`+setupURL+`". 6. There must be exactly 0 elements with the text "No VMs.". 7. Click the element with data-testid "nav-tab-dev".`, func(s *shelley) {
		s.flag("workspace", true)
		s.flag("dev-prod-tabs", true)
		s.route("/api/exe-vms", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"vms": []any{}, "setup_url": setupURL})
		})
	})
}

func TestE2EWorkspaceOnboardingCardUntilDismissed(t *testing.T) {
	const text = "echo: onboarding card"
	e2e(t, useful(0), `The onboarding card tops the Assistant's transcript until dismissed. The server runs in workspace mode. Until call "stop overriding", the workspace the page loads says the onboarding card was not dismissed, and the onboarding details say: no text number, not connected, exe.dev integration "acme-exe", VM host "NAME.exe.test"; after it, the page gets the server's own. Call "dismissed" returns the server's onboarding_dismissed ("true" or "false"). "The card" is the element with data-testid "onboarding"; "the message input" is data-testid "message-input"; "the sent message" is the first element inside ".messages-list" whose text contains "`+text+`". `+workspaceHow+`Perform the steps in order. 1. Navigate to /. 2. The card should be visible. 3. Click the card's element with data-testid "onboarding-web-app" (Build a web app prefills a prompt for the user to finish). 4. The message input's value should come to contain "through the acme-exe integration". 5. Its value should come to match the regular expression /make it public with `+"`share set-public NAME`"+`[\s\S]*\n\nThe app: $/ (the prompt ends with a blank line, then "The app: "). 6. Fill the message input with "`+text+`". 7. Click the element with data-testid "send-button". 8. The sent message should be visible. 9. The card must still be in the page (exactly 1). 10. Eval this; it must return true (the sent message lands below the card): [...document.querySelectorAll(".messages-list *")].some((m) => m.textContent === "`+text+`" && !!(document.querySelector('[data-testid="onboarding"]').compareDocumentPosition(m) & Node.DOCUMENT_POSITION_FOLLOWING)). 11. Eval the card's scrollIntoView(). 12. Call "stop overriding". 13. Click the card's element with data-testid "onboarding-dismiss". 14. There should be exactly 0 cards. 15. Reload. 16. The sent message should be visible. 17. There must be exactly 0 cards. 18. Call "dismissed": it must return "true" (dismissing is the server's, so it survives reloads).`, func(s *shelley) {
		s.flag("workspace", true)
		var override atomic.Bool
		override.Store(true)
		s.route("GET /api/workspace", func(w http.ResponseWriter, r *http.Request) {
			if !override.Load() {
				s.pass.ServeHTTP(w, r)
				return
			}
			var body map[string]any
			if err := s.fetchJSON(r, &body); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			body["onboarding_dismissed"] = false
			writeJSON(w, body)
		})
		s.route("/api/onboarding", func(w http.ResponseWriter, r *http.Request) {
			if !override.Load() {
				s.pass.ServeHTTP(w, r)
				return
			}
			writeJSON(w, map[string]any{"text_number": "", "text_connected": false, "exe_dev": "acme-exe", "vm_host": "NAME.exe.test"})
		})
		s.call("stop overriding", func() string {
			override.Store(false)
			return "stopped"
		})
		s.call("dismissed", func() string { return strconv.FormatBool(s.workspace().OnboardingDismissed) })
	})
}

func TestE2EWorkspaceRootOpensAssistant(t *testing.T) {
	e2e(t, useful(0), `Root opens the pinned Assistant; explicit conversation links remain selected. The server runs in workspace mode, with the conversation {{CONV_ID}} (slug {{CONV_SLUG}}), and the onboarding card not dismissed. Call "archive a new task" starts another conversation, {{ARCHIVED}}, and archives it. "The row of" a conversation X is ".conversation-item[data-conversation-id=\"X\"]". `+workspaceHow+`Perform the steps in order. 1. Navigate to /. 2. "h1.header-title" should have exactly the text "Assistant". 3. The page title should start with "Assistant - ". 4. The heading named exactly "What would you like to get done?" should be visible. 5. There must be exactly 1 element with data-testid "onboarding-web-app" (the onboarding card offers ways to start until the user dismisses it). 6. Open the drawer. 7. There must be exactly 1 element with data-testid "assistant-row", and exactly 0 matching ".conversation-group [data-testid=\"assistant-row\"]". 8. There must be exactly 0 elements with data-testid "nav-tab-prod" (the dev/prod tabs need their own flag). 9. There must be exactly 0 buttons named exactly "View Archived" (archived tasks are behind search's Archived toggle; the footer button is stock Shelley's). 10. Call "archive a new task". 11. The row of {{CONV_ID}} should be visible. 12. There should be exactly 0 rows of {{ARCHIVED}}. 13. Click the button named exactly "Search tasks...". 14. Click the element with data-testid "archived-toggle" ("the toggle"). 15. The toggle should have aria-pressed "true". 16. The row of {{CONV_ID}} should be visible. 17. The row of {{ARCHIVED}} should have the class "conversation-item-archived". 18. The assistant row should be visible, with exactly 0 buttons inside it. 19. The button named "Restore" in the row of {{ARCHIVED}} must be hidden (an archived row's Restore and Delete take its Archive's place). 20. Click the row of {{ARCHIVED}}. 21. Open the drawer. 22. The button named "Delete" in the row of {{ARCHIVED}} should be visible (a touch screen shows them on the open row). 23. Click the button named "Restore" in the row of {{ARCHIVED}}. 24. The row of {{ARCHIVED}} should come to not have the class "conversation-item-archived". 25. Click the toggle. 26. The row of {{ARCHIVED}} should be visible. 27. Navigate to /c/{{CONV_ID}}. 28. "h1.header-title" should come to not have exactly the text "Assistant". 29. The URL should end with /c/{{CONV_SLUG}} or /c/{{CONV_ID}}, with nothing after it.`, func(s *shelley) {
		s.flag("workspace", true)
		c := s.conversation("Hello", "")
		s.v("CONV_ID", c.ID)
		s.v("CONV_SLUG", c.Slug)
		if s.workspace().OnboardingDismissed {
			s.fail("a fresh server's onboarding card is dismissed")
		}
		s.call("archive a new task", func() string {
			archived := s.conversation("Hello", "")
			s.shelleyAPI("POST", "/api/conversation/"+archived.ID+"/archive", nil, nil)
			s.v("ARCHIVED", archived.ID)
			return "archived"
		})
	})
}

func TestE2EWorkspaceAssistantURLIsRoot(t *testing.T) {
	e2e(t, useful(0), `The Assistant's one URL is /. The server runs in workspace mode, with the Assistant conversation {{ASSISTANT}} and the conversation {{CONV_ID}} (slug {{CONV_SLUG}}). "The header title" is "h1.header-title"; "on the conversation" means the URL path is /c/{{CONV_SLUG}} or /c/{{CONV_ID}}. `+workspaceHow+`Perform the steps in order. 1. Navigate to /c/{{ASSISTANT}}?message_id=m1. 2. The header title should have exactly the text "Assistant". 3. The URL's path and query should come to be exactly "/?message_id=m1". 4. Navigate to /c/{{CONV_ID}}. 5. The page should come to be on the conversation. 6. Navigate to /. 7. The header title should have exactly the text "Assistant". 8. Open the drawer. 9. The element with data-testid "assistant-row" should have an href of exactly "/". 10. The URL's path and query should be exactly "/". 11. Eval: window.history.pushState({}, "", "/c/{{CONV_ID}}"); window.dispatchEvent(new PopStateEvent("popstate")). 12. The header title should come to not have exactly the text "Assistant". 13. Go back (the go_back action). 14. The header title should come to have exactly the text "Assistant". 15. The URL's path and query should be exactly "/". 16. Go forward (the go_forward action). 17. The header title should come to not have exactly the text "Assistant". 18. The page should be on the conversation. 19. Navigate to /c/{{CONV_ID}}. 20. Open the drawer. 21. Click the element with data-testid "assistant-row". 22. The header title should come to have exactly the text "Assistant". 23. The URL's path and query should come to be exactly "/".`, func(s *shelley) {
		s.flag("workspace", true)
		c := s.conversation("Hello", "")
		s.v("CONV_ID", c.ID)
		s.v("CONV_SLUG", c.Slug)
		s.v("ASSISTANT", s.workspace().AssistantID)
	})
}

func TestE2EWorkspaceProjectEditorActions(t *testing.T) {
	const name = "Publish tasks"
	e2e(t, useful(0), `Project editor, task creation, failed action retry, and success-only archive. The server runs in workspace mode, on a host without exe.dev integrations (as in CI): the VM choices stay until the editor closes, even after picking this host. "The project view" is the element with data-testid "project-view"; "the publish button" is the button named exactly "Publish & archive"; "the run card containing" T is the element with data-testid "project-action-card" containing T, and its "status" is its element with data-testid "project-action-status". Shell commands below are typed exactly as shown; \n in them is the two characters backslash and n, not a line break. `+workspaceHow+`Perform the steps in order. 1. Navigate to /. 2. "h1.header-title" should have exactly the text "Assistant". 3. Open the drawer. 4. Click the button named exactly "New project". 5. Fill the field labelled exactly "New project" inside the element with data-testid "new-project-row" with "`+name+`", and press Enter in it. 6. There should come to be exactly 0 such fields. 7. Open the settings of "`+name+`" (a project without a page opens on its settings). 8. The URL path should come to match /^\/project\/\d+\/settings$/. 9. The editor's button named "Delete project…" should be enabled. 10. Fill the editor's field labelled exactly "AGENTS.md (instructions used by every project task)" with "Keep publishing tasks brief.". 11. Fill its field labelled exactly "Working directory" with "{{CWD}}". 12. Fill its field labelled exactly "Project page" with "https://example.com/". 13. Click its button named exactly "Add action". 14. Fill its field labelled exactly "ID" with "publish", its "Button label" with "Publish & archive", and its "Shell command" with: printf 'publishing failed\n'; exit 7 15. Check its checkbox labelled exactly "Archive task on success (publish/apply actions)". 16. Click its button named exactly "Save project". 17. Its text "Saved." (exactly) should be visible. 18. The buttons in the project view's navigation should come to be exactly two, with the texts "example.com" and "Settings", in that order (a page gives the view a tab framing it, first). 19. Click the project view's button named exactly "example.com". 20. The URL path should come to match /^\/project\/\d+$/. 21. The editor should be hidden. 22. The link named "Open example.com in a new tab" in the project view's ".header" should have an href of exactly "https://example.com/". 23. Go back (the go_back action). 24. The editor should be visible. 25. Open the drawer. 26. Click the button named exactly "New task in `+name+`". 27. ".project-picker" should have exactly the text "`+name+`" (the regular composer starts it, with the project picked). 28. Fill the element with data-testid "message-input" with "Hello". 29. Click the button named exactly "Send message". 30. The URL path should come to start with "/c/". 31. The publish button should be enabled. 32. Click the publish button. 33. The status of the run card containing "Failed (exit 7)" should have exactly the text "Failed (exit 7)", and that card should contain "publishing failed". 34. "h1.header-title" must not contain "archived". 35. The publish button should be enabled. 36. The element with data-testid "project-action-card" inside an element with data-testid "message" should contain "Sent to the agent" (the agent gets the failure to work on, in the run's card). 37. Edit project "`+name+`" with the change: fill the editor's "Shell command" with: printf 'waiting\n'; sleep 600 38. Click the publish button. 39. The run card containing "Running" should contain "waiting". 40. Click that card's element with data-testid "project-action-stop" (a command that hangs can be stopped). 41. The status of the run card containing "Stopped by the user" should have exactly the text "Interrupted". 42. The publish button should be enabled. 43. Edit project "`+name+`" with the change: fill the editor's "Shell command" with: printf 'published successfully\n' 44. Click the publish button. 45. The status of the run card containing "Succeeded" should have exactly the text "Succeeded". 46. "h1.header-title" should come to contain "archived". 47. The run card containing "Succeeded" should contain "published successfully". 48. The run card containing "Failed (exit 7)" should still be visible. 49. The publish button should be disabled.`, func(s *shelley) {
		s.flag("workspace", true)
		s.route("/api/exe-dev-integrations", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"integrations": []any{}, "default": ""})
		})
	})
}

func TestE2EWorkspaceProjectPlusComposesThere(t *testing.T) {
	const a, b = "Plus A", "Plus B"
	e2e(t, useful(0), `A project's + composes its new conversation there, in the drawer and on the server. The server runs in workspace mode, with the projects "`+a+`" and "`+b+`" and no tasks. Call "new task" returns the project the server has the one task in, and makes that task {{NEW_TASK}}. "The composer row of" P is "section[aria-label=\"P\"] [data-conversation-id=\"composer\"]". `+workspaceHow+`Perform the steps in order. 1. Navigate to /. 2. Open the drawer. 3. The button named exactly "New task in `+a+`" should have the computed opacity "1" (phones, which can't hover, always show a project's +). 4. Click it. 5. ".project-picker" should have exactly the text "`+a+`". 6. Open the drawer. 7. The composer row of "`+a+`" should be visible. 8. Click the button named exactly "New task in `+b+`" (another project's + moves the new conversation there). 9. ".project-picker" should have exactly the text "`+b+`". 10. Open the drawer. 11. The composer row of "`+b+`" should be visible, and there should be exactly 0 composer rows of "`+a+`". 12. Click the button named exactly "Close tasks". 13. Click the button named exactly "New Task" (New Task from its composer stays in the project). 14. ".project-picker" should have exactly the text "`+b+`". 15. Fill the element with data-testid "message-input" with "Hello". 16. Click the button named exactly "Send message". 17. The URL path should come to start with "/c/". 18. Open the drawer. 19. Call "new task": it must return exactly "`+b+`". 20. There must be exactly 1 element matching "section[aria-label=\"`+b+`\"] .conversation-item[aria-current=\"page\"][data-conversation-id=\"{{NEW_TASK}}\"]" (the drawer's open row is that task, in that project).`, func(s *shelley) {
		s.flag("workspace", true)
		s.workspaceProject(a)
		s.workspaceProject(b)
		s.call("new task", func() string {
			w := s.workspace()
			var tasks []string
			project := ""
			for _, c := range w.Conversations {
				if c.ID != w.AssistantID {
					tasks = append(tasks, c.ID)
					project = c.Project
				}
			}
			if len(tasks) != 1 {
				return fmt.Sprintf("%d tasks: %v", len(tasks), tasks)
			}
			s.v("NEW_TASK", tasks[0])
			return project
		})
	})
}

func TestE2EWorkspaceAgentProjectEditsArriveLive(t *testing.T) {
	const name = "Agent project"
	e2e(t, useful(0), `Agent project edits arrive through workspace SSE without navigating away from Assistant. The server runs in workspace mode. Call "create project" creates the project "`+name+`" outside this tab (as the agent would); call "rename project" renames it "`+name+` renamed". `+workspaceHow+`Perform the steps in order. 1. Navigate to /. 2. "h1.header-title" should have exactly the text "Assistant". 3. Open the drawer. 4. Call "create project". 5. The button named exactly "New task in `+name+`" should become visible. 6. Call "rename project". 7. The button named exactly "New task in `+name+` renamed" should become visible. 8. "h1.header-title" should still have exactly the text "Assistant".`, func(s *shelley) {
		s.flag("workspace", true)
		var project map[string]any
		s.call("create project", func() string {
			s.shelleyAPI("POST", "/api/projects", map[string]any{"name": name, "instructions": "Created outside this tab", "cwd": s.cwd, "actions": []any{}}, &project)
			return "created"
		})
		s.call("rename project", func() string {
			project["name"] = name + " renamed"
			s.shelleyAPI("PUT", "/api/projects/"+url.PathEscape(name), project, nil)
			return "renamed"
		})
	})
}

func TestE2EWorkspaceProjectDeleteWaitsMoveChangesGroups(t *testing.T) {
	const from, to = "From tasks", "To tasks"
	e2e(t, useful(0), `A project's settings: delete waits for an empty project; a moved task changes groups. The server runs in workspace mode, with the projects "`+from+`", which has the task {{TASK}} ("Move me"), and "`+to+`", which is empty. Call "move" moves the task to "`+to+`" through the API (as the Assistant would; not the sidebar). "The task row" is ".conversation-item[data-conversation-id=\"{{TASK}}\"]"; "the group of" P is "section[aria-label=\"P\"]" and its "toggle" is its ".project-toggle". `+workspaceHow+`Perform the steps in order. 1. Navigate to /. 2. Open the drawer. 3. The task row inside the group of "`+from+`" should be visible. 4. The task row should come to hold exactly 0 ".working-indicator" elements. 5. The toggle of "`+from+`" should have aria-expanded "true", and that of "`+to+`" aria-expanded "false" (empty projects start folded). 6. Click the toggle of "`+to+`"; it should come to have aria-expanded "true". 7. Click it again; it should come to have aria-expanded "false" (fold the empty project explicitly; its first conversation unfolds it anyway). 8. Open the settings of "`+from+`". 9. The editor's button named "Delete project…" should be disabled, and the editor should contain "Archive or move its active task first" (a project with conversations can't be deleted, and its settings say why). 10. Go back (the go_back action). 11. Open the drawer. 12. Call "move". 13. The task row inside the group of "`+to+`" should become visible. 14. There should be exactly 0 task rows inside the group of "`+from+`". 15. The toggle of "`+to+`" should have aria-expanded "true", and that of "`+from+`" aria-expanded "false" (the first conversation unfolds its project; the emptied one folds). 16. Click the toggle of "`+from+`"; it should come to have aria-expanded "true" (an explicit choice outlasts emptiness). 17. Click the toggle of "`+to+`". 18. There should come to be exactly 0 task rows inside the group of "`+to+`", and its toggle should have aria-expanded "false".`, func(s *shelley) {
		s.flag("workspace", true)
		s.workspaceProject(from)
		s.workspaceProject(to)
		task := s.workspaceTask(from, "Move me", "Hello")
		s.v("TASK", task)
		s.call("move", func() string {
			s.shelleyAPI("PUT", "/api/conversation/"+task+"/project", map[string]any{"project": to}, nil)
			return "moved"
		})
	})
}

func TestE2EWorkspaceProjectFoldsSurviveReloadsAndRenames(t *testing.T) {
	const busy, empty, renamed = "Busy tasks", "Empty tasks", "Renamed tasks"
	e2e(t, useful(0), `Project folds survive reloads and renames; a deleted project's goes. The server runs in workspace mode, with the projects "`+busy+`" (id {{BUSY_ID}}), which has a task, and "`+empty+`" (id {{EMPTY_ID}}), right after it, which is empty. Call "order" returns the server's project names, in order, JSON-encoded; call "rename and delete" renames "`+busy+`" to "`+renamed+`" and deletes "`+empty+`". "The toggle of" P is "section[aria-label=\"P\"] .project-toggle". `+workspaceHow+`Perform the steps in order. 1. Navigate to /. 2. Open the drawer. 3. The toggle of "`+busy+`" should have aria-expanded "true", and that of "`+empty+`" aria-expanded "false". 4. Click the toggle of "`+busy+`", then the toggle of "`+empty+`". 5. The toggle of "`+busy+`" should come to have aria-expanded "false", and that of "`+empty+`" aria-expanded "true". 6. Reload. 7. Open the drawer. 8. The toggle of "`+busy+`" should have aria-expanded "false", and that of "`+empty+`" aria-expanded "true". 9. Open the settings of "`+empty+`". 10. Click the editor's button named exactly "Move up" (Move up swaps a project with the one before it, for every list of projects). 11. Call "order" until it returns exactly {{AFTER}}. 12. Eval this until it returns true: the aria-labels of the "section.project-group" elements, in document order, kept only if "`+busy+`" or "`+empty+`", are exactly ["`+empty+`", "`+busy+`"]. 13. Call "rename and delete" (folds follow the project's id, not its name). 14. Reload. 15. Open the drawer. 16. The toggle of "`+renamed+`" should have aria-expanded "false". 17. There must be exactly 0 toggles of "`+empty+`". 18. Eval this; it must return true: (() => { const stored = JSON.parse(localStorage.getItem("shelley-project-folds") ?? "{}"); return stored["project:{{BUSY_ID}}"] === true && !("project:{{EMPTY_ID}}" in stored); })().`, func(s *shelley) {
		s.flag("workspace", true)
		b := s.workspaceProject(busy)
		e := s.workspaceProject(empty)
		s.v("BUSY_ID", fmt.Sprint(b["id"]))
		s.v("EMPTY_ID", fmt.Sprint(e["id"]))
		s.workspaceTask(busy, "Fold me", "Hello")
		before := s.workspace().projectNames()
		at := slices.Index(before, empty)
		if at < 1 || before[at-1] != busy {
			s.fail("projects %q: want %q right before %q", before, busy, empty)
		}
		after := slices.Clone(before)
		after[at-1], after[at] = after[at], after[at-1]
		s.v("AFTER", mustJSON(after))
		s.call("order", func() string { return mustJSON(s.workspace().projectNames()) })
		s.call("rename and delete", func() string {
			body := map[string]any{"name": renamed, "instructions": "", "cwd": s.cwd, "actions": []any{}, "repos": []any{}, "vm": nil}
			s.shelleyAPI("PUT", "/api/projects/"+url.PathEscape(busy), body, nil)
			s.shelleyAPI("DELETE", "/api/projects/"+url.PathEscape(empty), nil, nil)
			return "done"
		})
	})
}

func TestE2EWorkspaceSearchShowsFoldedMatches(t *testing.T) {
	const word = "foldsearchword"
	const project = "Folded " + word
	e2e(t, useful(0), `A search shows matches in folded projects without unfolding them. The server runs in workspace mode, with the project "`+project+`" (id {{PROJECT_ID}}) holding the task {{TASK}}, titled "Find me `+word+`". "The toggle" is "section[aria-label=\"`+project+`\"] .project-toggle"; "the row" is "[data-conversation-id=\"{{TASK}}\"]". "The saved folds" are localStorage.getItem("shelley-project-folds"); "the saved folds are unchanged" means eval localStorage.getItem("shelley-project-folds") === sessionStorage.getItem("folds-before") returns true. `+workspaceHow+workspaceQueryHow+`Perform the steps in order. 1. Navigate to /. 2. Open the drawer. 3. Click the toggle. 4. The toggle should come to have aria-expanded "false". 5. There should be exactly 0 rows. 6. Eval this; it must return true: JSON.parse(localStorage.getItem("shelley-project-folds"))["project:{{PROJECT_ID}}"] === true. 7. Eval (() => { sessionStorage.setItem("folds-before", localStorage.getItem("shelley-project-folds")); return "saved"; })(), expecting "saved". 8. Click the button named "Search tasks...". 9. The element with data-testid "archived-toggle" should be visible. 10. There must be exactly 0 buttons named "Add tag filter" (projects, not tags, organize tasks). 11. Set the query to "tag:". 12. There must be exactly 0 elements with data-testid "tag-filter-panel" (no tag: suggestions). 13. Set the query to "`+word+`". 14. The row should be visible. 15. The toggle should have aria-expanded "true". 16. Click the toggle. 17. There should come to be exactly 0 rows (folding during a search lasts only as long as the search). 18. Click the toggle. 19. The row should be visible. 20. The saved folds are unchanged. 21. Reload (a reload mid-search leaves the saved fold as it was). 22. Open the drawer. 23. The toggle should have aria-expanded "false". 24. The saved folds are unchanged. 25. Click the button named "Search tasks...". 26. Set the query to "`+word+`". 27. The row should be visible. 28. Clear the query. 29. The toggle should have aria-expanded "false". 30. There should be exactly 0 rows. 31. The saved folds are unchanged.`, func(s *shelley) {
		s.flag("workspace", true)
		p := s.workspaceProject(project)
		s.v("PROJECT_ID", fmt.Sprint(p["id"]))
		s.v("TASK", s.workspaceTask(project, "Find me "+word, "echo: "+word))
	})
}

func TestE2EWorkspaceAssistantHidesTaskChrome(t *testing.T) {
	const text = "texted from a phone"
	e2e(t, useful(0), `The Assistant shows no title, host, working directory, diffs, git graph or message actions; conversations still do. The server runs in workspace mode, with the conversation {{CONV_ID}}, whose first message is "Hello". `+workspaceLoad+`Call "text the Assistant" has a text message "`+text+`" arrive at the Assistant through a channel. "The git buttons" are the buttons in ".chat-workspace-actions" named exactly "Diffs" or "Git Graph". "The title" is "h1.header-title", "the header" is ".header", and "the actions" are ".header-actions"; read their boxes with getBoundingClientRect(). To "check the brand rule" W: click the button named exactly "Collapse sidebar" (in the drawer's header); ".header-left .exe-brand" should have the computed border-right-width W (that brand exists only while the sidebar is collapsed: the open drawer shows its own in ".drawer-header"); click the button named exactly "Expand sidebar" (in the page's header). Perform the steps in order. 1. Navigate to /c/{{CONV_ID}}. 2. ".status-ready" and ".status-readout-cwd" should be visible. 3. There should be exactly 2 git buttons. 4. The title's width must be greater than 1. 5. Eval (() => { const h = document.querySelector(".header").getBoundingClientRect(); const a = document.querySelector(".header-actions").getBoundingClientRect(); sessionStorage.setItem("taskHeader", JSON.stringify({ height: h.height, right: a.x + a.width })); return "saved"; })(), expecting "saved" (the task's header). 6. Check the brand rule "1px". 7. Hover the first element with data-testid "message" containing "Hello". 8. Its button named "Copy" should be visible. 9. Navigate to /. 10. The title should have exactly the text "Assistant", and the class "sr-only" (it stays a heading for screen readers). 11. The title's width must be at most 1 (the title row shows no "Assistant"). 12. Eval this; it must return true (the header keeps its height and right-aligned actions): (() => { const saved = JSON.parse(sessionStorage.getItem("taskHeader")); const h = document.querySelector(".header").getBoundingClientRect(); const a = document.querySelector(".header-actions").getBoundingClientRect(); return h.height === saved.height && a.x + a.width === saved.right; })(). 13. Check the brand rule "0px" (with the drawer collapsed, the logo has no title to rule off). 14. ".context-usage-label" should be visible. 15. There must be exactly 0 ".status-ready" elements and exactly 0 matching ".status-readout-cwd, .status-field-cwd". 16. The button named exactly "Terminal" in ".chat-workspace-actions" should be visible. 17. There must be exactly 0 git buttons. 18. Call "text the Assistant". 19. "The texted message" is the element with data-testid "message" containing "`+text+`": hover it, then click it. 20. The texted message's element with data-testid "message-via-text" should be visible. 21. There must be exactly 0 ".message-action-bar" elements. 22. Set the viewport to width 390, height 800 (only width and height). 23. Click the button named exactly "More options". 24. ".overflow-toolbar-action[aria-label='Terminal']" should be visible. 25. There must be exactly 0 elements matching ".overflow-toolbar-action[aria-label='Diffs'], .overflow-toolbar-action[aria-label='Git Graph']". 26. The ".overflow-menu-item" containing "Change Language" should be visible. 27. There must be exactly 0 ".overflow-menu-item" elements containing "Directory".`, func(s *shelley) {
		s.flag("workspace", true)
		s.viewport(1280, 800)
		s.v("CONV_ID", s.conversation("Hello", "").ID)
		s.call("text the Assistant", func() string {
			s.shelleyAPI("POST", "/api/debug/channels/debug-e2e/receive", map[string]any{"text": text}, nil)
			return "texted"
		})
	})
}

func TestE2EWorkspaceTaskTitleRow(t *testing.T) {
	const name = "Header tasks"
	e2e(t, useful(0), `A task's title row holds its page tabs and where it runs; actions get the only other row. The server runs in workspace mode as host vm.example, whose menu links include its website "vm.example". The project "`+name+`" has an action "Ship it" and the task {{TASK}}, whose agent linked four pages, "Page 1" to "Page 4". The workspace the page loads says the task works on a shared VM "exe-hub" (integration "hub", ready). Call "start a bare task" makes a project "`+name+` bare" on this host without actions, and starts its task {{PLAIN}}. "The header" is ".header"; "the place" is the header's element with data-testid "conversation-place", whose accessible name is its aria-label. Perform the steps in order. 1. Navigate to /c/{{TASK}}. 2. The header should come to hold exactly 5 elements with data-testid "page-tab" (its VM's site and the pages the agent linked). 3. The header must have exactly 0 buttons named "All tabs". 4. The place's aria-label should be exactly "Shared VM exe-hub · connected". 5. Click the place. 6. ".conversation-place-popover" ("the details") should contain "Works on shared VM exe-hub". 7. The details' element with role "status" should have exactly the text "connected". 8. The details' link whose accessible name contains "exe-hub" should have an href of exactly "https://exe-hub.exe.xyz/". 9. Press "Escape". 10. The button named exactly "Ship it" in ".project-header" should be visible (the project's actions are its second row). 11. ".project-header" must not contain "exe-hub". 12. The header must hold exactly 0 links named "vm.example", hidden or not (tabs show the pages; the host's link stays in the menu). 13. The header must hold exactly 0 ".chat-workspace-divider" elements. 14. Click the header's button named exactly "More options". 15. The ".overflow-menu-item" containing "vm.example" should be visible. 16. Press "Escape". 17. Set the viewport to width 390, height 800 (only width and height). 18. The place should come to be entirely inside the viewport (its getBoundingClientRect() within 0..innerWidth and 0..innerHeight): on a phone the title gives way. 19. Click the header's button named "All tabs" (tabs that don't fit are in a menu). 20. The menu items (role "menuitem") in the element with data-testid "page-tabs-menu" should have exactly the texts "Chat", "exe-hub", "Page 1", "Page 2", "Page 3", "Page 4", in that order. 21. Press "Escape". 22. Set the viewport to width 1280, height 800 (only width and height). 23. Call "start a bare task". 24. Navigate to /c/{{PLAIN}}. 25. The place's aria-label should come to start with "This host, " (a task on this host without project machinery has one row). 26. There must be exactly 0 ".project-header" elements. 27. Navigate to /. 28. "h1.header-title" should have exactly the text "Assistant". 29. There must be exactly 0 elements with data-testid "conversation-place" and exactly 0 ".project-header" elements.`, func(s *shelley) {
		s.flag("workspace", true)
		s.viewport(1280, 800)
		s.shelleyAPI("POST", "/api/projects", map[string]any{
			"name": name, "instructions": "", "cwd": s.cwd,
			"actions": []map[string]any{{"id": "ship", "label": "Ship it", "command": "true"}},
		}, nil)
		links := ""
		for n := 1; n <= 4; n++ {
			if n > 1 {
				links += " "
			}
			links += fmt.Sprintf(`[Page %d](https://page%d.example.com/ "tab")`, n, n)
		}
		task := s.workspaceTask(name, "Header task", "echo: See "+links+".")
		s.v("TASK", task)
		s.serverLinks(true)
		s.route("GET /api/workspace", func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := s.fetchJSON(r, &body); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			vms, _ := body["vms"].([]any)
			body["vms"] = append(vms, map[string]any{
				"conversation_id": task, "vm_name": "exe-hub", "base_vm": "", "integration": "hub",
				"shared": true, "status": "ready", "error": "", "ssh_dest": "", "https_url": "",
				"dir": "/home/exedev/hub", "conversation_archived": false, "replaced": false,
			})
			writeJSON(w, body)
		})
		s.call("start a bare task", func() string {
			bare := name + " bare"
			s.shelleyAPI("POST", "/api/projects", map[string]any{"name": bare, "instructions": "", "cwd": s.cwd}, nil)
			s.v("PLAIN", s.workspaceTask(bare, "Bare task", "Hello"))
			return "started"
		})
	})
}

func TestE2EWorkspaceTaskMovesMachine(t *testing.T) {
	const dest = "nobody@shelley-e2e.invalid"
	e2e(t, useful(0), `A task moves to another machine from where it runs, and back. The server runs in workspace mode, with the project "Mover tasks" and its task {{TASK}}, whose first message is "Hello". "The place" is the element with data-testid "conversation-place" in ".header"; its accessible name is its aria-label. "The dialog" is the dialog (role "dialog") whose accessible name contains "Move task to". Perform the steps in order. 1. Navigate to /c/{{TASK}}. 2. The place's aria-label should come to start with "This host". 3. An element whose text is exactly "Hello" should be visible. 4. There should come to be exactly 0 elements with data-testid "agent-thinking". 5. Click the place. 6. Click the element with data-testid "conversation-place-move". 7. Click the dialog's text "An existing machine". 8. If the dialog has a select "#machine-via", select its option with the value "" (no integration). 9. Fill the dialog's field labelled "SSH destination" with "`+dest+`". 10. Fill its field labelled "Directory" with "/tmp". 11. Click its element with data-testid "machine-save". 12. There should come to be exactly 0 dialogs. 13. The place's aria-label should come to start with "Shared VM `+dest+`". 14. The last element with data-testid "message-cwdchange" should contain "`+dest+`" (the agent is told, as a status line). 15. Click the place. 16. Click the element with data-testid "conversation-place-move". 17. Click the dialog's text "This Shelley host". 18. Click its element with data-testid "machine-save". 19. There should come to be exactly 0 dialogs. 20. The place's aria-label should come to start with "This host".`, func(s *shelley) {
		s.flag("workspace", true)
		s.shelleyAPI("POST", "/api/projects", map[string]any{"name": "Mover tasks", "instructions": "", "cwd": s.cwd}, nil)
		s.v("TASK", s.workspaceTask("Mover tasks", "Mover task", "Hello"))
	})
}

func TestE2EWorkspaceSidebarRowsStartShort(t *testing.T) {
	e2e(t, useful(0), `Workspace mode's sidebar rows start short, their title alone. The server runs in workspace mode, with the task {{TASK}} (a fork of a conversation that can message the user), whose Shelley build is installed. Call "deliver" has its agent message the user "Ready" and end its turn. "The row" is ".conversation-item[data-conversation-id=\"{{TASK}}\"]", "the title" is its ".conversation-title", and "the assistant row" is the element with data-testid "assistant-row". `+workspaceHow+`Perform the steps in order. 1. Navigate to /. 2. Open the drawer. 3. The title should be visible. 4. The title's computed font-weight must not be "700". 5. Call "deliver". 6. The title's computed font-weight should come to be "700" (unread, its title is bold, like an unread email). 7. The row must hold exactly 0 elements whose text is exactly "1" (no count). 8. The row's ".conversation-time" should have exactly the text "now". 9. The row's button named exactly "Archive" must be hidden (a touch screen shows it only on the open row). 10. The row must hold exactly 0 buttons whose name contains "Actions". 11. The row's element with role "status" named "Shelley build" should have exactly the text "Installed" (a task's build status is a label on its title line). 12. The row must hold exactly 0 elements matching ".conversation-preview, .conversation-meta". 13. The assistant row must hold exactly 0 ".conversation-preview" elements. 14. Eval (() => { window.__shortRow = document.querySelector('.conversation-item[data-conversation-id="{{TASK}}"]').getBoundingClientRect().height; return "saved"; })(), expecting "saved". 15. Eval this; it must return true (titles line up with their project's name, past its chevron): the x of the title's getBoundingClientRect() differs by less than 1 from the x of the ".conversation-group-label" in the ".project-group" that contains the row. 16. Click the button named "View options". 17. In the group (role "group") named "Row size", click the button named "Full" (view options make them full). 18. The row should come to hold exactly 1 ".conversation-preview". 19. The title's computed font-weight should be "700". 20. The row should hold exactly 1 ".conversation-meta". 21. The assistant row should hold exactly 1 ".conversation-preview". 22. Eval this; it must return true: the row's getBoundingClientRect().height > window.__shortRow * 1.5.`, func(s *shelley) {
		s.flag("workspace", true)
		source := s.conversationWith(map[string]any{
			"message": "Hello", "model": "predictable", "cwd": s.cwd,
			"conversation_options": map[string]any{"tool_overrides": map[string]any{"message_user": "on"}},
		}, "")
		var fork struct {
			ID string `json:"conversation_id"`
		}
		s.api("POST", "/api/conversation/"+source.ID+"/fork", map[string]any{}, &fork)
		s.v("TASK", fork.ID)
		s.shelleyAPI("POST", "/api/conversation/"+fork.ID+"/customization", map[string]any{"status": "installed", "commit": "1"}, nil)
		s.call("deliver", func() string {
			s.api("POST", "/api/conversation/"+fork.ID+"/chat", map[string]any{"message": `message_user: {"text":"Ready","end_turn":true}`, "model": "predictable"}, nil)
			return "delivered"
		})
	})
}

// The row-turns-bold spec ran in workspace mode and in stock Shelley.
func TestE2EWorkspaceRowBoldOnceAgentStopsWorkspace(t *testing.T) {
	workspaceRowBold(t, useful(0), true)
}

func TestE2EWorkspaceRowBoldOnceAgentStopsStock(t *testing.T) {
	workspaceRowBold(t, useful(0), false)
}

func workspaceRowBold(t *testing.T, useful lazycue.Useful, workspace bool) {
	mode, start, open := "stock Shelley", "/new", ""
	if workspace {
		mode, start, open = "workspace mode", "/", workspaceHow
	}
	steps := `1. Navigate to ` + start + `. `
	n := 2
	if workspace {
		steps += `2. Open the drawer. `
		n = 3
	}
	for i, step := range []string{
		`The row should be visible.`,
		`Call "ready".`,
		`The bold row should become visible.`,
		`The title's computed font-weight should be "700".`,
		`Call "delay".`,
		`The row's ".working-indicator" should become visible.`,
		`There should be exactly 0 bold rows.`,
		`The title's computed font-weight must not be "700".`,
		`The row should come to hold exactly 0 ".working-indicator" elements (allow up to 15s).`,
		`The bold row should be visible.`,
		`The title's computed font-weight should be "700".`,
		`Click the row.`,
		`The row should come to have aria-current "page".`,
		`There should be exactly 0 bold rows.`,
		`The title's computed font-weight must not be "700".`,
	} {
		steps += fmt.Sprintf("%d. %s ", n+i, step)
	}
	e2e(t, useful, `A row turns bold, unread, only once its agent stops working in `+mode+`. The server runs in `+mode+`, with the conversation {{CONV_ID}}, which can message the user. Call "ready" has its agent message the user "Ready" and end its turn; call "delay" sends it a message its agent works on for 3 seconds. "The row" is ".conversation-item[data-conversation-id=\"{{CONV_ID}}\"]", "the bold row" is ".conversation-item-unread[data-conversation-id=\"{{CONV_ID}}\"]", and "the title" is the row's ".conversation-title". `+open+`Perform the steps in order. `+steps, func(s *shelley) {
		s.flag("workspace", workspace)
		if !workspace {
			s.viewport(1280, 900)
		}
		c := s.conversationWith(map[string]any{
			"message": "Hello", "model": "predictable", "cwd": s.cwd,
			"conversation_options": map[string]any{"tool_overrides": map[string]any{"message_user": "on"}},
		}, "")
		s.v("CONV_ID", c.ID)
		chat := func(message string) {
			s.api("POST", "/api/conversation/"+c.ID+"/chat", map[string]any{"message": message, "model": "predictable"}, nil)
		}
		s.call("ready", func() string {
			chat(`message_user: {"text":"Ready","end_turn":true}`)
			return "sent"
		})
		s.call("delay", func() string {
			chat("delay: 3")
			return "sent"
		})
	})
}

func TestE2EWorkspaceOpeningChatClearsBoldAcrossBrowsers(t *testing.T) {
	e2e(t, useful(0), `Opening a chat clears its bold title across browsers and keeps the open row quiet. The server runs in workspace mode, with the conversation {{CONV_ID}}. Call "reply" sends it a message its agent answers; call "another" starts another conversation, {{OTHER}}, and waits for its reply. The page's first read receipt for {{CONV_ID}} (a request to /api/conversation/{{CONV_ID}}/read) is held at the server until call "release"; call "read requested" says whether it has arrived ("true" or "false"). Besides the main tab there is a second browser window, the tab named "observer", a desktop: steps say which tab they act on, and the rest act on the main tab. "The row" is "[data-conversation-id=\"{{CONV_ID}}\"]" and "the bold row" is ".conversation-item-unread[data-conversation-id=\"{{CONV_ID}}\"]". Perform the steps in order. 1. Open the observer tab by navigating it to /new. 2. Set the observer tab's viewport to width 1280, height 900, scale 1, not mobile, no touch. 3. Navigate the observer tab to /new again, so it loads as a desktop. 4. In the observer tab, the row should be visible. 5. Set the main tab's viewport to width 1280, height 900 (only width and height). 6. Navigate the main tab to /new. 7. In the main tab, the row should be visible. 8. Call "reply". 9. In the observer tab, the bold row should become visible. 10. In the main tab, the bold row should become visible. 11. In the main tab, click the row. 12. Call "read requested" until it returns "true" (the receipt is now held). 13. In the main tab, the row should have aria-current "page". 14. In the main tab, there should be exactly 0 bold rows. 15. In the observer tab, the bold row should still be visible. 16. Call "release". 17. In the observer tab, there should come to be exactly 0 bold rows. 18. Call "another". 19. Reload the observer tab by navigating it to /new. 20. In the observer tab, ".conversation-item-unread[data-conversation-id=\"{{OTHER}}\"]" should be visible. 21. In the observer tab, the row should be visible. 22. In the observer tab, there must be exactly 0 bold rows.`, func(s *shelley) {
		s.flag("workspace", true)
		c := s.conversation("echo: first visit", "")
		s.v("CONV_ID", c.ID)
		requested := s.signal("read requested")
		release := s.gate("release")
		var held atomic.Bool
		s.route("/api/conversation/"+c.ID+"/read", func(w http.ResponseWriter, r *http.Request) {
			if held.CompareAndSwap(false, true) {
				requested.Store(true)
				if !release.wait(r) {
					return
				}
			}
			s.pass.ServeHTTP(w, r)
		})
		s.call("reply", func() string {
			s.api("POST", "/api/conversation/"+c.ID+"/chat", map[string]any{"message": "echo: ready for review", "model": "predictable"}, nil)
			return "sent"
		})
		s.call("another", func() string {
			s.v("OTHER", s.conversation("echo: another unread chat", "").ID)
			return "started"
		})
	})
}

// The spec's "with a mouse" tests ran on a 1280x800 desktop without touch.
var workspaceMouse = lazycue.Device{Width: 1280, Height: 800, Scale: 1}

func TestE2EWorkspaceWorkingDotInTimesPlace(t *testing.T) {
	e2e(t, useful(0), `With a mouse, a working task's dot sits in the time's place; Archive pushes it left. The server runs in workspace mode, with the idle conversation {{IDLE}} and the conversation {{WORKING}}, whose agent is busy running a command for a minute. "The row of" X is ".conversation-item[data-conversation-id=\"X\"]"; "the time" is the row of {{IDLE}}'s ".conversation-time"; "the dot" is the row of {{WORKING}}'s element with data-testid "drawer-working-indicator"; "Archive" is the row of {{WORKING}}'s button named exactly "Archive". Read boxes with getBoundingClientRect(); an element's right is x + width. `+workspaceHow+`Perform the steps in order. 1. Navigate to /. 2. Open the drawer. 3. The time should be visible. 4. The dot should be visible. 5. The row of {{WORKING}} must hold exactly 0 ".conversation-time" elements. 6. Eval this; it must return true (right-aligned where the times are): Math.abs(the dot's right - the time's right) < 2. 7. Hover the row of {{WORKING}}. 8. Archive's computed opacity should come to not be "0". 9. The dot's computed opacity should be "1" (still shown). 10. Eval this until it returns true (left of Archive): the dot's right <= Archive's x.`, func(s *shelley) {
		s.flag("workspace", true)
		s.device(workspaceMouse)
		s.v("IDLE", s.conversation("echo: idle", "").ID)
		var working struct {
			ID string `json:"conversation_id"`
		}
		s.api("POST", "/api/conversations/new", map[string]any{"message": "bash: sleep 60", "model": "predictable", "cwd": s.cwd}, &working)
		s.v("WORKING", working.ID)
	})
}

func TestE2EWorkspaceArchiveTakesTimesPlace(t *testing.T) {
	e2e(t, useful(0), `With a mouse, a task row's Archive takes its time's place on hover or keyboard focus; no menu. The server runs in workspace mode, with the conversation {{CONV_ID}}. Call "archived" says whether the server has it archived ("true" or "false"). "The row" is ".conversation-item[data-conversation-id=\"{{CONV_ID}}\"]"; "the time" is its ".conversation-time"; "Archive" is its button named exactly "Archive". Read boxes with getBoundingClientRect(); an element's right is x + width. `+workspaceHow+`Perform the steps in order. 1. Navigate to /. 2. Open the drawer. 3. The time's computed opacity should be "1", and Archive's "0". 4. Eval (() => { const b = document.querySelector('.conversation-item[data-conversation-id="{{CONV_ID}}"] .conversation-time').getBoundingClientRect(); window.__timeBox = { x: b.x, right: b.x + b.width }; return "saved"; })(), expecting "saved". 5. Hover the row. 6. The time's computed opacity should come to be "0", and Archive's to not be "0". 7. Eval this; it must return true (Archive sits where the time was): Archive's x < window.__timeBox.right && Archive's right > window.__timeBox.x. 8. The row must hold exactly 0 buttons whose name contains "Actions" (no ⋯). 9. Right-click the row. 10. The element with data-testid "conversation-context-menu" must be hidden (a right-click leaves the browser's own menu). 11. Move the mouse to the viewport point (0, 0) (a hover step with no selector, at x 0, y 0). 12. Archive's computed opacity should come to be "0", and the time's "1". 13. Eval the row's focus(). 14. Press "Tab". 15. Archive should be focused (document.activeElement is it): from the row, Tab reaches it. 16. Press "Tab" with the Shift modifier. 17. The row should be focused. 18. Archive's computed opacity must not be "0" (back on the row, it stays). 19. Click Archive. 20. There should come to be exactly 0 rows. 21. Call "archived": it must return "true".`, func(s *shelley) {
		s.flag("workspace", true)
		s.device(workspaceMouse)
		c := s.conversation("Hello", "")
		s.v("CONV_ID", c.ID)
		s.call("archived", func() string {
			var got struct {
				Conversation struct {
					Archived bool `json:"archived"`
				} `json:"conversation"`
			}
			s.api("GET", "/api/conversation/"+c.ID, nil, &got)
			return strconv.FormatBool(got.Conversation.Archived)
		})
	})
}
