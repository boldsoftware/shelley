package test

import (
	"path/filepath"
	"strings"
	"testing"
)

// Ported from ui/e2e/next-prompt-suggestions.spec.ts.

// After each turn, the suggested next message shows in the empty text box,
// in place of its placeholder: typing hides it, Tab or its button takes it,
// a new turn replaces it, and a new generation ends it.

// nextPromptSuggestions clears the server's override of the
// next-prompt-suggestions flag (the harness turns suggestions off for every
// other test), so the default applies, and starts the conversation /c/basil
// whose one turn was "echo: water the basil", in cwd if given.
func nextPromptSuggestions(s *shelley, cwd string) convo {
	return nextPromptSuggestionsFor(s, cwd, "echo: water the basil")
}

// nextPromptSuggestionsFor is nextPromptSuggestions with the turn's message.
func nextPromptSuggestionsFor(s *shelley, cwd, message string) convo {
	s.do("DELETE", "/feature-flags", map[string]any{"name": "next-prompt-suggestions"})
	body := map[string]any{"message": message, "model": "predictable", "cwd": s.cwd}
	if cwd != "" {
		body["cwd"] = cwd
	}
	return s.conversationWith(body, "basil")
}

// nextPromptJS gives pages helpers that return "ok" or what's wrong:
//
//   - suggestionInside(): the suggestion fills the message input's box, the
//     input has no placeholder, and the two share what decides where text
//     wraps, so taking the suggestion doesn't move it.
//   - useOnLastLine(): the suggestion's button sits on the input's last line
//     of text, inside the input, and is what a tap at its middle hits.
//   - markInput() then inputSettled(maxShrink): the input kept its width and
//     bottom edge and grew no taller, shrinking by at most maxShrink pixels.
//   - inputTallerThanLines(n), inputNoTallerThan(px), suggestionCutOff().
//
// suggestionTextPoint() is a point on the suggestion's text, well away from
// its button.
const nextPromptJS = `(() => {
  const q = (id) => document.querySelector('[data-testid="' + id + '"]');
  const box = (el) => el.getBoundingClientRect();
  const fmt = (r) => JSON.stringify({x: r.x, y: r.y, w: r.width, h: r.height});
  const metrics = ['box-sizing', 'max-height', 'padding-top', 'padding-right', 'padding-bottom', 'padding-left',
    'border-top-width', 'border-right-width', 'border-bottom-width', 'border-left-width', 'font-family',
    'font-size', 'font-weight', 'line-height', 'letter-spacing', 'word-spacing', 'white-space', 'overflow-wrap'];
  window.suggestionInside = () => {
    const s = q('next-prompt-suggestion'), i = q('message-input');
    if (!s || !i) return 'missing suggestion or input';
    const a = box(s), b = box(i);
    for (const k of ['x', 'y', 'width', 'height']) {
      if (Math.abs(a[k] - b[k]) > 1) return 'suggestion ' + fmt(a) + ' vs input ' + fmt(b);
    }
    if (i.hasAttribute('placeholder')) return 'input has placeholder ' + JSON.stringify(i.getAttribute('placeholder'));
    const ss = getComputedStyle(s), is = getComputedStyle(i);
    for (const m of metrics) {
      if (ss.getPropertyValue(m) !== is.getPropertyValue(m)) return m + ': ' + ss.getPropertyValue(m) + ' vs ' + is.getPropertyValue(m);
    }
    return 'ok';
  };
  window.useOnLastLine = () => {
    const u = q('next-prompt-suggestion-use'), i = q('message-input');
    if (!u || !i) return 'missing button or input';
    const f = box(i), b = box(u), st = getComputedStyle(i);
    const bottom = f.bottom - parseFloat(st.paddingBottom) - parseFloat(st.borderBottomWidth);
    const line = parseFloat(st.lineHeight);
    if (b.top < bottom - line - 0.5 || b.bottom > bottom + 0.5 || b.right > f.right + 0.5) {
      return 'button ' + fmt(b) + ', input ' + fmt(f) + ', last line ' + (bottom - line) + '..' + bottom;
    }
    const hit = document.elementFromPoint(b.x + b.width / 2, b.y + b.height / 2);
    if (!u.contains(hit)) return 'button covered by ' + (hit ? hit.outerHTML.slice(0, 80) : 'nothing');
    return 'ok';
  };
  window.markInput = () => { window.__inputBefore = box(q('message-input')); return 'ok'; };
  window.inputSettled = (maxShrink) => {
    const a = window.__inputBefore, b = box(q('message-input'));
    const ok = Math.abs(a.x - b.x) <= 1 && Math.abs(a.width - b.width) <= 1 && Math.abs(a.bottom - b.bottom) <= 1 &&
      b.height <= a.height + 0.5 && b.height >= a.height - maxShrink;
    return ok ? 'ok' : 'before ' + fmt(a) + ', after ' + fmt(b);
  };
  window.inputTallerThanLines = (n) => {
    const i = q('message-input'), h = box(i).height, line = parseFloat(getComputedStyle(i).lineHeight);
    return h > n * line ? 'ok' : 'input ' + h + 'px, line ' + line + 'px';
  };
  window.inputNoTallerThan = (px) => {
    const r = box(q('message-input'));
    return r.height <= px && r.bottom <= innerHeight ? 'ok' : 'input ' + fmt(r) + ', viewport ' + innerHeight;
  };
  window.suggestionCutOff = () => {
    const s = q('next-prompt-suggestion');
    return s.scrollHeight > s.clientHeight ? 'ok' : 'scrollHeight ' + s.scrollHeight + ', clientHeight ' + s.clientHeight;
  };
  window.suggestionTextPoint = () => {
    const r = box(q('next-prompt-suggestion').querySelector('span'));
    return {x: r.left + 10, y: r.top + 10};
  };
})();`

