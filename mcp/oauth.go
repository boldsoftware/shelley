package mcp

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"shelley.exe.dev/db/generated"
)

const loginTimeout = 10 * time.Minute

// LoginRequiredError means the user has to log in to Server in a browser.
type LoginRequiredError struct{ Server, URL string }

func (e *LoginRequiredError) Error() string {
	return fmt.Sprintf("MCP server %q needs you to log in: open %s", e.Server, e.URL)
}

// ErrLoginExpired is what a callback for a login that isn't pending gets.
var ErrLoginExpired = errors.New("this login expired or was already used")

// LoginState is a server's OAuth state, without secrets.
type LoginState struct {
	Auth            string // "logged_in" (a login is stored), "login_required", or ""
	ClientID        string
	Expiry          time.Time // of the access token; zero if unknown
	HasRefreshToken bool
	PendingSince    time.Time // when the pending login started; zero if none
}

// LoginState returns srv's login state. It reads the stored login rather
// than the cached one, whose lock a refresh holds while it waits for the
// token endpoint.
func (m *Manager) LoginState(ctx context.Context, srv Server) (LoginState, error) {
	var st LoginState
	if !srv.OAuthCapable() {
		return st, nil
	}
	m.authMu.Lock()
	if m.required[srv.Name] {
		st.Auth = "login_required"
	}
	if p := m.pending[srv.Name]; p != nil {
		st.PendingSince = p.started
	}
	m.authMu.Unlock()
	client, tok, err := m.storedLogin(ctx, srv.Name)
	if tok != nil {
		st.Auth, st.ClientID, st.Expiry, st.HasRefreshToken = "logged_in", client.ClientID, tok.Expiry, tok.RefreshToken != ""
	}
	return st, err
}

// storedLogin returns server's stored login, or nils if there is none.
func (m *Manager) storedLogin(ctx context.Context, server string) (*oauth2.Config, *oauth2.Token, error) {
	var row generated.McpOauth
	err := m.db.Queries(ctx, func(q *generated.Queries) (err error) {
		row, err = q.GetMCPOAuth(ctx, server)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	client, tok := &oauth2.Config{}, &oauth2.Token{}
	if err := errors.Join(json.Unmarshal([]byte(row.Client), client), json.Unmarshal([]byte(row.Token), tok)); err != nil {
		return nil, nil, fmt.Errorf("stored login for %q: %w", server, err)
	}
	return client, tok, nil
}

// Logout cancels name's pending login, deletes its stored login, and closes
// its sessions.
func (m *Manager) Logout(ctx context.Context, name string) error {
	m.authMu.Lock()
	if p := m.pending[name]; p != nil {
		p.cancel()
	}
	delete(m.required, name)
	n, err := m.deleteLoginLocked(ctx, name)
	m.authMu.Unlock()
	if err != nil {
		return err
	}
	m.CloseServer(name)
	if n > 0 {
		m.events.add(name, "", "logged_out", "")
	}
	return nil
}

// login is a stored login, shared by all sessions with its server.
type login struct {
	m      *Manager
	server string
	client *oauth2.Config

	mu  sync.Mutex
	src oauth2.TokenSource // refreshes tok when it expires
	tok *oauth2.Token      // the last token src returned
}

func (m *Manager) newLogin(server string, client *oauth2.Config, tok *oauth2.Token) *login {
	return &login{m: m, server: server, client: client, tok: tok, src: client.TokenSource(m.authCtx, tok)}
}

// login returns server's stored login, or nil if there is none.
func (m *Manager) login(ctx context.Context, server string) (*login, error) {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	if l, ok := m.logins[server]; ok {
		return l, nil
	}
	client, tok, err := m.storedLogin(ctx, server)
	if err != nil {
		return nil, err
	}
	if tok != nil {
		m.logins[server] = m.newLogin(server, client, tok)
	} else {
		m.logins[server] = nil
	}
	return m.logins[server], nil
}

// save stores client and tok as server's login, unless l, the login that
// refreshed tok, has been replaced or deleted since. l is nil for new logins.
func (m *Manager) save(server string, l *login, client *oauth2.Config, tok *oauth2.Token) error {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	if l != nil && m.logins[server] != l {
		return nil
	}
	c, _ := json.Marshal(client)
	t, _ := json.Marshal(tok)
	err := m.db.QueriesTx(m.authCtx, func(q *generated.Queries) error {
		return q.UpsertMCPOAuth(m.authCtx, generated.UpsertMCPOAuthParams{ServerName: server, Client: string(c), Token: string(t)})
	})
	if err == nil && l == nil {
		m.logins[server] = m.newLogin(server, client, tok)
		delete(m.required, server)
	}
	return err
}

// dropLogin deletes l, unless it has been replaced since, and returns the
// error that says to log in.
func (m *Manager) dropLogin(l *login) error {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	if m.logins[l.server] == l {
		if _, err := m.deleteLoginLocked(m.authCtx, l.server); err != nil {
			return err
		}
	}
	return m.loginRequiredLocked(l.server)
}

func (m *Manager) deleteLoginLocked(ctx context.Context, server string) (n int64, err error) {
	m.logins[server] = nil
	err = m.db.QueriesTx(ctx, func(q *generated.Queries) error {
		n, err = q.DeleteMCPOAuth(ctx, server)
		return err
	})
	return n, err
}

func (m *Manager) loginRequiredLocked(server string) error {
	m.required[server] = true
	return &LoginRequiredError{Server: server, URL: m.loginURL(server)}
}

// Token returns the current token, refreshing and saving it when it has
// expired. A refresh the authorization server refuses deletes the login.
func (l *login) Token() (*oauth2.Token, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tok.RefreshToken == "" {
		return l.tok, nil // if it expired, the server's 401 says to log in
	}
	tok, err := l.src.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			err = errors.New(tokenEndpointError(re))
		}
		l.m.events.add(l.server, "", "refresh_failed", err.Error())
		if re != nil && slices.Contains([]string{"invalid_grant", "invalid_client", "unauthorized_client"}, re.ErrorCode) {
			return nil, l.m.dropLogin(l)
		}
		return nil, fmt.Errorf("refreshing the token for %q: %w", l.server, err)
	}
	if tok.AccessToken != l.tok.AccessToken {
		if err := l.m.save(l.server, l, l.client, tok); err != nil {
			return nil, err
		}
		l.tok = tok
		l.m.events.add(l.server, "", "token_refreshed", "")
	}
	return tok, nil
}

