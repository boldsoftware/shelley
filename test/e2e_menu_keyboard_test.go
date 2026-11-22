package test

// Ported from ui/e2e/menu-keyboard.spec.ts.

import (
	"os"
	"strconv"
	"strings"
	"testing"

	lazycue "github.com/boldsoftware/shelley/lazycue"
)

// menuKeyboardDesktop is the spec's browser: a 1280x900 window, not a phone,
// without a touch screen.
var menuKeyboardDesktop = lazycue.Device{Width: 1280, Height: 900, Scale: 1}

// menuKeyboardFixture starts conversation message in a fresh directory inside
// the working directory, as the spec's fixtureDirectory did, setting {{ID}}
// to its ID. It returns the directory.
func menuKeyboardFixture(s *shelley, message string) string {
	dir := s.mkdir("menu-keyboard-fixture")
	c := s.conversationWith(map[string]any{"message": message, "model": "predictable", "cwd": dir}, "")
	s.v("ID", c.ID)
	return dir
}

// menuKeyboardRenderer has terminals render with renderer, as if chosen in the
// terminal panel's menu.
func menuKeyboardRenderer(s *shelley, renderer string) {
	s.initScript(`localStorage.setItem("shelley-terminal-renderer", ` + strconv.Quote(renderer) + `);`)
}

// menuKeyboardTerms says how a description presses keys "on" an element, as
// Playwright's locator.press did.
const menuKeyboardTerms = `The browser is a 1280x900 desktop window. "Press K on X" means: focus X with an eval calling X.focus(), then press K (for example "Control+Shift+D" is the key "D" with modifiers Control+Shift, and "Control+Backquote" is the key "` + "`" + `" with modifier Control). "The message input" is data-testid "message-input"; "the terminal input" is the element with role "textbox" and accessible name exactly "Terminal input" that is visible; "focused" means it is document.activeElement. `

func TestE2EMenuKeyboardDialogsKeepBackgroundOut(t *testing.T) {
	e2e(t, useful(0), `Keyboard shortcuts: dialogs keep background actions and the terminal toggle out. `+menuKeyboardTerms+`Perform the steps in order. 1. Navigate to /c/{{ID}} and wait for the message input to be visible. 2. Press Control+p on the message input. 3. "The finder" is the first element with role "textbox" inside the element with role "dialog": it should be focused. Remember the current URL (location.href) in a window variable. 4. For each of the keys Control+Shift+D, Control+Shift+X, Control+Backquote and Alt+ArrowDown, in turn: press it on the finder, then check that the dialog is still visible, that there is no ".diff-viewer-overlay" (count 0), and that ".terminal-panel" is hidden (absent or not visible). 5. Press Control+m on the finder, then press "n" on it: the finder's value must be exactly "n", and location.href must still be the remembered URL. 6. Press Escape: the dialog should be hidden. 7. Press Control+k on the message input: ".command-palette-input" should be focused. 8. Press Control+Shift+D on it, then Control+Backquote on it: there must be no ".diff-viewer-overlay" (count 0) and ".terminal-panel" must be hidden. 9. Press Control+k on the palette input: ".command-palette-input" should be hidden. 10. Press Control+Shift+D on the message input: ".diff-viewer-overlay" should be visible. 11. This overlay need not move focus, so checking only event.target is insufficient: press Control+Backquote on the message input, then press Control+Shift+X on it. 12. ".terminal-panel" must be hidden, and location.href must still be the remembered URL.`, func(s *shelley) {
		s.device(menuKeyboardDesktop)
		menuKeyboardFixture(s, "Hello")
	})
}

func TestE2EMenuKeyboardHandledAndComposingKeys(t *testing.T) {
	e2e(t, useful(0), `Keyboard shortcuts: keys already handled, and keys typed while composing, trigger neither the app's shortcuts nor the terminal toggle. `+menuKeyboardTerms+`Perform the steps in order. 1. Navigate to /new and wait for the message input to be visible. 2. For each state S of "handled", "composing" and "safari-composing", in turn: eval exactly this, with S substituted as a string literal: (() => { const S = <S>; const el = document.querySelector('[data-testid="message-input"]'); for (const [key, code, shiftKey] of [['D', 'KeyD', true], ['k', 'KeyK', false], ['p', 'KeyP', false], ['m', 'KeyM', false], ['`+"`"+`', 'Backquote', false]]) { const event = new KeyboardEvent('keydown', { key, code, shiftKey, ctrlKey: true, bubbles: true, cancelable: true, isComposing: S === 'composing', keyCode: S === 'safari-composing' ? 229 : 0 }); if (S === 'handled') event.preventDefault(); el.dispatchEvent(event); } return 'dispatched'; })(); then check that there is no ".diff-viewer-overlay, .command-palette-overlay" (count 0), no element with role "dialog" (count 0), and that ".terminal-panel" is hidden (absent or not visible). 3. Press "n" on the message input: its value must be exactly "n". 4. Press Control+k on the message input: ".command-palette-input" should be visible.`, func(s *shelley) {
		s.device(menuKeyboardDesktop)
	})
}

