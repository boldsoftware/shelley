package test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
)

// Secrets taken out of chat messages (docs/secrets.md): the composer's row
// says what sending will capture, the server stores it, the message shows a
// pill, and the Secrets modal manages it.

// secretsSetup makes a conversation ({{SLUG}}) and a clipboard that records
// what it is given. Call "stored" returns the IDs of the secrets the server
// stored for the conversation, as JSON; call "copied" what was copied.
func secretsSetup(s *shelley) {
	c := s.conversation("echo: ready", "")
	s.v("SLUG", c.Slug)
	s.v("ID", c.ID)
	s.call("stored", func() string {
		var all []struct {
			ID             string `json:"id"`
			ConversationID string `json:"conversation_id"`
		}
		s.api("GET", "/api/secrets", nil, &all)
		ids := []string{}
		for _, x := range all {
			if x.ConversationID == c.ID {
				ids = append(ids, x.ID)
			}
		}
		b, err := json.Marshal(ids)
		s.check(err)
		return string(b)
	})
	var mu sync.Mutex
	copied := "none"
	s.initScript(`Object.defineProperty(navigator, "clipboard", {
  configurable: true,
  value: { writeText: (text) => fetch("/__test/clipboard", { method: "POST", body: text }) },
});`)
	s.route("POST /__test/clipboard", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		copied = string(readBody(r))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	s.call("copied", func() string {
		mu.Lock()
		defer mu.Unlock()
		return copied
	})
}

const secretsHow = `The screen is the default 393x727 phone. The conversation /c/{{SLUG}} exists. Call "stored" returns the IDs of the secrets the server has stored for it, as a JSON array. Name elements by ARIA role and accessible name, as Playwright's getByRole does (visible elements only); "named X" means an exact name match. "The composer" is [data-testid="message-input"], "the row" [data-testid="secret-bar"] and "its button" [data-testid="secret-bar-action"]. `

func TestE2ESecretsModal(t *testing.T) {
	e2e(t, useful(0), `A password sent in a chat is captured, shown as a pill, and managed in the Secrets modal. `+secretsHow+`Call "copied" returns what the page last copied ("none" before anything). "The modal" is the element with role "dialog" named "Secrets", and "the target" its "tr.secrets-target". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. Fill the composer with "connect with postgres://app:Tz8qWm3RkP5vX7@db.internal:5432/app". 3. The row should contain the text "Captured on send" (allow 10s). 4. Click the row's button whose name contains "Database password". Eval (() => { const el = document.querySelector('[data-testid="message-input"]'); return el.value.slice(el.selectionStart, el.selectionEnd); })(): it must return "Tz8qWm3RkP5vX7". 5. Click [data-testid="send-button"]. The composer should have the value exactly "". 6. Call "stored" until it returns an array of exactly one ID, which starts with "database-password-". 7. The text "connect with postgres://app:" should be visible, and ".messages-container .secret-pill" (exactly one) should contain the text "database-password-". Eval document.body.textContent.includes("Tz8qWm3RkP5vX7"): it must return false. 8. Click the pill: the button whose name starts with "Secret database-password-". The target should contain the text "database-password-", and its visible link named "{{SLUG}}" should have the attribute href exactly "/c/{{SLUG}}". Eval /in (23|24)h/.test(document.querySelector('tr.secrets-target').textContent): it must return true. 9. The modal must not contain the text "Tz8qWm3RkP5vX7". Click the target's button named "Show": the modal's ".secrets-value" should have exactly the text "Tz8qWm3RkP5vX7". Click the target's button named "Hide": the modal must not contain the text "Tz8qWm3RkP5vX7". 10. Click the target's button named "Copy". The target's button named "Copied" should be visible. Call "copied" until it returns exactly "Tz8qWm3RkP5vX7". 11. Delete asks first; Cancel and Escape keep the secret and the focus. Click the target's button named "Delete". Eval document.activeElement.textContent.trim(): it must return "Cancel". Press Escape. The target should have no button named "Cancel" (count 0), and eval document.activeElement.textContent.trim() must return "Delete". Click the target's button named "Delete", then its button named "Cancel"; eval document.activeElement.textContent.trim() must return "Delete". 12. Click the target's button named "Delete", then its [data-testid="secret-delete-confirm"]. There should be no "tr.secrets-target" (count 0). Eval !!document.activeElement?.closest('[role="dialog"]'): it must return true. The modal must not contain the text "expired or was deleted". Call "stored": it must return []. 13. Press Escape: there should be no modal (count 0). Click the pill again. Eval (() => { const id = document.querySelector('.messages-container .secret-pill').dataset.secretId; return document.querySelector('[role="dialog"]').textContent.includes(id + ' expired or was deleted.'); })() until it returns true. 14. Press Escape: there should be no modal (count 0). Press Control+k, fill ".command-palette-input" with "Secrets" and press Enter. The modal should contain the text "Shelley tries to capture secrets from chat input".`, secretsSetup)
}

func TestE2ESecretsSentAsText(t *testing.T) {
	e2e(t, useful(0), `What the user says holds no secret is sent as typed, until something else is found. `+secretsHow+`Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. Fill the composer with "example: postgres://demo:Qv7nLx2WpR9kT4@localhost/demo". 3. The row should contain the text "Captured on send" (allow 10s), and its button should have exactly the text "Don't capture". 4. Click its button: the row should contain the text "Sent as text". Click its button: the row should contain "Captured on send". Click its button again: it should have exactly the text "Capture". 5. A new finding is captured with the rest, until it is gone again. Fill the composer with "example: postgres://demo:Qv7nLx2WpR9kT4@localhost/demo and postgres://demo:Hd4sYt8MzJ3cW6@localhost/demo": the row should contain "Captured on send". Fill the composer with "example: postgres://demo:Qv7nLx2WpR9kT4@localhost/demo": the row should contain "Sent as text". 6. Click [data-testid="send-button"]. The composer should have the value exactly "". 7. The first ".messages-container .message-user" containing "Qv7nLx2WpR9kT4" should be visible, and there should be no ".messages-container .secret-pill" (count 0). 8. Call "stored": it must return [].`, secretsSetup)
}

func TestE2ESecretsStayOutOfDrafts(t *testing.T) {
	e2e(t, useful(1), `Composer drafts don't keep secrets that weren't typed. `+secretsHow+`"The drafts" means the result of eval Object.keys(localStorage).filter((k) => k.startsWith("shelley-draft:")).map((k) => localStorage.getItem(k)).join(" "). "Paste X" means eval (() => { const el = document.querySelector('[data-testid="message-input"]'); el.focus(); el.value += X; el.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertFromPaste" })); })(). "Delete X" means the same with el.value = el.value.replace(X, "") and inputType "deleteContentBackward". Call "checks" returns how many of the composer's secret checks the server has answered since call "reset checks". Perform the steps in order. 1. Navigate to /new. Fill the composer with "first draft typed". The URL should come to contain "/c/" (allow 10s), as the draft is saved; then the composer should have the value exactly "first draft typed", and the drafts should contain "first draft typed" (allow 10s). Navigate to /c/{{SLUG}}. 2. Fill the composer with "note: ". The drafts should contain "note: " (allow 10s). 3. Paste "postgres://app:Tz8qWm3RkP5vX7@db.internal:5432/app". The row should contain the text "Captured on send" (allow 10s). The drafts must not contain "Tz8qWm3RkP5vX7". 4. Call "reset checks". Delete "7". Call "checks" until it returns "1". The drafts must not contain "Tz8qWm3RkP5vX". 5. Call "reset checks". Delete "postgres://app:Tz8qWm3RkP5vX@db.internal:5432/app": the composer should have the value exactly "note: ". Call "checks" until it returns "1". Fill the composer with "note: done". The drafts should contain "note: done" (allow 10s), and must not contain "Tz8qWm3RkP5vX". 6. Fill the composer with "delay: 60", click [data-testid="send-button"], and [data-testid="agent-thinking"] should be visible (allow 30s). 7. Paste "token postgres://app:Hd4sYt8MzJ3cW6@db.internal/app". The row should contain "Captured on send" (allow 10s). Call "fail queue". Call "reset checks". 8. Click [data-testid="send-options-button"], then [data-testid="queue-option"] once visible. Call "queue failed" until it returns "true". 9. The composer should have the value exactly "token postgres://app:Hd4sYt8MzJ3cW6@db.internal/app" (allow 10s), and call "checks" should return "1" (allow 10s). The drafts must not contain "Hd4sYt8MzJ3cW6".`, func(s *shelley) {
		secretsSetup(s)
		checks := s.counter("checks")
		s.call("reset checks", func() string { checks.Store(0); return "0" })
		s.route("POST /api/secrets/detect", func(w http.ResponseWriter, r *http.Request) {
			if queueRelay(s, w, r) == http.StatusOK {
				checks.Add(1)
			}
		})
		var fail atomic.Bool
		s.call("fail queue", func() string { fail.Store(true); return "ok" })
		failed := s.signal("queue failed")
		s.route("POST /api/conversation/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
			if fail.Load() && bytes.Contains(readBody(r), []byte(`"queue":true`)) {
				failed.Store(true)
				http.Error(w, "queue refused", http.StatusInternalServerError)
				return
			}
			queueRelay(s, w, r)
		})
	})
}

