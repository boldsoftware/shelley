package test

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
)

// Ported from ui/e2e/tail-first-mount.spec.ts.

// Tail-first conversation mounting: on load only the trailing chunks of a
// large conversation mount; older history renders as fixed-height
// placeholders that a background sweep (or scrolling near them) replaces
// with real content. See the tail-first block in ChatInterface.vue.
//
// Real chunks are 50 rows and the initial tail is 10 chunks, so exercising
// the placeholder path for real would need 500+ messages — too slow to seed
// in an e2e test. The shelley.tailFirstTest localStorage knob (read once at
// ChatInterface setup) shrinks both so a ~60-message conversation spans many
// chunks. Note LIVE_TAIL_CHUNKS=5 is not overridable, so the effective
// mounted tail is max(tailChunks, 5) chunks.

// tailFirst shrinks chunking so a ~60-row conversation spans many chunks
// (chunks of 5 rows, a tail of 2 chunks, the sweep on or off), and scales the
// chunk height estimate to match. The estimate must stay BELOW any real chunk
// height — production guarantees this (50-row chunks book 4000px, measure
// ~7000px), and the sweep's stability depends on it: an over-estimate
// deflates when a mounted chunk lays out near the viewport, and that shrink
// can race the clamp accounting into disarming follow. 50px is safely below
// any 5-row chunk. --messages-chunk-estimate feeds both .messages-chunk's
// contain-intrinsic-size and the placeholder height. It seeds a 15-turn
// conversation at {{SLUG}}.
func (s *shelley) tailFirst(sweep bool) convo {
	opts := map[string]any{"tailChunks": 2, "chunkSize": 5}
	if !sweep {
		opts["sweep"] = false
	}
	s.initScript(`localStorage.setItem('shelley.tailFirstTest', ` + strconv.Quote(mustJSON(opts)) + `);
const style = document.createElement('style');
style.textContent = '.messages-list { --messages-chunk-estimate: 50px; }';
document.addEventListener('DOMContentLoaded', () => document.head.appendChild(style));`)
	c := s.loremipsum("15")
	s.v("SLUG", c.Slug)
	return c
}

const tailFirstSetup = `The test shrinks the app's tail-first mounting so that a 15-turn conversation at /c/{{SLUG}} spans many chunks: on load only the last few chunks mount (as ".messages-chunk" elements) and older history renders as placeholders ([data-testid="pending-chunk"]). "The counts" are pending = document.querySelectorAll('[data-testid="pending-chunk"]').length and mounted = document.querySelectorAll('.messages-chunk').length. "The gap" is the messages container's (".messages-container") scrollHeight - scrollTop - clientHeight. To "open the conversation", navigate to /c/{{SLUG}}, wait for the message input ([data-testid="message-input"]) to be visible (allow 30s), then wait until the LAST element matching [data-testid="message"] is visible (its getBoundingClientRect() has a non-zero width and height; allow 30s). The test has already set the shelley.tailFirstTest knob; do not set it yourself. Check every assertion with an eval that has "expect" (or an assert_* or wait_* step): an eval without "expect" asserts nothing. Wait with such steps and a timeout, not with polling loops inside the expression. When a step says to record something, do it in one eval without "expect" that stores it in the named window variable, then check that variable in a separate eval with "expect". Perform the steps in order. `

func TestE2ETailFirstMountsTailPinned(t *testing.T) {
	e2e(t, tailFirstSetup+`Tail-first mounting mounts only the tail at load and keeps the bottom pinned; the background sweep is off.
1. Open the conversation.
2. Right away, in a single eval (no expect), record window.__atLoad = JSON.stringify({pending: pending > 0, mounted: mounted > 0, bottom: gap < 5}) from the counts and the gap at that moment.
3. window.__atLoad must be exactly {"pending":true,"mounted":true,"bottom":true}: placeholders stand in for older history, the tail is real content, and the viewport sits at the true bottom (not merely near it).
4. The last [data-testid="message"] element must intersect the viewport (its bounding rect overlaps both the window and the messages container's rect).
5. The scroll-to-bottom button (".scroll-to-bottom-button") must not be visible.`, func(s *shelley) {
		s.tailFirst(false)
	})
}

