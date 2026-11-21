//go:build !linux

package test

import "syscall"

// serverProcAttr puts a test server in a process group of its own.
func serverProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
