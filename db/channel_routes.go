package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"shelley.exe.dev/llm"
)

// ChannelRoute is where a texted message delivered to a conversation that
// is not bound to its chat (the Assistant) came from: replies to it go
// to Endpoint.
type ChannelRoute struct {
	ConversationID    string
	ExternalMessageID string
	ChatID            string
	Endpoint          string
}

// RecordChannelRoute records route. Recording it again, as a retried
// delivery does, keeps the first.
func (db *DB) RecordChannelRoute(ctx context.Context, route ChannelRoute) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO channel_message_routes(conversation_id,external_message_id,chat_id,endpoint)
			VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, route.ConversationID, route.ExternalMessageID, route.ChatID, route.Endpoint)
		return err
	})
}

// GetChannelRoute returns the route of the conversation's message with the
// channel id externalMessageID.
func (db *DB) GetChannelRoute(ctx context.Context, conversationID, externalMessageID string) (*ChannelRoute, error) {
	route := ChannelRoute{ConversationID: conversationID, ExternalMessageID: externalMessageID}
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow(`SELECT chat_id,endpoint FROM channel_message_routes WHERE conversation_id=? AND external_message_id=?`,
			conversationID, externalMessageID).Scan(&route.ChatID, &route.Endpoint)
	})
	if err != nil {
		return nil, err
	}
	return &route, nil
}

// LatestChannelRoute returns the route of chat chatID's latest message.
func (db *DB) LatestChannelRoute(ctx context.Context, chatID string) (*ChannelRoute, error) {
	route := ChannelRoute{ChatID: chatID}
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow(`SELECT conversation_id,external_message_id,endpoint FROM channel_message_routes
			WHERE chat_id=? ORDER BY created_at DESC, rowid DESC LIMIT 1`, chatID).Scan(&route.ConversationID, &route.ExternalMessageID, &route.Endpoint)
	})
	if err != nil {
		return nil, err
	}
	return &route, nil
}

// HasChannelRouteEndpoint reports whether a message of chat chatID came
// with endpoint.
func (db *DB) HasChannelRouteEndpoint(ctx context.Context, chatID, endpoint string) (bool, error) {
	var ok bool
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow(`SELECT EXISTS(SELECT 1 FROM channel_message_routes WHERE chat_id=? AND endpoint=?)`, chatID, endpoint).Scan(&ok)
	})
	return ok, err
}

// notToolResult keeps the messages m that carry no tool result.
var notToolResult = fmt.Sprintf("NOT EXISTS (SELECT 1 FROM json_each(m.llm_data,'$.Content') c WHERE json_extract(c.value,'$.Type')=%d)",
	int(llm.ContentTypeToolResult))

// LatestUserExternalMessageID returns the channel id of the conversation's
// latest user message, or "" if it did not come from a channel. Any user
// message counts, typed or a machine notice (a subagent's report, a
// finished job), except tool results and the copies a compaction carries
// forward: it is what the agent is answering.
func (db *DB) LatestUserExternalMessageID(ctx context.Context, conversationID string) (string, error) {
	var id sql.NullString
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow(`SELECT external_message_id FROM messages m
			WHERE conversation_id=? AND type='user'
			  AND NOT (json_valid(user_data) AND json_type(user_data,'$.compaction_carried') IS NOT NULL)
			  AND `+notToolResult+`
			ORDER BY sequence_id DESC LIMIT 1`, conversationID).Scan(&id)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id.String, err
}
