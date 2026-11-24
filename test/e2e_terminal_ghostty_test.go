package test

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/terminal-ghostty.spec.ts.

// ghostty-web is the default renderer. These cover what Shelley does on top
// of it; the other e2e_terminal_*_test.go cover the terminal panel on
// either renderer.

// ghosttyScript seeds a conversation at /c/slug whose directory holds t.sh,
// the lines of script, as the spec's runScript did, and returns the
// directory.
func (s *shelley) ghosttyScript(slug string, script ...string) string {
	_, dir := s.terminalConversation("terminal-ghostty", "terminal ghostty test", slug, map[string]string{
		"t.sh": strings.Join(script, "\n") + "\n",
	})
	return dir
}

// ghosttyFile has call create the file name in dir, for scripts that wait
// for it.
func (s *shelley) ghosttyFile(call, dir, name string) {
	s.call(call, func() string {
		s.write(dir+"/"+name, "")
		return "done"
	})
}

// canvasBackground is an eval of the color the canvas of the terminal sel
// matches paints its empty bottom row with.
func canvasBackground(sel string) string {
	return `(() => { const c = document.querySelector('` + sel + ` canvas'); const [r, g, b] = c.getContext("2d").getImageData(c.width / 2, c.height - 2, 1, 1).data; return "rgb(" + r + ", " + g + ", " + b + ")"; })()`
}

const (
	dark  = "rgb(26, 27, 38)"
	light = "rgb(248, 249, 250)"

	// ghosttyTerms defines the words the descriptions use.
	ghosttyTerms = `"The terminal" is '` + visibleTerminal + `' and "the input" is [data-testid="message-input"]. ` + termMenu + `"Read all output until it matches P" means: retry the following steps as one block until they pass (timeout 15s): fill the input with ""; choose the terminal menu item "Insert all output into input" (both clicks inside the block); the input's value must match P (eval with expect true). "Read the visible screen until it matches P" is the same with the menu item "Insert visible screen into input". "Set the theme to T" means what the theme menu does, the eval: localStorage.setItem("shelley-theme", "T"); document.documentElement.classList.toggle("dark", "T" === "dark"). `

	// runScript is the step that runs t.sh in a new terminal.
	runScript = `Navigate to /c/%s. Fill the input with "!bash t.sh" and click [data-testid="send-button"].`

	// settledRead defines reading the screen until it settles.
	settledRead = `"Read the visible screen until it settles" means: retry the following steps as one block until they pass (timeout 15s): eval window.__pass = (window.__pass || 0) + 1; fill the input with ""; choose the terminal menu item "Insert visible screen into input" (both clicks inside the block); then eval, with expect true, exactly: ` + "`" + `(() => { const v = document.querySelector('[data-testid="message-input"]').value; if (!v) return false; if (window.__readPass !== window.__pass) { window.__readPass = window.__pass; window.__last = window.__previous; window.__previous = v; } return window.__previous === window.__last; })()` + "`" + ` (the same screen twice running; window.__previous is the screen last read). `
)

func ghosttyDesc(title, slug, setup, steps string) string {
	return title + ` The test seeded a conversation at /c/` + slug + `, whose directory holds t.sh, which ` + setup + ` ` + ghosttyTerms + strings.ReplaceAll(steps, "RUN_SCRIPT", strings.Replace(runScript, "%s", slug, 1))
}

