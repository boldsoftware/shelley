package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// mockService implements Service interface for testing
type mockService struct {
	tokenContextWindow   int
	maxImageDimension    int
	useSimplifiedPatch   bool
	implementsSimplified bool
}

func (m *mockService) Do(ctx context.Context, req *Request) (*Response, error) {
	return &Response{}, nil
}

func (m *mockService) TokenContextWindow() int {
	return m.tokenContextWindow
}

func (m *mockService) MaxImageDimension() int {
	return m.maxImageDimension
}

// mockSimplifiedService implements both Service and SimplifiedPatcher interfaces
type mockSimplifiedService struct {
	mockService
}

func (m *mockSimplifiedService) UseSimplifiedPatch() bool {
	return m.useSimplifiedPatch
}

func TestMustSchema(t *testing.T) {
	tests := []struct {
		name        string
		schema      string
		expectPanic bool
	}{
		{
			name:        "valid schema",
			schema:      `{"type": "object", "properties": {}}`,
			expectPanic: false,
		},
		{
			name:        "valid schema with properties",
			schema:      `{"type": "object", "properties": {"name": {"type": "string"}}}`,
			expectPanic: false,
		},
		{
			name:        "invalid json",
			schema:      `{"type": "object", "properties": }`,
			expectPanic: true,
		},
		{
			name:        "missing type",
			schema:      `{"properties": {}}`,
			expectPanic: true,
		},
		{
			name:        "wrong type",
			schema:      `{"type": "string", "properties": {}}`,
			expectPanic: true,
		},
		{
			name:        "missing properties",
			schema:      `{"type": "object"}`,
			expectPanic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.expectPanic {
				defer func() {
					if r := recover(); r == nil {
						t.Errorf("Expected panic for schema: %s", tt.schema)
					}
				}()
			}
			result := MustSchema(tt.schema)
			if !tt.expectPanic {
				if string(result) != tt.schema {
					t.Errorf("MustSchema() = %s, want %s", string(result), tt.schema)
				}
			}
		})
	}
}

func TestEmptySchema(t *testing.T) {
	schema := EmptySchema()
	expected := `{"type": "object", "properties": {}}`
	if string(schema) != expected {
		t.Errorf("EmptySchema() = %s, want %s", string(schema), expected)
	}
}

