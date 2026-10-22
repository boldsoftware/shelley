package modelsources

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/models"
)

func TestMain(m *testing.M) {
	env, err := exeenv.New("https", "exe.xyz")
	if err != nil {
		panic(err)
	}
	exeenv.Configure(env) // tests use fake integration clients, never live VM metadata
	os.Exit(m.Run())
}

func testProdExeEnv(t *testing.T) exeenv.Environment {
	t.Helper()
	env, err := exeenv.New("https", "exe.xyz")
	if err != nil {
		t.Fatal(err)
	}
	return env
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func findBuilt(bs []models.Built, id string) *models.Built {
	for i := range bs {
		if bs[i].ID == id {
			return &bs[i]
		}
	}
	return nil
}

func TestPredictableBuilds(t *testing.T) {
	bs := Build(models.All(), []Source{Predictable()}, &http.Client{}, nil)
	if b := findBuilt(bs, "predictable"); b == nil {
		t.Fatalf("predictable not built; got %v", bs)
	}
}

func TestBuildConnectionRevisions(t *testing.T) {
	catalog := []models.Model{*models.ByID("claude-opus-4.7")}
	for _, source := range []Source{
		Env("old", "", "", ""),
		LLMIntegration(&LLMIntegrationConfig{
			Name: "llm", Host: "fixture.invalid", URL: "https://fixture.invalid",
			Models: []IntegrationModel{{
				ID: "anthropic/claude-opus-4-7", Provider: "anthropic",
				NativeID: "claude-opus-4-7", APIs: []string{"anthropic_messages"},
			}},
		}, ""),
	} {
		build := func() models.Built {
			t.Helper()
			built := Build(catalog, []Source{source}, &http.Client{}, nil)
			if len(built) != 1 {
				t.Fatalf("built %d models, want 1", len(built))
			}
			return built[0]
		}
		first := build()
		source.label = "renamed"
		next := build()
		if first.Service == next.Service || first.ServiceRevision == 0 || first.ServiceRevision != next.ServiceRevision {
			t.Fatal("unchanged connection did not retain its revision across service rebuild")
		}
		if source.integration != nil {
			source.integration.URL = "https://other.invalid"
		} else {
			source.providers[models.ProviderAnthropic].apiKey = "rotated"
		}
		if build().ServiceRevision == first.ServiceRevision {
			t.Fatal("changed connection retained its old revision")
		}
	}
}

func TestEnvSourceBuildsAllProviders(t *testing.T) {
	src := Env("a", "o", "g", "f")
	bs := Build(models.All(), []Source{src}, &http.Client{}, nil)
	// Order must match catalog order.
	var expected []string
	for _, m := range models.All() {
		// Env source covers Anthropic/OpenAI/Gemini/Fireworks only.
		switch m.Provider {
		case models.ProviderAnthropic, models.ProviderOpenAI, models.ProviderGemini, models.ProviderFireworks:
			expected = append(expected, m.ID)
		}
	}
	if len(bs) != len(expected) {
		t.Fatalf("built count %d != expected %d (got %v)", len(bs), len(expected), bs)
	}
	for i := range bs {
		if bs[i].ID != expected[i] {
			t.Errorf("index %d: got %q want %q", i, bs[i].ID, expected[i])
		}
	}
}

func TestEnvSourceLabels(t *testing.T) {
	bs := Build(models.All(), []Source{Env("a", "o", "g", "f")}, &http.Client{}, nil)
	for _, tt := range []struct {
		id, want string
	}{
		{"claude-opus-5.5", "$ANTHROPIC_API_KEY"},
		{"gpt-5.5", "$OPENAI_API_KEY"},
		{"gemini-3.8-flash", "$GEMINI_API_KEY"},
		{"kimi-k3-fireworks", "$FIREWORKS_API_KEY"},
		// Also pins that the embedded models.dev snapshot knows this model: the
		// loop below requires a non-empty release date, which the UI sorts by.
		{"deepseek-v4.1-flash-fireworks", "$FIREWORKS_API_KEY"},
	} {
		b := findBuilt(bs, tt.id)
		if b == nil {
			t.Errorf("missing %q", tt.id)
			continue
		}
		if b.ReleaseDate == "" {
			t.Errorf("model %q has no models.dev release date", tt.id)
		}
		if b.Source != tt.want {
			t.Errorf("%s source = %q, want %q", tt.id, b.Source, tt.want)
		}
	}
}

func TestGatewaySourceLabels(t *testing.T) {
	// Plain gateway.
	bs := Build(models.All(), []Source{Gateway("https://gw.example.com", "", "", "")}, &http.Client{}, nil)
	if b := findBuilt(bs, "claude-opus-5.5"); b == nil || b.Source != "exe.dev gateway" {
		t.Errorf("claude-opus-5.5 with plain gateway: %+v", b)
	}
	if b := findBuilt(bs, "gemini-3.8-flash"); b != nil {
		t.Errorf("gemini-3.8-flash should not be built by gateway, got %+v", b)
	}
	if b := findBuilt(bs, "grok-4.5"); b == nil || b.Source != "exe.dev gateway" {
		t.Errorf("grok-4.5 with plain gateway: %+v", b)
	}

	// Gateway with explicit anthropic key: provider label switches.
	bs = Build(models.All(), []Source{Gateway("https://gw.example.com", "real-key", "", "")}, &http.Client{}, nil)
	if b := findBuilt(bs, "claude-opus-5.5"); b == nil || b.Source != "$ANTHROPIC_API_KEY" {
		t.Errorf("claude-opus-5.5 with explicit anthropic key: %+v", b)
	}
	if b := findBuilt(bs, "gpt-5.5"); b == nil || b.Source != "exe.dev gateway" {
		t.Errorf("gpt-5.5 should still be gateway: %+v", b)
	}
}

func TestAnthropicThinkingBindingSources(t *testing.T) {
	gateway := Build(models.All(), []Source{Gateway("https://gw.example.com", "", "", "")}, &http.Client{}, nil)
	gatewayModel := findBuilt(gateway, "claude-opus-5.5")
	if gatewayModel == nil {
		t.Fatal("gateway did not build claude-opus-5.5")
	}
	gatewayService, ok := gatewayModel.Service.(*ant.Service)
	if !ok || !gatewayService.EnableThinkingBinding {
		t.Fatalf("gateway Anthropic service = %#v, want explicit binding support", gatewayService)
	}

	env := Build(models.All(), []Source{Env("key", "", "", "")}, &http.Client{}, nil)
	envModel := findBuilt(env, "claude-opus-5.5")
	if envModel == nil {
		t.Fatal("env source did not build claude-opus-5.5")
	}
	envService, ok := envModel.Service.(*ant.Service)
	if !ok || !envService.EnableThinkingBinding {
		t.Fatalf("native Anthropic service = %#v, want binding support", envService)
	}

	integration := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{{
			ID: "anthropic/claude-opus-5-5", Provider: "anthropic",
			NativeID: "claude-opus-5-5", APIs: []string{"anthropic_messages"},
		}},
	}
	built := Build(models.All(), []Source{LLMIntegration(integration, "")}, &http.Client{}, nil)
	integrationModel := findBuilt(built, "claude-opus-5.5")
	if integrationModel == nil {
		t.Fatal("catalog integration did not build claude-opus-5.5")
	}
	integrationService, ok := integrationModel.Service.(*ant.Service)
	if !ok || !integrationService.EnableThinkingBinding {
		t.Fatalf("catalog integration service = %#v, want explicit binding support", integrationService)
	}

	proxy := &LLMIntegrationConfig{
		Name: "proxy", Host: "proxy.int.exe.xyz", URL: "https://proxy.int.exe.xyz",
		Models: []IntegrationModel{{
			ID: "fireworks/claude-opus-5-5", Provider: "fireworks",
			NativeID: "claude-opus-5-5", APIs: []string{"anthropic_messages"},
		}},
	}
	built = Build(models.All(), []Source{LLMIntegration(proxy, "")}, &http.Client{}, nil)
	proxyModel := findBuilt(built, "claude-opus-5-5")
	if proxyModel == nil {
		t.Fatal("third-party integration did not build claude-opus-5-5")
	}
	proxyService, ok := proxyModel.Service.(*ant.Service)
	if !ok || proxyService.EnableThinkingBinding {
		t.Fatalf("third-party Anthropic-compatible service = %#v, want binding disabled", proxyService)
	}
}

