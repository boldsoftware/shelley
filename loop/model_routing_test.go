package loop

import (
	"context"
	"encoding/json"
	"errors"
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
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("notice storage failed")
			var events []string
			var recorded []Response
			toolRan := false
			err := Run(ctx, RunConfig{
				LLM: &runTestService{response: &llm.Response{
					Role: llm.MessageRoleAssistant, StopReason: reason,
					Origin: &llm.MessageOrigin{Model: "claude-fable-5-1"},
					Model:  "claude-opus-4-8",
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "completed reply"},
						{Type: llm.ContentTypeToolUse, ID: "job", ToolName: "work", ToolInput: json.RawMessage(`{}`)},
					},
				}},
				Tools: []*llm.Tool{{Name: "work", Run: func(context.Context, json.RawMessage) llm.ToolOut {
					toolRan = true
					return llm.ToolOut{}
				}}},
				Hooks: Hooks{
					OnRoutingWarning: func(ctx context.Context, _ RoutingNotice) error {
						cancel()
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
				},
			})
			if !errors.Is(err, failure) || toolRan {
				t.Fatalf("error = %v, tool ran = %v; preserve reply before reporting notice failure", err, toolRan)
			}
			wantEvents := []string{"notice", "reply"}
			if reason == llm.StopReasonRefusal || reason == llm.StopReasonMaxTokens {
				wantEvents = append(wantEvents, "reply")
			}
			if !slices.Equal(events, wantEvents) || recorded[0].Message.Content[0].Text != "completed reply" || recorded[0].Usage.Model != "claude-opus-4-8" {
				t.Fatalf("events = %v, replies = %+v; completed response and usage must survive", events, recorded)
			}
		})
	}
}

func TestRunRoutingNoticeSurvivesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var events []string
	err := Run(ctx, RunConfig{
		LLM: &runTestService{response: &llm.Response{
			Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn,
			Origin: &llm.MessageOrigin{Model: "claude-fable-5-1"},
			Model:  "claude-opus-4-8", Content: llm.TextContent("completed reply"),
		}},
		Hooks: Hooks{
			OnRoutingWarning: func(ctx context.Context, _ RoutingNotice) error {
				cancel()
				events = append(events, "notice")
				return ctx.Err()
			},
			OnResponse: func(ctx context.Context, _ Response) error {
				events = append(events, "reply")
				return ctx.Err()
			},
		},
	})
	if err != nil || !slices.Equal(events, []string{"notice", "reply"}) {
		t.Fatalf("error = %v, events = %v; preserve notice-before-reply order after cancellation", err, events)
	}
}
