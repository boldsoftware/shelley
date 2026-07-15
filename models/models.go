package models

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/gem"
	"shelley.exe.dev/llm/llmhttp"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/loop"
	"shelley.exe.dev/models/modelsdev"
)

// Provider identifies an LLM upstream API family.
type Provider string

const (
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
	ProviderFireworks Provider = "fireworks"
	ProviderGemini    Provider = "gemini"
	ProviderXAI       Provider = "xai"
	ProviderBuiltIn   Provider = "builtin"
)

// SourceCustomLabel is the label used for custom (DB-backed) models.
const SourceCustomLabel = "custom"

// Provider-default BARE base URLs (origins). API-protocol path suffixes
// like "/v1" or "/v1/messages" are appended by the per-API-type service
// factory in Model.Build — keeping that knowledge out of the catalog
// and out of any caller that hands a baseURL to Build.
const (
	DefaultAnthropicBaseURL = "https://api.anthropic.com"
	DefaultOpenAIBaseURL    = "https://api.openai.com"
	DefaultFireworksBaseURL = "https://api.fireworks.ai/inference"
	DefaultGeminiBaseURL    = "https://generativelanguage.googleapis.com"
	DefaultXAIBaseURL       = "https://api.x.ai"
)

// APIType identifies the wire protocol Shelley uses to talk to a model.
// Multiple APIType values can share a Provider (notably OpenAI: Responses
// API vs. Chat Completions).
type APIType string

const (
	APITypeAnthropicMessages APIType = "anthropic-messages"
	APITypeOpenAIResponses   APIType = "openai-responses"
	APITypeOpenAIChat        APIType = "openai-chat-completions"
	APITypeGemini            APIType = "gemini"
	APITypeBuiltIn           APIType = "builtin"
)

// Model is one entry in Shelley's catalog of built-in models.
type Model struct {
	// ID is the user-facing identifier.
	ID string

	// Provider is the upstream API family.
	Provider Provider

	// Description is a human-readable description.
	Description string

	// Tags is a comma-separated list of tags (e.g. "slug").
	Tags string

	// APIModelName is the model name sent on the wire (e.g. "claude-opus-4-7").
	// Also used to match against an LLM integration's /v1/models allow-list.
	APIModelName string

	// APIType identifies the wire protocol used to talk to this model.
	APIType APIType

	// DefaultBaseURL is the base URL the provider package uses when no
	// explicit URL is configured. Shown in `shelley models` so users can
	// see exactly which endpoint each model will be reached at.
	DefaultBaseURL string

	// Build constructs an llm.Service for this model given a BARE base
	// URL (origin + any non-API prefix, e.g. "https://llm.int.exe.xyz"
	// or "" for the provider package default), an API key, and an HTTP
	// client. The function is responsible for appending its own
	// API-protocol path ("/v1", "/v1/messages", "/v1beta", ...) — the
	// caller never encodes those.
	Build func(baseURL, apiKey string, httpc *http.Client) llm.Service
}

// Built is a ready-to-use model, shaped to mirror a row in the custom
// models database table. The Manager treats built-in and custom models
// uniformly via this struct.
type Built struct {
	ID          string
	DisplayName string
	Provider    Provider
	Source      string // human-readable origin ("exe.dev gateway", "$ANTHROPIC_API_KEY", "custom", ...)
	Tags        string
	Service     llm.Service

	// APIType is the wire protocol used to talk to this model.
	APIType APIType

	// BaseURL is the resolved upstream base URL (after applying any source
	// override on top of the catalog's DefaultBaseURL).
	BaseURL string
}

// Config holds runtime configuration for the Manager. Built-in models
// are passed in pre-materialized; custom models are loaded from DB.
type Config struct {
	// Models is the set of ready-to-use built-in models, in display order.
	Models []Built

	Logger *slog.Logger

	// DB holds custom models; optional.
	DB *db.DB

	// HTTPC is the shared HTTP client used to back custom models loaded
	// from DB. If nil, a default llmhttp client is created.
	HTTPC *http.Client
}

