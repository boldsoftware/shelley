package modelsdev

import (
	"reflect"
	"testing"

	"shelley.exe.dev/llm"
)

func TestLookupImageSupport(t *testing.T) {
	cases := []struct {
		name       string
		endpoint   string
		model      string
		wantFound  bool
		wantImages bool
	}{
		// First-party hosts (models.dev omits their "api" field; seeded via
		// knownHosts).
		{"anthropic", "https://api.anthropic.com", "claude-opus-4-5-20251101", true, true},
		{"openai", "https://api.openai.com/v1", "gpt-5.4", true, true},
		{"gemini", "https://generativelanguage.googleapis.com", "gemini-3.1-pro-preview", true, true},

		// Hosts that carry an explicit "api" field in models.dev.
		{"fireworks text-only", "https://api.fireworks.ai/inference/v1", "accounts/fireworks/models/glm-5p2", true, false},
		{"fireworks vision", "https://api.fireworks.ai/inference/v1", "accounts/fireworks/models/kimi-k3", true, true},

		// The original bug: a custom model pointed at opencode.ai/zen. The
		// host matches even though the configured path needn't be exact, and
		// deepseek-v4-flash is text-only.
		// /zen/go/v1 is the opencode-go provider (the exact URL from the
		// original 400). deepseek-v4-flash lives there and is text-only.
		{"opencode-go zen deepseek", "https://opencode.ai/zen/go/v1/chat/completions", "deepseek-v4-flash", true, false},
		// /zen/v1 is the opencode provider, which carries deepseek-v4-flash-free.
		{"opencode zen deepseek-free", "https://opencode.ai/zen/v1", "deepseek-v4-flash-free", true, false},
		// The path disambiguates which provider's catalog applies: the -free
		// id only exists under opencode (/zen/v1), not opencode-go.
		{"opencode bare host resolves go model", "opencode.ai", "deepseek-v4-flash", true, false},

		// Unknown / empty endpoints yield no information.
		{"unknown host", "https://made-up.example.com", "x", false, false},
		{"empty endpoint", "", "claude-opus-4-5-20251101", false, false},
		{"known host unknown model", "https://api.fireworks.ai/inference/v1", "made-up-model", false, false},

		// Last-segment fallback within a host-matched provider.
		{"openai slug", "https://api.openai.com", "openai/gpt-4o", true, true},
		{"openai slug text", "https://api.openai.com", "openai/gpt-oss-20b", true, false},

		// Slugs whose host we don't know fall through to OpenRouter's catalog.
		{"openrouter llama", "", "meta-llama/llama-3.3-70b-instruct", true, false},
		{"openrouter deepseek", "", "deepseek/deepseek-chat", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotImages, gotFound := LookupImageSupport(c.endpoint, c.model)
			if gotFound != c.wantFound || gotImages != c.wantImages {
				t.Errorf("LookupImageSupport(%q,%q) = (images=%v,found=%v); want (images=%v,found=%v)",
					c.endpoint, c.model, gotImages, gotFound, c.wantImages, c.wantFound)
			}
		})
	}
}

// imageEntry builds a modelEntry with the given image-input support.
func imageEntry(image bool) modelEntry {
	var m modelEntry
	if image {
		m.Modalities.Input = []string{"text", "image"}
	} else {
		m.Modalities.Input = []string{"text"}
	}
	return m
}

// prov builds a providerEntry with an "api" URL carrying a single model id.
func prov(api, modelID string, image bool) providerEntry {
	return providerEntry{API: api, Models: map[string]modelEntry{modelID: imageEntry(image)}}
}

