package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"shelley.exe.dev/db"
	"shelley.exe.dev/slug"
)

// Edit project shows the exe.dev account's integrations and the GitHub
// repositories its GitHub accounts can see, through an exe-dev integration
// acting as the user, and changes the account only when the user clicks.
// Integrations reach a project's VMs through the project's tag (projectTag):
// Shelley attaches them to the tag and tags the VMs the project's tasks run
// on: this host, the shared VM, or the base and its task VMs (copies keep
// their base's tags). A key that may not list or change the account (e.g. a
// tag-scoped one) gets exe.dev's refusal shown, and the UI links to exe.dev.
// All of it is workspace mode's (workspaceOnly).

// projectTagBase is the exe.dev tag a project gets from its name, which it
// keeps across renames once saved.
func projectTagBase(project string) string {
	name := slug.Sanitize(project)
	if len(name) > 40 {
		name = strings.TrimRight(name[:40], "-")
	}
	if name == "" {
		name = "project"
	}
	return "shelley-" + name
}

// projectPlacement is where a project's tasks run, as edited: mode "host"
// (this host), "own" (copies of base) or "shared" (shared_vm), with the
// exe-dev integration acting as the user ("" is this host's default).
type projectPlacement struct {
	Integration string `json:"integration"`
	Mode        string `json:"mode"`
	Base        string `json:"base"`
	SharedVM    string `json:"shared_vm"`
}

// projectTarget is what a placement resolves to.
type projectTarget struct {
	Integration string
	Project     string
	// Tag is the project's saved exe.dev tag, "" until the first change
	// saves it.
	Tag string
	// VMs the project's tasks run on now; none for VMs not copied from a
	// base yet, which gets the tag when created.
	VMs []string
}

func (s *Server) projectTarget(ctx context.Context, project string, p projectPlacement) (projectTarget, error) {
	if _, err := s.db.GetProject(ctx, project); err != nil {
		return projectTarget{}, err
	}
	t := projectTarget{Integration: p.Integration, Project: project}
	tag, saved, err := s.db.ProjectExeTag(ctx, project, projectTagBase(project))
	if err != nil {
		return t, err
	}
	if saved {
		t.Tag = tag
	}
	if t.Integration == "" {
		vm := db.ProjectVM{}
		if err := s.resolveProjectIntegration(ctx, &vm); err != nil {
			return t, err
		}
		t.Integration = vm.Integration
	}
	switch p.Mode {
	case "host":
		env, _, err := s.listIntegrations(ctx)
		if err != nil {
			return t, err
		}
		if env.VMName() == "" {
			return t, fmt.Errorf("%w: this host is not an exe.dev VM", db.ErrInvalid)
		}
		t.VMs = []string{env.VMName()}
	case "shared":
		if !db.ValidVMName(p.SharedVM) {
			return t, fmt.Errorf("%w: name the shared VM", db.ErrInvalid)
		}
		t.VMs = []string{p.SharedVM}
	case "own":
		if p.Base == "" {
			break
		}
		t.VMs = []string{p.Base}
		vms, err := s.db.ListConversationVMs(ctx)
		if err != nil {
			return t, err
		}
		for _, vm := range vms {
			if vm.Base == p.Base && !vm.Shared && !vm.ConversationArchived && vm.VMCreatedAt != "" {
				t.VMs = append(t.VMs, vm.Name)
			}
		}
	default:
		return t, fmt.Errorf("%w: mode must be host, own, or shared", db.ErrInvalid)
	}
	return t, nil
}

// exeAccountIntegration is an integration of the account, from
// `integrations list --json`, and whether it reaches the project's VMs.
type exeAccountIntegration struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Team         bool     `json:"team"`
	Title        string   `json:"title"`
	Comment      string   `json:"comment"`
	Repositories []string `json:"repositories"`
	Attachments  []string `json:"attachments"`
	// Added: attached to the project's tag.
	Added bool `json:"added"`
	// Reaches every VM the project's tasks run on now (with none yet,
	// Added).
	Reaches bool `json:"reaches"`
}

// exeAccountVM is a VM the project's tasks run on.
type exeAccountVM struct {
	Name   string   `json:"vm_name"`
	Found  bool     `json:"found"`
	Tags   []string `json:"tags"`
	Tagged bool     `json:"tagged"` // has the project's tag
}

