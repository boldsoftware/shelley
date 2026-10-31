package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/models"
	"shelley.exe.dev/modelsources"
	"shelley.exe.dev/subpub"
)

type modelListReasoningService struct{ llm.Service }

func (s *modelListReasoningService) SupportsReasoning() bool { return true }
func (s *modelListReasoningService) SupportedReasoningLevels() []llm.ThinkingLevel {
	return []llm.ThinkingLevel{llm.ThinkingLevelOff, llm.ThinkingLevelHigh, llm.ThinkingLevelMax}
}

func TestHandleModelsCarriesIntegrationMode(t *testing.T) {
	var catalog struct {
		Models []modelsources.IntegrationModel `json:"models"`
	}
	if err := json.Unmarshal([]byte(`{
		"models": [
			{"id":"openai/subscription","native_id":"gpt-5.6-luna","provider":"openai","apis":["openai_chat"],"exe_dev":{"mode":"chatgpt"}},
			{"id":"openai/managed","provider":"openai","apis":["openai_chat"],"exe_dev":{"mode":"managed"}},
			{"id":"openai/byok","provider":"openai","apis":["openai_chat"],"exe_dev":{"mode":"byok"}},
			{"id":"openai/future-mode","provider":"openai","apis":["openai_chat"],"exe_dev":{"mode":"future"}},
			{"id":"openai/chatgpt-name-only","provider":"openai","apis":["openai_chat"]}
		]
	}`), &catalog); err != nil {
		t.Fatal(err)
	}
	integration := &modelsources.LLMIntegrationConfig{
		Name:   "llm",
		Host:   "llm.int.exe.xyz",
		URL:    "https://llm.int.exe.xyz",
		Models: catalog.Models,
	}
	requests := 0
	httpc := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected LLM request to " + req.URL.String())
	})}
	built := modelsources.Build(nil, []modelsources.Source{modelsources.LLMIntegration(integration, "")}, httpc, nil)
	mgr, err := models.NewManager(&models.Config{Models: built})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if info := mgr.GetModelInfo("subscription"); info == nil || info.Mode != "chatgpt" {
		t.Fatalf("manager subscription info = %+v, want mode chatgpt", info)
	}
	s := &Server{db: testDB(t), llmManager: mgr, logger: slog.Default()}
	rec := httptest.NewRecorder()
	s.handleModels(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	if requests != 0 {
		t.Fatalf("made %d unexpected LLM requests", requests)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var response []map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]json.RawMessage{}
	for _, model := range response {
		var id string
		if err := json.Unmarshal(model["id"], &id); err != nil {
			t.Fatal(err)
		}
		got[id] = model
	}
	if len(got) != 5 {
		t.Fatalf("models = %v, want five catalog models", got)
	}
	if name := string(got["subscription"]["api_model_name"]); name != `"gpt-5.6-luna"` {
		t.Errorf("subscription API model name = %s, want native name gpt-5.6-luna", name)
	}
	if base := string(got["subscription"]["base_url"]); base != `"https://llm.int.exe.xyz"` {
		t.Errorf("subscription base URL = %s, want integration URL", base)
	}
	for id, want := range map[string]string{
		"subscription": "chatgpt",
		"managed":      "managed",
		"byok":         "byok",
		"future-mode":  "future",
	} {
		model, ok := got[id]
		if !ok {
			t.Fatalf("model %q missing from response", id)
		}
		var mode string
		if err := json.Unmarshal(model["mode"], &mode); err != nil {
			t.Errorf("%s mode: %v", id, err)
		} else if mode != want {
			t.Errorf("%s mode = %q, want %q", id, mode, want)
		}
	}
	model, ok := got["chatgpt-name-only"]
	if !ok {
		t.Fatal("model \"chatgpt-name-only\" missing from response")
	}
	if mode, ok := model["mode"]; ok {
		t.Errorf("chatgpt-name-only response = %s, want mode omitted", mode)
	}
}

