package test

import (
	"fmt"
	"strings"
	"testing"
)

// Ported from ui/e2e/markdown.spec.ts. Tests make their conversations
// through the API and then open them, which avoids the race between the
// page's first stream subscription and the first turn being recorded.

// markdownReply names the reply the markdown tests check.
const markdownReply = `"The reply" is the ".message-agent" element (the conversation has just one, so select it as plain ".message-agent", never with :last-of-type). `

func TestE2EMarkdownFormatting(t *testing.T) {
	e2e(t, useful(0), `Markdown formatting in agent messages renders as HTML. The conversation /c/{{SLUG}} was started with "markdown: **bold** and *italic* and `+"`code`"+`", which the predictable model answers with that markdown. `+markdownReply+`1. Navigate to /c/{{SLUG}}. 2. The reply's first "strong" element should contain "bold" (allow 30s). 3. Its first "em" element should contain "italic". 4. Its first "code" element should contain "code".`, func(s *shelley) {
		s.v("SLUG", s.conversation("markdown: **bold** and *italic* and `code`", "").Slug)
	})
}

// markdownFences are the fences the highlighting test sends: languages
// the highlighter bundles, with their source, then an unknown language and
// a fence with none.
var markdownFences = [][2]string{
	{"elixir", "answer = 42\n"},
	{"haskell", "answer :: Int\nanswer = 42\n"},
	{"zig", "const answer: i32 = 42;\n"},
	{"Dockerfile", "FROM alpine:latest\n"},
	{"nix", "answer = 42;\n"},
	{"clojure", "(def answer 42)\n"},
	{"clj", "(def alias-answer 42)\n"},
}

const (
	markdownUnknownSource   = "leave_this_plain();\n"
	markdownUnlabeledSource = "also_plain();\n"
)

// markdownBlockPage helps the highlighting tests pick and scroll to the
// reply's code blocks.
const markdownBlockPage = `window.__block = (i) => {
  const agents = document.querySelectorAll('.message-agent');
  const last = agents[agents.length - 1];
  return (last && last.querySelectorAll('pre > code')[i]) || null;
};
window.__scrollToBlock = (i) => {
  const container = document.querySelector('.messages-container');
  if (container) container.dispatchEvent(new WheelEvent('wheel', { deltaY: -200, bubbles: true }));
  const block = window.__block(i);
  if (block) block.scrollIntoView({ block: 'center' });
  return 'scrolled';
};`

func TestE2EMarkdownHighlightsBundledLanguages(t *testing.T) {
	// Highlighting waits until a block enters the scrollport (see the next
	// test). Nine fences, each with a language/copy header, no longer fit
	// on screen at once, so each block is scrolled in before its check. The
	// wheel event disarms auto-follow first, or its pin snaps the view back
	// to the bottom.
	var md []string
	var checks strings.Builder
	for i, f := range markdownFences {
		md = append(md, "```"+f[0]+"\n"+f[1]+"```")
		fmt.Fprintf(&checks, "Block %d: class exactly %q, source %s. ", i, "language-"+f[0], mustJSON(f[1]))
	}
	md = append(md, "```unknown-language\n"+markdownUnknownSource+"```", "```\n"+markdownUnlabeledSource+"```")
	message := "markdown: " + strings.Join(md, "\n\n")
	e2e(t, useful(0), `Fences in the bundled languages and their aliases are highlighted; fences in an unknown language or none stay plain. The conversation /c/{{SLUG}} was started with a message whose markdown has nine fenced code blocks, which the predictable model answers with. window.__block(i) is the reply's i-th "pre > code" element (0-based; the reply being the last ".message-agent"), and eval window.__scrollToBlock(i) (it returns "scrolled") disarms auto-follow with a wheel event and scrolls block i to the middle of the view. "Block i has tokens" means eval window.__block(i).querySelector('.shelley-code-token') !== null returns true (allow 30s: highlighting runs once the block is in view). The highlighted blocks, with their exact class attribute and exact textContent (as JSON strings: each ends in a newline): `+checks.String()+`1. Navigate to /c/{{SLUG}}. 2. Eval window.__block(8) !== null && window.__block(9) === null: it must return true (exactly nine blocks; allow 30s). 3. For each highlighted block i from 0 to 6, in order: eval window.__block(i).className, which must return exactly its class; eval window.__scrollToBlock(i); block i has tokens; eval window.__block(i).textContent === S, S being its source as a JavaScript string literal, which must return true. 4. Eval window.__scrollToBlock(7), then window.__scrollToBlock(8): so that "no tokens" below means the highlighter declined them, not that it never looked. 5. Eval window.__block(7).className: exactly "language-unknown-language". 6. Eval window.__block(8).getAttribute('class') === null: true (the unlabeled fence has no class at all). 7. Eval window.__block(7).textContent === `+mustJSON(markdownUnknownSource)+`: true. 8. Eval window.__block(8).textContent === `+mustJSON(markdownUnlabeledSource)+`: true. 9. Eval window.__block(7).querySelectorAll('.shelley-code-token').length: exactly "0". 10. Eval window.__block(8).querySelectorAll('.shelley-code-token').length: exactly "0". 11. Eval window.__scrollToBlock(0); block 0 has tokens. 12. In one eval without "expect", record window.__tokenCount = window.__block(0).querySelectorAll('.shelley-code-token').length and window.__lightColor = getComputedStyle(window.__block(0).querySelector('.shelley-code-token')).color. 13. Eval document.documentElement.classList.toggle('dark', true), switching to the dark theme. 14. Eval getComputedStyle(window.__block(0).querySelector('.shelley-code-token')).color !== window.__lightColor until it returns true: the tokens recolor. 15. Eval window.__block(0).querySelectorAll('.shelley-code-token').length === window.__tokenCount: true (recoloring does not re-tokenize).`, func(s *shelley) {
		s.initScript(markdownBlockPage)
		s.v("SLUG", s.conversation(message, "").Slug)
	})
}

