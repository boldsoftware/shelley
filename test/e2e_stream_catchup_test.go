package test

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Ported from ui/e2e/stream-catchup.spec.ts: the stream-catch-up behavior of
// the unified /api/stream2 connection and the IndexedDB messageStore. See
// ChatInterface.loadMessages and services/globalStream.ts.
//
// Dropped: "reconnect triggers REST backfill for the focused conversation",
// which Playwright skipped: setOffline does not tear down the established
// EventSource, so the reconnect path never ran there. Unit tests in
// services/messageStore.test.ts and services/globalStream.test.ts cover it.
//
// (d) Stale-cache catch-up via REST backfill on switch is not covered here:
// forcing the messageStore for a non-focused conversation to "behind" without
// a debug hook would require patching the production bundle.

// catchupLoadsSince counts the browser's GETs of a conversation's full-load
// endpoint, whatever their query, from request mark on.
func (s *shelley) catchupLoadsSince(mark int, id string) int {
	n := 0
	for _, r := range s.requestLog()[mark:] {
		path, _, _ := strings.Cut(r, "?")
		if path == "GET /api/conversation/"+id {
			n++
		}
	}
	return n
}

// catchup seeds conversations "catchup-a" (with "Hello") and "catchup-b"
// (with "hello"). Call "track" starts counting the browser's loads of A,
// and call "A loads" says how many there have been since.
func (s *shelley) catchup() (a, b convo) {
	a = s.conversation("Hello", "catchup-a")
	b = s.conversation("hello", "catchup-b")
	mark := -1
	var mu sync.Mutex
	s.call("track", func() string {
		mu.Lock()
		defer mu.Unlock()
		mark = len(s.requestLog())
		return "tracking"
	})
	s.call("A loads", func() string {
		mu.Lock()
		defer mu.Unlock()
		if mark < 0 {
			return "not tracking"
		}
		return strconv.Itoa(s.catchupLoadsSince(mark, a.ID))
	})
	return a, b
}

// The conversation list previews each conversation's latest message, so
// matching text anywhere on the page can pass before the conversation has
// loaded -- and a load that lands after a test starts counting requests then
// looks like a cache miss. Hence "shows" looks in the messages container only.
const catchupSetup = `The test seeds two conversations: A at /c/catchup-a, whose reply reads "Hello! I'm Shelley, your AI assistant.", and B at /c/catchup-b, whose reply reads "Well, hi there!". A conversation "shows" a text when document.querySelector(".messages-container")?.textContent includes it (wait for that with an eval expecting true, allowing 15s; do not look elsewhere on the page, as the conversation list previews messages too). To "select" a conversation with slug X: click the button[aria-label="Open conversations"]; wait for ".drawer.open" to be visible (allow 5s); wait for the ".drawer.open .conversation-title" whose text is exactly X to be visible (allow 15s) and click it; then wait for ".drawer.open" to be hidden (allow 10s). Perform the steps in order. `

func TestE2EStreamCatchupSecondVisitHitsCache(t *testing.T) {
	e2e(t, useful(0), catchupSetup+`A → B → A: the second visit hits the cache, with no extra REST backfill. Call "track" starts counting the browser's GETs of A's full-load endpoint (/api/conversation/<A's id>, any query) and call "A loads" returns how many there have been since, as a number.
1. Navigate to /c/catchup-a; wait for the message input ([data-testid="message-input"]) to be visible (allow 30s); A shows "Hello! I'm Shelley, your AI assistant.".
2. Call "track".
3. Select catchup-b; it shows "Well, hi there!".
4. Select catchup-a; it shows "Hello! I'm Shelley, your AI assistant.".
5. Call "A loads": it must return exactly "0".
6. Once more: select catchup-b, then select catchup-a; it shows "Hello! I'm Shelley, your AI assistant.".
7. Call "A loads": it must still return exactly "0".`, func(s *shelley) {
		s.catchup()
	})
}

