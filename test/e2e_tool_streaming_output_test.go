package test

import "testing"

// Ported from ui/e2e/tool-streaming-output.spec.ts.

// While a bash tool runs, its card shows a compact preview of the streamed
// output below the header, so a long-running command is legible without
// expanding anything. The preview is transient: it disappears when the tool
// finishes and the card's own output section takes over.
func TestE2EToolStreamingOutputPreviewsTail(t *testing.T) {
	// The command emits a new line every 200ms for ~12s: bash reports its
	// output tail to the UI every 500ms while it runs. The lines are wider
	// than the card on the 393px-wide phone, so they exercise
	// wrapping/clipping.
	e2e(t, `A running bash card previews the tail of its streamed output. "The card" is the ".bash-tool" element (the conversation's only tool call); "the preview" is the card's ".bash-tool-preview-code" element; "the preview's lines" is the number of lines of the preview's textContent (split on "\n") that are not blank after trimming; "the newest tick" is the largest N among all "tick-N" matches in the preview's textContent, or 0 when there are none or the preview is gone. Check every assertion with an assert_* or wait_* step, or an eval that has "expect": an eval without "expect" asserts nothing. Perform the steps in order, without pauses: the command finishes about 12 seconds after it starts.
1. Navigate to /c/{{SLUG}} and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. Fill the message input with exactly this text (one line): bash: /opt/toolchain/bin/some-really-long-builder-name --flag; for i in $(seq 1 60); do echo "tick-$i compiling a rather long module name ... ok"; sleep 0.2; done
3. Click the send button ([data-testid="send-button"]).
4. Wait until the card's data-testid is exactly "tool-call-running" (allow 30s).
5. Wait until the preview's text matches /tick-\d+/ (allow 30s).
6. The command stays visible in the header while the preview streams below it: the card's ".bash-tool-command" text contains "some-really-long-builder-name".
7. In one eval (no expect), store the newest tick in window.__first.
8. The preview tracks the newest line: wait (allow 15s) until the newest tick is greater than window.__first.
9. Right away, the preview's lines must be at most 5 (it shows only the tail).
10. The preview clips rather than widening the conversation: the messages container (".messages-container") has scrollWidth - clientWidth <= 1, and so does the html element (document.documentElement).
11. The card's ".bash-tool-preview-more" text matches /Show all \d+ lines/. Click it; then no ".bash-tool-preview-more" element remains (count 0).
12. Wait (allow 15s) until the preview's lines are more than 5: it now reveals everything streamed so far, in place.
13. Wait until the card's data-testid is exactly "tool-call-completed" (allow 60s). The card then contains no ".bash-tool-preview" element (count 0).
14. Click the card's ".bash-tool-header". The card's ".bash-tool-details" text contains "tick-60".`, func(s *shelley) {
		s.v("SLUG", s.conversation("hello", "").Slug)
	})
}
