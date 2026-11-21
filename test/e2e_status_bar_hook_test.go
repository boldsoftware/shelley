package test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Ported from ui/e2e/status-bar-hook.spec.ts.

// statusBarHook installs the status-bar hook, which prints .status-bar.json
// from the conversation's directory, and writes status as that file in
// dir. It returns a function that rewrites the file: a string as is,
// anything else as JSON.
func statusBarHook(s *shelley, dir string, status any) func(any) {
	hooks := filepath.Join(s.home, ".config", "shelley", "hooks")
	s.check(os.MkdirAll(hooks, 0o755))
	s.check(os.WriteFile(filepath.Join(hooks, "status-bar"), []byte("#!/bin/sh\n[ ! -f .status-bar.json ] || cat .status-bar.json\n"), 0o755))
	write := func(status any) {
		text, ok := status.(string)
		if !ok {
			text = mustJSON(status)
		}
		s.write(filepath.Join(dir, ".status-bar.json"), text)
	}
	write(status)
	return write
}

// statusBarHookVM has the page take this server for the exe.dev VM
// myvm.exe.xyz.
func statusBarHookVM(s *shelley) {
	s.patchInit(map[string]any{"is_exe_dev": true, "hostname": "myvm.exe.xyz"})
}

const statusBarHookSetup = `"The row" is the status-bar hook's row, [data-testid="status-bar-hook"]. "Go within the app to URL" means: eval (() => { window.history.pushState({}, '', 'URL'); window.dispatchEvent(new PopStateEvent('popstate')); })(), without reloading. "Send" means: fill the message input (data-testid "message-input") with "Hello", then click the send button (data-testid "send-button"). For CSS checks, eval getComputedStyle on the element and compare the property exactly. `

func TestE2EStatusBarHookShowsStatusAndReruns(t *testing.T) {
	first := "feature-x `wip` http://localhost:8000/"
	e2e(t, statusBarHookSetup+`The status-bar hook shows the conversation's status and reruns after each message. Calls "status ..." rewrite the hook's output before the next message. The conversation /c/hooked's hook first prints body `+mustJSON(first)+` (a JSON string; it has backticks) with background rgb(32, 74, 135) and color white; the conversation /c/other-chat's hook prints body "other-branch", but the page's first request for that conversation's status (GET /api/conversation/{{OTHER_ID}}/status-bar) is held until call "release other status"; call "other status asked" says whether the page has asked for it, "true" or "false". Perform the steps in order. 1. Navigate to /c/hooked. 2. The row's text should be exactly `+mustJSON(first)+` (VM-local URLs keep their text; backticks stay backticks). 3. The row's background-color should be "rgb(32, 74, 135)" and its color "rgb(255, 255, 255)". 4. There should be exactly one link in the row, and its href attribute should be exactly "https://myvm.exe.xyz:8000/". 5. The row has its own line above the composer: eval (() => { const r = document.querySelector('[data-testid="status-bar-hook"]').getBoundingClientRect(), i = document.querySelector('[data-testid="message-input"]').getBoundingClientRect(); return r.bottom <= i.top && r.width >= i.width; })() with expect "true". 6. Call "status feature-y", then send. The row's text should become exactly "feature-y", and its style attribute must not contain "background" (a missing style attribute passes). 7. Call "status not json", then send. The row's text should become exactly "Status hook failed". 8. Click the button named "Status hook failed" inside the row. The dialog named "Status hook error" (role dialog) should contain "status-bar hook failed: invalid JSON output:". 9. Press Escape; the dialog should become hidden. 10. Call "status feature-z", then send. The row's text should become exactly "feature-z". 11. Switching to another conversation keeps the row's place, blank, until its hook answers: go within the app to /c/other-chat. Call "other status asked" with expect "true". The row should exist with only whitespace as its text (eval /^\s*$/.test(row.textContent) with expect "true", failing if the row is missing), and its style attribute must not contain "background". 12. Call "release other status". The row's text should become exactly "other-branch". 13. An empty status hides the row: for each N of 1, 2, 3, 4 and 5 in turn: call "status empty N"; go within the app to /c/hooked; the row should be absent (count 0); go within the app to /c/other-chat; the row's text should be exactly "other-branch".`, func(s *shelley) {
		dir := filepath.Join(s.dir, "hooked")
		status := statusBarHook(s, dir, map[string]any{"body": first, "bgcolor": "rgb(32, 74, 135)", "color": "white"})
		s.conversationWith(map[string]any{"message": "Hello", "model": "predictable", "cwd": dir}, "hooked")
		statusBarHookVM(s)
		for name, value := range map[string]any{
			"feature-y": map[string]any{"body": "feature-y"},
			"not json":  "not json",
			"feature-z": map[string]any{"body": "feature-z", "bgcolor": "rgb(32, 74, 135)"},
		} {
			s.call("status "+name, func() string {
				status(value)
				return "written"
			})
		}
		for i, empty := range []any{"", map[string]any{}, map[string]any{"body": nil}, map[string]any{"body": []any{}}, map[string]any{"body": "   "}} {
			s.call("status empty "+strconv.Itoa(i+1), func() string {
				status(empty)
				return "written"
			})
		}
		other := filepath.Join(s.dir, "other")
		s.write(filepath.Join(other, ".status-bar.json"), mustJSON(map[string]any{"body": "other-branch"}))
		o := s.conversationWith(map[string]any{"message": "Hello", "model": "predictable", "cwd": other}, "other-chat")
		s.v("OTHER_ID", o.ID)
		// Hold its answer in the browser, not the hook in the server, whose
		// timeout would be a race.
		asked := s.signal("other status asked")
		release := s.gate("release other status")
		s.route("/api/conversation/"+o.ID+"/status-bar", func(w http.ResponseWriter, r *http.Request) {
			asked.Store(true)
			if release.wait(r) {
				s.pass.ServeHTTP(w, r)
			}
		})
	})
}

