package server

import (
	"context"
	"encoding/json"
	"fmt"
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
	"shelley.exe.dev/loop"
	"shelley.exe.dev/models"
)

// setupTestDB creates a test database
func setupTestDB(t *testing.T) (*db.DB, func()) {
	t.Helper()
	return db.NewTestDB(t)
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
		true, "predictable", "")
	// Isolate tests from any user hooks installed on the dev machine
	// (e.g. ~/.config/shelley/hooks/new-conversation). An empty temp dir
	// means findHookIn returns "" and the hooks are inert.
	svr.hooksDir = t.TempDir()
	if svr.terminals != nil {
		svr.terminals.SetSpawner(InProcessSpawner)
	}
	return svr, database, ps
}

// TestCancelWithPredictableModel tests cancellation with the predictable model
func TestCancelWithPredictableModel(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	// Create conversation
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
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
	t.Parallel()
	server, database, _ := newTestServer(t)

	// Create a conversation but don't start it
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
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
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
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

// switchableTestLLM wraps a llm.Service and can be toggled to return errors.
type switchableTestLLM struct {
	inner llm.Service
	mu    sync.Mutex
	err   error
}

func (s *switchableTestLLM) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.inner.Do(ctx, req)
}
func (s *switchableTestLLM) Provider() string        { return s.inner.Provider() }
func (s *switchableTestLLM) TokenContextWindow() int { return s.inner.TokenContextWindow() }
func (s *switchableTestLLM) MaxImageDimension() int  { return s.inner.MaxImageDimension() }
func (s *switchableTestLLM) MaxImageBytes() int      { return s.inner.MaxImageBytes() }
func (s *switchableTestLLM) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// TestRetryAfterLLMFailure: after a retryable LLM failure recorded as an error
// message, POST /retry should re-run the request, producing a fresh assistant
// message. The error message must remain in the conversation log UNMUTATED
// (messages are immutable) and the new LLM call must NOT see the error in the
// conversation.
func TestRetryAfterLLMFailure(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := loop.NewPredictableService()
	switchable := &switchableTestLLM{inner: ps, err: fmt.Errorf("connection error: EOF")}

	svr := NewServer(database, &testLLMManager{service: switchable},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		true, "predictable", "")
	if svr.terminals != nil {
		svr.terminals.SetSpawner(InProcessSpawner)
	}

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	chatBody, _ := json.Marshal(ChatRequest{Message: "hello", Model: "predictable"})
	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	svr.handleChatConversation(httptest.NewRecorder(), req, conversationID)

	// Wait for an error message to be recorded.
	waitFor(t, 10*time.Second, func() bool {
		var msgs []generated.Message
		database.Queries(context.Background(), func(q *generated.Queries) error {
			var e error
			msgs, e = q.ListMessages(context.Background(), conversationID)
			return e
		})
		for _, m := range msgs {
			if m.Type == string(db.MessageTypeError) {
				return true
			}
		}
		return false
	})

	// Wait for agent to stop working before retrying.
	waitFor(t, 5*time.Second, func() bool {
		return !svr.IsAgentWorking(conversationID)
	})

	// Recover the upstream, then retry.
	switchable.setErr(nil)
	ps.ClearRequests()

	retryReq := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/retry", nil)
	retryW := httptest.NewRecorder()
	svr.handleRetryConversation(retryW, retryReq, conversationID)
	if retryW.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", retryW.Code, retryW.Body.String())
	}

	// Wait for a successful agent message. The error message must remain in
	// the conversation log (append-only) and stay byte-for-byte unmutated.
	waitFor(t, 10*time.Second, func() bool {
		var msgs []generated.Message
		database.Queries(context.Background(), func(q *generated.Queries) error {
			var e error
			msgs, e = q.ListMessages(context.Background(), conversationID)
			return e
		})
		hasAgent := false
		for _, m := range msgs {
			if m.Type == string(db.MessageTypeAgent) {
				hasAgent = true
			}
		}
		return hasAgent
	})

	// Verify the error message is still present and was NOT mutated: it must
	// still be retryable and must never have gained a retried flag.
	var finalMsgs []generated.Message
	database.Queries(context.Background(), func(q *generated.Queries) error {
		var e error
		finalMsgs, e = q.ListMessages(context.Background(), conversationID)
		return e
	})
	foundErr := false
	for _, m := range finalMsgs {
		if m.Type != string(db.MessageTypeError) {
			continue
		}
		foundErr = true
		if m.UserData == nil {
			t.Fatalf("error message has nil user_data; want retryable=true")
		}
		var ud map[string]any
		if err := json.Unmarshal([]byte(*m.UserData), &ud); err != nil {
			t.Fatalf("unmarshal error user_data: %v", err)
		}
		if retryable, _ := ud["retryable"].(bool); !retryable {
			t.Errorf("expected error message to stay retryable, got %v", ud)
		}
		if _, present := ud["retried"]; present {
			t.Errorf("error message must not be mutated with a retried flag, got %v", ud)
		}
	}
	if !foundErr {
		t.Fatalf("expected error message to remain in conversation log after retry")
	}

	// Inspect the request that the LLM saw on retry: it must not contain any
	// error message text, and must contain the original user message.
	reqs := ps.GetRecentRequests()
	if len(reqs) == 0 {
		t.Fatalf("expected at least one LLM call after retry")
	}
	last := reqs[len(reqs)-1]
	sawUser := false
	for _, m := range last.Messages {
		if m.ErrorType != llm.ErrorTypeNone {
			t.Errorf("retry request leaked error message into LLM context")
		}
		for _, c := range m.Content {
			if strings.Contains(c.Text, "LLM request failed") {
				t.Errorf("retry request contained error text in LLM context: %q", c.Text)
			}
			if m.Role == llm.MessageRoleUser && strings.TrimSpace(c.Text) == "hello" {
				sawUser = true
			}
		}
	}
	if !sawUser {
		t.Errorf("retry request did not include the original user message")
	}
}