func TestLLMIntegrationSourceLabelsAndFiltering(t *testing.T) {
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "openai/gpt-6-astra", Provider: "openai", NativeID: "gpt-6-astra", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "openai/gpt-6.1-sol", Provider: "openai", NativeID: "gpt-6.1-sol", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "openai/gpt-6-sol", Provider: "openai", NativeID: "gpt-6-sol", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "openai/gpt-6-luna", Provider: "openai", NativeID: "gpt-6-luna", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "anthropic/claude-opus-4-8", Provider: "anthropic", NativeID: "claude-opus-4-8", APIs: []string{"anthropic_messages"}},
			{ID: "anthropic/claude-opus-4-7", Provider: "anthropic", NativeID: "claude-opus-4-7", APIs: []string{"anthropic_messages"}},
			{ID: "anthropic/claude-sonnet-4-6", Provider: "anthropic", NativeID: "claude-sonnet-4-6", APIs: []string{"anthropic_messages"}},
			{ID: "openai/gpt-5.6-sol", Provider: "openai", NativeID: "gpt-5.6-sol", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "openai/gpt-5.6-terra", Provider: "openai", NativeID: "gpt-5.6-terra", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "openai/gpt-5.6-luna", Provider: "openai", NativeID: "gpt-5.6-luna", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "openai/gpt-5.5", Provider: "openai", NativeID: "gpt-5.5", APIs: []string{"openai_responses"}},
			{ID: "fireworks/glm-5p2", Provider: "fireworks", NativeID: "accounts/fireworks/models/glm-5p2", APIs: []string{"openai_chat"}},
			{ID: "fireworks/glm-5p3", Provider: "fireworks", NativeID: "accounts/fireworks/models/glm-5p3", APIs: []string{"openai_chat"}},
			{ID: "fireworks/glm-5p3-flash", Provider: "fireworks", NativeID: "accounts/fireworks/models/glm-5p3-flash", APIs: []string{"openai_chat"}},
			{ID: "fireworks/kimi-k3", Provider: "fireworks", NativeID: "accounts/fireworks/models/kimi-k3", APIs: []string{"openai_chat"}},
			{ID: "fireworks/deepseek-v4p1-flash", Provider: "fireworks", NativeID: "accounts/fireworks/models/deepseek-v4p1-flash", APIs: []string{"openai_chat"}},
		},
	}
	bs := Build(models.All(), []Source{LLMIntegration(integ, ""), Predictable()}, &http.Client{}, nil)
	wantLabel := "llm.int.exe.xyz"
	for _, id := range []string{
		"gpt-6-astra",
		"gpt-6.1-sol",
		"gpt-6-sol",
		"gpt-6-luna",
		"claude-opus-4.8",
		"claude-opus-4.7",
		"claude-sonnet-4.6",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5.5",
		"glm-5.2-fireworks",
		"glm-5.3-fireworks",
		"glm-5.3-flash-fireworks",
		"kimi-k3-fireworks",
		// The gateway advertises this as fireworks/deepseek-v4p1-flash; it must
		// resolve to the catalog entry (reasoning, images, Shelley's ID) rather
		// than being materialized as a bare unknown model.
		"deepseek-v4.1-flash-fireworks",
	} {
		b := findBuilt(bs, id)
		if b == nil {
			t.Errorf("%q should be built", id)
			continue
		}
		if b.Source != wantLabel {
			t.Errorf("%s source = %q, want %q", id, b.Source, wantLabel)
		}
	}
	for id, want := range map[string]oai.Model{
		"gpt-6-astra": oai.GPT6Astra,
		"gpt-6.1-sol": oai.GPT61Sol,
		"gpt-6-sol":   oai.GPT6Sol,
		"gpt-6-luna":  oai.GPT6Luna,
	} {
		built := findBuilt(bs, id)
		if built == nil {
			t.Errorf("%s should be built", id)
			continue
		}
		if built.APIType != models.APITypeOpenAIResponses {
			t.Errorf("%s APIType = %q, want %q", id, built.APIType, models.APITypeOpenAIResponses)
		}
		svc, ok := built.Service.(*oai.ResponsesService)
		if !ok {
			t.Errorf("%s service = %T, want *oai.ResponsesService", id, built.Service)
			continue
		}
		if svc.Model != want {
			t.Errorf("%s model = %+v, want built-in model %+v", id, svc.Model, want)
		}
	}
	for _, id := range []string{
		"anthropic/claude-opus-4-7",
		"openai/gpt-5.5",
		"claude-opus-4-7",
		"glm-5p2",
		"glm-5p3",
		"glm-5p3-flash",
		"kimi-k3",
		"deepseek-v4p1-flash",
		"gemini-3.8-flash",
	} {
		if b := findBuilt(bs, id); b != nil {
			t.Errorf("%q should NOT be built, got %+v", id, b)
		}
	}
	if findBuilt(bs, "predictable") == nil {
		t.Errorf("predictable should survive integration filter")
	}
}

