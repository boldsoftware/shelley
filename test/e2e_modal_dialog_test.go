package test

// Ported from ui/e2e/modal-dialog.spec.ts.

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// The base Modal.vue is backed by PrimeVue Dialog (focus trap,
// role="dialog"/aria-modal, mask + Escape owned by PrimeVue). See
// components/Modal.vue. The DOM/ARIA contract that the other specs rely on
// (.modal-overlay mask, .modal panel, .modal-header .btn-icon close,
// .modal-body) is preserved via the #container slot and the mask passthrough;
// here we exercise the PrimeVue-specific guarantees.
//
// The Feature flags modal is the simplest consumer of the base Modal, and is
// reachable via the command palette (Ctrl+K -> "feature").

// modalDialogFeatureFlags says how a description opens the feature flags modal.
const modalDialogFeatureFlags = `To "open the feature flags modal": press Control+k (the command palette shortcut), wait for the command palette input (".command-palette-input") to be visible, fill it with "feature", wait for the first ".command-palette-item" to be visible and click it. `

// modalDialogDirectoryControlHow says how a description reaches the directory control
// on the default phone screen, where it lives in the overflow menu.
const modalDialogDirectoryControlHow = `On this phone-sized screen "the directory control" is in the overflow menu: when ".chat-overflow-popover" is not visible, first click the header's button with aria-label exactly "More options"; the control is then the button in ".chat-overflow-popover" whose accessible name starts with "Directory". `

func TestE2EModalDialogPaletteOpensTrapsFocusAndCloses(t *testing.T) {
	e2e(t, useful(0), `The base modal (a PrimeVue Dialog) opens via the command palette, traps focus, and closes via its X button, Escape and a backdrop click. `+modalDialogFeatureFlags+`Perform the steps in order. 1. Navigate to /c/{{SLUG}} and wait for the message input (data-testid "message-input") to be visible. 2. Open the feature flags modal. 3. The mask (".modal-overlay") and the panel (".modal") should be visible, and ".modal-title" should have exactly the text "Feature flags". 4. The element with role="dialog" should have aria-modal="true" and a non-empty aria-labelledby; the element whose id is that aria-labelledby value must have exactly the text "Feature flags" and the class "modal-title" (PrimeVue's default, never-rendered header id would fail this). 5. Focus moves into the dialog: poll an eval of document.querySelector('.modal')?.contains(document.activeElement) until it is true. 6. The close button ".modal-header .btn-icon[aria-label='Close modal']" should be visible. 7. Click it: then no ".modal" element may exist at all (count 0). 8. Open the feature flags modal again and wait for ".modal" to be visible; press Escape: ".modal" count should become 0. 9. Open the feature flags modal again and wait for ".modal" to be visible; click the mask ".modal-overlay" 5 pixels right and 5 pixels down from its top-left corner (well outside the centered panel): ".modal" count should become 0.`, func(s *shelley) {
		s.v("SLUG", s.conversation("Hello", "").Slug)
	})
}

// On desktop the centered panel leaves wide gaps left/right. The transparent
// p-dialog wrapper spans the mask there, and must not swallow backdrop clicks
// (pointer-events: none in styles.css): PrimeVue's dismissable-mask only
// fires when the click target IS the mask element itself. On the default
// mobile viewport the panel is near-full-width, so this can only regress on
// desktop -- hence the viewport override.
func TestE2EModalDialogBackdropBesidePanelDismisses(t *testing.T) {
	e2e(t, useful(0), `On a 1280x800 screen, the base modal dismisses when the backdrop is clicked beside the panel at the panel's mid-height. 1. Navigate to /c/{{SLUG}} and wait for the message input (data-testid "message-input") to be visible. 2. Press Control+k, wait for ".command-palette-input" to be visible, fill it with "feature" and click the first ".command-palette-item". 3. Wait for the panel (".modal") to be visible. 4. Click the viewport point x = (panel left) / 2, y = panel top + panel height / 2, computed from the panel's bounding box as the click runs: in the empty gap left of the panel, at its vertical middle. 5. ".modal" count should become 0.`, func(s *shelley) {
		s.viewport(1280, 800)
		s.v("SLUG", s.conversation("Hello", "").Slug)
	})
}

