package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// flusherRecorder wraps httptest.ResponseRecorder to implement http.Flusher
// and provide immediate access to written data in a thread-safe manner
type flusherRecorder struct {
	*httptest.ResponseRecorder
	mu      sync.Mutex
	chunks  []string
	flushed chan struct{}
}

func newFlusherRecorder() *flusherRecorder {
	return &flusherRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		flushed:          make(chan struct{}, 100),
	}
}

// Write overrides ResponseRecorder.Write to provide thread-safe access
func (f *flusherRecorder) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ResponseRecorder.Write(p)
}

func (f *flusherRecorder) Flush() {
	f.mu.Lock()
	body := f.Body.String()
	f.chunks = append(f.chunks, body)
	f.mu.Unlock()

	select {
	case f.flushed <- struct{}{}:
	default:
	}
}

func (f *flusherRecorder) getChunks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]string, len(f.chunks))
	copy(result, f.chunks)
	return result
}

// getString returns the current body contents in a thread-safe manner
func (f *flusherRecorder) getString() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Body.String()
}

// TestSSEUserMessageAppearsImmediately tests that when a user sends a message,
// the message appears in the SSE stream immediately, before the LLM responds.
func TestSSEUserMessageAppearsImmediately(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	// Create conversation
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Set up a context we can cancel to stop the SSE handler
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()

	// Start the SSE stream handler in a goroutine
	sseRecorder := newFlusherRecorder()
	sseReq := httptest.NewRequest("GET", "/api/conversation/"+conversationID+"/stream", nil)
	sseReq = sseReq.WithContext(sseCtx)

	sseStarted := make(chan struct{})
	sseDone := make(chan struct{})
	go func() {
		close(sseStarted)
		server.handleStreamConversation(sseRecorder, sseReq, conversationID)
		close(sseDone)
	}()

	// Wait for SSE handler to start and send initial state
	<-sseStarted

	// Wait for the initial SSE event (empty messages)
	select {
	case <-sseRecorder.flushed:
		// Got initial state
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initial SSE event")
	}

	// Now send a user message that triggers a SLOW LLM response (3 seconds delay)
	chatReq := ChatRequest{
		Message: "delay: 3",
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

	// The user message should appear in the SSE stream IMMEDIATELY (within 500ms)
	// NOT after the 3 second LLM delay
	deadline := time.Now().Add(500 * time.Millisecond)
	userMessageFound := false

	for time.Now().Before(deadline) {
		select {
		case <-sseRecorder.flushed:
			// Check if user message is now in the stream
			body := sseRecorder.getString()
			if containsUserMessage(body, "delay: 3") {
				userMessageFound = true
			}
		case <-time.After(50 * time.Millisecond):
			// Also check current body
			body := sseRecorder.getString()
			if containsUserMessage(body, "delay: 3") {
				userMessageFound = true
			}
		}
		if userMessageFound {
			break
		}
	}

	if !userMessageFound {
		t.Errorf("BUG: user message did not appear in SSE stream within 500ms (LLM has 3s delay)")
		t.Log("This likely means notifySubscribers is not being called immediately after recording the user message")
		t.Logf("SSE body so far: %s", sseRecorder.getString())
	} else {
		t.Log("SUCCESS: user message appeared in SSE stream immediately")
	}

	// Clean up: cancel SSE context and wait for handler to finish
	sseCancel()
	select {
	case <-sseDone:
	case <-time.After(1 * time.Second):
		// Handler may not exit immediately, that's OK
	}
}

// containsUserMessage checks if the SSE body contains a user message with the given text
func containsUserMessage(sseBody, messageText string) bool {
	// SSE format is "data: {json}\n\n"
	scanner := bufio.NewScanner(strings.NewReader(sseBody))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		jsonStr := strings.TrimPrefix(line, "data: ")

		var streamResp StreamResponse
		if err := json.Unmarshal([]byte(jsonStr), &streamResp); err != nil {
			continue
		}

		for _, msg := range streamResp.Messages {
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
				if content.Type == llm.ContentTypeText && strings.Contains(content.Text, messageText) {
					return true
				}
			}
		}
	}
	return false
}