func TestLLMIntegrationEnrichesCompatibleCatalogModel(t *testing.T) {
	const integrationURL = "https://llm.int.exe.xyz"
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: integrationURL,
		Models: []IntegrationModel{
			{ID: "openai/sol-upstream-id", Provider: "openai", NativeID: "gpt-5.6-sol", APIs: []string{"openai_responses"}},
		},
	}

	got := Build(models.All(), []Source{LLMIntegration(integ, "")}, &http.Client{}, nil)
	if len(got) != 1 {
		t.Fatalf("built models = %+v, want one", got)
	}
	built := got[0]
	if built.ID != "gpt-5.6-sol" {
		t.Errorf("ID = %q, want stable catalog ID", built.ID)
	}
	if built.BaseURL != integrationURL {
		t.Errorf("BaseURL = %q, want %q", built.BaseURL, integrationURL)
	}
	if built.APIType != models.APITypeOpenAIResponses {
		t.Errorf("APIType = %q, want %q", built.APIType, models.APITypeOpenAIResponses)
	}
	service, ok := built.Service.(*oai.ResponsesService)
	if !ok {
		t.Fatalf("service = %T, want *oai.ResponsesService", built.Service)
	}
	if service.Model.ModelName != "gpt-5.6-sol" {
		t.Errorf("wire model = %q, want native ID", service.Model.ModelName)
	}
	if service.ModelURL != integrationURL+"/v1" {
		t.Errorf("service URL = %q, want integration URL", service.ModelURL)
	}
	if service.APIKey != "implicit" {
		t.Errorf("API key = %q, want implicit", service.APIKey)
	}
	if !service.SupportsImages() {
		t.Error("known Sol should retain built-in image support")
	}
	if !service.Model.IsReasoningModel {
		t.Error("known Sol should retain built-in reasoning behavior")
	}
}

func TestLLMIntegrationEnrichesCatalogModelUsingIDFallback(t *testing.T) {
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "gpt-5.6-sol", Provider: "openai", APIs: []string{"openai_responses"}},
		},
	}

	got := Build(models.All(), []Source{LLMIntegration(integ, "")}, &http.Client{}, nil)
	if len(got) != 1 {
		t.Fatalf("built models = %+v, want one", got)
	}
	service, ok := got[0].Service.(*oai.ResponsesService)
	if !ok {
		t.Fatalf("service = %T, want *oai.ResponsesService", got[0].Service)
	}
	if !service.SupportsImages() || !service.Model.IsReasoningModel {
		t.Errorf("ID fallback did not retain Sol capabilities: %+v", service.Model)
	}
}

func TestLLMIntegrationUnknownModelUsesDynamicCapabilities(t *testing.T) {
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{
				ID:       "openai/upstream-only",
				Provider: "openai",
				NativeID: "native-upstream-only",
				APIs:     []string{"openai_responses"},
				Architecture: IntegrationModelArchitecture{
					InputModalities: []string{"text", "image"},
				},
			},
		},
	}

	got := Build(models.All(), []Source{LLMIntegration(integ, "")}, &http.Client{}, nil)
	if len(got) != 1 {
		t.Fatalf("built models = %+v, want one", got)
	}
	if got[0].ID != "upstream-only" {
		t.Errorf("ID = %q, want short upstream ID", got[0].ID)
	}
	service, ok := got[0].Service.(*oai.ResponsesService)
	if !ok {
		t.Fatalf("service = %T, want dynamic *oai.ResponsesService", got[0].Service)
	}
	if service.Model.ModelName != "native-upstream-only" {
		t.Errorf("wire model = %q, want native ID", service.Model.ModelName)
	}
	if !service.SupportsImages() {
		t.Error("unknown model should preserve upstream image modality")
	}
	if service.Model.IsReasoningModel {
		t.Error("unknown model should not inherit built-in reasoning behavior")
	}
}

func TestLLMIntegrationPrefersOpenAIAPIOverAnthropic(t *testing.T) {
	// Fireworks models advertise both OpenAI and Anthropic APIs; prefer the
	// more common OpenAI protocol over anthropic_messages.
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "fireworks/some-new-model", Provider: "fireworks", NativeID: "accounts/fireworks/models/some-new-model", APIs: []string{"openai_chat", "openai_responses", "anthropic_messages"}},
			{ID: "fireworks/chat-only", Provider: "fireworks", NativeID: "accounts/fireworks/models/chat-only", APIs: []string{"anthropic_messages", "openai_chat"}},
		},
	}

	got := Build(models.All(), []Source{LLMIntegration(integ, "")}, &http.Client{}, nil)
	if len(got) != 2 {
		t.Fatalf("built models = %+v, want two", got)
	}
	if got[0].APIType != models.APITypeOpenAIResponses {
		t.Errorf("APIType = %q, want %q", got[0].APIType, models.APITypeOpenAIResponses)
	}
	if _, ok := got[0].Service.(*oai.ResponsesService); !ok {
		t.Fatalf("service = %T, want *oai.ResponsesService", got[0].Service)
	}
	if got[1].APIType != models.APITypeOpenAIChat {
		t.Errorf("APIType = %q, want %q", got[1].APIType, models.APITypeOpenAIChat)
	}
	if _, ok := got[1].Service.(*oai.Service); !ok {
		t.Fatalf("service = %T, want *oai.Service", got[1].Service)
	}
}

