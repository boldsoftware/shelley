package lazycue

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// newBrowserOrSkip launches a headless browser, skipping the test cleanly when
// none is available (or when the integration env var is unset). Mirrors the
// gating used by the test/ package's LazyCue integration tests.
func newBrowserOrSkip(t *testing.T) *Browser {
	t.Helper()
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	br, err := NewBrowser(context.Background(), Launch{})
	if err != nil {
		t.Skipf("no browser available: %v", err)
	}
	t.Cleanup(br.Close)
	return br
}

// serveHTML serves a fixed HTML document at / and returns the server URL.
func serveHTML(t *testing.T, html string) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(html))
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestInitScriptRunsBeforePageScripts(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	br, err := NewBrowser(context.Background(), Launch{InitScript: `window.__lazycueInit = "ready"`})
	if err != nil {
		t.Skipf("no browser available: %v", err)
	}
	t.Cleanup(br.Close)
	url := serveHTML(t, `<!doctype html><html><body>
<script>document.body.dataset.initValue = window.__lazycueInit || "missing";</script>
</body></html>`)

	steps := []Step{
		{Action: ActionNavigate, URL: url},
		{Action: ActionAssertAttribute, Selector: "body", Attribute: "data-init-value", Value: "ready"},
	}
	if results, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, steps); err != nil {
		t.Fatalf("ExecuteSteps returned error: %v (results=%+v)", err, results)
	}
}

// TestAssertPollsUntilSettled proves the point-in-time assert_* steps now poll:
// a page that starts WITH a .spinner and removes it after ~600ms must pass
// `assert_count .spinner 0` (the old one-shot check failed here under load,
// which is the flake this change fixes). The spinner is present when the step
// first runs, so a non-polling assert would fail immediately.
func TestAssertPollsUntilSettled(t *testing.T) {
	br := newBrowserOrSkip(t)
	url := serveHTML(t, `<!doctype html><html><body>
<div class="spinner">loading</div>
<script>setTimeout(function(){document.querySelector('.spinner').remove();}, 600);</script>
</body></html>`)

	steps := []Step{
		{Action: ActionNavigate, URL: url},
		{Action: ActionAssertCount, Selector: ".spinner", Count: 0},
	}
	results, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, steps)
	if err != nil {
		t.Fatalf("ExecuteSteps returned error: %v (results=%+v)", err, results)
	}
	for i, r := range results {
		if !r.Pass {
			t.Fatalf("step %d (%s) failed: %s", i, r.Action, r.Error)
		}
	}
}

// TestAssertStillFailsWhenNeverTrue proves polling doesn't mask real failures:
// a page whose .spinner never goes away must still fail `assert_count .spinner
// 0` after the (short, overridden) timeout. This is the genuine forever-spinner
// regression the test guards against — polling only tolerates LATE settling, it
// never turns a never-true assertion into a pass.
func TestAssertStillFailsWhenNeverTrue(t *testing.T) {
	br := newBrowserOrSkip(t)
	url := serveHTML(t, `<!doctype html><html><body>
<div class="spinner">loading forever</div>
</body></html>`)

	steps := []Step{
		{Action: ActionNavigate, URL: url},
		{Action: ActionAssertCount, Selector: ".spinner", Count: 0, Timeout: "1s"},
	}
	start := time.Now()
	results, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, steps)
	if err == nil {
		t.Fatalf("expected assert_count to fail for a persistent spinner, got success: %+v", results)
	}
	// It should have polled for roughly the timeout before giving up, not
	// returned instantly (which would prove it wasn't really polling) and not
	// hung well past the deadline.
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond || elapsed > 6*time.Second {
		t.Fatalf("assert_count returned after %s, want ~1s (the polling timeout)", elapsed)
	}
}

// TestAssertVariantsPollUntilSettled exercises the polling mechanism across
// several assert_* variants (not just assert_count), each starting in the
// not-yet-satisfied state and becoming satisfied ~500ms later. Guards against a
// future edit to one case's closure quietly dropping the polling wrapper.
func TestAssertVariantsPollUntilSettled(t *testing.T) {
	br := newBrowserOrSkip(t)
	// The target starts hidden+empty; a timer reveals it and fills its text.
	url := serveHTML(t, `<!doctype html><html><body>
<div id="t" style="display:none"></div>
<script>setTimeout(function(){
  var el=document.getElementById('t');
  el.style.display='block';
  el.textContent='ready now';
}, 500);</script>
</body></html>`)

	cases := []struct {
		name string
		step Step
	}{
		{"assert_visible", Step{Action: ActionAssertVisible, Selector: "#t"}},
		{"assert_text", Step{Action: ActionAssertText, Selector: "#t", Text: "ready now"}},
		{"assert_text_contains", Step{Action: ActionAssertTextContains, Selector: "#t", Text: "ready"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps := []Step{
				{Action: ActionNavigate, URL: url},
				tc.step,
			}
			results, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, steps)
			if err != nil {
				t.Fatalf("%s: ExecuteSteps error: %v (results=%+v)", tc.name, err, results)
			}
			for i, r := range results {
				if !r.Pass {
					t.Fatalf("%s: step %d (%s) failed: %s", tc.name, i, r.Action, r.Error)
				}
			}
		})
	}
}

