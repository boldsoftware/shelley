package test

import (
	"testing"

	lazycue "github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/message-selection-comment.spec.ts.

// selectTextJS gives pages window.selectTextIn(selector, text), which makes
// the window's selection exactly the first occurrence of text in the text
// nodes of the first element matching selector, as a user's selection would.
const selectTextJS = `window.selectTextIn = (selector, text) => {
  const root = document.querySelector(selector);
  if (!root) throw new Error('missing ' + selector);
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  while (walker.nextNode()) {
    const node = walker.currentNode;
    const at = node.data.indexOf(text);
    if (at === -1) continue;
    const range = document.createRange();
    range.setStart(node, at);
    range.setEnd(node, at + text.length);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    return 'selected';
  }
  throw new Error('text ' + JSON.stringify(text) + ' not found in ' + selector);
};`

const selectTextSetup = `window.selectTextIn(selector, text) selects the first occurrence of text inside the first element matching selector, as a user's selection would; call it in an eval. "The comment button" is the element with data-testid "message-selection-comment-btn". To check the message input's value exactly, eval JSON.stringify(document.querySelector('[data-testid="message-input"]').value) and compare it with the JSON string given. `

func TestE2EMessageSelectionCommentTouchTap(t *testing.T) {
	e2e(t, selectTextSetup+`Tapping Comment on a touch screen injects a blockquote into the composer. Perform the steps in order. 1. Navigate to /c/basil. 2. The first commentable message ('[data-testid="message"][data-commentable="true"]') should contain "water the basil before noon". 3. Eval window.selectTextIn('[data-testid="message"][data-commentable="true"]', 'water the basil'). 4. The comment button should become visible, and it must be inside the viewport (its bounding rect intersects the window's innerWidth x innerHeight). 5. Tap it with a real touch held across frames: a touch step "start" on the comment button; then an eval that waits two animation frames, new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))); then a touch step "end". 6. The message input's value should become exactly "> water the basil\n\n" (JSON), the message input must be the focused element (document.activeElement), and the comment button should become hidden. 7. Eval window.selectTextIn('[data-testid="message"][data-commentable="true"]', 'before noon'); the comment button should become visible. 8. Tap it: touch "start" on the comment button immediately followed by touch "end". 9. The message input's value should become exactly "> water the basil\n\n> before noon\n\n" (JSON), and the comment button should become hidden.`, func(s *shelley) {
		s.initScript(selectTextJS)
		s.conversation("echo: water the basil before noon", "basil")
	})
}

func TestE2EMessageSelectionCommentNotOnUserMessages(t *testing.T) {
	e2e(t, selectTextSetup+`Selecting text in the user's own message does not offer Comment. Perform the steps in order. 1. Navigate to /c/reply. 2. The first message that is not commentable ('[data-testid="message"]:not([data-commentable="true"])') should contain "echo: reply text here". 3. Eval window.selectTextIn('[data-testid="message"]:not([data-commentable="true"])', 'reply text here'). 4. Give the page's selectionchange handling a turn: eval new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))). 5. The comment button must then be hidden (or absent).`, func(s *shelley) {
		s.initScript(selectTextJS)
		s.conversation("echo: reply text here", "reply")
	})
}

func TestE2EMessageSelectionCommentMouseClick(t *testing.T) {
	e2e(t, selectTextSetup+`On a desktop with a mouse, clicking Comment injects a blockquote into the composer, while a press that is released outside the button does nothing. Perform the steps in order. 1. Navigate to /c/mint. 2. The first commentable message ('[data-testid="message"][data-commentable="true"]') should contain "prune the mint". 3. Eval window.selectTextIn('[data-testid="message"][data-commentable="true"]', 'prune the mint'). 4. The comment button should become visible. 5. Press the mouse on the comment button and release it outside: hover the comment button (its center), mouse_down, then move the pointer (a hover step with no selector, "count" 5) to the viewport point 80px right of and 80px below the button's bottom-right corner (read the button's getBoundingClientRect() with an eval first; the layout is fixed), then mouse_up. 6. The message input's value must still be exactly "" (JSON), and the comment button must still be visible. 7. Click the comment button. 8. The message input's value should become exactly "> prune the mint\n\n" (JSON), the message input must be the focused element (document.activeElement), and the comment button should become hidden.`, func(s *shelley) {
		s.device(lazycue.Device{Width: 1280, Height: 800, Scale: lazycue.Pixel5.Scale})
		s.initScript(selectTextJS)
		s.conversation("echo: prune the mint", "mint")
	})
}
