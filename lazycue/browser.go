package lazycue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/heapprofiler"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// defaultStepTimeout is the polling ceiling for steps that don't set an
// explicit timeout. All wait_*/assert_* steps (and eval-with-expect) poll up to
// this long for their condition to hold before failing, so UI that is merely
// slow to settle under heavy CI load doesn't spuriously fail a step (which
// would trigger a costly LLM heal). The happy path returns as soon as the
// condition holds, so a generous ceiling costs nothing when things are fast.
const defaultStepTimeout = 15 * time.Second

// Browser wraps a headless Chrome instance via chromedp.
type Browser struct {
	allocCancel context.CancelFunc
	ctxCancel   context.CancelFunc
	ctx         context.Context
	control     *control
	// first is the state of the unnamed tab the browser started with, whose
	// context is ctx.
	first     *tabState
	launch    Launch
	closeOnce sync.Once

	// tabs holds the other tabs, by name: those steps opened and the popups
	// pages opened. changed is closed and replaced when it changes.
	mu      sync.Mutex
	tabs    map[string]*tab
	changed chan struct{}

	// screenshotSink, when non-nil, is invoked after every executed step with
	// the step index and a PNG screenshot of the page. Used to capture a
	// visual trace of a test run. Errors capturing screenshots are ignored.
	screenshotSink func(stepIndex int, action string, png []byte)
}

// SetScreenshotSink installs a callback invoked after each executed step with
// a PNG screenshot. Pass nil to disable. Used to produce a visual trace.
func (b *Browser) SetScreenshotSink(sink func(stepIndex int, action string, png []byte)) {
	b.screenshotSink = sink
}

type tab struct {
	id target.ID
	// ready is closed once ctx and state, or err, are set.
	ready  chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	state  *tabState
	err    error
}

// Device is the screen a browser emulates.
type Device struct {
	Width, Height int
	Scale         float64
	Mobile, Touch bool
}

// Pixel5 is the device a browser emulates unless told otherwise: Playwright's
// "Pixel 5", a phone with a touch screen, minus its browser chrome.
var Pixel5 = Device{Width: 393, Height: 727, Scale: 2.75, Mobile: true, Touch: true}

// DefaultMedia is the CSS media a browser emulates unless told otherwise.
// Reduced motion keeps animations from slowing down or racing the steps.
var DefaultMedia = map[string]string{"prefers-reduced-motion": "reduce"}

// Launch is how a browser's tabs start out. Tests set it in Go rather than
// in steps so that fixtures (a mock script, say) never pass through the
// model.
type Launch struct {
	// InitScript runs before every page's own scripts when non-empty.
	InitScript string
	// Device is the screen to emulate; nil is Pixel5.
	Device *Device
	// Media overrides CSS media features, over DefaultMedia; an empty value
	// drops a default.
	Media map[string]string
	// Timezone is an IANA time zone ID; empty is the host's.
	Timezone string
	// Hosts answers the requests of every tab to other origins, such as
	// "https://docs.example", in place of the network: by origin, the
	// handler to run on each. As with Playwright's routes, CORS preflights
	// are answered without the handler, and other origins may read a
	// response unless it names one in Access-Control-Allow-Origin. A page gets
	// a response once its handler returns. Chrome's local network access
	// checks stay on, as with Playwright, so these pages cannot load
	// anything from an app on 127.0.0.1.
	Hosts map[string]http.Handler
}

func (l Launch) device() Device {
	if l.Device == nil {
		return Pixel5
	}
	return *l.Device
}

func (l Launch) media() map[string]string {
	m := maps.Clone(DefaultMedia)
	for k, v := range l.Media {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return m
}

// tabState is what lazycue keeps of a tab: what steps changed about it and
// what Chrome does not report.
type tabState struct {
	// session is the tab's session on the control connection, which holds
	// its emulation.
	session session
	device  Device
	media   map[string]string
	// The mouse pointer's position, its pressed buttons (an
	// input.DispatchMouseEventParams Buttons mask) and the one pressed last,
	// and the modifier keys key_down holds, which Chrome does not report.
	mouseX, mouseY float64
	buttons        int64
	button         input.MouseButton
	keys           input.Modifier
	// The codes of the keys key_down holds.
	held map[string]bool
	// The fingers on the touch screen, by ID - 1.
	touches []*input.TouchPoint
}

// NewBrowser launches a headless Chrome instance whose tabs start out as
// launch says and may use the clipboard.
func NewBrowser(parentCtx context.Context, launch Launch) (*Browser, error) {
	for origin := range launch.Hosts {
		if u, err := url.Parse(origin); err != nil || u.Scheme+"://"+u.Host != origin {
			return nil, fmt.Errorf("Launch.Hosts: %q is not an origin like https://docs.example", origin)
		}
	}
	d := launch.device()
	devtools := make(chan string, 1)
	opts := append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		// A mouse, as Playwright's headless Chrome has: without it a page
		// without touch emulation matches neither (hover: hover) nor
		// (pointer: fine). Touch emulation still makes it a phone's.
		chromedp.Flag("blink-settings", "primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4"),
		chromedp.WindowSize(d.Width, d.Height),
		chromedp.CombinedOutput(devtoolsURL(devtools)),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(parentCtx, opts...)
	ctx, ctxCancel := chromedp.NewContext(allocCtx)
	b := &Browser{
		allocCancel: allocCancel,
		ctxCancel:   ctxCancel,
		ctx:         ctx,
		launch:      launch,
		tabs:        map[string]*tab{},
		changed:     make(chan struct{}),
	}
	err := chromedp.Run(ctx, grantClipboard())
	if err == nil {
		b.control, err = dialControl(ctx, <-devtools, launch, b.popup, b.closed)
	}
	if err == nil {
		setupCtx, cancel := context.WithTimeout(parentCtx, defaultStepTimeout)
		b.first, err = b.control.page(setupCtx, chromedp.FromContext(ctx).Target.TargetID)
		cancel()
		if err != nil {
			err = fmt.Errorf("set up the first tab: %w", err)
		}
	}
	if err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

// grantClipboard lets every page read and write the clipboard without asking,
// as tests of copy and paste need.
func grantClipboard() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		bctx := cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser)
		// Plain clipboard-write is what navigator.clipboard.writeText
		// needs; the unsanitized grants cover reading and other formats.
		for _, desc := range []*browser.PermissionDescriptor{
			{Name: "clipboard-read", AllowWithoutSanitization: true},
			{Name: "clipboard-write", AllowWithoutSanitization: true},
			{Name: "clipboard-write"},
		} {
			if err := browser.SetPermission(desc, browser.PermissionSettingGranted).Do(bctx); err != nil {
				return fmt.Errorf("grant %s: %w", desc.Name, err)
			}
		}
		return nil
	})
}

// setUpTab makes a new tab start out as launch says. All of it is per tab in
// Chrome, so a tab opened later needs it as much as the first.
//
// ctx's executor is a batch, which names a failed command itself.
func setUpTab(ctx context.Context, launch Launch, st *tabState) error {
	// For the dialogs control answers.
	if err := page.Enable().Do(ctx); err != nil {
		return err
	}
	if len(launch.Hosts) > 0 {
		// For the requests control answers. chromedp turns site isolation
		// off, so this covers cross-site iframes too: they are the page's.
		var patterns []*fetch.RequestPattern
		for origin := range launch.Hosts {
			patterns = append(patterns, &fetch.RequestPattern{URLPattern: origin + "/*"})
		}
		if err := fetch.Enable().WithPatterns(patterns).Do(ctx); err != nil {
			return err
		}
	}
	if launch.Timezone != "" {
		if err := emulation.SetTimezoneOverride(launch.Timezone).Do(ctx); err != nil {
			return err
		}
	}
	// Before any of the app's: keep the browser's own
	// requestAnimationFrame, so steps can wait on frames even when the
	// page fakes its clock.
	if _, err := page.AddScriptToEvaluateOnNewDocument(`Object.defineProperty(window, '__lazycueRAF', {value: requestAnimationFrame.bind(window)})`).Do(ctx); err != nil {
		return err
	}
	if launch.InitScript != "" {
		if _, err := page.AddScriptToEvaluateOnNewDocument(launch.InitScript).Do(ctx); err != nil {
			return err
		}
	}
	st.media = launch.media()
	if err := setDevice(ctx, st, launch.device()); err != nil {
		return err
	}
	return applyMedia(ctx, st)
}

// setDevice makes a tab emulate d.
func setDevice(ctx context.Context, st *tabState, d Device) error {
	if err := emulation.SetDeviceMetricsOverride(int64(d.Width), int64(d.Height), d.Scale, d.Mobile).Do(ctx); err != nil {
		return fmt.Errorf("set viewport: %w", err)
	}
	touch := emulation.SetTouchEmulationEnabled(d.Touch)
	if d.Touch {
		touch = touch.WithMaxTouchPoints(5)
	}
	if err := touch.Do(ctx); err != nil {
		return fmt.Errorf("set touch emulation: %w", err)
	}
	st.device = d
	return nil
}

// applyMedia emulates a tab's CSS media features.
func applyMedia(ctx context.Context, st *tabState) error {
	var features []*emulation.MediaFeature
	for _, name := range slices.Sorted(maps.Keys(st.media)) {
		features = append(features, &emulation.MediaFeature{Name: name, Value: st.media[name]})
	}
	if err := emulation.SetEmulatedMedia().WithFeatures(features).Do(ctx); err != nil {
		return fmt.Errorf("emulate media: %w", err)
	}
	return nil
}

// placeholder matches a {{NAME}} placeholder.
var placeholder = regexp.MustCompile(`\{\{([A-Za-z0-9_]+)\}\}`)

// substitute replaces a step's {{NAME}} placeholders, and those of the steps
// it holds, with their values in vars. A placeholder vars has no value for is
// an error rather than text to type or look for.
func substitute(step Step, vars map[string]string) (Step, error) {
	if step.Steps != nil {
		steps := make([]Step, len(step.Steps))
		for i, s := range step.Steps {
			var err error
			if steps[i], err = substitute(s, vars); err != nil {
				return step, err
			}
		}
		step.Steps = steps
	}
	var missing []string
	if step.Expect != nil {
		expect := *step.Expect
		step.Expect = &expect
	}
	if step.Within != nil {
		within := *step.Within
		step.Within = &within
	}
	step.Texts = slices.Clone(step.Texts)
	for _, f := range stringFields(&step) {
		*f = placeholder.ReplaceAllStringFunc(*f, func(m string) string {
			name := m[2 : len(m)-2]
			v, ok := vars[name]
			if !ok {
				missing = append(missing, m)
			}
			return v
		})
	}
	if missing != nil {
		return step, fmt.Errorf("no value for %s", strings.Join(missing, ", "))
	}
	return step, nil
}

