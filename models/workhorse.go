package models

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/gem"
	"shelley.exe.dev/models/modelsdev"
)

type workhorseFamily struct {
	contains string
	excludes []string
}

// workhorseFamilies identifies cheap, fast model families per provider for
// background tasks. The newest available release in the family is selected.
var workhorseFamilies = map[Provider][]workhorseFamily{
	ProviderOpenAI:    {{contains: "luna"}},
	ProviderAnthropic: {{contains: "haiku"}},
	ProviderGemini: {
		{
			contains: "flash",
			excludes: []string{"lite", "image", "tts", "live", "omni"},
		},
		{contains: "gemini-4-hydrogen"},
	},
	// DeepSeek's Flash line. Matched on "deepseek-v4" minus Pro rather than
	// "deepseek-v4-flash" because the 4.1 release is "deepseek-v4.1-flash":
	// the dot-versioned IDs do not share a literal "v4-flash" substring.
	ProviderFireworks: {{contains: "deepseek-v4", excludes: []string{"pro"}}},
}

// workhorseFallbackProviders defines the deterministic order used when a
// conversation model's provider has no matching workhorse of its own
// (for example, xAI or a custom gateway model).
var workhorseFallbackProviders = []Provider{
	ProviderAnthropic,
	ProviderOpenAI,
	ProviderGemini,
	ProviderFireworks,
}

type workhorseService struct {
	primary         llm.Service
	manager         *Manager
	fallbackModelID string
}

