package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type liveDelivery struct{ Transcript, VoiceContext, DeliveryID string }

// liveHarness is a call with a stand-in backend, on synctest's clock.
type liveHarness struct {
	t    *testing.T
	call *liveCall

	mu         sync.Mutex
	deliveries []liveDelivery
	reply      func(transcript string) (liveReceipt, error)
	headings   []string
	hold       chan struct{} // deliveries wait for it to close, if set
	deliveryN  int
	saved      []liveFragment
	saveErr    error
	// unsavedAt is, per delivery, its through and the deltas through it
	// not saved by then.
	unsavedAt [][2]int
}

func newLiveHarness(t *testing.T) *liveHarness {
	h := &liveHarness{t: t, reply: func(transcript string) (liveReceipt, error) {
		return liveReceipt{Status: "accepted"}, nil
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h.call = newLiveCall("call", "conv", 10, func(ctx context.Context, heading, transcript, voiceContext, deliveryID string, through int) (liveReceipt, error) {
		h.mu.Lock()
		unsaved := through
		for _, f := range h.saved {
			if f.n <= through {
				unsaved--
			}
		}
		h.unsavedAt = append(h.unsavedAt, [2]int{through, unsaved})
		h.deliveries = append(h.deliveries, liveDelivery{transcript, voiceContext, deliveryID})
		h.headings = append(h.headings, heading)
		hold, reply := h.hold, h.reply
		h.mu.Unlock()
		if hold != nil {
			<-hold
		}
		return reply(transcript)
	}, func(ctx context.Context, fragments []liveFragment) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.saveErr != nil {
			return h.saveErr
		}
		h.saved = append(h.saved, fragments...)
		return nil
	}, logger)
	h.call.newDeliveryID = func() string {
		h.deliveryN++
		return fmt.Sprintf("delivery-%d", h.deliveryN)
	}
	h.call.call = 1
	t.Cleanup(func() { h.call.end("test over") })
	return h
}

func (h *liveHarness) savedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.saved)
}

// set changes how the backend answers.
func (h *liveHarness) set(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f()
}

func (h *liveHarness) event(format string, args ...any) {
	h.t.Helper()
	e, ok := parseLiveEvent(fmt.Appendf(nil, format, args...))
	if !ok {
		h.t.Fatal("not an event")
	}
	h.call.handle([]liveEvent{e})
}

func (h *liveHarness) say(text string, start, end float64) {
	h.event(`{"type":"session.input_transcript.delta","delta":%q,"start_ms":%v,"end_ms":%v}`, text, start, end)
}

func (h *liveHarness) liveSays(text string, start, end float64) {
	h.event(`{"type":"session.output_transcript.delta","delta":%q,"start_ms":%v,"end_ms":%v}`, text, start, end)
}

func (h *liveHarness) delegate(id string, offset float64) {
	h.event(`{"type":"session.delegation.created","delegation":{"id":%q,"target":"client"},"offset_ms":%v}`, id, offset)
}