func (s *switchableTestLLM) SupportsImages() bool { return s.inner.SupportsImages() }

// fixedMultiToolService always responds with a fixed multi-tool-use batch.
type fixedMultiToolService struct {
	*loop.PredictableService
	content []llm.Content
}

func (s *fixedMultiToolService) Do(context.Context, *llm.Request) (*llm.Response, error) {
	return &llm.Response{
		Role:       llm.MessageRoleAssistant,
		Content:    append([]llm.Content(nil), s.content...),
		StopReason: llm.StopReasonToolUse,
	}, nil
}

// TestCancelMultiToolHTTPPersistsCompleteOrderedBatch drives a real HTTP
// cancel against a loop mid-way through a 3-tool batch and verifies the
// persisted transcript: assistant tool_use message, then ONE user message with
// a result for every tool_use in order (completed output kept, interrupted
// tools' output preserved with the cancelled sentinel), then the end-of-turn
// message. No duplicates, no orphans.
func TestCancelMultiToolHTTPPersistsCompleteOrderedBatch(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	conversationID := conversation.ConversationID
	manager, err := server.getOrCreateConversationManager(context.Background(), conversationID, "")
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}

	userMessage := llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "run tools"}}}
	if err := server.recordMessage(context.Background(), conversationID, userMessage, llm.Usage{}, nil); err != nil {
		t.Fatalf("record user: %v", err)
	}

	uses := []llm.Content{
		{ID: "first-id", Type: llm.ContentTypeToolUse, ToolName: "first", ToolInput: json.RawMessage(`{}`)},
		{ID: "active-id", Type: llm.ContentTypeToolUse, ToolName: "active", ToolInput: json.RawMessage(`{}`)},
		{ID: "unstarted-id", Type: llm.ContentTypeToolUse, ToolName: "unstarted", ToolInput: json.RawMessage(`{}`)},
	}
	activeStarted := make(chan struct{})
	tools := []*llm.Tool{
		{
			Name: "first", Description: "finishes", InputSchema: llm.MustSchema(`{"type":"object","properties":{}}`),
			Run: func(context.Context, json.RawMessage) llm.ToolOut {
				return llm.ToolOut{LLMContent: []llm.Content{{Type: llm.ContentTypeText, Text: "first output"}}}
			},
		},
		{
			Name: "active", Description: "waits for cancel", InputSchema: llm.MustSchema(`{"type":"object","properties":{}}`),
			Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
				close(activeStarted)
				<-ctx.Done()
				// Like bash: return the partial output inside the error.
				return llm.ErrorToolOut(fmt.Errorf("[command failed: %w]\nactive partial output", ctx.Err()))
			},
		},
		{
			Name: "unstarted", Description: "runs concurrently and observes cancel", InputSchema: llm.MustSchema(`{"type":"object","properties":{}}`),
			Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
				<-ctx.Done()
				return llm.ErrorToolOut(fmt.Errorf("[command failed: %w]\nthird partial output", ctx.Err()))
			},
		},
	}
	service := &fixedMultiToolService{PredictableService: loop.NewPredictableService(), content: uses}
	processCtx, processCancel := context.WithCancel(context.Background())
	loopInstance := loop.NewLoop(loop.Config{
		LLM:     service,
		History: []llm.Message{userMessage},
		Tools:   tools,
		RecordMessage: func(ctx context.Context, message llm.Message, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
			return server.recordMessage(ctx, conversationID, message, usage, otherUsage)
		},
	})
	loopDone := make(chan struct{})
	processDone := make(chan error, 1)
	manager.mu.Lock()
	manager.loop = loopInstance
	manager.loopCancel = processCancel
	manager.loopCtx = processCtx
	manager.loopDone = loopDone
	manager.modelID = "predictable"
	manager.mu.Unlock()
	manager.SetAgentWorking(true)

	go func() {
		processDone <- loopInstance.ProcessOneTurn(processCtx)
		close(loopDone)
	}()
	<-activeStarted

	req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversationID+"/cancel", nil)
	w := httptest.NewRecorder()
	server.handleCancelConversation(w, req, conversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel status = %d: %s", w.Code, w.Body.String())
	}
	if err := <-processDone; err != context.Canceled {
		t.Fatalf("ProcessOneTurn error = %v, want context.Canceled", err)
	}

	var messages []generated.Message
	if err := database.Queries(context.Background(), func(q *generated.Queries) error {
		var queryErr error
		messages, queryErr = q.ListMessages(context.Background(), conversationID)
		return queryErr
	}); err != nil {
		t.Fatalf("list messages: %v", err)
	}
	var transcript []llm.Message
	for _, message := range messages {
		if message.LlmData == nil {
			continue
		}
		var parsed llm.Message
		if err := json.Unmarshal([]byte(*message.LlmData), &parsed); err == nil {
			transcript = append(transcript, parsed)
		}
	}
	// …, assistant tool_use, ONE tool-results message, end-of-turn — no dupes.
	// (Hydration may record hidden context messages before the user message,
	// so anchor on the tail.)
	if len(transcript) < 4 {
		t.Fatalf("transcript length = %d, want at least 4: %+v", len(transcript), transcript)
	}
	tail := transcript[len(transcript)-4:]
	assistant, results, endTurn := tail[1], tail[2], tail[3]
	if tail[0].Role != llm.MessageRoleUser || tail[0].Content[0].Text != "run tools" {
		t.Fatalf("expected user message before assistant, got: %+v", tail[0])
	}
	if assistant.Role != llm.MessageRoleAssistant || results.Role != llm.MessageRoleUser || !endTurn.EndOfTurn {
		t.Fatalf("invalid message order: %+v", transcript)
	}
	if len(results.Content) != 3 {
		t.Fatalf("tool results = %d, want 3", len(results.Content))
	}
	if results.Content[0].ToolUseID != "first-id" || results.Content[0].ToolError || results.Content[0].ToolResult[0].Text != "first output" {
		t.Errorf("completed result = %+v", results.Content[0])
	}
	activeText := results.Content[1].ToolResult[0].Text
	if results.Content[1].ToolUseID != "active-id" || !strings.Contains(activeText, "active partial output") ||
		!strings.HasSuffix(strings.TrimSpace(activeText), "Tool execution cancelled by user") {
		t.Errorf("active result = %+v", results.Content[1])
	}
	thirdText := results.Content[2].ToolResult[0].Text
	if results.Content[2].ToolUseID != "unstarted-id" || !results.Content[2].ToolError ||
		!strings.HasSuffix(strings.TrimSpace(thirdText), "Tool execution cancelled by user") {
		t.Errorf("third result = %+v", results.Content[2])
	}
}

