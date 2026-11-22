package test

import (
	"net/http"
	"path/filepath"
	"syscall"
	"testing"
)

// Ported from ui/e2e/status-row.spec.ts.

// statusRowJS gives pages helpers that return "ok" or what is wrong:
//
//   - window.statusRowGeometry(width) checks the working status row at a
//     viewport width: the TOC button beside the model readout (desktop) or
//     Stop (phones), centred on the token count or Stop, inside the window;
//     no overflow; on phones the visible label shrinks to a pixel, Stop
//     sits at the far left, 24px wide and still, the token count right of
//     Stop and within 12px of the TOC, the directory and model readouts
//     hidden, and the send button above the textarea; on desktops the
//     label fits a line left of Stop, showing "Agent working...",
//     "working..." or "...". At 390px it notes where the token count ends.
//   - window.statusRowAria() checks that the agent-thinking indicator has
//     a status whose accessible text is "Agent working...".
//   - window.statusRowModelLocked() checks that the model picker in the
//     open menu is disabled while the agent works.
//   - window.statusRowIdleTokens() checks that the token count ends within
//     a pixel of where it ended at 390px while working.
const statusRowJS = `(() => {
  const visible = (e) => {
    const r = e.getBoundingClientRect();
    return r.width > 0 && r.height > 0 && getComputedStyle(e).visibility !== 'hidden';
  };
  const hidden = (selector) => [...document.querySelectorAll(selector)].every((e) => !visible(e));
  window.statusRowGeometry = (width) => {
    try {
      const rect = (selector) => {
        const el = [...document.querySelectorAll(selector)].find((e) => e.offsetWidth > 0);
        if (!el) throw new Error('Missing visible ' + selector + ' at ' + window.innerWidth + 'px');
        const r = el.getBoundingClientRect();
        return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, overflow: el.scrollWidth > el.clientWidth + 1 };
      };
      const text = document.querySelector('.animated-working');
      if (!text) return 'missing .animated-working';
      const g = {
        toc: rect('.toc-button'),
        model: window.innerWidth >= 768 ? rect('.status-readout-model') : null,
        status: text.offsetWidth > 0 ? rect('.animated-working') : null,
        stop: rect('.status-stop-button'),
        row: rect('.status-bar-active'),
        tokens: rect('.context-usage-label-tokens'),
        input: rect('.message-textarea'),
        send: rect('.message-send-wrapper'),
        text: [...text.querySelectorAll('span[aria-hidden="true"]')]
          .filter((e) => e.getClientRects().length > 0).map((e) => e.textContent).join(''),
      };
      const wrong = [];
      const check = (ok, what) => { if (!ok) wrong.push(what); };
      const phone = width <= 767;
      check(g.toc.left >= (phone ? g.stop.right : g.model.right), 'TOC overlaps ' + (phone ? 'Stop' : 'the model'));
      const anchor = phone ? g.stop : g.tokens;
      check(Math.abs((g.toc.top + g.toc.bottom - anchor.top - anchor.bottom) / 2) < 2, 'TOC not centred on ' + (phone ? 'Stop' : 'the token count'));
      check(g.toc.right <= width, 'TOC past the window');
      check(!g.row.overflow, 'row overflows');
      check(!g.tokens.overflow, 'token count overflows');
      if (phone) {
        if (!g.status) return 'no visible status label';
        check(g.status.right - g.status.left === 1 && g.status.bottom - g.status.top === 1, 'status label not 1x1');
        check(g.stop.left - g.row.left < 1, 'Stop not at the far left');
        check(g.tokens.left >= g.stop.right, 'token count overlaps Stop');
        check(g.toc.left - g.tokens.right < 12, 'token count far from the TOC');
        check(g.stop.right - g.stop.left === 24, 'Stop not 24px wide');
        check(g.send.bottom <= g.input.top, 'send not above the textarea');
        check(hidden('.status-readout-cwd'), 'directory readout shown');
        check(hidden('.status-readout-model'), 'model readout shown');
        const stop = document.querySelectorAll('.status-stop-button');
        check(stop.length === 1 && getComputedStyle(stop[0]).animationName === 'none', 'Stop animates');
      } else {
        if (!g.status) return 'no visible status label';
        check(g.status.bottom - g.status.top < 24, 'status label wraps');
        check(g.status.right <= g.stop.left, 'status label overlaps Stop');
        check(['Agent working...', 'working...', '...'].includes(g.text), 'label shows ' + JSON.stringify(g.text));
      }
      if (wrong.length) return width + 'px: ' + wrong.join('; ');
      if (width === 390) window.__workingTokensRight = g.tokens.right;
      return 'ok';
    } catch (e) {
      return String(e);
    }
  };
  window.statusRowAria = () => {
    const thinking = document.querySelector('[data-testid="agent-thinking"]');
    if (!thinking) return 'no agent-thinking';
    const statuses = [thinking, ...thinking.querySelectorAll('*')].filter((e) => e.getAttribute('role') === 'status' || e.tagName === 'OUTPUT');
    const spoken = (node) => {
      if (node.nodeType === Node.TEXT_NODE) return node.textContent;
      if (node.nodeType !== Node.ELEMENT_NODE || node.getAttribute('aria-hidden') === 'true' || getComputedStyle(node).display === 'none') return '';
      return [...node.childNodes].map(spoken).join('');
    };
    const texts = statuses.map((e) => spoken(e).replace(/\s+/g, ' ').trim());
    return texts.includes('Agent working...') ? 'ok' : 'statuses say ' + JSON.stringify(texts);
  };
  window.statusRowModelLocked = () => {
    const menus = document.querySelectorAll('.model-picker-menu');
    if (menus.length !== 1) return menus.length + ' model pickers';
    const menu = menus[0];
    if (!menu.classList.contains('p-disabled')) return 'not p-disabled';
    if (getComputedStyle(menu).opacity !== '0.5') return 'opacity ' + getComputedStyle(menu).opacity;
    const boxes = menu.querySelectorAll('[role="combobox"]');
    if (boxes.length !== 1) return boxes.length + ' comboboxes';
    if (boxes[0].getAttribute('aria-disabled') !== 'true') return 'combobox aria-disabled ' + boxes[0].getAttribute('aria-disabled');
    if (!/switch models/.test(boxes[0].getAttribute('aria-description') ?? '')) return 'aria-description ' + boxes[0].getAttribute('aria-description');
    return 'ok';
  };
  window.statusRowIdleTokens = () => {
    const tokens = document.querySelectorAll('.context-usage-label-tokens');
    if (tokens.length !== 1) return tokens.length + ' token counts';
    if (window.__workingTokensRight === undefined) return 'no working position';
    const right = tokens[0].getBoundingClientRect().right;
    return Math.abs(right - window.__workingTokensRight) < 1 ? 'ok' : 'idle ' + right + ', working ' + window.__workingTokensRight;
  };
})();`

