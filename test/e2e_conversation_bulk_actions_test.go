package test

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Ported from ui/e2e/conversation-bulk-actions.spec.ts.

// bulkWorld names the conversations a test made, so that calls can report
// on them by name: their IDs differ from run to run.
type bulkWorld struct {
	s     *shelley
	names map[string]string // by ID
	convs map[string]convo  // by name
}

// bulkSetup starts a world whose calls "archived" and "tags" report what
// the server holds.
func bulkSetup(s *shelley) *bulkWorld {
	w := &bulkWorld{s: s, names: map[string]string{}, convs: map[string]convo{}}
	s.call("archived", w.archived)
	s.call("tags", w.tags)
	return w
}

// conv starts a conversation called name, tagged tags, and sets
// {{<NAME>}} and {{<NAME>_SLUG}} to its ID and slug.
func (w *bulkWorld) conv(name, message string, tags ...string) convo {
	c := w.s.conversation(message, "")
	w.s.tagFilterSetTags(c.ID, tags...)
	w.names[c.ID] = name
	w.convs[name] = c
	w.s.v(strings.ToUpper(name), c.ID)
	w.s.v(strings.ToUpper(name)+"_SLUG", c.Slug)
	return c
}

// name is what the world calls the conversation id.
func (w *bulkWorld) name(id string) string {
	if n, ok := w.names[id]; ok {
		return n
	}
	return id
}

// archived lists the names of the archived conversations, sorted.
func (w *bulkWorld) archived() string {
	var convs []struct {
		ID string `json:"conversation_id"`
	}
	w.s.api("GET", "/api/conversations/archived", nil, &convs)
	names := []string{}
	for _, c := range convs {
		names = append(names, w.name(c.ID))
	}
	slices.Sort(names)
	return mustJSON(names)
}

// tagsOf is a conversation's tags, sorted.
func (w *bulkWorld) tagsOf(id string) []string {
	var got struct {
		Conversation struct {
			Tags string `json:"tags"`
		} `json:"conversation"`
	}
	w.s.api("GET", "/api/conversation/"+id, nil, &got)
	tags := []string{}
	if got.Conversation.Tags != "" {
		w.s.check(json.Unmarshal([]byte(got.Conversation.Tags), &tags))
	}
	slices.Sort(tags)
	return tags
}

// tags maps the name of every conversation of the world to its tags.
func (w *bulkWorld) tags() string {
	all := map[string][]string{}
	for name, c := range w.convs {
		all[name] = w.tagsOf(c.ID)
	}
	return mustJSON(all)
}

// bulkIDFromPath is the conversation ID of /api/conversation/<id>/<verb>.
func bulkIDFromPath(r *http.Request) string {
	return r.PathValue("id")
}

// How the spec's tests drive the drawer, on the Pixel 5 they ran on.
const bulkDrawer = `Conversation rows are ".conversation-item[data-conversation-id='<id>']" ("the row of <id>"), and "the check" of a row is its ".drawer-selection-check" checkbox; whether a checkbox "is checked" is an eval of its .checked (polled with expect). "The visible rows" are the elements matching ".drawer .conversation-item" with "visible": true. "The status" is ".drawer-bulk-status" and "the selection count" is ".drawer-bulk-count". To "open the search at P": navigate to P, click the button "Open conversations", click the button "Search conversations...", then wait for ".drawer-search-input" to be visible. To "select the N matches": click the button "Select conversations", click the checkbox named exactly "Select all N matching conversations" (role checkbox), then check that the selection count reads exactly "N selected". `

