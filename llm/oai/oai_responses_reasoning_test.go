package oai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
)

func TestResponsesReasoningStateStatelessRoundTrip(t *testing.T) {
	svc := &ResponsesService{}
	response := &responsesResponse{
		ID:    "resp_123",
		Model: "gpt-5.4",
		Output: []responsesOutputItem{
			{
				ID:               "rs_first",
				Type:             "reasoning",
				EncryptedContent: "encrypted-first",
				Summary: []responsesSummary{
					{Type: "summary_text", Text: "First part."},
					{Type: "summary_text", Text: "Second part."},
				},
			},
			{
				ID:               "rs_empty_summary",
				Type:             "reasoning",
				EncryptedContent: "encrypted-empty-summary",
			},
			{
				ID:   "rs_bare",
				Type: "reasoning",
				Summary: []responsesSummary{
					{Type: "summary_text", Text: "Display-only legacy summary."},
				},
			},
			{
				ID:        "fc_provider_item",
				Type:      "function_call",
				CallID:    "call_weather",
				Name:      "weather",
				Arguments: `{"city":"Paris"}`,
			},
		},
	}

	got := svc.toLLMResponseFromResponses(response, nil)
	if len(got.Content) != 4 {
		t.Fatalf("content count = %d, want 4: %+v", len(got.Content), got.Content)
	}

	first := got.Content[0]
	if first.Type != llm.ContentTypeThinking || first.Text != "First part.\nSecond part." {
		t.Fatalf("first reasoning display = %+v", first)
	}
	if first.OpenAIResponsesReasoning == nil {
		t.Fatal("first reasoning metadata is nil")
	}
	if first.OpenAIResponsesReasoning.ID != "rs_first" || first.OpenAIResponsesReasoning.EncryptedContent != "encrypted-first" {
		t.Fatalf("first reasoning metadata = %+v", first.OpenAIResponsesReasoning)
	}
	if len(first.OpenAIResponsesReasoning.Summary) != 2 || first.OpenAIResponsesReasoning.Summary[1].Text != "Second part." {
		t.Fatalf("first reasoning summary metadata = %+v", first.OpenAIResponsesReasoning.Summary)
	}

	emptySummary := got.Content[1]
	if emptySummary.Type != llm.ContentTypeThinking || emptySummary.Text != "" || emptySummary.OpenAIResponsesReasoning == nil {
		t.Fatalf("empty-summary reasoning = %+v", emptySummary)
	}
	if emptySummary.OpenAIResponsesReasoning.EncryptedContent != "encrypted-empty-summary" {
		t.Fatalf("empty-summary encrypted content = %q", emptySummary.OpenAIResponsesReasoning.EncryptedContent)
	}

	bare := got.Content[2]
	if bare.Text != "Display-only legacy summary." || bare.OpenAIResponsesReasoning == nil {
		t.Fatalf("bare reasoning display/metadata = %+v", bare)
	}
	if bare.OpenAIResponsesReasoning.EncryptedContent != "" {
		t.Fatalf("bare encrypted content = %q, want empty", bare.OpenAIResponsesReasoning.EncryptedContent)
	}

	assistant := got.ToMessage()
	items := fromLLMMessageResponses(assistant)
	if len(items) != 3 {
		t.Fatalf("replayed items = %d, want 3: %+v", len(items), items)
	}
	if items[0].Type != "reasoning" || items[0].EncryptedContent != "encrypted-first" || items[0].Summary == nil || len(*items[0].Summary) != 2 {
		t.Fatalf("replayed first reasoning = %+v", items[0])
	}
	if items[1].Type != "reasoning" || items[1].EncryptedContent != "encrypted-empty-summary" || items[1].Summary == nil || len(*items[1].Summary) != 0 {
		t.Fatalf("replayed empty-summary reasoning = %+v", items[1])
	}
	if items[2].Type != "function_call" || items[2].CallID != "call_weather" {
		t.Fatalf("replayed function call = %+v", items[2])
	}

	wire, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	var wireItems []map[string]any
	if err := json.Unmarshal(wire, &wireItems); err != nil {
		t.Fatal(err)
	}
	for i, wantID := range []string{"rs_first", "rs_empty_summary"} {
		item := wireItems[i]
		if item["id"] != wantID {
			t.Fatalf("reasoning item %d id = %#v, want %q", i, item["id"], wantID)
		}
		if _, ok := item["summary"].([]any); !ok {
			t.Fatalf("reasoning item %d summary is not an array: %#v", i, item)
		}
	}
	for _, item := range wireItems {
		if item["encrypted_content"] == nil && item["type"] == "reasoning" {
			t.Fatalf("bare reasoning item was replayed: %#v", item)
		}
	}
	legacy := fromLLMMessageResponses(llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{Type: llm.ContentTypeThinking, Text: "legacy display summary"},
			{Type: llm.ContentTypeToolUse, ID: "call_legacy", ToolName: "legacy", ToolInput: json.RawMessage(`{}`)},
		},
	})
	if len(legacy) != 1 || legacy[0].Type != "function_call" || legacy[0].CallID != "call_legacy" {
		t.Fatalf("legacy bare reasoning was not safely omitted: %+v", legacy)
	}

	toolResults := fromLLMMessageResponses(llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type:      llm.ContentTypeToolResult,
			ToolUseID: "call_weather",
			ToolResult: []llm.Content{{
				Type: llm.ContentTypeText,
				Text: "sunny",
			}},
		}},
	})
	if len(toolResults) != 1 || toolResults[0].Type != "function_call_output" || toolResults[0].CallID != "call_weather" {
		t.Fatalf("tool continuation = %+v", toolResults)
	}
}

