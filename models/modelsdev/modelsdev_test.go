package modelsdev

import (
	"encoding/json"
	"reflect"
	"sort"
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
		{"fireworks glm-5p3 text", "https://api.fireworks.ai/inference/v1", "accounts/fireworks/models/glm-5p3", true, false},
		{"fireworks glm-5p3-flash vision", "https://api.fireworks.ai/inference/v1", "accounts/fireworks/models/glm-5p3-flash", true, true},
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
	zen.ID = "opencode"
	zenGo := prov("https://opencode.ai/zen/go/v1", "m", false)
	zenGo.ID = "opencode-go"
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

func TestBestProviderForPathTieIsDeterministic(t *testing.T) {
	first := prov("https://gateway.example/v1", "m", true)
	first.ID = "first"
	second := prov("https://gateway.example/v1", "m", false)
	second.ID = "second"

	for i, providers := range [][]providerEntry{{second, first}, {first, second}} {
		got, ok := bestProviderForPath(providers, pathSegments("https://gateway.example/v1"), "m")
		if !ok || got.ID != first.ID {
			t.Fatalf("order %d: bestProviderForPath() = (%q, %v), want (%q, true)", i, got.ID, ok, first.ID)
		}
	}
}

func TestLookupReasoningSupport(t *testing.T) {
	cases := []struct {
		endpoint, model string
		want, found     bool
	}{
		{"https://api.openai.com/v1", "gpt-5.4", true, true},
		{"https://api.openai.com/v1", "gpt-4o", false, true},
		{"https://api.fireworks.ai/inference/v1", "accounts/fireworks/models/gpt-oss-120b", true, true},
		{"https://api.fireworks.ai/inference/v1", "accounts/fireworks/models/glm-5p3", true, true},
		{"https://api.fireworks.ai/inference/v1", "accounts/fireworks/models/glm-5p3-flash", true, true},
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
			name:     "GPT-6 Sol effort levels",
			endpoint: "https://gateway.example/openai/v1",
			model:    "gpt-6-sol",
			found:    true,
			want: ReasoningCapabilities{Supported: true, Levels: []llm.ThinkingLevel{
				llm.ThinkingLevelOff, llm.ThinkingLevelLow, llm.ThinkingLevelMedium,
				llm.ThinkingLevelHigh, llm.ThinkingLevelXHigh, llm.ThinkingLevelMax,
			}},
		},
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
		{
			name:  "Haiku 5.5 adaptive effort",
			model: "claude-haiku-5-5",
			found: true,
			want: ReasoningCapabilities{Supported: true, Levels: []llm.ThinkingLevel{
				llm.ThinkingLevelLow, llm.ThinkingLevelMedium, llm.ThinkingLevelHigh,
				llm.ThinkingLevelXHigh, llm.ThinkingLevelMax,
			}},
		},
		{
			name:     "fireworks glm-5p3 effort levels",
			endpoint: "https://api.fireworks.ai/inference/v1",
			model:    "accounts/fireworks/models/glm-5p3",
			found:    true,
			want: ReasoningCapabilities{Supported: true, Levels: []llm.ThinkingLevel{
				llm.ThinkingLevelLow, llm.ThinkingLevelHigh, llm.ThinkingLevelMax,
			}},
		},
		{
			name:     "fireworks glm-5p3-flash effort levels",
			endpoint: "https://api.fireworks.ai/inference/v1",
			model:    "accounts/fireworks/models/glm-5p3-flash",
			found:    true,
			want: ReasoningCapabilities{Supported: true, Levels: []llm.ThinkingLevel{
				llm.ThinkingLevelLow, llm.ThinkingLevelHigh, llm.ThinkingLevelMax,
			}},
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

func TestLookupInterleavedReasoningField(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		model    string
		want     string
		found    bool
	}{
		{
			name:     "gateway native Fireworks name",
			endpoint: "https://llm.int.exe.xyz/v1",
			model:    "accounts/fireworks/models/glm-5p2",
			want:     "reasoning_content",
			found:    true,
		},
		{
			name:     "gateway native Fireworks Kimi name",
			endpoint: "https://llm.int.exe.xyz/v1",
			model:    "accounts/fireworks/models/kimi-k3",
			want:     "reasoning_content",
			found:    true,
		},
		{
			name:     "public Fireworks slug matches final segment",
			endpoint: "https://proxy.example/v1",
			model:    "fireworks/kimi-k3",
			want:     "reasoning_content",
			found:    true,
		},
		{
			name:     "bare Fireworks name matches final segment",
			endpoint: "https://proxy.example/v1",
			model:    "glm-5p2",
			want:     "reasoning_content",
			found:    true,
		},
		{
			name:     "qualified OpenRouter Kimi does not inherit Fireworks metadata",
			endpoint: "https://proxy.example/v1",
			model:    "moonshotai/kimi-k3",
		},
		{
			name:     "case-insensitive OpenRouter Kimi does not inherit Fireworks metadata",
			endpoint: "https://proxy.example/v1",
			model:    "MOONSHOTAI/KIMI-K3",
		},
		{
			name:     "qualified OpenRouter DeepSeek does not inherit Fireworks metadata",
			endpoint: "https://proxy.example/v1",
			model:    "deepseek/deepseek-v4-pro-0813",
		},
		{
			name:     "ambiguous bare Kimi is unknown",
			endpoint: "https://proxy.example/v1",
			model:    "kimi-k3",
		},
		{
			name:     "known model without named field",
			endpoint: "https://llm.int.exe.xyz/v1",
			model:    "accounts/fireworks/models/gpt-oss-120b",
		},
		{name: "unknown", endpoint: "https://made-up.example", model: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := LookupInterleavedReasoningField(tt.endpoint, tt.model)
			if found != tt.found || got != tt.want {
				t.Fatalf("LookupInterleavedReasoningField() = (%q, %v), want (%q, %v)", got, found, tt.want, tt.found)
			}
		})
	}
}

func TestInterleavedMetadataUnmarshal(t *testing.T) {
	tests := []struct {
		json string
		want interleavedMetadata
	}{
		{json: `true`, want: interleavedMetadata{Supported: true}},
		{json: `false`},
		{json: `null`},
		{json: `{"field":"reasoning_content"}`, want: interleavedMetadata{Supported: true, Field: "reasoning_content"}},
	}
	for _, tt := range tests {
		var got interleavedMetadata
		if err := json.Unmarshal([]byte(tt.json), &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", tt.json, err)
		}
		if got != tt.want {
			t.Fatalf("Unmarshal(%s) = %+v, want %+v", tt.json, got, tt.want)
		}
	}
}

func TestLookupInProviderPrefersExactTailKey(t *testing.T) {
	exact := modelEntry{ReleaseDate: "exact"}
	nested := modelEntry{ReleaseDate: "nested"}
	provider := providerEntry{Models: map[string]modelEntry{
		"model":        exact,
		"vendor/model": nested,
	}}

	got, found := lookupInProvider(provider, "provider/model")
	if !found || got.ReleaseDate != exact.ReleaseDate {
		t.Fatalf("lookupInProvider() = (%+v, %v), want exact tail key", got, found)
	}
}

func TestLookupReleaseDate(t *testing.T) {
	for _, test := range []struct {
		endpoint string
		model    string
		want     string
	}{
		{"https://llm.int.exe.xyz/v1/messages", "claude-haiku-4-5", "2025-10-15"},
		{"https://llm.int.exe.xyz/v1/messages", "claude-haiku-5-5", "2026-10-07"},
		{"https://llm.int.exe.xyz/v1", "gpt-6.1-sol", "2026-09-29"},
		{"https://llm.int.exe.xyz/v1", "gpt-6-sol", "2026-09-22"},
		{"https://llm.int.exe.xyz/v1", "gpt-6-luna", "2026-09-22"},
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
		want      Cost
	}{
		// First-party models resolve by name alone even when the endpoint is
		// an unknown gateway host.
		{"anthropic via gateway", "https://llm.int.exe.xyz/v1/messages", "claude-opus-4-6", true, Cost{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25}},
		{"sonnet 5.5 via gateway", "https://llm.int.exe.xyz/v1/messages", "claude-sonnet-5-5", true, Cost{Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5}},
		{"haiku 5.5 via gateway", "https://llm.int.exe.xyz/v1/messages", "claude-haiku-5-5", true, Cost{Input: 0.1, Output: 0.5, CacheRead: 0.01, CacheWrite: 0.125}},
		{"anthropic dated", "", "claude-sonnet-4-5-20250929", true, Cost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}},
		// OpenAI snapshot names carry a date suffix that models.dev omits.
		{"openai dated", "https://llm.int.exe.xyz/v1/responses", "gpt-5.5-2026-04-23", true, Cost{Input: 5, Output: 30, CacheRead: 0.5}},
		{"openai undated", "", "gpt-5.3-codex", true, Cost{Input: 1.75, Output: 14, CacheRead: 0.175}},
		{"astra via gateway", "https://llm.int.exe.xyz/v1/responses", "gpt-6-astra", true, Cost{Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5}},
		{"sol 6.1 via gateway", "https://llm.int.exe.xyz/v1/responses", "gpt-6.1-sol", true, Cost{Input: 2, Output: 10, CacheRead: 0.1, CacheWrite: 2.5}},
		{"sol via gateway", "https://llm.int.exe.xyz/v1/responses", "gpt-6-sol", true, Cost{Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5}},
		{"luna via gateway", "https://llm.int.exe.xyz/v1/responses", "gpt-6-luna", true, Cost{Input: 0.1, Output: 0.5, CacheRead: 0.01, CacheWrite: 0.125}},
		{"fireworks full path", "", "accounts/fireworks/models/kimi-k2p6", true, Cost{Input: 0.95, Output: 4, CacheRead: 0.16}},
		{"fireworks glm-5p3", "", "accounts/fireworks/models/glm-5p3", true, Cost{Input: 1.4, Output: 4.4, CacheRead: 0.26}},
		{"fireworks glm-5p3-flash", "", "accounts/fireworks/models/glm-5p3-flash", true, Cost{Input: 0.15, Output: 0.5, CacheRead: 0.03}},
		{"unknown model", "", "predictable-v1", false, Cost{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, found := LookupCost(tc.endpoint, tc.model)
			if found != tc.wantFound {
				t.Fatalf("LookupCost(%q, %q) found = %v, want %v", tc.endpoint, tc.model, found, tc.wantFound)
			}
			c.Tiers = nil // pricing tiers are exercised by TestLookupContextLimit
			if !reflect.DeepEqual(c, tc.want) {
				t.Errorf("LookupCost(%q, %q) = %+v, want %+v", tc.endpoint, tc.model, c, tc.want)
			}
		})
	}
}

