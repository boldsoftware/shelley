package test

import "testing"

// Ported from ui/e2e/conversation-tool-calls.spec.ts.

// Split out of conversation.spec.ts. These cover how tool calls are rendered
// and coalesced in the transcript; they drive the UI from /new rather than
// seeding a conversation via the API.

func TestE2EConversationToolCallsShowsToolResult(t *testing.T) {
	e2e(t, useful(0), `Coalesced tool calls show the tool result with details. Perform the steps in order.
1. Navigate to /new.
2. Fill the message input ([data-testid="message-input"]) with the text bash: echo "hello world" and click the send button ([data-testid="send-button"]).
3. Wait for the first [data-testid="tool-call-completed"] element to be visible (allow 30s).
4. The first ".bash-tool-header" element is visible.
5. The first ".bash-tool-command" element is visible.`, nil)
}

func TestE2EConversationToolCallsAgentTextAndToolSeparate(t *testing.T) {
	e2e(t, useful(0), `Coalesced tool calls display the agent's text and the tool separately. Perform the steps in order.
1. Navigate to /new.
2. Fill the message input ([data-testid="message-input"]) with "bash: pwd" and click the send button ([data-testid="send-button"]).
3. Wait for the first [data-testid="tool-call-completed"] element to be visible (allow 30s).
4. The agent's message is shown: the first element ("*" with has_text, nth 0) whose text contains "I'll run the command: pwd" is visible. Present text is not enough; it must be visible.
5. The tool result is shown separately as a coalesced tool call: the first [data-testid="tool-call-completed"] element is visible, and the first element ("*" with has_text, nth 0) whose text contains "bash" is visible.`, nil)
}

func TestE2EConversationToolCallsSequential(t *testing.T) {
	e2e(t, useful(0), `Sequential tool calls are all displayed. Perform the steps in order.
1. Navigate to /new.
2. Fill the message input ([data-testid="message-input"]) with the text bash: echo "first" and click the send button ([data-testid="send-button"]).
3. Wait for the first [data-testid="tool-call-completed"] element to be visible (allow 30s).
4. Fill the message input with the text bash: echo "second" and click the send button.
5. Wait (allow 30s, with an eval that has expect "true") until there are at least 2 [data-testid="tool-call-completed"] elements: both tool calls are displayed.`, nil)
}

// The spec's last check, text=Error inside the alert, is a case-insensitive
// substring match; the alert has no separate label, so it matched the error
// text itself.
func TestE2EConversationToolCallsLLMError(t *testing.T) {
	e2e(t, useful(0), `An LLM error message is displayed in the UI. Perform the steps in order.
1. Navigate to /new (a new conversation) and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. Fill the message input with "error: test error message" and click the send button ([data-testid="send-button"]); the predictable model fails on it.
3. Wait until the page text contains "LLM request failed: predictable error: test error message" (allow 30s).
4. The error message is visible with error styling: an element with role "alert" is visible (allow 10s).
5. An element whose text contains "LLM request failed: predictable error: test error message" is visible.
6. The alert shows the error: its message content ('[role="alert"] [data-testid="message-content"]') is visible and its text contains "error" (lowercase, as in "predictable error").`, nil)
}
