package lazycue

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// runSteps runs steps in br against env, failing the test on any failed step.
func runSteps(t *testing.T, br *Browser, env *Env, steps ...Step) []StepResult {
	t.Helper()
	results, err := br.ExecuteSteps(context.Background(), env, steps)
	for _, r := range results {
		if !r.Pass {
			t.Errorf("%s: %s", r.Summary, r.Error)
		}
	}
	if err != nil {
		t.Fatalf("ExecuteSteps: %v", err)
	}
	return results
}

// failSteps runs steps expecting a failure and returns its error.
func failSteps(t *testing.T, br *Browser, env *Env, steps ...Step) error {
	t.Helper()
	_, err := br.ExecuteSteps(context.Background(), env, steps)
	if err == nil {
		t.Fatal("steps passed; want a failure")
	}
	return err
}

func ptr[T any](v T) *T { return &v }

const blankPage = `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body></body></html>`

// The default device is Playwright's Pixel 5: a phone with a touch screen
// that prefers reduced motion, may use the clipboard, and draws WebGL in
// software, as Playwright's Chrome does without a GPU.
func TestDefaultDeviceIsAPixel5(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, blankPage)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{
			Action: ActionEval, Expression: `[innerWidth, innerHeight, devicePixelRatio, 'ontouchstart' in window, navigator.maxTouchPoints,
			matchMedia('(pointer: coarse)').matches, matchMedia('(hover: none)').matches, matchMedia('(prefers-reduced-motion: reduce)').matches].join(' ')`,
			Expect: ptr("393 727 2.75 true 5 true true true"),
		},
		Step{Action: ActionEval, Expression: `navigator.clipboard.writeText('copied').then(() => navigator.clipboard.readText())`, Expect: ptr("copied")},
		// A copy button, as apps have, writes from a real click.
		Step{Action: ActionEval, Expression: `const b = document.body.appendChild(document.createElement('button')); b.id = 'copy'; b.textContent = 'Copy';
			b.onclick = () => navigator.clipboard.writeText('clicked').then(() => b.textContent = 'ok', e => b.textContent = e.name)`},
		Step{Action: ActionClick, Selector: "#copy"},
		Step{Action: ActionAssertText, Selector: "#copy", Text: "ok"},
		Step{Action: ActionEval, Expression: `navigator.clipboard.readText()`, Expect: ptr("clicked")},
		Step{Action: ActionEval, Expression: `!!document.createElement('canvas').getContext('webgl2')`, Expect: ptr("true")},
	)
}

func TestLaunchSettings(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	br, err := NewBrowser(context.Background(), Launch{
		Device:   &Device{Width: 1024, Height: 700, Scale: 1},
		Media:    map[string]string{"prefers-color-scheme": "dark", "prefers-reduced-motion": ""},
		Timezone: "America/New_York",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(br.Close)
	env := &Env{BaseURL: serveHTML(t, blankPage)}
	steps := []Step{
		{Action: ActionNavigate, URL: "/"},
		{
			Action: ActionEval, Expression: `[innerWidth, 'ontouchstart' in window, matchMedia('(prefers-color-scheme: dark)').matches,
			matchMedia('(prefers-reduced-motion: reduce)').matches, Intl.DateTimeFormat().resolvedOptions().timeZone,
			matchMedia('(hover: hover)').matches, matchMedia('(pointer: fine)').matches].join(' ')`,
			Expect: ptr("1024 false true false America/New_York true true"),
		},
	}
	runSteps(t, br, env, steps...)
	// Another tab starts out the same way.
	for i := range steps {
		steps[i].Tab = "B"
	}
	runSteps(t, br, env, steps...)
}

// An eval runs again, when it polls or a retry block repeats, though it
// declares a const at the top: evals run through an indirect eval, as
// Playwright's do, which works under a CSP that bars the page's own.
func TestEvalRunsTwice(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><body>
<iframe srcdoc="<p>x</p>"></iframe>
<script>setTimeout(() => document.title = 'ready', 300);</script>
</body></html>`)}
	results := runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: `const f = document.title; f`, Expect: ptr("ready")},
		Step{Action: ActionRetry, Steps: []Step{
			{Action: ActionEval, Expression: `const g = window.tries = (window.tries || 0) + 1; if (g < 2) throw new Error('again'); g`},
		}},
		Step{Action: ActionEval, Expression: `const n = 2; (async () => (await fetch('/')).status * n)()`, Expect: ptr("400")},
		Step{Action: ActionEval, Frame: "iframe", Expression: `let h = 1; h`},
		Step{Action: ActionEval, Frame: "iframe", Expression: `let h = 2; h`, Expect: ptr("2")},
		// An eval that navigates still gives its value.
		Step{Action: ActionEval, Expression: `location.href = '/?again'; ({went: true})`},
		Step{Action: ActionAssertURL, Value: "/?again"},
		Step{Action: ActionEval, Expression: `document.readyState`, Expect: ptr("complete")},
	)
	if got := results[6].Output; got != `{"went":true}` {
		t.Errorf("navigating eval = %q", got)
	}
	csp := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="script-src 'none'"></head><body></body></html>`)}
	runSteps(t, br, csp,
		Step{Action: ActionNavigate, URL: "/"},
		// The page's own eval is barred, outside the step's evaluation.
		Step{Action: ActionEval, Expression: `new Promise(done => setTimeout(() => { try { eval('1'); done('allowed') } catch (e) { done(e.name) } }))`, Expect: ptr("EvalError")},
		Step{Action: ActionEval, Expression: `const k = 3; k`, Expect: ptr("3")},
	)
}

func TestEvalAwaitsPromisesAndRendersJSON(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, blankPage)}
	results := runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: `(async () => { await new Promise(r => setTimeout(r, 50)); return 'later'; })()`, Expect: ptr("later")},
		Step{Action: ActionEval, Expression: `fetch('/').then(r => r.status)`, Expect: ptr("200")},
		Step{Action: ActionEval, Expression: `({a: [1, true, null]})`},
	)
	if got := results[3].Output; got != `{"a":[1,true,null]}` {
		t.Errorf("object output = %s", got)
	}
	// An empty expectation still asserts.
	var step Step
	if err := json.Unmarshal([]byte(`{"action": "eval", "expression": "'x'", "expect": "", "timeout": "300ms"}`), &step); err != nil {
		t.Fatal(err)
	}
	if err := failSteps(t, br, env, step); !strings.Contains(err.Error(), `expected "", got "x"`) {
		t.Errorf("empty expect error = %v", err)
	}
}

// Visible means a box of some size, as Playwright has it; position: fixed
// elements, whose offsetParent is null, are visible too, and a display:
// contents element, which has no box, is when a child is.
func TestVisibility(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<div id="fixed" style="position: fixed; top: 0; width: 50px; height: 50px"></div>
<div id="empty"></div>
<div id="hidden" style="visibility: hidden; height: 10px"></div>
<div style="display: none"><div id="inside" style="height: 10px"></div></div>
<div id="clear" style="opacity: 0; height: 10px"></div>
<div id="contents" style="display: contents"><span style="display: contents"><button>Go</button></span></div>
<div id="text" style="display: contents">Words</div>
<div id="none" style="display: contents"> <span></span> </div>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionWaitVisible, Selector: "#fixed"},
		Step{Action: ActionAssertVisible, Selector: "#fixed"},
		Step{Action: ActionAssertVisible, Selector: "#clear"},
		Step{Action: ActionAssertNotVisible, Selector: "#empty"},
		Step{Action: ActionAssertNotVisible, Selector: "#hidden"},
		Step{Action: ActionAssertNotVisible, Selector: "#inside"},
		Step{Action: ActionAssertVisible, Selector: "#contents"},
		Step{Action: ActionAssertVisible, Selector: "#text"},
		Step{Action: ActionAssertNotVisible, Selector: "#none"},
		Step{Action: ActionWaitHidden, Selector: "#missing"},
	)
	failSteps(t, br, env, Step{Action: ActionWaitHidden, Selector: "#fixed", Timeout: "300ms"})
	// A display: contents element is visible but has no box to aim at.
	for _, step := range []Step{
		{Action: ActionClick, Selector: "#contents", Timeout: "300ms"},
		{Action: ActionHover, Selector: "#text", Timeout: "300ms"},
	} {
		if err := failSteps(t, br, env, step); !strings.Contains(err.Error(), step.Selector+" is display: contents and has no box to aim at; pick an element in it") {
			t.Errorf("%s: %v", StepSummary(step), err)
		}
	}
}

func TestHasText(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"><title>Inner - app</title><script>window.x = 'Inner'</script></head><body>
<p style="display: none">Hidden note</p>
<div>Outer <div>Inner</div></div>
<ul><li><button>Rename</button></li><li><button title="a">Archive
  this</button></li><li><button>Archive that</button></li></ul>
<div><style>.styled {}</style>Styled</div>
<input type="submit" value="Send">
<script>document.addEventListener('click', e => document.title = e.target.title || e.target.textContent)</script>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionAssertCount, Selector: "button", HasText: "Archive", Count: 2},
		// Case does not matter, as with Playwright, unless exact.
		Step{Action: ActionAssertCount, Selector: "li", HasText: "ARCHIVE", Count: 2},
		Step{Action: ActionAssertCount, Selector: "li", HasText: "archive that", Exact: true, Count: 0},
		Step{Action: ActionAssertCount, Selector: "li", HasText: "Archive that", Exact: true, Count: 1},
		Step{Action: ActionAssertAttribute, Selector: "button", HasText: "Archive this", Attribute: "title", Value: "a"},
		Step{Action: ActionAssertText, Selector: "li", HasText: "that", Text: "Archive that"},
		Step{Action: ActionAssertNotVisible, Selector: "button", HasText: "Delete"},
		Step{Action: ActionAssertNotVisible, Selector: "*", HasText: "Hidden note"},
		Step{Action: ActionAssertText, Selector: "div", HasText: "Inner", Text: "Inner"},
		Step{Action: ActionAssertVisible, Selector: "*", HasText: "Inner"},
		Step{Action: ActionAssertCount, Selector: "*", HasText: "Inner", Count: 1},
		Step{Action: ActionAssertCount, Selector: "*", HasText: "app", Count: 0},
		Step{Action: ActionAssertNotVisible, Selector: "*", HasText: "app"},
		// As in Playwright, scripts and styles have no text, and a submit
		// or button input's is its value.
		Step{Action: ActionAssertCount, Selector: "*", HasText: "addEventListener", Count: 0},
		Step{Action: ActionAssertCount, Selector: "div", HasText: ".styled", Count: 0},
		Step{Action: ActionAssertCount, Selector: "div", HasText: "Styled", Count: 1},
		Step{Action: ActionAssertCount, Selector: "input", HasText: "send", Count: 1},
		Step{Action: ActionAssertCount, Selector: "*", HasText: "Archive", Count: 2},
		Step{Action: ActionClick, Selector: "button", HasText: "that"},
		Step{Action: ActionAssertTitle, Text: "Archive that"},
	)
	err := failSteps(t, br, env, Step{Action: ActionClick, Selector: "button", HasText: "Delete", Timeout: "300ms"})
	if err == nil || !strings.Contains(err.Error(), `no element matches button with text "Delete"`) {
		t.Fatalf("click on a missing text: %v", err)
	}
}

