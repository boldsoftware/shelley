package test

import (
	"regexp"
	"strconv"
	"testing"

	"github.com/google/uuid"
)

// Ported from ui/e2e/compaction-events.spec.ts.

const (
	compactionIndex       = "## Index\n#4–#12  earlier exploration\n#15–#20  implementation"
	compactionFullSummary = "Kept the decisions and current work.\nOriginal messages are still visible."
)

var compactionData = map[string]any{
	"squishes": []any{
		map[string]any{"from_sequence_id": 4, "to_sequence_id": 12, "summary": compactionFullSummary},
		map[string]any{"from_sequence_id": 15, "to_sequence_id": 20, "summary": "Preserved the implementation plan."},
	},
	"trims": []any{
		map[string]any{"sequence_id": 7, "tool_use_id": "first-output"},
		map[string]any{"sequence_id": 18, "tool_use_id": "second-output"},
	},
	"hidden_sequence_ids": []int{21},
	"hidden_tool_use_ids": []string{"compact-call"},
}

// compactionReplay serves a conversation, at /c/{{SLUG}}, that ends with a
// context nudge, the agent's compact_in_place call, its result and the
// in-place compaction event (data), with the live stream cut off.
// {{RESULT_ID}} is the compaction event's message ID and {{FRAGMENT}} the URL
// fragment that links to it.
func (s *shelley) compactionReplay(data map[string]any) {
	c := s.conversation("echo: compaction events fixture", "")
	body := s.conversationBody(c.ID)
	ms := messagesOf(body)
	base := ms[len(ms)-1]
	seq := base["sequence_id"].(float64)
	message := func(typ string, n int, llm, userData any) map[string]any {
		m := make(map[string]any, len(base))
		for k, v := range base {
			m[k] = v
		}
		m["message_id"] = uuid.NewString()
		m["sequence_id"] = seq + float64(n)
		m["type"] = typ
		m["llm_data"] = nil
		if llm != nil {
			m["llm_data"] = mustJSON(llm)
		}
		m["user_data"] = nil
		if userData != nil {
			m["user_data"] = mustJSON(userData)
		}
		m["end_of_turn"] = false
		return m
	}
	body["conversation"].(map[string]any)["agent_working"] = false
	result := message("inplacecompaction", 4, nil, data)
	body["messages"] = append(body["messages"].([]any),
		message("user", 1, map[string]any{
			"Role": 1,
			"Content": []any{
				map[string]any{"ID": "size", "Type": 2, "Text": "Context is 170k."},
				map[string]any{"ID": "index", "Type": 2, "Text": compactionIndex},
			},
		}, map[string]any{"context_nudge": true}),
		message("agent", 2, map[string]any{
			"Role": 2,
			"Content": []any{map[string]any{
				"ID": "compact-call", "Type": 5, "ToolName": "compact_in_place",
				"ToolInput": map[string]any{
					"action":   "compact",
					"collapse": []any{map[string]any{"from": "4", "to": "12", "note": compactionFullSummary}},
					"trim":     []string{"7"},
				},
			}},
		}, nil),
		message("tool", 3, map[string]any{
			"Role": 1,
			"Content": []any{map[string]any{
				"ID": "compact-result", "Type": 6, "ToolUseID": "compact-call",
				"ToolResult": []any{map[string]any{"ID": "result", "Type": 2, "Text": "Compacted: 2 collapsed, 2 trimmed"}},
			}},
		}, nil),
		result,
	)
	s.route("GET /api/stream2", abort)
	s.serveConversation(c.ID, body)
	id := result["message_id"].(string)
	s.v("SLUG", c.Slug)
	s.v("RESULT_ID", id)
	s.v("FRAGMENT", "m-"+regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(id, "")[:8])
}

const compactionSetup = `The conversation at /c/{{SLUG}} is served as a replay (its live stream is cut off) that ends with a context nudge, the agent's compact_in_place tool call, its result and the in-place compaction event. "The result" is the [data-testid="message-inplacecompaction"] element (the compaction event). Perform the steps in order. `

