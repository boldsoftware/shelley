package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Ported from ui/e2e/review-recording.spec.ts.

// reviewMicrophoneJS is a microphone and recorder that produce a few bytes
// per second: the recorder emits a Blob "chunk" every timeslice and "last"
// when stopped.
const reviewMicrophoneJS = `(() => {
  class Track extends EventTarget {
    kind = 'audio';
    stop() {}
  }
  class Stream {
    tracks = [new Track()];
    getTracks() { return this.tracks; }
    getAudioTracks() { return this.tracks; }
    getVideoTracks() { return []; }
  }
  class Recorder extends EventTarget {
    static isTypeSupported() { return true; }
    state = 'inactive';
    ondataavailable = null;
    timer = 0;
    constructor(_stream, options) {
      super();
      this.mimeType = options?.mimeType ?? 'audio/webm';
    }
    start(timeslice) {
      this.state = 'recording';
      this.timer = window.setInterval(() => this.emit('chunk'), timeslice);
    }
    stop() {
      window.clearInterval(this.timer);
      this.state = 'inactive';
      this.emit('last');
      setTimeout(() => this.dispatchEvent(new Event('stop')));
    }
    emit(text) {
      const event = new Event('dataavailable');
      Object.defineProperty(event, 'data', { value: new Blob([text], { type: this.mimeType }) });
      this.ondataavailable?.(event);
    }
  }
  Object.defineProperty(window, 'MediaRecorder', { value: Recorder, configurable: true });
  Object.defineProperty(window, 'AudioContext', { value: undefined, configurable: true });
  Object.defineProperty(navigator, 'mediaDevices', {
    configurable: true,
    value: { getUserMedia: async () => new Stream() },
  });
})();`

// review is a narrated review test's fixture: a repository to review, a
// microphone, and what the recording asked of the server.
type review struct {
	s          *shelley
	repo, hash string

	mu       sync.Mutex
	chats    []reviewChat
	drafts   []map[string]any // request bodies of POST /api/conversations/draft
	draftIDs []string         // the conversations they made
}

type reviewChat struct {
	ID   string // the conversation the chat request went to
	Body map[string]any
}

// newReview makes a repository whose HEAD commit renames one line of a file
// taller than the editor, so only part of it is ever on screen, and fakes a
// microphone; transcription says whether the server can transcribe.
// {{QUERY}} is the query that opens the diff viewer on HEAD in it.
func newReview(s *shelley, transcription bool) *review {
	repo := s.mkdir(filepath.Join(s.dir, "repo"))
	s.gitIn(repo, "init")
	for _, kv := range [][2]string{
		{"user.name", "Shelley Test"},
		{"user.email", "test@example.com"},
		{"commit.gpgsign", "false"},
		{"core.hooksPath", "/dev/null"},
	} {
		s.gitIn(repo, "config", kv[0], kv[1])
	}
	lines := make([]string, 80)
	for i := range lines {
		lines[i] = fmt.Sprintf("const value%d = %d;", i+1, i+1)
	}
	app := filepath.Join(repo, "src", "app.ts")
	s.write(app, strings.Join(lines, "\n")+"\n")
	s.gitIn(repo, "add", ".")
	s.gitIn(repo, "commit", "-m", "Base")
	lines[19] = "const renamedValue = 20;"
	s.write(app, strings.Join(lines, "\n")+"\n")
	s.gitIn(repo, "commit", "-am", "Rename value20")
	rv := &review{s: s, repo: repo}
	rv.setHash()
	s.patchInit(map[string]any{"transcription_available": transcription})
	s.initScript(reviewMicrophoneJS)
	return rv
}

func (rv *review) setHash() {
	rv.hash = rv.s.gitIn(rv.repo, "rev-parse", "HEAD")
	rv.s.v("SHORT", rv.hash[:8])
	rv.s.v("QUERY", "diff="+rv.hash+"&cwd="+url.QueryEscape(rv.repo))
}

// conversation starts a conversation in the repository.
func (rv *review) conversation(slug string) convo {
	return rv.s.conversationWith(map[string]any{"message": "Hello", "model": "predictable", "cwd": rv.repo}, slug)
}

// archived starts a conversation in the repository and archives it.
func (rv *review) archived(slug string) convo {
	c := rv.conversation(slug)
	rv.s.api("POST", "/api/conversation/"+c.ID+"/archive", nil, nil)
	return c
}

// Ways to answer the recording's chat request.
const (
	// chatAccept answers 202 as a transcribing server would, recording it.
	chatAccept = iota
	// chatText records it and passes it on as the plain message "Narrated
	// review": the predictable-only test server has no transcription
	// route, so it refuses /transcription, and plain text promotes a draft
	// all the same.
	chatText
	// chatFail records it and answers 503 "unavailable".
	chatFail
	// chatRefuse answers 503 "unavailable" without recording it.
	chatRefuse
)

// routeChats answers the page's chat requests as answer says at the time.
func (rv *review) routeChats(answer func() int) {
	rv.s.route("POST /api/conversation/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mode := answer()
		if mode != chatRefuse {
			rv.mu.Lock()
			rv.chats = append(rv.chats, reviewChat{r.PathValue("id"), body})
			rv.mu.Unlock()
		}
		switch mode {
		case chatAccept:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, "{}")
		case chatText:
			passed := maps.Clone(body)
			passed["message"] = "Narrated review"
			b := []byte(mustJSON(passed))
			r.Body = io.NopCloser(bytes.NewReader(b))
			r.ContentLength = int64(len(b))
			r.Header.Set("Content-Length", strconv.Itoa(len(b)))
			rv.s.pass.ServeHTTP(w, r)
		default:
			unavailable(w)
		}
	})
}

