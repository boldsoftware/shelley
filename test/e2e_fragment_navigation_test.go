package test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Ported from ui/e2e/fragment-navigation.spec.ts.

// lorem is a conversation /debug/loremipsum made, and its first two user
// messages.
type lorem struct {
	ID, Slug, First, Second string
}

// seedLorem makes a 12-turn conversation with /debug/loremipsum.
func (s *shelley) seedLorem() lorem {
	var created struct {
		ID string `json:"conversation_id"`
	}
	s.check(json.Unmarshal(s.postForm("/debug/loremipsum?json=1", url.Values{"size": {"12"}, "model": {"predictable"}}), &created))
	var got struct {
		Messages []struct {
			ID      string          `json:"message_id"`
			Type    string          `json:"type"`
			LLMData json.RawMessage `json:"llm_data"`
		} `json:"messages"`
		Conversation struct {
			Slug string `json:"slug"`
		} `json:"conversation"`
	}
	s.api("GET", "/api/conversation/"+created.ID, nil, &got)
	l := lorem{ID: created.ID, Slug: got.Conversation.Slug}
	for _, m := range got.Messages {
		if m.Type != "user" || !strings.Contains(string(m.LLMData), "Turn ") {
			continue
		}
		if l.First == "" {
			l.First = m.ID
		} else if l.Second == "" {
			l.Second = m.ID
		}
	}
	if l.Second == "" {
		s.fail("loremipsum made fewer than two user turns")
	}
	return l
}

// messageFragment is the URL fragment that links to a message.
func messageFragment(id string) string {
	id = regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(id, "")
	return "m-" + id[:min(8, len(id))]
}

// fragmentNavigation seeds a conversation for the fragment tests, as
// {{SLUG}}, {{FIRST}}, {{SECOND}}, {{FIRST_FRAGMENT}} and
// {{SECOND_FRAGMENT}}. window.fragmentScrolls records the data-message-id of
// every element scrolled into view, which happens instantly. Call "append"
// sends another message and waits for its reply, setting {{APPENDED}} to the
// reply's message ID.
func fragmentNavigation(s *shelley) lorem {
	s.initScript(`window.fragmentScrolls = [];
const original = Element.prototype.scrollIntoView;
Element.prototype.scrollIntoView = function (options) {
  const id = this.getAttribute('data-message-id');
  if (id) window.fragmentScrolls.push(id);
  original.call(this, typeof options === 'object' ? { ...options, behavior: 'instant' } : options);
};`)
	l := s.seedLorem()
	s.v("SLUG", l.Slug)
	s.v("FIRST", l.First)
	s.v("SECOND", l.Second)
	s.v("FIRST_FRAGMENT", messageFragment(l.First))
	s.v("SECOND_FRAGMENT", messageFragment(l.Second))
	s.call("append", func() string {
		const marker = "fragment regression append"
		s.api("POST", "/api/conversation/"+l.ID+"/chat", map[string]string{"message": "echo: " + marker, "model": "predictable"}, nil)
		s.poll(30*time.Second, func() error {
			var got struct {
				Messages []struct {
					ID        string          `json:"message_id"`
					EndOfTurn bool            `json:"end_of_turn"`
					LLMData   json.RawMessage `json:"llm_data"`
				} `json:"messages"`
			}
			s.api("GET", "/api/conversation/"+l.ID, nil, &got)
			last := got.Messages[len(got.Messages)-1]
			if !last.EndOfTurn || !strings.Contains(string(last.LLMData), marker) {
				return errors.New("no reply yet")
			}
			s.v("APPENDED", last.ID)
			return nil
		})
		return "appended"
	})
	return l
}

const fragmentSetup = `The test seeds a 12-turn conversation at /c/{{SLUG}} whose first two user messages have data-message-id {{FIRST}} and {{SECOND}}; the URL fragments that link to them are {{FIRST_FRAGMENT}} and {{SECOND_FRAGMENT}}. window.fragmentScrolls lists the data-message-id of every element scrolled into view so far, in order; check it with an eval of JSON.stringify(window.fragmentScrolls). Call "append" sends another message and waits for its reply; after it, {{APPENDED}} is the reply's data-message-id. To "set the fragment to X", eval a promise that sets location.hash to X and resolves on the next hashchange event. "At the bottom" means the messages container (".messages-container") has scrollHeight - clientHeight - scrollTop below 5 and the scroll-to-bottom button (".scroll-to-bottom-button") is hidden. To "delay" a message, rename its data-message-id attribute to data-delayed-message-id, as if its element had not mounted yet; to "mount" it, set data-message-id back from data-delayed-message-id (leaving the other attribute). Perform the steps in order. `

