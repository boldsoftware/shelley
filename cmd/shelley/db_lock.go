package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// lockServerDatabase excludes other participating servers, not SQLite readers
// or writers. The caller must close the database before closing the returned
// file. Never unlink the sidecar: its inode must remain stable across owners.
func lockServerDatabase(dsn string) (*os.File, error) {
	filename, _, _ := strings.Cut(dsn, "?")
	if strings.HasPrefix(dsn, "file:") {
		u, err := url.Parse(dsn)
		if err != nil {
			return nil, err
		}
		if u.Host != "" && u.Host != "localhost" || u.User != nil {
			return nil, fmt.Errorf("invalid SQLite file URI authority: %q", u.Host)
		}
		filename = u.Path
		if u.Opaque != "" {
			filename, err = url.PathUnescape(u.Opaque)
			if err != nil {
				return nil, err
			}
		}
		if u.Query().Get("mode") == "memory" {
			return nil, fmt.Errorf("database must be backed by a filesystem file")
		}
	} else if filename != "" && filename != ":memory:" {
		// Match db.New's directory creation for plain filenames only.
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			return nil, err
		}
	}
	if filename == "" || filename == ":memory:" {
		return nil, fmt.Errorf("database must be backed by a filesystem file")
	}
	filename, err := canonicalDatabasePath(filename)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}

	// os.OpenFile sets close-on-exec, so tools cannot inherit ownership.
	lock, err := os.OpenFile(filename+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open database ownership lock: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("another Shelley server already owns database %q: %w", filename, err)
		}
		return nil, fmt.Errorf("lock database %q: %w", filename, err)
	}
	return lock, nil
}

// Resolve aliases before SQLite opens, including a missing final component.
func canonicalDatabasePath(filename string) (string, error) {
	path, err := filepath.EvalSymlinks(filename)
	if err == nil {
		return filepath.Abs(path)
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	// Split, rather than Dir/Clean: a symlink followed by ".." must be
	// resolved by the filesystem before any lexical path cleaning.
	dir, base := filepath.Split(filename)
	if target, err := os.Readlink(filename); err == nil {
		// A dangling symlink can name a database that has not been created yet.
		if !filepath.IsAbs(target) {
			target = dir + target
		}
		return canonicalDatabasePath(target)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if dir == "" {
		dir = "."
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(dir, base))
}
