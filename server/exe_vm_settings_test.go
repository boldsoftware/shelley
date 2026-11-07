package server

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/exeenv"
)

// exeVMSettingsFixture is a server whose one exe.dev integration, "a",
// lists VMs web (owned, email on) and shared (shared with the user), with
// page as its page. It records the commands that change something.
func exeVMSettingsFixture(t *testing.T, page string) (*Server, func() []string) {
	t.Helper()
	s, _, _ := newTestServer(t)
	enableDevProdTabs(t, s)
	s.listIntegrations = func(context.Context) (exeenv.Environment, []reflectionIntegration, error) {
		return exeenv.Environment{}, []reflectionIntegration{{Name: "a", Type: "exe-dev"}}, nil
	}
	s.conversationVMs.page = func(string) string { return page }
	var mu sync.Mutex
	var changes []string
	s.conversationVMs.exe = func(_ context.Context, integration, command string) ([]byte, error) {
		if integration != "a" {
			t.Errorf("integration %q", integration)
		}
		switch {
		case command == "ls --json":
			return []byte(`{"vms":[{"vm_name":"web"},{"vm_name":"shared"}]}`), nil
		case command == "ls -l 'web' --json":
			return []byte(`{"vms":[{"vm_name":"web","status":"running","region_display":"Los Angeles, USA","allocated_cpus":2,
				"memory_capacity_bytes":8589934592,"proxy_port":8000,"proxy_share":"private","tags":["scope","prod"],
				"email_receive_enabled":true,"egress_policy":"all","access":{"admin":true},
				"sharing":{"named_user_shares":[{"email":"a@b.c","status":"active","share_level":"web"}]},
				"share_links":[{"token":"tok","url":"https://web.exe.xyz/?t=tok"}]}]}`), nil
		case command == "ls -l 'shared' --json":
			return []byte(`{"vms":[{"vm_name":"shared","access":{"admin":false}}]}`), nil
		case strings.HasPrefix(command, "domain ls "):
			if strings.Contains(page, "tagged") {
				return nil, &exeDevError{Command: command, Message: "command not allowed by SSH key permissions"}
			}
			return []byte(`{"domains":[]}`), nil
		case strings.HasPrefix(command, "share receive-email ") && !strings.Contains(command, "' '"):
			return []byte(`{"email_address":"anything@web.exe.xyz","email_enabled":true}`), nil
		case strings.HasPrefix(command, "share port "):
			return nil, &exeDevError{Command: command, Message: "command not allowed by SSH key permissions"}
		}
		mu.Lock()
		defer mu.Unlock()
		changes = append(changes, command)
		return []byte(`{}`), nil
	}
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(changes)
	}
}

