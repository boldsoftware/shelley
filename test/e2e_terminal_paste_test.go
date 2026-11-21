package test

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Ported from ui/e2e/terminal-paste.spec.ts.

// The server takes websocket messages of at most 32 KiB, and a paste is a
// single onData. A bigger paste used to close the socket and leave the
// terminal hung, whatever was reading it.
func TestE2ETerminalPasteBiggerThanMessage(t *testing.T) {
	e2e(t, `A paste bigger than a websocket message reaches the program. The test seeded a conversation at /c/term-paste, whose directory holds paste.sh, which prints "paste-ready" and then writes the next 600 lines it reads to the file "got". window.__paste is the text to paste: 600 lines, over 32 KiB as a websocket message. Call "got" compares the file "got" with that text; it returns "same" once they are equal. `+termMenu+`
1. Navigate to /c/term-paste. Fill [data-testid="message-input"] with "!bash paste.sh" and click [data-testid="send-button"].
2. Retry the following steps as one block until they pass (timeout 15s): choose the terminal menu item "Insert all output into input" (both clicks inside the block); then the message input's value must contain "paste-ready" (eval with expect true).
3. Paste: in one eval, on the terminal input ('`+visibleTerminal+` textarea[aria-label="Terminal input"]'), make a DataTransfer, setData("text/plain", window.__paste) on it, and dispatch on the textarea a new ClipboardEvent("paste", {clipboardData: that DataTransfer, bubbles: true, cancelable: true}).
4. There should be exactly 1 ".terminal-panel-tab-success".
5. Call "got" should return "same".`, func(s *shelley) {
		var lines []string
		for i := range 600 {
			lines = append(lines, fmt.Sprintf("line %d\t\"quoted\" \\ ünïcødé 😀 %s", i, strings.Repeat("x", 40)))
		}
		paste := strings.Join(lines, "\n") + "\n"
		if n := len([]rune(mustJSON(map[string]any{"type": "input", "data": paste}))); n <= 32<<10 {
			s.fail("the paste is only %d characters as a message", n)
		}
		s.initScript(`window.__paste = ` + mustJSON(paste) + `;`)
		_, dir := s.terminalConversation("terminal-paste", "terminal paste test", "term-paste", map[string]string{
			"paste.sh": "printf 'paste-ready\\n'\nhead -n 600 > got\n",
		})
		s.call("got", func() string {
			got, err := os.ReadFile(dir + "/got")
			if err != nil {
				return err.Error()
			}
			if string(got) != paste {
				return fmt.Sprintf("got %d bytes, want %d", len(got), len(paste))
			}
			return "same"
		})
	})
}