func TestLLMIntegrationAPIMismatchUsesDynamicService(t *testing.T) {
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "openai/sol-chat", Provider: "openai", NativeID: "gpt-5.6-sol", APIs: []string{"openai_chat"}},
		},
	}

	got := Build(models.All(), []Source{LLMIntegration(integ, "")}, &http.Client{}, nil)
	if len(got) != 1 {
		t.Fatalf("built models = %+v, want one", got)
	}
	if got[0].APIType != models.APITypeOpenAIChat {
		t.Errorf("APIType = %q, want %q", got[0].APIType, models.APITypeOpenAIChat)
	}
	service, ok := got[0].Service.(*oai.Service)
	if !ok {
		t.Fatalf("service = %T, want dynamic *oai.Service", got[0].Service)
	}
	if service.Model.ModelName != "gpt-5.6-sol" {
		t.Errorf("wire model = %q, want native ID", service.Model.ModelName)
	}
	if service.SupportsImages() {
		t.Error("API mismatch should preserve upstream modality instead of using Sol catalog capabilities")
	}
}

func TestLLMIntegrationCustomProviderSourceNamesProvider(t *testing.T) {
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "openai/gpt-5.5", Provider: "openai", NativeID: "gpt-5.5", APIs: []string{"openai_responses"}},
			{ID: "hyper/qwen3.7-flash", Provider: "hyper", NativeID: "qwen3.7-flash", APIs: []string{"openai_responses"}},
		},
	}

	got := Build(models.All(), []Source{LLMIntegration(integ, "")}, &http.Client{}, nil)
	if built := findBuilt(got, "gpt-5.5"); built == nil || built.Source != "llm.int.exe.xyz" {
		t.Fatalf("built-in provider = %+v, want integration host source", built)
	}
	if built := findBuilt(got, "qwen3.7-flash"); built == nil || built.Source != "llm.int.exe.xyz (hyper)" {
		t.Fatalf("custom provider = %+v, want provider-qualified source", built)
	}
}

func TestLLMIntegrationProviderMismatchUsesDynamicService(t *testing.T) {
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "mistral/sol", Provider: "mistral", NativeID: "gpt-5.6-sol", APIs: []string{"openai_responses"}},
		},
	}

	got := Build(models.All(), []Source{LLMIntegration(integ, "")}, &http.Client{}, nil)
	if len(got) != 1 {
		t.Fatalf("built models = %+v, want one", got)
	}
	service, ok := got[0].Service.(*oai.ResponsesService)
	if !ok {
		t.Fatalf("service = %T, want dynamic *oai.ResponsesService", got[0].Service)
	}
	if service.SupportsImages() || service.Model.IsReasoningModel {
		t.Errorf("provider mismatch inherited OpenAI catalog capabilities: %+v", service.Model)
	}
}

func TestLLMIntegrationModelsJSONIsAuthoritative(t *testing.T) {
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "openai/upstream-only", Provider: "openai", NativeID: "native-upstream-only", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "anthropic/known-but-renamed", Provider: "anthropic", NativeID: "claude-opus-4-7", APIs: []string{"anthropic_messages"}},
			{ID: "mistral/upstream-chat", Provider: "mistral", NativeID: "native-upstream-chat", APIs: []string{"openai_chat"}},
		},
	}

	got := Build(models.All(), []Source{LLMIntegration(integ, "@llm2")}, &http.Client{}, nil)
	if len(got) != 3 {
		t.Fatalf("built models = %+v, want exactly the three integration models", got)
	}
	wantIDs := []string{"upstream-only@llm2", "claude-opus-4.7@llm2", "upstream-chat@llm2"}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Fatalf("built model %d ID = %q, want %q", i, got[i].ID, want)
		}
	}
	if got[0].APIType != models.APITypeOpenAIResponses {
		t.Errorf("first API type = %q, want %q", got[0].APIType, models.APITypeOpenAIResponses)
	}
	responses, ok := got[0].Service.(*oai.ResponsesService)
	if !ok {
		t.Fatalf("first service = %T, want *oai.ResponsesService", got[0].Service)
	}
	if responses.Model.ModelName != "native-upstream-only" {
		t.Errorf("first wire model = %q, want native ID", responses.Model.ModelName)
	}
	if got[1].APIType != models.APITypeAnthropicMessages {
		t.Errorf("second API type = %q, want %q", got[1].APIType, models.APITypeAnthropicMessages)
	}
	anthropic, ok := got[1].Service.(*ant.Service)
	if !ok {
		t.Fatalf("second service = %T, want *ant.Service", got[1].Service)
	}
	if anthropic.Model != "claude-opus-4-7" {
		t.Errorf("second wire model = %q, want native ID", anthropic.Model)
	}
	if got[2].APIType != models.APITypeOpenAIChat {
		t.Errorf("third API type = %q, want %q", got[2].APIType, models.APITypeOpenAIChat)
	}
	chat, ok := got[2].Service.(*oai.Service)
	if !ok {
		t.Fatalf("third service = %T, want *oai.Service", got[2].Service)
	}
	if chat.Model.ModelName != "native-upstream-chat" {
		t.Errorf("third wire model = %q, want native ID", chat.Model.ModelName)
	}
}

func TestLLMIntegrationPreservesNestedModelIDPathAndOrder(t *testing.T) {
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "openrouter/meta/llama", Provider: "openrouter", NativeID: "meta/llama-native", APIs: []string{"openai_chat"}},
			{ID: "openai/gpt-5.5", Provider: "openai", NativeID: "gpt-5.5-native", APIs: []string{"openai_responses"}},
		},
	}

	got := Build(models.All(), []Source{LLMIntegration(integ, "")}, &http.Client{}, nil)
	if len(got) != 2 {
		t.Fatalf("built models = %+v, want two", got)
	}
	for i, want := range []string{"meta/llama", "gpt-5.5"} {
		if got[i].ID != want {
			t.Errorf("built model %d ID = %q, want %q", i, got[i].ID, want)
		}
	}
	chat, ok := got[0].Service.(*oai.Service)
	if !ok {
		t.Fatalf("first service = %T, want *oai.Service", got[0].Service)
	}
	if chat.Model.ModelName != "meta/llama-native" {
		t.Errorf("first wire model = %q, want native ID", chat.Model.ModelName)
	}
}

