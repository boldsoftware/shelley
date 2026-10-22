package server

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"shelley.exe.dev/dtach"
	"shelley.exe.dev/exescroll"
)

type terminalMessageKind int

const (
	terminalOutput terminalMessageKind = iota
	terminalExit
)

type terminalMessage struct {
	kind     terminalMessageKind
	data     []byte
	exitCode int
}

type terminalClient interface {
	Recv() (terminalMessage, error)
	SendInput([]byte) error
	SendResize(cols, rows uint16) error
	Close() error
}

type dtachTerminalClient struct {
	client *dtach.Client
}

func (c *dtachTerminalClient) Recv() (terminalMessage, error) {
	for {
		typ, payload, err := c.client.Recv()
		if err != nil {
			return terminalMessage{}, err
		}
		switch typ {
		case dtach.MsgSnapshot, dtach.MsgOutput:
			return terminalMessage{kind: terminalOutput, data: payload}, nil
		case dtach.MsgExit:
			code, _ := dtach.DecodeExit(payload)
			return terminalMessage{kind: terminalExit, exitCode: int(code)}, nil
		}
	}
}

func (c *dtachTerminalClient) SendInput(data []byte) error { return c.client.SendInput(data) }
func (c *dtachTerminalClient) SendResize(cols, rows uint16) error {
	return c.client.SendResize(cols, rows)
}
func (c *dtachTerminalClient) Close() error { return c.client.Close() }

type exeScrollTerminalClient struct {
	client *exescroll.Client
}

func (c *exeScrollTerminalClient) Recv() (terminalMessage, error) {
	message, err := c.client.Recv()
	if err != nil {
		return terminalMessage{}, err
	}
	if message.Kind == exescroll.MessageExit {
		return terminalMessage{kind: terminalExit, exitCode: message.ExitCode}, nil
	}
	return terminalMessage{kind: terminalOutput, data: message.Data}, nil
}

func (c *exeScrollTerminalClient) SendInput(data []byte) error { return c.client.SendInput(data) }
func (c *exeScrollTerminalClient) SendResize(cols, rows uint16) error {
	return c.client.SendResize(cols, rows)
}
func (c *exeScrollTerminalClient) Close() error { return c.client.Close() }

type exeScrollPTYClient struct {
	pty         *os.File
	cmd         *exec.Cmd
	exitFile    string
	waitDone    chan error
	closeOnce   sync.Once
	exitEmitted bool
}

func newExeScrollPTYClient(cmd *exec.Cmd, cols, rows uint16, exitFile string) (*exeScrollPTYClient, error) {
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, fmt.Errorf("start exe-scroll: %w", err)
	}
	client := &exeScrollPTYClient{
		pty:      ptmx,
		cmd:      cmd,
		exitFile: exitFile,
		waitDone: make(chan error, 1),
	}
	go func() { client.waitDone <- cmd.Wait() }()
	return client, nil
}

func (c *exeScrollPTYClient) Recv() (terminalMessage, error) {
	buf := make([]byte, 32*1024)
	n, readErr := c.pty.Read(buf)
	if n > 0 {
		return terminalMessage{kind: terminalOutput, data: buf[:n]}, nil
	}
	if !c.exitEmitted {
		if code, err := exescroll.ReadExitStatus(c.exitFile); err == nil {
			c.exitEmitted = true
			return terminalMessage{kind: terminalExit, exitCode: code}, nil
		}
	}
	select {
	case waitErr := <-c.waitDone:
		if waitErr != nil && !errors.Is(readErr, os.ErrClosed) {
			return terminalMessage{}, fmt.Errorf("exe-scroll attach exited: %w", waitErr)
		}
	default:
	}
	if readErr == nil {
		readErr = io.EOF
	}
	return terminalMessage{}, readErr
}

func (c *exeScrollPTYClient) SendInput(data []byte) error {
	_, err := c.pty.Write(data)
	return err
}

func (c *exeScrollPTYClient) SendResize(cols, rows uint16) error {
	return pty.Setsize(c.pty, &pty.Winsize{Cols: cols, Rows: rows})
}

func (c *exeScrollPTYClient) Close() error {
	var err error
	c.closeOnce.Do(func() {
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Signal(syscall.SIGUSR2)
		}
		err = c.pty.Close()
		select {
		case <-c.waitDone:
		case <-time.After(2 * time.Second):
		}
	})
	return err
}

