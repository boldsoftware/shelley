package server

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/db/generated"
)

// The /debug/channels/ page (debug/channels/) is a stand-in for the
// exe.dev messages service, for trying channels without a phone. The page
// plays the phone: what it types is delivered to the chat's conversation
// like exed delivers a text, and the chat's replies land in memory at this
// server's /debug/channels/gateway/{chat}/, which speaks the messages
// gateway's API. Chats are forgotten when Shelley restarts; their
// conversations are not.
//
// Debug chat ids start with "debug-", so the page never touches a real
// chat. A debug chat's endpoint carries a random token, which the gateway
// checks against the conversation's stored endpoint: the gateway is outside
// /api/ (Shelley calls it without the API's headers), so the token is what
// keeps others from writing to it.

// debugChatIDPattern matches debug chat ids.
var debugChatIDPattern = regexp.MustCompile(`^debug-[A-Za-z0-9_-]{1,64}$`)

// debugUnansweredLimit mirrors exed: the gateway refuses sends once this
// many go unanswered.
const debugUnansweredLimit = 5

// debugRefusals are the gateway refusals the page can impose, with the
// status and message exed answers.
var debugRefusals = map[string]struct {
	status  int
	message string
}{
	"opted_out":     {http.StatusForbidden, "the recipient asked us to stop messaging; wait for them to write again"},
	"no_chat":       {http.StatusConflict, "no message chat yet: the owner has not messaged exe.dev from a linked handle"},
	"line_paused":   {http.StatusServiceUnavailable, "our line is paused for deliverability; retry later"},
	"chat_critical": {http.StatusServiceUnavailable, "this chat's health is critical; wait for the recipient to reply"},
	"rate_limited":  {http.StatusTooManyRequests, "too many messages, retry later"},
	"unanswered":    {http.StatusTooManyRequests, "too many messages without a reply; wait for the recipient to write back"},
}

// channelDebug holds the /debug/channels chats.
type channelDebug struct {
	mu    sync.Mutex
	seq   int64
	chats map[string]*debugChat
	// run distinguishes this process's message ids from those of earlier
	// processes, which persist in conversations while seq restarts.
	run string
}

type debugChat struct {
	ChatID string           `json:"chat_id"`
	Events []debugChatEvent `json:"events"`
	// Typing is whether Shelley shows a typing indicator.
	Typing bool `json:"typing"`
	// Unanswered counts sends since the phone last wrote.
	Unanswered int `json:"unanswered"`
	// Refuse is a gateway refusal code imposed from the page, or "".
	Refuse string `json:"refuse"`
}

// debugChatEvent is one thing that happened on a debug chat.
type debugChatEvent struct {
	Seq  int64     `json:"seq"`
	Time time.Time `json:"time"`
	// Kind is "in" (the phone wrote), "out" (Shelley sent), "react",
	// "read", or "refused" (the gateway refused a call).
	Kind      string `json:"kind"`
	MessageID string `json:"message_id,omitempty"`
	Text      string `json:"text,omitempty"`
	MediaURL  string `json:"media_url,omitempty"`
	ReplyTo   string `json:"reply_to,omitempty"`
	// Error is why an "in" message was not delivered, or a refusal.
	Error string `json:"error,omitempty"`
}

// chatLocked returns the chat, creating it. d.mu must be held.
func (d *channelDebug) chatLocked(chatID string) *debugChat {
	if d.chats == nil {
		d.chats = make(map[string]*debugChat)
	}
	c := d.chats[chatID]
	if c == nil {
		c = &debugChat{ChatID: chatID, Events: []debugChatEvent{}}
		d.chats[chatID] = c
	}
	return c
}

// addLocked appends ev to the chat and returns it. Messages ("in" and
// "out") get an id. d.mu must be held.
func (d *channelDebug) addLocked(c *debugChat, ev debugChatEvent) debugChatEvent {
	if d.run == "" {
		d.run = strings.ToLower(rand.Text()[:6])
	}
	d.seq++
	ev.Seq = d.seq
	ev.Time = time.Now().UTC()
	if ev.Kind == "in" || ev.Kind == "out" {
		ev.MessageID = fmt.Sprintf("debug-%s-%s-%d", ev.Kind, d.run, ev.Seq)
	}
	c.Events = append(c.Events, ev)
	return ev
}

// debugGatewayPath is the path of debug chat chatID's gateway with token.
func debugGatewayPath(chatID, token string) string {
	return "/debug/channels/gateway/" + chatID + "/" + token
}

// debugChannelEndpoint returns a new endpoint for debug chat chatID's
// replies; the first one is stored with the chat's conversation.
func (s *Server) debugChannelEndpoint(chatID string) string {
	return fmt.Sprintf("http://localhost:%d%s", s.listenPort, debugGatewayPath(chatID, rand.Text()))
}

