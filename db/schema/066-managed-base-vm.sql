-- Whether an agent builds the base a project's own VMs are copied from.
-- Where the conversations run still follows from the other columns: all on
-- shared_vm if it is set, else each on its own VM, copied from base_vm, or
-- new and empty while base_vm is '' and managed_base_vm is off.
ALTER TABLE profile_vms ADD COLUMN managed_base_vm BOOLEAN NOT NULL DEFAULT FALSE;

-- Before this, a project without a base or shared VM had an agent set one
-- up, and kept the base Shelley created for that setup conversation.
UPDATE profile_vms SET managed_base_vm = TRUE
WHERE shared_vm = '' AND (base_vm = '' OR EXISTS (
    SELECT 1 FROM profile_base_vms b
    WHERE b.profile = profile_vms.profile AND b.vm_name = profile_vms.base_vm
));
