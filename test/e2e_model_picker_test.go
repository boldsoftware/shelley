package test

// Ported from ui/e2e/model-picker.spec.ts.

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

// menusOpenModelPickerHow says how a description opens the model picker
// (helpers.ts openModelPicker): phones reach the model through the overflow
// menu, in every conversation.
const menusOpenModelPickerHow = `To "open the model picker": wait for ".header" to be visible; when the window is narrower than 768px, the picker is in the overflow menu: if ".chat-overflow-popover" is not visible, click the button with aria-label exactly "More options", then click the visible ".model-picker.p-select" inside ".chat-overflow-popover"; on a wider window, click the visible ".model-picker.p-select" inside ".status-bar". The element clicked is "the picker"; its panel is ".model-picker-panel". `

// The unified model + effort picker (ChatStatusContent -> ModelPicker.vue) is
// built on PrimeVue <Select>. It renders on the new-conversation screen. Here
// we exercise the PrimeVue-specific open/select behavior, the inline
// reasoning-effort pill row, the Model heading's "Manage…" action, and
// persistence of the chosen model + effort to localStorage.

func TestE2EModelPickerOpensSelectsPersistsAndManages(t *testing.T) {
	e2e(t, `The model picker (a PrimeVue Select) opens, lists models, persists the selection, and its "Manage Models" action opens the manage-models modal. The screen is the default 393x727 phone. `+menusOpenModelPickerHow+`Name elements by ARIA role and accessible name, as Playwright's getByRole does; "named X" means an exact name match. Perform the steps in order. 1. Navigate to /new and open the model picker. 2. The panel should be visible and hold at least one ".p-select-option". Its buttons named "Manage Models" and "Refresh" should be visible. 3. On a phone it is a sheet across the bottom of the screen: the panel's bounding box must have x exactly 0, width exactly window.innerWidth, and Math.round(y + height) exactly window.innerHeight. 4. In this single-source install there must be no ".model-picker-option-source" in the panel (count 0). 5. Remember the trimmed text of the first option's ".model-picker-option-name" (in a window variable), then click the first option. 6. The picker should contain that remembered name, and localStorage "shelley_selected_model" must be exactly "predictable" (the raw model id, not the label). The panel stays open: it should still be visible. 7. Click the panel's button named "Close": the panel should be hidden, and the picker's ".p-select-label" must be document.activeElement (focus is handed back to the trigger). 8. Open the model picker again; the panel should be visible. 9. Click the panel's button named "Manage Models": a visible dialog (role dialog) named "Manage Models" should appear, and the panel should be hidden.`, nil)
}

// menusAccessibleNames tells the agent how to compute the names the descriptions
// use, as Playwright's getByRole does.
const menusAccessibleNames = `Accessible names: an element's name is its aria-label if it has one; otherwise a button's name is its text with whitespace collapsed and trimmed, leaving out aria-hidden parts, and a dialog's name is the text of the element its aria-labelledby refers to; "named X" means the name is exactly X. Only elements that are not display: none count. `

// modelPickerFieldRows checks the phone composer's model and directory rows all at
// once, returning what fails.
const modelPickerFieldRows = `window.__fieldRows = () => {
  const failures = [];
  const composer = document.querySelector(".status-bar-new");
  const labels = [...composer.querySelectorAll(".status-field-label")];
  const model = document.querySelector(".status-field-model .model-picker.p-select");
  const directory = document.querySelector(".status-field-cwd .status-chip");
  let bottom = 0;
  const rights = new Set();
  const lefts = new Set();
  for (const [name, label, field] of [["model", labels[0], model], ["directory", labels[labels.length - 1], directory]]) {
    const l = label.getBoundingClientRect();
    const f = field.getBoundingClientRect();
    if (!(Math.min(l.y, f.y) >= bottom)) failures.push(name + " row overlaps the row above");
    if (!(l.x >= 0)) failures.push(name + " label starts off screen");
    if (!(l.x + l.width <= f.x)) failures.push(name + " label overlaps its field");
    if (!(f.x + f.width <= window.innerWidth)) failures.push(name + " field ends off screen");
    const style = getComputedStyle(field);
    if (style.backgroundColor === "rgba(0, 0, 0, 0)") failures.push(name + " field has no background");
    if (style.borderTopStyle === "none") failures.push(name + " field has no border");
    rights.add(Math.round(l.x + l.width));
    lefts.add(Math.round(f.x));
    bottom = Math.max(l.y + l.height, f.y + f.height);
  }
  if (rights.size !== 1) failures.push("labels are not right-aligned");
  if (lefts.size !== 1) failures.push("fields are not left-aligned");
  return failures;
};`

