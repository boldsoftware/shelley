package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/mcp"
)

// MCPToolGroup is a named group of tools from a single MCP server.
type MCPToolGroup struct {
	Name  string      // server name (e.g., "datadog")
	Tools []*llm.Tool // the tools in this group
}

// MCPManager manages connections to MCP servers and their tools.
// It is safe for concurrent use.
type MCPManager struct {
	mu             sync.Mutex
	transports     []mcp.Transport
	activeTools    []*llm.Tool
	deferredGroups []MCPToolGroup
}

// NewMCPManager creates a new MCPManager, connects to all configured MCP servers,
// performs the initialization handshake, discovers their tools, and wraps them
// as Shelley tools. Servers with Defer=true have their tools held back for
// lazy activation.
func NewMCPManager(ctx context.Context, configs []mcp.ServerConfig) (*MCPManager, error) {
	m := &MCPManager{}

	for _, cfg := range configs {
		transport, err := mcp.NewTransport(ctx, cfg)
		if err != nil {
			// Log and skip failed servers rather than failing entirely.
			slog.Error("mcp: failed to start server", "name", cfg.Name, "error", err)
			continue
		}

		tools, err := transport.ListTools(ctx)
		if err != nil {
			slog.Error("mcp: failed to list tools", "name", cfg.Name, "error", err)
			transport.Close()
			continue
		}

		slog.Info("mcp: discovered tools", "name", cfg.Name, "count", len(tools), "defer", cfg.Defer)

		var wrapped []*llm.Tool
		for _, ti := range tools {
			wrapped = append(wrapped, wrapMCPTool(transport, cfg.Name, ti))
		}

		if cfg.Defer {
			m.deferredGroups = append(m.deferredGroups, MCPToolGroup{
				Name:  cfg.Name,
				Tools: wrapped,
			})
		} else {
			m.activeTools = append(m.activeTools, wrapped...)
		}

		m.transports = append(m.transports, transport)
	}

	return m, nil
}

// Tools returns the immediately-active MCP tools.
func (m *MCPManager) Tools() []*llm.Tool {
	if m == nil {
		return nil
	}
	return m.activeTools
}

// DeferredGroups returns the tool groups that are deferred for lazy activation.
func (m *MCPManager) DeferredGroups() []MCPToolGroup {
	if m == nil {
		return nil
	}
	return m.deferredGroups
}

// Close shuts down all MCP server connections.
func (m *MCPManager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, t := range m.transports {
		if err := t.Close(); err != nil {
			slog.Error("mcp: error closing client", "error", err)
		}
	}
	m.transports = nil
	return nil
}

// wrapMCPTool converts an MCP ToolInfo into a Shelley *llm.Tool that forwards
// calls to the MCP server.
func wrapMCPTool(transport mcp.Transport, serverName string, ti mcp.ToolInfo) *llm.Tool {
	// Build a description that includes the server name for disambiguation.
	desc := ti.Description
	if serverName != "" {
		desc = fmt.Sprintf("[%s] %s", serverName, desc)
	}

	// Ensure the input schema is valid for Shelley's MustSchema.
	// MCP tools may have schemas that don't include "type":"object" at the top level,
	// but well-behaved ones should. We pass through the raw schema.
	schema := ti.InputSchema
	if schema == nil {
		schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}

	return &llm.Tool{
		Name:        ti.Name,
		Description: desc,
		InputSchema: schema,
		Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
			result, err := transport.CallTool(ctx, ti.Name, input)
			if err != nil {
				return llm.ToolOut{Error: err}
			}
			return llm.ToolOut{
				LLMContent: llm.TextContent(result),
			}
		},
	}
}

// buildActivatorDescription creates the description for an activator tool
// that lists all the tools in the deferred group.
func buildActivatorDescription(group MCPToolGroup) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Load the %s tools into the conversation. ", group.Name))
	sb.WriteString("Call this tool when you need to use any of these tools:\n")
	for _, t := range group.Tools {
		// Strip the [server] prefix from description for the listing
		desc := t.Description
		prefix := fmt.Sprintf("[%s] ", group.Name)
		desc = strings.TrimPrefix(desc, prefix)
		// Truncate long descriptions
		if len(desc) > 120 {
			desc = desc[:117] + "..."
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Name, desc))
	}
	return sb.String()
}

// makeActivatorTool creates a lightweight tool that, when called, activates
// a deferred tool group. The tool's description lists all available tools
// so the LLM knows when to call it.
func makeActivatorTool(ts *ToolSet, group MCPToolGroup) *llm.Tool {
	name := "activate_" + group.Name + "_tools"
	desc := buildActivatorDescription(group)

	return &llm.Tool{
		Name:        name,
		Description: desc,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
			newTools := ts.ActivateGroup(group.Name)
			var names []string
			for _, t := range group.Tools {
				names = append(names, t.Name)
			}
			msg := fmt.Sprintf("%s tools activated (%d tools). They are now available: %s. Total tools: %d.",
				group.Name, len(group.Tools), strings.Join(names, ", "), len(newTools))
			return llm.ToolOut{
				LLMContent: llm.TextContent(msg),
			}
		},
	}
}
