package test

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
)

// What's typed into a conversation's composer is saved on the server, so it
// follows the user to their other devices, and sending it clears it there.
func TestE2EComposerTextFollowsTheUser(t *testing.T) {
	e2e(t, useful(0), `A started conversation's unsent text is kept on the server: the phone sees what the laptop typed, the laptop what the phone typed, and a send clears it. "The input" is the message input [data-testid="message-input"]; "the send button" is [data-testid="send-button"]. Call "saved text" returns the text the server keeps for the conversation's composer, or "(none)". Call "phone edits" saves "edited on the phone 5b1e" there, as the phone's composer would, and returns "edited". Call "send" tells which saved text the browser's last send said it was composed from. 1. Navigate to /c/composer-text and wait (up to 30s) for the input to be visible. 2. Fill the input with "typed on the laptop 3a9d". 3. Call "saved text"; it should become "typed on the laptop 3a9d" (up to 10s). 4. Call "phone edits"; it returns "edited". 5. The input's value should become exactly "edited on the phone 5b1e" (up to 10s). 6. Reload the page and wait (up to 30s) for the input to be visible; its value should be exactly "edited on the phone 5b1e" (up to 10s). 7. Fill the input with "echo: sent from the laptop 9f4d"; the send button should become enabled (up to 5s); click it. 8. The input's value should become exactly "" (up to 10s). 9. Call "send"; it should return "the saved text". 10. Call "saved text"; it should become "(none)" (up to 10s). 11. An element [data-testid="message"] containing "sent from the laptop 9f4d" should be visible (up to 10s).`, func(s *shelley) {
		c := s.conversation("echo: composer text seed", "composer-text")
		saved := savedText(s, c.ID)
		s.call("phone edits", func() string {
			s.api("PUT", "/api/conversation/"+c.ID+"/draft", map[string]string{"draft": "edited on the phone 5b1e"}, nil)
			return "edited"
		})
		named := sentVersion(s, saved)
		s.route(chatPattern, func(w http.ResponseWriter, r *http.Request) {
			named(r)
			queueRelay(s, w, r)
		})
	})
}

// The Compact in Place button sends a message of its own, leaving what's
// typed in the composer, here and on the server.
func TestE2ECompactButtonKeepsComposerText(t *testing.T) {
	e2e(t, useful(0), `The Compact in Place button leaves the composer's unsent text alone. "The input" is the message input [data-testid="message-input"]. Call "saved text" returns the text the server keeps for the conversation's composer, or "(none)". Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait (up to 30s) for the input to be visible.
2. Fill the input with "next question 7c2e".
3. Call "saved text"; it should become "next question 7c2e" (up to 10s).
4. Click the ".context-usage-label" element, then click the [data-testid="compact-in-place-button"] button.
5. An element with the text "Compact your context in place." is visible (up to 10s).
6. The input's value is exactly "next question 7c2e".
7. Call "saved text"; it returns "next question 7c2e".`, func(s *shelley) {
		s.device(compactInPlaceDevice)
		c := s.compactInPlaceConversation("echo: compact later", true)
		savedText(s, c.ID)
	})
}

// A send clears only the saved text it was composed from: not text the
// phone saved while the laptop's own save was still answering, which the
// composer shows once the send is done.
func TestE2ESendKeepsTextSavedMeanwhile(t *testing.T) {
	e2e(t, useful(0), `A send clears only the saved text it consumed. "The input" is the message input [data-testid="message-input"]; "the send button" is [data-testid="send-button"]. Call "saved text" returns the text the server keeps for the conversation's composer, or "(none)". Call "hold saves" makes the server's answers to the browser's saves of its composer text wait; call "saves held" returns how many saves the server took but hasn't answered; call "release" answers them. Call "phone edits" saves "edited on the phone 5b1e" there, as the phone's composer would, and returns "edited". Call "empty saves" returns how many times the browser saved empty composer text. Perform the steps in order.
1. Navigate to /c/composer-race and wait (up to 30s) for the input to be visible.
2. Call "hold saves"; it returns "holding".
3. Fill the input with "echo: sent from the laptop 9f4d".
4. Call "saves held"; it should become "1" (up to 10s).
5. Call "phone edits"; it returns "edited".
6. Call "saved text"; it returns "edited on the phone 5b1e".
7. The send button should become enabled (up to 5s); click it.
8. Call "release"; it returns "released".
9. An element [data-testid="message"] containing "sent from the laptop 9f4d" should be visible (up to 10s).
10. The input's value should become exactly "edited on the phone 5b1e" (up to 10s).
11. Call "saved text"; it returns "edited on the phone 5b1e".
12. Call "empty saves"; it returns "0".`, func(s *shelley) {
		c := s.conversation("echo: composer race seed", "composer-race")
		savedText(s, c.ID)
		s.call("phone edits", func() string {
			s.api("PUT", "/api/conversation/"+c.ID+"/draft", map[string]string{"draft": "edited on the phone 5b1e"}, nil)
			return "edited"
		})
		holdAnswers(s, "saves", "PUT /api/conversation/{id}/draft", emptySaves(s))
	})
}

