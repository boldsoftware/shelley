package ant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

func TestIsClaudeModel(t *testing.T) {
	tests := []struct {
		name     string
		userName string
		want     bool
	}{
		{"claude model", "claude", true},
		{"sonnet model", "sonnet", true},
		{"opus model", "opus", true},
		{"unknown model", "gpt-4", false},
		{"empty string", "", false},
		{"random string", "random", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsClaudeModel(tt.userName); got != tt.want {
				t.Errorf("IsClaudeModel(%q) = %v, want %v", tt.userName, got, tt.want)
			}
		})
	}
}

func TestClaudeModelName(t *testing.T) {
	tests := []struct {
		name     string
		userName string
		want     string
	}{
		{"claude model", "claude", Claude45Sonnet},
		{"sonnet model", "sonnet", Claude45Sonnet},
		{"opus model", "opus", Claude45Opus},
		{"unknown model", "gpt-4", ""},
		{"empty string", "", ""},
		{"random string", "random", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClaudeModelName(tt.userName); got != tt.want {
				t.Errorf("ClaudeModelName(%q) = %v, want %v", tt.userName, got, tt.want)
			}
		})
	}
}

func TestTokenContextWindow(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  int
	}{
		{"default model", "", 200000},
		{"Claude37Sonnet", Claude37Sonnet, 200000},
		{"Claude4Sonnet", Claude4Sonnet, 200000},
		{"Claude45Sonnet", Claude45Sonnet, 200000},
		{"Claude45Haiku", Claude45Haiku, 200000},
		{"Claude45Opus", Claude45Opus, 200000},
		{"unknown model", "unknown-model", 200000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{Model: tt.model}
			if got := s.TokenContextWindow(); got != tt.want {
				t.Errorf("TokenContextWindow() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMaxImageDimension(t *testing.T) {
	s := &Service{}
	want := 2000
	if got := s.MaxImageDimension(); got != want {
		t.Errorf("MaxImageDimension() = %v, want %v", got, want)
	}
}

func TestToLLMUsage(t *testing.T) {
	tests := []struct {
		name string
		u    usage
		want llm.Usage
	}{
		{
			name: "empty usage",
			u:    usage{},
			want: llm.Usage{},
		},
		{
			name: "full usage",
			u: usage{
				InputTokens:              100,
				CacheCreationInputTokens: 50,
				CacheReadInputTokens:     25,
				OutputTokens:             200,
				CostUSD:                  0.05,
			},
			want: llm.Usage{
				InputTokens:              100,
				CacheCreationInputTokens: 50,
				CacheReadInputTokens:     25,
				OutputTokens:             200,
				CostUSD:                  0.05,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toLLMUsage(tt.u)
			if got != tt.want {
				t.Errorf("toLLMUsage() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestToLLMContent(t *testing.T) {
	text := "hello world"
	tests := []struct {
		name string
		c    content
		want llm.Content
	}{
		{
			name: "text content",
			c: content{
				Type: "text",
				Text: &text,
			},
			want: llm.Content{
				Type: llm.ContentTypeText,
				Text: "hello world",
			},
		},
		{
			name: "thinking content",
			c: content{
				Type:      "thinking",
				Thinking:  "thinking content",
				Signature: "signature",
			},
			want: llm.Content{
				Type:      llm.ContentTypeThinking,
				Thinking:  "thinking content",
				Signature: "signature",
			},
		},
		{
			name: "redacted thinking content",
			c: content{
				Type:      "redacted_thinking",
				Data:      "redacted data",
				Signature: "signature",
			},
			want: llm.Content{
				Type:      llm.ContentTypeRedactedThinking,
				Data:      "redacted data",
				Signature: "signature",
			},
		},
		{
			name: "tool use content",
			c: content{
				Type:      "tool_use",
				ID:        "tool-id",
				ToolName:  "bash",
				ToolInput: json.RawMessage(`{"command":"ls"}`),
			},
			want: llm.Content{
				Type:      llm.ContentTypeToolUse,
				ID:        "tool-id",
				ToolName:  "bash",
				ToolInput: json.RawMessage(`{"command":"ls"}`),
			},
		},
		{
			name: "tool result content",
			c: content{
				Type:      "tool_result",
				ToolUseID: "tool-use-id",
				ToolError: true,
			},
			want: llm.Content{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-use-id",
				ToolError: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toLLMContent(tt.c)
			if got.Type != tt.want.Type {
				t.Errorf("toLLMContent().Type = %v, want %v", got.Type, tt.want.Type)
			}
			if got.Text != tt.want.Text {
				t.Errorf("toLLMContent().Text = %v, want %v", got.Text, tt.want.Text)
			}
			if got.Thinking != tt.want.Thinking {
				t.Errorf("toLLMContent().Thinking = %v, want %v", got.Thinking, tt.want.Thinking)
			}
			if got.Signature != tt.want.Signature {
				t.Errorf("toLLMContent().Signature = %v, want %v", got.Signature, tt.want.Signature)
			}
			if got.Data != tt.want.Data {
				t.Errorf("toLLMContent().Data = %v, want %v", got.Data, tt.want.Data)
			}
			if got.ID != tt.want.ID {
				t.Errorf("toLLMContent().ID = %v, want %v", got.ID, tt.want.ID)
			}
			if got.ToolName != tt.want.ToolName {
				t.Errorf("toLLMContent().ToolName = %v, want %v", got.ToolName, tt.want.ToolName)
			}
			if string(got.ToolInput) != string(tt.want.ToolInput) {
				t.Errorf("toLLMContent().ToolInput = %v, want %v", string(got.ToolInput), string(tt.want.ToolInput))
			}
			if got.ToolUseID != tt.want.ToolUseID {
				t.Errorf("toLLMContent().ToolUseID = %v, want %v", got.ToolUseID, tt.want.ToolUseID)
			}
			if got.ToolError != tt.want.ToolError {
				t.Errorf("toLLMContent().ToolError = %v, want %v", got.ToolError, tt.want.ToolError)
			}
		})
	}
}

func TestToLLMResponse(t *testing.T) {
	text := "Hello, world!"
	resp := &response{
		ID:         "msg_123",
		Type:       "message",
		Role:       "assistant",
		Model:      Claude45Sonnet,
		Content:    []content{{Type: "text", Text: &text}},
		StopReason: "end_turn",
		Usage: usage{
			InputTokens:  100,
			OutputTokens: 50,
			CostUSD:      0.01,
		},
	}

	got := toLLMResponse(resp)
	if got.ID != "msg_123" {
		t.Errorf("toLLMResponse().ID = %v, want %v", got.ID, "msg_123")
	}
	if got.Type != "message" {
		t.Errorf("toLLMResponse().Type = %v, want %v", got.Type, "message")
	}
	if got.Role != llm.MessageRoleAssistant {
		t.Errorf("toLLMResponse().Role = %v, want %v", got.Role, llm.MessageRoleAssistant)
	}
	if got.Model != Claude45Sonnet {
		t.Errorf("toLLMResponse().Model = %v, want %v", got.Model, Claude45Sonnet)
	}
	if len(got.Content) != 1 {
		t.Errorf("toLLMResponse().Content length = %v, want %v", len(got.Content), 1)
	}
	if got.Content[0].Type != llm.ContentTypeText {
		t.Errorf("toLLMResponse().Content[0].Type = %v, want %v", got.Content[0].Type, llm.ContentTypeText)
	}
	if got.Content[0].Text != "Hello, world!" {
		t.Errorf("toLLMResponse().Content[0].Text = %v, want %v", got.Content[0].Text, "Hello, world!")
	}
	if got.StopReason != llm.StopReasonEndTurn {
		t.Errorf("toLLMResponse().StopReason = %v, want %v", got.StopReason, llm.StopReasonEndTurn)
	}
	if got.Usage.InputTokens != 100 {
		t.Errorf("toLLMResponse().Usage.InputTokens = %v, want %v", got.Usage.InputTokens, 100)
	}
	if got.Usage.OutputTokens != 50 {
		t.Errorf("toLLMResponse().Usage.OutputTokens = %v, want %v", got.Usage.OutputTokens, 50)
	}
	if got.Usage.CostUSD != 0.01 {
		t.Errorf("toLLMResponse().Usage.CostUSD = %v, want %v", got.Usage.CostUSD, 0.01)
	}
}

func TestFromLLMToolUse(t *testing.T) {
	tests := []struct {
		name string
		tu   *llm.ToolUse
		want *toolUse
	}{
		{
			name: "nil tool use",
			tu:   nil,
			want: nil,
		},
		{
			name: "valid tool use",
			tu: &llm.ToolUse{
				ID:   "tool-id",
				Name: "bash",
			},
			want: &toolUse{
				ID:   "tool-id",
				Name: "bash",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fromLLMToolUse(tt.tu)
			if tt.want == nil && got != nil {
				t.Errorf("fromLLMToolUse() = %v, want nil", got)
			} else if tt.want != nil && got == nil {
				t.Errorf("fromLLMToolUse() = nil, want %v", tt.want)
			} else if tt.want != nil && got != nil {
				if got.ID != tt.want.ID || got.Name != tt.want.Name {
					t.Errorf("fromLLMToolUse() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestFromLLMMessage(t *testing.T) {
	text := "Hello, world!"
	msg := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{
				Type: llm.ContentTypeText,
				Text: text,
			},
		},
		ToolUse: &llm.ToolUse{
			ID:   "tool-id",
			Name: "bash",
		},
	}

	got := fromLLMMessage(msg)
	if got.Role != "assistant" {
		t.Errorf("fromLLMMessage().Role = %v, want %v", got.Role, "assistant")
	}
	if len(got.Content) != 1 {
		t.Errorf("fromLLMMessage().Content length = %v, want %v", len(got.Content), 1)
	}
	if got.Content[0].Type != "text" {
		t.Errorf("fromLLMMessage().Content[0].Type = %v, want %v", got.Content[0].Type, "text")
	}
	if *got.Content[0].Text != text {
		t.Errorf("fromLLMMessage().Content[0].Text = %v, want %v", *got.Content[0].Text, text)
	}
	if got.ToolUse == nil {
		t.Errorf("fromLLMMessage().ToolUse = nil, want not nil")
	} else {
		if got.ToolUse.ID != "tool-id" {
			t.Errorf("fromLLMMessage().ToolUse.ID = %v, want %v", got.ToolUse.ID, "tool-id")
		}
		if got.ToolUse.Name != "bash" {
			t.Errorf("fromLLMMessage().ToolUse.Name = %v, want %v", got.ToolUse.Name, "bash")
		}
	}
}

func TestFromLLMToolChoice(t *testing.T) {
	tests := []struct {
		name string
		tc   *llm.ToolChoice
		want *toolChoice
	}{
		{
			name: "nil tool choice",
			tc:   nil,
			want: nil,
		},
		{
			name: "auto tool choice",
			tc: &llm.ToolChoice{
				Type: llm.ToolChoiceTypeAuto,
			},
			want: &toolChoice{
				Type: "auto",
			},
		},
		{
			name: "tool tool choice",
			tc: &llm.ToolChoice{
				Type: llm.ToolChoiceTypeTool,
				Name: "bash",
			},
			want: &toolChoice{
				Type: "tool",
				Name: "bash",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fromLLMToolChoice(tt.tc)
			if tt.want == nil && got != nil {
				t.Errorf("fromLLMToolChoice() = %v, want nil", got)
			} else if tt.want != nil && got == nil {
				t.Errorf("fromLLMToolChoice() = nil, want %v", tt.want)
			} else if tt.want != nil && got != nil {
				if got.Type != tt.want.Type {
					t.Errorf("fromLLMToolChoice().Type = %v, want %v", got.Type, tt.want.Type)
				}
				if got.Name != tt.want.Name {
					t.Errorf("fromLLMToolChoice().Name = %v, want %v", got.Name, tt.want.Name)
				}
			}
		})
	}
}

func TestFromLLMTool(t *testing.T) {
	tool := &llm.Tool{
		Name:        "bash",
		Description: "Execute bash commands",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Cache:       true,
	}

	got := fromLLMTool(tool)
	if got.Name != "bash" {
		t.Errorf("fromLLMTool().Name = %v, want %v", got.Name, "bash")
	}
	if got.Description != "Execute bash commands" {
		t.Errorf("fromLLMTool().Description = %v, want %v", got.Description, "Execute bash commands")
	}
	if string(got.InputSchema) != `{"type":"object"}` {
		t.Errorf("fromLLMTool().InputSchema = %v, want %v", string(got.InputSchema), `{"type":"object"}`)
	}
	if string(got.CacheControl) != `{"type":"ephemeral"}` {
		t.Errorf("fromLLMTool().CacheControl = %v, want %v", string(got.CacheControl), `{"type":"ephemeral"}`)
	}
}

func TestFromLLMSystem(t *testing.T) {
	sys := llm.SystemContent{
		Text:  "You are a helpful assistant",
		Type:  "text",
		Cache: true,
	}

	got := fromLLMSystem(sys)
	if got.Text != "You are a helpful assistant" {
		t.Errorf("fromLLMSystem().Text = %v, want %v", got.Text, "You are a helpful assistant")
	}
	if got.Type != "text" {
		t.Errorf("fromLLMSystem().Type = %v, want %v", got.Type, "text")
	}
	if string(got.CacheControl) != `{"type":"ephemeral"}` {
		t.Errorf("fromLLMSystem().CacheControl = %v, want %v", string(got.CacheControl), `{"type":"ephemeral"}`)
	}
}

func TestMapped(t *testing.T) {
	// Test the mapped function with a simple example
	input := []int{1, 2, 3, 4, 5}
	expected := []int{2, 4, 6, 8, 10}

	got := mapped(input, func(x int) int { return x * 2 })

	if len(got) != len(expected) {
		t.Errorf("mapped() length = %v, want %v", len(got), len(expected))
	}

	for i, v := range got {
		if v != expected[i] {
			t.Errorf("mapped()[%d] = %v, want %v", i, v, expected[i])
		}
	}
}

func TestUsageAdd(t *testing.T) {
	u1 := usage{
		InputTokens:              100,
		CacheCreationInputTokens: 50,
		CacheReadInputTokens:     25,
		OutputTokens:             200,
		CostUSD:                  0.05,
	}

	u2 := usage{
		InputTokens:              150,
		CacheCreationInputTokens: 75,
		CacheReadInputTokens:     30,
		OutputTokens:             300,
		CostUSD:                  0.07,
	}

	u1.Add(u2)

	if u1.InputTokens != 250 {
		t.Errorf("usage.Add() InputTokens = %v, want %v", u1.InputTokens, 250)
	}
	if u1.CacheCreationInputTokens != 125 {
		t.Errorf("usage.Add() CacheCreationInputTokens = %v, want %v", u1.CacheCreationInputTokens, 125)
	}
	if u1.CacheReadInputTokens != 55 {
		t.Errorf("usage.Add() CacheReadInputTokens = %v, want %v", u1.CacheReadInputTokens, 55)
	}
	if u1.OutputTokens != 500 {
		t.Errorf("usage.Add() OutputTokens = %v, want %v", u1.OutputTokens, 500)
	}

	// Use a small epsilon for floating point comparison
	const epsilon = 1e-10
	expectedCost := 0.12
	if abs(u1.CostUSD-expectedCost) > epsilon {
		t.Errorf("usage.Add() CostUSD = %v, want %v", u1.CostUSD, expectedCost)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestFromLLMRequest(t *testing.T) {
	s := &Service{
		Model:     Claude45Sonnet,
		MaxTokens: 1000,
	}

	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "Hello, world!",
					},
				},
			},
		},
		ToolChoice: &llm.ToolChoice{
			Type: llm.ToolChoiceTypeAuto,
		},
		Tools: []*llm.Tool{
			{
				Name:        "bash",
				Description: "Execute bash commands",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
		System: []llm.SystemContent{
			{
				Text: "You are a helpful assistant",
			},
		},
	}

	got := s.fromLLMRequest(req)

	if got.Model != Claude45Sonnet {
		t.Errorf("fromLLMRequest().Model = %v, want %v", got.Model, Claude45Sonnet)
	}
	if got.MaxTokens != 1000 {
		t.Errorf("fromLLMRequest().MaxTokens = %v, want %v", got.MaxTokens, 1000)
	}
	if len(got.Messages) != 1 {
		t.Errorf("fromLLMRequest().Messages length = %v, want %v", len(got.Messages), 1)
	}
	if got.ToolChoice == nil {
		t.Errorf("fromLLMRequest().ToolChoice = nil, want not nil")
	} else if got.ToolChoice.Type != "auto" {
		t.Errorf("fromLLMRequest().ToolChoice.Type = %v, want %v", got.ToolChoice.Type, "auto")
	}
	if len(got.Tools) != 1 {
		t.Errorf("fromLLMRequest().Tools length = %v, want %v", len(got.Tools), 1)
	} else if got.Tools[0].Name != "bash" {
		t.Errorf("fromLLMRequest().Tools[0].Name = %v, want %v", got.Tools[0].Name, "bash")
	}
	if len(got.System) != 1 {
		t.Errorf("fromLLMRequest().System length = %v, want %v", len(got.System), 1)
	} else if got.System[0].Text != "You are a helpful assistant" {
		t.Errorf("fromLLMRequest().System[0].Text = %v, want %v", got.System[0].Text, "You are a helpful assistant")
	}
}

func TestConfigDetails(t *testing.T) {
	tests := []struct {
		name    string
		service *Service
		want    map[string]string
	}{
		{
			name: "default values",
			service: &Service{
				APIKey: "test-key",
			},
			want: map[string]string{
				"url":             DefaultURL,
				"model":           DefaultModel,
				"has_api_key_set": "true",
			},
		},
		{
			name: "custom values",
			service: &Service{
				URL:    "https://custom.anthropic.com/v1/messages",
				Model:  Claude45Opus,
				APIKey: "test-key",
			},
			want: map[string]string{
				"url":             "https://custom.anthropic.com/v1/messages",
				"model":           Claude45Opus,
				"has_api_key_set": "true",
			},
		},
		{
			name: "no api key",
			service: &Service{
				APIKey: "",
			},
			want: map[string]string{
				"url":             DefaultURL,
				"model":           DefaultModel,
				"has_api_key_set": "false",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.service.ConfigDetails()
			for key, wantValue := range tt.want {
				if gotValue, ok := got[key]; !ok {
					t.Errorf("ConfigDetails() missing key %q", key)
				} else if gotValue != wantValue {
					t.Errorf("ConfigDetails()[%q] = %v, want %v", key, gotValue, wantValue)
				}
			}
		})
	}
}

func TestDo(t *testing.T) {
	// Create a mock HTTP client that returns a predefined response
	mockResponse := `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5-20250929",
		"content": [
			{
				"type": "text",
				"text": "Hello, world!"
			}
		],
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cost_usd": 0.01
		}
	}`

	// Create a service with a mock HTTP client
	client := &http.Client{
		Transport: &mockHTTPTransport{responseBody: mockResponse, statusCode: 200},
	}

	s := &Service{
		APIKey: "test-key",
		HTTPC:  client,
	}

	// Create a request
	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "Hello, Claude!",
					},
				},
			},
		},
	}

	// Call Do
	resp, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}

	// Check the response
	if resp == nil {
		t.Fatalf("Do() response = nil, want not nil")
	}
	if resp.ID != "msg_123" {
		t.Errorf("Do() response ID = %v, want %v", resp.ID, "msg_123")
	}
	if resp.Role != llm.MessageRoleAssistant {
		t.Errorf("Do() response Role = %v, want %v", resp.Role, llm.MessageRoleAssistant)
	}
	if len(resp.Content) != 1 {
		t.Errorf("Do() response Content length = %v, want %v", len(resp.Content), 1)
	} else if resp.Content[0].Text != "Hello, world!" {
		t.Errorf("Do() response Content[0].Text = %v, want %v", resp.Content[0].Text, "Hello, world!")
	}
	if resp.Usage.InputTokens != 100 {
		t.Errorf("Do() response Usage.InputTokens = %v, want %v", resp.Usage.InputTokens, 100)
	}
	if resp.Usage.OutputTokens != 50 {
		t.Errorf("Do() response Usage.OutputTokens = %v, want %v", resp.Usage.OutputTokens, 50)
	}
}

// mockHTTPTransport is a mock HTTP transport for testing
type mockHTTPTransport struct {
	responseBody string
	statusCode   int
}

func (m *mockHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp := &http.Response{
		StatusCode: m.statusCode,
		Body:       io.NopCloser(strings.NewReader(m.responseBody)),
		Header:     make(http.Header),
	}
	resp.Header.Set("content-type", "application/json")
	return resp, nil
}

func TestFromLLMContent(t *testing.T) {
	text := "hello world"
	toolInput := json.RawMessage(`{"command":"ls"}`)

	tests := []struct {
		name string
		c    llm.Content
		want content
	}{
		{
			name: "text content",
			c: llm.Content{
				Type: llm.ContentTypeText,
				Text: "hello world",
			},
			want: content{
				Type: "text",
				Text: &text,
			},
		},
		{
			name: "thinking content",
			c: llm.Content{
				Type:      llm.ContentTypeThinking,
				Thinking:  "thinking content",
				Signature: "signature",
			},
			want: content{
				Type:      "thinking",
				Thinking:  "thinking content",
				Signature: "signature",
			},
		},
		{
			name: "redacted thinking content",
			c: llm.Content{
				Type:      llm.ContentTypeRedactedThinking,
				Data:      "redacted data",
				Signature: "signature",
			},
			want: content{
				Type:      "redacted_thinking",
				Data:      "redacted data",
				Signature: "signature",
			},
		},
		{
			name: "tool use content",
			c: llm.Content{
				Type:      llm.ContentTypeToolUse,
				ID:        "tool-id",
				ToolName:  "bash",
				ToolInput: toolInput,
			},
			want: content{
				Type:      "tool_use",
				ID:        "tool-id",
				ToolName:  "bash",
				ToolInput: toolInput,
			},
		},
		{
			name: "tool result content",
			c: llm.Content{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-use-id",
				ToolError: true,
			},
			want: content{
				Type:      "tool_result",
				ToolUseID: "tool-use-id",
				ToolError: true,
			},
		},
		{
			name: "image content as text",
			c: llm.Content{
				Type:      llm.ContentTypeText,
				MediaType: "image/jpeg",
				Data:      "base64image",
			},
			want: content{
				Type:   "image",
				Source: json.RawMessage(`{"type":"base64","media_type":"image/jpeg","data":"base64image"}`),
			},
		},
		{
			name: "tool result with nested content",
			c: llm.Content{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-use-id",
				ToolResult: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "nested text",
					},
				},
			},
			want: content{
				Type:      "tool_result",
				ToolUseID: "tool-use-id",
				ToolResult: []content{
					{
						Type: "text",
						Text: &[]string{"nested text"}[0],
					},
				},
			},
		},
		{
			name: "tool result with nested image content",
			c: llm.Content{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-use-id",
				ToolResult: []llm.Content{
					{
						Type:      llm.ContentTypeText,
						MediaType: "image/png",
						Data:      "base64image",
					},
				},
			},
			want: content{
				Type:      "tool_result",
				ToolUseID: "tool-use-id",
				ToolResult: []content{
					{
						Type:   "image",
						Source: json.RawMessage(`{"type":"base64","media_type":"image/png","data":"base64image"}`),
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fromLLMContent(tt.c)

			// Compare basic fields
			if got.Type != tt.want.Type {
				t.Errorf("fromLLMContent().Type = %v, want %v", got.Type, tt.want.Type)
			}

			if got.ID != tt.want.ID {
				t.Errorf("fromLLMContent().ID = %v, want %v", got.ID, tt.want.ID)
			}

			if got.Thinking != tt.want.Thinking {
				t.Errorf("fromLLMContent().Thinking = %v, want %v", got.Thinking, tt.want.Thinking)
			}

			if got.Signature != tt.want.Signature {
				t.Errorf("fromLLMContent().Signature = %v, want %v", got.Signature, tt.want.Signature)
			}

			if got.Data != tt.want.Data {
				t.Errorf("fromLLMContent().Data = %v, want %v", got.Data, tt.want.Data)
			}

			if got.ToolName != tt.want.ToolName {
				t.Errorf("fromLLMContent().ToolName = %v, want %v", got.ToolName, tt.want.ToolName)
			}

			if string(got.ToolInput) != string(tt.want.ToolInput) {
				t.Errorf("fromLLMContent().ToolInput = %v, want %v", string(got.ToolInput), string(tt.want.ToolInput))
			}

			if got.ToolUseID != tt.want.ToolUseID {
				t.Errorf("fromLLMContent().ToolUseID = %v, want %v", got.ToolUseID, tt.want.ToolUseID)
			}

			if got.ToolError != tt.want.ToolError {
				t.Errorf("fromLLMContent().ToolError = %v, want %v", got.ToolError, tt.want.ToolError)
			}

			// Compare text field
			if tt.want.Text != nil {
				if got.Text == nil {
					t.Errorf("fromLLMContent().Text = nil, want %v", *tt.want.Text)
				} else if *got.Text != *tt.want.Text {
					t.Errorf("fromLLMContent().Text = %v, want %v", *got.Text, *tt.want.Text)
				}
			} else if got.Text != nil {
				t.Errorf("fromLLMContent().Text = %v, want nil", *got.Text)
			}

			// Compare source field (for image content)
			if len(tt.want.Source) > 0 {
				if string(got.Source) != string(tt.want.Source) {
					t.Errorf("fromLLMContent().Source = %v, want %v", string(got.Source), string(tt.want.Source))
				}
			}

			// Compare tool result length
			if len(got.ToolResult) != len(tt.want.ToolResult) {
				t.Errorf("fromLLMContent().ToolResult length = %v, want %v", len(got.ToolResult), len(tt.want.ToolResult))
			} else if len(tt.want.ToolResult) > 0 {
				// Compare each tool result item
				for i, tr := range tt.want.ToolResult {
					if got.ToolResult[i].Type != tr.Type {
						t.Errorf("fromLLMContent().ToolResult[%d].Type = %v, want %v", i, got.ToolResult[i].Type, tr.Type)
					}
					if tr.Text != nil {
						if got.ToolResult[i].Text == nil {
							t.Errorf("fromLLMContent().ToolResult[%d].Text = nil, want %v", i, *tr.Text)
						} else if *got.ToolResult[i].Text != *tr.Text {
							t.Errorf("fromLLMContent().ToolResult[%d].Text = %v, want %v", i, *got.ToolResult[i].Text, *tr.Text)
						}
					}
					if len(tr.Source) > 0 {
						if string(got.ToolResult[i].Source) != string(tr.Source) {
							t.Errorf("fromLLMContent().ToolResult[%d].Source = %v, want %v", i, string(got.ToolResult[i].Source), string(tr.Source))
						}
					}
				}
			}
		})
	}
}

func TestInverted(t *testing.T) {
	// Test normal case
	m := map[string]int{
		"a": 1,
		"b": 2,
		"c": 3,
	}

	want := map[int]string{
		1: "a",
		2: "b",
		3: "c",
	}

	got := inverted(m)

	if len(got) != len(want) {
		t.Errorf("inverted() length = %v, want %v", len(got), len(want))
	}

	for k, v := range want {
		if gotV, ok := got[k]; !ok {
			t.Errorf("inverted() missing key %v", k)
		} else if gotV != v {
			t.Errorf("inverted()[%v] = %v, want %v", k, gotV, v)
		}
	}

	// Test panic case with duplicate values
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("inverted() should panic with duplicate values")
		}
	}()

	m2 := map[string]int{
		"a": 1,
		"b": 1, // duplicate value
	}

	inverted(m2)
}

func TestToLLMContentWithNestedToolResults(t *testing.T) {
	text := "nested text"
	nestedContent := content{
		Type: "text",
		Text: &text,
	}

	c := content{
		Type:      "tool_result",
		ToolUseID: "tool-use-id",
		ToolResult: []content{
			nestedContent,
		},
	}

	got := toLLMContent(c)

	if got.Type != llm.ContentTypeToolResult {
		t.Errorf("toLLMContent().Type = %v, want %v", got.Type, llm.ContentTypeToolResult)
	}

	if got.ToolUseID != "tool-use-id" {
		t.Errorf("toLLMContent().ToolUseID = %v, want %v", got.ToolUseID, "tool-use-id")
	}

	if len(got.ToolResult) != 1 {
		t.Errorf("toLLMContent().ToolResult length = %v, want %v", len(got.ToolResult), 1)
	} else {
		if got.ToolResult[0].Type != llm.ContentTypeText {
			t.Errorf("toLLMContent().ToolResult[0].Type = %v, want %v", got.ToolResult[0].Type, llm.ContentTypeText)
		}
		if got.ToolResult[0].Text != "nested text" {
			t.Errorf("toLLMContent().ToolResult[0].Text = %v, want %v", got.ToolResult[0].Text, "nested text")
		}
	}
}

func TestDoWithHTTPRecorder(t *testing.T) {
	// Create a mock HTTP client that returns a predefined response
	mockResponse := `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5-20250929",
		"content": [
			{
				"type": "text",
				"text": "Hello, world!"
			}
		],
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cost_usd": 0.01
		}
	}`

	// Variables to capture HTTPRecorder calls
	var recorded bool
	var recordedURL string
	var recordedStatusCode int

	// Create a service with a mock HTTP client and HTTPRecorder
	client := &http.Client{
		Transport: &mockHTTPTransport{responseBody: mockResponse, statusCode: 200},
	}

	s := &Service{
		APIKey: "test-key",
		HTTPC:  client,
		HTTPRecorder: func(url string, payload, response []byte, statusCode int, err error, duration time.Duration) {
			recorded = true
			recordedURL = url
			recordedStatusCode = statusCode
		},
	}

	// Create a request
	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "Hello, Claude!",
					},
				},
			},
		},
	}

	// Call Do
	resp, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}

	// Check the response
	if resp == nil {
		t.Fatalf("Do() response = nil, want not nil")
	}

	// Check that HTTPRecorder was called
	if !recorded {
		t.Error("HTTPRecorder was not called")
	}

	if recordedURL == "" {
		t.Error("HTTPRecorder did not record URL")
	}

	if recordedStatusCode != 200 {
		t.Errorf("HTTPRecorder recordedStatusCode = %v, want %v", recordedStatusCode, 200)
	}
}

