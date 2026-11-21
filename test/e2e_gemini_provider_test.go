package test

import (
	"net/http"
	"sync"
	"testing"
)

// Ported from ui/e2e/gemini-provider.spec.ts.

func TestE2EGeminiProviderDiscoversAndConnects(t *testing.T) {
	e2e(t, `The Gemini model provider discovers models and connects using its native endpoint. The screen is the default 393x727 phone. `+menusOpenModelPickerHow+`The server is faked: there are no custom models and no providers yet; checking a provider answers one model, "gemini-3.8-flash" (named "Gemini Flash"); connecting saves the provider "Google". Call "check input" returns the JSON body of the last provider check the page sent ("" if none), call "connect input" that of the last connect request, and call "connected" says whether a connect request has arrived ("true" or "false"). Name elements by ARIA role and accessible name, as Playwright's getByRole does; "named X" means an exact name match. "The form" is the element with data-testid "model-provider-form". Perform the steps in order. 1. Navigate to /new and open the model picker. 2. Click the button named "Manage Models", then the button named "+ Add Models". 3. Click the form's button named "Google Gemini". 4. The form's data-testid "provider-endpoint" input should have the value exactly "https://generativelanguage.googleapis.com/v1beta". 5. Fill the form's data-testid "provider-api-key" input with "gemini-secret" and its data-testid "provider-display-name" input with "Google". 6. Click the form's button named "Check models". 7. The form's element with role "status" should contain the text "1 chat models: gemini-3.8-flash". 8. Call "check input": it must return exactly {"api_key":"gemini-secret","display_name":"Google","endpoint":"https://generativelanguage.googleapis.com/v1beta","provider_type":"gemini"}. 9. Call "connected": it must return "false". 10. Click the form's button named "Connect". 11. The form should be hidden. 12. Call "connect input": it must return exactly the same JSON as step 8. 13. The element with data-testid "provider-connection" should contain the text "Google", and the element with data-testid "model-provider-list" the text "Gemini".`, func(s *shelley) {
		saved := map[string]any{
			"provider_id":   "google-account",
			"provider_type": "gemini",
			"endpoint":      "https://generativelanguage.googleapis.com/v1beta",
			"display_name":  "Google",
			"has_api_key":   true,
			"model_count":   1,
		}
		var mu sync.Mutex
		var connected bool
		var checkInput, connectInput string
		s.route("/api/custom-models", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []any{})
		})
		s.route("/api/models", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if !connected {
				writeJSON(w, []any{})
				return
			}
			writeJSON(w, []any{map[string]any{
				"id":             "provider:Z29vZ2xlLWFjY291bnQ:Z2VtaW5pLTMuOC1mbGFzaA",
				"ready":          true,
				"source_id":      saved["provider_id"],
				"source":         saved["display_name"],
				"api_model_name": "gemini-3.8-flash",
				"api_type":       "gemini",
				"base_url":       saved["endpoint"],
			}})
		})
		s.route("/api/model-providers/check", func(w http.ResponseWriter, r *http.Request) {
			body := mustJSON(object(readBody(r)))
			mu.Lock()
			checkInput = body
			mu.Unlock()
			writeJSON(w, []any{map[string]any{"id": "gemini-3.8-flash", "name": "Gemini Flash"}})
		})
		s.route("/api/model-providers", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if r.Method == "POST" {
				connectInput = mustJSON(object(readBody(r)))
				connected = true
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(mustJSON(saved)))
				return
			}
			if connected {
				writeJSON(w, []any{saved})
			} else {
				writeJSON(w, []any{})
			}
		})
		get := func(v *string) func() string {
			return func() string {
				mu.Lock()
				defer mu.Unlock()
				return *v
			}
		}
		s.call("check input", get(&checkInput))
		s.call("connect input", get(&connectInput))
		s.call("connected", func() string {
			mu.Lock()
			defer mu.Unlock()
			if connected {
				return "true"
			}
			return "false"
		})
	})
}
