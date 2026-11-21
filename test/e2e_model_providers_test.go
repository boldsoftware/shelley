package test

import (
	"net/http"
	"slices"
	"sync"
	"testing"
)

// Ported from ui/e2e/model-providers.spec.ts.

// modelProvidersNoModels makes the page's init data list no models, with
// extra fields.
func modelProvidersNoModels(s *shelley, extra string) {
	s.initScript(`(() => {
  let init;
  Object.defineProperty(window, "__SHELLEY_INIT__", {
    configurable: true,
    get: () => init,
    set: (value) => {
      init = { ...value, models: [], default_model: ""` + extra + ` };
    },
  });
})();`)
}

// modelProvidersSubset is the JSON of the given fields of a request body,
// or "none" before one.
func modelProvidersSubset(body map[string]any, keys ...string) string {
	if body == nil {
		return "none"
	}
	got := map[string]any{}
	for _, k := range keys {
		if v, ok := body[k]; ok {
			got[k] = v
		}
	}
	return mustJSON(got)
}

func TestE2EModelProvidersEmptyWarningLinksToAddForm(t *testing.T) {
	e2e(t, `The empty-model warning links directly to the add form. The page starts with no models at all and a hint to add one. The screen is the default phone. Name elements by ARIA role and accessible name, as Playwright's getByRole does. "The hint" is ".no-models-message"; "the action" is the hint's button named exactly "Click here to add models"; "the title" is the hint's ".no-models-title". Perform the steps in order. 1. Navigate to /new. 2. The hint must contain neither "Ctrl" nor "⌘". 3. The action's computed text-decoration-line should be "underline" and its computed overflow "visible". 4. Set the viewport to 1280x800 (width and height only). Using the bounding boxes of the title and the action: the action's x must be greater than the title's x + width (the link follows the title on its line), and |action y - title y| must be less than 10. 5. Set the viewport to 412x800 (width and height only). The title's computed text-align should be "center"; the action's y must be at least the title's y + height (the link goes below it), and the horizontal centers of the action and the title (x + width / 2) must differ by less than 1. 6. Click the action. The element with data-testid "model-provider-form" should be visible, and data-testid "model-mode-provider" should have aria-pressed="true".`, func(s *shelley) {
		modelProvidersNoModels(s, `, model_setup_hint: "add_model"`)
		s.route("/api/models", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []any{})
		})
	})
}

func TestE2EModelProvidersRefreshSendsPreservedDraft(t *testing.T) {
	e2e(t, `Refreshing the model catalog lets a conversation started with no model selected send its preserved draft. The page starts with no models; refreshing the catalog finds the model "predictable". The screen is the default 393x727 phone. `+menusOpenModelPickerHow+`Call "chat" returns the message and model fields of the chat request the page sent, as JSON ("none" before one). Name elements by ARIA role and accessible name, as Playwright's getByRole does; "named X" means an exact name match. "The input" is data-testid "message-input". Perform the steps in order. 1. Navigate to /new. ".no-models-message" should be visible. 2. Fill the input with "hello": the URL should come to contain "/c/". 3. Open the model picker. Click the panel's button named "Refresh"; the panel's ".model-picker-option-check" should be visible. Click the panel's button named "Close". 4. Click the button named "More options". 5. The input should have the value exactly "hello". 6. Call "chat": it must return "none". Click data-testid "send-button", then call "chat" until it returns {"message":"hello","model":"predictable"}.`, func(s *shelley) {
		modelProvidersNoModels(s, "")
		var mu sync.Mutex
		models := []any{}
		var chat map[string]any
		s.route("/api/models", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			writeJSON(w, models)
		})
		s.route("/api/models/refresh", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			models = []any{map[string]any{"id": "predictable", "ready": true}}
			writeJSON(w, models)
		})
		s.route("POST /api/conversation/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
			body := object(readBody(r))
			mu.Lock()
			if chat == nil {
				chat = body
			}
			mu.Unlock()
			s.pass.ServeHTTP(w, r)
		})
		s.call("chat", func() string {
			mu.Lock()
			defer mu.Unlock()
			return modelProvidersSubset(chat, "message", "model")
		})
	})
}