// TestBlockingStepsHonorTheirTimeout is a regression test for a CI hang: a
// lazycue test burned its entire 8-minute perTestBudget and took the step down
// with "relaunch browser: set viewport: context deadline exceeded" (CI builds
// 8614 and 8635, both on TestNewPageCompactAndSend).
//
// The wait_*/assert_* steps poll with a per-step timeout, but the blocking
// steps (click, press_key, navigate, fill) called chromedp.Run on the browser
// context, which has no deadline. chromedp.Click waits for its selector to
// match indefinitely, so a step whose target never becomes clickable — an
// option that failed to render because the app was slow, say — hung until the
// whole-test budget expired rather than failing its own step. That turned one
// slow render into a dead 8-minute job instead of a 15-second step failure
// (which lazycue can retry, and only then heal).
//
// Each of these steps targets a selector that never appears, so each must
// return an error in roughly its own timeout, not hang.
func TestBlockingStepsHonorTheirTimeout(t *testing.T) {
	br := newBrowserOrSkip(t)
	url := serveHTML(t, `<!doctype html><html><body><p>no such target here</p></body></html>`)

	cases := []struct {
		name string
		step Step
	}{
		{"click", Step{Action: ActionClick, Selector: "#never", Timeout: "2s"}},
		{"fill", Step{Action: ActionFill, Selector: "#never", Value: "x", Timeout: "2s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps := []Step{{Action: ActionNavigate, URL: url}, tc.step}
			start := time.Now()
			results, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, steps)
			elapsed := time.Since(start)
			// The step must fail (its target never exists) ...
			failed := err != nil
			for _, r := range results {
				if !r.Pass {
					failed = true
				}
			}
			if !failed {
				t.Fatalf("%s: expected the step to fail, got results=%+v", tc.name, results)
			}
			// ... and it must do so promptly rather than blocking forever. A
			// generous ceiling still catches the bug, where this took minutes.
			if elapsed > 60*time.Second {
				t.Fatalf("%s: step took %v; it ignored its 2s timeout", tc.name, elapsed)
			}
		})
	}
}

// TestPressKeyNamedKeys proves press_key sends named keys (Enter, Tab, Escape,
// arrows, function keys) as single key presses with the matching
// KeyboardEvent.key, applies "modifiers", and still types single characters.
// Previously the key name was handed to chromedp verbatim, which typed its
// letters one by one ("Enter" -> E, n, t, e, r), so no cached script could
// exercise Enter/Tab/Escape handling.
func TestPressKeyNamedKeys(t *testing.T) {
	br := newBrowserOrSkip(t)
	url := serveHTML(t, `<!doctype html><html><body>
<textarea id="t"></textarea>
<script>
window.log = [];
document.addEventListener('keydown', function (e) {
  window.log.push((e.shiftKey ? 'Shift+' : '') + (e.ctrlKey ? 'Control+' : '') + (e.altKey ? 'Alt+' : '') + e.key);
});
</script>
</body></html>`)

	steps := []Step{
		{Action: ActionNavigate, URL: url},
		{Action: ActionClick, Selector: "#t"},
		{Action: ActionPressKey, Key: "Enter"},
		{Action: ActionPressKey, Key: "Tab"},
		{Action: ActionPressKey, Key: "Escape"},
		{Action: ActionPressKey, Key: "ArrowUp"},
		{Action: ActionPressKey, Key: "F5"},
		{Action: ActionPressKey, Key: "Enter", Modifiers: "Shift"},
		{Action: ActionPressKey, Key: "a", Modifiers: "Control"},
		{Action: ActionPressKey, Key: "Space", Modifiers: "Alt"},
		{Action: ActionPressKey, Key: "x"},
		{Action: ActionPressKey, Key: "!"},
		{
			Action:     ActionEval,
			Expression: `window.log.join(' ')`,
			Expect:     ptr("Enter Tab Escape ArrowUp F5 Shift+Shift Shift+Enter Control+Control Control+a Alt+Alt Alt+  x Shift+!"),
		},
	}
	results, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, steps)
	if err != nil {
		t.Fatalf("ExecuteSteps returned error: %v", err)
	}
	for _, r := range results {
		if !r.Pass {
			t.Errorf("%s: %s", r.Summary, r.Error)
		}
	}
}