// ghostty-web can't change its palette in place: a theme switch reopens the
// terminal, which must get its screen back, keep talking to the same
// program, and leave focus where it was. The screen includes a gap, wide
// characters and a multi-code-point cluster, which Copy/Insert must keep.
func TestE2ETerminalGhosttyThemeSwitchKeys(t *testing.T) {
	e2e(t, useful(0), ghosttyDesc(`Survives theme switches and keeps Ctrl+V and Ctrl+Shift+C off paste and ^C.`, "gh-theme",
		`switches the terminal to raw mode without echo, prints the screen {{SCREEN}} (with a gap before the B, wide characters and a multi-code-point emoji), then reads raw input up to "q" and prints it as keys=<input, shell-quoted>.`,
		`window.__sockets lists the page's websockets in the order they opened, each as {termId: its URL's term_id parameter or null, outputs: how many "output" messages it received}; this terminal is the only terminal session on the server. "The background" is the eval `+"`"+canvasBackground(visibleTerminal)+"`"+`.
1. RUN_SCRIPT
2. The terminal should contain exactly 1 canvas and no ".xterm" (count 0).
3. Read all output until it contains {{SCREEN}} (eval document.querySelector('[data-testid="message-input"]').value.includes("{{SCREEN}}")).
4. The background must be exactly "`+light+`" (check once).
5. Eval: window.__focused = []; document.addEventListener("focusin", (e) => window.__focused.push(e.target.closest(".terminal-panel-content") ? "terminal" : "elsewhere")).
6. Focus the input (eval .focus()). Eval: window.__oldCanvas = the terminal's canvas.
7. Set the theme to dark. window.__oldCanvas.isConnected should become false. The background should become exactly "`+dark+`".
8. Read all output until it contains {{SCREEN}}. Then window.__focused must not contain "terminal" (check once).
9. A focused terminal stays focused: focus the terminal's input, '`+visibleTerminal+` textarea[aria-label="Terminal input"]' (eval .focus()). Eval: window.__oldCanvas = the terminal's canvas; window.__mark = window.__sockets.length.
10. Set the theme to light. window.__oldCanvas.isConnected should become false. The background should become exactly "`+light+`". The terminal's input should be focused (assert_focused).
11. Focus returns before the replacement socket opens. Wait for the replay without moving focus through the composer: eval window.__sockets.slice(window.__mark).some((s) => s.termId && s.outputs > 0) should become true.
12. Press Control+v, then Control+Shift+c, then q (press_key, on the focused terminal input).
13. There should be exactly 1 ".terminal-panel-tab-success".
14. Read all output until it matches /keys=\$'\\026'/ (Ctrl+V reached the program as ^V; Ctrl+Shift+C neither pasted nor sent ^C).`), func(s *shelley) {
		s.v("SCREEN", "ghostty-ready A"+strings.Repeat(" ", 8)+"B 漢字 👩\u200d💻")
		s.initScript(`window.__sockets = [];
const NativeWebSocket = window.WebSocket;
window.WebSocket = class extends NativeWebSocket {
  constructor(url, protocols) {
    super(url, protocols);
    const socket = { termId: new URL(url, location.href).searchParams.get("term_id"), outputs: 0 };
    window.__sockets.push(socket);
    this.addEventListener("message", (e) => {
      try {
        if (JSON.parse(e.data).type === "output") socket.outputs++;
      } catch {
        // Not a JSON message.
      }
    });
  }
};`)
		s.ghosttyScript("gh-theme",
			"stty raw -echo lnext undef",
			"printf 'ghostty-ready A\\033[24GB 漢字 👩\u200d💻\\r\\n'",
			"IFS= read -r -d q keys",
			"stty sane",
			`printf 'keys=%q\n' "$keys"`,
		)
	})
}

// A hidden terminal can't be measured. Reopening it must keep its size, not
// resize the program to the 80x24 default.
func TestE2ETerminalGhosttyThemeSwitchHiddenSize(t *testing.T) {
	e2e(t, useful(0), ghosttyDesc(`A theme switch leaves a hidden terminal's size alone.`, "gh-hidden",
		`prints "ready" and its size (stty size), prints "winch" and the new size whenever it is resized, and keeps running.`,
		`"The first terminal" is the eval document.querySelector('.terminal-panel-content [data-terminal-id="' + window.__firstId + '"]') and "the first tab" is '.terminal-panel-tab[title="bash t.sh"]'. An element is hidden when it has no box (getClientRects().length === 0) or computed visibility "hidden".
1. RUN_SCRIPT
2. Read all output until it matches /ready \d+ \d+/.
3. Eval: window.__firstId = the terminal's data-terminal-id; window.__oldCanvas = the first terminal's canvas.
4. Click the button with aria-label exactly "New terminal". There should be exactly 1 ".terminal-panel-tab-active .terminal-panel-tab-running". The first tab must not have the class "terminal-panel-tab-active" (assert_count '.terminal-panel-tab-active[title="bash t.sh"]' is 0). The first terminal should become hidden (eval, expect true).
5. Set the theme to dark. window.__oldCanvas.isConnected should become false. The first terminal should come to contain exactly 1 canvas (eval, expect true).
6. Click the first tab. The first terminal should become visible (eval: it has a box, expect true). The first terminal's canvas background (eval `+"`"+`(() => { const c = document.querySelector('.terminal-panel-content [data-terminal-id="' + window.__firstId + '"] canvas'); const [r, g, b] = c.getContext("2d").getImageData(c.width / 2, c.height - 2, 1, 1).data; return "rgb(" + r + ", " + g + ", " + b + ")"; })()`+"`"+`) should become exactly "`+dark+`".
7. Read all output until it matches /ready \d+ \d+/ and does not contain "winch" (both in one eval; the program was never resized).`), func(s *shelley) {
		s.ghosttyScript("gh-hidden",
			"trap 'echo winch $(stty size)' WINCH",
			"echo ready $(stty size)",
			"while :; do sleep 0.1; done",
		)
	})
}

