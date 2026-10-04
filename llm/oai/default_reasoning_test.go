package oai

import (
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/models/modelsdev"
)

func TestServiceDefaultReasoningLevel(t *testing.T) {
	tests := []struct {
		name string
		svc  llm.Service
		want string
	}{
		{"chat none stays verbatim", &Service{ReasoningEffort: "none"}, "none"},
		{"responses none stays verbatim", &ResponsesService{ReasoningEffort: "none"}, "none"},
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

// An enabled service default must not become an enabled choice when this
// model's endpoint only advertises off. The API uses off, not the wire's none.
func TestOffOnlyControlsDoNotInventDefaultEffort(t *testing.T) {
	caps := &modelsdev.ReasoningCapabilities{Supported: true, Levels: []llm.ThinkingLevel{llm.ThinkingLevelOff}}
	for _, svc := range []llm.Service{
		&Service{ReasoningOverride: caps, ThinkingLevel: llm.ThinkingLevelMedium},
		&ResponsesService{ReasoningOverride: caps, ThinkingLevel: llm.ThinkingLevelMedium},
	} {
		if got := llm.ServiceDefaultReasoningLevel(svc); got != "off" {
			t.Errorf("%T default = %q, want off", svc, got)
		}
	}
}
