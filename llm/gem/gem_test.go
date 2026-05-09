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
					"Content-Type":        []string{"application/json"},
					"Exedev-Gateway-Cost": []string{"0.00123456"},
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
	costHeader := headers.Get("Exedev-Gateway-Cost")
	if costHeader != "0.00123456" {
		t.Fatalf("Expected cost header '0.00123456', got '%s'", costHeader)
	}

	// Verify that llm.CostUSDFromResponse works with these headers
	costUSD := llm.CostUSDFromResponse(headers)
	expectedCost := 0.00123456
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
					"Content-Type":        []string{"application/json"},
					"Exedev-Gateway-Cost": []string{"0.000500"},
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
	expectedCost := 0.0005
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
			name:     "gemini-3-pro-preview",
			model:    "gemini-3-pro-preview",
			expected: 1000000,
		},
		{
			name:     "gemini-3-flash-preview",
			model:    "gemini-3-flash-preview",
			expected: 1000000,
		},
		{
			name:     "gemini-2.5-pro",
			model:    "gemini-2.5-pro",
			expected: 1000000,
		},
		{
			name:     "gemini-2.5-flash",
			model:    "gemini-2.5-flash",
			expected: 1000000,
		},
		{
			name:     "gemini-2.0-flash-exp",
			model:    "gemini-2.0-flash-exp",
			expected: 1000000,
		},
		{
			name:     "gemini-2.0-flash",
			model:    "gemini-2.0-flash",
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

func TestConvertResponseWithThinking(t *testing.T) {
	// A thought-summary part (thought:true) is classified as ContentTypeThinking.
	gemRes := &gemini.Response{
		Candidates: []gemini.Candidate{
			{
				Content: gemini.Content{
					Parts: []gemini.Part{
						{
							Text:             "Let me think about this problem step by step...",
							ThoughtSignature: "signature-abc123",
							Thought:          true,
						},
					},
				},
			},
		},
	}

	// Convert the response
	content := convertGeminiResponseToContent(gemRes)

	// Verify that content has thinking type
	if len(content) != 1 {
		t.Fatalf("Expected 1 content item, got %d", len(content))
	}

	if content[0].Type != llm.ContentTypeThinking {
		t.Fatalf("Expected content type Thinking, got %s", content[0].Type)
	}

	if content[0].Thinking != "Let me think about this problem step by step..." {
		t.Fatalf("Expected thinking text, got '%s'", content[0].Thinking)
	}

	if content[0].Signature != "signature-abc123" {
		t.Fatalf("Expected signature 'signature-abc123', got '%s'", content[0].Signature)
	}

	// Verify that Text field is empty for thinking content
	if content[0].Text != "" {
		t.Fatalf("Expected Text field to be empty for thinking content, got '%s'", content[0].Text)
	}
}

func TestConvertResponseWithRegularText(t *testing.T) {
	// Test that Gemini responses without ThoughtSignature are converted to ContentTypeText
	gemRes := &gemini.Response{
		Candidates: []gemini.Candidate{
			{
				Content: gemini.Content{
					Parts: []gemini.Part{
						{
							Text: "This is a regular response.",
							// No ThoughtSignature
						},
					},
				},
			},
		},
	}

	// Convert the response
	content := convertGeminiResponseToContent(gemRes)

	// Verify that content has text type
	if len(content) != 1 {
		t.Fatalf("Expected 1 content item, got %d", len(content))
	}

	if content[0].Type != llm.ContentTypeText {
		t.Fatalf("Expected content type Text, got %s", content[0].Type)
	}

	if content[0].Text != "This is a regular response." {
		t.Fatalf("Expected text, got '%s'", content[0].Text)
	}

	// Verify that Thinking field is empty for regular text
	if content[0].Thinking != "" {
		t.Fatalf("Expected Thinking field to be empty for text content, got '%s'", content[0].Thinking)
	}

	// Verify that Signature field is empty for regular text
	if content[0].Signature != "" {
		t.Fatalf("Expected Signature field to be empty for text content, got '%s'", content[0].Signature)
	}
}

func TestConvertResponseWithMixedContent(t *testing.T) {
	// Test that Gemini responses with both thinking and regular text are handled correctly
	gemRes := &gemini.Response{
		Candidates: []gemini.Candidate{
			{
				Content: gemini.Content{
					Parts: []gemini.Part{
						{
							Text:             "Thinking about the problem...",
							ThoughtSignature: "sig-1",
							Thought:          true,
						},
						{
							Text: "Here is my answer.",
							// No ThoughtSignature
						},
					},
				},
			},
		},
	}

	// Convert the response
	content := convertGeminiResponseToContent(gemRes)

	// Verify that we have 2 content items
	if len(content) != 2 {
		t.Fatalf("Expected 2 content items, got %d", len(content))
	}

	// First should be thinking
	if content[0].Type != llm.ContentTypeThinking {
		t.Fatalf("Expected first content type to be Thinking, got %s", content[0].Type)
	}

	if content[0].Thinking != "Thinking about the problem..." {
		t.Fatalf("Expected thinking text, got '%s'", content[0].Thinking)
	}

	if content[0].Signature != "sig-1" {
		t.Fatalf("Expected signature 'sig-1', got '%s'", content[0].Signature)
	}

	// Second should be regular text
	if content[1].Type != llm.ContentTypeText {
		t.Fatalf("Expected second content type to be Text, got %s", content[1].Type)
	}

	if content[1].Text != "Here is my answer." {
		t.Fatalf("Expected text, got '%s'", content[1].Text)
	}
}

// TestConvertResponseGemini3FinalAnswerWithSignature is a regression test for
// the gemini-3.x "Test failed: empty response from model" bug. Gemini 3 attaches
// a thoughtSignature to ordinary final-answer text parts (not just thoughts) so
// internal reasoning state can be rehydrated next turn. The presence of a signature
// alone must not classify the part as thinking — otherwise the model's actual
// answer disappears from llm.Response.Content.
func TestConvertResponseGemini3FinalAnswerWithSignature(t *testing.T) {
	gemRes := &gemini.Response{
		Candidates: []gemini.Candidate{{
			Content: gemini.Content{
				Parts: []gemini.Part{{
					Text:             "Test successful.",
					ThoughtSignature: "Eq0FCqoFAQw51sdx7TPrSqmb0Ts...",
					// Thought is intentionally false — this is the final answer.
				}},
			},
		}},
	}

	contents := convertGeminiResponseToContent(gemRes)
	if len(contents) != 1 {
		t.Fatalf("got %d contents, want 1", len(contents))
	}
	if contents[0].Type != llm.ContentTypeText {
		t.Fatalf("got type %s, want ContentTypeText", contents[0].Type)
	}
	if contents[0].Text != "Test successful." {
		t.Fatalf("got text %q, want %q", contents[0].Text, "Test successful.")
	}
	if contents[0].Signature == "" {
		t.Fatalf("expected Signature to be preserved on final-answer text for round-tripping")
	}
}

func TestBuildGeminiRequestWithThinking(t *testing.T) {
	// Test that thinking content is properly converted when building Gemini requests
	service := &Service{
		Model:  DefaultModel,
		APIKey: "test-api-key",
	}

	// Create a request with thinking content in conversation history
	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "What is 2 + 2?",
					},
				},
			},
			{
				Role: llm.MessageRoleAssistant,
				Content: []llm.Content{
					{
						Type:      llm.ContentTypeThinking,
						Thinking:  "Let me calculate 2 + 2...",
						Signature: "sig-123",
					},
					{
						Type: llm.ContentTypeText,
						Text: "The answer is 4.",
					},
				},
			},
		},
	}

	// Build the Gemini request
	gemReq, err := service.buildGeminiRequest(req)
	if err != nil {
		t.Fatalf("Failed to build Gemini request: %v", err)
	}

	// Verify the request structure
	if len(gemReq.Contents) != 2 {
		t.Fatalf("Expected 2 contents, got %d", len(gemReq.Contents))
	}

	// Verify the assistant message has 2 parts
	assistantContent := gemReq.Contents[1]
	if assistantContent.Role != "model" {
		t.Fatalf("Expected role 'model', got '%s'", assistantContent.Role)
	}

	if len(assistantContent.Parts) != 2 {
		t.Fatalf("Expected 2 parts in assistant message, got %d", len(assistantContent.Parts))
	}

	// Verify the first part is thinking content with the correct text and signature
	thinkingPart := assistantContent.Parts[0]
	if thinkingPart.Text != "Let me calculate 2 + 2..." {
		t.Fatalf("Expected thinking text 'Let me calculate 2 + 2...', got '%s'", thinkingPart.Text)
	}
	if thinkingPart.ThoughtSignature != "sig-123" {
		t.Fatalf("Expected signature 'sig-123', got '%s'", thinkingPart.ThoughtSignature)
	}
	if !thinkingPart.Thought {
		t.Fatal("Expected thinking part to be marked as thought")
	}

	// Verify the second part is regular text
	textPart := assistantContent.Parts[1]
	if textPart.Text != "The answer is 4." {
		t.Fatalf("Expected text 'The answer is 4.', got '%s'", textPart.Text)
	}
	if textPart.ThoughtSignature != "" {
		t.Fatalf("Expected no signature for text part, got '%s'", textPart.ThoughtSignature)
	}
}

