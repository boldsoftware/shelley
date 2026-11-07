package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// startSSHD returns the path of an ssh client configuration naming host
// "devbox", as a user's ~/.ssh/config would: a real sshd, run as this user in
// inetd mode for each connection, accepting a key of the configuration's.
func startSSHD(t *testing.T) string {
	t.Helper()
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		if sshd = "/usr/sbin/sshd"; !fileExists(sshd) {
			t.Skip("no sshd")
		}
	}
	dir := t.TempDir()
	for _, key := range []string{"host", "user"} {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, key)).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v: %s", err, out)
		}
	}
	config := fmt.Sprintf("HostKey %s\nAuthorizedKeysFile %s.pub\nPidFile none\nStrictModes no\nUsePAM no\nLogLevel ERROR\n",
		filepath.Join(dir, "host"), filepath.Join(dir, "user"))
	if err := os.WriteFile(filepath.Join(dir, "sshd_config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	client := filepath.Join(dir, "ssh_config")
	if err := os.WriteFile(client, []byte(fmt.Sprintf("Host devbox\n\tProxyCommand %s -i -e -f %s\n\tIdentityFile %s\n\tIdentitiesOnly yes\n\tIdentityAgent none\n\tUserKnownHostsFile %s\n",
		sshd, filepath.Join(dir, "sshd_config"), filepath.Join(dir, "user"), filepath.Join(dir, "known_hosts"))), 0o600); err != nil {
		t.Fatal(err)
	}
	return client
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// A project on an SSH host runs its conversations there over real ssh, with this
// host's own SSH configuration and keys (here a client configuration standing in
// for ~/.ssh/config), and never touches exe.dev.
func TestSSHHostProject(t *testing.T) {
	t.Parallel()
	client := startSSHD(t)
	s, database, _, _ := newActionFixture(t, "true", false)
	state := t.TempDir()
	wrapper := filepath.Join(state, "ssh")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec ssh -F "+shellJoin([]string{client})+` "$@"`+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.conversationVMs.ssh = wrapper
	s.conversationVMs.sshConfigDir = filepath.Join(state, "config")
	s.toolSetConfig.DBPath = filepath.Join(state, "shelley.db")
	s.conversationVMs.exe = func(_ context.Context, integration, command string) ([]byte, error) {
		t.Errorf("exe.dev %s: %s", integration, command)
		return nil, fmt.Errorf("no exe.dev")
	}

	repo := t.TempDir() // sshd runs here, as this user, so its paths are ours
	action := `printf '%s|%s|%s' "${SSH_CLIENT:+ssh}" "$PWD" "$SHELLEY_CONVERSATION_ID"`
	body, _ := json.Marshal(map[string]any{
		"name": "Box", "cwd": repo, "vm": map[string]string{"shared_vm": "devbox", "integration": ""},
		// Silent: a report would start a turn, whose VM preparation adds a
		// server worker while waitForActions waits for them.
		"actions": []map[string]any{{"id": "where", "label": "Where", "command": action, "silent": true}},
	})
	created := workspaceResponse[db.Project](t, workspaceAPI(s, "POST", "/api/projects", string(body)), 201)
	if created.VM == nil || *created.VM != (db.ProjectVM{SharedVM: "devbox"}) {
		t.Fatalf("SSH host not persisted: %+v", created.VM)
	}
	conversation, release, err := s.createProjectConversation(t.Context(), nil, nil, db.ConversationOptions{Profile: created.Name}, created.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	id := conversation.ConversationID
	s.workspace.workers.Wait() // the background preparation
	vm := conversationVMOf(t, database, id)
	if vm.Status != db.ConversationVMReady || vm.Name != "devbox" || !vm.Shared || vm.Integration != "" || vm.Base != "" {
		t.Fatalf("conversation host: %+v", vm)
	}
	t.Cleanup(func() { s.closeConversationVMConnection(vm) })

	prompt, err := conversationPrompt(t, s, id)
	if err != nil || !strings.Contains(prompt, `ssh_target "devbox": runs over ssh on devbox, in your working directory there, which starts as `+repo) || strings.Contains(prompt, "exe.dev") {
		t.Fatalf("conversation prompt: %v\n%s", err, prompt)
	}

	conversation, err = database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	target, scratch, err := s.conversationVMTools(t.Context(), conversation)
	if err != nil || target == nil {
		t.Fatalf("conversation tools: %v %v", target, err)
	}
	tools := map[string]*llm.Tool{}
	for _, tool := range claudetool.NewToolSet(t.Context(), claudetool.ToolSetConfig{WorkingDir: repo, HostDir: scratch, VM: target}).Tools() {
		tools[tool.Name] = tool
	}
	if out := tools["bash"].Run(t.Context(), json.RawMessage(`{"command":"printf '%s|%s' \"${SSH_CLIENT:+ssh}\" \"$PWD\"","ssh_target":"devbox"}`)); out.Error != nil || out.LLMContent[0].Text != "ssh|"+repo {
		t.Fatalf("bash on devbox: %v %+v", out.Error, out.LLMContent)
	}
	if out := tools["patch"].Run(t.Context(), json.RawMessage(`{"path":"notes.txt","ssh_target":"devbox","patches":[{"operation":"overwrite","newText":"over ssh\n"}]}`)); out.Error != nil {
		t.Fatal(out.Error)
	}
	if data, err := os.ReadFile(filepath.Join(repo, "notes.txt")); err != nil || string(data) != "over ssh\n" {
		t.Fatalf("patched file: %q %v", data, err)
	}

	run := workspaceResponse[db.ActionRun](t, workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/where", ""), 202)
	waitForActions(t, s)
	finished, err := database.GetActionRun(t.Context(), run.ID)
	if want := "ssh|" + repo + "|" + id; err != nil || finished.Status != "succeeded" || finished.Output != want {
		t.Fatalf("action: %v %s %q %s, want %q", err, finished.Status, finished.Output, finished.Error, want)
	}

	// The host is the user's: Shelley's SSH configuration leaves it alone.
	if path, err := s.vmSSHConfigPath(); err != nil || fileExists(path) {
		t.Fatalf("Shelley SSH configuration written for an SSH host: %v", err)
	}
}
