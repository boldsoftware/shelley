package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

// upgradeFixture is a running Shelley v0.1.0 whose release metadata comes
// from a fake server offering v0.2.0 as release.
type upgradeFixture struct {
	t         *testing.T
	dir       string
	installed string // the binary, which is started through link
	link      string
	sock      string
	base      string
	client    *http.Client
	cmd       *exec.Cmd
	exited    chan struct{}
	waitErr   error
	logPath   string
}

// startUpgradable builds and starts Shelley v0.1.0, through a symlink as in a
// Homebrew install, with no service manager. It inherits INVOCATION_ID, as if
// started from a shell inside a systemd service; nothing will restart it.
// prelude runs in the shell that execs Shelley.
func startUpgradable(t *testing.T, release []byte, prelude string) *upgradeFixture {
	f := &upgradeFixture{t: t, dir: t.TempDir(), client: &http.Client{Timeout: 2 * time.Second}}
	asset := fmt.Sprintf("shelley_%s_%s", runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("GET /release.json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"tag_name":      "v0.2.0",
			"download_urls": map[string]string{runtime.GOOS + "_" + runtime.GOARCH: srv.URL + "/" + asset},
			"checksums_url": srv.URL + "/checksums.txt",
		})
	})
	mux.HandleFunc("GET /checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(release), asset)
	})
	mux.HandleFunc("GET /"+asset, func(w http.ResponseWriter, r *http.Request) { w.Write(release) })

	f.installed = filepath.Join(f.dir, "Caskroom", "shelley")
	buildShelley(t, f.installed, "-X shelley.exe.dev/version.Tag=v0.1.0", "-X shelley.exe.dev/server.staticMetadataURL="+srv.URL)
	f.link = filepath.Join(f.dir, "bin", "shelley")
	if err := os.MkdirAll(filepath.Dir(f.link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.installed, f.link); err != nil {
		t.Fatal(err)
	}

	// A fixed port: the restarted process must be able to bind it again.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.base = "http://" + ln.Addr().String()
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	// Unix socket paths are limited to about 100 bytes; t.TempDir's can be longer.
	sockDir, err := os.MkdirTemp("", "shelley-sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	f.sock = filepath.Join(sockDir, "s.sock")

	var env []string
	for _, e := range os.Environ() {
		name, _, _ := strings.Cut(e, "=")
		switch name {
		case "SHELLEY_SKIP_VERSION_CHECK", "SHELLEY_VERSION_OVERRIDE", "SHELLEY_CUSTOMIZED_OVERRIDE", "HOME", "XDG_CONFIG_HOME":
			continue
		}
		env = append(env, e)
	}
	env = append(env, "HOME="+f.dir, "INVOCATION_ID=inherited")

	f.logPath = filepath.Join(f.dir, "shelley.log")
	logFile, err := os.Create(f.logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	// sh execs Shelley, so the PID is Shelley's.
	f.cmd = exec.Command("sh", "-c", prelude+`exec "$0" "$@"`, f.link, "--predictable-only",
		"--db", filepath.Join(f.dir, "shelley.db"), "serve", "--port", fmt.Sprint(port), "--socket", f.sock)
	f.cmd.Env = env
	f.cmd.Stdout = logFile
	f.cmd.Stderr = logFile
	if err := f.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f.exited = make(chan struct{})
	go func() { f.waitErr = f.cmd.Wait(); close(f.exited) }()
	t.Cleanup(func() {
		f.cmd.Process.Kill()
		<-f.exited
	})
	f.waitForTag("v0.1.0")
	return f
}

func buildShelley(t *testing.T, out string, ldflags ...string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-ldflags", strings.Join(ldflags, " "), "-o", out, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
}

func (f *upgradeFixture) output() string {
	b, _ := os.ReadFile(f.logPath)
	return string(b)
}

// alive fails the test if Shelley has exited.
func (f *upgradeFixture) alive() {
	f.t.Helper()
	select {
	case <-f.exited:
		f.t.Fatalf("shelley exited (%v)\n%s", f.waitErr, f.output())
	default:
	}
}

func (f *upgradeFixture) waitForTag(want string) {
	f.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		f.alive()
		var got struct {
			Tag string `json:"tag"`
		}
		if resp, err := f.client.Get(f.base + "/version"); err == nil {
			json.NewDecoder(resp.Body).Decode(&got)
			resp.Body.Close()
		}
		if got.Tag == want {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("timed out waiting for %s (last saw %q)\n%s", want, got.Tag, f.output())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (f *upgradeFixture) upgrade() {
	f.t.Helper()
	resp, err := f.client.Post(f.base+"/upgrade?restart=true", "", nil)
	if err != nil {
		f.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		f.t.Fatalf("upgrade: status %d\n%s", resp.StatusCode, f.output())
	}
}

// TestUpgradeRestartsInPlace upgrades a running Shelley that has no service
// manager (as on a Mac, or in a terminal) and checks that it comes back on its
// own, as the same process, running the new binary and serving its TCP port
// and Unix socket again. The upgrade must follow the Homebrew-style symlink
// (on Linux, /proc/self/exe already resolves it; macOS keeps the symlink). A
// background job spans the upgrade and must not linger as a zombie once it
// exits.
func TestUpgradeRestartsInPlace(t *testing.T) {
	newPath := filepath.Join(t.TempDir(), "shelley")
	buildShelley(t, newPath, "-X shelley.exe.dev/version.Tag=v0.2.0")
	newBinary, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	f := startUpgradable(t, newBinary, "")
	dir := f.dir

	// A background job whose wrapper, Shelley's child, waits on a FIFO.
	gate := filepath.Join(dir, "gate")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "job.pid")
	body, _ := json.Marshal(map[string]string{
		"model":   "predictable",
		"cwd":     dir,
		"message": fmt.Sprintf("bash-bg: echo $PPID > %s; read -r _ < %s", pidFile, gate),
	})
	resp, err := f.client.Post(f.base+"/api/conversations/new", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("new conversation: status %d\n%s", resp.StatusCode, f.output())
	}
	var jobPID int
	for deadline := time.Now().Add(30 * time.Second); jobPID == 0; time.Sleep(100 * time.Millisecond) {
		if b, err := os.ReadFile(pidFile); err == nil {
			jobPID, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if time.Now().After(deadline) {
			t.Fatalf("background job did not start\n%s", f.output())
		}
	}
	f.upgrade()
	f.waitForTag("v0.2.0")
	sockClient := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", f.sock)
		},
	}}
	if resp, err := sockClient.Get("http://shelley/version"); err != nil {
		t.Fatalf("unix socket after restart: %v\n%s", err, f.output())
	} else {
		resp.Body.Close()
	}
	f.alive()

	got, err := os.ReadFile(f.installed)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(newBinary) {
		t.Fatal("installed binary is not the new release")
	}
	if fi, err := os.Lstat(f.link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is no longer a symlink: %v", f.link, err)
	}

	// Let the job finish. Its wrapper is still the restarted Shelley's
	// child, and must be reaped: a zombie still answers kill(pid, 0).
	// Without a reader (the job died), a nonblocking open fails rather than hangs.
	fifo, err := os.OpenFile(gate, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("background job did not survive the upgrade: %v\n%s", err, f.output())
	}
	_, err = fifo.WriteString("go\n")
	fifo.Close()
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); syscall.Kill(jobPID, 0) == nil; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("background job %d was never reaped\n%s", jobPID, f.output())
		}
	}
}

