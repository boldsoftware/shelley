package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/llm/predictable"
)

func newExeNotifyTestServer(t *testing.T) *Server {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := predictable.NewService()
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
	old := reflectionHTTPClient()
	t.Cleanup(func() { setReflectionHTTPClient(old) })
	setReflectionHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != env.ReflectionURL()+"/integrations" {
			t.Fatalf("unexpected reflection URL %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(integrationsJSON)),
			Header:     make(http.Header),
		}, nil
	})})
}

func TestExeNotifyEnabledWhenIntegrationPresent(t *testing.T) {
	withReflection(t, `{"integrations":[{"name":"notify","type":"notify"}]}`)
	s := newExeNotifyTestServer(t)
	if !s.exeNotifyEnabled(t.Context()) {
		t.Fatal("expected exe_notify enabled by default when integration present")
	}
}

func TestExeNotifyDisabledWhenNoIntegration(t *testing.T) {
	withReflection(t, `{"integrations":[{"name":"reflection","type":"reflection"}]}`)
	s := newExeNotifyTestServer(t)
	if s.exeNotifyEnabled(t.Context()) {
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
	ps := predictable.NewService()
	s := NewServer(database, &testLLMManager{service: ps},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		true /* predictableOnly */, "predictable", "")
	if s.exeNotifyEnabled(t.Context()) {
		t.Fatal("expected exe_notify disabled in predictable-only mode")
	}
}

func TestExeNotifyDisabledBySetting(t *testing.T) {
	withReflection(t, `{"integrations":[{"name":"notify","type":"notify"}]}`)
	s := newExeNotifyTestServer(t)
	if err := s.db.SetSetting(t.Context(), exeNotifySettingKey, "false"); err != nil {
		t.Fatal(err)
	}
	if s.exeNotifyEnabled(t.Context()) {
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
	old := reflectionHTTPClient()
	t.Cleanup(func() { setReflectionHTTPClient(old) })
	setReflectionHTTPClient(http.DefaultClient)
	if exeDevHasNotifyIntegration() {
		t.Fatal("reflection probe must be disabled (no real network) when the" +
			" default client is used inside a test binary")
	}
}

func TestExeDevHasNotifyIntegrationUsesEnvironmentReflectionURL(t *testing.T) {
	tests := []struct {
		name    string
		scheme  string
		boxHost string
		wantURL string
	}{
		{"production", "https", "exe.xyz", "https://reflection.int.exe.xyz/integrations"},
		{"development", "http", "exe.cloud", "http://reflection.int.exe.cloud/integrations"},
		{"configured HTTPS", "https", "example.test", "https://reflection.int.example.test/integrations"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := reflectionHTTPClient()
			t.Cleanup(func() { setReflectionHTTPClient(old) })
			setReflectionHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != tt.wantURL {
					t.Fatalf("unexpected reflection URL %s", req.URL)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"integrations":[{"name":"notify","type":"notify"}]}`)),
					Header:     make(http.Header),
				}, nil
			})})

			env, err := exeenv.New(tt.scheme, tt.boxHost)
			if err != nil {
				t.Fatal(err)
			}
			if !exeDevHasNotifyIntegrationIn(env) {
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

func TestHandleTestExeNotifyDisabledInTests(t *testing.T) {
	old := setReflectionHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("test handler must not reach the network: %s", req.URL)
		return nil, nil
	})})
	t.Cleanup(func() { setReflectionHTTPClient(old) })

	rec := httptest.NewRecorder()
	(&Server{}).handleTestExeNotify(rec, httptest.NewRequest(http.MethodPost, "/api/integrations/notify/test",
		strings.NewReader(`{"message":"Hello"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleTestExeNotifyFreshAvailabilityAfterCachedFalse(t *testing.T) {
	env, err := exeenv.New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	s := newExeNotifyTestServer(t)
	s.exeNotifyOnce.Do(func() { s.exeNotifyDetected = false })
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case env.ReflectionURL() + "/integrations":
			return testHTTPResponse(http.StatusOK, `{"integrations":[{"name":"notify","type":"notify"}]}`), nil
		case env.IntegrationURL("notify", false) + "/":
			var body map[string]string
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" ||
				body["title"] != "Shelley test" || body["body"] != "Hello" || len(body) != 2 {
				t.Fatalf("notification request = method %s body %#v", req.Method, body)
			}
			return testHTTPResponse(http.StatusNoContent, ""), nil
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return nil, nil
		}
	})}
	rec := httptest.NewRecorder()
	s.handleTestExeNotifyIn(rec, httptest.NewRequest(http.MethodPost, "/api/integrations/notify/test",
		strings.NewReader(`{"message":"Hello"}`)), env, client)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleTestExeNotifyRejectsInvalidMessage(t *testing.T) {
	env, err := exeenv.New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	s := newExeNotifyTestServer(t)
	client := notifyTestClient(t, env, http.StatusNoContent)
	rec := httptest.NewRecorder()
	s.handleTestExeNotifyIn(rec, httptest.NewRequest(http.MethodPost, "/api/integrations/notify/test",
		strings.NewReader(`{"message":"   "}`)), env, client)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleTestExeNotifyGatewayRejection(t *testing.T) {
	env, err := exeenv.New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	s := newExeNotifyTestServer(t)
	client := notifyTestClient(t, env, http.StatusForbidden)
	rec := httptest.NewRecorder()
	s.handleTestExeNotifyIn(rec, httptest.NewRequest(http.MethodPost, "/api/integrations/notify/test",
		strings.NewReader(`{"message":"Hello"}`)), env, client)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleTestExeNotifyUnavailableAndPredictable(t *testing.T) {
	env, err := exeenv.New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("no attach", func(t *testing.T) {
		s := newExeNotifyTestServer(t)
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != env.ReflectionURL()+"/integrations" {
				t.Fatalf("unexpected request %s", req.URL)
			}
			return testHTTPResponse(http.StatusOK, `{"integrations":[]}`), nil
		})}
		rec := httptest.NewRecorder()
		s.handleTestExeNotifyIn(rec, httptest.NewRequest(http.MethodPost, "/api/integrations/notify/test",
			strings.NewReader(`{"message":"Hello"}`)), env, client)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("reflection error", func(t *testing.T) {
		s := newExeNotifyTestServer(t)
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("unavailable")
		})}
		rec := httptest.NewRecorder()
		s.handleTestExeNotifyIn(rec, httptest.NewRequest(http.MethodPost, "/api/integrations/notify/test",
			strings.NewReader(`{"message":"Hello"}`)), env, client)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("predictable", func(t *testing.T) {
		s := newExeNotifyTestServer(t)
		s.predictableOnly = true
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("predictable mode queried notify")
			return nil, nil
		})}
		rec := httptest.NewRecorder()
		s.handleTestExeNotifyIn(rec, httptest.NewRequest(http.MethodPost, "/api/integrations/notify/test",
			strings.NewReader(`{"message":"Hello"}`)), env, client)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
	})
}

func notifyTestClient(t *testing.T, env exeenv.Environment, gatewayStatus int) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case env.ReflectionURL() + "/integrations":
			return testHTTPResponse(http.StatusOK, `{"integrations":[{"name":"notify","type":"notify"}]}`), nil
		case env.IntegrationURL("notify", false) + "/":
			return testHTTPResponse(gatewayStatus, ""), nil
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return nil, nil
		}
	})}
}

func testHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
