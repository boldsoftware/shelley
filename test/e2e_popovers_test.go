package test

// Ported from ui/e2e/popovers.spec.ts.

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Anchored-popover contract for the two floating popups migrating to PrimeVue
// Popover: the ConversationTOC "Jump to…" panel and the ContextUsageBar token
// popup (opened from the "<tokens> · <model>" status label). These tests pin
// the DOM/ARIA contract (classes, labels, dismissal behavior) so it holds
// across the hand-rolled and PrimeVue implementations.

// popoversSlowConversation is conversationWith for a first turn that may take up to
// timeout, such as one that runs the browser tool.
func popoversSlowConversation(s *shelley, body map[string]any, timeout time.Duration) convo {
	var created struct {
		ID string `json:"conversation_id"`
	}
	s.api("POST", "/api/conversations/new", body, &created)
	c := convo{ID: created.ID}
	s.poll(timeout, func() error {
		var got struct {
			Messages     []apiMessage `json:"messages"`
			Conversation struct {
				Slug string `json:"slug"`
			} `json:"conversation"`
		}
		s.api("GET", "/api/conversation/"+c.ID, nil, &got)
		if !slices.ContainsFunc(got.Messages, apiMessage.endsTurn) {
			return errors.New("no reply yet")
		}
		if got.Conversation.Slug == "" {
			return errors.New("no slug yet")
		}
		c.Slug = got.Conversation.Slug
		return nil
	})
	return c
}

// popoversChatAndWait sends message to conversation id and waits until it has turns
// finished agent turns.
func popoversChatAndWait(s *shelley, id, message string, turns int) {
	s.api("POST", "/api/conversation/"+id+"/chat", map[string]string{"message": message, "model": "predictable"}, nil)
	s.poll(30*time.Second, func() error {
		var got struct {
			Messages []apiMessage `json:"messages"`
		}
		s.api("GET", "/api/conversation/"+id, nil, &got)
		n := 0
		for _, m := range got.Messages {
			if m.endsTurn() {
				n++
			}
		}
		if n != turns {
			return fmt.Errorf("%d finished turns, want %d", n, turns)
		}
		return nil
	})
}

