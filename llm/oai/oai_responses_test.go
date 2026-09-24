package oai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/imageutil"
	"shelley.exe.dev/llm/llmhttp"
)

func TestResponsesServiceBasic(t *testing.T) {
	// This is a basic compile-time test to ensure ResponsesService implements llm.Service
	var _ llm.Service = (*ResponsesService)(nil)
}

func TestFromLLMMessageResponses(t *testing.T) {
	tests := []struct {
		name     string
		msg      llm.Message
		expected int // expected number of output items
	}{
		{
			name: "simple user message",
			msg: llm.Message{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Hello"},
				},
			},
			expected: 1,
		},
		{
			name: "assistant message with text",
			msg: llm.Message{
				Role: llm.MessageRoleAssistant,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Hi there"},
				},
			},
			expected: 1,
		},
		{
			name: "message with tool use",
			msg: llm.Message{
				Role: llm.MessageRoleAssistant,
				Content: []llm.Content{
					{
						Type:      llm.ContentTypeToolUse,
						ID:        "call_123",
						ToolName:  "get_weather",
						ToolInput: json.RawMessage(`{"location":"SF"}`),
					},
				},
			},
			expected: 1,
		},
		{
			name: "message with tool result",
			msg: llm.Message{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type:      llm.ContentTypeToolResult,
						ToolUseID: "call_123",
						ToolResult: []llm.Content{
							{Type: llm.ContentTypeText, Text: "72 degrees"},
						},
					},
				},
			},
			expected: 1,
		},
		{
			name: "message with text and tool use",
			msg: llm.Message{
				Role: llm.MessageRoleAssistant,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Let me check"},
					{
						Type:      llm.ContentTypeToolUse,
						ID:        "call_123",
						ToolName:  "get_weather",
						ToolInput: json.RawMessage(`{"location":"SF"}`),
					},
				},
			},
			expected: 2, // one message item, one function_call item
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := fromLLMMessageResponses(tt.msg, responsesReasoningReplayEncrypted)
			if len(items) != tt.expected {
				t.Errorf("expected %d items, got %d", tt.expected, len(items))
			}

			// Verify structure based on content type
			for _, item := range items {
				switch item.Type {
				case "message":
					if item.Role == "" {
						t.Error("message item missing role")
					}
					if len(item.Content) == 0 {
						t.Error("message item has no content")
					}
				case "function_call":
					if item.CallID == "" {
						t.Error("function_call item missing call_id")
					}
					if item.Name == "" {
						t.Error("function_call item missing name")
					}
				case "function_call_output":
					if item.CallID == "" {
						t.Error("function_call_output item missing call_id")
					}
				}
			}
		})
	}
}

func TestFromLLMMessageResponsesWithImage(t *testing.T) {
	items := fromLLMMessageResponses(llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "What is in this image?"},
			{Type: llm.ContentTypeText, MediaType: "image/png", Data: "abc123"},
		},
	}, responsesReasoningReplayEncrypted)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if len(items[0].Content) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(items[0].Content))
	}
	if items[0].Content[0].Type != "input_text" || items[0].Content[0].Text != "What is in this image?" {
		t.Errorf("unexpected text content: %+v", items[0].Content[0])
	}
	if items[0].Content[1].Type != "input_image" || items[0].Content[1].ImageURL != "data:image/png;base64,abc123" {
		t.Errorf("unexpected image content: %+v", items[0].Content[1])
	}
}

func TestFromLLMMessageResponsesWithImageOnlyAndMultipleImages(t *testing.T) {
	items := fromLLMMessageResponses(llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, MediaType: "image/png", Data: "first"},
			{Type: llm.ContentTypeText, Text: "between"},
			{Type: llm.ContentTypeText, MediaType: "image/jpeg", Data: "second"},
		},
	}, responsesReasoningReplayEncrypted)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if len(items[0].Content) != 3 {
		t.Fatalf("expected 3 content parts, got %d", len(items[0].Content))
	}
	if items[0].Content[0].ImageURL != "data:image/png;base64,first" || items[0].Content[1].Text != "between" || items[0].Content[2].ImageURL != "data:image/jpeg;base64,second" {
		t.Errorf("content order not preserved: %+v", items[0].Content)
	}
}

