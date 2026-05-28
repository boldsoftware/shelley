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

	db, cleanup := NewTestDB(t)
	t.Cleanup(cleanup)
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

// TestDB_Migrate_TracksByName verifies that the migrations table is keyed
// by filename. If a row exists with a different name for an unapplied
// migration's number, the new migration must still be executed.
func TestDB_Migrate_TracksByName(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := New(Config{DSN: tmpDir + "/test.db"})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	// Simulate the cross-branch rename scenario: this database was
	// initialized against a branch where 017 was a different file. We
	// rewrite the migrations row to that old name and undo what real
	// 017 did, then re-run Migrate to confirm it doesn't silently skip
	// the on-disk 017 just because some other 017 row already exists.
	err = db.Pool().Tx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.Exec("DELETE FROM migrations WHERE migration_number = 17"); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO migrations (migration_number, migration_name) VALUES (17, '017-some-old-name.sql')"); err != nil {
			return err
		}
		_, err := tx.Exec("ALTER TABLE conversations DROP COLUMN conversation_options")
		return err
	})
	if err != nil {
		t.Fatalf("failed to set up rename scenario: %v", err)
	}

	// Now re-run Migrate. The real 017 has a different filename, so it must run.
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() after rename: %v", err)
	}

	var gotColumn int
	err = db.Pool().Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT COUNT(*) FROM pragma_table_info('conversations') WHERE name = 'conversation_options'").Scan(&gotColumn)
	})
	if err != nil {
		t.Fatalf("failed to check column existence: %v", err)
	}
	if gotColumn != 1 {
		t.Fatalf("conversation_options column was not re-added; row keyed by number prevented re-run")
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
			Model:          nil,
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
			SequenceID:     1,
			Generation:     1,
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
	result, err := WithTxRes(db, ctx, func(queries *generated.Queries) (string, error) {
		return "test result", nil
	})
	if err != nil {
		t.Errorf("WithTxRes() error = %v", err)
	}

	if result != "test result" {
		t.Errorf("Expected 'test result', got %s", result)
	}

	// Test WithTxRes with error handling
	_, err = WithTxRes(db, ctx, func(queries *generated.Queries) (string, error) {
		return "", fmt.Errorf("test error")
	})

	if err == nil {
		t.Error("Expected error from WithTxRes, got none")
	}
}

func TestNewTestDB_IsolatedCopies(t *testing.T) {
	db1, cleanup1 := NewTestDB(t)
	defer cleanup1()

	db2, cleanup2 := NewTestDB(t)
	defer cleanup2()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := db1.CreateConversation(ctx, stringPtr("only-in-db1"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("Failed to create conversation in first test db: %v", err)
	}

	var count int64
	err = db2.Queries(ctx, func(q *generated.Queries) error {
		var qerr error
		count, qerr = q.CountConversations(ctx)
		return qerr
	})
	if err != nil {
		t.Fatalf("Failed to count conversations in second test db: %v", err)
	}

	if count != 0 {
		t.Fatalf("Expected second test db to start empty, got %d conversations", count)
	}
}

func TestMessagesTypeHasNoCheckConstraint(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	assertMessagesTableHasNoCheck(t, db, ctx)
}

func TestDropMessageTypeCheckMigrationPreservesSearch(t *testing.T) {
	database := setupDBMigratedSkipping(t, 22)
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conv, err := database.CreateConversation(ctx, stringPtr("migration-fts"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	userMsg, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		UserData:       map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "pelican before migration"}}},
	})
	if err != nil {
		t.Fatalf("CreateMessage user: %v", err)
	}
	if _, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeTool,
		UserData:       map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "tool noise should not be indexed"}}},
	}); err != nil {
		t.Fatalf("CreateMessage tool: %v", err)
	}

	if err := database.runMigration(ctx, "022-drop-message-type-check-constraint.sql", 22); err != nil {
		t.Fatalf("run migration 022: %v", err)
	}

	assertMessagesTableHasNoCheck(t, database, ctx)
	assertMessagesIndexesExist(
		t, database, ctx,
		"idx_messages_conversation_id",
		"idx_messages_conversation_sequence",
		"idx_messages_type",
		"idx_messages_conversation_generation_context_sequence",
	)
	assertTriggersExist(t, database, ctx, "messages_fts_ai", "messages_fts_ad", "messages_fts_au")
	assertSearchHits(t, database, ctx, "pelican", conv.ConversationID)
	assertSearchMisses(t, database, ctx, "noise")

	updated := `{"Content":[{"Type":2,"Text":"albatross after update"}]}`
	if err := database.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.UpdateMessageUserData(ctx, generated.UpdateMessageUserDataParams{
			MessageID: userMsg.MessageID,
			UserData:  &updated,
		})
	}); err != nil {
		t.Fatalf("UpdateMessageUserData: %v", err)
	}
	assertSearchMisses(t, database, ctx, "pelican")
	assertSearchHits(t, database, ctx, "albatross", conv.ConversationID)

	agentMsg, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeAgent,
		LLMData:        map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "cormorant after insert"}}},
	})
	if err != nil {
		t.Fatalf("CreateMessage agent after migration: %v", err)
	}
	assertSearchHits(t, database, ctx, "cormorant", conv.ConversationID)

	if err := database.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.DeleteMessage(ctx, agentMsg.MessageID)
	}); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	assertSearchMisses(t, database, ctx, "cormorant")
}