// TestHoverMovesTheMouse: hover puts the real mouse pointer over the element,
// so both pointer events (mouseenter, which tooltips listen for) and CSS :hover
// apply, and leaves without clicking.
func TestHoverMovesTheMouse(t *testing.T) {
	br := newBrowserOrSkip(t)
	url := serveHTML(t, `<!doctype html><html><head><style>
#b { width: 20px; height: 20px; margin-top: 2000px; }
#b:hover { width: 30px; }
</style></head><body>
<button id="b"></button>
<script>
window.log = [];
const b = document.getElementById('b');
b.addEventListener('mouseenter', function () { window.log.push('enter'); });
b.addEventListener('click', function () { window.log.push('click'); });
</script>
</body></html>`)

	steps := []Step{
		{Action: ActionNavigate, URL: url},
		{Action: ActionHover, Selector: "#b"},
		{Action: ActionEval, Expression: `window.log.join(' ') + ' ' + document.getElementById('b').getBoundingClientRect().width`, Expect: ptr("enter 30")},
	}
	results, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, steps)
	if err != nil {
		t.Fatalf("ExecuteSteps returned error: %v", err)
	}
	for _, r := range results {
		if !r.Pass {
			t.Errorf("%s: %s", r.Summary, r.Error)
		}
	}
}

// TestHoverAtOffset: with x and y set, hover puts the pointer that many CSS
// pixels from the element's top-left corner, like Playwright's
// hover({position}), so a test can probe a button's edges.
func TestHoverAtOffset(t *testing.T) {
	br := newBrowserOrSkip(t)
	url := serveHTML(t, `<!doctype html><html><head><style>
#b { width: 28px; height: 28px; margin: 40px; border: 0; padding: 0; }
</style></head><body>
<button id="b"></button>
<script>
window.at = [];
const b = document.getElementById('b');
b.addEventListener('mousemove', function (e) {
  const r = b.getBoundingClientRect();
  window.at.push((e.clientX - r.left) + ',' + (e.clientY - r.top));
});
</script>
</body></html>`)

	x2, y2, x26, y26 := 2.0, 2.0, 26.0, 26.0
	steps := []Step{
		{Action: ActionNavigate, URL: url},
		{Action: ActionHover, Selector: "#b", X: &x2, Y: &y2},
		{Action: ActionHover, Selector: "#b", X: &x26, Y: &y26},
		{Action: ActionEval, Expression: `window.at.join(' ')`, Expect: ptr("2,2 26,26")},
	}
	results, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, steps)
	if err != nil {
		t.Fatalf("ExecuteSteps returned error: %v", err)
	}
	for _, r := range results {
		if !r.Pass {
			t.Errorf("%s: %s (%s)", r.Summary, r.Error, r.Output)
		}
	}
}

// TestPressKeyTypesLikeAKeyboard: a page that handles Enter on keydown and
// calls preventDefault() must not also receive a newline (chromedp's KeyEvent
// sends Enter as keyDown + a separate "char" event + keyUp, and Chrome inserts
// the char event's text regardless of the prevented keydown); an unprevented
// Enter still inserts one; Control/Alt/Meta chords type nothing; plain
// characters and Space type themselves.
func TestPressKeyTypesLikeAKeyboard(t *testing.T) {
	br := newBrowserOrSkip(t)
	url := serveHTML(t, `<!doctype html><html><body>
<textarea id="t"></textarea>
<script>
window.enters = 0;
document.getElementById('t').addEventListener('keydown', function (e) {
  if (e.key === 'Enter' && !e.shiftKey && !e.ctrlKey) { e.preventDefault(); window.enters++; }
});
</script>
</body></html>`)

	value := `JSON.stringify(document.getElementById('t').value)`
	steps := []Step{
		{Action: ActionNavigate, URL: url},
		{Action: ActionClick, Selector: "#t"},
		{Action: ActionFill, Selector: "#t", Value: "abc"},
		{Action: ActionPressKey, Key: "Enter"},
		{Action: ActionEval, Expression: `window.enters + ':' + ` + value, Expect: ptr(`1:"abc"`)},
		{Action: ActionPressKey, Key: "Enter", Modifiers: "Shift"},
		{Action: ActionEval, Expression: value, Expect: ptr(`"abc\n"`)},
		{Action: ActionPressKey, Key: "Enter", Modifiers: "Control"},
		{Action: ActionPressKey, Key: "Space", Modifiers: "Alt"},
		{Action: ActionPressKey, Key: "Space", Modifiers: "Meta"},
		{Action: ActionPressKey, Key: "d", Modifiers: "Control"},
		{Action: ActionEval, Expression: value, Expect: ptr(`"abc\n"`)},
		{Action: ActionPressKey, Key: "d"},
		{Action: ActionPressKey, Key: "Space"},
		{Action: ActionPressKey, Key: "E"},
		{Action: ActionEval, Expression: value, Expect: ptr(`"abc\nd E"`)},
	}
	results, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, steps)
	if err != nil {
		t.Fatalf("ExecuteSteps returned error: %v", err)
	}
	for _, r := range results {
		if !r.Pass {
			t.Errorf("%s: %s", r.Summary, r.Error)
		}
	}
}