// tokenEndpointError describes re by its error code: its body could echo
// a secret.
func tokenEndpointError(re *oauth2.RetrieveError) string {
	return "token endpoint: " + cmp.Or(re.ErrorCode, strconv.Itoa(re.Response.StatusCode))
}

// refresh refreshes the token even though it hasn't expired.
func (l *login) refresh() (*oauth2.Token, error) {
	l.mu.Lock()
	l.src = l.client.TokenSource(l.m.authCtx, &oauth2.Token{RefreshToken: l.tok.RefreshToken})
	l.mu.Unlock()
	return l.Token()
}

// oauthHandler authorizes a session's requests with its server's stored
// login. It never logs in: that needs the user, at the login link.
type oauthHandler struct {
	m   *Manager
	cfg Server
}

func (h *oauthHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	l, err := h.m.login(ctx, h.cfg.Name)
	if l == nil || err != nil {
		return nil, err
	}
	return l, nil
}

// Authorize is called when the server answers 401 or 403. go-sdk retries
// the request once if it returns nil.
func (h *oauthHandler) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return nil // the retry fails with the server's 403
	}
	l, err := h.m.login(ctx, h.cfg.Name)
	if err != nil {
		return err
	}
	if l == nil {
		h.m.authMu.Lock()
		defer h.m.authMu.Unlock()
		return h.m.loginRequiredLocked(h.cfg.Name)
	}
	l.mu.Lock()
	cur := l.tok
	l.mu.Unlock()
	if req.Header.Get("Authorization") != "Bearer "+cur.AccessToken {
		return nil // the token changed since: retry with the new one
	}
	if cur.RefreshToken == "" {
		return h.m.dropLogin(l)
	}
	// Servers that don't say when tokens expire only tell us with a 401.
	tok, err := l.refresh()
	if err != nil {
		return err
	}
	// If the server rejects the new token too, go-sdk fails the request
	// without asking us again; find out now.
	resp, err = probe(ctx, h.cfg, tok.AccessToken, pingRequest)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return h.m.dropLogin(l)
	}
	return nil
}

// pendingLogin is a login waiting for its callback.
type pendingLogin struct {
	server      string
	redirectURI string
	started     time.Time
	cancel      context.CancelFunc
	ready       chan struct{} // closed when url or err is set
	url         string        // the authorization URL
	err         error
	state       string
	callback    chan url.Values // the callback's query
	done        chan error      // the login's outcome, once the callback came
}

