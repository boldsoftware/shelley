package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeMCPServer is a test helper that simulates an MCP server.
type fakeMCPServer struct {
	mu        sync.Mutex
	requests  []jsonrpcRequest
	sessionID string
	useSSE    bool // if true, respond with SSE format
}

func (s *fakeMCPServer) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var req jsonrpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()

	// Set session ID on responses.
	if s.sessionID != "" {
		w.Header().Set("Mcp-Session-Id", s.sessionID)
	}

	// Notifications have no ID — respond with 202 Accepted.
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var result json.RawMessage
	switch req.Method {
	case "initialize":
		result = json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"test-server","version":"0.1.0"}}`)
	case "tools/list":
		result = json.RawMessage(`{"tools":[{"name":"echo","description":"Echo input","inputSchema":{"type":"object","properties":{"message":{"type":"string"}}}}]}`)
	case "tools/call":
		result = json.RawMessage(`{"content":[{"type":"text","text":"echoed: hello"}],"isError":false}`)
	default:
		resp := jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonrpcError{Code: -32601, Message: "method not found"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	resp := jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}

	if s.useSSE {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		data, _ := json.Marshal(resp)
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *fakeMCPServer) getRequests() []jsonrpcRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]jsonrpcRequest, len(s.requests))
	copy(cp, s.requests)
	return cp
}

func TestHTTPClient_InitializeAndListTools(t *testing.T) {
	fake := &fakeMCPServer{sessionID: "test-session-123"}
	ts := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer ts.Close()

	cfg := ServerConfig{
		Name: "test",
		URL:  ts.URL,
	}

	ctx := context.Background()
	client, err := NewHTTPClient(ctx, cfg)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	defer client.Close()

	// Verify initialization happened (initialize + notifications/initialized).
	reqs := fake.getRequests()
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(reqs))
	}
	if reqs[0].Method != "initialize" {
		t.Errorf("first request method = %q, want initialize", reqs[0].Method)
	}
	if reqs[1].Method != "notifications/initialized" {
		t.Errorf("second request method = %q, want notifications/initialized", reqs[1].Method)
	}

	// Verify session ID was captured.
	if client.sessionID != "test-session-123" {
		t.Errorf("sessionID = %q, want test-session-123", client.sessionID)
	}

	// List tools.
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Errorf("tool name = %q, want echo", tools[0].Name)
	}
}

func TestHTTPClient_CallTool(t *testing.T) {
	fake := &fakeMCPServer{}
	ts := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer ts.Close()

	cfg := ServerConfig{
		Name: "test",
		URL:  ts.URL,
	}

	ctx := context.Background()
	client, err := NewHTTPClient(ctx, cfg)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	defer client.Close()

	result, err := client.CallTool(ctx, "echo", json.RawMessage(`{"message":"hello"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result != "echoed: hello" {
		t.Errorf("result = %q, want %q", result, "echoed: hello")
	}
}

func TestHTTPClient_SSEResponse(t *testing.T) {
	fake := &fakeMCPServer{useSSE: true}
	ts := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer ts.Close()

	cfg := ServerConfig{
		Name: "test-sse",
		URL:  ts.URL,
	}

	ctx := context.Background()
	client, err := NewHTTPClient(ctx, cfg)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	defer client.Close()

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}

	result, err := client.CallTool(ctx, "echo", json.RawMessage(`{"message":"hello"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result != "echoed: hello" {
		t.Errorf("result = %q, want %q", result, "echoed: hello")
	}
}

func TestHTTPClient_SSEWithNotifications(t *testing.T) {
	// Server sends a notification before the actual response in SSE.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req jsonrpcRequest
		json.Unmarshal(body, &req)

		// Notifications: respond with 202.
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var result json.RawMessage
		switch req.Method {
		case "initialize":
			result = json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{}}`)
		default:
			result = json.RawMessage(`{"content":[{"type":"text","text":"ok"}],"isError":false}`)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Send a notification first (no id).
		notif := `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":50}}`
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", notif)

		// Then the actual response.
		resp := jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		}
		data, _ := json.Marshal(resp)
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	}))
	defer ts.Close()

	cfg := ServerConfig{Name: "test-sse-notif", URL: ts.URL}
	ctx := context.Background()

	client, err := NewHTTPClient(ctx, cfg)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	defer client.Close()

	result, err := client.CallTool(ctx, "test", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want %q", result, "ok")
	}
}

