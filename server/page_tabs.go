package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"shelley.exe.dev/exeenv"
)

// Page tabs (workspace mode): a task's header shows web pages relevant to it
// as tabs, framed in place of the transcript, or opened in a new browser tab
// when the browser can't show them framed. GET /api/page-frame says which.

// exe.dev's proxy serves a VM's ports on <vm>.exe.xyz (exe.xyz is a public
// suffix, so every VM is its own site) and sends a visitor without its
// cookie to the exe.dev login, on exe.dev or under /__exe.dev/ on the VM's
// host.
const (
	exeProxyHostSuffix = ".exe.xyz"
	exeLoginHost       = "exe.dev"
	exeLoginPathPrefix = "/__exe.dev/"
)

// Why a page can't be framed (pageFrame.Blocked); the UI words them.
const (
	// The page is Shelley itself.
	frameBlockedShelley = "shelley"
	// Its Content-Security-Policy frame-ancestors excludes Shelley.
	frameBlockedCSP = "csp"
	// It sends X-Frame-Options DENY or SAMEORIGIN.
	frameBlockedXFO = "x-frame-options"
	// It is behind the exe.dev login on another site, whose SameSite=Lax
	// cookie browsers never send it framed.
	frameBlockedLogin = "login"
)

// pageFrame is whether the browser can frame a page on Shelley's origin.
type pageFrame struct {
	// Blocked is why it can't (a frameBlocked* code), or "".
	Blocked string `json:"blocked"`
	// Login: the page is behind the exe.dev login, whose cookie browsers
	// send it framed only once the user has logged in to it in a tab of its
	// own: it is on Shelley's host, so it is same-site.
	Login bool `json:"login,omitempty"`
}

var (
	errExeLogin   = errors.New("redirected to the exe.dev login")
	errPrivateHop = errors.New("refused to follow a redirect to a private address")
)

// probeFrame fetches page as an anonymous visitor would and reports whether
// a browser can frame it on origin. It honors what the page and exe.dev
// send; it never works around them.
func probeFrame(ctx context.Context, page, origin *url.URL) (pageFrame, error) {
	if strings.EqualFold(page.Scheme, origin.Scheme) && strings.EqualFold(page.Host, origin.Host) {
		return pageFrame{Blocked: frameBlockedShelley}, nil
	}
	// A page on Shelley's own exe.dev host and another port: the proxy
	// can't be reached from its own VM, so ask the server behind it.
	thisHost := strings.EqualFold(page.Hostname(), origin.Hostname())
	proxied := strings.HasSuffix(strings.ToLower(page.Hostname()), exeProxyHostSuffix)
	target := *page
	if thisHost && proxied {
		port := page.Port()
		if port == "" {
			// The proxy's default port goes to the VM's default port.
			var err error
			if port, err = exeDefaultPort(ctx); err != nil {
				return pageFrame{}, err
			}
		}
		target.Scheme, target.Host = "http", "localhost:"+port
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return pageFrame{}, err
	}
	transport := probeTransport(target.Hostname(), portOf(&target))
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Hostname() == exeLoginHost || strings.HasPrefix(req.URL.Path, exeLoginPathPrefix) {
			return errExeLogin
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("refused to follow a redirect to %s", req.URL.Scheme)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if errors.Is(err, errExeLogin) {
		if thisHost {
			return pageFrame{Login: true}, nil
		}
		return pageFrame{Blocked: frameBlockedLogin}, nil
	}
	if err != nil {
		return pageFrame{}, fmt.Errorf("probe %s: %w", page, err)
	}
	resp.Body.Close()
	return pageFrame{Blocked: frameBlocked(resp.Header, origin), Login: thisHost && proxied}, nil
}

// probeTransport connects to host:port, the page that was asked about,
// wherever it is, but a redirect elsewhere only to a public address.
func probeTransport(host, port string) *http.Transport {
	direct := &net.Dialer{}
	public := &net.Dialer{Control: func(network, address string, _ syscall.RawConn) error {
		ip, err := netip.ParseAddrPort(address)
		if err != nil {
			return err
		}
		if addr := ip.Addr().Unmap(); addr.IsPrivate() || !addr.IsGlobalUnicast() {
			return errPrivateHop
		}
		return nil
	}}
	asked := net.JoinHostPort(host, port)
	return &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == asked {
			return direct.DialContext(ctx, network, address)
		}
		return public.DialContext(ctx, network, address)
	}}
}

// portOf returns u's port, or its scheme's.
func portOf(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	return map[string]string{"https": "443", "http": "80"}[u.Scheme]
}

