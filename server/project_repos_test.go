package server

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/exeenv"
)

func TestDefaultExeDevIntegration(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		names []string
		want  string
	}{
		{nil, ""},
		{[]string{"only"}, "only"},
		{[]string{"other-exe", host + "-exe"}, host + "-exe"},
		{[]string{"other-exe", "another-exe"}, ""},
		{[]string{host + "-a", host + "-b"}, ""},
	} {
		if got := defaultExeDevIntegration(c.names); got != c.want {
			t.Errorf("defaultExeDevIntegration(%v) = %q, want %q", c.names, got, c.want)
		}
	}
}

func TestProjectGithubReposAndSetup(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	env, err := exeenv.New("https", "exe.xyz")
	if err != nil {
		t.Fatal(err)
	}
	e.srv.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return env, []reflectionIntegration{
			{Name: "setup-test-exe", Type: "exe-dev"},
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
		{ProjectRepo: db.ProjectRepo{Integration: "team-acme-api", Repository: "acme/api"}, CloneURL: "https://github.int.exe.xyz/acme/api.git", Team: true},
		{ProjectRepo: db.ProjectRepo{Integration: "acme-web", Repository: "acme/web"}, CloneURL: "https://github.int.exe.xyz/acme/web.git"},
	}
	if !reflect.DeepEqual(listed.Repos, want) || !strings.HasPrefix(listed.ConnectURL, "https://exe.dev/integrations/add?") {
		t.Fatalf("listed: %+v", listed)
	}

	// Off exe.dev there is nothing to connect.
	onExeDev := e.srv.listIntegrations
	e.srv.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return exeenv.Environment{}, nil, nil
	}
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/github-repos", "")), &listed); err != nil || listed.ConnectURL != "" || len(listed.Repos) != 0 {
		t.Fatalf("off exe.dev: %+v %v", listed, err)
	}
	e.srv.listIntegrations = onExeDev

	var created db.Project
	body := `{"name":"Acme","instructions":"Ship the Acme storefront.","cwd":"/home/exedev","vm":{"managed_base_vm":true,"base":"","integration":"setup-test-exe"},
		"repos":[{"integration":"team-acme-api","repository":"acme/api"},{"integration":"acme-web","repository":"acme/web"}]}`
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/projects", body)), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Repos) != 2 || created.VM == nil || created.VM.SetupConversationID == "" {
		t.Fatalf("created: %+v", created)
	}
	setupID := created.VM.SetupConversationID
	e.waitTurn(setupID, 0)
	system := systemText(e.lastTurnRequest())
	for _, s := range []string{"acme/api into ~/api", "git clone https://github.int.exe.xyz/acme/web.git", `project "Acme"`, "<project_instructions>\nShip the Acme storefront."} {
		if !strings.Contains(system, s) {
			t.Fatalf("setup prompt lacks %q:\n%s", s, system)
		}
	}
	setWorkspaceFlag(t, e.srv, "true")
	assistantID := e.srv.assistantID()
	prompt, err := e.srv.workspaceInstructions(t.Context(), assistantID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, `"instructions":"Ship the Acme storefront.","repos":["acme/api","acme/web"]`) {
		t.Fatalf("catalog lacks the project's instructions and repos:\n%s", prompt)
	}
}

func TestProjectReposPrompt(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	env, err := exeenv.New("https", "exe.xyz")
	if err != nil {
		t.Fatal(err)
	}
	e.srv.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return env, []reflectionIntegration{{Name: "acme-web", Type: "github", Help: "git clone https://github.int.exe.xyz/acme/web"}}, nil
	}
	e.want(201, "POST", "/api/projects", `{"name":"Acme","instructions":"","cwd":"",
		"repos":[{"integration":"acme-web","repository":"acme/web"},{"integration":"acme-api","repository":"acme/api"}]}`)
	prompt := func() string {
		t.Helper()
		project, err := e.srv.db.GetProject(t.Context(), "Acme")
		if err != nil {
			t.Fatal(err)
		}
		text, err := e.srv.projectReposPrompt(t.Context(), project)
		if err != nil {
			t.Fatal(err)
		}
		return text
	}
	// Without a saved tag, the prompt names none, nor guesses a clone URL.
	text := prompt()
	for _, want := range []string{
		"acme/web into ~/web through exe.dev GitHub integration \"acme-web\": git clone https://github.int.exe.xyz/acme/web.git",
		"acme/api into ~/api through exe.dev GitHub integration \"acme-api\" (not attached to this host now: ask the user to attach it)",
		"(`curl https://reflection.int.exe.xyz/tags`). Check every clone",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "shelley-acme") || strings.Count(text, "git clone") != 1 {
		t.Fatalf("prompt names an unsaved tag or guesses a URL:\n%s", text)
	}
	// Once saved, the tag is how added integrations reach the base.
	if _, err := e.srv.db.SaveProjectExeTag(t.Context(), "Acme", projectTagBase("Acme")); err != nil {
		t.Fatal(err)
	}
	text = prompt()
	for _, want := range []string{
		"\"acme-api\" (not attached to this host; if it was added in Edit project, it reaches the base through the project's tag \"shelley-acme\": its clone URL is in the base's `curl https://reflection.int.exe.xyz/integrations`)",
		"Shelley gives it the project's tag \"shelley-acme\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, text)
		}
	}
}
