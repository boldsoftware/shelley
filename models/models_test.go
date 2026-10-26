package models

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/models/modelsdev"
)

// predictableBuilt returns a Built entry for the predictable test model.
// Tests that need a manager seeded with at least one model use this.
func predictableBuilt() Built {
	return Built{
		ID:       "predictable",
		Provider: ProviderBuiltIn,
		Source:   "test",
		Service:  predictable.NewService(),
	}
}

func TestAll(t *testing.T) {
	models := All()
	if len(models) == 0 {
		t.Fatal("expected at least one model")
	}
	for _, m := range models {
		if m.ID == "" {
			t.Errorf("model missing ID")
		}
		if m.Provider == "" {
			t.Errorf("model %s missing Provider", m.ID)
		}
		if m.Build == nil {
			t.Errorf("model %s missing Build", m.ID)
		}
	}
}

func TestGPT61SolInBuiltInCatalog(t *testing.T) {
	for _, m := range All() {
		if m.ID != "gpt-6.1-sol" {
			continue
		}
		if m.APIModelName != "gpt-6.1-sol" || m.Description != "GPT-6.1 Sol" || m.APIType != APITypeOpenAIResponses || m.Build == nil {
			t.Fatalf("GPT-6.1 Sol = %+v", m)
		}
		return
	}
	t.Fatal("GPT-6.1 Sol not in built-in catalog")
}

func TestGPT61TerraInBuiltInCatalog(t *testing.T) {
	for _, m := range All() {
		if m.ID != "gpt-6.1-terra" {
			continue
		}
		if m.APIModelName != "gpt-6.1-terra" || m.Description != "GPT-6.1 Terra" || m.APIType != APITypeOpenAIResponses || m.Build == nil {
			t.Fatalf("GPT-6.1 Terra = %+v", m)
		}
		svc, ok := m.Build("", "", nil).(*oai.ResponsesService)
		if !ok || svc.Model != oai.GPT61Terra || svc.MaxTokens != 128000 {
			t.Fatalf("GPT-6.1 Terra service = %+v", svc)
		}
		return
	}
	t.Fatal("GPT-6.1 Terra not in built-in catalog")
}

func TestSonnet55InBuiltInCatalog(t *testing.T) {
	var found bool
	for _, m := range All() {
		if m.ID != "claude-sonnet-5.5" {
			continue
		}
		found = true
		if m.APIModelName != "claude-sonnet-5-5" || m.Description != "Claude Sonnet 5.5" || m.APIType != APITypeAnthropicMessages || m.Build == nil {
			t.Fatalf("Sonnet 5.5 = %+v", m)
		}
	}
	if !found {
		t.Fatal("Claude Sonnet 5.5 not in built-in catalog")
	}
}

func TestHaiku55InBuiltInCatalog(t *testing.T) {
	models := All()
	for i, m := range models {
		if m.ID != "claude-haiku-5.5" {
			continue
		}
		if m.Provider != ProviderAnthropic || m.APIModelName != "claude-haiku-5-5" ||
			m.Description != "Claude Haiku 5.5" || m.APIType != APITypeAnthropicMessages ||
			m.DefaultBaseURL != DefaultAnthropicBaseURL || m.Build == nil {
			t.Fatalf("Haiku 5.5 = %+v", m)
		}
		for j, old := range models {
			if old.ID == "claude-haiku-4.5" {
				if i >= j {
					t.Fatalf("new Haiku at %d after old Haiku at %d", i, j)
				}
				return
			}
		}
		t.Fatal("Claude Haiku 4.5 was removed")
	}
	t.Fatal("Claude Haiku 5.5 not in built-in catalog")
}

func TestIDs(t *testing.T) {
	ids := IDs()
	if len(ids) == 0 {
		t.Fatal("expected at least one model ID")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate model ID: %s", id)
		}
		seen[id] = true
	}
}