// statusBarHookItemsJS gives pages helpers for the status row's items:
// window.statusItem(kind) is the item element of kind "branch" (the button
// named feature/long-branch-name), "model" (the inline model picker's
// label), "picker" (the inline model picker) or "link" (the link named
// Preview); window.statusFocusRing(kind) focuses that item and returns
// "ok" when it has focus with a 2px solid outline at offset 0, no box
// shadow, and the outline inside the row, or else what is wrong; and
// window.statusRowScrolls() returns "ok" when the row's items don't wrap
// and the row scrolls sideways without a visible scrollbar or widening the
// page, or else what is wrong.
const statusBarHookItemsJS = `(() => {
  const row = () => document.querySelector('[data-testid="status-bar-hook"]');
  window.statusItem = (kind) => {
    const r = row();
    if (!r) return null;
    const named = (selector, name) => [...r.querySelectorAll(selector)]
      .filter((e) => (e.getAttribute('aria-label') ?? e.textContent).trim() === name);
    const found = {
      branch: () => named('button', 'feature/long-branch-name'),
      model: () => [...r.querySelectorAll('.model-picker-inline .p-select-label')],
      picker: () => [...r.querySelectorAll('.model-picker-inline')],
      link: () => named('a', 'Preview'),
    }[kind]();
    return found.length === 1 ? found[0] : null;
  };
  window.statusFocusRing = (kind) => {
    const el = window.statusItem(kind);
    if (!el) return 'no single ' + kind;
    el.focus();
    if (document.activeElement !== el) return kind + ' not focused';
    const style = getComputedStyle(el);
    const want = { outlineWidth: '2px', outlineStyle: 'solid', outlineOffset: '0px', boxShadow: 'none' };
    for (const [property, value] of Object.entries(want)) {
      if (style[property] !== value) return kind + ' ' + property + ' ' + style[property];
    }
    const box = el.getBoundingClientRect();
    const r = el.closest('[data-testid="status-bar-hook"]').getBoundingClientRect();
    if (box.top - 2 - r.top < -0.1) return kind + ' outline above the row';
    if (r.bottom - box.bottom - 2 < -0.1) return kind + ' outline below the row';
    return 'ok';
  };
  window.statusRowScrolls = () => {
    const r = row();
    const items = r.querySelectorAll('.status-hook-items');
    if (items.length !== 1) return items.length + ' item lists';
    if (getComputedStyle(items[0]).flexWrap !== 'nowrap') return 'items wrap';
    const style = getComputedStyle(r);
    if (style.overflowX !== 'auto') return 'overflow-x ' + style.overflowX;
    if (style.scrollbarWidth !== 'none') return 'scrollbar-width ' + style.scrollbarWidth;
    if (getComputedStyle(r, '::-webkit-scrollbar').display !== 'none') return 'webkit scrollbar shown';
    r.scrollLeft = 50;
    if (!(r.scrollLeft > 0)) return 'row does not scroll';
    if (document.documentElement.scrollWidth !== innerWidth) return 'page ' + document.documentElement.scrollWidth + 'px wide in a ' + innerWidth + 'px window';
    return 'ok';
  };
})();`

