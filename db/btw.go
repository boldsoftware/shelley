package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

const BtwReaderKind = "btw_reader"

type BtwReaderIdentity struct {
	ConversationID       string           `json:"conversation_id"`
	ParentConversationID string           `json:"parent_conversation_id"`
	ParentPointer        BtwParentPointer `json:"parent_pointer"`
}

// ManagedBtwReaderIdentity validates all metadata that makes a conversation a
// managed BTW reader. Callers must not key behavior off Kind alone.
func ManagedBtwReaderIdentity(conversation generated.Conversation) (BtwReaderIdentity, bool) {
	options := ParseConversationOptions(conversation.ConversationOptions)
	if conversation.ParentConversationID == nil || conversation.UserInitiated ||
		options.Kind != BtwReaderKind || options.ParentPointer == nil || !options.ParentPointer.valid() {
		return BtwReaderIdentity{}, false
	}
	return BtwReaderIdentity{
		ConversationID:       conversation.ConversationID,
		ParentConversationID: *conversation.ParentConversationID,
		ParentPointer:        *options.ParentPointer,
	}, true
}

func (pointer BtwParentPointer) valid() bool {
	return pointer.Generation >= 1 && pointer.SequenceID >= 0
}

func scrubManagedBtwOptions(raw string) (string, bool, error) {
	var options map[string]json.RawMessage
	if raw == "" || json.Unmarshal([]byte(raw), &options) != nil {
		return raw, false, nil
	}
	_, hasKind := options["kind"]
	_, hasPointer := options["parent_pointer"]
	if !hasKind && !hasPointer {
		return raw, false, nil
	}
	delete(options, "kind")
	delete(options, "parent_pointer")
	scrubbed, err := json.Marshal(options)
	return string(scrubbed), true, err
}

type CreateBtwReaderConversationParams struct {
	ConversationID string
	SlugBase       string
	ParentID       string
	Cwd            *string
	Model          *string
	ParentPointer  BtwParentPointer
	ThinkingLevel  string
	SystemMessage  llm.Message
	UserMessage    llm.Message
	UserData       any
}

// CreateBtwReaderConversation atomically creates a working, managed child and
// its initial system and user rows. Parent text is referenced only by metadata.
func (db *DB) CreateBtwReaderConversation(ctx context.Context, params CreateBtwReaderConversationParams) (*generated.Conversation, error) {
	if !params.ParentPointer.valid() {
		return nil, fmt.Errorf("invalid BTW metadata")
	}
	conversationID := params.ConversationID
	if conversationID == "" {
		var err error
		conversationID, err = GenerateConversationID()
		if err != nil {
			return nil, err
		}
	}
	options, err := json.Marshal(ConversationOptions{
		Kind:          BtwReaderKind,
		ParentPointer: &params.ParentPointer,
		ThinkingLevel: params.ThinkingLevel,
	})
	if err != nil {
		return nil, err
	}
	slug := params.SlugBase + "-" + strings.ToLower(conversationID)

	var conversation generated.Conversation
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		conversation, err = q.CreateSubagentConversation(ctx, generated.CreateSubagentConversationParams{
			ConversationID: conversationID, Slug: &slug, Cwd: params.Cwd, ParentConversationID: &params.ParentID,
		})
		if err != nil {
			return err
		}
		if err := q.UpdateConversationOptions(ctx, generated.UpdateConversationOptionsParams{
			ConversationID: conversationID, ConversationOptions: string(options),
		}); err != nil {
			return err
		}
		if params.Model != nil && *params.Model != "" {
			if err := q.UpdateConversationModel(ctx, generated.UpdateConversationModelParams{
				ConversationID: conversationID, Model: params.Model,
			}); err != nil {
				return err
			}
		}
		for _, message := range []CreateMessageParams{
			{ConversationID: conversationID, Type: MessageTypeSystem, LLMData: params.SystemMessage},
			{
				ConversationID: conversationID, Type: MessageTypeUser, LLMData: params.UserMessage,
				UserData: params.UserData, MarkAgentStart: true, BumpTimestamp: true,
			},
		} {
			if _, err := insertMessageTx(ctx, q, message); err != nil {
				return err
			}
		}
		conversation, err = q.GetConversation(ctx, conversationID)
		return err
	})
	return &conversation, err
}

// GetBtwParentSnapshot returns the parent row and the immutable pointer for a
// new reader from one database snapshot.
func (db *DB) GetBtwParentSnapshot(ctx context.Context, parentID string) (generated.Conversation, BtwParentPointer, error) {
	var parent generated.Conversation
	var pointer BtwParentPointer
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		parent, err = q.GetConversation(ctx, parentID)
		if err != nil {
			return err
		}
		pointer.Generation = parent.CurrentGeneration
		return rx.QueryRow(
			"SELECT COALESCE(MAX(sequence_id), 0) FROM messages WHERE conversation_id = ? AND generation = ?",
			parentID, pointer.Generation,
		).Scan(&pointer.SequenceID)
	})
	return parent, pointer, err
}

