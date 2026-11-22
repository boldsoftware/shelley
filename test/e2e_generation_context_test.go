package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// Ported from ui/e2e/generation-context.spec.ts.

// generationContextPage helps the descriptions pick the context card's
// rows the way the spec did: the model bar, and the button whose
// accessible name has "System Prompt:" (its text; the icon is
// aria-hidden). The button's innerText stands in for the accessible name
// the spec matched: it holds the screen-reader "Expand" or "Collapse" as a
// line of its own, where textContent runs it into the counts.
const generationContextPage = `window.__card = () => document.querySelector('[data-testid="generation-context"]');
window.__promptButton = () => {
  const card = window.__card();
  return card ? [...card.querySelectorAll('button')].find((b) => /System Prompt:/.test(b.textContent)) || null : null;
};
window.__promptText = () => window.__promptButton().innerText.replace(/\s+/g, ' ').trim();`

func TestE2EGenerationContextLatestSystemPrompt(t *testing.T) {
	e2e(t, useful(0), `After in-place compaction refreshes the system prompt, the context card shows only the latest one. The conversation /c/{{SLUG}}'s history, as the page loads it, has its original system prompt and then two refreshed copies, "Refreshed system prompt 1" and "Refreshed system prompt 2"; the page's live stream is cut off. "The card" is the element with data-testid "generation-context". 1. Navigate to /c/{{SLUG}}. 2. There should be exactly 1 element matching '[data-testid="generation-context"] > .system-prompt-view' (a direct child of the card). 3. Click the card's button (role "button", within the card) whose name contains "System Prompt:". 4. The card's ".system-prompt-text" should read exactly "Refreshed system prompt 2". 5. The card's ".model-bar" should be visible.`, func(s *shelley) {
		c := s.conversation("echo: refreshed context card", "")
		s.v("SLUG", c.Slug)
		var body map[string]any
		s.api("GET", "/api/conversation/"+c.ID, nil, &body)
		messages, _ := body["messages"].([]any)
		var original map[string]any
		for _, m := range messages {
			if m := m.(map[string]any); m["type"] == "system" {
				original = m
				break
			}
		}
		if original == nil {
			s.fail("no system message")
		}
		last := messages[len(messages)-1].(map[string]any)["sequence_id"].(float64)
		for revision := 1; revision <= 2; revision++ {
			m := make(map[string]any, len(original))
			for k, v := range original {
				m[k] = v
			}
			m["message_id"] = fmt.Sprintf("refreshed-system-%d", revision)
			m["sequence_id"] = last + float64(revision)
			m["llm_data"] = mustJSON(map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": fmt.Sprintf("Refreshed system prompt %d", revision)}}})
			m["display_data"] = mustJSON(map[string]any{"tools": []any{}, "skills": []any{}})
			messages = append(messages, m)
		}
		body["messages"] = messages
		fixed, err := json.Marshal(body)
		s.check(err)
		s.route("/api/stream2", abort)
		s.route("GET /api/conversation/"+c.ID, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" {
				// The spec's route matched the bare URL only.
				s.pass.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixed)
		})
	})
}

