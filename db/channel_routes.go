package db

import (
	"context"
	"database/sql"
	"errors"

	"shelley.exe.dev/db/generated"
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
		return generated.New(tx.Conn()).CreateChannelRoute(ctx, generated.CreateChannelRouteParams{
			ConversationID: route.ConversationID, ExternalMessageID: route.ExternalMessageID, ChatID: route.ChatID, Endpoint: route.Endpoint,
		})
	})
}

func newChannelRoute(row generated.ChannelMessageRoute) *ChannelRoute {
	return &ChannelRoute{ConversationID: row.ConversationID, ExternalMessageID: row.ExternalMessageID, ChatID: row.ChatID, Endpoint: row.Endpoint}
}

// GetChannelRoute returns the route of the conversation's message with the
// channel id externalMessageID.
func (db *DB) GetChannelRoute(ctx context.Context, conversationID, externalMessageID string) (*ChannelRoute, error) {
	var route *ChannelRoute
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		row, err := generated.New(rx.Conn()).GetChannelRoute(ctx, generated.GetChannelRouteParams{
			ConversationID: conversationID, ExternalMessageID: externalMessageID,
		})
		route = newChannelRoute(row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return route, nil
}

// LatestChannelRoute returns the route of chat chatID's latest message.
func (db *DB) LatestChannelRoute(ctx context.Context, chatID string) (*ChannelRoute, error) {
	var route *ChannelRoute
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		row, err := generated.New(rx.Conn()).LatestChannelRoute(ctx, chatID)
		route = newChannelRoute(row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return route, nil
}

// HasChannelRouteEndpoint reports whether a message of chat chatID came
// with endpoint.
func (db *DB) HasChannelRouteEndpoint(ctx context.Context, chatID, endpoint string) (bool, error) {
	var ok bool
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		ok, err = generated.New(rx.Conn()).HasChannelRouteEndpoint(ctx, generated.HasChannelRouteEndpointParams{ChatID: chatID, Endpoint: endpoint})
		return err
	})
	return ok, err
}

// LatestUserExternalMessageID returns the channel id of the conversation's
// latest user message, or "" if it did not come from a channel. Any user
// message counts, typed or a machine notice (a subagent's report, a
// finished job), except tool results and the copies a compaction carries
// forward: it is what the agent is answering.
func (db *DB) LatestUserExternalMessageID(ctx context.Context, conversationID string) (string, error) {
	var id *string
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		id, err = generated.New(rx.Conn()).LatestUserExternalMessageID(ctx, conversationID)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil || id == nil {
		return "", err
	}
	return *id, nil
}
