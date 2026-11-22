package test

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// Ported from ui/e2e/tool-components.spec.ts.

// smorgasbord makes the "tool smorgasbord" conversation, one call of
// nearly every tool, and returns its slug. Its browser tools start Chrome,
// which can take a while, so it waits longer than conversation does.
func (s *shelley) smorgasbord() string {
	var created struct {
		ID string `json:"conversation_id"`
	}
	s.api("POST", "/api/conversations/new", map[string]any{"message": "tool smorgasbord", "model": "predictable", "cwd": s.cwd}, &created)
	var slug string
	s.poll(150*time.Second, func() error {
		var got struct {
			Messages     []apiMessage `json:"messages"`
			Conversation struct {
				Slug string `json:"slug"`
			} `json:"conversation"`
		}
		s.api("GET", "/api/conversation/"+created.ID, nil, &got)
		if !slices.ContainsFunc(got.Messages, apiMessage.endsTurn) {
			return errors.New("no reply yet")
		}
		if got.Conversation.Slug == "" {
			return errors.New("no slug yet")
		}
		slug = got.Conversation.Slug
		return nil
	})
	return slug
}

// toolComponentsMount waits for the smorgasbord's cards and mounts the ones
// still offscreen placeholders, so the whole set can be checked without
// scrolling through it.
const toolComponentsMount = `Navigate to /c/{{SLUG}}. Wait (allow 30s) until there are at least 15 [data-testid="tool-call-completed"] elements (an eval with expect). Then mount every card: dispatch a "beforeprint" Event on window (an eval without expect), and wait (allow 15s) until there is no ".tool-card-mount-placeholder" element (count 0).`

// toolComponentsEmoji defines window.__toolEmoji(summary): the text of the
// ".tool-emoji" of the first ".tool" card with a ".tool-command" matching
// summary, as Playwright's filter({has: locator('.tool-command', {hasText:
// summary})}) picks it.
const toolComponentsEmoji = `window.__toolEmoji = (summary) => {
  const matches = (el) => {
    const text = el.textContent.replace(/\s+/g, ' ').trim();
    return typeof summary === 'string' ? text.toLowerCase().includes(summary.toLowerCase()) : summary.test(text);
  };
  for (const card of document.querySelectorAll('.tool')) {
    if (![...card.querySelectorAll('.tool-command')].some(matches)) continue;
    const emoji = card.querySelector('.tool-emoji');
    return emoji ? emoji.textContent.trim() : 'no emoji';
  }
  return 'missing';
};`

