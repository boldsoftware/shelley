package test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const (
	mobileDiffOpen    = `Navigate to /c/{{SLUG}}. Open Diffs: ` + openToolPhone + `. The ".diff-viewer-overlay" should be visible. `
	mobileDiffWorking = `Click ".diff-viewer-overlay .commit-picker-trigger"; in ".git-graph-container" click the ".git-graph-row" containing "Working changes". `
)

func mobileDiffRepo(s *shelley) string {
	s.viewport(390, 844)
	return s.gitRepo(s.tempDir("mobile-diff"))
}

func mobileDiffConversation(s *shelley, dir string) { s.v("SLUG", s.conversationIn(dir, "Hello").Slug) }

func mobileDiffCommit(s *shelley, dir, message string) {
	s.gitIn(dir, "add", ".")
	s.gitIn(dir, "commit", "-m", message)
}

func TestE2EMobileDiffHunkNavigation(t *testing.T) {
	e2e(t, useful(0), `On a phone, three separated edits are navigation stops and each keeps its own comment draft. Use condition waits, never sleep steps. Use the supplied selectors directly. `+`Navigate to /c/{{SLUG}}. Fill the chat message input (data-testid "message-input") with "Unrelated composer draft". Open Diffs: `+openToolPhone+`. Wait for ".diff-viewer-overlay". `+mobileDiffWorking+`
1. In ".diff-rethink-mobile-files [title='hunks.txt']", evaluate expecting true that the bounding-box y values of ".diff-tree-changes-added" and ".diff-tree-changes-deleted" differ by less than 2 pixels. Click that row. Assert ".diff-rethink-file-name" reads "hunks.txt", ".diff-rethink-file-position" contains "File 1 of 1", and ".diff-rethink-bottom-change-label" reads "Change 1 of 3". There is no button with exact aria-label "Previous hunk (,)".
2. Click the exact "Next hunk (.)" button: the label reads "Change 2 of 3". Click again: "Change 3 of 3", and no button has exact aria-label "Next hunk (.)". Click the exact "Previous hunk (,)" button: "Change 2 of 3".
3. Click ".diff-rethink-change-nav [aria-label='Comment on current hunk']". The ".diff-rethink-rail-input" is visible, focused, and has aria-label "Comment on Line 20, new". There is no ".diff-viewer-comment-dialog" or ".diff-viewer-selected-text". The rail contains Cancel, Save Comment, Previous hunk and Next hunk buttons, with the arrows to the right of Save Comment.
4. Resize to 390x420, with mobile=true, touch=true and scale=2.75. The editor pane is ".diff-viewer-files-pane", not a CodeMirror element. Evaluate expecting true: (() => { const input=document.querySelector('.diff-rethink-rail-input'), pane=document.querySelector('.diff-viewer-files-pane'), rail=document.querySelector('.diff-rethink-change-nav'); if(!input||!pane||!rail)return false; const i=input.getBoundingClientRect(), p=pane.getBoundingClientRect(), v=window.visualViewport, top=v?v.offsetTop:0, bottom=top+(v?v.height:innerHeight); return i.top>=p.bottom-1 && i.bottom<=bottom+1 && [...pane.querySelectorAll('.diff-current-hunk-gutter')].some(e=>{const r=e.getBoundingClientRect();return r.bottom>p.top&&r.top<p.bottom;}) && [...rail.querySelectorAll('button')].every(e=>{const r=e.getBoundingClientRect();return r.top>=top-1&&r.bottom<=bottom+1;}); })(). Assert the rail input remains focused and the label still reads "Change 2 of 3".
5. Fill ".diff-rethink-rail-input" with "Review this hunk" and save that node as window.hunkInput. Click ".diff-rethink-next-change". The input has aria-label "Comment on Line 35, new", is empty, is still window.hunkInput, and remains focused. The next arrow is disabled.
6. Click ".diff-rethink-prev-change". The input has aria-label "Comment on Line 20, new" and value "Review this hunk". Click "Save Comment". The rail input closes and the review bubble says one comment. The chat message input still contains only "Unrelated composer draft".
7. Click the previous hunk arrow, then "Comment on current hunk". The input has aria-label "Comment on Line 5, new". Fill it with "First hunk feedback" and click "Save Comment". Open the review using the bottom-rail button with accessible name "Open draft review, 2 comments". The open dialog ".diff-draft-review[open]" has two articles, with each feedback string beside its correct hunks.txt line. The chat message input is still unchanged.
8. Click "Send to Chat". The review and diff close. One user message contains both feedback strings and their correct hunks.txt:5 and hunks.txt:20 references. The chat message input still contains only "Unrelated composer draft".`, func(s *shelley) {
		dir := mobileDiffRepo(s)
		lines := make([]string, 50)
		for i := range lines {
			lines[i] = fmt.Sprintf("line %d", i+1)
		}
		s.write(filepath.Join(dir, "hunks.txt"), strings.Join(lines, "\n")+"\n")
		mobileDiffCommit(s, dir, "base")
		for _, n := range []int{5, 20, 35} {
			lines[n-1] += " changed"
		}
		s.write(filepath.Join(dir, "hunks.txt"), strings.Join(lines, "\n")+"\n")
		mobileDiffConversation(s, dir)
	})
}