func TestE2EModelProvidersAddDialogPresetsAndFailures(t *testing.T) {
	e2e(t, `The shared add-models dialog, its endpoint presets, and recovery from provider failures. The screen is the default 393x727 phone. `+menusOpenModelPickerHow+`The server is faked: it has two provider accounts, "custom" (id account-1, https://api.example.test/v1, one model) and "Other" (id account-2, no models); its one model is "Work model" from account-1, and that model is selected (localStorage "shelley_selected_model" is "{{ROUTE}}"). Checking a provider finds the chat model gpt-6.1-sol. Adding a provider and refreshing account-1 fail with 502 "discovery failed". Editing account-1 renames it "Work edited" with 2 models; removing it removes it and its model. Adding a single model succeeds. Call "single input" returns the provider_type, endpoint and model_name fields of the last single model the page added, as JSON ("none" before one); call "provider input" returns the provider_type and endpoint fields of the last provider the page tried to add ("none" before one). Name elements by ARIA role and accessible name, as Playwright's getByRole does. Elements named by a test id are those with that data-testid. "The first connection" is the first "provider-connection". Perform the steps in order. 1. Navigate to /new and open the model picker; click the panel's button whose name contains "Manage Models". 2. The first connection should contain the text "custom", and ".models-cell-name" (exactly one) should have exactly the text "Work model". 3. Click the button named exactly "+ Add Models". "model-mode-provider" should have exactly the text "Connect a provider" and "model-mode-single" exactly "Add a single model". "model-mode-provider" should have aria-pressed="true" and computed font-weight "700"; "model-mode-single" font-weight "500". "provider-endpoint" should have the value exactly "https://api.anthropic.com/v1". 4. Click "model-mode-single": it should have aria-pressed="true" and font-weight "700", and "model-mode-provider" font-weight "500". 5. "The single form" is the visible ".model-form" inside the last element with role "dialog". Its "model-endpoint" should have the value exactly "https://api.anthropic.com/v1/messages". Click the single form's button named exactly "OpenAI (Chat API)", then its button named exactly "Endpoint", then the menu item (role "menuitem") whose name starts with "OpenRouter". The single form's "model-endpoint" should have the value exactly "https://openrouter.ai/api/v1". 6. In the single form, fill "model-endpoint" with "https://single.example.test/v1", the input whose placeholder starts with "Model name" with "single-model", the input in the ".form-group" containing "Display Name" with "Single model", and the input in the ".form-group" containing "API Key" with "single-secret". Click the single form's button named exactly "Add Model". Call "single input" until it returns {"endpoint":"https://single.example.test/v1","model_name":"single-model","provider_type":"openai"}. 7. Click the button named exactly "+ Add Models": "model-mode-provider" should have aria-pressed="true". "The add form" is "model-provider-form". Its "provider-display-name" should have the placeholder exactly "Optional, e.g. Work". Click the add form's button named exactly "OpenAI (Chat API)", then its button named exactly "Endpoint", then the menu item whose name starts with "Groq". The add form's "provider-endpoint" should have the value exactly "https://api.groq.com/openai/v1". 8. In the add form, fill "provider-endpoint" with "https://custom.example.test/v1", "provider-display-name" with "Failed add" and "provider-api-key" with "secret". Click its button named exactly "Check models". The add form's element with role "status" should contain "1 chat models: gpt-6.1-sol". Call "provider input": it must return "none". 9. Fill "provider-api-key" with "changed": the add form's status element should be hidden. Fill it with "secret" again. 10. Click the add form's button named exactly "Connect". Its element with role "alert" should have exactly the text "Failed to add provider: discovery failed". Call "provider input": it must return {"endpoint":"https://custom.example.test/v1","provider_type":"openai"}. The first connection should still contain "custom". Click the add form's button whose name contains "Cancel". 11. Click "refresh-provider-account-1". ".models-error" should contain "discovery failed", and the first connection should still contain "custom". 12. Click "edit-provider-account-1". In "model-provider-form", fill "provider-display-name" with "Work edited" and click its button whose name contains "Save". The first connection should contain "Work edited" and "(2)". 13. Click "remove-provider-account-1", then "confirm-remove-provider". There should be exactly 1 "provider-connection". The text "This provider account was removed. Choose another model." should be visible. localStorage "shelley_selected_model" must still be exactly "{{ROUTE}}".`, func(s *shelley) {
		const accountID = "account-1"
		const routeID = "provider:YWNjb3VudC0x:bW9kZWwtMQ"
		s.v("ROUTE", routeID)
		s.initScript(`localStorage.setItem("shelley_selected_model", ` + mustJSON(routeID) + `)`)
		var mu sync.Mutex
		providers := []map[string]any{
			{"provider_id": accountID, "provider_type": "openai", "endpoint": "https://api.example.test/v1", "display_name": "custom", "has_api_key": true, "model_count": 1},
			{"provider_id": "account-2", "provider_type": "openai", "endpoint": "https://other.example.test/v1", "display_name": "Other", "has_api_key": true, "model_count": 0},
		}
		var removed bool
		var providerInput, singleInput map[string]any
		failed := func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("discovery failed\n"))
		}
		s.route("/api/model-providers/check", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []any{map[string]any{"id": "gpt-6.1-sol", "name": "Sol"}})
		})
		s.route("/api/model-providers", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			switch r.Method {
			case "GET":
				writeJSON(w, providers)
			case "POST":
				providerInput = object(readBody(r))
				failed(w)
			default:
				s.pass.ServeHTTP(w, r)
			}
		})
		s.route("/api/model-providers/"+accountID+"/refresh", func(w http.ResponseWriter, r *http.Request) {
			failed(w)
		})
		s.route("/api/model-providers/"+accountID, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			switch r.Method {
			case "PUT":
				edited := map[string]any{}
				for k, v := range providers[0] {
					edited[k] = v
				}
				edited["display_name"] = "Work edited"
				edited["model_count"] = 2
				providers = []map[string]any{edited, providers[1]}
				writeJSON(w, providers[0])
			case "DELETE":
				providers = slices.DeleteFunc(providers, func(p map[string]any) bool { return p["provider_id"] == accountID })
				removed = true
				w.WriteHeader(http.StatusNoContent)
			default:
				s.pass.ServeHTTP(w, r)
			}
		})
		s.route("/api/models", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if removed {
				writeJSON(w, []any{})
				return
			}
			writeJSON(w, []any{map[string]any{"id": routeID, "display_name": "Work model", "source": "custom", "source_id": accountID, "ready": true}})
		})
		s.route("/api/custom-models", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" {
				writeJSON(w, []any{})
				return
			}
			body := object(readBody(r))
			mu.Lock()
			singleInput = body
			mu.Unlock()
			answer := map[string]any{"model_id": "single-test"}
			for k, v := range body {
				answer[k] = v
			}
			writeJSON(w, answer)
		})
		s.call("single input", func() string {
			mu.Lock()
			defer mu.Unlock()
			return modelProvidersSubset(singleInput, "provider_type", "endpoint", "model_name")
		})
		s.call("provider input", func() string {
			mu.Lock()
			defer mu.Unlock()
			return modelProvidersSubset(providerInput, "provider_type", "endpoint")
		})
	})
}

