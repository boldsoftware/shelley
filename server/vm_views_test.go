package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/commitnotes"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// newVMViewsConversation starts a conversation of a project on shared VM
// "hub", whose fake ssh runs commands here, so its paths are this host's.
func newVMViewsConversation(t *testing.T) (s *Server, database *db.DB, id, repo string, ssh *fakeSSH) {
	t.Helper()
	s, database, _, _ = newActionFixture(t, "true", false)
	ssh = newFakeSSH(t, s)
	s.conversationVMs.exe = (&fakeExeDev{vms: map[string]string{"hub": "2026-01-01T00:00:00Z"}}).exec
	repo = t.TempDir()
	created := workspaceResponse[db.Project](t, workspaceAPI(s, "POST", "/api/projects",
		`{"name":"Hub","cwd":"`+repo+`","vm":{"shared_vm":"hub","integration":"me-exe"}}`), 201)
	conversation, release, err := s.createProjectConversation(t.Context(), nil, nil, db.ConversationOptions{Profile: created.Name}, created.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	id = conversation.ConversationID
	s.workspace.workers.Wait() // the background preparation
	if vm := conversationVMOf(t, database, id); vm.Status != db.ConversationVMReady {
		t.Fatalf("conversation VM: %+v", vm)
	}
	return s, database, id, repo, ssh
}

// The diff view, highlights, file editor, terminal and directory picker of a
// conversation on a VM work on the VM, over ssh, in its directory there.
func TestVMConversationViews(t *testing.T) {
	t.Parallel()
	s, database, id, repo, ssh := newVMViewsConversation(t)
	sub := filepath.Join(repo, "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", sub},
		// Writing the highlights note needs an identity too.
		{"-C", sub, "config", "user.name", "T"},
		{"-C", sub, "config", "user.email", "t@t"},
		{"-C", sub, "add", "main.go"},
		{"-C", sub, "commit", "-q", "-m", "first"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	head, err := commitnotes.CommitHash(commitnotes.Local(sub), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	note := `{"version":1,"files":["main.go"],"highlights":[{"file":"main.go","line":1,"comment":"the package"}]}`
	if err := commitnotes.WriteHighlights(commitnotes.Local(sub), head, []byte(note)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "notes.txt"), []byte("draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The agent moves to the repository on the host, moving the conversation.
	manager, err := s.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	target, scratch, err := s.conversationVMTools(t.Context(), conversation)
	if err != nil {
		t.Fatal(err)
	}
	var changeDir *llm.Tool
	for _, tool := range claudetool.NewToolSet(t.Context(), claudetool.ToolSetConfig{
		WorkingDir: repo, HostDir: scratch, VM: target,
		OnWorkingDirChange: func(dir string) {
			if err := manager.SetCwd(context.WithoutCancel(t.Context()), dir); err != nil {
				t.Error(err)
			}
		},
	}).Tools() {
		if tool.Name == "change_dir" {
			changeDir = tool
		}
	}
	if out := changeDir.Run(t.Context(), json.RawMessage(`{"path":"app","ssh_target":"hub"}`)); out.Error != nil || !strings.Contains(out.LLMContent[0].Text, "Git repository detected") {
		t.Fatalf("change_dir on hub: %v %+v", out.Error, out.LLMContent)
	}
	if conversation, err = database.GetConversationByID(t.Context(), id); err != nil || derefString(conversation.Cwd) != sub {
		t.Fatalf("conversation cwd after change_dir: %q %v", derefString(conversation.Cwd), err)
	}

	// Each view's request goes over ssh.
	viaSSH := func(t *testing.T, what string, request func()) {
		t.Helper()
		before := len(ssh.calls(t, "run"))
		request()
		if len(ssh.calls(t, "run")) == before {
			t.Fatalf("%s did not use ssh", what)
		}
	}
	serve := func(handler http.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(method, target, strings.NewReader(body)))
		return response
	}
	query := "conversation_id=" + id + "&cwd=" + url.QueryEscape(sub)
	viaSSH(t, "diff list", func() {
		diffs := serve(s.handleGitDiffs, "GET", "/api/git/diffs?"+query, "")
		if diffs.Code != 200 || !strings.Contains(diffs.Body.String(), `"gitRoot":"`+sub+`"`) || !strings.Contains(diffs.Body.String(), `"hasHighlights":true`) {
			t.Fatalf("diffs: %d %s", diffs.Code, diffs.Body)
		}
	})
	viaSSH(t, "diff files", func() {
		files := serve(s.handleGitDiffFiles, "GET", "/api/git/diffs/working/files?"+query, "")
		if files.Code != 200 || !strings.Contains(files.Body.String(), "notes.txt") {
			t.Fatalf("diff files: %d %s", files.Code, files.Body)
		}
	})
	viaSSH(t, "highlights", func() {
		highlights := serve(s.handleGitHighlights, "GET", "/api/git/highlights?"+query+"&hash="+head, "")
		if highlights.Code != 200 || !strings.Contains(highlights.Body.String(), "the package") {
			t.Fatalf("highlights: %d %s", highlights.Code, highlights.Body)
		}
	})
	file := url.QueryEscape(filepath.Join(sub, "notes.txt"))
	viaSSH(t, "file read", func() {
		read := serve(s.handleReadFile, "GET", "/api/read-file?conversation_id="+id+"&path="+file, "")
		if read.Code != 200 || !strings.Contains(read.Body.String(), "draft") {
			t.Fatalf("read: %d %s", read.Code, read.Body)
		}
	})
	viaSSH(t, "file write", func() {
		body, _ := json.Marshal(map[string]string{"path": filepath.Join(sub, "notes.txt"), "content": "final\n"})
		if write := serve(s.handleWriteFile, "POST", "/api/write-file?conversation_id="+id, string(body)); write.Code != 200 {
			t.Fatalf("write: %d %s", write.Code, write.Body)
		}
	})
	if data, err := os.ReadFile(filepath.Join(sub, "notes.txt")); err != nil || string(data) != "final\n" {
		t.Fatalf("written file: %q %v", data, err)
	}

	// The terminal runs on the host, in the conversation's directory there.
	cmd, dir, err := s.terminalCommand(t.Context(), id, `printf '%s|%s' "$VIA_VM" "$PWD"`, sub)
	if err != nil || dir != scratch {
		t.Fatalf("terminal: %q in %q: %v", cmd, dir, err)
	}
	if !strings.Contains(cmd, " '-tt' 'hub' ") {
		t.Fatalf("terminal without a terminal: %s", cmd)
	}
	terminal := exec.Command("bash", "-c", cmd)
	terminal.Dir = dir
	if out, err := terminal.CombinedOutput(); err != nil || string(out) != "1|"+sub {
		t.Fatalf("terminal output: %q %v", out, err)
	}

	// Setting the directory checks it on the VM.
	mux := http.NewServeMux()
	s.registerConversationRoutes(mux)
	setCwd := func(cwd string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest("POST", "/api/conversation/"+id+"/cwd", strings.NewReader(`{"cwd":"`+cwd+`"}`)))
		return response
	}
	if response := setCwd(filepath.Join(repo, "missing")); response.Code != 400 {
		t.Fatalf("cwd to a missing directory: %d %s", response.Code, response.Body)
	}
	viaSSH(t, "cwd change", func() {
		if response := setCwd(repo); response.Code != 200 {
			t.Fatalf("cwd: %d %s", response.Code, response.Body)
		}
	})
	if got := manager.Cwd(); got != repo {
		t.Fatalf("manager cwd %q", got)
	}

	// The directory picker browses and makes directories on the VM.
	list := func(p string) ListDirectoryResponse {
		t.Helper()
		var listing ListDirectoryResponse
		viaSSH(t, "listing "+p, func() {
			response := serve(s.handleListDirectory, "GET", "/api/list-directory?conversation_id="+id+"&path="+url.QueryEscape(p), "")
			if response.Code != 200 {
				t.Fatalf("list %s: %d %s", p, response.Code, response.Body)
			}
			if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil {
				t.Fatal(err)
			}
		})
		return listing
	}
	if got := list(repo); got.Path != repo || got.Parent != filepath.Dir(repo) || got.GitRepoRoot != "" ||
		len(got.Entries) != 1 || got.Entries[0] != (DirectoryEntry{Name: "app", IsDir: true, GitHeadSubject: "first"}) {
		t.Fatalf("listing the project: %+v", got)
	}
	if got := list(sub + "/"); got.Path != sub || got.GitRepoRoot != sub || got.GitHeadSubject != "first" || got.GitWorktreeRoot != "" {
		t.Fatalf("listing the repository: %+v", got)
	}
	if got := list(filepath.Join(sub, "notes.txt")); got.Error != "path is not a directory" {
		t.Fatalf("listing a file: %+v", got)
	}
	mkdir := func(p string) string {
		t.Helper()
		response := serve(s.handleCreateDirectory, "POST", "/api/create-directory?conversation_id="+id, `{"path":"`+p+`"}`)
		if response.Code != 200 {
			t.Fatalf("mkdir %s: %d %s", p, response.Code, response.Body)
		}
		return response.Body.String()
	}
	viaSSH(t, "mkdir", func() {
		if got := mkdir(filepath.Join(repo, "made")); !strings.Contains(got, `"path":"`+filepath.Join(repo, "made")+`"`) {
			t.Fatalf("mkdir: %s", got)
		}
	})
	if info, err := os.Stat(filepath.Join(repo, "made")); err != nil || !info.IsDir() {
		t.Fatalf("made directory: %v", err)
	}
	if got := mkdir(filepath.Join(repo, "made")); !strings.Contains(got, "path already exists") {
		t.Fatalf("mkdir again: %s", got)
	}
	if got := mkdir(filepath.Join(repo, "no", "such")); !strings.Contains(got, "parent directory does not exist") {
		t.Fatalf("mkdir without a parent: %s", got)
	}
}
