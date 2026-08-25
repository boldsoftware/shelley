package models

import (
	"context"
	"errors"
	"testing"

	"shelley.exe.dev/llm"
)

type fakeCatalog struct {
	ids       []string
	providers map[string]Provider
	dates     map[string]string
	services  map[string]*recordingService
}

func (f *fakeCatalog) GetAvailableModels() []string { return f.ids }

func (f *fakeCatalog) GetModelInfo(modelID string) *ModelInfo {
	provider, ok := f.providers[modelID]
	if !ok {
		return nil
	}
	return &ModelInfo{DisplayName: modelID, Provider: provider, ReleaseDate: f.dates[modelID]}
}

func (f *fakeCatalog) GetService(modelID string) (llm.Service, error) {
	return f.services[modelID], nil
}

type recordingService struct {
	request *llm.Request
	err     error
}

func (s *recordingService) Do(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.request = req
	if s.err != nil {
		return nil, s.err
	}
	return &llm.Response{}, nil
}
func (s *recordingService) Provider() string        { return "test" }
func (s *recordingService) TokenContextWindow() int { return 8192 }
func (s *recordingService) MaxImageDimension() int  { return 0 }
func (s *recordingService) MaxImageBytes() int      { return 0 }
func (s *recordingService) SupportsImages() bool    { return false }

func TestWorkhorseModel(t *testing.T) {
	catalog := &fakeCatalog{
		ids: []string{
			"claude-opus-5", "claude-haiku-4-5", "claude-haiku-4-6",
			"gpt-5.6-luna", "gpt-5.7-luna", "gpt-5.4-nano",
			"gemini-3-flash", "gemini-3.6-flash", "gemini-3.7-flash-lite",
			"deepseek-v4-flash", "deepseek-v4-flash-0731-fireworks",
			"deepseek-v4-flash-0801-fireworks", "nemotron-lightning-3p5",
		},
		providers: map[string]Provider{
			"claude-opus-5":                    ProviderAnthropic,
			"claude-haiku-4-5":                 ProviderAnthropic,
			"claude-haiku-4-6":                 ProviderAnthropic,
			"gpt-5.6-luna":                     ProviderOpenAI,
			"gpt-5.7-luna":                     ProviderOpenAI,
			"gpt-5.4-nano":                     ProviderOpenAI,
			"gemini-3-flash":                   ProviderGemini,
			"gemini-3.6-flash":                 ProviderGemini,
			"gemini-3.7-flash-lite":            ProviderGemini,
			"deepseek-v4-flash":                ProviderFireworks,
			"deepseek-v4-flash-0731-fireworks": ProviderFireworks,
			"deepseek-v4-flash-0801-fireworks": ProviderFireworks,
			"nemotron-lightning-3p5":           ProviderFireworks,
		},
		dates: map[string]string{
			"claude-haiku-4-5":                 "2025-10-15",
			"claude-haiku-4-6":                 "2026-08-15",
			"gpt-5.6-luna":                     "2026-07-09",
			"gpt-5.7-luna":                     "2026-08-15",
			"gemini-3-flash":                   "2025-12-17",
			"gemini-3.6-flash":                 "2026-07-21",
			"gemini-3.7-flash-lite":            "2026-08-13",
			"deepseek-v4-flash":                "2026-04-24",
			"deepseek-v4-flash-0731-fireworks": "2026-07-31",
			"deepseek-v4-flash-0801-fireworks": "2026-08-01",
		},
	}

	for _, test := range []struct {
		conversationModel string
		want              string
	}{
		{"claude-opus-5", "claude-haiku-4-6"},
		{"claude-haiku-4-5", "claude-haiku-4-6"},
		{"gpt-5.4-nano", "gpt-5.7-luna"},
		{"gemini-3-flash", "gemini-3.6-flash"},
		{"nemotron-lightning-3p5", "deepseek-v4-flash-0801-fireworks"},
		{"unknown-custom-model", "unknown-custom-model"},
		{"", ""},
	} {
		if got := workhorseModel(catalog, test.conversationModel); got != test.want {
			t.Errorf("workhorseModel(%q) = %q, want %q", test.conversationModel, got, test.want)
		}
	}
}

func TestWorkhorseDoDisablesReasoning(t *testing.T) {
	workhorse := &recordingService{}
	conversation := &recordingService{}
	catalog := &fakeCatalog{
		ids: []string{"claude-opus-5", "claude-haiku-4-5"},
		providers: map[string]Provider{
			"claude-opus-5":    ProviderAnthropic,
			"claude-haiku-4-5": ProviderAnthropic,
		},
		services: map[string]*recordingService{
			"claude-opus-5":    conversation,
			"claude-haiku-4-5": workhorse,
		},
	}
	req := &llm.Request{ThinkingLevel: llm.ThinkingLevelHigh, ReasoningEffort: "high"}

	if _, err := WorkhorseDo(context.Background(), catalog, "claude-opus-5", req); err != nil {
		t.Fatal(err)
	}
	if workhorse.request == nil {
		t.Fatal("workhorse was not called")
	}
	if conversation.request != nil {
		t.Fatal("conversation model was called")
	}
	if workhorse.request.ThinkingLevel != llm.ThinkingLevelOff || workhorse.request.ReasoningEffort != "" {
		t.Fatalf("sent reasoning controls = (%v, %q), want (off, empty)", workhorse.request.ThinkingLevel, workhorse.request.ReasoningEffort)
	}
	if req.ThinkingLevel != llm.ThinkingLevelHigh || req.ReasoningEffort != "high" {
		t.Fatalf("caller request was mutated: (%v, %q)", req.ThinkingLevel, req.ReasoningEffort)
	}
}

func TestWorkhorseDoFallsBackToConversationModel(t *testing.T) {
	workhorse := &recordingService{err: errors.New("retired")}
	conversation := &recordingService{}
	catalog := &fakeCatalog{
		ids: []string{"claude-opus-5", "claude-haiku-4-5"},
		providers: map[string]Provider{
			"claude-opus-5":    ProviderAnthropic,
			"claude-haiku-4-5": ProviderAnthropic,
		},
		services: map[string]*recordingService{
			"claude-opus-5":    conversation,
			"claude-haiku-4-5": workhorse,
		},
	}

	if _, err := WorkhorseDo(context.Background(), catalog, "claude-opus-5", &llm.Request{}); err != nil {
		t.Fatal(err)
	}
	if workhorse.request == nil || conversation.request == nil {
		t.Fatalf("calls = (workhorse %v, conversation %v), want both", workhorse.request != nil, conversation.request != nil)
	}
	if conversation.request.ThinkingLevel != llm.ThinkingLevelOff {
		t.Fatalf("fallback thinking level = %v, want off", conversation.request.ThinkingLevel)
	}
}
