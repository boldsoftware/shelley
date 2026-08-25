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