func TestE2EStatusBarHookItems(t *testing.T) {
	e2e(t, statusBarHookSetup+`Status items keep their colors, native actions, links, and phone scrolling, in the dark color scheme. The hook prints four items on a white row with black text: a button "feature/long-branch-name" (opens Git Graph), a button "Review commit" (opens the diff of commit {{COMMIT}}), the inline model picker, and a link "Preview" to http://localhost:8000/preview with its own light grey background and dark red text. window.statusItem(kind) is the single item element of kind "branch", "model" (the picker's label), "picker" or "link" (null if missing); window.statusFocusRing(kind) and window.statusRowScrolls() return "ok" or what is wrong (use with expect "ok"). For a CSS check of an item, eval getComputedStyle(window.statusItem(kind)).PROPERTY and compare exactly. Call "mark diffs" notes the page's requests so far; call "diff requested" says whether since then the page asked for the diff of commit {{COMMIT}} (GET /api/git/diffs with commit={{COMMIT}}), "true" or "false". "Hidden" means hidden or absent. Perform the steps in order. 1. For each width W of 1280 then 390: a. Resize the viewport to W x 900 (set_viewport width and height only) and navigate to /c/items. b. The row's background-color should be "rgb(255, 255, 255)"; the branch's color "rgb(0, 0, 0)". c. Hover the branch (the button named "feature/long-branch-name" in the row); its color should still be "rgb(0, 0, 0)". d. The link (the link named "Preview" in the row) should have attributes href exactly "https://myvm.exe.xyz:8000/preview", target "_blank" and rel "noopener noreferrer", and background-color "rgb(240, 240, 240)". e. Hover the link; its color should be "rgb(128, 0, 0)". f. Use the keyboard: eval window.statusItem('branch').focus(), press Tab, then press Shift+Tab as a key_down step "Shift", a press_key step "Tab" and a key_up step "Shift". g. Eval window.statusFocusRing('branch'), window.statusFocusRing('model') and window.statusFocusRing('link'), each with expect "ok". h. Hover the picker (".model-picker-inline" in the row); its color should be "rgb(0, 0, 0)". i. Click the picker. ".model-picker-panel" should be visible and have at least one ".p-select-option". Click the button named exactly "Close" in ".model-picker-panel"; the panel should become hidden; the picker's color should be "rgb(0, 0, 0)". j. Call "mark diffs", then click the button named "Review commit" in the row. Call "diff requested" with expect "true". ".diff-viewer-overlay" should be visible; click its ".diff-viewer-close"; the overlay should become hidden. k. Click the branch. ".git-graph-container" should be visible; click the button named exactly "Close" in it; it should become hidden. l. Only when W is 390: eval window.statusRowScrolls() with expect "ok". 2. For each N of 1 then 2, with the error E being "commmit" for 1 and "full or abbreviated commit hash" for 2: a. Call "bad commit N" (the hook now prints one "Bad commit" item with bad arguments), then reload the page. b. The row's text should be exactly "Status hook failed", and ".diff-viewer-overlay" count 0. c. Click the button named "Status hook failed" in the row. d. The dialog named "Status hook error" ([role="dialog"][aria-label="Status hook error"]) should contain E and be entirely within the viewport. e. Press Escape; the dialog should become hidden; the row's text should still be exactly "Status hook failed".`, func(s *shelley) {
		s.initScript(statusBarHookItemsJS)
		s.media("prefers-color-scheme", "dark")
		dir := filepath.Join(s.dir, "items")
		s.check(os.MkdirAll(dir, 0o755))
		s.gitIn(dir, "init", "-q")
		s.write(filepath.Join(dir, "file.txt"), "initial\n")
		s.gitIn(dir, "add", "file.txt")
		s.gitIn(dir, "commit", "-q", "-m", "Initial file")
		commit := s.gitIn(dir, "rev-parse", "HEAD")
		s.v("COMMIT", commit)
		status := statusBarHook(s, dir, map[string]any{
			"bgcolor": "rgb(255, 255, 255)",
			"color":   "rgb(0, 0, 0)",
			"body": []any{
				map[string]any{"body": "feature/long-branch-name", "action": "open_git"},
				map[string]any{"body": "Review commit", "action": "open_diff", "args": map[string]any{"commit": commit}},
				map[string]any{"body": "Predictable", "action": "open_models"},
				map[string]any{
					"body":    "Preview",
					"action":  "open_url",
					"args":    map[string]any{"url": "http://localhost:8000/preview"},
					"bgcolor": "rgb(240, 240, 240)",
					"color":   "rgb(128, 0, 0)",
				},
			},
		})
		s.conversationWith(map[string]any{"message": "Hello", "model": "predictable", "cwd": dir}, "items")
		statusBarHookVM(s)
		var mu sync.Mutex
		from := 0
		s.call("mark diffs", func() string {
			mu.Lock()
			defer mu.Unlock()
			from = len(s.requestLog())
			return "marked"
		})
		s.call("diff requested", func() string {
			mu.Lock()
			defer mu.Unlock()
			for _, req := range s.requestLog()[from:] {
				method, uri, _ := strings.Cut(req, " ")
				u, err := url.Parse(uri)
				if err == nil && method == "GET" && u.Path == "/api/git/diffs" && u.Query().Get("commit") == commit {
					return "true"
				}
			}
			return "false"
		})
		for i, args := range []map[string]any{{"commmit": commit}, {"commit": "HEAD~1"}} {
			s.call("bad commit "+strconv.Itoa(i+1), func() string {
				status(map[string]any{"body": []any{map[string]any{"body": "Bad commit", "action": "open_diff", "args": args}}})
				return "written"
			})
		}
	})
}

