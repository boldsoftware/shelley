package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/slug"
)

// prepareProjectConversation makes a new conversation a member of the project
// named by req.Project, or by the profile it starts from. A project's profile
// is where the conversation's settings come from, and its directory where it
// starts (createProjectConversation checks that the directory exists).
func (s *Server) prepareProjectConversation(w http.ResponseWriter, r *http.Request, req *NewConversationRequest) bool {
	var change SettingsChange
	if req.ConversationOptions != nil {
		change = req.ConversationOptions.Change
	}
	explicit := req.Project != ""
	switch {
	case explicit && change.Profile != nil && *change.Profile != req.Project:
		http.Error(w, "A project conversation's profile is its project's", http.StatusBadRequest)
		return false
	case !explicit && change.Profile != nil:
		req.Project = *change.Profile
	case !explicit && req.Title == "":
		// No profile: the default's, which is never a project's.
		return true
	}
	project, err := s.db.GetProject(r.Context(), req.Project)
	switch {
	case errors.Is(err, sql.ErrNoRows) && explicit:
		http.Error(w, "Project not found", http.StatusNotFound)
		return false
	case errors.Is(err, sql.ErrNoRows) && req.Title != "":
		http.Error(w, "Conversation title requires a project", http.StatusBadRequest)
		return false
	case errors.Is(err, sql.ErrNoRows):
		// An ordinary conversation, in no project.
		req.Project = ""
		return true
	}
	if err != nil {
		s.internalError(w, "Failed to load project", err)
		return false
	}
	req.Title = strings.TrimSpace(req.Title)
	if len(req.Title) > 200 || req.Title != "" && slug.Sanitize(req.Title) == "" {
		http.Error(w, "Conversation title must contain letters or numbers and be at most 200 bytes", http.StatusBadRequest)
		return false
	}
	if req.ConversationOptions == nil {
		req.ConversationOptions = &ChatOptions{}
	}
	req.ConversationOptions.Change.Profile = &project.Name
	// A project supplies the initial directory, not a permanent override:
	// conversations can subsequently use their own worktrees via change_dir.
	if project.Cwd != "" {
		req.Cwd = project.Cwd
	}
	return true
}

func (s *Server) nameConversation(ctx context.Context, id, title string) error {
	for attempt := range db.SlugAttempts {
		_, err := s.db.UpdateConversationSlug(ctx, id, db.SlugCandidate(slug.Sanitize(title), attempt))
		if err == nil {
			return nil
		}
		if !isUniqueConstraintErr(err) {
			return err
		}
	}
	return fmt.Errorf("could not allocate a unique conversation title")
}

