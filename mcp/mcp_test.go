package mcp

import (
	"encoding/json"
	"testing"
)

func TestMarshalRequest(t *testing.T) {
	id := int64(1)
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	// Verify it round-trips correctly.
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if got["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v, want 2.0", got["jsonrpc"])
	}
	if got["method"] != "initialize" {
		t.Errorf("method = %v, want initialize", got["method"])
	}
	if got["id"].(float64) != 1 {
		t.Errorf("id = %v, want 1", got["id"])
	}
	params := got["params"].(map[string]any)
	if params["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", params["protocolVersion"])
	}
}

func TestMarshalNotification(t *testing.T) {
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if _, ok := got["id"]; ok {
		t.Error("notification should not have id field")
	}
	if got["method"] != "notifications/initialized" {
		t.Errorf("method = %v, want notifications/initialized", got["method"])
	}
}

func TestParseResponse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		checkFn func(t *testing.T, resp *jsonrpcResponse)
	}{
		{
			name:  "success response",
			input: `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{}}}`,
			checkFn: func(t *testing.T, resp *jsonrpcResponse) {
				if resp.ID == nil || *resp.ID != 1 {
					t.Errorf("id = %v, want 1", resp.ID)
				}
				if resp.Error != nil {
					t.Errorf("unexpected error: %v", resp.Error)
				}
				if resp.Result == nil {
					t.Error("result should not be nil")
				}
			},
		},
		{
			name:  "error response",
			input: `{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"method not found"}}`,
			checkFn: func(t *testing.T, resp *jsonrpcResponse) {
				if resp.ID == nil || *resp.ID != 2 {
					t.Errorf("id = %v, want 2", resp.ID)
				}
				if resp.Error == nil {
					t.Fatal("expected error")
				}
				if resp.Error.Code != -32601 {
					t.Errorf("error code = %d, want -32601", resp.Error.Code)
				}
				if resp.Error.Message != "method not found" {
					t.Errorf("error message = %q, want %q", resp.Error.Message, "method not found")
				}
			},
		},
		{
			name:  "notification (no id)",
			input: `{"jsonrpc":"2.0","method":"some/notification"}`,
			checkFn: func(t *testing.T, resp *jsonrpcResponse) {
				if resp.ID != nil {
					t.Errorf("id should be nil for notification, got %v", *resp.ID)
				}
			},
		},
		{
			name:    "invalid json",
			input:   `not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp jsonrpcResponse
			err := json.Unmarshal([]byte(tt.input), &resp)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.checkFn(t, &resp)
		})
	}
}

func TestParseToolCallResult(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantText  string
		wantError bool
		wantErr   bool // parse error
	}{
		{
			name:     "single text item",
			input:    `{"content":[{"type":"text","text":"hello world"}],"isError":false}`,
			wantText: "hello world",
		},
		{
			name:     "multiple text items",
			input:    `{"content":[{"type":"text","text":"line 1"},{"type":"text","text":"line 2"}],"isError":false}`,
			wantText: "line 1\nline 2",
		},
		{
			name:      "error result",
			input:     `{"content":[{"type":"text","text":"something failed"}],"isError":true}`,
			wantText:  "something failed",
			wantError: true,
		},
		{
			name:     "mixed content types",
			input:    `{"content":[{"type":"image","data":"abc"},{"type":"text","text":"only text"}],"isError":false}`,
			wantText: "only text",
		},
		{
			name:     "empty content",
			input:    `{"content":[],"isError":false}`,
			wantText: "",
		},
		{
			name:    "invalid json",
			input:   `not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, isError, err := parseToolCallResult(json.RawMessage(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected parse error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if text != tt.wantText {
				t.Errorf("text = %q, want %q", text, tt.wantText)
			}
			if isError != tt.wantError {
				t.Errorf("isError = %v, want %v", isError, tt.wantError)
			}
		})
	}
}

func TestParseToolsListResult(t *testing.T) {
	input := `{"tools":[{"name":"read_file","description":"Read a file","inputSchema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`

	var tlr toolsListResult
	if err := json.Unmarshal([]byte(input), &tlr); err != nil {
		t.Fatal(err)
	}

	if len(tlr.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tlr.Tools))
	}

	tool := tlr.Tools[0]
	if tool.Name != "read_file" {
		t.Errorf("name = %q, want read_file", tool.Name)
	}
	if tool.Description != "Read a file" {
		t.Errorf("description = %q, want %q", tool.Description, "Read a file")
	}
	if tool.InputSchema == nil {
		t.Error("inputSchema should not be nil")
	}

	// Verify the schema is valid JSON.
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Errorf("inputSchema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
}

func TestJsonrpcErrorFormat(t *testing.T) {
	e := &jsonrpcError{Code: -32600, Message: "invalid request"}
	got := e.Error()
	want := "jsonrpc error -32600: invalid request"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestMarshalToolsCallRequest(t *testing.T) {
	// Verify the structure of a tools/call request.
	id := int64(5)
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "tools/call",
		Params: map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": "/tmp/test.txt",
			},
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	// Round-trip and verify.
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	params := got["params"].(map[string]any)
	if params["name"] != "read_file" {
		t.Errorf("name = %v, want read_file", params["name"])
	}
	args := params["arguments"].(map[string]any)
	if args["path"] != "/tmp/test.txt" {
		t.Errorf("path = %v, want /tmp/test.txt", args["path"])
	}
}
