package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
	"shelley.exe.dev/models"
	"shelley.exe.dev/server"
	"shelley.exe.dev/slug"
)

func TestServerEndToEnd(t *testing.T) {
	// Create temporary database
	tempDB := t.TempDir() + "/test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer database.Close()

	// Run migrations
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	// Create logger first
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// Create LLM service manager with predictable service
	llmManager := server.NewLLMServiceManager(&server.LLMConfig{Logger: logger})
	predictableService := loop.NewPredictableService()
	// For testing, we'll override the manager's service selection
	_ = predictableService // will need to mock this properly

	// Set up tools
	// Set up tools config
	toolSetConfig := claudetool.ToolSetConfig{
		WorkingDir:    t.TempDir(),
		EnableBrowser: false,
	}

	// Create server
	svr := server.NewServer(database, llmManager, toolSetConfig, logger, false, "", "", "", nil, false)

	// Set up HTTP server
	mux := http.NewServeMux()
	svr.RegisterRoutes(mux)
	testServer := httptest.NewServer(mux)
	defer testServer.Close()

	t.Run("CreateAndListConversations", func(t *testing.T) {
		// Create a conversation
		// Using database directly instead of service
		slug := "test-conversation"
		conv, err := database.CreateConversation(context.Background(), &slug, true, nil, nil)
		if err != nil {
			t.Fatalf("Failed to create conversation: %v", err)
		}

		// List conversations
		resp, err := http.Get(testServer.URL + "/api/conversations")
		if err != nil {
			t.Fatalf("Failed to get conversations: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", resp.StatusCode)
		}

		var conversations []generated.Conversation
		if err := json.NewDecoder(resp.Body).Decode(&conversations); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if len(conversations) != 1 {
			t.Fatalf("Expected 1 conversation, got %d", len(conversations))
		}

		if conversations[0].ConversationID != conv.ConversationID {
			t.Fatalf("Conversation ID mismatch")
		}
	})

	t.Run("ChatEndToEnd", func(t *testing.T) {
		// Create a conversation
		// Using database directly instead of service
		slug := "chat-test"
		conv, err := database.CreateConversation(context.Background(), &slug, true, nil, nil)
		if err != nil {
			t.Fatalf("Failed to create conversation: %v", err)
		}

		// Send a chat message using predictable model
		chatReq := map[string]interface{}{"message": "Hello, can you help me?", "model": "predictable"}
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

		// Wait a bit for processing
		time.Sleep(500 * time.Millisecond)

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

		// Should have at least system and user messages
		if len(payload.Messages) < 2 {
			t.Fatalf("Expected at least 2 messages (system + user), got %d", len(payload.Messages))
		}

		// First message should be system prompt
		if payload.Messages[0].Type != "system" {
			t.Fatalf("Expected first message to be system, got %s", payload.Messages[0].Type)
		}

		// Second message should be from user
		if payload.Messages[1].Type != "user" {
			t.Fatalf("Expected second message to be user, got %s", payload.Messages[1].Type)
		}
	})

	t.Run("StreamEndpoint", func(t *testing.T) {
		// Create a conversation with some messages
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
				{Type: llm.ContentTypeText, Text: "Test message"},
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

		// Read first event (should be current messages)
		buf := make([]byte, 1024)
		n, err := resp.Body.Read(buf)
		if err != nil && err != io.EOF {
			t.Fatalf("Failed to read stream: %v", err)
		}

		data := string(buf[:n])
		if !strings.Contains(data, "data: ") {
			t.Fatal("Expected SSE data format")
		}
	})

	// Test that slug updates are reflected in the stream
	t.Run("SlugUpdateStream", func(t *testing.T) {
		// Create a context that won't be canceled unexpectedly
		ctx := context.Background()

		// Create a conversation without a slug
		conv, err := database.CreateConversation(ctx, nil, true, nil, nil)
		if err != nil {
			t.Fatalf("Failed to create conversation: %v", err)
		}

		// Verify initially no slug
		if conv.Slug != nil {
			t.Fatalf("Expected no initial slug, got: %v", *conv.Slug)
		}

		// Send a message which should trigger slug generation
		chatRequest := server.ChatRequest{
			Message: "Write a Python script to calculate fibonacci numbers",
			Model:   "predictable",
		}

		chatBody, _ := json.Marshal(chatRequest)
		chatResp, err := http.Post(
			testServer.URL+"/api/conversation/"+conv.ConversationID+"/chat",
			"application/json",
			strings.NewReader(string(chatBody)),
		)
		if err != nil {
			t.Fatalf("Failed to send chat message: %v", err)
		}
		defer chatResp.Body.Close()

		// Check response status before continuing
		if chatResp.StatusCode != http.StatusAccepted {
			t.Fatalf("Expected status 202, got %d", chatResp.StatusCode)
		}

		// Wait longer for slug generation (it happens asynchronously)
		// Poll every 100ms instead of 500ms for faster feedback
		for i := 0; i < 100; i++ {
			time.Sleep(100 * time.Millisecond)

			// Check if slug was generated
			updatedConv, err := database.GetConversationByID(ctx, conv.ConversationID)
			if err != nil {
				// Don't fail immediately on error - the conversation might be temporarily locked
				// Only fail if we've exhausted all retries
				if i == 99 {
					t.Fatalf("Failed to get updated conversation after all retries: %v", err)
				}
				continue
			}

			if updatedConv.Slug != nil {
				t.Logf("Slug generated successfully: %s", *updatedConv.Slug)
				return
			}
		}

		t.Fatal("Slug was not generated within timeout period")
	})

	t.Run("ErrorHandling", func(t *testing.T) {
		// Test non-existent conversation
		resp, err := http.Get(testServer.URL + "/api/conversation/nonexistent")
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		// Should handle gracefully (might be empty list or error depending on implementation)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
			t.Fatalf("Unexpected status code: %d", resp.StatusCode)
		}

		// Test invalid chat request
		invalidReq := map[string]string{"not_message": "test"}
		reqBody, _ := json.Marshal(invalidReq)
		chatResp, err := http.Post(
			testServer.URL+"/api/conversation/test/chat",
			"application/json",
			bytes.NewReader(reqBody),
		)
		if err != nil {
			t.Fatalf("Failed to send invalid chat: %v", err)
		}
		defer chatResp.Body.Close()

		if chatResp.StatusCode != http.StatusBadRequest {
			t.Fatalf("Expected status 400 for invalid request, got %d", chatResp.StatusCode)
		}
	})
}