// --- Catalog ---------------------------------------------------------------

// antSvc / oaiResponsesSvc / oaiChatSvc / gemSvc are factories for the
// per-provider llm.Service constructors used by Model.Build.
//
// The `baseURL` parameter is a BARE origin/prefix with NO API-protocol
// path on it (e.g. "https://llm.int.exe.xyz" or "" for the package
// default). Each factory knows its own protocol's path suffix and
// appends it before constructing the service. Keeping that knowledge
// inside the factory means modelsources never has to encode
// per-API-type paths like "/v1" or "/v1/messages".
func antSvc(modelName string) func(baseURL, apiKey string, httpc *http.Client) llm.Service {
	return func(baseURL, apiKey string, httpc *http.Client) llm.Service {
		s := &ant.Service{APIKey: apiKey, Model: modelName, HTTPC: httpc, ThinkingLevel: llm.ThinkingLevelMedium, SupportsImages_: true}
		if baseURL != "" {
			s.URL = baseURL + "/v1/messages"
		}
		return s
	}
}

func oaiResponsesSvc(model oai.Model) func(baseURL, apiKey string, httpc *http.Client) llm.Service {
	return oaiResponsesSvcNamed(model, "openai")
}

func oaiResponsesSvcNamed(model oai.Model, providerName string) func(baseURL, apiKey string, httpc *http.Client) llm.Service {
	return func(baseURL, apiKey string, httpc *http.Client) llm.Service {
		s := &oai.ResponsesService{Model: model, APIKey: apiKey, HTTPC: httpc, ThinkingLevel: llm.ThinkingLevelMedium, ProviderName: providerName}
		if baseURL != "" {
			s.ModelURL = baseURL + "/v1"
		}
		return s
	}
}

func oaiChatSvc(model oai.Model, providerName string) func(baseURL, apiKey string, httpc *http.Client) llm.Service {
	return func(baseURL, apiKey string, httpc *http.Client) llm.Service {
		s := &oai.Service{Model: model, APIKey: apiKey, HTTPC: httpc, ProviderName: providerName}
		if baseURL != "" {
			s.ModelURL = baseURL + "/v1"
		}
		return s
	}
}

func gemSvc(modelName string) func(baseURL, apiKey string, httpc *http.Client) llm.Service {
	return func(baseURL, apiKey string, httpc *http.Client) llm.Service {
		s := &gem.Service{APIKey: apiKey, Model: modelName, HTTPC: httpc, SupportsImages_: true}
		if baseURL != "" {
			s.URL = baseURL + "/v1beta"
		}
		return s
	}
}