func TestLLMIntegrationKeepsProviderPrefixesForShortIDCollisions(t *testing.T) {
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "openrouter/meta/llama", Provider: "openrouter", NativeID: "openrouter-llama", APIs: []string{"openai_chat"}},
			{ID: "fireworks/meta/llama", Provider: "fireworks", NativeID: "fireworks-llama", APIs: []string{"openai_chat"}},
			{ID: "openai/unique", Provider: "openai", NativeID: "unique", APIs: []string{"openai_responses"}},
		},
	}

	got := Build(models.All(), []Source{LLMIntegration(integ, "")}, &http.Client{}, nil)
	if len(got) != 3 {
		t.Fatalf("built models = %+v, want three", got)
	}
	for i, want := range []string{"openrouter/meta/llama", "fireworks/meta/llama", "unique"} {
		if got[i].ID != want {
			t.Errorf("built model %d ID = %q, want %q", i, got[i].ID, want)
		}
	}
}

func TestLLMIntegrationStableCatalogIDWinsShortIDCollision(t *testing.T) {
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "openrouter/claude-opus-4.7", Provider: "openrouter", NativeID: "openrouter-opus", APIs: []string{"openai_chat"}},
			{ID: "anthropic/renamed-upstream", Provider: "anthropic", NativeID: "claude-opus-4-7", APIs: []string{"anthropic_messages"}},
		},
	}

	got := Build(models.All(), []Source{LLMIntegration(integ, "")}, &http.Client{}, nil)
	if len(got) != 2 {
		t.Fatalf("built models = %+v, want two", got)
	}
	for i, want := range []string{"openrouter/claude-opus-4.7", "claude-opus-4.7"} {
		if got[i].ID != want {
			t.Errorf("built model %d ID = %q, want %q", i, got[i].ID, want)
		}
	}
}

func TestLLMIntegrationCollisionResolutionSpansSourcesBeforeSuffixes(t *testing.T) {
	primary := &LLMIntegrationConfig{
		Name: "primary", Host: "primary.int.exe.xyz", URL: "https://primary.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "openrouter/meta/llama", Provider: "openrouter", NativeID: "openrouter-llama", APIs: []string{"openai_chat"}},
		},
	}
	secondary := &LLMIntegrationConfig{
		Name: "secondary", Host: "secondary.int.exe.xyz", URL: "https://secondary.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "fireworks/meta/llama", Provider: "fireworks", NativeID: "fireworks-llama", APIs: []string{"openai_chat"}},
		},
	}

	got := Build(models.All(), []Source{
		LLMIntegration(primary, ""),
		LLMIntegration(secondary, "@secondary"),
	}, &http.Client{}, nil)
	if len(got) != 2 {
		t.Fatalf("built models = %+v, want two", got)
	}
	for i, want := range []string{"openrouter/meta/llama", "fireworks/meta/llama@secondary"} {
		if got[i].ID != want {
			t.Errorf("built model %d ID = %q, want %q", i, got[i].ID, want)
		}
	}
}

func TestLLMIntegrationAvoidsBuiltInModelIDCollision(t *testing.T) {
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "openai/predictable", Provider: "openai", NativeID: "predictable-upstream", APIs: []string{"openai_responses"}},
		},
	}

	got := Build(models.All(), []Source{LLMIntegration(integ, ""), Predictable()}, &http.Client{}, nil)
	for _, id := range []string{"openai/predictable", "predictable"} {
		if findBuilt(got, id) == nil {
			t.Errorf("missing %q from %+v", id, got)
		}
	}
}

func TestLLMIntegrationCatalogAndDynamicImageCapabilities(t *testing.T) {
	var catalog llmIntegrationModelCatalog
	if err := json.Unmarshal([]byte(`{
		"schema_version": 1,
		"models": [
			{"id":"anthropic/image","provider":"anthropic","native_id":"claude-opus-4-7","apis":["anthropic_messages"],"architecture":{"input_modalities":["text","image"]}},
			{"id":"anthropic/text","provider":"anthropic","native_id":"claude-opus-5-5","apis":["anthropic_messages"]},
			{"id":"openai/responses-image","provider":"openai","native_id":"gpt-5.6-sol","apis":["openai_responses"],"architecture":{"input_modalities":["text","image"]}},
			{"id":"openai/responses-text","provider":"openai","native_id":"gpt-5.5","apis":["openai_responses"]},
			{"id":"openai/chat-image","provider":"openai","native_id":"gpt-4o","apis":["openai_chat"],"architecture":{"input_modalities":["image","text"]}},
			{"id":"openai/chat-text","provider":"openai","native_id":"gpt-4.1","apis":["openai_chat"]}
		]
	}`), &catalog); err != nil {
		t.Fatal(err)
	}

	integ := &LLMIntegrationConfig{
		Name:   "llm",
		Host:   "llm.int.exe.xyz",
		URL:    "https://llm.int.exe.xyz",
		Models: integrationModelsFromCatalog(catalog),
	}
	built := Build(models.All(), []Source{LLMIntegration(integ, "")}, &http.Client{}, nil)
	for _, tt := range []struct {
		id   string
		want bool
	}{
		{"claude-opus-4.7", true},
		{"claude-opus-5.5", true},
		{"gpt-5.6-sol", true},
		{"gpt-5.5", true},
		{"chat-image", true},
		{"chat-text", false},
	} {
		b := findBuilt(built, tt.id)
		if b == nil {
			t.Errorf("missing %q", tt.id)
			continue
		}
		if got := b.Service.SupportsImages(); got != tt.want {
			t.Errorf("%s SupportsImages() = %t, want %t", tt.id, got, tt.want)
		}
	}
}

