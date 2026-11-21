package test

import (
	"testing"

	lazycue "github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/conversation.spec.ts.

// conversationTerms names what the conversation tests check. This
// conversation's messages, not the whole page: the drawer previews other
// conversations, which say the same things. A reply that calls a tool is a
// .message-tool, so a reply is anything not the user's.
const conversationTerms = `"The reply R" is the first element matching '.messages-container [data-testid="message"]:not(.message-user)' whose text contains R (has_text): a message of this conversation that is not the user's. "The user message U" is the first '.messages-container .message-user' element whose text contains U (has_text). "The input" is the element with data-testid "message-input" and "the send button" the one with data-testid "send-button". "Send M" means: fill the input with M, then click the send button. `

func TestE2EConversationSendHello(t *testing.T) {
	e2e(t, `Sending "Hello" gets the predictable model's greeting. `+conversationTerms+`1. Navigate to /new. 2. The input should be visible (allow 30s). 3. Fill the input with "Hello". 4. The send button should be visible. 5. Click it. 6. The reply "Hello! I'm Shelley, your AI assistant. How can I help you today?" should be visible (allow 30s). 7. The user message "Hello" should be visible.`, nil)
}

func TestE2EConversationEcho(t *testing.T) {
	e2e(t, `The echo command replies with its text. `+conversationTerms+`1. Navigate to /new. 2. Send "echo: test message". 3. The reply "test message" should be visible (allow 30s). 4. The user message "echo: test message" should be visible.`, nil)
}

func TestE2EConversationLowercaseHello(t *testing.T) {
	e2e(t, `The predictable model answers a lowercase "hello" differently. `+conversationTerms+`1. Navigate to /new. 2. Send "hello". 3. The reply "Well, hi there!" should be visible (allow 30s).`, nil)
}

func TestE2EConversationThinkingIndicator(t *testing.T) {
	// A deliberate delay keeps the indicator up long enough to observe.
	e2e(t, `The thinking indicator shows while a reply is awaited. `+conversationTerms+`"The indicator" is the element with data-testid "agent-thinking". 1. Navigate to /new. 2. Send "delay: 2". 3. The indicator should be visible (allow 5s). 4. The reply "Delayed for 2 seconds" should be visible (allow 30s). 5. The indicator should then be hidden (allow 10s).`, nil)
}

func TestE2EConversationThinkingIndicatorFollowUp(t *testing.T) {
	e2e(t, `The thinking indicator also shows for a follow-up message. `+conversationTerms+`"The indicator" is the element with data-testid "agent-thinking". 1. Navigate to /new. 2. Send "hello". 3. The reply "Well, hi there!" should be visible (allow 30s). 4. Send "delay: 2". 5. The indicator should be visible (allow 5s). 6. The reply "Delayed for 2 seconds" should be visible (allow 30s). 7. The indicator should then be hidden (allow 10s).`, nil)
}

func TestE2EConversationBashTool(t *testing.T) {
	e2e(t, `A message can make the agent use the bash tool. `+conversationTerms+`1. Navigate to /new. 2. Send 'bash: echo "hello world"'. 3. The reply 'I'll run the command: echo "hello world"' should be visible (allow 30s). 4. The first element with data-testid "tool-call-completed" should be visible (allow 10s). 5. The first element whose own text contains "bash" (selector "*" with has_text "bash", which picks the innermost) should be visible: the tool's name.`, nil)
}

func TestE2EConversationDefaultResponse(t *testing.T) {
	// The conversation is made through the API, so there is no race between
	// the page's first stream subscription and the reply being published.
	e2e(t, `A message the predictable model does not know gets its default reply. `+conversationTerms+`The conversation /c/{{SLUG}} was started with "this is an undefined message". 1. Navigate to /c/{{SLUG}}. 2. The reply "edit predictable.go to add a response for that one..." should be visible (allow 30s). 3. The user message "this is an undefined message" should be visible.`, func(s *shelley) {
		s.v("SLUG", s.conversation("this is an undefined message", "").Slug)
	})
}

func TestE2EConversationPersists(t *testing.T) {
	e2e(t, `A conversation keeps its earlier messages as it goes on. `+conversationTerms+`1. Navigate to /new. 2. Send "Hello". 3. The reply "Hello! I'm Shelley, your AI assistant. How can I help you today?" should be visible (allow 30s). 4. Send "echo: second message". 5. The reply "second message" should be visible (allow 30s). 6. The reply "Hello! I'm Shelley, your AI assistant. How can I help you today?" should still be visible. 7. The reply "second message" should be visible.`, nil)
}

// Enter sends only where there is no touch; on a touch device it is a
// newline, and the send button sends (MessageInput.handleKeyDown). The
// default device is a phone, so this test opts out of touch.
func TestE2EConversationSendWithEnter(t *testing.T) {
	e2e(t, `Without a touch screen, Enter in the input sends the message. `+conversationTerms+`1. Navigate to /new. 2. The input should be visible (allow 30s). 3. Fill the input with "Hello". 4. Press "Enter" in the input. 5. The reply "Hello! I'm Shelley, your AI assistant. How can I help you today?" should be visible (allow 30s).`, func(s *shelley) {
		// The Pixel 5 with hasTouch: false.
		d := lazycue.Pixel5
		d.Touch = false
		s.device(d)
	})
}

func TestE2EConversationThinkTool(t *testing.T) {
	// Thinking renders as data-testid "thinking-content" with a 💭, not as
	// a tool call.
	e2e(t, `The think tool's thoughts show in the transcript. `+conversationTerms+`1. Navigate to /new. 2. Send "think: I need to analyze this problem". 3. The reply "I've considered my approach." should be visible (allow 30s). 4. The first element with data-testid "thinking-content" should be visible (allow 10s). 5. The first element whose own text contains "💭" (selector "*" with has_text "💭") should be visible.`, nil)
}

func TestE2EConversationPatchTool(t *testing.T) {
	e2e(t, `A message can make the agent use the patch tool. `+conversationTerms+`1. Navigate to /new. 2. Send "patch: test.txt". 3. The reply "I'll patch the file: test.txt" should be visible (allow 30s). 4. The first element with data-testid "tool-call-completed" should be visible (allow 10s). 5. The first element whose own text contains "patch" (selector "*" with has_text "patch", which picks the innermost) should be visible.`, nil)
}

func TestE2EConversationToolResultsCollapsible(t *testing.T) {
	e2e(t, `Tool results show with a collapsible header. `+conversationTerms+`1. Navigate to /new. 2. Send 'bash: echo "testing tool results"'. 3. The first element with data-testid "tool-call-completed" should be visible (allow 30s). 4. The first ".bash-tool-header" element should be visible (allow 10s).`, nil)
}
