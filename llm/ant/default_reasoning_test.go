package ant

import (
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/models/modelsdev"
)

func TestDefaultReasoningLevel(t *testing.T) {
	tests := []struct {
		name  string
		level llm.ThinkingLevel
		want  string
	}{
		// Default and Off both send no thinking through applyAnthropicThinking,
		// so both must surface as "off" (the honest, model-visible behavior).
		{"default", llm.ThinkingLevelDefault, "off"},
		{"off", llm.ThinkingLevelOff, "off"},
		{"medium", llm.ThinkingLevelMedium, "medium"},
		{"high", llm.ThinkingLevelHigh, "high"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{Model: Claude48Opus, ThinkingLevel: tc.level}
			if got := s.DefaultReasoningLevel(); got != tc.want {
				t.Fatalf("DefaultReasoningLevel() = %q, want %q", got, tc.want)
			}
			// Must satisfy the llm.DefaultReasoner contract.
			if got := llm.ServiceDefaultReasoningLevel(s); got != tc.want {
				t.Fatalf("ServiceDefaultReasoningLevel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExplicitControlsPreserveLegacyBudgetAndAdaptiveEncoding(t *testing.T) {
	const endpoint = "https://api.pioneer.ai/v1/messages"
	levels := &modelsdev.ReasoningCapabilities{Supported: true, Levels: []llm.ThinkingLevel{llm.ThinkingLevelLow, llm.ThinkingLevelMedium, llm.ThinkingLevelHigh}}
	for _, tc := range []struct {
		name, model          string
		override             *modelsdev.ReasoningCapabilities
		wantType, wantEffort string
		wantBudget           int
	}{
		{"adaptive without override", Claude48Opus, nil, "adaptive", "max", 0},
		{"adaptive explicit levels", Claude48Opus, &modelsdev.ReasoningCapabilities{Supported: true, Levels: []llm.ThinkingLevel{llm.ThinkingLevelLow, llm.ThinkingLevelMedium, llm.ThinkingLevelHigh, llm.ThinkingLevelXHigh}}, "adaptive", "xhigh", 0},
		{"adaptive provider alias", "us.anthropic.claude-opus-4-8-v1:0", levels, "adaptive", "high", 0},
		{"budget without override", "claude-sonnet-4-5", nil, "enabled", "", 16384},
		{"budget sonnet explicit levels", Claude45Sonnet, levels, "enabled", "", 16384},
		{"budget opus explicit levels", Claude45Opus, levels, "enabled", "", 16384},
		{"budget haiku explicit levels", Claude45Haiku, levels, "enabled", "", 16384},
		{"budget opus46 explicit levels", Claude46Opus, levels, "enabled", "", 16384},
		{"budget sonnet46 explicit levels", Claude46Sonnet, levels, "enabled", "", 16384},
		{"budget provider alias", "anthropic.claude-sonnet-4-5-20250929-v1:0", levels, "enabled", "", 16384},
		{"unknown alias explicit levels", "gateway-model", levels, "enabled", "", 16384},
		{"budget clamps to low", Claude45Sonnet, &modelsdev.ReasoningCapabilities{Supported: true, Levels: []llm.ThinkingLevel{llm.ThinkingLevelLow}}, "enabled", "", 2048},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{Model: tc.model, URL: endpoint, ThinkingLevel: llm.ThinkingLevelMax, ReasoningOverride: tc.override}
			got := s.fromLLMRequest(&llm.Request{})
			if got.Thinking == nil || got.Thinking.Type != tc.wantType {
				t.Fatalf("thinking = %+v, want %s", got.Thinking, tc.wantType)
			}
			if tc.wantEffort != "" {
				if got.OutputConfig == nil || got.OutputConfig.Effort != tc.wantEffort {
					t.Fatalf("output_config = %+v, want effort %s", got.OutputConfig, tc.wantEffort)
				}
			} else if got.OutputConfig != nil || got.Thinking.BudgetTokens != tc.wantBudget {
				t.Fatalf("budget thinking changed: %+v, output_config %+v", got.Thinking, got.OutputConfig)
			}
		})
	}
}

func TestDisableReasoningOverridesEndpointControls(t *testing.T) {
	for _, caps := range []*modelsdev.ReasoningCapabilities{
		nil,
		{Supported: true, Levels: []llm.ThinkingLevel{llm.ThinkingLevelHigh}},
	} {
		s := &Service{
			Model: Claude48Opus, DisableReasoning: true,
			ThinkingLevel: llm.ThinkingLevelHigh, ReasoningOverride: caps,
		}
		if s.SupportsReasoning() || len(s.SupportedReasoningLevels()) != 0 || s.DefaultReasoningLevel() != "off" {
			t.Fatalf("disabled reasoning exposed controls with override %+v", caps)
		}
		req := s.fromLLMRequest(&llm.Request{ThinkingLevel: llm.ThinkingLevelHigh})
		if req.Thinking != nil || req.OutputConfig != nil {
			t.Fatalf("disabled reasoning emitted thinking: %+v, output_config: %+v", req.Thinking, req.OutputConfig)
		}
	}
}
