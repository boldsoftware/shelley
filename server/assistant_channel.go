package server

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
)

// errChannelEventNoID refuses a texted message without an id: replies to
// it could not be routed back.
var errChannelEventNoID = errors.New("channel event has no id")

// recoverAssistantChannelQueue drains the Assistant's queue at startup
// if it holds a texted message: the user may not open the UI to drain it.
func (s *Server) recoverAssistantChannelQueue(ctx context.Context) error {
	id := s.assistantID()
	if id == "" {
		return nil
	}
	queued, err := s.db.GetQueuedMessages(ctx, id)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(queued, func(qm db.QueuedMessage) bool { return qm.ExternalMessageID != "" }) {
		return nil
	}
	manager, err := s.getOrCreateConversationManager(ctx, id, "")
	if err != nil {
		return err
	}
	manager.drainQueueIfIdle(s)
	return nil
}

// assistantChat is the Assistant's claudetool.UserChat. It is bound to no
// chat: a message or reaction goes to a chat only when it answers a texted
// message, to the chat that message came from. Answers to a web chat's
// messages stay in the conversation, which the chat's page reads. Everything else (an
// answer to a message typed in the UI, to a conversation's or subagent's report, or
// to a scheduled wakeup) stays in the UI.
type assistantChat struct {
	s              *Server
	conversationID string
}

// route returns the route of the texted message that a message replying to
// target answers, or nil if it answers none. With no target, it answers the
// latest user message, typed or not.
func (c assistantChat) route(ctx context.Context, target claudetool.UserMessage) (*db.ChannelRoute, error) {
	id := target.ExternalID
	if target.SequenceID == 0 {
		var err error
		if id, err = c.s.db.LatestUserExternalMessageID(ctx, c.conversationID); err != nil {
			return nil, err
		}
	}
	if id == "" {
		return nil, nil
	}
	return c.s.db.GetChannelRoute(ctx, c.conversationID, id)
}

func (c assistantChat) Send(ctx context.Context, text string, attachments []claudetool.MessageUserAttachment, target claudetool.UserMessage) error {
	route, err := c.route(ctx, target)
	if err != nil {
		return fmt.Errorf("nothing was sent: %w", err)
	}
	if route == nil || route.Endpoint == "" {
		return nil
	}
	return c.s.sendChannelChat(ctx, route.Endpoint, text, attachments, target)
}

func (c assistantChat) React(ctx context.Context, target claudetool.UserMessage, emoji string) error {
	if target.ExternalID == "" {
		return nil
	}
	route, err := c.route(ctx, target)
	if err != nil {
		return fmt.Errorf("nothing was sent: %w", err)
	}
	if route == nil || route.Endpoint == "" {
		return nil
	}
	return c.s.reactChannelChat(ctx, route.Endpoint, target, emoji)
}

// Description implements claudetool.UserChatDescriber.
func (assistantChat) Description() string {
	return assistantChatDescription
}

const assistantChatDescription = `Messages wrapped in external_message were texted from the user's phone (iMessage, RCS, or SMS). What answers one is texted back to its chat automatically: a message or reaction whose reply_to is its sequence_id, or one without reply_to while it is the latest message. Everything else stays in Shelley's UI. Keep texted replies short and few: after 5 without a reply, the chat takes no more until the user writes. Attach local images or files by path; texted, they are uploaded and sent with your text as one message.`
