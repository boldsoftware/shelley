package test

import "testing"

// Ported from ui/e2e/ansi-rendering.spec.ts.

// ansiOutput tells the agent how to open a bash card and find its output.
const ansiOutput = `"The output" is the last ".bash-tool-code" inside that card's ".bash-tool-details". `

func TestE2EAnsiRenderingColors(t *testing.T) {
	// The spec also saved a full-page screenshot for visual review; it
	// asserted nothing.
	e2e(t, `Bash output with ANSI colors renders styled text, not raw escapes. The test seeded a conversation at /c/ansi-colors whose bash tool call ran printf with green, red, bold and yellow escape sequences around the words "Green", "Red", "Bold" and "Yellow", then " plain". `+ansiOutput+`
1. Navigate to /c/ansi-colors. The first '.bash-tool[data-testid="tool-call-completed"]' should become visible (wait up to 15s). The bash card renders inline, collapsed: click that card's ".bash-tool-header", and its ".bash-tool-details" should become visible.
2. The output should be visible.
3. The output's textContent must not contain "[0m", "[32m", "[31m" or the escape character (U+001B, '\u001b' in JS); it must contain each of "Green", "Red", "Bold", "Yellow" and "plain".
4. The output's innerHTML must contain "<span", "style=" and "color" (AnsiText renders the colors as spans with inline styles).
5. The span inside the output whose text contains "Green" should be visible and its style attribute must contain "color"; the same for the span containing "Red".
6. The span inside the output whose text contains "Bold" should be visible and its style attribute must contain "font-weight:bold".`, func(s *shelley) {
		s.conversation(`bash: printf '\033[32mGreen\033[0m \033[31mRed\033[0m \033[1mBold\033[0m \033[33mYellow\033[0m plain'`, "ansi-colors")
	})
}

func TestE2EAnsiRenderingPlainText(t *testing.T) {
	e2e(t, `Bash output without ANSI codes renders as plain text. The test seeded a conversation at /c/ansi-plain whose bash tool call ran: echo "just plain text with no escapes". "The card" is the first ".bash-tool" whose text contains "just plain text with no escapes". `+ansiOutput+`
1. Navigate to /c/ansi-plain. The card should become visible (wait up to 15s). Click its ".bash-tool-header"; its ".bash-tool-details" should become visible.
2. The output should be visible, and its textContent must contain "just plain text with no escapes".
3. Plain text renders as a text node, not HTML: the output's innerHTML must not contain "<span".`, func(s *shelley) {
		s.conversation(`bash: echo "just plain text with no escapes"`, "ansi-plain")
	})
}

// Regression: the old ansi-to-html library only understood SGR color codes;
// its catch-all token consumed the introducer + params but left the trailing
// final byte of cursor-movement sequences. \x1b[1G (Cursor Horizontal
// Absolute) is emitted by logging libraries to redraw/align status lines, and
// produced "GGGDev code has changes" with three of them. Shelley pre-strips
// non-SGR sequences (and now uses ansi_up, which parses complete CSI
// sequences), so only the readable text remains.
func TestE2EAnsiRenderingCursorMovement(t *testing.T) {
	e2e(t, `Bash output with cursor-movement ANSI sequences renders no stray letters. The test seeded a conversation at /c/ansi-cursor whose bash tool call ran printf with three cursor-horizontal-absolute sequences (ESC [1G) followed by "Dev code has changes not yet deployed". "The card" is the first ".bash-tool" whose text contains "Dev code has changes". `+ansiOutput+`
1. Navigate to /c/ansi-cursor. The card should become visible (wait up to 15s). Click its ".bash-tool-header"; its ".bash-tool-details" should become visible.
2. The output should be visible, and its textContent must contain "Dev code has changes not yet deployed".
3. No stray escape letters or raw fragments leak through: the output's textContent must not contain "GGG", "[1G" or the escape character (U+001B, '\u001b' in JS).
4. Cursor-only sequences are not colors: the output's innerHTML must not contain "<span".`, func(s *shelley) {
		s.conversation(`bash: printf '\033[1G\033[1G\033[1GDev code has changes not yet deployed'`, "ansi-cursor")
	})
}
