package test

import "testing"

// Ported from ui/e2e/message-input-alignment.spec.ts.

// inputAlignmentJS gives pages window.composerAligned(ids), which returns
// "ok" when the composer form is as tall as the message input (within half a
// pixel) and each control with a data-testid in ids is vertically centred on
// the input (within 2.5px), and otherwise what is wrong.
const inputAlignmentJS = `window.composerAligned = (ids) => {
  const rect = (id) => {
    const el = document.querySelector('[data-testid="' + id + '"]');
    if (!el) throw new Error('missing ' + id);
    return el.getBoundingClientRect();
  };
  const form = document.querySelector('.message-input-form');
  if (!form) throw new Error('missing .message-input-form');
  const input = rect('message-input');
  const formHeight = form.getBoundingClientRect().height;
  if (!(Math.abs(formHeight - input.height) < 0.5)) {
    return 'form height ' + formHeight + ' vs input height ' + input.height;
  }
  const centre = (r) => (r.top + r.bottom) / 2;
  for (const id of ids) {
    const r = rect(id);
    if (!(Math.abs(centre(r) - centre(input)) < 2.5)) {
      return id + ' centre ' + centre(r) + ' vs input centre ' + centre(input);
    }
  }
  return 'ok';
};`

const inputAlignmentSetup = `window.composerAligned(ids) returns "ok" when the composer form is as tall as the message input and every control whose data-testid is in ids is vertically centred on the input, or else what is wrong. `

func TestE2EMessageInputAlignmentNewConversation(t *testing.T) {
	e2e(t, inputAlignmentSetup+`On a desktop, the new-conversation composer aligns the textarea and its actions. 1. Navigate to /new. 2. The message input (data-testid "message-input"), the attach button (data-testid "attach-button") and the send button (data-testid "send-button") should be visible. 3. Eval window.composerAligned(["attach-button", "send-button"]) with expect "ok".`, func(s *shelley) {
		s.device(pixel5Desktop(1280, 720))
		s.initScript(inputAlignmentJS)
	})
}

func TestE2EMessageInputAlignmentConversation(t *testing.T) {
	e2e(t, inputAlignmentSetup+`On a desktop, a conversation's composer aligns the textarea and the split-send actions. 1. Navigate to /c/alignment. 2. The message input (data-testid "message-input"), the attach button (data-testid "attach-button"), the send button (data-testid "send-button") and the send options button (data-testid "send-options-button") should be visible. 3. Eval window.composerAligned(["attach-button", "send-button", "send-options-button"]) with expect "ok".`, func(s *shelley) {
		s.device(pixel5Desktop(1280, 720))
		s.initScript(inputAlignmentJS)
		s.conversation("composer alignment fixture", "alignment")
	})
}
