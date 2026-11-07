package siwc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// signInTimeout bounds how long a sign-in waits for its callback.
const signInTimeout = 15 * time.Minute

// ErrSignInRequired means an account has no usable tokens.
var ErrSignInRequired = errors.New("this ChatGPT account is signed out; sign in with ChatGPT again under Manage models")

var issuedClientID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

// Manager runs sign-ins and keeps accounts' tokens fresh.
type Manager struct {
	store  *Store
	client *Client
	// Changed, if set, is called after the set of usable accounts or their models change.
	Changed func()

	mu       sync.Mutex
	attempts map[string]*attempt // by ID
	locks    map[string]*sync.Mutex
}

func NewManager(store *Store, client *Client) *Manager {
	return &Manager{store: store, client: client, attempts: map[string]*attempt{}, locks: map[string]*sync.Mutex{}}
}

// AccountInfo describes an account without its credentials.
type AccountInfo struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	Email     string  `json:"-"`
	Name      string  `json:"name,omitempty"`
	SignedIn  bool    `json:"signed_in"`
	PlanUsage bool    `json:"plan_usage"`
	Models    []Model `json:"-"`
}

// Accounts lists accounts, labeled by email; registrations sharing an email
// (e.g. one per workspace) are numbered.
func (m *Manager) Accounts(ctx context.Context) ([]AccountInfo, error) {
	accounts, err := m.store.List(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]int{}
	infos := make([]AccountInfo, 0, len(accounts))
	for _, a := range accounts {
		label := firstNonEmpty(a.Email, "ChatGPT account")
		if seen[label]++; seen[label] > 1 {
			label = fmt.Sprintf("%s (%d)", label, seen[label])
		}
		infos = append(infos, AccountInfo{
			ID: a.ID, Label: label, Email: a.Email, Name: a.Name, SignedIn: a.SignedIn(),
			PlanUsage: a.PlanUsage(), Models: append([]Model{}, a.Models...),
		})
	}
	return infos, nil
}

