package server

import (
	"context"
	"io"
	"net/http"
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

	if got := cachedReflectionEmojiIn(context.Background(), env); got != "🐚" {
		t.Fatalf("cachedReflectionEmojiIn() = %q, want 🐚", got)
	}
	if got := cachedReflectionEmojiIn(context.Background(), env); got != "🐚" {
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

	if got := cachedReflectionEmojiIn(context.Background(), env); got != "" {
		t.Fatalf("cachedReflectionEmojiIn() = %q, want empty", got)
	}
}

func TestGenerateEmojiFaviconSVG(t *testing.T) {
	svg := generateEmojiFaviconSVG("🐚&")
	if !strings.Contains(svg, ">🐚&amp;</text>") {
		t.Fatalf("emoji not safely embedded in SVG: %s", svg)
	}
}
