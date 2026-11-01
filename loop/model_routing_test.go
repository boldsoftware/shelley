package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

func TestModelRoutingTransitions(t *testing.T) {
	var routing modelRouting
	var notices []RoutingNotice
	for _, tc := range []struct {
		served string
		want   int
	}{
		{"claude-fable-5-1", 0},
		{"claude-fable-5-1-20261001", 0},
		{"claude-opus-4-8", 1},
		{"us.anthropic.claude-opus-4-8-v1:0", 1},
		{"", 1},
		{"unrecognized-model", 1},
		{"claude-fable-5.1", 2},
	} {
		err := routing.observe(t.Context(), &llm.Response{
			Origin: &llm.MessageOrigin{Model: "claude-fable-5-1"},
			Model:  tc.served,
		}, func(_ context.Context, notice RoutingNotice) error {
			notices = append(notices, notice)
			return nil
		})
		if err != nil || len(notices) != tc.want {
			t.Fatalf("served %q: notices = %v, error = %v", tc.served, notices, err)
		}
	}
	if !strings.HasPrefix(notices[0].Text(), "Provider routed") || notices[0].Recovered ||
		!strings.HasPrefix(notices[1].Text(), "Back on your requested model") || !notices[1].Recovered ||
		notices[0].From != "Claude Fable 5.1" || notices[0].To != "Claude Opus 4.8" {
		t.Fatalf("unexpected notices: %v", notices)
	}
}

func TestModelRoutingRetriesFailedNotice(t *testing.T) {
	var routing modelRouting
	response := &llm.Response{
		Origin: &llm.MessageOrigin{Model: "claude-fable-5-1"},
		Model:  "claude-opus-4-8",
	}
	failure := errors.New("notice unavailable")
	if err := routing.observe(t.Context(), response, func(context.Context, RoutingNotice) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want notice failure", err)
	}
	called := false
	if err := routing.observe(t.Context(), response, func(context.Context, RoutingNotice) error {
		called = true
		return nil
	}); err != nil || !called {
		t.Fatalf("failed notice was not retried: called = %v, error = %v", called, err)
	}
}

func TestRunRoutingNoticeFailurePreservesResponse(t *testing.T) {
	for _, reason := range []llm.StopReason{
		llm.StopReasonEndTurn, llm.StopReasonToolUse, llm.StopReasonRefusal, llm.StopReasonMaxTokens,
	} {
		t.Run(reason.String(), func(t *testing.T) {
			failure := errors.New("notice storage failed")
			var events []string
			var recorded []Response
			var logs bytes.Buffer
			toolRan := false
			content := llm.TextContent("completed reply")
			if reason == llm.StopReasonToolUse {
				content = append(content, llm.Content{
					Type: llm.ContentTypeToolUse, ID: "job", ToolName: "work", ToolInput: json.RawMessage(`{}`),
				})
			}
			err := Run(t.Context(), RunConfig{
				Logger: slog.New(slog.NewTextHandler(&logs, nil)),
				LLM: &runTestService{response: &llm.Response{
					Role: llm.MessageRoleAssistant, StopReason: reason,
					Origin: &llm.MessageOrigin{Model: "claude-fable-5-1"},
					Model:  "claude-opus-4-8", Content: content,
				}},
				Tools: []*llm.Tool{{Name: "work", Run: func(context.Context, json.RawMessage) llm.ToolOut {
					toolRan = true
					return llm.ToolOut{}
				}}},
				Hooks: Hooks{
					OnRoutingWarning: func(ctx context.Context, _ RoutingNotice) error {
						if ctx.Err() != nil {
							t.Fatal("notice persistence context was cancelled")
						}
						events = append(events, "notice")
						return failure
					},
					OnResponse: func(ctx context.Context, response Response) error {
						if ctx.Err() != nil {
							t.Fatal("reply persistence context was cancelled")
						}
						events = append(events, "reply")
						recorded = append(recorded, response)
						return nil
					},
					OnToolResponse: func(_ context.Context, response ToolResponse) error {
						events = append(events, "tool result")
						if response.Message.Content[0].ToolUseID != "job" {
							t.Fatalf("unexpected tool result: %+v", response.Message)
						}
						return nil
					},
				},
			})
			if err != nil || toolRan != (reason == llm.StopReasonToolUse) {
				t.Fatalf("error = %v, tool ran = %v; notice failures must not interrupt the turn", err, toolRan)
			}
			if !strings.Contains(logs.String(), failure.Error()) {
				t.Fatal("notice failure was not logged")
			}
			wantEvents := []string{"notice", "reply"}
			if reason == llm.StopReasonRefusal || reason == llm.StopReasonMaxTokens {
				wantEvents = append(wantEvents, "reply")
			} else if reason == llm.StopReasonToolUse {
				wantEvents = append(wantEvents, "tool result", "reply")
			}
			if !slices.Equal(events, wantEvents) || recorded[0].Message.Content[0].Text != "completed reply" || recorded[0].Usage.Model != "claude-opus-4-8" {
				t.Fatalf("events = %v, replies = %+v; completed response and usage must survive", events, recorded)
			}
			if !recorded[len(recorded)-1].Message.EndOfTurn {
				t.Fatal("notice failure prevented the turn from ending")
			}
		})
	}
}

