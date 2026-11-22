package test

import "testing"

// Ported from ui/e2e/mobile-command-palette.spec.ts.

// Issue #260: mobile users have no keyboard shortcut for the Cmd/Ctrl+K
// command palette, so the overflow exposes a "Command menu" button. The
// test runs on the default Pixel 5, matching the mobile use case.
//
// The Playwright test checked for no visible shortcut inside the visible
// "Command menu" button, but on mobile that is the toolbar's icon button,
// which has none; the menu item with the Ctrl+K hint is hidden. This checks
// that the hint exists and is hidden.
func TestE2EMobileCommandPaletteFromOverflow(t *testing.T) {
	e2e(t, useful(0), `Command palette from overflow: the overflow button opens the command palette. To "tap" an element means a touch step with value "start" on it followed by a touch step with value "end" (a finger tap on the touch screen, not a click). Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The palette starts closed: there must be exactly 0 ".command-palette-input" elements. 3. Tap the button whose accessible name is exactly "More options". 4. In the overflow menu (".chat-overflow-popover"), the button whose accessible name starts with "Command menu" should be visible. 5. The keyboard shortcut hint is there but hidden: the overflow menu has exactly 1 ".overflow-menu-shortcut" whose text contains "Ctrl+K", and it must not be visible. 6. Tap that "Command menu" button. 7. The overflow menu ".chat-overflow-popover" should become hidden. 8. The palette's search input ".command-palette-input" should be visible. 9. Fill it with "notification". 10. The first ".command-palette-item" should be visible. 11. Its ".command-palette-item-title" must have the computed CSS font-size exactly "16px" (eval getComputedStyle(...).fontSize, expecting "16px"). 12. Press Escape. 13. There must then be exactly 0 ".command-palette-input" elements.`, func(s *shelley) {
		s.v("SLUG", s.conversation("Hello", "").Slug)
	})
}