func (m *Manager) lock(id string) func() {
	m.mu.Lock()
	l := m.locks[id]
	if l == nil {
		l = new(sync.Mutex)
		m.locks[id] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func (m *Manager) changed() {
	if m.Changed != nil {
		m.Changed()
	}
}

// SignIn is a started sign-in: the user opens AuthorizeURL in a browser,
// which returns to RedirectURI on 127.0.0.1.
type SignIn struct {
	ID           string `json:"id"`
	AuthorizeURL string `json:"authorize_url"`
	RedirectURI  string `json:"redirect_uri"`
}

// Result is a sign-in's outcome.
type Result struct {
	Account AccountInfo `json:"account"`
	// NewRegistration is true the first time the account signs in on this host.
	NewRegistration bool `json:"new_registration"`
}

type attempt struct {
	id, state, nonce, verifier, redirectURI string
	accountID, clientID, subject            string // of the account being reauthorized, if any
	server                                  *http.Server
	expires                                 time.Time

	mu       sync.Mutex
	claimed  bool // a callback is being handled
	finished bool
	result   *Result
	err      error
}

func random() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// StartSignIn begins a sign-in. With accountID it signs that account in again
// using its issued client ID; otherwise it registers a new account. consent
// asks the user to review permissions again, to enable plan usage after
// declining it.
//
// The callback arrives at a listener on this host's 127.0.0.1, which only
// works when the browser runs here too; otherwise the browser fails to load
// it, and the user pastes its URL into CompleteURL.
func (m *Manager) StartSignIn(ctx context.Context, accountID string, consent bool) (*SignIn, error) {
	hostID, err := m.store.HostID(ctx)
	if err != nil {
		return nil, err
	}
	d, err := m.client.config(ctx)
	if err != nil {
		return nil, err
	}
	a := &attempt{id: random(), state: random(), nonce: random(), verifier: random(), expires: time.Now().Add(signInTimeout)}
	query := url.Values{
		"response_type": {"code"}, "scope": {Scopes}, "resource": {Resource}, "state": {a.state}, "nonce": {a.nonce},
		"code_challenge_method": {"S256"}, "code_challenge": {pkceChallenge(a.verifier)}, "ext_agent_host_id": {hostID},
	}
	if accountID != "" {
		account, err := m.store.Get(ctx, accountID)
		if err != nil {
			return nil, err
		}
		a.accountID, a.clientID, a.subject = account.ID, account.ClientID, account.Subject
		query.Set("client_id", account.ClientID)
		// An ID token hint would expose the token in browser history; the email suffices.
		if account.Email != "" {
			query.Set("login_hint", account.Email)
		}
	} else {
		query.Set("client_id", registrationClientID)
		query.Set("agent_name_hint", AgentName)
	}
	if consent {
		query.Set("prompt", "consent")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for the ChatGPT sign-in callback: %w", err)
	}
	a.redirectURI = fmt.Sprintf("http://%s%s", ln.Addr(), callbackPath)
	query.Set("redirect_uri", a.redirectURI)
	a.server = &http.Server{Handler: m.callbackHandler(a, ln.Addr().String()), ReadHeaderTimeout: 10 * time.Second}
	go a.server.Serve(ln)
	time.AfterFunc(signInTimeout, func() { m.finish(a, nil, errors.New("sign-in timed out; start again")) })

	m.mu.Lock()
	for id, old := range m.attempts {
		if time.Now().After(old.expires) {
			delete(m.attempts, id)
		}
	}
	m.attempts[a.id] = a
	m.mu.Unlock()
	return &SignIn{ID: a.id, AuthorizeURL: d.AuthorizationEndpoint + "?" + query.Encode(), RedirectURI: a.redirectURI}, nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Status reports a sign-in's outcome; done is false while it waits for its callback.
func (m *Manager) Status(id string) (done bool, result *Result, err error) {
	m.mu.Lock()
	a := m.attempts[id]
	m.mu.Unlock()
	if a == nil {
		return true, nil, errors.New("this sign-in expired; start again")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.finished, a.result, a.err
}

var callbackPage = template.Must(template.New("").Parse(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width">` +
	`<title>Shelley</title><body style="font:16px system-ui;max-width:32rem;margin:15vh auto;padding:1rem">` +
	`{{if .}}<h1>Sign-in failed</h1><p>{{.}}</p>{{else}}<h1>Signed in</h1><p>Return to Shelley. You can close this tab.</p>{{end}}`))

func (m *Manager) callbackHandler(a *attempt, host string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
		if r.Method != http.MethodGet || r.Host != host || r.URL.Path != callbackPath {
			http.NotFound(w, r)
			return
		}
		// Unrelated requests must not end the sign-in.
		if !stateMatches(r.URL.Query(), a.state) {
			http.Error(w, "This sign-in link is not the one Shelley is waiting for.", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, err := m.complete(ctx, a, r.URL.Query())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var message any
		if err != nil {
			message = err.Error()
		}
		callbackPage.Execute(w, message)
	})
}

func stateMatches(query url.Values, state string) bool {
	return len(query["state"]) == 1 && subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state)) == 1
}

// CompleteURL finishes a sign-in from the callback URL the browser was sent
// to, for when the browser cannot reach this host's 127.0.0.1.
func (m *Manager) CompleteURL(ctx context.Context, raw string) (*Result, error) {
	raw = strings.TrimSpace(raw)
	// Safari copies addresses without their scheme.
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("paste the full address from the browser's address bar")
	}
	query := u.Query()
	m.mu.Lock()
	var a *attempt
	for _, candidate := range m.attempts {
		if stateMatches(query, candidate.state) {
			a = candidate
		}
	}
	m.mu.Unlock()
	if a == nil {
		return nil, errors.New("this address is not from a pending sign-in; start again")
	}
	if u.Scheme+"://"+u.Host+u.Path != a.redirectURI {
		return nil, fmt.Errorf("this address is not for %s", a.redirectURI)
	}
	return m.complete(ctx, a, query)
}

// complete exchanges the callback's code for tokens and saves the account.
func (m *Manager) complete(ctx context.Context, a *attempt, query url.Values) (*Result, error) {
	a.mu.Lock()
	if a.finished {
		err := a.err
		a.mu.Unlock()
		if err == nil {
			err = errors.New("this sign-in already finished")
		}
		return nil, err
	}
	if a.claimed {
		a.mu.Unlock()
		return nil, errors.New("this sign-in already finished")
	}
	a.claimed = true
	a.mu.Unlock()
	result, err := m.exchange(ctx, a, query)
	m.finish(a, result, err)
	return result, err
}

func (m *Manager) finish(a *attempt, result *Result, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished {
		return
	}
	a.finished, a.result, a.err = true, result, err
	// Shutdown lets the callback page finish; finish may run in its handler.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		a.server.Shutdown(ctx)
	}()
}