func TestResponsesImageContentJSON(t *testing.T) {
	got, err := json.Marshal(responsesImageContent(llm.Content{Type: llm.ContentTypeText, MediaType: "image/png", Data: "abc123"}))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"type":"input_image","image_url":"data:image/png;base64,abc123","detail":"auto"}`
	if string(got) != want {
		t.Fatalf("image content JSON = %s, want %s", got, want)
	}
}

func TestFromLLMMessageResponsesWithToolResultImage(t *testing.T) {
	items := fromLLMMessageResponses(llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type:      llm.ContentTypeToolResult,
			ToolUseID: "call_img",
			ToolResult: []llm.Content{
				{Type: llm.ContentTypeText, Text: "Screenshot captured"},
				{Type: llm.ContentTypeText, MediaType: "image/jpeg", Data: "xyz789"},
			},
		}},
	}, responsesReasoningReplayEncrypted)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Type != "function_call_output" || items[0].Output != "Screenshot captured" {
		t.Errorf("unexpected function output: %+v", items[0])
	}
	if items[1].Type != "message" || items[1].Role != "user" || len(items[1].Content) != 2 {
		t.Fatalf("unexpected image message: %+v", items[1])
	}
	if items[1].Content[1].Type != "input_image" || items[1].Content[1].ImageURL != "data:image/jpeg;base64,xyz789" {
		t.Errorf("unexpected tool image content: %+v", items[1].Content[1])
	}
}

func TestFromLLMMessageResponsesWithImageOnlyToolResultAndRegularContent(t *testing.T) {
	items := fromLLMMessageResponses(llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "call_img_only",
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, MediaType: "image/png", Data: "onlyimage"},
				},
			},
			{Type: llm.ContentTypeText, Text: "regular text"},
		},
	}, responsesReasoningReplayEncrypted)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d: %+v", len(items), items)
	}
	if items[0].Type != "function_call_output" || items[0].Output != " " {
		t.Errorf("unexpected image-only function output: %+v", items[0])
	}
	if items[1].Type != "message" || items[1].Role != "user" || len(items[1].Content) != 2 || items[1].Content[1].ImageURL != "data:image/png;base64,onlyimage" {
		t.Errorf("unexpected adjacent image message: %+v", items[1])
	}
	if items[2].Type != "message" || items[2].Content[0].Text != "regular text" {
		t.Errorf("regular content should follow tool image message: %+v", items[2])
	}
}

func TestResponsesContentOmitsEmptyTextForImages(t *testing.T) {
	got, err := json.Marshal(responsesImageContent(llm.Content{Type: llm.ContentTypeText, MediaType: "image/png", Data: "abc123"}))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(got), `"text"`) {
		t.Fatalf("image content should not include empty text field: %s", got)
	}
}

func TestFitResponsesImagesToPatchLimit(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 100, 80))
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	originalData := base64.StdEncoding.EncodeToString(encoded.Bytes())
	messages := []llm.Message{{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type:      llm.ContentTypeToolResult,
			ToolUseID: "call_image",
			ToolResult: []llm.Content{{
				Type:      llm.ContentTypeText,
				MediaType: "image/png",
				Data:      originalData,
			}},
		}},
	}}

	got, err := fitResponsesImagesToPatchLimit(messages, 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	content := got[0].Content[0].ToolResult[0]
	if content.Data == originalData {
		t.Fatal("oversized image was not resized")
	}
	data, err := base64.StdEncoding.DecodeString(content.Data)
	if err != nil {
		t.Fatal(err)
	}
	width, height, err := imageutil.DecodeDimensions(data)
	if err != nil {
		t.Fatal(err)
	}
	patches := ((width + 9) / 10) * ((height + 9) / 10)
	if patches > 20 {
		t.Fatalf("resized image is %dx%d (%d patches), want <= 20", width, height, patches)
	}
	if content.DisplayWidth != width || content.DisplayHeight != height {
		t.Fatalf("display dimensions = %dx%d, want %dx%d", content.DisplayWidth, content.DisplayHeight, width, height)
	}
	if messages[0].Content[0].ToolResult[0].Data != originalData {
		t.Fatal("input messages were mutated")
	}
}

func TestFromLLMToolResponses(t *testing.T) {
	tool := &llm.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: llm.MustSchema(`{
			"type": "object",
			"properties": {
				"param": {"type": "string"}
			}
		}`),
	}

	rtool := fromLLMToolResponses(tool)

	if rtool.Type != "function" {
		t.Errorf("expected type 'function', got %s", rtool.Type)
	}
	if rtool.Name != "test_tool" {
		t.Errorf("expected name 'test_tool', got %s", rtool.Name)
	}
	if rtool.Description != "A test tool" {
		t.Errorf("expected description 'A test tool', got %s", rtool.Description)
	}
	if len(rtool.Parameters) == 0 {
		t.Error("expected parameters to be set")
	}
}

func TestResponsesInstructionsFromLLMSystem(t *testing.T) {
	tests := []struct {
		name   string
		system []llm.SystemContent
		want   string
	}{
		{
			name:   "empty system",
			system: []llm.SystemContent{},
			want:   "",
		},
		{
			name: "single system message",
			system: []llm.SystemContent{
				{Text: "You are a helpful assistant"},
			},
			want: "You are a helpful assistant",
		},
		{
			name: "multiple system messages",
			system: []llm.SystemContent{
				{Text: "You are a helpful assistant"},
				{Text: "Be concise"},
			},
			want: "You are a helpful assistant\nBe concise",
		},
		{
			name: "skips empty system messages",
			system: []llm.SystemContent{
				{Text: ""},
				{Text: "You are a helpful assistant"},
				{Text: ""},
				{Text: "Be concise"},
			},
			want: "You are a helpful assistant\nBe concise",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := responsesInstructionsFromLLMSystem(tt.system)
			if got != tt.want {
				t.Errorf("instructions = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToLLMResponseFromResponses(t *testing.T) {
	svc := &ResponsesService{}

	tests := []struct {
		name           string
		resp           *responsesResponse
		expectedReason llm.StopReason
		contentCount   int
	}{
		{
			name: "simple text response",
			resp: &responsesResponse{
				ID:    "resp_123",
				Model: "gpt-5.3-codex",
				Output: []responsesOutputItem{
					{
						Type: "message",
						Role: "assistant",
						Content: []responsesContent{
							{Type: "output_text", Text: "Hello!"},
						},
					},
				},
			},
			expectedReason: llm.StopReasonStopSequence,
			contentCount:   1,
		},
		{
			name: "response with function call",
			resp: &responsesResponse{
				ID:    "resp_123",
				Model: "gpt-5.3-codex",
				Output: []responsesOutputItem{
					{
						Type:      "function_call",
						CallID:    "call_123",
						Name:      "get_weather",
						Arguments: `{"location":"SF"}`,
					},
				},
			},
			expectedReason: llm.StopReasonToolUse,
			contentCount:   1,
		},
		{
			name: "response with reasoning and message",
			resp: &responsesResponse{
				ID:    "resp_123",
				Model: "gpt-5.3-codex",
				Output: []responsesOutputItem{
					{
						Type: "reasoning",
						Summary: []responsesSummary{
							{Type: "summary_text", Text: "Let me think"},
							{Type: "summary_text", Text: "about this"},
						},
					},
					{
						Type: "message",
						Role: "assistant",
						Content: []responsesContent{
							{Type: "output_text", Text: "Here's the answer"},
						},
					},
				},
			},
			expectedReason: llm.StopReasonStopSequence,
			contentCount:   2, // reasoning + text
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			llmResp := svc.toLLMResponseFromResponses(tt.resp, nil)

			if llmResp.ID != tt.resp.ID {
				t.Errorf("expected ID %s, got %s", tt.resp.ID, llmResp.ID)
			}
			if llmResp.Model != tt.resp.Model {
				t.Errorf("expected model %s, got %s", tt.resp.Model, llmResp.Model)
			}
			if llmResp.StopReason != tt.expectedReason {
				t.Errorf("expected stop reason %v, got %v", tt.expectedReason, llmResp.StopReason)
			}
			if len(llmResp.Content) != tt.contentCount {
				t.Errorf("expected %d content items, got %d", tt.contentCount, len(llmResp.Content))
			}
		})
	}
}

// TestResponsesReasoningSummaryUnmarshal verifies that a reasoning output item
// with a structured summary array (objects, not bare strings) unmarshals
// successfully. Regression test for issue #192.
func TestResponsesReasoningSummaryUnmarshal(t *testing.T) {
	raw := []byte(`{
		"id": "resp_1",
		"object": "response",
		"status": "completed",
		"model": "gpt-5.3-codex",
		"output": [
			{
				"id": "rs_1",
				"type": "reasoning",
				"summary": [
					{"type": "summary_text", "text": "First thought."},
					{"type": "summary_text", "text": "Second thought."}
				]
			},
			{
				"id": "msg_1",
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "Hello."}]
			}
		],
		"usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
	}`)
	var resp responsesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Output) != 2 {
		t.Fatalf("expected 2 output items, got %d", len(resp.Output))
	}
	rs := resp.Output[0]
	if len(rs.Summary) != 2 || rs.Summary[0].Text != "First thought." || rs.Summary[1].Text != "Second thought." {
		t.Fatalf("unexpected summary: %+v", rs.Summary)
	}
	svc := &ResponsesService{}
	llmResp := svc.toLLMResponseFromResponses(&resp, nil)
	var gotThinking, gotText string
	for _, c := range llmResp.Content {
		switch c.Type {
		case llm.ContentTypeThinking:
			gotThinking = c.Text
		case llm.ContentTypeText:
			gotText = c.Text
		}
	}
	if gotThinking != "First thought.\nSecond thought." {
		t.Errorf("thinking: got %q", gotThinking)
	}
	if gotText != "Hello." {
		t.Errorf("text: got %q", gotText)
	}
}

func TestResponsesServiceConfigDetails(t *testing.T) {
	svc := &ResponsesService{
		Model:  GPT53Codex,
		APIKey: "test-key",
	}

	details := svc.ConfigDetails()

	if details["model_name"] != "gpt-5.3-codex" {
		t.Errorf("expected model_name 'gpt-5.3-codex', got %s", details["model_name"])
	}
	if details["full_url"] != "https://api.openai.com/v1/responses" {
		t.Errorf("unexpected full_url: %s", details["full_url"])
	}
	if details["has_api_key_set"] != "true" {
		t.Error("expected has_api_key_set to be true")
	}
}

// TestResponsesServiceIntegration is a live test that requires OPENAI_API_KEY
// Run with: go test -v -run TestResponsesServiceIntegration
func TestResponsesServiceIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	apiKey := os.Getenv(OpenAIAPIKeyEnv)
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}

	svc := &ResponsesService{
		APIKey: apiKey,
		Model:  GPT53Codex,
	}

	ctx := t.Context()

	t.Run("simple request", func(t *testing.T) {
		req := &llm.Request{
			Messages: []llm.Message{
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Say 'hello' and nothing else"},
					},
				},
			},
		}

		resp, err := svc.Do(ctx, req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}

		if resp.ID == "" {
			t.Error("expected response ID to be set")
		}
		if resp.Model != "gpt-5.3-codex" {
			t.Errorf("expected model gpt-5.3-codex, got %s", resp.Model)
		}
		if len(resp.Content) == 0 {
			t.Error("expected response to have content")
		}
	})

	t.Run("request with tools", func(t *testing.T) {
		req := &llm.Request{
			Messages: []llm.Message{
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "What's the weather in Paris?"},
					},
				},
			},
			Tools: []*llm.Tool{
				{
					Name:        "get_weather",
					Description: "Get weather for a location",
					InputSchema: llm.MustSchema(`{
						"type": "object",
						"properties": {
							"location": {"type": "string"}
						},
						"required": ["location"]
					}`),
				},
			},
		}

		resp, err := svc.Do(ctx, req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}

		if resp.StopReason != llm.StopReasonToolUse {
			t.Errorf("expected tool use stop reason, got %v", resp.StopReason)
		}

		// Find the tool use content
		var foundToolUse bool
		for _, c := range resp.Content {
			if c.Type == llm.ContentTypeToolUse {
				foundToolUse = true
				if c.ToolName != "get_weather" {
					t.Errorf("expected tool name get_weather, got %s", c.ToolName)
				}
			}
		}
		if !foundToolUse {
			t.Error("expected to find tool use in response")
		}
	})
}

func TestResponsesInstructionsFromLLMSystemAllEmpty(t *testing.T) {
	instructions := responsesInstructionsFromLLMSystem([]llm.SystemContent{
		{Text: ""},
		{Text: ""},
		{Text: ""},
	})
	if instructions != "" {
		t.Errorf("responsesInstructionsFromLLMSystem(all empty) = %q, expected empty", instructions)
	}
}

func TestResponsesServiceDoSendsSystemAsInstructions(t *testing.T) {
	var gotReq responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{
			ID:     "responses-test123",
			Status: "completed",
			Model:  "test-model",
			Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "ok"}}}},
			Usage:  responsesUsage{InputTokens: 10, OutputTokens: 20},
		})
	}))
	defer server.Close()

	svc := &ResponsesService{
		APIKey:   "test-api-key",
		Model:    GPT41,
		ModelURL: server.URL,
	}

	_, err := svc.Do(t.Context(), &llm.Request{
		System: []llm.SystemContent{
			{Text: "You are a helpful assistant"},
			{Text: "Be concise"},
		},
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Hello!"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}

	if gotReq.Instructions != "You are a helpful assistant\nBe concise" {
		t.Fatalf("instructions = %q", gotReq.Instructions)
	}
	if len(gotReq.Input) != 1 {
		t.Fatalf("input = %+v, want only conversation message", gotReq.Input)
	}
	input := gotReq.Input[0]
	if input.Type != "message" || input.Role != "user" || len(input.Content) != 1 {
		t.Fatalf("input[0] = %+v, want user message", input)
	}
	if input.Content[0].Type != "input_text" || input.Content[0].Text != "Hello!" {
		t.Fatalf("input[0].content[0] = %+v, want user text", input.Content[0])
	}
}

func TestResponsesServiceDoSendsDefaultMaxOutputTokens(t *testing.T) {
	var gotReq map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{
			ID:     "responses-test",
			Status: "completed",
			Model:  "test-model",
			Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "ok"}}}},
			Usage:  responsesUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		})
	}))
	defer server.Close()

	svc := &ResponsesService{
		APIKey:   "test-api-key",
		Model:    modelForTest("test-model"),
		ModelURL: server.URL,
	}

	_, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}},
		}},
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if got, ok := gotReq["max_output_tokens"].(float64); !ok || got != DefaultMaxTokens {
		t.Fatalf("max_output_tokens = %#v, want %d", gotReq["max_output_tokens"], DefaultMaxTokens)
	}
}

func TestResponsesServiceDo(t *testing.T) {
	// Create a mock Responses server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("Expected path /responses, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Errorf("Expected Authorization header, got %s", r.Header.Get("Authorization"))
		}

		// Send a mock response
		response := responsesResponse{
			ID:    "responses-test123",
			Model: "test-model",
			Output: []responsesOutputItem{
				{
					Type: "message",
					Role: "assistant",
					Content: []responsesContent{
						{
							Type: "text",
							Text: "Hello! How can I help you today?",
						},
					},
				},
			},
			Usage: responsesUsage{
				InputTokens:  10,
				OutputTokens: 20,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create a service with the mock server
	ctx := t.Context()
	svc := &ResponsesService{
		APIKey:   "test-api-key",
		Model:    GPT41,
		ModelURL: server.URL,
	}

	// Create a test request
	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Hello!"},
				},
			},
		},
	}

	// Call the Do method
	resp, err := svc.Do(ctx, req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}

	// Verify the response
	if resp == nil {
		t.Fatal("Do() returned nil response")
	}
	if resp.Role != llm.MessageRoleAssistant {
		t.Errorf("resp.Role = %v, expected %v", resp.Role, llm.MessageRoleAssistant)
	}
	if len(resp.Content) != 1 {
		t.Errorf("resp.Content length = %d, expected 1", len(resp.Content))
	} else {
		content := resp.Content[0]
		if content.Type != llm.ContentTypeText {
			t.Errorf("content.Type = %v, expected %v", content.Type, llm.ContentTypeText)
		}
		if content.Text != "Hello! How can I help you today?" {
			t.Errorf("content.Text = %q, expected %q", content.Text, "Hello! How can I help you today?")
		}
	}
	if resp.Usage.InputTokens != 10 {
		t.Errorf("resp.Usage.InputTokens = %d, expected 10", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 20 {
		t.Errorf("resp.Usage.OutputTokens = %d, expected 20", resp.Usage.OutputTokens)
	}
	// No cache details, so CacheCreation and CacheRead should be 0
	if resp.Usage.CacheCreationInputTokens != 0 {
		t.Errorf("resp.Usage.CacheCreationInputTokens = %d, expected 0", resp.Usage.CacheCreationInputTokens)
	}
	if resp.Usage.CacheReadInputTokens != 0 {
		t.Errorf("resp.Usage.CacheReadInputTokens = %d, expected 0", resp.Usage.CacheReadInputTokens)
	}
	// TotalInputTokens should equal InputTokens when no caching
	if resp.Usage.TotalInputTokens() != 10 {
		t.Errorf("resp.Usage.TotalInputTokens() = %d, expected 10", resp.Usage.TotalInputTokens())
	}
	// ContextWindowUsed = TotalInput + Output = 10 + 20 = 30
	if resp.Usage.ContextWindowUsed() != 30 {
		t.Errorf("resp.Usage.ContextWindowUsed() = %d, expected 30", resp.Usage.ContextWindowUsed())
	}
	requireResponseTiming(t, resp)
}

func TestResponsesServiceDoConsumesPlainTextStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req responsesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		if !req.Stream {
			http.Error(w, "stream is required", http.StatusBadRequest)
			return
		}

		response := responsesResponse{
			ID:     "responses-stream-test",
			Model:  "test-model",
			Status: "completed",
			Usage: responsesUsage{
				InputTokens:  10,
				OutputTokens: 20,
			},
		}
		messageItem := responsesOutputItem{
			Type: "message",
			Role: "assistant",
			Content: []responsesContent{
				{Type: "output_text", Text: "streamed response"},
			},
		}
		outputDone, err := json.Marshal(struct {
			Type        string              `json:"type"`
			OutputIndex int                 `json:"output_index"`
			Item        responsesOutputItem `json:"item"`
		}{
			Type:        "response.output_item.done",
			OutputIndex: 1,
			Item:        messageItem,
		})
		if err != nil {
			t.Fatalf("marshal output item event: %v", err)
		}
		completed, err := json.Marshal(struct {
			Type     string            `json:"type"`
			Response responsesResponse `json:"response"`
		}{
			Type:     "response.completed",
			Response: response,
		})
		if err != nil {
			t.Fatalf("marshal completed event: %v", err)
		}
		reasoningDone, err := json.Marshal(struct {
			Type        string              `json:"type"`
			OutputIndex int                 `json:"output_index"`
			Item        responsesOutputItem `json:"item"`
		}{
			Type:        "response.output_item.done",
			OutputIndex: 0,
			Item: responsesOutputItem{
				ID:      "reasoning-1",
				Type:    "reasoning",
				Summary: nil,
			},
		})
		if err != nil {
			t.Fatalf("marshal reasoning event: %v", err)
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "event: response.output_item.done\ndata: %s\n\n", reasoningDone)
		fmt.Fprint(w, "event: response.output_text.delta\n")
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","delta":"streamed ","content_index":0}`)
		fmt.Fprint(w, "\n\n")
		fmt.Fprint(w, "event: response.output_text.delta\n")
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","delta":"response","content_index":0}`)
		fmt.Fprint(w, "\n\n")
		fmt.Fprintf(w, "event: response.output_item.done\ndata: %s\n\n", outputDone)
		fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", completed)
	}))
	defer server.Close()

	var streamed strings.Builder
	svc := &ResponsesService{
		APIKey:   "test-api-key",
		Model:    GPT41,
		ModelURL: server.URL,
	}
	resp, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello!"}}}},
		OnStream: func(delta llm.StreamDelta) {
			streamed.WriteString(delta.Text)
		},
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if got := resp.Content[0].Text; got != "streamed response" {
		t.Fatalf("response text = %q, want streamed response", got)
	}
	if got := streamed.String(); got != "streamed response" {
		t.Fatalf("streamed text = %q, want streamed response", got)
	}
}

