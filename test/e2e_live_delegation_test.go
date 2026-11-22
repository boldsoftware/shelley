package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/live-delegation.spec.ts.

// liveVoiceJS fakes a voice call: a microphone (denied, or held until
// window.__releaseVoiceMicrophone(), as window.__VOICE_MOCK__ says) whose
// stopped tracks window.__voiceTracksStopped counts, and an RTCPeerConnection
// whose data channel, window.__mockLive, starts the session when connected,
// delivers what emit(event) is given, and keeps what Shelley sends.
const liveVoiceJS = `(() => {
  const { denied, held } = window.__VOICE_MOCK__;
  window.__voiceTracksStopped = 0;
  const track = { kind: 'audio', enabled: true, stop() { window.__voiceTracksStopped++; } };
  Object.defineProperty(navigator.mediaDevices, 'getUserMedia', {
    value: async () => {
      if (denied) throw new DOMException('Microphone denied', 'NotAllowedError');
      if (held) await new Promise((resolve) => { window.__releaseVoiceMicrophone = resolve; });
      return { getAudioTracks: () => [track], getTracks: () => [track] };
    },
  });
  class Channel {
    readyState = 'open';
    onmessage = null;
    onclose = null;
    sent = [];
    send(raw) { this.sent.push(JSON.parse(raw)); }
    emit(event) { this.onmessage?.({ data: JSON.stringify(event) }); }
    close() { this.readyState = 'closed'; this.onclose?.(); }
  }
  class Peer {
    iceGatheringState = 'complete';
    connectionState = 'connected';
    localDescription = null;
    channel = new Channel();
    addTrack() {}
    createDataChannel() { window.__mockLive = this.channel; return this.channel; }
    createOffer() { return Promise.resolve({ type: 'offer', sdp: 'v=0\r\nm=audio test' }); }
    setLocalDescription(description) { this.localDescription = description; return Promise.resolve(); }
    setRemoteDescription() {
      queueMicrotask(() => this.channel.emit({ type: 'session.started', session: { id: 'live_test' } }));
      return Promise.resolve();
    }
    addEventListener() {}
    close() { this.channel.close(); }
  }
  Object.defineProperty(window, 'RTCPeerConnection', { value: Peer });
  window.liveEmit = (event) => window.__mockLive.emit(event);
  window.liveNotes = () => JSON.stringify(window.__mockLive.sent);
  // Live delegates a client task after this speech; Shelley settles the
  // transcript and submits it.
  window.liveSpeak = (id, speech) => {
    window.liveEmit({ type: 'session.input_transcript.delta', delta: speech, start_ms: 0, end_ms: 900 });
    window.liveEmit({ type: 'session.delegation.created', offset_ms: 900, delegation: { id, target: 'client' } });
  };
})();`

// mockVoice installs a fake clock, as the spec's page.clock.install did, and
// the fake call.
func (s *shelley) mockVoice(denied, held bool) {
	s.clock(time.Time{})
	s.initScript(fmt.Sprintf("window.__VOICE_MOCK__ = {denied: %t, held: %t};\n%s", denied, held, liveVoiceJS))
}

const liveTerms = `The voice call is faked. In an eval, window.liveEmit(event) delivers event from Live (e.g. window.liveEmit({type: "session.input_transcript.delta", delta: "Hello ", start_ms: 0, end_ms: 400})); emit each event in its own eval, in the order given. window.liveSpeak(id, speech) speaks and delegates: it emits {type: "session.input_transcript.delta", delta: speech, start_ms: 0, end_ms: 900} and then {type: "session.delegation.created", offset_ms: 900, delegation: {id, target: "client"}}. window.liveNotes() returns the JSON of everything Shelley sent to Live. window.__voiceTracksStopped counts the microphone tracks stopped. "Start the call" means: click data-testid "live-call-header"; data-testid "live-bar" should contain "On call". "The exchange" is data-testid "live-exchange". "The message X" is the [data-testid="message"] elements whose text contains X (has_text; it collapses whitespace, so a line break is a space). Text checks on messages and the exchange compare whitespace-collapsed text: eval el.textContent.replace(/\s+/g, " ").includes(...) with expect true (or false for "not contain"), on every matching element. `

// liveRecorder records what the page asks of the server during a call.
type liveRecorder struct {
	s        *shelley
	mu       sync.Mutex
	sessions []liveSession // POST .../live-session
	relayed  [][]map[string]any
	hangups  []int // status of each DELETE .../live-session/{session}
}

type liveSession struct {
	Conversation string
	Body         map[string]any
}

// eventsOf is the events a relay request carries.
func eventsOf(body map[string]any) []map[string]any {
	var events []map[string]any
	list, _ := body["events"].([]any)
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			events = append(events, m)
		}
	}
	return events
}

// recordLive records call starts and relayed events; relay, when set,
// answers relay requests instead of the server.
func (s *shelley) recordLive(relay func(w http.ResponseWriter, r *http.Request, events []map[string]any)) *liveRecorder {
	l := &liveRecorder{s: s}
	s.route("POST /api/conversation/{id}/live-session", func(w http.ResponseWriter, r *http.Request) {
		body := object(readBody(r))
		l.mu.Lock()
		l.sessions = append(l.sessions, liveSession{r.PathValue("id"), body})
		l.mu.Unlock()
		s.pass.ServeHTTP(w, r)
	})
	s.route("POST /api/conversation/{id}/live-session/{session}/events", func(w http.ResponseWriter, r *http.Request) {
		body := object(readBody(r))
		events := eventsOf(body)
		l.mu.Lock()
		l.relayed = append(l.relayed, events)
		l.mu.Unlock()
		if relay != nil {
			relay(w, r, events)
			return
		}
		s.pass.ServeHTTP(w, r)
	})
	return l
}