func TestPressKeyRejectsUnknownKeysAndModifiers(t *testing.T) {
	for _, tc := range []struct{ key, mods string }{
		{"Enter", "Hyper"},
		{"Return", ""}, // not a KeyboardEvent.key name
		{"enter", ""},  // names are case-sensitive
		{"Shift+Enter", ""},
		{"", ""},
	} {
		if _, err := pressKey(tc.key, tc.mods, 0); err == nil {
			t.Errorf("pressKey(%q, %q): expected an error", tc.key, tc.mods)
		}
	}
	for _, name := range []string{"Enter", "Tab", "Escape", "Backspace", "Delete", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End", "PageUp", "PageDown", "Insert", "F1", "F12", "Space", "a", "Z", "1", "@", "é"} {
		if _, err := pressKey(name, "Shift+Control", 0); err != nil {
			t.Errorf("pressKey(%q): %v", name, err)
		}
	}
}

// TestTabsShareProfile: a step with "tab" acts in a second tab of the same
// browser profile, so the two behave like a user's two tabs: separate globals,
// shared localStorage down to the cross-window storage event, and both
// visible. (headless-shell, which runs these tests, reports every tab visible;
// the new window openTab asks for matters only under full Chrome.)
func TestTabsShareProfile(t *testing.T) {
	br := newBrowserOrSkip(t)
	url := serveHTML(t, `<!doctype html><html><body><p id="o">nothing yet</p>
<script>window.addEventListener('storage', function(e){document.getElementById('o').textContent = e.newValue;});</script>
</body></html>`)

	steps := []Step{
		{Action: ActionNavigate, URL: url + "/page?x=1"},
		{Action: ActionEval, Expression: "window.__mine = 'main'; 'set'", Expect: ptr("set")},
		// No URL: tab B opens on the first tab's current page.
		{Action: ActionNavigate, Tab: "B"},
		{Action: ActionAssertURL, Value: url + "/page?x=1", Tab: "B"},
		{Action: ActionEval, Expression: "String(window.__mine)", Expect: ptr("undefined"), Tab: "B"},
		{Action: ActionEval, Expression: "document.visibilityState", Expect: ptr("visible"), Tab: "B"},
		{Action: ActionEval, Expression: "document.visibilityState", Expect: ptr("visible")},
		{Action: ActionEval, Expression: "localStorage.setItem('k', 'from B'); 'stored'", Expect: ptr("stored"), Tab: "B"},
		{Action: ActionAssertText, Selector: "#o", Text: "from B"},
		{Action: ActionEval, Expression: "localStorage.getItem('k')", Expect: ptr("from B")},
	}
	results, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, steps)
	if err != nil {
		t.Fatalf("ExecuteSteps error: %v (results=%+v)", err, results)
	}
}

