package server

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
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/models"
)

// ClaudeTestHarness extends TestHarness with Claude-specific functionality
type ClaudeTestHarness struct {
	t                *testing.T
	db               *db.DB
	server           *Server
	cleanup          func()
	convID           string
	timeout          time.Duration
	llmService       *ant.Service
	requestTokens    []uint64 // Track total tokens for each request
	lastMessageCount int      // Track message count after last operation
	mu               sync.Mutex
}

// NewClaudeTestHarness creates a test harness that uses the real Claude API
func NewClaudeTestHarness(t *testing.T) *ClaudeTestHarness {
	t.Helper()

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping Claude test")
	}

	database, cleanup := setupTestDB(t)

	// Create Claude service with HTTP recorder to track token usage
	h := &ClaudeTestHarness{
		t:             t,
		db:            database,
		cleanup:       cleanup,
		timeout:       60 * time.Second, // Longer timeout for real API calls
		requestTokens: make([]uint64, 0),
	}

	// Create HTTP client with custom transport for token tracking
	httpc := &http.Client{
		Transport: &tokenTrackingTransport{
			base:        http.DefaultTransport,
			recordToken: h.recordHTTPResponse,
		},
	}

	service := &ant.Service{
		APIKey: apiKey,
		Model:  ant.Claude45Haiku, // Use cheaper model for testing
		HTTPC:  httpc,
	}
	h.llmService = service

	llmManager := &claudeLLMManager{service: service}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Set up tools - bash for testing tool cancellation
	toolSetConfig := claudetool.ToolSetConfig{
		WorkingDir:    t.TempDir(),
		EnableBrowser: false,
	}

	server := NewServer(database, llmManager, toolSetConfig, logger, true, "", "claude", "", nil)
	h.server = server

	return h
}

// tokenTrackingTransport wraps an HTTP transport to track token usage from responses
type tokenTrackingTransport struct {
	base        http.RoundTripper
	recordToken func(responseBody []byte, statusCode int)
}

func (t *tokenTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	// Read and restore the response body
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))

	t.recordToken(body, resp.StatusCode)
	return resp, nil
}

