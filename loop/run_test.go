package loop

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"shelley.exe.dev/llm"
)

type runTestService struct {
	requests []*llm.Request
	first    []llm.Content
	response *llm.Response
	failure  error
}

func TestRunDynamicToolEndTurn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		endTurn    bool
		wantRounds int
	}{
		{name: "final report", endTurn: true, wantRounds: 1},
		{name: "progress report", endTurn: false, wantRounds: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &runTestService{}
			var recorded []llm.Message
			record := func(_ context.Context, m llm.Message) error {
				recorded = append(recorded, m)
				return nil
			}
			err := Run(t.Context(), RunConfig{
				LLM:      service,
				Messages: []llm.Message{llm.UserStringMessage("start")},
				Tools: []*llm.Tool{{Name: "work", Run: func(context.Context, json.RawMessage) llm.ToolOut {
					return llm.ToolOut{LLMContent: llm.TextContent("reported"), EndTurn: tc.endTurn}
				}}},
				Hooks: Hooks{
					OnResponse: func(_ context.Context, response Response) error {
						return record(t.Context(), response.Message)
					},
					OnToolResponse: func(_ context.Context, response ToolResponse) error {
						return record(t.Context(), response.Message)
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(service.requests); got != tc.wantRounds {
				t.Fatalf("model rounds = %d, want %d", got, tc.wantRounds)
			}
			if len(recorded) != 3 {
				t.Fatalf("recorded %d messages, want tool use, result, and terminal message", len(recorded))
			}
			last := recorded[len(recorded)-1]
			if !last.EndOfTurn || last.ExcludedFromContext != tc.endTurn {
				t.Fatalf("last message = %+v, want terminal marker only for final report", last)
			}
			if tc.endTurn && len(last.Content) != 0 {
				t.Fatalf("terminal marker should be invisible: %+v", last)
			}
		})
	}
}

func TestRunToolEndTurnRequiresPersistedMarker(t *testing.T) {
	service := &runTestService{}
	err := Run(t.Context(), RunConfig{
		LLM:      service,
		Messages: []llm.Message{llm.UserStringMessage("start")},
		Tools: []*llm.Tool{{Name: "work", Run: func(context.Context, json.RawMessage) llm.ToolOut {
			return llm.ToolOut{EndTurn: true}
		}}},
		Hooks: Hooks{OnResponse: func(_ context.Context, response Response) error {
			if response.Message.EndOfTurn {
				return errors.New("marker storage failed")
			}
			return nil
		}},
	})
	if !errors.Is(err, errMessagePersistence) {
		t.Fatalf("marker failure = %v, want message persistence failure", err)
	}
	if len(service.requests) != 1 {
		t.Fatalf("sent %d model requests despite terminal marker failure", len(service.requests))
	}
}

func TestRunConditionalTurnEndingToolMustBeCalledAlone(t *testing.T) {
	service := &runTestService{first: []llm.Content{
		{Type: llm.ContentTypeToolUse, ID: "final", ToolName: "report", ToolInput: json.RawMessage(`{"end_turn":true}`)},
		{Type: llm.ContentTypeToolUse, ID: "sibling", ToolName: "work", ToolInput: json.RawMessage(`{}`)},
	}}
	reported, worked := false, false
	err := Run(t.Context(), RunConfig{
		LLM:      service,
		Messages: []llm.Message{llm.UserStringMessage("start")},
		Tools: []*llm.Tool{
			{Name: "report", EndsTurnWhen: func(input json.RawMessage) bool {
				var req struct {
					EndTurn bool `json:"end_turn"`
				}
				return json.Unmarshal(input, &req) == nil && req.EndTurn
			}, Run: func(context.Context, json.RawMessage) llm.ToolOut {
				reported = true
				return llm.ToolOut{EndTurn: true}
			}},
			{Name: "work", Run: func(context.Context, json.RawMessage) llm.ToolOut {
				worked = true
				return llm.ToolOut{}
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reported || !worked {
		t.Fatalf("final report ran with siblings: reported=%v worked=%v", reported, worked)
	}
	if len(service.requests) != 2 {
		t.Fatalf("model requests=%d, want error result followed by another round", len(service.requests))
	}
	result := service.requests[1].Messages[2]
	if !result.Content[0].ToolError || !strings.Contains(result.Content[0].ToolResult[0].Text, "called alone") {
		t.Fatalf("final report did not receive a solo-call error: %+v", result)
	}
}

func (s *runTestService) Do(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.requests = append(s.requests, req)
	if len(s.requests) == 1 {
		if s.response != nil || s.failure != nil {
			return s.response, s.failure
		}
		content := s.first
		if content == nil {
			content = []llm.Content{{Type: llm.ContentTypeToolUse, ID: "job", ToolName: "work", ToolInput: json.RawMessage(`{}`)}}
		}
		return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonToolUse, Content: content}, nil
	}
	return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn, Content: llm.TextContent("done")}, nil
}

func (*runTestService) Provider() string       { return "test" }
func (*runTestService) MaxImageDimension() int { return 0 }
func (*runTestService) MaxImageBytes() int     { return 0 }
func (*runTestService) SupportsImages() bool   { return false }

func TestRunDrainsPendingBetweenModelRequests(t *testing.T) {
	service := &runTestService{}
	var recorded []llm.Message
	checks := 0
	err := Run(t.Context(), RunConfig{
		LLM:      service,
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("start")}},
		Tools: []*llm.Tool{{Name: "work", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Run: func(context.Context, json.RawMessage) llm.ToolOut {
			return llm.ToolOut{LLMContent: llm.TextContent("finished")}
		}}},
		Pending: func(_ context.Context, messages []llm.Message) ([]llm.Message, error) {
			checks++
			if checks == 2 {
				return append(messages, llm.Message{Role: llm.MessageRoleUser, Content: llm.TextContent("child completed")}), nil
			}
			return messages, nil
		},
		Hooks: Hooks{
			OnResponse: func(_ context.Context, response Response) error {
				recorded = append(recorded, response.Message)
				return nil
			},
			OnToolResponse: func(_ context.Context, response ToolResponse) error {
				recorded = append(recorded, response.Message)
				return nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if checks != 2 || len(service.requests) != 2 {
		t.Fatalf("pending checks = %d, requests = %d; want two of each", checks, len(service.requests))
	}
	messages := service.requests[1].Messages
	if len(messages) != 4 || messages[2].Content[0].ToolUseID != "job" || messages[3].Content[0].Text != "child completed" {
		t.Fatalf("second request lost tool-result / pending order: %+v", messages)
	}
	if len(recorded) != 3 || recorded[0].Role != llm.MessageRoleAssistant || recorded[1].Content[0].ToolUseID != "job" || recorded[2].Content[0].Text != "done" {
		t.Fatalf("persisted responses out of order: %+v", recorded)
	}
}

func TestRunConcurrentToolsPreserveResponseOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan string, 2)
		release := make(chan struct{})
		tools := make([]*llm.Tool, 0, 2)
		service := &runTestService{}
		for _, name := range []string{"first", "second"} {
			tools = append(tools, &llm.Tool{
				Name: name, InputSchema: llm.MustSchema(`{"type":"object","properties":{}}`),
				Run: func(context.Context, json.RawMessage) llm.ToolOut {
					started <- name
					<-release
					return llm.ToolOut{LLMContent: llm.TextContent(name)}
				},
			})
			service.first = append(service.first, llm.Content{Type: llm.ContentTypeToolUse, ID: name, ToolName: name, ToolInput: json.RawMessage(`{}`)})
		}
		done := make(chan error, 1)
		go func() {
			done <- Run(t.Context(), RunConfig{LLM: service, Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("work")}}, Tools: tools})
		}()
		synctest.Wait()
		if len(started) != 2 {
			t.Fatalf("only %d of 2 tools started concurrently", len(started))
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		results := service.requests[1].Messages[2].Content
		for i, name := range []string{"first", "second"} {
			if results[i].ToolUseID != name || results[i].ToolResult[0].Text != name {
				t.Fatalf("result %d = %+v, want %s", i, results[i], name)
			}
		}
	})
}

func TestRunSequentialToolsPreserveRequestOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan string, 4)
		release := make(chan struct{})
		runTool := func(_ context.Context, input json.RawMessage) llm.ToolOut {
			var request struct{ Name string }
			if err := json.Unmarshal(input, &request); err != nil {
				return llm.ErrorToolOut(err)
			}
			started <- request.Name
			<-release
			return llm.ToolOut{LLMContent: llm.TextContent(request.Name)}
		}
		tools := []*llm.Tool{
			{Name: "browser", Sequential: true, Run: runTool},
			{Name: "bash", Run: runTool},
		}
		service := &runTestService{first: []llm.Content{
			{Type: llm.ContentTypeToolUse, ID: "navigate", ToolName: "browser", ToolInput: json.RawMessage(`{"name":"navigate"}`)},
			{Type: llm.ContentTypeToolUse, ID: "first", ToolName: "bash", ToolInput: json.RawMessage(`{"name":"first"}`)},
			{Type: llm.ContentTypeToolUse, ID: "screenshot", ToolName: "browser", ToolInput: json.RawMessage(`{"name":"screenshot"}`)},
			{Type: llm.ContentTypeToolUse, ID: "second", ToolName: "bash", ToolInput: json.RawMessage(`{"name":"second"}`)},
		}}
		done := make(chan error, 1)
		go func() {
			done <- Run(t.Context(), RunConfig{LLM: service, Messages: []llm.Message{llm.UserStringMessage("work")}, Tools: tools})
		}()
		synctest.Wait()
		var concurrent []string
		for len(started) > 0 {
			concurrent = append(concurrent, <-started)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		slices.Sort(concurrent)
		if want := []string{"first", "navigate", "second"}; !slices.Equal(concurrent, want) {
			t.Errorf("concurrent calls = %v, want %v before navigation completes", concurrent, want)
		}
		results := service.requests[1].Messages[2].Content
		for i, name := range []string{"navigate", "first", "screenshot", "second"} {
			if results[i].ToolUseID != name || results[i].ToolResult[0].Text != name {
				t.Errorf("result %d = %+v, want %s", i, results[i], name)
			}
		}
	})
}