func TestE2ETerminalGhosttyThemeSwitchWhileLoading(t *testing.T) {
	e2e(t, useful(0), ghosttyDesc(`A theme switch while ghostty-web loads still applies.`, "gh-loading",
		`prints "ghostty-ready" and then echoes what it reads. The server holds the page's request for ghostty-web until call "release" lets it through; call "requested" says whether the page asked for it yet ("true" or "false").`,
		`
1. RUN_SCRIPT
2. Call "requested" should return "true".
3. Set the theme to dark.
4. Call "release" (returns "released").
5. Read all output until it contains "ghostty-ready".
6. The terminal's canvas background (eval `+"`"+canvasBackground(visibleTerminal)+"`"+`) should become exactly "`+dark+`".`), func(s *shelley) {
		requested := s.signal("requested")
		release := s.gate("release")
		s.routeGhostty(func(w http.ResponseWriter, r *http.Request) {
			requested.Store(true)
			if release.wait(r) {
				s.pass.ServeHTTP(w, r)
			}
		})
		s.ghosttyScript("gh-loading", `printf 'ghostty-ready\n'`, "exec cat")
	})
}

// routeGhostty has handler answer the page's request for the ghostty-web
// bundle, which ui/scripts/build.js names after its content.
func (s *shelley) routeGhostty(handler http.HandlerFunc) {
	s.route("GET /assets/{file}", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.PathValue("file"), "ghostty-web-") {
			handler(w, r)
		} else {
			s.pass.ServeHTTP(w, r)
		}
	})
}

// ghosttyMissing has the server answer 404 for the ghostty-web bundle once
// open is called, or at once without one.
func (s *shelley) ghosttyMissing(open string) {
	var fail gate
	if open != "" {
		fail = s.gate(open)
	}
	s.routeGhostty(func(w http.ResponseWriter, r *http.Request) {
		if fail != nil && !fail.wait(r) {
			return
		}
		http.NotFound(w, r)
	})
}

func TestE2ETerminalGhosttyLoadFailure(t *testing.T) {
	e2e(t, useful(0), ghosttyDesc(`Says why when ghostty-web fails to load, and xterm.js can take over.`, "gh-404",
		`prints "xterm-took-over" and then echoes what it reads. The server answers 404 for ghostty-web.`,
		`
1. RUN_SCRIPT
2. The terminal should contain the text "Couldn't open the terminal". There should be exactly 1 ".terminal-panel-tab .terminal-panel-tab-error".
3. Choose the terminal menu item "Render with xterm.js". ".terminal-panel-tab-running" should be visible. The terminal should come not to contain "Couldn't open the terminal" (assert_text_not_contains).
4. Read all output until it contains "xterm-took-over".`), func(s *shelley) {
		s.ghosttyMissing("")
		s.ghosttyScript("gh-404", "echo xterm-took-over", "exec cat")
	})
}

func TestE2ETerminalGhosttyXtermChosenWhileLoading(t *testing.T) {
	e2e(t, useful(0), ghosttyDesc(`xterm.js chosen while ghostty-web loads takes over when the load fails.`, "gh-takeover",
		`prints "xterm-took-over" and then echoes what it reads. The server holds the page's request for ghostty-web until call "fail" has it answer 404.`,
		`
1. RUN_SCRIPT
2. Choose the terminal menu item "Render with xterm.js".
3. Call "fail" (returns "released").
4. ".terminal-panel-tab-running" should be visible. The terminal should not contain "Couldn't open the terminal" (assert_text_not_contains).
5. Read all output until it contains "xterm-took-over". Now that xterm.js has taken over, the terminal still must not contain "Couldn't open the terminal" (assert_text_not_contains).`), func(s *shelley) {
		s.ghosttyMissing("fail")
		s.ghosttyScript("gh-takeover", "echo xterm-took-over", "exec cat")
	})
}

func TestE2ETerminalGhosttyHistory(t *testing.T) {
	e2e(t, useful(0), ghosttyDesc(`Keeps 10,000 lines of history, as xterm.js does.`, "gh-history",
		`prints the numbers 1 to 10000, one per line, and then echoes what it reads.`,
		`
1. RUN_SCRIPT
2. Read all output until it contains a newline followed by "10000" (eval .value.includes("\n10000")).
3. Read all output until it matches /^1$/m (the first line is still there).`), func(s *shelley) {
		s.ghosttyScript("gh-history", "seq 1 10000", "exec cat")
	})
}