func TestE2EMenuKeyboardLocalKeyHandlers(t *testing.T) {
	e2e(t, useful(0), `Keyboard shortcuts: text fields and menus respect local key handlers. `+menuKeyboardTerms+`Perform the steps in order. 1. Navigate to /c/{{ID}} and fill the message input with "keep this draft". 2. Add two extra fields with one eval: for tag of "input" and "div", create the element (the div with contentEditable = "true"), give it id "terminal-shortcut-<tag>" and style.cssText "position:fixed;top:0;left:0;width:100px;height:30px;z-index:9999", and append it to document.body. 3. For each field F of the message input, #terminal-shortcut-input and #terminal-shortcut-div, in turn: a. press Control+Shift+D on F: ".diff-viewer-overlay" should be visible; b. press Escape: there should be no ".diff-viewer-overlay" (count 0); c. eval F.onkeydown = (event) => { if (event.code === 'Backquote') event.preventDefault(); }; d. press Control+Backquote on F: there must be no ".terminal-panel-tab" (count 0), and F must be focused; e. eval F.onkeydown = null; f. press Control+Backquote on F: the terminal input should be focused and there should be exactly 1 ".terminal-panel-tab"; g. press Control+Backquote (on the terminal input, still focused): the message input should be focused; h. click the button with accessible name exactly "Close terminal": ".terminal-panel-tab" count should become 0. 4. The message input's value must be exactly "keep this draft". 5. Click the button with aria-label exactly "More options": ".chat-overflow-popover" should be visible. 6. Press Control+Shift+D (without moving focus): ".diff-viewer-overlay" should be visible.`, func(s *shelley) {
		s.device(menuKeyboardDesktop)
		menuKeyboardFixture(s, "Hello")
	})
}

func TestE2EMenuKeyboardForeignTerminal(t *testing.T) {
	e2e(t, useful(0), `Keyboard shortcuts: a terminal of another conversation does not swallow opening the current one's. Terminals render with ghostty. `+menuKeyboardTerms+`Call "second" starts a second conversation in the same directory and sets {{SECOND}} to its ID; call "terminals" returns how many of the server's terminals belong to conversation {{SECOND}}. Perform the steps in order. 1. Navigate to /c/{{ID}}. 2. Press Control+Backquote on the message input: there should be exactly 1 ".terminal-panel-tab-running". 3. Call "second". 4. Navigate to /c/{{SECOND}}: ".terminal-panel" should be hidden (absent or not visible). 5. Press Control+Backquote on the message input: the terminal input should be focused, and there should be exactly 1 ".terminal-panel-tab-running". 6. Call "terminals" until it returns "1".`, func(s *shelley) {
		s.device(menuKeyboardDesktop)
		menuKeyboardRenderer(s, "ghostty")
		dir := menuKeyboardFixture(s, "First")
		var second string
		s.call("second", func() string {
			second = s.conversationWith(map[string]any{"message": "Second", "model": "predictable", "cwd": dir}, "").ID
			s.v("SECOND", second)
			return "started"
		})
		s.call("terminals", func() string {
			var terminals []struct {
				ConversationID string `json:"conversation_id"`
			}
			s.api("GET", "/api/terminals", nil, &terminals)
			n := 0
			for _, t := range terminals {
				if second != "" && t.ConversationID == second {
					n++
				}
			}
			return strconv.Itoa(n)
		})
	})
}

// menuKeyboardMacCommand is the Mac Command shortcut test with one terminal renderer.
func menuKeyboardMacCommand(t *testing.T, useful lazycue.Useful, renderer string) {
	e2e(t, useful, `Keyboard shortcuts: on a Mac, Command shortcuts still reach Shelley from the terminal. navigator.platform is "MacIntel", and terminals render with `+renderer+`. `+menuKeyboardTerms+`Perform the steps in order. 1. Navigate to /c/{{ID}} and wait for the message input to be enabled. 2. Press Control+Backquote on the message input: the terminal input should be focused. 3. Press Meta+k on the terminal input: ".command-palette-input" should be focused. 4. Press Meta+k on the palette input: there should be no ".command-palette-input" (count 0). 5. Press Meta+Shift+D on the terminal input (the key "D" with modifiers Meta+Shift): ".diff-viewer-overlay" should be visible. 6. Focus the message input and press Escape: there should be no ".diff-viewer-overlay" (count 0).`, func(s *shelley) {
		s.device(menuKeyboardDesktop)
		menuKeyboardRenderer(s, renderer)
		s.initScript(`Object.defineProperty(Navigator.prototype, "platform", { get: () => "MacIntel" });`)
		menuKeyboardFixture(s, "Hello")
	})
}

func TestE2EMenuKeyboardMacCommandGhostty(t *testing.T) {
	menuKeyboardMacCommand(t, useful(0), "ghostty")
}

func TestE2EMenuKeyboardMacCommandXterm(t *testing.T) { menuKeyboardMacCommand(t, useful(0), "xterm") }