func TestE2EMarkdownDefersHighlighting(t *testing.T) {
	// Every loremipsum turn carries a ```go fence. In a 17k-message
	// conversation that was 5,000 eagerly-highlighted blocks = 65k token
	// spans = 22% of the DOM, almost all of it dozens of screens above the
	// fold. Highlighting must instead hydrate like tool cards do: only once
	// the block comes within a viewport of view (sticky thereafter).
	e2e(t, useful(0), `Fence highlighting waits until a block comes near the view. The conversation {{ID}} is a medium /debug/loremipsum conversation: every agent turn carries a code fence. "The blocks" are the ".message-agent pre > code" elements, in document order. 1. Navigate to /c/{{ID}}. 2. The first element with data-testid "message" should be visible (allow 30s). 3. Eval (() => { const b = document.querySelectorAll('.message-agent pre > code'); return b.length > 0 && b[b.length - 1].querySelector('.shelley-code-token') !== null; })() until it returns true (allow 30s): the page loads pinned to the bottom, so the newest fence highlights. This wait is the barrier that makes the next checks meaningful: eager highlighting would have tokenized every block by now. 4. Eval document.querySelectorAll('.message-agent pre > code').length: it must be exactly "50". 5. Eval document.querySelector('.message-agent pre > code').querySelectorAll('.shelley-code-token').length: exactly "0" (the first fence, far above the fold, is still plain text). 6. Eval document.querySelectorAll('.shelley-code-token').length < 200: true (only the few blocks on screen are tokenized; each fence is about 13 tokens, so 50 eager blocks would be about 650). 7. Scroll the first block into the scrollport, disarming auto-follow first (without the wheel event its pin snaps the view back to the bottom), in exactly this eval: (() => { document.querySelector('.messages-container').dispatchEvent(new WheelEvent('wheel', { deltaY: -200, bubbles: true })); document.querySelector('.message-agent pre > code').scrollIntoView({ block: 'center' }); return 'scrolled'; })(), expecting "scrolled". 8. Eval document.querySelector('.message-agent pre > code').querySelector('.shelley-code-token') !== null until it returns true (allow 30s): scrolling it in highlights it.`, func(s *shelley) {
		s.v("ID", s.loremipsum("medium").ID)
	})
}

func TestE2EMarkdownLocalImages(t *testing.T) {
	// The "inline image" predictable pattern writes a tiny PNG into the
	// conversation's directory with bash, then shows it with relative-path
	// markdown. The UI rewrites its src to /api/message/{id}/file, and the
	// server serves the bytes.
	e2e(t, useful(0), `Local images in agent messages load through the per-message file endpoint. The conversation /c/{{SLUG}} was started with "inline image": the agent wrote a small PNG and showed it with relative-path markdown. "The image" is the last ".message-agent img" element. 1. Navigate to /c/{{SLUG}}. 2. The image should be visible (allow 30s). 3. Its src attribute must match /^\/api\/message\/[^/]+\/file\?path=/: eval (() => { const imgs = document.querySelectorAll('.message-agent img'); return /^\/api\/message\/[^/]+\/file\?path=/.test(imgs[imgs.length - 1].getAttribute('src')); })(), which must return true. 4. The browser loads its bytes: eval (() => { const imgs = document.querySelectorAll('.message-agent img'); return imgs[imgs.length - 1].naturalWidth > 0; })() until it returns true (allow 15s).`, func(s *shelley) {
		s.v("SLUG", s.conversation("inline image", "").Slug)
	})
}

func TestE2EMarkdownImageUnavailable(t *testing.T) {
	// A file deleted since the message showed it explains itself rather
	// than leaving a broken-image glyph.
	e2e(t, useful(0), `A local image that is gone says why. The conversation /c/{{SLUG}} was started with "echo: Before ![Settings page](/tmp/shelley-e2e-deleted-shot.png) after", and that file does not exist. 1. Navigate to /c/{{SLUG}}. 2. The ".message-agent .markdown-image-missing" element should read exactly "Image unavailable (Settings page: /tmp/shelley-e2e-deleted-shot.png: file not found)". 3. There must be exactly 0 ".message-agent img" elements.`, func(s *shelley) {
		s.v("SLUG", s.conversation("echo: Before ![Settings page](/tmp/shelley-e2e-deleted-shot.png) after", "").Slug)
	})
}

