package test

import (
	"net/http"
	"testing"
)

// deepLinkStreamJS counts, in window.__listPatches, the conversation list
// patches the page's event streams deliver.
const deepLinkStreamJS = `window.__listPatches = 0;
const NativeEventSource = window.EventSource;
window.EventSource = class extends NativeEventSource {
  constructor(...args) {
    super(...args);
    this.addEventListener('message', (event) => {
      try {
        if (JSON.parse(event.data).conversation_list_patch) window.__listPatches++;
      } catch {}
    });
  }
};`

// A link to a conversation opens it, even when the stream delivers the
// conversation list before the page's own first load has resolved the
// link: the app used to show a new conversation's composer meanwhile, and
// text typed there went to a stray draft while the linked conversation's
// Send stayed disabled.
func TestE2EDeepLinkWaitsForTheLinkedConversation(t *testing.T) {
	e2e(t, `A link to a conversation keeps the page loading until that conversation shows, even when the live stream delivers the conversation list first. The conversation at /c/deep-link exists; the server holds the page's first load of the conversation list (GET /api/conversations/snapshot) until the call "release". Calls: "snapshot requested" returns "true" once that request has arrived ("false" before). window.__listPatches counts the conversation list updates the page's live stream has delivered. "The message input" is [data-testid="message-input"]; "the loading screen" is ".loading-container". Perform the steps in order. 1. Navigate to /c/deep-link. 2. Call "snapshot requested" until it returns "true". 3. Eval window.__listPatches > 0 until it returns true: the stream has delivered the list while the first load is held. 4. Two frames later the page must still be loading, with no composer: eval new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => r(!!document.querySelector('.loading-container') + ' ' + !!document.querySelector('[data-testid="message-input"]'))))) with expect "true false". 5. Call "release". 6. The message input should become visible, and the URL must be exactly /c/deep-link. 7. Fill the message input with "Follow-up": the button [data-testid="send-button"] should be enabled.`, func(s *shelley) {
		s.initScript(deepLinkStreamJS)
		s.conversation("Hello", "deep-link")
		requested := s.signal("snapshot requested")
		release := s.gate("release")
		s.route("GET /api/conversations/snapshot", func(w http.ResponseWriter, r *http.Request) {
			requested.Store(true)
			if release.wait(r) {
				s.pass.ServeHTTP(w, r)
			}
		})
	})
}
