package test

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Ported from ui/e2e/day-filter.spec.ts, which ran in New York with the
// page's clock fixed at noon there.

var dayFilterNow = time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC)

// dayFilterSetup puts the page in New York at dayFilterNow.
func dayFilterSetup(s *shelley) {
	s.timezone("America/New_York")
	s.filtersFixedTime(dayFilterNow)
}

// dayFilterCorpus is the server's list of conversations, by ID.
func (s *shelley) dayFilterCorpus() map[string]map[string]any {
	var corpus []map[string]any
	s.api("GET", "/api/conversations", nil, &corpus)
	byID := map[string]map[string]any{}
	for _, c := range corpus {
		byID[c["conversation_id"].(string)] = c
	}
	return byID
}

// dayFilterFixture is the server's listing of conversation id with fields
// overridden.
func (s *shelley) dayFilterFixture(corpus map[string]map[string]any, id string, fields map[string]any) map[string]any {
	c, ok := corpus[id]
	if !ok {
		s.fail("conversation %s is not listed", id)
	}
	return dayFilterWith(c, fields)
}

// dayFilterWith is a copy of the listing c with fields overridden.
func dayFilterWith(c, fields map[string]any) map[string]any {
	c = maps.Clone(c)
	maps.Copy(c, fields)
	return c
}

// dayFilterList has the browser's conversation list requests answered
// with convs.
func (s *shelley) dayFilterList(convs []map[string]any, hash string) {
	s.route("GET /api/conversations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, convs)
	})
	s.route("/api/conversations/snapshot", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"conversations": convs, "hash": hash})
	})
}

// How the spec's tests drive the day picker.
const dayFilterPicker = `"The picker" is data-testid "conversation-day-filter"; "the date" is its ".drawer-day-date" and "the count" its ".drawer-day-count". The picker's buttons "Previous day", "Next day", "Today" and "Turn off Daily" are {"selector": "[data-testid='conversation-day-filter']", "role": "button", "name": "...", "exact": true}. To "group by L": click the button "View options", then click {"selector": ".group-by-menu", "role": "button", "name": "L", "exact": true}. Conversation rows are ".conversation-item[data-conversation-id='<id>']" ("the row of <id>"). `

func TestE2EDayFilterSubagents(t *testing.T) {
	e2e(t, useful(0), `Daily grouping expands subagents last touched on other days. The page runs in New York with its clock fixed at 2026-10-08T16:00:00Z. The test lists exactly nine conversations: {{PARENT}} (slug {{PARENT_SLUG}}), updated at that moment, with 8 subagents, and its subagents older-subagent-0 to older-subagent-7, updated one or two days before; the live update stream is cut off. `+dayFilterPicker+drawerDirect+drawerSteps+`1. Navigate to /c/{{PARENT_SLUG}}. 2. Click the button "Open conversations". 3. Group by Daily. 4. The count reads exactly "1". 5. The ".subagent-count-badge" inside "[data-conversation-id='{{PARENT}}']" reads exactly "8". 6. Click the button "Expand subagents" inside "[data-conversation-id='{{PARENT}}']". 7. There are exactly 8 ".drawer-subagent-list .subagent-item". 8. The subagents' titles, trimmed and sorted, are exactly older-subagent-0 to older-subagent-7: eval with expect '["older-subagent-0","older-subagent-1","older-subagent-2","older-subagent-3","older-subagent-4","older-subagent-5","older-subagent-6","older-subagent-7"]' of JSON.stringify([...document.querySelectorAll(".drawer-subagent-list .conversation-title")].map(e => e.textContent.trim()).sort()).`, func(s *shelley) {
		dayFilterSetup(s)
		c := s.conversation("echo: parent ready", "")
		s.v("PARENT", c.ID)
		s.v("PARENT_SLUG", c.Slug)
		parent := s.dayFilterFixture(s.dayFilterCorpus(), c.ID, map[string]any{
			"updated_at":     "2026-10-08T16:00:00Z",
			"subagent_count": 8,
		})
		convs := []map[string]any{parent}
		for i := range 8 {
			updated := "2026-10-06T16:00:00Z"
			if i%2 == 1 {
				updated = "2026-10-07T16:00:00Z"
			}
			convs = append(convs, dayFilterWith(parent, map[string]any{
				"conversation_id":         fmt.Sprintf("older-subagent-%d", i),
				"slug":                    fmt.Sprintf("older-subagent-%d", i),
				"parent_conversation_id":  c.ID,
				"user_initiated":          false,
				"updated_at":              updated,
				"subagent_count":          0,
				"working":                 false,
				"agent_working":           false,
				"running_background_jobs": 0,
			}))
		}
		s.route("/api/conversations/snapshot", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"conversations": convs, "hash": "cross-day-subagents"})
		})
		s.route("/api/stream2", abort)
	})
}

