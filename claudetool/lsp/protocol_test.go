package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"testing"
	"time"
)

func TestEncodeHeader(t *testing.T) {
	got := EncodeHeader(42)
	want := "Content-Length: 42\r\n\r\n"
	if got != want {
		t.Errorf("EncodeHeader(42) = %q, want %q", got, want)
	}
}

func TestDecodeHeader(t *testing.T) {
	tests := []struct {
		input string
		want  int
		ok    bool
	}{
		{"Content-Length: 42", 42, true},
		{"Content-Length: 0", 0, true},
		{"Content-Length: 12345", 12345, true},
		{"content-type: json", 0, false},
		{"", 0, false},
		{"Content-Length: abc", 0, false},
		{"  Content-Length: 10  ", 10, true},
	}
	for _, tt := range tests {
		got, ok := DecodeHeader(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Errorf("DecodeHeader(%q) = (%d, %v), want (%d, %v)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestRPCRequestEncoding(t *testing.T) {
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  map[string]any{"processId": 123},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v, want 2.0", parsed["jsonrpc"])
	}
	if parsed["method"] != "initialize" {
		t.Errorf("method = %v, want initialize", parsed["method"])
	}
	if parsed["id"].(float64) != 1 {
		t.Errorf("id = %v, want 1", parsed["id"])
	}
}

func TestRPCNotificationEncoding(t *testing.T) {
	notif := rpcNotification{
		JSONRPC: "2.0",
		Method:  "initialized",
	}
	data, err := json.Marshal(notif)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, hasID := parsed["id"]; hasID {
		t.Error("notification should not have id field")
	}
	if parsed["method"] != "initialized" {
		t.Errorf("method = %v, want initialized", parsed["method"])
	}
}

func TestClientContextCancellation(t *testing.T) {
	// Use `sleep` so nothing is echoed back to stdout
	cmd := exec.Command("sleep", "60")
	client, err := NewClient(cmd)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err = client.Call(ctx, "test/method", nil, nil)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}

	// Kill the process directly — don't use Close() which tries graceful shutdown
	if client.cmd.Process != nil {
		client.cmd.Process.Kill()
	}
}

func TestClientAlive(t *testing.T) {
	cmd := exec.Command("cat")
	client, err := NewClient(cmd)
	if err != nil {
		t.Fatal(err)
	}

	if !client.Alive() {
		t.Error("expected client to be alive after creation")
	}

	client.stdin.Close()
	time.Sleep(100 * time.Millisecond)

	if client.Alive() {
		t.Error("expected client to be dead after stdin close")
	}
}

func TestClientCallWithMockServer(t *testing.T) {
	// Create pipes for mock communication
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()

	cmd := exec.Command("true") // dummy, not used
	client := &Client{
		cmd:     cmd,
		stdin:   clientWriter,
		stdout:  clientReader,
		pending: make(map[int64]chan *rpcResponse),
		closed:  make(chan struct{}),
	}
	go client.readLoop()

	// Mock server goroutine: read one JSON-RPC message and respond
	go func() {
		reader := bufio.NewReader(serverReader)
		// Read Content-Length header
		var contentLength int
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if n, ok := DecodeHeader(line); ok {
				contentLength = n
			}
			if line == "\r\n" || line == "\n" {
				break
			}
		}
		// Read body
		body := make([]byte, contentLength)
		if _, err := io.ReadFull(reader, body); err != nil {
			return
		}

		// Parse request to get ID
		var req struct {
			ID int64 `json:"id"`
		}
		json.Unmarshal(body, &req)

		// Send response
		resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"capabilities":{}}}`, req.ID)
		header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(resp))
		serverWriter.Write([]byte(header + resp))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var result map[string]any
	err := client.Call(ctx, "initialize", map[string]any{"processId": 1}, &result)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if _, ok := result["capabilities"]; !ok {
		t.Error("expected capabilities in result")
	}

	clientWriter.Close()
	serverWriter.Close()
}