// recordHangups records the status of each hangup; fail, when set, says
// whether to answer it 503 "Hangup failed for test" instead.
func (l *liveRecorder) recordHangups(fail func() bool) {
	l.s.route("DELETE /api/conversation/{id}/live-session/{session}", func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		if fail != nil && fail() {
			rec.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(rec, "Hangup failed for test")
		} else {
			l.s.pass.ServeHTTP(rec, r)
		}
		l.mu.Lock()
		l.hangups = append(l.hangups, rec.status)
		l.mu.Unlock()
	})
	l.s.call("hangup", func() string {
		l.mu.Lock()
		defer l.mu.Unlock()
		if len(l.hangups) == 0 {
			return "no hangup yet"
		}
		if code := l.hangups[0]; code/100 != 2 {
			return "status " + strconv.Itoa(code)
		}
		return "ok"
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// conversationJSON is what GET /api/conversation/{id} says.
func (s *shelley) conversationJSON(id string) string {
	return string(s.do("GET", "/api/conversation/"+id, nil))
}

// liveHistory is a conversation's messages as the API returns them.
type liveHistory struct {
	Messages []liveMessage `json:"messages"`
}

type liveMessage struct {
	Type       string          `json:"type"`
	EndOfTurn  bool            `json:"end_of_turn"`
	UserData   json.RawMessage `json:"user_data"`
	SequenceID int64           `json:"sequence_id"`
}

func (s *shelley) liveHistory(id string) liveHistory {
	var h liveHistory
	s.api("GET", "/api/conversation/"+id, nil, &h)
	return h
}

func (h liveHistory) count(f func(typ string, endOfTurn bool) bool) int {
	n := 0
	for _, m := range h.Messages {
		if f(m.Type, m.EndOfTurn) {
			n++
		}
	}
	return n
}

// callLiveTranscripts has call "livetranscript rows" say whether the
// conversation has any livetranscript message.
func (s *shelley) callLiveTranscripts(id string) {
	s.call("livetranscript rows", func() string {
		return strconv.FormatBool(s.liveHistory(id).count(func(typ string, _ bool) bool { return typ == "livetranscript" }) > 0)
	})
}

// intercepting has a route act only after call "intercept", as the spec
// routed requests only once the call was up.
func (s *shelley) intercepting() *atomic.Bool {
	var on atomic.Bool
	s.call("intercept", func() string {
		on.Store(true)
		return "intercepting"
	})
	return &on
}

func TestE2ELiveDelegationQuestionAndReply(t *testing.T) {
	e2e(t, useful(0), `A delegated question reaches the chat, and its reply reaches Live. `+liveTerms+`Call "sessions" describes the call starts (POST /api/conversation/<id>/live-session) as JSON; call "relayed session.started" says whether any event of type "session.started" was relayed to the server. Perform the steps in order.
1. Navigate to /c/ready. data-testid "live-call-header" should have aria-label exactly "Start voice conversation".
2. Click it. data-testid "live-bar" should contain "On call"; the header button's aria-label should become exactly "End voice call", and its computed color should equal the computed backgroundColor of data-testid "live-stop" (eval the comparison, expect true). data-testid "live-timer" should be visible.
3. The strip sits by the composer, with a wide meter and no captions: the live bar's getBoundingClientRect().y should be >= that of ".message-input-container" (eval, expect true); data-testid "live-meter" should be at least 110px wide (eval, expect true); there should be no data-testid "live-transcript" (count 0).
4. Emit, in order: {type: "session.input_transcript.delta", delta: "Hello ", start_ms: 0, end_ms: 400}; {type: "session.output_transcript.delta", delta: "Hey there!", start_ms: 500, end_ms: 900}; {type: "session.input_transcript.delta", delta: "echo: why is this so slow", start_ms: 1200, end_ms: 1900}; {type: "session.started"} (Shelley doesn't need it); and then twice, as Live may repeat an event, {type: "session.delegation.created", offset_ms: 1900, delegation: {id: "captured_question", target: "client"}}.
5. There should be exactly one message "Spoken request: Hello echo: why is this so slow", and it should contain "Call #1 Voice Exchange [Captions 1-3] (for context only): USER: Hello LIVE: Hey there! USER: echo: why is this so slow".
6. Call "sessions" should return exactly {"count":1,"conversation_options":false}, and call "relayed session.started" exactly "false".
7. window.liveNotes() should come to include "The backend accepted this task: Hello echo: why is this so slow", and also '"delegation_id":"captured_question","content":"Backend reply: edit predictable.go' (eval includes, expect true).
8. Click the header button again. The live bar should become hidden, and the header button's aria-label exactly "Start voice conversation". window.liveNotes() should include "session.close", window.__voiceTracksStopped should be exactly 1, and there should still be exactly one message "Spoken request: Hello echo: why is this so slow".`, func(s *shelley) {
		s.conversation("echo: ready", "ready")
		s.mockVoice(false, false)
		l := s.recordLive(nil)
		s.call("sessions", func() string {
			l.mu.Lock()
			defer l.mu.Unlock()
			options := false
			if len(l.sessions) > 0 {
				_, options = l.sessions[0].Body["conversation_options"]
			}
			return fmt.Sprintf(`{"count":%d,"conversation_options":%t}`, len(l.sessions), options)
		})
		s.call("relayed session.started", func() string {
			l.mu.Lock()
			defer l.mu.Unlock()
			for _, events := range l.relayed {
				for _, e := range events {
					if e["type"] == "session.started" {
						return "true"
					}
				}
			}
			return "false"
		})
	})
}

func TestE2ELiveDelegationSentExchangesStayFrozen(t *testing.T) {
	e2e(t, useful(0), `Sent exchanges stay frozen while new Live speech waits for the next handoff. `+liveTerms+`Call "snapshot" saves the conversation as the server has it; call "unchanged" says whether it is still exactly the same; call "user messages added" counts the user messages added since the snapshot. Perform the steps in order.
1. Navigate to /c/transcript and start the call. Call "snapshot".
2. Emit {type: "session.input_transcript.delta", delta: "Hello.", start_ms: 0, end_ms: 400}, then {type: "session.output_transcript.delta", delta: "Hi, I'm ", start_ms: 500, end_ms: 900}. The exchange should contain "Hi, I'm".
3. Emit {type: "session.output_transcript.delta", delta: "here.", start_ms: 900, end_ms: 1100}. The exchange should contain "Hi, I'm here." and "Not sent to Shelley", and its className must not match /message-user/. Call "unchanged" should return exactly "true".
4. Emit {type: "session.input_transcript.delta", delta: "echo: check the transcript", start_ms: 1500, end_ms: 2200}, then {type: "session.delegation.created", offset_ms: 2200, delegation: {id: "transcript", target: "client"}}, then {type: "session.output_transcript.delta", delta: "Okay, let", start_ms: 2300, end_ms: 2600}. The exchange should contain "Okay, let".
5. Emit {type: "session.output_transcript.delta", delta: " me check.", start_ms: 2600, end_ms: 3000}. There should be exactly one message "Spoken request: Hello.echo: check the transcript" (call it the first request); it should contain "LIVE: Okay, let me check.". The exchange should become hidden.
6. Later speech stays unsent; it belongs to the next handoff, not this bubble: emit {type: "session.output_transcript.delta", delta: " I'm still here while that runs.", start_ms: 3500, end_ms: 4500}. The exchange should contain "I'm still here while that runs." and "Not sent to Shelley"; the first request must not contain "I'm still here while that runs.". Call "user messages added" should return exactly "1".
7. Emit {type: "session.input_transcript.delta", delta: "echo: now lint", start_ms: 5000, end_ms: 6000}, then {type: "session.delegation.created", offset_ms: 6000, delegation: {id: "next-transcript", target: "client"}}. There should be exactly one message "Spoken request: echo: now lint" (the next request), containing "LIVE: I'm still here while that runs.". The exchange should become hidden, and the first request must still not contain "I'm still here while that runs.". Call "livetranscript rows" should return exactly "false".
8. Click data-testid "live-stop", then navigate to /c/transcript again (a reload). There should be exactly one first request, still without "I'm still here while that runs.", and the next request should contain "LIVE: I'm still here while that runs.".`, func(s *shelley) {
		c := s.conversation("echo: transcript ready", "transcript")
		s.mockVoice(false, false)
		var before string
		var beforeUsers int
		users := func() int {
			return s.liveHistory(c.ID).count(func(typ string, _ bool) bool { return typ == "user" })
		}
		s.call("snapshot", func() string {
			before = s.conversationJSON(c.ID)
			beforeUsers = users()
			return "saved"
		})
		s.call("unchanged", func() string { return strconv.FormatBool(s.conversationJSON(c.ID) == before) })
		s.call("user messages added", func() string { return strconv.Itoa(users() - beforeUsers) })
		s.callLiveTranscripts(c.ID)
	})
}

func TestE2ELiveDelegationHangupKeepsSentRequest(t *testing.T) {
	e2e(t, useful(0), `Hangup does not append unsent captions to a sent request. `+liveTerms+`Once call "intercept" has run, the server's answer to a relay of events (POST .../live-session/<session>/events) carrying a delta "The live chat" is held back until call "release"; call "held" says whether one is being held. Perform the steps in order.
1. Navigate to /c/final and start the call.
2. Call window.liveSpeak("hangup", "echo: save the whole transcript"). There should be exactly one message "Spoken request: echo: save the whole transcript" (the request).
3. Call "intercept". Emit {type: "session.output_transcript.delta", delta: "The live chat", start_ms: 1000, end_ms: 1800}. Call "held" should come to return exactly "true".
4. Emit {type: "session.output_transcript.delta", delta: " panel doesn't get too tall.", start_ms: 1800, end_ms: 3000}. The exchange should contain "The live chat panel doesn't get too tall.", and the request must not contain "The live chat".
5. Click data-testid "live-stop", then call "release". data-testid "live-bar" should be gone (count 0), and the request must still not contain "The live chat".
6. Navigate to /c/final again (a reload). There should again be exactly one message "Spoken request: echo: save the whole transcript" (the request), and it must not contain "The live chat". Call "livetranscript rows" should return exactly "false".`, func(s *shelley) {
		c := s.conversation("echo: final transcript ready", "final")
		s.mockVoice(false, false)
		on := s.intercepting()
		wait := s.gate("release").wait
		var held atomic.Bool
		s.recordLive(func(w http.ResponseWriter, r *http.Request, events []map[string]any) {
			if on.Load() && slices.ContainsFunc(events, func(e map[string]any) bool { return e["delta"] == "The live chat" }) {
				held.Store(true)
				if !wait(r) {
					return
				}
			}
			s.pass.ServeHTTP(w, r)
		})
		s.call("held", func() string { return strconv.FormatBool(held.Load()) })
		s.callLiveTranscripts(c.ID)
	})
}

func TestE2ELiveDelegationBoundedExchange(t *testing.T) {
	e2e(t, useful(0), `Live groups overlapping thoughts and keeps the exchange bounded with optional scrollback. `+liveTerms+`"The turns" are the exchange's ".live-exchange-turn" elements; "the region" is the exchange's [role="region"][aria-label="Live exchange transcript"]; "the distance from the bottom" is the region's scrollHeight - clientHeight - scrollTop. Perform the steps in order.
1. Navigate to /c/overlap and start the call.
2. Emit {type: "session.input_transcript.delta", delta: "The transcript keeps", start_ms: 0, end_ms: 1200}, then {type: "session.output_transcript.delta", delta: "Mm-hmm.", start_ms: 900, end_ms: 1300}, then {type: "session.input_transcript.delta", delta: " splitting up.", start_ms: 1300, end_ms: 2500}. There should be exactly 2 turns, and the first should contain "The transcript keeps splitting up.".
3. Emit {type: "session.output_transcript.delta", delta: "A long reply with room to scroll back. ".repeat(80), start_ms: 4000, end_ms: 20000}. The region should come to overflow (scrollHeight - clientHeight > 0, eval expect true); its clientHeight should be at most 192; the distance from the bottom should come to be at most 1.
4. In a one-shot eval, scroll the region to the top: set its scrollTop = 0 and dispatch a "scroll" Event on it. The exchange's "Latest" button (".live-exchange-latest") should become visible.
5. Emit {type: "session.output_transcript.delta", delta: " The newest words.", start_ms: 20000, end_ms: 21000}. The exchange should contain "The newest words.", and the region's scrollTop should still be exactly 0.
6. Click the Latest button. The distance from the bottom should come to be at most 1.
7. Click data-testid "live-stop".`, func(s *shelley) {
		s.conversation("echo: overlap ready", "overlap")
		s.mockVoice(false, false)
	})
}

func TestE2ELiveDelegationPausedThought(t *testing.T) {
	e2e(t, useful(0), `Live keeps a paused thought together without moving an answer before its question. `+liveTerms+`"The turns" are the exchange's ".live-exchange-turn" elements; check the nth one with an eval on document.querySelectorAll("[data-testid=live-exchange] .live-exchange-turn")[n]. Perform the steps in order.
1. Navigate to /c/boundaries and start the call.
2. Emit {type: "session.input_transcript.delta", delta: "This is", start_ms: 0, end_ms: 1000}, then {type: "session.output_transcript.delta", delta: "Mm.", start_ms: 900, end_ms: 1200}, then {type: "session.input_transcript.delta", delta: " still one thought", start_ms: 3500, end_ms: 4500}, then {type: "session.input_transcript.delta", delta: "\n.", start_ms: 4500, end_ms: 4500} (a newline and a period). There should be exactly 2 turns, and the first turn's p element should have text exactly "This is still one thought.".
3. Emit {type: "session.output_transcript.delta", delta: "Which panel?", start_ms: 5000, end_ms: 6000}, then {type: "session.input_transcript.delta", delta: "Sidebar.", start_ms: 6200, end_ms: 7000}. There should be exactly 4 turns; the third (index 2) should contain "Which panel?", the fourth (index 3) "Sidebar.", and the first must not contain "Sidebar.".
4. Click data-testid "live-stop".`, func(s *shelley) {
		s.conversation("echo: boundaries ready", "boundaries")
		s.mockVoice(false, false)
	})
}

func TestE2ELiveDelegationLatePunctuation(t *testing.T) {
	e2e(t, useful(0), `Late punctuation neither amends a sent request nor creates another task. `+liveTerms+`Call "history" describes the conversation as the server has it. Perform the steps in order.
1. Navigate to /c/punctuation and start the call.
2. Call window.liveSpeak("punctuation", "echo: keep the period"). There should be exactly one message "Spoken request: echo: keep the period" (the request).
3. Emit {type: "session.input_transcript.delta", delta: ".", start_ms: 850, end_ms: 900}, then {type: "session.input_transcript.delta", delta: "Next thought.", start_ms: 5000, end_ms: 6000}. The exchange should contain "Next thought.", and must contain neither ".Next thought." nor ". Next thought.".
4. Click data-testid "live-stop". Call "history" should return exactly {"livetranscript":false,"continued":false} (no livetranscript message, and nothing says "Spoken request (continued)"). The request must not contain "USER: echo: keep the period.".
5. Navigate to /c/punctuation again (a reload). There should again be exactly one message "Spoken request: echo: keep the period" (the request), and it must not contain "USER: echo: keep the period.".`, func(s *shelley) {
		c := s.conversation("echo: punctuation ready", "punctuation")
		s.mockVoice(false, false)
		s.call("history", func() string {
			h := s.liveHistory(c.ID).count(func(typ string, _ bool) bool { return typ == "livetranscript" }) > 0
			continued := strings.Contains(s.conversationJSON(c.ID), "Spoken request (continued)")
			return fmt.Sprintf(`{"livetranscript":%t,"continued":%t}`, h, continued)
		})
	})
}

func TestE2ELiveDelegationBacklogDrains(t *testing.T) {
	e2e(t, useful(0), `A transcript backlog drains in order at hangup. `+liveTerms+`Once call "intercept" has run, every relay of events (POST .../live-session/<session>/events) is held back until call "release", and call "offsets" lists the start_ms of the events relayed so far, in order, as JSON. Call "hangup" says how the server answered the first hangup (DELETE .../live-session/<session>): "ok" for success. Call "saved transcript" says whether the transcript the server saved for call 1 contains the whole backlog. Perform the steps in order.
1. Navigate to /c/backlog and start the call.
2. Call "intercept". Then in one eval, emit four events at once: for i from 0 to 3, window.liveEmit({type: "session.input_transcript.delta", delta: "声".repeat(16000), start_ms: i, end_ms: i + 1}).
3. Call "offsets" should come to return exactly [0] (the first relay is held).
4. Click data-testid "live-stop", then call "release".
5. Call "hangup" should return exactly "ok". Call "offsets" should return exactly [0,1,2,3]. There should be no data-testid "live-error" (count 0). Call "saved transcript" should return exactly "true".`, func(s *shelley) {
		c := s.conversation("echo: backlog ready", "backlog")
		s.mockVoice(false, false)
		on := s.intercepting()
		wait := s.gate("release").wait
		var mu sync.Mutex
		offsets := []float64{}
		l := s.recordLive(func(w http.ResponseWriter, r *http.Request, events []map[string]any) {
			if on.Load() {
				mu.Lock()
				for _, e := range events {
					ms, _ := e["start_ms"].(float64)
					offsets = append(offsets, ms)
				}
				mu.Unlock()
				if !wait(r) {
					return
				}
			}
			s.pass.ServeHTTP(w, r)
		})
		l.recordHangups(nil)
		s.call("offsets", func() string {
			mu.Lock()
			defer mu.Unlock()
			return mustJSON(offsets)
		})
		s.call("saved transcript", func() string {
			saved := string(s.do("GET", "/api/conversation/"+c.ID+"/call/1/transcript", nil))
			return strconv.FormatBool(strings.Contains(saved, strings.Repeat("声", 64000)))
		})
	})
}

// failedRelay is a failed pending relay test: endManually hangs up while the
// relay is held.
func failedRelay(t *testing.T, useful lazycue.Useful, endManually bool) {
	hangUp := ""
	after := "automatic stop"
	if endManually {
		hangUp = `Click data-testid "live-stop", then call "release".`
		after = "manual hangup"
	} else {
		hangUp = `Call "release" (without hanging up).`
	}
	e2e(t, useful, `A failed pending relay still ends the call after `+after+`. `+liveTerms+`Once call "intercept" has run, the server's answer to every relay of events (POST .../live-session/<session>/events) is held back until call "release" and is then 503 "Relay failed for test"; call "held" says whether one is being held. Call "hangup" says how the server answered the first hangup (DELETE .../live-session/<session>): "ok" for success. Perform the steps in order.
1. Navigate to /c/relay and start the call.
2. Call "intercept". Emit {type: "session.input_transcript.delta", delta: "Already received words", start_ms: 0, end_ms: 1000}. Call "held" should come to return exactly "true".
3. `+hangUp+`
4. Call "hangup" should return exactly "ok". data-testid "live-error" should contain "Failed to relay the call to Shelley" and "Relay failed for test". There should be no data-testid "live-stop" (count 0), and window.__voiceTracksStopped should be greater than 0 (eval, expect true).`, func(s *shelley) {
		s.conversation("echo: relay failure ready", "relay")
		s.mockVoice(false, false)
		on := s.intercepting()
		wait := s.gate("release").wait
		var held atomic.Bool
		l := s.recordLive(func(w http.ResponseWriter, r *http.Request, events []map[string]any) {
			if !on.Load() {
				s.pass.ServeHTTP(w, r)
				return
			}
			held.Store(true)
			if !wait(r) {
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "Relay failed for test")
		})
		l.recordHangups(nil)
		s.call("held", func() string { return strconv.FormatBool(held.Load()) })
	})
}

func TestE2ELiveDelegationFailedRelayAutomaticStop(t *testing.T) {
	failedRelay(t, useful(0), false)
}

func TestE2ELiveDelegationFailedRelayManualHangup(t *testing.T) {
	failedRelay(t, useful(0), true)
}

func TestE2ELiveDelegationFailedHangup(t *testing.T) {
	e2e(t, useful(0), `A failed hangup remains visible alongside a failed final transcript. `+liveTerms+`Once call "intercept" has run, the server answers every relay of events (POST .../live-session/<session>/events) with 503 "Relay failed for test", and every hangup (DELETE .../live-session/<session>) with 503 "Hangup failed for test". Perform the steps in order.
1. Navigate to /c/hangup and start the call.
2. Call "intercept". Emit {type: "session.input_transcript.delta", delta: "Already received words", start_ms: 0, end_ms: 1000}.
3. data-testid "live-error" should contain "Failed to relay the call to Shelley", "Could not end the call" and "Hangup failed for test".`, func(s *shelley) {
		s.conversation("echo: hangup failure ready", "hangup")
		s.mockVoice(false, false)
		on := s.intercepting()
		l := s.recordLive(func(w http.ResponseWriter, r *http.Request, events []map[string]any) {
			if !on.Load() {
				s.pass.ServeHTTP(w, r)
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "Relay failed for test")
		})
		l.recordHangups(on.Load)
	})
}

func TestE2ELiveDelegationErrorsOnStrip(t *testing.T) {
	e2e(t, useful(0), `Shelley's errors about a call show on its strip. `+liveTerms+`Perform the steps in order.
1. Navigate to /c/ready and start the call.
2. Emit {type: "session.delegation.created", offset_ms: -1, delegation: {id: "bad", target: "client"}}.
3. data-testid "live-error" should have text exactly "Live returned an invalid delegation timestamp.".
4. Click data-testid "live-stop".`, func(s *shelley) {
		s.conversation("echo: ready", "ready")
		s.mockVoice(false, false)
	})
}

// navigateWithinAppDesc is the spec's navigateWithinApp.
const navigateWithinAppDesc = `"Go within the app to P" means: in an eval, window.history.pushState({}, "", P) and then window.dispatchEvent(new PopStateEvent("popstate")); the page's URL should then end with P (eval location.href.endsWith(P), expect true). `

func TestE2ELiveDelegationPinnedToOriginalChat(t *testing.T) {
	e2e(t, useful(0), `A call stays pinned to its original chat while browsing other chats. `+liveTerms+navigateWithinAppDesc+`There are two conversations, /c/kestrel (the original chat) and /c/other. Call "reply in other" sends "echo: unrelated private reply" to the other conversation through the API, and call "reply in origin" sends "echo: original background reply" to the original one; call "where delegated" says which conversations, as the server has them, mention "delegated to the original chat"; call "sessions" lists the conversations calls were started in (POST /api/conversation/<id>/live-session) as JSON; call "archive other" archives the other conversation. Perform the steps in order.
1. Navigate to /c/kestrel and start the call. Emit {type: "session.output_transcript.delta", delta: "Still in the original call.", start_ms: 0, end_ms: 500}. The exchange should contain "Still in the original call.".
2. Go within the app to /c/other. ".header-title" should have text exactly "other". data-testid "live-bar" should still be visible; the exchange should contain "Still in the original call." and "kestrel" (the original chat's name); data-testid "live-call-header" should have aria-label exactly "End voice call".
3. Call "reply in other" (it should return "sent"). The last message containing "unrelated private reply" should become visible.
4. Call "reply in origin" (it should return "sent"). window.liveNotes() should come to include "Backend reply: original background reply", and must not include "unrelated private reply".
5. Go within the app to /new. The message input (data-testid "message-input") should be visible.
6. Call window.liveSpeak("off_view_task", "echo: delegated to the original chat"). Call "where delegated" should come to return exactly {"kestrel":true,"other":false}. window.liveNotes() should come to include '"delegation_id":"off_view_task","content":"Backend reply: edit predictable.go'. The page's URL should still end with "/new". Call "sessions" should return exactly ["kestrel"].
7. data-testid "live-call-origin" should contain "kestrel". Click it. ".header-title" should have text exactly "kestrel"; the live bar should be visible; live-call-origin should be disabled.
8. The strip and its controls fit a phone, in an archive too: call "archive other"; once the page has learned of the archive, the drawer's row for the other conversation, .conversation-item[data-conversation-id="{{OTHER_ID}}"], is gone (count 0); then go within the app to /c/other. ".header-title" should have text exactly "other (archived)".
9. Set the viewport to 320x640, keeping this phone's scale 2.75, mobile and touch. The right edge of data-testid "live-stop" (x + width) should be within the live bar's right edge (eval, expect true); document.documentElement.scrollWidth <= innerWidth should be true; data-testid "live-meter" should be visible.
10. Click data-testid "live-mute"; the live bar should contain "Muted".
11. Click data-testid "live-stop". The live bar should become hidden, and window.__voiceTracksStopped should be exactly 1.`, func(s *shelley) {
		origin := s.conversation("echo: original voice chat", "kestrel")
		other := s.conversation("echo: other chat", "other")
		s.v("OTHER_ID", other.ID)
		s.mockVoice(false, false)
		l := s.recordLive(nil)
		chat := func(id, message string) string {
			s.api("POST", "/api/conversation/"+id+"/chat", map[string]any{"message": message, "model": "predictable"}, nil)
			return "sent"
		}
		var once sync.Map
		once1 := func(name string, f func() string) func() string {
			return func() string {
				if v, ok := once.Load(name); ok {
					return v.(string)
				}
				v := f()
				once.Store(name, v)
				return v
			}
		}
		s.call("reply in other", once1("other", func() string { return chat(other.ID, "echo: unrelated private reply") }))
		s.call("reply in origin", once1("origin", func() string { return chat(origin.ID, "echo: original background reply") }))
		s.call("where delegated", func() string {
			mentions := func(id string) bool {
				return strings.Contains(s.conversationJSON(id), "delegated to the original chat")
			}
			return fmt.Sprintf(`{"kestrel":%t,"other":%t}`, mentions(origin.ID), mentions(other.ID))
		})
		s.call("sessions", func() string {
			l.mu.Lock()
			defer l.mu.Unlock()
			names := []string{}
			for _, session := range l.sessions {
				switch session.Conversation {
				case origin.ID:
					names = append(names, "kestrel")
				case other.ID:
					names = append(names, "other")
				default:
					names = append(names, session.Conversation)
				}
			}
			return mustJSON(names)
		})
		s.call("archive other", once1("archive", func() string {
			s.api("POST", "/api/conversation/"+other.ID+"/archive", nil, nil)
			return "archived"
		}))
	})
}

func TestE2ELiveDelegationCallOnNewStartsOneDraft(t *testing.T) {
	e2e(t, useful(0), `A call on /new starts one blank draft with the composer's selection, wherever the user browses meanwhile. `+liveTerms+navigateWithinAppDesc+`The composer has the profile VoicePersonal-test selected (model predictable). The microphone is held: getUserMedia answers only once window.__releaseVoiceMicrophone() is called in an eval. There is a conversation /c/other. Call "profiles loaded" says whether the page has loaded the profiles (/api/profiles answered successfully); call "drafts" counts the requests to /api/conversations/draft; call "session" checks the call start (POST /api/conversation/<id>/live-session) and returns "ok" when it is right; call "promoted" checks the call's conversation as the server has it and returns "ok" when it is right; call "next session" describes the latest call start as JSON. Perform the steps in order.
1. Navigate to /new. Call "profiles loaded" should come to return exactly "true".
2. Click data-testid "live-call-header". data-testid "live-bar" should contain "Connecting". Nothing is created before the microphone is granted: call "drafts" should return exactly "0".
3. Go within the app to /c/other. ".header-title" should have text exactly "other".
4. Call window.__releaseVoiceMicrophone(). The live bar should contain "On call".
5. Call "session" should return exactly "ok": the call was started in a new conversation (not /c/other), which the server has as a draft, with model "predictable" and conversation_options carrying the profile VoicePersonal-test and tool overrides, so the call's tasks promote the draft with them.
6. Call window.liveSpeak("first_task", "echo: started by voice"). Call "promoted" should come to return exactly "ok": the conversation is no longer a draft, has model "predictable", and its conversation options name the profile VoicePersonal-test. Call "drafts" should return exactly "1". ".header-title" should still have text exactly "other". window.liveNotes() should come to include '"delegation_id":"first_task","content":"Backend reply: edit predictable.go'.
7. A conversation under way has its own settings; the next call sends none: click data-testid "live-call-origin". There should be exactly one message "Spoken request: echo: started by voice". Click data-testid "live-stop".
8. Click data-testid "live-call-header" again. The live bar should contain "Connecting". Call window.__releaseVoiceMicrophone(); the live bar should contain "On call". Call "next session" should return exactly {"sessions":2,"conversation_options":false}.
9. Click data-testid "live-call-header".`, func(s *shelley) {
		other := s.conversation("echo: browsing during permission", "other")
		const profile = "VoicePersonal-test"
		s.api("POST", "/api/profiles", map[string]any{"name": profile, "model": "predictable", "system_prompt": "You are a personal reflection partner."}, nil)
		s.initScript(fmt.Sprintf(`localStorage.setItem('shelley.profile', %q);`, profile))
		s.mockVoice(false, true)
		var drafts atomic.Int32
		s.route("/api/conversations/draft", func(w http.ResponseWriter, r *http.Request) {
			drafts.Add(1)
			s.pass.ServeHTTP(w, r)
		})
		var loaded atomic.Bool
		s.route("/api/profiles", func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			s.pass.ServeHTTP(rec, r)
			if rec.status/100 == 2 {
				loaded.Store(true)
			}
		})
		l := s.recordLive(nil)
		s.call("profiles loaded", func() string { return strconv.FormatBool(loaded.Load()) })
		s.call("drafts", func() string { return strconv.Itoa(int(drafts.Load())) })
		latest := func() (liveSession, int) {
			l.mu.Lock()
			defer l.mu.Unlock()
			if len(l.sessions) == 0 {
				return liveSession{}, 0
			}
			return l.sessions[len(l.sessions)-1], len(l.sessions)
		}
		type conversation struct {
			IsDraft bool   `json:"is_draft"`
			Model   string `json:"model"`
			Options string `json:"conversation_options"`
		}
		started := func(id string) conversation {
			var got struct {
				Conversation conversation `json:"conversation"`
			}
			s.api("GET", "/api/conversation/"+id, nil, &got)
			return got.Conversation
		}
		s.call("session", func() string {
			session, n := latest()
			if n == 0 {
				return "no call started"
			}
			var p problems
			p.check(session.Conversation != other.ID, "the call started in /c/other")
			options, _ := session.Body["conversation_options"].(map[string]any)
			_, overrides := options["tool_overrides"].(map[string]any)
			p.check(session.Body["model"] == "predictable" && options["profile"] == profile && overrides, "call start %s", mustJSON(session.Body))
			p.check(started(session.Conversation).IsDraft, "the conversation is not a draft")
			return p.result()
		})
		s.call("promoted", func() string {
			session, n := latest()
			if n == 0 {
				return "no call started"
			}
			c := started(session.Conversation)
			var options struct {
				Profile string `json:"profile"`
			}
			json.Unmarshal([]byte(c.Options), &options)
			if c.IsDraft || c.Model != "predictable" || options.Profile != profile {
				return "conversation " + mustJSON(c)
			}
			return "ok"
		})
		s.call("next session", func() string {
			session, n := latest()
			_, options := session.Body["conversation_options"]
			return fmt.Sprintf(`{"sessions":%d,"conversation_options":%t}`, n, options)
		})
	})
}

func TestE2ELiveDelegationCollapseKeepsListening(t *testing.T) {
	e2e(t, useful(0), `Live exchange collapse keeps listening and stays closed across handoffs. `+liveTerms+`"The toggle" is data-testid "live-exchange-toggle" (the exchange's summary); "the region" is the exchange's [role="region"][aria-label="Live exchange transcript"]. Call "snapshot" saves the conversation as the server has it; call "unchanged" says whether it is still exactly the same. Perform the steps in order.
1. Navigate to /c/collapse and start the call. Call "snapshot".
2. Emit {type: "session.input_transcript.delta", delta: "echo: collapse me", start_ms: 0, end_ms: 1000}.
3. Click the toggle. The exchange's JS property open should be false (eval, expect false), and the region should be hidden.
4. Emit {type: "session.output_transcript.delta", delta: "Still listening.", start_ms: 1100, end_ms: 1800}. data-testid "live-bar" should contain "On call". Call "unchanged" should return exactly "true".
5. Emit {type: "session.delegation.created", offset_ms: 1800, delegation: {id: "collapsed", target: "client"}}. The message "Spoken request: echo: collapse me" should contain "LIVE: Still listening.". The exchange should become hidden.
6. Emit {type: "session.output_transcript.delta", delta: "More words while collapsed. ".repeat(80), start_ms: 2000, end_ms: 12000}. The exchange should become visible, its open property still false, and the region hidden.
7. Focus the toggle (one-shot eval calling its focus()) and press Space. The region should become visible and contain "More words while collapsed.". Its distance from the bottom (scrollHeight - clientHeight - scrollTop) should come to be at most 1.
8. Click data-testid "live-stop".`, func(s *shelley) {
		c := s.conversation("echo: collapse ready", "collapse")
		s.mockVoice(false, false)
		var before string
		s.call("snapshot", func() string {
			before = s.conversationJSON(c.ID)
			return "saved"
		})
		s.call("unchanged", func() string { return strconv.FormatBool(s.conversationJSON(c.ID) == before) })
	})
}

func TestE2ELiveDelegationBriefView(t *testing.T) {
	e2e(t, useful(0), `Live delivery remains visible and replyable in brief view: the conversation lets the agent message the user (message_user on). `+liveTerms+`Call "snapshot turns" counts the conversation's messages that end a turn, as the server has them; call "more turns" says whether there are more now; call "reply" has the agent reply to the spoken request through the API, with message_user {"text": "On it.", "reply_to": <the spoken request's sequence_id>, "reaction": "👍", "end_turn": true}, and returns "sent" once done. Perform the steps in order.
1. Navigate to /c/brief and start the call. Call "snapshot turns".
2. Emit {type: "session.input_transcript.delta", delta: "Review the build", start_ms: 0, end_ms: 1000}, then {type: "session.delegation.created", offset_ms: 1000, delegation: {id: "brief", target: "client"}}.
3. The message "Spoken request: Review the build" (the request) should be visible. Call "more turns" should come to return exactly "true".
4. Call "reply" should return exactly "sent".
5. The data-testid "message-user-bubble" containing "On it." should have a data-testid "message-user-quote" containing "Review the build", and the request's data-testid "message-reactions" should have text exactly "👍".
6. Click data-testid "live-stop".`, func(s *shelley) {
		c := s.conversationWith(map[string]any{
			"message": "echo: brief ready", "model": "predictable", "cwd": s.cwd,
			"conversation_options": map[string]any{"tool_overrides": map[string]any{"message_user": "on"}},
		}, "brief")
		s.mockVoice(false, false)
		turns := func() int {
			return s.liveHistory(c.ID).count(func(_ string, endOfTurn bool) bool { return endOfTurn })
		}
		var before int
		s.call("snapshot turns", func() string {
			before = turns()
			return strconv.Itoa(before)
		})
		s.call("more turns", func() string { return strconv.FormatBool(turns() > before) })
		var replied string
		s.call("reply", func() string {
			if replied != "" {
				return replied
			}
			h := s.liveHistory(c.ID)
			i := slices.IndexFunc(h.Messages, func(m liveMessage) bool {
				return bytes.Contains(m.UserData, []byte("live_delivery_id"))
			})
			if i < 0 {
				return "no spoken request with a live_delivery_id"
			}
			reply := mustJSON(map[string]any{"text": "On it.", "reply_to": h.Messages[i].SequenceID, "reaction": "👍", "end_turn": true})
			s.api("POST", "/api/conversation/"+c.ID+"/chat", map[string]any{"model": "predictable", "message": "message_user: " + reply}, nil)
			replied = "sent"
			return replied
		})
	})
}
