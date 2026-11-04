package loop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

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

// duringRequest runs a callback while the model writes the first response.
type duringRequest struct {
	*predictable.Service
	once func()
}

func (s *duringRequest) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	if s.once != nil {
		s.once()
		s.once = nil
	}
	return s.Service.Do(ctx, req)
}

// A message that arrives while the model writes the final reply runs in a
// turn of its own, which Go marks the start of. Meanwhile, and when the turn
// before ends, WillRunTurn reports it.
func TestOnTurnStartPrecedesEveryTurn(t *testing.T) {
	service := &duringRequest{Service: predictable.NewService()}
	events := make(chan string, 8)
	var agent *Loop
	agent = NewLoop(Config{
		LLM:         service,
		OnTurnStart: func() { events <- fmt.Sprint("start ", agent.WillRunTurn()) },
		RecordMessage: func(_ context.Context, m llm.Message, _ llm.Usage, _ []llm.PurposedUsage) error {
			if m.EndOfTurn {
				events <- fmt.Sprint("end ", agent.WillRunTurn())
			}
			return nil
		},
	})
	service.once = func() { agent.QueueUserMessage(llm.UserStringMessage("echo: second")) }
	agent.QueueUserMessage(llm.UserStringMessage("echo: first"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go agent.Go(ctx)
	var got []string
	for len(got) < 4 {
		select {
		case e := <-events:
			got = append(got, e)
		case <-time.After(10 * time.Second):
			t.Fatalf("events = %v, timed out", got)
		}
	}
	if want := []string{"start true", "end true", "start true", "end false"}; !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}