const nextPromptSetup = `"The suggestion" is [data-testid="next-prompt-suggestion"]; "the input" is the message input [data-testid="message-input"]. `

// nextPromptLayout introduces the suggestion's button and nextPromptJS.
const nextPromptLayout = nextPromptSetup + `"The button" is the suggestion's button [data-testid="next-prompt-suggestion-use"]. The window.* helpers return "ok" or what is wrong; check each with an eval step expecting "ok". `

func TestE2ENextPromptSuggestionsSuggestsNextMessage(t *testing.T) {
	e2e(t, useful(0), nextPromptLayout+`On a desktop, the conversation suggests the next message in the empty input: typing hides it, Tab takes it, a new turn replaces it, a click on its text is a click in the input, its button takes it, and a new generation ends it. Perform the steps in order. 1. Navigate to /c/basil. 2. The suggestion should come to contain "echo: More about water the basil". 3. Eval the button's innerText; it should be exactly "Tab". Its aria-label should be exactly "Use (Tab)". 4. Eval window.suggestionInside(), then window.useOnLastLine(), then window.markInput(). 5. The input should be enabled. Fill it with "x"; the suggestion should become hidden. Fill it with ""; the suggestion should become visible. 6. Press Tab in the input. Its value should become exactly "echo: More about water the basil", it should have focus, and the suggestion should be hidden. Eval window.inputSettled(1.5). 7. Press Enter in the input. Inside a message (data-testid "message"), an element whose text is exactly "More about water the basil" should become visible. 8. The suggestion should come to contain "echo: More about More about water the basil". 9. Blur the input (eval document.activeElement.blur()). Click at the point window.suggestionTextPoint(). The input should have focus and its value should be exactly "". 10. Blur the input again, then click the button. The input's value should become exactly "echo: More about More about water the basil", and it should have focus. 11. Fill the input with "/clear" and click the send button (data-testid "send-button"). There should be exactly one ".generation-divider", and the suggestion should become hidden.`, func(s *shelley) {
		s.device(pixel5Desktop(1280, 800))
		s.initScript(nextPromptJS)
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

const nextPromptPhoneReply = "trim the tomatoes, feed the lemon tree, and repot the fig by the window"

func TestE2ENextPromptSuggestionsPhoneTap(t *testing.T) {
	e2e(t, useful(0), nextPromptLayout+`A phone has no Tab key: the suggestion wraps in full in the input, a tap on its text is a tap in the input, and a tap on its button, which says "Use", takes it. Perform the steps in order. 1. Navigate to /c/basil. 2. The suggestion should come to contain "echo: More about {{REPLY}}". 3. Eval the button's innerText; it should be exactly "Use". 4. Eval window.suggestionInside(), window.useOnLastLine(), window.inputTallerThanLines(2) and window.markInput(). 5. Tap at the point window.suggestionTextPoint(): a touch step "start" at that point, immediately followed by a touch step "end". The input should have focus and its value should be exactly "". 6. Tap the button: a touch step "start" on it, immediately followed by a touch step "end". 7. The input's value should become exactly "echo: More about {{REPLY}}", and the suggestion should become hidden. 8. Eval window.inputSettled(28).`, func(s *shelley) {
		s.viewport(320, 640)
		s.initScript(nextPromptJS)
		s.v("REPLY", nextPromptPhoneReply)
		nextPromptSuggestionsFor(s, "", "echo: "+nextPromptPhoneReply)
	})
}

func TestE2ENextPromptSuggestionsTooLong(t *testing.T) {
	// A phone with its keyboard up leaves little room for the box.
	reply := strings.Repeat("给番茄修枝，给柠檬树施肥，再把无花果移到窗边的大盆里，然后扫一扫门廊。", 3)
	expected := "echo: More about " + string([]rune(reply)[:100])
	e2e(t, useful(0), nextPromptLayout+`On a short phone screen, a suggestion too long for the input is cut off like overflowing text, no taller than the input gets (40% of the 240px screen), and its button stays in sight at the bottom. Perform the steps in order. 1. Navigate to /c/basil. 2. The suggestion should come to contain "{{EXPECTED}}". 3. Eval window.suggestionInside(), window.inputNoTallerThan(97), window.suggestionCutOff(), window.useOnLastLine() and window.markInput(). 4. Tap the button: a touch step "start" on it, immediately followed by a touch step "end". 5. The input's value should become exactly "{{EXPECTED}}". 6. Eval window.inputSettled(0.5).`, func(s *shelley) {
		s.viewport(320, 240)
		s.initScript(nextPromptJS)
		s.v("EXPECTED", expected)
		nextPromptSuggestionsFor(s, "", "echo: "+reply)
	})
}
