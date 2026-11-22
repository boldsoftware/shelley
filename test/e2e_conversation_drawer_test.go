package test

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	lazycue "github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/conversation-drawer.spec.ts.

// drawerDevice is the screen the spec's tests ran on: Playwright's Pixel 5
// with its viewport, isMobile and hasTouch overridden, so it kept the
// phone's pixel density.
func drawerDevice(width, height int) lazycue.Device {
	return lazycue.Device{Width: width, Height: height, Scale: lazycue.Pixel5.Scale}
}

// drawerTest runs a test of the spec's on its default 1280x720 screen
// without a touch screen, which setup may change.
func drawerTest(t *testing.T, useful lazycue.Useful, description string, setup func(s *shelley)) {
	t.Helper()
	e2e(t, useful, description, func(s *shelley) {
		s.device(drawerDevice(1280, 720))
		if setup != nil {
			setup(s)
		}
	})
}

// drawerConv is a conversation of a stubbed list, as the spec's
// conversation() made it.
type drawerConv map[string]any

// drawerConversation is a conversation whose ID (and slug, unless it is a
// draft) is id.
func drawerConversation(id string, draft bool) drawerConv {
	c := drawerConv{
		"conversation_id":            id,
		"slug":                       id,
		"user_initiated":             true,
		"created_at":                 "2026-07-23T12:00:00Z",
		"updated_at":                 "2026-07-23T12:00:00Z",
		"cwd":                        nil,
		"archived":                   false,
		"parent_conversation_id":     nil,
		"model":                      "predictable",
		"conversation_options":       "{}",
		"current_generation":         1,
		"agent_working":              false,
		"tags":                       "[]",
		"is_draft":                   draft,
		"draft":                      "",
		"queued_messages":            "[]",
		"turn_interrupted":           false,
		"external_conversation_id":   nil,
		"external_endpoint":          nil,
		"next_prompt_suggestion":     "",
		"next_prompt_suggestion_seq": 0,
		"working":                    false,
		"subagent_count":             0,
		"preview":                    "Preview",
		"max_sequence_id":            0,
	}
	if draft {
		c["slug"] = nil
		c["draft"] = "unfinished message"
	}
	return c
}

// with sets fields of c.
func (c drawerConv) with(kv ...any) drawerConv {
	for i := 0; i < len(kv); i += 2 {
		c[kv[i].(string)] = kv[i+1]
	}
	return c
}

// participants says who wrote in c: emails, each with one message.
func (c drawerConv) participants(emails ...string) drawerConv {
	ps := []map[string]any{}
	for _, e := range emails {
		ps = append(ps, map[string]any{"email": e, "message_count": 1})
	}
	c["participants"] = ps
	return c
}

// tags tags c.
func (c drawerConv) tags(tags ...string) drawerConv {
	c["tags"] = mustJSON(tags)
	return c
}

// drawerList is a stubbed conversation list.
type drawerList struct {
	mu    sync.Mutex
	convs []drawerConv
	// search answers searches; nil answers them with the list. Set it in
	// setup, before the page loads; it is not changed later.
	search http.HandlerFunc
}

// update changes the list under its lock.
func (l *drawerList) update(f func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f()
}

// list returns a copy of the conversations listed now, so that encoding it
// races no update, which may change a conversation in place.
func (l *drawerList) list() []drawerConv {
	l.mu.Lock()
	defer l.mu.Unlock()
	convs := make([]drawerConv, len(l.convs))
	for i, c := range l.convs {
		convs[i] = maps.Clone(c)
	}
	return convs
}

// stubDrawerList has the server list exactly convs, answer searches
// with them (unless the list's search is set), and cut off the live stream,
// as the spec's stubConversationList did.
func (s *shelley) stubDrawerList(convs ...drawerConv) *drawerList {
	l := &drawerList{convs: convs}
	s.route("GET /api/conversations/snapshot", func(w http.ResponseWriter, r *http.Request) {
		convs := l.list()
		writeJSON(w, map[string]any{"conversations": convs, "hash": fmt.Sprintf("test-%d", len(convs))})
	})
	s.route("/api/conversations/search", func(w http.ResponseWriter, r *http.Request) {
		if l.search != nil {
			l.search(w, r)
			return
		}
		writeJSON(w, l.list())
	})
	s.route("/api/stream2", abort)
	return l
}

// drawerAnswer is a handler that answers with v.
func drawerAnswer(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { writeJSON(w, v) }
}

// drawerThemes answers chapter index requests with index, counting them;
// call "themes" says how many there were.
func (s *shelley) drawerThemes(index map[string]any) {
	n := s.counter("themes")
	s.route("/api/conversations/themes", func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		writeJSON(w, index)
	})
}

// drawerChapter is one entry of a chapter index.
func drawerChapter(title, messageID string) []map[string]string {
	return []map[string]string{{"title": title, "message_id": messageID}}
}

// drawerSearchLog records the q of every search request; call "searches"
// lists them as JSON. Searches for any of hang never answer: "<q>-requested"
// says whether one was made and "<q>-cancelled" whether the browser gave up
// on it.
func (s *shelley) drawerSearchLog(l *drawerList, hit []drawerConv, hang ...string) {
	var mu sync.Mutex
	searches := []string{}
	requested := map[string]bool{}
	cancelled := map[string]bool{}
	l.search = func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		mu.Lock()
		searches = append(searches, q)
		requested[q] = true
		mu.Unlock()
		if slices.Contains(hang, q) {
			<-r.Context().Done()
			mu.Lock()
			cancelled[q] = true
			mu.Unlock()
			return
		}
		writeJSON(w, hit)
	}
	s.call("searches", func() string {
		mu.Lock()
		defer mu.Unlock()
		return mustJSON(searches)
	})
	for _, q := range hang {
		s.call(q+"-requested", func() string {
			mu.Lock()
			defer mu.Unlock()
			return strconv.FormatBool(requested[q])
		})
		s.call(q+"-cancelled", func() string {
			mu.Lock()
			defer mu.Unlock()
			return strconv.FormatBool(cancelled[q])
		})
	}
}

// The ways the descriptions drive the page.
const (
	drawerStubbed = `The test harness has already stubbed the server's conversation list and cut off its live update stream, so there is nothing to set up, intercept or check about that (do not look at the API): the drawer (".drawer") shows the conversations the test names, as rows [data-conversation-id="<id>"] whose title (".conversation-title") is their slug, which is their id (drafts have none), and whose preview is "Preview". `

	drawerQuery = `The search query editor is the contenteditable element with data-testid "conversation-query-editor" ("the editor"); its data-query-value attribute is the serialized query. "The query is X" means data-query-value is exactly X, spaces included. To "set the query to X": eval the editor's focus(), press_key "a" with modifiers "Control" (the editor's own select-all), insert_text X (as pasting would), then wait until the query is X. To "clear the query": eval the editor's focus(), press_key "a" with modifiers "Control", press Backspace, then wait until the query is "". Query tokens are [data-conversation-query-token="<kind>"] elements inside the editor (kind tag or user); "the token T" is the one whose text contains T. `

	drawerReload = `To "reload" the page, use the reload action. `

	drawerDirect = `Every selector you need is given here, so do not explore the source code or read other tests (the old Playwright spec is being replaced and does not apply): write all the steps in one run_steps call right away, and look something up only if a step fails. `

	drawerSteps = `Perform the numbered steps in order, each fully. A button "X" is {"role": "button", "name": "X", "exact": true}, with a "selector" to scope it when it is a row's, a menu's or a group's; but "there is no button X" counts {"role": "button", "name": "X"} without exact, so that a name containing X counts too. Texts that "are exactly [...]" are checked with assert_texts. "There is no S", "no S" or "none" means no element matches at all: assert_count 0, never a visibility check. Whether an element "has the class X" is an eval of its classList.contains('X') (polled with expect), never a visibility check: a collapsed drawer may have no size. `
)

// drawerVisibleButtons lists the aria-labels of the buttons in the drawer
// header's actions that Playwright's getByRole would count: the rendered ones.
const drawerVisibleButtons = `JSON.stringify([...document.querySelectorAll('.drawer-header-actions button')].filter(b => b.checkVisibility({visibilityProperty: true})).map(b => b.getAttribute('aria-label')))`

func drawerHeaderNewConversation(t *testing.T, useful lazycue.Useful, width int) {
	mobile := width < 768
	steps := fmt.Sprintf(`The drawer header starts a new conversation at %dpx. The screen is already %dx800 (do not resize it). A conversation exists at /c/{{SLUG}}. `, width, width) + drawerSteps + `1. Navigate to /c/{{SLUG}}. `
	last := "Collapse sidebar"
	if mobile {
		steps += `2. Click the button "Open conversations". `
		last = "Close conversations"
	}
	steps += fmt.Sprintf(`3. The drawer header's actions hold exactly four rendered buttons, in order "Search conversations...", "View options", "New Conversation" and %q: eval %s must return exactly ["Search conversations...","View options","New Conversation",%q]. 4. Click the button "New Conversation" inside ".drawer-header-actions". 5. The URL should become /new (assert_url value "/new"). 6. The message input (data-testid "message-input") should be empty.`, last, drawerVisibleButtons, last)
	if mobile {
		steps += ` 7. The drawer (".drawer") must not have the class "open".`
	}
	drawerTest(t, useful, steps, func(s *shelley) {
		s.device(drawerDevice(width, 800))
		s.v("SLUG", s.conversation("echo: drawer new", "").Slug)
	})
}

func TestE2EDrawerHeaderNewConversation390(t *testing.T) {
	drawerHeaderNewConversation(t, useful(0), 390)
}

func TestE2EDrawerHeaderNewConversation1280(t *testing.T) {
	drawerHeaderNewConversation(t, useful(0), 1280)
}

// Other specs' conversations shared Playwright's drawer; a tag of the test's
// own keeps just its two rows on screen and still, since scrolling the list
// closes any open menu.
func TestE2EDrawerContextMenu(t *testing.T) {
	drawerTest(t, useful(0), `Right-clicking a conversation row opens its actions at the pointer. Two conversations exist, rows [data-conversation-id="{{FIRST}}"] ("the first row") and [data-conversation-id="{{SECOND}}"] ("the second row"); the second is at /c/{{SECOND_SLUG}}. Both are tagged {{TAG}}. The context menu is data-testid "conversation-context-menu"; its items are its elements with role menuitem (a "selector" scoping a "role"), and its item "X" is the one named exactly X. "Fully in view" means assert_in_viewport with ratio 1. "Right-click the row at its edge" means click it with button "right" at x 4 and y half the row's height (probe the height first). `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Wait until the page has loaded the workspace (the menu offers more once it has): poll eval "performance.getEntriesByType('resource').some(e => new URL(e.name).pathname === '/api/workspace')" expecting true. 3. Click the button "Search conversations...". 4. Set the query to "tag:{{TAG}} ". 5. Exactly 2 ".conversation-item" elements are visible (others may exist hidden, so count with "visible": true). 6. The first row and the second row are each fully in view. 7. Right-click the second row's ".conversation-preview" 20px right of and 5px below its top-left corner (click, button "right", x 20, y 5). 8. The context menu is visible. 9. Its items' trimmed texts are exactly ["Archive","Rename","Edit tags"]. 10. The menu opened where the pointer was: its getBoundingClientRect() x is less than 4px from the preview's x + 20 and its y less than 4px from the preview's y + 5 (eval both rects now). 11. The second row has the class "context-menu-target". 12. The URL does not end with /c/{{SECOND_SLUG}}: eval "!location.href.endsWith('/c/{{SECOND_SLUG}}')" is true. 13. Right-click the first row at its edge (the open menu may cover its middle). 14. There is exactly one context menu element; the first row has the class "context-menu-target" and the second row does not. 15. Press Escape. 16. There is no context menu element, and the first row has no "context-menu-target" class. 17. A right-click anywhere else closes it: right-click the first row (at its center), and the context menu is visible. 18. Right-click the message input (data-testid "message-input"). 19. There is no context menu element and the first row has no "context-menu-target" class. 20. Opening it closes any row's actions menu: click the second row's button "Actions". 21. The actions menu (data-testid "conversation-actions-menu") is visible. 22. Right-click the first row at its edge. 23. The context menu is visible and there is no element with data-testid "conversation-actions-menu". 24. Press Escape; there is no context menu element. 25. Archive acts on the row right-clicked: right-click the first row (center), then click the context menu's item "Archive". 26. There is no context menu element, no first row element at all, and the second row is visible. 27. Call "archived" expecting "true" (the server lists the first conversation as archived). 28. Rename keeps the native menu in its text field: right-click the second row (center) and click the context menu's item "Rename". 29. The second row's ".drawer-rename-input" is focused (document.activeElement). 30. Right-click that input. 31. There is no context menu element. 32. Press Escape.`, func(s *shelley) {
		first := s.conversation("context menu first", "")
		second := s.conversation("context menu second", "")
		tag := "ctx-menu-" + first.ID
		for _, c := range []convo{first, second} {
			s.api("POST", "/api/conversation/"+c.ID+"/tags", map[string]any{"tags": []string{tag}}, nil)
		}
		// With projects, the menu would also offer Move to project.
		var workspace struct{ Projects []any }
		s.api("GET", "/api/workspace", nil, &workspace)
		if len(workspace.Projects) > 0 {
			s.fail("a fresh server has projects: %v", workspace.Projects)
		}
		s.v("FIRST", first.ID)
		s.v("SECOND", second.ID)
		s.v("SECOND_SLUG", second.Slug)
		s.v("TAG", tag)
		s.call("archived", func() string {
			var archived []struct {
				ID string `json:"conversation_id"`
			}
			s.api("GET", "/api/conversations/archived", nil, &archived)
			return strconv.FormatBool(slices.ContainsFunc(archived, func(c struct {
				ID string `json:"conversation_id"`
			},
			) bool {
				return c.ID == first.ID
			}))
		})
	})
}

