package test

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Ported from ui/e2e/conversation-provenance.spec.ts.

// provenanceSend sends text to the target conversation through the CLI, as
// the conversation sender does: trusted CLI requests carry the sender's ID.
func (s *shelley) provenanceSend(sender, target, text string) {
	cmd := exec.CommandContext(s.ctx, e2eBin, "client", "--url", "unix://"+filepath.Join(s.dir, "client.sock"), "chat", "-c", target, "-p", text)
	cmd.Env = append(os.Environ(), "SHELLEY_CONVERSATION_ID="+sender)
	if out, err := cmd.CombinedOutput(); err != nil {
		s.fail("shelley client chat: %v: %s", err, out)
	}
}

// provenancePage helps the descriptions find messages and check their
// conversation-message cards.
const provenancePage = `const norm = (s) => (s ?? '').replace(/\s+/g, ' ').trim();
// The first message whose text contains text (Playwright's filter({ hasText })).
window.__provMessage = (text) => [...document.querySelectorAll('[data-testid="message"]')].find((m) => norm(m.textContent).includes(norm(text))) ?? null;
window.__provCardOf = (text) => window.__provMessage(text)?.querySelector('[data-testid="conversation-message-card"]') ?? null;
// "ok", or what is wrong with the message's tool card.
window.__provCard = (text, headline, background, tag) => {
  const m = window.__provMessage(text);
  if (!m) return 'no message contains ' + text;
  const bad = [];
  if (!m.classList.contains('message-tool')) bad.push('class lacks message-tool: ' + m.className);
  if (/message-user|message-agent/.test(m.className)) bad.push('class has message-user or message-agent: ' + m.className);
  if (/<(?:parent|subagent)_message/.test(m.textContent)) bad.push('the raw message tags show');
  const card = m.querySelector('[data-testid="conversation-message-card"]');
  if (!card) return bad.concat('no card').join('; ');
  const got = {
    tag: norm(card.querySelector('.tool-tag')?.textContent),
    headline: norm(card.querySelector('[data-testid="conversation-message-card-headline"]')?.textContent),
    background: getComputedStyle(card).backgroundColor,
    radius: getComputedStyle(card).borderRadius,
  };
  const want = { tag, headline, background, radius: '8px' };
  for (const k of Object.keys(want)) if (got[k] !== want[k]) bad.push(k + ' is ' + JSON.stringify(got[k]) + ', not ' + JSON.stringify(want[k]));
  return bad.length ? bad.join('; ') : 'ok';
};
// How the card's headline lays out: its line count, whether it sits below
// the sender link, and whether it is clipped.
window.__provHeadline = (text) => {
  const el = window.__provCardOf(text).querySelector('[data-testid="conversation-message-card-headline"]');
  const range = document.createRange();
  range.selectNodeContents(el);
  const slug = el.parentElement.querySelector('a').getBoundingClientRect();
  return JSON.stringify({
    lines: new Set([...range.getClientRects()].map((r) => Math.round(r.top))).size,
    below: el.getBoundingClientRect().top >= slug.bottom,
    clipped: el.scrollWidth > el.clientWidth,
  });
};`

// provenanceProgress is the subagent's progress report, and its first line
// the card's headline.
const (
	provenanceHeadline = "Backend underway: **API and database** implementation are progressing."
	provenanceProgress = "  " + provenanceHeadline + "\nNext: tests, preview at https://example.com/api."
)

