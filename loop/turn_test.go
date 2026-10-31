package loop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

func TestQueuedTurnOverrideAndRetry(t *testing.T) {
	service := predictable.NewService()
	agent := NewLoop(Config{
		LLM: service, ModelID: "base", System: []llm.SystemContent{{Text: "BASE"}},
		RecordMessage: func(context.Context, llm.Message, llm.Usage, []llm.PurposedUsage) error { return nil },
	})
	retryable := errors.New("retry this turn")
	attempts := 0
	agent.QueueTurn(llm.UserStringMessage("+Probe hello"), func(ctx context.Context, turn RunConfig) error {
		attempts++
		if attempts == 1 {
			return retryable
		}
		turn.System = []llm.SystemContent{{Text: "PROFILE"}}
		turn.CurrentSystem = nil
		return Run(ctx, turn)
	})
	agent.QueueUserMessage(llm.UserStringMessage("ordinary followup"))
	if err := agent.ProcessOneTurn(t.Context()); !errors.Is(err, retryable) {
		t.Fatalf("first attempt: %v", err)
	}
	// Retry the failed turn without consuming the next queued message.
	if err := agent.processLLMRequest(t.Context()); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("override attempts = %d", attempts)
	}
	req := service.GetLastRequest()
	if req.System[0].Text != "PROFILE" {
		t.Fatal("retry lost its profile")
	}
	for _, message := range req.Messages {
		for _, content := range message.Content {
			if strings.Contains(content.Text, "ordinary followup") {
				t.Fatal("ordinary message was injected into the profile turn")
			}
		}
	}
	if err := agent.ProcessOneTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if req := service.GetLastRequest(); req.System[0].Text != "BASE" {
		t.Fatal("override changed the loop defaults")
	}
}
