package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Ported from ui/e2e/hook-health.spec.ts.

// hookHealth is the end-of-turn hook's health as the server streams it.
type hookHealth struct {
	EpisodeID      int    `json:"episode_id"`
	Revision       int    `json:"revision"`
	Active         bool   `json:"active"`
	Dismissed      bool   `json:"dismissed"`
	Failures       int    `json:"failures"`
	LastFailureAt  string `json:"last_failure_at"`
	ConversationID string `json:"conversation_id"`
}

// hookFailed is a failing hook's health, as the spec's failed() made it.
func hookFailed() hookHealth {
	return hookHealth{
		EpisodeID:      1,
		Revision:       1,
		Active:         true,
		Failures:       1,
		LastFailureAt:  "2026-11-14T23:00:00Z",
		ConversationID: "hook-chat",
	}
}

// hookHealthy is a healthy hook's health at a revision.
func hookHealthy(revision int) hookHealth {
	return hookHealth{Revision: revision}
}

// hookState is the health a test streams, which its calls change.
type hookState struct {
	mu      sync.Mutex
	current hookHealth
}

func (st *hookState) get() hookHealth {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.current
}

func (st *hookState) set(h hookHealth) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.current = h
}

// hookStream answers every /api/stream2 connection with the current health,
// then a heartbeat; the body ends at once, so the page reconnects about a
// second later. window.__hookFrames counts the stream messages carrying
// hook health that the page has handled since it loaded (counted after
// its own handlers and their rendering ran).
func (s *shelley) hookStream(initial hookHealth) *hookState {
	st := &hookState{current: initial}
	s.initScript(`window.__hookFrames = 0;
const HookNativeEventSource = window.EventSource;
window.EventSource = class extends HookNativeEventSource {
  constructor(...args) {
    super(...args);
    this.addEventListener('message', (event) => {
      if (String(event.data).includes('"hook_health"')) setTimeout(() => { window.__hookFrames++; }, 0);
    });
  }
};`)
	s.route("/api/stream2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range []any{map[string]any{"hook_health": st.get()}, map[string]any{"heartbeat": true}} {
			fmt.Fprintf(w, "data: %s\n\n", mustJSON(frame))
		}
	})
	return st
}

// hookStates gives the test a call for each named health.
func (s *shelley) hookStates(st *hookState, states map[string]hookHealth) {
	for name, h := range states {
		s.call(name, func() string {
			st.set(h)
			return "set"
		})
	}
}

// hookLayoutEval measures the notice: how many lines the hook's title
// takes, whether the troubleshoot action starts below the notice's text,
// and whether the notice's content fits its width.
const hookLayoutEval = `(async () => { await document.fonts.ready; const element = document.querySelector('[data-testid="hook-health-notice"]'); const title = element.querySelector('[data-testid=hook-health-title]'); const text = element.querySelector('.resource-notice-text'); const action = element.querySelector('[data-testid=hook-health-troubleshoot]'); const range = document.createRange(); range.selectNodeContents(title); return { titleLines: range.getClientRects().length, actionBelowText: action.getBoundingClientRect().top >= text.getBoundingClientRect().bottom, fits: element.scrollWidth <= element.clientWidth }; })()`

func TestE2EHookHealthMobileLayout(t *testing.T) {
	e2e(t, useful(0), `End-of-turn hook health keeps the mobile action below the full hook name and failure count. The page's live stream (/api/stream2) reports the end-of-turn hook failing; calls "1 failure" and "111 failures" set how many consecutive failures later page loads see (it starts at 1). "The notice" is the element with data-testid "hook-health-notice". "Check the layout" means: the notice should be visible, then this eval must give exactly {"actionBelowText":true,"fits":true,"titleLines":1}: `+hookLayoutEval+`. The device starts 320x844. Perform the steps in order, navigating (not reloading) each time: 1. Call "1 failure", navigate to /new and check the layout. 2. Call "111 failures", navigate to /new and check the layout. 3. Set the viewport to 390x844 (set_viewport with only width and height). 4. Call "1 failure", navigate to /new and check the layout. 5. Call "111 failures", navigate to /new and check the layout. 6. Set the viewport to 1280x844 (only width and height). 7. Without navigating, the notice must have the computed CSS display exactly "flex".`, func(s *shelley) {
		s.viewport(320, 844)
		st := s.hookStream(hookFailed())
		many := hookFailed()
		many.Failures = 111
		s.hookStates(st, map[string]hookHealth{"1 failure": hookFailed(), "111 failures": many})
	})
}

