package test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Ported from ui/e2e/scroll-behavior.spec.ts.

// scrollTerms defines the terms the scroll behavior descriptions use.
const scrollTerms = `"The container" is the messages container (".messages-container"), "the button" the scroll-to-bottom button (".scroll-to-bottom-button"), and "the gap" the container's scrollHeight - clientHeight - scrollTop. To "send" a message, fill the message input ([data-testid="message-input"]) with it and click the send button ([data-testid="send-button"]). Check every assertion with an eval that has "expect" (or an assert_* or wait_* step): an eval without "expect" asserts nothing. When a step says to record something, do it in one eval without "expect" that stores it in the named window variable, then check that variable. Perform the steps in order. `

// scrollEchoSetup seeds the conversation the "echo message" tests start from, as
// {{SLUG}}.
const scrollEchoSetup = `The test seeds a conversation at /c/{{SLUG}} whose first message is "echo message 0". To "open the conversation", navigate to /c/{{SLUG}} and wait for the message input to be visible (allow 30s). To "add message N", send "echo message N" and wait until a [data-testid="message"] element containing the text "echo message N" is visible (allow 30s). `

func scrollEchoConversation(s *shelley) {
	s.v("SLUG", s.conversation("echo message 0", "").Slug)
}

// scrollMediumSetup seeds the loremipsum conversation many tests scroll, as {{ID}}.
const scrollMediumSetup = `The test seeds a long conversation with ID {{ID}}; to "open the conversation", navigate to /c/{{ID}}, wait for the message input ([data-testid="message-input"]) to be visible (allow 30s), then wait for the first [data-testid="message"] element to be visible (allow 30s). `

func scrollMediumConversation(s *shelley) convo {
	c := s.loremipsum("medium")
	s.v("ID", c.ID)
	return c
}

func TestE2EScrollAutoPinningNoSyncReadback(t *testing.T) {
	e2e(t, scrollTerms+scrollMediumSetup+`Auto-pinning does not synchronously read back scrollTop. The page counts the container's scrollTop writes in window.__messagesScrollTopWrites, and in window.__messagesScrollTopReadsAfterWrite every read of its scrollTop in the same task as a write (before the next microtask).
1. Open the conversation.
2. Wait until (window.__messagesScrollTopWrites || 0) > 0 is true.
3. (window.__messagesScrollTopReadsAfterWrite || 0) must be exactly 0.`, func(s *shelley) {
		s.initScript(`const scrollTop = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTop');
let wroteMessagesScrollTop = false;
window.__messagesScrollTopWrites = 0;
window.__messagesScrollTopReadsAfterWrite = 0;
Object.defineProperty(Element.prototype, 'scrollTop', {
  configurable: scrollTop.configurable,
  enumerable: scrollTop.enumerable,
  get() {
    if (wroteMessagesScrollTop && this instanceof HTMLElement && this.classList.contains('messages-container')) {
      window.__messagesScrollTopReadsAfterWrite = (window.__messagesScrollTopReadsAfterWrite || 0) + 1;
    }
    return scrollTop.get.call(this);
  },
  set(value) {
    if (this instanceof HTMLElement && this.classList.contains('messages-container')) {
      window.__messagesScrollTopWrites = (window.__messagesScrollTopWrites || 0) + 1;
      wroteMessagesScrollTop = true;
      queueMicrotask(() => { wroteMessagesScrollTop = false; });
    }
    scrollTop.set.call(this, value);
  },
});`)
		scrollMediumConversation(s)
	})
}

func TestE2EScrollPromotedDraftAllowsBareScrolling(t *testing.T) {
	e2e(t, scrollTerms+`A promoted draft allows bare scrolling after its first response.
1. Navigate to /new and wait for the message input ([data-testid="message-input"]) to be visible.
2. Let the observer report the empty list before the draft gains an ID: eval a promise that resolves after two animation frames.
3. Fill the message input with "wide tables"; the draft is promoted, so wait until /\/c\/[^/]+$/.test(location.href) is true.
4. Click the send button ([data-testid="send-button"]).
5. Wait until an element whose whole text is exactly "Wide Table (many columns)" exists (it need not be visible): eval [...document.querySelectorAll("body *")].some((e) => e.textContent.trim() === "Wide Table (many columns)") expecting true. Then wait until the agent-thinking indicator ([data-testid="agent-thinking"]) is hidden.
6. Wait until the gap is below 5 (gap < 5, not exactly 0); the button must be hidden.
7. Find/accessibility navigation has no preceding wheel or touch event, and it must release initial bottom restoration rather than get pulled back down: eval setting the container's scrollTop to 0 (nothing else).
8. The button should become visible, and the container's scrollTop below 5.
9. Send "markdown: still reading". Wait until the last of all ".message-agent" elements in the page ([...document.querySelectorAll(".message-agent")].at(-1)) contains "still reading", then until the agent-thinking indicator is hidden.
10. The container's scrollTop must still be below 5, and the button visible.
11. Click the button. It should become hidden, and the gap below 5.`, nil)
}

// scrollProbes instruments the page for TestE2EScrollButtonAndAutoScroll.
const scrollProbes = `window.__installScrollProbes = () => {
  const originalRect = Element.prototype.getBoundingClientRect;
  const scrollHeight = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollHeight');
  const scrollTop = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTop');
  window.__messageRectReads = 0;
  window.__messagesScrollHeightReads = 0;
  Element.prototype.getBoundingClientRect = function () {
    if (this instanceof HTMLElement && this.hasAttribute('data-message-id')) {
      window.__messageRectReads = (window.__messageRectReads || 0) + 1;
    }
    return originalRect.call(this);
  };
  Object.defineProperty(Element.prototype, 'scrollHeight', {
    configurable: scrollHeight.configurable,
    enumerable: scrollHeight.enumerable,
    get() {
      if (this instanceof HTMLElement && this.classList.contains('messages-container')) {
        window.__messagesScrollHeightReads = (window.__messagesScrollHeightReads || 0) + 1;
      }
      return scrollHeight.get.call(this);
    },
  });
  Object.defineProperty(Element.prototype, 'scrollTop', {
    configurable: scrollTop.configurable,
    enumerable: scrollTop.enumerable,
    get() { return scrollTop.get.call(this); },
    set(value) {
      // WebKit can wrap extremely large scroll offsets back to zero.
      scrollTop.set.call(this, value > 0x7fffffff ? 0 : value);
    },
  });
  return 'installed';
};
// Setting scrollTop dispatches the 'scroll' event asynchronously, so the
// component's userScrolled flag isn't set synchronously. Under load a late
// streaming delta can fire the ResizeObserver before that scroll event lands
// and auto-scroll back to the bottom, hiding the button for good. So this
// re-scrolls until the button stays: for up to 30s, it zeroes the probe
// counters, sets the container's scrollTop to 0 and waits up to a second for
// the button to be visible.
window.__scrollUpUntilButton = async () => {
  const visible = () => {
    const b = document.querySelector('.scroll-to-bottom-button');
    if (!b) return false;
    const r = b.getBoundingClientRect();
    return r.width > 0 && r.height > 0 && getComputedStyle(b).visibility !== 'hidden';
  };
  const deadline = performance.now() + 30000;
  while (performance.now() < deadline) {
    window.__messageRectReads = 0;
    window.__messagesScrollHeightReads = 0;
    document.querySelector('.messages-container').scrollTop = 0;
    const until = performance.now() + 1000;
    while (performance.now() < until) {
      await new Promise((resolve) => setTimeout(resolve, 50));
      if (visible()) return 'shown';
    }
  }
  throw new Error('the scroll-to-bottom button never stayed visible');
};`

func TestE2EScrollButtonAndAutoScroll(t *testing.T) {
	e2e(t, scrollTerms+scrollEchoSetup+`The button shows when scrolled up, and the conversation auto-scrolls at the bottom. The page provides two helpers: window.__installScrollProbes() makes the page count, in window.__messageRectReads, getBoundingClientRect() calls on elements with a data-message-id, and in window.__messagesScrollHeightReads reads of the container's scrollHeight (and makes scrollTop writes above 0x7fffffff land at 0, as WebKit does); window.__scrollUpUntilButton() zeroes those counters, sets the container's scrollTop to 0 and waits for the button to be visible, repeating that for up to 30s until it is, and throws if it never is ("scroll up until the button shows": eval await window.__scrollUpUntilButton() with a 40s step timeout).
1. Open the conversation.
2. For N = 1, 2 and 3 in turn: add message N, then wait until the agent-thinking indicator ([data-testid="agent-thinking"]) is hidden (allow 30s).
3. Eval window.__installScrollProbes().
4. Scroll up until the button shows.
5. window.__messageRectReads must be exactly 0 and window.__messagesScrollHeightReads exactly 0: the table of contents must not synchronously measure every message during scroll (that forces Safari to lay out far-off content-visibility:auto messages).
6. Click the table of contents button (".toc-button"); the ".toc-entry-top" element should get the class "toc-entry-active".
7. The TOC's bottom action must stay pinned while lazy content expands. In one eval: insert a div with data-testid "lazy-bottom-growth" into the container's ".messages-list" just before its ".messages-bottom-sentinel", and start growing it on animation frames: each frame adds 400px to its height, until 1200px.
8. Right after, click ".toc-entry-bottom".
9. Wait until Math.abs(gap) <= 1 is true.
10. Scroll up until the button shows.
11. Return to the bottom. A late streaming-driven auto-scroll may beat us to it and hide the button first; either path leaves us pinned at the bottom. So in one eval, if the button is visible right now (it exists, its getBoundingClientRect() has non-zero width and height, and its computed visibility is not "hidden"), call its click(). Then the button should become hidden (allow 5s).
12. A slow touch or scrollbar drag must release the bottom pin even when its first scroll event moves less than the pin's jump threshold. In one async eval on the container: first await two animation frames, so the bottom observer has seen the return to the bottom (a drag in the very frame of the return races it); then dispatch new PointerEvent("pointerdown", {pointerId: 1, bubbles: true}); set scrollTop to Math.max(0, scrollTop - 50); await one animation frame; dispatch new PointerEvent("pointerup", {pointerId: 1, bubbles: true}).
13. Wait until the gap is greater than 20. The button should become visible (allow 5s). Click it; it should become hidden (allow 5s).
14. An upward wheel gesture must immediately release lazy-layout pinning. In one eval on the container: dispatch new WheelEvent("wheel", {deltaY: -200, bubbles: true}), then set scrollTop to 0. The button should become visible (allow 5s).
15. A large upward jump (Home, a scrollbar drag) must release the pin too, even with no wheel event before it. In one eval: call .click() on the button element (a DOM click, in the same task), insert a div 1200px tall into ".messages-list" just before ".messages-bottom-sentinel", and set the container's scrollTop to Math.max(0, scrollTop - 200).
16. Wait until the gap is greater than 100. The button should become visible (allow 5s). Click it. Wait until Math.abs(gap) <= 1 is true.
17. Send "echo final message" and wait until a [data-testid="message"] element containing "echo final message" is visible (allow 30s). The button should not become visible: assert it is not visible (we are following the conversation).
18. Regression: after a full reload the conversation renders through the loading spinner, which recreates ".messages-list", and the scroll observers must re-attach to the new nodes. Reload the page, then wait for the message input and the container to be visible (allow 30s).
19. Scroll up until the button shows.
20. Eval setting the container's scrollTop to its scrollHeight. The button should become hidden (allow 5s).`, func(s *shelley) {
		s.initScript(scrollProbes)
		scrollEchoConversation(s)
	})
}

