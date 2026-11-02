package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
)

// Readers must not need free disk for sorts and other temp tables: when the
// disk filled up, conversation list reads failed with SQLITE_FULL because
// their ORDER BY spilled to a temp file.
func TestPoolReadersUseMemoryTempStore(t *testing.T) {
	t.Parallel()
	p := setupTestDB(t).Pool()
	for range cap(p.readers) {
		var mode int
		if err := p.Rx(context.Background(), func(ctx context.Context, rx *Rx) error {
			return rx.Conn().QueryRowContext(ctx, "PRAGMA temp_store").Scan(&mode)
		}); err != nil {
			t.Fatal(err)
		}
		if mode != 2 {
			t.Fatalf("reader temp_store = %d, want 2 (MEMORY)", mode)
		}
	}
}

// fillDB makes the writer's next allocation fail with a real SQLITE_FULL.
func fillDB(t *testing.T, p *Pool) {
	t.Helper()
	ctx := context.Background()
	if err := p.Exec(ctx, "CREATE TABLE IF NOT EXISTS disk_full_filler (b BLOB)"); err != nil {
		t.Fatal(err)
	}
	var pages int
	if err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.Conn().QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages)
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Exec(ctx, fmt.Sprintf("PRAGMA max_page_count=%d", pages)); err != nil {
		t.Fatal(err)
	}
}

func TestPoolOnDiskFull(t *testing.T) {
	t.Parallel()
	p := setupTestDB(t).Pool()
	ctx := context.Background()
	fillDB(t, p)
	fired := 0
	p.OnDiskFull(func() { fired++ })

	err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Conn().ExecContext(ctx, "INSERT INTO disk_full_filler VALUES (zeroblob(1<<20))")
		return err
	})
	if !IsDiskFull(err) {
		t.Fatalf("Tx error = %v, want disk full", err)
	}
	if !strings.Contains(err.Error(), "database or disk is full") {
		t.Fatalf("Tx error = %q, want sqlite message", err)
	}
	if fired != 1 {
		t.Fatalf("Tx disk full fired %d hooks, want 1", fired)
	}

	err = p.Exec(ctx, "INSERT INTO disk_full_filler VALUES (zeroblob(1<<20))")
	if !IsDiskFull(err) {
		t.Fatalf("Exec error = %v, want disk full", err)
	}
	if fired != 2 {
		t.Fatalf("Exec disk full fired %d hooks, want 2", fired)
	}

	// Other failures are not disk-full.
	err = p.Exec(ctx, "INSERT INTO no_such_table VALUES (1)")
	if err == nil || IsDiskFull(err) {
		t.Fatalf("Exec error = %v, want non-disk-full error", err)
	}
	if fired != 2 {
		t.Fatalf("non-disk-full error fired hooks (%d)", fired)
	}

	// The writer is still usable once space is available again.
	if err := p.Exec(ctx, "PRAGMA max_page_count=1073741823"); err != nil {
		t.Fatal(err)
	}
	if err := p.Exec(ctx, "INSERT INTO disk_full_filler VALUES (zeroblob(1<<20))"); err != nil {
		t.Fatalf("writer unusable after disk full: %v", err)
	}
}

func TestIsDiskFull(t *testing.T) {
	t.Parallel()
	if !IsDiskFull(fmt.Errorf("write: %w", syscall.ENOSPC)) {
		t.Error("ENOSPC should be disk full")
	}
	if IsDiskFull(errors.New("database or disk is full")) {
		t.Error("string matching is not used")
	}
	if IsDiskFull(nil) {
		t.Error("nil is not disk full")
	}
}
