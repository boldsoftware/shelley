package db

import (
	"context"
	"strings"
	"testing"
)

// A deferred foreign-key violation succeeds in the callback but fails COMMIT.
// Callers (including persisted disk-notice dismissal) must see that failure.
func TestPoolTxCommitError(t *testing.T) {
	t.Parallel()
	database := setupTestDB(t)
	p := database.Pool()
	ctx := context.Background()
	if err := p.Exec(ctx, `
		CREATE TABLE commit_parent (id INTEGER PRIMARY KEY);
		CREATE TABLE commit_child (parent_id INTEGER REFERENCES commit_parent(id) DEFERRABLE INITIALLY DEFERRED);
	`); err != nil {
		t.Fatal(err)
	}
	commits := 0
	p.OnCommit(func() { commits++ })
	err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Conn().ExecContext(ctx, "INSERT INTO commit_child VALUES (42)")
		if err != nil {
			t.Fatalf("insert should succeed until commit: %v", err)
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("Tx error = %v, want commit error", err)
	}
	if commits != 0 {
		t.Fatalf("failed commit fired %d hooks", commits)
	}
	var count int
	if err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.Conn().QueryRowContext(ctx, "SELECT count(*) FROM commit_child").Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed commit left %d rows", count)
	}
	if err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Conn().ExecContext(ctx, "INSERT INTO commit_parent VALUES (42); INSERT INTO commit_child VALUES (42)")
		return err
	}); err != nil {
		t.Fatalf("connection was not usable after rollback: %v", err)
	}
	if commits != 1 {
		t.Fatalf("successful commit fired %d hooks, want 1", commits)
	}
}
