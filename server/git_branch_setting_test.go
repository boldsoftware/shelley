package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitBranchSetting(t *testing.T) {
	srv, _, _ := newTestServer(t)
	if got := indexInitData(t, srv)["show_git_branch"]; got != false {
		t.Fatalf("show_git_branch = %v, want false", got)
	}
	for _, enabled := range []bool{true, false} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/settings",
			strings.NewReader(fmt.Sprintf(`{"key":"show_git_branch","value":"%t"}`, enabled)))
		srv.handleSetSetting(w, req)
		if w.Code != 200 {
			t.Fatalf("POST /settings: %d: %s", w.Code, w.Body.String())
		}
		if got := indexInitData(t, srv)["show_git_branch"]; got != enabled {
			t.Fatalf("show_git_branch = %v, want %v", got, enabled)
		}
	}
}