// scrollEchoTurns has the description add messages 1 to n, optionally waiting
// for each turn to finish.
func scrollEchoTurns(n int, idle bool) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "add message %d", i)
		if idle {
			b.WriteString(` and wait until the agent-thinking indicator ([data-testid="agent-thinking"]) is hidden (allow 30s)`)
		}
		if i < n {
			b.WriteString("; then ")
		}
	}
	return b.String()
}

// Regression for "Safari stops following the conversation while Chrome
// keeps scrolling". .messages-chunk rows use content-visibility:auto with
// contain-intrinsic-size estimates, and WebKit's estimates drift far from
// real heights. While following, the ResizeObserver's follow branch writes
// a scrollTop computed from those estimates; resolving the write lays the
// chunks out for real, the estimates deflate (measured: a 150136px target
// clamped to 146906 — a 3230px jump), and the clamp's scroll event lands
// BEFORE the ResizeObserver's shrink report, so no clampBudget could have
// absorbed it. handleScroll saw an upward delta far past the 100px
// unambiguous-gesture threshold and disarmed auto-follow: the view stopped
// following mid-stream and the scroll-to-bottom button stranded, while the
// viewport sat exactly at the (real) bottom — the sentinel never left
// view, so the IntersectionObserver never corrected anything.
//
// The fix mirrors the container-growth retroactive undo for list shrinks:
// the ResizeObserver's late shrink report recognizes the just-misread
// scroll-up as its clamp and re-arms auto-follow.
//
// Chromium's estimates track real heights closely and its rendering
// pipeline delivered the shrink report first, which is why the bug was
// Safari-only. Simulate WebKit's behavior deterministically: grow the
// list (inflated estimate), and when the follow branch writes the
// now-stale bottom target, deflate the list inside the scrollTop setter —
// the browser clamps the write against the deflated layout exactly like
// WebKit resolving content-visibility estimates, and the shrink report
// reaches the ResizeObserver only after the clamp's scroll event.
const scrollDeflationTrap = `window.__springDeflationTrap = async () => {
  const container = document.querySelector('.messages-container');
  const list = container.querySelector('.messages-list');
  const sentinel = container.querySelector('.messages-bottom-sentinel');
  if (!list || !sentinel) throw new Error('message list sentinel not found');
  const proto = Object.getPrototypeOf(container);
  const desc = Object.getOwnPropertyDescriptor(proto, 'scrollTop') ||
    Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTop');
  const spacer = document.createElement('div');
  spacer.style.height = '600px';
  let fired = false;
  Object.defineProperty(container, 'scrollTop', {
    configurable: true,
    get() { return desc.get.call(this); },
    set(value) {
      // A follow write targeting far past the current offset is the one
      // computed from the inflated estimate. Deflate the list BEFORE the
      // write applies: the browser then clamps the write against the real
      // layout and fires a scroll event with a large upward delta before
      // any ResizeObserver report.
      if (!fired && value - desc.get.call(this) > 300) {
        fired = true;
        spacer.style.height = '0px';
      }
      desc.set.call(this, value);
    },
  });
  list.insertBefore(spacer, sentinel);
  // Let the ResizeObserver see the growth and follow it down.
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  spacer.remove();
  delete container.scrollTop;
  return fired;
};`

func TestE2EScrollClampedFollowWriteKeepsFollowing(t *testing.T) {
	e2e(t, scrollTerms+scrollEchoSetup+`A clamped follow write does not disarm auto-follow (Safari estimate deflation). The page provides window.__springDeflationTrap(): it inserts a 600px spacer before the container's bottom sentinel, waits four animation frames for the follow branch to chase it, and if that follow writes a scrollTop more than 300px past the current one it collapses the spacer just before the write lands (as WebKit's estimates deflate), then removes the spacer and resolves to whether that happened.
1. Open the conversation.
2. Enough content that the list actually scrolls: `+scrollEchoTurns(3, true)+`.
3. Wait until Math.abs(gap) <= 1 is true. The button must not be visible (allow 5s).
4. Retry until the trap actually fires: eval await window.__springDeflationTrap() expecting true, allowing 15s (the eval runs again until it returns true). scrollToBottom's animation-frame pin targets the current bottom, so only the estimate-inflated follow write springs the trap.
5. Settled state: the clamp left us at the true bottom, so the button must not be visible (allow 5s).
6. Send "echo after deflation" and wait until a [data-testid="message"] element containing "echo after deflation" is visible (allow 30s).
7. Still following: wait until Math.abs(gap) <= 1 is true, and the button must not be visible (allow 5s).`, func(s *shelley) {
		s.initScript(scrollDeflationTrap)
		scrollEchoConversation(s)
	})
}

// Regression for a CI flake (builds 8584/8621/8634): the button latched on
// while the container was sitting at the bottom, so it never went away.
//
// handleScroll treated *any* upward scrollTop delta as "user scrolled up".
// Drops smaller than the bottom sentinel's 100px rootMargin leave the
// sentinel intersecting, and an IntersectionObserver only reports changes,
// so it had already said "at bottom" and would not fire again to correct
// the button. Such small clamps are routine rather than exotic:
// content-visibility:auto chunks swap estimated heights for real ones as
// they lay out, which nudges scrollTop down by a few pixels. On a loaded
// CI host that reordering is easy to hit; locally it usually isn't, which
// is what made this look flaky instead of broken.
//
// scrollToBottom's rAF pin re-pins scrollTop every frame for ~120 frames,
// which would paper over the clamp. The test retries the nudge until it
// survives two frames: that is precisely the point the pin has lapsed, so
// the scroll event is handled the way a mid-conversation clamp would be.
// (Polling on the synchronous gap instead would succeed on the very first
// try, while the pin was still active and suppressing the code path under
// test.)
const scrollNudgeUp = `window.__nudgeUp = async () => {
  const el = document.querySelector('.messages-container');
  el.scrollTop = el.scrollTop - 5;
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  return el.scrollHeight - el.clientHeight - el.scrollTop;
};`

func TestE2EScrollSubMarginClampKeepsButtonHidden(t *testing.T) {
	e2e(t, scrollTerms+scrollEchoSetup+`A sub-margin scrollTop clamp does not strand the scroll-to-bottom button. The page provides window.__nudgeUp(): it lowers the container's scrollTop by 5, waits two animation frames, and resolves to the gap.
1. Open the conversation.
2. Enough content that the list actually scrolls: `+scrollEchoTurns(3, true)+`.
3. Wait until Math.abs(gap) <= 1 is true. The button must not be visible (allow 5s).
4. Retry the nudge until it survives the bottom pin: eval (await window.__nudgeUp()) > 0 expecting true, allowing 15s (the eval runs again until it returns true).
5. The scroll event has been delivered by now, so this is a settled state: record in window.__buttonNow whether the button is visible right now (it exists, its getBoundingClientRect() has non-zero width and height, and its computed visibility is not "hidden"), in one eval without expect, then check that window.__buttonNow is false.
6. Send "echo after clamp" and wait until a [data-testid="message"] element containing "echo after clamp" is visible (allow 30s). The button must not be visible (allow 5s): still following.`, func(s *shelley) {
		s.initScript(scrollNudgeUp)
		scrollEchoConversation(s)
	})
}

// Regression: a tap that shrinks the list at the bottom stopped following.
// Clicking Send in an MCP App view's consent bar closes the bar, and the list
// clamps by its height. When that clamp's scroll event lands before the
// ResizeObserver report (a forced reflow does it), handleScroll reads a drop
// past the gesture threshold as a scroll-up, and the observer's undo refused
// because the click's pointerdown counted as a scroll gesture. A press that
// ends without scrolling no longer counts.
const scrollCollapsible = `window.__addCollapsible = async () => {
  const container = document.querySelector('.messages-container');
  const list = container.querySelector('.messages-list');
  const sentinel = container.querySelector('.messages-bottom-sentinel');
  if (!list || !sentinel) throw new Error('message list sentinel not found');
  const block = document.createElement('div');
  block.style.cssText = 'height: 400px; display: flex; align-items: flex-end;';
  const button = document.createElement('button');
  button.dataset.testid = 'collapse-test';
  button.textContent = 'Collapse';
  button.addEventListener('click', () => {
    block.remove();
    void container.scrollTop; // a forced reflow: the clamp's scroll event goes first
  });
  block.append(button);
  list.insertBefore(block, sentinel);
  // Let following carry the list down to the block.
  const gap = () => container.scrollHeight - container.clientHeight - container.scrollTop;
  for (let i = 0; i < 300 && Math.abs(gap()) > 1; i++) {
    await new Promise((resolve) => requestAnimationFrame(resolve));
  }
  return gap();
};`

func TestE2EScrollClickCollapsingListKeepsFollowing(t *testing.T) {
	scrollTapCollapsing(t, "A mouse click", `Click [data-testid="collapse-test"] with a click step (not an eval).`)
}

func TestE2EScrollTapCollapsingListKeepsFollowing(t *testing.T) {
	scrollTapCollapsing(t, "A native tap", `Tap [data-testid="collapse-test"] with native touch: a touch start step on it, then a touch end step (no click, no eval).`)
}

// A touch whose element leaves the document mid-gesture gets its touchend
// there, where it never reaches the list. It must still end the touch, or
// following stays off.
func TestE2EScrollTouchOnRemovedElementEnds(t *testing.T) {
	e2e(t, scrollTerms+scrollEchoSetup+`A touch whose element is removed while the finger is down still ends, so following continues. The page provides window.__addCollapsible(): it inserts a 400px block, with a "Collapse" button ([data-testid="collapse-test"]) at its bottom, before the container's bottom sentinel, waits for following to reach it, and resolves to the gap.
1. Open the conversation.
2. Enough content that the list actually scrolls: `+scrollEchoTurns(3, true)+`.
3. Wait until Math.abs(gap) <= 1 is true (an eval expecting true: the gap can be fractional). The button must not be visible (allow 5s).
4. Eval Math.abs(await window.__addCollapsible()) <= 1 expecting true.
5. A touch start step on [data-testid="collapse-test"] (native touch).
6. Eval document.querySelector('[data-testid="collapse-test"]').remove() without expect (only the button: the block keeps its height).
7. A touch end step.
8. Send "echo after removed touch" and wait until a [data-testid="message"] element containing "echo after removed touch" is visible (allow 30s).
9. Still following: wait until Math.abs(gap) <= 1 is true (allow 10s), and the button must not be visible (allow 5s).`, func(s *shelley) {
		s.initScript(scrollCollapsible)
		scrollEchoConversation(s)
	})
}