// modelPickerFieldRowsAt is the phone composer's field rows test at one width.
func modelPickerFieldRowsAt(t *testing.T, width int) {
	w := strconv.Itoa(width)
	e2e(t, `On a phone, the new-conversation composer labels the model and directory fields a row each. The screen is {{W}}x915. `+menusOpenModelPickerHow+menusAccessibleNames+`Names used below: "the composer" is ".status-bar-new"; "the labels" are its ".status-field-label" elements; "the model field" is ".status-field-model .model-picker.p-select"; "the directory field" is ".status-field-cwd .status-chip". Perform the steps in order. 1. Navigate to /new. 2. The labels' texts should be exactly ["Model:", "Dir:"]. 3. Eval window.__fieldRows() (the page already defines it; do not define it yourself): it checks at once that the rows (first label with the model field, last label with the directory field) are stacked, inside the viewport, with the labels right-aligned in a column of their own and the fields boxed in one after it, and returns the list of what fails; it must return exactly []. 4. The model field's ".model-picker-value-name" text must not be empty, and its ".p-select-dropdown" should be visible. The composer must have no button whose accessible name starts with "Profile" (count 0). 5. Keyboard focus shows on the model's box: focus the directory field (eval .focus()), remember the model field's computed border-top-color in a window variable, then press Shift+Tab; the model field's element with role="combobox" must then be document.activeElement, and the model field's border-top-color must differ from the remembered one. 6. Click the model field: the panel should be visible and contain an element with data-testid "model-picker-profiles" (attached, visible or not). Click the panel's button with aria-label "Close": the panel should be hidden. 7. Click the directory field: ".modal.directory-picker-modal" should be visible; click its button named exactly "Cancel": it should be hidden. 8. Open the model picker (through the overflow menu, since the window is narrower than 768px): the picker should have the class "model-picker-menu", and the panel should be visible. Click the panel's button with aria-label "Close". 9. In ".chat-overflow-popover", a button whose accessible name starts with "Directory" should be visible, and there must be no button whose accessible name starts with "Profile" (count 0).`, func(s *shelley) {
		s.viewport(width, 915)
		s.v("W", w)
		s.initScript(modelPickerFieldRows)
	})
}

func TestE2EModelPickerMobileFieldRows412(t *testing.T) { modelPickerFieldRowsAt(t, 412) }
func TestE2EModelPickerMobileFieldRows320(t *testing.T) { modelPickerFieldRowsAt(t, 320) }
func TestE2EModelPickerMobileFieldRows260(t *testing.T) { modelPickerFieldRowsAt(t, 260) }

func TestE2EModelPickerShortScreenLeavesFieldsToMenu(t *testing.T) {
	e2e(t, `A short phone screen, as with Android's keyboard up, leaves the new-conversation composer's model and directory fields to the menu. The screen is 393x320. 1. Navigate to /new. 2. ".status-bar-new" should be hidden. 3. The message input (data-testid "message-input") must lie entirely inside the viewport: its bounding box has left >= 0, top >= 0, right <= window.innerWidth and bottom <= window.innerHeight.`, func(s *shelley) {
		s.viewport(393, 320)
	})
}

