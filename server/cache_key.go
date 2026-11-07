package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"

	"golang.org/x/crypto/hkdf"

	"shelley.exe.dev/db"
)

var errNoCacheSession = db.ErrNoCacheSession

// IndexedDB cache encryption — see the commit message that introduced
// this file for the full threat model.
//
// The browser holds an opaque cookie (`shelley_cache_session`). The server
// holds a long-lived master secret in the settings table. The IDB key the
// browser uses is HKDF(master_secret, salt=cookie, info="shelley-idb-v1").
// Releasing the key requires presenting BOTH the proxy-auth header AND a
// valid cookie/user-id match — losing either invalidates the cache.
//
// Caveats this scheme does NOT defend against:
//   - Anyone who can read both shelley.db AND the browser's IndexedDB
//     profile (e.g. self-hosted local-only shelley where both live on
//     the same disk) gets the master secret AND the cookie, and can
//     therefore decrypt. The protection is meaningful only when the
//     master secret lives somewhere the local-disk attacker can't reach.
//   - XSS or any same-origin attacker who can call /api/cache-key.
//   - A memory dump of the live browser tab.
//
// Server-side row deletion: handleCacheSessionClear deletes the row AND
// expires the cookie. If something else deletes the row but leaves the
// cookie intact, the next /api/cache-key re-records the same token under
// the same hash, derives the same key, and "resumes" the cache. The only
// way to force a key rotation is to also invalidate the cookie at the
// browser. Do not garbage-collect cache-session rows by age: deleting a row
// without invalidating the browser cookie resumes the same key rather than
// rotating it.

const (
	cacheCookieName = "shelley_cache_session"
	// 180 days. Matches our typical proxy session lifetime.
	cacheCookieMaxAge    = 180 * 24 * 60 * 60
	cacheMasterSecretKey = "cache_session_master_secret"
	cacheKDFInfo         = "shelley-idb-v1"
	cacheKeyLength       = 32 // AES-256-GCM
	cacheKeyAlg          = "AES-GCM-256"
)

// userIDFromRequest returns the user identifier the auth proxy attached to
// this request, or "" when no requireHeader is configured. The empty string
// is a stable sentinel for the "no proxy in front" deployment mode (e.g.
// local dev with no --require-header flag).
func (s *Server) userIDFromRequest(r *http.Request) string {
	if s.requireHeader == "" {
		return ""
	}
	return r.Header.Get(s.requireHeader)
}

// cacheMasterSecret returns the server's HKDF input keying material,
// generating and persisting it on first use. 32 random bytes. The cached
// copy lives on the Server so parallel tests with independent DBs cannot
// see each other's secrets.
func (s *Server) cacheMasterSecret(ctx context.Context) ([]byte, error) {
	s.cacheMasterSecretMu.Lock()
	defer s.cacheMasterSecretMu.Unlock()
	if s.cacheMasterSecretCache != nil {
		return s.cacheMasterSecretCache, nil
	}
	val, err := s.db.GetSetting(ctx, cacheMasterSecretKey)
	if err != nil {
		return nil, fmt.Errorf("get master secret: %w", err)
	}
	if val != "" {
		decoded, err := base64.StdEncoding.DecodeString(val)
		if err == nil && len(decoded) == 32 {
			s.cacheMasterSecretCache = decoded
			return s.cacheMasterSecretCache, nil
		}
		// Corrupt / wrong length — regenerate. The only consequence is
		// that any previously-issued IDB caches won't decrypt, which is
		// the safer failure mode.
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate master secret: %w", err)
	}
	if err := s.db.SetSetting(ctx, cacheMasterSecretKey, base64.StdEncoding.EncodeToString(secret)); err != nil {
		return nil, fmt.Errorf("persist master secret: %w", err)
	}
	s.cacheMasterSecretCache = secret
	return s.cacheMasterSecretCache, nil
}

// hashCacheToken returns hex(SHA-256(token)). Stored in the cache_sessions
// table; never the raw token.
//
// We hash with no salt: tokens are 32 random bytes, so a rainbow table is
// infeasible and the hash is only used for equality lookups.
func hashCacheToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// deriveKey runs HKDF-SHA256 to mix the master secret with the cookie token.
func deriveCacheKey(masterSecret []byte, token string) ([]byte, error) {
	r := hkdf.New(sha256.New, masterSecret, []byte(token), []byte(cacheKDFInfo))
	key := make([]byte, cacheKeyLength)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}
	return key, nil
}

