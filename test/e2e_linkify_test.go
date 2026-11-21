package test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/linkify.spec.ts.

// URLs in agent responses are linked. With markdown enabled (the default),
// agent messages render via Marked, which auto-links URLs. With markdown
// disabled, linkifyText adds .text-link spans.

func TestE2ELinkifyAgentMarkdown(t *testing.T) {
	e2e(t, `URLs in agent responses are linked (markdown mode). Perform the steps in order. 1. Navigate to /new. 2. [data-testid="message-input"] should be visible (allow 30s). 3. Fill it with "echo: Check https://example.com and https://test.com". 4. Click [data-testid="send-button"]. 5. Wait for a ".message-agent" element to be visible (allow 30s). 6. The first ".message-agent .markdown-content a" (the agent's first link) should be visible. 7. Its href attribute must be exactly "https://example.com", its target exactly "_blank" and its rel exactly "noopener noreferrer" (pick it with the selector ".message-agent .markdown-content a" alone, which matches the first). 8. The second such link should be visible and have href exactly "https://test.com": eval Array.from(document.querySelectorAll(".message-agent .markdown-content a")).slice(0, 2).map(a => a.getAttribute("href")) must give exactly ["https://example.com","https://test.com"], and ".message-agent .markdown-content a[href=\"https://test.com\"]" must be visible.`, nil)
}

// User messages always use linkifyText (never markdown).
func TestE2ELinkifyUserMessages(t *testing.T) {
	e2e(t, `URLs are linkified in user messages too. "The user message" is the ".message-user" element whose text contains "echo: Visit https://example.com". Perform the steps in order. 1. Navigate to /new. 2. [data-testid="message-input"] should be visible (allow 30s). 3. Fill it with "echo: Visit https://example.com". 4. Click [data-testid="send-button"]. 5. Wait for a ".message-user" element to be visible (allow 30s). 6. The user message's text should contain "echo: Visit https://example.com". 7. There must be exactly 1 "a.text-link" inside the user message. 8. Its href attribute must be exactly "https://example.com".`, nil)
}

// linkifyInlineCode is the spec's test of URLs in inline code, on a device.
func linkifyInlineCode(t *testing.T, device string, d *lazycue.Device) {
	const url = "https://example.com/preview?q=one&other=two#fragment"
	e2e(t, `On `+device+`, standalone inline-code URLs are clickable. Conversation /c/{{SLUG}} has one agent reply rendered from markdown with five paragraphs: "Preview: `+"`"+url+"`"+`", "Command: `+"`curl https://example.com/command`"+`", "Two URLs: `+"`https://one.example https://two.example`"+`", "Explicit link: [`+"`https://example.com/label`"+`](https://example.com/destination)" and a fenced code block holding https://example.com/fenced. The test serves https://example.com itself: every page there is "<h1>Preview destination</h1>". "The content" is the last ".message-agent .markdown-content" element; "the link" is the a[href="`+url+`"] inside it. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The link should be visible (allow 30s). 3. The "code" inside the link must have exactly the text "`+url+`". 4. That code element must have the computed CSS display exactly "inline". 5. The link's target attribute must be exactly "_blank" and its rel exactly "noopener noreferrer". 6. There must be exactly 2 "a" elements inside the content. 7. The content's 'a[href="https://example.com/destination"] code' must have exactly the text "https://example.com/label". 8. The content's "pre code" must have exactly the text "https://example.com/fenced". 9. There must be exactly 0 "pre a" elements inside the content. 10. Click the link (a real click). 11. It opens a popup: in tab "popup", the URL must be exactly "`+url+`" (an exact URL check). 12. In tab "popup", the "h1" must have exactly the text "Preview destination". 13. In tab "popup", eval window.opener === null must give true. 14. Close tab "popup".`, func(s *shelley) {
		if d != nil {
			s.device(*d)
		}
		s.host("https://example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<h1>Preview destination</h1>")
		}))
		s.v("SLUG", s.conversation(strings.Join([]string{
			"markdown: Preview: `" + url + "`",
			"Command: `curl https://example.com/command`",
			"Two URLs: `https://one.example https://two.example`",
			"Explicit link: [`https://example.com/label`](https://example.com/destination)",
			"```text\nhttps://example.com/fenced\n```",
		}, "\n\n"), "").Slug)
	})
}

func TestE2ELinkifyInlineCodeDesktop(t *testing.T) {
	d := pixel5Desktop(1280, 900)
	linkifyInlineCode(t, "a desktop (1280x900, no touch screen)", &d)
}

func TestE2ELinkifyInlineCodeProjectDevice(t *testing.T) {
	linkifyInlineCode(t, "the default phone", nil)
}
