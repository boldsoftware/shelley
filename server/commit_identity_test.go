package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

// The default identity, a project's own, and where tasks get them: their
// commands' environment (bash and actions) and their prompt.
func TestCommitIdentity(t *testing.T) {
	t.Parallel()
	s, database, id, _ := newActionFixture(t, `printf '%s <%s>' "$GIT_AUTHOR_NAME" "$GIT_COMMITTER_EMAIL"`, false)
	prompt := func() string {
		t.Helper()
		prompt, err := conversationPrompt(t, s, id)
		if err != nil {
			t.Fatal(err)
		}
		return prompt
	}
	if got := workspaceResponse[db.Workspace](t, workspaceAPI(s, "GET", "/api/workspace", ""), 200).CommitIdentity; !got.IsZero() {
		t.Fatalf("no identity set: %+v", got)
	}
	if p := prompt(); !strings.Contains(p, "No commit identity is set") || !strings.Contains(p, "Never invent") {
		t.Fatalf("prompt without an identity: %s", p)
	}
	if response := workspaceAPI(s, "PUT", "/api/commit-identity", `{"name":"Ada","email":"ada"}`); response.Code != 400 {
		t.Fatalf("bad email: %d %s", response.Code, response.Body)
	}
	ada := db.CommitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"}
	if got := workspaceResponse[db.CommitIdentity](t, workspaceAPI(s, "PUT", "/api/commit-identity", `{"name":" Ada Lovelace","email":"ada@example.com"}`), 200); got != ada {
		t.Fatalf("set default: %+v", got)
	}
	if got := workspaceResponse[db.Workspace](t, workspaceAPI(s, "GET", "/api/workspace", ""), 200).CommitIdentity; got != ada {
		t.Fatalf("workspace's default identity: %+v", got)
	}
	if p := prompt(); !strings.Contains(p, "commits are by Ada Lovelace <ada@example.com>") {
		t.Fatalf("prompt with the default: %s", p)
	}
	manager, err := s.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	env, err := manager.toolSetConfig.CommitEnv(t.Context())
	if err != nil || !slices.Contains(env, "GIT_AUTHOR_NAME=Ada Lovelace") || !slices.Contains(env, "GIT_COMMITTER_EMAIL=ada@example.com") {
		t.Fatalf("bash environment %q %v", env, err)
	}

	// The project's own identity wins, for actions too.
	workspaceResponse[db.Project](t, workspaceAPI(s, "PUT", "/api/projects/Commands",
		`{"name":"Commands","instructions":"","cwd":"","actions":[{"id":"run","label":"Run","command":"printf '%s <%s>' \"$GIT_AUTHOR_NAME\" \"$GIT_COMMITTER_EMAIL\""}],"vm":null,"repos":[],"commit_identity":{"name":"Grace Hopper","email":"grace@example.com"}}`), 200)
	run := workspaceResponse[db.ActionRun](t, workspaceAPI(s, "POST", "/api/conversation/"+id+"/actions/run", ""), 202)
	waitForActions(t, s)
	finished, err := database.GetActionRun(t.Context(), run.ID)
	if err != nil || finished.Output != "Grace Hopper <grace@example.com>" {
		t.Fatalf("action output %q %v", finished.Output, err)
	}
	if p := prompt(); !strings.Contains(p, "commits are by Grace Hopper <grace@example.com>") {
		t.Fatalf("prompt with the project's: %s", p)
	}
}

// Suggestions come from GitHub accounts' public profiles and the exe.dev
// account, with whatever can't be read reported, not fatal.
func TestCommitIdentitySuggestions(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/ada":
			w.Write([]byte(`{"login":"ada","id":42,"name":"Ada Lovelace","email":"ada@example.com"}`))
		case "/users/ada-org":
			w.Write([]byte(`{"login":"ada-org","id":7,"name":"","email":null}`))
		default:
			http.Error(w, "rate limited", http.StatusForbidden)
		}
	}))
	defer github.Close()
	s.githubAPI = github.URL
	api := http.NewServeMux()
	s.registerWorkspaceRoutes(api)
	request := httptest.NewRequest("GET", "/api/commit-identity/suggestions?login=ada&login=ada-org&login=gone", nil)
	request.Header.Set("X-ExeDev-Email", "ADA@example.com")
	recorder := httptest.NewRecorder()
	api.ServeHTTP(recorder, request)
	got := workspaceResponse[commitIdentitySuggestions](t, recorder, 200)
	want := []commitEmailSuggestion{
		{"ada@example.com", "github"},
		{"42+ada@users.noreply.github.com", "github_noreply"},
		{"7+ada-org@users.noreply.github.com", "github_noreply"},
	}
	if !slices.Equal(got.Names, []string{"Ada Lovelace"}) || !slices.Equal(got.Emails, want) ||
		len(got.Errors) != 1 || !strings.Contains(got.Errors[0], "GitHub user gone: 403") {
		t.Fatalf("suggestions %+v", got)
	}
	if response := workspaceAPI(s, "GET", "/api/commit-identity/suggestions?login=a/b", ""); response.Code != 400 {
		t.Fatalf("bad login: %d", response.Code)
	}
}