// popoversFlatModelCosts answers POST /api/model-costs with the same per-million
// prices for every model asked about.
func popoversFlatModelCosts(s *shelley, input, output, cacheRead, cacheWrite float64) {
	s.route("POST /api/model-costs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Models []struct {
				Model string `json:"model"`
			} `json:"models"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		costs := map[string]any{}
		for _, m := range body.Models {
			costs[m.Model] = map[string]float64{"input": input, "output": output, "cache_read": cacheRead, "cache_write": cacheWrite}
		}
		writeJSON(w, map[string]any{"costs": costs})
	})
}

// popoversTOCStyling seeds the TOC styling test's conversation: a real one, served
// with extra messages of every source the TOC marks, and with the live
// stream cut off so nothing replaces them.
func popoversTOCStyling(s *shelley) {
	c := s.conversation("echo TOC source styling", "")
	s.v("SLUG", c.Slug)
	var body map[string]any
	s.api("GET", "/api/conversation/"+c.ID, nil, &body)
	messages := body["messages"].([]any)
	base := messages[len(messages)-1].(map[string]any)
	sequence := base["sequence_id"].(float64)
	generation, _ := base["generation"].(float64)
	s.v("NEXT_GENERATION", strconv.Itoa(int(generation)+1))
	n := 0
	message := func(text string, overrides map[string]any) map[string]any {
		n++
		sequence++
		m := maps.Clone(base)
		m["message_id"] = fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
		m["sequence_id"] = sequence
		m["type"] = "user"
		m["user_data"] = nil
		m["end_of_turn"] = false
		role := 0
		if overrides["type"] == "agent" {
			role = 1
		}
		m["llm_data"] = mustJSON(map[string]any{"Role": role, "Content": []any{map[string]any{"ID": "", "Type": 2, "Text": text}}})
		maps.Copy(m, overrides)
		return m
	}
	body["conversation"].(map[string]any)["agent_working"] = false
	messages = append(messages,
		message("**Keep** the current `TOC`", nil),
		message("See [the preview](https://example.com) **now**", map[string]any{"type": "agent", "end_of_turn": true}),
	)
	for _, relationship := range []string{"parent", "subagent"} {
		messages = append(messages, message(relationship+" follow-up", map[string]any{
			"user_data": mustJSON(map[string]any{"sender_conversation_id": "other-conversation", "sender_slug": "reviewer", "sender_relationship": relationship}),
		}))
	}
	messages = append(messages,
		message("Background job abc123 finished: exit 2, 2m28s. Log: /tmp/job.log", map[string]any{
			"user_data": mustJSON(map[string]any{"background_job_id": "abc123", "command": "make build\necho done", "exit_code": 2, "duration": "2m28s", "log_path": "/tmp/job.log", "tail": ""}),
		}),
		message("Working directory changed", map[string]any{
			"user_data": mustJSON(map[string]any{"cwd_change": true, "from": "/old", "to": "/new"}),
		}),
		message("Continue with the approved design", map[string]any{"generation": generation + 1}),
	)
	body["messages"] = messages
	s.route("GET /api/stream2", abort)
	s.route("GET /api/conversation/"+c.ID, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			s.pass.ServeHTTP(w, r)
			return
		}
		writeJSON(w, body)
	})
}

const popoversTOCStylingTest = `The conversation table of contents marks message sources with quiet icons on one text edge. The conversation /c/{{SLUG}} is served with extra messages after its first turn: a user message "**Keep** the current ` + "`TOC`" + `", an agent reply "See [the preview](https://example.com) **now**", messages from a parent and from a subagent conversation ("parent follow-up", "subagent follow-up"), a background job report for "make build" that exited 2, a "Working directory changed" notice, and, in a new generation {{NEXT_GENERATION}}, "Continue with the approved design". Within the TOC popover (".toc-popover"): "row X" is the ".toc-entry" containing X; "human" is row "Keep the current TOC", "reply" is row "See the preview now", "reports" are the ".toc-category-subagent" elements, "job" is the ".toc-category-background", "notice" is the ".toc-entry-user.toc-category-notice". "The color of E" is getComputedStyle(E).color. Perform the steps in order. 1. Navigate to /c/{{SLUG}} and click ".toc-button". 2. human's ".toc-entry-icon" should have exactly the text "•", and human's ".toc-entry-label" computed font-weight should be exactly "500". 3. For each of reply, the first report, job and notice: it holds exactly one ".toc-entry-icon svg", and its ".toc-entry-label" font-weight is exactly "400". 4. There should be exactly 2 reports and exactly 1 ".toc-entry-gen" in the TOC. 5. The textContent of the TOC's last 9 ".toc-entry-label" elements, in order, must be exactly ["Keep the current TOC","See the preview now","parent follow-up","subagent follow-up","make build · exit 2","Working directory changed","New generation ({{NEXT_GENERATION}})","Continue with the approved design","End of conversation"]. 6. The labels of ".toc-entry:not(.toc-entry-gen) .toc-entry-label" in the TOC must all share one text edge: the set of their getBoundingClientRect().left values has exactly 1 member. 7. Then for dark = false and then dark = true: a. eval document.documentElement.classList.toggle("dark", dark); b. poll until the color of human equals the color of reply; c. then check, at once: the color of the first report differs from the color of reply; the color of job differs from the color of the first report; the color of human's ".toc-entry-icon" differs from the color of reply's ".toc-entry-icon svg"; human's getBoundingClientRect().height is exactly 30; and document.documentElement.scrollWidth <= window.innerWidth. 8. The TOC's one ".toc-entry-active" must have computed background-color exactly "rgba(0, 0, 0, 0)".`

func TestE2EPopoversTOCSourceStyling390(t *testing.T) {
	e2e(t, `The screen is {{W}}x844. `+popoversTOCStylingTest, func(s *shelley) {
		s.viewport(390, 844)
		s.v("W", "390")
		popoversTOCStyling(s)
	})
}

func TestE2EPopoversTOCSourceStyling1280(t *testing.T) {
	e2e(t, `The screen is {{W}}x844. `+popoversTOCStylingTest, func(s *shelley) {
		s.viewport(1280, 844)
		s.v("W", "1280")
		popoversTOCStyling(s)
	})
}

func TestE2EPopoversTOCOpensListsDismisses(t *testing.T) {
	e2e(t, `The conversation table of contents popover opens from its nav button, lists entries, and dismisses. "The button" is ".toc-button"; "the popover" is ".toc-popover". `+menusAccessibleNames+`Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The button should be visible (allow 30s), with aria-expanded="false" and accessible name exactly "Conversation table of contents". 3. Click it: the popover and its ".toc-popover-list" should be visible, the button should have aria-expanded="true", and ".toc-popover-title" should have exactly the text "Jump to…". 4. The popover's first ".toc-entry" should contain "Top of conversation", its last ".toc-entry" should contain "End of conversation", and its first ".toc-entry-user" should contain "echo table of contents". 5. Press Escape: the popover should be hidden and the button should have aria-expanded="false". 6. Click the button: the popover should be visible. Click ".messages-container" 10 pixels right and 10 pixels down from its top-left corner: the popover should be hidden. 7. Click the button: the popover should be visible. Click its first ".toc-entry-user": the popover should be hidden, and location.hash should come to match /^#m-[a-zA-Z0-9]+$/ (poll for up to 5s).`, func(s *shelley) {
		s.v("SLUG", s.conversation("echo table of contents", "").Slug)
	})
}

func TestE2EPopoversTOCImageThumbnails(t *testing.T) {
	e2e(t, `The conversation table of contents shows timeline images as thumbnails. /c/{{INLINE}} is a conversation whose agent shows an image inline in its reply; /c/{{TOOL}} is one whose agent took a screenshot with its browser tool. Perform the steps in order. 1. Navigate to /c/{{INLINE}}. 2. The first ".message-agent img" ("the inline image") should be visible (allow 30s). 3. Click ".toc-button". 4. In ".toc-popover", the ".toc-entry-eot" containing "Verified against the real product" should hold a visible ".toc-entry-thumbnail" whose src attribute is exactly the inline image's src attribute. 5. Press Escape. 6. Navigate to /c/{{TOOL}}. 7. The first ".screenshot-tool img" ("the tool image") should be visible (allow 30s); remember its src attribute in a window variable. 8. Click the first ".screenshot-tool-header": the tool image should be hidden (collapsed). 9. Click ".toc-button". 10. The first ".toc-popover .toc-entry-image" ("the tool entry") should hold a visible ".toc-entry-thumbnail" whose src attribute is exactly the remembered src. 11. Click the tool entry: ".toc-popover" should be hidden, the URL should come to match /#t-[a-zA-Z0-9]+$/, and the first ".screenshot-tool" should have the class "message-highlight".`, func(s *shelley) {
		inline := popoversSlowConversation(s, map[string]any{"message": "screenshot image", "model": "predictable", "cwd": s.cwd}, 90*time.Second)
		tool := popoversSlowConversation(s, map[string]any{"message": "screenshot", "model": "predictable", "cwd": s.cwd}, 90*time.Second)
		s.v("INLINE", inline.Slug)
		s.v("TOOL", tool.Slug)
	})
}

func TestE2EPopoversActionBarHoverLabels(t *testing.T) {
	e2e(t, `The message action bar uses CSS-only hover labels. `+menusAccessibleNames+`1. Navigate to /c/{{SLUG}}. 2. The first data-testid "message" containing "echo action bar" ("the message") should be visible (allow 30s). 3. Hover the message. 4. The message's one button whose accessible name contains "Copy" should be visible and have the attribute data-tooltip exactly "Copy".`, func(s *shelley) {
		s.v("SLUG", s.conversation("echo action bar", "").Slug)
	})
}

func TestE2EPopoversActionBarStickyAndFlippedTooltips(t *testing.T) {
	e2e(t, `The message action bar follows long messages and flips clipped tooltips below. The screen is 390x667. "The message" is the last "[data-testid=\"message\"].message-agent"; "the entity" is its [data-content-entity="content"]; "the container" is the entity's closest ".messages-container"; "the action bar" is the entity's ".message-action-bar-wrapper"; "fork" is the action bar's button whose accessible name contains "Fork conversation from here". `+menusAccessibleNames+`Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The message (allow 30s) and the entity should be visible. 3. In one eval on the entity: set its style.minHeight to "1000px"; add (entity top - container top + 96) to the container's scrollTop, using getBoundingClientRect() tops; then dispatch new MouseEvent("click", {bubbles: true}) on the entity. 4. The action bar should be visible. Its sticky inset, action bar top - container top (getBoundingClientRect), must be within 0.5 of 8. 5. Hover fork: it should get data-tooltip-placement="bottom". 6. In one eval: add (entity top - container top - 120) to the container's scrollTop, then dispatch new MouseEvent("mouseover", {bubbles: true, cancelable: true, composed: true}) on fork. 7. Fork should get data-tooltip-placement="top".`, func(s *shelley) {
		s.viewport(390, 667)
		s.v("SLUG", s.conversation("echo sticky action bar", "").Slug)
	})
}

// The context usage popup's tests run on a 1280x800 screen.

func TestE2EPopoversContextUsageToggles(t *testing.T) {
	e2e(t, `The context usage popup toggles from the usage label and closes on outside click. The screen is 1280x800. "The label" is ".context-usage-label"; "the popup" is ".chat-context-popup". `+menusAccessibleNames+`Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The label should be visible (allow 30s). Its ".context-usage-label-tokens" must not be empty (non-empty textContent), its accessible name must match /^Context usage: .+ tokens$/ (read it from the aria-label attribute), and it should have aria-expanded="false". 3. Click the label: the popup should be visible and contain "LLM call"; its ".usage-popup-title" text should match /^Current context: .+ tokens$/; the label should have aria-expanded="true". 4. The label's aria-controls attribute must be non-empty, and the element with that id must have role="dialog". 5. The popup's ".token-cost-graph" and ".token-cost-graph-svg" should be visible. 6. Click the label again: the popup should be hidden and the label should have aria-expanded="false". 7. Click the label: the popup should be visible. Click ".messages-container" 10 pixels right and 10 pixels down from its top-left corner: the popup should be hidden and the label should have aria-expanded="false". 8. Click the label: the popup should be visible. Press Escape: the popup should be hidden and the label should have aria-expanded="false".`, func(s *shelley) {
		s.viewport(1280, 800)
		s.v("SLUG", s.conversation("echo context usage", "").Slug)
	})
}

// popoversSubagentLayout measures the context usage popup's layout all at once, so
// that nothing straddles the popup moving.
const popoversSubagentLayout = `window.__subagentLayout = () => {
  const box = (selector) => {
    const found = document.querySelectorAll(selector);
    if (found.length !== 1) throw new Error(found.length + " × " + selector);
    return found[0].getBoundingClientRect();
  };
  const total = box('[data-testid="token-cost-total"]');
  const subtotal = box('[data-testid="conversation-cost-subtotal"]');
  const firstModel = document.querySelector(".token-cost-model-breakdown").getBoundingClientRect();
  const title = box(".usage-popup-title");
  const graph = box(".token-cost-graph-svg");
  const table = box(".token-cost-table");
  const subagentCost = box('[data-testid="subagent-cost-row"] .token-cost-legend-cost');
  const totalCost = box('[data-testid="token-cost-total"] .token-cost-legend-cost');
  return {
    totalAboveSubtotal: total.y < subtotal.y,
    subtotalAboveModels: subtotal.bottom <= firstModel.y + 1,
    graphWide: graph.width > 500,
    graphAsWideAsTable: Math.abs(graph.width - table.width) < 1,
    titleAlignedWithGraph: Math.abs(title.x - graph.x) < 1,
    costsAligned: Math.abs(subagentCost.right - totalCost.right) < 1,
  };
};`

func TestE2EPopoversContextUsageSubagents(t *testing.T) {
	e2e(t, `The context usage popup lists subagents in the breakdown and includes them in the total. The screen is 1280x900, the page's clock is fake (window.__clock; time flows until fastForward), and every model costs $10 per million tokens of each kind. The server reports the conversation's subagents as one model "review-model" with 9 calls costing $55.161; later it reports $60. Call "mark" remembers how many times the page has asked for subagent usage so far; call "asked since mark" returns "true" if the page has asked again since, else "false"; call "hold" makes the server hold every later subagent usage request instead of answering it, and also marks like "mark"; call "release" switches the cost to $60, stops holding, answers the held request (it fails if none is held) and waits until it has been answered. Perform the steps in order. 1. Navigate to /c/{{SLUG}} and click ".context-usage-label". 2. data-testid "subagent-cost-row" ("the subagent row") should be visible (allow 30s) and contain "$55.16". 3. data-testid "token-cost-total" ("the total") should be visible and contain "Total", and its "th" should have computed border-top-style "solid". 4. A ".token-cost-graph-note" containing "main conversation only" should be visible. 5. Poll eval window.__subagentLayout() (the page already defines it; do not define it yourself) until it returns exactly {"totalAboveSubtotal":true,"subtotalAboveModels":true,"graphWide":true,"graphAsWideAsTable":true,"titleAlignedWithGraph":true,"costsAligned":true} (it measures the popup's boxes all at once, as the popup moves on a later frame after it is resized for the subagents). 6. Call "hold". 7. Eval window.__clock.fastForward(5000). 8. Call "asked since mark" until it returns "true", then call "mark". 9. Eval window.__clock.fastForward(15000), then call "asked since mark": it must return "false" (no second poll while one is outstanding). 10. Call "release". 11. The subagent row should contain "$60.00". The ".token-cost-model-breakdown" containing "review-model" should contain "6.0M" and "$60.00". 12. Call "mark". Click ".context-usage-label": the total should be hidden. Click ".context-usage-label" again: the total should be visible. 13. Call "asked since mark" until it returns "true" (reopening asks again).`, func(s *shelley) {
		s.viewport(1280, 900)
		s.clock(time.Time{})
		s.initScript(popoversSubagentLayout)
		popoversFlatModelCosts(s, 10, 10, 10, 10)
		var (
			mu       sync.Mutex
			requests int
			mark     int
			usd      = 55.161
			hold     bool
			held     chan struct{}
			answered chan struct{}
		)
		s.route("GET /api/conversation/{id}/subagent-usage", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			requests++
			if hold {
				release, done := make(chan struct{}), make(chan struct{})
				held, answered = release, done
				mu.Unlock()
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				defer close(done)
				mu.Lock()
			}
			cost := usd
			mu.Unlock()
			writeJSON(w, map[string]any{
				"per_model": []any{map[string]any{
					"model": "review-model", "url": "https://review.test", "llm_calls": 9,
					"input_tokens": cost * 1e5, "output_tokens": 0,
					"cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
					"estimated_usd": cost, "reported_usd": 0,
					"cost": map[string]float64{"input": 10, "output": 10, "cache_read": 10, "cache_write": 10},
				}},
				"llm_calls": 9, "estimated_usd": cost, "reported_usd": 0,
				"unpriced_reported_usd": 0, "unpriced_models": []any{}, "unpriced_calls": 0,
			})
		})
		s.call("mark", func() string {
			mu.Lock()
			defer mu.Unlock()
			mark = requests
			return "marked"
		})
		s.call("asked since mark", func() string {
			mu.Lock()
			defer mu.Unlock()
			return strconv.FormatBool(requests > mark)
		})
		s.call("hold", func() string {
			mu.Lock()
			defer mu.Unlock()
			hold, mark = true, requests
			return "holding"
		})
		s.call("release", func() string {
			mu.Lock()
			release, done := held, answered
			usd, hold, held = 60, false, nil
			mu.Unlock()
			if release == nil {
				s.fail("no subagent usage request is held")
			}
			close(release)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				s.fail("the held subagent usage request was not answered")
			}
			return "released"
		})
		s.v("SLUG", s.conversation("echo cost subtotal", "").Slug)
	})
}