// Login returns the authorization URL that logs the user in to srv,
// redirecting to redirectURI. A login waits up to loginTimeout for its
// callback. There's one pending login per server; until it ends, Login
// returns its URL again for the same redirect URI: a double click, a second
// tab or a link preview mustn't break the user's login.
func (m *Manager) Login(ctx context.Context, srv Server, redirectURI string) (string, error) {
	if !srv.OAuthCapable() {
		return "", fmt.Errorf("MCP server %q has a static Authorization header", srv.Name)
	}
	m.authMu.Lock()
	p := m.pending[srv.Name]
	if p != nil && p.redirectURI != redirectURI {
		p.cancel() // it would send the browser back to another host
		p = nil
	}
	if p == nil {
		p = &pendingLogin{server: srv.Name, redirectURI: redirectURI, started: time.Now(), ready: make(chan struct{}), callback: make(chan url.Values, 1), done: make(chan error, 1)}
		var lctx context.Context
		lctx, p.cancel = context.WithTimeout(context.Background(), loginTimeout)
		m.pending[srv.Name] = p
		go m.runLogin(lctx, p, srv)
	}
	m.authMu.Unlock()
	select {
	case <-p.ready:
		return p.url, p.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// runLogin drives go-sdk's authorization code handler, which expects an
// interactive client: its fetcher hands the authorization URL to Login and
// waits for FinishLogin to bring the code.
func (m *Manager) runLogin(ctx context.Context, p *pendingLogin, srv Server) {
	defer p.cancel()
	asked := false // whether the user was sent to the authorization URL
	err := func() error {
		resp, err := probe(ctx, srv, "", initializeRequest)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusUnauthorized {
			return fmt.Errorf("it doesn't ask for a login (it answered %d %s)", resp.StatusCode, http.StatusText(resp.StatusCode))
		}
		var client *oauth2.Config
		var tok *oauth2.Token
		h, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
			DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
				RedirectURIs: []string{p.redirectURI},
				ClientName:   "Shelley",
				GrantTypes:   []string{"authorization_code", "refresh_token"},
			}},
			RedirectURL:         p.redirectURI,
			RequestRefreshToken: true,
			Client:              &http.Client{Timeout: 30 * time.Second, Transport: registrationErrors{p.redirectURI}},
			AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
				u, err := url.Parse(args.URL)
				if err != nil {
					return nil, err
				}
				asked = true
				m.authMu.Lock()
				p.url, p.state = args.URL, u.Query().Get("state")
				m.states[p.state] = p
				m.authMu.Unlock()
				close(p.ready)
				m.events.add(srv.Name, "", "login_started", "")
				select {
				case q := <-p.callback:
					if e := q.Get("error"); e != "" {
						return nil, fmt.Errorf("the authorization server answered %s", e)
					}
					return &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
				case <-ctx.Done():
					return nil, errors.New("the login expired or was canceled")
				}
			},
			NewTokenSource: func(_ context.Context, c *oauth2.Config, t *oauth2.Token) (oauth2.TokenSource, error) {
				client, tok = c, t
				return oauth2.StaticTokenSource(t), nil
			},
		})
		if err != nil {
			return err
		}
		if err := h.Authorize(ctx, resp.Request, resp); err != nil {
			return err
		}
		return m.save(srv.Name, nil, client, tok)
	}()
	var re *registrationError
	var te *oauth2.RetrieveError
	switch {
	case errors.As(err, &re):
		err = re
	case errors.As(err, &te):
		err = errors.New(tokenEndpointError(te))
	}
	m.authMu.Lock()
	if m.pending[srv.Name] == p {
		delete(m.pending, srv.Name)
	}
	delete(m.states, p.state)
	m.authMu.Unlock()
	if err != nil {
		err = fmt.Errorf("logging in to %s: %w", srv.Name, err)
		m.events.add(srv.Name, "", "login_failed", err.Error())
	} else {
		m.events.add(srv.Name, "", "login_completed", "")
		m.CloseServer(srv.Name) // its sessions reconnect with the new login
	}
	if asked {
		p.done <- err
	} else {
		p.err = err
		close(p.ready)
	}
}

// FinishLogin completes the pending login that the callback's query
// names by its state, and returns its server.
func (m *Manager) FinishLogin(ctx context.Context, query url.Values) (string, error) {
	m.authMu.Lock()
	p := m.states[query.Get("state")]
	delete(m.states, query.Get("state"))
	m.authMu.Unlock()
	if p == nil {
		return "", ErrLoginExpired
	}
	p.callback <- query
	select {
	case err := <-p.done:
		return p.server, err
	case <-ctx.Done():
		return p.server, ctx.Err()
	}
}

// registrationErrors explains a refused dynamic client registration, which
// go-sdk reports as tersely as the server: Figma answers a bare 403 for
// clients it hasn't approved, exe.dev invalid_redirect_uri for exe.xyz.
type registrationErrors struct{ redirectURI string }

type registrationError struct{ msg string }

func (e *registrationError) Error() string { return e.msg }

func (t registrationErrors) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := headerTransport(nil).RoundTrip(req)
	// Registration is the only JSON POST to an authorization server.
	if err != nil || resp.StatusCode < 400 || req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" {
		return resp, err
	}
	defer resp.Body.Close()
	var e struct {
		Code        string `json:"error"`
		Description string `json:"error_description"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
	msg := fmt.Sprintf("the authorization server refused to register Shelley as a client (%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	if e.Code != "" {
		msg += ", " + e.Code
	}
	msg += ")"
	if d := []rune(e.Description); len(d) > 0 {
		msg += ": " + string(d[:min(len(d), 200)])
	}
	return nil, &registrationError{msg + ". Some servers only accept clients they have approved. The redirect URI was " + t.redirectURI + "."}
}