func TestE2EHookHealthEpisodes(t *testing.T) {
	e2e(t, useful(0), `End-of-turn hook health aggregates failures, recovers, dismisses one episode, and re-arms. The page's live stream (/api/stream2) reports the end-of-turn hook's health; it starts healthy, and each of these calls sets what later page loads see: "fail" (episode 1, 1 failure), "fail 111 times" (episode 1, 111 failures), "recover" (healthy), "fail in episode 2" (episode 2, 1 failure), "fail again dismissed" (episode 2, 2 failures, already dismissed), "recover again" (healthy), "fail in episode 3" (episode 3, 1 failure). Dismissing is faked: the first dismiss request fails with HTTP 500, later ones succeed and mark the episode dismissed; call "dismiss attempts" returns how many dismiss requests the page made (or an error if one did not name the current episode). There are two conversations, /c/{{SLUG}} and /c/{{OTHER}}. "The notice" is the element with data-testid "hook-health-notice". window.__hookFrames counts the stream messages with hook health the page has handled since it loaded: "the notice is absent" means first wait until eval window.__hookFrames >= 1 gives true (so the page has heard the stream), then there must be exactly 0 notices. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The notice is absent. 3. Call "fail", reload the page, and the notice should be visible (allow 30s). 4. The element with data-testid "hook-health-title" should have exactly the text "Hook end-of-turn failed". 5. The notice's text should contain "1 consecutive failure". 6. Call "fail 111 times" and reload. 7. The notice's text should contain "111 consecutive failures" (an assert_text_contains on the notice itself, allowing 30s). 8. There must be exactly 1 notice. 9. Call "recover" and reload. 10. The notice is absent (allow 30s for the count). 11. Call "fail in episode 2" and reload. 12. The notice should be visible (allow 30s), and its text should contain "1 consecutive failure". 13. The warning is global, not attached to the current conversation: navigate to /c/{{OTHER}}; the notice should be visible. 14. Call "dismiss attempts": it must return exactly "0". 15. Click the element with data-testid "hook-health-dismiss". 16. Call "dismiss attempts" until it returns exactly "1". 17. The notice should still be visible. 18. Click the element with data-testid "hook-health-dismiss" again. 19. Call "dismiss attempts" until it returns exactly "2". 20. There must then be exactly 0 notices. 21. Reload: the notice is absent. 22. Call "fail again dismissed" and reload: the notice is absent. 23. Call "recover again" and reload: the notice is absent. 24. Call "fail in episode 3" and reload. 25. The notice should be visible (allow 30s), and its text should contain "1 consecutive failure".`, func(s *shelley) {
		st := s.hookStream(hookHealthy(0))
		fail111 := hookFailed()
		fail111.Revision, fail111.Failures = 2, 111
		episode2 := hookFailed()
		episode2.EpisodeID = 2
		again := hookFailed()
		again.EpisodeID, again.Revision, again.Failures, again.Dismissed = 2, 4, 2, true
		episode3 := hookFailed()
		episode3.EpisodeID = 3
		s.hookStates(st, map[string]hookHealth{
			"fail":                 hookFailed(),
			"fail 111 times":       fail111,
			"recover":              hookHealthy(3),
			"fail in episode 2":    episode2,
			"fail again dismissed": again,
			"recover again":        hookHealthy(5),
			"fail in episode 3":    episode3,
		})
		s.v("SLUG", s.conversation("echo: hook health", "").Slug)
		s.v("OTHER", s.conversation("echo: another chat", "").Slug)
		var mu sync.Mutex
		attempts := 0
		bad := ""
		s.route("POST /api/hooks/end-of-turn/dismiss", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			current := st.get()
			var body map[string]any
			json.Unmarshal(readBody(r), &body)
			if want := map[string]any{"episode_id": float64(current.EpisodeID)}; mustJSON(body) != mustJSON(want) {
				bad = fmt.Sprintf("dismiss body %s, want %s", mustJSON(body), mustJSON(want))
			}
			attempts++
			if attempts == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, "failed")
				return
			}
			current.Revision++
			current.Dismissed = true
			st.set(current)
			writeJSON(w, current)
		})
		s.call("dismiss attempts", func() string {
			mu.Lock()
			defer mu.Unlock()
			if bad != "" {
				return bad
			}
			return fmt.Sprint(attempts)
		})
	})
}

