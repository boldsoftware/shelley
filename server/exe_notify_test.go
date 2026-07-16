package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/loop"
)

func newExeNotifyTestServer(t *testing.T) *Server {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := loop.NewPredictableService()
	// predictableOnly is false here: these tests exercise the exe.dev notify
	// integration logic itself, which is intentionally disabled in
	// predictable-only mode (see exeNotifyEnabled). A predictable LLM service is
	// still fine to back the server.
	return NewServer(database, &testLLMManager{service: ps},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		false, "predictable", "")
}

// withReflection swaps in a fake reflection client returning the given
// integrations JSON, restoring the original on cleanup.
func withReflection(t *testing.T, integrationsJSON string) {
	t.Helper()
	env, err := exeenv.Current()
	if err != nil {
		t.Fatal(err)
	}
	old := exeReflectionHTTPClient
	t.Cleanup(func() { exeReflectionHTTPClient = old })
	exeReflectionHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != env.ReflectionURL()+"/integrations" {
			t.Fatalf("unexpected reflection URL %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(integrationsJSON)),
			Header:     make(http.Header),
		}, nil
	})}
}

func TestExeNotifyEnabledWhenIntegrationPresent(t *testing.T) {
	withReflection(t, `{"integrations":[{"name":"notify","type":"notify"}]}`)
	s := newExeNotifyTestServer(t)
	if !s.exeNotifyEnabled(context.Background()) {
		t.Fatal("expected exe_notify enabled by default when integration present")
	}
}

func TestExeNotifyDisabledWhenNoIntegration(t *testing.T) {
	withReflection(t, `{"integrations":[{"name":"reflection","type":"reflection"}]}`)
	s := newExeNotifyTestServer(t)
	if s.exeNotifyEnabled(context.Background()) {
		t.Fatal("expected exe_notify disabled without notify integration")
	}
}

func TestExeNotifyDisabledInPredictableMode(t *testing.T) {
	// Even with the notify integration present and the setting at its default,
	// predictable-only mode (used by automated browser tests) must never enable
	// exe.dev push notifications.
	withReflection(t, `{"integrations":[{"name":"notify","type":"notify"}]}`)
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := loop.NewPredictableService()
	s := NewServer(database, &testLLMManager{service: ps},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		true /* predictableOnly */, "predictable", "")
	if s.exeNotifyEnabled(context.Background()) {
		t.Fatal("expected exe_notify disabled in predictable-only mode")
	}
}

func TestExeNotifyDisabledBySetting(t *testing.T) {
	withReflection(t, `{"integrations":[{"name":"notify","type":"notify"}]}`)
	s := newExeNotifyTestServer(t)
	if err := s.db.SetSetting(context.Background(), exeNotifySettingKey, "false"); err != nil {
		t.Fatal(err)
	}
	if s.exeNotifyEnabled(context.Background()) {
		t.Fatal("expected exe_notify disabled by setting")
	}
}

func hookURLs(hooks []db.ConversationHook) []string {
	urls := make([]string, len(hooks))
	for i, h := range hooks {
		urls[i] = h.URL
	}
	return urls
}

// TestReflectionProbeSkippedWithoutInjectedClient guards the fix that stops
// test binaries from firing REAL exe.dev push notifications to the VM owner's
// devices. Many tests (in this package and the integration test/ package) run
// with predictableOnly=false and mock LLMs; the reflection probe must NOT hit
// the real network unless a test has explicitly injected a fake client. With
// the default client under a test binary the probe short-circuits to false.
func TestReflectionProbeSkippedWithoutInjectedClient(t *testing.T) {
	old := exeReflectionHTTPClient
	t.Cleanup(func() { exeReflectionHTTPClient = old })
	exeReflectionHTTPClient = http.DefaultClient
	if exeDevHasNotifyIntegration() {
		t.Fatal("reflection probe must be disabled (no real network) when the" +
			" default client is used inside a test binary")
	}
}

func TestExeDevHasNotifyIntegrationUsesEnvironmentReflectionURL(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		wantURL  string
	}{
		{"production", "box.exe.xyz", "https://reflection.int.exe.xyz/integrations"},
		{"development", "box.exe.cloud", "http://reflection.int.exe.cloud/integrations"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := exeReflectionHTTPClient
			t.Cleanup(func() { exeReflectionHTTPClient = old })
			exeReflectionHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != tt.wantURL {
					t.Fatalf("unexpected reflection URL %s", req.URL)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"integrations":[{"name":"notify","type":"notify"}]}`)),
					Header:     make(http.Header),
				}, nil
			})}

			if !exeDevHasNotifyIntegrationIn(exeenv.FromHostname(tt.hostname)) {
				t.Fatal("expected notify integration")
			}
		})
	}
}

func TestWithExeNotifyHook(t *testing.T) {
	gw := exeNotifyGatewayURL
	cases := []struct {
		name    string
		hooks   []db.ConversationHook
		enabled bool
		want    []string
	}{
		{"disabled empty", nil, false, nil},
		{"enabled empty", nil, true, []string{gw}},
		{"enabled appends", []db.ConversationHook{{URL: "https://other.int.exe.xyz/"}}, true, []string{"https://other.int.exe.xyz/", gw}},
		{"enabled dedupes existing", []db.ConversationHook{{URL: gw}}, true, []string{gw}},
		{"enabled dedupes duplicates", []db.ConversationHook{{URL: gw}, {URL: gw}}, true, []string{gw}},
		{"disabled strips gateway", []db.ConversationHook{{URL: gw}}, false, nil},
		{"disabled strips gateway keeps others", []db.ConversationHook{{URL: "https://other.int.exe.xyz/"}, {URL: gw}}, false, []string{"https://other.int.exe.xyz/"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hookURLs(withExeNotifyHook(tc.hooks, tc.enabled))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("withExeNotifyHook = %v, want %v", got, tc.want)
			}
		})
	}
}