// Visual order, not creation order: grouping and sorting can reorder rows.
func TestE2EBulkSelectionTagsArchive(t *testing.T) {
	e2e(t, `selection stays in the filtered list; tags preserve untouched values; archive can be undone and restored. Conversations {{B0}}, {{B1}} and {{B2}} are tagged bulk and bulk-common, and {{B0}} also bulk-private; {{OUTSIDE}} (slug {{OUTSIDE_SLUG}}) has no tags. Call "tags" returns the sorted tags of each conversation by name (b0, b1, b2, outside), and call "archived" the sorted names of the archived conversations. `+bulkDrawer+drawerQuery+drawerDirect+drawerSteps+`"The tag popover" is ".drawer-bulk-popover:has(input[aria-label='New tag'])", and its checkbox for tag T is the input inside its label whose text contains "#T". 1. Open the search at /c/{{OUTSIDE_SLUG}}. 2. Set the query to "tag:bulk ". 3. There are exactly 3 visible rows, and the row of {{OUTSIDE}} is not visible (assert_not_visible). 4. Click the button "Select conversations". 5. Click the check of the first visible row; then click, with modifiers "Shift", the check of the last visible row, aiming with a point expression that computes the center of the last of the ".drawer .conversation-item .drawer-selection-check" elements whose checkVisibility() is true. 6. The selection count reads exactly "3 selected". 7. The checks of the rows of {{B0}}, {{B1}} and {{B2}} are checked. 8. The URL is still exactly /c/{{OUTSIDE_SLUG}} (assert_url value). 9. Click the checkbox "Select all 3 matching conversations" to uncheck it: it is not checked, and the selection count reads exactly "0 selected". 10. Click it again to check it, and it is checked. 11. Click the button "Tag selected conversations". 12. The tag popover's checkbox for bulk-private is indeterminate (eval of its .indeterminate, expect "true"; some of the selection have it). 13. Its checkbox for bulk-common is checked; click it to uncheck it, and it is not checked. 14. Fill the tag popover's input labelled "New tag" (role textbox, exact name) with "bulk-added". 15. Click the button "Apply tags" in the tag popover. 16. The status reads exactly "Tags updated on 3 conversations". 17. Call "tags" with expect '{"b0":["bulk","bulk-added","bulk-private"],"b1":["bulk","bulk-added"],"b2":["bulk","bulk-added"],"outside":[]}' (untouched tags stay). 18. Click the button "Archive selected conversations". 19. The status contains "3 archived". 20. The rows of {{B0}}, {{B1}} and {{B2}} are not visible. 21. Call "archived" with expect '["b0","b1","b2"]'. 22. Click the button "Undo" inside the status (selector ".drawer-bulk-status" with role button, exact name). 23. The status reads exactly "Undone". 24. The rows of {{B0}}, {{B1}} and {{B2}} are visible, and call "archived" with expect '[]'. 25. Select the 3 matches. 26. Click the button "Archive selected conversations". 27. The status contains "3 archived". 28. Click the button "View Archived". 29. There are exactly 3 visible rows. 30. Select the 3 matches. 31. Click the button "Restore selected conversations". 32. The status reads exactly "3 restored". 33. The button "View Archived" is visible. 34. The rows of {{B0}}, {{B1}} and {{B2}} are visible, and call "archived" with expect '[]'.`, func(s *shelley) {
		w := bulkSetup(s)
		w.conv("b0", "echo: bulk 0", "bulk", "bulk-common", "bulk-private")
		w.conv("b1", "echo: bulk 1", "bulk", "bulk-common")
		w.conv("b2", "echo: bulk 2", "bulk", "bulk-common")
		w.conv("outside", "echo: outside bulk filter")
	})
}

func TestE2EBulkWorkspaceFailureBlocksArchive(t *testing.T) {
	e2e(t, `workspace failure blocks archive instead of assuming there are no VMs. Conversation {{C}} (slug {{C_SLUG}}) is tagged wsfail. The test answers every request for /api/workspace with a 500 "workspace unavailable" (call "workspace-requested" says "true" once there was one), and every archive request with a 500 (call "archive-requests" counts them). Call "archived" returns the sorted names of the archived conversations ({{C}} is c). `+bulkDrawer+drawerQuery+drawerDirect+drawerSteps+`1. Open the search at /c/{{C_SLUG}}. 2. Set the query to "tag:wsfail ". 3. Select the 1 matches. 4. Click the button "Archive selected conversations". 5. ".drawer-row-error" contains "workspace unavailable". 6. The button "Archive selected conversations" is enabled. 7. Call "workspace-requested" with expect "true". 8. Call "archive-requests" with expect "0" (no archive was attempted). 9. The check of the row of {{C}} is checked. 10. The button "Undo" inside the status (selector ".drawer-bulk-status" with role button, exact name) is not visible (assert_not_visible). 11. Call "archived" with expect '[]'.`, func(s *shelley) {
		w := bulkSetup(s)
		w.conv("c", "echo: workspace unavailable", "wsfail")
		workspace := s.signal("workspace-requested")
		s.route("/api/workspace", func(rw http.ResponseWriter, r *http.Request) {
			workspace.Store(true)
			rw.Header().Set("Content-Type", "text/plain")
			rw.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(rw, "workspace unavailable")
		})
		archives := s.counter("archive-requests")
		s.route("/api/conversation/{id}/archive", func(rw http.ResponseWriter, r *http.Request) {
			archives.Add(1)
			http.Error(rw, "Archive must wait for workspace", http.StatusInternalServerError)
		})
	})
}