func TestRunCancelledResponseSkipsRoutingNotice(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := Run(ctx, RunConfig{
		LLM: &runTestService{response: &llm.Response{
			Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonToolUse,
			Origin: &llm.MessageOrigin{Model: "claude-fable-5-1"},
			Model:  "claude-opus-4-8", Content: llm.TextContent("completed reply"),
		}},
		Hooks: Hooks{
			OnStreamDone: cancel,
			OnRoutingWarning: func(context.Context, RoutingNotice) error {
				t.Fatal("cancelled response recorded a routing notice")
				return nil
			},
			OnResponse: func(context.Context, Response) error {
				t.Fatal("cancelled response recorded a tool-use message")
				return nil
			},
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
}

func TestRunCancellationAfterToolUseRecordsResults(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	service := &runTestService{}
	toolRan := false
	var recorded []llm.Message
	err := Run(ctx, RunConfig{
		LLM: service,
		Tools: []*llm.Tool{{Name: "work", Run: func(context.Context, json.RawMessage) llm.ToolOut {
			toolRan = true
			return llm.ToolOut{}
		}}},
		Hooks: Hooks{
			OnResponse: func(_ context.Context, response Response) error {
				recorded = append(recorded, response.Message)
				cancel()
				return nil
			},
			OnToolResponse: func(ctx context.Context, response ToolResponse) error {
				if ctx.Err() != nil {
					t.Fatal("tool results inherited the cancelled context")
				}
				recorded = append(recorded, response.Message)
				return nil
			},
		},
	})
	if !errors.Is(err, context.Canceled) || toolRan || len(service.requests) != 1 || len(recorded) != 2 {
		t.Fatalf("error = %v, tool ran = %v, requests = %d, rows = %d", err, toolRan, len(service.requests), len(recorded))
	}
	call, result := recorded[0].Content[0], recorded[1].Content[0]
	if call.ID != result.ToolUseID || !result.ToolError || result.ToolResult[0].Text != notExecutedToolResultText {
		t.Fatalf("unmatched cancelled call: call = %+v, result = %+v", call, result)
	}
}

func TestModelRoutingPreservesRequestedModels(t *testing.T) {
	var routing modelRouting
	var notices []RoutingNotice
	for _, tc := range []struct {
		requested, served string
		want              int
	}{
		{"claude-fable-5-1", "claude-opus-4-8", 1},
		{"claude-sonnet-5", "claude-sonnet-5", 1},
		{"claude-fable-5-1", "claude-opus-4-8", 1},
		{"claude-sonnet-5", "claude-sonnet-5", 1},
		{"claude-fable-5-1", "claude-fable-5-1", 2},
	} {
		err := routing.observe(t.Context(), &llm.Response{
			Origin: &llm.MessageOrigin{Model: tc.requested}, Model: tc.served,
		}, func(_ context.Context, notice RoutingNotice) error {
			notices = append(notices, notice)
			return nil
		})
		if err != nil || len(notices) != tc.want {
			t.Fatalf("requested %s: notices = %+v, error = %v", tc.requested, notices, err)
		}
	}
	if !notices[1].Recovered || notices[1].From != "Claude Opus 4.8" || notices[1].To != "Claude Fable 5.1" {
		t.Fatalf("missing recovery after profile turn: %+v", notices)
	}
}
