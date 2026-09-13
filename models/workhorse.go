package models

import (
	"context"
	"fmt"
	"strings"

	"shelley.exe.dev/llm"
)

type workhorseFamily struct {
	contains string
	excludes []string
}

// workhorseFamilies identifies one cheap, fast model family per provider for
// background tasks. The newest available release in the family is selected.
var workhorseFamilies = map[Provider]workhorseFamily{
	ProviderOpenAI:    {contains: "luna"},
	ProviderAnthropic: {contains: "haiku"},
	ProviderGemini: {
		contains: "flash",
		excludes: []string{"lite", "image", "tts", "live", "omni"},
	},
	// DeepSeek's Flash line. Matched on "deepseek-v4" minus Pro rather than
	// "deepseek-v4-flash" because the 4.1 release is "deepseek-v4.1-flash":
	// the dot-versioned IDs do not share a literal "v4-flash" substring.
	ProviderFireworks: {contains: "deepseek-v4", excludes: []string{"pro"}},
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
	request.ThinkingLevel = llm.ThinkingLevelOff
	request.ReasoningEffort = ""
	return service.Do(ctx, &request)
}

func (s *workhorseService) Provider() string       { return s.primary.Provider() }
func (s *workhorseService) MaxImageDimension() int { return s.primary.MaxImageDimension() }
func (s *workhorseService) MaxImageBytes() int     { return s.primary.MaxImageBytes() }
func (s *workhorseService) SupportsImages() bool   { return s.primary.SupportsImages() }

func (m *Manager) getWorkhorseService(conversationModelID string) (llm.Service, error) {
	modelID := m.workhorseModel(conversationModelID)
	if modelID == "" {
		return nil, fmt.Errorf("no workhorse model available (conversation model %q)", conversationModelID)
	}
	return m.newWorkhorseService(modelID, conversationModelID)
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

func (m *Manager) workhorseModel(conversationModelID string) string {
	info := m.GetModelInfo(conversationModelID)
	if info == nil {
		return conversationModelID
	}
	family, ok := workhorseFamilies[info.Provider]
	if !ok {
		return conversationModelID
	}
	bestID := ""
	bestDate := ""
	for _, modelID := range m.GetAvailableModels() {
		candidate := m.GetModelInfo(modelID)
		if candidate == nil ||
			candidate.Provider != info.Provider ||
			candidate.BaseURL != info.BaseURL ||
			candidate.Source != info.Source ||
			!matchesWorkhorseFamily(modelID, family) {
			continue
		}
		if bestID == "" || candidate.ReleaseDate > bestDate {
			bestID = modelID
			bestDate = candidate.ReleaseDate
		}
	}
	if bestID != "" {
		return bestID
	}
	return conversationModelID
}

func matchesWorkhorseFamily(modelID string, family workhorseFamily) bool {
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