func TestE2EStatusRowTOCSharesCompactRow(t *testing.T) {
	widths := "1280, 900, 800, 700, 650, 600, 575, 550, 525, 500, 460, 420, 390, 360 and 320"
	e2e(t, useful(0), `The table of contents button shares the compact status row across desktop and mobile widths, while the agent works and after. "The TOC" is the button named exactly "Conversation table of contents". Evals of window.statusRowGeometry(W), window.statusRowAria(), window.statusRowModelLocked() and window.statusRowIdleTokens() return "ok" or what is wrong; use each with expect "ok". "Hidden" means hidden or absent. Perform the steps in order. 1. Navigate to /c/contents-row. 2. The TOC should be visible. Mark it: eval document.querySelector('[aria-label="Conversation table of contents"]').dataset.mountIdentity = "original" (check first that exactly one element has that aria-label and it is the TOC). 3. There must be no TOC in the navigation cluster: ".chat-nav-cluster .toc-button" count 0. 4. Fill the message input (data-testid "message-input") with "{{BLOCK}}" and click the send button (data-testid "send-button"); the command blocks until Stop. 5. The agent-thinking indicator (data-testid "agent-thinking") should become visible. 6. For each width W of `+widths+` in turn, as one block of steps per width: a. resize the viewport to W x 800 (set_viewport width and height only); b. eval window.statusRowGeometry(W) with expect "ok"; c. eval window.statusRowAria() with expect "ok"; d. the TOC's data-mount-identity attribute should be "original" (it was never remounted); e. click the TOC; f. ".toc-popover" should be visible; g. press Escape; h. only when W is 390, then also: click the button named exactly "More options", eval window.statusRowModelLocked() with expect "ok", and press Escape. 7. Resize the viewport to 1280x800. A narrow conversation pane on a desktop shortens the label too: eval document.querySelector('.main-content').style.maxWidth = '320px'. 8. The first ".working-prefix" and the first ".working-word" should be hidden; ".animated-working .sr-only" should be visible. 9. Click the button named exactly "Stop". The agent-thinking indicator should become hidden, and the TOC should be visible. 10. Eval document.querySelector('.main-content').style.maxWidth = ''. 11. Resize the viewport to 390x800. ".status-ready" should be hidden; ".status-stop-button" count 0; ".context-usage-label" should be visible. 12. Eval window.statusRowIdleTokens() with expect "ok". 13. ".status-readout-cwd-leaf" and ".status-readout-cwd-path" should be hidden. 14. Emulate the light color scheme (emulate_media prefers-color-scheme light); the html element must not have the class "dark" (eval document.documentElement.classList.contains('dark') with expect "false"); ".status-ready" and ".status-readout-sep-cwd" should be hidden. 15. Emulate the dark color scheme; the html element should come to have the class "dark" (expect "true"); ".status-ready" and ".status-readout-sep-cwd" should be hidden. 16. Call "archive", then reload the page. 17. The message input should be hidden; ".toc-button" count should be exactly 1; the TOC should be visible.`, func(s *shelley) {
		s.viewport(1280, 800)
		s.initScript(statusRowJS)
		c := s.conversation("echo contents row", "contents-row")
		// Block on a pipe, not a timer: the turn stays working until Stop
		// is clicked.
		pipe := filepath.Join(s.dir, "gate")
		s.check(syscall.Mkfifo(pipe, 0o600))
		s.v("BLOCK", "bash: read -r line < "+pipe)
		s.call("archive", func() string {
			s.api("POST", "/api/conversation/"+c.ID+"/archive", nil, nil)
			return "archived"
		})
	})
}

