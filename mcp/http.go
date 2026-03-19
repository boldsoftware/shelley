package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// HTTPClient communicates with an MCP server over the HTTP Streamable transport.
// It implements the Transport interface.
type HTTPClient struct {
	cfg       ServerConfig
	httpClient *http.Client

	nextID atomic.Int64

	mu        sync.Mutex
	sessionID string // Mcp-Session-Id from the server
	closed    atomic.Bool
}

// NewHTTPClient creates an HTTPClient and performs the MCP initialization handshake.
func NewHTTPClient(ctx context.Context, cfg ServerConfig) (*HTTPClient, error) {
	c := &HTTPClient{
		cfg:        cfg,
		httpClient: &http.Client{},
	}

	if err := c.initialize(ctx); err != nil {
		return nil, fmt.Errorf("mcp: initialize %q: %w", cfg.Name, err)
	}

	return c, nil
}

// initialize sends the initialize request and notifications/initialized notification.
func (c *HTTPClient) initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "shelley",
			"version": "1.0.0",
		},
	}

	result, err := c.sendRequest(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("initialize request: %w", err)
	}

	slog.Info("mcp: initialized", "name", c.cfg.Name, "result", string(result))

	// Send initialized notification (no id, no response expected).
	if err := c.sendNotification(ctx, "notifications/initialized", nil); err != nil {
		return fmt.Errorf("initialized notification: %w", err)
	}

	return nil
}

// ListTools returns the tools available on the MCP server.
func (c *HTTPClient) ListTools(ctx context.Context) ([]ToolInfo, error) {
	result, err := c.sendRequest(ctx, "tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: tools/list: %w", err)
	}

	var tlr toolsListResult
	if err := json.Unmarshal(result, &tlr); err != nil {
		return nil, fmt.Errorf("mcp: parse tools/list result: %w", err)
	}

	slog.Debug("mcp: listed tools", "name", c.cfg.Name, "count", len(tlr.Tools))
	return tlr.Tools, nil
}

// CallTool invokes a tool on the MCP server and returns the concatenated text result.
func (c *HTTPClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	params := map[string]any{
		"name": name,
	}
	if arguments != nil {
		var args any
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", fmt.Errorf("mcp: invalid arguments JSON: %w", err)
		}
		params["arguments"] = args
	}

	slog.Debug("mcp: calling tool", "name", c.cfg.Name, "tool", name)

	result, err := c.sendRequest(ctx, "tools/call", params)
	if err != nil {
		return "", fmt.Errorf("mcp: tools/call %q: %w", name, err)
	}

	text, isError, err := parseToolCallResult(result)
	if err != nil {
		return "", fmt.Errorf("mcp: parse tools/call %q result: %w", name, err)
	}

	if isError {
		return "", fmt.Errorf("mcp: tool %q returned error: %s", name, text)
	}

	return text, nil
}

// Close releases resources associated with the HTTP client.
func (c *HTTPClient) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	slog.Info("mcp: closing", "name", c.cfg.Name)
	return nil
}

// sendRequest sends a JSON-RPC request over HTTP and returns the result.
func (c *HTTPClient) sendRequest(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed.Load() {
		return nil, fmt.Errorf("client is closed")
	}

	id := c.nextID.Add(1)
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	}

	resp, err := c.doPost(ctx, req)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, resp.Error
	}

	return resp.Result, nil
}

// sendNotification sends a JSON-RPC notification (no id, no response expected) over HTTP.
func (c *HTTPClient) sendNotification(ctx context.Context, method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed.Load() {
		return fmt.Errorf("client is closed")
	}

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	slog.Debug("mcp: send notification", "name", c.cfg.Name, "msg", string(body))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(httpReq)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer httpResp.Body.Close()
	io.Copy(io.Discard, httpResp.Body)

	// Track session ID.
	if sid := httpResp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}

	if httpResp.StatusCode >= 400 {
		return fmt.Errorf("http %d for notification %q", httpResp.StatusCode, method)
	}

	return nil
}

// doPost sends a JSON-RPC message via HTTP POST and parses the response.
// It handles both direct JSON and SSE response types.
// Caller must hold c.mu.
func (c *HTTPClient) doPost(ctx context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	slog.Debug("mcp: send", "name", c.cfg.Name, "msg", string(body))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(httpReq)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}
	defer httpResp.Body.Close()

	// Track session ID.
	if sid := httpResp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}

	if httpResp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("http %d: %s", httpResp.StatusCode, string(respBody))
	}

	ct := httpResp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		return c.parseSSEResponse(httpResp.Body)
	}

	// Default: parse as direct JSON response.
	var resp jsonrpcResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	slog.Debug("mcp: recv", "name", c.cfg.Name, "id", resp.ID)
	return &resp, nil
}

// parseSSEResponse reads an SSE stream and extracts the first JSON-RPC response
// from an "event: message" / "data: ..." block.
func (c *HTTPClient) parseSSEResponse(r io.Reader) (*jsonrpcResponse, error) {
	scanner := bufio.NewScanner(r)
	// Allow large SSE payloads (16 MB).
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	var eventType string
	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// Blank line = end of event; reset for next event.
			eventType = ""
			continue
		}

		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}

		if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimSpace(data)

			if eventType != "message" {
				slog.Debug("mcp: skipping SSE event", "name", c.cfg.Name, "event", eventType)
				continue
			}

			var resp jsonrpcResponse
			if err := json.Unmarshal([]byte(data), &resp); err != nil {
				slog.Debug("mcp: skipping non-JSON SSE data", "name", c.cfg.Name, "err", err)
				continue
			}

			// Skip server-sent notifications (no id).
			if resp.ID == nil {
				slog.Debug("mcp: skipping server notification in SSE", "name", c.cfg.Name)
				continue
			}

			slog.Debug("mcp: recv SSE", "name", c.cfg.Name, "id", *resp.ID)
			return &resp, nil
		}

		// Ignore comment lines (starting with ':') and unknown fields.
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan SSE: %w", err)
	}

	return nil, fmt.Errorf("SSE stream ended without a message event")
}

// setHeaders sets the standard headers and any custom headers on an HTTP request.
// Caller must hold c.mu.
func (c *HTTPClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}
}
