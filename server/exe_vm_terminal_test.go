package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"shelley.exe.dev/exeenv"
)

// exeVMTerminalFixture serves the workspace routes, with VMs listed by a
// mocked exe.dev integration "a" whose host is also every VM's web
// terminal. It mints tokens for web and lapsed, which the terminal
// refuses, and refuses denied; the terminal lists sessions b and a, closes
// only a, and attaches to a line-echoing shell reporting its terminal size,
// which "exit" ends and "crash" fails. A websocket's end goes to gone;
// mints counts the tokens minted.
func exeVMTerminalFixture(t *testing.T) (base string, gone <-chan string, mints *atomic.Int32) {
	t.Helper()
	s, _, _, _ := newActionFixture(t, "true", false)
	enableDevProdTabs(t, s)
	s.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return exeenv.Environment{}, []reflectionIntegration{{Name: "a", Type: "exe-dev"}}, nil
	}
	s.conversationVMs.exe = func(context.Context, string, string) ([]byte, error) {
		vm := func(name, status string, shell bool) string {
			return `{"vm_name":"` + name + `","status":"` + status + `","access":{"shell":` + strconv.FormatBool(shell) + `}}`
		}
		return []byte(`{"vms":[` + strings.Join([]string{
			vm("web", "running", true), vm("off", "stopped", true), vm("noshell", "running", false),
			vm("denied", "running", true), vm("lapsed", "running", true),
		}, ",") + `]}`), nil
	}
	ended := make(chan string, 1)
	mints = new(atomic.Int32)
	exe := http.NewServeMux()
	var exeURL string
	exe.HandleFunc("POST /terminal-token/{vm}", func(w http.ResponseWriter, r *http.Request) {
		switch vm := r.PathValue("vm"); vm {
		case "web", "lapsed":
			mints.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{"url": exeURL, "token": "tok-" + vm, "expires_at": time.Now().Add(5 * time.Minute)})
		default:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("VM %q not found", vm)})
		}
	})
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer tok-web" {
				http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
				return
			}
			h(w, r)
		}
	}
	exe.HandleFunc("GET /terminal/sessions", authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"sessions": []map[string]string{{"name": "b"}, {"name": "a"}}})
	}))
	exe.HandleFunc("POST /terminal/sessions/close", authed(func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("name") != "a" {
			http.Error(w, "Failed to close session: no such luck", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	exe.HandleFunc("GET /terminal/ws/0", authed(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := r.Context()
		size := r.FormValue("rows") + " " + r.FormValue("cols")
		line := r.FormValue("name") + ":"
		for {
			var msg struct {
				Type, Data string
				Cols, Rows int
			}
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				ended <- fmt.Sprint(websocket.CloseStatus(err))
				return
			}
			switch {
			case msg.Type == "resize":
				size = fmt.Sprint(msg.Rows, " ", msg.Cols)
			case msg.Data == "exit\r":
				conn.Close(4001, "shell exited")
				return
			case msg.Data == "crash\r":
				conn.Close(websocket.StatusInternalError, "Failed to create terminal: boom")
				return
			case strings.HasSuffix(msg.Data, "\r"):
				out := "got " + line + strings.TrimSuffix(msg.Data, "\r") + " " + size + "\r\n"
				line = r.FormValue("name") + ":"
				wsjson.Write(ctx, conn, map[string]string{"type": "output", "data": base64.StdEncoding.EncodeToString([]byte(out))})
			default:
				line += msg.Data
			}
		}
	}))
	server := httptest.NewTLSServer(exe)
	t.Cleanup(server.Close)
	exeURL = server.URL
	s.conversationVMs.hosts = map[string]string{"a": server.Listener.Addr().String()}
	s.conversationVMs.http = server.Client()
	api := http.NewServeMux()
	s.registerWorkspaceRoutes(api)
	shelley := httptest.NewServer(api)
	t.Cleanup(shelley.Close)
	return "ws" + strings.TrimPrefix(shelley.URL, "http") + "/api/exe-vms/", ended, mints
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
	base, gone, _ := exeVMTerminalFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn := dialExeVMTerminal(t, ctx, base+"web/terminal?session=dev-1")
	defer conn.CloseNow()
	read := func() ExecMessage {
		t.Helper()
		var msg ExecMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			t.Fatal(err)
		}
		return msg
	}
	send := func(msgs ...ExecMessage) {
		t.Helper()
		for _, msg := range msgs {
			if err := wsjson.Write(ctx, conn, msg); err != nil {
				t.Fatal(err)
			}
		}
	}
	expect := func(want string) {
		t.Helper()
		msg := read()
		data, err := base64.StdEncoding.DecodeString(msg.Data)
		if msg.Type != "output" || err != nil || string(data) != want+"\r\n" {
			t.Fatalf("got %+v (%q), want output %q", msg, data, want)
		}
	}
	send(ExecMessage{Type: "input", Data: "hel"}, ExecMessage{Type: "input", Data: "lo\r"})
	expect("got dev-1:hello 30 100")
	send(ExecMessage{Type: "resize", Cols: 120, Rows: 40}, ExecMessage{Type: "input", Data: "again\r"})
	expect("got dev-1:again 40 120")

	// Closing the terminal detaches from the session.
	conn.Close(websocket.StatusNormalClosure, "")
	select {
	case got := <-gone:
		if got != fmt.Sprint(websocket.StatusNormalClosure) {
			t.Errorf("session's websocket ended with %s", got)
		}
	case <-ctx.Done():
		t.Fatal("session's websocket still open after the terminal closed")
	}

	// The session's end ends the terminal: an exit, or the reason it failed.
	for input, want := range map[string]ExecMessage{
		"exit\r":  {Type: "exit", Data: "0"},
		"crash\r": {Type: "error", Data: "web's terminal: Failed to create terminal: boom"},
	} {
		conn := dialExeVMTerminal(t, ctx, base+"web/terminal?session=dev-1")
		defer conn.CloseNow()
		if err := wsjson.Write(ctx, conn, ExecMessage{Type: "input", Data: input}); err != nil {
			t.Fatal(err)
		}
		var msg ExecMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil || msg != want {
			t.Errorf("%q: got %+v, %v; want %+v", input, msg, err, want)
		}
	}
}

