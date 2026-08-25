package modelsources

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/models"
)

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
		{"claude-opus-4.6", "$ANTHROPIC_API_KEY"},
		{"gpt-5.5", "$OPENAI_API_KEY"},
		{"gemini-3-flash", "$GEMINI_API_KEY"},
		{"deepseek-v4-flash-0731-fireworks", "$FIREWORKS_API_KEY"},
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
	if b := findBuilt(bs, "claude-opus-4.6"); b == nil || b.Source != "exe.dev gateway" {
		t.Errorf("claude-opus-4.6 with plain gateway: %+v", b)
	}
	if b := findBuilt(bs, "gemini-3-flash"); b != nil {
		t.Errorf("gemini-3-flash should not be built by gateway, got %+v", b)
	}
	if b := findBuilt(bs, "grok-4.5"); b == nil || b.Source != "exe.dev gateway" {
		t.Errorf("grok-4.5 with plain gateway: %+v", b)
	}

	// Gateway with explicit anthropic key: provider label switches.
	bs = Build(models.All(), []Source{Gateway("https://gw.example.com", "real-key", "", "")}, &http.Client{}, nil)
	if b := findBuilt(bs, "claude-opus-4.6"); b == nil || b.Source != "$ANTHROPIC_API_KEY" {
		t.Errorf("claude-opus-4.6 with explicit anthropic key: %+v", b)
	}
	if b := findBuilt(bs, "gpt-5.5"); b == nil || b.Source != "exe.dev gateway" {
		t.Errorf("gpt-5.5 should still be gateway: %+v", b)
	}
}

func TestLLMIntegrationSourceLabelsAndFiltering(t *testing.T) {
	integ := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{ID: "anthropic/claude-opus-4-8", Provider: "anthropic", NativeID: "claude-opus-4-8", APIs: []string{"anthropic_messages"}},
			{ID: "anthropic/claude-opus-4-7", Provider: "anthropic", NativeID: "claude-opus-4-7", APIs: []string{"anthropic_messages"}},
			{ID: "anthropic/claude-opus-4-6", Provider: "anthropic", NativeID: "claude-opus-4-6", APIs: []string{"anthropic_messages"}},
			{ID: "anthropic/claude-sonnet-4-6", Provider: "anthropic", NativeID: "claude-sonnet-4-6", APIs: []string{"anthropic_messages"}},
			{ID: "openai/gpt-5.6-sol", Provider: "openai", NativeID: "gpt-5.6-sol", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "openai/gpt-5.6-terra", Provider: "openai", NativeID: "gpt-5.6-terra", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "openai/gpt-5.6-luna", Provider: "openai", NativeID: "gpt-5.6-luna", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "openai/gpt-5.5", Provider: "openai", NativeID: "gpt-5.5", APIs: []string{"openai_responses"}},
			{ID: "fireworks/glm-5p2", Provider: "fireworks", NativeID: "accounts/fireworks/models/glm-5p2", APIs: []string{"openai_chat"}},
			{ID: "fireworks/kimi-k2p6", Provider: "fireworks", NativeID: "accounts/fireworks/models/kimi-k2p6", APIs: []string{"openai_chat"}},
			{ID: "fireworks/deepseek-v4-pro", Provider: "fireworks", NativeID: "accounts/fireworks/models/deepseek-v4-pro", APIs: []string{"openai_chat"}},
			{ID: "fireworks/deepseek-v4-flash-0731", Provider: "fireworks", NativeID: "accounts/fireworks/models/deepseek-v4-flash-0731", APIs: []string{"openai_chat"}},
		},
	}
	bs := Build(models.All(), []Source{LLMIntegration(integ, ""), Predictable()}, &http.Client{}, nil)
	wantLabel := "llm.int.exe.xyz"
	for _, id := range []string{
		"claude-opus-4.8",
		"claude-opus-4.7",
		"claude-opus-4.6",
		"claude-sonnet-4.6",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5.5",
		"glm-5.2-fireworks",
		"kimi-k2.6-fireworks",
		"deepseek-v4-pro-fireworks",
		"deepseek-v4-flash-0731-fireworks",
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
	for _, id := range []string{
		"anthropic/claude-opus-4-7",
		"openai/gpt-5.5",
		"claude-opus-4-7",
		"glm-5p2",
		"kimi-k2p6",
		"deepseek-v4-pro",
		"deepseek-v4-flash-0731",
		"gemini-3-flash",
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
			{"id":"anthropic/text","provider":"anthropic","native_id":"claude-opus-4-6","apis":["anthropic_messages"]},
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
		{"claude-opus-4.6", true},
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

			result := discoverLLMIntegrations(context.Background(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), exeenv.FromHostname("box.exe.xyz"))
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

	result := discoverLLMIntegrations(context.Background(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), exeenv.FromHostname("box.exe.xyz"))
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

	result := discoverLLMIntegrations(context.Background(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), exeenv.FromHostname("box.exe.xyz"))
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

	result := discoverLLMIntegrations(context.Background(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), env)
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
	result := DiscoverLLMIntegrations(context.Background(), client, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
					{"id":"fireworks/glm-5p2","provider":"fireworks","native_id":"accounts/fireworks/models/glm-5p2","apis":["openai_chat"]}
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

	result := discoverLLMIntegrations(context.Background(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), exeenv.FromHostname("box.exe.xyz"))
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

	result := discoverLLMIntegrations(context.Background(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), exeenv.FromHostname("box.exe.xyz"))
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
			result := discoverLLMIntegrations(context.Background(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), env)
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
		{"claude-opus-4.6", "https://api.anthropic.com"},
		{"gpt-5.5", "https://api.openai.com"},
		{"deepseek-v4-flash-0731-fireworks", "https://api.fireworks.ai/inference"},
		{"gemini-3-flash", "https://generativelanguage.googleapis.com"},
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
		{"claude-opus-4.6", models.APITypeAnthropicMessages},
		{"gpt-5.5", models.APITypeOpenAIResponses},
		{"deepseek-v4-flash-0731-fireworks", models.APITypeOpenAIChat},
		{"gemini-3-flash", models.APITypeGemini},
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