func TestE2EModelPickerDesktopFieldsInARow(t *testing.T) {
	e2e(t, `On a desktop-sized screen the new-conversation composer keeps the model and directory fields in a row, with only the directory labelled. The screen is 1280x800. "The labels" are ".status-bar-new .status-field-label"; "the model field" is ".status-field-model .model-picker.p-select"; "the directory field" is ".status-field-cwd .status-chip". 1. Navigate to /new. 2. The model field and the directory field should be visible. 3. The labels' texts should be exactly ["Model:", "Dir:"]; the first label should be hidden and the last visible. 4. The vertical centers (y + height / 2 of the bounding box) of the model field and the directory field must differ by less than 2px.`, func(s *shelley) {
		s.viewport(1280, 800)
	})
}

func TestE2EModelPickerMobileSearchNotFocused(t *testing.T) {
	e2e(t, `Opening the model picker on a phone does not focus its search. The screen is 390x844. `+menusOpenModelPickerHow+`1. Navigate to /new and open the model picker. 2. The panel's searchbox (role "searchbox") should be visible. 3. It must not be document.activeElement.`, func(s *shelley) {
		s.viewport(390, 844)
	})
}

func TestE2EModelPickerSearchSurvivesCompactViewport(t *testing.T) {
	e2e(t, `The new-chat model search stays usable when the viewport becomes compact. The screen starts at 390x844. 1. Navigate to /new. 2. Click ".status-bar-new .model-picker.p-select". 3. Click the searchbox (role "searchbox") in ".model-picker-panel". 4. Set the viewport to 390x460 (a set_viewport giving only width and height, so the phone keeps its touch screen and scale). 5. The searchbox should be visible and still be document.activeElement. 6. Fill it with "predict": its value should be exactly "predict". 7. The panel should then list exactly one ".p-select-option", and it should contain "predictable".`, func(s *shelley) {
		s.viewport(390, 844)
	})
}

// PrimeVue's Select clears the keyboard focus on a timer after its trigger
// blurs, which it does as the search takes focus; fire that timer instead of
// racing it.
func TestE2EModelPickerSelectedKeepsKeyboardFocus(t *testing.T) {
	e2e(t, `The selected model keeps the keyboard focus once the panel settles. The screen is 1280x800. The page runs on a fake clock that steps advance with eval "window.__clock.runFor(ms)". `+menusOpenModelPickerHow+`1. Navigate to /new and open the model picker. 2. The panel's searchbox (role "searchbox") should be document.activeElement. 3. Run the clock for 1000ms. 4. There should be exactly one ".p-select-option.p-focus" in the panel, and it must have the class "p-select-option-selected".`, func(s *shelley) {
		s.clock(time.Time{})
		s.viewport(1280, 800)
	})
}

func TestE2EModelPickerKeyboardFocusFollowsPointerPick(t *testing.T) {
	e2e(t, `Keyboard focus follows a pick made with the pointer. The screen is 1280x800 (desktop layout, where the search has focus from the start). The server offers a second model, "Predictable Twin". `+menusOpenModelPickerHow+`1. Navigate to /new and open the model picker. 2. The option ".p-select-option" containing "Predictable Twin" ("the twin") should be visible, and the panel's searchbox (role "searchbox") should be document.activeElement. 3. Press ArrowDown, then ArrowUp. 4. The panel's ".p-select-option.p-focus" must exist and not contain "Twin". 5. Click the twin: it should get the class "p-focus". 6. Press Enter: the panel should be hidden, and the picker's ".model-picker-value-name" should have exactly the text "Predictable Twin".`, func(s *shelley) {
		s.viewport(1280, 800)
		s.route("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
			var models []map[string]any
			if err := s.fetchJSON(r, &models); err != nil || len(models) == 0 {
				http.Error(w, "no models", http.StatusBadGateway)
				return
			}
			twin := map[string]any{}
			for k, v := range models[0] {
				twin[k] = v
			}
			twin["id"] = "predictable-twin"
			twin["display_name"] = "Predictable Twin"
			writeJSON(w, append(models, twin))
		})
	})
}

