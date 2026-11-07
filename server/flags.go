package server

import (
	"context"
	"encoding/json"
	"fmt"

	"shelley.exe.dev/db"
	"shelley.exe.dev/featureflags"
)

// FlagPerformanceHUD overlays a small heads-up display in the web UI showing
// live counters of hot reactive recomputations (message coalescing, render
// model rebuilds, markdown parses, scroll/resize handler fires, store
// notifications, ...). The counters themselves are always collected — they
// are plain Map increments, cheap enough to leave on — and are accessible
// from the browser console via window.__shelleyPerf regardless of the flag.
// The flag only controls whether the HUD overlay renders.
var FlagPerformanceHUD = featureflags.Register(featureflags.Flag{
	Name:        "performance-hud",
	Description: "Show a heads-up display of UI recomputation counters (also available via __shelleyPerf in the console).",
	Default:     false,
})

// FlagWorkspace turns on workspace mode: a pinned Assistant at / that routes
// work into projects, and the drawer grouped by project by default. Set at
// VM creation (POST /feature-flags {"name":"workspace","value":true}); the
// page and server read it on each load.
var FlagWorkspace = featureflags.Register(featureflags.Flag{
	Name:        db.WorkspaceFlag,
	Description: "Workspace mode: a pinned Assistant routes work into projects.",
	Default:     false,
})

// FlagNextPromptSuggestions asks the model, after each top-level turn, what
// the user will likely type next, and offers it in the empty composer.
var FlagNextPromptSuggestions = featureflags.Register(featureflags.Flag{
	Name:        "next-prompt-suggestions",
	Description: "After each turn, suggest the next message in the empty composer (Tab accepts). Costs one extra model request per turn, mostly prompt-cached where the model caches.",
	Default:     false,
})

// FlagCompactNav shows the sidebar's conversation rows as their title alone:
// no preview, date, path, tags or status lines under it. Workspace mode turns
// it on unless it is set.
var FlagCompactNav = featureflags.Register(featureflags.Flag{
	Name:             "compact-nav",
	Description:      "Sidebar conversation rows show only their title, its time on hover. On by default in workspace mode.",
	Default:          false,
	WorkspaceDefault: true,
})

// flagDefault is f's value on this server when no override is stored.
func (s *Server) flagDefault(f featureflags.Flag) any {
	if f.WorkspaceDefault != nil && s.workspaceMode() {
		return f.WorkspaceDefault
	}
	return f.Default
}

// flagOn reports whether the boolean flag f is on: its stored override, or
// else its default (see flagDefault).
func (s *Server) flagOn(ctx context.Context, f featureflags.Flag) (bool, error) {
	overrides, err := s.db.GetAllFeatureFlagOverrides(ctx)
	if err != nil {
		return false, err
	}
	raw, ok := overrides[f.Name]
	if !ok {
		return s.flagDefault(f) == true, nil
	}
	var on bool
	if err := json.Unmarshal([]byte(raw), &on); err != nil {
		return false, fmt.Errorf("feature flag %s: %w", f.Name, err)
	}
	return on, nil
}

// FlagDevProdTabs gives workspace mode's sidebar dev and prod tabs: prod
// lists the user's exe.dev VMs (see exe_vms.go).
var FlagDevProdTabs = featureflags.Register(featureflags.Flag{
	Name:        "dev-prod-tabs",
	Description: "Workspace mode: sidebar dev/prod tabs; prod lists your exe.dev VMs.",
	Default:     false,
})