func TestResponsesServiceRetriesPlainTextServerError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			http.Error(w, "gateway proxy: upstream request failed (trace: abc123)", http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{
			ID:     "retry-ok",
			Status: "completed",
			Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "ok"}}}},
			Usage:  responsesUsage{InputTokens: 1, OutputTokens: 1},
		})
	}))
	defer server.Close()

	var retries []llm.RetryEvent
	svc := &ResponsesService{
		APIKey:   "test-api-key",
		Model:    GPT41,
		ModelURL: server.URL,
		Backoff:  []time.Duration{0},
	}
	resp, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
		OnRetry:  func(event llm.RetryEvent) { retries = append(retries, event) },
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if len(retries) != 2 {
		t.Fatalf("retry events = %d, want 2", len(retries))
	}
	if got := retries[0].Err; !strings.Contains(got, "trace: abc123") {
		t.Fatalf("first retry error = %q, want gateway trace", got)
	}
	if got := resp.Content[0].Text; got != "ok" {
		t.Fatalf("response text = %q, want ok", got)
	}
}

func TestResponsesServicePrefersStructuredServerErrorMessage(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"message":"structured boom"}}`)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{
			ID:     "retry-ok",
			Status: "completed",
			Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "ok"}}}},
			Usage:  responsesUsage{InputTokens: 1, OutputTokens: 1},
		})
	}))
	defer server.Close()

	var retry llm.RetryEvent
	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	_, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
		OnRetry:  func(event llm.RetryEvent) { retry = event },
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if !strings.Contains(retry.Err, "structured boom") || strings.Contains(retry.Err, `{"error"`) {
		t.Fatalf("retry error = %q, want structured message only", retry.Err)
	}
}

func TestResponsesServiceUsesFirstBackoffForFirstRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary failure", http.StatusBadGateway)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var retry llm.RetryEvent
	svc := &ResponsesService{
		APIKey:   "test-api-key",
		Model:    GPT41,
		ModelURL: server.URL,
		Backoff:  []time.Duration{0, time.Hour},
	}
	_, err := svc.Do(ctx, &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
		OnRetry: func(event llm.RetryEvent) {
			retry = event
			cancel()
		},
	})
	if err == nil {
		t.Fatal("Do() error = nil, want cancellation")
	}
	if retry.Sleep != 0 {
		t.Fatalf("first retry sleep = %v, want Backoff[0] (0)", retry.Sleep)
	}
}

func TestResponsesServiceRetriesPlainTextRateLimit(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.Error(w, "gateway rate limited", http.StatusTooManyRequests)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{
			ID:     "retry-ok",
			Status: "completed",
			Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "ok"}}}},
			Usage:  responsesUsage{InputTokens: 1, OutputTokens: 1},
		})
	}))
	defer server.Close()

	var retries []llm.RetryEvent
	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	_, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
		OnRetry:  func(event llm.RetryEvent) { retries = append(retries, event) },
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(retries) != 1 || !strings.Contains(retries[0].Err, "rate limited") {
		t.Fatalf("retry events = %#v, want one rate-limit event", retries)
	}
}

func TestResponsesServiceBoundsRetriedErrorBodies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(strings.Repeat("x", 64<<10)))
	}))
	defer server.Close()

	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	_, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("Do() error = nil, want exhausted retries")
	}
	if len(err.Error()) > 20<<10 {
		t.Fatalf("error length = %d, want bounded retry history", len(err.Error()))
	}
}

type responsesRequestErrorForTest struct {
	info llm.RequestErrorInfo
}

func (e *responsesRequestErrorForTest) Error() string { return "request failed" }
func (e *responsesRequestErrorForTest) RequestErrorInfo() llm.RequestErrorInfo {
	return e.info
}

type responsesRoundTripFunc func(*http.Request) (*http.Response, error)

func (f responsesRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestResponsesServiceExhaustionUsesLatestRequestMetadata(t *testing.T) {
	for _, tt := range []struct {
		name               string
		latestAttemptStall bool
		wantStallDuration  time.Duration
	}{
		{name: "earlier stall does not leak", wantStallDuration: 0},
		{name: "latest stall is preserved", latestAttemptStall: true, wantStallDuration: 3 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			attempts := 0
			httpc := &http.Client{Transport: responsesRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				stall := attempts == 1
				if tt.latestAttemptStall {
					stall = attempts == 16
				}
				if stall {
					return nil, &responsesRequestErrorForTest{info: llm.RequestErrorInfo{
						Retryable:         true,
						IdleStallDuration: 3 * time.Minute,
					}}
				}
				stream := "event: response.failed\n" +
					`data: {"type":"response.failed","response":{"status":"failed","error":{"message":"temporary server failure","type":"server_error","code":"server_error"}}}` + "\n\n"
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(stream)),
					Request:    req,
				}, nil
			})}

			svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: "https://example.test", HTTPC: httpc, Backoff: []time.Duration{0}}
			_, err := svc.Do(t.Context(), &llm.Request{
				Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
			})
			if err == nil {
				t.Fatal("Do() error = nil, want exhausted retries")
			}
			info, ok := llm.RequestErrorInfoFromError(err)
			if !ok || !info.Retryable || !info.NoImmediateRetry || info.IdleStallDuration != tt.wantStallDuration {
				t.Fatalf("error metadata = %+v, %v; want stall %v", info, ok, tt.wantStallDuration)
			}
		})
	}
}

func TestResponsesErrorRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  *responsesError
		want bool
	}{
		{name: "nil", err: nil, want: true},
		{name: "unclassified", err: &responsesError{Message: "provider-specific failure"}, want: true},
		{name: "unsupported images override server code", err: &responsesError{Message: "This model does not support image inputs", Type: "server_error", Code: json.RawMessage(`"server_error"`)}, want: false},
		{name: "unsupported value", err: &responsesError{Code: json.RawMessage(`"unsupported_value"`)}, want: false},
		{name: "invalid prompt", err: &responsesError{Code: json.RawMessage(`"invalid_prompt"`)}, want: false},
		{name: "misalignment policy violation", err: &responsesError{Code: json.RawMessage(`"misalignment_policy_violation"`)}, want: false},
		{name: "invalid image", err: &responsesError{Code: json.RawMessage(`"invalid_image"`)}, want: false},
		{name: "invalid request type", err: &responsesError{Type: "invalid_request_error"}, want: false},
		{name: "vector store timeout", err: &responsesError{Code: json.RawMessage(`"vector_store_timeout"`)}, want: true},
		{name: "numeric rate limit", err: &responsesError{Code: json.RawMessage(`429`)}, want: true},
		{name: "numeric server error", err: &responsesError{Code: json.RawMessage(`503`)}, want: true},
		{name: "numeric client error", err: &responsesError{Code: json.RawMessage(`400`)}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := responsesErrorRetryable(tt.err); got != tt.want {
				t.Fatalf("responsesErrorRetryable(%+v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestParseResponsesSSEEmptyTerminalErrorMessage(t *testing.T) {
	stream := strings.Join([]string{
		`event: error`,
		`data: {"type":"error","error":{"message":"","type":"invalid_request_error","code":"unsupported_value"}}`,
		``,
	}, "\n")
	_, err := parseResponsesSSEStream(strings.NewReader(stream), nil)
	if err == nil {
		t.Fatal("parseResponsesSSEStream() error = nil, want terminal error")
	}
	info, ok := llm.RequestErrorInfoFromError(err)
	if !ok || info.Retryable {
		t.Fatalf("error metadata = %+v, %v; want non-retryable", info, ok)
	}
}

func TestResponsesServiceDoesNotRetryUnclassifiedJSONError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{
			Status: "failed",
			Error:  &responsesError{Message: "response was filtered by content policy"},
		})
	}))
	defer server.Close()

	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	_, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
	})
	if err == nil || !strings.Contains(err.Error(), "filtered by content policy") {
		t.Fatalf("Do() error = %v, want content policy error", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	info, ok := llm.RequestErrorInfoFromError(err)
	if !ok || info.Retryable {
		t.Fatalf("error metadata = %+v, %v; want non-retryable", info, ok)
	}
}

func TestResponsesServiceJSONServerErrorIsManualRetryOnly(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{
			Status: "failed",
			Error: &responsesError{
				Message: "The server had an error processing your request",
				Type:    "server_error",
				Code:    json.RawMessage(`"server_error"`),
			},
		})
	}))
	defer server.Close()

	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	_, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("Do() error = nil, want server error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	info, ok := llm.RequestErrorInfoFromError(err)
	if !ok || !info.Retryable || !info.NoImmediateRetry {
		t.Fatalf("error metadata = %+v, %v; want manual retry only", info, ok)
	}
}

func TestResponsesServiceMarksExhaustedRetries(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.failed\n")
		fmt.Fprint(w, `data: {"type":"response.failed","response":{"status":"failed","error":{"message":"temporary server failure","type":"server_error","code":"server_error"}}}`)
		fmt.Fprint(w, "\n\n")
	}))
	defer server.Close()

	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	_, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("Do() error = nil, want exhausted retries")
	}
	if attempts != 16 {
		t.Fatalf("attempts = %d, want 16", attempts)
	}
	info, ok := llm.RequestErrorInfoFromError(err)
	if !ok || !info.Retryable || !info.NoImmediateRetry {
		t.Fatalf("error metadata = %+v, %v; want manual retry only", info, ok)
	}
}

func TestResponsesServiceDoesNotRetryFailedStreamResponse(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.failed\n")
		fmt.Fprint(w, `data: {"type":"response.failed","response":{"status":"failed","error":{"message":"This model does not support image inputs","type":"server_error","code":"server_error"}}}`)
		fmt.Fprint(w, "\n\n")
	}))
	defer server.Close()

	var retries []llm.RetryEvent
	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	_, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
		OnRetry:  func(event llm.RetryEvent) { retries = append(retries, event) },
	})
	if err == nil || !strings.Contains(err.Error(), "This model does not support image inputs") {
		t.Fatalf("Do() error = %v, want image support error", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if len(retries) != 0 {
		t.Fatalf("retry events = %#v, want none", retries)
	}
	info, ok := llm.RequestErrorInfoFromError(err)
	if !ok || info.Retryable {
		t.Fatalf("error metadata = %+v, %v; want non-retryable", info, ok)
	}
}

func TestResponsesServiceRetriesTransientFailedStreamResponse(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: response.failed\n")
			fmt.Fprint(w, `data: {"type":"response.failed","response":{"status":"failed","error":{"message":"The service is temporarily overloaded","type":"server_error","code":"server_error"}}}`)
			fmt.Fprint(w, "\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{
			ID:     "retry-ok",
			Status: "completed",
			Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "ok"}}}},
			Usage:  responsesUsage{InputTokens: 1, OutputTokens: 1},
		})
	}))
	defer server.Close()

	var retries []llm.RetryEvent
	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	resp, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
		OnRetry:  func(event llm.RetryEvent) { retries = append(retries, event) },
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(retries) != 1 || !strings.Contains(retries[0].Err, "temporarily overloaded") {
		t.Fatalf("retry events = %#v, want transient stream failure", retries)
	}
	if got := resp.Content[0].Text; got != "ok" {
		t.Fatalf("response text = %q, want ok", got)
	}
}

func TestResponsesServiceRetriesTopLevelTransientStreamError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: error\n")
			fmt.Fprint(w, `data: {"type":"error","code":"server_error","message":"The service is temporarily unavailable"}`)
			fmt.Fprint(w, "\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{
			ID:     "retry-ok",
			Status: "completed",
			Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "ok"}}}},
			Usage:  responsesUsage{InputTokens: 1, OutputTokens: 1},
		})
	}))
	defer server.Close()

	var retries []llm.RetryEvent
	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	_, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
		OnRetry:  func(event llm.RetryEvent) { retries = append(retries, event) },
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if attempts != 2 || len(retries) != 1 {
		t.Fatalf("attempts = %d, retries = %#v; want 2 attempts and 1 retry", attempts, retries)
	}
}

func TestResponsesServiceRetriesUnclassifiedStreamError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: error\n")
			fmt.Fprint(w, `data: {"type":"error","message":"upstream connect error or disconnect/reset before headers"}`)
			fmt.Fprint(w, "\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{
			ID:     "retry-ok",
			Status: "completed",
			Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "ok"}}}},
			Usage:  responsesUsage{InputTokens: 1, OutputTokens: 1},
		})
	}))
	defer server.Close()

	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	_, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestResponsesServiceTerminalErrorOverridesEarlierRetryMetadata(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: response.failed\n")
			fmt.Fprint(w, `data: {"type":"response.failed","response":{"status":"failed","error":{"message":"The service is temporarily overloaded","type":"server_error","code":"server_error"}}}`)
			fmt.Fprint(w, "\n\n")
			return
		}
		http.Error(w, "invalid_request_error: unsupported model", http.StatusBadRequest)
	}))
	defer server.Close()

	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	_, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("Do() error = nil, want terminal client error")
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if !strings.Contains(err.Error(), "temporarily overloaded") {
		t.Fatalf("error = %v, want earlier retry context", err)
	}
	info, ok := llm.RequestErrorInfoFromError(err)
	if !ok || info.Retryable {
		t.Fatalf("error metadata = %+v, %v; want non-retryable", info, ok)
	}
}

func TestResponsesServiceDoesNotRetryPlainTextClientError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "integration not found or not attached to this VM", http.StatusForbidden)
	}))
	defer server.Close()

	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL, Backoff: []time.Duration{0}}
	_, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("Do() error = nil, want client error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if !strings.Contains(err.Error(), "integration not found") {
		t.Fatalf("error = %q, want raw gateway message", err)
	}
}

func TestResponsesServiceBoundsClientErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("useful prefix: " + strings.Repeat("x", 64<<10)))
	}))
	defer server.Close()

	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL}
	_, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("Do() error = nil, want client error")
	}
	if !strings.Contains(err.Error(), "useful prefix") {
		t.Fatalf("error = %q, want useful prefix", err)
	}
	if len(err.Error()) > 8<<10 {
		t.Fatalf("error length = %d, want bounded client error", len(err.Error()))
	}
}

func TestResponsesServiceRetriesEmptyJSONResponse(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			w.WriteHeader(http.StatusOK)
			return
		}

		json.NewEncoder(w).Encode(responsesResponse{
			ID:     "retry-ok",
			Status: "completed",
			Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "ok"}}}},
			Usage:  responsesUsage{InputTokens: 1, OutputTokens: 1},
		})
	}))
	defer server.Close()

	svc := &ResponsesService{APIKey: "test-api-key", Model: GPT41, ModelURL: server.URL}
	resp, err := svc.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if got := resp.Content[0].Text; got != "ok" {
		t.Fatalf("response text = %q, want ok", got)
	}
}

func TestShouldRetryResponsesDecodeError(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want bool
	}{
		{name: "empty", body: nil, want: true},
		{name: "whitespace", body: []byte(" \n\t"), want: true},
		{name: "truncated object", body: []byte(`{"id":"r"`), want: true},
		{name: "truncated string", body: []byte(`{"id":"r`), want: true},
		{name: "bad complete json", body: []byte(`{"id":}`), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp responsesResponse
			err := json.Unmarshal(tt.body, &resp)
			if err == nil {
				t.Fatal("json.Unmarshal succeeded, want error")
			}
			if got := shouldRetryResponsesDecodeError(err, tt.body); got != tt.want {
				t.Fatalf("shouldRetryResponsesDecodeError() = %v, want %v (err=%v)", got, tt.want, err)
			}
		})
	}
}