func TestResponsesServiceCodexRequestContract(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responsesResponse{
			ID:     "resp_ok",
			Status: "completed",
			Model:  "gpt-5.4",
			Output: []responsesOutputItem{{
				Type:    "message",
				Role:    "assistant",
				Content: []responsesContent{{Type: "output_text", Text: "ok"}},
			}},
		})
	}))
	defer server.Close()

	svc := &ResponsesService{
		APIKey:        "test-key",
		Model:         GPT54,
		ModelURL:      server.URL,
		ProviderName:  "openai",
		ThinkingLevel: llm.ThinkingLevelMedium,
	}
	ctx := llmhttp.WithConversationID(context.Background(), "conversation-123")
	_, err := svc.Do(ctx, &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
		}},
		Tools: []*llm.Tool{{
			Name:        "weather",
			Description: "Get the weather",
			InputSchema: llm.EmptySchema(),
		}},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	include, ok := got["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v", got["include"])
	}
	if got["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %#v, want auto", got["tool_choice"])
	}
	if got["parallel_tool_calls"] != true {
		t.Fatalf("parallel_tool_calls = %#v, want true", got["parallel_tool_calls"])
	}
	if got["store"] != false || got["stream"] != true {
		t.Fatalf("state/stream contract: store=%#v stream=%#v", got["store"], got["stream"])
	}
	if _, ok := got["previous_response_id"]; ok {
		t.Fatalf("stateless full-history request contains previous_response_id: %#v", got["previous_response_id"])
	}
	if got["prompt_cache_key"] != "conversation-123" {
		t.Fatalf("prompt_cache_key = %#v", got["prompt_cache_key"])
	}
	reasoning, ok := got["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "medium" || reasoning["summary"] != "auto" {
		t.Fatalf("reasoning = %#v", got["reasoning"])
	}
	text, ok := got["text"].(map[string]any)
	if !ok || text["verbosity"] != "low" {
		t.Fatalf("text = %#v", got["text"])
	}
}

