package gem

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/gem/gemini"
)

func TestBuildGeminiRequest(t *testing.T) {
	// Create a service
	service := &Service{
		Model:  DefaultModel,
		APIKey: "test-api-key",
	}

	// Create a simple request
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
		System: []llm.SystemContent{
			{
				Text: "You are a helpful assistant.",
			},
		},
	}

	// Build the Gemini request
	gemReq, err := service.buildGeminiRequest(req)
	if err != nil {
		t.Fatalf("Failed to build Gemini request: %v", err)
	}

	// Verify the system instruction
	if gemReq.SystemInstruction == nil {
		t.Fatalf("Expected system instruction, got nil")
	}
	if len(gemReq.SystemInstruction.Parts) != 1 {
		t.Fatalf("Expected 1 system part, got %d", len(gemReq.SystemInstruction.Parts))
	}
	if gemReq.SystemInstruction.Parts[0].Text != "You are a helpful assistant." {
		t.Fatalf("Expected system text 'You are a helpful assistant.', got '%s'", gemReq.SystemInstruction.Parts[0].Text)
	}

	// Verify the contents
	if len(gemReq.Contents) != 1 {
		t.Fatalf("Expected 1 content, got %d", len(gemReq.Contents))
	}
	if len(gemReq.Contents[0].Parts) != 1 {
		t.Fatalf("Expected 1 part, got %d", len(gemReq.Contents[0].Parts))
	}
	if gemReq.Contents[0].Parts[0].Text != "Hello, world!" {
		t.Fatalf("Expected text 'Hello, world!', got '%s'", gemReq.Contents[0].Parts[0].Text)
	}
	// Verify the role is set correctly
	if gemReq.Contents[0].Role != "user" {
		t.Fatalf("Expected role 'user', got '%s'", gemReq.Contents[0].Role)
	}
}

func TestConvertToolSchemas(t *testing.T) {
	// Create a simple tool with a JSON schema
	schema := `{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"description": "The name of the person"
			},
			"age": {
				"type": "integer",
				"description": "The age of the person"
			}
		},
		"required": ["name"]
	}`

	tools := []*llm.Tool{
		{
			Name:        "get_person",
			Description: "Get information about a person",
			InputSchema: json.RawMessage(schema),
		},
	}

	// Convert the tools
	decls, err := convertToolSchemas(tools)
	if err != nil {
		t.Fatalf("Failed to convert tool schemas: %v", err)
	}

	// Verify the result
	if len(decls) != 1 {
		t.Fatalf("Expected 1 declaration, got %d", len(decls))
	}
	if decls[0].Name != "get_person" {
		t.Fatalf("Expected name 'get_person', got '%s'", decls[0].Name)
	}
	if decls[0].Description != "Get information about a person" {
		t.Fatalf("Expected description 'Get information about a person', got '%s'", decls[0].Description)
	}

	// Verify the schema properties
	if decls[0].Parameters.Type != 6 { // DataTypeOBJECT
		t.Fatalf("Expected type OBJECT (6), got %d", decls[0].Parameters.Type)
	}
	if len(decls[0].Parameters.Properties) != 2 {
		t.Fatalf("Expected 2 properties, got %d", len(decls[0].Parameters.Properties))
	}
	if decls[0].Parameters.Properties["name"].Type != 1 { // DataTypeSTRING
		t.Fatalf("Expected name type STRING (1), got %d", decls[0].Parameters.Properties["name"].Type)
	}
	if decls[0].Parameters.Properties["age"].Type != 3 { // DataTypeINTEGER
		t.Fatalf("Expected age type INTEGER (3), got %d", decls[0].Parameters.Properties["age"].Type)
	}
	if len(decls[0].Parameters.Required) != 1 || decls[0].Parameters.Required[0] != "name" {
		t.Fatalf("Expected required field 'name', got %v", decls[0].Parameters.Required)
	}
}

func TestService_Do_MockResponse(t *testing.T) {
	// This is a mock test that doesn't make actual API calls
	// Create a mock HTTP client that returns a predefined response

	// Create a Service with a mock client
	service := &Service{
		Model:  DefaultModel,
		APIKey: "test-api-key",
		// We would use a mock HTTP client here in a real test
	}

	// Create a sample request
	ir := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "Hello",
					},
				},
			},
		},
	}

	// In a real test, we would execute service.Do with a mock client
	// and verify the response structure

	// For now, we'll just test that buildGeminiRequest works correctly
	_, err := service.buildGeminiRequest(ir)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}
}

