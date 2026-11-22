package test

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestE2EGitGraphSelectedHover(t *testing.T) {
	e2e(t, useful(0), `A selected desktop Git Graph row keeps its selection background when hovered. Navigate to /c/{{SLUG}}, then open Git Graph: `+fmt.Sprintf(openToolWide, "Git Graph")+`. Wait for ".git-graph-container .git-graph-row-selected" to be visible. Eval window.__selectedColor = getComputedStyle(document.querySelector(".git-graph-container .git-graph-row-selected")).backgroundColor without an expectation. Hover that same selected row. Eval getComputedStyle(document.querySelector(".git-graph-container .git-graph-row-selected")).backgroundColor === window.__selectedColor, expecting true.`, func(s *shelley) {
		s.viewport(1280, 900)
		dir := s.gitRepo(s.tempDir("graph-hover"))
		file := filepath.Join(dir, "example.txt")
		s.write(file, "before\n")
		mobileDiffCommit(s, dir, "Base")
		s.write(file, "after\n")
		mobileDiffCommit(s, dir, "Change")
		mobileDiffConversation(s, dir)
	})
}