func TestLookupCostTiers(t *testing.T) {
	// Haiku 5.5 reprices 5x once the prompt exceeds 100k tokens.
	c, found := LookupCost("https://api.anthropic.com/v1/messages", "claude-haiku-5-5")
	if !found {
		t.Fatal("LookupCost(haiku-5-5) not found")
	}
	want := []CostTier{{Context: 100000, Input: 0.5, Output: 2.5, CacheRead: 0.05, CacheWrite: 0.625}}
	if !reflect.DeepEqual(c.Tiers, want) {
		t.Fatalf("haiku-5-5 tiers = %+v, want %+v", c.Tiers, want)
	}
	// A flat-priced model carries no tiers.
	if c, _ := LookupCost("", "claude-haiku-4-5"); c.Tiers != nil {
		t.Errorf("claude-haiku-4-5 tiers = %+v, want nil", c.Tiers)
	}
}

func TestCostAtContext(t *testing.T) {
	base := Cost{Input: 0.1, Output: 0.5, CacheRead: 0.01, CacheWrite: 0.125}
	tiered := base
	tiered.Tiers = []CostTier{{Context: 100000, Input: 0.5, Output: 2.5, CacheRead: 0.05, CacheWrite: 0.625}}
	cases := []struct {
		name   string
		cost   Cost
		prompt int
		want   Cost
	}{
		{"flat pricing ignores prompt size", base, 500000, base},
		{"below threshold uses base", tiered, 50000, base},
		{"at threshold uses base", tiered, 100000, base},
		{"above threshold uses tier", tiered, 100001, Cost{Input: 0.5, Output: 2.5, CacheRead: 0.05, CacheWrite: 0.625}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cost.AtContext(tc.prompt)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("AtContext(%d) = %+v, want %+v", tc.prompt, got, tc.want)
			}
		})
	}
}