// advance moves the clock and lets the call act on it.
func (h *liveHarness) advance(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

func (h *liveHarness) transcripts() []string {
	synctest.Wait()
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, d := range h.deliveries {
		out = append(out, d.Transcript)
	}
	return out
}

// instructions are the call's instructions so far (see
// describeLiveInstruction).
func (h *liveHarness) instructions() []string {
	synctest.Wait()
	all, _, _ := h.call.instructions(0)
	var out []string
	for _, in := range all {
		out = append(out, describeLiveInstruction(h.t, in.data))
	}
	return out
}

// describeLiveInstruction is "<type>: <fields>", with Live's context as
// "thinking|commentary [delegation]: content".
func describeLiveInstruction(t *testing.T, data []byte) string {
	t.Helper()
	var i struct {
		Type       string  `json:"type"`
		Message    *string `json:"message"`
		HandingOff bool    `json:"handing_off"`
		Through    int     `json:"through"`
		DeliveryID string  `json:"delivery_id"`
		Event      struct {
			Type         string  `json:"type"`
			DelegationID *string `json:"delegation_id"`
			Content      string  `json:"content"`
		} `json:"event"`
	}
	if err := json.Unmarshal(data, &i); err != nil {
		t.Fatal(err)
	}
	switch i.Type {
	case "send":
		id := "-"
		if i.Event.DelegationID != nil {
			id = *i.Event.DelegationID
		}
		kind, _ := strings.CutSuffix(strings.TrimPrefix(i.Event.Type, "session."), ".append")
		return fmt.Sprintf("%s [%s]: %s", kind, id, i.Event.Content)
	case "activity":
		return fmt.Sprintf("activity: %v", i.HandingOff)
	case "handoff":
		return fmt.Sprintf("handoff %d: %s", i.Through, i.DeliveryID)
	default:
		message := "null"
		if i.Message != nil {
			message = *i.Message
		}
		return i.Type + ": " + message
	}
}

// context is what the call told Live.
func (h *liveHarness) context() []string {
	return slices.DeleteFunc(h.instructions(), func(s string) bool {
		return !strings.HasPrefix(s, "thinking") && !strings.HasPrefix(s, "commentary")
	})
}

func expectEqual[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	g, _ := json.MarshalIndent(got, "", " ")
	w, _ := json.MarshalIndent(want, "", " ")
	if string(g) != string(w) {
		t.Errorf("%s =\n%s\nwant\n%s", what, g, w)
	}
}

func TestLiveCallWaitsForLateSpeechThenDelivers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.liveSays("Hi, what can I do?", 0, 900)
		h.say("Please run", 5000, 5600)
		h.delegate("d1", 5600.4)
		h.advance(time.Second)
		// The user is still talking: the quiet period starts over.
		h.say(" the unit tests.", 6600, 7400)
		h.advance(time.Second)
		expectEqual(t, "transcripts", h.transcripts(), []string(nil))
		h.advance(200 * time.Millisecond)
		expectEqual(t, "deliveries", h.deliveries, []liveDelivery{{
			"Please run the unit tests.", exchange(1, 3, "LIVE: Hi, what can I do?\nUSER: Please run the unit tests."), "delivery-1",
		}})
		expectEqual(t, "instructions", h.instructions(), []string{
			"activity: true",
			"error: null",
			"handoff 3: delivery-1",
			"thinking [d1]: The backend accepted this task: Please run the unit tests.",
			"activity: false",
		})
	})
}

func TestLiveCallOutputDeltasExtendTheQuietPeriod(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.say("Run the tests", 0, 500)
		h.delegate("d1", 500)
		h.advance(time.Second)
		h.liveSays("I'll hand", 600, 900)
		h.advance(time.Second)
		expectEqual(t, "still acknowledging", h.transcripts(), []string(nil))
		h.liveSays(" that off.", 900, 1300)
		h.advance(liveQuiet - time.Nanosecond)
		expectEqual(t, "still settling", h.transcripts(), []string(nil))
		h.advance(time.Nanosecond)
		expectEqual(t, "deliveries", h.deliveries, []liveDelivery{{
			"Run the tests", exchange(1, 3, "USER: Run the tests\nLIVE: I'll hand that off."), "delivery-1",
		}})
		expectEqual(t, "instructions", h.instructions(), []string{
			"activity: true",
			"error: null",
			"handoff 3: delivery-1",
			"thinking [d1]: The backend accepted this task: Run the tests",
			"activity: false",
		})
	})
}

