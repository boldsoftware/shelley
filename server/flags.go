package server

import "shelley.exe.dev/featureflags"

// FlagToolPills toggles the iOS-style pill rendering of tool bursts in the
// web conversation UI. When false (the default), each tool call renders as
// a full-width CoalescedToolCall card as before. When true, consecutive
// non-auto-expand tool calls collapse into a wrapped row of compact pills;
// tapping a pill opens the full card in a modal.
//
// Auto-expand tools (patch, screenshot, read_image, output_iframe) are
// unaffected — they continue to render inline regardless of this flag.
var FlagToolPills = featureflags.Register(featureflags.Flag{
	Name:        "tool-pills",
	Description: "Render bursts of tool calls as compact pills (iOS-style). Click a pill to open the full tool card in a modal.",
	Default:     false,
})

// FlagReflectionEmojiFavicon, when enabled, replaces the generated "Cool S"
// favicon with the VM's emoji taken from the exe.dev reflection integration's
// root document. When false, the deterministic per-host Shelley mark is used
// instead. This flag is evaluated server-side, in serveIndexWithInit, because
// the favicon <link> is injected into index.html as the page is served.
// Defaults on; falls back to the generated mark when reflection has no emoji.
var FlagReflectionEmojiFavicon = featureflags.Register(featureflags.Flag{
	Name:        "reflection-emoji-favicon",
	Description: "Use the VM's reflection emoji as the browser favicon instead of the generated Shelley mark.",
	Default:     true,
})

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

// FlagPatchSimple switches the patch tool from its full nested patches schema
// to a simplified path-and-edits replacement schema.
var FlagPatchSimple = featureflags.Register(featureflags.Flag{
	Name:        "patch-simple",
	Description: "Use a simplified path and edits array for atomic exact-text replacements. When off, use the full nested patches schema.",
	Default:     false,
})

// FlagPatchOpenAIRaw lets capable direct OpenAI Responses models use the raw,
// grammar-constrained Codex apply_patch tool. It overrides patch-simple when
// both flags are enabled and has no effect on unsupported providers.
var FlagPatchOpenAIRaw = featureflags.Register(featureflags.Flag{
	Name:        "patch-openai-raw",
	Description: "Use raw grammar-constrained apply_patch for capable direct OpenAI Responses models, overriding the full or simplified nested patch schema.",
	Default:     false,
})
