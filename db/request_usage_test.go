package db

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

// The request breakdown uses the existing JSON columns. Both direct and
// indirect usage must survive storage and conversation forks unchanged.
func TestRequestUsageRoundTrip(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := t.Context()
	conv, err := database.CreateConversation(ctx, stringPtr("request-usage"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	middle := start.Add(time.Second)
	end := start.Add(3 * time.Second)
	usage := llm.Usage{
		InputTokens: 30, CacheCreationInputTokens: 3, CacheReadInputTokens: 7, OutputTokens: 11,
		CostUSD: 0.75, Model: "model", URL: "https://example.com/v1/messages",
		StartTime: &start, EndTime: &end,
		Requests: []llm.RequestUsage{
			{InputTokens: 10, CacheCreationInputTokens: 1, CacheReadInputTokens: 3, OutputTokens: 5,
				CostUSD: 0.25, StartTime: &start, EndTime: &middle},
			{InputTokens: 20, CacheCreationInputTokens: 2, CacheReadInputTokens: 4, OutputTokens: 6,
				CostUSD: 0.5, StartTime: &middle, EndTime: &end},
		},
	}
	legacy := usage
	legacy.Requests = nil
	for _, want := range []llm.Usage{usage, legacy} {
		other := []llm.PurposedUsage{{Purpose: "llm_one_shot", Usage: want}}
		_, err := database.CreateMessage(ctx, CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           MessageTypeAgent, LLMData: llm.Message{Role: llm.MessageRoleAssistant},
			UsageData: want, OtherUsageData: other, ModelName: want.Model, LLMAPIURL: want.URL,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	messages, err := database.ListMessages(ctx, conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}
	fork, err := database.ForkConversation(ctx, conv.ConversationID, messages[1].SequenceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{conv.ConversationID, fork.ConversationID} {
		messages, err := database.ListMessages(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) != 2 {
			t.Fatalf("got %d messages, want 2", len(messages))
		}
		for i, want := range []llm.Usage{usage, legacy} {
			msg := messages[i]
			if msg.UsageData == nil || msg.OtherUsageData == nil {
				t.Fatalf("missing usage: %+v", msg)
			}
			var got llm.Usage
			if err := json.Unmarshal([]byte(*msg.UsageData), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("stored usage = %+v, want %+v", got, want)
			}
			var other []llm.PurposedUsage
			if err := json.Unmarshal([]byte(*msg.OtherUsageData), &other); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(other, []llm.PurposedUsage{{Purpose: "llm_one_shot", Usage: want}}) {
				t.Errorf("stored other usage = %+v", other)
			}
			if len(got.RequestBreakdown()) != len(want.RequestBreakdown()) {
				t.Errorf("stored request count = %d, want %d", len(got.RequestBreakdown()), len(want.RequestBreakdown()))
			}
		}
	}
}

func TestSubagentRequestUsageShapes(t *testing.T) {
	for _, test := range []struct {
		name     string
		usage    string
		calls    int64
		input    int64
		reported float64
	}{
		{"legacy", `{"input_tokens":30,"cost_usd":0.75}`, 1, 30, 0.75},
		{"empty", `{"input_tokens":30,"cost_usd":0.75,"requests":[]}`, 1, 30, 0.75},
		{"null", `{"input_tokens":30,"cost_usd":0.75,"requests":null}`, 1, 30, 0.75},
		// Deliberately different aggregate fields prove the breakdown is
		// authoritative, not added to (or replaced by) the message totals.
		{"requests", `{"input_tokens":999,"cost_usd":999,"requests":[{"input_tokens":10,"cost_usd":0.25},{"input_tokens":20,"cost_usd":0.5}]}`, 2, 30, 0.75},
		{"zero requests", `{"requests":[{},{}]}`, 2, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := setupTestDB(t)
			defer database.Close()
			ctx := t.Context()
			parent, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
			if err != nil {
				t.Fatal(err)
			}
			child, err := database.CreateSubagentConversation(ctx, "child", parent.ConversationID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.CreateMessage(ctx, CreateMessageParams{
				ConversationID: child.ConversationID, Type: MessageTypeAgent,
				UsageData:      json.RawMessage(test.usage),
				OtherUsageData: json.RawMessage("[" + test.usage + "]"),
			}); err != nil {
				t.Fatal(err)
			}
			direct, err := database.GetSubagentUsage(ctx, parent.ConversationID)
			if err != nil {
				t.Fatal(err)
			}
			if len(direct) != 1 || direct[0].LlmCalls != test.calls ||
				direct[0].InputTokens != test.input || direct[0].CostUsd != test.reported {
				t.Errorf("direct usage = %+v", direct)
			}
			indirect, err := database.GetSubagentOtherUsage(ctx, parent.ConversationID)
			if err != nil {
				t.Fatal(err)
			}
			if len(indirect) != 1 || indirect[0].LlmCalls != test.calls ||
				indirect[0].InputTokens != test.input || indirect[0].CostUsd != test.reported {
				t.Errorf("indirect usage = %+v", indirect)
			}
		})
	}
}
