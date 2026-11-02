package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"shelley.exe.dev/db"
	"shelley.exe.dev/version"
)

const (
	connectTimeout = 60 * time.Second
	idleTimeout    = 30 * time.Minute
)

// ErrTimeout is what a call that timed out fails with.
var ErrTimeout = errors.New("timed out")

// Manager owns the MCP sessions, one per server and scope (the caller's
// conversation ID, "" for the UI), and the servers' OAuth logins. Sessions
// connect on first use and close after idleTimeout unused.
type Manager struct {
	db        *db.DB
	loginURL  func(server string) string
	client    *sdk.Client
	transport func(Server, auth.OAuthHandler) sdk.Transport
	events    events
	authCtx   context.Context // for token refreshes, which outlive requests

	// mu guards sessions, closed, and the sessions' users, lastUsed and
	// timer. Never call into a go-sdk session while holding it: its calls
	// wait for the network.
	mu       sync.Mutex
	sessions map[key]*session
	closed   bool
	wg       sync.WaitGroup // sessions not yet closed

	// authMu guards the OAuth state; see oauth.go.
	authMu   sync.Mutex
	logins   map[string]*login        // stored logins by server, once loaded; nil if none
	required map[string]bool          // servers that answered 401 and have no usable login
	pending  map[string]*pendingLogin // by server
	states   map[string]*pendingLogin // by OAuth state, until the callback comes
}

type key struct{ server, scope string }

type session struct {
	key    key
	cfg    Server
	ctx    context.Context // canceled, with why, when the session closes
	cancel context.CancelCauseFunc
	ready  chan struct{} // closed when connecting ends
	cs     *sdk.ClientSession
	err    error
	id     string // the MCP session ID, read once connected
	once   sync.Once

	// Guarded by Manager.mu.
	users    int
	lastUsed time.Time
	timer    *time.Timer
}

// NewManager returns a Manager whose logins are stored in database and
// that sends users to loginURL(server) to log in.
func NewManager(database *db.DB, loginURL func(server string) string) *Manager {
	return &Manager{
		db:        database,
		loginURL:  loginURL,
		client:    sdk.NewClient(&sdk.Implementation{Name: "shelley", Version: version.Version}, nil),
		transport: httpTransport,
		authCtx:   context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: 30 * time.Second, Transport: headerTransport(nil)}),
		sessions:  map[key]*session{},
		logins:    map[string]*login{},
		required:  map[string]bool{},
		pending:   map[string]*pendingLogin{},
		states:    map[string]*pendingLogin{},
	}
}

// ServerInfo is from the server's answer to initialize.
type ServerInfo struct {
	Name         string `json:"name"`
	Title        string `json:"title"`
	Version      string `json:"version"`
	Instructions string `json:"instructions"`
}

// Tools is a server's description and tools.
type Tools struct {
	Server ServerInfo  `json:"server"`
	Tools  []*sdk.Tool `json:"tools"`
}

// ListTools lists cfg's tools.
func (m *Manager) ListTools(ctx context.Context, cfg Server, scope string) (*Tools, error) {
	s, ctx, done, err := m.acquire(ctx, cfg, scope)
	if err != nil {
		return nil, err
	}
	defer done()
	init := s.cs.InitializeResult()
	tl := &Tools{Server: ServerInfo{Instructions: init.Instructions}, Tools: []*sdk.Tool{}}
	if i := init.ServerInfo; i != nil {
		tl.Server.Name, tl.Server.Title, tl.Server.Version = i.Name, i.Title, i.Version
	}
	for tool, err := range s.cs.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("listing the tools of %s: %w", cfg.Name, m.failed(s, ctx, err))
		}
		tl.Tools = append(tl.Tools, tool)
	}
	return tl, nil
}

// CallTool calls tool once. A tool's failure is a result with IsError set.
// A positive timeout bounds the call (not connecting); when it passes, the
// call fails with ErrTimeout.
func (m *Manager) CallTool(ctx context.Context, cfg Server, scope, tool string, args map[string]json.RawMessage, timeout time.Duration) (*sdk.CallToolResult, error) {
	s, ctx, done, err := m.acquire(ctx, cfg, scope)
	if err != nil {
		return nil, err
	}
	defer done()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, timeout, ErrTimeout)
		defer cancel()
	}
	if args == nil {
		args = map[string]json.RawMessage{}
	}
	res, err := s.cs.CallTool(ctx, &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil && context.Cause(ctx) == ErrTimeout {
		return nil, fmt.Errorf("calling %s.%s: %w after %s; the tool may still have run", cfg.Name, tool, ErrTimeout, timeout)
	}
	if err != nil {
		return nil, fmt.Errorf("calling %s.%s: %w", cfg.Name, tool, m.failed(s, ctx, err))
	}
	return res, nil
}

// failed explains err, from a request on s with ctx.
func (m *Manager) failed(s *session, ctx context.Context, err error) error {
	if errors.Is(err, sdk.ErrSessionMissing) {
		// The server forgot the session (it restarted, say): the next
		// request connects anew. Requests it closes under say so too.
		m.dropSession(s, fmt.Errorf("the server ended it (%w)", sdk.ErrSessionMissing))
	} else if ctx.Err() != nil && s.ctx.Err() != nil {
		// The session closed under the request. When its connection
		// failed, which also fails the request, the request may see
		// only the closing; the cause still says why.
		err = fmt.Errorf("the session was closed (%w)", context.Cause(s.ctx))
	}
	return err
}

