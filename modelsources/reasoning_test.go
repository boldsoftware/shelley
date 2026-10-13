package modelsources

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/models"
)

// Exercise discovery JSON, both construction paths, and captured requests without
// requiring a recognized hostname. Defaults and selections must reach the wire.
func TestIntegrationReasoningMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, native, provider        string
		api                           models.APIType
		levels                        []string
		defaultEffort, explicitEffort string
	}{
		{"Muse 1.2 responses", "meta/muse-spark-1.2-contributor", "opencode", models.APITypeOpenAIResponses, []string{"low", "medium", "high", "xhigh"}, "medium", "xhigh"},
		{"Muse 1.3 chat", "meta/muse-spark-1.3-contributor", "opencode", models.APITypeOpenAIChat, []string{"low", "medium", "high", "xhigh"}, "", "xhigh"},
		{"dynamic anthropic with off", "unknown-model", "custom", models.APITypeAnthropicMessages, []string{"off", "low", "medium", "high", "xhigh"}, "medium", "xhigh"},
		// Catalog defaults of medium tie between low/high: choose low, not high.
		{"catalog responses", "gpt-5.5", "openai", models.APITypeOpenAIResponses, []string{"low", "high"}, "low", "high"},
		{"catalog anthropic", "claude-opus-4-8", "anthropic", models.APITypeAnthropicMessages, []string{"low", "high"}, "low", "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const base = "https://arbitrary-endpoint.example.test"
			catalogJSON, err := json.Marshal(map[string]any{
				"schema_version": 1,
				"models": []any{map[string]any{
					"id": tc.provider + "/" + tc.native, "native_id": tc.native, "provider": tc.provider,
					"apis":     []string{"openai_responses", "openai_chat", "anthropic_messages", "gemini"},
					"upstream": map[string]any{"api_type": tc.api, "supports_reasoning": true, "reasoning_levels": tc.levels},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			var captured map[string]any
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "arbitrary-endpoint.example.test" {
					t.Fatalf("unexpected hostname: %s", req.URL)
				}
				body := string(catalogJSON)
				if req.Method == http.MethodPost {
					wantPath := map[models.APIType]string{models.APITypeOpenAIResponses: "/v1/responses", models.APITypeOpenAIChat: "/v1/chat/completions", models.APITypeAnthropicMessages: "/v1/messages"}[tc.api]
					if req.URL.Path != wantPath {
						t.Fatalf("wire path = %s, want %s", req.URL.Path, wantPath)
					}
					if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
						t.Fatal(err)
					}
					body = integrationResponse(t, tc.api)
				} else if req.Method != http.MethodGet || req.URL.Path != "/models.json" {
					t.Fatalf("unexpected discovery request: %s %s", req.Method, req.URL)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})}
			var catalog llmIntegrationModelCatalog
			if !fetchJSON(t.Context(), client, base+"/models.json", &catalog) {
				t.Fatal("discovery failed")
			}
			integration := &LLMIntegrationConfig{Name: "custom", Host: "arbitrary-endpoint.example.test", URL: base, Models: integrationModelsFromCatalog(catalog)}
			built := Build(models.All(), []Source{LLMIntegration(integration, "")}, client, nil)
			if len(built) != 1 {
				t.Fatalf("built = %+v", built)
			}
			svc := built[0].Service
			var levels []string
			for _, level := range llm.SupportedReasoningLevels(svc) {
				levels = append(levels, level.Name())
			}
			if !llm.SupportsReasoning(svc) || !reflect.DeepEqual(levels, tc.levels) {
				t.Fatalf("supported = %v, levels = %v; want true, %v", llm.SupportsReasoning(svc), levels, tc.levels)
			}
			if got := llm.ServiceDefaultReasoningLevel(svc); got != tc.defaultEffort {
				t.Fatalf("default = %q, want %q", got, tc.defaultEffort)
			}
			requestLevels := []llm.ThinkingLevel{llm.ThinkingLevelDefault, llm.ThinkingLevelXHigh}
			if slices.Contains(tc.levels, "off") {
				requestLevels = append(requestLevels, llm.ThinkingLevelOff)
			}
			for _, level := range requestLevels {
				captured = nil
				_, err := svc.Do(t.Context(), &llm.Request{ThinkingLevel: level, Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}}}}})
				if err != nil {
					t.Fatal(err)
				}
				if captured["model"] != tc.native {
					t.Fatalf("wire model = %v, want %s", captured["model"], tc.native)
				}
				want := tc.explicitEffort
				if level == llm.ThinkingLevelDefault {
					want = tc.defaultEffort
				}
				var got string
				switch tc.api {
				case models.APITypeOpenAIChat:
					got, _ = captured["reasoning_effort"].(string)
				case models.APITypeOpenAIResponses:
					if r, ok := captured["reasoning"].(map[string]any); ok {
						got, _ = r["effort"].(string)
					}
				case models.APITypeAnthropicMessages:
					if r, ok := captured["output_config"].(map[string]any); ok {
						got, _ = r["effort"].(string)
					}
					if level == llm.ThinkingLevelOff {
						want = ""
						if captured["thinking"] != nil {
							t.Fatalf("off emitted thinking: %v", captured)
						}
					} else if thinking, ok := captured["thinking"].(map[string]any); !ok || thinking["type"] != "adaptive" {
						t.Fatalf("missing adaptive thinking: %v", captured)
					}
				}
				if got != want {
					t.Errorf("level %s: wire effort = %q, want %q; request %v", level.Name(), got, want, captured)
				}
			}
		})
	}
}

