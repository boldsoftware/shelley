package test

import (
	"maps"
	"net/http"
	"strconv"
	"testing"
)

// Ported from ui/e2e/token-cost-subagents.spec.ts.

var (
	tokenCostPrice    = map[string]any{"input": 2, "output": 8, "cache_read": 0.2, "cache_write": 2.5}
	tokenCostReviewer = map[string]any{
		"model":                       "review-model",
		"url":                         "https://review.test",
		"llm_calls":                   4,
		"input_tokens":                2_000_000,
		"output_tokens":               250_000,
		"cache_read_input_tokens":     5_000_000,
		"cache_creation_input_tokens": 200_000,
		"estimated_usd":               7.5,
		"reported_usd":                999,
		"cost":                        tokenCostPrice,
	}
	tokenCostUnpriced = map[string]any{
		"model":                       "unpriced-model",
		"url":                         "https://unpriced.test",
		"llm_calls":                   1,
		"input_tokens":                3000,
		"output_tokens":               500,
		"cache_read_input_tokens":     0,
		"cache_creation_input_tokens": 0,
		"estimated_usd":               0,
		"reported_usd":                0.75,
		"cost":                        nil,
	}
	tokenCostUsage = map[string]any{
		"llm_calls":             5,
		"estimated_usd":         7.5,
		"reported_usd":          999.75,
		"unpriced_reported_usd": 0.75,
		"unpriced_models":       []string{"unpriced-model"},
		"unpriced_calls":        1,
		"per_model":             []any{tokenCostReviewer, tokenCostUnpriced},
	}
)