// menuKeyboardTerminalToggle is the terminal toggle test with one renderer. The spec
// gave this one a touch screen (hasTouch: true) on its desktop window.
func menuKeyboardTerminalToggle(t *testing.T, useful lazycue.Useful, renderer string) {
	e2e(t, useful, `The terminal toggle (`+renderer+`) keeps the selected tab and restores hardware-keyboard focus. Terminals render with `+renderer+`, and the desktop window also has a touch screen. `+menuKeyboardTerms+`"The selected view" is the one visible element with a data-terminal-id attribute. Perform the steps in order. 1. Navigate to /c/{{ID}} and wait for the message input to be enabled. 2. Press Control+Backquote on the message input: the terminal input should be focused, and there should be exactly 1 ".terminal-panel-tab-running". 3. Remember the selected view's data-terminal-id (in a window variable) as the first terminal's ID. 4. Click the button with accessible name exactly "New terminal": there should be exactly 2 ".terminal-panel-tab-running", and the terminal input should be focused. 5. Click the first ".terminal-panel-tab": the selected view's data-terminal-id should be the first terminal's ID. 6. Focus the message input (eval). 7. key_down Control, then key_down "`+"`"+`" (hold them): the terminal input should be focused, and the selected view's data-terminal-id should still be the first terminal's ID. 8. key_down "`+"`"+`" again while it is still held (an auto-repeat): ".terminal-panel" must not have the class "terminal-panel-minimized", and the terminal input must still be focused. 9. key_up "`+"`"+`", then key_up Control. 10. Press Control+Backquote on the terminal input: ".terminal-panel" should have the class "terminal-panel-minimized", and the message input should be focused. 11. Press Control+Backquote on the message input: the terminal input should be focused, the selected view's data-terminal-id should be the first terminal's ID, and there should be exactly 2 ".terminal-panel-tab-running".`, func(s *shelley) {
		d := menuKeyboardDesktop
		d.Touch = true
		s.device(d)
		menuKeyboardRenderer(s, renderer)
		menuKeyboardFixture(s, "Hello")
	})
}

func TestE2EMenuKeyboardTerminalToggleGhostty(t *testing.T) {
	menuKeyboardTerminalToggle(t, useful(0), "ghostty")
}

func TestE2EMenuKeyboardTerminalToggleXterm(t *testing.T) {
	menuKeyboardTerminalToggle(t, useful(0), "xterm")
}

// menuKeyboardShellControlKeys is the shell control keys test with one renderer.
func menuKeyboardShellControlKeys(t *testing.T, useful lazycue.Useful, renderer string) {
	e2e(t, useful, `Shell control keys arrive in the terminal (`+renderer+`), but the terminal toggle does not. Terminals render with `+renderer+`. `+menuKeyboardTerms+`The conversation's directory holds keys.sh, a script that puts the terminal in raw mode, creates a file "ready", reads keys up to a "q" and writes their hex bytes to a file "received". Call "ready" says whether the file "ready" exists ("true" or "false"); call "received" returns the trimmed content of "received" ("" while it does not exist). Perform the steps in order. 1. Navigate to /c/{{ID}}. 2. Fill the message input with "!bash keys.sh" and click the send button (data-testid "send-button"). 3. Call "ready" until it returns "true". 4. Press Control+Backquote on the message input: the terminal input should be focused. 5. Press Control+k on the terminal input: there must be no ".command-palette-overlay" (count 0). Press Control+b on the terminal input: still no ".command-palette-overlay" (count 0). 6. Press Control+Backquote on the terminal input: the message input should be focused. 7. Press Control+Backquote on the message input: the terminal input should be focused. 8. Press Control+Shift+D on the message input: ".diff-viewer-overlay" should be visible. 9. Press Control+Backquote on the terminal input: ".terminal-panel" must not have the class "terminal-panel-minimized". 10. Focus the message input and press Escape: there should be no ".diff-viewer-overlay" (count 0). 11. Press "q" on the terminal input. 12. Call "received" until it returns exactly "0b 02" (Control+k and Control+b reached the shell; the toggle presses did not).`, func(s *shelley) {
		s.device(menuKeyboardDesktop)
		menuKeyboardRenderer(s, renderer)
		dir := menuKeyboardFixture(s, "Hello")
		s.write(dir+"/keys.sh", "stty raw -echo\nprintf ready > ready\nIFS= read -r -d q keys\nstty sane\nprintf '%s' \"$keys\" | od -An -tx1 > received\nread -r\n")
		s.call("ready", func() string {
			_, err := os.Stat(dir + "/ready")
			return strconv.FormatBool(err == nil)
		})
		s.call("received", func() string {
			b, err := os.ReadFile(dir + "/received")
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(b))
		})
	})
}

func TestE2EMenuKeyboardShellControlKeysGhostty(t *testing.T) {
	menuKeyboardShellControlKeys(t, useful(0), "ghostty")
}

func TestE2EMenuKeyboardShellControlKeysXterm(t *testing.T) {
	menuKeyboardShellControlKeys(t, useful(0), "xterm")
}
