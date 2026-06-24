package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModelsListURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		endpoint string
		want     string
	}{
		{"https://api.openai.com/v1", "https://api.openai.com/v1/models"},
		{"https://api.anthropic.com/v1/messages", "https://api.anthropic.com/v1/models"},
		{"https://generativelanguage.googleapis.com/v1beta", "https://generativelanguage.googleapis.com/v1beta/models"},
		{"https://llm.example.com/v1/", "https://llm.example.com/v1/models"},
		{"https://llm.example.com/v1/models", "https://llm.example.com/v1/models"},
		{"https://llm.example.com/v1/chat/completions", "https://llm.example.com/v1/models"},
	}
	for _, tc := range tests {
		got, err := modelsListURL(tc.endpoint)
		if err != nil {
			t.Fatalf("modelsListURL(%q) error: %v", tc.endpoint, err)
		}
		if got != tc.want {
			t.Errorf("modelsListURL(%q) = %q, want %q", tc.endpoint, got, tc.want)
		}
	}
}

func TestDiscoverOpenAIModels(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		json.NewEncoder(w).Encode(openAIModelsResponse{
			Data: []struct {
				ID string `json:"id"`
			}{
				{ID: "gpt-5.5"},
				{ID: "gpt-5.4"},
			},
		})
	}))
	defer server.Close()

	models, err := discoverOpenAIModels(t.Context(), server.Client(), server.URL+"/v1", "test-key")
	if err != nil {
		t.Fatalf("discoverOpenAIModels failed: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "gpt-5.5" || models[1].ID != "gpt-5.4" {
		t.Fatalf("unexpected models: %+v", models)
	}
}

func TestDiscoverAnthropicModels(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "anthropic-key" {
			t.Fatalf("unexpected api key header: %q", got)
		}
		json.NewEncoder(w).Encode(anthropicModelsResponse{
			Data: []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			}{
				{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6"},
			},
		})
	}))
	defer server.Close()

	models, err := discoverAnthropicModels(t.Context(), server.Client(), server.URL+"/v1/messages", "anthropic-key")
	if err != nil {
		t.Fatalf("discoverAnthropicModels failed: %v", err)
	}
	if len(models) != 1 || models[0].ID != "claude-sonnet-4-6" || models[0].DisplayName != "Claude Sonnet 4.6" {
		t.Fatalf("unexpected models: %+v", models)
	}
}

func TestDiscoverGeminiModels(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("key"); got != "gemini-key" {
			t.Fatalf("unexpected key query: %q", got)
		}
		json.NewEncoder(w).Encode(geminiModelsResponse{
			Models: []struct {
				Name        string `json:"name"`
				DisplayName string `json:"displayName"`
			}{
				{Name: "models/gemini-3-pro-preview", DisplayName: "Gemini 3 Pro"},
			},
		})
	}))
	defer server.Close()

	models, err := discoverGeminiModels(t.Context(), server.Client(), server.URL+"/v1beta", "gemini-key")
	if err != nil {
		t.Fatalf("discoverGeminiModels failed: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gemini-3-pro-preview" || models[0].DisplayName != "Gemini 3 Pro" {
		t.Fatalf("unexpected models: %+v", models)
	}
}

func TestHandleDiscoverModelsEndpoint(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(openAIModelsResponse{
			Data: []struct {
				ID string `json:"id"`
			}{{ID: "only-model"}},
		})
	}))
	defer server.Close()

	h := NewTestHarness(t)
	reqBody, err := json.Marshal(DiscoverModelsRequest{
		ProviderType: "openai",
		Endpoint:     server.URL + "/v1",
		APIKey:       "test-key",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/custom-models-discover", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleDiscoverModels(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if success, ok := result["success"].(bool); !ok || !success {
		t.Fatalf("expected success=true, got %+v", result)
	}
	models, ok := result["models"].([]any)
	if !ok || len(models) != 1 {
		t.Fatalf("expected one model, got %+v", result["models"])
	}
}