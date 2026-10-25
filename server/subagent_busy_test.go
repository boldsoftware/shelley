package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// These tests cover sending a message to a subagent whose current turn is
// still in flight. The old behavior cancelled that turn (discarding its work
// and mis-reporting the cancellation to the parent as a completion). The new
// behavior never interrupts: the message joins the current turn.

func TestSubagentBusy(t *testing.T) {
	t.Parallel()
	t.Run("DeliversMidTurn", testSubagentBusy_DeliversMidTurn)
	t.Run("DeliversDurableInjectionMidTurn", testSubagentBusy_DeliversDurableInjectionMidTurn)
}

// The subagent tool description carries a query listing this conversation's
// subagents with their working state and latest reply. Run it against a real
// database so it keeps matching the schema.
func TestSubagentToolDescriptionListsSubagents(t *testing.T) {
	t.Parallel()
	f := newSubagentDoneFixture(t, "Found three\nflaky tests.")
	defer stopActiveConversationLoops(f.server)
	ctx := t.Context()
	// Workers and the source of a distill are children too, but not
	// subagents the agent can address.
	createWorker(t, f.database, f.parentID)
	distilled, err := f.database.CreateConversation(ctx, strPtr("distilled-prev"), true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.UpdateConversationParent(ctx, distilled.ConversationID, f.parentID); err != nil {
		t.Fatal(err)
	}
	f.subagentMgr.SetAgentWorking(true)

	desc := (&claudetool.SubagentTool{DBPath: "/data/shelley.db"}).Tool().Description
	m := regexp.MustCompile(`sqlite3 -json "([^"]*)" "([^"]*)"`).FindStringSubmatch(desc)
	if m == nil {
		t.Fatalf("description has no subagent query:\n%s", desc)
	}
	if m[1] != "/data/shelley.db" {
		t.Fatalf("query database = %q, want /data/shelley.db", m[1])
	}
	query := strings.ReplaceAll(m[2], "$SHELLEY_CONVERSATION_ID", f.parentID)

	type row struct {
		slug      string
		working   bool
		lastReply string
	}
	var got []row
	if err := f.database.Pool().Rx(ctx, func(ctx context.Context, rx *db.Rx) error {
		rows, err := rx.Query(query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r row
			var lastReply sql.NullString
			if err := rows.Scan(&r.slug, &r.working, &lastReply); err != nil {
				return err
			}
			r.lastReply = lastReply.String
			got = append(got, r)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("run %q: %v", query, err)
	}
	want := []row{{slug: "sub-test", working: true, lastReply: "Found three\nflaky tests."}}
	if !slices.Equal(got, want) {
		t.Fatalf("query rows = %+v, want %+v", got, want)
	}
}

// A reasoning level passed to RunSubagent must be persisted on the subagent's
// conversation options so the loop picks it up. An empty reasoning string is a
// no-op and must not overwrite an existing level.
func TestSubagentRunner_PersistsReasoning(t *testing.T) {
	t.Parallel()
	f := newSubagentDoneFixture(t, "irrelevant")
	ctx := t.Context()

	runner := NewSubagentRunner(f.server)
	// Send with an explicit reasoning level.
	if _, err := runner.RunSubagent(ctx, f.subagentID, "do it", "predictable", "high"); err != nil {
		t.Fatalf("RunSubagent(reasoning=high): %v", err)
	}

	var opts string
	if err := f.database.Queries(ctx, func(q *generated.Queries) error {
		var e error
		opts, e = q.GetConversationOptions(ctx, f.subagentID)
		return e
	}); err != nil {
		t.Fatalf("get conversation options: %v", err)
	}
	if got := db.ParseConversationOptions(opts).ThinkingLevel; got != "high" {
		t.Fatalf("expected persisted thinking_level 'high', got %q", got)
	}

	// A subsequent empty reasoning must not clobber the stored level.
	if _, err := runner.RunSubagent(ctx, f.subagentID, "again", "predictable", ""); err != nil {
		t.Fatalf("RunSubagent(reasoning=\"\"): %v", err)
	}
	if err := f.database.Queries(ctx, func(q *generated.Queries) error {
		var e error
		opts, e = q.GetConversationOptions(ctx, f.subagentID)
		return e
	}); err != nil {
		t.Fatalf("get conversation options: %v", err)
	}
	if got := db.ParseConversationOptions(opts).ThinkingLevel; got != "high" {
		t.Fatalf("empty reasoning clobbered level; got %q", got)
	}
}

// hasCancelledMessage reports whether the subagent recorded an
// "[Operation cancelled]" end-of-turn message (the cancel-path artifact we
// want to be sure no longer appears on the resend path).
func hasCancelledMessage(t *testing.T, f *subagentDoneFixture) bool {
	t.Helper()
	msgs, err := f.database.ListMessages(t.Context(), f.subagentID)
	if err != nil {
		t.Fatalf("list subagent messages: %v", err)
	}
	for _, m := range msgs {
		if m.LlmData != nil && strings.Contains(*m.LlmData, "[Operation cancelled]") {
			return true
		}
	}
	return false
}

// A message to a busy subagent must reach it within its current turn — at
// the turn's next LLM round, right after the running tool's result — without
// cancelling the turn or waiting for it to end.
func testSubagentBusy_DeliversMidTurn(t *testing.T) {
	t.Parallel()
	f := newSubagentDoneFixture(t, "irrelevant")
	defer stopActiveConversationLoops(f.server)
	runner := NewSubagentRunner(f.server)

	if _, err := runner.RunSubagent(t.Context(), f.subagentID, "bash: sleep 1", "predictable", ""); err != nil {
		t.Fatalf("RunSubagent(start): %v", err)
	}
	if !f.subagentMgr.IsAgentWorking() {
		t.Fatal("subagent should be working on its first turn")
	}
	// Steer only once the tool is running. Injected messages are taken before
	// every LLM request, so steering earlier can land ahead of the turn's first
	// request instead of after the tool result.
	waitForToolUseRecorded(t, f.database, f.subagentID)
	res, err := runner.RunSubagent(t.Context(), f.subagentID, "echo: steered", "predictable", "")
	if err != nil {
		t.Fatalf("RunSubagent(steer): %v", err)
	}
	if !strings.Contains(res, "current turn") {
		t.Fatalf("expected a mid-turn delivery status, got %q", res)
	}
	requireSteeredMidTurn(t, f)
	// The steering message is the parent's, like a prompt to an idle subagent.
	steered := 0
	for _, m := range listMessages(t, f.database, f.subagentID) {
		if m.Type != string(db.MessageTypeUser) || m.LlmData == nil || !strings.Contains(*m.LlmData, "echo: steered") {
			continue
		}
		steered++
		if !fromParent(m) {
			t.Fatalf("steering message user_data = %v, want the parent's provenance", derefString(m.UserData))
		}
	}
	if steered != 1 {
		t.Fatalf("found %d steering messages, want 1", steered)
	}
}

// A steering message is injectable because the durable queue says so, not
// because the manager that queued it is still in memory: an injected entry
// that a fresh manager finds in queued_messages (as after a restart) still
// reaches the running turn at its next LLM round.
func testSubagentBusy_DeliversDurableInjectionMidTurn(t *testing.T) {
	t.Parallel()
	f := newSubagentDoneFixture(t, "irrelevant")
	defer stopActiveConversationLoops(f.server)
	runner := NewSubagentRunner(f.server)

	if _, err := runner.RunSubagent(t.Context(), f.subagentID, "bash: sleep 1", "predictable", ""); err != nil {
		t.Fatalf("RunSubagent(start): %v", err)
	}
	// As in DeliversMidTurn: queue only once the tool runs, or the turn's
	// first request may take the message before the tool result exists.
	waitForToolUseRecorded(t, f.database, f.subagentID)
	if _, err := f.database.AppendQueuedMessage(t.Context(), f.subagentID, db.QueuedMessage{
		ID:        "durable-steer",
		Llm:       []byte(`{"Role":0,"Content":[{"Type":2,"Text":"echo: steered"}]}`),
		CreatedAt: time.Now().UTC(),
		Model:     "predictable",
		Inject:    true,
	}); err != nil {
		t.Fatal(err)
	}
	requireSteeredMidTurn(t, f)
}

// requireSteeredMidTurn waits for the subagent's turn to end and requires that
// the "echo: steered" message was recorded right after the running tool's
// result, without cancelling the turn, and left nothing queued.
func requireSteeredMidTurn(t *testing.T, f *subagentDoneFixture) {
	t.Helper()
	select {
	case <-f.subagentMgr.idle():
	case <-time.After(10 * time.Second):
		t.Fatal("subagent turn did not finish")
	}

	if hasCancelledMessage(t, f) {
		t.Fatal("subagent turn was cancelled; expected it to keep running")
	}
	msgs, err := f.database.ListMessages(t.Context(), f.subagentID)
	if err != nil {
		t.Fatal(err)
	}
	steered := -1
	for i, m := range msgs {
		if m.Type == string(db.MessageTypeUser) && m.LlmData != nil && strings.Contains(*m.LlmData, "echo: steered") {
			steered = i
		}
	}
	if steered < 1 {
		t.Fatalf("steering message not recorded: %d messages", len(msgs))
	}
	var prev llm.Message
	if err := json.Unmarshal([]byte(*msgs[steered-1].LlmData), &prev); err != nil {
		t.Fatal(err)
	}
	if prev.Role != llm.MessageRoleUser || len(prev.Content) == 0 || prev.Content[0].Type != llm.ContentTypeToolResult {
		t.Fatalf("steering message should follow the running tool's result, got %+v", prev)
	}
	if q := queuedMessages(t, f.database, f.subagentID); len(q) != 0 {
		t.Fatalf("expected no queued messages, got %+v", q)
	}
}