// role and name pick elements as Playwright's getByRole does, from Chrome's
// accessibility tree; exact and visible narrow any element step.
func TestRoles(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<nav><button>Archive</button><a href="#all">Archive all</a></nav>
<main>
<button aria-label="Close dialog">X</button>
<button>Save <span aria-hidden="true">!</span></button>
<button>  Archive
  this </button>
<button style="display: none">Archive hidden</button>
<div role="button" aria-hidden="true">Archive unseen</div>
<button aria-hidden="true">Archive unheard</button>
<div aria-hidden="true"><button>Archive under</button></div>
<button style="width: 0; height: 0; padding: 0; border: 0; overflow: hidden">Archive zero</button>
<input type="checkbox" id="c"><label for="c">Dark mode</label>
<input type="checkbox" role="switch" aria-label="Notify">
<input type="search" placeholder="Find">
<textarea aria-label="Message"></textarea>
<select aria-label="Pick"><option value="a">A</option><option value="b">B</option></select>
<img alt="Logo" width="10" height="10" src="data:image/gif;base64,R0lGODlhAQABAAAAACw=">
<section aria-label="Results"><h2>Results</h2><ul><li>One</li><li>Two</li></ul></section>
<div role="menu"><div role="menuitemcheckbox" aria-checked="true">Wrap lines</div></div>
<x-card></x-card>
</main>
<div id="order"><x-card></x-card><button>Light</button></div>
<script>
customElements.define('x-card', class extends HTMLElement {
  connectedCallback() { this.attachShadow({mode: 'open'}).innerHTML = '<button>Shadow</button>'; }
});
document.addEventListener('click', e => document.title = e.target.getAttribute('aria-label') || e.target.textContent);
setTimeout(() => document.querySelector('main').insertAdjacentHTML('beforeend', '<button>Later</button>'), 300);
</script>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		// A name is a case-insensitive substring, whitespace collapsed, of
		// an element's accessible name; hidden elements have none.
		Step{Action: ActionAssertCount, Role: "button", Name: "archive", Count: 3},
		Step{Action: ActionAssertCount, Role: "button", Name: "Archive", Exact: true, Count: 1},
		Step{Action: ActionAssertCount, Role: "button", Name: "Archive this", Exact: true, Count: 1},
		Step{Action: ActionAssertCount, Role: "button", Name: "archive", Visible: true, Count: 2},
		Step{Action: ActionAssertCount, Role: "button", Name: "Save", Exact: true, Count: 1},
		Step{Action: ActionAssertCount, Role: "link", Name: "Archive", Count: 1},
		Step{Action: ActionAssertText, Role: "button", Name: "archive", Text: "Archive"},
		// A selector scopes them: they must be inside what it matches.
		Step{Action: ActionAssertCount, Selector: "nav", Role: "button", Count: 1},
		Step{Action: ActionAssertCount, Selector: "main", Role: "button", Name: "Archive", Count: 2},
		Step{Action: ActionAssertCount, Selector: "section", Role: "listitem", Count: 2},
		Step{Action: ActionAssertCount, Selector: "button", Role: "button", Count: 0},
		// has_text filters them, without keeping only the innermost.
		Step{Action: ActionAssertCount, Role: "button", HasText: "THIS", Count: 1},
		Step{Action: ActionAssertCount, Role: "region", HasText: "One", Count: 1},
		Step{Action: ActionAssertCount, Role: "button", HasText: "Archive this", Exact: true, Count: 1},
		Step{Action: ActionAssertCount, Role: "button", HasText: "archive this", Exact: true, Count: 0},
		Step{Action: ActionAssertCount, Selector: "button", HasText: "Archive", Exact: true, Count: 1},
		Step{Action: ActionAssertCount, Selector: "button", HasText: "Archive", Count: 6},
		Step{Action: ActionAssertCount, Selector: "button", HasText: "Archive", Visible: true, Count: 4},
		// Shadow DOM counts, in document order, inside a selector's match too.
		Step{Action: ActionAssertCount, Role: "button", Name: "shadow", Count: 2},
		Step{Action: ActionAssertCount, Selector: "main x-card", Role: "button", Count: 1},
		Step{Action: ActionAssertTexts, Selector: "#order", Role: "button", Texts: []string{"Shadow", "Light"}},
		Step{Action: ActionClick, Role: "button", Name: "close"},
		Step{Action: ActionAssertTitle, Text: "Close dialog"},
		Step{Action: ActionClick, Role: "checkbox", Name: "Dark mode"},
		Step{Action: ActionEval, Expression: "String(document.getElementById('c').checked)", Expect: ptr("true")},
		Step{Action: ActionAssertVisible, Role: "switch", Name: "Notify"},
		Step{Action: ActionFill, Role: "searchbox", Name: "Find", Value: "x"},
		Step{Action: ActionAssertValue, Role: "searchbox", Value: "x"},
		Step{Action: ActionFill, Role: "textbox", Name: "message", Value: "hi"},
		Step{Action: ActionAssertFocused, Role: "textbox", Name: "Message"},
		Step{Action: ActionFill, Role: "combobox", Name: "Pick", Value: "b"},
		Step{Action: ActionAssertValue, Role: "combobox", Value: "b"},
		Step{Action: ActionAssertVisible, Role: "img", Name: "Logo"},
		Step{Action: ActionAssertAttribute, Role: "menuitemcheckbox", Name: "wrap", Attribute: "aria-checked", Value: "true"},
		Step{Action: ActionAssertNotVisible, Role: "button", Name: "Archive hidden"},
		Step{Action: ActionWaitVisible, Role: "button", Name: "Later"},
	)
	for _, c := range []struct {
		step Step
		err  string
	}{
		{Step{Action: ActionClick, Role: "button", Name: "Delete"}, `no element matches button "Delete"`},
		{Step{Action: ActionAssertCount, Selector: "nav", Role: "button", Name: "Archive", Count: 2}, `expected 2 of button "Archive" in nav, got 1`},
		{Step{Action: ActionAssertVisible, Role: "button", Name: "zero"}, `button "zero" is not visible`},
		{Step{Action: ActionClick, Role: "image"}, `unknown role "image"`},
		{Step{Action: ActionClick, Selector: "button", Name: "Archive"}, "a name needs a role"},
		{Step{Action: ActionClick, Role: "button", Timeout: "1ms"}, "timeout after 1ms: the button role lookup did not finish in time"},
	} {
		if c.step.Timeout == "" {
			c.step.Timeout = "300ms"
		}
		if err := failSteps(t, br, env, c.step); !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: got %v, want %q", StepSummary(c.step), err, c.err)
		}
	}
}