// recordHTTPResponse is a callback to record HTTP responses for token tracking
func (h *ClaudeTestHarness) recordHTTPResponse(responseBody []byte, statusCode int) {
	h.t.Logf("HTTP callback: status=%d, responseLen=%d", statusCode, len(responseBody))

	if statusCode != http.StatusOK || responseBody == nil {
		return
	}

	// Parse response to get token usage (including cache tokens)
	var resp struct {
		Usage struct {
			InputTokens              uint64 `json:"input_tokens"`
			OutputTokens             uint64 `json:"output_tokens"`
			CacheCreationInputTokens uint64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     uint64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if jsonErr := json.Unmarshal(responseBody, &resp); jsonErr == nil {
		// Total tokens = input + cache_creation + cache_read (this represents total context)
		totalTokens := resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens
		h.mu.Lock()
		h.requestTokens = append(h.requestTokens, totalTokens)
		h.mu.Unlock()
		h.t.Logf("Recorded request: input=%d, cache_creation=%d, cache_read=%d, total=%d",
			resp.Usage.InputTokens, resp.Usage.CacheCreationInputTokens, resp.Usage.CacheReadInputTokens, totalTokens)
	} else {
		h.t.Logf("Failed to parse response: %v", jsonErr)
	}
}

// GetRequestTokens returns a copy of recorded request token counts
func (h *ClaudeTestHarness) GetRequestTokens() []uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	tokens := make([]uint64, len(h.requestTokens))
	copy(tokens, h.requestTokens)
	return tokens
}

// VerifyTokensNonDecreasing checks that tokens don't decrease below a baseline
// This verifies that context is being preserved across requests
func (h *ClaudeTestHarness) VerifyTokensNonDecreasing() {
	h.t.Helper()
	tokens := h.GetRequestTokens()
	if len(tokens) == 0 {
		h.t.Log("No tokens recorded, skipping token verification")
		return
	}

	h.t.Logf("Token progression: %v", tokens)

	// Find the baseline (first substantial token count, skipping small slug generation requests)
	// Slug generation requests have ~100-200 tokens, conversation requests have 4000+
	var baseline uint64
	for _, t := range tokens {
		if t > 1000 { // Skip small requests like slug generation
			baseline = t
			break
		}
	}

	if baseline == 0 {
		h.t.Log("No substantial baseline found, skipping token verification")
		return
	}

	// Verify no substantial request drops significantly below baseline (allow 10% variance for caching)
	minAllowed := baseline * 9 / 10
	for i, t := range tokens {
		if t > 1000 && t < minAllowed { // Only check substantial requests
			h.t.Errorf("Token count at index %d dropped significantly: %d < %d (baseline=%d)", i, t, minAllowed, baseline)
		}
	}
}

// Close cleans up the test harness resources
func (h *ClaudeTestHarness) Close() {
	h.cleanup()
}

// NewConversation starts a new conversation with Claude
func (h *ClaudeTestHarness) NewConversation(msg, cwd string) *ClaudeTestHarness {
	h.t.Helper()

	chatReq := ChatRequest{
		Message: msg,
		Model:   "claude",
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

	// Reset lastMessageCount - new conversation starts fresh
	h.mu.Lock()
	h.lastMessageCount = 0
	h.mu.Unlock()

	return h
}

// Chat sends a message to the current conversation
func (h *ClaudeTestHarness) Chat(msg string) *ClaudeTestHarness {
	h.t.Helper()

	if h.convID == "" {
		h.t.Fatal("Chat: no conversation started, call NewConversation first")
	}

	// Record message count before sending
	h.mu.Lock()
	h.lastMessageCount = len(h.GetMessagesUnsafe())
	h.mu.Unlock()

	chatReq := ChatRequest{
		Message: msg,
		Model:   "claude",
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

// GetMessagesUnsafe gets messages without locking (internal use only)
func (h *ClaudeTestHarness) GetMessagesUnsafe() []generated.Message {
	var messages []generated.Message
	h.db.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(context.Background(), h.convID)
		return qerr
	})
	return messages
}

// Cancel cancels the current conversation
func (h *ClaudeTestHarness) Cancel() *ClaudeTestHarness {
	h.t.Helper()

	if h.convID == "" {
		h.t.Fatal("Cancel: no conversation started")
	}

	req := httptest.NewRequest("POST", "/api/conversation/"+h.convID+"/cancel", nil)
	w := httptest.NewRecorder()

	h.server.handleCancelConversation(w, req, h.convID)
	if w.Code != http.StatusOK {
		h.t.Fatalf("Cancel: expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	return h
}

// WaitForAgentWorking waits until the agent is working (tool call started)
func (h *ClaudeTestHarness) WaitForAgentWorking() *ClaudeTestHarness {
	h.t.Helper()

	deadline := time.Now().Add(h.timeout)
	for time.Now().Before(deadline) {
		if h.isAgentWorking() {
			return h
		}
		time.Sleep(100 * time.Millisecond)
	}

	h.t.Fatal("WaitForAgentWorking: timed out waiting for agent to start working")
	return h
}

// isAgentWorking checks if the agent is currently working
func (h *ClaudeTestHarness) isAgentWorking() bool {
	var messages []generated.Message
	err := h.db.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(context.Background(), h.convID)
		return qerr
	})
	if err != nil {
		return false
	}

	// Look for an assistant message with tool use that doesn't have a corresponding result
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Type != string(db.MessageTypeAgent) || msg.LlmData == nil {
			continue
		}

		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			continue
		}

		// Check if this assistant message has tool use
		for _, content := range llmMsg.Content {
			if content.Type == llm.ContentTypeToolUse {
				// Check if there's a corresponding tool result
				hasResult := false
				for j := i + 1; j < len(messages); j++ {
					nextMsg := messages[j]
					if nextMsg.Type == string(db.MessageTypeUser) && nextMsg.LlmData != nil {
						var userMsg llm.Message
						if err := json.Unmarshal([]byte(*nextMsg.LlmData), &userMsg); err != nil {
							continue
						}
						for _, c := range userMsg.Content {
							if c.Type == llm.ContentTypeToolResult && c.ToolUseID == content.ID {
								hasResult = true
								break
							}
						}
					}
					if hasResult {
						break
					}
				}
				if !hasResult {
					return true // Tool is in progress
				}
			}
		}
	}

	return false
}

