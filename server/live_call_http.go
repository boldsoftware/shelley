package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/db/generated"
)

// livePing keeps proxies from closing an idle instruction stream.
const livePing = 25 * time.Second

// liveCalls are the calls in progress, by Live session id.
type liveCalls struct {
	mu    sync.Mutex
	calls map[string]*liveCall
}

func (l *liveCalls) add(c *liveCall) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.calls == nil {
		l.calls = map[string]*liveCall{}
	}
	if old := l.calls[c.id]; old != nil {
		go old.end("replaced")
	}
	l.calls[c.id] = c
	go func() {
		<-c.done
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.calls[c.id] == c {
			delete(l.calls, c.id)
		}
	}()
}

func (l *liveCalls) get(conversationID, sessionID string) *liveCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	if c := l.calls[sessionID]; c != nil && c.conversationID == conversationID {
		return c
	}
	return nil
}

// Surface only a direct worker's filtered tool activity during an active parent
// turn. Its text, replies, and idle/end transitions belong to its own chat.
func (l *liveCalls) workerActivity(worker generated.Conversation, rows []generated.Message) {
	if !isDelegatedSubagent(worker) {
		return
	}
	l.withActiveProgress(*worker.ParentConversationID, func(c *liveCall) {
		for _, row := range rows {
			_, events, _ := liveProgress(row)
			for _, event := range events {
				c.noteProgress("Worker " + worker.ConversationID + ": " + event)
			}
		}
	})
}

func (l *liveCalls) withActiveProgress(conversationID string, f func(*liveCall)) {
	l.mu.Lock()
	var calls []*liveCall
	for _, c := range l.calls {
		if c.conversationID == conversationID && c.progressEnabled {
			calls = append(calls, c)
		}
	}
	l.mu.Unlock()
	for _, c := range calls {
		c.locked(func() {
			if c.active && c.progressWorking {
				f(c)
			}
		})
	}
}

// startLiveCall begins delegation for a Live session that holds the
// conversation up to initialSeq. Its tasks go through chat as r's user,
// with a draft's settings until the draft is promoted.
func (s *Server) startLiveCall(r *http.Request, conversationID, sessionID string, initialSeq int64, settings liveSettings, progressEnabled bool) error {
	var call int64
	err := s.db.QueriesTx(r.Context(), func(q *generated.Queries) error {
		var err error
		call, err = q.CreateLiveCall(r.Context(), conversationID)
		return err
	})
	if err != nil {
		return err
	}
	header := r.Header.Clone()
	deliver := func(ctx context.Context, heading, transcript, voiceContext, deliveryID string, through int) (liveReceipt, error) {
		conversation, err := s.db.GetConversationByID(ctx, conversationID)
		if err != nil {
			return liveReceipt{}, fmt.Errorf("Failed to deliver speech to Shelley: %w", err)
		}
		if !conversation.IsDraft {
			settings = liveSettings{}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/conversation/"+conversationID+"/chat", nil)
		if err != nil {
			return liveReceipt{}, err
		}
		req.Header = header.Clone()
		reference := liveTranscriptReference(call)
		recorder := s.liveChat(req, conversationID, heading, transcript, voiceContext, reference, settings, deliveryID)
		var receipt liveReceipt
		if recorder.Code != http.StatusAccepted || json.Unmarshal(recorder.Body.Bytes(), &receipt) != nil {
			detail := strings.TrimSpace(recorder.Body.String())
			if detail == "" {
				detail = http.StatusText(recorder.Code)
			}
			return liveReceipt{}, errors.New("Failed to deliver speech to Shelley: " + detail)
		}
		return receipt, nil
	}
	save := func(ctx context.Context, fragments []liveFragment) error {
		return s.db.QueriesTx(ctx, func(q *generated.Queries) error {
			for _, f := range fragments {
				err := q.AppendLiveCallTranscript(ctx, generated.AppendLiveCallTranscriptParams{
					ConversationID: conversationID, Call: call, N: int64(f.n), User: f.user,
					Text: f.text, StartMs: f.startMs, EndMs: f.endMs,
				})
				if err != nil {
					return err
				}
			}
			return nil
		})
	}
	c := newLiveCall(sessionID, conversationID, initialSeq, deliver, save, s.logger)
	c.call = call
	c.progressEnabled = progressEnabled
	s.liveCalls.add(c)
	go s.watchLiveCall(c, initialSeq)
	return nil
}

// watchLiveCall feeds the call the conversation's rows after seq as they
// are recorded, until the call ends.
func (s *Server) watchLiveCall(c *liveCall, seq int64) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer c.observeWorking(false)
	go func() {
		<-c.done
		cancel()
	}()
	catchUp := func() bool {
		var progressVersion uint64
		if c.progressEnabled {
			c.locked(func() { progressVersion = c.progressVersion })
		}
		var rows []generated.Message
		var working bool
		err := s.db.Queries(ctx, func(q *generated.Queries) error {
			if c.progressEnabled {
				conversation, err := q.GetConversation(ctx, c.conversationID)
				if err != nil {
					return err
				}
				working = conversation.AgentWorking
			}
			var err error
			rows, err = q.ListMessagesSince(ctx, generated.ListMessagesSinceParams{ConversationID: c.conversationID, SequenceID: seq})
			return err
		})
		if err != nil {
			if ctx.Err() == nil {
				c.logger.Error("Live call cannot read the conversation", "error", err)
			}
			return false
		}
		var heard []liveRow
		for _, row := range rows {
			seq = max(seq, row.SequenceID)
			user, text, full, err := liveDialogue(row)
			if err != nil {
				c.logger.Warn("Live call skipped a message", "error", err)
			}
			var progress []string
			var activity []string
			var endOfTurn bool
			if c.progressEnabled {
				progress, activity, endOfTurn = liveProgress(row)
			}
			heard = append(heard, liveRow{
				seq: row.SequenceID, user: user, text: text, full: full, progress: progress, activity: activity, endOfTurn: endOfTurn,
				liveDeliveryID: liveDeliveryIDFromUserData([]byte(derefString(row.UserData))),
			})
		}
		if c.progressEnabled {
			c.observeProgressSnapshot(heard, working, progressVersion)
		} else {
			c.observe(heard)
		}
		return true
	}
	for ctx.Err() == nil {
		// Subscribe before reading, so that no row falls in between; a
		// subscriber that falls behind starts over.
		next := s.streamPub.Subscribe(ctx, 0)
		if !catchUp() {
			return
		}
		for {
			event, ok := next()
			if !ok {
				break
			}
			if event.ConversationID != c.conversationID {
				continue
			}
			if (len(event.Messages) > 0 || c.progressEnabled && event.ConversationState != nil) && !catchUp() {
				return
			}
		}
	}
}

