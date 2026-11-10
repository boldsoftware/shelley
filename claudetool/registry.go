package claudetool

import "shelley.exe.dev/llm"

// ToolInfo describes a tool available to conversations.
type ToolInfo struct {
	Name       string `json:"name"`
	Summary    string `json:"summary"`
	DefaultOn  bool   `json:"default_on"`
	SourcePath string `json:"-"`
}

// ToolRegistry lists every tool that a Shelley conversation can use, along with
// whether it is on by default. This is what the UI enumerates in the gear menu
// and what the API accepts in tool_overrides.
//
// Keep in sync with NewToolSet / browse.RegisterBrowserTools.
var ToolRegistry = []ToolInfo{
	{Name: "bash", Summary: "Run shell commands.", DefaultOn: true, SourcePath: "claudetool/bash.go"},
	{Name: "patch", Summary: "Precise edits to files.", DefaultOn: true, SourcePath: "claudetool/patch.go"},
	{Name: "change_dir", Summary: "Change the working directory.", DefaultOn: true, SourcePath: "claudetool/changedir.go"},
	{Name: "output_iframe", Summary: "Show HTML/visualizations to the user.", DefaultOn: true, SourcePath: "claudetool/output_iframe.go"},
	{Name: "subagent", Summary: "Spawn a subagent conversation.", DefaultOn: true, SourcePath: "claudetool/subagent.go"},
	{Name: "llm_one_shot", Summary: "One-shot prompt to another LLM.", DefaultOn: true, SourcePath: "claudetool/llm_one_shot.go"},
	{Name: "browser", Summary: "Browser automation (navigate, eval, screenshot, emulate, network, accessibility, profile).", DefaultOn: true, SourcePath: "claudetool/browse/browse.go"},
	{Name: "read_image", Summary: "Read an image file for the model.", DefaultOn: true, SourcePath: "claudetool/browse/browse.go"},
	{Name: "compact_in_place", Summary: "The agent compacts its own context.", DefaultOn: true, SourcePath: "claudetool/compact_in_place.go"},
	{Name: "write_secret", Summary: "Write a secret the user stored to a file, unseen (secrets flag).", DefaultOn: true, SourcePath: "claudetool/secret.go"},
	{Name: "message_user", Summary: "Chat with the user: messages, replies, emoji reactions, attachments.", DefaultOn: false, SourcePath: "claudetool/message_user.go"},
	{Name: "chapter", Summary: "Mark a change of direction in the conversation's chapters.", DefaultOn: true, SourcePath: "claudetool/chapter.go"},
	{Name: "send_to_thread", Summary: "Reply in a Slack thread (Slack Assistant).", DefaultOn: false, SourcePath: "claudetool/slack.go"},
	{Name: "send_to_channel", Summary: "Post to a Slack channel (Slack Assistant).", DefaultOn: false, SourcePath: "claudetool/slack.go"},
	{Name: "add_reaction", Summary: "React to a Slack message with an emoji (Slack Assistant).", DefaultOn: false, SourcePath: "claudetool/slack.go"},
}

// ToolInfoByName returns registry metadata for a tool.
func ToolInfoByName(name string) (ToolInfo, bool) {
	name = registeredToolName(name)
	for _, tool := range ToolRegistry {
		if tool.Name == name {
			return tool, true
		}
	}
	return ToolInfo{}, false
}

func registeredToolName(name string) string {
	switch name {
	case ApplyPatchName:
		return PatchName
	case messageParentName:
		return subagentName
	}
	return name
}

// IsToolEnabled reports whether a tool with the given name is enabled for a
// conversation given the override map and a global "disable all" flag.
// overrides maps tool name to "on" or "off"; any other value is ignored.
func IsToolEnabled(name string, overrides map[string]string, disableAll bool) bool {
	name = registeredToolName(name)
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