func (m *Manager) exchange(ctx context.Context, a *attempt, query url.Values) (*Result, error) {
	if code := query.Get("error"); code != "" {
		if code == "access_denied" {
			return nil, errors.New("sign-in was cancelled or permission was declined")
		}
		return nil, &Error{Code: code, Message: query.Get("error_description")}
	}
	returned := query.Get("client_id")
	clientID := firstNonEmpty(returned, a.clientID)
	switch {
	case len(query["code"]) != 1 || query.Get("code") == "" || len(query["client_id"]) > 1:
		return nil, errors.New("ChatGPT returned an incomplete sign-in; start again")
	case a.clientID != "" && returned != "" && returned != a.clientID:
		return nil, errors.New("ChatGPT returned a different registration than this account's; start again")
	case clientID == registrationClientID || !issuedClientID.MatchString(clientID):
		return nil, errors.New("ChatGPT did not complete Shelley's registration; start again")
	}

	newRegistration := a.accountID == ""
	if newRegistration {
		existing, err := m.store.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, account := range existing {
			if account.ClientID == clientID {
				a.accountID = account.ID
			}
		}
	}
	if a.accountID == "" {
		// Save the issued client ID before the one-time code can fail, so a
		// retry reuses this registration instead of creating another.
		a.accountID = newAccountID()
		if err := m.store.Put(ctx, &Account{ID: a.accountID, ClientID: clientID}); err != nil {
			return nil, err
		}
	}

	t, err := m.client.exchange(ctx, clientID, query.Get("code"), a.verifier, a.redirectURI)
	if err != nil {
		return nil, fmt.Errorf("sign in to ChatGPT: %w", err)
	}
	if t.IDToken == "" || t.Scope == "" {
		return nil, errors.New("ChatGPT did not confirm the account or its permissions; start again")
	}
	claims, err := m.client.verifyIDToken(ctx, t.IDToken, clientID, a.nonce)
	if err != nil {
		return nil, err
	}

	unlock := m.lock(a.accountID)
	if previous, err := m.store.Get(ctx, a.accountID); err == nil && previous.Subject == "" {
		newRegistration = true
	}
	account, err := m.saveSignIn(ctx, a.accountID, claims, t)
	unlock()
	if account == nil {
		return nil, err
	}
	m.changed()
	if err != nil {
		return nil, err
	}
	infos, err := m.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	result := &Result{NewRegistration: newRegistration}
	for _, info := range infos {
		if info.ID == account.ID {
			result.Account = info
		}
	}
	return result, nil
}

// saveSignIn stores a sign-in's identity and tokens, then lists the plan's
// models. It returns the account if it was saved, with any error listing models.
func (m *Manager) saveSignIn(ctx context.Context, id string, claims *idClaims, t *tokenResponse) (*Account, error) {
	account, err := m.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if account.Subject != "" && account.Subject != claims.Subject {
		return nil, errors.New("this sign-in returned a different ChatGPT account; choose the original account, or add another")
	}
	account.Subject, account.Email, account.Name = claims.Subject, claims.Email, claims.Name
	account.signOut()
	account.setTokens(t)
	var modelsErr error
	if account.PlanUsage() {
		account.Models, modelsErr = m.client.ListModels(ctx, account.AccessToken)
	}
	if err := m.store.Put(ctx, account); err != nil {
		return nil, err
	}
	if modelsErr != nil {
		return account, fmt.Errorf("signed in, but %w", modelsErr)
	}
	return account, nil
}

// setTokens replaces the token set; a refresh may omit an unchanged scope or ID token.
func (a *Account) setTokens(t *tokenResponse) {
	a.AccessToken, a.RefreshToken, a.TokenType = t.AccessToken, t.RefreshToken, t.TokenType
	a.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	a.EarliestRefresh = t.earliestRefresh()
	if t.Scope != "" {
		a.Scopes = strings.Fields(t.Scope)
	}
	if t.IDToken != "" {
		a.IDToken = t.IDToken
	}
}

// Credentials supplies an account's access tokens for inference.
func (m *Manager) Credentials(accountID string) *Credentials {
	return &Credentials{m: m, id: accountID}
}

type Credentials struct {
	m  *Manager
	id string
}

