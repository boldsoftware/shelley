package models

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// failingLLMService fails every request with err.
type failingLLMService struct {
	mockLLMService
	err error
}

func (f *failingLLMService) Do(context.Context, *llm.Request) (*llm.Response, error) {
	return nil, f.err
}

func (f *failingLLMService) CacheTTL() time.Duration { return time.Minute }

func (f *failingLLMService) WarmCache(context.Context, *llm.Request) (llm.Usage, error) {
	return llm.Usage{}, f.err
}

// stalledRequestError carries request metadata like a provider idle abort.
type stalledRequestError struct{}

func (stalledRequestError) Error() string { return "stream stalled" }

func (stalledRequestError) RequestErrorInfo() llm.RequestErrorInfo {
	return llm.RequestErrorInfo{Retryable: true, IdleStallDuration: time.Minute}
}

func TestCustomModelErrorPrefixOnDBModel(t *testing.T) {
	const upstreamBody = `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	const apiKey = "sentinel-custom-model-key"
	testDB := newTestModelDB(t)
	if _, err := testDB.CreateModel(t.Context(), generated.CreateModelParams{
		ModelID: "claude-opus-5-5-local", DisplayName: "claude-opus-5-5", ProviderType: "anthropic",
		Endpoint: server.URL + "/v1/messages", ApiKey: apiKey, ModelName: "claude-opus-5-5",
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(&Config{DB: testDB, HTTPC: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	service, err := manager.GetService("claude-opus-5-5-local")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Do(t.Context(), simpleRequest())
	if err == nil {
		t.Fatal("Do succeeded, want upstream error")
	}
	t.Logf("error:\n%s", err)

	first, rest, ok := strings.Cut(err.Error(), "\n")
	if !ok {
		t.Fatalf("error has no prefix line: %q", err)
	}
	for _, want := range []string{
		`custom model "claude-opus-5-5"`, "id claude-opus-5-5-local", "host " + serverURL.Host,
		"used its own API key", "Model → Add / Remove Models",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("prefix line %q does not contain %q", first, want)
		}
	}
	var annotated *llm.AnnotatedError
	if !errors.As(err, &annotated) || annotated.Note != first {
		t.Fatalf("error is not an llm.AnnotatedError with the prefix as its note: %#v", err)
	}
	if want := customModelErrorPrefix("claude-opus-5-5", "claude-opus-5-5-local", server.URL+"/v1/messages"); first != want {
		t.Errorf("prefix line = %q, want %q", first, want)
	}
	upstream := errors.Unwrap(err)
	if upstream == nil || upstream.Error() != rest {
		t.Fatalf("upstream error not wrapped unchanged: unwrap=%v rest=%q", upstream, rest)
	}
	if !strings.Contains(rest, "400 Bad Request") || !strings.Contains(rest, upstreamBody) {
		t.Errorf("upstream error text = %q", rest)
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Errorf("error leaks the API key: %q", err)
	}
}

func TestCustomModelErrorWrapsUpstream(t *testing.T) {
	upstream := errors.New("status 400 Bad Request: credit balance is too low")
	entry := serviceEntry{
		service: &failingLLMService{err: upstream}, modelID: "my-model", source: SourceCustomLabel,
		displayName: "My Model", baseURL: "https://api.example.com/v1",
	}
	// A zero Manager has no logger; the wrapper must still apply.
	_, err := (&Manager{}).loggedService(entry).Do(t.Context(), simpleRequest())
	want := `custom model "My Model" (id my-model, host api.example.com) used its own API key; edit or remove it under Model → Add / Remove Models` +
		"\n" + upstream.Error()
	if err == nil || err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	if !errors.Is(err, upstream) {
		t.Fatal("errors.Is lost the upstream error")
	}

	entry.service = &failingLLMService{err: stalledRequestError{}}
	_, err = (&Manager{}).loggedService(entry).Do(t.Context(), simpleRequest())
	if info, ok := llm.RequestErrorInfoFromError(err); !ok || info.IdleStallDuration != time.Minute || !info.Retryable {
		t.Fatalf("request error info through the prefix = %+v, %v", info, ok)
	}
}

func TestCustomModelErrorNotOnWarmCache(t *testing.T) {
	upstream := errors.New("status 400 Bad Request: credit balance is too low")
	entry := serviceEntry{
		service: &failingLLMService{err: upstream}, modelID: "my-model", source: SourceCustomLabel,
		displayName: "My Model", baseURL: "https://api.example.com/v1",
	}
	if _, err := llm.WarmCache(t.Context(), (&Manager{}).loggedService(entry), simpleRequest()); err != upstream {
		t.Fatalf("WarmCache error = %v, want upstream error unchanged", err)
	}
}

func TestCustomModelErrorNotOnOtherModels(t *testing.T) {
	upstream := errors.New("status 400 Bad Request: credit balance is too low")
	manager, err := NewManager(&Config{Models: []Built{{
		ID: "builtin-model", Provider: ProviderAnthropic, Source: "exe.dev gateway",
		Service: &failingLLMService{err: upstream}, BaseURL: "https://api.anthropic.com",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := manager.GetService("builtin-model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Do(t.Context(), simpleRequest()); err != upstream {
		t.Fatalf("built-in model error = %v, want upstream error unchanged", err)
	}

	// A provider connection labelled "custom" is not a DB custom model.
	provider := serviceEntry{
		service: &failingLLMService{err: upstream}, modelID: "provider:a:m", source: SourceCustomLabel,
		sourceID: "a", baseURL: "https://api.example.com",
	}
	if _, err := manager.loggedService(provider).Do(t.Context(), simpleRequest()); err != upstream {
		t.Fatalf("provider connection error = %v, want upstream error unchanged", err)
	}
}

func TestCustomModelErrorPrefixHost(t *testing.T) {
	for baseURL, wantHost := range map[string]string{
		"https://api.anthropic.com":             "api.anthropic.com",
		"https://api.anthropic.com/v1/messages": "api.anthropic.com",
		"http://127.0.0.1:8080/v1":              "127.0.0.1:8080",
		"not a url":                             "not a url",
		"":                                      "",
	} {
		got := customModelErrorPrefix("Name", "id-1", baseURL)
		want := `custom model "Name" (id id-1, host ` + wantHost + `) used its own API key; edit or remove it under Model → Add / Remove Models`
		if got != want {
			t.Errorf("customModelErrorPrefix(%q) = %q, want %q", baseURL, got, want)
		}
	}
}
