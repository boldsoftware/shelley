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

func (db *DB) AppendConversationChapter(ctx context.Context, conversationID, title string) (string, error) {
	return db.QueriesTxRes(ctx, func(q *generated.Queries) (string, error) {
		users, err := q.ListTypedUserMessages(ctx, conversationID)
		if err != nil {
			return "", err
		}
		if len(users) == 0 {
			return "", fmt.Errorf("no user message to anchor chapter")
		}
		themes := ConversationThemes{Chapters: []ConversationChapter{}}
		row, err := q.GetConversationThemes(ctx, conversationID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if err == nil {
			if err := json.Unmarshal([]byte(row.ChaptersJson), &themes.Chapters); err != nil {
				return "", err
			}
		}
		var anchorSequence int64
		var currentTitle string
		if len(themes.Chapters) > 0 {
			current := themes.Chapters[len(themes.Chapters)-1]
			anchor, err := q.GetMessage(ctx, current.MessageID)
			if err != nil {
				return "", err
			}
			anchorSequence = anchor.SequenceID
			currentTitle = current.Title
		}
		turns := 0
		for _, user := range users {
			var data struct {
				CompactionCarried string `json:"compaction_carried"`
			}
			if user.UserData != nil && *user.UserData != "" {
				if err := json.Unmarshal([]byte(*user.UserData), &data); err != nil {
					return "", err
				}
			}
			if user.SequenceID > anchorSequence && data.CompactionCarried != "true" {
				turns++
			}
		}
		if len(themes.Chapters) == 0 && turns < 3 {
			return "Too early for the first chapter; wait until at least three user messages. Do not retry; continue the conversation.", nil
		}
		if len(themes.Chapters) > 0 && (turns < 3 || strings.EqualFold(currentTitle, title)) {
			return fmt.Sprintf("Keep current chapter %q (%d user messages since it). Do not retry; continue the conversation.", currentTitle, turns), nil
		}
		themes.Chapters = append(themes.Chapters, ConversationChapter{Title: title, MessageID: users[0].MessageID})
		chapters, err := json.Marshal(themes.Chapters)
		if err != nil {
			return "", err
		}
		if err := q.UpsertConversationThemes(ctx, generated.UpsertConversationThemesParams{
			ConversationID: conversationID, ChaptersJson: string(chapters), ThroughMessageID: users[0].MessageID,
		}); err != nil {
			return "", err
		}
		return fmt.Sprintf("Recorded chapter %q. Continue the conversation.", title), nil
	})
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
	if len(themes.Chapters) == 0 {
		return fmt.Errorf("at least one chapter is required")
	}
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
