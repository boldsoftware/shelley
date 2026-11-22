package test

import (
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/terminal-panel-layout.spec.ts.

const (
	// openTerminalPhone opens the Terminal workspace tool on a phone, which
	// keeps workspace tools in the overflow menu, as the spec's
	// openWorkspaceTool did there.
	openTerminalPhone = `open the Terminal workspace tool: phones keep workspace tools in the overflow menu, so click the visible button with aria-label exactly "More options", then the button in ".chat-overflow-popover" whose text starts with "Terminal"`

	// openTerminalDesktop opens the Terminal workspace tool on a wide
	// screen, from the toolbar.
	openTerminalDesktop = `open the Terminal workspace tool: click the visible button named exactly "Terminal" (aria-label or text) in ".chat-workspace-actions"; only if there is none, use the overflow menu as phones do (the visible button with aria-label exactly "More options", then the button in ".chat-overflow-popover" whose text starts with "Terminal")`

	// terminalSizes records the size messages terminals send their server.
	terminalSizes = `const sizeMessages = [];
Object.defineProperty(window, "__terminalSizeMessages", { value: sizeMessages });
const send = WebSocket.prototype.send;
WebSocket.prototype.send = function (data) {
  if (typeof data === "string") {
    try {
      const message = JSON.parse(data);
      if (message.type === "init" || message.type === "resize") sizeMessages.push(message);
    } catch {
      // Non-JSON terminal input is irrelevant to this layout test.
    }
  }
  Reflect.apply(send, this, [data]);
};`
)

func TestE2ETerminalPanelLayoutShortcutOverscroll(t *testing.T) {
	e2e(t, useful(0), `The terminal key shortcuts row contains its overscroll while staying scrollable. Key shortcuts are turned on. "The row" is '[role="group"][aria-label="Terminal key shortcuts"]'. The viewport is 393x851.
1. Navigate to /new and `+openTerminalPhone+`.
2. The row should be visible, and its computed overscroll-behavior-x must be "contain".
3. The row's scrollWidth - clientWidth should become greater than 0 (eval with expect true).
4. Eval: set the row's scrollLeft to its scrollWidth. Its scrollLeft should become greater than 0.
5. Click the button with aria-label exactly "Close active terminal".`, func(s *shelley) {
		s.initScript(`localStorage.setItem("shelley-terminal-key-shortcuts", "true");`)
		s.viewport(393, 851)
	})
}

// fitsAboveKeyboard is the test of the terminal fitting above a tall phone
// keyboard, with renderer drawing it into screen.
func fitsAboveKeyboard(t *testing.T, useful lazycue.Useful, renderer, screen string) {
	e2e(t, useful, `The terminal fits above a tall phone keyboard (`+renderer+`). Terminals render with `+renderer+`. "The panel" is ".terminal-panel" and "the screen" is `+screen+`. The viewport starts at 393x851.
1. Navigate to /new and `+openTerminalPhone+`.
2. The screen should be visible. Record the panel's getBoundingClientRect().height in a window variable as its original height.
3. Android's resizes-content viewport policy makes a tall keyboard (or landscape) reduce the layout viewport; the grid and its input must remain reachable. Set the viewport to 393x320. Then this should become true (eval with expect, within 5s): the screen's getBoundingClientRect() has top >= 0 and top + height <= 320, and [data-testid="message-input"]'s top + height <= 320.
4. They must recover their chosen height on dismissal: set the viewport to 393x851. The panel's height should become exactly the original height.
5. Click the button with aria-label exactly "Close active terminal".`, func(s *shelley) {
		s.terminalRenderer(renderer)
		s.viewport(393, 851)
	})
}

func TestE2ETerminalPanelLayoutFitsAboveKeyboardGhostty(t *testing.T) {
	fitsAboveKeyboard(t, useful(0), "ghostty", `the visible canvas inside the panel (the first "canvas" in it with a nonzero box)`)
}

func TestE2ETerminalPanelLayoutFitsAboveKeyboardXterm(t *testing.T) {
	fitsAboveKeyboard(t, useful(0), "xterm", `the visible ".xterm-screen" inside the panel (the first one with a nonzero box)`)
}

func TestE2ETerminalPanelLayoutPaddedContent(t *testing.T) {
	e2e(t, useful(0), `The terminal panel keeps the terminal inside its padded content area. The test seeded a conversation at /c/term-layout. window.__terminalSizeMessages lists the init and resize messages ({type, cols, rows}) the page's terminals sent their server, in order. "The terminal" is '`+visibleTerminal+`'; terminals render with ghostty by default, which paints a canvas (the inside check takes either renderer's screen). The viewport starts at 1280x720.
"The inside check" is this eval, which must become exactly {"screenBottom":true,"screenRight":true,"terminalBottom":true,"terminalRight":true} (xterm adds its screen after the container shows, so poll until then): (() => { const el = document.querySelector('`+visibleTerminal+`'); const t = el.getBoundingClientRect(); const c = el.parentElement.getBoundingClientRect(); const screen = el.querySelector(".xterm-screen, canvas"); if (!screen) return null; const s = screen.getBoundingClientRect(); return {terminalBottom: t.bottom <= c.bottom, terminalRight: t.right <= c.right, screenBottom: s.bottom <= t.bottom, screenRight: s.right <= t.right}; })()
1. Navigate to /c/term-layout and `+openTerminalDesktop+`.
2. The terminal should become visible (wait up to 30s).
3. Run the inside check.
4. The cols of the first "init" message in window.__terminalSizeMessages should become greater than 0. Record them in a window variable as the initial cols.
5. Click the button with aria-label exactly "Minimize terminals". The terminal should be hidden (minimizing sets display: none on ".terminal-panel-content").
6. Set the viewport to 900x720. Eval: wait two animation frames (a promise resolved by requestAnimationFrame inside requestAnimationFrame). Then eval: empty window.__terminalSizeMessages (set its length to 0).
7. Click the button with aria-label exactly "Expand terminals". The terminal should be visible.
8. Run the inside check again.
9. The narrower window must shrink the terminal: (the cols of the last "resize" message in window.__terminalSizeMessages, or the initial cols if there is none) should become less than the initial cols.`, func(s *shelley) {
		s.initScript(terminalSizes)
		s.viewport(1280, 720)
		s.conversation("terminal panel layout test", "term-layout")
	})
}