func TestHandleModelsIncludesReasoningLevels(t *testing.T) {
	mgr, err := models.NewManager(&models.Config{Models: []models.Built{{
		ID:       "reasoning-model",
		Provider: models.ProviderBuiltIn,
		Service:  &modelListReasoningService{Service: predictable.NewService()},
	}}})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	s := &Server{db: testDB(t), llmManager: mgr, logger: slog.Default()}
	rec := httptest.NewRecorder()
	s.handleModels(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))

	var got []ModelInfo
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].SupportsReasoning {
		t.Fatalf("models = %+v", got)
	}
	want := []string{"off", "high", "max"}
	if len(got[0].ReasoningLevels) != len(want) {
		t.Fatalf("reasoning_levels = %v, want %v", got[0].ReasoningLevels, want)
	}
	for i := range want {
		if got[0].ReasoningLevels[i] != want[i] {
			t.Fatalf("reasoning_levels = %v, want %v", got[0].ReasoningLevels, want)
		}
	}
}

func TestHandleModelsReportsMaxContextTokens(t *testing.T) {
	mgr, err := models.NewManager(&models.Config{Models: []models.Built{
		{ID: "sol", Provider: models.ProviderOpenAI, APIModelName: "gpt-5.6-sol", BaseURL: "https://api.openai.com", Service: predictable.NewService()},
		{ID: "opus", Provider: models.ProviderAnthropic, APIModelName: "claude-opus-5", BaseURL: "https://llm.int.exe.xyz", Service: predictable.NewService()},
		{ID: "mystery", Provider: models.ProviderBuiltIn, APIModelName: "not-in-models-dev", Service: predictable.NewService()},
	}})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	s := &Server{db: testDB(t), llmManager: mgr, logger: slog.Default()}
	rec := httptest.NewRecorder()
	s.handleModels(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))

	var got []ModelInfo
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"sol": 272000, "opus": 1000000, "mystery": 0}
	for _, m := range got {
		if m.MaxContextTokens != want[m.ID] {
			t.Errorf("%s max_context_tokens = %d, want %d", m.ID, m.MaxContextTokens, want[m.ID])
		}
	}
}