// stringFields lists a step's string fields, where placeholders may be.
func stringFields(s *Step) []*string {
	fields := []*string{
		&s.URL, &s.Selector, &s.Role, &s.Name, &s.HasText, &s.Value, &s.Text,
		&s.Expression, &s.Attribute, &s.Key, &s.Points, &s.Point, &s.Frame,
	}
	if s.Expect != nil {
		fields = append(fields, s.Expect)
	}
	if w := s.Within; w != nil {
		fields = append(fields, &w.Selector, &w.Role, &w.Name, &w.HasText)
	}
	for i := range s.Texts {
		fields = append(fields, &s.Texts[i])
	}
	return fields
}

// hasScheme matches a URL that starts with a scheme, which makes it absolute.
var hasScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// popupName matches the names of popups' tabs: popup, popup2, ...
var popupName = regexp.MustCompile(`^popup\d*$`)

// tab returns the chromedp context and state of the tab a step acts on. Only
// navigate opens a tab, so any other step naming an unopened tab is a script
// mistake and fails at once, except for a popup's, which waits for a page to
// open it.
func (b *Browser) tab(step Step) (context.Context, *tabState, error) {
	if step.Tab == "" {
		return b.ctx, b.first, nil
	}
	timeout := parseTimeout(step.Timeout, defaultStepTimeout)
	deadline := time.After(timeout)
	for {
		b.mu.Lock()
		t, ok := b.tabs[step.Tab]
		changed := b.changed
		b.mu.Unlock()
		if ok {
			select {
			case <-t.ready:
				return t.ctx, t.state, t.err
			case <-deadline:
				return nil, nil, fmt.Errorf("tab %q is not ready after %s", step.Tab, timeout)
			}
		}
		if !popupName.MatchString(step.Tab) {
			if step.Action == ActionNavigate {
				return b.openTab(step.Tab, timeout)
			}
			return nil, nil, mistake{fmt.Errorf("tab %q is not open; open it with a navigate step", step.Tab)}
		}
		select {
		case <-changed:
		case <-deadline:
			return nil, nil, fmt.Errorf("no page opened a popup for tab %q in %s", step.Tab, timeout)
		}
	}
}

// openTabCtx returns the chromedp context of an open tab, without waiting.
func (b *Browser) openTabCtx(name string) (context.Context, error) {
	if name == "" {
		return b.ctx, nil
	}
	b.mu.Lock()
	t, ok := b.tabs[name]
	b.mu.Unlock()
	if ok {
		select {
		case <-t.ready:
			if t.err == nil {
				return t.ctx, nil
			}
		default:
		}
	}
	return nil, fmt.Errorf("tab %q is not open", name)
}

// changeTabs runs f on the tabs and wakes the steps waiting for one.
func (b *Browser) changeTabs(f func(tabs map[string]*tab)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f(b.tabs)
	close(b.changed)
	b.changed = make(chan struct{})
}

// openTab opens another tab of the same browser profile, which shares
// localStorage, cookies and IndexedDB with the others the way a user's second
// tab does. It asks for its own window rather than a tab in the first one:
// full Chrome treats the inactive tab of a window as hidden and stops painting
// it, which no side-by-side user sees (headless-shell has no windows and
// ignores the request; its tabs all stay visible).
//
// timeout bounds the browser round trips, as the opening step's own timeout:
// the tab contexts have no deadline, and a stalled browser must fail the step
// rather than consume the whole-test budget.
func (b *Browser) openTab(name string, timeout time.Duration) (context.Context, *tabState, error) {
	openCtx, cancelOpen := context.WithTimeout(b.ctx, timeout)
	defer cancelOpen()
	var id target.ID
	if err := chromedp.Run(openCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		id, err = target.CreateTarget("about:blank").
			WithNewWindow(true).
			WithWidth(int64(b.launch.device().Width)).
			WithHeight(int64(b.launch.device().Height)).
			Do(cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser))
		return err
	})); err != nil {
		return nil, nil, fmt.Errorf("open tab %q: %w", name, err)
	}
	st, err := b.control.page(openCtx, id)
	var ctx context.Context
	var cancel context.CancelFunc
	if err == nil {
		ctx, cancel, err = b.attach(id, timeout)
	}
	if err != nil {
		// openCtx may have expired by now, so close on a fresh one.
		closeCtx, cancelClose := context.WithTimeout(b.ctx, time.Second)
		defer cancelClose()
		_ = chromedp.Run(closeCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			return target.CloseTarget(id).Do(cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser))
		}))
		return nil, nil, fmt.Errorf("open tab %q: %w", name, err)
	}
	t := &tab{id: id, ready: make(chan struct{}), ctx: ctx, cancel: cancel, state: st}
	close(t.ready)
	b.changeTabs(func(tabs map[string]*tab) { tabs[name] = t })
	return ctx, st, nil
}

// attach gives chromedp a context on page id, which control has set up.
func (b *Browser) attach(id target.ID, timeout time.Duration) (context.Context, context.CancelFunc, error) {
	ctx, cancel := chromedp.NewContext(b.ctx, chromedp.WithTargetID(id))
	// The first Run on a context attaches its target for as long as that Run's
	// context lives, so it must be ctx itself, not a bounded child; bound it
	// from outside instead (cancelling ctx unblocks a stalled Run).
	errc := make(chan error, 1)
	go func() { errc <- chromedp.Run(ctx) }()
	var err error
	select {
	case err = <-errc:
	case <-time.After(timeout):
		err = fmt.Errorf("timeout after %s", timeout)
	}
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return ctx, cancel, nil
}

// popup gives the tab of a page a page opened, in the order they open, the
// first free name of popup, popup2, ..., and attaches to it once control has
// set it up.
func (b *Browser) popup(id target.ID) {
	t := &tab{id: id, ready: make(chan struct{})}
	name := "popup"
	b.changeTabs(func(tabs map[string]*tab) {
		for n := 2; tabs[name] != nil; n++ {
			name = fmt.Sprintf("popup%d", n)
		}
		tabs[name] = t
	})
	go func() {
		defer close(t.ready)
		ctx, cancel := context.WithTimeout(b.ctx, defaultStepTimeout)
		defer cancel()
		t.state, t.err = b.control.page(ctx, id)
		if t.err == nil {
			t.ctx, t.cancel, t.err = b.attach(id, defaultStepTimeout)
		}
		if t.err != nil {
			t.err = fmt.Errorf("set up tab %q: %w", name, t.err)
		}
	}()
}

// closed forgets the tab of a page that is gone, such as a popup that closed
// itself.
func (b *Browser) closed(id target.ID) {
	b.changeTabs(func(tabs map[string]*tab) {
		for name, t := range tabs {
			if t.id == id {
				delete(tabs, name)
				go func() {
					<-t.ready
					if t.cancel != nil {
						t.cancel()
					}
				}()
			}
		}
	})
}

// closeTab closes a tab opened by a step or a page, as a user closing it
// would.
func (b *Browser) closeTab(step Step) error {
	if step.Tab == "" {
		return errors.New("close_tab needs a tab name; the tab the browser started with stays open")
	}
	if _, _, err := b.tab(step); err != nil {
		return err
	}
	var t *tab
	b.changeTabs(func(tabs map[string]*tab) {
		t = tabs[step.Tab]
		delete(tabs, step.Tab)
	})
	if t == nil {
		return nil // the page closed itself meanwhile
	}
	err := chromedp.Cancel(t.ctx)
	t.cancel()
	return err
}

// Close shuts down the browser and waits for the Chrome process to exit.
//
// We use chromedp.Cancel (which closes the browser gracefully and blocks until
// the process is gone) rather than just cancelling the contexts, so that two
// Chrome instances never run concurrently during the handoff between one test's
// browser tearing down and the next test's browser launching. On a small VM
// that overlap causes CPU contention that can slow a freshly-launched browser's
// first paint / first SSE frame enough to spuriously trip a wait step.
func (b *Browser) Close() {
	b.closeOnce.Do(func() {
		if b.ctx != nil {
			// Best-effort graceful close that waits for the process to exit.
			// Bound it so a wedged browser can't hang the suite; fall back to
			// the raw context cancels. chromedp.Cancel can panic ("close of
			// closed channel") if the allocator already tore down, so recover.
			done := make(chan struct{})
			go func() {
				defer func() { _ = recover() }()
				defer close(done)
				_ = chromedp.Cancel(b.ctx)
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
			}
		}
		if b.control != nil {
			b.control.close()
		}
		if b.ctxCancel != nil {
			b.ctxCancel()
		}
		if b.allocCancel != nil {
			b.allocCancel()
		}
	})
}

// Context returns the browser's chromedp context.
func (b *Browser) Context() context.Context {
	return b.ctx
}

// Screenshot captures a PNG screenshot of the named tab ("" is the tab the
// browser started with).
//
// The capture is bounded by a short timeout: it runs after every step
// (including while the predictable agent is mid-turn on a long `delay:`), and a
// busy/unresponsive renderer can otherwise leave CaptureScreenshot blocked
// indefinitely on b.ctx, which has no deadline. A diagnostic screenshot is
// never worth hanging the whole test, so we cap it and let callers ignore the
// error.
func (b *Browser) Screenshot(ctx context.Context, tabName string) ([]byte, error) {
	tabCtx, err := b.openTabCtx(tabName)
	if err != nil {
		return nil, err
	}
	return b.screenshot(tabCtx)
}

func (b *Browser) screenshot(tabCtx context.Context) ([]byte, error) {
	shotCtx, cancel := context.WithTimeout(tabCtx, 5*time.Second)
	defer cancel()
	var buf []byte
	if err := chromedp.Run(shotCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		buf, err = page.CaptureScreenshot().
			WithFormat(page.CaptureScreenshotFormatPng).
			WithCaptureBeyondViewport(false).
			Do(ctx)
		return err
	})); err != nil {
		return nil, err
	}
	return buf, nil
}