func TestDoClientError(t *testing.T) {
	// Create a mock HTTP client that returns a client error
	mockResponse := `{"error": "bad request"}`

	// Create a service with a mock HTTP client
	client := &http.Client{
		Transport: &mockHTTPTransport{responseBody: mockResponse, statusCode: 400},
	}

	s := &Service{
		APIKey: "test-key",
		HTTPC:  client,
	}

	// Create a request
	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "Hello, Claude!",
					},
				},
			},
		},
	}

	// Call Do - should fail immediately
	resp, err := s.Do(context.Background(), req)
	if err == nil {
		t.Fatalf("Do() error = nil, want error")
	}

	if resp != nil {
		t.Errorf("Do() response = %v, want nil", resp)
	}
}

func TestDoWithDumpLLM(t *testing.T) {
	// Create a mock HTTP client that returns a predefined response
	mockResponse := `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5-20250929",
		"content": [
			{
				"type": "text",
				"text": "Hello, world!"
			}
		],
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cost_usd": 0.01
		}
	}`

	// Create a service with a mock HTTP client and DumpLLM enabled
	client := &http.Client{
		Transport: &mockHTTPTransport{responseBody: mockResponse, statusCode: 200},
	}

	s := &Service{
		APIKey:  "test-key",
		HTTPC:   client,
		DumpLLM: true,
	}

	// Create a request
	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "Hello, Claude!",
					},
				},
			},
		},
	}

	// Call Do
	resp, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}

	// Check the response
	if resp == nil {
		t.Fatalf("Do() response = nil, want not nil")
	}
}