func TestE2EPopoversContextUsageGraphsStack(t *testing.T) {
	e2e(t, `The context usage popup's graphs stack, and each can be switched off. The screen is 1280x800, and every model costs $10 per million tokens of each kind. "The popup" is ".chat-context-popup"; "the graph" is its ".token-cost-graph"; "the svg" is the graph's ".token-cost-graph-svg"; "the switches" are the popup's element with role "group" and accessible name exactly "Graphs"; "the readout" is the graph's ".token-cost-readout-live"; "the hover text" is the readout's ".token-cost-hover-text"; "box" means getBoundingClientRect(). "Checkbox X" is the popup's checkbox whose accessible name contains X. `+menusAccessibleNames+`Perform the steps in order. 1. Navigate to /c/{{SLUG}} and click ".context-usage-label". 2. The svg should be visible (allow 30s). 3. The switches' "label" elements should have exactly the texts ["context", "cumulative", "incremental"], in order, and every checkbox in the switches must be checked. 4. With T the box of the popup's ".usage-popup-title" and G the switches' box: |(T.y + T.height/2) - (G.y + G.height/2)| < 6, and G.x > T.x + T.width. 5. The popup's button whose accessible name contains "calls" should be visible, and the switches must contain no button (count 0). 6. The svg should contain the text "LLM call" but not "LLM call number". 7. The svg should have exactly 3 ".token-cost-pane-title"; their aria-label attributes must be exactly ["Context tokens", "Cumulative cost", "Incremental cost"], in order, and the first one's first "text" element's transform attribute must contain "rotate(-90)". 8. With H(P) the box height of the first ".token-cost-axis" in the svg's g[data-pane="P"]: |H(context) - H(cumulative)| < 1 and |H(cumulative)/3 - H(incremental)| < 1. 9. Among the d attributes of the svg's g[data-pane="cumulative"] path[fill] elements, some must match /^M[\d.]+,[\d.]+L/; among those of g[data-pane="incremental"] path[fill], some must match /^M[\d.]+,[\d.]+V[\d.]+H[\d.]+V[\d.]+Z/. 10. The readout should have exactly the text "Hover a call to see what drove its cost.". Remember (in window variables) the box height of the graph's ".token-cost-hover-readout" and the svg's box y. 11. Hover the svg at its horizontal middle and vertical middle (offsets of half its width and half its height from its top-left corner): the hover text should match /^call 1, \$[\d.]+, cumulative \$[\d.]+, context [\d.]+k?( \(.+\))?$/. 12. The readout's first ".token-cost-call-part" should match /^(Input|Output) [\d.]+k? \$[\d.]+$/. The computed background-color of its ".token-cost-chip" must be among the computed fill values of the svg's g[data-pane="incremental"] path[fill] elements. The readout's ".token-cost-call-why" should have exactly the text "predictable-v1 reports no prompt caching.". 13. The ".token-cost-hover-readout" box height must be within 0.5 of the remembered one, and the svg's box y within 0.5 of the remembered one (hovering did not move the graph). 14. Click checkbox "Context length": the svg should have exactly 2 "g[data-pane]", no g[data-pane="context"] (count 0), and the graph no ".context-legend" (count 0). 15. Click checkbox "Incremental cost": the svg should have exactly 1 "g[data-pane]". Hover the svg at its horizontal middle, 40 pixels below its top: the hover text should match /^call 1, cumulative \$[\d.]+( \(.+\))?$/. 16. Click checkbox "Context length", then checkbox "Cumulative cost": the svg should have exactly 1 "g[data-pane]" and exactly 1 g[data-pane="context"], and the graph's ".context-legend" should be visible. 17. Click checkbox "Context length": there should be no svg (count 0), and the switches should still have exactly 3 checkboxes.`, func(s *shelley) {
		s.viewport(1280, 800)
		popoversFlatModelCosts(s, 10, 10, 10, 10)
		s.v("SLUG", s.conversation("echo delta cost", "").Slug)
	})
}

