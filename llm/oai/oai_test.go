package oai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
			name: "Default model for unknown",
			model: Model{
				UserName:           "",
				ModelName:          "unknown-model",
				URL:                "",
				APIKeyEnv:          "",
				IsReasoningModel:   false,
				UseSimplifiedPatch: false,
				SupportsImages:     false,
			},
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
			name: "Model with UseSimplifiedPatch=true",
			model: Model{
				UserName:           "",
				ModelName:          "",
				URL:                "",
				APIKeyEnv:          "",
				IsReasoningModel:   false,
				UseSimplifiedPatch: true,
				SupportsImages:     false,
			},
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
	model := Model{
		UserName:           "",
		ModelName:          "",
		URL:                "",
		APIKeyEnv:          "",
		IsReasoningModel:   false,
		UseSimplifiedPatch: true,
		SupportsImages:     false,
	}
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

	// Thinking content is hoisted onto the outgoing message's reasoning_content
	// field by fromLLMMessage, so fromLLMContent itself returns no text and no
	// tool calls for thinking blocks.
	thinkingContent := llm.Content{
		Type:     llm.ContentTypeThinking,
		Thinking: "Thinking about the answer...",
	}
	text, toolCalls = fromLLMContent(thinkingContent)
	if text != "" {
		t.Errorf("fromLLMContent(thinking) text = %q, expected empty", text)
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

	// Test user message with text and image content
	imageMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "What is in this image?"},
			{Type: llm.ContentTypeText, MediaType: "image/png", Data: "abc123"},
		},
	}
	messages = fromLLMMessage(imageMsg)
	if len(messages) != 1 {
		t.Errorf("fromLLMMessage(imageMsg) length = %d, expected 1", len(messages))
	} else {
		msg := messages[0]
		if msg.Content != "" {
			t.Errorf("image message Content = %q, expected empty string", msg.Content)
		}
		if len(msg.MultiContent) != 2 {
			t.Fatalf("image message MultiContent length = %d, expected 2", len(msg.MultiContent))
		}
		if msg.MultiContent[0].Type != openai.ChatMessagePartTypeText || msg.MultiContent[0].Text != "What is in this image?" {
			t.Errorf("unexpected text part: %+v", msg.MultiContent[0])
		}
		if msg.MultiContent[1].Type != openai.ChatMessagePartTypeImageURL {
			t.Errorf("second part type = %q, expected image_url", msg.MultiContent[1].Type)
		}
		if msg.MultiContent[1].ImageURL == nil || msg.MultiContent[1].ImageURL.URL != "data:image/png;base64,abc123" {
			t.Errorf("unexpected image URL: %+v", msg.MultiContent[1].ImageURL)
		}
	}

	// Test user message with image only
	imageOnlyMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, MediaType: "image/png", Data: "imageonly"},
		},
	}
	messages = fromLLMMessage(imageOnlyMsg)
	if len(messages) != 1 {
		t.Errorf("fromLLMMessage(imageOnlyMsg) length = %d, expected 1", len(messages))
	} else if len(messages[0].MultiContent) != 1 || messages[0].MultiContent[0].ImageURL == nil || messages[0].MultiContent[0].ImageURL.URL != "data:image/png;base64,imageonly" {
		t.Errorf("unexpected image-only message: %+v", messages[0])
	}

	// Test user message with multiple images preserves order
	multiImageMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, MediaType: "image/png", Data: "first"},
			{Type: llm.ContentTypeText, Text: "between"},
			{Type: llm.ContentTypeText, MediaType: "image/jpeg", Data: "second"},
		},
	}
	messages = fromLLMMessage(multiImageMsg)
	if len(messages) != 1 || len(messages[0].MultiContent) != 3 {
		t.Fatalf("unexpected multi-image message: %+v", messages)
	}
	if messages[0].MultiContent[0].ImageURL.URL != "data:image/png;base64,first" || messages[0].MultiContent[1].Text != "between" || messages[0].MultiContent[2].ImageURL.URL != "data:image/jpeg;base64,second" {
		t.Errorf("multi-image order not preserved: %+v", messages[0].MultiContent)
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

	// Test message with tool result containing image
	toolResultImageMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-call-image",
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Screenshot captured"},
					{Type: llm.ContentTypeText, MediaType: "image/jpeg", Data: "xyz789"},
				},
			},
		},
	}
	messages = fromLLMMessage(toolResultImageMsg)
	if len(messages) != 2 {
		t.Errorf("fromLLMMessage(toolResultImageMsg) length = %d, expected 2", len(messages))
	} else {
		if messages[0].Role != "tool" || messages[0].Content != "Screenshot captured" || messages[0].ToolCallID != "tool-call-image" {
			t.Errorf("unexpected tool result message: %+v", messages[0])
		}
		if messages[1].Role != "user" {
			t.Errorf("image follow-up role = %q, expected user", messages[1].Role)
		}
		if len(messages[1].MultiContent) != 2 {
			t.Fatalf("image follow-up MultiContent length = %d, expected 2", len(messages[1].MultiContent))
		}
		if messages[1].MultiContent[1].ImageURL == nil || messages[1].MultiContent[1].ImageURL.URL != "data:image/jpeg;base64,xyz789" {
			t.Errorf("unexpected tool image URL: %+v", messages[1].MultiContent[1].ImageURL)
		}
	}

	// Test message with image-only tool result
	toolResultImageOnlyMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-call-image-only",
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, MediaType: "image/png", Data: "onlyimage"},
				},
			},
		},
	}
	messages = fromLLMMessage(toolResultImageOnlyMsg)
	if len(messages) != 2 {
		t.Errorf("fromLLMMessage(toolResultImageOnlyMsg) length = %d, expected 2", len(messages))
	} else {
		if messages[0].Role != "tool" || messages[0].Content != " " || messages[0].ToolCallID != "tool-call-image-only" {
			t.Errorf("unexpected image-only tool message: %+v", messages[0])
		}
		if messages[1].Role != "user" || len(messages[1].MultiContent) != 2 || messages[1].MultiContent[1].ImageURL.URL != "data:image/png;base64,onlyimage" {
			t.Errorf("unexpected image-only follow-up: %+v", messages[1])
		}
	}

	// Test tool-result image stays adjacent to its originating tool result before regular content
	toolResultWithRegularMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-call-adjacent",
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Screenshot captured"},
					{Type: llm.ContentTypeText, MediaType: "image/png", Data: "adjacent"},
				},
			},
			{Type: llm.ContentTypeText, Text: "regular text"},
		},
	}
	messages = fromLLMMessage(toolResultWithRegularMsg)
	if len(messages) != 3 {
		t.Fatalf("fromLLMMessage(toolResultWithRegularMsg) length = %d, expected 3", len(messages))
	}
	if messages[0].Role != "tool" || messages[1].Role != "user" || len(messages[1].MultiContent) != 2 || messages[2].Content != "regular text" {
		t.Errorf("tool image is not adjacent before regular content: %+v", messages)
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

func TestFromLLMMessageFiltersServerSideContent(t *testing.T) {
	// Server-side content blocks (e.g., Anthropic web search) should be
	// stripped when converting to OpenAI messages.
	msg := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "Let me search for that."},
			{Type: llm.ContentTypeServerToolUse, ID: "srvtoolu_123", ToolName: "web_search"},
			{Type: llm.ContentTypeWebSearchToolResult, ID: "srvtoolu_123"},
			{Type: llm.ContentTypeText, Text: "Here are the results."},
		},
	}
	messages := fromLLMMessage(msg)
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	got := messages[0].Content
	want := "Let me search for that.\nHere are the results."
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if len(messages[0].ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(messages[0].ToolCalls))
	}
}