// keyIDForKey is a stable, public identifier the client uses to detect
// rotations without comparing key bytes. 16 hex chars (8 bytes).
//
// We hash the *derived* AES key (which is HKDF(master_secret, token)) rather
// than the token alone. If the server's master secret is ever regenerated
// (corruption fallback in cacheMasterSecret) the derived key changes while
// the cookie token stays put — deriving the id from the key guarantees a
// different id, which the UI translates into a wipe. Hashing the derived
// key is safe: the AES key is high-entropy random bytes (32 bytes from
// HKDF), so SHA-256(key)[:8] reveals nothing useful.
func keyIDForKey(derivedKey []byte) string {
	sum := sha256.Sum256(derivedKey)
	return hex.EncodeToString(sum[:8])
}

// newCacheToken returns 32 random bytes, base64-URL-no-padding encoded.
func newCacheToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// requestIsSecure reports whether the request reached us over HTTPS, either
// directly or via a reverse proxy that set X-Forwarded-Proto. We use this
// to decide whether to set the Secure cookie attribute — browsers reject
// Secure cookies over plain HTTP except on localhost.
func requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "https" {
		return true
	}
	return false
}

// setCacheCookie attaches a fresh cookie to the response.
func setCacheCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cacheCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   cacheCookieMaxAge,
		HttpOnly: true,
		Secure:   requestIsSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// clearCacheCookie removes the cookie.
func clearCacheCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cacheCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   requestIsSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// cacheKeyResponse is the JSON body of GET /api/cache-key.
type cacheKeyResponse struct {
	KeyID string `json:"key_id"`
	Key   string `json:"key"` // base64-std
	Alg   string `json:"alg"`
}

// handleCacheKey returns a per-browser AES-GCM key derived from the
// server's master secret + the browser's session cookie. Issues the cookie
// on first call; rotates it when the cookie's recorded user disagrees with
// the request's authenticated user.
func (s *Server) handleCacheKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := s.userIDFromRequest(r)

	// Read existing cookie if any.
	var token string
	if c, err := r.Cookie(cacheCookieName); err == nil {
		token = c.Value
	}

	// Validate the cookie against the cache_sessions table; rotate if
	// the recorded user differs.
	if token != "" {
		hash := hashCacheToken(token)
		rec, err := s.db.GetCacheSession(ctx, hash)
		switch {
		case err == nil:
			if rec.UserID != userID {
				// Different proxy user is using this browser — burn the
				// old session and mint a new cookie so they can't decrypt
				// the prior user's IDB cache.
				_ = s.db.DeleteCacheSession(ctx, hash)
				token = ""
			}
		case errors.Is(err, errNoCacheSession):
			// Cookie present but no row (server-side wipe). Re-record it.
			if err := s.db.UpsertCacheSession(ctx, hash, userID); err != nil {
				s.logger.Error("cache-key: re-upsert session", "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		default:
			s.logger.Error("cache-key: lookup session", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	// Mint a fresh cookie if needed.
	if token == "" {
		newTok, err := newCacheToken()
		if err != nil {
			s.logger.Error("cache-key: mint token", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		hash := hashCacheToken(newTok)
		if err := s.db.UpsertCacheSession(ctx, hash, userID); err != nil {
			s.logger.Error("cache-key: upsert session", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		token = newTok
		setCacheCookie(w, r, token)
	}

	master, err := s.cacheMasterSecret(ctx)
	if err != nil {
		s.logger.Error("cache-key: master secret", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	key, err := deriveCacheKey(master, token)
	if err != nil {
		s.logger.Error("cache-key: derive", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp := cacheKeyResponse{
		KeyID: keyIDForKey(key),
		Key:   base64.StdEncoding.EncodeToString(key),
		Alg:   cacheKeyAlg,
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

// handleCacheSessionClear invalidates the browser's cache session. The next
// GET /api/cache-key will mint a fresh cookie (and therefore a new key_id),
// which the client uses to recognize that its IDB cache is unreadable and
// must be wiped.
func (s *Server) handleCacheSessionClear(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if c, err := r.Cookie(cacheCookieName); err == nil && c.Value != "" {
		hash := hashCacheToken(c.Value)
		if err := s.db.DeleteCacheSession(ctx, hash); err != nil {
			s.logger.Error("cache-session/clear: delete", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	clearCacheCookie(w, r)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