func TestE2EMarkdownLinksNewTab(t *testing.T) {
	e2e(t, useful(0), `Markdown links open in a new tab with noopener. The conversation /c/{{SLUG}} was started with "markdown: [example](https://example.com)", which the predictable model answers with that markdown. `+markdownReply+`"The link" is the reply's first "a" element. 1. Navigate to /c/{{SLUG}}. 2. The link's href attribute should be exactly "https://example.com" (allow 30s). 3. Its target attribute should be exactly "_blank". 4. Its rel attribute should be exactly "noopener noreferrer".`, func(s *shelley) {
		s.v("SLUG", s.conversation("markdown: [example](https://example.com)", "").Slug)
	})
}

func TestE2EMarkdownUserMessagesPlain(t *testing.T) {
	e2e(t, useful(0), `User messages never render markdown: they show the raw text. The conversation /c/{{SLUG}} was started with the user message "**bold** and *italic*". "The user message" is the last ".message-user" element (the conversation has just one). 1. Navigate to /c/{{SLUG}}. 2. The user message should contain "**bold**" (allow 30s): the raw markdown characters show. 3. The user message must hold exactly 0 "strong" elements and exactly 0 "em" elements (assert_count with "within" it).`, func(s *shelley) {
		s.v("SLUG", s.conversation("**bold** and *italic*", "").Slug)
	})
}

func TestE2EMarkdownWebSearchCitations(t *testing.T) {
	// The "web search" predictable pattern returns a server-side web-search
	// message: a server_tool_use block, a web_search_tool_result, and many
	// small text blocks where cited quotes carry a Citations array. The UI
	// must merge adjacent text blocks (no stray line breaks) and show inline
	// citation markers and a numbered Sources list.
	e2e(t, useful(0), `Web-search citation blocks coalesce into one paragraph with markers. The conversation /c/{{SLUG}} was started with "web search": the reply is many small text blocks, some citing sources. `+markdownReply+`"The content" is the reply's '[data-testid="message-content"]' element; in evals it is CONTENT = [...document.querySelectorAll('.message-agent')].at(-1).querySelector('[data-testid="message-content"]'). 1. Navigate to /c/{{SLUG}}. 2. The content should contain "mid-session model switching" (allow 30s). 3. The sentence once split across blocks reads on (an inline citation marker may sit between its halves): eval /never lose work.*so model switching pairs well/s.test(CONTENT.textContent), which must return true. 4. Eval CONTENT.querySelectorAll('sup.citation-refs a.citation-ref').length > 0: true (inline markers are superscript source links). 5. Eval CONTENT.querySelectorAll('ol.citation-sources li.citation-source').length > 0: true (a numbered Sources list follows). 6. Eval /^https?:\/\//.test(CONTENT.querySelector('ol.citation-sources li.citation-source a').getAttribute('href')): true.`, func(s *shelley) {
		s.v("SLUG", s.conversation("web search", "").Slug)
	})
}

func TestE2EMarkdownGeminiSearchSuggestions(t *testing.T) {
	e2e(t, useful(0), `Gemini search suggestions show, and each claim is cited inline. The conversation /c/{{SLUG}} was started with "gemini grounded search": its web search tool card (the element with data-testid "tool-call-completed") shows Google's search suggestions in a sandboxed iframe. "The answer" is the first ".message-agent .markdown-content p" element whose text contains "Paris hosted" (has_text). 1. Navigate to /c/{{SLUG}}. 2. In the frame '[data-testid="tool-call-completed"] iframe' (the step's "frame" field), the innermost element containing "Olympic hosts" (selector "*" with has_text) should be visible (allow 30s). 3. The answer should read exactly "🏟 Paris hosted.1 Los Angeles follows.2" (assert_text).`, func(s *shelley) {
		s.v("SLUG", s.conversation("gemini grounded search", "").Slug)
	})
}

func TestE2EMarkdownWebSearchWithoutResults(t *testing.T) {
	e2e(t, useful(0), `A web search without structured results expands to show its queries. The conversation /c/{{SLUG}} was started with "web search without results". "The search" is the element with data-testid "tool-call-completed" whose text contains "Web Search" (has_text); the steps below look inside it with "within". 1. Navigate to /c/{{SLUG}}. 2. Mount the card: it mounts once its placeholder (the element with aria-label exactly "web_search tool result") comes near the viewport, and the page may still be settling and move it away again, so in a retry block (allow 30s) of two steps: eval (() => { const el = document.querySelector('[aria-label="web_search tool result"]'); if (el) el.scrollIntoView({ block: 'nearest' }); return 'scrolled'; })() expecting "scrolled", then the search should be visible. 3. The search should be visible. 4. It must hold exactly 1 ".tool-toggle" element. 5. Its ".web-search-query-preview" should read exactly "pi coding agent switch models". 6. Its ".web-search-queries" must not be visible (hidden or absent). 7. Click its ".tool-header". 8. Its ".web-search-queries" should be visible and contain "pi coding agent switch models".`, func(s *shelley) {
		s.v("SLUG", s.conversation("web search without results", "").Slug)
	})
}
