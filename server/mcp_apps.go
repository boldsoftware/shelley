package server

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/mcp"
)

// mcpAppHeader on a call's response says its MCP App's view is in the
// conversation.
const mcpAppHeader = "X-Shelley-Mcp-App"

// mcpAppServerHeader on a view's request is the AppServerID of the server
// the view came from.
const mcpAppServerHeader = "X-Shelley-Mcp-App-Server"

// appContextHeader on a chat response lists, by index and comma-separated,
// the request's app_context entries its message carries. Those not listed,
// and all of them without it, didn't go.
const appContextHeader = "X-Shelley-App-Context"

// mcpAppUserData is an mcpapp message's user_data: a call whose view the UI
// shows.
type mcpAppUserData struct {
	Server    string                     `json:"server"`
	ServerID  string                     `json:"server_id"` // mcp.Server.AppServerID
	Resource  string                     `json:"resource"`
	Tool      *sdk.Tool                  `json:"tool"`
	Arguments map[string]json.RawMessage `json:"arguments"`
	Result    *sdk.CallToolResult        `json:"result"`
}

// recordMCPApp appends the view of a call to the conversation, with stored
// secrets redacted as from any tool's output.
func (s *Server) recordMCPApp(ctx context.Context, conversationID string, data mcpAppUserData) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	redacted, err := s.secrets.Redact(ctx, string(raw))
	if err != nil {
		return err
	}
	message, err := s.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID:      conversationID,
		Type:                db.MessageTypeMCPApp,
		UserData:            json.RawMessage(redacted),
		ExcludedFromContext: true,
		BumpTimestamp:       true,
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	cm := s.activeConversations[conversationID]
	s.mu.Unlock()
	if cm != nil {
		cm.Touch()
		return cm.publishCreated(ctx, *message)
	}
	conversation, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		return err
	}
	if s.streamPub != nil {
		s.streamPub.Broadcast(StreamResponse{
			ConversationID: conversationID, Messages: toAPIMessages([]generated.Message{*message}), Conversation: conversation,
		})
	}
	return nil
}

// loadMCPAppServer loads the server a view's request names, if it's the
// server the view came from.
func (s *Server) loadMCPAppServer(w http.ResponseWriter, r *http.Request) (mcp.Server, bool) {
	srv, ok := s.loadMCPServer(w, r)
	if ok && r.Header.Get(mcpAppServerHeader) != srv.AppServerID() {
		http.Error(w, fmt.Sprintf("MCP server %q has changed since this view was shown", srv.Name), http.StatusConflict)
		return srv, false
	}
	return srv, ok
}

// handleMCPAppCall calls a tool for a view, in the view's conversation's
// session.
func (s *Server) handleMCPAppCall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tool      string                     `json:"tool"`
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	srv, ok := s.loadMCPAppServer(w, r)
	if !ok {
		return
	}
	// Listed afresh: whether views may call a tool is the server's to change.
	scope := mcpScope(r)
	tools, err := s.mcp.ListTools(r.Context(), srv, scope)
	if err != nil {
		s.mcpError(w, r, srv, err)
		return
	}
	i := slices.IndexFunc(tools.Tools, func(t *sdk.Tool) bool { return t.Name == req.Tool })
	if i < 0 || !mcp.ToolUIOf(tools.Tools[i]).VisibleTo("app") {
		http.Error(w, fmt.Sprintf("%s has no tool %q for views", srv.Name, req.Tool), http.StatusForbidden)
		return
	}
	res, err := s.mcp.CallTool(r.Context(), srv, scope, req.Tool, req.Arguments, 0)
	if err != nil {
		s.mcpError(w, r, srv, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleReadMCPResource reads a resource for a view.
func (s *Server) handleReadMCPResource(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.loadMCPAppServer(w, r)
	if !ok {
		return
	}
	res, err := s.mcp.ReadResource(r.Context(), srv, mcpScope(r), r.URL.Query().Get("uri"))
	if err != nil {
		s.mcpError(w, r, srv, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleReadMCPApp reads a view to render.
func (s *Server) handleReadMCPApp(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.loadMCPAppServer(w, r)
	if !ok {
		return
	}
	app, err := s.mcp.ReadApp(r.Context(), srv, mcpScope(r), r.URL.Query().Get("uri"))
	if err != nil {
		s.mcpError(w, r, srv, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

// appContext is what a view asked the model to know with the user's next
// message (ui/update-model-context). Source names the view.
type appContext struct {
	Source string `json:"source"`
	Text   string `json:"text"`
}

// withAppContext appends each view's context to a user message, for the
// model, as a view_context element whose text, escaped, can't close it.
// The UI shows it apart from the message.
func withAppContext(content []llm.Content, contexts []appContext) ([]llm.Content, error) {
	for _, c := range contexts {
		if strings.TrimSpace(c.Text) == "" {
			continue
		}
		opening, err := xmlProvenanceOpeningTag("view_context",
			xml.Attr{Name: xml.Name{Local: "source"}, Value: c.Source},
			xml.Attr{Name: xml.Name{Local: "note"}, Value: "what the view reports, not the user"})
		if err != nil {
			return nil, err
		}
		content = append(content, llm.Content{
			Type:    llm.ContentTypeText,
			Text:    opening + "\n" + xmlProvenanceText(c.Text) + "\n</view_context>",
			Display: appContextDisplay{Type: "app_context", Source: c.Source, Text: c.Text},
		})
	}
	return content, nil
}

// appContextDisplay is the Display of a view's context in a user message.
type appContextDisplay struct {
	Type   string `json:"type"`
	Source string `json:"source"`
	Text   string `json:"text"`
}

// isAppContext reports whether c is a view's context, which is already
// escaped.
func isAppContext(c llm.Content) bool {
	switch d := c.Display.(type) {
	case appContextDisplay:
		return true
	case map[string]any:
		return d["type"] == "app_context"
	}
	return false
}
