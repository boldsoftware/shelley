package oai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/models/modelsdev"
)

func TestServiceDefaultReasoningLevel(t *testing.T) {
	tests := []struct {
		name string
		svc  llm.Service
		want string
	}{
		{"chat none stays verbatim", &Service{ReasoningEffort: "none"}, "none"},
		{"responses none stays verbatim", &ResponsesService{ReasoningEffort: "none"}, "none"},
		{"chat verbatim wins", &Service{ProviderName: "fireworks", ReasoningEffort: "high", ThinkingLevel: llm.ThinkingLevelMedium}, "high"},
		{"chat service default", &Service{ProviderName: "fireworks", ThinkingLevel: llm.ThinkingLevelMedium}, "medium"},
		// Unset: provider picks its own default, which Shelley can't name.
		{"chat unset -> unknown", &Service{ProviderName: "fireworks"}, ""},
		{"responses verbatim wins", &ResponsesService{ProviderName: "openai", ReasoningEffort: "xhigh", ThinkingLevel: llm.ThinkingLevelMedium}, "xhigh"},
		{"responses service default", &ResponsesService{ProviderName: "openai", ThinkingLevel: llm.ThinkingLevelMedium}, "medium"},
		{"responses unset -> unknown", &ResponsesService{ProviderName: "openai"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.ServiceDefaultReasoningLevel(tc.svc); got != tc.want {
				t.Fatalf("ServiceDefaultReasoningLevel() = %q, want %q", got, tc.want)
			}
		})
	}
}

// An enabled service default must not become an enabled choice when this
// model's endpoint only advertises off. The API uses off, not the wire's none.
func TestOffOnlyControlsDoNotInventDefaultEffort(t *testing.T) {
	caps := &modelsdev.ReasoningCapabilities{Supported: true, Levels: []llm.ThinkingLevel{llm.ThinkingLevelOff}}
	for _, svc := range []llm.Service{
		&Service{ReasoningOverride: caps, ThinkingLevel: llm.ThinkingLevelMedium},
		&ResponsesService{ReasoningOverride: caps, ThinkingLevel: llm.ThinkingLevelMedium},
	} {
		if got := llm.ServiceDefaultReasoningLevel(svc); got != "off" {
			t.Errorf("%T default = %q, want off", svc, got)
		}
	}
}

func TestDisableReasoningOverridesEndpointControls(t *testing.T) {
	for _, caps := range []*modelsdev.ReasoningCapabilities{
		nil,
		{Supported: true, Levels: []llm.ThinkingLevel{llm.ThinkingLevelHigh}},
	} {
		var captured map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Error(err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/v1/responses" {
				_, _ = w.Write([]byte(`{"id":"test","status":"completed","output":[]}`))
			} else {
				_, _ = w.Write([]byte(`{"id":"test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}
		}))
		t.Cleanup(server.Close)
		for _, svc := range []llm.Service{
			&Service{
				Model: modelForTest("test"), APIKey: "test", ModelURL: server.URL + "/v1",
				DisableReasoning: true, ReasoningOverride: caps,
				ThinkingLevel: llm.ThinkingLevelHigh, ReasoningEffort: "high",
			},
			&ResponsesService{
				Model: modelForTest("test"), APIKey: "test", ModelURL: server.URL + "/v1",
				DisableReasoning: true, ReasoningOverride: caps,
				ThinkingLevel: llm.ThinkingLevelHigh, ReasoningEffort: "high",
			},
		} {
			if llm.SupportsReasoning(svc) || len(llm.SupportedReasoningLevels(svc)) != 0 || llm.ServiceDefaultReasoningLevel(svc) != "" {
				t.Fatalf("%T exposed disabled reasoning controls with override %+v", svc, caps)
			}
			for _, effort := range []string{"", "high"} {
				captured = nil
				if _, err := svc.Do(t.Context(), &llm.Request{
					ThinkingLevel: llm.ThinkingLevelHigh, ReasoningEffort: effort,
					Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}}}},
				}); err != nil {
					t.Fatal(err)
				}
				if captured == nil || captured["reasoning"] != nil || captured["reasoning_effort"] != nil {
					t.Fatalf("%T disabled reasoning request = %v", svc, captured)
				}
			}
		}
	}
}
