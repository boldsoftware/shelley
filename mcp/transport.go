package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"shelley.exe.dev/version"
)

func httpTransport(srv Server, h auth.OAuthHandler) sdk.Transport {
	return &sdk.StreamableClientTransport{
		Endpoint:   srv.URL,
		HTTPClient: httpClient(srv),
		// We don't handle server-initiated messages; don't hold a
		// connection open for them.
		DisableStandaloneSSE: true,
		OAuthHandler:         h,
	}
}

// httpClient returns a client for srv that adds its static headers.
func httpClient(srv Server) *http.Client {
	return &http.Client{Transport: headerTransport(srv.Headers), CheckRedirect: sameOriginRedirects}
}

// headerTransport identifies as Shelley and adds static headers, which
// may replace the User-Agent.
type headerTransport map[string]string

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", version.UserAgent())
	for k, v := range h {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err == nil && resp.StatusCode == http.StatusNotFound && r.Header.Get("Mcp-Session-Id") != "" {
		// The server forgot the session. Python SDK servers say so with a
		// JSON-RPC error, which go-sdk then reports instead of
		// ErrSessionMissing.
		resp.Body.Close()
		resp.Body = http.NoBody
	}
	return resp, err
}

// sameOriginRedirects refuses redirects to another origin: Go would send
// it the static headers too.
func sameOriginRedirects(req *http.Request, via []*http.Request) error {
	if from := via[0].URL; req.URL.Scheme != from.Scheme || req.URL.Host != from.Host {
		return fmt.Errorf("refusing redirect from %s://%s to another origin, %s://%s", from.Scheme, from.Host, req.URL.Scheme, req.URL.Host)
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// JSON-RPC requests for probe.
const (
	initializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"shelley","version":"1"}}}`
	pingRequest       = `{"jsonrpc":"2.0","id":1,"method":"ping"}`
)

// probe sends srv a JSON-RPC request outside of a session, with token
// unless it's empty, and returns the response, with its body closed.
func probe(ctx context.Context, srv Server, token, body string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient(srv).Do(req)
	if err != nil {
		return nil, fmt.Errorf("connecting to MCP server %q: %w", srv.Name, err)
	}
	resp.Body.Close()
	return resp, nil
}