func TestE2EPopoversContextUsageCacheMisses(t *testing.T) {
	e2e(t, `The context usage popup marks cache misses above their calls. The screen is 1280x800, and models cost (per million tokens) $5 input, $25 output, $0.50 cache read and $6.25 cache write. The conversation has three calls: the first writes 10000 tokens to the cache, the second reads them back, and the third finds only 500 still cached. "The graph" is ".chat-context-popup .token-cost-graph"; "the svg" is its ".token-cost-graph-svg"; "the mark" is the svg's ".token-cost-miss-mark"; "the readout" is the graph's ".token-cost-readout-live"; "box" means getBoundingClientRect(). Perform the steps in order. 1. Navigate to /c/{{SLUG}} and click ".context-usage-label". 2. The svg should be visible (allow 30s). 3. There should be exactly 1 mark, and its d attribute must contain exactly one "M". The readout should contain "▼ marks a cache miss.". 4. The mark's box bottom (y + height) must be <= (the box y of the first ".token-cost-axis" in the svg's g[data-pane="incremental"]) + 0.5. 5. "The bars": for every match of /M([\d.]+),[\d.]+V[\d.]+H([\d.]+)/g in the d attributes of the svg's g[data-pane="incremental"] path[fill] elements, the pair [left, right] of its two numbers. Their distinct left values, sorted ascending, must be exactly 3; "bar k" is the bar whose left is the k-th of them. "markMid" is the number right after the leading "M" of the mark's d, plus 3: it must be > bar 3's left and < bar 3's right. 6. "Hover at X" means: hover the svg at x offset X / W * (the svg's box width), where W is the third number of the svg's viewBox attribute, and y offset half the svg's box height (offsets from its top-left corner). 7. Hover at markMid: the readout's ".token-cost-hover-text" should match /^call 3,/, and the readout's ".token-cost-call-why" should match /^▼ Cache miss(, [\dhms ]+ after the previous call)?: 500 of 11k prompt tokens were still cached\.$/. 8. Hover at the middle of bar 2 ((left + right) / 2): the hover text should match /^call 2,/, and there should be no ".token-cost-call-why" in the readout (count 0).`, func(s *shelley) {
		s.viewport(1280, 800)
		popoversFlatModelCosts(s, 5, 25, 0.5, 6.25)
		c := s.conversation("cache: 0 10000", "")
		popoversChatAndWait(s, c.ID, "cache: 10000 500", 2)
		popoversChatAndWait(s, c.ID, "cache: 500 10500", 3)
		s.v("SLUG", c.Slug)
	})
}