func TestIntegrationModelsFromCatalogUsesNativeIDsForSupportedAPIs(t *testing.T) {
	got := integrationModelsFromCatalog(llmIntegrationModelCatalog{
		SchemaVersion: 1,
		Models: []IntegrationModel{
			{ID: "anthropic/claude-opus-4-7", Provider: "anthropic", NativeID: "claude-opus-4-7", APIs: []string{"anthropic_messages"}},
			{ID: "openai/gpt-5.6-sol", Provider: "openai", NativeID: "gpt-5.6-sol", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "openai/gpt-5.5", Provider: "openai", NativeID: "gpt-5.5", APIs: []string{"openai_responses"}},
			{ID: "fireworks/glm-5p2", Provider: "fireworks", NativeID: "accounts/fireworks/models/glm-5p2", APIs: []string{"openai_chat"}},
			{ID: "mistral/upstream-chat", Provider: "mistral", NativeID: "upstream-chat", APIs: []string{"openai_chat"}},
			{Provider: "openai", NativeID: "missing-integration-id", APIs: []string{"openai_responses"}},
			{ID: "openai/text-embedding-3-small", Provider: "openai", NativeID: "text-embedding-3-small", APIs: []string{"openai_embeddings"}},
			{ID: "gemini/gemini-3-flash", Provider: "gemini", NativeID: "gemini-3-flash-preview", APIs: []string{"gemini"}},
		},
	})

	if len(got) != 5 {
		t.Fatalf("supported model count = %d, want 5 (%+v)", len(got), got)
	}
	for i, want := range []string{"claude-opus-4-7", "gpt-5.6-sol", "gpt-5.5", "accounts/fireworks/models/glm-5p2", "upstream-chat"} {
		if got[i].apiModelName() != want {
			t.Fatalf("model %d apiModelName = %q, want %q", i, got[i].apiModelName(), want)
		}
	}
}

func TestDiscoverLLMIntegrationsFallsBackWhenReflectionRequestFails(t *testing.T) {
	tests := []struct {
		name             string
		catalogStatus    int
		catalogBody      string
		wantFound        bool
		wantIntegrations int
	}{
		{
			name:          "catalog available",
			catalogStatus: http.StatusOK,
			catalogBody: `{
				"schema_version": 1,
				"models": [
					{"id":"openai/gpt-5.5","provider":"openai","native_id":"gpt-5.5","apis":["openai_responses"]}
				]
			}`,
			wantFound:        true,
			wantIntegrations: 1,
		},
		{
			name:             "catalog has no supported models",
			catalogStatus:    http.StatusOK,
			catalogBody:      `{"schema_version":1,"models":[]}`,
			wantFound:        true,
			wantIntegrations: 0,
		},
		{
			name:             "catalog unavailable",
			catalogStatus:    http.StatusNotFound,
			wantFound:        false,
			wantIntegrations: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests []string
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests = append(requests, req.URL.String())
				status := http.StatusServiceUnavailable
				body := ""
				if req.URL.String() == "https://llm.int.exe.xyz/models.json" {
					status = tt.catalogStatus
					body = tt.catalogBody
				}
				return &http.Response{
					StatusCode: status,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    req,
				}, nil
			})}

			result := discoverLLMIntegrations(t.Context(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), testProdExeEnv(t))
			if result.Found != tt.wantFound {
				t.Fatalf("Found = %v, want %v", result.Found, tt.wantFound)
			}
			if len(result.Integrations) != tt.wantIntegrations {
				t.Fatalf("integrations = %+v, want %d", result.Integrations, tt.wantIntegrations)
			}
			wantRequests := []string{
				"https://reflection.int.exe.xyz/integrations",
				"https://llm.int.exe.xyz/models.json",
			}
			if strings.Join(requests, "\n") != strings.Join(wantRequests, "\n") {
				t.Fatalf("requests = %q, want %q", requests, wantRequests)
			}
			if tt.wantIntegrations == 1 {
				integ := result.Integrations[0]
				if integ.Name != "llm" || integ.Host != "llm.int.exe.xyz" || integ.URL != "https://llm.int.exe.xyz" {
					t.Fatalf("integration = %+v, want default llm integration", integ)
				}
			}
		})
	}
}