func TestE2EFragmentURLJumpNotReplayed(t *testing.T) {
	e2e(t, fragmentSetup+`A jump to a URL fragment is not replayed after scrolling away and appending. Navigate to /c/{{SLUG}}#{{FIRST_FRAGMENT}} and wait for the table of contents button (".toc-button") to be visible. window.fragmentScrolls should become ["{{FIRST}}"]. Scroll the messages container down by adding 1200 to its scrollTop. The element [data-message-id="{{FIRST}}"] should then not intersect the viewport. Call "append" and wait for an element with data-message-id {{APPENDED}} to exist. window.fragmentScrolls should still be ["{{FIRST}}"], and [data-message-id="{{FIRST}}"] still outside the viewport.`, func(s *shelley) {
		fragmentNavigation(s)
	})
}

func TestE2EFragmentTOCJumpNotReplayed(t *testing.T) {
	e2e(t, fragmentSetup+`A jump from the table of contents is not replayed after scrolling away and appending. Navigate to /c/{{SLUG}} and wait for the table of contents button (".toc-button") to be visible. Click it, then click the first user entry (".toc-entry-user"). window.fragmentScrolls should become ["{{FIRST}}"]. Scroll the messages container down by adding 1200 to its scrollTop. The element [data-message-id="{{FIRST}}"] should then not intersect the viewport. Call "append" and wait for an element with data-message-id {{APPENDED}} to exist. window.fragmentScrolls should still be ["{{FIRST}}"], and [data-message-id="{{FIRST}}"] still outside the viewport.`, func(s *shelley) {
		fragmentNavigation(s)
	})
}

func TestE2EFragmentChangeCancelsRetry(t *testing.T) {
	e2e(t, fragmentSetup+`Changed fragments resolve and cancel the preceding retry. The page runs on a fake clock that steps advance with eval "window.__clock.runFor(ms)". Navigate to /c/{{SLUG}} and wait for the table of contents button (".toc-button") to be visible. Delay message {{FIRST}}. Set the fragment to {{FIRST_FRAGMENT}}, then run the clock for 100ms, then set the fragment to {{SECOND_FRAGMENT}}. window.fragmentScrolls should become ["{{SECOND}}"]. Mount the delayed message, then run the clock for 1100ms: window.fragmentScrolls must still be exactly ["{{SECOND}}"]. Set the fragment to {{FIRST_FRAGMENT}}: window.fragmentScrolls should become ["{{SECOND}}","{{FIRST}}"].`, func(s *shelley) {
		s.clock(time.Time{})
		fragmentNavigation(s)
	})
}

func TestE2EFragmentPendingRetryResolvesOnce(t *testing.T) {
	e2e(t, fragmentSetup+`Messages arriving while a retry is pending resolve the delayed target exactly once. The page runs on a fake clock that steps drive with window.__clock. Navigate to /c/{{SLUG}} and wait for the table of contents button (".toc-button") to be visible. Pause the clock a minute from now: eval "window.__clock.pauseAt(Date.now() + 60000)". Delay message {{FIRST}}, then set the fragment to {{FIRST_FRAGMENT}}. window.fragmentScrolls must be exactly []. Mount the delayed message. Call "append", then wait for the reply to render: the paused clock delivers no animation frames, so poll an eval that advances the clock one frame and reports whether the reply is there, "(window.__clock.runFor(16), !!document.querySelector('[data-message-id=\"{{APPENDED}}\"]'))", expecting true. Then run the clock for 1100ms (eval "window.__clock.runFor(1100)"): window.fragmentScrolls must be exactly ["{{FIRST}}"].`, func(s *shelley) {
		s.clock(time.Time{})
		fragmentNavigation(s)
	})
}

func TestE2EFragmentExhaustedRetryResolvesOnce(t *testing.T) {
	e2e(t, fragmentSetup+`Messages arriving after a fragment's retries are exhausted resolve the delayed target exactly once. The page runs on a fake clock that steps drive with window.__clock. Navigate to /c/{{SLUG}} and wait for the table of contents button (".toc-button") to be visible. Pause the clock a minute from now: eval "window.__clock.pauseAt(Date.now() + 60000)". Delay message {{FIRST}}, then set the fragment to {{FIRST_FRAGMENT}}, then run the clock for 1100ms (eval "window.__clock.runFor(1100)"). window.fragmentScrolls must be exactly []. Mount the delayed message. Call "append", then wait for the reply to render: the paused clock delivers no animation frames, so poll an eval that advances the clock one frame and reports whether the reply is there, "(window.__clock.runFor(16), !!document.querySelector('[data-message-id=\"{{APPENDED}}\"]'))", expecting true. Then run the clock for 1100ms: window.fragmentScrolls must be exactly ["{{FIRST}}"].`, func(s *shelley) {
		s.clock(time.Time{})
		fragmentNavigation(s)
	})
}