func TestBuildGeminiRequestWithRedactedThinking(t *testing.T) {
	// Test that redacted thinking content is properly converted
	service := &Service{
		Model:  DefaultModel,
		APIKey: "test-api-key",
	}

	// Create a request with redacted thinking content
	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleAssistant,
				Content: []llm.Content{
					{
						Type:      llm.ContentTypeRedactedThinking,
						Data:      "<redacted>",
						Signature: "sig-redacted",
					},
					{
						Type: llm.ContentTypeText,
						Text: "Response text",
					},
				},
			},
		},
	}

	// Build the Gemini request
	gemReq, err := service.buildGeminiRequest(req)
	if err != nil {
		t.Fatalf("Failed to build Gemini request: %v", err)
	}

	// Verify the redacted thinking part
	if len(gemReq.Contents) != 1 {
		t.Fatalf("Expected 1 content, got %d", len(gemReq.Contents))
	}

	if len(gemReq.Contents[0].Parts) != 2 {
		t.Fatalf("Expected 2 parts, got %d", len(gemReq.Contents[0].Parts))
	}

	redactedPart := gemReq.Contents[0].Parts[0]
	if redactedPart.Text != "<redacted>" {
		t.Fatalf("Expected redacted text '<redacted>', got '%s'", redactedPart.Text)
	}
	if redactedPart.ThoughtSignature != "sig-redacted" {
		t.Fatalf("Expected signature 'sig-redacted', got '%s'", redactedPart.ThoughtSignature)
	}
	if !redactedPart.Thought {
		t.Fatal("Expected redacted thinking part to be marked as thought")
	}
}