func TestE2EModelProvidersConnectNotDismissedWhileSaving(t *testing.T) {
	e2e(t, `A provider's Connect cannot be dismissed while it is saving. The screen is the default 393x727 phone. `+menusOpenModelPickerHow+`The server is faked: there are no models and no providers. Connecting a provider waits until call "release", then saves "Pending account". Name elements by ARIA role and accessible name, as Playwright's getByRole does; "named X" means an exact name match. "The form" is the element with data-testid "model-provider-form". Perform the steps in order. 1. Navigate to /new and open the model picker. Click the button named "Manage Models", then the button named "+ Add Models". 2. Fill the form's data-testid "provider-api-key" input with "secret" and its data-testid "provider-display-name" input with "Pending account". 3. Click the form's button named "Connect". The form's button named "Saving…" should be disabled, and data-testid "model-mode-single" should be disabled. 4. Click the button whose name contains "Close modal" inside the last element with role "dialog": the form should still be visible. 5. Press Escape: the form should still be visible. 6. Call "release". 7. The form should be hidden, and data-testid "provider-connection" should contain the text "Pending account".`, func(s *shelley) {
		saved := map[string]any{
			"provider_id": "pending-account", "provider_type": "anthropic",
			"endpoint": "https://api.anthropic.com/v1", "display_name": "Pending account",
			"has_api_key": true, "model_count": 0,
		}
		var mu sync.Mutex
		var connected bool
		release := s.gate("release")
		s.route("/api/models", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []any{})
		})
		s.route("/api/custom-models", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []any{})
		})
		s.route("/api/model-providers", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				if !release.wait(r) {
					return
				}
				mu.Lock()
				connected = true
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(mustJSON(saved)))
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if connected {
				writeJSON(w, []any{saved})
			} else {
				writeJSON(w, []any{})
			}
		})
	})
}

func TestE2EModelProvidersCatalogFetchedOnce(t *testing.T) {
	e2e(t, `The model catalog is fetched once on connect, and not again on conversation switches. The screen is the default 393x727 phone. There is a draft conversation, /c/{{DRAFT}}, whose draft is "Provider catalog navigation fixture". Call "counts" returns how many times the page has asked for the models (/api/models) and for the providers (/api/model-providers), as JSON. "The input" is data-testid "message-input". Perform the steps in order. 1. Navigate to /new. Call "counts" until it returns {"models":1,"providers":1}. 2. Switch the app to /c/{{DRAFT}} the way its router would: eval window.history.pushState({}, "", "/c/{{DRAFT}}") and then window.dispatchEvent(new PopStateEvent("popstate")). The input should have the value exactly "Provider catalog navigation fixture". 3. Switch back to /new the same way: the input should have the value "" (empty). 4. Call "counts": it must return {"models":1,"providers":1}.`, func(s *shelley) {
		var draft struct {
			ID string `json:"conversation_id"`
		}
		s.api("POST", "/api/conversations/draft", map[string]any{"draft": "Provider catalog navigation fixture", "model": "predictable"}, &draft)
		s.v("DRAFT", draft.ID)
		var mu sync.Mutex
		counts := map[string]int{"models": 0, "providers": 0}
		s.route("/api/models", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			counts["models"]++
			mu.Unlock()
			writeJSON(w, []any{map[string]any{"id": "predictable", "ready": true}})
		})
		s.route("/api/model-providers", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			counts["providers"]++
			mu.Unlock()
			writeJSON(w, []any{})
		})
		s.call("counts", func() string {
			mu.Lock()
			defer mu.Unlock()
			return mustJSON(counts)
		})
	})
}
