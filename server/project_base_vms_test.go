package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/exeenv"
)

// newBaseVMEnv is a server with one exe.dev integration backed by exe.
func newBaseVMEnv(t *testing.T) (*profileEnv, *fakeExeDev) {
	t.Helper()
	e := newProfileEnv(t)
	e.srv.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return exeenv.Environment{}, []reflectionIntegration{{Name: "base-test-exe", Type: "exe-dev"}}, nil
	}
	newFakeSSH(t, e.srv)
	exe := &fakeExeDev{vms: map[string]string{"user-base": "2026-01-01T00:00:00Z"}}
	e.srv.conversationVMs.exe = exe.exec
	return e, exe
}

func (e *profileEnv) workspace() db.Workspace {
	e.t.Helper()
	var w db.Workspace
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/workspace", "")), &w); err != nil {
		e.t.Fatal(err)
	}
	return w
}

// newSetupProject creates a project whose setup conversation has started.
func (e *profileEnv) newSetupProject(name string) (string, string) {
	e.t.Helper()
	var c db.Project
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/projects", `{"name":"`+name+`","cwd":"/r","vm":{"base":"","integration":""}}`)), &c); err != nil {
		e.t.Fatal(err)
	}
	e.waitTurn(c.VM.SetupConversationID, 0)
	return c.Name, c.VM.SetupConversationID
}

func TestDeleteProjectDeletesItsBaseVM(t *testing.T) {
	t.Parallel()
	e, exe := newBaseVMEnv(t)
	name, setupID := e.newSetupProject("Remote Work")
	path := "/api/projects/" + url.PathEscape(name)

	e.want(400, "POST", path+"/base-vm", `{"name":"remote-work-base","flags":["--env=SECRET=x"]}`)
	e.want(400, "POST", path+"/base-vm", `{"name":"remote-work-base","flags":["--tag=a --name=other"]}`)
	if body := e.want(409, "POST", path+"/base-vm", `{"name":"user-base"}`); !strings.Contains(body, "already exists") {
		t.Fatalf("existing name: %s", body)
	}
	e.want(201, "POST", path+"/base-vm", `{"name":"remote-work-base","flags":["--tag=team","--memory=16GB"]}`)
	if got := exe.commands[len(exe.commands)-2]; got != "new --name=remote-work-base --comment='Shelley base VM for project remote-work' --json --tag=team --memory=16GB" {
		t.Fatalf("new: %s", got)
	}
	if vms := e.workspace().BaseVMs; len(vms) != 1 || vms[0].Name != "remote-work-base" || vms[0].Status != db.ProjectBaseVMCreated || vms[0].VMCreatedAt == "" {
		t.Fatalf("base VMs: %+v", vms)
	}

	// The setup sets the base; an active conversation blocks deletion.
	e.want(200, "PUT", path, `{"name":"Remote Work","cwd":"/r","vm":{"base":"remote-work-base","integration":"base-test-exe"}}`)
	var member struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/conversations/new", `{"project":"`+name+`","message":"hi"}`)), &member); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(member.ConversationID, 0)
	// So does the finished setup conversation, which is an ordinary one now.
	if body := e.want(409, "DELETE", path, ""); !strings.Contains(body, "has 2 active conversations") {
		t.Fatalf("active conversations: %s", body)
	}
	archiveConversation(t, e.srv, member.ConversationID, "archive")
	archiveConversation(t, e.srv, setupID, "archive")

	// A failed VM deletion leaves the project.
	exe.mu.Lock()
	exe.rmErr = errors.New("403 Forbidden")
	exe.mu.Unlock()
	if body := e.want(502, "DELETE", path, ""); !strings.Contains(body, "remote-work-base") || !strings.Contains(body, "403 Forbidden") {
		t.Fatalf("rm failure: %s", body)
	}
	if w := e.workspace(); len(w.BaseVMs) != 1 || len(w.Projects) != 1 {
		t.Fatalf("after failure: %+v", w)
	}
	exe.mu.Lock()
	exe.rmErr = nil
	exe.mu.Unlock()

	if body := e.want(200, "DELETE", path, ""); strings.TrimSpace(body) != `{"deleted_vms":["remote-work-base"],"kept_vms":[]}` {
		t.Fatalf("delete: %s", body)
	}
	exe.mu.Lock()
	_, left := exe.vms["remote-work-base"]
	exe.mu.Unlock()
	if left {
		t.Fatal("base VM not deleted")
	}
	w := e.workspace()
	for _, m := range w.Conversations {
		if m.ConversationID == member.ConversationID || m.ConversationID == setupID {
			t.Fatalf("conversation still in a project: %+v", m)
		}
	}
	if len(w.BaseVMs) != 0 || len(w.Projects) != 0 {
		t.Fatalf("after delete: %+v", w)
	}
	for _, cid := range []string{member.ConversationID, setupID} {
		if c, err := e.database.GetConversationByID(t.Context(), cid); err != nil || !c.Archived {
			t.Fatalf("history of %s: %+v %v", cid, c, err)
		}
	}
}