// All returns all available models in Shelley.
//
// Order is significant: it is the display order in the model picker, not a
// statement about which model is the default. Default() is chosen separately,
// so do not assume All()[0] is the default.
//
// Models are organized by "family" — the usual notion of a model lineage from
// one provider/trainer (the "Opus" line, the "GPT-5" line, and so on).
//
// Only the newest release in a family holds that family's flagship slot near
// the top of the list. Older releases in the same family are obviated by the
// newer one and drop into the secondary group lower down (they stay selectable,
// just deprioritized). A different family is never obviated by a higher-numbered
// release of another family, so each family keeps its own flagship slot.
//
// There is one surprising wrinkle: Opus <= 4.6 and Opus >= 4.7 are treated as
// two separate families even though both are "Opus". The reason is the
// tokenizer. Opus 4.7 introduced a new tokenizer (inherited by 4.8) that emits
// more tokens for the same text — the per-token rates are identical across 4.6,
// 4.7, and 4.8, but the same prompt costs more under 4.7/4.8. That difference is
// large enough that we keep 4.6 in its own flagship slot rather than letting it
// be obviated by 4.7/4.8.
//
// When adding a newer release of an existing family, put it in the family's
// flagship slot and move the prior release down into the secondary group.
func All() []Model {
	return []Model{
		{
			ID: "claude-opus-4.8", Provider: ProviderAnthropic,
			Description: "Claude Opus 4.8 (default)", APIModelName: ant.Claude48Opus,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude48Opus),
		},
		{
			ID: "claude-fable-5", Provider: ProviderAnthropic,
			Description: "Claude Fable 5", APIModelName: ant.ClaudeFable5,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.ClaudeFable5),
		},
		{
			ID: "gpt-5.6-sol", Provider: ProviderOpenAI,
			Description: "GPT-5.6 Sol", APIModelName: oai.GPT56Sol.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT56Sol),
		},
		{
			ID: "gpt-5.6-terra", Provider: ProviderOpenAI,
			Description: "GPT-5.6 Terra", APIModelName: oai.GPT56Terra.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT56Terra),
		},
		{
			ID: "gpt-5.6-luna", Provider: ProviderOpenAI,
			Description: "GPT-5.6 Luna", APIModelName: oai.GPT56Luna.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT56Luna),
		},
		{
			ID: "gpt-5.5", Provider: ProviderOpenAI,
			Description: "GPT-5.5", APIModelName: oai.GPT55.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT55),
		},
		{
			ID: "claude-opus-4.6", Provider: ProviderAnthropic,
			Description: "Claude Opus 4.6", APIModelName: ant.Claude46Opus,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude46Opus),
		},
		{
			ID: "glm-5.2-fireworks", Provider: ProviderFireworks,
			Description: "GLM-5.2 on Fireworks", APIModelName: oai.GLM52Fireworks.ModelName,
			APIType: APITypeOpenAIChat, DefaultBaseURL: DefaultFireworksBaseURL,
			Build: oaiChatSvc(oai.GLM52Fireworks, "fireworks"),
		},
		{
			ID: "glm-5.1-fireworks", Provider: ProviderFireworks,
			Description: "GLM-5.1 on Fireworks", APIModelName: oai.GLM51Fireworks.ModelName,
			APIType: APITypeOpenAIChat, DefaultBaseURL: DefaultFireworksBaseURL,
			Build: oaiChatSvc(oai.GLM51Fireworks, "fireworks"),
		},
		{
			ID: "gemini-3-pro", Provider: ProviderGemini,
			Description: "Gemini 3 Pro", APIModelName: "gemini-3-pro-preview",
			APIType: APITypeGemini, DefaultBaseURL: DefaultGeminiBaseURL,
			Build: gemSvc("gemini-3-pro-preview"),
		},
		{
			ID: "grok-4.5", Provider: ProviderXAI,
			Description: "Grok 4.5", APIModelName: oai.Grok45.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultXAIBaseURL,
			Build: oaiResponsesSvcNamed(oai.Grok45, "xai"),
		},
		{
			ID: "kimi-k2.6-fireworks", Provider: ProviderFireworks,
			Description: "Kimi K2.6 on Fireworks", APIModelName: oai.KimiK26Fireworks.ModelName,
			APIType: APITypeOpenAIChat, DefaultBaseURL: DefaultFireworksBaseURL,
			Build: oaiChatSvc(oai.KimiK26Fireworks, "fireworks"),
		},
		{
			ID: "deepseek-v4-pro-fireworks", Provider: ProviderFireworks,
			Description: "DeepSeek V4 Pro on Fireworks", APIModelName: oai.DeepseekV4ProFireworks.ModelName,
			APIType: APITypeOpenAIChat, DefaultBaseURL: DefaultFireworksBaseURL,
			Build: oaiChatSvc(oai.DeepseekV4ProFireworks, "fireworks"),
		},
		{
			ID: "claude-opus-4.7", Provider: ProviderAnthropic,
			Description: "Claude Opus 4.7", APIModelName: ant.Claude47Opus,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude47Opus),
		},
		{
			ID: "claude-opus-4.5", Provider: ProviderAnthropic,
			Description: "Claude Opus 4.5", APIModelName: ant.Claude45Opus,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude45Opus),
		},
		{
			ID: "claude-sonnet-5", Provider: ProviderAnthropic,
			Description: "Claude Sonnet 5", APIModelName: ant.Claude5Sonnet,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude5Sonnet),
		},
		{
			ID: "claude-sonnet-4.6", Provider: ProviderAnthropic,
			Description: "Claude Sonnet 4.6", APIModelName: ant.Claude46Sonnet,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude46Sonnet),
		},
		{
			ID: "claude-sonnet-4.5", Provider: ProviderAnthropic,
			Description: "Claude Sonnet 4.5", APIModelName: ant.Claude45Sonnet,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude45Sonnet),
		},
		{
			ID: "claude-haiku-4.5", Provider: ProviderAnthropic, Tags: "slug-backup",
			Description: "Claude Haiku 4.5", APIModelName: ant.Claude45Haiku,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude45Haiku),
		},
		{
			ID: "gpt-5.4", Provider: ProviderOpenAI,
			Description: "GPT-5.4", APIModelName: oai.GPT54.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT54),
		},
		{
			ID: "gpt-5.4-mini", Provider: ProviderOpenAI,
			Description: "GPT-5.4 mini", APIModelName: oai.GPT54Mini.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT54Mini),
		},
		{
			ID: "gpt-5.4-nano", Provider: ProviderOpenAI,
			Description: "GPT-5.4 nano", APIModelName: oai.GPT54Nano.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT54Nano),
		},
		{
			ID: "gpt-5.3-codex", Provider: ProviderOpenAI,
			Description: "GPT-5.3 Codex", APIModelName: oai.GPT53Codex.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT53Codex),
		},
		{
			ID: "gpt-5.2-codex", Provider: ProviderOpenAI,
			Description: "GPT-5.2 Codex", APIModelName: oai.GPT52Codex.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT52Codex),
		},
		{
			ID: "gemini-3-flash", Provider: ProviderGemini,
			Description: "Gemini 3 Flash", APIModelName: "gemini-3-flash-preview",
			APIType: APITypeGemini, DefaultBaseURL: DefaultGeminiBaseURL,
			Build: gemSvc("gemini-3-flash-preview"),
		},
		{
			ID: "deepseek-v4-flash-fireworks", Provider: ProviderFireworks,
			Description: "DeepSeek V4 Flash on Fireworks", APIModelName: oai.DeepseekV4FlashFireworks.ModelName,
			APIType: APITypeOpenAIChat, DefaultBaseURL: DefaultFireworksBaseURL,
			Build: oaiChatSvc(oai.DeepseekV4FlashFireworks, "fireworks"),
		},
		{
			ID: "qwen3.6-plus-fireworks", Provider: ProviderFireworks,
			Description: "Qwen 3.6 Plus on Fireworks", APIModelName: oai.Qwen36PlusFireworks.ModelName,
			APIType: APITypeOpenAIChat, DefaultBaseURL: DefaultFireworksBaseURL,
			Build: oaiChatSvc(oai.Qwen36PlusFireworks, "fireworks"),
		},
		{
			ID: "gpt-oss-20b-fireworks", Provider: ProviderFireworks, Tags: "slug",
			Description: "GPT-OSS 20B on Fireworks", APIModelName: oai.GPTOSS20B.ModelName,
			APIType: APITypeOpenAIChat, DefaultBaseURL: DefaultFireworksBaseURL,
			Build: oaiChatSvc(oai.GPTOSS20B, "fireworks"),
		},
		{
			ID: "predictable", Provider: ProviderBuiltIn,
			Description:    "Deterministic test model (no API key)",
			APIType:        APITypeBuiltIn,
			DefaultBaseURL: "-",
			Build:          func(url, apiKey string, httpc *http.Client) llm.Service { return loop.NewPredictableService() },
		},
	}
}