// xterm.js keeps a scrolled-back view on the same lines when output arrives,
// and returns to the bottom on input; ghostty-web jumps to the bottom on
// output. Once the history is full, new output evicts the oldest lines,
// which moves the viewed ones up.
func TestE2ETerminalGhosttyScrolledBackOutput(t *testing.T) {
	e2e(t, useful(0), ghosttyDesc(`Output leaves a scrolled-back terminal where it is, typing doesn't.`, "gh-scrollback",
		`prints the numbers 1 to 30000, waits for the file "go" (call "go" creates it), prints 30001 to 33000 and "more-output", and then echoes what it reads.`,
		settledRead+`The viewport is 1400x900 (wide, so fewer lines fill the history).
1. RUN_SCRIPT
2. Read the visible screen until it contains "30000".
3. Hover the center of the terminal's canvas, then wheel by x 0, y -1500 there.
4. Read the visible screen until it settles. window.__previous must not contain "30000" (check once).
5. Call "go" (returns "done"). Read all output until it contains "more-output".
6. Fill the input with "", choose the terminal menu item "Insert visible screen into input"; the input's value should be exactly window.__previous (eval with expect true): the view stayed put.
7. Typing right after a wheel, before any scroll animation is over: in one eval, dispatch on the terminal's canvas a new WheelEvent("wheel", {deltaY: -300, bubbles: true, cancelable: true}), then on the terminal's textarea a new KeyboardEvent("keydown", {key: "x", code: "KeyX", bubbles: true, cancelable: true}).
8. Read the visible screen until it settles. window.__previous must contain "more-output" (check once): the terminal went back to the bottom.`), func(s *shelley) {
		s.viewport(1400, 900)
		dir := s.ghosttyScript("gh-scrollback",
			"seq 1 30000",
			"while [ ! -e go ]; do sleep 0.05; done",
			"seq 30001 33000",
			"echo more-output",
			"exec cat",
		)
		s.ghosttyFile("go", dir, "go")
	})
}

// Repeated lines can't tell positions apart; the whole screen can.
func TestE2ETerminalGhosttyRepeatedLinesView(t *testing.T) {
	e2e(t, useful(0), ghosttyDesc(`A view scrolled back among repeated lines stays put.`, "gh-repeated",
		`prints "repeat" 30000 times, then "marker-2", "marker-3", ... up to "marker-<rows>" (one line short of a screen), waits for the file "go" (call "go" creates it), prints "repeat" 3000 more times and "more-output", and then echoes what it reads.`,
		`The viewport is 1400x900.
1. RUN_SCRIPT
2. Read the visible screen until it contains "marker-2" followed by a newline.
3. One row back, a repeated line over the markers: eval, dispatch on the terminal's canvas a new WheelEvent("wheel", {deltaY: -1, deltaMode: 1, bubbles: true}).
4. Read the visible screen until it matches /^repeat\nmarker-2\n/. Eval: window.__viewed = the input's value.
5. Call "go" (returns "done"). Read all output until it contains "more-output".
6. Fill the input with "", choose the terminal menu item "Insert visible screen into input"; the input's value should be exactly window.__viewed (eval with expect true).`), func(s *shelley) {
		s.viewport(1400, 900)
		dir := s.ghosttyScript("gh-repeated",
			"yes repeat | head -n 30000",
			"for i in $(seq 2 $(stty size | cut -d' ' -f1)); do echo marker-$i; done",
			"while [ ! -e go ]; do sleep 0.05; done",
			"yes repeat | head -n 3000",
			"echo more-output",
			"exec cat",
		)
		s.ghosttyFile("go", dir, "go")
	})
}

// Terminals sharing a ghostty emulator reuse each other's freed memory
// without clearing it.
func TestE2ETerminalGhosttyNewTerminalClean(t *testing.T) {
	e2e(t, useful(0), ghosttyDesc(`A new terminal shows none of a closed one's text.`, "gh-closed",
		`prints 3000 lines "closed-terminal <n> zzzzzzzzzzzzzzzzzzzzzzzzzzzzzz" (n from 1 to 3000) and then echoes what it reads; the directory also holds t2.sh, which prints the numbers 1 to 3000 and then echoes what it reads.`,
		`"The tab" is '.terminal-panel-tab[title="bash t.sh"]'.
1. RUN_SCRIPT
2. Read all output until it contains "closed-terminal 3000".
3. Click the tab's button with aria-label exactly "Close terminal". There should be no tab (count 0).
4. Fill the input with "!bash t2.sh" and click [data-testid="send-button"].
5. Read all output until it contains a newline followed by "3000" and does not contain "closed-terminal" (both in one eval).`), func(s *shelley) {
		dir := s.ghosttyScript("gh-closed",
			"seq -f 'closed-terminal %g zzzzzzzzzzzzzzzzzzzzzzzzzzzzzz' 3000",
			"exec cat",
		)
		s.write(dir+"/t2.sh", "seq 3000\nexec cat\n")
	})
}

