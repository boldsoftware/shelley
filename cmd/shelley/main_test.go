package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
	"shelley.exe.dev/modelsources"
	"shelley.exe.dev/slug"
)

// testBinDir holds the shelley binary that the CLI tests share. It outlives
// every test, so TestMain removes it rather than any one test's t.TempDir.
// It is made only when a test first needs the binary: tests that rerun this
// test binary as a helper process run TestMain again and may exit without
// returning to it.
var testBinDir string

func TestMain(m *testing.M) {
	code := m.Run()
	if testBinDir != "" {
		os.RemoveAll(testBinDir)
	}
	os.Exit(code)
}

// buildTestBinary builds the shelley binary once, on first use, for every test
// that runs it as is. Building it per test linked the same binary again each
// time, and parallel tests building at once would compile it side by side.
var buildTestBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "shelley-cmd-test-")
	if err != nil {
		return "", err
	}
	testBinDir = dir
	binary := filepath.Join(dir, "shelley")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		return "", fmt.Errorf("build shelley: %v\n%s", err, out)
	}
	return binary, nil
})

// shelleyBinary returns the shared shelley binary. Tests must not modify it.
func shelleyBinary(t *testing.T) string {
	t.Helper()
	binary, err := buildTestBinary()
	if err != nil {
		t.Fatal(err)
	}
	return binary
}

type tieredModelProvider struct {
	ids   []string
	infos map[string]*models.ModelInfo
}

func (p *tieredModelProvider) GetService(string) (llm.Service, error) { return nil, nil }
func (p *tieredModelProvider) GetAvailableModels() []string           { return p.ids }
func (p *tieredModelProvider) GetWorkhorseService(modelID string) (llm.Service, error) {
	return p.GetService(modelID)
}
func (p *tieredModelProvider) HasModel(string) bool { return true }
func (p *tieredModelProvider) GetModelInfo(id string) *models.ModelInfo {
	return p.infos[id]
}
func (p *tieredModelProvider) RefreshCustomModels() error { return nil }

