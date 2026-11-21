package test

import "testing"

// Ported from ui/e2e/terminal-mac-clipboard.spec.ts.

// nvim asks whether the terminal speaks the kitty keyboard protocol and, if it
// does, turns it on. xterm.js 6 then encodes Cmd+V as a key report (nvim shows
// `<D-v>`) and cancels the event, so Safari never pastes. The terminal must
// not answer, and Cmd+V must stay with the browser. So must other Cmd
// shortcuts, the browser's and the app's (recorded where the app listens).
func macCmdV(t *testing.T, renderer string) {
	e2e(t, `macOS Cmd+V reaches the browser when an app asks for the kitty keyboard protocol (`+renderer+`). Terminals render with `+renderer+`; the browser says it runs on a Mac (navigator.platform "MacIntel"). window.cmdKeydowns records, in order, {key, prevented} for every keydown with the Meta key held (other than Meta itself), as the window saw it once handlers had run. The test seeded a conversation at /c/term-kitty, whose directory holds keys.sh, which asks for the kitty keyboard protocol the way nvim does, prints "kitty-ready", then reports raw input (the terminal's reply included) up to "q" as keys=<input, shell-quoted>. "The input" is [data-testid="message-input"]. `+termInput+termMenu+`
1. Navigate to /c/term-kitty. Fill the input with "!bash keys.sh" and click [data-testid="send-button"].
2. Retry the following steps as one block until they pass (timeout 15s): choose the terminal menu item "Insert all output into input" (both clicks inside the block); then the input's value must contain "kitty-ready" (eval with expect true).
3. Fill the input with "".
4. Focus the terminal input (eval .focus()), then press, each with press_key: Meta+v, Meta+c, Meta+l, q.
5. There should be exactly 1 ".terminal-panel-tab-success".
6. Choose the terminal menu item "Insert all output into input". The input's value should contain "keys=''" (the program read nothing before the q: no key reports and no protocol reply).
7. window.cmdKeydowns must be exactly [{"key":"v","prevented":false},{"key":"c","prevented":false},{"key":"l","prevented":false}] (check once).`, func(s *shelley) {
		s.terminalRenderer(renderer)
		s.initScript(`Object.defineProperty(Navigator.prototype, "platform", { get: () => "MacIntel" });
window.cmdKeydowns = [];
window.addEventListener("keydown", (e) => {
  if (!e.metaKey || e.key === "Meta") return;
  setTimeout(() => window.cmdKeydowns.push({ key: e.key, prevented: e.defaultPrevented }));
});`)
		s.terminalConversation("terminal-kitty", "terminal mac clipboard test", "term-kitty", map[string]string{
			"keys.sh": `stty raw -echo
printf '\033[?u\033[>1u'
printf 'kitty-ready\r\n'
IFS= read -r -d q keys
stty sane
printf '\033[<u'
printf 'keys=%q\n' "$keys"
`,
		})
	})
}

func TestE2ETerminalMacCmdVGhostty(t *testing.T) { macCmdV(t, "ghostty") }

func TestE2ETerminalMacCmdVXterm(t *testing.T) { macCmdV(t, "xterm") }