// ByID returns the model with the given ID, or nil if not found.
func ByID(id string) *Model {
	for _, m := range All() {
		if m.ID == id {
			return &m
		}
	}
	return nil
}

// IDs returns all catalog model IDs.
func IDs() []string {
	models := All()
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}

// Default returns the default catalog model.
func Default() Model {
	if m := ByID("claude-opus-4.8"); m != nil {
		return *m
	}
	return All()[0]
}

// --- Manager ---------------------------------------------------------------

// Manager owns the live set of LLM services for a Shelley server.
type Manager struct {
	mu         sync.RWMutex
	services   map[string]serviceEntry
	modelOrder []string
	logger     *slog.Logger
	db         *db.DB
	httpc      *http.Client
}

type serviceEntry struct {
	service     llm.Service
	provider    Provider
	modelID     string
	source      string
	displayName string
	tags        string
	userAgent   string
	baseURL     string
	apiType     APIType
}

// ConfigInfo is an optional interface that services can implement to provide configuration details for logging
type ConfigInfo interface {
	ConfigDetails() map[string]string
}

// loggingService wraps an llm.Service with request/usage logging.
type loggingService struct {
	service   llm.Service
	logger    *slog.Logger
	modelID   string
	provider  Provider
	userAgent string
}

func (l *loggingService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	start := time.Now()
	ctx = llmhttp.WithModelID(ctx, l.modelID)
	ctx = llmhttp.WithProvider(ctx, string(l.provider))
	if l.userAgent != "" {
		ctx = llmhttp.WithUserAgent(ctx, l.userAgent)
	}
	response, err := l.service.Do(ctx, request)
	durationSeconds := time.Since(start).Seconds()

	if err != nil {
		if l.logger != nil {
			logAttrs := []any{"model", l.modelID, "duration_seconds", durationSeconds}
			if configProvider, ok := l.service.(ConfigInfo); ok {
				for k, v := range configProvider.ConfigDetails() {
					logAttrs = append(logAttrs, k, v)
				}
			}
			logAttrs = append(logAttrs, "error", err)
			l.logger.Error("LLM request failed", logAttrs...)
		}
		return response, err
	}

	if l.logger != nil {
		logAttrs := []any{"model", l.modelID, "duration_seconds", durationSeconds}
		if !response.Usage.IsZero() {
			logAttrs = append(
				logAttrs,
				"input_tokens", response.Usage.InputTokens,
				"output_tokens", response.Usage.OutputTokens,
				"cost_usd", response.Usage.CostUSD,
			)
			if response.Usage.CacheCreationInputTokens > 0 {
				logAttrs = append(logAttrs, "cache_creation_input_tokens", response.Usage.CacheCreationInputTokens)
			}
			if response.Usage.CacheReadInputTokens > 0 {
				logAttrs = append(logAttrs, "cache_read_input_tokens", response.Usage.CacheReadInputTokens)
			}
		}
		l.logger.Info("LLM request completed", logAttrs...)
	}
	return response, err
}

