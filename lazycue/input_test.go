package lazycue

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
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
			matchMedia('(pointer: coarse)').matches, matchMedia('(prefers-reduced-motion: reduce)').matches].join(' ')`,
			Expect: "393 727 2.75 true 5 true true",
		},
		Step{Action: ActionEval, Expression: `navigator.clipboard.writeText('copied').then(() => navigator.clipboard.readText())`, Expect: "copied"},
	)
}

// set_viewport and emulate_media change a tab for the rest of the script;
// the next script starts from the launch settings again.
func TestEmulationResetsBetweenScripts(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, blankPage)}
	runSteps(t, br, env,
		Step{Action: ActionSetViewport, Width: 1280, Height: 800, Scale: 1, Mobile: ptr(false), Touch: ptr(false)},
		Step{Action: ActionEmulateMedia, Value: "prefers-color-scheme: dark"},
		Step{Action: ActionEmulateMedia, Value: "prefers-reduced-motion:"},
		Step{Action: ActionNavigate, URL: "/"},
		Step{
			Action: ActionEval, Expression: `[innerWidth, innerHeight, devicePixelRatio, 'ontouchstart' in window,
			matchMedia('(prefers-color-scheme: dark)').matches, matchMedia('(prefers-reduced-motion: reduce)').matches].join(' ')`,
			Expect: "1280 800 1 false true false",
		},
	)
	runSteps(t, br, env,
		Step{Action: ActionNavigate, URL: "/"},
		Step{
			Action: ActionEval, Expression: `[innerWidth, 'ontouchstart' in window, matchMedia('(prefers-color-scheme: dark)').matches,
			matchMedia('(prefers-reduced-motion: reduce)').matches].join(' ')`,
			Expect: "393 true false true",
		},
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
			matchMedia('(prefers-reduced-motion: reduce)').matches, Intl.DateTimeFormat().resolvedOptions().timeZone].join(' ')`,
			Expect: "1024 false true false America/New_York",
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
		Step{Action: ActionEval, Expression: `(async () => { await new Promise(r => setTimeout(r, 50)); return 'later'; })()`, Expect: "later"},
		Step{Action: ActionEval, Expression: `fetch('/').then(r => r.status)`, Expect: "200"},
		Step{Action: ActionEval, Expression: `({a: [1, true, null]})`},
	)
	if got := results[3].Output; got != `{"a":[1,true,null]}` {
		t.Errorf("object output = %s", got)
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
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body>
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
		Step{Action: ActionClick, Selector: "button", HasText: "that"},
		Step{Action: ActionAssertTitle, Text: "Archive that"},
	)
	err := failSteps(t, br, env, Step{Action: ActionClick, Selector: "button", HasText: "Delete", Timeout: "300ms"})
	if err == nil || !strings.Contains(err.Error(), `no element matches button with text "Delete"`) {
		t.Fatalf("click on a missing text: %v", err)
	}
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
		Step{Action: ActionEval, Expression: `log.join(' ')`, Expect: "mousedown:b:0:1@10,5 mouseup:b:0:1@10,5 click:b:0:1@10,5"},
		Step{Action: ActionEval, Expression: `log = []`},
		Step{Action: ActionClick, Selector: "#b", Count: 2},
		Step{
			Action: ActionEval, Expression: `log.filter(e => e.startsWith('click') || e.startsWith('dblclick')).map(e => e.split('@')[0]).join(' ')`,
			Expect: "click:b:0:1 click:b:0:2 dblclick:b:0:2",
		},
		Step{Action: ActionEval, Expression: `log = []`},
		Step{Action: ActionClick, Selector: "#b", Button: "right"},
		Step{Action: ActionEval, Expression: `log.map(e => e.split('@')[0]).join(' ')`, Expect: "mousedown:b:2:1 contextmenu:b:2:0 mouseup:b:2:1"},
		Step{Action: ActionEval, Expression: `log = []`},
		// Without a selector, x,y is a point in the viewport.
		Step{Action: ActionClick, X: ptr(50.0), Y: ptr(20.0)},
		Step{Action: ActionEval, Expression: `log.at(-1)`, Expect: "click:b:0:1@50,20"},
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
		Step{Action: ActionEval, Expression: `location.hash + ' ' + log.at(-1).split('@')[0]`, Expect: "#w click:wrap:0:1"},
	)
	// A click waits for the element to be uncovered.
	runSteps(t, br, env,
		Step{Action: ActionEval, Expression: `log = []; document.getElementById('cover').style.display = 'block'; setTimeout(() => document.getElementById('cover').style.display = 'none', 300)`},
		Step{Action: ActionClick, Selector: "#b"},
		Step{Action: ActionEval, Expression: `log.at(-1).split('@')[0]`, Expect: "click:b:0:1"},
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
		Step{Action: ActionEval, Expression: `getSelection().toString()`, Expect: "hello"},
		Step{Action: ActionHover, Selector: "#d"},
		Step{Action: ActionMouseDown},
		Step{Action: ActionHover, Selector: "#t", Count: 5},
		Step{Action: ActionMouseUp},
		Step{Action: ActionEval, Expression: `log.join(' ')`, Expect: "dragstart:d drop:t"},
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
		Step{Action: ActionEval, Expression: `log.join(' ')`, Expect: "down:a drag:50 drag:75 up:z"},
		Step{Action: ActionWheel, X: ptr(0.0), Y: ptr(400.0)},
		Step{Action: ActionEval, Expression: `log.at(-1).split(':')[0] + ' ' + scrollY`, Expect: "wheel 400"},
		// The page scrolls by CSS pixels whatever the device's scale.
		Step{Action: ActionSetViewport, Width: 800, Height: 600, Scale: 2, Mobile: ptr(false), Touch: ptr(false)},
		Step{Action: ActionNavigate, URL: "/"},
		Step{Action: ActionHover, Selector: "#a"},
		Step{Action: ActionWheel, X: ptr(0.0), Y: ptr(400.0)},
		Step{Action: ActionEval, Expression: `scrollY`, Expect: "400"},
	)
}