func TestE2EPopoversContextUsageRemembersGraphs(t *testing.T) {
	e2e(t, `The context usage popup remembers which graphs are on across page loads. The screen is 1280x800. "The popup" is ".chat-context-popup"; "the svg" is its ".token-cost-graph-svg"; "checkbox X" is the popup's checkbox whose accessible name contains X. `+menusAccessibleNames+`Perform the steps in order. 1. Navigate to /c/{{SLUG}} and click ".context-usage-label". 2. The svg should be visible (allow 30s) with exactly 3 "g[data-pane]". 3. Click checkbox "Cumulative cost": the svg should have exactly 2 "g[data-pane]". 4. Reload (navigate to /c/{{SLUG}} again) and click ".context-usage-label". 5. The svg should be visible (allow 30s), with exactly 2 "g[data-pane]" and no g[data-pane="cumulative"] (count 0). Checkbox "Context length" should be checked, checkbox "Cumulative cost" not checked, and checkbox "Incremental cost" checked. 6. Click checkbox "Context length", then checkbox "Incremental cost": there should be no svg (count 0). 7. Reload (navigate to /c/{{SLUG}} again) and click ".context-usage-label". 8. Checkbox "Context length" should be visible and not checked (allow 30s), and there should be no svg (count 0).`, func(s *shelley) {
		s.viewport(1280, 800)
		s.v("SLUG", s.conversation("echo remember graphs", "").Slug)
	})
}

func TestE2EPopoversContextUsageAdvertisesClickable(t *testing.T) {
	e2e(t, `The token count advertises itself as clickable, and is its segment's only content. The screen is 1280x800. "The tokens" is the first visible ".context-usage-label-tokens"; "the segment" is the first visible ".context-usage-root". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The tokens should be visible (allow 30s). 3. Poll (up to 10s) one eval that reads both texts at once, as the count starts at 0 and changes when usage arrives: the segment's innerText, trimmed, must equal the innerText, trimmed, of the segment's ".context-usage-label-tokens". 4. The tokens' computed style must have text-decoration-line exactly "underline", text-decoration-style exactly "dotted" and cursor exactly "pointer". 5. Move the pointer to the viewport point (0, 0) (a hover without a selector), then hover the tokens: ".p-tooltip-text" should contain "Click for details".`, func(s *shelley) {
		s.viewport(1280, 800)
		s.v("SLUG", s.conversation("echo usage affordance", "").Slug)
	})
}