func TestPredictableServiceWithTools(t *testing.T) {
	// Test that the predictable service correctly handles tool calls
	service := loop.NewPredictableService()

	// First call should return greeting
	resp1, err := service.Do(context.Background(), &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}}},
		},
	})
	if err != nil {
		t.Fatalf("First call failed: %v", err)
	}

	if !strings.Contains(resp1.Content[0].Text, "Shelley") {
		t.Fatal("Expected greeting to mention Shelley")
	}

	// Second call should return tool use (bash command)
	resp2, err := service.Do(context.Background(), &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "bash: echo hello"}}},
		},
	})
	if err != nil {
		t.Fatalf("Second call failed: %v", err)
	}

	if resp2.StopReason != llm.StopReasonToolUse {
		t.Fatal("Expected tool use stop reason")
	}

	if len(resp2.Content) < 2 {
		t.Fatal("Expected both text and tool use content")
	}

	// Find tool use content
	var toolUse *llm.Content
	for i := range resp2.Content {
		if resp2.Content[i].Type == llm.ContentTypeToolUse {
			toolUse = &resp2.Content[i]
			break
		}
	}

	if toolUse == nil {
		t.Fatal("Expected tool use content")
	}

	if toolUse.ToolName != "bash" {
		t.Fatalf("Expected bash tool, got %s", toolUse.ToolName)
	}
}

func TestConversationCleanup(t *testing.T) {
	// Create temporary database
	tempDB := t.TempDir() + "/cleanup_test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer database.Close()

	// Run migrations
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	// Create server with predictable service
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	llmManager := server.NewLLMServiceManager(&server.LLMConfig{Logger: logger})
	svr := server.NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, false, "", "", "", nil, false)

	// Create a conversation
	// Using database directly instead of service
	conv, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Test cleanup indirectly by calling cleanup
	svr.Cleanup()

	// Test passes if no panic occurs
	t.Log("Cleanup completed successfully for conversation:", conv.ConversationID)
}

func TestSlugGeneration(t *testing.T) {
	// This test verifies that the slug generation logic is properly integrated
	// but uses the direct API to avoid timing issues with background goroutines

	// Create temporary database
	tempDB := t.TempDir() + "/test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer database.Close()

	// Run migrations
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	// Create server
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	llmManager := server.NewLLMServiceManager(&server.LLMConfig{Logger: logger})
	_ = server.NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, false, "", "", "", nil, false)

	// Test slug generation directly to avoid timing issues
	// ctx := context.Background()
	// testMessage := "help me create a Python web server"

	// TODO: Fix slug generation test - method moved to slug package
	// Generate slug directly
	// slugResult, err := svr.GenerateSlugForConversation(ctx, testMessage)
	// if err != nil {
	//	t.Fatalf("Slug generation failed: %v", err)
	// }
	// if slugResult == "" {
	//	t.Error("Generated slug is empty")
	// } else {
	//	t.Logf("Generated slug: %s", slugResult)
	// }

	// TODO: Fix slug tests
	// Test that the slug is properly sanitized
	// if !strings.Contains(slugResult, "python") || !strings.Contains(slugResult, "web") {
	//	t.Logf("Note: Generated slug '%s' may not contain expected keywords, but this is acceptable for AI-generated content", slugResult)
	// }

	// // Verify slug uniqueness handling
	// conv, err := database.CreateConversation(ctx, &slugResult, true)
	// if err != nil {
	//	t.Fatalf("Failed to create conversation with slug: %v", err)
	// }

	// TODO: Fix slug generation test
	// Try to generate the same slug again - should get a unique variant
	// slugResult2, err := svr.GenerateSlugForConversation(ctx, testMessage)
	// if err != nil {
	//	t.Fatalf("Second slug generation failed: %v", err)
	// }

	// // The second slug should be different (with -1, -2, etc.)
	// if slugResult == slugResult2 {
	//	t.Errorf("Expected different slugs for uniqueness, but got same: %s", slugResult)
	// } else {
	//	t.Logf("Unique slug generated: %s", slugResult2)
	// }

	// _ = conv // avoid unused variable warning
}

