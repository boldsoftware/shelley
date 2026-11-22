package browse

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"shelley.exe.dev/llm"
)

// emulateTestEnv holds shared scaffolding for emulation tests that verify
// user-agent override behaviour. Call newEmulateTestEnv from each test.
type emulateTestEnv struct {
	ctx          context.Context
	emuTool      *llm.Tool
	getUserAgent func(t *testing.T) string
	baselineUA   string
}

// newEmulateTestEnv starts a local HTTP server that echoes the User-Agent
// header, initialises BrowseTools, and captures the browser's baseline UA.
// Registers cleanup via t.Cleanup. Skips if the browser is unavailable.
func newEmulateTestEnv(t *testing.T) *emulateTestEnv {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start listener: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/ua", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><pre id="ua">%s</pre></body></html>`, html.EscapeString(r.UserAgent()))
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)

	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() { tools.Close() })

	browserTool := tools.CombinedTool()
	baseURL := fmt.Sprintf("http://127.0.0.1:%d/ua", port)

	getUserAgent := func(t *testing.T) string {
		t.Helper()
		toolOut := browserTool.Run(ctx, []byte(fmt.Sprintf(`{"action": "navigate", "url": %q}`, baseURL)))
		if toolOut.Error != nil {
			if strings.Contains(toolOut.Error.Error(), "failed to start browser") {
				t.Skip("Browser automation not available in this environment")
			}
			t.Fatalf("Navigation error: %v", toolOut.Error)
		}
		browserCtx, err := tools.GetBrowserContext()
		if err != nil {
			t.Fatalf("Failed to get browser context: %v", err)
		}
		var ua string
		if err := chromedp.Run(browserCtx, chromedp.Text("#ua", &ua)); err != nil {
			t.Fatalf("Failed to read user agent from page: %v", err)
		}
		return ua
	}

	baselineUA := getUserAgent(t)
	t.Logf("Baseline UA: %s", baselineUA)

	return &emulateTestEnv{
		ctx:          ctx,
		emuTool:      tools.CombinedTool(),
		getUserAgent: getUserAgent,
		baselineUA:   baselineUA,
	}
}

// TestEmulateDeviceSwitchClearsUserAgent verifies that switching from a mobile
// preset (which sets a user agent override) to a desktop preset (which has no
// user agent) correctly clears the mobile user agent instead of leaking it.
// Not parallel: shares browser process via NewBrowseTools.
func TestEmulateDeviceSwitchClearsUserAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser emulation test in short mode")
	}

	env := newEmulateTestEnv(t)

	// Step 1: Emulate iphone_14 — UA should contain "iPhone".
	toolOut := env.emuTool.Run(env.ctx, []byte(`{"action": "emulate_device", "device": "iphone_14"}`))
	if toolOut.Error != nil {
		if strings.Contains(toolOut.Error.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Emulate iphone_14 error: %v", toolOut.Error)
	}

	ua := env.getUserAgent(t)
	if !strings.Contains(ua, "iPhone") {
		t.Fatalf("After iphone_14 emulation, expected UA to contain 'iPhone', got: %s", ua)
	}
	t.Logf("After iphone_14: UA=%s", ua)

	// Step 2: Switch to desktop_hd — UA should NOT contain "iPhone".
	toolOut = env.emuTool.Run(env.ctx, []byte(`{"action": "emulate_device", "device": "desktop_hd"}`))
	if toolOut.Error != nil {
		t.Fatalf("Emulate desktop_hd error: %v", toolOut.Error)
	}

	ua = env.getUserAgent(t)
	if strings.Contains(ua, "iPhone") {
		t.Fatalf("After desktop_hd emulation, UA still contains 'iPhone' (leaked mobile UA): %s", ua)
	}
	// Verify the override was truly cleared (not just emptied to "") by
	// confirming the UA is back to the browser's baseline.
	if ua != env.baselineUA {
		t.Fatalf("After desktop_hd emulation, expected UA to match baseline %q, got: %s", env.baselineUA, ua)
	}
	t.Logf("After desktop_hd: UA=%s", ua)
}