func TestE2EDiffReviewResetComments(t *testing.T) {
	e2e(t, useful(0), `Reset comments clears the entire draft review, including an unfinished edit, without touching the composer or sending a message. Use the supplied selectors directly; use condition waits, never sleep steps. Navigate to /c/{{SLUG}}. Wait for the message input (data-testid "message-input"). The conversation already has messages: record its user-message count with eval window.resetUserCount = document.querySelectorAll('.message-user').length, with no expectation. Fill the chat message input with "Unrelated composer draft". Open Diffs: `+openToolPhone+`. Wait for ".diff-viewer-overlay". `+mobileDiffWorking+`
1. There are no ".diff-draft-review-toggle" elements before saving a comment. Click ".diff-rethink-mobile-files [title='only.txt']" and wait for ".diff-rethink-bottom-change-label" to read "Change 1 of 1". Click the button with aria-label "Comment on current hunk", fill ".diff-rethink-rail-input" with "First comment", and click "Save Comment".
2. Repeat on the same hunk with "Second comment". Click ".diff-rethink-change-nav .diff-draft-review-toggle" (the visible mobile rail's review button, not the hidden desktop viewer's duplicate). The dialog ".diff-draft-review[open]" has exactly two articles.
3. Click the first article's "Edit" button and fill the textarea with aria-label "Edit draft comment" with "Unfinished edit". Click "Reset comments" in the dialog.
4. The dialog stays open, contains "0 comments" and "Select a line in a diff to save a comment here.", and has no articles or edit textarea. Its "Reset comments" and "Send to Chat" buttons are disabled. There are no ".diff-draft-review-toggle" elements after resetting. The message input still has value exactly "Unrelated composer draft". Eval document.querySelectorAll('.message-user').length === window.resetUserCount should return true. Resize to 1280x900 and verify both footer buttons are visible and disabled, and there are still no ".diff-draft-review-toggle" elements.
5. Click "Close draft review". Resize to 390x844 and wait for the mobile hunk rail. There are no ".diff-draft-inline" elements. Click "Comment on current hunk", fill ".diff-rethink-rail-input" with "Fresh comment", and click "Save Comment". Open ".diff-rethink-change-nav .diff-draft-review-toggle", whose accessible name is exactly "Open draft review, 1 comment". The dialog has exactly one article containing "Fresh comment", no edit textarea, and an enabled "Send to Chat" button. "First comment", "Second comment", and "Unfinished edit" are absent from the dialog. The composer is still unchanged.`, func(s *shelley) {
		dir := mobileDiffRepo(s)
		file := filepath.Join(dir, "only.txt")
		s.write(file, "before\n")
		mobileDiffCommit(s, dir, "base")
		s.write(file, "after\n")
		mobileDiffConversation(s, dir)
	})
}