func TestLiveCallHandoffUsesSettledSnapshot(t *testing.T) {
	for _, status := range []string{"accepted", "queued"} {
		t.Run(status, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newLiveHarness(t)
				h.set(func() {
					h.hold = make(chan struct{})
					h.reply = func(string) (liveReceipt, error) {
						return liveReceipt{Status: status}, nil
					}
				})
				h.say("Run the tests", 0, 500)
				h.delegate("d1", 500)
				// Live's acknowledgement keeps resetting quiet.
				for i := range 4 {
					h.advance(time.Second)
					h.liveSays(" still speaking", float64(600+i*500), float64(1000+i*500))
				}
				h.advance(liveQuiet)
				expectEqual(t, "settled delivery", h.transcripts(), []string{"Run the tests"})
				expectEqual(t, "no receipt yet", h.instructions(), []string{"activity: true"})

				// These arrive after the snapshot but before the backend receipt.
				h.liveSays(" finished.", 3000, 3400)
				h.say("Also lint it", 4000, 4500)
				h.set(func() { close(h.hold) })
				synctest.Wait()
				if strings.Contains(h.deliveries[0].VoiceContext, "finished.") || strings.Contains(h.deliveries[0].VoiceContext, "Also lint it") {
					t.Errorf("late fragments entered earlier snapshot: %q", h.deliveries[0].VoiceContext)
				}
				accepted := "The backend accepted this task: "
				if status == "queued" {
					accepted = "This task is queued behind earlier work: "
				}
				expectEqual(t, "snapshot receipt", h.instructions(), []string{
					"activity: true",
					"error: null",
					"handoff 5: delivery-1",
					"thinking [d1]: " + accepted + "Run the tests",
					"activity: false",
				})
				// The unconsumed tail is available for the next delegation.
				h.delegate("d2", 4500)
				h.advance(liveQuiet)
				expectEqual(t, "transcripts", h.transcripts(), []string{"Run the tests", "Also lint it"})
				if !strings.Contains(h.deliveries[1].VoiceContext, "LIVE: finished.\nUSER: Also lint it") {
					t.Errorf("later exchange lost: %q", h.deliveries[1].VoiceContext)
				}
				expectEqual(t, "next receipt", h.instructions()[5:], []string{
					"activity: true",
					"error: null",
					"handoff 7: delivery-2",
					"thinking [d2]: " + accepted + "Also lint it",
					"activity: false",
				})
			})
		})
	}
}

func TestLiveCallHandoffCountSurvivesFragmentCompaction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		for i := range liveFragmentsMax + 1 {
			h.liveSays("", float64(i), float64(i))
		}
		h.say("Run the tests", 2000, 2500)
		expectEqual(t, "compacted fragments", len(h.call.fragments), liveFragmentsKept+1)
		h.delegate("d1", 2500)
		h.advance(liveQuiet)
		expectEqual(t, "instructions", h.instructions(), []string{
			"activity: true",
			"error: null",
			fmt.Sprintf("handoff %d: delivery-1", liveFragmentsMax+2),
			"thinking [d1]: The backend accepted this task: Run the tests",
			"activity: false",
		})
	})
}

func TestLiveCallDeliversAtTheDeadlineWhenSpeechNeverPauses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.say("Check", 0, 300)
		h.delegate("d1", 300)
		for i := 1; i <= 10; i++ {
			h.advance(600 * time.Millisecond)
			h.say(" more", float64(300+i*600), float64(600+i*600))
		}
		if got := h.transcripts(); len(got) != 1 || !strings.HasPrefix(got[0], "Check more more more more more more more more") {
			t.Errorf("transcripts = %q", got)
		}
	})
}

func TestLiveCallUserCaptionsDoNotExtendResponseDebounce(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(fmt.Sprintf("Live responds before delegation: %v", before), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newLiveHarness(t)
				h.say("Run tests", 0, 400)
				if before {
					h.liveSays("I'll run them.", 600, 1000)
				}
				h.delegate("d1", 1000)
				if !before {
					h.liveSays("I'll run them.", 600, 1000)
				}
				h.advance(time.Second)
				h.say(" in server", 400, 500)
				h.advance(liveQuiet - time.Second)
				expectEqual(t, "user words captured without extending response quiet", h.transcripts(), []string{"Run tests in server"})
			})
		})
	}
}

