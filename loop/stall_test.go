package loop

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/llm"
)

// progressLLM reports a model event every gap, events times, then answers.
// With events == 0 it never reports progress and waits for cancellation.
type progressLLM struct {
	events int
	gap    time.Duration
}

func (p *progressLLM) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	for i := 0; i < p.events; i++ {
		select {
		case <-time.After(p.gap):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		llm.ReportProgress(ctx)
	}
	if p.events == 0 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &llm.Response{
		Role:       llm.MessageRoleAssistant,
		StopReason: llm.StopReasonEndTurn,
		Content:    []llm.Content{{Type: llm.ContentTypeText, Text: "done"}},
	}, nil
}

func (p *progressLLM) Provider() string       { return "" }
func (p *progressLLM) MaxImageDimension() int { return 2000 }
func (p *progressLLM) MaxImageBytes() int     { return 5 * 1024 * 1024 }
func (p *progressLLM) SupportsImages() bool   { return true }

func runStallTurn(t *testing.T, service llm.Service) ([]llm.Message, error) {
	var recorded []llm.Message
	l := NewLoop(Config{
		LLM: service,
		RecordMessage: func(ctx context.Context, m llm.Message, _ llm.Usage, _ []llm.PurposedUsage) error {
			recorded = append(recorded, m)
			return nil
		},
	})
	l.QueueUserMessage(llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "go"}},
	})
	err := l.ProcessOneTurn(t.Context())
	return recorded, err
}

// A response that keeps producing model events runs past stallTimeout, up to
// maxRequestDuration.
func TestSlowStreamingTurnCompletes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gap := stallTimeout / 2
		events := int((maxRequestDuration - time.Minute) / gap)
		recorded, err := runStallTurn(t, &progressLLM{events: events, gap: gap})
		if err != nil {
			t.Fatalf("ProcessOneTurn: %v", err)
		}
		if len(recorded) != 1 || recorded[0].Content[0].Text != "done" {
			t.Fatalf("recorded = %+v, want the final answer", recorded)
		}
	})
}

// A response that produces no model events is aborted after stallTimeout
// with an error that says so.
func TestStalledTurnAborts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		recorded, err := runStallTurn(t, &progressLLM{})
		if err == nil {
			t.Fatal("ProcessOneTurn succeeded, want stall error")
		}
		if got := time.Since(start); got != stallTimeout {
			t.Errorf("aborted after %s, want %s", got, stallTimeout)
		}
		if len(recorded) != 1 {
			t.Fatalf("recorded %d messages, want 1 error message", len(recorded))
		}
		msg := recorded[0]
		if msg.ErrorType != llm.ErrorTypeLLMRequest || !msg.ErrorRetryable {
			t.Errorf("error message type=%q retryable=%v, want retryable llm_request", msg.ErrorType, msg.ErrorRetryable)
		}
		text := msg.Content[0].Text
		want := "LLM request stalled: the model produced no output for 5m0s, so Shelley aborted the request. Press Retry to try again."
		if !strings.HasPrefix(text, want) {
			t.Errorf("error text = %q, want prefix %q", text, want)
		}
		if strings.Contains(text, "deadline exceeded") {
			t.Errorf("error text surfaces opaque deadline: %q", text)
		}
	})
}

// A response that keeps producing model events is still aborted at
// maxRequestDuration.
func TestOverlongTurnAborts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		recorded, err := runStallTurn(t, &progressLLM{events: 1 << 20, gap: time.Minute})
		if err == nil {
			t.Fatal("ProcessOneTurn succeeded, want time limit error")
		}
		if got := time.Since(start); got != maxRequestDuration {
			t.Errorf("aborted after %s, want %s", got, maxRequestDuration)
		}
		if len(recorded) != 1 {
			t.Fatalf("recorded %d messages, want 1 error message", len(recorded))
		}
		msg := recorded[0]
		if msg.ErrorType != llm.ErrorTypeLLMRequest || !msg.ErrorRetryable {
			t.Errorf("error message type=%q retryable=%v, want retryable llm_request", msg.ErrorType, msg.ErrorRetryable)
		}
		text := msg.Content[0].Text
		want := "LLM request took too long: the model was still responding after 1h0m0s, so Shelley aborted the request. Press Retry to try again."
		if !strings.HasPrefix(text, want) {
			t.Errorf("error text = %q, want prefix %q", text, want)
		}
	})
}
