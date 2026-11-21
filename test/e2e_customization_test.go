package test

import "testing"

// Ported from ui/e2e/customization.spec.ts.

// customizationSetup turns on workspace mode with full drawer rows, so the
// drawer shows each task's whole build line, and makes calls that report
// steps of the task's install-custom run, as the script does.
func customizationSetup(s *shelley) convo {
	s.flag("workspace", true)
	s.initScript(`localStorage.setItem("shelley-row-size", "full")`)
	c := s.conversation("Hello", "")
	s.v("ID", c.ID)
	report := func(status, error string) func() string {
		return func() string {
			s.do("POST", "/api/conversation/"+c.ID+"/customization", map[string]string{"status": status, "commit": "1234567890abcdef", "error": error})
			return "reported"
		}
	}
	s.call("verifying", report("verifying", ""))
	s.call("failed", report("failed", "lint failed; see the log"))
	s.call("installed", report("installed", ""))
	return c
}

// customizationOpenWorkspace says how a description opens the task drawer
// on the phone, where it is off-canvas.
const customizationOpenWorkspace = `To "open the workspace": click the button named exactly "Open tasks" once it is visible; then the element with data-testid "assistant-row" should be in the viewport. `

func TestE2ECustomizationRowFollowsInstallStream(t *testing.T) {
	e2e(t, `A task's install-custom run shows on its row as the stream sends it. Workspace mode is on, the screen is the default 393x727 phone, and drawer rows are full size. `+customizationOpenWorkspace+`Calls "verifying", "failed" (with the error "lint failed; see the log") and "installed" each report that step of the task's run to the server, as the install script does. Name elements by ARIA role and accessible name, as Playwright's getByRole does. "The build line" is the element matched by the selector .conversation-item[data-conversation-id="{{ID}}"] + section[aria-label="Shelley build"]; "its status" is the element with role "status" inside it; "the restart button" is the button inside it named exactly "Restart Shelley". Perform the steps in order. 1. Navigate to / and open the workspace. 2. There is no build line yet (count 0). 3. Call "verifying". Its status should have exactly the text "Verifying…", and there is no restart button (count 0). 4. Call "failed". Its status should have exactly the text "Failed", the build line should contain the text "lint failed; see the log", and there is no restart button (count 0). 5. Call "installed". Its status should have exactly the text "Installed", the build line must not contain the text "12345678" (no commit hash), and the restart button should be visible. 6. A reload gets the states when the stream connects: reload the page, open the workspace again, and its status should have exactly the text "Installed".`, func(s *shelley) {
		customizationSetup(s)
	})
}

func TestE2ECustomizationShortRowBadge(t *testing.T) {
	e2e(t, `Short drawer rows show a task's build as a badge. Workspace mode is on and the screen is the default 393x727 phone; the task's install-custom run has reported "verifying" already. `+customizationOpenWorkspace+`Call "installed" reports that the run installed, as the install script does. Name elements by ARIA role and accessible name, as Playwright's getByRole does. "The badge" is the element with role "status" and name exactly "Shelley build" inside .conversation-item[data-conversation-id="{{ID}}"]. Perform the steps in order. 1. Navigate to / and open the workspace. 2. Click the button whose name contains "View options", then, inside the element with role "group" named "Row size", the button whose name contains "Short". 3. The badge should have exactly the text "Verifying…". 4. Call "installed": the badge should then have exactly the text "Installed".`, func(s *shelley) {
		c := customizationSetup(s)
		s.do("POST", "/api/conversation/"+c.ID+"/customization", map[string]string{"status": "verifying", "commit": "1234567890abcdef", "error": ""})
	})
}
