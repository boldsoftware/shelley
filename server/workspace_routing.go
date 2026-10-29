package server

import (
	"context"
	"database/sql"
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

func (s *Server) checkConversationRequest(w http.ResponseWriter, id string) bool {
	s.workspace.mu.Lock()
	defer s.workspace.mu.Unlock()
	return s.allowConversationMutation(w, id)
}

// Caller holds workspace.mu, which guards initializing.
func (s *Server) allowConversationMutation(w http.ResponseWriter, id string) bool {
	if s.workspace.initializing[id] {
		http.Error(w, "The conversation is starting; wait for its first message to be accepted", http.StatusConflict)
		return false
	}
	return true
}

// projectService gives a project conversation's loop its project's
// instructions; any other conversation's loop gets service unchanged.
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

// workspaceService reads project instructions at each model request. Project
// edits are immediately available even in long-lived conversations and survive
// compaction without stale prompt copies.
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
	project, err := s.db.GetProject(ctx, metadata.Project)
	if err != nil {
		return "", fmt.Errorf("load project: %w", err)
	}
	var out strings.Builder
	writeProjectPrompt(&out, project)
	return out.String(), nil
}

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
