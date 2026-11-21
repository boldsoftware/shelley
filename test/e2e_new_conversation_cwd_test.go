package test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// Ported from ui/e2e/new-conversation-cwd.spec.ts.

// newCwdRepo makes a directory {{DIR}} holding a git repository {{REPO}}
// with one empty commit and a worktree of it on branch feature, {{WORKTREE}},
// which has a subdirectory {{SUBDIR}}.
func (s *shelley) newCwdRepo() (dir, repo, worktree, subdir string) {
	dir = s.mkdir(filepath.Join(s.dir, "newcwd"))
	repo = s.mkdir(filepath.Join(dir, "repo"))
	s.gitIn(repo, "init", "--quiet")
	s.gitIn(repo, "config", "user.name", "Shelley Test")
	s.gitIn(repo, "config", "user.email", "test@example.com")
	s.gitIn(repo, "config", "commit.gpgsign", "false")
	s.gitIn(repo, "config", "core.hooksPath", "/dev/null")
	s.gitIn(repo, "commit", "--quiet", "--allow-empty", "-m", "Initial commit")
	worktree = filepath.Join(dir, "worktree")
	s.gitIn(repo, "worktree", "add", "--quiet", "-b", "feature", worktree)
	subdir = s.mkdir(filepath.Join(worktree, "subdir"))
	s.v("DIR", dir)
	s.v("REPO", repo)
	s.v("WORKTREE", worktree)
	s.v("SUBDIR", subdir)
	return dir, repo, worktree, subdir
}