// debugGatewayAuthorized reports whether token is the one in debug chat
// chatID's stored endpoint.
func (s *Server) debugGatewayAuthorized(r *http.Request, chatID, token string) bool {
	conv, err := s.debugChatConversation(r, chatID)
	if err != nil || conv.ExternalEndpoint == nil {
		return false
	}
	u, err := url.Parse(*conv.ExternalEndpoint)
	return err == nil && subtle.ConstantTimeCompare([]byte(u.Path), []byte(debugGatewayPath(chatID, token))) == 1
}

// debugChatConversation returns debug chat chatID's conversation.
func (s *Server) debugChatConversation(r *http.Request, chatID string) (generated.Conversation, error) {
	var conv generated.Conversation
	err := s.db.Queries(r.Context(), func(q *generated.Queries) (err error) {
		conv, err = q.GetConversationByExternalID(r.Context(), &chatID)
		return err
	})
	return conv, err
}

// debugChatID returns the request's debug chat id, or answers 404 and "".
func debugChatID(w http.ResponseWriter, r *http.Request) string {
	chatID := r.PathValue("chat")
	if !debugChatIDPattern.MatchString(chatID) {
		http.Error(w, "not a debug chat: ids match "+debugChatIDPattern.String(), http.StatusNotFound)
		return ""
	}
	return chatID
}

func writeChannelJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeDebugGatewayError(w http.ResponseWriter, status int, code, message string) {
	writeChannelJSON(w, status, map[string]string{"error": code, "message": message})
}

// handleDebugChannelGateway serves
// /debug/channels/gateway/{chat}/{token}/{action}, the messages gateway's
// API for a debug chat.
func (s *Server) handleDebugChannelGateway(w http.ResponseWriter, r *http.Request) {
	chatID, action := r.PathValue("chat"), r.PathValue("action")
	if !debugChatIDPattern.MatchString(chatID) || !s.debugGatewayAuthorized(r, chatID, r.PathValue("token")) {
		writeDebugGatewayError(w, http.StatusNotFound, "not_found", "unknown debug chat or token")
		return
	}
	var req struct {
		Text      string `json:"text"`
		MediaURL  string `json:"media_url"`
		ReplyTo   string `json:"reply_to"`
		MessageID string `json:"message_id"`
		Reaction  string `json:"reaction"`
		Remove    bool   `json:"remove"`
	}
	switch r.Method + " " + action {
	case "POST send", "POST react":
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeDebugGatewayError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
			return
		}
	case "POST typing", "DELETE typing", "POST read":
	default:
		writeDebugGatewayError(w, http.StatusNotFound, "not_found", "unknown path; use POST /send, POST|DELETE /typing, POST /react, POST /read")
		return
	}
	if action == "send" && req.Text == "" && req.MediaURL == "" {
		writeDebugGatewayError(w, http.StatusBadRequest, "bad_request", "text or media_url is required")
		return
	}
	if action == "react" && (req.MessageID == "" || req.Reaction == "") {
		writeDebugGatewayError(w, http.StatusBadRequest, "bad_request", "message_id and reaction are required")
		return
	}

	d := &s.channelDebug
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.chatLocked(chatID)

	// Refuse like exed: opted_out and no_chat refuse every call, the
	// deliverability refusals spare typing and read, and the send limits
	// apply only to sends.
	refuse := c.Refuse
	if refuse == "" && action == "send" && c.Unanswered >= debugUnansweredLimit {
		refuse = "unanswered"
	}
	switch {
	case refuse == "line_paused" || refuse == "chat_critical":
		if action != "send" && action != "react" {
			refuse = ""
		}
	case refuse == "rate_limited" || refuse == "unanswered":
		if action != "send" {
			refuse = ""
		}
	}
	if refuse != "" {
		rf := debugRefusals[refuse]
		d.addLocked(c, debugChatEvent{Kind: "refused", Text: req.Text, Error: fmt.Sprintf("%s %s: %s", r.Method, action, refuse)})
		writeDebugGatewayError(w, rf.status, refuse, rf.message)
		return
	}

	switch r.Method + " " + action {
	case "POST send":
		c.Typing = false
		c.Unanswered++
		ev := d.addLocked(c, debugChatEvent{Kind: "out", Text: req.Text, MediaURL: req.MediaURL, ReplyTo: req.ReplyTo})
		writeChannelJSON(w, http.StatusOK, map[string]string{"message_id": ev.MessageID})
		return
	case "POST typing":
		c.Typing = true
	case "DELETE typing":
		c.Typing = false
	case "POST read":
		d.addLocked(c, debugChatEvent{Kind: "read"})
	case "POST react":
		text := req.Reaction
		if req.Remove {
			text = "removed " + text
		}
		d.addLocked(c, debugChatEvent{Kind: "react", MessageID: req.MessageID, Text: text})
	}
	writeChannelJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// debugChatView is a chat as GET /api/debug/channels shows it.