func TestNewManagerRegistersBuiltModels(t *testing.T) {
	mgr, err := NewManager(&Config{Models: []Built{predictableBuilt()}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	svc, err := mgr.GetService("predictable")
	if err != nil || svc == nil {
		t.Fatalf("GetService(predictable) failed: svc=%v err=%v", svc, err)
	}
	info := mgr.GetModelInfo("predictable")
	if info == nil {
		t.Fatalf("GetModelInfo(predictable) = nil")
	}
	if info.Source != "test" {
		t.Errorf("source = %q, want %q", info.Source, "test")
	}
	if info.DisplayName != "predictable" {
		t.Errorf("display name = %q, want %q", info.DisplayName, "predictable")
	}
}

func TestLoggingServiceUsageCollector(t *testing.T) {
	type collected struct {
		purpose string
		usage   llm.Usage
	}
	var got []collected
	svc := &loggingService{
		service: &mockLLMService{},
		logger:  slog.Default(),
		modelID: "test-model",
	}
	req := &llm.Request{Messages: []llm.Message{llm.UserStringMessage("hi")}}
	ctxWithCollector := llm.WithUsageCollector(t.Context(), func(purpose string, usage llm.Usage) {
		got = append(got, collected{purpose, usage})
	})

	// No purpose tag: nothing collected even with a collector.
	if _, err := svc.Do(ctxWithCollector, req); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("collected %d calls without purpose, want 0", len(got))
	}

	// Purpose tag: collected, model falls back to modelID (mock leaves Model empty).
	ctx := llm.WithPurpose(ctxWithCollector, "keyword_search")
	if _, err := svc.Do(ctx, req); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("collected %d calls, want 1", len(got))
	}
	r := got[0]
	if r.purpose != "keyword_search" || r.usage.Model != "test-model" {
		t.Errorf("collected purpose=%q model=%q, want keyword_search/test-model", r.purpose, r.usage.Model)
	}
	if r.usage.InputTokens != 10 || r.usage.OutputTokens != 5 || r.usage.CostUSD != 0.001 {
		t.Errorf("collected usage = %+v", r.usage)
	}

	// Zero usage: not collected even with a purpose tag.
	svc.service = &zeroUsageLLMService{}
	if _, err := svc.Do(ctx, req); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("zero-usage response was collected (%d calls)", len(got))
	}

	// Purpose tag but no collector in ctx: no panic, nothing collected.
	svc.service = &mockLLMService{}
	if _, err := svc.Do(llm.WithPurpose(t.Context(), "keyword_search"), req); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("collector-less call was collected (%d calls)", len(got))
	}
}

// zeroUsageLLMService responds with no usage data.
type zeroUsageLLMService struct{ mockLLMService }

func (z *zeroUsageLLMService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: llm.TextContent("ok")}, nil
}

// mockLLMService implements llm.Service for testing.
type mockLLMService struct {
	maxImageDimension int
}

func (m *mockLLMService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	return &llm.Response{
		Content: llm.TextContent("Hello, world!"),
		Usage:   llm.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.001},
	}, nil
}

func (m *mockLLMService) Provider() string { return "" }

func (m *mockLLMService) MaxImageDimension() int {
	if m.maxImageDimension == 0 {
		return 2048
	}
	return m.maxImageDimension
}

func (m *mockLLMService) MaxImageBytes() int { return 5 * 1024 * 1024 }

func TestManagerGetService(t *testing.T) {
	mgr, err := NewManager(&Config{Models: []Built{predictableBuilt()}})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if svc, err := mgr.GetService("predictable"); err != nil || svc == nil {
		t.Errorf("GetService(predictable): svc=%v err=%v", svc, err)
	}
	if _, err := mgr.GetService("non-existent-model"); err == nil {
		t.Error("GetService(non-existent) should have failed")
	}
}

