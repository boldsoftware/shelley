package test

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	lazycue "github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/file-finder.spec.ts.

// finderOpen is how a step opens the fuzzy file finder.
const finderOpen = `Open the fuzzy file finder by pressing "p" (lowercase) with the Control modifier, without clicking anything first, and wait up to 10s for its input ".grp-filter" to be visible.`

// The fuzzy file finder (Cmd/Ctrl+P) ANDs whitespace-separated terms, so
// a half-remembered filename typed as words finds the file: "vm storage s3"
// must reach vm-storage-s3-backup-design.md even though the literal query
// (spaces and all) never appears in the path.
func TestE2EFileFinderTermsAreANDed(t *testing.T) {
	e2e(t, useful(0), `In the file finder, space-separated terms are ANDed. The conversation /c/{{SLUG}} runs in a directory whose docs folder holds vm-storage-s3-backup-design.md, vm-storage-replication.md and s3-uploads.md. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. `+finderOpen+` 3. Fill the finder input with "vm storage s3". 4. There should come to be exactly 1 ".grp-row" (up to 10s). 5. It should contain "docs/vm-storage-s3-backup-design.md". 6. Its "mark" elements should be exactly three, with the texts "vm", "storage" and "s3" in that order (each term underlines its literal run rather than a scattered subsequence). 7. Fill the finder input with "vm-storage". 8. There should come to be exactly 2 ".grp-row" elements (a single term still matches every file containing it).`, func(s *shelley) {
		dir := s.tempDir("finder")
		for _, name := range []string{"vm-storage-s3-backup-design.md", "vm-storage-replication.md", "s3-uploads.md"} {
			s.write(filepath.Join(dir, "docs", name), "x\n")
		}
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}

// A deep link to a conversation the first list page doesn't hold (an old
// one, or under load a just-created one) resolves its slug with a separate
// request. Cmd/Ctrl+P pressed before that lands opens the finder on a
// fallback directory; it must move once the conversation is known.
func TestE2EFileFinderFollowsLoadingConversationDir(t *testing.T) {
	e2e(t, useful(0), `The file finder opened while the conversation loads follows the conversation's directory once it arrives. The conversation /c/{{SLUG}} runs in {{DIR}}, which holds arrives-late.md; another conversation, in another directory, is there for the list to show meanwhile. The conversation list the page loads leaves /c/{{SLUG}} out, and the page's live stream and its request resolving the slug are both held back until call "release". Call "stand-in" returns "waiting" until the page has loaded the conversation list; then, if the directory of the list's most recent top-level conversation (the finder's stand-in) differs from {{DIR}}, it sets {{STAND_IN}} to it and returns "set". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. `+finderOpen+` 3. Call "stand-in" until it returns "set". 4. ".ff-dir-path" should have exactly the text "{{STAND_IN}}". 5. Fill the finder input with "arrives-late". 6. Call "release". 7. ".ff-dir-path" should come to have exactly the text "{{DIR}}". 8. The first ".grp-row" should contain "arrives-late.md". 9. The finder input should still have the value "arrives-late".`, func(s *shelley) {
		dir := s.tempDir("finder-early")
		s.write(filepath.Join(dir, "arrives-late.md"), "x\n")
		// Something for the list to show meanwhile, in another directory.
		s.conversation("Hello", "")
		slug := s.conversationIn(dir, "Hello").Slug
		s.v("SLUG", slug)
		s.v("DIR", dir)

		release := s.gate("release")
		hold := func(w http.ResponseWriter, r *http.Request) {
			if release.wait(r) {
				s.pass.ServeHTTP(w, r)
			}
		}
		// The stand-in: the directory of the list's most recent top-level
		// conversation, read from the list the page gets.
		var mu sync.Mutex
		standIn, listed := "", false
		s.route("GET /api/conversations/snapshot", func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := s.fetchJSON(r, &body); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			var kept []any
			top := ""
			found := false
			for _, c := range body["conversations"].([]any) {
				c := c.(map[string]any)
				if c["slug"] == slug {
					continue
				}
				kept = append(kept, c)
				if parent, _ := c["parent_conversation_id"].(string); !found && parent == "" {
					found = true
					top, _ = c["git_worktree_root"].(string)
					if top == "" {
						top, _ = c["cwd"].(string)
					}
				}
			}
			body["conversations"] = kept
			mu.Lock()
			standIn, listed = top, true
			mu.Unlock()
			writeJSON(w, body)
		})
		s.route("/api/stream2", hold)
		s.route("/api/conversation-by-slug/", hold)
		s.call("stand-in", func() string {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case !listed || standIn == "":
				return "waiting"
			case standIn == dir:
				return "the stand-in is the conversation's own directory"
			}
			s.v("STAND_IN", standIn)
			return "set"
		})
	})
}

