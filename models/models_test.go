package models

import (
	"context"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
	"shelley.exe.dev/loop"
)

// predictableBuilt returns a Built entry for the predictable test model.
// Tests that need a manager seeded with at least one model use this.
func predictableBuilt() Built {
	return Built{
		ID:       "predictable",
		Provider: ProviderBuiltIn,
		Source:   "test",
		Service:  loop.NewPredictableService(),
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

func TestByID(t *testing.T) {
	tests := []struct {
		id      string
		wantID  string
		wantNil bool
	}{
		{id: "gpt-5.6-sol", wantID: "gpt-5.6-sol"},
		{id: "gpt-5.6-terra", wantID: "gpt-5.6-terra"},
		{id: "gpt-5.6-luna", wantID: "gpt-5.6-luna"},
		{id: "gpt-5.5", wantID: "gpt-5.5"},
		{id: "gpt-5.5-pro", wantNil: true},
		{id: "deepseek-v4-pro-fireworks", wantID: "deepseek-v4-pro-fireworks"},
		{id: "gpt-oss-20b-fireworks", wantID: "gpt-oss-20b-fireworks"},
		{id: "gpt-5.2-codex", wantID: "gpt-5.2-codex"},
		{id: "claude-sonnet-5", wantID: "claude-sonnet-5"},
		{id: "claude-sonnet-4.5", wantID: "claude-sonnet-4.5"},
		{id: "claude-haiku-4.5", wantID: "claude-haiku-4.5"},
		{id: "claude-opus-4.5", wantID: "claude-opus-4.5"},
		{id: "claude-fable-5", wantID: "claude-fable-5"},
		{id: "claude-opus-4.8", wantID: "claude-opus-4.8"},
		{id: "claude-opus-4.7", wantID: "claude-opus-4.7"},
		{id: "claude-opus-4.6", wantID: "claude-opus-4.6"},
		{id: "grok-4.5", wantID: "grok-4.5"},
		{id: "nonexistent", wantNil: true},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			m := ByID(tt.id)
			if tt.wantNil {
				if m != nil {
					t.Errorf("ByID(%q) = %v, want nil", tt.id, m)
				}
				return
			}
			if m == nil {
				t.Fatalf("ByID(%q) = nil, want non-nil", tt.id)
			}
			if m.ID != tt.wantID {
				t.Errorf("ByID(%q).ID = %q, want %q", tt.id, m.ID, tt.wantID)
			}
		})
	}
}

func TestDefault(t *testing.T) {
	if d := Default(); d.ID != "claude-opus-4.8" {
		t.Errorf("Default().ID = %q, want %q", d.ID, "claude-opus-4.8")
	}
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

func TestGetAvailableModelsOrderStable(t *testing.T) {
	mgr, err := NewManager(&Config{Models: []Built{predictableBuilt()}})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	a := mgr.GetAvailableModels()
	b := mgr.GetAvailableModels()
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("unstable lengths %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("index %d differs: %q vs %q", i, a[i], b[i])
		}
	}
}

func TestLoggingService(t *testing.T) {
	mockService := &mockLLMService{}
	logger := slog.Default()
	loggingSvc := &loggingService{service: mockService, logger: logger, modelID: "test-model", provider: ProviderBuiltIn}

	response, err := loggingSvc.Do(context.Background(), &llm.Request{Messages: []llm.Message{llm.UserStringMessage("Hello")}})
	if err != nil || response == nil {
		t.Fatalf("Do: response=%v err=%v", response, err)
	}
	if loggingSvc.TokenContextWindow() != mockService.TokenContextWindow() {
		t.Errorf("TokenContextWindow mismatch")
	}
	if loggingSvc.MaxImageDimension() != mockService.MaxImageDimension() {
		t.Errorf("MaxImageDimension mismatch")
	}
}

// mockLLMService implements llm.Service for testing.
type mockLLMService struct {
	tokenContextWindow int
	maxImageDimension  int
	useSimplifiedPatch bool
}

func (m *mockLLMService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	return &llm.Response{
		Content: llm.TextContent("Hello, world!"),
		Usage:   llm.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.001},
	}, nil
}

func (m *mockLLMService) Provider() string { return "" }

func (m *mockLLMService) TokenContextWindow() int {
	if m.tokenContextWindow == 0 {
		return 4096
	}
	return m.tokenContextWindow
}

func (m *mockLLMService) MaxImageDimension() int {
	if m.maxImageDimension == 0 {
		return 2048
	}
	return m.maxImageDimension
}

func (m *mockLLMService) MaxImageBytes() int       { return 5 * 1024 * 1024 }
func (m *mockLLMService) UseSimplifiedPatch() bool { return m.useSimplifiedPatch }

type contextCapturingService struct {
	mockLLMService
	userAgent string
}

func (s *contextCapturingService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	s.userAgent = llmhttp.UserAgentFromContext(ctx)
	return s.mockLLMService.Do(ctx, request)
}

