package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveUIWorld(t *testing.T) {
	srv, database, _ := newTestServer(t)
	ctx := context.Background()

	req := func(target, header string) *http.Request {
		r := httptest.NewRequest("GET", target, nil)
		if header != "" {
			r.Header.Set(uiWorldHeader, header)
		}
		return r
	}

	// Default (no override): the vue-ui flag defaults to false -> react.
	if got := srv.resolveUIWorld(req("/", "")); got != UIWorldReact {
		t.Fatalf("default world = %q, want react", got)
	}

	// DB override flips the default to vue.
	if err := database.SetFeatureFlagOverride(ctx, FlagVueUI.Name, "true"); err != nil {
		t.Fatal(err)
	}
	if got := srv.resolveUIWorld(req("/", "")); got != UIWorldVue {
		t.Fatalf("after override world = %q, want vue", got)
	}

	// Query param wins over the DB override.
	if got := srv.resolveUIWorld(req("/?__ui=react", "")); got != UIWorldReact {
		t.Fatalf("query world = %q, want react", got)
	}

	// Header wins over the DB override (but query wins over header).
	if got := srv.resolveUIWorld(req("/", "react")); got != UIWorldReact {
		t.Fatalf("header world = %q, want react", got)
	}
	if got := srv.resolveUIWorld(req("/?__ui=vue", "react")); got != UIWorldVue {
		t.Fatalf("query-over-header world = %q, want vue", got)
	}

	// Clearing the override returns to the default (react).
	if err := database.DeleteFeatureFlagOverride(ctx, FlagVueUI.Name); err != nil {
		t.Fatal(err)
	}
	if got := srv.resolveUIWorld(req("/", "")); got != UIWorldReact {
		t.Fatalf("after clear world = %q, want react", got)
	}
}

func TestUIAssetsHTML(t *testing.T) {
	vue := uiAssetsHTML(UIWorldVue)
	if !strings.Contains(vue, "/main.vue.js") || !strings.Contains(vue, "/main.vue.css") {
		t.Fatalf("vue assets missing vue bundle: %q", vue)
	}
	if strings.Contains(vue, "main.react") {
		t.Fatalf("vue assets leaked react bundle: %q", vue)
	}
	react := uiAssetsHTML(UIWorldReact)
	if !strings.Contains(react, "/main.react.js") || !strings.Contains(react, "/main.react.css") {
		t.Fatalf("react assets missing react bundle: %q", react)
	}
}