func TestE2EDrawerDoubleClickRename(t *testing.T) {
	drawerTest(t, useful(0), `Double-clicking a conversation name renames it and Escape cancels. A conversation exists at /c/{{SLUG}}, row [data-conversation-id="{{ID}}"]; "the title" is that row's ".conversation-title" and "the input" its ".drawer-rename-input". `+drawerReload+drawerSteps+`1. Navigate to /new. 2. A single click still opens the conversation: click the title. 3. The URL should become /c/{{SLUG}} (assert_url value "/c/{{SLUG}}"). 4. The row has no ".drawer-rename-input" element. 5. Double-click the title (click with count 2). 6. The input is focused (document.activeElement) and its value is exactly "{{SLUG}}". 7. Fill the input with "discarded-name". 8. Press Escape. 9. The title's text is exactly "{{SLUG}}". 10. Navigate to /new. 11. Double-click the title. 12. The input is focused. 13. Fill the input with "{{NEW_SLUG}}". 14. Press Enter. 15. The title's text should become exactly "{{NEW_SLUG}}". 16. Reload. 17. The title's text is exactly "{{NEW_SLUG}}". 18. Click the title. 19. The URL should become /c/{{NEW_SLUG}} (assert_url value "/c/{{NEW_SLUG}}").`, func(s *shelley) {
		c := s.conversation("rename from name", "")
		s.v("ID", c.ID)
		s.v("SLUG", c.Slug)
		s.v("NEW_SLUG", "renamed-"+strings.ToLower(c.ID))
	})
}

// drawerSelection is the length of the selection in the focused input.
const drawerSelection = `document.activeElement.selectionEnd - document.activeElement.selectionStart`

func TestE2EDrawerRowTextNotSelectable(t *testing.T) {
	drawerTest(t, useful(0), `Row text is not selectable but rename and tag inputs are. A conversation exists, row [data-conversation-id="{{ID}}"] ("the row"). The context menu is data-testid "conversation-context-menu"; its item "X" is the element with role menuitem named exactly X inside it (a "selector" scoping a "role"). "Select all text of input I" means what Playwright's selectText does: eval I.select() then I.focus(). `+drawerSteps+`1. Navigate to /new. 2. The computed user-select of the row's ".conversation-title" is exactly "none", and so is that of its ".conversation-preview". 3. Right-click the row (click, button "right") and click the context menu's item "Rename". 4. The row's ".drawer-rename-input" has computed user-select exactly "text". 5. Select all its text; then eval `+drawerSelection+` must be greater than 0 (return the comparison, expecting true). 6. Press Escape. 7. Right-click the row and click the context menu's item "Edit tags". 8. The row's ".conversation-tag-inline-input" has computed user-select exactly "text". 9. Fill it with "selectable". 10. Select all its text; then eval `+drawerSelection+` must be exactly 10.`, func(s *shelley) {
		s.v("ID", s.conversation("selection test", "").ID)
	})
}

func TestE2EDrawerInterruptedPauseMark(t *testing.T) {
	drawerTest(t, useful(0), `The drawer shows a pause mark when a restart interrupted a conversation. `+drawerStubbed+`The list is "interrupted" (its turn was interrupted) and "running" (the agent is working). `+drawerSteps+`1. Navigate to /new. 2. In the interrupted row, ".drawer-interrupted-indicator" is visible, there is no ".drawer-working-indicator", and ".conversation-time" is visible. 3. A working row's dot stands in for its age: in the running row, ".drawer-working-indicator" is visible, and there is no ".conversation-time" and no ".drawer-interrupted-indicator".`, func(s *shelley) {
		s.stubDrawerList(
			drawerConversation("interrupted", false).with("turn_interrupted", true),
			drawerConversation("running", false).with("working", true, "agent_working", true),
		)
	})
}

// drawerBurstJS replaces EventSource with a stream the test drives, and
// window.__drawerBurst sends it 35 list patches in one frame burst.
const drawerBurstJS = `class TestEventSource {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSED = 2;
  constructor(url) {
    this.readyState = TestEventSource.CONNECTING;
    this.onopen = null;
    this.onmessage = null;
    this.onerror = null;
    window.__testStream = this;
    queueMicrotask(() => {
      this.readyState = TestEventSource.OPEN;
      this.onopen?.();
    });
  }
  close() {
    this.readyState = TestEventSource.CLOSED;
  }
}
Object.defineProperty(window, 'EventSource', { configurable: true, value: TestEventSource });
window.__drawerBurst = async () => {
  const events = Array.from({ length: 35 }, (_, index) => ({
    old_hash: index === 0 ? 'test-1' : 'h' + index,
    new_hash: 'h' + (index + 1),
    at: new Date().toISOString(),
    patch: [{ op: 'replace', path: '/0/preview', value: 'Preview ' + (index + 1) }],
  }));
  const stream = window.__testStream, perf = window.__shelleyPerf;
  if (!stream?.onmessage || !perf) throw new Error('stream/perf was not initialized');
  const frames = () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
  await frames();
  perf.reset();
  for (const event of events) {
    stream.onmessage({ data: JSON.stringify({ conversation_list_patch: event }) });
    await Promise.resolve();
  }
  await frames();
  const counters = perf.snapshot();
  window.__drawerBurstResult = { apply: counters['list.apply']?.count, topLevel: counters['drawer.topLevel']?.count };
  return window.__drawerBurstResult;
};`

func TestE2EDrawerBatchesPatchBurst(t *testing.T) {
	drawerTest(t, useful(0), `The drawer batches a frame-burst of conversation list patches before projecting the drawer. `+drawerStubbed+`The list is "burst-row". The page's live stream is a fake the test drives: eval "window.__drawerBurst()" sends it 35 list patches in one burst (the k-th sets the row's preview to "Preview k"), each in its own microtask, and resolves to the app's perf counters for it, {apply, topLevel} (how often the list applied a patch and the drawer re-projected its rows), also kept in window.__drawerBurstResult. `+drawerSteps+`1. Navigate to /new. 2. The row's ".conversation-preview" text is exactly "Preview". 3. Eval "window.__drawerBurst()" exactly once, with no expect. 4. The preview's text should become exactly "Preview 35". 5. Eval "JSON.stringify(window.__drawerBurstResult)" must be exactly {"apply":35,"topLevel":1}.`, func(s *shelley) {
		s.initScript(drawerBurstJS)
		s.stubDrawerList(drawerConversation("burst-row", false))
	})
}

func TestE2EDrawerRowSizes(t *testing.T) {
	drawerTest(t, useful(0), `View options sizes rows: short is the title, medium adds the preview. `+drawerStubbed+`The list is "tagged" (tagged, in a directory, with a commit) and "draft" (a draft). "The extra lines" of the tagged row are its elements matching ".conversation-tags, .conversation-meta, .conversation-git". A row's height is its getBoundingClientRect().height. The "Row size" group is {"role": "group", "name": "Row size", "exact": true} in the View options menu; scope its buttons with the selector [role="group"][aria-labelledby="drawer-row-size-heading"], the group labelled by #drawer-row-size-heading. `+drawerReload+drawerSteps+`1. Navigate to /new. 2. Full rows are the default, with every line: the tagged row's ".conversation-preview" text is exactly "Preview" and it has exactly 3 extra lines. 3. Record the tagged row's height in window.__fullHeight. 4. Click the button "View options". 5. The Row size group is visible, and #drawer-row-size-heading's trimmed text is exactly "Row size" (eval), so the selector scopes that group. 6. The group's button "Full" has aria-pressed "true". 7. Click the group's button "Medium". 8. The tagged row's preview text is exactly "Preview" and it has no extra lines (count 0). 9. A draft's delete moves up to its title line: the draft row's button "Delete" is visible. 10. The tagged row's height is less than window.__fullHeight; record it in window.__mediumHeight. 11. The menu stays open to compare sizes: click the group's button "Short". 12. The tagged row has no ".conversation-preview", its ".conversation-title" text is exactly "tagged", and the draft row's button "Delete" is visible. 13. The tagged row's height is less than window.__mediumHeight. 14. The choice is kept across reloads: reload. 15. The tagged row's ".conversation-title" is visible and it has no ".conversation-preview".`, func(s *shelley) {
		s.stubDrawerList(
			drawerConversation("tagged", false).tags("work").with("cwd", "/srv/app", "git_commit", "abc1234", "git_subject", "fix the thing"),
			drawerConversation("draft", true),
		)
	})
}

func TestE2EDrawerRunningTaskGear(t *testing.T) {
	drawerTest(t, useful(0), `The drawer shows a running-task gear count that opens the conversation. `+drawerStubbed+`The list is "parent", which has one background job running. `+drawerSteps+drawerDirect+`1. Navigate to /new. 2. The parent row's ".drawer-working-indicator" is visible. 3. The row's data-testid "conversation-running-work" element's trimmed text is exactly "1" (assert_text trims), its title is "1 task running", its aria-label "1 task running", and it has a visible svg inside. 4. The compact indicator is part of the ordinary row target, and no longer expands job logs in the drawer: click it. 5. The URL should become /c/parent (assert_url value "/c/parent"). 6. There is no element with data-testid "background-job".`, func(s *shelley) {
		s.stubDrawerList(drawerConversation("parent", false).with("running_background_jobs", 1))
	})
}

func TestE2EDrawerSubagentCounts(t *testing.T) {
	drawerTest(t, useful(0), `The drawer shows running and total subagent counts independently. `+drawerStubbed+`"active-parent" has 3 subagents: "active-child" (working, one background job), "waiting-child" (idle, two background jobs) and "active-idle-child". "idle-parent" has 2 idle subagents. Call "settle-active-child" makes active-child idle with no jobs, and call "settle-waiting-child" leaves waiting-child with no jobs, in the server's list. "The active badge" is "[data-conversation-id=\"active-parent\"] .subagent-count-badge" and "the idle badge" is "[data-conversation-id=\"idle-parent\"] .subagent-count-badge". `+drawerReload+drawerSteps+`1. Navigate to /new. 2. The active badge's text contains "2/3", and its data-testid "subagent-badge-running" element is visible. 3. The idle badge's trimmed text is exactly "2" and it has no data-testid "subagent-badge-running" element. 4. Call "settle-active-child", then reload. 5. The active badge's text should contain "1/3" and its data-testid "subagent-badge-running" element is visible. 6. Call "settle-waiting-child", then reload. 7. The active badge's trimmed text should be exactly "3", and it has no data-testid "subagent-badge-running" element.`, func(s *shelley) {
		activeChild := drawerConversation("active-child", false).with("parent_conversation_id", "active-parent", "working", true, "running_background_jobs", 1)
		waitingChild := drawerConversation("waiting-child", false).with("parent_conversation_id", "active-parent", "running_background_jobs", 2)
		l := s.stubDrawerList(
			drawerConversation("active-parent", false).with("subagent_count", 3),
			activeChild,
			waitingChild,
			drawerConversation("active-idle-child", false).with("parent_conversation_id", "active-parent"),
			drawerConversation("idle-parent", false).with("subagent_count", 2),
			drawerConversation("idle-child", false).with("parent_conversation_id", "idle-parent"),
			drawerConversation("second-idle-child", false).with("parent_conversation_id", "idle-parent"),
		)
		s.call("settle-active-child", func() string {
			l.update(func() { activeChild.with("working", false, "running_background_jobs", 0) })
			return "settled"
		})
		s.call("settle-waiting-child", func() string {
			l.update(func() { waitingChild.with("running_background_jobs", 0) })
			return "settled"
		})
	})
}

func TestE2EDrawerTruncatesLongSlugs(t *testing.T) {
	drawerTest(t, useful(0), `The drawer truncates long slugs to one line with an ellipsis. `+drawerStubbed+`The list is "{{LONG}}" and "short". "The long title" is "[data-conversation-id=\"{{LONG}}\"] .conversation-title" and "the short title" is "[data-conversation-id=\"short\"] .conversation-title". `+drawerSteps+`1. Navigate to /new. 2. The long title's text is exactly "{{LONG}}" and its title attribute is exactly "{{LONG}}". 3. It does not wrap: the long title's getBoundingClientRect().height equals exactly the short title's. 4. Overflowing content is clipped with an ellipsis rather than wrapped: for the long title, eval JSON.stringify({clipped: el.scrollWidth > el.clientWidth, textOverflow: getComputedStyle(el).textOverflow}) must be exactly {"clipped":true,"textOverflow":"ellipsis"}.`, func(s *shelley) {
		const long = "unexpected-conversation-navigation-redirect-with-an-even-longer-tail"
		s.v("LONG", long)
		s.stubDrawerList(drawerConversation(long, false), drawerConversation("short", false))
	})
}