// TestToLLMUsageFromResponses pins the split of OpenAI's input_tokens total
// into Shelley's Anthropic-style fields. Numbers are from real gpt-5.6-sol and
// gpt-6-astra calls: cached_tokens and cache_write_tokens are both subsets of
// input_tokens, and GPT-5.6+ bills writes at 1.25x input.
func TestToLLMUsageFromResponses(t *testing.T) {
	svc := &ResponsesService{}
	tests := []struct {
		name                        string
		details                     openAIInputTokensDetails
		wantIn, wantWrite, wantRead uint64
	}{
		{"pre-5.6: reads only", openAIInputTokensDetails{CachedTokens: 2816}, 945, 0, 2816},
		{"5.6+ cold call: nearly all writes", openAIInputTokensDetails{CacheWriteTokens: 3758}, 3, 3758, 0},
		{"5.6+ follow-up: mostly reads", openAIInputTokensDetails{CachedTokens: 3751, CacheWriteTokens: 7}, 3, 7, 3751},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := svc.toLLMUsageFromResponses(responsesUsage{InputTokens: 3761, InputTokensDetails: tt.details, OutputTokens: 6}, nil)
			if got.InputTokens != tt.wantIn || got.CacheCreationInputTokens != tt.wantWrite || got.CacheReadInputTokens != tt.wantRead || got.OutputTokens != 6 {
				t.Errorf("usage = %+v, want in=%d write=%d read=%d out=6", got, tt.wantIn, tt.wantWrite, tt.wantRead)
			}
		})
	}
}

