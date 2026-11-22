package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/terminal-composer.spec.ts.

// terminalCloseOnExit lists the close_on_exit of a conversation's
// terminals, oldest first, as JSON.
func (s *shelley) terminalCloseOnExit(conversation string) string {
	var terminals []struct {
		ConversationID *string `json:"conversation_id"`
		CloseOnExit    bool    `json:"close_on_exit"`
	}
	s.api("GET", "/api/terminals", nil, &terminals)
	policies := []bool{}
	for _, t := range terminals {
		if t.ConversationID != nil && *t.ConversationID == conversation {
			policies = append(policies, t.CloseOnExit)
		}
	}
	b, err := json.Marshal(policies)
	s.check(err)
	return string(b)
}

// termSend is how a description has the composer send a command.
const termSend = `fill [data-testid="message-input"] with it and click [data-testid="send-button"]`

func TestE2ETerminalComposerBareShellCommands(t *testing.T) {
	e2e(t, useful(0), `The composer opens terminals for bare shell commands. The test seeded a conversation at /c/term-bare.
1. Navigate to /c/term-bare. Send "!" (`+termSend+`). There should be exactly 1 ".terminal-panel-tab", and the message input should be empty (value "").
2. Fill the message input with "/shell". Its value should become exactly "!". Click the send button. There should be exactly 2 ".terminal-panel-tab", and the message input should be empty.`, func(s *shelley) {
		s.conversation("terminal composer test", "term-bare")
	})
}

func TestE2ETerminalComposerExitClosesOnlyItsTab(t *testing.T) {
	e2e(t, useful(0), `Shell exit closes only its own tab, keeping command output. The test seeded a conversation at /c/term-exit. `+termInput+termMenu+`
1. Navigate to /c/term-exit. Send "!printf preserved-output" (`+termSend+`). There should be exactly 1 ".terminal-panel-tab-success".
2. Send "!". There should be exactly 1 ".terminal-panel-tab-running".
3. Type "exit 0" into the terminal input and press Enter.
4. There should be exactly 1 ".terminal-panel-tab", and ".terminal-panel-tab-active" should have the title attribute exactly "printf preserved-output".
5. Choose the terminal menu item "Insert all output into input". The message input's value should contain "preserved-output".`, func(s *shelley) {
		s.conversation("terminal exit test", "term-exit")
	})
}

func TestE2ETerminalComposerNonzeroExitCloses(t *testing.T) {
	e2e(t, useful(0), `The composer closes direct shell launches on nonzero exits. The test seeded a conversation at /c/term-exit7. `+termInput+`
1. Navigate to /c/term-exit7. Send "!bash" (`+termSend+`). There should be exactly 1 ".terminal-panel-tab-running".
2. Type "exit 7" into the terminal input and press Enter.
3. There should be no ".terminal-panel-tab" (count 0), and ".terminal-panel" should be hidden.`, func(s *shelley) {
		s.conversation("terminal failed exit test", "term-exit7")
	})
}

func TestE2ETerminalComposerExitPolicyAfterReload(t *testing.T) {
	e2e(t, useful(0), `The composer restores each terminal's explicit exit policy after reload. The test seeded a conversation at /c/term-policy. "The held command" is: bash -c 'read -r answer; echo "$answer"'. `+termInput+termMenu+`
1. Navigate to /c/term-policy. Send "!" followed by the held command (`+termSend+`), then send "!bash". There should be exactly 2 ".terminal-panel-tab-running".
2. Call "closeOnExit" (the close_on_exit of this conversation's terminals on the server, oldest first) should return exactly [false,true].
3. Reload. There should be exactly 2 ".terminal-panel-tab-running" again.
4. Type "exit 7" into the terminal input (the "!bash" terminal, which is the visible one) and press Enter.
5. There should be exactly 1 ".terminal-panel-tab", and ".terminal-panel-tab-active" should have the title attribute exactly the held command.
6. A shell running a one-shot command retains output even though its executable is the same as the interactive shell that just closed: type "retained-after-reload" into the terminal input (now the held command's) and press Enter. There should be exactly 1 ".terminal-panel-tab-success".
7. Choose the terminal menu item "Insert all output into input". The message input's value should contain "retained-after-reload".`, func(s *shelley) {
		c := s.conversation("terminal exit policy test", "term-policy")
		s.call("closeOnExit", func() string { return s.terminalCloseOnExit(c.ID) })
	})
}

