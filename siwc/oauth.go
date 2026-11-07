// Package siwc lets Shelley use a ChatGPT plan through Sign in with ChatGPT:
// it registers Shelley as the user's agent, keeps the resulting tokens on this
// host, and lists the plan's models. Requests go straight to the public
// Responses API; see https://developers.openai.com/siwc/token-sharing-open-source.
package siwc

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	Issuer    = "https://auth.openai.com"
	Resource  = "https://api.openai.com/v1"
	Scopes    = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	PlanScope = "chatgpt.tokens.use.direct"
	AgentName = "Shelley"

	registrationClientID = "dynamic_agent_client"
	callbackPath         = "/auth/callback"
)

// Error is an OAuth or API error with its machine-readable code.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	msg := e.Code
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.Status != 0 {
		msg = fmt.Sprintf("HTTP %d: %s", e.Status, msg)
	}
	return msg
}

// terminal reports whether a refresh error means the refresh token can never
// work again, so the user has to sign in.
func (e *Error) terminal() bool {
	switch e.Code {
	case "invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused":
		return true
	}
	return false
}

// Client talks to OpenAI's authorization server and model catalog.
type Client struct {
	HTTPC  *http.Client
	Issuer string // the authorization server; tests use a fake
	API    string // the resource's API base

	configMu  sync.Mutex // held while fetching discovery
	discovery *discovery

	mu   sync.Mutex
	keys map[string]*rsa.PublicKey // by kid
}

type discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

func NewClient(httpc *http.Client) *Client {
	return &Client{HTTPC: httpc, Issuer: Issuer, API: Resource}
}

func (c *Client) get(ctx context.Context, target, bearer string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return c.do(req, v)
}

func (c *Client) post(ctx context.Context, target string, form url.Values, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return c.do(req, v)
}

// do never follows redirects, which could carry a code or token elsewhere.
func (c *Client) do(req *http.Request, v any) error {
	httpc := *c.HTTPC
	httpc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return responseError(resp.StatusCode, body)
	}
	if v == nil {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode %s response: %w", req.URL.Path, err)
	}
	return nil
}

// responseError reads OAuth ({"error","error_description"}), API
// ({"error":{"code","message"}}), and admission ({"detail"}) error bodies.
func responseError(status int, body []byte) error {
	var raw struct {
		Error       json.RawMessage `json:"error"`
		Description string          `json:"error_description"`
		Detail      string          `json:"detail"`
	}
	e := &Error{Status: status}
	if json.Unmarshal(body, &raw) == nil {
		var api struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw.Error, &e.Code) != nil && json.Unmarshal(raw.Error, &api) == nil {
			e.Code, e.Message = api.Code, api.Message
		}
		e.Message = firstNonEmpty(e.Message, raw.Description, raw.Detail)
	}
	if e.Code == "" && e.Message == "" {
		e.Message = strings.TrimSpace(string(body[:min(len(body), 200)]))
	}
	return e
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (c *Client) config(ctx context.Context) (*discovery, error) {
	c.configMu.Lock()
	defer c.configMu.Unlock()
	if c.discovery != nil {
		return c.discovery, nil
	}
	var d discovery
	if err := c.get(ctx, c.Issuer+"/.well-known/openid-configuration", "", &d); err != nil {
		return nil, fmt.Errorf("ChatGPT sign-in configuration: %w", err)
	}
	if d.Issuer != c.Issuer {
		return nil, fmt.Errorf("ChatGPT sign-in configuration names issuer %q", d.Issuer)
	}
	for _, endpoint := range []string{d.AuthorizationEndpoint, d.TokenEndpoint, d.JWKSURI, d.RevocationEndpoint} {
		if u, err := url.Parse(endpoint); endpoint != "" && (err != nil || u.Scheme+"://"+u.Host != c.Issuer) {
			return nil, fmt.Errorf("ChatGPT sign-in endpoint %q is not on %s", endpoint, c.Issuer)
		}
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.JWKSURI == "" {
		return nil, errors.New("ChatGPT sign-in configuration is incomplete")
	}
	c.discovery = &d
	return &d, nil
}

// tokenResponse is the token endpoint's answer to a code exchange or refresh.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	// EarliestRefreshAt is when the server will accept a refresh: a Unix
	// time or an RFC 3339 string.
	EarliestRefreshAt json.RawMessage `json:"earliest_refresh_at,omitempty"`
}

// earliestRefresh parses EarliestRefreshAt; zero means any time.
func (t *tokenResponse) earliestRefresh() time.Time {
	var n float64
	if json.Unmarshal(t.EarliestRefreshAt, &n) == nil && n > 0 {
		return time.Unix(int64(n), 0)
	}
	var s string
	if json.Unmarshal(t.EarliestRefreshAt, &s) == nil {
		if when, err := time.Parse(time.RFC3339, s); err == nil {
			return when
		}
	}
	return time.Time{}
}

func (c *Client) token(ctx context.Context, form url.Values) (*tokenResponse, error) {
	d, err := c.config(ctx)
	if err != nil {
		return nil, err
	}
	form.Set("resource", Resource)
	var t tokenResponse
	if err := c.post(ctx, d.TokenEndpoint, form, &t); err != nil {
		return nil, err
	}
	// Without offline_access (an identity-only sign-in) there is no
	// refresh token; a refresh always returns its replacement.
	needRefresh := form.Get("grant_type") == "refresh_token" || slices.Contains(strings.Fields(t.Scope), "offline_access")
	if t.AccessToken == "" || (needRefresh && t.RefreshToken == "") || t.ExpiresIn <= 0 || !strings.EqualFold(t.TokenType, "bearer") {
		return nil, errors.New("ChatGPT returned incomplete credentials")
	}
	return &t, nil
}

