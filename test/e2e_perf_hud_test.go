package test

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// Ported from ui/e2e/perf-hud.spec.ts. The performance-hud feature flag
// overlays live recomputation counters (see ui/src/utils/perf.ts). The
// counters themselves are always collected and exposed at
// window.__shelleyPerf; the flag only controls the overlay.

func TestE2EPerfHudHiddenByDefault(t *testing.T) {
	e2e(t, `The performance HUD is hidden by default, while window.__shelleyPerf is still there. The conversation /c/{{SLUG}} was started with "echo perf-hud-off". 1. Navigate to /c/{{SLUG}}. 2. The element with data-testid "message-input" should be visible (allow 30s). 3. There must be exactly 0 ".perf-hud" elements. 4. Eval (() => { const perf = window.__shelleyPerf; return perf ? Object.keys(perf.snapshot()).length : -1; })() > 0: it must return true (the counters exist and have names).`, func(s *shelley) {
		s.v("SLUG", s.conversation("echo perf-hud-off", "").Slug)
	})
}

// perfHudStatuses records, in every page, each new text of the
// conversation's loading status with how many messages were on the page
// then. A MutationObserver in the page sees a status that a long render
// keeps painted, where polling from outside might not.
const perfHudStatuses = `(() => {
  const statuses = [];
  window.__shelleyLoadingStatuses = statuses;
  const collect = () => {
    const el = document.querySelector('.conversation-loading');
    const text = el && el.textContent ? el.textContent.trim() : '';
    if (text && (statuses.length === 0 || statuses[statuses.length - 1].text !== text)) {
      statuses.push({ text, messageElements: document.querySelectorAll('[data-testid="message"]').length });
    }
  };
  new MutationObserver(collect).observe(document, { childList: true, characterData: true, subtree: true });
})();`

// perfHudCached is an expression that is true once IndexedDB holds a
// conversation's full history.
const perfHudCached = `(async () => { const db = await new Promise((resolve, reject) => { const req = indexedDB.open('shelley-messages'); req.onsuccess = () => resolve(req.result); req.onerror = () => reject(req.error); }); try { if (!db.objectStoreNames.contains('conversation_meta')) return false; const row = await new Promise((resolve) => { const get = db.transaction('conversation_meta', 'readonly').objectStore('conversation_meta').get('{{ID}}'); get.onsuccess = () => resolve(get.result); get.onerror = () => resolve(undefined); }); return !!(row && row.has_full_history); } finally { db.close(); } })()`

