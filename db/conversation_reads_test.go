package db

import (
	"database/sql"
	"errors"
	"testing"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func TestConversationReadAttentionUsesPersistedVisibleContent(t *testing.T) {
	database := setupDBMigratedThrough(t, 71)
	t.Cleanup(func() { database.Close() })
	ctx := t.Context()
	conversation := oldSchemaConversation(t, database, "c", ConversationOptions{})

	add := func(kind MessageType, message llm.Message, userData any) int64 {
		t.Helper()
		row, err := database.CreateMessage(ctx, CreateMessageParams{
			ConversationID: conversation.ConversationID,
			Type:           kind,
			LLMData:        message,
			UserData:       userData,
		})
		if err != nil {
			t.Fatal(err)
		}
		return row.SequenceID
	}
	attention := func() int64 {
		t.Helper()
		states, err := database.ConversationAttention(ctx, []string{conversation.ConversationID})
		if err != nil {
			t.Fatal(err)
		}
		return states[conversation.ConversationID]
	}
	assertAttention := func(want int64) {
		t.Helper()
		if got := attention(); got != want {
			t.Fatalf("attention = %d, want %d", got, want)
		}
	}
	changed := func(before int64) int64 {
		t.Helper()
		now := attention()
		if now <= before {
			t.Fatalf("attention failed to advance: %d -> %d", before, now)
		}
		return now
	}

	if err := database.UpsertCacheSession(ctx, "browser", "alice"); err != nil {
		t.Fatal(err)
	}
	oldSchemaMessage(t, database, conversation.ConversationID, 1, MessageTypeAgent, llm.UserStringMessage("historical reply"))
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	assertAttention(0)
	add(MessageTypeAgent, llm.UserStringMessage("new reply"), nil)
	postRollout := changed(0)
	markers, err := database.ConversationReads(ctx, "alice")
	if err != nil || markers.FirstSeenAt <= 0 || len(markers.Receipts) != 0 {
		t.Fatalf("rollout receipts = %v, %v", markers, err)
	}
	late, err := database.ConversationReads(ctx, "new user")
	if err != nil || late.FirstSeenAt < postRollout || len(late.Receipts) != 0 {
		t.Fatalf("first visit receipts = %v, %v", late, err)
	}
	add(MessageTypeUser, llm.UserStringMessage("my own message"), nil)
	add(MessageTypeAgent, llm.Message{Content: []llm.Content{{Type: llm.ContentTypeThinking, Text: "private reasoning"}}}, nil)
	add(MessageTypeAgent, llm.Message{Content: []llm.Content{{Type: llm.ContentTypeToolUse, ToolName: "bash"}}}, nil)
	add(MessageTypeAgent, llm.Message{}, nil) // Empty/live transcript update.
	assertAttention(postRollout)

	add(MessageTypeAgent, llm.Message{Content: []llm.Content{{Type: llm.ContentTypeText, Text: "answer"}}}, nil)
	visible := changed(postRollout)
	// A later tool result or empty final does not displace persisted visible text.
	add(MessageTypeTool, llm.Message{EndOfTurn: true}, nil)
	add(MessageTypeAgent, llm.Message{EndOfTurn: true}, nil)
	assertAttention(visible)

	add(MessageTypeAgent, llm.Message{Content: []llm.Content{{Type: llm.ContentTypeText, MediaType: "image/png"}}}, nil)
	media := changed(visible)
	add(MessageTypeError, llm.Message{Content: []llm.Content{{Type: llm.ContentTypeText, Text: "request failed"}}}, nil)
	errorText := changed(media)
	add(MessageTypeUser, llm.UserStringMessage("subagent finished"), map[string]any{
		"Text": "subagent finished", "sender_relationship": "subagent", "sender_conversation_id": "child",
	})
	notice := changed(errorText)
	add(MessageTypeUser, llm.UserStringMessage("background job finished"), map[string]any{
		"Text": "background job finished", "background_job_id": "job-1",
	})
	jobNotice := changed(notice)

	// Provenance alone, and untrusted ordinary user text, are not attention.
	add(MessageTypeUser, llm.UserStringMessage("ordinary input"), map[string]any{"Text": "ordinary input"})
	add(MessageTypeUser, llm.UserStringMessage(""), map[string]any{
		"Text": " ", "background_job_id": "job-2",
	})
	assertAttention(jobNotice)

	add(MessageTypeAgent, llm.Message{Content: []llm.Content{
		{Type: llm.ContentTypeToolUse, ID: "shell", ToolName: "bash"},
		{Type: llm.ContentTypeToolUse, ID: "send", ToolName: "message_user"},
	}}, nil)
	result := func(id string, failed bool) int64 {
		return add(MessageTypeUser, llm.Message{Content: []llm.Content{
			{Type: llm.ContentTypeToolResult, ToolUseID: id, ToolError: failed},
		}}, nil)
	}
	result("shell", false)
	result("unmatched", false)
	result("send", true)
	assertAttention(jobNotice)
	cutoff := result("send", false)
	delivered := changed(jobNotice)

	// The fork inherits the messages, then starts its own reply clock.
	conversation, err = database.ForkConversation(ctx, conversation.ConversationID, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	assertAttention(0)
	add(MessageTypeAgent, llm.UserStringMessage("first reply in fork"), nil)
	changed(delivered)
}

func TestAcknowledgeConversationNeverRegresses(t *testing.T) {
	database := setupTestDB(t)
	ctx := t.Context()
	alice, err := database.ConversationReads(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	c, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reply := func() int64 {
		t.Helper()
		if _, err := database.CreateMessage(ctx, CreateMessageParams{ConversationID: c.ConversationID, Type: MessageTypeAgent, LLMData: llm.UserStringMessage("reply")}); err != nil {
			t.Fatal(err)
		}
		marks, err := database.ConversationAttention(ctx, []string{c.ConversationID})
		if err != nil {
			t.Fatal(err)
		}
		return marks[c.ConversationID]
	}
	first, second := reply(), reply()
	if first <= alice.FirstSeenAt || second <= first {
		t.Fatalf("unordered reply times %d, %d, %d", alice.FirstSeenAt, first, second)
	}
	for _, invalid := range []int64{0, -1, second + 1} {
		if _, err := database.AcknowledgeConversation(ctx, "alice", c.ConversationID, invalid); !errors.Is(err, ErrInvalidReadTime) {
			t.Fatalf("invalid time %d: %v", invalid, err)
		}
	}
	for _, stamp := range []int64{second, first} {
		got, err := database.AcknowledgeConversation(ctx, "alice", c.ConversationID, stamp)
		if err != nil || got != second {
			t.Fatalf("receipt = %d, %v", got, err)
		}
	}
	bob, err := database.ConversationReads(ctx, "bob")
	if err != nil || bob.FirstSeenAt <= second || len(bob.Receipts) != 0 {
		t.Fatalf("first visit = %+v, %v", bob, err)
	}
	if err := database.DeleteConversation(ctx, c.ConversationID); err != nil {
		t.Fatal(err)
	}
	alice, err = database.ConversationReads(ctx, "alice")
	if err != nil || len(alice.Receipts) != 0 {
		t.Fatalf("receipts after deletion = %+v, %v", alice, err)
	}
}

func TestConversationReadMessageUserHiddenText(t *testing.T) {
	database := setupDBMigratedThrough(t, 74)
	t.Cleanup(func() { database.Close() })
	ctx := t.Context()
	conversation := oldSchemaConversation(t, database, "c", ConversationOptions{
		ToolOverrides: map[string]string{"message_user": "on"},
	})
	add := func(kind MessageType, content ...llm.Content) {
		t.Helper()
		if _, err := database.CreateMessage(ctx, CreateMessageParams{
			ConversationID: conversation.ConversationID, Type: kind,
			LLMData: llm.Message{Content: content},
		}); err != nil {
			t.Fatal(err)
		}
	}
	attention := func() int64 {
		t.Helper()
		marks, err := database.ConversationAttention(ctx, []string{conversation.ConversationID})
		if err != nil {
			t.Fatal(err)
		}
		return marks[conversation.ConversationID]
	}
	oldSchemaMessage(t, database, conversation.ConversationID, 1, MessageTypeAgent, llm.Message{Content: []llm.Content{{Type: llm.ContentTypeText, Text: "previously recorded reply"}}})
	beforeUpgrade := attention()
	if beforeUpgrade == 0 {
		t.Fatal("pre-upgrade text did not mark attention")
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	add(MessageTypeAgent, llm.Content{Type: llm.ContentTypeText, Text: "hidden housekeeping"})
	add(MessageTypeAgent, llm.Content{Type: llm.ContentTypeText, MediaType: "image/png"})
	if got := attention(); got != beforeUpgrade {
		t.Fatalf("hidden text changed attention: %d -> %d", beforeUpgrade, got)
	}
	notices := []map[string]any{
		{"Text": "child report", "sender_relationship": "subagent", "sender_conversation_id": "child"},
		{"Text": "parent report", "sender_relationship": "parent", "sender_conversation_id": "parent"},
		{"Text": "job finished", "background_job_id": "job"},
	}
	addNotice := func(data map[string]any) {
		t.Helper()
		if _, err := database.CreateMessage(ctx, CreateMessageParams{
			ConversationID: conversation.ConversationID, Type: MessageTypeUser,
			LLMData: llm.UserStringMessage(data["Text"].(string)), UserData: data,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, notice := range notices {
		addNotice(notice)
		if got := attention(); got != beforeUpgrade {
			t.Fatalf("hidden notice %v changed attention: %d -> %d", notice, beforeUpgrade, got)
		}
	}

	add(MessageTypeAgent,
		llm.Content{Type: llm.ContentTypeText, Text: "hidden preamble"},
		llm.Content{Type: llm.ContentTypeToolUse, ID: "send", ToolName: "message_user"},
	)
	if got := attention(); got != beforeUpgrade {
		t.Fatalf("pending delivery changed attention: %d -> %d", beforeUpgrade, got)
	}
	add(MessageTypeTool, llm.Content{Type: llm.ContentTypeToolResult, ToolUseID: "send", ToolError: true})
	if got := attention(); got != beforeUpgrade {
		t.Fatalf("failed delivery changed attention: %d -> %d", beforeUpgrade, got)
	}
	add(MessageTypeTool, llm.Content{Type: llm.ContentTypeToolResult, ToolUseID: "send"})
	delivered := attention()
	if delivered <= beforeUpgrade {
		t.Fatal("successful message_user delivery did not advance attention")
	}
	add(MessageTypeAgent, llm.Content{Type: llm.ContentTypeText, Text: "more housekeeping"})
	if got := attention(); got != delivered {
		t.Fatalf("hidden text displaced a real delivery: %d -> %d", delivered, got)
	}
	add(MessageTypeError, llm.Content{Type: llm.ContentTypeText, Text: "visible request failure"})
	visibleError := attention()
	if visibleError <= delivered {
		t.Fatal("visible error did not advance attention")
	}
	if err := database.UpdateConversationOptions(ctx, conversation.ConversationID, ConversationOptions{
		ToolOverrides: map[string]string{"message_user": "off"},
	}); err != nil {
		t.Fatal(err)
	}
	add(MessageTypeAgent, llm.Content{Type: llm.ContentTypeText, Text: "ordinary visible answer"})
	if attention() <= visibleError {
		t.Fatal("ordinary agent text did not advance attention after disabling message_user")
	}
	for _, notice := range notices {
		before := attention()
		addNotice(notice)
		if attention() <= before {
			t.Fatalf("visible notice %v did not advance attention", notice)
		}
	}
}

func TestConversationReadMalformedOptions(t *testing.T) {
	database := setupTestDB(t)
	ctx := t.Context()
	conversation, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.UpdateConversationOptions(ctx, generated.UpdateConversationOptionsParams{
			ConversationID: conversation.ConversationID, ConversationOptions: "{",
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conversation.ConversationID, Type: MessageTypeAgent,
		LLMData: llm.UserStringMessage("visible reply"),
	}); err != nil {
		t.Fatalf("unread trigger blocked reply with malformed settings: %v", err)
	}
	marks, err := database.ConversationAttention(ctx, []string{conversation.ConversationID})
	if err != nil || marks[conversation.ConversationID] <= 0 {
		t.Fatalf("visible reply attention = %v, %v", marks, err)
	}
}

func TestMarkConversationUnread(t *testing.T) {
	database := setupTestDB(t)
	ctx := t.Context()
	c, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	marks := func(user string) ConversationReads {
		t.Helper()
		reads, err := database.ConversationReads(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		return reads
	}
	wantMarks := func(user string, want ...int64) {
		t.Helper()
		reads := marks(user)
		if len(reads.UnreadMarks) != len(want) {
			t.Fatalf("%s unread marks = %+v, want %v", user, reads.UnreadMarks, want)
		}
		for i, at := range want {
			if m := reads.UnreadMarks[i]; m.ConversationID != c.ConversationID || m.UnreadAt != at {
				t.Fatalf("%s unread marks = %+v, want %v", user, reads.UnreadMarks, want)
			}
		}
	}

	// A conversation without replies can be marked; the mark postdates the
	// user's first visit, even when marking is that visit.
	first, err := database.MarkConversationUnread(ctx, "alice", c.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if seen := marks("alice").FirstSeenAt; first <= seen {
		t.Fatalf("mark %d does not follow first visit %d", first, seen)
	}
	wantMarks("alice", first)
	wantMarks("bob")

	// A read must reach the mark to clear it, and cannot pass it.
	if _, err := database.AcknowledgeConversation(ctx, "alice", c.ConversationID, first+1); !errors.Is(err, ErrInvalidReadTime) {
		t.Fatalf("read past the mark: %v", err)
	}
	if _, err := database.AcknowledgeConversation(ctx, "bob", c.ConversationID, first); !errors.Is(err, ErrInvalidReadTime) {
		t.Fatalf("read of another user's mark: %v", err)
	}
	if got, err := database.AcknowledgeConversation(ctx, "alice", c.ConversationID, first); err != nil || got != first {
		t.Fatalf("read of the mark = %d, %v", got, err)
	}
	wantMarks("alice")

	// A later reply and its read do not stop the conversation being marked
	// again; reading an earlier time then leaves the new mark.
	if _, err := database.CreateMessage(ctx, CreateMessageParams{ConversationID: c.ConversationID, Type: MessageTypeAgent, LLMData: llm.UserStringMessage("reply")}); err != nil {
		t.Fatal(err)
	}
	attention, err := database.ConversationAttention(ctx, []string{c.ConversationID})
	if err != nil {
		t.Fatal(err)
	}
	reply := attention[c.ConversationID]
	if _, err := database.AcknowledgeConversation(ctx, "alice", c.ConversationID, reply); err != nil {
		t.Fatal(err)
	}
	second, err := database.MarkConversationUnread(ctx, "alice", c.ConversationID)
	if err != nil || second <= reply {
		t.Fatalf("second mark = %d (reply %d), %v", second, reply, err)
	}
	if got, err := database.AcknowledgeConversation(ctx, "alice", c.ConversationID, reply); err != nil || got != reply {
		t.Fatalf("stale read = %d, %v", got, err)
	}
	wantMarks("alice", second)
	again, err := database.MarkConversationUnread(ctx, "alice", c.ConversationID)
	if err != nil || again < second {
		t.Fatalf("repeated mark moved back: %d -> %d, %v", second, again, err)
	}

	if _, err := database.MarkConversationUnread(ctx, "alice", "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mark of a missing conversation: %v", err)
	}
	if err := database.DeleteConversation(ctx, c.ConversationID); err != nil {
		t.Fatal(err)
	}
	wantMarks("alice")
}
