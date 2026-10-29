package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// fakeSSH stands in for ssh: it runs commands on this machine, marked
// VIA_VM=1, keeps control masters as marker files, and logs each call as
// "MODE HOST" (run, master, check, exit). A master fails with the contents
// of its fail file, if there is one.
type fakeSSH struct{ dir string }

func newFakeSSH(t *testing.T, s *Server) *fakeSSH {
	t.Helper()
	f := &fakeSSH{dir: t.TempDir()}
	script := `#!/bin/sh
dir=` + shellJoin([]string{f.dir}) + `
mode=run
errlog=/dev/stderr
config=
ctl=
while [ $# -gt 0 ]; do
	case $1 in
	-O) mode=$2; shift 2;;
	-E) errlog=$2; shift 2;;
	-F) config=$2; shift 2;;
	-o) case $2 in ControlPath=*) ctl=${2#ControlPath=};; esac; shift 2;;
	-L|-R) shift 2;;
	-N) mode=master; shift;;
	-*) shift;;
	*) break;;
	esac
done
host=$1; shift
echo "$mode $host" >>"$dir/log"
# Like ssh, the control socket (here a marker file) is the -o ControlPath,
# else the one the -F configuration gives the host.
if [ -z "$ctl" ] && [ -f "$config" ]; then
	ctl=$(awk -v h="$host" '$1 == "Match" { m = 0; n = split($3, a, ","); for (i = 1; i <= n; i++) if (a[i] == h) m = 1 } m && $1 == "ControlPath" { print $2; exit }' "$config")
fi
case $mode in
check|exit|master) if [ -z "$ctl" ]; then echo "No ControlPath specified for \"$host\"" >&2; exit 255; fi;;
esac
case $mode in
check) [ -e "$ctl" ]; exit;;
exit) [ -e "$ctl" ] || exit 255; rm -f "$ctl"; exit 0;;
master)
	if [ -e "$dir/fail" ]; then cat "$dir/fail" >>"$errlog"; exit 255; fi
	touch "$ctl"; exit 0;;
esac
exec env VIA_VM=1 sh -c "$*"
`
	if err := os.WriteFile(filepath.Join(f.dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s.conversationVMs.ssh = filepath.Join(f.dir, "ssh")
	s.conversationVMs.sshConfigDir = filepath.Join(f.dir, "config")
	// Its own conversation VM state, beside its own database.
	s.toolSetConfig.DBPath = filepath.Join(f.dir, "shelley.db")
	// The integrations tests' VMs use, as if attached to this host.
	s.conversationVMs.hosts = map[string]string{"exe": "exe.int.exe.test", "me-exe": "me-exe.int.exe.test"}
	return f
}

// conversationPrompt returns the instructions conversation id's manager adds
// to each model request.
func conversationPrompt(t *testing.T, s *Server, id string) (string, error) {
	t.Helper()
	manager, err := s.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		return "", err
	}
	if manager.decorateService == nil {
		return "", nil
	}
	service, err := manager.decorateService(nil)
	if err != nil {
		return "", err
	}
	return service.(*workspaceService).instructions(t.Context())
}

// calls returns the logged calls of mode.
func (f *fakeSSH) calls(t *testing.T, mode string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, "log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var hosts []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if m, host, ok := strings.Cut(line, " "); ok && m == mode {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func (f *fakeSSH) fail(t *testing.T, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "fail"), []byte(message), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newConversationVMFixture(t *testing.T, command string) (*Server, *db.DB, string, string, *fakeSSH, *fakeExeDev) {
	t.Helper()
	s, database, id, cwd := newActionFixture(t, command, false)
	ssh := newFakeSSH(t, s)
	exe := &fakeExeDev{vms: map[string]string{}}
	s.conversationVMs.exe = exe.exec
	if err := database.BindConversationVM(t.Context(), id, db.ProjectVM{Base: "base", Integration: "exe"}); err != nil {
		t.Fatal(err)
	}
	vm, err := database.GetConversationVM(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	vm.Dir = cwd
	if err := database.UpdateConversationVM(t.Context(), *vm); err != nil {
		t.Fatal(err)
	}
	return s, database, id, cwd, ssh, exe
}

func TestProjectVMConfig(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newActionFixture(t, "true", false)
	ssh := newFakeSSH(t, s)
	s.conversationVMs.exe = (&fakeExeDev{vms: map[string]string{}}).exec
	// A VM project's cwd is on its VMs, so it need not exist here.
	created := workspaceResponse[db.Project](t, workspaceAPI(s, "POST", "/api/projects",
		`{"name":"Remote","cwd":"/nonexistent/repo","vm":{"base":"repo-base","integration":"me-exe"}}`), 201)
	workspace := workspaceResponse[db.Workspace](t, workspaceAPI(s, "GET", "/api/workspace", ""), 200)
	var found *db.Project
	for i := range workspace.Projects {
		if workspace.Projects[i].Name == created.Name {
			found = &workspace.Projects[i]
		}
	}
	if found == nil || found.VM == nil || *found.VM != (db.ProjectVM{Base: "repo-base", Integration: "me-exe"}) {
		t.Fatalf("project VM not persisted: %+v", found)
	}

	conversation, release, err := s.createProjectConversation(t.Context(), nil, nil, db.ConversationOptions{Profile: created.Name}, created.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	waitForActions(t, s)
	name := "repo-base-" + strings.ToLower(conversation.ConversationID)
	if masters := ssh.calls(t, "master"); len(masters) != 1 || masters[0] != name {
		t.Fatalf("new conversation's VM masters: %v", masters)
	}
	// The conversation's tools run here, in its scratch directory.
	scratch, err := s.vmScratchDir(conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	vm, err := s.db.GetConversationVM(t.Context(), conversation.ConversationID)
	if err != nil || vm.Name != name || vm.Status != db.ConversationVMReady || derefString(conversation.Cwd) != scratch {
		t.Fatalf("conversation VM: %+v %v cwd=%q", vm, err, derefString(conversation.Cwd))
	}
	if info, err := os.Stat(scratch); err != nil || !info.IsDir() {
		t.Fatalf("scratch directory: %v", err)
	}
	// Subagents work on their conversation's VM.
	child, err := s.db.CreateSubagentConversation(t.Context(), "helper", conversation.ConversationID, conversation.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	if childVM, err := s.db.GetConversationVM(t.Context(), child.ConversationID); err != nil || childVM.Name != vm.Name {
		t.Fatalf("subagent VM: %+v %v", childVM, err)
	}
	if prompt, err := conversationPrompt(t, s, child.ConversationID); err != nil || !strings.Contains(prompt, `ssh_target "`+vm.Name+`": runs over ssh on `+vm.Name+". bash commands start in /nonexistent/repo") {
		t.Fatalf("subagent prompt lacks its VM: %v %q", err, prompt)
	}

	for _, body := range []string{
		`{"name":"Bad","cwd":"/r","vm":{"base":"Bad_Name","integration":"me-exe"}}`,
		`{"name":"Bad","cwd":"","vm":{"base":"base","integration":"me-exe"}}`,
	} {
		if response := workspaceAPI(s, "POST", "/api/projects", body); response.Code != 400 {
			t.Errorf("%s: %d %s", body, response.Code, response.Body.String())
		}
	}
	// Omitting vm turns conversation VMs off for new conversations; existing ones keep theirs.
	if response := workspaceAPI(s, "PUT", "/api/projects/"+url.PathEscape(created.Name), `{"name":"Remote","cwd":"/tmp"}`); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	project, err := s.db.GetProject(t.Context(), created.Name)
	if err != nil || project.VM != nil {
		t.Fatalf("VM not removed: %+v %v", project, err)
	}
	if _, err := s.db.GetConversationVM(t.Context(), conversation.ConversationID); err != nil {
		t.Fatal(err)
	}
}

// A VM conversation's tools run here, from its scratch directory; bash and patch
// target the VM (over SSH, in the project's directory) or this host. Its
// actions run on the VM, in the project's directory.
func TestConversationVMWorksOverSSH(t *testing.T) {
	t.Parallel()
	s, database, id, cwd, ssh, _ := newConversationVMFixture(t,
		`printf '%s|%s|%s|%s|%s' "$VIA_VM" "$PWD" "$SHELLEY_CONVERSATION_ID" "$SHELLEY_URL" "$SHELLEY_SOCKET"`)
	s.socketPath = "/tmp/local-only.sock"
	name := "base-" + strings.ToLower(id)
	// An instance that has exited left its configuration behind.
	exited := exec.Command("true")
	if err := exited.Run(); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(s.conversationVMs.sshConfigDir, "stale.conf")
	if err := os.MkdirAll(s.conversationVMs.sshConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte(fmt.Sprintf("Match host old exec \"kill -0 %d 2>/dev/null\"\n", exited.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := s.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	if scratch, err := s.vmScratchDir(id); err != nil || manager.Cwd() != scratch {
		t.Fatalf("conversation tools run in %q, not its scratch directory", manager.Cwd())
	}
	prompt, err := conversationPrompt(t, s, id)
	if err != nil || !strings.Contains(prompt, "<project_vm>") || !strings.Contains(prompt, `ssh_target "`+name+`": runs over ssh on `+name+". bash commands start in "+cwd) ||
		!strings.Contains(prompt, "actions (Run command) run on the VM") {
		t.Fatalf("conversation prompt lacks its VM: %v\n%s", err, prompt)
	}

	// bash and patch name their target.
	conversation, err := database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	vm, err := s.conversationVMTools(t.Context(), conversation)
	if err != nil || vm == nil || vm.Name() != name || vm.Dir() != cwd {
		t.Fatalf("conversation VM tools: %v %v", vm, err)
	}
	tools := map[string]*llm.Tool{}
	for _, tool := range claudetool.NewToolSet(t.Context(), claudetool.ToolSetConfig{WorkingDir: manager.Cwd(), VM: vm}).Tools() {
		tools[tool.Name] = tool
	}
	call := func(tool, input string) llm.ToolOut {
		t.Helper()
		return tools[tool].Run(t.Context(), json.RawMessage(input))
	}
	if out := call("bash", `{"command":"printf '%s|%s' \"$VIA_VM\" \"$PWD\"","ssh_target":"`+name+`"}`); out.Error != nil || out.LLMContent[0].Text != "1|"+cwd {
		t.Fatalf("bash on the VM: %v %+v", out.Error, out.LLMContent)
	}
	if out := call("bash", `{"command":"printf '%s|%s' \"$VIA_VM\" \"$PWD\"","ssh_target":"localhost"}`); out.Error != nil || out.LLMContent[0].Text != "|"+manager.Cwd() {
		t.Fatalf("bash here: %v %+v", out.Error, out.LLMContent)
	}
	if out := call("bash", `{"command":"true"}`); out.Error == nil || !strings.Contains(out.Error.Error(), "ssh_target is required") {
		t.Fatalf("bash without ssh_target: %v", out.Error)
	}
	if out := call("patch", `{"path":"notes.txt","ssh_target":"`+name+`","patches":[{"operation":"overwrite","newText":"on the VM\n"}]}`); out.Error != nil {
		t.Fatal(out.Error)
	}
	if data, err := os.ReadFile(filepath.Join(cwd, "notes.txt")); err != nil || string(data) != "on the VM\n" {
		t.Fatalf("patched VM file: %q %v", data, err)
	}

	run := workspaceResponse[db.ActionRun](t, workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/run", ""), 202)
	waitForActions(t, s)
	finished, err := database.GetActionRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := "1|" + cwd + "|" + id + "||"; finished.Status != "succeeded" || finished.Output != want {
		t.Fatalf("action: %s %q %s, want %q", finished.Status, finished.Output, finished.Error, want)
	}
	if masters := ssh.calls(t, "master"); len(masters) != 1 || masters[0] != name {
		t.Fatalf("masters: %v", masters)
	}

	// The SSH configuration names the VM, so "ssh NAME" works here, and
	// the exited instance's is gone.
	config, err := s.vmSSHConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(config)
	if err != nil || !strings.Contains(string(data), "Match host "+name+","+name+".exe.test exec") || strings.Contains(string(data), "ProxyCommand") {
		t.Fatalf("SSH configuration: %v\n%s", err, data)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale configuration: %v", err)
	}
}

func TestConversationVMActionFailsWithoutFallback(t *testing.T) {
	t.Parallel()
	s, database, id, _, ssh, exe := newConversationVMFixture(t, "echo ran")
	// An existing VM is not retried: a new copy's SSH may take a while.
	exe.vms["base-"+strings.ToLower(id)] = "2026-10-29T00:00:00Z"
	ssh.fail(t, "VM unreachable")
	run := workspaceResponse[db.ActionRun](t, workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/run", ""), 202)
	waitForActions(t, s)
	finished, err := database.GetActionRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "failed" || finished.Output != "" || !strings.Contains(finished.Error, "VM unreachable") {
		t.Fatalf("action: %s %q %q", finished.Status, finished.Output, finished.Error)
	}
	// Connecting fails with the reason, not an unknown host.
	config, err := s.vmSSHConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(config)
	if err != nil || !strings.Contains(string(data), "ProxyCommand") {
		t.Fatalf("SSH configuration: %v\n%s", err, data)
	}
	dir, err := s.conversationVMDir()
	if err != nil {
		t.Fatal(err)
	}
	status, err := os.ReadFile(filepath.Join(dir, "base-"+strings.ToLower(id)+".status"))
	if err != nil || !strings.Contains(string(status), "VM unreachable") {
		t.Fatalf("status: %v %q", err, status)
	}
}
