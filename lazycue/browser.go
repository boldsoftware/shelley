package lazycue

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
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
	// tabs holds the extra tabs opened by steps naming a tab, by name. The
	// unnamed tab the browser started with is ctx.
	tabs       map[string]tab
	initScript string
	closeOnce  sync.Once

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
}

// Pixel 5 viewport.
const (
	viewportWidth  = 393
	viewportHeight = 851
	viewportScale  = 2.75
)

// NewBrowser launches a headless Chrome instance with Pixel 5 viewport (393x851).
// initScript runs before every page's own scripts when non-empty.
func NewBrowser(parentCtx context.Context, initScript string) (*Browser, error) {
	opts := append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dbus", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.WindowSize(viewportWidth, viewportHeight),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(parentCtx, opts...)
	ctx, ctxCancel := chromedp.NewContext(allocCtx)

	if err := chromedp.Run(ctx, setUpTab(initScript)); err != nil {
		ctxCancel()
		allocCancel()
		return nil, err
	}

	return &Browser{
		allocCancel: allocCancel,
		ctxCancel:   ctxCancel,
		ctx:         ctx,
		initScript:  initScript,
		tabs:        map[string]tab{},
	}, nil
}

// setUpTab gives a tab the viewport and init script every tab gets; both are
// per tab in Chrome, so a tab opened later needs them as much as the first.
func setUpTab(initScript string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if err := emulation.SetDeviceMetricsOverride(viewportWidth, viewportHeight, viewportScale, true).Do(ctx); err != nil {
			return fmt.Errorf("set viewport: %w", err)
		}
		if initScript == "" {
			return nil
		}
		if _, err := page.AddScriptToEvaluateOnNewDocument(initScript).Do(ctx); err != nil {
			return fmt.Errorf("install init script: %w", err)
		}
		return nil
	})
}

// tabCtx returns the chromedp context of the tab a step acts on. Only navigate
// opens a tab, so any other step naming an unopened tab is a script mistake
// and fails at once.
func (b *Browser) tabCtx(step Step) (context.Context, error) {
	if step.Tab == "" {
		return b.ctx, nil
	}
	if t, ok := b.tabs[step.Tab]; ok {
		return t.ctx, nil
	}
	if step.Action == ActionNavigate {
		return b.openTab(step.Tab, parseTimeout(step.Timeout, defaultStepTimeout))
	}
	return nil, fmt.Errorf("tab %q is not open; open it with a navigate step", step.Tab)
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
func (b *Browser) openTab(name string, timeout time.Duration) (context.Context, error) {
	openCtx, cancelOpen := context.WithTimeout(b.ctx, timeout)
	defer cancelOpen()
	var id target.ID
	if err := chromedp.Run(openCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		id, err = target.CreateTarget("about:blank").
			WithNewWindow(true).
			WithWidth(viewportWidth).
			WithHeight(viewportHeight).
			Do(cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser))
		return err
	})); err != nil {
		return nil, fmt.Errorf("open tab %q: %w", name, err)
	}
	ctx, cancel := chromedp.NewContext(b.ctx, chromedp.WithTargetID(id))
	// The first Run on a context attaches its target for as long as that Run's
	// context lives, so it must be ctx itself, not a bounded child; bound it
	// from outside instead (cancelling ctx unblocks a stalled Run).
	errc := make(chan error, 1)
	go func() { errc <- chromedp.Run(ctx, setUpTab(b.initScript)) }()
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
		return nil, fmt.Errorf("open tab %q: %w", name, err)
	}
	b.tabs[name] = tab{ctx: ctx, cancel: cancel}
	return ctx, nil
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