func (l *loggingService) Provider() string        { return l.service.Provider() }
func (l *loggingService) TokenContextWindow() int { return l.service.TokenContextWindow() }
func (l *loggingService) MaxImageDimension() int  { return l.service.MaxImageDimension() }
func (l *loggingService) MaxImageBytes() int      { return l.service.MaxImageBytes() }

func (l *loggingService) UseSimplifiedPatch() bool {
	if sp, ok := l.service.(llm.SimplifiedPatcher); ok {
		return sp.UseSimplifiedPatch()
	}
	return false
}

func (l *loggingService) SupportsServerSideWebSearch() bool {
	type capable interface{ SupportsServerSideWebSearch() bool }
	if c, ok := l.service.(capable); ok {
		return c.SupportsServerSideWebSearch()
	}
	return false
}

func (l *loggingService) SupportsImages() bool { return l.service.SupportsImages() }
func (l *loggingService) SupportsReasoning() bool {
	return llm.SupportsReasoning(l.service)
}

func (l *loggingService) SupportedReasoningLevels() []llm.ThinkingLevel {
	return llm.SupportedReasoningLevels(l.service)
}

// DefaultReasoningLevel forwards the wrapped service's default reasoning level
// so the llm.DefaultReasoner assertion survives the logging wrapper.
func (l *loggingService) DefaultReasoningLevel() string {
	return llm.ServiceDefaultReasoningLevel(l.service)
}

