package browse

import (
	"context"
	"fmt"
	"html"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"shelley.exe.dev/llm"
)

// emulateTestEnv holds shared scaffolding for emulation tests that verify
// user-agent override behaviour. Call newEmulateTestEnv from each test.
type emulateTestEnv struct {
	ctx          context.Context
	emuTool      *llm.Tool
	getUserAgent func(t *testing.T) string
	baselineUA   string
}

// newEmulateTestEnv starts a local HTTP server that echoes the User-Agent
// header, initialises BrowseTools, and captures the browser's baseline UA.
// Registers cleanup via t.Cleanup. Skips if the browser is unavailable.
func newEmulateTestEnv(t *testing.T) *emulateTestEnv {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start listener: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/ua", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><pre id="ua">%s</pre></body></html>`, html.EscapeString(r.UserAgent()))
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	tools := NewBrowseTools(ctx, 0, 0)
	t.Cleanup(func() { tools.Close() })

	browserTool := tools.CombinedTool()
	baseURL := fmt.Sprintf("http://127.0.0.1:%d/ua", port)

	getUserAgent := func(t *testing.T) string {
		t.Helper()
		toolOut := browserTool.Run(ctx, []byte(fmt.Sprintf(`{"action": "navigate", "url": %q}`, baseURL)))
		if toolOut.Error != nil {
			if strings.Contains(toolOut.Error.Error(), "failed to start browser") {
				t.Skip("Browser automation not available in this environment")
			}
			t.Fatalf("Navigation error: %v", toolOut.Error)
		}
		browserCtx, err := tools.GetBrowserContext()
		if err != nil {
			t.Fatalf("Failed to get browser context: %v", err)
		}
		var ua string
		if err := chromedp.Run(browserCtx, chromedp.Text("#ua", &ua)); err != nil {
			t.Fatalf("Failed to read user agent from page: %v", err)
		}
		return ua
	}

	baselineUA := getUserAgent(t)
	t.Logf("Baseline UA: %s", baselineUA)

	return &emulateTestEnv{
		ctx:          ctx,
		emuTool:      tools.EmulateTool(),
		getUserAgent: getUserAgent,
		baselineUA:   baselineUA,
	}
}

// TestEmulateDeviceSwitchClearsUserAgent verifies that switching from a mobile
// preset (which sets a user agent override) to a desktop preset (which has no
// user agent) correctly clears the mobile user agent instead of leaking it.
// Not parallel: shares browser process via NewBrowseTools.
func TestEmulateDeviceSwitchClearsUserAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser emulation test in short mode")
	}

	env := newEmulateTestEnv(t)

	// Step 1: Emulate iphone_14 — UA should contain "iPhone".
	toolOut := env.emuTool.Run(env.ctx, []byte(`{"action": "device", "device": "iphone_14"}`))
	if toolOut.Error != nil {
		if strings.Contains(toolOut.Error.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Emulate iphone_14 error: %v", toolOut.Error)
	}

	ua := env.getUserAgent(t)
	if !strings.Contains(ua, "iPhone") {
		t.Fatalf("After iphone_14 emulation, expected UA to contain 'iPhone', got: %s", ua)
	}
	t.Logf("After iphone_14: UA=%s", ua)

	// Step 2: Switch to desktop_hd — UA should NOT contain "iPhone".
	toolOut = env.emuTool.Run(env.ctx, []byte(`{"action": "device", "device": "desktop_hd"}`))
	if toolOut.Error != nil {
		t.Fatalf("Emulate desktop_hd error: %v", toolOut.Error)
	}

	ua = env.getUserAgent(t)
	if strings.Contains(ua, "iPhone") {
		t.Fatalf("After desktop_hd emulation, UA still contains 'iPhone' (leaked mobile UA): %s", ua)
	}
	// Verify the override was truly cleared (not just emptied to "") by
	// confirming the UA is back to the browser's baseline.
	if ua != env.baselineUA {
		t.Fatalf("After desktop_hd emulation, expected UA to match baseline %q, got: %s", env.baselineUA, ua)
	}
	t.Logf("After desktop_hd: UA=%s", ua)
}

// TestEmulateDeviceToCustomClearsUserAgent verifies that switching from a mobile
// preset to a custom viewport clears the mobile UA override.
// Not parallel: shares browser process via NewBrowseTools.
func TestEmulateDeviceToCustomClearsUserAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser emulation test in short mode")
	}

	env := newEmulateTestEnv(t)

	// Step 1: Emulate iphone_14 — UA should contain "iPhone".
	toolOut := env.emuTool.Run(env.ctx, []byte(`{"action": "device", "device": "iphone_14"}`))
	if toolOut.Error != nil {
		if strings.Contains(toolOut.Error.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Emulate iphone_14 error: %v", toolOut.Error)
	}

	ua := env.getUserAgent(t)
	if !strings.Contains(ua, "iPhone") {
		t.Fatalf("After iphone_14 emulation, expected UA to contain 'iPhone', got: %s", ua)
	}
	t.Logf("After iphone_14: UA=%s", ua)

	// Step 2: Switch to a custom viewport — UA should revert to baseline.
	toolOut = env.emuTool.Run(env.ctx, []byte(`{"action": "custom", "width": 1280, "height": 800}`))
	if toolOut.Error != nil {
		t.Fatalf("Emulate custom error: %v", toolOut.Error)
	}

	ua = env.getUserAgent(t)
	if strings.Contains(ua, "iPhone") {
		t.Fatalf("After custom emulation, UA still contains 'iPhone' (leaked mobile UA): %s", ua)
	}
	if ua != env.baselineUA {
		t.Fatalf("After custom emulation, expected UA to match baseline %q, got: %s", env.baselineUA, ua)
	}
	t.Logf("After custom: UA=%s", ua)
}
