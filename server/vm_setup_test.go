package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/exeenv"
)

func TestProjectBaseVMSetup(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	integrations := []reflectionIntegration{{Name: "setup-test-exe", Type: "exe-dev"}, {Name: "gh", Type: "github"}}
	e.srv.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return exeenv.Environment{}, integrations, nil
	}
	newFakeSSH(t, e.srv)
	e.srv.conversationVMs.exe = (&fakeExeDev{vms: map[string]string{}}).exec
	project := func(body string) db.Project {
		t.Helper()
		var c db.Project
		if err := json.Unmarshal([]byte(body), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if body := e.want(200, "GET", "/api/exe-dev-integrations", ""); strings.TrimSpace(body) != `{"default":"setup-test-exe","integrations":["setup-test-exe"]}` {
		t.Fatalf("integrations: %s", body)
	}

	// No base: the only exe.dev integration is used, and a setup conversation starts.
	created := project(e.want(201, "POST", "/api/projects", `{"name":"Remote Work","cwd":"/nonexistent/repo","vm":{"base":"","integration":""}}`))
	vm := created.VM
	if vm == nil || vm.Base != "" || vm.Integration != "setup-test-exe" || vm.SetupConversationID == "" {
		t.Fatalf("created: %+v", vm)
	}
	setupID := vm.SetupConversationID
	e.waitTurn(setupID, 0)
	if _, err := e.database.GetConversationVM(t.Context(), setupID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("setup conversation must run here: %v", err)
	}
	home, _ := os.UserHomeDir()
	if conversation, err := e.database.GetConversationByID(t.Context(), setupID); err != nil || derefString(conversation.Cwd) != home {
		t.Fatalf("setup conversation cwd: %v %v", conversation, err)
	}
	if system := systemText(e.lastTurnRequest()); !strings.Contains(system, "<base_vm_setup>") || !strings.Contains(system, `"remote-work-base"`) ||
		!strings.Contains(system, "SSH_AUTH_SOCK=") || !strings.Contains(system, "/api/projects/"+url.PathEscape(created.Name)) {
		t.Fatalf("setup prompt:\n%s", system)
	}

	// Meanwhile new conversations are refused, and saving again keeps the same setup.
	if body := e.want(409, "POST", "/api/conversations/new", `{"project":"`+created.Name+`","message":"hi"}`); !strings.Contains(body, setupID) {
		t.Fatalf("refusal: %s", body)
	}
	update := `{"name":"Remote Work","cwd":"/nonexistent/repo","vm":{"base":"","integration":"setup-test-exe"}}`
	if again := project(e.want(200, "PUT", "/api/projects/"+url.PathEscape(created.Name), update)); again.VM.SetupConversationID != setupID {
		t.Fatalf("second setup started: %+v", again.VM)
	}

	// Setting the base ends the setup; conversations then get VMs copied from it.
	done := project(e.want(200, "PUT", "/api/projects/"+url.PathEscape(created.Name),
		`{"name":"Remote Work","cwd":"/nonexistent/repo","vm":{"base":"remote-work-base","integration":"setup-test-exe"}}`))
	if *done.VM != (db.ProjectVM{Base: "remote-work-base", Integration: "setup-test-exe"}) {
		t.Fatalf("done: %+v", done.VM)
	}
	var started struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/conversations/new", `{"project":"`+created.Name+`","message":"hi"}`)), &started); err != nil {
		t.Fatal(err)
	}
	if bound, err := e.database.GetConversationVM(t.Context(), started.ConversationID); err != nil || bound.Base != "remote-work-base" {
		t.Fatalf("conversation VM: %+v %v", bound, err)
	}
	e.waitTurn(started.ConversationID, 0)

	// Turning VMs off and on again without a base starts a new setup.
	e.want(200, "PUT", "/api/projects/"+url.PathEscape(created.Name), `{"name":"Remote Work","cwd":"/tmp"}`)
	if again := project(e.want(200, "PUT", "/api/projects/"+url.PathEscape(created.Name), update)); again.VM.SetupConversationID == "" || again.VM.SetupConversationID == setupID {
		t.Fatalf("restarted setup: %+v", again.VM)
	} else {
		e.waitTurn(again.VM.SetupConversationID, 0)
	}

	// An ambiguous integration must be chosen.
	integrations = append(integrations, reflectionIntegration{Name: "other-exe", Type: "exe-dev"})
	if body := e.want(400, "POST", "/api/projects", `{"name":"Ambiguous","cwd":"/r","vm":{"base":"b","integration":""}}`); !strings.Contains(body, "setup-test-exe, other-exe") {
		t.Fatalf("ambiguous: %s", body)
	}
}
