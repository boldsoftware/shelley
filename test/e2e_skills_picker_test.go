package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Ported from ui/e2e/skills-picker.spec.ts.

// skillsPicker starts the conversation /c/skills, whose one turn was
// "echo: fixture", in a directory with the skill draft-review.
func skillsPicker(s *shelley) convo {
	s.write(".skills/draft-review/SKILL.md", "---\nname: draft-review\ndescription: Review a draft for clarity\n---\nRead the draft.\n")
	return s.conversation("echo: fixture", "skills")
}

// skillsPickerMCP has the page list three MCP servers: web and library each
// offer a tool "search" (described "Search <server> sources", with a
// required string parameter query), and locked answers 401 "Log in to
// locked".
func skillsPickerMCP(s *shelley) {
	s.route("/api/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []any{map[string]any{"name": "web"}, map[string]any{"name": "library"}, map[string]any{"name": "locked"}})
	})
	s.route("/api/mcp/servers/{server}/tools", func(w http.ResponseWriter, r *http.Request) {
		server := r.PathValue("server")
		if server == "locked" {
			http.Error(w, "Log in to locked", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"tools": []any{map[string]any{
			"name":        "search",
			"description": "Search " + server + " sources",
			"inputSchema": map[string]any{
				"properties": map[string]any{"query": map[string]any{"type": "string", "description": "Search terms"}},
				"required":   []string{"query"},
			},
		}}})
	})
}

// skillsPickerChat records the message and skills of each chat the page
// posts to c, and reports the last with call "posted chat".
func skillsPickerChat(s *shelley, c convo, report func(message string, skills []string) string) {
	var mu sync.Mutex
	var last *struct {
		Message string   `json:"message"`
		Skills  []string `json:"skills"`
	}
	s.route("POST /api/conversation/"+c.ID+"/chat", func(w http.ResponseWriter, r *http.Request) {
		posted := new(struct {
			Message string   `json:"message"`
			Skills  []string `json:"skills"`
		})
		json.Unmarshal(readBody(r), posted)
		mu.Lock()
		last = posted
		mu.Unlock()
		s.pass.ServeHTTP(w, r)
	})
	s.call("posted chat", func() string {
		mu.Lock()
		defer mu.Unlock()
		if last == nil {
			return "nothing posted"
		}
		return report(last.Message, last.Skills)
	})
}

// skillsPickerBoxJS gives pages window.pickerBox(), the skill picker's
// bounding box as JSON {x, y, width, height}, and
// window.pickerPlacement(), its top relative to the visual viewport and
// the gap down to the message input, both rounded, as JSON {top, gap}.
const skillsPickerBoxJS = `window.pickerBox = () => {
  const r = document.querySelector('[data-testid="skill-picker"]').getBoundingClientRect();
  return JSON.stringify({ x: r.x, y: r.y, width: r.width, height: r.height });
};
window.pickerPlacement = () => {
  const r = document.querySelector('[data-testid="skill-picker"]').getBoundingClientRect();
  const input = document.querySelector('[data-testid="message-input"]').getBoundingClientRect();
  return JSON.stringify({
    top: Math.round(r.top - (window.visualViewport?.offsetTop ?? 0)),
    gap: Math.round(input.top - r.bottom),
  });
};`

const skillsPickerSetup = `"The input" is the message input [data-testid="message-input"]; "the picker" is the skill picker [data-testid="skill-picker"]; its options are its [role="option"] elements; "the chip" is [data-testid="skill-chip"]. `

