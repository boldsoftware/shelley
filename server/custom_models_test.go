package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/oai"
)

func TestCustomModelAPIKeyRedaction(t *testing.T) {
	h := NewTestHarness(t)
	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	const originalKey = "secret-original-provider-key"
	const replacementKey = "secret-replacement-provider-key"
	modelID := ""
	for _, step := range []struct {
		name, method, suffix, key, storedKey string
		status                               int
	}{
		{"create", http.MethodPost, "", originalKey, originalKey, http.StatusCreated},
		{"list", http.MethodGet, "", "", originalKey, http.StatusOK},
		{"get", http.MethodGet, "", "", originalKey, http.StatusOK},
		{"update preserves key", http.MethodPut, "", "", originalKey, http.StatusOK},
		{"update replaces key", http.MethodPut, "", replacementKey, replacementKey, http.StatusOK},
		{"duplicate", http.MethodPost, "/duplicate", "", replacementKey, http.StatusCreated},
	} {
		t.Run(step.name, func(t *testing.T) {
			path := "/api/custom-models"
			if step.name != "create" && step.name != "list" {
				path += "/" + modelID
			}
			path += step.suffix
			body, err := json.Marshal(CreateModelRequest{
				DisplayName: "Secret model", ProviderType: "openai",
				Endpoint: "https://example.test/v1", APIKey: step.key, ModelName: "test-model",
			})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(step.method, path, bytes.NewReader(body)))
			if rec.Code != step.status {
				t.Fatalf("status = %d, want %d", rec.Code, step.status)
			}
			for _, forbidden := range []string{`"api_key"`, originalKey, replacementKey} {
				if strings.Contains(rec.Body.String(), forbidden) {
					t.Fatal("response exposes an API key field or secret")
				}
			}
			var response ModelAPI
			if step.name == "list" {
				var list []ModelAPI
				if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
					t.Fatal(err)
				}
				if len(list) != 1 {
					t.Fatalf("got %d models, want 1", len(list))
				}
				response = list[0]
			} else if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if !response.HasAPIKey {
				t.Fatal("has_api_key = false for a stored key")
			}
			if response.ModelID == "" {
				t.Fatal("missing model ID")
			}
			if step.name == "create" {
				modelID = response.ModelID
			}
			stored, err := h.db.GetModel(t.Context(), response.ModelID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.ApiKey != step.storedKey {
				t.Fatal("stored API key was not preserved or replaced as requested")
			}
		})
	}
}

func TestToModelAPIWithoutKey(t *testing.T) {
	model := toModelAPI(generated.Model{})
	if model.HasAPIKey {
		t.Fatal("has_api_key = true for an empty key")
	}
}

func TestValidReasoningMapAcceptsMax(t *testing.T) {
	if err := validReasoningMap(`{"max":"max"}`); err != nil {
		t.Fatal(err)
	}
}