func TestE2EDrawerCommitCopyIcon(t *testing.T) {
	drawerTest(t, useful(0), `The drawer keeps commit hashes behind a compact copy icon. `+drawerStubbed+`The list is "with-commit" (commit 424904d, subject "Make git graph file paths readable"), "hash-only" (commit abcdef1, no subject) and "no-commit". "The copy button" is the with-commit row's button "Copy commit hash 424904d". The clipboard may be read with eval "navigator.clipboard.readText()". `+drawerSteps+drawerDirect+`1. Navigate to /new. 2. The copy button is visible. 3. The with-commit row's ".drawer-git-info" text is exactly "Make git graph file paths readable". 4. The copy button contains exactly 3 "circle" elements, and its "path" element's d attribute is exactly "M6 16.6V7.4M6 16.6C8 11 12 6 14.6 5". 5. Record the copy button's getBoundingClientRect() (x, y, width, height) in window.__copyBox; its width is at most 24. 6. Record its computed color in window.__restingColor. 7. Hover the copy button. 8. The element with role "tooltip" should contain the text "Click to copy 424904d". 9. The copy button's computed color should come to differ from window.__restingColor (poll: the hover color can land after the tooltip). 10. Click the copy button. 11. The clipboard should come to hold exactly "424904d". 12. The copy button has the class "drawer-git-copy-copied", contains no "circle" elements, and is focused (document.activeElement). 13. Its bounding rect's x, y, width and height are exactly those in window.__copyBox. 14. The URL is still /new (assert_url value "/new"). 15. Focus the hash-only row's button "Copy commit hash abcdef1" (it is visible; eval its focus()) and press Enter. 16. The clipboard should come to hold exactly "abcdef1". 17. The URL is still /new (assert_url value "/new"). 18. The no-commit row has no ".drawer-git-info".`, func(s *shelley) {
		s.stubDrawerList(
			drawerConversation("with-commit", false).with("git_commit", "424904d", "git_subject", "Make git graph file paths readable"),
			drawerConversation("hash-only", false).with("git_commit", "abcdef1"),
			drawerConversation("no-commit", false),
		)
	})
}

func TestE2EDrawerSearchHighlights(t *testing.T) {
	drawerTest(t, useful(0), `Matching slug text is highlighted in drawer and command-palette searches. `+drawerStubbed+`The list is "Pelican-project-pelican" and "message-only", both tagged birds; searches answer with the list, and message-only's search snippet is "A pelican by the bay" with "pelican" marked. "The title" is "[data-conversation-id=\"Pelican-project-pelican\"] .conversation-title" and "the other title" is "[data-conversation-id=\"message-only\"] .conversation-title". Lists of texts are checked with assert_texts when a selector picks exactly those elements, else with an eval of JSON.stringify of their trimmed textContents. `+drawerQuery+drawerSteps+`1. Navigate to /new. 2. The title's text is exactly "Pelican-project-pelican" and it contains no "mark". 3. Click the button "Search conversations...". 4. Set the query to "PELICAN tag:birds ". 5. The title's marks are exactly ["Pelican","pelican"] and its text is still exactly "Pelican-project-pelican"; the other title has no "mark"; the ".conversation-snippet mark" elements are exactly ["pelican"]. 6. Set the query to "project". 7. The title's marks are exactly ["project"]. 8. Clear the query. 9. The title has no "mark". 10. Press "k" with modifiers "Control" to open the command palette. 11. Fill ".command-palette-input" with "PELICAN". 12. In the palette, the ".command-palette-item-title" whose text contains "Pelican-project-pelican" has marks exactly ["Pelican","pelican"] and text exactly "Pelican-project-pelican"; the ".command-palette-item-title" containing "message-only" has no "mark". 13. Fill the palette input with "conversation". 14. Of the ".command-palette-item" elements that contain a ".command-palette-item-badge" (actions), the first is visible, and none of them contains a "mark". 15. Fill the palette input with "" (empty). 16. No ".command-palette-item-title" containing "Pelican-project-pelican" contains a "mark".`, func(s *shelley) {
		s.stubDrawerList(
			drawerConversation("Pelican-project-pelican", false).tags("birds"),
			drawerConversation("message-only", false).tags("birds").with("search_snippet", "A \x02pelican\x03 by the bay"),
		)
	})
}

func TestE2EDrawerSearchRevealsChapters(t *testing.T) {
	drawerTest(t, useful(0), `Search reveals chapters and prioritizes chapter-title matches. `+drawerStubbed+`The list is, in this order, "message-only", "chapter-prefix", "chapter-only" (these three tagged birds) and "other-tag" (tagged other). Searches answer with message-only alone, whose search snippet is "A pelican by the bay" with "pelican" marked. The chapter index (/api/conversations/themes) gives chapter-only the chapters "Pelican" (message 12345678-abcd-1234-abcd-123456789abc) and "Another direction", chapter-prefix "Pelican research", message-only "Unrelated chapter" and other-tag "Pelican"; call "themes" says how many times the page has requested it. "The row" is [data-conversation-id="chapter-only"]; "outlines" are ".drawer .conversation-themes-list" elements; "the search button" is the button "Search conversations...". Lists of texts are checked with assert_texts when a selector picks exactly those elements, else with an eval of JSON.stringify of their trimmed textContents. `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. The row is visible. 3. There are no outlines, and call "themes" must return exactly "0" right now. 4. Click the search button. 5. There should be exactly 4 buttons "Show chapters". 6. There are no outlines, and call "themes" returns exactly "1". 7. Focus the row's ".drawer-chapters-toggle" (eval its focus()) and press Enter. 8. That toggle's aria-expanded is "true". 9. The texts of the row's ".conversation-themes-list a" are exactly ["Pelican","Another direction"]. 10. The URL is still /new (assert_url value "/new"). 11. Click the row's button "Hide chapters". 12. There are no outlines. 13. Set the query to "tag:birds ". 14. Click the row's button "Show chapters". 15. The row's ".conversation-themes-list" is visible. 16. Clear the query. 17. There are no outlines. 18. Set the query to "PELICAN tag:birds ". 19. The texts of ".drawer .conversation-title" are exactly ["chapter-only","chapter-prefix","message-only"]. 20. There are exactly 2 outlines, and the texts of the "mark" elements in outlines are exactly ["Pelican","Pelican"]. 21. The texts of "[data-conversation-id=\"chapter-only\"] .conversation-themes-list a" are exactly ["Pelican","Another direction"], and the first of those links has the href attribute exactly "/c/chapter-only#m-12345678". 22. The texts of ".conversation-snippet mark" are exactly ["pelican"]. 23. Call "themes" returns exactly "1". 24. Click the button "Select conversations". 25. There are no outlines and no ".drawer-chapters-toggle" elements. 26. Click the row. 27. The row's ".drawer-selection-check" checkbox is checked. 28. The URL is still /new (assert_url value "/new"). 29. Click the button "Cancel selection". 30. There are exactly 2 outlines and the row's ".conversation-themes-list" is visible. 31. Clear the query. 32. There are no outlines, and the row's button "Show chapters" has aria-expanded "false". 33. Click the search button (closing search). 34. There are no outlines. 35. Click the search button again. 36. There should be exactly 4 buttons "Show chapters". 37. There are no outlines, and call "themes" returns exactly "2".`, func(s *shelley) {
		l := s.stubDrawerList(
			drawerConversation("message-only", false).tags("birds").with("search_snippet", "A \x02pelican\x03 by the bay"),
			drawerConversation("chapter-prefix", false).tags("birds"),
			drawerConversation("chapter-only", false).tags("birds"),
			drawerConversation("other-tag", false).tags("other"),
		)
		l.search = drawerAnswer([]drawerConv{l.list()[0]})
		s.drawerThemes(map[string]any{
			"chapter-only": []map[string]string{
				{"title": "Pelican", "message_id": "12345678-abcd-1234-abcd-123456789abc"},
				{"title": "Another direction", "message_id": "other-message"},
			},
			"chapter-prefix": drawerChapter("Pelican research", "prefix-message"),
			"message-only":   drawerChapter("Unrelated chapter", "text-message"),
			"other-tag":      drawerChapter("Pelican", "excluded-message"),
		})
	})
}

// drawerCenters says whether the working row's indicator, chapters toggle
// and actions trigger have their horizontal centers 20px apart.
const drawerCenters = `(() => { const end = document.querySelector('[data-conversation-id="working-chapters"] .drawer-row-end'); const center = (selector) => { const box = end.querySelector(selector).getBoundingClientRect(); return box.x + box.width / 2; }; const c = [center('.drawer-working-indicator'), center('.drawer-chapters-toggle'), center('.drawer-actions-trigger')]; return Math.abs(c[1] - c[0] - 20) < 0.5 && Math.abs(c[2] - c[1] - 20) < 0.5; })()`

// drawerTimeEdges says whether the right edges of the idle, plain and draft
// rows' times line up to the half pixel.
const drawerTimeEdges = `(() => { const e = ['idle-chapters', 'no-chapters', 'draft-no-chapters'].map(id => { const r = document.querySelector('[data-conversation-id="' + id + '"] .conversation-time').getBoundingClientRect(); return r.x + r.width; }); return Math.abs(e[1] - e[0]) < 0.5 && Math.abs(e[2] - e[0]) < 0.5; })()`

func drawerChapterControls(t *testing.T, useful lazycue.Useful, width int) {
	steps := fmt.Sprintf(`Chapter controls stay aligned and compact at %dpx. The screen is already %dx800 (do not resize it). `, width, width) + drawerStubbed + `The list is "working-chapters" (the agent is working), "idle-chapters", "no-chapters" and "draft-no-chapters" (a draft). The chapter index gives working-chapters the chapter "Working topic" and idle-chapters "Idle topic". "The working row" is [data-conversation-id="working-chapters"]. ` + drawerSteps + drawerDirect + `1. Navigate to /new. `
	if width < 768 {
		steps += `2. Click the button "Open conversations". `
	}
	steps += `3. Click the button "Search conversations...". 4. There should be exactly 2 buttons "Show chapters". 5. The no-chapters and draft-no-chapters rows have no ".drawer-chapters-toggle". 6. The rows' times line up: eval ` + drawerTimeEdges + ` must be true. 7. The working row's button "Show chapters" has no aria-controls attribute. 8. Focus that button (eval its focus()), then eval "document.addEventListener('keydown', event => { document.body.dataset.chapterKey = event.key; }, { once: true })". 9. Press ArrowRight. 10. The body's data-chapter-key attribute should be exactly "ArrowRight". 11. The working row's indicator, chapters toggle and actions trigger sit 20px apart: eval ` + drawerCenters + ` must be true. 12. The working row's ".drawer-chapters-toggle" getBoundingClientRect().x is within 0.5px of that of the idle-chapters row's ".drawer-chapters-toggle". 13. Click the working row's button "Show chapters". 14. The working row's ".conversation-themes-list a" is exactly one link, with text exactly "Working topic". 15. The idle-chapters row has no ".conversation-themes-list". 16. The URL is still /new (assert_url value "/new").`
	drawerTest(t, useful, steps, func(s *shelley) {
		s.device(drawerDevice(width, 800))
		s.stubDrawerList(
			drawerConversation("working-chapters", false).with("working", true, "agent_working", true),
			drawerConversation("idle-chapters", false),
			drawerConversation("no-chapters", false),
			drawerConversation("draft-no-chapters", true),
		)
		s.drawerThemes(map[string]any{
			"working-chapters": drawerChapter("Working topic", "working-message"),
			"idle-chapters":    drawerChapter("Idle topic", "idle-message"),
		})
	})
}

func TestE2EDrawerChapterControls390(t *testing.T) {
	drawerChapterControls(t, useful(0), 390)
}

func TestE2EDrawerChapterControls1280(t *testing.T) {
	drawerChapterControls(t, useful(0), 1280)
}

func TestE2EDrawerChaptersCollapseOnStructuredClear(t *testing.T) {
	drawerTest(t, useful(0), `Chapter outlines collapse when clearing structured-only queries. `+drawerStubbed+`The list is "structured-chapters", written in by adam@poyo.co, which the chapter index gives the chapter "A topic". `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. Set the query to "is:untagged ". 4. Click the button "Show chapters". 5. ".drawer .conversation-themes-list" is visible. 6. Clear the query. 7. There is no ".drawer .conversation-themes-list". 8. Set the query to "user:adam@poyo.co ". 9. Click the button "Show chapters". 10. ".drawer .conversation-themes-list" is visible. 11. Clear the query. 12. There is no ".drawer .conversation-themes-list".`, func(s *shelley) {
		s.stubDrawerList(drawerConversation("structured-chapters", false).participants("adam@poyo.co"))
		s.drawerThemes(map[string]any{"structured-chapters": drawerChapter("A topic", "topic-message")})
	})
}

// drawerNoOverlap says whether the archived row's action buttons all end
// left of its chapters toggle.
const drawerNoOverlap = `(() => { const row = document.querySelector('[data-conversation-id="archived-chapter-controls"]'); const toggle = row.querySelector('.drawer-chapters-toggle').getBoundingClientRect(); const actions = row.querySelector('.drawer-actions-top'); const boxes = [actions, ...[...actions.querySelectorAll('button')].filter(b => b.checkVisibility({visibilityProperty: true}))].map(e => e.getBoundingClientRect()); return boxes.every(b => b.x + b.width <= toggle.x + 0.5); })()`

func TestE2EDrawerChapterControlsLeaveRoom(t *testing.T) {
	drawerTest(t, useful(0), `Chapter controls leave room for workspace restore and delete confirmation. The server has the workspace feature on. `+drawerStubbed+`The list is "active"; the archived list is "archived-chapter-controls" ("the row"), which the chapter index gives the chapter "Archived topic". "The actions" are the row's ".drawer-actions-top". "No overlap" means eval `+drawerNoOverlap+` returns true (the actions and each of their rendered buttons end left of the row's chapters toggle). `+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search tasks...". 3. Click data-testid "archived-toggle". 4. Hover the row. 5. The actions' button "Restore" is visible. 6. No overlap. 7. Click the actions' button "Delete". 8. The actions' ".drawer-delete-confirm" is visible. 9. No overlap. 10. Click the row's button "Show chapters". 11. The row's ".conversation-themes-list a" is exactly one link, with text exactly "Archived topic".`, func(s *shelley) {
		s.flag("workspace", true)
		s.stubDrawerList(drawerConversation("active", false))
		archived := drawerConversation("archived-chapter-controls", false).with("archived", true)
		s.route("GET /api/conversations/archived", drawerAnswer([]drawerConv{archived}))
		s.drawerThemes(map[string]any{"archived-chapter-controls": drawerChapter("Archived topic", "archived-message")})
	})
}