// tokenCostWith is m with the given changes.
func tokenCostWith(m map[string]any, kv ...any) map[string]any {
	m = maps.Clone(m)
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

// tokenCostSubagentUsage answers every conversation's sub-agent usage with
// usage (nil for a 500 "unavailable").
func (s *shelley) tokenCostSubagentUsage(usage map[string]any) {
	s.route("GET /api/conversation/{id}/subagent-usage", func(w http.ResponseWriter, r *http.Request) {
		if usage == nil {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON(w, usage)
	})
}

// tokenCostSpend makes the conversation {{SLUG}} whose spend the tests
// open: priced main-model usage on its first agent message (direct) or no
// usage at all, with model pricing served or failing (503).
func (s *shelley) tokenCostSpend(direct, pricingFailed bool) {
	// The page POSTs the models it needs priced.
	s.route("/api/model-costs", func(w http.ResponseWriter, r *http.Request) {
		if pricingFailed {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]any{"costs": map[string]any{"main-model": tokenCostPrice}})
	})
	c := s.loremipsum("1")
	body := s.conversationBody(c.ID)
	var agents []map[string]any
	for _, m := range messagesOf(body) {
		m["usage_data"] = nil
		m["other_usage_data"] = nil
		if m["type"] == "agent" {
			agents = append(agents, m)
		}
	}
	if direct {
		agents[0]["usage_data"] = mustJSON(map[string]any{
			"model":                       "main-model",
			"input_tokens":                1_000_000,
			"output_tokens":               100_000,
			"cache_read_input_tokens":     2_000_000,
			"cache_creation_input_tokens": 100_000,
			"cost_usd":                    999,
		})
		agents[0]["other_usage_data"] = mustJSON([]any{
			map[string]any{"purpose": "compaction", "model": "main-model", "input_tokens": 100_000, "output_tokens": 10_000},
		})
	}
	s.serveConversation(c.ID, body)
	s.v("SLUG", c.Slug)
}

const tokenCostSpendSetup = `The conversation at /c/{{SLUG}} serves mocked usage and pricing. To "open the spend popup": navigate to /c/{{SLUG}}, then click the VISIBLE ".context-usage-label" element (a hidden one may exist too; pick the visible one). "The popup" is the ".chat-context-popup" element; "the table" is the element inside the popup with role "table" and accessible name "Spend by model". When a step says an element's text "matches" a regular expression, test the regular expression against the element's raw textContent in an eval that has "expect". Perform the steps in order. `

// tokenCostPerModel checks main and sub-agent spend side by side at width.
func tokenCostPerModel(t *testing.T, width int) {
	w := strconv.Itoa(width)
	e2e(t, tokenCostSpendSetup+`The spend popup shows per-model main and sub-agent spend with subtotals at a `+w+`px-wide window.
1. Open the spend popup. The table is visible.
2. The table's column headers (role "columnheader" inside the table) have exactly these texts, in order: "Model / tokens", "Main conversation", "Sub-agents".
3. The table's [data-testid="conversation-cost-subtotal"] text contains "$3.73"; its [data-testid="subagent-cost-row"] text contains "$8.25"; its [data-testid="token-cost-total"] text contains "≈$11.98".
4. "The breakdowns" are the table's ".token-cost-model-breakdown" elements: there are exactly 3.
5. The first breakdown's ".token-cost-model-row" text matches /main-model.*\$3.45.*—/, and the second breakdown's ".token-cost-model-row" text matches /review-model.*—.*\$7.50/.
6. The second breakdown's '.token-cost-legend-row [data-scope="subagents"]' elements are exactly 4, and their texts match, in order: /250k\s*@ \$8\/M\s*\$2.00/, /2.0M\s*@ \$2\/M\s*\$4.00/, /200k\s*@ \$2.50\/M\s*\$0.500/ and /5.0M\s*@ \$0.20\/M\s*\$1.00/.
7. The third breakdown's text contains "unpriced-model" and contains "$0.750 reported"; it has no ".token-cost-legend-row .token-cost-legend-unit" element (count 0).
8. The popup's text contains "1 call has no model pricing" and contains "Sub-agent model totals include nested sub-agents and indirect usage.".
9. The table's text contains "Other (indirect)" and contains "in model totals".
10. In one eval with expect "true", using getBoundingClientRect(): the table's second column header ("Main conversation") and third column header ("Sub-agents") have exactly the same y; the third's x is >= the second's x + width; the popup's x is >= 0 and its x + width is <= `+w+`; and the popup's scrollWidth <= its clientWidth.`, func(s *shelley) {
		s.viewport(width, 900)
		s.tokenCostSubagentUsage(tokenCostUsage)
		s.tokenCostSpend(true, false)
	})
}

func TestE2ETokenCostSubagentsPerModel768(t *testing.T) { tokenCostPerModel(t, 768) }

func TestE2ETokenCostSubagentsPerModel1280(t *testing.T) { tokenCostPerModel(t, 1280) }

func TestE2ETokenCostSubagentsWithoutDirectUsage(t *testing.T) {
	e2e(t, tokenCostSpendSetup+`The spend popup shows sub-agent models and totals for a conversation without direct usage.
1. Open the spend popup.
2. The popup's text contains "No direct usage data yet.".
3. The table's text contains "review-model".
4. The popup's [data-testid="token-cost-total"] text contains "≈$8.25".`, func(s *shelley) {
		s.viewport(1280, 900)
		s.tokenCostSubagentUsage(tokenCostUsage)
		s.tokenCostSpend(false, false)
	})
}

func TestE2ETokenCostSubagentsUnpricedNotFree(t *testing.T) {
	e2e(t, tokenCostSpendSetup+`Unpriced sub-agent usage is not presented as free.
1. Open the spend popup.
2. The table's ".token-cost-model-breakdown" text contains "no pricing".
3. The table's [data-testid="subagent-cost-row"] text contains "no pricing".
4. The popup's text contains "total may be incomplete".
5. The popup's [data-testid="token-cost-total"] is hidden (or absent).`, func(s *shelley) {
		s.viewport(1280, 900)
		s.tokenCostSubagentUsage(tokenCostWith(tokenCostUsage,
			"llm_calls", 1, "estimated_usd", 0, "reported_usd", 0, "unpriced_reported_usd", 0,
			"per_model", []any{tokenCostWith(tokenCostUnpriced, "reported_usd", 0)}))
		s.tokenCostSpend(false, false)
	})
}

func TestE2ETokenCostSubagentsCompactWithoutSubagents(t *testing.T) {
	e2e(t, tokenCostSpendSetup+`The spend popup stays compact for a conversation without sub-agents.
1. Open the spend popup.
2. The popup's [data-testid="token-cost-total"] has exactly the text "Total≈$3.73".
3. The popup has no element with role "columnheader" whose accessible name is exactly "Sub-agents" (count 0).
4. The popup's getBoundingClientRect().width is exactly 460.`, func(s *shelley) {
		s.viewport(1280, 900)
		s.tokenCostSpend(true, false)
	})
}

func TestE2ETokenCostSubagentsZeroTotalWhenFree(t *testing.T) {
	e2e(t, tokenCostSpendSetup+`The spend popup shows a zero total when sub-agent pricing is known and free.
1. Open the spend popup.
2. The popup's [data-testid="subagent-cost-row"] has exactly the text "$0".
3. The popup's [data-testid="token-cost-total"] has exactly the text "Total≈$0".
4. The popup's text does not contain "no pricing".`, func(s *shelley) {
		s.viewport(1280, 900)
		s.tokenCostSubagentUsage(tokenCostWith(tokenCostUsage,
			"llm_calls", 4, "estimated_usd", 0, "reported_usd", 0, "unpriced_reported_usd", 0,
			"unpriced_calls", 0, "unpriced_models", []string{},
			"per_model", []any{tokenCostWith(tokenCostReviewer, "estimated_usd", 0, "reported_usd", 0,
				"cost", map[string]any{"input": 0, "output": 0, "cache_read": 0, "cache_write": 0})}))
		s.tokenCostSpend(false, false)
	})
}

func TestE2ETokenCostSubagentsFetchFailureWarns(t *testing.T) {
	e2e(t, tokenCostSpendSetup+`The spend popup warns when sub-agent details cannot be fetched (the sub-agent usage request fails with HTTP 500).
1. Open the spend popup.
2. The popup's [data-testid="token-cost-total"] has exactly the text "Total≈$3.73".
3. The popup's text contains "Subagent cost unavailable; total may be incomplete.".
4. The popup's text does not contain "Loading total".`, func(s *shelley) {
		s.viewport(1280, 900)
		s.tokenCostSubagentUsage(nil)
		s.tokenCostSpend(true, false)
	})
}

func TestE2ETokenCostSubagentsMergesEndpointPrices(t *testing.T) {
	e2e(t, tokenCostSpendSetup+`The spend popup merges endpoint-specific prices into one model row without inventing a shared rate.
1. Open the spend popup.
2. "The model" is the popup's ".token-cost-model-breakdown" element whose text contains "review-model": there is exactly 1 such element.
3. The model's '.token-cost-model-row [data-scope="subagents"]' has exactly the text "$19.00".
4. "The input row" is the element with role "row" inside the model that contains an element with role "rowheader" whose accessible name is exactly "Input". The input row's [data-scope="subagents"] element has exactly the text "4.0Mmixed rates$12.00" (whitespace collapsed).
5. The popup's [data-testid="subagent-cost-row"] has computed border-top-style exactly "solid", and so does the popup's [data-testid="conversation-cost-subtotal"].`, func(s *shelley) {
		s.viewport(1280, 900)
		s.tokenCostSubagentUsage(tokenCostWith(tokenCostUsage,
			"llm_calls", 8, "estimated_usd", 19, "reported_usd", 1998, "unpriced_reported_usd", 0,
			"unpriced_models", []string{}, "unpriced_calls", 0,
			"per_model", []any{tokenCostReviewer, tokenCostWith(tokenCostReviewer,
				"url", "https://second.test/another-provider", "estimated_usd", 11.5,
				"cost", tokenCostWith(tokenCostPrice, "input", 4))}))
		s.tokenCostSpend(true, false)
	})
}

func TestE2ETokenCostSubagentsFailedPricingNotUnpriced(t *testing.T) {
	e2e(t, tokenCostSpendSetup+`Failed pricing lookups (the model costs request fails with HTTP 503) are not counted as confirmed unpriced calls.
1. Open the spend popup.
2. The popup's text contains "Pricing lookup failed" and contains "1 call has no model pricing", and does not contain "3 calls have no model pricing".
3. "The indirect row" is the element with role "row" in the popup that contains an element with role "rowheader" whose accessible name is exactly "compaction". Inside the indirect row's ".token-cost-cell" elements, find the element whose whole text is exactly "1 call" and the element whose whole text is exactly "no pricing" (the innermost such elements). In one eval with expect "true", their getBoundingClientRect() boxes do not overlap: one ends left of (or exactly where) the other starts, horizontally or vertically.`, func(s *shelley) {
		s.viewport(1280, 900)
		s.tokenCostSubagentUsage(tokenCostUsage)
		s.tokenCostSpend(true, true)
	})
}

func TestE2ETokenCostSubagentsTallBreakdownAboveToggle(t *testing.T) {
	e2e(t, tokenCostSpendSetup+`The spend popup keeps a tall breakdown above its toggle on a short (1280x720) desktop window.
1. Open the spend popup.
2. The popup's [data-testid="token-cost-total"] text contains "$11.98".
3. Wait (with an eval that has expect "true") until the popup's bottom edge (getBoundingClientRect().y + height) is at or above the top (y) of the visible ".context-usage-label": popup.y + popup.height - label.y <= 0.
4. Click the visible ".context-usage-label" again. The popup becomes hidden.`, func(s *shelley) {
		s.viewport(1280, 720)
		s.tokenCostSubagentUsage(tokenCostUsage)
		s.tokenCostSpend(true, false)
	})
}

// tokenCostShared checks that main and sub-agent usage of the same model
// line up in one table at width.
func tokenCostShared(t *testing.T, width int) {
	w := strconv.Itoa(width)
	e2e(t, tokenCostSpendSetup+`The spend popup aligns a model's main and sub-agent usage in one table at a `+w+`px-wide window.
1. Open the spend popup. The table is visible.
2. The table's column headers (role "columnheader" inside the table) have exactly these texts, in order: "Model / tokens", "Main conversation", "Sub-agents".
3. "The model" is the table's ".token-cost-model-breakdown" element containing a ".token-cost-model-name" element whose text contains "main-model": there is exactly 1 such element.
4. "The input row" is the element with role "row" inside the model that contains an element with role "rowheader" whose accessible name is exactly "Input". Its cells are its elements with role "cell" (the row header is not one). The first cell's text contains "1.0M" and "$2.00"; the second cell's text contains "2.0M" and "$4.00".
5. In one eval with expect "true", using getBoundingClientRect(): the first and second cells have exactly the same y, the second's x is >= the first's x + width, and the table's scrollWidth <= its clientWidth.
6. The popup has no element with role "region" and accessible name exactly "Sub-agents" (count 0).`, func(s *shelley) {
		s.viewport(width, 900)
		s.tokenCostSubagentUsage(tokenCostWith(tokenCostUsage,
			"per_model", []any{tokenCostWith(tokenCostReviewer, "model", "main-model"), tokenCostUnpriced}))
		s.tokenCostSpend(true, false)
	})
}

func TestE2ETokenCostSubagentsSharedModel768(t *testing.T) { tokenCostShared(t, 768) }

func TestE2ETokenCostSubagentsSharedModel1280(t *testing.T) { tokenCostShared(t, 1280) }