func TestE2EFragmentConversationChangeResolvesAfterLoading(t *testing.T) {
	e2e(t, fragmentSetup+`Conversation changes resolve after loading, even with the same fragment. The page runs on a fake clock that steps advance with eval "window.__clock.runFor(ms)". There is a second seeded conversation, /c/{{SLUG2}}. The server's answer to loading it is held back until call "release", and the message in it that comes second in its transcript is given the ID {{FIRST}} as well; call "loading" says whether that load has started ("true" or "false"). 1. The very first step navigates straight to /c/{{SLUG}}#{{FIRST_FRAGMENT}}: the page loads with the fragment already in its URL (do not load the conversation first). 2. window.fragmentScrolls should become ["{{FIRST}}"]. 3. Switch the app to /c/{{SLUG2}}#{{FIRST_FRAGMENT}} the way its router would: eval history.pushState(null, "", that URL) and then dispatch a PopStateEvent("popstate") on window. 4. Call "loading" until it returns "true". 5. window.fragmentScrolls must still be exactly ["{{FIRST}}"]. 6. Call "release". 7. window.fragmentScrolls should become ["{{FIRST}}","{{FIRST}}"]. 8. Switch back to /c/{{SLUG}} (no fragment) the same pushState-and-popstate way, and wait for an element [data-message-id="{{SECOND}}"] to exist. 9. Call "append" and wait for an element with data-message-id {{APPENDED}} to exist. 10. Run the clock for 1100ms: window.fragmentScrolls must still be exactly ["{{FIRST}}","{{FIRST}}"].`, func(s *shelley) {
		s.clock(time.Time{})
		first := fragmentNavigation(s)
		second := s.seedLorem()
		s.v("SLUG2", second.Slug)
		var loading atomic.Bool
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		s.route("GET /api/conversation/"+second.ID, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" {
				s.pass.ServeHTTP(w, r)
				return
			}
			var body map[string]any
			if err := s.fetchJSON(r, &body); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			for _, m := range body["messages"].([]any) {
				if m := m.(map[string]any); m["message_id"] == second.First {
					m["message_id"] = first.First
				}
			}
			loading.Store(true)
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			writeJSON(w, body)
		})
		s.call("loading", func() string { return strconv.FormatBool(loading.Load()) })
		s.call("release", func() string {
			release()
			return "released"
		})
	})
}

func TestE2EFragmentInvalidAnchorsKeepFollowing(t *testing.T) {
	e2e(t, fragmentSetup+`Invalid anchors leave follow enabled, and removing a fragment cancels retries. The page runs on a fake clock that steps advance with eval "window.__clock.runFor(ms)". Navigate to /c/{{SLUG}} and wait for the table of contents button (".toc-button") to be visible. It should be at the bottom. For each of the fragments "m-", "t-", "unrelated", "m-%21" and "m-deadbeef" in turn: set the fragment to it, run the clock for 1100ms, check that window.fragmentScrolls is exactly [] and that it is still at the bottom. Call "append" and wait for an element with data-message-id {{APPENDED}} to exist; it should be at the bottom (follow mode stayed armed). Then delay message {{FIRST}}, set the fragment to {{FIRST_FRAGMENT}}, run the clock for 100ms, set the fragment to "" (empty), mount the delayed message and run the clock for 1100ms: window.fragmentScrolls must be exactly [].`, func(s *shelley) {
		s.clock(time.Time{})
		fragmentNavigation(s)
	})
}

func TestE2EFragmentFreshConversationRestoresBottom(t *testing.T) {
	e2e(t, fragmentSetup+`A fresh conversation keeps bottom restoration through startup clamps. Navigate to /c/{{SLUG}} and wait for the table of contents button (".toc-button") to be visible. It should be at the bottom. Then, in one async eval: in the messages container, insert a 600px-tall div into ".messages-list" just before ".messages-bottom-sentinel"; wait two animation frames; set the container's scrollTop to its scrollHeight; wait two more animation frames; remove the div; read the container's scrollTop; dispatch a "scroll" event on the container and a "beforeunload" event on window; and return localStorage.getItem("shelley_scroll_{{ID}}"). It must return "bottom". It should still be at the bottom afterwards.`, func(s *shelley) {
		l := fragmentNavigation(s)
		s.v("ID", l.ID)
	})
}

func TestE2EFragmentScrollToBottomClearsFragment(t *testing.T) {
	e2e(t, fragmentSetup+`The scroll-to-bottom button clears the current fragment. 1. Navigate to /c/{{SLUG}} and wait for the table of contents button (".toc-button") to be visible. 2. It should be at the bottom. 3. Release the bottom pin as an upward wheel gesture would: dispatch a synthetic WheelEvent("wheel", {deltaY: -10000, bubbles: true}) on the messages container, then set its scrollTop to 0. 4. Wait until its scrollTop is 0. 5. The scroll-to-bottom button (".scroll-to-bottom-button") should become visible. 6. Only now, with the scroll done, set the fragment without navigating: eval history.replaceState(null, "", location.pathname + "#current"), and check that location.hash is "#current". 7. Click the scroll-to-bottom button. 8. location.hash should become "" and the scroll-to-bottom button should be hidden.`, func(s *shelley) {
		fragmentNavigation(s)
	})
}