// within scopes a step to the elements inside what it picks out, as
// Playwright's chained locators do.
func TestWithin(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<div data-testid="card" style="display: none"><span data-testid="status">Hidden</span></div>
<div data-testid="card"><span data-testid="status">Passed</span><button>Copy</button></div>
<div data-testid="card"><span data-testid="status">Failed (exit 7)</span><button>Copy</button></div>
<section aria-label="Messages">
<div class="msg">Hello <button aria-label="Copy">c</button></div>
<div class="msg">Bye <button aria-label="Copy">c</button></div>
</section>
<x-card></x-card>
<script>
customElements.define('x-card', class extends HTMLElement {
  connectedCallback() { this.attachShadow({mode: 'open'}).innerHTML = '<button>Shadow</button>'; }
});
document.addEventListener('click', e => document.title = e.target.closest('.msg, [data-testid=card]').firstChild.textContent.trim());
</script>
</body></html>`)}
	cards := &Pick{Selector: "[data-testid=card]"}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		// Only the step's own matches inside one with the text count; the
		// first of them is the step's element.
		Step{Action: ActionAssertText, Within: &Pick{Selector: "[data-testid=card]", HasText: "Failed (exit 7)"}, Selector: "[data-testid=status]", Text: "Failed (exit 7)"},
		Step{Action: ActionAssertCount, Within: cards, Selector: "[data-testid=status]", Count: 3},
		Step{Action: ActionAssertCount, Within: &Pick{Selector: "[data-testid=card]", Visible: true}, Selector: "[data-testid=status]", Count: 2},
		Step{Action: ActionAssertCount, Within: &Pick{Selector: "[data-testid=card]", HasText: "Failed", Exact: true}, Selector: "[data-testid=status]", Count: 0},
		Step{Action: ActionAssertText, Within: cards, Selector: "[data-testid=status]", Text: "Hidden"},
		// A within match itself does not count.
		Step{Action: ActionAssertCount, Within: cards, Selector: "div", Count: 0},
		// Roles on either side, each with its own name.
		Step{Action: ActionClick, Within: &Pick{Selector: ".msg", HasText: "Bye"}, Role: "button", Name: "Copy"},
		Step{Action: ActionAssertTitle, Text: "Bye"},
		Step{Action: ActionAssertCount, Within: &Pick{Role: "region", Name: "messages"}, Role: "button", Name: "Copy", Count: 2},
		Step{Action: ActionAssertCount, Within: &Pick{Selector: "section"}, Selector: "button", Count: 2},
		// The step's has_text applies after within.
		Step{Action: ActionAssertCount, Within: &Pick{Selector: "section"}, Selector: "div", HasText: "Bye", Count: 1},
		// Shadow DOM is inside its host.
		Step{Action: ActionAssertText, Within: &Pick{Selector: "x-card"}, Role: "button", Text: "Shadow"},
	)
	for _, c := range []struct {
		step Step
		err  string
	}{
		{Step{Action: ActionClick, Within: &Pick{Selector: "[data-testid=card]", HasText: "Nope"}, Selector: "button"}, `no element matches button within [data-testid=card] with text "Nope"`},
		{Step{Action: ActionClick, Within: &Pick{HasText: "Bye"}, Selector: "button"}, "within needs a selector or a role"},
		{Step{Action: ActionClick, Within: cards}, "within scopes a step's selector or role"},
		{Step{Action: ActionClick, Within: &Pick{Selector: ".msg", Name: "Bye"}, Selector: "button"}, "a name needs a role"},
		{Step{Action: ActionClick, Within: &Pick{Role: "image"}, Selector: "button"}, `unknown role "image"`},
	} {
		c.step.Timeout = "300ms"
		if err := failSteps(t, br, env, c.step); !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: got %v, want %q", StepSummary(c.step), err, c.err)
		}
	}
}

// has keeps the matches with an element inside them that it picks out, as
// Playwright's filter({has}), shadow roots included.
func TestHas(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<section><h2>Profiles</h2><ul>
<li class="item"><span class="name">Default (old)</span><button>Use</button></li>
<li class="item"><span class="name">Default</span><button>Use</button></li>
<li class="item"><x-badge></x-badge><button>Use</button></li>
</ul></section>
<section><h2>Other</h2><ul><li class="item"><span class="name">Default</span><button>Use</button></li></ul></section>
<div class="card"><div class="title">Hello</div><button>Go</button></div>
<script>
customElements.define('x-badge', class extends HTMLElement {
  connectedCallback() { this.attachShadow({mode: 'open'}).innerHTML = '<span class="name">Shadowed</span>'; }
});
document.addEventListener('click', e => document.title = e.target.closest('li').textContent);
</script>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionAssertCount, Selector: ".item", Has: &Pick{Selector: ".name"}, Count: 4},
		Step{Action: ActionAssertCount, Selector: ".item", Has: &Pick{Selector: ".name", HasText: "Default", Exact: true}, Count: 2},
		Step{Action: ActionClick, Role: "button", Within: &Pick{Selector: ".item", Has: &Pick{Selector: ".name", HasText: "Default", Exact: true}}},
		Step{Action: ActionAssertTitle, Text: "DefaultUse"},
		// A role, and shadow roots.
		Step{Action: ActionAssertCount, Selector: "section", Has: &Pick{Role: "heading", Name: "Profiles"}, Count: 1},
		Step{Action: ActionAssertCount, Selector: ".item", Has: &Pick{Selector: ".name", HasText: "Shadowed"}, Count: 1},
		// has's nth counts in each match: the first section has three rows.
		Step{Action: ActionAssertCount, Selector: "section", Has: &Pick{Selector: ".item", Nth: ptr(2)}, Count: 1},
		Step{Action: ActionAssertCount, Selector: "section", Has: &Pick{Selector: ".item", Nth: ptr(-3)}, Count: 1},
		Step{Action: ActionAssertCount, Selector: "section", Has: &Pick{Selector: ".item", Nth: ptr(3)}, Count: 0},
		Step{Action: ActionAssertCount, Selector: "section", Has: &Pick{Selector: ".item", Nth: ptr(0)}, Count: 2},
		// has goes before has_text keeps the innermost: the card holds the
		// title, but the title has no button.
		Step{Action: ActionAssertCount, Selector: "div", HasText: "Hello", Has: &Pick{Selector: "button"}, Count: 1},
		Step{Action: ActionAssertCount, Selector: ".card", HasText: "Hello", Has: &Pick{Selector: "button"}, Count: 1},
		// An element does not have itself.
		Step{Action: ActionAssertCount, Selector: ".name", Has: &Pick{Selector: ".name"}, Count: 0},
		// Within's has picks the section; the step's has, the row in it.
		Step{Action: ActionAssertCount, Within: &Pick{Selector: "section", Has: &Pick{Role: "heading", Name: "Other"}}, Selector: ".item", Has: &Pick{Selector: ".name", HasText: "Default"}, Count: 1},
	)
	for _, c := range []struct {
		step Step
		err  string
	}{
		{Step{Action: ActionClick, Selector: "li", Has: &Pick{HasText: "Default"}}, "has needs a selector or a role"},
		{Step{Action: ActionClick, Selector: "li", Has: &Pick{Selector: "span", Name: "x"}}, "a name needs a role"},
		{Step{Action: ActionClick, Has: &Pick{Selector: "span"}}, "has narrows a step's selector or role; set one"},
		{Step{Action: ActionClick, Selector: "li", Has: &Pick{Selector: ".nope"}}, "no element matches li has .nope"},
	} {
		c.step.Timeout = "300ms"
		if err := failSteps(t, br, env, c.step); !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: got %v, want %q", StepSummary(c.step), err, c.err)
		}
	}
}

// nth picks one of a step's matches, counting from 0, or back from the last
// at -1, after every other field has narrowed them, as Playwright's nth.
func TestNth(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<ul>
<li>One <button>Copy</button></li>
<li style="display: none">Hidden <button>Copy</button></li>
<li>Two <button>Copy</button></li>
<li>Three <button>Copy</button></li>
</ul>
<script>
document.addEventListener('click', e => document.title = e.target.closest('li').firstChild.textContent.trim());
</script>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionAssertText, Selector: "li", Nth: ptr(1), Text: "Hidden Copy"},
		Step{Action: ActionAssertText, Selector: "li", Nth: ptr(-1), Text: "Three Copy"},
		Step{Action: ActionAssertText, Selector: "li", Nth: ptr(-2), Text: "Two Copy"},
		// After visible, has_text and the role's own matches.
		Step{Action: ActionAssertText, Selector: "li", Visible: true, Nth: ptr(1), Text: "Two Copy"},
		Step{Action: ActionAssertText, Selector: "li", HasText: "T", Nth: ptr(1), Text: "Three Copy"},
		Step{Action: ActionClick, Role: "button", Name: "Copy", Nth: ptr(1)},
		Step{Action: ActionAssertTitle, Text: "Two"},
		// In within, it picks the one element to look inside.
		Step{Action: ActionClick, Within: &Pick{Selector: "li", Visible: true, Nth: ptr(0)}, Selector: "button"},
		Step{Action: ActionAssertTitle, Text: "One"},
		Step{Action: ActionAssertCount, Within: &Pick{Selector: "li", Nth: ptr(-1)}, Selector: "button", Count: 1},
	)
	for _, c := range []struct {
		step Step
		err  string
	}{
		{Step{Action: ActionClick, Selector: "li", Nth: ptr(4)}, "no element matches li nth 4"},
		{Step{Action: ActionClick, Selector: "li", Nth: ptr(-5)}, "no element matches li nth -5"},
		{Step{Action: ActionAssertCount, Selector: "li", Nth: ptr(0), Count: 1}, "nth picks one element"},
		{Step{Action: ActionAssertTexts, Selector: "li", Nth: ptr(0), Texts: []string{"One Copy"}}, "nth picks one element"},
	} {
		c.step.Timeout = "300ms"
		if err := failSteps(t, br, env, c.step); !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: got %v, want %q", StepSummary(c.step), err, c.err)
		}
	}
}

// Selectors reach into open shadow roots, as Playwright's do: the
// document's matches first, then each shadow root's, hosts in document
// order, nested ones under theirs. A closed shadow root stays shut.
func TestShadowSelectors(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<x-picker></x-picker>
<span class="deep">Light</span>
<x-closed></x-closed>
<x-embed></x-embed>
<x-button><span id="lbl">Label</span></x-button>
<script>
customElements.define('x-button', class extends HTMLElement {
  connectedCallback() {
    this.attachShadow({mode: 'open'}).innerHTML = '<button id="inner" style="padding: 0; border: 0"><slot></slot></button>';
    this.shadowRoot.querySelector('button').onclick = () => document.title = 'slotted';
  }
});
customElements.define('x-inner', class extends HTMLElement {
  connectedCallback() { this.attachShadow({mode: 'open'}).innerHTML = '<span class="deep">Nested</span>'; }
});
customElements.define('x-picker', class extends HTMLElement {
  connectedCallback() {
    this.attachShadow({mode: 'open'}).innerHTML = '<input type="search"><div class="status">Linked up</div><div class="status">Linked</div><span class="deep">Deep</span><x-inner></x-inner>';
    this.shadowRoot.querySelector('input').focus();
    this.shadowRoot.querySelector('.status').onclick = () => document.title = 'clicked';
  }
});
customElements.define('x-closed', class extends HTMLElement {
  connectedCallback() { this.attachShadow({mode: 'closed'}).innerHTML = '<span class="deep">Closed</span>'; }
});
customElements.define('x-embed', class extends HTMLElement {
  connectedCallback() { this.attachShadow({mode: 'open'}).innerHTML = '<iframe srcdoc="<p>Framed</p>"></iframe>'; }
});
</script>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionAssertFocused, Selector: "input[type=search]"},
		Step{Action: ActionAssertText, Selector: ".status", HasText: "Linked", Exact: true, Text: "Linked"},
		Step{Action: ActionAssertText, Selector: "*", HasText: "Nested", Text: "Nested"},
		Step{Action: ActionAssertTexts, Selector: ".deep", Texts: []string{"Light", "Deep", "Nested"}},
		Step{Action: ActionClick, Selector: ".status", HasText: "up"},
		Step{Action: ActionAssertTitle, Text: "clicked"},
		Step{Action: ActionAssertCount, Role: "searchbox", Selector: "x-picker", Count: 1},
		// One selector does not cross into a shadow root; within does.
		Step{Action: ActionAssertCount, Selector: "x-picker input", Count: 0},
		Step{Action: ActionAssertCount, Within: &Pick{Selector: "x-picker"}, Selector: "input", Count: 1},
		Step{Action: ActionWaitVisible, Frame: "iframe", Selector: "p", HasText: "Framed"},
		// What is slotted into an element is in it.
		Step{Action: ActionClick, Selector: "#inner"},
		Step{Action: ActionAssertTitle, Text: "slotted"},
		// A host has the text of its shadow root, as in Playwright.
		Step{Action: ActionAssertCount, Selector: "x-picker", HasText: "Linked up", Count: 1},
		Step{Action: ActionAssertText, Selector: "*", HasText: "Nested", Text: "Nested"},
	)
}

// Element steps, evals and text waits with a frame act in that iframe's
// document: same-origin, sandboxed without scripts, or cross-origin, and at
// an offset in the page.
func TestFrames(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	br, err := NewBrowser(context.Background(), Launch{Hosts: map[string]http.Handler{"https://other.test": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><body style="margin: 0"><button id="go" style="margin: 40px 0 0 50px">Go</button>
<script>go.onclick = () => parent.postMessage('cross clicked', '*')</script>`))
	})}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(br.Close)
	// A small button in a frame with a margin, border and padding: aiming
	// at it without them in the offset misses it.
	same := `<body style='margin: 0'><input data-testid='message-input'><button style='position: absolute; left: 30px; top: 40px; width: 12px; height: 12px; padding: 0' onclick='parent.document.title = &quot;same clicked&quot;'>Go</button></body>`
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body style="margin: 0">
<p>Main says hi</p>
<input data-testid="message-input" value="main">
<iframe id="same" style="margin: 9px; border: 7px solid; padding: 11px" srcdoc="`+same+`"></iframe>
<iframe id="sandboxed" sandbox="allow-popups allow-popups-to-escape-sandbox" srcdoc="<p>Olympic hosts</p><button>Search</button>"></iframe>
<div style="height: 2000px"></div>
<iframe id="cross" style="border: 0" src="https://other.test/"></iframe>
<script>addEventListener('message', e => document.title = e.data)</script>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionWaitVisible, Frame: "#sandboxed", Selector: "p", HasText: "Olympic hosts"},
		Step{Action: ActionWaitText, Frame: "#sandboxed", Text: "Olympic hosts"},
		Step{Action: ActionWaitTextGone, Frame: "#same", Text: "Main says hi"},
		Step{Action: ActionAssertText, Frame: "#sandboxed", Role: "button", Text: "Search"},
		Step{Action: ActionEval, Frame: "#sandboxed", Expression: "document.querySelector('p').textContent", Expect: ptr("Olympic hosts")},
		Step{Action: ActionAssertValue, Frame: "#same", Selector: "[data-testid=message-input]", Value: ""},
		Step{Action: ActionFill, Frame: "#same", Selector: "[data-testid=message-input]", Value: "hi"},
		Step{Action: ActionAssertValue, Frame: "#same", Selector: "[data-testid=message-input]", Value: "hi"},
		Step{Action: ActionAssertFocused, Frame: "#same", Selector: "[data-testid=message-input]"},
		Step{Action: ActionAssertValue, Selector: "[data-testid=message-input]", Value: "main"},
		Step{Action: ActionClick, Frame: "#same", Role: "button", Name: "Go"},
		Step{Action: ActionAssertTitle, Text: "same clicked"},
		// Below the fold, in another origin.
		Step{Action: ActionAssertNotInViewport, Frame: "#cross", Selector: "#go"},
		Step{Action: ActionWaitVisible, Frame: "#cross", Selector: "#go"},
		Step{Action: ActionClick, Frame: "#cross", Selector: "#go"},
		Step{Action: ActionAssertTitle, Text: "cross clicked"},
		Step{Action: ActionAssertInViewport, Frame: "#cross", Selector: "#go"},
		Step{Action: ActionAssertCount, Frame: "#cross", Role: "button", Name: "Go", Count: 1},
		// A frame that comes later is waited for.
		Step{Action: ActionEval, Expression: `setTimeout(() => document.body.insertAdjacentHTML('afterbegin', '<iframe id="late" srcdoc="<p>Late</p>"></iframe>'), 300)`},
		Step{Action: ActionWaitVisible, Frame: "#late", Selector: "p"},
	)
	for _, c := range []struct {
		step Step
		err  string
	}{
		{Step{Action: ActionClick, Frame: "#nope", Selector: "p"}, "no iframe matches #nope"},
		{Step{Action: ActionClick, Frame: "p", Selector: "p"}, "p is a <p>, not an iframe"},
		{Step{Action: ActionClick, Frame: "#same", X: ptr(1.0), Y: ptr(1.0)}, "a frame scopes a step's selector or role"},
		{Step{Action: ActionAssertURL, Frame: "#same", Text: "x"}, "a frame scopes a step's selector or role"},
	} {
		c.step.Timeout = "300ms"
		if err := failSteps(t, br, env, c.step); !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: got %v, want %q", StepSummary(c.step), err, c.err)
		}
	}
	// Chrome does not render a cross-origin frame out of view, so its roles
	// cannot be read.
	runSteps(t, br, env, Step{Action: ActionEval, Expression: "scrollTo(0, 0)"})
	if err := failSteps(t, br, env, Step{Action: ActionAssertCount, Frame: "#cross", Role: "button", Count: 1, Timeout: "300ms"}); !strings.Contains(err.Error(), "out of view") {
		t.Errorf("role in a cross-origin frame out of view: %v", err)
	}
}

