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
	// conversations can subsequently use their own worktrees via change_dir. A VM's
	// directory is always the project's: this host's means nothing there.
	if project.Cwd != "" || project.VM != nil {
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
// Only the small creation/reservation boundary holds the workspace mutex.
// Hooks may themselves use workspace APIs, so never hold it across user code.
func (s *Server) createProjectConversation(ctx context.Context, cwd, model *string, opts db.ConversationOptions, name string) (*generated.Conversation, func(), error) {
	s.workspace.mu.Lock()
	defer s.workspace.mu.Unlock()
	var project *db.Project
	var vm *db.ProjectVM
	if name != "" {
		var err error
		project, err = s.db.GetProject(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		if opts.Profile != project.Name {
			return nil, nil, fmt.Errorf("conversation in project %q would have profile %q", project.Name, opts.Profile)
		}
		if project.Cwd != "" && project.VM == nil {
			if err := validateConversationDirectory(project.Cwd); err != nil {
				return nil, nil, err
			}
		}
		if project.Cwd != "" {
			cwd = &project.Cwd
		}
		vm = project.VM
	}
	var conversation *generated.Conversation
	var err error
	if vm != nil {
		// Its VM is part of it from the start: a VM conversation never runs
		// here.
		conversation, err = s.db.CreateVMProjectConversation(ctx, cwd, model, opts, project)
	} else {
		conversation, err = s.db.CreateConversation(ctx, nil, true, cwd, model, opts)
	}
	if err != nil {
		return nil, nil, err
	}
	if vm != nil {
		// Moves it to its scratch directory here.
		if _, err := s.conversationVMTools(ctx, conversation); err != nil {
			return nil, nil, err
		}
		s.prepareConversationVM(conversation.ConversationID)
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

// beginConversationMutation is checkConversationRequest for an operation
// that must not overlap an action run (archive, delete, rename, cwd): until
// the returned done is called, actions on id are refused.
func (s *Server) beginConversationMutation(w http.ResponseWriter, r *http.Request, id, operation string) (done func(), ok bool) {
	s.workspace.mu.Lock()
	defer s.workspace.mu.Unlock()
	if !s.allowConversationMutation(w, r, id, operation) {
		return nil, false
	}
	if s.workspace.mutating == nil {
		s.workspace.mutating = make(map[string]int)
	}
	s.workspace.mutating[id]++
	return func() {
		s.workspace.mu.Lock()
		defer s.workspace.mu.Unlock()
		if s.workspace.mutating[id]--; s.workspace.mutating[id] == 0 {
			delete(s.workspace.mutating, id)
		}
	}, true
}

// /exit and exec-based upgrades do not pass through Serve's graceful
// shutdown. Refuse those restarts while a command is running, and fence
// new actions during the response flush/re-exec window.
func (s *Server) prepareRestart(ctx context.Context) error {
	s.workspace.mu.Lock()
	defer s.workspace.mu.Unlock()
	running, err := s.db.HasAnyRunningAction(ctx)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("%w: wait for running actions to finish before restarting Shelley", db.ErrConflict)
	}
	s.workspace.restarting = true
	return nil
}

func (s *Server) releaseRestart() {
	s.workspace.mu.Lock()
	s.workspace.restarting = false
	s.workspace.mu.Unlock()
}

// Caller holds workspace.mu, so action start and ordinary mutations
// cannot race past one another's preconditions.
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
	running, err := s.db.HasRunningAction(r.Context(), id)
	if err != nil {
		s.internalError(w, "Failed to check running action", err)
		return false
	}
	if running {
		http.Error(w, "An action is running; wait for it to finish before continuing", http.StatusConflict)
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

// All model-start paths call this while holding loopLifecycleMu. Action
// reservation uses that same lock, including starts from a late subagent or
// background completion rather than an HTTP chat request. Archiving a conversation
// with a VM deletes the VM, so it (and its subagents) start no turns until
// unarchived; work queued meanwhile waits.
func (cm *ConversationManager) checkProjectTurn(ctx context.Context) error {
	running, err := cm.db.HasRunningAction(ctx, cm.conversationID)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("%w: an action is running", db.ErrConflict)
	}
	if cm.toolSetConfig.VM == nil {
		return nil
	}
	vm, err := cm.db.GetConversationVM(ctx, cm.conversationID)
	if err != nil {
		return fmt.Errorf("load conversation VM: %w", err)
	}
	if err := conversationVMArchived(vm); err != nil {
		return err
	}
	// Copies a fresh VM after unarchiving, retries a failure, and restarts
	// a stopped SSH master.
	cm.prepareConversationVM()
	return nil
}

// workspaceService reads workspace instructions at each model request. Project
// edits and newly created projects are immediately available even in the
// long-lived Assistant and survive compaction without stale prompt copies.
// A VM conversation's (or its subagent's) also says where its work happens.
type workspaceService struct {
	llm.Service
	server         *Server
	conversationID string
	vm             bool
}

func (s *workspaceService) instructions(ctx context.Context) (string, error) {
	prompt, err := s.server.workspaceInstructions(ctx, s.conversationID)
	if err != nil || !s.vm {
		return prompt, err
	}
	vm, err := s.server.conversationVMPrompt(ctx, s.conversationID)
	return prompt + vm, err
}

func (s *workspaceService) request(ctx context.Context, request *llm.Request) (*llm.Request, error) {
	prompt, err := s.instructions(ctx)
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
// people write about a project: its name, the start of its instructions, and
// its repositories. Only the Assistant gets it, so a project edit changes no
// task's prompt.
func (s *Server) projectCatalog(ctx context.Context) ([]byte, error) {
	projects, err := s.db.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("load projects: %w", err)
	}
	type projectSummary struct {
		Name         string             `json:"name"`
		Instructions string             `json:"instructions,omitempty"`
		Repos        []string           `json:"repos,omitempty"`
		Cwd          string             `json:"cwd"`
		Actions      []db.ProjectAction `json:"actions"`
	}
	summaries := make([]projectSummary, 0, len(projects))
	for _, project := range projects {
		var repos []string
		for _, repo := range project.Repos {
			repos = append(repos, repo.Repository)
		}
		summaries = append(summaries, projectSummary{
			project.Name,
			truncateRunes(strings.TrimSpace(project.Instructions), 300), repos, project.Cwd, project.Actions,
		})
	}
	return json.Marshal(summaries)
}

const assistantRoutingPrompt = `You are the user's long-lived Assistant and task dispatcher, pinned above all projects. Answer quick questions, one-off research, and casual requests here. For example, answer "what's the weather today?" directly (use a real weather source).

For substantive project work, code changes, multi-step deliverables, or work that deserves its own history, CREATE A TASK in the best matching existing project using the API below, rather than doing the project work in this thread. For example, "please make the signup form validate email addresses" belongs in a project for the web app. You decide using the request's meaning and the live project catalog (names, instructions, repositories, directories, actions), not keywords alone. Each project is a profile: its tasks take the profile's settings (model, effort, tools), and the project's instructions, directory, repositories, and actions.

Before creating a task, inspect the workspace for an existing active task covering the same request; reuse it if appropriate rather than duplicating work. Every task needs a project. When no existing project is an obvious fit, create a new project for that kind of work, named and instructed so future related tasks belong there too, rather than placing the task in a catch-all; the user can also create one with the New Project button. Projects are agent-maintained: keep their names, instructions (the project's AGENTS.md), repositories, directories, and actions accurate and reusable; do not create a new project for every task.

Forward the full user request and all necessary context, constraints, and attachment paths to the task. Give it a short descriptive title. The task starts working immediately in its own conversation. Tell the user that you started it in one short plain-text message containing the task's full URL (see Task links below), not a markdown link or a bare path. Do not claim you completed the delegated work. Stay in the Assistant unless the user follows the task link.

Never invent a repository path or claim a remote operation succeeded without executing it. If a project directory is empty, the task should discover the correct environment before modifying files. Ask only when genuinely blocked. When creating or updating a project, configure its publish/apply/ship action (e.g. make release) with archive_on_success: true, so a successful finish archives the task; keep test/check actions (e.g. make test) non-archiving. Do not run a publish action just because you created a task; it is an explicit user-controlled finish operation.
`

const workspaceAPIPrompt = `
Manage projects and tasks using bash with the LOCAL Shelley HTTP API, using the transport and SHELLEY_API base specified above, not another instance or a guessed port. Use curl -fsS -H 'X-Shelley-Request: 1' -H 'Content-Type: application/json' (plus the socket flag when specified), with JSON encoded safely (e.g. python json.dumps to a file and --data-binary @file). Treat errors as errors. Never edit Shelley's SQLite tables directly.

GET $SHELLEY_API/api/workspace
  Returns assistant_id, projects, conversations (conversation_id, project, is_assistant, archived: the tasks), and recent action runs.
GET $SHELLEY_API/api/conversations
  Lists active conversations/tasks, including their ids, titles (slug), and working state. GET /api/conversation/ID retrieves messages for relevant context.
POST $SHELLEY_API/api/conversations/new
  Body: {"project":"Existing project name","title":"Short task title","message":"Full work request and context"}
  Returns conversation_id; work starts immediately.
POST $SHELLEY_API/api/conversation/ID/chat
  Body: {"message":"Follow-up instructions"}; send to an existing task only when appropriate.
POST $SHELLEY_API/api/projects
PUT $SHELLEY_API/api/projects/NAME
  Complete project body: {"name":"Project name","instructions":"Project instructions (AGENTS.md-style), appended to every task's prompt","cwd":"/absolute/real/project/path","repos":[{"integration":"GITHUB_INTEGRATION","repository":"OWNER/REPO"}],"actions":[{"id":"release","label":"make release","command":"make release","archive_on_success":true,"silent":false},{"id":"test","label":"make test","command":"make test","archive_on_success":false,"silent":false}],"vm":null}
  NAME is the project's name, URL-path-escaped (e.g. my%20project). A different name in a PUT body renames the project. Names are unique among profiles, at most 64 bytes, without "/". cwd can be empty when the environment is not known yet. Use an existing directory once discovered. Instructions are additive to the task's existing prompt; their start is what you route on, so lead with what the project is for. A project is a profile (GET $SHELLEY_API/api/profiles lists them, with "project": true): its settings (model, effort, tools, base system prompt) start as a copy of the default profile's, or of the profile named by "copy_settings_from" when creating; change them with PUT $SHELLEY_API/api/profiles/NAME (the complete settings). vm is null (tasks run on this host), {"base":"BASE_VM","integration":"EXE_DEV_INTEGRATION"} (each new task runs its tools and actions on its own VM copied from BASE_VM); cwd is then a directory on the VM. Browser runs on this host for VM tasks; files users upload stay on this host; tasks copy them to the VM with scp. An empty integration means this host's default exe.dev integration: the only one, or the one named after this VM (an error names the choices otherwise). repos are GitHub repositories this host reaches through exe.dev GitHub integrations (GET $SHELLEY_API/api/github-repos lists them, one per integration). Always send the project's current vm and repos back unchanged unless asked to change it; omitting it turns task VMs off. Enabling or changing vm needs the user's approval: it creates VMs on their account. Archiving a task on its own VM (by any route) deletes the VM Shelley created for it, losing uncommitted work there; unarchiving continues on a fresh copy of the base. Action ids are up to 64 letters, digits, "-" and "_". A run's result is sent to its task's agent unless the action is "silent". Keep actions deliberate and minimal: commands execute with the user's VM permissions, so do not add destructive cleanup or external side effects without authorization.
PUT $SHELLEY_API/api/conversation/ID/project
  Body: {"project":"Existing project name"}; moves an idle task, not the Assistant. Unless its settings were changed, it takes the new project's at its next turn.
DELETE $SHELLEY_API/api/projects/NAME
  Deletes the project and its profile. Refused (409) while the project has active tasks or a running action. Moves archived tasks to no project. Never deletes task history or repositories.
POST $SHELLEY_API/api/conversation/ID/actions/ACTION_ID
  Starts the project's configured command in the task directory; returns a run id. GET /api/action-runs/RUN_ID reports live output/status. Never manually archive while it runs: only a successful action archives, and errors/interruption leave the task visible. Do not fire-and-forget and report success.
`

// conversationVMPrompt tells a VM conversation (or its subagent) that its work happens on
// the VM, which its bash and patch calls target, while its tools run here.
// It amends what the base system prompt says about this machine and the
// working directory.
func (s *Server) conversationVMPrompt(ctx context.Context, id string) (string, error) {
	vm, err := s.db.GetConversationVM(ctx, id)
	if err != nil {
		return "", fmt.Errorf("load conversation VM: %w", err)
	}
	// Its project's actions run on the VM; one moved out of every project
	// (e.g. by deleting its project) has none.
	var actionList []db.ProjectAction
	metadata, err := s.db.GetWorkspaceConversation(ctx, vm.ConversationID)
	switch {
	case err == nil:
		project, err := s.db.GetProject(ctx, metadata.Project)
		if err != nil {
			return "", fmt.Errorf("load project: %w", err)
		}
		actionList = project.Actions
	case !errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("load conversation of VM %s: %w", vm.Name, err)
	}
	scratch, err := s.vmScratchDir(vm.ConversationID)
	if err != nil {
		return "", err
	}
	dir := vm.Dir
	web := "its HTTPS address, which this prompt gives once the VM is ready"
	if vm.HTTPSURL != "" {
		web = vm.HTTPSURL
	}
	workspace, err := s.db.WorkspaceMode(ctx)
	if err != nil {
		return "", err
	}
	this := "conversation"
	if workspace {
		this = "task"
	}
	where := fmt.Sprintf("its own exe.dev VM %q, a copy of %q", vm.Name, vm.Base)
	lifecycle := fmt.Sprintf("Archiving this %s deletes the VM, losing anything not pushed or published from it.", this)
	var actions string
	if len(actionList) > 0 {
		labels := make([]string, len(actionList))
		for i, action := range actionList {
			labels[i] = action.Label
		}
		actions = fmt.Sprintf("\n- The project's actions (%s) run on the VM, in %s, when the user starts one; they can't start during your turn, so to run one yourself, run its command with ssh_target %q.", strings.Join(labels, ", "), dir, vm.Name)
	}
	return fmt.Sprintf(`
<project_vm>
This %[7]s's work happens on %[1]s, in %[3]s. The agent loop and your tools run on the Shelley host, so every bash and patch call says which machine with ssh_target:
- ssh_target "%[2]s": runs over ssh on %[2]s. bash commands start in %[3]s (cd within a command to work elsewhere; change_dir moves only this host's directory), and patch paths are paths on %[2]s, relative ones resolving against %[3]s. Use it for ALL project work: reading and editing files, git, builds, tests, servers, package installs.
- ssh_target "localhost": runs here, on the Shelley host, which runs Shelley itself and keeps its data and other projects' files. Use it only for Shelley's API, copying files to or from %[2]s, and files for read_image, output_iframe, or message_user, which always read files here. Never do project work here, even where the same path exists on this host.

- Paths in the project instructions are VM paths. Read the repository's AGENTS.md (and similar guidance) on the VM before changing anything: guidance files and skills listed elsewhere in this prompt come from this host.
- Your working directory here, %[4]s, is this %[7]s's scratch space. To look at or send a VM file, copy it here first (ssh_target "localhost": `+"`scp %[2]s:FILE %[4]s/`"+`), then use read_image, output_iframe, or a message_user attachment. Files the user uploads arrive on this host (paths in their messages); copy them over with `+"`scp FILE %[2]s:DIR/`"+`. ssh, scp and rsync to %[2]s work from this host with no keys or options.
- The browser runs on this host. The user reaches the VM's web server on port 8000 at %[8]s, and port PORT at that address with :PORT, behind their exe.dev login, which the browser doesn't have; to browse a VM port, forward it (ssh_target "localhost": `+"`ssh -fN -L LOCALPORT:localhost:PORT %[2]s`"+`) and open http://localhost:LOCALPORT.
- Run long commands in bash's background mode, or in tmux on the VM.%[5]s
- While the VM is being created (a minute or two), ssh_target calls wait for it. If it can't be reached, the call's error says why: retry, or report it. Never fall back to doing the work on this host.
%[6]s
</project_vm>
`, where, vm.Name, dir, scratch, actions, lifecycle, this, web), nil
}

// writeProjectPrompt writes a project conversation's project instructions
// into out.
func writeProjectPrompt(out *strings.Builder, current *db.Project) {
	out.WriteString("<project_instructions>\n")
	out.WriteString(current.Instructions)
	out.WriteString("\n</project_instructions>\n")
	for _, repo := range current.Repos {
		fmt.Fprintf(out, "Project repository: %s (exe.dev GitHub integration %q).\n", repo.Repository, repo.Integration)
	}
	if current.Cwd == "" {
		out.WriteString("This project has no configured directory. Do not assume the initial working directory is its repository. Discover and configure the correct environment before editing files or running project commands.\n")
	}
}
