package oai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"shelley.exe.dev/llm"
)

func TestToRoleFromString(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		expected llm.MessageRole
	}{
		{
			name:     "assistant role",
			role:     "assistant",
			expected: llm.MessageRoleAssistant,
		},
		{
			name:     "user role",
			role:     "user",
			expected: llm.MessageRoleUser,
		},
		{
			name:     "tool role maps to assistant",
			role:     "tool",
			expected: llm.MessageRoleAssistant,
		},
		{
			name:     "system role maps to assistant",
			role:     "system",
			expected: llm.MessageRoleAssistant,
		},
		{
			name:     "function role maps to assistant",
			role:     "function",
			expected: llm.MessageRoleAssistant,
		},
		{
			name:     "unknown role defaults to user",
			role:     "unknown",
			expected: llm.MessageRoleUser,
		},
		{
			name:     "empty role defaults to user",
			role:     "",
			expected: llm.MessageRoleUser,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toRoleFromString(tt.role)
			if result != tt.expected {
				t.Errorf("toRoleFromString(%q) = %v, expected %v", tt.role, result, tt.expected)
			}
		})
	}
}

func TestToStopReason(t *testing.T) {
	tests := []struct {
		name     string
		reason   string
		expected llm.StopReason
	}{
		{
			name:     "stop reason",
			reason:   "stop",
			expected: llm.StopReasonStopSequence,
		},
		{
			name:     "length reason",
			reason:   "length",
			expected: llm.StopReasonMaxTokens,
		},
		{
			name:     "tool_calls reason",
			reason:   "tool_calls",
			expected: llm.StopReasonToolUse,
		},
		{
			name:     "function_call reason",
			reason:   "function_call",
			expected: llm.StopReasonToolUse,
		},
		{
			name:     "content_filter reason",
			reason:   "content_filter",
			expected: llm.StopReasonStopSequence,
		},
		{
			name:     "unknown reason defaults to stop_sequence",
			reason:   "unknown",
			expected: llm.StopReasonStopSequence,
		},
		{
			name:     "empty reason defaults to stop_sequence",
			reason:   "",
			expected: llm.StopReasonStopSequence,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toStopReason(tt.reason)
			if result != tt.expected {
				t.Errorf("toStopReason(%q) = %v, expected %v", tt.reason, result, tt.expected)
			}
		})
	}
}

func TestTokenContextWindow(t *testing.T) {
	tests := []struct {
		name     string
		model    Model
		expected int
	}{
		{
			name:     "GPT-4.1 model",
			model:    GPT41,
			expected: 200000,
		},
		{
			name:     "GPT-4o model",
			model:    GPT4o,
			expected: 128000,
		},
		{
			name:     "GPT-4o Mini model",
			model:    GPT4oMini,
			expected: 128000,
		},
		{
			name:     "O3 model",
			model:    O3,
			expected: 200000,
		},
		{
			name:     "O4-mini model",
			model:    O4Mini,
			expected: 128000, // o4-mini-2025-04-16 is not in the special cases, so it defaults to 128k
		},
		{
			name:     "Gemini 2.5 Flash model",
			model:    Gemini25Flash,
			expected: 128000,
		},
		{
			name:     "Gemini 2.5 Pro model",
			model:    Gemini25Pro,
			expected: 128000,
		},
		{
			name:     "Together Deepseek V3 model",
			model:    TogetherDeepseekV3,
			expected: 128000,
		},
		{
			name:     "Together Qwen3 model",
			model:    TogetherQwen3,
			expected: 128000, // Qwen/Qwen3-235B-A22B-fp8-tput is not in the special cases, so it defaults to 128k
		},
		{
			name:     "Default model for unknown",
			model:    Model{ModelName: "unknown-model"},
			expected: 128000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &Service{Model: tt.model}
			result := service.TokenContextWindow()
			if result != tt.expected {
				t.Errorf("TokenContextWindow() for model %s = %d, expected %d", tt.model.ModelName, result, tt.expected)
			}
		})
	}
}

func TestMaxImageDimension(t *testing.T) {
	// Test both Service and ResponsesService
	model := GPT41

	// Test Service.MaxImageDimension
	service := &Service{Model: model}
	result := service.MaxImageDimension()
	if result != 0 {
		t.Errorf("Service.MaxImageDimension() = %d, expected 0", result)
	}

	// Test ResponsesService.MaxImageDimension
	responsesService := &ResponsesService{Model: model}
	result2 := responsesService.MaxImageDimension()
	if result2 != 0 {
		t.Errorf("ResponsesService.MaxImageDimension() = %d, expected 0", result2)
	}
}