func TestE2ESecretsHeldTextStaysInTab(t *testing.T) {
	e2e(t, useful(1), `Composer text that may hold a secret, so isn't saved as a draft, stays in the tab. `+secretsHow+`The conversation {{OTHER}} exists too, and {{SLUG}}'s id is {{ID}}. "The drafts" means the result of eval Object.keys(localStorage).filter((k) => k.startsWith("shelley-draft:")).map((k) => localStorage.getItem(k)).join(" "). "Paste X" means eval (() => { const el = document.querySelector('[data-testid="message-input"]'); el.focus(); el.value += X; el.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertFromPaste" })); })(). To "open conversation X": if the button with aria-label "Open conversations" is visible, click it; then wait for .conversation-item[data-conversation-id="X"] to be visible, click it, and wait until it has the class "active". After call "hold sends", messages sent to a conversation wait for call "release sends"; after call "hold checks", the composer's secret checks wait for call "release checks". {{CHAT}} is Shelley's new conversation page with a chat from the bookmarklet whose selection holds a database URL with the password "Hd4sYt8MzJ3cW6". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. Fill the composer with "note: ". The drafts should contain "note: " (allow 10s). 2. Paste "postgres://app:Tz8qWm3RkP5vX7@db.internal:5432/app". The row should contain the text "Captured on send" (allow 10s). Click the composer, press End and type " later". 3. Open conversation {{OTHER}}. The composer should have the value exactly "" (allow 10s). 4. Open conversation {{ID}}. The composer should have the value exactly "note: postgres://app:Tz8qWm3RkP5vX7@db.internal:5432/app later" (allow 10s). 5. Click the visible button with class "btn-new" (named "New Conversation"). The URL should come to end with "/new" (allow 10s). Open conversation {{ID}}. The composer should have the value exactly "note: postgres://app:Tz8qWm3RkP5vX7@db.internal:5432/app later" (allow 10s). The drafts must not contain "Tz8qWm3RkP5vX7". 6. The row should contain "Captured on send" (allow 10s). Click its button; the row should contain "Sent as text". Click the composer, press End and type " thanks". Wait longer than the composer takes to save a draft: eval new Promise((resolve) => setTimeout(() => resolve('waited'), 1500)) expecting "waited". The drafts must not contain "Tz8qWm3RkP5vX7". Click its button; the row should contain "Captured on send". 7. Fill the composer with "". The drafts must come to not contain "note: " (allow 10s). Paste "postgres://app:Zx9cVb2NmQ4wE6@db.internal/app". The row should contain "Captured on send" (allow 10s). Click the composer, press End and type " sent-while-away". Call "hold sends". Click [data-testid="send-button"]. Open conversation {{OTHER}}. Call "release sends". Open conversation {{ID}}. The page should contain the text "sent-while-away" (allow 30s), and then the composer must have the value exactly "". 8. Call "hold checks". Navigate to {{CHAT}}. The composer should contain "Hd4sYt8MzJ3cW6" (allow 10s). Click the composer, press End and type " x". Wait longer than the composer takes to save a draft: eval new Promise((resolve) => setTimeout(() => resolve('waited'), 1500)) expecting "waited". The drafts must not contain "Hd4sYt8MzJ3cW6". 9. Call "release checks". The row should contain "Captured on send" (allow 10s). The drafts must not contain "Hd4sYt8MzJ3cW6".`, func(s *shelley) {
		secretsSetup(s)
		s.v("OTHER", s.conversation("echo: other", "").ID)
		s.v("CHAT", "/new#bookmarklet="+encodeURIComponent(`{"url":"http://host.test/","title":"Host Page","selection":"db postgres://app:Hd4sYt8MzJ3cW6@db.internal/app"}`))
		gated(s, "POST /api/conversation/{id}/chat", "sends")
		gated(s, "POST /api/secrets/detect", "checks")
	})
}

// gated relays requests to route, holding them from call "hold <name>" until
// call "release <name>".
func gated(s *shelley, route, name string) {
	var mu sync.Mutex
	var open chan struct{}
	s.call("hold "+name, func() string {
		mu.Lock()
		defer mu.Unlock()
		open = make(chan struct{})
		return "held"
	})
	s.call("release "+name, func() string {
		mu.Lock()
		defer mu.Unlock()
		if open != nil {
			close(open)
			open = nil
		}
		return "released"
	})
	s.route(route, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		held := open
		mu.Unlock()
		if held != nil {
			select {
			case <-held:
			case <-r.Context().Done():
				return
			}
		}
		queueRelay(s, w, r)
	})
}