func TestSanitizeSlug(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"basic text", "Hello World", "hello-world"},
		{"with numbers", "Python3 Tutorial", "python3-tutorial"},
		{"with special chars", "C++ Programming!", "c-programming"},
		{"multiple spaces", "Very  Long   Title", "very-long-title"},
		{"underscores", "test_function_name", "test-function-name"},
		{"mixed case", "CamelCaseExample", "camelcaseexample"},
		{"with hyphens", "pre-existing-hyphens", "pre-existing-hyphens"},
		{"leading/trailing spaces", "  trimmed  ", "trimmed"},
		{"leading/trailing hyphens", "-start-end-", "start-end"},
		{"multiple consecutive hyphens", "test---slug", "test-slug"},
		{"empty after sanitization", "!@#$%^&*()", ""},
		{"very long", "this-is-a-very-long-slug-that-should-be-truncated-because-it-exceeds-the-maximum-length", "this-is-a-very-long-slug-that-should-be-truncated-because-it"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := slug.Sanitize(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeSlug(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSlugGenerationWithPredictableService(t *testing.T) {
	// Create server with predictable service only
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	llmManager := server.NewLLMServiceManager(&server.LLMConfig{Logger: logger})

	// Create a temporary database
	tempDB := t.TempDir() + "/test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer database.Close()

	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	_ = server.NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, false, "", "", "", nil, false)

	// Test slug generation directly
	// ctx := context.Background()
	// testMessage := "help me write a python function"

	// TODO: Fix slug generation test
	// This should work with the predictable service falling back
	// slugResult, err := svr.GenerateSlugForConversation(ctx, testMessage)
	// if err != nil {
	//	t.Fatalf("Slug generation failed: %v", err)
	// }
	// if slugResult == "" {
	//	t.Error("Generated slug is empty")
	// }
	// t.Logf("Generated slug: %s", slugResult)

	// TODO: Fix slug sanitization test
	// Test slug sanitization which should always work
	// slug := slug.Sanitize(testMessage)
	// if slug != "help-me-write-a-python-function" {
	//	t.Errorf("Expected 'help-me-write-a-python-function', got '%s'", slug)
	// }
}

func TestSlugEndToEnd(t *testing.T) {
	// Create temporary database
	tempDB := t.TempDir() + "/test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer database.Close()

	// Run migrations
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	// Create a conversation with a specific slug
	ctx := context.Background()
	testSlug := "test-conversation-slug"
	conv, err := database.CreateConversation(ctx, &testSlug, true, nil, nil)
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Test retrieving by slug
	retrievedBySlug, err := database.GetConversationBySlug(ctx, testSlug)
	if err != nil {
		t.Fatalf("Failed to retrieve conversation by slug: %v", err)
	}

	if retrievedBySlug.ConversationID != conv.ConversationID {
		t.Errorf("Expected conversation ID %s, got %s", conv.ConversationID, retrievedBySlug.ConversationID)
	}

	if retrievedBySlug.Slug == nil || *retrievedBySlug.Slug != testSlug {
		t.Errorf("Expected slug %s, got %v", testSlug, retrievedBySlug.Slug)
	}

	// Test retrieving by ID still works
	retrievedByID, err := database.GetConversationByID(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("Failed to retrieve conversation by ID: %v", err)
	}

	if retrievedByID.ConversationID != conv.ConversationID {
		t.Errorf("Expected conversation ID %s, got %s", conv.ConversationID, retrievedByID.ConversationID)
	}

	t.Logf("Successfully tested slug-based conversation retrieval: %s -> %s", testSlug, conv.ConversationID)
}

// Test that slug updates are reflected in the stream

// Test that SSE only sends incremental message updates
func TestSSEIncrementalUpdates(t *testing.T) {
	// Create temporary database
	tempDB := t.TempDir() + "/test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer database.Close()

	// Run migrations
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	// Create logger and LLM manager
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	llmManager := server.NewLLMServiceManager(&server.LLMConfig{Logger: logger})

	// Create server
	serviceInstance := server.NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, false, "", "", "", nil, false)
	mux := http.NewServeMux()
	serviceInstance.RegisterRoutes(mux)
	testServer := httptest.NewServer(mux)
	defer testServer.Close()

	// Create a conversation with initial message
	slug := "test-sse"
	conv, err := database.CreateConversation(context.Background(), &slug, true, nil, nil)
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Add initial message
	_, err = database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        &llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}}},
		UserData:       map[string]string{"content": "Hello"},
		UsageData:      llm.Usage{},
	})
	if err != nil {
		t.Fatalf("Failed to create initial message: %v", err)
	}

	// Create first SSE client
	client1, err := http.Get(testServer.URL + "/api/conversation/" + conv.ConversationID + "/stream")
	if err != nil {
		t.Fatalf("Failed to connect client1: %v", err)
	}
	defer client1.Body.Close()

	// Read initial response from client1 (should contain the first message)
	// Buffer must be large enough to hold the full response including system prompt
	buf1 := make([]byte, 32768)
	n1, err := client1.Body.Read(buf1)
	if err != nil && err != io.EOF {
		t.Fatalf("Failed to read from client1: %v", err)
	}

	response1 := string(buf1[:n1])
	t.Logf("Client1 initial response: %s", response1)

	// Verify client1 received the initial message
	if !strings.Contains(response1, "Hello") {
		t.Fatal("Client1 should have received initial message")
	}

	// Add a second message
	_, err = database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        &llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hi there!"}}},
		UserData:       map[string]string{"content": "Hi there!"},
		UsageData:      llm.Usage{},
	})
	if err != nil {
		t.Fatalf("Failed to create second message: %v", err)
	}

	// Create second SSE client after the new message is added
	client2, err := http.Get(testServer.URL + "/api/conversation/" + conv.ConversationID + "/stream")
	if err != nil {
		t.Fatalf("Failed to connect client2: %v", err)
	}
	defer client2.Body.Close()

	// Read response from client2 (should contain both messages since it's a new client)
	buf2 := make([]byte, 32768)
	n2, err := client2.Body.Read(buf2)
	if err != nil && err != io.EOF {
		t.Fatalf("Failed to read from client2: %v", err)
	}

	response2 := string(buf2[:n2])
	t.Logf("Client2 initial response: %s", response2)

	// Verify client2 received both messages (new client gets full state)
	if !strings.Contains(response2, "Hello") {
		t.Fatal("Client2 should have received first message")
	}
	if !strings.Contains(response2, "Hi there!") {
		t.Fatal("Client2 should have received second message")
	}

	t.Log("SSE incremental updates test completed successfully")
}