// NewManager registers the supplied built-in models, then loads custom
// models from cfg.DB.
func NewManager(cfg *Config) (*Manager, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	httpc := cfg.HTTPC
	if httpc == nil {
		httpc = llmhttp.NewClient(nil)
	}
	m := &Manager{
		services: map[string]serviceEntry{},
		logger:   cfg.Logger,
		db:       cfg.DB,
		httpc:    httpc,
	}

	m.registerBuiltModelsLocked(cfg.Models)

	if err := m.loadCustomModels(); err != nil && cfg.Logger != nil {
		cfg.Logger.Warn("Failed to load custom models", "error", err)
	}
	return m, nil
}

func (m *Manager) registerBuiltModelsLocked(built []Built) {
	for _, b := range built {
		dn := b.DisplayName
		if dn == "" {
			dn = b.ID
		}
		m.services[b.ID] = serviceEntry{
			service:     b.Service,
			provider:    b.Provider,
			modelID:     b.ID,
			source:      b.Source,
			displayName: dn,
			tags:        b.Tags,
			baseURL:     b.BaseURL,
			apiType:     b.APIType,
		}
		m.modelOrder = append(m.modelOrder, b.ID)
		if m.logger != nil {
			m.logger.Info("Registered model", "id", b.ID, "source", b.Source)
		}
	}
}

func (m *Manager) customModelRows() ([]generated.Model, error) {
	if m.db == nil {
		return nil, nil
	}
	return m.db.GetModels(context.Background())
}

func (m *Manager) loadCustomModels() error {
	dbModels, err := m.customModelRows()
	if err != nil {
		return err
	}
	m.loadCustomModelsLocked(dbModels)
	return nil
}

func (m *Manager) loadCustomModelsLocked(dbModels []generated.Model) {
	for _, model := range dbModels {
		if _, exists := m.services[model.ModelID]; exists {
			continue
		}
		svc := m.createServiceFromModel(&model)
		if svc == nil {
			continue
		}
		m.services[model.ModelID] = serviceEntry{
			service:     svc,
			provider:    Provider(model.ProviderType),
			modelID:     model.ModelID,
			source:      SourceCustomLabel,
			displayName: model.DisplayName,
			tags:        model.Tags,
			userAgent:   model.UserAgent,
		}
		m.modelOrder = append(m.modelOrder, model.ModelID)
	}
}

// RefreshCustomModels reloads custom models from the database. Call this
// after adding or removing custom models via the UI.
func (m *Manager) RefreshCustomModels() error {
	dbModels, err := m.customModelRows()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	newOrder := make([]string, 0, len(m.modelOrder))
	for _, id := range m.modelOrder {
		entry, ok := m.services[id]
		if ok && entry.source != SourceCustomLabel {
			newOrder = append(newOrder, id)
		} else {
			delete(m.services, id)
		}
	}
	m.modelOrder = newOrder
	m.loadCustomModelsLocked(dbModels)
	return nil
}

// RefreshBuiltModels replaces the non-custom models with a freshly discovered
// built-in/catalog set, then re-applies DB-backed custom models.
func (m *Manager) RefreshBuiltModels(built []Built) error {
	dbModels, err := m.customModelRows()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.services = map[string]serviceEntry{}
	m.modelOrder = nil
	m.registerBuiltModelsLocked(built)
	m.loadCustomModelsLocked(dbModels)
	return nil
}

// GetService returns the LLM service for modelID, wrapped with logging.
func (m *Manager) GetService(modelID string) (llm.Service, error) {
	m.mu.RLock()
	entry, ok := m.services[modelID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unsupported model: %s", modelID)
	}
	return &loggingService{
		service:   entry.service,
		logger:    m.logger,
		modelID:   entry.modelID,
		provider:  entry.provider,
		userAgent: entry.userAgent,
	}, nil
}

func (m *Manager) GetAvailableModels() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]string, len(m.modelOrder))
	copy(result, m.modelOrder)
	return result
}

func (m *Manager) HasModel(modelID string) bool {
	m.mu.RLock()
	_, ok := m.services[modelID]
	m.mu.RUnlock()
	return ok
}

