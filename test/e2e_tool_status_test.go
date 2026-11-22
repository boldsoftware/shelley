package test

import (
	"strconv"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/tool-status.spec.ts.

// toolStatusBashExit checks that a bash card shows the command's exit code
// only in its expanded output header. Printing a fake status must not affect
// the real exit code in the header.
func toolStatusBashExit(t *testing.T, useful lazycue.Useful, code int) {
	n := strconv.Itoa(code)
	e2e(t, useful, `Bash exit `+n+` is shown only in the expanded output header. The conversation at /c/{{SLUG}} ran one bash command that printed the fake status line "[command failed: exit status 99]" and then "kept output", and exited with code `+n+`. "The card" is the first element matching .bash-tool[data-testid="tool-call-completed"]; "the header" is the card's .bash-tool-header; "the label" is the card's .bash-tool-label element whose text contains "Output". Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait for the card to be visible (allow 30s).
2. The header contains no .tool-status-icon element (count 0), and the header's text contains neither "✓" nor "✗".
3. The card contains no .bash-tool-details element (count 0).
4. Click the header.
5. The label's text contains "Output (exit code `+n+`):".
6. The card's .bash-tool-details text contains "kept output".
7. The card's .bash-tool-time element is visible.
8. Click the header again. The card then contains no .bash-tool-details element (count 0).
9. Reload the page and wait for the card to be visible again (allow 30s).
10. The header contains no .tool-status-icon element (count 0).
11. Click the header. The label's text contains "Output (exit code `+n+`):".`, func(s *shelley) {
		s.v("SLUG", s.conversation(`bash: printf '[command failed: exit status 99]\nkept output\n'; exit `+n, "").Slug)
	})
}

func TestE2EToolStatusBashExit0(t *testing.T) { toolStatusBashExit(t, useful(0), 0) }

func TestE2EToolStatusBashExit7(t *testing.T) { toolStatusBashExit(t, useful(0), 7) }

// The job wrapper records a signal death as the shell does: 128 + signal.
func TestE2EToolStatusSignalFailure(t *testing.T) {
	e2e(t, useful(0), `A signal failure reports the shell exit status. The conversation at /c/{{SLUG}} ran one bash command that killed its own shell with SIGTERM. "The card" is the first element matching .bash-tool[data-testid="tool-call-completed"]; "the header" is the card's .bash-tool-header. Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait for the card to be visible (allow 30s).
2. The header contains no .tool-status-icon element (count 0).
3. Click the header.
4. The card's .bash-tool-label element whose text contains "Output" has text containing "Output (exit code 143):".
5. The card's .bash-tool-details text contains "exit status 143".`, func(s *shelley) {
		s.v("SLUG", s.conversation("bash: kill -TERM $$", "").Slug)
	})
}

func TestE2EToolStatusNonBashKeepsErrorDetails(t *testing.T) {
	e2e(t, useful(0), `Non-bash tool cards keep their error details without header status marks. The conversation at /c/{{SLUG}} ran one change_dir tool call to a directory that does not exist. "The card" is the first element matching .tool[data-testid="tool-call-completed"]; "the header" is the card's .tool-header. Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait for the card to be visible (allow 30s).
2. The header contains no .tool-status-icon element (count 0), and the header's text contains neither "✓" nor "✗".
3. Click the header.
4. The card's .tool-details text contains "Result (Error):".
5. The card's .tool-details text contains "directory does not exist".`, func(s *shelley) {
		s.v("SLUG", s.conversation("change_dir: /nonexistent-tool-status-dir", "").Slug)
	})
}
