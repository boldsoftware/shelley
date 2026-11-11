package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The store takes pushes only with the proxy's identity and an attested
// source VM, and browsing only with the identity.
func TestArchiveStoreAuth(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)
	h := s.archiveStoreHandler()
	push := `{"conversation":{"conversation_id":"c1"},"after_seq":0,"through_seq":0,"messages":[]}`
	for _, tc := range []struct {
		method, path, body string
		headers            []string
		want               int
	}{
		{"GET", "/archive-store/api/conversations", "", nil, 401},
		{"GET", "/archive-store/api/conversations", "", []string{"X-ExeDev-Email", "b@x", "X-ExeDev-UserID", "u"}, 200},
		{"POST", "/archive-store/v1/conversations/c1", push, []string{"X-ExeDev-Email", "b@x", "X-ExeDev-UserID", "u"}, 403},
		{"POST", "/archive-store/v1/conversations/c1", push, []string{"X-Exedev-Source-Vm", "vm"}, 401},
		{"POST", "/archive-store/v1/conversations/c1", push, []string{"X-ExeDev-Email", "b@x", "X-ExeDev-UserID", "u", "X-Exedev-Source-Vm", "vm"}, 200},
		{"POST", "/archive-store/v1/conversations/c2", push, []string{"X-ExeDev-Email", "b@x", "X-ExeDev-UserID", "u", "X-Exedev-Source-Vm", "vm"}, 400},
		{"GET", "/", "", []string{"X-ExeDev-Email", "b@x", "X-ExeDev-UserID", "u"}, 302},
		{"GET", "/new", "", []string{"X-ExeDev-Email", "b@x", "X-ExeDev-UserID", "u"}, 404},
		{"GET", "/api/conversations", "", []string{"X-ExeDev-Email", "b@x", "X-ExeDev-UserID", "u"}, 404},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		for i := 0; i < len(tc.headers); i += 2 {
			r.Header.Set(tc.headers[i], tc.headers[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s %s %v: %d, want %d: %s", tc.method, tc.path, tc.headers, w.Code, tc.want, w.Body)
		}
	}
}
