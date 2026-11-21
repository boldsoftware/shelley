package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Ported from ui/e2e/commit-highlights.spec.ts.

// commitHighlightsRepo makes a repo whose top commit touches three files and
// carries highlights that rank them against alphabetical order, starts a
// conversation in it at {{SLUG}}, and has the diff viewer open in layout
// ("sidebar" or "header").
func (s *shelley) commitHighlightsRepo(layout string, extra ...map[string]any) {
	repo := s.gitRepo(s.path("repo"))
	s.mkdir("repo/src")
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = "line " + strconv.Itoa(i+1)
	}
	s.write("repo/src/zeta.ts", strings.Join(lines, "\n")+"\n")
	s.write("repo/src/alpha.ts", "export const alpha = 1;\n")
	s.write("repo/README.md", "# Demo\n")
	s.gitIn(repo, "add", ".")
	s.gitIn(repo, "commit", "-m", "Base commit")

	lines[159] = "const critical = true;"
	s.write("repo/src/zeta.ts", strings.Join(lines, "\n")+"\n")
	s.write("repo/src/alpha.ts", "export const alpha = 2;\n")
	s.write("repo/README.md", "# Demo\n\nNow with highlights.\n")
	s.gitIn(repo, "add", ".")
	s.gitIn(repo, "commit", "-m", "Highlighted commit")

	highlights := []map[string]any{
		{"file": "src/zeta.ts", "line": 160, "comment": "The one line that matters."},
		{"file": "README.md", "line": 3, "comment": "Docs follow the code."},
	}
	path := s.write("highlights.json", mustJSON(map[string]any{
		"version":    1,
		"files":      []string{"src/zeta.ts", "README.md", "src/alpha.ts"},
		"highlights": append(highlights, extra...),
	}))
	cmd := exec.CommandContext(s.ctx, e2eBin, "highlights", "attach", "-C", repo, "HEAD", path)
	cmd.Env = hermeticGitEnv(append(os.Environ(), "HOME="+s.home))
	if out, err := cmd.CombinedOutput(); err != nil {
		s.fail("shelley highlights attach: %v\n%s", err, out)
	}
	s.initScript(`localStorage.setItem('diff-viewer-layout', ` + strconv.Quote(layout) + `);`)
	s.v("SLUG", s.conversationWith(map[string]any{"message": "Hello", "model": "predictable", "cwd": filepath.Clean(repo)}, "").Slug)
}

const commitHighlightsOpen = `The conversation at /c/{{SLUG}} works in a git repo whose top commit, "Highlighted commit", changes src/zeta.ts (line 160 becomes "const critical = true;"), README.md and src/alpha.ts, and carries commit highlights that rank the files zeta.ts, README.md, alpha.ts and comment on src/zeta.ts line 160 ("The one line that matters.") and README.md line 3 ("Docs follow the code."). "The overlay" is the ".diff-viewer-overlay" element; "the files" are the overlay's ".diff-tree-row[aria-selected]" elements, in document order; "the second file" is the one at index 1 (pick it with "nth": 1, scoped with "within" the overlay). Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. Open the Diffs workspace tool: wait for the ".header" element to be visible, then click the button with accessible name exactly "Diffs" inside ".chat-workspace-actions" (when that toolbar is not shown, click the button named exactly "More options" and then the button whose name starts with "Diffs" in ".chat-overflow-popover"). The overlay becomes visible (allow 30s).
`

