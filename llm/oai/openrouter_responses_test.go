package oai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
	"shelley.exe.dev/models/modelsdev"
)

const (
	openRouterResponsesURL    = "https://openrouter.ai/api/v1"
	openRouterResponsesModel  = "google/gemini-4-carbon"
	openRouterReasoningFormat = "google-gemini-v1"
)

type openRouterResponsesTransport func(*http.Request) (*http.Response, error)

func (f openRouterResponsesTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type openRouterResponsesRequest struct {
	Model           string           `json:"model"`
	Input           []map[string]any `json:"input"`
	MaxOutputTokens int              `json:"max_output_tokens"`
	Instructions    string           `json:"instructions"`
	Tools           []map[string]any `json:"tools"`
}

func openRouterResponsesService(t *testing.T, provider, model string, handle func(int, openRouterResponsesRequest) *http.Response) *ResponsesService {
	t.Helper()
	calls := 0
	return &ResponsesService{
		APIKey: "test-key", Model: modelForTest(model), ModelURL: openRouterResponsesURL,
		ProviderName: provider, MaxTokens: 100, Backoff: []time.Duration{0},
		HTTPC: &http.Client{Transport: openRouterResponsesTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != openRouterResponsesURL+"/responses" || r.Method != http.MethodPost {
				t.Fatalf("request = %s %s", r.Method, r.URL)
			}
			var req openRouterResponsesRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			calls++
			return handle(calls, req), nil
		})},
	}
}

func openRouterJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func openRouterReasoning(id, token string) map[string]any {
	return map[string]any{
		"type": "reasoning", "id": id, "format": openRouterReasoningFormat,
		"encrypted_content": token, "summary": []any{},
	}
}

func openRouterText(text string) map[string]any {
	return map[string]any{
		"type": "message", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}
}

func openRouterResponse(status, reason string, output []any, input, generated int, cost float64) map[string]any {
	resp := map[string]any{
		"id": "response", "status": status, "model": openRouterResponsesModel, "output": output,
		"usage": map[string]any{"input_tokens": input, "output_tokens": generated, "cost": cost},
	}
	if reason != "" {
		resp["incomplete_details"] = map[string]any{"reason": reason}
	}
	return resp
}

