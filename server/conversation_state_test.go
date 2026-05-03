package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
)

// responseRecorderWithClose wraps httptest.ResponseRecorder to support
// CloseNotify and concurrent reads of the response body while the handler
// is still writing (via Snapshot).
type responseRecorderWithClose struct {
	*httptest.ResponseRecorder
	closeNotify chan bool
	mu          sync.Mutex
}

func newResponseRecorderWithClose() *responseRecorderWithClose {
	return &responseRecorderWithClose{
		ResponseRecorder: httptest.NewRecorder(),
		closeNotify:      make(chan bool, 1),
	}
}

func (r *responseRecorderWithClose) CloseNotify() <-chan bool {
	return r.closeNotify
}

func (r *responseRecorderWithClose) Close() {
	select {
	case r.closeNotify <- true:
	default:
	}
}

// Write serializes writes so Snapshot can safely read the body while the
// handler is still running.
func (r *responseRecorderWithClose) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(p)
}

// Snapshot returns a copy of the current body, safe to call concurrently
// with Write.
func (r *responseRecorderWithClose) Snapshot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Body.String()
}

// waitForSSEData polls the recorder's body until it contains an SSE frame
// (or an error response), returning the body snapshot. Used to avoid flaky
// fixed sleeps on slow CI.
func waitForSSEData(t *testing.T, r *responseRecorderWithClose, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body := r.Snapshot()
		if strings.HasPrefix(body, "data: ") && strings.Contains(body, "\n\n") {
			return body
		}
		// Error responses from http.Error have no "data:" prefix; return those
		// so the caller can surface a useful failure message.
		if body != "" && !strings.HasPrefix(body, "data: ") {
			return body
		}
		time.Sleep(10 * time.Millisecond)
	}
	return r.Snapshot()
}

// TestConversationStateAfterServerRestart verifies that when a conversation is
// loaded after a server restart (new manager created), the agent is correctly
// reported as not working since the loop isn't running.
func TestConversationStateAfterServerRestart(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	// Create a conversation with some messages (simulating previous activity)
	conv, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Add a user message
	userMsg := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}},
	}
	_, err = database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        userMsg,
	})
	if err != nil {
		t.Fatalf("Failed to create user message: %v", err)
	}

	// Add an agent message (without end_of_turn to simulate mid-conversation)
	agentMsg := llm.Message{
		Role:      llm.MessageRoleAssistant,
		Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "Hi there!"}},
		EndOfTurn: false,
	}
	_, err = database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        agentMsg,
	})
	if err != nil {
		t.Fatalf("Failed to create agent message: %v", err)
	}

	// Create a NEW server (simulating server restart - no active managers)
	ps := loop.NewPredictableService()
	server := NewServer(database, &testLLMManager{service: ps},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		true, "predictable", "")

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	// Make a streaming request with a context that cancels after we read the first message
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req := httptest.NewRequest("GET", "/api/conversation/"+conv.ConversationID+"/stream", nil).WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")

	w := newResponseRecorderWithClose()

	// Run handler in goroutine and close connection after getting first response
	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(w, req)
	}()

	// Wait until the handler has written the first SSE frame, or the context
	// cancels. Polling avoids flakes on slow CI where a fixed 500ms sleep was
	// insufficient for Hydrate + system prompt generation.
	body := waitForSSEData(t, w, 10*time.Second)
	w.Close()
	cancel()

	// Wait for handler to finish
	<-done

	if !strings.HasPrefix(body, "data: ") {
		t.Fatalf("Expected SSE data, got: %s", body)
	}

	jsonData := strings.TrimPrefix(strings.Split(body, "\n")[0], "data: ")
	var response StreamResponse
	if err := json.Unmarshal([]byte(jsonData), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Verify conversation state shows agent is NOT working
	// (because after server restart, no loop is running)
	if response.ConversationState == nil {
		t.Fatal("Expected ConversationState in response")
	}
	if response.ConversationState.ConversationID != conv.ConversationID {
		t.Errorf("Expected ConversationID %s, got %s", conv.ConversationID, response.ConversationState.ConversationID)
	}
	if response.ConversationState.Working {
		t.Error("Expected Working=false after server restart (no active loop)")
	}

	// Verify messages were loaded
	if len(response.Messages) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(response.Messages))
	}
}

// TestModelRestorationAfterServerRestart verifies that when a conversation is
// resumed after a server restart, the model is correctly loaded from the database
// and reported in the ConversationState.
func TestModelRestorationAfterServerRestart(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	// Create a conversation with a specific model
	modelID := "claude-sonnet-4.6"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelID, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Add a user message
	userMsg := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}},
	}
	_, err = database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        userMsg,
	})
	if err != nil {
		t.Fatalf("Failed to create user message: %v", err)
	}

	// Add an agent message
	agentMsg := llm.Message{
		Role:      llm.MessageRoleAssistant,
		Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "Hi there!"}},
		EndOfTurn: true,
	}
	_, err = database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        agentMsg,
	})
	if err != nil {
		t.Fatalf("Failed to create agent message: %v", err)
	}

	// Create a NEW server (simulating server restart - no active managers)
	ps := loop.NewPredictableService()
	server := NewServer(database, &testLLMManager{service: ps},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		true, "predictable", "")

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	// Make a streaming request
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req := httptest.NewRequest("GET", "/api/conversation/"+conv.ConversationID+"/stream", nil).WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")

	w := newResponseRecorderWithClose()

	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(w, req)
	}()

	body := waitForSSEData(t, w, 10*time.Second)
	w.Close()
	cancel()
	<-done

	if !strings.HasPrefix(body, "data: ") {
		t.Fatalf("Expected SSE data, got: %s", body)
	}

	jsonData := strings.TrimPrefix(strings.Split(body, "\n")[0], "data: ")
	var response StreamResponse
	if err := json.Unmarshal([]byte(jsonData), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Verify conversation state includes the model from the database
	if response.ConversationState == nil {
		t.Fatal("Expected ConversationState in response")
	}
	if response.ConversationState.Model != modelID {
		t.Errorf("Expected Model='%s', got '%s'", modelID, response.ConversationState.Model)
	}
}