func TestRunSkipsQueuedSequentialCallAfterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started := make(chan string, 2)
		tool := &llm.Tool{
			Name: "browser", Sequential: true,
			Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
				var request struct{ Name string }
				if err := json.Unmarshal(input, &request); err != nil {
					return llm.ErrorToolOut(err)
				}
				started <- request.Name
				if request.Name == "navigate" {
					<-ctx.Done()
					return llm.ErrorToolOut(ctx.Err())
				}
				return llm.ErrorfToolOut("queued sequential call ran after cancellation")
			},
		}
		service := &runTestService{first: []llm.Content{
			{Type: llm.ContentTypeToolUse, ID: "navigate", ToolName: tool.Name, ToolInput: json.RawMessage(`{"name":"navigate"}`)},
			{Type: llm.ContentTypeToolUse, ID: "screenshot", ToolName: tool.Name, ToolInput: json.RawMessage(`{"name":"screenshot"}`)},
		}}
		var recorded llm.Message
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, RunConfig{
				LLM: service, Messages: []llm.Message{llm.UserStringMessage("work")}, Tools: []*llm.Tool{tool},
				Hooks: Hooks{OnToolResponse: func(_ context.Context, response ToolResponse) error {
					recorded = response.Message
					return nil
				}},
			})
		}()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want cancellation", err)
		}
		if len(started) != 1 || <-started != "navigate" {
			t.Error("queued sequential call ran after cancellation")
		}
		if len(service.requests) != 1 {
			t.Errorf("model requests = %d, want 1", len(service.requests))
		}
		if len(recorded.Content) != 2 || recorded.Content[0].ToolUseID != "navigate" || recorded.Content[1].ToolUseID != "screenshot" || recorded.Content[1].ToolResult[0].Text != notExecutedToolResultText {
			t.Fatalf("tool results = %+v, want ordered results with screenshot not executed", recorded.Content)
		}
	})
}