// Encode real provider JSON rather than the implementation's response structs:
// otherwise a missing wire field could silently disappear from the fixture.
func openRouterHTTPResponse(t *testing.T, response map[string]any, sse bool, streamedReasoning map[string]any) *http.Response {
	t.Helper()
	body := string(openRouterJSON(t, response))
	contentType := "application/json"
	if sse {
		contentType = "text/event-stream"
		var events []string
		if streamedReasoning != nil {
			events = append(events, "event: response.output_item.done\ndata: "+string(openRouterJSON(t, map[string]any{
				"type": "response.output_item.done", "output_index": 0, "item": streamedReasoning,
			}))+"\n\n")
		}
		for index, item := range response["output"].([]any) {
			output := item.(map[string]any)
			if output["type"] == "message" {
				text := output["content"].([]any)[0].(map[string]any)["text"]
				events = append(events, "event: response.output_text.delta\ndata: "+string(openRouterJSON(t, map[string]any{
					"type": "response.output_text.delta", "output_index": index, "content_index": 0, "delta": text,
				}))+"\n\n")
			}
		}
		event := "response." + response["status"].(string)
		events = append(events, "event: "+event+"\ndata: "+string(openRouterJSON(t, map[string]any{
			"type": event, "response": response,
		}))+"\n\n")
		body = strings.Join(events, "")
	}
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

func openRouterUserRequest() *llm.Request {
	return &llm.Request{
		Messages: []llm.Message{{
			Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Continue the answer."}},
		}},
		OnRetry: func(event llm.RetryEvent) { panic(fmt.Sprintf("unexpected retry: %+v", event)) },
	}
}

func openRouterStoredMessage(t *testing.T, msg llm.Message) llm.Message {
	t.Helper()
	var stored llm.Message
	if err := json.Unmarshal(openRouterJSON(t, msg), &stored); err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestOpenRouterResponsesCacheKeyAndVerbosity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		provider  string
		endpoint  string
		model     string
		verbosity string
	}{
		{"provider", "openrouter", "https://router.example/v1", "openai/gpt-5.4", "low"},
		{"endpoint", "openai", openRouterResponsesURL, "openai/gpt-5.4", "low"},
		{"gemini", "google", openRouterResponsesURL, openRouterResponsesModel, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := modelForTest(tc.model)
			model.TextVerbosity = tc.verbosity
			svc := &ResponsesService{
				Model: model, ModelURL: tc.endpoint, ProviderName: tc.provider,
			}
			ctx := llmhttp.WithPromptCacheKey(t.Context(), "shared-prefix")
			req, _, _, err := svc.buildRequest(ctx, openRouterUserRequest())
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(openRouterJSON(t, req), &wire); err != nil {
				t.Fatal(err)
			}
			if wire["prompt_cache_key"] != "shared-prefix" {
				t.Errorf("prompt_cache_key = %#v, want shared-prefix", wire["prompt_cache_key"])
			}
			text, present := wire["text"].(map[string]any)
			if tc.verbosity == "" {
				if present {
					t.Errorf("text = %#v, want omitted", wire["text"])
				}
			} else if !present || text["verbosity"] != tc.verbosity {
				t.Errorf("text = %#v, want verbosity %q", wire["text"], tc.verbosity)
			}
			for _, field := range []string{"parallel_tool_calls", "tool_choice"} {
				if _, present := wire[field]; present {
					t.Errorf("OpenRouter request contains native OpenAI default %q", field)
				}
			}
		})
	}
}

func TestOpenRouterResponsesReasoningStorageAndReplay(t *testing.T) {
	for _, provider := range []string{"openrouter", "google"} {
		for _, sse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/sse=%t", provider, sse), func(t *testing.T) {
				// This model's Chat Completions metadata names reasoning_details,
				// not reasoning_content. Responses still replays encrypted items.
				const model = "google/gemini-3.1-pro-preview"
				if field, found := modelsdev.LookupInterleavedReasoningField(openRouterResponsesURL, model); !found || field != "reasoning_details" {
					t.Fatalf("catalog fixture field = %q, found=%t", field, found)
				}
				var requests []openRouterResponsesRequest
				svc := openRouterResponsesService(t, provider, model, func(call int, req openRouterResponsesRequest) *http.Response {
					requests = append(requests, req)
					if call == 1 {
						reasoning := openRouterReasoning("rs_opaque", "opaque-provider-state")
						var streamed map[string]any
						if sse {
							streamed = openRouterReasoning("rs_opaque", "stale-streamed-state")
							delete(reasoning, "format") // output_item.done is the only format source.
						}
						return openRouterHTTPResponse(t, openRouterResponse("completed", "", []any{
							reasoning, openRouterText("visible answer"),
						}, 12, 3, .25), sse, streamed)
					}
					return openRouterHTTPResponse(t, openRouterResponse("completed", "", []any{openRouterText("ok")}, 1, 1, 0), false, nil)
				})
				resp, err := svc.Do(t.Context(), openRouterUserRequest())
				if err != nil {
					t.Fatal(err)
				}
				stored := openRouterStoredMessage(t, resp.ToMessage())
				if stored.Origin == nil || stored.Origin.Provider != provider || stored.Origin.Model != model ||
					stored.Origin.Transport != "openai-responses:"+openRouterResponsesURL {
					t.Fatalf("stored origin = %+v", stored.Origin)
				}
				if len(stored.Content) != 2 || stored.Content[0].OpenAIResponsesReasoning == nil {
					t.Fatalf("stored content = %+v", stored.Content)
				}
				metadata := stored.Content[0].OpenAIResponsesReasoning
				if metadata.ID != "rs_opaque" || metadata.Format != openRouterReasoningFormat || metadata.EncryptedContent != "opaque-provider-state" {
					t.Fatalf("stored metadata = %+v", metadata)
				}

				for _, guard := range []string{"same origin", "provider", "model", "transport", "explicit none"} {
					t.Run(guard, func(t *testing.T) {
						msg := openRouterStoredMessage(t, stored)
						svc.ReasoningReplay = ReasoningReplayAuto
						switch guard {
						case "provider":
							msg.Origin.Provider = "other-provider"
						case "model":
							msg.Origin.Model = "other-model"
						case "transport":
							msg.Origin.Transport = "openai-responses:https://other.example/v1"
						case "explicit none":
							svc.ReasoningReplay = ReasoningReplayNone
						}
						before := string(openRouterJSON(t, msg))
						req := openRouterUserRequest()
						req.Messages = []llm.Message{msg}
						if _, err := svc.Do(t.Context(), req); err != nil {
							t.Fatal(err)
						}
						if after := string(openRouterJSON(t, req.Messages[0])); after != before {
							t.Fatal("replay mutated stored history")
						}
						input := requests[len(requests)-1].Input
						var reasoning []map[string]any
						var visible bool
						for _, item := range input {
							if item["type"] == "reasoning" {
								reasoning = append(reasoning, item)
							}
							visible = visible || item["type"] == "message"
						}
						if !visible {
							t.Fatalf("visible assistant text was stripped: %+v", input)
						}
						if guard != "same origin" {
							if len(reasoning) != 0 {
								t.Fatalf("guard %q leaked reasoning: %+v", guard, reasoning)
							}
							return
						}
						if len(reasoning) != 1 {
							t.Fatalf("reasoning replay = %+v", input)
						}
						if item := reasoning[0]; item["id"] != metadata.ID || item["format"] != metadata.Format || item["encrypted_content"] != metadata.EncryptedContent {
							t.Fatalf("replayed metadata = %+v", item)
						}
					})
				}
			})
		}
	}
}