func TestDiscoverLLMIntegrationsDoesNotFallbackAfterSuccessfulReflection(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.String() != "https://reflection.int.exe.xyz/integrations" {
			t.Fatalf("unexpected fallback request: %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"integrations":[]}`)),
			Request:    req,
		}, nil
	})}

	result := discoverLLMIntegrations(t.Context(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), testProdExeEnv(t))
	if result.Found {
		t.Fatal("Found = true, want false")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestDiscoverLLMIntegrationsKeepsFoundWhenReflectedCatalogFails(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := `{"integrations":[{"name":"llm","type":"llm"}]}`
		if req.URL.String() == "https://llm.int.exe.xyz/models.json" {
			status = http.StatusServiceUnavailable
			body = ""
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	result := discoverLLMIntegrations(t.Context(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), testProdExeEnv(t))
	if !result.Found || len(result.Integrations) != 0 {
		t.Fatalf("result = %+v, want found integration with unavailable catalog", result)
	}
}

func TestDiscoverLLMIntegrationsFallbackUsesEnvironmentURLs(t *testing.T) {
	env, err := exeenv.New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := `{"schema_version":1,"models":[{"id":"openai/gpt-5.5","native_id":"gpt-5.5","apis":["openai_responses"]}]}`
		switch req.URL.String() {
		case "https://reflection.int.example.test/integrations":
			status = http.StatusBadGateway
		case "https://llm.int.example.test/models.json":
		default:
			t.Fatalf("unexpected discovery request: %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	result := discoverLLMIntegrations(t.Context(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), env)
	if !result.Found || len(result.Integrations) != 1 {
		t.Fatalf("result = %+v, want one found integration", result)
	}
	if got := result.Integrations[0].URL; got != "https://llm.int.example.test" {
		t.Fatalf("integration URL = %q, want configured environment URL", got)
	}
}

func TestDiscoverLLMIntegrationsDoesNotProbeOutsideExeVM(t *testing.T) {
	oldMarkerPath := exeDevMarkerPath
	exeDevMarkerPath = t.TempDir() + "/missing"
	t.Cleanup(func() { exeDevMarkerPath = oldMarkerPath })

	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected discovery request outside exe VM: %s", req.URL.String())
		return nil, nil
	})}
	result := DiscoverLLMIntegrations(t.Context(), client, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if result.Found || len(result.Integrations) != 0 {
		t.Fatalf("result = %+v, want no discovered integration", result)
	}
}

func TestDiscoverLLMIntegrationsReadsModelsJSONCatalog(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Host + req.URL.Path {
		case "reflection.int.exe.xyz/integrations":
			body = `{"integrations":[{"name":"llm","type":"llm"}]}`
		case "llm.int.exe.xyz/models.json":
			body = `{
				"schema_version": 1,
				"models": [
					{"id":"anthropic/claude-opus-4-7","provider":"anthropic","native_id":"claude-opus-4-7","apis":["anthropic_messages"]},
					{"id":"openai/gpt-5.6-sol","provider":"openai","native_id":"gpt-5.6-sol","apis":["openai_chat","openai_responses"]},
					{"id":"openai/gpt-5.5","provider":"openai","native_id":"gpt-5.5","apis":["openai_responses"]},
					{"id":"fireworks/glm-5p2","provider":"fireworks","native_id":"accounts/fireworks/models/glm-5p2","apis":["openai_chat"]},
					{"id":"openai/gpt-transcribe","provider":"openai","native_id":"gpt-transcribe","apis":["openai_transcriptions"]}
				]
			}`
		default:
			t.Fatalf("unexpected discovery request: %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	result := discoverLLMIntegrations(t.Context(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), testProdExeEnv(t))
	if !result.Found {
		t.Fatal("Found = false, want true")
	}
	if len(result.Integrations) != 1 {
		t.Fatalf("integrations = %+v, want one", result.Integrations)
	}
	integ := result.Integrations[0]
	if integ.Name != "llm" || integ.Host != "llm.int.exe.xyz" || integ.URL != "https://llm.int.exe.xyz" {
		t.Fatalf("integration = %+v, want llm host/base URL", integ)
	}
	if len(integ.Models) != 4 {
		t.Fatalf("models = %+v, want 4", integ.Models)
	}
	for i, want := range []string{"claude-opus-4-7", "gpt-5.6-sol", "gpt-5.5", "accounts/fireworks/models/glm-5p2"} {
		if integ.Models[i].apiModelName() != want {
			t.Fatalf("model %d apiModelName = %q, want %q", i, integ.Models[i].apiModelName(), want)
		}
	}
	transcriptionModels := TranscriptionModels([]Source{LLMIntegration(integ, "")})
	if len(transcriptionModels) != 1 ||
		transcriptionModels[0].Model != "gpt-transcribe" ||
		transcriptionModels[0].Endpoint != "https://llm.int.exe.xyz/v1/audio/transcriptions" {
		t.Fatalf("transcription models = %+v", transcriptionModels)
	}
}

func TestDiscoverLLMIntegrationsUsesTeamHost(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Host + req.URL.Path {
		case "reflection.int.exe.xyz/integrations":
			body = `{"integrations":[{"name":"shared-llm","type":"llm","team":true}]}`
		case "shared-llm.team.exe.xyz/models.json":
			body = `{
				"schema_version": 1,
				"models": [
					{"id":"openai/gpt-5.5","provider":"openai","native_id":"gpt-5.5","apis":["openai_responses"]}
				]
			}`
		default:
			t.Fatalf("unexpected discovery request: %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	result := discoverLLMIntegrations(t.Context(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), testProdExeEnv(t))
	if !result.Found {
		t.Fatal("Found = false, want true")
	}
	if len(result.Integrations) != 1 {
		t.Fatalf("integrations = %+v, want one", result.Integrations)
	}
	integ := result.Integrations[0]
	if integ.Host != "shared-llm.team.exe.xyz" || integ.URL != "https://shared-llm.team.exe.xyz" {
		t.Fatalf("integration = %+v, want team host/base URL", integ)
	}
}

func TestDiscoverLLMIntegrationsUsesEnvironmentURLs(t *testing.T) {
	tests := []struct {
		name    string
		scheme  string
		boxHost string
	}{
		{name: "development", scheme: "http", boxHost: "exe.cloud"},
		{name: "configured HTTPS", scheme: "https", boxHost: "example.test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reflectionURL := tt.scheme + "://reflection.int." + tt.boxHost + "/integrations"
			personalURL := tt.scheme + "://llm.int." + tt.boxHost
			teamURL := tt.scheme + "://shared-llm.team." + tt.boxHost
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				var body string
				switch req.URL.String() {
				case reflectionURL:
					body = `{"integrations":[{"name":"llm","type":"llm"},{"name":"shared-llm","type":"llm","team":true}]}`
				case personalURL + "/models.json", teamURL + "/models.json":
					body = `{
						"schema_version": 1,
						"models": [
							{"id":"openai/gpt-5.5","provider":"openai","native_id":"gpt-5.5","apis":["openai_responses"]}
						]
					}`
				default:
					t.Fatalf("unexpected discovery request: %s", req.URL.String())
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    req,
				}, nil
			})}

			env, err := exeenv.New(tt.scheme, tt.boxHost)
			if err != nil {
				t.Fatal(err)
			}
			result := discoverLLMIntegrations(t.Context(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), env)
			if !result.Found {
				t.Fatal("Found = false, want true")
			}
			if len(result.Integrations) != 2 {
				t.Fatalf("integrations = %+v, want two", result.Integrations)
			}
			if got := result.Integrations[0]; got.Host != "llm.int."+tt.boxHost || got.URL != personalURL {
				t.Errorf("personal integration = %+v, want configured host/base URL", got)
			}
			if got := result.Integrations[1]; got.Host != "shared-llm.team."+tt.boxHost || got.URL != teamURL {
				t.Errorf("team integration = %+v, want configured host/base URL", got)
			}
		})
	}
}

func TestMultipleLLMIntegrationsUnionWithSuffix(t *testing.T) {
	primary := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "anthropic/claude-opus-4-7", Provider: "anthropic", NativeID: "claude-opus-4-7", APIs: []string{"anthropic_messages"}},
			{ID: "openai/gpt-5.5", Provider: "openai", NativeID: "gpt-5.5", APIs: []string{"openai_responses"}},
		},
	}
	secondary := &LLMIntegrationConfig{
		Name: "llm2", Host: "llm2.int.exe.xyz", URL: "https://llm2.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "anthropic/claude-opus-4-7", Provider: "anthropic", NativeID: "claude-opus-4-7", APIs: []string{"anthropic_messages"}},
			{ID: "anthropic/claude-sonnet-4-6", Provider: "anthropic", NativeID: "claude-sonnet-4-6", APIs: []string{"anthropic_messages"}},
		},
	}
	bs := Build(models.All(), []Source{
		LLMIntegration(primary, ""),
		LLMIntegration(secondary, "@llm2"),
		Predictable(),
	}, &http.Client{}, nil)
	for _, id := range []string{"claude-opus-4.7", "gpt-5.5", "claude-opus-4.7@llm2", "claude-sonnet-4.6@llm2"} {
		if findBuilt(bs, id) == nil {
			t.Errorf("missing %q", id)
		}
	}
	if b := findBuilt(bs, "claude-opus-4.7"); b == nil || b.Source != "llm.int.exe.xyz" {
		t.Errorf("primary collision lost: %+v", b)
	}
	if b := findBuilt(bs, "claude-opus-4.7@llm2"); b == nil || b.Source != "llm2.int.exe.xyz" {
		t.Errorf("suffixed model wrong: %+v", b)
	}
}

