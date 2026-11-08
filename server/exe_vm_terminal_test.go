package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"shelley.exe.dev/exeenv"
)

// exeVMTerminalFixture serves the workspace routes, with VMs listed by a
// mocked exe.dev integration "a" and a fake ssh: it records its arguments,
// fails as ssh does for hosts named denied.* and down.*, and is otherwise a
// line-echoing shell reporting its terminal size, which says "gone" into
// the dir's fifo when it ends.
func exeVMTerminalFixture(t *testing.T) (dir, base string) {
	t.Helper()
	s, _, _, _ := newActionFixture(t, "true", false)
	enableDevProdTabs(t, s)
	dir = t.TempDir()
	s.toolSetConfig.DBPath = filepath.Join(dir, "shelley.db")
	s.conversationVMs.hosts = map[string]string{"a": "a.int.exe.test"}
	s.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return exeenv.Environment{}, []reflectionIntegration{{Name: "a", Type: "exe-dev"}}, nil
	}
	s.conversationVMs.exe = func(context.Context, string, string) ([]byte, error) {
		vm := func(name, status string, shell bool) string {
			return `{"vm_name":"` + name + `","status":"` + status + `","ssh_dest":"` + name + `.exe.test","access":{"shell":` + map[bool]string{true: "true", false: "false"}[shell] + `}}`
		}
		return []byte(`{"vms":[` + strings.Join([]string{
			vm("web", "running", true), vm("off", "stopped", true), vm("noshell", "running", false),
			vm("denied", "running", true), vm("down", "running", true),
		}, ",") + `]}`), nil
	}
	script := `#!/bin/bash
dir=` + shellJoin([]string{dir}) + `
echo "$*" >"$dir/args"
while [ $# -gt 1 ]; do case $1 in -E) log=$2; shift 2;; -o|-F) shift 2;; *) shift;; esac; done
case $1 in
denied.*) echo "exedev@$1: Permission denied (publickey)." >>"$log"; exit 255;;
down.*) echo "ssh: connect to host $1 port 22: Connection timed out" >>"$log"; exit 255;;
esac
trap 'exit 0' HUP
trap 'echo gone >"$dir/fifo"' EXIT
while read -r line; do echo "got $line $(stty size)"; done
`
	s.conversationVMs.ssh = filepath.Join(dir, "ssh")
	if err := os.WriteFile(s.conversationVMs.ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := http.NewServeMux()
	s.registerWorkspaceRoutes(api)
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	return dir, "ws" + strings.TrimPrefix(server.URL, "http") + "/api/exe-vms/"
}

func dialExeVMTerminal(t *testing.T, ctx context.Context, url string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Write(ctx, conn, ExecMessage{Type: "init", Cols: 100, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestExeVMTerminal(t *testing.T) {
	t.Parallel()
	dir, base := exeVMTerminalFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn := dialExeVMTerminal(t, ctx, base+"web/terminal")
	defer conn.CloseNow()
	output := ""
	expect := func(want string) {
		t.Helper()
		for !strings.Contains(output, want) {
			var msg ExecMessage
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				t.Fatalf("waiting for %q in %q: %v", want, output, err)
			}
			if msg.Type != "output" {
				t.Fatalf("message %+v, want output", msg)
			}
			data, err := base64.StdEncoding.DecodeString(msg.Data)
			if err != nil {
				t.Fatal(err)
			}
			output += string(data)
		}
	}
	for _, msg := range []ExecMessage{{Type: "input", Data: "hello\r"}} {
		if err := wsjson.Write(ctx, conn, msg); err != nil {
			t.Fatal(err)
		}
	}
	expect("got hello 30 100")
	for _, msg := range []ExecMessage{{Type: "resize", Cols: 120, Rows: 40}, {Type: "input", Data: "again\r"}} {
		if err := wsjson.Write(ctx, conn, msg); err != nil {
			t.Fatal(err)
		}
	}
	expect("got again 40 120")
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-F none -tt ", "IdentityAgent=" + filepath.Join(dir, "vm-ssh-"), "a.agent", "web.known_hosts", "BatchMode=yes", " web.exe.test\n"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("ssh args %q lack %q", args, want)
		}
	}

	// Closing the terminal hangs up ssh.
	conn.Close(websocket.StatusNormalClosure, "")
	gone := make(chan string, 1)
	go func() {
		data, err := os.ReadFile(filepath.Join(dir, "fifo"))
		if err != nil {
			t.Error(err)
		}
		gone <- string(data)
	}()
	select {
	case got := <-gone:
		if got != "gone\n" {
			t.Errorf("fifo said %q", got)
		}
	case <-ctx.Done():
		t.Fatal("ssh still running after the terminal closed")
	}
}

func TestExeVMTerminalFailures(t *testing.T) {
	t.Parallel()
	_, base := exeVMTerminalFixture(t)
	for name, want := range map[string]string{
		"ghost":   "no exe.dev integration of this host lists a VM named ghost",
		"off":     "can't open a terminal on off: it is stopped, not running",
		"noshell": "exe.dev doesn't let the integration a open a shell on noshell",
		"denied":  "exe.dev denied the integration a a shell on denied: exedev@denied.exe.test: Permission denied (publickey).",
		"down":    "couldn't reach down: connect to host down.exe.test port 22: Connection timed out",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			conn := dialExeVMTerminal(t, ctx, base+name+"/terminal")
			defer conn.CloseNow()
			for {
				var msg ExecMessage
				if err := wsjson.Read(ctx, conn, &msg); err != nil {
					t.Fatalf("no error message: %v", err)
				}
				if msg.Type == "output" {
					continue
				}
				if msg.Type != "error" || msg.Data != want {
					t.Fatalf("got %+v, want error %q", msg, want)
				}
				return
			}
		})
	}
}

func TestExeVMTerminalNeedsTheFlag(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newActionFixture(t, "true", false)
	if code := workspaceAPI(s, "GET", "/api/exe-vms/web/terminal", "").Code; code != http.StatusNotFound {
		t.Errorf("status %d", code)
	}
}
