package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
)

// TestCustomModelWithThinking tests that the custom model test endpoint
// correctly handles responses from Anthropic models with ThinkingLevel enabled.
// When thinking is enabled, the first content block is a thinking block, not text.
func TestCustomModelWithThinking(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
func TestCustomModelUserAgentCRUD(t *testing.T) {
	h := NewTestHarness(t)

	body := `{"display_name":"UA model","provider_type":"openai-responses","endpoint":"https://example.com/v1","api_key":"secret","model_name":"test-model","max_tokens":4096,"user_agent":"  custom-agent/1.0  "}`
	req := httptest.NewRequest(http.MethodPost, "/api/custom-models", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.server.handleCreateModel(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", w.Code, w.Body.String())
	}
	var created ModelAPI
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.UserAgent != "custom-agent/1.0" {
		t.Fatalf("created User-Agent = %q", created.UserAgent)
	}

	empty := ""
	update := UpdateModelRequest{
		DisplayName: created.DisplayName, ProviderType: created.ProviderType,
		Endpoint: created.Endpoint, ModelName: created.ModelName, MaxTokens: created.MaxTokens,
		UserAgent: &empty,
	}
	updateBody, err := json.Marshal(update)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPut, "/api/custom-models/"+created.ModelID, bytes.NewReader(updateBody))
	w = httptest.NewRecorder()
	h.server.handleUpdateModel(w, req, created.ModelID)
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d: %s", w.Code, w.Body.String())
	}
	var updated ModelAPI
	if err := json.NewDecoder(w.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.UserAgent != "" {
		t.Fatalf("cleared User-Agent = %q", updated.UserAgent)
	}
}
