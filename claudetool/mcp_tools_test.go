package claudetool

import (
	"encoding/json"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/mcp"
)

func TestWrapMCPTool(t *testing.T) {
	// We can't easily test the full MCP flow without a server,
	// but we can verify the tool wrapping logic.
	ti := mcp.ToolInfo{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
	}

	tool := wrapMCPTool(nil, "test-server", ti)

	if tool.Name != "test_tool" {
		t.Errorf("Name = %q, want test_tool", tool.Name)
	}
	if tool.Description != "[test-server] A test tool" {
		t.Errorf("Description = %q, want [test-server] A test tool", tool.Description)
	}
	if tool.InputSchema == nil {
		t.Error("InputSchema should not be nil")
	}
	if tool.Run == nil {
		t.Error("Run should not be nil")
	}
}

func TestWrapMCPToolNilSchema(t *testing.T) {
	ti := mcp.ToolInfo{
		Name:        "empty_tool",
		Description: "Tool with no schema",
	}

	tool := wrapMCPTool(nil, "", ti)

	if tool.Description != "Tool with no schema" {
		t.Errorf("Description = %q, want no prefix when serverName is empty", tool.Description)
	}

	// Should get a default schema.
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
}

func TestNewToolSetWithMCPTools(t *testing.T) {
	// Verify MCP tools are included in the tool set.
	mcpTools := wrapMCPTool(nil, "vestige", mcp.ToolInfo{
		Name:        "search",
		Description: "Search memories",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
	})

	cfg := ToolSetConfig{
		WorkingDir: t.TempDir(),
		MCPTools:   []*llm.Tool{mcpTools},
	}

	ts := NewToolSet(t.Context(), cfg)
	defer ts.Cleanup()

	// Find the MCP tool in the set.
	found := false
	for _, tool := range ts.Tools() {
		if tool.Name == "search" {
			found = true
			break
		}
	}
	if !found {
		t.Error("MCP tool 'search' not found in tool set")
	}
}

func TestDeferredToolGroup(t *testing.T) {
	// Create a deferred group with some tools.
	ddTool1 := wrapMCPTool(nil, "datadog", mcp.ToolInfo{
		Name:        "search_datadog_logs",
		Description: "Search logs",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
	})
	ddTool2 := wrapMCPTool(nil, "datadog", mcp.ToolInfo{
		Name:        "get_datadog_metric",
		Description: "Get metrics",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
	})

	cfg := ToolSetConfig{
		WorkingDir: t.TempDir(),
		MCPDeferredGroups: []MCPToolGroup{
			{Name: "datadog", Tools: []*llm.Tool{ddTool1, ddTool2}},
		},
	}

	ts := NewToolSet(t.Context(), cfg)
	defer ts.Cleanup()

	// The activator tool should be present, but not the DD tools.
	tools := ts.Tools()
	var hasActivator, hasLogs, hasMetric bool
	for _, tool := range tools {
		switch tool.Name {
		case "activate_datadog_tools":
			hasActivator = true
		case "search_datadog_logs":
			hasLogs = true
		case "get_datadog_metric":
			hasMetric = true
		}
	}
	if !hasActivator {
		t.Error("activator tool not found in initial tools")
	}
	if hasLogs || hasMetric {
		t.Error("deferred tools should not be present initially")
	}

	// Activate the group.
	newTools := ts.ActivateGroup("datadog")

	// Now the activator should be gone, and the real tools should be present.
	hasActivator = false
	hasLogs = false
	hasMetric = false
	for _, tool := range newTools {
		switch tool.Name {
		case "activate_datadog_tools":
			hasActivator = true
		case "search_datadog_logs":
			hasLogs = true
		case "get_datadog_metric":
			hasMetric = true
		}
	}
	if hasActivator {
		t.Error("activator tool should be removed after activation")
	}
	if !hasLogs {
		t.Error("search_datadog_logs should be present after activation")
	}
	if !hasMetric {
		t.Error("get_datadog_metric should be present after activation")
	}

	// ts.Tools() should also reflect the change.
	if len(ts.Tools()) != len(newTools) {
		t.Errorf("Tools() len = %d, ActivateGroup returned len = %d", len(ts.Tools()), len(newTools))
	}

	// Activating again should be a no-op.
	sameTools := ts.ActivateGroup("datadog")
	if len(sameTools) != len(newTools) {
		t.Errorf("second activation changed tool count: %d vs %d", len(sameTools), len(newTools))
	}
}

func TestBuildActivatorDescription(t *testing.T) {
	group := MCPToolGroup{
		Name: "datadog",
		Tools: []*llm.Tool{
			{Name: "search_datadog_logs", Description: "[datadog] Search and retrieve raw log entries"},
			{Name: "get_datadog_metric", Description: "[datadog] Get metrics timeseries data"},
		},
	}

	desc := buildActivatorDescription(group)

	if !strings.Contains(desc, "search_datadog_logs") {
		t.Error("description should list search_datadog_logs")
	}
	if !strings.Contains(desc, "get_datadog_metric") {
		t.Error("description should list get_datadog_metric")
	}
	if !strings.Contains(desc, "datadog") {
		t.Error("description should mention the group name")
	}
}
