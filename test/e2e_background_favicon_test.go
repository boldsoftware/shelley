package test

import (
	"errors"
	"testing"
	"time"
)

func TestE2EBackgroundTabFaviconResetsWithoutAnimationFrames(t *testing.T) {
	e2e(t, useful(1), `When a background conversation finishes, its favicon becomes ready even while animation frames are suspended. The test seeded a conversation at /c/{{SLUG}}. "the icon" is 'link[rel="icon"]'. Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait up to 30s for [data-testid="message-input"] to be visible.
2. Fill the message input with "delay: 120" and click [data-testid="send-button"].
3. Wait until the icon's href contains "opacity%3D%220.35%22" (up to 15s), then wait two animation frames using eval new Promise(r => requestAnimationFrame(() => requestAnimationFrame(() => r('done')))) with expect "done".
4. Eval (() => { window.requestAnimationFrame = () => 0; window.cancelAnimationFrame = () => {}; })().
5. Call "working" and expect "true": the server snapshot confirms the turn is still working before cancellation. Call "finishTurn" and expect "ok". Call "idle" and expect "true".
6. While animation frames remain suspended, the icon must be present with a non-empty href that no longer contains "opacity%3D%220.35%22".`, func(s *shelley) {
		c := s.conversation("echo: favicon seed", "background-favicon")
		s.v("SLUG", c.Slug)
		s.call("working", func() string {
			s.waitConversationWorking(c.ID, true)
			return "true"
		})
		s.call("finishTurn", func() string {
			return okStatus(s.postStatus("/api/conversation/"+c.ID+"/cancel", nil))
		})
		s.call("idle", func() string {
			s.waitConversationWorking(c.ID, false)
			return "true"
		})
	})
}

func (s *shelley) waitConversationWorking(id string, want bool) {
	s.poll(15*time.Second, func() error {
		var snapshot struct {
			Conversations []struct {
				ID      string `json:"conversation_id"`
				Working bool   `json:"working"`
			} `json:"conversations"`
		}
		s.api("GET", "/api/conversations/snapshot", nil, &snapshot)
		for _, conversation := range snapshot.Conversations {
			if conversation.ID == id && conversation.Working == want {
				return nil
			}
		}
		return errors.New("conversation working state has not reached expected value")
	})
}
