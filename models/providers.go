package models

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/models/modelsdev"
)

func ProviderModelEligible(endpoint, upstreamID string) bool {
	if known, ok := modelsdev.LookupProviderModel(endpoint, upstreamID); ok {
		return known.Chat
	}
	for _, model := range All() {
		if model.ID == upstreamID || model.APIModelName == upstreamID {
			return true
		}
	}
	return false
}

func ProviderRouteID(providerID, upstreamID string) string {
	return "provider:" + base64.RawURLEncoding.EncodeToString([]byte(providerID)) + ":" + base64.RawURLEncoding.EncodeToString([]byte(upstreamID))
}

func ParseProviderRouteID(id string) (providerID, upstreamID string, ok bool) {
	parts := strings.Split(id, ":")
	if len(parts) != 3 || parts[0] != "provider" {
		return "", "", false
	}
	provider, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(provider) == 0 {
		return "", "", false
	}
	upstream, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(upstream) == 0 {
		return "", "", false
	}
	return string(provider), string(upstream), true
}

// RefreshProviders loads the persisted snapshots; it never contacts a provider.
func (m *Manager) RefreshProviders() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return errors.New("provider storage is unavailable")
	}
	rows, err := m.db.GetModelProviders(context.Background())
	if err != nil {
		return err
	}
	fresh := make(map[string]serviceEntry)
	var order []string
	for _, row := range rows {
		for _, model := range row.Models {
			entry, err := m.providerEntry(row, model.ID)
			if err != nil {
				return err
			}
			fresh[entry.modelID] = entry
			order = append(order, entry.modelID)
		}
	}
	previous := append([]string(nil), m.modelOrder...)
	kept := m.modelOrder[:0]
	for _, id := range m.modelOrder {
		if m.services[id].sourceID != "" {
			delete(m.services, id)
		} else {
			kept = append(kept, id)
		}
	}
	m.modelOrder = append(kept, order...)
	for id, entry := range fresh {
		m.services[id] = entry
	}
	m.providersEnabled = true
	m.preserveProviderOrder(previous)
	return nil
}

func (m *Manager) preserveProviderOrder(previous []string) {
	remaining := make(map[string]bool, len(m.modelOrder))
	for _, id := range m.modelOrder {
		remaining[id] = true
	}
	order := make([]string, 0, len(m.modelOrder))
	for _, id := range previous {
		if remaining[id] {
			order = append(order, id)
			delete(remaining, id)
		}
	}
	for _, id := range m.modelOrder {
		if remaining[id] {
			order = append(order, id)
			delete(remaining, id)
		}
	}
	m.modelOrder = order
}

// Listed routes use the registry; unlisted routes still resolve their saved account.
func (m *Manager) lookupService(id string) (serviceEntry, error) {
	m.mu.RLock()
	entry, ok := m.services[id]
	m.mu.RUnlock()
	if ok {
		return entry, nil
	}
	return m.resolveProviderService(id)
}

func (m *Manager) resolveProviderService(id string) (serviceEntry, error) {
	m.mu.RLock()
	enabled := m.providersEnabled
	m.mu.RUnlock()
	source, wire, valid := ParseProviderRouteID(id)
	if !valid || !enabled || m.db == nil {
		return serviceEntry{}, fmt.Errorf("unsupported model: %s", id)
	}
	row, err := m.db.GetModelProvider(context.Background(), source)
	if errors.Is(err, sql.ErrNoRows) {
		return serviceEntry{}, fmt.Errorf("model %s belongs to a removed provider connection", id)
	}
	if err != nil {
		return serviceEntry{}, err
	}
	return m.providerEntry(*row, wire)
}

func (m *Manager) providerEntry(row db.ModelProvider, wire string) (serviceEntry, error) {
	model := generated.Model{
		ModelID: ProviderRouteID(row.ProviderID, wire), ModelName: wire,
		DisplayName: wire, ProviderType: row.ProviderType, Endpoint: row.Endpoint, ApiKey: row.APIKey,
		MaxTokens: 8192, ImageSupport: "no", ReasoningSupport: "no",
	}
	for _, item := range row.Models {
		if item.ID == wire && item.Name != "" {
			model.DisplayName = item.Name
			break
		}
	}
	known, exact := modelsdev.LookupProviderModel(row.Endpoint, wire)
	if exact {
		if known.Images {
			model.ImageSupport = "yes"
		}
		if known.OutputLimit > 0 {
			model.MaxTokens = int64(known.OutputLimit)
		}
		if known.Reasoning.Supported && len(known.Reasoning.Levels) > 0 {
			model.ReasoningSupport = "yes"
		}
	}
	var apiType APIType
	switch row.ProviderType {
	case "anthropic":
		model.Endpoint = strings.TrimRight(row.Endpoint, "/") + "/messages"
		apiType = APITypeAnthropicMessages
	case "openai":
		apiType = APITypeOpenAIChat
	case "openai-responses":
		apiType = APITypeOpenAIResponses
	default:
		return serviceEntry{}, fmt.Errorf("unsupported provider type: %s", row.ProviderType)
	}
	cfg, _ := customServiceConfig(&model, m.httpc)
	cfg.DisableReasoning = model.ReasoningSupport == "no"
	if catalog := ByID(wire); exact && catalog != nil && apiType == APITypeOpenAIResponses && catalog.APIType == apiType {
		if base, ok := catalog.Build("", "", m.httpc).(*oai.ResponsesService); ok {
			cfg.OpenAIModel.TextVerbosity, cfg.OpenAIModel.SupportsApplyPatch = base.Model.TextVerbosity, base.Model.SupportsApplyPatch
		}
	}
	service := wrapReasoningService(NewService(cfg), &model).(*reasoningService)
	service.disableWebSearch = true
	// Advertise levels without imposing a custom-model override map.
	service.levels = known.Reasoning.Levels

	label := row.DisplayName
	if label == "" {
		label = row.Endpoint
	}
	provider := ProviderOpenAI
	if row.ProviderType == "anthropic" {
		provider = ProviderAnthropic
	}
	return serviceEntry{
		service: service, provider: provider, modelID: model.ModelID, sourceID: row.ProviderID,
		serviceRevision: ConnectionRevision(row.ProviderType, row.Endpoint, row.APIKey, wire), source: label, displayName: model.DisplayName,
		baseURL: row.Endpoint, apiType: apiType, apiModelName: wire, releaseDate: known.ReleaseDate,
	}, nil
}
