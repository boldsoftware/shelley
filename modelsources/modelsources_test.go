package modelsources

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"shelley.exe.dev/exeenv"
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
		{"gemini-3-pro", "$GEMINI_API_KEY"},
		{"gpt-oss-20b-fireworks", "$FIREWORKS_API_KEY"},
	} {
		b := findBuilt(bs, tt.id)
		if b == nil {
			t.Errorf("missing %q", tt.id)
			continue
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
	if b := findBuilt(bs, "gemini-3-pro"); b != nil {
		t.Errorf("gemini-3-pro should not be built by gateway, got %+v", b)
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
			{ID: "anthropic/claude-opus-4-7", Provider: "anthropic", NativeID: "claude-opus-4-7", APIs: []string{"anthropic_messages"}},
			{ID: "openai/gpt-5.6-sol", Provider: "openai", NativeID: "gpt-5.6-sol", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "openai/gpt-5.6-terra", Provider: "openai", NativeID: "gpt-5.6-terra", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "openai/gpt-5.6-luna", Provider: "openai", NativeID: "gpt-5.6-luna", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "openai/gpt-5.5", Provider: "openai", NativeID: "gpt-5.5", APIs: []string{"openai_responses"}},
			{ID: "fireworks/glm-5p2", Provider: "fireworks", NativeID: "accounts/fireworks/models/glm-5p2", APIs: []string{"openai_chat"}},
			{ID: "fireworks/gpt-oss-20b", Provider: "fireworks", NativeID: "accounts/fireworks/models/gpt-oss-20b", APIs: []string{"openai_chat"}},
		},
	}
	bs := Build(models.All(), []Source{LLMIntegration(integ, ""), Predictable()}, &http.Client{}, nil)
	wantLabel := "llm.int.exe.xyz"
	for _, id := range []string{"claude-opus-4.7", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "glm-5.2-fireworks", "gpt-oss-20b-fireworks"} {
		b := findBuilt(bs, id)
		if b == nil {
			t.Errorf("%q should be built", id)
			continue
		}
		if b.Source != wantLabel {
			t.Errorf("%s source = %q, want %q", id, b.Source, wantLabel)
		}
	}
	for _, id := range []string{"gpt-5.6", "gemini-3-pro", "gemini-3-flash", "claude-opus-4.6", "claude-sonnet-4.6"} {
		if b := findBuilt(bs, id); b != nil {
			t.Errorf("%q should NOT be built, got %+v", id, b)
		}
	}
	if findBuilt(bs, "predictable") == nil {
		t.Errorf("predictable should survive integration filter")
	}
}

func TestIntegrationModelsFromCatalogUsesNativeIDsForSupportedAPIs(t *testing.T) {
	got := integrationModelsFromCatalog(llmIntegrationModelCatalog{
		SchemaVersion: 1,
		Models: []IntegrationModel{
			{ID: "anthropic/claude-opus-4-7", Provider: "anthropic", NativeID: "claude-opus-4-7", APIs: []string{"anthropic_messages"}},
			{ID: "openai/gpt-5.6-sol", Provider: "openai", NativeID: "gpt-5.6-sol", APIs: []string{"openai_chat", "openai_responses"}},
			{ID: "openai/gpt-5.5", Provider: "openai", NativeID: "gpt-5.5", APIs: []string{"openai_responses"}},
			{ID: "fireworks/glm-5p1", Provider: "fireworks", NativeID: "accounts/fireworks/models/glm-5p1", APIs: []string{"openai_chat"}},
			{ID: "openai/text-embedding-3-small", Provider: "openai", NativeID: "text-embedding-3-small", APIs: []string{"openai_embeddings"}},
			{ID: "gemini/gemini-3-pro", Provider: "gemini", NativeID: "gemini-3-pro-preview", APIs: []string{"gemini"}},
		},
	})

	if len(got) != 4 {
		t.Fatalf("supported model count = %d, want 4 (%+v)", len(got), got)
	}
	for i, want := range []string{"claude-opus-4-7", "gpt-5.6-sol", "gpt-5.5", "accounts/fireworks/models/glm-5p1"} {
		if got[i].apiModelName() != want {
			t.Fatalf("model %d apiModelName = %q, want %q", i, got[i].apiModelName(), want)
		}
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
					{"id":"fireworks/glm-5p1","provider":"fireworks","native_id":"accounts/fireworks/models/glm-5p1","apis":["openai_chat"]}
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
	for i, want := range []string{"claude-opus-4-7", "gpt-5.6-sol", "gpt-5.5", "accounts/fireworks/models/glm-5p1"} {
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

func TestDiscoverLLMIntegrationsUsesDevelopmentURLs(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.String() {
		case "http://reflection.int.exe.cloud/integrations":
			body = `{"integrations":[{"name":"llm","type":"llm"},{"name":"shared-llm","type":"llm","team":true}]}`
		case "http://llm.int.exe.cloud/models.json", "http://shared-llm.team.exe.cloud/models.json":
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

	env := exeenv.FromHostname("box.exe.cloud")
	result := discoverLLMIntegrations(context.Background(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), env)
	if !result.Found {
		t.Fatal("Found = false, want true")
	}
	if len(result.Integrations) != 2 {
		t.Fatalf("integrations = %+v, want two", result.Integrations)
	}
	if got := result.Integrations[0]; got.Host != "llm.int.exe.cloud" || got.URL != "http://llm.int.exe.cloud" {
		t.Errorf("personal integration = %+v, want development host/base URL", got)
	}
	if got := result.Integrations[1]; got.Host != "shared-llm.team.exe.cloud" || got.URL != "http://shared-llm.team.exe.cloud" {
		t.Errorf("team integration = %+v, want development host/base URL", got)
	}
}

func TestMultipleLLMIntegrationsUnionWithSuffix(t *testing.T) {
	primary := &LLMIntegrationConfig{
		Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
		Models: []IntegrationModel{
			{Provider: "anthropic", NativeID: "claude-opus-4-7", APIs: []string{"anthropic_messages"}},
			{Provider: "openai", NativeID: "gpt-5.5", APIs: []string{"openai_responses"}},
		},
	}
	secondary := &LLMIntegrationConfig{
		Name: "llm2", Host: "llm2.int.exe.xyz", URL: "https://llm2.int.exe.xyz",
		Models: []IntegrationModel{
			{Provider: "anthropic", NativeID: "claude-opus-4-7", APIs: []string{"anthropic_messages"}},
			{Provider: "anthropic", NativeID: "claude-sonnet-4-6", APIs: []string{"anthropic_messages"}},
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
		{"gpt-oss-20b-fireworks", "https://api.fireworks.ai/inference"},
		{"gemini-3-pro", "https://generativelanguage.googleapis.com"},
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
			{Provider: "anthropic", NativeID: "claude-opus-4-7", APIs: []string{"anthropic_messages"}},
			{Provider: "openai", NativeID: "gpt-5.5", APIs: []string{"openai_responses"}},
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
		{"gpt-oss-20b-fireworks", models.APITypeOpenAIChat},
		{"gemini-3-pro", models.APITypeGemini},
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