// popoversTokenRamp samples the token count's color, weight and contrast against
// the status bar for each escalation step, as the spec's sample did.
const popoversTokenRamp = `window.__tokenRamp = (isDark) => {
  const el = [...document.querySelectorAll(".context-usage-label-tokens")].find((e) => e.getClientRects().length > 0);
  // Theme changes and the count's own color are animated; a computed style
  // read mid-transition reports the color being left behind. Kill every
  // transition on the page first, then flip the theme.
  const freeze = document.createElement("style");
  freeze.textContent = "* { transition: none !important; }";
  document.head.appendChild(freeze);
  document.documentElement.classList.toggle("dark", isDark);
  const parse = (c) => c.match(/[\d.]+/g).slice(0, 3).map(Number);
  const lum = (rgb) => {
    const [r, g, b] = rgb.map((v) => {
      const s = v / 255;
      return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  };
  // The bar's own background, wherever it is painted.
  const backdrop = (start) => {
    for (let n = start; n; n = n.parentElement) {
      const c = getComputedStyle(n).backgroundColor;
      const parts = parse(c);
      const alpha = c.match(/[\d.]+/g).length > 3 ? Number(c.match(/[\d.]+/g)[3]) : 1;
      if (alpha > 0) return lum(parts);
    }
    return 1;
  };
  const bg = backdrop(el);
  const read = (cls) => {
    el.className = cls ? "context-usage-label-tokens " + cls : "context-usage-label-tokens";
    const s = getComputedStyle(el);
    const fg = lum(parse(s.color));
    const [hi, lo] = fg > bg ? [fg, bg] : [bg, fg];
    return { color: s.color, weight: s.fontWeight, contrast: (hi + 0.05) / (lo + 0.05) };
  };
  try {
    return {
      plain: read(""),
      warn: read("context-usage-label-tokens-warn"),
      high: read("context-usage-label-tokens-high"),
      critical: read("context-usage-label-tokens-critical"),
    };
  } finally {
    freeze.remove();
  }
};`

func TestE2EPopoversContextUsageTokenRamp(t *testing.T) {
	e2e(t, `The token count escalates legibly in both themes. The screen is 1280x800. window.__tokenRamp(dark) (the page already defines it; do not define it yourself) switches the page to the dark theme or not, applies each escalation class to the visible token count in turn, and returns {plain, warn, high, critical}, each {color, weight, contrast} (contrast against the status bar). Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The first visible ".context-usage-label-tokens" should be visible (allow 30s). 3. For dark = false and then dark = true, eval window.__tokenRamp(dark) once, and check on its result r: the four colors r.plain.color, r.warn.color, r.high.color and r.critical.color are all distinct (4 different strings); r.warn.contrast >= r.plain.contrast; r.high.contrast >= r.warn.contrast; r.critical.contrast >= r.high.contrast; and Number(r.critical.weight) > Number(r.plain.weight).`, func(s *shelley) {
		s.viewport(1280, 800)
		s.initScript(popoversTokenRamp)
		s.v("SLUG", s.conversation("echo usage ramp", "").Slug)
	})
}

func TestE2EPopoversContextUsageAdvisesCompacting(t *testing.T) {
	e2e(t, `The context usage popup advises compacting late, and never when the agent is nudged. The screen is 1280x800. Three conversations each report a large context: /c/{{HIGH}} reports 170000 tokens with the agent's compaction nudges off; /c/{{CRITICAL}} reports 185000 tokens with the nudges off; /c/{{NUDGED}} reports 185000 tokens with the nudges on (the default). "The label" is the first visible ".context-usage-label"; "the tokens" is its ".context-usage-label-tokens"; "the popup" is ".chat-context-popup"; "the advice" is an element of the popup containing the text "This conversation is getting long.". For each case, in order, do: navigate to the conversation; the tokens' text should match /^1[78]\dk$/ (allow 30s); check the tokens' class as given; click the label; the popup's data-testid "compact-in-place-button" should be visible; check the advice as given; press Escape; the popup should be hidden. The cases: 1. /c/{{HIGH}}: the tokens have the class "context-usage-label-tokens-high"; there is no advice (count 0). 2. /c/{{CRITICAL}}: the tokens have the class "context-usage-label-tokens-critical"; the advice is visible. 3. /c/{{NUDGED}}: no class of the tokens starts with "context-usage-label-tokens-" (it has only "context-usage-label-tokens" itself); there is no advice (count 0).`, func(s *shelley) {
		s.viewport(1280, 800)
		nudgesOff := map[string]any{"tool_overrides": map[string]string{"compact_in_place": "off"}}
		start := func(message string, options map[string]any) string {
			body := map[string]any{"message": message, "model": "predictable", "cwd": s.cwd}
			if options != nil {
				body["conversation_options"] = options
			}
			return s.conversationWith(body, "").Slug
		}
		s.v("HIGH", start("cache: 170000 0", nudgesOff))
		s.v("CRITICAL", start("cache: 185000 0", nudgesOff))
		s.v("NUDGED", start("cache: 185000 0", nil))
	})
}

// popoversShrinkChain lists the ancestors of the visible model name up to the
// status readout, with their min-width, as the spec's chain did.
const popoversShrinkChain = `window.__shrinkChain = () => {
  const el = [...document.querySelectorAll(".model-picker-value-name")].find((e) => e.offsetParent);
  if (!el) return null;
  const out = [];
  for (let n = el.parentElement; n; n = n.parentElement) {
    out.push({ cls: n.className || n.tagName, minWidth: getComputedStyle(n).minWidth, isSelectLabel: n.classList.contains("p-select-label") });
    if (n.classList.contains("status-readout")) break;
  }
  return out;
};`