func getExeVMSettings(t *testing.T, s *Server, name string) exeVMSettings {
	t.Helper()
	response := workspaceAPI(s, "GET", "/api/exe-vms/"+name+"/settings", "")
	var settings exeVMSettings
	if err := json.Unmarshal(response.Body.Bytes(), &settings); response.Code != http.StatusOK || err != nil {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	return settings
}

func TestExeVMSettings(t *testing.T) {
	t.Parallel()
	s, _ := exeVMSettingsFixture(t, "This is exe.dev's API.\nThis integration's key reaches only VMs tagged scope.\n")
	web := getExeVMSettings(t, s, "web")
	if web.Region != "Los Angeles, USA" || web.CPUs != 2 || web.ProxyPort != 8000 || web.Integration != "a" ||
		web.ScopeTag != "scope" || web.EmailAddress != "anything@web.exe.xyz" || len(web.Sharing.Users) != 1 ||
		len(web.ShareLinks) != 1 || web.ShareLinks[0].Token != "tok" {
		t.Errorf("settings %+v", web)
	}
	// The tag-scoped key may not run domain commands, which its probe
	// found, nor change tags.
	if !strings.Contains(web.Denied["domain-add"], "tagged scope") || web.Denied["tag-add"] == "" || len(web.Denied) != 4 {
		t.Errorf("denied %v", web.Denied)
	}
	// A VM shared with the user is read-only.
	if shared := getExeVMSettings(t, s, "shared"); len(shared.Denied) != len(exeVMChanges) {
		t.Errorf("shared VM may change: %v", shared.Denied)
	}
	if response := workspaceAPI(s, "GET", "/api/exe-vms/gone/settings", ""); response.Code != http.StatusNotFound {
		t.Errorf("unlisted VM: %d", response.Code)
	}
}

func TestExeVMSettingsCommandLimits(t *testing.T) {
	t.Parallel()
	s, _ := exeVMSettingsFixture(t, "It may run only: ls, share, domain ls.\n")
	denied := getExeVMSettings(t, s, "web").Denied
	for _, kind := range []string{"comment", "tag-add", "domain-add", "delete", "restart"} {
		if denied[kind] == "" {
			t.Errorf("%s allowed: %v", kind, denied)
		}
	}
	for _, kind := range []string{"port", "public", "share-add", "email"} {
		if denied[kind] != "" {
			t.Errorf("%s denied: %s", kind, denied[kind])
		}
	}
}

func TestExeVMChange(t *testing.T) {
	t.Parallel()
	s, changes := exeVMSettingsFixture(t, "This integration's key reaches only VMs tagged scope.\n")
	for _, tc := range []struct {
		vm, body string
		code     int
		command  string
	}{
		{"web", `{"kind":"comment","value":"it's staging"}`, 204, `comment 'web' 'it'\''s staging' --json`},
		{"web", `{"kind":"comment","value":""}`, 204, `comment 'web' ' ' --json`},
		{"web", `{"kind":"resize","cpus":4,"disk_gb":50}`, 204, `resize 'web' '--cpu=4' '--disk=50GB' --json`},
		{"web", `{"kind":"public"}`, 204, `share set-public 'web' --json`},
		{"web", `{"kind":"delete"}`, 204, `rm 'web' --json`},
		{"web", `{"kind":"pause"}`, 204, `pause 'web' --json`},
		{"web", `{"kind":"copy","value":"web-copy"}`, 204, `cp 'web' 'web-copy' --json`},
		// Never a flag or a second argument.
		{"web", `{"kind":"tag-add","value":"--json"}`, 400, ""},
		{"web", `{"kind":"rename","value":"a b"}`, 400, ""},
		{"web", `{"kind":"comment","value":"-d"}`, 400, ""},
		{"web", `{"kind":"port","value":"80; rm"}`, 400, ""},
		{"web", `{"kind":"ssh","value":"x"}`, 400, ""},
		{"web", `{"kind":"egress","value":"none"}`, 400, ""},
		// What the settings deny is refused before exe.dev hears of it.
		{"web", `{"kind":"domain-add","value":"x.example.com"}`, 403, ""},
		{"web", `{"kind":"tag-add","value":"prod"}`, 403, ""},
		{"shared", `{"kind":"restart"}`, 403, ""},
		// exe.dev refusing the key is said plainly.
		{"web", `{"kind":"port","value":"8080"}`, 403, ""},
	} {
		before := len(changes())
		response := workspaceAPI(s, "POST", "/api/exe-vms/"+tc.vm+"/settings", tc.body)
		if response.Code != tc.code {
			t.Errorf("%s: %d %s", tc.body, response.Code, response.Body.String())
		}
		if tc.code == 403 && strings.Contains(response.Body.String(), "{") {
			t.Errorf("%s: refusal is not plain: %s", tc.body, response.Body.String())
		}
		var ran []string
		if got := changes(); len(got) > before {
			ran = got[before:]
		}
		if tc.command == "" && len(ran) != 0 || tc.command != "" && !slices.Equal(ran, []string{tc.command}) {
			t.Errorf("%s ran %q, want %q", tc.body, ran, tc.command)
		}
	}
}

func TestExeVMChangeUnrestricted(t *testing.T) {
	t.Parallel()
	s, changes := exeVMSettingsFixture(t, "This is exe.dev's API.\n")
	if denied := getExeVMSettings(t, s, "web").Denied; len(denied) != 0 {
		t.Errorf("denied %v", denied)
	}
	response := workspaceAPI(s, "POST", "/api/exe-vms/web/settings", `{"kind":"tag-remove","value":"scope"}`)
	if want := []string{`tag '-d' 'web' 'scope' --json`}; response.Code != 204 || !slices.Equal(changes(), want) {
		t.Errorf("%d %s ran %q", response.Code, response.Body.String(), changes())
	}
}
