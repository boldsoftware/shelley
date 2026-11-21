package test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ported from ui/e2e/git-graph-diffstat.spec.ts.

func TestE2EGitGraphDiffstatOpensFile(t *testing.T) {
	e2e(t, `A git graph diffstat link opens the selected changed file in the diff viewer. The conversation /c/{{SLUG}} runs in a git repository whose commits only ever changed example.txt. Perform the steps in order. 1. Navigate to /c/{{SLUG}} and wait up to 30s for the message input (data-testid "message-input") to be visible. 2. Open the Git Graph tool: `+fmt.Sprintf(openToolWide, "Git Graph")+`. 3. ".git-graph-container" should be visible (up to 30s). 4. The first ".git-graph-diffstat-link" in it should be visible (up to 30s). 5. Its title attribute is the changed file's path: it must be exactly "example.txt". 6. Its href must match the regular expression [?&]file=example\.txt (the path, URI-encoded, in a file query parameter). 7. Click that link. 8. The first "select.diff-viewer-select" in ".diff-viewer-overlay" should come to have the value "example.txt" (up to 30s).`, func(s *shelley) {
		s.viewport(1280, 900)
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

func TestE2EGitGraphDiffstatReadablePaths(t *testing.T) {
	const (
		github = "server/integrations/github/webhooks/notifications/dispatcher.go"
		slack  = "server/integrations/slack/webhooks/notifications/dispatcher.go"
	)
	longName := strings.Repeat("a_very_long_filename_", 6) + "test.go"
	files := []string{"db/db.go", github, slack, "server/" + longName, "assets/logo.bin"}
	e2e(t, `The git graph's diffstat keeps long paths distinct, counts aligned, and full-path links intact. The conversation /c/{{SLUG}} runs in a git repository whose last commit, "Update long paths", changed exactly five files: db/db.go, `+github+`, `+slack+`, server/`+longName+` (text files, one line replaced each) and assets/logo.bin (binary). "The link for P" is the element a[title="P"] in ".git-graph-container"; "the links" are its ".git-graph-diffstat-link" elements. Perform the steps in order. 1. Navigate to /c/{{SLUG}} and wait for the message input (data-testid "message-input") to be visible. 2. Open the Git Graph tool: `+fmt.Sprintf(openToolWide, "Git Graph")+`. 3. There should come to be exactly 5 links. 4. The container must hold exactly 0 ".git-graph-diffstat-bar" elements, and exactly one ".git-graph-diffstat-binary" element, with exactly the text "bin". 5. The ".git-graph-diffstat-path" of the link for `+github+` should have exactly the text "server/…/github/…/dispatcher.go", and that of the link for `+slack+` exactly "server/…/slack/…/dispatcher.go". 6. The link for db/db.go should contain the text "db/db.go", and the link for server/`+longName+` the text "`+longName+`". 7. The link for `+github+` should have exactly the accessible name "`+github+`, 1 added, 1 removed" (its aria-label), and the link for assets/logo.bin exactly "assets/logo.bin, binary". 8. The link for `+github+` should have the title attribute exactly "`+github+`" and an href matching the regular expression [?&]file=`+strings.ReplaceAll(github, "/", "%2F")+`. 9. In one eval, read getBoundingClientRect().right of every ".git-graph-diffstat-counts" inside the links: the largest minus the smallest must be less than 1. 10. Set the viewport to width 1280, height 900 (only width and height), then poll until every link has scrollWidth <= clientWidth + 1. 11. Set the viewport to width 390, height 900, then poll the same: every link has scrollWidth <= clientWidth + 1. 12. Set the viewport back to width 1280, height 900. 13. Set the style.width of the container's ".git-graph-detail" to "900px". 14. The ".git-graph-diffstat-path" of the link for `+github+` should then have exactly the text "`+github+`" (the full path). 15. Click the link for `+github+`. 16. The first "select.diff-viewer-select" in ".diff-viewer-overlay" should come to have the value "`+github+`".`, func(s *shelley) {
		s.viewport(1280, 900)
		repo := s.gitRepo(filepath.Join(s.dir, "readable-files"))
		write := func(content func(file string) []byte) {
			for _, file := range files {
				p := filepath.Join(repo, file)
				s.mkdir(filepath.Dir(p))
				s.check(os.WriteFile(p, content(file), 0o644))
			}
		}
		write(func(file string) []byte {
			if strings.HasSuffix(file, ".bin") {
				return []byte{0, 1}
			}
			return []byte("before\n")
		})
		s.gitIn(repo, "add", ".")
		s.gitIn(repo, "commit", "--quiet", "-m", "Base files")
		write(func(file string) []byte {
			if strings.HasSuffix(file, ".bin") {
				return []byte{0, 2}
			}
			return []byte("after\n")
		})
		s.gitIn(repo, "add", ".")
		s.gitIn(repo, "commit", "--quiet", "-m", "Update long paths")
		s.v("SLUG", s.conversationIn(repo, "Hello").Slug)
	})
}