func TestE2EToolComponentsAllCustom(t *testing.T) {
	e2e(t, useful(0), `Every tool uses its own component, not GenericTool. The conversation at /c/{{SLUG}} ran the "tool smorgasbord": one call of nearly every tool. The page defines window.__toolEmoji(summary): the text of the ".tool-emoji" of the first ".tool" card that has a ".tool-command" matching summary (a string matches as a case-insensitive substring, a RegExp is tested against the whitespace-collapsed text), or "missing" if there is no such card. The transcript is taller than the screen and offscreen cards are content-visibility: auto, so check that cards exist (assert_count or eval) rather than that they are visible, unless a step says visible. Perform the steps in order.
1. Call "keyword_search": it must return exactly "absent" (the tool registry, GET /api/tools, has no keyword_search tool).
2. `+toolComponentsMount+`
3. There is no ".tool-status-icon" element (count 0).
4. "The bash card" is the first ".bash-tool" element whose text contains "echo 'hello from bash'". It exists, has a ".bash-tool-emoji" element inside, and its ".bash-tool-command" text contains "echo 'hello from bash'".
5. Thinking content appears inline, not as a tool card: the first ".thinking-content" element whose text contains "I'm thinking about the best approach" is visible, and an element inside it whose text contains "💭" is visible.
6. Diff, image and iframe tools keep their own card shapes: a ".patch-tool" element exists with a ".patch-tool-emoji" inside it.
7. A browser screenshot action renders the screenshot card: a ".screenshot-tool" element exists whose whitespace-collapsed text matches /\.png$|screenshot/i, and a ".screenshot-tool .screenshot-tool-emoji" element whose text contains "📷" exists. The read_image card is a ".screenshot-tool" element whose text contains "/tmp/image.png"; it has a ".screenshot-tool-emoji" inside whose text contains "🖼️". A browser screencast_stop renders a ".screencast-tool" element (one exists).
8. Each card renders its component's emoji. The browser families (emulate, network, accessibility, profile) are folded into the browser tool as <family>_<sub> actions, and the old standalone browser_* tool names still render the same components; the emoji proves the dispatch. In one eval, ['https://example.com', 'document.title', /^pdf$/, /console/i, '/tmp/test-prompt.txt', /^device iphone_14$/, /^device ipad$/, /^enable$/, /^tree$/, /^metrics$/].map((s) => window.__toolEmoji(s)).join(' ') must equal exactly "🌐 ⚡ 📄 📋 🤖 📱 📱 📡 🌳 📊" (use that eval with this expect).
9. The browser pdf card links the saved file through /api/read: the first ".pdf-tool-link" element's href attribute (getAttribute) matches /^\/api\/read\?path=%2Ftmp%2Fshelley-pdfs%2F[0-9a-f-]+\.pdf$/ (an eval with expect "true"), and fetching that href from the page answers with the content-type header exactly "application/pdf" (an eval of fetch(href).then((r) => r.headers.get('content-type')) with that expect).
10. Summary lines carry no native title tooltip: there is no ".tool-command[title], .bash-tool-command[title]" element (count 0), and no ".tool-command:not(.change-dir-command) .ellipsis-start" element (count 0).
11. No tool falls back to GenericTool's gear: no ".tool-emoji" element whose text contains "⚙️" (assert_count 0 with that has_text).`, func(s *shelley) {
		s.initScript(toolComponentsEmoji)
		s.call("keyword_search", func() string {
			var got struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			s.api("GET", "/api/tools", nil, &got)
			if len(got.Tools) == 0 {
				return "no tools"
			}
			for _, tool := range got.Tools {
				if tool.Name == "keyword_search" {
					return "present"
				}
			}
			return "absent"
		})
		s.v("SLUG", s.smorgasbord())
	})
}

func TestE2EToolComponentsBrowserNavigateURL(t *testing.T) {
	e2e(t, useful(0), `The browser navigate card shows the URL in its header. The conversation at /c/{{SLUG}} ran the "tool smorgasbord", including a browser navigate to https://example.com. Perform the steps in order.
1. `+toolComponentsMount+`
2. "The navigate card" is the first ".tool" element that has a ".tool-command" whose text contains "https://example.com". Its text contains "https://example.com".
3. Click the navigate card's ".tool-header". Expanding it shows the URL section: a ".tool-code" element inside the navigate card whose text contains "https://example.com" is visible.`, func(s *shelley) {
		s.v("SLUG", s.smorgasbord())
	})
}

func TestE2EToolComponentsEmojiSizes(t *testing.T) {
	e2e(t, useful(0), `Emoji sizes are consistent across all tools. The conversation at /c/{{SLUG}} ran the "tool smorgasbord": one call of nearly every tool. Perform the steps in order.
1. `+toolComponentsMount+`
2. Every tool card's emoji has the computed font-size 16px (1rem): an eval of JSON.stringify([...new Set([...document.querySelectorAll('.tool-emoji, .bash-tool-emoji, .patch-tool-emoji, .screenshot-tool-emoji')].map((el) => getComputedStyle(el).fontSize))]) must equal exactly ["16px"].`, func(s *shelley) {
		s.v("SLUG", s.smorgasbord())
	})
}

