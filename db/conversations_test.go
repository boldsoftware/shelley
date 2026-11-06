package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func TestCreateBtwReaderConversationIsAtomicAndStoresPointer(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: parent.ConversationID, Type: MessageTypeUser,
		LLMData: llm.UserStringMessage("frozen"),
	})
	if err != nil {
		t.Fatal(err)
	}
	historical, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: parent.ConversationID, Type: MessageTypeAgent,
		LLMData: llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{{
			Type: llm.ContentTypeText, Text: "historical answer",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateBtwReaderConversation(ctx, CreateBtwReaderConversationParams{
		SlugBase: "btw-frozen-context", ParentID: parent.ConversationID,
		ParentPointer: BtwParentPointer{
			Generation: parent.CurrentGeneration,
			SequenceID: historical.SequenceID,
		},
		ThinkingLevel: "high",
		SystemMessage: llm.UserStringMessage("reader restriction"),
		UserMessage:   llm.UserStringMessage("side question"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentConversationID == nil || *child.ParentConversationID != parent.ConversationID {
		t.Fatalf("child parent = %v", child.ParentConversationID)
	}
	childOptions := ParseConversationOptions(child.ConversationOptions)
	if childOptions.Kind != BtwReaderKind || childOptions.ThinkingLevel != "high" {
		t.Fatalf("options = %s", child.ConversationOptions)
	}
	if childOptions.ParentPointer == nil ||
		childOptions.ParentPointer.Generation != parent.CurrentGeneration ||
		childOptions.ParentPointer.SequenceID != historical.SequenceID {
		t.Fatalf("frozen context pointer = %#v", childOptions)
	}
	childMessages, err := database.ListMessages(ctx, child.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(childMessages) != 2 || childMessages[0].Type != string(MessageTypeSystem) ||
		childMessages[1].Type != string(MessageTypeUser) || !child.AgentWorking {
		t.Fatalf("atomic child initialization = %#v, working=%t", childMessages, child.AgentWorking)
	}
	for _, message := range childMessages {
		if message.LlmData != nil && (strings.Contains(*message.LlmData, "frozen") ||
			strings.Contains(*message.LlmData, "historical answer")) {
			t.Fatalf("child copied parent text: %#v", message)
		}
	}
}

func TestCreateBtwReaderConversationInitializationRollback(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, stringPtr("parent"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversationID, err := GenerateConversationID()
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateBtwReaderConversation(ctx, CreateBtwReaderConversationParams{
		ConversationID: conversationID,
		SlugBase:       "btw-rollback",
		ParentID:       parent.ConversationID,
		ParentPointer: BtwParentPointer{
			Generation: parent.CurrentGeneration,
		},
		SystemMessage: llm.UserStringMessage("restriction"),
		UserMessage:   llm.UserStringMessage("question"),
		UserData:      make(chan struct{}),
	})
	if err == nil {
		t.Fatal("expected initialization failure")
	}
	if _, err := database.GetConversationByID(ctx, conversationID); err == nil {
		t.Fatal("failed initialization left child conversation")
	}
	if messages, err := database.ListMessages(ctx, conversationID); err != nil || len(messages) != 0 {
		t.Fatalf("failed initialization left messages: %#v, %v", messages, err)
	}
	if children, err := database.GetSubagents(ctx, parent.ConversationID); err != nil || len(children) != 0 {
		t.Fatalf("failed initialization left child listing: %#v, %v", children, err)
	}
}

func TestConversationService_Create(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	tests := []struct {
		name string
		slug *string
	}{
		{
			name: "with slug",
			slug: stringPtr("test-conversation"),
		},
		{
			name: "without slug",
			slug: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conv, err := db.CreateConversation(ctx, tt.slug, true, nil, nil, ConversationOptions{})
			if err != nil {
				t.Errorf("Create() error = %v", err)
				return
			}

			if conv.ConversationID == "" {
				t.Error("Expected non-empty conversation ID")
			}

			if tt.slug != nil {
				if conv.Slug == nil || *conv.Slug != *tt.slug {
					t.Errorf("Expected slug %v, got %v", tt.slug, conv.Slug)
				}
			} else {
				if conv.Slug != nil {
					t.Errorf("Expected nil slug, got %v", conv.Slug)
				}
			}

			if conv.CreatedAt.IsZero() {
				t.Error("Expected non-zero created_at time")
			}

			if conv.UpdatedAt.IsZero() {
				t.Error("Expected non-zero updated_at time")
			}
		})
	}
}

func TestConversationService_GetByID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	created, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Test getting existing conversation
	conv, err := db.GetConversationByID(ctx, created.ConversationID)
	if err != nil {
		t.Errorf("GetByID() error = %v", err)
		return
	}

	if conv.ConversationID != created.ConversationID {
		t.Errorf("Expected conversation ID %s, got %s", created.ConversationID, conv.ConversationID)
	}

	// Test getting non-existent conversation
	_, err = db.GetConversationByID(ctx, "non-existent")
	if err == nil {
		t.Error("Expected error for non-existent conversation")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Expected 'not found' in error message, got: %v", err)
	}
}

func TestConversationService_GetBySlug(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create a test conversation with slug
	created, err := db.CreateConversation(ctx, stringPtr("test-slug"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Test getting by existing slug
	conv, err := db.GetConversationBySlug(ctx, "test-slug")
	if err != nil {
		t.Errorf("GetBySlug() error = %v", err)
		return
	}

	if conv.ConversationID != created.ConversationID {
		t.Errorf("Expected conversation ID %s, got %s", created.ConversationID, conv.ConversationID)
	}

	// Test getting by non-existent slug
	_, err = db.GetConversationBySlug(ctx, "non-existent-slug")
	if err == nil {
		t.Error("Expected error for non-existent slug")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Expected 'not found' in error message, got: %v", err)
	}
}

func TestConversationService_UpdateSlug(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	created, err := db.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Update the slug
	newSlug := "updated-slug"
	updated, err := db.UpdateConversationSlug(ctx, created.ConversationID, newSlug)
	if err != nil {
		t.Errorf("UpdateSlug() error = %v", err)
		return
	}

	if updated.Slug == nil || *updated.Slug != newSlug {
		t.Errorf("Expected slug %s, got %v", newSlug, updated.Slug)
	}

	// Note: SQLite CURRENT_TIMESTAMP has second precision, so we check >= instead of >
	if updated.UpdatedAt.Before(created.UpdatedAt) {
		t.Errorf("Expected updated_at %v to be >= created updated_at %v", updated.UpdatedAt, created.UpdatedAt)
	}
}

func TestConversationService_List(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create multiple test conversations
	for i := range 5 {
		slug := stringPtr("conversation-" + string(rune('a'+i)))
		_, err := db.CreateConversation(ctx, slug, true, nil, nil, ConversationOptions{})
		if err != nil {
			t.Fatalf("Failed to create test conversation %d: %v", i, err)
		}
	}

	// Test listing with pagination
	conversations, err := db.ListConversations(ctx, 3, 0)
	if err != nil {
		t.Errorf("List() error = %v", err)
		return
	}

	if len(conversations) != 3 {
		t.Errorf("Expected 3 conversations, got %d", len(conversations))
	}

	// The query orders by updated_at DESC, but without sleeps all timestamps
	// may be identical, so we just verify we got the expected count
}

func TestConversationService_Search(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create test conversations with different slugs
	testCases := []string{"project-alpha", "project-beta", "work-task", "personal-note"}
	for _, slug := range testCases {
		_, err := db.CreateConversation(ctx, stringPtr(slug), true, nil, nil, ConversationOptions{})
		if err != nil {
			t.Fatalf("Failed to create test conversation with slug %s: %v", slug, err)
		}
	}

	// Search for "project" should return 2 conversations
	results, err := db.SearchConversations(ctx, "project", 10, 0)
	if err != nil {
		t.Errorf("Search() error = %v", err)
		return
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 search results, got %d", len(results))
	}

	// Verify the results contain "project"
	for _, conv := range results {
		if conv.Slug == nil || !strings.Contains(*conv.Slug, "project") {
			t.Errorf("Expected conversation slug to contain 'project', got %v", conv.Slug)
		}
	}
}

func TestConversationService_Touch(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	created, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Touch the conversation
	err = db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.UpdateConversationTimestamp(ctx, created.ConversationID)
	})
	if err != nil {
		t.Errorf("Touch() error = %v", err)
		return
	}

	// Verify updated_at was changed
	updated, err := db.GetConversationByID(ctx, created.ConversationID)
	if err != nil {
		t.Fatalf("Failed to get conversation after touch: %v", err)
	}

	// Note: SQLite CURRENT_TIMESTAMP has second precision, so we check >= instead of >
	if updated.UpdatedAt.Before(created.UpdatedAt) {
		t.Errorf("Expected updated_at %v to be >= created updated_at %v", updated.UpdatedAt, created.UpdatedAt)
	}
}

func TestConversationService_Delete(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	created, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Delete the conversation
	err = db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.DeleteConversation(ctx, created.ConversationID)
	})
	if err != nil {
		t.Errorf("Delete() error = %v", err)
		return
	}

	// Verify it's gone
	_, err = db.GetConversationByID(ctx, created.ConversationID)
	if err == nil {
		t.Error("Expected error when getting deleted conversation")
	}
}

func TestConversationService_Count(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Initial count should be 0
	var count int64
	err := db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		count, err = q.CountConversations(ctx)
		return err
	})
	if err != nil {
		t.Errorf("Count() error = %v", err)
		return
	}
	if count != 0 {
		t.Errorf("Expected initial count 0, got %d", count)
	}

	// Create test conversations
	for i := range 3 {
		_, err := db.CreateConversation(ctx, stringPtr("conversation-"+string(rune('a'+i))), true, nil, nil, ConversationOptions{})
		if err != nil {
			t.Fatalf("Failed to create test conversation %d: %v", i, err)
		}
	}

	// Count should now be 3
	err = db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		count, err = q.CountConversations(ctx)
		return err
	})
	if err != nil {
		t.Errorf("Count() error = %v", err)
		return
	}
	if count != 3 {
		t.Errorf("Expected count 3, got %d", count)
	}
}