// makeMostRecent renames conv until its last activity is strictly later than
// other's (activity times have one-second resolution, and a tie leaves the
// order to chance). It returns the final slug, which tells when an open page
// has caught up. Kept short: slugs are truncated, so a suffix on a long one
// could change nothing.
func (s *shelley) makeMostRecent(conv, other convo, touches *int) string {
	slug := ""
	s.poll(30*time.Second, func() error {
		*touches++
		var renamed struct {
			Slug string `json:"slug"`
		}
		s.api("POST", "/api/conversation/"+conv.ID+"/rename", map[string]string{"slug": fmt.Sprintf("recent-%s-%d", strings.ToLower(conv.ID), *touches)}, &renamed)
		slug = renamed.Slug
		var snapshot struct {
			Conversations []struct {
				ID        string    `json:"conversation_id"`
				UpdatedAt time.Time `json:"updated_at"`
			} `json:"conversations"`
		}
		s.api("GET", "/api/conversations/snapshot", nil, &snapshot)
		updated := map[string]time.Time{}
		for _, c := range snapshot.Conversations {
			updated[c.ID] = c.UpdatedAt
		}
		if !updated[conv.ID].After(updated[other.ID]) {
			return errors.New("not yet the most recent")
		}
		return nil
	})
	return slug
}

// On /new there is no conversation directory to wait for. The finder opens on
// the most recently active conversation's, which changes whenever another
// conversation does something; that must not move a finder in use.
func TestE2EFileFinderNewConversationStaysPut(t *testing.T) {
	e2e(t, useful(0), `On a new conversation, the file finder stays put when another conversation becomes the most recent. Two conversations: A, {{A_ID}}, in {{DIR_A}}, which holds only-in-a.md, and B in {{DIR_B}}, which holds only-in-b.md; B is the most recently active. Call "make A most recent" renames A until it is the most recently active conversation and then sets {{A_SLUG}} to its final slug. Perform the steps in order. 1. Navigate to /new and wait for the message input (data-testid "message-input") to be visible. 2. `+finderOpen+` 3. ".ff-dir-path" should have exactly the text "{{DIR_B}}". 4. Fill the finder input with "only-in". 5. The first ".grp-row" should contain "only-in-b.md". 6. Call "make A most recent". 7. The page hears of it: the drawer's '.conversation-item[data-conversation-id="{{A_ID}}"]' should come to contain the text "{{A_SLUG}}". 8. The open finder is still where the user is searching: ".ff-dir-path" should still have exactly the text "{{DIR_B}}", the first ".grp-row" should still contain "only-in-b.md", and the finder input should still have the value "only-in". 9. Press Escape. 10. The finder input should be hidden. 11. Press "p" (lowercase) with the Control modifier again. 12. ".ff-dir-path" should come to have exactly the text "{{DIR_A}}" (a fresh open takes the new most recent directory).`, func(s *shelley) {
		// As the spec's test.use: a 1280x800 viewport, no mobile, no touch.
		s.device(lazycue.Device{Width: 1280, Height: 800, Scale: lazycue.Pixel5.Scale})
		dirA := s.tempDir("finder-new-a")
		dirB := s.tempDir("finder-new-b")
		s.write(filepath.Join(dirA, "only-in-a.md"), "a\n")
		s.write(filepath.Join(dirB, "only-in-b.md"), "b\n")
		a := s.conversationIn(dirA, "Hello")
		b := s.conversationIn(dirB, "Hello")
		touches := 0
		s.makeMostRecent(b, a, &touches)
		s.v("A_ID", a.ID)
		s.v("DIR_A", dirA)
		s.v("DIR_B", dirB)
		var aTouches int
		s.call("make A most recent", func() string {
			s.v("A_SLUG", s.makeMostRecent(a, b, &aTouches))
			return "done"
		})
	})
}