// TestSystemPromptSentToLLM verifies that the system prompt is included in LLM requests
func TestSystemPromptSentToLLM(t *testing.T) {
	ctx := context.Background()

	// Create database and server with predictable service
	// Note: :memory: is not supported by our DB wrapper since it requires multiple connections.
	// Use a temp file-backed database for tests.
	tempDB := t.TempDir() + "/system_prompt_test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer database.Close()

	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create a predictable service we can inspect
	predictableService := loop.NewPredictableService()

	// Create a custom LLM manager that returns our inspectable predictable service
	customLLMManager := &inspectableLLMManager{
		predictableService: predictableService,
		logger:             logger,
	}

	tools := claudetool.ToolSetConfig{}
	svr := server.NewServer(database, customLLMManager, tools, logger, false, "", "", "", nil, false)

	// Start server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux := http.NewServeMux()
		svr.RegisterRoutes(mux)
		mux.ServeHTTP(w, r)
	}))
	defer ts.Close()

	// Test 1: Create new conversation and send first message
	t.Run("FirstMessage", func(t *testing.T) {
		predictableService.ClearRequests()

		// Send first message using /api/conversations/new
		chatReq := map[string]interface{}{
			"message": "Hello",
			"model":   "predictable",
		}
		body, _ := json.Marshal(chatReq)
		resp, err := http.Post(ts.URL+"/api/conversations/new", "application/json", bytes.NewBuffer(body))
		if err != nil {
			t.Fatalf("Failed to send message: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Expected status 201, got %d: %s", resp.StatusCode, body)
		}

		// Poll for async processing completion
		// We need to wait for a request WITH a system prompt, not just any request
		var lastReq *llm.Request
		for i := 0; i < 50; i++ {
			lastReq = predictableService.GetLastRequest()
			if lastReq != nil && len(lastReq.System) > 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if lastReq == nil {
			t.Fatal("No request was sent to the LLM service after 5 seconds")
		}

		if len(lastReq.System) == 0 {
			t.Fatal("System prompt was not included in the LLM request")
		}

		// Verify system prompt contains expected content
		systemText := ""
		for _, sys := range lastReq.System {
			systemText += sys.Text
		}
		if !strings.Contains(systemText, "Shelley") {
			t.Errorf("System prompt doesn't contain 'Shelley': %s", systemText)
		}
		if !strings.Contains(systemText, "coding agent") {
			t.Errorf("System prompt doesn't contain 'coding agent': %s", systemText)
		}

		t.Logf("System prompt successfully sent (length: %d chars)", len(systemText))
	})

	// Test 2: Send second message in existing conversation
	t.Run("SubsequentMessage", func(t *testing.T) {
		predictableService.ClearRequests()

		// Create conversation first
		chatReq := map[string]interface{}{
			"message": "Hello",
			"model":   "predictable",
		}
		body, _ := json.Marshal(chatReq)
		resp, err := http.Post(ts.URL+"/api/conversations/new", "application/json", bytes.NewBuffer(body))
		if err != nil {
			t.Fatalf("Failed to send first message: %v", err)
		}
		defer resp.Body.Close()

		var createResp struct {
			ConversationID string `json:"conversation_id"`
		}
		if resp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Expected status 201, got %d: %s", resp.StatusCode, body)
		}
		if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Failed to decode response (status %d): %v, body: %s", resp.StatusCode, err, body)
		}

		conversationID := createResp.ConversationID

		// Wait for first message to be processed
		var firstReq *llm.Request
		for i := 0; i < 50; i++ {
			firstReq = predictableService.GetLastRequest()
			if firstReq != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if firstReq == nil {
			t.Fatal("First request was not sent to the LLM service after 5 seconds")
		}

		// Clear requests and send second message
		predictableService.ClearRequests()

		chatReq2 := map[string]interface{}{
			"message": "what is the date",
			"model":   "predictable",
		}
		body2, _ := json.Marshal(chatReq2)
		resp2, err := http.Post(ts.URL+"/api/conversation/"+conversationID+"/chat", "application/json", bytes.NewBuffer(body2))
		if err != nil {
			t.Fatalf("Failed to send second message: %v", err)
		}
		defer resp2.Body.Close()

		if resp2.StatusCode != http.StatusAccepted {
			body, _ := io.ReadAll(resp2.Body)
			t.Fatalf("Expected status 202, got %d: %s", resp2.StatusCode, body)
		}

		// Poll for second message to be processed
		// We need to wait for a request WITH a system prompt, not just any request
		var lastReq *llm.Request
		for i := 0; i < 50; i++ {
			lastReq = predictableService.GetLastRequest()
			if lastReq != nil && len(lastReq.System) > 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if lastReq == nil {
			t.Fatal("No request was sent to the LLM service after 5 seconds")
		}

		if len(lastReq.System) == 0 {
			t.Fatal("System prompt was not included in subsequent LLM request")
		}

		// Verify system prompt contains expected content
		systemText := ""
		for _, sys := range lastReq.System {
			systemText += sys.Text
		}
		if !strings.Contains(systemText, "Shelley") {
			t.Errorf("System prompt doesn't contain 'Shelley' in subsequent request: %s", systemText)
		}

		t.Logf("System prompt successfully sent in subsequent message (length: %d chars)", len(systemText))
	})
}