// Each terminal has an emulator of tens of MB, which ghostty-web's
// leftover document listeners would keep after the terminal is gone.
func TestE2ETerminalGhosttyFreesEmulators(t *testing.T) {
	e2e(t, useful(0), ghosttyDesc(`Reopened and closed terminals free their emulators.`, "gh-free",
		`prints "ghostty-ready" and then echoes what it reads.`,
		`window.__memories holds a WeakRef to the WebAssembly memory of every WebAssembly instance the page made (each emulator has one). "Watch the emulators" means: eval window.__watched = window.__memories.filter((ref) => ref.deref()) (those alive now). "Wait until a watched emulator is freed" means: retry the following steps as one block until they pass (timeout 15s): collect_garbage; then eval window.__watched.filter((ref) => !ref.deref()).length > 0 must be true. "The tab" is '.terminal-panel-tab[title="bash t.sh"]'.
1. RUN_SCRIPT
2. Read all output until it contains "ghostty-ready".
3. Watch the emulators.
4. Set the theme to dark. The terminal's canvas background (eval `+"`"+canvasBackground(visibleTerminal)+"`"+`) should become exactly "`+dark+`".
5. Wait until a watched emulator is freed.
6. Watch the emulators again.
7. Click the tab's button with aria-label exactly "Close terminal". There should be no tab (count 0).
8. Wait until a watched emulator is freed.`), func(s *shelley) {
		s.initScript(`window.__memories = [];
const instantiate = WebAssembly.instantiate;
WebAssembly.instantiate = async function (...args) {
  const result = await Reflect.apply(instantiate, this, args);
  const instance = result instanceof WebAssembly.Instance ? result : result.instance;
  for (const value of Object.values(instance.exports)) {
    if (value instanceof WebAssembly.Memory) window.__memories.push(new WeakRef(value));
  }
  return result;
};`)
		s.ghosttyScript("gh-free", "echo ghostty-ready", "exec cat")
	})
}

// tabSwitchesRetainOutput is the test of a hidden terminal keeping what it
// was sent, on renderer.
func tabSwitchesRetainOutput(t *testing.T, useful lazycue.Useful, renderer, surface string) {
	slug := "gh-tabs-" + renderer
	e2e(t, useful, ghosttyDesc(`Tab switches retain output written while hidden (`+renderer+`).`, slug,
		`counts COUNT-001 to COUNT-100 in four batches of 25 lines: it prints the first batch, and each later batch once the test releases it (calls "release50", "release75" and "release100" each release one batch; the FIFO keeps output stopped while a tab is inspected, so new output cannot conceal a lost screen by immediately repainting it). Then it echoes what it reads. Terminals render with `+renderer+`.`,
		`window.__received is all the output the page's websocket for this script (the one whose URL has cmd "bash t.sh") received, decoded. Call "sockets" says how many terminal websockets the page has opened. "The first terminal" is the eval document.querySelector('[data-terminal-id="' + window.__firstId + '"]'), "the second terminal" the same with window.__secondId; "the first tab" is '.terminal-panel-tab[title="bash t.sh"]' and "the second tab" is the last ".terminal-panel-tab". An element is hidden when it has no box (getClientRects().length === 0) or computed visibility "hidden", visible otherwise. "The counts check for N" is the eval: JSON.stringify(document.querySelector('[data-testid="message-input"]').value.match(/COUNT-\d{3}/g)) === JSON.stringify(Array.from({length: N}, (_, i) => "COUNT-" + String(i + 1).padStart(3, "0"))).
1. RUN_SCRIPT
2. Eval: window.__firstId = the terminal's data-terminal-id; window.__surface = the first terminal's `+surface+`.
3. Read all output until it contains "COUNT-025".
4. Click the button with aria-label exactly "New terminal". The first terminal should become hidden (eval, expect true). Call "sockets" should return "2". Eval: window.__secondId = the terminal's data-terminal-id (the new, visible one).
5. Then, for N = 50, 75 and 100 in turn (write the steps out for each N; NNN is N with three digits):
   a. Call "releaseN" (returns "released").
   b. window.__received should come to contain "COUNT-NNN" (eval, expect true).
   c. The first terminal must be hidden (eval, expect true). Click the first tab. The first terminal should become visible.
   d. Read all output until it contains "COUNT-NNN".
   e. The counts check for N must be true: every count, once, in order.
   f. window.__surface.isConnected must be true (the first terminal's view was kept, not rebuilt).
   g. Click the second tab. The second terminal should become visible.
   h. Read all output until it does not contain "COUNT-" and is not empty (one eval: the second terminal's output has none of the counter's).
6. Click the first tab. Read the visible screen until it contains "COUNT-100".
7. Call "sockets" should return "2".`), func(s *shelley) {
		s.terminalRenderer(renderer)
		s.initScript(`window.__received = "";
const NativeWebSocket = window.WebSocket;
window.WebSocket = class extends NativeWebSocket {
  constructor(url, protocols) {
    super(url, protocols);
    const address = new URL(url, location.href);
    if (address.pathname !== "/api/exec-ws" || address.searchParams.get("cmd") !== "bash t.sh") return;
    this.addEventListener("message", (e) => {
      const message = JSON.parse(e.data);
      if (message.type === "output") {
        window.__received += new TextDecoder().decode(Uint8Array.from(atob(message.data), (c) => c.charCodeAt(0)));
      }
    });
  }
};`)
		dir := s.ghosttyScript(slug,
			"mkfifo gate",
			"exec 3<>gate",
			"for batch in 0 1 2 3; do",
			`  for ((i=batch*25+1; i<=(batch+1)*25; i++)); do printf 'COUNT-%03d\n' "$i"; done`,
			"  read -r -u 3",
			"done",
			"exec cat",
		)
		// The page attaches to every terminal on the server; this test's
		// server has only this test's.
		sockets := s.counter("sockets")
		s.route("GET /api/exec-ws", func(w http.ResponseWriter, r *http.Request) {
			sockets.Add(1)
			s.pass.ServeHTTP(w, r)
		})
		for _, end := range []string{"50", "75", "100"} {
			s.call("release"+end, sync.OnceValue(func() string {
				f, err := os.OpenFile(dir+"/gate", os.O_WRONLY, 0)
				if err != nil {
					return err.Error()
				}
				defer f.Close()
				if _, err := f.WriteString("next\n"); err != nil {
					return err.Error()
				}
				return "released"
			}))
		}
	})
}

