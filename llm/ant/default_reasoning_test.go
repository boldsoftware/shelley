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
	for _, tc := range []struct {
		name, model          string
		override             *modelsdev.ReasoningCapabilities
		wantType, wantEffort string
	}{
		{"adaptive without override", Claude48Opus, nil, "adaptive", "max"},
		{"adaptive explicit levels", Claude48Opus, &modelsdev.ReasoningCapabilities{Supported: true, Levels: []llm.ThinkingLevel{llm.ThinkingLevelLow, llm.ThinkingLevelMedium, llm.ThinkingLevelHigh, llm.ThinkingLevelXHigh}}, "adaptive", "xhigh"},
		{"budget without override", "claude-sonnet-4-5", nil, "enabled", ""},
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
			} else if got.OutputConfig != nil || got.Thinking.BudgetTokens != 16384 {
				t.Fatalf("budget thinking changed: %+v, output_config %+v", got.Thinking, got.OutputConfig)
			}
		})
	}
}