func TestTouch(t *testing.T) {
	br := newBrowserOrSkip(t)
	env := &Env{BaseURL: serveHTML(t, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body style="margin: 0">
<div id="pad" style="margin: 20px; width: 200px; height: 200px"></div>
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
			Expect: "touchstart:1:30,30 touchmove:1:120,120 touchend:0: touchstart:1:30,30 touchstart:2:30,30;120,120 touchmove:2:40,40;130,130 touchend:1:130,130 touchend:0:",
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
		Step{Action: ActionEval, Expression: `JSON.stringify(document.getElementById('t').value) + ' ' + log.join(' ')`, Expect: `"hi\nyo" h i Enter y o`},
		Step{Action: ActionFill, Selector: "#s", Value: "b"},
		Step{Action: ActionEval, Expression: `document.getElementById('s').value + ' ' + log.at(-1)`, Expect: "b change:s"},
		Step{Action: ActionFill, Selector: "#ce", Value: "new"},
		Step{Action: ActionEval, Expression: `document.getElementById('ce').textContent + ' ' + (document.activeElement.id)`, Expect: "new ce"},
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
		Step{Action: ActionEval, Expression: `log.filter(e => e.startsWith('click::')).join(' ')`, Expect: "click::truefalse"},
		Step{Action: ActionEval, Expression: `log = []`},
		Step{Action: ActionKeyDown, Key: "Alt"},
		Step{Action: ActionHover, Selector: "#b", X: ptr(5.0), Y: ptr(5.0)},
		Step{Action: ActionPressKey, Key: "a"},
		Step{Action: ActionKeyUp, Key: "Alt"},
		Step{Action: ActionPressKey, Key: "b"},
		Step{Action: ActionEval, Expression: `log.filter(e => !e.startsWith('click')).join(' ')`, Expect: "keydown:Alt:falsetrue mousemove::falsetrue keydown:a:falsetrue keyup:a:falsetrue keyup:Alt:falsefalse keydown:b:falsefalse keyup:b:falsefalse"},
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
		Step{Action: ActionEval, Expression: `document.getElementById('{{ID}}').textContent`, Expect: "{{WORD}}"},
		// A call with an expectation polls until it holds.
		Step{Action: ActionCall, Value: "count", Expect: "3"},
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