// Lifting one finger from a removed element does not end another finger's
// touch on the list.
func TestE2EScrollRemovedTouchKeepsOtherFinger(t *testing.T) {
	e2e(t, scrollTerms+scrollEchoSetup+`While a second finger is held on the list, lifting a first finger whose element was removed does not end the touch: content that grows below the held finger does not move the list.
1. Open the conversation.
2. Enough content that the list actually scrolls: `+scrollEchoTurns(3, true)+`.
3. Wait until Math.abs(gap) <= 1 is true (an eval expecting true: the gap can be fractional). The button must not be visible (allow 5s).
4. In one async eval without expect: create two empty divs, x and y, and insert both into the container's .messages-list before its .messages-bottom-sentinel; let a = new Touch({identifier: 1, target: x, clientX: 10, clientY: 10}) and b = new Touch({identifier: 2, target: y, clientX: 20, clientY: 20}); dispatch on x new TouchEvent("touchstart", {bubbles: true, touches: [a], targetTouches: [a], changedTouches: [a]}); dispatch on y new TouchEvent("touchstart", {bubbles: true, touches: [a, b], targetTouches: [b], changedTouches: [b]}); remove x; dispatch on x new TouchEvent("touchend", {bubbles: true, touches: [b], targetTouches: [], changedTouches: [a]}); await two animation frames; store the container's scrollTop in window.__topBefore; insert a div with style height 80px into the .messages-list before the .messages-bottom-sentinel; await three animation frames; store the container's scrollTop in window.__topAfter; then dispatch on y new TouchEvent("touchend", {bubbles: true, touches: [], targetTouches: [], changedTouches: [b]}).
5. Check that Math.abs(window.__topAfter - window.__topBefore) <= 1 is true.`, func(s *shelley) {
		scrollEchoConversation(s)
	})
}

// A mouse click on the list is no sign that a finger held there has lifted.
func TestE2EScrollMouseClickKeepsHeldTouch(t *testing.T) {
	e2e(t, scrollTerms+scrollEchoSetup+`While a finger is held on the list, a mouse click there does not end the touch: content that grows below the held finger does not move the list.
1. Open the conversation.
2. Enough content that the list actually scrolls: `+scrollEchoTurns(3, true)+`.
3. Wait until Math.abs(gap) <= 1 is true (an eval expecting true: the gap can be fractional). The button must not be visible (allow 5s).
4. In one async eval without expect, on the container: dispatch new Event("touchstart", {bubbles: true}) (a finger that stays down); dispatch new PointerEvent("pointerdown", {pointerId: 1, pointerType: "mouse", bubbles: true}), then new PointerEvent("pointerup", {pointerId: 1, pointerType: "mouse", bubbles: true}); the finger drags the list: lower scrollTop by 30 and dispatch new Event("scroll"); await two animation frames; store the container's scrollTop in window.__topBefore; insert a div with style height 80px into the container's .messages-list before its .messages-bottom-sentinel; await three animation frames; store the container's scrollTop in window.__topAfter; then dispatch new Event("touchend", {bubbles: true}).
5. Check that Math.abs(window.__topAfter - window.__topBefore) <= 1 is true.`, func(s *shelley) {
		scrollEchoConversation(s)
	})
}

// A touch's cancellation ends only a press the touch made.
func TestE2EScrollTouchCancelKeepsMouseClick(t *testing.T) {
	e2e(t, scrollTerms+scrollEchoSetup+`A touch cancelled during a mouse click does not keep that click from being a click: a click that shrinks the list at the bottom does not stop auto-follow. The page provides window.__addCollapsible(): it inserts a 400px block, with a "Collapse" button ([data-testid="collapse-test"]) at its bottom, before the container's bottom sentinel, waits for following to reach it, and resolves to the gap. Clicking the button removes the block.
1. Open the conversation.
2. Enough content that the list actually scrolls: `+scrollEchoTurns(3, true)+`.
3. Wait until Math.abs(gap) <= 1 is true. The button must not be visible (allow 5s).
4. Eval Math.abs(await window.__addCollapsible()) <= 1 expecting true.
5. In one async eval without expect, on the container: dispatch new Event("touchstart", {bubbles: true}); await new Promise((resolve) => setTimeout(resolve, 400)), so the touch is no longer a recent gesture; dispatch new PointerEvent("pointerdown", {pointerId: 1, pointerType: "mouse", bubbles: true}); dispatch new Event("touchcancel", {bubbles: true}); dispatch new PointerEvent("pointerup", {pointerId: 1, pointerType: "mouse", bubbles: true}); call document.querySelector('[data-testid="collapse-test"]').click(); await two animation frames; store in window.__buttonNow whether the button is visible (it exists, its getBoundingClientRect() has non-zero width and height, and its computed visibility is not "hidden").
6. Check that window.__buttonNow is false.
7. Send "echo after cancel" and wait until a [data-testid="message"] element containing "echo after cancel" is visible (allow 30s).
8. Still following: wait until Math.abs(gap) <= 1 is true (allow 10s), and the button must not be visible (allow 5s).`, func(s *shelley) {
		s.initScript(scrollCollapsible)
		scrollEchoConversation(s)
	})
}

// Only the pointer that pressed on the list ends its press: another
// pointer's release elsewhere must not make a drop during the press look
// like no one's.
func TestE2EScrollOtherPointerReleaseKeepsPress(t *testing.T) {
	e2e(t, scrollTerms+scrollEchoSetup+`While a mouse is pressed on the list, another pointer's release outside it does not end that press. The page provides window.__addCollapsible(): it inserts a 400px block, with a "Collapse" button ([data-testid="collapse-test"]) at its bottom, before the container's bottom sentinel, waits for following to reach it, and resolves to the gap. Clicking the button removes the block.
1. Open the conversation.
2. Enough content that the list actually scrolls: `+scrollEchoTurns(3, true)+`.
3. Wait until Math.abs(gap) <= 1 is true. The button must not be visible (allow 5s).
4. Eval Math.abs(await window.__addCollapsible()) <= 1 expecting true.
5. In one async eval without expect: dispatch new PointerEvent("pointerdown", {pointerId: 1, pointerType: "mouse", bubbles: true}) on the container; dispatch new PointerEvent("pointerup", {pointerId: 2, pointerType: "touch", bubbles: true}) on document.body; call document.querySelector('[data-testid="collapse-test"]').click(); await two animation frames; store in window.__buttonNow whether the button is visible (it exists, its getBoundingClientRect() has non-zero width and height, and its computed visibility is not "hidden"); then dispatch new PointerEvent("pointerup", {pointerId: 1, pointerType: "mouse", bubbles: true}) on the container.
6. Check that window.__buttonNow is true: the list dropped while the mouse was still pressed on it, so the drop was the reader's.`, func(s *shelley) {
		s.initScript(scrollCollapsible)
		scrollEchoConversation(s)
	})
}

func scrollTapCollapsing(t *testing.T, kind, press string) {
	e2e(t, scrollTerms+scrollEchoSetup+fmt.Sprintf(`%s that shrinks the list at the bottom does not stop auto-follow. The page provides window.__addCollapsible(): it inserts a 400px block, with a "Collapse" button ([data-testid="collapse-test"]) at its bottom, before the container's bottom sentinel, waits for following to reach it, and resolves to the gap. Clicking the button removes the block.
1. Open the conversation.
2. Enough content that the list actually scrolls: %s.
3. Wait until Math.abs(gap) <= 1 is true. The button must not be visible (allow 5s).
4. Eval Math.abs(await window.__addCollapsible()) <= 1 expecting true.
5. %s
6. Wait until document.querySelector('[data-testid="collapse-test"]') === null is true, then until Math.abs(gap) <= 1 is true. Then eval await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve(true)))) expecting true, so the scroll and resize callbacks have run.
7. Record in window.__buttonNow whether the button is visible right now (it exists, its getBoundingClientRect() has non-zero width and height, and its computed visibility is not "hidden"), in one eval without expect, then check that window.__buttonNow is false.
8. Send "echo after collapse" and wait until a [data-testid="message"] element containing "echo after collapse" is visible (allow 30s).
9. Still following: wait until Math.abs(gap) <= 1 is true (allow 10s), and the button must not be visible (allow 5s).`, kind, scrollEchoTurns(3, true), press), func(s *shelley) {
		s.initScript(scrollCollapsible)
		scrollEchoConversation(s)
	})
}

// Regression for the "chat jumps toward the top / stops following" bug
// (GitHub #245). Message rows are wrapped in .messages-chunk elements with
// content-visibility:auto and a contain-intrinsic-size estimate. Off-screen
// chunks report the *estimate* rather than their real height, so a reload
// computes an inflated scrollHeight. Persisting a numeric scrollTop then no
// longer lands at the bottom: the near-bottom check fails and auto-follow
// is silently disarmed. Firefox estimates more aggressively, but it
// reproduces on Chromium too.
//
// The fix persists a layout-free "at bottom" sentinel (from the bottom
// sentinel's IntersectionObserver) instead of a raw offset, so an
// at-bottom conversation re-pins to the real bottom on restore.
func TestE2EScrollReloadRestoresTrueBottom(t *testing.T) {
	e2e(t, scrollTerms+`The conversation restores to the true bottom after a reload despite content-visibility height estimates. The test seeds a conversation of 33 turns at /c/{{SLUG}}. "Open the conversation" means: navigate to /c/{{SLUG}}, wait for the message input ([data-testid="message-input"]) to be visible (allow 30s), then the container (allow 30s).
1. Open the conversation.
2. Confirm it actually built multiple content-visibility chunks, or the test would pass vacuously: wait until document.querySelectorAll(".messages-chunk").length >= 2 is true (allow 30s).
3. Go to the true bottom with the app's own scroll-to-bottom: in one eval, if the button is visible (it exists, its getBoundingClientRect() has non-zero width and height, and its computed visibility is not "hidden"), call its click(). Then the button must not be visible (allow 10s).
4. Reload the page, then wait for the message input, and then the container, to be visible (allow 30s each).
5. After restore we must be pinned at the bottom: the button must not be visible (allow 10s), and wait until the gap is below 120 (allow 10s).
6. A new message must still scroll into view (auto-follow armed): send "echo after reload" and wait until a [data-testid="message"] element containing "echo after reload" is visible (allow 30s). The button must not be visible (allow 5s), and wait until the gap is below 120 (allow 10s).`, func(s *shelley) {
		// Build enough turns to span several content-visibility chunks (~50
		// rows per chunk; each turn adds a user and an agent row),
		// serializing on each turn's completion: a chat posted while the
		// agent is still working on the previous one gets dropped.
		c := s.conversation("echo seed message", "")
		s.v("SLUG", c.Slug)
		for i := range 32 {
			s.sendChat(c.ID, fmt.Sprintf("echo bulk message number %d", i))
			s.waitTurns(c.ID, i+2)
		}
		// Narrow viewport => more wrapped rows => taller content => more chunks.
		s.viewport(480, 640)
	})
}

