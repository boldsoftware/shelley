package test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Ported from ui/e2e/recording.spec.ts.

// recording prepares a test of the media recording composer: the media
// mocks the spec's beforeEach installed, and recordingHelpersJS.
func recording(s *shelley) {
	s.mediaMocks(true, true)
	s.initScript(recordingHelpersJS)
}

// recordingHelpersJS gives steps window.__pasteFile(name, contents, type),
// which pastes a file into the message input as the spec's pasteAttachment
// did, and keeps each command palette item's data-test-title equal to its
// title, so that a selector can find an item by its exact title as the
// spec's recordingPaletteItem did.
const recordingHelpersJS = `new MutationObserver(() => {
  for (const item of document.querySelectorAll('.command-palette-item')) {
    const title = item.querySelector('.command-palette-item-title')?.textContent.trim() ?? '';
    if (item.dataset.testTitle !== title) item.dataset.testTitle = title;
  }
}).observe(document, { childList: true, subtree: true, characterData: true });
window.__pasteFile = (name, contents = 'attachment', type = 'text/plain') => {
  const transfer = new DataTransfer();
  transfer.items.add(new File([contents], name, { type }));
  document.querySelector('[data-testid="message-input"]').dispatchEvent(
    new ClipboardEvent('paste', { clipboardData: transfer, bubbles: true, cancelable: true }));
  return 'pasted';
};`

// Description snippets for what the spec's helpers did.
const (
	recMocks = `The page fakes the microphone, screen capture and MediaRecorder, and window.__recordingMock counts what the page asked for (read it with eval, e.g. "window.__recordingMock.recorderStarts"): displayRequests (screen captures requested), synchronousDisplayRequests (those requested inside a click or keydown handler), microphoneRequests, audioSources, recorderStarts, recorderStops and stoppedTracks (media tracks stopped). "The recording state" is the data-state attribute of ".recording-status". `

	recShortcuts = `The recording shortcuts are press_key "M" on whatever has focus, with modifiers "Control+Shift" for "the record-audio shortcut" and "Control+Alt+Shift" for "the record-screen shortcut". Press them on whatever has focus: do not click or focus anything for them. `

	recPalette = `To "open the recording palette", every time a step says so: press_key "k" with modifier "Control", wait for the command palette input (".command-palette-input") to be visible, and fill it with "Record audio" (all three, every time). "The palette item X" is the command palette item whose title is exactly X; select it with '.command-palette-item[data-test-title="X"]' (the page keeps that attribute equal to the item's title, so "Record audio" does not match "Record audio and screen"). `

	recDrawer = `To "select conversation X from the drawer": if the button with aria-label "Open conversations" is visible, click it; then wait for ".conversation-item[data-conversation-id=\"X\"]" to be visible, click it, and wait until it has the class "active". `

	recFloating = `The recorder "floats" while the recording panel is inside a [data-testid="recording-floating"] element: count '[data-testid="recording-floating"] [data-testid="recording-panel"]' (1 when floating, 0 when not). `

	recNewFromHeader = `To "start a new conversation from the header": if the button in ".header" whose accessible name is exactly "New Conversation" is visible, click it; otherwise click the button "More options" and then the button "New Conversation" in ".chat-overflow-popover". Click with click steps, not evals. `

	recPaste = `To "paste file F": eval window.__pasteFile(F), passing contents C and type T as further arguments only when the step names them, which pastes such a file into the message input; then wait until exactly one ".message-attachment-ready" exists. `

	recIDs = `The message input is [data-testid="message-input"], the voice button [data-testid="voice-button"], the recording panel [data-testid="recording-panel"], and its stop, cancel and retry buttons [data-testid="recording-stop-button"], [data-testid="recording-cancel-button"] and [data-testid="recording-retry-button"]. When a step says an element should contain or have some text, assert that on that element, not with a page-wide wait_text. Perform the steps in order. `
)

// fetched is the server's whole response to a request a route forwarded.
type fetched struct {
	status int
	header http.Header
	body   []byte
}

