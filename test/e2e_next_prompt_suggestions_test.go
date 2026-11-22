package test

import (
	"path/filepath"
	"testing"
)

// Ported from ui/e2e/next-prompt-suggestions.spec.ts.

// After each turn, the suggested next message shows, labeled, above the
// empty composer: typing hides it, Tab or a click takes it, a new turn
// replaces it, and a new generation ends it.

// nextPromptSuggestions clears the server's override of the
// next-prompt-suggestions flag (the harness turns suggestions off for every
// other test), so the default applies, and starts the conversation /c/basil
// whose one turn was "echo: water the basil", in cwd if given.
func nextPromptSuggestions(s *shelley, cwd string) convo {
	s.do("DELETE", "/feature-flags", map[string]any{"name": "next-prompt-suggestions"})
	body := map[string]any{"message": "echo: water the basil", "model": "predictable", "cwd": s.cwd}
	if cwd != "" {
		body["cwd"] = cwd
	}
	return s.conversationWith(body, "basil")
}

// nextPromptBetweenJS gives pages window.suggestionBetween(above), which
// returns "ok" when the suggestion's box starts below the bottom of the
// first element matching above and ends above the message input's top,
// and otherwise the boxes.
const nextPromptBetweenJS = `window.suggestionBetween = (above) => {
  const box = (el) => el.getBoundingClientRect();
  const s = box(document.querySelector('[data-testid="next-prompt-suggestion"]'));
  const a = box(document.querySelector(above));
  const i = box(document.querySelector('[data-testid="message-input"]'));
  if (s.top >= a.bottom && s.bottom <= i.top) return 'ok';
  return 'suggestion ' + s.top + '..' + s.bottom + ', ' + above + ' bottom ' + a.bottom + ', input top ' + i.top;
};`

const nextPromptSetup = `"The suggestion" is [data-testid="next-prompt-suggestion"]; "the input" is the message input [data-testid="message-input"]. `

func TestE2ENextPromptSuggestionsSuggestsNextMessage(t *testing.T) {
	e2e(t, useful(0), nextPromptSetup+`On a desktop, the conversation suggests the next message: typing hides it, Tab takes it, a new turn replaces it, a click takes it, and a new generation ends it. window.suggestionBetween(above) returns "ok" when the suggestion sits below the element matching above and above the input. Perform the steps in order. 1. Navigate to /c/basil. 2. The suggestion's whitespace-collapsed, trimmed text should come to match /^Suggestion\s*echo: More about water the basil/ (eval with expect "true"; it starts with the label "Suggestion"). 3. Eval window.suggestionBetween(".status-bar-content") with expect "ok". 4. The input should be enabled. Fill it with "x"; the suggestion should become hidden. Fill it with ""; the suggestion should become visible. 5. Press Tab in the input. Its value should become exactly "echo: More about water the basil", it should have focus, and the suggestion should be hidden. 6. Press Enter in the input. Inside a message (data-testid "message"), an element whose text is exactly "More about water the basil" should become visible. 7. The suggestion should come to contain "echo: More about More about water the basil". 8. Click the suggestion. The input's value should become exactly "echo: More about More about water the basil", and it should have focus. 9. Fill the input with "/clear" and click the send button (data-testid "send-button"). There should be exactly one ".generation-divider", and the suggestion should become hidden.`, func(s *shelley) {
		s.device(pixel5Desktop(1280, 800))
		s.initScript(nextPromptBetweenJS)
		nextPromptSuggestions(s, "")
	})
}

func TestE2ENextPromptSuggestionsPerConversation(t *testing.T) {
	e2e(t, useful(0), nextPromptSetup+`On a desktop, each conversation shows its own suggestion, and an archived one, with no composer, shows none. Call "start other" starts another conversation, whose one turn was "echo: prune the rosemary", and makes {{OTHER_ID}} its id; call "archive" archives /c/basil, whose id is {{BASIL_ID}}. Perform the steps in order. 1. Navigate to /c/basil. 2. The suggestion should contain "echo: More about water the basil". 3. Call "start other". 4. Click its drawer row, .conversation-item[data-conversation-id="{{OTHER_ID}}"] (wait for it to appear). The suggestion should come to contain "echo: More about prune the rosemary". 5. Click the drawer row .conversation-item[data-conversation-id="{{BASIL_ID}}"]. The suggestion should come to contain "echo: More about water the basil". 6. Call "archive". 7. Reload the page. The text "This conversation is archived." should be visible, and the suggestion should be hidden (or absent).`, func(s *shelley) {
		s.device(pixel5Desktop(1280, 800))
		basil := nextPromptSuggestions(s, "")
		s.v("BASIL_ID", basil.ID)
		s.call("start other", func() string {
			other := s.conversation("echo: prune the rosemary", "")
			s.v("OTHER_ID", other.ID)
			return "started"
		})
		s.call("archive", func() string {
			s.api("POST", "/api/conversation/"+basil.ID+"/archive", nil, nil)
			return "archived"
		})
	})
}

func TestE2ENextPromptSuggestionsWithoutFlags(t *testing.T) {
	e2e(t, useful(0), nextPromptSetup+`The page shows suggestions by default, before or without the feature flags: the page's requests for /feature-flags fail. 1. Navigate to /c/basil. 2. The suggestion should contain "echo: More about water the basil".`, func(s *shelley) {
		s.route("/feature-flags", abort)
		nextPromptSuggestions(s, "")
	})
}

func TestE2ENextPromptSuggestionsSkillHides(t *testing.T) {
	e2e(t, useful(0), nextPromptSetup+`A picked skill, like typed text, leaves the composer not empty, so the suggestion hides. The conversation's directory has a skill named "draft-review". 1. Navigate to /c/basil. 2. The suggestion should be visible. 3. Fill the input with "/skills draft". 4. Click the first option ([role="option"]) in the skill picker ([data-testid="skill-picker"]). 5. The skill chip ([data-testid="skill-chip"]) should contain "draft-review". 6. The input's value should be exactly "", and the suggestion should be hidden.`, func(s *shelley) {
		dir := filepath.Join(s.dir, "suggest")
		s.write(filepath.Join(dir, ".skills", "draft-review", "SKILL.md"), "---\nname: draft-review\ndescription: Review\n---\nRead.\n")
		nextPromptSuggestions(s, dir)
	})
}

func TestE2ENextPromptSuggestionsPhoneTap(t *testing.T) {
	e2e(t, useful(0), nextPromptSetup+`A phone has no Tab key: a tap on the suggestion takes it. window.suggestionBetween(above) returns "ok" when the suggestion sits below the element matching above and above the input. 1. Navigate to /c/basil. 2. The suggestion should be visible. 3. Eval window.suggestionBetween(".message-controls-status-slot") with expect "ok". 4. Tap the suggestion: a touch step "start" on it, immediately followed by a touch step "end". 5. The input's value should become exactly "echo: More about water the basil", and the suggestion should become hidden.`, func(s *shelley) {
		s.viewport(320, 640)
		s.initScript(nextPromptBetweenJS)
		nextPromptSuggestions(s, "")
	})
}
