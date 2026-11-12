package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"shelley.exe.dev/llm"
)

// customService allows custom response logic for testing
type customService struct {
	responses    []customResponse
	responseFunc func(req *llm.Request) (*llm.Response, error)
	callIndex    int
	mu           sync.Mutex
}

type customResponse struct {
	response *llm.Response
	err      error
}

func (s *customService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.responseFunc != nil {
		return s.responseFunc(req)
	}

	if s.callIndex >= len(s.responses) {
		// Default response
		return &llm.Response{
			Role:       llm.MessageRoleAssistant,
			StopReason: llm.StopReasonEndTurn,
			Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "No more responses configured"},
			},
		}, nil
	}

	resp := s.responses[s.callIndex]
	s.callIndex++
	return resp.response, resp.err
}

func (s *customService) Provider() string { return "" }

func (s *customService) MaxImageDimension() int {
	return 8000
}

func (s *customService) MaxImageBytes() int {
	return 0
}

// TestNoInterruptionNormalFlow verifies that normal tool chains work correctly

// TestNoInterruptionNormalFlow verifies that normal tool chains work correctly
// when no interruption is queued.
func TestNoInterruptionNormalFlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var toolCallCount atomic.Int32

		// Create a tool that tracks calls
		multiTool := &llm.Tool{
			Name:        "multi_tool",
			Description: "A tool",
			InputSchema: llm.MustSchema(`{"type": "object", "properties": {"step": {"type": "integer"}}}`),
			Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
				toolCallCount.Add(1)
				return llm.ToolOut{
					LLMContent: []llm.Content{
						{Type: llm.ContentTypeText, Text: "done"},
					},
				}
			},
		}

		recordMessage := func(ctx context.Context, message llm.Message, _ string, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
			return nil
		}

		// Service that makes 3 tool calls then finishes
		service := &customService{
			responseFunc: func(req *llm.Request) (*llm.Response, error) {
				toolResults := 0
				for _, msg := range req.Messages {
					for _, c := range msg.Content {
						if c.Type == llm.ContentTypeToolResult {
							toolResults++
						}
					}
				}

				if toolResults < 3 {
					return &llm.Response{
						Role:       llm.MessageRoleAssistant,
						StopReason: llm.StopReasonToolUse,
						Content: []llm.Content{
							{Type: llm.ContentTypeText, Text: "Calling tool"},
							{
								Type:      llm.ContentTypeToolUse,
								ID:        fmt.Sprintf("tool_%d", toolResults+1),
								ToolName:  "multi_tool",
								ToolInput: json.RawMessage(fmt.Sprintf(`{"step":%d}`, toolResults+1)),
							},
						},
					}, nil
				}

				return &llm.Response{
					Role:       llm.MessageRoleAssistant,
					StopReason: llm.StopReasonEndTurn,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "All done"},
					},
				}, nil
			},
		}

		loop := NewLoop(Config{
			LLM:           service,
			History:       []llm.Message{},
			Tools:         []*llm.Tool{multiTool},
			RecordMessage: recordMessage,
		})

		// Queue initial user message (no interruption)
		loop.QueueUserMessage(llm.Message{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "run tools"}},
		})

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var loopDone sync.WaitGroup
		loopDone.Go(func() {
			loop.Go(ctx)
		})

		// Let the turn run until the loop is idle.
		synctest.Wait()
		cancel()
		loopDone.Wait()

		if finalCount := toolCallCount.Load(); finalCount != 3 {
			t.Errorf("Expected 3 tool calls, got %d", finalCount)
		}
	})
}

func (s *customService) SupportsImages() bool { return true }
