package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/exeenv"
)

// exe.dev's Slack app delivers the VM owner's Slack events to the
// Assistant, as texts are (see channels.go): a message, a reaction to the
// bot's message, or a click on one of its buttons, each from the chat of
// its Slack thread. The Assistant answers with the Slack tools
// (claudetool/slack.go), which post through the slack-app integration:
//
//	POST <endpoint>/send {"channel","thread_ts","text","blocks","files":[{"name","data"}],"link"} answers {"message_id":"<ts>"}
//	POST <endpoint>/react {"channel","ts","emoji"} answers {}
//
// link is the Shelley URL of the thread's latest event. A refusal answers
// {"error":"<code>","message":"…"}, as channel endpoints do.

// slackIntegration is the exe.dev integration replies go through.
const slackIntegration = "slack-app"

// slackChatPrefix starts the chat id of a Slack thread,
// slack:<channel>/<thread_ts>. Its events' ids are <chat id>/<event id>.
const slackChatPrefix = "slack:"

// slackEvent is a Slack event, as exed posts it to POST
// /api/channels/slack.
type slackEvent struct {
	// Kind is "message", "reaction" (Reaction, by User, to the bot's
	// message TS, whose text is Text) or "action" (User clicked a button
	// on the bot's message TS, described by Text).
	Kind string `json:"kind"`
	// ID is unique per event in its thread: a message's ts, for one.
	ID          string `json:"id"`
	Channel     string `json:"channel"`
	ChannelName string `json:"channel_name"`
	// TS is the message's ts. ThreadTS is its thread's, its own for a
	// top-level message.
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
	User     string `json:"user"`
	UserName string `json:"user_name"`
	Text     string `json:"text"`
	// Files are a message's files, downloaded as attachments are.
	Files       []channelAttachment `json:"files"`
	Reaction    string              `json:"reaction"`
	MentionsYou bool                `json:"mentions_you"`
}

var errInvalidSlackEvent = errors.New("slack event needs a kind, id, channel, ts, thread_ts, and text or files")

// handleSlackEvent serves POST /api/channels/slack. A non-2xx answer makes
// the sender retry; a retry of a delivered event is dropped.
func (s *Server) handleSlackEvent(w http.ResponseWriter, r *http.Request) {
	var ev slackEvent
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&ev); err != nil {
		http.Error(w, "invalid slack event", http.StatusBadRequest)
		return
	}
	env, err := exeenv.Current()
	if err != nil {
		s.internalError(w, "Cannot determine exe.dev environment", err)
		return
	}
	err = s.receiveSlackEvent(r.Context(), env.IntegrationURL(slackIntegration, false), ev)
	if errors.Is(err, errInvalidSlackEvent) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to deliver slack event", err, "channel", ev.Channel, "ts", ev.TS)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// receiveSlackEvent delivers ev, from the slack-app integration at
// endpoint, to the Assistant.
func (s *Server) receiveSlackEvent(ctx context.Context, endpoint string, ev slackEvent) error {
	if !slices.Contains([]string{"message", "reaction", "action"}, ev.Kind) || ev.ID == "" || ev.Channel == "" || ev.TS == "" || ev.ThreadTS == "" || (strings.TrimSpace(ev.Text) == "" && len(ev.Files) == 0) {
		return errInvalidSlackEvent
	}
	chatID := slackChatPrefix + ev.Channel + "/" + ev.ThreadTS
	return s.deliverChatMessage(ctx, chatMessage{
		route:       db.ChannelRoute{ChatID: chatID, ExternalMessageID: chatID + "/" + ev.ID, Endpoint: endpoint},
		text:        ev.Text,
		attachments: ev.Files,
		tag:         ev.tagged,
	})
}

// tagged is ev, with text for its text, as it is stored. The UI reads it
// (ui/src/utils/slack.ts), and the model reads it as parseSlackEvent does.
func (ev slackEvent) tagged(text string) string {
	a := html.EscapeString
	extra := ""
	switch ev.Kind {
	case "message":
		extra = fmt.Sprintf(` mentions_you="%t"`, ev.MentionsYou)
	case "reaction":
		extra = fmt.Sprintf(` emoji=%q`, a(ev.Reaction))
	}
	return fmt.Sprintf(`<slack_%s channel=%q channel_name=%q thread_ts=%q ts=%q user=%q user_name=%q%s>`+"\n%s\n</slack_%s>",
		ev.Kind, a(ev.Channel), a(ev.ChannelName), a(ev.ThreadTS), a(ev.TS), a(ev.User), a(ev.UserName), extra, text, ev.Kind)
}