func TestLiveCallWaitsForLiveResponseToSettlePastDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.say("Run the tests", 0, 500)
		h.delegate("d1", 500)
		var response strings.Builder
		for i := range 6 {
			h.advance(time.Second)
			part := fmt.Sprintf("part %d %s", i, strings.Repeat("words ", 200))
			response.WriteString(part)
			h.liveSays(part, float64(600+i*1000), float64(1500+i*1000))
			expectEqual(t, "Live is still speaking", h.transcripts(), []string(nil))
		}
		h.liveSays("finished.", 6500, 7000)
		response.WriteString("finished.")
		h.advance(liveQuiet - time.Nanosecond)
		expectEqual(t, "still settling", h.transcripts(), []string(nil))
		h.advance(time.Nanosecond)
		expectEqual(t, "complete exchange", h.deliveries, []liveDelivery{{
			"Run the tests", exchange(1, 8, "USER: Run the tests\nLIVE: "+response.String()), "delivery-1",
		}})
	})
}

func TestLiveCallWaitsForTranscriptThatFollowsTheDelegation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.delegate("d1", 900)
		h.advance(2 * time.Second) // no speech yet: no quiet period runs
		h.say("Run the tests", 0, 900)
		h.advance(1100 * time.Millisecond)
		expectEqual(t, "transcripts", h.transcripts(), []string(nil))
		h.advance(100 * time.Millisecond)
		expectEqual(t, "transcripts", h.transcripts(), []string{"Run the tests"})
	})
}

func TestLiveCallNeverDeliversMissingOrEchoedSpeech(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		// Live created a delegation but no transcript arrived.
		h.delegate("d1", 1000)
		h.advance(liveDeadline)
		// The microphone picked up Live's own reply.
		h.liveSays("Shelley is working on that request now", 6000, 8000)
		h.say("Shelley is working on that request", 6100, 8000)
		h.delegate("d2", 8000)
		h.advance(liveQuiet)
		expectEqual(t, "transcripts", h.transcripts(), []string(nil))
		expectEqual(t, "context", h.context(), []string{})
		expectEqual(t, "instructions", h.instructions(), []string{
			"activity: true",
			"activity: false",
			"activity: true",
			"activity: false",
		})
	})
}

func TestLiveCallCountsOnlyNewClientDelegations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.say("Run the tests", 0, 500)
		h.event(`{"type":"session.delegation.created","delegation":{"id":"d1","target":"server"},"offset_ms":500}`)
		h.event(`{"type":"session.delegation.created","delegation":{"target":"client"},"offset_ms":500}`)
		h.delegate("d2", 500)
		h.delegate("d2", 500) // Live may repeat an event
		h.advance(6 * time.Second)
		expectEqual(t, "context", h.context(), []string{"thinking [d2]: The backend accepted this task: Run the tests"})
	})
}

func TestLiveCallReportsBadDelegations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.say("Run the tests", 0, 500)
		h.event(`{"type":"session.delegation.created","delegation":{"id":"d1","target":"client"},"offset_ms":-1}`)
		h.event(`{"type":"session.delegation.created","delegation":{"id":"d2","target":"client"},"offset_ms":"soon"}`)
		// A missing offset counts from the start of the session.
		h.event(`{"type":"session.delegation.created","delegation":{"id":"d3","target":"client"}}`)
		h.advance(liveQuiet)
		expectEqual(t, "instructions", h.instructions(), []string{
			"error: " + liveInvalidOffset,
			"error: " + liveInvalidOffset,
			"activity: true",
			"error: null",
			"handoff 1: delivery-1",
			"thinking [d3]: The backend accepted this task: Run the tests",
			"activity: false",
		})
	})
}

func TestLiveCallTellsLiveWhatBecameOfADelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.set(func() {
			h.reply = func(transcript string) (liveReceipt, error) {
				return liveReceipt{Status: "queued"}, nil
			}
		})
		h.say("Run the tests", 0, 500)
		h.delegate("d1", 500)
		h.advance(liveQuiet)
		h.set(func() {
			h.reply = func(string) (liveReceipt, error) {
				return liveReceipt{}, errors.New("Failed to deliver speech to Shelley: conversation is archived")
			}
		})
		h.say("Lint it", 3000, 3500)
		h.delegate("d2", 3500)
		h.advance(liveQuiet)
		expectEqual(t, "instructions", h.instructions(), []string{
			"activity: true",
			"error: null",
			"handoff 1: delivery-1",
			"thinking [d1]: This task is queued behind earlier work: Run the tests",
			"activity: false",
			"activity: true",
			"error: Failed to deliver speech to Shelley: conversation is archived",
			"thinking [d2]: " + liveDeliveryFailed,
			"activity: false",
		})
		expectEqual(t, "context", h.context()[0], "thinking [d1]: This task is queued behind earlier work: Run the tests")
	})
}

