package test

import (
	"net/http"
	"testing"
)

// Ported from ui/e2e/path-text.spec.ts.

// pathTextGeometry measures the MessageInput.vue label (the element
// window.__label() returns): whether the prefix has a width and sits before
// the tail's clip box, whether the path's last four characters are inside
// that clip box, whether the row's icon comes before the prefix, and
// whether the page overflows sideways.
const pathTextGeometry = `(() => { const el = window.__label(); const prefix = el.querySelector(".path-text-prefix").getBoundingClientRect(); const tail = el.querySelector(".path-text-tail"); const clip = tail.getBoundingClientRect(); const walker = document.createTreeWalker(tail, NodeFilter.SHOW_TEXT); let last = null; for (let node = walker.nextNode(); node; node = walker.nextNode()) { if (node.length) last = node; } const range = document.createRange(); range.setStart(last, last.length - 4); range.setEnd(last, last.length); const extension = range.getBoundingClientRect(); const row = el.closest(".grp-row"); return { prefixWidthPositive: prefix.width > 0, prefixBeforeTail: prefix.right <= clip.left + 1, extensionVisible: extension.left >= clip.left - 1 && extension.right <= clip.right + 1, iconBeforePath: row.querySelector(".grp-icon").getBoundingClientRect().right <= prefix.left, pageOverflow: document.documentElement.scrollWidth > innerWidth }; })()`

// pathTextOverflow says whether the label's tail overflows its box.
const pathTextOverflow = `(() => { const tail = window.__label().querySelector(".path-text-tail"); return tail.scrollWidth > tail.clientWidth; })()`

func TestE2EPathTextLabels(t *testing.T) {
	e2e(t, useful(0), `Path labels keep the home prefix, highlights, tail and full-path hover. The test itself answers the page's requests for the git repository list (GET /api/git/repos), already in place before the first step (steps must not patch fetch or stub anything): the list has two repositories, {{PATH}} (on branch main) and the Unicode path {{UNICODE}}, with home root /home/adam. "The picker" is the dialog whose accessible name is exactly "Pick git repository". "The label" is the ".grp-path" element inside the picker whose text contains "MessageInput.vue" (pick it with "within" the picker and has_text), and "the Unicode label" the picker's ".grp-path" whose text contains "דוח-final.v2.json". For evals, window.__label() returns the label and window.__unicodeLabel() the Unicode label. "Raw text" means the element's textContent exactly, read with an eval with "expect" (no whitespace collapsing). Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. Click the button in ".chat-workspace-actions" whose accessible name is exactly "Git Graph". 3. Click the button whose accessible name is exactly "Pick repository". 4. The label should be visible. 5. The label's title attribute must be exactly {{PATH}}. 6. The label's raw text must be exactly "~/projects/shelley/ui/src/vue/components/MessageInput.vue". 7. The label's ".path-text-prefix" must have exactly the text "~/". 8. The label's ".path-text-tail" must have computed CSS direction exactly "rtl" and computed text-align exactly "left". 9. The tail's ::before content must be a quoted left-to-right mark: eval getComputedStyle(window.__label().querySelector(".path-text-tail"), "::before").content === '"\u200e"' expecting true. 10. Fill the picker's textbox whose accessible name is exactly "Filter" with "messageinput". 11. The label's "mark" elements must be exactly one, with exactly the text "MessageInput" (assert_texts). 12. There must be exactly 0 "mark[title]" elements in the label. 13. The label's raw text must still be exactly "~/projects/shelley/ui/src/vue/components/MessageInput.vue". 14. For each width in turn, 1280, then 390, then 320: set the viewport to that width and height 800 (set_viewport with only width and height, keeping the rest of the device); the label's ".path-text-prefix" should be visible; the label's title must still be exactly {{PATH}}; and this eval of the label's geometry must give exactly {"extensionVisible":true,"iconBeforePath":true,"pageOverflow":false,"prefixBeforeTail":true,"prefixWidthPositive":true}: `+pathTextGeometry+`. At 320 px only, the tail must also overflow: this eval must give true: `+pathTextOverflow+`. 15. Fill the Filter textbox with "" (empty). 16. The Unicode label's title attribute must be exactly {{UNICODE}}. 17. Its raw text must be exactly {{UNICODE}}. 18. There must be exactly 0 ".path-text-prefix" elements inside the Unicode label (assert_count with "within" {".grp-path" with has_text "דוח-final.v2.json"} and selector ".path-text-prefix"; has_text belongs on the within, not on the counted element).`, func(s *shelley) {
		s.viewport(1280, 800)
		const home = "/home/adam"
		path := home + "/projects/shelley/ui/src/vue/components/MessageInput.vue"
		unicodePath := "/var/work trees/shelley (review)/التقارير/דוח-final.v2.json"
		s.v("PATH", path)
		s.v("UNICODE", unicodePath)
		s.initScript(`const pathTextLabel = (text) => Array.from(document.querySelectorAll('[role="dialog"] .grp-path')).find((el) => el.textContent.includes(text));
window.__label = () => pathTextLabel("MessageInput.vue");
window.__unicodeLabel = () => pathTextLabel("דוח-final.v2.json");`)
		s.route("/api/git/repos", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"repos":     []map[string]any{{"path": path, "branch": "main"}, {"path": unicodePath}},
				"roots":     []string{home},
				"truncated": false,
			})
		})
		s.v("SLUG", s.conversation("Hello", "").Slug)
	})
}