// toolComponentsReplay serves conversation c's full loads with calls (an
// agent message) and results (a user message) in place of its messages,
// with its live stream cut off.
func (s *shelley) toolComponentsReplay(c convo, date string, calls, results []any) {
	body := s.conversationBody(c.ID)
	body["conversation"].(map[string]any)["agent_working"] = false
	message := func(id string, seq int, typ string, role int, content []any, at string) map[string]any {
		return map[string]any{
			"message_id": id, "conversation_id": c.ID, "sequence_id": seq, "type": typ, "generation": 1,
			"created_at": at, "llm_data": mustJSON(map[string]any{"Role": role, "Content": content, "EndOfTurn": false}),
		}
	}
	body["messages"] = []any{
		message("tool-card-calls", 1, "agent", 1, calls, date+"T12:00:00Z"),
		message("tool-card-results", 2, "user", 0, results, date+"T12:00:08Z"),
	}
	s.route("GET /api/stream2", abort)
	s.serveConversation(c.ID, body)
	s.v("SLUG", c.Slug)
}

const (
	toolComponentsRawName     = "custom_tool_missing_ui_&_#_%_雪_with_a_very_long_name"
	toolComponentsBrowserName = "browser (future_action_&_#_%_雪_without_a_dedicated_component_name)"
)