func TestBuiltBaseURLResolution(t *testing.T) {
	// Env source supplies no URL: BaseURL should be the catalog default.
	bs := Build(models.All(), []Source{Env("a", "o", "g", "f")}, &http.Client{}, nil)
	for _, tt := range []struct {
		id, want string
	}{
		{"claude-opus-5.5", "https://api.anthropic.com"},
		{"gpt-5.5", "https://api.openai.com"},
		{"kimi-k3-fireworks", "https://api.fireworks.ai/inference"},
		{"gemini-3.8-flash", "https://generativelanguage.googleapis.com"},
	} {
		b := findBuilt(bs, tt.id)
		if b == nil {
			t.Errorf("missing %q", tt.id)
			continue
		}
		if b.BaseURL != tt.want {
			t.Errorf("%s BaseURL = %q, want %q", tt.id, b.BaseURL, tt.want)
		}
	}

	// LLM-integration source supplies a URL: BaseURL should be that URL.
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "anthropic/claude-opus-4-7", Provider: "anthropic", NativeID: "claude-opus-4-7", APIs: []string{"anthropic_messages"}},
			{ID: "openai/gpt-5.5", Provider: "openai", NativeID: "gpt-5.5", APIs: []string{"openai_responses"}},
		},
	}
	bs = Build(models.All(), []Source{LLMIntegration(integ, "")}, &http.Client{}, nil)
	if b := findBuilt(bs, "claude-opus-4.7"); b == nil || b.BaseURL != "https://llm.int.exe.xyz" {
		t.Errorf("claude-opus-4.7 BaseURL: %+v", b)
	}
	if b := findBuilt(bs, "gpt-5.5"); b == nil || b.BaseURL != "https://llm.int.exe.xyz" {
		t.Errorf("gpt-5.5 BaseURL: %+v", b)
	}
}

func TestBuiltAPITypePopulated(t *testing.T) {
	bs := Build(models.All(), []Source{Env("a", "o", "g", "f"), Predictable()}, &http.Client{}, nil)
	for _, tt := range []struct {
		id   string
		want models.APIType
	}{
		{"claude-opus-5.5", models.APITypeAnthropicMessages},
		{"gpt-5.5", models.APITypeOpenAIResponses},
		{"kimi-k3-fireworks", models.APITypeOpenAIChat},
		{"gemini-3.8-flash", models.APITypeGemini},
		{"predictable", models.APITypeBuiltIn},
	} {
		b := findBuilt(bs, tt.id)
		if b == nil {
			t.Errorf("missing %q", tt.id)
			continue
		}
		if b.APIType != tt.want {
			t.Errorf("%s APIType = %q, want %q", tt.id, b.APIType, tt.want)
		}
	}
}

func TestTranscriptionModelsFromOpenAICredentials(t *testing.T) {
	routes := func(sources ...Source) []models.TranscriptionModel {
		return TranscriptionModels(sources)
	}
	want := func(endpoint, apiKey, source string) []models.TranscriptionModel {
		return []models.TranscriptionModel{
			{Model: "gpt-transcribe", Endpoint: endpoint, APIKey: apiKey, Source: source, FromCredentials: true},
			{Model: "whisper-1", Endpoint: endpoint, APIKey: apiKey, Source: source, FromCredentials: true},
		}
	}
	for _, tt := range []struct {
		name string
		got  []models.TranscriptionModel
		want []models.TranscriptionModel
	}{
		{
			name: "env key",
			got:  routes(Env("a", "sk-openai", "g", "f"), Predictable()),
			want: want("https://api.openai.com/v1/audio/transcriptions", "sk-openai", "$OPENAI_API_KEY"),
		},
		{
			name: "gateway",
			got:  routes(Gateway("https://gw.example.com", "", "", "")),
			want: want("https://gw.example.com/openai/v1/audio/transcriptions", "implicit", "exe.dev gateway"),
		},
		{
			name: "gateway with explicit key",
			got:  routes(Gateway("https://gw.example.com", "", "sk-openai", "")),
			want: want("https://gw.example.com/openai/v1/audio/transcriptions", "sk-openai", "$OPENAI_API_KEY"),
		},
		{
			name: "no OpenAI credential",
			got:  routes(Env("a", "", "g", "f"), Predictable()),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.got) != len(tt.want) {
				t.Fatalf("routes = %+v, want %+v", tt.got, tt.want)
			}
			for i := range tt.want {
				if tt.got[i] != tt.want[i] {
					t.Fatalf("route %d = %+v, want %+v", i, tt.got[i], tt.want[i])
				}
			}
		})
	}
}
