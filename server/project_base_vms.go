package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/slug"
)

// Deleting a project deletes the base VMs Shelley created for it, and only
// those: the setup conversation creates the base through handleCreateProjectBaseVM,
// which records the VM before creating it. A base the user supplied and
// conversation VMs (archiving deletes those) are never touched here.
// Deletion stops a running setup conversation (and waits out a creation in flight),
// then archives it; archived conversations keep their history, in no project. Active
// conversations block deletion: they may hold uncommitted work on their VMs.

// baseVMFlag allows the exe.dev new flags a base needs (see `help new`):
// sizing, tags and integrations (which grant its access), and image. Values
// are plain words, so a flag can't smuggle in more of the command.
var baseVMFlag = regexp.MustCompile(`^--(cpu|memory|disk|tag|integration|image|allow-nested-virt)(=[A-Za-z0-9._:/@,+-]+)?$`)

// projectBaseVMLock serializes creating a project's base VMs with deleting
// them. VM names have no "/", so the key can't collide with a VM's lock.
func (s *Server) projectBaseVMLock(project string) func() {
	lock := s.conversationVMLock("base-vms/" + project)
	lock.Lock()
	return lock.Unlock
}

// projectDeleting refuses changes that would add work to a project being
// deleted. The caller holds workspace.mu.
func (s *Server) projectDeleting(project string) error {
	if s.workspace.deleting[project] {
		return fmt.Errorf("%w: project %s is being deleted", db.ErrConflict, project)
	}
	return nil
}

func (s *Server) handleCreateProjectBaseVM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string   `json:"name"`
		Flags []string `json:"flags"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	if !db.ValidVMName(req.Name) {
		http.Error(w, "name must be an exe.dev VM name (lowercase letters, digits, hyphens; at most 42 bytes)", http.StatusBadRequest)
		return
	}
	for _, flag := range req.Flags {
		if !baseVMFlag.MatchString(flag) {
			http.Error(w, fmt.Sprintf("flag %q not allowed: use --cpu, --memory, --disk, --tag, --integration, --image, or --allow-nested-virt with a plain value", flag), http.StatusBadRequest)
			return
		}
	}
	id := r.PathValue("name")
	s.workspace.mu.Lock()
	err := s.projectDeleting(id)
	s.workspace.mu.Unlock()
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	// A deletion that starts meanwhile waits for this creation, then deletes
	// the VM; once it is done, the project is gone.
	defer s.projectBaseVMLock(id)()
	project, err := s.db.GetProject(r.Context(), id)
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	if project.VM == nil || !project.VM.NeedsBase() {
		s.workspaceHTTPError(w, fmt.Errorf("%w: project %q needs no base VM created: its VMs are off or already have a base", db.ErrConflict, project.Name))
		return
	}
	// Once asked, creation runs to its end, even if the caller goes away
	// (e.g. deletion stops the setup conversation), so its outcome is recorded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
	defer cancel()
	vm, err := s.createProjectBaseVM(ctx, project, req.Name, req.Flags)
	s.notifyWorkspaceChanged()
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, vm)
}

// createProjectBaseVM creates project's base VM name, recording it first so
// deleting the project deletes it whatever happens next.
func (s *Server) createProjectBaseVM(ctx context.Context, project *db.Project, name string, flags []string) (*exeDevVM, error) {
	integration := project.VM.Integration
	found, err := s.findExeDevVM(ctx, integration, name)
	if err != nil {
		return nil, err
	}
	if found != nil {
		return nil, fmt.Errorf("%w: VM %s already exists; choose another name", db.ErrConflict, name)
	}
	record := db.ProjectBaseVM{Project: project.Name, Name: name, Integration: integration, Status: db.ProjectBaseVMCreating, RequestedAt: time.Now()}
	if err := s.db.RecordProjectBaseVM(ctx, record); err != nil {
		return nil, err
	}
	command := fmt.Sprintf("new --name=%s --comment='Shelley base VM for project %s' --json", name, slug.Sanitize(project.Name))
	if len(flags) > 0 {
		command += " " + strings.Join(flags, " ")
	}
	_, newErr := s.exeDev(ctx, integration, command)
	if found, err = s.findExeDevVM(ctx, integration, name); err != nil {
		return nil, errors.Join(newErr, err) // stays creating; deletion looks again
	}
	if found == nil {
		if newErr == nil {
			newErr = fmt.Errorf("exe.dev new %s reported success, but the VM is not listed", name)
		}
		return nil, errors.Join(newErr, s.db.ForgetProjectBaseVM(ctx, integration, name))
	}
	if found.CreatedAt == "" {
		return nil, errors.Join(newErr, fmt.Errorf("exe.dev ls %s lists no creation time", name))
	}
	record.Status, record.VMCreatedAt = db.ProjectBaseVMCreated, found.CreatedAt
	if err := s.db.RecordProjectBaseVM(ctx, record); err != nil {
		return nil, err
	}
	if newErr != nil {
		return nil, fmt.Errorf("%w (VM %s exists and is recorded; deleting the project deletes it)", newErr, name)
	}
	s.logger.Info("Created base VM", "project", project.Name, "vm", name)
	return found, nil
}

// deleteProjectBaseVM deletes vm if it is the VM Shelley created, returning
// why it kept it otherwise.
func (s *Server) deleteProjectBaseVM(ctx context.Context, vm db.ProjectBaseVM) (kept string, err error) {
	found, err := s.findExeDevVM(ctx, vm.Integration, vm.Name)
	if err != nil || found == nil {
		return "", err
	}
	switch vm.Status {
	case db.ProjectBaseVMCreated:
		if found.CreatedAt != vm.VMCreatedAt {
			return fmt.Sprintf("it was created at %s, not at %s when Shelley created it", found.CreatedAt, vm.VMCreatedAt), nil
		}
	default:
		// Shelley stopped while creating it. It did not exist just before
		// (allow for clock skew between here and exe.dev).
		created, err := time.Parse(time.RFC3339, found.CreatedAt)
		if err != nil || created.Before(vm.RequestedAt.Add(-time.Minute)) {
			return fmt.Sprintf("it was created at %q, before Shelley asked to create it at %s", found.CreatedAt, vm.RequestedAt.UTC().Format(time.RFC3339)), nil
		}
	}
	if _, err := s.exeDev(ctx, vm.Integration, "rm "+vm.Name+" --json"); err != nil {
		return "", err
	}
	if found, err := s.findExeDevVM(ctx, vm.Integration, vm.Name); err != nil || found != nil {
		return "", errors.Join(err, fmt.Errorf("exe.dev rm %s reported success, but the VM is still listed", vm.Name))
	}
	s.logger.Info("Deleted project's base VM", "project", vm.Project, "vm", vm.Name)
	return "", nil
}

type keptVM struct {
	Name   string `json:"vm_name"`
	Reason string `json:"reason"`
}
