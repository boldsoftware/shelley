package oai

import (
	"testing"

	"shelley.exe.dev/llm"
)

func TestServiceDefaultReasoningLevel(t *testing.T) {
	tests := []struct {
		name string
		svc  llm.Service
		want string
	}{
		{"chat verbatim wins", &Service{ProviderName: "fireworks", ReasoningEffort: "high", ThinkingLevel: llm.ThinkingLevelMedium}, "high"},
		{"chat service default", &Service{ProviderName: "fireworks", ThinkingLevel: llm.ThinkingLevelMedium}, "medium"},
		// Unset: provider picks its own default, which Shelley can't name.
		{"chat unset -> unknown", &Service{ProviderName: "fireworks"}, ""},
		{"responses verbatim wins", &ResponsesService{ProviderName: "openai", ReasoningEffort: "xhigh", ThinkingLevel: llm.ThinkingLevelMedium}, "xhigh"},
		{"responses service default", &ResponsesService{ProviderName: "openai", ThinkingLevel: llm.ThinkingLevelMedium}, "medium"},
		{"responses unset -> unknown", &ResponsesService{ProviderName: "openai"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.ServiceDefaultReasoningLevel(tc.svc); got != tc.want {
				t.Fatalf("ServiceDefaultReasoningLevel() = %q, want %q", got, tc.want)
			}
		})
	}
}