// assert_download waits for a download to begin with the file name, in the
// page or a frame, as Playwright's download event and suggestedFilename,
// and takes it and those before it.
func TestDownloads(t *testing.T) {
	br := newBrowserOrSkip(t)
	// The frame's begins later than the page's.
	save := func(name, delay string) string {
		return `<button onclick="const a = document.createElement('a'); a.href = window.URL.createObjectURL(new Blob(['red'])); a.download = '` + name + `'; setTimeout(() => a.click(), ` + delay + `)">Save</button>`
	}
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
`+save("colors.txt", "0")+`
<button onclick="setTimeout(() => { for (const name of ['a.txt', 'b.txt']) { const a = document.createElement('a'); a.href = window.URL.createObjectURL(new Blob([name])); a.download = name; a.click(); } }, 200)">Both</button>
<iframe srcdoc="`+html.EscapeString(save("frame.txt", "300"))+`"></iframe>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionAssertNoDownload},
		Step{Action: ActionClick, Role: "button", Name: "Save"},
		Step{Action: ActionAssertDownload, Value: "colors.txt"},
		Step{Action: ActionClick, Frame: "iframe", Role: "button", Name: "Save"},
		Step{Action: ActionAssertDownload, Value: "frame.txt"},
		Step{Action: ActionClick, Role: "button", Name: "Save"},
		Step{Action: ActionClick, Frame: "iframe", Role: "button", Name: "Save"},
		Step{Action: ActionAssertDownload, Value: "frame.txt"},
		// One takes those before it, not those after. Both begin in one
		// task, which saving them, rather than denying them, never loses
		// one of.
		Step{Action: ActionClick, Role: "button", Name: "Both"},
		Step{Action: ActionAssertDownload, Value: "a.txt"},
		Step{Action: ActionAssertDownload, Value: "b.txt"},
	)
	for _, c := range []struct {
		step Step
		err  string
	}{
		// The last took the colors.txt before it.
		{Step{Action: ActionAssertDownload, Value: "colors.txt"}, `assert_download: expected a download of "colors.txt", got none`},
		{Step{Action: ActionClick, Role: "button", Name: "Save"}, ""},
		{Step{Action: ActionAssertDownload, Value: "Colors.txt"}, `assert_download: expected a download of "Colors.txt", got ["colors.txt"]`},
		// assert_no_download fails at once, without waiting out its
		// timeout, and takes nothing.
		{Step{Action: ActionAssertNoDownload}, `(assert_no_download): assert_no_download: expected no download, got ["colors.txt"]`},
		{Step{Action: ActionAssertDownload, Value: "colors.txt"}, ""},
		{Step{Action: ActionAssertNoDownload}, ""},
		{Step{Action: ActionAssertDownload}, "assert_download needs a value: the file name"},
	} {
		if c.err == "" {
			runSteps(t, br, env, c.step)
			continue
		}
		c.step.Timeout = "500ms"
		if err := failSteps(t, br, env, c.step); !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: got %v, want %q", StepSummary(c.step), err, c.err)
		}
	}
	// The files are saved, and go with the browser.
	if saved, err := os.ReadDir(br.downloadDir); len(saved) == 0 {
		t.Fatalf("download dir: %d files, %v", len(saved), err)
	}
	br.Close()
	if _, err := os.Stat(br.downloadDir); !os.IsNotExist(err) {
		t.Errorf("download dir after Close: %v", err)
	}
}

// A frame's eval runs in the frame's own world, as Playwright's do, so it
// sees the globals the frame's scripts define and waits for their promises.
func TestFrameEvalSeesTheFramesGlobals(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	page := func(name string) string {
		return `<!doctype html><body><p>` + name + `</p><script>window.tell = () => new Promise(done => setTimeout(() => done('told ' + document.querySelector('p').textContent), 50));</script></body>`
	}
	br, err := NewBrowser(context.Background(), Launch{Hosts: map[string]http.Handler{"https://other.test": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(page("cross")))
	})}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(br.Close)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><body>
<iframe id="same" srcdoc="`+html.EscapeString(page("same"))+`"></iframe>
<iframe id="cross" src="https://other.test/"></iframe>
<script>window.tell = () => 'told main';</script>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: `tell()`, Expect: ptr("told main")},
		Step{Action: ActionEval, Frame: "#same", Expression: `tell()`, Expect: ptr("told same")},
		Step{Action: ActionEval, Frame: "#cross", Expression: `const t = tell(); t`, Expect: ptr("told cross")},
	)
}