func TestLiveCallTellsLiveTheReplyToItsTaskAndOtherTypedMessages(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		row := func(seq int64, user bool, text string) liveRow {
			return liveRow{seq: seq, user: user, text: text, full: text}
		}
		// History before the call is already in Live's session.
		h.call.observe([]liveRow{row(9, true, "old"), row(10, false, "old reply")})
		h.say("Run the tests", 0, 500)
		h.delegate("d1", 500)
		h.set(func() { h.hold = make(chan struct{}) })
		h.advance(liveQuiet)
		// The task's message arrives while its delivery is in flight; Live
		// must not hear it as a typed message.
		task := row(11, true, "Run the tests")
		task.liveDeliveryID = "delivery-1"
		h.call.observe([]liveRow{task})
		h.set(func() { close(h.hold) })
		synctest.Wait()
		h.call.observe([]liveRow{{seq: 12, full: "working…"}, row(13, false, "All 40 tests pass.")})
		h.call.observe([]liveRow{row(14, true, "also lint it"), row(15, false, "Lint is clean.")})
		h.call.observe([]liveRow{row(15, false, "Lint is clean.")})
		expectEqual(t, "context", h.context(), []string{
			"thinking [d1]: The backend accepted this task: Run the tests",
			"commentary [d1]: Backend reply: All 40 tests pass.",
			"thinking [-]: New typed message to the backend: also lint it",
			"commentary [-]: Backend reply: Lint is clean.",
		})
	})
}

func TestLiveCallEndsARunawayDelegationLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		for i := range 13 {
			at := float64(i * 2000)
			h.say(fmt.Sprintf("Request number %d", i), at, at+500)
			h.delegate(fmt.Sprintf("d%d", i), at+500)
			h.advance(2 * time.Second)
		}
		if n := len(h.transcripts()); n != liveMaxPerMinute {
			t.Errorf("%d deliveries", n)
		}
		if got := h.instructions(); got[len(got)-1] != "stop: "+liveFeedbackLoop {
			t.Errorf("last instruction = %q", got[len(got)-1])
		}
		select {
		case <-h.call.done:
		default:
			t.Error("the call goes on")
		}
	})
}

func TestLiveCallHangUpDropsPendingAndInFlightDelegations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.say("Run the tests", 0, 500)
		h.delegate("d1", 500)
		h.set(func() { h.hold = make(chan struct{}) })
		h.advance(liveQuiet)
		h.say("and lint", 2000, 2500)
		h.delegate("d2", 2500)
		h.call.end("hung up")
		h.set(func() { close(h.hold) })
		h.advance(10 * time.Second)
		expectEqual(t, "transcripts", h.transcripts(), []string{"Run the tests"})
		expectEqual(t, "context", len(h.context()), 0)
		expectEqual(t, "no handoff after hangup", h.instructions(), []string{"activity: true"})
	})
}

func TestLiveCallEndsWhenLiveCloses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.say("Run the tests", 0, 500)
		h.delegate("d1", 500)
		h.event(`{"type":"session.closed"}`)
		h.advance(liveDeadline)
		expectEqual(t, "transcripts", h.transcripts(), []string(nil))
	})
}

// A real call: Live read out the backend's questions, the user answered
// "New chats", then "Hello"; Live said it was handing that off and
// delegated as it finished speaking, seconds after the user stopped. The
// delegation must still carry the answer.
func TestLiveCallDeliversAShortAnswerLiveAcknowledgedBeforeDelegating(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.liveSays("Should they also start from a new or draft chat, like the web allows?", 0, 4000)
		h.liveSays(" Archived chats: should archiving the chat end the call?", 4000, 7000)
		h.say("New chats", 9000, 9800)
		h.say(" Hello", 12000, 12500)
		h.liveSays("I'm here, handing those two decisions off to get built.", 13500, 17500)
		h.delegate("d1", 17600)
		h.advance(liveQuiet)
		expectEqual(t, "transcripts", h.transcripts(), []string{"New chats Hello"})
	})
}

