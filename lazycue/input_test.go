package lazycue

import (
	"context"
	"encoding/json"
	"errors"
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
// that prefers reduced motion and may use the clipboard.
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
// elements, whose offsetParent is null, are visible too.
func TestVisibility(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
<div id="fixed" style="position: fixed; top: 0; width: 50px; height: 50px"></div>
<div id="empty"></div>
<div id="hidden" style="visibility: hidden; height: 10px"></div>
<div style="display: none"><div id="inside" style="height: 10px"></div></div>
<div id="clear" style="opacity: 0; height: 10px"></div>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionWaitVisible, Selector: "#fixed"},
		Step{Action: ActionAssertVisible, Selector: "#fixed"},
		Step{Action: ActionAssertVisible, Selector: "#clear"},
		Step{Action: ActionAssertNotVisible, Selector: "#empty"},
		Step{Action: ActionAssertNotVisible, Selector: "#hidden"},
		Step{Action: ActionAssertNotVisible, Selector: "#inside"},
		Step{Action: ActionWaitHidden, Selector: "#missing"},
	)
	failSteps(t, br, env, Step{Action: ActionWaitHidden, Selector: "#fixed", Timeout: "300ms"})
}

func TestHasText(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"><title>Inner - app</title><script>window.x = 'Inner'</script></head><body>
<p style="display: none">Hidden note</p>
<div>Outer <div>Inner</div></div>
<ul><li><button>Rename</button></li><li><button title="a">Archive
  this</button></li><li><button>Archive that</button></li></ul>
<script>document.addEventListener('click', e => document.title = e.target.title || e.target.textContent)</script>
</body></html>`)}
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionAssertCount, Selector: "button", HasText: "Archive", Count: 2},
		Step{Action: ActionAssertCount, Selector: "li", HasText: "archive", Count: 0},
		Step{Action: ActionAssertAttribute, Selector: "button", HasText: "Archive this", Attribute: "title", Value: "a"},
		Step{Action: ActionAssertText, Selector: "li", HasText: "that", Text: "Archive that"},
		Step{Action: ActionAssertNotVisible, Selector: "button", HasText: "Delete"},
		Step{Action: ActionAssertNotVisible, Selector: "*", HasText: "Hidden note"},
		Step{Action: ActionAssertText, Selector: "div", HasText: "Inner", Text: "Inner"},
		Step{Action: ActionAssertVisible, Selector: "*", HasText: "Inner"},
		Step{Action: ActionAssertCount, Selector: "*", HasText: "Inner", Count: 1},
		Step{Action: ActionAssertCount, Selector: "*", HasText: "app", Count: 0},
		Step{Action: ActionAssertNotVisible, Selector: "*", HasText: "app"},
		Step{Action: ActionAssertCount, Selector: "*", HasText: "Archive", Count: 2},
		Step{Action: ActionClick, Selector: "button", HasText: "that"},
		Step{Action: ActionAssertTitle, Text: "Archive that"},
	)
	err := failSteps(t, br, env, Step{Action: ActionClick, Selector: "button", HasText: "Delete", Timeout: "300ms"})
	if err == nil || !strings.Contains(err.Error(), `no element matches button with text "Delete"`) {
		t.Fatalf("click on a missing text: %v", err)
	}
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
		Step{Action: ActionEval, Expression: "keys.join(' ')", Expect: ptr("K:KeyK:true:true p:KeyP:true:false P:KeyP:true:true ?:Slash:false:true")},
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
	// steps inside without a timeout of their own wait 1s.
	start := time.Now()
	err := failSteps(t, br, env, Step{Action: ActionRetry, Timeout: "2s", Steps: []Step{
		{Action: ActionClick, Selector: "#copy"},
		{Action: ActionAssertValue, Selector: "#t", Value: "never"},
	}})
	if want := `step 0 (retry): after 2s, its step 1 (assert_value) still fails: assert_value: expected "never", got "output"`; err.Error() != want {
		t.Errorf("failed retry = %v, want %s", err, want)
	}
	if d := time.Since(start); d < 2*time.Second || d > 4*time.Second {
		t.Errorf("failed retry took %s", d)
	}
	if err := failSteps(t, br, env, Step{Action: ActionRetry, Steps: []Step{{Action: ActionRetry, Steps: []Step{{Action: ActionClick, Selector: "#copy"}}}}}); !strings.Contains(err.Error(), "retry: a retry block cannot hold another") {
		t.Errorf("nested retry = %v", err)
	}
}
