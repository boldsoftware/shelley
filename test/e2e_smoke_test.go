package test

import "testing"

// Ported from ui/e2e/smoke.spec.ts.

func TestE2ESmokePageLoads(t *testing.T) {
	e2e(t, `Navigate to /new. The page title should be "Shelley Agent".`, nil)
}

func TestE2ESmokeWelcomeLinks(t *testing.T) {
	e2e(t, `Navigate to /new. In the welcome text (".chat-welcome-text"), the link "open-source" should point to "https://github.com/boldsoftware/shelley/" and the link "can be customized" should point to "https://shelley.dev/#customization".`, nil)
}

func TestE2ESmokeComposerLabels(t *testing.T) {
	e2e(t, `Navigate to /new. The message input (data-testid "message-input") should be visible, empty, and have the aria-label "Message input" and some non-empty placeholder (it is picked at random, so do not assert its text). The send button (data-testid "send-button") should be visible, have the aria-label "Send message", and be disabled. After filling the message input with "test message", the send button should be enabled.`, nil)
}

func TestE2ESmokeComposerAutoGrows(t *testing.T) {
	e2e(t, `The message input auto-grows without synchronous layout reads. Navigate to /new and wait for the message input (data-testid "message-input") to be visible. Record its initial height (getBoundingClientRect().height) in a window variable. Then, in an eval, wrap Element.prototype's scrollHeight getter so that every read of scrollHeight on an element with the class "message-textarea" increments window.__textareaScrollHeightReads (starting from 0), still returning the real value. Fill the message input with six lines ("one" through "six", separated by newlines). The input's height must grow past its initial height, and window.__textareaScrollHeightReads must stay 0. Then fill it with twenty lines ("line 0" through "line 19"): its height must settle at no more than 202 pixels.`, nil)
}
