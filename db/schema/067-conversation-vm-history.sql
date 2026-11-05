-- A conversation can move to another machine (PUT
-- /api/conversation/ID/vm). Its current VM is the row with replaced_at
-- empty; an own VM it left keeps its row, replaced_at set, until archiving
-- the conversation deletes it.
CREATE TABLE conversation_vms_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL,
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
    updated_at TEXT NOT NULL,
    shared BOOLEAN NOT NULL DEFAULT FALSE,
    replaced_at TEXT NOT NULL DEFAULT ''
);
INSERT INTO conversation_vms_new (conversation_id, vm_name, base_vm, integration, status, error, ssh_dest, https_url,
    vm_created_at, copy_requested_at, dir, created_at, updated_at, shared)
SELECT conversation_id, vm_name, base_vm, integration, status, error, ssh_dest, https_url,
    vm_created_at, copy_requested_at, dir, created_at, updated_at, shared
FROM conversation_vms ORDER BY created_at, conversation_id;
DROP TABLE conversation_vms;
ALTER TABLE conversation_vms_new RENAME TO conversation_vms;
CREATE UNIQUE INDEX conversation_vms_current ON conversation_vms(conversation_id) WHERE replaced_at = '';
CREATE UNIQUE INDEX conversation_vms_own_name ON conversation_vms(vm_name) WHERE NOT shared;