func compactionTOC(t *testing.T, width int) {
	w := strconv.Itoa(width)
	e2e(t, compactionSetup+`The compaction's table of contents entry navigates to its status at a `+w+`px-wide window. "The TOC" is the ".toc-popover" element.
1. Navigate to /c/{{SLUG}}.
2. The result's data-message-id attribute is exactly "{{RESULT_ID}}".
3. Click the button with role "button" and accessible name exactly "Conversation table of contents".
4. The TOC has exactly 1 ".toc-entry-compaction" element ("the entry"), and the entry's accessible name is exactly "Compacted context".
5. The TOC has exactly 1 ".toc-entry-user" element, and the TOC's text does not contain "Context is 170k.".
6. Click the entry. The TOC becomes hidden, and the URL ends with "#{{FRAGMENT}}" (wait_url with that exact ending; nothing may follow it).
7. The result's class list contains "message-highlight"; the result is in the viewport; the result has no "open" attribute.
8. Reload the page. The result's class list contains "message-highlight" again, and the result is in the viewport.`, func(s *shelley) {
		s.viewport(width, 844)
		s.compactionReplay(compactionData)
	})
}

func TestE2ECompactionEventsTOCEntry390(t *testing.T) { compactionTOC(t, 390) }

func TestE2ECompactionEventsTOCEntry1280(t *testing.T) { compactionTOC(t, 1280) }

func TestE2ECompactionEventsNoticesOpenDirectly(t *testing.T) {
	e2e(t, compactionSetup+`Compaction notices open their index and audit table directly. "The nudge" is the [data-testid="message-context-nudge"] element and "the notice" is its direct child summary element (":scope > summary", i.e. selector '[data-testid="message-context-nudge"] > summary'); "the index" is the nudge's ".msg-context-nudge-index". "The status" is the result's direct child summary ('[data-testid="message-inplacecompaction"] > summary'); "the table" is the element inside the result with role "table" and accessible name "Compacted messages"; "the bookkeeping" is the element inside the result whose text contains "Hid 1 context nudge and 1 compaction call from the agent.".
1. Navigate to /c/{{SLUG}}.
2. The notice has exactly the text "📏 Compaction threshold reached · 170k tokens", and the index is hidden.
3. Click the notice at x=5, y=10 from its top-left corner (the row's empty left edge, not its text or chevron). The index becomes visible, and its text is exactly this (three lines; compare the raw textContent in an eval with expect): "## Index\n#4–#12  earlier exploration\n#15–#20  implementation" (with real newlines and two spaces after each range).
4. Focus the notice (in an eval: .focus()) and press Enter. The index becomes hidden.
5. "The tool" is the ".tool" element whose text contains "Compacted: 2 collapsed, 2 trimmed": it is visible, and its ".tool-details" is hidden.
6. The status has exactly the text "Compacted context · 2 groups, 2 tool outputs", and the table is hidden.
7. Click the status at x=5, y=10 from its top-left corner. The table becomes visible.
8. The table's column headers (role "columnheader" inside the table) have exactly the texts "Messages", "Summary", in order. The table's thead is visible. The first "tbody tr" of the table has computed display exactly "table-row". The table has exactly 2 "tbody tr" elements.
9. The result contains no "details" element (count 0).
10. The table's first ".msg-compaction-summary-text" is visible, and its raw textContent is exactly "Kept the decisions and current work.\nOriginal messages are still visible." (with a real newline; check in an eval with expect). The table's second ".msg-compaction-summary-text" is visible.
11. The result has the attribute "open" with the value "" (it is present and empty).
12. The result's ".msg-compaction-ids" is visible and has exactly the text "#7, #18".
13. The bookkeeping is visible.
14. Focus the status (in an eval: .focus()) and press Enter. The table becomes hidden, the result's ".msg-compaction-ids" becomes hidden, and the bookkeeping becomes hidden.`, func(s *shelley) {
		s.viewport(1280, 800)
		s.compactionReplay(compactionData)
	})
}