func TestE2ETailFirstCachedRefreshKeepsHistoryUnmounted(t *testing.T) {
	e2e(t, tailFirstSetup+`A cached refresh keeps history unmounted while waiting for the server; the background sweep is off and the conversation, {{ID}}, is archived. Once call "hold" has run, the server's answers to incremental loads of the conversation (a GET of /api/conversation/{{ID}} with a last_sequence_id query parameter) are held back until call "release"; call "incremental" says whether such a load has arrived ("true" or "false").
1. Navigate to /c/{{SLUG}}.
2. Wait until window.__shelleyPerf?.loads().some((load) => load.conversationId === "{{ID}}") is true.
3. Record the counts: window.__initial = {pending, mounted}. window.__initial.pending must be greater than 0.
4. Leave the conversation the way the router would: eval history.pushState({}, "", "/new") and then dispatch a PopStateEvent("popstate") on window.
5. Wait until there are exactly 0 [data-testid="message"] elements.
6. Call "hold".
7. Go back in the browser history, as its back button does (the app pushed that entry, so the same document stays loaded and window.__initial survives).
8. Call "incremental" until it returns "true".
9. Wait until the last [data-testid="message"] element is visible (non-zero width and height).
10. While the incremental load is still held, record the counts in window.__refreshing = {pending, mounted}; then JSON.stringify({pending: window.__refreshing.pending > 0, fewer: window.__refreshing.mounted < window.__initial.pending + window.__initial.mounted, overlays: document.querySelectorAll(".conversation-loading-overlay").length}) must be exactly {"pending":true,"fewer":true,"overlays":0}.
11. Call "release".
12. Wait until window.__shelleyPerf.loads().filter((load) => load.conversationId === "{{ID}}").length is exactly 2.
13. pending must still be greater than 0.`, func(s *shelley) {
		c := s.tailFirst(false)
		s.v("ID", c.ID)
		s.api("POST", "/api/conversation/"+c.ID+"/archive", nil, nil)
		var armed atomic.Bool
		seen := s.signal("incremental")
		held := s.gate("release")
		s.route("GET /api/conversation/"+c.ID, func(w http.ResponseWriter, r *http.Request) {
			if armed.Load() && r.URL.Query().Has("last_sequence_id") {
				seen.Store(true)
				if !held.wait(r) {
					return
				}
			}
			s.pass.ServeHTTP(w, r)
		})
		s.call("hold", func() string {
			armed.Store(true)
			return "holding"
		})
	})
}

func TestE2ETailFirstSweepKeepsViewport(t *testing.T) {
	e2e(t, tailFirstSetup+`The background sweep mounts everything without moving the viewport; the sweep is on.
1. Open the conversation.
2. Record the worst scroll drift during the sweep, not just the endpoint (the follow machinery could mask a jump by re-pinning afterward): in one eval, set window.__maxDrift = 0 and add a "scroll" listener to the messages container that computes drift = Math.abs((el.scrollHeight - el.clientHeight) - el.scrollTop) and raises window.__maxDrift to it when larger.
3. Wait until pending is exactly 0, with an eval whose "timeout" is "30s": the sweep replaced every placeholder.
4. Wait until the gap is below 5 (allow 15s).
5. window.__maxDrift must be below 120: the bottom-pinned viewport was not disturbed even transiently.
6. The scroll-to-bottom button (".scroll-to-bottom-button") must not be visible.`, func(s *shelley) {
		s.tailFirst(true)
	})
}

func TestE2ETailFirstScrollUpRevealsPlaceholders(t *testing.T) {
	e2e(t, tailFirstSetup+`Scrolling up reveals placeholders so none is left in the viewport; the background sweep is off.
1. Open the conversation.
2. Record the counts: window.__before = {pending, mounted}. window.__before.pending must be greater than 0.
3. Scroll to the top of the transcript: eval document.querySelector(".messages-container").scrollTo({top: 0, behavior: "instant"}).
4. Wait until mounted is greater than window.__before.mounted (allow 15s): the placeholders that entered the scrollport hydrate into real content.
5. Wait until no placeholder intersects the messages container (allow 15s): the number of [data-testid="pending-chunk"] elements whose getBoundingClientRect() has bottom > the container rect's top and top < the container rect's bottom must be exactly 0 (no blank band where the user is looking).`, func(s *shelley) {
		s.tailFirst(false)
	})
}

func TestE2ETailFirstTOCJumpMountsTarget(t *testing.T) {
	e2e(t, tailFirstSetup+`A table-of-contents jump into unmounted history mounts the target chunk; the background sweep is off.
1. Open the conversation.
2. Record the counts: window.__before = {pending, mounted}. window.__before.pending must be greater than 0.
3. Click the table of contents button (".toc-button"), then click the first TOC entry (".toc-entry") whose text contains "Turn 1:" (the first user turn, whose chunk is a placeholder).
4. Wait until mounted is greater than window.__before.mounted (allow 15s).
5. The highlighted target (".message-highlight") is in the viewport, clipped by the scrolling messages container like Playwright's toBeInViewport (assert_in_viewport, allow 15s): the mount and jump landed on it.`, func(s *shelley) {
		s.tailFirst(false)
	})
}

func TestE2ETailFirstAppendKeepsTailPinned(t *testing.T) {
	e2e(t, tailFirstSetup+`A streaming append while history is unmounted keeps the tail pinned; the background sweep is off.
1. Open the conversation.
2. pending must be greater than 0.
3. Fill the message input with "echo tail-first append" and click the send button ([data-testid="send-button"]).
4. Wait until a [data-testid="message"] element containing the text "echo tail-first append" is visible (allow 30s), then until the agent-thinking indicator ([data-testid="agent-thinking"]) is hidden (allow 30s; it may already be hidden, so do not wait for it to appear first).
5. Wait until the gap is below 5 (allow 15s): the append rendered at the bottom and stayed pinned.
6. pending must still be greater than 0: the placeholders were left alone.`, func(s *shelley) {
		s.tailFirst(false)
	})
}