func TestResponsesServiceDoWithCaching(t *testing.T) {
	// Test that cached tokens are correctly mapped to Usage fields
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := responsesResponse{
			ID:    "responses-cache-test",
			Model: "test-model",
			Output: []responsesOutputItem{
				{
					Type: "message",
					Role: "assistant",
					Content: []responsesContent{
						{Type: "text", Text: "cached response"},
					},
				},
			},
			Usage: responsesUsage{
				InputTokens: 100,
				InputTokensDetails: openAIInputTokensDetails{
					CachedTokens:     80,
					CacheWriteTokens: 15,
				},
				OutputTokens: 50,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	ctx := t.Context()
	svc := &ResponsesService{
		APIKey:   "test-api-key",
		Model:    GPT41,
		ModelURL: server.URL,
	}

	resp, err := svc.Do(ctx, &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "test"}},
		}},
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}

	// InputTokens should exclude cache reads and writes: 100 - 80 - 15 = 5
	if resp.Usage.InputTokens != 5 {
		t.Errorf("resp.Usage.InputTokens = %d, expected 5 (non-cached portion)", resp.Usage.InputTokens)
	}
	// CacheReadInputTokens should be the cached amount
	if resp.Usage.CacheReadInputTokens != 80 {
		t.Errorf("resp.Usage.CacheReadInputTokens = %d, expected 80", resp.Usage.CacheReadInputTokens)
	}
	// CacheCreationInputTokens should be the cache-write amount
	if resp.Usage.CacheCreationInputTokens != 15 {
		t.Errorf("resp.Usage.CacheCreationInputTokens = %d, expected 15", resp.Usage.CacheCreationInputTokens)
	}
	if resp.Usage.OutputTokens != 50 {
		t.Errorf("resp.Usage.OutputTokens = %d, expected 50", resp.Usage.OutputTokens)
	}
	// TotalInputTokens = 5 + 15 + 80 = 100 (matches OpenAI's input_tokens)
	if resp.Usage.TotalInputTokens() != 100 {
		t.Errorf("resp.Usage.TotalInputTokens() = %d, expected 100", resp.Usage.TotalInputTokens())
	}
	// ContextWindowUsed = 100 + 50 = 150
	if resp.Usage.ContextWindowUsed() != 150 {
		t.Errorf("resp.Usage.ContextWindowUsed() = %d, expected 150", resp.Usage.ContextWindowUsed())
	}
}

