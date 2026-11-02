package db

import (
	"context"
	"database/sql"
	"errors"

	"shelley.exe.dev/db/generated"
)

// Action runs report to their conversation's agent through its durable message
// queue. A run's report moves from pending to sent or skipped exactly once;
// sending appends the queue item in the same transaction, so a restart
// neither loses nor repeats it.

// ListDueActionReports returns finished runs with a report pending.
func (db *DB) ListDueActionReports(ctx context.Context) ([]ActionRun, error) {
	var runs []ActionRun
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := generated.New(rx.Conn()).ListDueActionReports(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			run, err := newActionRun(row)
			if err != nil {
				return err
			}
			runs = append(runs, *run)
		}
		return nil
	})
	return runs, err
}

// SkipActionReport decides a pending report needs no message. It reports
// whether the report was still pending.
func (db *DB) SkipActionReport(ctx context.Context, runID string) (bool, error) {
	var changed bool
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		n, err := generated.New(tx.Conn()).SetPendingActionReport(ctx, generated.SetPendingActionReportParams{Report: "skipped", ID: runID})
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
		q := generated.New(tx.Conn())
		run, err := q.GetActionRun(ctx, runID)
		if errors.Is(err, sql.ErrNoRows) || err == nil && run.Report != "pending" {
			return nil
		}
		if err != nil {
			return err
		}
		if err := mutateQueuedMessagesTx(ctx, tx, run.ConversationID, func(msgs []QueuedMessage) ([]QueuedMessage, error) {
			return append(msgs, qm), nil
		}); err != nil {
			return err
		}
		_, err = q.SetPendingActionReport(ctx, generated.SetPendingActionReportParams{Report: "sent", ID: runID})
		queued = err == nil
		return err
	})
	return queued, err
}
