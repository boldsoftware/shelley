package loop

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"shelley.exe.dev/llm"
)

// recordingService wraps another llm.Service and captures each request.
type recordingService struct {
	llm.Service
	mu   sync.Mutex
	reqs []*llm.Request
}

func (s *recordingService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	captured := *req
	captured.Messages = append([]llm.Message(nil), req.Messages...)
	s.reqs = append(s.reqs, &captured)
	s.mu.Unlock()
	return s.Service.Do(ctx, req)
}

// TestInjectMessagesMidTurn verifies that messages returned by the
// InjectMessages callback are spliced into history between tool rounds, so
// the very next LLM request already carries them (e.g. a subagent completion
// notification the parent should react to immediately, mid-turn).
func TestInjectMessagesMidTurn(t *testing.T) {
	echoTool := &llm.Tool{
		Name:        "echo",
		Description: "echoes",
		InputSchema: llm.MustSchema(`{"type": "object", "properties": {}}`),
		Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
			return llm.ToolOut{LLMContent: []llm.Content{{Type: llm.ContentTypeText, Text: "echoed"}}}
		},
	}

	service := &recordingService{Service: &customService{
		responseFunc: func(req *llm.Request) (*llm.Response, error) {
			// Any request that already carries a tool result ends the turn;
			// the first request calls the tool.
			for _, msg := range req.Messages {
				for _, c := range msg.Content {
					if c.Type == llm.ContentTypeToolResult {
						return &llm.Response{
							Role:       llm.MessageRoleAssistant,
							StopReason: llm.StopReasonEndTurn,
							Content:    []llm.Content{{Type: llm.ContentTypeText, Text: "done"}},
						}, nil
					}
				}
			}
			return &llm.Response{
				Role:       llm.MessageRoleAssistant,
				StopReason: llm.StopReasonToolUse,
				Content: []llm.Content{{
					Type: llm.ContentTypeToolUse, ID: "tool_1",
					ToolName: "echo", ToolInput: json.RawMessage(`{}`),
				}},
			}, nil
		},
	}}

	injectedPair := []llm.Message{
		{Role: llm.MessageRoleAssistant, Content: []llm.Content{{
			Type: llm.ContentTypeToolUse, ID: "sa_done_1", ToolName: "subagent",
			ToolInput: json.RawMessage(`{}`),
		}}},
		{Role: llm.MessageRoleUser, Content: []llm.Content{{
			Type: llm.ContentTypeToolResult, ToolUseID: "sa_done_1",
			ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "subagent finished"}},
		}}},
	}

	var calls atomic.Int64
	loop := NewLoop(Config{
		LLM:   service,
		Tools: []*llm.Tool{echoTool},
		RecordMessage: func(ctx context.Context, message llm.Message, _ string, usage llm.Usage, purposed []llm.PurposedUsage) error {
			return nil
		},
		InjectMessages: func(ctx context.Context) (Injection, error) {
			// The first call happens before the first request; inject only on
			// the call after the tool round.
			if calls.Add(1) == 2 {
				return Injection{Messages: injectedPair}, nil
			}
			return Injection{}, nil
		},
	})

	loop.QueueUserMessage(llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "go"}},
	})
	if err := loop.ProcessOneTurn(t.Context()); err != nil {
		t.Fatalf("ProcessOneTurn: %v", err)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.reqs) != 2 {
		t.Fatalf("expected 2 LLM requests, got %d", len(service.reqs))
	}
	// Second request must carry: user, assistant(tool_use), user(tool_result),
	// assistant(injected tool_use), user(injected tool_result).
	msgs := service.reqs[1].Messages
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages in second request, got %d: %+v", len(msgs), msgs)
	}
	if msgs[3].Content[0].ID != "sa_done_1" {
		t.Errorf("expected injected tool_use at index 3, got %+v", msgs[3])
	}
	if msgs[4].Content[0].ToolUseID != "sa_done_1" {
		t.Errorf("expected injected tool_result at index 4, got %+v", msgs[4])
	}
}

// TestInjectHistoryReplacesHistory verifies that an Injection's History and
// System replace the in-flight turn's messages and system prompt, and the
// loop's, for later turns.
func TestInjectHistoryReplacesHistory(t *testing.T) {
	echoTool := &llm.Tool{
		Name:        "echo",
		InputSchema: llm.MustSchema(`{"type": "object", "properties": {}}`),
		Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
			return llm.ToolOut{LLMContent: llm.TextContent("echoed")}
		},
	}
	service := &recordingService{Service: &customService{
		responseFunc: func(req *llm.Request) (*llm.Response, error) {
			if len(req.Messages) > 1 {
				return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn, Content: llm.TextContent("done")}, nil
			}
			return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonToolUse, Content: []llm.Content{{
				Type: llm.ContentTypeToolUse, ID: "tool_1", ToolName: "echo", ToolInput: json.RawMessage(`{}`),
			}}}, nil
		},
	}}
	replacement := []llm.Message{
		{Role: llm.MessageRoleUser, Content: llm.TextContent("compacted")},
		{Role: llm.MessageRoleUser, Content: llm.TextContent("tail")},
	}
	var calls atomic.Int64
	fresh := []llm.SystemContent{{Type: "text", Text: "fresh"}}
	loop := NewLoop(Config{
		LLM:    service,
		Tools:  []*llm.Tool{echoTool},
		System: []llm.SystemContent{{Type: "text", Text: "stale"}},
		RecordMessage: func(context.Context, llm.Message, string, llm.Usage, []llm.PurposedUsage) error {
			return nil
		},
		InjectMessages: func(ctx context.Context) (Injection, error) {
			if calls.Add(1) == 2 {
				return Injection{History: replacement, System: fresh, Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("injected")}}}, nil
			}
			return Injection{}, nil
		},
	})
	loop.QueueUserMessage(llm.Message{Role: llm.MessageRoleUser, Content: llm.TextContent("go")})
	if err := loop.ProcessOneTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	texts := func(msgs []llm.Message) string {
		var s []string
		for _, m := range msgs {
			s = append(s, m.Content[0].Text)
		}
		return strings.Join(s, ",")
	}
	if got := texts(service.reqs[1].Messages); got != "compacted,tail,injected" {
		t.Fatalf("second request = %s", got)
	}
	if got := texts(loop.GetHistory()); got != "compacted,tail,injected,done" {
		t.Fatalf("history = %s", got)
	}
	if got := service.reqs[0].System[0].Text; got != "stale" {
		t.Fatalf("first request system = %s", got)
	}
	if got := service.reqs[1].System[0].Text; got != "fresh" {
		t.Fatalf("second request system = %s", got)
	}
	service.mu.Unlock()
	loop.QueueUserMessage(llm.Message{Role: llm.MessageRoleUser, Content: llm.TextContent("again")})
	if err := loop.ProcessOneTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	if got := service.reqs[len(service.reqs)-1].System[0].Text; got != "fresh" {
		t.Fatalf("next turn system = %s", got)
	}
}
