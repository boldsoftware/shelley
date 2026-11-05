package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/exeenv"
)

// fullExeDev fakes the exe.dev CLI of a full-control exe-dev integration:
// VMs with tags, the account's integrations, and its GitHub accounts.
type fullExeDev struct {
	mu           sync.Mutex
	vms          map[string][]string // name -> tags
	integrations []map[string]any
	github       string
	refuse       string // set: refuse every command, as for a restricted key
	changes      []string
}

func (f *fullExeDev) exec(_ context.Context, _, command string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != "" {
		return nil, &exeDevError{Command: command, Message: f.refuse}
	}
	args := strings.Fields(command)
	find := func(name string) map[string]any {
		for _, ig := range f.integrations {
			if ig["name"] == name {
				return ig
			}
		}
		return nil
	}
	switch {
	case command == "ls --json":
		list := []exeDevVM{}
		for name, tags := range f.vms {
			list = append(list, exeDevVM{Name: name, Tags: tags})
		}
		return json.Marshal(map[string]any{"vms": list})
	case command == "integrations list --json":
		return json.Marshal(f.integrations)
	case command == "integrations setup github --repos --json":
		return []byte(f.github), nil
	}
	f.changes = append(f.changes, command)
	switch {
	case args[0] == "tag":
		f.vms[args[1]] = append(f.vms[args[1]], args[2])
	case args[1] == "attach":
		ig := find(args[2])
		ig["attachments"] = append(ig["attachments"].([]string), args[3])
	case args[1] == "detach":
		ig := find(args[2])
		ig["attachments"] = slices.DeleteFunc(ig["attachments"].([]string), func(a string) bool { return a == args[3] })
	case args[1] == "add":
		f.integrations = append(f.integrations, map[string]any{
			"name": strings.TrimPrefix(args[3], "--name="), "type": args[2],
			"repositories": []string{strings.TrimPrefix(args[4], "--repository=")}, "attachments": []string{strings.TrimPrefix(args[5], "--attach=")},
		})
	default:
		return nil, errors.New("unexpected command " + command)
	}
	return []byte(`{}`), nil
}

func (f *fullExeDev) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	changes := f.changes
	f.changes = nil
	return changes
}