// drawerDeleteSettled waits for the archived row's Delete button to finish
// its hover fade, then says whether the pointer still rests on it: it is
// hovered and is (or holds) the element at its center.
const drawerDeleteSettled = `new Promise((resolve) => { const del = document.querySelector('[data-conversation-id="archived-delete"] .drawer-actions-top button[aria-label="Delete"]'); const check = () => { if (del.getAnimations().length) return requestAnimationFrame(check); const r = del.getBoundingClientRect(); resolve(del.matches(':hover') && del.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2))); }; check(); })`

// With motion allowed, Delete fades in to full opacity on hover, and the
// row's time, faded out in the same place, must not take the pointer from
// it then.
func TestE2EDrawerWorkspaceDeleteKeepsThePointer(t *testing.T) {
	drawerTest(t, useful(0), `A workspace row's Delete button keeps the pointer once its hover fade has finished, with motion allowed. The server has the workspace feature on. `+drawerStubbed+`The list is "active"; the archived list is "archived-delete" ("the row"). "The actions" are the row's ".drawer-actions-top". `+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search tasks...". 3. Click data-testid "archived-toggle". 4. Hover the row. 5. Hover the actions' button "Delete". 6. Eval `+drawerDeleteSettled+` with expect "true". 7. Click the actions' button "Delete". 8. The actions' ".drawer-delete-confirm" is visible.`, func(s *shelley) {
		s.media("prefers-reduced-motion", "no-preference")
		s.flag("workspace", true)
		s.stubDrawerList(drawerConversation("active", false))
		s.route("GET /api/conversations/archived", drawerAnswer([]drawerConv{drawerConversation("archived-delete", false).with("archived", true)}))
	})
}

func TestE2EDrawerChapterSearchArchiveView(t *testing.T) {
	drawerTest(t, useful(0), `Chapter search respects the current archive view. `+drawerStubbed+`The list is "active-chapter-hit"; the archived list is "archived-chapter-hit"; searches find nothing. The chapter index gives both the chapter "Needle". Titles are checked with assert_texts on ".drawer .conversation-title". `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. Set the query to "needle". 4. The titles should be exactly ["active-chapter-hit"]. 5. Click the button "View Archived". 6. The titles should be exactly ["archived-chapter-hit"]. 7. Click the button "Back to conversations". 8. The titles should be exactly ["active-chapter-hit"].`, func(s *shelley) {
		l := s.stubDrawerList(drawerConversation("active-chapter-hit", false))
		l.search = drawerAnswer([]drawerConv{})
		archived := drawerConversation("archived-chapter-hit", false).with("archived", true)
		s.route("GET /api/conversations/archived", drawerAnswer([]drawerConv{archived}))
		s.drawerThemes(map[string]any{
			"active-chapter-hit":   drawerChapter("Needle", "active-message"),
			"archived-chapter-hit": drawerChapter("Needle", "archived-message"),
		})
	})
}

func TestE2EDrawerChapterSearchByDefault(t *testing.T) {
	drawerTest(t, useful(0), `Chapter search is enabled by default. `+drawerStubbed+`The list is "chapter-only", and searches answer with it. The chapter index gives it the chapter "Needle"; call "themes" says how many times the page has requested the index. `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. Set the query to "needle". 4. The trimmed texts of ".drawer .conversation-title" should be exactly ["chapter-only"]. 5. ".drawer .conversation-themes-list a" is exactly one link, with text exactly "Needle". 6. Call "themes" returns exactly "1".`, func(s *shelley) {
		s.stubDrawerList(drawerConversation("chapter-only", false))
		s.drawerThemes(map[string]any{"chapter-only": drawerChapter("Needle", "message")})
	})
}

func TestE2EDrawerChapterSearchLateArchives(t *testing.T) {
	drawerTest(t, useful(0), `Chapter search includes late-loading archived tasks in compact workspace mode. The server has the workspace feature on. `+drawerStubbed+`The list is "active-chapter-hit"; searches find nothing; the archived list is "archived-chapter-hit", but the server's answer to it is held back until call "release-archives". The chapter index gives both the chapter "Needle". `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search tasks...". 3. Set the query to "needle". 4. [data-conversation-id="active-chapter-hit"] is visible. 5. There is no ".drawer .conversation-preview" (compact rows). 6. Click data-testid "archived-toggle". 7. The button "Ask Shelley" should be enabled. 8. Call "release-archives". 9. [data-conversation-id="archived-chapter-hit"] should become visible, and [data-conversation-id="active-chapter-hit"] is visible too.`, func(s *shelley) {
		s.flag("workspace", true)
		l := s.stubDrawerList(drawerConversation("active-chapter-hit", false))
		l.search = drawerAnswer([]drawerConv{})
		archived := drawerConversation("archived-chapter-hit", false).with("archived", true)
		gate := s.gate("release-archives")
		s.route("GET /api/conversations/archived", func(w http.ResponseWriter, r *http.Request) {
			if gate.wait(r) {
				writeJSON(w, []drawerConv{archived})
			}
		})
		s.drawerThemes(map[string]any{
			"active-chapter-hit":   drawerChapter("Needle", "active-message"),
			"archived-chapter-hit": drawerChapter("Needle", "archived-message"),
		})
	})
}

func TestE2EDrawerChapterSearchRefreshes(t *testing.T) {
	drawerTest(t, useful(0), `Chapter search refreshes recorded chapters and surfaces index failures. `+drawerStubbed+`The list is "chapter-hit", and searches answer with it. The chapter index gives it one chapter, at first "First idea"; call "rename-chapter" renames it "Changed direction"; call "fail-index" makes the index answer HTTP 500 "Index failed", and call "heal-index" makes it answer again. "The link" is ".drawer .conversation-themes-list a". To "announce new chapters", eval "window.dispatchEvent(new CustomEvent('shelley-chapters-updated'))". `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. Click the button "Show chapters". 4. The link's text should be exactly "First idea". 5. Call "rename-chapter", then announce new chapters. 6. The link's text should become exactly "Changed direction". 7. Call "fail-index", then announce new chapters. 8. The element with role "alert" should contain the text "Failed to load chapters". 9. Call "heal-index". 10. Set the query to "changed". 11. The link's text should be exactly "Changed direction". 12. There should be no element with role "alert".`, func(s *shelley) {
		s.stubDrawerList(drawerConversation("chapter-hit", false))
		var mu sync.Mutex
		title, fail := "First idea", false
		s.route("/api/conversations/themes", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if fail {
				http.Error(w, "Index failed", http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"chapter-hit": drawerChapter(title, "message")})
		})
		set := func(name string, f func()) {
			s.call(name, func() string {
				mu.Lock()
				defer mu.Unlock()
				f()
				return "done"
			})
		}
		set("rename-chapter", func() { title = "Changed direction" })
		set("fail-index", func() { fail = true })
		set("heal-index", func() { fail = false })
	})
}

func TestE2EDrawerChapterSearchRefreshWhileLoading(t *testing.T) {
	drawerTest(t, useful(0), `Chapter search refreshes chapter updates recorded while the index is loading. `+drawerStubbed+`The list is "chapter-hit"; searches find nothing. The chapter index's first answer gives chapter-hit the chapter "Old direction" but is held back until call "release-index"; later answers give it "Needle". Call "themes" says how many times the page has requested the index. `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. Call "themes" until it returns "1" (the first index request has been made). 4. Set the query to "needle". 5. Eval "window.dispatchEvent(new CustomEvent('shelley-chapters-updated'))". 6. Call "release-index". 7. The trimmed texts of ".drawer .conversation-title" should be exactly ["chapter-hit"]. 8. ".drawer .conversation-themes-list a" should be exactly one link, with text exactly "Needle". 9. Call "themes" returns exactly "2".`, func(s *shelley) {
		l := s.stubDrawerList(drawerConversation("chapter-hit", false))
		l.search = drawerAnswer([]drawerConv{})
		gate, n := s.gate("release-index"), s.counter("themes")
		s.route("/api/conversations/themes", func(w http.ResponseWriter, r *http.Request) {
			title := "Needle"
			if n.Add(1) == 1 {
				title = "Old direction"
				if !gate.wait(r) {
					return
				}
			}
			writeJSON(w, map[string]any{"chapter-hit": drawerChapter(title, "message")})
		})
	})
}

func TestE2EDrawerClearsStaleMatches(t *testing.T) {
	drawerTest(t, useful(0), `The drawer clears stale matches while the next query is pending. `+drawerStubbed+`The list is "recent". Searches answer with "needle-match", but the answer to a search for "pending" is held back until call "release". `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. Set the query to "needle". 4. [data-conversation-id="needle-match"] should be visible. 5. Set the query to "pending". 6. There should be no [data-conversation-id="needle-match"]. 7. An element whose whole text is exactly "Searching..." is visible (selector "*" with has_text "Searching..." and exact). 8. Call "release". 9. Clear the query.`, func(s *shelley) {
		l := s.stubDrawerList(drawerConversation("recent", false))
		gate := s.gate("release")
		l.search = func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("q") == "pending" && !gate.wait(r) {
				return
			}
			writeJSON(w, []drawerConv{drawerConversation("needle-match", false)})
		}
	})
}

func TestE2EDrawerAskShelley(t *testing.T) {
	drawerTest(t, useful(0), `Ask Shelley waits for search results and immediately closes the mobile drawer. The screen is 390x844. `+drawerStubbed+`The list is "recent". Searches answer with "needle-match" (search snippet "Supporting passage" from message source-message), but are held back until call "release-results". Asking Shelley (POST /api/conversations/search/enhance) answers 202 with a new search conversation "ask-shelley-fixture", held back until call "release-chat"; call "payload" returns the JSON body the page posted there (keys sorted), or "" if none yet. `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Open conversations". 3. Click the button "Search conversations...". 4. Set the query to "needle". 5. The button "Ask Shelley" is disabled. 6. Call "release-results". 7. The button "Ask Shelley" should become enabled. 8. Click it. 9. The drawer (".drawer") should not have the class "open". 10. The URL is still /new (assert_url value "/new"). 11. Call "release-chat". 12. The URL should become /c/ask-shelley-fixture (assert_url value "/c/ask-shelley-fixture"). 13. There is no element with role dialog. 14. Call "payload" must return exactly {"archived":false,"matches":[{"conversation_id":"needle-match","message_id":"source-message","slug":"needle-match","snippet":"Supporting passage"}],"q":"needle"}.`, func(s *shelley) {
		s.device(drawerDevice(390, 844))
		match := drawerConversation("needle-match", false).with("search_snippet", "Supporting passage", "search_snippet_message_id", "source-message")
		chat := drawerConversation("ask-shelley-fixture", false).with("conversation_options", mustJSON(map[string]string{"kind": "search"}))
		l := s.stubDrawerList(drawerConversation("recent", false))
		results, chatGate := s.gate("release-results"), s.gate("release-chat")
		l.search = func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery == "" {
				writeJSON(w, l.list())
				return
			}
			if results.wait(r) {
				writeJSON(w, []drawerConv{match})
			}
		}
		var mu sync.Mutex
		payload := ""
		s.route("/api/conversations/search/enhance", func(w http.ResponseWriter, r *http.Request) {
			var body any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			payload = mustJSON(body)
			mu.Unlock()
			if chatGate.wait(r) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(map[string]any{"conversation": chat})
			}
		})
		fixture := drawerAnswer(map[string]any{"conversation": chat, "messages": []any{}})
		s.route("/api/conversation/ask-shelley-fixture", fixture)
		s.route("/api/conversation/ask-shelley-fixture/", fixture)
		s.call("payload", func() string {
			mu.Lock()
			defer mu.Unlock()
			return payload
		})
	})
}

func TestE2EDrawerSearchesArchiveView(t *testing.T) {
	drawerTest(t, useful(0), `The drawer searches only the current archive view. `+drawerStubbed+`The list is "recent"; the archived list is "needle-archived". Searches answer with "needle-archived" when their archived parameter is "true" and with "needle-active" otherwise; call "scopes" lists, as JSON, whether each search so far was of the archive. Titles are checked with assert_texts on ".drawer .conversation-title". `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. Set the query to "needle". 4. The titles should be exactly ["needle-active"]. 5. Click the button "View Archived". 6. The titles should be exactly ["needle-archived"]. 7. The editor's trimmed text is exactly "needle". 8. Click the button "Back to conversations". 9. The titles should be exactly ["needle-active"]. 10. Call "scopes" must return exactly [false,true,false].`, func(s *shelley) {
		active := drawerConversation("needle-active", false)
		archived := drawerConversation("needle-archived", false).with("archived", true)
		l := s.stubDrawerList(drawerConversation("recent", false))
		s.route("GET /api/conversations/archived", drawerAnswer([]drawerConv{archived}))
		var mu sync.Mutex
		scopes := []bool{}
		l.search = func(w http.ResponseWriter, r *http.Request) {
			archive := r.URL.Query().Get("archived") == "true"
			mu.Lock()
			scopes = append(scopes, archive)
			mu.Unlock()
			if archive {
				writeJSON(w, []drawerConv{archived})
			} else {
				writeJSON(w, []drawerConv{active})
			}
		}
		s.call("scopes", func() string {
			mu.Lock()
			defer mu.Unlock()
			return mustJSON(scopes)
		})
	})
}

