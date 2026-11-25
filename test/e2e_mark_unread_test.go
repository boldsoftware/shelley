package test

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// Both conversations come before the page's first visit, so they start read.
func TestE2EMarkUnread(t *testing.T) {
	drawerTest(t, useful(0), `Mark as unread keeps a chat bold until it is opened again, and a chat marked while open stays unread in its focused tab. Conversations {{A}} and {{B}} exist and have been read. Call "unread A" (or "unread B") returns whether the server lists that conversation as unread for this user, "true" or "false". Call "mark B" marks {{B}} unread from another device. Call "reply B" sends {{B}} the message "echo: newer reply N", N counting from 1, and returns once its agent has answered. "The row of X" is ".conversation-item[data-conversation-id=\"X\"]", and "X is bold" means the row of X has the class "conversation-item-unread". The context menu is data-testid "conversation-context-menu"; its items are its elements with role menuitem (a "selector" scoping a "role"), and its item "X" is the one named exactly X. The chat's menu opens with the button "More options"; its "Mark as unread" is data-testid "mark-unread". `+drawerSteps+drawerDirect+`1. Navigate to /new and wait for data-testid "message-input". The rows of {{A}} and {{B}} are visible, and neither is bold. 2. Right-click the row of {{A}} (center). The context menu's items' trimmed texts are exactly ["Archive","Rename","Edit tags","Mark as unread"]. Click its item "Mark as unread". 3. There is no context menu element. {{A}} is bold. Call "unread A" expecting "true". 4. Right-click the row of {{A}} (center). The context menu's items' trimmed texts are exactly ["Archive","Rename","Edit tags"]. Press Escape. 5. Click the row of {{B}}. The URL ends with /c/{{B_SLUG}}, and {{B}} is not bold. 6. Click the button "More options", then data-testid "mark-unread". 7. {{B}} is bold. Call "unread B" expecting "true". Click the button "More options": ".overflow-menu-panel" is visible, and there is no data-testid "mark-unread". Press Escape. 8. Call "reply B". The row of {{B}}'s ".conversation-preview" comes to contain the text "newer reply 1". {{B}} is still bold, and call "unread B" expecting "true". 9. Click the row of {{A}}. {{A}} comes to be not bold, and call "unread A" until it returns "false". {{B}} is still bold. 10. Click the row of {{B}}. {{B}} comes to be not bold, and call "unread B" until it returns "false". 11. Call "mark B". {{B}} comes to be bold. 12. Call "reply B". The row of {{B}}'s ".conversation-preview" comes to contain the text "newer reply 2". {{B}} is still bold, and call "unread B" expecting "true".`, func(s *shelley) {
		a := s.conversation("echo: mark unread A", "")
		b := s.conversation("echo: mark unread B", "")
		s.v("A", a.ID)
		s.v("B", b.ID)
		s.v("B_SLUG", b.Slug)
		for name, id := range map[string]string{"A": a.ID, "B": b.ID} {
			s.call("unread "+name, func() string {
				var rows []struct {
					ID     string `json:"conversation_id"`
					Unread bool   `json:"unread"`
				}
				s.api("GET", "/api/conversations", nil, &rows)
				for _, row := range rows {
					if row.ID == id {
						return fmt.Sprint(row.Unread)
					}
				}
				s.fail("conversation %s is not listed", id)
				return ""
			})
		}
		s.call("mark B", func() string {
			s.api("POST", "/api/conversation/"+b.ID+"/unread", nil, nil)
			return "marked"
		})
		turns := func() int {
			var got struct {
				Messages []apiMessage `json:"messages"`
			}
			s.api("GET", "/api/conversation/"+b.ID, nil, &got)
			n := 0
			for _, m := range got.Messages {
				if m.endsTurn() {
					n++
				}
			}
			return n
		}
		replies := 0
		s.call("reply B", func() string {
			replies++
			before := turns()
			s.api("POST", "/api/conversation/"+b.ID+"/chat", map[string]any{"message": fmt.Sprintf("echo: newer reply %d", replies), "model": "predictable"}, nil)
			s.poll(30*time.Second, func() error {
				if turns() == before {
					return errors.New("no reply yet")
				}
				return nil
			})
			return "replied"
		})
	})
}
