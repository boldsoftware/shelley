package test

import (
	"path/filepath"
	"testing"
)

// Ported from ui/e2e/gitinfo-layout.spec.ts.

// gitinfoLayoutJS checks that the viewport is w wide and the layout of the
// strip el at that width, returning "ok" or what is off.
const gitinfoLayoutJS = `((el, w) => { const rect = (s) => el.querySelector(s).getBoundingClientRect(); const title = rect('.msg-subject'), branch = rect('.msg-branch'), hash = rect('.msg-commit-hash'), path = rect('.msg-worktree'), diff = rect('.msg-diff-link'); const fonts = [...el.querySelectorAll('.msg-subject, .msg-branch, .msg-commit-hash, .msg-worktree')].map((c) => getComputedStyle(c).fontSize); const actionFont = getComputedStyle(el.querySelector('.msg-diff-link')).fontSize; const pathLabel = el.querySelector('.msg-worktree').textContent.trim(); const overflow = document.documentElement.scrollWidth > window.innerWidth; const b = el.querySelector('.msg-branch'); const branchOverflow = b.scrollWidth > b.clientWidth + 1; const bad = []; if (window.innerWidth !== w) bad.push('innerWidth ' + window.innerWidth); if (!fonts.every((f) => f === '14px')) bad.push('fonts ' + fonts); if (actionFont !== '12px') bad.push('actionFont ' + actionFont); if (overflow) bad.push('page overflows'); if (!(Math.abs(title.x - branch.x) < 1)) bad.push('title.x ' + title.x + ' branch.x ' + branch.x); if (diff.height !== 22) bad.push('diff.height ' + diff.height); if (!pathLabel.endsWith('active-checkout')) bad.push('pathLabel ' + pathLabel); if (window.innerWidth > 640) { if (title.height !== 20) bad.push('title.height ' + title.height); if (diff.y - title.bottom !== 7) bad.push('diff.y - title.bottom ' + (diff.y - title.bottom)); if (!(Math.abs(branch.y + branch.height / 2 - diff.y - diff.height / 2) < 1)) bad.push('branch/diff centers ' + branch.y + ',' + branch.height + ' ' + diff.y + ',' + diff.height); } else { if (branchOverflow) bad.push('branch overflows'); if (!(diff.y >= Math.max(branch.bottom, hash.bottom, path.bottom))) bad.push('diff.y ' + diff.y + ' above ' + Math.max(branch.bottom, hash.bottom, path.bottom)); } return bad.length ? bad.join('; ') : 'ok'; })(STRIP, W)`

func TestE2EGitinfoCompactLayoutBothThemes(t *testing.T) {
	e2e(t, `Git info keeps the approved compact layout in both themes. The conversation /c/{{SLUG}} runs in the git repository {{REPO}}, on the branch "feature/keep-long-conversation-metadata-readable", where its agent committed with the subject "Keep full commit subjects and branch names readable in the compact conversation git strip"; that commit's short hash is {{HASH}}. "The strip" is the element with data-testid "message-gitinfo" whose text contains {{HASH}}. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The strip's link named exactly "Open Diff" should be visible. 3. The strip's ".msg-subject" should have exactly the text "Keep full commit subjects and branch names readable in the compact conversation git strip" and the title attribute exactly the same. 4. Its ".msg-branch" should have exactly the text "feature/keep-long-conversation-metadata-readable". 5. Its ".msg-worktree" should have the title attribute exactly "{{REPO}}". 6. The Open Diff link's href must contain "cwd=" followed by encodeURIComponent("{{REPO}}") (compare in an eval). Then check the layout in the light theme and then in the dark one: 7. Eval document.documentElement.classList.toggle("dark", false). 8. The strip's computed background-color should be exactly "rgb(243, 244, 246)". 9. For each viewport width 1280, 960 and 390 in turn: set the viewport to that width and height 900 (only width and height), then poll this eval, with STRIP replaced by an expression for the strip and W by that width, until it returns "ok": `+gitinfoLayoutJS+`. 10. Eval document.documentElement.classList.toggle("dark", true). 11. The strip's computed background-color should be exactly "rgb(31, 41, 55)". 12. Again for each viewport width 1280, 960 and 390 in turn: set the viewport to that width and height 900, then poll the same eval (W being that width) until it returns "ok". 13. Click the strip's "Open Diff" link. 14. ".diff-viewer-overlay" should be visible.`, func(s *shelley) {
		repo := s.gitRepo(filepath.Join(s.dir, "projects", "long-parent-context", "worktrees", "active-checkout"))
		const (
			branch  = "feature/keep-long-conversation-metadata-readable"
			subject = "Keep full commit subjects and branch names readable in the compact conversation git strip"
		)
		s.write(filepath.Join(repo, "example.txt"), "before\n")
		s.gitIn(repo, "add", ".")
		s.gitIn(repo, "commit", "--quiet", "-m", "Base commit")
		s.gitIn(repo, "switch", "--quiet", "-c", branch)
		c := s.conversationIn(repo, `bash: printf 'after\n' > example.txt && git add example.txt && git commit -m '`+subject+`'`)
		s.v("SLUG", c.Slug)
		s.v("REPO", repo)
		s.v("HASH", s.gitIn(repo, "rev-parse", "--short", "HEAD"))
	})
}

func TestE2EGitinfoDetachedHeadAligned(t *testing.T) {
	e2e(t, `Git info aligns detached HEAD metadata with the subject. The conversation /c/{{SLUG}} runs in a git repository on a detached HEAD, where its agent committed "Detached HEAD change"; that commit's short hash is {{HASH}}. "The strip" is the element with data-testid "message-gitinfo" whose text contains {{HASH}}. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The strip's ".msg-subject" should be visible. 3. The strip must hold exactly 0 ".msg-branch" elements. 4. For each viewport width 1280, 960 and 390 in turn: set the viewport to that width and height 900 (only width and height), then poll an eval until it returns true, W being that width: window.innerWidth === W && Math.abs(strip.querySelector(".msg-subject").getBoundingClientRect().x - strip.querySelector(".msg-commit-hash").getBoundingClientRect().x) < 1.`, func(s *shelley) {
		repo := s.gitRepo(filepath.Join(s.dir, "detached"))
		s.write(filepath.Join(repo, "example.txt"), "before\n")
		s.gitIn(repo, "add", ".")
		s.gitIn(repo, "commit", "--quiet", "-m", "Base commit")
		s.gitIn(repo, "checkout", "--quiet", "--detach")
		c := s.conversationIn(repo, `bash: printf 'after\n' > example.txt && git add example.txt && git commit -m 'Detached HEAD change'`)
		s.v("SLUG", c.Slug)
		s.v("HASH", s.gitIn(repo, "rev-parse", "--short", "HEAD"))
	})
}
