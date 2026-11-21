package db

import (
	"context"
	"os"
	"testing"
)

// Migration 086 removes the archive store's triggers, views and
// bookkeeping, and its tables only where they are empty: data a store kept
// stays, inert.
func TestMigrateRemovesArchiveStoreKeepingData(t *testing.T) {
	const source = `
INSERT INTO archive_store_sources (source_vm, user_id, email) VALUES ('vm', 'u', 'a@example.com');
`
	// A store with a conversation (its triggers index it for search and
	// research); its reads log is empty. An archiver that recorded a
	// conversation, mid-way through a temporary turn.
	const data = source + `
INSERT INTO archive_store_conversations (source_vm, conversation_id, slug, created_at, updated_at)
  VALUES ('vm', 'c', 'webhooks', '2026-11-11 00:00:00', '2026-11-11 00:00:00');
INSERT INTO archive_store_messages (source_vm, conversation_id, sequence_id, message_id, type, llm_data, created_at, generation, excluded_from_context, user_email)
  VALUES ('vm', 'c', 1, 'm', 'user', '{"Content":[{"Type":2,"Text":"hello webhooks"}]}', '2026-11-11 00:00:00', 0, 0, 'a@example.com');
INSERT INTO feature_flags (name, value) VALUES ('archiver', '"https://store.example/"');
INSERT INTO conversations (conversation_id, slug) VALUES ('local', 'local');
INSERT INTO archiver_projects (singleton, projects) VALUES (1, '[]');
INSERT INTO temporary_runs (conversation_id) VALUES ('local');
INSERT INTO messages (message_id, conversation_id, sequence_id, type) VALUES ('m1', 'local', 1, 'user');
`
	// Which of the tables of migrations 077-082 086 keeps.
	kept := func(tables ...string) map[string]bool {
		m := map[string]bool{
			"archive_store_sources": false, "archive_store_conversations": false,
			"archive_store_messages": false, "archive_store_messages_fts": false,
			"archive_research_docs": false, "archive_research_fts": false, "archive_research_reads": false,
			"archiver_pushes": false, "archiver_projects": false, "temporary_runs": false, "temporary_messages": false,
		}
		for _, table := range tables {
			m[table] = true
		}
		return m
	}
	for _, tc := range []struct {
		name string
		data string
		kept map[string]bool
	}{
		{"empty", "", kept()},
		{"source", source, kept("archive_store_sources")},
		{"data", data, kept("archive_store_sources", "archive_store_conversations", "archive_store_messages",
			"archive_store_messages_fts", "archive_research_docs", "archive_research_fts")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupDBMigratedThrough(t, 85)
			defer db.Close()
			ctx := t.Context()
			schema, err := os.ReadFile("testdata/archive-store-schema.sql")
			if err != nil {
				t.Fatal(err)
			}
			// Raw SQL: nothing queries these tables anymore.
			exec := func(sql string) {
				t.Helper()
				if err := db.Pool().Tx(ctx, func(ctx context.Context, tx *Tx) error {
					_, err := tx.Conn().ExecContext(ctx, sql)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			count := func(sql string) int {
				t.Helper()
				var n int
				if err := db.Pool().Rx(ctx, func(ctx context.Context, rx *Rx) error {
					return rx.Conn().QueryRowContext(ctx, sql).Scan(&n)
				}); err != nil {
					t.Fatal(err)
				}
				return n
			}
			exec(string(schema) + tc.data)
			if tc.data == data && count("SELECT COUNT(*) FROM archiver_pushes")+count("SELECT COUNT(*) FROM temporary_messages") != 2 {
				t.Fatal("the archiver's triggers didn't record")
			}
			rows := map[string]int{}
			for table := range tc.kept {
				rows[table] = count("SELECT COUNT(*) FROM " + table)
			}

			if err := db.Migrate(ctx); err != nil {
				t.Fatal(err)
			}

			for table, kept := range tc.kept {
				if got := count("SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = '"+table+"'") == 1; got != kept {
					t.Errorf("%s kept: %v, want %v", table, got, kept)
				} else if kept && count("SELECT COUNT(*) FROM "+table) != rows[table] {
					t.Errorf("%s lost rows", table)
				}
			}
			if n := count(`SELECT COUNT(*) FROM sqlite_schema WHERE type IN ('trigger', 'view') AND (name LIKE 'archiv%' OR name LIKE 'temporary%')`); n != 0 {
				t.Errorf("%d archive store triggers and views remain", n)
			}
			if tc.data == "" {
				// Nothing at all, FTS shadow tables and indexes included.
				if n := count(`SELECT COUNT(*) FROM sqlite_schema WHERE name LIKE 'archiv%' OR name LIKE 'temporary%'`); n != 0 {
					t.Errorf("%d archive store objects remain", n)
				}
			}
			if tc.kept["archive_store_messages_fts"] {
				if n := count(`SELECT COUNT(*) FROM archive_store_messages_fts WHERE archive_store_messages_fts MATCH 'webhooks'`); n != 1 {
					t.Errorf("store search finds %d messages, want 1", n)
				}
			}
			if n := count(`SELECT COUNT(*) FROM feature_flags WHERE name = 'archiver'`); n != 0 {
				t.Errorf("archiver flag remains")
			}
			exec(`INSERT INTO conversations (conversation_id, slug) VALUES ('after', 'after');
INSERT INTO messages (message_id, conversation_id, sequence_id, type) VALUES ('m2', 'after', 1, 'user');
DELETE FROM conversations WHERE conversation_id = 'local';`)
		})
	}
}
