package server

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

// A project whose tasks own their VMs, without a base, gives each task a
// new, empty exe.dev VM: nothing to copy and no setup task.
func TestEmptyProjectVMs(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newActionFixture(t, "true", false)
	newFakeSSH(t, s)
	// An integration added in Edit project reaches a new VM through the
	// project's tag.
	exe := &fakeExeDev{vms: map[string]string{}, integrations: `[{"name":"gh","attachments":["tag:shelley-empty"]}]`}
	s.conversationVMs.exe = exe.exec
	// The fake ssh runs commands here, so the VM's directory is a local one.
	dir := filepath.Join(t.TempDir(), "repo")
	created := workspaceResponse[db.Project](t, workspaceAPI(s, "POST", "/api/projects",
		`{"name":"Empty","cwd":"`+dir+`","vm":{"integration":"me-exe"}}`), 201)
	if created.VM == nil || *created.VM != (db.ProjectVM{Integration: "me-exe"}) {
		t.Fatalf("project VM: %+v", created.VM)
	}
	if _, err := s.db.SaveProjectExeTag(t.Context(), created.Name, projectTagBase(created.Name)); err != nil {
		t.Fatal(err)
	}
	conversation, release, err := s.createProjectConversation(t.Context(), nil, nil, db.ConversationOptions{Profile: created.Name}, created.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	waitForActions(t, s)
	name := "shelley-" + strings.ToLower(conversation.ConversationID)
	if !slices.ContainsFunc(exe.commands, func(c string) bool {
		return strings.HasPrefix(c, "new --name="+name+" ") && strings.HasSuffix(c, " --tag=shelley-empty")
	}) {
		t.Fatalf("exe.dev commands: %v", exe.commands)
	}
	vm, err := s.db.GetConversationVM(t.Context(), conversation.ConversationID)
	if err != nil || vm.Status != db.ConversationVMReady || vm.Shared || vm.Base != "" || vm.VMCreatedAt == "" {
		t.Fatalf("conversation VM: %+v %v", vm, err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("project directory on the empty VM: %v", err)
	}
	if prompt, err := conversationPrompt(t, s, conversation.ConversationID); err != nil || !strings.Contains(prompt, "a new exe.dev VM that started empty") {
		t.Fatalf("prompt lacks the empty VM: %v %q", err, prompt)
	}
	// Archiving deletes it like any VM of its own.
	if status, reason, err := s.deleteConversationVM(t.Context(), *vm); err != nil || status != db.ConversationVMDeleted {
		t.Fatalf("delete: %q %q %v", status, reason, err)
	}
}
