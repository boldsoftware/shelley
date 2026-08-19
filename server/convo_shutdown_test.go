package server

import (
	"context"
	"errors"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
)

type shutdownBlockingService struct {
	started chan struct{}
}

func (s *shutdownBlockingService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	close(s.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*shutdownBlockingService) Provider() string        { return "test" }
func (*shutdownBlockingService) TokenContextWindow() int { return 200000 }
func (*shutdownBlockingService) MaxImageDimension() int  { return 2000 }
func (*shutdownBlockingService) MaxImageBytes() int      { return 5 * 1024 * 1024 }
func (*shutdownBlockingService) SupportsImages() bool    { return false }

func TestStopLoopWaitsForDurableCloseout(t *testing.T) {
	service := &shutdownBlockingService{started: make(chan struct{})}
	recordStarted := make(chan struct{})
	releaseRecord := make(chan struct{})
	recordCount := 0
	agentLoop := loop.NewLoop(loop.Config{
		LLM: service,
		RecordMessage: func(ctx context.Context, message llm.Message, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			recordCount++
			close(recordStarted)
			<-releaseRecord
			return nil
		},
	})
	agentLoop.QueueUserMessage(llm.UserStringMessage("start"))

	cancelCtx, cancel := context.WithCancelCause(context.Background())
	processCtx, timeoutCancel := context.WithCancel(cancelCtx)
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		_ = agentLoop.Go(processCtx)
	}()
	<-service.started

	manager := &ConversationManager{
		loop:              agentLoop,
		loopCancel:        cancel,
		loopTimeoutCancel: timeoutCancel,
		loopDone:          loopDone,
		loopCtx:           processCtx,
	}
	stopped := make(chan struct{})
	go func() {
		manager.stopLoopAndWait(context.Background())
		close(stopped)
	}()

	<-recordStarted
	if !errors.Is(context.Cause(processCtx), loop.ErrShutdown) {
		t.Fatalf("cancellation cause = %v, want loop.ErrShutdown", context.Cause(processCtx))
	}
	select {
	case <-stopped:
		t.Fatal("stopLoop returned before the closeout message was recorded")
	default:
	}

	close(releaseRecord)
	<-stopped
	if recordCount != 1 {
		t.Fatalf("record count = %d, want 1", recordCount)
	}
}
