package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/ui"
)

// setWorkspaceFlag sets or, with value "", clears the workspace flag through
// the feature-flag API.
func setWorkspaceFlag(t *testing.T, s *Server, value string) {
	t.Helper()
	w := httptest.NewRecorder()
	if value == "" {
		s.handleDeleteFeatureFlag(w, httptest.NewRequest("DELETE", "/feature-flags", strings.NewReader(`{"name":"workspace"}`)))
	} else {
		s.handleSetFeatureFlag(w, httptest.NewRequest("POST", "/feature-flags", strings.NewReader(`{"name":"workspace","value":`+value+`}`)))
	}
	if w.Code != http.StatusOK {
		t.Fatalf("workspace flag %q: %d %s", value, w.Code, w.Body.String())
	}
}

// newWorkspaceServer is a server in workspace mode, and its Assistant.
func newWorkspaceServer(t *testing.T) (*Server, *db.DB, string) {
	t.Helper()
	s, database, _ := newTestServer(t)
	setWorkspaceFlag(t, s, "true")
	return s, database, s.assistantID()
}

// The flag, not requests, makes the Assistant; pages reload for either value.
func TestWorkspaceFlagLoadsTheWorkspace(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	// Reading the workspace creates nothing, even with the flag set behind
	// the server's back.
	db.EnableWorkspaceMode(t, database)
	workspace := workspaceResponse[db.Workspace](t, workspaceAPI(s, "GET", "/api/workspace", ""), 200)
	if workspace.AssistantID != "" || len(workspace.Conversations) != 0 {
		t.Fatalf("GET /api/workspace created the Assistant: %+v", workspace)
	}
	if init := indexInitData(t, s); init["workspace"] != false || init["ui_build"] != ui.BuildID() {
		t.Fatalf("stock init data: workspace=%v ui_build=%v", init["workspace"], init["ui_build"])
	}

	builds := func(change func()) []string {
		ctx, cancel := context.WithCancel(t.Context())
		next := s.streamPub.Subscribe(ctx, -1)
		change()
		cancel()
		var builds []string
		for event, ok := next(); ok; event, ok = next() {
			if event.UIBuild != "" {
				builds = append(builds, event.UIBuild)
			}
		}
		return builds
	}
	workspaceBuild := ui.BuildID() + "+workspace"
	if got := builds(func() { setWorkspaceFlag(t, s, "true") }); len(got) != 1 || got[0] != workspaceBuild {
		t.Fatalf("turning workspace mode on announced builds %q", got)
	}
	workspace = workspaceResponse[db.Workspace](t, workspaceAPI(s, "GET", "/api/workspace", ""), 200)
	if workspace.AssistantID == "" || workspace.AssistantID != s.assistantID() || len(workspace.Conversations) != 1 ||
		!workspace.Conversations[0].IsAssistant || len(workspace.Projects) != 1 || workspace.Projects[0].Name != "Shelley" {
		t.Fatalf("workspace mode: %+v, cached Assistant %q", workspace, s.assistantID())
	}
	if init := indexInitData(t, s); init["workspace"] != true || init["ui_build"] != workspaceBuild {
		t.Fatalf("workspace init data: workspace=%v ui_build=%v", init["workspace"], init["ui_build"])
	}
	assistant, err := database.GetConversationByID(t.Context(), workspace.AssistantID)
	if err != nil {
		t.Fatal(err)
	}
	options := db.ParseConversationOptions(assistant.ConversationOptions)
	if options.Profile != "Assistant" || options.SettingsEdited || options.ToolOverrides["message_user"] != "on" ||
		!strings.Contains(options.SystemPrompt, "text-message exchange") {
		t.Fatalf("assistant did not start from the Assistant profile: %+v", options)
	}
	if setWorkspaceFlag(t, s, "true"); s.assistantID() != workspace.AssistantID {
		t.Fatal("assistant changed")
	}
	// The seeded Shelley project is deletable once empty.
	if response := workspaceAPI(s, "DELETE", "/api/projects/Shelley", ""); response.Code != 200 {
		t.Fatalf("delete the Shelley project: %d %s", response.Code, response.Body.String())
	}

	if got := builds(func() { setWorkspaceFlag(t, s, "") }); len(got) != 1 || got[0] != ui.BuildID() {
		t.Fatalf("turning workspace mode off announced builds %q", got)
	}
	if s.assistantID() != "" || s.isAssistant(workspace.AssistantID) {
		t.Fatal("an Assistant outside workspace mode")
	}
}

