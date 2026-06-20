package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestExecTerminal_SimpleCommand(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	// Convert http to ws URL
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/exec-ws?cmd=echo+hello"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	// Send init message
	initMsg := ExecMessage{Type: "init", Cols: 80, Rows: 24}
	if err := wsjson.Write(ctx, conn, initMsg); err != nil {
		t.Fatalf("Failed to write init message: %v", err)
	}

	// Read messages until connection closes (server closes after sending exit)
	var output strings.Builder
	var exitCode int = -1

	for {
		var msg ExecMessage
		err := wsjson.Read(ctx, conn, &msg)
		if err != nil {
			// Connection closed - this is expected after exit message
			break
		}

		switch msg.Type {
		case "output":
			data, err := base64.StdEncoding.DecodeString(msg.Data)
			if err == nil {
				output.Write(data)
			}
		case "exit":
			if msg.Data == "0" {
				exitCode = 0
			} else {
				exitCode = 1
			}
			// Don't break here - continue reading until connection is closed
			// to ensure we've received all output
		case "error":
			t.Fatalf("Received error: %s", msg.Data)
		}
	}

	if exitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", exitCode)
	}

	if !strings.Contains(output.String(), "hello") {
		t.Errorf("Expected output to contain 'hello', got: %q", output.String())
	}
}

func TestExecTerminal_FailingCommand(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/exec-ws?cmd=exit+42"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	// Send init message
	initMsg := ExecMessage{Type: "init", Cols: 80, Rows: 24}
	if err := wsjson.Write(ctx, conn, initMsg); err != nil {
		t.Fatalf("Failed to write init message: %v", err)
	}

	// Read messages until we get exit
	var exitCode string

	for {
		var msg ExecMessage
		err := wsjson.Read(ctx, conn, &msg)
		if err != nil {
			break
		}

		if msg.Type == "exit" {
			exitCode = msg.Data
		}
	}

	if exitCode != "42" {
		t.Errorf("Expected exit code 42, got %q", exitCode)
	}
}

func TestExecTerminal_MissingCmd(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	// Try without cmd parameter
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/exec-ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		t.Fatal("Expected error for missing cmd parameter")
	}

	if resp != nil && resp.StatusCode != 400 {
		t.Errorf("Expected status 400, got %d", resp.StatusCode)
	}
}

func TestExecTerminal_WorkingDirectory(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/exec-ws?cmd=pwd&cwd=/tmp"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	// Send init message
	initMsg := ExecMessage{Type: "init", Cols: 80, Rows: 24}
	if err := wsjson.Write(ctx, conn, initMsg); err != nil {
		t.Fatalf("Failed to write init message: %v", err)
	}

	// Read messages
	var output strings.Builder

	for {
		var msg ExecMessage
		err := wsjson.Read(ctx, conn, &msg)
		if err != nil {
			break
		}

		if msg.Type == "output" {
			data, _ := base64.StdEncoding.DecodeString(msg.Data)
			output.Write(data)
		}
	}

	if !strings.Contains(output.String(), "/tmp") {
		t.Errorf("Expected output to contain '/tmp', got: %q", output.String())
	}
}

func TestExecTerminal_Input(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	// Use cat which echoes input
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/exec-ws?cmd=cat"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	// Send init message
	initMsg := ExecMessage{Type: "init", Cols: 80, Rows: 24}
	if err := wsjson.Write(ctx, conn, initMsg); err != nil {
		t.Fatalf("Failed to write init message: %v", err)
	}

	// Send some input followed by EOF (Ctrl-D)
	inputMsg := ExecMessage{Type: "input", Data: "test input\n"}
	if err := wsjson.Write(ctx, conn, inputMsg); err != nil {
		t.Fatalf("Failed to write input message: %v", err)
	}

	// Send EOF
	eofMsg := ExecMessage{Type: "input", Data: "\x04"} // Ctrl-D
	if err := wsjson.Write(ctx, conn, eofMsg); err != nil {
		t.Fatalf("Failed to write EOF message: %v", err)
	}

	// Read messages
	var output strings.Builder
	var gotExit bool

	for i := 0; i < 20; i++ { // Limit iterations to avoid infinite loop
		var msg ExecMessage
		err := wsjson.Read(ctx, conn, &msg)
		if err != nil {
			break
		}

		switch msg.Type {
		case "output":
			data, _ := base64.StdEncoding.DecodeString(msg.Data)
			output.Write(data)
		case "exit":
			gotExit = true
		}

		if gotExit {
			break
		}
	}

	if !strings.Contains(output.String(), "test input") {
		t.Errorf("Expected output to contain 'test input', got: %q", output.String())
	}
}

