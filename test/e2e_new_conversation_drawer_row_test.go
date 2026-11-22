package test

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/new-conversation-drawer-row.spec.ts, which ran in a
// 1280x720 viewport. A new conversation is a selected draft row in the
// drawer from the moment it opens, and the row stays put through the send.

// newRowPage is the spec's page helpers, run in every page: it records the
// event stream, as the spec's init script did, and asks the test for the
// conversation a held request made (/test/held/NAME, "" until then), as
// the spec's awaited promises gave it.
const newRowPage = `
window.__streamed = [];
const NewRowEventSource = window.EventSource;
window.EventSource = class extends NewRowEventSource {
  constructor(url, init) {
    super(url, init);
    this.addEventListener("message", (e) => window.__streamed.push(e.data));
  }
};
window.__ncHeld = async (name) => (await fetch("/test/held/" + name)).text();
// Whether the event stream has reported the held conversation.
window.__ncStreamed = async (name) => {
  const id = await window.__ncHeld(name);
  return String(id !== "" && window.__streamed.some((d) => d.includes(id)));
};
// How many rows are the placeholder or the held conversation's.
window.__ncComposerRows = async (name) => {
  const id = await window.__ncHeld(name);
  return String(
    document.querySelectorAll(
      ".drawer .conversation-item:is([data-conversation-id='composer'], [data-conversation-id='" + id + "'])",
    ).length,
  );
};
// Whether the selected row is the held conversation's.
window.__ncActiveIsHeld = async (name) => {
  const id = await window.__ncHeld(name);
  const row = document.querySelector(".drawer .conversation-item.active");
  return String(id !== "" && row?.getAttribute("data-conversation-id") === id);
};
// How many rows are the held conversation's.
window.__ncHeldRows = async (name) => {
  const id = await window.__ncHeld(name);
  return String(
    document.querySelectorAll(".drawer .conversation-item[data-conversation-id='" + id + "']").length,
  );
};
// Notes in window.__ncNext the row below the row of id (or else above),
// which should open once it is deleted.
window.__ncNoteNext = (id) => {
  const ids = [...document.querySelectorAll(".drawer .conversation-item")].map((row) =>
    row.getAttribute("data-conversation-id"),
  );
  window.__ncNext = ids[ids.indexOf(id) + 1] ?? ids[ids.indexOf(id) - 1];
  return String(ids.includes(id));
};
// Samples the drawer every frame into window.__frames: whether a row is
// selected, its title and preview, and the URL path.
window.__ncRecordFrames = () => {
  const frames = [];
  window.__frames = frames;
  const tick = () => {
    const row = document.querySelector(".drawer .conversation-item.active");
    frames.push({
      title: row?.querySelector(".conversation-title")?.textContent?.trim() ?? "",
      preview: row?.querySelector(".conversation-preview")?.textContent?.trim() ?? "",
      shown: !!row,
      path: location.pathname,
    });
    requestAnimationFrame(tick);
  };
  requestAnimationFrame(tick);
  return "recording";
};
`