func TestOpenRouterResponsesContinuationBudgetAndUsage(t *testing.T) {
	for _, provider := range []string{"openrouter", "google"} {
		for _, sse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/sse=%t", provider, sse), func(t *testing.T) {
				var requests []openRouterResponsesRequest
				svc := openRouterResponsesService(t, provider, openRouterResponsesModel, func(call int, req openRouterResponsesRequest) *http.Response {
					if call > 3 {
						t.Fatal("completed checkpoint triggered an extra continuation")
					}
					requests = append(requests, req)
					status, reason := "incomplete", "max_output_tokens"
					if call == 3 {
						status, reason = "completed", ""
					}
					token := fmt.Sprintf("token-%d", call)
					checkpoint := openRouterReasoning("gemini-continuation", token)
					var streamed map[string]any
					if sse {
						streamed = openRouterReasoning("gemini-continuation", token)
						delete(checkpoint, "format")
					}
					response := openRouterResponse(status, reason, []any{
						checkpoint, openRouterText([]string{"one ", "two ", "three"}[call-1]),
					}, []int{100, 102, 104}[call-1], []int{3, 2, 4}[call-1], []float64{.125, .25, .5}[call-1])
					response["usage"].(map[string]any)["input_tokens_details"] = map[string]any{"cached_tokens": 20 + call - 1}
					return openRouterHTTPResponse(t, response, sse, streamed)
				})
				svc.MaxTokens = 10
				req := openRouterUserRequest()
				req.System = []llm.SystemContent{{Text: "Keep the original instructions."}}
				req.Tools = []*llm.Tool{{Name: "inspect", InputSchema: llm.EmptySchema()}}
				var streamedText strings.Builder
				if sse {
					req.OnStream = func(delta llm.StreamDelta) {
						if delta.Type == "text" {
							streamedText.WriteString(delta.Text)
						}
					}
				}
				before := string(openRouterJSON(t, req.Messages))
				resp, err := svc.Do(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				if len(requests) != 3 {
					t.Fatalf("requests = %d, want 3", len(requests))
				}
				for i, request := range requests {
					// Gemini tracks the cumulative budget inside its checkpoint:
					// do not subtract previous usage or raise the original cap.
					if request.MaxOutputTokens != 10 {
						t.Errorf("hop %d max_output_tokens = %d, want original cumulative budget 10", i, request.MaxOutputTokens)
					}
					if request.Model != requests[0].Model || request.Instructions != requests[0].Instructions || !reflect.DeepEqual(request.Tools, requests[0].Tools) {
						t.Errorf("hop %d changed the original request: %+v", i, request)
					}
					if i == 0 {
						continue
					}
					if len(request.Input) != len(requests[0].Input)+1 {
						t.Fatalf("hop %d input = %+v; want original input plus only latest checkpoint", i, request.Input)
					}
					if !reflect.DeepEqual(request.Input[:len(requests[0].Input)], requests[0].Input) {
						t.Errorf("hop %d changed original input: %+v", i, request.Input)
					}
					last := request.Input[len(request.Input)-1]
					if last["type"] != "reasoning" || last["id"] != "gemini-continuation" ||
						last["format"] != openRouterReasoningFormat || last["encrypted_content"] != fmt.Sprintf("token-%d", i) {
						t.Errorf("hop %d continuation = %+v", i, last)
					}
				}
				if string(openRouterJSON(t, req.Messages)) != before {
					t.Fatal("continuation mutated original messages")
				}
				var text strings.Builder
				for _, content := range resp.Content {
					if content.Type == llm.ContentTypeText {
						text.WriteString(content.Text)
					}
				}
				if text.String() != "one two three" {
					t.Errorf("combined text = %q", text.String())
				}
				if sse && streamedText.String() != text.String() {
					t.Errorf("streamed text = %q, want %q", streamedText.String(), text.String())
				}
				if resp.Usage.InputTokens != 243 || resp.Usage.CacheReadInputTokens != 63 || resp.Usage.TotalInputTokens() != 306 ||
					resp.Usage.OutputTokens != 9 || math.Abs(resp.Usage.CostUSD-.875) > 1e-9 {
					t.Errorf("summed usage = %+v", resp.Usage)
				}
				if resp.Usage.ContextWindowTokens != 109 || resp.Usage.ContextWindowUsed() != 109 {
					t.Errorf("context = %d (override %d), want first input + all output = 109", resp.Usage.ContextWindowUsed(), resp.Usage.ContextWindowTokens)
				}
				var usage llm.Usage
				if err := json.Unmarshal(openRouterJSON(t, resp.Usage), &usage); err != nil {
					t.Fatal(err)
				}
				if usage.ContextWindowUsed() != 109 {
					t.Errorf("stored context = %d, want 109", usage.ContextWindowUsed())
				}
			})
		}
	}
}