func TestE2EStreamCatchupOneEventSource(t *testing.T) {
	e2e(t, useful(0), catchupSetup+`Only one /api/stream2 EventSource exists, even after switching conversations. Call "stream requests" returns how many requests for /api/stream2 (any query) the browser has sent since the test began, as a number.
1. Navigate to /c/catchup-a; wait for the message input ([data-testid="message-input"]) to be visible (allow 30s); A shows "Hello! I'm Shelley, your AI assistant.".
2. Select catchup-b; it shows "Well, hi there!".
3. Select catchup-a, then select catchup-b, then select catchup-a; it shows "Hello! I'm Shelley, your AI assistant.".
4. Call "stream requests": it must return exactly "1". The stream is held open for the lifetime of the page; switching conversations must not reopen it.
5. performance.getEntriesByType("resource").filter((e) => e.name.includes("/api/stream2")).length must be at most 1 (Resource Timing logs only completed responses, so an open EventSource may show 0).`, func(s *shelley) {
		s.catchup()
		s.call("stream requests", func() string {
			n := 0
			for _, r := range s.requestLog() {
				path, _, _ := strings.Cut(r, "?")
				if path == "GET /api/stream2" {
					n++
				}
			}
			return strconv.Itoa(n)
		})
	})
}

func TestE2EStreamCatchupBackgroundUpdate(t *testing.T) {
	// The unified stream should deliver B's events into messageStore even
	// though B isn't the focused conversation. This asserts visibility, not a
	// 0-backfill count: hasFullHistory for B may not have been latched yet
	// (App seeded max_sequence_id_known from the snapshot, which may exceed
	// cached.maxSequenceId on the first switch and trip a defensive REST
	// catch-up). The contract that matters is that the background message
	// becomes visible on switch.
	e2e(t, useful(0), catchupSetup+`A cross-conversation live update: B receives a message while A is viewed. Call "surprise" sends B the message "echo: surprise from background" through the API and returns once the server has finished B's reply to it.
1. Navigate to /c/catchup-a; wait for the message input ([data-testid="message-input"]) to be visible (allow 30s); A shows "Hello! I'm Shelley, your AI assistant.".
2. Call "surprise".
3. Select catchup-b; it shows "surprise from background".`, func(s *shelley) {
		_, b := s.catchup()
		s.call("surprise", func() string {
			s.sendChat(b.ID, "echo: surprise from background")
			s.poll(30*time.Second, func() error {
				var got struct {
					Messages []struct {
						apiMessage
						LLMData string `json:"llm_data"`
					} `json:"messages"`
				}
				s.api("GET", "/api/conversation/"+b.ID, nil, &got)
				turns, surprise := 0, false
				for _, m := range got.Messages {
					if m.endsTurn() {
						turns++
					}
					surprise = surprise || strings.Contains(m.LLMData, "surprise from background")
				}
				if turns < 2 || !surprise {
					return fmt.Errorf("B's follow-up turn is not done (%d turns)", turns)
				}
				return nil
			})
			return "done"
		})
	})
}

func TestE2EStreamCatchupReloadPreservesCache(t *testing.T) {
	// A REST backfill may still fire on the initial mount after the reload as
	// defensive catch-up (the conversation list snapshot can advance
	// maxSequenceIdKnown past the cached maxSequenceId), so counting starts
	// after the reload settles.
	e2e(t, useful(0), catchupSetup+`A reload preserves the cache: no spinner, and no backfill after the reload. Call "track" starts counting the browser's GETs of A's full-load endpoint (/api/conversation/<A's id>, any query) and call "A loads" returns how many there have been since, as a number.
1. Navigate to /c/catchup-a; wait for the message input ([data-testid="message-input"]) to be visible (allow 30s); A shows "Hello! I'm Shelley, your AI assistant.".
2. Reload the page; wait for the message input to be visible (allow 30s); A shows "Hello! I'm Shelley, your AI assistant." (the IndexedDB cache rehydrates it).
3. There must be exactly 0 ".spinner" elements.
4. Call "track".
5. Select catchup-b; it shows "Well, hi there!".
6. Select catchup-a; it shows "Hello! I'm Shelley, your AI assistant.".
7. Call "A loads": it must return exactly "0": the B → A round trip after the reload must be served from the cache.`, func(s *shelley) {
		s.catchup()
	})
}

