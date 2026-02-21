package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
	"shelley.exe.dev/models"
)

// setupTestDB creates a test database
func setupTestDB(t *testing.T) (*db.DB, func()) {
	t.Helper()
	tmpDir := t.TempDir()
	database, err := db.New(db.Config{DSN: tmpDir + "/test.db"})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Failed to migrate test database: %v", err)
	}

	return database, func() {
		database.Close()
	}
}

// waitFor polls a condition until it returns true or the timeout is reached.
func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

// newTestServer creates a Server with a PredictableService for testing.
func newTestServer(t *testing.T) (*Server, *db.DB, *loop.PredictableService) {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := loop.NewPredictableService()
	svr := NewServer(database, &testLLMManager{service: ps},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		true, "", "predictable", "", nil)
	return svr, database, ps
}

// TestCancelWithPredictableModel tests cancellation with the predictable model
func TestCancelWithPredictableModel(t *testing.T) {
	server, database, _ := newTestServer(t)

	// Create conversation
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Start a conversation with a message that triggers a slow bash command
	chatReq := ChatRequest{
		Message: "bash: sleep 5",
		Model:   "predictable",
	}
	chatBody, _ := json.Marshal(chatReq)

	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleChatConversation(w, req, conversationID)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", w.Code, w.Body.String())
	}

	// Wait for agent to record an assistant message with tool use
	waitFor(t, 5*time.Second, func() bool {
		var messages []generated.Message
		err := database.Queries(context.Background(), func(q *generated.Queries) error {
			var qerr error
			messages, qerr = q.ListMessages(context.Background(), conversationID)
			return qerr
		})
		if err != nil || len(messages) < 2 {
			return false
		}
		// Check for assistant message with tool use
		for _, msg := range messages {
			if msg.Type != string(db.MessageTypeAgent) || msg.LlmData == nil {
				continue
			}
			var llmMsg llm.Message
			if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
				continue
			}
			for _, content := range llmMsg.Content {
				if content.Type == llm.ContentTypeToolUse {
					return true
				}
			}
		}
		return false
	})

	// Cancel the conversation
	cancelReq := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/cancel", nil)
	cancelW := httptest.NewRecorder()

	server.handleCancelConversation(cancelW, cancelReq, conversationID)

	if cancelW.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", cancelW.Code, cancelW.Body.String())
	}

	var cancelResp map[string]string
	if err := json.Unmarshal(cancelW.Body.Bytes(), &cancelResp); err != nil {
		t.Fatalf("failed to parse cancel response: %v", err)
	}

	if cancelResp["status"] != "cancelled" {
		t.Errorf("expected status 'cancelled', got '%s'", cancelResp["status"])
	}

	// Wait for agent to stop working (cancellation complete)
	waitFor(t, 5*time.Second, func() bool {
		return !server.IsAgentWorking(conversationID)
	})

	// Verify that a cancelled tool result was recorded
	var messages []generated.Message
	err = database.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(context.Background(), conversationID)
		return qerr
	})
	if err != nil {
		t.Fatalf("failed to get messages after cancel: %v", err)
	}

	// Should have: user message, assistant message with tool use, cancelled tool result, and end turn message
	if len(messages) < 4 {
		t.Fatalf("expected at least 4 messages after cancel, got %d", len(messages))
	}

	// Check that we have the cancelled tool result
	foundCancelledResult := false
	foundEndTurnMessage := false
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.LlmData == nil {
			continue
		}

		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			continue
		}

		// Check for cancelled tool result
		for _, content := range llmMsg.Content {
			if content.Type == llm.ContentTypeToolResult && content.ToolError {
				for _, result := range content.ToolResult {
					if result.Type == llm.ContentTypeText && strings.Contains(result.Text, "cancelled") {
						foundCancelledResult = true
						break
					}
				}
			}
		}

		// Check for end turn message
		if msg.Type == string(db.MessageTypeAgent) && llmMsg.EndOfTurn {
			for _, content := range llmMsg.Content {
				if content.Type == llm.ContentTypeText && strings.Contains(content.Text, "Operation cancelled") {
					foundEndTurnMessage = true
					break
				}
			}
		}
	}

	if !foundCancelledResult {
		t.Error("expected to find cancelled tool result in conversation")
	}

	if !foundEndTurnMessage {
		t.Error("expected to find end turn message after cancellation")
	}

	// Test that conversation can be resumed after cancellation
	resumeReq := ChatRequest{
		Message: "echo: test after cancel",
		Model:   "predictable",
	}
	resumeBody, _ := json.Marshal(resumeReq)

	resumeChatReq := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(resumeBody)))
	resumeChatReq.Header.Set("Content-Type", "application/json")
	resumeW := httptest.NewRecorder()

	server.handleChatConversation(resumeW, resumeChatReq, conversationID)

	if resumeW.Code != http.StatusAccepted {
		t.Fatalf("expected status 202 for resume, got %d: %s", resumeW.Code, resumeW.Body.String())
	}

	// Wait for agent to finish processing the resumed conversation
	waitFor(t, 5*time.Second, func() bool {
		return !server.IsAgentWorking(conversationID)
	})

	// Verify conversation continued
	err = database.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(context.Background(), conversationID)
		return qerr
	})
	if err != nil {
		t.Fatalf("failed to get messages after resume: %v", err)
	}

	// Should have additional messages from the resumed conversation
	if len(messages) < 5 {
		t.Fatalf("expected at least 5 messages after resume, got %d", len(messages))
	}

	// Check that we got the expected response
	foundContinueResponse := false
	for _, msg := range messages {
		if msg.Type != string(db.MessageTypeAgent) {
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
			if content.Type == llm.ContentTypeText && strings.Contains(content.Text, "test after cancel") {
				foundContinueResponse = true
				break
			}
		}
	}

	if !foundContinueResponse {
		t.Error("expected to find 'test after cancel' response")
	}
}

