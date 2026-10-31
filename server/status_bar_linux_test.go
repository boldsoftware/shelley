package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// A failed hook's children must not outlive it, and one still holding stdout
// must not hold up the response.
func TestStatusBarHookKillsChildrenOnFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, child, then, wantErr string }{
		{"holding stdout", "sleep 60", "exit 0", "a background process kept its output open"},
		{"failed", "sleep 60 >/dev/null 2>&1", "exit 1", "exit status 1"},
		{"failed holding stdout", "sleep 60", "exit 1", "exit status 1"},
		{"too long", "sleep 60 >/dev/null 2>&1", "head -c 5000 /dev/zero", "output exceeds 4096 bytes"},
		{"invalid output", "sleep 60 >/dev/null 2>&1", "echo not-json", "invalid JSON output: invalid character 'o' in literal null (expecting 'u')"},
		{"hung", "sleep 60 >/dev/null 2>&1", "exec sleep 60", "timed out after 2s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeStatusBarHook(t, dir, tc.child+` & echo $! > child; `+tc.then)
			if got, err := runStatusBarHookIn(t.Context(), dir, StatusBarHookInput{Cwd: dir}); !reflect.DeepEqual(got, StatusBar{}) || fmt.Sprint(err) != tc.wantErr {
				t.Fatalf("got %+v, %v; want %s", got, err, tc.wantErr)
			}
			waitForExit(t, filepath.Join(dir, "child"))
		})
	}
}

// A client going away stops the hook and its children with SIGTERM first, so
// a git among them can remove its index.lock, and then kills whatever ignored
// that.
func TestStatusBarHookCancel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, fifo := range []string{"ignoring", "started"} {
		if err := unix.Mkfifo(filepath.Join(dir, fifo), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Each child says when it has set its trap; the last one, everything.
	writeStatusBarHook(t, dir, `trap 'rm lock; exit 1' TERM
touch lock child-lock
sh -c 'trap "" TERM; echo > ignoring; exec sleep 60' >/dev/null 2>&1 &
echo $! > child
read _ < ignoring
sh -c 'trap "rm child-lock; exit 0" TERM; echo > started; sleep 60 & wait' >/dev/null 2>&1 &
wait`)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error)
	go func() {
		_, err := runStatusBarHookIn(ctx, dir, StatusBarHookInput{Cwd: dir})
		done <- err
	}()
	// Reading the FIFO blocks until something writes to it. A hook that ends
	// first (timed out on a loaded machine, say) fails the test, not hangs it.
	started := make(chan error, 1)
	go func() {
		_, err := os.ReadFile(filepath.Join(dir, "started"))
		started <- err
	}()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-done:
		t.Fatalf("hook ended before it started: %v", err)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled hook succeeded")
	}
	for _, lock := range []string{"lock", "child-lock"} {
		if _, err := os.Stat(filepath.Join(dir, lock)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: no cleanup on SIGTERM: %v", lock, err)
		}
	}
	waitForExit(t, filepath.Join(dir, "child"))
}

// waitForExit waits for the process whose pid is in pidFile to exit, up to a
// deadline: SIGKILL is asynchronous.
func waitForExit(t *testing.T, pidFile string) {
	t.Helper()
	out, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 10_000)
	for errors.Is(err, unix.EINTR) { // poll is never restarted after a signal
		n, err = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 10_000)
	}
	if n != 1 {
		t.Fatalf("process %d still running: %v", pid, err)
	}
}
