package test

import (
	"net/http"
	"strconv"
	"testing"
)

// Ported from ui/e2e/token-cost-mode.spec.ts.

// tokenCostCatalog serves models as the page's model catalog (/api/models)
// and costs as its pricing (/api/model-costs), and sets {{MODELS}} to the
// catalog's JSON, for the description to compare
// window.__SHELLEY_INIT__.models with.
func (s *shelley) tokenCostCatalog(models []map[string]any, costs map[string]any) {
	s.route("GET /api/models", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, models) })
	// The page POSTs the models it needs priced.
	s.route("/api/model-costs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"costs": costs})
	})
	s.v("MODELS", mustJSON(models))
}

// tokenCostOpenGraph is how the cost graph tests reach the popup: /new loads
// /api/models; client-side navigation keeps that catalog in the status
// readout, exercising its propagation all the way to the cost graph.
const tokenCostOpenGraph = `1. Navigate to /new.
2. Wait (with an eval that has expect) until JSON.stringify(window.__SHELLEY_INIT__?.models) is exactly {{MODELS}}: the page loaded the mocked model catalog.
3. Navigate client-side, in one eval: history.pushState({}, "", "/c/{{SLUG}}"); window.dispatchEvent(new PopStateEvent("popstate")). Do NOT use a navigate step here: the page must not reload.
4. Click the VISIBLE ".context-usage-label" element (a hidden one may exist too; pick the visible one). "The popup" is the ".chat-context-popup" element.
5. The popup's ".token-cost-graph-svg" element is visible.
`

// Usage carries native model names and endpoints, not the picker's suffixed
// IDs. Mixed integrations can contribute to the same model's cost breakdown.
func tokenCostMode(t *testing.T, width int) {
	w := strconv.Itoa(width)
	e2e(t, `The cost graph hides only zero cache writes for ChatGPT subscription models, at a `+w+`px-wide window. The model catalog and pricing are mocked, and the conversation at /c/{{SLUG}} serves usage on each of its agent messages from a mix of integrations. Perform the steps in order.
`+tokenCostOpenGraph+`6. The popup's [data-testid="token-cost-total"] text contains "≈$0.011".
7. Wait (with an eval that has expect) until the breakdown is exactly as below. Compute it in one eval from the popup's element with role "table" and accessible name "Spend by model" (the legend): walk the legend's elements matching ".token-cost-model-row, .token-cost-legend-row" in document order; a ".token-cost-model-row" starts a model named by the trimmed textContent of its ".token-cost-model-name"; each ".token-cost-legend-row" adds [label, tokens] to the current model, from the trimmed textContent of its ".token-cost-legend-label" and ".token-cost-legend-tokens". Return JSON.stringify of the object mapping model names to their lists, with the models in this order (pick them by name; any other models in the legend are ignored):
{"gpt-5.6-luna":[["Output","100"],["Input","1k"],["Cache read","2k"]],"gpt-6-astra":[["Output","100"],["Input","1k"],["Cache write","42"],["Cache read","2k"]],"gpt-5.6-sol":[["Output","200"],["Input","2k"],["Cache write","0"],["Cache read","4k"]],"gpt-5.6-terra":[["Output","100"],["Input","1k"],["Cache write","0"],["Cache read","2k"]],"unknown-mode":[["Output","100"],["Input","1k"],["Cache write","0"],["Cache read","2k"]],"removed-model":[["Output","100"],["Input","1k"],["Cache write","0"],["Cache read","2k"]],"gpt-5.5":[["Output","100"],["Input","1k"],["Cache write","0"],["Cache read","2k"]]}
The ChatGPT subscription model with no cache writes (gpt-5.6-luna) is the only one without a "Cache write" row.`, func(s *shelley) {
		s.viewport(width, 900)
		type usageCase struct {
			model, mode, base string
			writes            int
			url               string
		}
		cases := []usageCase{
			{model: "gpt-5.6-luna", mode: "chatgpt", base: "https://subscription.test"},
			{model: "gpt-6-astra", mode: "chatgpt", base: "https://subscription.test", writes: 42},
			{model: "gpt-5.6-sol", mode: "chatgpt", base: "https://subscription.test"},
			{model: "gpt-5.6-sol", mode: "managed", base: "https://managed.test"},
			{model: "gpt-5.6-terra", mode: "byok", base: "https://byok.test"},
			{model: "unknown-mode", base: "https://unknown.test"},
			{model: "removed-model", mode: "chatgpt", base: "https://removed.test"},
			{model: "gpt-5.5", mode: "chatgpt", base: "https://subscription.test", url: "https://subscription.test.other/v1/responses"},
		}
		models := []map[string]any{{"id": "predictable", "ready": true}}
		costs := map[string]any{}
		for i, c := range cases {
			costs[c.model] = map[string]any{"input": 1, "output": 2, "cache_read": 0.1, "cache_write": 1.25}
			if c.model == "removed-model" {
				continue
			}
			m := map[string]any{"id": c.model + "@integration-" + strconv.Itoa(i), "api_model_name": c.model, "base_url": c.base, "ready": true}
			if c.mode != "" {
				m["mode"] = c.mode
			}
			models = append(models, m)
		}
		s.tokenCostCatalog(models, costs)
		conv := s.loremipsum("4")
		body := s.conversationBody(conv.ID)
		var agents []map[string]any
		for _, m := range messagesOf(body) {
			if m["type"] == "agent" {
				agents = append(agents, m)
			}
		}
		if len(agents) != len(cases) {
			s.fail("loremipsum 4 made %d agent messages, want %d", len(agents), len(cases))
		}
		for i, m := range agents {
			c := cases[i]
			url := c.url
			if url == "" {
				url = c.base + "/v1/responses"
			}
			m["usage_data"] = mustJSON(map[string]any{
				"model": c.model, "url": url,
				"input_tokens": 1000, "cache_read_input_tokens": 2000, "cache_creation_input_tokens": c.writes,
				"output_tokens": 100, "cost_usd": 0,
			})
		}
		s.serveConversation(conv.ID, body)
		s.v("SLUG", body["conversation"].(map[string]any)["slug"].(string))
	})
}

func TestE2ETokenCostModeZeroCacheWrites768(t *testing.T) { tokenCostMode(t, 768) }

func TestE2ETokenCostModeZeroCacheWrites1280(t *testing.T) { tokenCostMode(t, 1280) }