func TestE2EBulkFoldedGroups(t *testing.T) {
	e2e(t, `folded tag groups reset selection and stay out of Shift ranges. Conversations {{A}}, {{B}} (slug {{B_SLUG}}) and {{C}} are all tagged fold, and each also fold-a, fold-b and fold-c respectively. The test records the archive requests: call "archive-requests" returns the sorted names (a, b, c) of the conversations archive was requested for, and call "archived" the sorted names of the archived conversations. `+bulkDrawer+drawerQuery+drawerDirect+drawerSteps+`1. Open the search at /c/{{B_SLUG}}. 2. Set the query to "tag:fold ". 3. Click the button "View options", then the button "Tags". 4. The texts of ".conversation-group .conversation-group-label" are exactly ["#fold #fold-a", "#fold #fold-b", "#fold #fold-c"]. 5. Click the button "Select conversations". 6. Click the check of the row of {{B}}. 7. The selection count reads exactly "1 selected". 8. Fold the middle group: click the ".conversation-group-header" of the ".conversation-group" whose ".conversation-group-label" contains "#fold-b" (has_text "#fold-b" on ".conversation-group-header"). 9. The row of {{B}} is not visible (assert_not_visible). 10. The button "Select conversations" is visible (folding reset the selection). 11. Select the 3 matches. 12. Click the checkbox "Select all 3 matching conversations" to uncheck it; the selection count reads exactly "0 selected". 13. There are exactly 2 visible checks: assert_count of ".drawer .conversation-item .drawer-selection-check" with "visible": true is 2. 14. Click the first visible check; then click, with modifiers "Shift", the last visible check, aiming with a point expression that computes the center of the last of the ".drawer .conversation-item .drawer-selection-check" elements whose checkVisibility() is true. 15. The selection count reads exactly "2 selected" (the folded row stays out of the range). 16. Click the button "Archive selected conversations". 17. The status contains "2 archived". 18. Call "archive-requests" with expect '["a","c"]'. 19. Call "archived" with expect '["a","c"]' ({{B}} was not archived).`, func(s *shelley) {
		w := bulkSetup(s)
		for _, x := range []string{"a", "b", "c"} {
			w.conv(x, "echo: fold "+x, "fold", "fold-"+x)
		}
		var mu sync.Mutex
		archived := []string{}
		s.route("/api/conversation/{id}/archive", func(rw http.ResponseWriter, r *http.Request) {
			mu.Lock()
			archived = append(archived, w.name(bulkIDFromPath(r)))
			mu.Unlock()
			s.pass.ServeHTTP(rw, r)
		})
		s.call("archive-requests", func() string {
			mu.Lock()
			defer mu.Unlock()
			return mustJSON(slices.Sorted(slices.Values(archived)))
		})
	})
}

func TestE2EBulkArchivedRevisits(t *testing.T) {
	e2e(t, `Archived revisits exclude restored rows and include newly archived rows. Conversations {{RESTORED}}, {{KEPT}} and {{LATER}} (slug {{LATER_SLUG}}) are tagged archive-cache; {{RESTORED}} and {{KEPT}} are archived. Call "archived" returns the sorted names (restored, kept, later) of the archived conversations. `+bulkDrawer+drawerQuery+drawerDirect+drawerSteps+`1. Open the search at /c/{{LATER_SLUG}}. 2. Set the query to "tag:archive-cache ". 3. Click the button "View Archived". 4. There are exactly 2 visible rows. 5. Click the button "Select conversations". 6. Click the check of the row of {{RESTORED}}. 7. Click the button "Restore selected conversations". 8. The status reads exactly "1 restored". 9. Click the button "View Archived". 10. The row of {{KEPT}} is visible; the rows of {{RESTORED}} and {{LATER}} are not visible (assert_not_visible). 11. There is exactly 1 visible row. 12. Click the button "Back to conversations". 13. The row of {{RESTORED}} is visible. 14. Click the button "Select conversations". 15. Click the check of the row of {{LATER}}. 16. Click the button "Archive selected conversations". 17. The status contains "1 archived". 18. Click the button "View Archived". 19. The rows of {{KEPT}} and {{LATER}} are visible, and the row of {{RESTORED}} is not visible. 20. There are exactly 2 visible rows. 21. Call "archived" with expect '["kept","later"]'.`, func(s *shelley) {
		w := bulkSetup(s)
		restored := w.conv("restored", "echo: restore", "archive-cache")
		kept := w.conv("kept", "echo: keep archived", "archive-cache")
		w.conv("later", "echo: archive later", "archive-cache")
		for _, c := range []convo{restored, kept} {
			s.api("POST", "/api/conversation/"+c.ID+"/archive", nil, nil)
		}
	})
}

