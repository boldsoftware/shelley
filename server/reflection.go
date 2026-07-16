package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"shelley.exe.dev/exeenv"
)

// exeReflectionHTTPClient is the HTTP client used to query the exe.dev
// reflection API. It is a package var so tests can inject a transport.
var exeReflectionHTTPClient = http.DefaultClient

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