func TestE2ETerminalComposerOneShotBashOutput(t *testing.T) {
	e2e(t, useful(0), `The composer keeps one-shot bash output visible. The test seeded a conversation at /c/term-oneshot. `+termMenu+`
1. Navigate to /c/term-oneshot. Send "!bash -c 'echo one-shot-output'" (`+termSend+`). There should be exactly 1 ".terminal-panel-tab-success".
2. Choose the terminal menu item "Insert all output into input". The message input's value should contain "one-shot-output".`, func(s *shelley) {
		s.conversation("terminal one-shot shell test", "term-oneshot")
	})
}

func TestE2ETerminalComposerScriptOutput(t *testing.T) {
	e2e(t, useful(0), `The composer keeps bash script output visible. The test seeded a conversation at /c/term-script, whose directory holds script.sh, which echoes "script-output". `+termMenu+`
1. Navigate to /c/term-script. Send "!bash script.sh" (`+termSend+`). There should be exactly 1 ".terminal-panel-tab-success".
2. Choose the terminal menu item "Insert all output into input". The message input's value should contain "script-output".`, func(s *shelley) {
		s.terminalConversation("terminal-script", "terminal shell script test", "term-script", map[string]string{"script.sh": "echo script-output\n"})
	})
}

func TestE2ETerminalComposerFailedCommandOutput(t *testing.T) {
	e2e(t, useful(0), `The composer keeps failed command output visible. The test seeded a conversation at /c/term-false. `+termMenu+`
1. Navigate to /c/term-false. Send "!false" (`+termSend+`). There should be exactly 1 ".terminal-panel-tab-error".
2. Choose the terminal menu item "Insert all output into input". The message input's value should match the regular expression /completed with exit\s+code 1/ (eval with expect true).`, func(s *shelley) {
		s.conversation("terminal command failure test", "term-false")
	})
}

func TestE2ETerminalComposerDisconnectIsNotExit(t *testing.T) {
	e2e(t, useful(0), `The terminal does not treat a disconnected socket as a shell exit. The test seeded a conversation at /c/term-disconnect. Call "dropSocket" drops the network connection of the browser's terminal websocket for this conversation (the one opened with its conversation_id), as a lost network would; it returns "dropped", or "no socket" if there was none. `+termInput+`
1. Navigate to /c/term-disconnect. Send "!" (`+termSend+`). There should be exactly 1 ".terminal-panel-tab-running".
2. Call "dropSocket"; it should return "dropped".
3. There should be exactly 1 ".terminal-panel-tab-error".
4. Reload. There should be exactly 1 ".terminal-panel-tab-running" (the shell still runs).
5. Type "exit" into the terminal input and press Enter. There should be no ".terminal-panel-tab" (count 0).`, func(s *shelley) {
		c := s.conversation("terminal disconnect test", "term-disconnect")
		drop := s.droppableSockets(func(r *http.Request) bool { return r.URL.Query().Get("conversation_id") == c.ID })
		s.call("dropSocket", func() string {
			if drop() {
				return "dropped"
			}
			return "no socket"
		})
	})
}

func TestE2ETerminalComposerLaunchErrorVisible(t *testing.T) {
	// Exercise a real server launch failure without changing the
	// conversation's working directory or breaking the agent fixture. A
	// missing cwd falls back to the default, so ask for a terminal session
	// that doesn't exist instead.
	e2e(t, useful(0), `The terminal keeps launch errors visible. The test seeded a conversation at /c/term-launch-error; the page's terminal websockets that launch a command ask the server for a terminal session that doesn't exist, so the launch fails. `+termMenu+`
1. Navigate to /c/term-launch-error. Send "!" (`+termSend+`). There should be exactly 1 ".terminal-panel-tab-error".
2. Choose the terminal menu item "Insert all output into input". The message input's value should contain "Error:".`, func(s *shelley) {
		s.conversation("terminal launch failure test", "term-launch-error")
		s.initScript(`const NativeWebSocket = window.WebSocket;
window.WebSocket = class extends NativeWebSocket {
  constructor(url, protocols) {
    const address = new URL(url, window.location.href);
    if (address.pathname === "/api/exec-ws" && address.searchParams.has("cmd")) {
      address.searchParams.set("term_id", "nonexistent-shelley-terminal-test-session");
    }
    super(address, protocols);
  }
};`)
	})
}

