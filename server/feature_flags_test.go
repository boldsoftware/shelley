package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/featureflags"
)

func TestFeatureFlagBool(t *testing.T) {
	flag := featureflags.Register(featureflags.Flag{
		Name:        "test-feature-flag-bool",
		Description: "test flag",
		Default:     false,
	})

	srv, database, _ := newTestServer(t)
	ctx := context.Background()

	// No override: the registered default applies.
	if got := srv.featureFlagBool(ctx, flag); got != false {
		t.Fatalf("featureFlagBool default = %v, want false", got)
	}

	// Override to true.
	if err := database.SetFeatureFlagOverride(ctx, flag.Name, `true`); err != nil {
		t.Fatal(err)
	}
	if got := srv.featureFlagBool(ctx, flag); got != true {
		t.Fatalf("featureFlagBool with true override = %v, want true", got)
	}

	// A non-boolean override falls back to the default.
	if err := database.SetFeatureFlagOverride(ctx, flag.Name, `"nope"`); err != nil {
		t.Fatal(err)
	}
	if got := srv.featureFlagBool(ctx, flag); got != false {
		t.Fatalf("featureFlagBool with bad override = %v, want false", got)
	}
}

func TestFeatureFlagsHandlers(t *testing.T) {
	// Register a unique flag for this test. Registry is process-global so a
	// distinctive name avoids collisions with future real flags.
	flag := featureflags.Register(featureflags.Flag{
		Name:        "test-handlers-flag",
		Description: "test flag",
		Default:     false,
	})
	_ = flag

	srv, database, _ := newTestServer(t)
	ctx := context.Background()

	// Seed a stale row that's no longer registered: must be ignored on read.
	if err := database.SetFeatureFlagOverride(ctx, "stale-unknown", `42`); err != nil {
		t.Fatal(err)
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
		if list[i].Name == "stale-unknown" {
			t.Fatal("unknown flag leaked into response")
		}
		if list[i].Name == "test-handlers-flag" {
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

	// POST: unknown flag rejected.
	w = httptest.NewRecorder()
	srv.handleSetFeatureFlag(w, httptest.NewRequest("POST", "/feature-flags",
		strings.NewReader(`{"name":"definitely-not-registered","value":true}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for unknown flag, got %d %s", w.Code, w.Body.String())
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