// dayFilterStatus records the status a request was answered with.
type dayFilterStatus struct {
	http.ResponseWriter
	code int
}

func (w *dayFilterStatus) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *dayFilterStatus) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func TestE2EDayFilterSendReturnsToToday(t *testing.T) {
	e2e(t, useful(0), `Daily returns to today only after sending in an older conversation. The page runs in New York with its clock fixed at 2026-10-08T16:00:00Z. The test lists exactly two conversations: {{OLDER}} (slug {{OLDER_SLUG}}), updated the day before, and {{TODAY}} (slug {{TODAY_SLUG}}), updated at that moment. The live update stream is held until call "release-list", after which it reports both as updated at that moment. The test fails the first message sent to {{OLDER}} with a 500 "daily send failed" and passes the rest to the server; call "chat-statuses" lists how each message sent to {{OLDER}} was answered: "500" for that failure, "ok" for a 2xx, else the status. `+dayFilterPicker+drawerDirect+drawerSteps+`1. Navigate to /c/{{TODAY_SLUG}}. 2. Click the button "Open conversations". 3. Group by Daily. 4. Click the picker's button "Previous day". 5. The date reads exactly "Yesterday". 6. Click the ".conversation-title" inside the row of {{OLDER}}. 7. The URL is exactly /c/{{OLDER_SLUG}} (assert_url value). 8. Click the button "Open conversations". 9. The date reads exactly "Yesterday", and the row of {{OLDER}} is visible. 10. Click the button "Close conversations". 11. Fill data-testid "message-input" with "Hello". 12. Click data-testid "send-button". 13. Call "chat-statuses" with expect '["500"]'. 14. The message input's value is still exactly "Hello" (assert_value). 15. Click the button "Open conversations". 16. The date reads exactly "Yesterday", and the row of {{OLDER}} is visible. 17. Click the button "Close conversations". 18. Click data-testid "send-button". 19. Call "chat-statuses" with expect '["500","ok"]'. 20. Click the button "Open conversations". 21. The date reads exactly "Today" and the count exactly "1". 22. The row of {{OLDER}} is not visible (assert_not_visible). 23. Call "release-list". 24. The row of {{OLDER}} is visible, and it has the class active. 25. The count reads exactly "2".`, func(s *shelley) {
		dayFilterSetup(s)
		older := s.conversation("day send older", "")
		today := s.conversation("day send today", "")
		s.v("OLDER", older.ID)
		s.v("OLDER_SLUG", older.Slug)
		s.v("TODAY", today.ID)
		s.v("TODAY_SLUG", today.Slug)
		corpus := s.dayFilterCorpus()
		fixtures := []map[string]any{
			s.dayFilterFixture(corpus, older.ID, map[string]any{"updated_at": "2026-10-07T16:00:00Z"}),
			s.dayFilterFixture(corpus, today.ID, map[string]any{"updated_at": "2026-10-08T16:00:00Z"}),
		}
		s.dayFilterList(fixtures, "before-send")
		var touched []map[string]any
		for _, c := range fixtures {
			touched = append(touched, dayFilterWith(c, map[string]any{"updated_at": "2026-10-08T16:00:00Z"}))
		}
		patch, err := json.Marshal(map[string]any{"conversation_list_patch": map[string]any{
			"reset":    true,
			"new_hash": "after-send",
			"at":       "2026-10-08T16:00:00Z",
			"patch":    []any{map[string]any{"op": "replace", "path": "", "value": touched}},
		}})
		s.check(err)
		release := s.gate("release-list")
		s.route("/api/stream2", func(w http.ResponseWriter, r *http.Request) {
			if !release.wait(r) {
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", patch)
		})
		var mu sync.Mutex
		statuses := []string{}
		s.route("/api/conversation/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") != older.ID {
				s.pass.ServeHTTP(w, r)
				return
			}
			mu.Lock()
			first := len(statuses) == 0
			if first {
				statuses = append(statuses, "pending")
			}
			mu.Unlock()
			if first {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, "daily send failed")
				mu.Lock()
				statuses[0] = "500"
				mu.Unlock()
				return
			}
			rec := &dayFilterStatus{ResponseWriter: w}
			s.pass.ServeHTTP(rec, r)
			status := strconv.Itoa(rec.code)
			if rec.code >= 200 && rec.code < 300 {
				status = "ok"
			}
			mu.Lock()
			statuses = append(statuses, status)
			mu.Unlock()
		})
		s.call("chat-statuses", func() string {
			mu.Lock()
			defer mu.Unlock()
			return mustJSON(statuses)
		})
	})
}

