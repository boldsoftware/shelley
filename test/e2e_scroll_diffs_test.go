package test

import (
	"testing"
)

// Ported from ui/e2e/scroll-diffs.spec.ts.

// Autoscroll while @pierre/diffs render.
//
// A patch tool's diff is not at its final height when the message arrives: the
// FileDiff renderer tokenizes in a worker and only then builds shadow DOM, so a
// tall diff keeps growing the message list for many frames afterwards. Until
// then PatchTool holds the space with a min-height placeholder.
//
// The bug this covers: the placeholder used to be dropped as soon as hydrate()
// was called, which is *before* the worker comes back. For that window the host
// had neither a placeholder nor content, so it collapsed to zero height and
// then re-expanded to the real height a few frames later. Chromium hides this
// because its scroll anchoring restores the offset exactly across such a
// round trip; WebKit's does not, so on Safari every diff that scrolled by
// knocked the viewport up by roughly a placeholder's worth and stranded the
// reader mid-diff with the scroll-to-bottom button showing.

// scrollDiffsSampler samples every diff host once per frame, looking for the bad
// state itself: no placeholder holding the space *and* no rendered diff to
// fill it. That is the transient zero-height gap the fix removes, and it is
// what WebKit cannot scroll-anchor its way back out of. Per-frame rather than
// a timer, and looking for a state rather than a transition: pre-fix the gap
// lasts ~500ms (tens of frames), so it is caught with a wide margin. The
// rendered <pre> is the signal for "the diff is really there". The shadow
// root is not: <diffs-container> attaches it in its constructor, and
// hydrate() immediately fills it with stylesheet and sprite markup.
const scrollDiffsSampler = `window.__emptyGaps = 0;
window.__sampledHosts = new Set();
const sample = () => {
  for (const host of document.querySelectorAll('.patch-tool-diff-host')) {
    window.__sampledHosts.add(host);
    const holdingSpace = !!host.style.minHeight;
    const hasDiff = !!host.querySelector('diffs-container')?.shadowRoot?.querySelector('pre');
    if (!holdingSpace && !hasDiff) window.__emptyGaps++;
  }
  requestAnimationFrame(sample);
};
requestAnimationFrame(sample);`

func TestE2EScrollDiffsPinnedWhileTallDiffsHydrate(t *testing.T) {
	e2e(t, useful(0), `Autoscroll stays pinned to the bottom while tall diffs hydrate. The test seeds a conversation at /c/{{SLUG}}. Call "big patch" sends it the message "big patch", whose reply ends in a patch with a diff many viewports tall, and returns without waiting for the reply. The page already runs a sampler that the test installed before the page's own scripts; do not create, reset or replace the sampler, window.__sampledHosts or window.__emptyGaps (only read them, in step 4). It checks every ".patch-tool-diff-host" element once per animation frame from page load on: window.__sampledHosts is the Set of hosts it has seen, and window.__emptyGaps counts the samples where a host had neither an inline style.minHeight (its placeholder) nor a rendered diff (a "pre" in the shadowRoot of its "diffs-container"). "The gap" is the messages container's (".messages-container") scrollHeight - scrollTop - clientHeight. Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait for the message input ([data-testid="message-input"]) and the messages container to be visible.
2. Then four turns, for N = 1, 2, 3 and 4 in turn (spell out each turn's steps with its own N): call "big patch"; wait until document.querySelectorAll(".patch-tool").length is exactly N (allow 30s); wait until the number of "diffs-container" elements whose shadow root has content, [...document.querySelectorAll("diffs-container")].filter((el) => (el.shadowRoot?.innerHTML.length ?? 0) > 0).length, is exactly N (allow 30s); then wait until the gap is below 120 (allow 15s).
3. document.querySelectorAll(".scroll-to-bottom-button").length must be exactly 0: never scrolled away, so the button must not exist at all.
4. window.__sampledHosts.size must be at least 4, and window.__emptyGaps must be exactly 0.`, func(s *shelley) {
		s.initScript(scrollDiffsSampler)
		c := s.conversation("echo seed", "")
		s.v("SLUG", c.Slug)
		s.call("big patch", func() string {
			s.sendChat(c.ID, "big patch")
			return "sent"
		})
	})
}