func TestE2EPopoversContextUsageEllipsizesModelName(t *testing.T) {
	e2e(t, `The status readout ellipsizes the model name instead of overflowing in a narrow desktop pane. The screen is 1280x800, and every model's display name is "Predictable model with a very long display name". "Overflows" for a selector S means: take the first element matching S that is visible (non-empty getClientRects()) - there must be one - and compute scrollWidth > clientWidth + 1. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. data-testid "message-input" should be visible (allow 30s). 3. Eval document.querySelector(".main-content").style.maxWidth = "320px". 4. ".context-usage-label" should be visible. 5. ".status-bar-active", ".status-readout" and ".context-usage-label-tokens" must each not overflow (false). 6. Eval window.__shrinkChain() (the page already defines it; do not define it yourself), which lists every ancestor of the visible ".model-picker-value-name" up to the ".status-readout" as {cls, minWidth, isSelectLabel}: the result must not be null, some entry's cls must be exactly "status-readout", and every entry whose isSelectLabel is false must have minWidth exactly "0px". 7. For the first visible ".model-picker-value-name": its trimmed textContent must be non-empty, its computed text-overflow must be exactly "ellipsis", and it must overflow (scrollWidth > clientWidth + 1 is true).`, func(s *shelley) {
		s.viewport(1280, 800)
		s.initScript(popoversShrinkChain)
		s.route("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
			var models []map[string]any
			if err := s.fetchJSON(r, &models); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			for _, m := range models {
				m["display_name"] = "Predictable model with a very long display name"
			}
			writeJSON(w, models)
		})
		s.v("SLUG", s.conversation("echo narrow readout", "").Slug)
	})
}

func TestE2EPopoversContextUsageFollowsConversationSwitch(t *testing.T) {
	e2e(t, `The context usage popup keeps its graph populated when the conversation changes while it is open. The screen is 1280x800. "The popup" is ".chat-context-popup". Perform the steps in order. 1. Navigate to /c/{{FIRST}}. 2. ".context-usage-label" should be visible (allow 30s); click it. 3. The popup's ".token-cost-graph-svg" should be visible. 4. Navigate on the client side, without loading a page, with one eval: history.pushState({}, "", "/c/{{SECOND}}"); window.dispatchEvent(new PopStateEvent("popstate")). 5. The first data-testid "message" containing "two" should be visible. 6. The popup's ".token-cost-graph-svg" should be visible, and the popup must not contain "No usage data yet.".`, func(s *shelley) {
		s.viewport(1280, 800)
		s.v("FIRST", s.conversation("echo context usage one", "").Slug)
		s.v("SECOND", s.conversation("echo context usage two", "").Slug)
	})
}

// The status readout's three controls have distinct destinations: the cwd
// opens the directory picker, the token count the cost popup, the model name
// the model picker.

func TestE2EPopoversReadoutSegmentsUnderlined(t *testing.T) {
	e2e(t, `Every segment of the status readout carries the same dotted-underline affordance. The screen is 1280x800. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. ".context-usage-label" should be visible (allow 30s). 3. For each selector S of ".status-readout-cwd-path", ".context-usage-label-tokens" and ".model-picker-inline .model-picker-value-name": the first visible element matching S should be visible, and its computed style must have text-decoration-line exactly "underline", text-decoration-style exactly "dotted" and cursor exactly "pointer".`, func(s *shelley) {
		s.viewport(1280, 800)
		s.v("SLUG", s.conversation("echo readout affordances", "").Slug)
	})
}

func TestE2EPopoversReadoutCwdMovesConversation(t *testing.T) {
	e2e(t, `The status readout's cwd segment moves the conversation, and tells the agent. The screen is 1280x800. The conversation's directory is {{CWD}}; {{TARGET}} is another, small directory (shown as {{TARGET_LABEL}}). Call "cwd" returns the conversation's working directory as the server has it. "The segment" is the first visible ".status-readout-cwd"; "the path" is its ".status-readout-cwd-path"; "the panel" is ".modal.directory-picker-modal". `+menusAccessibleNames+`Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The segment should be visible (allow 30s), and the path visible with exactly the text "{{CWD}}". 3. Click the segment: the panel should be visible. 4. Fill the panel's ".directory-picker-input" with "{{TARGET}}/": the panel's ".directory-picker-current-path" should contain "e2e". 5. Click the button in the panel's ".modal-footer" whose accessible name contains "Select": the panel should be hidden. 6. The path should have exactly the text "{{TARGET_LABEL}}" (allow 15s). 7. Call "cwd" until it returns "{{TARGET}}" (up to 15s). 8. The last data-testid "message-cwdchange" should be visible and contain "working directory" and "{{TARGET_LABEL}}". 9. There must be no '[data-testid="message"].message-user' containing "{{TARGET}}" (count 0): the notice is not shown as something the user said.`, func(s *shelley) {
		s.viewport(1280, 800)
		target := s.mkdir(filepath.Join(s.dir, "e2e"))
		s.write(filepath.Join(target, "README.md"), "small\n")
		label := target
		if rel, ok := strings.CutPrefix(target, s.home+"/"); ok {
			label = "~/" + rel
		}
		s.v("TARGET", target)
		s.v("TARGET_LABEL", label)
		c := s.conversation("echo cwd control", "")
		s.call("cwd", func() string {
			var got struct {
				Conversation struct {
					Cwd string `json:"cwd"`
				} `json:"conversation"`
			}
			s.api("GET", "/api/conversation/"+c.ID, nil, &got)
			return got.Conversation.Cwd
		})
		s.v("SLUG", c.Slug)
	})
}

func TestE2EPopoversReadoutTokensAndModelDestinations(t *testing.T) {
	e2e(t, `In the status readout, the token count opens the cost popup and the model name opens the picker. The screen is 1280x800. "The tokens" is ".context-usage-label"; "the model" is ".model-picker-inline .p-select-label"; "the cost popup" is ".chat-context-popup"; "the picker panel" is ".model-picker-panel". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The tokens (allow 30s) and the model should be visible. 3. Click the tokens: the cost popup should be visible and contain "LLM call", and there must be no picker panel (count 0). 4. Press Escape: the cost popup should be hidden. 5. Click the model: the picker panel should be visible, as should its first ".p-select-option" and its ".model-picker-effort-pills"; the cost popup should be hidden. 6. Press Escape: the picker panel should be hidden.`, func(s *shelley) {
		s.viewport(1280, 800)
		s.v("SLUG", s.conversation("echo readout controls", "").Slug)
	})
}