func TestLiveCallDeliversClarificationAfterDelayedAcknowledgement(t *testing.T) {
	for _, tt := range []struct {
		late  bool
		extra float64
	}{
		{false, 0}, {true, 0}, {false, 10000}, {true, 10000},
	} {
		t.Run(fmt.Sprintf("late transcript: %v, extra answer duration: %v", tt.late, tt.extra), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newLiveHarness(t)
				h.liveSays("Which bit's misbehaving?", 78000, 79600)
				captions := func() {
					h.say("Yeah, it often uses ", 82600, 83600+tt.extra)
					h.say("find double ", 83600+tt.extra, 84200+tt.extra)
					h.say("dot", 84200+tt.extra, 84400+tt.extra)
				}
				if !tt.late {
					captions()
					h.liveSays("Got it, I'll pass that along.", 87800+tt.extra, 89800+tt.extra)
				}
				h.delegate("d1", 89900+tt.extra)
				if tt.late {
					h.liveSays("Got it, I'll pass that along.", 87800+tt.extra, 89800+tt.extra)
					captions()
				}
				h.advance(liveQuiet)
				expectEqual(t, "clarification delivered at output quiet", h.transcripts(), []string{"Yeah, it often uses find double dot"})
				h.delegate("d1", 89900+tt.extra)
				h.delegate("d2", 90000+tt.extra)
				h.advance(liveDeadline)
				expectEqual(t, "no duplicate handoff", h.transcripts(), []string{"Yeah, it often uses find double dot"})
				expectEqual(t, "no spurious repeat prompt", h.context(), []string{
					"thinking [d1]: The backend accepted this task: Yeah, it often uses find double dot",
				})
			})
		})
	}
}

func TestLiveCallKeepsAQuickClarificationWithItsRequest(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("late answer captions: %v", late), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newLiveHarness(t)
				h.say("Find the files that use parent dirs.", 74000, 77000)
				h.liveSays("Which bit's misbehaving?", 77600, 79000)
				captions := func() {
					h.say(" Yeah, it often uses ", 81500, 82500)
					h.say("find double ", 82500, 83100)
					h.say("dot", 83100, 83300)
				}
				if !late {
					captions()
					h.liveSays("Okay, I'm looking at that now.", 86000, 88000)
				}
				h.delegate("d1", 88100)
				if late {
					h.liveSays("Okay, I'm looking at that now.", 86000, 88000)
					captions()
				}
				h.advance(liveQuiet)
				const speech = "Find the files that use parent dirs. Yeah, it often uses find double dot"
				expectEqual(t, "request and clarification delivered together at output quiet", h.transcripts(), []string{speech})
				h.delegate("d1", 88100)
				h.delegate("d2", 88200)
				h.advance(liveDeadline)
				expectEqual(t, "one combined handoff", h.transcripts(), []string{speech})
				expectEqual(t, "no silent drop or repeat prompt", h.context(), []string{
					"thinking [d1]: The backend accepted this task: " + speech,
				})
			})
		})
	}
}

// Choosing an option by repeating Live's words is not echo: echo is Live's
// playback picked up while it speaks, not speech after it stopped.
func TestLiveCallDeliversAnOptionRepeatedAfterLiveStopped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.liveSays("Should calls also start from a new or draft chat?", 0, 4000)
		h.say("calls also start from a new or draft chat", 7000, 9000)
		h.delegate("d1", 9100)
		h.advance(liveQuiet)
		expectEqual(t, "transcripts", h.transcripts(), []string{"calls also start from a new or draft chat"})
	})
}

