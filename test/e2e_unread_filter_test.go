package test

import (
	"maps"
	"net/http"
	"sync"
	"testing"
)

// Ported from ui/e2e/unread-filter.spec.ts, which ran in a 1280x900
// viewport.

// unreadMarkRead marks conversation id read up to its last attention, as
// opening it would.
func (s *shelley) unreadMarkRead(id string) {
	var snapshot struct {
		Conversations []map[string]any `json:"conversations"`
	}
	s.api("GET", "/api/conversations/snapshot", nil, &snapshot)
	for _, c := range snapshot.Conversations {
		if c["conversation_id"] == id {
			s.api("POST", "/api/conversation/"+id+"/read", map[string]any{"read_at": c["last_attention_at"]}, nil)
			return
		}
	}
	s.fail("conversation %s is not in the snapshot", id)
}

// unreadWith is a copy of the listing c with fields overridden.
func unreadWith(c, fields map[string]any) map[string]any {
	c = maps.Clone(c)
	maps.Copy(c, fields)
	return c
}

// How the spec's tests drive the unread filter.
const unreadFilter = `Conversation rows are ".conversation-item[data-conversation-id='<id>']" ("the row of <id>"). "Activity" is data-testid "activity-filter". Open it to see the checkbox labeled "Unread or working" (data-testid "unread-toggle"); assert its state by evaluating its checked property. To "close Activity", focus the checkbox (eval its focus()), press Escape and wait for the checkbox to be hidden; changing it does not close the popover. `

// Playwright loaded /new before making the conversations and reloaded
// after; here the conversations come first and the page loads once. The
// user's first read of the receipts (the page's event stream, or a listing)
// starts them, and everything older counts as read, so a listing comes
// first, as the page's stream did.
func TestE2EUnreadFilterTogglesAndCombines(t *testing.T) {
	e2e(t, useful(0), `unread filter toggles, combines with text and tags, and updates on read. Conversations {{UNREAD}} and {{READ}} both say "echo: unread filter needle" and are tagged unread-filter; {{READ}} has been read and {{UNREAD}} not. Call "read-unread" marks {{UNREAD}} read. The viewport is 1280x900, where the drawer stays open. `+unreadFilter+drawerQuery+drawerDirect+drawerSteps+`1. Navigate to /new and wait for data-testid "message-input". The row of {{UNREAD}} has the class conversation-item-unread and the row of {{READ}} is visible. 2. Click the button "Search conversations...". The checkbox is not visible. 3. Open Activity and assert the checkbox is unchecked. Click it and assert it is checked. Close Activity. 4. The query is "is:unread "; the row of {{UNREAD}} is visible and {{READ}} is not. 5. Open Activity and click the button "Past day" scoped to ".drawer-bulk-popover" and wait for the checkbox to be hidden. The activity token ([data-conversation-query-token="activity"]) is visible and {{READ}} is still hidden. 6. Set the query to "is:unread tag:unread-filter needle". The row of {{UNREAD}} is visible and {{READ}} is not. 7. Open Activity, click the checkbox to turn it off, assert it is unchecked, then close Activity. The query is "tag:unread-filter needle ", the activity token stays visible and {{READ}} is visible. 8. Open Activity, check the checkbox, then close Activity. {{READ}} is not visible. 9. Call "read-unread". The row of {{UNREAD}} is not visible. 10. Click "[data-testid='conversation-query-editor'] [data-conversation-query-token='unread'] .conversation-query-token-delete". Both rows are visible. Open Activity and assert the checkbox is unchecked.`, func(s *shelley) {
		s.viewport(1280, 900)
		s.api("GET", "/api/conversations", nil, nil)
		unread := s.conversation("echo: unread filter needle", "")
		read := s.conversation("echo: unread filter needle", "")
		s.unreadMarkRead(read.ID)
		for _, c := range []convo{unread, read} {
			s.tagFilterSetTags(c.ID, "unread-filter")
		}
		s.v("UNREAD", unread.ID)
		s.v("READ", read.ID)
		s.call("read-unread", func() string {
			s.unreadMarkRead(unread.ID)
			return "read"
		})
	})
}

func TestE2EUnreadFilterKeepsWorking(t *testing.T) {
	e2e(t, useful(0), `the unread filter also keeps read chats with foreground, background, or delegated work. The viewport is 1280x900, where the drawer stays open. The test lists only conversation {{ID}}, as read; the live update stream is cut off. What else it lists depends on the activity the test sets with calls: after "activity-foreground" (the setting at first) {{ID}} is working; after "activity-background" it is idle but runs a background job; after "activity-delegated" it is idle but has a working subagent; after "activity-idle" it is idle. The activity applies from the next page load on. `+unreadFilter+drawerDirect+drawerSteps+`"Filter at A" means: call "activity-A", navigate to /new, click the button "Search conversations...", then open Activity and click the checkbox. 1. Filter at foreground. 2. The row of {{ID}} is visible, and it does not have the class conversation-item-unread. 3. Filter at background. 4. The row of {{ID}} is visible, and it does not have the class conversation-item-unread. 5. Filter at delegated. 6. The row of {{ID}} is visible, and it does not have the class conversation-item-unread. 7. Filter at idle. 8. The row of {{ID}} is not visible (assert_not_visible), and data-testid "unread-filter-empty" is visible.`, func(s *shelley) {
		s.viewport(1280, 900)
		c := s.conversation("echo: working filter", "")
		s.v("ID", c.ID)
		var mu sync.Mutex
		activity := "foreground"
		for _, a := range []string{"foreground", "background", "delegated", "idle"} {
			s.call("activity-"+a, func() string {
				mu.Lock()
				defer mu.Unlock()
				activity = a
				return a
			})
		}
		s.route("/api/stream2", abort)
		s.route("/api/conversations/snapshot", func(w http.ResponseWriter, r *http.Request) {
			var snapshot map[string]any
			if err := s.fetchJSON(r, &snapshot); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			convs, _ := snapshot["conversations"].([]any)
			var parent map[string]any
			for _, x := range convs {
				if m, ok := x.(map[string]any); ok && m["conversation_id"] == c.ID {
					parent = m
				}
			}
			if parent == nil {
				http.Error(w, "conversation not in the snapshot", http.StatusBadGateway)
				return
			}
			mu.Lock()
			a := activity
			mu.Unlock()
			jobs := 0
			if a == "background" {
				jobs = 1
			}
			parent = unreadWith(parent, map[string]any{
				"last_attention_at":       0,
				"working":                 a == "foreground",
				"running_background_jobs": jobs,
			})
			listed := []any{parent}
			if a == "delegated" {
				listed = append(listed, unreadWith(parent, map[string]any{
					"conversation_id":        "working-filter-child",
					"parent_conversation_id": c.ID,
					"user_initiated":         false,
					"working":                true,
				}))
			}
			snapshot["conversations"] = listed
			writeJSON(w, snapshot)
		})
	})
}
