package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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
		{"cross-origin embed origin", "POST", "/api/embed-origins", http.Header{
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

// Pages of the embed origins, which the user lists in Shelley, may frame
// it, too, as embed.js does. X-Frame-Options stays: browsers ignore it
// where the CSP has frame-ancestors (embed.spec.ts frames Shelley so), and
// older browsers keep refusing every other site.
func TestEmbedOrigins(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	srv := NewServer(database, &twoModelLLMManager{service: predictable.NewService()}, claudetool.ToolSetConfig{},
		slog.New(slog.DiscardHandler), false, "model-a", "")
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(srv.tcpHandler(mux))
	t.Cleanup(ts.Close)

	do := func(method, path, body string, want int) string {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s: status = %d, want %d: %s", method, path, resp.StatusCode, want, b)
		}
		return strings.TrimSpace(string(b))
	}
	// framing checks the policy on a page loaded as dest, as a browser's
	// Sec-Fetch-Dest says, or not at all.
	framing := func(dest, want string) {
		t.Helper()
		req, err := http.NewRequest("GET", ts.URL+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		if dest != "" {
			req.Header.Set("Sec-Fetch-Dest", dest)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Content-Security-Policy"); got != want {
			t.Errorf("%s: Content-Security-Policy = %q, want %q", dest, got, want)
		}
		// Caches must not hand one destination's policy to another.
		if vary := strings.Join(resp.Header.Values("Vary"), ","); !strings.Contains(vary, "Sec-Fetch-Dest") {
			t.Errorf("%s: Vary = %q, want Sec-Fetch-Dest in it", dest, vary)
		}
		if got := resp.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
			t.Errorf("X-Frame-Options = %q, want SAMEORIGIN", got)
		}
	}

	if got := do("GET", "/api/embed-origins", "", http.StatusOK); got != `{"origins":[]}` {
		t.Errorf("no origins: %s", got)
	}
	do("POST", "/api/embed-origins", `{"origin":"https://App.Example.com/"}`, http.StatusOK)
	do("POST", "/api/embed-origins", `{"origin":"https://app.example.com:443"}`, http.StatusOK)
	got := do("POST", "/api/embed-origins", `{"origin":"http://localhost:3000"}`, http.StatusOK)
	if want := `{"origins":["http://localhost:3000","https://app.example.com"]}`; got != want {
		t.Errorf("after adding: %s, want %s", got, want)
	}
	if got := do("POST", "/api/embed-origins", `{"origin":"https://a.com; script-src *"}`, http.StatusBadRequest); !strings.Contains(got, "https://app.example.com") {
		t.Errorf("a bad origin's error should show a good one: %s", got)
	}
	listed := "frame-ancestors 'self' http://localhost:3000 https://app.example.com"
	for _, dest := range []string{"", "document", "iframe", "frame", "object", "embed"} {
		framing(dest, listed)
	}
	// No frame shows a script, style or API response.
	for _, dest := range []string{"script", "style", "empty"} {
		framing(dest, "frame-ancestors 'self'")
	}

	got = do("DELETE", "/api/embed-origins?origin=http://localhost:3000", "", http.StatusOK)
	if want := `{"origins":["https://app.example.com"]}`; got != want {
		t.Errorf("after removing: %s, want %s", got, want)
	}
	do("DELETE", "/api/embed-origins?origin=app.example.com", "", http.StatusBadRequest)
	do("DELETE", "/api/embed-origins?origin="+url.QueryEscape("HTTPS://App.example.com:443/"), "", http.StatusOK)
	framing("iframe", "frame-ancestors 'self'")
}

// Any page may load embed.js, even without the header -require-header
// names; whether it may frame Shelley is up to the framing policy.
func TestEmbedJS(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	srv := NewServer(database, &twoModelLLMManager{service: predictable.NewService()}, claudetool.ToolSetConfig{},
		slog.New(slog.DiscardHandler), false, "model-a", "X-Exedev-Userid")
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(srv.tcpHandler(mux))
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/embed.js")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if !strings.Contains(string(body), "Edit with Shelley") {
		t.Errorf("body is not embed.js:\n%s", body)
	}
	etag := resp.Header.Get("ETag")
	req, _ := http.NewRequest("GET", ts.URL+"/embed.js", nil)
	req.Header.Set("If-None-Match", etag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if etag == "" || resp.StatusCode != http.StatusNotModified {
		t.Errorf("revalidating ETag %q: status = %d, want 304", etag, resp.StatusCode)
	}
}

func TestParseEmbedOrigin(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"https://app.example.com":  "https://app.example.com",
		"https://App.Example.com/": "https://app.example.com",
		"http://localhost:3000":    "http://localhost:3000",
		"http://127.0.0.1:3000":    "http://127.0.0.1:3000",
		"http://10.0.0.255":        "http://10.0.0.255",
		"http://1a.example":        "http://1a.example",
		// Browsers spell these IPv4 addresses otherwise, so they'd never match.
		"http://127.1:3000":         "",
		"http://2130706433":         "",
		"http://0x7f.0.0.1":         "",
		"http://127.0.0.01":         "",
		"http://a.0x1":              "",
		"http://a.123":              "",
		"https://a.com:0443":        "https://a.com",
		" http://a.com:80 ":         "http://a.com",
		"http://a.com:443":          "http://a.com:443",
		"https://a.com:80":          "https://a.com:80",
		"https://a.com:8443":        "https://a.com:8443",
		"http://[::1]:3000":         "",
		"https://app_x.example.com": "",
		"https://app..example.com":  "",
		"https://app.example.com:":  "",
		"https://:80":               "",
		"https://a.com:99999":       "",
		"https://a.com:0":           "",
		"https://a.com\"":           "",
		"https://bücher.example":    "",
		"https://\u0130.example":    "",
		"app.example.com":           "",
		"https://":                  "",
		"*":                         "",
		"https://*.example.com":     "",
		"ftp://app.example.com":     "",
		"https://app.example.com/x": "",
		"https://app.example.com?":  "",
		"https://app.example.com#x": "",
		"https://u@app.example.com": "",
		"https://app.example.com;":  "",
		"https://a.com https://b":   "",
		"https://app.example.com:x": "",
	} {
		got, err := parseEmbedOrigin(in)
		if got != want || (err == nil) != (want != "") {
			t.Errorf("parseEmbedOrigin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
