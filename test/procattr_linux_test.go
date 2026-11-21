package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// serverProcAttr puts a test server in a process group of its own, which
// dies with the test process.
func serverProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

// killStrays kills the process groups of processes working under dir.
// Background jobs a server starts are in groups of their own and outlive
// it by design.
func killStrays(dir string) {
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return
	}
	entries, _ := os.ReadDir("/proc")
	self := syscall.Getpgrp()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cwd, err := os.Readlink("/proc/" + e.Name() + "/cwd")
		if err != nil || cwd != dir && !strings.HasPrefix(cwd, dir+"/") {
			continue
		}
		if pgid, err := syscall.Getpgid(pid); err == nil && pgid != self {
			syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}
}

func TestKillStrays(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "cwd")
	os.Mkdir(sub, 0o755)
	cmd := exec.Command("sleep", "600")
	cmd.Dir = sub
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	other := exec.Command("sleep", "600")
	other.Dir = t.TempDir()
	other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer other.Process.Kill()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	killStrays(dir)
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("a job working under the directory survived")
	}
	if dying(t, other.Process.Pid) {
		t.Error("a process elsewhere was killed")
	}
}

// dying says whether process pid has exited or has a SIGKILL pending: a
// killed process answers signals until it is reaped.
func dying(t *testing.T, pid int) bool {
	status, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(status)) {
		name, value, _ := strings.Cut(strings.TrimSpace(line), ":\t")
		switch name {
		case "State":
			if value[0] == 'Z' || value[0] == 'X' {
				return true
			}
		case "SigPnd", "ShdPnd":
			mask, err := strconv.ParseUint(value, 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			if mask&(1<<(syscall.SIGKILL-1)) != 0 {
				return true
			}
		}
	}
	return false
}
