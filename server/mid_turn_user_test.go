package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

func TestMidTurnUserMessage(t *testing.T) {
	t.Run("InjectedAtNextRound", testMidTurnUser_InjectedAtNextRound)
	t.Run("InterruptsSubagentWait", testMidTurnUser_InterruptsSubagentWait)
	t.Run("PlainSendCountsAsPending", testMidTurnUser_PlainSendCountsAsPending)
}

// The composer's plain Send (queue:false) while the agent is busy goes
// through AcceptUserMessage straight into the running loop's messageQueue,
// NOT pendingBatches. HasPendingUserMessage must see it there, otherwise a
// blocking subagent wait never yields to it (the bug seen in production:
// message sat for the whole wait).
func testMidTurnUser_PlainSendCountsAsPending(t *testing.T) {
	f := newSubagentDoneFixture(t, "unused")
	ctx := context.Background()

	start := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "bash: sleep 5"}},
	}
	if _, err := f.parentMgr.AcceptUserMessage(ctx, f.llmSvc, "predictable", start); err != nil {
		t.Fatalf("AcceptUserMessage: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return hasToolUse(t, f, "bash") })
	if f.parentMgr.HasPendingUserMessage() {
		t.Fatalf("nothing sent yet; should not be pending")
	}

	sent := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "echo: heard-you"}},
	}
	if _, err := f.parentMgr.AcceptUserMessage(ctx, f.llmSvc, "predictable", sent); err != nil {
		t.Fatalf("AcceptUserMessage (mid-turn): %v", err)
	}
	if !f.parentMgr.HasPendingUserMessage() {
		t.Fatalf("plain Send while the loop is blocked in a tool must count as pending")
	}
	waitFor(t, 15*time.Second, func() bool { return !f.parentMgr.IsAgentWorking() })
	if f.parentMgr.HasPendingUserMessage() {
		t.Fatalf("still pending after the turn drained it")
	}
}

// A user message queued while the parent is mid-turn is spliced into the
// running turn at the next LLM round (via takeInjectable), not held until the
// turn ends: its row lands BEFORE the turn's first end-of-turn assistant
// message and it leaves queued_messages.
func testMidTurnUser_InjectedAtNextRound(t *testing.T) {
	f := newSubagentDoneFixture(t, "unused")
	ctx := context.Background()

	start := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "bash: sleep 2"}},
	}
	if _, err := f.parentMgr.AcceptUserMessage(ctx, f.llmSvc, "predictable", start); err != nil {
		t.Fatalf("AcceptUserMessage: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return hasToolUse(t, f, "bash") })
	if !f.parentMgr.IsAgentWorking() {
		t.Fatalf("parent should be mid-turn")
	}

	queued := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "echo: heard-you"}},
	}
	if err := f.parentMgr.QueueMessage(ctx, f.server, "predictable", queued); err != nil {
		t.Fatalf("QueueMessage: %v", err)
	}
	if !f.parentMgr.HasPendingUserMessage() {
		t.Fatalf("expected a pending user batch")
	}

	waitFor(t, 15*time.Second, func() bool { return !f.parentMgr.IsAgentWorking() })

	msgs := f.parentMessages()
	userIdx, endIdx := -1, -1
	for i, m := range msgs {
		if m.LlmData == nil {
			continue
		}
		var lm llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &lm); err != nil {
			continue
		}
		if lm.Role == llm.MessageRoleUser && len(lm.Content) == 1 && lm.Content[0].Text == "echo: heard-you" {
			userIdx = i
		}
		if lm.Role == llm.MessageRoleAssistant && lm.EndOfTurn && endIdx == -1 {
			endIdx = i
		}
	}
	if userIdx == -1 || endIdx == -1 {
		t.Fatalf("missing queued user row (%d) or end-of-turn (%d)\n%s", userIdx, endIdx, dumpMessages(t, msgs))
	}
	if userIdx > endIdx {
		t.Fatalf("user message (idx %d) landed AFTER the turn ended (idx %d): not injected mid-turn\n%s",
			userIdx, endIdx, dumpMessages(t, msgs))
	}
	if f.parentMgr.HasPendingUserMessage() {
		t.Fatalf("user batch still pending after injection")
	}
	conv, err := f.database.GetConversationByID(ctx, f.parentID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if conv.QueuedMessages != "[]" {
		t.Fatalf("queued_messages not cleared: %s", conv.QueuedMessages)
	}
}

// A wait=true subagent call blocked on a long-running subagent returns as
// soon as the PARENT receives a queued user message, without cancelling the
// subagent, so the parent's loop can reach the round where that message is
// injected.
func testMidTurnUser_InterruptsSubagentWait(t *testing.T) {
	f := newSubagentDoneFixture(t, "irrelevant")
	ctx := context.Background()

	if err := f.subagentMgr.ensureLoop(f.llmSvc, "predictable"); err != nil {
		t.Fatalf("ensureLoop subagent: %v", err)
	}
	f.subagentMgr.SetAgentWorking(true) // stays working for the whole test
	defer f.subagentMgr.SetAgentWorking(false)

	// Parent looks busy so QueueMessage queues instead of starting a turn.
	f.parentMgr.SetAgentWorking(true)
	defer f.parentMgr.SetAgentWorking(false)

	runner := NewSubagentRunner(f.server)
	type result struct {
		res string
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := runner.RunSubagent(ctx, f.subagentID, "echo: foo", true, time.Minute, "predictable", "")
		done <- result{res, err}
	}()

	// Poll interval is 500ms; give the wait a moment to be established
	// (checked via the waiter slot) before the user "types".
	waitFor(t, 5*time.Second, func() bool {
		f.subagentMgr.mu.Lock()
		defer f.subagentMgr.mu.Unlock()
		return f.subagentMgr.subagentWaitOwners > 0
	})

	queued := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "are you there?"}},
	}
	if err := f.parentMgr.QueueMessage(ctx, f.server, "predictable", queued); err != nil {
		t.Fatalf("QueueMessage: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("RunSubagent: %v", r.err)
		}
		if !strings.Contains(r.res, "interrupted") {
			t.Fatalf("expected interrupted result, got %q", r.res)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("wait=true did not return after parent received a user message")
	}
	if hasCancelledMessage(t, f) {
		t.Fatalf("subagent turn was cancelled; expected it to keep running")
	}
	if !f.subagentMgr.IsAgentWorking() {
		t.Fatalf("subagent should still be working")
	}
	// The user message is untouched: it's the parent loop's job to deliver
	// it, not the subagent tool's.
	if !f.parentMgr.HasPendingUserMessage() {
		t.Fatalf("parent's user message should still be pending")
	}
}

func hasToolUse(t *testing.T, f *subagentDoneFixture, tool string) bool {
	t.Helper()
	for _, m := range f.parentMessages() {
		if m.LlmData == nil {
			continue
		}
		var lm llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &lm); err != nil {
			continue
		}
		for _, c := range lm.Content {
			if c.Type == llm.ContentTypeToolUse && c.ToolName == tool {
				return true
			}
		}
	}
	return false
}
