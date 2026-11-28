package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/gitstate"
	"shelley.exe.dev/llm/predictable"
)

// TestGitInfoForCommitInOtherRepo checks that a commit made by a bash command
// that cds into another repository is announced for that repository, while
// the conversation's working directory stays put.
func TestGitInfoForCommitInOtherRepo(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, other, readOnly := filepath.Join(root, "home"), filepath.Join(root, "other"), filepath.Join(root, "read-only")
	for _, dir := range []string{home, other, readOnly} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "init", "-q", "-b", "main")
		runGit(t, dir, "config", "user.email", "test@test.com")
		runGit(t, dir, "config", "user.name", "Test")
		runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "initial")
	}

	database, cleanup := setupTestDB(t)
	defer cleanup()
	server := NewServer(database, &testLLMManager{service: predictable.NewService()}, claudetool.ToolSetConfig{WorkingDir: home}, slog.Default(), true, "predictable", "")
	conversation, err := database.CreateConversation(t.Context(), nil, true, &home, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conversation.ConversationID

	postChatMessage(t, server, id, "bash: cd "+other+" && git commit -q --allow-empty -m elsewhere")
	waitForIdle(t, server, id)
	// A read-only git command elsewhere announces nothing.
	postChatMessage(t, server, id, "bash: cd "+readOnly+" && git log -1")
	waitForIdle(t, server, id)

	messages, err := database.ListMessagesByType(t.Context(), id, db.MessageTypeGitInfo)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].UserData == nil {
		t.Fatalf("want one gitinfo message, got %#v", messages)
	}
	var info GitInfoUserData
	if err := json.Unmarshal([]byte(*messages[0].UserData), &info); err != nil {
		t.Fatal(err)
	}
	if info.Worktree != other || info.Branch != "main" || info.Subject != "elsewhere" || info.Commit != strings.TrimSpace(testGitOutput(t, other, "rev-parse", "HEAD")) {
		t.Errorf("gitinfo = %+v, want the commit in %s", info, other)
	}
	wantCheckoutPaths(t, server, id, home, []string{other, home})
	conversation, err = database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Cwd == nil || *conversation.Cwd != home {
		t.Errorf("cwd = %v, want %s", conversation.Cwd, home)
	}
}

func TestRecentCheckoutsIncludeHelperEdits(t *testing.T) {
	server, database, _ := newTestServer(t)
	ctx := t.Context()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, b, workspace := filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "workspace")
	for _, dir := range []string{a, b, workspace} {
		runGit(t, root, "init", "-q", "-b", "main", dir)
		runGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@test.com", "commit", "-qm", "initial", "--allow-empty")
	}
	parent, err := database.CreateConversation(ctx, nil, true, &workspace, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(ctx, "helper", parent.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := server.getOrCreateConversationManager(ctx, parent.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	helper := ConversationManager{db: database, conversationID: child.ConversationID, logger: slog.Default()}
	helper.recordCheckout(ctx, "box", filepath.Join(root, "remote"))
	for _, dir := range []string{a, b} {
		patch := &claudetool.PatchTool{WorkingDir: claudetool.NewMutableWorkingDir(dir), OnCheckoutEdit: helper.noteCheckoutEdit}
		out := patch.Tool().Run(ctx, json.RawMessage(`{"path":"file","patches":[{"operation":"overwrite","newText":"edited"}]}`))
		if out.Error != nil {
			t.Fatal(out.Error)
		}
	}

	// Visiting A leaves B most recent. A later inline notice for A advances it.
	for _, dir := range []string{a, workspace} {
		if err := manager.SetCwd(ctx, dir); err != nil {
			t.Fatal(err)
		}
	}
	tracker := gitstate.NewTracker(func() string { return a })
	runGit(t, a, "-c", "user.name=Test", "-c", "user.email=test@test.com", "commit", "-qm", "external", "--allow-empty")
	states := tracker.Changes()
	if len(states) != 1 {
		t.Fatalf("expected an ordinary Git observation, got %v", states)
	}
	manager.recordGitStateChange(ctx, states[0])

	wantCheckoutPaths(t, server, parent.ConversationID, workspace, []string{a, b, workspace})
}

func wantCheckoutPaths(t *testing.T, server *Server, id, workspace string, paths []string) {
	t.Helper()
	query := url.Values{"conversation_id": {id}, "cwd": {workspace}}
	rec := httptest.NewRecorder()
	server.handleRecentCheckouts(rec, httptest.NewRequest(http.MethodGet, "/?"+query.Encode(), nil))
	var got struct {
		Workspace string   `json:"workspace"`
		Paths     []string `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil {
		t.Fatalf("recent checkouts: %d %s", rec.Code, rec.Body.String())
	}
	if got.Workspace != workspace || !slices.Equal(got.Paths, paths) {
		t.Errorf("recent checkouts = %+v, want %q", got, paths)
	}
}
