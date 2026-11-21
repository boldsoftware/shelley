package test

import (
	"testing"

	lazycue "github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/terminal-webgl.spec.ts.

// webglTerminal prepares a WebGL test: at 1x the WebGL renderer rounds cells
// to whole pixels (8px rather than the DOM renderer's 8.4px), so the two
// renderers fit different column counts. The WebGL renderer is xterm.js's;
// ghostty-web always draws to a canvas.
func webglTerminal(s *shelley) {
	d := lazycue.Pixel5
	d.Scale = 1
	s.device(d)
	s.terminalRenderer("xterm")
	s.conversation("terminal webgl test", "term-webgl")
}

// openWebGLTerminal is the description of opening the test's terminal.
const openWebGLTerminal = `1. Navigate to /c/term-webgl and ` + openTerminalPhone + `.
2. The terminal, '` + visibleTerminal + `', should become visible (wait up to 30s).
`

func TestE2ETerminalWebGLFallsBackToDOM(t *testing.T) {
	e2e(t, `The terminal renders with WebGL and falls back to the DOM on context loss. The test seeded a conversation at /c/term-webgl; terminals render with xterm.js, on a phone screen at device scale 1. window.__terminalCols lists the cols of every init and resize message the page's terminals sent their server, in order. "The WebGL canvas count" is the eval [...document.querySelector('`+visibleTerminal+`').querySelectorAll("canvas")].filter(c => c.getContext("webgl2")).length.
`+openWebGLTerminal+`3. The WebGL renderer replaces the DOM renderer's rows with a canvas: the WebGL canvas count should become 1, and the terminal should contain no ".xterm-rows" (count 0).
4. The last entry of window.__terminalCols should become greater than 0. Record it in a window variable as the WebGL cols.
5. A context that is not restored (too many live contexts) must not leave the terminal blank. Eval: for every canvas in the terminal, c.getContext("webgl2")?.getExtension("WEBGL_lose_context")?.loseContext().
6. xterm waits briefly for a restore, then the DOM renderer takes over: the terminal's ".xterm-rows" should become visible (wait up to 10s) and contain "$". Then the WebGL canvas count must be 0 (check it once, right away).
7. The terminal refits to the DOM renderer's wider cells: the last entry of window.__terminalCols should become less than the WebGL cols.`, func(s *shelley) {
		s.initScript(`const cols = [];
Object.defineProperty(window, "__terminalCols", { value: cols });
const send = WebSocket.prototype.send;
WebSocket.prototype.send = function (data) {
  if (typeof data === "string" && data.startsWith("{")) {
    const message = JSON.parse(data);
    if (message.type === "init" || message.type === "resize") cols.push(message.cols);
  }
  Reflect.apply(send, this, [data]);
};`)
		webglTerminal(s)
	})
}

func TestE2ETerminalWebGLDOMWithoutWebGL2(t *testing.T) {
	e2e(t, `The terminal renders with the DOM without WebGL2. The test seeded a conversation at /c/term-webgl; terminals render with xterm.js, on a phone screen at device scale 1, and the browser offers no WebGL2 (canvas getContext("webgl2") returns null).
`+openWebGLTerminal+`3. The terminal's ".xterm-rows" should contain "$".`, func(s *shelley) {
		s.initScript(`const getContext = HTMLCanvasElement.prototype.getContext;
HTMLCanvasElement.prototype.getContext = function (type, ...args) {
  return type === "webgl2" ? null : Reflect.apply(getContext, this, [type, ...args]);
};`)
		webglTerminal(s)
	})
}
