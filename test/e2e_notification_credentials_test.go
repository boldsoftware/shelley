package test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Ported from ui/e2e/notification-credentials.spec.ts.

// notificationCredentialsSecrets are the credentials the test types; no
// answer from the server may hold one.
var notificationCredentialsSecrets = []string{"fake-webhook", "fake-token", "fake-password", "replacement-token"}

// notificationCredentialsSave is a channel save the page sent and what the
// server answered.
type notificationCredentialsSave struct {
	status int
	sent   map[string]any // the request's config
	answer []byte
}

// notificationCredentialsHow describes the fake server and the calls of the
// notification credentials tests.
const notificationCredentialsHow = `The screen is the default 393x727 phone. A local webhook receiver runs at {{URL}}. Every POST or PUT the page sends to /api/notification-channels... is a "save", numbered from 1 in order (the Test button's request is not a save). For save N: call "save N" returns "ok" when the server answered it with a 2xx status and none of the secrets "fake-webhook", "fake-token", "fake-password", "replacement-token" in its answer (otherwise what was wrong, or "none" before that save); call "save N sent" returns the webhook_url, token and password fields of the config the page sent, as a JSON object holding only those present; "save N config" returns the config of the server's answer as JSON; "save N secrets" returns the answer's configured_secrets as JSON. Call "test success" returns the success field of the server's answer to the Test button as JSON ("none" before one); call "received" returns the path of the last request the webhook receiver got ("none" before one). Name elements by ARIA role and accessible name, as Playwright's getByRole does; "the X button" is the button named exactly X, unless it belongs to a card. "The name input" is the input inside the ".form-group" that has a label whose text is exactly "Display Name". "The webhook input" is "#config-webhook_url". "Open the notifications": press Control+k, fill ".command-palette-input" with "notification", click the first ".command-palette-item", and then the button whose name contains "+ Add Channel" should be visible. `

func TestE2ENotificationCredentialsDiscordStayServerSide(t *testing.T) {
	e2e(t, useful(0), `Discord webhook credentials stay on the server through create, edit, toggle and test. `+notificationCredentialsHow+`Perform the steps in order. 1. Navigate to /c/{{SLUG}} and open the notifications. 2. Click the button whose name contains "+ Add Channel", then the ".provider-btn" whose text is exactly "Discord Webhook". 3. Fill the name input with "Secret Discord". 4. The webhook input should have the attribute type="password" and the placeholder exactly "https://discord.com/api/webhooks/...". The "Add Channel" button should be disabled. 5. Fill the webhook input with "{{URL}}/fake-webhook" and click the "Add Channel" button. Call "save 1" until it returns "ok"; "save 1 config" must return {} and "save 1 secrets" must return ["webhook_url"]. 6. "The Discord card" is the ".model-card" containing the text "Secret Discord"; it should be visible. Click its button named exactly "Edit". 7. The webhook input should have the value "" (empty) and the placeholder exactly "Leave blank to keep the saved webhook on the server". There must be no checkbox named exactly "Clear saved credential" (count 0). The "Save" button should be enabled. 8. Fill the name input with "Edited Discord" and click the "Save" button. Call "save 2" until it returns "ok"; "save 2 sent" must return {} (the page did not send the webhook). 9. "The edited card" is the ".model-card" containing "Edited Discord". Click its button named exactly "On"; call "save 3" until it returns "ok". The edited card's button named exactly "Off" should be visible; click it and call "save 4" until it returns "ok". 10. Click the edited card's button named exactly "Edit" and fill the webhook input with "{{URL}}/unsaved-webhook". Click the "Test" button. Call "test success" until it returns "true"; then call "received": it must return "/fake-webhook" (the server tested the saved webhook, not the unsaved one). 11. Click the "Cancel" button. Click the edited card's "Edit" button: the webhook input should have the value "". Click the "Cancel" button. 12. Call "stored": it must return "clean" (the server's channel list, and each channel's config, hold no secret).`, notificationCredentialsSetup)
}

func TestE2ENotificationCredentialsNtfyStayServerSide(t *testing.T) {
	e2e(t, useful(0), `ntfy credentials stay on the server through create, edit, replace and clear. `+notificationCredentialsHow+`Perform the steps in order. 1. Navigate to /c/{{SLUG}} and open the notifications. 2. Click the button whose name contains "+ Add Channel", then the ".provider-btn" whose text is exactly "ntfy". Fill the name input with "Secret ntfy", "#config-server" with "{{URL}}", "#config-topic" with "local-topic", "#config-token" with "fake-token", "#config-username" with "reader" and "#config-password" with "fake-password". Click the "Add Channel" button. Call "save 1" until it returns "ok"; "save 1 secrets" must return ["token","password"]. 3. "The ntfy card" is the ".model-card" containing "Secret ntfy"; click its button named exactly "Edit". "#config-token" should have the value "", the attribute type="password" and the placeholder exactly "Leave blank to keep the saved token on the server"; "#config-password" should have the value "", type="password" and the placeholder exactly "Leave blank to keep the saved password on the server". 4. "The token's clear box" is the checkbox named exactly "Clear saved credential" inside the ".form-group" that contains "#config-token"; "the password's clear box" is the same inside the ".form-group" that contains "#config-password". Click the token's clear box to check it: "#config-token" should be disabled and its placeholder attribute should be "" (empty). Click it again to uncheck it: "#config-token" should be enabled with the placeholder exactly "Leave blank to keep the saved token on the server". 5. Click the "Save" button. Call "save 2" until it returns "ok"; "save 2 sent" must return {} (neither token nor password sent). 6. Click the ntfy card's "Edit" button, fill "#config-token" with "replacement-token", and click the "Save" button. Call "save 3" until it returns "ok"; "save 3 secrets" must return ["token","password"]. 7. Click the ntfy card's "Edit" button: "#config-token" should have the value "". Fill "#config-password" with "unsaved-password", then click the button named exactly "Close modal". Open the notifications again, and click the ntfy card's "Edit" button: "#config-password" should have the value "". 8. Click the token's clear box and then the password's clear box to check both: "#config-token" and "#config-password" should both be disabled, each with the placeholder attribute "". 9. Click the "Save" button. Call "save 4" until it returns "ok"; "save 4 sent" must return {"password":"","token":""} and "save 4 secrets" must return []. 10. Call "stored": it must return "clean" (the server's channel list, and each channel's config, hold no secret).`, notificationCredentialsSetup)
}

