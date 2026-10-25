package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/mcp"
)

// CloseMCP closes all MCP sessions.
func (s *Server) CloseMCP() { s.mcp.Close() }

// mcpServerChangeNotice is the text that tells a working agent the registered
// MCP servers changed. action is "added", "updated" or "removed".
func mcpServerChangeNotice(action, name string) string {
	switch action {
	case "added":
		return fmt.Sprintf("The MCP server %q was just registered. Use it with `shelley mcp` (see the mcp skill).", name)
	case "updated":
		return fmt.Sprintf("The MCP server %q was just reconfigured; its tools may have changed.", name)
	case "removed":
		return fmt.Sprintf("The MCP server %q was just removed.", name)
	default:
		return fmt.Sprintf("The registered MCP servers changed (%s %q).", action, name)
	}
}

// notifyMCPServersChanged injects a short notice into every agent-driven
// conversation whose agent is currently working, so the model learns the
// registered MCP servers changed mid-turn. Idle conversations aren't woken;
// their model can still run `shelley mcp list`, and conversations created after
// the change list the new set in their system prompt (an existing
// conversation's system prompt is written once and not regenerated). Internal
// children (btw readers, transcriptions) are skipped: a foreign
// user message would corrupt their fixed flow. It runs in its own goroutine,
// off the request path.
func (s *Server) notifyMCPServersChanged(ctx context.Context, action, name string) {
	s.mu.Lock()
	managers := make([]*ConversationManager, 0, len(s.activeConversations))
	for _, cm := range s.activeConversations {
		managers = append(managers, cm)
	}
	s.mu.Unlock()
	text := mcpServerChangeNotice(action, name)
	for _, cm := range managers {
		// Only conversations a human or the subagent tool drives use MCP tools.
		if cm.role != roleTopLevel && cm.role != roleSubagent {
			continue
		}
		// This check and the InjectMessage below aren't atomic: a conversation
		// that finishes its turn in between is woken for one short turn carrying
		// just this notice. That's rare and harmless, so we accept the race
		// rather than hold a lock across the injection.
		if !cm.IsAgentWorking() {
			continue
		}
		cm.mu.Lock()
		modelID := cm.modelID
		cm.mu.Unlock()
		data := mcpNoticeUserData{MCPServerChange: action, ServerName: name, Text: text}
		if err := cm.InjectMessage(contextWithTurnUserData(ctx, data), s, modelID, llm.UserStringMessage(text)); err != nil {
			s.logger.Warn("failed to inject MCP server change notice", "conversation", cm.conversationID, "error", err)
		}
	}
}