// provenanceCards checks the tool cards of CLI conversation messages, both
// ways between a parent and its subagent, on a screen of the given size and
// color scheme.
func provenanceCards(t *testing.T, width, height int, scheme string) {
	background := "rgb(243, 244, 246)"
	if scheme == "dark" {
		background = "rgb(31, 41, 55)"
	}
	layout := fmt.Sprintf(`{"lines":1,"below":%t,"clipped":false}`, width < 640)
	placement := "beside the sender link"
	if width < 640 {
		placement = "under the sender link"
	}
	card := func(text, headline, tag string) string {
		return fmt.Sprintf(`window.__provCard(%s, %s, '%s', '%s') must return "ok" (eval with expect, which polls)`, jsString(text), jsString(headline), background, tag)
	}
	e2e(t, fmt.Sprintf(`CLI conversation messages show as tool cards that say where they came from, at %dx%d in the %s color scheme. The conversation /c/{{SLUG}} (ID {{PARENT_ID}}) was started with "subagent: backend echo: Ready.", which made the subagent conversation {{CHILD}} (ID {{CHILD_ID}}). window.__provMessage(text) is the first element with data-testid "message" whose text contains text, and window.__provCard(text, headline, background, tag) checks that message's tool card: "ok", or what is wrong. "The card of M" is the element with data-testid "conversation-message-card" whose text contains M (has_text); look inside it with "within". Links are picked by role "link" and exact name. 1. Navigate to /c/{{SLUG}}. 2. The element with data-testid "message-input" should be visible (allow 30s). 3. The first element with data-testid "message" whose text contains "subagent: backend echo: Ready." (has_text) should be visible (allow 30s): the history has loaded. 4. Call "progress": the subagent sends this conversation a two-line progress report. 5. %s. 6. The card of "Backend underway" should show a visible link named exactly "{{CHILD}}". 7. Click the card's button named "Expand". 8. Eval window.__provCardOf('Backend underway').querySelector('.tool-details .tool-code').textContent === {{PROGRESS}}: it must return true (the verbatim report). 9. The card's link named exactly "https://example.com/api" should have the href attribute "https://example.com/api". 10. Reload the page. 11. %s (the stored message, after the reload). 12. The card's link named exactly "{{CHILD}}" should have the href attribute "/c/{{CHILD_ID}}" and the title attribute "Open subagent conversation". 13. Click it. 14. ".header-title" should read exactly "{{CHILD}}". 15. %s: the parent's delegated prompt. 16. The card of "echo: Ready." should show a visible link named exactly "{{SLUG}}". 17. Eval window.__provHeadline('echo: Ready.'): it must return exactly '%s' (the short message is one line, %s, and not clipped). 18. Call "instruct": the parent sends this subagent "Please finish the API tests before the database changes.". 19. %s. 20. The card of "Please finish the API tests" must hold exactly 0 buttons named "Expand": a short single-line message has nothing to expand. 21. That card should show a visible link named exactly "{{SLUG}}". 22. Call "rename": the parent conversation is renamed {{RENAMED}}. 23. The card of "Please finish the API tests" should come to have a link named exactly "{{RENAMED}}", with the href attribute "/c/{{PARENT_ID}}" and the title attribute "Open parent conversation". 24. Click it. 25. ".header-title" should read exactly "{{RENAMED}}". 26. The first element with data-testid "message" whose text contains "Backend underway" (has_text) should show a visible link named exactly "{{CHILD}}". 27. Eval window.__provMessage('subagent: backend echo: Ready.').classList.contains('message-user'): it must return true. 28. Eval window.__provMessage('subagent: backend echo: Ready.').querySelectorAll('[data-testid="conversation-message-card"]').length: it must return exactly "0". 29. Eval document.documentElement.scrollWidth <= window.innerWidth: it must return true (nothing overflows sideways).`,
		width, height, scheme,
		card("Backend underway", provenanceHeadline, "message"),
		card("Backend underway", provenanceHeadline, "message"),
		card("echo: Ready.", "echo: Ready.", "from parent"),
		layout, placement,
		card("Please finish the API tests", "Please finish the API tests before the database changes.", "from parent"),
	), func(s *shelley) {
		s.viewport(width, height)
		s.media("prefers-color-scheme", scheme)
		s.initScript(provenancePage)
		parent := s.conversation("subagent: backend echo: Ready.", "")
		var children []struct {
			ID   string `json:"conversation_id"`
			Slug string `json:"slug"`
		}
		s.api("GET", "/api/conversation/"+parent.ID+"/subagents", nil, &children)
		if len(children) == 0 || children[0].ID == "" {
			s.fail("no subagent: %+v", children)
		}
		child := children[0]
		renamed := "parent-renamed-" + strings.ToLower(parent.ID)
		s.v("SLUG", parent.Slug)
		s.v("PARENT_ID", parent.ID)
		s.v("CHILD", child.Slug)
		s.v("CHILD_ID", child.ID)
		s.v("RENAMED", renamed)
		s.v("PROGRESS", jsString(provenanceProgress))
		s.call("progress", func() string {
			s.provenanceSend(child.ID, parent.ID, provenanceProgress)
			return "sent"
		})
		s.call("instruct", func() string {
			s.provenanceSend(parent.ID, child.ID, "Please finish the API tests before the database changes.")
			return "sent"
		})
		s.call("rename", func() string {
			s.api("POST", "/api/conversation/"+parent.ID+"/rename", map[string]string{"slug": renamed}, nil)
			return "renamed"
		})
	})
}

// jsString is s as a JavaScript string literal.
func jsString(s string) string {
	return mustJSON(s)
}

func TestE2EConversationProvenancePhoneLight(t *testing.T) {
	provenanceCards(t, 390, 844, "light")
}

func TestE2EConversationProvenancePhoneDark(t *testing.T) {
	provenanceCards(t, 390, 844, "dark")
}

