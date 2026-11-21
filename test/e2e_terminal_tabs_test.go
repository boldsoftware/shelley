package test

import "testing"

// Ported from ui/e2e/terminal-tabs.spec.ts.

// inViewport is an eval of whether the element sel matches is in the
// viewport, as Playwright's toBeInViewport decides it.
func inViewport(sel string) string {
	return `new Promise(r => { const o = new IntersectionObserver(([e]) => { o.disconnect(); r(e.intersectionRatio > 0); }); o.observe(document.querySelector('` + sel + `')); })`
}

// Tabs are labelled with the window title the program sets (OSC 2), like any
// terminal emulator, and the + button opens another shell.
func TestE2ETerminalTabsFollowTitle(t *testing.T) {
	e2e(t, `Terminal tabs follow the window title and + opens a shell. The test seeded a conversation at /c/term-tabs, whose directory holds title.sh, which sets the window title (OSC 2) to "a long window title that ends in tab-tail" (call it "the title"), prints "title-set" and then waits reading input. "The active tab" is ".terminal-panel-tab-active", and a tab's label is its ".terminal-panel-tab-label". `+termInput+`
1. Navigate to /c/term-tabs. Fill [data-testid="message-input"] with "!bash title.sh" and click [data-testid="send-button"].
2. The active tab's label should have exactly the title as its text, and the active tab's ".terminal-panel-tab-label-tail" exactly "tab-tail". The tail must be in the viewport: eval `+"`"+inViewport(".terminal-panel-tab-active .terminal-panel-tab-label-tail")+"`"+` must be true. The active tab's title attribute should be exactly the title.
3. Reload. The active tab's label should again be exactly the title (the replayed screen carries no title; the tab remembers it).
4. The button with aria-label exactly "New terminal" must be in the viewport (the same IntersectionObserver eval on it must be true). Click it.
5. There should be exactly 2 ".terminal-panel-tab", and the second one should have the class "terminal-panel-tab-active". The second tab's ".terminal-panel-tab-running" should be visible (keys typed before the socket opens are dropped, so wait for it).
6. Type into the terminal input, key by key: printf '\033]2;second\007'; cat
   Then press Enter (cat keeps the prompt from retitling).
7. The active tab's label should become exactly "second", and the first ".terminal-panel-tab"'s label should still be exactly the title.`, func(s *shelley) {
		s.terminalConversation("terminal-tabs", "terminal tabs test", "term-tabs", map[string]string{
			"title.sh": "printf '\\033]2;%s\\007' 'a long window title that ends in tab-tail'\nprintf 'title-set\\n'\nexec cat\n",
		})
	})
}