// Deleting a project while its setup conversation is working and creating the
// base stops the conversation, waits for the creation, and deletes the new VM.
func TestDeleteProjectCancelsBaseVMSetup(t *testing.T) {
	t.Parallel()
	e, exe := newBaseVMEnv(t)
	name, setupID := e.newSetupProject("Remote Work")
	path := "/api/projects/" + url.PathEscape(name)
	e.want(202, "POST", "/api/conversation/"+setupID+"/chat", `{"message":"bash: sleep 300"}`)
	manager, err := e.srv.getOrCreateConversationManager(t.Context(), setupID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, manager.IsAgentWorking)

	exe.newGate = make(chan struct{})
	created := make(chan string)
	go func() { created <- e.want(201, "POST", path+"/base-vm", `{"name":"remote-work-base"}`) }()
	waitFor(t, 10*time.Second, func() bool {
		vms, err := e.database.ListProjectBaseVMs(t.Context(), name)
		return err == nil && len(vms) == 1 && vms[0].Status == db.ProjectBaseVMCreating
	})
	deleted := make(chan string)
	go func() { deleted <- e.want(200, "DELETE", path, "") }()
	waitFor(t, 10*time.Second, func() bool { return !manager.IsAgentWorking() })
	// The deletion refuses new work while it waits for the creation.
	if body := e.want(409, "POST", path+"/base-vm", `{"name":"other-base"}`); !strings.Contains(body, "being deleted") {
		t.Fatalf("second creation: %s", body)
	}
	close(exe.newGate)
	<-created
	if body := <-deleted; strings.TrimSpace(body) != `{"deleted_vms":["remote-work-base"],"kept_vms":[]}` {
		t.Fatalf("delete: %s", body)
	}
	if c, err := e.database.GetConversationByID(t.Context(), setupID); err != nil || !c.Archived {
		t.Fatalf("setup conversation: %+v %v", c, err)
	}
}

// Shelley never deletes a base it did not create, nor a shared VM.
func TestDeleteProjectKeepsOtherVMs(t *testing.T) {
	t.Parallel()
	e, exe := newBaseVMEnv(t)
	for _, vm := range []string{`{"base":"user-base","integration":"base-test-exe"}`, `{"shared_vm":"user-base","integration":"base-test-exe"}`} {
		var c db.Project
		if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/projects", `{"name":"Mine","cwd":"/r","vm":`+vm+`}`)), &c); err != nil {
			t.Fatal(err)
		}
		e.want(409, "POST", "/api/projects/"+url.PathEscape(c.Name)+"/base-vm", `{"name":"x-base"}`)
		if body := e.want(200, "DELETE", "/api/projects/"+url.PathEscape(c.Name), ""); strings.TrimSpace(body) != `{"deleted_vms":[],"kept_vms":[]}` {
			t.Fatalf("delete: %s", body)
		}
	}
	if len(exe.removed()) != 0 {
		t.Fatalf("removed: %v", exe.removed())
	}
}