// ExecuteSteps runs a sequence of DSL steps against the browser.
// It stops on the first failure and returns results for all attempted steps.
//
// Every script, the agent's included, gets a browser of its own, so none
// can pass because of what another left behind.
func (b *Browser) ExecuteSteps(ctx context.Context, env *Env, steps []Step) ([]StepResult, error) {
	var results []StepResult
	for i, step := range steps {
		start := time.Now()
		output, err := b.executeStep(ctx, env, step)
		dur := time.Since(start)
		sr := StepResult{
			Action:   step.Action,
			Summary:  StepSummary(step),
			Pass:     err == nil,
			Duration: dur,
			Output:   output,
		}
		if err != nil {
			sr.Error = err.Error()
		}
		if b.screenshotSink != nil {
			// Show the tab the step acted on; after close_tab (or a step on
			// a tab that is not open) fall back to the first tab.
			shotCtx, tErr := b.openTabCtx(step.Tab)
			if tErr != nil {
				shotCtx = b.ctx
			}
			if png, sErr := b.screenshot(shotCtx); sErr == nil {
				b.screenshotSink(i, step.Action, png)
			}
		}
		results = append(results, sr)
		if err != nil {
			return results, fmt.Errorf("step %d (%s): %w", i, step.Action, err)
		}
	}
	return results, nil
}

// executeStep runs a single DSL step. It returns an optional diagnostic output
// string (currently only the eval action populates it, with the JS result) and
// an error if the step failed. The eval result is surfaced so the generating
// agent can read the value it probed for instead of flying blind.
func (b *Browser) executeStep(ctx context.Context, env *Env, step Step) (string, error) {
	step, err := substitute(step, env.Vars)
	if err != nil {
		return "", err
	}
	return b.run(ctx, env, step)
}

// run runs a step whose placeholders have been substituted.
func (b *Browser) run(ctx context.Context, env *Env, step Step) (string, error) {
	picks := []Step{step}
	if w := step.Within; w != nil {
		if w.Selector == "" && w.Role == "" {
			return "", mistake{errors.New("within needs a selector or a role")}
		}
		if step.Selector == "" && step.Role == "" {
			return "", mistake{errors.New("within scopes a step's selector or role; set one")}
		}
		picks = append(picks, w.step())
	}
	for _, p := range picks {
		if _, ok := axRoles[p.Role]; p.Role != "" && !ok {
			return "", mistake{fmt.Errorf("unknown role %q; use an ARIA role such as button, link, textbox, checkbox, img or dialog", p.Role)}
		}
		if p.Name != "" && p.Role == "" {
			return "", mistake{errors.New("a name needs a role")}
		}
	}
	if step.Nth != nil && (step.Action == ActionAssertCount || step.Action == ActionAssertTexts) {
		return "", mistake{fmt.Errorf("nth picks one element; %s looks at all the matches", step.Action)}
	}
	if step.Frame != "" && step.Selector == "" && step.Role == "" && step.Action != ActionEval && step.Action != ActionWaitText && step.Action != ActionWaitTextGone {
		return "", mistake{errors.New("a frame scopes a step's selector or role, or an eval, wait_text or wait_text_gone")}
	}
	if step.Action == ActionRetry {
		return "", b.retry(ctx, env, step)
	}
	if step.Action == ActionCall {
		return env.call(ctx, step)
	}
	if step.Action == ActionCloseTab {
		return "", b.closeTab(step)
	}
	tabCtx, st, err := b.tab(step)
	if err != nil {
		return "", err
	}
	if step.Action == ActionEval {
		timeout := parseTimeout(step.Timeout, defaultStepTimeout)
		// Bound evaluation on the tab context, which has no deadline of its
		// own: a wedged renderer would otherwise stall here until the whole-test
		// budget expired instead of failing this step.
		runCtx, cancel := context.WithTimeout(tabCtx, timeout)
		defer cancel()
		return expectResult(ctx, step, timeout, func() (string, error) {
			var result any
			if err := chromedp.Run(runCtx, evalIn(step.Frame, step.Expression, &result)); err != nil {
				return "", err
			}
			return formatResult(result), nil
		})
	}
	return "", b.executeStepErr(ctx, tabCtx, st, env.BaseURL, step)
}

// retryBackoff is how long a retry step waits before each pass after the
// first, as Playwright's toPass: the last wait repeats.
var retryBackoff = []time.Duration{100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second}

// mistake is an error in a script, which no retry can fix.
type mistake struct{ error }

func (m mistake) Unwrap() error { return m.error }

// waits reports whether a step waits for the page to be some way.
func waits(s Step) bool {
	return strings.HasPrefix(s.Action, "wait_") || strings.HasPrefix(s.Action, "assert_") ||
		(s.Action == ActionEval || s.Action == ActionCall) && s.Expect != nil
}