func TestHTTPClient_CustomHeaders(t *testing.T) {
	var gotHeaders http.Header
	var mu sync.Mutex

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHeaders = r.Header.Clone()
		mu.Unlock()

		body, _ := io.ReadAll(r.Body)
		var req jsonrpcRequest
		json.Unmarshal(body, &req)

		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		result := json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{}}`)
		resp := jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	cfg := ServerConfig{
		Name: "test-headers",
		URL:  ts.URL,
		Headers: map[string]string{
			"DD-API-KEY":    "secret-key-123",
			"Authorization": "Bearer tok",
		},
	}

	ctx := context.Background()
	client, err := NewHTTPClient(ctx, cfg)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	defer client.Close()

	mu.Lock()
	headers := gotHeaders
	mu.Unlock()

	if headers.Get("DD-API-KEY") != "secret-key-123" {
		t.Errorf("DD-API-KEY = %q, want secret-key-123", headers.Get("DD-API-KEY"))
	}
	if headers.Get("Authorization") != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", headers.Get("Authorization"), "Bearer tok")
	}
	if headers.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", headers.Get("Content-Type"))
	}
	if headers.Get("Accept") != "application/json, text/event-stream" {
		t.Errorf("Accept = %q, want %q", headers.Get("Accept"), "application/json, text/event-stream")
	}
}

func TestHTTPClient_SessionIDSentOnSubsequentRequests(t *testing.T) {
	var receivedSessionIDs []string
	var mu sync.Mutex

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedSessionIDs = append(receivedSessionIDs, r.Header.Get("Mcp-Session-Id"))
		mu.Unlock()

		w.Header().Set("Mcp-Session-Id", "sess-abc")

		body, _ := io.ReadAll(r.Body)
		var req jsonrpcRequest
		json.Unmarshal(body, &req)

		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var result json.RawMessage
		switch req.Method {
		case "initialize":
			result = json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{}}`)
		case "tools/list":
			result = json.RawMessage(`{"tools":[]}`)
		default:
			result = json.RawMessage(`{}`)
		}

		resp := jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	cfg := ServerConfig{Name: "test-session", URL: ts.URL}
	ctx := context.Background()

	client, err := NewHTTPClient(ctx, cfg)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	defer client.Close()

	// Make another request after initialization.
	_, err = client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	mu.Lock()
	ids := receivedSessionIDs
	mu.Unlock()

	// First request (initialize) should have no session ID.
	if ids[0] != "" {
		t.Errorf("first request should have empty session id, got %q", ids[0])
	}

	// Subsequent requests should include the session ID.
	// ids[1] is notifications/initialized, ids[2] is tools/list
	if len(ids) < 3 {
		t.Fatalf("expected at least 3 requests, got %d", len(ids))
	}
	if ids[2] != "sess-abc" {
		t.Errorf("third request session id = %q, want sess-abc", ids[2])
	}
}

func TestHTTPClient_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer ts.Close()

	cfg := ServerConfig{Name: "test-error", URL: ts.URL}
	ctx := context.Background()

	_, err := NewHTTPClient(ctx, cfg)
	if err == nil {
		t.Fatal("expected error from NewHTTPClient")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500, got: %v", err)
	}
}

func TestHTTPClient_JSONRPCError(t *testing.T) {
	// Server that returns a JSON-RPC error for tools/call.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req jsonrpcRequest
		json.Unmarshal(body, &req)

		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var resp jsonrpcResponse
		switch req.Method {
		case "initialize":
			resp = jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{}}`)}
		default:
			resp = jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonrpcError{Code: -32601, Message: "method not found"}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	cfg := ServerConfig{Name: "test-rpc-err", URL: ts.URL}
	ctx := context.Background()

	client, err := NewHTTPClient(ctx, cfg)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	defer client.Close()

	// ListTools hits the default case which returns a JSON-RPC error.
	_, err = client.ListTools(ctx)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "method not found") {
		t.Errorf("error should mention 'method not found', got: %v", err)
	}
}

func TestHTTPClient_ClosePreventsRequests(t *testing.T) {
	fake := &fakeMCPServer{}
	ts := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer ts.Close()

	cfg := ServerConfig{Name: "test-close", URL: ts.URL}
	ctx := context.Background()

	client, err := NewHTTPClient(ctx, cfg)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}

	client.Close()

	_, err = client.ListTools(ctx)
	if err == nil {
		t.Fatal("expected error after close")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("error should mention closed, got: %v", err)
	}

	// Double close should be safe.
	if err := client.Close(); err != nil {
		t.Errorf("double close should not error, got: %v", err)
	}
}

func TestHTTPClient_ImplementsTransport(t *testing.T) {
	// Compile-time check that HTTPClient implements Transport.
	var _ Transport = (*HTTPClient)(nil)
	var _ Transport = (*Client)(nil)
}

func TestNewTransport_HTTP(t *testing.T) {
	fake := &fakeMCPServer{}
	ts := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer ts.Close()

	cfg := ServerConfig{Name: "test-factory", URL: ts.URL}
	ctx := context.Background()

	transport, err := NewTransport(ctx, cfg)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	defer transport.Close()

	_, ok := transport.(*HTTPClient)
	if !ok {
		t.Errorf("expected *HTTPClient, got %T", transport)
	}
}

func TestNewTransport_NeitherURLNorCommand(t *testing.T) {
	cfg := ServerConfig{Name: "test-neither"}
	ctx := context.Background()

	_, err := NewTransport(ctx, cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "neither URL nor Command") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHTTPClient_CallToolNilArguments(t *testing.T) {
	fake := &fakeMCPServer{}
	ts := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer ts.Close()

	cfg := ServerConfig{Name: "test", URL: ts.URL}
	ctx := context.Background()

	client, err := NewHTTPClient(ctx, cfg)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	defer client.Close()

	result, err := client.CallTool(ctx, "echo", nil)
	if err != nil {
		t.Fatalf("CallTool with nil arguments: %v", err)
	}
	if result != "echoed: hello" {
		t.Errorf("result = %q, want %q", result, "echoed: hello")
	}
}