func TestResponsesParallelToolCallContinuation(t *testing.T) {
	svc := &ResponsesService{}
	response := svc.toLLMResponseFromResponses(&responsesResponse{
		ID:    "resp_parallel",
		Model: "gpt-5.4",
		Output: []responsesOutputItem{
			{ID: "fc_item_1", Type: "function_call", CallID: "call_1", Name: "first", Arguments: `{}`},
			{ID: "fc_item_2", Type: "function_call", CallID: "call_2", Name: "second", Arguments: `{}`},
		},
	}, nil)
	if response.StopReason != llm.StopReasonToolUse || len(response.Content) != 2 {
		t.Fatalf("parallel tool response = %+v", response)
	}

	assistantItems := fromLLMMessageResponses(response.ToMessage())
	if len(assistantItems) != 2 || assistantItems[0].CallID != "call_1" || assistantItems[1].CallID != "call_2" {
		t.Fatalf("parallel function calls = %+v", assistantItems)
	}
	resultItems := fromLLMMessageResponses(llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeToolResult, ToolUseID: "call_1", ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "one"}}},
			{Type: llm.ContentTypeToolResult, ToolUseID: "call_2", ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "two"}}},
		},
	})
	if len(resultItems) != 2 || resultItems[0].CallID != "call_1" || resultItems[1].CallID != "call_2" {
		t.Fatalf("parallel function outputs = %+v", resultItems)
	}
}

func TestResponsesServiceOpenAIRequestDefaultsAreProviderIsolated(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responsesResponse{
			ID:     "resp_ok",
			Status: "completed",
			Model:  "grok-4.5",
			Output: []responsesOutputItem{{
				Type:    "message",
				Role:    "assistant",
				Content: []responsesContent{{Type: "output_text", Text: "ok"}},
			}},
		})
	}))
	defer server.Close()

	svc := &ResponsesService{
		APIKey:       "test-key",
		Model:        Grok45,
		ModelURL:     server.URL,
		ProviderName: "xai",
	}
	ctx := llmhttp.WithConversationID(context.Background(), "conversation-123")
	request := &llm.Request{
		Messages: []llm.Message{{
			Role: llm.MessageRoleAssistant,
			Content: []llm.Content{
				{
					Type: llm.ContentTypeThinking,
					OpenAIResponsesReasoning: &llm.OpenAIResponsesReasoningMetadata{
						ID:               "rs_private",
						EncryptedContent: "must-not-leak",
					},
				},
				{Type: llm.ContentTypeText, Text: "visible"},
			},
		}},
	}
	_, err := svc.Do(ctx, request)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if request.Messages[0].Content[0].OpenAIResponsesReasoning == nil {
		t.Fatal("Do mutated provider metadata in request history")
	}

	for _, field := range []string{"include", "parallel_tool_calls", "prompt_cache_key", "text", "tool_choice"} {
		if _, ok := got[field]; ok {
			t.Fatalf("xAI request contains OpenAI field %q: %#v", field, got[field])
		}
	}
	input, ok := got["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input = %#v", got["input"])
	}
	item, ok := input[0].(map[string]any)
	if !ok || item["type"] != "message" {
		t.Fatalf("input[0] = %#v", input[0])
	}
}

func TestResponsesServiceTextVerbosityFollowsModelMetadata(t *testing.T) {
	tests := []struct {
		model Model
		want  string
	}{
		{model: GPT54, want: "low"},
		{model: GPT54Mini, want: "medium"},
		{model: GPT54Nano, want: "medium"},
		{model: GPT53Codex, want: "low"},
		{model: GPT41, want: ""},
		{model: modelForTest("gpt-5-custom"), want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.model.ModelName, func(t *testing.T) {
			var got map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(responsesResponse{
					ID:     "resp_ok",
					Status: "completed",
					Model:  tt.model.ModelName,
					Output: []responsesOutputItem{{
						Type:    "message",
						Role:    "assistant",
						Content: []responsesContent{{Type: "output_text", Text: "ok"}},
					}},
				})
			}))
			defer server.Close()

			svc := &ResponsesService{
				APIKey:       "test-key",
				Model:        tt.model,
				ModelURL:     server.URL,
				ProviderName: "openai",
			}
			if _, err := svc.Do(context.Background(), &llm.Request{
				Messages: []llm.Message{{
					Role:    llm.MessageRoleUser,
					Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
				}},
			}); err != nil {
				t.Fatalf("Do: %v", err)
			}

			text, present := got["text"].(map[string]any)
			if tt.want == "" {
				if present {
					t.Fatalf("text = %#v, want field omitted", text)
				}
				return
			}
			if !present || text["verbosity"] != tt.want {
				t.Fatalf("text = %#v, want verbosity %q", got["text"], tt.want)
			}
		})
	}
}

