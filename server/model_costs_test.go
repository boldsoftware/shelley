package server

import (
	"encoding/json"
	"math"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models/modelsdev"
)

func TestModelCostsHandler(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)

	body := `{"models":[` +
		`{"model":"claude-opus-4-6","url":"https://llm.int.exe.xyz/v1/messages"},` +
		`{"model":"gpt-5.5-2026-04-23","url":"https://llm.int.exe.xyz/v1/responses"},` +
		`{"model":"gpt-6-astra","url":"https://llm.int.exe.xyz/v1/responses"},` +
		`{"model":"gpt-6-sol","url":"https://llm.int.exe.xyz/v1/responses"},` +
		`{"model":"gpt-6-luna","url":"https://llm.int.exe.xyz/v1/responses"},` +
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
	astra := res.Costs["gpt-6-astra"]
	if astra == nil || astra.Input != 10 || astra.Output != 50 || astra.CacheRead != 1 || astra.CacheWrite != 12.5 {
		t.Errorf("gpt-6-astra = %+v, want 10/50/1/12.5", astra)
	}
	sol := res.Costs["gpt-6-sol"]
	if sol == nil || sol.Input != 2 || sol.Output != 10 || sol.CacheRead != 0.2 || sol.CacheWrite != 2.5 {
		t.Errorf("gpt-6-sol = %+v, want 2/10/0.2/2.5", sol)
	}
	luna := res.Costs["gpt-6-luna"]
	if luna == nil || luna.Input != 0.1 || luna.Output != 0.5 || luna.CacheRead != 0.01 || luna.CacheWrite != 0.125 {
		t.Errorf("gpt-6-luna = %+v, want 0.1/0.5/0.01/0.125", luna)
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

	addUsage := func(convID, model, url string, in, cacheWrite, cacheRead, out int64, costUSD float64) {
		t.Helper()
		_, err := database.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: convID,
			Type:           db.MessageTypeAgent,
			UsageData: map[string]any{
				"input_tokens": in, "cache_creation_input_tokens": cacheWrite,
				"cache_read_input_tokens": cacheRead, "output_tokens": out,
				"model": model, "cost_usd": costUSD,
			},
			ModelName: model,
			LLMAPIURL: url,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	addOtherUsage := func(convID, model, url string, in, cacheWrite, cacheRead, out int64, costUSD float64) {
		t.Helper()
		_, err := database.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: convID,
			Type:           db.MessageTypeUser,
			LLMData:        llm.Message{Role: llm.MessageRoleUser},
			OtherUsageData: []llm.PurposedUsage{{
				Purpose: "test",
				Usage: llm.Usage{
					InputTokens: uint64(in), CacheCreationInputTokens: uint64(cacheWrite),
					CacheReadInputTokens: uint64(cacheRead), OutputTokens: uint64(out),
					CostUSD: costUSD, Model: model, URL: url,
				},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	const (
		opusURL = "https://llm.int.exe.xyz/v1/messages"
		gptURL  = "https://llm.int.exe.xyz/v1/responses"
	)
	// The parent's direct and indirect usage must not be counted.
	addUsage(parent.ConversationID, "claude-opus-4-6", opusURL, 9, 9, 9, 9, 9)
	addOtherUsage(parent.ConversationID, "claude-opus-4-6", opusURL, 9, 9, 9, 9, 9)
	// Direct and indirect usage for the same (model, URL) merge across descendants.
	addUsage(child.ConversationID, "claude-opus-4-6", opusURL, 1_000_000, 1_000_000, 1_000_000, 1_000_000, 1.25)
	addOtherUsage(grandchild.ConversationID, "claude-opus-4-6", opusURL, 1_000_000, 1_000_000, 1_000_000, 1_000_000, 0.75)
	// A different model remains a separate priced row.
	addUsage(grandchild.ConversationID, "gpt-5.5-2026-04-23", gptURL, 1_000_000, 0, 0, 1_000_000, 2)
	// The same model at different provider URLs uses each provider's pricing.
	addUsage(child.ConversationID, "qwen/qwen3.5-397b-a17b", "https://api.tokengo.com/v1", 1_000_000, 0, 0, 1_000_000, 0.5)
	addOtherUsage(grandchild.ConversationID, "qwen/qwen3.5-397b-a17b", "https://nano-gpt.com/api/v1", 1_000_000, 0, 0, 1_000_000, 0.25)
	// Provider-reported cost remains available for an unpriced model.
	addUsage(child.ConversationID, "mystery-model", "https://a.example/v1", 100, 200, 300, 400, 0.125)

	// A turn-ending tool's synthetic completion marker is not an LLM call.
	if err := srv.recordMessage(ctx, child.ConversationID, llm.Message{
		Role: llm.MessageRoleAssistant, EndOfTurn: true, ExcludedFromContext: true,
	}, llm.Usage{}, nil); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/conversation/"+parent.ConversationID+"/subagent-usage", nil)
	srv.handleSubagentUsage(w, req, parent.ConversationID)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	type perModelRow struct {
		Model                    string          `json:"model"`
		URL                      string          `json:"url"`
		LLMCalls                 int64           `json:"llm_calls"`
		InputTokens              int64           `json:"input_tokens"`
		CacheCreationInputTokens int64           `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64           `json:"cache_read_input_tokens"`
		OutputTokens             int64           `json:"output_tokens"`
		EstimatedUSD             float64         `json:"estimated_usd"`
		ReportedUSD              float64         `json:"reported_usd"`
		Cost                     *modelsdev.Cost `json:"cost"`
	}
	var res struct {
		LLMCalls            int64         `json:"llm_calls"`
		EstimatedUSD        float64       `json:"estimated_usd"`
		ReportedUSD         float64       `json:"reported_usd"`
		UnpricedReportedUSD float64       `json:"unpriced_reported_usd"`
		UnpricedModels      []string      `json:"unpriced_models"`
		UnpricedCalls       int64         `json:"unpriced_calls"`
		PerModel            []perModelRow `json:"per_model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.LLMCalls != 6 {
		t.Errorf("llm_calls = %d, want 6 (parent excluded, grandchild included)", res.LLMCalls)
	}
	if math.Abs(res.EstimatedUSD-115.75) > 1e-9 {
		t.Errorf("estimated_usd = %v, want 115.75", res.EstimatedUSD)
	}
	if math.Abs(res.ReportedUSD-4.875) > 1e-9 {
		t.Errorf("reported_usd = %v, want 4.875", res.ReportedUSD)
	}
	if math.Abs(res.UnpricedReportedUSD-0.125) > 1e-9 {
		t.Errorf("unpriced_reported_usd = %v, want 0.125", res.UnpricedReportedUSD)
	}
	if res.UnpricedCalls != 1 {
		t.Errorf("unpriced_calls = %d, want 1", res.UnpricedCalls)
	}
	if len(res.UnpricedModels) != 1 || res.UnpricedModels[0] != "mystery-model" {
		t.Errorf("unpriced_models = %v, want [mystery-model]", res.UnpricedModels)
	}
	if len(res.PerModel) != 5 {
		t.Fatalf("per_model = %+v, want 5 rows", res.PerModel)
	}

	opus := res.PerModel[0]
	if opus.Model != "claude-opus-4-6" || opus.URL != opusURL || opus.LLMCalls != 2 ||
		opus.InputTokens != 2_000_000 || opus.CacheCreationInputTokens != 2_000_000 ||
		opus.CacheReadInputTokens != 2_000_000 || opus.OutputTokens != 2_000_000 ||
		math.Abs(opus.EstimatedUSD-73.5) > 1e-9 || opus.ReportedUSD != 2 || opus.Cost == nil {
		t.Errorf("per_model[0] = %+v, want merged priced opus usage", opus)
	}
	gpt := res.PerModel[1]
	if gpt.Model != "gpt-5.5-2026-04-23" || gpt.URL != gptURL || gpt.LLMCalls != 1 ||
		gpt.InputTokens != 1_000_000 || gpt.OutputTokens != 1_000_000 ||
		math.Abs(gpt.EstimatedUSD-35) > 1e-9 || gpt.ReportedUSD != 2 || gpt.Cost == nil {
		t.Errorf("per_model[1] = %+v, want gpt usage", gpt)
	}
	mystery := res.PerModel[2]
	if mystery.Model != "mystery-model" || mystery.URL != "https://a.example/v1" ||
		mystery.LLMCalls != 1 || mystery.InputTokens != 100 || mystery.CacheCreationInputTokens != 200 ||
		mystery.CacheReadInputTokens != 300 || mystery.OutputTokens != 400 ||
		mystery.EstimatedUSD != 0 || mystery.ReportedUSD != 0.125 || mystery.Cost != nil {
		t.Errorf("per_model[2] = %+v, want unpriced reported usage", mystery)
	}
	qwenTokengo, qwenNano := res.PerModel[3], res.PerModel[4]
	if qwenTokengo.Model != "qwen/qwen3.5-397b-a17b" || qwenTokengo.URL != "https://api.tokengo.com/v1" ||
		qwenTokengo.LLMCalls != 1 || qwenTokengo.Cost == nil || qwenTokengo.Cost.Input != 0.4 ||
		qwenTokengo.Cost.Output != 2.65 || math.Abs(qwenTokengo.EstimatedUSD-3.05) > 1e-9 || qwenTokengo.ReportedUSD != 0.5 {
		t.Errorf("per_model[3] = %+v, want Tokengo pricing", qwenTokengo)
	}
	if qwenNano.Model != "qwen/qwen3.5-397b-a17b" || qwenNano.URL != "https://nano-gpt.com/api/v1" ||
		qwenNano.LLMCalls != 1 || qwenNano.Cost == nil || qwenNano.Cost.Input != 0.6 ||
		qwenNano.Cost.Output != 3.6 || math.Abs(qwenNano.EstimatedUSD-4.2) > 1e-9 || qwenNano.ReportedUSD != 0.25 {
		t.Errorf("per_model[4] = %+v, want Nano-GPT pricing", qwenNano)
	}

	var rowCalls int64
	var rowEstimatedUSD, rowReportedUSD float64
	for _, row := range res.PerModel {
		rowCalls += row.LLMCalls
		rowEstimatedUSD += row.EstimatedUSD
		rowReportedUSD += row.ReportedUSD
	}
	if rowCalls != res.LLMCalls || math.Abs(rowEstimatedUSD-res.EstimatedUSD) > 1e-9 || math.Abs(rowReportedUSD-res.ReportedUSD) > 1e-9 {
		t.Errorf("per_model totals = calls %d, estimated %v, reported %v; aggregate = calls %d, estimated %v, reported %v",
			rowCalls, rowEstimatedUSD, rowReportedUSD, res.LLMCalls, res.EstimatedUSD, res.ReportedUSD)
	}

	// A conversation with no subagents returns an empty, non-null per_model array.
	leaf, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	w2 := httptest.NewRecorder()
	srv.handleSubagentUsage(w2, req, leaf.ConversationID)
	var res2 struct {
		LLMCalls int64           `json:"llm_calls"`
		PerModel json.RawMessage `json:"per_model"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &res2); err != nil {
		t.Fatal(err)
	}
	if res2.LLMCalls != 0 {
		t.Errorf("leaf llm_calls = %d, want 0", res2.LLMCalls)
	}
	if string(res2.PerModel) != "[]" {
		t.Errorf("leaf per_model = %s, want []", res2.PerModel)
	}
}
