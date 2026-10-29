-- exe.dev VMs for a project's conversations: each conversation runs its
-- tools and actions on its own copy of base_vm.
CREATE TABLE profile_vms (
    profile TEXT PRIMARY KEY REFERENCES profiles(name) ON UPDATE CASCADE ON DELETE CASCADE,
    base_vm TEXT NOT NULL,
    integration TEXT NOT NULL
);

-- A conversation's VM, bound when it is created, and dir, its working
-- directory there (its project's then). Deliberately no foreign key to
-- conversations: the record outlives a deleted conversation so cleanup
-- deletes the VM. vm_created_at is the exe.dev creation time of the VM
-- Shelley copied for it, or empty if Shelley never created one; only a VM
-- with that name and creation time is ever deleted (on archive or
-- deletion). copy_requested_at is when Shelley last started copying it, so
-- a copy cut short by a restart is still recognized as Shelley's.
CREATE TABLE conversation_vms (
    conversation_id TEXT PRIMARY KEY,
    vm_name TEXT NOT NULL,
    base_vm TEXT NOT NULL,
    integration TEXT NOT NULL,
    status TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    ssh_dest TEXT NOT NULL DEFAULT '',
    https_url TEXT NOT NULL DEFAULT '',
    vm_created_at TEXT NOT NULL DEFAULT '',
    copy_requested_at TEXT NOT NULL DEFAULT '',
    dir TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX conversation_vms_own_name ON conversation_vms(vm_name);
