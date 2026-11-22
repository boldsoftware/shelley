package test

import "testing"

// Ported from ui/e2e/tool-card-mount.spec.ts.

func TestE2EToolCardMountDefersOffscreenCards(t *testing.T) {
	e2e(t, useful(0), `Offscreen completed tool cards are deferred as placeholders and hydrate near the viewport. A 12-turn generated conversation with many tool calls is at /c/{{ID}}. "The placeholder" is the FIRST element matching ".tool-card-mount-placeholder". Check every assertion with an assert_* or wait_* step, or an eval that has "expect": an eval without "expect" asserts nothing. Perform the steps in order.
1. Navigate to /c/{{ID}} and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. Wait for the placeholder to exist in the DOM (allow 30s; it need not be visible).
3. The placeholder is NOT in the viewport (assert_not_in_viewport).
4. The placeholder has data-testid exactly "tool-call-completed", role exactly "group", and an aria-label that ends with " tool result" (check this one with an eval that has "expect").
5. In one eval with expect "282": create a div with className "tool-card-mount-placeholder tool-card-mount-placeholder--media", append it to document.body, compute its getBoundingClientRect().height + parseFloat(marginTop) + parseFloat(marginBottom) from getComputedStyle, remove it, and return that number. The media placeholder's outer height must be exactly 282.
6. In one eval (no expect), store the placeholder's data-tool-use-id in window.__toolId. Then check with an eval that has "expect" that window.__toolId is a non-empty string.
7. Set window.location.hash to "t-" followed by window.__toolId with every character that is not a-z, A-Z or 0-9 removed, cut to its first 8 characters (in an eval, without reloading or navigating).
8. Wait (allow 10s) until no element matches .tool-card-mount-placeholder[data-tool-use-id="<window.__toolId>"]: the targeted card hydrates.
9. "The hydrated card" is the element matching .toc-tool-anchor[data-tool-use-id="<window.__toolId>"] + [data-testid="tool-call-completed"] (the card right after the tool's anchor). It becomes visible (allow 10s) and its class list contains "message-highlight".
10. Then, within 5s, the hydrated card's class list no longer contains "message-highlight".
11. Hydration is sticky: scroll the messages container (".messages-container") to the bottom by setting its scrollTop to its scrollHeight. Right after that, there is still no .tool-card-mount-placeholder[data-tool-use-id="<window.__toolId>"] element, and the hydrated card is still in the DOM.
12. Printing is eager: dispatch a "beforeprint" event on window. Then, within 10s, no ".tool-card-mount-placeholder" element remains at all.
Where a selector depends on window.__toolId, check it in an eval with "expect" that builds the selector from window.__toolId at run time (CSS.escape is not needed); never write the ID literally.`, func(s *shelley) {
		s.v("ID", s.loremipsum("12").ID)
	})
}
