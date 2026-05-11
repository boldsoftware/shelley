package dtach

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/creack/pty"
)

// ServerOptions configures Serve.
type ServerOptions struct {
	// SocketPath is the Unix socket the server listens on.
	SocketPath string
	// Command and Args are the program to exec inside the PTY.
	Command string
	Args    []string
	// Dir is the working directory for the command.
	Dir string
	// Env is the environment for the command. If nil, os.Environ() is used.
	Env []string
	// Cols, Rows is the initial PTY size. Defaults to 80x24 if zero.
	Cols, Rows uint16
	// ScrollbackBytes is the size of the rolling scrollback replayed to new
	// attachers. Defaults to 256 KiB.
	ScrollbackBytes int
	// Ready, if non-nil, is closed once the socket is listening and the PTY
	// process is started. Intended for tests/in-process spawners that want to
	// avoid polling for socket readiness.
	Ready chan<- struct{}
}

// Serve runs the PTY-backed process and serves clients on a Unix socket until
// the process exits, then unlinks the socket. It blocks until exit.
func Serve(opts ServerOptions) error {
	if opts.Cols == 0 {
		opts.Cols = 80
	}
	if opts.Rows == 0 {
		opts.Rows = 24
	}
	if opts.ScrollbackBytes <= 0 {
		opts.ScrollbackBytes = 256 * 1024
	}
	if opts.Command == "" {
		return errors.New("dtach: empty command")
	}

	if err := os.MkdirAll(filepath.Dir(opts.SocketPath), 0o700); err != nil {
		return fmt.Errorf("dtach: mkdir socket dir: %w", err)
	}
	_ = os.Remove(opts.SocketPath)
	ln, err := net.Listen("unix", opts.SocketPath)
	if err != nil {
		return fmt.Errorf("dtach: listen: %w", err)
	}
	// Restrict access to the user.
	_ = os.Chmod(opts.SocketPath, 0o600)
	defer func() {
		ln.Close()
		_ = os.Remove(opts.SocketPath)
	}()

	cmd := exec.Command(opts.Command, opts.Args...)
	cmd.Dir = opts.Dir
	if opts.Env == nil {
		cmd.Env = os.Environ()
	} else {
		cmd.Env = opts.Env
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: opts.Cols, Rows: opts.Rows})
	if err != nil {
		return fmt.Errorf("dtach: start pty: %w", err)
	}
	defer ptmx.Close()

	sess := &session{
		ptmx:       ptmx,
		cmd:        cmd,
		scrollback: newRing(opts.ScrollbackBytes),
		clients:    make(map[*client]struct{}),
	}

	// Accept loop. Track whether anyone has ever attached so that we don't
	// tear the session down for a fast-exiting command before a caller has
	// had a chance to connect and receive MsgExit.
	attached := make(chan struct{}, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case attached <- struct{}{}:
			default:
			}
			go sess.serveClient(conn)
		}
	}()

	if opts.Ready != nil {
		close(opts.Ready)
	}

	// Read from PTY -> fan out to clients + scrollback.
	pumpDone := make(chan struct{})
	go func() {
		sess.pumpPTY()
		close(pumpDone)
	}()

	// If the command exits before anyone connects, give callers a brief window
	// to attach so they can observe the exit code and final scrollback.
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitDone:
		// Process exited; wait briefly for a first attach before tearing down.
		select {
		case <-attached:
		case <-time.After(2 * time.Second):
		}
	case <-attached:
		waitErr = <-waitDone
	}
	exitCode := int32(0)
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exitCode = int32(ee.ExitCode())
		} else {
			exitCode = -1
		}
	}
	// Drain pty output before announcing exit so attached clients see all output.
	<-pumpDone
	sess.shutdown(exitCode)
	ln.Close()
	return nil
}

type client struct {
	conn   net.Conn
	sendMu sync.Mutex
}

func (c *client) write(t MsgType, p []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return WriteFrame(c.conn, t, p)
}

type session struct {
	ptmx       *os.File
	cmd        *exec.Cmd
	scrollback *ring

	mu       sync.Mutex
	clients  map[*client]struct{}
	exited   bool
	exitCode int32
}

func (s *session) pumpPTY() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			// Append to the scrollback and snapshot the client list as a
			// single critical section so that any new client that takes
			// `s.mu` afterwards sees the data in its snapshot AND is added
			// to s.clients only after this broadcast has finished
			// dispatching, preventing double-delivery of these bytes.
			s.mu.Lock()
			s.scrollback.write(chunk)
			cs := make([]*client, 0, len(s.clients))
			for c := range s.clients {
				cs = append(cs, c)
			}
			s.mu.Unlock()
			for _, c := range cs {
				if err := c.write(MsgOutput, chunk); err != nil {
					s.dropClient(c)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *session) dropClient(c *client) {
	s.mu.Lock()
	if _, ok := s.clients[c]; ok {
		delete(s.clients, c)
		_ = c.conn.Close()
	}
	s.mu.Unlock()
}

func (s *session) shutdown(code int32) {
	s.mu.Lock()
	s.exited = true
	s.exitCode = code
	cs := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		cs = append(cs, c)
	}
	s.mu.Unlock()
	payload := EncodeExit(code)
	for _, c := range cs {
		_ = c.write(MsgExit, payload)
		_ = c.conn.Close()
	}
}

func (s *session) serveClient(conn net.Conn) {
	c := &client{conn: conn}

	// Send the snapshot under c.sendMu so that any concurrent broadcast that
	// tries to write MsgOutput to this client blocks until the snapshot has
	// landed on the wire. pumpPTY appends to the scrollback and snapshots the
	// client list under s.mu, so by also snapshotting/joining under s.mu we
	// ensure exactly-once delivery of every byte after our snapshot.
	c.sendMu.Lock()
	s.mu.Lock()
	exited := s.exited
	exitCode := s.exitCode
	snap := s.scrollback.snapshot()
	if !exited {
		s.clients[c] = struct{}{}
	}
	s.mu.Unlock()
	writeErr := WriteFrame(conn, MsgSnapshot, snap)
	c.sendMu.Unlock()

	if writeErr != nil {
		s.dropClient(c)
		conn.Close()
		return
	}
	if exited {
		_ = c.write(MsgExit, EncodeExit(exitCode))
		conn.Close()
		return
	}

	defer s.dropClient(c)

	for {
		t, payload, err := ReadFrame(conn)
		if err != nil {
			return
		}
		switch t {
		case MsgInput:
			if _, err := s.ptmx.Write(payload); err != nil {
				return
			}
		case MsgResize:
			if cols, rows, ok := DecodeResize(payload); ok {
				setWinsize(s.ptmx, cols, rows)
			}
		default:
			// ignore unknown
		}
	}
}

// setWinsize forwards a TIOCSWINSZ to the PTY.
func setWinsize(f *os.File, cols, rows uint16) {
	ws := struct {
		Rows, Cols, X, Y uint16
	}{Rows: rows, Cols: cols}
	_, _, _ = syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		uintptr(syscall.TIOCSWINSZ),
		uintptr(unsafe.Pointer(&ws)),
	)
}