func TestE2EScrollDiffsToolCardReplacesPlaceholder(t *testing.T) {
	// A completed tool card mounts in place of a placeholder once it nears the
	// viewport, and Vue removes the placeholder before mounting the card. A
	// layout forced while the card set up (PatchTool read window.innerWidth)
	// saw the transcript short by the placeholder's height and clamped the
	// scroll offset at the bottom. The list was back to full height by the
	// next ResizeObserver report, so nothing explained the drop; when the
	// scroll event beat the card's growth it read as the reader scrolling
	// away, and the diff hydrated below a transcript that no longer followed.
	// Hold the placeholder's near-viewport report to swap it on cue.
	e2e(t, useful(0), `The transcript keeps the bottom when a tool card replaces its placeholder. The test seeds a conversation at /c/{{SLUG}} whose second, finished turn ends in a tall patch, so the page loads the patch as a completed tool card behind a placeholder (".tool-card-mount-placeholder--patch"). The page holds back IntersectionObserver reports about ".tool-card-mount-placeholder" elements until window.releaseToolCards() delivers them, which swaps the placeholder for the card. "The gap" is the messages container's (".messages-container") scrollHeight - scrollTop - clientHeight. Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait for an element ".tool-card-mount-placeholder--patch" to exist (allow 30s).
2. Wait until the gap is below 2 (an eval of gap < 2 expecting true).
3. The placeholder's offsetHeight must be greater than 2 (an eval of offsetHeight > 2 expecting true): without it the transcript would end above the current offset, the case that clamped.
4. In one async eval (no expect, it has side effects), on the messages container: record before = its scrollTop; call window.releaseToolCards(); await Promise.resolve() (Vue flushes on a microtask queued during the release, so the card is in place but no frame has run); record after = its scrollTop and mounted = whether the container contains a ".patch-tool"; store window.__swap = JSON.stringify({unchanged: after === before, mounted, scrolled: before > 0}).
5. window.__swap must be exactly {"unchanged":true,"mounted":true,"scrolled":true}.
6. Wait for an element ".patch-tool diffs-container" to exist.
7. Wait until the gap is below 120 (allow 15s).
8. document.querySelectorAll(".scroll-to-bottom-button").length must be exactly 0.`, func(s *shelley) {
		s.initScript(`const Native = window.IntersectionObserver;
const held = [];
let holding = true;
window.releaseToolCards = () => {
  holding = false;
  for (const deliver of held.splice(0)) deliver();
};
window.IntersectionObserver = class extends Native {
  constructor(callback, options) {
    super((entries, observer) => {
      const pass = entries.filter((e) => !holding || !e.target.matches('.tool-card-mount-placeholder'));
      const hold = entries.filter((e) => !pass.includes(e));
      if (hold.length) held.push(() => callback(hold, observer));
      if (pass.length) callback(pass, observer);
    }, options);
  }
};`)
		c := s.conversation("echo seed", "")
		s.sendChat(c.ID, "big patch")
		// Both turns done, so the page loads the patch as a completed card.
		s.waitTurns(c.ID, 2)
		s.v("SLUG", c.Slug)
	})
}

func TestE2EScrollDiffsNoHunksReleasesPlaceholder(t *testing.T) {
	// Guards the zero-hunks branch of watchForLayout, not the original bug:
	// holding the placeholder until the diff reports a height strands it forever
	// on a diff that will never have one. (So unlike the test above, this one is
	// green against the pre-fix code, which dropped every placeholder eagerly.
	// It fails if the observer is kept but the zero-hunks branch is dropped.)
	//
	// A no-op patch is that case: the tool still emits a unified diff, so
	// PatchTool renders a host, but it has no hunks, and with disableFileHeader
	// FileDiff renders nothing at all and the container stays 0px.
	//
	// Two identical overwrites in one conversation. The second is a no-op
	// whatever the file held beforehand: the fixture writes a fixed path.
	e2e(t, useful(0), `A diff with no hunks does not strand its placeholder. The test seeds a conversation at /c/{{SLUG}} with two identical patch turns; the second one's diff is a no-op with no hunks, so it renders nothing. "The second diff host" is the second element matching ".patch-tool-diff-host" (document.querySelectorAll(".patch-tool-diff-host")[1]); it is zero-height, so wait for it to exist, not to be visible. Wait with evals that have "expect" and a timeout, not with polling loops inside the expression. Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait until the second diff host exists (allow 30s).
2. Wait until the second diff host's style.minHeight is empty, i.e. (host.style.minHeight || "-") is exactly "-" (allow 15s): the placeholder was released.
3. Then the diff must really have hydrated and rendered nothing: for the second diff host's "diffs-container" element, JSON.stringify({hydrated: !!container?.shadowRoot, renderedDiff: !!container?.shadowRoot?.querySelector("pre")}) must be exactly {"hydrated":true,"renderedDiff":false}.`, func(s *shelley) {
		c := s.conversation("patch success", "")
		s.sendChat(c.ID, "patch success")
		s.waitTurns(c.ID, 2)
		s.v("SLUG", c.Slug)
	})
}