// ModelInfo contains display name, tags, source, base URL, and API type for a model.
type ModelInfo struct {
	DisplayName string
	Tags        string
	Source      string
	BaseURL     string
	APIType     string
}

func (m *Manager) GetModelInfo(modelID string) *ModelInfo {
	m.mu.RLock()
	entry, ok := m.services[modelID]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	return &ModelInfo{DisplayName: entry.displayName, Tags: entry.tags, Source: entry.source, BaseURL: entry.baseURL, APIType: string(entry.apiType)}
}

type reasoningMapping struct {
	level  llm.ThinkingLevel
	effort string
}

type reasoningService struct {
	llm.Service
	supported     bool
	levels        []llm.ThinkingLevel
	mapping       map[llm.ThinkingLevel]reasoningMapping
	defaultSource llm.ThinkingLevel
}

func (s *reasoningService) SupportsReasoning() bool  { return s.supported }
func (s *reasoningService) UseSimplifiedPatch() bool { return llm.UseSimplifiedPatch(s.Service) }
func (s *reasoningService) SupportsServerSideWebSearch() bool {
	type capable interface{ SupportsServerSideWebSearch() bool }
	c, ok := s.Service.(capable)
	return ok && c.SupportsServerSideWebSearch()
}

func (s *reasoningService) SupportedReasoningLevels() []llm.ThinkingLevel {
	return append([]llm.ThinkingLevel(nil), s.levels...)
}

func (s *reasoningService) DefaultReasoningLevel() string {
	if !s.supported {
		return "off"
	}
	if mapped, ok := s.mapping[s.defaultSource]; ok {
		if mapped.level != llm.ThinkingLevelDefault {
			return mapped.level.Name()
		}
		return mapped.effort
	}
	return llm.ServiceDefaultReasoningLevel(s.Service)
}

func (s *reasoningService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	copyReq := *req
	if !s.supported {
		copyReq.ThinkingLevel = llm.ThinkingLevelOff
	} else if copyReq.ThinkingLevel == llm.ThinkingLevelDefault {
		if mapped, ok := s.mapping[s.defaultSource]; ok {
			copyReq.ThinkingLevel = mapped.level
			copyReq.ReasoningEffort = mapped.effort
		}
	} else if len(s.mapping) > 0 {
		mapped, ok := s.mapping[copyReq.ThinkingLevel]
		if !ok {
			return nil, fmt.Errorf("reasoning level %q is not supported by this model", copyReq.ThinkingLevel.Name())
		}
		copyReq.ThinkingLevel = mapped.level
		copyReq.ReasoningEffort = mapped.effort
	}
	return s.Service.Do(ctx, &copyReq)
}

func wrapReasoningService(service llm.Service, model *generated.Model) llm.Service {
	return WrapReasoningConfig(service, model.Endpoint, model.ModelName, model.ReasoningSupport, model.ReasoningMap)
}

// WrapReasoningConfig applies custom-model capability and level mapping.
func WrapReasoningConfig(service llm.Service, endpoint, modelName, support, rawMap string) llm.Service {
	supported := ResolveSupportsReasoning(endpoint, modelName, support)
	mapping, levels := parseReasoningMap(rawMap)
	defaultSource := llm.ParseThinkingLevel(llm.ServiceDefaultReasoningLevel(service))
	return &reasoningService{Service: service, supported: supported, levels: levels, mapping: mapping, defaultSource: defaultSource}
}

func parseReasoningMap(raw string) (map[llm.ThinkingLevel]reasoningMapping, []llm.ThinkingLevel) {
	if raw == "" {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, nil
	}
	mapping := make(map[llm.ThinkingLevel]reasoningMapping, len(values))
	levels := make([]llm.ThinkingLevel, 0, len(values))
	for _, level := range []llm.ThinkingLevel{llm.ThinkingLevelOff, llm.ThinkingLevelMinimal, llm.ThinkingLevelLow, llm.ThinkingLevelMedium, llm.ThinkingLevelHigh, llm.ThinkingLevelXHigh} {
		mappedName, ok := values[level.Name()]
		if !ok || mappedName == "" {
			continue
		}
		mapped := llm.ParseThinkingLevel(mappedName)
		if mapped == llm.ThinkingLevelDefault && mappedName != "default" {
			// Preserve the generic level as a safe fallback for providers that do
			// not consume verbatim efforts; providers that do consume it use effort.
			mapping[level] = reasoningMapping{level: level, effort: mappedName}
		} else {
			mapping[level] = reasoningMapping{level: mapped}
		}
		levels = append(levels, level)
	}
	return mapping, levels
}

