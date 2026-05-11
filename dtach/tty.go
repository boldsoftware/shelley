package dtach

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// AttachTTY attaches the current terminal (stdin/stdout) to the dtach session
// at socketPath. There is no detach key: close the terminal or kill the
// attach process to detach. The session itself keeps running until its
// command exits.
//
// Returns the exit code reported by the session, or 0 if the attach ends
// before the command does (i.e. on detach).
func AttachTTY(socketPath string) (int, error) {
	c, err := Attach(socketPath)
	if err != nil {
		return -1, err
	}
	defer c.Close()

	inFd := int(os.Stdin.Fd())
	outFd := int(os.Stdout.Fd())

	if term.IsTerminal(inFd) {
		st, err := term.MakeRaw(inFd)
		if err != nil {
			return -1, fmt.Errorf("dtach: makeraw: %w", err)
		}
		defer func() { _ = term.Restore(inFd, st) }()
	}

	if term.IsTerminal(outFd) {
		if w, h, err := term.GetSize(outFd); err == nil {
			_ = c.SendResize(uint16(w), uint16(h))
		}
	}

	// Forward SIGWINCH as resize.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			if w, h, err := term.GetSize(outFd); err == nil {
				_ = c.SendResize(uint16(w), uint16(h))
			}
		}
	}()

	// stdin -> server. When stdin closes (EOF or process killed), this
	// goroutine returns; the deferred Close on the dtach client will unblock
	// the server->stdout loop below.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if werr := c.SendInput(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// server -> stdout, terminating on MsgExit or connection close.
	for {
		t, payload, err := c.Recv()
		if err != nil {
			if err == io.EOF {
				return 0, nil
			}
			return 0, nil
		}
		switch t {
		case MsgSnapshot, MsgOutput:
			_, _ = os.Stdout.Write(payload)
		case MsgExit:
			code, _ := DecodeExit(payload)
			return int(code), nil
		}
	}
}