// Whether Chrome renders a cross-origin frame, so that its roles can be
// read and its elements wait for frames, is whether the frame intersects
// the viewport through every scroller around it.
func TestCrossOriginFrameInView(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	br, err := NewBrowser(context.Background(), Launch{Hosts: map[string]http.Handler{"https://other.test": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><body style="margin: 0"><button id="go">Go</button>`))
	})}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(br.Close)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body style="margin: 0">
<iframe id="thin" style="position: absolute; left: 0; top: 0; width: 0; height: 100px; border: 0" src="https://other.test/"></iframe>
<div id="scroller" style="height: 100px; overflow: auto"><div style="height: 300px"></div><iframe id="clipped" src="https://other.test/"></iframe></div>
<iframe id="none" style="display: none" src="https://other.test/"></iframe>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionAssertCount, Frame: "#thin", Role: "button", Count: 1},
	)
	for _, c := range []struct {
		step Step
		err  string
	}{
		{Step{Action: ActionAssertCount, Frame: "#clipped", Role: "button", Count: 1}, "roles in the cross-origin frame #clipped cannot be found while it is hidden or out of view"},
		{Step{Action: ActionAssertCount, Frame: "#none", Role: "button", Count: 1}, "roles in the cross-origin frame #none cannot be found while it is hidden or out of view"},
		{Step{Action: ActionClick, Frame: "#none", Selector: "#go"}, "frame #none is not visible"},
	} {
		c.step.Timeout = "1s"
		if err := failSteps(t, br, env, c.step); !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: got %v, want %q", StepSummary(c.step), err, c.err)
		}
	}
	runSteps(t, br, env,
		Step{Action: ActionEval, Expression: `scroller.scrollTop = 300`},
		Step{Action: ActionAssertCount, Frame: "#clipped", Role: "button", Count: 1},
	)
}

// Chrome names elements from their rendered text, text-transform and all;
// Playwright, as the DOM has it. An exact name matches the DOM's.
func TestExactNamesIgnoreTextTransform(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<h3 id="h" style="text-transform: uppercase">Auto compaction</h3>
<div role="radiogroup" aria-labelledby="h"><input type="radio" aria-label="On"></div>
<button style="text-transform: uppercase">Save it</button>
<label style="text-transform: capitalize">the <input> box</label>
<button aria-label="SHOUT" style="text-transform: lowercase">Shout</button>
<button style="text-transform: uppercase"><div>Two</div><div>blocks</div></button>
<button><span style="display: none">look</span> Ok</button>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionAssertCount, Role: "radiogroup", Name: "Auto compaction", Exact: true, Count: 1},
		Step{Action: ActionAssertCount, Role: "radiogroup", Name: "AUTO COMPACTION", Exact: true, Count: 0},
		Step{Action: ActionAssertCount, Role: "button", Name: "Save it", Exact: true, Count: 1},
		Step{Action: ActionAssertCount, Role: "textbox", Name: "the box", Exact: true, Count: 1},
		// An aria-label is the name as written.
		Step{Action: ActionAssertCount, Role: "button", Name: "SHOUT", Exact: true, Count: 1},
		Step{Action: ActionAssertCount, Role: "button", Name: "Shout", Exact: true, Count: 0},
		// Chrome spaces out blocks that the DOM text runs together.
		Step{Action: ActionAssertCount, Role: "button", Name: "Two blocks", Exact: true, Count: 1},
		Step{Action: ActionAssertCount, Role: "button", Name: "TWO BLOCKS", Exact: true, Count: 0},
		// Hidden text the name leaves out holds the name in another case.
		Step{Action: ActionAssertCount, Role: "button", Name: "Ok", Exact: true, Count: 1},
		Step{Action: ActionAssertCount, Role: "button", Name: "OK", Exact: true, Count: 0},
	)
}

// Thousands of matches take under a second (one by one, they took 7s).
func TestManyRoles(t *testing.T) {
	if raceEnabled {
		t.Skip("too slow to time with -race")
	}
	br := newBrowserOrSkip(t)
	runSteps(t, br, &Env{BaseURL: serveHTML(t, blankPage)},
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: `document.body.innerHTML = '<button>Go</button>'.repeat(2000); 'made'`},
		Step{Action: ActionAssertCount, Role: "button", Name: "Go", Count: 2000, Timeout: "5s"},
	)
}

func TestFormStateAssertions(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<input id="name" value="attr">
<button id="on">on</button><button id="off" disabled>off</button>
<fieldset disabled><button id="inset">in set</button></fieldset>
<div aria-disabled="true"><button id="aria">aria</button></div>
<button id="empty" data-x="">empty</button>
<select id="pick"><option>a</option><option selected>b</option></select>
<div aria-disabled="true"><input id="ariain"></div>
<x-picker></x-picker>
<script>
customElements.define('x-picker', class extends HTMLElement {
  connectedCallback() { this.attachShadow({mode: 'open'}).innerHTML = '<input id="search">'; }
});
</script>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionFill, Selector: "#name", Value: "typed"},
		Step{Action: ActionAssertValue, Selector: "#name", Value: "typed"},
		Step{Action: ActionAssertAttribute, Selector: "#name", Attribute: "value", Value: "attr"},
		Step{Action: ActionAssertValue, Selector: "#pick", Value: "b"},
		Step{Action: ActionAssertEnabled, Selector: "#on"},
		Step{Action: ActionAssertDisabled, Selector: "#off"},
		Step{Action: ActionAssertDisabled, Selector: "#inset"},
		Step{Action: ActionAssertDisabled, Selector: "#aria"},
		Step{Action: ActionAssertAttribute, Selector: "#off", Attribute: "disabled", Value: ""},
		Step{Action: ActionAssertAttribute, Selector: "#empty", Attribute: "data-x", Value: ""},
		Step{Action: ActionAssertFocused, Selector: "#name"},
		Step{Action: ActionClick, Selector: "#on"},
		Step{Action: ActionAssertFocused, Selector: "#on"},
		Step{Action: ActionEval, Expression: "document.querySelector('x-picker').shadowRoot.querySelector('input').focus()"},
		Step{Action: ActionAssertFocused, Selector: "x-picker"},
	)
	for _, c := range []struct {
		step Step
		err  string
	}{
		{Step{Action: ActionAssertAttribute, Selector: "#on", Attribute: "disabled", Value: ""}, `#on has no "disabled" attribute`},
		{Step{Action: ActionAssertValue, Selector: "#name", Value: "attr"}, `expected "attr", got "typed"`},
		{Step{Action: ActionAssertValue, Selector: "#on", Value: ""}, "a <button> is not an input, textarea or select"},
		{Step{Action: ActionAssertValue, Selector: "option", Value: "a"}, "a <option> is not an input, textarea or select"},
		{Step{Action: ActionFill, Selector: "#ariain", Value: "x"}, "#ariain is disabled"},
		{Step{Action: ActionAssertFocused, Selector: "x-picker"}, "x-picker is not focused; button#on is"},
		{Step{Action: ActionAssertEnabled, Selector: "#off"}, "#off is disabled"},
		{Step{Action: ActionAssertDisabled, Selector: "#on"}, "#on is enabled"},
		{Step{Action: ActionAssertFocused, Selector: "#name"}, "#name is not focused; button#on is"},
	} {
		c.step.Timeout = "300ms"
		if err := failSteps(t, br, env, Step{Action: ActionNavigate, URL: "/"}, Step{Action: ActionFill, Selector: "#name", Value: "typed"}, Step{Action: ActionClick, Selector: "#on"}, c.step); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: got %v, want %q", StepSummary(c.step), err, c.err)
		}
	}
}

// Dialogs close as Playwright closes them when no test listens: the page
// may be left on beforeunload, and alerts, confirms and prompts cancel.
func TestDialogsClose(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<button id="b">b</button>
<script>addEventListener('beforeunload', e => e.preventDefault());</script>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionClick, Selector: "#b"},
		Step{Action: ActionEval, Expression: "alert('a'); [confirm('c'), prompt('p', 'x')]", Expect: ptr("[false,null]")},
		Step{Action: ActionNavigate, URL: "/?left", Timeout: "5s"},
		Step{Action: ActionAssertURL, Text: "?left"},
	)
}

const clickPage = `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body style="margin: 0">
<button id="b" style="width: 100px; height: 40px">b</button>
<button id="off" disabled>off</button>
<div id="cover" style="position: fixed; top: 0; left: 0; width: 200px; height: 100px; display: none"></div>
<script>
window.log = [];
for (const type of ['mousedown', 'mouseup', 'click', 'dblclick', 'contextmenu']) {
  document.addEventListener(type, e => log.push(type + ':' + e.target.id + ':' + e.button + ':' + e.detail + '@' + e.clientX + ',' + e.clientY));
}
</script>
</body></html>`

func TestClick(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, clickPage)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionClick, Selector: "#b", X: ptr(10.0), Y: ptr(5.0)},
		Step{Action: ActionEval, Expression: `log.join(' ')`, Expect: ptr("mousedown:b:0:1@10,5 mouseup:b:0:1@10,5 click:b:0:1@10,5")},
		Step{Action: ActionEval, Expression: `log = []`},
		Step{Action: ActionClick, Selector: "#b", Count: 2},
		Step{
			Action: ActionEval, Expression: `log.filter(e => e.startsWith('click') || e.startsWith('dblclick')).map(e => e.split('@')[0]).join(' ')`,
			Expect: ptr("click:b:0:1 click:b:0:2 dblclick:b:0:2"),
		},
		Step{Action: ActionEval, Expression: `log = []`},
		Step{Action: ActionClick, Selector: "#b", Button: "right"},
		Step{Action: ActionEval, Expression: `log.map(e => e.split('@')[0]).join(' ')`, Expect: ptr("mousedown:b:2:1 contextmenu:b:2:0 mouseup:b:2:1")},
		Step{Action: ActionEval, Expression: `log = []`},
		// Without a selector, x,y is a point in the viewport.
		Step{Action: ActionClick, X: ptr(50.0), Y: ptr(20.0)},
		Step{Action: ActionEval, Expression: `log.at(-1)`, Expect: ptr("click:b:0:1@50,20")},
	)
	if err := failSteps(t, br, env, Step{Action: ActionClick, Selector: "#off", Timeout: "300ms"}); !strings.Contains(err.Error(), "#off is disabled") {
		t.Errorf("disabled click error = %v", err)
	}
	err := failSteps(t, br, env,
		Step{Action: ActionEval, Expression: `document.getElementById('cover').style.display = 'block'`},
		Step{Action: ActionClick, Selector: "#b", Timeout: "300ms"})
	if !strings.Contains(err.Error(), "#b is covered by div#cover") {
		t.Errorf("covered click error = %v", err)
	}
	// A link wrapped over two lines is clicked in its first box, not the
	// middle of its bounding box, which is on neither.
	runSteps(t, br, env,
		Step{Action: ActionEval, Expression: `log = []; document.getElementById('cover').style.display = 'none'; document.body.insertAdjacentHTML('beforeend', '<p style="width: 200px; font: 16px monospace">aaaaaaaaaaaaaaaaaa <a id="wrap" href="#w">bbbbbbbbbb cccccccccccccccccc</a></p>')`},
		Step{Action: ActionClick, Selector: "#wrap", Timeout: "1s"},
		Step{Action: ActionEval, Expression: `location.hash + ' ' + log.at(-1).split('@')[0]`, Expect: ptr("#w click:wrap:0:1")},
	)
	// A click waits for the element to be uncovered.
	runSteps(t, br, env,
		Step{Action: ActionEval, Expression: `log = []; document.getElementById('cover').style.display = 'block'; setTimeout(() => document.getElementById('cover').style.display = 'none', 300)`},
		Step{Action: ActionClick, Selector: "#b"},
		Step{Action: ActionEval, Expression: `log.at(-1).split('@')[0]`, Expect: ptr("click:b:0:1")},
	)
}

