package lazycue

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// AgentMode specifies whether the agent should generate new steps or fix existing ones.
type AgentMode int

const (
	AgentModeGenerate AgentMode = iota
	AgentModeFix
)

// AgentConfig configures an agent run.
type AgentConfig struct {
	Mode          AgentMode
	Description   string
	PreviousSteps []byte // JSON of previous steps (fix mode)
	PreviousError string // Error from previous run (fix mode)
	CacheFilePath string // Path to the cached steps JSON for this test (fix mode); lets the agent inspect its git history for prior flakiness
	// Launch starts a fresh app and a browser on it. Every run of steps gets
	// its own, as a cached run does.
	Launch           func() (*Env, *Browser, error)
	Model            string
	AnthropicBaseURL string
	AnthropicAPIKey  string
	RepoRoot         string
	GitHidden        []string // see Options.GitHidden
	Verbose          bool
}

// AgentResult is the result of an agent run.
type AgentResult struct {
	Success        bool
	Error          string
	StepsJSON      []byte
	StepResults    []StepResult
	ScreenshotPath string
	InputTokens    int // including tokens written to and read from the prompt cache
	OutputTokens   int
	Cost           float64 // estimated USD
	// Effort diagnostics, surfaced so a human can see how hard the agent
	// worked (e.g. did a heal converge in 1-2 turns, or thrash to the limit?).
	Turns       int  // number of API round trips the agent took
	HitMaxTurns bool // true if the agent ran out of turns (agentTurns)
	ToolCalls   int  // total tool_use calls the agent issued (run_steps/screenshot/git_command)
}

// maxGitCommands caps git_command calls per agent run. Agents left to it
// read the test harness and the tests themselves instead of trying steps.
const maxGitCommands = 12

// anthropicCallTimeout bounds a single LLM HTTP request. http.DefaultClient has
// no timeout, so without this a hung request could block a heal indefinitely
// (until agentBudget, or formerly the package deadline).
const anthropicCallTimeout = 90 * time.Second

// anthropicMaxAttempts is the number of times callAnthropic will try a single
// LLM request before giving up. Transient failures (network errors, per-request
// timeouts, 429s, and 5xx responses) are common against the live Anthropic API
// in CI and would otherwise fail an entire heal — and thus a queue build — on a
// single hiccup. Retries are bounded by the surrounding agentBudget context.
const anthropicMaxAttempts = 3

// anthropicRetryBackoff is the base delay between retry attempts; it grows
// linearly with the attempt number (attempt*base) and is capped well under
// agentBudget so a retry chain can never exhaust it on sleeping alone. It is a
// var (not a const) so tests can shrink it to avoid real sleeps.
var anthropicRetryBackoff = 2 * time.Second

// --- Anthropic API types ---

type apiRequest struct {
	Model     string            `json:"model"`
	MaxTokens int               `json:"max_tokens"`
	System    []apiContentBlock `json:"system"`
	Messages  []apiMessage      `json:"messages"`
	Tools     []apiTool         `json:"tools"`
}

type apiMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []apiContentBlock
}

type apiContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   interface{}     `json:"content,omitempty"` // string or []apiContentBlock for tool_result
	Source    *apiImageSource `json:"source,omitempty"`
	// CacheControl marks the end of a prompt prefix to cache.
	CacheControl *apiCacheControl `json:"cache_control,omitempty"`
}

type apiImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type apiTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type apiUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// cost estimates a response's USD cost at Claude Sonnet prices.
func (u apiUsage) cost() float64 {
	return (float64(u.InputTokens)*3 + float64(u.CacheCreationInputTokens)*3.75 +
		float64(u.CacheReadInputTokens)*0.3 + float64(u.OutputTokens)*15) / 1_000_000
}

type apiCacheControl struct {
	Type string `json:"type"`
}

