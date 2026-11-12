package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"shelley.exe.dev/db"
)

// Commit identity: the name and email a conversation's commits are by, a
// default plus an optional override per project (see db.CommitIdentity).
// Nothing invents one: with none set, git's own configuration applies.
// Shelley applies it wherever the agent commits: its commands carry
// GIT_AUTHOR_* and GIT_COMMITTER_* (claudetool.ToolSetConfig.CommandEnv),
// as do actions, and a task's own VM gets it as git's global identity (see
// setUpConversationVM); a shared VM's or SSH host's configuration is left
// alone.

// handleSetCommitIdentity sets the default commit identity, which GET
// /api/workspace shows.
func (s *Server) handleSetCommitIdentity(w http.ResponseWriter, r *http.Request) {
	var identity db.CommitIdentity
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if !decodeStrict(w, r, &identity) {
		return
	}
	if err := s.db.SetDefaultCommitIdentity(r.Context(), identity); err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	s.notifyWorkspaceChanged()
	identity, err := s.db.DefaultCommitIdentity(r.Context())
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, identity)
}

// commitEmailSuggestion is an email address the user may commit as, and
// where it comes from: "github" (a GitHub account's public email),
// "github_noreply" (its no-reply address) or "exe_dev" (the exe.dev
// account's).
type commitEmailSuggestion struct {
	Email  string `json:"email"`
	Source string `json:"source"`
}

type commitIdentitySuggestions struct {
	Names  []string                `json:"names"`
	Emails []commitEmailSuggestion `json:"emails"`
	// Errors say which sources couldn't be read; the others still count.
	Errors []string `json:"errors"`
}

var githubLogin = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// handleCommitIdentitySuggestions serves GET
// /api/commit-identity/suggestions?login=LOGIN...: names and emails to
// offer as a commit identity, from the public profiles of the GitHub
// accounts LOGIN (which the UI knows from the exe.dev account's GitHub
// connections) and the exe.dev account. exe.dev's GitHub integrations
// proxy only repository APIs, not a user's own (/user, /user/emails), so
// a GitHub account's name and email are what its public profile shows.
func (s *Server) handleCommitIdentitySuggestions(w http.ResponseWriter, r *http.Request) {
	out := commitIdentitySuggestions{Names: []string{}, Emails: []commitEmailSuggestion{}, Errors: []string{}}
	addEmail := func(email, source string) {
		if email != "" && !slices.ContainsFunc(out.Emails, func(e commitEmailSuggestion) bool { return strings.EqualFold(e.Email, email) }) {
			out.Emails = append(out.Emails, commitEmailSuggestion{email, source})
		}
	}
	var noreply []string
	for _, login := range r.URL.Query()["login"] {
		if !githubLogin.MatchString(login) {
			http.Error(w, fmt.Sprintf("%q is not a GitHub login", login), http.StatusBadRequest)
			return
		}
		user, err := s.githubUser(r.Context(), login)
		if err != nil {
			out.Errors = append(out.Errors, err.Error())
			continue
		}
		if user.Name != "" && !slices.Contains(out.Names, user.Name) {
			out.Names = append(out.Names, user.Name)
		}
		addEmail(user.Email, "github")
		noreply = append(noreply, fmt.Sprintf("%d+%s@users.noreply.github.com", user.ID, user.Login))
	}
	addEmail(strings.TrimSpace(r.Header.Get("X-ExeDev-Email")), "exe_dev")
	for _, email := range noreply {
		addEmail(email, "github_noreply")
	}
	writeJSON(w, http.StatusOK, out)
}

type githubPublicUser struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// githubUser reads login's public GitHub profile.
func (s *Server) githubUser(ctx context.Context, login string) (*githubPublicUser, error) {
	api := s.githubAPI
	if api == "" {
		api = "https://api.github.com"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", api+"/users/"+login, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub user %s: %w", login, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub user %s: %s", login, resp.Status)
	}
	var user githubPublicUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("GitHub user %s: %w", login, err)
	}
	if user.ID == 0 || !githubLogin.MatchString(user.Login) {
		return nil, fmt.Errorf("GitHub user %s: no id or login in its profile", login)
	}
	return &user, nil
}

// commitIdentityPrompt tells a project conversation who its commits are by.
func (s *Server) commitIdentityPrompt(ctx context.Context, id string, project *db.Project) (string, error) {
	identity, err := s.db.ConversationCommitIdentity(ctx, id)
	if err != nil {
		return "", err
	}
	if identity.IsZero() {
		return "No commit identity is set, so git's own configuration decides who commits are by. Never invent a name or email for commits: if one matters, ask the user to set the commit identity in the project's settings.\n", nil
	}
	where := "your commands, here and on any VM, get GIT_AUTHOR_NAME, GIT_AUTHOR_EMAIL, GIT_COMMITTER_NAME and GIT_COMMITTER_EMAIL"
	if vm := project.VM; vm != nil && vm.SharedVM == "" {
		where += ", and each task's own VM has it as git's global user.name and user.email"
	}
	return fmt.Sprintf("Commit identity: commits are by %s; %s. Don't override it or change git's configuration for it; the user changes it in the project's settings.\n", identity, where), nil
}