// retry runs a retry step: its steps in order, starting over from the first
// when one fails, until a whole pass succeeds or the step's timeout passes.
// Its waits wait 1s unless they say otherwise, so that the block does the
// waiting; a mistake in a step fails it at once.
func (b *Browser) retry(ctx context.Context, env *Env, step Step) error {
	if len(step.Steps) == 0 {
		return errors.New("retry: no steps")
	}
	if step.Tab != "" {
		return errors.New("retry: set tab on the steps inside, not on the block")
	}
	steps := slices.Clone(step.Steps)
	for i := range steps {
		if steps[i].Action == ActionRetry {
			return errors.New("retry: a retry block cannot hold another")
		}
		if steps[i].Timeout == "" && waits(steps[i]) {
			steps[i].Timeout = "1s"
		}
	}
	timeout := parseTimeout(step.Timeout, defaultStepTimeout)
	deadline := time.Now().Add(timeout)
	for pass := 0; ; pass++ {
		err := func() error {
			for i, s := range steps {
				if _, err := b.run(ctx, env, s); err != nil {
					return fmt.Errorf("its step %d (%s) still fails: %w", i, s.Action, err)
				}
			}
			return nil
		}()
		if err == nil || errors.As(err, new(mistake)) {
			return err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return fmt.Errorf("after %s, %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(retryBackoff[min(pass, len(retryBackoff)-1)], left)):
		}
	}
}

// expectResult runs an eval or call step's probe. Without an expectation the
// step is a one-shot probe: run once and return whatever it yields. With one
// it is an assertion on async state (an <img> whose bytes are still loading,
// tool output still streaming, a file the server has yet to write): like
// wait_visible, poll until the expected value is observed or the timeout
// expires, so a value that is merely SLOW to settle under CI load doesn't
// spuriously fail the step (which would trigger a costly LLM heal). The probe
// must therefore have no side effects.
func expectResult(ctx context.Context, step Step, timeout time.Duration, probe func() (string, error)) (string, error) {
	if step.Expect == nil {
		return probe()
	}
	want := *step.Expect
	deadline := time.Now().Add(timeout)
	// last is the latest result or error, kept over the error of a probe cut
	// short by the deadline, which says nothing about the state.
	var last string
	for {
		got, err := probe()
		switch {
		case err == nil && got == want:
			return got, nil
		case err == nil:
			last = got
		case last == "" || !errors.Is(err, context.DeadlineExceeded):
			// Transient errors (element not present yet) are not fatal
			// while we still have time to poll.
			last = "<error: " + err.Error() + ">"
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("timeout after %s: %s: expected %q, got %q", timeout, step.Action, want, last)
		}
		select {
		case <-ctx.Done():
			return got, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// formatResult renders an eval result for output and comparison with an
// expectation: strings as they are, anything else as JSON, so objects render
// the same every run.
func formatResult(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// evaluate is chromedp.Evaluate for an eval step, in the execution context
// world (the page's when 0): it waits for a promise the expression returns
// to settle, so a step can use await or fetch.
func evaluate(world cdpruntime.ExecutionContextID, expression string, res any) chromedp.Action {
	return chromedp.Evaluate(expression, res, func(p *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams {
		return p.WithAwaitPromise(true).WithContextID(world)
	})
}

// evalIn is evaluate in the document of the iframe the CSS selector frame
// matches, or the page's when frame is empty.
func evalIn(frame, expression string, res any) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		world, err := frameWorld(ctx, frame)
		if err != nil {
			return err
		}
		return evaluate(world, expression, res).Do(ctx)
	})
}

// frameWorld is the execution context to run a step's scripts in: the
// page's (0) when frame is empty, else an isolated world in the document of
// the iframe the CSS selector frame matches. Like Playwright's utility
// world, it sees the document but not the globals of the frame's scripts,
// and it works in a sandboxed frame without scripts too.
func frameWorld(ctx context.Context, frame string) (cdpruntime.ExecutionContextID, error) {
	if frame == "" {
		return 0, nil
	}
	const group = "lazycue-frame"
	defer cdpruntime.ReleaseObjectGroup(group).Do(ctx) //nolint:errcheck // the page may be gone
	el, exc, err := cdpruntime.Evaluate(frameJS(frame)).WithObjectGroup(group).Do(ctx)
	if err != nil {
		return 0, err
	}
	if exc != nil {
		return 0, exc
	}
	if el.ObjectID == "" {
		return 0, fmt.Errorf("no iframe matches %s", frame)
	}
	node, err := dom.DescribeNode().WithObjectID(el.ObjectID).Do(ctx)
	if err != nil {
		return 0, err
	}
	if node.FrameID == "" {
		return 0, fmt.Errorf("%s is a <%s>, not an iframe", frame, strings.ToLower(node.NodeName))
	}
	return page.CreateIsolatedWorld(node.FrameID).WithWorldName("lazycue").Do(ctx)
}

func (b *Browser) executeStepErr(ctx, tabCtx context.Context, st *tabState, baseURL string, step Step) error {
	timeout := parseTimeout(step.Timeout, defaultStepTimeout)

	// runBounded runs actions against the browser context bounded by this step's
	// timeout. Many chromedp actions block until their selector matches (Click
	// waits for a clickable node, Navigate for the load event), and tabCtx has
	// no deadline, so running them directly lets one unsatisfiable step consume
	// the entire per-test budget instead of failing its own step. See
	// TestBlockingStepsHonorTheirTimeout.
	runBounded := func(actions ...chromedp.Action) error {
		runCtx, cancel := context.WithTimeout(tabCtx, timeout)
		defer cancel()
		err := chromedp.Run(runCtx, actions...)
		if err != nil && runCtx.Err() != nil && ctx.Err() == nil {
			// Report the step's own timeout rather than a bare
			// "context deadline exceeded", which reads like an
			// infrastructure failure and hides which step stalled.
			return fmt.Errorf("timeout after %s: %s %s: %w", timeout, step.Action, step.Selector, err)
		}
		return err
	}

	switch step.Action {
	case ActionNavigate:
		url := step.URL
		if url == "" && step.Tab != "" {
			// Another tab opened "on the same page": the first tab's current
			// URL, which a script cannot spell out when the app assigned it
			// (a conversation's /c/<slug>, say).
			locCtx, cancel := context.WithTimeout(b.ctx, timeout)
			defer cancel()
			if err := chromedp.Run(locCtx, chromedp.Location(&url)); err != nil {
				return fmt.Errorf("navigate: read first tab's URL: %w", err)
			}
			if url == "about:blank" {
				return errors.New("navigate: the first tab has not been navigated yet, so there is no page to open this tab on")
			}
		} else if !hasScheme.MatchString(url) {
			url = strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(url, "/")
		}
		return runBounded(chromedp.Navigate(url))

	case ActionReload:
		return runBounded(chromedp.Reload())

	case ActionGoBack, ActionGoForward:
		return b.goHistory(ctx, tabCtx, timeout, step)

	case ActionWaitText, ActionWaitTextGone:
		not := ""
		if step.Action == ActionWaitTextGone {
			not = "!"
		}
		return b.pollJS(ctx, tabCtx, timeout, step.Frame, fmt.Sprintf(
			`%s(n => n(document.body.textContent || '').includes(n(%q)))(s => s.replace(/\s+/g, ' ').trim())`, not, step.Text,
		))

	case ActionFill:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			return evalFind(runCtx, step, fmt.Sprintf(fillJS, findJS(step), describe(step), step.Value), nil)
		})

	case ActionClick, ActionHover:
		button, err := mouseButton(step.Button)
		if err != nil {
			return err
		}
		mods, err := parseKeyModifiers(step.Modifiers)
		if err != nil {
			return err
		}
		return b.atPoint(ctx, tabCtx, st, timeout, step, step.Action == ActionClick, func(ctx context.Context, x, y float64) error {
			if step.Action == ActionHover {
				return moveMouse(ctx, st, x, y, max(step.Count, 1), mods)
			}
			if err := moveMouse(ctx, st, x, y, 1, mods); err != nil {
				return err
			}
			for n := 1; n <= max(step.Count, 1); n++ {
				if err := mouseButtonEvent(ctx, st, input.MousePressed, button, int64(n), mods); err != nil {
					return err
				}
				if err := mouseButtonEvent(ctx, st, input.MouseReleased, button, int64(n), mods); err != nil {
					return err
				}
			}
			return nil
		})

	case ActionMouseDown, ActionMouseUp:
		button, err := mouseButton(step.Button)
		if err != nil {
			return err
		}
		mods, err := parseKeyModifiers(step.Modifiers)
		if err != nil {
			return err
		}
		typ := input.MousePressed
		if step.Action == ActionMouseUp {
			typ = input.MouseReleased
		}
		if step.Point != "" {
			return b.atPoint(ctx, tabCtx, st, timeout, step, false, func(ctx context.Context, x, y float64) error {
				if err := moveMouse(ctx, st, x, y, 1, mods); err != nil {
					return err
				}
				return mouseButtonEvent(ctx, st, typ, button, 1, mods)
			})
		}
		return runBounded(chromedp.ActionFunc(func(ctx context.Context) error {
			return mouseButtonEvent(ctx, st, typ, button, 1, mods)
		}))

	case ActionWheel:
		if step.X == nil || step.Y == nil {
			return errors.New("wheel: set x and y, the distances to scroll")
		}
		mods, err := parseKeyModifiers(step.Modifiers)
		if err != nil {
			return err
		}
		wheel := func(ctx context.Context) error {
			return (&input.DispatchMouseEventParams{
				Type:      input.MouseWheel,
				X:         st.mouseX,
				Y:         st.mouseY,
				Buttons:   st.buttons,
				DeltaX:    *step.X,
				DeltaY:    *step.Y,
				Modifiers: st.keys | mods,
			}).Do(ctx)
		}
		if step.Point == "" {
			return runBounded(chromedp.ActionFunc(wheel))
		}
		x, y, err := b.point(ctx, tabCtx, timeout, step)
		if err != nil {
			return err
		}
		return runBounded(chromedp.ActionFunc(func(ctx context.Context) error {
			if err := moveMouse(ctx, st, x, y, 1, mods); err != nil {
				return err
			}
			return wheel(ctx)
		}))

	case ActionTouch:
		return b.touch(ctx, tabCtx, st, timeout, step)

	case ActionPressKey:
		action, err := pressKey(step.Key, step.Modifiers, st.keys)
		if err != nil {
			return err
		}
		return runBounded(action)

	case ActionKeyDown, ActionKeyUp:
		// A modifier key's own events carry the modifiers held after them,
		// as a keyboard's do.
		held := st.keys | keyModifiers[step.Key]
		if step.Action == ActionKeyUp {
			held = st.keys &^ keyModifiers[step.Key]
		}
		down, up, err := keyEvents(step.Key, held)
		if err != nil {
			return err
		}
		ev := down
		if step.Action == ActionKeyUp {
			ev = up
		}
		// Pressing a key already held repeats it, as holding it would.
		down.AutoRepeat = st.held[down.Code]
		return runBounded(chromedp.ActionFunc(func(ctx context.Context) error {
			if err := ev.Do(ctx); err != nil {
				return err
			}
			st.keys = held
			if st.held == nil {
				st.held = map[string]bool{}
			}
			st.held[down.Code] = step.Action == ActionKeyDown
			return nil
		}))

	case ActionType:
		var actions []chromedp.Action
		for _, r := range step.Value {
			key := string(r)
			if r == '\n' {
				key = "Enter"
			}
			action, err := pressKey(key, "", st.keys)
			if err != nil {
				return fmt.Errorf("type: %w", err)
			}
			actions = append(actions, action)
		}
		return runBounded(actions...)

	case ActionInsertText:
		return runBounded(input.InsertText(step.Value))

	case ActionCollectGarbage:
		return runBounded(heapprofiler.CollectGarbage())

	case ActionSetViewport:
		d := st.device
		if step.Width != 0 {
			d.Width = step.Width
		}
		if step.Height != 0 {
			d.Height = step.Height
		}
		if step.Scale != 0 {
			d.Scale = step.Scale
		}
		if step.Mobile != nil {
			d.Mobile = *step.Mobile
		}
		if step.Touch != nil {
			d.Touch = *step.Touch
		}
		return runBounded(chromedp.ActionFunc(func(ctx context.Context) error {
			return setDevice(cdp.WithExecutor(ctx, st.session), st, d)
		}))

	case ActionEmulateMedia:
		name, value, ok := strings.Cut(step.Value, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" {
			return fmt.Errorf("emulate_media: value %q is not \"feature: value\"", step.Value)
		}
		return runBounded(chromedp.ActionFunc(func(ctx context.Context) error {
			if value == "" {
				delete(st.media, name)
			} else {
				st.media[name] = value
			}
			return applyMedia(cdp.WithExecutor(ctx, st.session), st)
		}))

	case ActionScreenshot:
		// Just take a screenshot, ignore the bytes (used for side effects in agent)
		_, err := b.screenshot(tabCtx)
		return err

	case ActionWaitVisible, ActionAssertVisible, ActionWaitHidden, ActionAssertNotVisible:
		want := step.Action == ActionWaitVisible || step.Action == ActionAssertVisible
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			var visible bool
			if err := evalFind(runCtx, step, visibleJS(step), &visible); err != nil {
				return err
			}
			if visible && !want {
				return fmt.Errorf("%s: %s is visible", step.Action, describe(step))
			}
			if !visible && want {
				return fmt.Errorf("%s: %s is not visible", step.Action, describe(step))
			}
			return nil
		})

	case ActionAssertText:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			got, err := elementString(runCtx, step, "el.textContent")
			if err != nil {
				return fmt.Errorf("assert_text: %w", err)
			}
			if collapse(got) != collapse(step.Text) {
				return fmt.Errorf("assert_text: expected %q, got %q", step.Text, got)
			}
			return nil
		})

	case ActionAssertTextContains, ActionAssertTextNotContains:
		want := step.Action == ActionAssertTextContains
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			got, err := elementString(runCtx, step, "el.textContent")
			if err != nil {
				return fmt.Errorf("%s: %w", step.Action, err)
			}
			got = collapse(got)
			has := strings.Contains(got, collapse(step.Text))
			if has && !want {
				return fmt.Errorf("%s: %q found in %q", step.Action, step.Text, got)
			}
			if !has && want {
				return fmt.Errorf("%s: %q not found in %q", step.Action, step.Text, got)
			}
			return nil
		})

	case ActionAssertTexts:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			var got []string
			if err := evalFind(runCtx, step, findAllJS(step)+`.map(el => el.textContent.replace(/\s+/g, ' ').trim())`, &got); err != nil {
				return fmt.Errorf("assert_texts: %w", err)
			}
			if !slices.Equal(got, step.Texts) {
				return fmt.Errorf("assert_texts: expected %s, got %s", quoteList(step.Texts), quoteList(got))
			}
			return nil
		})

	case ActionAssertInViewport, ActionAssertNotInViewport:
		if step.Ratio < 0 || step.Ratio > 1 {
			return fmt.Errorf("%s: ratio %g is not between 0 and 1", step.Action, step.Ratio)
		}
		want := step.Action == ActionAssertInViewport
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			var ratio float64
			if err := evalFind(runCtx, step, fmt.Sprintf(`(function(el) {
				if (!el) throw new Error(%q);
				return new Promise(resolve => {
					const o = new IntersectionObserver(entries => {
						o.disconnect();
						resolve(entries[0].intersectionRatio);
					});
					o.observe(el);
				});
			})(%s)`, "no element matches "+describe(step), findJS(step)), &ratio); err != nil {
				return fmt.Errorf("%s: %w", step.Action, err)
			}
			in := ratio > 0
			if step.Ratio > 0 {
				in = ratio >= step.Ratio
			}
			if in != want {
				return fmt.Errorf("%s: %s is %s%% in the viewport", step.Action, describe(step), strconv.FormatFloat(math.Round(ratio*1000)/10, 'f', -1, 64))
			}
			return nil
		})

	case ActionAssertAttribute:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			// An absent attribute is not an empty one: an assertion that a
			// boolean attribute such as disabled is "" must not pass when
			// it is missing.
			got, err := elementString(runCtx, step, fmt.Sprintf("el.hasAttribute(%[1]q) ? '=' + el.getAttribute(%[1]q) : 'absent'", step.Attribute))
			if err != nil {
				return fmt.Errorf("assert_attribute: %w", err)
			}
			if got == "absent" {
				return fmt.Errorf("assert_attribute: %s has no %q attribute", describe(step), step.Attribute)
			}
			if got[1:] != step.Value {
				return fmt.Errorf("assert_attribute %q: expected %q, got %q", step.Attribute, step.Value, got[1:])
			}
			return nil
		})

	case ActionAssertValue:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			got, err := elementString(runCtx, step, `(function() {
				if (!(el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement)) throw new Error('a <' + el.tagName.toLowerCase() + '> is not an input, textarea or select');
				return el.value;
			})()`)
			if err != nil {
				return fmt.Errorf("assert_value: %w", err)
			}
			if got != step.Value {
				return fmt.Errorf("assert_value: expected %q, got %q", step.Value, got)
			}
			return nil
		})

	case ActionAssertEnabled, ActionAssertDisabled:
		want := map[string]string{ActionAssertEnabled: "enabled", ActionAssertDisabled: "disabled"}[step.Action]
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			got, err := elementString(runCtx, step, "("+isDisabledJS+")(el) ? 'disabled' : 'enabled'")
			if err != nil {
				return fmt.Errorf("%s: %w", step.Action, err)
			}
			if got != want {
				return fmt.Errorf("%s: %s is %s", step.Action, describe(step), got)
			}
			return nil
		})

	case ActionAssertFocused:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			// As in Playwright, el is focused when it is its root's active
			// element: a custom element counts while focus is inside its
			// shadow root.
			got, err := elementString(runCtx, step, `(function() {
				if (el.getRootNode().activeElement === el) return 'focused';
				let a = document.activeElement;
				while (a && a.shadowRoot && a.shadowRoot.activeElement) a = a.shadowRoot.activeElement;
				return a ? a.tagName.toLowerCase() + (a.id ? '#' + a.id : '') : 'nothing';
			})()`)
			if err != nil {
				return fmt.Errorf("assert_focused: %w", err)
			}
			if got != "focused" {
				return fmt.Errorf("assert_focused: %s is not focused; %s is", describe(step), got)
			}
			return nil
		})

	case ActionWaitURL, ActionAssertURL:
		// An exact value may be the whole URL or just its path, query
		// and hash.
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			var got struct{ Href, Path string }
			if err := chromedp.Run(runCtx, chromedp.Evaluate(`({href: location.href, path: location.pathname + location.search + location.hash})`, &got)); err != nil {
				return err
			}
			if step.Value != "" && got.Href != step.Value && got.Path != step.Value {
				return fmt.Errorf("%s: expected %q, got %q", step.Action, step.Value, got.Href)
			}
			if !strings.Contains(got.Href, step.Text) {
				return fmt.Errorf("%s: %q not found in %q", step.Action, step.Text, got.Href)
			}
			return nil
		})

	case ActionAssertTitle:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			var got string
			if err := chromedp.Run(runCtx, chromedp.Title(&got)); err != nil {
				return err
			}
			if got != step.Text {
				return fmt.Errorf("assert_title: expected %q, got %q", step.Text, got)
			}
			return nil
		})

	case ActionAssertCount:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			var count int
			if err := evalFind(runCtx, step, findAllJS(step)+".length", &count); err != nil {
				return fmt.Errorf("assert_count: %w", err)
			}
			if count != step.Count {
				return fmt.Errorf("assert_count: expected %d of %s, got %d", step.Count, describe(step), count)
			}
			return nil
		})

	case ActionSleep:
		d := parseTimeout(step.Timeout, 1*time.Second)
		time.Sleep(d)
		return nil

	default:
		return mistake{fmt.Errorf("unknown action: %q", step.Action)}
	}
}

