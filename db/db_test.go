package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db/generated"
)

// setupTestDB creates a test database with schema migrated
func setupTestDB(t *testing.T) *DB {
	t.Helper()

	// Use a temporary file instead of :memory: because the pool requires multiple connections
	tmpDir := t.TempDir()
	db, err := New(Config{DSN: tmpDir + "/test.db"})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Failed to migrate test database: %v", err)
	}

	return db
}

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name:    "memory database not supported",
			cfg:     Config{DSN: ":memory:"},
			wantErr: true,
		},
		{
			name:    "empty DSN",
			cfg:     Config{DSN: ""},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := New(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if db != nil {
				defer db.Close()
			}
		})
	}
}

func TestDB_Migrate(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := New(Config{DSN: tmpDir + "/test.db"})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run migrations first time
	if err := db.Migrate(ctx); err != nil {
		t.Errorf("Migrate() error = %v", err)
	}

	// Verify tables were created by trying to count conversations
	var count int64
	err = db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		count, err = q.CountConversations(ctx)
		return err
	})
	if err != nil {
		t.Errorf("Failed to query conversations after migration: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 conversations, got %d", count)
	}

	// Run migrations a second time to verify idempotency
	if err := db.Migrate(ctx); err != nil {
		t.Errorf("Second Migrate() error = %v", err)
	}

	// Verify we can still query after running migrations twice
	err = db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		count, err = q.CountConversations(ctx)
		return err
	})
	if err != nil {
		t.Errorf("Failed to query conversations after second migration: %v", err)
	}
}

func TestDB_WithTx(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test successful transaction
	err := db.WithTx(ctx, func(q *generated.Queries) error {
		_, err := q.CreateConversation(ctx, generated.CreateConversationParams{
			ConversationID: "test-conv-1",
			Slug:           stringPtr("test-slug"),
			UserInitiated:  true,
		})
		return err
	})
	if err != nil {
		t.Errorf("WithTx() error = %v", err)
	}

	// Verify the conversation was created
	var conv generated.Conversation
	err = db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		conv, err = q.GetConversation(ctx, "test-conv-1")
		return err
	})
	if err != nil {
		t.Errorf("Failed to get conversation after transaction: %v", err)
	}
	if conv.ConversationID != "test-conv-1" {
		t.Errorf("Expected conversation ID 'test-conv-1', got %s", conv.ConversationID)
	}
}

// stringPtr returns a pointer to the given string
func stringPtr(s string) *string {
	return &s
}

func TestDB_ForeignKeyConstraints(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try to create a message with a non-existent conversation_id
	// This should fail due to foreign key constraint
	err := db.QueriesTx(ctx, func(q *generated.Queries) error {
		_, err := q.CreateMessage(ctx, generated.CreateMessageParams{
			MessageID:      "test-msg-1",
			ConversationID: "non-existent-conversation",
			Type:           "user",
		})
		return err
	})

	if err == nil {
		t.Error("Expected error when creating message with non-existent conversation_id")
		return
	}

	// Verify the error is related to foreign key constraint
	if !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Errorf("Expected foreign key constraint error, got: %v", err)
	}
}

func TestDB_Pool(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Test Pool method
	pool := db.Pool()
	if pool == nil {
		t.Error("Expected non-nil pool")
	}
}

func TestDB_WithTxRes(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test WithTxRes with a simple function that returns a string
	result, err := WithTxRes[string](db, ctx, func(queries *generated.Queries) (string, error) {
		return "test result", nil
	})
	if err != nil {
		t.Errorf("WithTxRes() error = %v", err)
	}

	if result != "test result" {
		t.Errorf("Expected 'test result', got %s", result)
	}

	// Test WithTxRes with error handling
	_, err = WithTxRes[string](db, ctx, func(queries *generated.Queries) (string, error) {
		return "", fmt.Errorf("test error")
	})

	if err == nil {
		t.Error("Expected error from WithTxRes, got none")
	}
}