// newRowHold handles the requests to pattern as the spec's holdCreate
// did: each waits for call before (if any) to reach the server, and for
// call after (if any) to have its answer let through. The conversation the
// first one made is at /test/held/NAME, and in what it returns, and call
// NAME-requested says whether a POST arrived.
func (s *shelley) newRowHold(pattern, name, before, after string) func() string {
	var pre, post gate
	if before != "" {
		pre = s.gate(before)
	}
	if after != "" {
		post = s.gate(after)
	}
	requested := s.signal(name + "-requested")
	var mu sync.Mutex
	id := ""
	s.route("GET /test/held/"+name, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		io.WriteString(w, id)
	})
	s.route(pattern, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			requested.Store(true)
		}
		if pre != nil && !pre.wait(r) {
			return
		}
		rec := httptest.NewRecorder()
		s.pass.ServeHTTP(rec, r)
		var made struct {
			ID string `json:"conversation_id"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &made) == nil && made.ID != "" {
			mu.Lock()
			if id == "" {
				id = made.ID
			}
			mu.Unlock()
		}
		if post != nil && !post.wait(r) {
			return
		}
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
	})
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return id
	}
}

// newRowTest runs a test of the spec in its viewport, with its page
// helpers.
func newRowTest(t *testing.T, useful lazycue.Useful, desc string, setup func(s *shelley)) {
	e2e(t, useful, desc, func(s *shelley) {
		s.viewport(1280, 720)
		s.initScript(newRowPage)
		setup(s)
	})
}

// How the spec's tests talk about the drawer.
const newRowDrawer = `The viewport is 1280x720. The page has helpers, from an init script, used in evals: __ncHeld(name), __ncStreamed(name), __ncComposerRows(name), __ncActiveIsHeld(name), __ncHeldRows(name), __ncNoteNext(id) and __ncRecordFrames(); evals that call them return strings, and those taking a name are async: write them as given. "The active row" is ".drawer .conversation-item.active"; any check of the active row (or of something in it) starts by checking that there is exactly one (assert_count 1), as the old test's locator was strict. "The placeholder" is ".drawer [data-conversation-id='composer']". "The row of <id>" is ".drawer .conversation-item[data-conversation-id='<id>']". A row's "preview" is its ".conversation-preview" and its "title" its ".conversation-title". "The input" is data-testid "message-input", and "the Send button" data-testid "send-button". To "open a new conversation": navigate to /new, wait up to 30s for the input to be visible, and check that the active row's data-conversation-id is exactly "composer". To "start a new conversation from the header": click {"selector": ".header", "role": "button", "name": "New Conversation", "exact": true} if it is visible (it is at this size); only if it is not, click the button "More options" and then {"selector": ".chat-overflow-popover", "role": "button", "name": "New Conversation", "exact": true}. "The Delete button of the active row" is {"selector": ".drawer .conversation-item.active", "role": "button", "name": "Delete", "exact": true}. To "delete row R" (R a selector): click {"selector": R, "role": "button", "name": "Delete", "exact": true}, then click {"selector": R + " .drawer-delete-confirm", "role": "button", "name": "Delete"}. "It opened the noted row" means: assert_count 1 of the active row; eval with expect "true" of String(document.querySelector(".drawer .conversation-item.active")?.getAttribute("data-conversation-id") === window.__ncNext); the URL contains "/c/" (assert_url with text). `

func TestE2ENewRowSelectedDraft(t *testing.T) {
	newRowTest(t, useful(0), `a new conversation is a selected draft row from the start. Conversation {{OTHER}} (slug {{OTHER_SLUG}}) exists. Requests to /api/conversations/draft are held before the server sees them until call "send-draft", and their answers after until call "release-draft"; the conversation the first one made is the held "draft". Call "delete-draft" deletes that conversation from outside the page. `+newRowDrawer+drawerDirect+drawerSteps+`1. Navigate to /c/{{OTHER_SLUG}} and wait up to 30s for the input to be visible. 2. The row of {{OTHER}} has the class active. 3. Start a new conversation from the header. 4. The active row's data-conversation-id is exactly "composer". 5. The active row's title matches /^draft( \d+)?$/: eval with expect "true" of String(/^draft( \d+)?$/.test(document.querySelector(".drawer .conversation-item.active .conversation-title").textContent.trim())). 6. Click the row of {{OTHER}}. 7. The row of {{OTHER}} has the class active. 8. The placeholder's count is exactly 1, and it does not have the class active. 9. Move the pointer out of the drawer: hover at x 900, y 400 of the viewport (no selector). 10. There is no placeholder (assert_count 0). 11. Start a new conversation from the header. 12. The active row's data-conversation-id is exactly "composer". 13. Fill the input with "write a limerick". 14. The active row's preview reads exactly "write a limerick". 15. Call "send-draft". 16. The event stream reported the draft before its answer came back: eval with expect "true" of __ncStreamed("draft"). 17. Still one row: eval with expect "1" of __ncComposerRows("draft"). 18. The active row's preview reads exactly "write a limerick". 19. Call "release-draft". 20. The active row is the draft's: eval with expect "true" of __ncActiveIsHeld("draft"). 21. The active row's preview reads exactly "write a limerick". 22. There is exactly one active row (assert_count 1). 23. Call "delete-draft" (no expect: it acts once). 24. The draft's row goes: eval with expect "0" of __ncHeldRows("draft").`, func(s *shelley) {
		other := s.conversation("echo: elsewhere", "")
		s.v("OTHER", other.ID)
		s.v("OTHER_SLUG", other.Slug)
		held := s.newRowHold("/api/conversations/draft", "draft", "send-draft", "release-draft")
		s.call("delete-draft", func() string {
			id := held()
			if id == "" {
				s.fail("no draft was made")
			}
			s.api("POST", "/api/conversation/"+id+"/delete", nil, nil)
			return "deleted"
		})
	})
}

func TestE2ENewRowDeleteOpensNeighbor(t *testing.T) {
	newRowTest(t, useful(0), `deleting the draft being composed opens its neighbor. Conversation {{NEIGHBOR}} exists. Requests to /api/conversations/draft are held before the server sees them until call "save-draft"; call "draft-requested" says whether a POST to it arrived, and the conversation the first one made is the held "draft". `+newRowDrawer+drawerDirect+drawerSteps+`1. Open a new conversation. 2. Unused, the placeholder goes at once: eval with expect "true" of __ncNoteNext("composer"). 3. Delete row ".drawer .conversation-item[data-conversation-id='composer']". 4. There is no placeholder row: assert_count 0 of ".drawer .conversation-item[data-conversation-id='composer']". 5. It opened the noted row. 6. Start a new conversation from the header. 7. The Delete button of the active row is visible (after checking there is one active row). 8. Fill the input with "never mind". 9. The active row's preview reads exactly "never mind". 10. There is no Delete button in the active row: assert_count 0 of {"selector": ".drawer .conversation-item.active", "role": "button", "name": "Delete", "exact": true}. 11. Call "draft-requested" with expect "true". 12. Fill the input with "". 13. The active row's preview reads exactly "". 14. There is no Delete button in the active row (as in step 10). 15. Fill the input with "never mind". 16. Call "save-draft". 17. The active row is the draft's: eval with expect "true" of __ncActiveIsHeld("draft"). 18. Note the row to open: eval with expect "true" of (async () => window.__ncNoteNext(await window.__ncHeld("draft")))(). 19. Delete row ".drawer .conversation-item.active" (the draft's). 20. The draft's row is gone: eval with expect "0" of __ncHeldRows("draft"). 21. It opened the noted row. 22. The input shows the noted conversation's own draft: eval with expect "true" of (async () => { const c = (await (await fetch("/api/conversation/" + window.__ncNext)).json()).conversation; return String(document.querySelector("[data-testid='message-input']").value === c.draft); })(). 23. The draft is gone from the server: eval with expect "404" of (async () => String((await fetch("/api/conversation/" + await window.__ncHeld("draft"))).status))(). 24. Start a new conversation from the header. 25. The input's value is exactly "" (assert_value).`, func(s *shelley) {
		neighbor := s.conversation("echo: neighbor", "")
		s.v("NEIGHBOR", neighbor.ID)
		s.newRowHold("/api/conversations/draft", "draft", "save-draft", "")
	})
}

func TestE2ENewRowAloneNoDelete(t *testing.T) {
	newRowTest(t, useful(0), `alone, a new conversation has no delete, and a deleted draft returns to it. The test lists no conversations, and the live update stream is cut off. The conversation the first request to /api/conversations/draft made is the held "draft". `+newRowDrawer+drawerDirect+drawerSteps+`1. Open a new conversation. 2. There is exactly one ".drawer .conversation-item" (assert_count 1). 3. There is no Delete button in the active row: assert_count 0 of {"selector": ".drawer .conversation-item.active", "role": "button", "name": "Delete", "exact": true}. 4. Fill the input with "lonely". 5. The active row is the draft's: eval with expect "true" of __ncActiveIsHeld("draft"). 6. The URL path is exactly the draft's: eval with expect "true" of (async () => String(location.pathname === "/c/" + await window.__ncHeld("draft")))(). 7. Note the row to open, which there is none of: eval with expect "true" of (async () => window.__ncNoteNext(await window.__ncHeld("draft")))(), then eval with expect "undefined" of String(window.__ncNext). 8. Delete row ".drawer .conversation-item.active" (the draft's). 9. The draft's row is gone: eval with expect "0" of __ncHeldRows("draft"). 10. The URL path is exactly "/new" (assert_url with value). 11. The active row's data-conversation-id is exactly "composer". 12. The input's value is exactly "" (assert_value).`, func(s *shelley) {
		s.route("/api/conversations/snapshot", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"conversations": []any{}, "hash": "none"})
		})
		s.route("/api/stream2", abort)
		s.newRowHold("/api/conversations/draft", "draft", "", "")
	})
}