type debugChatView struct {
	debugChat
	ConversationID string `json:"conversation_id,omitempty"`
	Slug           string `json:"slug,omitempty"`
}

// handleDebugChannelsList serves GET /api/debug/channels: the debug chats,
// with their conversations.
func (s *Server) handleDebugChannelsList(w http.ResponseWriter, r *http.Request) {
	d := &s.channelDebug
	d.mu.Lock()
	views := make([]debugChatView, 0, len(d.chats))
	for _, c := range d.chats {
		v := debugChatView{debugChat: *c}
		v.Events = append([]debugChatEvent{}, c.Events...)
		views = append(views, v)
	}
	d.mu.Unlock()

	for i := range views {
		conv, err := s.debugChatConversation(r, views[i].ChatID)
		if errors.Is(err, sql.ErrNoRows) {
			continue // not delivered yet, or deleted
		}
		if err != nil {
			s.internalError(w, "Failed to look up debug chat conversation", err, "chat_id", views[i].ChatID)
			return
		}
		views[i].ConversationID = conv.ConversationID
		if conv.Slug != nil {
			views[i].Slug = *conv.Slug
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeChannelJSON(w, http.StatusOK, views)
}

// handleDebugChannelReceive serves POST /api/debug/channels/{chat}/receive
// {"text": "..."}: the phone writes on the debug chat, which delivers the
// text to the chat's conversation as exed delivers a text. The body may set
// the conversation fields exed sends (system_prompt, first_reaction,
// first_reply) too.
func (s *Server) handleDebugChannelReceive(w http.ResponseWriter, r *http.Request) {
	chatID := debugChatID(w, r)
	if chatID == "" {
		return
	}
	var req channelEvent
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	d := &s.channelDebug
	d.mu.Lock()
	c := d.chatLocked(chatID)
	c.Unanswered = 0
	ev := d.addLocked(c, debugChatEvent{Kind: "in", Text: req.Text})
	d.mu.Unlock()

	err := s.receiveChannelEvent(r.Context(), s.debugChannelEndpoint(chatID), channelEvent{
		Type:          "message",
		ID:            ev.MessageID,
		ChatID:        chatID,
		Sender:        "debug",
		Text:          req.Text,
		SystemPrompt:  req.SystemPrompt,
		FirstReaction: req.FirstReaction,
		FirstReply:    req.FirstReply,
	})
	if err != nil {
		d.mu.Lock()
		for i := range c.Events {
			if c.Events[i].Seq == ev.Seq {
				c.Events[i].Error = err.Error()
			}
		}
		d.mu.Unlock()
		s.internalError(w, "Failed to deliver debug channel message", err, "chat_id", chatID)
		return
	}
	writeChannelJSON(w, http.StatusOK, map[string]string{"message_id": ev.MessageID})
}

// handleDebugChannelSend serves POST /api/debug/channels/{chat}/send
// {"text": "..."}: the chat's conversation sends text to the chat, through
// the same path as an agent's reply. The endpoint's refusal is passed on.
func (s *Server) handleDebugChannelSend(w http.ResponseWriter, r *http.Request) {
	chatID := debugChatID(w, r)
	if chatID == "" {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Text == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	conv, err := s.debugChatConversation(r, chatID)
	if err != nil {
		http.Error(w, "chat has no conversation; write from the phone first", http.StatusNotFound)
		return
	}
	id, err := s.sendChannelMessage(r.Context(), conv.ConversationID, req.Text, "")
	var rf *channelSendError
	if errors.As(err, &rf) {
		writeDebugGatewayError(w, rf.Status, rf.Code, rf.Message)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to send to debug channel", err, "chat_id", chatID)
		return
	}
	writeChannelJSON(w, http.StatusOK, map[string]string{"message_id": id})
}

// handleDebugChannelRefuse serves POST /api/debug/channels/{chat}/refuse
// {"code": "..."}: the debug chat's gateway refuses calls with code, a
// gateway refusal code, or stops refusing when code is "".
func (s *Server) handleDebugChannelRefuse(w http.ResponseWriter, r *http.Request) {
	chatID := debugChatID(w, r)
	if chatID == "" {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if _, ok := debugRefusals[req.Code]; !ok && req.Code != "" {
		http.Error(w, "unknown refusal code", http.StatusBadRequest)
		return
	}
	d := &s.channelDebug
	d.mu.Lock()
	d.chatLocked(chatID).Refuse = req.Code
	d.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}