func TestE2ECommitHighlightsSidebarRanksFiles(t *testing.T) {
	e2e(t, `The diff viewer's sidebar ranks files by commit highlights and marks highlighted lines. `+commitHighlightsOpen+`3. A clean tree defaults to the highlighted top commit: the overlay's element with role "radio" and name "Single commit" has aria-checked exactly "true".
4. The overlay's ".diff-viewer-commit-list-item .commit-picker-highlights-badge" has exactly the text "highlights".
5. An element in the overlay with the text "Commit Messages and Files by Importance" is visible.
6. There are exactly 4 files, and their ".diff-tree-label" elements have exactly the texts "Highlighted commit", "zeta.ts", "README.md", "alpha.ts", in order.
7. The first file contains no ".ellipsis-start" element (count 0); the second file contains exactly 1.
8. Each file sits under its folder, so src repeats around README.md: the overlay's ".diff-tree-row .diff-tree-label" elements have exactly the texts "Commit messages", "Highlighted commit", "src", "zeta.ts", "README.md", "src", "alpha.ts", in order.
9. Files show how many highlights they carry, not the comments: the files' ".diff-tree-highlight-count" elements have exactly the texts "★1", "★1", in order; the second file's accessible name contains "1 highlight"; the fourth file contains no ".diff-tree-highlight-count" (count 0); and no element in the overlay has the text "The one line that matters." (count 0).
10. A file opens at its first highlight, with the comment under it: click the second file. It gets aria-selected exactly "true". "The callout" is the overlay's ".diff-highlight-callout-text": it has exactly the text "The one line that matters." and is in the viewport. The LAST ".view-lines" element in the overlay contains the text "const critical = true;". The overlay has exactly 1 ".diff-highlight-gutter".
11. The next file follows the ranked order: click the overlay's button with name "Next file (>)". The third file gets aria-selected exactly "true", and the callout has exactly the text "Docs follow the code.".
12. Click "Next file (>)" again. The fourth file gets aria-selected exactly "true"; there is no callout (count 0) and no ".diff-highlight-gutter" in the overlay (count 0).
13. Highlights describe one commit; a range keeps the alphabetical tree: click the overlay's role "radio" named "Through working tree". An element in the overlay whose whole text is exactly "Commit Messages and Files" is visible; the files contain no ".diff-tree-highlight-count" (count 0); and the overlay's ".diff-tree-row:not([aria-selected]) .diff-tree-label" elements have exactly the texts "Commit messages", "src", in order.`, func(s *shelley) {
		s.viewport(1600, 900)
		s.commitHighlightsRepo("sidebar")
	})
}

func TestE2ECommitHighlightsCalloutCommentButton(t *testing.T) {
	e2e(t, `A highlight callout's Comment button answers the highlight. `+commitHighlightsOpen+`3. Click the second file. "The callout" is the overlay's ".diff-highlight-callout-text"; it is in the viewport.
4. Comment mode marks the line under the pointer, but not over a callout, where a click doesn't comment on a line. "The hover mark" is the overlay's ".diff-viewer-line-hover". Hover the overlay's ".view-line" whose text contains "const critical = true;": there is then exactly 1 hover mark.
5. The callout lets the pointer through to the space reserved for it: move the mouse (a hover step with a "point", no hit test) to x = the callout's left + 10 and y = its top + half its height (from getBoundingClientRect()). There is then no hover mark (count 0).
6. Screen readers reach the callout and its button: "the note" is the overlay's element with role "note"; its text contains "The one line that matters.". "The button" is the note's element with role "button" and name "Comment".
7. A quiet icon with a full touch target that doesn't heighten the note: in one eval with expect "true", the button's getBoundingClientRect() width and height are exactly 24 and 24, and the note's height is within 0.5 of the height of the note's ".diff-highlight-callout-text" + 8.
8. Click the button. "The dialog" is the ".diff-viewer-comment-dialog": its text contains "Add Comment (Line 160, new)", and its ".diff-viewer-selected-text" text, with whitespace runs collapsed to single spaces and trimmed, is exactly "const critical = true; ★ The one line that matters.".
9. Fill the dialog's ".diff-viewer-comment-input" with "Why this line?" and click the dialog's button named "Add Comment". The dialog is then not visible.
10. The comment quotes the line and the highlight it answers: press Escape. The overlay is then not visible, and the message input's value is exactly these three lines followed by an empty line: "> src/zeta.ts:160: const critical = true;\n> ★ The one line that matters.\nWhy this line?\n\n" (with real newlines; check the value in an eval with expect, as a JSON string, to see the newlines).`, func(s *shelley) {
		s.viewport(1600, 900)
		s.commitHighlightsRepo("sidebar")
	})
}

