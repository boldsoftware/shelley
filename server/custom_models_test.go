package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
)

// mockAnthropicSSE returns an SSE stream with thinking blocks followed by text.
func mockAnthropicSSE(thinkingText, responseText string) string {
	return fmt.Sprintf(`event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"%s"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"%s"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":20}}

event: message_stop
data: {"type":"message_stop"}

`, thinkingText, responseText)
}

// TestCustomModelTestEndpointWithMockAnthropic verifies that the model test
// endpoint correctly handles Anthropic responses with thinking blocks.
func TestCustomModelTestEndpointWithMockAnthropic(t *testing.T) {
	mockSSE := mockAnthropicSSE("I need to think about this...", "test successful")

	// Mock Anthropic API server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, mockSSE)
	}))
	defer mockServer.Close()

	h := NewTestHarness(t)

	testReq := struct {
		ProviderType string `json:"provider_type"`
		APIKey       string `json:"api_key"`
		Endpoint     string `json:"endpoint"`
		ModelName    string `json:"model_name"`
	}{
		ProviderType: "anthropic",
		APIKey:       "sk-test-fake",
		Endpoint:     mockServer.URL + "/v1/messages",
		ModelName:    "claude-test",
	}

	body, _ := json.Marshal(testReq)
	req := httptest.NewRequest(http.MethodPost, "/api/custom-models/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleTestModel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	success, ok := result["success"].(bool)
	if !ok || !success {
		t.Fatalf("Expected success=true, got: %v (message: %v)", result["success"], result["message"])
	}

	message, ok := result["message"].(string)
	if !ok {
		t.Fatal("Response missing message field")
	}

	if message == "" || message == "Test failed: empty response from model" {
		t.Fatal("Got empty response error despite valid mock API returning text after thinking block")
	}

	t.Logf("SUCCESS: Mock Anthropic response with thinking handled correctly: %s", message)
}

// TestCustomModelWithThinking tests that the custom model test endpoint
// correctly handles responses from Anthropic models with ThinkingLevel enabled.
// When thinking is enabled, the first content block is a thinking block, not text.
func TestCustomModelWithThinking(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping integration test")
	}

	// Create a service with thinking enabled
	service := &ant.Service{
		APIKey:        apiKey,
		Model:         ant.Claude46Opus,
		ThinkingLevel: llm.ThinkingLevelMedium,
	}

	// Send a simple test request
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	request := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Say 'test successful' in exactly two words."},
				},
			},
		},
	}

	response, err := service.Do(ctx, request)
	if err != nil {
		t.Fatalf("API call failed: %v", err)
	}

	// Verify response has content
	if len(response.Content) == 0 {
		t.Fatal("Response has no content blocks")
	}

	// The first block should be a thinking block
	if response.Content[0].Type != llm.ContentTypeThinking {
		t.Logf("Warning: Expected first block to be thinking, got %v", response.Content[0].Type)
	}

	// Find the first text block (skipping thinking blocks)
	var foundText bool
	var responseText string
	for _, content := range response.Content {
		if content.Type == llm.ContentTypeText && content.Text != "" {
			responseText = content.Text
			foundText = true
			break
		}
	}

	if !foundText {
		t.Fatal("No text content found in response (only thinking blocks)")
	}

	t.Logf("Successfully received response with thinking enabled: %s", responseText)
}

// TestCustomModelTestEndpoint tests the HTTP endpoint for testing custom models.
// This simulates what happens when a user adds a custom Anthropic model in the UI.
func TestCustomModelTestEndpoint(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping integration test")
	}

	h := NewTestHarness(t)

	// Create a test request that simulates adding a custom Anthropic model
	testReq := struct {
		ProviderType string `json:"provider_type"`
		APIKey       string `json:"api_key"`
		Endpoint     string `json:"endpoint"`
		ModelName    string `json:"model_name"`
	}{
		ProviderType: "anthropic",
		APIKey:       apiKey,
		Endpoint:     "https://api.anthropic.com/v1/messages",
		ModelName:    ant.Claude46Opus,
	}

	body, err := json.Marshal(testReq)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/custom-models/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleTestModel(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if success, ok := result["success"].(bool); !ok || !success {
		t.Errorf("Test failed: %v", result["message"])
	}

	message, ok := result["message"].(string)
	if !ok {
		t.Fatal("Response missing message field")
	}

	t.Logf("Test endpoint response: %s", message)

	// Verify that we got a non-empty response
	if message == "" || message == "Test failed: empty response from model" {
		t.Error("Got empty response error despite having a valid API key")
	}
}

// TestCustomModelTestEndpointOnlyThinkingBlocks verifies that the model test
// endpoint correctly rejects responses with only thinking blocks (no text).
func TestCustomModelTestEndpointOnlyThinkingBlocks(t *testing.T) {
	// SSE stream with ONLY thinking blocks, no text
	mockSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Hmm, let me think about this..."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":10}}

event: message_stop
data: {"type":"message_stop"}
`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, mockSSE)
	}))
	defer mockServer.Close()

	h := NewTestHarness(t)

	testReq := struct {
		ProviderType string `json:"provider_type"`
		APIKey       string `json:"api_key"`
		Endpoint     string `json:"endpoint"`
		ModelName    string `json:"model_name"`
	}{
		ProviderType: "anthropic",
		APIKey:       "sk-test-fake",
		Endpoint:     mockServer.URL + "/v1/messages",
		ModelName:    "claude-test",
	}

	body, _ := json.Marshal(testReq)
	req := httptest.NewRequest(http.MethodPost, "/api/custom-models/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleTestModel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	success, ok := result["success"].(bool)
	if ok && success {
		t.Fatal("Expected success=false for response with only thinking blocks")
	}

	message, ok := result["message"].(string)
	if !ok {
		t.Fatal("Response missing message field")
	}

	if message != "Test failed: empty response from model" {
		t.Fatalf("Expected 'empty response' error, got: %s", message)
	}

	t.Logf("SUCCESS: Response with only thinking blocks correctly rejected: %s", message)
}
