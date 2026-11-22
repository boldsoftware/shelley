package test

import "testing"

// Ported from ui/e2e/clear-command.spec.ts.

// /clear starts a fresh generation in the same conversation: it drops the
// prior context, and the UI marks the boundary with a generation divider
// while keeping the older messages on screen.

func TestE2EClearCommandNewGeneration(t *testing.T) {
	e2e(t, useful(0), `/clear clears the context and starts a new generation, keeping the older messages on screen. Perform the steps in order. 1. Navigate to /c/before-clear and wait (up to 30s) for the message input (data-testid "message-input") to be visible. 2. There must be no generation divider yet: ".generation-divider" count 0. 3. Fill the message input with "/clear". 4. The send button (data-testid "send-button") should become enabled (up to 5s); click it. 5. Exactly one generation divider (".generation-divider") should appear (count 1, wait up to 30s). 6. The message input should become empty (its value is exactly "", wait up to 30s; the composer clears only after the send's response arrives, so it may lag the divider). 7. The first message (data-testid "message") containing "before clear" should still be visible.`, func(s *shelley) {
		s.conversation("echo: before clear", "before-clear")
	})
}

func TestE2EClearCommandInMenu(t *testing.T) {
	e2e(t, useful(0), `/clear appears in the slash-command menu. 1. Navigate to /c/menu-seed and wait (up to 30s) for the message input (data-testid "message-input") to be visible. 2. Fill the message input with "/cle". 3. The slash-command menu (data-testid "slash-command-menu") should become visible (up to 5s). 4. Inside that menu, a command name (".slash-command-name") whose trimmed text is exactly "/clear" (equal, not merely containing it) should be visible.`, func(s *shelley) {
		s.conversation("echo: menu seed", "menu-seed")
	})
}