func TestE2EBulkPartialUndoRetries(t *testing.T) {
	e2e(t, `partial archive Undo keeps progress and retries only unfinished restores. Conversations {{FIRST}} and {{SECOND}} are tagged undo-retry. The test records every unarchive request and fails the second one with a 500 "undo restore failure". Call "after-first-undo" checks that exactly two restores were requested, one for each conversation, and that the one requested first is no longer archived while the other still is; it returns "ok" or what is wrong. Call "after-second-undo" checks that the restores requested are exactly the first two followed by a repeat of the second (only the unfinished one was retried), returning "ok" or what is wrong. Call "archived" returns the sorted names (first, second) of the archived conversations. `+bulkDrawer+drawerQuery+drawerDirect+drawerSteps+`"The Undo button" is the button "Undo" inside the status (selector ".drawer-bulk-status" with role button, exact name). 1. Open the search at /new. 2. Set the query to "tag:undo-retry ". 3. Select the 2 matches. 4. Click the button "Archive selected conversations". 5. The status contains "2 archived". 6. Click the Undo button. 7. ".drawer-row-error" contains "undo restore failure". 8. The status contains "1 of 2". 9. The Undo button is visible and enabled. 10. Call "after-first-undo" with expect "ok". 11. Click the Undo button. 12. The status reads exactly "Undone". 13. Call "after-second-undo" with expect "ok". 14. The rows of {{FIRST}} and {{SECOND}} are visible, and call "archived" with expect '[]'.`, func(s *shelley) {
		w := bulkSetup(s)
		first := w.conv("first", "echo: undo first", "undo-retry")
		second := w.conv("second", "echo: undo second", "undo-retry")
		var mu sync.Mutex
		restored := []string{}
		s.route("/api/conversation/{id}/unarchive", func(rw http.ResponseWriter, r *http.Request) {
			mu.Lock()
			restored = append(restored, bulkIDFromPath(r))
			n := len(restored)
			mu.Unlock()
			if n == 2 {
				rw.Header().Set("Content-Type", "text/plain")
				rw.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(rw, "undo restore failure")
				return
			}
			s.pass.ServeHTTP(rw, r)
		})
		requests := func() []string {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(restored)
		}
		s.call("after-first-undo", func() string {
			got := requests()
			if len(got) != 2 {
				return fmt.Sprintf("%d restores requested", len(got))
			}
			if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values([]string{first.ID, second.ID}))) {
				return "restores requested for " + w.name(got[0]) + " and " + w.name(got[1])
			}
			archived := w.archived()
			if strings.Contains(archived, `"`+w.name(got[0])+`"`) {
				return "the completed restore is archived: " + archived
			}
			if !strings.Contains(archived, `"`+w.name(got[1])+`"`) {
				return "the unfinished restore is not archived: " + archived
			}
			return "ok"
		})
		s.call("after-second-undo", func() string {
			got := requests()
			if len(got) != 3 || got[2] != got[1] || got[0] == got[1] {
				names := []string{}
				for _, id := range got {
					names = append(names, w.name(id))
				}
				return "restores requested: " + strings.Join(names, ", ")
			}
			return "ok"
		})
	})
}

