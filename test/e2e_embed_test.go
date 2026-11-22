package test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ported from ui/e2e/embed.spec.ts, for the Edit with Shelley snippet
// (embed/edit-with-shelley.html) that replaced embed.js.

// embedApp serves http://app.test/, a page with the snippet, framing this
// Shelley, which lets it (and Chrome, told to). Call "frame loads" counts
// the frame's loads of Shelley's / so far.
func (s *shelley) embedApp() {
	b, err := os.ReadFile(filepath.Join(e2eModuleDir, "embed", "edit-with-shelley.html"))
	s.check(err)
	s.do("POST", "/api/embed-origins", map[string]string{"origin": "http://app.test"})
	snippet := strings.Replace(string(b), "https://shelley.example.com/", s.url+"/", 1)
	s.launch.LocalNetwork = true
	s.host("http://app.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><meta name="viewport" content="width=device-width"><title>App</title><h1>App</h1>`+snippet)
	}))
	s.call("frame loads", func() string {
		n := 0
		for _, r := range s.requestLog() {
			if r == "GET /" {
				n++
			}
		}
		return fmt.Sprint(n)
	})
}

// embedOpen describes the page and how to open it.
const embedOpen = `The page http://app.test/ (title "App", an h1 "App") has the Edit with Shelley snippet: a details element ".edit-with-shelley" whose summary "Edit with Shelley" opens Shelley in an iframe with title "Shelley" ("the frame"), loaded lazily. Call "frame loads" counts the requests for Shelley's page / so far. A new frame loads all of Shelley, which can take a while: wait for it. Perform the steps in order. 1. Navigate to http://app.test/. 2. The summary ".edit-with-shelley summary" should be visible with exactly the text "Edit with Shelley". 3. Call "frame loads": it must return exactly "0" (closed, the frame loads nothing). `

func TestE2EEmbedWide(t *testing.T) {
	e2e(t, `On a wide page, the snippet docks Shelley on the right. `+embedOpen+`4. The button is at the top right: eval (() => { const r = document.querySelector('.edit-with-shelley summary').getBoundingClientRect(); return r.top < 24 && innerWidth - r.right < 24; })() expecting true. 5. Click the summary. 6. In the frame, [data-testid="message-input"] should be visible. 7. The page makes room for the frame: eval getComputedStyle(document.documentElement).marginRight expecting "480px". 8. The frame fills the right edge, 480px wide: eval (() => { const r = document.querySelector('iframe[title=Shelley]').getBoundingClientRect(); return [r.top, r.right === innerWidth, r.width, r.height === innerHeight].join(' '); })() expecting "0 true 480 true". 9. The button sits just left of the frame: eval (() => { const b = document.querySelector('.edit-with-shelley summary').getBoundingClientRect(); const f = document.querySelector('iframe[title=Shelley]').getBoundingClientRect(); return b.right <= f.left && f.left - b.right < 24; })() expecting true. 10. Click the summary again. 11. The frame must not be visible, and eval getComputedStyle(document.documentElement).marginRight expecting "0px".`, func(s *shelley) {
		s.viewport(1200, 800)
		s.embedApp()
	})
}

func TestE2EEmbedNarrow(t *testing.T) {
	e2e(t, `On a narrow page, the snippet keeps its button above Shelley. `+embedOpen+`4. Click the summary. 5. In the frame, [data-testid="message-input"] should be visible. 6. The frame covers the page, which keeps its width: eval getComputedStyle(document.documentElement).marginRight expecting "0px". 7. The button stays at the top right, above the frame, which fills the width below it: eval (() => { const b = document.querySelector('.edit-with-shelley summary').getBoundingClientRect(); const f = document.querySelector('iframe[title=Shelley]').getBoundingClientRect(); return [b.bottom <= f.top, innerWidth - b.right < 24, f.left, f.right === innerWidth, f.bottom === innerHeight].join(' '); })() expecting "true true 0 true true".`, func(s *shelley) {
		s.viewport(390, 800)
		s.embedApp()
	})
}

func TestE2EEmbedDialog(t *testing.T) {
	e2e(t, `The command palette's Edit with Shelley gives the snippet and the sites that may frame Shelley. "The dialog" is ".embed-modal". Perform the steps in order. 1. Navigate to /new. 2. [data-testid="message-input"] should be visible. 3. Press Control+k. 4. Fill ".command-palette-input" with "edit with shelley". 5. Click the first ".command-palette-item". 6. The dialog's ".modal-title" should have exactly the text "Edit with Shelley". 7. The snippet is this Shelley's: eval document.querySelector('.embed-modal [data-testid=embed-snippet]').textContent.includes('<iframe src="{{ORIGIN}}/" title="Shelley"') expecting true. 8. It starts with the advice on whom to show it to: eval document.querySelector('.embed-modal [data-testid=embed-snippet]').textContent.startsWith('<!-- Edit with Shelley: a button that opens Shelley beside this page.\n     Only people who can sign in to your Shelley can use it,') expecting true. 9. The list named "Sites whose pages can open Shelley" (role list) must contain the text "http://app.test". 10. In the dialog, fill the textbox named "Sites whose pages can open Shelley" with "app.example.com" and click the button named exactly "Add". 11. The dialog's alert (role alert) should contain the text "as https://app.example.com". 12. Fill that textbox with "HTTP://OTHER.TEST/" and click "Add". 13. The list should contain the text "http://other.test"; the textbox must have the value exactly ""; there must be exactly 0 elements with role alert in the dialog. 14. Click the button named exactly "Remove http://other.test". 15. The list must not contain the text "http://other.test". 16. Call "csp": it must return exactly "http://app.test, not http://other.test".`, func(s *shelley) {
		s.do("POST", "/api/embed-origins", map[string]string{"origin": "http://app.test"})
		s.v("ORIGIN", s.url)
		s.call("csp", func() string {
			resp, err := http.Get(s.backend + "/")
			s.check(err)
			resp.Body.Close()
			csp := resp.Header.Get("Content-Security-Policy")
			if !strings.Contains(csp, "http://app.test") || strings.Contains(csp, "http://other.test") {
				return csp
			}
			return "http://app.test, not http://other.test"
		})
	})
}