// savedText reads the text and stamp the server keeps for conversation id's
// composer, and has call "saved text" return the text, or "(none)".
func savedText(s *shelley, id string) func() (text, stamp string) {
	saved := func() (text, stamp string) {
		var got struct {
			Conversation struct {
				Draft          string  `json:"draft"`
				DraftUpdatedAt *string `json:"draft_updated_at"`
			} `json:"conversation"`
		}
		s.api("GET", "/api/conversation/"+id, nil, &got)
		if got.Conversation.DraftUpdatedAt != nil {
			stamp = *got.Conversation.DraftUpdatedAt
		}
		return got.Conversation.Draft, stamp
	}
	s.call("saved text", func() string {
		if text, _ := saved(); text != "" {
			return text
		}
		return "(none)"
	})
	return saved
}

// A send leaves text another device saves while it's under way, and the
// composer shows that text once the send is done.
func TestE2ESendShowsTextSavedWhileSending(t *testing.T) {
	e2e(t, useful(0), `Text saved on another device while a send is under way survives it and shows in the composer after. "The input" is the message input [data-testid="message-input"]; "the send button" is [data-testid="send-button"]. Call "saved text" returns the text the server keeps for the conversation's composer, or "(none)". Call "hold sends" makes the server's answers to the browser's message sends wait; call "sends held" returns how many sends the server took but hasn't answered; call "release" answers them. Call "phone edits" saves "edited on the phone 5b1e" there, as the phone's composer would, and returns "edited". Call "empty saves" returns how many times the browser saved empty composer text. Eval window.__streamSaw(T) says whether the page's event stream has brought text T. Perform the steps in order.
1. Navigate to /c/composer-sending and wait (up to 30s) for the input to be visible.
2. The input's value should become exactly "echo: saved elsewhere 4e1a" (up to 10s).
3. Call "hold sends"; it returns "holding".
4. The send button should become enabled (up to 5s); click it.
5. Call "sends held"; it should become "1" (up to 10s).
6. Call "phone edits"; it returns "edited".
7. Eval window.__streamSaw("edited on the phone 5b1e") should become true (up to 10s).
8. The input's value is exactly "echo: saved elsewhere 4e1a".
9. Call "release"; it returns "released".
10. An element [data-testid="message"] containing "saved elsewhere 4e1a" should be visible (up to 10s).
11. The input's value should become exactly "edited on the phone 5b1e" (up to 10s).
12. Call "saved text"; it returns "edited on the phone 5b1e".
13. Call "empty saves"; it returns "0".`, func(s *shelley) {
		s.initScript(streamSawPage)
		c := s.conversation("echo: composer sending seed", "composer-sending")
		s.api("PUT", "/api/conversation/"+c.ID+"/draft", map[string]string{"draft": "echo: saved elsewhere 4e1a"}, nil)
		savedText(s, c.ID)
		s.call("phone edits", func() string {
			s.api("PUT", "/api/conversation/"+c.ID+"/draft", map[string]string{"draft": "edited on the phone 5b1e"}, nil)
			return "edited"
		})
		empty := emptySaves(s)
		s.route("PUT /api/conversation/{id}/draft", func(w http.ResponseWriter, r *http.Request) {
			empty(r)
			queueRelay(s, w, r)
		})
		holdAnswers(s, "sends", chatPattern, nil)
	})
}

