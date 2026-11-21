package test

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// Ported from ui/e2e/conversation-grouping.spec.ts and
// ui/e2e/conversation-sort-bucketing.spec.ts.

func TestE2EGroupingActiveNotOther(t *testing.T) {
	e2e(t, `active conversation should not be grouped under Other when grouped by git repo. Conversations {{A}} and {{B}} (slug {{B_SLUG}}) both run in the server's git repository. `+drawerDirect+drawerSteps+`1. Navigate to /c/{{B_SLUG}} and wait up to 30s for data-testid "message-input" to be visible. 2. Click "button[aria-label='Open conversations']". 3. ".drawer.open" is visible. 4. Click "button[aria-label='View options']". 5. Click {"role": "button", "name": "Git Repo"}. 6. Exactly one ".conversation-group" holds the active row: assert_count of ".conversation-group:has(.conversation-item.active)" is 1. 7. Its label does not read "Other", read once as the old test did: eval WITHOUT expect (it runs once, and fails if it throws) of (() => { const l = document.querySelector(".conversation-group:has(.conversation-item.active) .conversation-group-label"); if (!l) throw new Error("the active group has no label"); const label = l.innerText.trim(); if (label === "Other") throw new Error("the active conversation is under Other"); return label; })().`, func(s *shelley) {
		var diffs struct {
			GitRoot string `json:"gitRoot"`
		}
		s.api("GET", "/api/git/diffs?cwd=.", nil, &diffs)
		if diffs.GitRoot == "" {
			s.fail("no git root")
		}
		for _, x := range []string{"A", "B"} {
			c := s.conversationWith(map[string]any{"message": "Hello from conversation " + x, "model": "predictable", "cwd": diffs.GitRoot}, "")
			s.v(x, c.ID)
			s.v(x+"_SLUG", c.Slug)
		}
	})
}

// Playwright read the order once the reply landed, and asserted it held if
// both conversations still shared a 5-minute bucket of updated_at, else
// that the newer bucket came first; the page does the same here, fetching
// updated_at itself.
func TestE2ESortBucketingNoFlipFlop(t *testing.T) {
	e2e(t, `does not flip-flop conversations updated within the same 5-minute bucket. Conversations {{A}} (slug {{A_SLUG}}) and {{B}} (slug {{B_SLUG}}) were created back to back. Call "poke" sends {{A}} the message "poke" and waits for its reply, returning "replied". `+drawerDirect+drawerSteps+`1. Navigate to /c/{{B_SLUG}} and wait up to 30s for data-testid "message-input" to be visible. 2. Click "button[aria-label='Open conversations']". 3. ".drawer.open" is visible. 4. A ".drawer.open .conversation-title" reading exactly "{{A_SLUG}}" is visible (has_text with exact), and so is one reading exactly "{{B_SLUG}}". 5. Note the order, which lists both: eval with expect "true" of (() => { const o = [...document.querySelectorAll(".drawer .conversation-item .conversation-title")].map(e => e.innerText); window.__initialOrder = o; return String(o.includes("{{A_SLUG}}") && o.includes("{{B_SLUG}}")); })(). 6. Call "poke" with timeout "40s" and no expect (it sends a message, so it must run once; it fails if no reply comes). 7. The order held within a bucket, or follows the buckets, read once as the old test did: eval WITHOUT expect (it runs once, and fails if it throws) of (async () => { const at = async id => (await (await fetch("/api/conversation/" + id)).json()).conversation.updated_at; const bucket = t => Math.floor(new Date(t).getTime() / 300000); const final = [...document.querySelectorAll(".drawer .conversation-item .conversation-title")].map(e => e.innerText); const initial = window.__initialOrder; const a = bucket(await at("{{A}}")), b = bucket(await at("{{B}}")); const fa = final.indexOf("{{A_SLUG}}"), fb = final.indexOf("{{B_SLUG}}"); if (a === b) { if (fa !== initial.indexOf("{{A_SLUG}}") || fb !== initial.indexOf("{{B_SLUG}}")) throw new Error("reordered: " + JSON.stringify(final)); return "held"; } if (!(a > b ? fa < fb : fb < fa)) throw new Error("not by bucket: " + JSON.stringify(final)); return "by bucket"; })().`, func(s *shelley) {
		a := s.conversation("first conversation", "")
		b := s.conversation("second conversation", "")
		s.v("A", a.ID)
		s.v("A_SLUG", a.Slug)
		s.v("B", b.ID)
		s.v("B_SLUG", b.Slug)
		s.call("poke", func() string {
			s.api("POST", "/api/conversation/"+a.ID+"/chat", map[string]any{"message": "poke", "model": "predictable"}, nil)
			s.poll(30*time.Second, func() error {
				var got struct {
					Messages []apiMessage `json:"messages"`
				}
				s.api("GET", "/api/conversation/"+a.ID, nil, &got)
				if n := len(slices.DeleteFunc(got.Messages, func(m apiMessage) bool { return !m.endsTurn() })); n < 2 {
					return errors.New("no reply yet")
				}
				return nil
			})
			return "replied"
		})
	})
}