// TestCancelSlowLoopWaitsForOrderedFinalization verifies that cancellation is a
// synchronous lifecycle boundary. A stalled tool-results write keeps cancel,
// a second cancel, and a model change blocked; after the write is released,
// the transcript is ordered tool_use -> results -> end-of-turn -> model marker
// and the stale loop is gone.
func TestCancelSlowLoopWaitsForOrderedFinalization(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	conversationID := conversation.ConversationID
	manager, err := server.getOrCreateConversationManager(context.Background(), conversationID, "")
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}

	userMessage := llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "run tool"}}}
	if err := server.recordMessage(context.Background(), conversationID, userMessage, llm.Usage{}, nil); err != nil {
		t.Fatalf("record user: %v", err)
	}

	uses := []llm.Content{
		{ID: "slow-id", Type: llm.ContentTypeToolUse, ToolName: "active", ToolInput: json.RawMessage(`{}`)},
	}
	activeStarted := make(chan struct{})
	tools := []*llm.Tool{{
		Name: "active", Description: "waits for cancel", InputSchema: llm.MustSchema(`{"type":"object","properties":{}}`),
		Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
			close(activeStarted)
			<-ctx.Done()
			return llm.ErrorToolOut(fmt.Errorf("[command failed: %w]\npartial output", ctx.Err()))
		},
	}}

	// Stall the tool-results write until the test releases it.
	releaseRecord := make(chan struct{})
	recordStarted := make(chan struct{})
	var stallOnce sync.Once
	service := &fixedMultiToolService{PredictableService: loop.NewPredictableService(), content: uses}
	processCtx, processCancel := context.WithCancel(context.Background())
	loopInstance := loop.NewLoop(loop.Config{
		LLM:     service,
		History: []llm.Message{userMessage},
		Tools:   tools,
		RecordMessage: func(ctx context.Context, message llm.Message, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
			// Stall only the tool-results message (a user-role message).
			if message.Role == llm.MessageRoleUser {
				stallOnce.Do(func() {
					close(recordStarted)
					<-releaseRecord
				})
			}
			return server.recordMessage(ctx, conversationID, message, usage, otherUsage)
		},
	})
	loopDone := make(chan struct{})
	processDone := make(chan error, 1)
	manager.mu.Lock()
	manager.loop = loopInstance
	manager.loopCancel = processCancel
	manager.loopCtx = processCtx
	manager.loopDone = loopDone
	manager.modelID = "predictable"
	manager.mu.Unlock()
	manager.SetAgentWorking(true)

	go func() {
		processDone <- loopInstance.ProcessOneTurn(processCtx)
		close(loopDone)
	}()
	<-activeStarted

	// Cancel over HTTP. It must remain blocked while the loop is stalled in
	// recordMessage; returning earlier would let callers race the dying loop.
	type cancelResult struct {
		code int
		body string
	}
	cancelDone := make(chan cancelResult, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversationID+"/cancel", nil)
		w := httptest.NewRecorder()
		server.handleCancelConversation(w, req, conversationID)
		cancelDone <- cancelResult{code: w.Code, body: w.Body.String()}
	}()
	<-recordStarted
	select {
	case result := <-cancelDone:
		t.Fatalf("cancel returned before stalled tool results were recorded: %d %s", result.code, result.body)
	default:
	}

	listTranscript := func() []llm.Message {
		var messages []generated.Message
		if err := database.Queries(context.Background(), func(q *generated.Queries) error {
			var queryErr error
			messages, queryErr = q.ListMessages(context.Background(), conversationID)
			return queryErr
		}); err != nil {
			t.Fatalf("list messages: %v", err)
		}
		var transcript []llm.Message
		for _, message := range messages {
			if message.LlmData == nil {
				continue
			}
			var parsed llm.Message
			if err := json.Unmarshal([]byte(*message.LlmData), &parsed); err == nil {
				transcript = append(transcript, parsed)
			}
		}
		return transcript
	}

	// End-of-turn must NOT be recorded while the loop is still stalled.
	for _, msg := range listTranscript() {
		if msg.EndOfTurn {
			t.Fatalf("end-of-turn recorded before the loop exited: %+v", msg)
		}
	}

	// A second cancel and a model change must also wait behind the same lifecycle
	// boundary; neither may return while the old loop can still write.
	secondCancelStarted := make(chan struct{})
	secondCancelDone := make(chan cancelResult, 1)
	go func() {
		close(secondCancelStarted)
		w := httptest.NewRecorder()
		server.handleCancelConversation(w, httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversationID+"/cancel", nil), conversationID)
		secondCancelDone <- cancelResult{code: w.Code, body: w.Body.String()}
	}()
	<-secondCancelStarted
	select {
	case result := <-secondCancelDone:
		t.Fatalf("second cancel crossed cancellation boundary: %d %s", result.code, result.body)
	default:
	}

	applyStarted := make(chan struct{})
	applyDone := make(chan error, 1)
	go func() {
		close(applyStarted)
		applyDone <- manager.ApplyModelSettings(context.Background(), ModelSettingsChange{
			OldModel: "predictable", NewModel: "predictable",
		})
	}()
	<-applyStarted
	select {
	case err := <-applyDone:
		t.Fatalf("ApplyModelSettings crossed cancellation boundary: %v", err)
	default:
	}

	// Release the stalled write. The first cancel can now persist end-of-turn
	// and tear down the loop; the waiting operations then proceed.
	close(releaseRecord)
	if err := <-processDone; err != context.Canceled {
		t.Fatalf("ProcessOneTurn error = %v, want context.Canceled", err)
	}
	result := <-cancelDone
	if result.code != http.StatusOK {
		t.Fatalf("cancel status = %d: %s", result.code, result.body)
	}
	result = <-secondCancelDone
	if result.code != http.StatusOK {
		t.Fatalf("second cancel status = %d: %s", result.code, result.body)
	}
	if err := <-applyDone; err != nil {
		t.Fatalf("ApplyModelSettings: %v", err)
	}
	manager.mu.Lock()
	staleLoop := manager.loop
	manager.mu.Unlock()
	if staleLoop == loopInstance {
		t.Fatal("stale loop still installed after cancellation")
	}
	waitFor(t, 5*time.Second, func() bool {
		return !server.IsAgentWorking(conversationID)
	})

	transcript := listTranscript()
	if len(transcript) < 5 {
		t.Fatalf("transcript length = %d, want at least 5: %+v", len(transcript), transcript)
	}
	// Tail order: assistant tool_use → tool results → end-of-turn → model-change
	// marker. The marker landing AFTER end-of-turn is the ApplyModelSettings
	// contract this test defends.
	tail := transcript[len(transcript)-4:]
	assistant, results, endTurn, marker := tail[0], tail[1], tail[2], tail[3]
	if assistant.Role != llm.MessageRoleAssistant || len(assistant.Content) == 0 || assistant.Content[0].Type != llm.ContentTypeToolUse {
		t.Fatalf("expected assistant tool_use, got: %+v", assistant)
	}
	if results.Role != llm.MessageRoleUser || len(results.Content) != 1 || results.Content[0].ToolUseID != "slow-id" {
		t.Fatalf("expected tool results before end-of-turn, got: %+v", results)
	}
	if !strings.Contains(results.Content[0].ToolResult[0].Text, "partial output") {
		t.Errorf("tool result lost output: %+v", results.Content[0])
	}
	if !endTurn.EndOfTurn || !strings.Contains(endTurn.Content[0].Text, "Operation cancelled") {
		t.Fatalf("expected end-of-turn after results, got: %+v", endTurn)
	}
	if marker.EndOfTurn || len(marker.Content) == 0 || !strings.Contains(marker.Content[0].Text, "predictable") {
		t.Fatalf("expected model-change marker last, got: %+v", marker)
	}
	// Exactly one end-of-turn despite the double cancel.
	endTurns := 0
	for _, msg := range transcript {
		if msg.EndOfTurn {
			endTurns++
		}
	}
	if endTurns != 1 {
		t.Fatalf("end-of-turn messages = %d, want 1", endTurns)
	}

	// Loop state was torn down before the lifecycle boundary reopened.
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.loop != nil || manager.cancelling {
		t.Fatalf("cancellation state not cleared: loop=%v cancelling=%v", manager.loop, manager.cancelling)
	}
}