// createServiceFromModel creates an LLM service from a database model configuration.
func (m *Manager) createServiceFromModel(model *generated.Model) llm.Service {
	supportsImages := ResolveSupportsImages(model.Endpoint, model.ModelName, model.ImageSupport)
	var service llm.Service
	switch model.ProviderType {
	case "anthropic":
		service = &ant.Service{
			APIKey:          model.ApiKey,
			URL:             model.Endpoint,
			Model:           model.ModelName,
			HTTPC:           m.httpc,
			ThinkingLevel:   llm.ThinkingLevelMedium,
			SupportsImages_: supportsImages,
		}
	case "openai":
		service = &oai.Service{
			APIKey:   model.ApiKey,
			ModelURL: model.Endpoint,
			Model: oai.Model{
				UserName:           "",
				ModelName:          model.ModelName,
				TextVerbosity:      "",
				URL:                model.Endpoint,
				APIKeyEnv:          "",
				IsReasoningModel:   false,
				UseSimplifiedPatch: false,
				SupportsImages:     supportsImages,
			},
			MaxTokens:       int(model.MaxTokens),
			HTTPC:           m.httpc,
			ProviderName:    "openai",
			ReasoningEffort: model.ReasoningEffort,
		}
	case "openai-responses":
		service = &oai.ResponsesService{
			APIKey:   model.ApiKey,
			ModelURL: model.Endpoint,
			Model: oai.Model{
				UserName:           "",
				ModelName:          model.ModelName,
				TextVerbosity:      "",
				URL:                model.Endpoint,
				APIKeyEnv:          "",
				IsReasoningModel:   false,
				UseSimplifiedPatch: false,
				SupportsImages:     supportsImages,
			},
			MaxTokens:       int(model.MaxTokens),
			HTTPC:           m.httpc,
			ThinkingLevel:   llm.ThinkingLevelMedium,
			ReasoningEffort: model.ReasoningEffort,
			ProviderName:    "openai",
		}
	case "gemini":
		service = &gem.Service{
			APIKey:          model.ApiKey,
			URL:             model.Endpoint,
			Model:           model.ModelName,
			HTTPC:           m.httpc,
			ReasoningEffort: model.ReasoningEffort,
			SupportsImages_: supportsImages,
		}
	default:
		if m.logger != nil {
			m.logger.Error("Unknown provider type for model", "model_id", model.ModelID, "provider_type", model.ProviderType)
		}
		return nil
	}
	return wrapReasoningService(service, model)
}

// ResolveSupportsImages turns a stored image_support value ("auto"|"yes"|"no")
// into a SupportsImages bool. "auto" is resolved from the model's endpoint URL
// and name; unknown models default to allowing images.
func ResolveSupportsImages(endpoint, modelName, imageSupport string) bool {
	switch imageSupport {
	case "yes":
		return true
	case "no":
		return false
	case "auto", "":
		supported, found := modelsdev.LookupImageSupport(endpoint, modelName)
		if !found {
			return true
		}
		return supported
	default:
		return true
	}
}

// ResolveSupportsReasoning turns a stored reasoning_support value
// ("auto"|"yes"|"no") into a capability boolean. Unknown models default to
// supporting reasoning so custom endpoints remain configurable.
func ResolveSupportsReasoning(endpoint, modelName, reasoningSupport string) bool {
	switch reasoningSupport {
	case "yes":
		return true
	case "no":
		return false
	case "auto", "":
		supported, found := modelsdev.LookupReasoningSupport(endpoint, modelName)
		if !found {
			return true
		}
		return supported
	default:
		return true
	}
}
