package mcptest

// Loosely adapted from go-sdk's internal/oauthtest (MIT).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// AuthServer is a fake OAuth authorization server protecting a Fixture at
// MCP.URL+"/mcp": metadata, dynamic client registration, an authorize
// endpoint that approves every request, and a token endpoint
// (authorization_code with PKCE, refresh_token with rotation).
type AuthServer struct {
	*httptest.Server
	MCP *httptest.Server

	mu        sync.Mutex
	expiresIn int // seconds; 0 omits expires_in
	refuseReg bool
	rejectAll bool
	clients   map[string]string // ID to secret
	codes     map[string]string // code to PKCE challenge
	access    map[string]bool   // valid access tokens
	refresh   map[string]bool   // valid refresh tokens
	stats     AuthStats
}

// AuthStats counts requests to an AuthServer.
type AuthStats struct {
	Registrations, Refreshes int
	// UserAgents are the distinct User-Agents of requests to the
	// authorization server or the MCP server, except a browser's to /authorize.
	UserAgents []string
}

// NewAuthServer starts an AuthServer issuing access tokens that live for
// an hour, and the MCP server it protects.
func NewAuthServer(t testing.TB) *AuthServer {
	s := &AuthServer{expiresIn: 3600, clients: map[string]string{}, codes: map[string]string{}, access: map[string]bool{}, refresh: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.metadata)
	mux.HandleFunc("POST /register", s.register)
	mux.HandleFunc("GET /authorize", s.authorize)
	mux.HandleFunc("POST /token", s.token)
	s.Server = httptest.NewServer(s.recordUserAgent(mux))
	t.Cleanup(s.Close)

	mcp := http.NewServeMux()
	s.MCP = httptest.NewServer(s.recordUserAgent(mcp))
	t.Cleanup(s.MCP.Close)
	prm := "/.well-known/oauth-protected-resource/mcp"
	mcp.Handle("/mcp", auth.RequireBearerToken(s.verify, &auth.RequireBearerTokenOptions{ResourceMetadataURL: s.MCP.URL + prm})(newFixture().handler()))
	mcp.Handle(prm, auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:             s.MCP.URL + "/mcp",
		AuthorizationServers: []string{s.URL},
		ScopesSupported:      []string{"read"},
	}))
	return s
}

// SetExpiresIn sets the expires_in of tokens issued from now on; 0 leaves
// it out. Tokens stay valid until revoked, whatever it says.
func (s *AuthServer) SetExpiresIn(seconds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expiresIn = seconds
}

// RefuseRegistration makes registration answer a bare 403, as Figma does
// for clients it hasn't approved.
func (s *AuthServer) RefuseRegistration() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuseReg = true
}

// RejectTokens makes the MCP server reject every access token.
func (s *AuthServer) RejectTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejectAll = true
}

// Stats returns the request counts.
func (s *AuthServer) Stats() AuthStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats
	st.UserAgents = slices.Clone(st.UserAgents)
	return st
}

func (s *AuthServer) recordUserAgent(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/authorize" {
			s.mu.Lock()
			if ua := r.UserAgent(); !slices.Contains(s.stats.UserAgents, ua) {
				s.stats.UserAgents = append(s.stats.UserAgents, ua)
			}
			s.mu.Unlock()
		}
		h.ServeHTTP(w, r)
	})
}

// RevokeAccessTokens invalidates the access tokens issued so far.
func (s *AuthServer) RevokeAccessTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.access)
}

// RevokeRefreshTokens invalidates the refresh tokens issued so far.
func (s *AuthServer) RevokeRefreshTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.refresh)
}

func (s *AuthServer) verify(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.access[token] || s.rejectAll {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{Expiration: time.Now().Add(time.Hour)}, nil
}

func (s *AuthServer) metadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, &oauthex.AuthServerMeta{
		Issuer:                            s.URL,
		AuthorizationEndpoint:             s.URL + "/authorize",
		TokenEndpoint:                     s.URL + "/token",
		RegistrationEndpoint:              s.URL + "/register",
		ScopesSupported:                   []string{"read", "offline_access"},
		ResponseTypesSupported:            []string{"code"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported:     []string{"S256"},
		TokenEndpointAuthMethodsSupported: []string{"client_secret_basic"},
	})
}

func (s *AuthServer) register(w http.ResponseWriter, r *http.Request) {
	var m oauthex.ClientRegistrationMetadata
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_client_metadata"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuseReg {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	s.stats.Registrations++
	id, secret := rand.Text(), rand.Text()
	s.clients[id] = secret
	m.TokenEndpointAuthMethod = "client_secret_basic"
	writeJSON(w, http.StatusCreated, &oauthex.ClientRegistrationResponse{ClientID: id, ClientSecret: secret, ClientRegistrationMetadata: m})
}

func (s *AuthServer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	code := rand.Text()
	s.codes[code] = q.Get("code_challenge")
	s.mu.Unlock()
	u, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	u.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *AuthServer) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	id, secret, _ := r.BasicAuth()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients[id] != secret || secret == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		challenge, ok := s.codes[r.PostForm.Get("code")]
		delete(s.codes, r.PostForm.Get("code"))
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		}
	case "refresh_token":
		if !s.refresh[r.PostForm.Get("refresh_token")] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		}
		delete(s.refresh, r.PostForm.Get("refresh_token"))
		s.stats.Refreshes++
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	access, refresh := rand.Text(), rand.Text()
	s.access[access], s.refresh[refresh] = true, true
	resp := map[string]any{"access_token": access, "token_type": "Bearer", "refresh_token": refresh}
	if s.expiresIn > 0 {
		resp["expires_in"] = s.expiresIn
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
