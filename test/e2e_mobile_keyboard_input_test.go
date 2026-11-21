package test

import (
	"fmt"
	"strings"
	"testing"
)

// Ported from ui/e2e/mobile-keyboard-input.spec.ts.

// Headless mobile emulation does not open Android's real software keyboard.
// The tests check the browser's keyboard-resize policy separately, then
// exercise the layout at the resulting reduced size. A viewport resize alone
// would pass without fixing the Android bug, because it resizes both
// viewports already.

func TestE2EMobileKeyboardInputResizesContent(t *testing.T) {
	e2e(t, `The app opts into keyboard-driven layout viewport resizing. 1. Navigate to /new. 2. There should be exactly one meta[name="viewport"]. 3. Its content attribute must have an interactive-widget=resizes-content entry: eval /(?:^|,)\s*interactive-widget\s*=\s*resizes-content\s*(?:,|$)/.test(document.querySelector('meta[name="viewport"]').getAttribute('content')) with expect "true".`, nil)
}

// mobileKeyboardWithinJS gives pages window.composerWithin(), which returns "ok"
// when the message input, the send button and the attach button are all
// visible within the visual viewport (a pixel's slack), and otherwise what
// is wrong.
const mobileKeyboardWithinJS = `window.composerWithin = () => {
  const vv = window.visualViewport;
  const top = vv.offsetTop, bottom = vv.offsetTop + vv.height;
  for (const id of ['message-input', 'send-button', 'attach-button']) {
    const el = document.querySelector('[data-testid="' + id + '"]');
    if (!el) return id + ' missing';
    const r = el.getBoundingClientRect();
    if (r.width === 0 || r.height === 0 || getComputedStyle(el).visibility === 'hidden') return id + ' hidden';
    if (r.top < top - 1) return id + ' above the visible viewport: ' + r.top + ' < ' + top;
    if (r.bottom > bottom + 1) return id + ' below the visible viewport: ' + r.bottom + ' > ' + bottom;
  }
  return 'ok';
};`

func mobileKeyboardTyping(t *testing.T, where, url string, setup func(s *shelley)) {
	draft := make([]string, 12)
	for i := range draft {
		draft[i] = fmt.Sprintf("Draft line %d", i+1)
	}
	draftJSON := mustJSON(strings.Join(draft, "\n"))
	e2e(t, `In a `+where+`, typing stays visible through keyboard-sized resizes. The phone's screen starts at 393x851. window.composerWithin() returns "ok" when the message input, the send button and the attach button are all visible within the visual viewport, or else what is wrong; "the composer is within the viewport" means an eval of window.composerWithin() with expect "ok". "The draft" is the 12-line text `+draftJSON+` (a JSON string; lines joined by newlines). To check the input's value exactly, eval JSON.stringify(document.querySelector('[data-testid="message-input"]').value) and compare it with the JSON string given. Perform the steps in order. 1. Navigate to `+url+`. 2. The message input (data-testid "message-input") should be enabled. 3. Fill it with "Typing with the keyboard open"; it must then have focus. 4. Resize the viewport to 393x420 (set_viewport width and height only), as an open keyboard would; the composer is within the viewport. 5. Fill the input with the draft; the composer is within the viewport, the input's value is exactly the draft, and the input has focus. 6. Resize the viewport to 851x360 (landscape); the composer is within the viewport, and the input's value is still exactly the draft. 7. Resize the viewport back to 393x851, as when the keyboard closes; the composer is within the viewport, the input's value is still exactly the draft, and the input still has focus. 8. Click the send button (data-testid "send-button"); the input's value should become exactly "" (JSON "\"\"").`, func(s *shelley) {
		s.viewport(393, 851)
		s.initScript(mobileKeyboardWithinJS)
		if setup != nil {
			setup(s)
		}
	})
}

func TestE2EMobileKeyboardInputNewConversation(t *testing.T) {
	mobileKeyboardTyping(t, "new conversation", "/new", nil)
}

func TestE2EMobileKeyboardInputExistingConversation(t *testing.T) {
	paragraphs := make([]string, 30)
	for i := range paragraphs {
		paragraphs[i] = fmt.Sprintf("Conversation paragraph %d.", i)
	}
	mobileKeyboardTyping(t, "existing conversation", "/c/long-read", func(s *shelley) {
		s.conversation("echo "+strings.Join(paragraphs, "\n\n"), "long-read")
	})
}