// TestResetLoopWaitsForExitBeforeReplacement verifies reset has the same hard
// lifecycle boundary as cancellation: it invalidates the old generation,
// cancels it, and waits for its final persistence before a replacement can be
// created. The stalled write is deterministic; no timing sleeps are needed.

// TestResetDoesNotDeadlockWithConcurrentFatalExit covers the ordering where a
// loop has already failed but reaches handleFatalLoopExit only after ResetLoop
// has invalidated it. handleFatalLoopExit runs before the loop goroutine's
// deferred close(loopDone), so it must return as stale rather than wait for the
// teardown that is itself waiting on loopDone.
func TestResetDoesNotDeadlockWithConcurrentFatalExit(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	manager, err := server.getOrCreateConversationManager(context.Background(), conversation.ConversationID, "")
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}

	oldLoop := loop.NewLoop(loop.Config{})
	processCtx, processCancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	cancelStarted := make(chan struct{})
	var cancelOnce sync.Once
	manager.mu.Lock()
	manager.loop = oldLoop
	manager.loopCancel = func() {
		processCancel()
		cancelOnce.Do(func() { close(cancelStarted) })
	}
	manager.loopCtx = processCtx
	manager.loopDone = loopDone
	manager.loopGeneration = 1
	manager.modelID = "predictable"
	manager.mu.Unlock()

	fatalEntered := make(chan struct{})
	fatalReturned := make(chan struct{})
	go func() {
		// Match ensureLoop's goroutine ordering: fatal cleanup happens before
		// the deferred loopDone close.
		<-cancelStarted
		close(fatalEntered)
		manager.handleFatalLoopExit(oldLoop, 1)
		close(fatalReturned)
		close(loopDone)
	}()

	resetDone := make(chan struct{})
	go func() {
		manager.ResetLoop()
		close(resetDone)
	}()
	<-fatalEntered

	select {
	case <-resetDone:
	case <-time.After(time.Second):
		t.Fatal("ResetLoop deadlocked waiting for a fatal loop exit")
	}
	select {
	case <-fatalReturned:
	case <-time.After(time.Second):
		t.Fatal("fatal exit did not return after reset invalidated its generation")
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.loopTearingDown || manager.loopLifecycleDone != nil || manager.loop != nil {
		t.Fatalf("reset teardown state not cleared: tearingDown=%v done=%v loop=%p", manager.loopTearingDown, manager.loopLifecycleDone, manager.loop)
	}
}

