package server

import (
	"sync/atomic"
	"testing"
	"testing/synctest"
)

// TestChatSessionMinimalCommits pins the number of DB commit hooks fired during
// a typical chat session. Every commit fires Pool.OnCommit, which runs a full
// conversation-list recompute (reading + hashing up to 5000 conversations) —
// so each redundant transaction is a redundant full-list scan. We previously
// recorded each message and then bumped the conversation timestamp in a
// SEPARATE transaction, and flipped agent_working in its own transaction too,
// so a single echo turn fired 6 commits where 2 sufficed. The timestamp bump
// and the agent_working flip are now folded into the message-INSERT Tx.
//
// The only transactions that legitimately can't be merged are those separated
// by the LLM round-trip (we must not hold SQLite's single write lock open
// across it): the user message that starts the turn, and the agent message
// that ends it. So a steady-state turn must be exactly 2 commits.
func TestChatSessionMinimalCommits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)

		// Warm up: first turn creates the conversation, system prompt, etc.
		h.NewConversation("echo one", "")
		h.WaitResponse()
		synctest.Wait()

		// Measure only steady-state turns (no first-turn setup overhead).
		var commits int64
		h.db.Pool().OnCommit(func() { atomic.AddInt64(&commits, 1) })

		const turns = 3
		for range turns {
			h.Chat("echo more")
			h.WaitResponse()
			synctest.Wait()
		}

		got := atomic.LoadInt64(&commits)
		// Exactly 2 commits per turn: the turn-start user message (with
		// agent_working=true + updated_at folded into the INSERT) and the
		// end-of-turn agent message (with agent_working=false + updated_at
		// folded in). They are separated by the LLM call and so cannot share a
		// Tx. Anything more means a redundant transaction (and a redundant
		// full-list recompute) has crept back in.
		const wantPerTurn = 2
		if got != int64(turns*wantPerTurn) {
			t.Fatalf("steady-state chat fired %d commit hooks over %d turns; expected exactly %d (%d/turn). A higher count means a message write split its timestamp bump or agent_working flip into a separate transaction.", got, turns, turns*wantPerTurn, wantPerTurn)
		}
	})
}
