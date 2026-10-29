package server

import (
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

// newSharedVMProject is a project whose conversations all run on shared VM "hub".
func newSharedVMProject(t *testing.T) (*Server, *db.DB, string, *fakeSSH, *fakeExeDev) {
	t.Helper()
	s, database, _, _ := newActionFixture(t, "true", false)
	ssh := newFakeSSH(t, s)
	exe := &fakeExeDev{vms: map[string]string{"hub": "2026-01-01T00:00:00Z"}}
	s.conversationVMs.exe = exe.exec
	created := workspaceResponse[db.Project](t, workspaceAPI(s, "POST", "/api/projects",
		`{"name":"Hub","cwd":"/home/exedev/hub","vm":{"shared_vm":"hub","integration":"me-exe"}}`), 201)
	if created.VM == nil || *created.VM != (db.ProjectVM{SharedVM: "hub", Integration: "me-exe"}) {
		t.Fatalf("shared VM not persisted: %+v", created.VM)
	}
	return s, database, created.Name, ssh, exe
}

func TestSharedConversationVM(t *testing.T) {
	t.Parallel()
	s, database, project, ssh, exe := newSharedVMProject(t)
	var ids []string
	for range 2 {
		conversation, release, err := s.createProjectConversation(t.Context(), nil, nil, db.ConversationOptions{Profile: project}, project, false)
		if err != nil {
			t.Fatal(err)
		}
		release()
		ids = append(ids, conversation.ConversationID)
	}
	waitForActions(t, s)
	// One SSH master serves every conversation on the VM.
	if masters := ssh.calls(t, "master"); len(masters) != 1 || masters[0] != "hub" {
		t.Fatalf("shared VM masters: %v", masters)
	}
	for _, id := range ids {
		vm := conversationVMOf(t, database, id)
		if vm.Name != "hub" || !vm.Shared || vm.Base != "" {
			t.Fatalf("conversation %s VM: %+v", id, vm)
		}
	}
	// No base VM setup for a shared VM.
	got, err := database.GetProject(t.Context(), project)
	if err != nil || got.VM.SetupConversationID != "" {
		t.Fatalf("shared project started a setup: %+v %v", got.VM, err)
	}
	prompt, err := conversationPrompt(t, s, ids[0])
	if err != nil || !strings.Contains(prompt, "SHARES") || !strings.Contains(prompt, `ssh_target "hub": runs over ssh on hub. bash commands start in /home/exedev/hub`) {
		t.Fatalf("shared conversation prompt: %v\n%s", err, prompt)
	}

	// Archiving never deletes or blocks the shared VM.
	archiveConversation(t, s, ids[0], "archive")
	s.cleanupConversationVMs(t.Context())
	if vm := conversationVMOf(t, database, ids[0]); vm.Status == db.ConversationVMDeleting || vm.Status == db.ConversationVMDeleted || len(exe.removed()) != 0 {
		t.Fatalf("archiving touched the shared VM: %+v %v", vm, exe.removed())
	}
	// But its conversation, no longer in the SSH configuration, works on
	// it only once unarchived.
	if _, err := s.prepareConversationVMNow(t.Context(), ids[0]); err == nil || !strings.Contains(err.Error(), "unarchive it to continue on the shared VM hub") {
		t.Fatalf("archived shared conversation's VM prepared: %v", err)
	}
	if _, _, err := s.deleteConversationVM(t.Context(), conversationVMOf(t, database, ids[1])); err == nil {
		t.Fatal("deleteConversationVM accepted a shared VM")
	}

	for _, body := range []string{
		`{"name":"Bad","cwd":"/r","vm":{"base":"b","shared_vm":"hub","integration":"me-exe"}}`,
		`{"name":"Bad","cwd":"/r","vm":{"shared_vm":"Hub_VM","integration":"me-exe"}}`,
	} {
		if response := workspaceAPI(s, "POST", "/api/projects", body); response.Code != 400 {
			t.Errorf("%s: %d %s", body, response.Code, response.Body.String())
		}
	}
}

// A shared VM is used as it is: never created, so a missing one fails.
func TestSharedConversationVMIsNeverCreated(t *testing.T) {
	t.Parallel()
	s, database, project, _, exe := newSharedVMProject(t)
	delete(exe.vms, "hub")
	conversation, release, err := s.createProjectConversation(t.Context(), nil, nil, db.ConversationOptions{Profile: project}, project, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	waitForActions(t, s)
	vm := conversationVMOf(t, database, conversation.ConversationID)
	if vm.Status != db.ConversationVMFailed || !strings.Contains(vm.Error, "never creates") {
		t.Fatalf("missing shared VM: %+v", vm)
	}
	for _, c := range exe.commands {
		if !strings.HasPrefix(c, "ls ") {
			t.Fatalf("shared VM provisioning ran %q", c)
		}
	}
}
