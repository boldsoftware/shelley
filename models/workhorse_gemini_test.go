package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/gem"
	"shelley.exe.dev/llm/gem/gemini"
)

func TestNativeGeminiWorkhorseThinking(t *testing.T) {
	zero, minimal := 0, 1024
	low := &gemini.ThinkingConfig{ThinkingLevel: "low", IncludeThoughts: true}
	tests := []struct {
		name, model, support, mapping, wantErr string
		fallback                               bool
		want                                   *gemini.ThinkingConfig
	}{
		{name: "Flash primary", model: "gemini-3.8-flash", want: low},
		{name: "Flash alias", model: "gemini-flash-latest", want: low},
		{name: "Argon fallback", model: "gemini-4-argon", fallback: true, want: low},
		{name: "2.5 Flash can disable", model: "gemini-2.5-flash", want: &gemini.ThinkingConfig{ThinkingBudget: &zero}},
		{name: "2.5 Pro needs thinking", model: "gemini-2.5-pro", want: &gemini.ThinkingConfig{ThinkingBudget: &minimal, IncludeThoughts: true}},
		{name: "unknown controls use provider default", model: "gemini-custom"},
		{name: "invalid disabled config stays invalid", model: "gemini-4-argon", support: "no", wantErr: "cannot disable"},
		{name: "Argon UI identity map", model: "gemini-4-argon", mapping: identityReasoningMap, want: low},
		{name: "unknown UI identity map", model: "gemini-custom", mapping: identityReasoningMap},
		{name: "unknown explicit mapping", model: "gemini-custom", mapping: `{"high":"high"}`, want: &gemini.ThinkingConfig{ThinkingLevel: "high", IncludeThoughts: true}},
		{name: "explicit off remapped to low", model: "gemini-4-argon", mapping: `{"off":"low","high":"high"}`, want: low},
		{name: "disabled config with identity map", model: "gemini-4-argon", support: "no", mapping: identityReasoningMap, wantErr: "cannot disable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type capture struct {
				model    string
				thinking *gemini.ThinkingConfig
			}
			captured := make(chan capture, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					GenerationConfig struct {
						Thinking *gemini.ThinkingConfig `json:"thinkingConfig"`
					} `json:"generationConfig"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				model := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/models/"), ":streamGenerateContent")
				captured <- capture{model, request.GenerationConfig.Thinking}
				if tt.fallback && model == "gemini-3.8-flash" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"code":400,"message":"retired"}}`))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: " + `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ready"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}` + "\n\n"))
			}))
			defer server.Close()
			native := func(model, support string) llm.Service {
				service := &gem.Service{Model: model, APIKey: "key", URL: server.URL, HTTPC: server.Client()}
				return WrapReasoningConfig(service, server.URL, model, support, tt.mapping)
			}
			built := []Built{{ID: tt.model, Provider: ProviderGemini, Service: native(tt.model, tt.support)}}
			wantModels := []string{tt.model}
			if tt.fallback {
				built = append(built, Built{ID: "gemini-3.8-flash", Provider: ProviderGemini, Service: native("gemini-3.8-flash", "")})
				wantModels = append([]string{"gemini-3.8-flash"}, wantModels...)
			}
			manager := newWorkhorseManager(t, built...)
			service, err := manager.GetWorkhorseService(tt.model)
			if err != nil {
				t.Fatal(err)
			}
			request := &llm.Request{Messages: []llm.Message{llm.UserStringMessage("Make a short title")}, ThinkingLevel: llm.ThinkingLevelHigh, ReasoningEffort: "high"}
			response, err := service.Do(t.Context(), request)
			assertRequestUnchanged(t, request)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || len(captured) != 0 {
					t.Fatalf("error=%v, provider calls=%d; want %q before HTTP", err, len(captured), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if llm.FirstText(response) != "ready" || len(captured) != len(wantModels) {
				t.Fatalf("response=%+v, provider calls=%d, want %d", response, len(captured), len(wantModels))
			}
			for _, model := range wantModels {
				got := <-captured
				if got.model != model || !reflect.DeepEqual(got.thinking, tt.want) {
					t.Errorf("model=%s, thinking=%+v; want model=%s, thinking=%+v", got.model, got.thinking, model, tt.want)
				}
			}
		})
	}
}

type geminiVendorService struct{ recordingService }

func (*geminiVendorService) SupportsReasoning() bool { return true }
func (*geminiVendorService) SupportedReasoningLevels() []llm.ThinkingLevel {
	return []llm.ThinkingLevel{llm.ThinkingLevelLow, llm.ThinkingLevelMedium, llm.ThinkingLevelHigh}
}

func TestNonNativeGeminiWorkhorseKeepsThinkingOff(t *testing.T) {
	// A provider label can describe a Gemini model behind an OpenAI-compatible API.
	inner := &geminiVendorService{recordingService{provider: "gemini"}}
	manager := newWorkhorseManager(t, Built{ID: "proxy-conversation", Provider: ProviderGemini, Service: inner})
	service, err := manager.GetWorkhorseService("proxy-conversation")
	if err != nil {
		t.Fatal(err)
	}
	request := &llm.Request{ThinkingLevel: llm.ThinkingLevelHigh, ReasoningEffort: "high"}
	if _, err := service.Do(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	assertReasoningDisabled(t, inner.request)
	assertRequestUnchanged(t, request)
}