func TestServerSideToolsFilteredFromOAIRequest(t *testing.T) {
	// Server-side tools must not be sent to OpenAI.
	tools := []*llm.Tool{
		{
			Name:        "bash",
			Description: "Run a command",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		{
			Name:       "web_search",
			Type:       "web_search_20250305",
			ServerSide: true,
		},
	}
	var oaiTools []openai.Tool
	for _, t := range tools {
		if t.ServerSide {
			continue
		}
		oaiTools = append(oaiTools, fromLLMTool(t))
	}
	if len(oaiTools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(oaiTools))
	}
	if oaiTools[0].Function.Name != "bash" {
		t.Errorf("expected tool name %q, got %q", "bash", oaiTools[0].Function.Name)
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
	// InputTokens should be non-cached portion: 100 - 25 = 75
	if usage.InputTokens != 75 {
		t.Errorf("toLLMUsage().InputTokens = %d, expected 75", usage.InputTokens)
	}
	if usage.CacheReadInputTokens != 25 {
		t.Errorf("toLLMUsage().CacheReadInputTokens = %d, expected 25", usage.CacheReadInputTokens)
	}
	// CacheCreationInputTokens should be 0 (OpenAI doesn't report this)
	if usage.CacheCreationInputTokens != 0 {
		t.Errorf("toLLMUsage().CacheCreationInputTokens = %d, expected 0", usage.CacheCreationInputTokens)
	}
	// TotalInputTokens should equal the original PromptTokens (100)
	if usage.TotalInputTokens() != 100 {
		t.Errorf("toLLMUsage().TotalInputTokens() = %d, expected 100", usage.TotalInputTokens())
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
			name:     "DeepSeek V4 Pro Fireworks model",
			model:    DeepseekV4ProFireworks,
			expected: 1048576,
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
			name:     "GPT-5.5 model",
			model:    GPT55,
			expected: 272000,
		},
		{
			name:     "GPT-5.5 Pro model",
			model:    GPT55Pro,
			expected: 272000,
		},
		{
			name: "GPT-5.5 dated model",
			model: Model{
				UserName:           "",
				ModelName:          "gpt-5.5-2026-04-23",
				URL:                "",
				APIKeyEnv:          "",
				IsReasoningModel:   false,
				UseSimplifiedPatch: false,
				SupportsImages:     false,
			},
			expected: 272000,
		},
		{
			name: "GPT-5.5 Pro dated model",
			model: Model{
				UserName:           "",
				ModelName:          "gpt-5.5-pro-2026-04-23",
				URL:                "",
				APIKeyEnv:          "",
				IsReasoningModel:   false,
				UseSimplifiedPatch: false,
				SupportsImages:     false,
			},
			expected: 272000,
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
			name: "Unknown model defaults to 128k",
			model: Model{
				UserName:           "",
				ModelName:          "unknown-model-name",
				URL:                "",
				APIKeyEnv:          "",
				IsReasoningModel:   false,
				UseSimplifiedPatch: false,
				SupportsImages:     false,
			},
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

func TestServiceDoOmitsMaxCompletionTokens(t *testing.T) {
	var gotReq map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			ID: "chatcmpl-test",
			Choices: []openai.ChatCompletionChoice{{
				Message:      openai.ChatCompletionMessage{Role: "assistant", Content: "ok"},
				FinishReason: "stop",
			}},
		})
	}))
	defer server.Close()

	svc := &Service{
		APIKey:   "test-api-key",
		Model:    Model{ModelName: "test-model"},
		ModelURL: server.URL + "/v1",
	}

	_, err := svc.Do(context.Background(), &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}},
		}},
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if _, hasCap := gotReq["max_completion_tokens"]; hasCap {
		t.Fatalf("max_completion_tokens present; body = %#v", gotReq)
	}
}