// The first conversation in the drawer's order runs on a VM, so the batch
// asks before its very first archive. Playwright loaded the page once to
// read that order before mocking the workspace; here setup works it out as
// the drawer sorts (sortConversationsByBucket), and the page confirms it.
func TestE2EBulkCancelVMArchive(t *testing.T) {
	e2e(t, `cancelling the first VM archive confirmation stops the batch and keeps selection. Conversations {{FIRST}} (slug {{FIRST_SLUG}}) and {{SECOND}} are tagged cancel-archive; the drawer lists {{FIRST_VISUAL}} first. The test's workspace says conversation {{FIRST_VISUAL}} runs on the VM bulk-cancel-vm, and answers every archive request with a 500 (call "archive-requests" counts them). Call "archived" returns the sorted names (first, second) of the archived conversations. `+bulkDrawer+drawerQuery+drawerDirect+drawerSteps+`"The confirmation" is the element with role dialog named exactly "Archive conversation?". 1. Open the search at /c/{{FIRST_SLUG}}. 2. Set the query to "tag:cancel-archive ". 3. There are exactly 2 visible rows, and the first visible row's data-conversation-id is exactly "{{FIRST_VISUAL}}". 4. Select the 2 matches. 5. Click the button "Archive selected conversations". 6. The confirmation contains the text "Archiving deletes VM bulk-cancel-vm". 7. Call "archive-requests" with expect "0". 8. Click the button "Cancel" inside the confirmation (exact name). 9. There is no confirmation. 10. The status reads exactly "0 of 2 archived; cancelled". 11. The button "Archive selected conversations" is enabled. 12. The selection count reads exactly "2 selected". 13. The checks of the rows of {{FIRST}} and {{SECOND}} are checked. 14. Call "archived" with expect '[]'. 15. Call "archive-requests" with expect "0".`, func(s *shelley) {
		w := bulkSetup(s)
		first := w.conv("first", "echo: VM archive target", "cancel-archive")
		second := w.conv("second", "echo: next archive target", "cancel-archive")
		type conv struct {
			ID      string    `json:"conversation_id"`
			Created time.Time `json:"created_at"`
			Updated time.Time `json:"updated_at"`
		}
		var convs []conv
		for _, c := range []convo{first, second} {
			var got struct {
				Conversation conv `json:"conversation"`
			}
			s.api("GET", "/api/conversation/"+c.ID, nil, &got)
			convs = append(convs, got.Conversation)
		}
		bucket := func(t time.Time) int64 { return t.UnixMilli() / (5 * 60 * 1000) }
		slices.SortFunc(convs, func(a, b conv) int {
			if d := bucket(b.Updated) - bucket(a.Updated); d != 0 {
				return int(d)
			}
			if d := b.Created.Compare(a.Created); d != 0 {
				return d
			}
			return strings.Compare(b.ID, a.ID)
		})
		firstVisual := convs[0].ID
		s.v("FIRST_VISUAL", firstVisual)
		s.route("GET /api/workspace", func(rw http.ResponseWriter, r *http.Request) {
			var workspace map[string]any
			if err := s.fetchJSON(r, &workspace); err != nil {
				http.Error(rw, err.Error(), http.StatusBadGateway)
				return
			}
			workspace["vms"] = []map[string]any{{
				"conversation_id":       firstVisual,
				"vm_name":               "bulk-cancel-vm",
				"base_vm":               "",
				"integration":           "exe",
				"shared":                false,
				"status":                "ready",
				"error":                 "",
				"ssh_dest":              "",
				"https_url":             "",
				"dir":                   s.cwd,
				"conversation_archived": false,
				"replaced":              false,
			}}
			writeJSON(rw, workspace)
		})
		archives := s.counter("archive-requests")
		s.route("/api/conversation/{id}/archive", func(rw http.ResponseWriter, r *http.Request) {
			archives.Add(1)
			http.Error(rw, "Unexpected archive request after cancellation", http.StatusInternalServerError)
		})
	})
}