func TestE2ETerminalTabSwitchRetainsOutputGhostty(t *testing.T) {
	tabSwitchesRetainOutput(t, useful(0), "ghostty", "canvas")
}

func TestE2ETerminalTabSwitchRetainsOutputXterm(t *testing.T) {
	tabSwitchesRetainOutput(t, useful(0), "xterm", ".xterm-screen")
}

// While a program tracks the mouse (Claude Code, tmux, vim with mouse=a), a
// plain drag goes to it and Shift+drag selects, as in desktop terminals and
// xterm.js. Stock ghostty-web reports the Shift+drag too, which clears the
// selection before it can be copied.
func TestE2ETerminalGhosttyShiftDragCopies(t *testing.T) {
	e2e(t, useful(0), ghosttyDesc(`Shift+drag selects and copies while the program tracks the mouse.`, "gh-shift-drag",
		`switches the terminal to raw mode, turns on mouse tracking (SGR mouse reports), prints "select-me-please" and then reads (and discards) its input.`,
		`window.__copied lists the texts the page wrote to the clipboard (navigator.clipboard.write or writeText). Offsets below are CSS pixels from the top-left corner of the terminal's canvas, whose width is W and height H.
1. RUN_SCRIPT
2. Read all output until it contains "select-me-please".
3. Hold Shift (key_down Shift). Hover the canvas at (2, 2). Press the left mouse button (mouse_down). Hover the canvas at (W/2, H/2) in 5 steps (count 5), then at (W-2, H-2) in 5 steps. Release the button (mouse_up). Release Shift (key_up Shift).
4. window.__copied should come to have an entry containing "select-me-please" (eval, expect true).`), func(s *shelley) {
		s.initScript(`const copied = [];
Object.defineProperty(window, "__copied", { value: copied });
navigator.clipboard.writeText = async (text) => void copied.push(text);
// ghostty-web writes a ClipboardItem when it can, and only falls back to
// writeText when that fails, as it does without clipboard permission.
navigator.clipboard.write = async (items) => {
  for (const item of items) {
    if (item.types.includes("text/plain")) copied.push(await (await item.getType("text/plain")).text());
  }
};`)
		s.ghosttyScript("gh-shift-drag",
			"stty raw -echo",
			`printf '\033[?1000h\033[?1006h'`,
			`printf 'select-me-please\r\n'`,
			"exec cat >/dev/null",
		)
	})
}

// serveFile has the page's requests for path return the content of the file
// name in dir, which a script writes, so that steps can fetch it.
func (s *shelley) serveFile(path, dir, name string) {
	s.route("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(dir + "/" + name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Write(b)
	})
}

// resizeBurst is the test of the grid fitting after a burst of resizes that
// move the divider by delta pixels three times.
func resizeBurst(t *testing.T, useful lazycue.Useful, direction string, delta int) {
	slug := "gh-resize-" + direction
	e2e(t, useful, ghosttyDesc(`Fits the grid after a burst of resizes (`+direction+`).`, slug,
		`prints "ghostty-ready" and then echoes what it reads.`,
		`"The handle" is ".terminal-panel-resize-handle". "The slack" is the eval (() => { const el = document.querySelector('`+visibleTerminal+`'); return el.firstElementChild.getBoundingClientRect().height - el.querySelector("canvas").getBoundingClientRect().height; })().
1. RUN_SCRIPT
2. Read all output until it contains "ghostty-ready".
3. Eval: const b = the handle's getBoundingClientRect(); window.__handle = {x: b.x + b.width / 2, y: b.y + b.height / 2, top: b.y}.
4. Drag the divider in a short burst, faster than ghostty-web's FitAddon accepts resizes: hover the point `+"`({x: window.__handle.x, y: window.__handle.y})`"+`, mouse_down, hover the point `+"`"+fmt.Sprintf("({x: window.__handle.x, y: window.__handle.y + %d})", delta*3)+"`"+` with count 3, mouse_up.
5. The handle's getBoundingClientRect().y should come to be within 0.5 of `+fmt.Sprintf("window.__handle.top + %d", delta*3)+` (eval, expect true).
6. Within one row (about 17px) and not overflowing: the slack should become at least 0 (eval, expect true), and then the slack should become less than 20 (a second eval).`), func(s *shelley) {
		s.ghosttyScript(slug, `printf 'ghostty-ready\n'`, "exec cat")
	})
}

