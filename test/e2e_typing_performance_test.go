package test

import "testing"

// Ported from ui/e2e/typing-performance.spec.ts.

// A regression test for typing performance in large conversations (Safari).
// ChatInterface once tracked the composer text in a reactive ref, so every
// keystroke re-rendered the whole conversation, including the updated hook
// of every mounted v-tooltip directive; and PrimeVue then rewrote the
// identical CSS into its shared <style> elements, which makes WebKit restyle
// the whole document (~660ms per keystroke in a 5000-message conversation).
// Typing must neither rewrite a stylesheet nor touch the conversation's DOM.

// typingCountersJS gives pages window.countTypingEffects(), which starts
// counting writes to any <style> element's textContent and DOM mutations
// under .messages-list, and window.typingEffects(), which reports both
// counts as JSON.
const typingCountersJS = `window.countTypingEffects = () => {
  window.__styleWrites = 0;
  window.__messagesListPatches = 0;
  const desc = Object.getOwnPropertyDescriptor(Node.prototype, 'textContent');
  if (!desc?.set || !desc?.get) throw new Error('Node.textContent accessors not found');
  Object.defineProperty(HTMLStyleElement.prototype, 'textContent', {
    configurable: true,
    get() { return desc.get.call(this); },
    set(v) { window.__styleWrites++; desc.set.call(this, v); },
  });
  const list = document.querySelector('.messages-list');
  if (!list) throw new Error('.messages-list not found');
  new MutationObserver((records) => { window.__messagesListPatches += records.length; })
    .observe(list, { subtree: true, childList: true, attributes: true, characterData: true });
  return 'counting';
};
window.typingEffects = () => JSON.stringify({
  styleWrites: window.__styleWrites,
  messagesListPatches: window.__messagesListPatches,
});`

func TestE2ETypingPerformanceNoRewrites(t *testing.T) {
	e2e(t, useful(0), `Typing neither rewrites stylesheets nor re-renders the conversation. window.countTypingEffects() starts counting <style> rewrites and DOM mutations in the messages list; window.typingEffects() returns the counts as JSON. Perform the steps in order. 1. Navigate to /c/typing and wait (up to 30s) for the message input (data-testid "message-input") to be visible. 2. Add messages so the conversation scrolls, for each N of 1, 2 and 3 in turn: fill the message input with "echo message N", click the send button (data-testid "send-button"), wait (up to 30s) until the last element containing the text "echo message N" is visible, then wait (up to 30s) until the agent-thinking indicator (data-testid "agent-thinking") is hidden. 3. Scroll the messages to the top: eval document.querySelector('.messages-container').scrollTop = 0. 4. The scroll-to-bottom button (".scroll-to-bottom-button", which has a tooltip) should become visible (up to 10s). 5. Eval window.countTypingEffects(), which returns "counting". 6. Click the message input, then type "hello performance world" key by key (a type step, not fill). 7. Right away, eval window.typingEffects(); it must be exactly {"styleWrites":0,"messagesListPatches":0}.`, func(s *shelley) {
		s.initScript(typingCountersJS)
		s.conversation("echo message 0", "typing")
	})
}