func TestE2EToolComponentsAudioAndGenericFallbacks(t *testing.T) {
	e2e(t, useful(0), `Audio transcription has its own card, and generic fallbacks identify themselves. The conversation at /c/{{SLUG}} is served as a replay (its live stream is cut off) of four tool calls: two audio transcriptions, one of an unknown tool "`+toolComponentsRawName+`" and one of an unknown browser action. Names used below:
- "the transcription" is the ".audio-transcription-tool" element whose text contains "planning-session.webm"; "the failed transcription" is the ".audio-transcription-tool" element whose text contains "broken-recording.webm".
- "The raw summary" is the ".tool-result-summary" element (the unknown tool's generic card); "the raw details" is the <details> element around it ('details:has(.tool-result-summary)'); "the raw warning" is the raw summary's "a.generic-tool-warning".
- "The browser card" is the ".tool" element whose text contains "`+toolComponentsBrowserName+`"; "the browser warning" is its "a.generic-tool-warning".
GitHub (https://github.com) is stubbed, so links to it open in a new tab without leaving this machine. Perform the steps in order.
1. Navigate to /c/{{SLUG}}. There are exactly 2 ".audio-transcription-tool" elements.
2. The transcription is visible and its data-testid is exactly "tool-call-completed". Its text contains "Audio transcription", "planning-session.webm", "gpt-transcribe" and "5.6s", and does not contain "duration_ms" or "We should ship the polished transcription card this week.".
3. The transcription's ".tool-header" does not overflow: its scrollWidth <= clientWidth (an eval with expect "true").
4. Click the transcription's ".tool-header". Its text then contains "We should ship the polished transcription card this week.", "1,234 prompt characters", "whisper-1" and "planning-session.webm.transcript-timestamps.json", and the transcription itself does not overflow (scrollWidth <= clientWidth, an eval with expect "true").
5. The failed transcription's text contains "failed". Click its ".tool-header". Its text then contains "Error:" and "The recording could not be transcribed.", and does not contain "Transcript:".
6. Neither the raw summary nor the browser card's ".tool-header" overflows (scrollWidth <= clientWidth for each, an eval with expect "true").
7. Check each warning link: the raw warning names the tool "`+toolComponentsRawName+`" and the browser warning names "`+toolComponentsBrowserName+`". For each, with N its name: its aria-label is exactly "GENERIC TOOL RESULT (Please report a bug) — missing UI for N (opens in new tab)", its target is exactly "_blank", its rel is exactly "noopener noreferrer", and its href is a URL whose origin + pathname is exactly "https://github.com/boldsoftware/shelley/issues/new", whose "labels" search parameter is exactly "bug", whose "title" parameter is exactly "Missing tool UI: N", and whose "body" parameter contains "**Tool:** `+"`N`"+`" (N in backticks). Check the href with one eval per link that parses it with new URL() and returns these four facts joined by "|" (the last as true/false), with the exact expect.
8. The raw details has no "open" attribute. Click the raw warning: a new tab opens (the popup); close it. The raw details still has no "open" attribute.
9. Focus the raw warning (an eval calling .focus()) and press Space. The raw details still has no "open" attribute.
10. Click the browser warning: a new tab opens; close it. The browser card's ".tool-details" is hidden (the click did not expand the card).
11. Exactly 2 elements have the whole text "GENERIC TOOL RESULT (Please report a bug)" (count, in an eval, the elements whose trimmed textContent equals it and none of whose children's does), and both are visible.`, func(s *shelley) {
		s.host("https://github.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "<!doctype html><title>New issue</title>New issue")
		}))
		c := s.conversation("echo: tool card fixture", "")
		calls := []any{
			map[string]any{"ID": "audio-transcription", "Type": 5, "ToolName": "openai_audio_transcription", "ToolInput": map[string]any{
				"file": "/tmp/shelley-uploads/planning-session.webm", "model": "gpt-transcribe", "response_format": "json", "prompt_chars": 1234,
			}},
			map[string]any{"ID": "audio-transcription-error", "Type": 5, "ToolName": "openai_audio_transcription", "ToolInput": map[string]any{
				"file": "/tmp/shelley-uploads/broken-recording.webm", "model": "gpt-transcribe", "prompt_chars": 0,
			}},
			map[string]any{"ID": "unknown-tool", "Type": 5, "ToolName": toolComponentsRawName, "ToolInput": map[string]any{"value": 42}},
			map[string]any{"ID": "unknown-browser-action", "Type": 5, "ToolName": "browser", "ToolInput": map[string]any{
				"action": "future_action_&_#_%_雪_without_a_dedicated_component_name",
			}},
		}
		text := func(v any) []any { return []any{map[string]any{"Type": 2, "Text": mustJSON(v)}} }
		results := []any{
			map[string]any{"Type": 6, "ToolUseID": "audio-transcription", "ToolResult": text(map[string]any{
				"duration_ms": 5573, "model": "gpt-transcribe", "text": "We should ship the polished transcription card this week.",
				"timestamps_model": "whisper-1", "timestamps_path": "/tmp/shelley-uploads/planning-session.webm.transcript-timestamps.json",
			}), "ToolUseStartTime": "2026-09-22T12:00:00Z", "ToolUseEndTime": "2026-09-22T12:00:05.573Z"},
			map[string]any{"Type": 6, "ToolUseID": "audio-transcription-error", "ToolError": true, "ToolResult": text(map[string]any{
				"duration_ms": 931, "error": "The recording could not be transcribed.", "model": "gpt-transcribe",
			}), "ToolUseStartTime": "2026-09-22T12:00:05.600Z", "ToolUseEndTime": "2026-09-22T12:00:06.531Z"},
			map[string]any{
				"Type": 6, "ToolUseID": "unknown-tool", "ToolResult": []any{map[string]any{"Type": 2, "Text": "unknown tool output"}},
				"ToolUseStartTime": "2026-09-22T12:00:06Z", "ToolUseEndTime": "2026-09-22T12:00:06.100Z",
			},
			map[string]any{
				"Type": 6, "ToolUseID": "unknown-browser-action", "ToolResult": []any{map[string]any{"Type": 2, "Text": "unknown browser action output"}},
				"ToolUseStartTime": "2026-09-22T12:00:07Z", "ToolUseEndTime": "2026-09-22T12:00:07.100Z",
			},
		}
		s.toolComponentsReplay(c, "2026-09-22", calls, results)
	})
}

// browserInputError is a browser call the backend rejected, and what its
// card shows.
type browserInputError struct {
	id    string
	input any
	label string
	text  string
}

