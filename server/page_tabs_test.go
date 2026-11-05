package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"shelley.exe.dev/db"
)

// TestPageFrame swaps the reflection client, so it isn't parallel.
func TestPageFrame(t *testing.T) {
	s, _, _ := newTestServer(t)
	ask := func(page, host string, headers map[string]string) *httptest.ResponseRecorder {
		api := http.NewServeMux()
		s.registerWorkspaceRoutes(api)
		// An origin parameter, naming the page's own host, is ignored.
		query := url.Values{"url": {page}, "origin": {page}}
		request := httptest.NewRequest("GET", "/api/page-frame?"+query.Encode(), nil)
		request.Host = host
		for k, v := range headers {
			request.Header.Set(k, v)
		}
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, request)
		return recorder
	}
	shelley := map[string]string{"X-Shelley-Request": "1"}
	if code := ask("https://x.exe.xyz/", "shelley.example", shelley).Code; code != http.StatusNotFound {
		t.Fatalf("outside workspace mode: %d", code)
	}
	db.EnableWorkspaceMode(t, s.db)
	if err := s.loadWorkspace(t.Context()); err != nil {
		t.Fatal(err)
	}
	if code := ask("https://x.exe.xyz/", "shelley.example", nil).Code; code != http.StatusForbidden {
		t.Errorf("without X-Shelley-Request: %d", code)
	}

	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherHits.Add(1) }))
	defer other.Close()
	var appHits atomic.Int32
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appHits.Add(1)
		switch r.URL.Path {
		case "/private":
			http.Redirect(w, r, "https://exe.dev/auth?redirect=x", http.StatusTemporaryRedirect)
		case "/denied":
			w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
		case "/elsewhere":
			http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
		case "/here":
			http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		}
	}))
	defer app.Close()
	appURL, _ := url.Parse(app.URL)
	old := reflectionHTTPClient()
	t.Cleanup(func() { setReflectionHTTPClient(old) })
	setReflectionHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/default_port" {
			t.Errorf("unexpected reflection URL %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"default_port": ` + appURL.Port() + `}`)),
		}, nil
	})})

	// On exe.dev, a page on Shelley's own host is asked of localhost.
	vm := "https://x.exe.xyz:" + appURL.Port()
	https := map[string]string{"X-Shelley-Request": "1", "X-Forwarded-Proto": "https"}
	for _, tc := range []struct {
		url, host string
		headers   map[string]string
		blocked   string
		login     bool
	}{
		{app.URL + "/", "shelley.example", shelley, "", false},
		{app.URL + "/here", "shelley.example", shelley, "", false},
		{app.URL + "/denied", "shelley.example", shelley, frameBlockedCSP, false},
		{app.URL + "/private", "shelley.example", shelley, frameBlockedLogin, false},
		{app.URL + "/", appURL.Host, shelley, frameBlockedShelley, false},
		{vm + "/private", "x.exe.xyz", https, "", true},
		{vm + "/", "x.exe.xyz", https, "", true},
		{vm + "/denied", "shelley.example", map[string]string{"X-Shelley-Request": "1", "X-Forwarded-Proto": "https", "X-Forwarded-Host": "x.exe.xyz:1"}, frameBlockedCSP, true},
		// The proxy's default port.
		{"https://x.exe.xyz/", "x.exe.xyz:9999", https, "", true},
		{"https://shelley.exe.xyz/", "shelley.exe.xyz", https, frameBlockedShelley, false},
	} {
		response := ask(tc.url, tc.host, tc.headers)
		var frame pageFrame
		if response.Code == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &frame); err != nil {
				t.Fatal(err)
			}
		}
		if response.Code != http.StatusOK || frame.Blocked != tc.blocked || frame.Login != tc.login {
			t.Errorf("%s on %s: %d %s %+v, want blocked=%q login=%v", tc.url, tc.host, response.Code, response.Body, frame, tc.blocked, tc.login)
		}
	}

	// A redirect to a private address elsewhere isn't followed, and the
	// error doesn't say what was reached.
	if response := ask(app.URL+"/elsewhere", "shelley.example", shelley); response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "127.0.0.1") {
		t.Errorf("redirect to a private address: %d %s", response.Code, response.Body)
	}
	if otherHits.Load() != 0 {
		t.Errorf("followed a redirect to a private address")
	}
	// Only Shelley's own host is asked of localhost: a page on another
	// host, whatever origin the request names, is fetched where it is.
	before := appHits.Load()
	if code := ask("http://spoof.invalid:"+appURL.Port()+"/", "shelley.example", shelley).Code; code != http.StatusBadGateway {
		t.Errorf("spoofed origin: %d", code)
	}
	if appHits.Load() != before {
		t.Errorf("a spoofed origin reached localhost")
	}
	if code := ask("file:///etc/passwd", "shelley.example", shelley).Code; code != http.StatusBadRequest {
		t.Errorf("file url: %d", code)
	}
}

func TestFrameBlocked(t *testing.T) {
	t.Parallel()
	origin, _ := url.Parse("https://shelley.exe.xyz")
	for _, tc := range []struct {
		headers map[string]string
		blocked bool
	}{
		{nil, false},
		{map[string]string{"X-Frame-Options": "DENY"}, true},
		{map[string]string{"X-Frame-Options": "sameorigin"}, true},
		{map[string]string{"Content-Security-Policy": "default-src 'self'"}, false},
		{map[string]string{"Content-Security-Policy": "frame-ancestors 'none'"}, true},
		{map[string]string{"Content-Security-Policy": "frame-ancestors 'self'"}, true},
		{map[string]string{"Content-Security-Policy": "frame-ancestors *", "X-Frame-Options": "DENY"}, false},
		{map[string]string{"Content-Security-Policy": "frame-ancestors https:"}, false},
		{map[string]string{"Content-Security-Policy": "frame-ancestors https://*.exe.xyz"}, false},
		{map[string]string{"Content-Security-Policy": "frame-ancestors https://*"}, false},
		{map[string]string{"Content-Security-Policy": "frame-ancestors http://*"}, true},
		{map[string]string{"Content-Security-Policy": "frame-ancestors shelley.exe.xyz:443"}, false},
		{map[string]string{"Content-Security-Policy": "frame-ancestors http://shelley.exe.xyz"}, true},
		{map[string]string{"Content-Security-Policy": "frame-ancestors https://other.exe.xyz"}, true},
		{map[string]string{"Content-Security-Policy": "frame-ancestors *, frame-ancestors 'none'"}, true},
	} {
		h := http.Header{}
		for k, v := range tc.headers {
			h.Set(k, v)
		}
		if reason := frameBlocked(h, origin); (reason != "") != tc.blocked {
			t.Errorf("%v: blocked %q, want blocked=%v", tc.headers, reason, tc.blocked)
		}
	}
}