func (c *Client) exchange(ctx context.Context, clientID, code, verifier, redirectURI string) (*tokenResponse, error) {
	return c.token(ctx, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"code_verifier": {verifier}, "redirect_uri": {redirectURI},
	})
}

// refresh omits scope to keep the grant; the refresh token rotates.
func (c *Client) refresh(ctx context.Context, clientID, refreshToken string) (*tokenResponse, error) {
	return c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refreshToken}})
}

// revoke ends the renewable session. It retries once after a server or network failure.
func (c *Client) revoke(ctx context.Context, clientID, refreshToken string) error {
	d, err := c.config(ctx)
	if err != nil {
		return err
	}
	if d.RevocationEndpoint == "" {
		return errors.New("ChatGPT offers no revocation endpoint")
	}
	form := url.Values{"token": {refreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {clientID}}
	err = c.post(ctx, d.RevocationEndpoint, form, nil)
	if e := (*Error)(nil); err != nil && (!errors.As(err, &e) || e.Status >= 500) {
		err = c.post(ctx, d.RevocationEndpoint, form, nil)
	}
	return err
}

// ListModels returns the models the access token's plan offers, in the server's order.
func (c *Client) ListModels(ctx context.Context, accessToken string) ([]Model, error) {
	var catalog struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if err := c.get(ctx, c.API+"/models", accessToken, &catalog); err != nil {
		return nil, fmt.Errorf("list ChatGPT models: %w", err)
	}
	models := []Model{}
	for _, m := range catalog.Models {
		if m.Visibility != "list" {
			continue
		}
		if m.Slug == "" {
			return nil, errors.New("list ChatGPT models: a model has no slug")
		}
		models = append(models, Model{Slug: m.Slug, DisplayName: firstNonEmpty(m.DisplayName, m.Slug)})
	}
	return models, nil
}

// idClaims are the ID token claims Shelley uses.
type idClaims struct {
	Issuer    string          `json:"iss"`
	Subject   string          `json:"sub"`
	Audience  json.RawMessage `json:"aud"`
	AZP       string          `json:"azp"`
	Expiry    float64         `json:"exp"`
	IssuedAt  float64         `json:"iat"`
	Nonce     string          `json:"nonce"`
	Email     string          `json:"email"`
	Name      string          `json:"name"`
	signature []byte
	signed    string
	kid, alg  string
}

func parseIDToken(raw string) (*idClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("ID token is not a JWT")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	var claims idClaims
	for i, v := range []any{&header, &claims} {
		data, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			return nil, fmt.Errorf("ID token: %w", err)
		}
		if err := json.Unmarshal(data, v); err != nil {
			return nil, fmt.Errorf("ID token: %w", err)
		}
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("ID token signature: %w", err)
	}
	claims.signature, claims.signed, claims.kid, claims.alg = sig, parts[0]+"."+parts[1], header.Kid, header.Alg
	return &claims, nil
}

// check validates the claims for clientID. An empty nonce skips the nonce check.
func (c *idClaims) check(issuer, clientID, nonce string, now time.Time) error {
	var audiences []string
	if json.Unmarshal(c.Audience, &audiences) != nil {
		var single string
		json.Unmarshal(c.Audience, &single)
		audiences = []string{single}
	}
	switch {
	case c.Issuer != issuer:
		return fmt.Errorf("ID token issuer is %q", c.Issuer)
	case !contains(audiences, clientID) || (c.AZP != "" && c.AZP != clientID) || (len(audiences) > 1 && c.AZP != clientID):
		return errors.New("ID token is for another client")
	case c.Subject == "":
		return errors.New("ID token has no subject")
	case now.After(time.Unix(int64(c.Expiry), 0).Add(time.Minute)):
		return errors.New("ID token expired")
	case c.IssuedAt <= 0 || time.Unix(int64(c.IssuedAt), 0).After(now.Add(5*time.Minute)):
		return errors.New("ID token has no valid issue time")
	case nonce != "" && c.Nonce != nonce:
		return errors.New("ID token nonce does not match this sign-in")
	}
	return nil
}

func contains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

// verifyIDToken checks the token's RS256 signature against OpenAI's published
// keys, then its claims.
func (c *Client) verifyIDToken(ctx context.Context, raw, clientID, nonce string) (*idClaims, error) {
	claims, err := parseIDToken(raw)
	if err != nil {
		return nil, err
	}
	if claims.alg != "RS256" {
		return nil, fmt.Errorf("ID token algorithm %q is not RS256", claims.alg)
	}
	key, err := c.key(ctx, claims.kid)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(claims.signed))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], claims.signature); err != nil {
		return nil, errors.New("ID token signature is invalid")
	}
	if err := claims.check(c.Issuer, clientID, nonce, time.Now()); err != nil {
		return nil, err
	}
	return claims, nil
}

// key returns the signing key kid, fetching the key set again for unknown kids
// so key rotation works.
func (c *Client) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	key := c.keys[kid]
	c.mu.Unlock()
	if key != nil {
		return key, nil
	}
	d, err := c.config(ctx)
	if err != nil {
		return nil, err
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := c.get(ctx, d.JWKSURI, "", &set); err != nil {
		return nil, fmt.Errorf("ChatGPT signing keys: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, k := range set.Keys {
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") || errN != nil || errE != nil || len(e) > 4 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	c.mu.Lock()
	c.keys = keys
	c.mu.Unlock()
	if keys[kid] == nil {
		return nil, fmt.Errorf("ID token signing key %q is not published", kid)
	}
	return keys[kid], nil
}
