package server

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"shelley.exe.dev/dtach"
)

// TerminalSession is the on-disk + in-memory record of a persistent terminal.
// The owning process is a `shelley dtach new` child detached via setsid so
// it survives the parent shelley exiting; it terminates when the user's
// command exits or the session is killed.
type TerminalSession struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	Cwd       string    `json:"cwd"`
	Socket    string    `json:"socket"`
	LogFile   string    `json:"log_file"`
	PID       int       `json:"pid"`
	CreatedAt time.Time `json:"created_at"`
}

// SpawnerFunc starts a dtach server hosting `cmd` on the given socket. The
// implementation must not return until the socket is ready to accept
// connections. The default spawns an out-of-process `shelley dtach serve`
// child so sessions outlive the parent shelley. Tests can replace it to run
// in-process.
type SpawnerFunc func(socket, logFile, cwd, command string, cols, rows uint16) (pid int, err error)

// TerminalSessions tracks persistent dtach sessions on disk.
type TerminalSessions struct {
	dir      string // root directory holding per-session files
	exe      string // path to the shelley executable (for re-exec)
	logger   *slog.Logger
	spawner  SpawnerFunc
	mu       sync.Mutex
	sessions map[string]*TerminalSession
	// attachMu serializes attachOrSpawn for the duration of socket-stat /
	// spawn so concurrent reconnects for the same id don't double-spawn.
	attachMu sync.Mutex
}

// NewTerminalSessions opens (or creates) a sessions directory and reaps any
// stale records left over from previous runs.
func NewTerminalSessions(dir string, logger *slog.Logger) (*TerminalSessions, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("terminals: mkdir %s: %w", dir, err)
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("terminals: locate shelley executable: %w", err)
	}
	ts := &TerminalSessions{
		dir:      dir,
		exe:      exe,
		logger:   logger,
		sessions: make(map[string]*TerminalSession),
	}
	ts.spawner = ts.spawnSubprocess
	ts.scan()
	return ts, nil
}

// SetSpawner overrides the spawn strategy (intended for tests).
func (t *TerminalSessions) SetSpawner(s SpawnerFunc) { t.spawner = s }

// scan loads sessions from disk, dropping any whose dtach socket is dead.
func (t *TerminalSessions) scan() {
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		id := name[:len(name)-len(".json")]
		data, err := os.ReadFile(filepath.Join(t.dir, name))
		if err != nil {
			continue
		}
		var s TerminalSession
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}
		if !t.socketAlive(s.Socket) {
			t.removeFiles(id)
			continue
		}
		t.sessions[id] = &s
	}
}

func (t *TerminalSessions) socketAlive(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (t *TerminalSessions) removeFiles(id string) {
	os.Remove(filepath.Join(t.dir, id+".json"))
	os.Remove(filepath.Join(t.dir, id+".sock"))
	os.Remove(filepath.Join(t.dir, id+".log"))
}

// List returns a snapshot of known live sessions, oldest first.
func (t *TerminalSessions) List() []*TerminalSession {
	t.mu.Lock()
	out := make([]*TerminalSession, 0, len(t.sessions))
	for _, s := range t.sessions {
		out = append(out, s)
	}
	t.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Get returns a session by ID, or nil.
func (t *TerminalSessions) Get(id string) *TerminalSession {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessions[id]
}

// Spawn launches a new dtach-backed session, immediately attaches to it, and
// returns the session record together with the attached client. Doing the
// attach inline closes the race where a fast-exiting command tears down the
// socket before any external attach can succeed.
func (t *TerminalSessions) Spawn(command, cwd string, cols, rows uint16) (*TerminalSession, *dtach.Client, error) {
	if command == "" {
		return nil, nil, errors.New("terminals: empty command")
	}
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		} else {
			cwd = "/"
		}
	}
	id, err := newTerminalID()
	if err != nil {
		return nil, nil, err
	}
	socket := filepath.Join(t.dir, id+".sock")
	logFile := filepath.Join(t.dir, id+".log")

	pid, err := t.spawner(socket, logFile, cwd, command, cols, rows)
	if err != nil {
		return nil, nil, err
	}

	// Attach immediately to pin the session open: while at least one client
	// is connected, Serve will not tear down even if the command exits quickly.
	dc, attachErr := attachWithRetry(socket, 3*time.Second)
	if attachErr != nil {
		return nil, nil, fmt.Errorf("terminals: attach freshly spawned session: %w", attachErr)
	}

	sess := &TerminalSession{
		ID:        id,
		Command:   command,
		Cwd:       cwd,
		Socket:    socket,
		LogFile:   logFile,
		PID:       pid,
		CreatedAt: time.Now().UTC(),
	}

	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		dc.Close()
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(t.dir, id+".json"), data, 0o600); err != nil {
		dc.Close()
		return nil, nil, err
	}

	t.mu.Lock()
	t.sessions[id] = sess
	t.mu.Unlock()
	t.logger.Info("spawned persistent terminal", "id", id, "command", command, "cwd", cwd, "pid", sess.PID)
	return sess, dc, nil
}

