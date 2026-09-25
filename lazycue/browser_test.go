package lazycue

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// newBrowserOrSkip launches a headless browser, skipping the test cleanly when
// none is available (or when the integration env var is unset). Mirrors the
// gating used by the test/ package's LazyCue integration tests.
func newBrowserOrSkip(t *testing.T) *Browser {
	t.Helper()
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	br, err := NewBrowser(context.Background())
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
	results, err := br.ExecuteSteps(context.Background(), url, steps)
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
	results, err := br.ExecuteSteps(context.Background(), url, steps)
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
			results, err := br.ExecuteSteps(context.Background(), url, steps)
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
			results, err := br.ExecuteSteps(context.Background(), url, steps)
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
			Expect:     "Enter Tab Escape ArrowUp F5 Shift+Enter Control+a Alt+  x Shift+!",
		},
	}
	results, err := br.ExecuteSteps(context.Background(), url, steps)
	if err != nil {
		t.Fatalf("ExecuteSteps returned error: %v", err)
	}
	for _, r := range results {
		if !r.Pass {
			t.Errorf("%s: %s", r.Summary, r.Error)
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
		{Action: ActionEval, Expression: `window.enters + ':' + ` + value, Expect: `1:"abc"`},
		{Action: ActionPressKey, Key: "Enter", Modifiers: "Shift"},
		{Action: ActionEval, Expression: value, Expect: `"abc\n"`},
		{Action: ActionPressKey, Key: "Enter", Modifiers: "Control"},
		{Action: ActionPressKey, Key: "Space", Modifiers: "Alt"},
		{Action: ActionPressKey, Key: "Space", Modifiers: "Meta"},
		{Action: ActionPressKey, Key: "d", Modifiers: "Control"},
		{Action: ActionEval, Expression: value, Expect: `"abc\n"`},
		{Action: ActionPressKey, Key: "d"},
		{Action: ActionPressKey, Key: "Space"},
		{Action: ActionPressKey, Key: "E"},
		{Action: ActionEval, Expression: value, Expect: `"abc\nd E"`},
	}
	results, err := br.ExecuteSteps(context.Background(), url, steps)
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
		if _, err := pressKey(tc.key, tc.mods); err == nil {
			t.Errorf("pressKey(%q, %q): expected an error", tc.key, tc.mods)
		}
	}
	for _, name := range []string{"Enter", "Tab", "Escape", "Backspace", "Delete", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End", "PageUp", "PageDown", "Insert", "F1", "F12", "Space", "a", "Z", "1", "@", "é"} {
		if _, err := pressKey(name, "Shift+Control"); err != nil {
			t.Errorf("pressKey(%q): %v", name, err)
		}
	}
}