func TestRoundTripThinking(t *testing.T) {
	// Test that thinking content survives a round trip: response -> content -> request

	// Step 1: Convert a Gemini response with thinking to llm.Content
	gemRes := &gemini.Response{
		Candidates: []gemini.Candidate{
			{
				Content: gemini.Content{
					Parts: []gemini.Part{
						{
							Text:             "Analyzing the problem...",
							ThoughtSignature: "sig-abc",
							Thought:          true,
						},
						{
							Text: "The answer is 42.",
						},
					},
				},
			},
		},
	}

	contents := convertGeminiResponseToContent(gemRes)

	// Verify we got thinking and text content
	if len(contents) != 2 {
		t.Fatalf("Expected 2 content items, got %d", len(contents))
	}

	if contents[0].Type != llm.ContentTypeThinking {
		t.Fatalf("Expected first content to be thinking, got %s", contents[0].Type)
	}

	if contents[1].Type != llm.ContentTypeText {
		t.Fatalf("Expected second content to be text, got %s", contents[1].Type)
	}

	// Step 2: Use this content in a new request
	service := &Service{
		Model:  DefaultModel,
		APIKey: "test-api-key",
	}

	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role:    llm.MessageRoleAssistant,
				Content: contents,
			},
		},
	}

	gemReq, err := service.buildGeminiRequest(req)
	if err != nil {
		t.Fatalf("Failed to build Gemini request: %v", err)
	}

	// Step 3: Verify the thinking content was preserved in the request
	if len(gemReq.Contents) != 1 {
		t.Fatalf("Expected 1 content, got %d", len(gemReq.Contents))
	}

	if len(gemReq.Contents[0].Parts) != 2 {
		t.Fatalf("Expected 2 parts, got %d", len(gemReq.Contents[0].Parts))
	}

	// Verify thinking part preserved
	thinkingPart := gemReq.Contents[0].Parts[0]
	if thinkingPart.Text != "Analyzing the problem..." {
		t.Fatalf("Expected thinking text to be preserved, got '%s'", thinkingPart.Text)
	}
	if thinkingPart.ThoughtSignature != "sig-abc" {
		t.Fatalf("Expected signature to be preserved, got '%s'", thinkingPart.ThoughtSignature)
	}
	if !thinkingPart.Thought {
		t.Fatal("Expected thinking part to stay marked as thought")
	}

	// Verify text part preserved
	textPart := gemReq.Contents[0].Parts[1]
	if textPart.Text != "The answer is 42." {
		t.Fatalf("Expected text to be preserved, got '%s'", textPart.Text)
	}
	if textPart.ThoughtSignature != "" {
		t.Fatalf("Expected no signature for text, got '%s'", textPart.ThoughtSignature)
	}
}

