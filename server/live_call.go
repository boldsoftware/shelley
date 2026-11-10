package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
)

// A Live call's delegation logic. GPT-Live decides when to delegate; the
// call collects the speech around each client delegation, waits for late
// transcript, hands it to the backend, and keeps Live informed of what the
// backend says. The client only relays: Live's events in (handle), and
// instructions out (the outbox, read by the call's stream).

const (
	// User capture has a deadline; Live's response settles on output quiet.
	liveQuiet            = 1200 * time.Millisecond
	liveDeadline         = 5 * time.Second
	liveMaxPerMinute     = 12
	liveFeedbackLoop     = "Voice delegation paused to prevent a feedback loop. Start voice again to continue."
	liveInvalidOffset    = "Live returned an invalid delegation timestamp."
	liveDeliveryFailed   = "The last delegation could not be sent to the backend. Tell the user briefly and ask them to repeat it. Do not report progress."
	liveFragmentsKept    = 500
	liveFragmentsMax     = 1000
	liveInstructionsKept = 256
	// Allow a backgrounded browser time to reconnect both sides of the relay.
	liveIdle = 5 * time.Minute
)

// liveReceipt is chat's answer to a delivered delegation.
type liveReceipt struct {
	Status string `json:"status"` // "accepted" or "queued"
}

// liveDeliverFunc hands speech and its voice exchange to the backend, with
// the call's saved transcript through delta through.
type liveDeliverFunc func(ctx context.Context, heading, transcript, voiceContext, deliveryID string, through int) (liveReceipt, error)

// liveSaveFunc appends transcript deltas to the call's saved transcript.
type liveSaveFunc func(ctx context.Context, fragments []liveFragment) error

// liveSaveDelay batches transcript deltas into one write.
const liveSaveDelay = time.Second

// liveEvent is the part of a Live data-channel event a call reads.
type liveEvent struct {
	typ                            string
	delta                          string
	startMs, endMs                 float64
	offsetMs                       json.RawMessage
	delegationID, delegationTarget string
}

// parseLiveEvent reads an event object as leniently as Live's clients
// did; ok is false if raw is not a JSON object.
func parseLiveEvent(raw json.RawMessage) (e liveEvent, ok bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return e, false
	}
	json.Unmarshal(fields["type"], &e.typ)
	var delta any
	if json.Unmarshal(fields["delta"], &delta) == nil {
		switch d := delta.(type) {
		case nil:
		case string:
			e.delta = d
		default:
			e.delta = string(fields["delta"])
		}
	}
	e.startMs, _ = liveNumber(fields["start_ms"])
	e.endMs, _ = liveNumber(fields["end_ms"])
	e.offsetMs = fields["offset_ms"]
	var delegation map[string]any
	json.Unmarshal(fields["delegation"], &delegation)
	e.delegationID, _ = delegation["id"].(string)
	e.delegationTarget, _ = delegation["target"].(string)
	return e, true
}

// liveNumber reads a JSON number; ok is false for anything else.
func liveNumber(raw json.RawMessage) (n float64, ok bool) {
	return n, len(raw) > 0 && raw[0] != 'n' && json.Unmarshal(raw, &n) == nil
}

type livePending struct {
	offsetMs float64
	output   bool
	settle   *time.Timer
	settleN  int // the settle timer that may commit
	deadline *time.Timer
}

// liveSubmitted is a delegation the backend took: its stable delivery ID,
// the row sequence that identifies it, and whether its reply has arrived.
type liveSubmitted struct {
	id, deliveryID string
	afterSeq       int64
	userSeq        int64 // 0 until its row arrives
	finished       bool
}

type liveInstruction struct {
	id   int64
	data []byte
}

type liveCall struct {
	id, conversationID string
	call               int64 // its number in the conversation
	deliver            liveDeliverFunc
	save               liveSaveFunc
	newDeliveryID      func() string
	logger             *slog.Logger

	mu          sync.Mutex
	active      bool
	done        chan struct{} // closed when the call ends
	fragments   []liveFragment
	transcriptN int          // accepted transcript deltas, independent of fragment trimming
	claimed     int          // start of the fresh exchange in the next task context
	taken       map[int]bool // user ordinals reserved by tasks; released on failure
	tails       []*liveTail
	seen        map[string]bool
	pending     map[string]*livePending
	order       []string // pending, oldest first
	sendTimes   []time.Time
	jobs        []liveJob
	kick        chan struct{}
	inflight    int
	handing     bool
	submitted   []*liveSubmitted
	lastSeen    int64 // the newest row Live heard about
	latest      int64
	unseen      map[int64]liveRow
	eventSeq    int
	unsaved     []liveFragment // deltas not yet handed to save
	saveTimer   *time.Timer
	saveMu      sync.Mutex // orders saves

	out    []liveInstruction
	nextID int64
	wake   chan struct{} // closed and replaced on each instruction

	streams   int
	attached  chan struct{} // closed when a newer stream attaches
	idle      *time.Timer
	idleUntil time.Time
}