func TestConvertResponseWithToolCall(t *testing.T) {
	// Create a mock Gemini response with a function call
	gemRes := &gemini.Response{
		Candidates: []gemini.Candidate{
			{
				Content: gemini.Content{
					Parts: []gemini.Part{
						{
							FunctionCall: &gemini.FunctionCall{
								Name: "bash",
								Args: map[string]any{
									"command": "cat README.md",
								},
							},
						},
					},
				},
			},
		},
	}

	// Convert the response
	content := convertGeminiResponseToContent(gemRes)

	// Verify that content has a tool use
	if len(content) != 1 {
		t.Fatalf("Expected 1 content item, got %d", len(content))
	}

	if content[0].Type != llm.ContentTypeToolUse {
		t.Fatalf("Expected content type ToolUse, got %s", content[0].Type)
	}

	if content[0].ToolName != "bash" {
		t.Fatalf("Expected tool name 'bash', got '%s'", content[0].ToolName)
	}

	// Verify the tool input
	var args map[string]any
	if err := json.Unmarshal(content[0].ToolInput, &args); err != nil {
		t.Fatalf("Failed to unmarshal tool input: %v", err)
	}

	cmd, ok := args["command"]
	if !ok {
		t.Fatalf("Expected 'command' argument, not found")
	}

	if cmd != "cat README.md" {
		t.Fatalf("Expected command 'cat README.md', got '%s'", cmd)
	}
}

