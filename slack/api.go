package slack

import (
	"context"
	"fmt"
	"strings"

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
// Results are cached in the channel name→ID cache for ResolveChannel.
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

	// Populate channel name→ID cache.
	b.mu.Lock()
	for _, ch := range all {
		b.channelCache[strings.ToLower(ch.Name)] = ch.ID
	}
	b.mu.Unlock()

	return all, nil
}

// ResolveChannel resolves a channel name (e.g. "#general" or "general") to a channel ID.
// If the input already looks like a channel ID (starts with C/G/D), it is returned as-is.
// Results are cached to avoid repeated conversations.list API calls.
func (b *Bot) ResolveChannel(ctx context.Context, nameOrID string) (string, error) {
	nameOrID = strings.TrimPrefix(nameOrID, "#")
	// Already a channel ID
	if len(nameOrID) > 0 && (nameOrID[0] == 'C' || nameOrID[0] == 'G' || nameOrID[0] == 'D') && !strings.ContainsAny(nameOrID, " -") {
		return nameOrID, nil
	}
	key := strings.ToLower(nameOrID)

	// Check cache first.
	b.mu.RLock()
	if id, ok := b.channelCache[key]; ok {
		b.mu.RUnlock()
		return id, nil
	}
	b.mu.RUnlock()

	// Cache miss — fetch from API (ListChannels populates the cache).
	if _, err := b.ListChannels(ctx); err != nil {
		return "", fmt.Errorf("list channels: %w", err)
	}

	b.mu.RLock()
	id, ok := b.channelCache[key]
	b.mu.RUnlock()
	if ok {
		return id, nil
	}
	return "", fmt.Errorf("channel %q not found (bot may not be a member)", nameOrID)
}

// AddReaction adds an emoji reaction to a message.
func (b *Bot) AddReaction(ctx context.Context, channel, timestamp, emoji string) error {
	return b.api.AddReactionContext(ctx, emoji, slack.NewRefToMessage(channel, timestamp))
}