func TestCostAtContextMultipleTiers(t *testing.T) {
	// Decode order shouldn't matter: tiers sort ascending, so the highest
	// crossed threshold wins.
	c := Cost{Input: 1, Output: 2}
	c.Tiers = []CostTier{
		{Context: 400000, Input: 4, Output: 8},
		{Context: 200000, Input: 2, Output: 4},
	}
	sort.Slice(c.Tiers, func(i, j int) bool { return c.Tiers[i].Context < c.Tiers[j].Context })
	if got := c.AtContext(100000); got.Input != 1 {
		t.Errorf("AtContext(100000).Input = %v, want 1", got.Input)
	}
	if got := c.AtContext(250000); got.Input != 2 {
		t.Errorf("AtContext(250000).Input = %v, want 2", got.Input)
	}
	if got := c.AtContext(500000); got.Input != 4 {
		t.Errorf("AtContext(500000).Input = %v, want 4", got.Input)
	}
}

func TestLookupAnthropicOutputLimit(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		model    string
		want     int
		found    bool
	}{
		{"canonical Anthropic", "https://api.anthropic.com/v1/messages", "claude-opus-4-5-20251101", 64000, true},
		{"gateway Claude resolves canonical catalog", "https://llm.int.exe.xyz/anthropic/v1/messages", "claude-sonnet-5", 128000, true},
		{"sonnet 5.5 uses snapshot limit", "https://llm.int.exe.xyz/anthropic/v1/messages", "claude-sonnet-5-5", 128000, true},
		{"haiku 5.5 uses snapshot limit", "https://llm.int.exe.xyz/anthropic/v1/messages", "claude-haiku-5-5", 128000, true},
		{"date alias resolves canonical catalog", "", "claude-opus-4-5-2026-01-01", 64000, true},
		// The Fireworks endpoint must win over OpenRouter's differently priced
		// gpt-oss-120b entry. No cross-provider catalog scan is allowed.
		{"endpoint-specific entry does not spill providers", "https://api.fireworks.ai/inference/v1", "accounts/fireworks/models/gpt-oss-120b", 32768, true},
		{"unknown has no invented limit", "https://made-up.example/v1", "custom-claude", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := LookupAnthropicOutputLimit(tc.endpoint, tc.model)
			if found != tc.found || got != tc.want {
				t.Fatalf("LookupAnthropicOutputLimit(%q, %q) = (%d, %v), want (%d, %v)", tc.endpoint, tc.model, got, found, tc.want, tc.found)
			}
		})
	}
}

