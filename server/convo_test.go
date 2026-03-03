package server

import (
	"context"
	"encoding/json"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

func TestHydrateGeneratesSystemPromptWithSubagentTool(t *testing.T) {
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
