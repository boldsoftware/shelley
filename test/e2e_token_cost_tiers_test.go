package test

import "testing"

// Ported from ui/e2e/token-cost-tiers.spec.ts.

// Context-size pricing tiers (issue #322): a model like Claude Haiku 5.5
// bills 5x more once a call's prompt exceeds 100k tokens. The cost graph
// must price each call at the tier its prompt lands in, not always at the
// base rate.
func TestE2ETokenCostTiersPricesByContextTier(t *testing.T) {
	// Call 1: 60k prompt (< 100k) -> base rate. input 60k*$0.1/M=$0.006,
	// output 10k*$0.5/M=$0.005. Call 2: 150k prompt (> 100k) -> tier rate.
	// input 150k*$0.5/M=$0.075, output 10k*$2.5/M=$0.025. Total $0.111. At
	// base rate throughout it would be only $0.031, so the number itself
	// proves the tier was applied.
	e2e(t, `The cost graph prices calls by the context tier their prompt lands in. The model catalog and pricing are mocked: the model bills 5x once a call's prompt exceeds 100k tokens. The conversation at /c/{{SLUG}} serves usage for two calls, one below and one above the threshold. Perform the steps in order.
`+tokenCostOpenGraph+`6. The popup's [data-testid="token-cost-total"] text contains "≈$0.111" (at the base rate throughout it would be only $0.031).
7. The calls straddle the threshold, so each band was served at two rates: the first ".token-cost-legend-unit" element in the popup whose text contains "mixed rates" is visible.`, func(s *shelley) {
		s.viewport(1280, 900)
		const model = "claude-haiku-5-5"
		s.tokenCostCatalog([]map[string]any{
			{"id": "predictable", "ready": true},
			{"id": model + "@integration", "api_model_name": model, "base_url": "https://anthropic.test", "ready": true},
		}, map[string]any{model: map[string]any{
			"input": 0.1, "output": 0.5, "cache_read": 0.01, "cache_write": 0.125,
			"tiers": []any{map[string]any{"context": 100000, "input": 0.5, "output": 2.5, "cache_read": 0.05, "cache_write": 0.625}},
		}})
		conv := s.loremipsum("1")
		body := s.conversationBody(conv.ID)
		var agents []map[string]any
		for _, m := range messagesOf(body) {
			if m["type"] == "agent" {
				agents = append(agents, m)
			}
		}
		if len(agents) < 2 {
			s.fail("loremipsum 1 made %d agent messages, want at least 2", len(agents))
		}
		// Only the first two calls carry cost; any extras the generator adds
		// stay at zero tokens so the total stays an exact, tier-revealing
		// number.
		prompts := []int{60000, 150000}
		for i, m := range agents {
			input, output := 0, 0
			if i < len(prompts) {
				input, output = prompts[i], 10000
			}
			m["usage_data"] = mustJSON(map[string]any{
				"model": model, "url": "https://anthropic.test/v1/messages",
				"input_tokens": input, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0,
				"output_tokens": output, "cost_usd": 0,
			})
		}
		s.serveConversation(conv.ID, body)
		s.v("SLUG", body["conversation"].(map[string]any)["slug"].(string))
	})
}