// Typing a path re-roots the finder at that directory, so a file outside the
// conversation's working directory can still be opened in the editor.
func TestE2EFileFinderAbsolutePathOpensFile(t *testing.T) {
	e2e(t, useful(0), `In the file finder, an absolute path searches that directory and opens the file. The conversation /c/{{SLUG}} runs in a directory holding local.md; another directory, {{ELSEWHERE}}, holds handoff-notes.md, whose content is "far away content". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. `+finderOpen+` 3. Fill the finder input with "{{ELSEWHERE}}/handoff". 4. There should come to be exactly 1 ".grp-row" (up to 10s), containing "handoff-notes.md". 5. The list is no longer relative to the working directory, so the finder says where it is looking: ".ff-scope" should contain "{{ELSEWHERE}}". 6. With the finder input focused, press Enter. 7. A dialog (role "dialog") whose accessible name contains "Edit {{ELSEWHERE}}/handoff-notes.md" should become visible (up to 15s): Enter opens the file from the re-rooted directory, not a path joined against the conversation's directory. 8. In it, a ".view-line" containing "far away content" should be visible (up to 15s).`, func(s *shelley) {
		cwd := s.tempDir("finder-cwd")
		s.write(filepath.Join(cwd, "local.md"), "local\n")
		elsewhere := s.tempDir("finder-else")
		s.write(filepath.Join(elsewhere, "handoff-notes.md"), "far away content\n")
		s.v("SLUG", s.conversationIn(cwd, "Hello").Slug)
		s.v("ELSEWHERE", elsewhere)
	})
}

func TestE2EFileFinderClearingPathReturnsToCwd(t *testing.T) {
	e2e(t, useful(0), `In the file finder, clearing a path query returns to the working directory. The conversation /c/{{SLUG}} runs in a directory holding local-only.md and a subdirectory {{AWAY}} holding remote.md. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. `+finderOpen+` 3. Fill the finder input with "{{AWAY}}/". 4. ".ff-scope" should come to contain "{{AWAY}}" (up to 10s). 5. Fill the finder input with "local-only". 6. There should come to be exactly 1 ".grp-row" (up to 10s), containing "local-only.md". 7. There must be exactly 0 ".ff-scope" elements.`, func(s *shelley) {
		cwd := s.tempDir("finder-back")
		s.write(filepath.Join(cwd, "local-only.md"), "local\n")
		// A sibling to wander into, not a shared directory that is slow and
		// nondeterministic to walk.
		away := s.mkdir(filepath.Join(cwd, "away"))
		s.write(filepath.Join(away, "remote.md"), "remote\n")
		s.v("SLUG", s.conversationIn(cwd, "Hello").Slug)
		s.v("AWAY", away)
	})
}

func TestE2EFileFinderEmptyDirectory(t *testing.T) {
	e2e(t, useful(0), `In the file finder, a path to an empty directory says the directory is empty. The conversation /c/{{SLUG}} runs in a directory holding local.md and an empty subdirectory {{EMPTY}}. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. `+finderOpen+` 3. Fill the finder input with "{{EMPTY}}/" (naming a directory lists all of it). 4. ".grp-empty" should come to have exactly the text "No files in this directory." (up to 10s). 5. Fill the finder input with "{{EMPTY}}/zzz" (a pattern that matches nothing inside it). 6. ".grp-empty" should come to have exactly the text "No matching files." (up to 10s).`, func(s *shelley) {
		cwd := s.tempDir("finder-empty")
		s.write(filepath.Join(cwd, "local.md"), "local\n")
		s.v("EMPTY", s.mkdir(filepath.Join(cwd, "empty-dir")))
		s.v("SLUG", s.conversationIn(cwd, "Hello").Slug)
	})
}

