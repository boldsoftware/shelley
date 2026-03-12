package slack

import (
	"context"
	"fmt"

	"github.com/slack-go/slack"

	"shelley.exe.dev/claudetool"
)

// Compile-time check that Bot implements claudetool.SlackAPI.
var _ claudetool.SlackAPI = (*Bot)(nil)

// GetHistory returns recent messages from a channel.
func (b *Bot) GetHistory(ctx context.Context, channel string, limit int) ([]claudetool.SlackMessage, error) {
	resp, err := b.api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
		ChannelID: channel,
		Limit:     limit,
	})
	if err != nil {
		return nil, fmt.Errorf("get conversation history: %w", err)
	}
	msgs := make([]claudetool.SlackMessage, len(resp.Messages))
	for i, m := range resp.Messages {
		msgs[i] = claudetool.SlackMessage{
			User:      m.User,
			Text:      m.Text,
			Timestamp: m.Timestamp,
			ThreadTS:  m.ThreadTimestamp,
		}
	}
	return msgs, nil
}

// GetThread returns messages in a thread.
func (b *Bot) GetThread(ctx context.Context, channel, threadTS string, limit int) ([]claudetool.SlackMessage, error) {
	replies, _, _, err := b.api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channel,
		Timestamp: threadTS,
		Limit:     limit,
	})
	if err != nil {
		return nil, fmt.Errorf("get conversation replies: %w", err)
	}
	msgs := make([]claudetool.SlackMessage, len(replies))
	for i, m := range replies {
		msgs[i] = claudetool.SlackMessage{
			User:      m.User,
			Text:      m.Text,
			Timestamp: m.Timestamp,
			ThreadTS:  m.ThreadTimestamp,
		}
	}
	return msgs, nil
}

// ListChannels returns channels the bot is a member of.
func (b *Bot) ListChannels(ctx context.Context) ([]claudetool.SlackChannel, error) {
	var all []claudetool.SlackChannel
	cursor := ""
	for {
		channels, nextCursor, err := b.api.GetConversationsContext(ctx, &slack.GetConversationsParameters{
			Cursor:          cursor,
			ExcludeArchived: true,
			Limit:           200,
			Types:           []string{"public_channel", "private_channel"},
		})
		if err != nil {
			return nil, fmt.Errorf("get conversations: %w", err)
		}
		for _, ch := range channels {
			if ch.IsMember {
				all = append(all, claudetool.SlackChannel{ID: ch.ID, Name: ch.Name})
			}
		}
		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}
	return all, nil
}

// AddReaction adds an emoji reaction to a message.
func (b *Bot) AddReaction(ctx context.Context, channel, timestamp, emoji string) error {
	return b.api.AddReactionContext(ctx, emoji, slack.NewRefToMessage(channel, timestamp))
}