// statusRowLayoutJS gives pages window.attachmentsLayout(minHeight), which
// checks that the attachments sit below the composer's controls and above
// the textarea and are taller than minHeight, and window.errorLayout(),
// which checks that the error is more than 36px tall, inside the form and
// above the textarea; each returns "ok" or what is wrong.
const statusRowLayoutJS = `(() => {
  const rect = (selector) => {
    const el = document.querySelector(selector);
    if (!el) throw new Error('missing ' + selector);
    return el.getBoundingClientRect();
  };
  window.attachmentsLayout = (minHeight) => {
    try {
      const controlsBottom = Math.max(rect('.message-controls-status-slot').bottom, rect('.message-send-wrapper').bottom);
      const a = rect('.message-attachments');
      const inputTop = rect('.message-textarea').top;
      if (a.top < controlsBottom) return 'attachments top ' + a.top + ' above the controls bottom ' + controlsBottom;
      if (a.bottom >= inputTop) return 'attachments bottom ' + a.bottom + ' not above the textarea top ' + inputTop;
      if (a.height <= minHeight) return 'attachments only ' + a.height + 'px tall';
      return 'ok';
    } catch (e) {
      return String(e);
    }
  };
  window.errorLayout = () => {
    try {
      const e = rect('.status-error');
      const formTop = rect('.message-input-form').top;
      const inputTop = rect('.message-textarea').top;
      if (e.height <= 36) return 'error only ' + e.height + 'px tall';
      if (e.top < formTop) return 'error top ' + e.top + ' above the form top ' + formTop;
      if (e.bottom >= inputTop) return 'error bottom ' + e.bottom + ' not above the textarea top ' + inputTop;
      return 'ok';
    } catch (e) {
      return String(e);
    }
  };
})();`

const statusRowError = "The server could not accept this message because the requested model is unavailable. Please select another model and try sending the message again."

