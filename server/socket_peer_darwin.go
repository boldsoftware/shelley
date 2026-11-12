package server

import "net"

// peerPID is not needed on macOS, where processTemporary knows no
// environments.
func peerPID(*net.UnixConn) (int, error) { return 0, nil }

// processTemporary is false on macOS, which doesn't expose other processes'
// environments: messages from temporary turns through the local socket are
// temporary only on Linux.
func processTemporary(int) (bool, error) { return false, nil }
