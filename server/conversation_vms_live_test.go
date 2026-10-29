package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// TestConversationVMLive copies a real VM, works on it over SSH as the agent would,
// and runs an action on it. It creates a VM on the account, so it runs only
// when asked:
//
//	SHELLEY_VM_TEST=BASE_VM,INTEGRATION,DIR go test ./server -run TestConversationVMLive
//
// DIR on BASE_VM must be a git repository. The
// test archives its conversation, which deletes the VM it created.
func TestConversationVMLive(t *testing.T) {
	config := strings.Split(os.Getenv("SHELLEY_VM_TEST"), ",")
	if len(config) != 3 {
		t.Skip("set SHELLEY_VM_TEST=BASE_VM,INTEGRATION,DIR")
	}
	// This test means to reach the integration (see refuseLinkLocalDials).
	refusing := http.DefaultTransport
	http.DefaultTransport = &http.Transport{}
	t.Cleanup(func() { http.DefaultTransport = refusing })
	s, database, _ := newTestServer(t)
	db.EnableWorkspaceMode(t, s.db)
	if _, err := s.ensureWorkspace(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.socketPath = os.Getenv("SHELLEY_SOCKET")
	project, err := database.CreateProject(t.Context(), db.Project{
		Name: "Live VM", Cwd: config[2], VM: &db.ProjectVM{Base: config[0], Integration: config[1]},
		Actions: []db.ProjectAction{{ID: "t", Label: "git status", Command: "git status --short && echo $(hostname) $PWD"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	conversation, release, err := s.createProjectConversation(t.Context(), nil, nil, db.ConversationOptions{Profile: project.Name}, project.Name)
	if err != nil {
		t.Fatal(err)
	}
	release()
	id := conversation.ConversationID
	s.workspace.workers.Wait() // the background preparation
	vm, err := database.GetConversationVM(t.Context(), id)
	if err != nil || vm.Status != "ready" {
		t.Fatalf("VM not ready: %+v %v", vm, err)
	}
	t.Logf("created VM %s (%s)", vm.Name, vm.HTTPSURL)
	t.Cleanup(func() {
		if t.Failed() {
			s.exeDev(context.Background(), config[1], "rm "+vm.Name+" --json")
		}
	})

	// The agent's tools run here, in the conversation's scratch directory; bash
	// and patch target the VM or this host.
	conversation, err = database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.conversationVMTools(t.Context(), conversation)
	if err != nil {
		t.Fatal(err)
	}
	scratch := derefString(conversation.Cwd)
	jobs := make(liveJobs, 1)
	ts := claudetool.NewToolSet(t.Context(), claudetool.ToolSetConfig{WorkingDir: scratch, ConversationID: id, BackgroundJobs: jobs, VM: target})
	tools := map[string]*llm.Tool{}
	for _, tool := range ts.Tools() {
		tools[tool.Name] = tool
	}
	run := func(name string, input map[string]any) string {
		t.Helper()
		data, _ := json.Marshal(input)
		out := tools[name].Run(t.Context(), data)
		if out.Error != nil {
			t.Fatalf("%s: %v", name, out.Error)
		}
		return out.LLMContent[0].Text
	}
	vmCall := func(name string, input map[string]any) string {
		t.Helper()
		input["ssh_target"] = vm.Name
		return run(name, input)
	}
	hostCall := func(name string, input map[string]any) string {
		t.Helper()
		input["ssh_target"] = claudetool.Localhost
		return run(name, input)
	}
	dir := config[2]
	if out := vmCall("bash", map[string]any{"command": "hostname; pwd; git rev-parse --show-toplevel"}); out != vm.Name+"\n"+dir+"\n"+dir+"\n" {
		t.Fatalf("bash on the VM: %q", out)
	}
	if out := hostCall("bash", map[string]any{"command": "pwd; ssh " + vm.Name + " hostname"}); out != scratch+"\n"+vm.Name+"\n" {
		t.Fatalf("bash here: %q, want scratch %q", out, scratch)
	}
	// patch edits VM files: a new one, then a replacement.
	vmCall("patch", map[string]any{"path": "live.txt", "patches": []map[string]any{{"operation": "overwrite", "newText": "from 'shelley' $HOME\n"}}})
	vmCall("patch", map[string]any{"path": dir + "/live.txt", "patches": []map[string]any{{"operation": "replace", "oldText": "'shelley'", "newText": `"shelley"`}}})
	if out := vmCall("bash", map[string]any{"command": "cat live.txt && git status --short"}); out != "from \"shelley\" $HOME\n?? live.txt\n" {
		t.Fatalf("edited file: %q", out)
	}
	if out := hostCall("bash", map[string]any{"command": "scp -q " + vm.Name + ":" + dir + "/live.txt . && cat live.txt"}); out != "from \"shelley\" $HOME\n" {
		t.Fatalf("scp: %q", out)
	}
	// A background VM command reports its exit.
	vmCall("bash", map[string]any{"command": "echo started; echo finished; exit 7", "background": true})
	job := <-jobs
	if outcome := job.Outcome(); outcome.ExitCode == nil || *outcome.ExitCode != 7 || !strings.Contains(outcome.Tail, "finished") {
		t.Fatalf("background job: %+v", outcome)
	}
	action := workspaceResponse[db.ActionRun](t, workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/t", ""), 202)
	waitForActions(t, s)
	finished, err := database.GetActionRun(t.Context(), action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "succeeded" || !strings.HasSuffix(finished.Output, vm.Name+" "+dir+"\n") {
		t.Fatalf("action: %s %q %s", finished.Status, finished.Output, finished.Error)
	}

	// Archiving deletes the VM, ending a background job on it first.
	vmCall("bash", map[string]any{"command": "exec sleep 3000", "background": true})
	if _, err := database.ArchiveConversation(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.cleanupConversationVM(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	<-jobs
	if vm, err := database.GetConversationVM(t.Context(), id); err != nil || vm.Status != db.ConversationVMDeleted {
		t.Fatalf("archived conversation's VM: %+v %v", vm, err)
	}
	if found, err := s.findExeDevVM(t.Context(), config[1], vm.Name); err != nil || found != nil {
		t.Fatalf("VM %s still listed: %+v %v", vm.Name, found, err)
	}
	if found, err := s.findExeDevVM(t.Context(), config[1], config[0]); err != nil || found == nil {
		t.Fatalf("base VM %s gone: %v", config[0], err)
	}
	t.Logf("deleted VM %s", vm.Name)
}

type liveJobs chan claudetool.BackgroundJob

func (j liveJobs) Background(_ context.Context, job claudetool.BackgroundJob, exited <-chan struct{}) error {
	go func() {
		<-exited
		j <- job
	}()
	return nil
}