// A click, hover or touch whose element moves away between the checks and
// the events does not land on what took its place: as with Playwright's
// hit-target interceptor, the events are blocked and the step tries again.
func TestHitTarget(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body style="margin: 0">
<button id="near" style="position: absolute; left: 20px; top: 20px; width: 100px; height: 40px">Near</button>
<button id="go" style="position: absolute; left: 20px; top: 20px; width: 100px; height: 40px">Go</button>
<button id="badge" style="position: absolute; left: 20px; top: 100px; width: 100px; height: 40px"><span id="label">Inbox</span><b id="count" style="position: absolute; inset: 0">3</b></button>
<iframe style="position: absolute; left: 20px; top: 200px; width: 400px; height: 100px" srcdoc="
<button id='near' style='position: absolute; left: 20px; top: 20px; width: 100px; height: 40px'>Near</button>
<button id='go' style='position: absolute; left: 20px; top: 20px; width: 100px; height: 40px'>Go</button>
<script>
// The step's checks run apart from the frame's scripts, so #go moves as the
// mouse arrives, just before the click's events.
window.shift = false;
window.log = [];
addEventListener('mousemove', () => {
  if (!shift) return;
  shift = false;
  go.style.left = '200px';
}, {capture: true});
document.addEventListener('click', e => log.push(e.target.id));
</script>"></iframe>
<script>
// With shift set, #go moves away just after the step checks what is at
// its point, before the events come: a layout shift at the worst time.
window.shift = false;
window.log = [];
const elementFromPoint = document.elementFromPoint;
document.elementFromPoint = function(...args) {
  if (shift) {
    shift = false;
    queueMicrotask(() => go.style.left = '200px');
  }
  return elementFromPoint.apply(this, args);
};
for (const type of ['click', 'touchstart']) {
  document.addEventListener(type, e => log.push(type + ':' + e.target.id));
}
</script></body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: `shift = true`},
		Step{Action: ActionClick, Selector: "#go"},
		Step{Action: ActionEval, Expression: `log.join(' ')`, Expect: ptr("click:go")},
		Step{Action: ActionEval, Expression: `go.style.left = '20px'; shift = true; log = []`},
		Step{Action: ActionHover, Selector: "#go"},
		Step{Action: ActionEval, Expression: `go.matches(':hover')`, Expect: ptr("true")},
		Step{Action: ActionEval, Expression: `go.style.left = '20px'; shift = true`},
		Step{Action: ActionTouch, Value: "start", Selector: "#go"},
		Step{Action: ActionTouch, Value: "end"},
		Step{Action: ActionEval, Expression: `log.join(' ') + ' ' + go.style.left`, Expect: ptr("touchstart:go click:go 200px")},
		// Anything in the button or link an element is in counts as it.
		Step{Action: ActionEval, Expression: `log = []`},
		Step{Action: ActionClick, Selector: "#label", Timeout: "1s"},
		Step{Action: ActionEval, Expression: `log.join(' ')`, Expect: ptr("click:count")},
		// So does a click in a frame.
		Step{Action: ActionEval, Expression: `frames[0].shift = true`},
		Step{Action: ActionClick, Frame: "iframe", Selector: "#go"},
		Step{Action: ActionEval, Expression: `frames[0].log.join(' ')`, Expect: ptr("go")},
	)
}

// A page that fakes its clock, freezing requestAnimationFrame, can still be
// clicked: the check that an element holds still uses the browser's frames.
func TestClickWithFrozenFrames(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	br, err := NewBrowser(context.Background(), Launch{InitScript: `window.requestAnimationFrame = () => 0;`})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(br.Close)
	runSteps(t, br, &Env{BaseURL: serveHTML(t, clickPage)},
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionClick, Selector: "#b", Timeout: "2s"},
		Step{Action: ActionEval, Expression: `log.at(-1).split('@')[0]`, Expect: ptr("click:b:0:1")},
	)
}

// A drag selects text natively and starts HTML drag and drop, which need
// moves that name the held button.
func TestNativeDrag(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body style="margin: 0">
<p id="p" style="font: 20px monospace; margin: 0">hello world</p>
<div id="d" draggable="true" style="width: 50px; height: 50px; margin-top: 20px">drag</div>
<div id="t" style="width: 50px; height: 50px"></div>
<script>
window.log = [];
document.addEventListener('dragstart', e => log.push('dragstart:' + e.target.id));
document.addEventListener('drop', e => { e.preventDefault(); log.push('drop:' + e.target.id); });
document.addEventListener('dragover', e => e.preventDefault());
</script></body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionHover, Selector: "#p", X: ptr(1.0), Y: ptr(10.0)},
		Step{Action: ActionMouseDown},
		Step{Action: ActionHover, Selector: "#p", X: ptr(60.0), Y: ptr(10.0), Count: 5},
		Step{Action: ActionMouseUp},
		Step{Action: ActionEval, Expression: `getSelection().toString()`, Expect: ptr("hello")},
		Step{Action: ActionHover, Selector: "#d"},
		Step{Action: ActionMouseDown},
		Step{Action: ActionHover, Selector: "#t", Count: 5},
		Step{Action: ActionMouseUp},
		Step{Action: ActionEval, Expression: `log.join(' ')`, Expect: ptr("dragstart:d drop:t")},
	)
}

func TestDragAndWheel(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body style="margin: 0; height: 3000px">
<div id="a" style="width: 50px; height: 50px"></div><div id="z" style="width: 50px; height: 50px"></div>
<script>
window.log = [];
document.addEventListener('mousedown', e => log.push('down:' + e.target.id));
document.addEventListener('mousemove', e => { if (e.buttons) log.push('drag:' + e.clientY); });
document.addEventListener('mouseup', e => log.push('up:' + e.target.id));
document.addEventListener('wheel', e => log.push('wheel:' + e.deltaY));
</script></body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionHover, Selector: "#a"},
		Step{Action: ActionMouseDown},
		Step{Action: ActionHover, Selector: "#z", Count: 2},
		Step{Action: ActionMouseUp},
		Step{Action: ActionEval, Expression: `log.join(' ')`, Expect: ptr("down:a drag:50 drag:75 up:z")},
		Step{Action: ActionWheel, X: ptr(0.0), Y: ptr(400.0)},
		Step{Action: ActionEval, Expression: `log.at(-1).split(':')[0] + ' ' + scrollY`, Expect: ptr("wheel 400")},
		// The page scrolls by CSS pixels whatever the device's scale.
		Step{Action: ActionSetViewport, Width: 800, Height: 600, Scale: 2, Mobile: ptr(false), Touch: ptr(false)},
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionHover, Selector: "#a"},
		Step{Action: ActionWheel, X: ptr(0.0), Y: ptr(400.0)},
		Step{Action: ActionEval, Expression: `scrollY`, Expect: ptr("400")},
	)
}

func TestTouch(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body style="margin: 0">
<div id="pad" style="margin: 20px; width: 200px; height: 200px; touch-action: none"></div>
<script>
window.log = [];
for (const type of ['touchstart', 'touchmove', 'touchend']) {
  document.addEventListener(type, e => log.push(type + ':' + e.touches.length + ':' + [...e.touches].map(t => t.clientX + ',' + t.clientY).join(';')));
}
</script></body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionTouch, Value: "start", Selector: "#pad", X: ptr(10.0), Y: ptr(10.0)},
		Step{Action: ActionTouch, Value: "move", Selector: "#pad"},
		Step{Action: ActionTouch, Value: "end"},
		Step{Action: ActionTouch, Value: "start", Selector: "#pad", Points: "10,10 100,100"},
		Step{Action: ActionTouch, Value: "move", Points: "40,40 130,130"},
		Step{Action: ActionTouch, Value: "end"},
		Step{
			Action: ActionEval, Expression: `log.join(' ')`,
			// Chrome fires an event per finger that lands or lifts.
			Expect: ptr("touchstart:1:30,30 touchmove:1:120,120 touchend:0: touchstart:1:30,30 touchstart:2:30,30;120,120 touchmove:2:40,40;130,130 touchend:1:130,130 touchend:0:"),
		},
		// A move with a count goes there in even steps from where each
		// finger was.
		Step{Action: ActionEval, Expression: `log = []`},
		Step{Action: ActionTouch, Value: "start", Selector: "#pad", Points: "10,10 100,100"},
		Step{Action: ActionTouch, Value: "move", Selector: "#pad", Points: "40,40 130,70", Count: 3},
		Step{Action: ActionTouch, Value: "move", Selector: "#pad", Points: "10,10 100,100", Count: 2},
		Step{Action: ActionTouch, Value: "end"},
		Step{Action: ActionTouch, Value: "start", X: ptr(50.0), Y: ptr(50.0)},
		Step{Action: ActionTouch, Value: "move", X: ptr(150.0), Y: ptr(50.0), Count: 4},
		Step{Action: ActionTouch, Value: "cancel"},
		Step{
			Action: ActionEval, Expression: `log.filter(e => e.startsWith('touchmove')).join(' ')`,
			Expect: ptr("touchmove:2:40,40;130,110 touchmove:2:50,50;140,100 touchmove:2:60,60;150,90 touchmove:2:45,45;135,105 touchmove:2:30,30;120,120 touchmove:1:75,50 touchmove:1:100,50 touchmove:1:125,50 touchmove:1:150,50"),
		},
	)
}

func TestTypeAndFill(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<textarea id="t"></textarea>
<select id="s"><option value="a">A</option><option value="b">B</option></select>
<div id="ce" contenteditable="true">old</div>
<input id="off" disabled>
<input id="gone" style="display: none">
<script>
window.log = [];
document.addEventListener('keydown', e => log.push(e.key));
document.addEventListener('change', e => log.push('change:' + e.target.id));
</script></body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionClick, Selector: "#t"},
		Step{Action: ActionType, Value: "hi\nyo"},
		Step{Action: ActionEval, Expression: `JSON.stringify(document.getElementById('t').value) + ' ' + log.join(' ')`, Expect: ptr(`"hi\nyo" h i Enter y o`)},
		Step{Action: ActionFill, Selector: "#s", Value: "b"},
		Step{Action: ActionEval, Expression: `document.getElementById('s').value + ' ' + log.at(-1)`, Expect: ptr("b change:s")},
		Step{Action: ActionFill, Selector: "#ce", Value: "new"},
		Step{Action: ActionEval, Expression: `document.getElementById('ce').textContent + ' ' + (document.activeElement.id)`, Expect: ptr("new ce")},
	)
	// insert_text inputs text at once, without key events.
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: `document.addEventListener('input', e => log.push('input:' + e.inputType + ':' + e.data))`},
		Step{Action: ActionFill, Selector: "#t", Value: "a"},
		Step{Action: ActionEval, Expression: `log = []`},
		Step{Action: ActionInsertText, Value: "b ü😀"},
		Step{Action: ActionEval, Expression: `document.getElementById('t').value + ' ' + log.join(' ')`, Expect: ptr("ab ü😀 input:insertText:b ü😀")},
	)
	if err := failSteps(t, br, env, Step{Action: ActionFill, Selector: "#off", Value: "x", Timeout: "300ms"}); !strings.Contains(err.Error(), "#off is disabled") {
		t.Errorf("disabled fill error = %v", err)
	}
	if err := failSteps(t, br, env, Step{Action: ActionFill, Selector: "#s", Value: "zzz", Timeout: "300ms"}); !strings.Contains(err.Error(), "no option") {
		t.Errorf("bad option fill error = %v", err)
	}
	if err := failSteps(t, br, env, Step{Action: ActionFill, Selector: "#gone", Value: "x", Timeout: "300ms"}); !strings.Contains(err.Error(), "#gone is not visible") {
		t.Errorf("hidden fill error = %v", err)
	}
}

