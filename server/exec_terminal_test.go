package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestExecTerminal_SimpleCommand(t *testing.T) {
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
	h := NewTestHarness(t)

	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	// Use cat -v which renders control characters as ^X notation.
	// Sending Ctrl-B (\x02) should appear as "^B" in the output.
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/exec-ws?cmd=cat+-v"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	// Send Ctrl-B (\x02) followed by newline to flush the line buffer.
	if err := wsjson.Write(ctx, conn, ExecMessage{Type: "input", Data: "\x02\n"}); err != nil {
		t.Fatalf("Failed to write input: %v", err)
	}

	// Read output until we see ^B (cat -v notation for \x02).
	var output strings.Builder
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		readCtx, readCancel := context.WithTimeout(ctx, 200*time.Millisecond)
		var msg ExecMessage
		err := wsjson.Read(readCtx, conn, &msg)
		readCancel()
		if err != nil {
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
	t.Errorf("Ctrl-B (\\x02) was not delivered through pty; cat -v output: %q", output.String())
}
