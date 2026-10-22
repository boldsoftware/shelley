//go:build linux

package server

import (
	"context"
	"encoding/base64"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// dialFullDiskTerminal opens a new terminal on a server whose disk reports no
// free space, and checks that the server says it is unpersisted.
func dialFullDiskTerminal(t *testing.T, ctx context.Context, command string) (*Server, *websocket.Conn) {
	t.Helper()
	s, _, _ := newTestServer(t)
	s.terminals.probe = func(string) (uint64, uint64, error) { return 0, math.MaxUint64, nil }
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/exec-ws?cmd=" + url.QueryEscape(command)
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if err := wsjson.Write(ctx, conn, ExecMessage{Type: "init", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	var msg ExecMessage
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "unpersisted" {
		t.Fatalf("first message %+v, want unpersisted", msg)
	}
	return s, conn
}

// readTerminalOutput reads output messages until the accumulated text matches
// re, failing on anything other than output.
func readTerminalOutput(t *testing.T, ctx context.Context, conn *websocket.Conn, re *regexp.Regexp) string {
	t.Helper()
	var out strings.Builder
	for !re.MatchString(out.String()) {
		var msg ExecMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			t.Fatalf("reading for %s, got %q: %v", re, out.String(), err)
		}
		if msg.Type != "output" {
			t.Fatalf("unexpected %+v while reading for %s; output so far %q", msg, re, out.String())
		}
		data, err := base64.StdEncoding.DecodeString(msg.Data)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(data)
	}
	return out.String()
}

func TestExecTerminal_DiskFullRunsWithoutPersisting(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s, conn := dialFullDiskTerminal(t, ctx, "echo ready; read -r name; echo hello $name; exit 3")

	readTerminalOutput(t, ctx, conn, regexp.MustCompile(`ready`))
	if err := wsjson.Write(ctx, conn, ExecMessage{Type: "input", Data: "disk\n"}); err != nil {
		t.Fatal(err)
	}
	readTerminalOutput(t, ctx, conn, regexp.MustCompile(`hello disk`))
	var msg ExecMessage
	for msg.Type != "exit" {
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type != "output" && msg.Type != "exit" {
			t.Fatalf("unexpected message %+v", msg)
		}
	}
	if msg.Data != "3" {
		t.Errorf("exit = %q, want 3", msg.Data)
	}
	if list := s.terminals.List(); len(list) != 0 {
		t.Errorf("full disk recorded sessions %+v", list)
	}
	if _, err := os.Stat(s.terminals.dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("full disk created %s: %v", s.terminals.dir, err)
	}
}

func TestExecTerminal_DiskFullCloseHangsUp(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// The shell stays in front, so the job only dies if the hangup reaches
	// past the session leader.
	_, conn := dialFullDiskTerminal(t, ctx, "sh -c 'echo pid=$$.; exec sleep 1000'; echo unreachable")

	pidRE := regexp.MustCompile(`pid=(\d+)\.`)
	out := readTerminalOutput(t, ctx, conn, pidRE)
	pid, err := strconv.Atoi(pidRE.FindStringSubmatch(out)[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	waitForProcessGone(t, pid)
}

// Input the command never reads must not keep the websocket loop, which
// notices the tab closing, from hanging up the terminal.
func TestExecTerminal_DiskFullCloseWithUnreadInput(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// Raw mode, so the terminal queues input rather than discarding it.
	_, conn := dialFullDiskTerminal(t, ctx, "stty raw -echo; echo pid=$$.; exec sleep 1000")

	pidRE := regexp.MustCompile(`pid=(\d+)\.`)
	out := readTerminalOutput(t, ctx, conn, pidRE)
	pid, err := strconv.Atoi(pidRE.FindStringSubmatch(out)[1])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := wsjson.Write(ctx, conn, ExecMessage{Type: "input", Data: strings.Repeat("x", 16<<10)}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	waitForProcessGone(t, pid)
}

// A command that ignores SIGHUP survives Close, as under a closed terminal
// window, but must not keep a pending Recv waiting: neither on a PTY it
// holds open, nor on its exit after it let go of the PTY.
func TestDirectPTYCloseInterruptsRecv(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		command string
		// parked lists goroutine states of Recv waiting for the command:
		// on the PTY, or once the PTY is gone, on the command's exit.
		parked []string
	}{
		{"holding the terminal", `trap "" HUP; echo ready; exec sleep 1000`, []string{"IO wait", "syscall"}},
		{"detached", `trap "" HUP; echo ready; exec sleep 1000 </dev/null >/dev/null 2>&1`, []string{"select", "chan receive"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := startDirectPTY(tc.command, t.TempDir(), 80, 24, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = syscall.Kill(client.cmd.Process.Pid, syscall.SIGKILL) })
			readTerminalUntil(t, client, "ready", 5*time.Second)
			done := make(chan error, 1)
			go func() {
				_, err := client.Recv()
				done <- err
			}()
			waitForParkedGoroutine(t, tc.parked, "(*directPTYClient).Recv")
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, os.ErrClosed) {
					t.Fatalf("Recv after Close = %v, want os.ErrClosed", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Close did not interrupt Recv")
			}
		})
	}
}

// Input a surviving command never read must not stay queued after Close.
func TestDirectPTYCloseDropsQueuedInput(t *testing.T) {
	t.Parallel()
	client, err := startDirectPTY(`trap "" HUP; stty raw -echo; echo ready; exec sleep 1000`, t.TempDir(), 80, 24, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(client.cmd.Process.Pid, syscall.SIGKILL) })
	readTerminalUntil(t, client, "ready", 5*time.Second)
	// More than the terminal's input queue holds, so the writer parks.
	if err := client.SendInput(make([]byte, 64<<10)); err != nil {
		t.Fatal(err)
	}
	waitForParkedGoroutine(t, []string{"IO wait"}, "(*directPTYClient).writeInput")
	if err := client.SendInput([]byte("queued")); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.SendInput([]byte("late")); !errors.Is(err, os.ErrClosed) {
		t.Errorf("SendInput after Close = %v, want os.ErrClosed", err)
	}
	client.inputMu.Lock()
	queued := len(client.input)
	client.inputMu.Unlock()
	if queued != 0 {
		t.Errorf("%d inputs still queued after Close", queued)
	}
}

// waitForParkedGoroutine polls until a goroutine is parked within fn in one
// of states.
func waitForParkedGoroutine(t *testing.T, states []string, fn string) {
	t.Helper()
	buf := make([]byte, 1<<20)
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := runtime.Stack(buf, true)
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			for _, state := range states {
				if strings.HasPrefix(g, "goroutine ") && strings.Contains(g, " ["+state) && strings.Contains(g, fn) {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no goroutine parked in %q within %s", states, fn)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTerminalSessionsDiskFull(t *testing.T) {
	t.Parallel()
	ts := newSessionsAt(t, filepath.Join(t.TempDir(), "terminals"))
	if ts.diskFull() {
		t.Fatal("the test machine's disk counts as full")
	}
	var probed string
	for _, tc := range []struct {
		name          string
		bytes, inodes uint64
		want          bool
	}{
		{"roomy", 1 << 30, 1 << 20, false},
		{"no space", terminalMinFreeBytes - 1, 1 << 20, true},
		{"no inodes", 1 << 30, terminalMinFreeInodes - 1, true},
	} {
		ts.probe = func(path string) (uint64, uint64, error) {
			probed = path
			return tc.bytes, tc.inodes, nil
		}
		if got := ts.diskFull(); got != tc.want {
			t.Errorf("%s: diskFull = %t, want %t", tc.name, got, tc.want)
		}
	}
	if want := filepath.Dir(ts.dir); probed != want {
		t.Errorf("before the sessions dir exists, probed %s, want %s", probed, want)
	}
	if err := os.Mkdir(ts.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ts.diskFull()
	if probed != ts.dir {
		t.Errorf("probed %s, want the sessions dir %s", probed, ts.dir)
	}
}
