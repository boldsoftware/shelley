package lazycue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
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
	// first is the state of the unnamed tab the browser started with, whose
	// context is ctx. tabs holds the extra tabs opened by steps naming a tab,
	// by name.
	first     *tabState
	tabs      map[string]tab
	launch    Launch
	closeOnce sync.Once

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
	ctx    context.Context
	cancel context.CancelFunc
	state  *tabState
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

// Launch is how a browser's tabs start out, and what ExecuteSteps puts back
// before each script. Tests set it in Go rather than in steps so that
// fixtures (a mock script, say) never pass through the model.
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

// tabState is what steps change about a tab, which ExecuteSteps puts back.
type tabState struct {
	device Device
	media  map[string]string
	// The mouse pointer's position, its pressed buttons (an
	// input.DispatchMouseEventParams Buttons mask) and the one pressed last,
	// and the modifier keys key_down holds, which Chrome does not report.
	mouseX, mouseY float64
	buttons        int64
	button         input.MouseButton
	keys           input.Modifier
	// The codes of the keys key_down holds.
	held map[string]bool
}

// NewBrowser launches a headless Chrome instance whose tabs start out as
// launch says and may use the clipboard.
func NewBrowser(parentCtx context.Context, launch Launch) (*Browser, error) {
	d := launch.device()
	opts := append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.WindowSize(d.Width, d.Height),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(parentCtx, opts...)
	ctx, ctxCancel := chromedp.NewContext(allocCtx)

	first := &tabState{}
	if err := chromedp.Run(ctx, grantClipboard(), setUpTab(launch, first)); err != nil {
		ctxCancel()
		allocCancel()
		return nil, err
	}

	return &Browser{
		allocCancel: allocCancel,
		ctxCancel:   ctxCancel,
		ctx:         ctx,
		first:       first,
		launch:      launch,
		tabs:        map[string]tab{},
	}, nil
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
func setUpTab(launch Launch, st *tabState) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if launch.Timezone != "" {
			if err := emulation.SetTimezoneOverride(launch.Timezone).Do(ctx); err != nil {
				return fmt.Errorf("set time zone: %w", err)
			}
		}
		// Before any of the app's: keep the browser's own
		// requestAnimationFrame, so steps can wait on frames even when the
		// page fakes its clock.
		if _, err := page.AddScriptToEvaluateOnNewDocument(`Object.defineProperty(window, '__lazycueRAF', {value: requestAnimationFrame.bind(window)})`).Do(ctx); err != nil {
			return fmt.Errorf("install init script: %w", err)
		}
		if launch.InitScript != "" {
			if _, err := page.AddScriptToEvaluateOnNewDocument(launch.InitScript).Do(ctx); err != nil {
				return fmt.Errorf("install init script: %w", err)
			}
		}
		// Close dialogs as Playwright does when no test listens for them:
		// leave the page on beforeunload, cancel alerts, confirms and
		// prompts. Otherwise the page hangs on them.
		chromedp.ListenTarget(ctx, func(ev any) {
			if e, ok := ev.(*page.EventJavascriptDialogOpening); ok {
				go page.HandleJavaScriptDialog(e.Type == page.DialogTypeBeforeunload).Do(ctx) //nolint:errcheck // the page may be gone
			}
		})
		*st = tabState{media: launch.media()}
		if err := setDevice(ctx, st, launch.device()); err != nil {
			return err
		}
		return applyMedia(ctx, st)
	})
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

// substitute replaces a step's {{NAME}} placeholders with their values in
// vars. A placeholder vars has no value for is an error rather than text to
// type or look for.
func substitute(step Step, vars map[string]string) (Step, error) {
	var missing []string
	fields := []*string{
		&step.URL, &step.Selector, &step.HasText, &step.Value, &step.Text,
		&step.Expression, &step.Attribute, &step.Key, &step.Points,
	}
	if step.Expect != nil {
		expect := *step.Expect
		step.Expect = &expect
		fields = append(fields, step.Expect)
	}
	for _, f := range fields {
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

// tabCtx returns the chromedp context of the tab a step acts on. Only navigate
// opens a tab, so any other step naming an unopened tab is a script mistake
// and fails at once.
func (b *Browser) tabCtx(step Step) (context.Context, error) {
	ctx, _, err := b.tab(step)
	return ctx, err
}

// tab is tabCtx with the tab's state.
func (b *Browser) tab(step Step) (context.Context, *tabState, error) {
	if step.Tab == "" {
		return b.ctx, b.first, nil
	}
	if t, ok := b.tabs[step.Tab]; ok {
		return t.ctx, t.state, nil
	}
	if step.Action == ActionNavigate {
		return b.openTab(step.Tab, parseTimeout(step.Timeout, defaultStepTimeout))
	}
	return nil, nil, fmt.Errorf("tab %q is not open; open it with a navigate step", step.Tab)
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
	ctx, cancel := chromedp.NewContext(b.ctx, chromedp.WithTargetID(id))
	st := &tabState{}
	// The first Run on a context attaches its target for as long as that Run's
	// context lives, so it must be ctx itself, not a bounded child; bound it
	// from outside instead (cancelling ctx unblocks a stalled Run).
	errc := make(chan error, 1)
	go func() { errc <- chromedp.Run(ctx, setUpTab(b.launch, st)) }()
	var err error
	select {
	case err = <-errc:
	case <-time.After(timeout):
		err = fmt.Errorf("timeout after %s", timeout)
	}
	if err != nil {
		// Cancelling a context that never attached does not close its target.
		// openCtx may have expired by now, so close on a fresh one.
		cancel()
		closeCtx, cancelClose := context.WithTimeout(b.ctx, time.Second)
		defer cancelClose()
		_ = chromedp.Run(closeCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			return target.CloseTarget(id).Do(cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser))
		}))
		return nil, nil, fmt.Errorf("open tab %q: %w", name, err)
	}
	b.tabs[name] = tab{ctx: ctx, cancel: cancel, state: st}
	return ctx, st, nil
}

