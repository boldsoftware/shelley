// Package slack integrates Shelley with Slack via Socket Mode.
//
// When a user @-mentions the bot, a new Shelley conversation is created and the
// bot replies in a Slack thread. Subsequent messages in that thread are forwarded
// to the same Shelley conversation. Shelley's responses are posted back to the thread.
package slack

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// ConversationAPI is the interface the Slack bot needs from the Shelley server.
// This avoids importing the server package directly.
type ConversationAPI interface {
	// NewConversation creates a new conversation and sends the first message.
	// Returns the conversation ID.
	NewConversation(ctx context.Context, message, model string) (conversationID string, err error)

	// SendMessage sends a message to an existing conversation.
	SendMessage(ctx context.Context, conversationID, message, model string) error

	// GetLatestAgentResponse returns the message ID and text of the latest agent message.
	GetLatestAgentResponse(conversationID string) (messageID string, text string)
}

// Bot is a Slack bot that bridges Slack threads to Shelley conversations.
type Bot struct {
	api    *slack.Client
	socket *socketmode.Client
	convo  ConversationAPI
	logger *slog.Logger
	model  string // default model to use
	botUID string // the bot's own Slack user ID

	// thread mapping: slack "channel:thread_ts" -> shelley conversation ID
	mu      sync.RWMutex
	threads map[string]string // key: "C123:1234567890.123456" -> conversation_id
	reverse map[string]string // key: conversation_id -> "C123:1234567890.123456"

	// dedup: last message ID we posted per conversation to avoid re-posting
	lastPosted map[string]string // key: conversation_id -> message_id

	// channel name -> ID cache to avoid repeated conversations.list calls
	channelCache map[string]string
}

// Config holds configuration for the Slack bot.
type Config struct {
	BotToken string // xoxb-...
	AppToken string // xapp-...
	Model    string // default LLM model
	Convo    ConversationAPI
	Logger   *slog.Logger
}

// NewBot creates a new Slack bot.
func NewBot(cfg Config) (*Bot, error) {
	if cfg.BotToken == "" {
		return nil, fmt.Errorf("slack bot token is required")
	}
	if cfg.AppToken == "" {
		return nil, fmt.Errorf("slack app token is required")
	}
	if cfg.Convo == nil {
		return nil, fmt.Errorf("conversation API is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	api := slack.New(cfg.BotToken, slack.OptionAppLevelToken(cfg.AppToken))
	socket := socketmode.New(api)

	return &Bot{
		api:        api,
		socket:     socket,
		convo:      cfg.Convo,
		logger:     cfg.Logger.With("component", "slack"),
		model:      cfg.Model,
		threads:      make(map[string]string),
		reverse:      make(map[string]string),
		lastPosted:   make(map[string]string),
		channelCache: make(map[string]string),
	}, nil
}

// Run starts the bot. It blocks until the context is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	// Get our own bot user ID so we can strip @mentions
	authResp, err := b.api.AuthTestContext(ctx)
	if err != nil {
		return fmt.Errorf("slack auth test failed: %w", err)
	}
	b.botUID = authResp.UserID
	b.logger.Info("slack bot authenticated", "user_id", b.botUID, "team", authResp.Team)

	go b.handleEvents(ctx)

	return b.socket.RunContext(ctx)
}

// OnAgentDone is called by the server when any conversation's agent finishes a turn.
// It checks whether the conversation belongs to a Slack thread and posts the response.
func (b *Bot) OnAgentDone(conversationID string) {
	b.mu.RLock()
	key, ok := b.reverse[conversationID]
	b.mu.RUnlock()
	if !ok {
		return
	}

	msgID, response := b.convo.GetLatestAgentResponse(conversationID)
	if response == "" {
		return
	}

	// Dedup: don't re-post the same message
	b.mu.Lock()
	if b.lastPosted[conversationID] == msgID {
		b.mu.Unlock()
		return
	}
	b.lastPosted[conversationID] = msgID
	b.mu.Unlock()

	parts := strings.SplitN(key, ":", 2)
	channel, threadTS := parts[0], parts[1]
	if err := b.PostMessage(channel, threadTS, response); err != nil {
		b.logger.Error("failed to post agent response", "error", err, "channel", channel)
	}
}

func (b *Bot) handleEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-b.socket.Events:
			if !ok {
				return
			}
			b.handleEvent(ctx, evt)
		}
	}
}

func (b *Bot) handleEvent(ctx context.Context, evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		data, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		b.socket.Ack(*evt.Request)

		switch inner := data.InnerEvent.Data.(type) {
		case *slackevents.AppMentionEvent:
			go b.handleMention(ctx, inner)
		case *slackevents.MessageEvent:
			go b.handleThreadMessage(ctx, inner)
		}

	case socketmode.EventTypeConnecting:
		b.logger.Info("slack connecting...")
	case socketmode.EventTypeConnected:
		b.logger.Info("slack connected")
	case socketmode.EventTypeConnectionError:
		b.logger.Error("slack connection error")
	}
}

