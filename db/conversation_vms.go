package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"shelley.exe.dev/db/generated"
)

// ProjectVM makes a project run its conversations off this host: all on
// SharedVM if it is set, else each on its own exe.dev VM, copied from Base,
// or new and empty while Base is "" and ManagedBaseVM is off.
type ProjectVM struct {
	// Base is the VM each conversation's own VM is copied from (exe.dev cp):
	// it should already hold the repository at the project's cwd,
	// toolchains, and warm caches. It is "" with SharedVM.
	Base string `json:"base"`
	// ManagedBaseVM has an agent build Base: while it is "", a setup
	// conversation prepares one and the project starts no conversations.
	// It is false with SharedVM.
	ManagedBaseVM bool `json:"managed_base_vm"`
	// SharedVM, if set, is the existing VM every conversation in the
	// project runs on, concurrently, all starting in the project's
	// directory. Shelley never creates, copies, or deletes it. Without an
	// Integration, it is an ssh destination ([user@]host or a ~/.ssh/config
	// alias) reached with this host's own SSH configuration, keys, and agent.
	SharedVM string `json:"shared_vm"`
	// Integration names the exe.dev integration that acts as the user
	// (<integration>.int.exe.xyz) to copy VMs and SSH into them; "" for
	// an ssh destination.
	Integration string `json:"integration"`
	// SetupConversationID is the conversation preparing a ManagedBaseVM
	// project's base while Base is "". It is read-only: Shelley starts that
	// conversation when the project has no base, and the conversation sets
	// Base when it is done.
	SetupConversationID string `json:"setup_conversation_id"`
}

// ConversationVM is the VM a conversation runs on: its own copy of Base, or a shared VM.
type ConversationVM struct {
	ConversationID string `json:"conversation_id"`
	Name           string `json:"vm_name"`
	Base           string `json:"base_vm"` // "" for a shared or empty VM
	// Integration is "" for an SSH host (see ProjectVM.Integration), reached
	// with this host's own SSH configuration.
	Integration string `json:"integration"`
	// Shared marks a project's shared VM, used by many conversations: Shelley
	// never creates or deletes it, and archiving leaves it alone.
	Shared bool `json:"shared"`
	// Status is pending until the VM is reachable, then ready; failed
	// records Error, and the next use retries. Archiving the conversation makes it
	// deleting (Error records a failed attempt, which is retried) until the
	// VM is deleted, or kept if it is not the VM Shelley created (Error says
	// why). The next use after unarchiving copies a fresh VM.
	Status   string `json:"status"`
	Error    string `json:"error"`
	SSHDest  string `json:"ssh_dest"`
	HTTPSURL string `json:"https_url"`
	// VMCreatedAt is the exe.dev creation time of the VM Shelley copied for
	// the conversation, or "" if Shelley has not created it. Cleanup deletes only
	// the VM with this name and creation time.
	VMCreatedAt string `json:"vm_created_at"`
	// CopyRequestedAt is when Shelley last started copying the VM, or "": a
	// copy cut short (e.g. by a restart) that exe.dev completed is Shelley's
	// if the VM was created after it.
	CopyRequestedAt string `json:"-"`
	// Dir is the working directory on the VM: its project's when the
	// conversation was created.
	Dir string `json:"dir"`
	// ConversationArchived reports whether the conversation owning the VM is
	// archived, or deleted.
	ConversationArchived bool      `json:"conversation_archived"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// Conversation VM statuses.
const (
	ConversationVMPending  = "pending"
	ConversationVMReady    = "ready"
	ConversationVMFailed   = "failed"
	ConversationVMDeleting = "deleting"
	ConversationVMDeleted  = "deleted"
	ConversationVMKept     = "kept"
)

var vmNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,40}[a-z0-9])?$`)

// SSHHost reports whether vm is an SSH host (see ProjectVM.Integration),
// reached with this host's own SSH configuration rather than through exe.dev.
func (vm *ConversationVM) SSHHost() bool { return vm.Integration == "" }

