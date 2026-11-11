package db

import (
	"context"
	"testing"
	"time"

	"shelley.exe.dev/db/generated"
)

// The store keeps pushed times in SQLite's format, which SQLite's date
// functions read; migration 080 converts the Go text form it used to keep.
func TestArchiveStoreTimes(t *testing.T) {
	ctx := context.Background()
	db := setupDBMigratedThrough(t, 79)
	defer db.Close()
	at := time.Date(2026, 11, 3, 9, 30, 15, 123456789, time.FixedZone("PST", -8*3600))
	push := func(id string) {
		t.Helper()
		_, err := db.ReceiveArchiveStorePush(ctx, ArchiveStoreSource{SourceVM: "vm", UserID: "usr", Email: "a@example.com"}, id, ArchiveStorePush{
			Conversation: ArchiveStoreConversation{ConversationID: id, CreatedAt: at, UpdatedAt: at},
			ThroughSeq:   1,
			Messages:     []ArchiveStoreMessage{{MessageID: id + "-m", SequenceID: 1, Type: "user", CreatedAt: at}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	push("cNew")
	// What the store wrote before: Go's text form.
	push("cOld")
	old := at.UTC().String()
	for _, q := range []string{
		"UPDATE archive_store_conversations SET created_at = ?, updated_at = ? WHERE conversation_id = 'cOld'",
		"UPDATE archive_store_messages SET created_at = ?1 WHERE conversation_id = 'cOld'",
	} {
		if err := db.pool.Exec(ctx, q, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.runMigration(ctx, "080-archive-store-times.sql", 80); err != nil {
		t.Fatal(err)
	}
	const want = "2026-11-03 17:30:15"
	n := 0
	if err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := rx.Conn().QueryContext(ctx, `
			SELECT conversation_id, CAST(created_at AS TEXT), CAST(updated_at AS TEXT), datetime(updated_at) FROM archive_store_conversations
			UNION ALL
			SELECT conversation_id, CAST(created_at AS TEXT), CAST(created_at AS TEXT), datetime(created_at) FROM archive_store_messages`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, created, updated, parsed string
			if err := rows.Scan(&id, &created, &updated, &parsed); err != nil {
				return err
			}
			if created != want || updated != want || parsed != want {
				t.Errorf("%s: created_at %q, updated_at %q, datetime %q; want %q", id, created, updated, parsed, want)
			}
			n++
		}
		return rows.Err()
	}); err != nil || n != 4 {
		t.Fatalf("rows: %d, %v", n, err)
	}
	// And they read back as the same instant.
	var c generated.GetArchiveStoreConversationRow
	if err := db.Queries(ctx, func(q *generated.Queries) (err error) {
		c, err = q.GetArchiveStoreConversation(ctx, generated.GetArchiveStoreConversationParams{SourceVm: "vm", ConversationID: "cNew"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !c.CreatedAt.Equal(at.Truncate(time.Second)) {
		t.Errorf("read back %v, want %v", c.CreatedAt, at)
	}
}