func TestBestProviderForPath(t *testing.T) {
	// Mirror the real opencode collision: two providers on one host with
	// different paths and different image support for the same model id.
	zen := prov("https://opencode.ai/zen/v1", "m", true)
	zenGo := prov("https://opencode.ai/zen/go/v1", "m", false)
	providers := []providerEntry{zen, zenGo}

	cases := []struct {
		name     string
		endpoint string
		wantAPI  string // "" means expect ok=false
	}{
		{"go path picks opencode-go", "https://opencode.ai/zen/go/v1/chat/completions", zenGo.API},
		{"plain zen path picks opencode", "https://opencode.ai/zen/v1/chat/completions", zen.API},
		{"shorter/looser path still resolves", "https://opencode.ai/zen", zen.API},
		{"model absent everywhere", "https://opencode.ai/zen/go/v1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			model := "m"
			if c.wantAPI == "" {
				model = "absent"
			}
			p, ok := bestProviderForPath(providers, pathSegments(c.endpoint), model)
			if (c.wantAPI != "") != ok {
				t.Fatalf("ok = %v; want %v", ok, c.wantAPI != "")
			}
			if ok && p.API != c.wantAPI {
				t.Errorf("chose %q; want %q", p.API, c.wantAPI)
			}
		})
	}
}

func TestLookupReasoningSupport(t *testing.T) {
	cases := []struct {
		endpoint, model string
		want, found     bool
	}{
		{"https://api.openai.com/v1", "gpt-5.4", true, true},
		{"https://api.openai.com/v1", "gpt-4o", false, true},
		{"https://api.fireworks.ai/inference/v1", "accounts/fireworks/models/gpt-oss-20b", true, true},
		{"https://generativelanguage.googleapis.com", "gemini-3-flash-preview", true, true},
		{"https://made-up.example.com", "x", false, false},
	}
	for _, tc := range cases {
		got, found := LookupReasoningSupport(tc.endpoint, tc.model)
		if got != tc.want || found != tc.found {
			t.Errorf("LookupReasoningSupport(%q, %q) = (%v, %v), want (%v, %v)", tc.endpoint, tc.model, got, found, tc.want, tc.found)
		}
	}
}