func TestOpenRouterResponsesDoesNotResumeWithoutEligibleCheckpoint(t *testing.T) {
	for _, sse := range []bool{false, true} {
		for _, tt := range []struct {
			name, status, reason, id string
			token                    *string
			output                   int
			replay                   ReasoningReplay
			wantError                bool
		}{
			{name: "completed checkpoint", status: "completed", reason: "max_output_tokens", id: "gemini-continuation", token: new("state"), output: 1},
			{name: "missing token", status: "incomplete", reason: "max_output_tokens", id: "gemini-continuation", output: 1},
			{name: "empty token", status: "incomplete", reason: "max_output_tokens", id: "gemini-continuation", token: new(""), output: 1},
			{name: "ordinary reasoning", status: "incomplete", reason: "max_output_tokens", id: "rs_ordinary", token: new("state"), output: 1},
			{name: "exhausted budget", status: "incomplete", reason: "max_output_tokens", id: "gemini-continuation", token: new("state"), output: 10},
			{name: "over budget", status: "incomplete", reason: "max_output_tokens", id: "gemini-continuation", token: new("state"), output: 11},
			{name: "content filter", status: "incomplete", reason: "content_filter", id: "gemini-continuation", token: new("state"), output: 1},
			{name: "missing reason", status: "incomplete", id: "gemini-continuation", token: new("state"), output: 1},
			{name: "no progress", status: "incomplete", reason: "max_output_tokens", id: "gemini-continuation", token: new("state"), output: 0, wantError: true},
			{name: "negative usage", status: "incomplete", reason: "max_output_tokens", id: "gemini-continuation", token: new("state"), output: -1},
			{name: "explicit none", status: "incomplete", reason: "max_output_tokens", id: "gemini-continuation", token: new("state"), output: 1, replay: ReasoningReplayNone},
			{name: "summary replay", status: "incomplete", reason: "max_output_tokens", id: "gemini-continuation", token: new("state"), output: 1, replay: ReasoningReplayContent},
			{name: "unknown replay", status: "incomplete", reason: "max_output_tokens", id: "gemini-continuation", token: new("state"), output: 1, replay: "unsupported"},
		} {
			t.Run(fmt.Sprintf("%s/sse=%t", tt.name, sse), func(t *testing.T) {
				calls := 0
				svc := openRouterResponsesService(t, "google", openRouterResponsesModel, func(call int, _ openRouterResponsesRequest) *http.Response {
					calls = call
					if call != 1 {
						t.Fatalf("%s resumed despite an ineligible checkpoint", tt.name)
					}
					checkpoint := openRouterReasoning(tt.id, "")
					delete(checkpoint, "encrypted_content")
					if tt.token != nil {
						checkpoint["encrypted_content"] = *tt.token
					}
					return openRouterHTTPResponse(t, openRouterResponse(tt.status, tt.reason, []any{
						checkpoint, openRouterText("partial answer"),
					}, 20, tt.output, .1), sse, nil)
				})
				svc.MaxTokens = 10
				svc.ReasoningReplay = tt.replay
				if _, err := svc.Do(t.Context(), openRouterUserRequest()); (err != nil) != tt.wantError {
					t.Fatalf("Do error = %v, wantError=%t", err, tt.wantError)
				}
				if calls != 1 {
					t.Fatalf("requests = %d, want 1", calls)
				}
			})
		}
	}
}