// The globals where locate leaves a step's role matches and its within's.
const (
	roleSlot   = "__lazycueRole"
	withinSlot = "__lazycueWithinRole"
)

// findAllJS is an expression for the elements a step picks out: those its
// selector matches, in deepAll's order, or, with a role, those locate found
// (in document order) inside what its selector matches; then those inside
// what its within picks out; then those whose text has has_text (with a
// selector alone, an element that holds another match does not count, as
// with Playwright's getByText); then, with visible, the visible ones; then,
// with nth, the one at that index. Evaluate it with evalFind.
func findAllJS(step Step) string {
	scopes := "null"
	if step.Within != nil {
		scopes = pickJS(step.Within.step(), withinSlot, "null")
	}
	return pickJS(step, roleSlot, scopes)
}

// pickJS is findAllJS for step's own fields, with its role matches in the
// global slot and scopes an expression for the elements to look inside, or
// null.
func pickJS(step Step, slot, scopes string) string {
	nth := "null"
	if step.Nth != nil {
		nth = strconv.Itoa(*step.Nth)
	}
	return fmt.Sprintf(`(function(sel, role, hasText, exact, visible, nth, slot, scopes) {
		`+isVisibleJS+`
		`+deepAllJS+`
		const norm = s => s.replace(/\s+/g, ' ').trim();
		const has = el => exact ? norm(el.textContent) === norm(hasText) : norm(el.textContent).toLowerCase().includes(norm(hasText).toLowerCase());
		// Whether an element is inside one of els, shadow roots included.
		const inside = els => {
			const set = new Set(els);
			return el => {
				for (let p = el.parentNode || el.host; p; p = p.parentNode || p.host) {
					if (set.has(p)) return true;
				}
				return false;
			};
		};
		let all;
		if (role) {
			all = window[slot];
			// Hold the matches no longer than this expression does.
			delete window[slot];
			if (sel) all = all.filter(inside(deepAll(sel)));
		} else {
			all = deepAll(sel);
		}
		if (scopes) all = all.filter(inside(scopes));
		if (hasText !== '' && role) {
			all = all.filter(has);
		} else if (hasText !== '') {
			// Text a user can see: in the body, not in scripts or styles.
			const inBody = el => {
				for (let p = el; p; p = p.parentNode || p.host) {
					if (p === document.body) return true;
				}
				return false;
			};
			const matches = all.filter(el => inBody(el) && !el.closest('script, style, noscript, template') && has(el));
			// Keep the innermost: an element holding a match holds its
			// text too, and "*" would otherwise pick <html>. Shadow hosts
			// hold what their shadow roots do.
			const outer = new Set();
			const set = new Set(matches);
			for (const el of matches) {
				for (let p = el.parentNode; p && !outer.has(p); p = p.parentNode || p.host) {
					if (set.has(p)) outer.add(p);
				}
			}
			all = matches.filter(el => !outer.has(el));
		}
		if (visible) all = all.filter(isVisible);
		if (nth === null) return all;
		const el = all.at(nth);
		return el ? [el] : [];
	})(%q, %t, %q, %t, %t, %s, %q, %s)`, step.Selector, step.Role != "", step.HasText, step.Exact, step.Visible, nth, slot, scopes)
}

// deepAllJS defines deepAll(sel): the elements a CSS selector matches in the
// document and its open shadow roots, in the order of Playwright's CSS
// engine: a root's own matches, then those of the shadow roots of its
// elements in document order, each followed by its nested ones. Each match
// is within one root: "x-picker input" does not reach into x-picker's
// shadow root.
const deepAllJS = `function deepAll(sel) {
	const all = [];
	const query = root => {
		const els = root.querySelectorAll(sel);
		for (let i = 0; i < els.length; i++) all.push(els[i]);
		// A walker does not go into shadow roots, and it is the fastest
		// way through the elements of a large page.
		const w = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT);
		for (let el = w.nextNode(); el; el = w.nextNode()) {
			if (el.shadowRoot) query(el.shadowRoot);
		}
	};
	query(document);
	return all;
}`

// frameJS is an expression for the first element the CSS selector frame
// matches, as deepAll has them, or undefined.
func frameJS(frame string) string {
	return fmt.Sprintf(`(() => {
		`+deepAllJS+`
		return deepAll(%q)[0];
	})()`, frame)
}

// evalFind evaluates expr, which uses findAllJS or findJS, in step's frame
// once locate has found the elements of step's role.
func evalFind(ctx context.Context, step Step, expr string, res any) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		world, err := frameWorld(ctx, step.Frame)
		if err != nil {
			return err
		}
		if err := locate(ctx, step, world); err != nil {
			return err
		}
		return evaluate(world, expr, res).Do(ctx)
	}))
}

// axRoles maps the ARIA roles a step may name to the names Chrome's
// accessibility tree has for them.
var axRoles = func() map[string]string {
	m := map[string]string{"img": "image"}
	for _, r := range strings.Fields(`alert alertdialog application article banner blockquote button caption cell
		checkbox code columnheader combobox complementary contentinfo definition deletion dialog document
		emphasis feed figure form generic grid gridcell group heading insertion link list listbox listitem
		log main marquee math meter menu menubar menuitem menuitemcheckbox menuitemradio navigation note
		option paragraph progressbar radio radiogroup region row rowgroup rowheader scrollbar search
		searchbox separator slider spinbutton status strong subscript superscript switch tab table tablist
		tabpanel term textbox time timer toolbar tooltip tree treegrid treeitem`) {
		m[r] = r
	}
	return m
}()

// locate finds the elements of a step's role and name, and of its within's,
// in the execution context world of its frame, for findAllJS.
func locate(ctx context.Context, step Step, world cdpruntime.ExecutionContextID) error {
	if step.Role == "" && (step.Within == nil || step.Within.Role == "") {
		return nil
	}
	if step.Frame != "" {
		// Chrome stops rendering a cross-origin frame out of view, and its
		// accessibility tree waits for the next rendering.
		var outOfView bool
		if err := evaluate(0, fmt.Sprintf(`(f => !f.contentDocument && (r => r.bottom <= 0 || r.right <= 0 || r.top >= innerHeight || r.left >= innerWidth)(f.getBoundingClientRect()))(%s)`, frameJS(step.Frame)), &outOfView).Do(ctx); err != nil {
			return err
		}
		if outOfView {
			return fmt.Errorf("roles in the cross-origin frame %s cannot be found while it is out of view; scroll to it, or pick by selector", step.Frame)
		}
	}
	if step.Within != nil {
		if err := locateRole(ctx, step.Within.step(), withinSlot, world); err != nil {
			return err
		}
	}
	return locateRole(ctx, step, roleSlot, world)
}

