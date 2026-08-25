package exescroll

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	messageData   = 1
	messageWinch  = 2
	messageAttach = 3
	messageDetach = 4

	replayScrollback = 2
	maxFrame         = 4 * 1024 * 1024
)

// MessageKind identifies output and command-exit events from a session.
type MessageKind int

const (
	MessageOutput MessageKind = iota
	MessageExit
)

// Message is one event received from an exe-scroll session.
type Message struct {
	Kind     MessageKind
	Data     []byte
	ExitCode int
}

// Client is a direct attachment to exe-scroll's Unix-socket protocol.
type Client struct {
	conn        net.Conn
	exitFile    string
	writeMu     sync.Mutex
	exitEmitted bool
}

// Attach connects to an existing exe-scroll session and requests full scrollback.
func Attach(socket, exitFile string, cols, rows uint16) (*Client, error) {
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("exe-scroll: attach: %w", err)
	}
	client := &Client{conn: conn, exitFile: exitFile}
	if err := client.SendResize(cols, rows); err != nil {
		conn.Close()
		return nil, err
	}
	if err := client.writeFrame(messageAttach, []byte{replayScrollback}); err != nil {
		conn.Close()
		return nil, err
	}
	return client, nil
}

// Recv returns terminal output, followed by the wrapped command's exit status
// when the session socket closes naturally.
func (c *Client) Recv() (Message, error) {
	for {
		typ, payload, err := readFrame(c.conn)
		if err != nil {
			if !c.exitEmitted {
				if code, statusErr := ReadExitStatus(c.exitFile); statusErr == nil {
					c.exitEmitted = true
					return Message{Kind: MessageExit, ExitCode: code}, nil
				}
			}
			return Message{}, err
		}
		if typ == messageData {
			return Message{Kind: MessageOutput, Data: payload}, nil
		}
	}
}

// SendInput writes raw terminal bytes to the session PTY.
func (c *Client) SendInput(data []byte) error {
	return c.writeFrame(messageData, data)
}

// SendResize updates the session PTY size.
func (c *Client) SendResize(cols, rows uint16) error {
	var payload [8]byte
	binary.LittleEndian.PutUint16(payload[0:2], rows)
	binary.LittleEndian.PutUint16(payload[2:4], cols)
	return c.writeFrame(messageWinch, payload[:])
}

// Close detaches without ending the session.
func (c *Client) Close() error {
	_ = c.writeFrame(messageDetach, nil)
	return c.conn.Close()
}

func (c *Client) writeFrame(typ byte, payload []byte) error {
	if len(payload) > maxFrame {
		return fmt.Errorf("exe-scroll: frame too large: %d", len(payload))
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	var header [5]byte
	header[0] = typ
	binary.LittleEndian.PutUint32(header[1:], uint32(len(payload)))
	if err := writeAll(c.conn, header[:]); err != nil {
		return err
	}
	return writeAll(c.conn, payload)
}

func readFrame(reader io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, nil, err
	}
	size := binary.LittleEndian.Uint32(header[1:])
	if size > maxFrame {
		return 0, nil, fmt.Errorf("exe-scroll: frame too large: %d", size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

// ReadExitStatus reads the status atomically written by Shelley's command wrapper.
func ReadExitStatus(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("exe-scroll: invalid exit status: %w", err)
	}
	return code, nil
}
