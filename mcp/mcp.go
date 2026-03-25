// Package mcp implements a client for the Model Context Protocol (MCP).
//
// MCP supports two transports:
//   - stdio: spawns a server subprocess and communicates via JSON-RPC 2.0 over stdin/stdout
//   - HTTP Streamable: POSTs JSON-RPC 2.0 requests to a server URL
//
// Both transports implement the Transport interface.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// Transport is the interface for communicating with an MCP server.
// Both the stdio Client and the HTTPClient implement this interface.
type Transport interface {
	ListTools(ctx context.Context) ([]ToolInfo, error)
	CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error)
	Close() error
}

// ServerConfig describes how to connect to an MCP server.
// If URL is set, the HTTP Streamable transport is used.
// If Command is set, the stdio transport is used.
type ServerConfig struct {
	Name    string            // human-readable name (e.g., "vestige")
	Command string            // binary to execute (stdio transport)
	Args    []string          // command-line arguments (stdio transport)
	Env     map[string]string // extra environment variables (stdio transport)
	URL     string            // server URL (HTTP Streamable transport)
	Headers map[string]string // extra HTTP headers, e.g. auth keys (HTTP transport)
	Defer   bool              // if true, tools are not loaded into context until explicitly activated
}

// NewTransport creates a Transport for the given server config.
// It picks the appropriate implementation based on whether URL or Command is set.
func NewTransport(ctx context.Context, cfg ServerConfig) (Transport, error) {
	if cfg.URL != "" {
		return NewHTTPClient(ctx, cfg)
	}
	if cfg.Command != "" {
		return NewClient(ctx, cfg)
	}
	return nil, fmt.Errorf("mcp: server %q has neither URL nor Command set", cfg.Name)
}

// ToolInfo describes a tool exposed by an MCP server.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Client manages a connection to a single MCP server subprocess.
type Client struct {
	cfg    ServerConfig
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner

	nextID atomic.Int64

	// mu serializes writes to stdin and reads from stdout.
	// We use synchronous request/response, so only one in-flight request at a time.
	mu sync.Mutex

	// closed is set when Close is called.
	closed atomic.Bool
}

// jsonrpcRequest is a JSON-RPC 2.0 request or notification.
type jsonrpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"` // nil for notifications
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonrpcResponse is a JSON-RPC 2.0 response.
type jsonrpcResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *int64           `json:"id"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *jsonrpcError    `json:"error,omitempty"`
}

// jsonrpcError is the error object in a JSON-RPC 2.0 response.
type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *jsonrpcError) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// toolCallResult is the result of a tools/call response.
type toolCallResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolsListResult is the result of a tools/list response.
type toolsListResult struct {
	Tools []ToolInfo `json:"tools"`
}

// NewClient spawns an MCP server subprocess and performs the initialization handshake.
// The context governs the lifetime of the subprocess; canceling it will kill the process.
func NewClient(ctx context.Context, cfg ServerConfig) (*Client, error) {
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)

	// Build environment: inherit current env and merge extras.
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("mcp: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("mcp: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: start %q: %w", cfg.Command, err)
	}

	slog.Info("mcp: started server", "name", cfg.Name, "pid", cmd.Process.Pid)

	// Drain stderr in the background, logging each line.
	go drainStderr(cfg.Name, stderr)

	scanner := bufio.NewScanner(stdout)
	// Allow large responses (16 MB).
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	c := &Client{
		cfg:    cfg,
		cmd:    cmd,
		stdin:  stdin,
		stdout: scanner,
	}

	// Perform initialization handshake.
	if err := c.initialize(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("mcp: initialize %q: %w", cfg.Name, err)
	}

	return c, nil
}

// initialize sends the initialize request and notifications/initialized notification.
func (c *Client) initialize(ctx context.Context) error {
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
	if err := c.sendNotification("notifications/initialized", nil); err != nil {
		return fmt.Errorf("initialized notification: %w", err)
	}

	return nil
}

// ListTools returns the tools available on the MCP server.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
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
// If the server indicates an error in the response (isError: true), the text is returned
// as an error.
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	params := map[string]any{
		"name": name,
	}
	if arguments != nil {
		// Parse arguments so they're embedded as an object, not a string.
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

// parseToolCallResult extracts the concatenated text from a tools/call result.
func parseToolCallResult(raw json.RawMessage) (text string, isError bool, err error) {
	var tcr toolCallResult
	if err := json.Unmarshal(raw, &tcr); err != nil {
		return "", false, err
	}

	var sb strings.Builder
	for _, item := range tcr.Content {
		if item.Type == "text" {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(item.Text)
		}
	}

	return sb.String(), tcr.IsError, nil
}

// Close shuts down the MCP server subprocess.
func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil // already closed
	}

	slog.Info("mcp: closing", "name", c.cfg.Name)

	// Close stdin to signal the subprocess to exit.
	stdinErr := c.stdin.Close()

	// Wait for the process to exit.
	waitErr := c.cmd.Wait()

	return errors.Join(stdinErr, waitErr)
}

// sendRequest sends a JSON-RPC request and waits for the response.
// The caller must not hold c.mu.
func (c *Client) sendRequest(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed.Load() {
		return nil, errors.New("client is closed")
	}

	id := c.nextID.Add(1)
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	}

	if err := c.writeMessage(req); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Read response, skipping any notifications from the server.
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		resp, err := c.readResponse()
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}

		// Skip server-sent notifications (no id).
		if resp.ID == nil {
			slog.Debug("mcp: skipping server notification", "name", c.cfg.Name)
			continue
		}

		if *resp.ID != id {
			return nil, fmt.Errorf("response id mismatch: got %d, want %d", *resp.ID, id)
		}

		if resp.Error != nil {
			return nil, resp.Error
		}

		return resp.Result, nil
	}
}

// sendNotification sends a JSON-RPC notification (no id, no response expected).
func (c *Client) sendNotification(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed.Load() {
		return errors.New("client is closed")
	}

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	return c.writeMessage(req)
}

// writeMessage encodes and writes a JSON-RPC message to the subprocess stdin.
// Caller must hold c.mu.
func (c *Client) writeMessage(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	slog.Debug("mcp: send", "name", c.cfg.Name, "msg", string(data))

	// Write the JSON followed by a newline.
	data = append(data, '\n')
	_, err = c.stdin.Write(data)
	return err
}

// readResponse reads and parses a single JSON-RPC response line from stdout.
// Caller must hold c.mu.
func (c *Client) readResponse() (*jsonrpcResponse, error) {
	if !c.stdout.Scan() {
		if err := c.stdout.Err(); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		return nil, errors.New("unexpected EOF from server")
	}

	line := c.stdout.Bytes()
	slog.Debug("mcp: recv", "name", c.cfg.Name, "msg", string(line))

	var resp jsonrpcResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &resp, nil
}

// drainStderr reads lines from the subprocess stderr and logs them.
func drainStderr(name string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		slog.Debug("mcp: server stderr", "name", name, "line", scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		slog.Debug("mcp: server stderr read error", "name", name, "err", err)
	}
}
