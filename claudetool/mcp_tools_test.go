package claudetool

import (
	"encoding/json"
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
