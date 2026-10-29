package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/ui"
)

// A page knows its UI build from its init data, and the server's from the
// first frame of each stream connection; see globalStream.ts.
func TestPageAndStreamCarryUIBuild(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)
	build := ui.BuildID()
	if build == "" {
		t.Fatal("no UI build; run make ui")
	}
	if got := indexInitData(t, srv)["ui_build"]; got != build {
		t.Fatalf("init data ui_build = %#v, want %q", got, build)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream2", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		srv.handleStream(rec, req)
		close(done)
	}()
	timeout := time.After(streamWaitTimeout)
	for !strings.Contains(rec.getString(), `"ui_build":"`+build+`"`) {
		select {
		case <-rec.flushed:
		case <-timeout:
			t.Fatalf("no ui_build frame; body=%s", rec.getString())
		}
	}
	cancel()
	<-done
}
