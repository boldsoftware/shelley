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
	request            *llm.Request
	calls              int
	err                error
	provider           string
	tokenContextWindow int
	maxImageDimension  int
	maxImageBytes      int
	supportsImages     bool
}

func (s *recordingService) Do(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.calls++
	s.request = req
	if s.err != nil {
		return nil, s.err
	}
	return &llm.Response{}, nil
}
func (s *recordingService) Provider() string        { return s.provider }
func (s *recordingService) TokenContextWindow() int { return s.tokenContextWindow }
func (s *recordingService) MaxImageDimension() int  { return s.maxImageDimension }
func (s *recordingService) MaxImageBytes() int      { return s.maxImageBytes }
func (s *recordingService) SupportsImages() bool    { return s.supportsImages }

type optionalRecordingService struct{ *recordingService }

func (s *optionalRecordingService) PatchProfile() string { return "optional" }

func TestWorkhorseModel(t *testing.T) {
	manager := newWorkhorseManager(
		t,
		Built{ID: "claude-opus-5", Provider: ProviderAnthropic},
		Built{ID: "claude-haiku-4-5", Provider: ProviderAnthropic, ReleaseDate: "2025-10-15"},
		Built{ID: "claude-haiku-4-6", Provider: ProviderAnthropic, ReleaseDate: "2026-08-15"},
		Built{ID: "gpt-5.6-luna", Provider: ProviderOpenAI, ReleaseDate: "2026-07-09"},
		Built{ID: "gpt-5.7-luna", Provider: ProviderOpenAI, ReleaseDate: "2026-08-15"},
		Built{ID: "gpt-5.4-nano", Provider: ProviderOpenAI},
		Built{ID: "gemini-3-flash", Provider: ProviderGemini, ReleaseDate: "2025-12-17"},
		Built{ID: "gemini-3.6-flash", Provider: ProviderGemini, ReleaseDate: "2026-07-21"},
		Built{ID: "gemini-3.7-flash-lite", Provider: ProviderGemini, ReleaseDate: "2026-08-13"},
		Built{ID: "deepseek-v4-flash", Provider: ProviderFireworks, ReleaseDate: "2026-04-24"},
		Built{ID: "deepseek-v4-flash-0731-fireworks", Provider: ProviderFireworks, ReleaseDate: "2026-07-31"},
		Built{ID: "deepseek-v4-flash-0801-fireworks", Provider: ProviderFireworks, ReleaseDate: "2026-08-01"},
		Built{ID: "nemotron-lightning-3p5", Provider: ProviderFireworks},
	)

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
		if got := manager.workhorseModel(test.conversationModel); got != test.want {
			t.Errorf("workhorseModel(%q) = %q, want %q", test.conversationModel, got, test.want)
		}
	}
}

func TestWorkhorseModelStaysOnConversationGateway(t *testing.T) {
	conversation := Built{
		ID: "gpt-5.6-terra@priyanka", Provider: ProviderOpenAI,
		Source: "priyanka gateway", BaseURL: "https://priyanka.example",
	}
	otherGateway := Built{
		ID: "gpt-5.7-luna@chelsea", Provider: ProviderOpenAI, ReleaseDate: "2026-08-15",
		Source: "chelsea gateway", BaseURL: "https://chelsea.example",
	}
	sameGateway := Built{
		ID: "gpt-5.6-luna@priyanka", Provider: ProviderOpenAI, ReleaseDate: "2026-07-09",
		Source: "priyanka gateway", BaseURL: "https://priyanka.example",
	}

	manager := newWorkhorseManager(t, conversation, otherGateway, sameGateway)
	if got := manager.workhorseModel(conversation.ID); got != sameGateway.ID {
		t.Fatalf("workhorseModel(%q) = %q, want same-gateway %q", conversation.ID, got, sameGateway.ID)
	}

	// Without a same-gateway workhorse, stay on the conversation model rather
	// than crossing to another gateway's newer model.
	manager = newWorkhorseManager(t, conversation, otherGateway)
	if got := manager.workhorseModel(conversation.ID); got != conversation.ID {
		t.Fatalf("workhorseModel(%q) = %q, want conversation model rather than cross-gateway fallback", conversation.ID, got)
	}
}

func TestGetWorkhorseServiceUsesSelectedPrimary(t *testing.T) {
	workhorse := &optionalRecordingService{recordingService: &recordingService{
		provider:           "anthropic",
		tokenContextWindow: 200000,
		maxImageDimension:  2048,
		maxImageBytes:      5 << 20,
		supportsImages:     true,
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
		service.TokenContextWindow() != workhorse.tokenContextWindow ||
		service.MaxImageDimension() != workhorse.maxImageDimension ||
		service.MaxImageBytes() != workhorse.maxImageBytes ||
		service.SupportsImages() != workhorse.supportsImages {
		t.Fatal("workhorse service metadata did not delegate to the selected primary")
	}
	if _, ok := service.(llm.PatchProfiler); ok {
		t.Fatal("workhorse service unexpectedly exposes optional primary capabilities")
	}

	req := &llm.Request{ThinkingLevel: llm.ThinkingLevelHigh, ReasoningEffort: "high"}
	if _, err := service.Do(context.Background(), req); err != nil {
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
		provider:           "anthropic",
		tokenContextWindow: 100000,
		maxImageDimension:  1024,
		maxImageBytes:      2 << 20,
		supportsImages:     true,
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
		service.TokenContextWindow() != conversation.tokenContextWindow ||
		service.MaxImageDimension() != conversation.maxImageDimension ||
		service.MaxImageBytes() != conversation.maxImageBytes ||
		service.SupportsImages() != conversation.supportsImages {
		t.Fatal("workhorse service metadata did not delegate to the conversation fallback")
	}
	if _, err := service.Do(context.Background(), &llm.Request{}); err != nil {
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
	if _, err := service.Do(context.Background(), &llm.Request{}); err != nil {
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

	if _, err := service.Do(context.Background(), req); err != nil {
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
	ctx, cancel := context.WithCancel(context.Background())
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

	_, err = service.Do(context.Background(), &llm.Request{})
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

	_, err = service.Do(context.Background(), &llm.Request{})
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

	_, err = service.Do(context.Background(), &llm.Request{})
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
