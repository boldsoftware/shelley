package test

import "testing"

// Ported from ui/e2e/config-field-input.spec.ts.

// ConfigFieldInput.vue renders the per-channel-type config fields inside the
// Notifications "Add channel" form, as PrimeVue Select and InputText. The
// ntfy channel type is the richest consumer: text fields, password fields,
// and two Selects (Done/Error Priority).

func TestE2EConfigFieldInputNtfyForm(t *testing.T) {
	e2e(t, useful(0), `The ntfy add-channel form uses PrimeVue InputText and Select, and they update the form. The screen is the default 393x727 phone. Name elements by ARIA role and accessible name, as Playwright's getByRole does. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. Press Control+k to open the command palette; ".command-palette-input" should be visible. Fill it with "notification". The first ".command-palette-item" should be visible; click it. 3. Click the button whose name contains "+ Add Channel". 4. Click the ".provider-btn" whose text is exactly "ntfy". 5. "#config-topic" should have the class "p-inputtext". Fill it with "my-topic"; its value should be exactly "my-topic". 6. "#config-password" should have the attribute type="password" and the attribute aria-describedby exactly "config-password-desc", and "#config-password-desc" should be visible. 7. "The priority select" is the ".p-select" inside the ".form-group" containing the text "Done Priority". It should be visible, and its ".p-select-label" should have exactly the text "default". 8. Click the priority select. ".p-select-overlay" should be visible, and the texts of its ".p-select-option" elements should be exactly, in order: "min", "low", "default", "high", "max". 9. Click the overlay's ".p-select-option" whose text is exactly "max". The priority select's ".p-select-label" should then have exactly the text "max".`, func(s *shelley) {
		s.v("SLUG", s.conversation("Hello", "").Slug)
	})
}