func TestE2EDrawerCancelsSupersededSearches(t *testing.T) {
	drawerTest(t, useful(0), `The drawer cancels superseded and cleared searches. `+drawerStubbed+`The list is "recent". Searches answer with "needle-active", except that searches for "slow" and "cleared" never answer. Call "searches" lists the q of every search so far, as JSON; calls "slow-requested" and "cleared-requested" say ("true"/"false") whether such a search was made, and "slow-cancelled" and "cleared-cancelled" whether the browser then cancelled it. `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. Set the query to "slow". 4. Call "slow-requested" until it returns "true". 5. Set the query to "needle". 6. Call "slow-cancelled" until it returns "true". 7. [data-conversation-id="needle-active"] should be visible. 8. Set the query to "cleared". 9. Call "cleared-requested" until it returns "true". 10. Clear the query. 11. Call "cleared-cancelled" until it returns "true". 12. [data-conversation-id="recent"] should be visible and there should be no [data-conversation-id="needle-active"]. 13. Call "searches" must return exactly ["slow","needle","cleared"].`, func(s *shelley) {
		l := s.stubDrawerList(drawerConversation("recent", false))
		s.drawerSearchLog(l, []drawerConv{drawerConversation("needle-active", false)}, "slow", "cleared")
	})
}

func TestE2EDrawerPaletteIndexedSearch(t *testing.T) {
	drawerTest(t, useful(0), `The command palette uses indexed search and cancels superseded requests. `+drawerStubbed+`The list is "recent". Indexed searches answer with "needle-active" and "message-only", except that searches for "slow" and "cleared" never answer. Call "searches" lists the q of every indexed search so far, as JSON; calls "slow-requested" and "cleared-requested" say ("true"/"false") whether such a search was made, and "slow-cancelled" and "cleared-cancelled" whether the browser then cancelled it. Call "legacy" lists, as JSON, the URLs of every request for the legacy search (/api/conversations?...). "The input" is ".command-palette-input"; "titles" are ".command-palette-item-title" elements. `+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Press "k" with modifiers "Control" to open the command palette. 3. A title containing "recent" should be visible. 4. Fill the input with "slow". 5. Call "slow-requested" until it returns "true". 6. Fill the input with " needle " (spaces included). 7. Call "slow-cancelled" until it returns "true". 8. The trimmed texts of the titles should be exactly ["needle-active","message-only"]. 9. The trimmed texts of the "mark" elements in the first title are exactly ["needle"]. 10. Fill the input with "cleared". 11. Call "cleared-requested" until it returns "true". 12. Fill the input with "" (empty). 13. Call "cleared-cancelled" until it returns "true". 14. A title containing "recent" should be visible, and no title contains "needle-active". 15. Call "searches" must return exactly ["slow","needle","cleared"], and call "legacy" exactly [].`, func(s *shelley) {
		l := s.stubDrawerList(drawerConversation("recent", false))
		s.drawerSearchLog(l, []drawerConv{drawerConversation("needle-active", false), drawerConversation("message-only", false)}, "slow", "cleared")
		var mu sync.Mutex
		legacy := []string{}
		s.route("/api/conversations", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery == "" {
				s.pass.ServeHTTP(w, r)
				return
			}
			mu.Lock()
			legacy = append(legacy, r.URL.String())
			mu.Unlock()
			writeJSON(w, []any{})
		})
		s.call("legacy", func() string {
			mu.Lock()
			defer mu.Unlock()
			return mustJSON(legacy)
		})
	})
}

// drawerVisibleIDs lists the IDs of the rendered conversation rows, in
// order, as Playwright's :visible has it.
const drawerVisibleIDs = `JSON.stringify([...document.querySelectorAll('[data-conversation-id]')].filter(e => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && e.checkVisibility({visibilityProperty: true}); }).map(e => e.getAttribute('data-conversation-id')))`

// drawerMe has the server see the browser's requests as me@example.com's.
func drawerMe(s *shelley) {
	s.header("X-ExeDev-Email", "me@example.com")
}

const drawerAsMe = `The browser's requests carry the header X-ExeDev-Email: me@example.com, so the current user is me@example.com. `

func TestE2EDrawerSingleUserNoParticipantFilter(t *testing.T) {
	drawerTest(t, useful(0), `Single-user lists have no participant filter. `+drawerAsMe+drawerStubbed+`The list is "mine" (written in by me@example.com alone), its subagent "subagent" (written in by other@example.com), and "draft" (a draft). "The search toggle" is the button "Search conversations...". `+drawerSteps+`1. Navigate to /new. 2. There is no ".drawer-search-input". 3. The search toggle does not have the class "search-toggle-active". 4. There is no ".conversation-participant-badge". 5. [data-conversation-id="mine"] and [data-conversation-id="draft"] are visible. 6. Click the search toggle. 7. There is no button "Add user filter".`, func(s *shelley) {
		drawerMe(s)
		s.stubDrawerList(
			drawerConversation("mine", false).participants("me@example.com").with("subagent_count", 1),
			drawerConversation("subagent", false).participants("other@example.com").with("parent_conversation_id", "mine"),
			drawerConversation("draft", true),
		)
	})
}

func TestE2EDrawerMultiUserSeedsQuery(t *testing.T) {
	drawerTest(t, useful(0), `Multi-user lists seed the current-user query once. `+drawerAsMe+drawerStubbed+`The list is "mine" (participants collaborator@example.com with 2 messages and me@example.com with 3), "triple" (me@example.com, collaborator@example.com and third@example.com), "other" (other@example.com) and "draft" (a draft, which has no participants). "The search toggle" is the button "Search conversations..."; "the user panel" is data-testid "user-filter-panel", whose options are its elements with role option (a "selector" scoping a "role"; the option containing X is the one whose name contains X, without exact); "the tooltip" is ".p-tooltip". The visible rows are listed by eval `+drawerVisibleIDs+`. `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. There is no editor. 3. The search toggle has the class "search-toggle-active". 4. [data-conversation-id="mine"] is visible. 5. A draft belongs to whoever is typing it and survives the seeded current-user filter: [data-conversation-id="draft"] is visible. 6. [data-conversation-id="other"] is not visible. 7. The mine row's ".conversation-participant-badge" contains the text "2" and holds exactly one ".conversation-participant-badge-front-filled". 8. Click that badge. 9. The tooltip should contain "collaborator@example.com"; ".p-tooltip strong" has text exactly "me@example.com"; there is exactly 1 ".p-tooltip .conversation-participant-tooltip-identity-current" and exactly 2 ".p-tooltip .conversation-participant-tooltip-identity". 10. Of the ".p-tooltip .conversation-participant-tooltip-row" elements, the first contains "me@example.com" and the second "collaborator@example.com"; the row containing "collaborator@example.com" has no "strong"; the row containing "me@example.com" has a ".conversation-participant-tooltip-count" with text exactly "3". 11. Click the search toggle. 12. The editor's autocapitalize attribute is "none" and its contenteditable attribute is "true". 13. The query is "user:me@example.com ", and the token user:me@example.com is visible. 14. No element has the exact text "@ Participating". 15. Click the button "Add user filter"; the query should be "user:me@example.com user:". 16. Click the button "Add tag filter"; the query should be "user:me@example.com tag:". 17. Click the button "Add user filter"; the query should be "user:me@example.com user:". 18. Press Escape. 19. Record the visible rows in window.__defaultOrder (eval "window.__defaultOrder = " followed by that expression). 20. Click the button "Add user filter". 21. No option of the user panel contains "other@example.com". 22. Click the user panel's option containing "third@example.com". 23. The query should be "user:me@example.com user:third@example.com "; there are exactly 2 user tokens; the token user:third@example.com is visible. 24. Set the query to "user:me@example.com ". 25. The visible rows should be exactly window.__defaultOrder again (poll an eval comparing the two). 26. Click the search toggle. 27. There is no editor; the search toggle does not have the class "search-toggle-active"; [data-conversation-id="other"] is visible. 28. Click the search toggle. 29. The query is "". 30. Clear the query. 31. [data-conversation-id="other"] and [data-conversation-id="draft"] are visible. 32. The other row's button whose accessible name contains "other@example.com" contains the text "1". 33. There are no user tokens. 34. Click the button "Add user filter"; the query should be "user:" and the user panel visible. 35. Click empty header space, left of the actions, to dismiss the panel: click ".drawer-header" at x 24, y 24. 36. There should be no user panel. 37. Click the button "Add user filter"; the user panel is visible. 38. Click the user panel's option containing "me@example.com". 39. The query should be "user:me@example.com "; [data-conversation-id="other"] is not visible. 40. Clear the query. 41. Click the search toggle. 42. There is no editor, and the search toggle does not have the class "search-toggle-active".`, func(s *shelley) {
		drawerMe(s)
		mine := drawerConversation("mine", false)
		mine["participants"] = []map[string]any{
			{"email": "collaborator@example.com", "message_count": 2},
			{"email": "me@example.com", "message_count": 3},
		}
		s.stubDrawerList(
			mine,
			drawerConversation("triple", false).participants("me@example.com", "collaborator@example.com", "third@example.com"),
			drawerConversation("other", false).participants("other@example.com"),
			drawerConversation("draft", true),
		)
	})
}

func drawerWorkspaceSearchExit(t *testing.T, useful lazycue.Useful, exit string) {
	how := `click the search toggle again`
	if exit == "Escape" {
		how = `focus the query editor (data-testid "conversation-query-editor"; eval its focus()) and press Escape`
	}
	drawerTest(t, useful, fmt.Sprintf(`Closing workspace search with %s resets the Archived filter. The server has the workspace feature on. `, exit)+drawerStubbed+`The list is "active"; the archived list is "archived". "The search toggle" is the button "Search tasks...". `+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the search toggle. 3. Click data-testid "archived-toggle". 4. [data-conversation-id="archived"] should be visible. 5. Close search: `+how+`. 6. There should be no data-testid "conversation-query-editor"; the search toggle does not have the class "search-toggle-active"; there is no [data-conversation-id="archived"]; [data-conversation-id="active"] is visible. 7. Click the search toggle. 8. Data-testid "archived-toggle" has aria-pressed "false".`, func(s *shelley) {
		s.flag("workspace", true)
		s.stubDrawerList(drawerConversation("active", false))
		s.route("GET /api/conversations/archived", drawerAnswer([]drawerConv{drawerConversation("archived", false).with("archived", true)}))
	})
}

func TestE2EDrawerWorkspaceSearchToggleResetsArchived(t *testing.T) {
	drawerWorkspaceSearchExit(t, useful(0), "toggle")
}

func TestE2EDrawerWorkspaceSearchEscapeResetsArchived(t *testing.T) {
	drawerWorkspaceSearchExit(t, useful(0), "Escape")
}

func drawerSearchExit(t *testing.T, useful lazycue.Useful, exit string) {
	how, query := `click the search toggle again`, "user:me@example.com tag:alpha is:unread needle"
	if exit == "Escape" {
		how, query = `focus the editor (eval its focus()) and press Escape`, "user:me@example.com tag:alpha is:unread "
	}
	drawerTest(t, useful, fmt.Sprintf(`Closing search with %s resets the query and all search filters. `, exit)+drawerAsMe+drawerStubbed+`The list is "mine" (written in by me@example.com, tagged alpha, unread) and "other" (written in by other@example.com). "The search toggle" is the button "Search conversations...". `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the search toggle. 3. Click the button "Activity". 4. Click the button "1d+" inside ".drawer-bulk-popover". 5. [data-conversation-query-token="activity"] should be visible. 6. Set the query to "`+query+`". 7. [data-conversation-id="other"] is not visible. 8. Close search: `+how+`. 9. There should be no editor; the search toggle does not have the class "search-toggle-active"; [data-conversation-id="mine"] and [data-conversation-id="other"] are visible. 10. Click the search toggle. 11. The query is "". 12. There is no [data-conversation-query-token="activity"]. 13. Data-testid "unread-toggle" has aria-pressed "false".`, func(s *shelley) {
		drawerMe(s)
		s.stubDrawerList(
			drawerConversation("mine", false).participants("me@example.com").tags("alpha").with("unread", true),
			drawerConversation("other", false).participants("other@example.com"),
		)
	})
}

func TestE2EDrawerSearchToggleResetsFilters(t *testing.T) {
	drawerSearchExit(t, useful(0), "toggle")
}

func TestE2EDrawerSearchEscapeResetsFilters(t *testing.T) {
	drawerSearchExit(t, useful(0), "Escape")
}

func TestE2EDrawerUserPickerExhausted(t *testing.T) {
	drawerTest(t, useful(0), `The user picker disables exhausted options and reports no matches. `+drawerAsMe+drawerStubbed+`The list is "shared", written in by me@example.com and other@example.com. "The user panel" is data-testid "user-filter-panel", whose options are its elements with role option (a "selector" scoping a "role"; the option containing X is the one whose name contains X, without exact). `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. Click the button "Add user filter". 4. The query should be "user:me@example.com user:". 5. Eval the editor's focus() (it keeps the caret where it is) and type "missing" key by key. 6. The query should be "user:me@example.com user:missing". 7. The text "No matching users" inside the user panel is visible. 8. Set the query to "user:me@example.com ". 9. Click the button "Add user filter". 10. Click the user panel's option containing "other@example.com". 11. The button "Add user filter" should be disabled. 12. Hover that button's parent element. 13. ".p-tooltip" should contain the text "All users added".`, func(s *shelley) {
		drawerMe(s)
		s.stubDrawerList(drawerConversation("shared", false).participants("me@example.com", "other@example.com"))
	})
}

// drawerTokenLayout says whether both tag tokens are single lines, their
// prefix and value side by side, that never wrap.
const drawerTokenLayout = `(() => { const tokens = [...document.querySelectorAll('[data-testid="conversation-query-editor"] [data-conversation-query-token="tag"]')]; return tokens.length === 2 && tokens.every(token => { const prefix = token.querySelector('.conversation-query-token-prefix'); const value = token.querySelector('.conversation-query-token-value'); return !!prefix && !!value && token.getBoundingClientRect().height < 30 && prefix.getBoundingClientRect().top === value.getBoundingClientRect().top && getComputedStyle(token).whiteSpace === 'nowrap'; }); })()`

