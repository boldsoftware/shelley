package server

import (
	"encoding/json"
	"math"
	"net/http/httptest"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models/modelsdev"
)

func TestSubagentUsageCountsRequests(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(ctx, "request-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := database.CreateSubagentConversation(ctx, "request-grandchild", child.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	usage := llm.Usage{
		Model: "claude-opus-4-6", URL: "https://api.anthropic.com/v1/messages",
		InputTokens: 3_000_000, CacheCreationInputTokens: 1_000_000,
		CacheReadInputTokens: 3_000_000, OutputTokens: 300_000, CostUSD: 0.75,
		Requests: []llm.RequestUsage{
			{InputTokens: 1_000_000, CacheReadInputTokens: 2_000_000, OutputTokens: 100_000, CostUSD: 0.25},
			{InputTokens: 2_000_000, CacheCreationInputTokens: 1_000_000,
				CacheReadInputTokens: 1_000_000, OutputTokens: 200_000, CostUSD: 0.5},
		},
	}
	add := func(convID string, u llm.Usage, other bool) {
		t.Helper()
		params := db.CreateMessageParams{ConversationID: convID}
		if other {
			params.Type = db.MessageTypeUser
			params.OtherUsageData = []llm.PurposedUsage{{Purpose: "llm_one_shot", Usage: u}}
		} else {
			params.Type = db.MessageTypeAgent
			params.UsageData = u
			params.ModelName, params.LLMAPIURL = u.Model, u.URL
		}
		if _, err := database.CreateMessage(ctx, params); err != nil {
			t.Fatal(err)
		}
	}
	for _, other := range []bool{false, true} {
		add(parent.ConversationID, usage, other) // Parent usage is excluded.
		add(child.ConversationID, usage, other)
		legacy := usage
		legacy.Requests = nil
		add(grandchild.ConversationID, legacy, other)
		unknown := usage
		unknown.Model = "unpriced-request-model"
		add(grandchild.ConversationID, unknown, other)
	}
	w := httptest.NewRecorder()
	srv.handleSubagentUsage(w, httptest.NewRequest("GET", "/", nil), parent.ConversationID)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	type modelUsage struct {
		Model                    string  `json:"model"`
		URL                      string  `json:"url"`
		LLMCalls                 int64   `json:"llm_calls"`
		InputTokens              int64   `json:"input_tokens"`
		CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
		OutputTokens             int64   `json:"output_tokens"`
		EstimatedUSD             float64 `json:"estimated_usd"`
		ReportedUSD              float64 `json:"reported_usd"`
	}
	var got struct {
		LLMCalls            int64        `json:"llm_calls"`
		EstimatedUSD        float64      `json:"estimated_usd"`
		ReportedUSD         float64      `json:"reported_usd"`
		UnpricedReportedUSD float64      `json:"unpriced_reported_usd"`
		UnpricedCalls       int64        `json:"unpriced_calls"`
		PerModel            []modelUsage `json:"per_model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Use current flat catalog rates; this test is about request accounting,
	// not a snapshot of the pricing catalog.
	cost, found := modelsdev.LookupCost(usage.URL, usage.Model)
	if !found {
		t.Fatal("fixture model has no pricing")
	}
	estimate := (float64(usage.InputTokens)*cost.Input + float64(usage.CacheCreationInputTokens)*cost.CacheWrite +
		float64(usage.CacheReadInputTokens)*cost.CacheRead + float64(usage.OutputTokens)*cost.Output) / 1e6
	if got.LLMCalls != 10 || math.Abs(got.EstimatedUSD-4*estimate) > 1e-9 ||
		got.ReportedUSD != 4.5 || got.UnpricedReportedUSD != 1.5 || got.UnpricedCalls != 4 {
		t.Errorf("subagent totals = %+v", got)
	}
	if len(got.PerModel) != 2 {
		t.Fatalf("per-model usage = %+v", got.PerModel)
	}
	for _, row := range got.PerModel {
		multiplier, calls, estimated := int64(4), int64(6), 4*estimate
		if row.Model == "unpriced-request-model" {
			multiplier, calls, estimated = 2, 4, 0
		} else if row.Model != usage.Model {
			t.Fatalf("unexpected model %q", row.Model)
		}
		if row.URL != usage.URL || row.LLMCalls != calls ||
			row.InputTokens != multiplier*int64(usage.InputTokens) ||
			row.CacheCreationInputTokens != multiplier*int64(usage.CacheCreationInputTokens) ||
			row.CacheReadInputTokens != multiplier*int64(usage.CacheReadInputTokens) ||
			row.OutputTokens != multiplier*int64(usage.OutputTokens) ||
			row.ReportedUSD != float64(multiplier)*usage.CostUSD ||
			math.Abs(row.EstimatedUSD-estimated) > 1e-9 {
			t.Errorf("per-model usage = %+v", row)
		}
	}
}