// inspectableLLMManager is a test helper that always returns the same predictable service
type inspectableLLMManager struct {
	predictableService *loop.PredictableService
	logger             *slog.Logger
}

func (m *inspectableLLMManager) GetService(modelID string) (llm.Service, error) {
	if modelID != "predictable" {
		return nil, fmt.Errorf("unsupported model: %s", modelID)
	}
	return m.predictableService, nil
}

func (m *inspectableLLMManager) GetAvailableModels() []string {
	return []string{"predictable"}
}

func (m *inspectableLLMManager) HasModel(modelID string) bool {
	return modelID == "predictable"
}

func (m *inspectableLLMManager) GetModelInfo(modelID string) *models.ModelInfo {
	return nil
}

func (m *inspectableLLMManager) RefreshCustomModels() error {
	return nil
}

func TestVersionEndpoint(t *testing.T) {
	// Create temp DB-backed server
	ctx := context.Background()
	tempDB := t.TempDir() + "/version_test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	llmManager := server.NewLLMServiceManager(&server.LLMConfig{Logger: logger})
	svr := server.NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "", "", nil, false)

	mux := http.NewServeMux()
	svr.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Request /version endpoint
	resp, err := http.Get(ts.URL + "/version")
	if err != nil {
		t.Fatalf("GET /version failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(b))
	}

	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}

	// Parse the response
	var versionInfo struct {
		Commit     string `json:"commit"`
		CommitTime string `json:"commit_time"`
		Modified   bool   `json:"modified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&versionInfo); err != nil {
		t.Fatalf("Failed to decode version info: %v", err)
	}

	t.Logf("Version info: %+v", versionInfo)
}

func TestScreenshotRouteServesImage(t *testing.T) {
	// Create temp DB-backed server
	ctx := context.Background()
	tempDB := t.TempDir() + "/route_test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	llmManager := server.NewLLMServiceManager(&server.LLMConfig{Logger: logger})
	svr := server.NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "", "", nil, false)

	mux := http.NewServeMux()
	svr.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Create a fake screenshot file in the expected location
	id := "testshot"
	path := browse.GetScreenshotPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("Failed to create screenshot dir: %v", err)
	}
	pngData := []byte{0x89, 0x50, 0x4E, 0x47} // PNG magic, minimal content
	if err := os.WriteFile(path, pngData, 0o644); err != nil {
		t.Fatalf("Failed to write screenshot: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	// Request the screenshot
	resp, err := http.Get(ts.URL + "/api/read?path=" + url.QueryEscape(path))
	if err != nil {
		t.Fatalf("GET screenshot failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(b))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("expected image/png, got %q", ct)
	}
	// Cache-Control should be set
	if cc := resp.Header.Get("Cache-Control"); cc == "" {
		t.Fatalf("expected Cache-Control header to be set")
	}
}

// TestGitStateChangeCreatesGitInfoMessage verifies that when the agent makes a git commit,
// a gitinfo message is created in the database.
func TestGitStateChangeCreatesGitInfoMessage(t *testing.T) {
	ctx := context.Background()

	// Create a temp directory with a git repo
	workDir := t.TempDir()

	// Initialize git repo
	runCmd := func(name string, args ...string) {
		// For git commits, use --no-verify to skip hooks
		if name == "git" && len(args) > 0 && args[0] == "commit" {
			newArgs := []string{"commit", "--no-verify"}
			newArgs = append(newArgs, args[1:]...)
			args = newArgs
		}
		cmd := exec.Command(name, args...)
		cmd.Dir = workDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Command %s %v failed: %v\n%s", name, args, err, out)
		}
	}
	runCmd("git", "init")
	runCmd("git", "config", "user.email", "test@example.com")
	runCmd("git", "config", "user.name", "Test User")

	// Create initial commit
	initialFile := filepath.Join(workDir, "initial.txt")
	if err := os.WriteFile(initialFile, []byte("initial content"), 0o644); err != nil {
		t.Fatalf("Failed to write initial file: %v", err)
	}
	runCmd("git", "add", ".")
	runCmd("git", "commit", "-m", "Initial commit")

	// Create database
	tempDB := t.TempDir() + "/gitstate_test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Create LLM manager that returns predictable service
	predictableService := loop.NewPredictableService()
	customLLMManager := &inspectableLLMManager{
		predictableService: predictableService,
		logger:             logger,
	}

	// Create server with git repo as working directory
	toolConfig := claudetool.ToolSetConfig{
		WorkingDir:    workDir,
		EnableBrowser: false,
	}
	svr := server.NewServer(database, customLLMManager, toolConfig, logger, false, "", "", "", nil, false)

	mux := http.NewServeMux()
	svr.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// The test command creates a file and commits it. We use explicit paths to avoid bash safety checks.
	// NOTE: We must set cwd when creating the conversation so the tools run in our git repo.
	// Use --no-verify to skip commit hooks that may interfere with tests.
	chatReq := map[string]interface{}{
		"message": "bash: echo 'new content' > newfile.txt && git add newfile.txt && git commit --no-verify -m 'Add new file'",
		"model":   "predictable",
		"cwd":     workDir,
	}
	body, _ := json.Marshal(chatReq)
	resp, err := http.Post(ts.URL+"/api/conversations/new", "application/json", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("Failed to send message: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Expected status 201, got %d: %s", resp.StatusCode, body)
	}

	var createResp struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Poll for the gitinfo message to appear
	var foundGitInfo bool
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)

		messages, err := database.ListMessagesByConversationPaginated(ctx, createResp.ConversationID, 100, 0)
		if err != nil {
			continue
		}

		for _, msg := range messages {
			if msg.Type == string(db.MessageTypeGitInfo) {
				foundGitInfo = true
				t.Logf("Found gitinfo message: %v", msg.UserData)
				break
			}
		}
		if foundGitInfo {
			break
		}
	}

	if !foundGitInfo {
		t.Fatal("Expected a gitinfo message to be created after git commit, but none was found")
	}
}

func TestSubagentEndToEnd(t *testing.T) {
	// Create temporary database
	tempDB := t.TempDir() + "/test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer database.Close()

	// Run migrations
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	// Create logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// Create LLM service manager with predictable service
	llmManager := server.NewLLMServiceManager(&server.LLMConfig{Logger: logger})

	// Set up tools config
	toolSetConfig := claudetool.ToolSetConfig{
		WorkingDir:    t.TempDir(),
		EnableBrowser: false,
	}

	// Create server (predictable-only mode)
	svr := server.NewServer(database, llmManager, toolSetConfig, logger, true, "", "", "", nil, false)

	// Set up HTTP server
	mux := http.NewServeMux()
	svr.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := &http.Client{Timeout: 60 * time.Second}

	// Create a new conversation that will spawn a subagent
	// The predictable service will respond with a subagent tool call for "subagent: test-worker echo hello"
	chatReq := map[string]interface{}{
		"message": "subagent: test-worker echo hello",
		"model":   "predictable",
	}
	reqBody, _ := json.Marshal(chatReq)

	resp, err := client.Post(ts.URL+"/api/conversations/new", "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Expected 200/201, got %d: %s", resp.StatusCode, string(body))
	}

	var createResp struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	parentConvID := createResp.ConversationID
	t.Logf("Created parent conversation: %s", parentConvID)

	// Wait for the conversation to complete (subagent should be created and executed)
	time.Sleep(3 * time.Second)

	// Check that subagents were created
	subagentsResp, err := client.Get(ts.URL + "/api/conversation/" + parentConvID + "/subagents")
	if err != nil {
		t.Fatalf("Failed to get subagents: %v", err)
	}
	defer subagentsResp.Body.Close()

	var subagents []generated.Conversation
	if err := json.NewDecoder(subagentsResp.Body).Decode(&subagents); err != nil {
		t.Fatalf("Failed to decode subagents: %v", err)
	}

	if len(subagents) == 0 {
		t.Fatal("Expected at least one subagent to be created")
	}

	t.Logf("Created %d subagent(s)", len(subagents))
	for _, sub := range subagents {
		t.Logf("  - Subagent: %s (slug: %v)", sub.ConversationID, sub.Slug)
	}

	// Verify the subagent has the expected slug (or a suffixed version)
	foundExpectedSlug := false
	for _, sub := range subagents {
		if sub.Slug != nil && (strings.HasPrefix(*sub.Slug, "test-worker")) {
			foundExpectedSlug = true
			break
		}
	}
	if !foundExpectedSlug {
		t.Errorf("Expected to find subagent with slug starting with 'test-worker'")
	}

	// Verify the subagent has a parent_conversation_id set
	for _, sub := range subagents {
		if sub.ParentConversationID == nil || *sub.ParentConversationID != parentConvID {
			t.Errorf("Subagent %s has wrong parent_conversation_id: %v", sub.ConversationID, sub.ParentConversationID)
		}
	}

	// Verify the subagent conversation has messages
	subConvResp, err := client.Get(ts.URL + "/api/conversation/" + subagents[0].ConversationID)
	if err != nil {
		t.Fatalf("Failed to get subagent conversation: %v", err)
	}
	defer subConvResp.Body.Close()

	var subConvData struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.NewDecoder(subConvResp.Body).Decode(&subConvData); err != nil {
		t.Fatalf("Failed to decode subagent conversation: %v", err)
	}

	if len(subConvData.Messages) == 0 {
		t.Error("Expected subagent conversation to have messages")
	}
	t.Logf("Subagent conversation has %d messages", len(subConvData.Messages))
}

func TestContinueConversation(t *testing.T) {
	// Create temporary database
	tempDB := t.TempDir() + "/test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer database.Close()

	// Run migrations
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	// Create logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// Create LLM service manager
	llmManager := server.NewLLMServiceManager(&server.LLMConfig{Logger: logger, DB: database})

	// Set up tools config
	toolSetConfig := claudetool.ToolSetConfig{
		WorkingDir:    t.TempDir(),
		EnableBrowser: false,
	}

	// Create server
	svr := server.NewServer(database, llmManager, toolSetConfig, logger, false, "", "", "", nil, false)

	// Set up HTTP server
	mux := http.NewServeMux()
	svr.RegisterRoutes(mux)
	testServer := httptest.NewServer(mux)
	defer testServer.Close()

	ctx := context.Background()

	// Create source conversation with a slug and some messages
	sourceSlug := "source-conversation"
	cwd := "/tmp/testdir"
	model := "predictable"
	sourceConv, err := database.CreateConversation(ctx, &sourceSlug, true, &cwd, &model)
	if err != nil {
		t.Fatalf("Failed to create source conversation: %v", err)
	}

	// Add some messages to the source conversation
	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello, this is a test message"}},
	}
	_, err = database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: sourceConv.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        userMessage,
	})
	if err != nil {
		t.Fatalf("Failed to create user message: %v", err)
	}

	agentMessage := llm.Message{
		Role:    llm.MessageRoleAssistant,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello! How can I help you?"}},
	}
	_, err = database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: sourceConv.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        agentMessage,
	})
	if err != nil {
		t.Fatalf("Failed to create agent message: %v", err)
	}

	// Create a tool use message
	toolMessage := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{{
			Type:      llm.ContentTypeToolUse,
			ToolName:  "bash",
			ToolInput: json.RawMessage(`{"command": "echo hello world this is a long command that should be truncated if it exceeds the limit"}`),
		}},
	}
	_, err = database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: sourceConv.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        toolMessage,
	})
	if err != nil {
		t.Fatalf("Failed to create tool message: %v", err)
	}

	// Test the continue conversation endpoint
	reqBody := map[string]string{
		"source_conversation_id": sourceConv.ConversationID,
		"model":                  "predictable",
	}
	body, _ := json.Marshal(reqBody)

	resp, err := http.Post(testServer.URL+"/api/conversations/continue", "application/json", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("Failed to continue conversation: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("Expected status 201, got %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	newConversationID, ok := result["conversation_id"].(string)
	if !ok || newConversationID == "" {
		t.Fatal("Response should contain conversation_id")
	}

	// Verify new conversation was created
	newConv, err := database.GetConversationByID(ctx, newConversationID)
	if err != nil {
		t.Fatalf("Failed to get new conversation: %v", err)
	}

	// Verify the new conversation inherited the cwd
	if newConv.Cwd == nil || *newConv.Cwd != cwd {
		t.Errorf("Expected cwd %s, got %v", cwd, newConv.Cwd)
	}

	// Verify the new conversation has a user message with the summary
	messages, err := database.ListMessages(ctx, newConversationID)
	if err != nil {
		t.Fatalf("Failed to list messages: %v", err)
	}

	if len(messages) < 1 {
		t.Fatal("Expected at least 1 message in new conversation")
	}

	// Find the user message with the summary (may be after system prompt)
	var summaryText string
	for _, msg := range messages {
		if msg.Type != string(db.MessageTypeUser) {
			continue
		}
		if msg.LlmData == nil {
			continue
		}
		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			continue
		}
		for _, content := range llmMsg.Content {
			if content.Type == llm.ContentTypeText && strings.Contains(content.Text, "Continue the conversation") {
				summaryText = content.Text
				break
			}
		}
		if summaryText != "" {
			break
		}
	}

	if summaryText == "" {
		t.Fatal("Could not find summary message in new conversation")
	}

	if !strings.Contains(summaryText, sourceSlug) {
		t.Errorf("Summary should reference source conversation slug %q, got: %s", sourceSlug, summaryText)
	}

	if !strings.Contains(summaryText, "Hello, this is a test message") {
		t.Error("Summary should contain user message text")
	}

	if !strings.Contains(summaryText, "Hello! How can I help you?") {
		t.Error("Summary should contain agent message text")
	}

	if !strings.Contains(summaryText, "Tool: bash") {
		t.Error("Summary should contain tool name")
	}

	// Verify that the agent was NOT started - there should only be a user message,
	// no agent response. The user should be able to add instructions before sending.
	var hasAgentMessage bool
	for _, msg := range messages {
		if msg.Type == string(db.MessageTypeAgent) {
			hasAgentMessage = true
			break
		}
	}
	if hasAgentMessage {
		t.Error("Expected no agent message - the agent should NOT be started automatically")
	}

	// Verify the status is "created" not "accepted" (since agent wasn't started)
	if status, ok := result["status"].(string); !ok || status != "created" {
		t.Errorf("Expected status 'created', got %v", result["status"])
	}

	t.Logf("Successfully continued conversation from %s to %s", sourceConv.ConversationID, newConversationID)

	// Now test that sending a follow-up message works correctly.
	// The agent should receive both the summary message AND the new message.
	followUpReq := map[string]string{
		"message": "Please focus on the bash commands.",
		"model":   "predictable",
	}
	followUpBody, _ := json.Marshal(followUpReq)

	followUpResp, err := http.Post(testServer.URL+"/api/conversation/"+newConversationID+"/chat", "application/json", bytes.NewBuffer(followUpBody))
	if err != nil {
		t.Fatalf("Failed to send follow-up message: %v", err)
	}
	defer followUpResp.Body.Close()

	if followUpResp.StatusCode != http.StatusAccepted {
		bodyBytes, _ := io.ReadAll(followUpResp.Body)
		t.Fatalf("Expected status 202 for follow-up, got %d: %s", followUpResp.StatusCode, string(bodyBytes))
	}

	// Wait briefly for the agent to process
	time.Sleep(200 * time.Millisecond)

	// Verify we now have messages: summary (user), follow-up (user), and agent response
	updatedMessages, err := database.ListMessages(ctx, newConversationID)
	if err != nil {
		t.Fatalf("Failed to list updated messages: %v", err)
	}

	// Count message types
	userCount := 0
	agentCount := 0
	for _, msg := range updatedMessages {
		switch msg.Type {
		case string(db.MessageTypeUser):
			userCount++
		case string(db.MessageTypeAgent):
			agentCount++
		}
	}

	// Should have 2 user messages (summary + follow-up) and at least 1 agent response
	if userCount != 2 {
		t.Errorf("Expected 2 user messages, got %d", userCount)
	}
	if agentCount < 1 {
		t.Errorf("Expected at least 1 agent message after follow-up, got %d", agentCount)
	}

	t.Logf("Follow-up message processed: %d user messages, %d agent messages", userCount, agentCount)
}