func TestE2EHookHealthTroubleshoot(t *testing.T) {
	e2e(t, useful(0), `End-of-turn hook health: troubleshooting starts a safe new chat; failed requests preserve the current composer. The page's live stream (/api/stream2) reports the end-of-turn hook failing twice in a row, latest at 2026-11-14T23:00:00Z, in conversation "affected-conversation". Starting a conversation (POST /api/conversations/new) fails with HTTP 500 "chat creation failed" until call "allow new chats", after which such requests go through to the server and the first one's body is kept. Call "safe prompt" checks that kept body: it returns "no request yet" until there is one, then "ok" if its message contains "end-of-turn", "Consecutive failed invocations: 2", "Latest failure (UTC): 2026-11-14T23:00:00Z" and "Latest affected conversation ID: affected-conversation", mentions read-only or inspect and redact or secret (any case), and mentions none of stderr, credential-value or webhook-token; otherwise it says what is wrong. Call "posts" counts the POST requests the page sent, as "new=<POSTs to /api/conversations/new> chat=<POSTs to paths ending in /chat>". "The notice" is the element with data-testid "hook-health-notice"; "the composer" is [data-testid="message-input"]. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The notice should be visible (allow 30s). 3. Fill the composer with "keep my current unsent note". 4. Click the button whose accessible name is exactly "Troubleshoot with Shelley". 5. The notice should still be visible. 6. The composer's value must be exactly "keep my current unsent note". 7. An element with the text "Failed to start conversation: chat creation failed" should be visible. 8. Call "allow new chats". 9. Click the button named exactly "Troubleshoot with Shelley" again. 10. Call "safe prompt" until it returns exactly "ok". 11. The page should leave the conversation: eval location.pathname !== "/c/{{SLUG}}" should give true. 12. The first ".messages-container .message-user" whose text contains "end-of-turn" should be visible (allow 30s). 13. Call "posts": it must return exactly "new=2 chat=0" right now (no polling needed; check it once). 14. Navigate to /c/{{SLUG}}. 15. The composer's value must be exactly "keep my current unsent note".`, func(s *shelley) {
		status := hookFailed()
		status.ConversationID = "affected-conversation"
		status.Failures = 2
		s.hookStream(status)
		s.v("SLUG", s.conversation("echo: diagnose hook", "").Slug)
		var allowed atomic.Bool
		var mu sync.Mutex
		var chatBody []byte
		s.route("POST /api/conversations/new", func(w http.ResponseWriter, r *http.Request) {
			if !allowed.Load() {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, "chat creation failed")
				return
			}
			body := readBody(r)
			mu.Lock()
			if chatBody == nil {
				chatBody = body
			}
			mu.Unlock()
			s.pass.ServeHTTP(w, r)
		})
		s.call("allow new chats", func() string {
			allowed.Store(true)
			return "allowed"
		})
		s.call("safe prompt", func() string {
			mu.Lock()
			body := chatBody
			mu.Unlock()
			if body == nil {
				return "no request yet"
			}
			var prompt struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(body, &prompt); err != nil {
				return fmt.Sprintf("body %q: %v", body, err)
			}
			for _, want := range []string{
				"end-of-turn",
				"Consecutive failed invocations: 2",
				"Latest failure (UTC): 2026-11-14T23:00:00Z",
				"Latest affected conversation ID: affected-conversation",
			} {
				if !strings.Contains(prompt.Message, want) {
					return fmt.Sprintf("message lacks %q: %q", want, prompt.Message)
				}
			}
			for _, re := range []string{`(?i)read-only|inspect`, `(?i)redact|secret`} {
				if !regexp.MustCompile(re).MatchString(prompt.Message) {
					return fmt.Sprintf("message does not match %s: %q", re, prompt.Message)
				}
			}
			if m := regexp.MustCompile(`(?i)stderr|credential-value|webhook-token`).FindString(prompt.Message); m != "" {
				return fmt.Sprintf("message mentions %q: %q", m, prompt.Message)
			}
			return "ok"
		})
		s.call("posts", func() string {
			newChats, chats := 0, 0
			for _, req := range s.requestLog() {
				method, uri, _ := strings.Cut(req, " ")
				path, _, _ := strings.Cut(uri, "?")
				if method != "POST" {
					continue
				}
				if path == "/api/conversations/new" {
					newChats++
				}
				if strings.HasSuffix(path, "/chat") {
					chats++
				}
			}
			return fmt.Sprintf("new=%d chat=%d", newChats, chats)
		})
	})
}