func TestSanitizeSlug(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Simple Test", "simple-test"},
		{"Create a Python Script", "create-a-python-script"},
		{"Multiple   Spaces", "multiple-spaces"},
		{"Special@#$%Characters", "specialcharacters"},
		{"Under_Score_Test", "under-score-test"},
		{"--multiple-hyphens--", "multiple-hyphens"},
		{"CamelCase Example", "camelcase-example"},
		{"123 Numbers Test 456", "123-numbers-test-456"},
		{"   leading and trailing   ", "leading-and-trailing"},
		{"", ""},
	}

	for _, test := range tests {
		result := slug.Sanitize(test.input)
		if result != test.expected {
			t.Errorf("slug.Sanitize(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}

func TestBuildLLMConfigSkipsGatewayWhenReflectionFoundLLMIntegration(t *testing.T) {
	oldDiscover := discoverLLMIntegrations
	discoverLLMIntegrations = func(context.Context, *http.Client, *slog.Logger) modelsources.LLMIntegrationDiscoveryResult {
		return modelsources.LLMIntegrationDiscoveryResult{Found: true}
	}
	t.Cleanup(func() { discoverLLMIntegrations = oldDiscover })

	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("FIREWORKS_API_KEY", "")

	configPath := filepath.Join(t.TempDir(), "shelley.json")
	if err := os.WriteFile(configPath, []byte(`{"llm_gateway":"https://gateway.example.com"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := buildLLMConfig(GlobalConfig{ConfigPath: configPath}, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range cfg.Models {
		if model.Source == "exe.dev gateway" {
			t.Fatalf("gateway model %q was built despite discovered LLM integration", model.ID)
		}
	}
	if findBuiltModelSource(cfg.Models, "predictable") != "builtin" {
		t.Fatalf("predictable model missing from config")
	}
}

func TestBuildLLMConfigRoutesGemini(t *testing.T) {
	oldDiscover := discoverLLMIntegrations
	discoverLLMIntegrations = func(context.Context, *http.Client, *slog.Logger) modelsources.LLMIntegrationDiscoveryResult {
		return modelsources.LLMIntegrationDiscoveryResult{}
	}
	t.Cleanup(func() { discoverLLMIntegrations = oldDiscover })
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("FIREWORKS_API_KEY", "")
	configPath := filepath.Join(t.TempDir(), "shelley.json")
	if err := os.WriteFile(configPath, []byte(`{"llm_gateway":"https://gateway.example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct{ key, want string }{
		{"", "exe.dev gateway"},
		{"gemini-key", "$GEMINI_API_KEY"},
	} {
		t.Setenv("GEMINI_API_KEY", tt.key)
		cfg, err := buildLLMConfig(GlobalConfig{ConfigPath: configPath}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := findBuiltModelSource(cfg.Models, "gemini-3.8-flash"); got != tt.want {
			t.Errorf("GEMINI_API_KEY=%q: gemini-3.8-flash source = %q, want %q", tt.key, got, tt.want)
		}
	}
}

// GEMINI_API_KEY predates Gemini on LLM integrations, so it keeps serving
// Gemini when one is found, rather than moving that spend to the integration.
func TestBuildLLMConfigPrefersGeminiKeyOverIntegration(t *testing.T) {
	oldDiscover := discoverLLMIntegrations
	discoverLLMIntegrations = func(context.Context, *http.Client, *slog.Logger) modelsources.LLMIntegrationDiscoveryResult {
		return modelsources.LLMIntegrationDiscoveryResult{Found: true, Integrations: []*modelsources.LLMIntegrationConfig{{
			Name: "llm", Host: "llm.int.exe.xyz", URL: "https://llm.int.exe.xyz",
			Models: []modelsources.IntegrationModel{{ID: "google/gemini-3.8-flash", Provider: "google", NativeID: "gemini-3.8-flash", APIs: []string{"gemini_generate_content"}}},
		}}}
	}
	t.Cleanup(func() { discoverLLMIntegrations = oldDiscover })
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("FIREWORKS_API_KEY", "")
	configPath := filepath.Join(t.TempDir(), "shelley.json")
	if err := os.WriteFile(configPath, []byte(`{"llm_gateway":"https://gateway.example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct{ key, want string }{
		{"", "llm.int.exe.xyz"},
		{"gemini-key", "$GEMINI_API_KEY"},
	} {
		t.Setenv("GEMINI_API_KEY", tt.key)
		cfg, err := buildLLMConfig(GlobalConfig{ConfigPath: configPath}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := findBuiltModelSource(cfg.Models, "gemini-3.8-flash"); got != tt.want {
			t.Errorf("GEMINI_API_KEY=%q: gemini-3.8-flash source = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestBuildLLMConfigDoesNotOverrideMetadataEnvironment(t *testing.T) {
	oldEnv, err := exeenv.New("https", "exe.xyz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exeenv.Configure(oldEnv) })
	metadataEnv, err := exeenv.New("http", "exe.cloud")
	if err != nil {
		t.Fatal(err)
	}
	exeenv.Configure(metadataEnv)

	oldDiscover := discoverLLMIntegrations
	discoverLLMIntegrations = func(context.Context, *http.Client, *slog.Logger) modelsources.LLMIntegrationDiscoveryResult {
		env, err := exeenv.Current()
		if err != nil {
			t.Fatal(err)
		}
		if got := env.ReflectionURL(); got != metadataEnv.ReflectionURL() {
			t.Fatalf("discovery environment = %q", got)
		}
		return modelsources.LLMIntegrationDiscoveryResult{}
	}
	t.Cleanup(func() { discoverLLMIntegrations = oldDiscover })

	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("FIREWORKS_API_KEY", "")

	configPath := filepath.Join(t.TempDir(), "shelley.json")
	config := `{
		"llm_gateway": "https://gateway.example.com/",
		"default_model": "configured-default",
		"exe_environment": {"scheme": "https", "box_host": "example.test"}
	}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := buildLLMConfig(GlobalConfig{ConfigPath: configPath}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "configured-default" {
		t.Fatalf("DefaultModel = %q", cfg.DefaultModel)
	}
	gatewayFound := false
	for _, model := range cfg.Models {
		gatewayFound = gatewayFound || model.Source == "exe.dev gateway"
	}
	if !gatewayFound {
		t.Fatal("parsed llm_gateway was not reused")
	}
}

// TestGlobalFlagsDefaultModelEmptyByDefault guards the production fix: the
// -default-model flag must default to empty so shelley.json's default_model
// takes effect on VMs (where the systemd unit passes no -default-model). A
// non-empty flag default would shadow the config field via the
// defaultModel=="" guard in buildLLMModelSources. This parses through a real
// FlagSet rather than constructing GlobalConfig directly, so reverting the
// flag default fails here.
func TestGlobalFlagsDefaultModelEmptyByDefault(t *testing.T) {
	var global GlobalConfig
	fs := flag.NewFlagSet("shelley", flag.ContinueOnError)
	registerGlobalFlags(fs, &global)
	if err := fs.Parse([]string{"serve"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if global.DefaultModel != "" {
		t.Fatalf("DefaultModel default = %q, want empty", global.DefaultModel)
	}

	// An explicit flag is still captured.
	global = GlobalConfig{}
	fs = flag.NewFlagSet("shelley", flag.ContinueOnError)
	registerGlobalFlags(fs, &global)
	if err := fs.Parse([]string{"-default-model", "claude-opus-5", "serve"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if global.DefaultModel != "claude-opus-5" {
		t.Fatalf("DefaultModel = %q, want claude-opus-5", global.DefaultModel)
	}
}

// TestBuildLLMConfigDefaultModelPrecedence pins the default-model precedence
// that the shelley.json default-model mechanism relies on: an explicit
// -default-model flag wins, otherwise shelley.json's default_model is honored.
// The flag value is obtained via real flag parsing (see
// TestGlobalFlagsDefaultModelEmptyByDefault) so on VMs, where the flag is
// unset, the shelley.json value is what takes effect.
func TestBuildLLMConfigDefaultModelPrecedence(t *testing.T) {
	oldDiscover := discoverLLMIntegrations
	discoverLLMIntegrations = func(context.Context, *http.Client, *slog.Logger) modelsources.LLMIntegrationDiscoveryResult {
		return modelsources.LLMIntegrationDiscoveryResult{}
	}
	t.Cleanup(func() { discoverLLMIntegrations = oldDiscover })

	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("FIREWORKS_API_KEY", "")

	configPath := filepath.Join(t.TempDir(), "shelley.json")
	config := `{"default_model": "gpt-5.6-sol"}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// parseGlobal registers and parses the global flags exactly as main does,
	// then points -config at our temp file. This ties the flag default to the
	// precedence assertion so neither can regress silently.
	parseGlobal := func(t *testing.T, args ...string) GlobalConfig {
		t.Helper()
		var global GlobalConfig
		fs := flag.NewFlagSet("shelley", flag.ContinueOnError)
		registerGlobalFlags(fs, &global)
		if err := fs.Parse(append([]string{"-config", configPath}, args...)); err != nil {
			t.Fatalf("parse: %v", err)
		}
		return global
	}

	// Flag unset (the VM case): shelley.json's default_model wins.
	cfg, err := buildLLMConfig(parseGlobal(t, "serve"), logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "gpt-5.6-sol" {
		t.Fatalf("flag unset: DefaultModel = %q, want gpt-5.6-sol", cfg.DefaultModel)
	}

	// Flag explicitly set: it overrides shelley.json.
	cfg, err = buildLLMConfig(parseGlobal(t, "-default-model", "claude-opus-5", "serve"), logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "claude-opus-5" {
		t.Fatalf("flag set: DefaultModel = %q, want claude-opus-5", cfg.DefaultModel)
	}
}

func TestModelsCommandDefaultIDMatchesServerVisibility(t *testing.T) {
	modelList := []models.Built{
		{ID: "first-ready"},
		{ID: "configured"},
		{ID: "predictable"},
	}
	tests := []struct {
		name            string
		configured      string
		predictableOnly bool
		want            string
	}{
		{name: "configured model", configured: "configured", want: "configured"},
		{name: "missing configured model", configured: "missing", want: "first-ready"},
		{name: "predictable hidden normally", configured: "predictable", want: "first-ready"},
		{name: "predictable only", configured: "configured", predictableOnly: true, want: "predictable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelsCommandDefaultID(tt.configured, modelList, tt.predictableOnly); got != tt.want {
				t.Fatalf("modelsCommandDefaultID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildLLMConfigIgnoresInvalidLegacyExeEnvironment(t *testing.T) {
	oldDiscover := discoverLLMIntegrations
	discoveryCalls := 0
	discoverLLMIntegrations = func(context.Context, *http.Client, *slog.Logger) modelsources.LLMIntegrationDiscoveryResult {
		discoveryCalls++
		return modelsources.LLMIntegrationDiscoveryResult{}
	}
	t.Cleanup(func() { discoverLLMIntegrations = oldDiscover })

	configPath := filepath.Join(t.TempDir(), "shelley.json")
	if err := os.WriteFile(configPath, []byte(`{"exe_environment":{"scheme":"ftp","box_host":"example.test"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := buildLLMConfig(GlobalConfig{ConfigPath: configPath}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatalf("legacy exe_environment should be ignored: %v", err)
	}
	if discoveryCalls != 1 {
		t.Fatalf("discovery called %d times, want 1", discoveryCalls)
	}
}

func TestToolModelsHideUnknownIntegrationModelsButKeepCustomModels(t *testing.T) {
	provider := &tieredModelProvider{
		ids: []string{"gpt-5.6-sol", "upstream-only", "my-custom-model"},
		infos: map[string]*models.ModelInfo{
			"gpt-5.6-sol":     {Source: "llm.int.exe.xyz"},
			"upstream-only":   {Source: "llm.int.exe.xyz"},
			"my-custom-model": {Source: models.SourceCustomLabel},
		},
	}

	got := setupToolSetConfig(nil, provider).BuildAvailableModels("")
	want := []claudetool.AvailableModel{
		{Name: "gpt-5.6-sol", ID: "gpt-5.6-sol"},
		{Name: "my-custom-model", ID: "my-custom-model"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("available tool models = %+v, want %+v", got, want)
	}
}

func TestToolModelsKeepBothLunaGenerationsAvailable(t *testing.T) {
	provider := &tieredModelProvider{
		ids: []string{"gpt-6-luna", "gpt-5.6-luna", "gpt-6-sol", "gpt-5.6-sol"},
	}

	got := setupToolSetConfig(nil, provider).BuildAvailableModels("")
	want := []claudetool.AvailableModel{
		{Name: "gpt-6-luna", ID: "gpt-6-luna"},
		{Name: "gpt-5.6-luna", ID: "gpt-5.6-luna"},
		{Name: "gpt-6-sol", ID: "gpt-6-sol"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("available tool models = %+v, want %+v", got, want)
	}
}

func TestToolModelsPreferParentChatGPTSubscriptionForDuplicates(t *testing.T) {
	const (
		parentModel  = "gpt-6-astra@llm-sub-oai"
		subscription = "https://llm-sub-oai.int.exe.xyz"
	)
	provider := &tieredModelProvider{
		ids: []string{
			parentModel,
			"gpt-6-luna@llm-sub-oai",
			"gpt-6-luna",
			"gpt-6-sol",
			"claude-opus-5.5",
			"custom-luna-endpoint",
			"upstream-only@llm-sub-oai",
		},
		infos: map[string]*models.ModelInfo{
			parentModel:                 {Mode: "chatgpt", BaseURL: subscription, Provider: models.ProviderOpenAI, APIModelName: "gpt-6-astra"},
			"gpt-6-luna@llm-sub-oai":    {Mode: "chatgpt", BaseURL: subscription, Provider: models.ProviderOpenAI, APIModelName: "gpt-6-luna"},
			"gpt-6-luna":                {Mode: "managed", BaseURL: "https://llm.int.exe.xyz", Provider: models.ProviderOpenAI, APIModelName: "gpt-6-luna"},
			"gpt-6-sol":                 {Mode: "managed", BaseURL: "https://llm.int.exe.xyz", Provider: models.ProviderOpenAI, APIModelName: "gpt-6-sol"},
			"claude-opus-5.5":           {Provider: models.ProviderAnthropic, APIModelName: "claude-opus-5.5"},
			"custom-luna-endpoint":      {Source: models.SourceCustomLabel, Provider: models.ProviderOpenAI, APIModelName: "gpt-6-luna"},
			"upstream-only@llm-sub-oai": {Mode: "chatgpt", BaseURL: subscription, Provider: models.ProviderOpenAI, APIModelName: "upstream-only"},
		},
	}

	// Agents see catalog names; the subscription route is chosen when used.
	got := setupToolSetConfig(nil, provider).BuildAvailableModels(parentModel)
	want := []claudetool.AvailableModel{
		{Name: "gpt-6-astra", ID: parentModel},
		{Name: "gpt-6-luna", ID: "gpt-6-luna@llm-sub-oai"},
		{Name: "gpt-6-sol", ID: "gpt-6-sol"},
		{Name: "claude-opus-5.5", ID: "claude-opus-5.5"},
		{Name: "custom-luna-endpoint", ID: "custom-luna-endpoint"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("available models = %+v, want %+v", got, want)
	}
}

func TestToolModelsKeepDistinctNativeModelsWithSameCatalogID(t *testing.T) {
	const (
		parentModel  = "gpt-6-astra@subscription"
		subscription = "https://subscription.int.exe.xyz"
	)
	provider := &tieredModelProvider{
		ids: []string{parentModel, "gpt-6-luna@subscription", "gpt-6-luna"},
		infos: map[string]*models.ModelInfo{
			parentModel:               {Mode: "chatgpt", BaseURL: subscription, Provider: models.ProviderOpenAI, APIModelName: "gpt-6-astra"},
			"gpt-6-luna@subscription": {Mode: "chatgpt", BaseURL: subscription, Provider: models.ProviderOpenAI, APIModelName: "subscription-luna"},
			"gpt-6-luna":              {Mode: "managed", BaseURL: "https://gateway.int.exe.xyz", Provider: models.ProviderOpenAI, APIModelName: "gpt-6-luna"},
		},
	}

	// Both routes stay selectable, so their names must stay distinct.
	got := setupToolSetConfig(nil, provider).BuildAvailableModels(parentModel)
	want := []claudetool.AvailableModel{
		{Name: "gpt-6-astra", ID: parentModel},
		{Name: "gpt-6-luna@subscription", ID: "gpt-6-luna@subscription"},
		{Name: "gpt-6-luna", ID: "gpt-6-luna"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("available models = %+v, want %+v", got, want)
	}
}

func TestToolModelsDoNotPreferHiddenSubscriptionDuplicate(t *testing.T) {
	const (
		parentModel  = "gpt-6-astra@subscription"
		subscription = "https://subscription.int.exe.xyz"
	)
	provider := &tieredModelProvider{
		ids: []string{parentModel, "future-luna@subscription", "gpt-6-luna"},
		infos: map[string]*models.ModelInfo{
			parentModel:                {Mode: "chatgpt", BaseURL: subscription, Provider: models.ProviderOpenAI, APIModelName: "gpt-6-astra"},
			"future-luna@subscription": {Mode: "chatgpt", BaseURL: subscription, Provider: models.ProviderOpenAI, APIModelName: "gpt-6-luna"},
			"gpt-6-luna":               {Mode: "managed", BaseURL: "https://gateway.int.exe.xyz", Provider: models.ProviderOpenAI, APIModelName: "gpt-6-luna"},
		},
	}

	got := setupToolSetConfig(nil, provider).BuildAvailableModels(parentModel)
	want := []claudetool.AvailableModel{
		{Name: "gpt-6-astra", ID: parentModel},
		{Name: "gpt-6-luna", ID: "gpt-6-luna"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("available models = %+v, want %+v", got, want)
	}
}

func TestToolModelsPreferUnqualifiedParentSubscription(t *testing.T) {
	const (
		parentModel  = "gpt-6-astra"
		subscription = "https://llm.int.exe.xyz"
	)
	provider := &tieredModelProvider{
		ids: []string{
			parentModel,
			"gpt-6-luna",
			"gpt-6-luna@gateway",
			"gpt-6-sol",
		},
		infos: map[string]*models.ModelInfo{
			parentModel:          {Mode: "chatgpt", BaseURL: subscription, Provider: models.ProviderOpenAI, APIModelName: "gpt-6-astra"},
			"gpt-6-luna":         {Mode: "chatgpt", BaseURL: subscription, Provider: models.ProviderOpenAI, APIModelName: "gpt-6-luna"},
			"gpt-6-luna@gateway": {Mode: "managed", BaseURL: "https://gateway.int.exe.xyz", Provider: models.ProviderOpenAI, APIModelName: "gpt-6-luna"},
			"gpt-6-sol":          {Mode: "managed", BaseURL: "https://gateway.int.exe.xyz", Provider: models.ProviderOpenAI, APIModelName: "gpt-6-sol"},
		},
	}

	got := setupToolSetConfig(nil, provider).BuildAvailableModels(parentModel)
	want := []claudetool.AvailableModel{
		{Name: parentModel, ID: parentModel},
		{Name: "gpt-6-luna", ID: "gpt-6-luna"},
		{Name: "gpt-6-sol", ID: "gpt-6-sol"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("available models = %+v, want %+v", got, want)
	}
}

func TestToolModelsDoNotPreferSubscriptionForOtherParents(t *testing.T) {
	const parentModel = "gpt-6-astra"
	provider := &tieredModelProvider{
		ids: []string{parentModel, "gpt-6-luna", "gpt-6-luna@subscription"},
		infos: map[string]*models.ModelInfo{
			parentModel:               {Mode: "managed", BaseURL: "https://gateway.int.exe.xyz", Provider: models.ProviderOpenAI, APIModelName: "gpt-6-astra"},
			"gpt-6-luna":              {Mode: "managed", BaseURL: "https://gateway.int.exe.xyz", Provider: models.ProviderOpenAI, APIModelName: "gpt-6-luna"},
			"gpt-6-luna@subscription": {Mode: "chatgpt", BaseURL: "https://subscription.int.exe.xyz", Provider: models.ProviderOpenAI, APIModelName: "gpt-6-luna"},
		},
	}

	got := setupToolSetConfig(nil, provider).BuildAvailableModels(parentModel)
	want := []claudetool.AvailableModel{
		{Name: parentModel, ID: parentModel},
		{Name: "gpt-6-luna", ID: "gpt-6-luna"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("available models = %+v, want %+v", got, want)
	}
}

func TestToolModelsKeepGlobalChoicesWhenSubscriptionEndpointUnknown(t *testing.T) {
	const parentModel = "upstream-parent@subscription"
	provider := &tieredModelProvider{
		ids: []string{parentModel, "gpt-6-luna", "claude-opus-5.5"},
		infos: map[string]*models.ModelInfo{
			parentModel:       {Mode: "chatgpt"},
			"gpt-6-luna":      {Mode: "managed", BaseURL: "https://gateway.int.exe.xyz"},
			"claude-opus-5.5": {Provider: models.ProviderAnthropic, APIModelName: "claude-opus-5.5"},
		},
	}

	got := setupToolSetConfig(nil, provider).BuildAvailableModels(parentModel)
	want := []claudetool.AvailableModel{
		{Name: "gpt-6-luna", ID: "gpt-6-luna"},
		{Name: "claude-opus-5.5", ID: "claude-opus-5.5"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("available models = %+v, want %+v", got, want)
	}
}

func findBuiltModelSource(built []models.Built, id string) string {
	for _, model := range built {
		if model.ID == id {
			return model.Source
		}
	}
	return ""
}

func TestCLICommands(t *testing.T) {
	t.Parallel()
	binary := shelleyBinary(t)

	t.Run("help message", func(t *testing.T) {
		cmd := exec.Command(binary)
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("Expected command to fail with no arguments")
		}
		outputStr := string(output)
		if !strings.Contains(outputStr, "Commands:") {
			t.Errorf("Expected help message, got: %s", outputStr)
		}
	})

	t.Run("serve flag parsing", func(t *testing.T) {
		// Test that serve command accepts flags - we can't easily test the full server
		// but we can test that it doesn't immediately error on flag parsing
		cmd := exec.Command(binary, "serve", "-h")
		output, err := cmd.CombinedOutput()
		// With flag package, -h should cause exit with code 2
		if err != nil {
			if exitError, ok := err.(*exec.ExitError); ok {
				if exitError.ExitCode() == 2 {
					// This is expected for -h flag
					outputStr := string(output)
					if !strings.Contains(outputStr, "-port") || !strings.Contains(outputStr, "-db") {
						t.Errorf("Expected serve help to show -port and -db flags, got: %s", outputStr)
					}
					if !strings.Contains(outputStr, "-systemd-activation") {
						t.Errorf("Expected serve help to show -systemd-activation flag, got: %s", outputStr)
					}
					return
				}
			}
		}
		// If no error or different error, that's also fine for this basic test
		t.Logf("Serve command output: %s", string(output))
	})
}

func TestSystemdListenerErrors(t *testing.T) {
	for _, k := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES", "LISTEN_PIDFDID"} {
		t.Setenv(k, "") // restored after the test
	}

	t.Run("no LISTEN_FDS", func(t *testing.T) {
		os.Unsetenv("LISTEN_FDS")
		os.Unsetenv("LISTEN_PID")
		_, err := systemdListeners()
		if err == nil {
			t.Fatal("Expected error when LISTEN_FDS not set")
		}
		if !strings.Contains(err.Error(), "LISTEN_FDS not set") {
			t.Errorf("Unexpected error message: %v", err)
		}
	})

	t.Run("wrong LISTEN_PID", func(t *testing.T) {
		os.Setenv("LISTEN_FDS", "1")
		os.Setenv("LISTEN_PID", "99999999") // Unlikely to match our PID
		os.Setenv("LISTEN_FDNAMES", "shelley.socket")
		os.Setenv("LISTEN_PIDFDID", "1")
		_, err := systemdListeners()
		if err == nil {
			t.Fatal("Expected error when LISTEN_PID doesn't match")
		}
		if !strings.Contains(err.Error(), "does not match current PID") {
			t.Errorf("Unexpected error message: %v", err)
		}
		// Children (agent shells, an sshd they start) must not think
		// systemd activated them, whatever became of the listeners.
		for _, k := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES", "LISTEN_PIDFDID"} {
			if v, ok := os.LookupEnv(k); ok {
				t.Errorf("%s=%q still set", k, v)
			}
		}
	})

	t.Run("invalid LISTEN_FDS", func(t *testing.T) {
		os.Setenv("LISTEN_FDS", "notanumber")
		os.Unsetenv("LISTEN_PID")
		_, err := systemdListeners()
		if err == nil {
			t.Fatal("Expected error when LISTEN_FDS is invalid")
		}
		if !strings.Contains(err.Error(), "invalid LISTEN_FDS") {
			t.Errorf("Unexpected error message: %v", err)
		}
	})

	t.Run("zero LISTEN_FDS", func(t *testing.T) {
		os.Setenv("LISTEN_FDS", "0")
		os.Unsetenv("LISTEN_PID")
		_, err := systemdListeners()
		if err == nil {
			t.Fatal("Expected error when LISTEN_FDS is 0")
		}
		if !strings.Contains(err.Error(), "expected at least 1") {
			t.Errorf("Unexpected error message: %v", err)
		}
	})
}

func TestSystemdListenerIntegration(t *testing.T) {
	// This test simulates what systemd does: create a listener, get the fd,
	// and pass it to a child process via environment and fd inheritance.
	// Since we can't easily test in-process (fd 3 is likely already in use),
	// we test by spawning a subprocess.

	t.Parallel()
	tempDir := t.TempDir()
	binary := shelleyBinary(t)

	// Create a listener on a random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	// Get the file descriptor from the listener
	tcpListener := listener.(*net.TCPListener)
	file, err := tcpListener.File()
	if err != nil {
		listener.Close()
		t.Fatalf("Failed to get file from listener: %v", err)
	}
	listener.Close() // Close original listener, file still holds the socket

	// Create a temp database for the test
	dbPath := filepath.Join(tempDir, "test.db")

	// Spawn shelley with the file descriptor as fd 3
	// Note: We don't set LISTEN_PID here because we don't know the child PID yet.
	// The systemdListeners function handles missing LISTEN_PID gracefully.
	cmd := exec.Command(binary, "-db", dbPath, "serve", "-systemd-activation", "-socket", "none")
	// Build environment without LISTEN_PID (will be inherited from parent otherwise)
	// and add LISTEN_FDS=1
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "LISTEN_PID=") {
			env = append(env, e)
		}
	}
	env = append(env, "LISTEN_FDS=1")
	cmd.Env = env
	cmd.ExtraFiles = []*os.File{file} // This makes the file fd 3 in the child
	var stderrBuf, stdoutBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	// Start the process
	if err := cmd.Start(); err != nil {
		file.Close()
		t.Fatalf("Failed to start shelley: %v", err)
	}
	file.Close() // Close our copy after child inherits it

	// Poll until the server answers on the inherited socket.
	var resp *http.Response
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err = client.Get(fmt.Sprintf("http://127.0.0.1:%d/version", port))
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Kill the server
	cmd.Process.Kill()
	cmd.Wait()

	if err != nil {
		t.Fatalf("Failed to connect to server: %v\nstdout: %s\nstderr: %s", err, stdoutBuf.String(), stderrBuf.String())
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("Unexpected status code %d, body: %s", resp.StatusCode, body)
	}
}

// TestSystemdActivatedCLISocket passes shelley the CLI's Unix socket as well
// as the web one, as exeuntu's shelley.socket does, so that `shelley client`
// can reach a shelley that is not running yet: the kernel queues the
// connection until shelley starts and accepts it. A client started alongside
// the server must get through without retrying, and the socket must outlive
// the server, since it belongs to systemd.
func TestSystemdActivatedCLISocket(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	binary := shelleyBinary(t)

	listenerFile := func(network, address string) *os.File {
		t.Helper()
		ln, err := net.Listen(network, address)
		if err != nil {
			t.Fatal(err)
		}
		if u, ok := ln.(*net.UnixListener); ok {
			u.SetUnlinkOnClose(false) // the socket is systemd's, not ours
		}
		f, err := ln.(interface{ File() (*os.File, error) }).File()
		if err != nil {
			t.Fatal(err)
		}
		ln.Close()
		t.Cleanup(func() { f.Close() })
		return f
	}
	web := listenerFile("tcp", "127.0.0.1:0")
	socketPath := filepath.Join(tempDir, "shelley.sock")
	cli := listenerFile("unix", socketPath)

	env := []string{"LISTEN_FDS=2"}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "LISTEN_") && !strings.HasPrefix(e, "SHELLEY_SOCKET=") {
			env = append(env, e)
		}
	}
	server := exec.Command(binary, "-db", filepath.Join(tempDir, "test.db"), "serve", "-systemd-activation", "-socket", socketPath)
	server.Env = env
	server.ExtraFiles = []*os.File{web, cli} // fds 3 and 4
	var serverOut bytes.Buffer
	server.Stdout = &serverOut
	server.Stderr = &serverOut
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Process.Kill(); server.Wait() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := exec.CommandContext(ctx, binary, "client", "list")
	client.Env = append(env, "SHELLEY_SOCKET="+socketPath)
	if out, err := client.CombinedOutput(); err != nil {
		t.Fatalf("shelley client list: %v\n%s\nserver output:\n%s", err, out, serverOut.String())
	}

	if err := server.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := server.Wait(); err != nil {
		t.Fatalf("server exit: %v\n%s", err, serverOut.String())
	}
	if _, err := os.Stat(socketPath); err != nil {
		t.Errorf("activated socket after shutdown: %v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(tempDir, "shelley-*.sock")); len(matches) > 0 {
		t.Errorf("server made its own socket %v instead of using the activated one", matches)
	}
}

// TestSystemdActivationIgnoresExtraListeners: a unit may hold more sockets
// than shelley serves, such as the web port on both IPv4 and IPv6. shelley
// serves the first and ignores the rest, as it always has, rather than not
// starting at all.
func TestSystemdActivationIgnoresExtraListeners(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	binary := shelleyBinary(t)

	var files []*os.File
	var webAddr string
	for range 2 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if webAddr == "" {
			webAddr = ln.Addr().String()
		}
		f, err := ln.(*net.TCPListener).File()
		if err != nil {
			t.Fatal(err)
		}
		ln.Close()
		t.Cleanup(func() { f.Close() })
		files = append(files, f)
	}

	env := []string{"LISTEN_FDS=2"}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "LISTEN_") {
			env = append(env, e)
		}
	}
	server := exec.Command(binary, "-db", filepath.Join(tempDir, "test.db"), "serve", "-systemd-activation", "-socket", filepath.Join(tempDir, "shelley.sock"))
	server.Env = env
	server.ExtraFiles = files // fds 3 and 4
	var serverOut bytes.Buffer
	server.Stdout = &serverOut
	server.Stderr = &serverOut
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- server.Wait() }()
	t.Cleanup(func() { server.Process.Kill(); <-exited })

	// The listener is live before shelley starts, so the request waits in
	// the kernel's queue until shelley serves it, unless shelley exits.
	got := make(chan error, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Get("http://" + webAddr + "/version")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("status %s", resp.Status)
			}
		}
		got <- err
	}()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("GET /version: %v\nserver output:\n%s", err, serverOut.String())
		}
	case err := <-exited:
		exited <- err
		t.Fatalf("server exited: %v\n%s", err, serverOut.String())
	}
}

func TestToolModelsStayWithinSavedAccount(t *testing.T) {
	parent := models.ProviderRouteID("work", "gpt-6.1-sol")
	sibling := models.ProviderRouteID("work", "gpt-6-luna")
	other := models.ProviderRouteID("personal", "gpt-6-luna")
	provider := &tieredModelProvider{
		ids: []string{"gpt-6.1-sol", sibling, other}, // parent is no longer listed
		infos: map[string]*models.ModelInfo{
			parent:  {SourceID: "work", Source: "Same label", APIModelName: "gpt-6.1-sol"},
			sibling: {SourceID: "work", Source: "Same label", APIModelName: "gpt-6-luna"},
			other:   {SourceID: "personal", Source: "Same label", APIModelName: "gpt-6-luna"},
		},
	}
	choices := setupToolSetConfig(nil, provider).BuildAvailableModels
	if got := choices(parent); !slices.Equal(got, []claudetool.AvailableModel{{Name: "gpt-6.1-sol", ID: parent}, {Name: "gpt-6-luna", ID: sibling}}) {
		t.Fatalf("saved choices crossed accounts or lost parent: %+v", got)
	}
	if got := choices("gpt-6.1-sol"); !slices.Equal(got, []claudetool.AvailableModel{{Name: "gpt-6.1-sol", ID: "gpt-6.1-sol"}}) {
		t.Fatalf("legacy choices gained saved-account routes: %+v", got)
	}
}