func TestGeminiHeaderCapture(t *testing.T) {
	// Create a mock HTTP client that returns a response with headers
	mockClient := &http.Client{
		Transport: &mockRoundTripper{
			response: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type":            []string{"application/json"},
					"Skaband-Cost-Microcents": []string{"123456"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{
					"candidates": [{
						"content": {
							"parts": [{
								"text": "Hello!"
							}]
						}
					}]
				}`)),
			},
		},
	}

	// Create a Gemini model with the mock client
	model := gemini.Model{
		Model:    "models/gemini-test",
		APIKey:   "test-key",
		HTTPC:    mockClient,
		Endpoint: "https://test.googleapis.com",
	}

	// Make a request
	req := &gemini.Request{
		Contents: []gemini.Content{
			{
				Parts: []gemini.Part{{Text: "Hello"}},
				Role:  "user",
			},
		},
	}

	ctx := context.Background()
	res, err := model.GenerateContent(ctx, req)
	if err != nil {
		t.Fatalf("Failed to generate content: %v", err)
	}

	// Verify that headers were captured
	headers := res.Header()
	if headers == nil {
		t.Fatalf("Expected headers to be captured, got nil")
	}

	// Check for the cost header
	costHeader := headers.Get("Skaband-Cost-Microcents")
	if costHeader != "123456" {
		t.Fatalf("Expected cost header '123456', got '%s'", costHeader)
	}

	// Verify that llm.CostUSDFromResponse works with these headers
	costUSD := llm.CostUSDFromResponse(headers)
	expectedCost := 0.00123456 // 123456 microcents / 100,000,000
	if costUSD != expectedCost {
		t.Fatalf("Expected cost USD %.8f, got %.8f", expectedCost, costUSD)
	}
}

// mockRoundTripper is a mock HTTP transport for testing
type mockRoundTripper struct {
	response *http.Response
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.response, nil
}

func TestHeaderCostIntegration(t *testing.T) {
	// Create a mock HTTP client that returns a response with cost headers
	mockClient := &http.Client{
		Transport: &mockRoundTripper{
			response: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type":            []string{"application/json"},
					"Skaband-Cost-Microcents": []string{"50000"}, // 0.5 USD
				},
				Body: io.NopCloser(bytes.NewBufferString(`{
					"candidates": [{
						"content": {
							"parts": [{
								"text": "Test response"
							}]
						}
					}]
				}`)),
			},
		},
	}

	// Create a Gem service with the mock client
	service := &Service{
		Model:  "gemini-test",
		APIKey: "test-key",
		HTTPC:  mockClient,
		URL:    "https://test.googleapis.com",
	}

	// Create a request
	ir := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "Hello",
					},
				},
			},
		},
	}

	// Make the request
	ctx := context.Background()
	res, err := service.Do(ctx, ir)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}

	// Verify that the cost was captured from headers
	expectedCost := 0.0005 // 50000 microcents / 100,000,000
	if res.Usage.CostUSD != expectedCost {
		t.Fatalf("Expected cost USD %.8f, got %.8f", expectedCost, res.Usage.CostUSD)
	}

	// Verify token counts are still estimated
	if res.Usage.InputTokens == 0 {
		t.Fatalf("Expected input tokens to be estimated, got 0")
	}
	if res.Usage.OutputTokens == 0 {
		t.Fatalf("Expected output tokens to be estimated, got 0")
	}
}

func TestTokenContextWindow(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected int
	}{
		{
			name:     "gemini-2.5-pro-preview-03-25",
			model:    "gemini-2.5-pro-preview-03-25",
			expected: 1000000,
		},
		{
			name:     "gemini-2.0-flash-exp",
			model:    "gemini-2.0-flash-exp",
			expected: 1000000,
		},
		{
			name:     "gemini-1.5-pro",
			model:    "gemini-1.5-pro",
			expected: 2000000,
		},
		{
			name:     "gemini-1.5-pro-latest",
			model:    "gemini-1.5-pro-latest",
			expected: 2000000,
		},
		{
			name:     "gemini-1.5-flash",
			model:    "gemini-1.5-flash",
			expected: 1000000,
		},
		{
			name:     "gemini-1.5-flash-latest",
			model:    "gemini-1.5-flash-latest",
			expected: 1000000,
		},
		{
			name:     "default model",
			model:    "",
			expected: 1000000,
		},
		{
			name:     "unknown model",
			model:    "unknown-model",
			expected: 1000000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &Service{
				Model: tt.model,
			}
			got := service.TokenContextWindow()
			if got != tt.expected {
				t.Errorf("TokenContextWindow() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestMaxImageDimension(t *testing.T) {
	service := &Service{}
	got := service.MaxImageDimension()
	// Currently returns 0 as per implementation
	expected := 0
	if got != expected {
		t.Errorf("MaxImageDimension() = %v, want %v", got, expected)
	}
}

func TestEnsureToolIDs(t *testing.T) {
	tests := []struct {
		name     string
		contents []llm.Content
		wantIDs  bool
	}{
		{
			name: "no tool uses",
			contents: []llm.Content{
				{
					Type: llm.ContentTypeText,
					Text: "Hello",
				},
			},
			wantIDs: false,
		},
		{
			name: "tool use with existing ID",
			contents: []llm.Content{
				{
					ID:       "existing-id",
					Type:     llm.ContentTypeToolUse,
					ToolName: "test-tool",
				},
			},
			wantIDs: true,
		},
		{
			name: "tool use without ID",
			contents: []llm.Content{
				{
					Type:     llm.ContentTypeToolUse,
					ToolName: "test-tool",
				},
			},
			wantIDs: true,
		},
		{
			name: "mixed content",
			contents: []llm.Content{
				{
					Type: llm.ContentTypeText,
					Text: "Hello",
				},
				{
					Type:     llm.ContentTypeToolUse,
					ToolName: "test-tool",
				},
				{
					ID:       "existing-id",
					Type:     llm.ContentTypeToolUse,
					ToolName: "test-tool-2",
				},
			},
			wantIDs: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy to avoid modifying the test data
			contents := make([]llm.Content, len(tt.contents))
			copy(contents, tt.contents)

			ensureToolIDs(contents)

			// Check if tool uses have IDs
			hasGeneratedIDs := false
			for _, content := range contents {
				if content.Type == llm.ContentTypeToolUse {
					if content.ID == "" {
						t.Errorf("Tool use missing ID")
					} else if content.ID != "existing-id" {
						// This is a generated ID
						hasGeneratedIDs = true
					}
				}
			}

			// If we expected IDs to be generated, check that at least one was
			if tt.wantIDs && !hasGeneratedIDs {
				// Check if all tool uses had existing IDs
				hasExistingIDs := false
				for _, content := range tt.contents {
					if content.Type == llm.ContentTypeToolUse && content.ID != "" {
						hasExistingIDs = true
					}
				}
				if !hasExistingIDs {
					t.Errorf("Expected generated IDs but none were found")
				}
			}
		})
	}
}

func TestCalculateUsage(t *testing.T) {
	// Test with a simple request and response
	req := &gemini.Request{
		SystemInstruction: &gemini.Content{
			Parts: []gemini.Part{
				{Text: "You are a helpful assistant."},
			},
		},
		Contents: []gemini.Content{
			{
				Parts: []gemini.Part{
					{Text: "Hello, how are you?"},
				},
				Role: "user",
			},
		},
	}

	res := &gemini.Response{
		Candidates: []gemini.Candidate{
			{
				Content: gemini.Content{
					Parts: []gemini.Part{
						{Text: "I'm doing well, thank you for asking!"},
					},
				},
			},
		},
	}

	usage := calculateUsage(req, res)

	// Verify that we got some token counts (they'll be estimated)
	if usage.InputTokens == 0 {
		t.Errorf("Expected input tokens to be greater than 0, got %d", usage.InputTokens)
	}
	if usage.OutputTokens == 0 {
		t.Errorf("Expected output tokens to be greater than 0, got %d", usage.OutputTokens)
	}

	// Test with nil response
	usageNil := calculateUsage(req, nil)
	if usageNil.InputTokens == 0 {
		t.Errorf("Expected input tokens with nil response to be greater than 0, got %d", usageNil.InputTokens)
	}
	if usageNil.OutputTokens != 0 {
		t.Errorf("Expected output tokens with nil response to be 0, got %d", usageNil.OutputTokens)
	}

	// Test with function calls
	reqWithFunction := &gemini.Request{
		Contents: []gemini.Content{
			{
				Parts: []gemini.Part{
					{
						FunctionCall: &gemini.FunctionCall{
							Name: "test_function",
							Args: map[string]any{
								"param1": "value1",
							},
						},
					},
				},
				Role: "user",
			},
		},
	}

	resWithFunction := &gemini.Response{
		Candidates: []gemini.Candidate{
			{
				Content: gemini.Content{
					Parts: []gemini.Part{
						{
							FunctionCall: &gemini.FunctionCall{
								Name: "response_function",
								Args: map[string]any{
									"result": "success",
								},
							},
						},
					},
				},
			},
		},
	}

	usageWithFunction := calculateUsage(reqWithFunction, resWithFunction)
	if usageWithFunction.InputTokens == 0 {
		t.Errorf("Expected input tokens with function calls to be greater than 0, got %d", usageWithFunction.InputTokens)
	}
	if usageWithFunction.OutputTokens == 0 {
		t.Errorf("Expected output tokens with function calls to be greater than 0, got %d", usageWithFunction.OutputTokens)
	}
}

func TestCalculateUsageWithFunctionResponse(t *testing.T) {
	// Test with function response in input (tool result)
	reqWithFunctionResponse := &gemini.Request{
		Contents: []gemini.Content{
			{
				Parts: []gemini.Part{
					{
						FunctionResponse: &gemini.FunctionResponse{
							Name: "test_function",
							Response: map[string]any{
								"result": "success",
								"error":  nil,
							},
						},
					},
				},
				Role: "user",
			},
		},
	}

	res := &gemini.Response{
		Candidates: []gemini.Candidate{
			{
				Content: gemini.Content{
					Parts: []gemini.Part{
						{Text: "Hello"},
					},
				},
			},
		},
	}

	usage := calculateUsage(reqWithFunctionResponse, res)
	// Should have some input tokens from the function response
	if usage.InputTokens == 0 {
		t.Errorf("Expected input tokens with function response to be greater than 0, got %d", usage.InputTokens)
	}
	if usage.OutputTokens == 0 {
		t.Errorf("Expected output tokens to be greater than 0, got %d", usage.OutputTokens)
	}
}

func TestCalculateUsageWithEmptyText(t *testing.T) {
	// Test with empty text parts
	req := &gemini.Request{
		Contents: []gemini.Content{
			{
				Parts: []gemini.Part{
					{Text: ""}, // Empty text
				},
				Role: "user",
			},
		},
	}

	res := &gemini.Response{
		Candidates: []gemini.Candidate{
			{
				Content: gemini.Content{
					Parts: []gemini.Part{
						{Text: ""}, // Empty text
					},
				},
			},
		},
	}

	usage := calculateUsage(req, res)
	// Should have 0 tokens for empty text
	if usage.InputTokens != 0 {
		t.Errorf("Expected input tokens to be 0 for empty text, got %d", usage.InputTokens)
	}
	if usage.OutputTokens != 0 {
		t.Errorf("Expected output tokens to be 0 for empty text, got %d", usage.OutputTokens)
	}
}

func TestCalculateUsageWithComplexFunctionCall(t *testing.T) {
	// Test with complex function call arguments
	req := &gemini.Request{
		Contents: []gemini.Content{
			{
				Parts: []gemini.Part{
					{
						FunctionCall: &gemini.FunctionCall{
							Name: "complex_function",
							Args: map[string]any{
								"string_param": "value",
								"int_param":    42,
								"array_param":  []any{"item1", "item2"},
								"object_param": map[string]any{
									"nested": "value",
								},
							},
						},
					},
				},
				Role: "user",
			},
		},
	}

	res := &gemini.Response{
		Candidates: []gemini.Candidate{
			{
				Content: gemini.Content{
					Parts: []gemini.Part{
						{
							FunctionCall: &gemini.FunctionCall{
								Name: "response_function",
								Args: map[string]any{
									"complex_result": map[string]any{
										"status": "success",
										"data":   []any{1, 2, 3},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	usage := calculateUsage(req, res)
	if usage.InputTokens == 0 {
		t.Errorf("Expected input tokens with complex function call to be greater than 0, got %d", usage.InputTokens)
	}
	if usage.OutputTokens == 0 {
		t.Errorf("Expected output tokens with complex function call to be greater than 0, got %d", usage.OutputTokens)
	}
}
