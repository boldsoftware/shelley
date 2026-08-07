package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"tailscale.com/util/singleflight"

	"shelley.exe.dev/exeenv"
)

// exeReflectionHTTPClient is used to query reflection integration endpoints.
var exeReflectionHTTPClient = http.DefaultClient

// exeReflectionEmojiHTTPClient is separate so tests that mock integration
// discovery do not also receive root-document requests during page rendering.
var exeReflectionEmojiHTTPClient = http.DefaultClient

const (
	reflectionEmojiTimeout  = time.Second
	reflectionEmojiCacheTTL = time.Minute
)

var (
	reflectionEmojiMu    sync.Mutex
	reflectionEmojiValue string
	reflectionEmojiAt    time.Time
	reflectionEmojiOnce  bool
	reflectionEmojiFly   singleflight.Group[string, string]
)

type reflectionEmojiResponse struct {
	Emoji string `json:"emoji"`
}

// cachedReflectionEmoji returns the VM emoji from reflection. A short cache
// keeps concurrent page loads from making redundant requests.
func cachedReflectionEmoji(ctx context.Context) string {
	if testing.Testing() && exeReflectionEmojiHTTPClient == http.DefaultClient {
		return ""
	}
	env, err := exeenv.Current()
	if err != nil {
		return ""
	}
	return cachedReflectionEmojiIn(ctx, env)
}

func cachedReflectionEmojiIn(ctx context.Context, env exeenv.Environment) string {
	reflectionEmojiMu.Lock()
	if reflectionEmojiOnce && time.Since(reflectionEmojiAt) < reflectionEmojiCacheTTL {
		emoji := reflectionEmojiValue
		reflectionEmojiMu.Unlock()
		return emoji
	}
	reflectionEmojiMu.Unlock()

	emoji, _, _ := reflectionEmojiFly.Do("emoji", func() (string, error) {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reflectionEmojiTimeout)
		defer cancel()

		emoji := reflectionEmoji(fetchCtx, env)
		reflectionEmojiMu.Lock()
		reflectionEmojiValue, reflectionEmojiAt, reflectionEmojiOnce = emoji, time.Now(), true
		reflectionEmojiMu.Unlock()
		return emoji, nil
	})
	return emoji
}

func reflectionEmoji(ctx context.Context, env exeenv.Environment) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.ReflectionURL(), nil)
	if err != nil {
		return ""
	}
	resp, err := exeReflectionEmojiHTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var body reflectionEmojiResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ""
	}
	return strings.TrimSpace(body.Emoji)
}

func resetReflectionEmojiCache() {
	reflectionEmojiMu.Lock()
	reflectionEmojiValue = ""
	reflectionEmojiAt = time.Time{}
	reflectionEmojiOnce = false
	reflectionEmojiMu.Unlock()
}

// reflectionIntegration is one entry in the reflection /integrations response.
type reflectionIntegration struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// exeDevHasNotifyIntegration reports whether this VM has a "notify" integration
// available (i.e. push notifications to the owner's devices are possible). It
// queries the default "reflection" integration. Returns false if the
// integration is disabled/detached or on any network error.
func exeDevHasNotifyIntegration() bool {
	// Never probe the reflection API over the real network from a test binary
	// unless a test has explicitly injected its own client. Many server and
	// integration tests run with predictableOnly=false and mock LLMs; without
	// this guard every simulated end-of-turn would fire a REAL push to the VM
	// owner's devices whenever the host VM happens to have the "notify"
	// integration attached. Tests that exercise the reflection logic itself
	// override exeReflectionHTTPClient with a fake transport (see
	// exe_notify_test.go); those are unaffected because the client is no longer
	// the default.
	if testing.Testing() && exeReflectionHTTPClient == http.DefaultClient {
		return false
	}
	env, err := exeenv.Current()
	if err != nil {
		return false
	}
	return exeDevHasNotifyIntegrationIn(env)
}

func exeDevHasNotifyIntegrationIn(env exeenv.Environment) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", env.ReflectionURL()+"/integrations", nil)
	if err != nil {
		return false
	}
	resp, err := exeReflectionHTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Integrations []reflectionIntegration `json:"integrations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	for _, ig := range body.Integrations {
		if ig.Type == "notify" {
			return true
		}
	}
	return false
}
