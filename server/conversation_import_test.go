package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// newImportSource creates another Shelley's database at path, with a
// conversation, its subagent, and their messages, the first one texted.
func newImportSource(t *testing.T, path string) (source *db.DB, parent, child *generated.Conversation) {
	t.Helper()
	ctx := t.Context()
	source, err := db.New(db.Config{DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { source.Close() })
	if err := source.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cwd, model := "/tmp/play", "predictable"
	parent, err = source.CreateConversation(ctx, nil, true, &cwd, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.UpdateConversationSlug(ctx, parent.ConversationID, "a-long-original-task-slug"); err != nil {
		t.Fatal(err)
	}
	child, err = source.CreateSubagentConversation(ctx, "helper", parent.ConversationID, &cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent.ConversationID, child.ConversationID} {
		for i, text := range []string{"hello", "world"} {
			params := db.CreateMessageParams{
				ConversationID: id, Type: db.MessageTypeUser,
				LLMData:  llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: text}}},
				UserData: map[string]string{"text": text}, UserEmail: "me@example.com",
			}
			if i == 0 {
				params.ExternalMessageID = "texted-" + id
			}
			if _, err := source.CreateMessage(ctx, params); err != nil {
				t.Fatal(err)
			}
		}
	}
	return source, parent, child
}

func importBody(path, project, id string) string {
	return `{"source_db":"` + path + `","project":"` + project + `","conversations":[{"id":"` + id + `","title":"short title"}]}`
}

func TestImportConversations(t *testing.T) {
	t.Parallel()
	s, database, project, ssh, _ := newSharedVMProject(t)
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "source.db")
	source, parent, child := newImportSource(t, path)
	want, err := source.ListMessages(ctx, parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	source.Close()

	body := importBody(path, project, parent.ConversationID)
	workspaceResponse[map[string]any](t, workspaceAPI(s, "POST", "/api/conversations/import", body), 201)
	waitForActions(t, s)

	got, err := database.ListMessages(ctx, parent.ConversationID)
	if err != nil || len(got) != len(want) {
		t.Fatalf("messages: %d want %d, %v", len(got), len(want), err)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.MessageID != w.MessageID || g.SequenceID != w.SequenceID || *g.UserData != *w.UserData || *g.LlmData != *w.LlmData ||
			*g.UserEmail != *w.UserEmail || !g.CreatedAt.Equal(w.CreatedAt.Truncate(1e9)) {
			t.Fatalf("message %d: %+v want %+v", i, g, w)
		}
	}
	// It takes the project's directory; the source host's /tmp/play isn't here.
	p, err := database.GetProject(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := database.GetConversationByID(ctx, parent.ConversationID)
	if err != nil || *conversation.Slug != "short-title" || *conversation.Cwd != p.Cwd || p.Cwd == "/tmp/play" || conversation.Archived {
		t.Fatalf("imported conversation: %+v %v", conversation, err)
	}
	metadata, err := database.GetWorkspaceConversation(ctx, parent.ConversationID)
	if err != nil || metadata.Project != project {
		t.Fatalf("membership: %+v %v", metadata, err)
	}
	subagents, err := database.GetSubagents(ctx, parent.ConversationID)
	if err != nil || len(subagents) != 1 || subagents[0].ConversationID != child.ConversationID {
		t.Fatalf("subagents: %+v %v", subagents, err)
	}
	if vm := conversationVMOf(t, database, child.ConversationID); vm.Name != "hub" || !vm.Shared || vm.Dir != "/home/exedev/hub" {
		t.Fatalf("subagent VM: %+v", vm)
	}
	if masters := ssh.calls(t, "master"); len(masters) != 1 {
		t.Fatalf("imported conversation's VM masters: %v", masters)
	}
	// Importing again conflicts and changes nothing.
	if response := workspaceAPI(s, "POST", "/api/conversations/import", body); response.Code != 409 {
		t.Fatalf("re-import: %d %s", response.Code, response.Body.String())
	}
	// A VM project still setting up its base refuses imports, as it refuses
	// new conversations.
	p.VM = &db.ProjectVM{Integration: p.VM.Integration}
	if err := database.UpdateProject(ctx, project, *p); err != nil {
		t.Fatal(err)
	}
	if response := workspaceAPI(s, "POST", "/api/conversations/import", body); response.Code != 409 ||
		!strings.Contains(response.Body.String(), "no base VM yet") {
		t.Fatalf("import into a project without a base: %d %s", response.Code, response.Body.String())
	}
}

// Into a project with a directory: no VM, the project's directory, and
// messages without the source host's channel ids. The source may be a live
// database, its latest writes still in its WAL.
func TestImportConversationsIntoDirectoryProject(t *testing.T) {
	t.Parallel()
	s, database, _, _ := newActionFixture(t, "true", false)
	project := "Commands"
	path := filepath.Join(t.TempDir(), "source.db")
	_, parent, child := newImportSource(t, path)
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("source has no WAL: %v", err)
	}
	workspaceResponse[map[string]any](t, workspaceAPI(s, "POST", "/api/conversations/import", importBody(path, project, parent.ConversationID)), http.StatusCreated)
	p, err := database.GetProject(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent.ConversationID, child.ConversationID} {
		conversation, err := database.GetConversationByID(t.Context(), id)
		if err != nil || *conversation.Cwd != p.Cwd {
			t.Fatalf("imported %s: %+v %v", id, conversation, err)
		}
		if _, err := database.GetConversationVM(t.Context(), id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("%s bound to a VM: %v", id, err)
		}
		messages, err := database.ListMessages(t.Context(), id)
		if err != nil || len(messages) != 2 {
			t.Fatalf("messages of %s: %d %v", id, len(messages), err)
		}
		for _, m := range messages {
			if m.ExternalMessageID != nil {
				t.Fatalf("message %s kept channel id %q", m.MessageID, *m.ExternalMessageID)
			}
		}
	}
}

// Refused imports change nothing.
func TestImportConversationsRefused(t *testing.T) {
	t.Parallel()
	s, database, _, _ := newActionFixture(t, "true", false)
	project := "Commands"
	dir := t.TempDir()
	path := filepath.Join(dir, "source.db")
	source, parent, child := newImportSource(t, path)
	source.Close()
	refuse := func(name, body, reason string) {
		t.Helper()
		response := workspaceAPI(s, "POST", "/api/conversations/import", body)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), reason) {
			t.Fatalf("%s: %d %s", name, response.Code, response.Body.String())
		}
		if _, err := database.GetConversationByID(context.Background(), parent.ConversationID); err == nil {
			t.Fatalf("%s: imported anyway", name)
		}
	}
	refuse("unknown project", importBody(path, "no-such-project", parent.ConversationID), "no project no-such-project")
	refuse("subagent", importBody(path, project, child.ConversationID), "not a top-level user conversation")

	// A copy of a database in WAL mode, without its -shm, in a directory
	// a read-only connection can't create one in.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	refuse("read-only WAL copy", importBody(path, project, parent.ConversationID), "needs its -wal and -shm files or a writable directory")
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceResponse[map[string]any](t, workspaceAPI(s, "POST", "/api/conversations/import", importBody(path, project, parent.ConversationID)), http.StatusCreated)
}
