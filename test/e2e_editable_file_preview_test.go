package test

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// Ported from ui/e2e/editable-file-preview.spec.ts.

// previewOpen says how a step opens a file in the editor modal (the spec's
// openFile).
const previewOpen = `To "open F": press "p" (lowercase) with the Control modifier (on whatever has focus); wait for the finder input ".grp-filter" to be visible; fill it with F; wait for the FIRST ".grp-row" to contain F (assert_text_contains on ".grp-row", which checks the first match: the finder lists every file until the query applies, and Enter opens the top row, so waiting for any row with the text is not enough); press Enter in the finder input; the dialog (role "dialog") whose accessible name matches the regular expression /Edit .*F/ is then "the modal" for F, and its ".monaco-editor" should become visible (up to 15s). "The preview" is the modal's region (role "region") named "Markdown preview". "The editor input" is the modal's ".monaco-editor textarea.inputarea"; to focus it, eval its focus(). Key presses go to whatever has focus; do not click anything to focus first unless a step says so. Perform the steps in order. `

// previewWriteFile routes POST /api/write-file through intercept, which sees
// the path the request writes.
func (s *shelley) previewWriteFile(intercept func(w http.ResponseWriter, r *http.Request, path string) bool) {
	s.route("POST /api/write-file", func(w http.ResponseWriter, r *http.Request) {
		path, _ := object(readBody(r))["path"].(string)
		if intercept(w, r, path) {
			return
		}
		s.pass.ServeHTTP(w, r)
	})
}

// previewCodeLayoutJS narrows the preview's inline code paragraphs and checks
// how the code wraps, returning "ok" or what is off.
const previewCodeLayoutJS = `(() => { const codes = [...PREVIEW.querySelectorAll('p > code')]; const layout = codes.map((code, index) => { const paragraph = code.parentElement; paragraph.style.width = index === 1 ? '120px' : '250px'; const range = document.createRange(); range.selectNodeContents(code); return { lines: range.getClientRects().length, display: getComputedStyle(code).display, belowPrefix: code.getBoundingClientRect().top - paragraph.getBoundingClientRect().top > parseFloat(getComputedStyle(paragraph).lineHeight) / 2, overflow: paragraph.scrollWidth > paragraph.clientWidth }; }); const bad = []; if (!(layout[0].lines === 1 && layout[0].belowPrefix === true)) bad.push('first ' + JSON.stringify(layout[0])); if (layout[1].display !== 'inline') bad.push('second display ' + layout[1].display); if (!(layout[1].lines > 1)) bad.push('second lines ' + layout[1].lines); if (!(layout[2].lines > 1)) bad.push('third lines ' + layout[2].lines); if (!layout.every((c) => !c.overflow)) bad.push('overflow ' + JSON.stringify(layout)); return bad.length ? bad.join('; ') : 'ok'; })()`