func TestValidReasoningReplay(t *testing.T) {
	for _, tt := range []struct {
		in, want oai.ReasoningReplay
		wantErr  bool
	}{
		{in: "", want: "auto"},
		{in: "auto", want: "auto"},
		{in: "none", want: "none"},
		{in: "reasoning_content", want: "reasoning_content"},
		{in: "reasoning_details", wantErr: true},
	} {
		got, err := validReasoningReplay(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Fatalf("validReasoningReplay(%q) = (%q, %v), want (%q, err=%v)", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestCustomModelMutationsNotifyModelsChanged(t *testing.T) {
	h := NewTestHarness(t)
	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	body := []byte(`{
		"display_name":"Custom model",
		"provider_type":"openai",
		"endpoint":"https://proxy.example/v1",
		"api_key":"test-key",
		"model_name":"custom-model"
	}`)
	var model ModelAPI
	for _, step := range []struct {
		method string
		suffix string
		status int
	}{
		{http.MethodPost, "", http.StatusCreated},
		{http.MethodPut, "", http.StatusOK},
		{http.MethodPost, "/duplicate", http.StatusCreated},
		{http.MethodDelete, "", http.StatusNoContent},
	} {
		ctx, cancel := context.WithCancel(t.Context())
		nextA := h.server.streamPub.Subscribe(ctx, -1)
		nextB := h.server.streamPub.Subscribe(ctx, -1)
		path := "/api/custom-models"
		if model.ModelID != "" {
			path += "/" + model.ModelID
		}
		path += step.suffix
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(step.method, path, bytes.NewReader(body)))
		cancel()
		if rec.Code != step.status {
			t.Fatalf("%s %s: status=%d body=%s", step.method, path, rec.Code, rec.Body.String())
		}
		for i, next := range []func() (StreamResponse, bool){nextA, nextB} {
			count := 0
			for event, ok := next(); ok; event, ok = next() {
				if event.ModelsChanged {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("%s %s: subscriber %d got %d catalog events, want 1", step.method, path, i, count)
			}
		}
		if model.ModelID == "" {
			if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCustomModelReasoningReplayLifecycle(t *testing.T) {
	h := NewTestHarness(t)
	createBody := []byte(`{
		"display_name":"Replay model",
		"provider_type":"openai",
		"endpoint":"https://proxy.example/v1",
		"api_key":"test-key",
		"model_name":"custom-model",
		"reasoning_replay":"reasoning_content"
	}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/custom-models", bytes.NewReader(createBody))
	createRec := httptest.NewRecorder()
	h.server.handleCreateModel(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var created ModelAPI
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ReasoningReplay != "reasoning_content" || created.ResolvedReasoningReplay != "reasoning_content" {
		t.Fatalf("created replay fields = (%q, %q)", created.ReasoningReplay, created.ResolvedReasoningReplay)
	}
	stored, err := h.db.GetModel(t.Context(), created.ModelID)
	if err != nil || stored.ReasoningReplay != "reasoning_content" {
		t.Fatalf("stored replay field = (%q, %v)", stored.ReasoningReplay, err)
	}

	preserveBody := []byte(`{
		"display_name":"Replay model",
		"provider_type":"openai",
		"endpoint":"https://proxy.example/v1",
		"model_name":"custom-model"
	}`)
	preserveReq := httptest.NewRequest(http.MethodPut, "/api/custom-models/"+created.ModelID, bytes.NewReader(preserveBody))
	preserveRec := httptest.NewRecorder()
	h.server.handleUpdateModel(preserveRec, preserveReq, created.ModelID)
	if preserveRec.Code != http.StatusOK {
		t.Fatalf("preserve status=%d body=%s", preserveRec.Code, preserveRec.Body.String())
	}
	var preserved ModelAPI
	if err := json.Unmarshal(preserveRec.Body.Bytes(), &preserved); err != nil {
		t.Fatal(err)
	}
	if preserved.ReasoningReplay != "reasoning_content" {
		t.Fatalf("omitted update changed replay field to %q", preserved.ReasoningReplay)
	}

	duplicateRec := httptest.NewRecorder()
	h.server.handleDuplicateModel(duplicateRec, httptest.NewRequest(http.MethodPost, "/api/custom-models/"+created.ModelID+"/duplicate", nil), created.ModelID)
	if duplicateRec.Code != http.StatusCreated {
		t.Fatalf("duplicate status=%d body=%s", duplicateRec.Code, duplicateRec.Body.String())
	}
	var duplicate ModelAPI
	if err := json.Unmarshal(duplicateRec.Body.Bytes(), &duplicate); err != nil {
		t.Fatal(err)
	}
	if duplicate.ReasoningReplay != oai.ReasoningReplayContent {
		t.Fatalf("duplicate replay = %q", duplicate.ReasoningReplay)
	}

	listRec := httptest.NewRecorder()
	h.server.handleListModels(listRec, httptest.NewRequest(http.MethodGet, "/api/custom-models", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var listed []ModelAPI
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, model := range listed {
		if model.ModelID != created.ModelID {
			continue
		}
		found = true
		if model.ReasoningReplay != oai.ReasoningReplayContent {
			t.Fatalf("listed replay = %q", model.ReasoningReplay)
		}
	}
	if !found {
		t.Fatal("created model missing from list")
	}

	updateBody := []byte(`{
		"display_name":"Replay model",
		"provider_type":"openai",
		"endpoint":"https://proxy.example/v1",
		"model_name":"custom-model",
		"reasoning_replay":""
	}`)
	updateReq := httptest.NewRequest(http.MethodPut, "/api/custom-models/"+created.ModelID, bytes.NewReader(updateBody))
	updateRec := httptest.NewRecorder()
	h.server.handleUpdateModel(updateRec, updateReq, created.ModelID)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updateRec.Code, updateRec.Body.String())
	}
	var updated ModelAPI
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.ReasoningReplay != "auto" || updated.ResolvedReasoningReplay != "" {
		t.Fatalf("updated replay fields = (%q, %q)", updated.ReasoningReplay, updated.ResolvedReasoningReplay)
	}
	stored, err = h.db.GetModel(t.Context(), created.ModelID)
	if err != nil || stored.ReasoningReplay != "auto" {
		t.Fatalf("reset replay field = (%q, %v)", stored.ReasoningReplay, err)
	}
}

func TestCustomModelAutoInfersReasoningReplay(t *testing.T) {
	h := NewTestHarness(t)
	body := []byte(`{
		"display_name":"GLM proxy",
		"provider_type":"openai",
		"endpoint":"https://proxy.example/v1",
		"api_key":"test-key",
		"model_name":"fireworks/glm-5p2"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/custom-models", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.server.handleCreateModel(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created ModelAPI
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ReasoningReplay != "auto" || created.ResolvedReasoningReplay != "reasoning_content" {
		t.Fatalf("auto replay fields = (%q, %q)", created.ReasoningReplay, created.ResolvedReasoningReplay)
	}
	stored, err := h.db.GetModel(t.Context(), created.ModelID)
	if err != nil || stored.ReasoningReplay != "auto" {
		t.Fatalf("stored auto replay field = (%q, %v)", stored.ReasoningReplay, err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/custom-models/"+created.ModelID, nil)
	getRec := httptest.NewRecorder()
	h.server.handleGetModel(getRec, getReq, created.ModelID)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	var fetched ModelAPI
	if err := json.Unmarshal(getRec.Body.Bytes(), &fetched); err != nil {
		t.Fatal(err)
	}
	if fetched.ReasoningReplay != "auto" || fetched.ResolvedReasoningReplay != "reasoning_content" {
		t.Fatalf("fetched auto replay fields = (%q, %q)", fetched.ReasoningReplay, fetched.ResolvedReasoningReplay)
	}
}

func TestCustomModelAPIPersistsMaxOutputTokens(t *testing.T) {
	h := NewTestHarness(t)
	body := []byte(`{
		"display_name":"Test model",
		"provider_type":"openai",
		"endpoint":"https://example.test/v1",
		"api_key":"test-key",
		"model_name":"test-model",
		"max_tokens":77777
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/custom-models", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.server.handleCreateModel(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create custom model: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response ModelAPI
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.MaxTokens != 77777 {
		t.Fatalf("response max_tokens = %d, want 77777", response.MaxTokens)
	}
	stored, err := h.db.GetModel(context.Background(), response.ModelID)
	if err != nil {
		t.Fatalf("get stored custom model: %v", err)
	}
	if stored.MaxTokens != 77777 {
		t.Fatalf("stored max_tokens = %d, want 77777", stored.MaxTokens)
	}
}

func TestCustomModelUpdatePersistsMaxOutputTokens(t *testing.T) {
	h := NewTestHarness(t)
	created, err := h.db.CreateModel(context.Background(), generated.CreateModelParams{
		ModelID:      "legacy-max",
		DisplayName:  "Legacy max",
		ProviderType: "openai",
		Endpoint:     "https://example.test/v1",
		ApiKey:       "test-key",
		ModelName:    "test-model",
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	body := []byte(`{
		"display_name":"Renamed model",
		"provider_type":"openai",
		"endpoint":"https://example.test/v1",
		"api_key":"",
		"model_name":"test-model",
		"max_tokens":77777
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/custom-models/"+created.ModelID, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.server.handleUpdateModel(rec, req, created.ModelID)
	if rec.Code != http.StatusOK {
		t.Fatalf("update custom model: status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := h.db.GetModel(context.Background(), created.ModelID)
	if err != nil {
		t.Fatalf("get updated model: %v", err)
	}
	if stored.MaxTokens != 77777 {
		t.Fatalf("stored max_tokens = %d, want 77777", stored.MaxTokens)
	}
}

func TestCustomModelUpdateWithoutMaxOutputTokensPreservesExistingValue(t *testing.T) {
	h := NewTestHarness(t)
	created, err := h.db.CreateModel(context.Background(), generated.CreateModelParams{
		ModelID: "preserve-max", DisplayName: "Preserve max", ProviderType: "openai",
		Endpoint: "https://example.test/v1", ApiKey: "test-key", ModelName: "test-model", MaxTokens: 77777,
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	body := []byte(`{
		"display_name":"Renamed model",
		"provider_type":"openai",
		"endpoint":"https://example.test/v1",
		"api_key":"",
		"model_name":"test-model"
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/custom-models/"+created.ModelID, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.server.handleUpdateModel(rec, req, created.ModelID)
	if rec.Code != http.StatusOK {
		t.Fatalf("update custom model: status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := h.db.GetModel(context.Background(), created.ModelID)
	if err != nil {
		t.Fatalf("get updated model: %v", err)
	}
	if stored.MaxTokens != 77777 {
		t.Fatalf("stored max_tokens = %d, want 77777", stored.MaxTokens)
	}
}

// A custom model created without max_tokens stores 0, which means the
// provider's own default applies at request time.
func TestCustomModelCreateBlankMaxOutputTokensStoresZero(t *testing.T) {
	h := NewTestHarness(t)
	body := []byte(`{
		"display_name":"Test model",
		"provider_type":"openai",
		"endpoint":"https://example.test/v1",
		"api_key":"test-key",
		"model_name":"test-model"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/custom-models", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.server.handleCreateModel(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create custom model: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response ModelAPI
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.MaxTokens != 0 {
		t.Fatalf("response max_tokens = %d, want 0", response.MaxTokens)
	}
}

func TestCustomModelRejectsNegativeMaxOutputTokens(t *testing.T) {
	h := NewTestHarness(t)
	createBody := []byte(`{
		"display_name":"Test model",
		"provider_type":"openai",
		"endpoint":"https://example.test/v1",
		"api_key":"test-key",
		"model_name":"test-model",
		"max_tokens":-1
	}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/custom-models", bytes.NewReader(createBody))
	createRec := httptest.NewRecorder()
	h.server.handleCreateModel(createRec, createReq)
	if createRec.Code != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400: %s", createRec.Code, createRec.Body.String())
	}

	created, err := h.db.CreateModel(context.Background(), generated.CreateModelParams{
		ModelID: "negative-max", DisplayName: "Negative max", ProviderType: "openai",
		Endpoint: "https://example.test/v1", ApiKey: "test-key", ModelName: "test-model", MaxTokens: 77777,
	})
	if err != nil {
		t.Fatalf("create stored model: %v", err)
	}
	updateBody := []byte(`{
		"display_name":"Negative max",
		"provider_type":"openai",
		"endpoint":"https://example.test/v1",
		"api_key":"",
		"model_name":"test-model",
		"max_tokens":-1
	}`)
	updateReq := httptest.NewRequest(http.MethodPut, "/api/custom-models/"+created.ModelID, bytes.NewReader(updateBody))
	updateRec := httptest.NewRecorder()
	h.server.handleUpdateModel(updateRec, updateReq, created.ModelID)
	if updateRec.Code != http.StatusBadRequest {
		t.Fatalf("update status = %d, want 400: %s", updateRec.Code, updateRec.Body.String())
	}

	testReq := httptest.NewRequest(http.MethodPost, "/api/custom-models-test", bytes.NewReader([]byte(`{
		"provider_type":"openai",
		"endpoint":"https://example.test/v1",
		"api_key":"test-key",
		"model_name":"test-model",
		"max_tokens":-1
	}`)))
	testRec := httptest.NewRecorder()
	h.server.handleTestModel(testRec, testReq)
	if testRec.Code != http.StatusBadRequest {
		t.Fatalf("test status = %d, want 400: %s", testRec.Code, testRec.Body.String())
	}
}

func TestCustomModelTestMaxOutputTokenPresence(t *testing.T) {
	zero := int64(0)
	for _, tc := range []struct {
		name      string
		maxTokens *int64
		want      float64
	}{
		{name: "omitted uses saved value", want: 77777},
		{name: "explicit zero uses provider default", maxTokens: &zero, want: float64(oai.DefaultMaxTokens)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer saved-key" {
					t.Error("test request did not use the stored API key")
				}
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Errorf("decode upstream request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chat-test","choices":[{"message":{"role":"assistant","content":"test successful"},"finish_reason":"stop"}]}`))
			}))
			defer upstream.Close()

			h := NewTestHarness(t)
			modelID := "test-max-output"
			if _, err := h.db.CreateModel(context.Background(), generated.CreateModelParams{
				ModelID: modelID, DisplayName: "Test max output", ProviderType: "openai",
				Endpoint: upstream.URL, ApiKey: "saved-key", ModelName: "test-model", MaxTokens: 77777,
			}); err != nil {
				t.Fatalf("create model: %v", err)
			}
			body := map[string]any{
				"model_id": modelID, "provider_type": "openai", "endpoint": upstream.URL,
				"model_name": "test-model",
			}
			if tc.maxTokens != nil {
				body["max_tokens"] = *tc.maxTokens
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/custom-models-test", bytes.NewReader(encoded))
			rec := httptest.NewRecorder()
			h.server.handleTestModel(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("test status = %d: %s", rec.Code, rec.Body.String())
			}
			if got["max_completion_tokens"] != tc.want {
				t.Fatalf("max_completion_tokens = %#v, want %.0f", got["max_completion_tokens"], tc.want)
			}
		})
	}
}

// TestCustomModelWithThinking tests that the custom model test endpoint
// correctly handles responses from Anthropic models with ThinkingLevel enabled.
// When thinking is enabled, the first content block is a thinking block, not text.
func TestCustomModelWithThinking(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping integration test")
	}

	// Create a service with thinking enabled
	service := &ant.Service{
		APIKey:        apiKey,
		Model:         ant.Claude46Opus,
		ThinkingLevel: llm.ThinkingLevelMedium,
	}

	// Send a simple test request
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	request := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Say 'test successful' in exactly two words."},
				},
			},
		},
	}

	response, err := service.Do(ctx, request)
	if err != nil {
		t.Fatalf("API call failed: %v", err)
	}

	// Verify response has content
	if len(response.Content) == 0 {
		t.Fatal("Response has no content blocks")
	}

	// The first block should be a thinking block
	if response.Content[0].Type != llm.ContentTypeThinking {
		t.Logf("Warning: Expected first block to be thinking, got %v", response.Content[0].Type)
	}

	// Find the first text block (skipping thinking blocks)
	var foundText bool
	var responseText string
	for _, content := range response.Content {
		if content.Type == llm.ContentTypeText && content.Text != "" {
			responseText = content.Text
			foundText = true
			break
		}
	}

	if !foundText {
		t.Fatal("No text content found in response (only thinking blocks)")
	}

	t.Logf("Successfully received response with thinking enabled: %s", responseText)
}

// TestCustomModelTestEndpoint tests the HTTP endpoint for testing custom models.
// This simulates what happens when a user adds a custom Anthropic model in the UI.
func TestCustomModelTestEndpoint(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping integration test")
	}

	h := NewTestHarness(t)

	// Create a test request that simulates adding a custom Anthropic model
	testReq := struct {
		ProviderType string `json:"provider_type"`
		APIKey       string `json:"api_key"`
		Endpoint     string `json:"endpoint"`
		ModelName    string `json:"model_name"`
	}{
		ProviderType: "anthropic",
		APIKey:       apiKey,
		Endpoint:     "https://api.anthropic.com/v1/messages",
		ModelName:    ant.Claude46Opus,
	}

	body, err := json.Marshal(testReq)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/custom-models/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleTestModel(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if success, ok := result["success"].(bool); !ok || !success {
		t.Errorf("Test failed: %v", result["message"])
	}

	message, ok := result["message"].(string)
	if !ok {
		t.Fatal("Response missing message field")
	}

	t.Logf("Test endpoint response: %s", message)

	// Verify that we got a non-empty response
	if message == "" || message == "Test failed: empty response from model" {
		t.Error("Got empty response error despite having a valid API key")
	}
}