func TestE2EGenerationContextOneCard(t *testing.T) {
	e2e(t, useful(0), `The model and the system prompt are grouped into one context card. The window starts 1000x800. The conversation /c/{{SLUG}} was started with "echo: context card". "The card" is the element with data-testid "generation-context"; "the model bar" is '[data-testid="generation-context"] > .model-bar' (a direct child). window.__promptButton() is the card's button whose name contains "System Prompt:" (role "button" with that name, within the card, picks the same one), and window.__promptText() its rendered text (innerText, which has the screen-reader "Expand" or "Collapse" on a line of its own) with whitespace collapsed. 1. Navigate to /c/{{SLUG}}. 2. The element with data-testid "message-input" should be visible (allow 30s). 3. The card should be visible; so should the model bar. There must be exactly 1 '[data-testid="generation-context"] > .system-prompt-view' (it is display: contents, with no box of its own to be visible; the prompt button inside it shows). 4. The prompt button should be visible, and eval /System Prompt:\s*\d+ tools?\s*,?\s*\d+ skills? Expand/.test(window.__promptText()) must return true. The screen-reader text must be part of its accessible name too: the card's button named "Expand" (role "button" with that name, within the card) should be visible. 5. The model bar should contain "Model:". 6. The model bar must not contain "Reasoning" (assert_text_not_contains). 7. Eval /System Prompt:\s*\d+ tools?,\s*\d+ skills?/.test(window.__promptText()): true. 8. Eval (() => { const cs = getComputedStyle(window.__promptButton()); return cs.boxShadow + ' ' + cs.borderLeftWidth; })(): exactly "none 0px". 9. In one eval without "expect", make the model name very long: document.querySelector('[data-testid="generation-context"] > .model-bar .model-bar-name').textContent = 'a-very-long-custom-model-name-'.repeat(12). 10. Eval (() => { const card = window.__card().getBoundingClientRect(); const p = window.__promptButton().getBoundingClientRect(); return p.width > 200 && p.right <= card.right + 1; })(): true. 11. The long name ellipsizes: eval (() => { const e = document.querySelector('[data-testid="generation-context"] > .model-bar .model-bar-name'); return e.scrollWidth > e.clientWidth; })(): true. 12. The ", <reasoning>" after it stays fully visible, clipped by no ancestor: eval (() => { const el = document.querySelector('[data-testid="generation-context"] > .model-bar .model-bar-reasoning'); const range = document.createRange(); range.selectNodeContents(el); const r = range.getBoundingClientRect(); const y = r.top + r.height / 2; return JSON.stringify([r.left + 1, r.right - 1].map((x) => document.elementFromPoint(x, y) === el)); })(): exactly "[true,true]". 13. The model bar and the prompt button share a line: eval (() => { const m = document.querySelector('[data-testid="generation-context"] > .model-bar').getBoundingClientRect(); const p = window.__promptButton().getBoundingClientRect(); return Math.abs(m.top - p.top) < 1 && Math.abs(m.bottom - p.bottom) < 1; })(): true. 14. Set the viewport to 540x800 (set_viewport). 15. The prompt button should be visible. 16. In one eval without "expect", record window.__skills = Number(window.__promptText().match(/(\d+) skills?/)[1]) and window.__tools = Number(window.__promptText().match(/(\d+) tools?/)[1]). 17. Eval window.__skills > 0 && window.__tools > 0: true. 18. Click the prompt button (role "button" named "System Prompt:" within the card). 19. Eval /System Prompt:\s*\d+ tools?\s*,?\s*\d+ skills? Collapse/.test(window.__promptText()) until it returns true, and the card's button named "Collapse" (role "button" with that name, within the card) should be visible. 20. The card's ".system-prompt-content" should be visible. 21. Eval window.__card().querySelectorAll('.system-prompt-skill-item').length === window.__skills && window.__card().querySelectorAll('.system-prompt-tool-item').length === window.__tools: true. 22. "The skill card" is the card's ".system-prompt-skill-item" whose ".system-prompt-card-name" reads exactly "commit-highlights" (a built-in skill; integration skills may come first, so pick it by name; use "within" with has_text "commit-highlights"). Click its ".system-prompt-card-summary". 23. Its ".system-prompt-card-detail" should be visible and contain "Source". 24. The first "code" element in its ".system-prompt-card-detail" should contain "SKILL.md". 25. "The tool card" is the card's first ".system-prompt-tool-item". Click its ".system-prompt-card-summary". 26. Its ".system-prompt-card-detail" should be visible and contain "Source". 27. The first "code" element in its ".system-prompt-card-detail" should contain "claudetool/". 28. The card's two rows draw no surface of their own: eval JSON.stringify([...window.__card().querySelectorAll(':scope > .model-bar, :scope > .system-prompt-view')].map((e) => { const s = getComputedStyle(e); return [s.backgroundColor, s.borderTopWidth, s.borderRightWidth, s.borderBottomWidth, s.borderLeftWidth].join(' '); })), which must return exactly `+mustJSON(`["rgba(0, 0, 0, 0) 0px 0px 0px 0px","rgba(0, 0, 0, 0) 0px 0px 0px 0px"]`)+`.`, func(s *shelley) {
		s.viewport(1000, 800)
		s.initScript(generationContextPage)
		s.v("SLUG", s.conversation("echo: context card", "").Slug)
	})
}

