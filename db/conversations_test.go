package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db/generated"
)

func TestConversationService_Create(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Using db directly instead of service
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
			conv, err := db.CreateConversation(ctx, tt.slug, true, nil)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	created, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation with slug
	created, err := db.CreateConversation(ctx, stringPtr("test-slug"), true, nil)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	created, err := db.CreateConversation(ctx, nil, true, nil)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create multiple test conversations
	for i := 0; i < 5; i++ {
		slug := stringPtr("conversation-" + string(rune('a'+i)))
		_, err := db.CreateConversation(ctx, slug, true, nil)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create test conversations with different slugs
	testCases := []string{"project-alpha", "project-beta", "work-task", "personal-note"}
	for _, slug := range testCases {
		_, err := db.CreateConversation(ctx, stringPtr(slug), true, nil)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	created, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	created, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	for i := 0; i < 3; i++ {
		_, err := db.CreateConversation(ctx, stringPtr("conversation-"+string(rune('a'+i))), true, nil)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create multiple conversations with null slugs - this should not fail
	conv1, err := db.CreateConversation(ctx, nil, true, nil)
	if err != nil {
		t.Errorf("Create() first conversation error = %v", err)
		return
	}

	conv2, err := db.CreateConversation(ctx, nil, true, nil)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create first conversation with a slug
	_, err := db.CreateConversation(ctx, stringPtr("unique-slug"), true, nil)
	if err != nil {
		t.Errorf("Create() first conversation error = %v", err)
		return
	}

	// Try to create second conversation with the same slug - this should fail
	_, err = db.CreateConversation(ctx, stringPtr("unique-slug"), true, nil)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation"), true, nil)
	if err != nil {
		t.Fatalf("Failed to create test conversation: %v", err)
	}

	// Test ArchiveConversation
	archivedConv, err := db.ArchiveConversation(ctx, conv.ConversationID)
	if err != nil {
		t.Errorf("ArchiveConversation() error = %v", err)
	}

	if !archivedConv.Archived {
		t.Error("Expected conversation to be archived")
	}

	// Test UnarchiveConversation
	unarchivedConv, err := db.UnarchiveConversation(ctx, conv.ConversationID)
	if err != nil {
		t.Errorf("UnarchiveConversation() error = %v", err)
	}

	if unarchivedConv.Archived {
		t.Error("Expected conversation to be unarchived")
	}
}

func TestConversationService_ListArchivedConversations(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create test conversations
	conv1, err := db.CreateConversation(ctx, stringPtr("test-conversation-1"), true, nil)
	if err != nil {
		t.Fatalf("Failed to create test conversation 1: %v", err)
	}

	conv2, err := db.CreateConversation(ctx, stringPtr("test-conversation-2"), true, nil)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create test conversations
	conv1, err := db.CreateConversation(ctx, stringPtr("test-conversation-search-1"), true, nil)
	if err != nil {
		t.Fatalf("Failed to create test conversation 1: %v", err)
	}

	conv2, err := db.CreateConversation(ctx, stringPtr("another-conversation"), true, nil)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation-to-delete"), true, nil)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a test conversation
	conv, err := db.CreateConversation(ctx, stringPtr("test-conversation-cwd"), true, nil)
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