func TestConversationService_MultipleNullSlugs(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create multiple conversations with null slugs - this should not fail
	conv1, err := db.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Errorf("Create() first conversation error = %v", err)
		return
	}

	conv2, err := db.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Errorf("Create() second conversation error = %v", err)
		return
	}

	// Both should have null slugs
	if conv1.Slug != nil {
		t.Errorf("Expected first conversation slug to be nil, got %v", conv1.Slug)
	}
	if conv2.Slug != nil {
		t.Errorf("Expected second conversation slug to be nil, got %v", conv2.Slug)
	}

	// They should have different IDs
	if conv1.ConversationID == conv2.ConversationID {
		t.Error("Expected different conversation IDs")
	}
}

func TestConversationService_SlugUniquenessWhenNotNull(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create first conversation with a slug
	_, err := db.CreateConversation(ctx, stringPtr("unique-slug"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Errorf("Create() first conversation error = %v", err)
		return
	}

	// Try to create second conversation with the same slug - this should fail
	_, err = db.CreateConversation(ctx, stringPtr("unique-slug"), true, nil, nil, ConversationOptions{})
	if err == nil {
		t.Error("Expected error when creating conversation with duplicate slug")
		return
	}

	// Verify the error is related to uniqueness constraint
	if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Errorf("Expected UNIQUE constraint error, got: %v", err)
	}
}

func TestConversationService_ArchiveUnarchive(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Store original updated_at timestamp
	originalUpdatedAt := conv.UpdatedAt

	// Test ArchiveConversation
	archivedConv, err := db.ArchiveConversation(ctx, conv.ConversationID)
	if err != nil {
		t.Errorf("ArchiveConversation() error = %v", err)
	}

	if !archivedConv.Archived {
		t.Error("Expected conversation to be archived")
	}

	// Verify that updated_at was NOT modified by archiving
	if archivedConv.UpdatedAt != originalUpdatedAt {
		t.Errorf("ArchiveConversation should not modify updated_at: got %v, want %v", archivedConv.UpdatedAt, originalUpdatedAt)
	}

	// Test UnarchiveConversation
	unarchivedConv, err := db.UnarchiveConversation(ctx, conv.ConversationID)
	if err != nil {
		t.Errorf("UnarchiveConversation() error = %v", err)
	}

	if unarchivedConv.Archived {
		t.Error("Expected conversation to be unarchived")
	}

	// Verify that updated_at was NOT modified by unarchiving
	if unarchivedConv.UpdatedAt != originalUpdatedAt {
		t.Errorf("UnarchiveConversation should not modify updated_at: got %v, want %v", unarchivedConv.UpdatedAt, originalUpdatedAt)
	}
}