// browserInputErrors mirror the backend's messages; the legacy rows come
// from older builds.
func browserInputErrors() []browserInputError {
	const (
		retry         = " · model asked to retry"
		missingAction = "Invalid browser call: missing action"
		invalidInput  = "Invalid browser call: input must be a JSON object"
		actionType    = "Invalid browser call: action must be a string"
	)
	unsupported := func(action string) string { return `Invalid browser call: unsupported action "` + action + `"` }
	cases := []browserInputError{
		{"missing-action", map[string]any{"url": "https://example.com"}, missingAction + retry, `invalid browser call: "action" is required and must be a non-empty string. Choose an action from the browser schema and resend the call.`},
		{"empty-action", map[string]any{"action": ""}, missingAction + retry, `invalid browser call: "action" is required.`},
		{"legacy-missing", map[string]any{"action": ""}, missingAction, `unknown action: ""`},
		{"string-input", `{"action":"eval"}`, invalidInput + retry, `invalid browser call: arguments were sent as a quoted JSON string. Decoded arguments: {"action":"eval"}. Resend an unquoted JSON object with a non-empty string "action" and field types matching the browser schema.`},
		{"malformed-input", `{"action":"eval"`, invalidInput + retry, `invalid browser call: arguments were not valid JSON.`},
		{"null-input", nil, invalidInput + retry, `invalid browser call: arguments were null.`},
		{"array-input", []any{}, invalidInput + retry, `invalid browser call: arguments were a JSON array.`},
		{"number-action", map[string]any{"action": 42}, actionType + retry, `invalid browser call: "action" must be a string, got 42.`},
		{"null-action", map[string]any{"action": nil}, actionType + retry, `invalid browser call: "action" must be a string, got null.`},
	}
	for _, action := range []string{"click", "emulate_dark_mode", "emulate_bogus", "network_bogus", "accessibility_bogus", "profile_bogus"} {
		cases = append(cases, browserInputError{
			action,
			map[string]any{"action": action},
			unsupported(action) + retry,
			`invalid browser call: unknown action "` + action + `". Choose an action from the browser schema and resend the call. All supported actions: navigate, eval, screenshot.`,
		})
	}
	return append(cases,
		browserInputError{"width-type", map[string]any{"action": "resize", "width": "wide"}, "Invalid browser call: wrong field type for resize" + retry, `invalid browser call: field "width" must be an integer, got a JSON string.`},
		browserInputError{"await-type", map[string]any{"action": "eval", "expression": "x", "await": "yes"}, "Invalid browser call: wrong field type for eval" + retry, `invalid browser call: field "await" must be a boolean, got a JSON string.`},
		browserInputError{"legacy-unknown", map[string]any{"action": "legacy_bogus"}, unsupported("legacy_bogus"), `unknown action: "legacy_bogus"`},
	)
}