func TestUseSimplifiedPatch(t *testing.T) {
	tests := []struct {
		name     string
		service  Service
		expected bool
	}{
		{
			name: "service without SimplifiedPatcher",
			service: &mockService{
				implementsSimplified: false,
				useSimplifiedPatch:   false,
			},
			expected: false,
		},
		{
			name: "service with SimplifiedPatcher returning false",
			service: &mockSimplifiedService{
				mockService: mockService{
					implementsSimplified: true,
					useSimplifiedPatch:   false,
				},
			},
			expected: false,
		},
		{
			name: "service with SimplifiedPatcher returning true",
			service: &mockSimplifiedService{
				mockService: mockService{
					implementsSimplified: true,
					useSimplifiedPatch:   true,
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := UseSimplifiedPatch(tt.service)
			if result != tt.expected {
				t.Errorf("UseSimplifiedPatch() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestStringContent(t *testing.T) {
	text := "test content"
	content := StringContent(text)

	if content.Type != ContentTypeText {
		t.Errorf("StringContent().Type = %v, want %v", content.Type, ContentTypeText)
	}

	if content.Text != text {
		t.Errorf("StringContent().Text = %s, want %s", content.Text, text)
	}
}

func TestTextContent(t *testing.T) {
	text := "test text content"
	contents := TextContent(text)

	if len(contents) != 1 {
		t.Errorf("TextContent() returned %d items, want 1", len(contents))
	}

	if contents[0].Type != ContentTypeText {
		t.Errorf("TextContent()[0].Type = %v, want %v", contents[0].Type, ContentTypeText)
	}

	if contents[0].Text != text {
		t.Errorf("TextContent()[0].Text = %s, want %s", contents[0].Text, text)
	}
}

func TestUserStringMessage(t *testing.T) {
	text := "user message"
	message := UserStringMessage(text)

	if message.Role != MessageRoleUser {
		t.Errorf("UserStringMessage().Role = %v, want %v", message.Role, MessageRoleUser)
	}

	if len(message.Content) != 1 {
		t.Errorf("UserStringMessage().Content length = %d, want 1", len(message.Content))
	}

	if message.Content[0].Type != ContentTypeText {
		t.Errorf("UserStringMessage().Content[0].Type = %v, want %v", message.Content[0].Type, ContentTypeText)
	}

	if message.Content[0].Text != text {
		t.Errorf("UserStringMessage().Content[0].Text = %s, want %s", message.Content[0].Text, text)
	}
}

func TestErrorToolOut(t *testing.T) {
	err := fmt.Errorf("test error")
	toolOut := ErrorToolOut(err)

	if toolOut.Error != err {
		t.Errorf("ErrorToolOut().Error = %v, want %v", toolOut.Error, err)
	}

	// Test panic with nil error
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("Expected panic when calling ErrorToolOut with nil error")
		}
	}()
	ErrorToolOut(nil)
}

func TestErrorfToolOut(t *testing.T) {
	format := "error: %s"
	arg := "test"
	toolOut := ErrorfToolOut(format, arg)

	if toolOut.Error == nil {
		t.Errorf("ErrorfToolOut().Error = nil, want error")
	}

	expected := fmt.Sprintf(format, arg)
	if toolOut.Error.Error() != expected {
		t.Errorf("ErrorfToolOut().Error = %v, want %v", toolOut.Error.Error(), expected)
	}
}

func TestUsageAdd(t *testing.T) {
	u1 := Usage{
		InputTokens:              100,
		CacheCreationInputTokens: 50,
		CacheReadInputTokens:     25,
		OutputTokens:             200,
		CostUSD:                  0.01,
	}

	u2 := Usage{
		InputTokens:              150,
		CacheCreationInputTokens: 75,
		CacheReadInputTokens:     30,
		OutputTokens:             100,
		CostUSD:                  0.02,
	}

	u1.Add(u2)

	expected := Usage{
		InputTokens:              250,  // 100 + 150
		CacheCreationInputTokens: 125,  // 50 + 75
		CacheReadInputTokens:     55,   // 25 + 30
		OutputTokens:             300,  // 200 + 100
		CostUSD:                  0.03, // 0.01 + 0.02
	}

	if u1 != expected {
		t.Errorf("Usage.Add() resulted in %v, want %v", u1, expected)
	}
}

func TestUsageString(t *testing.T) {
	tests := []struct {
		name  string
		usage Usage
		want  string
	}{
		{
			name: "normal usage",
			usage: Usage{
				InputTokens:  100,
				OutputTokens: 50,
			},
			want: "in: 100, out: 50",
		},
		{
			name: "zero usage",
			usage: Usage{
				InputTokens:  0,
				OutputTokens: 0,
			},
			want: "in: 0, out: 0",
		},
		{
			name: "high usage",
			usage: Usage{
				InputTokens:  1000000,
				OutputTokens: 500000,
			},
			want: "in: 1000000, out: 500000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.usage.String()
			if result != tt.want {
				t.Errorf("Usage.String() = %s, want %s", result, tt.want)
			}
		})
	}
}

func TestUsageIsZero(t *testing.T) {
	tests := []struct {
		name  string
		usage Usage
		want  bool
	}{
		{
			name:  "zero usage",
			usage: Usage{},
			want:  true,
		},
		{
			name: "non-zero input tokens",
			usage: Usage{
				InputTokens: 1,
			},
			want: false,
		},
		{
			name: "non-zero output tokens",
			usage: Usage{
				OutputTokens: 1,
			},
			want: false,
		},
		{
			name: "non-zero cost",
			usage: Usage{
				CostUSD: 0.01,
			},
			want: false,
		},
		{
			name: "all fields zero",
			usage: Usage{
				InputTokens:              0,
				CacheCreationInputTokens: 0,
				CacheReadInputTokens:     0,
				OutputTokens:             0,
				CostUSD:                  0,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.usage.IsZero()
			if result != tt.want {
				t.Errorf("Usage.IsZero() = %v, want %v", result, tt.want)
			}
		})
	}
}

func TestResponseToMessage(t *testing.T) {
	tests := []struct {
		name          string
		response      Response
		wantRole      MessageRole
		wantEndOfTurn bool
	}{
		{
			name: "tool use stop reason",
			response: Response{
				Role:       MessageRoleAssistant,
				StopReason: StopReasonToolUse,
			},
			wantRole:      MessageRoleAssistant,
			wantEndOfTurn: false,
		},
		{
			name: "end turn stop reason",
			response: Response{
				Role:       MessageRoleAssistant,
				StopReason: StopReasonEndTurn,
			},
			wantRole:      MessageRoleAssistant,
			wantEndOfTurn: true,
		},
		{
			name: "max tokens stop reason",
			response: Response{
				Role:       MessageRoleAssistant,
				StopReason: StopReasonMaxTokens,
			},
			wantRole:      MessageRoleAssistant,
			wantEndOfTurn: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := tt.response.ToMessage()

			if message.Role != tt.wantRole {
				t.Errorf("ToMessage().Role = %v, want %v", message.Role, tt.wantRole)
			}

			if message.EndOfTurn != tt.wantEndOfTurn {
				t.Errorf("ToMessage().EndOfTurn = %v, want %v", message.EndOfTurn, tt.wantEndOfTurn)
			}
		})
	}
}

func TestContentsAttr(t *testing.T) {
	tests := []struct {
		name     string
		contents []Content
	}{
		{
			name: "text content",
			contents: []Content{
				{
					ID:   "1",
					Type: ContentTypeText,
					Text: "hello world",
				},
			},
		},
		{
			name: "tool use content",
			contents: []Content{
				{
					ID:        "2",
					Type:      ContentTypeToolUse,
					ToolName:  "test_tool",
					ToolInput: json.RawMessage(`{"param": "value"}`),
				},
			},
		},
		{
			name: "tool result content",
			contents: []Content{
				{
					ID:         "3",
					Type:       ContentTypeToolResult,
					ToolResult: []Content{{Type: ContentTypeText, Text: "result"}},
					ToolError:  false,
				},
			},
		},
		{
			name: "thinking content",
			contents: []Content{
				{
					ID:   "4",
					Type: ContentTypeThinking,
					Text: "thinking...",
				},
			},
		},
		{
			name:     "empty contents",
			contents: []Content{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attr := ContentsAttr(tt.contents)
			if attr.Key != "contents" {
				t.Errorf("ContentsAttr().Key = %s, want 'contents'", attr.Key)
			}
		})
	}
}

func TestCostUSDFromResponse(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		wantCost float64
	}{
		{
			name: "valid cost header",
			headers: map[string]string{
				"Skaband-Cost-Microcents": "10000000", // 0.1 USD
			},
			wantCost: 0.1,
		},
		{
			name: "invalid cost header",
			headers: map[string]string{
				"Skaband-Cost-Microcents": "invalid",
			},
			wantCost: 0,
		},
		{
			name:     "missing cost header",
			headers:  map[string]string{},
			wantCost: 0,
		},
		{
			name: "empty cost header",
			headers: map[string]string{
				"Skaband-Cost-Microcents": "",
			},
			wantCost: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			for k, v := range tt.headers {
				headers.Set(k, v)
			}

			cost := CostUSDFromResponse(headers)
			if cost != tt.wantCost {
				t.Errorf("CostUSDFromResponse() = %f, want %f", cost, tt.wantCost)
			}
		})
	}
}

func TestUsageAttr(t *testing.T) {
	usage := Usage{
		InputTokens:              100,
		OutputTokens:             50,
		CacheCreationInputTokens: 25,
		CacheReadInputTokens:     75,
		CostUSD:                  0.01,
	}

	attr := usage.Attr()
	if attr.Key != "usage" {
		t.Errorf("Attr().Key = %s, want 'usage'", attr.Key)
	}
}

func TestDumpToFile(t *testing.T) {
	// This test just verifies the function exists and can be called
	// We don't actually want to write files during testing
	// So we'll just ensure it doesn't panic with valid inputs
	content := []byte("test content")

	// This might fail due to permissions, but it shouldn't panic
	_ = DumpToFile("test", "http://example.com", content)
}
