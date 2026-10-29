package server

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

// With a workspace, the workspace Assistant takes every texted
// message, from any chat, instead of a conversation per chat. Each texted
// message records its chat and endpoint in channel_message_routes, and the
// Assistant's message_user texts only what answers a texted message, to
// the chat it came from (see assistantChat). Web chats keep their own
// conversations: their page reads the chat's conversation stream, which
// must not be the Assistant's. A texted chat's own conversation, if it has
// one, gets no new messages.
//
// The event's SystemPrompt, FirstReaction, and FirstReply configure and
// greet a chat's new conversation; the Assistant is neither new nor the
// chat's, and has its own profile, so they are ignored.

// errChannelEventNoID refuses a texted message without an id: replies to
// it could not be routed back.
var errChannelEventNoID = errors.New("channel event has no id")

// assistantChannelConversation returns the workspace Assistant's conversation if
// ev goes to it, or nil if it goes to its chat's conversation: a web chat's,
// or any without a workspace.
func (s *Server) assistantChannelConversation(ctx context.Context, ev channelEvent) (conv *generated.Conversation, assistant bool, err error) {
	if ev.Web {
		return nil, false, nil
	}
	id := s.assistantID()
	if id == "" {
		return nil, false, nil
	}
	if ev.ID == "" {
		return nil, false, errChannelEventNoID
	}
	c, err := s.db.GetConversationByID(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return c, true, nil
}

// recoverAssistantChannelQueue drains the workspace Assistant's queue at startup
// if it holds a texted message, as recoverChannelQueues does for chats'
// conversations: the user may not open the UI to drain it.
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

// assistantChat is the workspace Assistant's claudetool.UserChat. It is bound to
// no chat: a message or reaction goes to a chat only when it answers a
// texted message, to the chat that message came from. Everything else (an
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
	if route == nil {
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
	return c.s.reactChannelChat(ctx, route.Endpoint, target, emoji)
}

// Description implements claudetool.UserChatDescriber.
func (assistantChat) Description() string {
	return assistantChatDescription
}

const assistantChatDescription = `Messages wrapped in external_message were texted from the user's phone (iMessage, RCS, or SMS). What answers one is texted back to its chat automatically: a message or reaction whose reply_to is its sequence_id, or one without reply_to while it is the latest message. Everything else stays in Shelley's UI. Keep texted replies short and few: after 5 without a reply, the chat takes no more until the user writes. Attach local images or files by path; texted, they are uploaded and sent with your text as one message.`