func TestHandleModelRefreshReturnsRefreshedModels(t *testing.T) {
	mgr, err := models.NewManager(&models.Config{
		Models: []models.Built{
			{
				ID:       "old-built",
				Provider: models.ProviderBuiltIn,
				Source:   "old source",
				Service:  predictable.NewService(),
			},
		},
		Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	s := &Server{
		db:         testDB(t),
		llmManager: mgr,
		logger:     slog.Default(),
		streamPub:  subpub.New[StreamResponse](),
		refreshBuiltModels: func(context.Context) ([]models.Built, error) {
			return []models.Built{
				{
					ID:       "new-built",
					Provider: models.ProviderBuiltIn,
					Source:   "new source",
					Service:  predictable.NewService(),
				},
				{
					ID:       models.Default().ID,
					Provider: models.ProviderAnthropic,
					Source:   "new source",
					Service:  predictable.NewService(),
				},
			}, nil
		},
	}

	ctx, cancel := context.WithCancel(t.Context())
	next := s.streamPub.Subscribe(ctx, -1)
	req := httptest.NewRequest(http.MethodPost, "/api/models/refresh", nil)
	rec := httptest.NewRecorder()
	s.handleModelRefresh(rec, req)
	cancel()

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var got []ModelInfo
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 2 || got[0].ID != "new-built" || got[0].Source != "new source" {
		t.Fatalf("models = %+v, want new-built first", got)
	}
	if got[0].IsDefault || !got[1].IsDefault {
		t.Fatalf("models = %+v, want the ready tier-1 refreshed model marked default", got)
	}
	if mgr.HasModel("old-built") {
		t.Fatal("old built model was not removed")
	}
	if event, ok := next(); !ok || !event.ModelsChanged {
		t.Fatalf("catalog event = %+v, ok=%v", event, ok)
	}
	if event, ok := next(); ok {
		t.Fatalf("unexpected extra event: %+v", event)
	}
}

func TestHandleModelsAssignsTiers(t *testing.T) {
	mgr, err := models.NewManager(&models.Config{
		Models: []models.Built{
			{ID: "claude-opus-4.8", Provider: models.ProviderAnthropic, Service: predictable.NewService()},
			{ID: "claude-opus-4.7", Provider: models.ProviderAnthropic, Service: predictable.NewService()},
			{ID: "gpt-6-astra", Provider: models.ProviderOpenAI, Service: predictable.NewService()},
			{ID: "gpt-6-sol", Provider: models.ProviderOpenAI, Service: predictable.NewService()},
			{ID: "gpt-6-luna", Provider: models.ProviderOpenAI, Service: predictable.NewService()},
			{ID: "gpt-5.6-sol", Provider: models.ProviderOpenAI, Service: predictable.NewService()},
			{ID: "gpt-5.6-luna", Provider: models.ProviderOpenAI, Service: predictable.NewService()},
		},
		Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	s := &Server{db: testDB(t), llmManager: mgr, logger: slog.Default()}

	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	rec := httptest.NewRecorder()
	s.handleModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var got []ModelInfo
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	tiers := map[string]int{}
	for _, m := range got {
		tiers[m.ID] = m.Tier
	}
	if tiers["claude-opus-4.8"] != models.Tier1 {
		t.Errorf("opus-4.8 tier = %d, want %d", tiers["claude-opus-4.8"], models.Tier1)
	}
	if tiers["claude-opus-4.7"] != models.Tier2 {
		t.Errorf("opus-4.7 tier = %d, want %d", tiers["claude-opus-4.7"], models.Tier2)
	}
	for _, id := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		if tiers[id] != models.Tier1 {
			t.Errorf("%s tier = %d, want %d", id, tiers[id], models.Tier1)
		}
	}
	if tiers["gpt-5.6-sol"] != models.Tier2 {
		t.Errorf("gpt-5.6-sol tier = %d, want %d", tiers["gpt-5.6-sol"], models.Tier2)
	}
	if tiers["gpt-5.6-luna"] != models.Tier1 {
		t.Errorf("gpt-5.6-luna tier = %d, want %d while subscription access lags", tiers["gpt-5.6-luna"], models.Tier1)
	}
}

func TestAssignModelTiersKeepsCustomModelsProminent(t *testing.T) {
	modelList := []ModelInfo{
		{ID: "gpt-5.6-sol", Source: "llm.int.exe.xyz", Ready: true},
		{ID: "upstream-only", Source: "llm.int.exe.xyz", Ready: true},
		{ID: "my-custom-model", Source: models.SourceCustomLabel, Ready: true},
	}

	assignModelTiers(modelList)

	want := map[string]int{
		"gpt-5.6-sol":     models.Tier1,
		"upstream-only":   models.Tier2,
		"my-custom-model": models.Tier1,
	}
	for _, model := range modelList {
		if model.Tier != want[model.ID] {
			t.Errorf("%s tier = %d, want %d", model.ID, model.Tier, want[model.ID])
		}
	}
}

func testDB(t *testing.T) *db.DB {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	return database
}

func TestModelUpgradeDismissedPerPair(t *testing.T) {
	mgr, err := models.NewManager(&models.Config{
		Models: []models.Built{
			{ID: "claude-opus-4.7", Provider: models.ProviderAnthropic, Service: predictable.NewService()},
			{ID: "claude-opus-4.8", Provider: models.ProviderAnthropic, Service: predictable.NewService()},
			{ID: "claude-opus-5.5", Provider: models.ProviderAnthropic, Service: predictable.NewService()},
		},
		Logger: slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{db: testDB(t), llmManager: mgr, logger: slog.Default()}
	if err := s.db.SetSetting(context.Background(), upgradeDismissedKeyPrefix+"claude-opus-5.5", "1"); err != nil {
		t.Fatal(err)
	}
	upgrades := func() map[string]string {
		rec := httptest.NewRecorder()
		s.handleModels(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
		var got []ModelInfo
		if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, m := range got {
			if m.Upgrade != nil {
				out[m.ID] = m.Upgrade.To
			}
		}
		return out
	}
	if got := upgrades(); got["claude-opus-4.7"] != "claude-opus-5.5" || got["claude-opus-4.8"] != "claude-opus-5.5" {
		t.Fatalf("before dismiss: %v", got)
	}
	rec := httptest.NewRecorder()
	s.handleDismissModelUpgrade(rec, httptest.NewRequest(http.MethodPost, "/api/model-upgrade/dismiss", strings.NewReader(`{"from":"claude-opus-4.7","to":"claude-opus-5.5"}`)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("dismiss status = %d", rec.Code)
	}
	if got := upgrades(); got["claude-opus-4.7"] != "" || got["claude-opus-4.8"] != "claude-opus-5.5" {
		t.Fatalf("after dismiss: %v", got)
	}
	rec = httptest.NewRecorder()
	s.handleDismissModelUpgrade(rec, httptest.NewRequest(http.MethodPost, "/api/model-upgrade/dismiss", strings.NewReader(`{"from":"claude-opus-4.7","to":"nope"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown target status = %d", rec.Code)
	}
}

func TestUpgradeAllMovesOtherConversations(t *testing.T) {
	mgr, err := models.NewManager(&models.Config{
		Models: []models.Built{
			{ID: "claude-opus-4.7", Provider: models.ProviderAnthropic, Service: predictable.NewService()},
			{ID: "claude-opus-5.5", Provider: models.ProviderAnthropic, Service: predictable.NewService()},
			{ID: "claude-opus-4.8", Provider: models.ProviderAnthropic, Service: predictable.NewService()},
		},
		Logger: slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s, _, _ := newTestServer(t)
	s.llmManager = mgr
	s.predictableOnly = false
	ctx := context.Background()
	old := "claude-opus-4.7"
	newest := "claude-opus-5.5"
	slug := "behind"
	behind, err := s.db.CreateConversation(ctx, &slug, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	slug2 := "atop"
	atop, err := s.db.CreateConversation(ctx, &slug2, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	slug3 := "elsewhere"
	other, err := s.db.CreateConversation(ctx, &slug3, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// "elsewhere" has the same upgrade but a different model, so it stays.
	for id, model := range map[string]string{behind.ConversationID: old, atop.ConversationID: newest, other.ConversationID: "claude-opus-4.8"} {
		rec := httptest.NewRecorder()
		s.handleConversationSettings(rec, httptest.NewRequest(http.MethodPost, "/api/conversation/"+id+"/settings", strings.NewReader(`{"model":"`+model+`"}`)), id)
		if rec.Code != http.StatusOK {
			t.Fatalf("seed %s = %d, body %q", id, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	s.handleUpgradeAllModels(rec, httptest.NewRequest(http.MethodPost, "/api/model-upgrade/apply-all", strings.NewReader(`{"from":"claude-opus-4.7","to":"claude-opus-4.8"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a target that is not the offered upgrade = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.handleUpgradeAllModels(rec, httptest.NewRequest(http.MethodPost, "/api/model-upgrade/apply-all", strings.NewReader(`{"from":"claude-opus-4.7","to":"claude-opus-5.5"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "{\"skipped\":0,\"upgraded\":1}\n" {
		t.Fatalf("body = %q", got)
	}
	moved, err := s.db.GetConversationByID(ctx, behind.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if derefString(moved.Model) != newest {
		t.Fatalf("behind model = %q", derefString(moved.Model))
	}
	untouched, err := s.db.GetConversationByID(ctx, other.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if derefString(untouched.Model) != "claude-opus-4.8" {
		t.Fatalf("other model = %q", derefString(untouched.Model))
	}
	if done, err := s.db.GetSetting(ctx, upgradePromptKey); err != nil || done == "" {
		t.Fatalf("prompt key = %q, %v", done, err)
	}
}
