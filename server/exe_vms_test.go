package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/exeenv"
)

func TestExeVMs(t *testing.T) {
	t.Parallel()
	s, database, id, _ := newActionFixture(t, "true", false)
	if err := database.BindConversationVM(t.Context(), id, db.ProjectVM{Base: "repo-base", Integration: "a"}, "/home/exedev"); err != nil {
		t.Fatal(err)
	}
	conversationVM, err := database.GetConversationVM(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	enableDevProdTabs(t, s)
	workspaceResponse[db.Project](t, workspaceAPI(s, "POST", "/api/projects",
		`{"name":"Remote","cwd":"/r","vm":{"base":"repo-base","integration":"a"}}`), 201)
	s.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return exeenv.Environment{}, []reflectionIntegration{{Name: "a", Type: "exe-dev"}, {Name: "llm", Type: "llm"}, {Name: "b", Type: "exe-dev"}}, nil
	}
	var calls atomic.Int32
	s.conversationVMs.exe = func(_ context.Context, integration, command string) ([]byte, error) {
		calls.Add(1)
		if command != "ls --json" {
			t.Errorf("command %q", command)
		}
		vms := map[string]string{
			"a": `[{"vm_name":"web","emoji":"🐟","status":"running","https_url":"https://web.exe.xyz","ssh_command":"ssh web.exe.xyz","proxy_share":"public"},{"vm_name":"repo-base"},{"vm_name":"` + conversationVM.Name + `"}]`,
			"b": `[{"vm_name":"web","emoji":"🐟","status":"running","https_url":"https://web.exe.xyz","proxy_share":"public"},{"vm_name":"api","proxy_share":"private"}]`,
		}[integration]
		return []byte(`{"vms":` + vms + `}`), nil
	}
	get := func(path string) exeVMList {
		response := workspaceAPI(s, "GET", path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		var list exeVMList
		if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		return list
	}
	list := get("/api/exe-vms")
	got := map[string]exeVM{}
	var names []string
	for _, vm := range list.VMs {
		got[vm.Name] = vm
		names = append(names, vm.Name)
	}
	if len(names) != 4 || names[0] != "api" {
		t.Fatalf("VMs not merged, deduplicated, and sorted: %v", names)
	}
	if web := got["web"]; !web.Public || web.Emoji != "🐟" || web.URL != "https://web.exe.xyz" || web.SSH != "ssh web.exe.xyz" || web.Role != "" {
		t.Errorf("web: %+v", web)
	}
	if got["api"].Public || got["repo-base"].Role != "base" || got[conversationVM.Name].Role != "conversation" {
		t.Errorf("roles or sharing: %+v", got)
	}

	// Cached, unless refreshed.
	get("/api/exe-vms")
	if calls.Load() != 2 {
		t.Errorf("cached list asked exe.dev again: %d calls", calls.Load())
	}
	get("/api/exe-vms?refresh=1")
	if calls.Load() != 4 {
		t.Errorf("refresh did not ask exe.dev: %d calls", calls.Load())
	}
}

// enableDevProdTabs turns on workspace mode and its dev/prod tabs.
func enableDevProdTabs(t *testing.T, s *Server) {
	t.Helper()
	db.EnableWorkspaceMode(t, s.db)
	if err := s.loadWorkspace(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetFeatureFlagOverride(t.Context(), FlagDevProdTabs.Name, "true"); err != nil {
		t.Fatal(err)
	}
}

func TestExeVMsNeedTheFlag(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)
	db.EnableWorkspaceMode(t, s.db)
	if err := s.loadWorkspace(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct{ method, path, body string }{
		{"GET", "/api/exe-vms", ""},
		{"GET", "/api/exe-vms/web/frame?origin=https://x.exe.xyz", ""},
		{"GET", "/api/exe-vms/web/settings", ""},
		{"POST", "/api/exe-vms/web/settings", `{"kind":"restart"}`},
	} {
		if response := workspaceAPI(s, request.method, request.path, request.body); response.Code != http.StatusNotFound {
			t.Errorf("%s %s without the flag: %d", request.method, request.path, response.Code)
		}
	}
}

func TestExeVMsWithoutIntegrationLinkToSetup(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)
	enableDevProdTabs(t, s)
	s.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return exeenv.Environment{}, []reflectionIntegration{{Name: "llm", Type: "llm"}}, nil
	}
	response := workspaceAPI(s, "GET", "/api/exe-vms", "")
	var list exeVMList
	if err := json.Unmarshal(response.Body.Bytes(), &list); response.Code != http.StatusOK || err != nil {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	host, _ := os.Hostname()
	setup, err := url.Parse(list.SetupURL)
	if err != nil || len(list.VMs) != 0 || setup.Host != "exe.dev" || setup.Path != "/integrations/add" ||
		setup.Query().Get("service") != "exe-dev" || setup.Query().Get("attach") != "vm:"+host {
		t.Errorf("list %+v", list)
	}
}