// WaitResponse waits for the assistant's text response (end of turn)
// It waits for a NEW response after the last Chat/NewConversation call
func (h *ClaudeTestHarness) WaitResponse() string {
	h.t.Helper()

	if h.convID == "" {
		h.t.Fatal("WaitResponse: no conversation started")
	}

	h.mu.Lock()
	minMessageCount := h.lastMessageCount
	h.mu.Unlock()

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

		// Look for an assistant message with end_of_turn that came AFTER minMessageCount
		// Start from the end to find the most recent one
		for i := len(messages) - 1; i >= 0 && i >= minMessageCount; i-- {
			msg := messages[i]
			if msg.Type != string(db.MessageTypeAgent) || msg.LlmData == nil {
				continue
			}

			var llmMsg llm.Message
			if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
				continue
			}

			if llmMsg.EndOfTurn {
				for _, content := range llmMsg.Content {
					if content.Type == llm.ContentTypeText {
						// Update lastMessageCount for the next wait
						h.mu.Lock()
						h.lastMessageCount = len(messages)
						h.mu.Unlock()
						return content.Text
					}
				}
			}
		}

		time.Sleep(100 * time.Millisecond)
	}

	h.t.Fatalf("WaitResponse: timed out waiting for response (lastMessageCount=%d)", minMessageCount)
	return ""
}

// WaitToolResult waits for a tool result and returns its text content
func (h *ClaudeTestHarness) WaitToolResult() string {
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

// ConversationID returns the current conversation ID
func (h *ClaudeTestHarness) ConversationID() string {
	return h.convID
}

// GetMessages returns all messages in the conversation
func (h *ClaudeTestHarness) GetMessages() []generated.Message {
	var messages []generated.Message
	err := h.db.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(context.Background(), h.convID)
		return qerr
	})
	if err != nil {
		h.t.Fatalf("GetMessages: failed to get messages: %v", err)
	}
	return messages
}

// HasCancelledToolResult checks if there's a cancelled tool result in the conversation
func (h *ClaudeTestHarness) HasCancelledToolResult() bool {
	messages := h.GetMessages()
	for _, msg := range messages {
		if msg.Type != string(db.MessageTypeUser) || msg.LlmData == nil {
			continue
		}

		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			continue
		}

		for _, content := range llmMsg.Content {
			if content.Type == llm.ContentTypeToolResult && content.ToolError {
				for _, result := range content.ToolResult {
					if result.Type == llm.ContentTypeText && strings.Contains(result.Text, "cancelled") {
						return true
					}
				}
			}
		}
	}
	return false
}

// HasCancellationMessage checks if there's a cancellation message in the conversation
func (h *ClaudeTestHarness) HasCancellationMessage() bool {
	messages := h.GetMessages()
	for _, msg := range messages {
		if msg.Type != string(db.MessageTypeAgent) || msg.LlmData == nil {
			continue
		}

		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			continue
		}

		for _, content := range llmMsg.Content {
			if content.Type == llm.ContentTypeText && strings.Contains(content.Text, "Operation cancelled") {
				return true
			}
		}
	}
	return false
}

// claudeLLMManager is an LLMProvider that returns the Claude service
type claudeLLMManager struct {
	service llm.Service
}

func (m *claudeLLMManager) GetService(modelID string) (llm.Service, error) {
	return m.service, nil
}

func (m *claudeLLMManager) GetAvailableModels() []string {
	return []string{"claude", "claude-haiku-4.5"}
}

func (m *claudeLLMManager) HasModel(modelID string) bool {
	return modelID == "claude" || modelID == "claude-haiku-4.5"
}

func (m *claudeLLMManager) GetModelInfo(modelID string) *models.ModelInfo {
	if modelID == "claude-haiku-4.5" {
		return &models.ModelInfo{DisplayName: "Claude Haiku", Tags: "slug"}
	}
	return nil
}

func (m *claudeLLMManager) RefreshCustomModels() error {
	return nil
}

