package loop

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

// blockingService blocks in Do until its context is cancelled, then returns
// the context error. Mimics an in-flight LLM request interrupted by a user
// cancel (CancelConversation) or a context timeout.
type blockingService struct{}

func (s *blockingService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (s *blockingService) Provider() string        { return "" }
func (s *blockingService) TokenContextWindow() int { return 200000 }
func (s *blockingService) MaxImageDimension() int  { return 0 }
func (s *blockingService) MaxImageBytes() int      { return 0 }
func (s *blockingService) SupportsImages() bool    { return false }

// failingService blocks until its context dies, then fails with a
// non-retryable error. Mimics a slow LLM request that outlives the turn
// context and THEN fails: by the time the loop records the user-facing error
// row, ctx is already expired.
type failingService struct{}

func (s *failingService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	<-ctx.Done()
	return nil, errors.New("status 400: bad request")
}
func (s *failingService) Provider() string        { return "" }
func (s *failingService) TokenContextWindow() int { return 200000 }
func (s *failingService) MaxImageDimension() int  { return 0 }
func (s *failingService) MaxImageBytes() int      { return 0 }
func (s *failingService) SupportsImages() bool    { return false }

// TestCancelledTurnDoesNotRecordErrorMessage verifies that when the loop's
// context is cancelled mid-request (the user hit Stop; CancelConversation
// records its own "[Operation cancelled]" bookkeeping), the loop does NOT try
// to record an LLM-error message. Pre-fix it did, and the write always failed
// with "failed to create message: Tx: context canceled" because the recording
// context was already dead.
func TestCancelledTurnDoesNotRecordErrorMessage(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var recorded []llm.Message
	l := NewLoop(Config{
		LLM: &blockingService{},
		// Accept every write, even on a dead context. The point under test is
		// that the loop never ATTEMPTS to record an LLM-error row for a
		// cancelled turn — CancelConversation owns that bookkeeping — so a
		// permissive recorder is what discriminates: pre-fix the loop called
		// it (via a dead ctx) and a row would land here.
		RecordMessage: func(ctx context.Context, message llm.Message, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
			mu.Lock()
			recorded = append(recorded, message)
			mu.Unlock()
			return nil
		},
	})
	l.QueueUserMessage(llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Go(ctx) }()

	// Give the loop a moment to enter the blocked Do, then cancel like
	// CancelConversation does.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop after cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, m := range recorded {
		if m.ErrorType == llm.ErrorTypeLLMRequest {
			t.Fatalf("cancelled turn recorded an LLM error message: %+v", m)
		}
	}
}

// TestLLMErrorRecordedDespiteExpiredContext verifies the inverse case: a
// genuine LLM failure must record its user-visible error row (whose
// MarkAgentDone clears agent_working) even if the turn context died between
// the failure and the write. Otherwise the conversation is stuck showing
// "Agent working..." with no error bubble and no Retry affordance.
func TestLLMErrorRecordedDespiteExpiredContext(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var recorded []llm.Message
	l := NewLoop(Config{
		LLM: &failingService{},
		// Enforce what the real recorder (db.Pool.Tx) does: refuse writes on
		// a dead context. Pre-fix the loop recorded on the turn's own (now
		// expired) ctx, so this guard rejected the write and the test failed.
		RecordMessage: func(ctx context.Context, message llm.Message, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			mu.Lock()
			recorded = append(recorded, message)
			mu.Unlock()
			return nil
		},
	})
	l.QueueUserMessage(llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}},
	})

	// A short deadline: failingService blocks until the deadline expires and
	// THEN fails, so by the time the loop records the error message the turn
	// ctx is already dead — the state a conversation reaches when its outer
	// loop context dies (e.g. the 12-hour processCtx ceiling) while a request
	// is in flight. ctx.Err() here is DeadlineExceeded, not Canceled, so the
	// loop must take the error-record path (not the user-cancel skip) and
	// write via WithoutCancel.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(150*time.Millisecond))
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- l.Go(ctx) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop")
	}

	mu.Lock()
	defer mu.Unlock()
	var found bool
	for _, m := range recorded {
		if m.ErrorType == llm.ErrorTypeLLMRequest && m.EndOfTurn {
			found = true
		}
	}
	if !found {
		t.Fatalf("LLM failure did not record an end-of-turn error message; recorded: %d messages", len(recorded))
	}
}