// scrollSavedBottom instruments TestE2EScrollSavedBottomRestoration. Each helper
// records what it returns in a window variable.
const scrollSavedBottom = `const settle = () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
const container = () => document.querySelector('.messages-container');
// A list shrink clamps scrollTop while leaving the bottom sentinel in view.
window.__clampAndSave = async (key) => {
  const element = container();
  const list = element.querySelector('.messages-list');
  const sentinel = element.querySelector('.messages-bottom-sentinel');
  if (!list || !sentinel) throw new Error('message list sentinel not found');
  const spacer = document.createElement('div');
  spacer.style.height = '600px';
  list.insertBefore(spacer, sentinel);
  await settle();
  element.scrollTop = element.scrollHeight;
  await settle();
  spacer.remove();
  // Force the clamp and its scroll handler before ResizeObserver can
  // explain the shrink, matching WebKit's event ordering.
  void element.scrollTop;
  element.dispatchEvent(new Event('scroll'));
  window.dispatchEvent(new Event('beforeunload'));
  const immediate = localStorage.getItem(key);
  await settle();
  window.dispatchEvent(new Event('beforeunload'));
  window.__afterClamp = JSON.stringify({ immediate, settled: localStorage.getItem(key) });
  return window.__afterClamp;
};
// A bare jump, as Find, accessibility or programmatic scrolling makes.
window.__bareJump = (key) => {
  const element = container();
  element.scrollTop = 0;
  element.dispatchEvent(new Event('scroll'));
  window.dispatchEvent(new Event('beforeunload'));
  window.__beforeConfirmation = localStorage.getItem(key);
  return window.__beforeConfirmation;
};
// An explicit upward wheel gesture, then a jump to the top.
window.__wheelUp = (key) => {
  const element = container();
  element.dispatchEvent(new WheelEvent('wheel', { deltaY: -200, bubbles: true }));
  window.dispatchEvent(new Event('beforeunload'));
  window.__afterWheel = localStorage.getItem(key);
  element.scrollTop = 0;
  return window.__afterWheel;
};
// What leaving the page now would save.
window.__saved = (key) => {
  window.dispatchEvent(new Event('beforeunload'));
  return localStorage.getItem(key);
};`

func TestE2EScrollSavedBottomRestoration(t *testing.T) {
	e2e(t, scrollTerms+`Saved-bottom restoration survives clamps but yields to gestures and bare navigation. The test seeds a long conversation with ID {{ID}}; its saved scroll position is localStorage item "shelley_scroll_{{ID}}", called KEY below (pass the string "shelley_scroll_{{ID}}" where KEY appears). The page provides helpers, each of which also stores its result in a window variable: window.__clampAndSave(KEY) shrinks the list by 600px so the browser clamps scrollTop with the bottom sentinel still in view, saves (a beforeunload event) at once and again two frames later, and stores a JSON string of both saved values {immediate, settled} in window.__afterClamp; window.__bareJump(KEY) sets scrollTop to 0 with a bare scroll event, saves at once, and stores the saved value in window.__beforeConfirmation; window.__wheelUp(KEY) dispatches an upward wheel event, saves at once, stores the saved value in window.__afterWheel and then sets scrollTop to 0; window.__saved(KEY) saves and returns the saved value. "Wait for the bottom sentinel" means: wait until document.querySelector(".messages-bottom-sentinel") !== null is true (allow 30s).
1. Navigate to /c/{{ID}} and wait for the container to be visible (allow 30s).
2. Eval localStorage.setItem(KEY, "bottom"), then reload the page. Wait for the button to be hidden (allow 10s) and for the bottom sentinel.
3. Saving during a startup-style clamp must keep the semantic bottom rather than persist the transient pixel offset: eval await window.__clampAndSave(KEY) without expect, then window.__afterClamp must be exactly {"immediate":"bottom","settled":"bottom"}.
4. A bare jump is provisional until the sentinel confirms it left the bottom: eval window.__bareJump(KEY) without expect, then window.__beforeConfirmation must be exactly "bottom". The button should become visible (allow 5s). Then wait until window.__saved(KEY) !== "bottom" is true.
5. Returning to the bottom and restoring again re-arms the guard, but an explicit upward wheel gesture cancels it immediately: click the button, eval localStorage.setItem(KEY, "bottom"), and reload the page. Wait for the button to be hidden (allow 10s) and for the bottom sentinel, then eval a promise that resolves after two animation frames.
6. Eval window.__wheelUp(KEY) without expect, then window.__afterWheel !== "bottom" must be true. The button should become visible (allow 5s).
7. Record window.__saved(KEY) in window.__final (one eval without expect), then window.__final !== "bottom" must be true.`, func(s *shelley) {
		s.initScript(scrollSavedBottom)
		scrollMediumConversation(s)
	})
}

func TestE2EScrollLeavingAfterScrollToBottomKeepsBottom(t *testing.T) {
	e2e(t, scrollTerms+scrollMediumSetup+`Leaving right after Scroll to bottom saves the bottom, not a pixel offset. The conversation's saved scroll position is localStorage item "shelley_scroll_{{ID}}"; "what leaving saves" is the value of that item right after window.dispatchEvent(new Event("beforeunload")).
1. Open the conversation.
2. In one eval, set the container's scrollTop to 0 and dispatch new Event("scroll") on it. The button should become visible.
3. Wait until what leaving saves is not "bottom": eval (window.dispatchEvent(new Event("beforeunload")), localStorage.getItem("shelley_scroll_{{ID}}")) !== "bottom", expecting true.
4. The click and the unload share a task, so no observer report can come in between: the click alone has to establish that this is the bottom. A pixel offset would not survive the reload (lazily laid-out history changes the height), and would leave follow disarmed. In one eval without expect: call click() on the button element, dispatch a beforeunload event on window, and store localStorage.getItem("shelley_scroll_{{ID}}") in window.__savedOnLeave.
5. window.__savedOnLeave must be exactly "bottom".`, func(s *shelley) {
		scrollMediumConversation(s)
	})
}

// The sub-margin clamp fix above made handleScroll defer to the bottom
// sentinel, which is right for clamps but must not swallow a real gesture.
// The IntersectionObserver is async, so a scroll event can arrive while
// sentinelAtBottom is still stale-true. handleScroll then does nothing, and
// the observer's own callback (which shows the button) does not arm
// userScrolled -- so auto-follow stayed on and the next list growth yanked
// the user back to the bottom.
//
// Scroll far enough to leave the 100px margin, then check both halves:
// the button appears, AND new content does not drag the viewport down.
func TestE2EScrollUnreportedScrollUpDisarms(t *testing.T) {
	e2e(t, scrollTerms+scrollEchoSetup+`A scroll-up the observer has not yet reported still disarms auto-follow.
1. Open the conversation.
2. `+strings.ToUpper(scrollEchoTurns(4, false)[:1])+scrollEchoTurns(4, false)[1:]+`.
3. The button must not be visible (allow 5s).
4. A genuine scroll to the very top, as a bare scrollTop write so no wheel or touch handler runs: eval setting the container's scrollTop to 0 (nothing else). The button should become visible (allow 5s).
5. Record the container's scrollTop in window.__before; window.__before < 50 must be true.
6. Grow the list; auto-follow must stay disarmed, the user is reading: send "echo message 5" and wait until some [data-testid="message"] element's text contains "echo message 5" (it need not be visible; allow 30s).
7. Wait until the container's scrollTop is below 50 (allow 3s). The button must be visible.`, scrollEchoConversation)
}

// Tighter version of the test above. There, the observer had already fired
// by the time content grew. Here the scroll and the growth land together,
// so the ResizeObserver's follow-the-bottom branch runs while both
// handleScroll (which defers to the observer's flag) and the observer
// itself are still behind -- and it re-pinned to the bottom, throwing the
// reader back down.
func TestE2EScrollScrollUpRacingGrowth(t *testing.T) {
	e2e(t, scrollTerms+scrollEchoSetup+`A scroll-up racing list growth in the same frame is not overridden.
1. Open the conversation.
2. `+strings.ToUpper(scrollEchoTurns(4, false)[:1])+scrollEchoTurns(4, false)[1:]+`.
3. The button must not be visible (allow 5s).
4. Scroll to the top and grow the list in the same task, so no observer callback can run in between, and deliberately with no wheel event (those handlers would hide the bug): in one eval, set the container's scrollTop to 0, then append to its ".messages-list" a div 800px tall with attribute data-test-filler="1".
5. The reader must stay where they scrolled to: wait until the container's scrollTop is below 50 (allow 3s). The button should be visible (allow 5s).`, scrollEchoConversation)
}

// scrollStream drives a real, open EventSource one chunk at a time,
// through globalStream and messageStore: no model timing, keyboard
// surrogate, or direct component calls. It answers the page's /api/stream2
// itself and sends events only on call steps.
type scrollStream struct {
	s    *shelley
	id   string
	rows []map[string]any
	mu   sync.Mutex
	seq  int
	conn chan string // the newest connection's events
}

// scrollStreamPage helps the streaming descriptions.
const scrollStreamPage = `window.__streamed = (text) => {
  const norm = (s) => s.replace(/\s+/g, ' ').trim();
  const els = document.querySelectorAll('.streaming-message');
  return els.length === 1 && norm(els[0].textContent).includes(norm(text));
};
window.__paragraphs = (prefix) => Array.from({ length: 12 }, (_, i) => prefix + ' paragraph ' + i + '.').join(' ');
window.__frames = () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve('framed'))));
window.__gap = () => {
  const el = document.querySelector('.messages-container');
  return el.scrollHeight - el.clientHeight - el.scrollTop;
};`