// generationContextLooks measures the card's two rows, each [icon tile]
// [label] [value], picking the parts as the spec did: the label is the
// innermost element whose text is exactly "Model:" or "System Prompt:",
// the value its next sibling, the icon the row's first aria-hidden
// element.
const generationContextLooks = `window.__ctxText = ['font-family', 'font-size', 'font-weight', 'color'];
window.__ctxTile = ['width', 'height', 'background-color', 'border-radius', 'font-size'];
window.__ctxRow = (which) => which === 'model'
  ? document.querySelector('[data-testid="generation-context"] > .model-bar')
  : window.__promptButton();
window.__ctxParts = (which) => {
  const row = window.__ctxRow(which);
  const norm = (s) => s.replace(/\s+/g, ' ').trim();
  const matches = [...row.querySelectorAll('*')].filter((e) => /^(Model|System Prompt):$/.test(norm(e.textContent)));
  const label = matches.find((e) => !matches.some((o) => o !== e && e.contains(o)));
  if (!label) throw new Error('no label in the ' + which + ' row');
  return { icon: row.querySelector("[aria-hidden='true']"), label, value: label.nextElementSibling };
};
window.__ctxLook = (which, part, props) => {
  const cs = getComputedStyle(window.__ctxParts(which)[part]);
  return JSON.stringify(Object.fromEntries(props.map((n) => [n, cs.getPropertyValue(n)])));
};
window.__ctxSpacing = (which) => {
  const { icon, label, value } = window.__ctxParts(which);
  const [i, l, v] = [icon, label, value].map((e) => e.getBoundingClientRect());
  return JSON.stringify([Math.round(l.left - i.right), Math.round(v.left - l.right)]);
};
window.__ctxCommaGaps = (which) => {
  const root = window.__ctxRow(which);
  const glyphs = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const s = node.textContent || '';
    for (let i = 0; i < s.length; i++) {
      const range = document.createRange();
      range.setStart(node, i);
      range.setEnd(node, i + 1);
      const rect = range.getBoundingClientRect();
      if (rect.width > 0 && s[i].trim()) glyphs.push({ ch: s[i], rect });
    }
  }
  return glyphs.flatMap((g, i) => (g.ch === ',' && i > 0 ? [g.rect.left - glyphs[i - 1].rect.right] : []));
};`

func TestE2EGenerationContextSharedLook(t *testing.T) {
	e2e(t, useful(0), `The model and system prompt rows share one look. The window starts 1400x800. The conversation /c/{{SLUG}} was started with "echo: context styles". The context card (data-testid "generation-context") has two rows: "the model row" ('[data-testid="generation-context"] > .model-bar') and "the prompt row" (the card's button named "System Prompt:"). Each row is [icon tile] [label] [value]. The page provides, for which = 'model' or 'prompt': window.__ctxLook(which, part, props), the JSON of the computed styles props of part 'icon', 'label' or 'value'; window.__ctxSpacing(which), the JSON of the gaps icon-to-label and label-to-value; window.__ctxCommaGaps(which), the horizontal gap before every comma glyph in the row; window.__ctxParts(which).label, the label element; and the property lists window.__ctxText (text styles) and window.__ctxTile (icon tile styles). 1. Navigate to /c/{{SLUG}}. 2. The model row should be visible, and so should the prompt row (role "button" named "System Prompt:" within the card). 3. Each of these evals must return true: __ctxLook('model', 'label', __ctxText) === __ctxLook('prompt', 'label', __ctxText); __ctxLook('model', 'value', __ctxText) === __ctxLook('prompt', 'value', __ctxText); __ctxLook('model', 'icon', __ctxTile) === __ctxLook('prompt', 'icon', __ctxTile); __ctxSpacing('model') === __ctxSpacing('prompt'). 4. Each row has exactly one comma, sitting right against the glyph before it, with no space or flex gap ("Claude Opus 5.5, xhigh" and "9 tools, 11 skills"): for which = 'model' and then 'prompt', eval (() => { const g = __ctxCommaGaps(which); return g.length === 1 && Math.abs(g[0]) < 1; })() with which filled in; each must return true. 5. Eval /Model:\s*\S.*\S, \S+$/.test(document.querySelector('[data-testid="generation-context"] > .model-bar').textContent.replace(/\s+/g, ' ').trim()): true. 6. Set the viewport to 390x800 (set_viewport): phones hide the labels but keep the icon tiles and values identical. 7. The prompt row should be visible. 8. The layout switches on a media query listener, so it settles after the resize: in a retry block, eval this until it returns true: __ctxParts('model').label.getBoundingClientRect().width <= 1 && __ctxParts('prompt').label.getBoundingClientRect().width <= 1 && __ctxLook('model', 'value', __ctxText) === __ctxLook('prompt', 'value', __ctxText) && __ctxLook('model', 'icon', __ctxTile) === __ctxLook('prompt', 'icon', __ctxTile).`, func(s *shelley) {
		s.viewport(1400, 800)
		s.initScript(generationContextPage + "\n" + generationContextLooks)
		s.v("SLUG", s.conversation("echo: context styles", "").Slug)
	})
}