func TestManagerHasModel(t *testing.T) {
	mgr, err := NewManager(&Config{Models: []Built{predictableBuilt()}})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if !mgr.HasModel("predictable") {
		t.Error("HasModel(predictable) should return true")
	}
	if mgr.HasModel("claude-opus-4.7") {
		t.Error("HasModel(claude-opus-4.7) should return false without sources")
	}
	if mgr.HasModel("non-existent-model") {
		t.Error("HasModel(non-existent) should return false")
	}
}

func TestRefreshCustomModelsConcurrent(t *testing.T) {
	testDB, err := db.New(db.Config{DSN: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}
	defer testDB.Close()
	if err := testDB.Migrate(t.Context()); err != nil {
		t.Fatalf("failed to migrate test db: %v", err)
	}
	if _, err := testDB.CreateModel(t.Context(), generated.CreateModelParams{
		ModelID:      "custom-test-model",
		DisplayName:  "Test Model",
		ProviderType: "openai",
		Endpoint:     "https://api.example.com/v1",
		ApiKey:       "test-key",
		ModelName:    "test-model",
	}); err != nil {
		t.Fatalf("failed to create test model: %v", err)
	}

	mgr, err := NewManager(&Config{DB: testDB})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	var wg sync.WaitGroup
	const N = 10
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				mgr.GetAvailableModels()
				mgr.HasModel("custom-test-model")
				mgr.GetModelInfo("custom-test-model")
				mgr.GetService("custom-test-model")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 100; j++ {
			mgr.RefreshCustomModels()
		}
	}()
	wg.Wait()
}

func TestCustomOpenAIModelsCapLegacyMaxOutputTokens(t *testing.T) {
	wantMax, found := modelsdev.LookupOutputLimit("", "gpt-5.6-sol")
	if !found {
		t.Fatal("no catalog output limit for gpt-5.6-sol")
	}
	for _, tc := range []struct {
		name     string
		provider string
		path     string
		response string
	}{
		{
			name:     "chat completions",
			provider: "openai",
			path:     "/chat/completions",
			response: `{"id":"chat-test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		},
		{
			name:     "responses",
			provider: "openai-responses",
			path:     "/responses",
			response: `{"id":"responses-test","status":"completed","model":"test-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got map[string]json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("request path = %q, want %q", r.URL.Path, tc.path)
				}
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Errorf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()

			testDB := newTestModelDB(t)
			modelID := "legacy-" + tc.provider
			if _, err := testDB.CreateModel(context.Background(), generated.CreateModelParams{
				ModelID: modelID, DisplayName: modelID, ProviderType: tc.provider,
				Endpoint: server.URL, ApiKey: "test-key", ModelName: "gpt-5.6-sol",
			}); err != nil {
				t.Fatalf("create model: %v", err)
			}
			if err := testDB.Pool().Exec(context.Background(), "UPDATE models SET max_tokens = ? WHERE model_id = ?", 200000, modelID); err != nil {
				t.Fatalf("seed legacy max_tokens: %v", err)
			}

			manager, err := NewManager(&Config{DB: testDB, HTTPC: server.Client()})
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			service, err := manager.GetService(modelID)
			if err != nil {
				t.Fatalf("GetService: %v", err)
			}
			response, err := service.Do(context.Background(), simpleRequest())
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if len(response.Content) == 0 || response.Content[0].Text != "ok" {
				t.Fatalf("response = %#v, want successful provider response", response)
			}
			wantKey := "max_completion_tokens"
			if tc.provider == "openai-responses" {
				wantKey = "max_output_tokens"
			}
			var maxTokens int
			if err := json.Unmarshal(got[wantKey], &maxTokens); err != nil {
				t.Fatalf("%s = %s, want %d", wantKey, got[wantKey], wantMax)
			}
			if maxTokens != wantMax {
				t.Fatalf("%s = %d, want %d", wantKey, maxTokens, wantMax)
			}
		})
	}
}