func TestExecTerminal_LoginShell(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	// Test that bash runs as a login shell by checking the login_shell option
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/exec-ws?cmd=shopt+login_shell+%7C+grep+-q+on+%26%26+echo+login"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	// Send init message
	initMsg := ExecMessage{Type: "init", Cols: 80, Rows: 24}
	if err := wsjson.Write(ctx, conn, initMsg); err != nil {
		t.Fatalf("Failed to write init message: %v", err)
	}

	// Read messages until connection closes
	var output strings.Builder
	var exitCode int = -1

	for {
		var msg ExecMessage
		err := wsjson.Read(ctx, conn, &msg)
		if err != nil {
			break
		}

		switch msg.Type {
		case "output":
			data, err := base64.StdEncoding.DecodeString(msg.Data)
			if err == nil {
				output.Write(data)
			}
		case "exit":
			if msg.Data == "0" {
				exitCode = 0
			} else {
				exitCode = 1
			}
		case "error":
			t.Fatalf("Received error: %s", msg.Data)
		}
	}

	if exitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", exitCode)
	}

	if !strings.Contains(output.String(), "login") {
		t.Errorf("Expected bash to run as login shell, got: %q", output.String())
	}
}

func TestExecTerminal_ControlCharacters(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	// Use cat -v which renders control characters as ^X notation.
	// Sending Ctrl-B (\x02) should appear as "^B" in the output.
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/exec-ws?cmd=cat+-v"

	// Generous overall deadline: under heavy CI load the pty spawn + first
	// output can lag well past a second. We bound every Read on this single
	// context rather than a per-read sub-context — with coder/websocket a Read
	// whose context is cancelled fails the whole connection, so the old
	// 200ms-per-read pattern poisoned the socket on the first premature
	// timeout (every later Read then errored, yielding empty output). This
	// loop instead blocks on Read until data arrives or the deadline fires.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	initMsg := ExecMessage{Type: "init", Cols: 80, Rows: 24}
	if err := wsjson.Write(ctx, conn, initMsg); err != nil {
		t.Fatalf("Failed to write init message: %v", err)
	}

	// Read output until we see ^B (cat -v notation for \x02). Wait for the
	// server's "attached" handshake before sending input so the pty (and
	// cat -v) is guaranteed to be running and won't drop the keystroke.
	var output strings.Builder
	sentInput := false
	for {
		var msg ExecMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			t.Errorf("Ctrl-B (\\x02) was not delivered through pty (read: %v); cat -v output: %q", err, output.String())
			return
		}
		if msg.Type == "attached" && !sentInput {
			// Send Ctrl-B (\x02) followed by newline to flush the line buffer.
			if err := wsjson.Write(ctx, conn, ExecMessage{Type: "input", Data: "\x02\n"}); err != nil {
				t.Fatalf("Failed to write input: %v", err)
			}
			sentInput = true
			continue
		}
		if msg.Type == "output" {
			data, _ := base64.StdEncoding.DecodeString(msg.Data)
			output.Write(data)
		}
		if strings.Contains(output.String(), "^B") {
			return // success
		}
	}
}

// TestExecTerminal_ShelleyEnvVars confirms the websocket spawner exposes
// SHELLEY_CONVERSATION_ID, SHELLEY_MODEL, SHELLEY_USER_EMAIL, SHELLEY_CWD, and
// SHELLEY_TERMINAL_ID to the spawned command. These are how `!` shell
// commands (and persistent terminals) learn what conversation they belong to.
func TestExecTerminal_ShelleyEnvVars(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cwd := t.TempDir()
	cmd := `printf 'CID=%s SLUG=%s MODEL=%s EMAIL=%s CWD=%s TID=%s PORT=%s URL=%s\n' "$SHELLEY_CONVERSATION_ID" "$SHELLEY_CONVERSATION_SLUG" "$SHELLEY_MODEL" "$SHELLEY_USER_EMAIL" "$SHELLEY_CWD" "$SHELLEY_TERMINAL_ID" "$SHELLEY_PORT" "$SHELLEY_URL"`
	// Create a real conversation with a known slug so SHELLEY_CONVERSATION_SLUG
	// gets populated via the DB lookup in handleExecWS.
	slug := "demo-slug"
	conv, err := h.db.CreateConversation(context.Background(), &slug, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") +
		"/api/exec-ws?cmd=" + url.QueryEscape(cmd) +
		"&cwd=" + url.QueryEscape(cwd) +
		"&conversation_id=" + url.QueryEscape(conv.ConversationID) +
		"&model=predictable"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Force a known listenPort so SHELLEY_PORT/URL are populated even when
	// running through httptest (which doesn't go through s.Start).
	h.server.listenPort = 12345

	header := http.Header{}
	header.Set("X-ExeDev-Email", "alice@example.com")
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	if err := wsjson.Write(ctx, conn, ExecMessage{Type: "init", Cols: 80, Rows: 24}); err != nil {
		t.Fatalf("init: %v", err)
	}

	var output strings.Builder
	for {
		var msg ExecMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			break
		}
		if msg.Type == "output" {
			data, _ := base64.StdEncoding.DecodeString(msg.Data)
			output.Write(data)
		}
		if msg.Type == "exit" {
			// keep reading until close to drain any output frames
		}
	}

	got := output.String()
	want := []string{
		"CID=" + conv.ConversationID,
		"SLUG=demo-slug",
		"MODEL=predictable",
		"EMAIL=alice@example.com",
		"CWD=" + cwd,
		"TID=t", // terminal ids are prefixed with 't'
		"PORT=12345",
		"URL=http://localhost:12345",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in output: %q", w, got)
		}
	}
}