func TestE2EConversationProvenanceDesktopLight(t *testing.T) {
	provenanceCards(t, 1280, 720, "light")
}

func TestE2EConversationProvenanceDesktopDark(t *testing.T) {
	provenanceCards(t, 1280, 720, "dark")
}

func TestE2EConversationProvenanceQueuedAndTranscribing(t *testing.T) {
	e2e(t, `Queued and transcribing conversation messages share the source header. The conversation /c/{{SLUG}} was started with "echo: queue provenance". The test adds three queued messages, all created one second after the page's fake clock starts, to what the server says of it, and cuts its stream off: "Queued progress" from the subagent "backend", and two voice transcriptions (one working, one failed) from the parent conversation "{{SLUG}}". The page runs on a fake clock that steps drive with window.__clock; it is paused at the messages' creation time when the page loads. "The ghost" is the element with data-testid "queued-ghost", "the tasks" the elements with data-testid "transcription-task". 1. Navigate to /c/{{SLUG}}. 2. There must be exactly 2 tasks (allow 30s). 3. There must be exactly 0 ghosts. 4. Eval window.__clock.runFor(1999) on its own (it returns a promise the step awaits). 5. There must still be exactly 0 ghosts. 6. Eval window.__clock.runFor(1) on its own. 7. The ghost's element with data-testid "message-author-conversation" should read exactly "Message from backend". 8. Eval document.querySelector('[data-testid="queued-ghost"]').classList.contains('message-tool'): it must return true. 9. Eval getComputedStyle(document.querySelector('[data-testid="queued-ghost"]')).backgroundColor: it must return exactly "rgb(243, 244, 246)". 10. Eval getComputedStyle(document.querySelector('[data-testid="queued-ghost"] [data-testid="message-content"]')).borderTopStyle: it must return exactly "dashed". 11. The ghost should contain "Queued progress". 12. For each task, i = 0 and i = 1 of document.querySelectorAll('[data-testid="transcription-task"]'): eval getComputedStyle(<task i>).backgroundColor, which must return exactly "rgb(243, 244, 246)", and eval <task i>.querySelector('[data-testid="message-author-conversation"]').textContent.replace(/\s+/g, ' ').trim(), which must return exactly "Message from {{SLUG}}".`, func(s *shelley) {
		parent := s.conversation("echo: queue provenance", "")
		s.v("SLUG", parent.Slug)
		start := time.Now().Truncate(time.Millisecond)
		queuedAt := start.Add(time.Second)
		s.clock(start)
		s.initScript(fmt.Sprintf("window.__clock.pauseAt(%d);", queuedAt.UnixMilli()))
		created := queuedAt.UTC().Format("2006-01-02T15:04:05.000Z")
		source := map[string]any{
			"sender_conversation_id": "child",
			"sender_slug":            "backend",
			"sender_relationship":    "subagent",
			"Text":                   "Queued progress",
		}
		queued := []any{map[string]any{
			"id":         "queued-source",
			"created_at": created,
			"model":      "predictable",
			"llm":        map[string]any{"Role": 0, "Content": []any{map[string]any{"Type": 2, "Text": "Queued progress"}}},
			"user_data":  source,
		}}
		for _, state := range []string{"working", "failed"} {
			queued = append(queued, map[string]any{
				"id":            "transcription-" + state,
				"created_at":    created,
				"model":         "predictable",
				"kind":          "transcription",
				"state":         state,
				"transcription": map[string]any{"media_path": "/tmp/progress.webm", "context": "Progress report"},
				"user_data": map[string]any{
					"sender_conversation_id": parent.ID,
					"sender_slug":            parent.Slug,
					"sender_relationship":    "parent",
					"Text":                   "Queued progress",
				},
			})
		}
		// The page gets the queue as the server sends it: a JSON string.
		queue := mustJSON(queued)
		s.route("GET /api/stream2", abort)
		s.route("GET /api/conversations/snapshot", func(w http.ResponseWriter, r *http.Request) {
			var raw map[string]any
			if err := s.fetchJSON(r, &raw); err != nil {
				abort(w, r)
			}
			for _, c := range raw["conversations"].([]any) {
				if c := c.(map[string]any); c["conversation_id"] == parent.ID {
					c["queued_messages"] = queue
				}
			}
			writeJSON(w, raw)
		})
		s.route("GET /api/conversation/{id}", func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") != parent.ID {
				s.pass.ServeHTTP(w, r)
				return
			}
			var raw map[string]any
			if err := s.fetchJSON(r, &raw); err != nil {
				abort(w, r)
			}
			raw["conversation"].(map[string]any)["queued_messages"] = queue
			writeJSON(w, raw)
		})
	})
}