// CloseSession closes the session of name and scope, if any.
func (m *Manager) CloseSession(name, scope string) {
	m.closeWhere(func(k key) bool { return k == key{name, scope} }, "restarted")
}

// CloseServer closes the sessions of name and waits for them to end.
func (m *Manager) CloseServer(name string) {
	m.closeWhere(func(k key) bool { return k.server == name }, "the server was edited or logged out")
}

// Close closes all sessions, fails later requests, and waits until every
// session has ended.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.closeWhere(func(key) bool { return true }, "Shelley is shutting down")
	m.wg.Wait()
}

func (m *Manager) closeWhere(match func(key) bool, why string) {
	var wg sync.WaitGroup
	m.mu.Lock()
	for k, s := range m.sessions {
		if match(k) {
			m.removeLocked(s)
			wg.Go(func() { m.close(s, errors.New(why)) })
		}
	}
	m.mu.Unlock()
	wg.Wait()
}

// acquire returns a connected session for cfg and scope, connecting at
// most once at a time per key, and a context for requests on it, which is
// also canceled when the session closes. Call done when finished with it.
func (m *Manager) acquire(ctx context.Context, cfg Server, scope string) (*session, context.Context, func(), error) {
	k := key{cfg.Name, scope}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, nil, errors.New("Shelley is shutting down")
	}
	s := m.sessions[k]
	if s != nil && !s.cfg.sameConnection(cfg) {
		m.removeLocked(s)
		go m.close(s, errors.New("the server was edited"))
		s = nil
	}
	if s == nil {
		s = &session{key: k, cfg: cfg, ready: make(chan struct{})}
		s.ctx, s.cancel = context.WithCancelCause(context.Background())
		m.sessions[k] = s
		m.wg.Add(1)
		go m.connect(s)
	}
	s.users++
	if s.timer != nil {
		s.timer.Stop()
	}
	m.mu.Unlock()

	select {
	case <-s.ready:
	case <-ctx.Done():
		m.release(s)
		return nil, nil, nil, ctx.Err()
	}
	if s.err != nil {
		m.release(s)
		return nil, nil, nil, s.err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return s, ctx, func() {
		stop()
		cancel()
		m.release(s)
	}, nil
}

func (m *Manager) connect(s *session) {
	ctx, cancel := context.WithTimeout(s.ctx, connectTimeout)
	defer cancel()
	var h auth.OAuthHandler
	if s.cfg.OAuthCapable() {
		h = &oauthHandler{m, s.cfg}
	}
	s.cs, s.err = m.client.Connect(ctx, m.transport(s.cfg, h), nil)
	if s.err != nil {
		s.err = fmt.Errorf("connecting to MCP server %q: %w", s.cfg.Name, s.err)
		m.events.add(s.key.server, s.key.scope, "connect_failed", s.err.Error())
		close(s.ready)
		m.dropSession(s, s.err)
		return
	}
	s.id = s.cs.ID()
	m.events.add(s.key.server, s.key.scope, "connected", s.id)
	close(s.ready)
	go func() {
		why := errors.New("the connection ended")
		if err := s.cs.Wait(); err != nil {
			why = fmt.Errorf("the connection ended: %w", err)
		}
		m.dropSession(s, why)
	}()
}

// dropSession removes s, if current, and closes it.
func (m *Manager) dropSession(s *session, why error) {
	m.mu.Lock()
	m.removeLocked(s)
	m.mu.Unlock()
	m.close(s, why)
}

func (m *Manager) removeLocked(s *session) {
	if m.sessions[s.key] == s {
		delete(m.sessions, s.key)
	}
}

// release ends a use of s, arming its idle timer when it was the last.
func (m *Manager) release(s *session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s.users--
	s.lastUsed = time.Now()
	if s.users > 0 || m.sessions[s.key] != s {
		return
	}
	if s.timer == nil {
		s.timer = time.AfterFunc(idleTimeout, func() { m.expire(s) })
	} else {
		s.timer.Reset(idleTimeout)
	}
}

// expire closes s if it is still unused and current; the timer may have
// fired just as s was used again.
func (m *Manager) expire(s *session) {
	m.mu.Lock()
	idle := s.users == 0 && m.sessions[s.key] == s && time.Since(s.lastUsed) >= idleTimeout
	if idle {
		m.removeLocked(s)
	}
	m.mu.Unlock()
	if idle {
		m.close(s, errors.New("idle"))
	}
}

// close ends s, which has been removed, failing its requests, once it has
// finished connecting.
func (m *Manager) close(s *session, why error) {
	s.once.Do(func() {
		s.cancel(why)
		<-s.ready
		if s.cs != nil {
			s.cs.Close()
			m.events.add(s.key.server, s.key.scope, "session_closed", why.Error())
		}
		m.wg.Done()
	})
}
