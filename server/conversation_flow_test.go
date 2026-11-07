package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// TestMessageQueuedDuringThinking tests that messages sent while the LLM is
// processing (thinking/tool execution) are properly queued and eventually processed.
func TestMessageQueuedDuringThinking(t *testing.T) {
	t.Parallel()
	synctest.Test(t, testMessageQueuedDuringThinking)
}

func testMessageQueuedDuringThinking(t *testing.T) {
	server, database, _ := newTestServer(t)
	defer stopActiveConversationLoops(server)

	// Create conversation
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Send first message that triggers a slow response via "delay:" prefix
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
		t.Fatalf("expected status 202 for first message, got %d: %s", w.Code, w.Body.String())
	}

	// Let the loop reach the LLM call. Inside the bubble that call is parked
	// on the fake clock, so the first turn is in flight and stays there until
	// something sleeps.
	synctest.Wait()

	// Now send a SECOND message while the first is still processing.
	secondReq := ChatRequest{
		Message: "echo: second message while thinking",
		Model:   "predictable",
	}
	secondBody, _ := json.Marshal(secondReq)

	req2 := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(secondBody)))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()

	server.handleChatConversation(w2, req2, conversationID)
	if w2.Code != http.StatusAccepted {
		t.Fatalf("expected status 202 for second message, got %d: %s", w2.Code, w2.Body.String())
	}

	// The second message must not be lost: until the running turn takes it
	// at its next model request, it waits in the queue, for the turn.
	synctest.Wait()
	queued, err := database.GetQueuedMessages(t.Context(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || !queued[0].Inject || !strings.Contains(string(queued[0].Llm), "second message while thinking") {
		t.Fatalf("second message is not queued for the running turn: %+v", queued)
	}

	var messages []generated.Message
	// Wait for everything to complete
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err = database.Queries(t.Context(), func(q *generated.Queries) error {
			var qerr error
			messages, qerr = q.ListMessages(t.Context(), conversationID)
			return qerr
		})
		if err != nil {
			t.Fatalf("failed to get messages: %v", err)
		}
		// Look for response to second message
		for _, msg := range messages {
			if msg.Type != string(db.MessageTypeAgent) || msg.LlmData == nil {
				continue
			}
			var llmMsg llm.Message
			if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
				continue
			}
			for _, content := range llmMsg.Content {
				if content.Type == llm.ContentTypeText && strings.Contains(content.Text, "second message while thinking") {
					// Found the response
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("timed out waiting for response to second message")
}

// TestContextPreservedAfterCancel tests that conversation context is properly
// preserved after cancellation and the conversation can be resumed correctly.
func TestContextPreservedAfterCancel(t *testing.T) {
	t.Parallel()
	server, database, predictableService := newTestServer(t)

	// Create conversation
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Send first message and let it complete
	chatReq := ChatRequest{
		Message: "echo: initial context message",
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

	waitForIdle(t, server, conversationID)

	// Now start a slow operation and cancel it
	slowReq := ChatRequest{
		Message: "bash: sleep 5",
		Model:   "predictable",
	}
	slowBody, _ := json.Marshal(slowReq)

	req2 := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(slowBody)))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()

	server.handleChatConversation(w2, req2, conversationID)
	if w2.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", w2.Code, w2.Body.String())
	}

	// Wait for the turn to reach the bash tool call before cancelling.
	waitForToolUseRecorded(t, database, conversationID)

	// Cancel the conversation
	cancelReq := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/cancel", nil)
	cancelW := httptest.NewRecorder()
	server.handleCancelConversation(cancelW, cancelReq, conversationID)

	if cancelW.Code != http.StatusOK {
		t.Fatalf("expected cancel status 200, got %d: %s", cancelW.Code, cancelW.Body.String())
	}

	waitForIdle(t, server, conversationID)

	// Clear the predictable service request history so we can inspect the next request
	predictableService.ClearRequests()

	// Resume the conversation
	resumeReq := ChatRequest{
		Message: "echo: after cancel",
		Model:   "predictable",
	}
	resumeBody, _ := json.Marshal(resumeReq)

	req3 := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(resumeBody)))
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()

	server.handleChatConversation(w3, req3, conversationID)
	if w3.Code != http.StatusAccepted {
		t.Fatalf("expected status 202 for resume, got %d: %s", w3.Code, w3.Body.String())
	}

	// Check that the LLM request included the conversation history
	waitFor(t, 5*time.Second, func() bool { return predictableService.GetLastRequest() != nil })
	lastReq := predictableService.GetLastRequest()

	// The request should include ALL previous messages:
	// 1. Initial context message (user)
	// 2. Response to initial context (assistant)
	// 3. bash: sleep 5 (user)
	// 4. Assistant response with tool use
	// 5. Cancelled tool result (user)
	// 6. [Operation cancelled] (assistant)
	// 7. echo: after cancel (user)
	//
	// If context is lost, we'll only have the last message (#7)

	if len(lastReq.Messages) < 3 {
		t.Errorf("BUG: context lost after cancellation! Expected at least 3 messages in LLM request, got %d", len(lastReq.Messages))
		t.Log("Messages in request:")
		for i, msg := range lastReq.Messages {
			t.Logf("  Message %d: role=%s, content_count=%d", i, msg.Role, len(msg.Content))
			for j, content := range msg.Content {
				if content.Type == llm.ContentTypeText {
					// Truncate long text
					text := content.Text
					if len(text) > 100 {
						text = text[:100] + "..."
					}
					t.Logf("    Content %d: type=%s, text=%q", j, content.Type, text)
				} else {
					t.Logf("    Content %d: type=%s", j, content.Type)
				}
			}
		}
	}

	// Check that "initial context message" appears somewhere in the history
	foundInitialContext := false
	for _, msg := range lastReq.Messages {
		for _, content := range msg.Content {
			if content.Type == llm.ContentTypeText && strings.Contains(content.Text, "initial context message") {
				foundInitialContext = true
				break
			}
		}
	}

	if !foundInitialContext {
		t.Error("BUG: initial context message was not preserved after cancellation")
	}
}

// waitForToolUseRecorded waits until the conversation has recorded an assistant
// message containing a tool_use block.
func waitForToolUseRecorded(t *testing.T, database *db.DB, conversationID string) {
	t.Helper()
	waitFor(t, 10*time.Second, func() bool {
		messages, err := database.ListMessages(t.Context(), conversationID)
		if err != nil {
			return false
		}
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
}
