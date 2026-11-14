package dtach

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/josharian/sockpath"
)

func TestRingDropsOldest(t *testing.T) {
	r := newRing(8)
	r.write([]byte("abcd"))
	r.write([]byte("efghij"))
	got := r.snapshot()
	want := []byte("cdefghij")
	if !bytes.Equal(got, want) {
		t.Fatalf("ring: got %q want %q", got, want)
	}
}

func serveInBackground(t *testing.T, opts ServerOptions) <-chan error {
	t.Helper()
	ready := make(chan struct{})
	opts.Ready = ready
	done := make(chan error, 1)
	go func() { done <- Serve(opts) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("serve exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not become ready")
	}
	return done
}

func TestServeAttachSurvivesDetach(t *testing.T) {
	dir := sockpath.TempDir(t)
	sock := filepath.Join(dir, "sock")

	// Use `cat` so the session stays alive until we explicitly signal EOF.
	// A fast-exiting command would race the test: the session can tear down
	// (closing and unlinking the socket) before the test gets a chance to
	// attach.
	serverDone := serveInBackground(t, ServerOptions{
		SocketPath: sock,
		Command:    "cat",
	})

	c, err := Attach(sock)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	if err := c.SendInput([]byte("hello\nworld\n")); err != nil {
		t.Fatalf("send: %v", err)
	}
	// Tell cat to exit (EOF) and let the session shut down naturally.
	if err := c.SendInput([]byte{0x04}); err != nil {
		t.Fatalf("send eof: %v", err)
	}

	var collected bytes.Buffer
	var exitCode int32 = -2
	for {
		mt, p, err := c.Recv()
		if err != nil {
			break
		}
		switch mt {
		case MsgSnapshot, MsgOutput:
			collected.Write(p)
		case MsgExit:
			exitCode, _ = DecodeExit(p)
		}
	}
	c.Close()

	if exitCode != 0 {
		t.Fatalf("exit code = %d want 0", exitCode)
	}
	if !bytes.Contains(collected.Bytes(), []byte("hello")) || !bytes.Contains(collected.Bytes(), []byte("world")) {
		t.Fatalf("missing output, got %q", collected.String())
	}

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not exit")
	}
}

func TestAttachReplaysScrollbackToLateClient(t *testing.T) {
	dir := sockpath.TempDir(t)
	sock := filepath.Join(dir, "sock")

	// Use `cat` so the session stays alive until we send EOF. While running,
	// any output we feed it should appear in the scrollback for late
	// attachers.
	done := serveInBackground(t, ServerOptions{
		SocketPath: sock,
		Command:    "cat",
	})

	c1, err := Attach(sock)
	if err != nil {
		t.Fatalf("attach c1: %v", err)
	}
	if _, _, err := c1.Recv(); err != nil { // initial empty snapshot
		t.Fatalf("recv snap c1: %v", err)
	}
	if err := c1.SendInput([]byte("hello\n")); err != nil {
		t.Fatalf("send: %v", err)
	}
	// Wait until cat has echoed it back — the data is now in the scrollback.
	for {
		mt, p, err := c1.Recv()
		if err != nil {
			t.Fatalf("recv c1: %v", err)
		}
		if mt == MsgOutput && bytes.Contains(p, []byte("hello")) {
			break
		}
	}
	c1.Close()

	c2, err := Attach(sock)
	if err != nil {
		t.Fatalf("attach c2: %v", err)
	}
	mt, snap, err := c2.Recv()
	if err != nil || mt != MsgSnapshot {
		t.Fatalf("recv c2 snapshot: mt=%v err=%v", mt, err)
	}
	if !bytes.Contains(snap, []byte("hello")) {
		t.Fatalf("snapshot missing expected output, got %q", snap)
	}
	// Kill the session by closing cat's stdin. Drain until we see exit.
	if err := c2.SendInput([]byte{0x04}); err != nil {
		t.Fatalf("send eof: %v", err)
	}
	for {
		mt, _, err := c2.Recv()
		if err != nil || mt == MsgExit {
			break
		}
	}
	c2.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit")
	}
}

// The command gets PWD naming Dir as given (not resolved), and Env on top
// of the inherited environment, Env winning.
func TestServeRunsCommandInDirAsGiven(t *testing.T) {
	dir := sockpath.TempDir(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DTACH_TEST_INHERITED", "inherited")
	t.Setenv("TERM", "dumb")
	done := serveInBackground(t, ServerOptions{
		SocketPath: filepath.Join(dir, "sock"),
		Command:    "sh",
		// Wait for input so the session is still there to attach to.
		Args: []string{"-c", `read _; echo "PWD=$PWD $DTACH_TEST_INHERITED $DTACH_TEST_ADDED $TERM"`},
		Dir:  link,
		Env:  []string{"DTACH_TEST_ADDED=added", "TERM=xterm-256color"},
	})
	c, err := Attach(filepath.Join(dir, "sock"))
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer c.Close()
	if err := c.SendInput([]byte("\n")); err != nil {
		t.Fatalf("send: %v", err)
	}
	var collected bytes.Buffer
	for {
		mt, p, err := c.Recv()
		if err != nil || mt == MsgExit {
			break
		}
		collected.Write(p)
	}
	if want := "PWD=" + link + " inherited added xterm-256color"; !bytes.Contains(collected.Bytes(), []byte(want)) {
		t.Fatalf("output %q, want it to contain %q", collected.String(), want)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not exit")
	}
}