// locateRole finds the elements of a step's role and name, as Chrome
// computes them for its accessibility tree, and puts them into the global
// slot, in the tree's order: document order, shadow DOM included. Elements
// hidden from the tree (display: none, visibility: hidden, aria-hidden,
// inert, behind a modal dialog) have none. Steps without a role need
// nothing. It looks in the document of the execution context world.
func locateRole(ctx context.Context, step Step, slot string, world cdpruntime.ExecutionContextID) error {
	if step.Role == "" {
		return nil
	}
	const group = "lazycue-role"
	defer cdpruntime.ReleaseObjectGroup(group).Do(ctx) //nolint:errcheck // the page may be gone
	doc, exc, err := cdpruntime.Evaluate("document").WithContextID(world).WithObjectGroup(group).Do(ctx)
	if err != nil {
		return lookupErr(ctx, step, err)
	}
	if exc != nil {
		return exc
	}
	nodes, err := accessibility.QueryAXTree().WithObjectID(doc.ObjectID).WithRole(axRoles[step.Role]).Do(ctx)
	if err != nil {
		return lookupErr(ctx, step, err)
	}
	want := collapse(step.Name)
	var ids []cdp.BackendNodeID
	for _, n := range nodes {
		if n.Ignored || n.BackendDOMNodeID == 0 {
			continue
		}
		if want != "" {
			var name string
			if n.Name != nil {
				_ = json.Unmarshal(n.Name.Value, &name) // not a string: no name
			}
			name = collapse(name)
			if step.Exact && name != want || !step.Exact && !strings.Contains(strings.ToLower(name), strings.ToLower(want)) {
				continue
			}
		}
		ids = append(ids, n.BackendDOMNodeID)
	}
	// One round trip each, so all at once: a page may have thousands.
	args := make([]*cdpruntime.CallArgument, len(ids))
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() {
			el, err := dom.ResolveNode().WithBackendNodeID(id).WithExecutionContextID(world).WithObjectGroup(group).Do(ctx)
			if err == nil {
				args[i] = &cdpruntime.CallArgument{ObjectID: el.ObjectID}
			}
			errs[i] = err
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return lookupErr(ctx, step, err)
		}
	}
	_, exc, err = cdpruntime.CallFunctionOn(fmt.Sprintf(`function(...els) {
		window[%q] = els;
	}`, slot)).WithObjectID(doc.ObjectID).WithArguments(args).Do(ctx)
	if err != nil {
		return err
	}
	if exc != nil {
		return exc
	}
	return nil
}

// lookupErr is err from finding step's role, which says so when the step's
// time ran out first.
func lookupErr(ctx context.Context, step Step, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("the %s role lookup did not finish in time", step.Role)
	}
	return fmt.Errorf("find role %s: %w", step.Role, err)
}

// findJS is an expression for the first element findAllJS has, or undefined.
func findJS(step Step) string {
	return findAllJS(step) + "[0]"
}

// elementString evaluates expr, a string-valued expression of el, on the
// element step picks out.
func elementString(ctx context.Context, step Step, expr string) (string, error) {
	var got string
	err := evalFind(ctx, step, fmt.Sprintf(`(function(el) {
		if (!el) throw new Error(%q);
		return %s;
	})(%s)`, "no element matches "+describe(step), expr, findJS(step)), &got)
	return got, err
}

// visibleJS is an expression for whether the element a step picks out is
// visible: rendered with a box of some size and not visibility: hidden, as
// Playwright has it. Opacity does not count.
func visibleJS(step Step) string {
	return fmt.Sprintf(`(function(el) {
		`+isVisibleJS+`
		return !!el && isVisible(el);
	})(%s)`, findJS(step))
}

// isDisabledJS is a function of an element: whether it is disabled, as
// Playwright has it: a disabled form control (or one in a disabled
// fieldset), or inside an element with aria-disabled="true".
const isDisabledJS = `function(el) {
	return !!(el.closest(':disabled') || el.closest('[aria-disabled="true"]'));
}`

// isVisibleJS declares isVisible(el), the test visibleJS describes.
const isVisibleJS = `function isVisible(el) {
	const r = el.getBoundingClientRect();
	return r.width > 0 && r.height > 0 && el.checkVisibility({visibilityProperty: true, checkVisibilityCSS: true});
}`

// fillJS sets the value of an input, textarea, select or contenteditable
// element the way typing would, so frameworks listening for input and change
// events see it. Format with findJS, describe and the value.
const fillJS = `(function(el, sel, value) {
	` + isVisibleJS + `
	if (!el) throw new Error('no element matches ' + sel);
	if (!isVisible(el)) throw new Error(sel + ' is not visible');
	if ((` + isDisabledJS + `)(el)) throw new Error(sel + ' is disabled');
	if (el.readOnly) throw new Error(sel + ' is read-only');
	el.focus();
	if (el.isContentEditable) {
		document.execCommand('selectAll');
		if (value) document.execCommand('insertText', false, value);
		else document.execCommand('delete');
		return;
	}
	const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype
		: el instanceof HTMLSelectElement ? HTMLSelectElement.prototype
		: el instanceof HTMLInputElement ? HTMLInputElement.prototype : null;
	if (!proto) throw new Error(sel + ' is a <' + el.tagName.toLowerCase() + '>, not an input, textarea, select or contenteditable element');
	Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, value);
	if (el instanceof HTMLSelectElement && el.value !== value) throw new Error(sel + ' has no option with value ' + JSON.stringify(value));
	el.dispatchEvent(new Event('input', { bubbles: true }));
	el.dispatchEvent(new Event('change', { bubbles: true }));
})(%s, %q, %q)`

// targetJS finds where a pointer step lands on the element a selector
// matches: dx,dy CSS pixels from its top-left corner, or else the center of
// its first box (an inline element wrapped over two lines has two, and the
// center of their bounding box may be on neither). Like Playwright, it
// first scrolls the element into view and insists that it is visible,
// holding still, enabled (when the step needs that) and not covered by
// another element at that point (a hit anywhere in the button or link el is
// in counts); the step polls until it is. Then it intercepts the step's
// events. Format with findJS, describe, dx, dy, whether to check enabled and
// the events to intercept, a JSON array.
const targetJS = `(async function(el, sel, dx, dy, checkEnabled, events) {
	` + isVisibleJS + `
	` + hitTestJS + `
	` + interceptJS + `
	if (!el) return {err: 'no element matches ' + sel};
	el.scrollIntoViewIfNeeded(true);
	if (!isVisible(el)) return {err: sel + ' is not visible'};
	const r = el.getBoundingClientRect();
	const raf = window.__lazycueRAF || requestAnimationFrame;
	await new Promise(done => raf(() => raf(done)));
	const r2 = el.getBoundingClientRect();
	if (r2.x !== r.x || r2.y !== r.y || r2.width !== r.width || r2.height !== r.height) return {err: sel + ' is moving'};
	if (checkEnabled && (` + isDisabledJS + `)(el)) return {err: sel + ' is disabled'};
	const box = dx === null ? [...el.getClientRects()].find(b => b.width > 0 && b.height > 0) || r : null;
	const x = box ? box.left + box.width / 2 : r.left + dx;
	const y = box ? box.top + box.height / 2 : r.top + dy;
	const target = el.matches('input, textarea, select') || el.isContentEditable ? el : el.closest('button, [role=button], a, [role=link]') || el;
	const hit = hitTest(target, sel, x, y);
	if (!hit.err && events.length) intercept(target, sel, x, y, events);
	return hit;
})(%s, %q, %s, %s, %t, %s)`

// interceptJS defines intercept(el, sel, x, y, types), Playwright's
// hit-target interceptor, which guards against the page changing between
// the checks and the events: the first trusted event of types aimed at x,y
// (a hover's moves on the way are not) must find el there, else it and all
// later events of types are blocked. window.__lazycueHit(stop) says what
// was there instead, or nothing, and with stop removes the interceptor.
const interceptJS = `function intercept(el, sel, x, y, types) {
	if (window.__lazycueHit) window.__lazycueHit(true);
	let miss;
	const check = e => {
		if (!e.isTrusted) return;
		const p = window.TouchEvent && e instanceof TouchEvent ? e.touches[0] : e;
		// A mouse event's coordinates are whole pixels.
		if (miss === undefined && p && Math.abs(p.clientX - x) < 1 && Math.abs(p.clientY - y) < 1) miss = hitTest(el, sel, x, y).err || '';
		if (miss) {
			e.preventDefault();
			e.stopImmediatePropagation();
		}
	};
	for (const t of types) addEventListener(t, check, {capture: true, passive: false});
	window.__lazycueHit = stop => {
		if (stop) {
			for (const t of types) removeEventListener(t, check, {capture: true});
			delete window.__lazycueHit;
		}
		return miss || '';
	};
}`

// hitEvents are the events whose target a pointer step checks as they come,
// as Playwright does: a click's presses, releases and clicks, a hover's
// moves, and the touches of a touch start (a tap's); a touch move has none.
func hitEvents(step Step) []string {
	switch {
	case step.Action == ActionClick:
		return []string{"mousedown", "mouseup", "pointerdown", "pointerup", "click", "auxclick", "dblclick", "contextmenu"}
	case step.Action == ActionHover:
		return []string{"mousemove"}
	case step.Action == ActionTouch && step.Value == "start":
		return []string{"pointerdown", "pointerup", "touchstart", "touchend", "touchcancel"}
	}
	return []string{}
}

// hitTestJS defines hitTest(el, sel, x, y): {x, y} when the element at that
// point in the viewport, shadow DOM included, is el or inside it, else {err}
// naming what is there instead.
const hitTestJS = `function hitTest(el, sel, x, y) {
	let hit = document.elementFromPoint(x, y);
	while (hit && hit.shadowRoot) {
		const inner = hit.shadowRoot.elementFromPoint(x, y);
		if (!inner || inner === hit) break;
		hit = inner;
	}
	if (!hit) return {err: 'point ' + x + ',' + y + ' on ' + sel + ' is outside the viewport'};
	for (let n = hit; n; n = n.parentNode || n.host) {
		if (n === el) return {x, y};
	}
	const desc = hit.tagName.toLowerCase() + (hit.id ? '#' + hit.id : '') + (typeof hit.className === 'string' && hit.className ? '.' + hit.className.trim().split(/\s+/).join('.') : '');
	return {err: sel + ' is covered by ' + desc + ' at ' + x + ',' + y};
}`

// framePointJS maps a point in the viewport of the iframe a selector matches
// to the page's, where the iframe must be what is there.
const framePointJS = `(function(f, sel, x, y) {
	` + hitTestJS + `
	if (!f) return {err: 'no iframe matches ' + sel};
	const r = f.getBoundingClientRect(), s = getComputedStyle(f);
	return hitTest(f, 'frame ' + sel, r.left + f.clientLeft + parseFloat(s.paddingLeft) + x, r.top + f.clientTop + parseFloat(s.paddingTop) + y);
})(%s, %q, %g, %g)`

