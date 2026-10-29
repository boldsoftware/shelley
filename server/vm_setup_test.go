package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/exeenv"
)

func TestProjectVMIntegration(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	integrations := []reflectionIntegration{{Name: "setup-test-exe", Type: "exe-dev"}, {Name: "gh", Type: "github"}}
	e.srv.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return exeenv.Environment{}, integrations, nil
	}
	if body := e.want(200, "GET", "/api/exe-dev-integrations", ""); strings.TrimSpace(body) != `{"default":"setup-test-exe","integrations":["setup-test-exe"]}` {
		t.Fatalf("integrations: %s", body)
	}

	// An empty integration means the only exe.dev integration.
	var created db.Project
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/projects", `{"name":"Remote Work","cwd":"/nonexistent/repo","vm":{"base":"remote-work-base","integration":""}}`)), &created); err != nil {
		t.Fatal(err)
	}
	if *created.VM != (db.ProjectVM{Base: "remote-work-base", Integration: "setup-test-exe"}) {
		t.Fatalf("created: %+v", created.VM)
	}
	// A base is required.
	e.want(400, "POST", "/api/projects", `{"name":"No Base","cwd":"/r","vm":{"base":"","integration":""}}`)

	// An ambiguous integration must be chosen.
	integrations = append(integrations, reflectionIntegration{Name: "other-exe", Type: "exe-dev"})
	if body := e.want(400, "POST", "/api/projects", `{"name":"Ambiguous","cwd":"/r","vm":{"base":"b","integration":""}}`); !strings.Contains(body, "setup-test-exe, other-exe") {
		t.Fatalf("ambiguous: %s", body)
	}
}