func TestE2EToolComponentsBrowserInputErrors(t *testing.T) {
	cases := browserInputErrors()
	var list strings.Builder
	for i, c := range cases {
		fmt.Fprintf(&list, "%d. ID %s: label %q; text %q", i+1, c.id, c.label, c.text)
		if in, ok := c.input.(string); ok {
			fmt.Fprintf(&list, "; code %q", in)
		}
		list.WriteString("\n")
	}
	e2e(t, useful(0), `Browser input errors are labeled, and keep the model's guidance visible. The conversation at /c/{{SLUG}} is served as a replay (its live stream is cut off) of `+fmt.Sprint(len(cases)+7)+` browser tool calls: `+fmt.Sprint(len(cases))+` the backend rejected as invalid (listed below), 2 with actions this UI has no card for ("brand_new_action", which worked, and "other_new_action", which failed), one valid eval of "document.title", and one each of emulate_help, network_help, accessibility_help and profile_help. "The invalid cards" are the ".tool" elements whose text contains "Invalid browser call:" (has_text); the Nth invalid call below renders the Nth invalid card (pick it with nth, as the spec's invalidCards.nth(i) did). "The card for action A" is the ".tool" element that has a ".tool-command" whose text contains "browser (A)" (a "has" filter).
The invalid calls, in order, with the label each card's ".tool-command" shows, a text its expanded details contain, and for some the code its input shows (JSON-quoted here; the page shows the unquoted text):
`+list.String()+`Perform the steps in order.
1. Navigate to /c/{{SLUG}}. There are exactly `+fmt.Sprint(len(cases)+7)+` [data-testid="tool-call-completed"] elements (one per call).
2. Mount every card: dispatch a "beforeprint" Event on window (an eval without expect), and wait (allow 15s) until there is no ".tool-card-mount-placeholder" element (count 0).
3. There are exactly `+fmt.Sprint(len(cases))+` invalid cards, and no ".generic-tool-warning" inside any of them (assert_count of ".tool:has(.generic-tool-warning)" with has_text "Invalid browser call:" is 0).
4. For each invalid call above, in order, on its invalid card: its ".tool-command" text is exactly the label (assert_text); click its ".tool-header"; its ".tool-details" text then contains "Output (Error):" and contains the text; and if a code is listed, its first ".tool-code" element's text is exactly the code (assert_text).
5. The calls with no card of their own keep the bug-report warning: the card for action brand_new_action and the card for action other_new_action each have exactly 1 ".generic-tool-warning" inside, and the page has exactly 2 ".generic-tool-warning" elements.
6. The valid eval's card, the ".tool" element that has a ".tool-command" whose text contains "document.title", has a ".tool-emoji" whose text is exactly "⚡".
7. Each browser family keeps its own card: for each of "📱", "📡", "🌳" and "📊", exactly 1 ".tool-emoji" element's text contains it (assert_count with has_text).`, func(s *shelley) {
		c := s.conversation("echo: browser input errors", "")
		var calls, results []any
		result := func(id string, isError bool, text string) any {
			return map[string]any{"Type": 6, "ToolUseID": id, "ToolError": isError, "ToolResult": []any{map[string]any{"Type": 2, "Text": text}}}
		}
		call := func(id string, input any) any {
			return map[string]any{"ID": id, "Type": 5, "ToolName": "browser", "ToolInput": input}
		}
		for _, e := range cases {
			calls = append(calls, call(e.id, e.input))
			results = append(results, result(e.id, true, e.text))
		}
		calls = append(calls,
			call("new-action-ok", map[string]any{"action": "brand_new_action"}),
			call("new-action-failed", map[string]any{"action": "other_new_action"}),
			call("valid-action", map[string]any{"action": "eval", "expression": "document.title"}))
		results = append(results,
			result("new-action-ok", false, "worked"),
			result("new-action-failed", true, "failed to start browser"),
			result("valid-action", false, "Fixture title"))
		for _, action := range []string{"emulate_help", "network_help", "accessibility_help", "profile_help"} {
			calls = append(calls, call(action, map[string]any{"action": action}))
			results = append(results, result(action, false, "Fixture "+action))
		}
		s.toolComponentsReplay(c, "2026-10-11", calls, results)
	})
}

// toolComponentsAuditHTML is a generic tool card's summary, as the spec
// put it in place of the page.
const toolComponentsAuditHTML = `<div style="padding: 16px">
  <details class="tool-result-details">
    <summary class="tool-result-summary">
      <div class="tool-result-meta">
        <div class="tool-result-primary flex space-x-2">
          <svg class="chat-tool-icon"></svg>
          <div class="generic-tool-result-summary">
            <div class="generic-tool-result-heading">
              <span class="tool-result-name text-sm font-medium text-blue">
                custom_tool_missing_ui
              </span>
              <a
                class="generic-tool-warning"
                href="https://github.com/boldsoftware/shelley/issues/new"
                target="_blank"
                rel="noopener noreferrer"
              >
                GENERIC TOOL RESULT (Please report a bug)
              </a>
            </div>
            <span class="tool-result-status text-xs">
              unknown tool output that must remain inside the card
            </span>
          </div>
        </div>
        <div class="tool-result-time"></div>
      </div>
    </summary>
  </details>
</div>`