func TestResetLoopWaitsForExitBeforeReplacement(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	manager, err := server.getOrCreateConversationManager(context.Background(), conversation.ConversationID, "")
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}

	userMessage := llm.Message{Role: llm.MessageRoleUser, Content: llm.TextContent("reset while tool runs")}
	if err := server.recordMessage(context.Background(), conversation.ConversationID, userMessage, llm.Usage{}, nil); err != nil {
		t.Fatalf("record user: %v", err)
	}

	toolStarted := make(chan struct{})
	tool := &llm.Tool{
		Name:        "blocking",
		Description: "wait for reset cancellation",
		InputSchema: llm.EmptySchema(),
		Run: func(ctx context.Context, _ json.RawMessage) llm.ToolOut {
			close(toolStarted)
			<-ctx.Done()
			return llm.ErrorToolOut(fmt.Errorf("[command cancelled: %w]", ctx.Err()))
		},
	}
	uses := []llm.Content{{ID: "reset-tool", Type: llm.ContentTypeToolUse, ToolName: tool.Name, ToolInput: json.RawMessage(`{}`)}}
	service := &fixedMultiToolService{PredictableService: loop.NewPredictableService(), content: uses}

	recordStarted := make(chan struct{})
	releaseRecord := make(chan struct{})
	var stallOnce sync.Once
	oldLoop := loop.NewLoop(loop.Config{
		LLM:     service,
		History: []llm.Message{userMessage},
		Tools:   []*llm.Tool{tool},
		RecordMessage: func(ctx context.Context, message llm.Message, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
			if message.Role == llm.MessageRoleUser {
				stallOnce.Do(func() {
					close(recordStarted)
					<-releaseRecord
				})
			}
			return server.recordMessage(ctx, conversation.ConversationID, message, usage, otherUsage)
		},
	})
	processCtx, cancelProcess := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	processDone := make(chan error, 1)
	manager.mu.Lock()
	manager.loop = oldLoop
	manager.loopCancel = cancelProcess
	manager.loopCtx = processCtx
	manager.loopDone = loopDone
	manager.loopGeneration = 1
	manager.modelID = "predictable"
	manager.mu.Unlock()
	manager.SetAgentWorking(true)
	go func() {
		processDone <- oldLoop.ProcessOneTurn(processCtx)
		close(loopDone)
	}()
	<-toolStarted

	resetDone := make(chan struct{})
	go func() {
		manager.ResetLoop()
		close(resetDone)
	}()
	<-recordStarted
	select {
	case <-resetDone:
		t.Fatal("ResetLoop returned while the old loop could still persist")
	default:
	}

	close(releaseRecord)
	if err := <-processDone; err != context.Canceled {
		t.Fatalf("ProcessOneTurn error = %v, want context.Canceled", err)
	}
	<-resetDone

	if err := manager.ensureLoop(loop.NewPredictableService(), "predictable"); err != nil {
		t.Fatalf("ensure replacement loop: %v", err)
	}
	manager.mu.Lock()
	replacement := manager.loop
	generation := manager.loopGeneration
	tearingDown := manager.loopTearingDown
	manager.mu.Unlock()
	if replacement == nil || replacement == oldLoop || generation <= 1 || tearingDown {
		t.Fatalf("replacement lifecycle state = loop:%p old:%p generation:%d tearingDown:%v", replacement, oldLoop, generation, tearingDown)
	}
	manager.stopLoop()
}
