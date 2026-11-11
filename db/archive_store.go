package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"shelley.exe.dev/db/generated"
)

// Shelley archive store (server/archive_store.go): the push protocol's wire types, and the
// store's side of it.

// ArchiveStoreConversation is a pushed conversation's metadata.
type ArchiveStoreConversation struct {
	ConversationID       string    `json:"conversation_id"`
	ParentConversationID *string   `json:"parent_conversation_id"`
	Slug                 *string   `json:"slug"`
	Project              string    `json:"project"`
	Model                *string   `json:"model"`
	Cwd                  *string   `json:"cwd"`
	Archived             bool      `json:"archived"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// ArchiveStoreMessage is a pushed message.
type ArchiveStoreMessage struct {
	MessageID           string    `json:"message_id"`
	SequenceID          int64     `json:"sequence_id"`
	Type                string    `json:"type"`
	LlmData             *string   `json:"llm_data"`
	UserData            *string   `json:"user_data"`
	UsageData           *string   `json:"usage_data"`
	DisplayData         *string   `json:"display_data"`
	CreatedAt           time.Time `json:"created_at"`
	Generation          int64     `json:"generation"`
	ExcludedFromContext bool      `json:"excluded_from_context"`
	ModelName           *string   `json:"model_name"`
	UserEmail           *string   `json:"user_email"`
	// ExternalMessageID is set on a message that came in through a channel
	// (a text); optional, since archivers that predate it omit it.
	ExternalMessageID *string `json:"external_message_id,omitempty"`
}

// ArchiveStorePush is the body of POST /archive-store/v1/conversations/{id}: the
// conversation's metadata and its messages after AfterSeq through
// ThroughSeq, less the ones the archiver doesn't push.
type ArchiveStorePush struct {
	Conversation ArchiveStoreConversation `json:"conversation"`
	AfterSeq     int64                    `json:"after_seq"`
	ThroughSeq   int64                    `json:"through_seq"`
	Reset        bool                     `json:"reset"`
	Messages     []ArchiveStoreMessage    `json:"messages"`
}

// ArchiveStoreSource is who pushes: the source VM, attributed to its user.
type ArchiveStoreSource struct {
	SourceVM string
	UserID   string
	Email    string
}

// ErrArchiveStoreBehind is ReceiveArchiveStorePush's error when the store has less
// of the conversation than the push builds on.
var ErrArchiveStoreBehind = errors.New("archive store is behind the push")

func (p *ArchiveStorePush) validate(id string) error {
	if p.Conversation.ConversationID != id {
		return fmt.Errorf("%w: conversation_id %q doesn't match the path", ErrInvalid, p.Conversation.ConversationID)
	}
	if p.ThroughSeq < p.AfterSeq || (p.Reset && p.AfterSeq != 0) {
		return fmt.Errorf("%w: bad sequence range", ErrInvalid)
	}
	for _, m := range p.Messages {
		if m.SequenceID <= p.AfterSeq || m.SequenceID > p.ThroughSeq {
			return fmt.Errorf("%w: message %d outside (%d, %d]", ErrInvalid, m.SequenceID, p.AfterSeq, p.ThroughSeq)
		}
	}
	return nil
}

// ReceiveArchiveStorePush stores a source's push and returns the sequence id
// the store now covers. Messages it has are ignored, so pushes may repeat.
// It returns ErrArchiveStoreBehind, with what it has, if the push builds on
// messages the store doesn't have.
func (db *DB) ReceiveArchiveStorePush(ctx context.Context, m ArchiveStoreSource, id string, p ArchiveStorePush) (int64, error) {
	if err := p.validate(id); err != nil {
		return 0, err
	}
	return db.QueriesTxRes(ctx, func(q *generated.Queries) (int64, error) {
		if err := q.UpsertArchiveStoreSource(ctx, generated.UpsertArchiveStoreSourceParams{SourceVm: m.SourceVM, UserID: m.UserID, Email: m.Email}); err != nil {
			return 0, err
		}
		key := generated.GetArchiveStoreHaveSeqParams{SourceVm: m.SourceVM, ConversationID: id}
		have, err := q.GetArchiveStoreHaveSeq(ctx, key)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if p.Reset {
			have = 0
			if err := q.DeleteArchiveStoreMessages(ctx, generated.DeleteArchiveStoreMessagesParams(key)); err != nil {
				return 0, err
			}
		}
		if p.AfterSeq > have {
			return have, ErrArchiveStoreBehind
		}
		c := p.Conversation
		if err := q.UpsertArchiveStoreConversation(ctx, generated.UpsertArchiveStoreConversationParams{
			SourceVm: m.SourceVM, ConversationID: id, ParentConversationID: c.ParentConversationID,
			Slug: c.Slug, Project: c.Project, Model: c.Model, Cwd: c.Cwd, Archived: c.Archived,
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, HaveSeq: max(have, p.ThroughSeq),
		}); err != nil {
			return 0, err
		}
		for _, msg := range p.Messages {
			if err := q.InsertArchiveStoreMessage(ctx, generated.InsertArchiveStoreMessageParams{
				SourceVm: m.SourceVM, ConversationID: id, SequenceID: msg.SequenceID, MessageID: msg.MessageID,
				Type: msg.Type, LlmData: msg.LlmData, UserData: msg.UserData, UsageData: msg.UsageData,
				DisplayData: msg.DisplayData, CreatedAt: msg.CreatedAt, Generation: msg.Generation,
				ExcludedFromContext: msg.ExcludedFromContext, ModelName: msg.ModelName, UserEmail: msg.UserEmail,
				ExternalMessageID: msg.ExternalMessageID,
			}); err != nil {
				return 0, err
			}
		}
		return max(have, p.ThroughSeq), nil
	})
}

// ReceiveArchiveStoreProjects replaces a source's projects (a JSON array).
func (db *DB) ReceiveArchiveStoreProjects(ctx context.Context, m ArchiveStoreSource, projects string) error {
	return db.QueriesTx(ctx, func(q *generated.Queries) error {
		if err := q.UpsertArchiveStoreSource(ctx, generated.UpsertArchiveStoreSourceParams{SourceVm: m.SourceVM, UserID: m.UserID, Email: m.Email}); err != nil {
			return err
		}
		return q.SetArchiveStoreSourceProjects(ctx, generated.SetArchiveStoreSourceProjectsParams{Projects: projects, SourceVm: m.SourceVM})
	})
}

// DeleteArchiveStoreConversation deletes a source's conversation and its
// messages from the store.
func (db *DB) DeleteArchiveStoreConversation(ctx context.Context, m ArchiveStoreSource, id string) error {
	return db.QueriesTx(ctx, func(q *generated.Queries) error {
		if err := q.UpsertArchiveStoreSource(ctx, generated.UpsertArchiveStoreSourceParams{SourceVm: m.SourceVM, UserID: m.UserID, Email: m.Email}); err != nil {
			return err
		}
		if err := q.DeleteArchiveStoreMessages(ctx, generated.DeleteArchiveStoreMessagesParams{SourceVm: m.SourceVM, ConversationID: id}); err != nil {
			return err
		}
		return q.DeleteArchiveStoreConversation(ctx, generated.DeleteArchiveStoreConversationParams{SourceVm: m.SourceVM, ConversationID: id})
	})
}

// FTSMatch turns search input into an FTS5 MATCH expression: its fields,
// quoted and ANDed, the ones of three or more characters as prefixes.
func FTSMatch(query string) string {
	fields := strings.Fields(query)
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		part := `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
		if utf8.RuneCountInString(f) >= 3 {
			part += "*"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " AND ")
}

// SearchArchiveStore returns the best snippet of each conversation whose
// messages match query, keyed by source VM and conversation id.
func (db *DB) SearchArchiveStore(ctx context.Context, query string) (map[[2]string]string, error) {
	match := FTSMatch(query)
	rows, err := db.QueriesRes(ctx, func(q *generated.Queries) ([]generated.SearchArchiveStoreMessagesRow, error) {
		return q.SearchArchiveStoreMessages(ctx, generated.SearchArchiveStoreMessagesParams{
			MarkStart: SnippetMarkStart, MarkEnd: SnippetMarkEnd, FtsMatch: &match,
		})
	})
	if err != nil {
		return nil, err
	}
	hits := map[[2]string]string{}
	for _, r := range rows {
		key := [2]string{r.SourceVm, r.ConversationID}
		if _, ok := hits[key]; !ok {
			hits[key] = r.Snippet
		}
	}
	return hits, nil
}
