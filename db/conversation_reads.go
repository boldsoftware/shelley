package db

import (
	"context"
	"database/sql"
	"errors"

	"shelley.exe.dev/db/generated"
)

var ErrInvalidReadTime = errors.New("read_at must be positive and no later than the latest reply or unread mark")

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

// AcknowledgeConversation monotonically advances a user's read-through cursor
// up to the latest reply or the user's unread mark; reaching the mark clears
// it. Validation and MAX happen in one write transaction.
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

// MarkConversationUnread stamps the user's unread mark on a conversation and
// returns it. The mark is later than every receipt, so the conversation is
// unread until a receipt reaches it. sql.ErrNoRows: no such conversation.
func (db *DB) MarkConversationUnread(ctx context.Context, userID, conversationID string) (int64, error) {
	return db.QueriesTxRes(ctx, func(q *generated.Queries) (int64, error) {
		if err := q.StartConversationReads(ctx, userID); err != nil {
			return 0, err
		}
		return q.MarkConversationUnread(ctx, generated.MarkConversationUnreadParams{
			UserID:         userID,
			ConversationID: conversationID,
		})
	})
}

type ConversationReads struct {
	FirstSeenAt int64                                  `json:"first_seen_at"`
	Receipts    []generated.ConversationReadMarkersRow `json:"receipts"`
	// UnreadMarks are the marks no receipt has reached yet.
	UnreadMarks []generated.ConversationUnreadMarksRow `json:"unread_marks"`
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
		marks, err := q.ConversationUnreadMarks(ctx, userID)
		if err != nil {
			return ConversationReads{}, err
		}
		return ConversationReads{FirstSeenAt: rows[0].ReadAt, Receipts: rows[1:], UnreadMarks: marks}, nil
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