func TestLookupOutputLimit(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		model    string
		want     int
		found    bool
	}{
		{"OpenAI endpoint", "https://api.openai.com/v1", "gpt-5.4", 128000, true},
		{"GPT-6.1 Sol", "https://api.openai.com/v1", "gpt-6.1-sol", 128000, true},
		{"GPT-6 Luna", "https://api.openai.com/v1", "gpt-6-luna", 128000, true},
		{"Google endpoint", "https://generativelanguage.googleapis.com/v1beta", "gemini-3-flash-preview", 65536, true},
		{"Fireworks endpoint", "https://api.fireworks.ai/inference/v1", "accounts/fireworks/models/gpt-oss-120b", 32768, true},
		{"gateway falls back by model name", "https://llm.int.exe.xyz/v1", "gpt-5.4", 128000, true},
		{"unknown has no invented limit", "https://made-up.example/v1", "custom-model", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := LookupOutputLimit(tc.endpoint, tc.model)
			if found != tc.found || got != tc.want {
				t.Fatalf("LookupOutputLimit(%q, %q) = (%d, %v), want (%d, %v)", tc.endpoint, tc.model, got, found, tc.want, tc.found)
			}
		})
	}
}

func TestLookupContextLimit(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		model    string
		want     int
		found    bool
	}{
		{"OpenAI clamps GPT-6 to the context pricing tier", "https://api.openai.com/v1", "gpt-6-sol", 272000, true},
		{"OpenAI clamps to the context pricing tier", "https://api.openai.com/v1", "gpt-5.6-sol", 272000, true},
		{"Anthropic 1M has no tier", "https://api.anthropic.com", "claude-opus-5", 1000000, true},
		{"Haiku 5.5 clamps at pricing cliff", "https://api.anthropic.com", "claude-haiku-5-5", 100000, true},
		{"Anthropic 200k", "https://api.anthropic.com", "claude-opus-4-5-20251101", 200000, true},
		{"unknown has no invented limit", "https://made-up.example/v1", "custom-model", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := LookupContextLimit(tc.endpoint, tc.model)
			if found != tc.found || got != tc.want {
				t.Fatalf("LookupContextLimit(%q, %q) = (%d, %v), want (%d, %v)", tc.endpoint, tc.model, got, found, tc.want, tc.found)
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
			name:     "GPT-6 Sol multimodal",
			endpoint: "https://api.openai.com/v1",
			model:    "gpt-6-sol",
			want:     Modalities{Input: []string{"text", "image", "pdf"}, Output: []string{"text"}},
			wantOK:   true,
		},
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