func TestParseReasoningCapabilities(t *testing.T) {
	levels := func(names ...string) []llm.ThinkingLevel {
		out := make([]llm.ThinkingLevel, len(names))
		for i, name := range names {
			out[i] = llm.ParseThinkingLevel(name)
		}
		return out
	}
	tests := []struct {
		name string
		in   modelEntry
		want ReasoningCapabilities
	}{
		{name: "unsupported", in: modelEntry{}},
		{
			name: "explicit efforts",
			in:   modelEntry{Reasoning: true, ReasoningOptions: []reasoningOption{{Type: "effort", Values: []string{"none", "low", "medium", "high", "xhigh", "max"}}}},
			want: ReasoningCapabilities{Supported: true, Levels: levels("off", "low", "medium", "high", "xhigh", "max")},
		},
		{
			name: "toggle adds off",
			in:   modelEntry{Reasoning: true, ReasoningOptions: []reasoningOption{{Type: "toggle"}, {Type: "effort", Values: []string{"high", "max"}}}},
			want: ReasoningCapabilities{Supported: true, Levels: levels("off", "high", "max")},
		},
		{
			name: "toggle only leaves levels unknown",
			in:   modelEntry{Reasoning: true, ReasoningOptions: []reasoningOption{{Type: "toggle"}}},
			want: ReasoningCapabilities{Supported: true},
		},
		{
			name: "budget only leaves levels unknown",
			in:   modelEntry{Reasoning: true, ReasoningOptions: []reasoningOption{{Type: "budget_tokens"}}},
			want: ReasoningCapabilities{Supported: true},
		},
		{
			name: "default and unknown values are ignored",
			in:   modelEntry{Reasoning: true, ReasoningOptions: []reasoningOption{{Type: "effort", Values: []string{"default", "future"}}}},
			want: ReasoningCapabilities{Supported: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseReasoningCapabilities(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("capabilities = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLookupReasoningCapabilities(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		model    string
		found    bool
		want     ReasoningCapabilities
	}{
		{
			name:     "gateway model resolves by first-party name",
			endpoint: "https://gateway.example/openai/v1",
			model:    "gpt-5.6-sol",
			found:    true,
			want: ReasoningCapabilities{Supported: true, Levels: []llm.ThinkingLevel{
				llm.ThinkingLevelOff, llm.ThinkingLevelLow, llm.ThinkingLevelMedium,
				llm.ThinkingLevelHigh, llm.ThinkingLevelXHigh, llm.ThinkingLevelMax,
			}},
		},
		{
			name:  "date suffix is stripped",
			model: "claude-haiku-4-5-20251001",
			found: true,
			want:  ReasoningCapabilities{Supported: true},
		},
		{name: "unknown", endpoint: "https://made-up.example", model: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := LookupReasoningCapabilities(tt.endpoint, tt.model)
			if found != tt.found || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("LookupReasoningCapabilities() = (%+v, %v), want (%+v, %v)", got, found, tt.want, tt.found)
			}
		})
	}
}

func TestLookupReleaseDate(t *testing.T) {
	for _, test := range []struct {
		endpoint string
		model    string
		want     string
	}{
		{"https://llm.int.exe.xyz/v1/messages", "claude-haiku-4-5", "2025-10-15"},
		{"https://llm.int.exe.xyz/v1", "gpt-5.6-luna", "2026-07-09"},
		{"https://llm.int.exe.xyz/v1", "accounts/fireworks/models/deepseek-v4-flash-0731", "2026-07-31"},
	} {
		got, found := LookupReleaseDate(test.endpoint, test.model)
		if !found || got != test.want {
			t.Errorf("LookupReleaseDate(%q, %q) = (%q, %v), want (%q, true)", test.endpoint, test.model, got, found, test.want)
		}
	}
}

func TestLookupCost(t *testing.T) {
	cases := []struct {
		name      string
		endpoint  string
		model     string
		wantFound bool
		wantIn    float64
		wantOut   float64
	}{
		// First-party models resolve by name alone even when the endpoint is
		// an unknown gateway host.
		{"anthropic via gateway", "https://llm.int.exe.xyz/v1/messages", "claude-opus-4-6", true, 5, 25},
		{"anthropic dated", "", "claude-sonnet-4-5-20250929", true, 3, 15},
		// OpenAI snapshot names carry a date suffix that models.dev omits.
		{"openai dated", "https://llm.int.exe.xyz/v1/responses", "gpt-5.5-2026-04-23", true, 5, 30},
		{"openai undated", "", "gpt-5.3-codex", true, 1.75, 14},
		{"fireworks full path", "", "accounts/fireworks/models/kimi-k2p6", true, 0.95, 4},
		{"unknown model", "", "predictable-v1", false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, found := LookupCost(tc.endpoint, tc.model)
			if found != tc.wantFound {
				t.Fatalf("LookupCost(%q, %q) found = %v, want %v", tc.endpoint, tc.model, found, tc.wantFound)
			}
			if c.Input != tc.wantIn || c.Output != tc.wantOut {
				t.Errorf("LookupCost(%q, %q) = %+v, want input=%v output=%v", tc.endpoint, tc.model, c, tc.wantIn, tc.wantOut)
			}
		})
	}
}

func TestLookupModalities(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		model    string
		want     Modalities
		wantOK   bool
	}{
		{
			name:     "openai multimodal",
			endpoint: "https://api.openai.com/v1",
			model:    "gpt-4o",
			want:     Modalities{Input: []string{"text", "image", "pdf"}, Output: []string{"text"}},
			wantOK:   true,
		},
		{
			name:     "custom base_url with rich modalities",
			endpoint: "https://opencode.ai/zen/v1",
			model:    "gemini-3-pro",
			want:     Modalities{Input: []string{"text", "image", "video", "audio", "pdf"}, Output: []string{"text"}},
			wantOK:   true,
		},
		{
			name:     "unknown model",
			endpoint: "https://api.openai.com/v1",
			model:    "made-up-model",
			wantOK:   false,
		},
		{
			name:     "unknown host",
			endpoint: "https://made-up.example.com/v1",
			model:    "some-model",
			wantOK:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := LookupModalities(c.endpoint, c.model)
			if ok != c.wantOK {
				t.Fatalf("LookupModalities(%q,%q) found = %v, want %v", c.endpoint, c.model, ok, c.wantOK)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("LookupModalities(%q,%q) = %+v, want %+v", c.endpoint, c.model, got, c.want)
			}
		})
	}
}
