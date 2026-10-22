package server

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// childPIDs lists this process's children.
func childPIDs() ([]int, error) {
	stats, err := filepath.Glob("/proc/[0-9]*/stat")
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	var pids []int
	for _, path := range stats {
		b, err := os.ReadFile(path)
		if err != nil {
			continue // The process exited.
		}
		// "pid (comm) state ppid ...", where comm may hold spaces and parens.
		stat := string(b)
		fields := strings.Fields(stat[strings.LastIndex(stat, ")")+1:])
		if len(fields) < 2 {
			continue
		}
		if ppid, err := strconv.Atoi(fields[1]); err == nil && ppid == self {
			pid, _ := strconv.Atoi(filepath.Base(filepath.Dir(path)))
			pids = append(pids, pid)
		}
	}
	return pids, nil
}
