package loop_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/loop"
)

type incompleteProvider struct{ name, provider, url, model string }

var incompleteProviders = []incompleteProvider{
	{"openai", "openai", "https://api.openai.com/v1", "gpt-5.4"},
	{"openrouter-gemini", "openrouter", "https://openrouter.ai/api/v1", "google/gemini-4-carbon"},
	{"xai", "xai", "https://api.x.ai/v1", "grok-4.5"},
}

type fakeResponses struct {
	t        *testing.T
	provider incompleteProvider
	sse      bool
	maxCalls int
	respond  func(call int) map[string]any
	requests []map[string]any
}

func (f *fakeResponses) service() *oai.ResponsesService {
	return &oai.ResponsesService{
		APIKey: "test-key", Model: oai.Model{ModelName: f.provider.model}, ModelURL: f.provider.url,
		ProviderName: f.provider.provider, MaxTokens: 100, Backoff: []time.Duration{0},
		HTTPC: &http.Client{Transport: f},
	}
}

func (f *fakeResponses) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodPost || r.URL.String() != f.provider.url+"/responses" {
		f.t.Errorf("request = %s %s", r.Method, r.URL)
	}
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("decoding request: %v", err)
	}
	f.requests = append(f.requests, req)
	call := len(f.requests)
	if call > f.maxCalls {
		f.t.Errorf("unexpected model request %d (limit %d)", call, f.maxCalls)
		return &http.Response{
			StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"error":{"message":"too many requests"}}`)), Request: r,
		}, nil
	}
	response := f.respond(call)
	if !f.sse {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(bytes.NewReader(mustJSON(f.t, response))), Request: r,
		}, nil
	}
	var stream strings.Builder
	for i, item := range response["output"].([]any) {
		stream.WriteString("event: response.output_item.done\ndata: " + string(mustJSON(f.t, map[string]any{
			"type": "response.output_item.done", "output_index": i, "item": item,
		})) + "\n\n")
	}
	event := "response." + response["status"].(string)
	stream.WriteString("event: " + event + "\ndata: " + string(mustJSON(f.t, map[string]any{"type": event, "response": response})) + "\n\n")
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(stream.String())), Request: r,
	}, nil
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func responsesText(text string) map[string]any {
	return map[string]any{
		"type": "message", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}
}

func responsesFunctionCall(callID, status string) map[string]any {
	return map[string]any{
		"type": "function_call", "id": "fc_" + callID, "call_id": callID, "name": "work",
		"arguments": `{"text":"x"}`, "status": status,
	}
}

func responsesReasoning(id string) map[string]any {
	return map[string]any{
		"type": "reasoning", "id": id, "format": "google-gemini-v1",
		"encrypted_content": "state", "summary": []any{},
	}
}

func responsesBody(status, reason string, output []any, outputTokens int) map[string]any {
	body := map[string]any{
		"id": "resp", "status": status, "model": "provider-model", "output": output,
		"usage": map[string]any{
			"input_tokens": 120, "output_tokens": outputTokens, "cost": .25,
			"input_tokens_details": map[string]any{"cached_tokens": 20},
		},
	}
	if reason != "" {
		body["incomplete_details"] = map[string]any{"reason": reason}
	}
	return body
}

type workTool struct {
	runs   atomic.Int32
	inputs chan json.RawMessage
}

func newWorkTool() *workTool { return &workTool{inputs: make(chan json.RawMessage, 8)} }

func (w *workTool) tool() *llm.Tool {
	return &llm.Tool{Name: "work", Description: "work", InputSchema: llm.EmptySchema(), Run: func(_ context.Context, input json.RawMessage) llm.ToolOut {
		w.runs.Add(1)
		w.inputs <- input
		return llm.ToolOut{LLMContent: llm.TextContent("ran")}
	}}
}

type runRecorder struct {
	responses     []loop.Response
	toolResponses int
	successful    int
}

func (r *runRecorder) hooks() loop.Hooks {
	return loop.Hooks{
		OnResponse: func(_ context.Context, response loop.Response) error {
			r.responses = append(r.responses, response)
			return nil
		},
		OnToolResponse:      func(context.Context, loop.ToolResponse) error { r.toolResponses++; return nil },
		OnSuccessfulRequest: func(*llm.Request) { r.successful++ },
	}
}

func responsesUsageWant(outputTokens uint64) llm.Usage {
	return llm.Usage{InputTokens: 100, CacheReadInputTokens: 20, OutputTokens: outputTokens, CostUSD: .25}
}

func checkUsage(t *testing.T, got llm.Usage, outputTokens uint64) {
	t.Helper()
	if want := responsesUsageWant(outputTokens); got.InputTokens != want.InputTokens || got.CacheReadInputTokens != want.CacheReadInputTokens ||
		got.OutputTokens != want.OutputTokens || got.CostUSD != want.CostUSD {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
}

func TestResponsesIncompleteStopsBeforeTools(t *testing.T) {
	outputs := []struct {
		name   string
		output func() []any
	}{
		{"empty", func() []any { return []any{} }},
		{"text", func() []any { return []any{responsesText("partial answer")} }},
		{"function_call", func() []any { return []any{responsesFunctionCall("call_1", "incomplete")} }},
		{"ordinary_reasoning_text", func() []any { return []any{responsesReasoning("rs_ordinary"), responsesText("partial answer")} }},
		{"checkpoint_text", func() []any { return []any{responsesReasoning("gemini-continuation"), responsesText("partial answer")} }},
	}
	reasons := []struct {
		reason    string
		errorType llm.ErrorType
	}{
		{"max_output_tokens", llm.ErrorTypeTruncation},
		{"content_filter", llm.ErrorTypeRefusal},
	}
	for _, provider := range incompleteProviders {
		for _, sse := range []bool{false, true} {
			for _, reason := range reasons {
				for _, output := range outputs {
					// Only Gemini's eligible length stops should resume.
					if provider.name == "openrouter-gemini" && output.name == "checkpoint_text" && reason.reason == "max_output_tokens" {
						continue
					}
					t.Run(fmt.Sprintf("%s/sse=%t/%s/%s", provider.name, sse, reason.reason, output.name), func(t *testing.T) {
						fake := &fakeResponses{t: t, provider: provider, sse: sse, maxCalls: 1, respond: func(int) map[string]any {
							return responsesBody("incomplete", reason.reason, output.output(), 17)
						}}
						work, rec := newWorkTool(), &runRecorder{}
						err := loop.Run(t.Context(), loop.RunConfig{
							LLM: fake.service(), ModelID: "chosen-model", Messages: []llm.Message{llm.UserStringMessage("start")},
							Tools: []*llm.Tool{work.tool()}, Hooks: rec.hooks(),
						})
						if err != nil {
							t.Fatal(err)
						}
						if len(fake.requests) != 1 {
							t.Fatalf("model requests = %d, want 1 (no retry or continuation)", len(fake.requests))
						}
						if work.runs.Load() != 0 || rec.toolResponses != 0 || rec.successful != 0 {
							t.Fatalf("tool runs=%d tool responses=%d successful requests=%d, want none", work.runs.Load(), rec.toolResponses, rec.successful)
						}
						if len(rec.responses) != 2 {
							t.Fatalf("recorded %d messages, want raw response and error: %+v", len(rec.responses), rec.responses)
						}

						raw, notice := rec.responses[0], rec.responses[1]
						if !raw.Message.ExcludedFromContext || raw.Message.EndOfTurn || raw.Message.ErrorType != llm.ErrorTypeNone {
							t.Fatalf("raw response must be excluded, not end the turn, and not be an error: %+v", raw.Message)
						}
						checkUsage(t, raw.Usage, 17)
						switch output.name {
						case "text", "ordinary_reasoning_text", "checkpoint_text":
							if !strings.Contains(fmt.Sprint(raw.Message.Content), "partial answer") {
								t.Fatalf("raw response lost its text: %+v", raw.Message.Content)
							}
						case "function_call":
							var calls []llm.Content
							for _, c := range raw.Message.Content {
								if c.Type == llm.ContentTypeToolUse {
									calls = append(calls, c)
								}
							}
							if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].ToolName != "work" || !json.Valid(calls[0].ToolInput) {
								t.Fatalf("raw response must keep the well-formed call it did not run: %+v", raw.Message.Content)
							}
						}

						if notice.Message.ErrorType != reason.errorType || !notice.Message.EndOfTurn || notice.Message.ExcludedFromContext ||
							len(notice.Message.Content) != 1 || notice.Message.Content[0].Text == "" {
							t.Fatalf("notice = %+v, want visible end-of-turn %q error", notice.Message, reason.errorType)
						}
						if (notice.Message.RefusalModel == "chosen-model") != (reason.errorType == llm.ErrorTypeRefusal) || notice.Message.ErrorRetryable && reason.errorType == llm.ErrorTypeRefusal {
							t.Fatalf("notice refusal fields = %+v", notice.Message)
						}
						if notice.Usage != (llm.Usage{}) {
							t.Fatalf("notice repeats usage: %+v", notice.Usage)
						}
					})
				}
			}
		}
	}
}

func TestResponsesCompletedToolCallRunsThenFinishes(t *testing.T) {
	for _, provider := range incompleteProviders {
		for _, sse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/sse=%t", provider.name, sse), func(t *testing.T) {
				fake := &fakeResponses{t: t, provider: provider, sse: sse, maxCalls: 2, respond: func(call int) map[string]any {
					if call == 1 {
						return responsesBody("completed", "", []any{responsesFunctionCall("call_1", "completed")}, 17)
					}
					return responsesBody("completed", "", []any{responsesText("done")}, 5)
				}}
				work, rec := newWorkTool(), &runRecorder{}
				err := loop.Run(t.Context(), loop.RunConfig{
					LLM: fake.service(), Messages: []llm.Message{llm.UserStringMessage("start")},
					Tools: []*llm.Tool{work.tool()}, Hooks: rec.hooks(),
				})
				if err != nil {
					t.Fatal(err)
				}
				if len(fake.requests) != 2 || work.runs.Load() != 1 || rec.toolResponses != 1 || rec.successful != 2 {
					t.Fatalf("requests=%d tool runs=%d tool responses=%d successful=%d, want 2/1/1/2", len(fake.requests), work.runs.Load(), rec.toolResponses, rec.successful)
				}
				if got := string(<-work.inputs); got != `{"text":"x"}` {
					t.Fatalf("tool input = %s", got)
				}
				var sentResult bool
				for _, item := range fake.requests[1]["input"].([]any) {
					item := item.(map[string]any)
					if item["type"] == "function_call_output" && item["call_id"] == "call_1" && strings.Contains(fmt.Sprint(item["output"]), "ran") {
						sentResult = true
					}
				}
				if !sentResult {
					t.Fatalf("second request did not carry the tool result: %v", fake.requests[1]["input"])
				}
				if len(rec.responses) != 2 {
					t.Fatalf("recorded %d messages, want tool-use and final reply: %+v", len(rec.responses), rec.responses)
				}
				toolUse, final := rec.responses[0], rec.responses[1]
				if toolUse.Message.ExcludedFromContext || toolUse.Message.EndOfTurn || toolUse.Message.ErrorType != llm.ErrorTypeNone {
					t.Fatalf("tool-use message = %+v", toolUse.Message)
				}
				if final.Message.ExcludedFromContext || !final.Message.EndOfTurn || final.Message.ErrorType != llm.ErrorTypeNone ||
					len(final.Message.Content) != 1 || final.Message.Content[0].Text != "done" {
					t.Fatalf("final message = %+v", final.Message)
				}
				checkUsage(t, toolUse.Usage, 17)
				if final.Usage.OutputTokens != 5 {
					t.Fatalf("final usage = %+v", final.Usage)
				}
			})
		}
	}
}
