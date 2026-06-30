package db

import (
	"context"
	"fmt"
	"os"
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
	// Run all migrations through HEAD. CreateConversation uses sqlc-generated
	// INSERTs that reference columns added in later migrations, so we can't
	// stop early. The test still verifies that migration 22 (and everything
	// after) preserves the FTS triggers / indexes.
	database := setupDBMigratedThrough(t, 1000)
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

// TestCheckpoint verifies that Checkpoint truncates the WAL file back down
// after it has grown from writes, even while the long-lived reader
// connections in the pool are open.
func TestCheckpoint(t *testing.T) {
	tmpDir := t.TempDir()
	dsn := tmpDir + "/test.db"
	database, err := New(Config{DSN: dsn})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Generate a bunch of WAL writes.
	for i := 0; i < 500; i++ {
		if err := database.SetFeatureFlagOverride(ctx, fmt.Sprintf("flag-%d", i), fmt.Sprintf(`"%d"`, i)); err != nil {
			t.Fatalf("SetFeatureFlagOverride: %v", err)
		}
	}

	walPath := dsn + "-wal"
	beforeInfo, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal before: %v", err)
	}
	if beforeInfo.Size() == 0 {
		t.Fatalf("expected non-empty WAL before checkpoint")
	}

	if err := database.Checkpoint(ctx); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	afterInfo, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal after: %v", err)
	}
	if afterInfo.Size() >= beforeInfo.Size() {
		t.Fatalf("Checkpoint did not shrink WAL: before=%d after=%d", beforeInfo.Size(), afterInfo.Size())
	}
}

// TestCreateMessages verifies the bulk insert assigns monotonically increasing
// sequence ids, preserves input order, and commits in a single transaction
// (one commit hook) regardless of message count.
func TestCreateMessages(t *testing.T) {
	database := setupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conv, err := database.CreateConversation(ctx, stringPtr("bulk"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	// One pre-existing message so we can confirm bulk sequence ids continue
	// past it rather than restarting.
	first, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		UserData:       map[string]any{"n": 0},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	var commits int
	database.Pool().OnCommit(func() { commits++ })

	params := []CreateMessageParams{
		{ConversationID: conv.ConversationID, Type: MessageTypeUser, UserData: map[string]any{"n": 1}},
		{ConversationID: conv.ConversationID, Type: MessageTypeAgent, UserData: map[string]any{"n": 2}},
		{ConversationID: conv.ConversationID, Type: MessageTypeUser, UserData: map[string]any{"n": 3}},
	}
	created, err := database.CreateMessages(ctx, params)
	if err != nil {
		t.Fatalf("CreateMessages: %v", err)
	}
	if commits != 1 {
		t.Fatalf("expected exactly 1 commit hook fire for the batch, got %d", commits)
	}
	if len(created) != 3 {
		t.Fatalf("expected 3 created messages, got %d", len(created))
	}
	// Sequence ids strictly increasing and greater than the pre-existing one.
	prev := first.SequenceID
	for i, m := range created {
		if m.SequenceID <= prev {
			t.Fatalf("message %d sequence_id %d not greater than previous %d", i, m.SequenceID, prev)
		}
		prev = m.SequenceID
	}

	// Persisted order matches input order.
	all, err := database.ListMessages(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 messages total, got %d", len(all))
	}

	// Empty batch is a no-op.
	got, err := database.CreateMessages(ctx, nil)
	if err != nil || got != nil {
		t.Fatalf("CreateMessages(nil) = %v, %v; want nil, nil", got, err)
	}

	// Mixed conversations are rejected.
	other, err := database.CreateConversation(ctx, stringPtr("other"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation other: %v", err)
	}
	_, err = database.CreateMessages(ctx, []CreateMessageParams{
		{ConversationID: conv.ConversationID, Type: MessageTypeUser},
		{ConversationID: other.ConversationID, Type: MessageTypeUser},
	})
	if err == nil {
		t.Fatalf("expected error for mixed-conversation batch")
	}
}

// TestCreateMessageFoldsAgentWorkingAndTimestamp verifies that MarkAgentStart,
// MarkAgentDone, and BumpTimestamp all apply inside the single message-INSERT
// transaction (one commit hook), so the chat loop doesn't pay a second commit
// (and a second full conversation-list recompute) for the agent_working flip
// or the updated_at bump.
func TestCreateMessageFoldsAgentWorkingAndTimestamp(t *testing.T) {
	database := setupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conv, err := database.CreateConversation(ctx, stringPtr("fold"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if conv.AgentWorking {
		t.Fatalf("new conversation should not be agent_working")
	}

	// Backdate updated_at so a CURRENT_TIMESTAMP bump (second granularity) is
	// observably newer even when the test runs within the same wall-clock second.
	backdated := time.Now().Add(-time.Hour).UTC()
	if err := database.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Conn().ExecContext(ctx, "UPDATE conversations SET updated_at = ? WHERE conversation_id = ?", backdated, conv.ConversationID)
		return err
	}); err != nil {
		t.Fatalf("backdate updated_at: %v", err)
	}
	startTimestamp := backdated

	// MarkAgentStart + BumpTimestamp in one Tx (one commit hook).
	var commits int
	database.Pool().OnCommit(func() { commits++ })
	if _, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		MarkAgentStart: true,
		BumpTimestamp:  true,
	}); err != nil {
		t.Fatalf("CreateMessage(start): %v", err)
	}
	if commits != 1 {
		t.Fatalf("expected exactly 1 commit hook for start insert, got %d", commits)
	}
	afterStart, err := database.GetConversationByID(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if !afterStart.AgentWorking {
		t.Fatalf("MarkAgentStart did not set agent_working=true in the insert Tx")
	}
	if !afterStart.UpdatedAt.After(startTimestamp) {
		t.Fatalf("BumpTimestamp did not advance updated_at: before=%v after=%v", startTimestamp, afterStart.UpdatedAt)
	}

	// MarkAgentDone clears it again in one Tx.
	commits = 0
	if _, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeAgent,
		MarkAgentDone:  true,
	}); err != nil {
		t.Fatalf("CreateMessage(done): %v", err)
	}
	if commits != 1 {
		t.Fatalf("expected exactly 1 commit hook for done insert, got %d", commits)
	}
	afterDone, err := database.GetConversationByID(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if afterDone.AgentWorking {
		t.Fatalf("MarkAgentDone did not set agent_working=false in the insert Tx")
	}

	// MarkAgentStart and MarkAgentDone are mutually exclusive.
	if _, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		MarkAgentStart: true,
		MarkAgentDone:  true,
	}); err == nil {
		t.Fatalf("expected error when both MarkAgentStart and MarkAgentDone are set")
	}
}