// sshDestPattern is an ssh destination that can't be mistaken for an option
// or carry a port or path: [user@]host, the host a name, alias, or IPv4 address.
var sshDestPattern = regexp.MustCompile(`^([A-Za-z0-9._][A-Za-z0-9._-]*@)?[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// ValidVMName reports whether name is an exe.dev VM name.
func ValidVMName(name string) bool { return vmNamePattern.MatchString(name) }

func validateProjectVM(vm *ProjectVM, cwd string) error {
	if vm == nil {
		return nil
	}
	if vm.SharedVM != "" && (vm.Base != "" || vm.ManagedBaseVM) {
		return fmt.Errorf("%w: vm.shared_vm runs every conversation on that one machine, so it takes no vm.base or vm.managed_base_vm", ErrInvalid)
	}
	switch {
	case vm.Integration == "" && vm.SharedVM == "":
		return fmt.Errorf("%w: vm.integration must name the exe.dev integration for conversations' own VMs (only a vm.shared_vm ssh destination has none)", ErrInvalid)
	case vm.Integration == "":
		if len(vm.SharedVM) > 255 || !sshDestPattern.MatchString(vm.SharedVM) {
			return fmt.Errorf("%w: without vm.integration, vm.shared_vm must be an ssh destination, [user@]host (set a port or other options in ~/.ssh/config)", ErrInvalid)
		}
	case vm.Base != "" && !vmNamePattern.MatchString(vm.Base) || vm.SharedVM != "" && !vmNamePattern.MatchString(vm.SharedVM) ||
		!vmNamePattern.MatchString(vm.Integration):
		return fmt.Errorf("%w: vm.base, vm.shared_vm, and vm.integration must be exe.dev names (lowercase letters, digits, hyphens; at most 42 bytes)", ErrInvalid)
	}
	if len(vm.Base) > maxBaseVMName {
		return fmt.Errorf("%w: vm.base must be at most %d bytes, leaving room for each conversation's VM name (BASE-<conversation id>)", ErrInvalid, maxBaseVMName)
	}
	if !strings.HasPrefix(cwd, "/") {
		return fmt.Errorf("%w: a project with a VM needs an absolute cwd on that VM", ErrInvalid)
	}
	return nil
}

// maxBaseVMName leaves room in a VM name for "-" and a conversation id.
const maxBaseVMName = 42 - 8

func setProjectVM(ctx context.Context, q *generated.Queries, project string, vm *ProjectVM) error {
	if vm == nil {
		return q.DeleteProjectVM(ctx, project)
	}
	if err := q.UpsertProjectVM(ctx, generated.UpsertProjectVMParams{
		Profile: project, BaseVm: vm.Base, SharedVm: vm.SharedVM, ManagedBaseVm: vm.ManagedBaseVM, Integration: vm.Integration,
	}); err != nil || vm.NeedsBase() {
		return err
	}
	return q.DeleteProjectVMSetup(ctx, project)
}

// setProjectVMSetup records conversationID as the conversation preparing the base VM
// of project, whose VMs must be on with no base or setup yet.
func setProjectVMSetup(ctx context.Context, q *generated.Queries, project, conversationID string) error {
	err := rowsChanged(q.CreateProjectVMSetup(ctx, generated.CreateProjectVMSetupParams{ConversationID: conversationID, Profile: project}))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: project %s no longer needs a base VM set up", ErrConflict, project)
	}
	return err
}

// CreateVMProjectConversation creates a top-level conversation in project, a
// VM project, in one transaction with either its VM, working in the
// project's directory there, or, for setup, the record that it is the
// project's base VM setup conversation.
func (db *DB) CreateVMProjectConversation(ctx context.Context, cwd, model *string, opts ConversationOptions, project *Project, setup bool) (*generated.Conversation, error) {
	id, err := GenerateConversationID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate conversation ID: %w", err)
	}
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal conversation options: %w", err)
	}
	var conversation generated.Conversation
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		conversation, err = q.CreateConversation(ctx, generated.CreateConversationParams{
			ConversationID: id, UserInitiated: true, Cwd: cwd, Model: model, ConversationOptions: string(optsJSON),
		})
		if err != nil {
			return err
		}
		if setup {
			return setProjectVMSetup(ctx, q, project.Name, id)
		}
		return insertConversationVM(ctx, q, id, *project.VM, project.VMDir())
	})
	return &conversation, err
}

// insertConversationVM records the VM conversationID uses in a project with
// vm, working in dir there.
func insertConversationVM(ctx context.Context, q *generated.Queries, conversationID string, vm ProjectVM, dir string) error {
	if vm.NeedsBase() {
		return fmt.Errorf("%w: the project has no base VM yet", ErrConflict)
	}
	now := formatTime(time.Now())
	return q.CreateConversationVM(ctx, generated.CreateConversationVMParams{
		ConversationID: conversationID, VmName: ConversationVMName(vm, conversationID), BaseVm: vm.Base, Integration: vm.Integration,
		Shared: vm.SharedVM != "", Status: ConversationVMPending, Dir: dir, CreatedAt: now, UpdatedAt: now,
	})
}

func attachProjectVMs(ctx context.Context, q *generated.Queries, projects []Project) error {
	rows, err := q.ListProjectVMs(ctx)
	if err != nil {
		return err
	}
	vms := map[string]*ProjectVM{}
	for _, row := range rows {
		vms[row.Profile] = &ProjectVM{Base: row.BaseVm, SharedVM: row.SharedVm, ManagedBaseVM: row.ManagedBaseVm, Integration: row.Integration, SetupConversationID: row.SetupConversationID}
	}
	for i := range projects {
		projects[i].VM = vms[projects[i].Name]
	}
	return nil
}

// NeedsBase reports whether the project has a ManagedBaseVM but no base yet,
// which a setup conversation prepares.
func (vm *ProjectVM) NeedsBase() bool {
	return vm.ManagedBaseVM && vm.Base == "" && vm.SharedVM == ""
}

// Empty reports whether each conversation gets its own new, empty VM.
func (vm *ProjectVM) Empty() bool {
	return vm.Base == "" && vm.SharedVM == "" && !vm.ManagedBaseVM
}

// VMDir is where a VM project's work happens on its VMs.
func (p *Project) VMDir() string {
	if p.Cwd == "" {
		return "/home/exedev"
	}
	return p.Cwd
}

// ConversationVMName is the VM name a conversation in a project with vm gets.
func ConversationVMName(vm ProjectVM, conversationID string) string {
	switch {
	case vm.SharedVM != "":
		return vm.SharedVM
	case vm.Base == "":
		return "shelley-" + strings.ToLower(conversationID)
	}
	return vm.Base + "-" + strings.ToLower(conversationID)
}

// BindConversationVM records the VM a conversation in a VM project uses,
// working in dir there. The conversation keeps it, even if it later moves to
// another project.
func (db *DB) BindConversationVM(ctx context.Context, conversationID string, vm ProjectVM, dir string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		return insertConversationVM(ctx, generated.New(tx.Conn()), conversationID, vm, dir)
	})
}

func newConversationVM(row generated.ConversationVm, archived bool) (*ConversationVM, error) {
	vm := ConversationVM{
		ConversationID: row.ConversationID, Name: row.VmName, Base: row.BaseVm, Integration: row.Integration, Shared: row.Shared,
		Status: row.Status, Error: row.Error, SSHDest: row.SshDest, HTTPSURL: row.HttpsUrl, VMCreatedAt: row.VmCreatedAt,
		CopyRequestedAt: row.CopyRequestedAt, Dir: row.Dir, ConversationArchived: archived,
	}
	var err1, err2 error
	vm.CreatedAt, err1 = parseTime(row.CreatedAt)
	vm.UpdatedAt, err2 = parseTime(row.UpdatedAt)
	return &vm, errors.Join(err1, err2)
}

// GetConversationVM returns the VM of conversationID, or of the top-level
// conversation it is a subagent of: subagents share their conversation's VM. It
// returns sql.ErrNoRows for a conversation that runs locally.
func (db *DB) GetConversationVM(ctx context.Context, conversationID string) (*ConversationVM, error) {
	var vm *ConversationVM
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		row, err := generated.New(rx.Conn()).GetConversationVM(ctx, conversationID)
		if err != nil {
			return err
		}
		vm, err = newConversationVM(row.ConversationVm, row.ConversationArchived)
		return err
	})
	return vm, err
}

// ListConversationVMs returns every conversation VM.
func (db *DB) ListConversationVMs(ctx context.Context) (vms []ConversationVM, err error) {
	err = db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		vms, err = listConversationVMs(ctx, generated.New(rx.Conn()), false)
		return err
	})
	return vms, err
}

// listConversationVMs lists every conversation VM or, if current, those that
// exist or that an active conversation copies afresh at its next turn.
func listConversationVMs(ctx context.Context, q *generated.Queries, current bool) ([]ConversationVM, error) {
	var rows []generated.ListConversationVMsRow
	var err error
	if current {
		var currentRows []generated.ListCurrentConversationVMsRow
		currentRows, err = q.ListCurrentConversationVMs(ctx)
		for _, row := range currentRows {
			rows = append(rows, generated.ListConversationVMsRow(row))
		}
	} else {
		rows, err = q.ListConversationVMs(ctx)
	}
	if err != nil {
		return nil, err
	}
	out := []ConversationVM{}
	for _, row := range rows {
		vm, err := newConversationVM(row.ConversationVm, row.ConversationArchived)
		if err != nil {
			return nil, err
		}
		out = append(out, *vm)
	}
	return out, nil
}

// UpdateConversationVM records the VM's status, error, addresses, identity,
// and directory.
func (db *DB) UpdateConversationVM(ctx context.Context, vm ConversationVM) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		return rowsChanged(generated.New(tx.Conn()).UpdateConversationVM(ctx, generated.UpdateConversationVMParams{
			Status: vm.Status, Error: vm.Error, SshDest: vm.SSHDest, HttpsUrl: vm.HTTPSURL, VmCreatedAt: vm.VMCreatedAt,
			CopyRequestedAt: vm.CopyRequestedAt, Dir: vm.Dir, UpdatedAt: formatTime(time.Now()), ConversationID: vm.ConversationID,
		}))
	})
}
