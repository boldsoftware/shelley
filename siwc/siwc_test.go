package siwc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

// fakeOpenAI is an authorization server and model catalog in the shape of
// OpenAI's, which issues tokens for one user.
type fakeOpenAI struct {
	t      *testing.T
	server *httptest.Server
	key    *rsa.PrivateKey

	mu         sync.Mutex
	subject    string
	scope      string
	nonce      string          // of the authorization being answered
	tokenForms []url.Values    // received by the token endpoint
	refreshErr string          // returned for refresh grants, if set
	revokes    []url.Values    //
	revokeFail bool            //
	rotation   int             // tokens issued so far
	valid      map[string]bool // refresh tokens that still work
	earliest   any             // earliest_refresh_at to send, if set
	onToken    func()          // called as each token request is answered
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOpenAI{t: t, key: key, subject: "user-1", scope: Scopes, valid: map[string]bool{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOpenAI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		u := f.server.URL
		json.NewEncoder(w).Encode(map[string]string{
			"issuer": u, "authorization_endpoint": u + "/api/accounts/authorize", "token_endpoint": u + "/api/accounts/oauth/token",
			"revocation_endpoint": u + "/api/accounts/oauth/revoke", "jwks_uri": u + "/.well-known/jwks.json",
		})
	case "/.well-known/jwks.json":
		e := big.NewInt(int64(f.key.E)).Bytes()
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e),
		}}})
	case "/api/accounts/oauth/token":
		r.ParseForm()
		f.tokenForms = append(f.tokenForms, r.PostForm)
		if f.onToken != nil {
			f.onToken()
		}
		clientID := r.PostForm.Get("client_id")
		if r.PostForm.Get("grant_type") == "refresh_token" {
			if f.refreshErr != "" || !f.valid[r.PostForm.Get("refresh_token")] {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": firstNonEmpty(f.refreshErr, "refresh_token_reused")})
				return
			}
			delete(f.valid, r.PostForm.Get("refresh_token"))
			f.issue(w, clientID, "", "")
			return
		}
		if r.PostForm.Get("code") != "code-1" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		f.issue(w, clientID, f.nonce, f.scope)
	case "/api/accounts/oauth/revoke":
		r.ParseForm()
		f.revokes = append(f.revokes, r.PostForm)
		if f.revokeFail {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	case "/v1/models":
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer access-") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"models":[{"slug":"gpt-6.1-sol","display_name":"GPT-6.1 Sol","visibility":"list"},`+
			`{"slug":"internal","display_name":"Internal","visibility":"hide"},{"slug":"gpt-6-luna","display_name":"GPT-6 Luna","visibility":"list"}]}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOpenAI) issue(w http.ResponseWriter, clientID, nonce, scope string) {
	f.rotation++
	resp := map[string]any{
		"access_token": fmt.Sprintf("access-%d", f.rotation), "token_type": "Bearer",
		"expires_in": 3600, "scope": scope, "id_token": f.idToken(clientID, nonce),
	}
	// Refresh tokens come with offline_access, which refreshes keep.
	if scope == "" || slices.Contains(strings.Fields(scope), "offline_access") {
		refresh := fmt.Sprintf("refresh-%d", f.rotation)
		f.valid[refresh] = true
		resp["refresh_token"] = refresh
	}
	if f.earliest != nil {
		resp["earliest_refresh_at"] = f.earliest
	}
	json.NewEncoder(w).Encode(resp)
}

func (f *fakeOpenAI) idToken(clientID, nonce string) string {
	return signJWT(f.t, f.key, map[string]any{
		"iss": f.server.URL, "sub": f.subject, "aud": clientID, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"nonce": nonce, "email": "ada@example.com", "name": "Ada",
	})
}

func signJWT(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	body, _ := json.Marshal(claims)
	signed := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

type harness struct {
	fake    *fakeOpenAI
	store   *Store
	manager *Manager
}

func newHarness(t *testing.T) *harness {
	fake := newFakeOpenAI(t)
	database, closeDB := db.NewTestDB(t)
	t.Cleanup(closeDB)
	client := &Client{HTTPC: fake.server.Client(), Issuer: fake.server.URL, API: fake.server.URL + "/v1"}
	store := NewStore(database)
	return &harness{fake: fake, store: store, manager: NewManager(store, client)}
}

// authorize plays the user approving the sign-in in a browser, returning the
// authorize parameters and the callback URL the browser is sent to.
func (h *harness) authorize(t *testing.T, accountID string, callback url.Values) (url.Values, string) {
	signIn, err := h.manager.StartSignIn(t.Context(), accountID, false)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(signIn.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	params := u.Query()
	h.fake.mu.Lock()
	h.fake.nonce = params.Get("nonce")
	h.fake.mu.Unlock()
	query := url.Values{"code": {"code-1"}, "state": {params.Get("state")}}
	for k, v := range callback {
		query[k] = v
	}
	return params, params.Get("redirect_uri") + "?" + query.Encode()
}

func (h *harness) signInNew(t *testing.T) *Result {
	_, callback := h.authorize(t, "", url.Values{"client_id": {"app_issued"}})
	result, err := h.manager.CompleteURL(t.Context(), callback)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRegistrationThroughLoopbackCallback(t *testing.T) {
	h := newHarness(t)
	params, callback := h.authorize(t, "", url.Values{"client_id": {"app_issued"}})

	if got := params.Get("client_id"); got != registrationClientID {
		t.Errorf("client_id = %q", got)
	}
	if got := params.Get("agent_name_hint"); got != AgentName {
		t.Errorf("agent_name_hint = %q", got)
	}
	hostID, _ := h.store.HostID(t.Context())
	if got := params.Get("ext_agent_host_id"); got != hostID || !strings.HasPrefix(got, "urn:uuid:") {
		t.Errorf("ext_agent_host_id = %q, host ID %q", got, hostID)
	}
	if params.Get("scope") != Scopes || params.Get("resource") != Resource || params.Get("code_challenge_method") != "S256" {
		t.Errorf("authorize params = %v", params)
	}
	if !strings.HasPrefix(params.Get("redirect_uri"), "http://127.0.0.1:") || !strings.HasSuffix(params.Get("redirect_uri"), "/auth/callback") {
		t.Errorf("redirect_uri = %q", params.Get("redirect_uri"))
	}

	// The browser runs on this host, so it reaches the listener itself.
	resp, err := http.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status %d", resp.StatusCode)
	}

	form := h.fake.tokenForms[0]
	if form.Get("client_id") != "app_issued" || form.Get("redirect_uri") != params.Get("redirect_uri") || form.Get("resource") != Resource ||
		pkceChallenge(form.Get("code_verifier")) != params.Get("code_challenge") {
		t.Errorf("token form = %v", form)
	}
	accounts, err := h.manager.Accounts(t.Context())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts = %v, %v", accounts, err)
	}
	a := accounts[0]
	if !a.PlanUsage || a.Email != "ada@example.com" || len(a.Models) != 2 || a.Models[1].Slug != "gpt-6-luna" {
		t.Errorf("account = %+v", a)
	}
	token, err := h.manager.Credentials(a.ID).Token(t.Context())
	if err != nil || token != "access-1" {
		t.Errorf("token = %q, %v", token, err)
	}
}

// The issued client ID must survive a failed exchange so retrying signs in to
// the same registration instead of creating another.
func TestFailedExchangeKeepsRegistration(t *testing.T) {
	h := newHarness(t)
	_, callback := h.authorize(t, "", url.Values{"client_id": {"app_issued"}, "code": {"stale"}})
	if _, err := h.manager.CompleteURL(t.Context(), callback); err == nil {
		t.Fatal("exchange of a bad code succeeded")
	}
	accounts, _ := h.store.List(t.Context())
	if len(accounts) != 1 || accounts[0].ClientID != "app_issued" || accounts[0].SignedIn() {
		t.Fatalf("accounts = %+v", accounts)
	}

	params, callback := h.authorize(t, accounts[0].ID, nil)
	if params.Get("client_id") != "app_issued" || params.Has("agent_name_hint") {
		t.Errorf("retry authorize params = %v", params)
	}
	// Safari copies the address without its scheme.
	callback = "  " + strings.TrimPrefix(callback, "http://") + "\n"
	if _, err := h.manager.CompleteURL(t.Context(), callback); err != nil {
		t.Fatal(err)
	}
	if accounts, _ := h.store.List(t.Context()); len(accounts) != 1 || !accounts[0].PlanUsage() {
		t.Fatalf("accounts after retry = %+v", accounts)
	}
}

func TestCallbackValidation(t *testing.T) {
	h := newHarness(t)
	id := h.signInNew(t).Account.ID

	tests := []struct {
		name     string
		callback func(callback string) string
		params   url.Values
		want     string
	}{
		{"wrong state", func(c string) string { return strings.Replace(c, "state=", "state=x", 1) }, nil, "not from a pending sign-in"},
		{"other client", nil, url.Values{"client_id": {"app_other"}}, "different registration"},
		{"declined", nil, url.Values{"error": {"access_denied"}}, "cancelled"},
		{"wrong port", func(c string) string { return strings.Replace(c, "127.0.0.1:", "127.0.0.1:1", 1) }, nil, "not for"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, callback := h.authorize(t, id, tt.params)
			if tt.callback != nil {
				callback = tt.callback(callback)
			}
			if _, err := h.manager.CompleteURL(t.Context(), callback); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestIDTokenMustMatchSignIn(t *testing.T) {
	h := newHarness(t)
	_, callback := h.authorize(t, "", url.Values{"client_id": {"app_issued"}})
	h.fake.nonce = "replayed"
	if _, err := h.manager.CompleteURL(t.Context(), callback); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("err = %v, want nonce mismatch", err)
	}

	forged, _ := rsa.GenerateKey(rand.Reader, 2048)
	h.fake.key = forged // signs with a key the JWKS no longer matches once cached
	if _, err := h.manager.client.verifyIDToken(t.Context(), h.fake.idToken("app_issued", ""), "app_issued", ""); err == nil {
		t.Fatal("token signed by an unpublished key verified")
	}

	// Signing in again as a different person must not take over the account.
	h = newHarness(t)
	id := h.signInNew(t).Account.ID
	h.fake.subject = "user-2"
	_, callback = h.authorize(t, id, nil)
	if _, err := h.manager.CompleteURL(t.Context(), callback); err == nil || !strings.Contains(err.Error(), "different ChatGPT account") {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanUsageNeedsGrantedScope(t *testing.T) {
	h := newHarness(t)
	h.fake.scope = "openid profile email offline_access"
	result := h.signInNew(t)
	if result.Account.PlanUsage || len(result.Account.Models) != 0 {
		t.Fatalf("account = %+v", result.Account)
	}
	if _, err := h.manager.Credentials(result.Account.ID).Token(t.Context()); err == nil {
		t.Fatal("got a token for inference without plan usage")
	}
}

func TestRefreshRotatesTokens(t *testing.T) {
	h := newHarness(t)
	id := h.signInNew(t).Account.ID
	creds := h.manager.Credentials(id)

	// Concurrent requests that saw the same rejected token share one refresh;
	// a second refresh would present a rotated-out refresh token.
	var wg sync.WaitGroup
	tokens := make([]string, 4)
	for i := range tokens {
		wg.Go(func() {
			token, err := creds.Refresh(t.Context(), "access-1")
			if err != nil {
				t.Error(err)
			}
			tokens[i] = token
		})
	}
	wg.Wait()
	for _, token := range tokens {
		if token != "access-2" {
			t.Fatalf("tokens = %v", tokens)
		}
	}
	a, _ := h.store.Get(t.Context(), id)
	if a.RefreshToken != "refresh-2" || !a.PlanUsage() {
		t.Fatalf("after refresh: %+v", a)
	}
	if form := h.fake.tokenForms[len(h.fake.tokenForms)-1]; form.Has("scope") || form.Get("refresh_token") != "refresh-1" {
		t.Errorf("refresh form = %v", form)
	}

	// An access token about to expire is refreshed before use.
	a.ExpiresAt = time.Now().Add(time.Minute)
	h.store.Put(t.Context(), a)
	if token, err := creds.Token(t.Context()); err != nil || token != "access-3" {
		t.Fatalf("token = %q, %v", token, err)
	}
}

func TestRevokedRefreshSignsOut(t *testing.T) {
	h := newHarness(t)
	id := h.signInNew(t).Account.ID
	changed := 0
	h.manager.Changed = func() { changed++ }
	h.fake.refreshErr = "refresh_token_invalidated"

	if _, err := h.manager.Credentials(id).Refresh(t.Context(), "access-1"); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("err = %v", err)
	}
	a, _ := h.store.Get(t.Context(), id)
	if a.SignedIn() || a.ClientID != "app_issued" || changed != 1 {
		t.Fatalf("account = %+v, changed %d", a, changed)
	}
	if _, err := h.manager.Credentials(id).Token(t.Context()); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("err = %v", err)
	}
}

func TestSignOut(t *testing.T) {
	h := newHarness(t)
	id := h.signInNew(t).Account.ID
	hostID, _ := h.store.HostID(t.Context())
	if err := h.manager.SignOut(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.revokes[0]; got.Get("token") != "refresh-1" || got.Get("client_id") != "app_issued" {
		t.Errorf("revoke form = %v", got)
	}
	a, _ := h.store.Get(t.Context(), id)
	if a.SignedIn() || a.ClientID != "app_issued" {
		t.Fatalf("after sign out: %+v", a)
	}

	// When OpenAI does not confirm, tokens are still gone and the user is told.
	_, callback := h.authorize(t, id, nil)
	if _, err := h.manager.CompleteURL(t.Context(), callback); err != nil {
		t.Fatal(err)
	}
	h.fake.revokeFail = true
	if err := h.manager.SignOut(t.Context(), id, true); !errors.Is(err, ErrNotRevoked) {
		t.Fatalf("err = %v", err)
	}
	if len(h.fake.revokes) != 3 {
		t.Errorf("revocation attempts = %d, want a retry", len(h.fake.revokes)-1)
	}
	if accounts, _ := h.store.List(t.Context()); len(accounts) != 0 {
		t.Fatalf("accounts = %+v", accounts)
	}
	if after, _ := h.store.HostID(t.Context()); after != hostID {
		t.Errorf("host ID = %q after removing the account, was %q", after, hostID)
	}
}

func TestIdentityOnlySignIn(t *testing.T) {
	// Declining plan usage still signs in, so the user can enable it later.
	h := newHarness(t)
	h.fake.scope = "openid profile email"
	result := h.signInNew(t)
	if !result.Account.SignedIn || result.Account.PlanUsage || result.Account.Email != "ada@example.com" {
		t.Fatalf("account = %+v", result.Account)
	}
	if _, err := h.manager.Credentials(result.Account.ID).Token(t.Context()); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("err = %v", err)
	}
}

func TestSignInAgainReusesRegistration(t *testing.T) {
	// OpenAI may hand back an existing registration's client ID.
	h := newHarness(t)
	first := h.signInNew(t)
	second := h.signInNew(t)
	if accounts, _ := h.store.List(t.Context()); len(accounts) != 1 || second.Account.ID != first.Account.ID {
		t.Fatalf("accounts = %+v", accounts)
	}
}

func TestCancelledRefreshKeepsRotatedToken(t *testing.T) {
	h := newHarness(t)
	id := h.signInNew(t).Account.ID

	// The request gives up after OpenAI rotated the refresh token. The
	// replacement must still be saved: the old one is spent.
	ctx, cancel := context.WithCancel(t.Context())
	h.fake.onToken = cancel
	h.manager.Credentials(id).Refresh(ctx, "access-1")
	h.fake.onToken = nil

	a, _ := h.store.Get(t.Context(), id)
	if a.RefreshToken != "refresh-2" || a.AccessToken != "access-2" {
		t.Fatalf("after cancelled refresh: refresh %q, access %q", a.RefreshToken, a.AccessToken)
	}
	if token, err := h.manager.Credentials(id).Refresh(t.Context(), "access-2"); err != nil || token != "access-3" {
		t.Fatalf("next refresh = %q, %v", token, err)
	}
}

func TestRefreshForAnotherPersonSignsOut(t *testing.T) {
	h := newHarness(t)
	id := h.signInNew(t).Account.ID
	changed := 0
	h.manager.Changed = func() { changed++ }
	h.fake.subject = "user-2"

	if _, err := h.manager.Credentials(id).Refresh(t.Context(), "access-1"); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("err = %v", err)
	}
	if a, _ := h.store.Get(t.Context(), id); a.SignedIn() || a.AccessToken != "" || changed != 1 {
		t.Fatalf("account = %+v, changed %d", a, changed)
	}
}

func TestEarliestRefreshAt(t *testing.T) {
	h := newHarness(t)
	h.fake.earliest = time.Now().Add(time.Hour).Unix()
	id := h.signInNew(t).Account.ID
	creds := h.manager.Credentials(id)
	forms := len(h.fake.tokenForms)

	// Near expiry, the current token is still used until a refresh is allowed.
	a, _ := h.store.Get(t.Context(), id)
	a.ExpiresAt = time.Now().Add(time.Minute)
	h.store.Put(t.Context(), a)
	if token, err := creds.Token(t.Context()); err != nil || token != "access-1" {
		t.Fatalf("token = %q, %v", token, err)
	}
	if _, err := creds.Refresh(t.Context(), "access-1"); err == nil || !strings.Contains(err.Error(), "will not renew") {
		t.Fatalf("err = %v", err)
	}
	if len(h.fake.tokenForms) != forms {
		t.Fatal("refreshed before earliest_refresh_at")
	}

	h.fake.earliest = time.Now().Add(-time.Minute).Format(time.RFC3339)
	a.EarliestRefresh = time.Now().Add(-time.Minute)
	h.store.Put(t.Context(), a)
	if token, err := creds.Refresh(t.Context(), "access-1"); err != nil || token != "access-2" {
		t.Fatalf("token = %q, %v", token, err)
	}
}

func TestEndedSignInCannotComplete(t *testing.T) {
	h := newHarness(t)
	params, callback := h.authorize(t, "", url.Values{"client_id": {"app_issued"}})
	var a *attempt
	for _, candidate := range h.manager.attempts {
		if candidate.state == params.Get("state") {
			a = candidate
		}
	}
	h.manager.finish(a, nil, errors.New("sign-in timed out; start again"))
	if _, err := h.manager.CompleteURL(t.Context(), callback); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if len(h.fake.tokenForms) != 0 {
		t.Fatal("ended sign-in exchanged its code")
	}
}

func TestLoopbackRejectsOtherRequests(t *testing.T) {
	h := newHarness(t)
	_, callback := h.authorize(t, "", url.Values{"client_id": {"app_issued"}})

	// A page elsewhere can POST to 127.0.0.1, and DNS rebinding sends
	// requests with another Host; neither may finish the sign-in.
	post, _ := http.NewRequest(http.MethodPost, callback, nil)
	rebound, _ := http.NewRequest(http.MethodGet, callback, nil)
	rebound.Host = "evil.example:80"
	for _, req := range []*http.Request{post, rebound} {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s Host %s: status %d", req.Method, req.Host, resp.StatusCode)
		}
	}

	resp, err := http.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Signed in") {
		t.Fatalf("callback page = %s", body)
	}
}

func TestIDTokenClaims(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	valid := map[string]any{"iss": h.fake.server.URL, "sub": "user-1", "aud": "app_issued", "exp": now.Add(time.Hour).Unix(), "iat": now.Unix()}
	tests := []struct {
		name   string
		change map[string]any
	}{
		{"other issuer", map[string]any{"iss": "https://evil.example"}},
		{"other audience", map[string]any{"aud": "app_other"}},
		{"several audiences without azp", map[string]any{"aud": []string{"app_issued", "app_other"}}},
		{"expired", map[string]any{"exp": now.Add(-time.Hour).Unix()}},
		{"no issue time", map[string]any{"iat": nil}},
		{"no subject", map[string]any{"sub": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := maps.Clone(valid)
			for k, v := range tt.change {
				if v == nil {
					delete(claims, k)
				} else {
					claims[k] = v
				}
			}
			if _, err := h.manager.client.verifyIDToken(t.Context(), signJWT(t, h.fake.key, claims), "app_issued", ""); err == nil {
				t.Fatal("verified")
			}
		})
	}
	if _, err := h.manager.client.verifyIDToken(t.Context(), signJWT(t, h.fake.key, valid), "app_issued", ""); err != nil {
		t.Fatalf("valid token: %v", err)
	}
}