// drawerTokenPreamble introduces the token query editing tests.
const drawerTokenPreamble = drawerAsMe + drawerStubbed + `The list is "mine" (written in by me@example.com and other@example.com; tagged alpha, "in progress" and alpine), "other" (other@example.com) and "alpine" (third@example.com; tagged alpine). "The tag panel" is data-testid "tag-filter-panel"; its entries are [data-tag="<tag>"]. "The user panel" is data-testid "user-filter-panel". "The token T" (the token whose text contains T) is the element [data-query-raw="T"] inside the editor (for example [data-testid="conversation-query-editor"] [data-query-raw="tag:alpha"]); "tag tokens" are [data-testid="conversation-query-editor"] [data-conversation-query-token="tag"]. "Click the token T" means click the ".conversation-query-token-value" inside the token T. "Press the clear button" means click the ".drawer-search-clear" button (never clear with the keyboard here). The clipboard may be read with eval "navigator.clipboard.readText()". Type with the type action (key by key) and press keys with press_key, both into the focused element. ` + drawerQuery + drawerSteps + drawerDirect

func TestE2EDrawerTokenQueryEditing(t *testing.T) {
	// The Playwright test is one long scenario; each part below is independent
	// (every later section starts by clearing the query), split so that each
	// script fits in one agent run.
	setup := func(s *shelley) {
		drawerMe(s)
		s.stubDrawerList(
			drawerConversation("mine", false).participants("me@example.com", "other@example.com").tags("alpha", "in progress", "alpine"),
			drawerConversation("other", false).participants("other@example.com"),
			drawerConversation("alpine", false).participants("third@example.com").tags("alpine"),
		)
	}
	t.Run("CompletesAndDeletes", func(t *testing.T) {
		drawerTest(t, useful(0), `Token query editing completes tokens, copies, deletes and places the caret. `+drawerTokenPreamble+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. Eval "document.querySelector('.drawer').style.width = '600px'". 4. The editor's contenteditable attribute is "true" and its autocapitalize is "none"; it holds exactly one "p" element; the token user:me@example.com is visible. 5. Typing a structured cue opens autocomplete, and clicking replaces that exact partial with a token and one ordinary trailing space: click the token user:me@example.com, then type "tag:al". 6. The tag panel and its entry [data-tag="alpha"] should be visible. 7. Click [data-tag="alpha"] in the tag panel. 8. The query should be "user:me@example.com tag:alpha " and the token tag:alpha visible. 9. A second typed cue creates a second, independent token: type "tag:alpi". 10. The tag panel's [data-tag="alpine"] should be visible; click it. 11. The query should be "user:me@example.com tag:alpha tag:alpine " and there are exactly 2 tag tokens. 12. Free text after tokens stays outside them: type "notes". 13. The query should be "user:me@example.com tag:alpha tag:alpine notes". 14. Press Escape. 15. The query should be "user:me@example.com tag:alpha tag:alpine ". 16. Copying copies the serialized query, not the delete buttons: eval the editor's focus(), press "a" with modifiers "Control", then press "c" with modifiers "Control". 17. The clipboard should come to hold exactly "user:me@example.com tag:alpha tag:alpine ". 18. Press ArrowRight. 19. The delete button removes one token and its separator: click the button "Remove tag:alpine" inside the token tag:alpine. 20. The query should be "user:me@example.com tag:alpha " and there is exactly 1 tag token. 21. Re-add it: click the token tag:alpha, type "tag:alpi", and click the tag panel's [data-tag="alpine"]. 22. A click on a token's body puts the caret after its separator: click the token tag:alpine, then type "Z". 23. The query should be "user:me@example.com tag:alpha tag:alpine Z". 24. Press Backspace. 25. The query should be "user:me@example.com tag:alpha tag:alpine ".`, setup)
	})
	t.Run("BackspaceAndPaste", func(t *testing.T) {
		drawerTest(t, useful(0), `Token query editing handles plain text, pasted line breaks and Backspace around tokens. `+drawerTokenPreamble+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. Eval "document.querySelector('.drawer').style.width = '600px'". 4. Plain structured-looking text is not a token, so Backspace removes only its trailing separator: press the clear button, click the editor, type "tag:missing " (with the trailing space), then press Backspace. 5. The query should be "tag:missing" and there are no tag tokens. 6. Pasted line breaks become separators without a second paragraph: press the clear button, click the editor, eval "navigator.clipboard.writeText('alpha\\nbeta')" (a line break between the words), then press "v" with modifiers "Control". 7. The query should be "alpha beta" and the editor holds exactly one "p". 8. Extra spaces are deleted normally before Backspace reaches a token: press the clear button; click the button "Add user filter" and the user panel's option containing "me@example.com"; click the button "Add tag filter" and the tag panel's [data-tag="alpha"]; click the button "Add tag filter" and the tag panel's [data-tag="alpine"]. 9. The query should be "user:me@example.com tag:alpha tag:alpine ". 10. Press Space right away (without clicking or moving the caret first). 11. The query should be "user:me@example.com tag:alpha tag:alpine  " (two trailing spaces). 12. Press Backspace. 13. The query should be "user:me@example.com tag:alpha tag:alpine ". 14. Removing the first of adjacent tokens keeps an insertion slot before the second: click the token tag:alpha and press Backspace. 15. The query should be "user:me@example.com  tag:alpine " (two spaces after the user token), and there is no token tag:alpha. 16. Type "Z". 17. The query should be "user:me@example.com Z tag:alpine ". 18. Press Backspace. 19. The query should be "user:me@example.com  tag:alpine ".`, setup)
	})
	t.Run("SpaceCompletionAndWrap", func(t *testing.T) {
		drawerTest(t, useful(0), `Token query editing completes on Space, keeps quoted spaces and wraps whole tokens. `+drawerTokenPreamble+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. Eval "document.querySelector('.drawer').style.width = '600px'". 4. Space completion also makes a token and leaves typing outside it: press the clear button, click the editor, type "tag:alpi". 5. The tag panel's [data-tag="alpine"] should be visible. 6. Press Space. 7. The query should be "tag:alpine " and the token tag:alpine visible. 8. Type "followup". 9. The query should be "tag:alpine followup". 10. A space inside an open quoted tag stays literal and keeps autocomplete: press the clear button, click the editor, type 'tag:"in' (with the double quote). 11. The tag panel's [data-tag="in progress"] should be visible. 12. Press Space. 13. The query should be 'tag:"in ' (ending in a space), and the tag panel itself (data-testid "tag-filter-panel") is visible. 14. Tokens wrap only as whole units: press the clear button; click the button "Add tag filter" and the tag panel's [data-tag="in progress"]; click the button "Add tag filter" and the tag panel's [data-tag="alpha"]. 15. Eval "document.querySelector('.drawer').style.width = '250px'". 16. Eval `+drawerTokenLayout+` must be true (exactly two tag tokens, each under 30px tall, its prefix and value tops equal, white-space nowrap).`, setup)
	})
}

func TestE2EDrawerFiltersKeepSubagents(t *testing.T) {
	drawerTest(t, useful(0), `User and tag filters never hide subagent chats. `+drawerAsMe+drawerStubbed+`The list is "parent" (written in by me@example.com and other@example.com, tagged keep) and its subagent "subagent" (written in by third@example.com, tagged different). "The subagent row" is the ".subagent-item" whose text contains "subagent". `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. [data-conversation-id="parent"] is visible. 3. Click the button "Expand subagents". 4. The subagent row is visible. 5. Subagent rows are keyboard-reachable links, like their parents: the subagent row's role attribute is "link" and its tabindex "0". 6. Click the button "Search conversations...". 7. Click the button "Add tag filter". 8. Click [data-tag="keep"] in data-testid "tag-filter-panel". 9. The query should be "user:me@example.com tag:keep ". 10. The subagent row is visible.`, func(s *shelley) {
		drawerMe(s)
		s.stubDrawerList(
			drawerConversation("parent", false).participants("me@example.com", "other@example.com").tags("keep").with("subagent_count", 1),
			drawerConversation("subagent", false).participants("third@example.com").tags("different").with("parent_conversation_id", "parent"),
		)
	})
}

// drawerErrorsJS collects the page's uncaught errors and console errors
// (Vue reports a throwing computed with console.error) in
// window.__drawerErrors.
const drawerErrorsJS = `window.__drawerErrors = [];
window.addEventListener('error', (e) => window.__drawerErrors.push(String(e.message)));
window.addEventListener('unhandledrejection', (e) => window.__drawerErrors.push(String(e.reason)));
const consoleError = console.error;
console.error = function (...args) {
  window.__drawerErrors.push(args.map(String).join(' '));
  return consoleError.apply(this, args);
};`

func TestE2EDrawerBackspacePillBeforePartial(t *testing.T) {
	drawerTest(t, useful(0), `Backspace on a pill ahead of a partial term keeps query and list in sync. `+drawerAsMe+drawerStubbed+`The list is "mine" (written in by me@example.com and other@example.com, tagged alpha), "other" (other@example.com) and "alpine" (third@example.com, tagged alpine). "The tag panel" is data-testid "tag-filter-panel"; its entries are [data-tag="<tag>"]. "The count" is ".drawer-filter-result-count". "The user pill" is the ".conversation-query-token-value" inside the token user:me@example.com. window.__drawerErrors lists the page's uncaught errors and console.error messages. `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. The query should be "user:me@example.com ". 4. The count's text should be exactly "2" (the new conversation's draft row passes the participant scope too). 5. A trailing partial tag: click the user pill, then type "tag:al" key by key. 6. The query should be "user:me@example.com tag:al"; the tag panel's [data-tag="alpha"] is visible and it has no [data-tag="alpine"]; the count's text is exactly "2". 7. Click the user pill again, which puts the caret right after its separator. 8. Press Backspace. 9. Removing the pill shifts the partial left and the edit follows it: the query should be " tag:al" (with the leading space); there are no user tokens; the tag panel's [data-tag="alpha"] and [data-tag="alpine"] are both visible (the participant scope is gone). 10. Press Escape. 11. There should be no tag panel; the query is still " tag:al"; the count's text should be exactly "4"; [data-conversation-id="other"] and [data-conversation-id="alpine"] are visible. 12. Eval "JSON.stringify(window.__drawerErrors)" must be exactly [].`, func(s *shelley) {
		drawerMe(s)
		s.initScript(drawerErrorsJS)
		s.stubDrawerList(
			drawerConversation("mine", false).participants("me@example.com", "other@example.com").tags("alpha"),
			drawerConversation("other", false).participants("other@example.com"),
			drawerConversation("alpine", false).participants("third@example.com").tags("alpine"),
		)
	})
}

func TestE2EDrawerExactMatchOutranks(t *testing.T) {
	drawerTest(t, useful(0), `An exactly typed tag or user outranks more popular superstrings. `+drawerAsMe+drawerStubbed+`The list is "exact" (written in by me@example.com and other@example.com, tagged alpha) and "more-1", "more-2" and "more-3" (each written in by me@example.com and other@example.com.au, tagged alpha-more). "The tag panel" is data-testid "tag-filter-panel" and "the user panel" data-testid "user-filter-panel"; their options are their elements with role option (a "selector" scoping a "role"). `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. The query should be "user:me@example.com ". 4. Click the ".conversation-query-token-value" inside the token user:me@example.com, then type "tag:alph" key by key. 5. The tag panel's first option should have data-tag "alpha-more" (it has three conversations to alpha's one). 6. Type "a". 7. The tag panel's first option should have data-tag "alpha", and its [aria-selected="true"] element has data-tag "alpha". 8. Press Space. 9. The query should be "user:me@example.com tag:alpha " and the token tag:alpha visible. 10. The same goes for emails, committed with Enter: set the query to "user:me@example.com ". 11. Click the button "Add user filter". 12. The query should be "user:me@example.com user:". 13. The user panel's first option's trimmed text should match /^other@example\.com\.au\s*3$/. 14. Type "other@example.com" key by key. 15. The user panel should have exactly 2 options, the first one's trimmed text matching /^other@example\.com\s*1$/. 16. Press Enter. 17. The query should be "user:me@example.com user:other@example.com ".`, func(s *shelley) {
		drawerMe(s)
		convs := []drawerConv{drawerConversation("exact", false).participants("me@example.com", "other@example.com").tags("alpha")}
		for _, id := range []string{"more-1", "more-2", "more-3"} {
			convs = append(convs, drawerConversation(id, false).participants("me@example.com", "other@example.com.au").tags("alpha-more"))
		}
		s.stubDrawerList(convs...)
	})
}

func TestE2EDrawerMultiplayerFollowsActiveList(t *testing.T) {
	drawerTest(t, useful(0), `Multiplayer capability follows the active list, not search results. `+drawerAsMe+drawerStubbed+`The list is "mine" (written in by me@example.com alone). Searches answer with "foreign", an archived conversation written in by me@example.com and other@example.com: a hit like that must not switch the participant UI on mid-session. `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. Click the button "Search conversations...". 3. The query is "". 4. There is no button "Add user filter". 5. Set the query to "foreign". 6. [data-conversation-id="foreign"] should be visible. 7. There is no button "Add user filter" and no ".conversation-participant-badge". 8. Clear the query. 9. The query is "".`, func(s *shelley) {
		drawerMe(s)
		l := s.stubDrawerList(drawerConversation("mine", false).participants("me@example.com"))
		l.search = drawerAnswer([]drawerConv{drawerConversation("foreign", false).participants("me@example.com", "other@example.com").with("archived", true)})
	})
}

func TestE2EDrawerParticipantsWithoutCurrentUser(t *testing.T) {
	drawerTest(t, useful(0), `Participant UI appears without a current user when a chat has several. There is no current user. `+drawerStubbed+`The list is "shared" (written in by alice@example.com and bob@example.com) and "solo" (alice@example.com). `+drawerQuery+drawerSteps+`1. Navigate to /new. 2. The shared row's ".conversation-participant-badge" should contain the text "2". 3. Click the button "Search conversations...". 4. With no current user there is nothing to seed the query with: the query is "". 5. The button "Add user filter" is enabled. 6. Click the button "View options". 7. The button "Participants" is visible.`, func(s *shelley) {
		s.stubDrawerList(
			drawerConversation("shared", false).participants("alice@example.com", "bob@example.com"),
			drawerConversation("solo", false).participants("alice@example.com"),
		)
	})
}