// Token returns a current access token, refreshing one that expires soon.
func (c *Credentials) Token(ctx context.Context) (string, error) {
	return c.m.token(ctx, c.id, "")
}

// Refresh replaces rejected, unless another request already did.
func (c *Credentials) Refresh(ctx context.Context, rejected string) (string, error) {
	return c.m.token(ctx, c.id, rejected)
}

func (m *Manager) token(ctx context.Context, id, rejected string) (string, error) {
	unlock := m.lock(id)
	token, changed, err := m.lockedToken(ctx, id, rejected)
	unlock()
	if changed {
		m.changed()
	}
	return token, err
}

// lockedToken is token with the account locked. changed reports whether the
// account was signed out.
func (m *Manager) lockedToken(ctx context.Context, id, rejected string) (token string, changed bool, err error) {
	a, err := m.store.Get(ctx, id)
	if err != nil {
		return "", false, err
	}
	if !a.SignedIn() {
		return "", false, ErrSignInRequired
	}
	if !a.PlanUsage() {
		return "", false, errors.New("this ChatGPT account has not allowed Shelley to use its plan; enable it under Manage models")
	}
	now := time.Now()
	usable := a.AccessToken != rejected && now.Before(a.ExpiresAt)
	if usable && a.ExpiresAt.Sub(now) > 2*time.Minute {
		return a.AccessToken, false, nil
	}
	if now.Before(a.EarliestRefresh) {
		if usable {
			return a.AccessToken, false, nil
		}
		return "", false, fmt.Errorf("ChatGPT will not renew this account's credentials until %s; try again then", a.EarliestRefresh.Format(time.Kitchen))
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	// Once sent, a refresh spends the refresh token, so finish it and save the
	// replacement even if the caller gives up: losing it signs the account out.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	t, err := m.client.refresh(ctx, a.ClientID, a.RefreshToken)
	if e := (*Error)(nil); errors.As(err, &e) && e.terminal() {
		return "", true, m.signOutLocked(ctx, a, fmt.Errorf("%w (%v)", ErrSignInRequired, e))
	}
	if e := (*Error)(nil); errors.As(err, &e) && e.Code == "invalid_client" {
		return "", false, errors.New("ChatGPT no longer recognizes Shelley's registration for this account; remove it under Manage models and add it again")
	}
	if err != nil {
		return "", false, fmt.Errorf("refresh ChatGPT credentials: %w", err)
	}
	if t.IDToken != "" {
		claims, err := m.client.verifyIDToken(ctx, t.IDToken, a.ClientID, "")
		if err == nil && claims.Subject != a.Subject {
			err = errors.New("renewed ChatGPT credentials are for a different person")
		}
		if err != nil {
			return "", true, m.signOutLocked(ctx, a, fmt.Errorf("%w (%v)", ErrSignInRequired, err))
		}
	}
	a.setTokens(t)
	if err := m.store.Put(ctx, a); err != nil {
		return "", false, err
	}
	return a.AccessToken, false, nil
}

// signOutLocked clears a's tokens after err ended its session.
func (m *Manager) signOutLocked(ctx context.Context, a *Account, err error) error {
	a.signOut()
	if putErr := m.store.Put(ctx, a); putErr != nil {
		return errors.Join(err, putErr)
	}
	return err
}

// ErrNotRevoked means local tokens were cleared but OpenAI did not confirm
// ending the session.
var ErrNotRevoked = errors.New("signed out here, but ChatGPT did not confirm ending the session; disconnect Shelley in ChatGPT settings")

// SignOut ends the account's session and clears its tokens, keeping its
// registration for signing in again. remove also forgets the registration.
func (m *Manager) SignOut(ctx context.Context, id string, remove bool) error {
	unlock := m.lock(id)
	a, err := m.store.Get(ctx, id)
	if err != nil {
		unlock()
		return err
	}
	var revokeErr error
	if a.SignedIn() {
		revokeErr = m.client.revoke(ctx, a.ClientID, a.RefreshToken)
	}
	a.signOut()
	if remove {
		err = m.store.Delete(ctx, id)
	} else {
		err = m.store.Put(ctx, a)
	}
	unlock()
	m.changed()
	if err != nil {
		return err
	}
	if revokeErr != nil {
		return fmt.Errorf("%w: %v", ErrNotRevoked, revokeErr)
	}
	return nil
}

// API is the base URL of the API the accounts' tokens are for.
func (m *Manager) API() string { return m.client.API }
