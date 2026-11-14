package db

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"

	"shelley.exe.dev/db/generated"
)

// Closing a database must close every file it opened. The pool keeps its
// connections checked out of database/sql, whose Close skips checked-out
// connections, so each closed test database used to keep its db, -wal and
// -shm files open until the process exited ("too many open files").
func TestCloseReleasesFiles(t *testing.T) {
	fdDir := map[string]string{"linux": "/proc/self/fd", "darwin": "/dev/fd"}[runtime.GOOS]
	if fdDir == "" {
		t.Skipf("no fd listing on %s", runtime.GOOS)
	}
	openFiles := func() int {
		t.Helper()
		entries, err := os.ReadDir(fdDir)
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	openAndClose := func() {
		t.Helper()
		database, cleanup := NewTestDB(t)
		ctx := context.Background()
		conv, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.GetConversationByID(ctx, conv.ConversationID); err != nil {
			t.Fatal(err)
		}
		cleanup()
	}

	openAndClose() // warm up the template snapshot and runtime fds
	before := openFiles()
	for range 5 {
		openAndClose()
	}
	// Only growth is a leak: a finalizer may close an earlier test's fd.
	if after := openFiles(); after > before {
		t.Fatalf("open files: %d before, %d after closing 5 databases", before, after)
	}
}

// Close must not close a connection while an operation uses it: database/sql
// can hand a statement starting on a closing Conn a nil driver connection (a
// SIGSEGV when a finished test's goroutine read its database during Close).
// Close waits for the read; later calls are refused.
func TestCloseWaitsForRunningRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		database, cleanup := NewTestDB(t)
		defer cleanup()
		readStarted, releaseRead := make(chan struct{}), make(chan struct{})
		readDone := make(chan error, 1)
		go func() {
			readDone <- database.Pool().Rx(t.Context(), func(ctx context.Context, rx *Rx) error {
				close(readStarted)
				<-releaseRead
				_, err := generated.New(rx.Conn()).CountConversations(ctx)
				return err
			})
		}()
		<-readStarted

		closeDone := make(chan error, 1)
		go func() { closeDone <- database.Close() }()
		synctest.Wait()
		select {
		case <-closeDone:
			t.Fatal("Close returned while a read was running")
		default:
		}

		close(releaseRead)
		if err := <-readDone; err != nil {
			t.Fatalf("read while Close waited: %v", err)
		}
		if err := <-closeDone; err != nil {
			t.Fatalf("Close: %v", err)
		}
		if _, err := database.GetConversationByID(t.Context(), "c0"); err == nil || !strings.Contains(err.Error(), "database is closing") {
			t.Fatalf("read after Close = %v, want database is closing", err)
		}
	})
}

// A pool call made from inside another while Close waits must fail, not
// wait for Close, which waits for the outer call: that would deadlock.
func TestCloseRefusesNestedCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		database, cleanup := NewTestDB(t)
		defer cleanup()
		outerStarted, closing := make(chan struct{}), make(chan struct{})
		nestedErr := make(chan error, 1)
		go func() {
			_ = database.Pool().Rx(t.Context(), func(ctx context.Context, rx *Rx) error {
				close(outerStarted)
				<-closing
				_, err := database.GetConversationByID(t.Context(), "c0")
				nestedErr <- err
				return err
			})
		}()
		<-outerStarted

		closeDone := make(chan error, 1)
		go func() { closeDone <- database.Close() }()
		synctest.Wait() // Close is waiting for the outer read.
		close(closing)
		if err := <-nestedErr; err == nil || !strings.Contains(err.Error(), "database is closing") {
			t.Fatalf("nested call during Close = %v, want database is closing", err)
		}
		if err := <-closeDone; err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
}