// closeTabs closes every tab a script opened, leaving the first.
func (b *Browser) closeTabs() {
	for name := range b.tabs {
		_ = b.closeTab(name)
	}
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
// A script starts with only the first tab open. The generating agent runs
// several scripts in one browser while it explores; tabs left open by one
// must not let the next pass without opening them itself, as it will have to
// in the fresh browser of a cached run.
func (b *Browser) ExecuteSteps(ctx context.Context, baseURL string, steps []Step) ([]StepResult, error) {
	b.closeTabs()
	var results []StepResult
	for i, step := range steps {
		start := time.Now()
		output, err := b.executeStep(ctx, baseURL, step)
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
func (b *Browser) executeStep(ctx context.Context, baseURL string, step Step) (string, error) {
	if step.Action == ActionCloseTab {
		return "", b.closeTab(step.Tab)
	}
	tabCtx, err := b.tabCtx(step)
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
		// An eval WITHOUT an expectation is a one-shot probe: run once and
		// return whatever it yields.
		if step.Expect == "" {
			var result interface{}
			if err := chromedp.Run(runCtx, chromedp.Evaluate(step.Expression, &result)); err != nil {
				return "", err
			}
			return fmt.Sprintf("%v", result), nil
		}
		// An eval WITH an expectation is an assertion on async UI state (e.g.
		// an <img> whose bytes are still loading, or tool output that is still
		// streaming/rendering). Like wait_visible/wait_text, poll until the
		// expected value is observed or the timeout expires, so a value that is
		// merely SLOW to settle under CI load doesn't spuriously fail the step
		// (which would trigger a costly LLM heal). The assertion is unchanged:
		// the expected value must still become true within the window.
		deadline := time.Now().Add(timeout)
		var got string
		for {
			var result interface{}
			if err := chromedp.Run(runCtx, chromedp.Evaluate(step.Expression, &result)); err != nil {
				// Transient JS errors (element not present yet) are not fatal
				// while we still have time to poll.
				got = "<eval error: " + err.Error() + ">"
			} else {
				got = fmt.Sprintf("%v", result)
				if got == step.Expect {
					return got, nil
				}
			}
			if time.Now().After(deadline) {
				return got, fmt.Errorf("eval: expected %q, got %q (after %s)", step.Expect, got, timeout)
			}
			select {
			case <-ctx.Done():
				return got, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	return "", b.executeStepErr(ctx, tabCtx, baseURL, step)
}

func (b *Browser) executeStepErr(ctx, tabCtx context.Context, baseURL string, step Step) error {
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
		return b.pollJS(ctx, tabCtx, timeout, fmt.Sprintf(
			`(function() {
				const el = document.querySelector(%q);
				if (!el) return false;
				const style = window.getComputedStyle(el);
				return style.display !== 'none' && style.visibility !== 'hidden' && el.offsetParent !== null;
			})()`, step.Selector,
		))

	case ActionWaitHidden:
		return b.pollJS(ctx, tabCtx, timeout, fmt.Sprintf(
			`(function() {
				const el = document.querySelector(%q);
				if (!el) return true;
				const style = window.getComputedStyle(el);
				return style.display === 'none' || style.visibility === 'hidden' || el.offsetParent === null;
			})()`, step.Selector,
		))

	case ActionWaitText:
		return b.pollJS(ctx, tabCtx, timeout, fmt.Sprintf(
			`(document.body.textContent || '').includes(%q)`, step.Text,
		))

	case ActionWaitTextGone:
		return b.pollJS(ctx, tabCtx, timeout, fmt.Sprintf(
			`!(document.body.textContent || '').includes(%q)`, step.Text,
		))

	case ActionFill:
		return b.fill(tabCtx, step.Selector, step.Value, timeout)

	case ActionClick:
		return runBounded(chromedp.Click(step.Selector, chromedp.ByQuery))

	case ActionHover:
		return runBounded(chromedp.QueryAfter(step.Selector, func(ctx context.Context, _ cdpruntime.ExecutionContextID, nodes ...*cdp.Node) error {
			return mouseMoveToNode(ctx, nodes[0], step.X, step.Y)
		}, chromedp.ByQuery, chromedp.NodeVisible))

	case ActionPressKey:
		action, err := pressKey(step.Key, step.Modifiers)
		if err != nil {
			return err
		}
		return runBounded(action)

	case ActionScreenshot:
		// Just take a screenshot, ignore the bytes (used for side effects in agent)
		_, err := b.screenshot(tabCtx)
		return err

	case ActionAssertVisible:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			var visible bool
			if err := chromedp.Run(runCtx, chromedp.Evaluate(fmt.Sprintf(
				`(function() {
				const el = document.querySelector(%q);
				if (!el) return false;
				const style = window.getComputedStyle(el);
				return style.display !== 'none' && style.visibility !== 'hidden' && el.offsetParent !== null;
			})()`, step.Selector,
			), &visible)); err != nil {
				return err
			}
			if !visible {
				return fmt.Errorf("assert_visible: element %q not visible", step.Selector)
			}
			return nil
		})

	case ActionAssertNotVisible:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			var visible bool
			if err := chromedp.Run(runCtx, chromedp.Evaluate(fmt.Sprintf(
				`(function() {
				const el = document.querySelector(%q);
				if (!el) return false;
				const style = window.getComputedStyle(el);
				return style.display !== 'none' && style.visibility !== 'hidden' && el.offsetParent !== null;
			})()`, step.Selector,
			), &visible)); err != nil {
				return err
			}
			if visible {
				return fmt.Errorf("assert_not_visible: element %q is visible", step.Selector)
			}
			return nil
		})

	case ActionAssertText:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			var got string
			if err := chromedp.Run(runCtx, chromedp.TextContent(step.Selector, &got, chromedp.ByQuery)); err != nil {
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
			var got string
			if err := chromedp.Run(runCtx, chromedp.TextContent(step.Selector, &got, chromedp.ByQuery)); err != nil {
				return fmt.Errorf("assert_text_contains: %w", err)
			}
			if !strings.Contains(got, step.Text) {
				return fmt.Errorf("assert_text_contains: %q not found in %q", step.Text, got)
			}
			return nil
		})

	case ActionAssertAttribute:
		return b.pollCheck(ctx, tabCtx, timeout, func(runCtx context.Context) error {
			var got string
			if err := chromedp.Run(runCtx, chromedp.AttributeValue(step.Selector, step.Attribute, &got, nil, chromedp.ByQuery)); err != nil {
				return fmt.Errorf("assert_attribute: %w", err)
			}
			if got != step.Value {
				return fmt.Errorf("assert_attribute %q: expected %q, got %q", step.Attribute, step.Value, got)
			}
			return nil
		})

	case ActionWaitURL:
		// Poll the current location until it matches. Useful for SPA route
		// changes (e.g. /new -> /c/<slug>) that happen asynchronously after a
		// click and can't be caught by the instantaneous assert_url.
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
			if err := chromedp.Run(runCtx, chromedp.Evaluate(fmt.Sprintf(
				`document.querySelectorAll(%q).length`, step.Selector,
			), &count)); err != nil {
				return fmt.Errorf("assert_count: %w", err)
			}
			if count != step.Count {
				return fmt.Errorf("assert_count: expected %d elements matching %q, got %d", step.Count, step.Selector, count)
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

// fill sets a value on an input/textarea with React-compatible event dispatching.
// timeout bounds the evaluation so a wedged renderer fails this step rather than
// hanging on the tab's deadline-free context.
func (b *Browser) fill(tabCtx context.Context, selector, value string, timeout time.Duration) error {
	// Determine if this is a textarea or input.
	js := fmt.Sprintf(`(function() {
		const el = document.querySelector(%q);
		if (!el) throw new Error('element not found: ' + %q);
		const tag = el.tagName.toLowerCase();
		const proto = tag === 'textarea' ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
		const nativeSetter = Object.getOwnPropertyDescriptor(proto, 'value').set;
		nativeSetter.call(el, %q);
		el.dispatchEvent(new Event('input', { bubbles: true }));
		el.dispatchEvent(new Event('change', { bubbles: true }));
		return true;
	})()`, selector, selector, value)

	var result bool
	runCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()
	return chromedp.Run(runCtx, chromedp.Evaluate(js, &result))
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
	for {
		err := check(runCtx)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
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

// parseKeyModifiers parses a press_key "modifiers" field such as "Shift" or
// "Control+Alt".
func parseKeyModifiers(modifiers string) (input.Modifier, error) {
	var mods input.Modifier
	for _, name := range strings.FieldsFunc(modifiers, func(r rune) bool { return r == '+' || r == ',' || r == ' ' }) {
		m, ok := keyModifiers[name]
		if !ok {
			return 0, fmt.Errorf("press_key: unknown modifier %q", name)
		}
		mods |= m
	}
	return mods, nil
}

// mouseMoveToNode moves the mouse pointer over n, scrolling it into view
// first: chromedp.MouseClickNode without the press and release. The pointer
// lands x,y CSS pixels from n's top-left corner when both are set, else at its
// center.
func mouseMoveToNode(ctx context.Context, n *cdp.Node, x, y *float64) error {
	if (x == nil) != (y == nil) {
		return fmt.Errorf("hover: set both x and y, or neither")
	}
	if err := dom.ScrollIntoViewIfNeeded().WithNodeID(n.NodeID).Do(ctx); err != nil {
		return err
	}
	quads, err := dom.GetContentQuads().WithNodeID(n.NodeID).Do(ctx)
	if err != nil {
		return err
	}
	if len(quads) == 0 || len(quads[0]) != 8 {
		return chromedp.ErrInvalidDimensions
	}
	q := quads[0]
	if x != nil {
		return input.DispatchMouseEvent(input.MouseMoved, q[0]+*x, q[1]+*y).Do(ctx)
	}
	var cx, cy float64
	for i := 0; i < 8; i += 2 {
		cx += q[i] / 4
		cy += q[i+1] / 4
	}
	return input.DispatchMouseEvent(input.MouseMoved, cx, cy).Do(ctx)
}

// pressKey builds the action for a press_key step: a key name from keyNames or
// a single character, dispatched the way a physical keyboard (and Playwright)
// would — one keyDown carrying the key's text, then one keyUp. That matters
// for Enter: chromedp.KeyEvent would add a separate "char" event, which Chrome
// turns into a newline even when the page preventDefault()ed the keydown, so
// an app's Enter handler would fire AND a newline would land in the textarea.
// Control/Alt/Meta chords carry no text, since they don't type.
func pressKey(key, modifiers string) (chromedp.Action, error) {
	mods, err := parseKeyModifiers(modifiers)
	if err != nil {
		return nil, err
	}
	r, ok := keyNames[key]
	if !ok {
		rs := []rune(key)
		if len(rs) != 1 {
			return nil, fmt.Errorf("press_key: unknown key %q (use a KeyboardEvent.key name such as Enter, Tab, Escape, ArrowDown or F5, or a single character)", key)
		}
		r = rs[0]
	}
	def, ok := kb.Keys[r]
	if !ok {
		// A character chromedp has no key definition for: type it.
		return chromedp.KeyEvent(string(r), chromedp.KeyModifiers(mods)), nil
	}
	if def.Shift {
		mods |= input.ModifierShift
	}
	down := &input.DispatchKeyEventParams{
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
	up := *down
	up.Type = input.KeyUp
	up.Text, up.UnmodifiedText = "", ""
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if err := down.Do(ctx); err != nil {
			return err
		}
		return up.Do(ctx)
	}), nil
}