// TestClaudeCancelDuringToolCall tests cancellation during tool execution with Claude
func TestClaudeCancelDuringToolCall(t *testing.T) {
	h := NewClaudeTestHarness(t)
	defer h.Close()

	// Start a conversation that triggers a slow bash command
	h.NewConversation("Please run the bash command: sleep 10", "")

	// Wait for the tool to start executing
	h.WaitForAgentWorking()
	t.Log("Agent is working on tool call")

	// Cancel the conversation
	h.Cancel()
	t.Log("Cancelled conversation")

	// Wait a bit for cancellation to complete
	time.Sleep(500 * time.Millisecond)

	// Verify cancellation was recorded properly
	if !h.HasCancelledToolResult() {
		t.Error("expected cancelled tool result to be recorded")
	}

	if !h.HasCancellationMessage() {
		t.Error("expected cancellation message to be recorded")
	}

	messages := h.GetMessages()
	t.Logf("Total messages after cancellation: %d", len(messages))

	// Verify tokens are maintained
	h.VerifyTokensNonDecreasing()
}

// TestClaudeCancelDuringLLMCall tests cancellation during LLM API call with Claude
func TestClaudeCancelDuringLLMCall(t *testing.T) {
	h := NewClaudeTestHarness(t)
	defer h.Close()

	// Start a conversation with a message that will take some time to process
	h.NewConversation("Please write a very detailed essay about the history of computing, covering at least 10 major milestones.", "")

	// Wait briefly for the request to be sent to Claude
	time.Sleep(500 * time.Millisecond)

	// Cancel during the LLM call
	h.Cancel()
	t.Log("Cancelled during LLM call")

	// Wait for cancellation
	time.Sleep(500 * time.Millisecond)

	// Verify cancellation message exists
	if !h.HasCancellationMessage() {
		t.Error("expected cancellation message to be recorded")
	}

	messages := h.GetMessages()
	t.Logf("Total messages after cancellation: %d", len(messages))

	// Verify tokens are maintained
	h.VerifyTokensNonDecreasing()
}

// TestClaudeCancelDuringLLMCallThenResume tests cancellation during LLM API call and then resuming
func TestClaudeCancelDuringLLMCallThenResume(t *testing.T) {
	h := NewClaudeTestHarness(t)
	defer h.Close()

	// Start a conversation with context we can verify later
	h.NewConversation("Remember this code: BLUE42. Write a long essay about colors.", "")

	// Wait briefly for the request to be sent to Claude
	time.Sleep(300 * time.Millisecond)

	// Cancel during the LLM call (before response arrives)
	h.Cancel()
	t.Log("Cancelled during LLM call")
	time.Sleep(500 * time.Millisecond)

	if !h.HasCancellationMessage() {
		t.Error("expected cancellation message to be recorded")
	}

	tokensAfterCancel := h.GetRequestTokens()
	t.Logf("Tokens after cancel: %v", tokensAfterCancel)

	// Now resume and verify context is preserved
	h.Chat("What was the code I asked you to remember? Just tell me the code.")
	response := h.WaitResponse()
	t.Logf("Response after resume: %s", response)

	// Verify context was preserved - Claude should remember BLUE42
	if !strings.Contains(strings.ToUpper(response), "BLUE42") {
		t.Errorf("expected response to contain BLUE42, got: %s", response)
	}

	// Verify tokens are maintained
	h.VerifyTokensNonDecreasing()
}

// TestClaudeCancelDuringLLMCallMultipleTimes tests multiple cancellations during LLM calls
func TestClaudeCancelDuringLLMCallMultipleTimes(t *testing.T) {
	h := NewClaudeTestHarness(t)
	defer h.Close()

	// First: cancel during LLM call
	h.NewConversation("Write a very long detailed story about space exploration.", "")
	time.Sleep(300 * time.Millisecond)
	h.Cancel()
	t.Log("First cancel during LLM")
	time.Sleep(500 * time.Millisecond)

	// Second: cancel during LLM call again
	h.Chat("Write a very long detailed story about ocean exploration.")
	time.Sleep(300 * time.Millisecond)
	h.Cancel()
	t.Log("Second cancel during LLM")
	time.Sleep(500 * time.Millisecond)

	// Third: cancel during LLM call again
	h.Chat("Write a very long detailed story about mountain climbing.")
	time.Sleep(300 * time.Millisecond)
	h.Cancel()
	t.Log("Third cancel during LLM")
	time.Sleep(500 * time.Millisecond)

	// Now resume normally - the conversation should still work
	h.Chat("Just say 'conversation recovered' and nothing else.")
	response := h.WaitResponse()
	t.Logf("Response after multiple cancels: %s", response)

	// Verify the conversation is functional - response should not indicate an error
	lowerResp := strings.ToLower(response)
	if strings.Contains(lowerResp, "error") || strings.Contains(lowerResp, "invalid") {
		t.Errorf("response may indicate an error: %s", response)
	}

	// Verify tokens are maintained
	h.VerifyTokensNonDecreasing()
}