func TestE2EModalDialogPaletteHomeDirectory(t *testing.T) {
	e2e(t, useful(0), `The command palette's home-directory action starts a new conversation in $HOME. `+modalDialogDirectoryControlHow+`1. Navigate to /new. 2. window.__SHELLEY_INIT__.home_dir should be exactly "{{HOME}}". 3. Set localStorage "shelley_selected_cwd" to "{{HOME}}/not-home" and reload by navigating to /new again. 4. Press Control+k, wait for ".command-palette-input" to be visible and fill it with "home directory". 5. Click the palette item (".command-palette-item") whose ".command-palette-item-title" has exactly the text "New Conversation in Home Directory" (has_text alone on a broad selector would match an ancestor such as the whole page). 6. The directory control should contain the text "~". 7. localStorage "shelley_selected_cwd" should become exactly "{{HOME}}".`, nil)
}

// DirectoryPickerModal is the most involved consumer of the shared Modal: it
// uses the #footer slot (New Folder / Cancel / Select) and has an inline
// create form whose local Escape must cancel create mode without closing the
// dialog (it stopPropagation()s before the shared modal Escape stack sees it).

func TestE2EModalDialogDirectoryPickerRecent(t *testing.T) {
	e2e(t, useful(0), `The directory picker switches to recent directories and navigates to one. A conversation was started in the directory {{RECENT}}. The picker is ".modal.directory-picker-modal"; its recent toggle is the element with data-testid "recent-toggle" inside it. 1. Navigate to /new and click the directory chip ".status-field-cwd .status-chip". 2. The recent toggle should have aria-pressed="false"; click it. 3. It should have aria-pressed="true". In the picker's data-testid "recent-list", an element whose text is exactly "Other" should be visible. 4. Click the element in that list whose title attribute is exactly "{{RECENT}}". 5. The recent toggle should have aria-pressed="false", and the picker's ".directory-picker-input" should have the value exactly "{{RECENT}}/". 6. Click the recent toggle: aria-pressed="true". 7. Click it again: aria-pressed="false", and the data-testid "recent-list" should be hidden.`, func(s *shelley) {
		recent := s.mkdir(filepath.Join(s.dir, "dirpicker-recent"))
		s.v("RECENT", recent)
		s.conversationWith(map[string]any{"message": "Recent directory picker test", "model": "predictable", "cwd": recent}, "")
	})
}

func TestE2EModalDialogDirectoryPickerHomeRelativeChip(t *testing.T) {
	e2e(t, useful(0), `The new-conversation directory chip shows a home-relative path, and a stale directory listing does not overwrite a newer one. `+modalDialogDirectoryControlHow+`The directory {{HOME}}/exe exists. The picker is ".modal.directory-picker-modal". The server's answer to listing the directory /definitely-missing-shelley-directory is held back: call "requested" says whether that listing request has arrived ("true" or "false"); call "release" lets it answer {"error": "delayed directory error"} and returns once it has. Perform the steps in order. 1. Navigate to /new. 2. window.__SHELLEY_INIT__.home_dir should be exactly "{{HOME}}". 3. Set localStorage "shelley_selected_cwd" to "{{HOME}}/exe" and reload by navigating to /new again. 4. The directory control should contain the text "~/exe". 5. Click the directory control. 6. The picker's ".directory-picker-input" should have the value exactly "{{HOME}}/exe/". 7. Click the picker's button named "Go to home directory". 8. The input should have the value exactly "{{HOME}}/" and ".directory-picker-current-path" exactly the text "{{HOME}}". 9. Fill the input with "/definitely-missing-shelley-directory/". 10. Call "requested" until it returns "true". 11. Click "Go to home directory" again. 12. The input should have the value exactly "{{HOME}}/" and ".directory-picker-current-path" exactly the text "{{HOME}}". 13. Call "release". 14. Wait one animation frame (eval a promise that resolves in requestAnimationFrame). 15. There must be no ".directory-picker-error" element (count 0).`, func(s *shelley) {
		s.mkdir(filepath.Join(s.home, "exe"))
		const delayed = "/definitely-missing-shelley-directory"
		var requested atomic.Bool
		gate := make(chan struct{})
		fulfilled := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		s.route("GET /api/list-directory", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("path") != delayed {
				s.pass.ServeHTTP(w, r)
				return
			}
			requested.Store(true)
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			writeJSON(w, map[string]string{"error": "delayed directory error"})
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			close(fulfilled)
		})
		s.call("requested", func() string { return strconv.FormatBool(requested.Load()) })
		s.call("release", func() string {
			release()
			select {
			case <-fulfilled:
			case <-s.ctx.Done():
				s.fail("released response never sent: %v", s.ctx.Err())
			}
			return "released"
		})
	})
}