func TestOpenRouterResponsesContinuationRejectsRepeatedToken(t *testing.T) {
	calls := 0
	svc := openRouterResponsesService(t, "openrouter", openRouterResponsesModel, func(call int, _ openRouterResponsesRequest) *http.Response {
		calls = call
		if call > 2 {
			t.Fatal("repeated continuation token made no progress but triggered another request")
		}
		return openRouterHTTPResponse(t, openRouterResponse("incomplete", "max_output_tokens", []any{
			openRouterReasoning("gemini-continuation", "unchanged-token"),
		}, 20, 1, .1), false, nil)
	})
	// Positive output usage is not progress if the opaque checkpoint repeats.
	if _, err := svc.Do(t.Context(), openRouterUserRequest()); err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("Do error = %v, want repeated-checkpoint error", err)
	}
	if calls != 2 {
		t.Fatalf("requests = %d, want initial request and one continuation", calls)
	}
}

func TestOpenRouterResponsesContinuationStopsAtCumulativeBudget(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse=%t", sse), func(t *testing.T) {
			calls := 0
			svc := openRouterResponsesService(t, "google", openRouterResponsesModel, func(call int, req openRouterResponsesRequest) *http.Response {
				calls = call
				if call > 2 {
					t.Fatal("continued after spending the original cumulative budget")
				}
				if req.MaxOutputTokens != 10 {
					t.Fatalf("hop %d budget = %d, want unchanged 10", call, req.MaxOutputTokens)
				}
				if call == 1 {
					return openRouterHTTPResponse(t, openRouterResponse("incomplete", "max_output_tokens", []any{
						openRouterReasoning("gemini-continuation", "earlier-checkpoint"),
						openRouterReasoning("gemini-continuation", "latest-checkpoint"),
					}, 20, 6, .1), sse, nil)
				}
				if len(req.Input) != 2 || req.Input[1]["encrypted_content"] != "latest-checkpoint" {
					t.Fatalf("continuation must use only the latest checkpoint: %+v", req.Input)
				}
				return openRouterHTTPResponse(t, openRouterResponse("incomplete", "max_output_tokens", []any{
					openRouterReasoning("gemini-continuation", "spent-checkpoint"), openRouterText("finished budget"),
				}, 25, 4, .2), sse, nil)
			})
			svc.MaxTokens = 10
			response, err := svc.Do(t.Context(), openRouterUserRequest())
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || response.Usage.OutputTokens != 10 || response.StopReason != llm.StopReasonMaxTokens {
				t.Fatalf("calls=%d, response=%+v", calls, response)
			}
		})
	}
}