type exeAccountView struct {
	Integration string `json:"integration"`
	// Tag is the project's exe.dev tag, "" until something is added.
	Tag          string                  `json:"tag"`
	VMs          []exeAccountVM          `json:"vms"`
	Integrations []exeAccountIntegration `json:"integrations"`
	// Error is why the integration can't show the account, e.g. exe.dev
	// refusing a restricted key.
	Error string `json:"error"`
	// ManageURL is where the user manages integrations on exe.dev's
	// website, "" where it isn't known.
	ManageURL string `json:"manage_url"`
}

// exeDevWebPage returns the URL of path on exe.dev's website, or "" where
// this exe.dev environment doesn't say where its website is.
func (s *Server) exeDevWebPage(ctx context.Context, path string) (string, error) {
	env, _, err := s.listIntegrations(ctx)
	if err != nil || env.WebURL() == "" {
		return "", err
	}
	return env.WebURL() + path, nil
}

// exeDevRefusal returns exe.dev's message when it refused a command, and
// err otherwise.
func exeDevRefusal(err error) (string, error) {
	if refusal, ok := errors.AsType[*exeDevError](err); ok {
		return refusal.Message, nil
	}
	return "", err
}

// exeAccountView lists the account's integrations and the target's VMs.
func (s *Server) exeAccountView(ctx context.Context, t projectTarget) (*exeAccountView, error) {
	view := &exeAccountView{Integration: t.Integration, Tag: t.Tag, VMs: []exeAccountVM{}, Integrations: []exeAccountIntegration{}}
	var err error
	if view.ManageURL, err = s.exeDevWebPage(ctx, "/integrations"); err != nil {
		return nil, err
	}
	vms, err := s.exeDevVMs(ctx, t)
	if err == nil {
		view.VMs = vms
		view.Integrations, err = s.exeAccountIntegrations(ctx, t, vms)
	}
	if err != nil {
		if view.Error, err = exeDevRefusal(err); err != nil {
			return nil, err
		}
	}
	return view, nil
}

func (s *Server) exeDevVMs(ctx context.Context, t projectTarget) ([]exeAccountVM, error) {
	vms := []exeAccountVM{}
	if len(t.VMs) == 0 {
		return vms, nil
	}
	out, err := s.exeDev(ctx, t.Integration, "ls --json")
	if err != nil {
		return nil, err
	}
	var list struct {
		VMs []exeDevVM `json:"vms"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("exe.dev ls: %w", err)
	}
	for _, name := range t.VMs {
		vm := exeAccountVM{Name: name, Tags: []string{}}
		for _, found := range list.VMs {
			if found.Name == name {
				vm.Found, vm.Tags = true, append(vm.Tags, found.Tags...)
			}
		}
		vm.Tagged = t.Tag != "" && slices.Contains(vm.Tags, t.Tag)
		vms = append(vms, vm)
	}
	return vms, nil
}

func (s *Server) exeAccountIntegrations(ctx context.Context, t projectTarget, vms []exeAccountVM) ([]exeAccountIntegration, error) {
	out, err := s.exeDev(ctx, t.Integration, "integrations list --json")
	if err != nil {
		return nil, err
	}
	var items []struct {
		Name         string   `json:"name"`
		Type         string   `json:"type"`
		Team         bool     `json:"isTeam"`
		Comment      string   `json:"comment"`
		CatalogTitle string   `json:"catalogTitle"`
		WireTitle    string   `json:"wireTitle"`
		Repositories []string `json:"repositories"`
		Attachments  []string `json:"attachments"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("exe.dev integrations list: %w", err)
	}
	list := []exeAccountIntegration{}
	for _, it := range items {
		ig := exeAccountIntegration{
			Name: it.Name, Type: it.Type, Team: it.Team, Title: it.CatalogTitle + it.WireTitle, Comment: it.Comment,
			Repositories: append([]string{}, it.Repositories...), Attachments: append([]string{}, it.Attachments...),
		}
		ig.Added = t.Tag != "" && slices.Contains(ig.Attachments, "tag:"+t.Tag)
		ig.Reaches = ig.Added || len(vms) > 0
		for _, vm := range vms {
			ig.Reaches = ig.Reaches && slices.ContainsFunc(ig.Attachments, func(a string) bool {
				return a == "auto:all" || a == "vm:"+vm.Name || strings.HasPrefix(a, "tag:") && slices.Contains(vm.Tags, a[len("tag:"):])
			})
		}
		list = append(list, ig)
	}
	return list, nil
}

