package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/models/modelsdev"
)

func TestModelCostsHandler(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)

	body := `{"models":[` +
		`{"model":"claude-opus-4-6","url":"https://llm.int.exe.xyz/v1/messages"},` +
		`{"model":"gpt-5.5-2026-04-23","url":"https://llm.int.exe.xyz/v1/responses"},` +
		`{"model":"predictable-v1"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/model-costs", strings.NewReader(body))
	srv.handleModelCosts(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var res struct {
		Costs map[string]*modelsdev.Cost `json:"costs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	opus := res.Costs["claude-opus-4-6"]
	if opus == nil || opus.Input != 5 || opus.Output != 25 || opus.CacheRead != 0.5 || opus.CacheWrite != 6.25 {
		t.Errorf("claude-opus-4-6 = %+v, want 5/25/0.5/6.25", opus)
	}
	gpt := res.Costs["gpt-5.5-2026-04-23"]
	if gpt == nil || gpt.Input != 5 || gpt.Output != 30 {
		t.Errorf("gpt-5.5-2026-04-23 = %+v, want input=5 output=30", gpt)
	}
	if got, ok := res.Costs["predictable-v1"]; !ok || got != nil {
		t.Errorf("predictable-v1 = %+v (present %v), want explicit null", got, ok)
	}
}

func TestSubagentUsageHandler(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	ctx := t.Context()

	parent, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(ctx, "sub-1", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := database.CreateSubagentConversation(ctx, "sub-2", child.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}

	addUsage := func(convID, model, url string, in, out int64, costUsd float64) {
		t.Helper()
		_, err := database.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: convID,
			Type:           db.MessageTypeAgent,
			UsageData: map[string]any{
				"input_tokens": in, "output_tokens": out,
				"model": model, "cost_usd": costUsd,
			},
			ModelName: model,
			LLMAPIURL: url,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Parent usage must NOT be counted.
	addUsage(parent.ConversationID, "claude-opus-4-6", "https://llm.int.exe.xyz/v1/messages", 1_000_000, 0, 0)
	// Child: priced. 1M input @$5 + 1M output @$25 = $30.
	addUsage(child.ConversationID, "claude-opus-4-6", "https://llm.int.exe.xyz/v1/messages", 1_000_000, 1_000_000, 1.25)
	// Grandchild (recursive): unpriced model with provider-reported cost.
	addUsage(grandchild.ConversationID, "mystery-model", "", 500, 500, 0.75)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/conversation/"+parent.ConversationID+"/subagent-usage", nil)
	srv.handleSubagentUsage(w, req, parent.ConversationID)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var res struct {
		LLMCalls            int64    `json:"llm_calls"`
		EstimatedUsd        float64  `json:"estimated_usd"`
		ReportedUsd         float64  `json:"reported_usd"`
		UnpricedReportedUsd float64  `json:"unpriced_reported_usd"`
		UnpricedModels      []string `json:"unpriced_models"`
		UnpricedCalls       int64    `json:"unpriced_calls"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.LLMCalls != 2 {
		t.Errorf("llm_calls = %d, want 2 (parent excluded, grandchild included)", res.LLMCalls)
	}
	if res.EstimatedUsd < 29.99 || res.EstimatedUsd > 30.01 {
		t.Errorf("estimated_usd = %v, want ~30", res.EstimatedUsd)
	}
	if res.ReportedUsd != 2 {
		t.Errorf("reported_usd = %v, want 2", res.ReportedUsd)
	}
	if res.UnpricedReportedUsd != 0.75 {
		t.Errorf("unpriced_reported_usd = %v, want 0.75", res.UnpricedReportedUsd)
	}
	if len(res.UnpricedModels) != 1 || res.UnpricedModels[0] != "mystery-model" || res.UnpricedCalls != 1 {
		t.Errorf("unpriced = %v / %d calls, want [mystery-model] / 1", res.UnpricedModels, res.UnpricedCalls)
	}

	// A conversation with no subagents returns zeros.
	leaf, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	w2 := httptest.NewRecorder()
	srv.handleSubagentUsage(w2, req, leaf.ConversationID)
	var res2 struct {
		LLMCalls int64 `json:"llm_calls"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &res2); err != nil {
		t.Fatal(err)
	}
	if res2.LLMCalls != 0 {
		t.Errorf("leaf llm_calls = %d, want 0", res2.LLMCalls)
	}
}
