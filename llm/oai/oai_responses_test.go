package oai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"shelley.exe.dev/llm"
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
			items := fromLLMMessageResponses(tt.msg)
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

func TestFromLLMSystemResponses(t *testing.T) {
	tests := []struct {
		name     string
		system   []llm.SystemContent
		expected int
	}{
		{
			name:     "empty system",
			system:   []llm.SystemContent{},
			expected: 0,
		},
		{
			name: "single system message",
			system: []llm.SystemContent{
				{Text: "You are a helpful assistant"},
			},
			expected: 1,
		},
		{
			name: "multiple system messages",
			system: []llm.SystemContent{
				{Text: "You are a helpful assistant"},
				{Text: "Be concise"},
			},
			expected: 1, // should be combined into one message
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := fromLLMSystemResponses(tt.system)
			if len(items) != tt.expected {
				t.Errorf("expected %d items, got %d", len(items), tt.expected)
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
				Model: "gpt-5.1-codex",
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
				Model: "gpt-5.1-codex",
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
				Model: "gpt-5.1-codex",
				Output: []responsesOutputItem{
					{
						Type:    "reasoning",
						Summary: []string{"Let me think", "about this"},
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

func TestResponsesServiceTokenContextWindow(t *testing.T) {
	tests := []struct {
		model    Model
		expected int
	}{
		{model: GPT5Codex, expected: 256000},
		{model: GPT41, expected: 200000},
		{model: GPT4o, expected: 128000},
	}

	for _, tt := range tests {
		t.Run(tt.model.UserName, func(t *testing.T) {
			svc := &ResponsesService{Model: tt.model}
			got := svc.TokenContextWindow()
			if got != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, got)
			}
		})
	}
}

func TestResponsesServiceConfigDetails(t *testing.T) {
	svc := &ResponsesService{
		Model:  GPT5Codex,
		APIKey: "test-key",
	}

	details := svc.ConfigDetails()

	if details["model_name"] != "gpt-5.1-codex" {
		t.Errorf("expected model_name 'gpt-5.1-codex', got %s", details["model_name"])
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
		Model:  GPT5Codex,
	}

	ctx := context.Background()

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
		if resp.Model != "gpt-5.1-codex" {
			t.Errorf("expected model gpt-5.1-codex, got %s", resp.Model)
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

// Test system content with all empty text (should return nil)
func TestFromLLMSystemResponsesAllEmpty(t *testing.T) {
	items := fromLLMSystemResponses([]llm.SystemContent{
		{Text: ""},
		{Text: ""},
		{Text: ""},
	})
	if items != nil {
		t.Errorf("fromLLMSystemResponses(all empty) = %v, expected nil", items)
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
	ctx := context.Background()
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
}
