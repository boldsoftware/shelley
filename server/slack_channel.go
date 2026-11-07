package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/llm"
)

// exe.dev's Slack app connects the VM owner's Slack account to one
// conversation, the Slack Assistant (slug slack-app): every event exed
// routes here (a message, a reaction to the bot's message, a click on one of
// its buttons) goes to it, tagged with its channel and thread, and the agent
// answers with the Slack tools, which POST to the slack-app integration:
//
//	POST <endpoint>/send {"channel","thread_ts","text","blocks","files":[{"name","data"}],"link"} answers {"message_id":"<ts>"}
//	POST <endpoint>/react {"channel","ts","emoji"} answers {}
//
// link is the Shelley URL of the thread's latest event. A refusal answers
// {"error":"<code>","message":"…"}, as channel endpoints do.

// slackChatID is the external_conversation_id of the Slack Assistant.
const slackChatID = "slack:assistant"

// slackAssistantProfile is the profile the Slack Assistant starts from.
const slackAssistantProfile = "Slack Assistant"

const (
	// slackIntegration is the exe.dev integration replies go through.
	slackIntegration = "slack-app"
	// slackSlug is the Slack Assistant's slug.
	slackSlug = "slack-app"
)

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
// the sender retry; a retry of a delivered message is dropped.
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
	id, err := s.receiveSlackEvent(r.Context(), env.IntegrationURL(slackIntegration, false), ev)
	if errors.Is(err, errInvalidSlackEvent) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to deliver slack event", err, "channel", ev.Channel, "ts", ev.TS)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"conversation_id": id})
}

// receiveSlackEvent delivers ev to the Slack Assistant, created on the
// first event with its replies going to endpoint, and returns its id.
func (s *Server) receiveSlackEvent(ctx context.Context, endpoint string, ev slackEvent) (string, error) {
	if !slices.Contains([]string{"message", "reaction", "action"}, ev.Kind) || ev.ID == "" || ev.Channel == "" || ev.TS == "" || ev.ThreadTS == "" || (strings.TrimSpace(ev.Text) == "" && len(ev.Files) == 0) {
		return "", errInvalidSlackEvent
	}
	s.noteUserActivity()
	unlock, err := s.lockChannelChat(ctx, slackChatID)
	if err != nil {
		return "", err
	}
	defer unlock()

	conv, err := s.slackConversation(ctx, endpoint)
	if err != nil {
		return "", err
	}
	id := conv.ConversationID
	manager, err := s.getOrCreateConversationManager(ctx, id, "")
	if err != nil {
		return "", err
	}
	externalID := slackThreadPrefix(ev.Channel, ev.ThreadTS) + ev.ID
	delivered, err := s.channelMessageDelivered(ctx, id, externalID)
	if err != nil {
		return "", err
	}
	if delivered {
		manager.drainQueueIfIdle()
		return id, nil
	}
	if conv.Model == nil {
		return "", fmt.Errorf("conversation %s has no model", id)
	}
	modelID := *conv.Model
	service, err := s.llmManager.GetService(modelID)
	if err != nil {
		return "", fmt.Errorf("model %s: %w", modelID, err)
	}
	// Fetch files only after deduplication, while holding the chat lock.
	ev.Text, err = s.channelMessageText(ctx, ev.Text, ev.Files)
	if err != nil {
		return "", err
	}
	ctx = contextWithExternalMessageID(ctx, externalID)
	message := llm.UserStringMessage(ev.tagged())
	_, err = manager.AcceptUserMessage(ctx, service, modelID, message)
	return id, err
}

// slackThreadPrefix starts the external ids of a thread's events.
func slackThreadPrefix(channel, threadTS string) string {
	return channel + "/" + threadTS + "/"
}

// tagged is ev as the agent reads it.
func (ev slackEvent) tagged() string {
	a := html.EscapeString
	extra := ""
	switch ev.Kind {
	case "message":
		extra = fmt.Sprintf(` mentions_you="%t"`, ev.MentionsYou)
	case "reaction":
		extra = fmt.Sprintf(` emoji=%q`, a(ev.Reaction))
	}
	return fmt.Sprintf(`<slack_%s channel=%q channel_name=%q thread_ts=%q ts=%q user=%q user_name=%q%s>`+"\n%s\n</slack_%s>",
		ev.Kind, a(ev.Channel), a(ev.ChannelName), a(ev.ThreadTS), a(ev.TS), a(ev.User), a(ev.UserName), extra, ev.Text, ev.Kind)
}

// slackConversation returns the Slack Assistant, created from its profile
// with its replies going to endpoint if there is none.
func (s *Server) slackConversation(ctx context.Context, endpoint string) (*generated.Conversation, error) {
	chatID := slackChatID
	var conv generated.Conversation
	err := s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		conv, err = q.GetConversationByExternalID(ctx, &chatID)
		return err
	})
	if err == nil {
		return &conv, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	profile, err := s.getProfile(ctx, slackAssistantProfile)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", slackAssistantProfile, err)
	}
	settings, err := s.resolveSettings(ctx, ConversationSettings{Profile: profile.Name, Settings: profile.Settings}, SettingsChange{}, s.getModelList())
	if err != nil {
		return nil, err
	}
	var opts db.ConversationOptions
	settings.applyTo(&opts)
	c, err := s.db.CreateChannelConversation(ctx, chatID, endpoint, settings.Model, opts)
	if err != nil {
		return nil, err
	}
	if _, _, err := s.db.SetConversationSlugIfUnset(ctx, c.ConversationID, slackSlug); err != nil {
		return nil, err
	}
	return c, nil
}

// isSlackChat reports whether conv is the Slack Assistant.
func isSlackChat(conv *generated.Conversation) bool {
	return derefString(conv.ExternalConversationID) == slackChatID
}

// slackChat is the Slack Assistant's claudetool.Slack.
type slackChat struct {
	s              *Server
	conversationID string
}

func (c slackChat) Send(ctx context.Context, p claudetool.SlackPost) (string, error) {
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
	err = c.call(ctx, "/send", body, &sent)
	return sent.MessageID, err
}

func (c slackChat) React(ctx context.Context, channel, ts, emoji string) error {
	var answer struct{}
	body := map[string]string{"channel": channel, "ts": ts, "emoji": strings.Trim(emoji, ":")}
	return c.call(ctx, "/react", body, &answer)
}

func (c slackChat) call(ctx context.Context, path string, body, out any) error {
	conv, err := c.s.db.GetConversationByID(ctx, c.conversationID)
	if err != nil {
		return fmt.Errorf("nothing was sent: %w", err)
	}
	endpoint := derefString(conv.ExternalEndpoint)
	if endpoint == "" {
		return errors.New("nothing was sent: the Slack Assistant has no endpoint")
	}
	return chatDeliveryError(c.s.callChannelEndpoint(ctx, endpoint, path, body, out))
}

// link returns the Shelley URL of the latest event in the thread, or of
// the conversation for a new thread.
func (c slackChat) link(ctx context.Context, channel, threadTS string) (string, error) {
	conv, err := c.s.db.GetConversationByID(ctx, c.conversationID)
	if err != nil {
		return "", err
	}
	u := c.s.conversationURL(derefString(conv.Slug))
	if threadTS == "" {
		return u, nil
	}
	var id string
	err = c.s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		id, err = q.LatestExternalMessageWithPrefix(ctx, generated.LatestExternalMessageWithPrefixParams{ConversationID: c.conversationID, Prefix: slackThreadPrefix(channel, threadTS)})
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return u, nil
	}
	return u + "?message_id=" + url.QueryEscape(id), err
}
