package server

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// A prod VM's terminal is a session of its exe.dev web terminal
// (<vm>.xterm.exe.xyz), through the terminal's HTTP API, as exe.dev's apps
// use it, with a short-lived token for it from the exe.dev integration
// that lists the VM (POST /terminal-token/<vm>), which the terminal checks
// as any token of the integration's key. Tokens are cached until shortly
// before they expire, or until the terminal refuses one.

// exeVMSessionName is the shape of exe.dev's web terminal session names.
var exeVMSessionName = regexp.MustCompile(`^[a-zA-Z0-9-]{1,64}$`)

// exeVMShellExited is the web terminal's close code for a session whose
// shell exited.
const exeVMShellExited = websocket.StatusCode(4001)

// handleExeVMTerminal handles GET /api/exe-vms/{name}/terminal?session=S,
// a websocket speaking /api/exec-ws's protocol to session S of the web
// terminal of a VM of the prod tab, which creates S if need be. The browser
// names a listed VM, never a host. Closing the websocket detaches; the
// session stays.
func (s *Server) handleExeVMTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.requireDevProdTabs(w, r) {
		return
	}
	session := r.URL.Query().Get("session")
	if !exeVMSessionName.MatchString(session) {
		http.Error(w, "session must be 1 to 64 letters, digits and dashes", http.StatusBadRequest)
		return
	}
	conn, cols, rows, ok := s.acceptTerminalWS(w, r)
	if !ok {
		return
	}
	defer conn.Close(websocket.StatusInternalError, "internal error")
	ctx := r.Context()
	client, err := s.dialExeVMTerminal(ctx, r.PathValue("name"), session, cols, rows)
	if err != nil {
		wsjson.Write(ctx, conn, ExecMessage{Type: "error", Data: err.Error()})
		conn.Close(websocket.StatusNormalClosure, "no shell")
		return
	}
	defer client.Close()
	s.bridgeWS(ctx, conn, client, "")
}

// handleExeVMTerminalSessions handles GET /api/exe-vms/{name}/terminal-sessions:
// the names of the VM's live web terminal sessions.
func (s *Server) handleExeVMTerminalSessions(w http.ResponseWriter, r *http.Request) {
	if !s.requireDevProdTabs(w, r) {
		return
	}
	var list struct {
		Sessions []struct {
			Name string `json:"name"`
		} `json:"sessions"`
	}
	if err := s.exeVMTerminalRequest(r.Context(), r.PathValue("name"), http.MethodGet, "/terminal/sessions", nil, &list); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	sessions := []string{}
	for _, session := range list.Sessions {
		sessions = append(sessions, session.Name)
	}
	writeJSON(w, http.StatusOK, map[string][]string{"sessions": sessions})
}

// handleExeVMTerminalClose handles DELETE
// /api/exe-vms/{name}/terminal-sessions/{session}: it ends the session,
// which hangs up whoever is attached to it.
func (s *Server) handleExeVMTerminalClose(w http.ResponseWriter, r *http.Request) {
	if !s.requireDevProdTabs(w, r) {
		return
	}
	session := r.PathValue("session")
	if !exeVMSessionName.MatchString(session) {
		http.Error(w, "session must be 1 to 64 letters, digits and dashes", http.StatusBadRequest)
		return
	}
	form := url.Values{"name": {session}}
	if err := s.exeVMTerminalRequest(r.Context(), r.PathValue("name"), http.MethodPost, "/terminal/sessions/close", form, nil); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// exeVMTerminalAccess is a listed VM's web terminal and a token for it,
// good until expires.
type exeVMTerminalAccess struct {
	url     string
	token   string
	expires time.Time
}

// exeVMTokenSlack is how long before its token expires access is no longer
// handed out: long enough to reach the terminal with it.
const exeVMTokenSlack = time.Minute

// exeVMTerminal returns access to listed VM name's web terminal, minting a
// token unless one cached lasts. Its errors are one line for the user.
func (s *Server) exeVMTerminal(ctx context.Context, name string) (*exeVMTerminalAccess, error) {
	c := &s.exeVMCache
	c.mu.Lock()
	access, ok := c.terminals[name]
	c.mu.Unlock()
	if ok && time.Until(access.expires) > exeVMTokenSlack {
		return access, nil
	}
	list, err := s.exeVMs(ctx, false)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(list.VMs, func(vm exeVM) bool { return vm.Name == name })
	if i < 0 {
		return nil, fmt.Errorf("no exe.dev integration of this host lists a VM named %s", name)
	}
	vm := list.VMs[i]
	switch {
	case vm.Status != "running":
		return nil, fmt.Errorf("can't open a terminal on %s: it is %s, not running", vm.Name, vm.Status)
	case !vm.Shell:
		return nil, fmt.Errorf("exe.dev doesn't let the integration %s open a shell on %s", vm.Integration, vm.Name)
	}
	host, err := s.exeDevIntegrationHost(ctx, vm.Integration)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+host+"/terminal-token/"+url.PathEscape(vm.Name), nil)
	if err != nil {
		return nil, err
	}
	var got struct {
		URL       string    `json:"url"`
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
		Error     string    `json:"error"`
	}
	if err := s.exeVMDo(req, &got); err != nil {
		if got.Error != "" {
			err = errors.New(got.Error)
		}
		return nil, fmt.Errorf("exe.dev denied the integration %s a terminal on %s: %w", vm.Integration, vm.Name, err)
	}
	if got.URL == "" || got.Token == "" || got.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("exe.dev gave the integration %s no terminal for %s", vm.Integration, vm.Name)
	}
	access = &exeVMTerminalAccess{url: got.URL, token: got.Token, expires: got.ExpiresAt}
	c.mu.Lock()
	if c.terminals == nil {
		c.terminals = map[string]*exeVMTerminalAccess{}
	}
	c.terminals[name] = access
	c.mu.Unlock()
	return access, nil
}