// threadKey returns the map key for a Slack thread.
func threadKey(channel, threadTS string) string {
	return channel + ":" + threadTS
}

// stripMention removes the @bot mention from the message text.
func (b *Bot) stripMention(text string) string {
	// Slack formats mentions as <@U12345>
	mention := "<@" + b.botUID + ">"
	text = strings.ReplaceAll(text, mention, "")
	return strings.TrimSpace(text)
}

// handleMention handles an @-mention of the bot. This starts a new conversation.
func (b *Bot) handleMention(ctx context.Context, event *slackevents.AppMentionEvent) {
	text := b.stripMention(event.Text)
	if text == "" {
		return
	}

	// The thread_ts for the new thread is the message's own timestamp
	// (app_mention events in a thread have ThreadTimeStamp set; top-level ones don't)
	threadTS := event.ThreadTimeStamp
	if threadTS == "" {
		threadTS = event.TimeStamp
	}

	channel := event.Channel
	key := threadKey(channel, threadTS)

	// Check if this thread already has a conversation
	b.mu.RLock()
	convID, exists := b.threads[key]
	b.mu.RUnlock()

	if exists {
		// Thread already mapped — send as a follow-up message
		b.sendToConversation(ctx, convID, channel, threadTS, text)
		return
	}

	// Create a new Shelley conversation
	slackContext := fmt.Sprintf("[Slack message from <@%s> in <#%s>]\n\n%s", event.User, channel, text)
	convID, err := b.convo.NewConversation(ctx, slackContext, b.model)
	if err != nil {
		b.logger.Error("failed to create conversation", "error", err, "channel", channel)
		if err2 := b.PostMessage(channel, threadTS, "Sorry, I couldn't start a conversation. Please try again."); err2 != nil {
			b.logger.Error("failed to post error message", "error", err2, "channel", channel)
		}
		return
	}

	b.logger.Info("new conversation from slack", "conversation_id", convID, "channel", channel, "thread_ts", threadTS)

	// Map thread <-> conversation
	b.mu.Lock()
	b.threads[key] = convID
	b.reverse[convID] = key
	b.mu.Unlock()
}

// handleThreadMessage handles a message in a thread that the bot is watching.
func (b *Bot) handleThreadMessage(ctx context.Context, event *slackevents.MessageEvent) {
	// Ignore messages from bots (including ourselves)
	if event.BotID != "" || event.User == b.botUID {
		return
	}

	// Only handle threaded messages
	if event.ThreadTimeStamp == "" {
		return
	}

	// Ignore message subtypes (edits, deletes, etc.)
	if event.SubType != "" {
		return
	}

	key := threadKey(event.Channel, event.ThreadTimeStamp)

	b.mu.RLock()
	convID, exists := b.threads[key]
	b.mu.RUnlock()

	if !exists {
		return // Not a thread we're tracking
	}

	text := b.stripMention(event.Text)
	if text == "" {
		return
	}

	b.sendToConversation(ctx, convID, event.Channel, event.ThreadTimeStamp, text)
}

// sendToConversation sends a user message to an existing Shelley conversation.
func (b *Bot) sendToConversation(ctx context.Context, convID, channel, threadTS, text string) {
	if err := b.convo.SendMessage(ctx, convID, text, b.model); err != nil {
		b.logger.Error("failed to send message", "error", err, "conversation_id", convID)
		if err2 := b.PostMessage(channel, threadTS, "Sorry, I couldn't process your message. Please try again."); err2 != nil {
			b.logger.Error("failed to post error message", "error", err2, "channel", channel)
		}
	}
}

const slackMaxMessageLength = 3900 // leave room for formatting; actual limit is 4000

// PostMessage sends a message to a Slack channel/thread, chunking if necessary.
func (b *Bot) PostMessage(channel, threadTS, text string) error {
	chunks := chunkText(text, slackMaxMessageLength)
	for _, chunk := range chunks {
		opts := []slack.MsgOption{
			slack.MsgOptionText(chunk, false),
		}
		if threadTS != "" {
			opts = append(opts, slack.MsgOptionTS(threadTS))
		}
		_, _, err := b.api.PostMessage(channel, opts...)
		if err != nil {
			return fmt.Errorf("post to %s: %w", channel, err)
		}
	}
	return nil
}

// chunkText splits text into chunks of at most maxLen bytes,
// preferring to break at newlines.
func chunkText(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}

		// Try to find a newline to break at
		cut := maxLen
		if idx := strings.LastIndex(text[:maxLen], "\n"); idx > maxLen/2 {
			cut = idx + 1
		}

		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}

	return chunks
}