// newCwdSaved watches the page send its first message (POST
// /api/conversations/new or /api/conversation/<id>/chat): call "saved"
// returns "none" until it has, then "ok cwd=<the conversation's cwd>
// draft=<whether it is still a draft>" (or "status <code>" for a failed
// send).
func (s *shelley) newCwdSaved() {
	var mu sync.Mutex
	sent := "" // the first send's conversation ID, or "status <code>"
	relay := func(w http.ResponseWriter, r *http.Request) {
		resp, err := s.forward(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		result := r.PathValue("id")
		if resp.StatusCode/100 != 2 {
			result = fmt.Sprintf("status %d", resp.StatusCode)
		} else if result == "" {
			var created struct {
				ID string `json:"conversation_id"`
			}
			json.Unmarshal(body, &created)
			result = created.ID
		}
		mu.Lock()
		if sent == "" {
			sent = result
		}
		mu.Unlock()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}
	s.route("POST /api/conversations/new", relay)
	s.route("POST /api/conversation/{id}/chat", relay)
	s.call("saved", func() string {
		mu.Lock()
		id := sent
		mu.Unlock()
		if id == "" {
			return "none"
		}
		if len(id) > 7 && id[:7] == "status " {
			return id
		}
		var got struct {
			Conversation struct {
				Cwd     string `json:"cwd"`
				IsDraft bool   `json:"is_draft"`
			} `json:"conversation"`
		}
		s.api("GET", "/api/conversation/"+id, nil, &got)
		return fmt.Sprintf("ok cwd=%s draft=%t", got.Conversation.Cwd, got.Conversation.IsDraft)
	})
}

// newCwdConversation starts a conversation in cwd, archived if asked, as
// {{ID}}.
func (s *shelley) newCwdConversation(cwd string, archived bool) {
	c := s.conversationWith(map[string]any{"message": "Hello", "model": "predictable", "cwd": cwd}, "")
	if archived {
		s.do("POST", "/api/conversation/"+c.ID+"/archive", nil)
	}
	s.v("ID", c.ID)
}

// Description snippets for the spec's helpers.
const (
	newCwdStart = `To "start a new conversation": wait for ".header" to be visible; if the button in ".header" whose accessible name is exactly "New Conversation" is visible, click it; otherwise click the button in ".header" named exactly "More options", then the button named exactly "New Conversation" in ".chat-overflow-popover". `

	newCwdControl = `"The directory control" on this phone-sized screen is the button in ".chat-overflow-popover" whose accessible name starts with "Directory"; to reach it, click the button named exactly "More options" first unless ".chat-overflow-popover" is already visible. `

	newCwdSend = `Call "saved" reports the first message the page sends: "none" before it, then "ok cwd=<the conversation's saved working directory> draft=<whether it is still a draft>". To "send P and expect cwd C": fill [data-testid="message-input"] with P; call "saved", which must return exactly "none"; click [data-testid="send-button"]; then call "saved" until it returns exactly "ok cwd=C draft=false". To "check and send in C": the directory control's text should contain C; press Escape; then send "echo: new conversation directory" and expect cwd C. Perform the steps in order. `
)

// newCwdFrom is the spec's test of starting a new conversation from a
// conversation in a worktree, a worktree's subdirectory, an archived
// worktree conversation or an ordinary directory.
func newCwdFrom(t *testing.T, location, cwdVar, expectedVar string) {
	e2e(t, `A new conversation from a conversation in `+location+` chooses the right directory. {{DIR}} is a directory holding a git repository {{REPO}} and a worktree of it, {{WORKTREE}}, which has a subdirectory {{SUBDIR}}. Conversation {{ID}} runs in {{`+cwdVar+`}}`+map[bool]string{true: " and is archived", false: ""}[location == "an archived worktree"]+`. `+newCwdStart+newCwdControl+newCwdSend+`1. Navigate to /c/{{ID}}. 2. Start a new conversation. 3. The URL should become exactly /new (an exact path check). 4. Check and send in {{`+expectedVar+`}}.`, func(s *shelley) {
		s.newCwdRepo()
		s.newCwdConversation(s.vars[cwdVar], location == "an archived worktree")
		s.newCwdSaved()
	})
}

func TestE2ENewConversationCwdFromWorktree(t *testing.T) {
	newCwdFrom(t, "a worktree", "WORKTREE", "REPO")
}

func TestE2ENewConversationCwdFromWorktreeSubdirectory(t *testing.T) {
	newCwdFrom(t, "a worktree subdirectory", "SUBDIR", "REPO")
}

func TestE2ENewConversationCwdFromArchivedWorktree(t *testing.T) {
	newCwdFrom(t, "an archived worktree", "WORKTREE", "REPO")
}

func TestE2ENewConversationCwdFromOrdinaryDirectory(t *testing.T) {
	newCwdFrom(t, "an ordinary directory", "DIR", "DIR")
}

func TestE2ENewConversationCwdSelectedWorktreeStays(t *testing.T) {
	e2e(t, `An explicitly selected worktree stays selected. {{REPO}} is a git repository with a worktree {{WORKTREE}}; an init script sets localStorage "shelley_selected_cwd" to {{REPO}} before the page's own scripts on every load (already in place: steps must not set it). "The dialog" is the dialog named "Select Directory". `+newCwdControl+newCwdSend+`1. Navigate to /new. 2. Click the directory control. 3. Fill the dialog's ".directory-picker-input" with "{{WORKTREE}}/" (the worktree path followed by a slash). 4. The dialog's ".directory-picker-current-path" should have exactly the text {{WORKTREE}}. 5. Click the dialog's button named exactly "Select". 6. Check and send in {{WORKTREE}}.`, func(s *shelley) {
		_, repo, _, _ := s.newCwdRepo()
		s.initScript(fmt.Sprintf(`localStorage.setItem("shelley_selected_cwd", %q);`, repo))
		s.newCwdSaved()
	})
}

func TestE2ENewConversationCwdSlashNewUsesMainRepo(t *testing.T) {
	e2e(t, `/new with a prompt uses the main repository. {{REPO}} is a git repository with a worktree {{WORKTREE}}; conversation {{ID}} runs in the worktree. `+newCwdSend+`1. Navigate to /c/{{ID}}. 2. Send "/new echo: new conversation directory" and expect cwd {{REPO}}.`, func(s *shelley) {
		_, _, worktree, _ := s.newCwdRepo()
		s.newCwdConversation(worktree, false)
		s.newCwdSaved()
	})
}

func TestE2ENewConversationCwdLookupFailureRetry(t *testing.T) {
	e2e(t, `Directory lookup failures are visible and can be retried. Conversation {{ID}} runs in {{DIR}}, an empty directory that is not a git repository, and is archived; another conversation is in the list. Call "break lookups" makes the server's directory lookups (GET /api/list-directory) fail with HTTP 503 "Directory lookup unavailable"; call "fix lookups" lets them through again. `+newCwdStart+newCwdControl+newCwdSend+`1. Navigate to /c/{{ID}}. 2. Call "break lookups". 3. Start a new conversation. 4. The element with role "alert" should contain the text "Directory lookup unavailable". 5. The page must not have gone to /new: eval /\/new$/.test(location.href) must give false. 6. Call "fix lookups". 7. Click the button named "Dismiss" inside the alert. 8. Start a new conversation. 9. Check and send in {{DIR}}.`, func(s *shelley) {
		dir := s.mkdir(filepath.Join(s.dir, "newcwd"))
		s.v("DIR", dir)
		s.newCwdConversation(dir, true)
		// The spec ran against a server other tests had filled; with no
		// conversation in the list, the page shows the lookup's error in
		// place of the whole app, not in a dismissable alert.
		s.conversation("echo: another conversation", "")
		s.newCwdSaved()
		var broken atomic.Bool
		s.route("GET /api/list-directory", func(w http.ResponseWriter, r *http.Request) {
			if broken.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, "Directory lookup unavailable")
				return
			}
			s.pass.ServeHTTP(w, r)
		})
		s.call("break lookups", func() string {
			broken.Store(true)
			return "broken"
		})
		s.call("fix lookups", func() string {
			broken.Store(false)
			return "fixed"
		})
	})
}