var (
	slackTag     = regexp.MustCompile(`^<(slack_(?:message|reaction|action))((?: [a-z_]+="(?:[^"\\]|\\.)*")*)>\n([\s\S]*)\n</(slack_[a-z]+)>$`)
	slackTagAttr = regexp.MustCompile(` ([a-z_]+)=("(?:[^"\\]|\\.)*")`)
)

// parseSlackEvent splits the text of the message with externalID, if it is
// a stored Slack event, into its tag, attributes and text.
func parseSlackEvent(externalID, text string) (tag string, attrs []xml.Attr, body string, ok bool) {
	m := slackTag.FindStringSubmatch(text)
	if !strings.HasPrefix(externalID, slackChatPrefix) || m == nil || m[1] != m[4] {
		return "", nil, "", false
	}
	for _, a := range slackTagAttr.FindAllStringSubmatch(m[2], -1) {
		v, err := strconv.Unquote(a[2])
		if err != nil {
			return "", nil, "", false
		}
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: a[1]}, Value: html.UnescapeString(v)})
	}
	return m[1], attrs, m[3], true
}

// slackChat is the Assistant's claudetool.Slack. It posts through the
// slack-app integration that Slack events reached the Assistant through.
type slackChat struct {
	s              *Server
	conversationID string
}

func (c slackChat) Send(ctx context.Context, p claudetool.SlackPost) (string, error) {
	endpoint, err := c.endpoint(ctx)
	if err != nil {
		return "", fmt.Errorf("nothing was sent: %w", err)
	}
	link, err := c.link(ctx, p.Channel, p.ThreadTS)
	if err != nil {
		return "", fmt.Errorf("nothing was sent: %w", err)
	}
	var sent struct {
		MessageID string `json:"message_id"`
	}
	body := struct {
		claudetool.SlackPost
		Link string `json:"link"`
	}{p, link}
	err = chatDeliveryError(c.s.callChannelEndpoint(ctx, endpoint, "/send", body, &sent))
	return sent.MessageID, err
}

func (c slackChat) React(ctx context.Context, channel, ts, emoji string) error {
	endpoint, err := c.endpoint(ctx)
	if err != nil {
		return fmt.Errorf("nothing was sent: %w", err)
	}
	var answer struct{}
	body := map[string]string{"channel": channel, "ts": ts, "emoji": strings.Trim(emoji, ":")}
	return chatDeliveryError(c.s.callChannelEndpoint(ctx, endpoint, "/react", body, &answer))
}

// endpoint returns the endpoint of the latest Slack event.
func (c slackChat) endpoint(ctx context.Context) (string, error) {
	route, err := c.s.db.LatestChannelRouteWithChatPrefix(ctx, c.conversationID, slackChatPrefix)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("no Slack event has arrived yet, so Slack may not be connected")
	}
	if err != nil {
		return "", err
	}
	return route.Endpoint, nil
}

// link returns the Shelley URL of the latest event in the thread, or of
// the conversation for a new thread.
func (c slackChat) link(ctx context.Context, channel, threadTS string) (string, error) {
	conv, err := c.s.db.GetConversationByID(ctx, c.conversationID)
	if err != nil {
		return "", err
	}
	// The Assistant has no slug while another conversation holds its own.
	slug := derefString(conv.Slug)
	if slug == "" {
		slug = c.conversationID
	}
	u := c.s.conversationURL(slug)
	if threadTS == "" {
		return u, nil
	}
	var id string
	err = c.s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		id, err = q.LatestExternalMessageWithPrefix(ctx, generated.LatestExternalMessageWithPrefixParams{ConversationID: c.conversationID, Prefix: slackChatPrefix + channel + "/" + threadTS + "/"})
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return u, nil
	}
	return u + "?message_id=" + url.QueryEscape(id), err
}
