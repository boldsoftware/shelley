-- Base VM setup: a project's profile_vms.base_vm is '' while a setup
-- conversation prepares its base.

-- The conversation preparing a project's base VM; it sets base_vm when done.
CREATE TABLE profile_vm_setups (
    profile TEXT PRIMARY KEY REFERENCES profile_vms(profile) ON UPDATE CASCADE ON DELETE CASCADE,
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) ON DELETE CASCADE
);

-- Base VMs Shelley created for a project (POST /api/projects/{name}/base-vm).
-- Deleting the project deletes them, and only them: a base the user supplied
-- was never recorded here. status is 'creating' while exe.dev new runs (and
-- stays so if Shelley stopped meanwhile), then 'created' with the VM's
-- exe.dev creation time, which identifies it.
CREATE TABLE profile_base_vms (
    integration TEXT NOT NULL,
    vm_name TEXT NOT NULL,
    profile TEXT NOT NULL REFERENCES profiles(name) ON UPDATE CASCADE ON DELETE RESTRICT,
    status TEXT NOT NULL,
    vm_created_at TEXT NOT NULL DEFAULT '',
    requested_at TEXT NOT NULL,
    PRIMARY KEY (integration, vm_name)
);
CREATE INDEX profile_base_vms_profile ON profile_base_vms(profile);
