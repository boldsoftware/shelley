package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/featureflags"
)

var _ = featureflags.Register(featureflags.Flag{
	Name:        "test-handlers-flag",
	Description: "test flag",
	Default:     false,
})

func TestFeatureFlagsHandlers(t *testing.T) {
	srv, database, _ := newTestServer(t)
	ctx := t.Context()

	// Seed stale rows, including overrides for removed flags: none may leak.
	for name, value := range map[string]string{
		"stale-unknown":            `42`,
		"tool-pills":               `true`,
		"reflection-emoji-favicon": `false`,
		"patch-simple":             `true`,
		"bookmarklet":              `true`,
	} {
		if err := database.SetFeatureFlagOverride(ctx, name, value); err != nil {
			t.Fatal(err)
		}
	}

	// GET
	w := httptest.NewRecorder()
	srv.handleGetFeatureFlags(w, httptest.NewRequest("GET", "/feature-flags", nil))
	if w.Code != 200 {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	var list []FeatureFlagDTO
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var found *FeatureFlagDTO
	for i := range list {
		switch list[i].Name {
		case "stale-unknown", "tool-pills", "reflection-emoji-favicon", "patch-simple", "bookmarklet":
			t.Fatalf("removed or unknown flag %q leaked into response", list[i].Name)
		case "test-handlers-flag":
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatal("registered flag missing")
	}
	if found.Override != nil {
		t.Fatalf("unexpected override: %s", *found.Override)
	}

	// POST: set override.
	w = httptest.NewRecorder()
	srv.handleSetFeatureFlag(w, httptest.NewRequest("POST", "/feature-flags",
		strings.NewReader(`{"name":"test-handlers-flag","value":true}`)))
	if w.Code != 200 {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}

	// POST: unknown and removed flags are rejected.
	for _, name := range []string{"definitely-not-registered", "tool-pills", "reflection-emoji-favicon", "patch-simple", "bookmarklet"} {
		w = httptest.NewRecorder()
		body := fmt.Sprintf(`{"name":%q,"value":true}`, name)
		srv.handleSetFeatureFlag(w, httptest.NewRequest("POST", "/feature-flags", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("POST %q: want 400, got %d %s", name, w.Code, w.Body.String())
		}
	}

	// GET again, override present.
	w = httptest.NewRecorder()
	srv.handleGetFeatureFlags(w, httptest.NewRequest("GET", "/feature-flags", nil))
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	found = nil
	for i := range list {
		if list[i].Name == "test-handlers-flag" {
			found = &list[i]
		}
	}
	if found == nil || found.Override == nil || string(*found.Override) != "true" {
		t.Fatalf("override not set: %+v", found)
	}

	// DELETE
	w = httptest.NewRecorder()
	srv.handleDeleteFeatureFlag(w, httptest.NewRequest("DELETE", "/feature-flags",
		strings.NewReader(`{"name":"test-handlers-flag"}`)))
	if w.Code != 200 {
		t.Fatalf("DELETE: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	srv.handleGetFeatureFlags(w, httptest.NewRequest("GET", "/feature-flags", nil))
	list = nil
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	for _, f := range list {
		if f.Name == "test-handlers-flag" && f.Override != nil {
			t.Fatalf("override still present after delete: %s", *f.Override)
		}
	}
}