// directPTYClient runs a command on a PTY owned by this process, writing
// nothing to disk. It serves when the disk is too full for a persistent
// session; the command is hung up when the client closes.
type directPTYClient struct {
	pty       *os.File // pollable, so Close interrupts Read and Write
	cmd       *exec.Cmd
	waitDone  chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
	exited    bool

	// SendInput queues input for writeInput, as a terminal emulator does:
	// a command that stops reading must not block the caller, which also
	// has to notice the browser going away.
	inputMu    sync.Mutex
	input      [][]byte
	inputReady chan struct{} // capacity 1: input is queued
}

func startDirectPTY(command, cwd string, cols, rows uint16, extraEnv []string) (*directPTYClient, error) {
	cmd := exec.Command("bash", "--login", "-c", command)
	cmd.Dir = cwd
	cmd.Env = terminalEnvironment(extraEnv)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, fmt.Errorf("terminals: start %q: %w", command, err)
	}
	pollable, err := pollableFile(ptmx)
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, fmt.Errorf("terminals: start %q: %w", command, err)
	}
	client := &directPTYClient{
		pty:        pollable,
		cmd:        cmd,
		waitDone:   make(chan struct{}),
		closed:     make(chan struct{}),
		inputReady: make(chan struct{}, 1),
	}
	go func() {
		_ = cmd.Wait()
		close(client.waitDone)
	}()
	go client.writeInput()
	return client, nil
}

// pollableFile replaces f with a non-blocking duplicate registered with the
// runtime poller. creack/pty calls File.Fd, which leaves its files blocking:
// a Read on them ignores Close.
func pollableFile(f *os.File) (*os.File, error) {
	defer f.Close()
	raw, err := f.SyscallConn()
	if err != nil {
		return nil, err
	}
	var fd int
	var dupErr error
	if err := raw.Control(func(old uintptr) {
		fd, dupErr = unix.FcntlInt(old, unix.F_DUPFD_CLOEXEC, 0)
	}); err != nil {
		return nil, err
	}
	if dupErr != nil {
		return nil, fmt.Errorf("dup pty: %w", dupErr)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("pty nonblock: %w", err)
	}
	return os.NewFile(uintptr(fd), f.Name()), nil
}

func (c *directPTYClient) Recv() (terminalMessage, error) {
	if c.exited {
		return terminalMessage{}, io.EOF
	}
	buf := make([]byte, 32*1024)
	n, err := c.pty.Read(buf)
	if n > 0 {
		return terminalMessage{kind: terminalOutput, data: buf[:n]}, nil
	}
	if errors.Is(err, os.ErrClosed) {
		return terminalMessage{}, err
	}
	// Reads fail with EIO once nothing holds the terminal open. Like an
	// exe-scroll session, that waits for background jobs using it too.
	select {
	case <-c.waitDone:
	case <-c.closed:
		return terminalMessage{}, os.ErrClosed
	}
	c.exited = true
	code := c.cmd.ProcessState.ExitCode()
	if status, ok := c.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		code = 128 + int(status.Signal())
	}
	return terminalMessage{kind: terminalExit, exitCode: code}, nil
}

func (c *directPTYClient) SendInput(data []byte) error {
	c.inputMu.Lock()
	select {
	case <-c.closed:
		c.inputMu.Unlock()
		return os.ErrClosed
	default:
	}
	c.input = append(c.input, data)
	c.inputMu.Unlock()
	select {
	case c.inputReady <- struct{}{}:
	default:
	}
	return nil
}

// writeInput feeds queued input to the PTY until the client closes or the
// terminal goes away.
func (c *directPTYClient) writeInput() {
	for {
		select {
		case <-c.closed:
			return
		case <-c.inputReady:
		}
		c.inputMu.Lock()
		queued := c.input
		c.input = nil
		c.inputMu.Unlock()
		for _, data := range queued {
			if _, err := c.pty.Write(data); err != nil {
				return
			}
		}
	}
}

func (c *directPTYClient) SendResize(cols, rows uint16) error {
	raw, err := c.pty.SyscallConn()
	if err != nil {
		return err
	}
	// Not pty.Setsize: it calls File.Fd, which makes the PTY blocking again.
	var ioctlErr error
	if err := raw.Control(func(fd uintptr) {
		ioctlErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
	}); err != nil {
		return err
	}
	return ioctlErr
}

func (c *directPTYClient) Close() error {
	var err error
	// Closing the master hangs up the terminal, as closing a window does:
	// the kernel sends SIGHUP to the session leader and, once it exits, to
	// the foreground job.
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.pty.Close()
		// The command may outlive the client; don't keep its unread input.
		c.inputMu.Lock()
		c.input = nil
		c.inputMu.Unlock()
	})
	return err
}
