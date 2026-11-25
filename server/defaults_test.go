package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/llm/predictable"
)

func TestDefaults(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	srv := NewServer(database, &twoModelLLMManager{service: predictable.NewService()}, claudetool.ToolSetConfig{},
		slog.New(slog.DiscardHandler), false, "model-a", "")
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(srv.tcpHandler(mux))
	t.Cleanup(ts.Close)

	do := func(method, path, body string, want int) string {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s: status = %d, want %d: %s", method, path, resp.StatusCode, want, b)
		}
		return strings.TrimSpace(string(b))
	}
	entry := func(s string) defaultEntry {
		t.Helper()
		var e defaultEntry
		if err := json.Unmarshal([]byte(s), &e); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		return e
	}

	if got := do("GET", "/api/defaults", "", 200); got != `{"defaults":[]}` {
		t.Fatalf("empty list: %s", got)
	}
	do("GET", "/api/defaults/app.views.a", "", 404)

	// Made at version 1, stored as compact JSON.
	a1 := entry(do("POST", "/api/defaults/app.views.a", `{"value": {"name": "A", "n": [1, 2]}}`, 200))
	if a1.Key != "app.views.a" || string(a1.Value) != `{"name":"A","n":[1,2]}` || a1.Version != 1 || a1.CreatedAt.IsZero() || !a1.UpdatedAt.Equal(a1.CreatedAt) {
		t.Fatalf("made: %+v", a1)
	}
	if got := entry(do("GET", "/api/defaults/app.views.a", "", 200)); got.Version != 1 || string(got.Value) != string(a1.Value) || !got.UpdatedAt.Equal(a1.UpdatedAt) {
		t.Fatalf("read back: %+v, wrote %+v", got, a1)
	}
	// The same value again is no new version.
	if got := entry(do("POST", "/api/defaults/app.views.a", `{"value":{"name":"A","n":[1,2]}}`, 200)); got.Version != 1 || !got.UpdatedAt.Equal(a1.UpdatedAt) {
		t.Fatalf("unchanged: %+v", got)
	}
	// A change is, with a later updated_at, even within the millisecond.
	a2 := entry(do("POST", "/api/defaults/app.views.a", `{"value":"two"}`, 200))
	if a2.Version != 2 || string(a2.Value) != `"two"` || !a2.UpdatedAt.After(a1.UpdatedAt) || !a2.CreatedAt.Equal(a1.CreatedAt) {
		t.Fatalf("changed: %+v after %+v", a2, a1)
	}

	// Compare-and-swap: a stale version, or 0 for a key that exists, is
	// refused; the current one writes.
	do("POST", "/api/defaults/app.views.a", `{"value":3,"version":1}`, 409)
	do("POST", "/api/defaults/app.views.a", `{"value":3,"version":0}`, 409)
	if got := entry(do("POST", "/api/defaults/app.views.a", `{"value":3,"version":2}`, 200)); got.Version != 3 {
		t.Fatalf("swapped: %+v", got)
	}
	do("POST", "/api/defaults/app.views.b", `{"value":true,"version":5}`, 409)
	do("POST", "/api/defaults/app.views.b", `{"value":true,"version":0}`, 200)
	do("POST", "/api/defaults/other", `{"value":{}}`, 200)

	// Listing, by key, all or by prefix (which is literal: '_' and '%'
	// match only themselves).
	var list struct{ Defaults []defaultEntry }
	if err := json.Unmarshal([]byte(do("GET", "/api/defaults?prefix=app.views.", "", 200)), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Defaults) != 2 || list.Defaults[0].Key != "app.views.a" || string(list.Defaults[0].Value) != "3" || list.Defaults[1].Key != "app.views.b" {
		t.Fatalf("prefix list: %+v", list)
	}
	if got := do("GET", "/api/defaults?prefix=app_", "", 200); got != `{"defaults":[]}` {
		t.Fatalf("prefix app_: %s", got)
	}
	if got := do("GET", "/api/defaults?prefix=App", "", 200); got != `{"defaults":[]}` {
		t.Fatalf("prefix App: %s", got)
	}
	if err := json.Unmarshal([]byte(do("GET", "/api/defaults", "", 200)), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Defaults) != 3 || list.Defaults[2].Key != "other" {
		t.Fatalf("list: %+v", list)
	}

	// Bad keys, values and bodies.
	do("POST", "/api/defaults/.hidden", `{"value":1}`, 400)
	do("POST", "/api/defaults/a%20b", `{"value":1}`, 400)
	do("GET", "/api/defaults/"+strings.Repeat("k", 129), "", 400)
	do("POST", "/api/defaults/k", `{}`, 400)
	do("POST", "/api/defaults/k", `{"value":null}`, 400)
	do("POST", "/api/defaults/k", `{"value":`, 400)
	do("POST", "/api/defaults/k", `{"value":1} {"value":2}`, 400)
	do("POST", "/api/defaults/k", `{"value":"`+strings.Repeat("x", maxDefaultValue)+`"}`, 413)
	do("GET", "/api/defaults/k", "", 404)

	// Deleting, with or without a version.
	do("DELETE", "/api/defaults/app.views.a?version=2", "", 409)
	do("DELETE", "/api/defaults/app.views.a?version=x", "", 400)
	do("DELETE", "/api/defaults/app.views.a?version=3", "", 204)
	do("DELETE", "/api/defaults/app.views.b", "", 204)
	do("DELETE", "/api/defaults/app.views.b", "", 404)
	do("GET", "/api/defaults/app.views.a", "", 404)
	if err := json.Unmarshal([]byte(do("GET", "/api/defaults?prefix=app.", "", 200)), &list); err != nil || len(list.Defaults) != 0 {
		t.Fatalf("list after deleting: %+v %v", list, err)
	}
	// A key made again goes on from the deleted one's version, so what
	// was read before is still stale.
	do("POST", "/api/defaults/app.views.a", `{"value":1,"version":3}`, 409)
	a4 := entry(do("POST", "/api/defaults/app.views.a", `{"value":1,"version":0}`, 200))
	if a4.Version != 5 || !a4.CreatedAt.After(a1.CreatedAt) {
		t.Fatalf("made again: %+v", a4)
	}
	do("DELETE", "/api/defaults/app.views.b?version=1", "", 409)
	do("DELETE", "/api/defaults/app.views.b?version=0", "", 404)
	if got := entry(do("POST", "/api/defaults/app.views.b", `{"value":1}`, 200)); got.Version != 3 {
		t.Fatalf("b made again: %+v", got)
	}
	do("DELETE", "/api/defaults/app.views.b?version=1", "", 409)
}