// TestSSEUserMessageWithRealHTTPServer tests with a real HTTP server to properly
// test HTTP context cancellation behavior
func TestSSEUserMessageWithRealHTTPServer(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)

	// Create conversation
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Set up real HTTP server
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	// Connect to SSE stream
	sseResp, err := http.Get(httpServer.URL + "/api/conversation/" + conversationID + "/stream")
	if err != nil {
		t.Fatalf("failed to connect to SSE stream: %v", err)
	}
	defer sseResp.Body.Close()

	// Start reading SSE events in background
	sseEvents := make(chan string, 100)
	go func() {
		scanner := bufio.NewScanner(sseResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				sseEvents <- line
			}
		}
	}()

	// Wait for initial SSE event
	select {
	case <-sseEvents:
		// Got initial state
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial SSE event")
	}

	// Send user message with slow LLM response via real HTTP client
	chatReq := ChatRequest{
		Message: "delay: 5",
		Model:   "predictable",
	}
	chatBody, _ := json.Marshal(chatReq)

	resp, err := http.Post(
		httpServer.URL+"/api/conversation/"+conversationID+"/chat",
		"application/json",
		strings.NewReader(string(chatBody)),
	)
	if err != nil {
		t.Fatalf("failed to send chat message: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", resp.StatusCode)
	}

	// User message should appear in SSE stream within 500ms (before 5s LLM delay)
	deadline := time.Now().Add(500 * time.Millisecond)
	userMessageFound := false

	for time.Now().Before(deadline) && !userMessageFound {
		select {
		case eventLine := <-sseEvents:
			jsonStr := strings.TrimPrefix(eventLine, "data: ")
			var streamResp StreamResponse
			if err := json.Unmarshal([]byte(jsonStr), &streamResp); err != nil {
				continue
			}
			for _, msg := range streamResp.Messages {
				if msg.Type == string(db.MessageTypeUser) && msg.LlmData != nil {
					var llmMsg llm.Message
					if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err == nil {
						for _, content := range llmMsg.Content {
							if content.Type == llm.ContentTypeText && strings.Contains(content.Text, "delay: 5") {
								userMessageFound = true
								break
							}
						}
					}
				}
			}
		case <-time.After(50 * time.Millisecond):
			// Keep waiting
		}
	}

	if !userMessageFound {
		t.Error("BUG: user message did not appear in SSE stream within 500ms with real HTTP server")
		t.Log("This confirms the context cancellation bug in notifySubscribers")
	} else {
		t.Log("SUCCESS: user message appeared in SSE stream immediately with real HTTP server")
	}
}

// TestSSEUserMessageWithExistingConnection is a simpler version that tests
// message recording and notification without the SSE complexity
func TestSSEUserMessageWithExistingConnection(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	// Create conversation and get a manager (simulating an established SSE connection)
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Get the conversation manager to set up subscription
	manager, err := server.getOrCreateConversationManager(context.Background(), conversationID, "")
	if err != nil {
		t.Fatalf("failed to get conversation manager: %v", err)
	}

	// Subscribe to updates
	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	next := manager.subpub.Subscribe(subCtx, -1)

	// Channel to receive updates
	updates := make(chan StreamResponse, 10)
	go func() {
		for {
			data, ok := next()
			if !ok {
				return
			}
			updates <- data
		}
	}()

	// Now send a user message with slow LLM response
	chatReq := ChatRequest{
		Message: "delay: 5",
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

	// We should receive an update with the user message within 500ms
	// (well before the 5 second LLM delay)
	// Note: We may receive other updates first (e.g., ConversationListUpdate for slug changes),
	// so we need to keep checking until we find the user message or timeout.
	deadline := time.Now().Add(500 * time.Millisecond)
	foundUserMsg := false

	for time.Now().Before(deadline) && !foundUserMsg {
		select {
		case update := <-updates:
			// Check if this update contains the user message
			for _, msg := range update.Messages {
				if msg.Type == string(db.MessageTypeUser) && msg.LlmData != nil {
					var llmMsg llm.Message
					if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err == nil {
						for _, content := range llmMsg.Content {
							if content.Type == llm.ContentTypeText && strings.Contains(content.Text, "delay: 5") {
								foundUserMsg = true
								break
							}
						}
					}
				}
			}
		case <-time.After(50 * time.Millisecond):
			// Keep waiting
		}
	}

	if !foundUserMsg {
		t.Error("BUG: did not receive subpub update with user message within 500ms")
		t.Log("This means notifySubscribers is failing or not being called after user message is recorded")
	} else {
		t.Log("SUCCESS: received user message via subpub immediately")
	}
}
