package test

import (
	"maps"
	"net/http"
	"sync"
	"testing"
)

// Ported from ui/e2e/custom-model-output-limits.spec.ts.

// customModelOutputLimitsModel is the custom model the fake server holds,
// with overrides.
func customModelOutputLimitsModel(overrides map[string]any) map[string]any {
	m := map[string]any{
		"model_id":           "custom-1",
		"display_name":       "Existing custom model",
		"provider_type":      "openai",
		"endpoint":           "https://api.openai.com/v1",
		"has_api_key":        true,
		"model_name":         "custom-model",
		"max_tokens":         200000,
		"reasoning_effort":   "",
		"reasoning_support":  "auto",
		"reasoning_map":      "",
		"supports_reasoning": false,
		"image_support":      "auto",
		"supports_images":    true,
	}
	maps.Copy(m, overrides)
	return m
}

func TestE2ECustomModelOutputLimitsPreservesTestsSavesBlanks(t *testing.T) {
	e2e(t, useful(0), `A custom model's max output tokens are preserved, tested, saved, and blanked. The screen is the default 393x727 phone. `+menusOpenModelPickerHow+`The server is faked: there are no built-in models, and one custom model "Existing custom model" (id custom-1, max_tokens 200000, with a saved API key); saving it stores what the page sent (except the API key), and testing it answers success. Calls report the JSON value of a field of the last request, or "absent" when it lacks the field, or "none" before any: "update max_tokens" and "update api_key" for the last save of the model, "test max_tokens", "test api_key" and "test model_id" for the last test. Name elements by ARIA role and accessible name, as Playwright's getByRole does. "The form" is the topmost dialog, the one editing the model: the CSS selector '[role="dialog"]:has(.modal.modal-wide)' (the models list beneath it is ".modal-xwide"). "The form's max tokens input" is the input inside the form's ".form-group" containing the text "Max output tokens" ("#custom-model-max-output-tokens"); "the form's key input" is the form's input[type="password"]; "the form's display name input" is the input inside the form's ".form-group" containing "Display Name". "Edit Model" is the models list's button named "Edit Model". Perform the steps in order. 1. Navigate to /new and open the model picker. 2. Click the button whose name contains "Manage Models", then the button whose name contains "Edit Model". 3. The form's key input should have the value "" (empty) and the placeholder attribute exactly "Leave blank to keep the saved key". The form's max tokens input should have the value exactly "200000". 4. Fill the form's display name input with "Renamed custom model", then click the form's button whose name contains "Save". 5. Call "update max_tokens" until it returns "200000"; call "update api_key": it must return "\"\"" (the empty string, JSON-encoded). 6. Click the button whose name contains "Edit Model" again. The form's max tokens input should have the value "200000"; fill it with "123456". 7. Click the form's button whose name contains "Test". Call "test max_tokens" until it returns "123456"; "test api_key" must return "\"\"" and "test model_id" must return "\"custom-1\"". 8. Click the form's button whose name contains "Save". Call "update max_tokens" until it returns "123456". 9. Blank saves as 0 (the provider default) and reopens blank, the placeholder naming what blank resolves to: click "Edit Model" again. The form's max tokens input should have the value "123456"; fill it with "" (empty). It should then have the placeholder exactly "Model's published limit". Click the form's "Save" button; call "update max_tokens" until it returns "0". 10. Click "Edit Model" again. The form's max tokens input should have the value "" and the placeholder exactly "Model's published limit". 11. The form's key input should have the value ""; fill it with "replacement-key" and click the form's "Save" button. Call "update api_key" until it returns "\"replacement-key\"". 12. Click "Edit Model" again: the form's key input should have the value "".`, func(s *shelley) {
		var mu sync.Mutex
		current := customModelOutputLimitsModel(nil)
		var update, test map[string]any
		s.route("/api/models", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []any{})
		})
		s.route("/api/custom-models", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				s.pass.ServeHTTP(w, r)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			writeJSON(w, []any{current})
		})
		s.route("/api/custom-models/custom-1", func(w http.ResponseWriter, r *http.Request) {
			body := object(readBody(r))
			mu.Lock()
			defer mu.Unlock()
			update = body
			fields := maps.Clone(body)
			delete(fields, "api_key")
			current = customModelOutputLimitsModel(fields)
			writeJSON(w, current)
		})
		s.route("/api/custom-models-test", func(w http.ResponseWriter, r *http.Request) {
			body := object(readBody(r))
			mu.Lock()
			test = body
			mu.Unlock()
			writeJSON(w, map[string]any{"success": true, "message": "ok"})
		})
		field := func(m *map[string]any, key string) func() string {
			return func() string {
				mu.Lock()
				defer mu.Unlock()
				if *m == nil {
					return "none"
				}
				v, ok := (*m)[key]
				if !ok {
					return "absent"
				}
				return mustJSON(v)
			}
		}
		s.call("update max_tokens", field(&update, "max_tokens"))
		s.call("update api_key", field(&update, "api_key"))
		s.call("test max_tokens", field(&test, "max_tokens"))
		s.call("test api_key", field(&test, "api_key"))
		s.call("test model_id", field(&test, "model_id"))
	})
}