// statusBarHookSwipeJS gives pages window.swipe(selector, startX, endX),
// which dispatches a synthetic one-finger horizontal swipe (touchstart,
// one touchmove, touchend at clientY 300) on the first element matching
// selector and returns whether the touchmove's default was prevented.
const statusBarHookSwipeJS = `window.swipe = (selector, startX, endX) => {
  const target = document.querySelector(selector);
  if (!(target instanceof Element)) throw new Error('Missing ' + selector);
  const touch = (x) => new Touch({ identifier: 1, target, clientX: x, clientY: 300, screenX: x, screenY: 300, pageX: x, pageY: 300 });
  const start = touch(startX), end = touch(endX);
  target.dispatchEvent(new TouchEvent('touchstart', { bubbles: true, cancelable: true, touches: [start], targetTouches: [start], changedTouches: [start] }));
  const move = new TouchEvent('touchmove', { bubbles: true, cancelable: true, touches: [end], targetTouches: [end], changedTouches: [end] });
  target.dispatchEvent(move);
  target.dispatchEvent(new TouchEvent('touchend', { bubbles: true, cancelable: true, touches: [], targetTouches: [], changedTouches: [end] }));
  return move.defaultPrevented;
};`

func TestE2EStatusBarHookMobileSwipesLeaveDrawer(t *testing.T) {
	e2e(t, statusBarHookSetup+`On a phone, swipes on the status row leave the conversation drawer alone, even when the row's content fits. window.swipe(selector, startX, endX) dispatches a synthetic horizontal swipe on the first element matching selector and returns whether it was taken (its touchmove's default prevented), "true" or "false"; it runs once per eval, so use evals without expect, or with expect only when a single run's result is the check. "The drawer" is ".drawer"; it is open when it has the class "open". Perform the steps in order. 1. Navigate to /c/swipe. 2. A link inside the row should be visible. 3. The row's content fits: eval (() => { const el = document.querySelector('[data-testid="status-bar-hook"]'); return el.scrollWidth <= el.clientWidth; })() with expect "true". 4. The drawer must not have the class "open" (eval with expect "false" of document.querySelector('.drawer').classList.contains('open')). 5. Eval window.swipe('[data-testid="status-bar-hook"]', 180, 256) with expect "false"; the drawer must still not be open. 6. Eval window.swipe('.status-hook-row .text-link', 180, 256) with expect "false"; the drawer must still not be open. 7. Call "status long" (the row's text becomes long), then reload the page. 8. The row should contain "long-status-". 9. It overflows: eval (() => { const el = document.querySelector('[data-testid="status-bar-hook"]'); return el.scrollWidth > el.clientWidth; })() with expect "true". 10. Eval window.swipe('.status-hook-row .status-hook-item', 180, 256) with expect "false"; the drawer must still not be open. 11. Eval window.swipe('.main-content', 180, 256) with expect "true"; the drawer should come to have the class "open". 12. Eval window.swipe('.backdrop', 350, 150) with expect "true"; the drawer should come to not have the class "open".`, func(s *shelley) {
		s.viewport(393, 851)
		s.initScript(statusBarHookSwipeJS)
		dir := filepath.Join(s.dir, "swipe")
		status := statusBarHook(s, dir, map[string]any{"body": "Short https://example.com/"})
		s.conversationWith(map[string]any{"message": "Hello", "model": "predictable", "cwd": dir}, "swipe")
		s.call("status long", func() string {
			status(map[string]any{"body": strings.Repeat("long-status-", 50) + " https://example.com/"})
			return "written"
		})
	})
}
