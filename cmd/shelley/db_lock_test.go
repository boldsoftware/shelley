package main

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"shelley.exe.dev/db"
)

func TestServerDatabaseLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "test.db")
	lock, err := lockServerDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	if second, err := lockServerDatabase(path); !errors.Is(err, unix.EWOULDBLOCK) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("second owner: %v", err)
	}
	other, err := lockServerDatabase(filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	before, err := os.Stat(lock.Name())
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := lockServerDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	after, err := os.Stat(next.Name())
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("sidecar inode changed after release: %v", err)
	}
}

func TestServerDatabaseLockAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spaces # and %.db")
	lock, err := lockServerDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	t.Chdir(dir)
	relative := filepath.Base(path)
	dirLink := filepath.Join(t.TempDir(), "dir-link")
	if err := os.Symlink(dir, dirLink); err != nil {
		t.Fatal(err)
	}
	fileLink := filepath.Join(dir, "file-link")
	if err := os.Symlink(filepath.Base(path), fileLink); err != nil {
		t.Fatal(err)
	}
	dotdotAlias := dirLink + "/../" + filepath.Base(dir) + "/" + filepath.Base(path)
	aliases := []string{
		relative,
		path + "?_busy_timeout=1000",
		(&url.URL{Scheme: "file", Path: path, RawQuery: "mode=rwc"}).String(),
		(&url.URL{Scheme: "file", Host: "localhost", Path: path}).String(),
		"file:" + (&url.URL{Path: relative}).String(),
		filepath.Join(dirLink, filepath.Base(path)),
		dotdotAlias,
		fileLink,
		filepath.Base(fileLink),
	}
	for _, exists := range []bool{false, true} {
		if exists {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, alias := range aliases {
			if second, err := lockServerDatabase(alias); !errors.Is(err, unix.EWOULDBLOCK) {
				if second != nil {
					second.Close()
				}
				t.Fatalf("alias %q (DB exists=%v): %v", alias, exists, err)
			}
		}
	}
	database, err := db.New(db.Config{DSN: dotdotAlias})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if lock.Name() != database.Path()+".lock" {
		t.Fatalf("symlink/.. lock %q does not match SQLite path %q", lock.Name(), database.Path())
	}
}

func TestServerDatabaseLockAllowsReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spaces # and ?.db")
	uri := &url.URL{Scheme: "file", Path: path}
	lock, err := lockServerDatabase(uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	database, err := db.New(db.Config{DSN: uri.String()})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if lock.Name() != database.Path()+".lock" {
		t.Fatalf("lock %q does not match SQLite path %q", lock.Name(), database.Path())
	}
	uri.RawQuery = "mode=ro"
	reader, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var count int
	if err := reader.QueryRowContext(t.Context(), "SELECT count(*) FROM migrations").Scan(&count); err != nil || count == 0 {
		t.Fatalf("read live database: count=%d, err=%v", count, err)
	}
}

func TestServerDatabaseLockErrors(t *testing.T) {
	for _, dsn := range []string{"", ":memory:", "file::memory:?cache=shared", "file:test?mode=memory"} {
		if lock, err := lockServerDatabase(dsn); err == nil {
			lock.Close()
			t.Fatalf("accepted non-file database %q", dsn)
		}
	}
	path := filepath.Join(t.TempDir(), "test.db")
	if err := os.Mkdir(path+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	if lock, err := lockServerDatabase(path); !errors.Is(err, unix.EISDIR) {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("sidecar open error: %v", err)
	}
}

func TestServerDatabaseLockProcess(t *testing.T) {
	const env = "SHELLEY_TEST_DB_LOCK_PATH"
	if path := os.Getenv(env); path != "" {
		runServe(GlobalConfig{DBPath: path, PredictableOnly: true}, []string{"-port", "0", "-socket", "none"})
		return
	}
	path := filepath.Join(t.TempDir(), "test.db")
	lock, err := lockServerDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServerDatabaseLockProcess$")
	cmd.Env = append(os.Environ(), env+"="+path)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "another Shelley server already owns database") {
		t.Fatalf("second serve: %v\n%s", err, output)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("second serve opened SQLite before acquiring ownership: %v", err)
	}
}
