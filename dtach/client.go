package dtach

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

// Client is a programmatic attachment to a dtach session. Use Attach to dial
// the server, then Read/Write to send input and consume output. The snapshot
// (replayed scrollback) is delivered as a MsgSnapshot via Recv.
type Client struct {
	conn net.Conn
	mu   sync.Mutex
}

// Attach dials the dtach server at socketPath. If the socket is missing or
// dead, returns ErrNotRunning.
func Attach(socketPath string) (*Client, error) {
	if _, err := os.Stat(socketPath); err != nil {
		return nil, ErrNotRunning
	}
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	return &Client{conn: conn}, nil
}

// ErrNotRunning indicates the dtach session is not reachable.
var ErrNotRunning = errors.New("dtach: session not running")

// Recv reads the next framed message from the server.
func (c *Client) Recv() (MsgType, []byte, error) {
	return ReadFrame(c.conn)
}

// SendInput sends bytes to the PTY.
func (c *Client) SendInput(p []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return WriteFrame(c.conn, MsgInput, p)
}

// SendResize updates the PTY window size.
func (c *Client) SendResize(cols, rows uint16) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return WriteFrame(c.conn, MsgResize, EncodeResize(cols, rows))
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