func TestAssistantMutationGuards(t *testing.T) {
	t.Parallel()
	s, _, assistantID := newWorkspaceServer(t)
	for _, operation := range []string{"archive", "delete", "rename"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"slug":"renamed"}`))
		switch operation {
		case "archive":
			s.handleArchiveConversation(w, req, assistantID)
		case "delete":
			s.handleDeleteConversation(w, req, assistantID)
		case "rename":
			s.handleRenameConversation(w, req, assistantID)
		}
		if w.Code != http.StatusConflict {
			t.Errorf("Assistant %s: %d %s", operation, w.Code, w.Body.String())
		}
	}
	db.AddTestProject(t, s.db, "Docs")
	if response := workspaceAPI(s, "PUT", "/api/conversation/"+assistantID+"/project", `{"project":"Docs"}`); response.Code != http.StatusConflict {
		t.Errorf("Assistant move: %d %s", response.Code, response.Body.String())
	}
	// The Assistant has a directory, but it is not a project's task.
	if err := s.db.UpdateConversationCwd(t.Context(), assistantID, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if response := workspaceAPI(s, "POST", "/api/conversation/"+assistantID+"/actions/release", ""); response.Code != http.StatusConflict {
		t.Errorf("Assistant action: %d %s", response.Code, response.Body.String())
	}
}

// The Assistant routes with the project catalog; a task gets its own
// project only, so editing another project changes no task's prompt.
func TestWorkspaceInstructions(t *testing.T) {
	t.Parallel()
	s, database, assistantID := newWorkspaceServer(t)
	prompt, err := s.workspaceInstructions(t.Context(), assistantID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"long-lived Assistant", "CREATE A TASK", "what's the weather today?", "create a new project", "catch-all", "POST $SHELLEY_API/api/conversations/new"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("Assistant instructions missing %q", want)
		}
	}
	if !strings.Contains(prompt, "web address is unknown") {
		t.Error("Assistant instructions invent a conversation URL before any browser visit")
	}
	visit := func(origin string) {
		r := httptest.NewRequest("POST", "/api/none", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		s.rememberWebOrigin(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), r)
	}
	visit("https://vm.shelley.example")
	if prompt, err = s.workspaceInstructions(t.Context(), assistantID); err != nil || !strings.Contains(prompt, "Task links: https://vm.shelley.example/c/CONVERSATION_ID") {
		t.Fatalf("Assistant conversation links missing the browser origin: %v %q", err, prompt)
	}

	for _, name := range []string{"Documentation", "Other"} {
		if _, err := database.CreateProject(t.Context(), db.Project{Name: name, Instructions: name + " guides."}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if prompt, err := s.workspaceInstructions(t.Context(), assistantID); err != nil || !strings.Contains(prompt, `"name":"Documentation"`) {
		t.Fatalf("Assistant catalog lacks a new project: %v %q", err, prompt)
	}
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{Profile: "Documentation"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.workspaceInstructions(t.Context(), conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(task, "<workspace>") || !strings.Contains(task, "Do not route the same request") ||
		!strings.Contains(task, "Documentation guides.") || strings.Contains(task, "Other") {
		t.Fatalf("task instructions: %q", task)
	}

	// Outside workspace mode no origin is kept, and a task gets only its
	// project's instructions.
	setWorkspaceFlag(t, s, "")
	visit("https://elsewhere.example")
	if origin := s.webOrigin.Load(); origin == nil || *origin != "https://vm.shelley.example" {
		t.Fatalf("origin remembered outside workspace mode: %v", origin)
	}
	if task, err := s.workspaceInstructions(t.Context(), conv.ConversationID); err != nil || strings.Contains(task, "<workspace>") ||
		!strings.Contains(task, "<project_instructions>\nDocumentation guides.") {
		t.Fatalf("stock project instructions: %v %q", err, task)
	}
}
