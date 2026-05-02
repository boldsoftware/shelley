//go:build unix && !linux

package browse

import "syscall"

func setPdeathsig(*syscall.SysProcAttr) {}