func TestServiceDoProxyPlainTextError(t *testing.T) {
	// Simulate a proxy returning a plain-text error (not JSON).
	// Previously this would fail to parse as *openai.APIError and
	// return immediately without retrying. Now it should be handled
	// via *openai.RequestError and retried as a 502.
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			// Return plain-text 502 like the integration proxy used to
			http.Error(w, "integration proxy: upstream request failed (trace: abc123)", http.StatusBadGateway)
			return
		}
		// Third attempt succeeds
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			ID:     "chatcmpl-test",
			Object: "chat.completion",
			Model:  "gpt-4.1",
			Choices: []openai.ChatCompletionChoice{{
				Index:        0,
				Message:      openai.ChatCompletionMessage{Role: "assistant", Content: "ok"},
				FinishReason: "stop",
			}},
		})
	}))
	defer server.Close()

	svc := &Service{
		APIKey:   "test-key",
		Model:    GPT41,
		ModelURL: server.URL + "/v1",
		Backoff:  []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond},
	}
	req := &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}},
		}},
	}

	resp, err := svc.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do() error = %v, expected success after retry", err)
	}
	if resp == nil {
		t.Fatal("Do() returned nil response")
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts (2 failures + 1 success), got %d", attempts)
	}
}

func TestServiceDoProxyPlainText4xxError(t *testing.T) {
	// A 403 plain-text error should NOT be retried.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "integration not found or not attached to this VM", http.StatusForbidden)
	}))
	defer server.Close()

	svc := &Service{
		APIKey:   "test-key",
		Model:    GPT41,
		ModelURL: server.URL + "/v1",
	}
	req := &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}},
		}},
	}

	_, err := svc.Do(context.Background(), req)
	if err == nil {
		t.Fatal("Do() expected error for 403, got nil")
	}
	if !strings.Contains(err.Error(), "status 403") {
		t.Errorf("expected error to mention status 403, got: %v", err)
	}
	if !strings.Contains(err.Error(), "integration not found") {
		t.Errorf("expected error to contain proxy message, got: %v", err)
	}
}

func TestIsDeepSeekBaseURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://api.deepseek.com", true},
		{"https://api.deepseek.com/v1", true},
		{"https://api.deepseek.com:443/v1", true},
		{"https://API.DeepSeek.com/v1", true},
		{"https://beta.deepseek.com", true},
		{"https://deepseek.com", true},
		{"https://api.openai.com/v1", false},
		{"https://api.fireworks.ai/inference/v1", false},
		{"https://gateway.example.com/_/gateway/openai/v1", false},
		{"", false},
		{"://bad", false},
	}
	for _, tt := range tests {
		if got := isDeepSeekBaseURL(tt.url); got != tt.want {
			t.Errorf("isDeepSeekBaseURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestToLLMContentsExtractsReasoningContent(t *testing.T) {
	msg := openai.ChatCompletionMessage{
		Role:             "assistant",
		Content:          "The answer is 4.",
		ReasoningContent: "2+2 = 4 is basic arithmetic.",
	}
	contents := toLLMContents(msg)
	if len(contents) != 2 {
		t.Fatalf("got %d contents, want 2; contents=%+v", len(contents), contents)
	}
	if contents[0].Type != llm.ContentTypeThinking {
		t.Errorf("contents[0].Type = %v, want Thinking", contents[0].Type)
	}
	if contents[0].Thinking != "2+2 = 4 is basic arithmetic." {
		t.Errorf("contents[0].Thinking = %q", contents[0].Thinking)
	}
	if contents[1].Type != llm.ContentTypeText || contents[1].Text != "The answer is 4." {
		t.Errorf("contents[1] = %+v, want text 'The answer is 4.'", contents[1])
	}
}

func TestToLLMContentsReasoningWithToolCalls(t *testing.T) {
	msg := openai.ChatCompletionMessage{
		Role:             "assistant",
		ReasoningContent: "I need to call the weather tool.",
		ToolCalls: []openai.ToolCall{{
			ID: "call_1", Type: openai.ToolTypeFunction,
			Function: openai.FunctionCall{Name: "get_weather", Arguments: `{"city":"Paris"}`},
		}},
	}
	contents := toLLMContents(msg)
	if len(contents) != 2 {
		t.Fatalf("got %d contents, want 2; contents=%+v", len(contents), contents)
	}
	if contents[0].Type != llm.ContentTypeThinking {
		t.Errorf("contents[0].Type = %v, want Thinking", contents[0].Type)
	}
	if contents[1].Type != llm.ContentTypeToolUse || contents[1].ToolName != "get_weather" {
		t.Errorf("contents[1] = %+v", contents[1])
	}
}

func TestFromLLMMessageHoistsThinkingToReasoningContent(t *testing.T) {
	msg := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{Type: llm.ContentTypeThinking, Thinking: "Step 1: figure out the date."},
			{Type: llm.ContentTypeToolUse, ID: "call_1", ToolName: "get_date", ToolInput: []byte(`{}`)},
		},
	}
	msgs := fromLLMMessage(msg)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	m := msgs[0]
	if m.ReasoningContent != "Step 1: figure out the date." {
		t.Errorf("m.ReasoningContent = %q", m.ReasoningContent)
	}
	if len(m.ToolCalls) != 1 || m.ToolCalls[0].Function.Name != "get_date" {
		t.Errorf("tool calls: %+v", m.ToolCalls)
	}
	// Thinking should NOT have leaked into Content.
	if m.Content != "" {
		t.Errorf("m.Content = %q, want empty", m.Content)
	}
}