func TestE2EStatusRowMobileAttachmentsAndErrors(t *testing.T) {
	e2e(t, useful(0), `A phone's composer keeps attachments and wrapped errors above the input. window.setInputFiles(selector, files) chooses files (a list of [name, content] pairs) on a file input; window.attachmentsLayout(minHeight) and window.errorLayout() return "ok" or what is wrong; use them with expect "ok". The page's uploads succeed at once, and its messages to this conversation fail with 503 and the text "`+statusRowError+`". "Hidden" means hidden or absent. Perform the steps in order. 1. Navigate to /c/layout. 2. The message input (data-testid "message-input") and ".context-usage-label" should be visible; ".status-ready" should be hidden. 3. Eval window.setInputFiles('input[type="file"]', [["attachment-1-0.txt", "layout fixture"]]). 4. ".message-attachment-uploading" count should be 0, and ".message-attachment-ready" count exactly 1. 5. For each width W of 390 then 320: resize the viewport to W x 800 (set_viewport width and height only), then eval window.attachmentsLayout(64) with expect "ok". 6. Eval window.setInputFiles('input[type="file"]', [["attachment-5-0.txt", "layout fixture"], ["attachment-5-1.txt", "layout fixture"], ["attachment-5-2.txt", "layout fixture"], ["attachment-5-3.txt", "layout fixture"], ["attachment-5-4.txt", "layout fixture"]]). 7. ".message-attachment-uploading" count should be 0, and ".message-attachment-ready" count exactly 6. 8. For each width W of 390 then 320: resize the viewport to W x 800, then eval window.attachmentsLayout(128) with expect "ok". 9. Remove every attachment: click the first ".message-attachment-remove" six times, waiting each time for the count of ".message-attachment-remove" to drop by one. 10. [data-testid="message-attachments"] count should be 0. 11. Fill the message input with "keep my draft" and click the send button (data-testid "send-button"). 12. ".message-controls-status-slot .status-error" should contain "`+statusRowError+`". 13. For each width W of 390 then 320: resize the viewport to W x 800, then eval window.errorLayout() with expect "ok". 14. Click ".message-controls-status-slot .status-button-text". 15. The message input and ".context-usage-label" should be visible, ".status-ready" hidden, and the message input's value exactly "keep my draft".`, func(s *shelley) {
		s.viewport(390, 800)
		s.initScript(inputFilesJS)
		s.initScript(statusRowLayoutJS)
		c := s.conversation("echo layout", "layout")
		s.route("/api/upload", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"path": "/tmp/composer-layout.txt"})
		})
		s.route("/api/conversation/"+c.ID+"/chat", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(statusRowError))
		})
	})
}

func TestE2EStatusRowContextCountOpensUsage(t *testing.T) {
	e2e(t, useful(0), `The context count opens the usage graphs on desktop and mobile widths. "The count" is ".context-usage-label"; "the popup" is ".chat-context-popup". "Hidden" means hidden or absent. Perform the steps in order. 1. Navigate to /c/usage. 2. For each width W of 1280, 768, 767, 390 and 320 in turn: a. resize the viewport to W x 800 (set_viewport width and height only); b. the count should be visible; c. only when W is below 768 (phones show the count alone, without the model and directory): ".status-readout-model" and ".status-readout-cwd" should be hidden; d. click the count; e. the popup should be visible and entirely within the viewport (all of it in view); f. ".chat-context-popup .token-cost-graph-svg" should be visible; g. press Escape; h. the popup should become hidden.`, func(s *shelley) {
		s.conversation("echo usage", "usage")
	})
}

