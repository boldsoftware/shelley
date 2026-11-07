package gem

import (
	"testing"

	"shelley.exe.dev/llm"
)

func TestDefaultReasoningLevel(t *testing.T) {
	tests := []struct {
		name string
		svc  *Service
		want string
	}{
		{"verbatim effort wins", &Service{ReasoningEffort: "high", ThinkingLevel: llm.ThinkingLevelMedium}, "high"},
		{"service default", &Service{ThinkingLevel: llm.ThinkingLevelMedium}, "medium"},
		{"Flash documented default", &Service{}, "medium"},
		{"Argon documented default", &Service{Model: "gemini-4-argon"}, "medium"},
		{"Argon resource name", &Service{Model: "models/gemini-4-argon"}, "medium"},
		{"generic level reflects clamping", &Service{ThinkingLevel: llm.ThinkingLevelXHigh}, "high"},
		{"budget level reflects clamping", &Service{Model: "gemini-2.5-pro", ThinkingLevel: llm.ThinkingLevelXHigh}, "high"},
		{"invalid effort is not advertised", &Service{Model: "gemini-4-argon", ReasoningEffort: "minimal"}, ""},
		{"unsupported off is not advertised", &Service{Model: "gemini-4-argon", ThinkingLevel: llm.ThinkingLevelOff}, ""},
		{"budget default is dynamic", &Service{Model: "gemini-2.5-pro"}, ""},
		{"Flash Lite default is off", &Service{Model: "gemini-2.5-flash-lite"}, "off"},
		{"model without thinking", &Service{Model: "gemini-2.5-flash-preview-tts"}, "off"},
		{"unknown default", &Service{Model: "custom-unknown"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.ServiceDefaultReasoningLevel(tc.svc); got != tc.want {
				t.Fatalf("ServiceDefaultReasoningLevel() = %q, want %q", got, tc.want)
			}
		})
	}
}