func TestManagerAppliesUserAgentWithoutLogger(t *testing.T) {
	inner := &contextCapturingService{}
	mgr, err := NewManager(&Config{Models: []Built{{
		ID:       "custom-ua",
		Provider: ProviderOpenAI,
		Source:   "test",
		Service:  inner,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	mgr.services["custom-ua"] = serviceEntry{
		service: inner, provider: ProviderOpenAI, modelID: "custom-ua", userAgent: "codex_cli_rs/0.144.0",
	}
	svc, err := mgr.GetService("custom-ua")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Do(context.Background(), &llm.Request{}); err != nil {
		t.Fatal(err)
	}
	if inner.userAgent != "codex_cli_rs/0.144.0" {
		t.Fatalf("User-Agent override = %q", inner.userAgent)
	}
}

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

func TestModelBuildSignature(t *testing.T) {
	// Each catalog model's Build must produce a non-nil llm.Service when
	// given any URL/key and an http.Client.
	customClient := &http.Client{}
	for _, m := range All() {
		svc := m.Build("https://example.test/v1", "key", customClient)
		if svc == nil {
			t.Errorf("Build(%s) returned nil", m.ID)
		}
	}
}

func TestUseSimplifiedPatch(t *testing.T) {
	logger := slog.Default()
	plain := &loggingService{service: &mockLLMService{}, logger: logger, modelID: "t1", provider: ProviderBuiltIn}
	if plain.UseSimplifiedPatch() {
		t.Error("plain mock should not implement SimplifiedPatcher")
	}
	with := &loggingService{service: &mockSimplifiedLLMService{useSimplified: true}, logger: logger, modelID: "t2", provider: ProviderBuiltIn}
	if !with.UseSimplifiedPatch() {
		t.Error("simplified mock should return true")
	}
}

type mockSimplifiedLLMService struct {
	mockLLMService
	useSimplified bool
}

func (m *mockSimplifiedLLMService) UseSimplifiedPatch() bool { return m.useSimplified }

func TestRefreshCustomModelsConcurrent(t *testing.T) {
	testDB, err := db.New(db.Config{DSN: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}
	defer testDB.Close()
	if err := testDB.Migrate(context.Background()); err != nil {
		t.Fatalf("failed to migrate test db: %v", err)
	}
	if _, err := testDB.CreateModel(context.Background(), generated.CreateModelParams{
		ModelID:      "custom-test-model",
		DisplayName:  "Test Model",
		ProviderType: "openai",
		Endpoint:     "https://api.example.com/v1",
		ApiKey:       "test-key",
		ModelName:    "test-model",
		MaxTokens:    4096,
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

func TestRefreshBuiltModelsReplacesBuiltModelsAndPreservesCustomModels(t *testing.T) {
	testDB, err := db.New(db.Config{DSN: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}
	defer testDB.Close()
	if err := testDB.Migrate(context.Background()); err != nil {
		t.Fatalf("failed to migrate test db: %v", err)
	}
	if _, err := testDB.CreateModel(context.Background(), generated.CreateModelParams{
		ModelID:      "custom-test-model",
		DisplayName:  "Test Model",
		ProviderType: "openai",
		Endpoint:     "https://api.example.com/v1",
		ApiKey:       "test-key",
		ModelName:    "test-model",
		MaxTokens:    4096,
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

func TestPreferredToolModelsAreRegistered(t *testing.T) {
	known := map[string]bool{}
	for _, m := range All() {
		known[m.ID] = true
	}
	for _, id := range claudetool.PreferredToolModels {
		if !known[id] {
			t.Errorf("PreferredToolModels contains %q which is not registered in models.All()", id)
		}
	}
}

func (m *mockLLMService) SupportsImages() bool { return true }

func TestReasoningServiceMapping(t *testing.T) {
	inner := &captureThinkingService{}
	svc := WrapReasoningConfig(inner, "", "unknown", "yes", `{"off":"off","minimal":"low","medium":"high"}`)

	levels := llm.SupportedReasoningLevels(svc)
	if got := []string{levels[0].Name(), levels[1].Name(), levels[2].Name()}; !reflect.DeepEqual(got, []string{"off", "minimal", "medium"}) {
		t.Fatalf("levels = %v", got)
	}
	if _, err := svc.Do(context.Background(), &llm.Request{ThinkingLevel: llm.ThinkingLevelMinimal}); err != nil {
		t.Fatal(err)
	}
	if inner.got != llm.ThinkingLevelLow {
		t.Fatalf("mapped level = %s, want low", inner.got.Name())
	}
}

func TestReasoningServiceDisabled(t *testing.T) {
	inner := &captureThinkingService{}
	svc := WrapReasoningConfig(inner, "", "unknown", "no", "")
	if llm.SupportsReasoning(svc) {
		t.Fatal("disabled service reports reasoning support")
	}
	if _, err := svc.Do(context.Background(), &llm.Request{ThinkingLevel: llm.ThinkingLevelHigh}); err != nil {
		t.Fatal(err)
	}
	if inner.got != llm.ThinkingLevelOff {
		t.Fatalf("level = %s, want off", inner.got.Name())
	}
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
	_, err := svc.Do(context.Background(), &llm.Request{ThinkingLevel: llm.ThinkingLevelHigh})
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
	if _, err := svc.Do(context.Background(), &llm.Request{}); err != nil {
		t.Fatal(err)
	}
	if inner.got != llm.ThinkingLevelLow {
		t.Fatalf("level = %s, want low", inner.got.Name())
	}
}

type defaultThinkingService struct{ captureThinkingService }

func (s *defaultThinkingService) DefaultReasoningLevel() string { return "medium" }