func TestE2ESkillsPickerLongMCPDetailsKeepPickerFixed(t *testing.T) {
	e2e(t, skillsPickerSetup+`On a wide screen, hovering long MCP tool details keeps the picker's height and position fixed. The page's one MCP server, web, has the tool "search" with a very long description ("Long MCP tool description. " repeated). window.pickerBox() returns the picker's bounding box as JSON. Perform the steps in order. 1. Navigate to /c/skills. 2. Fill the input with "/skills " (with the trailing space). 3. Exactly one option should contain "search". 4. Hover the option containing "draft-review". 5. Eval window.pickerBox(); its height must be exactly 420: eval JSON.parse(window.pickerBox()).height with expect "420". 6. Save the box: eval window.savedBox = window.pickerBox(). 7. Hover the option containing "search". 8. The picker's ".skill-picker-detail" should contain "Long MCP tool description.". 9. The box is unchanged: eval window.pickerBox() === window.savedBox with expect "true". 10. The detail overflows and scrolls: eval (() => { const el = document.querySelector('[data-testid="skill-picker"] .skill-picker-detail'); return el.scrollHeight > el.clientHeight; })() with expect "true". 11. Hover the option containing "draft-review" again. 12. The box is still unchanged: eval window.pickerBox() === window.savedBox with expect "true".`, func(s *shelley) {
		s.viewport(1280, 800)
		s.initScript(skillsPickerBoxJS)
		s.route("/api/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []any{map[string]any{"name": "web"}})
		})
		s.route("/api/mcp/servers/web/tools", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"tools": []any{map[string]any{
				"name":        "search",
				"description": strings.Repeat("Long MCP tool description. ", 200),
				"inputSchema": map[string]any{"properties": map[string]any{}},
			}}})
		})
		skillsPicker(s)
	})
}

func TestE2ESkillsPickerChoosingAddsChip(t *testing.T) {
	e2e(t, skillsPickerSetup+`On a wide screen, choosing a skill adds a chip, clears "/skills" and sends nothing. Call "start recording" starts recording the page's POST requests, other than the activity heartbeat and read receipts; call "recorded posts" returns them, or "none". Perform the steps in order. 1. Navigate to /c/skills, and wait for the input to be visible. 2. Call "start recording". 3. Fill the input with "/skills draft". 4. Click the first option. 5. The chip should contain "draft-review". 6. The input's value should be exactly "". 7. The picker should be hidden (or absent). 8. Call "recorded posts" with expect "none".`, func(s *shelley) {
		s.viewport(1280, 800)
		skillsPicker(s)
		var mu sync.Mutex
		from := -1
		s.call("start recording", func() string {
			mu.Lock()
			defer mu.Unlock()
			from = len(s.requestLog())
			return "recording"
		})
		s.call("recorded posts", func() string {
			mu.Lock()
			defer mu.Unlock()
			if from < 0 {
				return "not recording"
			}
			var sent []string
			for _, req := range s.requestLog()[from:] {
				method, uri, _ := strings.Cut(req, " ")
				// Neither the activity heartbeat nor the read receipt is a send.
				if method == "POST" && !strings.HasSuffix(uri, "/api/activity") && !strings.HasSuffix(uri, "/read") {
					sent = append(sent, uri)
				}
			}
			if len(sent) == 0 {
				return "none"
			}
			return strings.Join(sent, ", ")
		})
	})
}

func TestE2ESkillsPickerChipRemoved(t *testing.T) {
	e2e(t, skillsPickerSetup+`On a wide screen, the chip can be removed. 1. Navigate to /c/skills. 2. Fill the input with "/skills draft". 3. Click the first option. 4. The chip should contain "draft-review". 5. Click the button labeled "Remove skill draft-review" (button[aria-label="Remove skill draft-review"]). 6. There should be no chips: [data-testid="skill-chip"] count 0.`, func(s *shelley) {
		s.viewport(1280, 800)
		skillsPicker(s)
	})
}