func TestExeVMTerminalFailures(t *testing.T) {
	t.Parallel()
	base, _, _ := exeVMTerminalFixture(t)
	for name, want := range map[string]string{
		"ghost":   "no exe.dev integration of this host lists a VM named ghost",
		"off":     "can't open a terminal on off: it is stopped, not running",
		"noshell": "exe.dev doesn't let the integration a open a shell on noshell",
		"denied":  `exe.dev denied the integration a a terminal on denied: VM "denied" not found`,
		"lapsed":  "couldn't open lapsed's terminal: 307 Temporary Redirect",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			conn := dialExeVMTerminal(t, ctx, base+name+"/terminal?session=s")
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

func TestExeVMTerminalSessions(t *testing.T) {
	t.Parallel()
	base, _, _ := exeVMTerminalFixture(t)
	base = "http" + strings.TrimPrefix(base, "ws")
	for name, want := range map[string]string{
		"web":    "200 {\"sessions\":[\"b\",\"a\"]}\n",
		"off":    "502 can't open a terminal on off: it is stopped, not running\n",
		"denied": "502 exe.dev denied the integration a a terminal on denied: VM \"denied\" not found\n",
		"lapsed": "502 lapsed's terminal: 307 Temporary Redirect: <a href=\"/login\">Temporary Redirect</a>.\n",
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := http.Get(base + name + "/terminal-sessions")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprint(resp.StatusCode, " ", string(body)); got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestExeVMTerminalClose(t *testing.T) {
	t.Parallel()
	base, _, _ := exeVMTerminalFixture(t)
	base = "http" + strings.TrimPrefix(base, "ws")
	for path, want := range map[string]string{
		"web/terminal-sessions/a":    "204 ",
		"web/terminal-sessions/b":    "502 web's terminal: 502 Bad Gateway: Failed to close session: no such luck\n",
		"web/terminal-sessions/a_b":  "400 session must be 1 to 64 letters, digits and dashes\n",
		"denied/terminal-sessions/a": "502 exe.dev denied the integration a a terminal on denied: VM \"denied\" not found\n",
	} {
		t.Run(path, func(t *testing.T) {
			req, err := http.NewRequest("DELETE", base+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprint(resp.StatusCode, " ", string(body)); got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestExeVMTerminalBadSession(t *testing.T) {
	t.Parallel()
	base, _, _ := exeVMTerminalFixture(t)
	resp, err := http.Get("http" + strings.TrimPrefix(base, "ws") + "web/terminal?session=a%20b")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestExeVMTerminalNeedsTheFlag(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newActionFixture(t, "true", false)
	for _, route := range []string{"GET /api/exe-vms/web/terminal?session=s", "GET /api/exe-vms/web/terminal-sessions", "DELETE /api/exe-vms/web/terminal-sessions/a"} {
		method, path, _ := strings.Cut(route, " ")
		if code := workspaceAPI(s, method, path, "").Code; code != http.StatusNotFound {
			t.Errorf("%s: status %d", route, code)
		}
	}
}

// A token lasts for the requests after it, until the terminal refuses it.
func TestExeVMTerminalCachesTokens(t *testing.T) {
	t.Parallel()
	base, _, mints := exeVMTerminalFixture(t)
	base = "http" + strings.TrimPrefix(base, "ws")
	get := func(vm string) {
		t.Helper()
		resp, err := http.Get(base + vm + "/terminal-sessions")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	for _, step := range []struct {
		vm    string
		mints int32
	}{{"web", 1}, {"web", 1}, {"lapsed", 2}, {"lapsed", 3}, {"web", 3}} {
		get(step.vm)
		if got := mints.Load(); got != step.mints {
			t.Fatalf("after %s: %d tokens minted, want %d", step.vm, got, step.mints)
		}
	}
}