func TestE2EEditableFilePreviewRendersSafely(t *testing.T) {
	notes := strings.Join([]string{
		"# Release notes",
		"",
		"| Feature | Status |",
		"| --- | --- |",
		"| Preview | Ready |",
		"",
		"Wrapped paragraph",
		"continues here.",
		"",
		"Hard break with spaces.  ",
		"Next line.",
		"",
		"Hard break with backslash.\\",
		"Next line.",
		"",
		"Hard break with HTML.<br>Next line.",
		"",
		"Single token: `here_is-a_piece-of-code`.",
		"",
		"Words: `here is a piece of code`.",
		"",
		"Oversized: `" + strings.Repeat("long_token-", 40) + "`.",
		"",
		"```js",
		"window.__previewExecuted = true;",
		"// Code stays on its own line.",
		"```",
		"",
		`<img src="/missing-image" onerror="window.__previewExecuted = true">`,
		`<svg onload="window.__previewExecuted = true"></svg>`,
		`<iframe srcdoc="<script>window.__previewExecuted = true</script>"></iframe>`,
		"<script>window.__previewExecuted = true</script>",
		`<div class="diff-viewer-overlay">spoofed overlay</div>`,
	}, "\n")
	e2e(t, useful(0), `The Markdown file editor preview renders tables and pending edits without running file contents. The conversation /c/{{SLUG}} runs in a directory holding notes.md, a Markdown file with: a heading "Release notes"; a table with a "Ready" cell; a paragraph "Wrapped paragraph" whose source continues on a second line "continues here."; three paragraphs "Hard break with spaces.", "Hard break with backslash." and "Hard break with HTML." each followed by a hard line break and "Next line."; three paragraphs with inline code, "Single token: `+"`here_is-a_piece-of-code`"+`.", "Words: `+"`here is a piece of code`"+`." and "Oversized: " with one very long unbroken code token; a js code block of two lines, "window.__previewExecuted = true;" and "// Code stays on its own line."; and raw HTML that would set window.__previewExecuted = true if it ran (an img with onerror, an svg with onload, an iframe with a script in srcdoc, a script), plus a div with the class "diff-viewer-overlay" holding "spoofed overlay". window.__previewExecuted starts out false on every page. "The wrapped paragraph" is the preview's "p" element containing "Wrapped paragraph". `+previewOpen+`1. Navigate to /c/{{SLUG}}. 2. Open notes.md. 3. The modal's button named "Preview mode" should be visible. 4. Press "p" (lowercase) with the modifiers Control+Alt. 5. The preview should be visible and focused (document.activeElement is it). 6. The preview should show a visible heading "Release notes" and a visible table cell "Ready" in its table. 7. The wrapped paragraph's text, whitespace-collapsed and trimmed, must be exactly "Wrapped paragraph continues here.", and it must hold exactly 0 "br" elements. 8. For each of "spaces", "backslash" and "HTML": the preview's "p" elements containing "Hard break with <kind>." must hold exactly 1 "br" in total. 9. The preview must have exactly one "pre code" element, whose textContent is exactly "window.__previewExecuted = true;\n// Code stays on its own line.\n" (two lines, each ending in a newline). 10. The preview must hold exactly 0 elements matching "script, iframe, img, [onerror], [onload]", exactly 0 ".diff-viewer-overlay" elements, and still contain the text "spoofed overlay". 11. Its "pre > code" must have the class "language-js". 12. It must hold exactly 0 ".markdown-content > svg" elements (the UI's own code-copy button has an SVG icon; the file's SVG markup must still be removed). 13. Eval window.__previewExecuted: it must be exactly false. 14. Eval this, with PREVIEW replaced by an expression for the preview; it must return "ok": `+previewCodeLayoutJS+`. 15. Click the modal's button named "Split view". 16. The wrapped paragraph should come to hold exactly 1 "br" (split keeps the source's line breaks), and the preview's first "p > code" should have the computed display "inline". 17. Click the modal's button named "Preview mode". 18. The wrapped paragraph should come to hold exactly 0 "br" (full preview alone reflows). 19. Press "K" with the modifiers Control+Shift to return to Monaco (this goes back to the mode before preview, split view here, so the editor shows again). 20. Focus the editor input. 21. Press "End" with the Control modifier. 22. Type, key by key, "\n\n## Unsaved view\n\nA fresh line." (each \n presses Enter). 23. Click the modal's button named "Preview mode". 24. The preview should show a visible heading (an h1-h6 element) with the text "Unsaved view" and contain "A fresh line.": no save or reload is needed to see the current editor buffer. 25. Click the modal's button named "Edit mode". 26. The modal's ".view-lines" should come to contain "Unsaved view" (assert_text_contains, which polls: Monaco draws its lines a moment after the editor shows again).`, func(s *shelley) {
		s.viewport(1280, 800)
		dir := s.tempDir("md-preview")
		s.write(filepath.Join(dir, "notes.md"), notes)
		s.initScript(`window.__previewExecuted = false;`)
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}

func TestE2EEditableFilePreviewOnlyForMarkdown(t *testing.T) {
	e2e(t, useful(0), `The file editor keeps preview exclusive to .md paths. The conversation /c/{{SLUG}} runs in a directory holding notes.txt, which reads "# This is plain text". `+previewOpen+`1. Navigate to /c/{{SLUG}}. 2. Open notes.txt. 3. The modal must have exactly 0 buttons named "Preview mode" and exactly 0 named "Split view". 4. Press "K" with the modifiers Control+Shift. 5. The modal must hold exactly 0 regions named "Markdown preview". 6. Its ".monaco-editor" should be visible.`, func(s *shelley) {
		s.viewport(1280, 800)
		dir := s.tempDir("plain-preview")
		s.write(filepath.Join(dir, "notes.txt"), "# This is plain text\n")
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}

func TestE2EEditableFilePreviewKeepsDeleteLine(t *testing.T) {
	e2e(t, useful(0), `The Markdown file editor keeps Monaco's delete-line shortcut while the editor has focus. The conversation /c/{{SLUG}} runs in a directory holding keys.md, whose lines are "# Keep", "Remove this line" and "Keep this too". `+previewOpen+`1. Navigate to /c/{{SLUG}}. 2. Open keys.md. 3. Focus the editor input. 4. Press "Home" with the Control modifier. 5. Press "ArrowDown". 6. Press "K" with the modifiers Control+Shift (Monaco's delete line). 7. The modal's ".view-lines" should come to not contain "Remove this line" (assert_text_not_contains, which polls). 8. The modal must hold exactly 0 regions named "Markdown preview". 9. Press "p" (lowercase) with the modifiers Control+Alt. 10. The preview should be visible. 11. Press "K" with the modifiers Control+Shift. 12. The modal's ".monaco-editor" should be visible. 13. Its ".view-lines" must still not contain "Remove this line" (assert_text_not_contains: Monaco draws spaces as U+00A0, so do not compare raw textContent).`, func(s *shelley) {
		s.viewport(1280, 800)
		dir := s.tempDir("md-shortcut")
		s.write(filepath.Join(dir, "keys.md"), "# Keep\nRemove this line\nKeep this too\n")
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}

func TestE2EEditableFilePreviewLeavesShortcutsToPalette(t *testing.T) {
	e2e(t, useful(0), `The Markdown file editor leaves shortcuts to the stacked command palette. The conversation /c/{{SLUG}} runs in a directory holding notes.md, which reads "# A note". `+previewOpen+`1. Navigate to /c/{{SLUG}}. 2. Open notes.md. 3. Click the modal's button named "Preview mode". 4. Press "k" with the Control modifier. 5. The command palette's input ".command-palette-input" should become focused (document.activeElement is it; the palette focuses its input a task after it opens, and until then the shortcut is still the editor's). 6. Press "p" (lowercase) with the modifiers Control+Alt. 7. The preview should still be visible. 8. ".command-palette-input" should still be focused.`, func(s *shelley) {
		s.viewport(1280, 800)
		dir := s.tempDir("preview-palette")
		s.write(filepath.Join(dir, "notes.md"), "# A note\n")
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}

func TestE2EEditableFilePreviewAgentsMD(t *testing.T) {
	e2e(t, useful(0), `The editor also previews AGENTS.md, without a comment toggle. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. Click the overflow menu button ".chat-overflow-menu-wrapper .btn-icon". 3. Click the ".overflow-menu-item" whose text contains "Edit User AGENTS.md". 4. In the dialog (role "dialog") whose accessible name contains "Edit AGENTS.md", ".monaco-editor" should be visible. 5. That dialog must have exactly 0 buttons named "Comment mode". 6. Click its button named "Preview mode". 7. Its region (role "region") named "Markdown preview" should be visible.`, func(s *shelley) {
		s.viewport(1280, 800)
		s.v("SLUG", s.conversation("Hello", "").Slug)
	})
}

func TestE2EEditableFilePreviewSplitView(t *testing.T) {
	e2e(t, useful(0), `The Markdown file editor offers a live side-by-side editor and preview. The conversation /c/{{SLUG}} runs in a directory holding draft.md: a heading "Draft", a table and a js code block. "The editor pane" is the modal's ".diff-viewer-editor". `+previewOpen+`1. Navigate to /c/{{SLUG}}. 2. Open draft.md. 3. Click the modal's button named "Split view". 4. The preview's table should be visible. 5. The editor pane's right edge must meet the preview's left edge: with getBoundingClientRect(), Math.abs(pane.x + pane.width - preview.x) < 3. 6. The editor input should already be focused (assert_focused): the app focuses it, so the test must not focus it itself. 7. Press "End" with the Control modifier. 8. Type, key by key, "\n\n## New while typing" (each \n presses Enter). 9. The preview should show a visible heading (an h1-h6 element) with the text "New while typing". 10. Click the modal's button named "Preview mode". 11. The editor pane should be hidden, and the preview should still show a visible heading "New while typing". 12. The preview's "pre > code" should come to have a data-shelley-code-highlight attribute of exactly "js" or "javascript" (poll: it reads "pending" while highlighting loads). 13. Press "K" with the modifiers Control+Shift (this goes back to the mode before preview: split view). 14. The editor pane and the preview should both be visible. 15. Press "p" (lowercase) with the modifiers Control+Alt. 16. The editor pane should be hidden. 17. Set the viewport to width 390, height 844 (only width and height). 18. The modal should come to have exactly 0 buttons named "Split view". 19. Press "p" (lowercase) with the modifiers Control+Alt. 20. The editor pane should be visible, and there must be exactly 0 previews in the modal.`, func(s *shelley) {
		s.viewport(1280, 800)
		dir := s.tempDir("split-preview")
		s.write(filepath.Join(dir, "draft.md"), "# Draft\n\n| Item | Done |\n| --- | --- |\n| One | yes |\n\n```js\nconst x = 42;\n```\n")
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}

func TestE2EEditableFilePreviewVimScrolling(t *testing.T) {
	e2e(t, useful(0), `The Markdown file editor's preview supports Vim scrolling and returning to edit. The conversation /c/{{SLUG}} runs in a directory holding long.md: a heading "Long read" and a hundred paragraphs. `+previewOpen+`Read the preview's scrollTop with eval. 1. Navigate to /c/{{SLUG}}. 2. Open long.md. 3. Click the modal's ".vim-toggle". 4. The modal's ".monaco-vim-status" should contain "NORMAL". 5. Click the modal's button named "Preview mode". 6. The preview should be focused (document.activeElement is it). 7. Dispatch on the preview a KeyboardEvent("keydown", {key: "o", metaKey: true, bubbles: true}). 8. The preview should still be visible. 9. Press "G". 10. The preview's scrollTop must be greater than 0. 11. Press "g", then "g" again. 12. Its scrollTop must be exactly 0. 13. Press "j". 14. Its scrollTop must be greater than 0. 15. Press "k". 16. Its scrollTop must be exactly 0. 17. Press "d" with the Control modifier. 18. Its scrollTop must be greater than 0. 19. Press "i". 20. The modal's ".monaco-editor" should be visible and the editor input focused (document.activeElement is it).`, func(s *shelley) {
		s.viewport(1280, 800)
		dir := s.tempDir("vim-preview")
		s.write(filepath.Join(dir, "long.md"), "# Long read\n\n"+strings.Repeat("Paragraph of text.\n\n", 100))
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}

func TestE2EEditableFilePreviewKeepsUnfinishedComment(t *testing.T) {
	e2e(t, useful(0), `The Markdown file editor preserves an unfinished comment before entering preview. The conversation /c/{{SLUG}} runs in a directory holding draft.md, which reads "# Keep this note". "The comment dialog" is ".diff-viewer-comment-dialog". `+previewOpen+`1. Navigate to /c/{{SLUG}}. 2. Open draft.md. 3. Click the modal's button named "Comment mode". 4. Click the modal's ".view-line" containing "Keep this note". 5. The comment dialog should be visible. 6. Fill its ".diff-viewer-comment-input" with "unfinished comment". 7. The modal's button named "Preview mode" should be disabled. 8. Press "p" (lowercase) with the modifiers Control+Alt. 9. The comment input should still have the value "unfinished comment". 10. The modal must hold exactly 0 regions named "Markdown preview".`, func(s *shelley) {
		s.viewport(1280, 800)
		dir := s.tempDir("md-comment-preview")
		s.write(filepath.Join(dir, "draft.md"), "# Keep this note\n")
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}

func TestE2EEditableFilePreviewSeparateBuffers(t *testing.T) {
	e2e(t, useful(0), `Reopening a different file keeps the editor buffers and pending saves separate. The conversation /c/{{SLUG}} runs in a directory holding first.md, which reads "# First file", and second.md, which reads "# Second file". Call "first.md" says whether first.md on disk contains "Pending edit" ("true" or "false"); call "second.md" returns second.md's contents on disk, JSON-encoded. `+previewOpen+`1. Navigate to /c/{{SLUG}}. 2. Open first.md. 3. Focus the editor input. 4. Press "End" with the Control modifier. 5. Type, key by key, "\n## Pending edit" (the \n presses Enter). 6. Press "p" (lowercase) with the modifiers Control+Alt. 7. The modal should show a visible heading "Pending edit". 8. The finder stacks above the editor: press "p" (lowercase) with the Control modifier, wait for ".grp-filter" to be visible, fill it with "second.md", and wait for the first ".grp-row" to contain "second.md". 9. Press "p" (lowercase) with the modifiers Control+Alt (focus is in the finder). 10. The first.md modal's "Markdown preview" region should still be visible. 11. Press Enter in the finder input. Selecting a second file must unmount the first editor, flushing its pending save to the FIRST path. 12. In the dialog whose accessible name matches /Edit .*second\.md/ ("the second modal"), ".monaco-editor" should be visible. 13. The second modal must hold exactly 0 regions named "Markdown preview". 14. Click the second modal's button named "Preview mode". 15. The second modal should show a visible heading "Second file". 16. The second modal must not contain the text "Pending edit". 17. Call "first.md" until it returns "true". 18. Call "second.md": it must return exactly "\"# Second file\\n\"" (the file unchanged).`, func(s *shelley) {
		s.viewport(1280, 800)
		dir := s.tempDir("preview-switch")
		first := s.write(filepath.Join(dir, "first.md"), "# First file\n")
		second := s.write(filepath.Join(dir, "second.md"), "# Second file\n")
		s.call("first.md", func() string { return strconv.FormatBool(strings.Contains(s.read(first), "Pending edit")) })
		s.call("second.md", func() string { return mustJSON(s.read(second)) })
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}

func TestE2EEditableFilePreviewSerializesSaves(t *testing.T) {
	e2e(t, useful(0), `The file editor serializes saves when switching files during an in-flight write. The conversation /c/{{SLUG}} runs in a directory holding first.md ("# First") and second.md ("# Second"). The server's answer to the first save of first.md (POST /api/write-file) is held back until call "release"; call "first save started" says whether that save has arrived ("true" or "false"); call "first.md" says whether first.md on disk contains "Latest edit" ("true" or "false"). `+previewOpen+`1. Navigate to /c/{{SLUG}}. 2. Open first.md. 3. Focus the editor input. 4. Press "End" with the Control modifier. 5. Type, key by key, "\n## First edit" (the \n presses Enter). 6. Call "first save started" until it returns "true": the first auto-save is now held at the network boundary. 7. Press "End" with the Control modifier. 8. Type, key by key, "\n## Latest edit". 9. Press "p" (lowercase) with the Control modifier, fill ".grp-filter" with "second.md", wait for the first ".grp-row" to contain "second.md", and press Enter in the finder input. 10. In the dialog whose accessible name matches /Edit .*second\.md/, ".monaco-editor" should be visible. 11. Press "p" (lowercase) with the Control modifier, wait for ".grp-filter" to be visible, fill it with "first.md", wait for the first ".grp-row" to contain "first.md", and press Enter in the finder input. 12. The dialog whose accessible name matches /Edit .*first\.md/ ("the reopened modal") should be visible. 13. The reopened modal must hold exactly 0 ".monaco-editor" elements (it waits for the pending save). 14. Call "release". 15. The reopened modal's ".monaco-editor" should become visible. 16. Its ".view-lines" should contain "Latest edit". 17. Call "first.md" until it returns "true".`, func(s *shelley) {
		s.viewport(1280, 800)
		dir := s.tempDir("preview-save-order")
		first := s.write(filepath.Join(dir, "first.md"), "# First\n")
		s.write(filepath.Join(dir, "second.md"), "# Second\n")
		started := s.signal("first save started")
		release := s.gate("release")
		var intercepted atomic.Bool
		s.previewWriteFile(func(w http.ResponseWriter, r *http.Request, path string) bool {
			// Only the first save waits; any later one goes straight through.
			if path == first && intercepted.CompareAndSwap(false, true) {
				started.Store(true)
				release.wait(r)
			}
			return false
		})
		s.call("first.md", func() string { return strconv.FormatBool(strings.Contains(s.read(first), "Latest edit")) })
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}

func TestE2EEditableFilePreviewRecoversFailedSave(t *testing.T) {
	e2e(t, useful(0), `The file editor recovers unsaved edits when switching files after a failed save. The conversation /c/{{SLUG}} runs in a directory holding first.md ("# First") and second.md ("# Second"). Every save of first.md (POST /api/write-file) fails with status 503 until call "allow writes"; call "first.md" returns first.md's contents on disk, JSON-encoded; call "first.md has Keep me" says whether first.md on disk contains "Keep me" ("true" or "false"). `+previewOpen+`1. Navigate to /c/{{SLUG}}. 2. Open first.md. 3. Focus the editor input. 4. Press "End" with the Control modifier. 5. Type, key by key, "\n## Keep me" (the \n presses Enter). 6. Open second.md. 7. Open first.md again ("the reopened modal"). 8. The reopened modal's ".view-lines" should contain "Keep me". 9. Its ".agents-md-save-error" should be visible. 10. Call "first.md": it must return exactly "\"# First\\n\"" (nothing saved). 11. Call "allow writes". 12. Focus the reopened modal's editor input. 13. Press "s" (lowercase) with the Control modifier. 14. Call "first.md has Keep me" until it returns "true".`, func(s *shelley) {
		s.viewport(1280, 800)
		dir := s.tempDir("preview-failed-save")
		first := s.write(filepath.Join(dir, "first.md"), "# First\n")
		s.write(filepath.Join(dir, "second.md"), "# Second\n")
		var failWrites atomic.Bool
		failWrites.Store(true)
		s.previewWriteFile(func(w http.ResponseWriter, r *http.Request, path string) bool {
			if failWrites.Load() && path == first {
				http.Error(w, "write failed", http.StatusServiceUnavailable)
				return true
			}
			return false
		})
		s.call("allow writes", func() string {
			failWrites.Store(false)
			return "allowed"
		})
		s.call("first.md", func() string { return mustJSON(s.read(first)) })
		s.call("first.md has Keep me", func() string { return strconv.FormatBool(strings.Contains(s.read(first), "Keep me")) })
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}

func TestE2EEditableFilePreviewMobileWideTables(t *testing.T) {
	e2e(t, useful(0), `The Markdown preview on a phone keeps wide tables inside the reading pane. The conversation /c/{{SLUG}} runs in a directory holding README.md: a heading "Mobile notes" and a table with a very long unbroken value. `+previewOpen+`1. Navigate to /c/{{SLUG}}. 2. Open README.md. 3. The modal must have exactly 0 buttons named "Split view". 4. Click the modal's button named "Preview mode". 5. The preview should show a visible heading "Mobile notes", and its "table" should be visible. 6. The preview's scrollWidth must be less than or equal to its clientWidth. 7. Click the modal's button named "Edit mode". 8. The editor input must not be focused (document.activeElement is not it). 9. The modal's ".agents-md-header-path" should be visible.`, func(s *shelley) {
		s.viewport(390, 844)
		dir := s.tempDir("mobile-preview")
		s.write(filepath.Join(dir, "README.md"), "# Mobile notes\n\n| Key | Very long description |\n| --- | --- |\n| Value | "+strings.Repeat("unbrokenvalue", 20)+" |\n")
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}