func TestCustomAnthropicModelsUseCatalogLimitsOnWire(t *testing.T) {
	for _, tc := range []struct {
		name      string
		endpoint  string
		model     string
		wantMax   int
		captured  bool
		maxTokens int64
	}{
		{
			name:    "unknown endpoint known Claude uses canonical catalog",
			model:   "claude-sonnet-5",
			wantMax: 128000,
		},
		{
			name:     "known provider endpoint uses its snapshot entry",
			endpoint: "https://api.fireworks.ai/inference/v1",
			model:    "accounts/fireworks/models/gpt-oss-120b",
			wantMax:  32768,
			captured: true,
		},
		{
			name:      "unknown model may lower the default output limit",
			model:     "custom-claude",
			wantMax:   8192,
			maxTokens: 8192,
		},
		{
			name:      "unknown model with stale context-token value is capped",
			model:     "custom-claude",
			wantMax:   64000,
			maxTokens: 200000,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got struct {
				MaxTokens int `json:"max_tokens"`
				Thinking  *struct {
					BudgetTokens int `json:"budget_tokens"`
				} `json:"thinking"`
			}
			var gotRaw map[string]json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&gotRaw); err != nil {
					t.Errorf("decode request: %v", err)
				}
				if err := json.Unmarshal([]byte(mustJSON(t, gotRaw)), &got); err != nil {
					t.Errorf("decode max token fields: %v", err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(mockAnthropicSSE(tc.model)))
			}))
			defer server.Close()

			endpoint := tc.endpoint
			httpc := server.Client()
			if endpoint == "" {
				endpoint = server.URL
			}
			var captured *captureTransport
			if tc.captured {
				captured = &captureTransport{response: mockAnthropicSSE(tc.model)}
				httpc = &http.Client{Transport: captured}
			}
			testDB := newTestModelDB(t)
			modelID := "anthropic-" + strings.ReplaceAll(tc.model, "/", "-")
			if _, err := testDB.CreateModel(context.Background(), generated.CreateModelParams{
				ModelID: modelID, DisplayName: modelID, ProviderType: "anthropic",
				Endpoint: endpoint, ApiKey: "test-key", ModelName: tc.model, MaxTokens: tc.maxTokens, ReasoningSupport: "yes",
			}); err != nil {
				t.Fatalf("create model: %v", err)
			}

			manager, err := NewManager(&Config{DB: testDB, HTTPC: httpc})
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			service, err := manager.GetService(modelID)
			if err != nil {
				t.Fatalf("GetService: %v", err)
			}
			request := simpleRequest()
			request.ThinkingLevel = llm.ThinkingLevelHigh
			response, err := service.Do(context.Background(), request)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if captured != nil {
				if err := json.Unmarshal(captured.body, &gotRaw); err != nil {
					t.Fatalf("decode captured request: %v", err)
				}
				if err := json.Unmarshal([]byte(mustJSON(t, gotRaw)), &got); err != nil {
					t.Fatalf("decode captured max token fields: %v", err)
				}
			}
			if len(response.Content) == 0 || response.Content[0].Text != "ok" {
				t.Fatalf("response = %#v, want successful provider response", response)
			}
			if _, found := gotRaw["max_tokens"]; !found {
				t.Fatal("required max_tokens was omitted")
			}
			if got.MaxTokens != tc.wantMax {
				t.Fatalf("max_tokens = %d, want %d", got.MaxTokens, tc.wantMax)
			}
			if got.Thinking == nil {
				t.Fatal("thinking was omitted")
			}
			if got.Thinking.BudgetTokens >= got.MaxTokens {
				t.Fatalf("thinking budget = %d, want less than max_tokens %d", got.Thinking.BudgetTokens, got.MaxTokens)
			}
			if captured != nil && captured.url != endpoint {
				t.Fatalf("request URL = %q, want configured endpoint %q", captured.url, endpoint)
			}
		})
	}
}

