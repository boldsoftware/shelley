package test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/diff-viewer-find.spec.ts.

// This test exercises the Ctrl+F find widget inside the diff viewer.
// It verifies that:
//  1. Ctrl+F opens Monaco's find widget (not the browser's)
//  2. Typing in the find widget works (keys like "." don't trigger navigation)
//  3. Escape closes the find widget without closing the diff viewer
func TestE2EDiffViewerFindWidget(t *testing.T)       { diffViewerFindWidget(t, useful(0), false) }
func TestE2EDiffViewerFindWidgetMobile(t *testing.T) { diffViewerFindWidget(t, useful(0), true) }

func diffViewerFindWidget(t *testing.T, count lazycue.Useful, mobile bool) {
	opener := openToolWide
	if mobile {
		opener = openToolPhone
	}
	description := `In the diff viewer, Ctrl+F opens Monaco's find widget, typing in it works, and Escape closes the find widget, not the viewer. The conversation /c/{{SLUG}} runs in a git repository with commits. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. Open the Diffs tool: ` + fmt.Sprintf(opener, "Diffs") + `. 3. The overlay ".diff-viewer-overlay" should be visible. 4. Wait until the first "select.diff-viewer-select" in the overlay (the file select) has more than 1 option (more than its placeholder). 5. Wait until the overlay's ".diff-viewer-editor" is visible and contains at least one ".monaco-editor" element. 6. The editor container must contain exactly 0 ".find-widget.visible" elements. 7. Without clicking or focusing anything, press "f" with the Control modifier. 8. ".diff-viewer-editor .find-widget.visible" should become visible. 9. Type "test.file" key by key (a real keystroke for each character, including the ".", which would otherwise trigger "next change" navigation). 10. The find widget's text box with accessible name "Find" (its textarea or input with aria-label "Find") should have a value containing "test.file". 11. The overlay should still be visible. 12. Press Escape. 13. There should be exactly 0 ".find-widget.visible" elements in the editor container. 14. The overlay should still be visible. 15. Press Escape again. 16. There should be exactly 0 ".diff-viewer-overlay" elements.`
	if mobile {
		description = strings.Replace(description, `4. Wait until the first "select.diff-viewer-select" in the overlay (the file select) has more than 1 option (more than its placeholder).`, `4. Click the first ".diff-rethink-message-files .diff-tree-code-row" in the overlay; ".diff-rethink-file-name" should be visible.`, 1)
	}
	e2e(t, count, description, func(s *shelley) {
		if mobile {
			s.viewport(390, 844)
		} else {
			s.viewport(1280, 900)
		}
		// Playwright asked the API for the server directory's git root.
		var diffs struct {
			GitRoot string `json:"gitRoot"`
		}
		s.api("GET", "/api/git/diffs?cwd=.", nil, &diffs)
		if diffs.GitRoot == "" {
			s.fail("no gitRoot for the server's directory")
		}
		s.v("SLUG", s.conversationIn(diffs.GitRoot, "Hello").Slug)
	})
}
