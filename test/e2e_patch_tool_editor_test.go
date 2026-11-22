package test

import (
	"path/filepath"
	"strings"
	"testing"
)

// Ported from ui/e2e/patch-tool-editor.spec.ts.

// A patch tool card's header offers "open in editor": it opens the patched
// file in the same standalone Monaco editor the fuzzy finder uses, so a patch
// can be inspected/fixed in place without hunting for the path.
func TestE2EPatchToolOpenInEditor(t *testing.T) {
	e2e(t, useful(0), `A patch tool card's header button opens the patched file in the editor. The conversation /c/{{SLUG}} patched the file {{FILE}}, which now contains "updated example". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The first completed patch card ('.patch-tool[data-testid="tool-call-completed"]') should be visible (wait up to 15s). 3. Click the button named "Open in editor" in that card. 4. A dialog (role "dialog") whose accessible name contains "Edit {{FILE}}" should become visible (up to 15s). 5. In that dialog, a ".view-line" containing "updated example" should be visible (up to 15s). 6. Press Escape. 7. The dialog should no longer be visible. 8. The patch card should still be visible (the editor is not nested in it).`, func(s *shelley) {
		dir := s.tempDir("patchedit")
		file := s.write(filepath.Join(dir, "notes.txt"), "an example line\n")
		// The predictable model's "patch: <path>" replaces "example" in that file.
		c := s.conversationIn(dir, "patch: "+file)
		if got := s.read(file); !strings.Contains(got, "updated example") {
			s.fail("the patch did not update %s: %q", file, got)
		}
		s.v("SLUG", c.Slug)
		s.v("FILE", file)
	})
}

// A successful patch records an absolutized path, so the relative branch is
// only reachable for a FAILED patch, where the card falls back to the path
// the agent passed. "example" is missing from this file, so the replace
// fails and the card keeps "./sub/notes.txt".
func TestE2EPatchToolRelativePathResolvesAgainstCwd(t *testing.T) {
	e2e(t, useful(0), `A relative patch path resolves against the conversation's working directory, {{DIR}}. The conversation /c/{{SLUG}} tried to patch "./sub/notes.txt" there and failed. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The first completed patch card ('.patch-tool[data-testid="tool-call-completed"]') should be visible (wait up to 15s). 3. Its ".patch-tool-filename" should have exactly the text "./sub/notes.txt". 4. It must contain exactly 0 ".patch-tool-header .tool-status-icon" elements and exactly 0 ".patch-tool-details" elements. 5. Click its ".patch-tool-header". 6. Its ".patch-tool-error-message" should become visible. 7. Click the button named "Open in editor" in the card. 8. A dialog (role "dialog") whose accessible name contains "Edit {{DIR}}/sub/notes.txt" (the absolute path) should become visible (up to 15s). 9. In that dialog, a ".view-line" containing "nothing to replace here" should be visible (up to 15s).`, func(s *shelley) {
		dir := s.tempDir("patchedit-rel")
		s.write(filepath.Join(dir, "sub", "notes.txt"), "nothing to replace here\n")
		s.v("SLUG", s.conversationIn(dir, "patch: ./sub/notes.txt").Slug)
		s.v("DIR", dir)
	})
}
