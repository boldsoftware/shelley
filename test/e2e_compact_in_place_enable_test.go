package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/compact-in-place-enable.spec.ts.

// compactInPlaceDevice is the spec's 1280x800 window with no touch screen.
var compactInPlaceDevice = lazycue.Device{Width: 1280, Height: 800, Scale: lazycue.Pixel5.Scale}

// compactInPlaceConversation starts a conversation at {{SLUG}} with
// message, with the compact_in_place tool off unless on.
func (s *shelley) compactInPlaceConversation(message string, on bool) convo {
	body := map[string]any{"message": message, "model": "predictable", "cwd": s.cwd}
	if !on {
		body["conversation_options"] = map[string]any{"tool_overrides": map[string]string{"compact_in_place": "off"}}
	}
	c := s.conversationWith(body, "")
	s.v("SLUG", c.Slug)
	return c
}

// Compact in Place is offered even in a conversation started with the
// compact_in_place tool off: the button enables it (without the context
// nudges), marks that in the log, and then asks the agent to compact.

func TestE2ECompactInPlaceEnableEnablesThenAsks(t *testing.T) {
	e2e(t, `Compact in Place, in a conversation started with the compact_in_place tool off, enables the tool and then asks the agent to compact. Perform the steps in order.
1. Navigate to /c/{{SLUG}}.
2. Click the ".context-usage-label" element, then click the [data-testid="compact-in-place-button"] button.
3. The [data-testid="message-modelchange"] element has the aria-label exactly "Enabled the compact_in_place tool." and its text contains "+compact_in_place".
4. An element with the text "Compact your context in place." is visible.
5. Call "options": it reads the conversation's stored options and must return exactly "compact_in_place=on disable_compact_nudges=true" (the tool is on, without the context nudges).`, func(s *shelley) {
		s.device(compactInPlaceDevice)
		c := s.compactInPlaceConversation("echo: compact in place later", false)
		s.call("options", func() string {
			var got struct {
				Conversation struct {
					Options string `json:"conversation_options"`
				} `json:"conversation"`
			}
			s.api("GET", "/api/conversation/"+c.ID, nil, &got)
			var opts struct {
				ToolOverrides        map[string]string `json:"tool_overrides"`
				DisableCompactNudges any               `json:"disable_compact_nudges"`
			}
			if err := json.Unmarshal([]byte(got.Conversation.Options), &opts); err != nil {
				return fmt.Sprintf("bad options %q: %v", got.Conversation.Options, err)
			}
			return fmt.Sprintf("compact_in_place=%s disable_compact_nudges=%v", opts.ToolOverrides["compact_in_place"], opts.DisableCompactNudges)
		})
	})
}

func TestE2ECompactInPlaceEnableWaitsForTurnEnd(t *testing.T) {
	e2e(t, `Compact in Place, in a conversation started with the compact_in_place tool off, waits for the current turn to end. The agent is busy running "sleep 30" in the conversation at /c/{{SLUG}}. Perform the steps in order.
1. Navigate to /c/{{SLUG}}.
2. Click the ".context-usage-label" element.
3. The [data-testid="compact-in-place-button"] button is disabled, and an element with the text "Finish or stop the current turn to compact in place." is visible.
4. Call "cancel": it cancels the agent's turn through the API.
5. The [data-testid="compact-in-place-button"] button becomes enabled.`, func(s *shelley) {
		s.device(compactInPlaceDevice)
		c := s.compactInPlaceConversation("echo: busy", false)
		s.sendChat(c.ID, "bash: sleep 30")
		s.call("cancel", func() string {
			s.do("POST", "/api/conversation/"+c.ID+"/cancel", nil)
			return "cancelled"
		})
	})
}

// With the tool on, as it is by default, the button just asks the agent.
func TestE2ECompactInPlaceEnableWithToolAsks(t *testing.T) {
	e2e(t, `Compact in Place, in a conversation with the compact_in_place tool on (the default), just asks the agent. Perform the steps in order.
1. Navigate to /c/{{SLUG}}.
2. Click the ".context-usage-label" element, then click the [data-testid="compact-in-place-button"] button.
3. An element with the text "Compact your context in place." is visible.
4. Call "enable requests": it returns how many requests the page sent whose URL contains "enable-compact-in-place"; it must return exactly "0".
5. There is no [data-testid="message-modelchange"] element (count 0).`, func(s *shelley) {
		s.device(compactInPlaceDevice)
		s.compactInPlaceConversation("echo: compact now", true)
		s.call("enable requests", func() string {
			n := 0
			for _, r := range s.requestLog() {
				if strings.Contains(r, "enable-compact-in-place") {
					n++
				}
			}
			return fmt.Sprint(n)
		})
	})
}

// /compact runs the same in-place request, with any instructions appended
// after a blank line.
func TestE2ECompactInPlaceEnableSlashCompact(t *testing.T) {
	e2e(t, `The /compact command asks the agent to compact in place, with its instructions appended after a blank line. Perform the steps in order.
1. Navigate to /c/{{SLUG}}.
2. Call "chat": it returns the message of the first chat request (POST /api/conversation/<id>/chat) the page sent, as a JSON string, or "none" before there is one. It must return exactly "none" now.
3. Fill the message input ([data-testid="message-input"]) with "/compact keep the API notes" and press Enter in it.
4. Call "chat" again: it must return exactly "Compact your context in place.\n\nkeep the API notes" (a JSON string, quotes included, with the two newlines written as \n).
5. An element with the text "keep the API notes" is visible.`, func(s *shelley) {
		s.device(compactInPlaceDevice)
		s.compactInPlaceConversation("echo: slash compact", true)
		var mu sync.Mutex
		first := "none"
		s.route("POST /api/conversation/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
			msg, _ := object(readBody(r))["message"].(string)
			mu.Lock()
			if first == "none" {
				first = mustJSON(msg)
			}
			mu.Unlock()
			s.pass.ServeHTTP(w, r)
		})
		s.call("chat", func() string {
			mu.Lock()
			defer mu.Unlock()
			return first
		})
	})
}
