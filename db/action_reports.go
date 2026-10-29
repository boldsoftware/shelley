package db

import (
	"context"
	"database/sql"
	"errors"
)

// Action runs report to their conversation's agent through its durable message
// queue. A run's report moves from pending to sent or skipped exactly once;
// sending appends the queue item in the same transaction, so a restart
// neither loses nor repeats it.

// ListDueActionReports returns finished runs with a report pending.
func (db *DB) ListDueActionReports(ctx context.Context) ([]ActionRun, error) {
	return db.listActionRuns(ctx, `WHERE status!='running' AND report='pending'`)
}

// SkipActionReport decides a pending report needs no message. It reports
// whether the report was still pending.
func (db *DB) SkipActionReport(ctx context.Context, runID string) (bool, error) {
	var changed bool
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		result, err := tx.Exec("UPDATE action_runs SET report='skipped' WHERE id=? AND report='pending'", runID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		changed = n == 1
		return err
	})
	return changed, err
}

// QueueActionReport appends qm to the queue of the run's conversation and
// marks the report sent, unless it is no longer pending.
func (db *DB) QueueActionReport(ctx context.Context, runID string, qm QueuedMessage) (bool, error) {
	var queued bool
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		var conversationID string
		err := tx.QueryRow("SELECT conversation_id FROM action_runs WHERE id=? AND report='pending'", runID).Scan(&conversationID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := mutateQueuedMessagesTx(ctx, tx, conversationID, func(msgs []QueuedMessage) ([]QueuedMessage, error) {
			return append(msgs, qm), nil
		}); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE action_runs SET report='sent' WHERE id=?", runID); err != nil {
			return err
		}
		queued = true
		return nil
	})
	return queued, err
}