// atPoint runs f at the point a pointer step aims at: x,y CSS pixels from the
// top-left corner of the element step.Selector matches when both are set,
// else its center, once targetJS finds the element ready there; in a frame,
// that point maps to the page, where the iframe must be what is there. When
// the element is no longer what the step's hitEvents find there, they are
// blocked and the step tries again. Without a selector, x,y is a point in
// the viewport, taken as is.
func (b *Browser) atPoint(ctx, tabCtx context.Context, st *tabState, timeout time.Duration, step Step, checkEnabled bool, f func(ctx context.Context, x, y float64) error) error {
	if (step.X == nil) != (step.Y == nil) {
		return fmt.Errorf("%s: set both x and y, or neither", step.Action)
	}
	// run runs f with time of its own: the element may have become ready
	// just as the step's timeout ran out.
	run := func(f func(ctx context.Context) error) error {
		runCtx, cancel := context.WithTimeout(tabCtx, timeout)
		defer cancel()
		return chromedp.Run(runCtx, chromedp.ActionFunc(f))
	}
	dispatch := func(x, y float64) error {
		return run(func(ctx context.Context) error { return f(ctx, x, y) })
	}
	if step.Point != "" {
		if step.Selector != "" || step.Role != "" || step.X != nil {
			return fmt.Errorf("%s: point replaces selector, role, x and y", step.Action)
		}
		x, y, err := b.point(ctx, tabCtx, timeout, step)
		if err != nil {
			return err
		}
		return dispatch(x, y)
	}
	if step.Selector == "" && step.Role == "" {
		if step.X == nil {
			return fmt.Errorf("%s: set a selector or role, or x and y", step.Action)
		}
		return dispatch(*step.X, *step.Y)
	}
	dx, dy := "null", "null"
	if step.X != nil {
		dx, dy = strconv.FormatFloat(*step.X, 'g', -1, 64), strconv.FormatFloat(*step.Y, 'g', -1, 64)
	}
	events := hitEvents(step)
	eventsJSON, err := json.Marshal(events)
	if err != nil {
		return err
	}
	expr := fmt.Sprintf(targetJS, findJS(step), describe(step), dx, dy, checkEnabled, eventsJSON)
	// landed says what was at the point instead of the element when the
	// events came, if anything, and with stop removes the interceptor
	// targetJS set up. An error (a click may have navigated) means they
	// went through, as with Playwright.
	landed := func(stop bool) string {
		var miss string
		_ = run(evalIn(step.Frame, fmt.Sprintf(`window.__lazycueHit ? window.__lazycueHit(%t) : ''`, stop), &miss).Do)
		return miss
	}
	var target struct {
		X, Y float64
		Err  string
	}
	var sendErr error
	err = b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
		target.Err = ""
		if err := evalFind(runCtx, step, expr, &target); err != nil {
			return err
		}
		if target.Err == "" && step.Frame != "" {
			err := chromedp.Run(runCtx, evaluate(0, fmt.Sprintf(framePointJS, frameJS(step.Frame), step.Frame, target.X, target.Y), &target))
			if err != nil || target.Err != "" {
				landed(true)
			}
			if err != nil {
				return err
			}
		}
		if target.Err != "" {
			return fmt.Errorf("%s: %s", step.Action, target.Err)
		}
		sendErr = dispatch(target.X, target.Y)
		miss := landed(step.Action != ActionTouch)
		if step.Action == ActionTouch {
			if miss != "" && sendErr == nil {
				// Lift the finger, unseen too, before trying again.
				sendErr = run(func(ctx context.Context) error {
					st.touches = nil
					return input.DispatchTouchEvent(input.TouchCancel, []*input.TouchPoint{}).Do(ctx)
				})
			}
			landed(true)
		}
		if miss != "" && sendErr == nil {
			return fmt.Errorf("%s: %s when the events came", step.Action, miss)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return sendErr
}

// point evaluates a step's point until it gives a finite {x, y} inside the
// viewport.
func (b *Browser) point(ctx, tabCtx context.Context, timeout time.Duration, step Step) (x, y float64, err error) {
	expr := fmt.Sprintf(`(async () => {
		const p = await (
%s
		);
		const ok = p !== null && typeof p === 'object' && Number.isFinite(p.x) && Number.isFinite(p.y);
		return {ok, got: ok ? '' : String(JSON.stringify(p)), x: ok ? p.x : 0, y: ok ? p.y : 0, w: innerWidth, h: innerHeight};
	})()`, step.Point)
	var p struct {
		OK   bool
		Got  string
		X, Y float64
		W, H float64
	}
	err = b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
		if err := chromedp.Run(runCtx, evaluate(0, expr, &p)); err != nil {
			return fmt.Errorf("%s: point: %w", step.Action, err)
		}
		if !p.OK {
			return fmt.Errorf("%s: point is %s, not {x, y}", step.Action, p.Got)
		}
		if p.X < 0 || p.X >= p.W || p.Y < 0 || p.Y >= p.H {
			return fmt.Errorf("%s: point %g,%g is outside the %gx%g viewport", step.Action, p.X, p.Y, p.W, p.H)
		}
		return nil
	})
	return p.X, p.Y, err
}

