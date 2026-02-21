package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/server"
)

func TestWithAnthropicAPI(t *testing.T) {
	// Skip if no API key
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping Anthropic API test")
	}

	// Create temporary database
	tempDB := t.TempDir() + "/anthropic_test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer database.Close()

	// Run migrations
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	// Create LLM service manager
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo, // Less verbose for real API test
	}))
	llmConfig := &server.LLMConfig{
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		OpenAIAPIKey:    os.Getenv("OPENAI_API_KEY"),
		GeminiAPIKey:    os.Getenv("GEMINI_API_KEY"),
		FireworksAPIKey: os.Getenv("FIREWORKS_API_KEY"),
		Logger:          logger,
	}
	llmManager := server.NewLLMServiceManager(llmConfig)

	// Set up tools config
	toolSetConfig := claudetool.ToolSetConfig{
		WorkingDir:    t.TempDir(),
		LLMProvider:   llmManager,
		EnableBrowser: false,
	}

	// Create server
	svr := server.NewServer(database, llmManager, toolSetConfig, logger, false, "", "", "", nil)

	// Set up HTTP server
	mux := http.NewServeMux()
	svr.RegisterRoutes(mux)
	testServer := httptest.NewServer(mux)
	defer testServer.Close()

	t.Run("SimpleConversationWithClaude", func(t *testing.T) {
		// Create a conversation
		// Using database directly instead of service
		slug := "claude-test"
		conv, err := database.CreateConversation(context.Background(), &slug, true, nil, nil)
		if err != nil {
			t.Fatalf("Failed to create conversation: %v", err)
		}

		// Send a simple message
		chatReq := map[string]interface{}{
			"message": "Hello! Please introduce yourself briefly and tell me what you can help me with. Keep your response under 50 words.",
			"model":   "claude-haiku-4.5",
		}
		reqBody, _ := json.Marshal(chatReq)

		resp, err := http.Post(
			testServer.URL+"/api/conversation/"+conv.ConversationID+"/chat",
			"application/json",
			bytes.NewReader(reqBody),
		)
		if err != nil {
			t.Fatalf("Failed to send chat message: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("Expected status 202, got %d", resp.StatusCode)
		}

		// Wait for processing (Claude API can be slow)
		time.Sleep(5 * time.Second)

		// Check messages
		msgResp, err := http.Get(testServer.URL + "/api/conversation/" + conv.ConversationID)
		if err != nil {
			t.Fatalf("Failed to get conversation: %v", err)
		}
		defer msgResp.Body.Close()

		if msgResp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", msgResp.StatusCode)
		}

		var payload server.StreamResponse
		if err := json.NewDecoder(msgResp.Body).Decode(&payload); err != nil {
			t.Fatalf("Failed to decode messages: %v", err)
		}

		// Should have system message, user message and assistant response
		if len(payload.Messages) < 3 {
			msgTypes := make([]string, len(payload.Messages))
			for i, msg := range payload.Messages {
				msgTypes[i] = msg.Type
			}
			t.Fatalf("Expected at least 3 messages (system + user + assistant), got %d: %v", len(payload.Messages), msgTypes)
		}

		// Check first message is system prompt
		if payload.Messages[0].Type != "system" {
			t.Fatalf("Expected first message to be system, got %s", payload.Messages[0].Type)
		}

		// Check user message is second
		if payload.Messages[1].Type != "user" {
			t.Fatalf("Expected second message to be user, got %s", payload.Messages[1].Type)
		}

		// Check assistant response
		assistantFound := false
		for _, msg := range payload.Messages {
			if msg.Type == "agent" {
				assistantFound = true
				if msg.LlmData == nil {
					t.Fatal("Assistant message has no LLM data")
				}

				// Parse and check the response content
				var llmMsg llm.Message
				if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
					t.Fatalf("Failed to unmarshal LLM data: %v", err)
				}

				if len(llmMsg.Content) == 0 {
					t.Fatal("Assistant response has no content")
				}

				responseText := llmMsg.Content[0].Text
				if responseText == "" {
					t.Fatal("Assistant response text is empty")
				}

				// Claude should mention being Claude or an AI assistant
				lowerResponse := strings.ToLower(responseText)
				if !strings.Contains(lowerResponse, "claude") && !strings.Contains(lowerResponse, "assistant") {
					t.Logf("Response: %s", responseText)
					// This is not a hard failure - Claude might respond differently
				}

				t.Logf("Claude responded: %s", responseText)
				break
			}
		}

		if !assistantFound {
			t.Fatal("No assistant response found")
		}
	})

	t.Run("ConversationWithToolUse", func(t *testing.T) {
		// Create a conversation
		// Using database directly instead of service
		slug := "tool-test"
		conv, err := database.CreateConversation(context.Background(), &slug, true, nil, nil)
		if err != nil {
			t.Fatalf("Failed to create conversation: %v", err)
		}

		// Ask Claude to think about something
		chatReq := map[string]interface{}{
			"message": "Please use the think tool to plan how you would help someone learn to code. Keep it brief.",
			"model":   "claude-haiku-4.5",
		}
		reqBody, _ := json.Marshal(chatReq)

		resp, err := http.Post(
			testServer.URL+"/api/conversation/"+conv.ConversationID+"/chat",
			"application/json",
			bytes.NewReader(reqBody),
		)
		if err != nil {
			t.Fatalf("Failed to send chat message: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("Expected status 202, got %d", resp.StatusCode)
		}

		// Wait for processing (tool use might take longer)
		time.Sleep(8 * time.Second)

		// Check messages
		msgResp, err := http.Get(testServer.URL + "/api/conversation/" + conv.ConversationID)
		if err != nil {
			t.Fatalf("Failed to get conversation: %v", err)
		}
		defer msgResp.Body.Close()

		var payload server.StreamResponse
		if err := json.NewDecoder(msgResp.Body).Decode(&payload); err != nil {
			t.Fatalf("Failed to decode messages: %v", err)
		}

		// Should have multiple messages due to tool use
		if len(payload.Messages) < 3 {
			t.Logf("Got %d messages, expected at least 3 for tool use interaction", len(payload.Messages))
			// This might not always be the case depending on Claude's response
		}

		// Log all messages for debugging
		for i, msg := range payload.Messages {
			t.Logf("Message %d: Type=%s", i, msg.Type)
			if msg.LlmData != nil {
				var llmMsg llm.Message
				if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err == nil {
					if len(llmMsg.Content) > 0 && llmMsg.Content[0].Text != "" {
						t.Logf("  Content: %s", llmMsg.Content[0].Text[:min(100, len(llmMsg.Content[0].Text))])
					}
				}
			}
		}
	})

	t.Run("StreamingEndpoint", func(t *testing.T) {
		// Create a conversation with a message
		// Using database directly instead of service
		// Using database directly instead of service
		slug := "stream-test"
		conv, err := database.CreateConversation(context.Background(), &slug, true, nil, nil)
		if err != nil {
			t.Fatalf("Failed to create conversation: %v", err)
		}

		// Add a test message
		testMsg := llm.Message{
			Role: llm.MessageRoleUser,
			Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "Hello streaming test"},
			},
		}
		_, err = database.CreateMessage(context.Background(), db.CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           db.MessageTypeUser,
			LLMData:        testMsg,
		})
		if err != nil {
			t.Fatalf("Failed to create message: %v", err)
		}

		// Test stream endpoint
		resp, err := http.Get(testServer.URL + "/api/conversation/" + conv.ConversationID + "/stream")
		if err != nil {
			t.Fatalf("Failed to get stream: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", resp.StatusCode)
		}

		// Check headers
		if resp.Header.Get("Content-Type") != "text/event-stream" {
			t.Fatal("Expected text/event-stream content type")
		}

		// Read first chunk (should contain current messages)
		buf := make([]byte, 2048)
		n, err := resp.Body.Read(buf)
		if err != nil && err != io.EOF {
			t.Fatalf("Failed to read stream: %v", err)
		}

		data := string(buf[:n])
		if !strings.Contains(data, "data: ") {
			t.Fatal("Expected SSE data format")
		}

		t.Logf("Received stream data: %s", data[:min(200, len(data))])
	})
}

// Helper function for min
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
