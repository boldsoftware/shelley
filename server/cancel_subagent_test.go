package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// TestCancelParentCancelsSubagents verifies that cancelling a parent conversation
// also cancels any active subagent conversations.
func TestCancelParentCancelsSubagents(t *testing.T) {
	server, database, _ := newTestServer(t)

	// Create parent conversation
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	parentID := conversation.ConversationID

	// Start the parent conversation. The predictable model will:
	// 1. See "subagent: worker bash: sleep 30" and invoke the subagent tool
	// 2. The subagent will receive "bash: sleep 30" and start a long sleep
	chatReq := ChatRequest{
		Message: "subagent: worker bash: sleep 30",
		Model:   "predictable",
	}
	chatBody, _ := json.Marshal(chatReq)

	req := httptest.NewRequest("POST", "/api/conversation/"+parentID+"/chat", strings.NewReader(string(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleChatConversation(w, req, parentID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	// Wait for the subagent to exist and start working
	var subagentID string
	waitFor(t, 10*time.Second, func() bool {
		subagents, err := database.GetSubagents(context.Background(), parentID)
		if err != nil || len(subagents) == 0 {
			return false
		}
		subagentID = subagents[0].ConversationID
		return server.IsAgentWorking(subagentID)
	})

	t.Logf("subagent %s is working", subagentID)

	// Verify parent is also working (blocked on the subagent tool call)
	if !server.IsAgentWorking(parentID) {
		t.Fatal("expected parent to be working")
	}

	// Cancel the parent
	cancelReq := httptest.NewRequest("POST", "/api/conversation/"+parentID+"/cancel", nil)
	cancelW := httptest.NewRecorder()
	server.handleCancelConversation(cancelW, cancelReq, parentID)
	if cancelW.Code != http.StatusOK {
		t.Fatalf("cancel expected 200, got %d: %s", cancelW.Code, cancelW.Body.String())
	}

	// Wait for parent to stop working
	waitFor(t, 5*time.Second, func() bool {
		return !server.IsAgentWorking(parentID)
	})

	// The subagent must also stop working
	waitFor(t, 5*time.Second, func() bool {
		return !server.IsAgentWorking(subagentID)
	})

	// Verify subagent has a cancellation end-of-turn message
	var subMsgs []generated.Message
	err = database.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		subMsgs, qerr = q.ListMessages(context.Background(), subagentID)
		return qerr
	})
	if err != nil {
		t.Fatalf("failed to list subagent messages: %v", err)
	}

	foundEndTurn := false
	for _, msg := range subMsgs {
		if msg.Type != string(db.MessageTypeAgent) || msg.LlmData == nil {
			continue
		}
		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			continue
		}
		if llmMsg.EndOfTurn {
			foundEndTurn = true
			break
		}
	}
	if !foundEndTurn {
		t.Error("expected subagent to have an end-of-turn message after parent cancellation")
	}
}
