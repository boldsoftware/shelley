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

// ProjectVM makes a project run its conversations on exe.dev VMs: each on
// its own copy of Base, or all on the one existing SharedVM.
type ProjectVM struct {
	// Base is the VM each conversation's VM is copied from (exe.dev cp): it should
	// already hold the repository at the project's cwd, toolchains, and
	// warm caches. It is "" with SharedVM.
	Base string `json:"base"`
	// SharedVM, if set, is an existing VM every conversation in the project runs
	// on, concurrently, in its own working directory. Shelley never
	// creates, copies, or deletes it.
	SharedVM string `json:"shared_vm"`
	// Integration names the exe.dev integration that acts as the user
	// (<integration>.int.exe.xyz) to copy VMs and SSH into them.
	Integration string `json:"integration"`
	// SetupConversationID is the conversation preparing the base while Base is "".
	// It is read-only: Shelley starts that conversation when a project's VMs are
	// turned on without a base, and the conversation sets Base when it is done.
	SetupConversationID string `json:"setup_conversation_id"`
}

// ConversationVM is the VM a conversation runs on: its own copy of Base, or a shared VM.
type ConversationVM struct {
	ConversationID string `json:"conversation_id"`
	Name           string `json:"vm_name"`
	Base           string `json:"base_vm"` // "" for a shared VM
	Integration    string `json:"integration"`
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

// ValidVMName reports whether name is an exe.dev VM name.
func ValidVMName(name string) bool { return vmNamePattern.MatchString(name) }

func validateProjectVM(vm *ProjectVM, cwd string) error {
	if vm == nil {
		return nil
	}
	if vm.Base != "" && !vmNamePattern.MatchString(vm.Base) || vm.SharedVM != "" && !vmNamePattern.MatchString(vm.SharedVM) ||
		!vmNamePattern.MatchString(vm.Integration) {
		return fmt.Errorf("%w: vm.base (empty to have a setup conversation create one), vm.shared_vm, and vm.integration must be exe.dev names (lowercase letters, digits, hyphens; at most 42 bytes)", ErrInvalid)
	}
	if vm.Base != "" && vm.SharedVM != "" {
		return fmt.Errorf("%w: set vm.base (each conversation gets its own copy) or vm.shared_vm (all of them share one VM), not both", ErrInvalid)
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

func setProjectVM(tx *Tx, project string, vm *ProjectVM) error {
	if vm == nil {
		_, err := tx.Exec("DELETE FROM profile_vms WHERE profile=?", project)
		return err
	}
	_, err := tx.Exec(`INSERT INTO profile_vms(profile,base_vm,shared_vm,integration) VALUES(?,?,?,?)
		ON CONFLICT(profile) DO UPDATE SET base_vm=excluded.base_vm,shared_vm=excluded.shared_vm,integration=excluded.integration`,
		project, vm.Base, vm.SharedVM, vm.Integration)
	if err != nil || vm.NeedsBase() {
		return err
	}
	_, err = tx.Exec("DELETE FROM profile_vm_setups WHERE profile=?", project)
	return err
}

// setProjectVMSetup records conversationID as the conversation preparing the base VM
// of project, whose VMs must be on with no base or setup yet.
func setProjectVMSetup(tx *Tx, project, conversationID string) error {
	result, err := tx.Exec(`INSERT INTO profile_vm_setups(profile,conversation_id)
		SELECT profile,? FROM profile_vms WHERE profile=? AND base_vm='' AND shared_vm=''`, conversationID, project)
	if err := rowsChanged(result, err); errors.Is(err, sql.ErrNoRows) {
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
		conversation, err = generated.New(tx.Conn()).CreateConversation(ctx, generated.CreateConversationParams{
			ConversationID: id, UserInitiated: true, Cwd: cwd, Model: model, ConversationOptions: string(optsJSON),
		})
		if err != nil {
			return err
		}
		if setup {
			return setProjectVMSetup(tx, project.Name, id)
		}
		return insertConversationVM(tx, id, *project.VM, project.VMDir())
	})
	return &conversation, err
}

// insertConversationVM records the VM conversationID uses in a project with
// vm, working in dir there.
func insertConversationVM(tx *Tx, conversationID string, vm ProjectVM, dir string) error {
	if vm.NeedsBase() {
		return fmt.Errorf("%w: the project has no base VM yet", ErrConflict)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := tx.Exec(`INSERT INTO conversation_vms(conversation_id,vm_name,base_vm,integration,shared,status,dir,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, conversationID, ConversationVMName(vm, conversationID), vm.Base, vm.Integration, vm.SharedVM != "", ConversationVMPending, dir, now, now)
	return err
}

func attachProjectVMs(rx *Rx, projects []Project) error {
	rows, err := rx.Query(`SELECT v.profile,v.base_vm,v.shared_vm,v.integration,COALESCE(s.conversation_id,'')
		FROM profile_vms v LEFT JOIN profile_vm_setups s ON s.profile=v.profile`)
	if err != nil {
		return err
	}
	defer rows.Close()
	vms := map[string]*ProjectVM{}
	for rows.Next() {
		var name string
		var vm ProjectVM
		if err := rows.Scan(&name, &vm.Base, &vm.SharedVM, &vm.Integration, &vm.SetupConversationID); err != nil {
			return err
		}
		vms[name] = &vm
	}
	for i := range projects {
		projects[i].VM = vms[projects[i].Name]
	}
	return rows.Err()
}

// NeedsBase reports whether the project's conversations get their own VMs but it
// has no base yet, which a setup conversation prepares.
func (vm *ProjectVM) NeedsBase() bool {
	return vm.Base == "" && vm.SharedVM == ""
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
	if vm.SharedVM != "" {
		return vm.SharedVM
	}
	return vm.Base + "-" + strings.ToLower(conversationID)
}

// BindConversationVM records the VM a conversation in a VM project uses,
// working in dir there. The conversation keeps it, even if it later moves to
// another project.
func (db *DB) BindConversationVM(ctx context.Context, conversationID string, vm ProjectVM, dir string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		return insertConversationVM(tx, conversationID, vm, dir)
	})
}

const conversationVMColumns = `v.conversation_id,v.vm_name,v.base_vm,v.integration,v.shared,v.status,v.error,v.ssh_dest,v.https_url,
	v.vm_created_at,v.copy_requested_at,v.dir,COALESCE(c.archived,1),v.created_at,v.updated_at`

// conversationVMTables joins each VM to its conversation, which a deleted
// conversation lacks: it counts as archived, so its VM is deleted too.
const conversationVMTables = "conversation_vms v LEFT JOIN conversations c ON c.conversation_id=v.conversation_id"

func scanConversationVM(scanner rowScanner) (*ConversationVM, error) {
	var vm ConversationVM
	var created, updated string
	if err := scanner.Scan(&vm.ConversationID, &vm.Name, &vm.Base, &vm.Integration, &vm.Shared, &vm.Status, &vm.Error,
		&vm.SSHDest, &vm.HTTPSURL, &vm.VMCreatedAt, &vm.CopyRequestedAt, &vm.Dir, &vm.ConversationArchived, &created, &updated); err != nil {
		return nil, err
	}
	var err1, err2 error
	vm.CreatedAt, err1 = time.Parse(time.RFC3339Nano, created)
	vm.UpdatedAt, err2 = time.Parse(time.RFC3339Nano, updated)
	return &vm, errors.Join(err1, err2)
}

// GetConversationVM returns the VM of conversationID, or of the top-level
// conversation it is a subagent of: subagents share their conversation's VM. It
// returns sql.ErrNoRows for a conversation that runs locally.
func (db *DB) GetConversationVM(ctx context.Context, conversationID string) (*ConversationVM, error) {
	var vm *ConversationVM
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		vm, err = scanConversationVM(rx.QueryRow(`WITH RECURSIVE chain(id, depth) AS (
				SELECT ?, 0
				UNION ALL
				SELECT c.parent_conversation_id, chain.depth+1 FROM conversations c JOIN chain ON c.conversation_id=chain.id
				WHERE c.parent_conversation_id IS NOT NULL AND chain.depth < 64
			)
			SELECT `+conversationVMColumns+` FROM `+conversationVMTables+` JOIN chain ON v.conversation_id=chain.id
			ORDER BY chain.depth DESC LIMIT 1`, conversationID))
		return err
	})
	return vm, err
}

// ListConversationVMs returns every conversation VM.
func (db *DB) ListConversationVMs(ctx context.Context) (vms []ConversationVM, err error) {
	err = db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		vms, err = listConversationVMs(rx, false)
		return err
	})
	return vms, err
}

// listConversationVMs lists every conversation VM or, if current, those that
// exist or that an active conversation copies afresh at its next turn.
func listConversationVMs(rx *Rx, current bool) ([]ConversationVM, error) {
	out := []ConversationVM{}
	where := ""
	if current {
		where = " WHERE v.status!='" + ConversationVMDeleted + "' OR NOT COALESCE(c.archived,1)"
	}
	rows, err := rx.Query("SELECT " + conversationVMColumns + " FROM " + conversationVMTables + where + " ORDER BY v.created_at,v.conversation_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		vm, err := scanConversationVM(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *vm)
	}
	return out, rows.Err()
}

// UpdateConversationVM records the VM's status, error, addresses, identity,
// and directory.
func (db *DB) UpdateConversationVM(ctx context.Context, vm ConversationVM) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		result, err := tx.Exec(`UPDATE conversation_vms SET status=?,error=?,ssh_dest=?,https_url=?,vm_created_at=?,copy_requested_at=?,dir=?,updated_at=? WHERE conversation_id=?`,
			vm.Status, vm.Error, vm.SSHDest, vm.HTTPSURL, vm.VMCreatedAt, vm.CopyRequestedAt, vm.Dir, time.Now().UTC().Format(time.RFC3339Nano), vm.ConversationID)
		return rowsChanged(result, err)
	})
}