// pollCheck repeatedly runs check until it returns nil or the timeout expires,
// returning check's last error on timeout, after "timeout after <d>: " as
// every wait that runs out says. It is the Go-assertion analogue of
// pollJS: point-in-time assert_* steps use it so UI state that is merely SLOW
// to settle under CI load doesn't spuriously fail (which would trigger a costly
// LLM heal) — the same rationale as the polling eval-with-expect path. The
// assertion is unchanged: it must still hold within the window, and a check
// that never passes (a genuine mismatch, e.g. a forever-spinner regression)
// still fails after the timeout. Polling only tolerates late settling; it never
// turns a currently-passing assert into a failure.
func (b *Browser) pollCheck(ctx, tabCtx context.Context, timeout time.Duration, check func(runCtx context.Context) error) error {
	// One tab context bounded by the whole polling window, handed to each
	// check. Several chromedp actions (TextContent, AttributeValue, Click, …)
	// block until their selector matches, so a check run against the unbounded
	// tab context could hang forever *inside* an iteration and the deadline
	// below would never be consulted. Bounding it here means a stuck check
	// unblocks when the step's own timeout expires.
	runCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()
	deadline := time.Now().Add(timeout)
	var last error
	for {
		err := check(runCtx)
		if err == nil {
			return nil
		}
		// Keep the last error over that of a check cut short by the
		// deadline, which says nothing about the page.
		if last == nil || runCtx.Err() == nil {
			last = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout after %s: %w", timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// pollJS polls a JS expression, in the document of the iframe frame matches
// when set, until it returns true or the timeout expires.
func (b *Browser) pollJS(ctx, tabCtx context.Context, timeout time.Duration, frame, expr string) error {
	runCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()
	deadline := time.Now().Add(timeout)
	interval := 200 * time.Millisecond

	for {
		var result bool
		if err := chromedp.Run(runCtx, evalIn(frame, expr, &result)); err != nil {
			// JS errors during polling are not fatal — element might not exist yet
		} else if result {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timeout after %s waiting for: %s%s", timeout, expr, inFrame(Step{Frame: frame}))
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// goHistory runs a go_back or go_forward step: it goes to the tab's previous
// or next history entry and waits for its document to load. An entry of the
// same document (pushState) fires no load event, so rather than wait for one
// it marks the page and polls until the mark is gone (a new document) or the
// URL changed, and the document is complete.
func (b *Browser) goHistory(ctx, tabCtx context.Context, timeout time.Duration, step Step) error {
	by, which := int64(-1), "earlier"
	if step.Action == ActionGoForward {
		by, which = 1, "later"
	}
	mark := strconv.FormatInt(time.Now().UnixNano(), 36)
	runCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()
	if err := chromedp.Run(runCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		cur, entries, err := page.GetNavigationHistory().Do(ctx)
		if err != nil {
			return err
		}
		to := cur + by
		if to < 0 || to >= int64(len(entries)) {
			return fmt.Errorf("%s: no %s page in the tab's history", step.Action, which)
		}
		// A new document drops the mark; a move within the document, even to
		// an entry with the same URL, pops state.
		if err := chromedp.Evaluate(fmt.Sprintf(`window.__lazycueMark = %[1]q;
			addEventListener('popstate', () => { window.__lazycuePopped = %[1]q }, {once: true})`, mark), nil).Do(ctx); err != nil {
			return err
		}
		return page.NavigateToHistoryEntry(entries[to].ID).Do(ctx)
	})); err != nil {
		return err
	}
	return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
		var loaded bool
		if err := chromedp.Run(runCtx, chromedp.Evaluate(fmt.Sprintf(
			`(window.__lazycueMark !== %[1]q || window.__lazycuePopped === %[1]q) && document.readyState === 'complete'`, mark,
		), &loaded)); err != nil {
			return err
		}
		if !loaded {
			return fmt.Errorf("%s: the page did not load", step.Action)
		}
		return nil
	})
}

// parseTimeout parses a duration string like "10s", "5s", etc.
// Returns the default if the string is empty or unparseable.
func parseTimeout(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// keyNames maps the names a press_key step may use to chromedp key
// definitions: every multi-character KeyboardEvent.key value chromedp knows
// ("Enter", "Tab", "Escape", "ArrowUp", "F5", "Insert", ...) plus "Space" for
// the space bar, whose KeyboardEvent.key is " ". chromedp.KeyEvent types its
// argument rune by rune, so "Enter" passed through verbatim would type the
// letters E, n, t, e, r.
var keyNames = func() map[string]rune {
	names := map[string]rune{"Space": ' '}
	runes := make([]rune, 0, len(kb.Keys))
	for r := range kb.Keys {
		runes = append(runes, r)
	}
	slices.Sort(runes) // deterministic winner when two runes share a name
	for _, r := range runes {
		name := kb.Keys[r].Key
		if _, dup := names[name]; !dup && utf8.RuneCountInString(name) > 1 {
			names[name] = r
		}
	}
	return names
}()

var keyModifiers = map[string]input.Modifier{
	"Shift":   input.ModifierShift,
	"Control": input.ModifierCtrl,
	"Ctrl":    input.ModifierCtrl,
	"Alt":     input.ModifierAlt,
	"Meta":    input.ModifierMeta,
	"Cmd":     input.ModifierMeta,
}

// modifierKeys names the key of each modifier.
var modifierKeys = map[input.Modifier]string{
	input.ModifierShift: "Shift",
	input.ModifierCtrl:  "Control",
	input.ModifierAlt:   "Alt",
	input.ModifierMeta:  "Meta",
}

// parseKeyModifiers parses a "modifiers" field such as "Shift" or
// "Control+Alt".
func parseKeyModifiers(modifiers string) (input.Modifier, error) {
	list, err := keyModifierList(modifiers)
	var mods input.Modifier
	for _, m := range list {
		mods |= m
	}
	return mods, err
}

// keyModifierList is parseKeyModifiers keeping the order of the modifiers.
func keyModifierList(modifiers string) ([]input.Modifier, error) {
	var list []input.Modifier
	for _, name := range strings.FieldsFunc(modifiers, func(r rune) bool { return r == '+' || r == ',' || r == ' ' }) {
		m, ok := keyModifiers[name]
		if !ok {
			return nil, fmt.Errorf("unknown modifier %q (use Shift, Control, Alt or Meta)", name)
		}
		list = append(list, m)
	}
	return list, nil
}

// mouseButtons maps a step's button to the button and its bit in a
// DispatchMouseEvent Buttons mask.
var mouseButtons = map[string]struct {
	button input.MouseButton
	bit    int64
}{
	"":       {input.Left, 1},
	"left":   {input.Left, 1},
	"right":  {input.Right, 2},
	"middle": {input.Middle, 4},
}

func mouseButton(name string) (input.MouseButton, error) {
	b, ok := mouseButtons[name]
	if !ok {
		return "", fmt.Errorf("unknown mouse button %q (use left, right or middle)", name)
	}
	return b.button, nil
}

// moveMouse moves the pointer to x,y in steps moves, as a hand would, with
// the buttons mouse_down pressed still held so that moves drag. Chrome only
// takes a move for a drag when it names a held button, as Playwright's do.
func moveMouse(ctx context.Context, st *tabState, x, y float64, steps int, mods input.Modifier) error {
	fromX, fromY := st.mouseX, st.mouseY
	button := input.None
	if st.buttons != 0 {
		button = st.button
	}
	for i := 1; i <= steps; i++ {
		st.mouseX = fromX + (x-fromX)*float64(i)/float64(steps)
		st.mouseY = fromY + (y-fromY)*float64(i)/float64(steps)
		err := (&input.DispatchMouseEventParams{
			Type:      input.MouseMoved,
			X:         st.mouseX,
			Y:         st.mouseY,
			Button:    button,
			Buttons:   st.buttons,
			Modifiers: st.keys | mods,
		}).Do(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}

// mouseButtonEvent presses or releases button where the pointer is.
func mouseButtonEvent(ctx context.Context, st *tabState, typ input.MouseType, button input.MouseButton, clickCount int64, mods input.Modifier) error {
	var bit int64
	for _, b := range mouseButtons {
		if b.button == button {
			bit = b.bit
		}
	}
	if typ == input.MousePressed {
		st.buttons |= bit
		st.button = button
	} else {
		st.buttons &^= bit
	}
	return (&input.DispatchMouseEventParams{
		Type:       typ,
		X:          st.mouseX,
		Y:          st.mouseY,
		Button:     button,
		Buttons:    st.buttons,
		ClickCount: clickCount,
		Modifiers:  st.keys | mods,
	}).Do(ctx)
}

// touchTypes maps a touch step's value to its event type.
var touchTypes = map[string]input.TouchType{
	"start":  input.TouchStart,
	"move":   input.TouchMove,
	"end":    input.TouchEnd,
	"cancel": input.TouchCancel,
}

// touch runs a touch step: fingers going down (start), moving (move) or
// lifting (end, cancel). Start and move put one finger where a click would
// land, or several at step.Points, offsets from the element's top-left corner
// (or the viewport's without a selector). A move goes there in step.Count
// even steps from where each finger was.
func (b *Browser) touch(ctx, tabCtx context.Context, st *tabState, timeout time.Duration, step Step) error {
	typ, ok := touchTypes[step.Value]
	if !ok {
		return fmt.Errorf("touch: value %q is not start, move, end or cancel", step.Value)
	}
	if typ == input.TouchEnd || typ == input.TouchCancel {
		runCtx, cancel := context.WithTimeout(tabCtx, timeout)
		defer cancel()
		st.touches = nil
		return chromedp.Run(runCtx, input.DispatchTouchEvent(typ, []*input.TouchPoint{}))
	}
	steps := 1
	if typ == input.TouchMove {
		steps = max(step.Count, 1)
	}
	// send moves the fingers from where they were to points.
	send := func(ctx context.Context, points []*input.TouchPoint) error {
		from := st.touches
		for n := 1; n <= steps; n++ {
			f := float64(n) / float64(steps)
			at := make([]*input.TouchPoint, len(points))
			for i, p := range points {
				q := *p
				if i < len(from) {
					q.X = from[i].X + (p.X-from[i].X)*f
					q.Y = from[i].Y + (p.Y-from[i].Y)*f
				}
				at[i] = &q
			}
			if err := input.DispatchTouchEvent(typ, at).Do(ctx); err != nil {
				return err
			}
			st.touches = at
		}
		return nil
	}
	if step.Points == "" {
		return b.atPoint(ctx, tabCtx, st, timeout, step, false, func(ctx context.Context, x, y float64) error {
			return send(ctx, []*input.TouchPoint{{X: x, Y: y, ID: 1}})
		})
	}
	var offsets [][2]float64
	for _, p := range strings.Fields(step.Points) {
		var x, y float64
		if _, err := fmt.Sscanf(p, "%g,%g", &x, &y); err != nil {
			return fmt.Errorf("touch: point %q is not x,y", p)
		}
		offsets = append(offsets, [2]float64{x, y})
	}
	if len(offsets) == 0 {
		return fmt.Errorf("touch: no points in %q", step.Points)
	}
	// Aim at the first finger, then place the rest relative to it; with a
	// point, aim at the point the offsets start from.
	first := step
	var ox, oy float64
	if step.Point == "" {
		first.X, first.Y = &offsets[0][0], &offsets[0][1]
		ox, oy = offsets[0][0], offsets[0][1]
	}
	return b.atPoint(ctx, tabCtx, st, timeout, first, false, func(ctx context.Context, x, y float64) error {
		var points []*input.TouchPoint
		for i, o := range offsets {
			points = append(points, &input.TouchPoint{X: x + o[0] - ox, Y: y + o[1] - oy, ID: float64(i + 1)})
		}
		return send(ctx, points)
	})
}

// pressKey builds the action for a press_key step: a key name from keyNames or
// a single character, dispatched the way a physical keyboard (and Playwright)
// would — one keyDown carrying the key's text, then one keyUp. That matters
// for Enter: chromedp.KeyEvent would add a separate "char" event, which Chrome
// turns into a newline even when the page preventDefault()ed the keydown, so
// an app's Enter handler would fire AND a newline would land in the textarea.
// Control/Alt/Meta chords carry no text, since they don't type. As with
// Playwright's press("Control+Enter"), the modifiers not already held go down
// first, in order, and up last, in reverse.
func pressKey(key, modifiers string, held input.Modifier) (chromedp.Action, error) {
	list, err := keyModifierList(modifiers)
	if err != nil {
		return nil, err
	}
	var downs, ups []*input.DispatchKeyEventParams
	mods := held
	for _, m := range list {
		if mods&m != 0 {
			continue
		}
		mods |= m
		down, _, err := keyEvents(modifierKeys[m], mods)
		if err != nil {
			return nil, err
		}
		downs = append(downs, down)
		_, up, err := keyEvents(modifierKeys[m], mods&^m)
		if err != nil {
			return nil, err
		}
		ups = append([]*input.DispatchKeyEventParams{up}, ups...)
	}
	down, up, err := keyEvents(key, mods)
	if err != nil {
		return nil, err
	}
	events := append(append(append(downs, down), up), ups...)
	return chromedp.ActionFunc(func(ctx context.Context) error {
		for _, ev := range events {
			if err := ev.Do(ctx); err != nil {
				return err
			}
		}
		return nil
	}), nil
}

// shiftedKeys maps a key's code to what it gives with Shift held, for the
// keys whose character Shift changes.
var shiftedKeys = func() map[string]*kb.Key {
	m := map[string]*kb.Key{}
	for _, k := range kb.Keys {
		if k.Shift && k.Code != "" && k.Text != "" {
			m[k.Code] = k
		}
	}
	return m
}()

// keyEvents builds the keyDown and keyUp events of a key, by name from
// keyNames or a single character, pressed with mods held.
func keyEvents(key string, mods input.Modifier) (down, up *input.DispatchKeyEventParams, err error) {
	r, ok := keyNames[key]
	if !ok {
		rs := []rune(key)
		if len(rs) != 1 {
			return nil, nil, fmt.Errorf("unknown key %q (use a KeyboardEvent.key name such as Enter, Tab, Escape, ArrowDown, Shift or F5, or a single character)", key)
		}
		r = rs[0]
	}
	def, ok := kb.Keys[r]
	if !ok {
		// A character with no key of its own, such as an emoji.
		def = &kb.Key{Key: string(r), Text: string(r), Unmodified: string(r)}
	}
	if def.Shift {
		mods |= input.ModifierShift
	} else if shifted, ok := shiftedKeys[def.Code]; ok && mods&input.ModifierShift != 0 {
		// With Shift held a key gives its shifted character, as on a
		// real keyboard and in Playwright: Control+Shift+k is "K".
		def = shifted
	}
	down = &input.DispatchKeyEventParams{
		Type:                  input.KeyDown,
		Key:                   def.Key,
		Code:                  def.Code,
		NativeVirtualKeyCode:  def.Native,
		WindowsVirtualKeyCode: def.Windows,
		Modifiers:             mods,
	}
	if runtime.GOOS == "darwin" {
		down.NativeVirtualKeyCode = 0 // as kb.Encode does
	}
	if mods&(input.ModifierCtrl|input.ModifierAlt|input.ModifierMeta) == 0 {
		down.Text, down.UnmodifiedText = def.Text, def.Unmodified
	}
	up = &input.DispatchKeyEventParams{}
	*up = *down
	up.Type = input.KeyUp
	up.Text, up.UnmodifiedText = "", ""
	return down, up, nil
}
