//go:build linux

package server

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"shelley.exe.dev/exescroll"
)

func TestExeScrollHelperProcess(t *testing.T) {
	if os.Getenv("SHELLEY_EXE_SCROLL_HELPER") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		t.Fatal("missing helper argument separator")
	}
	if err := exescroll.Exec(os.Args[separator+1:]); err != nil {
		t.Fatal(err)
	}
}

func newExeScrollTestSessions(t *testing.T) *TerminalSessions {
	t.Helper()
	ts, err := NewTerminalSessions(t.TempDir(), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts.exeScrollCommand = func(args ...string) *exec.Cmd {
		helperArgs := append([]string{"-test.run=^TestExeScrollHelperProcess$", "--"}, args...)
		return exec.Command(os.Args[0], helperArgs...)
	}
	return ts
}

func TestExeScrollSessionReattachesWithScrollbackAndExitStatus(t *testing.T) {
	ts := newExeScrollTestSessions(t)
	command := `if [ -n "${SHELLEY_EXE_SCROLL_FD:-}" ]; then echo inherited-fd-leaked; fi; for i in $(seq 1 200); do printf 'scroll-line-%03d\n' "$i"; done; read -r answer; printf 'answer=%s\n' "$answer"`
	sess, first, err := ts.Spawn(command, t.TempDir(), "conv-1", 80, 24, []string{"SHELLEY_EXE_SCROLL_HELPER=1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		first.Close()
		_ = ts.Kill(sess.ID)
	})
	if sess.Engine != terminalEngineExeScroll {
		t.Fatalf("engine = %q, want %q", sess.Engine, terminalEngineExeScroll)
	}
	initialOutput := readTerminalUntil(t, first, "scroll-line-200", 10*time.Second)
	if bytes.Contains(initialOutput, []byte("inherited-fd-leaked")) {
		t.Fatal("internal shared-memfd descriptor leaked into the terminal command environment")
	}
	if err := first.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}

	reloaded, err := NewTerminalSessions(ts.dir, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	restored := reloaded.Get(sess.ID)
	if restored == nil {
		t.Fatal("exe-scroll session was not restored")
	}
	second, err := reloaded.Attach(restored, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	snapshot := readTerminalUntil(t, second, "scroll-line-200", 10*time.Second)
	if !bytes.Contains(snapshot, []byte("scroll-line-001")) {
		t.Fatalf("reattach snapshot lost old scrollback; tail=%q", tail(snapshot, 1000))
	}
	if err := second.SendInput([]byte("done\n")); err != nil {
		t.Fatal(err)
	}
	output := readTerminalUntil(t, second, "answer=done", 10*time.Second)
	if !bytes.Contains(output, []byte("answer=done")) {
		t.Fatalf("missing command output: %q", output)
	}
	message, err := recvTerminalKind(t, second, terminalExit, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if message.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", message.exitCode)
	}
}

func TestExeScrollPreservesNonzeroExitStatus(t *testing.T) {
	ts := newExeScrollTestSessions(t)
	sess, client, err := ts.Spawn("exit 42", t.TempDir(), "", 80, 24, []string{"SHELLEY_EXE_SCROLL_HELPER=1"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer ts.Forget(sess.ID)
	message, err := recvTerminalKind(t, client, terminalExit, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if message.exitCode != 42 {
		t.Fatalf("exit code = %d, want 42", message.exitCode)
	}
}

func TestExeScrollSpawnFailureTerminatesSessionServer(t *testing.T) {
	ts := newExeScrollTestSessions(t)
	var serverPID int
	ts.waitForPID = func(path string, max time.Duration) (int, error) {
		pid, err := waitForPIDFile(path, max)
		if err != nil {
			return 0, err
		}
		serverPID = pid
		return pid, errors.New("forced PID handoff failure")
	}
	_, client, err := ts.Spawn("read -r _", t.TempDir(), "", 80, 24, []string{"SHELLEY_EXE_SCROLL_HELPER=1"})
	if err == nil {
		if client != nil {
			client.Close()
		}
		t.Fatal("Spawn succeeded despite forced PID handoff failure")
	}
	if serverPID <= 0 {
		t.Fatal("test did not observe the exe-scroll session PID")
	}
	waitForProcessGone(t, serverPID)
}

func TestExeScrollSessionsShareExecutableMapping(t *testing.T) {
	ts := newExeScrollTestSessions(t)
	firstSession, firstClient, err := ts.Spawn("read -r _", t.TempDir(), "", 80, 24, []string{"SHELLEY_EXE_SCROLL_HELPER=1"})
	if err != nil {
		t.Fatal(err)
	}
	defer firstClient.Close()
	defer ts.Kill(firstSession.ID)

	secondSession, secondClient, err := ts.Spawn("read -r _", t.TempDir(), "", 80, 24, []string{"SHELLEY_EXE_SCROLL_HELPER=1"})
	if err != nil {
		t.Fatal(err)
	}
	defer secondClient.Close()
	defer ts.Kill(secondSession.ID)

	firstMapping := exeScrollExecutableMapping(t, firstSession.PID)
	secondMapping := exeScrollExecutableMapping(t, secondSession.PID)
	if firstMapping != secondMapping {
		t.Fatalf("exe-scroll mappings differ: %q != %q", firstMapping, secondMapping)
	}
}

func exeScrollExecutableMapping(t *testing.T, pid int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "maps"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 6 && strings.Contains(line, "memfd:shelley-exe-scroll") && strings.Contains(fields[1], "x") {
			return fields[3] + ":" + fields[4]
		}
	}
	t.Fatalf("pid %d has no executable exe-scroll memfd mapping", pid)
	return ""
}

func TestKillExeScrollStopsSessionServer(t *testing.T) {
	ts := newExeScrollTestSessions(t)
	sess, client, err := ts.Spawn("read -r _", t.TempDir(), "", 80, 24, []string{"SHELLEY_EXE_SCROLL_HELPER=1"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := ts.Kill(sess.ID); err != nil {
		t.Fatal(err)
	}
	waitForProcessGone(t, sess.PID)
	if _, err := os.Stat(sess.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still exists after Kill: %v", err)
	}
}

func waitForProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("process pid %d survived termination", pid)
		case <-ticker.C:
		}
	}
}

func readTerminalUntil(t *testing.T, client terminalClient, marker string, timeout time.Duration) []byte {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		var output bytes.Buffer
		for {
			message, err := client.Recv()
			if err != nil {
				done <- result{data: output.Bytes(), err: err}
				return
			}
			if message.kind == terminalExit {
				done <- result{data: output.Bytes(), err: fmt.Errorf("session exited with %d before %q", message.exitCode, marker)}
				return
			}
			output.Write(message.data)
			if strings.Contains(output.String(), marker) {
				done <- result{data: output.Bytes()}
				return
			}
		}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("read terminal: %v; output=%q", result.err, tail(result.data, 1000))
		}
		return result.data
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %q", marker)
		return nil
	}
}

func recvTerminalKind(t *testing.T, client terminalClient, kind terminalMessageKind, timeout time.Duration) (terminalMessage, error) {
	t.Helper()
	type result struct {
		message terminalMessage
		err     error
	}
	done := make(chan result, 1)
	go func() {
		for {
			message, err := client.Recv()
			if err != nil || message.kind == kind {
				done <- result{message: message, err: err}
				return
			}
		}
	}()
	select {
	case result := <-done:
		return result.message, result.err
	case <-time.After(timeout):
		return terminalMessage{}, fmt.Errorf("timed out waiting for terminal message kind %d", kind)
	}
}

func tail(data []byte, size int) []byte {
	if len(data) <= size {
		return data
	}
	return data[len(data)-size:]
}
