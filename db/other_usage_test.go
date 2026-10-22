package db

import (
	"encoding/json"
	"testing"

	"shelley.exe.dev/llm"
)

// TestMessageOtherUsageRoundTrip verifies that CreateMessage persists
// OtherUsageData as a JSON array, that it is NULL when unset, and that a
// forked copy preserves it (usage attribution across forks relies on
// forked_from_message_id for dedup, same as usage_data).
func TestMessageOtherUsageRoundTrip(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	ctx := t.Context()
	conv, err := database.CreateConversation(ctx, stringPtr("other-usage-round-trip"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}

	otherUsage := []llm.PurposedUsage{
		{Purpose: "keyword_search", Usage: llm.Usage{InputTokens: 100, OutputTokens: 10, CostUSD: 0.01, Model: "m1", URL: "u1"}},
		{Purpose: "llm_one_shot", Usage: llm.Usage{InputTokens: 5, OutputTokens: 1, Model: "m2"}},
	}
	if _, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		LLMData:        llm.Message{Role: llm.MessageRoleUser},
		OtherUsageData: otherUsage,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeAgent,
		LLMData:        llm.Message{Role: llm.MessageRoleAssistant},
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := database.ListMessages(ctx, conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}
	if messages[0].OtherUsageData == nil {
		t.Fatal("message[0].OtherUsageData = nil, want JSON array")
	}
	var back []llm.PurposedUsage
	if err := json.Unmarshal([]byte(*messages[0].OtherUsageData), &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 || back[0].Purpose != "keyword_search" || back[0].InputTokens != 100 || back[1].Purpose != "llm_one_shot" {
		t.Errorf("round-trip = %+v", back)
	}
	if messages[1].OtherUsageData != nil {
		t.Errorf("message[1].OtherUsageData = %v, want nil", *messages[1].OtherUsageData)
	}

	// Forking copies other_usage_data onto the fork's copies.
	forked, err := database.ForkConversation(ctx, conv.ConversationID, messages[1].SequenceID)
	if err != nil {
		t.Fatal(err)
	}
	forkedMsgs, err := database.ListMessages(ctx, forked.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(forkedMsgs) != 2 {
		t.Fatalf("got %d forked messages, want 2", len(forkedMsgs))
	}
	if forkedMsgs[0].OtherUsageData == nil || *forkedMsgs[0].OtherUsageData != *messages[0].OtherUsageData {
		t.Errorf("forked message[0].OtherUsageData = %v, want %v", forkedMsgs[0].OtherUsageData, *messages[0].OtherUsageData)
	}
	if forkedMsgs[0].ForkedFromMessageID == nil || *forkedMsgs[0].ForkedFromMessageID != messages[0].MessageID {
		t.Errorf("fork provenance missing: %v", forkedMsgs[0].ForkedFromMessageID)
	}
	if forkedMsgs[1].OtherUsageData != nil {
		t.Errorf("forked message[1].OtherUsageData = %v, want nil", *forkedMsgs[1].OtherUsageData)
	}
}

func TestCreateUsageMessage(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	ctx := t.Context()
	conv, err := database.CreateConversation(ctx, stringPtr("slug-usage"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		LLMData:        llm.Message{Role: llm.MessageRoleUser},
	}); err != nil {
		t.Fatal(err)
	}

	entries := []llm.PurposedUsage{{Purpose: "slug", Usage: llm.Usage{InputTokens: 10, OutputTokens: 2, CostUSD: 0.001, Model: "m"}}}
	msg, err := database.CreateUsageMessage(ctx, conv.ConversationID, entries)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != string(MessageTypeSlug) {
		t.Errorf("type = %q, want slug", msg.Type)
	}
	// Belt and braces: the type is filtered out of LLM context, and the row is
	// also flagged so a context builder that forgets the type still can't send it.
	if !msg.ExcludedFromContext {
		t.Error("slug marker must be excluded_from_context")
	}
	if msg.LlmData != nil {
		t.Errorf("llm_data = %v, want nil: the marker has no content", *msg.LlmData)
	}
	if got := mustParseOtherUsage(t, msg.OtherUsageData); len(got) != 1 || got[0].Purpose != "slug" || got[0].InputTokens != 10 {
		t.Errorf("other_usage_data = %+v, want the slug entry", got)
	}

	// It is appended, leaving the user message untouched.
	messages, err := database.ListMessages(ctx, conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].MessageID != msg.MessageID {
		t.Fatalf("messages = %+v, want the user message then the slug marker", messages)
	}
	if messages[0].OtherUsageData != nil {
		t.Errorf("user message was modified: %v", *messages[0].OtherUsageData)
	}

	// Nothing to record: no marker, so we don't litter empty rows.
	empty, err := database.CreateUsageMessage(ctx, conv.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty != nil {
		t.Errorf("created a marker for zero usage entries: %+v", empty)
	}
}

// TestForkDoesNotCopySlugMarker: a fork derives its slug from the source
// synchronously, with no LLM call. Copying the marker would bill the fork for a
// call it never made.
func TestForkDoesNotCopySlugMarker(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := t.Context()

	conv, err := database.CreateConversation(ctx, stringPtr("fork-slug-src"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		LLMData:        llm.Message{Role: llm.MessageRoleUser},
		OtherUsageData: []llm.PurposedUsage{{Purpose: "keyword_search", Usage: llm.Usage{InputTokens: 5, Model: "m"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateUsageMessage(ctx, conv.ConversationID, []llm.PurposedUsage{
		{Purpose: "slug", Usage: llm.Usage{InputTokens: 10, CostUSD: 0.001, Model: "m"}},
	}); err != nil {
		t.Fatal(err)
	}

	// Fork past the slug marker's sequence_id, so the copy exclusion is what
	// keeps it out rather than the cutoff falling short of it.
	srcMsgs, err := database.ListMessages(ctx, conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := srcMsgs[len(srcMsgs)-1].SequenceID
	if srcMsgs[len(srcMsgs)-1].Type != string(MessageTypeSlug) {
		t.Fatalf("expected the slug marker last, got %+v", srcMsgs)
	}
	forked, err := database.ForkConversation(ctx, conv.ConversationID, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	forkedMsgs, err := database.ListMessages(ctx, forked.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range forkedMsgs {
		if m.Type == string(MessageTypeSlug) {
			t.Errorf("fork inherited a slug marker (%s); it would re-report the source's slug cost", m.MessageID)
		}
	}
	// Message-affiliated indirect usage still copies: that work IS part of the
	// forked history.
	if len(forkedMsgs) != 1 {
		t.Fatalf("forked messages = %+v, want just the user message", forkedMsgs)
	}
	if got := mustParseOtherUsage(t, forkedMsgs[0].OtherUsageData); len(got) != 1 || got[0].Purpose != "keyword_search" {
		t.Errorf("forked user message usage = %+v, want the keyword_search entry preserved", got)
	}
}

func mustParseOtherUsage(t *testing.T, raw *string) []llm.PurposedUsage {
	t.Helper()
	if raw == nil {
		return nil
	}
	var entries []llm.PurposedUsage
	if err := json.Unmarshal([]byte(*raw), &entries); err != nil {
		t.Fatalf("parse other usage %q: %v", *raw, err)
	}
	return entries
}

func TestGetSubagentOtherUsage(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, stringPtr("other-usage-parent"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(ctx, "other-usage-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := database.CreateSubagentConversation(ctx, "other-usage-grandchild", child.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}

	add := func(convID string, entries []llm.PurposedUsage) {
		t.Helper()
		if _, err := database.CreateMessage(ctx, CreateMessageParams{
			ConversationID: convID,
			Type:           MessageTypeUser,
			LLMData:        llm.Message{Role: llm.MessageRoleUser},
			OtherUsageData: entries,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The query now returns one row per entry (pricing/tiering happens later in
	// the handler); descendant entries come through individually while the
	// parent's own entry must NOT be counted.
	add(child.ConversationID, []llm.PurposedUsage{
		{Purpose: "keyword_search", Usage: llm.Usage{InputTokens: 100, CacheReadInputTokens: 7, OutputTokens: 10, CostUSD: 0.01, Model: "m1", URL: "u1"}},
		{Purpose: "llm_one_shot", Usage: llm.Usage{InputTokens: 50, OutputTokens: 5, CostUSD: 0.005, Model: "m1", URL: "u1"}},
	})
	add(grandchild.ConversationID, []llm.PurposedUsage{
		{Purpose: "subagent_progress", Usage: llm.Usage{InputTokens: 30, OutputTokens: 3, CostUSD: 0.003, Model: "m1", URL: "u1"}},
		{Purpose: "keyword_search", Usage: llm.Usage{InputTokens: 9, OutputTokens: 1, Model: "m2", URL: "u2"}},
		// No model/URL (omitempty drops them from the JSON): must scan as empty
		// strings, not fail scanning NULL.
		{Purpose: "tool_install", Usage: llm.Usage{InputTokens: 4, OutputTokens: 2}},
	})
	add(parent.ConversationID, []llm.PurposedUsage{
		{Purpose: "compaction", Usage: llm.Usage{InputTokens: 999, CostUSD: 9, Model: "m1", URL: "u1"}},
	})

	rows, err := database.GetSubagentOtherUsage(ctx, parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	// 5 descendant entries (2 child + 3 grandchild), one row each; parent excluded.
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5: %+v", len(rows), rows)
	}
	var m1Calls, totalInput int64
	var m1Cost float64
	var sawM2, sawNoModel bool
	for _, r := range rows {
		totalInput += r.InputTokens
		switch r.ModelName {
		case "m1":
			if r.LlmApiUrl != "u1" {
				t.Errorf("m1 row url = %q, want u1", r.LlmApiUrl)
			}
			m1Calls++
			m1Cost += r.CostUsd
		case "m2":
			sawM2 = true
			if r.LlmApiUrl != "u2" || r.InputTokens != 9 || r.OutputTokens != 1 {
				t.Errorf("m2 row = %+v", r)
			}
		case "":
			sawNoModel = true
			// The entry with no model/URL scans as empty strings, not NULL.
			if r.LlmApiUrl != "" || r.InputTokens != 4 || r.OutputTokens != 2 {
				t.Errorf("no-model row = %+v", r)
			}
		default:
			t.Errorf("unexpected model %q in row %+v", r.ModelName, r)
		}
	}
	if m1Calls != 3 || m1Cost < 0.0179 || m1Cost > 0.0181 {
		t.Errorf("m1 rows = %d calls, cost %v; want 3 calls, ~0.018", m1Calls, m1Cost)
	}
	if totalInput != 193 || !sawM2 || !sawNoModel {
		t.Errorf("rows = %+v; want total input 193 with m2 and no-model entries", rows)
	}
}