// scrollControlled seeds a short conversation as {{ID}} and answers the page's
// stream. Call "start" marks it working and sends "Stream ready."; call
// "finish" marks it idle.
func (s *shelley) scrollControlled() *scrollStream {
	c := s.loremipsum("5")
	s.v("ID", c.ID)
	var snapshot struct {
		Conversations []map[string]any `json:"conversations"`
	}
	s.api("GET", "/api/conversations/snapshot", nil, &snapshot)
	cs := &scrollStream{s: s, id: c.ID, rows: snapshot.Conversations}
	s.route("GET /api/stream2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		rc.Flush()
		events := make(chan string, 100)
		cs.mu.Lock()
		cs.conn = events
		cs.mu.Unlock()
		for {
			select {
			case e := <-events:
				fmt.Fprint(w, e)
				rc.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	s.initScript(scrollStreamPage)
	s.call("start", func() string {
		cs.working(true)
		cs.delta("text", "Stream ready.\n\n")
		return "started"
	})
	s.call("finish", func() string {
		cs.working(false)
		return "finished"
	})
	return cs
}

func (cs *scrollStream) send(event any) {
	var events chan string
	cs.s.poll(15*time.Second, func() error {
		cs.mu.Lock()
		defer cs.mu.Unlock()
		if events = cs.conn; events == nil {
			return fmt.Errorf("the page has not opened /api/stream2")
		}
		return nil
	})
	events <- "data: " + mustJSON(event) + "\n\n"
}

// working says the conversation is working, or idle, in a conversation
// list reset.
func (cs *scrollStream) working(v bool) {
	rows := make([]map[string]any, len(cs.rows))
	for i, row := range cs.rows {
		if row["conversation_id"] == cs.id {
			c := make(map[string]any, len(row)+2)
			for k, x := range row {
				c[k] = x
			}
			c["working"], c["agent_working"] = v, v
			row = c
		}
		rows[i] = row
	}
	cs.send(map[string]any{"conversation_list_patch": map[string]any{
		"reset":    true,
		"new_hash": fmt.Sprintf("controlled-%v", v),
		"at":       time.Now().UTC().Format(time.RFC3339Nano),
		"patch":    []any{map[string]any{"op": "replace", "path": "", "value": rows}},
	}})
}

// delta sends a streaming delta of kind "text" or "thinking".
func (cs *scrollStream) delta(kind, text string) {
	index := 1
	if kind == "thinking" {
		index = 0
	}
	cs.mu.Lock()
	seq := cs.seq
	cs.seq++
	cs.mu.Unlock()
	cs.send(map[string]any{
		"conversation_id": cs.id,
		"stream_delta":    map[string]any{"type": kind, "text": text, "index": index, "seq": seq},
	})
}

// chunk lets call step name send a delta.
func (cs *scrollStream) chunk(name, kind, text string) {
	cs.s.call(name, func() string {
		cs.delta(kind, text)
		return "sent"
	})
}

// scrollParagraphs is twelve paragraphs, "<prefix> paragraph <i>.".
func scrollParagraphs(prefix string) string {
	var b strings.Builder
	for i := range 12 {
		fmt.Fprintf(&b, "%s paragraph %d.\n\n", prefix, i)
	}
	return b.String()
}

// scrollStreamSetup describes scrollControlled and the start of every streaming test.
const scrollStreamSetup = `The test answers the page's /api/stream2 EventSource itself and sends stream events only on call steps, for a short seeded conversation with ID {{ID}}. "The container" is the messages container (".messages-container"), "the button" the scroll-to-bottom button (".scroll-to-bottom-button"), and "the gap" the value of window.__gap() (the container's scrollHeight - clientHeight - scrollTop). To "stream" a call with text T: run the call step, then wait until window.__streamed(T) is true (the one ".streaming-message" element's text contains T, with runs of whitespace taken as one space), then eval await window.__frames() (two animation frames, so the rendered chunk's resize and intersection callbacks run). "Following" means: wait until the gap is below 2, and the button is hidden. To "finish": call "finish" (it marks the conversation idle), then wait until ".streaming-message" is hidden. The touch helpers dispatch on the container: "touch down" means, in that order, new PointerEvent("pointerdown", {pointerType: "touch", bubbles: true}), new Event("touchstart", {bubbles: true}) and new PointerEvent("pointercancel", {pointerType: "touch", bubbles: true}) (native panning cancels the pointer, not the touch gesture); "dispatch a native E" (E being touchend or touchcancel) means dispatch new TouchEvent(E, {bubbles: true, cancelable: true, composed: true}). Check every assertion with an eval that has "expect" (or an assert_* or wait_* step): an eval without "expect" asserts nothing. Wait with such steps, not with polling loops inside the expression. When a step says to record something, do it in one eval without "expect" that stores it in the named window variable, then check it in a separate eval with "expect". "Unmoved from V" means: record in window.__unmoved whether Math.abs(the container's scrollTop - V) < 0.5, and window.__unmoved must be true. Perform the steps in order. Start like this:
1. Navigate to /c/{{ID}}; wait for the message input ([data-testid="message-input"]) to be visible (allow 30s), then the first [data-testid="message"] element (allow 30s).
2. Stream call "start" (it marks the conversation working and sends a text chunk) with text "Stream ready.".
3. Wait until the gap is below 2.
`

// scrollTouchUp has the description scroll up 300px within a touch gesture,
// recording the offset reached in window.__scrolled.
const scrollTouchUp = `In one eval: touch down; then lower the container's scrollTop by 300 and dispatch new Event("scroll") on it; store the container's scrollTop in window.__scrolled.`

// Mobile streaming scroll gestures: a 393x851 touch screen.
func scrollMobileStream(t *testing.T, description string, setup func(s *shelley, cs *scrollStream)) {
	e2e(t, scrollStreamSetup+description, func(s *shelley) {
		s.viewport(393, 851)
		cs := s.scrollControlled()
		setup(s, cs)
	})
}

// Normally the touch is released before the arrow is clicked; the
// "unreleased" case covers an interrupted gesture whose touchend never
// reached the container: explicit return must win. Reaching the bottom once
// is insufficient: the sentinel stops the RAF pin, and later chunks must
// still follow after that temporary pin ends.
func scrollArrowResumes(t *testing.T, end string) {
	release := "Dispatch a native " + end + "."
	if end == "unreleased" {
		release = "Do not end the touch: it is left unreleased."
	}
	scrollMobileStream(t, fmt.Sprintf(`The scroll-to-bottom arrow resumes following continuing chunks after %s.
4. %s
5. %s
6. The button should become visible.
7. Stream call "before arrow" with text "Before arrow.". The list must be unmoved from window.__scrolled.
8. Click the button. Following.
9. Stream call "continuing 0" with text window.__paragraphs("Continuing 0") (pass that expression, which builds the twelve paragraphs, to window.__streamed). Following.
10. Stream call "continuing 1" with text window.__paragraphs("Continuing 1"). Following.
11. Stream call "continuing 2" with text window.__paragraphs("Continuing 2"). Following.
12. Finish.`, end, scrollTouchUp, release), func(s *shelley, cs *scrollStream) {
		cs.chunk("before arrow", "text", "Before arrow.\n\n")
		for i := range 3 {
			cs.chunk(fmt.Sprintf("continuing %d", i), "text", scrollParagraphs(fmt.Sprintf("Continuing %d", i)))
		}
	})
}

func TestE2EScrollMobileArrowResumesAfterTouchend(t *testing.T) { scrollArrowResumes(t, "touchend") }

func TestE2EScrollMobileArrowResumesAfterTouchcancel(t *testing.T) {
	scrollArrowResumes(t, "touchcancel")
}

func TestE2EScrollMobileArrowResumesUnreleased(t *testing.T) { scrollArrowResumes(t, "unreleased") }

// With no arrow or shortcut, the scroll and the real sentinel intersection
// are delivered before more SSE data, including when touchend was lost. A
// late end event must not be needed to resume, nor undo the resume.
func scrollManualBottomResumes(t *testing.T, end string) {
	release, late := "Do not end the touch here.", ""
	switch end {
	case "touchend", "touchcancel":
		release = "Dispatch a native " + end + "."
	case "delayed touchend":
		late = " Then, late, dispatch a native touchend."
	}
	scrollMobileStream(t, fmt.Sprintf(`Returning to the bottom by hand resumes following continuing chunks after %s.
4. %s
5. The button should become visible.
6. Stream call "while away" with text "While manually scrolled away.". The list must be unmoved from window.__scrolled.
7. %s
8. In one eval that awaits: set the container's scrollTop to scrollHeight - clientHeight, dispatch new Event("scroll") on it, then await two animation frames.
9. Following.
10. Stream call "manual return 0" with text window.__paragraphs("Manual return 0") (pass that expression, which builds the twelve paragraphs, to window.__streamed). Following.%s
11. Stream call "manual return 1" with text window.__paragraphs("Manual return 1"). Following.
12. Stream call "manual return 2" with text window.__paragraphs("Manual return 2"). Following.
13. Finish.`, end, scrollTouchUp, release, late), func(s *shelley, cs *scrollStream) {
		cs.chunk("while away", "text", "While manually scrolled away.\n\n")
		for i := range 3 {
			cs.chunk(fmt.Sprintf("manual return %d", i), "text", scrollParagraphs(fmt.Sprintf("Manual return %d", i)))
		}
	})
}

func TestE2EScrollMobileManualBottomResumesAfterTouchend(t *testing.T) {
	scrollManualBottomResumes(t, "touchend")
}

func TestE2EScrollMobileManualBottomResumesAfterTouchcancel(t *testing.T) {
	scrollManualBottomResumes(t, "touchcancel")
}

func TestE2EScrollMobileManualBottomResumesAfterDelayedTouchend(t *testing.T) {
	scrollManualBottomResumes(t, "delayed touchend")
}

func TestE2EScrollMobileManualBottomResumesUnreleased(t *testing.T) {
	scrollManualBottomResumes(t, "unreleased")
}

// A second gesture must work after an explicit return to the bottom. The
// 20px scroll-up is below both the pin-release and sentinel margins. On the
// second gesture, the touch is released before the browser delivers the
// queued scroll event.
func scrollChunksRespectUp(t *testing.T, end string) {
	scrollMobileStream(t, scrollChunksRespectUpSteps(end), func(s *shelley, cs *scrollStream) {
		for g := range 2 {
			cs.chunk(fmt.Sprintf("held %d", g), "text", fmt.Sprintf("Held chunk %d.\n\n", g))
			cs.chunk(fmt.Sprintf("thinking %d", g), "thinking", fmt.Sprintf("Thinking %d. ", g))
			cs.chunk(fmt.Sprintf("after release %d", g), "text", fmt.Sprintf("After release %d.\n\n", g))
		}
	})
}

func scrollChunksRespectUpSteps(end string) string {
	var b strings.Builder
	n := 3
	step := func(format string, args ...any) {
		n++
		fmt.Fprintf(&b, "\n%d. "+format, append([]any{n}, args...)...)
	}
	for g := range 2 {
		event := "scroll"
		if g == 1 {
			event = end
		}
		step("Gesture %d: in one eval, touch down and store the container's scrollTop in window.__before.", g)
		step(`Stream call "held %d" with text "Held chunk %d.". The list must be unmoved from window.__before.`, g, g)
		step("In one eval: lower the container's scrollTop by 20, then dispatch new Event(%q, {bubbles: true}) on it.", event)
		step(`Stream call "thinking %d" (a thinking chunk) with text "Thinking %d.". The list must be unmoved from window.__before - 20.`, g, g)
		if g == 0 {
			step("Dispatch a native %s.", end)
		}
		step(`Stream call "after release %d" with text "After release %d.". The list must be unmoved from window.__before - 20.`, g, g)
		step("Press ArrowDown with the Control modifier (a press_key step alone: do not click or tap anything first, a tap being a touch gesture of its own), and wait until window.__gap() < 2 is true.")
	}
	step("Finish.")
	return fmt.Sprintf("Incoming chunks respect a scroll-up made through %s.%s", end, b.String())
}

func TestE2EScrollMobileChunksRespectScrollUpThroughTouchend(t *testing.T) {
	scrollChunksRespectUp(t, "touchend")
}

func TestE2EScrollMobileChunksRespectScrollUpThroughTouchcancel(t *testing.T) {
	scrollChunksRespectUp(t, "touchcancel")
}

// The held chunk crosses the sentinel margin as well as growing the list:
// neither observer may re-pin or mistake growth for a genuine user
// scroll-up. Then no new chunk, DOM growth, or keyboard action can rescue a
// missing resume.
func scrollResumesDeferredFollow(t *testing.T, end string) {
	scrollMobileStream(t, fmt.Sprintf(`Follow deferred by a touch resumes on %s, without another chunk.
4. In one eval, touch down and store the container's scrollTop in window.__before.
5. Stream call "held" with text window.__paragraphs("Held") (pass that expression, which builds the twelve paragraphs, to window.__streamed).
6. Record in window.__held JSON.stringify({unmoved: Math.abs(scrollTop - window.__before) < 0.5, gap: gap > 100}) from the container's scrollTop and the gap; it must be exactly {"unmoved":true,"gap":true}.
7. Dispatch a native %s.
8. Wait until the gap is below 2.
9. Finish.`, end, end), func(s *shelley, cs *scrollStream) {
		cs.chunk("held", "text", scrollParagraphs("Held"))
	})
}

func TestE2EScrollMobileResumesDeferredFollowOnTouchend(t *testing.T) {
	scrollResumesDeferredFollow(t, "touchend")
}

func TestE2EScrollMobileResumesDeferredFollowOnTouchcancel(t *testing.T) {
	scrollResumesDeferredFollow(t, "touchcancel")
}

// IntersectionObserver reports arrive in a task some time after the frame
// that computed them, and are computed a frame after the scroll they
// describe. On a busy device a touch can land in between, so a report of a
// return to the bottom made *before* the finger came down arrives while it
// is down. These tests produce those orders on purpose: holding reports
// back, or touching in the same task as the scroll.
const scrollLateReports = `const Native = window.IntersectionObserver;
const held = [];
let holding = false;
let heldSentinel = 0;
const deliver = ({ callback, observer, entries }) => {
  const sentinel = entries.findLast((e) => e.target.matches('.messages-bottom-sentinel'));
  if (sentinel) window.sentinelNear = sentinel.isIntersecting;
  callback(entries, observer);
};
Object.assign(window, {
  holdIntersections: () => (holding = true),
  heldIntersections: () => heldSentinel,
  // Batched, as a browser delivers reports that piled up: one callback per
  // observer with every held entry, oldest first.
  releaseIntersections: (batched = false) => {
    holding = false;
    heldSentinel = 0;
    const batches = held.splice(0);
    if (!batched) return batches.forEach(deliver);
    const merged = new Map();
    for (const b of batches) {
      const m = merged.get(b.callback);
      if (m) m.entries.push(...b.entries);
      else merged.set(b.callback, { ...b, entries: [...b.entries] });
    }
    merged.forEach(deliver);
  },
});
window.IntersectionObserver = class extends Native {
  constructor(callback, options) {
    super((entries, observer) => {
      const batch = { callback, observer, entries };
      if (!holding) return deliver(batch);
      if (entries.some((e) => e.target.matches('.messages-bottom-sentinel'))) heldSentinel++;
      held.push(batch);
    }, options);
  }
};
// A touch as the page sees one: native panning cancels the pointer, not the
// touch gesture.
window.__touch = (type) => {
  const el = document.querySelector('.messages-container');
  if (type === 'touchstart') {
    el.dispatchEvent(new PointerEvent('pointerdown', { pointerType: 'touch', bubbles: true }));
  }
  el.dispatchEvent(new Event(type, { bubbles: true }));
  if (type === 'touchstart') {
    el.dispatchEvent(new PointerEvent('pointercancel', { pointerType: 'touch', bubbles: true }));
  }
  return type;
};
window.__scrollBy = (delta) => {
  const el = document.querySelector('.messages-container');
  el.scrollTop += delta;
  el.dispatchEvent(new Event('scroll', { bubbles: true }));
  return el.scrollTop;
};`

// scrollLateSetup describes scrollLateReports.
const scrollLateSetup = `The page holds back IntersectionObserver reports on request: window.holdIntersections() starts holding them, window.heldIntersections() is how many held reports include the bottom sentinel, window.releaseIntersections() delivers the held reports one by one and window.releaseIntersections(true) delivers them batched (one callback per observer with every held entry, oldest first, as a browser delivers reports that piled up). window.sentinelNear is whether the page has been told the bottom sentinel is near (held reports don't count until released). "Wait for held reports" means wait until window.heldIntersections() > 0 is true. window.__touch("touchstart") and window.__touch("touchend") make a touch as the page sees one, on the container; window.__scrollBy(D) adds D to the container's scrollTop and dispatches a scroll event. "Scroll away" means: eval window.__touch("touchstart"), eval window.__scrollBy(-400), eval window.__touch("touchend"), the button should become visible, then wait until window.sentinelNear is false (scrolled away by a finished touch, and told so, so the next return crosses into the near-bottom zone and is reported). `

func scrollLateReport(t *testing.T, description string, setup func(cs *scrollStream)) {
	e2e(t, scrollStreamSetup+scrollLateSetup+description, func(s *shelley) {
		s.initScript(scrollLateReports)
		setup(s.scrollControlled())
	})
}

func TestE2EScrollLateReportTouchAfterObserved(t *testing.T) {
	scrollLateReport(t, `A late near-bottom report does not end a touch that began after it was observed.
4. Scroll away.
5. The reader comes back down and, before that is reported, touches again: eval window.holdIntersections(); eval setting the container's scrollTop to its scrollHeight; wait for held reports; eval window.__touch("touchstart"); eval window.releaseIntersections().
6. The finger is still down, so growth must not move the list under it: record the container's scrollTop in window.__before, then stream call "held" with text "Held under the finger."; the list must be unmoved from window.__before.
7. Lifting it at the bottom resumes following: eval window.__touch("touchend"), stream call "after lifting" with text "After lifting.", and wait until the gap is below 2.
8. Finish.`, func(cs *scrollStream) {
		cs.chunk("held", "text", "Held under the finger.\n\n")
		cs.chunk("after lifting", "text", "After lifting.\n\n")
	})
}

func TestE2EScrollLateReportReturnBeforeTouch(t *testing.T) {
	scrollLateReport(t, `A late near-bottom report does not end a touch observed after it began, for a return made before it.
4. Scroll away.
5. Back to the bottom and a touch in one task, so no frame comes between and the return is observed only once the finger is down: in one eval, set the container's scrollTop to its scrollHeight, touch down (pointerdown, touchstart, pointercancel as defined above, not window.__touch), and store the container's scrollTop in window.__before.
6. The button should become hidden.
7. Stream call "held" with text "Held under the finger."; the list must be unmoved from window.__before.
8. Eval window.__touch("touchend"), stream call "after lifting" with text "After lifting.", and wait until the gap is below 2.
9. Finish.`, func(cs *scrollStream) {
		cs.chunk("held", "text", "Held under the finger.\n\n")
		cs.chunk("after lifting", "text", "After lifting.\n\n")
	})
}

func TestE2EScrollLateReportTouchReturnedWithoutTouchend(t *testing.T) {
	scrollLateReport(t, `A touch that came back to where it began ends without its touchend: up and away, then back to the very offset the touch found. The touch made this return, so its report resumes following even if the end event never comes.
4. Eval window.__touch("touchstart"), then record the container's scrollTop in window.__start.
5. Eval window.__scrollBy(-300); wait until window.sentinelNear is false.
6. Eval window.__scrollBy(300); record in window.__back whether the container's scrollTop === window.__start, and window.__back must be true. Then wait until window.sentinelNear is true.
7. Stream call "followed" with text window.__paragraphs("Followed") (pass that expression, which builds the twelve paragraphs, to window.__streamed), and wait until the gap is below 2.
8. Finish.`, func(cs *scrollStream) {
		cs.chunk("followed", "text", scrollParagraphs("Followed"))
	})
}

func TestE2EScrollLateReportNewestOfBatch(t *testing.T) {
	scrollLateReport(t, `The page goes by the newest of reports delivered together: away and back within one touch, both reported late and together.
4. Eval window.holdIntersections(); eval window.__touch("touchstart"); eval window.__scrollBy(-400); wait for held reports.
5. Eval window.__scrollBy(400); wait until window.heldIntersections() > 1 is true.
6. Eval window.releaseIntersections(true), then eval window.__touch("touchend").
7. The button should become hidden.
8. Stream call "followed" with text window.__paragraphs("Followed") (pass that expression, which builds the twelve paragraphs, to window.__streamed), and wait until the gap is below 2.
9. Finish.`, func(cs *scrollStream) {
		cs.chunk("followed", "text", scrollParagraphs("Followed"))
	})
}

func TestE2EScrollLateReportKeepsLaterScrollUp(t *testing.T) {
	scrollLateReport(t, `A late near-bottom report does not undo a scroll-up the touch made after it was observed.
4. Scroll away.
5. The reader comes back down and, before that is reported, touches again and drags up and away: eval window.holdIntersections(); eval setting the container's scrollTop to its scrollHeight; wait for held reports; eval window.__touch("touchstart"); eval window.__scrollBy(-400); record the container's scrollTop in window.__away; eval window.releaseIntersections().
6. Lifting the finger there leaves the list there, through more output: eval window.__touch("touchend"), then stream call "after lifting" with text "After lifting."; the list must be unmoved from window.__away. The button must be visible.
7. Finish.`, func(cs *scrollStream) {
		cs.chunk("after lifting", "text", "After lifting.\n\n")
	})
}

// Desktop streaming scroll behavior.
func scrollDesktopStream(t *testing.T, description string, setup func(cs *scrollStream)) {
	e2e(t, scrollStreamSetup+description, func(s *shelley) {
		s.device(desktop)
		setup(s.scrollControlled())
	})
}

func TestE2EScrollStreamedCodeStaysPlain(t *testing.T) {
	scrollDesktopStream(t, `Streamed fenced code stays plain until the durable message renders.
4. Wait until the last ".message-agent pre > code" element contains a ".shelley-code-token" element (allow 30s): durable messages highlight their code.
5. Stream call "code" with text "const values = Array.from({ length: 200 }, (_, index) => index);" (the call sends that line as a fenced typescript block).
6. There must be exactly 1 ".streaming-message pre > code" element; it must not have a data-shelley-code-highlight attribute; and ".streaming-message pre > code .shelley-code-token" must match exactly 0 elements.
7. Finish.`, func(cs *scrollStream) {
		cs.chunk("code", "text", "```typescript\nconst values = Array.from({ length: 200 }, (_, index) => index);\n```\n")
	})
}

// Mouse pointerdown only stops the current pin; unlike touch, it must not
// pause follow writes from incoming chunks while the pointer stays down.
// The existing wheel release / scrollPointerActive recognition must keep
// small upward movements from being undone by the next streaming chunk.
func TestE2EScrollMouseHeldStreamingFollows(t *testing.T) {
	scrollDesktopStream(t, `Mouse-held streaming still follows; wheel and pointer scroll-ups still disarm.
4. In one eval: dispatch new PointerEvent("pointerdown", {pointerType: "mouse", bubbles: true}) on the container and store its scrollTop in window.__before.
5. Stream call "mouse held" with text "Mouse-held chunk.". Wait until the gap is below 2. Record in window.__followed whether the container's scrollTop > window.__before; it must be true.
6. Dispatch new PointerEvent("pointerup", {bubbles: true, cancelable: true, composed: true}) on the container.
7. Press ArrowDown with the Control modifier (a press_key step alone, with no click before it), and wait until the gap is below 2.
8. In one eval on the container: dispatch new WheelEvent("wheel", {deltaY: -20, bubbles: true}); lower scrollTop by 20; dispatch new Event("scroll"); dispatch new PointerEvent("pointerup", {pointerType: "mouse", bubbles: true}); store scrollTop in window.__scrolled.
9. Stream call "after wheel" with text "After wheel scroll-up."; the list must be unmoved from window.__scrolled.
10. Press ArrowDown with the Control modifier (a press_key step alone, with no click before it), and wait until the gap is below 2.
11. In one eval on the container: dispatch new PointerEvent("pointerdown", {pointerType: "mouse", bubbles: true}); lower scrollTop by 20; dispatch new Event("scroll"); dispatch new PointerEvent("pointerup", {pointerType: "mouse", bubbles: true}); store scrollTop in window.__scrolled.
12. Stream call "after pointer" with text "After pointer scroll-up."; the list must be unmoved from window.__scrolled.
13. Finish.`, func(cs *scrollStream) {
		cs.chunk("mouse held", "text", "Mouse-held chunk.\n\n")
		cs.chunk("after wheel", "text", "After wheel scroll-up.\n\n")
		cs.chunk("after pointer", "text", "After pointer scroll-up.\n\n")
	})
}

// Desktop keeps the conversation drawer visible, which makes this a direct
// regression test for the row-click behavior rather than a mobile drawer
// test.
func TestE2EScrollDrawerOpensReadConversationAtBottom(t *testing.T) {
	e2e(t, scrollTerms+`Selecting a previously read conversation in the drawer opens it at the bottom. The test seeds three conversations, with IDs {{FIRST}} and {{SECOND}} (long) and {{THIRD}} (shorter). "The row" of a conversation X is ".conversation-item[data-conversation-id=\"X\"]" in the drawer, which stays visible on this desktop window. A conversation's saved scroll position is localStorage item "shelley_scroll_<its ID>". "Many messages" means: wait until document.querySelectorAll('[data-testid="message"]').length > 100 is true. Once call "hold third" has run, the server's answer to the page's load of {{THIRD}} (a GET of /api/conversation/{{THIRD}} with no query) is held back until call "release third"; call "third requested" says whether such a load has arrived ("true" or "false").
1. Navigate to /c/{{FIRST}}; wait for the message input ([data-testid="message-input"]) to be visible (allow 30s). Many messages.
2. Eval setting the container's scrollTop to 0. The button should become visible (allow 10s). Wait until localStorage.getItem("shelley_scroll_{{FIRST}}") is exactly "0".
3. Click {{SECOND}}'s row. Wait until new RegExp("/c/[^/]*{{SECOND}}").test(location.href) is true (allow 30s). Many messages.
4. Click {{FIRST}}'s row. Wait until new RegExp("/c/[^/]*{{FIRST}}").test(location.href) is true (allow 30s). Many messages.
5. Wait until the gap is below 120 (allow 10s). The button must not be visible (allow 10s). Wait until localStorage.getItem("shelley_scroll_{{FIRST}}") is exactly "bottom".
6. Explicit in-conversation navigation must release the open-at-bottom follow state rather than be snapped back by late layout protection: click the table of contents button (".toc-button"), then ".toc-entry-top". Wait until the container's scrollTop is below 50 (allow 10s). The button should become visible (allow 10s).
7. Clicking the already-active conversation is also a selection: it should return to the latest content without leaving pending scroll state behind. Click {{FIRST}}'s row. Wait until the gap is below 120 (allow 10s). The button must not be visible (allow 10s).
8. Browser find and accessibility jumps carry no wheel or pointer markers; once the selected conversation reaches the bottom, a raw upward scroll must be treated as navigation rather than late layout growth. Eval setting the container's scrollTop to 0 (nothing else). Wait until the container's scrollTop is below 50 (allow 10s). The button should become visible (allow 10s). Click {{FIRST}}'s row. The button must not be visible (allow 10s).
9. Reselecting while a saved-position load is still pending must replace that restoration with the explicit bottom request. Eval localStorage.setItem("shelley_scroll_{{THIRD}}", "1000"). Call "hold third". Navigate to /c/{{THIRD}}. Call "third requested" until it returns "true". Click {{THIRD}}'s row. Call "release third".
10. Wait until document.querySelectorAll('[data-testid="message"]').length > 20 is true. Wait until the gap is below 120 (allow 10s).`, func(s *shelley) {
		s.device(desktop)
		first, second, third := s.loremipsum("medium"), s.loremipsum("medium"), s.loremipsum("15")
		s.v("FIRST", first.ID)
		s.v("SECOND", second.ID)
		s.v("THIRD", third.ID)
		var armed atomic.Bool
		seen := s.signal("third requested")
		held := s.gate("release third")
		s.route("GET /api/conversation/"+third.ID, func(w http.ResponseWriter, r *http.Request) {
			if armed.Load() && r.URL.RawQuery == "" {
				seen.Store(true)
				if !held.wait(r) {
					return
				}
			}
			s.pass.ServeHTTP(w, r)
		})
		s.call("hold third", func() string {
			armed.Store(true)
			return "holding"
		})
	})
}

// scrollPageKeys has a description work on a desktop window and keeps
// content-visibility estimates from changing scrollTop underneath the
// keypress: these tests are about routing the key, not lazy-layout
// anchoring.
const scrollPageKeys = `"Settle the layout" means: in one eval, append to document.head a <style> element with the text ".messages-chunk { content-visibility: visible !important; contain-intrinsic-size: none !important; }"; then eval a promise that resolves after two animation frames. `

// scrollPinStopped waits out scrollToBottom's animation-frame pin: it moves 1px
// above the bottom, waits two frames, and says whether the pin left it
// there, then returns to the bottom.
const scrollPinStopped = `window.__pinStopped = async () => {
  const el = document.querySelector('.messages-container');
  const bottom = el.scrollHeight - el.clientHeight;
  el.scrollTop = bottom - 1;
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  const pinStopped = el.scrollTop < bottom;
  el.scrollTop = el.scrollHeight;
  return pinStopped;
};`

// Desktop window: PageUp/PageDown are reading controls for the
// conversation, even though the composer deliberately keeps keyboard focus
// for fast replies.
func TestE2EScrollPageKeysKeepComposerFocus(t *testing.T) {
	e2e(t, scrollTerms+scrollMediumSetup+scrollPageKeys+`PageUp and PageDown scroll the conversation while the composer stays focused. "The input" is [data-testid="message-input"]; "the input is focused" means document.activeElement === the input is true.
1. Open the conversation, then settle the layout.
2. Fill the input with "keep this draft in the composer". Eval setting the input's selectionStart and selectionEnd to its value.length. Eval setting the container's scrollTop to its scrollHeight. Wait until the input is focused.
3. Record the container's scrollTop in window.__bottom. Press PageUp. Wait until the container's scrollTop < window.__bottom - 100 is true (allow 5s). Wait until the input is focused. Record the input's selectionEnd in window.__selectionEnd; it must be exactly 31 (the draft's length).
4. Record the container's scrollTop in window.__pageUp. Press PageDown. Wait until the container's scrollTop > window.__pageUp + 100 is true (allow 5s). Wait until the input is focused. The input's value must be exactly "keep this draft in the composer".`, func(s *shelley) {
		s.device(desktop)
		scrollMediumConversation(s)
	})
}

// scrollKeyThenGrow dispatches a page key on the composer and grows the list in
// the same task, recording the scrollTop before and right after the key.
const scrollKeyThenGrow = `window.__keyThenGrow = (key) => {
  const input = document.querySelector('[data-testid="message-input"]');
  const container = document.querySelector('.messages-container');
  const list = container?.querySelector('.messages-list');
  const sentinel = container?.querySelector('.messages-bottom-sentinel');
  if (!container || !list || !sentinel) throw new Error('message scroll elements not found');
  const before = container.scrollTop;
  input.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }));
  const afterKey = container.scrollTop;
  const filler = document.createElement('div');
  filler.style.height = '800px';
  list.insertBefore(filler, sentinel);
  window.__positions = { before, afterKey };
  return JSON.stringify(window.__positions);
};`

// Force the ResizeObserver growth callback to beat the scroll listener,
// reproducing the streaming race this test protects against.
const scrollDelayedListeners = `const addEventListener = EventTarget.prototype.addEventListener;
EventTarget.prototype.addEventListener = function (type, listener, options) {
  if (type === 'scroll' && this instanceof HTMLElement && this.classList.contains('messages-container') && listener) {
    const delayed = (event) => {
      requestAnimationFrame(() => requestAnimationFrame(() => {
        if (typeof listener === 'function') listener.call(this, event);
        else listener.handleEvent(event);
      }));
    };
    return addEventListener.call(this, type, delayed, options);
  }
  return addEventListener.call(this, type, listener, options);
};`

const scrollPinSteps = `Eval document.querySelector('[data-testid="message-input"]').focus(). Wait out the bottom pin: eval await window.__pinStopped() expecting true, allowing 5s (the eval runs again until it returns true).`

func TestE2EScrollPageUpDisarmsBeforeGrowth(t *testing.T) {
	e2e(t, scrollTerms+scrollMediumSetup+scrollPageKeys+`PageUp disarms auto-follow before same-frame conversation growth. The page delivers the container's scroll events to the app two animation frames late. window.__pinStopped() moves the container 1px above the bottom, waits two frames, and returns whether it stayed there (the bottom pin has lapsed), then returns to the bottom. window.__keyThenGrow(key) dispatches new KeyboardEvent("keydown", {key, bubbles: true}) on the message input and, in the same task, inserts an 800px div before the bottom sentinel; it records {before, afterKey}, the container's scrollTop before and right after the key, in window.__positions.
1. Open the conversation, then settle the layout.
2. `+scrollPinSteps+`
3. Eval window.__keyThenGrow("PageUp") without expect. window.__positions.afterKey < window.__positions.before - 100 must be true.
4. Wait until the gap is greater than 500 (allow 5s). The button should become visible (allow 5s).`, func(s *shelley) {
		s.device(desktop)
		s.initScript(scrollDelayedListeners)
		s.initScript(scrollPinStopped)
		s.initScript(scrollKeyThenGrow)
		scrollMediumConversation(s)
	})
}

func TestE2EScrollPageDownRearmsBeforeGrowth(t *testing.T) {
	e2e(t, scrollTerms+scrollMediumSetup+scrollPageKeys+`PageDown rearms auto-follow before same-frame growth at the bottom. window.__pinStopped() moves the container 1px above the bottom, waits two frames, and returns whether it stayed there (the bottom pin has lapsed), then returns to the bottom. window.__keyThenGrow(key) dispatches new KeyboardEvent("keydown", {key, bubbles: true}) on the message input and, in the same task, inserts an 800px div before the bottom sentinel.
1. Open the conversation, then settle the layout.
2. `+scrollPinSteps+`
3. Eval setting the container's scrollTop to scrollHeight - clientHeight - clientHeight / 2. The button should become visible (allow 5s).
4. Eval window.__keyThenGrow("PageDown").
5. Wait until the gap is below 100 (allow 5s). The button must not be visible (allow 5s).`, func(s *shelley) {
		s.device(desktop)
		s.initScript(scrollPinStopped)
		s.initScript(scrollKeyThenGrow)
		scrollMediumConversation(s)
	})
}

func TestE2EScrollPageKeysLeftToIME(t *testing.T) {
	e2e(t, scrollTerms+`Page keys typed while composing belong to the IME. The test seeds a conversation at /c/{{SLUG}}.
1. Navigate to /c/{{SLUG}} and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. In one eval without expect: dispatch on the message input const event = new KeyboardEvent("keydown", {key: "PageUp", bubbles: true, cancelable: true, isComposing: true}), and store event.defaultPrevented in window.__prevented.
3. window.__prevented must be false.`, func(s *shelley) {
		s.device(desktop)
		s.v("SLUG", s.conversation("echo page key IME", "").Slug)
	})
}

func TestE2EScrollModifiedPageKeysEditComposer(t *testing.T) {
	e2e(t, scrollTerms+scrollMediumSetup+`Modified page keys stay composer editing commands. "The input" is [data-testid="message-input"].
1. Open the conversation.
2. Fill the input with twenty lines, "draft line 0" to "draft line 19", joined by newlines (no trailing newline). Eval setting the input's selectionStart and selectionEnd to its value.length. Eval setting the container's scrollTop to 0.
3. Press PageUp with the Shift modifier.
4. Record the container's scrollTop in window.__top right away; window.__top must be exactly 0.
5. Wait until the input's selectionStart < 269 (the draft's length) is true. Record the input's selectionEnd in window.__selectionEnd; it must be exactly 269.`, func(s *shelley) {
		s.device(desktop)
		scrollMediumConversation(s)
	})
}

// scrollShortcut sets up the Cmd/Ctrl+ArrowDown tests: a desktop window, where
// the drawer is rendered alongside the chat and the shortcut is a
// desktop-only affordance anyway, and a long conversation.
func scrollShortcut(t *testing.T, description string, setup func(s *shelley)) {
	e2e(t, scrollTerms+scrollMediumSetup+`"Scroll up" means: eval setting the container's scrollTop to 0; the button should become visible (allow 10s). "Still up" means: record the container's scrollTop in window.__top, then window.__top < 50 must be true. Focus matters here: press keys with press_key steps alone, and add no clicks the steps do not ask for. `+description, func(s *shelley) {
		s.device(desktop)
		scrollMediumConversation(s)
		if setup != nil {
			setup(s)
		}
	})
}

// scrollOpenWorkspaceTool describes opening a workspace tool from the chat
// header, as e2e/helpers.ts's openWorkspaceTool did.
func scrollOpenWorkspaceTool(name string) string {
	return fmt.Sprintf(`open the %q workspace tool: click the button in ".chat-workspace-actions" whose aria-label is exactly %q if it is visible, or else click the "More options" button (aria-label "More options") and then the button in ".chat-overflow-popover" whose text starts with %q`, name, name, name)
}

// Regression: selecting a conversation in the drawer auto-focuses the
// composer, and the shortcut used to bail out on any focused textarea. The
// keypress then went nowhere -- exactly the state a reader is in right
// after switching conversations.
func TestE2EScrollShortcutAfterDrawerSwitch(t *testing.T) {
	scrollShortcut(t, `Cmd/Ctrl+ArrowDown scrolls to the bottom after switching conversations in the drawer. The test also seeds a second long conversation, {{SECOND}}.
1. Open the conversation.
2. Click ".conversation-item[data-conversation-id=\"{{SECOND}}\"]" in the drawer. The first conversation's messages stay mounted briefly during the switch, and the second's own load-time autoscroll could land after the manual scroll below, so wait until new RegExp("/c/[^/]*{{SECOND}}").test(location.href) is true (allow 30s), then for the first [data-testid="message"] to be visible (allow 30s).
3. The precondition the fix hinges on: focus is in the composer, not the list. Wait until document.activeElement?.tagName is exactly "TEXTAREA" (allow 10s).
4. In one eval on the container: dispatch new WheelEvent("wheel", {deltaY: -200, bubbles: true}), then set scrollTop to 0. The button should become visible (allow 10s).
5. Press ArrowDown with the Control modifier.
6. Wait until the gap <= 2 is true (allow 10s). The button must not be visible (allow 10s).`, func(s *shelley) {
		s.v("SECOND", s.loremipsum("medium").ID)
	})
}

// The composer only yields the shortcut when the caret has nowhere further
// to go; mid-draft, the native "move down/to end" gesture wins. Asserting
// the caret actually moved doubles as a positive control: without it, this
// test would also pass if the shortcut had been deleted outright rather
// than merely yielding here. Where it lands is platform-specific
// (Linux/Windows move down a line, macOS jumps to the end), so it only
// asserts that it left the first line.
func TestE2EScrollShortcutLeavesCaretWithTextBelow(t *testing.T) {
	scrollShortcut(t, `Cmd/Ctrl+ArrowDown leaves the caret alone when the composer has text below it.
1. Open the conversation.
2. Fill the message input with "first line\nsecond line" (two lines). Eval setting its selectionStart and selectionEnd to 0.
3. Scroll up.
4. Press ArrowDown with the Control modifier.
5. Wait until the message input's selectionEnd >= 11 is true (allow 5s): the caret left the first line.
6. Still up.`, nil)
}

// ArrowDown is list navigation in the drawer's single-line inputs, so they
// keep the key even though no caret moves vertically.
func TestE2EScrollShortcutLeavesDrawerSearch(t *testing.T) {
	scrollShortcut(t, `Cmd/Ctrl+ArrowDown leaves the drawer search box alone.
1. Open the conversation.
2. Scroll up.
3. Click the element with aria-label "Search conversations" inside ".drawer-header-actions". Wait until document.activeElement matches ".drawer-search-input" (allow 10s).
4. Press ArrowDown with the Control modifier, then sleep 300ms.
5. Still up.`, nil)
}

// The slash-command menu steps its selection with ArrowDown regardless of
// modifiers. It handles the key on the textarea itself, so this shortcut
// must not also fire and yank the reader to the bottom. The menu consuming
// the key and advancing its selection is a positive signal, so this doesn't
// green merely because the keypress went unprocessed.
func TestE2EScrollShortcutLeavesSlashMenu(t *testing.T) {
	scrollShortcut(t, `Cmd/Ctrl+ArrowDown leaves the composer's slash menu alone.
1. Open the conversation.
2. Click the message input and type "/". The slash command menu ([data-testid="slash-command-menu"]) should become visible (allow 10s).
3. Scroll up.
4. Press ArrowDown with the Control modifier.
5. Wait until the second [role="option"] element in the menu, document.querySelectorAll('[data-testid="slash-command-menu"] [role="option"]')[1], has aria-selected "true" (allow 5s).
6. Still up.`, nil)
}

// Regression: xterm consumes almost every key, but deliberately passes
// ArrowDown+Meta through (macOS reserves the chord). It arrives with the
// terminal's empty input textarea as the target, and the terminal dock sits
// below the message list rather than over it, so neither the textarea rule
// nor the cover check caught it -- Cmd+Down at a shell prompt scrolled the
// conversation behind the dock. Meta specifically: Ctrl+ArrowDown is
// consumed by xterm itself, so it would pass even with the bug present.
func TestE2EScrollShortcutLeavesTerminal(t *testing.T) {
	scrollShortcut(t, `Cmd+ArrowDown leaves the terminal alone.
1. Open the conversation.
2. `+strings.ToUpper(scrollOpenWorkspaceTool("Terminal")[:1])+scrollOpenWorkspaceTool("Terminal")[1:]+`. Wait for the terminal's input, ".terminal-panel textarea[aria-label=\"Terminal input\"]", to be visible (allow 30s), then eval its focus().
3. Scroll up.
4. Press ArrowDown with the Meta modifier, then sleep 500ms.
5. Still up.`, nil)
}

// The git graph covers the conversation and binds ArrowDown to its own
// commit navigation. Scrolling a list the reader cannot see would both
// fight that binding and lose their place. The header button that opened
// the graph keeps focus once it closes, so the test focuses the composer to
// exercise its shortcut.
func TestE2EScrollShortcutIgnoresListBehindOverlay(t *testing.T) {
	scrollShortcut(t, `Cmd/Ctrl+ArrowDown does not scroll the message list hidden behind an overlay.
1. Open the conversation.
2. Scroll up.
3. `+strings.ToUpper(scrollOpenWorkspaceTool("Git Graph")[:1])+scrollOpenWorkspaceTool("Git Graph")[1:]+`. ".git-graph-container" should become visible (allow 30s).
4. Press ArrowDown with the Control modifier, then sleep 500ms.
5. Still up.
6. Press Escape. ".git-graph-container" should become hidden (allow 10s). The focused element must be the button in ".chat-workspace-actions" whose aria-label is exactly "Git Graph".
7. Eval document.querySelector('[data-testid="message-input"]').focus(). Wait until the message input is focused (allow 10s). Its value must be exactly "".
8. Press ArrowDown with the Control modifier. Wait until the gap <= 2 is true (allow 10s).`, nil)
}