func TestE2ETerminalGhosttyResizeBurstGrowing(t *testing.T) {
	resizeBurst(t, useful(0), "growing", -60)
}

func TestE2ETerminalGhosttyResizeBurstShrinking(t *testing.T) {
	resizeBurst(t, useful(0), "shrinking", 60)
}

// On a phone, a drag scrolls back through output instead of focusing the
// terminal and summoning the keyboard.
// xterm.js scrolls on touch itself; ghostty-web doesn't, so Shelley does.
// Either way a drag scrolls as far as the finger moves: once, not twice.
//
// touchDragScrolls is the test of a touch drag scrolling a terminal drawn by
// renderer.
func touchDragScrolls(t *testing.T, useful lazycue.Useful, renderer string) {
	slug := "gh-touch-" + renderer
	e2e(t, useful, ghosttyDesc(`A touch drag scrolls back as far as the finger moves, without focusing (`+renderer+`).`, slug,
		`writes the terminal's row count (stty size) to the file "rows", prints the numbers 1 to 300, one per line, and then echoes what it reads. Terminals render with `+renderer+`; fetching /e2e/rows returns the file "rows".`,
		`"The top line" of a screen read is parseInt of the input's value (base 10).
1. RUN_SCRIPT
2. Read all output until it contains a newline followed by "300".
3. Retry the following steps as one block until they pass (timeout 15s): fill the input with ""; choose the terminal menu item "Insert visible screen into input"; eval: const n = parseInt(input value, 10); if it is NaN return false; else window.__before = n and return true (expect true).
4. Eval (awaited): const b = the terminal's getBoundingClientRect(); const rows = parseInt(await (await fetch("/e2e/rows")).text(), 10); window.__drag = {x: b.x + b.width / 2, from: b.y + 10, to: b.y + b.height - 10}; window.__drag.rows = (window.__drag.to - window.__drag.from) / (b.height / rows); window.__drag.moves = Math.floor((window.__drag.to - window.__drag.from) / 10). Note the value of window.__drag.moves (call it M).
5. Finger moves down: content follows it, older lines come into view. Touch start at the point `+"`({x: window.__drag.x, y: window.__drag.from})`"+`; touch move to the point `+"`({x: window.__drag.x, y: window.__drag.from + 10 * window.__drag.moves})`"+` with count M (a move every 10px); touch end.
6. Retry the following steps as one block until they pass (timeout 15s): fill the input with ""; choose "Insert visible screen into input"; the top line must be less than window.__before - window.__drag.rows * 0.7 (eval, expect true).
7. Settled, the same top line twice running: retry the following steps as one block until they pass (timeout 15s): eval window.__pass = (window.__pass || 0) + 1; fill the input with ""; choose "Insert visible screen into input"; then eval, with expect true, exactly: `+"`"+`(() => { const n = parseInt(document.querySelector('[data-testid="message-input"]').value, 10); if (isNaN(n)) return false; if (window.__readPass !== window.__pass) { window.__readPass = window.__pass; window.__lastTop = window.__top; window.__top = n; } return window.__top === window.__lastTop; })()`+"`"+`.
8. window.__before - window.__top must be less than window.__drag.rows * 1.3 (check once): it scrolled once, not twice.
9. The terminal input ('textarea[aria-label="Terminal input"]') must not be focused (eval: document.activeElement is not one, expect true).`), func(s *shelley) {
		s.terminalRenderer(renderer)
		dir := s.ghosttyScript(slug, "stty size | cut -d' ' -f1 > rows", "seq 1 300", "exec cat")
		s.serveFile("/e2e/rows", dir, "rows")
	})
}

func TestE2ETerminalTouchDragScrollsGhostty(t *testing.T) { touchDragScrolls(t, useful(0), "ghostty") }

func TestE2ETerminalTouchDragScrollsXterm(t *testing.T) { touchDragScrolls(t, useful(0), "xterm") }