func TestUseSimplifiedPatch(t *testing.T) {
	// Test Service.UseSimplifiedPatch
	tests := []struct {
		name     string
		model    Model
		expected bool
	}{
		{
			name:     "Default model (false)",
			model:    GPT41,
			expected: false,
		},
		{
			name:     "Model with UseSimplifiedPatch=true",
			model:    Model{UseSimplifiedPatch: true},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &Service{Model: tt.model}
			result := service.UseSimplifiedPatch()
			if result != tt.expected {
				t.Errorf("Service.UseSimplifiedPatch() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestConfigDetails(t *testing.T) {
	model := GPT41
	service := &Service{Model: model}

	details := service.ConfigDetails()

	expectedKeys := []string{"base_url", "model_name", "full_url", "api_key_env", "has_api_key_set"}
	for _, key := range expectedKeys {
		if _, exists := details[key]; !exists {
			t.Errorf("ConfigDetails() missing key: %s", key)
		}
	}

	if details["model_name"] != model.ModelName {
		t.Errorf("ConfigDetails()[model_name] = %s, expected %s", details["model_name"], model.ModelName)
	}

	if details["base_url"] != model.URL {
		t.Errorf("ConfigDetails()[base_url] = %s, expected %s", details["base_url"], model.URL)
	}

	if details["api_key_env"] != model.APIKeyEnv {
		t.Errorf("ConfigDetails()[api_key_env] = %s, expected %s", details["api_key_env"], model.APIKeyEnv)
	}
}

func TestOAIResponsesServiceUseSimplifiedPatch(t *testing.T) {
	model := Model{UseSimplifiedPatch: true}
	service := &ResponsesService{Model: model}

	result := service.UseSimplifiedPatch()
	if !result {
		t.Errorf("ResponsesService.UseSimplifiedPatch() = %v, expected true", result)
	}
}

func TestOAIResponsesServiceConfigDetails(t *testing.T) {
	model := GPT41
	service := &ResponsesService{Model: model}

	details := service.ConfigDetails()

	expectedKeys := []string{"base_url", "model_name", "full_url", "api_key_env", "has_api_key_set"}
	for _, key := range expectedKeys {
		if _, exists := details[key]; !exists {
			t.Errorf("ConfigDetails() missing key: %s", key)
		}
	}

	// Check that the full_url is different (should be /responses instead of /chat/completions)
	if details["full_url"] != model.URL+"/responses" {
		t.Errorf("ConfigDetails()[full_url] = %s, expected %s", details["full_url"], model.URL+"/responses")
	}
}

func TestFromLLMContent(t *testing.T) {
	// Test text content
	textContent := llm.Content{
		Type: llm.ContentTypeText,
		Text: "Hello, world!",
	}
	text, toolCalls := fromLLMContent(textContent)
	if text != "Hello, world!" {
		t.Errorf("fromLLMContent(text) text = %q, expected %q", text, "Hello, world!")
	}
	if len(toolCalls) != 0 {
		t.Errorf("fromLLMContent(text) toolCalls length = %d, expected 0", len(toolCalls))
	}

	// Test tool use content
	toolUseContent := llm.Content{
		Type:      llm.ContentTypeToolUse,
		ID:        "tool-call-1",
		ToolName:  "get_weather",
		ToolInput: json.RawMessage(`{"location": "New York"}`),
	}
	text, toolCalls = fromLLMContent(toolUseContent)
	if text != "" {
		t.Errorf("fromLLMContent(toolUse) text = %q, expected empty string", text)
	}
	if len(toolCalls) != 1 {
		t.Errorf("fromLLMContent(toolUse) toolCalls length = %d, expected 1", len(toolCalls))
	} else {
		tc := toolCalls[0]
		if tc.Type != openai.ToolTypeFunction {
			t.Errorf("toolCall.Type = %q, expected %q", tc.Type, openai.ToolTypeFunction)
		}
		if tc.ID != "tool-call-1" {
			t.Errorf("toolCall.ID = %q, expected %q", tc.ID, "tool-call-1")
		}
		if tc.Function.Name != "get_weather" {
			t.Errorf("toolCall.Function.Name = %q, expected %q", tc.Function.Name, "get_weather")
		}
		if tc.Function.Arguments != `{"location": "New York"}` {
			t.Errorf("toolCall.Function.Arguments = %q, expected %q", tc.Function.Arguments, `{"location": "New York"}`)
		}
	}

	// Test tool result content
	toolResultContent := llm.Content{
		Type: llm.ContentTypeToolResult,
		ToolResult: []llm.Content{
			{Type: llm.ContentTypeText, Text: "Sunny"},
			{Type: llm.ContentTypeText, Text: "72°F"},
		},
	}
	text, toolCalls = fromLLMContent(toolResultContent)
	expectedText := "Sunny\n72°F"
	if text != expectedText {
		t.Errorf("fromLLMContent(toolResult) text = %q, expected %q", text, expectedText)
	}
	if len(toolCalls) != 0 {
		t.Errorf("fromLLMContent(toolResult) toolCalls length = %d, expected 0", len(toolCalls))
	}

	// Test default case (thinking content)
	thinkingContent := llm.Content{
		Type: llm.ContentTypeThinking,
		Text: "Thinking about the answer...",
	}
	text, toolCalls = fromLLMContent(thinkingContent)
	if text != "Thinking about the answer..." {
		t.Errorf("fromLLMContent(thinking) text = %q, expected %q", text, "Thinking about the answer...")
	}
	if len(toolCalls) != 0 {
		t.Errorf("fromLLMContent(thinking) toolCalls length = %d, expected 0", len(toolCalls))
	}
}

func TestToRawLLMContent(t *testing.T) {
	content := toRawLLMContent("test text")
	if content.Type != llm.ContentTypeText {
		t.Errorf("toRawLLMContent().Type = %v, expected %v", content.Type, llm.ContentTypeText)
	}
	if content.Text != "test text" {
		t.Errorf("toRawLLMContent().Text = %q, expected %q", content.Text, "test text")
	}
}

func TestToToolCallLLMContent(t *testing.T) {
	// Test with ID
	toolCall := openai.ToolCall{
		ID:   "tool-call-1",
		Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{
			Name:      "get_weather",
			Arguments: `{"location": "New York"}`,
		},
	}
	content := toToolCallLLMContent(toolCall)
	if content.Type != llm.ContentTypeToolUse {
		t.Errorf("toToolCallLLMContent().Type = %v, expected %v", content.Type, llm.ContentTypeToolUse)
	}
	if content.ID != "tool-call-1" {
		t.Errorf("toToolCallLLMContent().ID = %q, expected %q", content.ID, "tool-call-1")
	}
	if content.ToolName != "get_weather" {
		t.Errorf("toToolCallLLMContent().ToolName = %q, expected %q", content.ToolName, "get_weather")
	}
	if string(content.ToolInput) != `{"location": "New York"}` {
		t.Errorf("toToolCallLLMContent().ToolInput = %q, expected %q", string(content.ToolInput), `{"location": "New York"}`)
	}

	// Test without ID (should generate one)
	toolCallNoID := openai.ToolCall{
		Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{
			Name:      "get_weather",
			Arguments: `{"location": "New York"}`,
		},
	}
	contentNoID := toToolCallLLMContent(toolCallNoID)
	if contentNoID.ID != "tc_get_weather" {
		t.Errorf("toToolCallLLMContent() with no ID = %q, expected %q", contentNoID.ID, "tc_get_weather")
	}
}

func TestToToolResultLLMContent(t *testing.T) {
	msg := openai.ChatCompletionMessage{
		Role:       "tool",
		Content:    "Sunny weather",
		ToolCallID: "tool-call-1",
	}
	content := toToolResultLLMContent(msg)
	if content.Type != llm.ContentTypeToolResult {
		t.Errorf("toToolResultLLMContent().Type = %v, expected %v", content.Type, llm.ContentTypeToolResult)
	}
	if content.ToolUseID != "tool-call-1" {
		t.Errorf("toToolResultLLMContent().ToolUseID = %q, expected %q", content.ToolUseID, "tool-call-1")
	}
	if len(content.ToolResult) != 1 {
		t.Errorf("toToolResultLLMContent().ToolResult length = %d, expected 1", len(content.ToolResult))
	} else {
		result := content.ToolResult[0]
		if result.Type != llm.ContentTypeText {
			t.Errorf("ToolResult[0].Type = %v, expected %v", result.Type, llm.ContentTypeText)
		}
		if result.Text != "Sunny weather" {
			t.Errorf("ToolResult[0].Text = %q, expected %q", result.Text, "Sunny weather")
		}
	}
	if content.ToolError != false {
		t.Errorf("toToolResultLLMContent().ToolError = %v, expected false", content.ToolError)
	}
}

func TestToLLMContents(t *testing.T) {
	// Test tool response message
	toolMsg := openai.ChatCompletionMessage{
		Role:       "tool",
		Content:    "Sunny weather",
		ToolCallID: "tool-call-1",
	}
	contents := toLLMContents(toolMsg)
	if len(contents) != 1 {
		t.Errorf("toLLMContents(toolMsg) length = %d, expected 1", len(contents))
	} else {
		content := contents[0]
		if content.Type != llm.ContentTypeToolResult {
			t.Errorf("toLLMContents(toolMsg)[0].Type = %v, expected %v", content.Type, llm.ContentTypeToolResult)
		}
	}

	// Test regular message with text
	textMsg := openai.ChatCompletionMessage{
		Role:    "assistant",
		Content: "Hello, world!",
	}
	contents = toLLMContents(textMsg)
	if len(contents) != 1 {
		t.Errorf("toLLMContents(textMsg) length = %d, expected 1", len(contents))
	} else {
		content := contents[0]
		if content.Type != llm.ContentTypeText {
			t.Errorf("toLLMContents(textMsg)[0].Type = %v, expected %v", content.Type, llm.ContentTypeText)
		}
		if content.Text != "Hello, world!" {
			t.Errorf("toLLMContents(textMsg)[0].Text = %q, expected %q", content.Text, "Hello, world!")
		}
	}

	// Test message with tool calls
	toolCallMsg := openai.ChatCompletionMessage{
		Role:    "assistant",
		Content: "",
		ToolCalls: []openai.ToolCall{
			{
				ID:   "tool-call-1",
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      "get_weather",
					Arguments: `{"location": "New York"}`,
				},
			},
		},
	}
	contents = toLLMContents(toolCallMsg)
	if len(contents) != 1 {
		t.Errorf("toLLMContents(toolCallMsg) length = %d, expected 1", len(contents))
	} else {
		content := contents[0]
		if content.Type != llm.ContentTypeToolUse {
			t.Errorf("toLLMContents(toolCallMsg)[0].Type = %v, expected %v", content.Type, llm.ContentTypeToolUse)
		}
	}

	// Test empty message
	emptyMsg := openai.ChatCompletionMessage{
		Role:    "assistant",
		Content: "",
	}
	contents = toLLMContents(emptyMsg)
	if len(contents) != 1 {
		t.Errorf("toLLMContents(emptyMsg) length = %d, expected 1", len(contents))
	} else {
		content := contents[0]
		if content.Type != llm.ContentTypeText {
			t.Errorf("toLLMContents(emptyMsg)[0].Type = %v, expected %v", content.Type, llm.ContentTypeText)
		}
		if content.Text != "" {
			t.Errorf("toLLMContents(emptyMsg)[0].Text = %q, expected empty string", content.Text)
		}
	}
}

func TestFromLLMToolChoice(t *testing.T) {
	// Test nil tool choice
	result := fromLLMToolChoice(nil)
	if result != nil {
		t.Errorf("fromLLMToolChoice(nil) = %v, expected nil", result)
	}

	// Test specific tool choice
	toolChoice := &llm.ToolChoice{
		Type: llm.ToolChoiceTypeTool,
		Name: "get_weather",
	}
	result = fromLLMToolChoice(toolChoice)
	if toolChoiceResult, ok := result.(openai.ToolChoice); !ok {
		t.Errorf("fromLLMToolChoice(tool) result type = %T, expected openai.ToolChoice", result)
	} else {
		if toolChoiceResult.Type != openai.ToolTypeFunction {
			t.Errorf("ToolChoice.Type = %q, expected %q", toolChoiceResult.Type, openai.ToolTypeFunction)
		}
		if toolChoiceResult.Function.Name != "get_weather" {
			t.Errorf("ToolChoice.Function.Name = %q, expected %q", toolChoiceResult.Function.Name, "get_weather")
		}
	}

	// Test auto tool choice
	autoChoice := &llm.ToolChoice{Type: llm.ToolChoiceTypeAuto}
	result = fromLLMToolChoice(autoChoice)
	if result != "auto" {
		t.Errorf("fromLLMToolChoice(auto) = %v, expected %q", result, "auto")
	}

	// Test any tool choice
	anyChoice := &llm.ToolChoice{Type: llm.ToolChoiceTypeAny}
	result = fromLLMToolChoice(anyChoice)
	if result != "any" {
		t.Errorf("fromLLMToolChoice(any) = %v, expected %q", result, "any")
	}

	// Test none tool choice
	noneChoice := &llm.ToolChoice{Type: llm.ToolChoiceTypeNone}
	result = fromLLMToolChoice(noneChoice)
	if result != "none" {
		t.Errorf("fromLLMToolChoice(none) = %v, expected %q", result, "none")
	}
}

func TestFromLLMMessage(t *testing.T) {
	// Test regular message with text content
	textMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "Hello, world!"},
		},
	}
	messages := fromLLMMessage(textMsg)
	if len(messages) != 1 {
		t.Errorf("fromLLMMessage(textMsg) length = %d, expected 1", len(messages))
	} else {
		msg := messages[0]
		if msg.Role != "user" {
			t.Errorf("message.Role = %q, expected %q", msg.Role, "user")
		}
		if msg.Content != "Hello, world!" {
			t.Errorf("message.Content = %q, expected %q", msg.Content, "Hello, world!")
		}
		if len(msg.ToolCalls) != 0 {
			t.Errorf("message.ToolCalls length = %d, expected 0", len(msg.ToolCalls))
		}
	}

	// Test assistant message with tool use
	toolMsg := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolUse,
				ID:        "tool-call-1",
				ToolName:  "get_weather",
				ToolInput: json.RawMessage(`{"location": "New York"}`),
			},
		},
	}
	messages = fromLLMMessage(toolMsg)
	if len(messages) != 1 {
		t.Errorf("fromLLMMessage(toolMsg) length = %d, expected 1", len(messages))
	} else {
		msg := messages[0]
		if msg.Role != "assistant" {
			t.Errorf("message.Role = %q, expected %q", msg.Role, "assistant")
		}
		if msg.Content != "" {
			t.Errorf("message.Content = %q, expected empty string", msg.Content)
		}
		if len(msg.ToolCalls) != 1 {
			t.Errorf("message.ToolCalls length = %d, expected 1", len(msg.ToolCalls))
		} else {
			tc := msg.ToolCalls[0]
			if tc.ID != "tool-call-1" {
				t.Errorf("toolCall.ID = %q, expected %q", tc.ID, "tool-call-1")
			}
			if tc.Function.Name != "get_weather" {
				t.Errorf("toolCall.Function.Name = %q, expected %q", tc.Function.Name, "get_weather")
			}
		}
	}

	// Test message with tool result
	toolResultMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-call-1",
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Sunny"},
					{Type: llm.ContentTypeText, Text: "72°F"},
				},
			},
		},
	}
	messages = fromLLMMessage(toolResultMsg)
	if len(messages) != 1 {
		t.Errorf("fromLLMMessage(toolResultMsg) length = %d, expected 1", len(messages))
	} else {
		msg := messages[0]
		if msg.Role != "tool" {
			t.Errorf("message.Role = %q, expected %q", msg.Role, "tool")
		}
		expectedContent := "Sunny\n72°F"
		if msg.Content != expectedContent {
			t.Errorf("message.Content = %q, expected %q", msg.Content, expectedContent)
		}
		if msg.ToolCallID != "tool-call-1" {
			t.Errorf("message.ToolCallID = %q, expected %q", msg.ToolCallID, "tool-call-1")
		}
	}

	// Test message with tool result and error
	toolResultErrorMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-call-1",
				ToolError: true,
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: "API error"},
				},
			},
		},
	}
	messages = fromLLMMessage(toolResultErrorMsg)
	if len(messages) != 1 {
		t.Errorf("fromLLMMessage(toolResultErrorMsg) length = %d, expected 1", len(messages))
	} else {
		msg := messages[0]
		if msg.Role != "tool" {
			t.Errorf("message.Role = %q, expected %q", msg.Role, "tool")
		}
		expectedContent := "error: API error"
		if msg.Content != expectedContent {
			t.Errorf("message.Content = %q, expected %q", msg.Content, expectedContent)
		}
		if msg.ToolCallID != "tool-call-1" {
			t.Errorf("message.ToolCallID = %q, expected %q", msg.ToolCallID, "tool-call-1")
		}
	}

	// Test message with both regular content and tool result
	mixedMsg := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "The weather is:"},
			{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-call-1",
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Sunny"},
				},
			},
		},
	}
	messages = fromLLMMessage(mixedMsg)
	if len(messages) != 2 {
		t.Errorf("fromLLMMessage(mixedMsg) length = %d, expected 2", len(messages))
	} else {
		// First message should be the tool result
		toolMsg := messages[0]
		if toolMsg.Role != "tool" {
			t.Errorf("first message.Role = %q, expected %q", toolMsg.Role, "tool")
		}
		if toolMsg.Content != "Sunny" {
			t.Errorf("first message.Content = %q, expected %q", toolMsg.Content, "Sunny")
		}

		// Second message should be the regular content
		regularMsg := messages[1]
		if regularMsg.Role != "assistant" {
			t.Errorf("second message.Role = %q, expected %q", regularMsg.Role, "assistant")
		}
		if regularMsg.Content != "The weather is:" {
			t.Errorf("second message.Content = %q, expected %q", regularMsg.Content, "The weather is:")
		}
	}
}