// TestTabsGetTheInitScript: the init script (the recording tests' media mock)
// is installed per tab in Chrome, so a tab opened by a step needs it too.
func TestTabsGetTheInitScript(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	br, err := NewBrowser(context.Background(), Launch{InitScript: "window.__init = 'ran'"})
	if err != nil {
		t.Skipf("no browser available: %v", err)
	}
	t.Cleanup(br.Close)
	url := serveHTML(t, `<!doctype html><html><body></body></html>`)
	if _, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, []Step{
		{Action: ActionNavigate, URL: url},
		{Action: ActionEval, Expression: "window.__init", Expect: ptr("ran")},
		{Action: ActionNavigate, URL: url, Tab: "B"},
		{Action: ActionEval, Expression: "window.__init", Expect: ptr("ran"), Tab: "B", Timeout: "3s"},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestTabMustBeOpenedByNavigate: only navigate opens a tab; any other step on
// an unopened tab fails at once, naming the tab, so the generating agent gets a
// hint instead of a 15s timeout. close_tab closes the tab (in Chrome, not just
// in the script's bookkeeping) while the main tab keeps working, and so does
// the end of the script: the next script in the same browser starts with only
// the first tab.
func TestTabMustBeOpenedByNavigate(t *testing.T) {
	br := newBrowserOrSkip(t)
	url := serveHTML(t, `<!doctype html><html><body><p id="o">hi</p></body></html>`)

	start := time.Now()
	_, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, []Step{
		{Action: ActionNavigate, URL: url},
		{Action: ActionAssertText, Selector: "#o", Text: "hi", Tab: "B"},
	})
	if err == nil || !strings.Contains(err.Error(), `tab "B" is not open`) {
		t.Fatalf("step on unopened tab: got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("unopened tab took %v to fail; it should not poll", time.Since(start))
	}

	_, err = br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, []Step{
		{Action: ActionNavigate, URL: url, Tab: "B"},
		{Action: ActionAssertText, Selector: "#o", Text: "hi", Tab: "B"},
		{Action: ActionCloseTab, Tab: "B"},
		{Action: ActionAssertText, Selector: "#o", Text: "hi"},
		{Action: ActionAssertText, Selector: "#o", Text: "hi", Tab: "B"},
	})
	if err == nil || !strings.Contains(err.Error(), `step 4`) || !strings.Contains(err.Error(), `tab "B" is not open`) {
		t.Fatalf("step on closed tab: got %v", err)
	}
	waitForPageCount(t, br, 1)

	if _, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, []Step{{Action: ActionCloseTab}}); err == nil {
		t.Fatal("close_tab without a tab name must fail; the main tab stays open")
	}
}

// TestNavigateWithoutURL: for another tab it means the first tab's current
// page, which must exist; for the first tab it is the base URL, as before tabs.
func TestNavigateWithoutURL(t *testing.T) {
	br := newBrowserOrSkip(t)
	url := serveHTML(t, `<!doctype html><html><body><p id="o">hi</p></body></html>`)

	_, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, []Step{{Action: ActionNavigate, Tab: "B"}})
	if err == nil || !strings.Contains(err.Error(), "first tab has not been navigated") {
		t.Fatalf("tab opened on a first tab still on about:blank: got %v", err)
	}
	if _, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: url}, []Step{
		{Action: ActionNavigate},
		{Action: ActionAssertURL, Value: url + "/"},
	}); err != nil {
		t.Fatal(err)
	}
}

// A URL with a scheme is absolute; any other is relative to the base URL.
func TestNavigateURLs(t *testing.T) {
	br := newBrowserOrSkip(t)
	base := serveHTML(t, blankPage)
	runSteps(t, br, &Env{BaseURL: base},
		Step{Action: ActionNavigate, URL: "/a"},
		Step{Action: ActionAssertURL, Value: base + "/a"},
		Step{Action: ActionNavigate, URL: "about:blank"},
		Step{Action: ActionEval, Expression: "location.href", Expect: ptr("about:blank")},
		Step{Action: ActionNavigate, URL: "b?x=1"},
		Step{Action: ActionAssertURL, Value: base + "/b?x=1"},
		Step{Action: ActionNavigate, URL: "data:text/html,<p>hi"},
		Step{Action: ActionAssertURL, Value: "data:text/html,<p>hi"},
	)
}

// TestScreenshotsFollowTheStepsTab: the per-step screenshot shows the tab the
// step acted on, so a trace of a two-tab test shows what each step saw.
func TestScreenshotsFollowTheStepsTab(t *testing.T) {
	br := newBrowserOrSkip(t)
	red := serveHTML(t, `<!doctype html><html><body style="margin:0;background:#f00"><div id="d" style="height:100vh"></div></body></html>`)
	blue := serveHTML(t, `<!doctype html><html><body style="margin:0;background:#00f"><div id="d" style="height:100vh"></div></body></html>`)

	shots := map[int][]byte{}
	br.SetScreenshotSink(func(i int, _ string, png []byte) { shots[i] = png })
	steps := []Step{
		{Action: ActionNavigate, URL: red},
		{Action: ActionNavigate, URL: blue, Tab: "B"},
		{Action: ActionAssertVisible, Selector: "#d"},
		{Action: ActionAssertVisible, Selector: "#d", Tab: "B"},
	}
	if _, err := br.ExecuteSteps(context.Background(), &Env{BaseURL: red}, steps); err != nil {
		t.Fatal(err)
	}
	for i, want := range map[int]string{2: "red", 3: "blue"} {
		if got := dominantHue(t, shots[i]); got != want {
			t.Errorf("screenshot after step %d shows %s, want %s", i, got, want)
		}
	}
	if _, err := br.Screenshot(context.Background(), "B"); err != nil {
		t.Fatalf("Screenshot of tab B: %v", err)
	}
	if _, err := br.Screenshot(context.Background(), "C"); err == nil {
		t.Fatal("Screenshot of an unopened tab must fail")
	}
}

