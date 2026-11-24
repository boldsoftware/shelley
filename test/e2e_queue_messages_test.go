package test

import (
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/queue-messages.spec.ts.

// queueTextCountJS gives pages window.textCount(text, exact), the number of
// elements in the body whose whitespace-normalised text contains text
// (ignoring case), or equals it when exact, and that have no child element
// that matches too: what Playwright's text= locators and getByText count.
const queueTextCountJS = `window.textCount = (text, exact) => {
  const norm = (s) => s.replace(/\s+/g, ' ').trim();
  const want = norm(text);
  const matches = (el) => {
    const got = norm(el.textContent);
    return exact ? got === want : got.toLowerCase().includes(want.toLowerCase());
  };
  let count = 0;
  const walk = (el) => {
    if (['SCRIPT', 'NOSCRIPT', 'STYLE', 'TEMPLATE'].includes(el.tagName)) return false;
    let child = false;
    for (const c of el.children) if (walk(c)) child = true;
    const self = matches(el);
    if (self && !child) count++;
    return self;
  };
  walk(document.body);
  return count;
};`

const queueTextCountSetup = `window.textCount(text) counts the innermost elements whose text contains text, ignoring case; window.textCount(text, true) counts those whose whole text is exactly text. `

// queueRelay forwards r to the server and copies its answer to w,
// returning the answer's status.
func queueRelay(s *shelley, w http.ResponseWriter, r *http.Request) int {
	resp, err := s.forward(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return 0
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
	return resp.StatusCode
}

// queueAnswered counts the server's 200 answers to requests matching
// patterns, which call reports.
func queueAnswered(s *shelley, call string, patterns ...string) *atomic.Int64 {
	n := s.counter(call)
	for _, pattern := range patterns {
		s.route(pattern, func(w http.ResponseWriter, r *http.Request) {
			if queueRelay(s, w, r) == http.StatusOK {
				n.Add(1)
			}
		})
	}
	return n
}

const queueSetup = `"The input" is the message input [data-testid="message-input"]; "the send button" is [data-testid="send-button"]; "the chevron" is the send options button [data-testid="send-options-button"]; "the thinking indicator" is [data-testid="agent-thinking"]; "the Stop button" is button[aria-label="Stop"]; "queued badges" are [data-testid="queued-badge"]. "Tap X" means a touch step "start" on X immediately followed by a touch step "end" (a tap, not a click). "Send X and wait for work" means: fill the input with X; the send button should become enabled (up to 5s); tap it; the thinking indicator should become visible (up to 30s). "Queue X" means: fill the input with X; the chevron should be enabled and visible (up to 5s each); tap it; the queue option [data-testid="queue-option"] should become visible (up to 5s); tap it. Every test starts: navigate to /c/queue and wait (up to 30s) for the input to be visible. `

func queueMessages(t *testing.T, useful lazycue.Useful, description string, setup func(s *shelley)) {
	e2e(t, useful, queueSetup+description, func(s *shelley) {
		s.conversation("echo: queue seed", "queue")
		if setup != nil {
			setup(s)
		}
	})
}

func TestE2EQueueMessagesSplitButtonWhileWorking(t *testing.T) {
	queueMessages(t, useful(0), `The split button's chevron appears while the agent works. 1. Navigate to /c/queue and wait for the input. 2. Send "delay: 15" and wait for work. 3. The chevron should be visible (up to 5s).`, nil)
}

func TestE2EQueueMessagesChevronInactiveWhenIdle(t *testing.T) {
	queueMessages(t, useful(0), `The chevron goes inactive when the agent finishes: with a conversation open the split button stays, but with nothing to queue behind its chevron is disabled. 1. Navigate to /c/queue and wait for the input. 2. Send "delay: 2" and wait for work. 3. Wait (up to 30s) for the page's text to include "Delayed for 2 seconds" (a page-wide wait_text). 4. Fill the input with "test". 5. The chevron should be visible, and should become disabled (up to 10s). 6. The send button should be visible and enabled.`, nil)
}

func TestE2EQueueMessagesQueueViaDropdown(t *testing.T) {
	queueMessages(t, useful(0), `A message can be queued from the split button's dropdown. 1. Navigate to /c/queue and wait for the input. 2. Send "delay: 15" and wait for work. 3. Queue "echo: queued hello". 4. A queued badge should become visible (up to 10s).`, nil)
}

func TestE2EQueueMessagesQueuedHasCancel(t *testing.T) {
	queueMessages(t, useful(0), `A queued message's menu can cancel it. 1. Navigate to /c/queue and wait for the input. 2. Send "delay: 15" and wait for work. 3. Queue "echo: test cancel". 4. A queued badge should become visible (up to 10s). 5. Tap it. 6. The cancel item [data-testid="cancel-queued"] should become visible (up to 5s).`, nil)
}

func TestE2EQueueMessagesCancelRestoresToEmptyComposer(t *testing.T) {
	queueMessages(t, useful(0), queueTextCountSetup+`Cancelling a queued message puts it back in an empty composer, and the server forgets it. Call "cancel-queued answered" reports how many requests to /cancel-queued the server has answered with 200. 1. Navigate to /c/queue and wait for the input. 2. Send "delay: 60" and wait for work. 3. Queue "echo: to be cancelled". 4. A queued badge should become visible (up to 10s). 5. Tap it; the cancel item [data-testid="cancel-queued"] should become visible (up to 5s). 6. Call "cancel-queued answered"; it must be "0". 7. Tap the cancel item. 8. Call "cancel-queued answered"; it should become "1" (up to 10s). 9. The input's value should be exactly "echo: to be cancelled". 10. Reload the page (the stream only sends metadata for the deletion) and wait (up to 30s) for the input to be visible. 11. There should be no queued badge (count 0, up to 10s). 12. Nothing on the page may contain "to be cancelled" any more: eval window.textCount("to be cancelled") with expect "0" (up to 10s).`, func(s *shelley) {
		s.initScript(queueTextCountJS)
		queueAnswered(s, "cancel-queued answered", "/api/conversation/{id}/cancel-queued")
	})
}

func TestE2EQueueMessagesCancelKeepsTextTypedInFlight(t *testing.T) {
	queueMessages(t, useful(1), `Cancelling a queued message keeps text typed while the cancel request is in flight. The server's answer to /cancel-queued is held back until call "release cancel"; call "cancel started" reports "true" once that request has reached the server, and call "cancel-queued answered" reports how many such requests it has answered with 200. 1. Navigate to /c/queue and wait for the input. 2. Send "delay: 60" and wait for work. 3. Queue "echo: queued message". 4. There should be exactly one queued badge (up to 10s). 5. Tap the queued badge, then, once visible (up to 5s), the cancel item [data-testid="cancel-queued"]. 6. Call "cancel started"; it should become "true". 7. Fill the input with "keep this draft". 8. Call "release cancel". 9. Call "cancel-queued answered"; it should become "1" (up to 10s). 10. The input's value should be exactly "keep this draft".`, func(s *shelley) {
		started := s.signal("cancel started")
		release := s.gate("release cancel")
		answered := s.counter("cancel-queued answered")
		s.route("/api/conversation/{id}/cancel-queued", func(w http.ResponseWriter, r *http.Request) {
			started.Store(true)
			if !release.wait(r) {
				return
			}
			if queueRelay(s, w, r) == http.StatusOK {
				answered.Add(1)
			}
		})
	})
}

func TestE2EQueueMessagesStopRestoresAllQueued(t *testing.T) {
	queueMessages(t, useful(0), `Stopping the agent restores all queued messages to the composer. Call "cancel answered" reports "true" once the server has answered a request to /cancel (or /cancel-queued) with 200, "false" before. 1. Navigate to /c/queue and wait for the input. 2. Send "delay: 60" and wait for work. 3. Queue "echo: first queued message", then queue "echo: second queued message". 4. There should be exactly two queued badges (up to 10s). 5. Call "cancel answered"; it must be "false". 6. Tap the Stop button. 7. Call "cancel answered"; it should become "true" (up to 10s). 8. The input's value should be exactly "echo: first queued message\necho: second queued message" (the two joined by a newline; check JSON.stringify of the value). 9. There should be no queued badge (count 0, up to 10s).`, func(s *shelley) {
		answered := s.signal("cancel answered")
		for _, pattern := range []string{"/api/conversation/{id}/cancel", "/api/conversation/{id}/cancel-queued"} {
			s.route(pattern, func(w http.ResponseWriter, r *http.Request) {
				if queueRelay(s, w, r) == http.StatusOK {
					answered.Store(true)
				}
			})
		}
	})
}

func TestE2EQueueMessagesStopKeepsComposerText(t *testing.T) {
	queueMessages(t, useful(0), `Stopping the agent does not replace text already in the composer with the queued message. 1. Navigate to /c/queue and wait for the input. 2. Send "delay: 60" and wait for work. 3. Queue "echo: queued message". 4. There should be exactly one queued badge (up to 10s). 5. Fill the input with "keep this draft". 6. Tap the Stop button. 7. The input's value should be exactly "keep this draft", and there should be no queued badge (count 0, up to 10s).`, nil)
}

func TestE2EQueueMessagesStopRestoresUnacceptedQueue(t *testing.T) {
	queueMessages(t, useful(0), `Stopping the agent restores a queued message whose request the server has not accepted yet. Queueing requests (/chat requests whose JSON body has queue true) are held back until call "release queue"; call "queue started" reports "true" once one has reached the server. 1. Navigate to /c/queue and wait for the input. 2. Send "delay: 60" and wait for work. 3. Queue "echo: pending steering". 4. Call "queue started"; it should become "true". 5. Tap the Stop button. 6. The input's value should be exactly "echo: pending steering". 7. Call "release queue".`, func(s *shelley) {
		started := s.signal("queue started")
		release := s.gate("release queue")
		s.route("/api/conversation/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
			if object(readBody(r))["queue"] == true {
				started.Store(true)
				if !release.wait(r) {
					return
				}
			}
			s.pass.ServeHTTP(w, r)
		})
	})
}

func TestE2EQueueMessagesDrainsAfterAgentFinishes(t *testing.T) {
	queueMessages(t, useful(0), `A queued message runs once the agent finishes. 1. Navigate to /c/queue and wait for the input. 2. Send "delay: 3" and wait for work. 3. Queue "echo: queued drain test". 4. A queued badge should become visible (up to 10s). 5. Wait (up to 30s) for the page's text to include "Delayed for 3 seconds" (a page-wide wait_text). 6. The queued badge should become hidden (up to 15s). 7. Wait (up to 30s) for the page's text to include "queued drain test" (a page-wide wait_text).`, nil)
}

func TestE2EQueueMessagesQueuedWhileIdleNotWorking(t *testing.T) {
	queueMessages(t, useful(0), `A message the server queues while the agent is idle (as it does behind a recording whose transcription failed) does not show the agent working. The server answers every /chat request with 202 {"status":"queued"} without starting a turn. 1. Navigate to /c/queue and wait for the input. 2. Fill the input with "waits behind a failed recording" and tap the send button. 3. The input's value should become exactly "". 4. There must be no thinking indicator (count 0).`, func(s *shelley) {
		s.route("/api/conversation/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, `{"status":"queued"}`)
		})
	})
}

func TestE2EQueueMessagesQueuedWhileWorkingKeepsWorking(t *testing.T) {
	queueMessages(t, useful(0), `A plain send while the agent works is delivered now, the queued message stays queued, and the agent keeps working. 1. Navigate to /c/queue and wait for the input. 2. Send "delay: 15" and wait for work. 3. Queue "echo: first queued". 4. There should be exactly one queued badge. 5. Fill the input with "echo: sent now" and tap the send button. 6. Wait (up to 10s) for the page's text to include "echo: sent now" (a page-wide wait_text). 7. There should still be exactly one queued badge, and the thinking indicator should be visible.`, nil)
}

func TestE2EQueueMessagesSendWhileWorking(t *testing.T) {
	queueMessages(t, useful(0), `The main send button still sends normally while the agent works: the message waits for the turn's next model request, not for the turn to end. 1. Navigate to /c/queue and wait for the input. 2. Send "delay: 15" and wait for work. 3. Fill the input with "echo: immediate send" and tap the send button (the main button, not the chevron). 4. Wait (up to 10s) for the page's text to include "echo: immediate send" (a page-wide wait_text). 5. There should be exactly one [data-testid="injected-badge"] (up to 3s), and no queued badge (count 0).`, nil)
}