func TestE2EMobileDiffOpenCommentFileNavigation(t *testing.T) {
	e2e(t, useful(0), `An open phone comment rail navigates between files without losing focus or mixing their drafts. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+mobileDiffWorking+`Use these selectors directly; wait for conditions, never sleep.
1. Click button.diff-tree-row[title="first.txt"], wait for the modified Monaco editor, then click .diff-rethink-change-nav [aria-label="Comment on current hunk"]. Resize to 390x420. Save the textarea .diff-rethink-rail-input as window.railInput. Fill it with "First file draft" using the native textarea value setter and a bubbling input event.
2. Click .diff-rethink-next-change. Wait for .diff-rethink-file-name to read second.txt and the textarea aria-label to read "Comment on Line 1, new" (the new comment target is ready). Check that the textarea is still window.railInput, empty, and focused. Fill it with "Second file draft" using the same setter and event.
3. Click .diff-rethink-prev-change. Wait for .diff-rethink-file-name to read first.txt and the textarea value to read "First file draft". Check that it is still window.railInput and focused.
4. Click .diff-rethink-next-change. Wait for .diff-rethink-file-name to read second.txt and the textarea value to read "Second file draft". Check that it is still window.railInput and focused. Click .diff-rethink-cancel-comment and wait for the textarea to close.`, func(s *shelley) {
		dir := mobileDiffRepo(s)
		s.write(filepath.Join(dir, "first.txt"), "first\n")
		s.write(filepath.Join(dir, "second.txt"), "second\n")
		mobileDiffCommit(s, dir, "base")
		s.write(filepath.Join(dir, "first.txt"), "first changed\n")
		s.write(filepath.Join(dir, "second.txt"), "second changed\n")
		mobileDiffConversation(s, dir)
	})
}

func TestE2EMobileDiffCurrentHunkGutter(t *testing.T) {
	e2e(t, useful(1), `The phone diff highlights only the active hunk's plus and minus signs, including on first load and after navigation. Use waits, never sleep steps. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+mobileDiffWorking+`Wait for ".diff-viewer-rethink" (the graph container persists; do not wait for it to hide), then click the row ".diff-rethink-mobile-files [title='hunks.txt']" (not the graph's diffstat). Wait for ".diff-rethink-bottom-change-label" to read "Change 1 of 3" and for the first ".diff-current-hunk-gutter" decoration. In the modified Monaco editor, evaluate expecting true: there are three ".inline-deleted-margin-view-zone" elements; exactly the first has class "diff-current-hunk-deleted"; its ".delete-sign" and the active ".margin-view-overlays > div:has(.diff-current-hunk-gutter) .insert-sign" both have computed background-color alpha 0.25. Click "Next hunk (.)"; the single marked deleted zone is now the second one, with alpha 0.25. Click "Next hunk (.)" again; the single marked zone is the third one. Click "Previous hunk (,)"; the single marked zone is again the second one.`, func(s *shelley) {
		dir := mobileDiffRepo(s)
		lines := make([]string, 50)
		for i := range lines {
			lines[i] = fmt.Sprintf("line %d", i+1)
		}
		s.write(filepath.Join(dir, "hunks.txt"), strings.Join(lines, "\n")+"\n")
		mobileDiffCommit(s, dir, "base")
		for _, n := range []int{5, 20, 35} {
			lines[n-1] += " changed"
		}
		s.write(filepath.Join(dir, "hunks.txt"), strings.Join(lines, "\n")+"\n")
		mobileDiffConversation(s, dir)
	})
}

func TestE2EMobileDiffSingleFileNoPhantomNext(t *testing.T) {
	e2e(t, useful(0), `On a phone, selecting the only changed file again must not offer a next arrow when it has one change. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+mobileDiffWorking+`Click the row titled "only.txt" in ".diff-rethink-mobile-files". Wait until ".diff-rethink-bottom-change-label" reads "Change 1 of 1" AND there is no ".diff-rethink-next-change" button (the editor has finished loading). Click ".diff-rethink-file-jump" to open the file picker, then click its row titled "only.txt" again. The picker closes, ".diff-rethink-file-position" reads "File 1 of 1", the bottom still reads "Change 1 of 1", and there must still be no ".diff-rethink-next-change" button.`, func(s *shelley) {
		dir := mobileDiffRepo(s)
		file := filepath.Join(dir, "only.txt")
		s.write(file, "before\n")
		mobileDiffCommit(s, dir, "base")
		s.write(file, "after\n")
		mobileDiffConversation(s, dir)
	})
}