func TestServiceConfigDetails(t *testing.T) {
	tests := []struct {
		name    string
		service *Service
		want    map[string]string
	}{
		{
			name: "default values",
			service: &Service{
				APIKey: "test-key",
			},
			want: map[string]string{
				"url":             DefaultURL,
				"model":           DefaultModel,
				"has_api_key_set": "true",
			},
		},
		{
			name: "custom values",
			service: &Service{
				APIKey: "test-key",
				URL:    "https://custom-url.com",
				Model:  "custom-model",
			},
			want: map[string]string{
				"url":             "https://custom-url.com",
				"model":           "custom-model",
				"has_api_key_set": "true",
			},
		},
		{
			name: "empty api key",
			service: &Service{
				APIKey: "",
			},
			want: map[string]string{
				"url":             DefaultURL,
				"model":           DefaultModel,
				"has_api_key_set": "false",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.service.ConfigDetails()

			for key, wantValue := range tt.want {
				if gotValue, ok := got[key]; !ok {
					t.Errorf("ConfigDetails() missing key %v", key)
				} else if gotValue != wantValue {
					t.Errorf("ConfigDetails()[%v] = %v, want %v", key, gotValue, wantValue)
				}
			}
		})
	}
}

func TestDoStartTimeEndTime(t *testing.T) {
	// Create a mock HTTP client that returns a predefined response
	mockResponse := `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5-20250929",
		"content": [
			{
				"type": "text",
				"text": "Hello, world!"
			}
		],
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cost_usd": 0.01
		}
	}`

	// Create a service with a mock HTTP client
	client := &http.Client{
		Transport: &mockHTTPTransport{responseBody: mockResponse, statusCode: 200},
	}

	s := &Service{
		APIKey: "test-key",
		HTTPC:  client,
	}

	// Create a request
	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "Hello, Claude!",
					},
				},
			},
		},
	}

	// Call Do
	resp, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}

	// Check the response
	if resp == nil {
		t.Fatalf("Do() response = nil, want not nil")
	}

	// Check that StartTime and EndTime are set
	if resp.StartTime == nil {
		t.Error("Do() response StartTime = nil, want not nil")
	}

	if resp.EndTime == nil {
		t.Error("Do() response EndTime = nil, want not nil")
	}

	// Check that EndTime is after StartTime
	if resp.StartTime != nil && resp.EndTime != nil {
		if resp.EndTime.Before(*resp.StartTime) {
			t.Error("Do() response EndTime should be after StartTime")
		}
	}
}
