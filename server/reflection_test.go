package server

import (
	"context"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"shelley.exe.dev/exeenv"
)

func TestCachedReflectionEmoji(t *testing.T) {
	resetReflectionEmojiCache()
	t.Cleanup(resetReflectionEmojiCache)

	env, err := exeenv.New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	old := exeReflectionEmojiHTTPClient
	t.Cleanup(func() { exeReflectionEmojiHTTPClient = old })
	requests := 0
	exeReflectionEmojiHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.String() != "https://reflection.int.example.test" {
			t.Fatalf("unexpected reflection URL %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"emoji":"🐚"}`)),
			Header:     make(http.Header),
		}, nil
	})}

	if got := cachedReflectionEmojiIn(t.Context(), env); got != "🐚" {
		t.Fatalf("cachedReflectionEmojiIn() = %q, want 🐚", got)
	}
	if got := cachedReflectionEmojiIn(t.Context(), env); got != "🐚" {
		t.Fatalf("cachedReflectionEmojiIn() = %q, want 🐚", got)
	}
	if requests != 1 {
		t.Fatalf("reflection requests = %d, want 1", requests)
	}
}

func TestReflectionEmojiFallback(t *testing.T) {
	resetReflectionEmojiCache()
	t.Cleanup(resetReflectionEmojiCache)

	env, err := exeenv.New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	old := exeReflectionEmojiHTTPClient
	t.Cleanup(func() { exeReflectionEmojiHTTPClient = old })
	exeReflectionEmojiHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("unavailable")),
			Header:     make(http.Header),
		}, nil
	})}

	if got := cachedReflectionEmojiIn(t.Context(), env); got != "" {
		t.Fatalf("cachedReflectionEmojiIn() = %q, want empty", got)
	}
}

func TestHandleIntegrationsIn(t *testing.T) {
	env, err := exeenv.New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		upstream   int
		body       string
		wantStatus int
		wantBody   string
	}{
		{"attached", http.StatusOK, `{"integrations":[{"name":"llm","type":"llm","comment":"models","team":true,"help":"curl the service"}]}`, http.StatusOK, "{\"integrations\":[{\"name\":\"llm\",\"type\":\"llm\",\"comment\":\"models\",\"help\":\"curl the service\",\"team\":true,\"url\":\"https://llm.team.example.test\"}]}\n"},
		{"empty", http.StatusOK, `{"integrations":[]}`, http.StatusOK, "{\"integrations\":[]}\n"},
		{"detached", http.StatusForbidden, ``, http.StatusServiceUnavailable, "Reflection integration is not attached to this VM\n"},
		{"upstream error", http.StatusInternalServerError, ``, http.StatusBadGateway, "Reflection integration is unavailable\n"},
		{"bad response", http.StatusOK, `{`, http.StatusBadGateway, "Invalid reflection integration response\n"},
		{"missing list", http.StatusOK, `{}`, http.StatusBadGateway, "Invalid reflection integration response\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || req.URL.String() != env.ReflectionURL()+"/integrations" {
					t.Fatalf("unexpected reflection request %s %s", req.Method, req.URL)
				}
				return &http.Response{
					StatusCode: tc.upstream,
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Header:     make(http.Header),
				}, nil
			})}
			rec := httptest.NewRecorder()
			handleIntegrationsIn(rec, httptest.NewRequest(http.MethodGet, "/api/integrations", nil), env, client)
			if rec.Code != tc.wantStatus || rec.Body.String() != tc.wantBody {
				t.Fatalf("got %d %q, want %d %q", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestIndexUsesReflectionEmojiDespiteStaleFalseOverride(t *testing.T) {
	srv, database, _ := newTestServer(t)
	srv.reflectionEmoji = func(context.Context) string { return "🧪" }
	if err := database.SetFeatureFlagOverride(t.Context(), "reflection-emoji-favicon", `false`); err != nil {
		t.Fatal(err)
	}

	svg := indexFaviconSVG(t, srv)
	if !strings.Contains(svg, ">🧪</text>") {
		t.Fatalf("favicon does not use reflection emoji: %s", svg)
	}
}

func TestIndexUsesShellEmojiWithoutReflectionMetadata(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.reflectionEmoji = func(context.Context) string { return "" }

	svg := indexFaviconSVG(t, srv)
	if !strings.Contains(svg, ">🐚</text>") {
		t.Fatalf("favicon does not default to shell emoji: %s", svg)
	}
}

func TestIndexEmojiFaviconEscapesReflectionMetadata(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.reflectionEmoji = func(context.Context) string { return "🐚&" }

	svg := indexFaviconSVG(t, srv)
	if !strings.Contains(svg, ">🐚&amp;</text>") {
		t.Fatalf("reflection emoji not safely embedded in favicon: %s", svg)
	}
}

func indexFaviconSVG(t *testing.T, srv *Server) string {
	t.Helper()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200: %s", w.Code, w.Body.String())
	}

	const prefix = `<link rel="icon" type="image/svg+xml" href="data:image/svg+xml,`
	body := w.Body.String()
	start := strings.Index(body, prefix)
	if start < 0 {
		t.Fatal("favicon link missing from index")
	}
	encoded := body[start+len(prefix):]
	end := strings.Index(encoded, `"/>`)
	if end < 0 {
		t.Fatal("favicon link is malformed")
	}
	encoded = html.UnescapeString(encoded[:end])
	svg, err := url.PathUnescape(encoded)
	if err != nil {
		t.Fatalf("decode favicon: %v", err)
	}
	return svg
}