func TestE2EModelPickerEffortPillsPersist(t *testing.T) {
	e2e(t, `The reasoning-effort pills select a level, persist it, and keep the popover open. The screen is the default phone. `+menusOpenModelPickerHow+`The pills are the panel's ".model-picker-effort-pill" elements. 1. Navigate to /new, remove localStorage "shelley.thinkingLevel.v2", and reload by navigating to /new again. 2. Open the model picker; the panel should be visible. 3. There should be at least 6 pills, and none whose text is exactly "max" (models without capability metadata get the standard levels through xhigh). 4. Click the pill whose text is exactly "high". 5. The panel should still be visible, localStorage "shelley.thinkingLevel.v2" must be exactly "high", and that pill should have aria-checked="true". 6. Press Escape: the panel should be hidden. 7. Reload by navigating to /new again and open the model picker. 8. The panel's element with role "radio" and accessible name exactly "high" should have aria-checked="true".`, nil)
}

func TestE2EModelPickerRecentCombinations(t *testing.T) {
	// Playwright also filtered other workers' conversations out of the
	// conversation list; each test here has a server of its own, which holds
	// only the conversation below.
	e2e(t, `The model picker shows recent model and effort combinations, hidden while searching. The only conversation so far used the model "predictable" with reasoning effort "high", and the composer has taken on the default profile already (localStorage "shelley.thinkingLevel.v2" is "high", "shelley.profileApplied" is "Default"). The screen is the default phone. `+menusOpenModelPickerHow+`Within the panel: "the recent row" is ".p-select-option:has(.model-picker-option-effort)" and "the model row" is ".p-select-option:not(:has(.model-picker-option-effort))". Perform the steps in order. 1. Navigate to /new and open the model picker. 2. There must be no ".model-picker-group-label" in the panel (count 0). 3. The model row's ".model-picker-option-name" should have exactly the text "predictable", and its ".model-picker-option-check" should be visible. 4. Click the panel's ".model-picker-effort-pill" whose text is exactly "low". 5. Recent combinations are taken as the panel opens: there must still be no ".model-picker-group-label" (count 0). 6. Press Escape, then open the model picker again. 7. ".model-picker-group-label" should have exactly the text "Recent", and ".model-picker-group-divider" should be visible. 8. The recent row's ".model-picker-option-name" should have exactly the text "predictable", its ".model-picker-option-effort" exactly the text "high", and it must have no ".model-picker-option-check" (count 0). 9. The recent row's computed background-color must be exactly "rgba(0, 0, 0, 0)" (a heading, not a highlight, sets it apart), and the model row must have the class "p-focus" (the keyboard starts on the selected model). 10. The recent row's ".model-picker-option-effort" must start (bounding box x) right of the end (x + width) of its ".model-picker-option-name", and its computed background-color must not be "rgba(0, 0, 0, 0)". 11. Hover the recent row: it must not have the class "p-focus". 12. Fill the panel's searchbox (role "searchbox") with "predictable": there must be no ".model-picker-group-label" (count 0) and no visible ".p-select-option-group" (count of visible ones 0). 13. Fill the searchbox with "" (empty): ".model-picker-group-label" should be visible. 14. Click the recent row: it must still have no ".model-picker-option-check" (count 0), the model row's ".model-picker-option-check" should be visible, the panel's element with role "radio" and accessible name exactly "high" should have aria-checked="true", and the panel should still be visible.`, func(s *shelley) {
		s.conversationWith(map[string]any{
			"message":              "echo recent model picker",
			"model":                "predictable",
			"cwd":                  s.cwd,
			"conversation_options": map[string]any{"thinking_level": "high"},
		}, "")
		s.initScript(`localStorage.setItem("shelley.thinkingLevel.v2", "high");
localStorage.setItem("shelley.profileApplied", "Default");`)
	})
}
