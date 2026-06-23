package server

import (
	"context"
	"net/http"
	"os"
	"strings"
)

// UIWorld identifies which frontend bundle the server serves for a request.
type UIWorld string

const (
	// UIWorldVue is the Vue 3 + PrimeVue frontend (main.vue.js/css).
	UIWorldVue UIWorld = "vue"
	// UIWorldReact is the legacy React frontend (main.react.js/css).
	UIWorldReact UIWorld = "react"
)

// uiWorldHeader / uiWorldQuery let a single request force a frontend regardless
// of the stored flag. The e2e and lazycue suites use these to run every spec in
// both worlds against one server, without mutating shared DB state (which would
// race across parallel workers).
const (
	uiWorldHeader = "X-Shelley-UI"
	uiWorldQuery  = "__ui"
)

// parseUIWorld maps a free-form string to a UIWorld, or "" if unrecognized.
func parseUIWorld(s string) UIWorld {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "vue":
		return UIWorldVue
	case "react":
		return UIWorldReact
	default:
		return ""
	}
}

// resolveUIWorld decides which frontend to serve for an index.html request.
// Precedence (highest first):
//  1. ?__ui=vue|react query param
//  2. X-Shelley-UI: vue|react request header
//  3. the `vue-ui` feature flag (DB override, else registry default)
//  4. SHELLEY_UI=vue|react env var (last-resort default when the flag is unset)
//
// The per-request overrides (1 & 2) never touch persisted state, so concurrent
// test workers can each pin a world on the same server.
func (s *Server) resolveUIWorld(r *http.Request) UIWorld {
	if w := parseUIWorld(r.URL.Query().Get(uiWorldQuery)); w != "" {
		return w
	}
	if w := parseUIWorld(r.Header.Get(uiWorldHeader)); w != "" {
		return w
	}
	if s.vueUIFlag(r.Context()) {
		return UIWorldVue
	}
	return UIWorldReact
}

// vueUIFlag resolves the boolean `vue-ui` feature flag: a DB override (if
// present and valid) wins; otherwise the env default SHELLEY_UI, then the
// code-defined registry default. Any error reading the DB falls back to the
// registry default so a flaky DB never breaks page loads.
func (s *Server) vueUIFlag(ctx context.Context) bool {
	if overrides, err := s.db.GetAllFeatureFlagOverrides(ctx); err == nil {
		if raw, ok := overrides[FlagVueUI.Name]; ok {
			switch strings.TrimSpace(raw) {
			case "true":
				return true
			case "false":
				return false
			}
		}
	}
	// No stored override. An explicit SHELLEY_UI env var sets the default world
	// for this process (handy for `shelley serve` and demos); otherwise use the
	// registry default.
	if w := parseUIWorld(os.Getenv("SHELLEY_UI")); w != "" {
		return w == UIWorldVue
	}
	def, _ := FlagVueUI.Default.(bool)
	return def
}

// uiAssetsHTML returns the <link>/<script> tags that load the bundle for the
// given world. These replace the %SHELLEY_UI_ASSETS% marker in index.html.
func uiAssetsHTML(world UIWorld) string {
	return `<link rel="stylesheet" href="/main.` + string(world) + `.css" />` +
		`<script type="module" src="/main.` + string(world) + `.js"></script>`
}