// exeDefaultPort asks exe.dev which of the VM's ports its proxy serves on
// the default port.
func exeDefaultPort(ctx context.Context) (string, error) {
	env, err := exeenv.Current()
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.ReflectionURL()+"/default_port", nil)
	if err != nil {
		return "", err
	}
	resp, err := reflectionHTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("default port: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		DefaultPort int `json:"default_port"`
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("default port: %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("default port: %w", err)
	}
	if body.DefaultPort == 0 {
		return "", errors.New("default port: none")
	}
	return strconv.Itoa(body.DefaultPort), nil
}

// requestOrigin is the origin the browser reached Shelley at, behind
// whatever proxy.
func requestOrigin(r *http.Request) *url.URL {
	origin := &url.URL{Scheme: "http", Host: r.Host}
	if r.TLS != nil {
		origin.Scheme = "https"
	}
	if p, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ","); p != "" {
		origin.Scheme = strings.TrimSpace(p)
	}
	if h, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ","); h != "" {
		origin.Host = strings.TrimSpace(h)
	}
	return origin
}

// handlePageFrame reports whether the browser can frame ?url= on the
// origin it reached Shelley at, as a pageFrame. Only Shelley's UI may ask:
// it fetches a URL server-side.
func (s *Server) handlePageFrame(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Shelley-Request") != "1" {
		http.Error(w, "Missing request header", http.StatusForbidden)
		return
	}
	if !s.workspaceMode() {
		http.NotFound(w, r)
		return
	}
	page, err := url.Parse(r.URL.Query().Get("url"))
	if err != nil || (page.Scheme != "https" && page.Scheme != "http") || page.Host == "" {
		http.Error(w, "an http(s) url is required", http.StatusBadRequest)
		return
	}
	frame, err := probeFrame(r.Context(), page, requestOrigin(r))
	if err != nil {
		s.logger.Info("Page frame probe failed", "url", page.String(), "error", err)
		http.Error(w, "Shelley couldn't load the page", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, frame)
}

// pageTabsPrompt tells a task's agent how to declare a page tab.
const pageTabsPrompt = `When a web page is what this task works on (a draft, a dev server through the exe.dev proxy, a PR), link it in a message with the title "tab", e.g. [Draft deploy page](https://example.com/draft "tab"), and the user gets it as a tab above the conversation. A tab follows your later links to the same site (scheme, host and port), so link the page again when it moves. Only for the few pages that matter; use the URL the user's browser can open.
`

// frameBlocked returns why response headers h forbid framing on origin (a
// frameBlocked* code), or "". CSP frame-ancestors, where present, overrides X-Frame-Options.
func frameBlocked(h http.Header, origin *url.URL) string {
	found := false
	for _, header := range h.Values("Content-Security-Policy") {
		for _, policy := range strings.Split(header, ",") {
			for _, directive := range strings.Split(policy, ";") {
				fields := strings.Fields(directive)
				if len(fields) == 0 || !strings.EqualFold(fields[0], "frame-ancestors") {
					continue
				}
				found = true
				if !slices.ContainsFunc(fields[1:], func(source string) bool { return frameSourceMatches(source, origin) }) {
					return frameBlockedCSP
				}
			}
		}
	}
	if found {
		return ""
	}
	switch strings.ToUpper(strings.TrimSpace(h.Get("X-Frame-Options"))) {
	case "DENY", "SAMEORIGIN":
		return frameBlockedXFO
	}
	return ""
}

// frameSourceMatches reports whether CSP source expression source allows
// origin. 'self' never does: a VM is never Shelley's origin.
func frameSourceMatches(source string, origin *url.URL) bool {
	if source == "*" {
		return true
	}
	if strings.HasSuffix(source, ":") && !strings.Contains(source, "/") {
		return strings.EqualFold(strings.TrimSuffix(source, ":"), origin.Scheme)
	}
	if strings.HasPrefix(source, "'") {
		return false
	}
	scheme, rest, ok := strings.Cut(source, "://")
	if !ok {
		scheme, rest = "", source
	} else if !strings.EqualFold(scheme, origin.Scheme) {
		return false
	}
	rest, _, _ = strings.Cut(rest, "/")
	host, port, hasPort := strings.Cut(rest, ":")
	if hasPort && port != "*" && port != portOf(origin) {
		return false
	}
	if !hasPort && origin.Port() != "" {
		return false
	}
	if host == "*" {
		return true
	}
	if suffix, ok := strings.CutPrefix(host, "*."); ok {
		return strings.HasSuffix(strings.ToLower(origin.Hostname()), "."+strings.ToLower(suffix))
	}
	return strings.EqualFold(host, origin.Hostname())
}