const compactionNoOverflow = `Neither the page nor the compaction result overflows horizontally. The page clips its own overflow (html has overflow hidden), so check the result too: in one eval with expect "true", with R the [data-testid="message-inplacecompaction"] element, document.documentElement.scrollWidth <= window.innerWidth && R.scrollWidth <= R.clientWidth && R.getBoundingClientRect().right <= window.innerWidth.`

func TestE2ECompactionEventsMobileSingularLongSummary(t *testing.T) {
	summary := ""
	for range 200 {
		summary += "long-summary-"
	}
	e2e(t, compactionSetup+`The compaction event handles singular counts and long summaries on a phone. The compaction has one group, whose summary is "long-summary-" repeated 200 times, and one trimmed tool output (#7). "The table" is the result's ".msg-compaction-table"; "the cells" are the "td" elements of the table's first "tbody tr".
1. Navigate to /c/{{SLUG}}.
2. The status ('[data-testid="message-inplacecompaction"] > summary') has exactly the text "Compacted context · 1 group, 1 tool output".
3. Click the status. The result then contains no "details" element (count 0).
4. The table's thead has computed clip-path exactly "inset(50%)" (visually hidden), and the table's column headers (role "columnheader" inside the table) have exactly the texts "Messages", "Summary", in order.
5. The first and second cells each have computed display exactly "block".
6. In one eval with expect "true", using getBoundingClientRect() of the first cell (header) and the second cell (content): content.y >= header.y + header.height, content.x === header.x and content.width === header.width (the cells stack).
7. The result's ".msg-compaction-summary-text" is visible, and its text is exactly "long-summary-" repeated 200 times (compare with an eval that has expect "true": textContent === 'long-summary-'.repeat(200)).
8. The result's ".msg-compaction-ids" is visible and has exactly the text "#7".
9. `+compactionNoOverflow, func(s *shelley) {
		s.viewport(390, 844)
		s.compactionReplay(map[string]any{
			"squishes": []any{map[string]any{"from_sequence_id": 4, "to_sequence_id": 12, "summary": summary}},
			"trims":    []any{map[string]any{"sequence_id": 7, "tool_use_id": "first-output"}},
		})
	})
}

func TestE2ECompactionEventsMobileTrimsWithoutSummaries(t *testing.T) {
	e2e(t, compactionSetup+`The compaction event handles trims without summaries on a phone. The compaction only trimmed one tool output (#7).
1. Navigate to /c/{{SLUG}}.
2. The status ('[data-testid="message-inplacecompaction"] > summary') has exactly the text "Compacted context · 1 tool output".
3. Click the status. The result then contains no "details" element (count 0).
4. The result contains no element with role "table" (count 0).
5. The result's ".msg-compaction-ids" is visible and has exactly the text "#7".
6. `+compactionNoOverflow, func(s *shelley) {
		s.viewport(390, 844)
		s.compactionReplay(map[string]any{
			"trims": []any{map[string]any{"sequence_id": 7, "tool_use_id": "first-output"}},
		})
	})
}

func TestE2ECompactionEventsMobileSkips(t *testing.T) {
	e2e(t, compactionSetup+`The compaction event handles a skipped compaction on a phone. The compaction only hid one message (the context nudge).
1. Navigate to /c/{{SLUG}}.
2. The status ('[data-testid="message-inplacecompaction"] > summary') has exactly the text "Compaction skipped".
3. Click the status. The result then contains no "details" element (count 0).
4. The result contains no element with role "table" (count 0).
5. The innermost element inside the result whose text contains "Hid 1 context nudge from the agent." is visible ("*" with has_text, within the result): not the result itself, which stays visible through its summary even while closed.
6. `+compactionNoOverflow, func(s *shelley) {
		s.viewport(390, 844)
		s.compactionReplay(map[string]any{"hidden_sequence_ids": []int{21}})
	})
}