// TestClaudeCancelDuringLLMCallAndVerifyMessageStructure verifies message structure after LLM cancellation
func TestClaudeCancelDuringLLMCallAndVerifyMessageStructure(t *testing.T) {
	h := NewClaudeTestHarness(t)
	defer h.Close()

	h.NewConversation("Write a very long detailed story about a wizard.", "")
	time.Sleep(300 * time.Millisecond)
	h.Cancel()
	time.Sleep(500 * time.Millisecond)

	// Check message structure
	messages := h.GetMessages()
	t.Logf("Messages after LLM cancel: %d", len(messages))

	// Should have: system message, user message, cancellation message
	// The user message should be recorded even if Claude didn't respond
	userMessageFound := false
	cancelMessageFound := false

	for _, msg := range messages {
		t.Logf("Message type: %s", msg.Type)
		if msg.Type == string(db.MessageTypeUser) {
			userMessageFound = true
		}
		if msg.Type == string(db.MessageTypeAgent) && msg.LlmData != nil {
			var llmMsg llm.Message
			if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err == nil {
				for _, content := range llmMsg.Content {
					if content.Type == llm.ContentTypeText && strings.Contains(content.Text, "cancelled") {
						cancelMessageFound = true
					}
				}
			}
		}
	}

	if !userMessageFound {
		t.Error("expected user message to be recorded")
	}
	if !cancelMessageFound {
		t.Error("expected cancellation message to be recorded")
	}

	// Now send a follow-up and verify no API errors about message format
	h.Chat("Just say hello.")
	response := h.WaitResponse()
	t.Logf("Follow-up response: %s", response)

	// Response should not indicate an error
	lowerResp := strings.ToLower(response)
	if strings.Contains(lowerResp, "error") || strings.Contains(lowerResp, "invalid") {
		t.Errorf("response may indicate API error: %s", response)
	}

	h.VerifyTokensNonDecreasing()
}

// TestClaudeResumeAfterCancellation tests that a conversation can be resumed after cancellation
func TestClaudeResumeAfterCancellation(t *testing.T) {
	h := NewClaudeTestHarness(t)
	defer h.Close()

	// Start a conversation
	h.NewConversation("Please run: sleep 5", "")

	// Wait for tool to start
	h.WaitForAgentWorking()
	t.Log("Agent started tool call")

	// Cancel
	h.Cancel()
	t.Log("Cancelled")
	time.Sleep(500 * time.Millisecond)

	// Verify cancellation
	if !h.HasCancellationMessage() {
		t.Error("expected cancellation message")
	}

	messagesAfterCancel := len(h.GetMessages())
	t.Logf("Messages after cancel: %d", messagesAfterCancel)

	// Resume the conversation
	h.Chat("Hello, let's continue. Please just say 'resumed' and nothing else.")

	// Wait for response
	response := h.WaitResponse()
	t.Logf("Response after resume: %s", response)

	// Verify we got more messages
	messagesAfterResume := len(h.GetMessages())
	t.Logf("Messages after resume: %d", messagesAfterResume)

	if messagesAfterResume <= messagesAfterCancel {
		t.Error("expected more messages after resume")
	}

	// Verify tokens are maintained
	h.VerifyTokensNonDecreasing()
}