// TestEmulateDeviceToCustomClearsUserAgent verifies that switching from a mobile
// preset to a custom viewport clears the mobile UA override.
// Not parallel: shares browser process via NewBrowseTools.
func TestEmulateDeviceToCustomClearsUserAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser emulation test in short mode")
	}

	env := newEmulateTestEnv(t)

	// Step 1: Emulate iphone_14 — UA should contain "iPhone".
	toolOut := env.emuTool.Run(env.ctx, []byte(`{"action": "emulate_device", "device": "iphone_14"}`))
	if toolOut.Error != nil {
		if strings.Contains(toolOut.Error.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Emulate iphone_14 error: %v", toolOut.Error)
	}

	ua := env.getUserAgent(t)
	if !strings.Contains(ua, "iPhone") {
		t.Fatalf("After iphone_14 emulation, expected UA to contain 'iPhone', got: %s", ua)
	}
	t.Logf("After iphone_14: UA=%s", ua)

	// Step 2: Switch to a custom viewport — UA should revert to baseline.
	toolOut = env.emuTool.Run(env.ctx, []byte(`{"action": "emulate_custom", "width": 1280, "height": 800}`))
	if toolOut.Error != nil {
		t.Fatalf("Emulate custom error: %v", toolOut.Error)
	}

	ua = env.getUserAgent(t)
	if strings.Contains(ua, "iPhone") {
		t.Fatalf("After custom emulation, UA still contains 'iPhone' (leaked mobile UA): %s", ua)
	}
	if ua != env.baselineUA {
		t.Fatalf("After custom emulation, expected UA to match baseline %q, got: %s", env.baselineUA, ua)
	}
	t.Logf("After custom: UA=%s", ua)
}

// colorSchemePage paints the body from a prefers-color-scheme media query
// (white or black) and the square at x=100..200 from a matchMedia change
// listener (yellow for light, blue for dark), so screenshots show whether both
// CSS and script saw the scheme. The square fades like a theme switch, and
// only starts to once the listener has run. ?busy=ms makes the listener hog
// the page for that long, ?fade=ms sets the fade's length, and ?hostile then
// replaces matchMedia (always light),
// requestAnimationFrame and setTimeout (never call back).
const colorSchemePage = `<!doctype html>
<style>
  html, body { margin: 0; height: 100%; }
  body { background: rgb(255, 255, 255); }
  @media (prefers-color-scheme: dark) { body { background: rgb(0, 0, 0); } }
  #js { position: absolute; left: 100px; top: 0; width: 100px; height: 100px; transition: background-color 400ms linear; }
  .js-light #js { background: rgb(255, 255, 0); }
  .js-dark #js { background: rgb(0, 0, 255); }
</style>
<div id="js"></div>
<script>
  const busy = Number(new URLSearchParams(location.search).get('busy'));
  const fade = new URLSearchParams(location.search).get('fade');
  if (fade) document.getElementById('js').style.transitionDuration = fade + 'ms';
  const mq = matchMedia('(prefers-color-scheme: dark)');
  const sync = () => { document.documentElement.className = mq.matches ? 'js-dark' : 'js-light'; };
  mq.addEventListener('change', () => { for (const end = Date.now() + busy; Date.now() < end;); sync(); });
  sync();
  if (new URLSearchParams(location.search).has('hostile')) {
    window.matchMedia = () => ({ matches: false, addEventListener() {} });
    window.requestAnimationFrame = window.setTimeout = () => 0;
  }
</script>`

var (
	white, black = [3]uint8{255, 255, 255}, [3]uint8{0, 0, 0}
	yellow, blue = [3]uint8{255, 255, 0}, [3]uint8{0, 0, 255}
)

// colorSchemeEnv drives the browser tool against colorSchemePage.
type colorSchemeEnv struct {
	ctx   context.Context
	tools *BrowseTools
	tool  *llm.Tool
	url   string
}

// newColorSchemeEnv serves colorSchemePage and opens it, skipping the test if
// there is no browser.
func newColorSchemeEnv(t *testing.T) *colorSchemeEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping browser color scheme test in short mode")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, colorSchemePage)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(tools.Close)
	e := &colorSchemeEnv{ctx: ctx, tools: tools, tool: tools.CombinedTool(), url: server.URL}
	out := e.tool.Run(ctx, []byte(fmt.Sprintf(`{"action":"navigate","url":%q}`, server.URL)))
	if out.Error != nil {
		if browserUnavailable(out.Error.Error()) {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("navigate: %v", out.Error)
	}
	return e
}

