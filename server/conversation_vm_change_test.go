package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

// A conversation moves between machines: a new VM of its own (empty or a
// copy), an existing machine, and back to this host. It keeps every own VM
// it leaves until archiving it deletes them.
func TestChangeConversationVM(t *testing.T) {
	t.Parallel()
	s, database, id, cwd := newActionFixture(t, "true", false)
	ssh := newFakeSSH(t, s)
	exe := &fakeExeDev{vms: map[string]string{"base": "2026-01-01T00:00:00Z"}}
	s.conversationVMs.exe = exe.exec
	lower := strings.ToLower(id)
	// The fake ssh runs commands here, so VM directories are local ones.
	vmDir := filepath.Join(t.TempDir(), "work")

	move := func(body string, want int) {
		t.Helper()
		if response := workspaceAPI(s, "PUT", "/api/conversation/"+id+"/vm", body); response.Code != want {
			t.Fatalf("%s: %d %s", body, response.Code, response.Body.String())
		}
		s.workspace.workers.Wait()
	}
	current := func() *db.ConversationVM {
		t.Helper()
		vm, err := database.GetConversationVM(t.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return vm
	}
	cwdOf := func() string {
		t.Helper()
		conversation, err := database.GetConversationByID(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		return derefString(conversation.Cwd)
	}
	notice := func() map[string]any {
		t.Helper()
		messages, err := database.ListMessages(t.Context(), id)
		if err != nil || len(messages) == 0 {
			t.Fatal(messages, err)
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(derefString(messages[len(messages)-1].UserData)), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}

	// A new, empty VM, made in the directory given.
	move(`{"vm":{"integration":"me-exe"},"dir":"`+vmDir+`"}`, 200)
	vm := current()
	if vm == nil || vm.Name != "shelley-"+lower || vm.Status != db.ConversationVMReady || vm.Shared || vm.VMCreatedAt == "" || cwdOf() != vmDir {
		t.Fatalf("empty VM: %+v cwd=%q", vm, cwdOf())
	}
	if info, err := os.Stat(vmDir); err != nil || !info.IsDir() {
		t.Fatalf("directory on the new VM: %v", err)
	}
	if prompt, err := conversationPrompt(t, s, id); err != nil || !strings.Contains(prompt, "a new exe.dev VM that started empty") {
		t.Fatalf("prompt lacks the VM: %v %q", err, prompt)
	}
	if data := notice(); data["machine"] != vm.Name || data["from"] != cwd || data["to"] != vmDir {
		t.Fatalf("notice: %v", data)
	}

	// A copy of a base; the empty VM is kept.
	move(`{"vm":{"base":"base","integration":"me-exe"},"dir":"`+vmDir+`"}`, 200)
	if vm := current(); vm == nil || vm.Name != "base-"+lower || vm.Status != db.ConversationVMReady {
		t.Fatalf("copied VM: %+v", vm)
	}
	// An SSH host needs a directory: its home is unknown.
	move(`{"vm":{"shared_vm":"devbox"}}`, 400)
	move(`{"vm":{"shared_vm":"devbox"},"dir":"`+vmDir+`"}`, 200)
	if vm := current(); vm == nil || vm.Name != "devbox" || !vm.Shared || !vm.SSHHost() || vm.Status != db.ConversationVMReady {
		t.Fatalf("SSH host: %+v", vm)
	}
	if masters := ssh.calls(t, "master"); !slices.Contains(masters, "devbox") {
		t.Fatalf("masters: %v", masters)
	}
	// Back here, in the project's directory; the shared machine is forgotten.
	move(`{"vm":null}`, 200)
	if vm := current(); vm != nil || cwdOf() != cwd {
		t.Fatalf("on this host: %+v cwd=%q", vm, cwdOf())
	}
	if prompt, err := conversationPrompt(t, s, id); err != nil || strings.Contains(prompt, "project_vm") {
		t.Fatalf("prompt still has a VM: %v %q", err, prompt)
	}
	if data := notice(); data["machine"] != "this Shelley host" || data["to"] != cwd {
		t.Fatalf("notice: %v", data)
	}
	// Another empty VM can't reuse the name of the one kept.
	move(`{"vm":{"integration":"me-exe"},"dir":"`+vmDir+`"}`, 200)
	if vm := current(); vm == nil || vm.Name != "shelley-"+lower+"-2" {
		t.Fatalf("second empty VM: %+v", vm)
	}
	if rm := exe.removed(); len(rm) != 0 {
		t.Fatalf("VMs removed before archiving: %v", rm)
	}

	// Subagents follow their conversation.
	child, err := database.CreateSubagentConversation(t.Context(), "helper", id, &vmDir)
	if err != nil {
		t.Fatal(err)
	}
	if response := workspaceAPI(s, "PUT", "/api/conversation/"+child.ConversationID+"/vm", `{"vm":null}`); response.Code != 409 {
		t.Fatalf("subagent: %d %s", response.Code, response.Body.String())
	}

	// Archiving deletes every VM of its own, current or left.
	archiveConversation(t, s, id, "archive")
	s.cleanupConversationVMs(t.Context())
	rm := exe.removed()
	slices.Sort(rm)
	want := []string{"rm base-" + lower + " --json", "rm shelley-" + lower + " --json", "rm shelley-" + lower + "-2 --json"}
	if !slices.Equal(rm, want) {
		t.Fatalf("removed %v, want %v", rm, want)
	}
	vms, err := database.ListConversationVMs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, vm := range vms {
		if vm.Status != db.ConversationVMDeleted {
			t.Errorf("after archiving: %+v", vm)
		}
	}
}