// fetch forwards r to the server and reads its response, as Playwright's
// route.fetch did.
func (s *shelley) fetch(r *http.Request) (*fetched, error) {
	resp, err := s.forward(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &fetched{resp.StatusCode, resp.Header, body}, nil
}

// id is the conversation_id in f's JSON body.
func (f *fetched) id() string {
	var v struct {
		ID string `json:"conversation_id"`
	}
	json.Unmarshal(f.body, &v)
	return v.ID
}

// relay answers with f, as route.fulfill({ response }) did.
func (f *fetched) relay(w http.ResponseWriter) {
	for k, v := range f.header {
		if k != "Content-Length" {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(f.status)
	w.Write(f.body)
}

// fulfill answers with status and v as JSON.
func fulfill(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// failText answers with status and exactly text.
func failText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	io.WriteString(w, text)
}

// jsonText is v as JSON, without HTML escaping.
func jsonText(v any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

// ordered is a JSON object that keeps its keys in order, as descriptions
// spell them, where a map would sort them.
type ordered []field

type field struct {
	key   string
	value any
}

func (o ordered) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(jsonText(f.key) + ":" + jsonText(f.value))
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// loggedRequest is a request a route recorded.
type loggedRequest struct {
	path  string
	query string
	body  []byte
}

// requests records requests for calls to report.
type requests struct {
	mu   sync.Mutex
	reqs []loggedRequest
}

// add records r and returns how many requests there are now.
func (l *requests) add(r *http.Request) int {
	b := readBody(r)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reqs = append(l.reqs, loggedRequest{r.URL.Path, r.URL.RawQuery, b})
	return len(l.reqs)
}

func (l *requests) all() []loggedRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]loggedRequest(nil), l.reqs...)
}

// draft creates a draft conversation through the API, as the spec's
// createDraftViaAPI did, and returns its ID.
func (s *shelley) draft(text string) string {
	var created struct {
		ID string `json:"conversation_id"`
	}
	s.api("POST", "/api/conversations/draft", map[string]any{"draft": text, "model": "predictable", "cwd": s.cwd}, &created)
	if created.ID == "" {
		s.fail("draft creation returned no conversation_id")
	}
	return created.ID
}

// capture is a value a route learns, which a call puts in a variable.
type capture struct {
	mu  sync.Mutex
	val string
}

// capture makes a capture that call reports: "false" until a route sets
// it, then "true", after setting the variable name to its value.
func (s *shelley) capture(call, name string) *capture {
	c := &capture{}
	s.call(call, func() string {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.val == "" {
			return "false"
		}
		s.v(name, c.val)
		return "true"
	})
	return c
}

// set sets c, keeping the first value.
func (c *capture) set(v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.val == "" {
		c.val = v
	}
}

// heldDraft holds back the server's answers to the browser's draft
// creations until call "releaseDraft". Call "draftCreated" says whether the
// server has created one and, once it has, sets {{DRAFT}} to the first's ID;
// call "draftAnswered" says whether the browser has been answered, and call
// "draftText" returns the "draft" field of the last one's request.
func (s *shelley) heldDraft() {
	release := s.gate("releaseDraft")
	created := s.capture("draftCreated", "DRAFT")
	answered := s.signal("draftAnswered")
	var mu sync.Mutex
	text := "none"
	s.call("draftText", func() string {
		mu.Lock()
		defer mu.Unlock()
		return text
	})
	s.route("POST /api/conversations/draft", func(w http.ResponseWriter, r *http.Request) {
		draft, _ := object(readBody(r))["draft"].(string)
		mu.Lock()
		text = draft
		mu.Unlock()
		f, err := s.fetch(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		created.set(f.id())
		if release.wait(r) {
			f.relay(w)
			answered.Store(true)
		}
	})
}

// failedDrafts fails the browser's draft creations with "draft
// unavailable", after holding them until call "releaseDraft".
func (s *shelley) failedDrafts() {
	release := s.gate("releaseDraft")
	s.route("POST /api/conversations/draft", func(w http.ResponseWriter, r *http.Request) {
		if release.wait(r) {
			failText(w, http.StatusInternalServerError, "draft unavailable")
		}
	})
}

// fakeUploads answers recording uploads (POST /api/upload/raw) with path,
// and counts them for call "uploads".
func (s *shelley) fakeUploads(path string) {
	n := s.counter("uploads")
	s.route("POST /api/upload/raw", func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		fulfill(w, http.StatusOK, map[string]string{"path": path})
	})
}

// chatPattern is the route of message submissions.
const chatPattern = "POST /api/conversation/{id}/chat"

// acceptChat accepts a message submission without sending it on.
func acceptChat(w http.ResponseWriter) {
	fulfill(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

// acceptChats accepts message submissions without sending them on. Call
// "chats" counts them and call "chatPaths" lists their paths as JSON.
func (s *shelley) acceptChats() *requests {
	var chats requests
	s.call("chats", func() string { return strconv.Itoa(len(chats.all())) })
	s.call("chatPaths", func() string {
		paths := []string{}
		for _, c := range chats.all() {
			paths = append(paths, c.path)
		}
		return jsonText(paths)
	})
	s.route(chatPattern, func(w http.ResponseWriter, r *http.Request) {
		chats.add(r)
		acceptChat(w)
	})
	return &chats
}

func TestE2ERecordingMobileTOCSurvivesRecorder(t *testing.T) {
	e2e(t, `The mobile table of contents survives opening and cancelling the recorder. The test seeded a conversation at /c/{{SLUG}}. "The TOC button" is 'button[aria-label="Conversation table of contents"]', which stays in the page while hidden; check whether it is visible or hidden with wait_visible and wait_hidden on that selector, and click it with a click step (a real click, not an eval). `+recIDs+`
1. Navigate to /c/{{SLUG}}. The TOC button should become visible.
2. Mark it: eval "document.querySelector('button[aria-label=\"Conversation table of contents\"]').dataset.mountIdentity = 'original'".
Then do steps 3 to 10 twice, as two rounds:
3. Click the voice button.
4. [data-testid="recording-inline"] should be visible and the message input hidden.
5. The TOC button should be hidden and still have data-mount-identity "original".
6. Click the cancel button. No recording panel should remain (count 0).
7. The message input should be visible and focused (it is document.activeElement).
8. The TOC button should be visible and still have data-mount-identity "original".
9. Click the TOC button: ".toc-popover" should become visible.
10. Press Escape: ".toc-popover" should become hidden.`, func(s *shelley) {
		recording(s)
		s.viewport(390, 800)
		s.v("SLUG", s.conversation("echo: recording contents", "").Slug)
	})
}

func TestE2ERecordingPaletteActions(t *testing.T) {
	e2e(t, `The command palette offers exact recording actions and starts audio by click and screen by Enter. The test seeded a conversation at /c/{{SLUG}}. `+recMocks+recPalette+recIDs+`
1. Navigate to /c/{{SLUG}} and wait up to 30s for the message input to be visible.
2. Open the recording palette.
3. The palette item "Record audio" should be visible, and its ".command-palette-item-shortcut" should have the text exactly "Ctrl+Shift+M" (assert_text on '.command-palette-item[data-test-title="Record audio"] .command-palette-item-shortcut').
4. The palette item "Record audio and screen" should be visible, and its ".command-palette-item-shortcut" should have the text exactly "Ctrl+Alt+Shift+M".
5. Click the palette item "Record audio".
6. The recording panel should get data-mode "microphone", and window.__recordingMock.recorderStarts should become 1.
7. Click the cancel button. No recording panel should remain (count 0).
8. Open the recording palette again, then fill its input with "audio and screen".
9. The palette item "Record audio and screen" should be visible and have the class "selected".
10. Press Enter (the palette input has focus; do not click anything).
11. The recording panel should get data-mode "screen" and [data-testid="recording-preview"] should be visible.
12. Eval "(m => ({display: m.displayRequests, synchronousDisplay: m.synchronousDisplayRequests, microphone: m.microphoneRequests, starts: m.recorderStarts}))(window.__recordingMock)" should become exactly {"display":1,"synchronousDisplay":1,"microphone":2,"starts":2}.
13. Open the recording palette again. There must be no palette item "Record audio" and no palette item "Record audio and screen" (both counts 0).
14. Press Escape, then click the cancel button.`, func(s *shelley) {
		recording(s)
		s.initScript(`Object.defineProperty(navigator, "platform", { configurable: true, value: "Linux x86_64" });`)
		s.v("SLUG", s.conversation("echo: recording palette actions", "").Slug)
	})
}

func TestE2ERecordingScreenShortcutBeforeDelayedDraft(t *testing.T) {
	e2e(t, `The record-screen shortcut requests the screen in its own turn, before a delayed draft, and blocks duplicates. `+recMocks+recShortcuts+recPalette+`The server's answers to the browser's draft creations (POST /api/conversations/draft) are held back until call "releaseDraft"; call "draftStarted" says ("true" or "false") whether the server has created the draft, and call "draftRequests" counts the draft creations the browser sent. window.__displayRequestsAtDraft is what window.__recordingMock.displayRequests was when the page sent its draft creation. `+recIDs+`
1. Navigate to /new and wait up to 30s for the message input to be visible.
2. Press the record-screen shortcut.
3. Call "draftStarted" until it returns "true".
4. window.__displayRequestsAtDraft must be 1, and window.__recordingMock.displayRequests and window.__recordingMock.synchronousDisplayRequests must both be 1.
5. The recording panel should get data-mode "screen".
6. Eval "(m => ({microphone: m.microphoneRequests, starts: m.recorderStarts}))(window.__recordingMock)" should become exactly {"microphone":1,"starts":1}.
7. Open the recording palette. There must be no palette item "Record audio" and no palette item "Record audio and screen" (both counts 0). Press Escape.
8. Press the record-audio shortcut, then the record-screen shortcut.
9. Call "draftRequests" must return "1", and window.__recordingMock.displayRequests must still be 1.
10. Call "releaseDraft".
11. The recording panel should have data-mode "screen", and eval "(m => ({display: m.displayRequests, microphone: m.microphoneRequests, starts: m.recorderStarts}))(window.__recordingMock)" should become exactly {"display":1,"microphone":1,"starts":1}.
12. Press the record-audio shortcut, then the record-screen shortcut. The same eval must still return exactly {"display":1,"microphone":1,"starts":1}.
13. Click the cancel button.`, func(s *shelley) {
		recording(s)
		// Playwright read displayRequests when the draft request reached
		// its route; the page records it as it sends the request.
		s.initScript(`const fetch0 = window.fetch;
window.fetch = function (input, init) {
  const url = new URL(typeof input === 'string' ? input : input.url, location.href);
  const method = (init?.method ?? input?.method ?? 'GET').toUpperCase();
  if (method === 'POST' && url.pathname === '/api/conversations/draft') {
    window.__displayRequestsAtDraft = window.__recordingMock.displayRequests;
  }
  return fetch0.apply(this, arguments);
};`)
		release := s.gate("releaseDraft")
		started := s.signal("draftStarted")
		requests := s.counter("draftRequests")
		s.route("POST /api/conversations/draft", func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			f, err := s.fetch(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			started.Store(true)
			if release.wait(r) {
				f.relay(w)
			}
		})
	})
}

// earlyStopAttachments is the spec's test of early-stop attachments, with
// the drawer navigation before or after Stop.
func earlyStopAttachments(t *testing.T, beforeStop bool) {
	navigate := `6. Select conversation {{VIEWED}} from the drawer.
7. Click the stop button; the recording state should become "stopping".`
	name := "before"
	if !beforeStop {
		navigate = `6. Click the stop button; the recording state should become "stopping".
7. Select conversation {{VIEWED}} from the drawer.`
		name = "after"
	}
	e2e(t, `Early-stop attachments are kept when navigating `+name+` Stop and the submission fails. `+recMocks+recPaste+recDrawer+`The test seeded a separate draft conversation with ID {{VIEWED}} and the draft text "A separately viewed draft.". The server's answer to the browser's own draft creation is held back until call "releaseDraft"; call "draftCreated" says ("true" or "false") whether the server has created it and, once it has, sets {{DRAFT}} to its conversation ID. Recording uploads succeed, and every message submission (POST /api/conversation/<id>/chat) fails with "acceptance unavailable". `+recIDs+`
1. Navigate to /new. Fill the message input with "Keep this text and attachment after the failed send.".
2. Paste file "keep.txt".
3. Click the voice button.
4. Call "draftCreated" until it returns "true".
5. The recording state should become "recording".
`+navigate+`
8. The message input should have the value "A separately viewed draft.", and no ".message-attachment-ready" should exist (count 0).
9. Call "releaseDraft".
10. [data-testid="recording-retained"] should become visible, and ".status-error" should contain "acceptance unavailable".
11. Select conversation {{DRAFT}} from the drawer.
12. Click the cancel button.
13. The message input should have the value "Keep this text and attachment after the failed send.", and exactly one ".message-attachment-ready" should exist, containing the text "keep.txt".`, func(s *shelley) {
		recording(s)
		s.v("VIEWED", s.draft("A separately viewed draft."))
		s.heldDraft()
		s.fakeUploads("/tmp/shelley-uploads/early-attachment.webm")
		s.route(chatPattern, func(w http.ResponseWriter, r *http.Request) {
			failText(w, http.StatusInternalServerError, "acceptance unavailable")
		})
	})
}

func TestE2ERecordingEarlyStopAttachmentsNavigateBeforeStop(t *testing.T) {
	earlyStopAttachments(t, true)
}

func TestE2ERecordingEarlyStopAttachmentsNavigateAfterStop(t *testing.T) {
	earlyStopAttachments(t, false)
}

// uploadedPNG matches the reference to a pasted PNG's upload in a message.
var uploadedPNG = regexp.MustCompile(`\[/tmp/shelley-uploads/[^\]]+\.png\]`)

func TestE2ERecordingInlineMicrophoneSubmitsTranscription(t *testing.T) {
	e2e(t, `Microphone recording starts inline and submits only the transcription command. `+recMocks+recPaste+`Setting window.__recordingMock.meterPeak changes the fake microphone's level (it starts at 4). The page runs on a fake clock that steps drive with window.__clock: pauseAt(ms), runFor(ms) and resume(). Recording uploads (POST /api/upload/raw) answer with the path /tmp/shelley-uploads/recording.webm; call "upload" describes the last one as "<method> <filename with every digit replaced by #> <its first 4 bytes in hex> <the rest of its bytes as text>". Message submissions (POST /api/conversation/<id>/chat) are recorded and held back until call "releaseAcceptance", then accepted; call "transcriptionRequested" says ("true" or "false") whether one has arrived, and call "chats" lists them as JSON: each one's message (with any "[/tmp/shelley-uploads/<name>.png]" reference rewritten to "[/tmp/shelley-uploads/*.png]"), its model, and whether its body has a transcription_context property. `+recIDs+`
1. Navigate to /new. Fill the message input with "Keep this note with the recording.".
2. Paste file "context.png" with contents "image" and type "image/png".
3. [data-testid="voice-video-icon"] should be visible.
4. Save the message input's height: eval "window.__composerHeight = document.querySelector('[data-testid=\"message-input\"]').getBoundingClientRect().height".
5. Pause the clock a second from now: eval "window.__clock.pauseAt(Date.now() + 1000)".
6. Click the voice button (a click step on its selector, not an eval).
7. The recording panel should be visible. Its height (getBoundingClientRect().height) must be at most 46 and less than window.__composerHeight.
8. No dialog named "Record media" may exist: eval "[...document.querySelectorAll('[role=dialog], dialog')].filter(d => d.getAttribute('aria-label') === 'Record media').length" must be 0.
9. The message input should be hidden, and window.__recordingMock.recorderStarts should become 1.
10. [data-testid="recording-status"] should have the text exactly "Recording…", the recording state should be "preroll", and [data-testid="recording-waveform"] should have the class "recording-waveform-preroll".
11. Run the clock 99ms (eval "window.__clock.runFor(99)"). The recording state must still be "preroll".
12. Run the clock 1ms more (eval "window.__clock.runFor(1)"). The recording state should become "recording".
13. Resume the clock: eval "window.__clock.resume()".
14. [data-testid="recording-status"] should have the text exactly "Recording…", [data-testid="recording-preserved-text"] the text exactly "Keep this note with the recording.", and [data-testid="recording-waveform"] should be visible.
15. Save the waveform bars' heights: eval "window.__bars = [...document.querySelectorAll('.recording-waveform-bar')].map(b => getComputedStyle(b).height)". They must not all be equal: eval "new Set(window.__bars).size > 1" must be true.
16. Raise the level: eval "window.__recordingMock.meterPeak = 48".
17. The tallest bar should grow to more than twice the tallest saved height: eval "Math.max(...[...document.querySelectorAll('.recording-waveform-bar')].map(b => parseFloat(getComputedStyle(b).height))) > 2 * Math.max(...window.__bars.map(parseFloat))" should become true.
18. window.__recordingMock.microphoneRequests should become 1.
19. Click the stop button (a click step on its selector; the clock runs again, so this works). Call "transcriptionRequested" until it returns "true".
20. The recording state should be "stopping", and the cancel button should be disabled.
21. Call "releaseAcceptance". No recording panel should remain (count 0).
22. The message input should be visible, enabled and focused (it is document.activeElement). Fill it with "I can keep typing while the server transcribes.".
23. Call "chats" must return exactly [{"message":"/transcription /tmp/shelley-uploads/recording.webm\nKeep this note with the recording. [/tmp/shelley-uploads/*.png]","model":"predictable","transcription_context":false}].
24. No [data-testid="message-attachments"] and no [data-testid="transcription-task"] may exist (both counts 0), and the message input should have the value "I can keep typing while the server transcribes.".
25. Call "upload" must return exactly "POST rec-########-######.webm 1a45dfa3 firstsecondlast".
26. window.__recordingMock.stoppedTracks must be greater than 0.`, func(s *shelley) {
		recording(s)
		s.clock(time.Time{})
		s.viewport(390, 844)
		var mu sync.Mutex
		upload := "none"
		s.call("upload", func() string {
			mu.Lock()
			defer mu.Unlock()
			return upload
		})
		s.route("/api/upload/raw", func(w http.ResponseWriter, r *http.Request) {
			b := readBody(r)
			name := regexp.MustCompile(`[0-9]`).ReplaceAllString(r.URL.Query().Get("filename"), "#")
			head, rest := b[:min(4, len(b))], b[min(4, len(b)):]
			mu.Lock()
			upload = r.Method + " " + name + " " + hex.EncodeToString(head) + " " + string(rest)
			mu.Unlock()
			fulfill(w, http.StatusOK, map[string]string{"path": "/tmp/shelley-uploads/recording.webm"})
		})
		requested := s.signal("transcriptionRequested")
		release := s.gate("releaseAcceptance")
		var chats requests
		s.call("chats", func() string {
			var out []map[string]any
			for _, c := range chats.all() {
				body := object(c.body)
				message, _ := body["message"].(string)
				_, hasContext := body["transcription_context"]
				out = append(out, map[string]any{
					"message":               uploadedPNG.ReplaceAllString(message, "[/tmp/shelley-uploads/*.png]"),
					"model":                 body["model"],
					"transcription_context": hasContext,
				})
			}
			return jsonText(out)
		})
		s.route(chatPattern, func(w http.ResponseWriter, r *http.Request) {
			chats.add(r)
			requested.Store(true)
			if release.wait(r) {
				acceptChat(w)
			}
		})
	})
}

// queuedMessage is a queued message as the server stores it.
func queuedMessage(id, text string, extra map[string]any) map[string]any {
	m := map[string]any{
		"id":         id,
		"llm":        map[string]any{"Role": 0, "Content": []any{map[string]any{"ID": "", "Type": 2, "Text": text}}},
		"created_at": "2026-09-12T00:00:00Z",
		"model":      "predictable",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// queuedConversation seeds a conversation whose snapshot and loads report
// queued as its queued messages, with the live stream cut off, as the spec's
// openQueuedConversation did. legacyCompacting adds the distill_status row
// the old generation-based compaction left behind.
func (s *shelley) queuedConversation(queued []map[string]any, legacyCompacting bool) convo {
	c := s.conversation("echo: recording queue seed", "")
	queuedJSON := mustJSON(queued)
	s.route("/api/stream2", abort)
	s.route("GET /api/conversations/snapshot", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := s.fetchJSON(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		conversations, _ := body["conversations"].([]any)
		for _, conversation := range conversations {
			if m, ok := conversation.(map[string]any); ok && m["conversation_id"] == c.ID {
				m["queued_messages"] = queuedJSON
			}
		}
		writeJSON(w, body)
	})
	s.route("GET /api/conversation/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := s.fetchJSON(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if conversation, ok := body["conversation"].(map[string]any); ok {
			conversation["queued_messages"] = queuedJSON
		}
		if legacyCompacting {
			messages, _ := body["messages"].([]any)
			body["messages"] = append(messages, map[string]any{
				"message_id":      "distill-in-progress",
				"conversation_id": c.ID,
				"sequence_id":     999,
				"type":            "agent",
				"user_data":       mustJSON(map[string]any{"distill_status": "in_progress", "distill_method": "compact"}),
				"created_at":      "2026-09-12T00:00:00Z",
				"generation":      1,
			})
		}
		writeJSON(w, body)
	})
	return c
}

// firstRequest reports the first request the browser sent whose path ends
// in suffix, as "<method> <suffix>?<query>", or "none".
func (s *shelley) firstRequest(call, suffix string) {
	s.call(call, func() string {
		for _, r := range s.requestLog() {
			method, uri, _ := strings.Cut(r, " ")
			path, query, _ := strings.Cut(uri, "?")
			if strings.HasSuffix(path, suffix) {
				return method + " " + suffix + "?" + query
			}
		}
		return "none"
	})
}

func TestE2ERecordingQueueStatesInOrderAfterReload(t *testing.T) {
	e2e(t, `Durable transcription queue states render in exact order, also after a reload. The test seeded a conversation at /c/{{SLUG}} whose server data reports four queued messages, in this order: a plain message "echo: first queued message"; a transcription still working on /tmp/shelley-uploads/working.webm with the context "Keep this image [/tmp/shelley-uploads/context.png]"; a ready transcription of /tmp/shelley-uploads/ready.webm with the text "Finished spoken words."; and a failed transcription of /tmp/shelley-uploads/failed.webm with the error "transcription unavailable". The live event stream is cut off. Requests to send a queued message now (POST /api/conversation/<id>/send-queued?queued_id=<id>) are accepted without effect; call "sendQueued" reports the first one as "<method> /send-queued?<query>" ("none" before there is one). "The items" are the elements matching '[data-testid="queued-ghost"], [data-testid="transcription-task"]', in document order. Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait up to 30s for the message input ([data-testid="message-input"]) to be visible.
2. There should be exactly 4 items. Item 1 should contain "first queued message"; item 2 "Transcribing…", "working.webm" and "Keep this image"; item 3 "Finished spoken words." and "Queued"; item 4 "Recording failed" and "transcription unavailable".
3. There should be exactly 2 [data-testid="queued-ghost"], exactly 2 [data-testid="transcription-task"] and exactly 1 [data-testid="send-queued-now"].
4. Click [data-testid="send-queued-now"]. Call "sendQueued" should return "POST /send-queued?queued_id=q-first".
5. Reload: navigate to /c/{{SLUG}} again. The URL should have exactly the path "/c/{{SLUG}}" (an exact "value", not "text").
6. There should again be exactly 4 items: item 1 containing "first queued message", item 2 "working.webm", item 3 "Finished spoken words." and item 4 "failed.webm".`, func(s *shelley) {
		recording(s)
		c := s.queuedConversation([]map[string]any{
			queuedMessage("q-first", "echo: first queued message", nil),
			queuedMessage("q-working", "", map[string]any{
				"kind":  "transcription",
				"state": "working",
				"transcription": map[string]any{
					"media_path": "/tmp/shelley-uploads/working.webm",
					"context":    "Keep this image [/tmp/shelley-uploads/context.png]",
				},
			}),
			queuedMessage("q-ready", "Finished spoken words.", map[string]any{
				"kind":          "transcription",
				"state":         "ready",
				"transcription": map[string]any{"media_path": "/tmp/shelley-uploads/ready.webm"},
			}),
			queuedMessage("q-failed", "", map[string]any{
				"kind":          "transcription",
				"state":         "failed",
				"transcription": map[string]any{"media_path": "/tmp/shelley-uploads/failed.webm"},
				"error":         "transcription unavailable",
			}),
		}, false)
		s.v("SLUG", c.Slug)
		s.route("POST /api/conversation/{id}/send-queued", func(w http.ResponseWriter, r *http.Request) {
			fulfill(w, http.StatusAccepted, map[string]any{})
		})
		s.firstRequest("sendQueued", "/send-queued")
	})
}

func TestE2ERecordingMacShortcutsAndHints(t *testing.T) {
	e2e(t, `On Macs, recording uses Command shortcuts and the palette shows matching hints. navigator.platform is "MacIntel", so the record-audio shortcut is press_key "M" with modifiers "Meta+Shift" and the record-screen shortcut press_key "M" with modifiers "Meta+Alt+Shift". `+recMocks+`"The palette item X" is the command palette item whose title is exactly X; select it with '.command-palette-item[data-test-title="X"]' (the page keeps that attribute equal to the item's title). `+recIDs+`
1. Navigate to /new. The voice button should be enabled.
2. Press "k" with modifier "Meta". The command palette input (".command-palette-input") should become visible; fill it with "Record audio".
3. The "kbd" of the palette item "Record audio" should have the text exactly "⌘⇧M", and the "kbd" of the palette item "Record audio and screen" the text exactly "⌘⌥⇧M".
4. Press Escape in the palette input.
5. Press the record-audio shortcut. The recording panel should get data-mode "microphone" and the recording state should become "recording". Click the cancel button; the voice button should be enabled.
6. Press the record-screen shortcut. The recording panel should get data-mode "screen" and the recording state should become "recording". Click the cancel button; the voice button should be enabled.
7. window.__recordingMock.recorderStarts must be 2 and window.__recordingMock.synchronousDisplayRequests must be 1.`, func(s *shelley) {
		recording(s)
		s.initScript(`Object.defineProperty(navigator, "platform", { configurable: true, value: "MacIntel" });`)
	})
}

func TestE2ERecordingIgnoresRepeatedComposingConsumedShortcuts(t *testing.T) {
	e2e(t, `Recording ignores repeated, composing, and consumed recording shortcuts. `+recMocks+recShortcuts+recIDs+`
1. Navigate to /new. The voice button should be enabled.
2. Dispatch three record-audio keydowns on the document that recording must ignore, in one eval: "(() => { const o = {key: 'M', code: 'KeyM', ctrlKey: true, shiftKey: true, bubbles: true, cancelable: true}; document.dispatchEvent(new KeyboardEvent('keydown', {...o, repeat: true})); document.dispatchEvent(new KeyboardEvent('keydown', {...o, isComposing: true})); const consumed = new KeyboardEvent('keydown', o); consumed.preventDefault(); document.dispatchEvent(consumed); return 'dispatched'; })()".
3. The voice button should be enabled, and window.__recordingMock.microphoneRequests must be 0.
4. Press the record-audio shortcut. The recording panel should get data-mode "microphone".
5. Click the cancel button.`, recording)
}

func TestE2ERecordingNotBehindAnotherDialog(t *testing.T) {
	e2e(t, `Recording does not start behind another dialog. `+recMocks+recShortcuts+recPalette+`"The dialog" is the one visible element with role "dialog"; there must be exactly one. `+recIDs+`
1. Navigate to /new. The voice button should be enabled.
2. Open the recording palette, then fill its input with "Notification Settings" and click the element whose text is exactly "Notification Settings".
3. The dialog should be visible.
4. Press the record-audio shortcut, then the record-screen shortcut.
Cmd-K can also open above an existing modal: neither its shortcuts nor its action rows may start an inaccessible recorder behind that modal.
5. Open the recording palette. Press the record-audio shortcut. The palette input should still be visible. Fill it with "Record audio" and press Enter in it. The dialog should be visible, and no recording panel may exist (count 0).
6. Open the recording palette. Press the record-screen shortcut. The palette input should still be visible. Fill it with "Record audio and screen" and press Enter in it. The dialog should be visible, and no recording panel may exist (count 0).
7. window.__recordingMock.microphoneRequests and window.__recordingMock.displayRequests must both be 0.`, recording)
}

func TestE2ERecordingCancelsScreenWhileDraftPending(t *testing.T) {
	e2e(t, `Cancelling selected screen capture while draft creation is pending. `+recMocks+recShortcuts+`The server's answer to the browser's draft creation is held back until call "releaseDraft"; call "draftCreated" says ("true" or "false") whether the server has created the draft, and call "draftAnswered" whether the browser has received the answer. `+recIDs+`
1. Navigate to /new. The voice button should be enabled.
2. Press the record-screen shortcut.
3. Call "draftCreated" until it returns "true".
4. window.__recordingMock.recorderStarts should become 1.
5. Click the cancel button. The voice button should be enabled, and window.__recordingMock.stoppedTracks should become 4.
6. Call "releaseDraft", then call "draftAnswered" until it returns "true".
7. The URL should contain "/c/", and no recording panel may exist (count 0).
8. Press the record-audio shortcut. The recording panel should get data-mode "microphone", and window.__recordingMock.recorderStarts should become 2.
9. window.__recordingMock.displayRequests must be 1.
10. Click the cancel button.`, func(s *shelley) {
		recording(s)
		s.heldDraft()
	})
}

func TestE2ERecordingMicrophoneBeforeDelayedDraftCancelStopsTracks(t *testing.T) {
	e2e(t, `The microphone starts before a delayed draft, and cancelling stops its tracks. `+recMocks+recShortcuts+`The server's answer to the browser's draft creation is held back until call "releaseDraft"; call "draftCreated" says ("true" or "false") whether the server has created the draft. Call "uploads" counts recording uploads (POST /api/upload/raw). `+recIDs+`
1. Navigate to /new. Fill the message input with "Pending draft text.".
2. Press the record-audio shortcut.
3. Exactly one [data-testid="recording-inline"] should exist, and the recording state should become "recording".
4. Call "draftCreated" until it returns "true".
5. window.__recordingMock.recorderStarts must be 1, and the URL must still have exactly the path "/new" (an exact "value", not "text").
6. Click the cancel button. No recording panel should remain (count 0), and the message input should have the value "Pending draft text.".
7. window.__recordingMock.stoppedTracks should become 1, and call "uploads" must return "0".
8. Call "releaseDraft". The URL should come to contain "/c/".
9. No recording panel may exist (count 0), and the message input should still have the value "Pending draft text.".`, func(s *shelley) {
		recording(s)
		s.fakeUploads("/tmp/shelley-uploads/unexpected.webm")
		s.heldDraft()
	})
}

func TestE2ERecordingReturnBeforeDelayedDraftNoNavigateAfterCancel(t *testing.T) {
	e2e(t, `A Return clicked before the delayed draft resolves does not navigate after cancel. The test seeded a conversation {{VIEWED}} at /c/{{VIEWED_SLUG}}. `+recMocks+recDrawer+recFloating+`The server's answer to the browser's draft creation is held back until call "releaseDraft"; call "draftCreated" says ("true" or "false") whether the server has created the draft, and call "draftAnswered" whether the browser has received the answer. `+recIDs+`
1. Navigate to /new and click the voice button.
2. Call "draftCreated" until it returns "true".
3. Select conversation {{VIEWED}} from the drawer. The recorder should float.
4. Click [data-testid="recording-return-button"], then click the cancel button. No recording panel should remain (count 0).
5. Call "releaseDraft", then call "draftAnswered" until it returns "true".
6. The URL should have exactly the path "/c/{{VIEWED_SLUG}}" (an exact "value", not "text").
7. The voice button should be enabled, and the URL must still have exactly the path "/c/{{VIEWED_SLUG}}" (an exact "value", not "text").`, func(s *shelley) {
		recording(s)
		viewed := s.conversation("echo: stay here", "")
		s.v("VIEWED", viewed.ID)
		s.v("VIEWED_SLUG", viewed.Slug)
		s.heldDraft()
	})
}

func TestE2ERecordingReturnLookupAfterCancelNoNavigate(t *testing.T) {
	e2e(t, `A Return whose lookup finishes after cancel does not navigate. The test seeded a source conversation at /c/{{SOURCE_SLUG}} and a conversation {{VIEWED}} at /c/{{VIEWED_SLUG}}. `+recMocks+recDrawer+recFloating+`The recorder's lookup of its source conversation (GET /api/conversation-by-slug/<source ID>) is held back until call "releaseLookup"; call "lookupStarted" says ("true" or "false") whether it has arrived. Eval window.__watchReturnRead() makes the page set the body's data-recording-return-read attribute to "true" once it has read the JSON of a later conversation-by-slug response. `+recIDs+`
1. Navigate to /c/{{SOURCE_SLUG}}. The voice button should be enabled; click it.
2. The recording state should become "recording".
3. Select conversation {{VIEWED}} from the drawer. The recorder should float.
4. Eval window.__watchReturnRead().
5. Click [data-testid="recording-return-button"]. Call "lookupStarted" until it returns "true".
6. Click the cancel button. No recording panel should remain (count 0).
7. Call "releaseLookup".
Wait for JSON consumption, not merely the response headers. The lookup's awaiting continuation runs before the next browser assertion.
8. The body should get data-recording-return-read "true".
9. The voice button should be enabled, and the URL should have exactly the path "/c/{{VIEWED_SLUG}}" (an exact "value", not "text").`, func(s *shelley) {
		recording(s)
		s.initScript(`window.__watchReturnRead = () => {
  const fetch0 = window.fetch;
  window.fetch = async (...args) => {
    const response = await fetch0(...args);
    if (response.url.includes('/api/conversation-by-slug/')) {
      const json = response.json.bind(response);
      response.json = async () => {
        const body = await json();
        document.body.dataset.recordingReturnRead = 'true';
        return body;
      };
    }
    return response;
  };
  return 'watching';
};`)
		source := s.conversation("echo: return source", "")
		viewed := s.conversation("echo: return viewed", "")
		s.v("SOURCE_SLUG", source.Slug)
		s.v("VIEWED", viewed.ID)
		s.v("VIEWED_SLUG", viewed.Slug)
		release := s.gate("releaseLookup")
		started := s.signal("lookupStarted")
		s.route("/api/conversation-by-slug/"+source.ID, func(w http.ResponseWriter, r *http.Request) {
			started.Store(true)
			if release.wait(r) {
				s.pass.ServeHTTP(w, r)
			}
		})
	})
}

func TestE2ERecordingFinishedBeforeDelayedDraftSubmitsToIt(t *testing.T) {
	e2e(t, `A recording that finished before its delayed draft is submitted to that draft. `+recMocks+`The server's answer to the browser's draft creation is held back until call "releaseDraft"; call "draftCreated" says ("true" or "false") whether the server has created it and, once it has, sets {{DRAFT}} to its conversation ID. Recording uploads (POST /api/upload/raw) succeed with the path /tmp/shelley-uploads/early-stop.webm, and call "uploads" counts them. Message submissions (POST /api/conversation/<id>/chat) are accepted without being sent on; call "chatPaths" lists their URL paths as JSON. `+recIDs+`
1. Navigate to /new. Fill the message input with "Context for the early stop.". Click the voice button.
2. The recording state should become "recording".
3. Call "draftCreated" until it returns "true".
4. Click the stop button. The recording state should become "stopping", and [data-testid="recording-preserved-text"] should have the text exactly "Context for the early stop.".
5. Call "uploads" should return "1", window.__recordingMock.stoppedTracks should become 1, and call "chatPaths" must return exactly [].
6. Call "releaseDraft".
7. Call "chatPaths" should return exactly ["/api/conversation/{{DRAFT}}/chat"].
8. The URL should have exactly the path "/c/{{DRAFT}}" (an exact "value", not "text"), and the message input should have the value "" (empty).`, func(s *shelley) {
		recording(s)
		s.heldDraft()
		s.fakeUploads("/tmp/shelley-uploads/early-stop.webm")
		s.acceptChats()
	})
}

func TestE2ERecordingRetainsUploadWhenDelayedDraftFailsAfterStop(t *testing.T) {
	e2e(t, `The uploaded recording is retained when its delayed draft fails after stopping. `+recMocks+`The browser's draft creations are held back until call "releaseDraft", then fail with "draft unavailable". Recording uploads (POST /api/upload/raw) succeed with the path /tmp/shelley-uploads/orphaned.webm. Message submissions are accepted without being sent on, and call "chats" counts them. `+recIDs+`
1. Navigate to /new. Fill the message input with "Context that stays after the failed draft.". Click the voice button.
2. The recording state should become "recording".
3. Click the stop button. The recording state should become "stopping".
4. Call "releaseDraft".
5. [data-testid="recording-retained"] should become visible, and ".status-error" should contain "Retry with /transcription /tmp/shelley-uploads/orphaned.webm".
6. The cancel button's accessible name (its aria-label) should be exactly "Discard recording". Click it.
7. The message input should have the value "Context that stays after the failed draft.", and call "chats" must return "0".`, func(s *shelley) {
		recording(s)
		s.failedDrafts()
		s.fakeUploads("/tmp/shelley-uploads/orphaned.webm")
		s.acceptChats()
	})
}

// retriesScreenMetadata is the spec's test of retrying a screen recording
// whose metadata upload failed, with or without a failed draft too.
func retriesScreenMetadata(t *testing.T, draftFails bool) {
	title, drafts, draftSteps, draftCheck := "Retained screen metadata is retried without reuploading the video", "", "", ""
	if draftFails {
		title = "Retained screen metadata is retried after draft failure"
		drafts = `The browser's first draft creation is held back until call "releaseDraft", then fails with "draft unavailable"; later ones reach the server. `
		draftSteps = `
4a. Call "releaseDraft". The recording state should become "stopping", and ".status-error" should contain "draft unavailable".`
		draftCheck = `,"drafts":2`
	}
	e2e(t, title+`. `+recMocks+recShortcuts+recPaste+drafts+`Recording uploads (POST /api/upload/raw) are either the video or, with a filename ending in ".json", its metadata. Videos succeed with the path /tmp/shelley-uploads/saved-screen.webm. The first metadata upload is held back until call "releaseMetadata", then fails with "metadata unavailable"; call "metadataStarted" says ("true" or "false") whether it has arrived. Later metadata uploads succeed with the path /tmp/shelley-uploads/saved-screen.webm.json. Message submissions are accepted without being sent on, and call "chats" counts them. Call "requests" reports, as JSON, how many videos and metadata were uploaded, whether the second metadata upload's body is the same as the first's ("sameMetadata"), and how many submissions`+map[bool]string{true: ` and draft creations`, false: ``}[draftFails]+` there were. `+recIDs+`
1. Navigate to /new. Fill the message input with "Keep this screen recording context.". Paste file "screen-context.txt".
2. Press the record-screen shortcut. The recording state should become "recording".
3. Click the stop button.
4. Call "metadataStarted" until it returns "true".`+draftSteps+`
5. Call "releaseMetadata".
6. ".status-error" should contain "Retry with /transcription /tmp/shelley-uploads/saved-screen.webm" and also "metadata unavailable", and [data-testid="recording-retained"] should be visible.
7. window.__recordingMock.stoppedTracks should become 4, and call "chats" must return "0".
8. Click the retry button. No recording panel should remain (count 0).
9. Call "requests" must return exactly {"videos":1,"metadata":2,"sameMetadata":true,"chats":1`+draftCheck+`}.
10. window.__recordingMock.recorderStarts must be 1, the message input should have the value "" (empty), and no ".message-attachment-ready" may exist (count 0).`, func(s *shelley) {
		recording(s)
		var draftRequests atomic.Int64
		if draftFails {
			release := s.gate("releaseDraft")
			s.route("POST /api/conversations/draft", func(w http.ResponseWriter, r *http.Request) {
				if draftRequests.Add(1) > 1 {
					s.pass.ServeHTTP(w, r)
					return
				}
				if release.wait(r) {
					failText(w, http.StatusInternalServerError, "draft unavailable")
				}
			})
		}
		releaseMetadata := s.gate("releaseMetadata")
		started := s.signal("metadataStarted")
		var videos atomic.Int64
		var metadata requests
		s.route("POST /api/upload/raw", func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Query().Get("filename"), ".json") {
				videos.Add(1)
				fulfill(w, http.StatusOK, map[string]string{"path": "/tmp/shelley-uploads/saved-screen.webm"})
				return
			}
			if metadata.add(r) > 1 {
				fulfill(w, http.StatusOK, map[string]string{"path": "/tmp/shelley-uploads/saved-screen.webm.json"})
				return
			}
			started.Store(true)
			if releaseMetadata.wait(r) {
				failText(w, http.StatusInternalServerError, "metadata unavailable")
			}
		})
		chats := s.acceptChats()
		s.call("requests", func() string {
			m := metadata.all()
			out := ordered{
				{"videos", videos.Load()},
				{"metadata", len(m)},
				{"sameMetadata", len(m) > 1 && bytes.Equal(m[1].body, m[0].body)},
				{"chats", len(chats.all())},
			}
			if draftFails {
				out = append(out, field{"drafts", draftRequests.Load()})
			}
			return jsonText(out)
		})
	})
}

func TestE2ERecordingRetriesScreenMetadataWithoutReupload(t *testing.T) {
	retriesScreenMetadata(t, false)
}

func TestE2ERecordingRetriesScreenMetadataAfterDraftFailure(t *testing.T) {
	retriesScreenMetadata(t, true)
}

func TestE2ERecordingRetainsUploadFinishingAfterDraftFailure(t *testing.T) {
	e2e(t, `An upload that finishes after draft failure is retained. `+recMocks+`The browser's draft creations are held back until call "releaseDraft", then fail with "draft unavailable". Recording uploads (POST /api/upload/raw) are held back until call "releaseUpload", then succeed with the path /tmp/shelley-uploads/late-upload.webm; call "uploadStarted" says ("true" or "false") whether one has arrived. `+recIDs+`
1. Navigate to /new and click the voice button. The recording state should become "recording".
2. Click the stop button. Call "uploadStarted" until it returns "true".
3. Call "releaseDraft". The recording state should be "stopping", and ".status-error" should contain "draft unavailable".
4. Call "releaseUpload". ".status-error" should come to contain "Retry with /transcription /tmp/shelley-uploads/late-upload.webm", and [data-testid="recording-retained"] should be visible.
5. Click the cancel button. The voice button should be enabled.`, func(s *shelley) {
		recording(s)
		s.failedDrafts()
		release := s.gate("releaseUpload")
		started := s.signal("uploadStarted")
		s.route("POST /api/upload/raw", func(w http.ResponseWriter, r *http.Request) {
			started.Store(true)
			if release.wait(r) {
				fulfill(w, http.StatusOK, map[string]string{"path": "/tmp/shelley-uploads/late-upload.webm"})
			}
		})
	})
}

func TestE2ERecordingKeepsMicrophoneAfterDestinationFailure(t *testing.T) {
	e2e(t, `The microphone recording is kept after destination failure until the user cancels. `+recMocks+recPaste+`The browser's draft creations are held back until call "releaseDraft", then fail with "draft unavailable". `+recIDs+`
1. Navigate to /new. Fill the message input with "Keep this after the failed draft.". Paste file "keep.txt".
2. Click the voice button. The recording state should become "recording".
3. Call "releaseDraft".
4. [data-testid="recording-error"] should come to contain "draft unavailable", and the recording state should be "recording".
5. window.__recordingMock.stoppedTracks must be 0.
6. Click the cancel button.
7. The message input should have the value "Keep this after the failed draft." and be enabled, and exactly one ".message-attachment-ready" should exist.
8. window.__recordingMock.stoppedTracks should become 1, and window.__recordingMock.recorderStarts must be 1.
9. The voice button should be enabled.`, func(s *shelley) {
		recording(s)
		s.failedDrafts()
	})
}

func TestE2ERecordingRetriesFailedDestinationAfterNavigation(t *testing.T) {
	e2e(t, `A failed recording destination is retried after navigation without recapturing or changing its context. The test seeded a separate draft conversation {{VIEWED}} with the draft text "A separate draft.". `+recMocks+recPaste+recDrawer+recNewFromHeader+`Draft creations (POST /api/conversations/draft) whose "draft" is "A newer new-conversation composer." reach the server, but the server's answer is held back until call "releaseNewerDraft"; call "newerDraftStarted" says ("true" or "false") whether the server has created that draft. Of the other draft creations, the first is held back until call "releaseFirstDraft" and then fails with "draft unavailable", the second fails with "retry still unavailable", and later ones reach the server. Call "drafts" reports those other draft creations as "<count> <same|different>": "same" when every request body equals the first's. Call "source" says ("true" or "false") whether one of them reached the server, and once it has, sets {{SOURCE}} to the conversation it created. Attachment uploads (POST /api/upload) answer with the path /tmp/shelley-uploads/original-context.txt. Recording uploads (POST /api/upload/raw) answer with the path /tmp/shelley-uploads/retained.webm, and call "uploads" counts them. Message submissions are accepted without being sent on; call "submissions" lists them as JSON, each with its URL path, message and model. `+recIDs+`
1. Navigate to /new. Fill the message input with "Original recording context.". Paste file "original-context.txt".
2. Click the voice button. The recording state should become "recording".
3. Call "releaseFirstDraft". [data-testid="recording-error"] should come to contain "draft unavailable", and the recording state should still be "recording".
4. window.__recordingMock.stoppedTracks must be 0.
5. Click the stop button. [data-testid="recording-retained"] should become visible.
6. Call "drafts" must return "1 same", call "uploads" must return "1", and call "submissions" must return exactly [].
7. window.__recordingMock.stoppedTracks should become 1.
8. Select conversation {{VIEWED}} from the drawer. Fill the message input with "Leave this different composer alone.".
9. Click the retry button twice in a single eval: "(b => { b.click(); b.click(); return 'clicked'; })(document.querySelector('[data-testid=\"recording-retry-button\"]'))".
10. [data-testid="recording-error"] should come to contain "retry still unavailable", and [data-testid="recording-retained"] should be visible.
11. Call "drafts" must return "2 same", call "submissions" must return exactly [], and the message input should have the value "Leave this different composer alone.".
12. Start a new conversation from the header. Fill the message input with "A newer new-conversation composer.".
13. Call "newerDraftStarted" until it returns "true".
14. Click the retry button. No recording panel should remain (count 0).
15. Call "drafts" must return "3 same". Call "source" must return "true".
16. Call "submissions" must return exactly [{"path":"/api/conversation/{{SOURCE}}/chat","message":"/transcription /tmp/shelley-uploads/retained.webm\nOriginal recording context. [/tmp/shelley-uploads/original-context.txt]","model":"predictable"}].
17. Call "uploads" must return "1", window.__recordingMock.recorderStarts must be 1, and window.__recordingMock.microphoneRequests must be 1.
18. The URL should have exactly the path "/new" (an exact "value", not "text"), and the message input should have the value "A newer new-conversation composer.".`, func(s *shelley) {
		recording(s)
		s.v("VIEWED", s.draft("A separate draft."))
		releaseFirst := s.gate("releaseFirstDraft")
		releaseNewer := s.gate("releaseNewerDraft")
		newerStarted := s.signal("newerDraftStarted")
		source := s.capture("source", "SOURCE")
		var drafts requests
		s.call("drafts", func() string {
			all := drafts.all()
			same := "same"
			for _, d := range all {
				if jsonText(object(d.body)) != jsonText(object(all[0].body)) {
					same = "different"
				}
			}
			return strconv.Itoa(len(all)) + " " + same
		})
		s.route("POST /api/conversations/draft", func(w http.ResponseWriter, r *http.Request) {
			if object(readBody(r))["draft"] == "A newer new-conversation composer." {
				f, err := s.fetch(r)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
				newerStarted.Store(true)
				if releaseNewer.wait(r) {
					f.relay(w)
				}
				return
			}
			switch drafts.add(r) {
			case 1:
				if releaseFirst.wait(r) {
					failText(w, http.StatusInternalServerError, "draft unavailable")
				}
			case 2:
				failText(w, http.StatusInternalServerError, "retry still unavailable")
			default:
				f, err := s.fetch(r)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
				source.set(f.id())
				f.relay(w)
			}
		})
		s.route("POST /api/upload", func(w http.ResponseWriter, r *http.Request) {
			fulfill(w, http.StatusOK, map[string]string{"path": "/tmp/shelley-uploads/original-context.txt"})
		})
		s.fakeUploads("/tmp/shelley-uploads/retained.webm")
		s.call("submissions", submissions(s.acceptChats(), "path", "message", "model"))
	})
}

func TestE2ERecordingRetriesFailedRawUploadWithRetainedBytes(t *testing.T) {
	e2e(t, `A failed raw upload is retried using the retained audio bytes. `+recMocks+`The first recording upload (POST /api/upload/raw) fails with "upload unavailable"; later ones succeed with the path /tmp/shelley-uploads/retried-upload.webm. Call "uploads" reports them as "<count> <same|different>": "same" when every upload's bytes equal the first's. Message submissions are accepted without being sent on, and call "chats" counts them. `+recIDs+`
1. Navigate to /new and click the voice button. The recording state should become "recording".
2. Click the stop button. [data-testid="recording-retained"] should become visible, and [data-testid="recording-error"] should contain "upload unavailable".
3. Call "chats" must return "0", and window.__recordingMock.stoppedTracks should become 1.
4. Click the retry button. No recording panel should remain (count 0).
5. Call "uploads" must return "2 same", call "chats" must return "1", and window.__recordingMock.recorderStarts must be 1.`, func(s *shelley) {
		recording(s)
		var uploads requests
		s.call("uploads", func() string {
			all := uploads.all()
			same := "same"
			for _, u := range all {
				if !bytes.Equal(u.body, all[0].body) {
					same = "different"
				}
			}
			return strconv.Itoa(len(all)) + " " + same
		})
		s.route("POST /api/upload/raw", func(w http.ResponseWriter, r *http.Request) {
			if uploads.add(r) == 1 {
				failText(w, http.StatusServiceUnavailable, "upload unavailable")
				return
			}
			fulfill(w, http.StatusOK, map[string]string{"path": "/tmp/shelley-uploads/retried-upload.webm"})
		})
		s.acceptChats()
	})
}

func TestE2ERecordingDiscardsRetainedWhileDestinationPending(t *testing.T) {
	e2e(t, `Discarding retained audio while its destination is pending submits nothing later. `+recMocks+`The server's answer to the browser's draft creation is held back until call "releaseDraft"; call "draftCreated" says ("true" or "false") whether the server has created it and, once it has, sets {{DRAFT}} to its conversation ID. Recording uploads succeed with the path /tmp/shelley-uploads/discard-pending.webm, and call "uploads" counts them. Message submissions are accepted without being sent on, and call "chats" counts them. `+recIDs+`
1. Navigate to /new. Fill the message input with "Keep the composer after discarding audio.". Click the voice button.
2. Call "draftCreated" until it returns "true".
3. The recording state should become "recording". Click the stop button.
4. Call "uploads" should return "1".
5. The cancel button's accessible name (its aria-label) should be exactly "Discard recording", and it should be enabled. Click it.
6. No recording panel should remain (count 0), and the message input should have the value "Keep the composer after discarding audio.".
7. Call "releaseDraft". The URL should come to have exactly the path "/c/{{DRAFT}}" (an exact "value", not "text").
8. The message input should still have the value "Keep the composer after discarding audio.".
9. Call "chats" must return "0", and window.__recordingMock.stoppedTracks must be 1.`, func(s *shelley) {
		recording(s)
		s.heldDraft()
		s.fakeUploads("/tmp/shelley-uploads/discard-pending.webm")
		s.acceptChats()
	})
}

func TestE2ERecordingDiscardFailedFloatingReleasesPreviews(t *testing.T) {
	e2e(t, `Discarding a failed floating recording releases orphaned previews without touching the viewed composer. The test seeded a separate draft conversation {{VIEWED}} with the draft text "Keep the viewed draft.". `+recMocks+`window.__recordingMock.revokedPreviews lists the object URLs the page revoked. `+recPaste+recDrawer+`Draft creations fail with "draft unavailable". Attachment uploads (POST /api/upload) answer with the path /tmp/shelley-uploads/attachment.png, and recording uploads with /tmp/shelley-uploads/discarded.webm. `+recIDs+`
1. Navigate to /new. Paste file "source.png" with contents "image" and type "image/png".
2. Save the preview: eval "window.__preview = document.querySelector('.message-attachment-thumb').getAttribute('src')".
3. Click the voice button. [data-testid="recording-error"] should come to contain "draft unavailable", and the recording state should be "recording".
4. Select conversation {{VIEWED}} from the drawer. Paste file "viewed.txt".
5. Click the stop button. [data-testid="recording-retained"] should become visible.
6. Eval "JSON.stringify(window.__recordingMock.revokedPreviews)" must return "[]".
7. Click the cancel button. No recording panel should remain (count 0).
8. Eval "JSON.stringify(window.__recordingMock.revokedPreviews) === JSON.stringify([window.__preview])" must be true.
9. The message input should have the value "Keep the viewed draft.", and ".message-attachment-ready" should contain "viewed.txt".`, func(s *shelley) {
		recording(s)
		s.v("VIEWED", s.draft("Keep the viewed draft."))
		s.route("POST /api/conversations/draft", func(w http.ResponseWriter, r *http.Request) {
			failText(w, http.StatusInternalServerError, "draft unavailable")
		})
		s.route("POST /api/upload", func(w http.ResponseWriter, r *http.Request) {
			fulfill(w, http.StatusOK, map[string]string{"path": "/tmp/shelley-uploads/attachment.png"})
		})
		s.fakeUploads("/tmp/shelley-uploads/discarded.webm")
	})
}

// escapeConsumed is the spec's test that a menu consuming Escape leaves the
// Ctrl+M chord unarmed.
func escapeConsumed(t *testing.T, fileMenu bool) {
	menu, open, typed := "palette", `Open the recording palette.`, `"a"`
	if fileMenu {
		menu, open, typed = "file menu", `Fill the message input with "@example". [data-testid="file-completion-menu"] should become visible.`, `"@examplea"`
	}
	e2e(t, `Plain typing is not recorded after the `+menu+` consumes Escape. `+recMocks+recPalette+recIDs+`
1. Navigate to /new. The voice button should be enabled.
2. `+open+`
3. Press "m" with modifier "Control", then press Escape.
4. No ".command-palette-input" and no [data-testid="file-completion-menu"] may exist (both counts 0).
5. Focus the message input (eval its focus()) and press "a".
6. The message input should have the value `+typed+`, and no recording panel may exist (count 0).
7. window.__recordingMock.microphoneRequests and window.__recordingMock.displayRequests must both be 0.`, recording)
}

func TestE2ERecordingPlainTypingAfterPaletteConsumesEscape(t *testing.T) {
	escapeConsumed(t, false)
}

func TestE2ERecordingPlainTypingAfterFileMenuConsumesEscape(t *testing.T) {
	escapeConsumed(t, true)
}

func TestE2ERecordingRejectedChooserRetriesScreenInClickTurn(t *testing.T) {
	e2e(t, `The composer is preserved after a rejected chooser, and screen capture is retried in a click turn. The test seeded a conversation at /c/{{SLUG}}. `+recMocks+`Setting window.__recordingMock.displayError to a message makes screen capture fail with it; "" makes it succeed. `+recShortcuts+recIDs+`Read the counts with eval "(m => ({display: m.displayRequests, synchronousDisplay: m.synchronousDisplayRequests, microphone: m.microphoneRequests, starts: m.recorderStarts}))(window.__recordingMock)".
1. Navigate to /c/{{SLUG}} and wait up to 30s for the message input to be visible. Fill it with "Keep this draft through screen failures.".
2. Eval "window.__recordingMock.displayError = 'Screen sharing was cancelled'".
3. Press the record-screen shortcut.
4. [data-testid="recording-error"] should have the text exactly "Screen sharing was cancelled", the recording panel should have data-mode "screen", and [data-testid="recording-preserved-text"] should have the text exactly "Keep this draft through screen failures.".
5. The counts must be exactly {"display":1,"synchronousDisplay":1,"microphone":0,"starts":0}.
6. Eval "window.__recordingMock.displayError = ''".
7. Click the retry button. [data-testid="recording-status"] should have the text exactly "Recording screen + microphone…".
8. The counts must be exactly {"display":2,"synchronousDisplay":2,"microphone":1,"starts":1}.
9. Click the cancel button. The message input should have the value "Keep this draft through screen failures." and be enabled.`, func(s *shelley) {
		recording(s)
		s.v("SLUG", s.conversation("echo: screen chooser retry", "").Slug)
	})
}

func TestE2ERecordingKeepsScreenAfterDestinationFailure(t *testing.T) {
	e2e(t, `Screen capture is kept after destination failure until the user cancels. `+recMocks+recShortcuts+`Draft creations fail with "draft unavailable"; call "draftText" returns the "draft" field of the last draft creation's JSON body ("none" before there is one). `+recIDs+`
1. Navigate to /new and wait up to 30s for the message input to be visible. Fill it with "Do not lose this failed recording draft.".
2. Press the record-screen shortcut.
3. Call "draftText" should return "Do not lose this failed recording draft.".
4. [data-testid="recording-error"] should come to contain "draft unavailable", and the recording state should be "recording".
5. window.__recordingMock.stoppedTracks must be 0.
6. Click the cancel button. The message input should have the value "Do not lose this failed recording draft." and be enabled.
7. window.__recordingMock.stoppedTracks should become at least 2 (eval "window.__recordingMock.stoppedTracks >= 2" should become true).
8. Eval "(m => ({display: m.displayRequests, synchronousDisplay: m.synchronousDisplayRequests}))(window.__recordingMock)" must return exactly {"display":1,"synchronousDisplay":1}.`, func(s *shelley) {
		recording(s)
		var mu sync.Mutex
		draft := "none"
		s.call("draftText", func() string {
			mu.Lock()
			defer mu.Unlock()
			return draft
		})
		s.route("POST /api/conversations/draft", func(w http.ResponseWriter, r *http.Request) {
			text, _ := object(readBody(r))["draft"].(string)
			mu.Lock()
			draft = text
			mu.Unlock()
			failText(w, http.StatusInternalServerError, "draft unavailable")
		})
	})
}

func TestE2ERecordingPaletteHidesActionsWhileAttachmentUploads(t *testing.T) {
	e2e(t, `Recording palette actions are hidden while an attachment uploads. The test seeded a conversation at /c/{{SLUG}}. Attachment uploads (POST /api/upload) are held back until call "releaseUpload". `+recPalette+recIDs+`
1. Navigate to /c/{{SLUG}} and wait up to 30s for the message input to be visible.
2. Drop a file on the composer: eval window.__dropFile("pending.txt", "pending").
3. Exactly one ".message-attachment-uploading" should exist.
4. Open the recording palette. There must be no palette item "Record audio" and no palette item "Record audio and screen" (both counts 0). Press Escape.
5. Call "releaseUpload". Exactly one ".message-attachment-ready" should come to exist.
6. Open the recording palette. The palette items "Record audio" and "Record audio and screen" should both be visible.`, func(s *shelley) {
		recording(s)
		s.initScript(`window.__dropFile = (name, contents) => {
  const transfer = new DataTransfer();
  transfer.items.add(new File([contents], name, { type: 'text/plain' }));
  document.querySelector('.message-input-container')?.dispatchEvent(
    new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: transfer }));
  return 'dropped';
};`)
		s.v("SLUG", s.conversation("echo: recording action upload guard", "").Slug)
		release := s.gate("releaseUpload")
		s.route("POST /api/upload", func(w http.ResponseWriter, r *http.Request) {
			if release.wait(r) {
				s.pass.ServeHTTP(w, r)
			}
		})
	})
}

func TestE2ERecordingPaletteHidesActionsInArchivedConversation(t *testing.T) {
	e2e(t, `Recording palette actions are hidden in an archived conversation. The test seeded and archived a conversation {{ARCHIVED}}. `+recPalette+recIDs+`
1. Navigate to /new and wait up to 30s for the message input to be visible.
2. If the button with aria-label "Open conversations" is visible, click it.
3. Click the button "View Archived", then click ".conversation-item[data-conversation-id=\"{{ARCHIVED}}\"]".
4. The message input should become hidden.
5. Open the recording palette. There must be no palette item "Record audio" and no palette item "Record audio and screen" (both counts 0).`, func(s *shelley) {
		recording(s)
		archived := s.conversation("echo: archived recording action guard", "")
		s.api("POST", "/api/conversation/"+archived.ID+"/archive", nil, nil)
		s.v("ARCHIVED", archived.ID)
	})
}

// submissions lists chats as JSON for a call: each one's fields, where
// "path" is its URL path, a name ending in "?" says whether its body has that
// property, and other names are properties of its body.
func submissions(chats *requests, fields ...string) func() string {
	return func() string {
		out := []ordered{}
		for _, c := range chats.all() {
			body := object(c.body)
			var o ordered
			for _, f := range fields {
				switch name, present := strings.CutSuffix(f, "?"); {
				case f == "path":
					o = append(o, field{f, c.path})
				case present:
					_, has := body[name]
					o = append(o, field{name, has})
				default:
					o = append(o, field{f, body[f]})
				}
			}
			out = append(out, o)
		}
		return jsonText(out)
	}
}

// switchingRecorder is the spec's test of one live recorder across a switch
// away from an existing draft or conversation and back.
func switchingRecorder(t *testing.T, draft bool) {
	kind, prepare, original := "conversation", `Fill it with "Preserve the conversation recording context.".`, `the conversation at /c/{{ORIGINAL}}`
	if draft {
		kind, prepare, original = "draft", `It should have the value "Preserve the draft recording context.".`, `a draft conversation at /c/{{ORIGINAL}} with the draft text "Preserve the draft recording context."`
	}
	e2e(t, `One live recorder is kept when switching from an existing `+kind+` and returning. The test seeded `+original+`, and another conversation {{OTHER}} at /c/{{OTHER_SLUG}}. `+recMocks+recDrawer+recFloating+`Recording uploads are counted by call "uploads". "The recorder counts" are what eval "(m => ({starts: m.recorderStarts, stops: m.recorderStops}))(window.__recordingMock)" returns. `+recIDs+`
1. Navigate to /c/{{ORIGINAL}} and wait up to 30s for the message input to be visible. `+prepare+`
2. Click the voice button. The recording panel should be visible. Mark it: eval "document.querySelector('[data-testid=\"recording-panel\"]').setAttribute('data-instance-marker', 'original')".
3. window.__recordingMock.recorderStarts should become 1.
4. Select conversation {{OTHER}} from the drawer. The URL should have exactly the path "/c/{{OTHER_SLUG}}" (an exact "value", not "text").
5. Exactly one recording panel should exist (count 1), and it should still have data-instance-marker "original", the recorder should float, [data-testid="recording-return-button"] should be visible, and the voice button should be disabled.
6. The recorder counts must be exactly {"starts":1,"stops":0}.
7. Click [data-testid="recording-return-button"]. The URL should have exactly the path "/c/{{ORIGINAL}}" (an exact "value", not "text").
8. Exactly one recording panel should exist (count 1), and it should still have data-instance-marker "original", the recorder should not float, and no [data-testid="recording-return-button"] may exist (count 0).
9. The recorder counts must be exactly {"starts":1,"stops":0}.
10. [data-testid="recording-preserved-text"] should have the text exactly "Preserve the `+kind+` recording context.".
11. Click the cancel button. No recording panel should remain (count 0).
12. Call "uploads" must return "0", and window.__recordingMock.recorderStops must be 1.`, func(s *shelley) {
		recording(s)
		other := s.conversation("echo: floating recorder destination from "+kind, "")
		s.v("OTHER", other.ID)
		s.v("OTHER_SLUG", other.Slug)
		if draft {
			s.v("ORIGINAL", s.draft("Preserve the draft recording context."))
		} else {
			s.v("ORIGINAL", s.conversation("echo: floating recorder origin", "").Slug)
		}
		s.fakeUploads("/tmp/shelley-uploads/unexpected.webm")
	})
}

func TestE2ERecordingOneLiveRecorderSwitchingFromDraft(t *testing.T) {
	switchingRecorder(t, true)
}

func TestE2ERecordingOneLiveRecorderSwitchingFromConversation(t *testing.T) {
	switchingRecorder(t, false)
}

func TestE2ERecordingScreenCaptureAndPreviewSurviveNavigation(t *testing.T) {
	e2e(t, `One screen capture and its preview stay alive across navigation and return. The test seeded the conversation at /c/{{ORIGINAL_SLUG}} and another conversation {{VIEWED}}. `+recMocks+recDrawer+recFloating+`Recording uploads are counted by call "uploads". "The preview" is [data-testid="recording-preview"], a video element. "The capture counts" are what eval "({sameStarts: window.__recordingMock.recorderStarts === window.__starts, displayRequests: window.__recordingMock.displayRequests})" returns. "The preview keeps its stream" when eval "document.querySelector('[data-testid=\"recording-preview\"]').srcObject === window.__recordingPreviewSource" is true. `+recIDs+`
1. Navigate to /c/{{ORIGINAL_SLUG}} and wait up to 30s for the message input to be visible.
2. Click the voice button. [data-testid="recording-status"] should have the text exactly "Recording…".
3. Click [data-testid="recording-screen-button"].
4. The recording panel should get data-mode "screen", and the preview should be visible.
5. Mark them in one eval: "(() => { document.querySelector('[data-testid=\"recording-panel\"]').setAttribute('data-instance-marker', 'screen'); const p = document.querySelector('[data-testid=\"recording-preview\"]'); p.setAttribute('data-instance-marker', 'screen-preview'); window.__recordingPreviewSource = p.srcObject; window.__starts = window.__recordingMock.recorderStarts; return 'marked'; })()".
6. window.__starts must be greater than 0, and window.__recordingMock.displayRequests must be 1.
7. Select conversation {{VIEWED}} from the drawer.
8. Exactly one recording panel and one preview should exist (count 1 each); the panel should still have data-instance-marker "screen" and the preview data-instance-marker "screen-preview". The preview must keep its stream, and the recorder should float.
9. The capture counts must be exactly {"sameStarts":true,"displayRequests":1}.
10. Click [data-testid="recording-return-button"]. The URL should have exactly the path "/c/{{ORIGINAL_SLUG}}" (an exact "value", not "text").
11. Exactly one recording panel and one preview should exist (count 1 each); the panel should still have data-instance-marker "screen" and the preview data-instance-marker "screen-preview". The preview must keep its stream, and the recorder should not float.
12. The capture counts must be exactly {"sameStarts":true,"displayRequests":1}.
13. Click the cancel button. Call "uploads" must return "0".`, func(s *shelley) {
		recording(s)
		s.v("ORIGINAL_SLUG", s.conversation("echo: floating screen origin", "").Slug)
		s.v("VIEWED", s.conversation("echo: floating screen destination", "").ID)
		s.fakeUploads("/tmp/shelley-uploads/unexpected.webm")
	})
}

func TestE2ERecordingFloatsWhileArchivedConversationViewed(t *testing.T) {
	e2e(t, `The active recorder keeps floating while an archived conversation is viewed. The test seeded the conversation at /c/{{ORIGINAL_SLUG}}, and archived another conversation {{ARCHIVED}} at /c/{{ARCHIVED_SLUG}}. `+recMocks+recFloating+`Recording uploads are counted by call "uploads". `+recIDs+`
1. Navigate to /c/{{ORIGINAL_SLUG}} and wait up to 30s for the message input to be visible.
2. Click the voice button. The recording panel should be visible. Mark it: eval "document.querySelector('[data-testid=\"recording-panel\"]').setAttribute('data-instance-marker', 'archived')".
3. If the button with aria-label "Open conversations" is visible, click it. Click the button "View Archived", then click ".conversation-item[data-conversation-id=\"{{ARCHIVED}}\"]".
4. The URL should have exactly the path "/c/{{ARCHIVED_SLUG}}" (an exact "value", not "text"), and the message input should be hidden.
5. Exactly one recording panel should exist (count 1); it should be visible and still have data-instance-marker "archived", the recorder should float, and [data-testid="recording-return-button"] should be visible.
6. Click [data-testid="recording-return-button"]. The URL should have exactly the path "/c/{{ORIGINAL_SLUG}}" (an exact "value", not "text"), and the recorder should not float.
7. Click the cancel button. Call "uploads" must return "0".`, func(s *shelley) {
		recording(s)
		s.v("ORIGINAL_SLUG", s.conversation("echo: recorder beside archived view origin", "").Slug)
		archived := s.conversation("echo: recorder beside archived view destination", "")
		s.api("POST", "/api/conversation/"+archived.ID+"/archive", nil, nil)
		s.v("ARCHIVED", archived.ID)
		s.v("ARCHIVED_SLUG", archived.Slug)
		s.fakeUploads("/tmp/shelley-uploads/unexpected.webm")
	})
}

func TestE2ERecordingRestoresSourceDraftKeepsViewedDraftSeparate(t *testing.T) {
	e2e(t, `The source draft is restored after return, and the viewed draft is kept separate. The test seeded the conversation at /c/{{ORIGINAL_SLUG}} and a draft conversation {{VIEWED}} with the draft text "Seeded text in the viewed draft.". `+recMocks+recDrawer+`Recording uploads are counted by call "uploads". `+recIDs+`
1. Navigate to /c/{{ORIGINAL_SLUG}} and wait up to 30s for the message input to be visible. Fill it with "Restore this source text after cancellation.".
2. Click the voice button. The recording panel should be visible.
3. Select conversation {{VIEWED}} from the drawer. The message input should have the value "Seeded text in the viewed draft.".
4. Fill it with "Edited and read text in the viewed draft.". It should have that value.
5. Click [data-testid="recording-return-button"]. The URL should have exactly the path "/c/{{ORIGINAL_SLUG}}" (an exact "value", not "text").
6. Click the cancel button. The message input should have the value "Restore this source text after cancellation.".
7. Select conversation {{VIEWED}} from the drawer. The message input should have the value "Edited and read text in the viewed draft.".
8. Call "uploads" must return "0".`, func(s *shelley) {
		recording(s)
		s.v("ORIGINAL_SLUG", s.conversation("echo: composer restore recording origin", "").Slug)
		s.v("VIEWED", s.draft("Seeded text in the viewed draft."))
		s.fakeUploads("/tmp/shelley-uploads/unexpected.webm")
	})
}

func TestE2ERecordingStopsFloatingIntoOriginalConversation(t *testing.T) {
	e2e(t, `Stopping a floating recording submits it to its original conversation without touching the viewed composer. The test seeded the conversation {{ORIGINAL}} at /c/{{ORIGINAL_SLUG}} and another conversation {{VIEWED}} at /c/{{VIEWED_SLUG}}. `+recMocks+recDrawer+recFloating+recPaste+`Attachment uploads (POST /api/upload) answer with the path /tmp/shelley-uploads/viewed-draft.txt. Recording uploads (POST /api/upload/raw) answer with /tmp/shelley-uploads/floating.webm, and call "uploads" counts them. Message submissions are accepted without being sent on; call "chats" counts them, and call "submissions" lists them as JSON, each with its URL path, message, model, and whether its body has a transcription_context property. `+recIDs+`
1. Navigate to /c/{{ORIGINAL_SLUG}} and wait up to 30s for the message input to be visible. Fill it with "Original spoken context.".
2. Click the voice button. [data-testid="recording-status"] should have the text exactly "Recording…".
3. Select conversation {{VIEWED}} from the drawer. Fill the message input with "Do not disturb this viewed draft.". Paste file "viewed-draft.txt".
4. The voice button should be disabled, and the recorder should float.
5. Click the stop button. Call "chats" should return "1". No recording panel should remain (count 0).
6. Call "submissions" must return exactly [{"path":"/api/conversation/{{ORIGINAL}}/chat","message":"/transcription /tmp/shelley-uploads/floating.webm\nOriginal spoken context.","model":"predictable","transcription_context":false}].
7. Call "uploads" must return "1".
8. The URL should have exactly the path "/c/{{VIEWED_SLUG}}" (an exact "value", not "text"), and the message input should have the value "Do not disturb this viewed draft.".
9. The ".message-attachment-name" should have the text exactly "viewed-draft.txt", and exactly one ".message-attachment-ready" should exist.`, func(s *shelley) {
		recording(s)
		original := s.conversation("echo: floating stop origin", "")
		viewed := s.conversation("echo: floating stop destination", "")
		s.v("ORIGINAL", original.ID)
		s.v("ORIGINAL_SLUG", original.Slug)
		s.v("VIEWED", viewed.ID)
		s.v("VIEWED_SLUG", viewed.Slug)
		s.route("POST /api/upload", func(w http.ResponseWriter, r *http.Request) {
			fulfill(w, http.StatusOK, map[string]string{"path": "/tmp/shelley-uploads/viewed-draft.txt"})
		})
		s.fakeUploads("/tmp/shelley-uploads/floating.webm")
		s.call("submissions", submissions(s.acceptChats(), "path", "message", "model", "transcription_context?"))
	})
}

func TestE2ERecordingDiscardWhileFloatingSavesTrailingSourceEdit(t *testing.T) {
	e2e(t, `Discarding while floating saves a trailing source edit without touching the viewed draft. The test seeded a draft conversation {{ORIGINAL}} with the draft text "Original saved draft before the final edit.", and another draft conversation {{VIEWED}} with the draft text "Keep the viewed saved draft.". `+recMocks+recDrawer+recFloating+`Call "savedDrafts" returns, as JSON, the drafts the server has saved for [{{ORIGINAL}}, {{VIEWED}}]. Recording uploads are counted by call "uploads". `+recIDs+`
1. Navigate to /c/{{ORIGINAL}}. The message input should have the value "Original saved draft before the final edit." (wait up to 30s).
Leave the edit inside the autosave debounce, then switch immediately. The trailing save must retain the source id instead of reading the newly viewed draft from mutable composer mirrors.
2. Fill the message input with "Keep the original saved draft's final edit.", then immediately click the voice button and select conversation {{VIEWED}} from the drawer.
3. The message input should have the value "Keep the viewed saved draft.", and the recorder should float.
4. Call "savedDrafts" should return exactly ["Keep the original saved draft's final edit.","Keep the viewed saved draft."].
5. Click the cancel button. No recording panel should remain (count 0), and the message input should have the value "Keep the viewed saved draft.".
6. Select conversation {{ORIGINAL}} from the drawer. The message input should have the value "Keep the original saved draft's final edit.".
7. Select conversation {{VIEWED}} from the drawer. The message input should have the value "Keep the viewed saved draft.".
8. Call "uploads" must return "0".`, func(s *shelley) {
		recording(s)
		original := s.draft("Original saved draft before the final edit.")
		viewed := s.draft("Keep the viewed saved draft.")
		s.v("ORIGINAL", original)
		s.v("VIEWED", viewed)
		s.call("savedDrafts", func() string {
			var drafts []string
			for _, id := range []string{original, viewed} {
				var c struct {
					Draft string `json:"draft"`
				}
				s.api("GET", "/api/conversation-by-slug/"+id, nil, &c)
				drafts = append(drafts, c.Draft)
			}
			return jsonText(drafts)
		})
		s.fakeUploads("/tmp/shelley-uploads/unexpected.webm")
	})
}

func TestE2ERecordingBlankNewConversationSavedReturnTarget(t *testing.T) {
	e2e(t, `A blank new-conversation recording gets a saved return target. The test seeded a conversation {{VIEWED}}. `+recMocks+recDrawer+recFloating+`Call "draftCreated" says ("true" or "false") whether the browser's first draft creation (POST /api/conversations/draft) has been answered and, once it has, sets {{DRAFT}} to the conversation it created; call "draftResponse" reports that answer's status and the "draft" field of its request as JSON. Recording uploads are counted by call "uploads". `+recIDs+`
1. Navigate to /new and click the voice button.
2. Call "draftCreated" should return "true" within 10s.
3. Call "draftResponse" must return exactly {"status":201,"draft":""}.
4. The URL should have exactly the path "/c/{{DRAFT}}" (an exact "value", not "text").
5. The recording panel should be visible. Mark it: eval "document.querySelector('[data-testid=\"recording-panel\"]').setAttribute('data-instance-marker', 'blank-new')".
6. Select conversation {{VIEWED}} from the drawer. The recorder should float, and the voice button should be disabled.
7. Click [data-testid="recording-return-button"]. The URL should have exactly the path "/c/{{DRAFT}}" (an exact "value", not "text").
8. Exactly one recording panel should exist (count 1), and it should still have data-instance-marker "blank-new", and the recorder should not float.
9. Click the cancel button. Call "uploads" must return "0".`, func(s *shelley) {
		recording(s)
		s.v("VIEWED", s.conversation("echo: blank recording destination", "").ID)
		created := s.capture("draftCreated", "DRAFT")
		var mu sync.Mutex
		response := "none"
		s.call("draftResponse", func() string {
			mu.Lock()
			defer mu.Unlock()
			return response
		})
		s.route("POST /api/conversations/draft", func(w http.ResponseWriter, r *http.Request) {
			draft := object(readBody(r))["draft"]
			f, err := s.fetch(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			f.relay(w)
			mu.Lock()
			if response == "none" {
				response = jsonText(ordered{{"status", f.status}, {"draft", draft}})
			}
			mu.Unlock()
			created.set(f.id())
		})
		s.fakeUploads("/tmp/shelley-uploads/unexpected.webm")
	})
}

// recHeldDraft describes heldDraft.
const recHeldDraft = `The server's answer to the browser's draft creation (POST /api/conversations/draft) is held back until call "releaseDraft"; call "draftCreated" says ("true" or "false") whether the server has created the draft and, once it has, sets {{DRAFT}} to its conversation ID; call "draftAnswered" says whether the browser has received the answer. `

func TestE2ERecordingDelayedBlankDraftNoHijack(t *testing.T) {
	e2e(t, `Delayed blank-recording draft creation does not hijack a later selection. The test seeded a conversation {{VIEWED}} at /c/{{VIEWED_SLUG}}. `+recMocks+recDrawer+recFloating+recHeldDraft+recIDs+`
1. Navigate to /new and click the voice button.
2. Call "draftCreated" until it returns "true".
3. Exactly one [data-testid="recording-inline"] should exist, and window.__recordingMock.recorderStarts should become 1.
4. Select conversation {{VIEWED}} from the drawer. The URL should have exactly the path "/c/{{VIEWED_SLUG}}" (an exact "value", not "text"), and the voice button should be disabled.
5. Call "releaseDraft", then call "draftAnswered" until it returns "true".
6. The recording panel should be visible, the recorder should float, and the URL should still have exactly the path "/c/{{VIEWED_SLUG}}" (an exact "value", not "text"). window.__recordingMock.recorderStarts should be 1.
7. Click [data-testid="recording-return-button"]. The URL should have exactly the path "/c/{{DRAFT}}" (an exact "value", not "text"), and the recorder should not float.
8. Click the cancel button.`, func(s *shelley) {
		recording(s)
		viewed := s.conversation("echo: delayed blank recording destination", "")
		s.v("VIEWED", viewed.ID)
		s.v("VIEWED_SLUG", viewed.Slug)
		s.heldDraft()
	})
}

// delayedSend is the spec's test that a send waits for its delayed
// new-conversation draft, as a normal message or a /transcription command.
func delayedSend(t *testing.T, transcription bool) {
	label, fill, message, chats := "Normal send", `"Send this message to the draft that created it."`, "Send this message to the draft that created it.", `reach the server`
	if transcription {
		label, fill, message, chats = "Explicit transcription command", `two lines, "/transcription /tmp/shelley-uploads/existing-recording.webm" and "Keep this context with it.", separated by a newline`, "/transcription /tmp/shelley-uploads/existing-recording.webm\nKeep this context with it.", `are accepted without being sent on`
	}
	last := ""
	if !transcription {
		last = `
9. Select conversation {{DRAFT}} from the drawer. The message input should have the value "" (empty).`
	}
	e2e(t, label+` waits for its delayed new-conversation draft without following later navigation. The test seeded a conversation {{VIEWED}} at /c/{{VIEWED_SLUG}}. `+recDrawer+recHeldDraft+`Message submissions (POST /api/conversation/<id>/chat) `+chats+`; call "chats" counts them, and call "submissions" lists them as JSON, each with its URL path and message. `+recIDs+`
1. Navigate to /new. Fill the message input with `+fill+`.
2. Call "draftCreated" until it returns "true".
3. Click [data-testid="send-button"].
4. Select conversation {{VIEWED}} from the drawer. The URL should have exactly the path "/c/{{VIEWED_SLUG}}" (an exact "value", not "text").
5. Call "releaseDraft". Call "chats" should return "1".
6. Call "submissions" must return exactly [`+jsonText(ordered{{"path", "/api/conversation/{{DRAFT}}/chat"}, {"message", message}})+`].
7. The URL should still have exactly the path "/c/{{VIEWED_SLUG}}" (an exact "value", not "text").
8. Eval "localStorage.getItem('shelley-draft:{{DRAFT}}') === null" should become true.`+last, func(s *shelley) {
		recording(s)
		viewed := s.conversation("echo: delayed "+strings.ToLower(label)+" destination", "")
		s.v("VIEWED", viewed.ID)
		s.v("VIEWED_SLUG", viewed.Slug)
		s.heldDraft()
		var chats requests
		s.call("chats", func() string { return strconv.Itoa(len(chats.all())) })
		s.call("submissions", submissions(&chats, "path", "message"))
		s.route(chatPattern, func(w http.ResponseWriter, r *http.Request) {
			chats.add(r)
			if transcription {
				acceptChat(w)
			} else {
				s.pass.ServeHTTP(w, r)
			}
		})
	})
}

func TestE2ERecordingNormalSendWaitsForDelayedDraft(t *testing.T) {
	delayedSend(t, false)
}

func TestE2ERecordingTranscriptionCommandWaitsForDelayedDraft(t *testing.T) {
	delayedSend(t, true)
}

func TestE2ERecordingDelayedDraftMovesAfterNavigation(t *testing.T) {
	e2e(t, `A delayed new-conversation recording draft moves after navigation and clears its source. The test seeded a conversation {{VIEWED}} at /c/{{VIEWED_SLUG}}. `+recMocks+recDrawer+recFloating+recNewFromHeader+recHeldDraft+`Call "draftText" returns the "draft" field of the last draft creation's request. Recording uploads (POST /api/upload/raw) answer with the path /tmp/shelley-uploads/delayed-new.webm. Message submissions are accepted without being sent on; call "chats" counts them, and call "submissions" lists them as JSON, each with its URL path, message and model. `+recIDs+`
1. Navigate to /new. Fill the message input with "Initial text while the draft request starts.".
2. Call "draftCreated" until it returns "true". Call "draftText" must return "Initial text while the draft request starts.".
3. Fill the message input with "Latest text owned by the delayed recording draft.". Click the voice button.
4. Exactly one [data-testid="recording-inline"] should exist, and window.__recordingMock.recorderStarts should become 1.
5. Select conversation {{VIEWED}} from the drawer. The URL should have exactly the path "/c/{{VIEWED_SLUG}}" (an exact "value", not "text").
6. Call "releaseDraft", then call "draftAnswered" until it returns "true".
7. The recording panel should be visible, the recorder should float, and the URL should still have exactly the path "/c/{{VIEWED_SLUG}}" (an exact "value", not "text").
8. Click the stop button. Call "chats" should return "1". No recording panel should remain (count 0).
9. Call "submissions" must return exactly [{"path":"/api/conversation/{{DRAFT}}/chat","message":"/transcription /tmp/shelley-uploads/delayed-new.webm\nLatest text owned by the delayed recording draft.","model":"predictable"}].
10. The URL should still have exactly the path "/c/{{VIEWED_SLUG}}" (an exact "value", not "text").
11. Start a new conversation from the header. The URL should have exactly the path "/new" (an exact "value", not "text"), and the message input should have the value "" (empty).`, func(s *shelley) {
		recording(s)
		viewed := s.conversation("echo: delayed nonempty recording destination", "")
		s.v("VIEWED", viewed.ID)
		s.v("VIEWED_SLUG", viewed.Slug)
		s.heldDraft()
		s.fakeUploads("/tmp/shelley-uploads/delayed-new.webm")
		s.call("submissions", submissions(s.acceptChats(), "path", "message", "model"))
	})
}

func TestE2ERecordingDelayedDraftSparesNewerComposer(t *testing.T) {
	e2e(t, `A delayed recording draft does not consume a newer new-conversation composer. The test seeded a conversation {{VIEWED}}. `+recMocks+recDrawer+recFloating+recNewFromHeader+`The server's answer to the browser's first draft creation (POST /api/conversations/draft) is held back until call "releaseOldDraft"; call "oldDraftCreated" says ("true" or "false") whether the server has created it and, once it has, sets {{OLD}} to its conversation ID; call "oldDraftAnswered" says whether the browser has received that answer. The answers to later draft creations are held back for the rest of the test. `+recIDs+`
1. Navigate to /new. Fill the message input with "Text owned by the older delayed draft.".
2. Call "oldDraftCreated" until it returns "true". Click the voice button.
3. Exactly one [data-testid="recording-inline"] should exist, and window.__recordingMock.recorderStarts should become 1.
4. Select conversation {{VIEWED}} from the drawer. Start a new conversation from the header. The URL should have exactly the path "/new" (an exact "value", not "text").
5. Fill the message input with "Text entered in the newer new-conversation session.".
6. Call "releaseOldDraft", then call "oldDraftAnswered" until it returns "true".
7. The recording panel should be visible, the recorder should float, the URL should still have exactly the path "/new" (an exact "value", not "text"), and the message input should have the value "Text entered in the newer new-conversation session.".
8. Click [data-testid="recording-return-button"]. The URL should have exactly the path "/c/{{OLD}}" (an exact "value", not "text").
9. Click the cancel button. The message input should have the value "Text owned by the older delayed draft.".
10. Start a new conversation from the header. The URL should have exactly the path "/new" (an exact "value", not "text"), and the message input should have the value "Text entered in the newer new-conversation session.".`, func(s *shelley) {
		recording(s)
		s.v("VIEWED", s.conversation("echo: delayed recording before newer new conversation", "").ID)
		release := s.gate("releaseOldDraft")
		created := s.capture("oldDraftCreated", "OLD")
		answered := s.signal("oldDraftAnswered")
		var requests atomic.Int64
		s.route("POST /api/conversations/draft", func(w http.ResponseWriter, r *http.Request) {
			first := requests.Add(1) == 1
			f, err := s.fetch(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			if !first {
				<-r.Context().Done()
				return
			}
			created.set(f.id())
			if release.wait(r) {
				f.relay(w)
				answered.Store(true)
			}
		})
	})
}

func TestE2ERecordingNarrowDesktopControlsFit(t *testing.T) {
	e2e(t, `The recording controls fit in a narrow desktop composer. `+recMocks+recIDs+`
1. Navigate to /new and wait up to 30s for the message input to be visible. Fill it with "Keep this long note with the recording so the status has to truncate.".
2. Click the voice button. The recording state should become "recording".
3. Eval "(() => { const b = s => { const r = document.querySelector(s).getBoundingClientRect(); return {left: r.x, right: r.x + r.width, width: r.width}; }; const panel = b('.recording-panel'), main = b('.recording-panel-main'), waveform = b('[data-testid=\"recording-waveform\"]'), preserved = b('[data-testid=\"recording-preserved-text\"]'), timer = b('[data-testid=\"recording-timer\"]'), screen = b('[data-testid=\"recording-screen-button\"]'), cancel = b('[data-testid=\"recording-cancel-button\"]'); return {waveformInPanel: waveform.left > panel.left, waveformBeforeText: waveform.right <= preserved.left, textWide: preserved.width > 40, textBeforeTimer: preserved.right <= timer.left, timerWide: timer.width > 30, timerInMain: timer.right <= main.right, mainBeforeScreen: main.right <= screen.left, cancelInPanel: cancel.right < panel.right}; })()" must return exactly {"waveformInPanel":true,"waveformBeforeText":true,"textWide":true,"textBeforeTimer":true,"timerWide":true,"timerInMain":true,"mainBeforeScreen":true,"cancelInPanel":true}.
The secondary action goes icon-only before the primary ones do.
4. '[data-testid="recording-screen-button"] .recording-action-label' should be hidden; '[data-testid="recording-stop-button"] .recording-action-label' and '[data-testid="recording-cancel-button"] .recording-action-label' should be visible.
5. Click the cancel button. The message input should have the value "Keep this long note with the recording so the status has to truncate.".`, func(s *shelley) {
		recording(s)
		s.viewport(660, 800)
	})
}

func TestE2ERecordingDiscardsMicrophoneSubmitsScreen(t *testing.T) {
	e2e(t, `Microphone capture is discarded and the screen recording submitted for transcription. `+recMocks+`Recording uploads (POST /api/upload/raw) answer with the path /tmp/shelley-uploads/screen.webm.json for filenames ending in ".json" and /tmp/shelley-uploads/screen.webm otherwise; call "uploadNames" lists their filenames as JSON, with every digit replaced by "#". Message submissions are accepted without being sent on; call "transcription" returns the last one's message and model as JSON ("none" before there is one). `+recIDs+`
1. Navigate to /new and click the voice button. [data-testid="recording-status"] should have the text exactly "Recording…".
2. Click [data-testid="recording-screen-button"].
3. The recording panel should get data-mode "screen", [data-testid="recording-preview"] should be visible, and [data-testid="recording-status"] should have the text exactly "Recording screen + microphone…".
4. Call "uploadNames" must return exactly [].
5. Eval "(m => ({display: m.displayRequests, microphone: m.microphoneRequests, sources: m.audioSources}))(window.__recordingMock)" must return exactly {"display":1,"microphone":2,"sources":4}.
6. Press the stop button with a touch pointerdown: eval "document.querySelector('[data-testid=\"recording-stop-button\"]').dispatchEvent(new PointerEvent('pointerdown', {pointerType: 'touch', button: 0, bubbles: true, cancelable: true, composed: true}))".
7. Call "transcription" should return exactly {"message":"/transcription /tmp/shelley-uploads/screen.webm","model":"predictable"}.
8. The message input should have the value "" (empty).
9. Call "uploadNames" must return exactly ["rec-########-######.webm","screen.webm.json"].`, func(s *shelley) {
		recording(s)
		var mu sync.Mutex
		names := []string{}
		s.call("uploadNames", func() string {
			mu.Lock()
			defer mu.Unlock()
			return jsonText(names)
		})
		digits := regexp.MustCompile(`[0-9]`)
		s.route("POST /api/upload/raw", func(w http.ResponseWriter, r *http.Request) {
			name := r.URL.Query().Get("filename")
			mu.Lock()
			names = append(names, digits.ReplaceAllString(name, "#"))
			mu.Unlock()
			path := "/tmp/shelley-uploads/screen.webm"
			if strings.HasSuffix(name, ".json") {
				path += ".json"
			}
			fulfill(w, http.StatusOK, map[string]string{"path": path})
		})
		s.lastTranscription()
	})
}

// lastTranscription accepts message submissions without sending them on;
// call "transcription" returns the last one's message and model as JSON.
func (s *shelley) lastTranscription() {
	var mu sync.Mutex
	last := "none"
	s.call("transcription", func() string {
		mu.Lock()
		defer mu.Unlock()
		return last
	})
	s.route(chatPattern, func(w http.ResponseWriter, r *http.Request) {
		body := object(readBody(r))
		mu.Lock()
		last = jsonText(ordered{{"message", body["message"]}, {"model", body["model"]}})
		mu.Unlock()
		acceptChat(w)
	})
}

func TestE2ERecordingHoverOffersVoiceAndScreen(t *testing.T) {
	e2e(t, `Hovering the record button offers Voice & Screen with a single screen request. `+recMocks+`"The record menu" is [data-testid="record-menu"]. `+recIDs+`
1. Navigate to /new. No record menu may exist (count 0).
2. Hover the voice button. The record menu should become visible.
3. Exactly 2 buttons ('[data-testid="record-menu"] button') should be in it. The first's accessible name (its aria-label) should be exactly "Voice", and the last's exactly "Voice & Screen".
Moving away closes it.
4. Hover [data-testid="attach-button"]. No record menu should remain (count 0).
5. Hover the voice button, then click the record menu's button "Voice & Screen" ('[data-testid="record-menu"] button[aria-label="Voice & Screen"]').
6. The recording panel should get data-mode "screen", and [data-testid="recording-status"] should have the text exactly "Recording screen + microphone…".
7. Eval "JSON.stringify((m => ({display: m.displayRequests, synchronous: m.synchronousDisplayRequests, microphone: m.microphoneRequests}))(window.__recordingMock))" must return exactly {"display":1,"synchronous":1,"microphone":1}.`, recording)
}

func TestE2ERecordingLongPressOpensMenuWithoutRecording(t *testing.T) {
	// Playwright let the fake clock flow between its steps; this pauses it
	// so that only runFor moves it past the 450ms long press, and resumes it
	// for the final click.
	e2e(t, `Long-pressing the record button opens the menu without starting a recording. `+recMocks+`The page runs on a fake clock that steps drive with window.__clock: pauseAt(ms), runFor(ms) and resume(). "The record menu" is [data-testid="record-menu"]. `+recIDs+`
1. Navigate to /new. The voice button should be visible.
2. Pause the clock a moment from now: eval "window.__clock.pauseAt(Date.now() + 100)" (the clock cannot go back, and time flows on while the step runs).
3. Touch the voice button: eval "document.querySelector('[data-testid=\"voice-button\"]').dispatchEvent(new PointerEvent('pointerdown', {pointerType: 'touch', button: 0, isPrimary: true, bubbles: true, cancelable: true, composed: true}))".
4. Run the clock 200ms (eval "window.__clock.runFor(200)"). No record menu may exist (count 0).
5. Run the clock 400ms more (eval "window.__clock.runFor(400)"). The record menu should be visible.
The click that ends a long press must not also start voice recording.
6. Lift the touch and click, in one eval: "(b => { b.dispatchEvent(new PointerEvent('pointerup', {pointerType: 'touch', button: 0, isPrimary: true, bubbles: true, cancelable: true, composed: true})); b.dispatchEvent(new MouseEvent('click', {detail: 1, bubbles: true, cancelable: true, composed: true})); return 'released'; })(document.querySelector('[data-testid=\"voice-button\"]'))".
7. No recording panel may exist (count 0), and the record menu should still be visible.
8. Resume the clock: eval "window.__clock.resume()".
9. Click the record menu's button "Voice" ('[data-testid="record-menu"] button[aria-label="Voice"]').
10. The recording panel should get data-mode "microphone", and window.__recordingMock.displayRequests must be 0.`, func(s *shelley) {
		recording(s)
		s.clock(time.Time{})
	})
}

func TestE2ERecordingKeyboardFocusRevealsChoices(t *testing.T) {
	e2e(t, `Keyboard focus reveals the record choices, and Escape returns focus to the button. `+recMocks+`"The record menu" is [data-testid="record-menu"]. Keys go to whatever has focus: do not click anything in this test. "X is focused" means X is document.activeElement. `+recIDs+`
1. Navigate to /new. The voice button should be visible.
2. Focus [data-testid="attach-button"] (eval its focus()). Press Tab.
3. The voice button should be focused, and the record menu visible.
4. Press Tab twice. The record menu's button "Voice & Screen" ('[data-testid="record-menu"] button[aria-label="Voice & Screen"]') should be focused.
5. Press Escape. No record menu may remain (count 0), and the voice button should be focused.
6. Press Enter. The recording panel should get data-mode "microphone".`, recording)
}

func TestE2ERecordingPrerollScreenEndUsesRecorderLifetime(t *testing.T) {
	e2e(t, `The recorder lifetime is used when screen sharing ends during pre-roll. `+recMocks+`window.__recordingMock.endDisplay() ends the screen share as the browser's "Stop sharing" would. The page runs on a fake clock that is already paused when the page loads, at window.__captureStartedAt (epoch milliseconds); steps drive it with window.__clock.runFor(ms) and window.__clock.setFixedTime(ms). Click buttons with click steps on their selectors, never with an eval. Recording uploads (POST /api/upload/raw) answer with the path /tmp/shelley-uploads/preroll.webm.json for filenames ending in ".json" and /tmp/shelley-uploads/preroll.webm otherwise; call "metadataDuration" returns the duration_ms of the last metadata (".json") upload's JSON body ("none" before there is one). Message submissions are accepted without being sent on. `+recIDs+`
1. Navigate to /new. The voice button should be visible; click it.
2. The recording state should become "preroll".
3. Run the clock 100ms (eval "window.__clock.runFor(100)"). The recording state should become "recording".
4. Click [data-testid="recording-screen-button"]. The recording state should become "preroll".
5. Fix the time 1100ms after the capture started: eval "window.__clock.setFixedTime(window.__captureStartedAt + 1100)".
6. End the screen share: eval "window.__recordingMock.endDisplay()".
7. No recording panel should remain (count 0).
8. Call "metadataDuration" must return "1000".`, func(s *shelley) {
		recording(s)
		s.clock(time.Time{})
		s.initScript(`window.__captureStartedAt = Date.now() + 1000;
window.__clock.pauseAt(window.__captureStartedAt);`)
		var mu sync.Mutex
		duration := "none"
		s.call("metadataDuration", func() string {
			mu.Lock()
			defer mu.Unlock()
			return duration
		})
		s.route("POST /api/upload/raw", func(w http.ResponseWriter, r *http.Request) {
			path := "/tmp/shelley-uploads/preroll.webm"
			if strings.HasSuffix(r.URL.Query().Get("filename"), ".json") {
				path += ".json"
				var metadata struct {
					DurationMS json.RawMessage `json:"duration_ms"`
				}
				json.Unmarshal(readBody(r), &metadata)
				mu.Lock()
				duration = string(metadata.DurationMS)
				mu.Unlock()
			}
			fulfill(w, http.StatusOK, map[string]string{"path": path})
		})
		s.route(chatPattern, func(w http.ResponseWriter, r *http.Request) { acceptChat(w) })
	})
}

func TestE2ERecordingLegacyCompactionRowDoesNotBlockQueue(t *testing.T) {
	e2e(t, `Legacy compaction status rows render without blocking queued messages. The test seeded a conversation at /c/{{SLUG}} whose server data reports one queued message, "queued during compaction", and also an in-progress compaction status row that the old generation-based compaction left behind. The live event stream is cut off.
1. Navigate to /c/{{SLUG}} and wait up to 30s for the message input ([data-testid="message-input"]) to be visible.
2. [data-testid="distill-in-progress"] should contain "Compacting", and [data-testid="queued-badge"] should be visible.
3. Exactly one [data-testid="send-queued-now"] should exist, and [data-testid="cancel-queued"] should be visible.`, func(s *shelley) {
		recording(s)
		s.v("SLUG", s.queuedConversation([]map[string]any{queuedMessage("q-compacting", "queued during compaction", nil)}, true).Slug)
	})
}

func TestE2ERecordingTranscriptionCardsUseQueuedMessageRPCs(t *testing.T) {
	e2e(t, `Transcription cards use the queued-message cancel and retry RPCs. The test seeded a conversation at /c/{{SLUG}} whose server data reports two queued transcriptions: one still working on /tmp/shelley-uploads/working.webm with the context "Keep working draft", and one of /tmp/shelley-uploads/failed.webm that failed with "transcription unavailable", with the context "Keep failed draft". The live event stream is cut off. Requests to cancel or retry a queued message (POST /api/conversation/<id>/cancel-queued?queued_id=<id> and .../retry-queued?queued_id=<id>) succeed without effect. Calls "stopRequest", "retryRequest" and "cancelRequest" return the method of the first request whose URL contains "/cancel-queued?queued_id=q-working", "/retry-queued?queued_id=q-failed" and "/cancel-queued?queued_id=q-failed" respectively ("none" before there is one). The message input is [data-testid="message-input"]. Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait up to 30s for the message input to be visible.
2. Call "stopRequest" should return "none" (nothing sent yet). Click [data-testid="transcription-stop-button"]. Call "stopRequest" should return "POST". The message input should have the value "Keep working draft".
3. Fill the message input with "" (empty it).
4. Call "retryRequest" should return "none". Click [data-testid="transcription-retry-button"]. Call "retryRequest" should return "POST". The message input should have the value "" (empty).
5. Call "cancelRequest" should return "none". Click [data-testid="transcription-cancel-button"]. Call "cancelRequest" should return "POST". The message input should have the value "Keep failed draft".`, func(s *shelley) {
		recording(s)
		c := s.queuedConversation([]map[string]any{
			queuedMessage("q-working", "", map[string]any{
				"kind":          "transcription",
				"state":         "working",
				"transcription": map[string]any{"media_path": "/tmp/shelley-uploads/working.webm", "context": "Keep working draft"},
			}),
			queuedMessage("q-failed", "", map[string]any{
				"kind":          "transcription",
				"state":         "failed",
				"transcription": map[string]any{"media_path": "/tmp/shelley-uploads/failed.webm", "context": "Keep failed draft"},
				"error":         "transcription unavailable",
			}),
		}, false)
		s.v("SLUG", c.Slug)
		for _, rpc := range []string{"cancel-queued", "retry-queued"} {
			s.route("/api/conversation/{id}/"+rpc, func(w http.ResponseWriter, r *http.Request) {
				fulfill(w, http.StatusOK, map[string]any{})
			})
		}
		for call, fragment := range map[string]string{
			"stopRequest":   "/cancel-queued?queued_id=q-working",
			"retryRequest":  "/retry-queued?queued_id=q-failed",
			"cancelRequest": "/cancel-queued?queued_id=q-failed",
		} {
			s.call(call, func() string {
				for _, r := range s.requestLog() {
					if method, uri, _ := strings.Cut(r, " "); strings.Contains(uri, fragment) {
						return method
					}
				}
				return "none"
			})
		}
	})
}

func TestE2ERecordingRetriesFailedDurableAcceptance(t *testing.T) {
	e2e(t, `The original recording is retained, and failed durable acceptance is retried. `+recMocks+`Recording uploads (POST /api/upload/raw) answer with the path /tmp/shelley-uploads/preserved.webm, and call "uploads" counts them. The first message submission (POST /api/conversation/<id>/chat) fails with "transcription unavailable"; later ones are accepted without being sent on. Call "submissions" reports them as "<count> <same|different>": "same" when every one has the same URL and body as the first. `+recIDs+`
1. Navigate to /new. Fill the message input with "Keep this draft". Click the voice button.
2. [data-testid="recording-status"] should have the text exactly "Recording…". Click the stop button.
3. [data-testid="recording-retained"] should become visible, [data-testid="recording-preserved-text"] should have the text exactly "Keep this draft", and no [data-testid="transcription-task"] may exist (count 0).
4. Click the retry button. No recording panel should remain (count 0), and the message input should have the value "" (empty).
5. Call "uploads" must return "1", call "submissions" must return "2 same", and window.__recordingMock.recorderStarts must be 1.`, func(s *shelley) {
		recording(s)
		s.fakeUploads("/tmp/shelley-uploads/preserved.webm")
		var chats requests
		var mu sync.Mutex
		var uris []string
		s.call("submissions", func() string {
			all := chats.all()
			mu.Lock()
			defer mu.Unlock()
			same := "same"
			for i, c := range all {
				if uris[i] != uris[0] || jsonText(object(c.body)) != jsonText(object(all[0].body)) {
					same = "different"
				}
			}
			return strconv.Itoa(len(all)) + " " + same
		})
		s.route(chatPattern, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			uris = append(uris, r.URL.RequestURI())
			mu.Unlock()
			if chats.add(r) == 1 {
				failText(w, http.StatusInternalServerError, "transcription unavailable")
				return
			}
			acceptChat(w)
		})
	})
}

func TestE2ERecordingKeepsMicrophoneWhenScreenSelectionFails(t *testing.T) {
	e2e(t, `The microphone recording is kept when screen selection fails. `+recMocks+`Setting window.__recordingMock.displayError to a message makes screen capture fail with it. Recording uploads (POST /api/upload/raw) answer with the path /tmp/shelley-uploads/kept.webm, and call "uploads" counts them. Message submissions are accepted without being sent on; call "transcription" returns the last one's message and model as JSON ("none" before there is one). `+recIDs+`
1. Navigate to /new and click the voice button. [data-testid="recording-status"] should have the text exactly "Recording…".
2. Eval "window.__recordingMock.displayError = 'Screen sharing was cancelled'".
3. Click [data-testid="recording-screen-button"].
4. [data-testid="recording-error"] should have the text exactly "Screen sharing was cancelled", the recording panel should have data-mode "microphone", and the stop button should be visible.
5. Call "uploads" must return "0".
6. Click the stop button. Call "transcription" should return exactly {"message":"/transcription /tmp/shelley-uploads/kept.webm","model":"predictable"}.
7. The message input should have the value "" (empty), and call "uploads" must return "1".`, func(s *shelley) {
		recording(s)
		s.fakeUploads("/tmp/shelley-uploads/kept.webm")
		s.lastTranscription()
	})
}

func TestE2ERecordingCancelInlineRestoresComposer(t *testing.T) {
	e2e(t, `Cancelling an active inline recording uploads nothing and restores the composer. `+recMocks+`Recording uploads (POST /api/upload/raw) are counted by call "uploads". `+recIDs+`
1. Navigate to /new and click the voice button. [data-testid="recording-status"] should have the text exactly "Recording…".
2. Click the cancel button. No recording panel should remain (count 0).
3. Call "uploads" must return "0", and the message input should have the value "" (empty).
4. window.__recordingMock.stoppedTracks must be greater than 0.`, func(s *shelley) {
		recording(s)
		s.fakeUploads("/tmp/shelley-uploads/unexpected.webm")
	})
}

// The spec's last three tests ran outside its describe block, without its
// beforeEach media mocks, and installed their own.

func TestE2ERecordingAudioWithoutScreenCapture(t *testing.T) {
	e2e(t, `Audio recording stays available without screen capture. The browser can record audio but cannot capture the screen. The page runs on a fake clock that steps drive with window.__clock.runFor(ms). `+recPalette+recIDs+`
1. Navigate to /new. [data-testid="voice-microphone-icon"] should be visible, and no [data-testid="voice-video-icon"] may exist (count 0).
2. Hover the voice button. Run the clock 1000ms (eval "window.__clock.runFor(1000)").
3. No [data-testid="record-menu"] may exist (count 0).
4. Open the recording palette. The palette item "Record audio" should be visible, and there must be no palette item "Record audio and screen" (count 0).`, func(s *shelley) {
		s.mediaMocks(false, true)
		s.initScript(recordingHelpersJS)
		s.clock(time.Time{})
		s.viewport(390, 844)
	})
}

func TestE2ERecordingExplainsMissingTranscription(t *testing.T) {
	e2e(t, `Missing transcription is explained instead of recording. The server has no transcription available. window.__recordingMock.microphoneRequests counts the microphone captures the page requested. `+recShortcuts+recIDs+`
1. Navigate to /new and click the voice button.
2. ".status-error" should contain "Set OPENAI_API_KEY", and no recording panel may exist (count 0).
3. Press the record-audio shortcut. No recording panel may exist (count 0).
4. window.__recordingMock.microphoneRequests must be 0.`, func(s *shelley) {
		s.mediaMocks(true, false)
		s.initScript(recordingHelpersJS)
	})
}

func TestE2ERecordingPaletteHidesActionsWithoutMediaRecorder(t *testing.T) {
	e2e(t, `Recording palette actions are hidden when media recording is unavailable: the page has no MediaRecorder. `+recPalette+`
1. Navigate to /new. No [data-testid="voice-button"] may exist (count 0).
2. Open the recording palette. There must be no palette item "Record audio" and no palette item "Record audio and screen" (both counts 0).`, func(s *shelley) {
		s.mediaMocks(true, true)
		s.initScript(recordingHelpersJS)
		s.initScript(`Reflect.deleteProperty(window, "MediaRecorder");`)
	})
}
