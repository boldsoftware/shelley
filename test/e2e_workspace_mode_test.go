package test

import (
	"net/http"
	"testing"
)

// Ported from ui/e2e/workspace-mode.spec.ts.

// workspaceLoad warns the agent about a fresh server's first load in
// workspace mode, which creates the Assistant.
const workspaceLoad = `On a fresh server the first load of a page takes a few seconds while the Assistant is created (the header title reads "Shelley" until then): wait for elements rather than reading the page right after navigating. On a phone-sized screen the drawer is off-canvas until the header's "Open tasks" button is clicked. `

// Workspace mode (the "workspace" feature flag): a pinned Assistant, the
// drawer always grouped by project, and task wording.

// A grouping chosen outside workspace mode doesn't apply in it.
func TestE2EWorkspaceModePinsAssistantGroupsByProject(t *testing.T) {
	e2e(t, useful(0), `Workspace mode pins the Assistant and always groups by project. The server runs in workspace mode, and the page's localStorage has "shelley-group-by" set to "none" (a grouping chosen outside workspace mode). `+workspaceLoad+`Perform the steps in order. 1. Navigate to /. 2. "h1.header-title" should have exactly the text "Assistant". 3. Click the button named exactly "Open tasks". 4. The element with data-testid "assistant-row" should be visible. 5. There must be exactly 0 elements with data-testid "nav-tab-dev". 6. The region (role "region") named exactly "Self" should be visible. 7. Click the button named "View options". 8. The group (role "group") named "Row size" should be visible. 9. There must be exactly 0 buttons named "No grouping".`, func(s *shelley) {
		s.flag("workspace", true)
		s.initScript(`localStorage.setItem("shelley-group-by", "none");`)
	})
}

func TestE2EWorkspaceModeNoUserFilterSeed(t *testing.T) {
	e2e(t, useful(0), `Workspace mode doesn't seed a user: filter that hides API-started tasks. The server runs in workspace mode; the browser is signed in as me@example.com. A project has two tasks: {{SHARED}}, in which several people (me@example.com and colleague@example.com) wrote, which would seed a "user:<me>" search filter in stock Shelley, and {{API_STARTED}}, started through the API, with no human author. The conversation list the page loads says so, and the page's live stream is cut off so that it keeps that list. `+workspaceLoad+`Perform the steps in order. 1. Navigate to /. 2. Click the button named exactly "Open tasks". 3. The element [data-conversation-id="{{SHARED}}"] should be visible. 4. The element [data-conversation-id="{{API_STARTED}}"] should be visible. 5. The button named "Search tasks..." must not have the class "search-toggle-active" (no search filter was seeded).`, func(s *shelley) {
		s.flag("workspace", true)
		const project = "seed-project"
		s.shelleyAPI("POST", "/api/projects", map[string]any{"name": project, "instructions": "", "cwd": s.cwd}, nil)
		var ids []string
		for _, message := range []string{"echo: shared", "echo: api started"} {
			var created struct {
				ID string `json:"conversation_id"`
			}
			s.shelleyAPI("POST", "/api/conversations/new", map[string]any{"project": project, "message": message}, &created)
			ids = append(ids, created.ID)
		}
		shared, apiStarted := ids[0], ids[1]
		s.v("SHARED", shared)
		s.v("API_STARTED", apiStarted)
		s.header("X-ExeDev-Email", "me@example.com")
		s.route("GET /api/conversations/snapshot", func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := s.fetchJSON(r, &body); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			conversations, ok := body["conversations"].([]any)
			if !ok {
				// Failing beats passing an unpatched list through, which
				// would leave the test checking nothing.
				http.Error(w, "snapshot: conversations is not a list", http.StatusInternalServerError)
				return
			}
			for _, c := range conversations {
				c := c.(map[string]any)
				if c["conversation_id"] == shared {
					c["participants"] = []map[string]any{
						{"email": "me@example.com", "message_count": 1},
						{"email": "colleague@example.com", "message_count": 1},
					}
				} else {
					c["participants"] = []any{}
				}
			}
			writeJSON(w, body)
		})
		// The stream would replace the patched list with the real one.
		s.route("/api/stream2", abort)
		s.route("/api/stream2/", abort)
	})
}