// groupThousands writes n as en-US toLocaleString does.
func groupThousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func TestE2EPerfHudLoadSources(t *testing.T) {
	// A cache hit still has to mount thousands of DOM nodes. The detailed
	// status stays in the DOM through that work, instead of only the
	// generic spinner whose delayed timer cannot fire while the main thread
	// is blocked.
	e2e(t, `Loads from IndexedDB show a status, loads from memory render at once, and the HUD reports both. The performance-hud flag is on. The conversation {{ID}} (slug {{CSLUG}}) is a medium /debug/loremipsum conversation of exactly {{COUNT}} messages. window.__shelleyLoadingStatuses lists, for the current page, each text the loading status (".conversation-loading") showed, as {text, messageElements} with the number of [data-testid="message"] elements at that moment. Call "mark" notes where the browser's requests are; call "full loads" returns how many full loads of the conversation (GET /api/conversation/{{ID}} without last_sequence_id) the browser has made since. "The HUD" is the ".perf-hud" element; "the latest load" is window.__shelleyPerf.loads().at(-1). 1. Navigate to /c/{{ID}}. 2. The element with data-testid "message-input" should be visible (allow 30s), and the first element with data-testid "message" too (allow 30s). 3. Wait until IndexedDB has the whole history: eval this until it returns true (allow 30s): `+perfHudCached+`. 4. Call "mark"; it returns "marked". 5. Reload. 6. The first element with data-testid "message" should be visible (allow 30s). 7. Eval window.__shelleyLoadingStatuses.some((s) => s.text.includes('{{STATUS}}') && s.messageElements === 0): it must return true (the status named the message count while no message was mounted yet). 8. There should be exactly 0 ".conversation-loading" elements (allow 30s). 9. Call "full loads": it must return exactly "0" (the history came from IndexedDB). 10. Eval (() => { const l = window.__shelleyPerf.loads().at(-1); return !!l && l.conversationId === '{{ID}}' && l.source === 'indexeddb' && l.messages === {{COUNT}} && l.totalMs > 0 && l.renderMs > 0; })(): true. 11. The HUD's first ".perf-hud-load" should contain "IndexedDB" (allow 5s). 12. The expanded HUD overlays the drawer, so collapse it: click the HUD's ".perf-hud-title". 13. Eval document.querySelector('.perf-hud').classList.contains('collapsed') until it returns true. 14. In one eval without "expect", empty the list: window.__shelleyLoadingStatuses.length = 0. 15. Start a new conversation from the header: click the ".header" button named exactly "New Conversation" (role "button"; if the header does not show it, click the button named exactly "More options", then the button named exactly "New Conversation" in ".chat-overflow-popover"). 16. The URL should end in /new (assert_url, exact path "/new"). 17. Open the conversation list: on a phone, click the button with aria-label "Open conversations" and ".drawer.open" should be visible (on a wide screen, click the button with aria-label "Expand sidebar" if it shows instead). 18. "The row" is the element inside a ".conversation-title" whose text is exactly "{{CSLUG}}" (within ".conversation-title", selector "*", has_text "{{CSLUG}}", exact). Scroll it into view if needed, and it should then be in the viewport (assert_in_viewport). 19. Click the row. 20. The first element with data-testid "message" should be visible (allow 30s). 21. Eval JSON.stringify(window.__shelleyLoadingStatuses): exactly "[]" (a memory hit renders in the switch's own flush: no loading status). 22. Eval (() => { const l = window.__shelleyPerf.loads().at(-1); return !!l && l.conversationId === '{{ID}}' && l.source === 'memory' && l.messages === {{COUNT}}; })() until it returns true (the load is recorded once rendered, which can trail the first message showing). 23. Click the HUD's ".perf-hud-title" to expand it. 24. The HUD's first ".perf-hud-load" should contain "Memory".`, func(s *shelley) {
		s.pageFlag("performance-hud", true)
		s.initScript(perfHudStatuses)
		c := s.loremipsum("medium")
		var got struct {
			Messages     []any `json:"messages"`
			Conversation struct {
				Slug string `json:"slug"`
			} `json:"conversation"`
		}
		s.api("GET", "/api/conversation/"+c.ID, nil, &got)
		s.v("ID", c.ID)
		s.v("CSLUG", got.Conversation.Slug)
		s.v("COUNT", strconv.Itoa(len(got.Messages)))
		s.v("STATUS", groupThousands(len(got.Messages))+" messages")
		var mark int
		s.call("mark", func() string {
			mark = len(s.requestLog())
			return "marked"
		})
		s.call("full loads", func() string {
			n := 0
			for _, r := range s.requestLog()[mark:] {
				method, uri, _ := strings.Cut(r, " ")
				u, err := url.Parse(uri)
				if err == nil && method == "GET" && u.Path == "/api/conversation/"+c.ID && !u.Query().Has("last_sequence_id") {
					n++
				}
			}
			return strconv.Itoa(n)
		})
	})
}

func TestE2EPerfHudLiveCounters(t *testing.T) {
	e2e(t, `The performance-hud flag shows the HUD with live counters. The flag is on. The conversation /c/{{SLUG}} was started with "echo perf-hud-on". "The HUD" is the ".perf-hud" element. 1. Navigate to /c/{{SLUG}}. 2. The element with data-testid "message-input" should be visible (allow 30s). 3. The HUD should be visible (allow 10s). 4. The HUD's first "tbody tr" should be visible (allow 5s): loading a conversation mounts messages, which count. 5. The HUD must hold exactly 0 ".perf-hud-empty" elements (its "no counters yet" row). 6. Click the HUD's ".perf-hud-title" (not the header's right side: its reset/copy/pause buttons stop the click). 7. Eval document.querySelector('.perf-hud').classList.contains('collapsed') until it returns true. 8. The HUD's ".perf-hud-mini" should be visible. 9. Click the HUD's ".perf-hud-title" again. 10. Long tasks (main-thread blocks over 50ms) get their own section. Reset the counters, register a sentinel counter, then block the main thread in a real event-loop task (work run directly in a DevTools evaluate is not reported as a long task), with exactly this eval: new Promise((resolve) => { const perf = window.__shelleyPerf; perf.reset(); perf.count('test.suspect'); setTimeout(() => { const t0 = performance.now(); while (performance.now() - t0 < 80) {} resolve('blocked'); }, 0); }), expecting "blocked". 11. The HUD's first ".perf-hud-longtask" should be visible (allow 5s). 12. The HUD's ".perf-hud-longtask-suspects" element containing "test.suspect" (has_text) should be visible: the sentinel is a suspect for that long task.`, func(s *shelley) {
		s.pageFlag("performance-hud", true)
		s.v("SLUG", s.conversation("echo perf-hud-on", "").Slug)
	})
}