// handleLiveStream streams a call's instructions to its client as SSE.
func (s *Server) handleLiveStream(w http.ResponseWriter, r *http.Request) {
	c := s.liveCalls.get(r.PathValue("id"), r.PathValue("session"))
	if c == nil {
		http.Error(w, "Live call not found", http.StatusNotFound)
		return
	}
	resume := r.URL.Query().Get("after")
	if resume == "" {
		resume = r.Header.Get("Last-Event-ID")
	}
	var after int64
	if resume != "" {
		var err error
		if after, err = strconv.ParseInt(resume, 10, 64); err != nil {
			http.Error(w, "invalid after", http.StatusBadRequest)
			return
		}
	}
	superseded, detach := c.attach()
	defer detach()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	ping := time.NewTicker(livePing)
	defer ping.Stop()
	write := func() (wake, done <-chan struct{}, ok bool) {
		out, wake, done := c.instructions(after)
		for _, in := range out {
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", in.id, in.data); err != nil {
				return nil, nil, false
			}
			after = in.id
		}
		return wake, done, rc.Flush() == nil
	}
	for {
		wake, done, ok := write()
		if !ok {
			return
		}
		select {
		case <-wake:
		case <-done:
			write() // what ended the call
			return
		case <-superseded:
			return
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
		}
	}
}

// handleLiveEvents takes Live's data-channel events for a call.
func (s *Server) handleLiveEvents(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLiveRequestBytes)).Decode(&body); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "Live events too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Invalid live events", http.StatusBadRequest)
		return
	}
	events := make([]liveEvent, len(body.Events))
	for i, raw := range body.Events {
		var ok bool
		if events[i], ok = parseLiveEvent(raw); !ok {
			http.Error(w, "Invalid live events", http.StatusBadRequest)
			return
		}
	}
	if len(events) == 0 {
		http.Error(w, "Invalid live events", http.StatusBadRequest)
		return
	}
	c := s.liveCalls.get(r.PathValue("id"), r.PathValue("session"))
	if c == nil {
		http.Error(w, "Live call not found", http.StatusNotFound)
		return
	}
	c.handle(events)
	w.WriteHeader(http.StatusNoContent)
}

// handleLiveHangUp ends a call; it is idempotent.
func (s *Server) handleLiveHangUp(w http.ResponseWriter, r *http.Request) {
	if c := s.liveCalls.get(r.PathValue("id"), r.PathValue("session")); c != nil {
		c.end("hung up")
	}
	w.WriteHeader(http.StatusNoContent)
}

// liveTranscriptMarker starts the pointer a delegated request carries to its
// call's saved transcript, numbered as its voice exchange.
const liveTranscriptMarker = "\n\nFull transcript: "

func liveTranscriptReference(call int64) string {
	return fmt.Sprintf("%s`shelley client call-transcript %d`", liveTranscriptMarker, call)
}

// handleLiveTranscript serves a call's saved transcript as plain text, one
// line per run of a speaker's captions (transcript deltas), numbered by the
// run's last caption.
func (s *Server) handleLiveTranscript(w http.ResponseWriter, r *http.Request) {
	call, err := strconv.ParseInt(r.PathValue("call"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid call", http.StatusBadRequest)
		return
	}
	var rows []generated.LiveCallTranscript
	err = s.db.Queries(r.Context(), func(q *generated.Queries) error {
		var err error
		rows, err = q.ListLiveCallTranscript(r.Context(), generated.ListLiveCallTranscriptParams{
			ConversationID: r.PathValue("id"), Call: call,
		})
		return err
	})
	if err != nil {
		s.internalError(w, "Failed to read the call transcript", err)
		return
	}
	var b strings.Builder
	for i, row := range rows {
		if i > 0 && rows[i-1].User == row.User {
			continue
		}
		var text strings.Builder
		last := row
		for _, run := range rows[i:] {
			if run.User != row.User {
				break
			}
			text.WriteString(run.Text)
			last = run
		}
		speaker := "Live"
		if row.User {
			speaker = "User"
		}
		fmt.Fprintf(&b, "%d %s: %s\n", last.N, speaker, strings.Join(strings.Fields(text.String()), " "))
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, b.String())
}
