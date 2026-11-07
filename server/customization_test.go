package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

// customizationClient drives the in-memory build states over HTTP, as
// install-custom and the UI do.
type customizationClient struct {
	t   *testing.T
	mux *http.ServeMux
}

func (c customizationClient) do(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("X-Shelley-Request", "1")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c.mux.ServeHTTP(w, req)
	return w
}

func (c customizationClient) report(id, status, commit, errText string) {
	c.t.Helper()
	w := c.do(http.MethodPost, "/api/conversation/"+id+"/customization", map[string]string{"status": status, "commit": commit, "error": errText})
	if w.Code != http.StatusOK {
		c.t.Fatalf("report %s = %d: %s", status, w.Code, w.Body)
	}
}

func (c customizationClient) list() map[string]customization {
	c.t.Helper()
	w := c.do(http.MethodGet, "/api/customizations", nil)
	if w.Code != http.StatusOK {
		c.t.Fatalf("GET /api/customizations = %d: %s", w.Code, w.Body)
	}
	var list []customization
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		c.t.Fatal(err)
	}
	byID := map[string]customization{}
	for _, entry := range list {
		byID[entry.ConversationID] = entry
	}
	return byID
}

func newCustomizationTest(t *testing.T, pending bool) (*Server, customizationClient, chan int) {
	server, _, _ := newTestServer(t)
	exited := make(chan int, 1)
	server.exitDelay = 0
	server.exitProcess = func(code int) { exited <- code }
	server.restartPendingNow = func() bool { return pending }
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	return server, customizationClient{t: t, mux: mux}, exited
}

func TestCustomizationStates(t *testing.T) {
	server, c, _ := newCustomizationTest(t, true)
	c.report("a", "verifying", "aaa", "")
	c.report("b", "failed", "bbb", "the lint step failed")
	if got := c.list(); got["a"].Status != "verifying" || got["b"].Status != "failed" || got["b"].Error != "the lint step failed" {
		t.Fatalf("states = %+v", got)
	}

	// Installing auto-restarts once the user is idle.
	c.report("a", "installed", "aaa", "")
	server.noteUserActivity()
	got := c.list()["a"]
	if got.Status != "installed" || got.AutoRestart != "pending" || got.AutoRestartInMS == nil || *got.AutoRestartInMS <= 0 {
		t.Fatalf("installed = %+v", got)
	}

	// A newer install replaces the binary: the older one is gone.
	c.report("b", "installed", "bbb", "")
	if got := c.list(); len(got) != 1 || got["b"].Status != "installed" {
		t.Fatalf("after b installed = %+v", got)
	}

	if w := c.do(http.MethodPost, "/api/conversation/a/customization", map[string]string{"status": "bogus"}); w.Code != http.StatusBadRequest {
		t.Errorf("bogus status = %d", w.Code)
	}
}

func TestCustomizationCancelAutoRestart(t *testing.T) {
	server, c, _ := newCustomizationTest(t, true)
	c.report("a", "installed", "aaa", "")
	server.lastUserActivity.Store(time.Now().Add(-idleRestartAfter).UnixNano())
	if w := c.do(http.MethodPost, "/api/conversation/a/customization/auto-restart/cancel", nil); w.Code != http.StatusOK {
		t.Fatalf("cancel = %d: %s", w.Code, w.Body)
	}
	if got := c.list()["a"]; got.AutoRestart != "" {
		t.Errorf("auto_restart = %q after cancel", got.AutoRestart)
	}
	if server.idleRestartTick(context.Background()) {
		t.Fatal("restarted into a build whose auto-restart was cancelled")
	}
	// A new install plans a restart again.
	c.report("a", "installed", "aab", "")
	if !server.idleRestartTick(context.Background()) {
		t.Fatal("no idle restart after a new install")
	}
}

func TestCustomizationRestart(t *testing.T) {
	server, c, exited := newCustomizationTest(t, true)
	c.report("a", "installed", "aaa", "")
	if w := c.do(http.MethodPost, "/api/conversation/a/customization/restart", map[string]string{"commit": "old"}); w.Code != http.StatusConflict {
		t.Fatalf("restart into another commit = %d", w.Code)
	}
	w := c.do(http.MethodPost, "/api/conversation/a/customization/restart", map[string]string{"commit": "aaa"})
	if w.Code != http.StatusOK {
		t.Fatalf("restart = %d: %s", w.Code, w.Body)
	}
	var got customization
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Status != "restarting" {
		t.Errorf("status = %q, want restarting", got.Status)
	}
	if resume, _ := server.db.GetSetting(context.Background(), db.ResumeAfterUpgradeSettingKey); resume != "1" {
		t.Errorf("resume marker = %q", resume)
	}
	select {
	case code := <-exited:
		if code != restartExitCode {
			t.Errorf("exit code = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("exit was not scheduled")
	}
}

func TestCustomizationStream(t *testing.T) {
	server, c, _ := newCustomizationTest(t, true)
	ctx, cancel := context.WithCancel(t.Context())
	next := server.streamPub.Subscribe(ctx, -1)
	c.report("a", "verifying", "aaa", "")
	c.report("a", "installed", "aaa", "")
	server.publishCustomizations() // Unchanged: sends nothing.
	server.noteUserActivity()
	server.publishCustomizations() // The deadline moved.
	cancel()
	var got []string
	for event, ok := next(); ok; event, ok = next() {
		if event.Customizations == nil {
			continue
		}
		list := *event.Customizations
		if len(list) != 1 {
			t.Fatalf("customizations = %+v", list)
		}
		got = append(got, list[0].Status+"/"+list[0].AutoRestart)
	}
	if want := []string{"verifying/", "installed/pending", "installed/pending"}; !slices.Equal(got, want) {
		t.Errorf("stream sent %v, want %v", got, want)
	}
}
