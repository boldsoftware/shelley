package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db/generated"
)

func TestMessageService_Create(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil)
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	tests := []struct {
		name      string
		msgType   MessageType
		llmData   interface{}
		userData  interface{}
		usageData interface{}
	}{
		{
			name:      "user message with data",
			msgType:   MessageTypeUser,
			llmData:   map[string]string{"content": "Hello, AI!"},
			userData:  map[string]string{"display": "Hello, AI!"},
			usageData: nil,
		},
		{
			name:      "agent message with usage",
			msgType:   MessageTypeAgent,
			llmData:   map[string]string{"response": "Hello, human!"},
			userData:  map[string]string{"formatted": "Hello, human!"},
			usageData: map[string]int{"tokens": 42},
		},
		{
			name:      "tool message minimal",
			msgType:   MessageTypeTool,
			llmData:   nil,
			userData:  nil,
			usageData: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := db.CreateMessage(ctx, CreateMessageParams{
				ConversationID: conv.ConversationID,
				Type:           tt.msgType,
				LLMData:        tt.llmData,
				UserData:       tt.userData,
				UsageData:      tt.usageData,
			})
			if err != nil {
				t.Errorf("Create() error = %v", err)
				return
			}

			if msg.MessageID == "" {
				t.Error("Expected non-empty message ID")
			}

			if msg.ConversationID != conv.ConversationID {
				t.Errorf("Expected conversation ID %s, got %s", conv.ConversationID, msg.ConversationID)
			}

			if msg.Type != string(tt.msgType) {
				t.Errorf("Expected message type %s, got %s", tt.msgType, msg.Type)
			}

			// Test JSON data marshalling
			if tt.llmData != nil {
				if msg.LlmData == nil {
					t.Error("Expected LLM data to be non-nil")
				} else {
					var unmarshalled map[string]interface{}
					err := json.Unmarshal([]byte(*msg.LlmData), &unmarshalled)
					if err != nil {
						t.Errorf("Failed to unmarshal LLM data: %v", err)
					}
				}
			} else {
				if msg.LlmData != nil {
					t.Error("Expected LLM data to be nil")
				}
			}

			if msg.CreatedAt.IsZero() {
				t.Error("Expected non-zero created_at time")
			}
		})
	}
}

func TestMessageService_GetByID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil)
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Create a test message
	created, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		LLMData:        map[string]string{"content": "test message"},
	})
	if err != nil {
		t.Fatalf("Failed to create test message: %v", err)
	}

	// Test getting existing message
	msg, err := db.GetMessageByID(ctx, created.MessageID)
	if err != nil {
		t.Errorf("GetByID() error = %v", err)
		return
	}

	if msg.MessageID != created.MessageID {
		t.Errorf("Expected message ID %s, got %s", created.MessageID, msg.MessageID)
	}

	// Test getting non-existent message
	_, err = db.GetMessageByID(ctx, "non-existent")
	if err == nil {
		t.Error("Expected error for non-existent message")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Expected 'not found' in error message, got: %v", err)
	}
}

func TestMessageService_ListByConversation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil)
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Create multiple test messages
	msgTypes := []MessageType{MessageTypeUser, MessageTypeAgent, MessageTypeTool}
	for i, msgType := range msgTypes {
		_, err := db.CreateMessage(ctx, CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           msgType,
			LLMData:        map[string]interface{}{"index": i, "type": string(msgType)},
		})
		if err != nil {
			t.Fatalf("Failed to create test message %d: %v", i, err)
		}
	}

	// List messages
	var messages []generated.Message
	err = db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		messages, err = q.ListMessages(ctx, conv.ConversationID)
		return err
	})
	if err != nil {
		t.Errorf("ListByConversation() error = %v", err)
		return
	}

	if len(messages) != 3 {
		t.Errorf("Expected 3 messages, got %d", len(messages))
	}

	// Messages should be ordered by created_at ASC (oldest first) by the query
	// We verify this by checking the message types are in the order we created them
	expectedTypes := []string{"user", "agent", "tool"}
	for i, msg := range messages {
		if msg.Type != expectedTypes[i] {
			t.Errorf("Expected message %d to be type %s, got %s", i, expectedTypes[i], msg.Type)
		}
	}
}

func TestMessageService_ListByType(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil)
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Create messages of different types
	msgTypes := []MessageType{MessageTypeUser, MessageTypeAgent, MessageTypeUser, MessageTypeTool}
	for i, msgType := range msgTypes {
		_, err := db.CreateMessage(ctx, CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           msgType,
			LLMData:        map[string]interface{}{"index": i},
		})
		if err != nil {
			t.Fatalf("Failed to create test message %d: %v", i, err)
		}
	}

	// List only user messages
	userMessages, err := db.ListMessagesByType(ctx, conv.ConversationID, MessageTypeUser)
	if err != nil {
		t.Errorf("ListByType() error = %v", err)
		return
	}

	if len(userMessages) != 2 {
		t.Errorf("Expected 2 user messages, got %d", len(userMessages))
	}

	// Verify all messages are user type
	for _, msg := range userMessages {
		if msg.Type != string(MessageTypeUser) {
			t.Errorf("Expected user message, got %s", msg.Type)
		}
	}
}

