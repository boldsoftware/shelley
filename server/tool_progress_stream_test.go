package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func readStreamResponseWithTimeout(reader *bufio.Reader, timeout time.Duration) (*StreamResponse, error) {
	type result struct {
		resp *StreamResponse
		err  error
	}
	ch := make(chan result, 1)

	go func() {
		var dataLines []string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				ch <- result{nil, err}
				return
			}
			line = strings.TrimSpace(line)
			if line == "" && len(dataLines) > 0 {
				break
			}
			if strings.HasPrefix(line, "data: ") {
				dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
			}
		}

		data := strings.Join(dataLines, "\n")
		if data == "" {
			ch <- result{nil, nil}
			return
		}

		var response StreamResponse
		if err := json.Unmarshal([]byte(data), &response); err != nil {
			ch <- result{nil, err}
			return
		}
		ch <- result{&response, nil}
	}()

	select {
	case r := <-ch:
		return r.resp, r.err
	case <-time.After(timeout):
		return nil, context.DeadlineExceeded
	}
}

func streamResponseContainsToolResultText(resp *StreamResponse, want string) bool {
	for _, msg := range resp.Messages {
		if msg.LlmData == nil {
			continue
		}
		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			continue
		}
		for _, content := range llmMsg.Content {
			if content.Type != llm.ContentTypeToolResult {
				continue
			}
			for _, result := range content.ToolResult {
				if result.Type == llm.ContentTypeText && strings.Contains(result.Text, want) {
					return true
				}
			}
		}
	}
	return false
}

func openFIFOForWriteWithTimeout(path string, timeout time.Duration) error {
	type result struct {
		err error
	}
	ch := make(chan result, 1)
	go func() {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			ch <- result{err: err}
			return
		}
		defer f.Close()
		_, err = f.WriteString("go\n")
		ch <- result{err: err}
	}()

	select {
	case r := <-ch:
		return r.err
	case <-time.After(timeout):
		return context.DeadlineExceeded
	}
}

func TestToolProgressStreamedBeforeToolResult(t *testing.T) {
	srv, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	streamResp, err := http.Get(httpServer.URL + "/api/conversation/" + conversation.ConversationID + "/stream")
	if err != nil {
		t.Fatalf("failed to connect to stream: %v", err)
	}
	defer streamResp.Body.Close()

	reader := bufio.NewReader(streamResp.Body)
	initialEvent, err := readStreamResponseWithTimeout(reader, 2*time.Second)
	if err != nil {
		t.Fatalf("failed to read initial SSE event: %v", err)
	}
	if initialEvent == nil {
		t.Fatal("expected initial SSE event")
	}

	fifoPath := fmt.Sprintf("%s/tool-progress.fifo", t.TempDir())
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("failed to create FIFO: %v", err)
	}

	chatReq := ChatRequest{
		Message: fmt.Sprintf("bash: printf 'alpha\\n'; cat %q >/dev/null; printf 'omega\\n'", fifoPath),
		Model:   "predictable",
	}
	chatBody, _ := json.Marshal(chatReq)
	resp, err := http.Post(
		httpServer.URL+"/api/conversation/"+conversation.ConversationID+"/chat",
		"application/json",
		strings.NewReader(string(chatBody)),
	)
	if err != nil {
		t.Fatalf("failed to post chat request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	sawAlphaProgress := false
	progressDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(progressDeadline) {
		event, err := readStreamResponseWithTimeout(reader, time.Until(progressDeadline))
		if err == context.DeadlineExceeded {
			break
		}
		if err != nil {
			t.Fatalf("failed reading SSE event: %v", err)
		}
		if event == nil {
			continue
		}
		if streamResponseContainsToolResultText(event, "alpha") || streamResponseContainsToolResultText(event, "omega") {
			t.Fatal("received tool result before tool progress was streamed")
		}
		if event.ToolProgress != nil && event.ToolProgress.ToolName == "bash" && strings.Contains(event.ToolProgress.Output, "alpha") {
			sawAlphaProgress = true
			break
		}
	}
	if !sawAlphaProgress {
		t.Fatal("did not receive bash tool progress containing alpha before completion")
	}

	if err := openFIFOForWriteWithTimeout(fifoPath, 2*time.Second); err != nil {
		t.Fatalf("failed to unblock FIFO: %v", err)
	}

	sawOmegaResult := false
	resultDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(resultDeadline) {
		event, err := readStreamResponseWithTimeout(reader, time.Until(resultDeadline))
		if err == context.DeadlineExceeded {
			break
		}
		if err != nil {
			t.Fatalf("failed reading SSE event after unblocking FIFO: %v", err)
		}
		if event == nil {
			continue
		}
		if streamResponseContainsToolResultText(event, "omega") {
			sawOmegaResult = true
			break
		}
	}
	if !sawOmegaResult {
		t.Fatal("did not receive final tool result containing omega after unblocking FIFO")
	}
}

func TestGetConversationDedupesSupersededPartialToolResults(t *testing.T) {
	srv, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}

	assistantToolUse := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "running two tools"},
			{ID: "tool_1", Type: llm.ContentTypeToolUse, ToolName: "bash", ToolInput: json.RawMessage(`{"command":"printf 'first'"}`)},
			{ID: "tool_2", Type: llm.ContentTypeToolUse, ToolName: "bash", ToolInput: json.RawMessage(`{"command":"printf 'second'"}`)},
		},
	}
	if _, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversation.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        assistantToolUse,
		UsageData:      llm.Usage{},
	}); err != nil {
		t.Fatalf("failed to create assistant tool_use message: %v", err)
	}

	now := time.Now()
	partialResult := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type:             llm.ContentTypeToolResult,
			ToolUseID:        "tool_1",
			ToolResult:       []llm.Content{{Type: llm.ContentTypeText, Text: "first done"}},
			ToolUseStartTime: &now,
			ToolUseEndTime:   &now,
		}},
	}
	if _, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversation.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        partialResult,
		UsageData:      llm.Usage{},
	}); err != nil {
		t.Fatalf("failed to create partial tool result message: %v", err)
	}

	finalResult := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:             llm.ContentTypeToolResult,
				ToolUseID:        "tool_1",
				ToolResult:       []llm.Content{{Type: llm.ContentTypeText, Text: "first done"}},
				ToolUseStartTime: &now,
				ToolUseEndTime:   &now,
			},
			{
				Type:             llm.ContentTypeToolResult,
				ToolUseID:        "tool_2",
				ToolResult:       []llm.Content{{Type: llm.ContentTypeText, Text: "second done"}},
				ToolUseStartTime: &now,
				ToolUseEndTime:   &now,
			},
		},
	}
	if _, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversation.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        finalResult,
		UsageData:      llm.Usage{},
	}); err != nil {
		t.Fatalf("failed to create final tool result message: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/conversation/"+conversation.ConversationID, nil)
	w := httptest.NewRecorder()
	srv.handleGetConversation(w, req, conversation.ConversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp StreamResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	toolResultMessages := 0
	for _, msg := range resp.Messages {
		if msg.Type != string(db.MessageTypeUser) || msg.LlmData == nil {
			continue
		}
		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			t.Fatalf("failed to decode llm_data: %v", err)
		}
		hasToolResult := false
		for _, content := range llmMsg.Content {
			if content.Type == llm.ContentTypeToolResult {
				hasToolResult = true
			}
		}
		if hasToolResult {
			toolResultMessages++
		}
	}

	if toolResultMessages != 1 {
		t.Fatalf("expected 1 deduped tool result message, got %d", toolResultMessages)
	}
}

