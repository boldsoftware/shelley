package models

import (
	"context"
	"errors"
	"testing"

	"shelley.exe.dev/llm"
)

func newWorkhorseManager(t *testing.T, built ...Built) *Manager {
	t.Helper()
	manager, err := NewManager(&Config{Models: built})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

type recordingService struct {
	request           *llm.Request
	calls             int
	err               error
	provider          string
	maxImageDimension int
	maxImageBytes     int
	supportsImages    bool
}

func (s *recordingService) Do(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.calls++
	s.request = req
	if s.err != nil {
		return nil, s.err
	}
	return &llm.Response{}, nil
}
func (s *recordingService) Provider() string       { return s.provider }
func (s *recordingService) MaxImageDimension() int { return s.maxImageDimension }
func (s *recordingService) MaxImageBytes() int     { return s.maxImageBytes }
func (s *recordingService) SupportsImages() bool   { return s.supportsImages }

type optionalRecordingService struct{ *recordingService }

func (s *optionalRecordingService) PatchProfile() string { return "optional" }

func TestWorkhorseModel(t *testing.T) {
	manager := newWorkhorseManager(
		t,
		Built{ID: "claude-opus-5", Provider: ProviderAnthropic},
		Built{ID: "claude-haiku-4-5", Provider: ProviderAnthropic, ReleaseDate: "2025-10-15"},
		Built{ID: "claude-haiku-4-6", Provider: ProviderAnthropic, ReleaseDate: "2026-08-15"},
		Built{ID: "claude-haiku-5.5", Provider: ProviderAnthropic, ReleaseDate: "2026-10-07"},
		Built{ID: "gpt-5.6-luna", Provider: ProviderOpenAI, ReleaseDate: "2026-07-09"},
		Built{ID: "gpt-5.7-luna", Provider: ProviderOpenAI, ReleaseDate: "2026-08-15"},
		Built{ID: "gpt-5.4-nano", Provider: ProviderOpenAI},
		Built{ID: "gemini-3-flash", Provider: ProviderGemini, ReleaseDate: "2025-12-17"},
		Built{ID: "gemini-3.6-flash", Provider: ProviderGemini, ReleaseDate: "2026-07-21"},
		Built{ID: "gemini-3.7-flash-lite", Provider: ProviderGemini, ReleaseDate: "2026-08-13"},
		Built{ID: "deepseek-v4-flash", Provider: ProviderFireworks, ReleaseDate: "2026-04-24"},
		Built{ID: "deepseek-v4-flash-0731-fireworks", Provider: ProviderFireworks, ReleaseDate: "2026-07-31"},
		// Pro must not be picked as a workhorse: it is the expensive sibling, and
		// the family match is "deepseek-v4" minus "pro" so the dot-versioned
		// deepseek-v4.1-flash ID still matches.
		Built{ID: "deepseek-v4-pro-fireworks", Provider: ProviderFireworks, ReleaseDate: "2026-08-13"},
		Built{ID: "deepseek-v4.1-flash-fireworks", Provider: ProviderFireworks, ReleaseDate: "2026-09-10"},
		Built{ID: "deepseek-v4.1-flash-1017-fireworks", Provider: ProviderFireworks, ReleaseDate: "2026-10-17"},
		Built{ID: "deepseek-v4-flash-0801-fireworks", Provider: ProviderFireworks, ReleaseDate: "2026-08-01"},
		Built{ID: "nemotron-lightning-3p5", Provider: ProviderFireworks},
	)

	for _, test := range []struct {
		conversationModel string
		want              string
	}{
		{"claude-opus-5", "claude-haiku-5.5"},
		{"claude-haiku-4-5", "claude-haiku-5.5"},
		{"gpt-5.4-nano", "gpt-5.7-luna"},
		{"gemini-3-flash", "gemini-3.6-flash"},
		{"nemotron-lightning-3p5", "deepseek-v4.1-flash-1017-fireworks"},
		{"unknown-custom-model", "unknown-custom-model"},
		{"", ""},
	} {
		if got := manager.GetWorkhorseModelID(test.conversationModel); got != test.want {
			t.Errorf("GetWorkhorseModelID(%q) = %q, want %q", test.conversationModel, got, test.want)
		}
	}
}

func TestGetWorkhorseServiceUsesSelectedPrimary(t *testing.T) {
	workhorse := &optionalRecordingService{recordingService: &recordingService{
		provider:          "anthropic",
		maxImageDimension: 2048,
		maxImageBytes:     5 << 20,
		supportsImages:    true,
	}}
	conversation := &recordingService{provider: "conversation"}
	manager := newWorkhorseManager(
		t,
		Built{ID: "claude-opus-5", Provider: ProviderAnthropic, Service: conversation},
		Built{ID: "claude-haiku-4-5", Provider: ProviderAnthropic, Service: workhorse},
	)

	service, err := manager.GetWorkhorseService("claude-opus-5")
	if err != nil {
		t.Fatal(err)
	}
	if service.Provider() != workhorse.provider ||
		service.MaxImageDimension() != workhorse.maxImageDimension ||
		service.MaxImageBytes() != workhorse.maxImageBytes ||
		service.SupportsImages() != workhorse.supportsImages {
		t.Fatal("workhorse service metadata did not delegate to the selected primary")
	}
	if _, ok := service.(llm.PatchProfiler); ok {
		t.Fatal("workhorse service unexpectedly exposes optional primary capabilities")
	}

	req := &llm.Request{ThinkingLevel: llm.ThinkingLevelHigh, ReasoningEffort: "high"}
	if _, err := service.Do(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if workhorse.calls != 1 || conversation.calls != 0 {
		t.Fatalf("calls = (workhorse %d, conversation %d), want (1, 0)", workhorse.calls, conversation.calls)
	}
	assertReasoningDisabled(t, workhorse.request)
	assertRequestUnchanged(t, req)
}

func TestGetWorkhorseServiceFallsBackWhenPrimaryLookupFails(t *testing.T) {
	conversation := &recordingService{
		provider:          "anthropic",
		maxImageDimension: 1024,
		maxImageBytes:     2 << 20,
		supportsImages:    true,
	}
	manager := newWorkhorseManager(
		t,
		Built{ID: "claude-opus-5", Provider: ProviderAnthropic, Service: conversation},
	)

	service, err := manager.newWorkhorseService("missing-workhorse", "claude-opus-5")
	if err != nil {
		t.Fatal(err)
	}
	if service.Provider() != conversation.provider ||
		service.MaxImageDimension() != conversation.maxImageDimension ||
		service.MaxImageBytes() != conversation.maxImageBytes ||
		service.SupportsImages() != conversation.supportsImages {
		t.Fatal("workhorse service metadata did not delegate to the conversation fallback")
	}
	if _, err := service.Do(t.Context(), &llm.Request{}); err != nil {
		t.Fatal(err)
	}
	if conversation.calls != 1 {
		t.Fatalf("conversation calls = %d, want 1", conversation.calls)
	}
}

func TestGetWorkhorseServiceDoesNotEagerlyLookupFallback(t *testing.T) {
	primary := &recordingService{}
	manager := newWorkhorseManager(
		t,
		Built{ID: "claude-haiku-4-5", Provider: ProviderAnthropic, Service: primary},
	)

	service, err := manager.newWorkhorseService("claude-haiku-4-5", "missing-conversation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Do(t.Context(), &llm.Request{}); err != nil {
		t.Fatal(err)
	}
	if primary.calls != 1 {
		t.Fatalf("primary calls = %d, want 1", primary.calls)
	}
}

func TestWorkhorseServiceFallsBackAfterPrimaryFailure(t *testing.T) {
	workhorse := &recordingService{err: errors.New("retired")}
	conversation := &recordingService{}
	manager := newWorkhorseManager(
		t,
		Built{ID: "claude-opus-5", Provider: ProviderAnthropic, Service: conversation},
		Built{ID: "claude-haiku-4-5", Provider: ProviderAnthropic, Service: workhorse},
	)
	service, err := manager.GetWorkhorseService("claude-opus-5")
	if err != nil {
		t.Fatal(err)
	}
	req := &llm.Request{ThinkingLevel: llm.ThinkingLevelHigh, ReasoningEffort: "high"}

	if _, err := service.Do(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if workhorse.calls != 1 || conversation.calls != 1 {
		t.Fatalf("calls = (workhorse %d, conversation %d), want (1, 1)", workhorse.calls, conversation.calls)
	}
	assertReasoningDisabled(t, workhorse.request)
	assertReasoningDisabled(t, conversation.request)
	if workhorse.request == conversation.request || workhorse.request == req || conversation.request == req {
		t.Fatal("each attempt must receive its own request copy")
	}
	assertRequestUnchanged(t, req)
}

func TestWorkhorseServiceDoesNotFallbackWhenContextCanceled(t *testing.T) {
	workhorse := &recordingService{err: errors.New("retired")}
	conversation := &recordingService{}
	manager := newWorkhorseManager(
		t,
		Built{ID: "claude-opus-5", Provider: ProviderAnthropic, Service: conversation},
		Built{ID: "claude-haiku-4-5", Provider: ProviderAnthropic, Service: workhorse},
	)
	service, err := manager.GetWorkhorseService("claude-opus-5")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = service.Do(ctx, &llm.Request{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if workhorse.calls != 1 || conversation.calls != 0 {
		t.Fatalf("calls = (workhorse %d, conversation %d), want (1, 0)", workhorse.calls, conversation.calls)
	}
}

func TestWorkhorseServiceDoesNotDuplicateConversationModel(t *testing.T) {
	modelErr := errors.New("model failed")
	conversation := &recordingService{err: modelErr}
	manager := newWorkhorseManager(
		t,
		Built{ID: "custom-model", Provider: ProviderBuiltIn, Service: conversation},
	)
	service, err := manager.GetWorkhorseService("custom-model")
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Do(t.Context(), &llm.Request{})
	if !errors.Is(err, modelErr) {
		t.Fatalf("error = %v, want %v", err, modelErr)
	}
	if conversation.calls != 1 {
		t.Fatalf("conversation calls = %d, want 1", conversation.calls)
	}
}

func TestWorkhorseServiceReturnsFallbackError(t *testing.T) {
	workhorse := &recordingService{err: errors.New("workhorse failed")}
	fallbackErr := errors.New("conversation failed")
	conversation := &recordingService{err: fallbackErr}
	manager := newWorkhorseManager(
		t,
		Built{ID: "claude-opus-5", Provider: ProviderAnthropic, Service: conversation},
		Built{ID: "claude-haiku-4-5", Provider: ProviderAnthropic, Service: workhorse},
	)
	service, err := manager.GetWorkhorseService("claude-opus-5")
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Do(t.Context(), &llm.Request{})
	if !errors.Is(err, fallbackErr) {
		t.Fatalf("error = %v, want %v", err, fallbackErr)
	}
	if workhorse.calls != 1 || conversation.calls != 1 {
		t.Fatalf("calls = (workhorse %d, conversation %d), want (1, 1)", workhorse.calls, conversation.calls)
	}
}

func TestWorkhorseServiceReturnsFallbackLookupError(t *testing.T) {
	workhorse := &recordingService{err: errors.New("workhorse failed")}
	manager := newWorkhorseManager(
		t,
		Built{ID: "claude-haiku-4-5", Provider: ProviderAnthropic, Service: workhorse},
	)
	service, err := manager.newWorkhorseService("claude-haiku-4-5", "missing-conversation")
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Do(t.Context(), &llm.Request{})
	if err == nil {
		t.Fatal("expected fallback lookup error")
	}
	if got, want := err.Error(), "unsupported model: missing-conversation"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestGetWorkhorseServiceReturnsSelectionErrors(t *testing.T) {
	manager := newWorkhorseManager(t)

	if _, err := manager.GetWorkhorseService(""); err == nil || err.Error() != `no workhorse model available (conversation model "")` {
		t.Fatalf("empty model error = %v", err)
	}
	if _, err := manager.GetWorkhorseService("missing-model"); err == nil || err.Error() != "unsupported model: missing-model" {
		t.Fatalf("missing model error = %v", err)
	}
}

func TestWorkhorseClampsExplicitReasoningMapWithoutOff(t *testing.T) {
	for _, tt := range []struct {
		name       string
		support    string
		rawMap     string
		wantLevel  llm.ThinkingLevel
		wantEffort string
	}{
		{
			name:      "mandatory reasoning map clamps off to lowest configured level",
			support:   "yes",
			rawMap:    `{"low":"low","medium":"medium","high":"high"}`,
			wantLevel: llm.ThinkingLevelLow,
		},
		{
			name:       "custom verbatim effort at lowest tier",
			support:    "yes",
			rawMap:     `{"medium":"provider-low","high":"provider-high"}`,
			wantLevel:  llm.ThinkingLevelMedium,
			wantEffort: "provider-low",
		},
		{
			name:      "map with off keeps thinking off",
			support:   "yes",
			rawMap:    `{"off":"off","low":"low","high":"high"}`,
			wantLevel: llm.ThinkingLevelOff,
		},
		{
			name:      "disabled reasoning ignores map",
			support:   "no",
			rawMap:    `{"low":"low","high":"high"}`,
			wantLevel: llm.ThinkingLevelOff,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inner := &recordingService{provider: "openai"}
			wrapped := WrapReasoningConfig(inner, "https://openrouter.int.exe.xyz/api/v1", "google/gemini-4-carbon", tt.support, tt.rawMap)
			manager := newWorkhorseManager(t, Built{ID: "gemini-4-carbon-openrouter", Provider: ProviderOpenAI, Service: wrapped})
			service, err := manager.GetWorkhorseService("gemini-4-carbon-openrouter")
			if err != nil {
				t.Fatal(err)
			}
			req := &llm.Request{ThinkingLevel: llm.ThinkingLevelHigh, ReasoningEffort: "high"}
			if _, err := service.Do(t.Context(), req); err != nil {
				t.Fatalf("workhorse Do failed: %v", err)
			}
			assertRequestUnchanged(t, req)
			if inner.request == nil {
				t.Fatal("inner service was not called")
			}
			if inner.request.ThinkingLevel != tt.wantLevel || inner.request.ReasoningEffort != tt.wantEffort {
				t.Fatalf("sent (%v, %q), want (%v, %q)", inner.request.ThinkingLevel, inner.request.ReasoningEffort, tt.wantLevel, tt.wantEffort)
			}
		})
	}
}

func assertReasoningDisabled(t *testing.T, req *llm.Request) {
	t.Helper()
	if req == nil {
		t.Fatal("service was not called")
	}
	if req.ThinkingLevel != llm.ThinkingLevelOff || req.ReasoningEffort != "" {
		t.Fatalf("sent reasoning controls = (%v, %q), want (off, empty)", req.ThinkingLevel, req.ReasoningEffort)
	}
}

func assertRequestUnchanged(t *testing.T, req *llm.Request) {
	t.Helper()
	if req.ThinkingLevel != llm.ThinkingLevelHigh || req.ReasoningEffort != "high" {
		t.Fatalf("caller request was mutated: (%v, %q)", req.ThinkingLevel, req.ReasoningEffort)
	}
}
