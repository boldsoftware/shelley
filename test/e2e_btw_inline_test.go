package test

import (
	"path/filepath"
	"syscall"
	"testing"
)

// Ported from ui/e2e/btw-inline.spec.ts.

// btwInlineBarrier makes a FIFO that nothing ever writes, outside the
// working directory, and returns a predictable-model command whose bash call
// blocks reading it, keeping its conversation busy until it is stopped.
func btwInlineBarrier(s *shelley, name string) string {
	fifo := filepath.Join(s.dir, name)
	s.check(syscall.Mkfifo(fifo, 0o600))
	return "bash: read release < " + fifo
}

// btwInlineCompletedJS gives pages window.btwCompleted(text, turns), which
// resolves true once the BTW reader containing text has a child conversation
// (its data-btw-exchange-id) with at least turns completed agent turns.
const btwInlineCompletedJS = `window.btwCompleted = async (text, turns) => {
  const reader = [...document.querySelectorAll('[data-testid="btw-inline"]')]
    .find((el) => el.textContent.includes(text));
  const id = reader?.getAttribute('data-btw-exchange-id');
  if (!id) return false;
  const response = await fetch('/api/conversation/' + id);
  if (!response.ok) return false;
  const body = await response.json();
  return (body.messages || []).filter((m) => m.type === 'agent' && m.end_of_turn === true).length >= turns;
};`

const btwInlineSetup = `"Send X" means: fill the message input (data-testid "message-input") with X, click the send button (data-testid "send-button"), and wait (up to 30s) until the message input's value is exactly "". A "reader" is a BTW thread shown inline, [data-testid="btw-inline"]; "the reader for X" is the one containing the text X. Inside a reader: its answers are ".btw-inline-answer", its expand/collapse toggle is ".btw-inline-label", its body is ".btw-inline-body", its follow-up box is "[data-btw-follow-up]", and its action buttons are found by their text ("Summarize to input", "Dismiss", "Cancel") or label (button[aria-label="Submit follow up"]); its "Open subagent" link is the a.btw-inline-open-subagent. "The Stop button" is button[aria-label="Stop"]. "The reader for X has completed N turns" means: eval window.btwCompleted(X, N) with expect "true" (it checks the reader's child conversation on the server); then the reader's "Summarize to input" button should be visible (up to 30s), and its follow-up box should be enabled. `

func TestE2EBtwInlineIndependentReaders(t *testing.T) {
	e2e(t, useful(0), btwInlineSetup+`The parent conversation and sibling BTW readers stay independent through a follow-up, a summary and reloads. Perform the steps in order. 1. Navigate to /c/side-parent and wait (up to 30s) for the message input to be visible. 2. Send "{{PARENT_BARRIER}}" (a bash command that blocks until stopped). The Stop button should be visible. 3. Send "/btw echo: side one", then send "/btw echo: side two". 4. There should be exactly two readers (up to 30s). The reader for "echo: side one" should have an answer containing "side one", and the reader for "echo: side two" an answer containing "side two". 5. The reader for "echo: side one" has completed 1 turn, and the reader for "echo: side two" has completed 1 turn. 6. Click the Stop button; it should be gone (count 0, up to 30s), and there should still be exactly two readers. 7. In the reader for "echo: side one": fill its follow-up box with "unsent follow-up"; click its toggle, and its body should become hidden; click its toggle again, and its follow-up box's value should be exactly "unsent follow-up". 8. Fill that follow-up box with "echo: follow-up answer" and click that reader's Submit follow up button. That reader's last answer should come to contain "follow-up answer" (up to 30s). 9. The reader for "echo: side one" has completed 2 turns. 10. Click that reader's "Summarize to input" button. The message input's value should become exactly "edit predictable.go to add a response for that one..." (up to 30s). 11. Reload the page. The message input's value should be exactly "edit predictable.go to add a response for that one..." (up to 30s), and there should be exactly two readers. 12. Remember the reader for "echo: side one"'s child conversation: eval window.__dismissed = <that reader>.getAttribute('data-btw-exchange-id'), which must be non-empty. 13. Click that reader's "Dismiss" button. The reader for "echo: side one" should be gone (count 0, up to 30s), leaving exactly one reader. 14. The dismissed reader's child conversation still exists: eval fetch('/api/conversation/' + window.__dismissed).then(r => r.ok) with expect "true". 15. Reload the page. There must be no reader for "echo: side one", and exactly one reader. 16. Click the "Open subagent" link of the reader for "echo: side two". The URL path should come to start with "/c/btw-" (up to 10s).`, func(s *shelley) {
		s.initScript(btwInlineCompletedJS)
		s.v("PARENT_BARRIER", btwInlineBarrier(s, "parent-release"))
		s.conversation("echo: parent ready", "side-parent")
	})
}

func TestE2EBtwInlineChildCancellation(t *testing.T) {
	e2e(t, useful(0), btwInlineSetup+`Cancelling a BTW reader's work leaves the parent alone, and a reader started after /clear is restored after a reload. Perform the steps in order. 1. Navigate to /c/side-cancel and wait (up to 30s) for the message input to be visible. 2. Send "/btw {{CHILD_BARRIER}}" (its bash command blocks until cancelled). 3. Click the "Cancel" button of the reader for "read release". That reader should come to contain "Cancelled" (up to 30s), and there must be no Stop button (count 0). 4. Send "echo: parent still works". The last element containing "parent still works" should become visible (up to 30s). 5. Send "/clear". Exactly one ".generation-divider" should appear (up to 30s). 6. Send "/btw echo: after clear". The reader for "echo: after clear" should have an answer that comes to contain "after clear" (up to 30s), and exactly one answer (count 1). 7. Reload the page. The reader for "echo: after clear"'s answer should again contain "after clear" (up to 30s), and it should again have exactly one answer.`, func(s *shelley) {
		s.initScript(btwInlineCompletedJS)
		s.v("CHILD_BARRIER", btwInlineBarrier(s, "child-release"))
		s.conversation("echo: parent ready", "side-cancel")
	})
}
