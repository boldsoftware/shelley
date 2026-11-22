package test

import "testing"

// Ported from ui/e2e/subagent-live.spec.ts.

// Live subagent visualization: while a subagent conversation is working, the
// parent's subagent tool card shows a live activity strip (what the subagent
// is doing right now) that opens the subagent when clicked, and the drawer's
// subagent count badge shows running/total.
func TestE2ESubagentLiveActivity(t *testing.T) {
	e2e(t, useful(0), `Subagent live activity: the card strip shows the activity and opens the subagent; the drawer badge shows the running count. "The live strip" is the [data-testid="subagent-live"] element; "the badge" is ".conversation-item.active .subagent-count-badge". Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. Kick off a subagent that stays busy for a while: fill the message input with "subagent: helper bash: sleep 120" and click the send button ([data-testid="send-button"]).
3. The live strip appears on the running subagent tool card: it becomes visible (allow 30s), and its text contains "sleep" (allow 30s): what the subagent is doing, streamed into the parent's page.
4. Click the button with aria-label "Open conversations". The drawer (".drawer.open") becomes visible.
5. The badge becomes visible (allow 15s); its text contains "1/1" (1 running of 1 total), and the badge's [data-testid="subagent-badge-running"] element is visible.
6. Click the button with aria-label "Close conversations". Then no ".drawer.open" element remains (count 0).
7. Click the live strip. The URL then contains "/c/helper" (allow 10s): it opened the subagent's conversation.`, func(s *shelley) {
		s.v("SLUG", s.conversation("hello there", "").Slug)
	})
}
