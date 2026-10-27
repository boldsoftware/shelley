package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"shelley.exe.dev/db/generated"
	_ "shelley.exe.dev/server/notifications/channels"
)

func notificationRequest(t *testing.T, s *Server, method, id string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, "/api/notification-channels/"+id, strings.NewReader(string(raw)))
	w := httptest.NewRecorder()
	switch {
	case strings.HasSuffix(id, "/test"):
		s.handleTestNotificationChannel(w, r, strings.TrimSuffix(id, "/test"))
	case method == http.MethodPost:
		s.handleCreateNotificationChannel(w, r)
	case method == http.MethodPut:
		s.handleUpdateNotificationChannel(w, r, id)
	case id != "":
		s.handleGetNotificationChannel(w, r, id)
	default:
		s.handleListNotificationChannels(w, r)
	}
	return w
}

func notificationResponse(t *testing.T, w *httptest.ResponseRecorder, status int, secrets []string) NotificationChannelAPI {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	for _, secret := range []string{"fake-webhook", "fake-token", "fake-password", "replacement-token", "replacement-password"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("response exposed %q: %s", secret, w.Body.String())
		}
	}
	var ch NotificationChannelAPI
	if err := json.Unmarshal(w.Body.Bytes(), &ch); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ch.ConfiguredSecrets, secrets) {
		t.Fatalf("configured secrets = %v, want %v", ch.ConfiguredSecrets, secrets)
	}
	config := ch.Config.(map[string]any)
	for _, key := range []string{"webhook_url", "token", "password"} {
		if _, exists := config[key]; exists {
			t.Fatalf("secret field %s present in response", key)
		}
	}
	return ch
}

