//go:build !linux

package test

import "syscall"

// serverProcAttr puts a test server in a process group of its own.
func serverProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killStrays would kill the processes working under dir; only Linux says
// where a process works.
func killStrays(dir string) {}