func TestResponsesServiceReasoningEffort(t *testing.T) {
	tests := []struct {
		name            string
		thinkingLevel   llm.ThinkingLevel
		reasoningEffort string
		wantEffort      string // "" means reasoning field should be absent
	}{
		{name: "thinking off, no override", thinkingLevel: llm.ThinkingLevelOff, reasoningEffort: "", wantEffort: ""},
		{name: "thinking medium maps to medium", thinkingLevel: llm.ThinkingLevelMedium, reasoningEffort: "", wantEffort: "medium"},
		{name: "thinking high maps to high", thinkingLevel: llm.ThinkingLevelHigh, reasoningEffort: "", wantEffort: "high"},
		{name: "override beats thinking level", thinkingLevel: llm.ThinkingLevelMedium, reasoningEffort: "xhigh", wantEffort: "xhigh"},
		{name: "override none disables reasoning", thinkingLevel: llm.ThinkingLevelMedium, reasoningEffort: "none", wantEffort: "none"},
		{name: "override when thinking off", thinkingLevel: llm.ThinkingLevelOff, reasoningEffort: "high", wantEffort: "high"},
		{name: "xhigh maps to xhigh", thinkingLevel: llm.ThinkingLevelXHigh, reasoningEffort: "", wantEffort: "xhigh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReasoning *responsesReasoning
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req responsesRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatalf("decode req: %v", err)
				}
				gotReasoning = req.Reasoning
				resp := responsesResponse{
					ID:     "r",
					Status: "completed",
					Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "ok"}}}},
					Usage:  responsesUsage{InputTokens: 1, OutputTokens: 1},
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			svc := &ResponsesService{
				APIKey:          "k",
				Model:           GPT41,
				ModelURL:        server.URL,
				ThinkingLevel:   tt.thinkingLevel,
				ReasoningEffort: tt.reasoningEffort,
			}
			_, err := svc.Do(t.Context(), &llm.Request{
				Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if tt.wantEffort == "" {
				if gotReasoning != nil {
					t.Fatalf("expected no reasoning, got %+v", gotReasoning)
				}
				return
			}
			if gotReasoning == nil {
				t.Fatalf("expected reasoning.effort=%q, got nil", tt.wantEffort)
			}
			if gotReasoning.Effort != tt.wantEffort {
				t.Errorf("effort = %q, want %q", gotReasoning.Effort, tt.wantEffort)
			}
			// The OpenAI provider always requests reasoning summaries;
			// without summary: "auto" the API omits summary text and the UI
			// never shows thinking.
			if gotReasoning.Summary != "auto" {
				t.Errorf("summary = %q, want %q", gotReasoning.Summary, "auto")
			}
		})
	}
}

