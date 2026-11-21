package test

import "testing"

// Ported from ui/e2e/bash-syntax-highlighting.spec.ts.

func TestE2EBashSyntaxHighlighting(t *testing.T) {
	e2e(t, `Bash commands are Shiki-tokenized without changing output rendering. The test seeded a conversation at /c/bash-syntax whose bash tool call ran the command "if true; then echo bash-output-plain; fi" (call it "the command"). "The card" is the first ".bash-tool" whose text contains the command, and "the summary" is the card's ".bash-tool-command".
1. Navigate to /c/bash-syntax. The summary's text should become exactly the command (wait up to 30s).
2. The summary must have no title attribute (no native tooltip: the browser renders it in a system font, and expanding the card already shows the full command).
3. The summary must contain at least one ".shelley-code-token" element (attached; it need not be visible).
4. Record, in window variables: the number of ".shelley-code-token" elements in the summary, getComputedStyle(first such token).color, and the summary's innerHTML.
5. Add the class "dark" to the html element. The first token's computed color should become different from the recorded one.
6. Then the summary must still have exactly the recorded number of tokens, and its innerHTML must be exactly the recorded innerHTML (the theme changes colors through CSS, not by re-rendering).
7. Click the card's ".bash-tool-header"; its ".bash-tool-details" should become visible.
8. The ".bash-tool-code" inside the details whose text contains the command should have exactly the command as its text, and contain at least one ".shelley-code-token".
9. The last ".bash-tool-code" inside the details is the output: its text should be exactly "bash-output-plain", it must contain no ".shelley-code-token" elements (count 0), and its innerHTML must not contain "shelley-code-token".`, func(s *shelley) {
		s.conversation("bash: if true; then echo bash-output-plain; fi", "bash-syntax")
	})
}