func TestCustomGeminiModelCapsConfiguredOutputLimit(t *testing.T) {
	wantMax, found := modelsdev.LookupOutputLimit("", "gemini-3-flash-preview")
	if !found {
		t.Fatal("no catalog output limit for gemini-3-flash-preview")
	}
	var got map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
	}))
	defer server.Close()

	testDB := newTestModelDB(t)
	if _, err := testDB.CreateModel(context.Background(), generated.CreateModelParams{
		ModelID: "custom-gemini", DisplayName: "Custom Gemini", ProviderType: "gemini",
		Endpoint: server.URL, ApiKey: "test-key",
		ModelName: "gemini-3-flash-preview", MaxTokens: 200000,
	}); err != nil {
		t.Fatalf("create model: %v", err)
	}
	manager, err := NewManager(&Config{DB: testDB, HTTPC: server.Client()})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	service, err := manager.GetService("custom-gemini")
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	response, err := service.Do(context.Background(), simpleRequest())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(response.Content) == 0 || response.Content[0].Text != "ok" {
		t.Fatalf("response = %#v, want successful provider response", response)
	}
	var generationConfig struct {
		MaxOutputTokens int `json:"maxOutputTokens"`
	}
	if err := json.Unmarshal(got["generationConfig"], &generationConfig); err != nil {
		t.Fatalf("decode generationConfig: %v", err)
	}
	if generationConfig.MaxOutputTokens != wantMax {
		t.Fatalf("maxOutputTokens = %d, want %d", generationConfig.MaxOutputTokens, wantMax)
	}
}

func newTestModelDB(t *testing.T) *db.DB {
	t.Helper()
	testDB, err := db.New(db.Config{DSN: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("new test DB: %v", err)
	}
	t.Cleanup(func() { testDB.Close() })
	if err := testDB.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate test DB: %v", err)
	}
	return testDB
}

func simpleRequest() *llm.Request {
	return &llm.Request{Messages: []llm.Message{llm.UserStringMessage("hi")}}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return string(body)
}

func mockAnthropicSSE(model string) string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"" + model + "\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

type captureTransport struct {
	response string
	url      string
	body     []byte
}

func (t *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.url = req.URL.String()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	t.body = body
	req.Body.Close()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(t.response)),
		Request:    req,
	}, nil
}