// waitForPageCount polls until Chrome reports want page targets; closing a
// target is asynchronous.
func waitForPageCount(t *testing.T, br *Browser, want int) {
	t.Helper()
	var got int
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		got = 0
		err := chromedp.Run(br.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			infos, err := target.GetTargets().Do(cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser))
			for _, info := range infos {
				if info.Type == "page" {
					got++
				}
			}
			return err
		}))
		if err != nil {
			t.Fatal(err)
		}
		if got == want {
			return
		}
	}
	t.Fatalf("Chrome has %d page targets, want %d", got, want)
}

func dominantHue(t *testing.T, png []byte) string {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil {
		t.Fatalf("decode screenshot: %v", err)
	}
	b := img.Bounds()
	r, _, bl, _ := img.At(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2).RGBA()
	switch {
	case r > bl:
		return "red"
	case bl > r:
		return "blue"
	}
	return "neither"
}

// reload, go_back and go_forward wait for a new document to load, but not
// for a same-document (pushState) entry, which never fires load.
func TestHistory(t *testing.T) {
	br := newBrowserOrSkip(t)
	var hold atomic.Bool
	done := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/img" {
			w.Header().Set("Cache-Control", "no-store")
			if hold.Load() {
				select {
				case <-done:
				case <-r.Context().Done():
				}
			}
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body><img src="/img"></body></html>`))
	}))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(done) })
	env := &Env{BaseURL: ts.URL, Calls: map[string]func(context.Context) (string, error){
		"hold": func(context.Context) (string, error) { hold.Store(true); return "", nil },
	}}
	// is checks the page at once, without waiting for it.
	is := func(want string) Step {
		return Step{Action: ActionEval, Expression: fmt.Sprintf(`(s => { if (s !== %q) throw new Error(s) })(document.readyState + ' ' + location.pathname + ' ' + (window.mark || 'fresh'))`, want)}
	}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/a"},
		Step{Action: ActionNavigate, URL: "/b"},
		Step{Action: ActionEval, Expression: `window.mark = 'old'`},
		Step{Action: ActionGoBack},
		is("complete /a fresh"),
		Step{Action: ActionGoForward},
		is("complete /b fresh"),
		Step{Action: ActionEval, Expression: `window.mark = 'old'; history.pushState(null, '', '/c')`},
		Step{Action: ActionGoBack, Timeout: "2s"},
		is("complete /b old"),
		Step{Action: ActionGoForward, Timeout: "2s"},
		is("complete /c old"),
		// An exact URL may be just the path, query and hash.
		Step{Action: ActionAssertURL, Value: "/c"},
		Step{Action: ActionAssertURL, Value: ts.URL + "/c"},
		Step{Action: ActionWaitURL, Value: "/c"},
		Step{Action: ActionReload},
		is("complete /c fresh"),
		// Even to an entry of the same URL.
		Step{Action: ActionEval, Expression: `window.mark = 'old'; history.pushState(null, '', location.href)`},
		Step{Action: ActionGoBack, Timeout: "2s"},
		is("complete /c old"),
		Step{Action: ActionGoForward, Timeout: "2s"},
		is("complete /c old"),
	)
	if err := failSteps(t, br, env, Step{Action: ActionGoForward}); !strings.Contains(err.Error(), "go_forward: no later page in the tab's history") {
		t.Errorf("go_forward at the end = %v", err)
	}
	if err := failSteps(t, br, env, Step{Action: ActionAssertURL, Value: "/b", Timeout: "300ms"}); !strings.Contains(err.Error(), `assert_url: expected "/b", got "`+ts.URL+`/c"`) {
		t.Errorf("assert_url of another path = %v", err)
	}
	// Back to the about:blank a tab opens on, and no further.
	if err := failSteps(t, br, env, Step{Action: ActionNavigate, Tab: "B", URL: "/a"}, Step{Action: ActionGoBack, Tab: "B"}, Step{Action: ActionGoBack, Tab: "B"}); !strings.Contains(err.Error(), "go_back: no earlier page in the tab's history") {
		t.Errorf("go_back at the start = %v", err)
	}
	// A page still loading holds them up.
	for _, step := range []Step{{Action: ActionGoBack, Timeout: "500ms"}, {Action: ActionReload, Timeout: "500ms"}} {
		if err := failSteps(t, br, env, Step{Action: ActionNavigate, URL: "/a"}, Step{Action: ActionNavigate, URL: "/b"}, Step{Action: ActionCall, Value: "hold"}, step); !strings.Contains(err.Error(), "timeout") {
			t.Errorf("%s on a loading page = %v", step.Action, err)
		}
		hold.Store(false)
	}
}

// collect_garbage frees what only a WeakRef holds.
func TestCollectGarbage(t *testing.T) {
	br := newBrowserOrSkip(t)
	runSteps(t, br, &Env{BaseURL: serveHTML(t, blankPage)},
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: `window.ref = new WeakRef({big: new Array(1000).fill(1)}); 'made'`},
		Step{Action: ActionEval, Expression: `if (!ref.deref()) throw new Error('freed already')`},
		Step{Action: ActionCollectGarbage},
		Step{Action: ActionEval, Expression: `if (ref.deref()) throw new Error('not freed')`},
	)
	// Nor do steps by role hold what they matched.
	runSteps(t, br, &Env{BaseURL: serveHTML(t, `<!doctype html><html><body><div id="panel"><button onclick="this.parentNode.remove()">Remove</button></div></body></html>`)},
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: `window.ref = new WeakRef(document.getElementById('panel')); 'made'`},
		Step{Action: ActionClick, Role: "button", Name: "Remove"},
		Step{Action: ActionAssertCount, Selector: "#panel", Count: 0},
		Step{Action: ActionCollectGarbage},
		Step{Action: ActionEval, Expression: `if (ref.deref()) throw new Error('not freed')`},
	)
}

// A page a page opens becomes tab "popup" (then "popup2", ...), set up as
// the launch says before it runs, as Playwright's popups are.
func TestPopups(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	br, err := NewBrowser(context.Background(), Launch{InitScript: `window.__init = 'yes'`, Timezone: "Asia/Tokyo"})
	if err != nil {
		t.Skipf("no browser available: %v", err)
	}
	t.Cleanup(br.Close)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/p" {
			// The alert would hold the page if nothing answered it.
			_, _ = w.Write([]byte(`<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body><script>
alert('hi');
document.title = 'init=' + window.__init + ' w=' + innerWidth + ' opener=' + (window.opener !== null) + ' tz=' + Intl.DateTimeFormat().resolvedOptions().timeZone;
</script></body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<a id="blank" href="/p?x=1" target="_blank">open</a>
<button id="open" onclick="window.open('/p?y=2')">open</button>
</body></html>`))
	}))
	t.Cleanup(ts.Close)
	env := &Env{BaseURL: ts.URL}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionClick, Selector: "#blank"},
		Step{Action: ActionAssertURL, Tab: "popup", Value: "/p?x=1"},
		Step{Action: ActionAssertTitle, Tab: "popup", Text: "init=yes w=393 opener=false tz=Asia/Tokyo"},
		Step{Action: ActionClick, Selector: "#open"},
		Step{Action: ActionAssertTitle, Tab: "popup2", Text: "init=yes w=393 opener=true tz=Asia/Tokyo"},
		// Its name is free again once it closes.
		Step{Action: ActionCloseTab, Tab: "popup"},
		Step{Action: ActionClick, Selector: "#blank"},
		Step{Action: ActionAssertURL, Tab: "popup", Value: "/p?x=1"},
	)
	if err := failSteps(t, br, env, Step{Action: ActionEval, Tab: "popup3", Expression: "1", Timeout: "300ms"}); !strings.Contains(err.Error(), `no page opened a popup for tab "popup3" in 300ms`) {
		t.Errorf("unopened popup: %v", err)
	}
	// A popup that closes itself is gone too.
	runSteps(t, br, env, Step{Action: ActionEval, Tab: "popup2", Expression: "setTimeout(() => window.close(), 100)"})
	deadline := time.After(5 * time.Second)
	for {
		br.mu.Lock()
		_, open := br.tabs["popup2"]
		changed := br.changed
		br.mu.Unlock()
		if !open {
			break
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("popup2 is still open after closing itself")
		}
	}
}

// Launch.Hosts answers requests to other origins, in every tab including
// popups and cross-site iframes, as Playwright's context routes do.
func TestHosts(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	other := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/frame":
			_, _ = w.Write([]byte(`<!doctype html><script src="/frame.js"></script>`))
		case "/frame.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write([]byte(`parent.postMessage('frame ' + location.host, '*')`))
		case "/echo":
			if r.Method == http.MethodOptions {
				t.Error("the handler got a CORS preflight")
			}
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Access-Control-Expose-Headers", "X-Seen")
			w.Header().Set("X-Seen", r.Header.Get("X-Ask"))
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte(r.Method + " " + r.RequestURI + " " + string(body)))
		case "/theirs":
			w.Header().Set("Access-Control-Allow-Origin", "https://nobody.test")
		default:
			_, _ = w.Write([]byte(`<!doctype html><title>other ` + r.Host + r.URL.Path + `</title>`))
		}
	})
	br, err := NewBrowser(context.Background(), Launch{Hosts: map[string]http.Handler{"https://other.test": other}})
	if err != nil {
		t.Skipf("no browser available: %v", err)
	}
	t.Cleanup(br.Close)
	base := serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<a id="blank" href="https://other.test/docs" target="_blank">docs</a>
<script>addEventListener('message', (e) => { document.body.dataset.msg = e.data })</script>
</body></html>`)
	runSteps(t, br, &Env{BaseURL: base},
		Step{Action: ActionNavigate, URL: "/"},
		// Preflights are answered for it, and other origins may read what
		// it says unless it says who may, as with Playwright's routes.
		Step{Action: ActionEval, Expression: `fetch('https://other.test/echo?q=1', {method: 'POST', body: 'hi', headers: {'X-Ask': 'yes'}, credentials: 'include'}).then(async (r) => r.status + ' ' + r.headers.get('X-Seen') + ' ' + await r.text())`, Expect: ptr("418 yes POST /echo?q=1 hi")},
		Step{Action: ActionEval, Expression: `fetch('https://other.test/theirs').then(() => 'read', () => 'blocked')`, Expect: ptr("blocked")},
		Step{Action: ActionEval, Expression: `document.body.append(Object.assign(document.createElement('iframe'), {src: 'https://other.test/frame'}))`},
		Step{Action: ActionEval, Expression: `document.body.dataset.msg`, Expect: ptr("frame other.test")},
		Step{Action: ActionClick, Selector: "#blank"},
		Step{Action: ActionAssertTitle, Tab: "popup", Text: "other other.test/docs"},
		Step{Action: ActionNavigate, Tab: "b", URL: "https://other.test/b"},
		Step{Action: ActionAssertTitle, Tab: "b", Text: "other other.test/b"},
		// A URL no handler could take is a bad request.
		Step{Action: ActionNavigate, Tab: "b", URL: "https://other.test/100%"},
		Step{Action: ActionAssertText, Tab: "b", Selector: "body", Text: `parse "https://other.test/100%": invalid URL escape "%"`},
	)
}

