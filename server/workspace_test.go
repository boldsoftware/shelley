package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// newProjectConversationFixture makes a conversation, and a project to move it to.
func newProjectConversationFixture(t *testing.T) (*Server, *db.DB, string) {
	t.Helper()
	s, database, _ := newTestServer(t)
	cwd := t.TempDir()
	project, err := database.CreateProject(t.Context(), db.Project{Name: "Commands", Cwd: cwd}, "")
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
	return s, database, conversation.ConversationID
}

func TestWorkspaceHTTPAndProjectCRUD(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	workspace := workspaceResponse[db.Workspace](t, workspaceAPI(s, "GET", "/api/workspace", ""), 200)
	if len(workspace.Projects) != 0 || len(workspace.Conversations) != 0 {
		t.Fatalf("unexpected initial workspace: %+v", workspace)
	}
	project := workspaceResponse[db.Project](t, workspaceAPI(s, "POST", "/api/projects",
		`{"name":"Docs","instructions":"Use examples.","cwd":""}`), 201)
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
	if response := workspaceAPI(s, "DELETE", "/api/projects/Documentation", ""); response.Code != 204 {
		t.Fatalf("delete empty project: %d %s", response.Code, response.Body.String())
	}
	if response := workspaceAPI(s, "POST", "/api/projects", `{"name":"Assistant"}`); response.Code != 409 {
		t.Fatalf("project named like a profile: %d %s", response.Code, response.Body.String())
	}
	for _, request := range []struct{ method, path, body string }{
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

func TestConversationMoveRejectsInitializationAndLiveAgent(t *testing.T) {
	t.Parallel()
	s, _, id := newProjectConversationFixture(t)
	s.workspace.mu.Lock()
	s.workspace.initializing = map[string]bool{id: true}
	s.workspace.mu.Unlock()
	assertBusy := func(id string) {
		t.Helper()
		response := workspaceAPI(s, "PUT", "/api/conversation/"+id+"/project", `{"project":"my-project"}`)
		if response.Code != 409 {
			t.Fatalf("protected move: %d %s", response.Code, response.Body.String())
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
