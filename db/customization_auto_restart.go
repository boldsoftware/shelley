package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"shelley.exe.dev/db/generated"
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
		return generated.New(tx.Conn()).CreateCustomizationAutoRestart(ctx, generated.CreateCustomizationAutoRestartParams{
			ConversationID: event.ConversationID, CommitSha: event.Commit, ArtifactSha256: event.ArtifactSHA256,
			Event: event.Event, Error: event.Error, CreatedAt: formatTime(tx.Now),
		})
	})
}

// LastCustomizationAutoRestart returns the latest event for a build, or
// "" if Shelley never auto-restarted into it nor was told not to.
func (db *DB) LastCustomizationAutoRestart(ctx context.Context, artifactSHA256 string) (string, error) {
	var event string
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		event, err = generated.New(rx.Conn()).LastCustomizationAutoRestart(ctx, artifactSHA256)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return event, err
}