// rewriteHostTransport forwards requests to a fixed addr while preserving the
// original Host on the request, so URL-based provider detection still triggers.
type rewriteHostTransport struct{ addr string }

func (r rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = r.addr
	req.Host = r.addr
	return http.DefaultTransport.RoundTrip(req)
}

func TestServiceDoDeepSeekRoundTripsReasoningContent(t *testing.T) {
	// Round-trip scenario: user -> assistant(thinking + tool_call) -> tool_result.
	// The outgoing request to DeepSeek must echo the prior assistant's
	// reasoning_content (not a placeholder) so the model can continue its
	// chain of thought.
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		resp := openai.ChatCompletionResponse{
			ID: "x", Model: "deepseek-v4-pro",
			Choices: []openai.ChatCompletionChoice{{
				Message:      openai.ChatCompletionMessage{Role: "assistant", Content: "ok"},
				FinishReason: "stop",
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	u, _ := url.Parse(server.URL)
	httpc := &http.Client{Transport: rewriteHostTransport{addr: u.Host}}
	svc := &Service{
		APIKey:   "k",
		Model:    Model{ModelName: "deepseek-v4-pro"},
		ModelURL: "https://api.deepseek.com",
		HTTPC:    httpc,
	}

	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "weather?"}}},
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{
				{Type: llm.ContentTypeThinking, Thinking: "I should call the weather tool."},
				{Type: llm.ContentTypeToolUse, ID: "call_1", ToolName: "get_weather", ToolInput: []byte(`{"city":"Paris"}`)},
			}},
			{Role: llm.MessageRoleUser, Content: []llm.Content{{
				Type: llm.ContentTypeToolResult, ToolUseID: "call_1",
				ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "sunny"}},
			}}},
		},
	}
	if _, err := svc.Do(context.Background(), req); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if !strings.Contains(string(gotBody), `"reasoning_content":"I should call the weather tool."`) {
		t.Errorf("expected real reasoning_content in request body, got: %s", gotBody)
	}
}

func TestServiceDoDeepSeekPlaceholderWhenNoThinking(t *testing.T) {
	// Legacy/replayed assistant messages without a thinking block still need
	// a reasoning_content field on tool_calls or DeepSeek returns 400.
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		resp := openai.ChatCompletionResponse{ID: "x", Choices: []openai.ChatCompletionChoice{{
			Message: openai.ChatCompletionMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop",
		}}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	u, _ := url.Parse(server.URL)
	httpc := &http.Client{Transport: rewriteHostTransport{addr: u.Host}}
	svc := &Service{
		APIKey:   "k",
		Model:    Model{ModelName: "deepseek-v4-pro"},
		ModelURL: "https://api.deepseek.com",
		HTTPC:    httpc,
	}

	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{{
				Type: llm.ContentTypeToolUse, ID: "call_1", ToolName: "x", ToolInput: []byte(`{}`),
			}}},
			{Role: llm.MessageRoleUser, Content: []llm.Content{{
				Type: llm.ContentTypeToolResult, ToolUseID: "call_1",
				ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "r"}},
			}}},
		},
	}
	if _, err := svc.Do(context.Background(), req); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if !strings.Contains(string(gotBody), `"reasoning_content"`) {
		t.Errorf("expected reasoning_content placeholder in body, got: %s", gotBody)
	}
}

