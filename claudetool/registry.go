package claudetool

import "shelley.exe.dev/llm"

// ToolInfo describes a tool available to conversations.
type ToolInfo struct {
	Name      string `json:"name"`
	Summary   string `json:"summary"`
	DefaultOn bool   `json:"default_on"`
}

// ToolRegistry lists every tool that a Shelley conversation can use, along with
// whether it is on by default. This is what the UI enumerates in the gear menu
// and what the API accepts in tool_overrides.
//
// Keep in sync with NewToolSet / NewOrchestratorToolSet / browse.RegisterBrowserTools.
var ToolRegistry = []ToolInfo{
	{Name: "bash", Summary: "Run shell commands.", DefaultOn: true},
	{Name: "shell", Summary: "Run shell commands.", DefaultOn: false},
	{Name: "patch", Summary: "Precise edits to files.", DefaultOn: true},
	{Name: "keyword_search", Summary: "Search the codebase by keyword.", DefaultOn: true},
	{Name: "change_dir", Summary: "Change the working directory.", DefaultOn: true},
	{Name: "output_iframe", Summary: "Show HTML/visualizations to the user.", DefaultOn: true},
	{Name: "subagent", Summary: "Spawn a subagent conversation.", DefaultOn: true},
	{Name: "llm_one_shot", Summary: "One-shot prompt to another LLM.", DefaultOn: true},
	{Name: "read_context_file", Summary: "Read a file from the orchestrator context dir.", DefaultOn: true},
	{Name: "browser", Summary: "Browser automation (navigate, eval, screenshot, ...).", DefaultOn: true},
	{Name: "read_image", Summary: "Read an image file for the model.", DefaultOn: true},
	{Name: "browser_emulate", Summary: "Device/display emulation in the browser.", DefaultOn: true},
	{Name: "browser_network", Summary: "Browser network inspection.", DefaultOn: true},
	{Name: "browser_accessibility", Summary: "Browser accessibility tree inspection.", DefaultOn: true},
	{Name: "browser_profile", Summary: "Browser performance profiling.", DefaultOn: true},
}

// IsToolEnabled reports whether a tool with the given name is enabled for a
// conversation given the override map and a global "disable all" flag.
// overrides maps tool name to "on" or "off"; any other value is ignored.
func IsToolEnabled(name string, overrides map[string]string, disableAll bool) bool {
	switch overrides[name] {
	case "on":
		return true
	case "off":
		return false
	}
	if disableAll {
		return false
	}
	for _, t := range ToolRegistry {
		if t.Name == name {
			return t.DefaultOn
		}
	}
	// Unknown tool: be permissive (forward-compat for tool registry lag).
	return true
}

// FilterTools returns only the tools that are enabled under the given overrides.
func FilterTools(tools []*llm.Tool, overrides map[string]string, disableAll bool) []*llm.Tool {
	out := make([]*llm.Tool, 0, len(tools))
	for _, t := range tools {
		if IsToolEnabled(t.Name, overrides, disableAll) {
			out = append(out, t)
		}
	}
	return out
}