// TestUpgradeKeepsServingWhenRestartFails installs a release that cannot run.
// The old binary must keep serving with what it had: its open-files limit
// (checked on Linux), and turns in flight, which a later restart must not
// resume.
func TestUpgradeKeepsServingWhenRestartFails(t *testing.T) {
	f := startUpgradable(t, []byte("not a binary"), "ulimit -Sn 256; ")
	limit := func() string {
		if runtime.GOOS != "linux" {
			return ""
		}
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/limits", f.cmd.Process.Pid))
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.Lines(string(b)) {
			if strings.HasPrefix(line, "Max open files") {
				return line
			}
		}
		t.Fatalf("no open-files limit in\n%s", b)
		return ""
	}
	before := limit()

	f.upgrade()
	for deadline := time.Now().Add(15 * time.Second); !strings.Contains(f.output(), "Failed to restart after upgrade"); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("restart did not fail\n%s", f.output())
		}
	}
	f.waitForTag("v0.1.0")
	if after := limit(); after != before {
		t.Errorf("open-files limit went from\n%s to\n%s", before, after)
	}
	database, err := db.New(db.Config{DSN: filepath.Join(f.dir, "shelley.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		v, err := database.GetSetting(t.Context(), db.ResumeAfterUpgradeSettingKey)
		if err != nil {
			t.Fatal(err)
		}
		if v == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resume after upgrade is still set")
		}
	}
}
