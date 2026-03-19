package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/mcp"
)

// MCPManager manages connections to MCP servers and their tools.
// It is safe for concurrent use.
type MCPManager struct {
	mu         sync.Mutex
	transports []mcp.Transport
	tools      []*llm.Tool
}

// NewMCPManager creates a new MCPManager, connects to all configured MCP servers,
// performs the initialization handshake, discovers their tools, and wraps them
// as Shelley tools.
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

		slog.Info("mcp: discovered tools", "name", cfg.Name, "count", len(tools))

		for _, ti := range tools {
			tool := wrapMCPTool(transport, cfg.Name, ti)
			m.tools = append(m.tools, tool)
		}

		m.transports = append(m.transports, transport)
	}

	return m, nil
}

// Tools returns all discovered MCP tools as Shelley tools.
func (m *MCPManager) Tools() []*llm.Tool {
	if m == nil {
		return nil
	}
	return m.tools
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