type apiResponse struct {
	ID         string            `json:"id"`
	Content    []apiContentBlock `json:"content"`
	StopReason string            `json:"stop_reason"`
	Error      *apiError         `json:"error,omitempty"`
	Usage      apiUsage          `json:"usage"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// --- Tool input types ---

type runStepsInput struct {
	Steps json.RawMessage `json:"steps"`
	// Final marks this as the complete test to save (not an exploratory probe).
	// When true and all steps pass, the harness caches exactly these steps.
	Final bool `json:"final,omitempty"`
}

type screenshotInput struct {
	Tab string `json:"tab"`
}

type gitCommandInput struct {
	Command string `json:"command"`
}

// --- Agent implementation ---

func buildSystemPrompt() string {
	return `You are a browser test-writing agent. You write DSL test steps (JSON arrays) to test a web application.

Available DSL actions:
- navigate: {"action": "navigate", "url": "/path"} - Navigate to URL: one with a scheme (https:, about:, data:) as it is, any other relative to the base
- reload: {"action": "reload"} - Reload the page and wait for it to load.
- go_back / go_forward: {"action": "go_back"} - Go back or forward in the tab's history, as the browser's buttons do, and wait for the page to load (or just for the URL to change, for an entry the app pushed with pushState). Fails when there is no page to go to.
- wait_visible: {"action": "wait_visible", "selector": "...", "timeout": "10s"} - Wait for element to be visible
- wait_hidden: {"action": "wait_hidden", "selector": "...", "timeout": "10s"} - Wait for element to be hidden
- wait_text: {"action": "wait_text", "text": "...", "timeout": "30s"} - Wait for text in page body, whitespace collapsed
- wait_text_gone: {"action": "wait_text_gone", "text": "...", "timeout": "10s"} - Wait for text to disappear
- fill: {"action": "fill", "selector": "...", "value": "..."} - Focus a visible input, textarea, select (value = an option's value) or contenteditable element and set its value at once, firing input and change events. Fails on a disabled or read-only element.
- type: {"action": "type", "value": "..."} - Type text key by key into the focused element; "\n" presses Enter. Use it when key events matter; otherwise fill. With a selector or role (and any picking field), it first waits for that element to be visible and enabled and focuses it, as Playwright's pressSequentially; an input it moves focus to gets its caret at the start. insert_text and press_key take an element the same way.
- insert_text: {"action": "insert_text", "value": "..."} - Insert text at the caret of the focused element as an IME or emoji picker would: one input event, no key events.
- click: {"action": "click", "selector": "..."} - Click the element's center. Like a user, it waits until the element is visible, holding still, enabled and not covered by another element there, scrolling it into view, and tries again if the page moves it before the click lands. Optional: "x" and "y" (both or neither) click that many CSS pixels from the element's top-left corner instead (without a selector, at that point of the viewport); "button": "right" or "middle"; "count": 2 for a double click; "modifiers": "Shift" (as for press_key) to hold keys during it.
- hover: {"action": "hover", "selector": "..."} - Move the mouse over the element (mouseenter fires and CSS :hover applies) without clicking; waits like click, takes "x"/"y" like click, and "count": N moves there in N steps from where the pointer was (to drag).
- mouse_down / mouse_up: {"action": "mouse_down", "button": "left"} - Press or release a mouse button where the pointer is (or first move it to "point"). Drag: hover the start, mouse_down, hover the end with "count": 10, mouse_up. Hover, mouse_down, mouse_up and wheel take "modifiers" like click.
- wheel: {"action": "wheel", "x": 0, "y": 300} - Turn the mouse wheel where the pointer is (or first move it to "point"), scrolling by x,y pixels.
- touch: {"action": "touch", "value": "start", "selector": "..."} - Touch screen: "value" is start (fingers down), move, end or cancel (all fingers up; no selector). Start and move put one finger where a click would land ("x"/"y" as for click), or several at "points": "10,10 60,10" (x,y offsets from the element's top-left corner, or the viewport's without a selector). A move with "count": N gets there in N even steps from where each finger was (to drag). With "point", "points" are offsets from the point.
- press_key: {"action": "press_key", "key": "Enter", "modifiers": "Shift"} - Press one key on the focused element. "key" is a KeyboardEvent.key name (Enter, Tab, Escape, Backspace, Delete, ArrowUp/Down/Left/Right, Home, End, PageUp, PageDown, F1-F12, Insert), "Space", or a single character. A capital letter or shifted symbol ("P", "?") also holds Shift, as on a real keyboard, so a shortcut written Ctrl+P is key "p" with modifiers "Control". Optional modifiers: Shift, Control, Alt, Meta (combine with "+"), pressed before the key and released after it as on a keyboard, so the page sees them held. Give it a selector or role to focus that element first, as with type, or else focus the target first (click or fill it).
- key_down / key_up: {"action": "key_down", "key": "Shift"} - Press a key and keep it held until key_up, as for press_key. A held Shift, Control, Alt or Meta modifies every later key and mouse step. A key_down of a key already held repeats it (KeyboardEvent.repeat), as holding a key does.
- set_viewport: {"action": "set_viewport", "width": 1280, "height": 800, "scale": 1, "mobile": false, "touch": false} - Change the emulated device for the rest of the test; omitted fields keep their value. Whether the page sees a touch screen changes on the next navigate.
- emulate_media: {"action": "emulate_media", "value": "prefers-color-scheme: dark"} - Override a CSS media feature for the rest of the test; "prefers-reduced-motion:" with no value drops the override.
- screenshot: {"action": "screenshot"} - Take screenshot
- eval: {"action": "eval", "expression": "...", "expect": "..."} - Evaluate JS, waiting for a promise it returns to settle (so fetch and async functions work). The result renders as a string: strings as they are, anything else as JSON with object keys sorted (true, 3, {"a":1,"b":2} for {b: 2, a: 1}), so an expected object lists its keys alphabetically. If "expect" is set (even to "", which expects an empty string), the expression is re-run until its result equals expect or the timeout passes, so it must have no side effects. The run_steps result echoes the value back to you ("=> <value>"), so use eval WITHOUT expect to probe page state (selectors, scrollHeight, classes, etc.) while developing the test.
- call: {"action": "call", "value": "name", "expect": "..."} - Run a function the test provides on the host (the user prompt lists them), for what the page cannot do or see. It returns a string; "expect" re-runs it until the result equals expect, like eval.
- assert_visible: {"action": "assert_visible", "selector": "..."} - Assert element is visible
- assert_not_visible: {"action": "assert_not_visible", "selector": "..."} - Assert element is not visible
- assert_text: {"action": "assert_text", "selector": "...", "text": "..."} - Assert the element's whole text, whitespace collapsed
- assert_text_contains / assert_text_not_contains: {"action": "assert_text_contains", "selector": "...", "text": "..."} - Assert the element's text contains, or does not contain, the text, whitespace collapsed. When the description names an element, assert on that element this way rather than with a page-wide wait_text.
- assert_texts: {"action": "assert_texts", "selector": "li", "texts": ["One", "Two"]} - Assert the texts of ALL the matches, whitespace collapsed: as many as listed, equal, in that order, like Playwright's toHaveText([...]). "texts": [] (or none) asserts there are no matches.
- assert_in_viewport / assert_not_in_viewport: {"action": "assert_in_viewport", "selector": "...", "ratio": 0.5} - Assert any part of the element (or, with "ratio", at least that share of it) is or is not inside the viewport, after clipping by scrolling ancestors, like Playwright's toBeInViewport. The element must exist.
- assert_attribute: {"action": "assert_attribute", "selector": "...", "attribute": "...", "value": "..."} - Assert an attribute is present with this value; it fails when the attribute is absent, even for value "". To check that an attribute is present or absent, use assert_count with an attribute selector, e.g. {"action": "assert_count", "selector": "details:not([open])", "count": 1}. For disabled state use assert_enabled / assert_disabled.
- assert_value: {"action": "assert_value", "selector": "...", "value": "..."} - Assert the current value of an input, textarea or select (the DOM property, which typing changes; not the value attribute).
- assert_enabled / assert_disabled: {"action": "assert_enabled", "selector": "..."} - Assert a control is enabled or disabled: disabled means a disabled form control (or one in a disabled fieldset) or inside aria-disabled="true".
- assert_focused: {"action": "assert_focused", "selector": "..."} - Assert the element has keyboard focus. For a custom element with a shadow root, focus anywhere inside it counts.
- assert_not_focused: {"action": "assert_not_focused", "selector": "..."} - Assert the element exists and does not have keyboard focus, by the same rule.
- wait_url / assert_url (the same step): {"action": "assert_url", "value": "/c/abc"} (exact) or {"action": "assert_url", "text": "..."} (substring) - Wait for the browser URL to match. An exact "value" may be the whole URL or just its path, query and hash: for a URL that must equal (or end with) a path, use an exact value rather than text.
- assert_title: {"action": "assert_title", "text": "..."}
- assert_count: {"action": "assert_count", "selector": "...", "count": 3}
- sleep: {"action": "sleep", "timeout": "1s"}
- retry: {"action": "retry", "steps": [...], "timeout": "15s"} - Run the steps in order and, when one fails, start over from the first (after 100ms, 250ms, 500ms, then every 1s) until a whole pass succeeds or the timeout (default 15s) passes, like Playwright's expect.poll or toPass. Waits, assertions and evals or calls with an expect inside without a timeout of their own get 1s, so the block does the waiting; a mistake in a step (an unknown action or role) fails the block at once. Use it when an action must be repeated until an assertion after it holds, e.g. choose a menu item that copies a terminal's output into an input, then assert the input's value. Retries do not nest.
- close_tab: {"action": "close_tab", "tab": "B"} - Close a tab opened by an earlier step, as a user would.
- collect_garbage: {"action": "collect_garbage"} - Force a full garbage collection in the page, e.g. before checking that what only a WeakRef holds was freed.

Every assert_* and wait_* step retries until it holds or its timeout (default 15s) passes, so they wait for the app on their own; don't add sleeps or eval polls before them. Dialogs close by themselves: a page may always be left despite a beforeunload handler, and alert, confirm and prompt are cancelled (confirm returns false).

Visible means rendered with a box of some size and not visibility: hidden; opacity does not matter. Every text check collapses runs of whitespace, non-breaking spaces included, into one space and trims, on both sides, as Playwright does. Selectors are CSS and match the first element, looking into open shadow roots too (the page's own matches first, as Playwright does); a selector does not span a shadow boundary, so for the input in <x-picker>'s shadow root use {"within": {"selector": "x-picker"}, "selector": "input"}, not "x-picker input". Any step with a selector may add "has_text": "..." to keep only the elements whose text contains it (ignoring case, whitespace collapsed), e.g. {"action": "click", "selector": "button", "has_text": "Archive"}; prefer it to eval for picking an element by its text. Of nested matches only the innermost counts, so {"selector": "*", "has_text": "Saved"} is the element that holds the text itself, not <html>. The browser emulates a Pixel 5 phone (393x727, touch screen) that prefers reduced motion and may use the clipboard, unless the test launched it otherwise.

ROLES: like Playwright's getByRole, any step that takes a selector may instead pick elements by "role" and accessible "name": {"action": "click", "role": "button", "name": "Archive"}. Prefer them to CSS for controls, dialogs, menus and headings. The role is an ARIA role, explicit or implicit: button, link, textbox (<input>, <textarea>), searchbox (<input type=search>), checkbox, radio, switch, combobox (<select>), listbox, option, menu, menuitem, menuitemcheckbox, menuitemradio, tab, tablist, tabpanel, dialog, alertdialog, heading, img, list, listitem, region (<section> with a label), navigation, main, group, toolbar, tooltip, status, alert, progressbar, slider, table, row, cell, … The name is what a screen reader announces (aria-label, aria-labelledby, a <label>, alt, placeholder, or the text inside), matched as a case-insensitive substring with whitespace collapsed. Elements hidden from screen readers (display: none, visibility: hidden, aria-hidden, inert, and everything behind an open modal <dialog>) never match; pick those with a selector. With a "selector" too, only role matches inside an element it matches count, e.g. {"selector": "[role=dialog]", "role": "button", "name": "Save"}; "has_text" further keeps those whose text contains it. Two more fields narrow any element step: "exact": true matches the whole name and has_text, case-sensitively, in the case the HTML has, whatever CSS text-transform shows; "visible": true keeps only visible matches, e.g. {"selector": "button", "has_text": "Copy", "visible": true}. Last, "nth" keeps the match at that index, as Playwright's nth(): 0 is the first, 1 the second, -1 the last, e.g. {"action": "click", "role": "button", "name": "Copy", "nth": -1}; prefer has_text or within when some text tells the matches apart, since an index breaks when the page adds one.

WITHIN: like Playwright's chained locators, any element step may add "within": an object with the same picking fields (selector, role, name, has_text, exact, visible, nth, has) that keeps only the step's matches inside an element it picks out, e.g. {"action": "assert_text", "within": {"selector": "[data-testid=card]", "has_text": "Stopped by the user"}, "selector": "[data-testid=status]", "text": "Interrupted"} or {"action": "click", "within": {"selector": ".message", "has_text": "Hello"}, "role": "button", "name": "Copy"}. Prefer it to eval for picking the element in a card or row that has some text. Likewise "has", like Playwright's filter({has}), keeps only the matches holding an element it picks out, e.g. the profile row whose name is exactly "Default": {"action": "click", "selector": ".profiles-item", "has": {"selector": ".profiles-item-name", "has_text": "Default", "exact": true}}.

FRAMES: like Playwright's frameLocator, any element step, eval, wait_text or wait_text_gone may add "frame": a CSS selector for an <iframe> in the page, to look and act in its document instead, e.g. {"action": "assert_value", "frame": "iframe", "selector": "[data-testid=message-input]", "value": ""}. Same-origin, sandboxed and cross-origin frames all work; an eval there sees the frame's DOM but not its scripts' globals, and roles in a cross-origin frame are found only while it is in view.

POINTS: click, hover, mouse_down, mouse_up, touch and wheel may aim at "point" instead: a JS expression giving {x, y} in viewport CSS pixels, computed as the step runs (and awaited), e.g. {"action": "click", "point": "(() => { const r = document.querySelector('canvas').getBoundingClientRect(); return {x: r.left + 5 * r.width / 80, y: r.top + 2 * r.height / 24}; })()"} for a cell of a terminal, or a handle's center plus an offset to drag from just outside it. It is re-run until it gives a finite point inside the viewport; nothing there is waited for or checked, like Playwright's page.mouse. Use a selector or role when the target is an element.

TABS: any step may add "tab": "<name>" to act in another tab of the same browser profile — like a user with two tabs open, the tabs share localStorage, cookies and IndexedDB but have their own windows. The tab the browser started with has no name: steps without "tab" act in it, and a description's "this tab", "the first tab" or "tab A" means it, so never give it a name (a navigate with a new name opens ANOTHER tab). A navigate step with a not-yet-open tab name opens that tab; {"action": "navigate", "tab": "B"} with no url opens it on the page the first tab is currently on (use this for a URL the app assigned, like a conversation's /c/<slug>). When a description says "in tab B" or "another tab", use this rather than iframes or window.open. A page the app opens itself (a target=_blank link, window.open) is tab "popup", or "popup2", "popup3", ... while an earlier one is open: a step with "tab": "popup" waits for it to open, then acts in it as in any tab, e.g. {"action": "assert_url", "tab": "popup", "text": "/docs"}; close_tab closes it. Never navigate to a popup's name to open one.

WORKFLOW:
1. Every run_steps call gets a fresh app and browser, exactly as a cached run will: nothing carries over from an earlier call (no page, storage or server state), so start each one by navigating to the appropriate page.
2. Steps wait up to 15s on their own; give a step a longer timeout only when the app needs more.
3. Use the run_steps tool to test your DSL steps. Review the results and fix any failures.
4. Use screenshot to see what the page looks like if you're unsure about selectors or page state.
5. Use git_command sparingly (at most ` + strconv.Itoa(maxGitCommands) + ` calls) to look up the app's own source: selectors, data-testid attributes, labels. Placeholders, window helpers and calls a description names come from the test, not the app: try them with run_steps (eval) instead of searching for them, and don't look for other tests or their cached scripts.
6. Your FINAL call to run_steps must be the COMPLETE, passing test that exercises every part of the description. Submit it by setting "final": true on that run_steps call (exploratory probes must NOT set final). The harness caches exactly the steps from your final submission, so it must contain all the assertions — never submit a bare navigation/probe as final.
7. Minimize the number of tool calls. Aim for 1-3 run_steps calls total.

CRITICAL: WHEN FIXING A FAILING TEST:
- The test DESCRIPTION is the source of truth for what the application SHOULD do.
- If the description says "the title should be X" and the page shows "Y", that means the APPLICATION IS BROKEN, not the test.
- Only fix MECHANICAL issues: wrong selectors, missing waits, timing issues, wrong CSS selectors.
- NEVER change what the test asserts to match broken application behavior.
- If the application doesn't match the description, the test SHOULD fail. Report it as a genuine failure.
- If you determine the app is genuinely broken (not matching the description), output an empty steps array and explain the failure.

DISTINGUISHING FLAKES FROM GENUINE FAILURES:
Many heal requests are triggered not by a broken app but by a LOAD-INDUCED FLAKE:
under heavy CI parallelism the box is so starved that an element appears later
than a wait_* timeout allows, even though the same steps pass quickly when the
machine is idle. Treat an error as a likely flake when it is timeout-shaped
(e.g. "wait_visible/wait_text timed out", an element that intermittently doesn't
appear, an assert that fails only on a transient/empty state) AND the steps
themselves look correct for the description.
- A timeout-shaped error is NOT automatically a flake. The real discriminator is
  "appears LATE" vs "never appears / appears WRONG". Before deciding it's a flake
  you MUST confirm the app actually does the right thing now: navigate and use
  screenshot / eval (without expect) to observe the live page, and verify the
  awaited element/text is genuinely present and CORRECT per the description, just
  slow. If it is absent or shows the wrong content even after waiting, that is a
  GENUINE FAILURE (a real regression often looks timeout-shaped because the
  element never appears) — do not paper over it by raising timeouts.
- INVESTIGATE BEFORE EDITING. Use git_command to read the cached test's history:
  'git log --oneline -- <cache-file>' and 'git log -p -- <cache-file>'. Repeated
  past edits that only bumped timeouts or swapped a sleep for a wait are a strong
  signal this test flakes under load.
- Once you have CONFIRMED the behavior is correct-but-slow, the right fix is to
  make the SAME steps MORE ROBUST, never to weaken assertions: raise wait_*
  timeouts generously (e.g. 30s -> 60s), replace bare sleeps or implicit waits
  with explicit wait_visible/wait_text on the element you are about to assert,
  and wait for the precise post-condition (specific text/element/url) rather
  than a coarse one. Keep every assertion in the description intact.
- Conclude "genuine app failure" (empty steps array, with an explanation)
  whenever the live page does not match the description — including when an
  element never appears or shows wrong content despite waiting. When in doubt
  between flake and regression, prefer reporting the failure over masking it.`
}

func buildGenerateUserPrompt(description string) string {
	return fmt.Sprintf(`Write DSL test steps for this behavior:

%s

Write the complete test as a single run_steps call. If the description mentions specific selectors or page structure you're unsure about, use screenshot or git_command to discover them first.`, description)
}

func buildFixUserPrompt(description string, previousSteps []byte, previousError, cacheFilePath string) string {
	historyHint := ""
	if cacheFilePath != "" {
		historyHint = fmt.Sprintf(`

This test's cached steps live at: %s
Before editing, check whether this test has a history of flaking under load:
  git log --oneline -- %s
  git log -p -- %s
A history of timeout-only bumps is strong evidence the failure is a load flake,
not a broken app.`, cacheFilePath, cacheFilePath, cacheFilePath)
	}
	return fmt.Sprintf(`A previously generated DSL test is failing. Fix it.

Test description: %s

Previous DSL steps:
%s

Error:
%s

INSTRUCTIONS:
- The test DESCRIPTION is the source of truth for what the app SHOULD do.
- First decide: is this a LOAD-INDUCED FLAKE or a GENUINE app failure?
  A timeout-shaped error (wait_* timed out, an element that intermittently
  doesn't appear) can be either: "appears late" (flake) or "never appears /
  appears wrong" (real regression). Observe the LIVE page (navigate + screenshot
  / eval) and confirm the awaited element/text is actually present and correct
  per the description before calling it a flake.%s
- For a flake: keep every assertion, and make the same steps more robust —
  raise wait_* timeouts generously and wait explicitly on the precise element
  you are about to assert. Do NOT weaken assertions.
- For a mechanical bug: fix wrong selectors / missing waits only.
- NEVER change assertions to match broken application behavior.
- If the app genuinely doesn't match the description (e.g., wrong title, missing
  text, and robustness changes can't fix it), return an empty steps array [] and
  explain the failure.
- Use screenshots to verify the current state, then decide.`, description, string(previousSteps), previousError, historyHint)
}

func buildTools() []apiTool {
	return []apiTool{
		{
			Name:        "run_steps",
			Description: "Execute an array of DSL test steps against a fresh app and browser, exactly as a cached run will: nothing carries over from an earlier call. Returns structured results showing which step passed/failed and why; the screenshot tool then shows the browser as the steps left it. Use this to test your generated DSL. When you have the COMPLETE test that exercises everything in the description and it passes, call run_steps one last time with \"final\": true to submit it for caching.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"steps": {
						"type": "array",
						"description": "Array of DSL step objects to execute",
						"items": { "type": "object" }
					},
					"final": {
						"type": "boolean",
						"description": "Set true only when these steps are the complete, final test to save. Do not set on exploratory probes."
					}
				},
				"required": ["steps"]
			}`),
		},
		{
			Name:        "screenshot",
			Description: "Take a screenshot of the current page state. Returns the screenshot as a base64-encoded PNG image. Use this to see what the page looks like.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"tab": {
						"type": "string",
						"description": "Name of the tab to capture, as used in steps' \"tab\" field. Omit for the tab the browser started with."
					}
				},
				"required": []
			}`),
		},
		{
			Name:        "git_command",
			Description: "Run a read-only git command in the app's source directory: git grep, git ls-files, git show, git log, git diff. Paths are relative to that directory, so read a file with git show HEAD:./<path>. Limited to a few calls per run.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {
						"type": "string",
						"description": "The git command to run (e.g., 'git grep data-testid', 'git ls-files vue/')"
					}
				},
				"required": ["command"]
			}`),
		},
	}
}

// envPrompt tells the agent about the device its test launched and the
// placeholders and calls its app offers.
func envPrompt(env *Env) string {
	var sb strings.Builder
	d := env.Launch.device()
	name, mobile, touch := "", "not mobile", "without"
	if d == Pixel5 {
		name = "a Pixel 5 (the default): "
	}
	if d.Mobile {
		mobile = "mobile"
	}
	if d.Touch {
		touch = "with"
	}
	fmt.Fprintf(&sb, "\n\nDEVICE: %s%dx%d CSS pixels at scale %g, %s, %s a touch screen. The test launched it, so don't set_viewport unless the description asks for another device or size.\n", name, d.Width, d.Height, d.Scale, mobile, touch)
	if len(env.Vars) > 0 {
		sb.WriteString("\n\nPLACEHOLDERS: their values change from run to run (each run_steps result lists that run's). Steps must write the placeholder, never the value; the harness substitutes it in every string field. In the first run:\n")
		for _, name := range slices.Sorted(maps.Keys(env.Vars)) {
			fmt.Fprintf(&sb, "- {{%s}} = %s\n", name, env.Vars[name])
		}
	}
	if len(env.Launch.Hosts) > 0 {
		sb.WriteString("\n\nOTHER HOSTS the test answers for in every tab: " + strings.Join(slices.Sorted(maps.Keys(env.Launch.Hosts)), ", ") + "\n")
		sb.WriteString("Any origin may fetch from them. Their pages cannot load anything from the app: Chrome keeps public pages off the local network.\n")
	}
	if len(env.Calls) > 0 {
		sb.WriteString("\n\nCALLS available to call steps: " + strings.Join(slices.Sorted(maps.Keys(env.Calls)), ", ") + "\n")
	}
	return sb.String()
}

// varsLine lists a run's placeholder values on one line.
func varsLine(vars map[string]string) string {
	var parts []string
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		parts = append(parts, fmt.Sprintf("{{%s}} = %s", name, vars[name]))
	}
	return strings.Join(parts, ", ")
}

// inlinedVar returns the name of a var whose value in any of runs a step
// spells out instead of using its placeholder, or "" if none does.
func inlinedVar(steps []Step, runs ...map[string]string) string {
	for _, vars := range runs {
		for _, name := range slices.Sorted(maps.Keys(vars)) {
			v := vars[name]
			if len(v) < 4 {
				continue
			}
			for _, s := range withNested(steps) {
				for _, f := range stringFields(&s) {
					if strings.Contains(*f, v) {
						return name
					}
				}
			}
		}
	}
	return ""
}

// withNested lists steps and the steps they hold.
func withNested(steps []Step) []Step {
	var all []Step
	for _, s := range steps {
		all = append(append(all, s), withNested(s.Steps)...)
	}
	return all
}

// cacheConversation returns messages with the last block marked for prompt
// caching, so each turn re-reads the conversation so far from the cache
// instead of paying for it again.
func cacheConversation(messages []apiMessage) []apiMessage {
	if len(messages) == 0 {
		return messages
	}
	out := slices.Clone(messages)
	last := &out[len(out)-1]
	var blocks []apiContentBlock
	switch c := last.Content.(type) {
	case string:
		blocks = []apiContentBlock{{Type: "text", Text: c}}
	case []apiContentBlock:
		blocks = slices.Clone(c)
	default:
		return out
	}
	if len(blocks) == 0 {
		return out
	}
	blocks[len(blocks)-1].CacheControl = &apiCacheControl{Type: "ephemeral"}
	last.Content = blocks
	return out
}

// RunAgent executes the LLM agent loop to generate or fix DSL test steps.
func RunAgent(ctx context.Context, cfg *AgentConfig) (*AgentResult, error) {
	logf := func(format string, args ...any) {
		if cfg.Verbose {
			log.Printf("[agent] "+format, args...)
		}
	}

	// Bound the whole agent run so a stuck heal can't run until the package
	// test deadline and panic (taking the rest of the package down with it).
	ctx, cancel := context.WithTimeout(ctx, agentBudget)
	defer cancel()

	// The app and browser the last run of steps had, which the screenshot
	// tool looks at; the next run gets fresh ones.
	var env *Env
	var br *Browser
	var used bool
	var runVars []map[string]string // every run's placeholder values
	closeSession := func() {
		if br != nil {
			br.Close()
			env.close()
			br, env = nil, nil
		}
	}
	defer closeSession()
	fresh := func() error {
		closeSession()
		e, b, err := cfg.Launch()
		if err != nil {
			return err
		}
		env, br, used = e, b, false
		runVars = append(runVars, e.Vars)
		return nil
	}
	if err := fresh(); err != nil {
		return nil, err
	}

	systemPrompt := buildSystemPrompt()
	var userPrompt string
	if cfg.Mode == AgentModeFix {
		userPrompt = buildFixUserPrompt(cfg.Description, cfg.PreviousSteps, cfg.PreviousError, cfg.CacheFilePath)
	} else {
		userPrompt = buildGenerateUserPrompt(cfg.Description)
	}
	userPrompt += envPrompt(env)

	messages := []apiMessage{
		{Role: "user", Content: userPrompt},
	}

	tools := buildTools()

	var lastStepsJSON []byte
	var lastStepResults []StepResult
	// finalStepsJSON / finalStepResults hold the run_steps call the agent
	// explicitly marked as its complete test ("// FINAL"). Exploratory probes
	// must not be cached, so we only accept these as the saved test.
	var finalStepsJSON []byte
	var finalStepResults []StepResult
	var nudgedForFinal bool
	var lastError string
	var genuineFailure bool // set when agent determines the APP is broken, not the test
	var totalInputTokens, totalOutputTokens int
	var totalCost float64
	// Effort accounting for heal diagnostics: how many API round trips and tool
	// calls the agent took, and whether it exhausted its turn budget.
	var turnsTaken, toolCallCount, gitCommands int
	finish := func(r *AgentResult) (*AgentResult, error) {
		r.InputTokens = totalInputTokens
		r.OutputTokens = totalOutputTokens
		r.Cost = totalCost
		r.Turns = turnsTaken
		r.ToolCalls = toolCallCount
		r.HitMaxTurns = turnsTaken >= agentTurns
		return r, nil
	}

	for turn := 0; turn < agentTurns; turn++ {
		turnsTaken = turn + 1
		logf("turn %d", turn)

		resp, err := callAnthropic(ctx, cfg, systemPrompt, messages, tools)
		if err != nil {
			return nil, fmt.Errorf("anthropic API call: %w", err)
		}

		if resp.Error != nil {
			return nil, fmt.Errorf("anthropic error: %s: %s", resp.Error.Type, resp.Error.Message)
		}

		totalInputTokens += resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens
		totalOutputTokens += resp.Usage.OutputTokens
		totalCost += resp.Usage.cost()

		// Check for tool_use blocks. Track whether this assistant turn declared
		// its run_steps as the FINAL, complete test (vs. an exploratory probe).
		var toolUses []apiContentBlock
		var turnIsFinal bool
		for _, block := range resp.Content {
			if block.Type == "tool_use" {
				toolUses = append(toolUses, block)
			}
			if block.Type == "text" {
				logf("assistant: %s", truncate(block.Text, 200))
				if strings.Contains(block.Text, "// FINAL") {
					turnIsFinal = true
				}
			}
		}

		// Check text blocks for genuine failure signals.
		for _, block := range resp.Content {
			if block.Type == "text" && isGenuineFailureSignal(block.Text) {
				logf("agent detected genuine application failure")
				genuineFailure = true
				if lastError == "" {
					lastError = block.Text
				}
			}
		}

		if len(toolUses) == 0 {
			// No tool calls — agent is done.
			if genuineFailure {
				errMsg := lastError
				if errMsg == "" {
					errMsg = "agent determined the application is broken"
				}
				return finish(&AgentResult{
					Success:     false,
					Error:       errMsg,
					StepsJSON:   lastStepsJSON,
					StepResults: lastStepResults,
				})
			}
			// Only accept a run_steps call the agent explicitly marked "// FINAL"
			// as the saved test. This prevents caching an exploratory probe that
			// happened to be the agent's last run_steps but doesn't actually test
			// the described behavior.
			if finalStepsJSON != nil && allPassed(finalStepResults) {
				return finish(&AgentResult{
					Success:     true,
					StepsJSON:   finalStepsJSON,
					StepResults: finalStepResults,
				})
			}
			// The agent stopped without a passing FINAL test. Nudge it once to
			// produce one (it may have only run exploratory probes), then fail.
			if !nudgedForFinal {
				nudgedForFinal = true
				messages = append(messages, apiMessage{
					Role: "user",
					Content: []apiContentBlock{{
						Type: "text",
						Text: "You have not yet produced a complete, passing test. Do NOT stop on an exploratory probe. Write the FULL test that exercises everything in the description (including every assertion), output the text \"// FINAL\" on its own, and call run_steps with the complete step list. If you believe the application is genuinely broken, say so explicitly instead.",
					}},
				})
				continue
			}
			errMsg := lastError
			if errMsg == "" {
				for _, block := range resp.Content {
					if block.Type == "text" {
						errMsg = block.Text
						break
					}
				}
			}
			if errMsg == "" {
				errMsg = "agent stopped without producing passing test"
			}
			return finish(&AgentResult{
				Success:     false,
				Error:       errMsg,
				StepsJSON:   lastStepsJSON,
				StepResults: lastStepResults,
			})
		}

		// Append assistant response to messages.
		messages = append(messages, apiMessage{
			Role:    "assistant",
			Content: resp.Content,
		})

		// Process tool calls.
		var toolResults []apiContentBlock
		var finalSubmittedThisTurn bool
		toolCallCount += len(toolUses)
		for _, tu := range toolUses {
			switch tu.Name {
			case "run_steps":
				var input runStepsInput
				if err := json.Unmarshal(tu.Input, &input); err != nil {
					toolResults = append(toolResults, makeToolResult(tu.ID, fmt.Sprintf("Error parsing input: %v", err)))
					continue
				}

				steps, err := ParseSteps(input.Steps)
				if err != nil {
					toolResults = append(toolResults, makeToolResult(tu.ID, fmt.Sprintf("Error parsing steps: %v", err)))
					continue
				}

				logf("tool: run_steps (%d steps)", len(steps))
				for si, s := range steps {
					logf("  [%d] %s", si, StepSummary(s))
				}

				// A call is the final test if the agent set the explicit `final`
				// flag on the tool call OR wrote the legacy "// FINAL" marker.
				final := input.Final || turnIsFinal
				if final {
					if v := inlinedVar(steps, runVars...); v != "" {
						toolResults = append(toolResults, makeToolResult(tu.ID, fmt.Sprintf("Not run: a step contains the value of {{%s}}, which changes from run to run. Write the placeholder instead.", v)))
						continue
					}
				}
				if used {
					if err := fresh(); err != nil {
						return nil, err
					}
				}
				used = true
				results, execErr := br.ExecuteSteps(ctx, env, steps)
				lastStepResults = results
				lastStepsJSON = input.Steps
				if final {
					finalStepResults = results
					finalStepsJSON = input.Steps
					finalSubmittedThisTurn = true
				}

				// Build result summary.
				var sb strings.Builder
				for i, r := range results {
					status := "PASS"
					if !r.Pass {
						status = "FAIL"
					}
					sb.WriteString(fmt.Sprintf("Step %d [%s] %s (%s)", i, r.Action, status, r.Duration.Round(time.Millisecond)))
					if r.Error != "" {
						sb.WriteString(": " + r.Error)
					}
					if r.Output != "" {
						// Surface eval results so the agent can read the value it
						// probed for. Truncate to keep tool results compact.
						sb.WriteString(" => " + truncateArg(r.Output, 200))
					}
					sb.WriteString("\n")
				}

				if execErr != nil {
					lastError = execErr.Error()
					sb.WriteString("\nOverall: FAILED - " + execErr.Error())
				} else {
					sb.WriteString("\nOverall: ALL STEPS PASSED")
				}
				if len(env.Vars) > 0 {
					sb.WriteString("\nPlaceholders in this run: " + varsLine(env.Vars))
				}

				toolResults = append(toolResults, makeToolResult(tu.ID, sb.String()))

			case "screenshot":
				var input screenshotInput
				if err := json.Unmarshal(tu.Input, &input); err != nil {
					toolResults = append(toolResults, makeToolResult(tu.ID, fmt.Sprintf("Error parsing input: %v", err)))
					continue
				}
				if input.Tab == "" {
					logf("tool: screenshot")
				} else {
					logf("tool: screenshot tab %s", input.Tab)
				}
				png, err := br.Screenshot(ctx, input.Tab)
				if err != nil {
					toolResults = append(toolResults, makeToolResult(tu.ID, fmt.Sprintf("Screenshot failed: %v", err)))
					continue
				}
				b64 := base64.StdEncoding.EncodeToString(png)
				toolResults = append(toolResults, apiContentBlock{
					Type:      "tool_result",
					ToolUseID: tu.ID,
					Content: []apiContentBlock{
						{
							Type: "image",
							Source: &apiImageSource{
								Type:      "base64",
								MediaType: "image/png",
								Data:      b64,
							},
						},
					},
				})

			case "git_command":
				var input gitCommandInput
				if err := json.Unmarshal(tu.Input, &input); err != nil {
					toolResults = append(toolResults, makeToolResult(tu.ID, fmt.Sprintf("Error parsing input: %v", err)))
					continue
				}

				logf("tool: git_command %s", input.Command)
				gitCommands++
				result := fmt.Sprintf("Error: git_command is limited to %d calls per run; work with run_steps and screenshot now.", maxGitCommands)
				if gitCommands <= maxGitCommands {
					result = executeGitCommand(cfg.RepoRoot, cfg.GitHidden, cfg.CacheFilePath, input.Command)
				}
				toolResults = append(toolResults, makeToolResult(tu.ID, result))

			default:
				logf("tool: %s (unknown)", tu.Name)
				toolResults = append(toolResults, makeToolResult(tu.ID, fmt.Sprintf("Unknown tool: %s", tu.Name)))
			}
		}

		messages = append(messages, apiMessage{
			Role:    "user",
			Content: toolResults,
		})

		// The agent submitted a final test (via the run_steps `final` flag or
		// "// FINAL") this turn and every step passed: accept it immediately
		// without burning another round trip waiting for an end-of-turn message.
		if (turnIsFinal || finalSubmittedThisTurn) && finalStepsJSON != nil && allPassed(finalStepResults) {
			return finish(&AgentResult{
				Success:     true,
				StepsJSON:   finalStepsJSON,
				StepResults: finalStepResults,
			})
		}

		// If genuine failure was detected, stop immediately.
		if genuineFailure {
			errMsg := lastError
			if errMsg == "" {
				errMsg = "agent determined the application is broken"
			}
			return finish(&AgentResult{
				Success:     false,
				Error:       errMsg,
				StepsJSON:   lastStepsJSON,
				StepResults: lastStepResults,
			})
		}

		// If the agent marked a complete (FINAL) test that passed all steps,
		// we're done. Exploratory probes are never accepted as the saved test.
		if finalStepsJSON != nil && allPassed(finalStepResults) {
			if resp.StopReason == "end_turn" {
				return finish(&AgentResult{
					Success:     true,
					StepsJSON:   finalStepsJSON,
					StepResults: finalStepResults,
				})
			}
			// Continue to let agent confirm/finalize.
		}
	}

	// Exhausted turns: only accept a passing FINAL test.
	if finalStepsJSON != nil && allPassed(finalStepResults) {
		return finish(&AgentResult{
			Success:     true,
			StepsJSON:   finalStepsJSON,
			StepResults: finalStepResults,
		})
	}

	return finish(&AgentResult{
		Success:     false,
		Error:       "agent exhausted maximum turns",
		StepsJSON:   lastStepsJSON,
		StepResults: lastStepResults,
	})
}

func allPassed(results []StepResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, r := range results {
		if !r.Pass {
			return false
		}
	}
	return true
}

func makeToolResult(id, text string) apiContentBlock {
	return apiContentBlock{
		Type:      "tool_result",
		ToolUseID: id,
		Content:   text,
	}
}

// gitExec runs a git command in the given directory and returns trimmed stdout.
func gitExec(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(args, " "), err, string(ee.Stderr))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func executeGitCommand(repoRoot string, hidden []string, cacheFile, command string) string {
	args, err := splitArgs(strings.TrimSpace(command))
	if err != nil {
		return "Error: " + err.Error()
	}
	if len(args) < 2 || args[0] != "git" {
		return "Error: Only git commands are allowed"
	}
	allowedSubcommands := []string{"grep", "ls-files", "show", "log", "diff", "cat-file", "rev-parse", "for-each-ref"}
	if !slices.Contains(allowedSubcommands, args[1]) {
		return fmt.Sprintf("Error: Only these git subcommands are allowed: %s", strings.Join(allowedSubcommands, ", "))
	}
	args = args[1:]
	if len(hidden) > 0 {
		if msg := fenceGit(args, hidden, cacheFile); msg != "" {
			return "Error: " + msg
		}
	}

	out, err := gitExec(repoRoot, args...)
	var ee *exec.ExitError
	if args[0] == "grep" && errors.As(err, &ee) && ee.ExitCode() == 1 {
		return "(no matches)"
	}
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if len(hidden) > 0 && !(cacheFile != "" && slices.Contains(args, cacheFile)) {
		// Drop what a command turned up in hidden paths anyway.
		lines := slices.DeleteFunc(strings.Split(out, "\n"), func(l string) bool { return hiddenIn(l, hidden) != "" })
		out = strings.Join(lines, "\n")
	}
	if out == "" {
		return "(empty output)"
	}
	const maxLen = 8000
	if len(out) > maxLen {
		return out[:maxLen] + fmt.Sprintf("\n... (truncated, %d total chars)", len(out))
	}
	return out
}

// fenceGit keeps a git command (args after "git") away from hidden paths
// and in its directory, where other tests are not, and says why it may
// not run, or returns "". The cache file is fine. Arguments that may be
// patterns are refused only for unmistakable escapes.
func fenceGit(args, hidden []string, cacheFile string) string {
	sub := args[0]
	for i, a := range args[1:] {
		if cacheFile != "" && a == cacheFile {
			continue
		}
		if h := hiddenIn(a, hidden); h != "" {
			return h + " is off limits; follow the description and look at the live page instead."
		}
		pathspec := slices.Contains(args[1:i+1], "--")
		if (sub == "show" || sub == "cat-file") && !pathspec && !strings.HasPrefix(a, "-") {
			if rev, tree, ok := strings.Cut(a, ":"); ok && (!strings.HasPrefix(tree, "./") || strings.Contains(tree, "/..")) {
				return a + " is relative to the repository root; write " + rev + ":./<path> for a path in this directory."
			}
		}
		// Before --, only shapes no pattern of interest has.
		escapes := a == ".." || strings.HasPrefix(a, "../") || strings.HasPrefix(a, ":/") || strings.HasPrefix(a, ":(") || isFile(a)
		exclude := strings.HasPrefix(a, ":!") || strings.HasPrefix(a, ":^") // stays in the directory
		if pathspec && (strings.HasPrefix(a, "..") || strings.Contains(a, "/..") || strings.HasPrefix(a, ":") && !exclude || strings.HasPrefix(a, "/")) ||
			!pathspec && !strings.HasPrefix(a, "-") && escapes {
			return a + " leaves this directory; use paths inside it."
		}
	}
	return ""
}

// isFile reports whether s is an absolute path that exists.
func isFile(s string) bool {
	if !filepath.IsAbs(s) {
		return false
	}
	_, err := os.Stat(s)
	return err == nil
}

// hiddenIn returns the first of hidden that s contains, or "".
func hiddenIn(s string, hidden []string) string {
	for _, h := range hidden {
		if strings.Contains(s, h) {
			return h
		}
	}
	return ""
}

// splitArgs splits a command line as a shell would for simple quoting:
// single quotes keep everything, double quotes keep all but backslash
// escapes of " and \.
func splitArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inArg := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, errors.New("unterminated ' in command")
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 1
			inArg = true
		case c == '"':
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
					i++
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, errors.New("unterminated \" in command")
			}
			inArg = true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			inArg = true
		default:
			cur.WriteByte(c)
			inArg = true
		}
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}

// callAnthropic issues an LLM request, retrying transient failures (network
// errors, per-request timeouts, 429, and 5xx) up to anthropicMaxAttempts times
// with a linear backoff. Non-retryable failures (e.g. 4xx other than 429, or a
// cancelled parent context) return immediately. All sleeping respects ctx so
// the surrounding agent budget and test cancellation still win.
func callAnthropic(ctx context.Context, cfg *AgentConfig, systemPrompt string, messages []apiMessage, tools []apiTool) (*apiResponse, error) {
	var lastErr error
	for attempt := 1; attempt <= anthropicMaxAttempts; attempt++ {
		resp, err := callAnthropicOnce(ctx, cfg, systemPrompt, messages, tools)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		// Stop early if the parent context is done (agent budget exhausted or
		// the test was cancelled) or the error isn't worth retrying.
		if ctx.Err() != nil || !isRetryableAnthropicErr(err) || attempt == anthropicMaxAttempts {
			break
		}
		sleep := time.Duration(attempt) * anthropicRetryBackoff
		if cfg.Verbose {
			log.Printf("[agent] anthropic request failed (attempt %d/%d), retrying in %s: %v", attempt, anthropicMaxAttempts, sleep, err)
		}
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return nil, fmt.Errorf("anthropic API call: %w (last error: %v)", ctx.Err(), lastErr)
		}
	}
	return nil, lastErr
}

// isRetryableAnthropicErr reports whether an error from callAnthropicOnce is
// worth retrying. Transport errors (DNS, connection reset, EOF) and per-request
// deadline timeouts are transient, as are HTTP 429 and 5xx responses. A
// cancelled parent context is never retryable.
func isRetryableAnthropicErr(err error) bool {
	if err == nil {
		return false
	}
	// A cancelled or expired parent context should not be retried; only the
	// per-request timeout (a fresh deadline.Exceeded with the parent still live)
	// is handled by the ctx.Err() check in the caller.
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var apiErr *anthropicStatusError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
	}
	// Default: treat unknown errors (transport-level) as retryable, since the
	// dominant CI failure mode is a flaky network to api.anthropic.com.
	return true
}

// anthropicStatusError carries a non-200 HTTP status so the retry logic can
// distinguish retryable (429, 5xx) from terminal (other 4xx) responses.
type anthropicStatusError struct {
	StatusCode int
	Body       string
}

func (e *anthropicStatusError) Error() string {
	return fmt.Sprintf("API returned %d: %s", e.StatusCode, e.Body)
}

func callAnthropicOnce(ctx context.Context, cfg *AgentConfig, systemPrompt string, messages []apiMessage, tools []apiTool) (*apiResponse, error) {
	reqBody := apiRequest{
		Model:     cfg.Model,
		MaxTokens: 8192,
		System:    []apiContentBlock{{Type: "text", Text: systemPrompt, CacheControl: &apiCacheControl{Type: "ephemeral"}}},
		Messages:  cacheConversation(messages),
		Tools:     tools,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Bound each request so a hung connection can't stall the whole agent.
	// Derived from ctx so the agent budget (and test cancellation) still win.
	ctx, cancel := context.WithTimeout(ctx, anthropicCallTimeout)
	defer cancel()

	url := strings.TrimRight(cfg.AnthropicBaseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cfg.AnthropicAPIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != 200 {
		return nil, &anthropicStatusError{StatusCode: httpResp.StatusCode, Body: string(respBody)}
	}

	var resp apiResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &resp, nil
}

// isGenuineFailureSignal checks if the agent's text output indicates the
// APPLICATION is genuinely broken (not a mechanical test issue).
func isGenuineFailureSignal(text string) bool {
	lower := strings.ToLower(text)
	signals := []string{
		"genuine application failure",
		"genuine failure",
		"application is broken",
		"app is broken",
		"the application does not match",
		"not a mechanical",
		"not a test issue",
		"the app is genuinely broken",
		"this is a real bug",
		"this is a genuine bug",
	}
	for _, s := range signals {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