func TestE2EDrawerGroupByParticipantsOffered(t *testing.T) {
	drawerTest(t, useful(0), `Group by participants is offered only for multi-participant lists. `+drawerAsMe+drawerStubbed+`The list is "mine" (written in by me@example.com) and "other" (other@example.com): two participants across the list, but no conversation has more than one, so there is nothing to group by. `+drawerSteps+`1. Navigate to /new. 2. [data-conversation-id="mine"] is visible. 3. Click the button "View options". 4. The button "Tags" is visible. 5. There is no button "Participants".`, func(s *shelley) {
		drawerMe(s)
		s.stubDrawerList(
			drawerConversation("mine", false).participants("me@example.com"),
			drawerConversation("other", false).participants("other@example.com"),
		)
	})
}

func TestE2EDrawerGroupByParticipantSets(t *testing.T) {
	drawerTest(t, useful(0), `Group by participants buckets whole participant sets, current user first. The browser's requests carry the header X-ExeDev-Email: adam@poyo.co, so the current user is adam@poyo.co. `+drawerStubbed+`The list is "zed-pair" (written in by zed@example.com and yan@example.com), "mine" (adam@poyo.co), "shared" (aaaa@bbb.com and adam@poyo.co), "shared-again" (ADAM@POYO.CO and aaaa@bbb.com: the same set in another order and case), "alice-pair" (alice@example.com and bob@example.com) and "none" (nobody). `+drawerQuery+drawerSteps+drawerDirect+`1. Navigate to /new. 2. [data-conversation-id="mine"] is visible. 3. Drop the seeded current-user filter so every group is on screen: click the button "Search conversations..."; the query should be "user:adam@poyo.co "; clear the query. 4. [data-conversation-id="none"] should be visible. 5. Click the button "View options", then the button "Participants". 6. The trimmed texts of the ".conversation-group-label" elements should be exactly ["adam@poyo.co","aaaa@bbb.com, adam@poyo.co","alice@example.com, bob@example.com","yan@example.com, zed@example.com","Unattributed"]. 7. Exactly one ".conversation-group" contains [data-conversation-id="shared"]; that group contains exactly one [data-conversation-id="shared-again"], and its ".conversation-group-label" has the title attribute exactly "aaaa@bbb.com, adam@poyo.co". 8. There is exactly one [data-conversation-id="shared"] in the page.`, func(s *shelley) {
		s.header("X-ExeDev-Email", "adam@poyo.co")
		s.stubDrawerList(
			drawerConversation("zed-pair", false).participants("zed@example.com", "yan@example.com"),
			drawerConversation("mine", false).participants("adam@poyo.co"),
			drawerConversation("shared", false).participants("aaaa@bbb.com", "adam@poyo.co"),
			drawerConversation("shared-again", false).participants("ADAM@POYO.CO", "aaaa@bbb.com"),
			drawerConversation("alice-pair", false).participants("alice@example.com", "bob@example.com"),
			drawerConversation("none", false).participants(),
		)
	})
}

func TestE2EDrawerStartsExpandedWithOnlyDraft(t *testing.T) {
	drawerTest(t, useful(0), `The drawer starts expanded even when the only item is a draft. `+drawerStubbed+`The list is "draft", a draft. `+drawerSteps+`1. Navigate to /new. 2. The drawer (".drawer") does not have the class "collapsed". 3. The button "Collapse sidebar" is visible.`, func(s *shelley) {
		s.stubDrawerList(drawerConversation("draft", true))
	})
}

// drawerHeaderMetrics says whether the drawer header lines up with the app
// bar, its actions 16px from its right edge.
const drawerHeaderMetrics = `(() => { const rect = (selector) => document.querySelector(selector).getBoundingClientRect(); const header = rect('.drawer-header'), actions = rect('.drawer-header-actions'), chat = rect('.header'); return JSON.stringify({top: header.top === chat.top, bottom: header.bottom === chat.bottom, inset: header.right - actions.right}); })()`

func TestE2EDrawerUntitledHeaderAligned(t *testing.T) {
	drawerTest(t, useful(0), `The drawer starts expanded with an untitled header aligned to the app bar. `+drawerStubbed+`The list is "first" and "second". `+drawerSteps+`1. Navigate to /new. 2. The drawer (".drawer") does not have the class "collapsed". 3. The button "Collapse sidebar" is visible. 4. The drawer has no ".drawer-title", and no element inside the drawer has the exact (trimmed) text "Conversations". 5. Eval `+drawerHeaderMetrics+` must be exactly {"top":true,"bottom":true,"inset":16} (the drawer header's top and bottom equal the app bar's, and its actions end 16px inside its right edge). 6. Click the button "View Archived". 7. ".drawer .drawer-title" should have text exactly "Archived".`, func(s *shelley) {
		s.stubDrawerList(drawerConversation("first", false), drawerConversation("second", false))
	})
}

func TestE2EDrawerCollapseSurvivesReloadSingle(t *testing.T) {
	drawerTest(t, useful(0), `A manual collapse survives reload with a single conversation. `+drawerStubbed+`The list is "only". `+drawerReload+drawerSteps+`1. Navigate to /new. 2. The drawer (".drawer") does not have the class "collapsed". 3. Click the button "Collapse sidebar". 4. The drawer should have the class "collapsed". 5. Reload. 6. The drawer has the class "collapsed". 7. Click the button "Expand sidebar". 8. The drawer should not have the class "collapsed". 9. Reload. 10. The drawer does not have the class "collapsed".`, func(s *shelley) {
		s.stubDrawerList(drawerConversation("only", false))
	})
}

func TestE2EDrawerCollapseSurvivesReloadMany(t *testing.T) {
	drawerTest(t, useful(0), `A manual collapse survives reload with many conversations. `+drawerStubbed+`The list is "first" and "second". `+drawerReload+drawerSteps+`1. Navigate to /new. 2. The drawer (".drawer") does not have the class "collapsed". 3. Click the button "Collapse sidebar". 4. The drawer should have the class "collapsed". 5. Reload. 6. The drawer has the class "collapsed".`, func(s *shelley) {
		s.stubDrawerList(drawerConversation("first", false), drawerConversation("second", false))
	})
}

// drawerSwipeJS gives pages the spec's synthetic touch gestures:
//
//   - window.__drawerSwipe(selector, x1, y1, x2, y2): one finger down at
//     x1,y1 on the first element matching selector, one move to x2,y2, and
//     up.
//   - window.__drawerDrag(selector, points, ending, secondFinger,
//     modalAtEnd): one finger down at the first [x, y] of points, moving
//     through the others (with a second finger 2px left of it when
//     secondFinger), ending with "touchend" or "touchcancel", and with a
//     command-palette modal shown just before the release when modalAtEnd.
//     It returns the computed styles of the drawers and their scrims just
//     before the release, and keeps them in window.__drawerDragStyles.
//   - window.__drawerSwipePatchDiff(x1, x2): a horizontal swipe on a line of
//     a horizontally scrollable patch diff (in a shadow root), added to the
//     main content.
//   - window.__drawerAddFields(): adds to the main content an input, a
//     textarea, a status hook, a contenteditable and horizontally scrollable
//     regions, code and table.
const drawerSwipeJS = `window.__drawerSwipe = (selector, x1, y1, x2, y2) => {
  const target = document.querySelector(selector);
  if (!(target instanceof Element)) throw new Error('Missing ' + selector);
  const makeTouch = (x, y) => new Touch({ identifier: 1, target, clientX: x, clientY: y, screenX: x, screenY: y, pageX: x, pageY: y });
  const startTouch = makeTouch(x1, y1);
  const endTouch = makeTouch(x2, y2);
  let timestamp = 0;
  const dispatch = (type, touches, changedTouches) => {
    const event = new TouchEvent(type, { bubbles: true, cancelable: true, touches, targetTouches: touches, changedTouches });
    timestamp += type === 'touchmove' ? 10 : 1;
    Object.defineProperty(event, 'timeStamp', { value: timestamp });
    target.dispatchEvent(event);
  };
  dispatch('touchstart', [startTouch], [startTouch]);
  dispatch('touchmove', [endTouch], [endTouch]);
  dispatch('touchend', [], [endTouch]);
  return 'swiped';
};
window.__drawerDrag = async (selector, points, ending = 'touchend', secondFinger = false, modalAtEnd = false) => {
  const target = document.querySelector(selector);
  if (!(target instanceof Element)) throw new Error('Missing ' + selector);
  const touch = ([x, y], identifier) => new Touch({ identifier, target, clientX: x, clientY: y, screenX: x, screenY: y, pageX: x, pageY: y });
  let timestamp = 0;
  const emit = (type, touches, changedTouches = touches) => {
    const event = new TouchEvent(type, { bubbles: true, cancelable: true, touches, targetTouches: touches, changedTouches });
    timestamp += type === 'touchmove' ? 10 : 1;
    Object.defineProperty(event, 'timeStamp', { value: timestamp });
    target.dispatchEvent(event);
  };
  const [start, ...moves] = points;
  emit('touchstart', [touch(start, 1)]);
  await Promise.resolve();
  for (const point of moves) {
    const active = [touch(point, 1)];
    if (secondFinger) active.push(touch([point[0] - 2, point[1]], 2));
    emit('touchmove', active);
    await Promise.resolve();
  }
  const surfaces = [...document.querySelectorAll('.drawer, .backdrop, .toc-drawer, .toc-drawer-scrim')];
  const styles = surfaces.map((element) => ({
    className: element.className,
    transform: getComputedStyle(element).transform,
    translate: getComputedStyle(element).translate,
    opacity: getComputedStyle(element).opacity,
  }));
  if (modalAtEnd) {
    const modal = document.createElement('div');
    modal.className = 'command-palette-overlay';
    modal.setAttribute('aria-modal', 'true');
    document.body.append(modal);
  }
  emit(ending, [], [touch(moves.at(-1) ?? start, 1)]);
  document.querySelector('.command-palette-overlay')?.remove();
  window.__drawerDragStyles = styles;
  return styles;
};
window.__drawerSwipePatchDiff = (startX = 160, endX = 220) => {
  const mainContent = document.querySelector('.main-content');
  if (!(mainContent instanceof HTMLElement)) throw new Error('Missing main content');
  const diffsContainer = document.createElement('diffs-container');
  const shadowRoot = diffsContainer.shadowRoot ?? diffsContainer.attachShadow({ mode: 'open' });
  const scrollContainer = document.createElement('div');
  const diffLine = document.createElement('span');
  diffsContainer.style.cssText = 'display:block;width:120px';
  scrollContainer.style.cssText = 'overflow-x:auto;width:120px';
  diffLine.style.cssText = 'display:block;width:320px';
  scrollContainer.append(diffLine);
  shadowRoot.append(scrollContainer);
  mainContent.append(diffsContainer);
  Object.defineProperties(scrollContainer, { clientWidth: { value: 120 }, scrollWidth: { value: 320 } });
  if (scrollContainer.scrollWidth <= scrollContainer.clientWidth) throw new Error('Patch diff test fixture is not horizontally scrollable');
  const makeTouch = (x, y) => new Touch({ identifier: 1, target: diffLine, clientX: x, clientY: y, screenX: x, screenY: y, pageX: x, pageY: y });
  const startTouch = makeTouch(startX, 300);
  const endTouch = makeTouch(endX, 304);
  const fire = (type, touches, changedTouches) => diffLine.dispatchEvent(new TouchEvent(type, { bubbles: true, cancelable: true, composed: true, touches, targetTouches: touches, changedTouches }));
  fire('touchstart', [startTouch], [startTouch]);
  fire('touchmove', [endTouch], [endTouch]);
  fire('touchend', [], [endTouch]);
  return 'swiped';
};
window.__drawerAddFields = () => {
  document.querySelector('.main-content').insertAdjacentHTML('beforeend', '<input style="height:40px"><textarea style="height:40px"></textarea><div class="status-hook-row" style="height:120px"><div class="status-hook-item">hook</div></div><div contenteditable="true" style="height:80px">editable</div><div class="horizontal-test" style="overflow-x:auto;width:120px"><div style="width:320px;height:80px"></div></div><pre class="code-scroll" style="overflow-x:auto;width:120px"><code style="display:block;width:320px;height:24px">long code</code></pre><div class="table-scroll" style="overflow-x:auto;width:120px"><table style="width:320px"><tbody><tr><td>wide table</td></tr></tbody></table></div>');
  return 'added';
};`