func TestE2EDayFilterPagesCountsCalendar(t *testing.T) {
	e2e(t, useful(0), `Daily grouping pages and counts last-touched chats and selects calendar dates. The page runs in New York with its clock fixed at 2026-10-08T16:00:00Z (noon there). The test lists exactly four conversations, which every search and the archive list (as archived) return too: {{TODAY}} updated at 00:00 that day in New York and {{ALSO_TODAY}} at 13:00, both tagged day-today; {{YESTERDAY}} updated at 23:59:59 the day before, tagged day-yesterday; and {{EARLIER}} updated on Tuesday, October 6, tagged day-earlier. The live update stream stays open and quiet. Call "mark-searches" notes how many searches were requested, and call "searched" says "true" once a search was requested since. `+dayFilterPicker+drawerQuery+drawerDirect+drawerSteps+`"The calendar" is the dialog named exactly "Choose last-touched day" (role dialog); its CSS selector is ".drawer-day-calendar". "Day N" is ".drawer-day-calendar [data-pc-section='daycell'][aria-label='N']:not([data-p-other-month='true']) [data-pc-section='day']". "The picker fits" means: eval with expect "true" of (() => { const el = document.querySelector("[data-testid='conversation-day-filter']"); return String(!!el && el.scrollWidth <= el.clientWidth); })(). "Check the group menu" means: eval with expect '["Tags","Daily"]' of JSON.stringify([...document.querySelectorAll(".group-by-menu-item")].slice(3, 5).map(e => e.textContent.trim())). In this test, to group by L, check the group menu between opening it and clicking L. 1. Navigate to /. 2. Click the button "Open conversations". 3. The picker is not visible (assert_not_visible). 4. Click the button "Search conversations...". 5. ".drawer-search-shell" is visible. 6. The search sits 8px below the header: eval with expect "8" of (() => { const el = document.querySelector(".drawer-search-shell"); const header = el.closest(".drawer").querySelector(".drawer-header"); return String(el.getBoundingClientRect().top - header.getBoundingClientRect().bottom); })(). 7. The picker is not visible. 8. Group by Daily. 9. The date reads exactly "Today" and the count exactly "2". 10. The row of {{TODAY}} is visible and the row of {{YESTERDAY}} is not visible. 11. The first row sits 4px below the search: eval with expect "4" of (() => { const el = [...document.querySelectorAll(".conversation-list .conversation-item")].find(e => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && getComputedStyle(e).visibility !== "hidden"; }); const search = el.closest(".drawer").querySelector(".drawer-search"); return String(el.getBoundingClientRect().top - search.getBoundingClientRect().bottom); })(). 12. The picker's button "Next day" is disabled. 13. Click the picker's button "Previous day". 14. The date reads exactly "Yesterday" and the count exactly "1"; the row of {{YESTERDAY}} is visible and the row of {{TODAY}} is not visible. 15. Click the button "Add tag filter". 16. "[data-testid='tag-filter-option'][data-tag='day-yesterday']" is visible, and there is no "[data-testid='tag-filter-option'][data-tag='day-today']". 17. Click "[data-testid='tag-filter-option'][data-tag='day-yesterday']". 18. Click the picker's button "Next day". 19. The count reads exactly "0". 20. Click the button "Clear search". 21. The count reads exactly "2". 22. Group by Directory. 23. The picker is not visible; the rows of {{TODAY}} and {{YESTERDAY}} are visible. 24. Group by Daily. 25. Call "mark-searches". 26. Set the query to "day paging". 27. Call "searched" with expect "true". 28. The count reads exactly "2". 29. Click the picker's button "Previous day". 30. The count reads exactly "1", and the row of {{YESTERDAY}} is visible. 31. Click the button "Clear search". 32. Click the button "View Archived". 33. The count reads exactly "1"; the row of {{YESTERDAY}} is visible and the row of {{TODAY}} is not visible. 34. Click the button "Back to conversations". 35. Click the picker's ".drawer-day-label". 36. The calendar is visible. 37. The ".drawer-calendar-count" of day 8 reads exactly "2", that of day 7 exactly "1" and that of day 6 exactly "1"; day 5 has no ".drawer-calendar-count" (assert_count 0). 38. There is no ".drawer-day-calendar .drawer-calendar-circle". 39. Day 7 is shaded: eval with expect "true" of (() => { const el = document.querySelector(".drawer-day-calendar [data-pc-section='daycell'][aria-label='7']:not([data-p-other-month='true']) [data-pc-section='day']"); return String(!!el && getComputedStyle(el).backgroundColor !== "rgba(0, 0, 0, 0)"); })(). 40. Day 9's aria-disabled is exactly "true". 41. Click day 6. 42. The calendar is not visible. 43. The date reads exactly "Tue, Oct 6" and the count exactly "1"; the row of {{EARLIER}} is visible. 44. The picker fits. 45. Click the picker's button "Previous day". 46. The count reads exactly "0"; data-testid "day-filter-empty" is visible; the picker's button "Next day" is enabled. 47. Click the picker's ".drawer-day-label". 48. The calendar is visible. 49. The ".drawer-calendar-count" of day 8 reads exactly "2" and that of day 7 exactly "1". 50. Click the button "Today" inside the calendar ({"selector": ".drawer-day-calendar", "role": "button", "name": "Today", "exact": true}). 51. The calendar is not visible. 52. The date reads exactly "Today" and the count exactly "2". 53. Click the picker's button "Previous day". 54. Click the picker's button "Today". 55. The date reads exactly "Today" and the count exactly "2"; the picker's button "Today" is not visible. 56. Click the button "Search conversations...". 57. The picker is visible. 58. Click the picker's button "Turn off Daily". 59. The picker is not visible; the rows of {{TODAY}} and {{YESTERDAY}} are visible. 60. Group by Daily. 61. The date reads exactly "Today" and the count exactly "2". 62. Reload the page (reload action). 63. Click the button "Open conversations". 64. The date reads exactly "Today" and the count exactly "2". 65. Set the viewport to 1280x800 (set_viewport). 66. The picker fits.`, func(s *shelley) {
		dayFilterSetup(s)
		names := []string{"TODAY", "ALSO_TODAY", "YESTERDAY", "EARLIER"}
		messages := []string{"day paging today", "day paging today too", "day paging yesterday", "day paging earlier"}
		var ids []string
		for i, name := range names {
			c := s.conversation(messages[i], "")
			s.v(name, c.ID)
			ids = append(ids, c.ID)
		}
		corpus := s.dayFilterCorpus()
		var fixtures, archived []map[string]any
		for i, f := range []struct{ updated, tag string }{
			{"2026-10-08T04:00:00Z", "day-today"},
			{"2026-10-08T17:00:00Z", "day-today"},
			{"2026-10-08T03:59:59Z", "day-yesterday"},
			{"2026-10-06T16:00:00Z", "day-earlier"},
		} {
			c := s.dayFilterFixture(corpus, ids[i], map[string]any{
				"created_at": "2025-01-01T00:00:00Z",
				"updated_at": f.updated,
				"tags":       mustJSON([]string{f.tag}),
			})
			fixtures = append(fixtures, c)
			archived = append(archived, dayFilterWith(c, map[string]any{"archived": true}))
		}
		s.dayFilterList(fixtures, "day-filter-fixture")
		var mu sync.Mutex
		searches, mark := 0, -1
		s.route("/api/conversations/search", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			searches++
			mu.Unlock()
			writeJSON(w, fixtures)
		})
		s.call("mark-searches", func() string {
			mu.Lock()
			defer mu.Unlock()
			mark = searches
			return "marked"
		})
		s.call("searched", func() string {
			mu.Lock()
			defer mu.Unlock()
			return strconv.FormatBool(mark >= 0 && searches > mark)
		})
		s.route("/api/conversations/archived", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, archived)
		})
		// A failing stream would put "Reconnecting…" above the search,
		// whose inset the test measures.
		s.route("/api/stream2", func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		})
	})
}