func TestParseResponsesSSEPreservesReasoningStateAndOutputOrder(t *testing.T) {
	stream := strings.Join([]string{
		responsesSSEOutputDone(t, 1, responsesOutputItem{
			ID:        "fc_item",
			Type:      "function_call",
			CallID:    "call_1",
			Name:      "tool",
			Arguments: `{}`,
		}),
		responsesSSEOutputDone(t, 0, responsesOutputItem{
			ID:               "rs_1",
			Type:             "reasoning",
			EncryptedContent: "from-output-item-done",
			Summary:          []responsesSummary{{Type: "summary_text", Text: "thinking"}},
		}),
		responsesSSECompleted(t, responsesResponse{
			ID:     "resp_1",
			Status: "completed",
			Output: []responsesOutputItem{
				{ID: "rs_1", Type: "reasoning", Summary: []responsesSummary{{Type: "summary_text", Text: "thinking"}}},
				{ID: "fc_item", Type: "function_call", CallID: "call_1", Name: "tool", Arguments: `{}`},
			},
		}),
	}, "")

	resp, err := parseResponsesSSEStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSEStream: %v", err)
	}
	if len(resp.Output) != 2 || resp.Output[0].Type != "reasoning" || resp.Output[1].Type != "function_call" {
		t.Fatalf("output order = %+v", resp.Output)
	}
	if resp.Output[0].EncryptedContent != "from-output-item-done" {
		t.Fatalf("encrypted content = %q", resp.Output[0].EncryptedContent)
	}
}

func TestParseResponsesSSECompletedReasoningStateWins(t *testing.T) {
	stream := responsesSSEOutputDone(t, 0, responsesOutputItem{
		ID:               "rs_1",
		Type:             "reasoning",
		EncryptedContent: "from-output-item-done",
	}) + responsesSSECompleted(t, responsesResponse{
		ID:     "resp_1",
		Status: "completed",
		Output: []responsesOutputItem{{
			ID:               "rs_1",
			Type:             "reasoning",
			EncryptedContent: "from-completed",
		}},
	})

	resp, err := parseResponsesSSEStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSEStream: %v", err)
	}
	if got := resp.Output[0].EncryptedContent; got != "from-completed" {
		t.Fatalf("encrypted content = %q, want completed value", got)
	}
}

func TestParseResponsesSSERejectsInterruptedReasoningState(t *testing.T) {
	stream := responsesSSEOutputDone(t, 0, responsesOutputItem{
		ID:               "rs_partial",
		Type:             "reasoning",
		EncryptedContent: "partial",
	})
	if _, err := parseResponsesSSEStream(strings.NewReader(stream), nil); err == nil || !strings.Contains(err.Error(), "incomplete stream") {
		t.Fatalf("error = %v, want incomplete stream", err)
	}
}

func responsesSSEOutputDone(t *testing.T, outputIndex int, item responsesOutputItem) string {
	t.Helper()
	b, err := json.Marshal(struct {
		Type        string              `json:"type"`
		OutputIndex int                 `json:"output_index"`
		Item        responsesOutputItem `json:"item"`
	}{
		Type:        "response.output_item.done",
		OutputIndex: outputIndex,
		Item:        item,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("event: response.output_item.done\ndata: %s\n\n", b)
}

func responsesSSECompleted(t *testing.T, response responsesResponse) string {
	t.Helper()
	b, err := json.Marshal(struct {
		Type     string            `json:"type"`
		Response responsesResponse `json:"response"`
	}{
		Type:     "response.completed",
		Response: response,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("event: response.completed\ndata: %s\n\n", b)
}