// A recording started right after typing takes the text's save along, so
// the transcript's send clears it even once the recorder floats elsewhere.
func TestE2ERecordingConsumesTextTypedJustBefore(t *testing.T) {
	e2e(t, useful(0), `A recording started right after typing consumes that text's saved copy. The test seeded the conversation at /c/composer-recorded and another conversation {{VIEWED}} at /c/{{VIEWED_SLUG}}. `+recMocks+recDrawer+recFloating+`Call "saved text" returns the text the server keeps for /c/composer-recorded's composer, or "(none)". Message submissions are accepted without being sent on; call "chats" counts them, and call "send" tells which saved text the last one said it was composed from. `+recIDs+`
1. Navigate to /c/composer-recorded and wait (up to 30s) for the message input to be visible.
2. Fill the message input with "context typed just before 7a1b", then at once click the voice button.
3. Exactly one [data-testid="recording-inline"] should exist, and window.__recordingMock.recorderStarts should become 1.
4. Select conversation {{VIEWED}} from the drawer. The URL should have exactly the path "/c/{{VIEWED_SLUG}}" (an exact "value", not "text").
5. The recorder should float.
6. Call "saved text"; it should become "context typed just before 7a1b" (up to 10s).
7. Click the stop button. Call "chats" should become "1".
8. Call "send"; it returns "the saved text".`, func(s *shelley) {
		recording(s)
		c := s.conversation("echo: composer recorded seed", "composer-recorded")
		viewed := s.conversation("echo: recorder floats here", "")
		s.v("VIEWED", viewed.ID)
		s.v("VIEWED_SLUG", viewed.Slug)
		s.fakeUploads("/tmp/shelley-uploads/composer-recorded.webm")
		saved := savedText(s, c.ID)
		chats := s.counter("chats")
		named := sentVersion(s, saved)
		s.route(chatPattern, func(w http.ResponseWriter, r *http.Request) {
			named(r)
			chats.Add(1)
			acceptChat(w)
		})
	})
}