func TestConversationService_ListArchivedConversations(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create test conversations
	conv1, err := db.CreateConversation(ctx, stringPtr("test-conversation-1"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create test conversation 1: %v", err)
	}

	conv2, err := db.CreateConversation(ctx, stringPtr("test-conversation-2"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create test conversation 2: %v", err)
	}

	// Archive both conversations
	_, err = db.ArchiveConversation(ctx, conv1.ConversationID)
	if err != nil {
		t.Fatalf("Failed to archive conversation 1: %v", err)
	}

	_, err = db.ArchiveConversation(ctx, conv2.ConversationID)
	if err != nil {
		t.Fatalf("Failed to archive conversation 2: %v", err)
	}

	// Test ListArchivedConversations
	conversations, err := db.ListArchivedConversations(ctx, 10, 0)
	if err != nil {
		t.Errorf("ListArchivedConversations() error = %v", err)
	}

	if len(conversations) != 2 {
		t.Errorf("Expected 2 archived conversations, got %d", len(conversations))
	}

	// Check that all returned conversations are archived
	for _, conv := range conversations {
		if !conv.Archived {
			t.Error("Expected all conversations to be archived")
			break
		}
	}
}

func TestConversationService_SearchArchivedConversations(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create test conversations
	conv1, err := db.CreateConversation(ctx, stringPtr("test-conversation-search-1"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create test conversation 1: %v", err)
	}

	conv2, err := db.CreateConversation(ctx, stringPtr("another-conversation"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create test conversation 2: %v", err)
	}

	// Archive both conversations
	_, err = db.ArchiveConversation(ctx, conv1.ConversationID)
	if err != nil {
		t.Fatalf("Failed to archive conversation 1: %v", err)
	}

	_, err = db.ArchiveConversation(ctx, conv2.ConversationID)
	if err != nil {
		t.Fatalf("Failed to archive conversation 2: %v", err)
	}

	// Test SearchArchivedConversations
	conversations, err := db.SearchArchivedConversations(ctx, "test-conversation", 10, 0)
	if err != nil {
		t.Errorf("SearchArchivedConversations() error = %v", err)
	}

	if len(conversations) != 1 {
		t.Errorf("Expected 1 archived conversation matching search, got %d", len(conversations))
	}

	if len(conversations) > 0 && conversations[0].Slug == nil {
		t.Error("Expected conversation to have a slug")
	} else if len(conversations) > 0 && !strings.Contains(*conversations[0].Slug, "test-conversation") {
		t.Errorf("Expected conversation slug to contain 'test-conversation', got %s", *conversations[0].Slug)
	}
}

func TestConversationService_DeleteConversation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation-to-delete"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Add a message to the conversation
	_, err = db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		LLMData:        map[string]string{"text": "test message"},
	})
	if err != nil {
		t.Fatalf("Failed to create test message: %v", err)
	}

	// Test DeleteConversation
	err = db.DeleteConversation(ctx, conv.ConversationID)
	if err != nil {
		t.Errorf("DeleteConversation() error = %v", err)
	}

	// Verify conversation is deleted
	_, err = db.GetConversationByID(ctx, conv.ConversationID)
	if err == nil {
		t.Error("Expected error when getting deleted conversation, got none")
	}
}

func TestConversationService_UpdateConversationCwd(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation-cwd"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Test UpdateConversationCwd
	newCwd := "/test/new/working/directory"
	err = db.UpdateConversationCwd(ctx, conv.ConversationID, newCwd)
	if err != nil {
		t.Errorf("UpdateConversationCwd() error = %v", err)
	}

	// Verify the cwd was updated
	updatedConv, err := db.GetConversationByID(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("Failed to get updated conversation: %v", err)
	}

	if updatedConv.Cwd == nil {
		t.Error("Expected conversation to have a cwd")
	} else if *updatedConv.Cwd != newCwd {
		t.Errorf("Expected cwd %s, got %s", newCwd, *updatedConv.Cwd)
	}
}

func TestArchivedConversations_SortedByUpdatedAt_NotArchiveTime(t *testing.T) {
	// This test verifies the fix for a bug where archiving a conversation
	// would update its updated_at timestamp, causing archived conversations
	// to be sorted by archive time rather than by their last activity time.
	//
	// The correct behavior is: archived conversations should be sorted by
	// updated_at (which reflects the last message/activity time), NOT by
	// when they were archived.
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	// Create three conversations
	convA, err := db.CreateConversation(ctx, stringPtr("conv-oldest-activity"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conversation A: %v", err)
	}
	convB, err := db.CreateConversation(ctx, stringPtr("conv-newest-activity"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conversation B: %v", err)
	}
	convC, err := db.CreateConversation(ctx, stringPtr("conv-middle-activity"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conversation C: %v", err)
	}

	// Simulate different activity times by directly setting updated_at.
	// SQLite CURRENT_TIMESTAMP has second precision, so we use explicit
	// timestamps to avoid timing issues.
	//
	// Activity order (oldest to newest): A < C < B
	err = db.Pool().Exec(ctx, "UPDATE conversations SET updated_at = '2024-01-01 10:00:00' WHERE conversation_id = ?", convA.ConversationID)
	if err != nil {
		t.Fatalf("Failed to set updated_at for conv A: %v", err)
	}
	err = db.Pool().Exec(ctx, "UPDATE conversations SET updated_at = '2024-01-01 12:00:00' WHERE conversation_id = ?", convC.ConversationID)
	if err != nil {
		t.Fatalf("Failed to set updated_at for conv C: %v", err)
	}
	err = db.Pool().Exec(ctx, "UPDATE conversations SET updated_at = '2024-01-01 14:00:00' WHERE conversation_id = ?", convB.ConversationID)
	if err != nil {
		t.Fatalf("Failed to set updated_at for conv B: %v", err)
	}

	// Archive in a DIFFERENT order than activity order: C first, then B, then A.
	// If archive incorrectly bumps updated_at, the sort order would follow
	// archive order instead of activity order.
	_, err = db.ArchiveConversation(ctx, convC.ConversationID)
	if err != nil {
		t.Fatalf("Failed to archive conv C: %v", err)
	}
	_, err = db.ArchiveConversation(ctx, convB.ConversationID)
	if err != nil {
		t.Fatalf("Failed to archive conv B: %v", err)
	}
	_, err = db.ArchiveConversation(ctx, convA.ConversationID)
	if err != nil {
		t.Fatalf("Failed to archive conv A: %v", err)
	}

	// List archived conversations - should be ordered by updated_at DESC
	// Expected order: B (14:00), C (12:00), A (10:00)
	archived, err := db.ListArchivedConversations(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListArchivedConversations() error = %v", err)
	}

	if len(archived) != 3 {
		t.Fatalf("Expected 3 archived conversations, got %d", len(archived))
	}

	// Verify sort order is by activity time (updated_at), not archive time
	expectedOrder := []string{convB.ConversationID, convC.ConversationID, convA.ConversationID}
	for i, expected := range expectedOrder {
		if archived[i].ConversationID != expected {
			t.Errorf("Position %d: expected conversation %s, got %s", i, expected, archived[i].ConversationID)
		}
	}

	// Also verify that updated_at values were NOT changed by archiving
	for _, conv := range archived {
		switch conv.ConversationID {
		case convA.ConversationID:
			if !strings.Contains(conv.UpdatedAt.Format(time.DateTime), "2024-01-01 10:00:00") {
				t.Errorf("Conv A updated_at should be 2024-01-01 10:00:00, got %v", conv.UpdatedAt)
			}
		case convB.ConversationID:
			if !strings.Contains(conv.UpdatedAt.Format(time.DateTime), "2024-01-01 14:00:00") {
				t.Errorf("Conv B updated_at should be 2024-01-01 14:00:00, got %v", conv.UpdatedAt)
			}
		case convC.ConversationID:
			if !strings.Contains(conv.UpdatedAt.Format(time.DateTime), "2024-01-01 12:00:00") {
				t.Errorf("Conv C updated_at should be 2024-01-01 12:00:00, got %v", conv.UpdatedAt)
			}
		}
	}
}

func TestArchiveDoesNotChangeUpdatedAt(t *testing.T) {
	// Directly verify that archiving/unarchiving does not modify updated_at
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create a conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-archive-timestamp"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Set a known timestamp
	err = db.Pool().Exec(ctx, "UPDATE conversations SET updated_at = '2024-06-15 09:30:00' WHERE conversation_id = ?", conv.ConversationID)
	if err != nil {
		t.Fatalf("Failed to set updated_at: %v", err)
	}

	// Archive the conversation
	archived, err := db.ArchiveConversation(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("ArchiveConversation() error = %v", err)
	}

	// Verify updated_at was NOT changed
	if !strings.Contains(archived.UpdatedAt.Format(time.DateTime), "2024-06-15 09:30:00") {
		t.Errorf("ArchiveConversation should not change updated_at: expected 2024-06-15 09:30:00, got %v", archived.UpdatedAt)
	}

	// Unarchive the conversation
	unarchived, err := db.UnarchiveConversation(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("UnarchiveConversation() error = %v", err)
	}

	// Verify updated_at was NOT changed by unarchive either
	if !strings.Contains(unarchived.UpdatedAt.Format(time.DateTime), "2024-06-15 09:30:00") {
		t.Errorf("UnarchiveConversation should not change updated_at: expected 2024-06-15 09:30:00, got %v", unarchived.UpdatedAt)
	}
}

func TestUnarchivePreservesSortOrder(t *testing.T) {
	// When a conversation is unarchived, it should return to the active list
	// at its original position based on updated_at, not jump to the top
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Create three conversations with known activity times
	convOld, err := db.CreateConversation(ctx, stringPtr("conv-old"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conv-old: %v", err)
	}
	convMid, err := db.CreateConversation(ctx, stringPtr("conv-mid"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conv-mid: %v", err)
	}
	convNew, err := db.CreateConversation(ctx, stringPtr("conv-new"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conv-new: %v", err)
	}

	// Set activity times: old < mid < new
	err = db.Pool().Exec(ctx, "UPDATE conversations SET updated_at = '2024-01-01 08:00:00' WHERE conversation_id = ?", convOld.ConversationID)
	if err != nil {
		t.Fatalf("Failed to set updated_at for conv-old: %v", err)
	}
	err = db.Pool().Exec(ctx, "UPDATE conversations SET updated_at = '2024-01-01 12:00:00' WHERE conversation_id = ?", convMid.ConversationID)
	if err != nil {
		t.Fatalf("Failed to set updated_at for conv-mid: %v", err)
	}
	err = db.Pool().Exec(ctx, "UPDATE conversations SET updated_at = '2024-01-01 16:00:00' WHERE conversation_id = ?", convNew.ConversationID)
	if err != nil {
		t.Fatalf("Failed to set updated_at for conv-new: %v", err)
	}

	// Archive the middle conversation, then unarchive it
	_, err = db.ArchiveConversation(ctx, convMid.ConversationID)
	if err != nil {
		t.Fatalf("Failed to archive conv-mid: %v", err)
	}
	_, err = db.UnarchiveConversation(ctx, convMid.ConversationID)
	if err != nil {
		t.Fatalf("Failed to unarchive conv-mid: %v", err)
	}

	// List active conversations - mid should still be in its original position
	// Expected order: new (16:00), mid (12:00), old (08:00)
	conversations, err := db.ListConversations(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListConversations() error = %v", err)
	}

	if len(conversations) != 3 {
		t.Fatalf("Expected 3 conversations, got %d", len(conversations))
	}

	expectedOrder := []string{convNew.ConversationID, convMid.ConversationID, convOld.ConversationID}
	for i, expected := range expectedOrder {
		if conversations[i].ConversationID != expected {
			t.Errorf("Position %d: expected conversation %s, got %s",
				i, expected, conversations[i].ConversationID)
		}
	}
}

func TestSplitPreviewPacked(t *testing.T) {
	const ts = "2026-06-01T21:51:28Z" // exactly previewTimestampLen bytes
	tests := []struct {
		name          string
		packed        string
		wantPreview   string
		wantUpdatedAt string
	}{
		{"empty", "", "", ""},
		{"timestamp only", ts, "", ts},
		{"timestamp and text", ts + "hello world", "hello world", ts},
		{"shorter than timestamp", "abc", "", ""},
		{"citation markers stripped", ts + "answer\ue200cite\ue202turn1search0\ue201 next", "answer next", ts},
		{"multibyte text preserved", ts + "héllo\u00e9", "héllo\u00e9", ts},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preview, updatedAt := splitPreviewPacked(tt.packed)
			if preview != tt.wantPreview {
				t.Errorf("preview = %q, want %q", preview, tt.wantPreview)
			}
			if updatedAt != tt.wantUpdatedAt {
				t.Errorf("updatedAt = %q, want %q", updatedAt, tt.wantUpdatedAt)
			}
		})
	}
}

func TestQueuedMessages(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	conv, err := db.CreateConversation(ctx, stringPtr("queued-test"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	id := conv.ConversationID

	// Fresh conversation defaults to an empty array.
	if got := ParseQueuedMessages(conv.QueuedMessages); len(got) != 0 {
		t.Fatalf("new conversation queued_messages = %v, want empty", got)
	}

	// Append two messages.
	for _, qm := range []QueuedMessage{
		{ID: "a", Llm: []byte(`{"role":"user"}`), CreatedAt: time.Now(), Model: "m1"},
		{ID: "b", Llm: []byte(`{"role":"user"}`), CreatedAt: time.Now(), Model: "m1"},
	} {
		if _, err := db.AppendQueuedMessage(ctx, id, qm); err != nil {
			t.Fatalf("AppendQueuedMessage: %v", err)
		}
	}

	after, err := db.GetConversationByID(ctx, id)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	got := ParseQueuedMessages(after.QueuedMessages)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("after append, queued = %+v, want [a b] in order", got)
	}

	// Remove one by id.
	if _, err := db.RemoveQueuedMessages(ctx, id, "a"); err != nil {
		t.Fatalf("RemoveQueuedMessages: %v", err)
	}
	after, _ = db.GetConversationByID(ctx, id)
	got = ParseQueuedMessages(after.QueuedMessages)
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("after remove, queued = %+v, want [b]", got)
	}

	// Clear.
	if _, err := db.ClearQueuedMessages(ctx, id); err != nil {
		t.Fatalf("ClearQueuedMessages: %v", err)
	}
	after, _ = db.GetConversationByID(ctx, id)
	if after.QueuedMessages != "[]" {
		t.Fatalf("after clear, queued_messages = %q, want []", after.QueuedMessages)
	}
}

func TestQueuedTranscriptionLifecycle(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := t.Context()

	parent, err := database.CreateConversation(ctx, stringPtr("queued-transcription"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	queued := QueuedMessage{
		ID:        "transcription-1",
		CreatedAt: time.Now().UTC(),
		Model:     "predictable",
		Kind:      QueuedMessageKindTranscription,
		State:     QueuedMessageStateWorking,
		Transcription: &QueuedTranscription{
			MediaPath: "/tmp/a.webm",
		},
	}
	updatedParent, created, err := database.CreateQueuedTranscription(ctx, parent.ConversationID, queued)
	if err != nil {
		t.Fatal(err)
	}
	if created.Transcription.MediaPath != "/tmp/a.webm" {
		t.Fatalf("created transcription = %#v", created.Transcription)
	}
	persisted := ParseQueuedMessages(updatedParent.QueuedMessages)
	if len(persisted) != 1 || persisted[0].Kind != QueuedMessageKindTranscription || persisted[0].State != QueuedMessageStateWorking {
		t.Fatalf("persisted queue = %#v", persisted)
	}
	if len(persisted[0].Llm) != 0 {
		t.Fatalf("working transcription has llm payload: %s", persisted[0].Llm)
	}

	readyLLM := json.RawMessage(`{"Role":0,"Content":[{"Type":2,"Text":"hello"}]}`)
	_, ready, err := database.UpdateQueuedMessage(ctx, parent.ConversationID, queued.ID, func(qm *QueuedMessage) error {
		qm.State = QueuedMessageStateReady
		qm.Llm = readyLLM
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != QueuedMessageStateReady || string(ready.Llm) != string(readyLLM) {
		t.Fatalf("ready item = %#v", ready)
	}

	_, _, err = database.UpdateQueuedMessage(ctx, parent.ConversationID, "missing", func(*QueuedMessage) error { return nil })
	if !errors.Is(err, ErrQueuedMessageNotFound) {
		t.Fatalf("missing update error = %v", err)
	}
}

func TestRetryQueuedTranscriptionIsAtomic(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := t.Context()

	parent, err := database.CreateConversation(ctx, stringPtr("retry-transcription"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	queued := QueuedMessage{
		ID:        "retry-1",
		Llm:       json.RawMessage(`{"Role":0}`),
		CreatedAt: time.Now().UTC(),
		Model:     "predictable",
		Kind:      QueuedMessageKindTranscription,
		State:     QueuedMessageStateFailed,
		Error:     "first attempt failed",
		Transcription: &QueuedTranscription{
			MediaPath: "/tmp/a.webm",
			Audit:     json.RawMessage(`[{"Role":1}]`),
		},
	}
	if _, err := database.AppendQueuedMessage(ctx, parent.ConversationID, queued); err != nil {
		t.Fatal(err)
	}

	_, retried, err := database.RetryQueuedTranscription(ctx, parent.ConversationID, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.State != QueuedMessageStateWorking || retried.Error != "" ||
		len(retried.Transcription.Audit) != 0 {
		t.Fatalf("retried item = %#v", retried)
	}

	_, _, err = database.RetryQueuedTranscription(ctx, parent.ConversationID, queued.ID)
	if !errors.Is(err, ErrQueuedMessageNotRetryable) {
		t.Fatalf("duplicate retry error = %v", err)
	}
	children, err := database.GetSubagents(ctx, parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 0 {
		t.Fatalf("children after duplicate retry = %d, want 0", len(children))
	}
}

// TestCreateMessageRemoveQueuedIDAtomic verifies that RemoveQueuedID drops the
// matching queued entry in the SAME Tx as the INSERT, so the real immutable
func TestCreateMessageRemoveQueuedIDAtomic(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	conv, err := db.CreateConversation(ctx, stringPtr("atomic-drain"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	id := conv.ConversationID

	for _, qm := range []QueuedMessage{
		{ID: "keep", Llm: []byte(`{"role":"user"}`), CreatedAt: time.Now(), Model: "m"},
		{ID: "drain", Llm: []byte(`{"role":"user"}`), CreatedAt: time.Now(), Model: "m"},
	} {
		if _, err := db.AppendQueuedMessage(ctx, id, qm); err != nil {
			t.Fatalf("AppendQueuedMessage: %v", err)
		}
	}

	// Insert a real user row AND remove the "drain" entry in one Tx.
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: id,
		Type:           MessageTypeUser,
		LLMData:        map[string]any{"Role": 0},
		UsageData:      map[string]any{},
		BumpTimestamp:  true,
		RemoveQueuedID: "drain",
	}); err != nil {
		t.Fatalf("CreateMessage with RemoveQueuedID: %v", err)
	}

	after, _ := db.GetConversationByID(ctx, id)
	got := ParseQueuedMessages(after.QueuedMessages)
	if len(got) != 1 || got[0].ID != "keep" {
		t.Fatalf("after atomic drain, queued = %+v, want [keep]", got)
	}
	msgs, err := db.ListMessages(ctx, id)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	users := 0
	for _, m := range msgs {
		if m.Type == string(MessageTypeUser) {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("expected exactly 1 real user row after drain, got %d", users)
	}

	// A cancellation that wins the race with drain must not leave a parent
	// message behind. Missing queue identity aborts the entire INSERT Tx.
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: id,
		Type:           MessageTypeUser,
		LLMData:        map[string]any{"Role": 0},
		RemoveQueuedID: "already-cancelled",
	}); !errors.Is(err, ErrQueuedMessageNotFound) {
		t.Fatalf("missing RemoveQueuedID error = %v", err)
	}
	msgs, err = db.ListMessages(ctx, id)
	if err != nil {
		t.Fatalf("ListMessages after cancelled drain: %v", err)
	}
	users = 0
	for _, m := range msgs {
		if m.Type == string(MessageTypeUser) {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("cancelled drain inserted a user row: got %d", users)
	}
}

// TestQueuedMessagesMutationStrictOnCorruptColumn verifies that append/remove
// REFUSE to clobber a corrupt-but-nonempty queued_messages column (MAJOR 5),
// while the lenient ParseQueuedMessages still returns empty for reads.
func TestQueuedMessagesMutationStrictOnCorruptColumn(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	conv, err := db.CreateConversation(ctx, stringPtr("corrupt-queue"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	id := conv.ConversationID

	// Write a corrupt (non-JSON) value directly.
	if err := db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.UpdateConversationQueuedMessages(ctx, generated.UpdateConversationQueuedMessagesParams{
			QueuedMessages: "{not json",
			ConversationID: id,
		})
	}); err != nil {
		t.Fatalf("seed corrupt column: %v", err)
	}

	// Mutations must error rather than overwrite with '[]'.
	if _, err := db.AppendQueuedMessage(ctx, id, QueuedMessage{ID: "x"}); err == nil {
		t.Fatal("AppendQueuedMessage on corrupt column: want error, got nil")
	}
	if _, err := db.RemoveQueuedMessages(ctx, id, "x"); err == nil {
		t.Fatal("RemoveQueuedMessages on corrupt column: want error, got nil")
	}
	// The corrupt data must still be intact (not clobbered).
	after, _ := db.GetConversationByID(ctx, id)
	if after.QueuedMessages != "{not json" {
		t.Fatalf("corrupt column was modified: %q", after.QueuedMessages)
	}
	// Lenient read returns empty (for render/Hydrate), strict returns error.
	if got := ParseQueuedMessages(after.QueuedMessages); len(got) != 0 {
		t.Fatalf("lenient parse want empty, got %+v", got)
	}
	if _, err := ParseQueuedMessagesStrict(after.QueuedMessages); err == nil {
		t.Fatal("ParseQueuedMessagesStrict want error on corrupt input")
	}
	// An explicit clear is still allowed (hard reset).
	if _, err := db.ClearQueuedMessages(ctx, id); err != nil {
		t.Fatalf("ClearQueuedMessages: %v", err)
	}
}

// PromoteDraft applies send-time overrides and clears is_draft in one
// transaction. Nil overrides keep the draft's persisted values; non-nil
// ones win; the returned row reflects the final state the loop must pin
// to. A second promote (or one on a never-draft row) reports
// ErrConversationNotDraft and applies nothing.
func TestPromoteDraftAtomicOverrides(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := t.Context()

	origModel := "model-orig"
	origCwd := "/tmp/orig"
	draft, err := db.CreateDraftConversation(ctx, &origCwd, &origModel, ConversationOptions{}, "body")
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

	// Promote with all overrides.
	newModel := "model-new"
	newCwd := "/tmp/new"
	opts := ConversationOptions{ThinkingLevel: "high"}
	promoted, err := db.PromoteDraft(ctx, draft.ConversationID, &newCwd, &newModel, &opts)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if promoted.IsDraft || promoted.Draft != "" {
		t.Fatalf("promoted row still draft-ish: is_draft=%v draft=%q", promoted.IsDraft, promoted.Draft)
	}
	if promoted.Model == nil || *promoted.Model != newModel {
		t.Fatalf("promoted model: got %v, want %q", promoted.Model, newModel)
	}
	if promoted.Cwd == nil || *promoted.Cwd != newCwd {
		t.Fatalf("promoted cwd: got %v, want %q", promoted.Cwd, newCwd)
	}
	if got := ParseConversationOptions(promoted.ConversationOptions); got.ThinkingLevel != "high" {
		t.Fatalf("promoted options: got %+v", got)
	}

	// A second promote loses the race: ErrConversationNotDraft, nothing applied.
	stale := "model-stale"
	if _, err := db.PromoteDraft(ctx, draft.ConversationID, nil, &stale, nil); !errors.Is(err, ErrConversationNotDraft) {
		t.Fatalf("second promote: want ErrConversationNotDraft, got %v", err)
	}
	after, err := db.GetConversationByID(ctx, draft.ConversationID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Model == nil || *after.Model != newModel {
		t.Fatalf("lost-race promote mutated model: got %v", after.Model)
	}

	// Nil overrides keep the draft's persisted values.
	draft2, err := db.CreateDraftConversation(ctx, &origCwd, &origModel, ConversationOptions{ThinkingLevel: "low"}, "body2")
	if err != nil {
		t.Fatalf("create draft2: %v", err)
	}
	promoted2, err := db.PromoteDraft(ctx, draft2.ConversationID, nil, nil, nil)
	if err != nil {
		t.Fatalf("promote2: %v", err)
	}
	if promoted2.Model == nil || *promoted2.Model != origModel {
		t.Fatalf("promote2 model: got %v, want %q", promoted2.Model, origModel)
	}
	if promoted2.Cwd == nil || *promoted2.Cwd != origCwd {
		t.Fatalf("promote2 cwd: got %v, want %q", promoted2.Cwd, origCwd)
	}
	if got := ParseConversationOptions(promoted2.ConversationOptions); got.ThinkingLevel != "low" {
		t.Fatalf("promote2 options clobbered: got %+v", got)
	}
}

// TestListConversationsParticipants verifies that the conversation list
// queries collect each exe.dev author and authored-message count, sorted by
// email, ignoring messages with no author.
func TestListConversationsParticipants(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	shared, err := db.CreateConversation(ctx, stringPtr("shared"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("create shared: %v", err)
	}
	// bob first, then alice, then bob again: participants must come back sorted
	// by email with counts, not in insertion order.
	for _, email := range []string{"bob@example.com", "alice@example.com", "bob@example.com"} {
		if _, err := db.CreateMessage(ctx, CreateMessageParams{
			ConversationID: shared.ConversationID,
			Type:           MessageTypeUser,
			UserEmail:      email,
		}); err != nil {
			t.Fatalf("create user msg: %v", err)
		}
	}
	// An agent reply and an unauthenticated user message contribute nobody.
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: shared.ConversationID,
		Type:           MessageTypeAgent,
	}); err != nil {
		t.Fatalf("create agent msg: %v", err)
	}
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: shared.ConversationID,
		Type:           MessageTypeUser,
	}); err != nil {
		t.Fatalf("create anonymous user msg: %v", err)
	}

	// A conversation whose only message has no author has no participants.
	anon, err := db.CreateConversation(ctx, stringPtr("anon"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("create anon: %v", err)
	}
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: anon.ConversationID,
		Type:           MessageTypeUser,
	}); err != nil {
		t.Fatalf("create anon msg: %v", err)
	}

	// check asserts that every wanted conversation appears in items with
	// exactly the expected participants.
	check := func(name string, items []ConversationListItem, want map[string][]ConversationParticipant) {
		t.Helper()
		got := make(map[string][]ConversationParticipant, len(items))
		for _, item := range items {
			got[item.ConversationID] = item.Participants
		}
		for id, wantParticipants := range want {
			participants, ok := got[id]
			if !ok {
				t.Errorf("%s: conversation %s missing from results", name, id)
				continue
			}
			if !slices.Equal(participants, wantParticipants) {
				t.Errorf("%s: participants for %s = %v, want %v", name, id, participants, wantParticipants)
			}
		}
	}
	both := map[string][]ConversationParticipant{
		shared.ConversationID: {
			{Email: "alice@example.com", MessageCount: 1},
			{Email: "bob@example.com", MessageCount: 2},
		},
		anon.ConversationID: nil,
	}
	onlyShared := map[string][]ConversationParticipant{
		shared.ConversationID: {
			{Email: "alice@example.com", MessageCount: 1},
			{Email: "bob@example.com", MessageCount: 2},
		},
	}

	items, err := db.ListConversations(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	check("ListConversations", items, both)

	items, err = db.ListAllConversations(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListAllConversations: %v", err)
	}
	check("ListAllConversations", items, both)

	items, err = db.SearchConversations(ctx, "shared", 10, 0)
	if err != nil {
		t.Fatalf("SearchConversations: %v", err)
	}
	check("SearchConversations", items, onlyShared)

	items, err = db.SearchConversationsWithMessages(ctx, "shared", 10, 0)
	if err != nil {
		t.Fatalf("SearchConversationsWithMessages: %v", err)
	}
	check("SearchConversationsWithMessages", items, onlyShared)

	hits, err := db.SearchConversationsFTS(ctx, "shared", false, 10, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS: %v", err)
	}
	ftsItems := make([]ConversationListItem, len(hits))
	for i, h := range hits {
		ftsItems[i] = h.ConversationListItem
	}
	check("SearchConversationsFTS", ftsItems, onlyShared)
}

// TestDecodeParticipants covers the participants_json decoder directly: the
// conversation-list patch stream hashes the marshalled list, so the output
// must be sorted regardless of the order SQLite aggregated in, and an empty
// array must decode to nil so it stays out of the JSON entirely.
func TestDecodeParticipants(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []ConversationParticipant
	}{
		{"empty array", "[]", nil},
		{
			"single",
			`[{"email":"alice@example.com","message_count":3}]`,
			[]ConversationParticipant{{Email: "alice@example.com", MessageCount: 3}},
		},
		{
			"unsorted",
			`[{"email":"bob@example.com","message_count":2},{"email":"alice@example.com","message_count":1}]`,
			[]ConversationParticipant{
				{Email: "alice@example.com", MessageCount: 1},
				{Email: "bob@example.com", MessageCount: 2},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeParticipants(tt.raw)
			if err != nil {
				t.Fatalf("decodeParticipants(%q): %v", tt.raw, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("decodeParticipants(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
	if _, err := decodeParticipants("not json"); err == nil {
		t.Error("decodeParticipants(\"not json\") = nil error, want error")
	}
}

func TestListFrozenParentMessagesReturnsExactPointerPrefix(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	add := func(text string, excluded bool) int64 {
		t.Helper()
		message, err := database.CreateMessage(ctx, CreateMessageParams{
			ConversationID:      parent.ConversationID,
			Type:                MessageTypeUser,
			LLMData:             llm.UserStringMessage(text),
			ExcludedFromContext: excluded,
		})
		if err != nil {
			t.Fatal(err)
		}
		return message.SequenceID
	}
	add("system context", false)
	var cutoff int64
	for i := 0; i < 21; i++ {
		if i == 10 {
			add("excluded", true)
		}
		cutoff = add(fmt.Sprintf("message-%d", i), false)
	}
	add("later", false)
	messages, err := database.ListFrozenParentMessages(ctx, parent.ConversationID, BtwParentPointer{
		Generation: parent.CurrentGeneration,
		SequenceID: cutoff,
	})
	if err != nil || len(messages) != 22 {
		t.Fatalf("frozen messages = %d, %v", len(messages), err)
	}
	for _, message := range messages {
		if message.ExcludedFromContext || message.SequenceID > cutoff ||
			message.Generation != parent.CurrentGeneration {
			t.Fatalf("message outside exact pointer selection: %#v", message)
		}
	}
	var first llm.Message
	if err := json.Unmarshal([]byte(*messages[1].LlmData), &first); err != nil || first.Content[0].Text != "message-0" {
		t.Fatalf("oldest retained message = %#v, %v", first, err)
	}
}

func TestManagedBtwIdentityAndUserInitiatedScrubbing(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := database.CreateBtwReaderConversation(ctx, CreateBtwReaderConversationParams{
		SlugBase:      "btw-identity",
		ParentID:      parent.ConversationID,
		ParentPointer: BtwParentPointer{Generation: 1, SequenceID: 0},
		SystemMessage: llm.UserStringMessage("reader"),
		UserMessage:   llm.UserStringMessage("question"),
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := ManagedBtwReaderIdentity(*reader)
	if !ok || identity.ConversationID != reader.ConversationID || identity.ParentConversationID != parent.ConversationID {
		t.Fatalf("identity=%#v ok=%t", identity, ok)
	}
	assertScrubbed := func(name string, conversation *generated.Conversation) {
		t.Helper()
		options := ParseConversationOptions(conversation.ConversationOptions)
		if options.Kind != "" || options.ParentPointer != nil || options.ThinkingLevel != "high" {
			t.Fatalf("%s retained managed identity: %#v", name, options)
		}
		if _, ok := ManagedBtwReaderIdentity(*conversation); ok {
			t.Fatalf("%s validated as BTW reader", name)
		}
	}

	stale, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{
		Kind:          BtwReaderKind,
		ParentPointer: &BtwParentPointer{Generation: 1, SequenceID: 2},
		ThinkingLevel: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: stale.ConversationID,
		Type:           MessageTypeUser,
		LLMData:        llm.UserStringMessage("ordinary"),
	})
	if err != nil {
		t.Fatal(err)
	}
	forked, err := database.ForkConversation(ctx, stale.ConversationID, message.SequenceID)
	if err != nil {
		t.Fatal(err)
	}
	assertScrubbed("user-initiated fork", forked)

	draft, err := database.CreateDraftConversation(ctx, nil, nil, ConversationOptions{
		Kind:          BtwReaderKind,
		ParentPointer: &BtwParentPointer{Generation: 1, SequenceID: 2},
		ThinkingLevel: "high",
	}, "draft")
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := database.PromoteDraft(ctx, draft.ConversationID, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertScrubbed("promoted draft", promoted)

	malformed := *reader
	malformed.ConversationOptions = `{"kind":"btw_reader","parent_pointer":{"generation":0,"sequence_id":0}}`
	if _, ok := ManagedBtwReaderIdentity(malformed); ok {
		t.Fatal("malformed reader identity validated")
	}
}

// Recovery relies on the serialized form of a transcription item matching the
// ListConversationsWithQueuedTranscriptions filter; pin that here.
func TestListConversationsWithQueuedTranscriptions(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := t.Context()

	plain, err := database.CreateConversation(ctx, stringPtr("plain-queue"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AppendQueuedMessage(ctx, plain.ConversationID, QueuedMessage{ID: "plain", Llm: json.RawMessage(`{}`), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	parent, err := database.CreateConversation(ctx, stringPtr("transcription-queue"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AppendQueuedMessage(ctx, parent.ConversationID, QueuedMessage{
		ID: "t", Llm: json.RawMessage(`{}`), CreatedAt: time.Now().UTC(),
		Kind: QueuedMessageKindTranscription, State: QueuedMessageStateFailed,
		Transcription: &QueuedTranscription{MediaPath: "/tmp/a.webm"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ArchiveConversation(ctx, parent.ConversationID); err != nil {
		t.Fatal(err)
	}

	got, err := database.ListConversationsWithQueuedTranscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ConversationID != parent.ConversationID {
		t.Fatalf("conversations with queued transcriptions = %#v", got)
	}
}

// Every list and search query counts each listed conversation's subagents
// and running background jobs itself, so the list is one query.
func TestConversationListCounts(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := t.Context()

	parent, err := db.CreateConversation(ctx, stringPtr("counted-parent"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pid := parent.ConversationID
	for _, slug := range []string{"counted-child-a", "counted-child-b", "counted-child-c"} {
		if _, err := db.CreateSubagentConversation(ctx, slug, pid, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueriesTx(ctx, func(q *generated.Queries) error {
		for _, id := range []string{"running-1", "running-2", "exited"} {
			if err := q.InsertBackgroundJob(ctx, generated.InsertBackgroundJobParams{JobID: id, ConversationID: pid, StartedAt: time.Now()}); err != nil {
				return err
			}
		}
		_, err := q.MarkBackgroundJobExited(ctx, "exited")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	lists := map[string]func() ([]ConversationListItem, error){
		"ListConversations":    func() ([]ConversationListItem, error) { return db.ListConversations(ctx, 100, 0) },
		"ListAllConversations": func() ([]ConversationListItem, error) { return db.ListAllConversations(ctx, 100, 0) },
		"SearchConversations":  func() ([]ConversationListItem, error) { return db.SearchConversations(ctx, "counted-parent", 100, 0) },
		"SearchConversationsWithMessages": func() ([]ConversationListItem, error) {
			return db.SearchConversationsWithMessages(ctx, "counted-parent", 100, 0)
		},
		"SearchConversationsFTS": func() ([]ConversationListItem, error) {
			hits, err := db.SearchConversationsFTS(ctx, "counted-parent", false, 100, 0)
			items := make([]ConversationListItem, len(hits))
			for i, h := range hits {
				items[i] = h.ConversationListItem
			}
			return items, err
		},
	}
	for name, list := range lists {
		items, err := list()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		i := slices.IndexFunc(items, func(c ConversationListItem) bool { return c.ConversationID == pid })
		if i < 0 {
			t.Fatalf("%s: parent not listed", name)
		}
		if got := items[i]; got.SubagentCount != 3 || got.RunningBackgroundJobs != 2 {
			t.Errorf("%s: subagents %d, running jobs %d; want 3, 2", name, got.SubagentCount, got.RunningBackgroundJobs)
		}
	}
}

// A subagent slug that many earlier conversations already use still yields a
// new subagent: base, base-1..base-3, then base-<random>.
func TestGetOrCreateSubagentConversationCrowdedSlug(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := t.Context()
	adapter := &SubagentDBAdapter{DB: db}

	taken := map[string]bool{}
	for i := range 100 {
		slug := "review"
		if i > 0 {
			slug = fmt.Sprintf("review-%d", i)
		}
		if _, err := db.CreateConversation(ctx, &slug, true, nil, nil, ConversationOptions{}); err != nil {
			t.Fatal(err)
		}
		taken[slug] = true
	}
	parent, err := db.CreateConversation(ctx, stringPtr("crowded-parent"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}

	id, slug, err := adapter.GetOrCreateSubagentConversation(ctx, "review", parent.ConversationID, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if taken[slug] || !strings.HasPrefix(slug, "review-") {
		t.Fatalf("slug = %q, want a new review-<suffix>", slug)
	}
	again, _, err := adapter.GetOrCreateSubagentConversation(ctx, slug, parent.ConversationID, "/tmp")
	if err != nil || again != id {
		t.Fatalf("reuse %q: got %q, %v; want %q", slug, again, err, id)
	}
}