func TestFromLLMTool(t *testing.T) {
	tool := &llm.Tool{
		Name:        "get_weather",
		Description: "Get the current weather for a location",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {"location": {"type": "string"}}}`),
	}
	openaiTool := fromLLMTool(tool)
	if openaiTool.Type != openai.ToolTypeFunction {
		t.Errorf("fromLLMTool().Type = %q, expected %q", openaiTool.Type, openai.ToolTypeFunction)
	}
	if openaiTool.Function.Name != "get_weather" {
		t.Errorf("fromLLMTool().Function.Name = %q, expected %q", openaiTool.Function.Name, "get_weather")
	}
	if openaiTool.Function.Description != "Get the current weather for a location" {
		t.Errorf("fromLLMTool().Function.Description = %q, expected %q", openaiTool.Function.Description, "Get the current weather for a location")
	}
	// Note: Parameters is stored as json.RawMessage (byte slice), so we can't directly compare as string
	// The important thing is that it's not nil and was assigned
	if openaiTool.Function.Parameters == nil {
		t.Errorf("fromLLMTool().Function.Parameters should not be nil")
	}
}

func TestListModels(t *testing.T) {
	models := ListModels()
	if len(models) == 0 {
		t.Errorf("ListModels() returned empty slice")
	}
	// Check that some known models are in the list
	expectedModels := []string{"gpt4.1", "gpt4o", "gpt4o-mini", "o3", "o4-mini"}
	for _, expected := range expectedModels {
		found := false
		for _, model := range models {
			if model == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ListModels() missing expected model: %s", expected)
		}
	}
}

func TestModelByUserName(t *testing.T) {
	// Test finding an existing model
	model := ModelByUserName("gpt4.1")
	if model.UserName != "gpt4.1" {
		t.Errorf("ModelByUserName(gpt4.1).UserName = %q, expected %q", model.UserName, "gpt4.1")
	}

	// Test finding a non-existent model
	model = ModelByUserName("non-existent")
	if !model.IsZero() {
		t.Errorf("ModelByUserName(non-existent) should return zero value, got: %+v", model)
	}
}

func TestModelIsZero(t *testing.T) {
	// Test zero value
	var zeroModel Model
	if !zeroModel.IsZero() {
		t.Errorf("Model{}.IsZero() = false, expected true")
	}

	// Test non-zero value
	model := GPT41
	if model.IsZero() {
		t.Errorf("GPT41.IsZero() = true, expected false")
	}
}

func TestToLLMUsage(t *testing.T) {
	// Create a service instance
	service := &Service{}

	// Test usage conversion
	openaiUsage := openai.Usage{
		PromptTokens:     100,
		CompletionTokens: 50,
	}
	usage := service.toLLMUsage(openaiUsage, nil)
	if usage.InputTokens != 100 {
		t.Errorf("toLLMUsage().InputTokens = %d, expected 100", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("toLLMUsage().OutputTokens = %d, expected 50", usage.OutputTokens)
	}
	if usage.CacheReadInputTokens != 0 {
		t.Errorf("toLLMUsage().CacheReadInputTokens = %d, expected 0", usage.CacheReadInputTokens)
	}

	// Test with prompt tokens details
	openaiUsageWithDetails := openai.Usage{
		PromptTokens:     100,
		CompletionTokens: 50,
		PromptTokensDetails: &openai.PromptTokensDetails{
			CachedTokens: 25,
		},
	}
	usage = service.toLLMUsage(openaiUsageWithDetails, nil)
	if usage.InputTokens != 100 {
		t.Errorf("toLLMUsage().InputTokens = %d, expected 100", usage.InputTokens)
	}
	if usage.CacheReadInputTokens != 25 {
		t.Errorf("toLLMUsage().CacheReadInputTokens = %d, expected 25", usage.CacheReadInputTokens)
	}
}

func TestToLLMResponse(t *testing.T) {
	// Create a service instance
	service := &Service{}

	// Test response with no choices
	emptyResponse := &openai.ChatCompletionResponse{
		ID:    "test-id",
		Model: "gpt-4.1",
	}
	response := service.toLLMResponse(emptyResponse)
	if response.ID != "test-id" {
		t.Errorf("toLLMResponse().ID = %q, expected %q", response.ID, "test-id")
	}
	if response.Model != "gpt-4.1" {
		t.Errorf("toLLMResponse().Model = %q, expected %q", response.Model, "gpt-4.1")
	}
	if response.Role != llm.MessageRoleAssistant {
		t.Errorf("toLLMResponse().Role = %v, expected %v", response.Role, llm.MessageRoleAssistant)
	}
	if len(response.Content) != 0 {
		t.Errorf("toLLMResponse().Content length = %d, expected 0", len(response.Content))
	}

	// Test response with a choice
	choiceResponse := &openai.ChatCompletionResponse{
		ID:    "test-id-2",
		Model: "gpt-4.1",
		Choices: []openai.ChatCompletionChoice{
			{
				Message: openai.ChatCompletionMessage{
					Role:    "assistant",
					Content: "Hello, world!",
				},
				FinishReason: openai.FinishReasonStop,
			},
		},
		Usage: openai.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
		},
	}
	response = service.toLLMResponse(choiceResponse)
	if response.ID != "test-id-2" {
		t.Errorf("toLLMResponse().ID = %q, expected %q", response.ID, "test-id-2")
	}
	if response.Model != "gpt-4.1" {
		t.Errorf("toLLMResponse().Model = %q, expected %q", response.Model, "gpt-4.1")
	}
	if response.Role != llm.MessageRoleAssistant {
		t.Errorf("toLLMResponse().Role = %v, expected %v", response.Role, llm.MessageRoleAssistant)
	}
	if len(response.Content) != 1 {
		t.Errorf("toLLMResponse().Content length = %d, expected 1", len(response.Content))
	} else {
		content := response.Content[0]
		if content.Type != llm.ContentTypeText {
			t.Errorf("response.Content[0].Type = %v, expected %v", content.Type, llm.ContentTypeText)
		}
		if content.Text != "Hello, world!" {
			t.Errorf("response.Content[0].Text = %q, expected %q", content.Text, "Hello, world!")
		}
	}
	if response.StopReason != llm.StopReasonStopSequence {
		t.Errorf("toLLMResponse().StopReason = %v, expected %v", response.StopReason, llm.StopReasonStopSequence)
	}
	if response.Usage.InputTokens != 100 {
		t.Errorf("toLLMResponse().Usage.InputTokens = %d, expected 100", response.Usage.InputTokens)
	}
	if response.Usage.OutputTokens != 50 {
		t.Errorf("toLLMResponse().Usage.OutputTokens = %d, expected 50", response.Usage.OutputTokens)
	}
}

func TestFromLLMSystem(t *testing.T) {
	// Test empty system content
	messages := fromLLMSystem(nil)
	if messages != nil {
		t.Errorf("fromLLMSystem(nil) = %v, expected nil", messages)
	}

	// Test empty slice
	messages = fromLLMSystem([]llm.SystemContent{})
	if messages != nil {
		t.Errorf("fromLLMSystem([]) = %v, expected nil", messages)
	}

	// Test single system content
	systemContent := []llm.SystemContent{
		{Text: "You are a helpful assistant."},
	}
	messages = fromLLMSystem(systemContent)
	if len(messages) != 1 {
		t.Errorf("fromLLMSystem(single) length = %d, expected 1", len(messages))
	} else {
		msg := messages[0]
		if msg.Role != "system" {
			t.Errorf("message.Role = %q, expected %q", msg.Role, "system")
		}
		if msg.Content != "You are a helpful assistant." {
			t.Errorf("message.Content = %q, expected %q", msg.Content, "You are a helpful assistant.")
		}
	}

	// Test multiple system content
	multiSystemContent := []llm.SystemContent{
		{Text: "You are a helpful assistant."},
		{Text: "Be concise in your responses."},
	}
	messages = fromLLMSystem(multiSystemContent)
	if len(messages) != 1 {
		t.Errorf("fromLLMSystem(multiple) length = %d, expected 1", len(messages))
	} else {
		msg := messages[0]
		if msg.Role != "system" {
			t.Errorf("message.Role = %q, expected %q", msg.Role, "system")
		}
		expectedContent := "You are a helpful assistant.\nBe concise in your responses."
		if msg.Content != expectedContent {
			t.Errorf("message.Content = %q, expected %q", msg.Content, expectedContent)
		}
	}

	// Test system content with empty text
	emptySystemContent := []llm.SystemContent{
		{Text: ""},
		{Text: "You are a helpful assistant."},
		{Text: ""},
	}
	messages = fromLLMSystem(emptySystemContent)
	if len(messages) != 1 {
		t.Errorf("fromLLMSystem(with empty) length = %d, expected 1", len(messages))
	} else {
		msg := messages[0]
		if msg.Role != "system" {
			t.Errorf("message.Role = %q, expected %q", msg.Role, "system")
		}
		if msg.Content != "You are a helpful assistant." {
			t.Errorf("message.Content = %q, expected %q", msg.Content, "You are a helpful assistant.")
		}
	}

	// Test system content with all empty text (should return nil)
	allEmptySystemContent := []llm.SystemContent{
		{Text: ""},
		{Text: ""},
		{Text: ""},
	}
	messages = fromLLMSystem(allEmptySystemContent)
	if messages != nil {
		t.Errorf("fromLLMSystem(all empty) = %v, expected nil", messages)
	}
}

func TestFromLLMMessageEdgeCases(t *testing.T) {
	// Test message with tool results containing empty text
	toolResultMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-call-1",
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: ""},
				},
			},
		},
	}
	messages := fromLLMMessage(toolResultMsg)
	if len(messages) != 1 {
		t.Errorf("fromLLMMessage(toolResultMsg) length = %d, expected 1", len(messages))
	} else {
		msg := messages[0]
		if msg.Role != "tool" {
			t.Errorf("message.Role = %q, expected %q", msg.Role, "tool")
		}
		// Should be " " (space) when empty to avoid omitempty issues
		if msg.Content != " " {
			t.Errorf("message.Content = %q, expected %q", msg.Content, " ")
		}
		if msg.ToolCallID != "tool-call-1" {
			t.Errorf("message.ToolCallID = %q, expected %q", msg.ToolCallID, "tool-call-1")
		}
	}

	// Test message with tool results containing only whitespace
	toolResultWhitespaceMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-call-2",
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: "   \n\t  "},
				},
			},
		},
	}
	messages = fromLLMMessage(toolResultWhitespaceMsg)
	if len(messages) != 1 {
		t.Errorf("fromLLMMessage(toolResultWhitespaceMsg) length = %d, expected 1", len(messages))
	} else {
		msg := messages[0]
		if msg.Role != "tool" {
			t.Errorf("message.Role = %q, expected %q", msg.Role, "tool")
		}
		// Should be " " (space) when only whitespace to avoid omitempty issues
		if msg.Content != " " {
			t.Errorf("message.Content = %q, expected %q", msg.Content, " ")
		}
		if msg.ToolCallID != "tool-call-2" {
			t.Errorf("message.ToolCallID = %q, expected %q", msg.ToolCallID, "tool-call-2")
		}
	}

	// Test message with tool error but empty content
	toolErrorEmptyMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-call-3",
				ToolError: true,
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: ""},
				},
			},
		},
	}
	messages = fromLLMMessage(toolErrorEmptyMsg)
	if len(messages) != 1 {
		t.Errorf("fromLLMMessage(toolErrorEmptyMsg) length = %d, expected 1", len(messages))
	} else {
		msg := messages[0]
		if msg.Role != "tool" {
			t.Errorf("message.Role = %q, expected %q", msg.Role, "tool")
		}
		expectedContent := "error: tool execution failed"
		if msg.Content != expectedContent {
			t.Errorf("message.Content = %q, expected %q", msg.Content, expectedContent)
		}
		if msg.ToolCallID != "tool-call-3" {
			t.Errorf("message.ToolCallID = %q, expected %q", msg.ToolCallID, "tool-call-3")
		}
	}

	// Test message with tool error and content
	toolErrorWithContentMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-call-4",
				ToolError: true,
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: "something went wrong"},
				},
			},
		},
	}
	messages = fromLLMMessage(toolErrorWithContentMsg)
	if len(messages) != 1 {
		t.Errorf("fromLLMMessage(toolErrorWithContentMsg) length = %d, expected 1", len(messages))
	} else {
		msg := messages[0]
		if msg.Role != "tool" {
			t.Errorf("message.Role = %q, expected %q", msg.Role, "tool")
		}
		expectedContent := "error: something went wrong"
		if msg.Content != expectedContent {
			t.Errorf("message.Content = %q, expected %q", msg.Content, expectedContent)
		}
		if msg.ToolCallID != "tool-call-4" {
			t.Errorf("message.ToolCallID = %q, expected %q", msg.ToolCallID, "tool-call-4")
		}
	}

	// Test message with mixed content (regular text + tool results)
	mixedContentMsg := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "Here's the result:"},
			{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-call-5",
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: "The weather is sunny"},
				},
			},
			{Type: llm.ContentTypeText, Text: "Have a nice day!"},
		},
	}
	messages = fromLLMMessage(mixedContentMsg)
	// Should produce 2 messages: one tool result message and one regular message
	if len(messages) != 2 {
		t.Errorf("fromLLMMessage(mixedContentMsg) length = %d, expected 2", len(messages))
	} else {
		// First message should be the tool result
		toolMsg := messages[0]
		if toolMsg.Role != "tool" {
			t.Errorf("first message.Role = %q, expected %q", toolMsg.Role, "tool")
		}
		if toolMsg.Content != "The weather is sunny" {
			t.Errorf("first message.Content = %q, expected %q", toolMsg.Content, "The weather is sunny")
		}
		if toolMsg.ToolCallID != "tool-call-5" {
			t.Errorf("first message.ToolCallID = %q, expected %q", toolMsg.ToolCallID, "tool-call-5")
		}

		// Second message should be the regular content
		regularMsg := messages[1]
		if regularMsg.Role != "assistant" {
			t.Errorf("second message.Role = %q, expected %q", regularMsg.Role, "assistant")
		}
		// Should combine both text contents with newline
		expectedContent := "Here's the result:\nHave a nice day!"
		if regularMsg.Content != expectedContent {
			t.Errorf("second message.Content = %q, expected %q", regularMsg.Content, expectedContent)
		}
	}
}

func TestTokenContextWindowAdditionalCases(t *testing.T) {
	tests := []struct {
		name     string
		model    Model
		expected int
	}{
		{
			name:     "GPT-4.1 Mini model",
			model:    GPT41Mini,
			expected: 200000,
		},
		{
			name:     "GPT-4.1 Nano model",
			model:    GPT41Nano,
			expected: 200000,
		},
		{
			name:     "GLM model",
			model:    GLM,
			expected: 128000,
		},
		{
			name:     "Qwen model",
			model:    Qwen,
			expected: 256000,
		},
		{
			name:     "GPT-OSS 20B model",
			model:    GPTOSS20B,
			expected: 128000,
		},
		{
			name:     "GPT-OSS 120B model",
			model:    GPTOSS120B,
			expected: 128000,
		},
		{
			name:     "GPT-5 model",
			model:    GPT5,
			expected: 256000,
		},
		{
			name:     "GPT-5 Mini model",
			model:    GPT5Mini,
			expected: 256000,
		},
		{
			name:     "GPT-5 Nano model",
			model:    GPT5Nano,
			expected: 256000,
		},
		{
			name:     "Unknown model defaults to 128k",
			model:    Model{ModelName: "unknown-model-name"},
			expected: 128000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &Service{Model: tt.model}
			result := service.TokenContextWindow()
			if result != tt.expected {
				t.Errorf("TokenContextWindow() for model %s = %d, expected %d", tt.model.ModelName, result, tt.expected)
			}
		})
	}
}

func TestServiceDo(t *testing.T) {
	// Create a mock OpenAI server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("Expected path /v1/chat/completions, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Errorf("Expected Authorization header, got %s", r.Header.Get("Authorization"))
		}

		// Send a mock response
		response := openai.ChatCompletionResponse{
			ID:      "chatcmpl-test123",
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   "gpt-4.1-2025-04-14",
			Choices: []openai.ChatCompletionChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionMessage{
						Role:    "assistant",
						Content: "Hello! How can I help you today?",
					},
					FinishReason: "stop",
				},
			},
			Usage: openai.Usage{
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalTokens:      30,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create a service with the mock server
	ctx := context.Background()
	svc := &Service{
		APIKey:   "test-api-key",
		Model:    GPT41,
		ModelURL: server.URL + "/v1",
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