// In a git repo the finder also greps file contents, so a term that appears
// nowhere in any file name still finds the file, and the row shows the
// matching line as a snippet with the term highlighted.
func TestE2EFileFinderContentOnlyMatch(t *testing.T) {
	e2e(t, useful(0), `In the file finder, a term matching only file content finds the file and shows a snippet. The conversation /c/{{SLUG}} runs in a git repository holding (untracked) recipes.txt, which reads "secret ingredient: cardamom", and shopping-list.txt, which reads "eggs and flour". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. `+finderOpen+` 3. Fill the finder input with "cardamom". 4. There should come to be exactly 1 ".grp-row" (up to 10s): "cardamom" matches no file name, so the only row is the content hit. 5. It should contain "recipes.txt". 6. Its ".ff-snippet" should be visible and contain "cardamom". 7. The snippet should hold exactly one "mark" element, with exactly the text "cardamom".`, func(s *shelley) {
		// git grep --untracked searches untracked files, so init suffices.
		dir := s.gitRepo(filepath.Join(s.dir, "finder-grep"))
		s.write(filepath.Join(dir, "recipes.txt"), "secret ingredient: cardamom\n")
		s.write(filepath.Join(dir, "shopping-list.txt"), "eggs and flour\n")
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}

// One file matches by name, another only by content: the finder should show
// both, with the (fast, first-phase) name match on top.
func TestE2EFileFinderNameMatchesFirst(t *testing.T) {
	e2e(t, useful(0), `In the file finder, name matches and content matches share the list, names first. The conversation /c/{{SLUG}} runs in a git repository holding (untracked) cardamom-notes.md, which reads "about the spice", and recipes.txt, which reads "secret ingredient: cardamom". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. `+finderOpen+` 3. Fill the finder input with "cardamom". 4. There should come to be exactly 2 ".grp-row" elements (up to 10s). 5. The first should contain "cardamom-notes.md" (the name match, rendered by the fast pass) and the second "recipes.txt" (the content-only hit). 6. The second row's ".ff-snippet mark" should be exactly one element, with exactly the text "cardamom".`, func(s *shelley) {
		dir := s.gitRepo(filepath.Join(s.dir, "finder-grep-mix"))
		s.write(filepath.Join(dir, "cardamom-notes.md"), "about the spice\n")
		s.write(filepath.Join(dir, "recipes.txt"), "secret ingredient: cardamom\n")
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}

// "cardamom" hits only paprika-notes.txt by content; "paprika" hits it by
// name. If the first keystroke's (delayed) content response applied after
// the second keystroke's results, the row would gain a snippet mentioning
// cardamom, which the second query never asked about.
func TestE2EFileFinderStaleContentResponseDropped(t *testing.T) {
	e2e(t, useful(0), `In the file finder, a stale content response from a superseded keystroke never lands. The conversation /c/{{SLUG}} runs in a git repository holding (untracked) paprika-notes.txt, which reads "cardamom goes well here", and other.txt, which reads "nothing relevant". The server's answer to the FIRST content-search request (GET /api/find-files with content=only) is held back until call "release"; every other request passes. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. `+finderOpen+` 3. Fill the finder input with "cardamom". 4. ".ff-grep-pending" should become visible (up to 10s): that keystroke's content search is now stuck. 5. Fill the finder input with "paprika" (this supersedes it; its own phases both land). 6. There should come to be exactly 1 ".grp-row" (up to 10s), containing "paprika-notes.txt". 7. Call "release". 8. Sleep 300ms to give the stale response a beat to (not) apply. 9. There must still be exactly 1 ".grp-row", and exactly 0 ".ff-snippet" elements.`, func(s *shelley) {
		dir := s.gitRepo(filepath.Join(s.dir, "finder-grep-stale"))
		s.write(filepath.Join(dir, "paprika-notes.txt"), "cardamom goes well here\n")
		s.write(filepath.Join(dir, "other.txt"), "nothing relevant\n")
		release := s.gate("release")
		var held atomic.Bool
		s.route("GET /api/find-files", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("content") == "only" && held.CompareAndSwap(false, true) {
				if !release.wait(r) {
					return // the superseding keystroke aborted it
				}
			}
			s.pass.ServeHTTP(w, r)
		})
		s.v("SLUG", s.conversationIn(dir, "Hello").Slug)
	})
}
