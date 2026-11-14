package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"shelley.exe.dev/llm"
)

// The Slack tools post to the Slack workspace connected through exe.dev's
// Slack app. Its events reach the Assistant wrapped in slack_message,
// slack_reaction and slack_action tags carrying their channel and thread.
const (
	SendToThreadName  = "send_to_thread"
	SendToChannelName = "send_to_channel"
	AddReactionName   = "add_reaction"
)

// sendToThreadDescription carries the Slack guidance, so that it comes
// with the tools whatever the system prompt.
const sendToThreadDescription = `Reply in a Slack thread. Use the channel and thread_ts of the event you answer.

The user's Slack messages reach you through exe.dev's Slack app, each with its channel, channel_name, thread_ts, ts, user and user_name: slack_message (they wrote; mentions_you says whether they mentioned you; files they shared are saved locally and referenced by path in brackets), slack_reaction (they reacted with emoji to your message ts, whose text it carries) and slack_action (they clicked a button on your message ts). Their text is XML-escaped over Slack's own markup: &lt;@U1&gt; is the mention <@U1>.

Slack sees only what you send with send_to_thread, send_to_channel and add_reaction: answer Slack only with them, never message_user, and end your turn once you have. Reply in the event's thread; use send_to_channel only to start something new. Respond to direct messages (channel D…), mentions, and reactions and clicks that ask for something; a thread reply may be meant for someone else in the thread, so stay quiet then. Others read Slack channels and threads: share private data there only where the user asked for it, and never carry one thread's content into another. When a request needs work, acknowledge it with add_reaction (eyes) on its ts instead of a message, then reply in its thread with the result. For structured results (status, summaries, choices) use blocks. Every post gets a link back to this conversation automatically; don't add one.`

// maxSlackFileBytes caps each file sent to Slack.
const maxSlackFileBytes = 50 << 20

// Slack posts to a Slack workspace. It is implemented by the server
// package. Errors say whether Slack refused or delivery is unknown.
type Slack interface {
	// Send posts p and returns the posted message's ts.
	Send(ctx context.Context, p SlackPost) (ts string, err error)
	// React adds the emoji reaction name to the message ts.
	React(ctx context.Context, channel, ts, name string) error
}

// SlackPost is a message to post: in the thread ThreadTS if set, else
// top-level. Files are shared in the message's thread after it.
type SlackPost struct {
	Channel  string          `json:"channel"`
	ThreadTS string          `json:"thread_ts,omitempty"`
	Text     string          `json:"text"`
	Blocks   json.RawMessage `json:"blocks,omitempty"`
	Files    []SlackFile     `json:"files,omitempty"`
}

// SlackFile is a file to share, read from a local path.
type SlackFile struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

type slackSendInput struct {
	Channel  string          `json:"channel"`
	ThreadTS string          `json:"thread_ts"`
	Text     string          `json:"text"`
	Blocks   json.RawMessage `json:"blocks"`
	Files    []string        `json:"files"`
}

type slackReactInput struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
	Emoji   string `json:"emoji"`
}

const slackSendProperties = `
    "channel": {"type": "string", "description": "channel id, like C0123 or D0123"},
    "text": {"type": "string", "description": "Slack mrkdwn: *bold*, _italic_, ` + "`code`" + `, <url|label>, <@USER_ID> to mention, :emoji_name:. With blocks, the notification fallback."},
    "blocks": {"type": "array", "items": {"type": "object"}, "description": "optional Block Kit blocks for a rich card: header, section (with fields or an accessory), divider, context, image (public image_url), actions (buttons with a url, or with action_id and value: a click arrives as a slack_action)"},
    "files": {"type": "array", "items": {"type": "string"}, "description": "optional local file paths (images, logs, …) to share in the thread"}`

// SlackTools returns the Slack tools, backed by s; relative file paths
// resolve against wd.
func SlackTools(s Slack, wd *MutableWorkingDir) []*llm.Tool {
	send := func(thread bool) func(context.Context, slackSendInput) llm.ToolOut {
		return func(ctx context.Context, in slackSendInput) llm.ToolOut {
			if !thread {
				in.ThreadTS = ""
			}
			switch {
			case in.Channel == "":
				return llm.ErrorfToolOut("channel is required")
			case thread && in.ThreadTS == "":
				return llm.ErrorfToolOut("thread_ts is required")
			case in.Text == "":
				return llm.ErrorfToolOut("text is required")
			}
			p := SlackPost{Channel: in.Channel, ThreadTS: in.ThreadTS, Text: in.Text, Blocks: in.Blocks}
			for _, path := range in.Files {
				f, err := readSlackFile(wd.Get(), path)
				if err != nil {
					return llm.ErrorfToolOut("nothing was sent: %w", err)
				}
				p.Files = append(p.Files, f)
			}
			ts, err := s.Send(ctx, p)
			if err != nil {
				return slackFailed(err)
			}
			return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf("Sent (ts %s).", ts))}
		}
	}
	return []*llm.Tool{
		{
			Name:        SendToThreadName,
			Description: sendToThreadDescription,
			InputSchema: llm.MustSchema(`{
  "type": "object",
  "required": ["channel", "thread_ts", "text"],
  "properties": {
    "thread_ts": {"type": "string", "description": "the thread's thread_ts"},` + slackSendProperties + `
  }
}`),
			Run: llm.RunJSON(send(true)),
		},
		{
			Name:        SendToChannelName,
			Description: "Post a new top-level message to a Slack channel. Its ts starts a thread that replies can go to.",
			InputSchema: llm.MustSchema(`{
  "type": "object",
  "required": ["channel", "text"],
  "properties": {` + slackSendProperties + `
  }
}`),
			Run: llm.RunJSON(send(false)),
		},
		{
			Name:        AddReactionName,
			Description: "Add an emoji reaction to a Slack message, such as eyes to acknowledge it or white_check_mark when done.",
			InputSchema: llm.MustSchema(`{
  "type": "object",
  "required": ["channel", "ts", "emoji"],
  "properties": {
    "channel": {"type": "string", "description": "channel id"},
    "ts": {"type": "string", "description": "the message's ts"},
    "emoji": {"type": "string", "description": "emoji name without colons, like eyes, thumbsup, tada"}
  }
}`),
			Run: llm.RunJSON(func(ctx context.Context, in slackReactInput) llm.ToolOut {
				if in.Channel == "" || in.TS == "" || in.Emoji == "" {
					return llm.ErrorfToolOut("channel, ts and emoji are required")
				}
				if err := s.React(ctx, in.Channel, in.TS, in.Emoji); err != nil {
					return slackFailed(err)
				}
				return llm.ToolOut{LLMContent: llm.TextContent("Reacted.")}
			}),
		},
	}
}

// SlackDisplay is the UI's view of a failed Slack call.
type SlackDisplay struct {
	// ChatFailed marks a call that failed in Slack, or on its way there,
	// not a mistake in the call; see MessageUserDisplay.ChatFailed.
	ChatFailed bool `json:"chat_failed,omitempty"`
}

// slackFailed is the result of a call that failed in Slack.
func slackFailed(err error) llm.ToolOut {
	return llm.ToolOut{Error: err, Display: SlackDisplay{ChatFailed: true}}
}

func readSlackFile(cwd, path string) (SlackFile, error) {
	path = resolvePath(cwd, path)
	fi, err := os.Stat(path)
	if err != nil {
		return SlackFile{}, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxSlackFileBytes {
		return SlackFile{}, fmt.Errorf("%s: not a regular file of at most %d MB", path, maxSlackFileBytes>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SlackFile{}, err
	}
	return SlackFile{Name: fi.Name(), Data: data}, nil
}
