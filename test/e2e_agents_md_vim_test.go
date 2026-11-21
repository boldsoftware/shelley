package test

import "testing"

// Ported from ui/e2e/agents-md-vim.spec.ts.

// Regression test for a hang in the AGENTS.md editor with Vim mode on: the
// Monaco editor sat in a deeply reactive Vue ref, and Vim's per-keystroke
// work through that proxy pegged the main thread.

func TestE2EAgentsMdVimDoesNotHang(t *testing.T) {
	e2e(t, `Enabling Vim in the AGENTS.md editor and typing in it does not hang the page. The screen is 1280x800 (the Vim toggle is desktop-only). "Monaco" is the first ".monaco-editor". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. ".chat-overflow-menu-wrapper .btn-icon" should be visible (allow 10s); click it. 3. The ".overflow-menu-item" whose text contains "AGENTS.md" (case-insensitive) should be visible; click it. 4. Monaco should be visible (allow 15s). 5. ".vim-toggle" should be visible; click it. 6. ".monaco-vim-status" should contain the text "NORMAL" (allow 10s). 7. Focus Monaco's "textarea.inputarea" with an eval calling its focus(). 8. Press "o", type "___shelley_smoke_marker" key by key, then press Escape. 9. Monaco's ".view-lines" should contain the text "smoke_marker" (allow 10s), and ".monaco-vim-status" should contain "NORMAL". 10. Delete the line again, so the save on close writes back the original file: retry for up to 10s, as a block, typing "dd" key by key and then checking (allowing 1s) that Monaco's ".view-lines" does not contain "smoke_marker". 11. Click ".diff-viewer-close": Monaco should be hidden (allow 10s). 12. The page is still interactive: [data-testid="message-input"] should be visible.`, func(s *shelley) {
		s.viewport(1280, 800)
		s.v("SLUG", s.conversation("Hello", "").Slug)
	})
}