func TestE2EMobileDiffWorkingFileReadOnly(t *testing.T) {
	e2e(t, useful(0), `A working text file on a phone is review-only. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+mobileDiffWorking+`Click the row titled "only.txt" in ".diff-rethink-mobile-files" and wait for the modified Monaco editor to show "after". There is no button named "Edit" in the viewer. Evaluate that ".diff-viewer-rethink .editor.modified textarea.inputarea" has readOnly === true and tabIndex === -1. Tap the "after" line in the modified editor; the rail input ".diff-rethink-rail-input" opens above the rail's Cancel (".diff-rethink-cancel-comment") and Save Comment (".diff-rethink-comment-change") buttons. In an eval with expect true, assert the input's bounding-box bottom is at or above both buttons' tops and its value is empty. There is no ".diff-viewer-selected-text" code excerpt. Click Cancel without submitting. Call "working-file-content" and confirm it still returns "after\n".`, func(s *shelley) {
		dir := mobileDiffRepo(s)
		file := filepath.Join(dir, "only.txt")
		s.write(file, "before\n")
		mobileDiffCommit(s, dir, "base")
		s.write(file, "after\n")
		s.call("working-file-content", func() string { return s.read(file) })
		mobileDiffConversation(s, dir)
	})
}

func TestE2EDiffViewerSavesBeforeMobileResize(t *testing.T) {
	e2e(t, useful(0), `An unsaved desktop edit survives resizing to the review-only phone viewer. Navigate to /c/{{SLUG}} at 1280x900. Open Diffs: `+fmt.Sprintf(openToolWide, "Diffs")+`. Choose Working changes from the commit picker and select "only.txt" in the file tree or selector. Wait for its modified Monaco editor to show "after". Click "Edit mode", type "resize-kept" into the modified file, and immediately resize to 390x844 before its one-second autosave. The mobile viewer has no Edit button. Call "working-file-content" until the file on disk contains "resize-kept".`, func(s *shelley) {
		s.viewport(1280, 900)
		dir := s.gitRepo(s.tempDir("desktop-resize"))
		file := filepath.Join(dir, "only.txt")
		s.write(file, "before\n")
		mobileDiffCommit(s, dir, "base")
		s.write(file, "after\n")
		s.call("working-file-content", func() string { return s.read(file) })
		mobileDiffConversation(s, dir)
	})
}

func TestE2EMobileDiffFilePicker(t *testing.T) {
	e2e(t, useful(1), `The phone file picker fills the content width and height beneath its 48px toolbar, scrolls, and dismisses without closing the viewer. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+mobileDiffWorking+`Click the row with title "change-01.txt" in ".diff-rethink-mobile-files". The file position reads "File 1 of 35"; the bottom ".diff-rethink-bottom-change-label" reads "Change 1 of 1" without a file count. The ".diff-rethink-file-jump" spans the phone width with 8px side gutters (compare its bounding box with innerWidth). Click it. The dialog named "Choose file" is visible. Compare its getBoundingClientRect() with ".diff-viewer-content": left and right edges coincide, its top is 48px below content top and its bottom coincides with content bottom (round differences; eval expecting true). Its ".diff-tree-scroll" has scrollHeight greater than clientHeight. Scroll the row titled "change-35.txt" into view and click it. The file name is "change-35.txt", the position is "File 35 of 35", and the dialog is hidden. Open the picker again, resize viewport to 390x500, and repeat the edge and 48px geometry assertion. Press Escape: picker hidden, overlay still visible. Open picker again, click its button named exactly "Close": picker hidden and overlay visible.`, func(s *shelley) {
		dir := mobileDiffRepo(s)
		s.write(filepath.Join(dir, "base.txt"), "base\n")
		mobileDiffCommit(s, dir, "base")
		for n := 1; n <= 35; n++ {
			s.write(filepath.Join(dir, fmt.Sprintf("change-%02d.txt", n)), fmt.Sprintf("file %d\n", n))
		}
		mobileDiffConversation(s, dir)
	})
}

func TestE2EMobileDiffFileFilterPunctuation(t *testing.T) {
	e2e(t, useful(0), `Typing a period in the phone file filter must not navigate away from the picker. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+mobileDiffWorking+`Click the row titled "foo.txt" in ".diff-rethink-mobile-files", then click ".diff-rethink-file-jump". The dialog "Choose file" is visible. Fill its textbox "Filter files" with "foo", then focus that textbox and press the period key. Its value is "foo."; the picker remains visible and has the row "foo.txt" but not "fooXtxt".`, func(s *shelley) {
		dir := mobileDiffRepo(s)
		s.write(filepath.Join(dir, "base.txt"), "base\n")
		mobileDiffCommit(s, dir, "base")
		s.write(filepath.Join(dir, "foo.txt"), "text\n")
		s.write(filepath.Join(dir, "fooXtxt"), "other\n")
		mobileDiffConversation(s, dir)
	})
}

