package test

import (
	"testing"
)

// Ported from ui/e2e/markdown-sanitization.spec.ts. Every test makes its
// conversation through the API and then opens it, which avoids the race
// between the page's first stream subscription and the first turn being
// recorded.

// sanitizationTerms names what the sanitization tests check.
const sanitizationTerms = `The conversation /c/{{SLUG}} was started with the message {{MESSAGE}}, which the predictable model answers with that markdown. "The reply" is the ".message-agent" element (the conversation has just one, so select it as plain ".message-agent", never with :last-of-type). "The reply's HTML must not contain S" means: eval [...document.querySelectorAll('.message-agent')].at(-1).innerHTML.includes(S), which must return exactly false (it is the raw HTML, so check it with exactly this eval). "Holds no X" means the reply must hold exactly 0 elements matching the CSS selector X (assert_count with "within" the reply). "The reply should contain X" means assert_text_contains on the selector ".message-agent", never wait_text (the user message holds the same words). Check exactly the numbered steps below, nothing more. 1. Navigate to /c/{{SLUG}}. `

// sanitized runs a sanitization test of the conversation started with
// message.
func sanitized(t *testing.T, title, message, steps string) {
	t.Helper()
	e2e(t, title+` `+sanitizationTerms+steps, func(s *shelley) {
		s.v("MESSAGE", mustJSON(message))
		s.v("SLUG", s.conversation(message, "").Slug)
	})
}

func TestE2EMarkdownSanitizationScript(t *testing.T) {
	sanitized(t, `Markdown in agent messages loses its script tags.`, `markdown: hello <script>alert("xss")</script> world`,
		`2. The reply should contain "hello" (allow 30s) and "world". 3. It holds no "script". 4. The reply's HTML must not contain "<script". 5. The reply's HTML must not contain "alert".`)
}

func TestE2EMarkdownSanitizationRemoteImage(t *testing.T) {
	// Remote images would let a message track whoever reads it.
	sanitized(t, `Markdown in agent messages loses remote images.`, `markdown: ![tracker](https://evil.com/pixel.gif) safe text`,
		`2. The reply should contain "safe text" (allow 30s). 3. It holds no "img". 4. The reply's HTML must not contain "<img". 5. The reply's HTML must not contain "evil.com".`)
}

func TestE2EMarkdownSanitizationIframe(t *testing.T) {
	sanitized(t, `Markdown in agent messages loses iframes.`, `markdown: <iframe src="https://evil.com"></iframe> safe`,
		`2. The reply should contain "safe" (allow 30s). 3. It holds no "iframe".`)
}

func TestE2EMarkdownSanitizationEventHandlers(t *testing.T) {
	sanitized(t, `Markdown in agent messages loses event handler attributes.`, `markdown: <div onclick="alert(1)">click me</div>`,
		`2. The reply should contain "click me" (allow 30s). 3. The reply's HTML must not contain "onclick". 4. The reply's HTML must not contain "alert".`)
}

func TestE2EMarkdownSanitizationJavascriptHref(t *testing.T) {
	sanitized(t, `Links in agent messages lose javascript: hrefs.`, `markdown: <a href="javascript:alert(document.cookie)">steal cookies</a>`,
		`2. The reply should contain "steal cookies" (allow 30s). 3. The reply's HTML must not contain "javascript:".`)
}

func TestE2EMarkdownSanitizationSVG(t *testing.T) {
	sanitized(t, `Markdown in agent messages loses SVG with embedded script.`, `markdown: <svg onload="alert(1)"><circle r="50"/></svg> safe`,
		`2. The reply should contain "safe" (allow 30s). 3. The reply's HTML must not contain "<svg". 4. The reply's HTML must not contain "onload".`)
}

func TestE2EMarkdownSanitizationInputs(t *testing.T) {
	// Text and password inputs would let a message phish.
	sanitized(t, `Markdown in agent messages loses inputs other than checkboxes.`, `markdown: <input type="text" placeholder="Enter password"> <input type="password"> safe`,
		`2. The reply should contain "safe" (allow 30s). 3. It holds no 'input[type="text"]'. 4. It holds no 'input[type="password"]'.`)
}

func TestE2EMarkdownSanitizationForm(t *testing.T) {
	// Only the rendered markdown: the action bar around it has buttons of
	// its own (copy, usage).
	sanitized(t, `Markdown in agent messages loses forms and submit buttons.`, `markdown: <form action="https://evil.com/steal"><button type="submit">Login</button></form> safe`,
		`2. The reply should contain "safe" (allow 30s). 3. "The content" is the reply's '[data-testid="message-content"]' element; check its raw HTML with exactly this eval, C being each of the three strings: [...document.querySelectorAll('.message-agent')].at(-1).querySelector('[data-testid="message-content"]').innerHTML.includes(C), which must return exactly false for C = "<form", for C = "<button" and for C = "evil.com".`)
}
