package test

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Ported from ui/e2e/diff-viewer-binary.spec.ts.

var (
	binaryImageBefore = mustBase64("iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAACXBIWXMAAAABAAAAAQBPJcTWAAAAF0lEQVR4nGP4y8BAEiJN9aiGUQ1DSgMAfsz9ATa/hGgAAAAASUVORK5CYII=")
	binaryImageAfter  = mustBase64("iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAACXBIWXMAAAABAAAAAQBPJcTWAAAAFUlEQVR4nGNgqGcgDY1qGNUwfDUAAIZufwE07jNxAAAAAElFTkSuQmCC")
	binaryAudio       = mustBase64("SUQzBAAAAAAAI1RTU0UAAAAPAAADTGF2ZjYwLjE2LjEwMAAAAAAAAAAAAAAA/+M4wAAAAAAAAAAAAEluZm8AAAAPAAAABgAAAogAcXFxcXFxcXFxcXFxcXFxcY6Ojo6Ojo6Ojo6Ojo6Ojo6OqqqqqqqqqqqqqqqqqqqqqsfHx8fHx8fHx8fHx8fHx8fH4+Pj4+Pj4+Pj4+Pj4+Pj4+P/////////////////////AAAAAExhdmM2MC4zMQAAAAAAAAAAAAAAACQDcAAAAAAAAAKIwPmwWQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/+MYxAAM+ALeWUEAApJJCKNtwAfB8Hz8QAgGMHwffKAgGPUXB8H+UBD/wcOYgB/gR3D/R/8EAQB8HwfB8CAg7D44bDYd/xqI/+MYxAcNsPKgAYfQAKPDb/B8Og9AF/4k7xcIEJQ6cu75LLDBHB6FRfi+QioLuuymMxmtTUwK/4SBoSnQav/////6y6wtQAIA/+MYxAsM6OZMAdYwAADAGGADA2EQDCOAwDBeJYDHON4DqSssDJcN8DDEAsYgAIBg8YzICAxt/////+pNy4XC0AuAEBwawMA4/+MYxBINYOpIAVYwAEEDDCEkDD8L4DG4I4Du8SADJqJECoHgMBwBgMBgBBjTJQ4IxQEEkhsxEbDo7/UcG5Ia+7tEYIgjAE6J/+MYxBcS8XakyYegAJQG8BFsBYoXyqLmHyGogDBRZirL01M20qDuJ0ipYKaZ5X//z5eMTBRoFm/+syDIoaGN//Wlaq8zPqZn/+MYxAYNCP20AcYYAdVrz5nKqvJxJLTSMsSyq8zMsaAQmAYBEnIgQEKMKAiYBEjoroILkNiv//4N6bkqTEFNRTMuMTAwqqqq")
	binaryBytes       = []byte{0, 1, 255}
)

func mustBase64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// binaryDiffFixture makes a repository with uncommitted changes to binary
// files: image.png (a 16x16 PNG, changed), tone.mp3 (changed), opaque.bin
// (bytes 00 01 ff, now 00 02 ff), added.bin (new), deleted.bin (removed) and
// text.dat ("before" changed to "after"), and a conversation there as
// {{SLUG}}. It returns the repository.
func binaryDiffFixture(s *shelley) string {
	cwd := s.gitRepo(filepath.Join(s.dir, "binary"))
	write := func(name string, b []byte) {
		s.check(os.WriteFile(filepath.Join(cwd, name), b, 0o644))
	}
	write("image.png", binaryImageBefore)
	write("tone.mp3", binaryAudio)
	write("opaque.bin", binaryBytes)
	write("deleted.bin", binaryBytes)
	write("text.dat", []byte("before\n"))
	s.gitIn(cwd, "add", ".")
	s.gitIn(cwd, "commit", "--quiet", "-m", "binary diff fixtures")
	write("image.png", binaryImageAfter)
	write("tone.mp3", append(append([]byte{}, binaryAudio...), make([]byte, 8)...))
	write("opaque.bin", []byte{0, 2, 255})
	write("added.bin", binaryBytes)
	write("text.dat", []byte("after\n"))
	s.check(os.Remove(filepath.Join(cwd, "deleted.bin")))
	s.v("SLUG", s.conversationIn(cwd, "Hello").Slug)
	return cwd
}