// createProjectConversation creates a conversation in project name, or in
// none. Until release, requests that would race a project conversation's
// first message are refused (see allowConversationMutation).
// Only the small creation boundary holds the workspace mutex.
// Hooks may themselves use workspace APIs, so never hold it across user code.
func (s *Server) createProjectConversation(ctx context.Context, cwd, model *string, opts db.ConversationOptions, name string) (*generated.Conversation, func(), error) {
	s.workspace.mu.Lock()
	defer s.workspace.mu.Unlock()
	if name != "" {
		project, err := s.db.GetProject(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		if opts.Profile != project.Name {
			return nil, nil, fmt.Errorf("conversation in project %q would have profile %q", project.Name, opts.Profile)
		}
		if project.Cwd != "" {
			if err := validateConversationDirectory(project.Cwd); err != nil {
				return nil, nil, err
			}
			cwd = &project.Cwd
		}
	}
	conversation, err := s.db.CreateConversation(ctx, nil, true, cwd, model, opts)
	if err != nil {
		return nil, nil, err
	}
	if name != "" {
		if s.workspace.initializing == nil {
			s.workspace.initializing = make(map[string]bool)
		}
		s.workspace.initializing[conversation.ConversationID] = true
		s.notifyWorkspaceChanged()
	}
	release := func() {
		s.workspace.mu.Lock()
		delete(s.workspace.initializing, conversation.ConversationID)
		s.workspace.mu.Unlock()
	}
	return conversation, release, nil
}

func (s *Server) checkConversationRequest(w http.ResponseWriter, r *http.Request, id, operation string) bool {
	s.workspace.mu.Lock()
	defer s.workspace.mu.Unlock()
	return s.allowConversationMutation(w, r, id, operation)
}

// Caller holds workspace.mu, which guards initializing.
func (s *Server) allowConversationMutation(w http.ResponseWriter, r *http.Request, id, operation string) bool {
	if s.workspace.initializing[id] {
		http.Error(w, "The conversation is starting; wait for its first message to be accepted", http.StatusConflict)
		return false
	}
	protected := operation == "archive" || operation == "delete" || operation == "rename"
	if protected && s.isAssistant(id) {
		http.Error(w, "The Assistant is permanent; this operation is not available", http.StatusConflict)
		return false
	}
	return true
}

// projectService gives a project conversation's loop, or the Assistant's,
// its workspace instructions; any other conversation's loop gets service
// unchanged.
// Membership is decided when the loop starts, and a move restarts it.
func (s *Server) projectService(conversationID string, service llm.Service) (llm.Service, error) {
	_, err := s.db.GetWorkspaceConversation(context.Background(), conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return service, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load workspace context: %w", err)
	}
	return &workspaceService{Service: service, server: s, conversationID: conversationID}, nil
}

// workspaceService reads workspace instructions at each model request. Project
// edits and newly created projects are immediately available even in the
// long-lived Assistant and survive compaction without stale prompt copies.
type workspaceService struct {
	llm.Service
	server         *Server
	conversationID string
}

func (s *workspaceService) request(ctx context.Context, request *llm.Request) (*llm.Request, error) {
	prompt, err := s.server.workspaceInstructions(ctx, s.conversationID)
	if err != nil {
		return nil, err
	}
	if prompt == "" {
		return request, nil
	}
	copyRequest := *request
	copyRequest.System = append(append([]llm.SystemContent(nil), request.System...), llm.SystemContent{
		Type: "text", Text: prompt, Cache: true,
	})
	return &copyRequest, nil
}

func (s *workspaceService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	next, err := s.request(ctx, request)
	if err != nil {
		return nil, err
	}
	return s.Service.Do(ctx, next)
}

func (s *workspaceService) PatchProfile() string { return llm.PatchProfile(s.Service) }
func (s *workspaceService) SupportsReasoning() bool {
	return llm.SupportsReasoning(s.Service)
}

func (s *workspaceService) SupportedReasoningLevels() []llm.ThinkingLevel {
	return llm.SupportedReasoningLevels(s.Service)
}

func (s *workspaceService) DefaultReasoningLevel() string {
	return llm.ServiceDefaultReasoningLevel(s.Service)
}

func (s *workspaceService) SupportsServerSideWebSearch() bool {
	capability, ok := s.Service.(interface{ SupportsServerSideWebSearch() bool })
	return ok && capability.SupportsServerSideWebSearch()
}
func (s *workspaceService) CacheTTL() time.Duration { return llm.CacheTTL(s.Service) }
func (s *workspaceService) WarmCache(ctx context.Context, request *llm.Request) (llm.Usage, error) {
	next, err := s.request(ctx, request)
	if err != nil {
		return llm.Usage{}, err
	}
	return llm.WarmCache(ctx, s.Service, next)
}

func (s *Server) workspaceInstructions(ctx context.Context, id string) (string, error) {
	metadata, err := s.db.GetWorkspaceConversation(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		// Conversations in no project, internal readers, and subagents.
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load workspace context: %w", err)
	}
	if s.assistantID() == "" {
		// Outside workspace mode a project is its profile plus instructions;
		// the workspace (routing, its API) stays out of the prompt.
		project, err := s.db.GetProject(ctx, metadata.Project)
		if err != nil {
			return "", fmt.Errorf("load project: %w", err)
		}
		var out strings.Builder
		writeProjectPrompt(&out, project)
		return out.String(), nil
	}
	var out strings.Builder
	out.WriteString("<workspace>\n")
	if metadata.IsAssistant {
		out.WriteString(assistantRoutingPrompt)
		if origin := s.webOrigin.Load(); origin != nil {
			fmt.Fprintf(&out, "Task links: %s/c/CONVERSATION_ID, with the returned id.\n", *origin)
		} else {
			out.WriteString("Task links: no browser has used this Shelley since it started, so its web address is unknown. Give the task's title and id instead of guessing a URL.\n")
		}
		catalog, err := s.projectCatalog(ctx)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&out, "\nCurrent conversation id: %s\nAvailable projects (live catalog): %s\n", id, catalog)
	} else {
		project, err := s.db.GetProject(ctx, metadata.Project)
		if err != nil {
			return "", fmt.Errorf("load project: %w", err)
		}
		fmt.Fprintf(&out, "This conversation is a task in project %q.\n", project.Name)
		out.WriteString("Complete the user's work in THIS task. Do not route the same request into another task. The project instructions below (the project's AGENTS.md) supplement the task's own system prompt.\n")
		writeProjectPrompt(&out, project)
		fmt.Fprintf(&out, "\nCurrent conversation id: %s\n", id)
	}
	if s.socketPath != "" {
		out.WriteString("\nLocal API transport: set SHELLEY_API=http://localhost and use curl --unix-socket \"$SHELLEY_SOCKET\" for ALL API calls below. The exported socket identifies THIS instance and is trusted locally; plain TCP may require a proxy authentication header. Do not guess or spoof proxy headers.\n")
	} else {
		out.WriteString("\nLocal API transport: set SHELLEY_API=\"$SHELLEY_URL\" and use curl for the API calls below. This is this running instance's local address, not the external proxy.\n")
	}
	out.WriteString(workspaceAPIPrompt)
	out.WriteString("\n</workspace>\n")
	return out.String(), nil
}

