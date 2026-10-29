-- Shared VMs: with shared_vm, all of a project's conversations run on that
-- one existing VM (base_vm is then ''), which Shelley never creates, copies
-- or deletes.
ALTER TABLE profile_vms ADD COLUMN shared_vm TEXT NOT NULL DEFAULT '';

-- A conversation on its project's shared VM, which many conversations use:
-- only own VMs' names are unique.
ALTER TABLE conversation_vms ADD COLUMN shared BOOLEAN NOT NULL DEFAULT FALSE;
DROP INDEX conversation_vms_own_name;
CREATE UNIQUE INDEX conversation_vms_own_name ON conversation_vms(vm_name) WHERE NOT shared;
