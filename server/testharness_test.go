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
)

// TestHarness provides a DSL-like interface for testing conversations.
type TestHarness struct {
	t              *testing.T
	db             *db.DB
	server         *Server
	cleanup        func()
	llm            *loop.PredictableService
	convID         string
	timeout        time.Duration
	responsesCount int // Number of agent responses seen so far
}

// NewTestHarness creates a new test harness with a predictable LLM and bash tool.
func NewTestHarness(t *testing.T) *TestHarness {
	t.Helper()

	database, cleanup := setupTestDB(t)

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

	toolSetConfig := claudetool.ToolSetConfig{EnableBrowser: false}
	server := NewServer(database, llmManager, toolSetConfig, logger, true, "", "predictable", "", nil)

	return &TestHarness{
		t:       t,
		db:      database,
		server:  server,
		cleanup: cleanup,
		llm:     predictableService,
		timeout: 5 * time.Second,
	}
}

// Close cleans up the test harness resources.
func (h *TestHarness) Close() {
	h.cleanup()
}

// NewConversation starts a new conversation with the given message and options.
func (h *TestHarness) NewConversation(msg, cwd string) *TestHarness {
	h.t.Helper()

	chatReq := ChatRequest{
		Message: msg,
		Model:   "predictable",
		Cwd:     cwd,
	}
	chatBody, _ := json.Marshal(chatReq)

	req := httptest.NewRequest("POST", "/api/conversations/new", strings.NewReader(string(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleNewConversation(w, req)
	if w.Code != http.StatusCreated {
		h.t.Fatalf("NewConversation: expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("NewConversation: failed to parse response: %v", err)
	}
	h.convID = resp.ConversationID
	h.responsesCount = 0 // Reset for new conversation
	return h
}

// Chat sends a message to the current conversation.
func (h *TestHarness) Chat(msg string) *TestHarness {
	h.t.Helper()

	if h.convID == "" {
		h.t.Fatal("Chat: no conversation started, call NewConversation first")
	}

	chatReq := ChatRequest{
		Message: msg,
		Model:   "predictable",
	}
	chatBody, _ := json.Marshal(chatReq)

	req := httptest.NewRequest("POST", "/api/conversation/"+h.convID+"/chat", strings.NewReader(string(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleChatConversation(w, req, h.convID)
	if w.Code != http.StatusAccepted {
		h.t.Fatalf("Chat: expected status 202, got %d: %s", w.Code, w.Body.String())
	}
	return h
}

// WaitToolResult waits for a tool result and returns its text content.
func (h *TestHarness) WaitToolResult() string {
	h.t.Helper()

	if h.convID == "" {
		h.t.Fatal("WaitToolResult: no conversation started")
	}

	deadline := time.Now().Add(h.timeout)
	for time.Now().Before(deadline) {
		var messages []generated.Message
		err := h.db.Queries(context.Background(), func(q *generated.Queries) error {
			var qerr error
			messages, qerr = q.ListMessages(context.Background(), h.convID)
			return qerr
		})
		if err != nil {
			h.t.Fatalf("WaitToolResult: failed to get messages: %v", err)
		}

		for _, msg := range messages {
			if msg.Type != string(db.MessageTypeUser) || msg.LlmData == nil {
				continue
			}

			var llmMsg llm.Message
			if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
				continue
			}

			for _, content := range llmMsg.Content {
				if content.Type == llm.ContentTypeToolResult {
					for _, result := range content.ToolResult {
						if result.Type == llm.ContentTypeText && result.Text != "" {
							return result.Text
						}
					}
				}
			}
		}

		time.Sleep(100 * time.Millisecond)
	}

	h.t.Fatalf("WaitToolResult: timed out waiting for tool result")
	return ""
}

// WaitResponse waits for the assistant's text response (end of turn).
// It waits for a NEW response that hasn't been seen before.
func (h *TestHarness) WaitResponse() string {
	h.t.Helper()

	if h.convID == "" {
		h.t.Fatal("WaitResponse: no conversation started")
	}

	targetCount := h.responsesCount + 1

	deadline := time.Now().Add(h.timeout)
	for time.Now().Before(deadline) {
		var messages []generated.Message
		err := h.db.Queries(context.Background(), func(q *generated.Queries) error {
			var qerr error
			messages, qerr = q.ListMessages(context.Background(), h.convID)
			return qerr
		})
		if err != nil {
			h.t.Fatalf("WaitResponse: failed to get messages: %v", err)
		}

		// Count assistant messages with end_of_turn
		count := 0
		var lastText string
		for _, msg := range messages {
			if msg.Type != string(db.MessageTypeAgent) || msg.LlmData == nil {
				continue
			}

			var llmMsg llm.Message
			if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
				continue
			}

			if llmMsg.EndOfTurn {
				count++
				for _, content := range llmMsg.Content {
					if content.Type == llm.ContentTypeText {
						lastText = content.Text
						break
					}
				}
			}
		}

		if count >= targetCount {
			h.responsesCount = count
			return lastText
		}

		time.Sleep(100 * time.Millisecond)
	}

	h.t.Fatalf("WaitResponse: timed out waiting for response (seen %d, need %d)", h.responsesCount, targetCount)
	return ""
}

// ConversationID returns the current conversation ID.
func (h *TestHarness) ConversationID() string {
	return h.convID
}

// GetContextWindowSize retrieves the current context window size from the server.
func (h *TestHarness) GetContextWindowSize() uint64 {
	h.t.Helper()

	if h.convID == "" {
		h.t.Fatal("GetContextWindowSize: no conversation started")
	}

	// Use handleGetConversation (GET /conversation/<id>) instead of stream endpoint
	req := httptest.NewRequest("GET", "/api/conversation/"+h.convID, nil)
	w := httptest.NewRecorder()

	h.server.handleGetConversation(w, req, h.convID)
	if w.Code != http.StatusOK {
		h.t.Fatalf("GetContextWindowSize: expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp StreamResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("GetContextWindowSize: failed to parse response: %v", err)
	}

	return resp.ContextWindowSize
}
