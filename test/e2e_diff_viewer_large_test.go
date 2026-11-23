package test

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func largeDiffFixture(s *shelley) {
	dir := s.gitRepo(s.tempDir("large-diff"))
	padding := strings.Repeat(strings.Repeat("x", 127)+"\n", 8192)
	before, after := "oversized before\n"+padding, "oversized after\n"+padding
	s.write(filepath.Join(dir, "a-large.txt"), before)
	s.write(filepath.Join(dir, "b-small.txt"), "ordinary before\n")
	mobileDiffCommit(s, dir, "base")
	s.write(filepath.Join(dir, "a-large.txt"), after)
	s.write(filepath.Join(dir, "b-small.txt"), "ordinary after\n")
	s.v("OLD_BYTES", strconv.Itoa(len(before)))
	s.v("NEW_BYTES", strconv.Itoa(len(after)))
	s.v("REPO", dir)
	s.call("patch downloaded", func() string {
		for _, request := range s.requestLog() {
			uri, ok := strings.CutPrefix(request, "GET ")
			if !ok {
				continue
			}
			u, err := url.Parse(uri)
			if err == nil && u.Path == "/api/git/file-diff/working/a-large.txt" &&
				u.Query().Get("download") == "patch" && u.Query().Get("cwd") == dir {
				return "true"
			}
		}
		return "false"
	})
	mobileDiffConversation(s, dir)
}

const largeDiffWarning = `The panel with data-testid "large-file-panel" must be visible and contain "Too large to display", "Combined file size exceeds 2 MiB.", "Before:", "After:", and "KiB". The ".diff-viewer-editor" must be hidden. Poll an eval expecting true that every ".diff-viewer-editor .view-lines" has no ".view-line" children: any prior Monaco model must be detached, not merely hidden. `

const largeDiffDownload = `The panel must have exactly one link named "Download patch". Evaluate its href as a URL: pathname must be "/api/git/file-diff/working/a-large.txt", searchParams.get("download") must be "patch", and searchParams.get("cwd") must equal "{{REPO}}"; it must have a download attribute. In an async eval, fetch that same URL with only its download parameter removed. The actual JSON response must have tooLarge === true, path === "a-large.txt", oldBytes === {{OLD_BYTES}}, newBytes === {{NEW_BYTES}}, oldExists === true, newExists === true, oldContent === "" and newContent === "". Do not mock requests. Click the "Download patch" link, then call "patch downloaded" until it returns "true". `

func TestE2EDiffViewerLargeFile(t *testing.T) {
	e2e(t, useful(0), `On desktop, oversized working files are guarded by the actual API and switching files restores ordinary Monaco models. All selectors and expectations are given; no source exploration is needed. Generate and run the complete script, with waits and no sleep steps. Navigate to /c/{{SLUG}} and open Diffs: `+fmt.Sprintf(openToolWide, "Diffs")+`. Wait for ".diff-viewer-overlay". Click its ".commit-picker-trigger", wait for ".commit-picker-popover", then click ".commit-picker-row-working .commit-picker-row-main" in that popover (desktop uses a popover, not Git Graph). The file select is the first "select.diff-viewer-select" in the overlay. Select "b-small.txt" and wait for ".editor.modified .view-lines" to contain "ordinary after". Select "a-large.txt". `+largeDiffWarning+largeDiffDownload+`The overlay's button named exactly "Next change (.)" must be disabled. Select "b-small.txt" again. The large-file panel is hidden, the ".diff-viewer-editor" is visible and its modified side contains "ordinary after". Select "a-large.txt" again. `+largeDiffWarning, func(s *shelley) {
		s.viewport(1280, 900)
		largeDiffFixture(s)
	})
}

func TestE2EMobileDiffLargeFile(t *testing.T) {
	e2e(t, useful(0), `On a phone, oversized working files show an honest warning, remain selectable, and preserve swipe navigation both ways between blocked and ordinary files. All selectors and expectations are given; no source exploration is needed. For every eval return a single boolean and expect true, not JSON strings or objects. Generate and run the complete script. Use real touch start/move/end actions, not synthetic events or direct navigation calls, and no sleep steps. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+mobileDiffWorking+`Click ".diff-rethink-mobile-files [title='b-small.txt']" (the title is on a tree row, not a button). Wait for ".editor.modified .view-lines" to contain "ordinary after". Click ".diff-rethink-file-jump", then click ".diff-rethink-mobile-files[aria-label='Choose file'] [title='a-large.txt']". Do not use an unscoped title selector: Git Graph also holds hidden rows with these titles. `+largeDiffWarning+`The ".diff-rethink-bottom-change-label" must read exactly "Diff not displayed", not "No changes". `+largeDiffDownload+`Click the warning's "Choose another file" button. The dialog "Choose file" is visible. Click ".diff-rethink-mobile-files[aria-label='Choose file'] [title='b-small.txt']". The dialog and warning are hidden, ".diff-rethink-file-name" reads "b-small.txt", and the visible modified Monaco editor contains "ordinary after". Swipe RIGHT across ".diff-rethink-file-jump" with touch start at offsets x=40,y=10, move to x=260,y=10 in 3 steps, then touch end immediately (under 800ms). The file name becomes "a-large.txt". `+largeDiffWarning+`The bottom label reads "Diff not displayed". Swipe LEFT across the warning heading using viewport point touch actions, not selector touch actions: touch start at {x: r.right-5, y: r.top+r.height/2}, move to {x: r.left+5, y: r.top+r.height/2} in 3 steps, then end immediately. For each point, get r from document.querySelector('[data-testid="large-file-panel"] h3').getBoundingClientRect(); these points span over 100px. Use point rather than selector for this gesture because heading descendants may be covered by the parent panel during a drag; never bypass real touch events. The file name becomes "b-small.txt", the warning is hidden and the visible modified editor contains "ordinary after". Repeat the exact RIGHT touch gesture across ".diff-rethink-file-jump"; "a-large.txt" and the warning return. Repeat the exact LEFT viewport-point touch gesture across the warning heading; "b-small.txt" and the visible ordinary Monaco content return.`, func(s *shelley) {
		s.viewport(390, 844)
		largeDiffFixture(s)
	})
}