func TestE2EMobileDiffCommitMessageResize(t *testing.T) {
	e2e(t, useful(0), `A commit body gets an overflow control when the phone viewport shrinks. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+`The ".diff-viewer-rethink .diff-commit-message" is visible and contains no button named "Show more". Resize to 390x500; its "Show more" button becomes visible. Click it; "Show less" is visible. Resize to 390x844, click "Show less"; there are zero "Show more" buttons in the message.`, func(s *shelley) {
		dir := mobileDiffRepo(s)
		s.write(filepath.Join(dir, "note.txt"), "hello\n")
		s.gitIn(dir, "add", ".")
		s.gitIn(dir, "commit", "-m", "long message", "-m", strings.Repeat("A long note ", 40))
		mobileDiffConversation(s, dir)
	})
}

func TestE2EMobileDiffCompareRange(t *testing.T) {
	e2e(t, useful(0), `A compare range is one navigation stop and shows its net changes. The graph rows for commits {{FIRST}} and {{LAST}} are identified by their seven-character hashes. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+`Click ".commit-picker-trigger". In ".git-graph-container" click "Select range". The row containing "Working changes" has aria-disabled="true" before a starting commit is selected. Click the row containing {{LAST}}, then the row containing {{FIRST}}. The ".diff-rethink-range-overview" has heading "2 commits", exactly two details elements, none open. Click the last details' summary; heading "Notes" appears. Click that summary again. The overview has treeitem "b.txt" and no treeitem "a.txt" (net diff). Click the button "Next file"; ".diff-rethink-file-name" reads "b.txt". Click "Previous commit"; the range overview is visible again. Click "Show range in graph". The row for {{FIRST}} has class "git-graph-row-range-from" and {{LAST}} has class "git-graph-row-range-to". Click "Select range", then row {{FIRST}}, then "Working changes". The overview heading reads "2 commits" and its "Show range in graph" button contains "working".`, func(s *shelley) {
		dir := mobileDiffRepo(s)
		a := filepath.Join(dir, "a.txt")
		s.write(a, "original\n")
		mobileDiffCommit(s, dir, "base")
		s.write(a, "changed\n")
		s.gitIn(dir, "add", ".")
		s.gitIn(dir, "commit", "-m", "change a", "-m", "## Notes\n\nImportant")
		s.v("FIRST", s.gitIn(dir, "rev-parse", "--short=7", "HEAD"))
		s.write(a, "original\n")
		s.write(filepath.Join(dir, "b.txt"), "added\n")
		mobileDiffCommit(s, dir, "revert a and add b")
		s.v("LAST", s.gitIn(dir, "rev-parse", "--short=7", "HEAD"))
		mobileDiffConversation(s, dir)
	})
}