const binaryDiffSetup = `The conversation /c/{{SLUG}} runs in a git repository with uncommitted changes: image.png (a 16x16 PNG, changed), tone.mp3 (an MP3, changed), opaque.bin (binary bytes 00 01 ff, now 00 02 ff), added.bin (a new binary file), deleted.bin (a deleted binary file) and text.dat (a text file whose line "before" became "after"). "The file select" is the overlay's "select.diff-viewer-select" (pick a file by filling it with the file's name as the option value); "the panel" is the element with data-testid "binary-file-panel" in the overlay. Perform the steps in order. `

func TestE2EDiffViewerBinaryOptInReadOnly(t *testing.T) {
	e2e(t, useful(0), binaryDiffSetup+`Binary contents are opt-in and remain read-only in edit mode. Call "opaque.bin" returns the bytes of opaque.bin on disk in hex; call "text.dat has editable" says whether text.dat on disk contains "editable" ("true" or "false"); call "mark writes" marks the current point in the POST /api/write-file requests, and call "first write after mark" returns the HTTP status the server answered the first such request after the mark with ("none" while there is none). 1. Navigate to /c/{{SLUG}}. 2. Open the Diffs tool: `+fmt.Sprintf(openToolWide, "Diffs")+`. 3. The overlay ".diff-viewer-overlay" should be visible. 4. Pick opaque.bin in the file select. 5. The panel should be visible. 6. The overlay's ".diff-viewer-binary-toolbar" should have exactly the text "View as text". 7. The overlay's ".diff-viewer-editor" should be hidden. 8. The panel should hold exactly 2 links named exactly "Download". 9. Every ".diff-viewer-binary-side-content" in the panel (check them all in one eval) must contain exactly 1 element matching ".diff-viewer-binary-line + .diff-viewer-binary-download" (a Download link right after a line). 10. The overlay's button named exactly "Next change (.)" should be disabled. 11. Click the overlay's button named exactly "View as text". 12. The overlay's ".diff-viewer-editor" should be visible, the panel hidden, and the ".diff-viewer-binary-toolbar" should have exactly the text "Binary view". 13. Click the overlay's button named exactly "Edit mode". 14. Click the editor's modified side, ".diff-viewer-editor .editor.modified .view-lines:not(.line-delete)". 15. Insert the text "must-not-write" into the focused element with the insert_text action (one paste-like input event). 16. That modified side must not contain "must-not-write". 17. Press "s" with the Control modifier. 18. Right away, call "opaque.bin": it must return exactly "0002ff". 19. Click the overlay's button named exactly "Binary view". 20. The panel should be visible. 21. Pick text.dat in the file select. 22. The panel should be hidden, and the modified side (".diff-viewer-editor .editor.modified .view-lines:not(.line-delete)") should contain "after". 23. Click the modified side, then insert the text "editable" the same way (insert_text). 24. The modified side should contain "editable". 25. Call "mark writes". 26. Press "s" with the Control modifier. 27. Call "first write after mark" until it returns "200". 28. Call "text.dat has editable": it must return "true". 29. Pick opaque.bin in the file select again. 30. The panel should be visible and the ".diff-viewer-editor" hidden. 31. Call "opaque.bin": it must still return exactly "0002ff".`, func(s *shelley) {
		s.viewport(1280, 900)
		cwd := binaryDiffFixture(s)
		var mu sync.Mutex
		var statuses []int
		mark := 0
		s.route("POST /api/write-file", func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			s.pass.ServeHTTP(rec, r)
			mu.Lock()
			statuses = append(statuses, rec.status)
			mu.Unlock()
		})
		s.call("mark writes", func() string {
			mu.Lock()
			defer mu.Unlock()
			mark = len(statuses)
			return "marked"
		})
		s.call("first write after mark", func() string {
			mu.Lock()
			defer mu.Unlock()
			if len(statuses) <= mark {
				return "none"
			}
			return strconv.Itoa(statuses[mark])
		})
		s.call("opaque.bin", func() string { return hex.EncodeToString([]byte(s.read(filepath.Join(cwd, "opaque.bin")))) })
		s.call("text.dat has editable", func() string {
			return strconv.FormatBool(strings.Contains(s.read(filepath.Join(cwd, "text.dat")), "editable"))
		})
	})
}