func TestOpenRouterResponsesContinuationKeepsOnlyFinalToolCall(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse=%t", sse), func(t *testing.T) {
			calls := 0
			svc := openRouterResponsesService(t, "google", openRouterResponsesModel, func(call int, req openRouterResponsesRequest) *http.Response {
				calls = call
				if call > 2 {
					t.Fatal("completed tool call triggered another continuation")
				}
				arguments := `{"value":"partial"}`
				status, reason := "incomplete", "max_output_tokens"
				if call == 2 {
					arguments = `{"value":"complete"}`
					status, reason = "completed", ""
					for _, item := range req.Input {
						if item["type"] == "function_call" {
							t.Fatalf("partial tool call was replayed: %+v", req.Input)
						}
					}
				}
				return openRouterHTTPResponse(t, openRouterResponse(status, reason, []any{
					openRouterReasoning("gemini-continuation", fmt.Sprintf("token-%d", call)),
					map[string]any{"type": "function_call", "id": "fc_item", "call_id": "call_X", "name": "inspect", "arguments": arguments},
				}, 20, 2, .1), sse, nil)
			})
			resp, err := svc.Do(t.Context(), openRouterUserRequest())
			if err != nil {
				t.Fatal(err)
			}
			var tools []llm.Content
			for _, content := range resp.Content {
				if content.Type == llm.ContentTypeToolUse {
					tools = append(tools, content)
				}
			}
			if calls != 2 || resp.StopReason != llm.StopReasonToolUse || len(tools) != 1 {
				t.Fatalf("calls=%d, stop=%v, tools=%+v", calls, resp.StopReason, tools)
			}
			if tools[0].ID != "call_X" || tools[0].ToolName != "inspect" || string(tools[0].ToolInput) != `{"value":"complete"}` {
				t.Fatalf("settled tool call = %+v", tools[0])
			}
		})
	}
}