// TestClaudeTokensMonotonicallyIncreasing tests that token count increases when resuming
// With prompt caching, total tokens = input + cache_creation + cache_read
func TestClaudeTokensMonotonicallyIncreasing(t *testing.T) {
	h := NewClaudeTestHarness(t)
	defer h.Close()

	// First conversation turn
	h.NewConversation("Hello, please respond with 'first response' and nothing else.", "")
	h.WaitResponse()
	time.Sleep(500 * time.Millisecond) // Wait for any pending operations

	tokens1 := h.GetRequestTokens()
	if len(tokens1) == 0 {
		t.Skip("No token data recorded (API may not be returning it)")
	}
	lastToken1 := tokens1[len(tokens1)-1]
	t.Logf("First turn total tokens: %d", lastToken1)

	// Second conversation turn
	h.Chat("Now please respond with 'second response' and nothing else.")
	h.WaitResponse()
	time.Sleep(500 * time.Millisecond)

	tokens2 := h.GetRequestTokens()
	if len(tokens2) <= len(tokens1) {
		t.Fatal("expected more requests in second turn")
	}
	lastToken2 := tokens2[len(tokens2)-1]
	t.Logf("Second turn total tokens: %d", lastToken2)

	// With prompt caching, tokens should increase or stay similar
	// The key is that we're still sending context (total should be meaningful)
	if lastToken2 < lastToken1 {
		t.Errorf("tokens decreased significantly: first=%d, second=%d", lastToken1, lastToken2)
	}

	// Third turn
	h.Chat("Third turn - respond with 'third response' only.")
	h.WaitResponse()
	time.Sleep(500 * time.Millisecond)

	tokens3 := h.GetRequestTokens()
	if len(tokens3) <= len(tokens2) {
		t.Fatal("expected more requests in third turn")
	}
	lastToken3 := tokens3[len(tokens3)-1]
	t.Logf("Third turn total tokens: %d", lastToken3)

	// Each subsequent turn should have at least as many tokens as the first turn
	// (because we're including more conversation history)
	if lastToken3 < lastToken1 {
		t.Errorf("third turn has fewer tokens than first: first=%d, third=%d", lastToken1, lastToken3)
	}

	t.Logf("Token progression: %d -> %d -> %d", lastToken1, lastToken2, lastToken3)
}

// TestClaudeResumeAfterCancellationPreservesContext tests context preservation after cancellation
func TestClaudeResumeAfterCancellationPreservesContext(t *testing.T) {
	h := NewClaudeTestHarness(t)
	defer h.Close()

	// Start with specific context
	h.NewConversation("Remember this secret word: ELEPHANT. I will ask you about it later. For now, just acknowledge with 'understood'.", "")
	response1 := h.WaitResponse()
	t.Logf("First response: %s", response1)

	tokens1 := h.GetRequestTokens()
	if len(tokens1) == 0 {
		t.Skip("No token data recorded")
	}
	t.Logf("Tokens after first exchange: %v", tokens1)

	// Start a slow command to trigger cancellation
	h.Chat("Run this command: sleep 10")
	h.WaitForAgentWorking()

	// Cancel
	h.Cancel()
	time.Sleep(500 * time.Millisecond)

	tokensAfterCancel := h.GetRequestTokens()
	t.Logf("Tokens after cancel: %v", tokensAfterCancel)

	// Resume and ask about the secret word
	h.Chat("What was the secret word I told you to remember?")
	response2 := h.WaitResponse()
	t.Logf("Response after resume: %s", response2)

	tokensAfterResume := h.GetRequestTokens()
	t.Logf("Tokens after resume: %v", tokensAfterResume)

	// Check that the response mentions ELEPHANT
	if !strings.Contains(strings.ToUpper(response2), "ELEPHANT") {
		t.Errorf("expected response to mention ELEPHANT, got: %s", response2)
	}

	// Verify tokens are maintained
	h.VerifyTokensNonDecreasing()
}