// press_key, type and insert_text with an element focus it first, as
// Playwright's locator.press and pressSequentially do: an input they were
// not focused on gets its caret at the start.
func TestKeysIntoElement(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<input id="a" aria-label="First">
<input id="b" value="xyz">
<input id="email" type="email" value="e">
<div id="plain">Not focusable</div>
<input id="off" disabled>
<input id="gone" style="display: none">
<script>b.setSelectionRange(3, 3)</script>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionClick, Selector: "#a"},
		Step{Action: ActionType, Selector: "#b", Value: "hi"},
		Step{Action: ActionType, Selector: "#b", Value: "!"},
		Step{Action: ActionEval, Expression: `b.value`, Expect: ptr("hi!xyz")},
		Step{Action: ActionInsertText, Role: "textbox", Name: "First", Value: "ins"},
		Step{Action: ActionPressKey, Selector: "#a", Key: "Backspace"},
		Step{Action: ActionEval, Expression: `a.value`, Expect: ptr("in")},
		// An email input has no caret to move.
		Step{Action: ActionType, Selector: "#email", Value: "x"},
		Step{Action: ActionEval, Expression: `email.value.length + ' ' + email.value.includes('x')`, Expect: ptr("2 true")},
		Step{Action: ActionAssertFocused, Selector: "#email"},
		Step{Action: ActionAssertNotFocused, Selector: "#a"},
	)
	for _, c := range []struct {
		step Step
		err  string
	}{
		{Step{Action: ActionType, Selector: "#plain", Value: "x"}, "#plain cannot take focus"},
		{Step{Action: ActionPressKey, Selector: "#off", Key: "Enter"}, "#off is disabled"},
		{Step{Action: ActionInsertText, Selector: "#gone", Value: "x"}, "#gone is not visible"},
		{Step{Action: ActionAssertNotFocused, Selector: "#email"}, "#email is focused"},
		{Step{Action: ActionAssertNotFocused, Selector: "#missing"}, "no element matches #missing"},
	} {
		c.step.Timeout = "300ms"
		if err := failSteps(t, br, env, c.step); !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: got %v, want %q", StepSummary(c.step), err, c.err)
		}
	}
}

// Pointer and key steps carry the modifiers they name and those key_down
// holds, as Playwright's do.
func TestModifiers(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, clickPage)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: `for (const type of ['click', 'keydown', 'keyup', 'mousemove']) document.addEventListener(type, e => log.push(type + ':' + (e.key || '') + ':' + e.shiftKey + e.altKey))`},
		Step{Action: ActionEval, Expression: `log = []`},
		Step{Action: ActionClick, Selector: "#b", Modifiers: "Shift"},
		Step{Action: ActionEval, Expression: `log.filter(e => e.startsWith('click::')).join(' ')`, Expect: ptr("click::truefalse")},
		Step{Action: ActionEval, Expression: `log = []`},
		Step{Action: ActionKeyDown, Key: "Alt"},
		Step{Action: ActionHover, Selector: "#b", X: ptr(5.0), Y: ptr(5.0)},
		Step{Action: ActionPressKey, Key: "a"},
		Step{Action: ActionKeyUp, Key: "Alt"},
		Step{Action: ActionPressKey, Key: "b"},
		Step{Action: ActionEval, Expression: `log.filter(e => !e.startsWith('click')).join(' ')`, Expect: ptr("keydown:Alt:falsetrue mousemove::falsetrue keydown:a:falsetrue keyup:a:falsetrue keyup:Alt:falsefalse keydown:b:falsefalse keyup:b:falsefalse")},
	)
	// Pressing a held key again repeats it.
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: "repeats = []; document.addEventListener('keydown', e => repeats.push(e.key + ':' + e.ctrlKey + ':' + e.repeat))"},
		Step{Action: ActionKeyDown, Key: "Control"},
		Step{Action: ActionKeyDown, Key: "`"},
		Step{Action: ActionKeyDown, Key: "`"},
		Step{Action: ActionKeyUp, Key: "`"},
		Step{Action: ActionKeyDown, Key: "`"},
		Step{Action: ActionEval, Expression: "repeats.join(' ')", Expect: ptr("Control:true:false `:true:false `:true:true `:true:false")},
		Step{Action: ActionKeyUp, Key: "`"},
		Step{Action: ActionKeyUp, Key: "Control"},
	)
	// Shift gives a key's shifted character; a capital holds Shift.
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: "keys = []; document.addEventListener('keydown', e => keys.push(e.key + ':' + e.code + ':' + e.ctrlKey + ':' + e.shiftKey))"},
		Step{Action: ActionPressKey, Key: "k", Modifiers: "Control+Shift"},
		Step{Action: ActionPressKey, Key: "p", Modifiers: "Control"},
		Step{Action: ActionPressKey, Key: "P", Modifiers: "Control"},
		Step{Action: ActionPressKey, Key: "/", Modifiers: "Shift"},
		Step{Action: ActionEval, Expression: "keys.filter(k => !/^(Control|Shift):/.test(k)).join(' ')", Expect: ptr("K:KeyK:true:true p:KeyP:true:false P:KeyP:true:true ?:Slash:false:true")},
	)
	// A press_key's modifiers go down before its key and up after it, in
	// reverse, as with Playwright's press("Control+Alt+Enter"); those
	// key_down holds stay held.
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: "keys = []; for (const type of ['keydown', 'keyup']) document.addEventListener(type, e => keys.push(type.slice(3) + ':' + e.key + ':' + e.ctrlKey + e.altKey))"},
		Step{Action: ActionPressKey, Key: "Enter", Modifiers: "Control+Alt"},
		Step{Action: ActionEval, Expression: "keys.join(' ')", Expect: ptr("down:Control:truefalse down:Alt:truetrue down:Enter:truetrue up:Enter:truetrue up:Alt:truefalse up:Control:falsefalse")},
		Step{Action: ActionEval, Expression: "keys = []"},
		Step{Action: ActionKeyDown, Key: "Alt"},
		Step{Action: ActionPressKey, Key: "Enter", Modifiers: "Alt"},
		Step{Action: ActionKeyUp, Key: "Alt"},
		Step{Action: ActionEval, Expression: "keys.join(' ')", Expect: ptr("down:Alt:falsetrue down:Enter:falsetrue up:Enter:falsetrue up:Alt:falsefalse")},
	)
	if err := failSteps(t, br, env, Step{Action: ActionClick, Selector: "#b", Modifiers: "Hyper"}); !strings.Contains(err.Error(), `unknown modifier "Hyper"`) {
		t.Errorf("bad modifier error = %v", err)
	}
}