func TestE2EDiffViewerBinaryNativeMediaPreviews(t *testing.T) {
	e2e(t, useful(0), binaryDiffSetup+`Images and audio have native previews without download links. 1. Navigate to /c/{{SLUG}}. 2. Open the Diffs tool: `+fmt.Sprintf(openToolWide, "Diffs")+`. 3. The overlay ".diff-viewer-overlay" should be visible. 4. Pick image.png in the file select. 5. The panel should come to hold exactly 2 "img" elements. 6. Both images' naturalWidth should come to be exactly 16 (poll). 7. In one eval with expect true, both images are vertically centered in their parents: for each image, abs((img.top - parent.top) - (parent.height - img.height)/2) is less than 0.5, using getBoundingClientRect(). 8. The panel must hold exactly 0 links named "Download" and exactly 0 ".diff-viewer-binary-side-header" elements. 9. Pick tone.mp3 in the file select. 10. The panel should come to hold exactly 2 "audio" elements. 11. Both players' duration should come to be greater than 0 (poll). 12. Each player must have the attribute controls with the value "" (empty string), and getComputedStyle(player.parentElement).paddingTop must be exactly "12px" for each. 13. The panel must hold exactly 0 links named "Download".`, func(s *shelley) {
		s.viewport(1280, 900)
		binaryDiffFixture(s)
	})
}

func TestE2EDiffViewerBinaryAddedDeletedHatched(t *testing.T) {
	e2e(t, useful(0), binaryDiffSetup+`Added and deleted binaries shrink and hatch the missing side like text diffs. 1. Navigate to /c/{{SLUG}}. 2. Open the Diffs tool: `+fmt.Sprintf(openToolWide, "Diffs")+`. 3. The overlay ".diff-viewer-overlay" should be visible. Then do steps 4-9 first for added.bin, whose missing side is the FIRST ".diff-viewer-binary-side" in the panel (index 0), and then for deleted.bin, whose missing side is the SECOND (index 1): 4. Pick the file in the file select. 5. The panel should be visible. 6. The missing side should have the class "diff-viewer-binary-side-empty" and be empty (its textContent.trim() is ""). 7. Its width relative to its parent's, el.getBoundingClientRect().width / el.parentElement.getBoundingClientRect().width, should come to be within 0.005 of 0.1 (poll: reduced motion still gives every property a 0.01ms transition, so the new split settles a frame after the class changes). 8. getComputedStyle(missingSide).backgroundImage must contain "linear-gradient". 9. The panel must hold exactly 1 link named exactly "Download".`, func(s *shelley) {
		s.viewport(1280, 900)
		binaryDiffFixture(s)
	})
}

func TestE2EDiffViewerBinaryMobileReadOnly(t *testing.T) {
	e2e(t, useful(0), `On a phone, binary-as-text cannot be edited or saved. Navigate to /c/{{SLUG}}. Open Diffs: `+fmt.Sprintf(openToolPhone, "Diffs")+`. The overlay ".diff-viewer-overlay" should be visible. `+mobileDiffWorking+`In its working-changes overview click ".diff-rethink-mobile-files .diff-tree-code-row[title='opaque.bin']". The panel with data-testid "binary-file-panel" is visible. There is no button named exactly "Edit". Click "View as text"; the ".diff-viewer-editor" becomes visible and there is still no button named exactly "Edit". Click the last ".editor.modified .view-lines" and insert_text "must-not-write". It must not contain that text. Press Control+s, then call "opaque.bin": it returns exactly "0002ff".`, func(s *shelley) {
		s.viewport(390, 844)
		cwd := binaryDiffFixture(s)
		s.call("opaque.bin", func() string { return hex.EncodeToString([]byte(s.read(filepath.Join(cwd, "opaque.bin")))) })
	})
}