// forgetExeVMTerminal drops VM name's cached token, which its terminal
// refused or which failed some other way, so the next request mints one.
func (s *Server) forgetExeVMTerminal(name string) {
	s.exeVMCache.mu.Lock()
	delete(s.exeVMCache.terminals, name)
	s.exeVMCache.mu.Unlock()
}

// exeVMTerminalRequest sends a request to listed VM name's web terminal,
// with form as its body if set, and decodes its JSON answer into out if
// set. Its errors are one line for the user.
func (s *Server) exeVMTerminalRequest(ctx context.Context, name, method, path string, form url.Values, out any) error {
	access, err := s.exeVMTerminal(ctx, name)
	if err != nil {
		return err
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, access.url+path, body)
	if err != nil {
		return err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Authorization", "Bearer "+access.token)
	if err := s.exeVMDo(req, out); err != nil {
		s.forgetExeVMTerminal(name)
		return fmt.Errorf("%s's terminal: %w", name, err)
	}
	return nil
}

// exeVMHTTP is the HTTP client of VMs' web terminals, which doesn't follow
// redirects: a terminal redirects a request it refuses to its login.
func (s *Server) exeVMHTTP() *http.Client {
	client := *cmp.Or(s.conversationVMs.http, http.DefaultClient)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

// exeVMDo sends req and decodes a JSON answer into out if set, an error's
// too.
func (s *Server) exeVMDo(req *http.Request, out any) error {
	resp, err := s.exeVMHTTP().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if out != nil && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(body, out); err != nil && resp.StatusCode/100 == 2 {
			return err
		}
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(strings.SplitN(string(body), "\n", 2)[0]))
	}
	return nil
}

// dialExeVMTerminal attaches to session of listed VM name's web terminal.
func (s *Server) dialExeVMTerminal(ctx context.Context, name, session string, cols, rows uint16) (*exeVMTerminalClient, error) {
	access, err := s.exeVMTerminal(ctx, name)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(access.url)
	if err != nil {
		return nil, err
	}
	u.Scheme = map[string]string{"https": "wss", "http": "ws"}[u.Scheme]
	u.Path = "/terminal/ws/0"
	u.RawQuery = url.Values{"name": {session}, "cols": {strconv.Itoa(int(cols))}, "rows": {strconv.Itoa(int(rows))}}.Encode()
	// The websocket outlives the request that opened it.
	conn, resp, err := websocket.Dial(context.WithoutCancel(ctx), u.String(), &websocket.DialOptions{
		HTTPClient: s.exeVMHTTP(),
		HTTPHeader: http.Header{"Authorization": {"Bearer " + access.token}},
	})
	if err != nil {
		s.forgetExeVMTerminal(name)
		if resp != nil {
			return nil, fmt.Errorf("couldn't open %s's terminal: %s", name, resp.Status)
		}
		return nil, fmt.Errorf("couldn't open %s's terminal: %w", name, err)
	}
	conn.SetReadLimit(1 << 20)
	return &exeVMTerminalClient{conn: conn, vm: name}, nil
}

// exeVMTerminalClient is a web terminal session's websocket, whose messages
// are {type, data, cols, rows}: input, resize and ping from us, output
// (base64) and pong to us.
type exeVMTerminalClient struct {
	conn *websocket.Conn
	vm   string
}

func (c *exeVMTerminalClient) Recv() (terminalMessage, error) {
	for {
		var msg struct{ Type, Data string }
		if err := wsjson.Read(context.Background(), c.conn, &msg); err != nil {
			var closed websocket.CloseError
			switch {
			case websocket.CloseStatus(err) == exeVMShellExited:
				return terminalMessage{kind: terminalExit}, nil
			case errors.As(err, &closed) && closed.Code != websocket.StatusNormalClosure && closed.Reason != "":
				return terminalMessage{kind: terminalFailed, data: []byte(c.vm + "'s terminal: " + closed.Reason)}, nil
			}
			return terminalMessage{}, err
		}
		if msg.Type != "output" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(msg.Data)
		if err != nil {
			return terminalMessage{}, err
		}
		return terminalMessage{kind: terminalOutput, data: data}, nil
	}
}

func (c *exeVMTerminalClient) SendInput(data []byte) error {
	return wsjson.Write(context.Background(), c.conn, map[string]string{"type": "input", "data": string(data)})
}

func (c *exeVMTerminalClient) SendResize(cols, rows uint16) error {
	return wsjson.Write(context.Background(), c.conn, map[string]any{"type": "resize", "cols": cols, "rows": rows})
}

func (c *exeVMTerminalClient) Close() error {
	return c.conn.Close(websocket.StatusNormalClosure, "")
}
