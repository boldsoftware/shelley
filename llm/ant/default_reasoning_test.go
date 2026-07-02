package ant

import (
	"testing"

	"shelley.exe.dev/llm"
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