func TestE2EMobileGitGraphSingleRail(t *testing.T) {
	e2e(t, useful(0), `On a 320px phone, the mobile Git Graph fits All branches, Current branch, Select range, repository, and Close on one rail, without a title or second bar. Use waits, never sleep steps. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+`Click ".commit-picker-trigger". Wait for ".git-graph-toolbar-mobile". Its buttons have aria-labels "All branches", "Current branch", "Select range", "Pick repository", and "Close"; their visible text matches those first three labels. Evaluate expecting true that the rail's scrollWidth is at most clientWidth, its buttons' bounding boxes lie within the viewport, and there is no ".git-graph-compare-bar" or "Git graph" title. Click ".git-graph-toolbar-mobile .git-graph-compare-tool"; evaluate expecting true that this same button now has visible text "Cancel" and aria-pressed="true" (not the All scope button). Click it again; its visible text is "Select range" and aria-pressed="false".`, func(s *shelley) {
		dir := mobileDiffRepo(s)
		s.viewport(320, 700)
		s.write(filepath.Join(dir, "note.txt"), "before\n")
		mobileDiffCommit(s, dir, "base")
		s.write(filepath.Join(dir, "note.txt"), "after\n")
		mobileDiffCommit(s, dir, "change")
		mobileDiffConversation(s, dir)
	})
}

func TestE2EMobileDiffAlignedDeletionRuler(t *testing.T) {
	e2e(t, useful(0), `A wrapped replacement aligns the red deletion marker with the green addition in the phone overview ruler. Use waits, never sleeps. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+mobileDiffWorking+`Click the row titled "wrap.txt" in ".diff-rethink-mobile-files". Wait for ".diff-rethink-deletion-overview-mark" and "Change 1 of 1". Evaluate expecting true: the only ".inline-deleted-margin-view-zone" is over 250px tall, the only red marker is over 30px tall, and the original overview canvas has computed visibility "hidden". The red marker's offsetTop differs by at most 2px from that deletion margin zone's own style.top multiplied by the marker's offsetHeight divided by the zone's offsetHeight (the exact wrapped zone height). Read the pixels at x=20 from ".diffOverview canvas.modified" using getImageData; the first row with alpha > 0 and green > red * 1.15, divided by devicePixelRatio, is within 5px of the red marker's offsetTop + offsetHeight. Also evaluate expecting true: the ".diff-viewer-editor" top minus ".diff-rethink-file-jump" bottom is 6px (rounded), the ".diff-viewer-main" background matches ".diff-rethink-change-nav", and the change nav is 50px high.`, func(s *shelley) {
		dir := mobileDiffRepo(s)
		file := filepath.Join(dir, "wrap.txt")
		before := strings.Repeat("long removed text to wrap ", 12)
		after := strings.Repeat("long added text to wrap ", 12)
		s.write(file, "keep start\n"+strings.Repeat(before+"\n", 3)+"keep end\n")
		mobileDiffCommit(s, dir, "base")
		s.write(file, "keep start\n"+strings.Repeat(after+"\n", 3)+"keep end\n")
		mobileDiffConversation(s, dir)
	})
}

func TestE2EMobileDiffEOFDeletionRuler(t *testing.T) {
	e2e(t, useful(0), `A pure deletion after a wrapped last line appears at the correct place in the phone overview ruler, not inside that surviving line. Use waits, never sleeps. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+mobileDiffWorking+`Click the row titled "tail.txt" in ".diff-rethink-mobile-files". Wait for one ".diff-rethink-deletion-overview-mark" and its ".inline-deleted-margin-view-zone". Evaluate expecting true: the margin zone contains at least 16 direct ".delete-sign" children, is visible with an offsetHeight of at least 250px and a style.top over 100px (the surviving line wraps), and the red marker has at least 4px height. Its offsetTop differs by at most 2px from that zone's own style.top multiplied by the marker's offsetHeight divided by the zone's offsetHeight.`, func(s *shelley) {
		dir := mobileDiffRepo(s)
		file := filepath.Join(dir, "tail.txt")
		survivor := strings.Repeat("long surviving final line ", 12)
		s.write(file, "keep start\n"+survivor+"\n"+strings.Repeat("remove this line\n", 16))
		mobileDiffCommit(s, dir, "base")
		s.write(file, "keep start\n"+survivor)
		mobileDiffConversation(s, dir)
	})
}