func TestE2ECommitHighlightsTabScrollsCallout(t *testing.T) {
	e2e(t, `Tabbing to a highlight callout's button scrolls it into view. The commit also highlights src/zeta.ts line 10 ("Far above the other."). `+commitHighlightsOpen+`3. Click the second file. It opens at its first highlight; the second is far below: "the notes" are the overlay's elements with role "note"; the first note is in the viewport and the last note is NOT in the viewport.
4. Focus the first note's button with role "button" and name "Comment" (in an eval: .focus()), then press Tab.
5. The last note's button with role "button" and name "Comment" has focus; the last note is in the viewport, and its text contains "The one line that matters.".
6. Press Enter. The ".diff-viewer-comment-dialog" text contains "Add Comment (Line 160, new)".`, func(s *shelley) {
		s.viewport(1600, 900)
		s.commitHighlightsRepo("sidebar", map[string]any{"file": "src/zeta.ts", "line": 10, "comment": "Far above the other."})
	})
}

func TestE2ECommitHighlightsCalloutsKeepSpace(t *testing.T) {
	comment := strings.TrimSpace(strings.Repeat("A long highlight that wraps onto more lines as the editor narrows. ", 3))
	e2e(t, `Highlight callouts keep their space as the editor narrows. The commit also highlights src/zeta.ts line 10 with a long comment that wraps onto more lines as the editor narrows. `+commitHighlightsOpen+`3. Click the second file. "The callout" is the FIRST ".diff-highlight-zone" in the overlay; "the next line" is the overlay's ".editor.modified .view-line" element whose textContent matches /^line\s11$/. "The gap" is the next line's getBoundingClientRect().y - (the callout's y + height), or NaN when either is missing (Monaco re-renders lines as it lays out).
4. The callout is in the viewport.
5. The line after the highlight starts where its callout ends: wait (with an eval that has expect "true") until Math.abs(gap) < 0.5.
6. In one eval (no expect), store the callout's getBoundingClientRect().height in window.__height.
7. Narrow the window to 800x900 (set_viewport with only width 800 and height 900, keeping the rest of the device).
8. Wait (with an eval that has expect "true") until the callout's height is greater than window.__height: the comment wraps onto more lines.
9. Wait (with an eval that has expect "true") until Math.abs(gap) < 0.5 again.`, func(s *shelley) {
		s.viewport(1600, 900)
		s.commitHighlightsRepo("sidebar", map[string]any{"file": "src/zeta.ts", "line": 10, "comment": comment})
	})
}

func TestE2ECommitHighlightsPhoneFilePicker(t *testing.T) {
	e2e(t, `A phone's diff file picker follows the ranked order. `+commitHighlightsOpen+`3. "The picker" is the VISIBLE select element matching ".diff-viewer-file-selector-wrapper select" in the overlay; "the options" are its option elements. There are exactly 5 options: a placeholder, the commit message, then the ranked files.
4. In one eval with expect "true" testing each option's raw textContent: the third option matches / src\/zeta\.ts .*★1$/, the fourth matches / README\.md .*★1$/ and the fifth matches / src\/alpha\.ts /.
5. The narrow diff is inline; the comment still shows under its line: select the option with value "src/zeta.ts" in the picker. "The callout" is the overlay's ".diff-highlight-callout-text": it has exactly the text "The one line that matters." and is in the viewport.
6. A tap on the callout's button opens the comment dialog, though Monaco claims touches inside the editor: tap (touch start, then touch end) the button with role "button" and name "Comment" inside the overlay's element with role "note". The ".diff-viewer-comment-dialog" text then contains "Add Comment (Line 160, new)".`, func(s *shelley) {
		s.viewport(390, 844)
		s.commitHighlightsRepo("header")
	})
}
