package test

import "testing"

// Ported from ui/e2e/exe-brand.spec.ts.

// Workspace mode's drawer leads with exe's logo; stock Shelley's
// drawer doesn't have it.
func TestE2EExeBrandWorkspaceOnly(t *testing.T) {
	e2e(t, `Workspace mode shows the exe logo atop the drawer, stock Shelley doesn't. The server starts in workspace mode (the "workspace" feature flag is on); call "workspace off" turns the flag off. On a fresh server the first load of a page in workspace mode takes a few seconds while the Assistant is created: wait for elements rather than reading the page right after navigating. Perform the steps in order. 1. Navigate to /. 2. The element ".drawer-header [data-testid='exe-brand']" (the brand) should be visible. 3. The brand's whole text should be exactly "exe". 4. The img inside the brand should load: an eval of document.querySelector(".drawer-header [data-testid='exe-brand'] img").naturalWidth > 0 should become true. 5. The img's rendered width (getBoundingClientRect().width) must be greater than 16: eval that comparison, expecting true. 6. Call "workspace off". 7. Navigate to / again. 8. ".drawer-header" should be visible. 9. There must be exactly 0 elements with data-testid "exe-brand" anywhere on the page.`, func(s *shelley) {
		s.viewport(1280, 800)
		s.flag("workspace", true)
		s.call("workspace off", func() string {
			s.do("DELETE", "/feature-flags", map[string]string{"name": "workspace"})
			return "off"
		})
	})
}
