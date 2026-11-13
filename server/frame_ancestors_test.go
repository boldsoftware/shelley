package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/llm/predictable"
)

// Shelley must not be framed by other sites: a framed, logged-in Shelley is a
// clickjacking target, and a click there can run commands on the VM. Shelley
// frames its own pages (PDF previews via /api/read), so same-origin stays.
func TestTCPHandlerForbidsCrossOriginFraming(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	srv := NewServer(database, &twoModelLLMManager{service: predictable.NewService()}, claudetool.ToolSetConfig{},
		slog.New(slog.DiscardHandler), false, "model-a", "X-Exedev-Userid")
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(srv.tcpHandler(mux))
	t.Cleanup(ts.Close)

	for _, tc := range []struct {
		name, method, path string
		header             http.Header
		want               int
	}{
		{"ui", "GET", "/", nil, 0},
		{"api", "GET", "/api/conversations", http.Header{"X-Exedev-Userid": {"u"}}, http.StatusOK},
		{"missing proxy header", "GET", "/api/conversations", nil, http.StatusForbidden},
		{"cross-origin post", "POST", "/api/conversations/new", http.Header{
			"X-Exedev-Userid": {"u"},
			"Sec-Fetch-Site":  {"cross-site"},
		}, http.StatusForbidden},
		{"not found", "GET", "/no-such-page", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, ts.URL+tc.path, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.header {
				req.Header[k] = v
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if tc.want != 0 && resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if got := resp.Header.Get("Content-Security-Policy"); got != "frame-ancestors 'self'" {
				t.Errorf("Content-Security-Policy = %q, want frame-ancestors 'self'", got)
			}
			if got := resp.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
				t.Errorf("X-Frame-Options = %q, want SAMEORIGIN", got)
			}
		})
	}
}