func TestRefreshBuiltModelsReplacesBuiltModelsAndPreservesCustomModels(t *testing.T) {
	testDB, err := db.New(db.Config{DSN: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}
	defer testDB.Close()
	if err := testDB.Migrate(t.Context()); err != nil {
		t.Fatalf("failed to migrate test db: %v", err)
	}
	if _, err := testDB.CreateModel(t.Context(), generated.CreateModelParams{
		ModelID:      "custom-test-model",
		DisplayName:  "Test Model",
		ProviderType: "openai",
		Endpoint:     "https://api.example.com/v1",
		ApiKey:       "test-key",
		ModelName:    "test-model",
	}); err != nil {
		t.Fatalf("failed to create test model: %v", err)
	}

	mgr, err := NewManager(&Config{
		Models: []Built{
			{
				ID:          "old-built",
				DisplayName: "Old Built",
				Provider:    ProviderBuiltIn,
				Source:      "old source",
				Service:     &mockLLMService{},
			},
		},
		DB: testDB,
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	if err := mgr.RefreshBuiltModels([]Built{
		{
			ID:          "new-built",
			DisplayName: "New Built",
			Provider:    ProviderBuiltIn,
			Source:      "new source",
			Service:     &mockLLMService{},
		},
	}); err != nil {
		t.Fatalf("RefreshBuiltModels failed: %v", err)
	}

	if mgr.HasModel("old-built") {
		t.Fatal("old built model was not removed")
	}
	if !mgr.HasModel("new-built") {
		t.Fatal("new built model was not added")
	}
	if !mgr.HasModel("custom-test-model") {
		t.Fatal("custom model was not preserved")
	}
	got := mgr.GetAvailableModels()
	want := []string{"new-built", "custom-test-model"}
	if len(got) != len(want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("models = %v, want %v", got, want)
		}
	}
}

func TestGetTranscriptionModelsIncludesIntegrationAndCustomRoutes(t *testing.T) {
	testDB, err := db.New(db.Config{DSN: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer testDB.Close()
	if err := testDB.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.CreateModel(t.Context(), generated.CreateModelParams{
		ModelID:      "custom-transcription-model",
		DisplayName:  "Transcription Model",
		ProviderType: "openai",
		Endpoint:     "https://api.example.com/v1",
		ApiKey:       "transcription-key",
		ModelName:    "gpt-transcribe",
	}); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(&Config{
		TranscriptionModels: []TranscriptionModel{{
			Model:    "gpt-transcribe",
			Endpoint: "https://llm.int.exe.xyz/v1/audio/transcriptions",
			Source:   "llm.int.exe.xyz",
		}},
		DB: testDB,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := mgr.GetTranscriptionModels("gpt-transcribe")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("transcription models = %+v, want integration and custom routes", got)
	}
	if got[0].Endpoint != "https://llm.int.exe.xyz/v1/audio/transcriptions" {
		t.Fatalf("integration route = %+v", got[0])
	}
	if got[1].Endpoint != "https://api.example.com/v1/audio/transcriptions" || got[1].APIKey != "transcription-key" {
		t.Fatalf("custom route = %+v", got[1])
	}
}

func (m *mockLLMService) SupportsImages() bool { return true }

func TestManagerLoadsCustomReasoningReplayOverride(t *testing.T) {
	testDB, err := db.New(db.Config{DSN: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer testDB.Close()
	if err := testDB.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.CreateModel(t.Context(), generated.CreateModelParams{
		ModelID: "replay-model", DisplayName: "Replay model", ProviderType: "openai",
		Endpoint: "https://proxy.example/v1", ApiKey: "key", ModelName: "custom-model",
		ReasoningReplay: string(oai.ReasoningReplayContent),
	}); err != nil {
		t.Fatal(err)
	}

	manager, err := NewManager(&Config{DB: testDB})
	if err != nil {
		t.Fatal(err)
	}
	service, err := manager.GetService("replay-model")
	if err != nil {
		t.Fatal(err)
	}
	wrapped, ok := service.(*reasoningService)
	if !ok {
		t.Fatalf("service type = %T", service)
	}
	chat, ok := wrapped.Service.(*oai.Service)
	if !ok {
		t.Fatalf("inner service type = %T", wrapped.Service)
	}
	if chat.ReasoningReplay != oai.ReasoningReplayContent {
		t.Fatalf("reasoning replay = %q", chat.ReasoningReplay)
	}
}

func TestReasoningServiceMapping(t *testing.T) {
	inner := &captureThinkingService{}
	svc := WrapReasoningConfig(inner, "", "unknown", "yes", `{"off":"off","minimal":"low","medium":"high","max":"max"}`)

	levels := llm.SupportedReasoningLevels(svc)
	if got := []string{levels[0].Name(), levels[1].Name(), levels[2].Name(), levels[3].Name()}; !reflect.DeepEqual(got, []string{"off", "minimal", "medium", "max"}) {
		t.Fatalf("levels = %v", got)
	}
	if _, err := svc.Do(t.Context(), &llm.Request{ThinkingLevel: llm.ThinkingLevelMinimal}); err != nil {
		t.Fatal(err)
	}
	if inner.got != llm.ThinkingLevelLow {
		t.Fatalf("mapped level = %s, want low", inner.got.Name())
	}
	if _, err := svc.Do(t.Context(), &llm.Request{ThinkingLevel: llm.ThinkingLevelMax}); err != nil {
		t.Fatal(err)
	}
	if inner.got != llm.ThinkingLevelMax {
		t.Fatalf("mapped level = %s, want max", inner.got.Name())
	}
}

// Reasoning effort is part of the cache key, so a warm must map it as Do does.
func TestReasoningServiceWarmsWithMappedLevel(t *testing.T) {
	inner := predictable.NewService()
	svc := WrapReasoningConfig(inner, "", "unknown", "yes", `{"minimal":"low"}`)
	if llm.CacheTTL(svc) != inner.CacheTTL() {
		t.Fatalf("CacheTTL = %v, want %v", llm.CacheTTL(svc), inner.CacheTTL())
	}
	if _, err := llm.WarmCache(t.Context(), svc, &llm.Request{ThinkingLevel: llm.ThinkingLevelMinimal}); err != nil {
		t.Fatal(err)
	}
	if warms := inner.WarmRequests(); len(warms) != 1 || warms[0].ThinkingLevel != llm.ThinkingLevelLow {
		t.Fatalf("warms = %+v, want one at low", warms)
	}
}

func TestReasoningServiceDelegatesLevelsWithoutMap(t *testing.T) {
	inner := &advertisedReasoningService{levels: []llm.ThinkingLevel{llm.ThinkingLevelOff, llm.ThinkingLevelHigh, llm.ThinkingLevelMax}}
	svc := WrapReasoningConfig(inner, "", "unknown", "yes", "")
	if got := llm.SupportedReasoningLevels(svc); !reflect.DeepEqual(got, inner.levels) {
		t.Fatalf("levels = %v, want %v", got, inner.levels)
	}
}

func TestReasoningServiceDisabled(t *testing.T) {
	inner := &captureThinkingService{}
	svc := WrapReasoningConfig(inner, "", "unknown", "no", "")
	if llm.SupportsReasoning(svc) {
		t.Fatal("disabled service reports reasoning support")
	}
	if _, err := svc.Do(t.Context(), &llm.Request{ThinkingLevel: llm.ThinkingLevelHigh}); err != nil {
		t.Fatal(err)
	}
	if inner.got != llm.ThinkingLevelOff {
		t.Fatalf("level = %s, want off", inner.got.Name())
	}
}

type advertisedReasoningService struct {
	captureThinkingService
	levels []llm.ThinkingLevel
}

func (s *advertisedReasoningService) SupportsReasoning() bool { return true }
func (s *advertisedReasoningService) SupportedReasoningLevels() []llm.ThinkingLevel {
	return s.levels
}

type captureThinkingService struct {
	mockLLMService
	got llm.ThinkingLevel
}

func (s *captureThinkingService) Do(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.got = req.ThinkingLevel
	return &llm.Response{}, nil
}

func TestReasoningServiceRejectsUnsupportedLevel(t *testing.T) {
	svc := WrapReasoningConfig(&captureThinkingService{}, "", "unknown", "yes", `{"low":"low"}`)
	_, err := svc.Do(t.Context(), &llm.Request{ThinkingLevel: llm.ThinkingLevelHigh})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("error = %v, want unsupported-level error", err)
	}
}

func TestReasoningServiceMapsServiceDefault(t *testing.T) {
	inner := &defaultThinkingService{captureThinkingService: captureThinkingService{}}
	svc := WrapReasoningConfig(inner, "", "unknown", "yes", `{"medium":"low"}`)
	if got := llm.ServiceDefaultReasoningLevel(svc); got != "low" {
		t.Fatalf("default = %q, want low", got)
	}
	if _, err := svc.Do(t.Context(), &llm.Request{}); err != nil {
		t.Fatal(err)
	}
	if inner.got != llm.ThinkingLevelLow {
		t.Fatalf("level = %s, want low", inner.got.Name())
	}
}

type defaultThinkingService struct{ captureThinkingService }

func (s *defaultThinkingService) DefaultReasoningLevel() string { return "medium" }