// ListBtwReaders returns exactly the managed BTW children owned by parentID.
func (db *DB) ListBtwReaders(ctx context.Context, parentID string) ([]BtwReaderIdentity, error) {
	readers := make([]BtwReaderIdentity, 0)
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		children, err := generated.New(rx.Conn()).GetSubagents(ctx, &parentID)
		if err != nil {
			return err
		}
		for _, child := range children {
			if identity, ok := ManagedBtwReaderIdentity(child); ok {
				readers = append(readers, identity)
			}
		}
		return nil
	})
	return readers, err
}

// DismissBtwReader removes managed BTW metadata from a child owned by parentID.
// The conversation, its parent relationship, and all messages remain intact.
func (db *DB) DismissBtwReader(ctx context.Context, parentID, childID string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		child, err := q.GetConversation(ctx, childID)
		if err != nil {
			return err
		}
		identity, ok := ManagedBtwReaderIdentity(child)
		if !ok || identity.ParentConversationID != parentID {
			return fmt.Errorf("not an owned BTW reader")
		}
		options, _, err := scrubManagedBtwOptions(child.ConversationOptions)
		if err != nil {
			return err
		}
		return q.UpdateConversationOptions(ctx, generated.UpdateConversationOptionsParams{
			ConversationID:      childID,
			ConversationOptions: options,
		})
	})
}

// ListFrozenParentMessages returns the exact context-visible parent prefix
// selected by conversation, generation, and inclusive sequence boundary.
func (db *DB) ListFrozenParentMessages(ctx context.Context, conversationID string, pointer BtwParentPointer) ([]generated.Message, error) {
	var messages []generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := rx.Query(
			`SELECT message_id, conversation_id, sequence_id, type, llm_data, user_data, usage_data,
				created_at, display_data, excluded_from_context, generation, llm_api_url, model_name,
				forked_from_message_id, user_email, other_usage_data, external_message_id
			FROM messages
			WHERE conversation_id = ? AND generation = ? AND sequence_id <= ? AND excluded_from_context = FALSE
			ORDER BY sequence_id ASC`,
			conversationID, pointer.Generation, pointer.SequenceID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m generated.Message
			if err := rows.Scan(
				&m.MessageID, &m.ConversationID, &m.SequenceID, &m.Type, &m.LlmData, &m.UserData,
				&m.UsageData, &m.CreatedAt, &m.DisplayData, &m.ExcludedFromContext, &m.Generation,
				&m.LlmApiUrl, &m.ModelName, &m.ForkedFromMessageID, &m.UserEmail, &m.OtherUsageData,
				&m.ExternalMessageID,
			); err != nil {
				return err
			}
			messages = append(messages, m)
		}
		return rows.Err()
	})
	return messages, err
}

type btwDeletionChildren struct {
	readers []string
}

// legacyCommitTourKind marks the hidden workers that Shelleys with commit
// tours created under the conversation that requested a tour. Nothing creates
// them any more, but existing ones still belong to that conversation.
const legacyCommitTourKind = "commit-tour"

func isLegacyCommitTourWorker(conversation generated.Conversation) bool {
	return conversation.ParentConversationID != nil && !conversation.UserInitiated &&
		ParseConversationOptions(conversation.ConversationOptions).Kind == legacyCommitTourKind
}

func classifyBtwDeletionChildren(children []generated.Conversation) btwDeletionChildren {
	var result btwDeletionChildren
	for _, child := range children {
		if _, ok := ManagedBtwReaderIdentity(child); ok || isLegacyCommitTourWorker(child) {
			result.readers = append(result.readers, child.ConversationID)
		}
	}
	return result
}

func loadBtwDeletionChildren(ctx context.Context, q *generated.Queries, conversationID string) (btwDeletionChildren, bool, error) {
	if _, err := q.GetConversation(ctx, conversationID); errors.Is(err, sql.ErrNoRows) {
		return btwDeletionChildren{}, false, nil
	} else if err != nil {
		return btwDeletionChildren{}, false, err
	}
	children, err := q.GetSubagents(ctx, &conversationID)
	if err != nil {
		return btwDeletionChildren{}, false, err
	}
	return classifyBtwDeletionChildren(children), true, nil
}

// PlanConversationDeletion returns the direct managed detached children (BTW
// readers and legacy commit-tour workers) owned by conversationID. Every other
// child remains attached so the generic foreign-key deletion behavior is
// unchanged.
func (db *DB) PlanConversationDeletion(ctx context.Context, conversationID string) ([]string, error) {
	var readers []string
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		children, _, err := loadBtwDeletionChildren(ctx, generated.New(rx.Conn()), conversationID)
		if err != nil {
			return err
		}
		readers = children.readers
		return nil
	})
	return readers, err
}

// DeleteConversationWithBtwReaders atomically deletes direct managed detached
// children and conversationID. Any other child remains attached, so the parent
// delete fails and rolls the reader deletions back exactly like generic
// conversation deletion.
func (db *DB) DeleteConversationWithBtwReaders(ctx context.Context, conversationID string) ([]string, error) {
	var readers []string
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		children, exists, err := loadBtwDeletionChildren(ctx, q, conversationID)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		deleteConversation := func(id string) error {
			if err := q.DeleteConversationMessages(ctx, id); err != nil {
				return err
			}
			return q.DeleteConversation(ctx, id)
		}
		readers = children.readers
		for _, childID := range readers {
			if err := deleteConversation(childID); err != nil {
				return err
			}
		}
		return deleteConversation(conversationID)
	})
	return readers, err
}
