package db

import (
	"context"
	"encoding/json"
	"testing"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func TestCreateWarningMessageCapsConsecutiveWarnings(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()

	ctx := context.Background()
	conv, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	var result *CreateWarningMessageResult
	for i := range 5 {
		result, err = database.CreateWarningMessage(ctx, conv.ConversationID, "retrying", 3, "Suppressing further warnings.")
		if err != nil {
			t.Fatalf("CreateWarningMessage %d: %v", i, err)
		}
	}
	if result == nil || !result.Suppressed {
		t.Fatalf("fifth warning was not marked suppressed: %#v", result)
	}

	messages, err := database.ListMessages(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("got %d messages, want 3", len(messages))
	}
	for _, msg := range messages {
		if msg.Type != string(MessageTypeWarning) {
			t.Fatalf("message type = %q, want warning", msg.Type)
		}
		if !msg.ExcludedFromContext {
			t.Fatalf("warning %s not excluded from context", msg.MessageID)
		}
	}

	var userData map[string]interface{}
	if err := json.Unmarshal([]byte(*messages[2].UserData), &userData); err != nil {
		t.Fatalf("unmarshal suppression user_data: %v", err)
	}
	if userData["text"] != "retrying" {
		t.Fatalf("last warning text = %q", userData["text"])
	}
	if userData["suppression_text"] != "Suppressing further warnings." {
		t.Fatalf("last warning suppression text = %q", userData["suppression_text"])
	}
	if userData["suppressed"] != true {
		t.Fatalf("last warning suppressed = %v", userData["suppressed"])
	}

	_, err = database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		LLMData: llm.Message{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	created, err := database.CreateWarningMessage(ctx, conv.ConversationID, "retrying again", 3, "Suppressing further warnings.")
	if err != nil {
		t.Fatalf("CreateWarningMessage after user: %v", err)
	}
	if created == nil || created.Message == nil || created.Suppressed {
		t.Fatalf("expected unsuppressed warning after non-warning reset, got %#v", created)
	}
}

func TestCreateWarningMessageCountsCurrentGeneration(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()

	ctx := context.Background()
	conv, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	for i := range 3 {
		result, err := database.CreateWarningMessage(ctx, conv.ConversationID, "old generation", 3, "Suppressing further warnings.")
		if err != nil {
			t.Fatalf("CreateWarningMessage old generation %d: %v", i, err)
		}
		if result.Suppressed {
			t.Fatalf("old generation warning %d was suppressed", i)
		}
	}

	if err := database.QueriesTx(ctx, func(q *generated.Queries) error {
		_, err := q.IncrementConversationGeneration(ctx, conv.ConversationID)
		return err
	}); err != nil {
		t.Fatalf("IncrementConversationGeneration: %v", err)
	}

	result, err := database.CreateWarningMessage(ctx, conv.ConversationID, "new generation", 3, "Suppressing further warnings.")
	if err != nil {
		t.Fatalf("CreateWarningMessage new generation: %v", err)
	}
	if result.Suppressed || result.Message == nil {
		t.Fatalf("new generation warning suppressed: %#v", result)
	}

	messages, err := database.ListMessages(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("got %d messages, want 4", len(messages))
	}
	if messages[3].Generation != 2 {
		t.Fatalf("new warning generation = %d, want 2", messages[3].Generation)
	}
}

func TestWarningMigrationKeepsContextIndex(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()

	ctx := context.Background()
	var found bool
	err := database.Pool().Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := rx.Query("PRAGMA index_list(messages)")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var seq int
			var name string
			var unique bool
			var origin string
			var partial bool
			if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
				return err
			}
			if name == "idx_messages_conversation_generation_context_sequence" {
				found = true
			}
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("PRAGMA index_list: %v", err)
	}
	if !found {
		t.Fatal("idx_messages_conversation_generation_context_sequence missing")
	}
}
