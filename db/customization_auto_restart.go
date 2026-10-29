package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CustomizationAutoRestart is one automatic-restart event for a build.
type CustomizationAutoRestart struct {
	ConversationID string
	Commit         string
	ArtifactSHA256 string
	Event          string // attempted, failed, or cancelled
	Error          string
}

func (db *DB) RecordCustomizationAutoRestart(ctx context.Context, event CustomizationAutoRestart) error {
	switch event.Event {
	case "attempted", "failed", "cancelled":
	default:
		return fmt.Errorf("%w: unknown auto-restart event %q", ErrInvalid, event.Event)
	}
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO shelley_customization_auto_restarts
			(conversation_id,commit_sha,artifact_sha256,event,error,created_at) VALUES(?,?,?,?,?,?)`,
			event.ConversationID, event.Commit, event.ArtifactSHA256, event.Event, event.Error,
			tx.Now.UTC().Format(time.RFC3339Nano))
		return err
	})
}

// LastCustomizationAutoRestart returns the latest event for a build, or
// "" if Shelley never auto-restarted into it nor was told not to.
func (db *DB) LastCustomizationAutoRestart(ctx context.Context, artifactSHA256 string) (string, error) {
	var event string
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow(`SELECT event FROM shelley_customization_auto_restarts
			WHERE artifact_sha256=? ORDER BY id DESC LIMIT 1`, artifactSHA256).Scan(&event)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return event, err
}