func TestE2EModalDialogDirectoryPickerFooterAndNestedEscape(t *testing.T) {
	e2e(t, useful(0), `The directory picker opens from the directory control, pins its footer, and handles nested Escape. `+modalDialogDirectoryControlHow+`The picker is ".modal.directory-picker-modal"; its footer is its ".modal-footer". {{SMALL}} is a small directory named "e2e". 1. Navigate to /new. 2. The directory control should be visible; click it. 3. The picker should be visible, ".modal-title" should have exactly the text "Select Directory", and the element with role="dialog" whose accessible name (the text of the element its aria-labelledby names) is "Select Directory" should have aria-modal="true" (pick it by name: the overflow menu is also a dialog and may still be leaving). 4. Fill the picker's ".directory-picker-input" with "{{SMALL}}/"; ".directory-picker-current-path" should then contain "e2e". 5. The footer's buttons named "Select" and "Cancel" should be visible. 6. Click the footer's "New Folder" button: ".directory-picker-create-input" should be visible. 7. With that create input focused, press Escape: there should be no ".directory-picker-create-input" (count 0), and the picker should still be visible. 8. Press Escape again: the picker count should become 0. 9. Click the directory control again (reopening the overflow menu if needed): the picker should be visible. 10. Click the footer's "Cancel" button: the picker count should become 0.`, func(s *shelley) {
		small := s.mkdir(filepath.Join(s.dir, "e2e"))
		for _, name := range []string{"helpers", "fixtures", "scripts"} {
			s.mkdir(filepath.Join(small, name))
		}
		s.write(filepath.Join(small, "README.md"), "small\n")
		s.v("SMALL", small)
	})
}

// A directory with more subdirs than MAX_VISIBLE_ENTRIES (500). Rendering
// all of them used to freeze the tab for seconds (e.g. /tmp on a busy box);
// the picker must cap the list and show a "type to filter" hint.
func TestE2EModalDialogDirectoryPickerCapsHugeDirectories(t *testing.T) {
	e2e(t, useful(0), `The directory picker caps rendering of huge directories and filters within them. `+modalDialogDirectoryControlHow+`The picker is ".modal.directory-picker-modal". {{BIG}} is a directory holding 520 subdirectories sub-0000 to sub-0519 and one named zz-needle. 1. Navigate to /new and click the directory control. 2. The picker should be visible. 3. Fill its ".directory-picker-input" with "{{BIG}}/". 4. There should be exactly 501 ".directory-picker-entry" elements in the picker (500 rows plus the ".." parent row), and ".directory-picker-truncated" should contain "more — type to filter". 5. Fill the input with "{{BIG}}/zz": there should be exactly 1 ".directory-picker-entry-name" (the ".." row has none), containing "zz-needle". 6. Press Escape: the picker count should become 0.`, func(s *shelley) {
		big := s.mkdir(filepath.Join(s.dir, "dirpicker-big"))
		for i := range 520 {
			s.mkdir(filepath.Join(big, fmt.Sprintf("sub-%04d", i)))
		}
		s.mkdir(filepath.Join(big, "zz-needle"))
		s.v("BIG", big)
	})
}
