package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Pages learn whether the bookmarklet flag is on.
func TestBookmarkletFlagInInitData(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)
	if on := indexInitData(t, s)["bookmarklet"]; on != false {
		t.Fatalf("flag off: bookmarklet = %v", on)
	}
	w := httptest.NewRecorder()
	s.handleSetFeatureFlag(w, httptest.NewRequest("POST", "/feature-flags", strings.NewReader(`{"name":"bookmarklet","value":true}`)))
	if w.Code != 200 {
		t.Fatalf("set flag: %d %s", w.Code, w.Body.String())
	}
	if on := indexInitData(t, s)["bookmarklet"]; on != true {
		t.Fatalf("flag on: bookmarklet = %v", on)
	}
}