// TestCancelWithNoActiveConversation tests cancelling when there's no active conversation
func TestCancelWithNoActiveConversation(t *testing.T) {
	server, database, _ := newTestServer(t)

	// Create a conversation but don't start it
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Try to cancel without any active loop
	cancelReq := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/cancel", nil)
	cancelW := httptest.NewRecorder()

	server.handleCancelConversation(cancelW, cancelReq, conversationID)

	if cancelW.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", cancelW.Code, cancelW.Body.String())
	}

	var cancelResp map[string]string
	if err := json.Unmarshal(cancelW.Body.Bytes(), &cancelResp); err != nil {
		t.Fatalf("failed to parse cancel response: %v", err)
	}

	if cancelResp["status"] != "no_active_conversation" {
		t.Errorf("expected status 'no_active_conversation', got '%s'", cancelResp["status"])
	}
}

// TestCancelDuringTextGeneration tests cancelling during text generation (no tool call)
func TestCancelDuringTextGeneration(t *testing.T) {
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Start conversation with a delay to simulate slow text generation
	chatReq := ChatRequest{
		Message: "delay: 2",
		Model:   "predictable",
	}
	chatBody, _ := json.Marshal(chatReq)

	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleChatConversation(w, req, conversationID)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", w.Code, w.Body.String())
	}

	// Wait for agent to start working
	waitFor(t, 5*time.Second, func() bool {
		return server.IsAgentWorking(conversationID)
	})

	// Cancel during text generation
	cancelReq := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/cancel", nil)
	cancelW := httptest.NewRecorder()

	server.handleCancelConversation(cancelW, cancelReq, conversationID)

	if cancelW.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", cancelW.Code, cancelW.Body.String())
	}

	// Wait for agent to stop working (cancellation complete)
	waitFor(t, 5*time.Second, func() bool {
		return !server.IsAgentWorking(conversationID)
	})

	// Verify that no cancelled tool result was added (since there was no tool call)
	var messages []generated.Message
	err = database.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(context.Background(), conversationID)
		return qerr
	})
	if err != nil {
		t.Fatalf("failed to get messages: %v", err)
	}

	// Should only have user message (and possibly incomplete assistant message)
	// Should NOT have a tool result message
	for _, msg := range messages {
		if msg.Type == string(db.MessageTypeUser) {
			if msg.LlmData == nil {
				continue
			}
			var llmMsg llm.Message
			if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
				continue
			}
			for _, content := range llmMsg.Content {
				if content.Type == llm.ContentTypeToolResult {
					t.Error("did not expect tool result when cancelling during text generation")
				}
			}
		}
	}
}

// testLLMManager is a simple test implementation of LLMProvider
type testLLMManager struct {
	service llm.Service
}

func (m *testLLMManager) GetService(modelID string) (llm.Service, error) {
	return m.service, nil
}

func (m *testLLMManager) GetAvailableModels() []string {
	return []string{"predictable"}
}

func (m *testLLMManager) HasModel(modelID string) bool {
	return modelID == "predictable"
}

func (m *testLLMManager) GetModelInfo(modelID string) *models.ModelInfo {
	return nil
}

func (m *testLLMManager) RefreshCustomModels() error {
	return nil
}
