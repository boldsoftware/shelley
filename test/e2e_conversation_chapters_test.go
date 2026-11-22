package test

import (
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/conversation-chapters.spec.ts.

// conversationChapters is the spec's test at one viewport width: the
// conversation's chapters come from a fake /themes endpoint whose one
// chapter call "retitle" renames.
func conversationChapters(t *testing.T, useful lazycue.Useful, width int) {
	e2e(t, useful, `At `+strconv.Itoa(width)+`px wide, the table of contents shows and refreshes chapters without a feature flag. The conversation /c/{{SLUG}} (ID {{ID}}) has one chapter, titled "Chapter navigation", anchored at its user message, whose data-message-id is {{ANCHOR}}. Call "retitle" renames the chapter to "Retrospective heading" on the server. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. Click the button whose accessible name is exactly "Conversation table of contents". 3. The chapter entry (".toc-entry-chapter") should have exactly the text "Chapter navigation". 4. Call "retitle". 5. Tell the page the chapters changed: eval window.dispatchEvent(new CustomEvent("shelley-chapters-updated", { detail: "{{ID}}" })). 6. The chapter entry should then have exactly the text "Retrospective heading". 7. Click the chapter entry. 8. The URL should become exactly /c/{{SLUG}}#m-{{ANCHOR8}} (an exact URL check, not a substring).`, func(s *shelley) {
		s.viewport(width, 800)
		c := s.conversation("echo: chapter navigation", "")
		var got struct {
			Messages []struct {
				ID   string `json:"message_id"`
				Type string `json:"type"`
			} `json:"messages"`
		}
		s.api("GET", "/api/conversation/"+c.ID, nil, &got)
		anchor := ""
		for _, m := range got.Messages {
			if m.Type == "user" {
				anchor = m.ID
				break
			}
		}
		if len(anchor) < 8 {
			s.fail("no user message in %s", c.ID)
		}
		s.v("SLUG", c.Slug)
		s.v("ID", c.ID)
		s.v("ANCHOR", anchor)
		s.v("ANCHOR8", anchor[:8])
		var mu sync.Mutex
		title := "Chapter navigation"
		s.route("/api/conversation/"+c.ID+"/themes", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			writeJSON(w, map[string]any{
				"chapters":           []map[string]any{{"title": title, "message_id": anchor}},
				"through_message_id": anchor,
			})
		})
		s.call("retitle", func() string {
			mu.Lock()
			defer mu.Unlock()
			title = "Retrospective heading"
			return "retitled"
		})
	})
}

func TestE2EConversationChaptersAt390(t *testing.T)  { conversationChapters(t, useful(0), 390) }
func TestE2EConversationChaptersAt1280(t *testing.T) { conversationChapters(t, useful(0), 1280) }