func TestE2ENewRowKeepsSentMessage(t *testing.T) {
	newRowTest(t, useful(0), `a draft row keeps the sent message until the agent replies. The conversation the first request to /api/conversations/draft made is the held "draft". `+newRowDrawer+drawerDirect+drawerSteps+`1. Open a new conversation. 2. Fill the input with "{{MESSAGE}}". 3. The active row is the draft's: eval with expect "true" of __ncActiveIsHeld("draft"). 4. Eval with expect "recording" of __ncRecordFrames() (it must run once). 5. Click the Send button. 6. The active row's preview reads exactly "{{REPLY}}" (up to 30s). 7. Every frame recorded showed a row, previewing the message, the reply or "New {{REPLY}}", at a path other than /: eval with expect "ok" of (() => { const frames = window.__frames; if (!frames.length) return "no frames"; const bad = frames.find((f) => !f.shown || !["{{MESSAGE}}", "{{REPLY}}", "New {{REPLY}}"].includes(f.preview) || f.path === "/"); return bad ? JSON.stringify(bad) : "ok"; })().`, func(s *shelley) {
		message := "echo: drafted 4f2a9c1e"
		s.v("MESSAGE", message)
		s.v("REPLY", message[len("echo: "):])
		s.newRowHold("/api/conversations/draft", "draft", "", "")
	})
}

