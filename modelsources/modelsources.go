// Package modelsources composes built-in Shelley models from credential
// origins (exe.dev LLM integrations, the exe.dev gateway, provider env
// vars, and the predictable test service) and materializes them into a
// flat []models.Built that the server can register directly.
package modelsources

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"time"

	"shelley.exe.dev/llm/llmhttp"
	"shelley.exe.dev/models"
)

// providerConn is the connection configuration for one upstream provider
// reachable from a single Source.
//
// `baseURL` is a BARE origin/prefix (e.g. "https://llm.int.exe.xyz")
// with NO API-protocol path on it. The per-API-type service factory in
// models.Model.Build appends "/v1", "/v1/messages", "/v1beta", etc. so
// sources never have to encode protocol details. Empty falls back to
// the catalog's DefaultBaseURL, which is also a bare origin.
type providerConn struct {
	baseURL string
	apiKey  string // "implicit" when credentials are injected at the network edge
}

// Source is one origin from which built-in Shelley models can be
// materialized into the server's Manager. Sources are evaluated in
// order; the first to claim an ID wins.
type Source struct {
	// label is the default human-readable origin shown in the UI.
	label string

	// idSuffix is appended to each materialized model ID (e.g. "@llm2")
	// to disambiguate when multiple sources serve overlapping models.
	idSuffix string

	// providers is the per-provider connection config. A nil entry means
	// this source does not serve that provider.
	providers map[models.Provider]*providerConn

	// providerLabels overrides label on a per-provider basis (used for
	// the env source where each provider has its own env-var name).
	providerLabels map[models.Provider]string

	// allowedAPIModels, when non-empty, restricts this source to models
	// whose APIModelName is in the set (used for LLM integrations).
	allowedAPIModels map[string]bool
}

func (s *Source) labelFor(p models.Provider) string {
	if l, ok := s.providerLabels[p]; ok {
		return l
	}
	return s.label
}

// Predictable returns a Source that materializes only the predictable
// test model. Always safe to include in any deployment.
func Predictable() Source {
	return Source{
		label:     "builtin",
		providers: map[models.Provider]*providerConn{models.ProviderBuiltIn: {}},
	}
}

// Gateway returns a Source for the exe.dev gateway. The gateway serves
// Anthropic, OpenAI, Fireworks, and xAI but not Gemini; Gemini models must
// come from an env-var or LLM-integration source. Any non-empty
// explicit per-provider key overrides the gateway's implicit credential.
func Gateway(gatewayURL, anthropicKey, openAIKey, fireworksKey, xaiKey string) Source {
	key := func(k string) string {
		if k != "" {
			return k
		}
		return "implicit"
	}
	return Source{
		label: "exe.dev gateway",
		providers: map[models.Provider]*providerConn{
			models.ProviderAnthropic: {baseURL: gatewayURL + "/anthropic", apiKey: key(anthropicKey)},
			models.ProviderOpenAI:    {baseURL: gatewayURL + "/openai", apiKey: key(openAIKey)},
			models.ProviderFireworks: {baseURL: gatewayURL + "/fireworks/inference", apiKey: key(fireworksKey)},
			models.ProviderXAI:       {baseURL: gatewayURL + "/xai", apiKey: key(xaiKey)},
		},
		providerLabels: explicitEnvLabels(anthropicKey, openAIKey, fireworksKey, xaiKey),
	}
}

// Env returns a Source for direct-to-provider env-var credentials. Only
// providers with a non-empty key are included.
func Env(anthropicKey, openAIKey, geminiKey, fireworksKey, xaiKey string) Source {
	prov := map[models.Provider]*providerConn{}
	labels := map[models.Provider]string{}
	add := func(p models.Provider, k, env string) {
		if k == "" {
			return
		}
		prov[p] = &providerConn{apiKey: k}
		labels[p] = "$" + env
	}
	add(models.ProviderAnthropic, anthropicKey, "ANTHROPIC_API_KEY")
	add(models.ProviderOpenAI, openAIKey, "OPENAI_API_KEY")
	add(models.ProviderGemini, geminiKey, "GEMINI_API_KEY")
	add(models.ProviderFireworks, fireworksKey, "FIREWORKS_API_KEY")
	add(models.ProviderXAI, xaiKey, "XAI_API_KEY")
	return Source{label: "env", providers: prov, providerLabels: labels}
}

