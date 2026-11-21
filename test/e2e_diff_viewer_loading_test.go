package test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
)

// Ported from ui/e2e/diff-viewer-loading.spec.ts.

func TestE2EDiffViewerLoadingCommitMessageFirst(t *testing.T) {
	e2e(t, `The diff viewer shows the commit message before the changed files load and keeps it selected. The conversation /c/{{SLUG}} runs in a git repository whose last commit, "update story", changed story.txt. The server's answers to the diff viewer's changed-files requests (GET /api/git/diffs/<commit>/files?...) are held back until call "release"; call "files-requested" says whether one has arrived ("true" or "false"). Perform the steps in order. 1. Navigate to /c/{{SLUG}} and wait up to 30s for the message input (data-testid "message-input") to be visible. 2. Open the Diffs tool: `+fmt.Sprintf(openToolWide, "Diffs")+`. 3. The overlay ".diff-viewer-overlay" should be visible. 4. Call "files-requested" until it returns "true". 5. The first "select.diff-viewer-select" in the overlay (the file select) should come to have a value starting with "commit-message:". 6. The overlay's ".diff-viewer-editor .editor.modified .view-lines" should contain the text "update story". 7. The file select must have exactly 0 options with value "story.txt" (option[value="story.txt"]). 8. Call "release". 9. The file select should come to have exactly 1 option with value "story.txt". 10. The file select's value should still start with "commit-message:".`, func(s *shelley) {
		s.viewport(1280, 900)
		dir := s.gitRepo(filepath.Join(s.dir, "story"))
		s.write(filepath.Join(dir, "story.txt"), "before\n")
		s.gitIn(dir, "add", "story.txt")
		s.gitIn(dir, "commit", "--quiet", "-m", "initial story")
		s.write(filepath.Join(dir, "story.txt"), "actual file content\n")
		s.gitIn(dir, "add", "story.txt")
		s.gitIn(dir, "commit", "--quiet", "-m", "update story")

		requested := s.signal("files-requested")
		release := s.gate("release")
		s.route("GET /api/git/diffs/{commit}/files", func(w http.ResponseWriter, r *http.Request) {
			requested.Store(true)
			if release.wait(r) {
				s.pass.ServeHTTP(w, r)
			}
		})
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}