// closeTab closes a tab opened by a step, as a user closing it would.
func (b *Browser) closeTab(name string) error {
	if name == "" {
		return errors.New("close_tab needs a tab name; the tab the browser started with stays open")
	}
	t, ok := b.tabs[name]
	if !ok {
		return fmt.Errorf("tab %q is not open", name)
	}
	delete(b.tabs, name)
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
	tabCtx, err := b.tabCtx(Step{Tab: tabName})
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
			shotCtx, tErr := b.tabCtx(Step{Tab: step.Tab})
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
	if step.Action == ActionCall {
		return env.call(ctx, step)
	}
	if step.Action == ActionCloseTab {
		return "", b.closeTab(step.Tab)
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
			if err := chromedp.Run(runCtx, evaluate(step.Expression, &result)); err != nil {
				return "", err
			}
			return formatResult(result), nil
		})
	}
	return "", b.executeStepErr(ctx, tabCtx, st, env.BaseURL, step)
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
			return last, fmt.Errorf("%s: expected %q, got %q (after %s)", step.Action, want, last, timeout)
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

// evaluate is chromedp.Evaluate for an eval step: it waits for a promise the
// expression returns to settle, so a step can use await or fetch.
func evaluate(expression string, res any) chromedp.Action {
	return chromedp.Evaluate(expression, res, func(p *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams {
		return p.WithAwaitPromise(true)
	})
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
		} else if !strings.HasPrefix(url, "http") {
			url = strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(url, "/")
		}
		return runBounded(chromedp.Navigate(url))

	case ActionWaitVisible:
		return b.pollJS(ctx, tabCtx, timeout, visibleJS(step))

	case ActionWaitHidden:
		return b.pollJS(ctx, tabCtx, timeout, "!"+visibleJS(step))

	case ActionWaitText:
		return b.pollJS(ctx, tabCtx, timeout, fmt.Sprintf(
			`(document.body.textContent || '').includes(%q)`, step.Text,
		))

	case ActionWaitTextGone:
		return b.pollJS(ctx, tabCtx, timeout, fmt.Sprintf(
			`!(document.body.textContent || '').includes(%q)`, step.Text,
		))

	case ActionFill:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			return chromedp.Run(runCtx, chromedp.Evaluate(fmt.Sprintf(fillJS, findJS(step), describe(step), step.Value), nil))
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
		return b.atPoint(ctx, tabCtx, timeout, step, step.Action == ActionClick, func(ctx context.Context, x, y float64) error {
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
		return runBounded(chromedp.ActionFunc(func(ctx context.Context) error {
			return (&input.DispatchMouseEventParams{
				Type:      input.MouseWheel,
				X:         st.mouseX,
				Y:         st.mouseY,
				Buttons:   st.buttons,
				DeltaX:    *step.X,
				DeltaY:    *step.Y,
				Modifiers: st.keys | mods,
			}).Do(ctx)
		}))

	case ActionTouch:
		return b.touch(ctx, tabCtx, timeout, step)

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
			return setDevice(ctx, st, d)
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
			return applyMedia(ctx, st)
		}))

	case ActionScreenshot:
		// Just take a screenshot, ignore the bytes (used for side effects in agent)
		_, err := b.screenshot(tabCtx)
		return err

	case ActionAssertVisible:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			var visible bool
			if err := chromedp.Run(runCtx, chromedp.Evaluate(visibleJS(step), &visible)); err != nil {
				return err
			}
			if !visible {
				return fmt.Errorf("assert_visible: %s not visible", describe(step))
			}
			return nil
		})

	case ActionAssertNotVisible:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			var visible bool
			if err := chromedp.Run(runCtx, chromedp.Evaluate(visibleJS(step), &visible)); err != nil {
				return err
			}
			if visible {
				return fmt.Errorf("assert_not_visible: %s is visible", describe(step))
			}
			return nil
		})

	case ActionAssertText:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			got, err := elementString(runCtx, step, "el.textContent")
			if err != nil {
				return fmt.Errorf("assert_text: %w", err)
			}
			got = strings.TrimSpace(got)
			if got != step.Text {
				return fmt.Errorf("assert_text: expected %q, got %q", step.Text, got)
			}
			return nil
		})

	case ActionAssertTextContains:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			got, err := elementString(runCtx, step, "el.textContent")
			if err != nil {
				return fmt.Errorf("assert_text_contains: %w", err)
			}
			if !strings.Contains(got, step.Text) {
				return fmt.Errorf("assert_text_contains: %q not found in %q", step.Text, got)
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
			// shadow root, since selectors cannot reach in there.
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

	case ActionWaitURL:
		// Like assert_url, but an exact value may also be just the path,
		// query and hash.
		if step.Value != "" {
			return b.pollJS(ctx, tabCtx, timeout, fmt.Sprintf(
				`window.location.href === %q || (window.location.pathname + window.location.search + window.location.hash) === %q`,
				step.Value, step.Value,
			))
		}
		return b.pollJS(ctx, tabCtx, timeout, fmt.Sprintf(
			`window.location.href.includes(%q)`, step.Text,
		))

	case ActionAssertURL:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			var got string
			if err := chromedp.Run(runCtx, chromedp.Location(&got)); err != nil {
				return err
			}
			if step.Value != "" && got != step.Value {
				return fmt.Errorf("assert_url: expected %q, got %q", step.Value, got)
			}
			if step.Text != "" && !strings.Contains(got, step.Text) {
				return fmt.Errorf("assert_url: %q not found in %q", step.Text, got)
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
			if err := chromedp.Run(runCtx, chromedp.Evaluate(findAllJS(step)+".length", &count)); err != nil {
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
		return fmt.Errorf("unknown action: %q", step.Action)
	}
}

// findAllJS is an expression for the elements a step's selector and has_text
// match, in document order. With has_text, an element that holds another
// match does not count, as with Playwright's getByText.
func findAllJS(step Step) string {
	return fmt.Sprintf(`(function(sel, hasText) {
		const all = Array.from(document.querySelectorAll(sel));
		if (hasText === '') return all;
		const norm = s => s.replace(/\s+/g, ' ').trim();
		// Text a user can see: not the title, scripts or styles.
		const matches = all.filter(el => !el.closest('head, script, style, noscript, template') && norm(el.textContent).includes(norm(hasText)));
		// Keep the innermost: an element holding a match holds its text
		// too, and "*" would otherwise pick <html>.
		const outer = new Set();
		const set = new Set(matches);
		for (const el of matches) {
			for (let p = el.parentElement; p && !outer.has(p); p = p.parentElement) {
				if (set.has(p)) outer.add(p);
			}
		}
		return matches.filter(el => !outer.has(el));
	})(%q, %q)`, step.Selector, step.HasText)
}

// findJS is an expression for the first element findAllJS has, or undefined.
func findJS(step Step) string {
	return findAllJS(step) + "[0]"
}

// describe names what a step's selector and has_text pick out, for errors.
func describe(step Step) string {
	if step.HasText == "" {
		return step.Selector
	}
	return fmt.Sprintf("%s with text %q", step.Selector, step.HasText)
}

// elementString evaluates expr, a string-valued expression of el, on the
// element step picks out.
func elementString(ctx context.Context, step Step, expr string) (string, error) {
	var got string
	err := chromedp.Run(ctx, evaluate(fmt.Sprintf(`(function(el) {
		if (!el) throw new Error(%q);
		return %s;
	})(%s)`, "no element matches "+describe(step), expr, findJS(step)), &got))
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
// another element at that point; the step polls until it is. Format with
// findJS, describe, dx, dy and whether to check enabled.
const targetJS = `(async function(el, sel, dx, dy, checkEnabled) {
	` + isVisibleJS + `
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
})(%s, %q, %s, %s, %t)`

// atPoint runs f at the point a pointer step aims at: x,y CSS pixels from the
// top-left corner of the element step.Selector matches when both are set,
// else its center, once targetJS finds the element ready there. Without a
// selector, x,y is a point in the viewport, taken as is.
func (b *Browser) atPoint(ctx, tabCtx context.Context, timeout time.Duration, step Step, checkEnabled bool, f func(ctx context.Context, x, y float64) error) error {
	if (step.X == nil) != (step.Y == nil) {
		return fmt.Errorf("%s: set both x and y, or neither", step.Action)
	}
	// dispatch runs f with time of its own: the element may have become
	// ready just as the step's timeout ran out.
	dispatch := func(x, y float64) error {
		runCtx, cancel := context.WithTimeout(tabCtx, timeout)
		defer cancel()
		return chromedp.Run(runCtx, chromedp.ActionFunc(func(ctx context.Context) error { return f(ctx, x, y) }))
	}
	if step.Selector == "" {
		if step.X == nil {
			return fmt.Errorf("%s: set a selector, or x and y", step.Action)
		}
		return dispatch(*step.X, *step.Y)
	}
	dx, dy := "null", "null"
	if step.X != nil {
		dx, dy = strconv.FormatFloat(*step.X, 'g', -1, 64), strconv.FormatFloat(*step.Y, 'g', -1, 64)
	}
	expr := fmt.Sprintf(targetJS, findJS(step), describe(step), dx, dy, checkEnabled)
	var target struct {
		X, Y float64
		Err  string
	}
	err := b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
		target.Err = ""
		if err := chromedp.Run(runCtx, evaluate(expr, &target)); err != nil {
			return err
		}
		if target.Err != "" {
			return fmt.Errorf("%s: %s", step.Action, target.Err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return dispatch(target.X, target.Y)
}

// pollCheck repeatedly runs check until it returns nil or the timeout expires,
// returning check's last error on timeout. It is the Go-assertion analogue of
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
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// pollJS polls a JS expression until it returns true or the timeout expires.
func (b *Browser) pollJS(ctx, tabCtx context.Context, timeout time.Duration, expr string) error {
	runCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()
	deadline := time.Now().Add(timeout)
	interval := 200 * time.Millisecond

	for {
		var result bool
		if err := chromedp.Run(runCtx, chromedp.Evaluate(expr, &result)); err != nil {
			// JS errors during polling are not fatal — element might not exist yet
		} else if result {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timeout after %s waiting for: %s", timeout, expr)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
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

// parseKeyModifiers parses a "modifiers" field such as "Shift" or
// "Control+Alt".
func parseKeyModifiers(modifiers string) (input.Modifier, error) {
	var mods input.Modifier
	for _, name := range strings.FieldsFunc(modifiers, func(r rune) bool { return r == '+' || r == ',' || r == ' ' }) {
		m, ok := keyModifiers[name]
		if !ok {
			return 0, fmt.Errorf("unknown modifier %q (use Shift, Control, Alt or Meta)", name)
		}
		mods |= m
	}
	return mods, nil
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
// (or the viewport's without a selector).
func (b *Browser) touch(ctx, tabCtx context.Context, timeout time.Duration, step Step) error {
	typ, ok := touchTypes[step.Value]
	if !ok {
		return fmt.Errorf("touch: value %q is not start, move, end or cancel", step.Value)
	}
	if typ == input.TouchEnd || typ == input.TouchCancel {
		runCtx, cancel := context.WithTimeout(tabCtx, timeout)
		defer cancel()
		return chromedp.Run(runCtx, input.DispatchTouchEvent(typ, []*input.TouchPoint{}))
	}
	if step.Points == "" {
		return b.atPoint(ctx, tabCtx, timeout, step, false, func(ctx context.Context, x, y float64) error {
			return input.DispatchTouchEvent(typ, []*input.TouchPoint{{X: x, Y: y, ID: 1}}).Do(ctx)
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
	// Aim at the first finger, then place the rest relative to it.
	first := step
	first.X, first.Y = &offsets[0][0], &offsets[0][1]
	return b.atPoint(ctx, tabCtx, timeout, first, false, func(ctx context.Context, x, y float64) error {
		var points []*input.TouchPoint
		for i, o := range offsets {
			points = append(points, &input.TouchPoint{X: x + o[0] - offsets[0][0], Y: y + o[1] - offsets[0][1], ID: float64(i + 1)})
		}
		return input.DispatchTouchEvent(typ, points).Do(ctx)
	})
}

// pressKey builds the action for a press_key step: a key name from keyNames or
// a single character, dispatched the way a physical keyboard (and Playwright)
// would — one keyDown carrying the key's text, then one keyUp. That matters
// for Enter: chromedp.KeyEvent would add a separate "char" event, which Chrome
// turns into a newline even when the page preventDefault()ed the keydown, so
// an app's Enter handler would fire AND a newline would land in the textarea.
// Control/Alt/Meta chords carry no text, since they don't type.
func pressKey(key, modifiers string, held input.Modifier) (chromedp.Action, error) {
	mods, err := parseKeyModifiers(modifiers)
	if err != nil {
		return nil, err
	}
	down, up, err := keyEvents(key, held|mods)
	if err != nil {
		return nil, err
	}
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if err := down.Do(ctx); err != nil {
			return err
		}
		return up.Do(ctx)
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
