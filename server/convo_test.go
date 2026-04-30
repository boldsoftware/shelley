package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

func TestHydrateGeneratesSystemPromptWithSubagentTool(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	ctx := context.Background()

	// Create a new conversation
	h.NewConversation("Hello", "")
	convID := h.ConversationID()

	// The system prompt should have been created during NewConversation (via handleNewConversation -> getOrCreateConversationManager -> Hydrate)
	// Let's verify it has the subagent tool in its display data.

	var messages []generated.Message
	err := h.db.Queries(ctx, func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(ctx, convID)
		return qerr
	})
	if err != nil {
		t.Fatalf("Failed to list messages: %v", err)
	}

	var systemMsg *generated.Message
	for _, msg := range messages {
		if msg.Type == string(db.MessageTypeSystem) {
			systemMsg = &msg
			break
		}
	}

	if systemMsg == nil {
		t.Fatal("System message not found")
	}

	if systemMsg.DisplayData == nil {
		t.Fatal("System message has no display data")
	}

	var displayData struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(*systemMsg.DisplayData), &displayData); err != nil {
		t.Fatalf("Failed to unmarshal display data: %v", err)
	}

	hasSubagent := false
	for _, tool := range displayData.Tools {
		if tool.Name == "subagent" {
			hasSubagent = true
			break
		}
	}

	if !hasSubagent {
		t.Errorf("System prompt display data should include 'subagent' tool")
		t.Logf("Found tools: %v", displayData.Tools)
	}
}

func TestHydrateSystemPromptDisplayDataRespectsToolOverrides(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	chatBody := `{"message":"Hello","model":"predictable","conversation_options":{"tool_overrides":{"bash":"off","shell":"on"}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/new", strings.NewReader(chatBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.server.handleNewConversation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	messages, err := db.WithTxRes(h.db, context.Background(), func(q *generated.Queries) ([]generated.Message, error) {
		return q.ListMessages(context.Background(), resp.ConversationID)
	})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}

	var systemMsg *generated.Message
	for _, msg := range messages {
		if msg.Type == string(db.MessageTypeSystem) {
			systemMsg = &msg
			break
		}
	}
	if systemMsg == nil {
		t.Fatal("system message not found")
	}
	if systemMsg.DisplayData == nil {
		t.Fatal("system message has no display data")
	}

	var displayData struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(*systemMsg.DisplayData), &displayData); err != nil {
		t.Fatalf("unmarshal display data: %v", err)
	}

	var hasBash, hasShell bool
	for _, tool := range displayData.Tools {
		switch tool.Name {
		case "bash":
			hasBash = true
		case "shell":
			hasShell = true
		}
	}
	if hasBash {
		t.Fatalf("display data should not include disabled bash tool: %+v", displayData.Tools)
	}
	if !hasShell {
		t.Fatalf("display data should include enabled shell tool: %+v", displayData.Tools)
	}
}