// tagProjectVMs gives the project's VMs its tag.
func (s *Server) tagProjectVMs(ctx context.Context, t projectTarget) error {
	vms, err := s.exeDevVMs(ctx, t)
	if err != nil {
		return err
	}
	for _, vm := range vms {
		if !vm.Found {
			return fmt.Errorf("%w: VM %s not found", db.ErrInvalid, vm.Name)
		}
		if !vm.Tagged {
			if _, err := s.exeDev(ctx, t.Integration, fmt.Sprintf("tag %s %s --json", vm.Name, t.Tag)); err != nil {
				return err
			}
		}
	}
	return nil
}

var exeIntegrationName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func teamFlag(team bool) string {
	if team {
		return " --team"
	}
	return ""
}

// addProjectIntegration attaches integration name to the project's tag and
// tags the project's VMs.
func (s *Server) addProjectIntegration(ctx context.Context, t projectTarget, name string, team bool) error {
	if err := s.tagProjectVMs(ctx, t); err != nil {
		return err
	}
	_, err := s.exeDev(ctx, t.Integration, fmt.Sprintf("integrations attach %s tag:%s%s", name, t.Tag, teamFlag(team)))
	return err
}

var githubRepository = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// addProjectGithubRepo makes repository reach the project's VMs: through
// a GitHub integration that already does, an existing one it attaches to
// the project's tag, or a new one.
func (s *Server) addProjectGithubRepo(ctx context.Context, t projectTarget, repository string) (db.ProjectRepo, error) {
	repo := db.ProjectRepo{Repository: repository}
	vms, err := s.exeDevVMs(ctx, t)
	if err != nil {
		return repo, err
	}
	list, err := s.exeAccountIntegrations(ctx, t, vms)
	if err != nil {
		return repo, err
	}
	var covering []exeAccountIntegration
	taken := map[string]bool{}
	for _, ig := range list {
		taken[ig.Name] = true
		if ig.Type == "github" && slices.ContainsFunc(ig.Repositories, func(r string) bool { return strings.EqualFold(r, repository) }) {
			covering = append(covering, ig)
		}
	}
	// Prefer one that reaches the VMs, then one already added, then a
	// personal one.
	slices.SortStableFunc(covering, func(a, b exeAccountIntegration) int {
		rank := func(ig exeAccountIntegration) int {
			switch {
			case ig.Reaches:
				return 0
			case ig.Added:
				return 1
			case !ig.Team:
				return 2
			}
			return 3
		}
		return rank(a) - rank(b)
	})
	if len(covering) > 0 {
		ig := covering[0]
		repo.Integration = ig.Name
		switch {
		case ig.Reaches:
			return repo, nil
		case ig.Added:
			return repo, s.tagProjectVMs(ctx, t)
		}
		return repo, s.addProjectIntegration(ctx, t, ig.Name, ig.Team)
	}
	repo.Integration = githubIntegrationName(repository, taken)
	if err := s.tagProjectVMs(ctx, t); err != nil {
		return repo, err
	}
	_, err = s.exeDev(ctx, t.Integration, fmt.Sprintf("integrations add github --name=%s --repository=%s --attach=tag:%s --source=shelley --json", repo.Integration, repository, t.Tag))
	return repo, err
}

var nonNameChars = regexp.MustCompile(`[^a-z0-9]+`)