func TestE2ESkillsPickerSendCarriesSkill(t *testing.T) {
	prefix := "Use the following skills: `draft-review`.\n\n"
	e2e(t, skillsPickerSetup+`On a wide screen, sending carries the skill, and the stored message has the skills prefix. Call "posted chat" returns the message and skills of the last chat the page posted, as JSON; call "stored message" returns the conversation's last stored user message as a JSON string. Perform the steps in order. 1. Navigate to /c/skills. 2. Fill the input with "/skills draft". 3. Click the first option. 4. The chip should contain "draft-review". 5. Fill the input with "echo: please review". 6. Click the send button (data-testid "send-button"). 7. Call "posted chat" with expect `+mustJSON(mustJSON(map[string]any{"message": "echo: please review", "skills": []string{"draft-review"}}))+` (a JSON string holding the expected output). 8. There should be no chips: [data-testid="skill-chip"] count 0. 9. Exactly one element on the page has the text "Use the following skills:" (it may contain more text), and it is visible. 10. Call "stored message" with expect `+mustJSON(mustJSON(prefix+"echo: please review"))+` (a JSON string holding the expected output).`, func(s *shelley) {
		s.viewport(1280, 800)
		c := skillsPicker(s)
		skillsPickerChat(s, c, func(message string, skills []string) string {
			return mustJSON(map[string]any{"message": message, "skills": skills})
		})
		s.call("stored message", func() string {
			var state struct {
				Messages []struct {
					Type    string `json:"type"`
					LLMData string `json:"llm_data"`
				} `json:"messages"`
			}
			s.api("GET", "/api/conversation/"+c.ID, nil, &state)
			last := "no user messages"
			for _, m := range state.Messages {
				if m.Type != "user" {
					continue
				}
				var data struct {
					Content []struct {
						Text string
					}
				}
				json.Unmarshal([]byte(m.LLMData), &data)
				if len(data.Content) > 0 {
					last = data.Content[0].Text
				}
			}
			return mustJSON(last)
		})
	})
}

func TestE2ESkillsPickerMCPTools(t *testing.T) {
	e2e(t, skillsPickerSetup+`On a wide screen, MCP tools show short names, server badges, schemas and partial errors, and send the exact tool. The page lists MCP servers web and library, each with a tool "search" taking a required "query", and locked, whose tools fail to load. Call "posted chat" returns JSON {"skills": the last posted chat's skills, "requests web.search": whether its message starts with "echo: find sources", a blank line, then 'Use the MCP tool "web.search"'}. Perform the steps in order. 1. Navigate to /c/skills. 2. Fill the input with "/skills search". 3. There should be exactly 2 options. 4. The first option's ".truncate" text should be exactly "search", and its ".skill-mcp-badge" text exactly "web". 5. The last option's ".skill-mcp-badge" text should be exactly "library". 6. The picker's ".skill-picker-warning" should contain "locked". 7. The picker's ".skill-mcp-parameters" should contain "query" and contain "required". 8. The picker's ".skill-mcp-activate" should contain "'web.search'" (with the single quotes). 9. Click the first option. 10. The chip's ".skill-chip-label" text should be exactly "search", and the chip's ".skill-mcp-badge" text exactly "web". 11. Inside the chip, a button whose accessible name is exactly "Remove tool search from web" (button[aria-label="Remove tool search from web"]) should be visible. 12. Fill the input with "echo: find sources". 13. Click the send button (data-testid "send-button"). 14. Call "posted chat" with expect "{\"skills\":[],\"requests web.search\":true}". 15. There should be no chips: [data-testid="skill-chip"] count 0. 16. The last user message (the last ".message-user") should contain 'Use the MCP tool "web.search"'.`, func(s *shelley) {
		s.viewport(1280, 800)
		skillsPickerMCP(s)
		c := skillsPicker(s)
		skillsPickerChat(s, c, func(message string, skills []string) string {
			if skills == nil {
				skills = []string{}
			}
			// Keys in this order.
			return `{"skills":` + mustJSON(skills) + `,"requests web.search":` +
				mustJSON(strings.HasPrefix(message, "echo: find sources\n\nUse the MCP tool \"web.search\"")) + `}`
		})
	})
}