// A launch Chrome rejects says which part.
func TestBadLaunch(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	_, err := NewBrowser(context.Background(), Launch{Timezone: "Mars/Olympus"})
	if want := "set up the first tab: Emulation.setTimezoneOverride: Invalid timezone id"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("bad time zone: %v, want %q", err, want)
	}
}

func TestHostsAreOrigins(t *testing.T) {
	for _, origin := range []string{"https://docs.example/", "docs.example", "https://docs.example/x"} {
		_, err := NewBrowser(context.Background(), Launch{Hosts: map[string]http.Handler{origin: http.NotFoundHandler()}})
		if err == nil || !strings.Contains(err.Error(), "is not an origin") {
			t.Errorf("%q: %v", origin, err)
		}
	}
}

// A Hosts handler that takes its time holds up neither the other requests,
// nor dialogs, nor closing the browser.
func TestSlowHostHandler(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	release, held := make(chan struct{}), make(chan struct{})
	other := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			close(held)
			<-release
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = w.Write([]byte("ok " + r.URL.Path))
	})
	br, err := NewBrowser(context.Background(), Launch{Hosts: map[string]http.Handler{"https://other.test": other}})
	if err != nil {
		t.Skipf("no browser available: %v", err)
	}
	t.Cleanup(br.Close)
	// Before br.Close, should it be held up.
	t.Cleanup(func() { close(release) })
	env := &Env{BaseURL: serveHTML(t, blankPage), Calls: map[string]func(context.Context) (string, error){
		"held": func(ctx context.Context) (string, error) {
			select {
			case <-held:
				return "", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	}}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: `fetch('https://other.test/slow'); 'sent'`},
		Step{Action: ActionCall, Value: "held"},
		Step{Action: ActionEval, Expression: `fetch('https://other.test/fast').then(r => r.text())`, Expect: ptr("ok /fast"), Timeout: "3s"},
		Step{Action: ActionEval, Expression: `alert('hi'); 'answered'`, Timeout: "3s"},
	)
	closed := make(chan struct{})
	go func() {
		br.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		t.Error("Close is held up by a host handler")
	}
}