// liveRow is a conversation row the call may tell Live about: its
// dialogue text ("" if none, see liveDialogue) and whole visible text.
type liveRow struct {
	seq            int64
	user           bool
	text, full     string
	liveDeliveryID string
}

// newLiveCall starts a call whose Live session already holds the
// conversation up to sequence initialSeq.
func newLiveCall(id, conversationID string, initialSeq int64, deliver liveDeliverFunc, save liveSaveFunc, logger *slog.Logger) *liveCall {
	c := &liveCall{
		id: id, conversationID: conversationID, deliver: deliver, save: save,
		logger: logger.With("liveCall", id, "conversationID", conversationID),
		active: true, done: make(chan struct{}), seen: map[string]bool{}, pending: map[string]*livePending{},
		kick: make(chan struct{}, 1), lastSeen: initialSeq, latest: initialSeq, unseen: map[int64]liveRow{},
		taken: map[int]bool{}, wake: make(chan struct{}), attached: make(chan struct{}),
		newDeliveryID: uuid.NewString,
	}
	c.idleUntil = time.Now().Add(liveIdle)
	c.idle = time.AfterFunc(liveIdle, c.expireIdle)
	go c.deliveries()
	return c
}

// handle applies Live's events, in the order Live sent them.
func (c *liveCall) handle(events []liveEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.streams == 0 {
		c.idleUntil = time.Now().Add(liveIdle)
	}
	for _, e := range events {
		if !c.active {
			return
		}
		switch e.typ {
		case "session.input_transcript.delta", "session.output_transcript.delta":
			user := e.typ == "session.input_transcript.delta"
			c.transcriptN++
			f := liveFragment{n: c.transcriptN, user: user, text: e.delta, startMs: e.startMs, endMs: e.endMs}
			c.fragments = append(c.fragments, f)
			c.unsaved = append(c.unsaved, f)
			if c.saveTimer == nil {
				c.saveTimer = time.AfterFunc(liveSaveDelay, func() { c.saveTranscript() })
			}
			c.acceptTail(f)
			if len(c.fragments) > liveFragmentsMax && len(c.pending) == 0 {
				drop := len(c.fragments) - liveFragmentsKept
				c.fragments = slices.Clone(c.fragments[drop:])
				c.claimed = max(0, c.claimed-drop)
			}
			for _, id := range c.order {
				p := c.pending[id]
				if !user {
					p.output = true
				}
				if !user || !p.output {
					c.settle(id)
				}
			}
		case "session.delegation.created":
			c.delegation(e)
		case "session.closed":
			c.endLocked("Live closed the session")
		}
	}
}

func (c *liveCall) delegation(e liveEvent) {
	id := e.delegationID
	if id == "" || e.delegationTarget != "client" || c.seen[id] {
		c.logger.Info("Live delegation ignored", "delegation", id, "target", e.delegationTarget, "seen", c.seen[id])
		return
	}
	c.seen[id] = true
	offset := 0.0
	if len(e.offsetMs) > 0 && string(e.offsetMs) != "null" {
		var ok bool
		if offset, ok = liveNumber(e.offsetMs); !ok || offset < 0 {
			c.logger.Info("Live delegation dropped: invalid offset", "delegation", id, "offset", string(e.offsetMs))
			c.error(liveInvalidOffset)
			return
		}
	}
	// A new delegation can claim late words itself. Never race a continuation
	// against its transcript snapshot.
	for _, t := range c.tails {
		if t.settle != nil {
			t.settle.Stop()
		}
	}
	p := &livePending{offsetMs: offset}
	p.deadline = time.AfterFunc(liveDeadline, func() {
		c.locked(func() {
			if !p.output {
				c.commit(id)
			}
		})
	})
	c.pending[id] = p
	c.order = append(c.order, id)
	c.activity()
	// Settle now only if the user's speech already leads up to the
	// delegation; otherwise wait for its transcript.
	user := liveRecentUser(c.unclaimedUser())
	if len(user) > 0 {
		last := user[len(user)-1]
		replyEnd := liveTurnEnd(user, c.fragments)
		if replyEnd+liveUserLeadMs >= offset {
			p.output = replyEnd > last.endMs
			c.settle(id)
		}
	}
}

