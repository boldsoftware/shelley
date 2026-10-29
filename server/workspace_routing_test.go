package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func TestConversationInstructionsFollowProjectEdits(t *testing.T) {
	t.Parallel()
	s, database, service := newTestServer(t)
	project, err := database.CreateProject(t.Context(), db.Project{
		Name: "Documentation", Instructions: "Writing guides. Use short examples.",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MoveConversation(t.Context(), conv.ConversationID, project.Name); err != nil {
		t.Fatal(err)
	}
	wrapper := &workspaceService{Service: service, server: s, conversationID: conv.ConversationID}
	request := &llm.Request{System: []llm.SystemContent{{Type: "text", Text: "Conversation-specific custom prompt."}}, Messages: []llm.Message{llm.UserStringMessage("echo: ready")}}
	if _, err := wrapper.Do(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	requests := service.GetRecentRequests()
	got := requests[len(requests)-1]
	if len(got.System) != 2 || !strings.Contains(got.System[1].Text, "Use short examples.") {
		t.Fatalf("project instructions not applied: %+v", got.System)
	}
	if len(request.System) != 1 || request.System[0].Text != "Conversation-specific custom prompt." {
		t.Fatal("decorator mutated the original request")
	}
	project.Instructions = "Use detailed runnable examples."
	if err := database.UpdateProject(t.Context(), project.Name, *project); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapper.Do(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	requests = service.GetRecentRequests()
	got = requests[len(requests)-1]
	if !strings.Contains(got.System[1].Text, project.Instructions) || strings.Contains(got.System[1].Text, "Use short examples.") {
		t.Fatal("a long-lived conversation did not take updated project instructions")
	}
	if _, err := wrapper.WarmCache(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if wrapper.CacheTTL() != service.CacheTTL() || len(service.WarmRequests()[0].System) != 2 {
		t.Fatal("workspace decorator lost cache warming or its instructions")
	}
}

func TestConversationCreateStartsStandardConversation(t *testing.T) {
	t.Parallel()
	s, database, service := newTestServer(t)
	project, err := database.CreateProject(t.Context(), db.Project{
		Name: "Tests", Cwd: t.TempDir(), Instructions: "Include regression tests.",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	next := s.streamPub.Subscribe(ctx, 0)
	payload, _ := json.Marshal(map[string]string{"project": project.Name, "title": "Add tests", "message": "echo: conversation started"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/conversations/new", strings.NewReader(string(payload)))
	req.Header.Set("X-ExeDev-Email", "author@example.com")
	s.handleNewConversation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	defer func() {
		s.mu.Lock()
		manager := s.activeConversations[result.ConversationID]
		s.mu.Unlock()
		if manager != nil {
			manager.stopLoop()
		}
	}()
	for {
		event, ok := next()
		if !ok {
			t.Fatal("conversation did not finish before deadline")
		}
		if state := event.ConversationState; state != nil && state.ConversationID == result.ConversationID && !state.Working {
			break
		}
	}
	conv, err := database.GetConversationByID(t.Context(), result.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if derefString(conv.Cwd) != project.Cwd || derefString(conv.Slug) != "add-tests" {
		t.Fatalf("conversation directory/title not applied: %+v", conv)
	}
	metadata, err := database.GetWorkspaceConversation(t.Context(), conv.ConversationID)
	if err != nil || metadata.Project != project.Name {
		t.Fatal("workspace metadata incorrect", metadata, err)
	}
	found := false
	for _, request := range service.GetRecentRequests() {
		for _, system := range request.System {
			found = found || strings.Contains(system.Text, "Include regression tests.")
		}
	}
	if !found {
		t.Fatal("first conversation model call lacked project instructions")
	}
}

// Until a project conversation's first message is accepted, requests on it
// are refused; a conversation in no project is never fenced.
func TestConversationCreationFencesProjectConversations(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	cwd := t.TempDir()
	project, err := database.CreateProject(t.Context(), db.Project{
		Name: "Creation", Cwd: cwd,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	rename := func(id string) int {
		w := httptest.NewRecorder()
		s.handleRenameConversation(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"slug":"renamed-`+id+`"}`)), id)
		return w.Code
	}
	conv, release, err := s.createProjectConversation(t.Context(), &cwd, nil, db.ConversationOptions{Profile: project.Name}, project.Name)
	if err != nil {
		t.Fatal(err)
	}
	if code := rename(conv.ConversationID); code != http.StatusConflict {
		t.Fatalf("renamed a project conversation during creation: %d", code)
	}
	release()
	if code := rename(conv.ConversationID); code != http.StatusOK {
		t.Fatalf("rename after creation: %d", code)
	}
	plain, release, err := s.createProjectConversation(t.Context(), &cwd, nil, db.ConversationOptions{}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if code := rename(plain.ConversationID); code != http.StatusOK {
		t.Fatalf("fenced a conversation in no project: %d", code)
	}
}

// The workspace changes, and is broadcast as changed, only when a project
// conversation is created, archived, unarchived or deleted.
func TestOnlyProjectConversationsNotifyWorkspaceChanged(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	cwd := t.TempDir()
	project, err := database.CreateProject(t.Context(), db.Project{Name: "Notified", Cwd: cwd}, "")
	if err != nil {
		t.Fatal(err)
	}
	// notifications counts the workspace_changed broadcasts during change.
	notifications := func(change func()) int {
		ctx, cancel := context.WithCancel(t.Context())
		next := s.streamPub.Subscribe(ctx, -1)
		change()
		cancel()
		count := 0
		for event, ok := next(); ok; event, ok = next() {
			if event.WorkspaceChanged {
				count++
			}
		}
		return count
	}
	for _, name := range []string{project.Name, ""} {
		want := 0
		if name != "" {
			want = 1
		}
		var id string
		if got := notifications(func() {
			conv, release, err := s.createProjectConversation(t.Context(), &cwd, nil, db.ConversationOptions{Profile: name}, name)
			if err != nil {
				t.Fatal(err)
			}
			release()
			id = conv.ConversationID
		}); got != want {
			t.Fatalf("project %q: create notified %d times, want %d", name, got, want)
		}
		for _, op := range []struct {
			name   string
			handle func(http.ResponseWriter, *http.Request, string)
		}{
			{"archive", s.handleArchiveConversation},
			{"unarchive", s.handleUnarchiveConversation},
			{"delete", s.handleDeleteConversation},
		} {
			if got := notifications(func() {
				w := httptest.NewRecorder()
				op.handle(w, httptest.NewRequest("POST", "/", nil), id)
				if w.Code != http.StatusOK {
					t.Fatalf("%s: %d %s", op.name, w.Code, w.Body.String())
				}
			}); got != want {
				t.Fatalf("project %q: %s notified %d times, want %d", name, op.name, got, want)
			}
		}
	}
}