func TestSSEStreamDeduplicatesCatchUpMessages(t *testing.T) {
	// Verifies that the SSE event loop skips messages whose sequence IDs
	// were already sent via the catch-up query (messages recorded between
	// the initial DB query and the subscribe call).
	srv, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}

	// Seed a user message so the conversation has a known sequence ID.
	userMsg := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
	}
	msg1, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversation.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        userMsg,
		UsageData:      llm.Usage{},
	})
	if err != nil {
		t.Fatalf("failed to create user message: %v", err)
	}

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	// Connect to the SSE stream fresh (no last_sequence_id).
	streamResp, err := http.Get(httpServer.URL + "/api/conversation/" + conversation.ConversationID + "/stream")
	if err != nil {
		t.Fatalf("failed to connect to stream: %v", err)
	}
	defer streamResp.Body.Close()

	reader := bufio.NewReader(streamResp.Body)
	initialEvent, err := readStreamResponseWithTimeout(reader, 2*time.Second)
	if err != nil {
		t.Fatalf("failed to read initial SSE event: %v", err)
	}
	if initialEvent == nil || len(initialEvent.Messages) == 0 {
		t.Fatal("expected initial SSE event with at least one message")
	}

	// Record the sequence ID from the initial event.
	lastSeq := int64(0)
	for _, m := range initialEvent.Messages {
		if m.SequenceID > lastSeq {
			lastSeq = m.SequenceID
		}
	}
	if lastSeq == 0 {
		t.Fatal("expected positive sequence ID in initial event")
	}

	// Now add an agent message. This is recorded in the DB and published
	// to the subscriber.
	agentMsg := llm.Message{
		Role:    llm.MessageRoleAssistant,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "I am the agent"}},
	}
	msg2, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversation.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        agentMsg,
		UsageData:      llm.Usage{},
	})
	if err != nil {
		t.Fatalf("failed to create agent message: %v", err)
	}

	// Notify the subscriber about the new message.
	srv.notifySubscribersNewMessage(context.Background(), conversation.ConversationID, msg2)

	// Read the streamed event — it should contain the new message exactly once.
	seenMessageIDs := make(map[string]int)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		event, err := readStreamResponseWithTimeout(reader, time.Until(deadline))
		if err == context.DeadlineExceeded {
			break
		}
		if err != nil {
			t.Fatalf("failed reading SSE event: %v", err)
		}
		if event == nil {
			continue
		}
		for _, m := range event.Messages {
			seenMessageIDs[m.MessageID]++
		}
		if _, ok := seenMessageIDs[msg2.MessageID]; ok {
			break
		}
	}

	count := seenMessageIDs[msg2.MessageID]
	if count != 1 {
		t.Fatalf("expected agent message %q delivered exactly once, got %d", msg2.MessageID, count)
	}
	_ = msg1 // used to seed the conversation
}