func TestE2ESkillsPickerMobileFillsSpace(t *testing.T) {
	e2e(t, skillsPickerSetup+`On a phone, the skills picker fills the space above the composer and keeps typing focus in the input. The page lists MCP servers web and library, each with a tool "search", and locked, whose tools fail to load. window.pickerBox() returns the picker's bounding box as JSON {x, y, width, height}; window.pickerPlacement() returns JSON {"top": its top relative to the visual viewport, "gap": the gap from its bottom down to the input's top}, both rounded. Perform the steps in order. 1. Navigate to /c/skills. 2. Fill the input with "/skills " (with the trailing space). 3. The input should have focus, and the picker should be visible and contain no search box: no element with role "searchbox" inside the picker (count 0). 4. The picker's top should come to be 12 to within half a pixel: eval Math.abs(JSON.parse(window.pickerBox()).y - 12) < 0.5 with expect "true". 5. Its height must be more than 420: eval JSON.parse(window.pickerBox()).height > 420 with expect "true". 6. Type "a-long-skill-query-that-wraps-across-multiple-lines" into the input key by key (a type step, not fill; the input keeps focus). 7. The picker should contain "No matching skills". 8. Eval window.pickerPlacement() and JSON.parse it: its top should come to be 12 (eval JSON.parse(window.pickerPlacement()).top with expect "12"). 9. Tap the "No matching skills" text in the picker: a touch step "start" on it immediately followed by a touch step "end". 10. The input should have focus, and the picker should be visible. 11. Fill the input with "/skills " (with the trailing space), then type "draft-review" key by key. 12. There should be exactly one option, containing "draft-review", and the input should have focus. 13. Resize the viewport to 393x500 (set_viewport width and height only). 14. Eval window.pickerPlacement() with expect "{\"top\":12,\"gap\":6}". 15. The picker's height must be more than 200: eval JSON.parse(window.pickerBox()).height > 200 with expect "true". 16. Click the picker's "Close skills" button (button[aria-label="Close skills"]). 17. The picker should become hidden (or absent), and the input should have focus.`, func(s *shelley) {
		s.initScript(skillsPickerBoxJS)
		skillsPickerMCP(s)
		skillsPicker(s)
	})
}

func TestE2ESkillsPickerMobileMCPFiltering(t *testing.T) {
	e2e(t, skillsPickerSetup+`On a phone, filtering MCP tools and selecting one keep focus in the input. The page lists MCP servers web and library, each with a tool "search", and locked, whose tools fail to load. Perform the steps in order. 1. Navigate to /c/skills. 2. Fill the input with "/skills " (with the trailing space); the input should have focus. 3. Type "library search" into the input key by key (a type step, not fill). 4. There should be exactly one option, and the picker's ".skill-mcp-badge" text should be exactly "library". 5. The input should have focus. 6. Click the option. 7. The chip should contain "search". 8. The input's value should be exactly "", and the input should have focus.`, func(s *shelley) {
		skillsPickerMCP(s)
		skillsPicker(s)
	})
}

func TestE2ESkillsPickerMobileMCPDistinct(t *testing.T) {
	e2e(t, skillsPickerSetup+`On a phone, the MCP tools named "search" stay distinct by their server badges, and both can be selected. The page lists MCP servers web and library, each with a tool "search" described "Search <server> sources", and locked, whose tools fail to load. Perform the steps in order. 1. Navigate to /c/skills. 2. For each server S of "web", then "library": fill the input with "/skills S search"; there should be exactly one option; the picker's ".skill-mcp-badge" text should be exactly S, and its ".skill-picker-description" text exactly "Search S sources"; click the option. 3. There should be exactly 2 chips, and the chips' ".skill-mcp-badge" texts, in order, should be exactly "web" and "library" (and no others).`, func(s *shelley) {
		skillsPickerMCP(s)
		skillsPicker(s)
	})
}