func (c *liveCall) locked(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f()
}

// settle (re)starts a delegation's quiet period.
func (c *liveCall) settle(id string) {
	p := c.pending[id]
	if p.settle != nil {
		p.settle.Stop()
	}
	p.settleN++
	n := p.settleN
	p.settle = time.AfterFunc(liveQuiet, func() {
		c.locked(func() {
			if p := c.pending[id]; p != nil && p.settleN == n {
				c.commit(id)
			}
		})
	})
}

func (c *liveCall) unclaimedUser() []liveFragment {
	var user []liveFragment
	for _, f := range c.fragments {
		if f.user && !c.taken[f.n] {
			user = append(user, f)
		}
	}
	return user
}

// commit decides what a delegation becomes, once its transcript settled.
func (c *liveCall) commit(id string) {
	p := c.pending[id]
	if !c.active || p == nil {
		return
	}
	delete(c.pending, id)
	c.order = slices.DeleteFunc(c.order, func(o string) bool { return o == id })
	if p.settle != nil {
		p.settle.Stop()
	}
	p.deadline.Stop()
	defer func() {
		for _, t := range c.tails {
			c.flushContinuation(t)
		}
		c.activity()
	}()

	since := c.claimed
	speech, start, ok := liveUtterance(c.unclaimedUser(), p.offsetMs, c.fragments)
	c.claimed = len(c.fragments)
	echo := ok && liveEcho(speech, start, liveLatestReply(c.fragments))
	if !ok || echo {
		if !ok {
			c.consumeUnusableUser()
		} else {
			for _, f := range c.unclaimedUser() {
				c.taken[f.n] = true
			}
		}
		c.logger.Info("Live delegation dropped: no user request", "delegation", id, "speech", ok)
		return
	}
	now := time.Now()
	c.sendTimes = slices.DeleteFunc(c.sendTimes, func(t time.Time) bool { return now.Sub(t) >= time.Minute })
	if len(c.sendTimes) >= liveMaxPerMinute {
		c.logger.Warn("Live delegations look like a feedback loop; ending the call", "delegation", id)
		c.emit(map[string]string{"type": "stop", "message": liveFeedbackLoop})
		c.endLocked("feedback loop")
		return
	}
	c.sendTimes = append(c.sendTimes, now)
	voiceContext := liveVoiceContext(c.call, c.fragments, since)
	c.logger.Info("Live delegation committed", "delegation", id, "chars", len(speech))
	t := &liveTail{id: id, deliveryID: c.newDeliveryID(), startMs: start, offsetMs: p.offsetMs, through: c.transcriptN}
	c.tails = append(c.tails, t)
	var ordinals []int
	for _, f := range c.unclaimedUser() {
		c.taken[f.n] = true
		ordinals = append(ordinals, f.n)
	}
	// Delegations reach the backend in order. Only the original delegation
	// uses a prefix handoff; continuations carry explicit sparse ordinals.
	c.enqueue(liveJob{
		tail: t, release: ordinals,
		run: func() {
			c.deliverTask(t, "Spoken request:", speech, voiceContext, t.through, ordinals, t.deliveryID, false)
		},
	})
}

// consumeUnusableUser consumes stale speech when an unusable delegation has
// conclusively rejected it. A successful delivery's pending tail remains
// available to its continuation.
func (c *liveCall) consumeUnusableUser() {
	protected := map[int]bool{}
	for _, t := range c.tails {
		for _, f := range t.user {
			protected[f.n] = true
		}
	}
	for _, f := range c.unclaimedUser() {
		if !protected[f.n] {
			c.taken[f.n] = true
		}
	}
}

func (c *liveCall) deliveries() {
	for {
		select {
		case <-c.kick:
		case <-c.done:
		}
		for {
			c.mu.Lock()
			if len(c.jobs) == 0 {
				active := c.active
				c.mu.Unlock()
				if !active {
					return
				}
				break
			}
			job := c.jobs[0]
			c.jobs = c.jobs[1:]
			c.mu.Unlock()
			job.run()
		}
	}
}

