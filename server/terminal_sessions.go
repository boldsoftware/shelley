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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"shelley.exe.dev/dtach"
	"shelley.exe.dev/exescroll"
)

const (
	terminalEngineDtach     = "dtach"
	terminalEngineExeScroll = "exe-scroll"
)

// TerminalSession is the on-disk + in-memory record of a persistent terminal.
// New records are owned by an exe-scroll session server; records without an
// engine field are legacy dtach sessions and remain attachable.
type TerminalSession struct {
	ID      string `json:"id"`
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	Socket  string `json:"socket"`
	LogFile string `json:"log_file"`
	PID     int    `json:"pid"`
	// Engine is omitted on records created by Shelley versions that only used
	// dtach. An empty value therefore means dtach for compatibility.
	Engine string `json:"engine,omitempty"`
	// ConversationID is the conversation that owns this terminal. Empty means
	// global: the terminal is visible in every conversation. Records written
	// before scoping existed unmarshal with an empty value and so read as
	// global. On the wire this is represented as null (see terminalDTO); the
	// "" <-> null conversion happens only in terminalDTO and in the scope
	// handler.
	ConversationID string    `json:"conversation_id"`
	CreatedAt      time.Time `json:"created_at"`
}

// SpawnerFunc starts the legacy dtach server used by tests and compatibility
// sessions. Production sessions use the embedded exe-scroll binary.
type SpawnerFunc func(socket, logFile, cwd, command string, cols, rows uint16, extraEnv []string) (pid int, err error)

// TerminalSessions tracks persistent terminal sessions on disk.
type TerminalSessions struct {
	dir              string // root directory holding per-session files
	exe              string // path to the shelley executable (for re-exec)
	logger           *slog.Logger
	spawner          SpawnerFunc
	exeScrollCommand func(args ...string) *exec.Cmd
	waitForPID       func(path string, max time.Duration) (int, error)
	mu               sync.Mutex
	sessions         map[string]*TerminalSession
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
	ts.exeScrollCommand = func(args ...string) *exec.Cmd {
		return exec.Command(ts.exe, append([]string{"exe-scroll"}, args...)...)
	}
	ts.waitForPID = waitForPIDFile
	ts.scan()
	return ts, nil
}

// SetSpawner overrides the spawn strategy (intended for tests).
func (t *TerminalSessions) SetSpawner(s SpawnerFunc) { t.spawner = s }

// scan loads sessions from disk, dropping any whose socket is dead.
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
	t.removeFilesExceptExit(id)
	os.Remove(t.exitFile(id))
}

func (t *TerminalSessions) removeFilesExceptExit(id string) {
	os.Remove(filepath.Join(t.dir, id+".json"))
	os.Remove(filepath.Join(t.dir, id+".sock"))
	os.Remove(filepath.Join(t.dir, id+".log"))
	os.Remove(t.serverPIDFile(id))
}

func (t *TerminalSessions) serverPIDFile(id string) string {
	return filepath.Join(t.dir, id+".pid")
}

