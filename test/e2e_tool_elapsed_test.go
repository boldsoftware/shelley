package test

import (
	"testing"
	"time"
)

// Ported from ui/e2e/tool-elapsed.spec.ts.

func TestE2EToolElapsedRunningShowsLiveTime(t *testing.T) {
	e2e(t, `Expanded running tools show a live elapsed time that stops on completion. The page runs on a fake clock that flows like real time until a step moves it: "fast-forward N ms" means an eval of exactly window.__clock.fastForward(N) (it returns a promise the eval awaits). "The tool" is the ".bash-tool" element; "the elapsed time" is the tool's [data-testid="tool-running-elapsed"] element; "the seconds" is the number in the elapsed time's text matched by /(\d+)s/. Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait for the message input ([data-testid="message-input"]) to be visible.
2. Fill the message input with "bash: sleep 100" and click the send button ([data-testid="send-button"]).
3. Wait until the tool's data-testid is exactly "tool-call-running" (allow 30s).
4. The tool contains no elapsed-time element (count 0).
5. Click the tool's ".bash-tool-header". The elapsed time's text then matches /Running for \d+s/.
6. In one eval (no expect), store the seconds in window.__initial.
7. Fast-forward 5000 ms.
8. Wait (with an eval that has "expect" "true") until the seconds are >= window.__initial + 5.
9. Click the tool's ".bash-tool-header" (collapsing it). The tool then contains no elapsed-time element (count 0).
10. Fast-forward 3000 ms.
11. Click the tool's ".bash-tool-header" again. Wait (with an eval that has "expect" "true") until the seconds are >= window.__initial + 8.
12. Click the stop button (".status-stop-button"). Wait until the tool's data-testid is exactly "tool-call-completed" (allow 10s).
13. The tool contains no elapsed-time element (count 0).
14. Click the tool's ".bash-tool-header" (the completed card starts collapsed, so this expands it) and wait until the tool's ".bash-tool-details" is visible, so the checks below look inside the expanded card. The tool contains no ".bash-tool-time" element (count 0) and no elapsed-time element (count 0): a stopped run shows neither.`, func(s *shelley) {
		s.v("SLUG", s.conversation("hello", "").Slug)
		s.clock(time.Time{})
	})
}

func TestE2EToolElapsedSubagentRunningRequests(t *testing.T) {
	// Subagent calls return as soon as the child starts; the live link stays
	// while the child works.
	e2e(t, `A working subagent's running requests show their elapsed time. "The subagent card" is the ".tool" element that contains a ".subagent-tool-slug" element. "The child tool" is the ".bash-tool" element on the subagent's page. Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait for the message input ([data-testid="message-input"]) to be visible.
2. Fill the message input with "subagent: helper bash: sleep 100" and click the send button ([data-testid="send-button"]).
3. Wait until the subagent card's data-testid is exactly "tool-call-completed" (allow 30s): the subagent call returns as soon as the child starts.
4. Click the subagent card's [data-testid="subagent-live"] element (the live link stays while the child works); it opens the subagent's conversation.
5. Wait until the child tool's data-testid is exactly "tool-call-running" (allow 30s).
6. Click the child tool's ".bash-tool-header". The child tool's [data-testid="tool-running-elapsed"] text then contains "Running for".
7. Click the stop button (".status-stop-button"). Wait until the child tool's data-testid is exactly "tool-call-completed" (allow 10s).`, func(s *shelley) {
		s.v("SLUG", s.conversation("hello", "").Slug)
	})
}
