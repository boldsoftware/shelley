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
		// Un-configured Gemini uses its own dynamic default Shelley can't name.
		{"unset -> unknown", &Service{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.ServiceDefaultReasoningLevel(tc.svc); got != tc.want {
				t.Fatalf("ServiceDefaultReasoningLevel() = %q, want %q", got, tc.want)
			}
		})
	}
}