func TestOpenRouterResponsesGeminiFormatCannotReplayToNativeProviders(t *testing.T) {
	for _, route := range []struct{ provider, endpoint, model string }{
		{"openai", OpenAIURL, "gpt-5.4"},
		{"xai", "https://api.x.ai/v1", "grok-4.5"},
		{"fireworks", FireworksURL, "accounts/fireworks/models/glm-5p3"},
	} {
		for _, replay := range []ReasoningReplay{ReasoningReplayAuto, ReasoningReplayContent} {
			t.Run(route.provider+"/"+string(replay), func(t *testing.T) {
				var input []map[string]any
				svc := &ResponsesService{
					APIKey: "test-key", Model: modelForTest(route.model), ModelURL: route.endpoint,
					ProviderName: route.provider, ReasoningReplay: replay, Backoff: []time.Duration{0},
					HTTPC: &http.Client{Transport: openRouterResponsesTransport(func(r *http.Request) (*http.Response, error) {
						if r.URL.String() != route.endpoint+"/responses" {
							t.Fatalf("request URL = %s", r.URL)
						}
						var req openRouterResponsesRequest
						if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
							t.Fatal(err)
						}
						input = req.Input
						return openRouterHTTPResponse(t, openRouterResponse("completed", "", []any{openRouterText("ok")}, 1, 1, 0), false, nil), nil
					})},
				}
				// Legacy stored messages have no origin. The Gemini format is
				// sufficient evidence that this opaque state belongs elsewhere.
				message := openRouterStoredMessage(t, llm.Message{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeThinking, OpenAIResponsesReasoning: &llm.OpenAIResponsesReasoningMetadata{
							ID: "rs_gemini", Format: openRouterReasoningFormat, EncryptedContent: "private-gemini-state",
						}},
						{Type: llm.ContentTypeThinking, OpenAIResponsesReasoning: &llm.OpenAIResponsesReasoningMetadata{
							ID: "rs_summary", Format: openRouterReasoningFormat,
							Summary: []llm.OpenAIResponsesReasoningSummary{{Type: "summary_text", Text: "Private Gemini summary."}},
						}},
						{Type: llm.ContentTypeText, Text: "visible answer"},
					},
				})
				before := string(openRouterJSON(t, message))
				req := openRouterUserRequest()
				req.Messages = []llm.Message{message}
				if _, err := svc.Do(t.Context(), req); err != nil {
					t.Fatal(err)
				}
				if len(input) != 1 || input[0]["type"] != "message" {
					t.Fatalf("Gemini metadata escaped to %s: %+v", route.provider, input)
				}
				if string(openRouterJSON(t, req.Messages[0])) != before {
					t.Fatal("native replay mutated stored Gemini history")
				}
			})
		}
	}
}

type openRouterCancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b openRouterCancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func TestOpenRouterResponsesContinuationCancellation(t *testing.T) {
	for _, betweenHops := range []bool{true, false} {
		t.Run(fmt.Sprintf("between_hops=%t", betweenHops), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			svc := openRouterResponsesService(t, "google", openRouterResponsesModel, func(call int, _ openRouterResponsesRequest) *http.Response {
				calls = call
				response := openRouterHTTPResponse(t, openRouterResponse("incomplete", "max_output_tokens", []any{
					openRouterReasoning("gemini-continuation", "next-state"),
				}, 20, 1, .1), false, nil)
				if betweenHops {
					response.Body = openRouterCancelBody{ReadCloser: response.Body, cancel: cancel}
				}
				return response
			})
			if !betweenHops {
				first := svc.HTTPC.Transport
				svc.HTTPC.Transport = openRouterResponsesTransport(func(r *http.Request) (*http.Response, error) {
					if calls != 0 {
						calls++
						cancel()
						return nil, r.Context().Err()
					}
					return first.RoundTrip(r)
				})
			}
			_, err := svc.Do(ctx, openRouterUserRequest())
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Do error = %v, want context cancellation", err)
			}
			want := 2
			if betweenHops {
				want = 1
			}
			if calls != want {
				t.Fatalf("requests = %d, want %d", calls, want)
			}
		})
	}
}

func TestOpenRouterResponsesContextWindowOverrideJSON(t *testing.T) {
	usage := llm.Usage{InputTokens: 100, CacheReadInputTokens: 20, OutputTokens: 5}
	var fields map[string]any
	if err := json.Unmarshal(openRouterJSON(t, usage), &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["context_window_tokens"]; present {
		t.Fatal("zero context_window_tokens was not omitted")
	}
	if usage.ContextWindowUsed() != 125 {
		t.Fatalf("legacy context usage = %d, want 125", usage.ContextWindowUsed())
	}
	usage.ContextWindowTokens = 42
	if usage.ContextWindowUsed() != 42 {
		t.Fatalf("explicit context usage = %d, want 42", usage.ContextWindowUsed())
	}
	if err := json.Unmarshal(openRouterJSON(t, usage), &fields); err != nil {
		t.Fatal(err)
	}
	if fields["context_window_tokens"] != float64(42) {
		t.Fatalf("context_window_tokens JSON = %+v", fields)
	}
}