// catchupCoalescedIdle seeds a conversation and fakes the server around a send:
// the conversation list snapshot lists the conversation idle (or lists
// nothing), the send is accepted without running, and /api/stream2 holds
// its answer until call "release", then answers with one coalesced
// conversation list patch: the conversation, still idle, two messages on.
func catchupCoalescedIdle(s *shelley, listed bool) {
	c := s.conversation("Hello", "")
	s.v("SLUG", c.Slug)
	var snapshot struct {
		Conversations []map[string]any `json:"conversations"`
	}
	s.api("GET", "/api/conversations/snapshot", nil, &snapshot)
	var initial map[string]any
	for _, row := range snapshot.Conversations {
		if row["conversation_id"] == c.ID {
			initial = row
		}
	}
	if initial == nil {
		s.fail("conversation %s not in the snapshot", c.ID)
	}
	if initial["working"] != false {
		s.fail("conversation is working: %v", initial["working"])
	}
	final := map[string]any{}
	for k, v := range initial {
		final[k] = v
	}
	final["max_sequence_id"] = initial["max_sequence_id"].(float64) + 2
	held := s.gate("release")
	s.route("GET /api/stream2", func(w http.ResponseWriter, r *http.Request) {
		if !held.wait(r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{
			"conversation_list_patch": map[string]any{
				"reset":    true,
				"new_hash": "coalesced-idle",
				"at":       time.Now().UTC().Format(time.RFC3339Nano),
				"patch":    []any{map[string]any{"op": "replace", "path": "", "value": []any{final}}},
			},
		}))
	})
	rows := []any{}
	if listed {
		rows = append(rows, initial)
	}
	s.route("/api/conversations/snapshot", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"conversations": rows, "hash": "before-send"})
	})
	s.route("/api/conversation/"+c.ID+"/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"status":"accepted"}`)
	})
}

const catchupCoalescedIdleDescription = `The page's /api/stream2 EventSource gets no answer until call "release"; then it receives a single conversation list patch, coalesced, that shows the conversation idle with two more messages. Sending a message is accepted (202) without the server running it. Perform the steps in order.
1. Navigate to /c/{{SLUG}}; wait until document.querySelector(".messages-container")?.textContent includes "Hello! I'm Shelley, your AI assistant." (allow 15s).
2. Fill the message input ([data-testid="message-input"]) with "echo: fast reply" and click the send button ([data-testid="send-button"]).
3. The Stop button (button[aria-label="Stop"]) should become visible: the optimistic Thinking state.
4. Call "release".
5. The Stop button should become hidden.`

func TestE2EStreamCatchupCoalescedIdleKnownRow(t *testing.T) {
	e2e(t, useful(0), `A coalesced idle patch clears optimistic Thinking for a conversation the list already had: the conversation list snapshot the page loads lists the conversation /c/{{SLUG}}, idle. `+catchupCoalescedIdleDescription, func(s *shelley) {
		catchupCoalescedIdle(s, true)
	})
}

func TestE2EStreamCatchupCoalescedIdleFirstRow(t *testing.T) {
	e2e(t, useful(0), `A coalesced idle patch clears optimistic Thinking when it brings the list's first row: the conversation list snapshot the page loads is empty, so the patch is the first the list hears of the conversation /c/{{SLUG}}. `+catchupCoalescedIdleDescription, func(s *shelley) {
		catchupCoalescedIdle(s, false)
	})
}