// TestResponsesServiceRequestLevelThinking verifies that a non-default
// Request.ThinkingLevel overrides both the service ThinkingLevel and the
// service ReasoningEffort verbatim string.
func TestResponsesServiceRequestLevelThinking(t *testing.T) {
	tests := []struct {
		name       string
		svcLevel   llm.ThinkingLevel
		svcEffort  string
		reqLevel   llm.ThinkingLevel
		wantEffort string
	}{
		{name: "req overrides svc default", svcLevel: llm.ThinkingLevelMedium, reqLevel: llm.ThinkingLevelHigh, wantEffort: "high"},
		{name: "req off beats svc medium", svcLevel: llm.ThinkingLevelMedium, reqLevel: llm.ThinkingLevelOff, wantEffort: ""},
		{name: "req off beats svc verbatim", svcLevel: llm.ThinkingLevelMedium, svcEffort: "xhigh", reqLevel: llm.ThinkingLevelOff, wantEffort: ""},
		{name: "req default falls back to svc verbatim", svcLevel: llm.ThinkingLevelMedium, svcEffort: "xhigh", reqLevel: llm.ThinkingLevelDefault, wantEffort: "xhigh"},
		{name: "req xhigh beats svc verbatim", svcLevel: llm.ThinkingLevelMedium, svcEffort: "verbatim", reqLevel: llm.ThinkingLevelXHigh, wantEffort: "xhigh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReasoning *responsesReasoning
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req responsesRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatalf("decode req: %v", err)
				}
				gotReasoning = req.Reasoning
				resp := responsesResponse{
					ID:     "r",
					Status: "completed",
					Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "ok"}}}},
					Usage:  responsesUsage{InputTokens: 1, OutputTokens: 1},
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			svc := &ResponsesService{
				APIKey:          "k",
				Model:           GPT41,
				ModelURL:        server.URL,
				ThinkingLevel:   tt.svcLevel,
				ReasoningEffort: tt.svcEffort,
			}
			_, err := svc.Do(t.Context(), &llm.Request{
				Messages:      []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
				ThinkingLevel: tt.reqLevel,
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if tt.wantEffort == "" {
				if gotReasoning != nil {
					t.Fatalf("expected no reasoning, got %+v", gotReasoning)
				}
				return
			}
			if gotReasoning == nil {
				t.Fatalf("expected reasoning.effort=%q, got nil", tt.wantEffort)
			}
			if gotReasoning.Effort != tt.wantEffort {
				t.Errorf("effort = %q, want %q", gotReasoning.Effort, tt.wantEffort)
			}
		})
	}
}