func TestLiveCallDeliversAnAnswerLiveRepeatsInAcknowledgement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.liveSays("Which bit's misbehaving?", 78000, 79600)
		h.say("Yeah, it often uses ", 82600, 83600)
		h.say("find double ", 83600, 84200)
		h.say("dot", 84200, 84400)
		h.liveSays("Yeah, it often uses find double dot. I'll pass that along.", 87800, 89800)
		h.delegate("d1", 89900)
		h.advance(liveQuiet)
		expectEqual(t, "answer predates acknowledgement, not microphone echo", h.transcripts(), []string{"Yeah, it often uses find double dot"})
	})
}

func TestLiveCallDoesNotReviveAGreetingAfterLiveSpokeFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.liveSays("Hello, how can I help?", 0, 1500)
		h.say("Hello", 2000, 2500)
		h.liveSays("What would you like to work on?", 8000, 9000)
		h.delegate("d1", 9100)
		h.advance(liveDeadline)
		expectEqual(t, "stale greeting is not a request", h.transcripts(), []string(nil))
		expectEqual(t, "no spurious repeat prompt", h.context(), []string{})
	})
}

func TestLiveCallOldResponseDoesNotDisablePendingCapture(t *testing.T) {
	for _, lateInput := range []bool{true, false} {
		t.Run(fmt.Sprintf("late input: %v", lateInput), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newLiveHarness(t)
				h.say("Hello", 0, 500)
				h.liveSays("Hi, how can I help?", 600, 1500)
				h.delegate("d1", 9100)
				if lateInput {
					h.say("Fix the login bug", 8000, 9000)
					h.advance(liveQuiet)
					expectEqual(t, "late request", h.transcripts(), []string{"Fix the login bug"})
				} else {
					h.advance(liveDeadline)
					expectEqual(t, "no request", h.transcripts(), []string(nil))
				}
				expectEqual(t, "delegation resolved", len(h.call.pending), 0)
			})
		})
	}
}

// The transcript is saved in batches as the call goes on, and through a
// delegation's speech before the request pointing into it is delivered.
func TestLiveCallSavesTranscript(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.liveSays("Hi, what can I do?", 0, 900)
		h.say("Please run", 5000, 5600)
		h.advance(liveSaveDelay - time.Millisecond)
		expectEqual(t, "saved before the delay", h.savedCount(), 0)
		h.advance(time.Millisecond)
		expectEqual(t, "saved after the delay", h.savedCount(), 2)

		h.delegate("d1", 5600.4)
		h.say(" the unit tests.", 5600, 6400)
		h.advance(liveQuiet)
		expectEqual(t, "through, unsaved at delivery", h.unsavedAt, [][2]int{{3, 0}})

		h.liveSays("On it.", 7000, 7400)
		h.call.end("hung up")
		expectEqual(t, "saved at hang-up", h.savedCount(), 4)
	})
}

// A request is not delivered pointing at a transcript that failed to save;
// the deltas are kept for the next save.
func TestLiveCallTranscriptSaveFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.set(func() { h.saveErr = errors.New("disk full") })
		h.say("Run the tests.", 0, 800)
		h.delegate("d1", 800)
		h.advance(liveQuiet)
		expectEqual(t, "transcripts", h.transcripts(), []string(nil))
		if got := h.instructions(); !slices.Contains(got, "error: Failed to save the call transcript: disk full") {
			t.Fatalf("instructions = %q", got)
		}
		h.set(func() { h.saveErr = nil })
		h.call.end("hung up")
		expectEqual(t, "saved after recovery", h.savedCount(), 1)
	})
}

func TestLiveCallBackgroundReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		_, detach := h.call.attach()
		detach()
		h.advance(4 * time.Minute)
		h.say("still on the call", 0, 1000)
		h.advance(4 * time.Minute)
		_, detach = h.call.attach()
		h.advance(liveIdle)
		select {
		case <-h.call.done:
			t.Fatal("call expired despite relay activity and reconnection")
		default:
		}
		detach()
		h.advance(liveIdle)
		select {
		case <-h.call.done:
		default:
			t.Fatal("disconnected call never expired")
		}
	})
}
