package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/exeenv"
)

func TestProjectGithubRepos(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	env, err := exeenv.New("https", "exe.xyz")
	if err != nil {
		t.Fatal(err)
	}
	e.srv.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return env, []reflectionIntegration{
			{Name: "team-acme-api", Type: "github", Team: true, Help: "git clone https://github.int.exe.xyz/acme/api.git"},
			{Name: "acme-web", Type: "github", Help: "git clone https://github.int.exe.xyz/acme/web"},
			{Name: "broken", Type: "github", Help: "no url"},
		}, nil
	}
	var listed struct {
		Repos      []githubRepo `json:"repos"`
		ConnectURL string       `json:"connect_url"`
	}
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/github-repos", "")), &listed); err != nil {
		t.Fatal(err)
	}
	want := []githubRepo{
		{ProjectRepo: db.ProjectRepo{Integration: "team-acme-api", Repository: "acme/api"}, Team: true},
		{ProjectRepo: db.ProjectRepo{Integration: "acme-web", Repository: "acme/web"}},
	}
	if !reflect.DeepEqual(listed.Repos, want) || !strings.HasPrefix(listed.ConnectURL, "https://exe.dev/integrations/add?") {
		t.Fatalf("listed: %+v", listed)
	}

	// Off exe.dev there is nothing to connect.
	e.srv.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return exeenv.Environment{}, nil, nil
	}
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/github-repos", "")), &listed); err != nil || listed.ConnectURL != "" || len(listed.Repos) != 0 {
		t.Fatalf("off exe.dev: %+v %v", listed, err)
	}

	var created db.Project
	body := `{"name":"Acme","instructions":"Ship the Acme storefront.","cwd":"","repos":[{"integration":"team-acme-api","repository":"acme/api"},{"integration":"acme-web","repository":"acme/web"}]}`
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/projects", body)), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Repos) != 2 {
		t.Fatalf("created: %+v", created)
	}
	db.EnableWorkspaceMode(t, e.srv.db)
	assistantID, err := e.srv.ensureWorkspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := e.srv.workspaceInstructions(t.Context(), assistantID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, `"instructions":"Ship the Acme storefront.","repos":["acme/api","acme/web"]`) {
		t.Fatalf("catalog lacks the project's instructions and repos:\n%s", prompt)
	}
}
