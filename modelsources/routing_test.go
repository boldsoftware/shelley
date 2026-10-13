package modelsources

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
)

func TestIntegrationUpstreamAPIRouting(t *testing.T) {
	for _, tc := range []struct {
		name, native, provider, upstream string
		apis                             []string
		responseAPI                      models.APIType
		wantPath                         string
	}{
		{name: "upstream chat overrides catalog responses", native: "gpt-5.5", provider: "openai", upstream: "openai-chat-completions", responseAPI: models.APITypeOpenAIChat, wantPath: "/v1/chat/completions"},
		{name: "upstream responses overrides catalog messages", native: "claude-opus-4-8", provider: "anthropic", upstream: "openai-responses", responseAPI: models.APITypeOpenAIResponses, wantPath: "/v1/responses"},
		{name: "upstream chat overrides default responses", native: "unknown", upstream: "openai-chat-completions", responseAPI: models.APITypeOpenAIChat, wantPath: "/v1/chat/completions"},
		{name: "unspecified upstream keeps catalog messages", native: "claude-opus-4-8", provider: "anthropic", responseAPI: models.APITypeAnthropicMessages, wantPath: "/v1/messages"},
		{name: "invalid upstream keeps catalog messages", native: "claude-opus-4-8", provider: "anthropic", upstream: "invented", responseAPI: models.APITypeAnthropicMessages, wantPath: "/v1/messages"},
		{name: "unadvertised upstream keeps advertised chat", native: "unknown", upstream: "anthropic-messages", apis: []string{"openai_chat"}, responseAPI: models.APITypeOpenAIChat, wantPath: "/v1/chat/completions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apis := tc.apis
			if apis == nil {
				apis = []string{"openai_responses", "openai_chat", "anthropic_messages", "gemini"}
			}
			discovered := map[string]any{"id": tc.provider + "/" + tc.native, "native_id": tc.native, "provider": tc.provider, "apis": apis}
			if tc.upstream != "" {
				discovered["upstream"] = map[string]string{"api_type": tc.upstream}
			}
			body, err := json.Marshal(discovered)
			if err != nil {
				t.Fatal(err)
			}
			var m IntegrationModel
			if err := json.Unmarshal(body, &m); err != nil {
				t.Fatal(err)
			}
			var paths []string
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost || req.URL.Host != "arbitrary.example" {
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
				}
				paths = append(paths, req.URL.Path)
				if req.URL.Path != tc.wantPath {
					t.Fatalf("request path = %q, want %q", req.URL.Path, tc.wantPath)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(integrationResponse(t, tc.responseAPI))),
					Request:    req,
				}, nil
			})}
			_, svc, ok := buildIntegrationService(models.All(), m, "https://arbitrary.example", client)
			if !ok {
				t.Fatal("integration service was not built")
			}
			_, err = svc.Do(t.Context(), &llm.Request{Messages: []llm.Message{
				{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(paths) != 1 {
				t.Fatalf("request paths = %v, want one request to %s", paths, tc.wantPath)
			}
		})
	}
}