func (c *liveCall) deliverTask(t *liveTail, heading, speech, voiceContext string, through int, ordinals []int, deliveryID string, continued bool) {
	c.mu.Lock()
	if !c.active && !continued {
		for _, n := range ordinals {
			delete(c.taken, n)
		}
		t.failed = true
		for _, tail := range c.tails {
			c.flushContinuation(tail)
		}
		c.mu.Unlock()
		return
	}
	c.inflight++
	afterSeq := c.latest
	c.activity()
	c.mu.Unlock()

	// The request points into the saved transcript, so save it first.
	receipt, err := liveReceipt{}, c.saveTranscript()
	if err == nil {
		// A delivery outlives a hang-up, as a sent chat message would.
		receipt, err = c.deliver(context.Background(), heading, speech, voiceContext, deliveryID, through)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflight--
	if err != nil {
		for _, n := range ordinals {
			delete(c.taken, n)
		}
		if !continued {
			t.failed = true
		}
		c.logger.Error("Live delegation failed", "delegation", t.id, "error", err)
		c.error(err.Error())
		if c.active {
			c.thinking(liveDeliveryFailed, &t.id)
		}
	} else {
		c.logger.Info("Live delegation delivered", "delegation", t.id, "status", receipt.Status)
		if c.active {
			c.emit(map[string]any{"type": "error", "message": nil})
			handoff := map[string]any{"type": "handoff", "through": through, "delivery_id": deliveryID}
			if continued {
				handoff["fragments"] = ordinals
			}
			c.emit(handoff)
			c.submitted = append(c.submitted, &liveSubmitted{id: t.id, deliveryID: deliveryID, afterSeq: afterSeq})
			if continued {
				c.thinking("The rest of the spoken request was passed to the backend. Do not delegate those words again: "+speech, &t.id)
			} else if receipt.Status == "queued" {
				c.thinking("This task is queued behind earlier work: "+speech, &t.id)
			} else {
				c.thinking("The backend accepted this task: "+speech, &t.id)
			}
		}
		if !continued {
			t.delivered = true
			c.flushContinuation(t)
		}
	}
	c.sync()
	c.activity()
}

// observe takes conversation rows; any after the newest Live heard about
// are told to Live, in order. Duplicates are fine.
func (c *liveCall) observe(rows []liveRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return
	}
	for _, row := range rows {
		if row.seq > c.lastSeen {
			c.unseen[row.seq] = row
			c.latest = max(c.latest, row.seq)
		}
	}
	c.sync()
}

// sync keeps Live informed of the backend's dialogue, replies scoped to the
// delegation they answer. Rows wait while a delivery is in flight: the
// delegation's own message may arrive before its receipt.
func (c *liveCall) sync() {
	if !c.active || c.inflight > 0 {
		return
	}
	var rows []liveRow
	for _, row := range c.unseen {
		rows = append(rows, row)
	}
	clear(c.unseen)
	slices.SortFunc(rows, func(a, b liveRow) int { return int(a.seq - b.seq) })
	for _, row := range rows {
		if row.seq <= c.lastSeen {
			continue
		}
		c.lastSeen = row.seq
		if row.text == "" {
			continue
		}
		if row.user {
			i := slices.IndexFunc(c.submitted, func(s *liveSubmitted) bool {
				return s.userSeq == 0 && row.seq > s.afterSeq && s.deliveryID == row.liveDeliveryID
			})
			if i >= 0 {
				c.submitted[i].userSeq = row.seq
			} else {
				c.thinking("New typed message to the backend: "+row.text, nil)
			}
			continue
		}
		var current *liveSubmitted
		for _, s := range c.submitted {
			if !s.finished && s.userSeq != 0 && row.seq > s.userSeq {
				current = s
			}
		}
		var delegation *string
		if current != nil {
			delegation = &current.id
			for _, s := range c.submitted {
				if s.userSeq != 0 && s.userSeq <= row.seq {
					s.finished = true
				}
			}
		}
		c.commentary("Backend reply: "+row.text, delegation)
	}
}

// commentary can be spoken; thinking keeps Live current without
// interrupting.
func (c *liveCall) commentary(content string, delegation *string) {
	c.appendContext("session.commentary.append", content, delegation)
}

func (c *liveCall) thinking(content string, delegation *string) {
	c.appendContext("session.thinking.append", content, delegation)
}

