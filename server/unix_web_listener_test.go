package server

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A launcher can hand Shelley a Unix socket in place of the TCP listener (via
// systemd-style activation) so the web UI is reachable only through that
// socket. That listener must still get the browser-facing middleware.
func TestStartWithUnixWebListener(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)

	// t.TempDir() can exceed the 104-byte sun_path limit on macOS.
	dir, err := os.MkdirTemp("", "sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "web.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

	started := make(chan error, 1)
	go func() { started <- s.StartWithListeners(listener, "") }()

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
	type result struct {
		resp *http.Response
		err  error
	}
	do := func(req *http.Request) *http.Response {
		t.Helper()
		done := make(chan result, 1)
		go func() {
			resp, err := client.Do(req)
			done <- result{resp, err}
		}()
		select {
		case err := <-started:
			t.Fatalf("server exited: %v", err)
		case r := <-done:
			if r.err != nil {
				t.Fatal(r.err)
			}
			r.resp.Body.Close()
			return r.resp
		}
		return nil
	}

	get, _ := http.NewRequest("GET", "http://shelley/version", nil)
	if resp := do(get); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /version = %d, want 200", resp.StatusCode)
	}

	post, _ := http.NewRequest("POST", "http://shelley/api/conversations/new", strings.NewReader("{}"))
	post.Header.Set("Sec-Fetch-Site", "cross-site")
	if resp := do(post); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site POST = %d, want 403 (CSRF protection)", resp.StatusCode)
	}
	if s.listenPort != 0 {
		t.Fatalf("listenPort = %d, want 0 for a Unix listener", s.listenPort)
	}
}