func TestNotificationChannelSecretLifecycle(t *testing.T) {
	for _, kind := range []string{"discord", "ntfy"} {
		t.Run(kind, func(t *testing.T) {
			s, database, _ := newTestServer(t)
			config := map[string]any{"webhook_url": "http://127.0.0.1/fake-webhook"}
			secrets := []string{"webhook_url"}
			if kind == "ntfy" {
				config = map[string]any{"server": "http://127.0.0.1", "topic": "topic", "username": "reader", "token": "fake-token", "password": "fake-password", "obsolete": "removed on update"}
				secrets = []string{"token", "password"}
			}
			created := notificationResponse(t, notificationRequest(t, s, "POST", "", CreateNotificationChannelRequest{ChannelType: kind, DisplayName: "original", Enabled: true, Config: config}), http.StatusCreated, secrets)
			notificationResponse(t, notificationRequest(t, s, "GET", created.ChannelID, nil), http.StatusOK, secrets)
			list := notificationRequest(t, s, "GET", "", nil)
			if list.Code != http.StatusOK {
				t.Fatal(list.Body.String())
			}
			var rows []json.RawMessage
			if err := json.Unmarshal(list.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 {
				t.Fatalf("list length = %d", len(rows))
			}
			single := httptest.NewRecorder()
			single.Body.Write(rows[0])
			notificationResponse(t, single, http.StatusOK, secrets)
			beforeInvalid, err := database.GetNotificationChannel(t.Context(), created.ChannelID)
			if err != nil {
				t.Fatal(err)
			}
			for _, malformed := range []any{nil, "bad", []string{}, map[string]any{"token": false, "webhook_url": 123}} {
				w := notificationRequest(t, s, "PUT", created.ChannelID, UpdateNotificationChannelRequest{DisplayName: "invalid", Config: malformed})
				if w.Code != http.StatusBadRequest {
					t.Fatalf("malformed update accepted: %s", w.Body.String())
				}
				after, err := database.GetNotificationChannel(t.Context(), created.ChannelID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(*beforeInvalid, *after) {
					t.Fatal("malformed update changed row")
				}
			}
			ordinary := map[string]any{}
			if kind == "ntfy" {
				ordinary = map[string]any{"server": "http://127.0.0.1", "topic": "edited", "username": "reader"}
			}
			for _, enabled := range []bool{true, false, true} {
				notificationResponse(t, notificationRequest(t, s, "PUT", created.ChannelID, UpdateNotificationChannelRequest{DisplayName: "edited", Enabled: enabled, Config: ordinary}), http.StatusOK, secrets)
				stored, err := database.GetNotificationChannel(t.Context(), created.ChannelID)
				if err != nil {
					t.Fatal(err)
				}
				got, err := notificationConfig([]byte(stored.Config), kind)
				if err != nil {
					t.Fatal(err)
				}
				for _, key := range secrets {
					if got[key] != config[key] {
						t.Fatalf("%s not preserved: %v", key, got)
					}
				}
				if _, exists := got["obsolete"]; exists {
					t.Fatal("ordinary field unexpectedly preserved")
				}
				if (stored.Enabled != 0) != enabled {
					t.Fatal("enabled state not saved")
				}
			}
			if kind == "ntfy" {
				ordinary["token"] = "replacement-token"
				ordinary["password"] = "replacement-password"
			} else {
				ordinary["webhook_url"] = "http://127.0.0.1/fake-webhook-replacement"
			}
			notificationResponse(t, notificationRequest(t, s, "PUT", created.ChannelID, UpdateNotificationChannelRequest{DisplayName: "replaced", Enabled: false, Config: ordinary}), http.StatusOK, secrets)
			stored, err := database.GetNotificationChannel(t.Context(), created.ChannelID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := notificationConfig([]byte(stored.Config), kind)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range secrets {
				if got[key] != ordinary[key] {
					t.Fatalf("%s not replaced", key)
				}
			}
			if kind == "ntfy" {
				ordinary["token"] = ""
				notificationResponse(t, notificationRequest(t, s, "PUT", created.ChannelID, UpdateNotificationChannelRequest{DisplayName: "clear token", Config: ordinary}), http.StatusOK, []string{"password"})
				delete(ordinary, "token")
				ordinary["password"] = ""
				notificationResponse(t, notificationRequest(t, s, "PUT", created.ChannelID, UpdateNotificationChannelRequest{DisplayName: "clear password", Config: ordinary}), http.StatusOK, []string{})
				stored, err := database.GetNotificationChannel(t.Context(), created.ChannelID)
				if err != nil {
					t.Fatal(err)
				}
				got, err := notificationConfig([]byte(stored.Config), kind)
				if err != nil {
					t.Fatal(err)
				}
				if got["token"] != "" || got["password"] != "" {
					t.Fatalf("credentials not cleared: %v", got)
				}
			} else {
				before := *stored
				for _, value := range []any{"", nil, 123} {
					ordinary["webhook_url"] = value
					w := notificationRequest(t, s, "PUT", created.ChannelID, UpdateNotificationChannelRequest{DisplayName: "invalid", Config: ordinary})
					if w.Code != http.StatusBadRequest {
						t.Fatalf("cleared/invalid webhook accepted: %s", w.Body.String())
					}
					after, err := database.GetNotificationChannel(t.Context(), created.ChannelID)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(before, *after) {
						t.Fatal("rejected update changed stored channel")
					}
				}
			}
		})
	}
}

func TestNotificationChannelInvalidConfiguration(t *testing.T) {
	s, database, _ := newTestServer(t)
	for _, kind := range []string{"discord", "ntfy"} {
		for _, config := range []any{nil, "not-an-object", []string{}, 17, map[string]any{"token": 123}} {
			w := notificationRequest(t, s, "POST", "", CreateNotificationChannelRequest{ChannelType: kind, DisplayName: "invalid", Config: config})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s config %v: status %d", kind, config, w.Code)
			}
		}
	}
	rows, err := database.GetNotificationChannels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatal("invalid creates stored channels")
	}
	for _, raw := range []string{"{", "null", "[]", `{"token":123}`} {
		stored, err := database.CreateNotificationChannel(t.Context(), generated.CreateNotificationChannelParams{ChannelID: "broken", ChannelType: "ntfy", DisplayName: "broken", Config: raw})
		if err != nil {
			t.Fatal(err)
		}
		for _, action := range []struct {
			method, id string
			body       any
		}{
			{"GET", "", nil},
			{"GET", "broken", nil},
			{"POST", "broken/test", nil},
			{"PUT", "broken", UpdateNotificationChannelRequest{DisplayName: "changed", Config: map[string]any{"server": "http://127.0.0.1", "topic": "topic"}}},
		} {
			w := notificationRequest(t, s, action.method, action.id, action.body)
			if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "Invalid stored notification configuration") {
				t.Fatalf("malformed stored config response %d: %s", w.Code, w.Body.String())
			}
		}
		after, err := database.GetNotificationChannel(t.Context(), "broken")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(*stored, *after) {
			t.Fatal("malformed configuration changed")
		}
		if err := database.DeleteNotificationChannel(t.Context(), "broken"); err != nil {
			t.Fatal(err)
		}
	}
	// A legacy Discord row with no webhook cannot be saved without supplying one.
	_, err = database.CreateNotificationChannel(t.Context(), generated.CreateNotificationChannelParams{ChannelID: "missing", ChannelType: "discord", DisplayName: "missing", Config: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range []any{map[string]any{}, map[string]any{"webhook_url": ""}, nil, []string{}, "bad"} {
		w := notificationRequest(t, s, "PUT", "missing", UpdateNotificationChannelRequest{DisplayName: "changed", Config: config})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("missing webhook accepted: %s", w.Body.String())
		}
		row, err := database.GetNotificationChannel(t.Context(), "missing")
		if err != nil {
			t.Fatal(err)
		}
		if row.Config != `{}` || row.DisplayName != "missing" {
			t.Fatal("invalid update changed row")
		}
	}
}

func TestNotificationChannelTestSafeFailures(t *testing.T) {
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "fake-webhook") && r.Header.Get("Authorization") != "Bearer fake-token" {
			t.Error("saved credential not used")
		}
		http.Error(w, "fake-token fake-password fake-webhook", http.StatusUnauthorized)
	}))
	defer downstream.Close()
	for _, config := range []struct{ kind, raw string }{
		{"discord", `{"webhook_url":"` + downstream.URL + `/fake-webhook"}`},
		{"ntfy", `{"server":"` + downstream.URL + `","topic":"test","token":"fake-token","password":"fake-password"}`},
		{"discord", `{"webhook_url":"http://[fake-webhook"}`},
		{"discord", `{}`},
	} {
		t.Run(config.raw, func(t *testing.T) {
			s, database, _ := newTestServer(t)
			_, err := database.CreateNotificationChannel(t.Context(), generated.CreateNotificationChannelParams{ChannelID: "test", ChannelType: config.kind, DisplayName: "test", Config: config.raw})
			if err != nil {
				t.Fatal(err)
			}
			w := notificationRequest(t, s, "POST", "test/test", nil)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			var result struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Success || result.Message != "Test notification failed. Check the saved channel configuration." {
				t.Fatalf("unsafe failure: %s", w.Body.String())
			}
		})
	}
}