// githubIntegrationName names a new integration for repository "owner/repo"
// "owner-repo", or "owner-repo-2", … if taken.
func githubIntegrationName(repository string, taken map[string]bool) string {
	base := strings.Trim(nonNameChars.ReplaceAllString(strings.ToLower(repository), "-"), "-")
	if strings.HasPrefix(base, "exe-") || base == "github" || base == "" {
		base = "github-" + base
	}
	base = strings.TrimRight(base[:min(len(base), 56)], "-")
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// projectTagInUse returns the project's exe.dev tag if an integration is
// attached to it, so its new base needs the tag, else "". A project that
// never changed the account in Edit project, or a key that may not list
// integrations, has none.
func (s *Server) projectTagInUse(ctx context.Context, integration, project string) (string, error) {
	tag, saved, err := s.db.ProjectExeTag(ctx, project, projectTagBase(project))
	if err != nil || !saved {
		return "", err
	}
	list, err := s.exeAccountIntegrations(ctx, projectTarget{Integration: integration, Project: project, Tag: tag}, nil)
	if message, err := exeDevRefusal(err); err != nil || message != "" {
		return "", err
	}
	if !slices.ContainsFunc(list, func(ig exeAccountIntegration) bool { return ig.Added }) {
		return "", nil
	}
	return tag, nil
}

// githubAccount is a GitHub account connected to the exe.dev account, from
// `integrations setup github --repos --json`.
type githubAccount struct {
	Account        string `json:"account"`
	GithubLogin    string `json:"github_login"`
	InstallationID int64  `json:"installation_id"`
	Repos          []struct {
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
	} `json:"repos"`
	Error string `json:"error"`
}

// handleGithubAccounts serves GET /api/github-accounts?integration=NAME:
// the GitHub accounts connected to the exe.dev account, through exe-dev
// integration NAME ("" for this host's default). It needs no project, so
// onboarding lists repositories before creating one.
func (s *Server) handleGithubAccounts(w http.ResponseWriter, r *http.Request) {
	vm := db.ProjectVM{Integration: r.URL.Query().Get("integration")}
	if err := s.resolveProjectIntegration(r.Context(), &vm); err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	result := struct {
		Accounts []githubAccount `json:"accounts"`
		Error    string          `json:"error"`
		// SetupURL starts exe.dev's GitHub sign-in and app installation,
		// "" where exe.dev's website isn't known.
		SetupURL string `json:"setup_url"`
	}{}
	var err error
	if result.SetupURL, err = s.exeDevWebPage(r.Context(), "/github/setup"); err != nil {
		s.internalError(w, "Failed to list integrations", err)
		return
	}
	out, err := s.exeDev(r.Context(), vm.Integration, "integrations setup github --repos --json")
	if err == nil {
		err = json.Unmarshal(out, &result)
	}
	if err != nil {
		if result.Error, err = exeDevRefusal(err); err != nil {
			s.internalError(w, "Failed to list GitHub repositories", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func placementOf(r *http.Request) projectPlacement {
	q := r.URL.Query()
	return projectPlacement{Integration: q.Get("integration"), Mode: q.Get("mode"), Base: q.Get("base"), SharedVM: q.Get("shared_vm")}
}

func (s *Server) handleExeAccount(w http.ResponseWriter, r *http.Request) {
	t, err := s.projectTarget(r.Context(), r.PathValue("name"), placementOf(r))
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	view, err := s.exeAccountView(r.Context(), t)
	if err != nil {
		s.internalError(w, "Failed to list exe.dev integrations", err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleExeAccountChange makes one change the user clicked: add (attach)
// or remove (detach) an integration, tag the project's VMs, or add a
// GitHub repository, which returns the project repo to save.
func (s *Server) handleExeAccountChange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		projectPlacement
		Action     string `json:"action"`
		Name       string `json:"name"`
		Team       bool   `json:"team"`
		Repository string `json:"repository"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	ctx := r.Context()
	t, err := s.projectTarget(ctx, r.PathValue("name"), req.projectPlacement)
	if err == nil && (req.Action == "add" || req.Action == "remove") && !exeIntegrationName.MatchString(req.Name) {
		err = fmt.Errorf("%w: invalid integration name %q", db.ErrInvalid, req.Name)
	}
	if err == nil && req.Action == "github" && !githubRepository.MatchString(req.Repository) {
		err = fmt.Errorf("%w: repository must be owner/repo", db.ErrInvalid)
	}
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	if req.Action == "remove" && t.Tag == "" {
		s.workspaceHTTPError(w, fmt.Errorf("%w: nothing was added to the project", db.ErrInvalid))
		return
	}
	if t.Tag == "" {
		if t.Tag, err = s.db.SaveProjectExeTag(ctx, t.Project, projectTagBase(t.Project)); err != nil {
			s.internalError(w, "Failed to save the project's exe.dev tag", err)
			return
		}
	}
	var repo db.ProjectRepo
	switch req.Action {
	case "add":
		err = s.addProjectIntegration(ctx, t, req.Name, req.Team)
	case "remove":
		_, err = s.exeDev(ctx, t.Integration, fmt.Sprintf("integrations detach %s tag:%s%s", req.Name, t.Tag, teamFlag(req.Team)))
	case "tag":
		err = s.tagProjectVMs(ctx, t)
	case "github":
		repo, err = s.addProjectGithubRepo(ctx, t, req.Repository)
	default:
		err = fmt.Errorf("%w: action must be add, remove, tag, or github", db.ErrInvalid)
	}
	if message, unexpected := exeDevRefusal(err); unexpected != nil {
		s.workspaceHTTPError(w, unexpected)
		return
	} else if message != "" {
		http.Error(w, "exe.dev refused: "+message, http.StatusBadGateway)
		return
	}
	if req.Action == "github" {
		writeJSON(w, http.StatusOK, repo)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