func (e *colorSchemeEnv) run(t *testing.T, input string) string {
	t.Helper()
	out := e.tool.Run(e.ctx, []byte(input))
	if out.Error != nil {
		t.Fatalf("%s: %v", input, out.Error)
	}
	return out.LLMContent[0].Text
}

// eval returns the JSON result of a JavaScript expression.
func (e *colorSchemeEnv) eval(t *testing.T, expression string) string {
	t.Helper()
	text := e.run(t, fmt.Sprintf(`{"action":"eval","expression":%q}`, expression))
	return strings.TrimSuffix(strings.TrimPrefix(text, "<javascript_result>"), "</javascript_result>")
}

func (e *colorSchemeEnv) wantMatches(t *testing.T, query string, want bool) {
	t.Helper()
	if got := e.eval(t, fmt.Sprintf("matchMedia(%q).matches", query)); got != fmt.Sprint(want) {
		t.Fatalf("matchMedia(%q).matches = %s, want %v", query, got, want)
	}
}

// fail runs a tool call that must fail with an error containing want.
func (e *colorSchemeEnv) fail(t *testing.T, input, want string) {
	t.Helper()
	if out := e.tool.Run(e.ctx, []byte(input)); out.Error == nil || !strings.Contains(out.Error.Error(), want) {
		t.Fatalf("%s: error %v, want it to contain %q", input, out.Error, want)
	}
}

