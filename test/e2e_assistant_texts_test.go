package test

import (
	"testing"
)

// Ported from ui/e2e/assistant-texts.spec.ts.

// In workspace mode, texted messages go to the Assistant, marked "via
// text".
func TestE2EAssistantTextsMarkedViaText(t *testing.T) {
	e2e(t, useful(0), `A message texted to the Assistant is marked "via text". Workspace mode is on. `+workspaceLoad+`Call "text" texts the message "texted to the Assistant 4f1c" to Shelley through a debug channel, as a phone would. 1. Navigate to /. 2. The header title (h1.header-title) should read exactly "Assistant". 3. Call "text"; it returns "texted". 4. In the element with data-testid "message" whose text contains "texted to the Assistant 4f1c" (within, has_text), the element with data-testid "message-via-text" should read exactly "via text".`, func(s *shelley) {
		s.flag("workspace", true)
		s.call("text", func() string {
			s.shelleyAPI("POST", "/api/debug/channels/debug-assistant-texts/receive", map[string]string{"text": "texted to the Assistant 4f1c"}, nil)
			return "texted"
		})
	})
}
