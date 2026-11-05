package db

import "testing"

// Migration 067 keeps every conversation's VM as its current one.
func TestConversationVMHistoryMigration(t *testing.T) {
	t.Parallel()
	database := setupDBMigratedThrough(t, 66)
	defer database.Close()
	ctx := t.Context()
	if err := database.pool.Exec(ctx, `INSERT INTO conversation_vms
		(conversation_id, vm_name, base_vm, integration, shared, status, vm_created_at, dir, created_at, updated_at) VALUES
		('c1', 'base-c1', 'base', 'i', 0, 'ready', '2026-01-02T03:04:05Z', '/r', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
		('c2', 'hub', '', 'i', 1, 'ready', '', '/r', '2026-01-01T00:00:01Z', '2026-01-01T00:00:01Z')`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2"} {
		vm, err := database.GetConversationVM(ctx, id)
		if err != nil || vm.ID == 0 || vm.Replaced || vm.Status != ConversationVMReady {
			t.Fatalf("%s: %+v %v", id, vm, err)
		}
	}
	// Still one current VM per conversation.
	if err := database.pool.Exec(ctx, `INSERT INTO conversation_vms
		(conversation_id, vm_name, base_vm, integration, status, created_at, updated_at)
		VALUES ('c1', 'other', '', 'i', 'pending', '', '')`); err == nil {
		t.Fatal("a second current VM was accepted")
	}
}
