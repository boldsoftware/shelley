package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
)

// Ported from ui/e2e/project-grouping.spec.ts.

// How the spec drives a project's settings.
const projectGroupingEditor = `"The editor" is the region named exactly "Edit project"; its CSS selector is "section[aria-label='Edit project']". To "open the settings of P": click "section[aria-label='P'] a.project-select" (in the drawer), then click {"selector": "[data-testid='project-view'] nav", "role": "button", "name": "Settings", "exact": true}. The editor's radios are {"selector": "section[aria-label='Edit project']", "role": "radio", "name": "..."} (name a substring): "This Shelley host" is input[name='project-vm-mode'][value='host'], "One shared machine" is the one with value shared, and "Their own VMs" the one with value own; whether one "is checked" is an eval of its .checked (polled with expect), and to "choose" one, click it and then check that it is checked. The editor's fields, by the exact text of their <label>: "Working directory" is #project-cwd, "Reached through" is the select #project-vm-via, "VM" and "SSH destination" are both #project-vm-machine (the label says which), "Base VM" is #project-vm-base and "exe.dev integration" is #project-vm-integration. "The field L is visible" means: "section[aria-label='Edit project'] label" with has_text L and "exact": true is visible, and so is the field. "There is no field L" means: assert_count 0 of "section[aria-label='Edit project'] label" with has_text L and "exact": true. "The Automanage checkbox" is {"selector": "section[aria-label='Edit project']", "role": "checkbox", "name": "Automanage"}; to tick or untick it, click it and then check its .checked with an eval of document.querySelector("section[aria-label='Edit project'] label.project-checkbox input").checked (it is the first such checkbox). "The text T is visible" means: assert_visible of "section[aria-label='Edit project'] *" with has_text T. `

func TestE2EProjectGroupingStockMode(t *testing.T) {
	e2e(t, `stock mode offers Group by Project without the workspace. Project {{NAME}} runs in the server's directory. Conversation {{PROJECT_CONV}} was started in it, and {{PLAIN}} in no project. Call "integrations-none" has the browser told there are no exe.dev integrations, and call "integrations-exe" that there is one, exe; before either the server answers. Call "saved-vm" returns the base, integration, managed_base_vm and shared_vm of the vm the last project save (a PUT to /api/projects/...) sent, or "none". `+projectGroupingEditor+drawerDirect+drawerSteps+`1. Navigate to /. 2. The URL contains "/c/" (assert_url with text). 3. Click the button "Open conversations". 4. There is no data-testid "assistant-row", no data-testid "nav-tab-dev" and no ".project-group" (assert_count 0 each). 5. Click the button "View options". 6. Click {"selector": ".group-by-menu", "role": "button", "name": "Project", "exact": true}. 7. ".project-group[aria-label='{{NAME}}'] [data-conversation-id='{{PROJECT_CONV}}']" is visible. 8. The region named exactly "No project" is visible (role region), and "section[aria-label='No project'] [data-conversation-id='{{PLAIN}}']" is visible. 9. Click the button "New conversation in {{NAME}}". 10. Data-testid "message-input" is visible; there is no ".project-picker"; ".status-field-cwd" is visible. 11. Navigate to /. 12. Click the button "Open conversations". 13. Reload the page (reload action). 14. Click the button "Open conversations". 15. ".project-group[aria-label='{{NAME}}'] [data-conversation-id='{{PROJECT_CONV}}']" is visible (Project grouping is remembered). 16. Call "integrations-none". 17. Open the settings of {{NAME}}. 18. The field "Working directory" is visible. 19. The radio "This Shelley host" is checked. 20. There is no radio "Their own VMs" in the editor (assert_count 0 by role and name). 21. Choose the radio "One shared machine". 22. There is no field "Reached through" and no field "exe.dev integration". 23. Fill the field "SSH destination" with "me@devbox" (check its label first). 24. Go back (go_back). 25. Click the button "Open conversations". 26. Call "integrations-exe". 27. Open the settings of {{NAME}}. 28. The radio "This Shelley host" is checked. 29. No element in the editor contains the text "Recommended": assert_count 0 of "section[aria-label='Edit project'] *" with has_text "Recommended". 30. Choose the radio "One shared machine". 31. The field "Reached through" is visible, and its value is exactly "exe" (assert_value). 32. The field "VM" is visible. 33. In the field "Reached through", pick the option labelled "ssh, with this host's SSH configuration", whose value is "": fill the select with value "", then check that the chosen option reads that label: eval with expect "ssh, with this host's SSH configuration" of document.querySelector("#project-vm-via").selectedOptions[0].textContent.trim(). 34. The field "SSH destination" is visible. 35. Choose the radio "Their own VMs". 36. The value of the field "Base VM" is exactly ""; its placeholder contains "Optional" (assert_count 1 of "#project-vm-base[placeholder*='Optional']"). 37. The text "starts on a new, empty VM" is visible. 38. Fill the field "Base VM" with "my-base". 39. The text "a copy of this existing VM" is visible. 40. Tick the Automanage checkbox. 41. The field "Base VM" has the attribute readonly with value "" (assert_attribute), its value is exactly "", and its placeholder contains "setup task" (assert_count 1 of "#project-vm-base[placeholder*='setup task']"). 42. The text "Saving starts a setup" is visible. 43. Untick the Automanage checkbox. 44. The value of the field "Base VM" is exactly "". 45. Click {"selector": "section[aria-label='Edit project']", "role": "button", "name": "Save project", "exact": true}. 46. Call "saved-vm" with expect '{"base":"","integration":"exe","managed_base_vm":false,"shared_vm":""}'.`, func(s *shelley) {
		name := "Stock 4f2a9c1e"
		s.v("NAME", name)
		s.api("POST", "/api/projects", map[string]any{"name": name, "instructions": "", "cwd": s.cwd, "actions": []any{}}, nil)
		var created struct {
			ID string `json:"conversation_id"`
		}
		s.api("POST", "/api/conversations/new", map[string]any{"project": name, "message": "echo: in a project"}, &created)
		s.v("PROJECT_CONV", created.ID)
		plain := s.conversation("echo: in no project", "")
		s.v("PLAIN", plain.ID)

		var mu sync.Mutex
		var integrations []string // nil: the server answers
		savedVM := "none"
		set := func(names ...string) func() string {
			return func() string {
				mu.Lock()
				defer mu.Unlock()
				integrations = append([]string{}, names...)
				return mustJSON(integrations)
			}
		}
		s.call("integrations-none", set())
		s.call("integrations-exe", set("exe"))
		s.route("/api/exe-dev-integrations", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			names := integrations
			mu.Unlock()
			if names == nil {
				s.pass.ServeHTTP(w, r)
				return
			}
			defaultName := ""
			if len(names) > 0 {
				defaultName = names[0]
			}
			writeJSON(w, map[string]any{"integrations": names, "default": defaultName})
		})
		s.route("PUT /api/projects/", func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var project struct {
				VM map[string]any `json:"vm"`
			}
			got := "unparsable: " + string(body)
			if json.Unmarshal(body, &project) == nil {
				picked := map[string]any{}
				for _, k := range []string{"base", "integration", "managed_base_vm", "shared_vm"} {
					picked[k] = project.VM[k]
				}
				got = mustJSON(picked)
			}
			mu.Lock()
			savedVM = got
			mu.Unlock()
			s.pass.ServeHTTP(w, r)
		})
		s.call("saved-vm", func() string {
			mu.Lock()
			defer mu.Unlock()
			return savedVM
		})
	})
}