// TestResponsesServiceStallTimeout verifies that when the upstream stream
// stalls mid-response (headers + a delta sent, then silence), the idle-timeout
// client aborts the request with retryable idle-stall metadata rather than
// hanging or capping on a fixed total deadline.
func TestResponsesServiceStallTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		fmt.Fprint(w, "event: response.output_text.delta\n")
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","delta":"partial","content_index":0}`)
		fmt.Fprint(w, "\n\n")
		if f != nil {
			f.Flush()
		}
		// Stall: never send response.completed.
		<-release
	}))
	defer server.Close()
	defer close(release)

	svc := &ResponsesService{
		APIKey:   "test-api-key",
		Model:    GPT41,
		ModelURL: server.URL,
		HTTPC:    llmhttp.NewClientWithIdleTimeout(nil, 150*time.Millisecond),
	}

	// Bound the test so a regression (no idle timeout) fails fast instead of
	// hanging the suite. The responses client retries stream failures with
	// backoff, so this deadline also caps how long those retries run.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	_, err := svc.Do(ctx, &llm.Request{
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello!"}}}},
	})
	if err == nil {
		t.Fatalf("expected stall error, got nil")
	}
	info, ok := llm.RequestErrorInfoFromError(err)
	if !ok || info.IdleStallDuration != 150*time.Millisecond || !info.Retryable {
		t.Fatalf("error metadata = %+v, %v; want retryable 150ms idle stall (error: %v)", info, ok, err)
	}
	if wantURL := "url=" + server.URL + "/responses"; !strings.Contains(err.Error(), wantURL) {
		t.Fatalf("error = %v, want request URL diagnostic %q", err, wantURL)
	}
}

func TestResponsesServicePatchProfile(t *testing.T) {
	for _, model := range []Model{GPT6Astra, GPT6Sol, GPT6Luna} {
		if got := (&ResponsesService{ProviderName: "openai", Model: model}).PatchProfile(); got != "codex_apply_patch" {
			t.Fatalf("%s OpenAI Responses profile = %q", model.ModelName, got)
		}
	}
	if got := (&ResponsesService{ProviderName: "openai"}).PatchProfile(); got != "flat" {
		t.Fatalf("uncatalogued OpenAI Responses profile = %q", got)
	}
	if got := (&ResponsesService{ProviderName: "xai", Model: Model{SupportsApplyPatch: true}}).PatchProfile(); got != "flat" {
		t.Fatalf("non-OpenAI Responses profile = %q", got)
	}
}

func TestResponsesCustomGrammarToolWireFormat(t *testing.T) {
	tool := fromLLMToolResponses(&llm.Tool{Name: "apply_patch", Description: "patch", CustomGrammar: "start: /.+/s"})
	got, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"custom","name":"apply_patch","description":"patch","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/s"}}`
	if string(got) != want {
		t.Fatalf("custom tool = %s, want %s", got, want)
	}
}

func TestResponsesCustomToolCallConversion(t *testing.T) {
	service := &ResponsesService{}
	response := service.toLLMResponseFromResponses(&responsesResponse{Output: []responsesOutputItem{{Type: "custom_tool_call", CallID: "call_1", Name: "apply_patch", Input: "*** Begin Patch\n*** End Patch"}}}, nil)
	if len(response.Content) != 1 || response.Content[0].ToolName != "apply_patch" {
		t.Fatalf("content = %+v", response.Content)
	}
	if got := string(response.Content[0].ToolInput); got != `{"input":"*** Begin Patch\n*** End Patch"}` {
		t.Fatalf("tool input = %s", got)
	}

	items := fromLLMMessageResponses(llm.Message{Role: llm.MessageRoleAssistant, Content: response.Content}, responsesReasoningReplayEncrypted)
	if len(items) != 1 || items[0].Type != "custom_tool_call" || items[0].Input == "" {
		t.Fatalf("replayed items = %+v", items)
	}
}

func TestResponsesCustomToolResultUsesCustomOutput(t *testing.T) {
	items := fromLLMMessageResponses(llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeToolResult, ToolName: "apply_patch", ToolUseID: "call_1", ToolResult: llm.TextContent("done")}}}, responsesReasoningReplayEncrypted)
	if len(items) != 1 || items[0].Type != "custom_tool_call_output" {
		t.Fatalf("items = %+v", items)
	}
}

func TestParseResponsesSSECustomToolCall(t *testing.T) {
	stream := strings.Join([]string{
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"*** Begin Patch\n*** End Patch"}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		``,
	}, "\n")
	response, err := parseResponsesSSEStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 1 || response.Output[0].Type != "custom_tool_call" || response.Output[0].Input == "" {
		t.Fatalf("output = %+v", response.Output)
	}
}

func TestParseResponsesSSEErrorAcceptsNumericCode(t *testing.T) {
	stream := strings.Join([]string{
		`event: error`,
		`data: {"type":"error","error":{"message":"overloaded","type":"server_error","code":500}}`,
		``,
	}, "\n")
	_, err := parseResponsesSSEStream(strings.NewReader(stream), nil)
	if err == nil || !strings.Contains(err.Error(), "stream error event: overloaded") {
		t.Fatalf("error = %v, want parsed stream error", err)
	}
}

func TestResponsesResponseCreatedAtFractionalJSON(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want float64
	}{
		{name: "integer", raw: `{"id":"resp_1","created_at":1788698982}`, want: 1788698982},
		{name: "zero fraction", raw: `{"id":"resp_1","created_at":1788698982.0}`, want: 1788698982.0},
		{name: "true fraction", raw: `{"id":"resp_1","created_at":1788698982.25}`, want: 1788698982.25},
		{name: "exponent", raw: `{"id":"resp_1","created_at":1.78869898225e9}`, want: 1788698982.25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var response responsesResponse
			if err := json.Unmarshal([]byte(tt.raw), &response); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			if response.CreatedAt != tt.want {
				t.Fatalf("created_at = %v, want %v", response.CreatedAt, tt.want)
			}
		})
	}
}

func TestParseResponsesSSETimestamps(t *testing.T) {
	stream := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_1","created_at":1788698982.0,"status":"in_progress","output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello"}`,
		``,
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","created_at":1788698982.0,"status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
		``,
	}, "\n")

	var deltas []llm.StreamDelta
	response, err := parseResponsesSSEStream(strings.NewReader(stream), func(delta llm.StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.CreatedAt != 1788698982.0 {
		t.Fatalf("created_at = %v, want 1788698982.0", response.CreatedAt)
	}
	if response.Usage.InputTokens != 3 || response.Usage.OutputTokens != 2 || response.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	if len(response.Output) != 1 || response.Output[0].ID != "msg_1" || response.Output[0].Type != "message" {
		t.Fatalf("output = %+v", response.Output)
	}
	if len(deltas) != 1 || deltas[0].Type != "text" || deltas[0].Text != "hello" || deltas[0].Index != 0 {
		t.Fatalf("deltas = %+v", deltas)
	}
}

func TestResponsesServiceDoPreservesStartTimeAcrossRetries(t *testing.T) {
	firstRequest := make(chan time.Time, 1)
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			firstRequest <- time.Now()
			http.Error(w, "temporary failure", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{
			Status: "completed",
			Output: []responsesOutputItem{{
				Type:    "message",
				Role:    "assistant",
				Content: []responsesContent{{Type: "output_text", Text: "ok"}},
			}},
		})
	}))
	defer server.Close()

	resp, err := (&ResponsesService{
		APIKey:   "test-key",
		Model:    GPT41,
		ModelURL: server.URL,
		Backoff:  []time.Duration{0},
	}).Do(t.Context(), &llm.Request{Messages: []llm.Message{{Role: llm.MessageRoleUser}}})
	if err != nil {
		t.Fatal(err)
	}
	requireResponseTiming(t, resp)
	if first := <-firstRequest; resp.StartTime.After(first) {
		t.Fatalf("response StartTime %v is after first request %v", *resp.StartTime, first)
	}
}