func TestServiceDoNonDeepSeekStripsReasoningContent(t *testing.T) {
	// reasoning_content is a DeepSeek extension. Don't forward it to OpenAI
	// (or anyone else) — they may reject it or silently misinterpret it.
	// This also covers the case of a gateway URL like
	// https://gateway.exe.dev/_/gateway/openai/v1 which is NOT DeepSeek.
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		resp := openai.ChatCompletionResponse{ID: "x", Choices: []openai.ChatCompletionChoice{{
			Message: openai.ChatCompletionMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop",
		}}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	svc := &Service{APIKey: "k", Model: GPT41, ModelURL: server.URL + "/v1"}
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{
				{Type: llm.ContentTypeThinking, Thinking: "some leftover CoT from a deepseek session"},
				{Type: llm.ContentTypeToolUse, ID: "call_1", ToolName: "x", ToolInput: []byte(`{}`)},
			}},
			{Role: llm.MessageRoleUser, Content: []llm.Content{{
				Type: llm.ContentTypeToolResult, ToolUseID: "call_1",
				ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "r"}},
			}}},
		},
	}
	if _, err := svc.Do(context.Background(), req); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if strings.Contains(string(gotBody), `"reasoning_content"`) {
		t.Errorf("non-deepseek request should not include reasoning_content; body: %s", gotBody)
	}
}

// TestServiceReasoningEffort verifies that Service emits the right
// reasoning_effort field across precedence (request override beats service
// verbatim beats service default).
func TestServiceReasoningEffort(t *testing.T) {
	tests := []struct {
		name       string
		svcLevel   llm.ThinkingLevel
		svcEffort  string
		reqLevel   llm.ThinkingLevel
		wantEffort string // "" means absent
	}{
		{name: "all zero", wantEffort: ""},
		{name: "svc default medium", svcLevel: llm.ThinkingLevelMedium, wantEffort: "medium"},
		{name: "svc high", svcLevel: llm.ThinkingLevelHigh, wantEffort: "high"},
		{name: "svc xhigh clamped to high", svcLevel: llm.ThinkingLevelXHigh, wantEffort: "high"},
		{name: "svc off, svc verbatim wins", svcLevel: llm.ThinkingLevelOff, svcEffort: "verbatim", wantEffort: "verbatim"},
		{name: "req override beats svc default", svcLevel: llm.ThinkingLevelMedium, reqLevel: llm.ThinkingLevelLow, wantEffort: "low"},
		{name: "req off wins", svcLevel: llm.ThinkingLevelMedium, svcEffort: "v", reqLevel: llm.ThinkingLevelOff, wantEffort: ""},
		{name: "req xhigh beats svc verbatim, clamped to high", svcLevel: llm.ThinkingLevelMedium, svcEffort: "v", reqLevel: llm.ThinkingLevelXHigh, wantEffort: "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotEffort string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req openai.ChatCompletionRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatalf("decode: %v", err)
				}
				gotEffort = req.ReasoningEffort
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
					Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
				})
			}))
			defer server.Close()

			svc := &Service{
				APIKey:          "k",
				Model:           GPT41,
				ModelURL:        server.URL + "/v1",
				ThinkingLevel:   tt.svcLevel,
				ReasoningEffort: tt.svcEffort,
			}
			_, err := svc.Do(context.Background(), &llm.Request{
				Messages:      []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}},
				ThinkingLevel: tt.reqLevel,
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if gotEffort != tt.wantEffort {
				t.Errorf("reasoning_effort = %q, want %q", gotEffort, tt.wantEffort)
			}
		})
	}
}
