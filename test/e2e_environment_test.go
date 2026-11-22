package test

import (
	"os"
	"path/filepath"
	"testing"
)

// Ported from ui/e2e/test-environment.spec.ts. Each test's server is its own
// by construction, so the spec's check that Playwright workers did not share
// one has no counterpart.

// Editor tests write AGENTS.md in HOME, and every new conversation scans cwd
// for guidance and skills. Neither may use the runner's shared HOME or /tmp.
func TestE2EEnvironmentIsolatesHomeAndCwd(t *testing.T) {
	e2e(t, useful(0), `Navigate to /new and wait for the message input (data-testid "message-input"). The page's window.__SHELLEY_INIT__ should have default_cwd "{{CWD}}", home_dir "{{HOME}}" and user_agents_md_path "{{HOME}}/.config/shelley/AGENTS.md". The conversation the test created should be in that directory: call "conversation cwd" should return "{{CWD}}".`, func(s *shelley) {
		c := s.conversation("Hello", "")
		s.call("conversation cwd", func() string {
			var got struct {
				Conversation struct {
					Cwd string `json:"cwd"`
				} `json:"conversation"`
			}
			s.api("GET", "/api/conversation/"+c.ID, nil, &got)
			return got.Conversation.Cwd
		})
	})
}

// /new otherwise defaults to the most recent conversation's cwd, which may be
// a directory that is gone by now.
func TestE2EEnvironmentNewIgnoresDeletedDirectory(t *testing.T) {
	e2e(t, useful(0), `Navigate to /new, fill the message input (data-testid "message-input") with "echo: still here" and click the send button (data-testid "send-button"). The first agent message (".messages-container .message-agent") should contain "still here".`, func(s *shelley) {
		gone := s.mkdir(filepath.Join(s.dir, "gone"))
		s.conversationWith(map[string]any{"message": "Hello", "model": "predictable", "cwd": gone}, "")
		s.check(os.RemoveAll(gone))
	})
}