func (s *Server) registerMCPRoutes(api, mux *http.ServeMux) {
	api.HandleFunc("GET /api/mcp/servers", s.handleListMCPServers)
	api.HandleFunc("POST /api/mcp/servers", s.handleCreateMCPServer)
	api.HandleFunc("PUT /api/mcp/servers/{name}", s.handleUpdateMCPServer)
	api.HandleFunc("DELETE /api/mcp/servers/{name}", s.handleDeleteMCPServer)
	api.HandleFunc("GET /api/mcp/servers/{name}/tools", s.handleListMCPTools)
	api.HandleFunc("POST /api/mcp/servers/{name}/call", s.handleCallMCPTool)
	api.HandleFunc("POST /api/mcp/servers/{name}/restart", s.handleRestartMCPSession)
	api.HandleFunc("POST /api/mcp/servers/{name}/logout", s.handleMCPLogout)
	mux.HandleFunc("GET /mcp/login/{name}", s.handleMCPLogin)
	// Link previews mustn't start logins.
	mux.HandleFunc("HEAD /mcp/login/{name}", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /mcp/oauth/callback", s.handleMCPCallback)
	mux.HandleFunc("GET /debug/mcp", s.handleDebugMCP)
}

// mcpServerJSON is a server in the API.
type mcpServerJSON struct {
	mcp.Server
	Auth        string `json:"auth"`
	LoginURL    string `json:"login_url,omitempty"`
	Integration string `json:"integration,omitempty"`
}

func (s *Server) mcpServerJSON(ctx context.Context, row generated.McpServer) (mcpServerJSON, error) {
	srv, err := mcpServerFromRow(row)
	if err != nil {
		return mcpServerJSON{}, err
	}
	st, err := s.mcp.LoginState(ctx, srv)
	if err != nil {
		return mcpServerJSON{}, err
	}
	v := mcpServerJSON{Server: srv, Auth: st.Auth}
	if srv.OAuthCapable() {
		v.LoginURL = s.mcpLoginURL(srv.Name)
	}
	return v, nil
}

func mcpServerFromRow(row generated.McpServer) (mcp.Server, error) {
	srv := mcp.Server{Name: row.Name, URL: row.Url, Description: row.Description, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	return srv, json.Unmarshal([]byte(row.Headers), &srv.Headers)
}

// mcpLoginURL is the link that logs the user in to the named server. It's a
// root-relative path so it resolves against whatever host the user reached
// Shelley on (localhost, a LAN address, or the exe.dev proxy), rather than a
// guessed public hostname that may be wrong off exe.dev.
func (s *Server) mcpLoginURL(name string) string {
	return "/mcp/login/" + name
}

func writeMCPJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// mcpError writes err, from talking to an MCP server.
func mcpError(w http.ResponseWriter, err error) {
	var lr *mcp.LoginRequiredError
	switch {
	case errors.As(err, &lr):
		http.Error(w, lr.Error(), http.StatusUnauthorized)
	case errors.Is(err, mcp.ErrTimeout):
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

// getMCPServer returns the named server, or an error wrapping
// sql.ErrNoRows if there's none.
func (s *Server) getMCPServer(ctx context.Context, name string) (mcp.Server, error) {
	var row generated.McpServer
	err := s.db.Queries(ctx, func(q *generated.Queries) (err error) {
		row, err = q.GetMCPServer(ctx, name)
		return err
	})
	if !errors.Is(err, sql.ErrNoRows) {
		if err != nil {
			return mcp.Server{}, err
		}
		return mcpServerFromRow(row)
	}
	integrations, err := s.mcpIntegrations(ctx)
	if err != nil {
		return mcp.Server{}, err
	}
	for _, ig := range integrations {
		if ig.Name == name {
			return ig.Server, nil
		}
	}
	return mcp.Server{}, sql.ErrNoRows
}

// loadMCPServer returns the server named in r's path, or writes an error.
func (s *Server) loadMCPServer(w http.ResponseWriter, r *http.Request) (mcp.Server, bool) {
	srv, err := s.getMCPServer(r.Context(), r.PathValue("name"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, mcpNotFound(r.PathValue("name")), http.StatusNotFound)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	return srv, err == nil
}

func mcpNotFound(name string) string { return fmt.Sprintf("MCP server %q not found", name) }

func (s *Server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	var rows []generated.McpServer
	err := s.db.Queries(r.Context(), func(q *generated.Queries) (err error) {
		rows, err = q.ListMCPServers(r.Context())
		return err
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := []mcpServerJSON{}
	for _, row := range rows {
		v, err := s.mcpServerJSON(r.Context(), row)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, v)
	}
	integrations, err := s.mcpIntegrations(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("discovering MCP integrations: %v", err), http.StatusBadGateway)
		return
	}
	writeMCPJSON(w, mergeMCPIntegrations(out, integrations))
}

// mcpIntegrations returns the MCP integrations attached to this VM. They are
// looked up each time, not stored; exe.dev's proxy adds their credentials.
func (s *Server) mcpIntegrations(ctx context.Context) ([]mcpServerJSON, error) {
	env, integrations, err := s.listIntegrations(ctx)
	return mcpIntegrationsOf(env, integrations), err
}

// mcpIntegrationsOf returns the MCP integrations among integrations.
func mcpIntegrationsOf(env exeenv.Environment, integrations []reflectionIntegration) []mcpServerJSON {
	var out []mcpServerJSON
	for _, ig := range integrations {
		if slices.Contains(ig.Attributes, "type:mcp") {
			out = append(out, mcpServerJSON{
				Server:      mcp.Server{Name: ig.Name, URL: env.IntegrationURL(ig.Name, ig.Team) + "/mcp", Description: ig.Comment, Headers: map[string]string{}},
				Integration: ig.Name,
			})
		}
	}
	return out
}

// mergeMCPIntegrations adds integrations to servers. A registered server an
// integration reaches is marked as that integration's; one with the same
// name hides it.
func mergeMCPIntegrations(servers, integrations []mcpServerJSON) []mcpServerJSON {
	for _, ig := range integrations {
		covered := false
		for i := range servers {
			if mcp.SameLoginScope(servers[i].URL, ig.URL) {
				servers[i].Auth, servers[i].LoginURL, servers[i].Integration = "", "", ig.Integration
				covered = true
			}
			covered = covered || servers[i].Name == ig.Name
		}
		if !covered {
			servers = append(servers, ig)
		}
	}
	return servers
}

// decodeMCPServer reads a server from r's body.
func decodeMCPServer(r *http.Request) (mcp.Server, error) {
	var srv mcp.Server
	if err := json.NewDecoder(r.Body).Decode(&srv); err != nil {
		return srv, fmt.Errorf("invalid JSON: %w", err)
	}
	if name := r.PathValue("name"); name != "" {
		if srv.Name != "" && srv.Name != name {
			return srv, errors.New("the name can't be changed")
		}
		srv.Name = name
	}
	if srv.Headers == nil {
		srv.Headers = map[string]string{}
	}
	return srv, srv.Validate()
}

func (s *Server) handleCreateMCPServer(w http.ResponseWriter, r *http.Request) {
	srv, err := decodeMCPServer(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	headers, _ := json.Marshal(srv.Headers)
	var row generated.McpServer
	err = s.db.QueriesTx(r.Context(), func(q *generated.Queries) (err error) {
		row, err = q.CreateMCPServer(r.Context(), generated.CreateMCPServerParams{Name: srv.Name, Url: srv.URL, Description: srv.Description, Headers: string(headers)})
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("MCP server %q already exists", srv.Name), http.StatusBadRequest)
		return
	}
	if err == nil {
		go s.notifyMCPServersChanged(context.WithoutCancel(r.Context()), "added", srv.Name)
	}
	s.writeMCPServer(w, r, row, err)
}

func (s *Server) handleUpdateMCPServer(w http.ResponseWriter, r *http.Request) {
	srv, err := decodeMCPServer(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	headers, _ := json.Marshal(srv.Headers)
	var old, row generated.McpServer
	err = s.db.QueriesTx(r.Context(), func(q *generated.Queries) (err error) {
		if old, err = q.GetMCPServer(r.Context(), srv.Name); err != nil {
			return err
		}
		row, err = q.UpdateMCPServer(r.Context(), generated.UpdateMCPServerParams{Name: srv.Name, Url: srv.URL, Description: srv.Description, Headers: string(headers)})
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, mcpNotFound(srv.Name), http.StatusNotFound)
		return
	}
	// A description-only edit leaves the connection and tools unchanged, so it
	// mustn't drop sessions or nudge working agents.
	connectionChanged := old.Url != srv.URL || old.Headers != string(headers)
	if err == nil && connectionChanged {
		// A login is for its origin, and unused with an Authorization header.
		// Editing only the path or query (a common way to pick which tools a
		// server exposes) keeps the login so the user needn't log in again.
		if !srv.OAuthCapable() || !mcp.SameLoginScope(old.Url, srv.URL) {
			err = s.mcp.Logout(r.Context(), srv.Name)
		} else {
			s.mcp.CloseServer(srv.Name)
		}
	}
	if err == nil && connectionChanged {
		go s.notifyMCPServersChanged(context.WithoutCancel(r.Context()), "updated", srv.Name)
	}
	s.writeMCPServer(w, r, row, err)
}

func (s *Server) writeMCPServer(w http.ResponseWriter, r *http.Request, row generated.McpServer, err error) {
	var v mcpServerJSON
	if err == nil {
		v, err = s.mcpServerJSON(r.Context(), row)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeMCPJSON(w, v)
}

func (s *Server) handleDeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var n int64
	err := s.db.QueriesTx(r.Context(), func(q *generated.Queries) (err error) {
		n, err = q.DeleteMCPServer(r.Context(), name)
		return err
	})
	if err == nil && n > 0 {
		err = s.mcp.Logout(r.Context(), name) // the login went with the server; this forgets the rest
	}
	switch {
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	case n == 0:
		http.Error(w, mcpNotFound(name), http.StatusNotFound)
	default:
		go s.notifyMCPServersChanged(context.WithoutCancel(r.Context()), "removed", name)
		w.WriteHeader(http.StatusNoContent)
	}
}

// mcpScope is the caller's conversation, whose sessions it uses.
func mcpScope(r *http.Request) string { return r.Header.Get("X-Shelley-Conversation-Id") }

func (s *Server) handleListMCPTools(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.loadMCPServer(w, r)
	if !ok {
		return
	}
	tools, err := s.mcp.ListTools(r.Context(), srv, mcpScope(r))
	if err != nil {
		mcpError(w, err)
		return
	}
	writeMCPJSON(w, tools)
}

func (s *Server) handleCallMCPTool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tool      string                     `json:"tool"`
		Arguments map[string]json.RawMessage `json:"arguments"`
		TimeoutMS int64                      `json:"timeout_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Tool == "" {
		http.Error(w, "tool is required", http.StatusBadRequest)
		return
	}
	srv, ok := s.loadMCPServer(w, r)
	if !ok {
		return
	}
	res, err := s.mcp.CallTool(r.Context(), srv, mcpScope(r), req.Tool, req.Arguments, time.Duration(req.TimeoutMS)*time.Millisecond)
	if err != nil {
		mcpError(w, err)
		return
	}
	writeMCPJSON(w, res)
}

func (s *Server) handleRestartMCPSession(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.loadMCPServer(w, r)
	if !ok {
		return
	}
	s.mcp.CloseSession(srv.Name, mcpScope(r))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMCPLogout(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.loadMCPServer(w, r)
	if !ok {
		return
	}
	if err := s.mcp.Logout(r.Context(), srv.Name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMCPLogin sends the browser to log in to an MCP server, and back
// into Shelley if that can't start.
func (s *Server) handleMCPLogin(w http.ResponseWriter, r *http.Request) {
	setLoginHeaders(w)
	name := r.PathValue("name")
	srv, err := s.getMCPServer(r.Context(), name)
	if errors.Is(err, sql.ErrNoRows) {
		err = errors.New(mcpNotFound(name))
	}
	var authURL string
	if err == nil {
		authURL, err = s.mcp.Login(r.Context(), srv, mcpRedirectURI(r))
	}
	if err != nil {
		redirectMCPLogin(w, r, name, err)
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleMCPCallback finishes a login and sends the browser back into
// Shelley, which shows the outcome.
func (s *Server) handleMCPCallback(w http.ResponseWriter, r *http.Request) {
	setLoginHeaders(w)
	name, err := s.mcp.FinishLogin(r.Context(), r.URL.Query())
	if errors.Is(err, mcp.ErrLoginExpired) {
		err = errors.New("This login expired or was already used. Start it again from the MCP Servers dialog or the agent's link.")
	}
	redirectMCPLogin(w, r, name, err)
}

// redirectMCPLogin sends the browser to Shelley, saying how logging in to
// name went.
func redirectMCPLogin(w http.ResponseWriter, r *http.Request, name string, err error) {
	q := url.Values{}
	if name != "" {
		q.Set("mcp_login", name)
	}
	if err != nil {
		q.Set("mcp_error", err.Error())
	}
	http.Redirect(w, r, "/?"+q.Encode(), http.StatusFound)
}

// setLoginHeaders keeps login URLs, which hold codes and states, out of
// caches and Referer headers.
func setLoginHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// mcpRedirectURI is the OAuth callback on the host the browser used, which
// is behind a proxy on exe.dev.
func mcpRedirectURI(r *http.Request) string {
	scheme, host := "http", r.Host
	if r.TLS != nil {
		scheme = "https"
	}
	if p, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ","); p != "" {
		scheme = strings.TrimSpace(p)
	}
	if h, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ","); h != "" {
		host = strings.TrimSpace(h)
	}
	return scheme + "://" + host + "/mcp/oauth/callback"
}