func (c *liveCall) appendContext(typ, content string, delegation *string) {
	c.eventSeq++
	c.emit(map[string]any{"type": "send", "event": struct {
		Type         string  `json:"type"`
		EventID      string  `json:"event_id"`
		DelegationID *string `json:"delegation_id"`
		Content      string  `json:"content"`
	}{typ, "shelley_" + strconv.Itoa(c.eventSeq), delegation, liveTruncate(content)}})
}

func (c *liveCall) error(message string) {
	c.emit(map[string]any{"type": "error", "message": message})
}

// activity reports whether a delegation is waiting for its transcript or
// to be handed to the backend, or is being handed over, when that changes.
func (c *liveCall) activity() {
	handing := len(c.pending) > 0 || len(c.jobs) > 0 || c.inflight > 0
	if c.active && handing != c.handing {
		c.handing = handing
		c.emit(map[string]any{"type": "activity", "handing_off": handing})
	}
}

func (c *liveCall) emit(instruction any) {
	data, err := json.Marshal(instruction)
	if err != nil {
		panic(err) // only fixed shapes of strings and bools
	}
	c.nextID++
	c.out = append(c.out, liveInstruction{c.nextID, data})
	if len(c.out) > liveInstructionsKept {
		c.out = slices.Clone(c.out[len(c.out)-liveInstructionsKept:])
	}
	close(c.wake)
	c.wake = make(chan struct{})
}

// instructions returns those after id, and channels that close on the
// next instruction and when the call ends.
func (c *liveCall) instructions(after int64) (out []liveInstruction, wake, done <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, in := range c.out {
		if in.id > after {
			out = append(out, in)
		}
	}
	return out, c.wake, c.done
}

// attach registers an instruction stream. Only the newest stream reads:
// superseded closes when another attaches. When the last one detaches,
// incoming events can still keep the call alive.
func (c *liveCall) attach() (superseded <-chan struct{}, detach func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streams++
	c.idle.Stop()
	close(c.attached)
	c.attached = make(chan struct{})
	return c.attached, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.streams--; c.streams == 0 && c.active {
			c.idleUntil = time.Now().Add(liveIdle)
			c.idle.Reset(liveIdle)
		}
	}
}

// Recheck under the lock: events or a reconnect can race a firing timer.
func (c *liveCall) expireIdle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active || c.streams != 0 {
		return
	}
	if remaining := time.Until(c.idleUntil); remaining > 0 {
		c.idle.Reset(remaining)
		return
	}
	c.endLocked("no instruction stream or voice events")
}

// end stops the call for good. Unsent delegations are dropped, but received
// extensions of successful deliveries drain even after the stream closes.
func (c *liveCall) end(reason string) {
	c.locked(func() { c.endLocked(reason) })
	c.saveTranscript()
}

func (c *liveCall) endLocked(reason string) {
	if !c.active {
		return
	}
	c.logger.Info("Live call ended", "reason", reason)
	c.idle.Stop()
	for _, p := range c.pending {
		if p.settle != nil {
			p.settle.Stop()
		}
		p.deadline.Stop()
	}
	clear(c.pending)
	var kept []liveJob
	for _, job := range c.jobs {
		if job.keepAfterEnd {
			kept = append(kept, job)
			continue
		}
		for _, n := range job.release {
			delete(c.taken, n)
		}
		if job.tail != nil {
			job.tail.failed = true
		}
	}
	c.jobs = kept
	for _, t := range c.tails {
		if t.settle != nil {
			t.settle.Stop()
		}
		c.flushContinuation(t)
	}
	c.active = false
	c.order = nil
	close(c.done)
	go c.saveTranscript() // end saves before returning
}

// saveTranscript saves the deltas received so far. A failed save keeps
// them for the next.
func (c *liveCall) saveTranscript() error {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	c.mu.Lock()
	batch := c.unsaved
	c.unsaved = nil
	if c.saveTimer != nil {
		c.saveTimer.Stop()
		c.saveTimer = nil
	}
	c.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := c.save(ctx, batch)
	if err != nil {
		c.logger.Error("Live call cannot save its transcript", "error", err)
		c.mu.Lock()
		c.unsaved = append(batch, c.unsaved...)
		c.mu.Unlock()
		return fmt.Errorf("Failed to save the call transcript: %w", err)
	}
	return nil
}
