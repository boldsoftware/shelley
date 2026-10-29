package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"

	"shelley.exe.dev/db"
	"shelley.exe.dev/slug"
)

// Base VM setup: turning a project's VMs on without a base (vm.base "")
// starts a setup conversation in that project. It runs here, not on a VM: it reads
// the project's local checkout, creates and prepares the base through the
// project's integration, then sets vm.base. Until then the project refuses
// new conversations rather than running them locally; the setup conversation is the only
// exception.

func (s *Server) exeDevIntegrationNames(ctx context.Context) ([]string, error) {
	env, integrations, err := s.listIntegrations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list exe.dev integrations: %w", err)
	}
	names := []string{}
	for _, integration := range exeDevIntegrationsOf(env, integrations) {
		names = append(names, integration.Name)
	}
	return names, nil
}

func (s *Server) handleExeDevIntegrations(w http.ResponseWriter, r *http.Request) {
	names, err := s.exeDevIntegrationNames(r.Context())
	if err != nil {
		s.internalError(w, "Failed to list exe.dev integrations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"integrations": names, "default": defaultExeDevIntegration(names)})
}

// resolveProjectIntegration fills in an empty vm.integration with this
// host's default exe.dev integration.
func (s *Server) resolveProjectIntegration(ctx context.Context, vm *db.ProjectVM) error {
	if vm == nil || vm.Integration != "" {
		return nil
	}
	names, err := s.exeDevIntegrationNames(ctx)
	if err != nil {
		return err
	}
	if vm.Integration = defaultExeDevIntegration(names); vm.Integration != "" {
		return nil
	}
	if len(names) == 0 {
		return fmt.Errorf("%w: set vm.integration: this host has no exe.dev integration to use", db.ErrInvalid)
	}
	return fmt.Errorf("%w: choose vm.integration: this host has several exe.dev integrations (%s)", db.ErrInvalid, strings.Join(names, ", "))
}

// startBaseVMSetup starts the project's setup conversation if its VMs are on with
// neither a base nor a setup conversation.
func (s *Server) startBaseVMSetup(r *http.Request, name string) error {
	s.baseVMSetupMu.Lock()
	defer s.baseVMSetupMu.Unlock()
	project, err := s.db.GetProject(r.Context(), name)
	if err != nil {
		return err
	}
	if project.VM == nil || !project.VM.NeedsBase() || project.VM.SetupConversationID != "" {
		return nil
	}
	recorder := httptest.NewRecorder()
	s.newConversation(recorder, r, NewConversationRequest{
		ChatRequest: ChatRequest{
			Message: fmt.Sprintf("Prepare a base VM for the %q project, so each of its conversations can work on its own copy. "+
				"Follow the base VM setup instructions in your system prompt.", project.Name),
		},
		Project: name,
		Title:   "Set up base VM for " + project.Name,
	}, true)
	if recorder.Code != http.StatusCreated {
		return fmt.Errorf("start base VM setup conversation: %d %s", recorder.Code, strings.TrimSpace(recorder.Body.String()))
	}
	return nil
}

// baseVMPending explains why a project without a base refuses new conversations.
func baseVMPending(project *db.Project) error {
	if id := project.VM.SetupConversationID; id != "" {
		return fmt.Errorf("%w: project %q is still setting up its base VM in conversation %s; start conversations there once it is ready", db.ErrConflict, project.Name, id)
	}
	return fmt.Errorf("%w: project %q runs conversations on VMs but has no base VM; set one, or save the project with an empty base VM to start a setup conversation", db.ErrConflict, project.Name)
}

// baseVMSetupCwd is where a setup conversation starts: the project's directory if
// this host has it (usually the checkout the base needs), else home.
func baseVMSetupCwd(cwd string) (string, error) {
	if info, err := os.Stat(cwd); err == nil && info.IsDir() {
		return cwd, nil
	}
	return os.UserHomeDir()
}

// baseVMName suggests a name for project's base VM, short enough to leave
// room for collision suffixes and conversation VM names (BASE-<conversation id>).
func baseVMName(project *db.Project) string {
	name := slug.Sanitize(project.Name)
	if len(name) > 24 {
		name = strings.TrimRight(name[:24], "-")
	}
	if name == "" {
		name = "project"
	}
	return name + "-base"
}

func (s *Server) baseVMSetupPrompt(ctx context.Context, project *db.Project) (string, error) {
	agent, err := s.agentSocket(ctx, project.VM.Integration)
	if err != nil {
		return "", err
	}
	host, err := s.exeDevIntegrationHost(ctx, project.VM.Integration)
	if err != nil {
		return "", err
	}
	repos, err := s.projectReposPrompt(ctx, project)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`<base_vm_setup>
This conversation prepares the base VM of project %[1]q. Each conversation in the project works on its own exe.dev VM copied (exe.dev cp) from the base, with the base's files, toolchains, caches, and tags. The project refuses new conversations until you set its vm.base. You run on the Shelley host, not on a VM. The user approved creating this base VM; never modify or delete any other VM.
1. Integration %[3]q acts as the user: `+"`curl https://%[8]s/`"+` says what it may do (e.g. a tag-scoped key reaches only VMs with its tag, so the base needs that tag), and `+"`curl -X POST https://%[8]s/exec -d 'COMMAND'`"+` runs an exe.dev CLI command (`+"`help new`"+` lists flags).
2. Name the VM %[4]q, or %[4]s-2, -3, … if `+"`ls NAME --json`"+` lists it already. Create it only through Shelley, which records it so deleting the project deletes it (never with the integration's `+"`new`"+`): POST $SHELLEY_API/api/projects/%[2]s/base-vm with {"name":"NAME","flags":["--cpu=4","--memory=16GB","--disk=50GB","--tag=TAG"]}, sized for the work, with the tags and integrations the work needs (the reflection-integration skill shows this host's; e.g. GitHub or CI integrations are usually granted through tags). Allowed flags: --cpu, --memory, --disk, --tag, --integration, --image, --allow-nested-virt. It returns the VM's ssh_dest and https_url.
3. SSH to its ssh_dest with the integration's ssh-agent: `+"`SSH_AUTH_SOCK=%[5]s ssh -o StrictHostKeyChecking=accept-new SSH_DEST`"+`. A new VM's SSH can take a few seconds to come up.
4. %[7]s
5. Find out what the work needs from the project's instructions, repositories, and actions, its directory %[6]q (a path on the base and every conversation VM), and any checkout at that path on this host: its real remote, guidance (AGENTS.md, README), setup/bootstrap scripts, and toolchain pins. Clone from real remotes, using credentials the VM gets through exe.dev integrations or tags. Install the toolchains, run the repositories' setup/bootstrap scripts, build once to warm caches, and check that each project action can run there (its command and prerequisites exist); run only actions without external side effects, never publishing (archive_on_success) ones.
6. Never copy private keys, tokens, or other secrets to the VM without the user's explicit approval in this conversation: ask and wait. Don't edit, commit, or push the repositories on this host or the VM: setup installs tools and warms caches, and the base's checkouts should match their remotes. If the work seems to need a code change, report it instead.
7. When the base is ready, GET $SHELLEY_API/api/workspace and PUT $SHELLEY_API/api/projects/%[2]s with the project's complete body and "vm":{"base":"NAME","integration":%[3]q}. That ends the setup; new conversations are then copied from the base.
8. Report briefly: the VM and its link (its https_url), what you set up and checked, and anything you could not set up and why (e.g. secrets left for the user). If you cannot finish, say why and leave vm.base unset; the user can switch the project's conversations to run on this host instead.
</base_vm_setup>
`, project.Name, url.PathEscape(project.Name), project.VM.Integration, baseVMName(project), agent, project.Cwd, repos, host), nil
}