func TestThinkingConfig(t *testing.T) {
	userMsg := llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}},
		}},
	}

	tests := []struct {
		name        string
		svc         *Service
		wantLevel   string
		wantBudget  *int
		wantOmitted bool
	}{
		{
			name:        "off by default",
			svc:         &Service{Model: "gemini-3-flash-preview", APIKey: "x"},
			wantOmitted: true,
		},
		{
			name:      "gemini-3-flash maps medium to thinkingLevel",
			svc:       &Service{Model: "gemini-3-flash-preview", APIKey: "x", ThinkingLevel: llm.ThinkingLevelMedium},
			wantLevel: "medium",
		},
		{
			name:      "gemini-3-pro clamps medium to high",
			svc:       &Service{Model: "gemini-3-pro-preview", APIKey: "x", ThinkingLevel: llm.ThinkingLevelMedium},
			wantLevel: "high",
		},
		{
			name:      "gemini-3-pro clamps minimal to low",
			svc:       &Service{Model: "gemini-3-pro-preview", APIKey: "x", ThinkingLevel: llm.ThinkingLevelMinimal},
			wantLevel: "low",
		},
		{
			name:      "gemini-3.1-pro accepts medium",
			svc:       &Service{Model: "gemini-3.1-pro-preview", APIKey: "x", ThinkingLevel: llm.ThinkingLevelMedium},
			wantLevel: "medium",
		},
		{
			name:      "ReasoningEffort overrides ThinkingLevel for 3.x",
			svc:       &Service{Model: "gemini-3-flash-preview", APIKey: "x", ThinkingLevel: llm.ThinkingLevelMedium, ReasoningEffort: "high"},
			wantLevel: "high",
		},
		{
			name:       "gemini-2.5 uses thinkingBudget",
			svc:        &Service{Model: "gemini-2.5-pro", APIKey: "x", ThinkingLevel: llm.ThinkingLevelMedium},
			wantBudget: ptr(8192),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gemReq, err := tt.svc.buildGeminiRequest(&userMsg)
			if err != nil {
				t.Fatalf("buildGeminiRequest: %v", err)
			}
			if tt.wantOmitted {
				if gemReq.GenerationConfig != nil && gemReq.GenerationConfig.ThinkingConfig != nil {
					t.Fatalf("expected no thinkingConfig, got %+v", gemReq.GenerationConfig.ThinkingConfig)
				}
				return
			}
			if gemReq.GenerationConfig == nil || gemReq.GenerationConfig.ThinkingConfig == nil {
				t.Fatalf("expected thinkingConfig to be set")
			}
			tc := gemReq.GenerationConfig.ThinkingConfig
			if tc.ThinkingLevel != tt.wantLevel {
				t.Errorf("thinkingLevel = %q, want %q", tc.ThinkingLevel, tt.wantLevel)
			}
			if tt.wantBudget != nil {
				if tc.ThinkingBudget == nil || *tc.ThinkingBudget != *tt.wantBudget {
					t.Errorf("thinkingBudget = %v, want %d", tc.ThinkingBudget, *tt.wantBudget)
				}
			} else if tc.ThinkingBudget != nil {
				t.Errorf("unexpected thinkingBudget = %d", *tc.ThinkingBudget)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestBuildGeminiRequestImage(t *testing.T) {
	service := &Service{Model: DefaultModel, APIKey: "test"}
	req := &llm.Request{
		Messages: []llm.Message{{
			Role: llm.MessageRoleUser,
			Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "What color is this?"},
				{Type: llm.ContentTypeText, MediaType: "image/png", Data: "AAAA"},
			},
		}},
	}
	gemReq, err := service.buildGeminiRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(gemReq.Contents) != 1 || len(gemReq.Contents[0].Parts) != 2 {
		t.Fatalf("expected 1 content with 2 parts, got %+v", gemReq.Contents)
	}
	parts := gemReq.Contents[0].Parts
	if parts[0].Text != "What color is this?" {
		t.Errorf("part 0 text = %q", parts[0].Text)
	}
	if parts[1].InlineData == nil {
		t.Fatalf("part 1 inlineData = nil")
	}
	if parts[1].InlineData.MimeType != "image/png" || parts[1].InlineData.Data != "AAAA" {
		t.Errorf("inlineData = %+v", parts[1].InlineData)
	}
	if parts[1].Text != "" {
		t.Errorf("part 1 should not have text, got %q", parts[1].Text)
	}
}

func TestBuildGeminiRequestImageInToolResult(t *testing.T) {
	service := &Service{Model: DefaultModel, APIKey: "test"}
	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleAssistant,
				Content: []llm.Content{{
					ID:        "tool_1",
					Type:      llm.ContentTypeToolUse,
					ToolName:  "screenshot",
					ToolInput: json.RawMessage(`{}`),
				}},
			},
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{{
					Type:      llm.ContentTypeToolResult,
					ToolUseID: "tool_1",
					ToolName:  "screenshot",
					ToolResult: []llm.Content{
						{Type: llm.ContentTypeText, Text: "screenshot taken"},
						{Type: llm.ContentTypeText, MediaType: "image/png", Data: "BBBB"},
					},
				}},
			},
		},
	}
	gemReq, err := service.buildGeminiRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(gemReq.Contents) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(gemReq.Contents))
	}
	parts := gemReq.Contents[1].Parts
	if len(parts) != 2 {
		t.Fatalf("expected tool-result content with 2 parts (functionResponse + inlineData), got %d: %+v", len(parts), parts)
	}
	if parts[0].FunctionResponse == nil || parts[0].FunctionResponse.Name != "screenshot" {
		t.Errorf("part 0 functionResponse = %+v", parts[0].FunctionResponse)
	}
	if parts[1].InlineData == nil || parts[1].InlineData.MimeType != "image/png" || parts[1].InlineData.Data != "BBBB" {
		t.Errorf("part 1 inlineData = %+v", parts[1].InlineData)
	}
}