type cancellingRunService struct{ entered chan struct{} }

func (s *cancellingRunService) Do(ctx context.Context, _ *llm.Request) (*llm.Response, error) {
	close(s.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*cancellingRunService) Provider() string       { return "test" }
func (*cancellingRunService) MaxImageDimension() int { return 0 }
func (*cancellingRunService) MaxImageBytes() int     { return 0 }
func (*cancellingRunService) SupportsImages() bool   { return false }

func TestRunCancellationDoesNotPublishModelFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := &cancellingRunService{entered: make(chan struct{})}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var recorded []llm.Message
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, RunConfig{
				LLM: service, Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("hello")}},
				Hooks: Hooks{OnResponse: func(_ context.Context, response Response) error {
					recorded = append(recorded, response.Message)
					return nil
				}},
			})
		}()
		<-service.entered
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want cancellation", err)
		}
		if len(recorded) != 0 {
			t.Fatalf("canceled request recorded a model failure: %+v", recorded)
		}
	})
}

func TestRunDoesNotExecuteUnpersistedToolUse(t *testing.T) {
	service := &runTestService{}
	called := false
	err := Run(t.Context(), RunConfig{
		LLM: service,
		Tools: []*llm.Tool{{Name: "work", Run: func(context.Context, json.RawMessage) llm.ToolOut {
			called = true
			return llm.ToolOut{}
		}}},
		Hooks: Hooks{OnResponse: func(context.Context, Response) error { return errors.New("storage unavailable") }},
	})
	if !errors.Is(err, errMessagePersistence) || called || len(service.requests) != 1 {
		t.Fatalf("error = %v, tool ran = %v, requests = %d; tool-use must be stored first", err, called, len(service.requests))
	}
}