func unavailable(w http.ResponseWriter) {
	w.WriteHeader(http.StatusServiceUnavailable)
	io.WriteString(w, "unavailable")
}

// routeDrafts records the drafts the page makes.
func (rv *review) routeDrafts() {
	rv.s.route("POST /api/conversations/draft", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var body map[string]any
		json.Unmarshal(b, &body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		resp, err := rv.s.forward(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		var created struct {
			ID string `json:"conversation_id"`
		}
		json.Unmarshal(out, &created)
		rv.mu.Lock()
		rv.drafts = append(rv.drafts, body)
		rv.draftIDs = append(rv.draftIDs, created.ID)
		rv.mu.Unlock()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		w.Write(out)
	})
}

// failUploads has the server refuse audio uploads until call "accept
// uploads".
func (rv *review) failUploads() {
	var accept atomic.Bool
	rv.s.route("POST /api/upload/raw", func(w http.ResponseWriter, r *http.Request) {
		if !accept.Load() {
			unavailable(w)
			return
		}
		rv.s.pass.ServeHTTP(w, r)
	})
	rv.s.call("accept uploads", func() string {
		accept.Store(true)
		return "accepted"
	})
}

// uploads counts the page's audio and review uploads.
func (rv *review) uploads() int {
	n := 0
	for _, req := range rv.s.requestLog() {
		if strings.HasPrefix(req, "POST /api/upload/raw") {
			n++
		}
	}
	return n
}

func (rv *review) recorded() ([]reviewChat, []map[string]any, []string) {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	return slices.Clone(rv.chats), slices.Clone(rv.drafts), slices.Clone(rv.draftIDs)
}

// callChats has call "chats" list the conversations chat requests went to,
// as JSON paths.
func (rv *review) callChats() {
	rv.s.call("chats", func() string {
		chats, _, _ := rv.recorded()
		paths := []string{}
		for _, c := range chats {
			paths = append(paths, "/api/conversation/"+c.ID+"/chat")
		}
		return mustJSON(paths)
	})
}

// callDrafts has call "drafts" count the drafts the page made, making
// {{DRAFT_ID}} the first one.
func (rv *review) callDrafts() {
	rv.s.call("drafts", func() string {
		_, _, ids := rv.recorded()
		if len(ids) > 0 {
			rv.s.v("DRAFT_ID", ids[0])
		}
		return strconv.Itoa(len(ids))
	})
}

// started is what the server says of the conversation id.
func (rv *review) started(id string) map[string]any {
	var got map[string]any
	rv.s.api("GET", "/api/conversation-by-slug/"+id, nil, &got)
	return got
}

// sentReview is the review a chat request handed over: its audio file's
// contents and the review JSON beside it.
type sentReview struct {
	Audio    string
	Version  any              `json:"version"`
	Source   any              `json:"source"`
	MimeType any              `json:"mime_type"`
	Events   []map[string]any `json:"events"`
}

var transcriptionMessage = regexp.MustCompile(`^/transcription (/.*review-.*\.webm)$`)

// sentReview reads the review chat hands over.
func readSentReview(chat reviewChat) (*sentReview, error) {
	message, _ := chat.Body["message"].(string)
	m := transcriptionMessage.FindStringSubmatch(message)
	if m == nil {
		return nil, fmt.Errorf("chat message %q does not name a review-*.webm", message)
	}
	audio, err := os.ReadFile(m[1])
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(m[1] + ".review.json")
	if err != nil {
		return nil, err
	}
	r := &sentReview{Audio: string(audio)}
	if err := json.Unmarshal(b, r); err != nil {
		return nil, err
	}
	return r, nil
}

// like reports whether got has every field of want: a func(any) bool tests
// the value; anything else must equal it, as JSON.
func like(got, want map[string]any) bool {
	for k, w := range want {
		v, ok := got[k]
		if !ok {
			return false
		}
		if f, isFunc := w.(func(any) bool); isFunc {
			if !f(v) {
				return false
			}
		} else if mustJSON(v) != mustJSON(w) {
			return false
		}
	}
	return true
}

// problems collects failed checks; a call returns "ok" when there are none.
type problems []string

func (p *problems) check(ok bool, format string, args ...any) {
	if !ok {
		*p = append(*p, fmt.Sprintf(format, args...))
	}
}

func (p problems) result() string {
	if len(p) == 0 {
		return "ok"
	}
	return strings.Join(p, "; ")
}

// describeWant is want as JSON, its matchers shown as "<matcher>".
func describeWant(want map[string]any) string {
	shown := map[string]any{}
	for k, v := range want {
		if _, isFunc := v.(func(any) bool); isFunc {
			v = "<matcher>"
		}
		shown[k] = v
	}
	return mustJSON(shown)
}

// containsLike reports whether some event is like want.
func containsLike(events []map[string]any, want map[string]any) bool {
	return slices.ContainsFunc(events, func(e map[string]any) bool { return like(e, want) })
}

func matching(re string) func(any) bool {
	r := regexp.MustCompile(re)
	return func(v any) bool {
		s, ok := v.(string)
		return ok && r.MatchString(s)
	}
}

// Terms the descriptions share.
const reviewTerms = `In the diff viewer (".diff-viewer-overlay"), "the start button" is data-testid "review-record-start", "the stop button" data-testid "review-record-stop", "the cancel button" data-testid "review-record-cancel", "the status" data-testid "review-recording-status", "where" data-testid "review-recording-where" (its textContent joins its crumbs with no separator) and "said" data-testid "review-recording-said"; "the tooltip" is ".p-tooltip". `

const reviewOpenDiffs = `"Open the diffs" means: wait (up to 30s) for the message input (data-testid "message-input") to be visible, click the "Diffs" button in ".chat-workspace-actions" (if it is not visible, as on a phone, click the button "More options" instead and then the button whose name starts with "Diffs" in ".chat-overflow-popover"), and wait for the commit message. `

const reviewCommitMessage = `"Wait for the commit message" means: wait (up to 30s) until a ".diff-viewer-overlay .editor.modified .view-line" containing "Rename value20" is visible; the viewer opens on the repository's HEAD commit {{SHORT}}, which renames line 20 of src/app.ts to "const renamedValue = 20;". `

const reviewSent = `"It is sent" means data-testid "review-recording-sent" becomes visible (wait up to 30s: sending takes several requests). `

const reviewTick = `"Wait for the clock to start" means: wait until the time element of the stop button no longer reads "00:00" (eval document.querySelector("[data-testid=review-record-stop] time")?.textContent !== "00:00", expect true). `

const reviewQuery = `{{QUERY}} is the query string that opens the diff viewer on that commit in the repository. `

const reviewContentTop = `"Remember where the content starts" means an eval with no expectation that sets window.__contentTop = document.querySelector(".diff-viewer-overlay .diff-viewer-content").getBoundingClientRect().y; "the content has not moved" means eval document.querySelector(".diff-viewer-overlay .diff-viewer-content").getBoundingClientRect().y === window.__contentTop is true. `

const reviewSelectLine = `"Select across the line" means: hover the line at offset x 2, y 8 from its top-left, mouse down, hover it at x 160, y 8, mouse up. `

func TestE2EReviewRecordingCapturesAndHandsOver(t *testing.T) {
	e2e(t, `Narrated review recording captures what was pointed at and selected, then hands it to the conversation. `+reviewTerms+reviewOpenDiffs+reviewCommitMessage+reviewSent+reviewContentTop+reviewSelectLine+`The renamed line is ".diff-viewer-overlay .editor.modified .view-line" containing "renamedValue". To match where's text against a regular expression, eval the regex's test() on its textContent, expecting true. Perform the steps in order.
1. Navigate to /c/review and open the diffs. Remember where the content starts.
2. Click the start button. The stop button should become visible, and where's text should be exactly "commit message {{SHORT}} (new 1)".
3. The live status takes over the header instead of adding a strip: the status should be visible inside ".diff-viewer-overlay .diff-viewer-header", and the content has not moved. There should be no data-testid "review-recording-bar" (count 0).
4. The viewer cannot be closed mid-recording: ".diff-viewer-overlay .diff-viewer-close" should have aria-disabled "true". Press Escape; ".diff-viewer-overlay" should still be visible.
5. Controls outside the editor are named by their region and label: hover the toolbar button ".diff-viewer-overlay .diff-viewer-nav-btn[aria-label='Next file (>)']"; where's textContent should match /^Toolbar.*Next file \(>\)"$/.
6. Hover the file tree item ([role="treeitem"] in the overlay) containing "app.ts"; where's textContent should match /^Changed files.*"app\.ts"$/. Click that tree item; the renamed line should become visible.
7. In the editor, the line under the pointer is what counts: hover the renamed line; where's textContent should match /^Files.*src\/app\.ts new 20$/ and said's text should be exactly "const renamedValue = 20;".
8. Select across the renamed line. The status should contain "Selected", where's textContent should match /^Files.*src\/app\.ts new 20$/, and there should be no said element (count 0).
9. Click the stop button. It is sent. The close button's aria-disabled should no longer be "true". Once sent, the header is back: the status should be gone (count 0), and ".diff-viewer-overlay .diff-viewer-header-title" should have no inert attribute.
10. Call "check review": it should return exactly "ok". It checks on disk, as the spec did, that exactly one chat message handed over the recording ("/transcription <audio path>"), that the audio holds the recorder's bytes, and that the review JSON beside it records the view, the pointer on the toolbar button, the file tree item and line 20, the selection, what was on screen, and the file pick as the only click.`, func(s *shelley) {
		rv := newReview(s, true)
		rv.conversation("review")
		rv.routeChats(func() int { return chatAccept })
		s.initScript(`localStorage.setItem('diff-viewer-layout', 'sidebar');`)
		s.viewport(1500, 900)
		s.call("check review", func() string {
			chats, _, _ := rv.recorded()
			if len(chats) != 1 {
				return fmt.Sprintf("%d chat requests", len(chats))
			}
			r, err := readSentReview(chats[0])
			if err != nil {
				return err.Error()
			}
			var p problems
			p.check(regexp.MustCompile(`^(chunk)*last$`).MatchString(r.Audio), "audio is %q", r.Audio)
			p.check(like(map[string]any{"version": r.Version, "source": r.Source, "mime_type": r.MimeType},
				map[string]any{"version": 1, "source": "diff-viewer", "mime_type": "audio/webm;codecs=opus"}),
				"review is version %v from %v in %v", r.Version, r.Source, r.MimeType)
			events := r.Events
			if len(events) == 0 {
				return "no events"
			}
			p.check(slices.IsSortedFunc(events, func(a, b map[string]any) int {
				return int(a["ms"].(float64) - b["ms"].(float64))
			}), "events are out of order")
			short := rv.hash[:8]
			commit := short + " Rename value20"
			p.check(like(events[0], map[string]any{
				"ms": 0, "type": "view", "where": "Files · " + commit + " · commit message " + short,
				"mode": "files", "commit": rv.hash,
			}), "first event is %s", mustJSON(events[0]))
			p.check(events[len(events)-1]["type"] == "stop", "last event is %s", mustJSON(events[len(events)-1]))
			for _, want := range []map[string]any{
				{"type": "pointer", "where": `Toolbar › "Next file (>)"`},
				{"type": "pointer", "where": `Changed files › "app.ts"`},
				{
					"type": "view", "where": "Files · " + commit + " · src/app.ts", "mode": "files",
					"commit": rv.hash, "file": "src/app.ts",
				},
				{
					"type": "pointer", "where": "Files › src/app.ts new 20", "file": "src/app.ts",
					"side": "new", "line": 20, "text": "const renamedValue = 20;",
				},
				{
					"type": "selection", "where": "Files › src/app.ts new 20", "file": "src/app.ts",
					"side": "new", "line": 20, "end_line": 20, "text": matching("renamed"),
				},
				{"type": "on_screen", "items": func(v any) bool {
					items, ok := v.([]any)
					return ok && len(items) == 1 && matching(`^src/app\.ts \(new 1–\d+, old 1–\d+\)$`)(items[0])
				}},
			} {
				p.check(containsLike(events, want), "no event like %s in %s", describeWant(want), mustJSON(events))
			}
			// The file pick is the only click: stopping is not part of the review.
			var clicks []map[string]any
			for _, e := range events {
				if e["type"] == "click" {
					clicks = append(clicks, e)
				}
			}
			p.check(len(clicks) == 1 && clicks[0]["where"] == `Changed files › "app.ts"`, "clicks are %s", mustJSON(clicks))
			return p.result()
		})
	})
}

func TestE2EReviewRecordingBinaryViews(t *testing.T) {
	e2e(t, `Binary views do not reuse the previous text selection or visible lines. `+reviewTerms+reviewOpenDiffs+reviewCommitMessage+reviewSent+reviewSelectLine+`The commit also adds a binary file, opaque.bin. The renamed line is ".diff-viewer-overlay .editor.modified .view-line" containing "renamedValue". Perform the steps in order.
1. Navigate to /c/review and open the diffs.
2. Click the start button, then click the file tree item ([role="treeitem"] in the overlay) containing "app.ts"; the renamed line should become visible.
3. Select across the renamed line. The status should contain "Selected".
4. Click the file tree item containing "opaque.bin"; data-testid "binary-file-panel" should become visible.
5. Hover the status. It should no longer contain "Selected" (eval that its textContent does not include "Selected", expect true), and where's text should be exactly "opaque.bin".
6. Click the stop button. It is sent.
7. Call "check review": it should return exactly "ok". It checks on disk that exactly one chat message handed over the recording, and that its review JSON has no selection event for opaque.bin but does have an on_screen event whose items are exactly ["opaque.bin"].`, func(s *shelley) {
		rv := newReview(s, true)
		s.write(filepath.Join(rv.repo, "opaque.bin"), string([]byte{0, 1, 255}))
		s.gitIn(rv.repo, "add", "opaque.bin")
		s.gitIn(rv.repo, "commit", "--amend", "--no-edit")
		rv.setHash()
		rv.conversation("review")
		rv.routeChats(func() int { return chatAccept })
		s.initScript(`localStorage.setItem('diff-viewer-layout', 'sidebar');`)
		s.viewport(1500, 900)
		s.call("check review", func() string {
			chats, _, _ := rv.recorded()
			if len(chats) != 1 {
				return fmt.Sprintf("%d chat requests", len(chats))
			}
			r, err := readSentReview(chats[0])
			if err != nil {
				return err.Error()
			}
			var p problems
			p.check(!containsLike(r.Events, map[string]any{"type": "selection", "file": "opaque.bin"}), "a selection in opaque.bin")
			p.check(containsLike(r.Events, map[string]any{"type": "on_screen", "items": []string{"opaque.bin"}}), "opaque.bin was never alone on screen")
			return p.result()
		})
	})
}

func TestE2EReviewRecordingNeedsTranscription(t *testing.T) {
	e2e(t, `Offers no narrated review without transcription: the server says it cannot transcribe. `+reviewOpenDiffs+reviewCommitMessage+`Navigate to /c/review and open the diffs. Then there should be no start button: data-testid "review-record-start" count exactly 0.`, func(s *shelley) {
		rv := newReview(s, false)
		rv.conversation("review")
		s.viewport(1500, 900)
	})
}

func TestE2EReviewRecordingCancel(t *testing.T) {
	e2e(t, `Cancelling throws the recording away. `+reviewTerms+reviewOpenDiffs+reviewCommitMessage+`"The close button" is ".diff-viewer-overlay .diff-viewer-close". "Wait for the clock to tick" means: in an eval with no expectation set window.__tick = document.querySelector("[data-testid=review-record-stop] time").textContent, then wait until that text differs (eval document.querySelector("[data-testid=review-record-stop] time").textContent !== window.__tick, expect true). Perform the steps in order.
1. Navigate to /c/review and open the diffs.
2. Click the start button; the stop button should become visible.
3. Locked controls look it: the close button should have aria-disabled "true", computed opacity exactly "0.35" and computed cursor exactly "not-allowed".
4. Locked controls say why in a tooltip that outlasts the running clock's re-renders: hover the close button; the tooltip's text should be exactly "Stop recording to close". Wait for the clock to tick; the tooltip's text should still be exactly "Stop recording to close".
5. Hover the cancel button; the tooltip's text should be exactly "Stop and discard the recording". Wait for the clock to tick; it should still be exactly "Stop and discard the recording".
6. The first click only asks, and a double-click is not an answer: double-click the cancel button (one click step with count 2). The cancel button's text should be exactly "Discard?", and the stop button should still be visible.
7. A second, separate click throws it away: sleep 500ms (a confirming click within 400ms of the first counts as part of the double-click), then click the cancel button once. The start button should become visible.
8. The start button should be enabled and focused (document.activeElement is it). The status and data-testid "review-recording-bar" should be gone (count 0 each). The close button's aria-disabled should no longer be "true", and its computed opacity should be exactly "1".
9. Call "requests" should return exactly {"uploads":0,"chats":0}: nothing was uploaded or handed to the conversation.
10. Nothing was left behind to offer as unsent: navigate to /c/review again (a reload) and open the diffs. The start button should be visible, and there should be no review-recording-bar (count 0).`, func(s *shelley) {
		rv := newReview(s, true)
		rv.conversation("review")
		rv.routeChats(func() int { return chatAccept })
		s.viewport(1500, 900)
		s.call("requests", func() string {
			chats, _, _ := rv.recorded()
			return fmt.Sprintf(`{"uploads":%d,"chats":%d}`, rv.uploads(), len(chats))
		})
	})
}

func TestE2EReviewRecordingMobileHeader(t *testing.T) {
	e2e(t, `Shows the live status in the mobile header. `+reviewTerms+reviewOpenDiffs+reviewCommitMessage+reviewContentTop+`Perform the steps in order.
1. Navigate to /c/review and open the diffs (this phone hides the toolbar's buttons: click button[aria-label="More options"], then ".chat-overflow-popover button[aria-label^='Diffs']"). Remember where the content starts.
2. Click the start button. where's text should be exactly "commit message {{SHORT}} (new 1)".
3. The status should be visible inside ".diff-viewer-overlay .diff-viewer-header", and the content has not moved.
4. The pickers are covered rather than squeezed, and cannot be reached: ".diff-viewer-overlay .diff-viewer-mobile-selectors" should have the inert attribute with value exactly "" (eval getAttribute("inert") === "", expect true).`, func(s *shelley) {
		rv := newReview(s, true)
		rv.conversation("review")
		s.viewport(390, 844)
	})
}

func TestE2EReviewRecordingOutsideConversation(t *testing.T) {
	e2e(t, `Outside a conversation, a recording starts one of its own. `+reviewTerms+reviewCommitMessage+reviewQuery+reviewSent+reviewTick+`The server refuses audio uploads (/api/upload/raw answers 503 "unavailable") until call "accept uploads". Perform the steps in order.
1. Navigate to /new?{{QUERY}} and wait for the commit message.
2. Hover the start button; the tooltip's text should be exactly "Record a narrated review for a new conversation".
3. Click the start button. Hover the stop button; the tooltip's text should be exactly "Stop and send to a new conversation".
4. Wait for the clock to start, then click the stop button.
5. data-testid "review-recording-error" should be visible, and data-testid "review-recording-pending" should contain "for a new conversation".
6. Nothing is made for a recording that hasn't been accepted: call "drafts" should return exactly "0", and the page's URL should end with "/new" (eval location.href.endsWith("/new"), expect true).
7. After a reload the page's draft is out of the picture, and the recording gets a conversation of its own, set up as the page was at start: call "accept uploads", navigate to /new?{{QUERY}} again, and click data-testid "review-recording-send". It is sent.
8. Call "check send" should return exactly "ok": it checks that exactly one draft was made, with an empty draft text, the repository as cwd and a model; that exactly one chat request went to it, handing over "/transcription <audio path>" with the repository as cwd and the draft's model; and that the server now has it as a conversation that is not a draft, in the repository. It makes {{NEW_ID}} that conversation's id.
9. Sent, the page moves to the new conversation behind the viewer (its URL waits for its title, so check the drawer): ".conversation-item[data-conversation-id='{{NEW_ID}}']" should have class "active" (eval classList.contains("active"), expect true).
10. Hover the start button; the tooltip's text should be exactly "Record a narrated review".`, func(s *shelley) {
		rv := newReview(s, true)
		rv.routeDrafts()
		rv.routeChats(func() int { return chatText })
		rv.failUploads()
		rv.callDrafts()
		s.viewport(1500, 900)
		s.call("check send", func() string {
			chats, drafts, ids := rv.recorded()
			if len(drafts) != 1 || len(chats) != 1 {
				return fmt.Sprintf("%d drafts, %d chat requests", len(drafts), len(chats))
			}
			var p problems
			_, isString := drafts[0]["model"].(string)
			p.check(isString && like(drafts[0], map[string]any{"draft": "", "cwd": rv.repo}), "draft request %s", mustJSON(drafts[0]))
			p.check(chats[0].ID == ids[0], "chat went to %s, not the draft %s", chats[0].ID, ids[0])
			p.check(like(chats[0].Body, map[string]any{
				"message": matching(transcriptionMessage.String()), "cwd": rv.repo, "model": drafts[0]["model"],
			}), "chat request %s", mustJSON(chats[0].Body))
			p.check(like(rv.started(chats[0].ID), map[string]any{"is_draft": false, "cwd": rv.repo}), "conversation %s", mustJSON(rv.started(chats[0].ID)))
			s.v("NEW_ID", chats[0].ID)
			return p.result()
		})
	})
}

const reviewComment = `"Comment on the commit message" means: click the commit message's line (".diff-viewer-overlay .editor.modified .view-line" containing "Rename value20"), fill ".diff-viewer-comment-input" with "Keep the old name.", and click the button named exactly "Add Comment". `

func TestE2EReviewRecordingCommentSharesConversation(t *testing.T) {
	e2e(t, `A comment made while recording on the new page shares the recording's conversation. `+reviewTerms+reviewCommitMessage+reviewQuery+reviewSent+reviewTick+reviewComment+`The server answers the recording's chat requests with 503 "unavailable" until call "accept chats". Call "drafts" counts the drafts the page made (POST /api/conversations/draft) and makes {{DRAFT_ID}} the first one's id; call "chats" returns the JSON list of the paths chat requests went to. To match the message input's (data-testid "message-input") value against a regular expression, eval the regex's test() on it, expecting true. Perform the steps in order.
1. Navigate to /new?{{QUERY}} and wait for the commit message.
2. Click the start button; the stop button should become visible.
3. Comment on the commit message.
4. The comment lands in the composer, which saves it as the page's draft: the message input's value should match /Keep the old name\.\n*$/. Call "drafts" should return exactly "1". The page's URL should then end with exactly "/c/{{DRAFT_ID}}" (eval location.href.endsWith("/c/{{DRAFT_ID}}"), expect true).
5. Wait for the clock to start, then click the stop button. data-testid "review-recording-error" should contain "unavailable".
6. Call "accept chats", then click data-testid "review-recording-send". It is sent.
7. Both attempts went to the page's draft, and no other was made: call "chats" should return exactly ["/api/conversation/{{DRAFT_ID}}/chat","/api/conversation/{{DRAFT_ID}}/chat"], and call "drafts" still exactly "1".
8. ".conversation-item[data-conversation-id='{{DRAFT_ID}}']" should have class "active" (eval classList.contains("active"), expect true), and the message input's value should still match /Keep the old name\.\n*$/.`, func(s *shelley) {
		rv := newReview(s, true)
		rv.routeDrafts()
		var accept atomic.Bool
		rv.routeChats(func() int {
			if accept.Load() {
				return chatText
			}
			return chatFail
		})
		s.call("accept chats", func() string {
			accept.Store(true)
			return "accepted"
		})
		rv.callDrafts()
		rv.callChats()
		s.viewport(1500, 900)
	})
}

func TestE2EReviewRecordingReloadKeepsCommentDraft(t *testing.T) {
	e2e(t, `A reload mid-recording still sends it to the draft a comment made. `+reviewTerms+reviewCommitMessage+reviewQuery+reviewSent+reviewComment+`Call "drafts" counts the drafts the page made (POST /api/conversations/draft) and makes {{DRAFT_ID}} the first one's id; call "chats" returns the JSON list of the paths chat requests went to. Perform the steps in order.
1. Navigate to /new?{{QUERY}} and wait for the commit message.
2. Click the start button; the stop button should become visible.
3. Comment on the commit message. The page's URL should come to end with "/c/" and an id, nothing after it (eval /\/c\/[^/?]+$/.test(location.href), expect true). Call "drafts" should return exactly "1".
4. The saved recording names the draft while it is still recording: this eval should come to return exactly ["{{DRAFT_ID}}"]:
new Promise((resolve, reject) => { const open = indexedDB.open("shelley-review-recordings"); open.onerror = () => reject(open.error); open.onsuccess = () => { const all = open.result.transaction("sessions").objectStore("sessions").getAll(); all.onsuccess = () => { open.result.close(); resolve(JSON.stringify(all.result.map((session) => session.conversationId))); }; }; })
5. Without stopping, navigate to /c/{{DRAFT_ID}}?{{QUERY}}. data-testid "review-recording-pending" should contain "Unsent recording".
6. Click data-testid "review-recording-send". It is sent.
7. Call "chats" should return exactly ["/api/conversation/{{DRAFT_ID}}/chat"], and call "drafts" still exactly "1".`, func(s *shelley) {
		rv := newReview(s, true)
		rv.routeDrafts()
		rv.routeChats(func() int { return chatText })
		rv.callDrafts()
		rv.callChats()
		s.viewport(1500, 900)
	})
}

func TestE2EReviewRecordingSentFromLiveConversation(t *testing.T) {
	e2e(t, `A recording sent from a live conversation leaves the view there. `+reviewTerms+reviewCommitMessage+reviewQuery+reviewSent+reviewTick+`There is a live conversation /c/live in the repository, whose drawer item is ".conversation-item[data-conversation-id='{{LIVE_ID}}']". The server refuses audio uploads (/api/upload/raw answers 503 "unavailable") until call "accept uploads". Perform the steps in order.
1. Navigate to /new?{{QUERY}} and wait for the commit message.
2. Click the start button, wait for the clock to start, then click the stop button. data-testid "review-recording-error" should be visible.
3. Call "accept uploads", then navigate to /c/live?{{QUERY}} and click data-testid "review-recording-send". It is sent.
4. Call "chats" should return exactly {"chats":1,"to_live":false}: one chat request, to a conversation other than the live one. It makes {{NEW_ID}} that conversation's id.
5. The new conversation shows up, but the view stays where the user is: ".conversation-item[data-conversation-id='{{NEW_ID}}']" should be visible, the live conversation's drawer item should have class "active" (eval classList.contains("active"), expect true), and the page's URL should end with exactly "/c/live" (eval location.href.endsWith("/c/live"), expect true).`, func(s *shelley) {
		rv := newReview(s, true)
		live := rv.conversation("live")
		s.v("LIVE_ID", live.ID)
		rv.routeChats(func() int { return chatText })
		rv.failUploads()
		s.viewport(1500, 900)
		s.call("chats", func() string {
			chats, _, _ := rv.recorded()
			if len(chats) > 0 {
				s.v("NEW_ID", chats[0].ID)
			}
			return fmt.Sprintf(`{"chats":%d,"to_live":%t}`, len(chats), len(chats) > 0 && chats[0].ID == live.ID)
		})
	})
}

func TestE2EReviewRecordingDeletedDraft(t *testing.T) {
	e2e(t, `A recording whose draft was deleted starts another. `+reviewTerms+reviewCommitMessage+reviewQuery+reviewSent+reviewTick+`The server answers the recording's chat requests with 503 "unavailable" until call "accept chats". Perform the steps in order.
1. Navigate to /new?{{QUERY}} and wait for the commit message.
2. Click the start button, wait for the clock to start, then click the stop button. data-testid "review-recording-error" should contain "unavailable".
3. Call "delete draft" should return exactly "deleted": it checks the recording made exactly one draft and deletes it on the server (POST /api/conversation/<id>/delete).
4. One click: the gone draft is noticed and replaced. Call "accept chats", then click data-testid "review-recording-send" once. It is sent.
5. Call "check send" should return exactly "ok": it checks that exactly two drafts were made, that the chat requests went exactly to the deleted draft (the refused attempt) and then the new one, so nothing more went to the deleted draft, and that the server has the new one as a conversation that is not a draft, in the repository. It makes {{NEW_ID}} the new draft's id.
6. ".conversation-item[data-conversation-id='{{NEW_ID}}']" should have class "active" (eval classList.contains("active"), expect true).`, func(s *shelley) {
		rv := newReview(s, true)
		rv.routeDrafts()
		var accept atomic.Bool
		rv.routeChats(func() int {
			if accept.Load() {
				return chatText
			}
			return chatFail
		})
		s.call("accept chats", func() string {
			accept.Store(true)
			return "accepted"
		})
		s.call("delete draft", func() string {
			_, _, ids := rv.recorded()
			if len(ids) != 1 {
				return fmt.Sprintf("%d drafts", len(ids))
			}
			s.api("POST", "/api/conversation/"+ids[0]+"/delete", nil, nil)
			return "deleted"
		})
		s.call("check send", func() string {
			chats, _, ids := rv.recorded()
			if len(ids) != 2 {
				return fmt.Sprintf("%d drafts", len(ids))
			}
			var p problems
			got := make([]string, len(chats))
			for i, c := range chats {
				got[i] = c.ID
			}
			p.check(slices.Equal(got, ids), "chat requests went to %v, drafts are %v", got, ids)
			p.check(like(rv.started(ids[1]), map[string]any{"is_draft": false, "cwd": rv.repo}), "conversation %s", mustJSON(rv.started(ids[1])))
			s.v("NEW_ID", ids[1])
			return p.result()
		})
		s.viewport(1500, 900)
	})
}

func TestE2EReviewRecordingArchivedStartsNew(t *testing.T) {
	e2e(t, `A recording on an archived conversation starts a new one. `+reviewTerms+reviewCommitMessage+reviewQuery+reviewSent+reviewTick+`There is an archived conversation /c/archived in the repository. Perform the steps in order.
1. Navigate to /c/archived?{{QUERY}} and wait for the commit message.
2. Hover the start button; the tooltip's text should be exactly "Record a narrated review for a new conversation".
3. Click the start button, wait for the clock to start, then click the stop button. It is sent.
4. Call "check send" should return exactly "ok": it checks that exactly one chat request was made, to a conversation other than the archived one, which the server has as not a draft, in the repository, and not archived. It makes {{NEW_ID}} that conversation's id.
5. ".conversation-item[data-conversation-id='{{NEW_ID}}']" should have class "active" (eval classList.contains("active"), expect true).`, func(s *shelley) {
		rv := newReview(s, true)
		archived := rv.archived("archived")
		rv.routeChats(func() int { return chatText })
		s.viewport(1500, 900)
		s.call("check send", func() string {
			chats, _, _ := rv.recorded()
			if len(chats) != 1 {
				return fmt.Sprintf("%d chat requests", len(chats))
			}
			id := chats[0].ID
			if id == archived.ID {
				return "the chat went to the archived conversation"
			}
			s.v("NEW_ID", id)
			got := rv.started(id)
			if !like(got, map[string]any{"is_draft": false, "cwd": rv.repo, "archived": false}) {
				return "conversation " + mustJSON(got)
			}
			return "ok"
		})
	})
}

func TestE2EReviewRecordingDraftKeepsText(t *testing.T) {
	e2e(t, `A recording on a draft goes to it after a reload and keeps its text. `+reviewTerms+reviewCommitMessage+reviewQuery+reviewSent+reviewTick+`There is a draft conversation {{DRAFT_ID}} in the repository, with the unsent text "Written elsewhere." and model predictable. The server refuses audio uploads (/api/upload/raw answers 503 "unavailable") until call "accept uploads". Perform the steps in order.
1. Navigate to /c/{{DRAFT_ID}}?{{QUERY}} and wait for the commit message.
2. Click the start button, wait for the clock to start, then click the stop button. data-testid "review-recording-error" should be visible.
3. The draft it began on still takes it after a reload: call "accept uploads", navigate to /c/{{DRAFT_ID}}?{{QUERY}} again, and click data-testid "review-recording-send". It is sent.
4. Call "check send" should return exactly "ok": it checks that exactly one chat request was made, to the draft, with model "predictable" and the repository as cwd, and that the server no longer has the draft as a draft.
5. Its unsent text stays in the composer: click the viewer's close button (".diff-viewer-overlay .diff-viewer-close"). The message input's (data-testid "message-input") value should be exactly "Written elsewhere.".`, func(s *shelley) {
		rv := newReview(s, true)
		var created struct {
			ID string `json:"conversation_id"`
		}
		s.api("POST", "/api/conversations/draft", map[string]any{"draft": "Written elsewhere.", "model": "predictable", "cwd": rv.repo}, &created)
		s.v("DRAFT_ID", created.ID)
		rv.routeChats(func() int { return chatText })
		rv.failUploads()
		s.viewport(1500, 900)
		s.call("check send", func() string {
			chats, _, _ := rv.recorded()
			if len(chats) != 1 {
				return fmt.Sprintf("%d chat requests", len(chats))
			}
			var p problems
			p.check(chats[0].ID == created.ID, "the chat went to %s", chats[0].ID)
			p.check(like(chats[0].Body, map[string]any{"model": "predictable", "cwd": rv.repo}), "chat request %s", mustJSON(chats[0].Body))
			p.check(like(rv.started(created.ID), map[string]any{"is_draft": false}), "conversation %s", mustJSON(rv.started(created.ID)))
			return p.result()
		})
	})
}

func TestE2EReviewRecordingDiscardDeletesDraft(t *testing.T) {
	e2e(t, `Discarding a recording deletes the empty draft it made. `+reviewTerms+reviewCommitMessage+reviewQuery+reviewTick+`There is an archived conversation /c/archived in the repository. The server answers the recording's chat requests with 503 "unavailable". Perform the steps in order.
1. Navigate to /c/archived?{{QUERY}} and wait for the commit message.
2. Click the start button, wait for the clock to start, then click the stop button. data-testid "review-recording-error" should be visible.
3. Call "chats" should return exactly "ok": it checks that exactly one chat request was made, to a conversation other than the archived one, which the server has. It makes {{NEW_ID}} that conversation's id.
4. Discard the recording: click data-testid "review-recording-discard"; the first click only arms it, and its text becomes exactly "Discard for good?". Sleep 500ms (a confirming click within 400ms of the first is ignored), then click it again. data-testid "review-recording-bar" should be gone (count 0).
5. Call "status" should come to return exactly "404": the server no longer has conversation {{NEW_ID}}.`, func(s *shelley) {
		rv := newReview(s, true)
		archived := rv.archived("archived")
		rv.routeChats(func() int { return chatFail })
		s.viewport(1500, 900)
		var id string
		s.call("chats", func() string {
			chats, _, _ := rv.recorded()
			if len(chats) != 1 {
				return fmt.Sprintf("%d chat requests", len(chats))
			}
			id = chats[0].ID
			if id == archived.ID {
				return "the chat went to the archived conversation"
			}
			s.v("NEW_ID", id)
			rv.started(id)
			return "ok"
		})
		s.call("status", func() string {
			s.start()
			resp, err := http.Get(s.backend + "/api/conversation-by-slug/" + id)
			s.check(err)
			resp.Body.Close()
			return strconv.Itoa(resp.StatusCode)
		})
	})
}

func TestE2EReviewRecordingKeepsUnsent(t *testing.T) {
	e2e(t, `Keeps an unsent recording across reloads until the conversation accepts it. `+reviewTerms+reviewOpenDiffs+reviewCommitMessage+reviewSent+reviewTick+reviewContentTop+`The server answers the recording's chat requests with 503 "unavailable" until call "accept chats". Call "requests" returns JSON counting the audio and review uploads (POST /api/upload/raw) and the accepted chat requests, and whether every accepted chat handed over a recording ("/transcription <path of a review-*.webm>"). Perform the steps in order.
1. Navigate to /c/review and open the diffs.
2. Click the start button; the stop button should become visible. Wait for the clock to start, then click the stop button.
3. data-testid "review-recording-error" should contain "unavailable", and there should be exactly one data-testid "review-recording-pending". Call "requests" should return exactly {"uploads":2,"chats":0,"transcriptions":true}.
4. Navigate to /c/review again (a reload) and open the diffs. data-testid "review-recording-pending" should contain "Unsent recording".
5. A new recording leaves the unsent one in place, waiting its turn: call "accept chats", remember where the content starts, and click the start button. The status should be visible inside ".diff-viewer-overlay .diff-viewer-header", and the content has not moved. data-testid "review-recording-send" should be disabled.
6. Click the stop button. It is sent. There should still be exactly one review-recording-pending, and call "requests" should return exactly {"uploads":4,"chats":1,"transcriptions":true}.
7. Click data-testid "review-recording-send". review-recording-pending should be gone (count 0). Both files were already on the server, so only the handoff was retried: call "requests" should return exactly {"uploads":4,"chats":2,"transcriptions":true}.
8. Navigate to /c/review again (a reload) and open the diffs. There should be no data-testid "review-recording-bar" (count 0).`, func(s *shelley) {
		rv := newReview(s, true)
		rv.conversation("review")
		var accept atomic.Bool
		rv.routeChats(func() int {
			if accept.Load() {
				return chatAccept
			}
			return chatRefuse
		})
		s.call("accept chats", func() string {
			accept.Store(true)
			return "accepted"
		})
		s.call("requests", func() string {
			chats, _, _ := rv.recorded()
			transcriptions := true
			for _, c := range chats {
				message, _ := c.Body["message"].(string)
				transcriptions = transcriptions && transcriptionMessage.MatchString(message)
			}
			return fmt.Sprintf(`{"uploads":%d,"chats":%d,"transcriptions":%t}`, rv.uploads(), len(chats), transcriptions)
		})
		s.viewport(1500, 900)
	})
}
