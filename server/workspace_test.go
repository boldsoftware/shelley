package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

func workspaceAPI(s *Server, method, path, body string) *httptest.ResponseRecorder {
	api := http.NewServeMux()
	s.registerWorkspaceRoutes(api)
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	api.ServeHTTP(recorder, request)
	return recorder
}

func workspaceResponse[T any](t *testing.T, response *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want %d: %s", response.Code, status, response.Body.String())
	}
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

// newActionFixture makes a conversation, and a project with one action,
// "run", to move it to.
func newActionFixture(t *testing.T, command string, archive bool) (*Server, *db.DB, string, string) {
	t.Helper()
	s, database, _ := newTestServer(t)
	cwd := t.TempDir()
	project, err := database.CreateProject(t.Context(), db.Project{
		Name: "Commands", Cwd: cwd,
		Actions: []db.ProjectAction{{ID: "run", Label: "Run command", Command: command, ArchiveOnSuccess: archive}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	model := "predictable"
	conversation, err := database.CreateConversation(t.Context(), nil, true, &cwd, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MoveConversation(t.Context(), conversation.ConversationID, project.Name); err != nil {
		t.Fatal(err)
	}
	db.AddTestProject(t, database, "my-project") // a move target
	t.Cleanup(func() {
		select {
		case <-s.shutdownCh:
		default:
			close(s.shutdownCh)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.waitActions(ctx); err != nil {
			t.Errorf("waiting for action processes: %v", err)
		}
	})
	return s, database, conversation.ConversationID, cwd
}

func waitForActions(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.waitActions(ctx); err != nil {
		t.Fatal(err)
	}
}

func outputSignal(t *testing.T, database *db.DB, text string) <-chan struct{} {
	t.Helper()
	ready := make(chan struct{})
	var once sync.Once
	database.Pool().OnCommit(func() {
		workspace, err := database.GetWorkspace(context.Background())
		if err != nil {
			return
		}
		for _, summary := range workspace.Runs {
			run, err := database.GetActionRun(context.Background(), summary.ID)
			if err == nil && strings.Contains(run.Output, text) {
				once.Do(func() { close(ready) })
			}
		}
	})
	return ready
}

func awaitConversationSignal(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("action did not produce its readiness signal")
	}
}

func TestWorkspaceHTTPAndProjectCRUD(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	workspace := workspaceResponse[db.Workspace](t, workspaceAPI(s, "GET", "/api/workspace", ""), 200)
	if len(workspace.Projects) != 0 || len(workspace.Conversations) != 0 || workspace.Runs == nil {
		t.Fatalf("unexpected initial workspace: %+v", workspace)
	}
	project := workspaceResponse[db.Project](t, workspaceAPI(s, "POST", "/api/projects",
		`{"name":"Docs","instructions":"Use examples.","cwd":"","actions":[]}`), 201)
	project.Name = "Documentation"
	encoded, _ := json.Marshal(project)
	updated := workspaceResponse[db.Project](t, workspaceAPI(s, "PUT", "/api/projects/Docs", string(encoded)), 200)
	if updated.Name != "Documentation" || updated.Instructions != "Use examples." {
		t.Fatalf("project not replaced: %+v", updated)
	}
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Conversations outside a project's profile are not members, and get no
	// project instructions, until moved into one.
	if after := workspaceResponse[db.Workspace](t, workspaceAPI(s, "GET", "/api/workspace", ""), 200); len(after.Conversations) != 0 {
		t.Fatalf("conversation joined a project implicitly: %+v", after.Conversations)
	}
	if instructions, err := s.workspaceInstructions(t.Context(), conv.ConversationID); err != nil || instructions != "" {
		t.Fatalf("instructions outside a project: %q %v", instructions, err)
	}
	moved := workspaceResponse[db.WorkspaceConversation](t, workspaceAPI(s, "PUT", "/api/conversation/"+conv.ConversationID+"/project",
		`{"project":"Documentation"}`), 200)
	if moved.Project != "Documentation" {
		t.Fatalf("move: %+v", moved)
	}
	if response := workspaceAPI(s, "DELETE", "/api/projects/Documentation", ""); response.Code != 409 || !strings.Contains(response.Body.String(), "has an active conversation") {
		t.Fatalf("deleted project with an active conversation: %d %s", response.Code, response.Body.String())
	}
	if _, err := database.ArchiveConversation(t.Context(), conv.ConversationID); err != nil {
		t.Fatal(err)
	}
	db.AddTestProject(t, database, "my-project")
	workspaceResponse[db.WorkspaceConversation](t, workspaceAPI(s, "PUT", "/api/conversation/"+conv.ConversationID+"/project", `{"project":"my-project"}`), 200)
	if response := workspaceAPI(s, "DELETE", "/api/projects/Documentation", ""); response.Code != 200 {
		t.Fatalf("delete empty project: %d %s", response.Code, response.Body.String())
	}
	if response := workspaceAPI(s, "POST", "/api/projects", `{"name":"Assistant"}`); response.Code != 409 {
		t.Fatalf("project named like a profile: %d %s", response.Code, response.Body.String())
	}
	for _, request := range []struct{ method, path, body string }{
		{"POST", "/api/projects", `{"name":"Bad","actions":[{"id":"x","label":"Run","command":""}]}`},
		{"POST", "/api/projects", `{"name":"Bad","cwd":"/nonexistent-task-directory"}`},
		{"POST", "/api/projects", `{"name":"Bad/Name"}`},
		{"POST", "/api/projects", `{"name":"Bad","description":"Old field"}`},
		{"POST", "/api/projects", `{"name":"Bad","copy_settings_from":"Nope"}`},
		{"PUT", "/api/projects/my-project", `{"name":"my-project","copy_settings_from":"Assistant"}`},
		{"PUT", "/api/conversation/" + conv.ConversationID + "/project", `{"project":""}`},
	} {
		response := workspaceAPI(s, request.method, request.path, request.body)
		if response.Code != 400 {
			t.Fatalf("%s %s %s: status=%d body=%s", request.method, request.path, request.body, response.Code, response.Body.String())
		}
	}
}

func TestActionHTTPOutlivesRequestAndArchivesOnSuccess(t *testing.T) {
	t.Parallel()
	s, database, id, cwd := newActionFixture(t, "printf 'stdout\\n'; printf 'stderr\\n' >&2; pwd", true)
	api := http.NewServeMux()
	s.registerWorkspaceRoutes(api)
	ctx, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequest("POST", "/api/conversation/"+id+"/actions/run", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	cancel()
	run := workspaceResponse[db.ActionRun](t, response, 202)
	if run.Status != "running" {
		t.Fatalf("initial run: %+v", run)
	}
	waitForActions(t, s)
	finished := workspaceResponse[db.ActionRun](t, workspaceAPI(s, "GET", "/api/action-runs/"+run.ID, ""), 200)
	if finished.Status != "succeeded" || finished.ExitCode == nil || *finished.ExitCode != 0 ||
		finished.FinishedAt == nil || !strings.Contains(finished.Output, "stdout\n") ||
		!strings.Contains(finished.Output, "stderr\n") || !strings.Contains(finished.Output, cwd) {
		t.Fatalf("finished run: %+v", finished)
	}
	conversation, err := database.GetConversationByID(t.Context(), id)
	if err != nil || !conversation.Archived {
		t.Fatalf("successful archive: %+v %v", conversation, err)
	}
}

func TestActionHTTPFailureRetainsConversationAndOutput(t *testing.T) {
	t.Parallel()
	s, database, id, _ := newActionFixture(t, "printf 'failure detail\\n' >&2; exit 23", true)
	run := workspaceResponse[db.ActionRun](t, workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/run", ""), 202)
	waitForActions(t, s)
	finished, err := database.GetActionRun(t.Context(), run.ID)
	if err != nil || finished.Status != "failed" || finished.ExitCode == nil ||
		*finished.ExitCode != 23 || finished.Error == "" || !strings.Contains(finished.Output, "failure detail") {
		t.Fatalf("failed command: %+v %v", finished, err)
	}
	conversation, err := database.GetConversationByID(t.Context(), id)
	if err != nil || conversation.Archived {
		t.Fatalf("failed command archived conversation: %+v %v", conversation, err)
	}
}

func TestActionHTTPDuplicateBusyAndInvalidDirectory(t *testing.T) {
	t.Parallel()
	s, database, id, cwd := newActionFixture(t, "printf ready; read line < gate; printf done", true)
	if err := syscall.Mkfifo(filepath.Join(cwd, "gate"), 0o600); err != nil {
		t.Fatal(err)
	}
	ready := outputSignal(t, database, "ready")
	run := workspaceResponse[db.ActionRun](t, workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/run", ""), 202)
	awaitConversationSignal(t, ready)
	for _, request := range []struct{ method, path, body string }{
		{"POST", "/api/conversation/" + id + "/actions/run", ""},
		{"PUT", "/api/conversation/" + id + "/project", `{"project":"my-project"}`},
	} {
		response := workspaceAPI(s, request.method, request.path, request.body)
		if response.Code != 409 {
			t.Fatalf("busy mutation %s: %d %s", request.path, response.Code, response.Body.String())
		}
	}
	if err := os.WriteFile(filepath.Join(cwd, "gate"), []byte("continue\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForActions(t, s)
	finished, err := database.GetActionRun(t.Context(), run.ID)
	if err != nil || finished.Status != "succeeded" {
		t.Fatalf("gated command: %+v %v", finished, err)
	}

	s2, database2, id2, _ := newActionFixture(t, "true", true)
	if err := database2.UpdateConversationCwd(t.Context(), id2, "/missing-task-directory"); err != nil {
		t.Fatal(err)
	}
	if response := workspaceAPI(s2, "POST", "/api/conversation/"+id2+"/actions/run", ""); response.Code != 400 {
		t.Fatalf("invalid cwd: %d %s", response.Code, response.Body.String())
	}
	workspace, err := database2.GetWorkspace(t.Context())
	if err != nil || len(workspace.Runs) != 0 {
		t.Fatalf("invalid directory spawned a run: %+v %v", workspace, err)
	}
}

func TestActionShutdownKillsProcessGroupWithoutArchiving(t *testing.T) {
	t.Parallel()
	s, database, id, cwd := newActionFixture(t, "sh -c 'printf ready; read line < gate; touch must-not-exist' & wait", true)
	if err := syscall.Mkfifo(filepath.Join(cwd, "gate"), 0o600); err != nil {
		t.Fatal(err)
	}
	ready := outputSignal(t, database, "ready")
	run := workspaceResponse[db.ActionRun](t, workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/run", ""), 202)
	awaitConversationSignal(t, ready)
	close(s.shutdownCh)
	waitForActions(t, s)
	finished, err := database.GetActionRun(t.Context(), run.ID)
	if err != nil || finished.Status != "interrupted" || finished.FinishedAt == nil || finished.Error == "" {
		t.Fatalf("shutdown run: %+v %v", finished, err)
	}
	conversation, err := database.GetConversationByID(t.Context(), id)
	if err != nil || conversation.Archived {
		t.Fatalf("shutdown archived conversation: %+v %v", conversation, err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatalf("command continued past shutdown: %v", err)
	}
	if response := workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/run", ""); response.Code != 503 {
		t.Fatalf("started action during shutdown: %d %s", response.Code, response.Body.String())
	}
}

func TestActionStopKillsProcessGroup(t *testing.T) {
	t.Parallel()
	s, database, id, cwd := newActionFixture(t, "sh -c 'printf ready; read line < gate; touch must-not-exist' & wait", true)
	if err := syscall.Mkfifo(filepath.Join(cwd, "gate"), 0o600); err != nil {
		t.Fatal(err)
	}
	ready := outputSignal(t, database, "ready")
	run := workspaceResponse[db.ActionRun](t, workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/run", ""), 202)
	awaitConversationSignal(t, ready)
	if response := workspaceAPI(s, "POST", "/api/action-runs/"+run.ID+"/cancel", ""); response.Code != 204 {
		t.Fatalf("stop: %d %s", response.Code, response.Body.String())
	}
	waitForActions(t, s)
	finished, err := database.GetActionRun(t.Context(), run.ID)
	if err != nil || finished.Status != "interrupted" || !strings.Contains(finished.Error, "Stopped by the user") {
		t.Fatalf("stopped run: %+v %v", finished, err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatalf("command continued past stop: %v", err)
	}
	if response := workspaceAPI(s, "POST", "/api/action-runs/"+run.ID+"/cancel", ""); response.Code != 409 {
		t.Fatalf("stopping a finished run: %d %s", response.Code, response.Body.String())
	}
}

// A background process holding the output open does not keep the run going.
func TestActionFinishesDespiteBackgroundProcess(t *testing.T) {
	t.Parallel()
	s, database, id, cwd := newActionFixture(t, "echo started; (read line < gate) &", false)
	if err := syscall.Mkfifo(filepath.Join(cwd, "gate"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Releases the background reader.
		if err := os.WriteFile(filepath.Join(cwd, "gate"), []byte("done\n"), 0o600); err != nil {
			t.Error(err)
		}
	})
	run := workspaceResponse[db.ActionRun](t, workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/run", ""), 202)
	ctx, cancel := context.WithTimeout(t.Context(), actionWaitDelay+5*time.Second)
	defer cancel()
	if err := s.waitActions(ctx); err != nil {
		t.Fatal(err)
	}
	finished, err := database.GetActionRun(t.Context(), run.ID)
	if err != nil || finished.Status != "succeeded" || finished.Output != "started\n" {
		t.Fatalf("run: %+v %v", finished, err)
	}
}

func TestActionWorkspaceStartupInterruptsWithoutExecution(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	cwd := t.TempDir()
	if _, err := database.EnsureWorkspace(t.Context(), "predictable", db.ConversationOptions{}); err != nil {
		t.Fatal(err)
	}
	conversation, err := database.CreateConversation(t.Context(), nil, true, &cwd, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	db.AddTestProject(t, database, "my-project")
	if err := database.MoveConversation(t.Context(), conversation.ConversationID, "my-project"); err != nil {
		t.Fatal(err)
	}
	run, _, _, err := database.BeginAction(t.Context(), conversation.ConversationID, "release")
	if err != nil {
		t.Fatal(err)
	}
	startTestServer(t, s)
	t.Cleanup(func() { close(s.shutdownCh) })
	waitFor(t, 5*time.Second, func() bool {
		stored, err := database.GetActionRun(t.Context(), run.ID)
		return err == nil && stored.Status == "interrupted"
	})
	conversation, err = database.GetConversationByID(t.Context(), conversation.ConversationID)
	if err != nil || conversation.Archived {
		t.Fatalf("startup archived conversation: %+v %v", conversation, err)
	}
}

func TestActionAndAssignmentRejectInitializationAndLiveAgent(t *testing.T) {
	t.Parallel()
	s, _, id, _ := newActionFixture(t, "true", true)
	s.workspace.mu.Lock()
	s.workspace.initializing = map[string]bool{id: true}
	s.workspace.mu.Unlock()
	assertBusy := func(id string) {
		t.Helper()
		for _, request := range []struct{ method, path, body string }{
			{"POST", "/api/conversation/" + id + "/actions/run", ""},
			{"PUT", "/api/conversation/" + id + "/project", `{"project":"my-project"}`},
		} {
			response := workspaceAPI(s, request.method, request.path, request.body)
			if response.Code != 409 {
				t.Fatalf("protected mutation %s: %d %s", request.path, response.Code, response.Body.String())
			}
		}
	}
	assertBusy(id)
	s.workspace.mu.Lock()
	delete(s.workspace.initializing, id)
	s.workspace.mu.Unlock()
	manager, err := s.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	manager.syncAgentWorking(true) // Deliberately ahead of its persisted flag.
	assertBusy(id)
	manager.syncAgentWorking(false)
}

// An archive, delete, rename or cwd change checks for running actions
// before doing its work, so actions do not start until it is done.
func TestActionRejectsConversationMutationInProgress(t *testing.T) {
	t.Parallel()
	s, _, id, _ := newActionFixture(t, "true", false)
	done, ok := s.beginConversationMutation(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), id, "archive")
	if !ok {
		t.Fatal("archive refused")
	}
	if response := workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/run", ""); response.Code != 409 {
		t.Fatalf("action during archive: %d %s", response.Code, response.Body.String())
	}
	done()
	workspaceResponse[db.ActionRun](t, workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/run", ""), 202)
	waitForActions(t, s)
}

func TestActionReceivesOwnEnvironment(t *testing.T) {
	t.Parallel()
	command := `printf '%s|%s|%s|%s|%s|%s|%s' "$SHELLEY_CONVERSATION_ID" "$SHELLEY_CWD" "$SHELLEY_MODEL" "$SHELLEY_PORT" "$SHELLEY_URL" "$SHELLEY_SOCKET" "$SHELLEY_USER_EMAIL"`
	s, database, id, cwd := newActionFixture(t, command, false)
	s.listenPort = 8123
	s.socketPath = "/tmp/task-test.sock"
	api := http.NewServeMux()
	s.registerWorkspaceRoutes(api)
	request := httptest.NewRequest("POST", "/api/conversation/"+id+"/actions/run", nil)
	request.Header.Set("X-ExeDev-Email", "task@example.com")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	run := workspaceResponse[db.ActionRun](t, response, 202)
	waitForActions(t, s)
	finished, err := database.GetActionRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.Join([]string{id, cwd, "predictable", "8123", "http://localhost:8123", "/tmp/task-test.sock", "task@example.com"}, "|")
	if finished.Status != "succeeded" || finished.Output != expected {
		t.Fatalf("action environment: status=%s output=%q want=%q", finished.Status, finished.Output, expected)
	}
	s.workspace.mu.Lock()
	s.workspace.restarting = true
	s.workspace.mu.Unlock()
	if response := workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/run", ""); response.Code != 503 {
		t.Fatalf("action started during restart: %d %s", response.Code, response.Body.String())
	}
}

func TestActionEnvironmentStripsInheritedConversationContext(t *testing.T) {
	t.Parallel()
	env := actionEnvironment([]string{
		"PATH=/bin", "HOME=/home/user", "SHELLEY_CONVERSATION_ID=parent",
		"SHELLEY_CONVERSATION_SLUG=parent-slug", "SHELLEY_MODEL=stale-model", "SHELLEY_URL=http://stale",
	}, []string{"SHELLEY_CONVERSATION_ID=task", "SHELLEY_CWD=/repo"})
	if got := strings.Join(env, "\n"); got != "PATH=/bin\nHOME=/home/user\nSHELLEY_CONVERSATION_ID=task\nSHELLEY_CWD=/repo" {
		t.Fatalf("inherited context leaked: %s", got)
	}
}
