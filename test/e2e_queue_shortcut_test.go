package test

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/queue-shortcut.spec.ts.

// Alt+Enter (⌥↵ on mac) queues the composed message to run after the current
// turn instead of interrupting it, and falls back to an ordinary send when the
// agent is idle. The browser reports a Linux platform, so the mod is Ctrl.

const queueShortcutSetup = `"The input" is the message input [data-testid="message-input"]; "the send button" is [data-testid="send-button"]; "queued badges" are [data-testid="queued-badge"]. "Send X and wait for work" means: fill the input with X, click the send button, and wait (up to 30s) for the thinking indicator [data-testid="agent-thinking"] to be visible. Keys pressed "in the input" go to the input while it has focus (filling it focuses it). "Press M+Enter" presses its modifiers as a keyboard does: a key_down step for each modifier M in the order written, then a press_key step "Enter" (without modifiers of its own; the held keys apply), then a key_up step for each modifier in reverse order. Every test starts: navigate to /c/queue-shortcut and wait (up to 30s) for the input to be visible. `

func queueShortcut(t *testing.T, useful lazycue.Useful, description string, setup func(s *shelley)) {
	e2e(t, useful, queueShortcutSetup+description, func(s *shelley) {
		s.device(pixel5Desktop(1280, 800))
		s.initScript(queueTextCountJS)
		s.conversation("echo: queue shortcut seed", "queue-shortcut")
		if setup != nil {
			setup(s)
		}
	})
}

func TestE2EQueueShortcutAltEnterQueues(t *testing.T) {
	queueShortcut(t, useful(0), queueTextCountSetup+`Alt+Enter queues the composed message while the agent works. 1. Navigate to /c/queue-shortcut and wait for the input. 2. Send "delay: 60" and wait for work. 3. Fill the input with "queued via shortcut" and press Alt+Enter in it. 4. The input's value should become exactly "". 5. There should be exactly one queued badge (up to 10s). 6. Eval window.textCount("queued via shortcut") with expect "1".`, nil)
}

func TestE2EQueueShortcutHoldingAltPreviews(t *testing.T) {
	queueShortcut(t, useful(0), `Holding Alt previews queueing on the send button. 1. Navigate to /c/queue-shortcut and wait for the input. 2. Send "delay: 60" and wait for work. 3. Fill the input with "hold to preview". 4. Hold Alt down (a key_down step "Alt"). The send button's aria-label should become exactly "Queue after agent finishes". 5. Release Alt (a key_up step "Alt"). The send button's aria-label should become exactly "Send message".`, nil)
}

func queueShortcutNewline(t *testing.T, useful lazycue.Useful, shortcut string) {
	queueShortcut(t, useful, shortcut+` still inserts a newline while the agent works. 1. Navigate to /c/queue-shortcut and wait for the input. 2. Send "delay: 60" and wait for work. 3. Fill the input with "first line" and press `+shortcut+` in it. 4. Type "second line" key by key (a type step, not fill). 5. The input's value should be exactly "first line\nsecond line" (with a newline; check JSON.stringify of the value). 6. There must be no queued badge (count 0).`, nil)
}

func TestE2EQueueShortcutShiftEnterNewline(t *testing.T) {
	queueShortcutNewline(t, useful(0), "Shift+Enter")
}

func TestE2EQueueShortcutAltShiftEnterNewline(t *testing.T) {
	queueShortcutNewline(t, useful(0), "Alt+Shift+Enter")
}

func TestE2EQueueShortcutAltClickQueues(t *testing.T) {
	queueShortcut(t, useful(0), queueTextCountSetup+`Alt+click on Send queues the composed message. 1. Navigate to /c/queue-shortcut and wait for the input. 2. Send "delay: 60" and wait for work. 3. Fill the input with "queued via click". 4. Alt+click the send button as a user holding Alt would: a key_down step "Alt", a click on the send button with modifiers "Alt", then a key_up step "Alt". 5. The input's value should become exactly "". 6. There should be exactly one queued badge. 7. Eval window.textCount("queued via click", true) with expect "1".`, nil)
}