func (t *TerminalSessions) exitFile(id string) string {
	return filepath.Join(t.dir, id+".exit")
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

// Spawn launches a new persistent session and immediately attaches to it.
// Production sessions use exe-scroll; tests can set a legacy dtach spawner.
func (t *TerminalSessions) Spawn(command, cwd, conversationID string, cols, rows uint16, extraEnv []string) (*TerminalSession, terminalClient, error) {
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
	env := append([]string(nil), extraEnv...)
	env = append(env, "SHELLEY_TERMINAL_ID="+id)
	if t.spawner != nil {
		return t.spawnDtach(id, command, cwd, conversationID, cols, rows, env)
	}
	return t.spawnExeScroll(id, command, cwd, conversationID, cols, rows, env)
}

func (t *TerminalSessions) spawnDtach(id, command, cwd, conversationID string, cols, rows uint16, env []string) (*TerminalSession, terminalClient, error) {
	socket := filepath.Join(t.dir, id+".sock")
	logFile := filepath.Join(t.dir, id+".log")
	pid, err := t.spawner(socket, logFile, cwd, command, cols, rows, env)
	if err != nil {
		return nil, nil, err
	}
	dc, err := attachWithRetry(socket, 3*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("terminals: attach freshly spawned dtach session: %w", err)
	}
	client := &dtachTerminalClient{client: dc}
	sess := &TerminalSession{
		ID:             id,
		Command:        command,
		Cwd:            cwd,
		Socket:         socket,
		LogFile:        logFile,
		PID:            pid,
		ConversationID: conversationID,
		CreatedAt:      time.Now().UTC(),
	}
	if err := t.writeSession(sess); err != nil {
		client.Close()
		return nil, nil, err
	}
	t.logger.Info("spawned persistent terminal", "engine", terminalEngineDtach, "id", id, "command", command, "cwd", cwd, "pid", pid, "conversation_id", conversationID)
	return sess, client, nil
}

const exeScrollCommandWrapper = `
umask 077
pid_tmp="$2.tmp.$$"
printf '%s\n' "$PPID" > "$pid_tmp" || exit 125
mv -f "$pid_tmp" "$2" || exit 125
bash --login -c "$1"
status=$?
exit_tmp="$3.tmp.$$"
printf '%s\n' "$status" > "$exit_tmp" || exit 125
mv -f "$exit_tmp" "$3" || exit 125
exit "$status"
`

func (t *TerminalSessions) spawnExeScroll(id, command, cwd, conversationID string, cols, rows uint16, extraEnv []string) (*TerminalSession, terminalClient, error) {
	socket := filepath.Join(t.dir, id+".sock")
	pidFile := t.serverPIDFile(id)
	exitFile := t.exitFile(id)
	cmd := t.exeScrollCommand(
		socket,
		"--",
		"sh", "-c", exeScrollCommandWrapper,
		"shelley-exe-scroll", command, pidFile, exitFile,
	)
	cmd.Dir = cwd
	cmd.Env = terminalEnvironment(extraEnv)
	if err := exescroll.ConfigureCommand(cmd); err != nil {
		return nil, nil, fmt.Errorf("terminals: share embedded exe-scroll: %w", err)
	}
	client, err := newExeScrollPTYClient(cmd, cols, rows, exitFile)
	if err != nil {
		return nil, nil, err
	}
	pid, err := t.waitForPID(pidFile, 3*time.Second)
	if err != nil {
		cleanupErr := t.terminateSpawnedExeScroll(socket, pid)
		client.Close()
		if cleanupErr == nil {
			t.removeFiles(id)
		}
		return nil, nil, errors.Join(
			fmt.Errorf("terminals: start exe-scroll session: %w", err),
			cleanupErr,
		)
	}
	sess := &TerminalSession{
		ID:             id,
		Command:        command,
		Cwd:            cwd,
		Socket:         socket,
		LogFile:        filepath.Join(t.dir, id+".log"),
		PID:            pid,
		Engine:         terminalEngineExeScroll,
		ConversationID: conversationID,
		CreatedAt:      time.Now().UTC(),
	}
	if err := t.writeSession(sess); err != nil {
		cleanupErr := t.killExeScroll(sess)
		client.Close()
		if cleanupErr == nil {
			t.removeFiles(id)
		}
		return nil, nil, errors.Join(err, cleanupErr)
	}
	t.logger.Info("spawned persistent terminal", "engine", terminalEngineExeScroll, "id", id, "command", command, "cwd", cwd, "pid", pid, "conversation_id", conversationID)
	return sess, client, nil
}

func terminalEnvironment(extra []string) []string {
	env := append(os.Environ(), extra...)
	out := env[:0]
	for _, value := range env {
		if strings.HasPrefix(value, "TERM=") || strings.HasPrefix(value, "COLORTERM=") {
			continue
		}
		out = append(out, value)
	}
	return append(out, "TERM=xterm-256color", "COLORTERM=truecolor")
}

func waitForPIDFile(path string, max time.Duration) (int, error) {
	deadline := time.Now().Add(max)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil || pid <= 0 {
				return 0, fmt.Errorf("invalid server pid %q", strings.TrimSpace(string(data)))
			}
			return pid, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("timed out waiting for server pid")
		}
		<-ticker.C
	}
}