func TestE2ENewRowSendBeforeSave(t *testing.T) {
	newRowTest(t, useful(0), `a first message sent before the draft is saved keeps a single selected row. Answers to /api/conversations/new are held until call "release-send"; the conversation the first one made is the held "send". `+newRowDrawer+drawerDirect+drawerSteps+`1. Open a new conversation. 2. Eval with expect "recording" of __ncRecordFrames() (it must run once). 3. Type and send in one go, before the draft autosave can fire, so no draft exists yet: eval with expect "sent" of (async () => { const input = document.querySelector("[data-testid='message-input']"); input.value = "{{MESSAGE}}"; input.dispatchEvent(new Event("input", { bubbles: true })); await Promise.resolve(); document.querySelector("[data-testid='send-button']").click(); return "sent"; })() (it must run once). 4. The event stream reported the conversation before its answer came back: eval with expect "true" of __ncStreamed("send"). 5. Still one row: eval with expect "1" of __ncComposerRows("send"). 6. The active row's preview reads exactly "{{MESSAGE}}". 7. Call "release-send". 8. The active row's preview reads exactly "{{REPLY}}" (up to 30s). 9. A row was selected in every frame recorded, and once it previewed the message, it previewed the message or the reply: eval with expect "ok" of (() => { const frames = window.__frames; const unshown = frames.find((f) => !f.shown); if (unshown) return JSON.stringify(unshown); const bad = frames.slice(frames.findIndex((f) => f.preview)).find((f) => !["{{MESSAGE}}", "{{REPLY}}"].includes(f.preview)); return bad ? JSON.stringify(bad) : "ok"; })(). 10. There is exactly one active row (assert_count 1). 11. Exactly one ".drawer .conversation-item" has the text "{{REPLY}}" (assert_count 1 with has_text).`, func(s *shelley) {
		message := "echo: undrafted 4f2a9c1e"
		s.v("MESSAGE", message)
		s.v("REPLY", message[len("echo: "):])
		s.newRowHold("/api/conversations/new", "send", "", "release-send")
	})
}