// projectCatalog lists the projects for the Assistant, which routes on what
// people write about a project: its name and the start of its instructions.
// Only the Assistant gets it, so a project edit changes no task's prompt.
func (s *Server) projectCatalog(ctx context.Context) ([]byte, error) {
	projects, err := s.db.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("load projects: %w", err)
	}
	type projectSummary struct {
		Name         string `json:"name"`
		Instructions string `json:"instructions,omitempty"`
		Cwd          string `json:"cwd"`
	}
	summaries := make([]projectSummary, 0, len(projects))
	for _, project := range projects {
		summaries = append(summaries, projectSummary{project.Name,
			truncateRunes(strings.TrimSpace(project.Instructions), 300), project.Cwd})
	}
	return json.Marshal(summaries)
}

const assistantRoutingPrompt = `You are the user's long-lived Assistant and task dispatcher, pinned above all projects. Answer quick questions, one-off research, and casual requests here. For example, answer "what's the weather today?" directly (use a real weather source).

For substantive project work, code changes, multi-step deliverables, or work that deserves its own history, CREATE A TASK in the best matching existing project using the API below, rather than doing the project work in this thread. For example, "please make the signup form validate email addresses" belongs in a project for the web app. You decide using the request's meaning and the live project catalog (names, instructions, directories), not keywords alone. Each project is a profile: its tasks take the profile's settings (model, effort, tools), and the project's instructions and directory.

Before creating a task, inspect the workspace for an existing active task covering the same request; reuse it if appropriate rather than duplicating work. Every task needs a project. When no existing project is an obvious fit, create a new project for that kind of work, named and instructed so future related tasks belong there too, rather than placing the task in a catch-all. Projects are agent-maintained: keep their names, instructions (the project's AGENTS.md), and directories accurate and reusable; do not create a new project for every task.

Forward the full user request and all necessary context, constraints, and attachment paths to the task. Give it a short descriptive title. The task starts working immediately in its own conversation. Tell the user that you started it in one short plain-text message containing the task's full URL (see Task links below), not a markdown link or a bare path. Do not claim you completed the delegated work. Stay in the Assistant unless the user follows the task link.

Never invent a repository path or claim a remote operation succeeded without executing it. If a project directory is empty, the task should discover the correct environment before modifying files. Ask only when genuinely blocked.
`

const workspaceAPIPrompt = `
Manage projects and tasks using bash with the LOCAL Shelley HTTP API, using the transport and SHELLEY_API base specified above, not another instance or a guessed port. Use curl -fsS -H 'X-Shelley-Request: 1' -H 'Content-Type: application/json' (plus the socket flag when specified), with JSON encoded safely (e.g. python json.dumps to a file and --data-binary @file). Treat errors as errors. Never edit Shelley's SQLite tables directly.

GET $SHELLEY_API/api/workspace
  Returns assistant_id, projects, conversations (conversation_id, project, is_assistant, archived: the tasks).
GET $SHELLEY_API/api/conversations
  Lists active conversations/tasks, including their ids, titles (slug), and working state. GET /api/conversation/ID retrieves messages for relevant context.
POST $SHELLEY_API/api/conversations/new
  Body: {"project":"Existing project name","title":"Short task title","message":"Full work request and context"}
  Returns conversation_id; work starts immediately.
POST $SHELLEY_API/api/conversation/ID/chat
  Body: {"message":"Follow-up instructions"}; send to an existing task only when appropriate.
POST $SHELLEY_API/api/projects
PUT $SHELLEY_API/api/projects/NAME
  Complete project body: {"name":"Project name","instructions":"Project instructions (AGENTS.md-style), appended to every task's prompt","cwd":"/absolute/real/project/path"}
  NAME is the project's name, URL-path-escaped (e.g. my%20project). A different name in a PUT body renames the project. Names are unique among profiles, at most 64 bytes, without "/". cwd can be empty when the environment is not known yet. Use an existing directory once discovered. Instructions are additive to the task's existing prompt; their start is what you route on, so lead with what the project is for. A project is a profile (GET $SHELLEY_API/api/profiles lists them, with "project": true): its settings (model, effort, tools, base system prompt) start as a copy of the default profile's, or of the profile named by "copy_settings_from" when creating; change them with PUT $SHELLEY_API/api/profiles/NAME (the complete settings).
PUT $SHELLEY_API/api/conversation/ID/project
  Body: {"project":"Existing project name"}; moves an idle task, not the Assistant. Unless its settings were changed, it takes the new project's at its next turn.
DELETE $SHELLEY_API/api/projects/NAME
  Deletes the project and its profile. Refused (409) while the project has active tasks. Moves archived tasks to no project. Never deletes task history or repositories.
`

// writeProjectPrompt writes a project conversation's project instructions
// into out.
func writeProjectPrompt(out *strings.Builder, current *db.Project) {
	out.WriteString("<project_instructions>\n")
	out.WriteString(current.Instructions)
	out.WriteString("\n</project_instructions>\n")
	if current.Cwd == "" {
		out.WriteString("This project has no configured directory. Do not assume the initial working directory is its repository. Discover and configure the correct environment before editing files or running project commands.\n")
	}
}