// setupDBMigratedSkipping migrates the test DB through every embedded
// migration except those whose numeric prefix is in skip. Lets us exercise
// a specific migration in isolation (run it after CreateConversation/
// CreateMessage have populated data) while still ensuring the rest of the
// schema is recent enough for sqlc-generated queries to work.
func setupDBMigratedSkipping(t *testing.T, skip ...int) *DB {
	t.Helper()
	skipSet := make(map[int]struct{}, len(skip))
	for _, n := range skip {
		skipSet[n] = struct{}{}
	}

	tmpDir := t.TempDir()
	database, err := New(Config{DSN: tmpDir + "/test.db"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	entries, err := schemaFS.ReadDir("schema")
	if err != nil {
		database.Close()
		t.Fatalf("read schema: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || len(entry.Name()) < 3 {
			continue
		}
		var migrationNumber int
		if _, err := fmt.Sscanf(entry.Name()[:3], "%d", &migrationNumber); err != nil {
			continue
		}
		if _, skipped := skipSet[migrationNumber]; skipped {
			continue
		}
		if err := database.runMigration(ctx, entry.Name(), migrationNumber); err != nil {
			database.Close()
			t.Fatalf("run migration %s: %v", entry.Name(), err)
		}
	}
	return database
}

func setupDBMigratedThrough(t *testing.T, lastMigration int) *DB {
	t.Helper()

	tmpDir := t.TempDir()
	database, err := New(Config{DSN: tmpDir + "/test.db"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	entries, err := schemaFS.ReadDir("schema")
	if err != nil {
		database.Close()
		t.Fatalf("read schema: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || len(entry.Name()) < 3 {
			continue
		}
		var migrationNumber int
		if _, err := fmt.Sscanf(entry.Name()[:3], "%d", &migrationNumber); err != nil || migrationNumber > lastMigration {
			continue
		}
		if err := database.runMigration(ctx, entry.Name(), migrationNumber); err != nil {
			database.Close()
			t.Fatalf("run migration %s: %v", entry.Name(), err)
		}
	}
	return database
}

func assertMessagesTableHasNoCheck(t *testing.T, db *DB, ctx context.Context) {
	t.Helper()

	var createSQL string
	err := db.Pool().Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'messages'").Scan(&createSQL)
	})
	if err != nil {
		t.Fatalf("query messages schema: %v", err)
	}
	if strings.Contains(strings.ToUpper(createSQL), "CHECK") {
		t.Fatalf("messages table has a CHECK constraint; do not constrain messages.type in SQLite:\n%s", createSQL)
	}
}

func assertMessagesIndexesExist(t *testing.T, db *DB, ctx context.Context, names ...string) {
	t.Helper()

	found := map[string]bool{}
	err := db.Pool().Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := rx.Query("SELECT name FROM sqlite_schema WHERE type = 'index' AND tbl_name = 'messages'")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			found[name] = true
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("query messages indexes: %v", err)
	}
	for _, name := range names {
		if !found[name] {
			t.Fatalf("missing messages index %s; found %#v", name, found)
		}
	}
}

func assertTriggersExist(t *testing.T, db *DB, ctx context.Context, names ...string) {
	t.Helper()

	found := map[string]bool{}
	err := db.Pool().Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := rx.Query("SELECT name FROM sqlite_schema WHERE type = 'trigger'")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			found[name] = true
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("query triggers: %v", err)
	}
	for _, name := range names {
		if !found[name] {
			t.Fatalf("missing trigger %s; found %#v", name, found)
		}
	}
}

func assertSearchHits(t *testing.T, db *DB, ctx context.Context, query, conversationID string) {
	t.Helper()

	results, err := db.SearchConversationsFTS(ctx, query, 50, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS(%q): %v", query, err)
	}
	for _, result := range results {
		if result.Conversation.ConversationID == conversationID {
			return
		}
	}
	t.Fatalf("SearchConversationsFTS(%q) did not include %s: %#v", query, conversationID, results)
}

func assertSearchMisses(t *testing.T, db *DB, ctx context.Context, query string) {
	t.Helper()

	results, err := db.SearchConversationsFTS(ctx, query, 50, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS(%q): %v", query, err)
	}
	if len(results) != 0 {
		t.Fatalf("SearchConversationsFTS(%q) got %d results, want 0: %#v", query, len(results), results)
	}
}
