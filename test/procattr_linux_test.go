package test

import "syscall"

// serverProcAttr puts a test server in a process group of its own, which
// dies with the test process.
func serverProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
