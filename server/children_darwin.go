package server

import (
	"os"

	"golang.org/x/sys/unix"
)

// childPIDs lists this process's children.
func childPIDs() ([]int, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	var pids []int
	for _, p := range procs {
		if int(p.Eproc.Ppid) == self {
			pids = append(pids, int(p.Proc.P_pid))
		}
	}
	return pids, nil
}