func TestProjectExeAccount(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	e.want(201, "POST", "/api/projects", `{"name":"Acme Shop","instructions":"","cwd":""}`)
	prod, err := exeenv.New("https", "exe.xyz")
	if err != nil {
		t.Fatal(err)
	}
	e.srv.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return prod, nil, nil
	}
	exe := &fullExeDev{
		vms: map[string][]string{"acme-base": {"buildkite"}, "shared": {}},
		integrations: []map[string]any{
			{"name": "clickhouse", "type": "http-proxy", "attachments": []string{}},
			{"name": "ci", "type": "http-proxy", "isTeam": true, "attachments": []string{"tag:buildkite"}},
			{"name": "acme-web", "type": "github", "repositories": []string{"acme/web"}, "attachments": []string{"vm:elsewhere"}},
			{"name": "acme-api", "type": "github", "isTeam": true, "repositories": []string{"acme/api"}, "attachments": []string{"tag:buildkite"}},
		},
		github: `{"accounts":[{"account":"acme","github_login":"ann","installation_id":7,"repos":[{"full_name":"acme/api","private":true}],"error":""}]}`,
	}
	e.srv.conversationVMs.exe = exe.exec
	const own = "integration=acme-exe&mode=own&base=acme-base"
	view := func(query string) exeAccountView {
		var v exeAccountView
		if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/projects/Acme%20Shop/exe-account?"+query, "")), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	state := func(v exeAccountView) string {
		var out []string
		for _, ig := range v.Integrations {
			out = append(out, fmt.Sprintf("%s:%v/%v", ig.Name, ig.Added, ig.Reaches))
		}
		return strings.Join(out, " ")
	}
	change := func(code int, body string) string {
		return e.want(code, "POST", "/api/projects/Acme%20Shop/exe-account", body)
	}

	// Outside workspace mode, the routes don't exist.
	e.want(404, "GET", "/api/projects/Acme%20Shop/exe-account?"+own, "")
	change(404, `{"integration":"acme-exe","mode":"own","base":"acme-base","action":"add","name":"clickhouse"}`)
	e.want(404, "GET", "/api/github-accounts?integration=acme-exe", "")
	db.EnableWorkspaceMode(t, e.srv.db)
	if err := e.srv.loadWorkspace(t.Context()); err != nil {
		t.Fatal(err)
	}

	// No tag is shown before one is saved.
	v := view(own)
	if v.Tag != "" || v.ManageURL != "https://exe.dev/integrations" || v.Error != "" || len(v.VMs) != 1 || v.VMs[0].Tagged || !v.VMs[0].Found {
		t.Fatalf("view: %+v", v)
	}
	if got := state(v); got != "clickhouse:false/false ci:false/true acme-web:false/false acme-api:false/true" {
		t.Fatalf("state: %s", got)
	}
	if len(exe.took()) != 0 {
		t.Fatal("viewing changed the account")
	}
	change(400, `{"integration":"acme-exe","mode":"own","base":"acme-base","action":"remove","name":"clickhouse"}`)

	// Adding tags the base, then attaches to the project's tag.
	change(204, `{"integration":"acme-exe","mode":"own","base":"acme-base","action":"add","name":"clickhouse"}`)
	if got := exe.took(); !slices.Equal(got, []string{"tag acme-base shelley-acme-shop --json", "integrations attach clickhouse tag:shelley-acme-shop"}) {
		t.Fatalf("add: %q", got)
	}
	if v := view(own); v.Tag != "shelley-acme-shop" || !strings.HasPrefix(state(v), "clickhouse:true/true") {
		t.Fatalf("after add: %+v", v)
	}
	change(204, `{"integration":"acme-exe","mode":"own","base":"acme-base","action":"remove","name":"clickhouse"}`)
	if got := exe.took(); !slices.Equal(got, []string{"integrations detach clickhouse tag:shelley-acme-shop"}) {
		t.Fatalf("remove: %q", got)
	}

	// A repository that reaches the VMs already changes nothing; one with an
	// integration elsewhere attaches it; one without gets a new integration.
	github := func(repo string) string {
		var got struct{ Integration, Repository string }
		if err := json.Unmarshal([]byte(change(200, `{"integration":"acme-exe","mode":"shared","shared_vm":"shared","action":"github","repository":"`+repo+`"}`)), &got); err != nil || got.Repository != repo {
			t.Fatalf("github %s: %+v %v", repo, got, err)
		}
		return got.Integration + ": " + strings.Join(exe.took(), "; ")
	}
	exe.vms["shared"] = []string{"buildkite"}
	if got := github("acme/api"); got != "acme-api: " {
		t.Fatalf("reaching: %s", got)
	}
	if got := github("Acme/Web"); got != "acme-web: tag shared shelley-acme-shop --json; integrations attach acme-web tag:shelley-acme-shop" {
		t.Fatalf("existing: %s", got)
	}
	if got := github("acme/new.js"); got != "acme-new-js: integrations add github --name=acme-new-js --repository=acme/new.js --attach=tag:shelley-acme-shop --source=shelley --json" {
		t.Fatalf("new: %s", got)
	}
	change(400, `{"mode":"own","action":"github","repository":"acme/new; rm -rf"}`)
	change(400, `{"integration":"acme-exe","mode":"shared","shared_vm":"missing","action":"tag"}`)

	// GitHub accounts pass through.
	var accounts struct {
		Accounts []githubAccount `json:"accounts"`
		SetupURL string          `json:"setup_url"`
	}
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/github-accounts?integration=acme-exe", "")), &accounts); err != nil || len(accounts.Accounts) != 1 || accounts.Accounts[0].Repos[0].FullName != "acme/api" || accounts.SetupURL != "https://exe.dev/github/setup" {
		t.Fatalf("accounts: %+v %v", accounts, err)
	}

	// A restricted key gets exe.dev's refusal to show.
	exe.refuse = "tag-scoped SSH keys cannot list integrations"
	if v := view(own); v.Error != exe.refuse || len(v.Integrations) != 0 {
		t.Fatalf("restricted view: %+v", v)
	}
	if body := e.want(200, "GET", "/api/github-accounts?integration=acme-exe", ""); !strings.Contains(body, `"error":"tag-scoped`) {
		t.Fatalf("restricted accounts: %s", body)
	}
	if body := change(502, `{"integration":"acme-exe","mode":"own","action":"add","name":"clickhouse"}`); !strings.Contains(body, "exe.dev refused: tag-scoped") {
		t.Fatalf("restricted add: %s", body)
	}
	e.want(404, "GET", "/api/projects/Nope/exe-account?"+own, "")

	// The saved tag survives a rename; a new project named like the old one
	// gets the next free tag.
	e.want(200, "PUT", "/api/projects/Acme%20Shop", `{"name":"Renamed","instructions":"","cwd":""}`)
	e.want(201, "POST", "/api/projects", `{"name":"Acme Shop","instructions":"","cwd":""}`)
	exe.refuse = ""
	if v := view(own); v.Tag != "" {
		t.Fatalf("new project's unsaved tag: %s", v.Tag)
	}
	change(204, `{"integration":"acme-exe","mode":"own","base":"acme-base","action":"add","name":"clickhouse"}`)
	if v := view(own); v.Tag != "shelley-acme-shop-2" {
		t.Fatalf("new project's tag: %s", v.Tag)
	}
	if v := e.want(200, "GET", "/api/projects/Renamed/exe-account?"+own, ""); !strings.Contains(v, `"tag":"shelley-acme-shop"`) {
		t.Fatalf("renamed project's tag: %s", v)
	}
}

func TestGithubIntegrationName(t *testing.T) {
	taken := map[string]bool{"acme-web": true, "acme-web-2": true}
	for repo, want := range map[string]string{"acme/web": "acme-web-3", "Acme/My_Repo.go": "acme-my-repo-go", "exe/x": "github-exe-x"} {
		if got := githubIntegrationName(repo, taken); got != want {
			t.Errorf("%s: %s, want %s", repo, got, want)
		}
	}
}