func TestE2EBulkMoveBetweenProjects(t *testing.T) {
	e2e(t, `moves idle conversations between named projects and undoes the move. Projects move-source and move-destination run in the server's directory. Conversations {{M0}} (slug {{M0_SLUG}}) and {{M1}} are tagged move and in project move-source. Call "projects" returns the project of each conversation by name (m0, m1), as the workspace reports it. `+bulkDrawer+drawerQuery+drawerDirect+drawerSteps+`"The Undo button" is the button "Undo" inside the status (selector ".drawer-bulk-status" with role button, exact name). 1. Open the search at /c/{{M0_SLUG}}. 2. Set the query to "tag:move ". 3. Select the 2 matches. 4. Click the button "Move to project". 5. Click the button named exactly "move-destination" inside ".drawer-bulk-popover". 6. The status contains "2 moved to move-destination". 7. Call "projects" with expect '{"m0":"move-destination","m1":"move-destination"}'. 8. Click the Undo button. 9. The status reads exactly "Undone". 10. Call "projects" with expect '{"m0":"move-source","m1":"move-source"}'. 11. Reload the page (reload action). 12. The option chosen in the select labelled "Conversation project" reads exactly "move-source": eval with expect "move-source" of (() => { const s = [...document.querySelectorAll("select")].find(s => [...s.labels].some(l => l.textContent.trim() === "Conversation project")); return s ? (s.selectedOptions[0]?.textContent.trim() ?? "none") : "no select"; })().`, func(s *shelley) {
		w := bulkSetup(s)
		for _, name := range []string{"move-source", "move-destination"} {
			s.api("POST", "/api/projects", map[string]any{"name": name, "instructions": "", "cwd": s.cwd, "actions": []any{}}, nil)
		}
		for i := range 2 {
			c := w.conv(fmt.Sprintf("m%d", i), fmt.Sprintf("echo: move %d", i), "move")
			s.api("PUT", "/api/conversation/"+c.ID+"/project", map[string]any{"project": "move-source"}, nil)
		}
		s.call("projects", func() string {
			var workspace struct {
				Conversations []struct {
					ID      string `json:"conversation_id"`
					Project string `json:"project"`
				} `json:"conversations"`
			}
			s.api("GET", "/api/workspace", nil, &workspace)
			projects := map[string]string{}
			for _, c := range workspace.Conversations {
				if name, ok := w.names[c.ID]; ok {
					projects[name] = c.Project
				}
			}
			return mustJSON(projects)
		})
	})
}

func TestE2EBulkPartialTagFailure(t *testing.T) {
	e2e(t, `a partial tag failure is visible and retry preserves existing tags. Conversations {{FIRST}} (slug {{FIRST_SLUG}}) and {{SECOND}} are tagged tagfail. The test fails the second request to set tags with a 500 "bulk tag failure" and lets the others through; call "tag-requests" counts them all. Call "after-failure" checks that exactly one request succeeded and that the conversation it was for is tagged exactly tagfail and tagfail-added while the other is still tagged just tagfail, returning "ok" or what is wrong. Call "tags" returns the sorted tags of each conversation by name (first, second). `+bulkDrawer+drawerQuery+drawerDirect+drawerSteps+`1. Open the search at /c/{{FIRST_SLUG}}. 2. Set the query to "tag:tagfail ". 3. Select the 2 matches. 4. Click the button "Tag selected conversations". 5. Fill the input labelled "New tag" (role textbox, exact name) with "tagfail-added". 6. Click the button "Apply tags". 7. ".drawer-row-error" contains "bulk tag failure". 8. The status reads exactly "Tags updated on 1 of 2 conversations". 9. The selection count reads exactly "2 selected". 10. Call "tag-requests" with expect "2". 11. Call "after-failure" with expect "ok". 12. The checks of the rows of {{FIRST}} and {{SECOND}} are checked. 13. The button "Apply tags" is enabled. 14. Click the button "Apply tags". 15. The status reads exactly "Tags updated on 2 conversations". 16. There is no ".drawer-row-error". 17. Call "tags" with expect '{"first":["tagfail","tagfail-added"],"second":["tagfail","tagfail-added"]}'.`, func(s *shelley) {
		w := bulkSetup(s)
		w.conv("first", "echo: first tag target", "tagfail")
		w.conv("second", "echo: second tag target", "tagfail")
		var mu sync.Mutex
		attempted := 0
		successful := []string{}
		s.route("/api/conversation/{id}/tags", func(rw http.ResponseWriter, r *http.Request) {
			mu.Lock()
			attempted++
			n := attempted
			if n != 2 {
				successful = append(successful, bulkIDFromPath(r))
			}
			mu.Unlock()
			if n == 2 {
				rw.Header().Set("Content-Type", "text/plain")
				rw.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(rw, "bulk tag failure")
				return
			}
			s.pass.ServeHTTP(rw, r)
		})
		s.call("tag-requests", func() string {
			mu.Lock()
			defer mu.Unlock()
			return fmt.Sprint(attempted)
		})
		s.call("after-failure", func() string {
			mu.Lock()
			done := slices.Clone(successful)
			mu.Unlock()
			if len(done) != 1 {
				return fmt.Sprintf("%d requests succeeded", len(done))
			}
			for name, c := range w.convs {
				want := []string{"tagfail"}
				if c.ID == done[0] {
					want = []string{"tagfail", "tagfail-added"}
				}
				if got := w.tagsOf(c.ID); !slices.Equal(got, want) {
					return fmt.Sprintf("%s is tagged %v, not %v", name, got, want)
				}
			}
			return "ok"
		})
	})
}

