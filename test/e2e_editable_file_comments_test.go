package test

import (
	"path/filepath"
	"testing"
)

// Ported from ui/e2e/editable-file-comments.spec.ts.

// The standalone "Edit file" modal (fuzzy finder -> EditableFileModal) offers
// the same edit/comment mode toggle as the diff viewer. Comment mode makes
// the editor read-only and click-to-comment: clicking a line opens the Add
// Comment dialog, and the submitted comment lands in the chat message input
// as a quoted block ("> path:line: code\ncomment").
//
// The mode toggle and click-to-comment flow are exercised on a wide screen
// (the default viewport is a phone's).
func TestE2EEditableFileCommentFlowsIntoInput(t *testing.T) {
	e2e(t, useful(0), `In the Edit file modal, a comment on a line flows into the message input. The conversation /c/{{SLUG}} runs in a directory holding {{FILE}}, a notes.txt with the lines "alpha first line", "bravo second line" and "charlie third line". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. Open the fuzzy file finder by pressing "p" (lowercase) with the Control modifier, without clicking anything first. 3. The finder input ".grp-filter" should become visible (up to 10s). 4. Fill it with "notes.txt". 5. Text "notes.txt" should be visible in ".grp-body" (up to 10s). 6. With the finder input focused, press Enter. 7. A dialog (role "dialog") whose accessible name matches "Edit <something>notes.txt" should be visible (up to 15s), and the first ".monaco-editor" in it visible too. 8. In the dialog, the button named "Edit mode" should have the class "active" (edit mode is the default). 9. Click the dialog's button named "Comment mode"; it should then have the class "active". 10. Click the dialog's ".view-line" containing "bravo second line". 11. The comment dialog ".diff-viewer-comment-dialog" should be visible and contain "Add Comment (Line 2)", and its ".diff-viewer-selected-text" should contain "bravo second line". 12. Fill its ".diff-viewer-comment-input" with "needs more bravado", then blur that input (eval its blur()). 13. Click the same ".view-line" containing "bravo second line" again: the dialog retargets, and since its label stays the same, check what follows explicitly. 14. The comment dialog should still contain "Add Comment (Line 2)", its comment input should still have the value "needs more bravado", and the comment input should become focused again (document.activeElement is it). 15. Click the comment dialog's button named "Add Comment". 16. The comment dialog should no longer be visible. 17. Press Escape. 18. The Edit dialog should no longer be visible. 19. The message input (data-testid "message-input") should have a value containing "> {{FILE}}:2: bravo second line" and containing "needs more bravado".`, func(s *shelley) {
		s.viewport(1280, 800)
		dir := s.tempDir("editfile")
		file := s.write(filepath.Join(dir, "notes.txt"), "alpha first line\nbravo second line\ncharlie third line\n")
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
		s.v("FILE", file)
	})
}