// LLMIntegration returns a Source backed by one exe.dev "llm"
// integration. idSuffix, when non-empty, is appended to each
// materialized model ID to disambiguate multiple integrations.
func LLMIntegration(integ *LLMIntegrationConfig, idSuffix string) Source {
	allowed := make(map[string]bool, len(integ.Models))
	for _, m := range integ.Models {
		if apiModelName := m.apiModelName(); apiModelName != "" {
			allowed[apiModelName] = true
		}
	}
	return Source{
		label:    integ.Host,
		idSuffix: idSuffix,
		providers: map[models.Provider]*providerConn{
			models.ProviderAnthropic: {baseURL: integ.URL, apiKey: "implicit"},
			models.ProviderOpenAI:    {baseURL: integ.URL, apiKey: "implicit"},
			models.ProviderFireworks: {baseURL: integ.URL, apiKey: "implicit"},
			models.ProviderXAI:       {baseURL: integ.URL, apiKey: "implicit"},
			// Gemini: the integration's /v1/models is OpenAI-shaped and does
			// not expose Gemini-native endpoints. Omit.
		},
		allowedAPIModels: allowed,
	}
}

// explicitEnvLabels returns providerLabels that overlay env-var-style
// labels on top of a gateway source for any provider whose key was set
// explicitly. Gemini is omitted because the gateway never serves it.
func explicitEnvLabels(anthropic, openAI, fireworks, xai string) map[models.Provider]string {
	labels := map[models.Provider]string{}
	if anthropic != "" {
		labels[models.ProviderAnthropic] = "$ANTHROPIC_API_KEY"
	}
	if openAI != "" {
		labels[models.ProviderOpenAI] = "$OPENAI_API_KEY"
	}
	if fireworks != "" {
		labels[models.ProviderFireworks] = "$FIREWORKS_API_KEY"
	}
	if xai != "" {
		labels[models.ProviderXAI] = "$XAI_API_KEY"
	}
	return labels
}

// Build walks the catalog × sources and produces ready-to-use
// models.Built values. Order: each Source in turn (preserving catalog
// order within), first to claim an ID wins.
func Build(catalog []models.Model, sources []Source, httpc *http.Client, logger *slog.Logger) []models.Built {
	if logger == nil {
		logger = slog.Default()
	}
	if httpc == nil {
		httpc = llmhttp.NewClient(nil)
	}
	var out []models.Built
	seen := map[string]bool{}
	for _, src := range sources {
		for _, m := range catalog {
			conn := src.providers[m.Provider]
			if conn == nil {
				continue
			}
			if src.allowedAPIModels != nil && !src.allowedAPIModels[m.APIModelName] {
				continue
			}
			id := m.ID + src.idSuffix
			if seen[id] {
				continue
			}
			seen[id] = true
			svc := m.Build(conn.baseURL, conn.apiKey, httpc)
			label := src.labelFor(m.Provider)
			baseURL := conn.baseURL
			if baseURL == "" {
				baseURL = m.DefaultBaseURL
			}
			out = append(out, models.Built{
				ID:          id,
				DisplayName: id,
				Provider:    m.Provider,
				Tags:        m.Tags,
				Source:      label,
				Service:     svc,
				APIType:     m.APIType,
				BaseURL:     baseURL,
			})
			logger.Debug("Materialized model", "id", id, "source", label)
		}
	}
	return out
}

// --- exe.dev LLM integration discovery ------------------------------------

// integrationDiscoveryTimeout bounds each HTTP call made during exe.dev
// integration discovery. Generous so a slow upstream during models.json
// can't silently drop the integration.
const integrationDiscoveryTimeout = 30 * time.Second

var exeDevMarkerPath = "/exe.dev"

// IntegrationModel is one entry from an LLM integration's models.json catalog.
type IntegrationModel struct {
	ID       string   `json:"id"`
	Provider string   `json:"provider,omitempty"`
	NativeID string   `json:"native_id,omitempty"`
	APIs     []string `json:"apis,omitempty"`
}

func (m IntegrationModel) apiModelName() string {
	if m.NativeID != "" {
		return m.NativeID
	}
	return m.ID
}

// LLMIntegrationConfig describes one exe.dev "llm" integration that
// proxies requests to upstream LLM providers using credentials injected
// at the network edge.
type LLMIntegrationConfig struct {
	// Name is the integration name (e.g. "llm").
	Name string

	// Host is the integration hostname (e.g. "llm.int.exe.xyz"), shown
	// to users in source labels.
	Host string

	// URL is the integration base URL (no trailing slash, no path).
	URL string

	// Models is the set of models the integration serves, in the order
	// returned by models.json.
	Models []IntegrationModel
}

