package test

import "testing"

// Ported from ui/e2e/terminal-menu.spec.ts.

// The terminal panel keeps its copy and insert actions in one menu.
func TestE2ETerminalMenuCopiesAndInserts(t *testing.T) {
	e2e(t, useful(0), `The terminal menu copies and inserts the output. The test seeded a conversation at /c/term-menu, whose directory holds t.sh, which prints "early-1" to "early-200" (one per line), then "menu-ready", then waits reading input. "The trigger" is the button with aria-label exactly "Terminal menu" and "the menu" is ".overflow-menu-panel". "The clipboard" is the text the eval `+"`navigator.clipboard.readText()`"+` returns. "The input" is [data-testid="message-input"]. `+termMenu+`
1. Navigate to /c/term-menu. Fill the input with "!bash t.sh" and click [data-testid="send-button"]. ".terminal-panel-tab-running" should be visible.
2. Click the trigger. The menu's buttons (role button inside the menu) should have exactly these texts, in order (assert_texts): ["Copy visible screen","Copy all output","Insert visible screen into input","Insert all output into input","Show key shortcuts","Render with ghostty","Render with xterm.js"].
3. Click the trigger again. The menu should be hidden.
4. Retry the following steps as one block until they pass (timeout 15s): choose the terminal menu item "Copy visible screen" (both clicks inside the block); then the clipboard must contain "menu-ready" (eval with expect true).
5. The menu should be hidden, and the trigger should have the class "terminal-panel-action-btn-feedback". The clipboard must not contain "early-1" followed by a newline (check once).
6. Choose the terminal menu item "Copy all output". The clipboard should come to contain "early-1" followed by a newline (eval with expect true, polled).
7. Choose the terminal menu item "Insert visible screen into input". The input's value should match /menu-ready/, and must not match /early-1\n/.
8. Fill the input with "". Choose the terminal menu item "Insert all output into input". The input's value should match /early-1\n[^]*menu-ready/ (eval with expect true).`, func(s *shelley) {
		s.terminalConversation("terminal-menu", "terminal menu test", "term-menu", map[string]string{
			"t.sh": "seq -f 'early-%g' 200\necho menu-ready\nexec cat\n",
		})
	})
}

func TestE2ETerminalMenuKeyboardClosesWithPanel(t *testing.T) {
	e2e(t, useful(0), `The terminal menu works from the keyboard and closes with the panel. The test seeded a conversation at /c/term-menu-keys. "The trigger" is the button with aria-label exactly "Terminal menu", "the menu" is ".overflow-menu-panel", and a menu item is the button inside the menu with exactly that text. Call "go" creates the file "go" in the terminal's directory. `+termInput+`Press keys with press_key on whatever has focus; don't click menu items in steps 2 to 4.
1. Navigate to /c/term-menu-keys. Fill [data-testid="message-input"] with "!bash" and click [data-testid="send-button"]. ".terminal-panel-tab-running" should be visible.
2. Focus the trigger (eval .focus()) and press Enter. The menu item "Copy visible screen" should be focused. Press Tab: the item "Copy all output" should be focused.
3. Press Escape. The menu should be hidden and the trigger focused.
4. Press Enter, then Enter again (opens the menu, then chooses "Copy visible screen"). The menu should be hidden and the trigger focused.
5. The shell ends, and its tab and the panel go: so does the menu. Type into the terminal input: while [ ! -e go ]; do sleep 0.05; done; exit
   Then press Enter.
6. Click the trigger. The menu should be visible.
7. Call "go". ".terminal-panel" should become hidden, and the menu hidden.`, func(s *shelley) {
		_, dir := s.terminalConversation("terminal-menu", "terminal menu keyboard test", "term-menu-keys", nil)
		s.call("go", func() string {
			s.write(dir+"/go", "")
			return "done"
		})
	})
}

// ghostty-web draws terminals unless the menu says xterm.js. Switching
// reopens running terminals, and the server replays their screens.
func TestE2ETerminalMenuSwitchesRenderers(t *testing.T) {
	e2e(t, useful(0), `The terminal menu switches renderers and remembers the choice. The test seeded a conversation at /c/term-renderer, whose directory holds t.sh, which prints "before-switch" and then echoes what it reads (cat). "The terminal" is '`+visibleTerminal+`', "the menu" is ".overflow-menu-panel", and "the input" is [data-testid="message-input"]. `+termInput+termMenu+`"Check the renderer is R" means: click the button with aria-label exactly "Terminal menu"; the menu's [aria-pressed="true"] should have exactly the text R; press Escape; the menu should be hidden. "Read the output until it matches P" means: retry the following steps as one block until they pass (timeout 15s): fill the input with ""; choose the terminal menu item "Insert all output into input" (both clicks inside the block); the input's value must match P (eval with expect true).
1. Navigate to /c/term-renderer. Fill the input with "!bash t.sh" and click [data-testid="send-button"]. ".terminal-panel-tab-running" should be visible.
2. ghostty-web paints a canvas; xterm.js renders into an .xterm element. Check the renderer is "Render with ghostty". The terminal should contain exactly 1 canvas and no ".xterm" (count 0).
3. Choose the terminal menu item "Render with xterm.js". The terminal should contain exactly 1 ".xterm".
4. Read the output until it matches /before-switch/.
5. The insert focuses the input in a zero-delay timeout: let that timeout run first (eval, awaited: new Promise((resolve) => setTimeout(() => resolve("flushed"), 0)), expect "flushed"), then type "typed-in-xterm" into the terminal input and press Enter.
6. Read the output until it matches /typed-in-xterm\n[^]*typed-in-xterm/ (the echo of the typed line, then cat's copy).
7. Reload. ".terminal-panel-tab-running" should be visible, and the terminal should contain exactly 1 ".xterm".
8. Check the renderer is "Render with xterm.js".
9. Choose the terminal menu item "Render with ghostty". The terminal should contain no ".xterm" (count 0) and exactly 1 canvas.
10. Read the output until it matches /typed-in-xterm/.
11. Again let the insert's timeout run first (the same eval), then type "typed-in-ghostty" into the terminal input and press Enter.
12. Read the output until it matches /typed-in-ghostty\n[^]*typed-in-ghostty/.`, func(s *shelley) {
		s.terminalConversation("terminal-menu", "terminal renderer test", "term-renderer", map[string]string{
			"t.sh": "echo before-switch\nexec cat\n",
		})
	})
}
