package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/exeenv"
)

func TestHandleTestSlackDestination(t *testing.T) {
	const attached = `{"integrations":[{"name":"slack-hook","type":"slack","team":true}]}`
	for _, tc := range []struct {
		name           string
		reflection     string
		reflectionCode int
		gatewayCode    int
		gatewayBody    string
		want           int
		wantSend       bool
	}{
		{"team hook", attached, 200, 200, "ok", 204, true},
		{"detached", `{"integrations":[]}`, 200, 200, "ok", 409, false},
		{"wrong scope", `{"integrations":[{"name":"slack-hook","type":"slack"}]}`, 200, 200, "ok", 409, false},
		{"wrong type", `{"integrations":[{"name":"slack-hook","type":"http-proxy","team":true}]}`, 200, 200, "ok", 409, false},
		{"reflection unavailable", attached, 500, 200, "ok", 502, false},
		{"gateway rejected", attached, 200, 429, "rate_limited", 502, true},
		{"unconfirmed delivery", attached, 200, 200, "invalid_payload", 502, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := exeenv.New("http", "example.test")
			if err != nil {
				t.Fatal(err)
			}
			sent := false
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case env.ReflectionURL() + "/integrations":
					if req.Method != http.MethodGet {
						t.Fatalf("reflection method = %s", req.Method)
					}
					return testHTTPResponse(tc.reflectionCode, tc.reflection), nil
				case env.IntegrationURL("slack-hook", true) + "/":
					if !tc.wantSend {
						t.Fatal("sent a message without verifying its destination")
					}
					if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" {
						t.Fatalf("invalid Slack request: %s %v", req.Method, req.Header)
					}
					var payload map[string]string
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					if len(payload) != 1 || payload["text"] != "Hello" {
						t.Fatalf("Slack payload = %#v", payload)
					}
					sent = true
					return testHTTPResponse(tc.gatewayCode, tc.gatewayBody), nil
				case env.IntegrationURL("slack-hook", true) + "/api/auth.test":
					if !sent || tc.want != http.StatusBadGateway {
						t.Fatal("identity check without a failed send")
					}
					return testHTTPResponse(http.StatusBadRequest, "invalid_payload"), nil
				default:
					t.Fatalf("unexpected request: %s", req.URL)
					return nil, nil
				}
			})}
			rec := httptest.NewRecorder()
			(&Server{}).handleTestSlackIn(rec, httptest.NewRequest(http.MethodPost, "/api/integrations/slack/test",
				strings.NewReader(`{"name":"slack-hook","team":true,"message":" Hello "}`)),
				env, client)
			if rec.Code != tc.want || sent != tc.wantSend {
				t.Fatalf("status=%d sent=%v body=%s", rec.Code, sent, rec.Body.String())
			}
		})
	}
}

func TestHandleTestSlackBotDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		body         string
		networkError bool
		want         int
	}{
		{"bot", 200, `{"ok":true,"bot_id":"B123"}`, false, 422},
		{"webhook", 400, "invalid_payload", false, 502},
		{"bad authentication", 200, `{"ok":false,"error":"invalid_auth"}`, false, 502},
		{"user identity", 200, `{"ok":true,"user_id":"U123"}`, false, 502},
		{"missing confirmation", 200, `{"bot_id":"B123"}`, false, 502},
		{"HTTP failure", 403, `{"ok":true,"bot_id":"B123"}`, false, 502},
		{"malformed response", 200, "{", false, 502},
		{"network failure", 0, "", true, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := exeenv.New("https", "example.test")
			if err != nil {
				t.Fatal(err)
			}
			sent, diagnosed := false, false
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case env.ReflectionURL() + "/integrations":
					return testHTTPResponse(200, `{"integrations":[{"name":"slack-hook","type":"slack","team":true}]}`), nil
				case env.IntegrationURL("slack-hook", true) + "/":
					sent = true
					return testHTTPResponse(404, "not_found"), nil
				case env.IntegrationURL("slack-hook", true) + "/api/auth.test":
					if !sent || req.Method != http.MethodPost || req.Body != nil {
						t.Fatal("identity check must follow a failed test and have no message payload")
					}
					diagnosed = true
					if tc.networkError {
						return nil, errors.New("identity check unavailable")
					}
					return testHTTPResponse(tc.status, tc.body), nil
				default:
					t.Fatalf("unexpected request: %s", req.URL)
					return nil, nil
				}
			})}
			rec := httptest.NewRecorder()
			(&Server{}).handleTestSlackIn(rec, httptest.NewRequest(http.MethodPost, "/api/integrations/slack/test",
				strings.NewReader(`{"name":"slack-hook","team":true,"message":"Hello"}`)), env, client)
			if rec.Code != tc.want || !diagnosed {
				t.Fatalf("status=%d diagnosed=%v body=%s", rec.Code, diagnosed, rec.Body.String())
			}
			if tc.want == 422 {
				if strings.TrimSpace(rec.Body.String()) != "This is a Slack bot. This test only supports incoming webhooks." {
					t.Fatalf("unexpected bot error: %s", rec.Body.String())
				}
			} else if !strings.Contains(rec.Body.String(), "HTTP 404") {
				t.Fatalf("identity failure hid the original error: %s", rec.Body.String())
			}
		})
	}
}

func TestHandleTestSlackRejectsBeforeNetwork(t *testing.T) {
	env, err := exeenv.New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unexpected network request")
		return nil, nil
	})}
	for _, tc := range []struct {
		name        string
		body        string
		predictable bool
		want        int
	}{
		{"empty message", `{"name":"slack-hook","message":" "}`, false, 400},
		{"missing hook", `{"message":"Hello"}`, false, 400},
		{"malformed", `{`, false, 400},
		{"too long", `{"name":"slack-hook","message":"` + strings.Repeat("x", 201) + `"}`, false, 400},
		{"predictable", `{"name":"slack-hook","message":"Hello"}`, true, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			(&Server{predictableOnly: tc.predictable}).handleTestSlackIn(rec,
				httptest.NewRequest(http.MethodPost, "/api/integrations/slack/test", strings.NewReader(tc.body)),
				env, client)
			if rec.Code != tc.want {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