func TestE2EToolComponentsGenericAuditMobile(t *testing.T) {
	e2e(t, useful(0), `The generic tool audit stays inside its card on a 360px-wide phone. The page defines window.__auditHTML, the markup of a generic tool card. Perform the steps in order.
1. Navigate to /new and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. Replace the page with the card: in an eval, set document.body.innerHTML = window.__auditHTML.
3. The card (".tool-result-details") and the warning (".generic-tool-warning") are visible.
4. The summary (".tool-result-summary") does not overflow: its scrollWidth <= clientWidth (an eval with expect "true").
5. Neither the status (".tool-result-status") nor the warning sticks out of the summary: each one's getBoundingClientRect().right <= the summary's getBoundingClientRect().right (one eval with expect "true").`, func(s *shelley) {
		s.viewport(360, 800)
		s.initScript("window.__auditHTML = " + mustJSON(toolComponentsAuditHTML) + ";")
	})
}

func TestE2EToolComponentsBashCommandInHeader(t *testing.T) {
	e2e(t, useful(0), `The bash card shows its command in the header. The conversation at /c/{{SLUG}} ran the bash command "unique-test-command-xyz123". Perform the steps in order.
1. Navigate to /c/{{SLUG}}.
2. "The card" is the ".bash-tool" element whose text contains "unique-test-command-xyz123"; it renders inline and collapsed, and is visible (allow 15s).
3. The card's ".bash-tool-command" is visible and its text contains "unique-test-command-xyz123".`, func(s *shelley) {
		s.v("SLUG", s.conversation("bash: unique-test-command-xyz123", "").Slug)
	})
}

func TestE2EToolComponentsThinkInline(t *testing.T) {
	e2e(t, useful(0), `The think tool's thought shows inline with its text. The conversation at /c/{{SLUG}} thought "This is a long thought that should be truncated in the header display" and then answered "I've considered my approach.". Perform the steps in order.
1. Navigate to /c/{{SLUG}}.
2. Wait until the page text contains "I've considered my approach." (allow 15s).
3. The first ".thinking-content" element whose text contains "This is a long thought" is visible, and its text contains "This is a long thought".`, func(s *shelley) {
		s.v("SLUG", s.conversation("think: This is a long thought that should be truncated in the header display", "").Slug)
	})
}

// toolComponentsPageErrors keeps the page's uncaught errors in
// window.__pageErrors.
const toolComponentsPageErrors = `window.__pageErrors = [];
window.addEventListener('error', (e) => window.__pageErrors.push(String(e.message)));
window.addEventListener('unhandledrejection', (e) => window.__pageErrors.push(String(e.reason && e.reason.message || e.reason)));`

func TestE2EToolComponentsPatchToggle(t *testing.T) {
	e2e(t, useful(0), `The patch card collapses and expands without errors. The conversation at /c/{{SLUG}} applied a patch to test-patch-success.txt. The page keeps its uncaught errors in window.__pageErrors. "The card" is the first '.patch-tool[data-testid="tool-call-completed"]' element whose text contains "test-patch-success.txt"; "the header" is its ".patch-tool-header" and "the details" its ".patch-tool-details". Perform the steps in order.
1. Navigate to /c/{{SLUG}}. The card is visible (allow 15s).
2. The card's ".patch-tool-toggle" is visible.
3. Click the header: the details become hidden.
4. Click the header: the details become visible (allow 10s).
5. Click the header: the details become hidden.
6. Click the header: the details become visible (allow 10s).
7. No Monaco model error occurred: an eval of window.__pageErrors.filter((m) => m.includes('model') && m.includes('already exists')).length with expect "0".`, func(s *shelley) {
		s.initScript(toolComponentsPageErrors)
		s.v("SLUG", s.conversation("patch success", "").Slug)
	})
}