func (s *workhorseService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	resp, err := s.do(ctx, s.primary, req)
	if err == nil || s.fallbackModelID == "" {
		return resp, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	fallback, err := s.manager.GetService(s.fallbackModelID)
	if err != nil {
		return nil, err
	}
	return s.do(ctx, fallback, req)
}

func (s *workhorseService) do(ctx context.Context, service llm.Service, req *llm.Request) (*llm.Response, error) {
	request := *req
	request.ThinkingLevel = workhorseThinkingLevel(service)
	request.ReasoningEffort = ""
	return service.Do(ctx, &request)
}

func workhorseThinkingLevel(service llm.Service) llm.ThinkingLevel {
	switch s := service.(type) {
	case *loggingService:
		return workhorseThinkingLevel(s.service)
	case *reasoningService:
		if isNativeGemini(s.Service) {
			if s.supported {
				if levels := s.SupportedReasoningLevels(); len(levels) > 0 {
					return llm.ClampThinkingLevel(llm.ThinkingLevelOff, levels)
				}
			}
			return llm.ThinkingLevelDefault
		}
		if s.supported && len(s.levels) > 0 {
			return llm.ClampThinkingLevel(llm.ThinkingLevelOff, s.levels)
		}
		return llm.ThinkingLevelOff
	case *gem.Service, *gem.InteractionsService:
		if levels := llm.SupportedReasoningLevels(s); llm.SupportsReasoning(s) && len(levels) > 0 {
			return llm.ClampThinkingLevel(llm.ThinkingLevelOff, levels)
		}
		return llm.ThinkingLevelDefault
	default:
		return llm.ThinkingLevelOff
	}
}

func isNativeGemini(service llm.Service) bool {
	switch s := service.(type) {
	case *gem.Service, *gem.InteractionsService:
		return true
	case *loggingService:
		return isNativeGemini(s.service)
	case *reasoningService:
		return isNativeGemini(s.Service)
	default:
		return false
	}
}

func (s *workhorseService) Provider() string       { return s.primary.Provider() }
func (s *workhorseService) MaxImageDimension() int { return s.primary.MaxImageDimension() }
func (s *workhorseService) MaxImageBytes() int     { return s.primary.MaxImageBytes() }
func (s *workhorseService) SupportsImages() bool   { return s.primary.SupportsImages() }

func (m *Manager) getWorkhorseService(conversationModelID string) (llm.Service, error) {
	modelID := m.GetWorkhorseModelID(conversationModelID)
	if modelID == "" {
		return nil, fmt.Errorf("no workhorse model available (conversation model %q)", conversationModelID)
	}
	return m.newWorkhorseService(modelID, conversationModelID)
}

func (m *Manager) GetWorkhorseServiceNoFallback(conversationModelID string) (llm.Service, error) {
	modelID, ok := m.findWorkhorseModelID(conversationModelID)
	if !ok {
		return nil, fmt.Errorf("no workhorse model available (conversation model %q)", conversationModelID)
	}
	return m.GetService(modelID)
}

func (m *Manager) newWorkhorseService(modelID, conversationModelID string) (llm.Service, error) {
	primary, err := m.GetService(modelID)
	if err != nil {
		if modelID == conversationModelID {
			return nil, err
		}
		primary, err = m.GetService(conversationModelID)
		if err != nil {
			return nil, err
		}
		modelID = conversationModelID
	}
	fallbackModelID := ""
	if modelID != conversationModelID {
		fallbackModelID = conversationModelID
	}
	return &workhorseService{
		primary:         primary,
		manager:         m,
		fallbackModelID: fallbackModelID,
	}, nil
}

func (m *Manager) GetWorkhorseModelID(conversationModelID string) string {
	if modelID, ok := m.findWorkhorseModelID(conversationModelID); ok {
		return modelID
	}
	return conversationModelID
}

func (m *Manager) findWorkhorseModelID(conversationModelID string) (string, bool) {
	info := m.GetModelInfo(conversationModelID)
	if info == nil || info.Provider == ProviderBuiltIn {
		return "", false
	}
	available := m.GetAvailableModels()
	available = append(available, conversationModelID)

	// 1. Prefer the conversation's model family on the same source.
	custom := isCustomWorkhorseRoute(info)
	family := info.Provider
	if custom {
		family, _ = classifyWorkhorseModel(workhorseMatchName(conversationModelID, info))
	}
	if family != "" {
		if bestID := m.bestWorkhorse(available, func(modelID string, candidate *ModelInfo) bool {
			if candidate.Provider != info.Provider || candidate.SourceID != info.SourceID ||
				isCustomWorkhorseRoute(candidate) != custom || (custom && candidate.BaseURL != info.BaseURL) {
				return false
			}
			candidateFamily, cheap := classifyWorkhorseModel(workhorseMatchName(modelID, candidate))
			return cheap && candidateFamily == family
		}); bestID != "" {
			return bestID, true
		}
	}

	// 2. Broad fallback: pick the newest available cheap workhorse from any
	// configured built-in provider, then any other available workhorse route.
	for _, provider := range workhorseFallbackProviders {
		if bestID := m.bestWorkhorse(available, func(modelID string, candidate *ModelInfo) bool {
			candidateFamily, cheap := classifyWorkhorseModel(workhorseMatchName(modelID, candidate))
			return candidate.Provider == provider &&
				!isCustomWorkhorseRoute(candidate) &&
				candidateFamily == provider && cheap
		}); bestID != "" {
			return bestID, true
		}
	}
	if bestID := m.bestWorkhorse(available, func(modelID string, candidate *ModelInfo) bool {
		if candidate.Provider == ProviderBuiltIn {
			return false
		}
		_, cheap := classifyWorkhorseModel(workhorseMatchName(modelID, candidate))
		return cheap
	}); bestID != "" {
		return bestID, true
	}
	return "", false
}

func (m *Manager) bestWorkhorse(available []string, match func(modelID string, candidate *ModelInfo) bool) string {
	bestID := ""
	bestDate := ""
	for _, modelID := range available {
		candidate := m.GetModelInfo(modelID)
		if candidate == nil || !match(modelID, candidate) {
			continue
		}
		name := candidate.APIModelName
		if name == "" {
			name = modelID
		}
		if modalities, known := modelsdev.LookupModalities(candidate.BaseURL, name); known {
			if len(modalities.Input) > 0 && !slices.Contains(modalities.Input, "text") ||
				len(modalities.Output) > 0 && !slices.Contains(modalities.Output, "text") {
				continue
			}
		}
		if bestID == "" || candidate.ReleaseDate > bestDate {
			bestID = modelID
			bestDate = candidate.ReleaseDate
		}
	}
	return bestID
}

func workhorseMatchName(modelID string, info *ModelInfo) string {
	if info != nil && info.APIModelName != "" {
		return info.APIModelName
	}
	return modelID
}

func isCustomWorkhorseRoute(info *ModelInfo) bool {
	return info.SourceID != "" || info.Source == SourceCustomLabel
}

func classifyWorkhorseModel(modelID string) (Provider, bool) {
	name := strings.ToLower(strings.TrimPrefix(modelID[strings.LastIndex(modelID, "/")+1:], "~"))
	head := name
	if i := strings.IndexAny(head, "-@"); i >= 0 {
		head = head[:i]
	}
	if len(head) > 1 && head[0] == 'o' && strings.Trim(head[1:], "0123456789") == "" {
		return ProviderOpenAI, false
	}
	if name == "codex-mini" || strings.HasPrefix(name, "codex-mini-") || strings.HasPrefix(name, "codex-mini@") {
		return ProviderOpenAI, false
	}
	markers := "-" + strings.ReplaceAll(name, ".", "-")
	for _, family := range []struct {
		marker   string
		provider Provider
	}{
		{"gpt-", ProviderOpenAI},
		{"claude-", ProviderAnthropic},
		{"gemini-", ProviderGemini},
		{"deepseek-", ProviderFireworks},
	} {
		if strings.Contains(markers, "-"+family.marker) {
			return family.provider, matchesAnyWorkhorseFamily(name, workhorseFamilies[family.provider])
		}
	}
	return "", false
}

func matchesAnyWorkhorseFamily(modelID string, families []workhorseFamily) bool {
	for _, family := range families {
		if matchesWorkhorseFamily(modelID, family) {
			return true
		}
	}
	return false
}

func matchesWorkhorseFamily(modelID string, family workhorseFamily) bool {
	if family.contains == "luna" {
		tokens := strings.FieldsFunc(modelID, func(r rune) bool {
			return r < 'a' || r > 'z'
		})
		if !slices.Contains(tokens, "luna") {
			return false
		}
	}
	if !strings.Contains(modelID, family.contains) {
		return false
	}
	for _, excluded := range family.excludes {
		if strings.Contains(modelID, excluded) {
			return false
		}
	}
	return true
}