func TestE2EQueueShortcutCtrlAltDoesNotQueue(t *testing.T) {
	queueShortcut(t, useful(0), queueTextCountSetup+`Ctrl+Alt neither arms nor invokes queueing. 1. Navigate to /c/queue-shortcut and wait for the input. 2. Send "delay: 60" and wait for work. 3. Fill the input with "sent with control". 4. Hold Control, then Alt (key_down "Control", then key_down "Alt"). The send button's aria-label should be exactly "Send message". 5. Press "Enter" while both are held (a press_key step "Enter" without modifiers of its own; the held keys apply). 6. Release Alt, then Control (key_up "Alt", then key_up "Control"). 7. The input's value should become exactly "". 8. There must be no queued badge (count 0). 9. Eval window.textCount("sent with control", true) with expect "1".`, nil)
}

func TestE2EQueueShortcutAltEnterPendingUpload(t *testing.T) {
	queueShortcut(t, useful(0), `Alt+Enter keeps a pending upload in the composer and queues it once it is ready. Uploads (/api/upload) are held back until call "release upload", and then answered with the path "/tmp/queue-shortcut.txt". window.setInputFiles(selector, files) chooses files in a file input, as a user picking them would: call it in an eval, e.g. window.setInputFiles('input[type="file"]', [["a.txt", "content"]]). Call "queued chat" describes the first /chat request whose JSON body has queue true: "none" before there is one, else whether its message mentions the uploaded file's path ("with attachment" or "without attachment"). 1. Navigate to /c/queue-shortcut and wait for the input. 2. Send "delay: 60" and wait for work, then for the input's value to be exactly "" (the send has gone through: files chosen while it is in flight are cleared with it). 3. Eval window.setInputFiles('input[type="file"]', [["queue-shortcut.txt", "queued attachment"]]). 4. There should be exactly one ".message-attachment-uploading". 5. Fill the input with "keep my attachment" and press Alt+Enter in it. 6. The input's value should still be exactly "keep my attachment", there should still be exactly one ".message-attachment-uploading", and there must be no queued badge (count 0). 7. Call "release upload". 8. There should be exactly one ".message-attachment-ready". 9. Call "queued chat"; it must be "none". 10. Press Alt+Enter in the input again. 11. Call "queued chat"; it should become exactly "with attachment". 12. The input's value should become exactly "", there should be no ".message-attachment" at all (count 0), and there should be exactly one queued badge.`, func(s *shelley) {
		s.initScript(inputFilesJS)
		release := s.gate("release upload")
		s.route("/api/upload", func(w http.ResponseWriter, r *http.Request) {
			if !release.wait(r) {
				return
			}
			writeJSON(w, map[string]string{"path": "/tmp/queue-shortcut.txt"})
		})
		var mu sync.Mutex
		queued := "none"
		s.call("queued chat", func() string {
			mu.Lock()
			defer mu.Unlock()
			return queued
		})
		s.route("POST /api/conversation/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
			body := object(readBody(r))
			if body["queue"] == true {
				mu.Lock()
				if queued == "none" {
					queued = "without attachment"
					if message, _ := body["message"].(string); strings.Contains(message, "/tmp/queue-shortcut.txt") {
						queued = "with attachment"
					}
				}
				mu.Unlock()
			}
			s.pass.ServeHTTP(w, r)
		})
	})
}

func TestE2EQueueShortcutAltEnterIdleSends(t *testing.T) {
	queueShortcut(t, useful(0), queueTextCountSetup+`Alt+Enter sends at once when the agent is idle. 1. Navigate to /c/queue-shortcut and wait for the input. 2. Fill the input with "sent via shortcut" and press Alt+Enter in it. 3. The input's value should become exactly "". 4. Eval window.textCount("sent via shortcut") with expect "1". 5. There must be no queued badge (count 0).`, nil)
}
