package test

import (
	"net/http"
	"testing"
)

// Ported from ui/e2e/auth-disconnect.spec.ts.

// authDisconnectStream replaces EventSource with a fake the test can fail, as
// a dropped connection would; window.__testStream is the last one opened.
const authDisconnectStream = `(() => {
  class TestEventSource {
    constructor(url) {
      this.readyState = TestEventSource.CONNECTING;
      this.onopen = null;
      this.onmessage = null;
      this.onerror = null;
      window.__testStream = this;
      queueMicrotask(() => {
        this.readyState = TestEventSource.OPEN;
        this.onopen?.();
      });
    }
    close() {
      this.readyState = TestEventSource.CLOSED;
    }
  }
  TestEventSource.CONNECTING = 0;
  TestEventSource.OPEN = 1;
  TestEventSource.CLOSED = 2;
  Object.defineProperty(window, "EventSource", { configurable: true, value: TestEventSource });
})();`

func TestE2EAuthDisconnectReloadsIntoAuthFlow(t *testing.T) {
	e2e(t, `An expired exe.dev login reloads the page into the auth flow, keeping its place. The page's EventSource is a fake (the page already defines it): window.__testStream is the stream the app opened, and the test fails it as a dropped connection would. The server answers GET /api/upload/raw, the app's login probe, with a 307 redirect to /__exe.dev/login?redirect=%2Fnew, as exe.dev does once the login has expired. Perform the steps in order. 1. Navigate to /new#keep-my-place; the element with data-testid "message-input" should be visible. 2. Fail the stream with one eval: (() => { const s = window.__testStream; if (!s || !s.onerror) throw new Error("global stream was not initialized"); s.readyState = 0; s.onerror(); return "failed"; })() which must return "failed". 3. The page must reload: performance.getEntriesByType("navigation")[0]?.type should become exactly "reload". 4. location.hash must be exactly "#keep-my-place".`, func(s *shelley) {
		s.initScript(authDisconnectStream)
		s.route("GET /api/upload/raw", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "/__exe.dev/login?redirect=%2Fnew")
			w.WriteHeader(http.StatusTemporaryRedirect)
		})
	})
}
