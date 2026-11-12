package db

import (
	"context"
	"database/sql"
	"errors"

	"shelley.exe.dev/db/generated"
)

var ErrInvalidReadTime = errors.New("read_at must be positive and no later than the latest reply")

func (db *DB) ConversationAttention(ctx context.Context, ids []string) (map[string]int64, error) {
	return db.QueriesRes(ctx, func(q *generated.Queries) (map[string]int64, error) {
		rows, err := q.ConversationAttention(ctx, ids)
		if err != nil {
			return nil, err
		}
		result := make(map[string]int64, len(rows))
		for _, row := range rows {
			result[row.ConversationID] = row.MarkedAt
		}
		return result, nil
	})
}

// AcknowledgeConversation monotonically advances a user's read-through cursor.
// Validation and MAX happen in one write transaction.
func (db *DB) AcknowledgeConversation(ctx context.Context, userID, conversationID string, readAt int64) (int64, error) {
	if readAt <= 0 {
		return 0, ErrInvalidReadTime
	}
	return db.QueriesTxRes(ctx, func(q *generated.Queries) (int64, error) {
		if err := q.StartConversationReads(ctx, userID); err != nil {
			return 0, err
		}
		at, err := q.UpsertConversationRead(ctx, generated.UpsertConversationReadParams{
			UserID:         userID,
			ConversationID: conversationID,
			ReadAt:         readAt,
		})
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := q.GetConversation(ctx, conversationID); err != nil {
				return 0, err
			}
			return 0, ErrInvalidReadTime
		}
		return at, err
	})
}

type ConversationReads struct {
	FirstSeenAt int64                                  `json:"first_seen_at"`
	Receipts    []generated.ConversationReadMarkersRow `json:"receipts"`
}

func (db *DB) ConversationReads(ctx context.Context, userID string) (ConversationReads, error) {
	load := func(q *generated.Queries) (ConversationReads, error) {
		rows, err := q.ConversationReadMarkers(ctx, userID)
		if err != nil {
			return ConversationReads{}, err
		}
		// The visit row has an empty conversation ID and sorts first.
		if len(rows) == 0 || rows[0].ConversationID != "" {
			return ConversationReads{}, sql.ErrNoRows
		}
		return ConversationReads{FirstSeenAt: rows[0].ReadAt, Receipts: rows[1:]}, nil
	}
	reads, err := db.QueriesRes(ctx, load)
	if !errors.Is(err, sql.ErrNoRows) {
		return reads, err
	}
	return db.QueriesTxRes(ctx, func(q *generated.Queries) (ConversationReads, error) {
		if err := q.StartConversationReads(ctx, userID); err != nil {
			return ConversationReads{}, err
		}
		return load(q)
	})
}