// TestClaudeMultipleCancellations tests multiple cancellations in a row
func TestClaudeMultipleCancellations(t *testing.T) {
	h := NewClaudeTestHarness(t)
	defer h.Close()

	// First cancellation
	h.NewConversation("Run: sleep 10", "")
	h.WaitForAgentWorking()
	h.Cancel()
	time.Sleep(300 * time.Millisecond)

	if !h.HasCancellationMessage() {
		t.Error("expected first cancellation message")
	}

	// Second cancellation
	h.Chat("Run: sleep 10")
	time.Sleep(2 * time.Second) // Wait for Claude to respond and start tool
	h.Cancel()
	time.Sleep(300 * time.Millisecond)

	// Third: complete normally
	h.Chat("Just say 'done' and nothing else.")
	response := h.WaitResponse()
	t.Logf("Final response: %s", response)

	// Verify tokens are maintained
	h.VerifyTokensNonDecreasing()
}

// TestClaudeCancelImmediately tests cancelling immediately after sending a message
func TestClaudeCancelImmediately(t *testing.T) {
	h := NewClaudeTestHarness(t)
	defer h.Close()

	h.NewConversation("Write a very long essay about everything.", "")

	// Cancel immediately
	time.Sleep(50 * time.Millisecond)
	h.Cancel()

	time.Sleep(500 * time.Millisecond)

	// Should still be able to resume
	h.Chat("Just say 'hello'")
	response := h.WaitResponse()
	t.Logf("Response after immediate cancel: %s", response)

	if response == "" {
		t.Error("expected a response after resuming from immediate cancel")
	}

	// Verify tokens are maintained
	h.VerifyTokensNonDecreasing()
}

// TestClaudeCancelWithPendingToolResult tests that missing tool results are handled properly
func TestClaudeCancelWithPendingToolResult(t *testing.T) {
	h := NewClaudeTestHarness(t)
	defer h.Close()

	// This tests the insertMissingToolResults logic
	h.NewConversation("Run: sleep 20", "")
	h.WaitForAgentWorking()

	// Cancel during tool execution
	h.Cancel()
	time.Sleep(500 * time.Millisecond)

	// Resume - this should handle the missing tool result
	h.Chat("Please just say 'recovered' if you can hear me.")
	response := h.WaitResponse()
	t.Logf("Recovery response: %s", response)

	// The conversation should have recovered
	// Claude should not complain about bad messages
	if strings.Contains(strings.ToLower(response), "error") {
		t.Errorf("response indicates an error, which may mean message handling failed: %s", response)
	}

	// Verify tokens are maintained
	h.VerifyTokensNonDecreasing()
}

// TestClaudeCancelDuringLLMCallRapidFire tests rapid cancellations during LLM calls
func TestClaudeCancelDuringLLMCallRapidFire(t *testing.T) {
	h := NewClaudeTestHarness(t)
	defer h.Close()

	// Send message and cancel as fast as possible, multiple times
	for i := 0; i < 3; i++ {
		if i == 0 {
			h.NewConversation("Write a long story.", "")
		} else {
			h.Chat("Write another long story.")
		}
		time.Sleep(100 * time.Millisecond)
		h.Cancel()
		time.Sleep(200 * time.Millisecond)
		t.Logf("Rapid cancel %d complete", i+1)
	}

	// Now do a normal conversation
	h.Chat("Just say 'stable' and nothing else.")
	response := h.WaitResponse()
	t.Logf("Final response after rapid cancels: %s", response)

	// Verify tokens are maintained
	h.VerifyTokensNonDecreasing()
}

// TestClaudeCancelDuringLLMCallWithToolUseResponse tests cancel when Claude is about to use a tool
func TestClaudeCancelDuringLLMCallWithToolUseResponse(t *testing.T) {
	h := NewClaudeTestHarness(t)
	defer h.Close()

	// Ask Claude to use a tool - the response will contain tool_use
	// Cancel before the tool actually executes
	h.NewConversation("Run: echo hello world", "")

	// Wait just enough for the LLM request to be sent but not for tool execution
	time.Sleep(500 * time.Millisecond)

	// Cancel - this might catch the LLM responding with tool_use but before tool execution
	h.Cancel()
	time.Sleep(500 * time.Millisecond)

	t.Logf("Cancelled during potential tool_use response")

	// Resume and verify conversation works
	h.Chat("Just say 'ok' if you can hear me.")
	response := h.WaitResponse()
	t.Logf("Response: %s", response)

	// Verify tokens are maintained
	h.VerifyTokensNonDecreasing()
}
