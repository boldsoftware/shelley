package claudetool

import (
	"context"
	"encoding/json"
	"testing"
)

func TestThinkRun(t *testing.T) {
	input := struct {
		Thoughts string `json:"thoughts"`
	}{
		Thoughts: "This is a test thought",
	}

	inputBytes, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	result := thinkRun(context.Background(), inputBytes)

	if result.Error != nil {
		t.Errorf("unexpected error: %v", result.Error)
	}

	if len(result.LLMContent) == 0 {
		t.Error("expected LLM content")
	}

	if result.LLMContent[0].Text != "recorded" {
		t.Errorf("expected 'recorded', got %q", result.LLMContent[0].Text)
	}
}
