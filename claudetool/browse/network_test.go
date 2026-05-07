package browse

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestBrowserNetworkClearCache exercises the clear_cache action end-to-end.
//
// It serves a page whose body contains a counter that increments on each
// server hit, with a long Cache-Control max-age. Navigating twice should
// yield the same counter (served from cache). After clear_cache, navigating
// again should hit the server and bump the counter.
func TestBrowserNetworkClearCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser network clear_cache test in short mode")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start listener: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	var hits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/cached", func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html><html><body><div id="hits">%d</div></body></html>`, n)
	})

	server := &http.Server{Handler: mux}
	go server.Serve(listener)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0, 0)
	t.Cleanup(func() { tools.Close() })
	t.Cleanup(func() { server.Close() })

	browser := tools.CombinedTool()
	netTool := tools.NetworkTool()

	navURL := fmt.Sprintf(`{"action": "navigate", "url": "http://127.0.0.1:%d/cached"}`, port)
	readHits := []byte(`{"action": "eval", "expression": "document.getElementById('hits').textContent"}`)

	// Navigation 1: server should see a hit.
	out := browser.Run(ctx, []byte(navURL))
	if out.Error != nil {
		if strings.Contains(out.Error.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("navigate 1: %v", out.Error)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("after first nav: hits=%d, want 1", got)
	}

	// Navigation 2: should be served from cache, no new server hit.
	out = browser.Run(ctx, []byte(navURL))
	if out.Error != nil {
		t.Fatalf("navigate 2: %v", out.Error)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("after cached nav: hits=%d, want 1 (cache should have served the page)", got)
	}

	// Sanity check the page content reflects hit #1 (eval returns the
	// JSON-encoded textContent, so we expect the literal string `"1"`).
	out = browser.Run(ctx, readHits)
	if out.Error != nil {
		t.Fatalf("eval hits: %v", out.Error)
	}
	if body := out.LLMContent[0].Text; !strings.Contains(body, `"1"`) {
		t.Errorf("expected cached page to show hit count 1, got: %s", body)
	}

	// Clear the browser cache.
	out = netTool.Run(ctx, []byte(`{"action": "clear_cache"}`))
	if out.Error != nil {
		t.Fatalf("clear_cache: %v", out.Error)
	}
	if body := out.LLMContent[0].Text; !strings.Contains(body, "cache cleared") {
		t.Errorf("expected confirmation, got: %s", body)
	}

	// Navigation 3: cache is gone, server should see a fresh hit.
	out = browser.Run(ctx, []byte(navURL))
	if out.Error != nil {
		t.Fatalf("navigate 3: %v", out.Error)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("after clear_cache + nav: hits=%d, want 2 (cache should have been invalidated)", got)
	}
}