// Long URLs (an OAuth sign-in link) soft-wrap over several rows. A click on
// any of them opens the whole URL, as in xterm.js and desktop terminals;
// ghostty-web on its own finds URLs a row at a time, so the first row opened
// a truncated URL and the rest opened nothing. A URL that fills its row and
// then ends with a newline is not joined to the next line.
func TestE2ETerminalGhosttyCtrlClickWrappedURL(t *testing.T) {
	e2e(t, useful(0), ghosttyDesc(`Ctrl+click opens a URL that wraps over several rows.`, "gh-url",
		`prints "ghostty-ready"; once the file "go" exists (call "go" creates it) it writes the terminal size (stty size: "<rows> <cols>") to the file "size", clears the screen and homes the cursor, then prints: https://example.com/ followed by 2*cols "a"s and "/end" (a URL wrapping over three rows); https://example.org/ followed by cols-20 "b"s (exactly one row: the newline, not a wrap, ends it); "not-part-of-the-url"; and an OSC 8 hyperlink to https://target.example/ whose text is https://example.net/ followed by cols "c"s (two rows). Once the file "more" exists (call "more" creates it) it prints the numbers 1 to 2*rows, pushing it all into the scrollback, then echoes what it reads. Fetching /e2e/size returns the file "size".`,
		`window.__opened lists the URLs the page opened (window.open). "Clear the opened list" means eval window.__opened.length = 0. "The opened list should be L" means: eval JSON.stringify(window.__opened) === JSON.stringify(L) should become true. "Cell (R, C)" is the point `+"`"+`(() => { const r = document.querySelector('`+visibleTerminal+` canvas').getBoundingClientRect(); return {x: r.left + (C + 0.5) * (r.width / window.__size.cols), y: r.top + (R + 0.5) * (r.height / window.__size.rows)}; })()`+"`"+`, with R and C written in as numbers or expressions (cols means window.__size.cols). "Ctrl+click cell (R, C)" means: hover cell (R, C); key_down Control; click cell (R, C); key_up Control.
1. RUN_SCRIPT
2. Read all output until it contains "ghostty-ready".
3. Call "go" (returns "done"). Read the visible screen until it contains "https://example.net/".
4. Eval (awaited): const [rows, cols] = (await (await fetch("/e2e/size")).text()).trim().split(" ").map(Number); window.__size = {rows, cols}; window.__long = "https://example.com/" + "a".repeat(cols * 2) + "/end"; window.__short = "https://example.org/" + "b".repeat(cols - 20).
5. The URL's three rows: for each of the cells (0, 2), (1, cols - 1) and (2, 1), in turn: Ctrl+click it; the opened list should be [window.__long]; clear the opened list.
6. The one-row URL: Ctrl+click cell (3, cols - 1); the opened list should be [window.__short]; clear the opened list.
7. The line after it opens nothing: Ctrl+click cell (4, 2), then Ctrl+click cell (3, 5) (a click that does open something, to wait for); the opened list should be [window.__short]; clear the opened list.
8. The hyperlink's target, not its text, from either row, second row first: for rows 6 and then 5: Ctrl+click cell (row, 3); the opened list should be ["https://target.example/"]; clear the opened list.
9. The pointer shows it's a link: hover cell (4, 2); the canvas's style.cursor should become "text". Hover cell (1, 3); it should become "pointer".
10. Scrolled back to, from the scrollback, where ghostty-web keeps no wrap flags: call "more" (returns "done"). Read the visible screen until it contains a newline followed by the number 2 * window.__size.rows.
11. Retry the following steps as one block until they pass (timeout 30s): hover cell (0, 0) (inserting the screen moved the pointer to the menu); wheel by x 0 and y minus half the canvas's height (the canvas's getBoundingClientRect().height / 2, measured, written as a negative number); fill the input with ""; choose the terminal menu item "Insert visible screen into input"; the input's value must match /^https:\/\/example\.com\/a+\naaa/m (eval, expect true).
12. Eval: window.__top = the index of the first line of the input's value (split on "\n") that starts with "https://example.com".
13. Ctrl+click cell (window.__top + 1, 3); the opened list should be [window.__long].`), func(s *shelley) {
		s.initScript(`const opened = [];
Object.defineProperty(window, "__opened", { value: opened });
window.open = (url) => (opened.push(String(url)), null);`)
		dir := s.ghosttyScript("gh-url",
			"echo ghostty-ready",
			"while [ ! -e go ]; do sleep 0.05; done",
			"stty size > size",
			"cols=$(cut -d' ' -f2 size)",
			`pad() { head -c "$1" /dev/zero | tr '\0' "$2"; }`,
			// Clear and home, so the wrapped URL starts on the top row.
			`printf '\033[H\033[2J'`,
			`printf 'https://example.com/%s/end\n' "$(pad $((cols * 2)) a)"`,
			// Exactly one row: the newline, not a wrap, ends it.
			`printf 'https://example.org/%s\n' "$(pad $((cols - 20)) b)"`,
			"echo not-part-of-the-url",
			// An OSC 8 hyperlink whose wrapped text is another URL.
			`printf '\033]8;;https://target.example/\033\\%s\033]8;;\033\\\n' "https://example.net/$(pad "$cols" c)"`,
			// Then push it all into the scrollback.
			"while [ ! -e more ]; do sleep 0.05; done",
			"seq 1 $(( $(cut -d' ' -f1 size) * 2 ))",
			"exec cat",
		)
		s.ghosttyFile("go", dir, "go")
		s.ghosttyFile("more", dir, "more")
		s.serveFile("/e2e/size", dir, "size")
	})
}