// attachWithRetry repeatedly tries to dial the dtach socket for up to the
// given deadline. Sub-process spawns can race ahead of accept(), so a brief
// retry loop avoids spurious failures.
func attachWithRetry(socket string, max time.Duration) (*dtach.Client, error) {
	deadline := time.Now().Add(max)
	var lastErr error
	for {
		dc, err := dtach.Attach(socket)
		if err == nil {
			return dc, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, lastErr
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Kill terminates the session (SIGTERM the dtach server group) and removes
// its on-disk files.
func (t *TerminalSessions) Kill(id string) error {
	t.mu.Lock()
	s := t.sessions[id]
	if s != nil {
		delete(t.sessions, id)
	}
	t.mu.Unlock()
	if s == nil {
		return nil
	}
	if s.PID > 0 {
		// Signal the process group (dtach + child shell + descendants).
		_ = syscall.Kill(-s.PID, syscall.SIGTERM)
	}
	t.removeFiles(id)
	return nil
}

// Forget drops a session from memory and disk without signalling it; intended
// for cleanup after the underlying socket is observed dead.
func (t *TerminalSessions) Forget(id string) {
	t.mu.Lock()
	delete(t.sessions, id)
	t.mu.Unlock()
	t.removeFiles(id)
}

func newTerminalID() (string, error) {
	var b [9]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return "t" + string(out), nil
}

// spawnSubprocess starts `shelley dtach new` as an out-of-process child so
// it survives shelley restarts (Setsid + Release).
func (t *TerminalSessions) spawnSubprocess(socket, logFile, cwd, command string, cols, rows uint16) (int, error) {
	logF, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("terminals: open log: %w", err)
	}
	defer logF.Close()

	args := []string{
		"dtach", "new",
		"-s", socket,
		"-cwd", cwd,
		"-cols", fmt.Sprintf("%d", cols),
		"-rows", fmt.Sprintf("%d", rows),
		"--",
		"bash", "--login", "-c", command,
	}
	cmd := exec.Command(t.exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("terminals: start dtach: %w", err)
	}
	// Waiting for the listener to come up is the caller's job (attachWithRetry).
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

// InProcessSpawner runs the dtach server in a goroutine inside the current
// process. Sessions die when this process exits. Intended for tests; blocks
// until the listener is ready.
func InProcessSpawner(socket, logFile, cwd, command string, cols, rows uint16) (int, error) {
	ready := make(chan struct{})
	go func() {
		_ = dtach.Serve(dtach.ServerOptions{
			SocketPath: socket,
			Command:    "bash",
			Args:       []string{"--login", "-c", command},
			Dir:        cwd,
			Cols:       cols,
			Rows:       rows,
			Ready:      ready,
		})
	}()
	<-ready
	return os.Getpid(), nil
}

// LockAttach returns a function that releases the attach mutex. Callers use
// it to serialize the lookup-or-spawn path.
func (t *TerminalSessions) LockAttach() func() {
	t.attachMu.Lock()
	return t.attachMu.Unlock
}
