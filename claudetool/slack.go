package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"shelley.exe.dev/llm"
)

// SlackAPI is the interface the Slack tool needs. Implemented by the slack.Bot.
type SlackAPI interface {
	PostMessage(channel, threadTS, text string)
	GetHistory(ctx context.Context, channel string, limit int) ([]SlackMessage, error)
	GetThread(ctx context.Context, channel, threadTS string, limit int) ([]SlackMessage, error)
	ListChannels(ctx context.Context) ([]SlackChannel, error)
	AddReaction(ctx context.Context, channel, timestamp, emoji string) error
}

// SlackMessage is a simplified Slack message for tool output.
type SlackMessage struct {
	User      string `json:"user"`
	Text      string `json:"text"`
	Timestamp string `json:"ts"`
	ThreadTS  string `json:"thread_ts,omitempty"`
}

// SlackChannel is a simplified Slack channel for tool output.
type SlackChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SlackTool provides Slack integration tools for Claude.
type SlackTool struct {
	API SlackAPI
}

const (
	slackToolName        = "slack"
	slackToolDescription = `Interact with Slack. Use the "action" field to select an operation:

- action: "send_message"
  Send a message to a channel or thread.
  Parameters: channel (string, required), text (string, required), thread_ts (string, optional)

- action: "get_history"
  Get recent messages from a channel.
  Parameters: channel (string, required), limit (integer, optional, default 20)

- action: "get_thread"
  Get messages in a thread.
  Parameters: channel (string, required), thread_ts (string, required), limit (integer, optional, default 50)

- action: "list_channels"
  List channels the bot is a member of.
  No additional parameters.

- action: "add_reaction"
  Add an emoji reaction to a message.
  Parameters: channel (string, required), timestamp (string, required), emoji (string, required)`

	slackToolInputSchema = `{
  "type": "object",
  "required": ["action"],
  "properties": {
    "action": {
      "type": "string",
      "enum": ["send_message", "get_history", "get_thread", "list_channels", "add_reaction"],
      "description": "The Slack action to perform"
    },
    "channel": {
      "type": "string",
      "description": "Channel ID (e.g. C01234567)"
    },
    "text": {
      "type": "string",
      "description": "Message text to send"
    },
    "thread_ts": {
      "type": "string",
      "description": "Thread timestamp for threading"
    },
    "timestamp": {
      "type": "string",
      "description": "Message timestamp (for reactions)"
    },
    "emoji": {
      "type": "string",
      "description": "Emoji name without colons (e.g. thumbsup)"
    },
    "limit": {
      "type": "integer",
      "description": "Max number of messages to return"
    }
  }
}`
)

type slackInput struct {
	Action   string `json:"action"`
	Channel  string `json:"channel"`
	Text     string `json:"text"`
	ThreadTS string `json:"thread_ts"`
	Timestamp string `json:"timestamp"`
	Emoji    string `json:"emoji"`
	Limit    int    `json:"limit"`
}

// Tool returns an llm.Tool for Slack interactions.
func (s *SlackTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        slackToolName,
		Description: slackToolDescription,
		InputSchema: llm.MustSchema(slackToolInputSchema),
		Run:         s.Run,
	}
}

func (s *SlackTool) Run(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var req slackInput
	if err := json.Unmarshal(m, &req); err != nil {
		return llm.ErrorfToolOut("failed to parse slack input: %w", err)
	}

	switch req.Action {
	case "send_message":
		return s.sendMessage(req)
	case "get_history":
		return s.getHistory(ctx, req)
	case "get_thread":
		return s.getThread(ctx, req)
	case "list_channels":
		return s.listChannels(ctx)
	case "add_reaction":
		return s.addReaction(ctx, req)
	default:
		return llm.ErrorfToolOut("unknown slack action: %s", req.Action)
	}
}

func (s *SlackTool) sendMessage(req slackInput) llm.ToolOut {
	if req.Channel == "" {
		return llm.ErrorfToolOut("channel is required")
	}
	if req.Text == "" {
		return llm.ErrorfToolOut("text is required")
	}
	s.API.PostMessage(req.Channel, req.ThreadTS, req.Text)
	return llm.ToolOut{LLMContent: llm.TextContent("Message sent.")}
}

func (s *SlackTool) getHistory(ctx context.Context, req slackInput) llm.ToolOut {
	if req.Channel == "" {
		return llm.ErrorfToolOut("channel is required")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	msgs, err := s.API.GetHistory(ctx, req.Channel, limit)
	if err != nil {
		return llm.ErrorfToolOut("get history: %v", err)
	}
	return llm.ToolOut{LLMContent: llm.TextContent(formatMessages(msgs))}
}

func (s *SlackTool) getThread(ctx context.Context, req slackInput) llm.ToolOut {
	if req.Channel == "" {
		return llm.ErrorfToolOut("channel is required")
	}
	if req.ThreadTS == "" {
		return llm.ErrorfToolOut("thread_ts is required")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	msgs, err := s.API.GetThread(ctx, req.Channel, req.ThreadTS, limit)
	if err != nil {
		return llm.ErrorfToolOut("get thread: %v", err)
	}
	return llm.ToolOut{LLMContent: llm.TextContent(formatMessages(msgs))}
}

func (s *SlackTool) listChannels(ctx context.Context) llm.ToolOut {
	channels, err := s.API.ListChannels(ctx)
	if err != nil {
		return llm.ErrorfToolOut("list channels: %v", err)
	}
	var sb strings.Builder
	for _, ch := range channels {
		fmt.Fprintf(&sb, "%s  #%s\n", ch.ID, ch.Name)
	}
	if sb.Len() == 0 {
		return llm.ToolOut{LLMContent: llm.TextContent("No channels found.")}
	}
	return llm.ToolOut{LLMContent: llm.TextContent(sb.String())}
}

func (s *SlackTool) addReaction(ctx context.Context, req slackInput) llm.ToolOut {
	if req.Channel == "" {
		return llm.ErrorfToolOut("channel is required")
	}
	if req.Timestamp == "" {
		return llm.ErrorfToolOut("timestamp is required")
	}
	if req.Emoji == "" {
		return llm.ErrorfToolOut("emoji is required")
	}
	if err := s.API.AddReaction(ctx, req.Channel, req.Timestamp, req.Emoji); err != nil {
		return llm.ErrorfToolOut("add reaction: %v", err)
	}
	return llm.ToolOut{LLMContent: llm.TextContent("Reaction added.")}
}

func formatMessages(msgs []SlackMessage) string {
	if len(msgs) == 0 {
		return "No messages found."
	}
	var sb strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&sb, "[%s] %s: %s\n", m.Timestamp, m.User, m.Text)
	}
	return sb.String()
}