// terminalCount is how many terminals the server has for a conversation.
func (s *shelley) terminalCount(conversation string) string {
	var terminals []struct {
		ConversationID *string `json:"conversation_id"`
	}
	s.api("GET", "/api/terminals", nil, &terminals)
	n := 0
	for _, t := range terminals {
		if t.ConversationID != nil && *t.ConversationID == conversation {
			n++
		}
	}
	return fmt.Sprint(n)
}

// eofClosesTab is the test of command's terminal tab closing after EOF.
func eofClosesTab(t *testing.T, useful lazycue.Useful, command, slug string) {
	e2e(t, useful, command+` closes its terminal tab after EOF. The test seeded a conversation at /c/`+slug+`. Call "terminals" says how many terminals the server still has for this conversation. The test's init script sets window.__terminalsHydrated, a promise that resolves to "hydrated" once the page has fetched GET /api/terminals (which restores the server's terminals as tabs) and has had a turn to render the result. `+termInput+termMenu+`
1. Navigate to /c/`+slug+`. Fill [data-testid="message-input"] with "`+command+`" and click [data-testid="send-button"]. There should be exactly 1 ".terminal-panel-tab-running".
2. Readline handles EOF at an empty prompt. Synchronize with the shell by waiting for a marker in real terminal output, not just the open socket: type into the terminal input, key by key, the 19 characters printf 'shell-ready\n' (a backslash and an n, not a newline), then press Enter.
3. Retry the following steps as one block until they pass (timeout 15s): choose the terminal menu item "Insert all output into input" (both clicks inside the block); then the message input's value must match /shell-ready\r?\n/ (eval /shell-ready\r?\n/.test(document.querySelector('[data-testid="message-input"]').value) with expect true).
4. The insert focuses the message input in a zero-delay timeout: let that timeout run first (eval, awaited: new Promise((resolve) => setTimeout(() => resolve("flushed"), 0)), expect "flushed"), then focus the terminal input again (eval .focus()) and press Control+d.
5. There should be no ".terminal-panel-tab" (count 0), and ".terminal-panel" should be hidden.
6. Call "terminals" should return "0".
7. Reload. Wait until the page has restored the server's terminals (eval, awaited: window.__terminalsHydrated, expect "hydrated"). There should be no ".terminal-panel-tab" (count 0).`, func(s *shelley) {
		c := s.conversation("terminal EOF test", slug)
		s.initScript(terminalsHydrated)
		s.call("terminals", func() string { return s.terminalCount(c.ID) })
	})
}

// terminalsHydrated resolves window.__terminalsHydrated once the app's
// startup GET /api/terminals has been answered and rendered, so a check that
// no tab came back after a reload cannot pass before the tabs are restored.
const terminalsHydrated = `window.__terminalsHydrated = new Promise((resolve) => {
  const fetch = window.fetch;
  window.fetch = function (input, init) {
    const response = fetch.apply(this, arguments);
    const url = new URL(typeof input === "string" ? input : input.url, location.href);
    if (url.pathname === "/api/terminals" && (!init || !init.method || init.method === "GET")) {
      // Read a copy of the body alongside the app, then let the app's
      // promise callbacks and Vue's render run before resolving.
      response
        .then((r) => r.clone().text())
        .catch(() => {})
        .then(() => setTimeout(() => setTimeout(() => resolve("hydrated"), 0), 0));
    }
    return response;
  };
});`

func TestE2ETerminalComposerEOFClosesBang(t *testing.T) {
	eofClosesTab(t, useful(0), "!", "term-eof-bang")
}

func TestE2ETerminalComposerEOFClosesShell(t *testing.T) {
	eofClosesTab(t, useful(0), "/shell", "term-eof-shell")
}

func TestE2ETerminalComposerEOFClosesBash(t *testing.T) {
	eofClosesTab(t, useful(0), "!bash", "term-eof-bash")
}

func TestE2ETerminalComposerEOFClosesInteractiveBash(t *testing.T) {
	eofClosesTab(t, useful(0), "!/bin/bash -i", "term-eof-bash-i")
}