func TestMessageService_GetLatest(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil)
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Test getting latest from empty conversation
	_, err = db.GetLatestMessage(ctx, conv.ConversationID)
	if err == nil {
		t.Error("Expected error for conversation with no messages")
	}

	// Create multiple test messages
	var lastCreated *generated.Message
	for i := 0; i < 3; i++ {
		created, err := db.CreateMessage(ctx, CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           MessageTypeUser,
			LLMData:        map[string]interface{}{"index": i},
		})
		if err != nil {
			t.Fatalf("Failed to create test message %d: %v", i, err)
		}
		lastCreated = created
	}

	// Get the latest message
	latest, err := db.GetLatestMessage(ctx, conv.ConversationID)
	if err != nil {
		t.Errorf("GetLatest() error = %v", err)
		return
	}

	if latest.MessageID != lastCreated.MessageID {
		t.Errorf("Expected latest message ID %s, got %s", lastCreated.MessageID, latest.MessageID)
	}
}

func TestMessageService_Delete(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil)
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Create a test message
	created, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		LLMData:        map[string]string{"content": "test message"},
	})
	if err != nil {
		t.Fatalf("Failed to create test message: %v", err)
	}

	// Delete the message
	err = db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.DeleteMessage(ctx, created.MessageID)
	})
	if err != nil {
		t.Errorf("Delete() error = %v", err)
		return
	}

	// Verify it's gone
	_, err = db.GetMessageByID(ctx, created.MessageID)
	if err == nil {
		t.Error("Expected error when getting deleted message")
	}
}

func TestMessageService_CountInConversation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil)
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Initial count should be 0
	var count int64
	err = db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		count, err = q.CountMessagesInConversation(ctx, conv.ConversationID)
		return err
	})
	if err != nil {
		t.Errorf("CountInConversation() error = %v", err)
		return
	}
	if count != 0 {
		t.Errorf("Expected initial count 0, got %d", count)
	}

	// Create test messages
	for i := 0; i < 4; i++ {
		_, err := db.CreateMessage(ctx, CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           MessageTypeUser,
			LLMData:        map[string]interface{}{"index": i},
		})
		if err != nil {
			t.Fatalf("Failed to create test message %d: %v", i, err)
		}
	}

	// Count should now be 4
	err = db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		count, err = q.CountMessagesInConversation(ctx, conv.ConversationID)
		return err
	})
	if err != nil {
		t.Errorf("CountInConversation() error = %v", err)
		return
	}
	if count != 4 {
		t.Errorf("Expected count 4, got %d", count)
	}
}

func TestMessageService_CountByType(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil)
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Create messages of different types
	msgTypes := []MessageType{MessageTypeUser, MessageTypeAgent, MessageTypeUser, MessageTypeTool, MessageTypeUser}
	for i, msgType := range msgTypes {
		_, err := db.CreateMessage(ctx, CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           msgType,
			LLMData:        map[string]interface{}{"index": i},
		})
		if err != nil {
			t.Fatalf("Failed to create test message %d: %v", i, err)
		}
	}

	// Count user messages (should be 3)
	userCount, err := db.CountMessagesByType(ctx, conv.ConversationID, MessageTypeUser)
	if err != nil {
		t.Errorf("CountByType() error = %v", err)
		return
	}
	if userCount != 3 {
		t.Errorf("Expected 3 user messages, got %d", userCount)
	}

	// Count agent messages (should be 1)
	agentCount, err := db.CountMessagesByType(ctx, conv.ConversationID, MessageTypeAgent)
	if err != nil {
		t.Errorf("CountByType() error = %v", err)
		return
	}
	if agentCount != 1 {
		t.Errorf("Expected 1 agent message, got %d", agentCount)
	}

	// Count tool messages (should be 1)
	toolCount, err := db.CountMessagesByType(ctx, conv.ConversationID, MessageTypeTool)
	if err != nil {
		t.Errorf("CountByType() error = %v", err)
		return
	}
	if toolCount != 1 {
		t.Errorf("Expected 1 tool message, got %d", toolCount)
	}
}

func TestMessageService_ListMessagesByConversationPaginated(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation-paginated"), true, nil)
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Create multiple test messages
	for i := 0; i < 5; i++ {
		_, err := db.CreateMessage(ctx, CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           MessageTypeUser,
			LLMData:        map[string]string{"text": fmt.Sprintf("test message %d", i)},
		})
		if err != nil {
			t.Fatalf("Failed to create test message %d: %v", i, err)
		}
	}

	// Test ListMessagesByConversationPaginated with limit and offset
	messages, err := db.ListMessagesByConversationPaginated(ctx, conv.ConversationID, 3, 0)
	if err != nil {
		t.Errorf("ListMessagesByConversationPaginated() error = %v", err)
	}

	if len(messages) != 3 {
		t.Errorf("Expected 3 messages, got %d", len(messages))
	}

	// Test with offset
	messages2, err := db.ListMessagesByConversationPaginated(ctx, conv.ConversationID, 3, 3)
	if err != nil {
		t.Errorf("ListMessagesByConversationPaginated() with offset error = %v", err)
	}

	if len(messages2) != 2 {
		t.Errorf("Expected 2 messages with offset, got %d", len(messages2))
	}

	// Verify no duplicate messages between pages
	messageIDs := make(map[string]bool)
	for _, msg := range messages {
		if messageIDs[msg.MessageID] {
			t.Error("Found duplicate message ID in first page")
		}
		messageIDs[msg.MessageID] = true
	}

	for _, msg := range messages2 {
		if messageIDs[msg.MessageID] {
			t.Error("Found duplicate message ID in second page")
		}
		messageIDs[msg.MessageID] = true
	}
}
