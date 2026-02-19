package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client is a JSON-RPC 2.0 client that communicates with an LSP server over stdin/stdout.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	mu       sync.Mutex
	nextID   atomic.Int64
	pending  map[int64]chan *rpcResponse
	closed   chan struct{}
	closeErr error
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("LSP error %d: %s", e.Code, e.Message)
}

// NewClient starts the given command and returns a JSON-RPC 2.0 client connected to it.
func NewClient(cmd *exec.Cmd) (*Client, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start LSP server: %w", err)
	}
	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  stdout,
		pending: make(map[int64]chan *rpcResponse),
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// Call sends a JSON-RPC request and waits for the response.
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	ch := make(chan *rpcResponse, 1)

	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	if err := c.send(req); err != nil {
		return fmt.Errorf("send %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return fmt.Errorf("LSP server closed: %w", c.closeErr)
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("unmarshal %s result: %w", method, err)
			}
		}
		return nil
	}
}

// Notify sends a JSON-RPC notification (no response expected).
func (c *Client) Notify(method string, params any) error {
	n := rpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	return c.send(n)
}

// Close shuts down the LSP server gracefully.
func (c *Client) Close() error {
	// Send shutdown request with timeout (server may already be dead or unresponsive)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = c.Call(ctx, "shutdown", nil, nil)
	cancel()
	_ = c.Notify("exit", nil)
	_ = c.stdin.Close()

	// Wait briefly for process to exit, then kill it
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()

	var err error
	select {
	case err = <-done:
	case <-time.After(3 * time.Second):
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		err = <-done
	}

	// Signal readLoop to stop
	select {
	case <-c.closed:
	default:
		c.closeErr = err
		close(c.closed)
	}
	return err
}

// Alive returns true if the underlying process is still running.
func (c *Client) Alive() bool {
	select {
	case <-c.closed:
		return false
	default:
		return c.cmd.ProcessState == nil // not yet exited
	}
}

func (c *Client) send(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := io.WriteString(c.stdin, header); err != nil {
		return err
	}
	_, err = c.stdin.Write(data)
	return err
}

func (c *Client) readLoop() {
	reader := bufio.NewReader(c.stdout)
	for {
		// Read headers
		contentLength := -1
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				c.mu.Lock()
				c.closeErr = err
				c.mu.Unlock()
				select {
				case <-c.closed:
				default:
					close(c.closed)
				}
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break // End of headers
			}
			if strings.HasPrefix(line, "Content-Length: ") {
				n, err := strconv.Atoi(strings.TrimPrefix(line, "Content-Length: "))
				if err == nil {
					contentLength = n
				}
			}
		}
		if contentLength < 0 {
			continue
		}

		// Read body
		body := make([]byte, contentLength)
		if _, err := io.ReadFull(reader, body); err != nil {
			c.mu.Lock()
			c.closeErr = err
			c.mu.Unlock()
			select {
			case <-c.closed:
			default:
				close(c.closed)
			}
			return
		}

		var resp rpcResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			slog.Debug("lsp: failed to unmarshal response", "err", err)
			continue
		}

		// If no ID, it's a server notification — ignore
		if resp.ID == nil {
			continue
		}

		var id int64
		if err := json.Unmarshal(*resp.ID, &id); err != nil {
			slog.Debug("lsp: failed to unmarshal response ID", "err", err)
			continue
		}

		c.mu.Lock()
		ch, ok := c.pending[id]
		c.mu.Unlock()
		if ok {
			ch <- &resp
		}
	}
}

// EncodeHeader returns the Content-Length header for a JSON-RPC message.
// Exported for testing.
func EncodeHeader(bodyLen int) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n", bodyLen)
}

// DecodeHeader parses a Content-Length header value from a header line.
// Exported for testing.
func DecodeHeader(line string) (int, bool) {
	val, ok := strings.CutPrefix(strings.TrimSpace(line), "Content-Length: ")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, false
	}
	return n, true
}
