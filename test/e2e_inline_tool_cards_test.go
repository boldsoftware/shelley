package test

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
)

// Ported from ui/e2e/inline-tool-cards.spec.ts.

// Tool calls always render as full-width inline cards, and the VM's
// reflection emoji is always the favicon. The two flags that used to gate
// those choices ("tool-pills", "reflection-emoji-favicon") are gone; these
// tests pin that removal down from the outside so neither can come back by
// accident.

func TestE2EInlineToolCardsRemovedFlagsAbsent(t *testing.T) {
	e2e(t, `The removed feature flags "tool-pills" and "reflection-emoji-favicon" are absent from the API and the flags modal. Perform the steps in order.
1. Call "flags": it reads the server's feature flag registry (GET /feature-flags) and must return exactly "served=true tool-pills=absent reflection-emoji-favicon=absent" (the registry is non-empty, so the absence means something, and neither removed flag is in it).
2. Navigate to /c/{{SLUG}} and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
3. Press Control+k to open the command palette. The command palette's search box (".command-palette-input") becomes visible.
4. Fill the search box with "feature", then click the FIRST ".command-palette-item".
5. The modal title (".modal-title") has exactly the text "Feature flags".
6. The first ".feature-flag-name" element is visible.
7. In an eval with expect "true", collect the textContent of every ".feature-flag-name" element into a list; the list must be non-empty and must not include an entry equal to "tool-pills" or to "reflection-emoji-favicon".`, func(s *shelley) {
		s.v("SLUG", s.conversation("hello", "").Slug)
		s.call("flags", func() string {
			var flags []struct {
				Name string `json:"name"`
			}
			s.check(json.Unmarshal(s.do("GET", "/feature-flags", nil), &flags))
			names := make([]string, len(flags))
			for i, f := range flags {
				names[i] = f.Name
			}
			state := func(name string) string {
				if slices.Contains(names, name) {
					return "present"
				}
				return "absent"
			}
			return fmt.Sprintf("served=%t tool-pills=%s reflection-emoji-favicon=%s", len(flags) > 0, state("tool-pills"), state("reflection-emoji-favicon"))
		})
	})
}

// A stale localStorage override from before the removal must not resurrect
// the pill row: nothing reads the flag any more, so a burst of consecutive
// tool calls still renders as inline cards.
func TestE2EInlineToolCardsStaleToolPillsOverride(t *testing.T) {
	e2e(t, `A stale "tool-pills" feature flag override still renders bursts of tool calls as inline cards. The page's localStorage already holds the override ff:tool-pills = "true" (do not set it yourself). The conversation at /c/{{ID}} is a generated one whose turns each contain several consecutive tool calls: the burst shape that used to collapse into one pill row. "The cards" are the elements matching '[data-testid="tool-call-completed"], [data-testid="tool-call-running"]', in document order. Perform the steps in order.
1. Navigate to /c/{{ID}} and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. The override really is in place: localStorage.getItem("ff:tool-pills") is exactly "true".
3. Wait until there are more than 1 cards (allow 30s): every tool call is an inline card (or its cheap geometry placeholder, for the ones far offscreen).
4. There are no ".tool-pill", no ".tool-pills-row" and no ".tool-detail-modal" elements (count 0 each).
5. Two tool calls from the same burst sit next to each other in the stream, each as its own full-width card rather than side by side in a row: in an eval with expect "true", each of the first two cards has getBoundingClientRect().width greater than 0.5 times the messages container's (".messages-container") clientWidth.`, func(s *shelley) {
		s.pageFlag("tool-pills", true)
		s.v("ID", s.loremipsum("12").ID)
	})
}

// A tool whose whole point is its output (a patch) shows it without a click,
// and a collapsed card opens in place: no modal is involved in reading a tool
// result any more.
func TestE2EInlineToolCardsOutputReadInline(t *testing.T) {
	e2e(t, `Tool output is read inline, never in a modal. The conversation at /c/{{PATCH}} applied a patch to test-patch-success.txt; the conversation at /c/{{BASH}} ran the bash command "echo inline-only". Perform the steps in order.
1. Navigate to /c/{{PATCH}}. "The patch card" is the first '.patch-tool[data-testid="tool-call-completed"]' element whose text contains "test-patch-success.txt". Wait for it to be visible (allow 30s).
2. It is expanded on arrival, with no click: the patch card's ".patch-tool-details" element is visible.
3. Navigate to /c/{{BASH}}. "The bash card" is the first '.bash-tool[data-testid="tool-call-completed"]' element. Wait for it to be visible (allow 30s).
4. Click the bash card's ".bash-tool-header". The bash card's ".bash-tool-details" element becomes visible and its text contains "inline-only".
5. It expanded in place, inside the card, with no modal opened: there are no ".modal" and no ".tool-detail-modal" elements (count 0 each).`, func(s *shelley) {
		s.v("PATCH", s.conversation("patch success", "").Slug)
		s.v("BASH", s.conversation("bash: echo inline-only", "").Slug)
	})
}