// notificationCredentialsSetup starts the webhook receiver, records the
// channel saves and provides the calls.
func notificationCredentialsSetup(s *shelley) {
	var mu sync.Mutex
	var received []string
	var saves []notificationCredentialsSave
	testSuccess := "none"
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received = append(received, r.URL.Path)
		mu.Unlock()
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	go func() {
		<-s.ctx.Done()
		downstream.Close()
	}()
	s.v("URL", downstream.URL)
	s.v("SLUG", s.conversation("Hello", "").Slug)

	channels := func(w http.ResponseWriter, r *http.Request) {
		isTest := strings.HasSuffix(r.URL.Path, "/test")
		if r.Method != "POST" && r.Method != "PUT" {
			s.pass.ServeHTTP(w, r)
			return
		}
		sent, _ := object(readBody(r))["config"].(map[string]any)
		resp, err := s.forward(r)
		if err != nil {
			abort(w, r)
		}
		defer resp.Body.Close()
		answer, _ := io.ReadAll(resp.Body)
		mu.Lock()
		if isTest {
			testSuccess = mustJSON(object(answer)["success"])
		} else {
			saves = append(saves, notificationCredentialsSave{resp.StatusCode, sent, answer})
		}
		mu.Unlock()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(answer)
	}
	s.route("/api/notification-channels", channels)
	s.route("/api/notification-channels/", channels)

	for n := 1; n <= 4; n++ {
		save := func(f func(notificationCredentialsSave) string) func() string {
			return func() string {
				mu.Lock()
				defer mu.Unlock()
				if len(saves) < n {
					return "none"
				}
				return f(saves[n-1])
			}
		}
		s.call(fmt.Sprintf("save %d", n), save(func(sv notificationCredentialsSave) string {
			var wrong []string
			if sv.status/100 != 2 {
				wrong = append(wrong, fmt.Sprintf("status %d", sv.status))
			}
			for _, secret := range notificationCredentialsSecrets {
				if strings.Contains(string(sv.answer), secret) {
					wrong = append(wrong, "answer holds "+secret)
				}
			}
			if wrong == nil {
				return "ok"
			}
			return strings.Join(wrong, "; ")
		}))
		s.call(fmt.Sprintf("save %d sent", n), save(func(sv notificationCredentialsSave) string {
			got := map[string]any{}
			for _, key := range []string{"webhook_url", "token", "password"} {
				if v, ok := sv.sent[key]; ok {
					got[key] = v
				}
			}
			return mustJSON(got)
		}))
		s.call(fmt.Sprintf("save %d config", n), save(func(sv notificationCredentialsSave) string {
			return mustJSON(object(sv.answer)["config"])
		}))
		s.call(fmt.Sprintf("save %d secrets", n), save(func(sv notificationCredentialsSave) string {
			return mustJSON(object(sv.answer)["configured_secrets"])
		}))
	}
	s.call("test success", func() string {
		mu.Lock()
		defer mu.Unlock()
		return testSuccess
	})
	s.call("received", func() string {
		mu.Lock()
		defer mu.Unlock()
		if len(received) == 0 {
			return "none"
		}
		return received[len(received)-1]
	})
	s.call("stored", func() string {
		var wrong []string
		list := s.do("GET", "/api/notification-channels", nil)
		for _, secret := range notificationCredentialsSecrets {
			if strings.Contains(string(list), secret) {
				wrong = append(wrong, "list holds "+secret)
			}
		}
		var channels []struct {
			ID string `json:"channel_id"`
		}
		s.check(json.Unmarshal(list, &channels))
		for _, c := range channels {
			var got struct {
				Config map[string]any `json:"config"`
			}
			s.api("GET", "/api/notification-channels/"+c.ID, nil, &got)
			for _, key := range []string{"webhook_url", "token", "password"} {
				if _, ok := got.Config[key]; ok {
					wrong = append(wrong, c.ID+" config has "+key)
				}
			}
		}
		if wrong == nil {
			return "clean"
		}
		return strings.Join(wrong, "; ")
	})
}
