package test

import (
	"path/filepath"
	"strings"
	"testing"
)

// Ported from ui/e2e/vim-gq-format.spec.ts.

// monaco-vim never implemented the gq/gw format operators; Shelley registers
// its own in services/monaco.ts. These tests drive real keystrokes through
// the vim adapter in the file editor and check the reflowed text lands on
// screen and in the auto-saved file.

// vimGqOpen says how a description opens {{FILE}} in the editor with Vim on.
const vimGqOpen = `To "open the file with Vim": press Control+p (the file finder); ".grp-filter" should be visible (allow 10s). Fill it with {{QUERY}}; there should be exactly one ".grp-row" (allow 10s). Press Enter with the filter focused. "The modal" is the element with role "dialog" whose accessible name contains "Edit {{FILE}}"; it should be visible (allow 15s). "Monaco" is the first ".monaco-editor" inside the modal; the ".view-line" inside it containing the text {{VISIBLE}} should be visible (allow 15s). The modal's ".vim-toggle" should be visible; click it. The modal's ".monaco-vim-status" ("the status") should contain the text "NORMAL" (allow 10s). Then focus Monaco's "textarea.inputarea" with an eval calling its focus(). `

// vimGqSetup writes file into a fresh directory, starts a conversation
// there, and has call "file" report the file's lines as a JSON array and
// "first four lines" the first four of them.
func vimGqSetup(s *shelley, name, content string) {
	s.viewport(1280, 800)
	dir := s.mkdir(filepath.Join(s.dir, "vim-gq"))
	file := s.write(filepath.Join(dir, name), content)
	s.v("FILE", file)
	s.v("SLUG", s.conversationWith(map[string]any{"message": "Hello", "model": "predictable", "cwd": dir}, "").Slug)
	lines := func() []string { return strings.Split(s.read(file), "\n") }
	s.call("file", func() string { return mustJSON(lines()) })
	s.call("first four lines", func() string {
		l := lines()
		return mustJSON(l[:min(4, len(l))])
	})
}

func TestE2EVimGqReflowsParagraph(t *testing.T) {
	const long = "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa"
	e2e(t, `In the file editor with Vim on, "gqip" reflows the paragraph under the cursor to the text width. The screen is 1280x800 (the Vim toggle is desktop-only). The conversation's directory holds {{FILE}}, a first paragraph of a long line "`+long+`" and a short line "quebec romeo", then a blank line and a second paragraph "second paragraph stays". Call "file" returns the file's lines (its text split at "\n") as a JSON array; call "first four lines" returns the first four of them. `+strings.NewReplacer("{{QUERY}}", `"notes"`, "{{VISIBLE}}", `"quebec romeo"`).Replace(vimGqOpen)+`Keys are typed key by key into the focused editor. Perform the steps in order. 1. Navigate to /c/{{SLUG}} and open the file with Vim. 2. Type "gg", then type "gqip". 3. Monaco's ".view-lines" should contain the text "papa quebec romeo" (allow 10s) and the text "second paragraph stays"; the status should contain "NORMAL". 4. The editor auto-saves: call "first four lines" until (up to 10s) it returns exactly ["alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike","november oscar papa quebec romeo","","second paragraph stays"]. 5. Undo restores the original text in one step: type "u". Monaco's ".view-lines" should contain the text "`+long+`" (allow 10s), and call "file" should return (up to 10s) exactly ["`+long+`","quebec romeo","","second paragraph stays",""]. 6. Click the modal's ".diff-viewer-close": the modal should be hidden (allow 10s).`, func(s *shelley) {
		vimGqSetup(s, "notes.md", long+"\nquebec romeo\n\nsecond paragraph stays\n")
	})
}

func TestE2EVimGqGoCommentLeader(t *testing.T) {
	e2e(t, `In a Go file, Vim's "gqip" re-applies the "// " comment leader to the lines it wraps. The screen is 1280x800 (the Vim toggle is desktop-only). The conversation's directory holds {{FILE}}: "package doc", a blank line, a ragged three-line doc comment ("// Frobnicate applies the frobnication transform to every element of xs", "// in", "// place and reports how many elements changed as a result."), and right after it, with no blank line, "func Frobnicate(xs []int) int { return 0 }". Call "file" returns the file's lines (its text split at "\n") as a JSON array. `+strings.NewReplacer("{{QUERY}}", `"doc.go"`, "{{VISIBLE}}", `"// in"`).Replace(vimGqOpen)+`Keys are typed key by key into the focused editor. Perform the steps in order. 1. Navigate to /c/{{SLUG}} and open the file with Vim. 2. Line 3 is the first comment line: type "3G", then type "gqip" ("ip" covers the comment and the func line, but the change of leader keeps the func line apart). 3. Monaco's ".view-lines" should contain the text "// place and reports" (allow 10s); the status should contain "NORMAL". 4. Call "file" until (up to 10s) it returns exactly ["package doc","","// Frobnicate applies the frobnication transform to every element of xs in","// place and reports how many elements changed as a result.","func Frobnicate(xs []int) int { return 0 }",""]. 5. Click the modal's ".diff-viewer-close": the modal should be hidden (allow 10s).`, func(s *shelley) {
		vimGqSetup(s, "doc.go", "package doc\n\n// Frobnicate applies the frobnication transform to every element of xs\n// in\n// place and reports how many elements changed as a result.\nfunc Frobnicate(xs []int) int { return 0 }\n")
	})
}