// holdAnswers has the server take requests matching pattern while call
// "hold <what>" is in effect, answering them only on call "release"; call
// "<what> held" counts those taken but not answered. inspect, if set, sees
// each request first.
func holdAnswers(s *shelley, what, pattern string, inspect func(r *http.Request)) {
	var holding atomic.Bool
	var held atomic.Int64
	release := make(chan struct{})
	s.call("hold "+what, func() string {
		holding.Store(true)
		return "holding"
	})
	s.call(what+" held", func() string { return strconv.FormatInt(held.Load(), 10) })
	s.call("release", func() string {
		if holding.Swap(false) {
			close(release)
		}
		return "released"
	})
	s.route(pattern, func(w http.ResponseWriter, r *http.Request) {
		if inspect != nil {
			inspect(r)
		}
		if !holding.Load() {
			queueRelay(s, w, r)
			return
		}
		// The request lands; its answer waits.
		resp, err := s.forward(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		held.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		held.Add(-1)
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})
}

// emptySaves has call "empty saves" count the browser's saves of empty
// composer text, for an inspect of holdAnswers or a route of its own.
func emptySaves(s *shelley) func(r *http.Request) {
	n := s.counter("empty saves")
	return func(r *http.Request) {
		var body struct {
			Draft *string `json:"draft"`
		}
		json.Unmarshal(readBody(r), &body)
		if body.Draft != nil && *body.Draft == "" {
			n.Add(1)
		}
	}
}

// sentVersion has call "send" tell which saved text the last message send
// it sees said it was composed from: "the saved text" if it named the
// version saved, "none" if it named none.
func sentVersion(s *shelley, saved func() (text, stamp string)) func(r *http.Request) {
	var named atomic.Value
	named.Store("no send yet")
	s.call("send", func() string { return named.Load().(string) })
	return func(r *http.Request) {
		var body struct {
			DraftUpdatedAt *string `json:"draft_updated_at"`
		}
		json.Unmarshal(readBody(r), &body)
		err := s.try(func() {
			_, stamp := saved()
			switch {
			case body.DraftUpdatedAt == nil:
				named.Store("none")
			case *body.DraftUpdatedAt == stamp:
				named.Store("the saved text")
			default:
				named.Store("version " + *body.DraftUpdatedAt + ", not the saved " + stamp)
			}
		})
		if err != nil {
			named.Store(err.Error())
		}
	}
}

// streamSawPage gives steps window.__streamSaw(text): whether the page's
// event stream has brought text yet.
const streamSawPage = `
const streamed = [];
const SawEventSource = window.EventSource;
window.EventSource = class extends SawEventSource {
  constructor(url, init) {
    super(url, init);
    this.addEventListener("message", (e) => streamed.push(e.data));
  }
};
window.__streamSaw = (text) => streamed.some((d) => d.includes(text));`

const composerQueueSetup = `"The input" is the message input [data-testid="message-input"]; "the send button" is [data-testid="send-button"]; "the thinking indicator" is [data-testid="agent-thinking"]; "Queue it" means: the send options button [data-testid="send-options-button"] should be enabled and visible (up to 5s); click it; the queue option [data-testid="queue-option"] should become visible (up to 5s); click it. Call "saved text" returns the text the server keeps for the conversation's composer, or "(none)". `

// Queued text stays out of the composer though the echo of its save comes
// while the queue's send is under way.
func TestE2EQueuedTextStaysQueued(t *testing.T) {
	e2e(t, useful(0), composerQueueSetup+`Call "hold saves" makes the browser's saves of its composer text wait before they reach the server, call "saves held" counts those waiting, and call "release saves" lets them through; calls "hold sends", "sends held" and "release sends" do the same for its message sends. Eval window.__streamSaw(T) says whether the page's event stream has brought text T. Perform the steps in order.
1. Navigate to /c/composer-queue and wait (up to 30s) for the input to be visible.
2. Fill the input with "delay: 60"; the send button should become enabled (up to 5s); click it; the thinking indicator should become visible (up to 30s).
3. Call "hold saves"; it returns "holding".
4. Call "hold sends"; it returns "holding".
5. Fill the input with "queued 7c2e".
6. Call "saves held"; it should become "1" (up to 10s).
7. Queue it.
8. The input's value should become exactly "" (up to 5s).
9. Call "release saves"; it returns "released".
10. Call "sends held"; it should become "1" (up to 10s).
11. Eval window.__streamSaw("queued 7c2e") should become true (up to 10s).
12. The input's value is exactly "".
13. Call "release sends"; it returns "released".
14. A queued badge [data-testid="queued-badge"] should become visible (up to 10s).
15. The input's value is exactly "".
16. Call "saved text"; it should become "(none)" (up to 10s).`, func(s *shelley) {
		s.initScript(streamSawPage)
		c := s.conversation("echo: composer queue seed", "composer-queue")
		savedText(s, c.ID)
		holdRequests(s, "saves", "PUT /api/conversation/{id}/draft")
		holdRequests(s, "sends", chatPattern)
	})
}

// Text typed while a queue's send is under way is saved, though it repeats
// what was queued.
func TestE2ETextTypedWhileQueueingIsSaved(t *testing.T) {
	e2e(t, useful(0), composerQueueSetup+`Call "hold sends" makes the browser's message sends wait before they reach the server, call "sends held" counts those waiting, and call "release sends" lets them through. Perform the steps in order.
1. Navigate to /c/composer-requeue and wait (up to 30s) for the input to be visible.
2. Fill the input with "delay: 60"; the send button should become enabled (up to 5s); click it; the thinking indicator should become visible (up to 30s).
3. Fill the input with "continue".
4. Call "saved text"; it should become "continue" (up to 10s).
5. Call "hold sends"; it returns "holding".
6. Queue it.
7. The input's value should become exactly "" (up to 5s).
8. Call "sends held"; it should become "1" (up to 10s).
9. Fill the input with "continue".
10. Call "release sends"; it returns "released".
11. A queued badge [data-testid="queued-badge"] should become visible (up to 10s).
12. Call "saved text"; it should become "continue" (up to 10s).
13. The input's value is exactly "continue".`, func(s *shelley) {
		c := s.conversation("echo: composer requeue seed", "composer-requeue")
		savedText(s, c.ID)
		holdRequests(s, "sends", chatPattern)
	})
}

// A send that fails keeps its text, saved, though the send called off the
// save it was waiting for.
func TestE2EFailedSendSavesText(t *testing.T) {
	e2e(t, useful(0), `A message send that fails leaves its text saved for the conversation's composer. "The input" is the message input [data-testid="message-input"]; "the send button" is [data-testid="send-button"]. Call "saved text" returns the text the server keeps for the conversation's composer, or "(none)". The server fails every message send; call "sends failed" counts them. Perform the steps in order.
1. Navigate to /c/composer-failed and wait (up to 30s) for the input to be visible.
2. Fill the input with "echo: failed send 4d1a" and, right away, press Enter in it.
3. Call "sends failed"; it should become "1" (up to 10s).
4. Call "saved text"; it should become "echo: failed send 4d1a" (up to 10s).
5. The input's value is exactly "echo: failed send 4d1a".`, func(s *shelley) {
		c := s.conversation("echo: composer failed seed", "composer-failed")
		savedText(s, c.ID)
		failed := s.counter("sends failed")
		s.route(chatPattern, func(w http.ResponseWriter, r *http.Request) {
			failed.Add(1)
			http.Error(w, "send failed", http.StatusInternalServerError)
		})
	})
}

// holdRequests has requests matching pattern wait before they reach the
// server while call "hold <what>" is in effect, until call "release
// <what>"; call "<what> held" counts those waiting.
func holdRequests(s *shelley, what, pattern string) {
	var holding atomic.Bool
	var held atomic.Int64
	release := s.gate("release " + what)
	s.call("hold "+what, func() string {
		holding.Store(true)
		return "holding"
	})
	s.call(what+" held", func() string { return strconv.FormatInt(held.Load(), 10) })
	s.route(pattern, func(w http.ResponseWriter, r *http.Request) {
		if holding.Load() {
			held.Add(1)
			released := release.wait(r)
			held.Add(-1)
			if !released {
				return
			}
		}
		queueRelay(s, w, r)
	})
}

// Leaving a conversation while its queue's send is under way, and coming
// back, keeps the queued text out of the composer and the server.
func TestE2EQueuedTextStaysQueuedAcrossNavigation(t *testing.T) {
	e2e(t, useful(0), composerQueueSetup+recDrawer+`The test seeded the conversation {{QUEUE}} at /c/composer-queue-away and another one, {{OTHER}}. Call "hold sends" makes the browser's message sends wait before they reach the server, call "sends held" counts those waiting, and call "release sends" lets them through. Perform the steps in order.
1. Navigate to /c/composer-queue-away and wait (up to 30s) for the input to be visible.
2. Fill the input with "delay: 60"; the send button should become enabled (up to 5s); click it; the thinking indicator should become visible (up to 30s).
3. Fill the input with "queued 3f8a".
4. Call "saved text"; it should become "queued 3f8a" (up to 10s).
5. Call "hold sends"; it returns "holding".
6. Queue it.
7. The input's value should become exactly "" (up to 5s).
8. Call "sends held"; it should become "1" (up to 10s).
9. Select conversation {{OTHER}} from the drawer.
10. Select conversation {{QUEUE}} from the drawer.
11. Call "release sends"; it returns "released".
12. A queued badge [data-testid="queued-badge"] should become visible (up to 10s).
13. Call "saved text"; it should become "(none)" (up to 10s).
14. The input's value should become exactly "" (up to 10s).
15. Call "saved text"; it returns "(none)".`, func(s *shelley) {
		c := s.conversation("echo: composer queue away seed", "composer-queue-away")
		other := s.conversation("echo: composer elsewhere", "composer-elsewhere")
		s.v("QUEUE", c.ID)
		s.v("OTHER", other.ID)
		savedText(s, c.ID)
		holdRequests(s, "sends", chatPattern)
	})
}