func TestE2ENewRowUnusedDraftStays(t *testing.T) {
	newRowTest(t, useful(0), `an unused draft left for other rows stays put until the pointer leaves. Conversation {{OTHER}} exists, and so does {{DRAFT}}, a saved draft ("saved idea"). `+newRowDrawer+drawerDirect+drawerSteps+`1. Open a new conversation. 2. Click the row of {{DRAFT}}. 3. The row of {{DRAFT}} has the class active. 4. The placeholder's count is exactly 1, and it does not have the class active. 5. Click the row of {{OTHER}}. 6. The row of {{OTHER}} has the class active. 7. The placeholder's count is exactly 1. 8. Click the placeholder. 9. The URL path is exactly "/new" (assert_url with value). 10. The active row's data-conversation-id is exactly "composer". 11. There is exactly one active row (assert_count 1).`, func(s *shelley) {
		other := s.conversation("echo: elsewhere", "")
		s.v("OTHER", other.ID)
		var saved struct {
			ID string `json:"conversation_id"`
		}
		s.api("POST", "/api/conversations/draft", map[string]any{"draft": "saved idea", "cwd": s.cwd}, &saved)
		s.v("DRAFT", saved.ID)
	})
}

func TestE2ENewRowArchiveOnlyRow(t *testing.T) {
	newRowTest(t, useful(0), `archiving the only row beside an unused draft returns to the draft. The test lists only conversation {{ONLY}}, and the live update stream is cut off. Call "composer-requests" lists the requests to /api/conversation/composer (or below it) as JSON. `+newRowDrawer+drawerDirect+drawerSteps+`1. Open a new conversation. 2. Click the row of {{ONLY}}. 3. The row of {{ONLY}} has the class active. 4. Archive from the keyboard, the pointer still over the drawer: focus the row's Actions button with an eval of document.querySelector(".drawer .conversation-item[data-conversation-id='{{ONLY}}'] [data-testid='conversation-actions-menu'] button[aria-label='Actions']").focus() if that is the button named Actions in the row (look it up if not), and press Enter (press_key). 5. The menu item "Archive" is visible (role menuitem, name "Archive"). 6. Press ArrowDown, then press Enter. 7. The URL path is exactly "/new" (assert_url with value). 8. The active row's data-conversation-id is exactly "composer". 9. Call "composer-requests" with expect "[]".`, func(s *shelley) {
		only := s.conversation("echo: alone", "")
		s.v("ONLY", only.ID)
		var snapshot struct {
			Conversations []map[string]any `json:"conversations"`
		}
		s.api("GET", "/api/conversations/snapshot", nil, &snapshot)
		var listed []map[string]any
		for _, c := range snapshot.Conversations {
			if c["conversation_id"] == only.ID {
				listed = append(listed, c)
			}
		}
		if len(listed) != 1 {
			s.fail("the snapshot lists %s %d times", only.ID, len(listed))
		}
		s.route("/api/conversations/snapshot", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"conversations": listed, "hash": "only"})
		})
		s.route("/api/stream2", abort)
		var mu sync.Mutex
		composer := []string{}
		record := func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			composer = append(composer, r.URL.String())
			mu.Unlock()
			s.pass.ServeHTTP(w, r)
		}
		s.route("/api/conversation/composer", record)
		s.route("/api/conversation/composer/", record)
		s.call("composer-requests", func() string {
			mu.Lock()
			defer mu.Unlock()
			return mustJSON(composer)
		})
	})
}

func TestE2ENewRowNextNewEmpty(t *testing.T) {
	newRowTest(t, useful(0), `a first message sent before the draft is saved leaves the next new conversation empty. `+newRowDrawer+drawerDirect+drawerSteps+`1. Open a new conversation. 2. Fill the input with "{{MESSAGE}}". 3. Click the Send button. 4. The URL contains "/c/" (assert_url with text). 5. The input's value is exactly "" (assert_value). 6. Start a new conversation from the header. 7. The input's value is exactly "". 8. Reload the page (reload action). 9. The input's value is exactly "".`, func(s *shelley) {
		s.v("MESSAGE", "echo: once 4f2a9c1e")
	})
}