func TestE2EPopoversReadoutPickerOnScreenNarrow(t *testing.T) {
	e2e(t, `The model picker's overlay stays on screen on a narrow viewport. The screen is 360x760. `+menusOpenModelPickerHow+`Perform the steps in order. 1. Navigate to /c/{{SLUG}} and open the model picker. 2. The panel and its first ".p-select-option" should be visible. 3. With B = the panel's getBoundingClientRect(): B.x >= 0, B.x + B.width <= 360, B.y >= 0 and B.y + B.height <= 760. 4. The panel's "input" should be visible.`, func(s *shelley) {
		s.viewport(360, 760)
		s.v("SLUG", s.conversation("echo narrow picker", "").Slug)
	})
}

// popoversSettingsRequests sets up calls "mark settings" and "settings since mark",
// which count the browser's requests to a /settings URL.
func popoversSettingsRequests(s *shelley) {
	count := func() int {
		n := 0
		for _, r := range s.requestLog() {
			if strings.Contains(r, "/settings") {
				n++
			}
		}
		return n
	}
	var mark int
	s.call("mark settings", func() string {
		mark = count()
		return "marked"
	})
	s.call("settings since mark", func() string {
		return strconv.Itoa(count() - mark)
	})
}

func TestE2EPopoversReadoutReasoningPillPersists(t *testing.T) {
	e2e(t, `A reasoning pill picked in the status readout's model picker persists server-side. The screen is 1280x800. Call "mark settings" remembers how many requests to a /settings URL the page has sent; call "settings since mark" returns how many it has sent since. "The trigger" is ".model-picker-inline .p-select-label"; "the pills" are the ".model-picker-effort-pills [role=radio]" elements. `+menusAccessibleNames+`Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The trigger should be visible (allow 30s); click it. 3. The first pill should be visible. 4. "The chosen pill" is the first pill whose text does not contain "auto" and whose aria-checked is "false"; there must be one. Remember its trimmed textContent ("the chosen level") in a window variable. 5. Click the chosen pill. 6. The last data-testid "message-modelchange" should contain the chosen level. 7. Poll (up to 10s) an eval that fetches /api/conversations, takes the JSON's conversations array (or the JSON itself when it is an array), finds the conversation whose slug is "{{SLUG}}", and checks that its conversation_options string contains '"thinking_level":"' + the chosen level + '"'. 8. If the click closed the picker (the first pill is not visible), click the trigger again. The first pill should be visible. 9. The element with role "radio" and accessible name exactly the chosen level should have aria-checked="true". 10. Count the data-testid "message-modelchange" elements and remember the count. Call "mark settings". 11. Click that radio again (it is already selected). Sleep 1000ms. 12. Call "settings since mark": it must return "0" (re-selecting the current level sends nothing), and the count of data-testid "message-modelchange" must still be the remembered one.`, func(s *shelley) {
		s.viewport(1280, 800)
		popoversSettingsRequests(s)
		s.v("SLUG", s.conversation("echo reasoning pill", "").Slug)
	})
}

func TestE2EPopoversReadoutPickerClosesWhenTurnStarts(t *testing.T) {
	e2e(t, `An open model picker closes when a turn starts. The screen is 1280x800. Call "mark settings" remembers how many requests to a /settings URL the page has sent; call "settings since mark" returns how many it has sent since. Call "start turn" sends the conversation the message "bash: sleep 8" through the API (not through the page), starting a turn that runs for 8 seconds. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. data-testid "message-input" should be visible (allow 30s). 3. Click ".model-picker-inline .p-select-label": ".model-picker-panel" should be visible. 4. Call "mark settings". 5. Call "start turn". 6. data-testid "agent-thinking" should be visible (allow 20s). 7. There should be no ".model-picker-panel" (count 0). 8. Call "settings since mark": it must return "0".`, func(s *shelley) {
		s.viewport(1280, 800)
		popoversSettingsRequests(s)
		c := s.conversation("echo open picker", "")
		s.call("start turn", func() string {
			s.api("POST", "/api/conversation/"+c.ID+"/chat", map[string]string{"message": "bash: sleep 8", "model": "predictable"}, nil)
			return "started"
		})
		s.v("SLUG", c.Slug)
	})
}

func TestE2EPopoversReadoutPickerDisabledWhileWorking(t *testing.T) {
	e2e(t, `The status readout's model picker is disabled while the agent works. The screen is 1280x800. "The model" is ".model-picker-inline .p-select-label"; "the picker" is ".model-picker-inline". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. data-testid "message-input" should be visible (allow 30s). 3. The model should be visible, and the picker must not have the class "p-disabled". 4. Fill the message input with "bash: sleep 8" and click data-testid "send-button". 5. data-testid "agent-thinking" should be visible (allow 20s). 6. The picker should have the class "p-disabled". 7. Click where the model is, ignoring that it is disabled: a disabled picker takes no pointer events, so click its wrapper ".status-readout-model" at the model's center (offsets from the wrapper's top-left corner to the center of the model's bounding box). 8. There should be no ".model-picker-panel" (count 0). 9. The model's computed cursor must be exactly "default", and the picker's computed opacity, as a number, must be < 1 and > 0.4. 10. The model's aria-description attribute must contain "switch models". 11. Move the pointer to the viewport point (0, 0) (a hover without a selector), then hover ".status-readout-model": ".p-tooltip-text" should contain "switch models". 12. Move the pointer to the viewport point (0, 0) again: there should be no ".p-tooltip-text" (count 0). 13. Click ".context-usage-label": ".chat-context-popup" should be visible. 14. The first visible ".status-readout-cwd" should be disabled, and the computed text-decoration-line of its ".status-readout-cwd-path" must be exactly "none".`, func(s *shelley) {
		s.viewport(1280, 800)
		s.v("SLUG", s.conversation("echo busy picker", "").Slug)
	})
}
