package server

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"slices"

	"golang.org/x/sys/unix"
)

// peerPID is the process that connected to the local socket, or 0 for one of
// another user: the agent's commands run as Shelley's user.
func peerPID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, fmt.Errorf("socket peer: %w", credErr)
	}
	if int(cred.Uid) != os.Getuid() {
		return 0, nil
	}
	return int(cred.Pid), nil
}

// processTemporary reports whether process pid runs in a temporary turn: its
// environment has SHELLEY_TEMPORARY=1.
func processTemporary(pid int) (bool, error) {
	if pid == 0 {
		return false, nil
	}
	environ, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return false, fmt.Errorf("socket peer environment: %w", err)
	}
	return slices.ContainsFunc(bytes.Split(environ, []byte{0}), func(v []byte) bool {
		return string(v) == temporaryEnv
	}), nil
}