func TestPlaceholdersAndCalls(t *testing.T) {
	br := newBrowserOrSkip(t)
	var n atomic.Int32
	env := &Env{
		BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body><p id="p">hello</p></body></html>`),
		Vars:    map[string]string{"ID": "p", "WORD": "hello"},
		Calls: map[string]func(context.Context) (string, error){
			"count": func(context.Context) (string, error) { return string(rune('0' + n.Add(1))), nil },
			"boom":  func(context.Context) (string, error) { return "", errors.New("kaboom") },
		},
	}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionAssertText, Selector: "#{{ID}}", Text: "{{WORD}}"},
		Step{Action: ActionEval, Expression: `document.getElementById('{{ID}}').textContent`, Expect: ptr("{{WORD}}")},
		// A call with an expectation polls until it holds.
		Step{Action: ActionCall, Value: "count", Expect: ptr("3")},
		Step{Action: ActionRetry, Steps: []Step{{Action: ActionAssertText, Selector: "#{{ID}}", Text: "{{WORD}}"}}},
	)
	if err := failSteps(t, br, env, Step{Action: ActionAssertText, Selector: "#{{NOPE}}", Text: "x"}); !strings.Contains(err.Error(), "no value for {{NOPE}}") {
		t.Errorf("missing placeholder error = %v", err)
	}
	if err := failSteps(t, br, env, Step{Action: ActionCall, Value: "boom"}); !strings.Contains(err.Error(), "kaboom") {
		t.Errorf("failing call error = %v", err)
	}
	if err := failSteps(t, br, env, Step{Action: ActionCall, Value: "nope"}); !strings.Contains(err.Error(), "have: boom, count") {
		t.Errorf("unknown call error = %v", err)
	}
}

// An eval's save keeps its result as a placeholder for the steps after it,
// so a value in the page can be looked for in a frame.
func TestSave(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{
		BaseURL: serveHTML(t, `<!doctype html><html><head><style>:root { --bg-base: #123456 }</style></head><body>
<iframe srcdoc="<p id='c'>#123456</p>"></iframe>
</body></html>`),
		Vars: map[string]string{"ID": "c"},
	}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: `getComputedStyle(document.documentElement).getPropertyValue('--bg-base').trim()`, Save: "BG"},
		Step{Action: ActionAssertText, Frame: "iframe", Selector: "#{{ID}}", Text: "{{BG}}"},
		// A step in a retry block sees what one before it saved in the
		// same pass.
		Step{Action: ActionRetry, Steps: []Step{
			{Action: ActionEval, Expression: `window.n = (window.n || 0) + 1`, Save: "N"},
			{Action: ActionEval, Expression: `{{N}} >= 2`, Expect: ptr("true"), Timeout: "100ms"},
		}},
		Step{Action: ActionEval, Expression: `{{N}}`, Expect: ptr("2")},
	)
	for _, c := range []struct {
		step Step
		err  string
	}{
		// A failed eval saves nothing.
		{Step{Action: ActionEval, Expression: `'x'`, Expect: ptr("y"), Save: "X"}, `expected "y"`},
		{Step{Action: ActionEval, Expression: `'{{X}}'`}, "no value for {{X}}"},
		// A retry block fails at once on a placeholder with no value.
		{Step{Action: ActionRetry, Steps: []Step{{Action: ActionEval, Expression: `'{{X}}'`}}}, "(retry): its step 0 (eval) still fails: no value for {{X}}"},
		{Step{Action: ActionEval, Expression: `1`, Save: "ID"}, "save: {{ID}} is one of the test's placeholders; pick another name"},
		{Step{Action: ActionEval, Expression: `1`, Save: "a-b"}, "save takes a name of letters, digits and _"},
		{Step{Action: ActionClick, Selector: "iframe", Save: "X"}, "save keeps an eval's result"},
	} {
		c.step.Timeout = "300ms"
		if err := failSteps(t, br, env, c.step); !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: got %v, want %q", StepSummary(c.step), err, c.err)
		}
	}
}

// A retry block starts its steps over until a whole pass succeeds, as
// Playwright's expect.poll and toPass do.
func TestRetry(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<button id="copy">Copy</button><textarea id="t"></textarea>
<script>window.n = 0; document.getElementById('copy').onclick = () => document.getElementById('t').value = ++n >= 3 ? 'output' : '';</script>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionRetry, Steps: []Step{
			{Action: ActionClick, Selector: "#copy"},
			{Action: ActionAssertValue, Selector: "#t", Value: "output", Timeout: "100ms"},
		}},
		Step{Action: ActionEval, Expression: "n", Expect: ptr("3")},
	)
	// It fails at its timeout with the error of the step that failed last;
	// waits inside without a timeout of their own wait 1s.
	start := time.Now()
	err := failSteps(t, br, env, Step{Action: ActionRetry, Timeout: "2s", Steps: []Step{
		{Action: ActionClick, Selector: "#copy"},
		{Action: ActionAssertValue, Selector: "#t", Value: "never"},
	}})
	if want := `step 0 (retry): after 2s, its step 1 (assert_value) still fails: timeout after 1s: assert_value: expected "never", got "output"`; err.Error() != want {
		t.Errorf("failed retry = %v, want %s", err, want)
	}
	if d := time.Since(start); d < 2*time.Second || d > 4*time.Second {
		t.Errorf("failed retry took %s", d)
	}
	if err := failSteps(t, br, env, Step{Action: ActionRetry, Steps: []Step{{Action: ActionRetry, Steps: []Step{{Action: ActionClick, Selector: "#copy"}}}}}); !strings.Contains(err.Error(), "retry: a retry block cannot hold another") {
		t.Errorf("nested retry = %v", err)
	}
	// Other steps keep their own default: this click waits past the
	// block's timeout for its button.
	runSteps(t, br, env,
		Step{Action: ActionEval, Expression: `setTimeout(() => document.body.insertAdjacentHTML('beforeend', '<button id="late">Late</button>'), 1500); 'later'`},
		Step{Action: ActionRetry, Timeout: "500ms", Steps: []Step{{Action: ActionClick, Selector: "#late"}}},
	)
	// A mistake fails it at once, rather than retrying until the block's
	// timeout; the bound leaves a loaded host plenty of room.
	for _, s := range []Step{{Action: "clik", Selector: "#copy"}, {Action: ActionClick, Role: "buton"}, {Action: ActionClick, Tab: "B"}} {
		start := time.Now()
		err := failSteps(t, br, env, Step{Action: ActionRetry, Steps: []Step{s}})
		if d := time.Since(start); d > defaultStepTimeout/3 {
			t.Errorf("retry of a mistake took %s: %v", d, err)
		}
	}
}

func TestListAndTextAssertions(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<ul><li> One </li><li>Two
  words</li><li style="display: none">Three</li></ul>
<button>Save</button><button>Cancel</button>
<p id="p">hello world</p>
<pre id="mono">  let&nbsp;&nbsp;x =
 1 </pre>
<script>setTimeout(() => { document.querySelector('ul').insertAdjacentHTML('beforeend', '<li>Four</li>'); document.getElementById('p').textContent = 'hello there'; }, 300);</script>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionAssertTexts, Selector: "li", Texts: []string{"One", "Two words", "Three", "Four"}},
		Step{Action: ActionAssertTexts, Selector: "li", Visible: true, Texts: []string{"One", "Two words", "Four"}},
		Step{Action: ActionAssertTexts, Role: "button", Texts: []string{"Save", "Cancel"}},
		Step{Action: ActionAssertTexts, Selector: "table"},
		Step{Action: ActionAssertTextNotContains, Selector: "#p", Text: "world"},
		Step{Action: ActionAssertTextNotContains, Selector: "#p", Text: "bye"},
		// Every text check collapses whitespace, non-breaking spaces too, on
		// both sides, as Playwright does.
		Step{Action: ActionAssertText, Selector: "#mono", Text: "let x = 1"},
		Step{Action: ActionAssertText, Selector: "#mono", Text: "let x  =  1 "},
		Step{Action: ActionWaitText, Text: "let x = 1"},
		Step{Action: ActionWaitText, Text: " x = "},
		Step{Action: ActionEval, Expression: `document.getElementById('mono').remove()`},
		Step{Action: ActionWaitTextGone, Text: "let x"},
	)
	for _, c := range []struct {
		step Step
		err  string
	}{
		{Step{Action: ActionAssertTexts, Selector: "li", Texts: []string{"One", "Two"}}, `assert_texts: expected ["One","Two"], got ["One","Two words","Three","Four"]`},
		{Step{Action: ActionAssertTexts, Selector: "li", Texts: []string{"Four", "One", "Two words", "Three"}}, `got ["One","Two words","Three","Four"]`},
		{Step{Action: ActionAssertTextNotContains, Selector: "#p", Text: "there"}, `assert_text_not_contains: "there" found in "hello there"`},
		{Step{Action: ActionAssertTextNotContains, Selector: "#q", Text: "there"}, `no element matches #q`},
		{Step{Action: ActionAssertText, Selector: "li", Text: "On e"}, `assert_text: expected "On e", got " One "`},
		// wait_text looks at the whole page, not at an element.
		{Step{Action: ActionWaitText, Selector: "#p", Text: "hello"}, "wait_text looks at the whole page; use assert_text_contains on the element"},
		{Step{Action: ActionWaitTextGone, Role: "button", Text: "x"}, "wait_text_gone looks at the whole page; use assert_text_not_contains on the element"},
	} {
		c.step.Timeout = "300ms"
		if err := failSteps(t, br, env, c.step); !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: got %v, want %q", StepSummary(c.step), err, c.err)
		}
	}
}

func TestViewportAssertions(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body style="margin: 0; height: 5000px">
<div id="top" style="height: 10px"></div>
<div id="half" style="position: absolute; top: calc(100vh - 50px); height: 100px; width: 50px"></div>
<div id="far" style="position: absolute; top: 3000px; height: 10px; width: 10px"></div>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionAssertInViewport, Selector: "#top"},
		Step{Action: ActionAssertNotInViewport, Selector: "#far"},
		Step{Action: ActionAssertInViewport, Selector: "#half"},
		Step{Action: ActionAssertInViewport, Selector: "#half", Ratio: 0.4},
		Step{Action: ActionAssertNotInViewport, Selector: "#half", Ratio: 0.6},
		Step{Action: ActionEval, Expression: `setTimeout(() => scrollTo(0, 2900), 300)`},
		Step{Action: ActionAssertInViewport, Selector: "#far", Ratio: 1},
		Step{Action: ActionAssertNotInViewport, Selector: "#top"},
	)
	for _, c := range []struct {
		step Step
		err  string
	}{
		{Step{Action: ActionAssertInViewport, Selector: "#top"}, `assert_in_viewport: #top is 0% in the viewport`},
		{Step{Action: ActionAssertNotInViewport, Selector: "#far", Ratio: 0.5}, `assert_not_in_viewport: #far is 100% in the viewport`},
		{Step{Action: ActionAssertNotInViewport, Selector: "#none"}, `no element matches #none`},
	} {
		c.step.Timeout = "300ms"
		if err := failSteps(t, br, env, c.step); !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: got %v, want %q", StepSummary(c.step), err, c.err)
		}
	}
}

// A point computed when the step runs aims the pointer without checking
// any element, as Playwright's page.mouse does.
func TestPoint(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, clickPage), Vars: map[string]string{"DX": "10"}}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionClick, Point: "({x: {{DX}}, y: 5})"},
		Step{Action: ActionEval, Expression: `log.join(' ')`, Expect: ptr("mousedown:b:0:1@10,5 mouseup:b:0:1@10,5 click:b:0:1@10,5")},
		// No element needs to be ready there.
		Step{Action: ActionEval, Expression: `log = []; document.getElementById('cover').style.display = 'block'`},
		Step{Action: ActionClick, Point: "new Promise(r => setTimeout(() => r({x: 20, y: 6}), 50))"},
		Step{Action: ActionEval, Expression: `log.at(-1)`, Expect: ptr("click:cover:0:1@20,6")},
		// It is evaluated again until it gives a point in the viewport.
		Step{Action: ActionEval, Expression: `log = []; setTimeout(() => window.p = {x: 30, y: 7}, 300)`},
		Step{Action: ActionHover, Point: "window.p"},
		Step{Action: ActionMouseDown, Point: "({x: 40, y: 8})"},
		Step{Action: ActionMouseUp, Point: "({x: 50, y: 9})", Button: "left"},
		Step{Action: ActionEval, Expression: `log.join(' ')`, Expect: ptr("mousedown:cover:0:1@40,8 mouseup:cover:0:1@50,9 click:cover:0:1@50,9")},
	)
	for _, c := range []struct {
		step Step
		err  string
	}{
		{Step{Action: ActionClick, Point: "({x: -1, y: 5})"}, "click: point -1,5 is outside the 393x727 viewport"},
		{Step{Action: ActionClick, Point: "({x: 1})"}, `click: point is {"x":1}, not {x, y}`},
		{Step{Action: ActionClick, Point: "({x: 1, y: 1})", Selector: "#b"}, "click: point replaces selector, role, x and y"},
	} {
		c.step.Timeout = "300ms"
		if err := failSteps(t, br, env, c.step); !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: got %v, want %q", StepSummary(c.step), err, c.err)
		}
	}
	// A wheel goes where the point is first; a touch's points are offsets
	// from it.
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionEval, Expression: `for (const type of ['wheel', 'touchstart', 'touchmove']) document.addEventListener(type, e => log.push(type + '@' + (e.touches ? [...e.touches].map(t => t.clientX + ',' + t.clientY).join(';') : e.clientX + ',' + e.clientY)), {passive: false})`},
		Step{Action: ActionWheel, Point: "({x: 60, y: 30})", X: ptr(0.0), Y: ptr(10.0)},
		Step{Action: ActionTouch, Value: "start", Point: "({x: 30, y: 30})", Points: "0,0 50,50"},
		Step{Action: ActionTouch, Value: "move", Point: "({x: 40, y: 40})"},
		Step{Action: ActionTouch, Value: "end"},
		Step{Action: ActionEval, Expression: `log.filter(e => !e.startsWith('mouse')).join(' ')`, Expect: ptr("wheel@60,30 touchstart@30,30 touchstart@30,30;80,80 touchmove@40,40;80,80")},
	)
}