func TestRunDoesNotRequestModelWithUnpersistedToolResult(t *testing.T) {
	service := &runTestService{}
	err := Run(t.Context(), RunConfig{
		LLM: service,
		Tools: []*llm.Tool{{Name: "work", Run: func(context.Context, json.RawMessage) llm.ToolOut {
			return llm.ToolOut{LLMContent: llm.TextContent("result")}
		}}},
		Hooks: Hooks{OnToolResponse: func(context.Context, ToolResponse) error { return errors.New("storage unavailable") }},
	})
	if !errors.Is(err, errMessagePersistence) || len(service.requests) != 1 {
		t.Fatalf("error = %v, requests = %d; failed tool result must not reach model", err, len(service.requests))
	}
}

func TestRunPassesWorkingDirToTools(t *testing.T) {
	service := &runTestService{}
	var got string
	err := Run(t.Context(), RunConfig{
		LLM: service, WorkingDir: "/workspace/project",
		Tools: []*llm.Tool{{Name: "work", Run: func(ctx context.Context, _ json.RawMessage) llm.ToolOut {
			got = llm.WorkingDir(ctx)
			return llm.ToolOut{LLMContent: llm.TextContent("done")}
		}}},
	})
	if err != nil || got != "/workspace/project" {
		t.Fatalf("error = %v, tool working dir = %q", err, got)
	}
}

func TestRunRefusalCarriesModelChoice(t *testing.T) {
	for _, tc := range []struct{ configured, want string }{
		{configured: "chosen-model", want: "chosen-model"},
		{want: "provider-model"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			service := &runTestService{response: &llm.Response{
				Role: llm.MessageRoleAssistant, Model: "provider-model", StopReason: llm.StopReasonRefusal,
			}}
			var notice llm.Message
			err := Run(t.Context(), RunConfig{
				LLM: service, ModelID: tc.configured,
				Hooks: Hooks{OnResponse: func(_ context.Context, response Response) error {
					if response.Message.ErrorType == llm.ErrorTypeRefusal {
						notice = response.Message
					}
					return nil
				}},
			})
			if err != nil || notice.RefusalModel != tc.want || !strings.Contains(notice.Content[0].Text, "Choose another model") || strings.Contains(notice.Content[0].Text, "Opus") {
				t.Fatalf("error = %v, refusal notice = %+v", err, notice)
			}
		})
	}
}

func TestRunFlushesStreamBeforeRecording(t *testing.T) {
	for _, failed := range []bool{false, true} {
		service := &runTestService{response: &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn}}
		if failed {
			service.failure = errors.New("not retryable")
			service.response = nil
		}
		var events []string
		err := Run(t.Context(), RunConfig{LLM: service, Hooks: Hooks{
			OnStreamDone: func() { events = append(events, "flush") },
			OnResponse: func(context.Context, Response) error {
				events = append(events, "record")
				return nil
			},
		}})
		if (err != nil) != failed || !slices.Equal(events, []string{"flush", "record"}) {
			t.Fatalf("failed = %v, error = %v, events = %v", failed, err, events)
		}
	}
}

