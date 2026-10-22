package server

import (
	"encoding/json"
	"net/http"
	"sort"

	"shelley.exe.dev/models/modelsdev"
)

// handleModelCosts resolves pricing (USD per million tokens) for a batch of
// (model, url) pairs seen in a conversation's usage data. Models without
// pricing map to null.
func (s *Server) handleModelCosts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Models []struct {
			Model string `json:"model"`
			URL   string `json:"url"`
		} `json:"models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	costs := make(map[string]*modelsdev.Cost, len(req.Models))
	for _, m := range req.Models {
		if m.Model == "" {
			continue
		}
		if c, found := modelsdev.LookupCost(m.URL, m.Model); found {
			costs[m.Model] = &c
		} else {
			costs[m.Model] = nil
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"costs": costs})
}

// handleSubagentUsage aggregates LLM usage across a conversation's subagents
// (recursively) and prices it. The UI shows per-model breakdowns and a separate
// subtotal; subagent calls are not part of the direct-usage graph.
// Descendants' indirect usage (other_usage_data entries) is included.
func (s *Server) handleSubagentUsage(w http.ResponseWriter, r *http.Request, conversationID string) {
	rows, err := s.db.GetSubagentUsage(r.Context(), conversationID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	otherRows, err := s.db.GetSubagentOtherUsage(r.Context(), conversationID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type perModelUsage struct {
		Model                    string          `json:"model"`
		URL                      string          `json:"url"`
		LLMCalls                 int64           `json:"llm_calls"`
		InputTokens              int64           `json:"input_tokens"`
		CacheCreationInputTokens int64           `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64           `json:"cache_read_input_tokens"`
		OutputTokens             int64           `json:"output_tokens"`
		EstimatedUsd             float64         `json:"estimated_usd"`
		ReportedUsd              float64         `json:"reported_usd"`
		Cost                     *modelsdev.Cost `json:"cost"`
	}
	var resp struct {
		LLMCalls            int64           `json:"llm_calls"`
		EstimatedUsd        float64         `json:"estimated_usd"`
		ReportedUsd         float64         `json:"reported_usd"`
		UnpricedReportedUsd float64         `json:"unpriced_reported_usd"`
		UnpricedModels      []string        `json:"unpriced_models"`
		UnpricedCalls       int64           `json:"unpriced_calls"`
		PerModel            []perModelUsage `json:"per_model"`
	}
	resp.UnpricedModels = []string{}
	resp.PerModel = []perModelUsage{}
	// Each call is priced by the context-size tier its own prompt lands in, so
	// calls of one (model, url) that crossed a tier split into separate rows,
	// one per effective rate. The UI groups them back under the model and shows
	// "mixed rates" per band when they differ, while the totals stay exact.
	type bucketKey struct {
		model, url                           string
		priced                               bool
		input, output, cacheRead, cacheWrite float64
	}
	buckets := make(map[bucketKey]*perModelUsage)
	seenUnpriced := make(map[string]bool)
	fold := func(model, url string, in, cacheWrite, cacheRead, out int64, costUsd float64) {
		resp.LLMCalls++
		resp.ReportedUsd += costUsd

		cost, found := modelsdev.LookupCost(url, model)
		var rates modelsdev.Cost
		if found {
			// The prompt is everything sent to the model: fresh input plus the
			// cached prefix it reused. AtContext reprices the whole call when
			// that exceeds a tier threshold.
			rates = cost.AtContext(int(in + cacheWrite + cacheRead))
		}
		key := bucketKey{
			model: model, url: url, priced: found,
			input: rates.Input, output: rates.Output,
			cacheRead: rates.CacheRead, cacheWrite: rates.CacheWrite,
		}
		row := buckets[key]
		if row == nil {
			row = &perModelUsage{Model: model, URL: url}
			if found {
				effective := rates // flat rates for this tier; no nested Tiers
				row.Cost = &effective
			}
			buckets[key] = row
		}
		row.LLMCalls++
		row.InputTokens += in
		row.CacheCreationInputTokens += cacheWrite
		row.CacheReadInputTokens += cacheRead
		row.OutputTokens += out
		row.ReportedUsd += costUsd

		if found {
			estimatedUsd := float64(in)*rates.Input/1e6 +
				float64(cacheWrite)*rates.CacheWrite/1e6 +
				float64(cacheRead)*rates.CacheRead/1e6 +
				float64(out)*rates.Output/1e6
			row.EstimatedUsd += estimatedUsd
			resp.EstimatedUsd += estimatedUsd
		} else {
			resp.UnpricedReportedUsd += costUsd
			resp.UnpricedCalls++
			if !seenUnpriced[model] {
				seenUnpriced[model] = true
				resp.UnpricedModels = append(resp.UnpricedModels, model)
			}
		}
	}
	for _, row := range rows {
		model, url := "", ""
		if row.ModelName != nil {
			model = *row.ModelName
		}
		if row.LlmApiUrl != nil {
			url = *row.LlmApiUrl
		}
		fold(model, url, row.InputTokens, row.CacheCreationInputTokens, row.CacheReadInputTokens, row.OutputTokens, row.CostUsd)
	}
	for _, row := range otherRows {
		fold(row.ModelName, row.LlmApiUrl, row.InputTokens, row.CacheCreationInputTokens, row.CacheReadInputTokens, row.OutputTokens, row.CostUsd)
	}
	for _, row := range buckets {
		resp.PerModel = append(resp.PerModel, *row)
	}
	sort.Slice(resp.PerModel, func(i, j int) bool {
		a, b := resp.PerModel[i], resp.PerModel[j]
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.URL != b.URL {
			return a.URL < b.URL
		}
		// Within a model+url, order tiers by input rate (base first).
		return costInput(a.Cost) < costInput(b.Cost)
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// costInput is a nil-safe accessor for a row's effective input rate, used only
// to order a model's tier rows deterministically (base rate first).
func costInput(c *modelsdev.Cost) float64 {
	if c == nil {
		return 0
	}
	return c.Input
}
