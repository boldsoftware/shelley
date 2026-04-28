package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type testTB interface {
	Helper()
	Fatalf(format string, args ...any)
	TempDir() string
}

var (
	testTemplateOnce     sync.Once
	testTemplateSnapshot []byte
	testTemplateErr      error
)

// NewTestDB creates a fresh migrated test database by writing a pre-migrated
// template snapshot to disk. This avoids replaying every migration for each
// individual test while keeping per-test database isolation intact.
func NewTestDB(tb testTB) (*DB, func()) {
	tb.Helper()

	templateSnapshot, err := ensureTestTemplateSnapshot()
	if err != nil {
		tb.Fatalf("Failed to prepare test database snapshot: %v", err)
	}

	tmpDir := tb.TempDir()
	dsn := filepath.Join(tmpDir, "test.db")
	if err := os.WriteFile(dsn, templateSnapshot, 0o600); err != nil {
		tb.Fatalf("Failed to write test database snapshot: %v", err)
	}

	database, err := New(Config{DSN: dsn})
	if err != nil {
		tb.Fatalf("Failed to create test database from snapshot: %v", err)
	}

	return database, func() {
		_ = database.Close()
	}
}

func ensureTestTemplateSnapshot() ([]byte, error) {
	testTemplateOnce.Do(func() {
		testTemplateSnapshot, testTemplateErr = buildTestTemplateSnapshot()
	})

	if testTemplateErr != nil {
		return nil, testTemplateErr
	}
	return testTemplateSnapshot, nil
}

func buildTestTemplateSnapshot() (snapshot []byte, err error) {
	templateDir, err := os.MkdirTemp("", "shelley-db-template-*")
	if err != nil {
		return nil, fmt.Errorf("mkdir temp template dir: %w", err)
	}
	defer os.RemoveAll(templateDir)

	templatePath := filepath.Join(templateDir, "test.db")
	database, err := New(Config{DSN: templatePath})
	if err != nil {
		return nil, fmt.Errorf("create template database: %w", err)
	}
	defer func() {
		if database == nil {
			return
		}
		if closeErr := database.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close template database: %w", closeErr)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := database.Migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrate template database: %w", err)
	}

	if err := database.Pool().Exec(ctx, "PRAGMA wal_checkpoint(TRUNCATE);"); err != nil {
		return nil, fmt.Errorf("checkpoint template database: %w", err)
	}

	if err := database.Close(); err != nil {
		return nil, fmt.Errorf("close template database: %w", err)
	}
	database = nil

	snapshot, err = os.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("read template database: %w", err)
	}

	return snapshot, nil
}
