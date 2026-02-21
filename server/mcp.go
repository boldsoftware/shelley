package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"shelley.exe.dev/claudetool"
)

// NewMCPHandler creates an HTTP handler for the MCP endpoint for a given toolset.
func NewMCPHandler(tools *claudetool.ToolSet) http.Handler {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "shelley-mcp", Version: "1.0.0"},
		nil,
	)

	for _, tool := range tools.Tools() {
		if tool.Name == "subagent" {
			continue
		}

		schemaRaw, _ := json.Marshal(tool.InputSchema)
		t := tool // capture loop variable

		mcpServer.AddTool(
			&mcp.Tool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: json.RawMessage(schemaRaw),
			},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				toolOut := t.Run(ctx, req.Params.Arguments)

				if toolOut.Error != nil {
					return &mcp.CallToolResult{
						Content: []mcp.Content{&mcp.TextContent{Text: toolOut.Error.Error()}},
						IsError: true,
					}, nil
				}

				var text string
				if len(toolOut.LLMContent) > 0 {
					text = toolOut.LLMContent[0].Text
				}

				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: text}},
				}, nil
			},
		)
	}

	return mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	conversationID := r.PathValue("id")
	if conversationID == "" {
		http.Error(w, "missing conversation id", 400)
		return
	}

	manager, err := s.getOrCreateConversationManager(r.Context(), conversationID)
	if err != nil {
		http.Error(w, "conversation not found", 404)
		return
	}

	manager.mu.Lock()
	if manager.toolSet == nil {
		manager.toolSet = claudetool.NewToolSet(context.Background(), manager.toolSetConfig)
	}
	ts := manager.toolSet
	manager.mu.Unlock()

	handler := NewMCPHandler(ts)
	handler.ServeHTTP(w, r)
}