// screenshot takes a screenshot and checks the body (CSS) and the square
// (script) are exactly the given colors, so a capture taken mid-fade fails,
// and that it is labelled with the color scheme ("" for none).
func (e *colorSchemeEnv) screenshot(t *testing.T, scheme string, body, square [3]uint8) {
	t.Helper()
	out := e.tool.Run(e.ctx, []byte(`{"action":"screenshot"}`))
	if out.Error != nil {
		t.Fatalf("screenshot: %v", out.Error)
	}
	display := out.Display.(map[string]any)
	t.Cleanup(func() { os.Remove(display["path"].(string)) })
	if got, _ := display["color_scheme"].(string); got != scheme {
		t.Errorf("screenshot display color_scheme = %q, want %q", got, scheme)
	}
	if text := out.LLMContent[0].Text; strings.Contains(text, "prefers-color-scheme: "+scheme) != (scheme != "") {
		t.Errorf("screenshot output %q, want it to name the color scheme %q", text, scheme)
	}
	data, err := base64.StdEncoding.DecodeString(out.LLMContent[1].Data)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		x, y int
		want [3]uint8
	}{{"body", 50, 50, body}, {"script-styled square", 150, 50, square}} {
		r, g, b, _ := img.At(c.x, c.y).RGBA()
		if got := [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}; got != c.want {
			t.Errorf("%s pixel = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestEmulateColorScheme drives prefers-color-scheme through the browser tool
// the way an agent would: set it, check it survives navigation, media type
// changes and a browser restart, screenshot both themes, and clear it again.
// Not parallel: shares browser process via NewBrowseTools.
func TestEmulateColorScheme(t *testing.T) {
	e := newColorSchemeEnv(t)
	// Same server, different origin: emulation must survive cross-origin navigation.
	otherOrigin := strings.Replace(e.url, "127.0.0.1", "localhost", 1)
	dark := "(prefers-color-scheme: dark)"

	e.wantMatches(t, dark, false)
	e.wantMatches(t, "(prefers-color-scheme: light)", true)
	e.screenshot(t, "", white, yellow)

	// The tool returns once the page shows the new theme, fade included.
	if out := e.run(t, `{"action":"emulate_color_scheme","color_scheme":"dark"}`); !strings.Contains(out, "prefers-color-scheme: dark") {
		t.Errorf("emulate_color_scheme dark output %q does not report the resulting scheme", out)
	}
	e.screenshot(t, "dark", black, blue)
	e.run(t, `{"action":"emulate_color_scheme","color_scheme":"light"}`)
	e.screenshot(t, "light", white, yellow)
	e.run(t, `{"action":"emulate_color_scheme","color_scheme":"dark"}`)

	e.run(t, fmt.Sprintf(`{"action":"navigate","url":%q}`, otherOrigin))
	e.wantMatches(t, dark, true)

	// Chrome's setEmulatedMedia replaces the media type and features together.
	e.run(t, `{"action":"emulate_media","media":"print"}`)
	e.wantMatches(t, dark, true)
	e.run(t, `{"action":"emulate_color_scheme","color_scheme":"light"}`)
	e.wantMatches(t, "print", true)
	e.wantMatches(t, dark, false)
	e.run(t, `{"action":"emulate_color_scheme","color_scheme":"dark"}`)
	e.run(t, `{"action":"emulate_media","media":""}`)
	e.wantMatches(t, "print", false)
	e.wantMatches(t, dark, true)

	// The browser restarts after an idle shutdown or a crash; the scheme sticks.
	e.tools.mux.Lock()
	e.tools.browserCtxCancel()
	e.tools.mux.Unlock()
	e.run(t, fmt.Sprintf(`{"action":"navigate","url":%q}`, e.url))
	e.wantMatches(t, dark, true)
	e.screenshot(t, "dark", black, blue)

	if out := e.run(t, `{"action":"emulate_color_scheme","color_scheme":"system"}`); !strings.Contains(out, "prefers-color-scheme: light") {
		t.Errorf("emulate_color_scheme system output %q does not report the resulting scheme", out)
	}
	e.screenshot(t, "", white, yellow)
}

// TestEmulateColorSchemeBusyPage checks that a change a busy page can't take
// in time still sticks, and the error and later screenshots say so, whether
// the page was busy before Chrome got the change or in the listener it set off.
func TestEmulateColorSchemeBusyPage(t *testing.T) {
	e := newColorSchemeEnv(t)
	e.run(t, `{"action":"emulate_color_scheme","color_scheme":"light"}`)
	// Busy before the change: Chrome can't confirm it in time.
	browserCtx, err := e.tools.GetBrowserContext()
	if err != nil {
		t.Fatal(err)
	}
	busy := make(chan struct{})
	var once sync.Once
	chromedp.ListenTarget(browserCtx, func(ev any) {
		if ev, ok := ev.(*runtime.EventConsoleAPICalled); ok && len(ev.Args) > 0 && string(ev.Args[0].Value) == `"busy"` {
			once.Do(func() { close(busy) })
		}
	})
	e.eval(t, `setTimeout(() => { console.log("busy"); for (const end = Date.now() + 1000; Date.now() < end;); }), true`)
	select {
	case <-busy:
	case <-e.ctx.Done():
		t.Fatal("page never got busy")
	}
	e.fail(t, `{"action":"emulate_color_scheme","color_scheme":"dark","timeout":"300ms"}`, "color_scheme is now dark, but the page did not show it in time: sending media emulation (prefers-color-scheme: dark):")
	e.screenshot(t, "dark", black, blue)

	// Busy in the change listener: Chrome confirms, but the page can't render it in time.
	e.run(t, fmt.Sprintf(`{"action":"navigate","url":%q}`, e.url+"?busy=1000"))
	out := e.tool.Run(e.ctx, []byte(`{"action":"emulate_color_scheme","color_scheme":"light","timeout":"300ms"}`))
	if out.Error == nil || !strings.Contains(out.Error.Error(), "color_scheme is now light, but the page did not show it in time: waiting for the page to render media emulation (prefers-color-scheme: light):") {
		t.Fatalf("error %v, want it to say light did not render in time, after Chrome took it", out.Error)
	}
	e.screenshot(t, "light", white, yellow)
}

// TestEmulateColorSchemeUnsent checks that a change that never reached the
// page gets there, fully rendered, with the next browser action.
func TestEmulateColorSchemeUnsent(t *testing.T) {
	e := newColorSchemeEnv(t)
	e.run(t, `{"action":"emulate_color_scheme","color_scheme":"light"}`)
	e.tools.mediaMu.Lock()
	e.tools.saveMedia(emulatedMedia{ColorScheme: "dark"})
	e.tools.mediaMu.Unlock()
	e.screenshot(t, "dark", black, blue)
	e.tools.mediaMu.Lock()
	e.tools.saveMedia(emulatedMedia{ColorScheme: "light"})
	e.tools.mediaMu.Unlock()
	e.run(t, fmt.Sprintf(`{"action":"navigate","url":%q}`, e.url))
	e.wantMatches(t, "(prefers-color-scheme: dark)", false)

	// Through the tool: a call whose time is up before it starts.
	e.fail(t, `{"action":"emulate_color_scheme","color_scheme":"dark","timeout":"0s"}`, "color_scheme is now dark, but the page did not show it in time")
	e.screenshot(t, "dark", black, blue)
	e.fail(t, `{"action":"emulate_media","media":"print","timeout":"0s"}`, `media type is now "print"`)
	e.run(t, fmt.Sprintf(`{"action":"navigate","url":%q}`, e.url))
	e.wantMatches(t, "print", true)
	e.wantMatches(t, "(prefers-color-scheme: dark)", true)
}

// TestEmulateColorSchemeStuckPage checks that a page that can't finish
// rendering a theme change doesn't hold up leaving it: one with a long fade,
// and one whose change listener never returns.
func TestEmulateColorSchemeStuckPage(t *testing.T) {
	e := newColorSchemeEnv(t)
	e.run(t, fmt.Sprintf(`{"action":"navigate","url":%q}`, e.url+"?fade=5000"))
	e.fail(t, `{"action":"emulate_color_scheme","color_scheme":"dark","timeout":"300ms"}`, "color_scheme is now dark, but the page did not show it in time")
	e.run(t, fmt.Sprintf(`{"action":"navigate","url":%q,"timeout":"1s"}`, e.url))
	e.screenshot(t, "dark", black, blue)

	e.run(t, fmt.Sprintf(`{"action":"navigate","url":%q}`, e.url+"?busy=600000"))
	e.fail(t, `{"action":"emulate_color_scheme","color_scheme":"light","timeout":"300ms"}`, "color_scheme is now light, but the page did not show it in time")
	// A cross-site page gets a renderer of its own.
	e.run(t, fmt.Sprintf(`{"action":"navigate","url":%q,"timeout":"2s"}`, strings.Replace(e.url, "127.0.0.1", "localhost", 1)))
	e.screenshot(t, "light", white, yellow)
}

// TestEmulateColorSchemeHostilePage checks that page scripts replacing
// matchMedia, requestAnimationFrame and setTimeout neither stall the tool nor
// change the scheme it reports.
func TestEmulateColorSchemeHostilePage(t *testing.T) {
	e := newColorSchemeEnv(t)
	e.run(t, fmt.Sprintf(`{"action":"navigate","url":%q}`, e.url+"?hostile"))
	if out := e.run(t, `{"action":"emulate_color_scheme","color_scheme":"dark"}`); !strings.Contains(out, "prefers-color-scheme: dark") {
		t.Errorf("emulate_color_scheme dark output %q does not report dark", out)
	}
	e.screenshot(t, "dark", black, blue)
	if out := e.run(t, `{"action":"emulate_color_scheme","color_scheme":"system"}`); !strings.Contains(out, "prefers-color-scheme: light") {
		t.Errorf("emulate_color_scheme system output %q does not report light", out)
	}
	e.screenshot(t, "", white, yellow)
}

// TestColorSchemeInputErrors checks that bad color scheme and media inputs are
// rejected before the browser starts, with a message naming the valid values.
func TestColorSchemeInputErrors(t *testing.T) {
	tools := NewBrowseTools(t.Context(), 0)
	t.Cleanup(tools.Close)
	tool := tools.CombinedTool()
	for _, tc := range []struct{ input, want string }{
		{`{"action":"emulate_color_scheme"}`, `color_scheme must be "light", "dark", or "system"`},
		{`{"action":"emulate_color_scheme","color_scheme":"blue"}`, `color_scheme must be "light", "dark", or "system", got "blue"`},
		{`{"action":"emulate_media","media":"prefers-color-scheme: dark"}`, `use emulate_color_scheme`},
	} {
		out := tool.Run(t.Context(), []byte(tc.input))
		if out.Error == nil || !strings.Contains(out.Error.Error(), tc.want) {
			t.Errorf("%s: error %v, want it to contain %q", tc.input, out.Error, tc.want)
		}
	}
	if tools.browserCtx != nil {
		t.Fatal("invalid input must not start a browser")
	}
}