// LLMIntegrationDiscoveryResult distinguishes "reflection found no LLM
// integrations" from "reflection found LLM integrations, but none produced a
// usable catalog." Callers use Found to avoid falling back to the gateway when
// a VM has an explicit LLM integration attached.
type LLMIntegrationDiscoveryResult struct {
	Found        bool
	Integrations []*LLMIntegrationConfig
}

type reflectionIntegration struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Team bool   `json:"team,omitempty"`
}

func (i reflectionIntegration) host() string {
	if i.Team {
		return fmt.Sprintf("%s.team.exe.xyz", i.Name)
	}
	return fmt.Sprintf("%s.int.exe.xyz", i.Name)
}

type reflectionIntegrationsResponse struct {
	Integrations []reflectionIntegration `json:"integrations"`
}

type llmIntegrationModelCatalog struct {
	SchemaVersion int                `json:"schema_version"`
	Models        []IntegrationModel `json:"models"`
}

// DiscoverLLMIntegrations looks up every integration of type "llm" via
// the reflection endpoint and returns the resolved configs, sorted by name.
// Found is false when we are not on an exe.dev VM, reflection is unreachable,
// or no "llm" integration is registered. An integration whose models.json
// fetch fails is logged and skipped; other integrations are still returned.
func DiscoverLLMIntegrations(ctx context.Context, httpc *http.Client, logger *slog.Logger) LLMIntegrationDiscoveryResult {
	if logger == nil {
		logger = slog.Default()
	}
	if _, err := os.Stat(exeDevMarkerPath); err != nil {
		return LLMIntegrationDiscoveryResult{}
	}
	if httpc == nil {
		httpc = http.DefaultClient
	}

	var ints reflectionIntegrationsResponse
	if !fetchJSON(ctx, httpc, "https://reflection.int.exe.xyz/integrations", &ints) {
		return LLMIntegrationDiscoveryResult{}
	}

	var llmIntegrations []reflectionIntegration
	for _, i := range ints.Integrations {
		if i.Type == "llm" && i.Name != "" {
			llmIntegrations = append(llmIntegrations, i)
		}
	}
	if len(llmIntegrations) == 0 {
		return LLMIntegrationDiscoveryResult{}
	}
	slices.SortFunc(llmIntegrations, func(a, b reflectionIntegration) int {
		if c := cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.host(), b.host())
	})

	result := LLMIntegrationDiscoveryResult{Found: true}
	for _, integ := range llmIntegrations {
		host := integ.host()
		base := "https://" + host
		var catalog llmIntegrationModelCatalog
		if !fetchJSON(ctx, httpc, base+"/models.json", &catalog) {
			logger.Warn("LLM integration discovery: models.json fetch failed; skipping", "name", integ.Name, "host", host)
			continue
		}
		models := integrationModelsFromCatalog(catalog)
		if len(models) == 0 {
			logger.Warn("LLM integration discovery: models.json returned no supported models; skipping", "name", integ.Name, "host", host)
			continue
		}
		result.Integrations = append(result.Integrations, &LLMIntegrationConfig{
			Name:   integ.Name,
			Host:   host,
			URL:    base,
			Models: models,
		})
		logger.Info("Discovered exe.dev LLM integration", "name", integ.Name, "host", host, "models", len(models))
	}
	return result
}

func integrationModelsFromCatalog(catalog llmIntegrationModelCatalog) []IntegrationModel {
	if catalog.SchemaVersion != 1 {
		return nil
	}
	var out []IntegrationModel
	for _, model := range catalog.Models {
		if model.apiModelName() == "" || !integrationModelSupportedByShelley(model) {
			continue
		}
		out = append(out, model)
	}
	return out
}

func integrationModelSupportedByShelley(model IntegrationModel) bool {
	switch model.Provider {
	case string(models.ProviderAnthropic):
		return slices.Contains(model.APIs, "anthropic_messages")
	case string(models.ProviderOpenAI):
		return slices.Contains(model.APIs, "openai_responses") || slices.Contains(model.APIs, "openai_chat")
	case string(models.ProviderFireworks):
		return slices.Contains(model.APIs, "openai_chat")
	case string(models.ProviderXAI):
		return slices.Contains(model.APIs, "openai_responses") || slices.Contains(model.APIs, "openai_chat")
	default:
		return false
	}
}

// fetchJSON GETs url with a per-call timeout and decodes JSON into out.
// Returns false on any error (network, status, decode).
func fetchJSON(ctx context.Context, httpc *http.Client, url string, out any) bool {
	ctx, cancel := context.WithTimeout(ctx, integrationDiscoveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(resp.Body).Decode(out) == nil
}
