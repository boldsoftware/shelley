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

// FlagVueUI selects which frontend ("world") the server serves. When true, the
// page loads the Vue 3 + PrimeVue bundle (main.vue.js/css); when false, the
// legacy React bundle (main.react.js/css). Both bundles are built and embedded;
// the server injects the right pair into index.html at request time, so a user
// can flip this flag and reload to switch frontends without a rebuild.
//
// The resolved world can also be forced per-request (e.g. by the e2e/lazycue
// suites, which run every spec in both worlds) via the `__ui` query param or
// the X-Shelley-UI header. See server/ui_world.go.
var FlagVueUI = featureflags.Register(featureflags.Flag{
	Name:        "vue-ui",
	Description: "Serve the Vue 3 + PrimeVue frontend instead of the legacy React frontend. Reload after changing.",
	Default:     false,
})
