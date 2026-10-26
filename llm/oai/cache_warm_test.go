package oai

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
)

// warmServer records request bodies and answers each with reply.
func warmServer(t *testing.T, reply func(body map[string]any) string) (*httptest.Server, *[]map[string]any) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(reply(body)))
	}))
	t.Cleanup(server.Close)
	return server, &bodies
}

const prewarmReply = `{"id":"r","status":"completed","model":"gpt-6.1-sol","output":[],` +
	`"usage":{"input_tokens":5000,"input_tokens_details":{"cached_tokens":4880},"output_tokens":0}}`

func warmRequest() *llm.Request {
	return &llm.Request{
		System: []llm.SystemContent{{Text: "personal assistant"}},
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "water the basil"}}},
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "noted"}}},
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "thanks"}}},
		},
		Tools:         []*llm.Tool{{Name: "lookup", Description: "look up", InputSchema: llm.EmptySchema()}},
		ThinkingLevel: llm.ThinkingLevelHigh,
	}
}

// A prewarm must hit the same cache entry as the turn it replays: every field
// Do sends must match, and only output generation may change.
func TestWarmCacheReplaysRequestWithPrewarm(t *testing.T) {
	server, bodies := warmServer(t, func(map[string]any) string { return prewarmReply })
	s := &ResponsesService{APIKey: "k", Model: GPT61Sol, ModelURL: server.URL, ProviderName: "openai"}
	ctx := llmhttp.WithPromptCacheKey(t.Context(), "conv")
	if _, err := s.Do(ctx, warmRequest()); err != nil {
		t.Fatal(err)
	}
	usage, err := s.WarmCache(ctx, warmRequest())
	if err != nil {
		t.Fatal(err)
	}
	if usage.CacheReadInputTokens != 4880 || usage.OutputTokens != 0 || usage.Model != "gpt-6.1-sol" {
		t.Fatalf("usage = %+v", usage)
	}
	if len(*bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(*bodies))
	}
	turn, warm := (*bodies)[0], (*bodies)[1]
	if warm["stream"] != false || warm["max_output_tokens"] != float64(prewarmOutputCap) ||
		!reflect.DeepEqual(warm["prompt_cache_options"], map[string]any{"prewarm": true}) {
		t.Fatalf("warm controls: stream %v, max_output_tokens %v, prompt_cache_options %v",
			warm["stream"], warm["max_output_tokens"], warm["prompt_cache_options"])
	}
	for _, m := range []map[string]any{turn, warm} {
		for _, k := range []string{"stream", "max_output_tokens", "prompt_cache_options"} {
			delete(m, k)
		}
	}
	if !maps.EqualFunc(turn, warm, func(a, b any) bool { return reflect.DeepEqual(a, b) }) {
		t.Fatalf("warm prefix differs from turn:\nturn %v\nwarm %v", turn, warm)
	}
}

// If prewarm were ignored, the request would generate; that must surface.
func TestWarmCacheRejectsOutput(t *testing.T) {
	server, _ := warmServer(t, func(map[string]any) string {
		return `{"status":"incomplete","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],` +
			`"usage":{"input_tokens":5000,"output_tokens":16}}`
	})
	s := &ResponsesService{APIKey: "k", Model: GPT61Sol, ModelURL: server.URL, ProviderName: "openai"}
	if _, err := s.WarmCache(t.Context(), warmRequest()); err == nil || !strings.Contains(err.Error(), "generated output") {
		t.Fatalf("err = %v, want generated output", err)
	}
}

func TestPrewarmSupport(t *testing.T) {
	server, _ := warmServer(t, func(map[string]any) string { return prewarmReply })
	for _, tc := range []struct {
		model    string
		provider string
		want     bool
	}{
		{"gpt-6.1-sol", "openai", true},
		{"gpt-6.1-terra", "openai", true},
		{"gpt-6-luna", "openai", true},
		{"gpt-5.6-terra", "openai", true},
		{"gpt-5.4", "openai", false},
		{"gpt-5.10", "openai", true},
		{"gpt-4.1-2025-04-14", "openai", false},
		{"gpt-6.1-sol", "xai", false},
		{"o3", "openai", false},
	} {
		s := &ResponsesService{Model: Model{ModelName: tc.model}, ModelURL: server.URL, ProviderName: tc.provider}
		got := s.CacheTTL() != 0
		_, err := s.WarmCache(t.Context(), warmRequest())
		if got != tc.want || errors.Is(err, llm.ErrCacheWarmUnsupported) == tc.want {
			t.Errorf("%s via %s: CacheTTL %v, WarmCache err %v; want support %v", tc.model, tc.provider, s.CacheTTL(), err, tc.want)
		}
	}
}