const drawerSwipes = `The phone's screen is 393x851. The page has synthetic touch gestures, run with eval: "window.__drawerSwipe(selector, x1, y1, x2, y2)" puts one finger down at viewport point x1,y1 on the first element matching selector, moves it to x2,y2 and lifts it; "window.__drawerDrag(selector, points, ending, secondFinger, modalAtEnd)" puts a finger down at the first [x, y] of points and moves it through the rest (with a second finger beside it when secondFinger is true), then ends with ending ("touchend" or "touchcancel"); when modalAtEnd is true a modal (".command-palette-overlay" with aria-modal) appears just before the release and goes after it. __drawerDrag returns the computed styles ({className, transform, translate, opacity}) of every ".drawer, .backdrop, .toc-drawer, .toc-drawer-scrim" element just before the release, and keeps them in window.__drawerDragStyles. Run each gesture with a single eval, without expect; wait for its target to exist first. "Open" means the drawer (".drawer") has the class "open"; "closed" that it exists without it (eval "document.querySelector('.drawer').classList.contains('open')" with no guard, so that a missing drawer fails). `

func drawerSwipeTest(t *testing.T, useful lazycue.Useful, description string, setup func(s *shelley)) {
	t.Helper()
	e2e(t, useful, description, func(s *shelley) {
		s.viewport(393, 851)
		s.initScript(drawerSwipeJS)
		setup(s)
	})
}

func drawerSwipeStub(s *shelley) {
	s.stubDrawerList(drawerConversation("first", false))
}

const drawerSwipeStubbed = `The test harness has already stubbed the server's conversation list to one conversation, "first", and cut off its live update stream; there is nothing to set up or check about that. `

func TestE2EDrawerSwipeOpensAndCloses(t *testing.T) {
	drawerSwipeTest(t, useful(0), `The mobile drawer opens and closes with opposite page swipes. `+drawerSwipes+drawerSwipeStubbed+drawerSteps+`1. Navigate to /new. 2. The drawer is closed. 3. Focus the message input (data-testid "message-input"; eval its focus()); it is focused (document.activeElement). 4. Swipe ".main-content" from 180,300 to 350,304. 5. The drawer should be open. 6. The message input is no longer focused. 7. Swipe ".backdrop" from 350,300 to 150,304. 8. The drawer should be closed.`, drawerSwipeStub)
}

func TestE2EDrawerSwipeFilterLowercase(t *testing.T) {
	drawerSwipeTest(t, useful(0), `The mobile drawer keeps filter text lowercase-ready. `+drawerSwipes+drawerSwipeStubbed+drawerSteps+`1. Navigate to /new. 2. ".main-content" is visible. 3. Swipe ".main-content" from 180,300 to 350,304. 4. Click the button "Search conversations...". 5. There is no ".p-tooltip" (a tap shows no tooltip). 6. Click the button "Add tag filter". 7. ".drawer-search-input" should have data-query-value exactly "tag:", autocapitalize "none", and text containing "tag:". 8. It holds no [data-conversation-query-token="tag"].`, drawerSwipeStub)
}

func TestE2EDrawerSwipeEdgesAndShortMoves(t *testing.T) {
	drawerSwipeTest(t, useful(0), `The mobile drawer leaves the system edge, short movement, and vertical scrolling alone. `+drawerSwipes+drawerSwipeStubbed+drawerSteps+`1. Navigate to /new. 2. Swipe ".main-content" from 40,300 to 140,304. 3. The drawer should be open. 4. Swipe ".backdrop" from 350,300 to 150,304. 5. The drawer should be closed. 6. Swipe ".main-content" from 12,300 to 112,304 (the system edge). 7. The drawer is closed. 8. Swipe ".main-content" from 340,300 to 360,304. 9. The drawer should be open. 10. Swipe ".backdrop" from 350,300 to 150,304. 11. The drawer should be closed. 12. Swipe ".main-content" from 370,300 to 390,304 (the right system edge). 13. The drawer is closed. 14. Swipe ".main-content" from 160,300 to 166,304 (too short). 15. The drawer is closed. 16. Swipe ".main-content" from 64,200 to 92,300 (mostly vertical). 17. The drawer is closed.`, drawerSwipeStub)
}

func TestE2EDrawerSwipeLeavesPatchDiffScroll(t *testing.T) {
	drawerSwipeTest(t, useful(0), `The mobile drawer leaves horizontal patch diff scrolling alone. `+drawerSwipes+drawerSwipeStubbed+`Eval "window.__drawerSwipePatchDiff(160, 220)" adds a horizontally scrollable patch diff (in a shadow root) to the main content and swipes one of its lines from x 160 to x 220. `+drawerSteps+`1. Navigate to /new. 2. ".main-content" is visible. 3. Eval "window.__drawerSwipePatchDiff(160, 220)". 4. The drawer is closed.`, drawerSwipeStub)
}

// drawerDragFollows says whether the drawer had moved and its scrim was part
// faded in when the finger was lifted.
const drawerDragFollows = `(() => { const s = window.__drawerDragStyles; const drawer = s.find(x => x.className.includes('drawer')); const scrim = s.find(x => x.className.includes('backdrop')); return !!drawer && !!scrim && drawer.transform !== 'matrix(1, 0, 0, 1, 0, 0)' && Number(scrim.opacity) > 0 && Number(scrim.opacity) < 1; })()`

func TestE2EDrawerSwipeFollowsFinger(t *testing.T) {
	drawerSwipeTest(t, useful(0), `The mobile drawer follows the finger and cancels safely on a second finger or touchcancel. `+drawerSwipes+drawerSwipeStubbed+drawerSteps+`1. Navigate to /new. 2. Drag ".main-content" through [[180,300],[260,302],[370,304]] with "touchend". 3. Mid-gesture the drawer had moved and its scrim was partly opaque: eval `+drawerDragFollows+` must be true. 4. The drawer should be open. 5. Drag ".backdrop" through [[350,300],[250,302]] with "touchend" and a second finger. 6. The drawer is still open. 7. Swipe ".backdrop" from 350,300 to 150,304. 8. The drawer should be closed. 9. Drag ".main-content" through [[180,300],[220,302]] ending with "touchcancel". 10. The drawer is closed.`, drawerSwipeStub)
}

func TestE2EDrawerSwipeCancelledByModal(t *testing.T) {
	drawerSwipeTest(t, useful(0), `The mobile drawer cancels an in-flight open if a modal appears before release. `+drawerSwipes+drawerSwipeStubbed+drawerSteps+`1. Navigate to /new. 2. Drag ".main-content" through [[180,300],[340,302]] with "touchend", no second finger, and a modal at the end. 3. The drawer should be closed. 4. The drawer exists and its style attribute, if any, does not contain "transform" (no guard on the drawer). 5. There is no ".backdrop".`, drawerSwipeStub)
}

func TestE2EDrawerSwipeIgnoresFields(t *testing.T) {
	drawerSwipeTest(t, useful(0), `The mobile drawer keeps form fields, editable content, status hooks, and scroll regions out of drawer swipes. `+drawerSwipes+drawerSwipeStubbed+`Eval "window.__drawerAddFields()" adds to the main content an input, a textarea, a status hook (".status-hook-item"), a contenteditable, and horizontally scrollable regions (".horizontal-test > div"), code (".code-scroll code") and table (".table-scroll td"). `+drawerSteps+`1. Navigate to /new. 2. Swipe ".main-content" from 180,300 to 256,304. 3. The drawer should be open. 4. Swipe ".backdrop" from 350,300 to 150,304. 5. The drawer should be closed. 6. Eval "window.__drawerAddFields()". 7. For each of the selectors "input", "textarea", ".status-hook-item", "[contenteditable=\"true\"]", ".horizontal-test > div", ".code-scroll code" and ".table-scroll td", in turn: swipe the first element it matches from 180,300 to 256,304, then check that the drawer is closed.`, drawerSwipeStub)
}

func TestE2EDrawerSwipeBlockedByDiffViewer(t *testing.T) {
	drawerSwipeTest(t, useful(0), `The mobile drawer does not open while a diff viewer modal is shown. `+drawerSwipes+`The test harness has already stubbed the server's conversation list to one conversation, "first", in /tmp, and cut off its live update stream; there is nothing to set up or check about that. `+drawerSteps+`1. Navigate to /new. 2. The message input (data-testid "message-input") should be visible (the shortcut is the app's; a key pressed before it has mounted is lost). 3. Without clicking or focusing anything first, press "D" with modifiers "Control+Shift". 4. ".diff-viewer-overlay" should be visible. 5. Swipe ".diff-viewer-header" from 180,100 to 256,104. 6. The drawer is closed.`, func(s *shelley) {
		s.stubDrawerList(drawerConversation("first", false).with("cwd", "/tmp"))
	})
}

// drawerTOCTracks says whether the TOC drawer had moved and its scrim was
// part faded in when the finger was lifted.
const drawerTOCTracks = `(() => { const s = window.__drawerDragStyles; const drawer = s.find(x => x.className.includes('toc-drawer')); const scrim = s.find(x => x.className.includes('toc-drawer-scrim')); return !!drawer && !!scrim && drawer.translate !== '0px' && Number(scrim.opacity) > 0 && Number(scrim.opacity) < 1; })()`

const drawerTOC = `A conversation exists at /c/{{SLUG}}. "The TOC drawer" is ".toc-drawer". "Open the TOC" means drag ".main-content" through [[220,300],[130,302],[40,304]] with "touchend", then the TOC drawer should be visible; "close the TOC" means drag ".toc-drawer" through [[100,300],[200,302],[300,304]] with "touchend", then the TOC drawer should be hidden. `

func TestE2EDrawerTOCSwipe(t *testing.T) {
	drawerSwipeTest(t, useful(0), `The mobile TOC drawer tracks opening and closes with a swipe; its own dialog is exempt from modal blocking. `+drawerSwipes+drawerTOC+drawerSteps+`1. Navigate to /c/{{SLUG}}. 2. ".toc-button" should be visible. 3. Drag ".main-content" through [[220,300],[130,302],[40,304]] with "touchend". 4. Mid-gesture the TOC drawer had moved and its scrim was partly opaque: eval `+drawerTOCTracks+` must be true. 5. The TOC drawer should be visible. 6. A touch that begins in the drawer's own dialog surface still swipes, though unrelated modal overlays inhibit starting a gesture: drag ".toc-drawer" through [[100,300],[200,302],[300,304]] with "touchend". 7. The TOC drawer should be hidden.`, func(s *shelley) {
		s.v("SLUG", s.conversation("echo mobile TOC drawer", "").Slug)
	})
}

func TestE2EDrawerTOCSwipeGuards(t *testing.T) {
	drawerSwipeTest(t, useful(0), `The mobile TOC drawer honors edge, vertical, scroll, editable, modal, multitouch, and cancel guards. `+drawerSwipes+drawerTOC+`Eval "window.__drawerAddFields()" adds to the main content an input, a textarea, a status hook (".status-hook-item"), a contenteditable, and horizontally scrollable regions (".horizontal-test > div"), code (".code-scroll code") and table (".table-scroll td"). Eval "window.__drawerSwipePatchDiff(220, 160)" adds a horizontally scrollable patch diff to the main content and swipes one of its lines from x 220 to x 160. `+drawerSteps+`1. Navigate to /c/{{SLUG}}. 2. ".toc-button" should be visible. 3. Swipe ".main-content" from 220,300 to 180,304. 4. The TOC drawer should be visible. 5. Swipe ".toc-drawer" from 100,300 to 300,304. 6. The TOC drawer should be hidden. 7. Swipe ".main-content" from 40,300 to 16,304. 8. The TOC drawer should be visible. 9. Swipe ".toc-drawer" from 100,300 to 300,304. 10. The TOC drawer should be hidden. 11. Swipe ".main-content" from 28,300 to 4,304. 12. The TOC drawer is hidden. 13. Swipe ".main-content" from 350,300 to 288,304. 14. The TOC drawer should be visible. 15. Swipe ".toc-drawer" from 100,300 to 300,304. 16. The TOC drawer should be hidden. 17. Swipe ".main-content" from 382,300 to 320,304 (the right system edge), then swipe ".main-content" from 180,200 to 184,280 (vertical). 18. The TOC drawer is hidden. 19. Eval "window.__drawerAddFields()". 20. For each of the selectors "input", "textarea", ".status-hook-item", "[contenteditable=\"true\"]", ".horizontal-test > div", ".code-scroll code" and ".table-scroll td", in turn: swipe the first element it matches from 220,300 to 180,304, then check that the TOC drawer is hidden. 21. Eval "window.__drawerSwipePatchDiff(220, 160)". 22. The TOC drawer is hidden. 23. Eval "(() => { const modal = document.createElement('div'); modal.className = 'command-palette-overlay'; modal.setAttribute('aria-modal', 'true'); document.querySelector('.main-content').append(modal); return 'modal'; })()". 24. Swipe ".main-content" from 220,300 to 180,304. 25. The TOC drawer is hidden. 26. Eval "document.querySelector('.command-palette-overlay').remove()". 27. Drag ".main-content" through [[220,300],[130,302],[40,304]] ending with "touchcancel". 28. The TOC drawer is hidden. 29. Drag ".main-content" through [[220,300],[130,302],[40,304]] with "touchend", no second finger, and a modal at the end. 30. The TOC drawer is hidden. 31. Open the TOC. 32. Drag ".toc-drawer" through [[100,300],[200,302],[300,304]] with "touchend", no second finger, and a modal at the end. 33. The TOC drawer is visible, and its style attribute, if any, does not contain "translate". 34. Drag ".toc-drawer" through [[100,300],[200,302],[300,304]] with "touchend" and a second finger. 35. The TOC drawer is visible. 36. Drag ".toc-drawer" through [[100,300],[200,302],[300,304]] ending with "touchcancel". 37. The TOC drawer is visible. 38. Close the TOC.`, func(s *shelley) {
		s.v("SLUG", s.conversation("echo TOC swipe guards", "").Slug)
	})
}
