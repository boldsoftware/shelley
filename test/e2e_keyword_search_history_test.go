package test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
)

// Ported from ui/e2e/keyword-search-history.spec.ts.

// keywordSearchHistory checks that a stored keyword_search call and its
// result still render, though keyword_search is no longer an executable
// tool: the route replays the conversation's bash call as one.
func keywordSearchHistory(t *testing.T, hasError bool, searchTerms any) {
	const query = "find authentication handlers"
	output, label, errors, outcome := "src/auth.ts:42: handleLogin", "Results:", "0", "successful"
	if hasError {
		output, label, errors, outcome = "Search failed: directory not found", "Results (Error):", "1", "failed"
	}
	terms := "an array of terms"
	if _, ok := searchTerms.(string); ok {
		terms = "a single string of terms"
	}
	e2e(t, `A historical keyword_search call renders its `+outcome+` result (the stored call has `+terms+`). "The card" is the completed tool card, .tool[data-testid="tool-call-completed"], whose ".tool-command" contains "`+query+`"; all checks below are inside the card unless said otherwise. Perform the steps in order. 1. Call "tool registry"; it must return "keyword_search is not registered". 2. Navigate to /c/historical-search. 3. The card should be visible. 4. Its ".tool-emoji" text should be exactly "🔍". 5. Its ".tool-header .tool-status-icon" count must be 0, and its ".tool-header" text must contain neither "✓" nor "✗". 6. Its ".tool-details" must be hidden (or absent). 7. Click the card's ".tool-header", then move the mouse away to the viewport's top-left corner (a hover step with no selector at x 0, y 0). 8. There must be no message action bar anywhere on the page: "[data-action-bar]" count 0. 9. The card's ".tool-code" elements must be exactly three, with whitespace-normalised texts, in order, exactly "`+query+`", "auth, handler" and "`+output+`". 10. The card's ".tool-time" text should be exactly "1.0s". 11. The card's last ".tool-label" should contain "`+label+`". 12. The card's ".tool-code.error" count should be exactly `+errors+`. 13. Click the card's collapse button, button[aria-label="Collapse"] inside the card. 14. The card's ".tool-details" should become hidden (or absent).`, func(s *shelley) {
		s.call("tool registry", func() string {
			var registry struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			s.api("GET", "/api/tools", nil, &registry)
			if slices.ContainsFunc(registry.Tools, func(tool struct {
				Name string `json:"name"`
			},
			) bool {
				return tool.Name == "keyword_search"
			}) {
				return "keyword_search is registered"
			}
			return "keyword_search is not registered"
		})
		c := s.conversation("bash: echo historical-search", "historical-search")
		// Replay a stored call/result pair without reintroducing an
		// executable tool.
		s.route("/api/stream2", abort)
		s.route("GET /api/conversation/"+c.ID, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := s.fetchJSON(r, &body); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			messages, _ := body["messages"].([]any)
			for _, m := range messages {
				message := m.(map[string]any)
				raw, _ := message["llm_data"].(string)
				if raw == "" {
					continue
				}
				var data map[string]any
				if err := json.Unmarshal([]byte(raw), &data); err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
				contents, _ := data["Content"].([]any)
				for _, c := range contents {
					content := c.(map[string]any)
					switch content["Type"] {
					case 5.0:
						content["ToolName"] = "keyword_search"
						content["ToolInput"] = map[string]any{"query": query, "search_terms": searchTerms}
					case 6.0:
						content["ToolResult"] = []any{map[string]any{"ID": "historical-search-result", "Type": 2, "Text": output}}
						content["ToolError"] = hasError
						content["ToolUseStartTime"] = "2026-01-01T00:00:00Z"
						content["ToolUseEndTime"] = "2026-01-01T00:00:01Z"
					}
				}
				message["llm_data"] = mustJSON(data)
			}
			writeJSON(w, body)
		})
	})
}

func TestE2EKeywordSearchHistorySuccessfulArrayTerms(t *testing.T) {
	keywordSearchHistory(t, false, []string{"auth", "handler"})
}

func TestE2EKeywordSearchHistoryFailedArrayTerms(t *testing.T) {
	keywordSearchHistory(t, true, []string{"auth", "handler"})
}

func TestE2EKeywordSearchHistorySuccessfulStringTerms(t *testing.T) {
	keywordSearchHistory(t, false, "auth, handler")
}