// filtersFixedTime has Date report t from page load on, as Playwright's
// page.clock.setFixedTime does, while timers keep running.
func (s *shelley) filtersFixedTime(t time.Time) {
	s.clock(t)
	s.initScript(fmt.Sprintf("window.__clock.setFixedTime(%d);", t.UnixMilli()))
}

func TestE2EBulkActivityFilter(t *testing.T) {
	e2e(t, `Activity filters updated time for each period and removes its token. The page's clock is fixed at 2026-11-20T12:00:00Z. The test lists exactly four conversations, copies of one: {{SEED}} (slug {{SEED_SLUG}}) updated half a day before, bulk-activity-1 three days before, bulk-activity-2 14 days before and bulk-activity-3 45 days before; their live update stream is cut off. `+bulkDrawer+drawerDirect+drawerSteps+`"The token" is [data-conversation-query-token="activity"]. "Pick the period P" means: click the button "Activity", then click the button named exactly P inside ".drawer-bulk-popover", then check that the token is visible. 1. Open the search at /c/{{SEED_SLUG}}. 2. Pick the period "Past day"; the row of {{SEED}} is visible, and the rows of bulk-activity-1, bulk-activity-2 and bulk-activity-3 are not visible (assert_not_visible). 3. Pick the period "Past week"; the rows of {{SEED}} and bulk-activity-1 are visible, and the rows of bulk-activity-2 and bulk-activity-3 are not visible. 4. Pick the period "Past month"; the rows of {{SEED}}, bulk-activity-1 and bulk-activity-2 are visible, and the row of bulk-activity-3 is not visible. 5. Pick the period "1d+"; the rows of bulk-activity-1, bulk-activity-2 and bulk-activity-3 are visible, and the row of {{SEED}} is not visible. 6. Pick the period "1w+"; the rows of bulk-activity-2 and bulk-activity-3 are visible, and the rows of {{SEED}} and bulk-activity-1 are not visible. 7. Pick the period "1mo+"; the row of bulk-activity-3 is visible, and the rows of {{SEED}}, bulk-activity-1 and bulk-activity-2 are not visible. 8. Click the button "Remove activity filter". 9. There is no token. 10. There are exactly 4 visible rows.`, func(s *shelley) {
		now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
		s.filtersFixedTime(now)
		seed := s.conversation("echo: activity seed", "")
		var corpus []map[string]any
		s.api("GET", "/api/conversations", nil, &corpus)
		i := slices.IndexFunc(corpus, func(c map[string]any) bool { return c["conversation_id"] == seed.ID })
		if i < 0 {
			s.fail("the seed is not listed")
		}
		var convs []map[string]any
		for index, days := range []float64{0.5, 3, 14, 45} {
			c := maps.Clone(corpus[i])
			if index > 0 {
				c["conversation_id"] = fmt.Sprintf("bulk-activity-%d", index)
				c["slug"] = fmt.Sprintf("bulk-activity-%d", index)
			}
			c["updated_at"] = now.Add(-time.Duration(days * float64(24*time.Hour))).Format("2006-01-02T15:04:05.000Z")
			c["created_at"] = "2026-01-01T00:00:00Z"
			c["working"] = false
			c["subagent_count"] = 0
			convs = append(convs, c)
		}
		s.v("SEED", seed.ID)
		s.v("SEED_SLUG", seed.Slug)
		s.route("/api/conversations/snapshot", func(rw http.ResponseWriter, r *http.Request) {
			writeJSON(rw, map[string]any{"conversations": convs, "hash": "bulk-activity"})
		})
		s.route("/api/stream2", abort)
	})
}
