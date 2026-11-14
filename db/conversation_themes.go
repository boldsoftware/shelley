package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"shelley.exe.dev/db/generated"
)

type ConversationChapter struct {
	Title     string `json:"title"`
	MessageID string `json:"message_id"`
}

type ConversationThemes struct {
	Chapters         []ConversationChapter `json:"chapters"`
	ThroughMessageID string                `json:"through_message_id"`
}

func (db *DB) GetConversationThemes(ctx context.Context, conversationID string) (ConversationThemes, error) {
	return db.QueriesRes(ctx, func(q *generated.Queries) (ConversationThemes, error) {
		result := ConversationThemes{Chapters: []ConversationChapter{}}
		if _, err := q.GetConversation(ctx, conversationID); err != nil {
			return result, err
		}
		row, err := q.GetConversationThemes(ctx, conversationID)
		if errors.Is(err, sql.ErrNoRows) {
			return result, nil
		}
		if err != nil {
			return result, err
		}
		result.ThroughMessageID = row.ThroughMessageID
		err = json.Unmarshal([]byte(row.ChaptersJson), &result.Chapters)
		return result, err
	})
}

func (db *DB) ListConversationThemes(ctx context.Context) (map[string][]ConversationChapter, error) {
	return db.QueriesRes(ctx, func(q *generated.Queries) (map[string][]ConversationChapter, error) {
		rows, err := q.ListConversationThemes(ctx)
		if err != nil {
			return nil, err
		}
		result := make(map[string][]ConversationChapter, len(rows))
		for _, row := range rows {
			var chapters []ConversationChapter
			if err := json.Unmarshal([]byte(row.ChaptersJson), &chapters); err != nil {
				return nil, err
			}
			if len(chapters) > 0 {
				result[row.ConversationID] = chapters
			}
		}
		return result, nil
	})
}

func (db *DB) SetConversationThemes(ctx context.Context, conversationID string, themes ConversationThemes) error {
	return db.QueriesTx(ctx, func(q *generated.Queries) error {
		through, err := q.GetMessage(ctx, themes.ThroughMessageID)
		if err != nil {
			return err
		}
		if through.ConversationID != conversationID {
			return fmt.Errorf("through message belongs to another conversation")
		}
		var previous int64
		for _, chapter := range themes.Chapters {
			if strings.TrimSpace(chapter.Title) == "" {
				return fmt.Errorf("chapter title is required")
			}
			message, err := q.GetMessage(ctx, chapter.MessageID)
			if err != nil {
				return err
			}
			if message.ConversationID != conversationID || message.SequenceID <= previous || message.SequenceID > through.SequenceID {
				return fmt.Errorf("chapter anchors must belong to the conversation and be ordered within the summarized range")
			}
			previous = message.SequenceID
		}
		chapters, err := json.Marshal(themes.Chapters)
		if err != nil {
			return err
		}
		return q.UpsertConversationThemes(ctx, generated.UpsertConversationThemesParams{
			ConversationID: conversationID, ChaptersJson: string(chapters), ThroughMessageID: themes.ThroughMessageID,
		})
	})
}