// statusRowInterruptJS, given a conversation ID as window.__interrupted,
// has the page's conversation list, from its snapshot and its stream, say
// that conversation's turn was interrupted: marking a turn interrupted
// takes a server restart. It also gives pages window.countAt(), where the
// one visible token count ends ([right, top], rounded, as JSON), or what is
// wrong, and window.settledCountAt(), a promise of the same once no CSS
// transition is running.
const statusRowInterruptJS = `(() => {
  const id = window.__interrupted;
  const interrupt = (value) => {
    if (Array.isArray(value)) return value.map(interrupt);
    if (!value || typeof value !== 'object') return value;
    const object = Object.fromEntries(Object.entries(value).map(([key, field]) => [key, interrupt(field)]));
    if (object.conversation_id === id) object.turn_interrupted = true;
    return object;
  };
  const fetchNative = window.fetch;
  window.fetch = async (input, init) => {
    const response = await fetchNative(input, init);
    if (!String(input).endsWith('/conversations/snapshot')) return response;
    return Response.json(interrupt(await response.json()));
  };
  const EventSourceNative = window.EventSource;
  window.EventSource = class extends EventSourceNative {
    set onmessage(handler) {
      super.onmessage = handler && ((ev) => {
        const data = JSON.stringify(interrupt(JSON.parse(ev.data)));
        handler.call(this, new MessageEvent('message', { data }));
      });
    }
  };
  window.countAt = () => {
    const counts = [...document.querySelectorAll('.context-usage-label-tokens')].filter((e) => {
      const r = e.getBoundingClientRect();
      return r.width > 0 && r.height > 0 && getComputedStyle(e).visibility !== 'hidden';
    });
    if (counts.length !== 1) return counts.length + ' visible token counts';
    const r = counts[0].getBoundingClientRect();
    return JSON.stringify([Math.round(r.x + r.width), Math.round(r.y)]);
  };
  // The count arrives with the status slot that holds it, and the slot
  // switches the phone composer to its grid, whose row gap is wider. The
  // browser asks for reduced motion, and styles.css answers with a 0.01ms
  // transition on every property, so the gap can still be the old one when
  // the count first shows: measure once transitions have finished.
  window.settledCountAt = () => new Promise((resolve) => {
    const check = () => {
      if (document.getAnimations().some((animation) => animation instanceof CSSTransition)) {
        requestAnimationFrame(check);
      } else {
        resolve(window.countAt());
      }
    };
    check();
  });
})();`

func TestE2EStatusRowInterruptedKeepsCount(t *testing.T) {
	e2e(t, useful(0), `An interrupted conversation keeps the context count in place on phones. The page takes /c/interrupted's last turn for interrupted; /c/steady is an ordinary conversation. window.countAt() returns where the one visible token count (".context-usage-label-tokens") ends, as JSON [right, top], or what is wrong when there isn't exactly one; window.settledCountAt() is a promise of the same, read once no CSS transition is running (the phone composer's gap changes through a transition when the count arrives). "The group" is [data-testid="conversation-interrupted"]; "its message" is ".status-message" inside the group. Perform the steps in order. 1. For each width W of 1280, 390, 360 and 320 in turn: a. resize the viewport to W x 800 (set_viewport width and height only); b. navigate to /c/steady; c. there should be exactly one visible count: eval window.countAt().startsWith('[') with expect "true"; then save where it is across the navigation once transitions finish: eval, awaited, window.settledCountAt().then((at) => (sessionStorage.idle = at).startsWith('[')) with expect "true"; d. navigate to /c/interrupted; e. its message's innerText, trimmed, should be exactly "Conversation Interrupted" (eval with expect, failing if the message is missing); f. the resume button (data-testid "resume-interrupted-button") should be visible; g. the message must not overflow: eval its scrollWidth <= clientWidth with expect "true"; h. eval window.countAt().startsWith('[') with expect "true" (exactly one visible count); i. only when W is below 768 (phones keep the count in the composer row, where it is otherwise): once transitions finish, the count must be where it was in /c/steady, read once: eval, awaited, (window.__phoneAt ??= window.settledCountAt()).then((at) => at === sessionStorage.idle ? 'in place' : at + ' vs ' + sessionStorage.idle) with expect "in place" (the first reading is kept, so a retry of the eval cannot wait for the count to move back). 2. A failed Continue says so once, where errors otherwise show: the test server answers every resume request (POST /api/conversation/<id>/resume) with 503 "Resume failed", so there is nothing to set up. Click the resume button. 3. There should be exactly one ".status-error", with text exactly "Resume failed". 4. ".message-controls-status-slot .status-error" should be visible. 5. The group's count should be 0.`, func(s *shelley) {
		c := s.conversation("echo usage", "interrupted")
		s.initScript("window.__interrupted = " + mustJSON(c.ID) + ";\n" + statusRowInterruptJS)
		// Where the count sits in a conversation that isn't interrupted.
		s.conversation("echo usage", "steady")
		s.route("/api/conversation/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("Resume failed"))
		})
	})
}
