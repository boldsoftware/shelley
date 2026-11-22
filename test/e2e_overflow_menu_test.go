package test

// Ported from ui/e2e/overflow-menu.spec.ts.

import (
	"encoding/json"
	"net/url"
	"strconv"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

func TestE2EOverflowMenuMobileNewPlusLeftOfCall(t *testing.T) {
	e2e(t, useful(0), `On a phone, the header's New button is a plain plus immediately left of the Call button. "The plus" is the button inside ".header" whose accessible name is exactly "New Conversation"; "the call button" is data-testid "live-call-header". Do the steps in order, and repeat steps 1-3 for each width W of 320, 390 and 1280 in turn: 1. Set the viewport to W x 800 (a set_viewport giving only width and height, so the phone keeps its touch screen and scale) and navigate to /c/{{SLUG}}. 2. The plus and the call button should both be visible. 3. Compare their bounding boxes. If W < 768: the plus's right edge (x + width) must be <= the call button's x; the gap (call x minus plus right edge) must be < 8; the plus's computed background-color must be exactly "rgba(0, 0, 0, 0)"; and the computed width of the svg inside the plus must be exactly "22px". Otherwise (W = 1280): the call button's right edge must be <= the plus's x. After the three widths: 4. Set the viewport to 390x800 (width and height only; no navigation) and click the plus. 5. The URL should become exactly /new: that path, with no query or hash.`, func(s *shelley) {
		s.v("SLUG", s.conversation("echo header controls", "").Slug)
	})
}

func TestE2EOverflowMenuMobileModelRowSavesReasoning(t *testing.T) {
	e2e(t, useful(0), `On a phone, the overflow menu's model row matches the Directory row, and the reasoning level chosen in it is saved to the live conversation. The screen is 390x800. `+menusOpenModelPickerHow+`Call "thinking" returns the thinking_level the server stored in the conversation's options. Perform the steps in order. 1. Navigate to /c/{{SLUG}} and open the model picker (through the overflow menu). 2. The picker must not have the class "p-disabled", and its "svg.chat-menu-icon" should be visible. 3. "The directory row" is the button in ".chat-overflow-popover" whose accessible name starts with "Directory". Compare the picker's and the directory row's bounding boxes: their heights must be exactly equal, their x exactly equal, their widths exactly equal, and the picker's bottom (y + height) must be <= the directory row's y. 4. Click the element with role "radio" and accessible name exactly "high" in ".model-picker-panel". 5. Call "thinking" until it returns "high". 6. The panel should still be visible. 7. Reload by navigating to /c/{{SLUG}} again and open the model picker. 8. The panel's radio named exactly "high" should have aria-checked="true".`, func(s *shelley) {
		s.viewport(390, 800)
		c := s.conversation("echo mobile model", "")
		s.v("SLUG", c.Slug)
		s.call("thinking", func() string {
			var got struct {
				Conversation struct {
					Options string `json:"conversation_options"`
				} `json:"conversation"`
			}
			s.api("GET", "/api/conversation/"+c.ID, nil, &got)
			var opts struct {
				Thinking string `json:"thinking_level"`
			}
			if got.Conversation.Options != "" {
				s.check(json.Unmarshal([]byte(got.Conversation.Options), &opts))
			}
			return opts.Thinking
		})
	})
}

// overflowMenuRowsEval lists the overflow menu's visible rows, as the spec did.
const overflowMenuRowsEval = `Array.from(document.querySelector('.overflow-menu-panel').children).filter((child) => child.checkVisibility()).map((child) => { if (child.classList.contains('overflow-menu-divider')) return 'separator'; if (child.classList.contains('overflow-quick-controls')) return 'preferences'; if (child.classList.contains('overflow-menu-toolbar')) return 'icons: ' + [...child.querySelectorAll('button')].map((b) => b.ariaLabel).join(', '); const row = child.cloneNode(true); row.querySelectorAll('.overflow-menu-shortcut, .overflow-menu-cwd, .overflow-menu-language, .version-menu-dot').forEach((extra) => extra.remove()); return row.textContent.trim().replace(/\s+/g, ' '); })`

func TestE2EOverflowMenuFourGroupsPhone(t *testing.T) {
	// Phones keep the header's workspace tools in a row of icons here, with
	// the menu's own that have keyboard shortcuts.
	e2e(t, useful(0), `At 393px wide, the overflow menu follows the requested four groups. The server presents itself as host vm.example with links "Manage on exe.dev", "vm.example" and "Documentation". 1. Navigate to /c/{{SLUG}} and click the button with aria-label exactly "More options". 2. ".overflow-menu-panel" should be visible. 3. This eval lists its visible rows: `+overflowMenuRowsEval+`. Its result must be exactly ["icons: Diffs, Git Graph, Terminal, Edit File…, Command menu, vm.example","Manage on exe.dev","Documentation","separator","Model","Directory","Export Conversation","Archive Conversation","separator","Edit User AGENTS.md","MCP Servers","separator","preferences","Change Language","Check for New Version"]. 4. There should be exactly 3 ".overflow-menu-divider" elements in the panel.`, func(s *shelley) {
		s.viewport(393, 851)
		s.serverLinks(true)
		s.v("SLUG", s.conversation("Hello", "").Slug)
	})
}

func TestE2EOverflowMenuFourGroupsDesktop(t *testing.T) {
	e2e(t, useful(0), `At 1280px wide, the overflow menu follows the requested four groups. The server presents itself as host vm.example with links "Manage on exe.dev", "vm.example" and "Documentation". 1. Navigate to /c/{{SLUG}} and click the button with aria-label exactly "More options". 2. ".overflow-menu-panel" should be visible. 3. This eval lists its visible rows: `+overflowMenuRowsEval+`. Its result must be exactly ["Command menu","Manage on exe.dev","Documentation","separator","Edit File…","Export Conversation","Archive Conversation","separator","Edit User AGENTS.md","MCP Servers","separator","preferences","Change Language","Check for New Version"]. 4. There should be exactly 3 ".overflow-menu-divider" elements in the panel.`, func(s *shelley) {
		s.viewport(1280, 851)
		s.serverLinks(true)
		s.v("SLUG", s.conversation("Hello", "").Slug)
	})
}

// The top-right overflow ("kebab") menu uses a PrimeVue Popover, compact
// native icon buttons, and the shared Modal for language selection. The DOM
// contract (.chat-overflow-menu-wrapper / .btn-icon / .overflow-menu-item) is
// covered by other specs (agents-md-vim).

func TestE2EOverflowMenuDirectoryItemMobile(t *testing.T) {
	e2e(t, useful(0), `On a phone (the default Pixel 5 touch screen), the overflow menu's Directory item opens the directory picker and closes the menu. To "tap" an element, do a touch start on it and then a touch end (not a click). Name elements by ARIA role and accessible name, as Playwright's getByRole does. "The trigger" is the button named "More options"; "the menu" is ".chat-overflow-popover"; "the directory item" is the menu's button whose name starts with "Directory" (role button, name "Directory" inside the menu: no other menu button's name contains that word); "the picker" is ".modal.directory-picker-modal". Perform the steps in order. 1. Navigate to /new and tap the trigger. 2. The menu and the directory item should be visible. 3. Navigate to /c/{{SLUG}}. 4. The picker should be hidden. Tap the trigger. 5. The menu and the directory item should be visible, and so should the menu's button named exactly "Command menu" (exact match) and its button whose name starts with "Terminal" (name "Terminal"). 6. The directory item's ".overflow-menu-cwd" should have exactly the text "{{CWD}}" and the title attribute exactly "{{CWD}}". 7. Tap the directory item. 8. The picker should be visible, the menu hidden, and the trigger should have aria-expanded="false". 9. Tap the picker's button named exactly "Cancel" (exact match): the picker should be hidden. 10. Tap the trigger: the directory item should be visible. 11. Set the viewport to 1280x800 (a set_viewport giving only width and height, so the phone keeps its touch screen and scale; do not navigate). 12. The menu should still be visible, and there must be no directory item at all (count 0).`, func(s *shelley) {
		s.v("SLUG", s.conversation("Hello", "").Slug)
	})
}

func TestE2EOverflowMenuCompactControls(t *testing.T) {
	e2e(t, useful(0), `The overflow menu popover opens and its compact controls work. The page's Notification API is fake: its permission starts "default", and requestPermission() denies it. "The trigger" is ".chat-overflow-menu-wrapper .btn-icon"; "the popover" is ".chat-overflow-popover". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. Set localStorage "shelley-theme" to "system" and "shelley-notification-prefs" to '{"channels":{"favicon":{"enabled":true},"browser":{"enabled":true}}}', then reload by navigating to /c/{{SLUG}} again. 3. The trigger should be visible; click it. 4. The popover should be visible. Every choice is visible and the active icon is emphasized: the popover should have exactly 3 ".overflow-quick-control", elements whose text is exactly "Brevity", "Look" and "Notifications" should be visible, and it should have exactly 3 ".overflow-choice-options", exactly 7 ".overflow-choice-option" and exactly 3 ".overflow-choice-option.is-selected". 5. In data-testid "theme-cycle", the button whose accessible name contains "System" should have aria-pressed="true". 6. In data-testid "notification-toggle" ("the notification options"), the button named "Enable Notifications" should have aria-pressed="true". 7. Click the button named "Disable Notifications": it should get aria-pressed="true", and the "Enable Notifications" button should be enabled. 8. Click the "Enable Notifications" button. 9. The notification options' button named "Blocked by browser" should be disabled, and JSON.parse(localStorage.getItem("shelley-notification-prefs") || "{}").channels?.browser?.enabled must be exactly false. 10. Click the theme-cycle button named "Light": the html element must not have the class "dark", and localStorage "shelley-theme" must be exactly "light". 11. Click the theme-cycle button named "Dark": the html element should have the class "dark", and localStorage "shelley-theme" must be exactly "dark". 12. The popover should still be visible.`, func(s *shelley) {
		s.initScript(`Object.defineProperty(window, "Notification", {
  configurable: true,
  value: class FakeNotification {
    static permission = "default";
    static requestPermission = async () => {
      FakeNotification.permission = "denied";
      return "denied";
    };
    close() {}
  },
});`)
		s.v("SLUG", s.conversation("Hello", "").Slug)
	})
}

// overflowMenuLanguageNames says how the language tests name elements.
const overflowMenuLanguageNames = `Name elements by ARIA role and accessible name, as Playwright's getByRole does: "named X" means the accessible name is exactly X, and "whose accessible name contains X" means it contains X. `

// overflowMenuLanguagePicker is the language action test at one viewport size.
// The description names the size, so that each size has a cached script of its
// own: a heal at one size can't then rewrite another's.
func overflowMenuLanguagePicker(t *testing.T, useful lazycue.Useful, width, height int) {
	w, h := strconv.Itoa(width), strconv.Itoa(height)
	e2e(t, useful, `The overflow menu's language action opens a compact picker. The screen is `+w+`x`+h+`. "The trigger" is ".chat-overflow-menu-wrapper .btn-icon"; "the popover" is ".chat-overflow-popover"; "the dialog" is the element with role="dialog" that contains a ".language-picker-modal" ("[role=dialog]:has(.language-picker-modal)"). `+overflowMenuLanguageNames+`Perform the steps in order. 1. Navigate to /new and click the trigger. 2. "The action" is the popover's button whose accessible name contains "Change Language". Its class attribute must be exactly "overflow-menu-item" (nothing else), it should contain the text "English", it must contain no element with role "combobox" (count 0), and its bounding box height must be <= 48. 3. Click the action. 4. The popover should be hidden. The dialog's accessible name should be exactly "Change Language". The dialog should hold exactly one button with aria-pressed="true", and its accessible name must be exactly "English". 5. The buttons inside the dialog's element with role "group" and accessible name exactly "Language" ("the options") should number exactly 9. Scroll the last option into view if needed: it must then lie inside the viewport. 6. The dialog's bounding box must lie inside the viewport: x >= 0, y >= 0, x + width <= `+w+`, y + height <= `+h+`. 7. Click the dialog's button named exactly "日本語". 8. The dialog should be hidden, the trigger must be document.activeElement, and localStorage "shelley-locale" must be exactly "ja". 9. Click the trigger: an element in the popover whose text is exactly "外観" should be visible. 10. Click the popover's button whose accessible name contains "言語を切り替える". 11. The dialog's only button with aria-pressed="true" must have the accessible name exactly "日本語". 12. Click the dialog's button named exactly "English". 13. Reload by navigating to /new again and click the trigger. 14. The popover's button whose accessible name contains "Change Language" should contain the text "English".`, func(s *shelley) {
		s.viewport(width, height)
	})
}

func TestE2EOverflowMenuLanguagePicker320(t *testing.T) {
	overflowMenuLanguagePicker(t, useful(0), 320, 568)
}

func TestE2EOverflowMenuLanguagePicker393(t *testing.T) {
	overflowMenuLanguagePicker(t, useful(0), 393, 851)
}

func TestE2EOverflowMenuLanguagePicker1280(t *testing.T) {
	overflowMenuLanguagePicker(t, useful(0), 1280, 800)
}

func TestE2EOverflowMenuLanguagePickerKeyboard(t *testing.T) {
	e2e(t, useful(0), `The language picker dismisses without changes and supports keyboard selection. "The trigger" is ".chat-overflow-menu-wrapper .btn-icon"; "the popover" is ".chat-overflow-popover"; "the dialog" is the element with role="dialog" that contains a ".language-picker-modal" ("[role=dialog]:has(.language-picker-modal)"). `+overflowMenuLanguageNames+`To "open the picker": click the trigger, focus (with an eval calling .focus()) the popover's button whose accessible name contains "Change Language", press Enter, wait for the dialog to be visible, and check that the dialog's button named exactly "English" is document.activeElement. Perform the steps in order. 1. Navigate to /new. 2. Open the picker. Press Escape: the dialog should be hidden, the trigger must be document.activeElement, and the popover should be hidden. 3. Open the picker. Click the dialog's button named "Close modal": the dialog should be hidden and the trigger document.activeElement. 4. Open the picker. Click ".modal-overlay" 2 pixels right and 2 pixels down from its top-left corner: the dialog should be hidden and the trigger document.activeElement; localStorage "shelley-locale" must be null (nothing was changed). 5. Open the picker. Press Tab: the dialog's button named exactly "日本語" must become document.activeElement, with computed outline-style exactly "solid" and outline-width exactly "2px". 6. Press Enter: the dialog should be hidden, the trigger document.activeElement, and localStorage "shelley-locale" exactly "ja". 7. Reload by navigating to /new again, click the trigger, then click the popover's button whose accessible name contains "言語を切り替える". 8. The dialog's button named exactly "日本語" should become document.activeElement.`, nil)
}

func TestE2EOverflowMenuEndOfTurnView(t *testing.T) {
	e2e(t, useful(0), `The overflow menu can show only user, end-of-turn and notification messages. Conversation {{ID}} is an 18-turn conversation: each turn has a user message starting "Turn N:", an agent message starting "I'll work on turn N.", a tool call, and a final "Done with turn N."; turn 18 also has a notification starting "Warning on turn 18:". "Text X is present" means some element's text contains X; for "absent", check that document.body.textContent does not include X at all (hidden elements count too). Perform the steps in order. 1. Navigate to /c/{{ID}}. 2. Wait for the first elements containing "Turn 1:", "I'll work on turn 1." and "Done with turn 1." to be visible, and the first data-testid "tool-call-completed" to be visible. 3. Click ".chat-overflow-menu-wrapper .btn-icon". 4. In data-testid "conversation-view-toggle", the button named "See All" should have aria-pressed="true". Click the button named "See End of Turn Messages Only": it should get aria-pressed="true". 5. The first elements containing "Turn 1:" and "Done with turn 1." should be visible, and the one element containing "Warning on turn 18:" should be visible. 6. "I'll work on turn 1." must be absent, and there must be no data-testid "tool-call-completed" (count 0). 7. localStorage "shelley-conversation-view" must be exactly "end-of-turn". 8. Reload by navigating to /c/{{ID}} again. 9. The first element containing "Done with turn 1." should be visible, and "I'll work on turn 1." must be absent.`, func(s *shelley) {
		var created struct {
			ID string `json:"conversation_id"`
		}
		s.check(json.Unmarshal(s.postForm("/debug/loremipsum?json=1", url.Values{"size": {"18"}, "model": {"predictable"}}), &created))
		s.v("ID", created.ID)
	})
}
