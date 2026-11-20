package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"shelley.exe.dev/db/generated"
)

// embedHost is a host a CSP source expression can name: no IPv6
// literals, no wildcards.
var embedHost = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*(:[0-9]+)?$`)

// numericLabel is a host's last label that makes browsers read the host as
// an IPv4 address.
var numericLabel = regexp.MustCompile(`(^|\.)([0-9]+|0[xX][0-9A-Fa-f]*)$`)

// canonicalIPv4 reports whether host, if browsers read it as an IPv4
// address, is that address as they spell it: 127.0.0.1, not 127.1.
func canonicalIPv4(host string) bool {
	if !numericLabel.MatchString(host) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() != nil && ip.String() == host
}

func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return p == "" || err == nil && n > 0 && n <= 65535
}

// parseEmbedOrigin checks that s is an origin, scheme://host[:port], that
// may frame Shelley, and returns it as a CSP source expression, spelled
// as browsers spell origins. Browsers let an http origin's https twin
// frame Shelley, too.
func parseEmbedOrigin(s string) (string, error) {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.User != nil ||
		u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		!embedHost.MatchString(u.Host) || !validPort(u.Port()) || !canonicalIPv4(u.Hostname()) {
		return "", fmt.Errorf("%q is not an origin, scheme://host[:port], as https://app.example.com", s)
	}
	origin := strings.ToLower(u.Scheme + "://" + u.Hostname())
	if p := u.Port(); p != "" {
		n, _ := strconv.Atoi(p)
		if n != map[string]int{"http": 80, "https": 443}[u.Scheme] {
			origin += ":" + strconv.Itoa(n)
		}
	}
	return origin, nil
}

func (s *Server) listEmbedOrigins(ctx context.Context) ([]string, error) {
	return s.db.QueriesRes(ctx, func(q *generated.Queries) ([]string, error) {
		return q.ListEmbedOrigins(ctx)
	})
}

// writeEmbedOrigins answers an /api/embed-origins request with the list.
func (s *Server) writeEmbedOrigins(w http.ResponseWriter, r *http.Request) {
	origins, err := s.listEmbedOrigins(r.Context())
	if err != nil {
		s.internalError(w, "Failed to list embed origins", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"origins": origins})
}

// handleAddEmbedOrigin handles POST /api/embed-origins.
func (s *Server) handleAddEmbedOrigin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Origin string `json:"origin"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	origin, err := parseEmbedOrigin(req.Origin)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.db.QueriesTx(r.Context(), func(q *generated.Queries) error {
		return q.AddEmbedOrigin(r.Context(), origin)
	}); err != nil {
		s.internalError(w, "Failed to add embed origin", err)
		return
	}
	s.writeEmbedOrigins(w, r)
}

// handleDeleteEmbedOrigin handles DELETE /api/embed-origins?origin=.
func (s *Server) handleDeleteEmbedOrigin(w http.ResponseWriter, r *http.Request) {
	origin, err := parseEmbedOrigin(r.URL.Query().Get("origin"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.db.QueriesTx(r.Context(), func(q *generated.Queries) error {
		return q.DeleteEmbedOrigin(r.Context(), origin)
	}); err != nil {
		s.internalError(w, "Failed to delete embed origin", err)
		return
	}
	s.writeEmbedOrigins(w, r)
}

// framingPolicy lets only Shelley's own pages and the embed origins' pages
// frame Shelley: a framed Shelley is a clickjacking target, and a click
// there can run commands on the machine. Shelley frames itself (PDF
// previews). It reads the list for every page a frame may load, so a
// change applies at once to Shelley's own pages, which no one caches, in
// every Shelley sharing the database; scripts, styles and API calls,
// which no frame shows, get 'self' alone, and Vary keeps caches from
// mixing the two up.
//
// X-Frame-Options backs the CSP up in browsers without frame-ancestors;
// browsers with it ignore X-Frame-Options, so the embedding pages it can't
// name still frame Shelley. Handlers that replace the CSP (sandboxed files)
// keep X-Frame-Options alone, which browsers check against every ancestor:
// only a Shelley that isn't itself framed may frame those.
func (s *Server) framingPolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var origins []string
		if mayFrame(r) {
			var err error
			if origins, err = s.listEmbedOrigins(r.Context()); err != nil {
				s.internalError(w, "Failed to list embed origins", err)
				return
			}
		}
		w.Header().Set("Content-Security-Policy", strings.Join(append([]string{"frame-ancestors 'self'"}, origins...), " "))
		w.Header().Add("Vary", "Sec-Fetch-Dest")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		next.ServeHTTP(w, r)
	})
}

// mayFrame reports whether r may load a page into a frame. Browsers leave
// Sec-Fetch-Dest out on plain http sites other than localhost.
func mayFrame(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "", "document", "iframe", "frame", "object", "embed":
		return true
	}
	return false
}