// RewriteFinalReply turns a turn-ending reply into tool calls that run like
// the model's own, at most once per turn.
func TestRunRewriteFinalReply(t *testing.T) {
	for _, tc := range []struct {
		name       string
		endTurn    bool
		wantRounds int
	}{
		{name: "tool ends the turn", endTurn: true, wantRounds: 1},
		{name: "tool fails", endTurn: false, wantRounds: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &runTestService{response: &llm.Response{
				Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn, Content: llm.TextContent("my report"),
			}}
			var rewrites, runs int
			var recorded []llm.Message
			err := Run(t.Context(), RunConfig{
				LLM:      service,
				Messages: []llm.Message{llm.UserStringMessage("start")},
				Tools: []*llm.Tool{{Name: "report", Run: func(_ context.Context, input json.RawMessage) llm.ToolOut {
					runs++
					if string(input) != `"my report"` {
						t.Errorf("tool input = %s", input)
					}
					if !tc.endTurn {
						return llm.ToolOut{Error: errors.New("parent stopped")}
					}
					return llm.ToolOut{LLMContent: llm.TextContent("sent"), EndTurn: true}
				}}},
				Hooks: Hooks{
					RewriteFinalReply: func(_ context.Context, reply llm.Message) (llm.Message, bool) {
						rewrites++
						input, _ := json.Marshal(reply.Content[0].Text)
						return llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{
							{Type: llm.ContentTypeToolUse, ID: "r", ToolName: "report", ToolInput: input},
						}}, true
					},
					OnResponse: func(_ context.Context, response Response) error {
						recorded = append(recorded, response.Message)
						return nil
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if rewrites != 1 || runs != 1 || len(service.requests) != tc.wantRounds {
				t.Fatalf("rewrites=%d runs=%d rounds=%d, want 1, 1, %d", rewrites, runs, len(service.requests), tc.wantRounds)
			}
			if first := recorded[0]; first.EndOfTurn || first.Content[0].Type != llm.ContentTypeToolUse {
				t.Fatalf("recorded reply = %+v, want the rewritten tool call", first)
			}
		})
	}
}

// An error that ends the turn — a failed request, a truncated response, a
// refusal — is offered to RewriteFinalReply too, once its row is recorded.
func TestRunRewriteFinalError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		service   *runTestService
		errorType llm.ErrorType
		wantErr   bool
	}{
		{"request failed", &runTestService{failure: errors.New("boom")}, llm.ErrorTypeLLMRequest, true},
		{"truncated", &runTestService{response: &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonMaxTokens, Content: llm.TextContent("partial")}}, llm.ErrorTypeTruncation, false},
		{"refused", &runTestService{response: &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonRefusal}}, llm.ErrorTypeRefusal, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var offered, recorded []llm.Message
			runs := 0
			err := Run(t.Context(), RunConfig{
				LLM:      tc.service,
				Messages: []llm.Message{llm.UserStringMessage("start")},
				Tools: []*llm.Tool{{Name: "report", Run: func(context.Context, json.RawMessage) llm.ToolOut {
					runs++
					return llm.ToolOut{LLMContent: llm.TextContent("sent"), EndTurn: true}
				}}},
				Hooks: Hooks{
					RewriteFinalReply: func(_ context.Context, reply llm.Message) (llm.Message, bool) {
						offered = append(offered, reply)
						return llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{
							{Type: llm.ContentTypeToolUse, ID: "r", ToolName: "report", ToolInput: json.RawMessage(`{}`)},
						}}, true
					},
					OnResponse: func(_ context.Context, response Response) error {
						recorded = append(recorded, response.Message)
						return nil
					},
				},
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error %v", err, tc.wantErr)
			}
			if len(offered) != 1 || offered[0].ErrorType != tc.errorType || runs != 1 {
				t.Fatalf("offered %+v and ran the tool %d times, want the %s error offered once and reported", offered, runs, tc.errorType)
			}
			// The error row stays, followed by the report and the tool's end of turn.
			if n := len(recorded); n < 3 {
				t.Fatalf("recorded %d messages, want at least 3", n)
			}
			errorRow, report, marker := recorded[len(recorded)-3], recorded[len(recorded)-2], recorded[len(recorded)-1]
			if errorRow.ErrorType != tc.errorType || !errorRow.EndOfTurn {
				t.Fatalf("error row = %+v, want %s ending the turn", errorRow, tc.errorType)
			}
			if report.ErrorType != llm.ErrorTypeNone || report.EndOfTurn || report.Content[0].Type != llm.ContentTypeToolUse {
				t.Fatalf("report = %+v, want the rewritten tool call", report)
			}
			if !marker.EndOfTurn || !marker.ExcludedFromContext {
				t.Fatalf("last row = %+v, want the tool's end-of-turn marker", marker)
			}
		})
	}
}