// Missing effort lists must not replace catalog factories or turn their
// models.dev capabilities into endpoint overrides (including support-only JSON).
func TestIntegrationUnspecifiedControlsUseCatalogBuild(t *testing.T) {
	const base = "https://arbitrary.example"
	client := &http.Client{}
	catalog := append(models.All(), models.Model{
		ID: "configured-chat", APIModelName: "unknown-configured-chat", Provider: models.ProviderOpenAI, APIType: models.APITypeOpenAIChat,
		Build: func(base, key string, httpc *http.Client) llm.Service {
			return &oai.Service{Model: oai.Model{ModelName: "unknown-configured-chat"}, ModelURL: base + "/v1", APIKey: key, HTTPC: httpc, ReasoningEffort: "none", ThinkingLevel: llm.ThinkingLevelXHigh, MaxTokens: 1234}
		},
	}, models.Model{
		ID: "configured-responses", APIModelName: "unknown-configured-responses", Provider: models.ProviderOpenAI, APIType: models.APITypeOpenAIResponses,
		Build: func(base, key string, httpc *http.Client) llm.Service {
			return &oai.ResponsesService{Model: oai.Model{ModelName: "unknown-configured-responses"}, ModelURL: base + "/v1", APIKey: key, HTTPC: httpc, ReasoningEffort: "custom-effort", ThinkingLevel: llm.ThinkingLevelHigh, MaxTokens: 4321}
		},
	})
	for _, entry := range catalog {
		if entry.APIType != models.APITypeAnthropicMessages && entry.APIType != models.APITypeOpenAIResponses && entry.APIType != models.APITypeOpenAIChat {
			continue
		}
		baseline := entry.Build(base, "implicit", client)
		for _, upstream := range []string{"null", `{}`, `{"supports_reasoning":true}`, `{"supports_reasoning":true,"reasoning_levels":null}`, `{"api_type":"` + string(entry.APIType) + `","supports_reasoning":true}`} {
			t.Run(entry.ID+upstream, func(t *testing.T) {
				model := IntegrationModel{ID: entry.ID, NativeID: entry.APIModelName, Provider: string(entry.Provider), APIs: []string{"openai_responses", "openai_chat", "anthropic_messages"}}
				if err := json.Unmarshal([]byte(upstream), &model.Upstream); err != nil {
					t.Fatal(err)
				}
				api, svc, ok := buildIntegrationService(catalog, model, base, client)
				if !ok || api != entry.APIType || !reflect.DeepEqual(svc, baseline) {
					t.Fatalf("catalog Build changed: %s, %T %+v; want %s, %T %+v", api, svc, svc, entry.APIType, baseline, baseline)
				}
			})
		}
	}
}

// Explicitly disabled or unrecognized controls must not silently regain the
// generic efforts from the built-in catalog.
func TestUnusableControlsDoNotSendCatalogEfforts(t *testing.T) {
	for _, upstream := range []string{
		`{"reasoning_levels":[]}`,
		`{"supports_reasoning":false,"reasoning_levels":["low","high"]}`,
		`{"reasoning_levels":["none","thinking"]}`,
		`{"reasoning_levels":["high","ultra"]}`,
	} {
		t.Run(upstream, func(t *testing.T) {
			var captured map[string]any
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(integrationResponse(t, models.APITypeOpenAIResponses))), Request: req}, nil
			})}
			model := IntegrationModel{ID: "gpt-5.5", NativeID: "gpt-5.5", Provider: "openai", APIs: []string{"openai_responses"}}
			if err := json.Unmarshal([]byte(upstream), &model.Upstream); err != nil {
				t.Fatal(err)
			}
			_, svc, ok := buildIntegrationService(models.All(), model, "https://arbitrary.example", client)
			if !ok {
				t.Fatal("model not built")
			}
			if llm.SupportsReasoning(svc) || len(llm.SupportedReasoningLevels(svc)) != 0 || llm.ServiceDefaultReasoningLevel(svc) != "" {
				t.Fatal("unusable controls exposed catalog thinking levels")
			}
			if _, err := svc.Do(t.Context(), &llm.Request{ThinkingLevel: llm.ThinkingLevelHigh, Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}}}}}); err != nil {
				t.Fatal(err)
			}
			if captured["reasoning"] != nil {
				t.Fatalf("disabled controls sent reasoning: %v", captured)
			}
		})
	}
}

func integrationResponse(t *testing.T, api models.APIType) string {
	t.Helper()
	switch api {
	case models.APITypeOpenAIChat:
		return `{"id":"test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	case models.APITypeOpenAIResponses:
		return `{"id":"test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`
	case models.APITypeAnthropicMessages:
		return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"test\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	default:
		t.Fatalf("no response fixture for %s", api)
		return ""
	}
}