// writeSession persists a session record and then publishes it in memory. The
// JSON is written to a temp file in the sessions dir and renamed over the
// record so a crash mid-write can never leave a truncated file, and the
// in-memory map is only updated once the durable write succeeded.
func (t *TerminalSessions) writeSession(sess *TerminalSession) error {
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(t.dir, sess.ID+".json.*")
	if err != nil {
		return fmt.Errorf("terminals: create temp record: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("terminals: write temp record: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("terminals: chmod temp record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("terminals: close temp record: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(t.dir, sess.ID+".json")); err != nil {
		return fmt.Errorf("terminals: rename record: %w", err)
	}

	t.mu.Lock()
	t.sessions[sess.ID] = sess
	t.mu.Unlock()
	return nil
}

// ErrNoSuchTerminal reports that a terminal id is not known.
var ErrNoSuchTerminal = errors.New("terminals: unknown terminal id")

// SetConversationID re-scopes a terminal. An empty conversationID makes the
// terminal global. Returns the updated record.
func (t *TerminalSessions) SetConversationID(id, conversationID string) (*TerminalSession, error) {
	t.mu.Lock()
	cur := t.sessions[id]
	t.mu.Unlock()
	if cur == nil {
		return nil, ErrNoSuchTerminal
	}
	// Copy so a failed write leaves the in-memory record untouched.
	updated := *cur
	updated.ConversationID = conversationID
	if err := t.writeSession(&updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

// GlobalizeConversation re-scopes every terminal owned by conversationID to
// global. Used when a conversation is permanently deleted so its terminals
// remain reachable rather than pointing at a conversation that no longer
// exists.
func (t *TerminalSessions) GlobalizeConversation(conversationID string) error {
	if conversationID == "" {
		return nil
	}
	for _, s := range t.List() {
		if s.ConversationID != conversationID {
			continue
		}
		if _, err := t.SetConversationID(s.ID, ""); err != nil && !errors.Is(err, ErrNoSuchTerminal) {
			return err
		}
	}
	return nil
}

func (t *TerminalSessions) Attach(sess *TerminalSession, cols, rows uint16) (terminalClient, error) {
	switch sess.Engine {
	case "", terminalEngineDtach:
		client, err := dtach.Attach(sess.Socket)
		if err != nil {
			return nil, err
		}
		return &dtachTerminalClient{client: client}, nil
	case terminalEngineExeScroll:
		client, err := exescroll.Attach(sess.Socket, t.exitFile(sess.ID), cols, rows)
		if err != nil {
			return nil, err
		}
		return &exeScrollTerminalClient{client: client}, nil
	default:
		return nil, fmt.Errorf("terminals: unknown engine %q", sess.Engine)
	}
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

// Kill terminates the session and removes its on-disk files.
func (t *TerminalSessions) Kill(id string) error {
	t.mu.Lock()
	s := t.sessions[id]
	t.mu.Unlock()
	if s == nil {
		return nil
	}
	var err error
	switch s.Engine {
	case terminalEngineExeScroll:
		err = t.killExeScroll(s)
	case "", terminalEngineDtach:
		if s.PID > 0 {
			// Legacy dtach owns a process group containing the server and command.
			err = syscall.Kill(-s.PID, syscall.SIGTERM)
			if errors.Is(err, syscall.ESRCH) {
				err = nil
			}
		}
	default:
		err = fmt.Errorf("terminals: unknown engine %q", s.Engine)
	}
	if err != nil {
		return err
	}
	t.mu.Lock()
	if t.sessions[id] == s {
		delete(t.sessions, id)
	}
	t.mu.Unlock()
	t.removeFiles(id)
	return nil
}

func (t *TerminalSessions) terminateSpawnedExeScroll(socket string, pid int) error {
	if pid <= 0 {
		var err error
		pid, err = findExeScrollServerPID(socket)
		if err != nil {
			return fmt.Errorf("terminals: locate failed exe-scroll session: %w", err)
		}
	}
	return t.killExeScroll(&TerminalSession{PID: pid, Socket: socket, Engine: terminalEngineExeScroll})
}

func findExeScrollServerPID(socket string) (int, error) {
	output, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return 0, err
	}
	want := "exe-scroll: session " + socket
	found := 0
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		command := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
		if command != want {
			continue
		}
		if found != 0 {
			return 0, fmt.Errorf("multiple session servers match %q", socket)
		}
		found = pid
	}
	if found == 0 {
		return 0, fmt.Errorf("no session server matches %q", socket)
	}
	return found, nil
}

func (t *TerminalSessions) killExeScroll(sess *TerminalSession) error {
	if sess.PID <= 0 {
		return nil
	}
	if err := syscall.Kill(sess.PID, 0); errors.Is(err, syscall.ESRCH) {
		return nil
	} else if err != nil {
		return err
	}
	want := "exe-scroll: session " + sess.Socket
	command, err := waitForProcessCommand(sess.PID, want, time.Second)
	if err != nil {
		return err
	}
	if command != want {
		return fmt.Errorf("terminals: refusing to signal pid %d with command %q", sess.PID, command)
	}
	if err := syscall.Kill(sess.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func waitForProcessCommand(pid int, want string, max time.Duration) (string, error) {
	deadline := time.Now().Add(max)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var last string
	for {
		command, err := processCommand(pid)
		if err == nil {
			last = command
			if command == want {
				return command, nil
			}
		} else if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return "", err
		}
		if time.Now().After(deadline) {
			return last, nil
		}
		<-ticker.C
	}
}

func processCommand(pid int) (string, error) {
	if data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline")); err == nil {
		return strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", " ")), nil
	}
	output, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", fmt.Errorf("terminals: inspect pid %d: %w", pid, err)
	}
	return strings.TrimSpace(string(output)), nil
}

// Forget drops a finished session from memory and disk without signalling it.
// Keep the tiny exit-status file briefly so concurrent attachments can all
// observe the command's status after the shared socket closes.
func (t *TerminalSessions) Forget(id string) {
	t.mu.Lock()
	delete(t.sessions, id)
	t.mu.Unlock()
	t.removeFilesExceptExit(id)
	exitFile := t.exitFile(id)
	time.AfterFunc(time.Minute, func() { _ = os.Remove(exitFile) })
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
// it survives shelley restarts (Setsid keeps it detached in its own session).
func (t *TerminalSessions) spawnSubprocess(socket, logFile, cwd, command string, cols, rows uint16, extraEnv []string) (int, error) {
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
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	cmd.Stdin = nil
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("terminals: start dtach: %w", err)
	}
	// Waiting for the listener to come up is the caller's job (attachWithRetry).
	// Reap the child in the background so it does not become a zombie when it
	// exits. The PID is captured first so spawning stays non-blocking. If
	// shelley exits, this goroutine dies and the orphaned child reparents to
	// init, which reaps it.
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	return pid, nil
}

// InProcessSpawner runs the dtach server in a goroutine inside the current
// process. Sessions die when this process exits. Intended for tests; blocks
// until the listener is ready.
func InProcessSpawner(socket, logFile, cwd, command string, cols, rows uint16, extraEnv []string) (int, error) {
	ready := make(chan struct{})
	var env []string
	if len(extraEnv) > 0 {
		env = append(os.Environ(), extraEnv...)
	}
	go func() {
		_ = dtach.Serve(dtach.ServerOptions{
			SocketPath: socket,
			Command:    "bash",
			Args:       []string{"--login", "-c", command},
			Dir:        cwd,
			Cols:       cols,
			Rows:       rows,
			Env:        env,
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
