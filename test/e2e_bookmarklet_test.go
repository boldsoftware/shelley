package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Ported from ui/e2e/bookmarklet.spec.ts.

// bookmarkletHostHTML is a page on another origin, http://host.test/,
// where the user clicks the bookmarklet. It counts the clicks that reach its
// h1 in window.__h1Clicks; picking swallows them.
const bookmarkletHostHTML = `<!doctype html><title>Host Page</title>
<main><h1>Host Page</h1><p id="para">The quick brown fox jumps over the lazy dog.</p>
<ul class="list"><li>First item</li><li>Second item</li></ul></main>
<script>document.querySelector("h1").addEventListener("click", () => { window.__h1Clicks = (window.__h1Clicks || 0) + 1; });</script>`

// bookmarkletChat is where the bookmarklet opens Shelley about the host
// page with nothing selected.
var bookmarkletChat = "/new#bookmarklet=" + encodeURIComponent(`{"url":"http://host.test/","title":"Host Page","selection":""}`)

// bookmarkletMessagesJS keeps every bookmarklet message a page receives in
// window.__bookmarkletMessages.
const bookmarkletMessagesJS = `window.__bookmarkletMessages = [];
window.addEventListener("message", (event) => {
  if (event.data?.tag === "shelley-bookmarklet") window.__bookmarkletMessages.push(event.data);
});`

// The composer texts the bookmarklet's chats start with and picks add.
const (
	bookmarkletPage       = "Page: Host Page — http://host.test/\n> The quick brown fox jumps over the lazy dog."
	bookmarkletPickedItem = "\n\nPicked on http://host.test/: `body > main > ul.list > li:nth-of-type(2)`\n```html\n<li>Second item</li>\n```"
	bookmarkletPickedH1   = "\n\nPicked on http://host.test/: `body > main > h1`\n```html\n<h1>Host Page</h1>\n```"
)

// bookmarkletSetup sets up what the spec's openFromBookmarklet did: the
// media mocks, the message log, and the host page, which also keeps the
// bookmarklet a step hands it (POST http://host.test/bookmarklet) and gives
// it back (GET).
func (s *shelley) bookmarkletSetup() {
	s.mediaMocks(true, true)
	s.initScript(bookmarkletMessagesJS)
	s.v("PAGE", bookmarkletPage)
	var mu sync.Mutex
	var href string
	s.host("http://host.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/bookmarklet" && r.Method == "POST":
			b := readBody(r)
			mu.Lock()
			href = string(b)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/bookmarklet":
			mu.Lock()
			defer mu.Unlock()
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, href)
		default:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, bookmarkletHostHTML)
		}
	}))
}

// bookmarkletOpen describes opening Shelley from the bookmarklet, taken
// from the overflow menu or, when standalone, the standalone bookmarklet
// page, as the spec's openFromBookmarklet did.
func bookmarkletOpen(standalone bool) string {
	take := `1. Navigate to /new. 2. Click the button whose accessible name contains "More options". 3. The overflow menu's bookmarklet item is the bookmarklet too: keep its link, which must be a javascript: URL, with eval (window.__href = document.querySelector('[data-testid=bookmarklet-menu-item]')?.getAttribute('href') ?? '').startsWith('javascript:') expecting true. 4. Click [data-testid="bookmarklet-menu-item"], which opens a dialog with the bookmarklet to drag. 5. That dialog's link has the same href: eval document.querySelector('[data-testid=bookmarklet-link]')?.getAttribute('href') === window.__href expecting true. `
	if standalone {
		take = `1. Navigate to /shelley-bookmarklet.html (the standalone bookmarklet page). 2. Keep its bookmarklet link, which must be a javascript: URL: eval (window.__href = document.querySelector('#link')?.getAttribute('href') ?? '').startsWith('javascript:') expecting true. `
	}
	return `The test opens Shelley from the bookmarklet as a user would: it takes the bookmarklet from Shelley, then clicks it on a page on another origin, http://host.test/ (title "Host Page", a main with an h1 "Host Page", a paragraph #para "The quick brown fox jumps over the lazy dog." and a list ul.list of "First item" and "Second item"), with that paragraph selected. The bookmarklet adds a toolbar to the host page, the element "shelley-bookmarklet", whose controls are in its open shadow root: a status span ".status" and the buttons Record, Pick, Shelley and Close. It opens Shelley in a popup, tab "popup", "the Shelley window". "The toolbar status" is the ".status" element in the toolbar: {"within": {"selector": "shelley-bookmarklet"}, "selector": ".status"} (selectors reach into open shadow roots, but a combinator like "shelley-bookmarklet .status" does not cross into one). Pick the toolbar's buttons by role "button" and exact accessible name, also with "within" the toolbar. Opening it: ` + take + `Then hand the link to the host page with eval fetch('http://host.test/bookmarklet', { method: 'POST', body: window.__href }).then((r) => r.ok) expecting true. Navigate tab "host" to http://host.test/. In tab "host", select the paragraph: eval getSelection().selectAllChildren(document.getElementById('para')). In tab "host", click the bookmarklet as if in the bookmarks bar: eval fetch('/bookmarklet').then((r) => r.text()).then((href) => { location.href = href; }). In tab "host", the toolbar status should be visible with exactly the text "Linked". Then perform these steps in order: `
}

// bookmarkletRecording fakes recording uploads and message submissions, as
// the spec's recordWhileClicking did. The upload of each review log
// (*.review.json) is answered with the next of reviewPaths, then with its
// own path. Call "uploads" lists the file names uploaded since call "clear
// uploads", comma-separated ("none" when there are none), with the first
// audio file (*.webm) as AUDIO. Call "sent" counts the messages sent and
// gives the start of the first. Call "review" summarizes the last review log
// uploaded for AUDIO.
func (s *shelley) bookmarkletRecording(reviewPaths ...string) {
	var mu sync.Mutex
	var names []string
	var audio string
	reviews := map[string][]byte{}
	var sent []string
	s.route("POST /api/upload/raw", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("filename")
		body := readBody(r)
		mu.Lock()
		names = append(names, name)
		if audio == "" && strings.HasSuffix(name, ".webm") {
			audio = name
		}
		path := "/tmp/shelley-uploads/" + name
		if strings.HasSuffix(name, ".review.json") {
			reviews[name] = body
			if len(reviewPaths) > 0 {
				path, reviewPaths = reviewPaths[0], reviewPaths[1:]
			}
		}
		mu.Unlock()
		fulfill(w, http.StatusOK, map[string]string{"path": path})
	})
	s.route(chatPattern, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message string `json:"message"`
		}
		json.Unmarshal(readBody(r), &body)
		mu.Lock()
		sent = append(sent, body.Message)
		mu.Unlock()
		acceptChat(w)
	})
	// asAudio writes the first audio file's name as AUDIO.
	asAudio := func(s string) string {
		if audio == "" {
			return s
		}
		return strings.ReplaceAll(s, audio, "AUDIO")
	}
	s.call("uploads", func() string {
		mu.Lock()
		defer mu.Unlock()
		if len(names) == 0 {
			return "none"
		}
		return asAudio(strings.Join(names, ", "))
	})
	s.call("clear uploads", func() string {
		mu.Lock()
		defer mu.Unlock()
		names = nil
		return "cleared"
	})
	s.call("sent", func() string {
		mu.Lock()
		defer mu.Unlock()
		got := fmt.Sprintf("%d sent", len(sent))
		if len(sent) > 0 {
			const want = "/transcription /tmp/shelley-uploads/AUDIO"
			if first := asAudio(sent[0]); strings.HasPrefix(first, want) {
				got += "; the first starts " + want
			} else {
				got += "; the first is " + first
			}
		}
		return got
	})
	s.call("review", func() string {
		mu.Lock()
		defer mu.Unlock()
		return bookmarkletReview(reviews[audio+".review.json"])
	})
}

// bookmarkletReview summarizes a review log as the spec checked it.
func bookmarkletReview(b []byte) string {
	if b == nil {
		return "none"
	}
	var review struct {
		Source     string `json:"source"`
		URL        string `json:"url"`
		Title      string `json:"title"`
		DurationMS int64  `json:"duration_ms"`
		Events     []struct {
			MS    *int64 `json:"ms"`
			Type  string `json:"type"`
			Where string `json:"where"`
			Text  string `json:"text"`
		} `json:"events"`
	}
	if err := json.Unmarshal(b, &review); err != nil {
		return "bad JSON: " + err.Error()
	}
	var clicked, inRange bool
	inRange = true
	for _, e := range review.Events {
		if e.Type == "click" && e.Where == "#para" && strings.Contains(e.Text, "fox") {
			clicked = true
		}
		if e.MS == nil || *e.MS < 0 || *e.MS > review.DurationMS {
			inRange = false
		}
	}
	stopLast := false
	if n := len(review.Events); n > 0 {
		last := review.Events[n-1]
		var raw struct {
			Events []map[string]any `json:"events"`
		}
		json.Unmarshal(b, &raw)
		stopLast = last.Type == "stop" && last.MS != nil && *last.MS == review.DurationMS && len(raw.Events[n-1]) == 2
	}
	return fmt.Sprintf("source=%s url=%s title=%s click-on-para=%t stop-last=%t times-in-range=%t",
		review.Source, review.URL, review.Title, clicked, stopLast, inRange)
}

// The spec's recordWhileClicking, once something has started recording.
const bookmarkletRecordWhileClicking = `In the Shelley window, ".recording-status" should have the attribute data-state exactly "recording". In tab "host", the toolbar status should have exactly the text "Recording page", and the toolbar's "Record" button must be disabled. In the Shelley window, [data-testid="bookmarklet-status"] must have exactly the text "Recording what you do on host.test". In tab "host", click #para. In the Shelley window, the page must hear of that click: eval window.__bookmarkletMessages.some((m) => m.event?.type === 'click') expecting true. In the Shelley window, click [data-testid="recording-stop-button"].`

func TestE2EBookmarkletLinkedChat(t *testing.T) {
	e2e(t, useful(0), `The bookmarklet opens a linked chat about the page. The test itself answers Shelley's uploads and message sends (already in place; steps must not stub anything): call "uploads" lists the files uploaded, with the recording's audio file as AUDIO ("none" before any); call "sent" counts the messages sent and how the first starts; call "review" summarizes the review log uploaded with AUDIO. `+bookmarkletOpen(false)+`1. In the Shelley window, [data-testid="message-input"] must have the value exactly {{PAGE}}. 2. The Shelley window's URL must be exactly /new (assert_url value). 3. In the Shelley window, [data-testid="bookmarklet-status"] must have exactly the text "Linked to host.test". 4. Pick an element into the chat: in tab "host", click the toolbar's "Pick" button, then click the "ul.list li" with has_text "Second item". 5. In the Shelley window, the message input's value must be exactly {{PICKED_ITEM}}. 6. Call "uploads": it must return exactly "none". 7. Record from the page: in tab "host", click the toolbar's "Record" button; then in the Shelley window click the button whose accessible name is exactly "Allow" inside [data-testid="bookmarklet-ask"]. 8. `+bookmarkletRecordWhileClicking+` 9. In the Shelley window, there must then be exactly 0 [data-testid="recording-panel"] elements. 10. In tab "host", the toolbar status should have exactly the text "Linked", and the toolbar's "Record" button must be enabled. 11. In the Shelley window, [data-testid="bookmarklet-status"] must have exactly the text "Linked to host.test". 12. Call "uploads": it must return exactly "AUDIO, AUDIO.review.json". 13. Call "sent": it must return exactly "1 sent; the first starts /transcription /tmp/shelley-uploads/AUDIO". 14. Call "review": it must return exactly "source=web-page url=http://host.test/ title=Host Page click-on-para=true stop-last=true times-in-range=true".`, func(s *shelley) {
		s.bookmarkletSetup()
		s.v("PICKED_ITEM", bookmarkletPage+bookmarkletPickedItem)
		s.bookmarkletRecording()
	})
}

func TestE2EBookmarkletRetryFreshPair(t *testing.T) {
	e2e(t, useful(0), `A retry uploads a fresh pair when the review's name was taken. The test itself answers Shelley's uploads and message sends (already in place; steps must not stub anything), and the first review log upload is answered with another file's path, /tmp/shelley-uploads/taken_0123.review.json, as when the first attempt stored the review but its response was lost. Call "uploads" lists the files uploaded since the last call "clear uploads", with the recording's audio file as AUDIO ("none" before any); call "sent" counts the messages sent and how the first starts; call "review" summarizes the review log uploaded with AUDIO. `+bookmarkletOpen(false)+`1. Call "uploads": it must return exactly "none". 2. In the Shelley window, click [data-testid="voice-button"]. 3. `+bookmarkletRecordWhileClicking+` 4. In the Shelley window, [data-testid="recording-error"] should be visible. 5. Call "sent": it must return exactly "0 sent". 6. Call "uploads": it must return exactly "AUDIO, AUDIO.review.json". 7. Call "clear uploads". 8. In the Shelley window, click [data-testid="recording-retry-button"]. 9. In the Shelley window, there must then be exactly 0 [data-testid="recording-panel"] elements. 10. Call "uploads": it must return exactly "AUDIO, AUDIO.review.json" (the same audio file again, with its review). 11. Call "sent": it must return exactly "1 sent; the first starts /transcription /tmp/shelley-uploads/AUDIO". 12. Call "review": it must return exactly "source=web-page url=http://host.test/ title=Host Page click-on-para=true stop-last=true times-in-range=true".`, func(s *shelley) {
		s.bookmarkletSetup()
		s.bookmarkletRecording("/tmp/shelley-uploads/taken_0123.review.json")
	})
}

func TestE2EBookmarkletPicksFromOpenerOnly(t *testing.T) {
	e2e(t, useful(0), `Shelley takes picks only from its opener, and picking skips the toolbar. `+bookmarkletOpen(false)+`1. In the Shelley window, [data-testid="message-input"] must have the value exactly {{PAGE}}. 2. A pick from Shelley's own origin, not the linked page, is dropped: in the Shelley window, eval window.postMessage({ tag: 'shelley-bookmarklet', type: 'pick', pick: { url: 'https://evil.test/', selector: 'x', html: 'x' } }, '*'). 3. In tab "host", click the toolbar's "Pick" button, then click the page's h1. 4. In the Shelley window, the message input's value must be exactly {{PICKED_H1}} (the h1's pick and nothing from evil.test). 5. Clicking the toolbar while picking works the toolbar: in tab "host", click the toolbar's "Pick" button; it must have the attribute aria-pressed exactly "true". 6. Click it again; its aria-pressed must be exactly "false". 7. Click it once more (picking again), then click the toolbar's "Close" button. 8. In tab "host", there must be exactly 0 "shelley-bookmarklet" elements. 9. In tab "host", click the h1. 10. With picking off, that click reaches the page (picking swallows clicks, so the page has counted none on its h1 before): in tab "host", eval window.__h1Clicks expecting 1. 11. In the Shelley window, the message input's value must still be exactly {{PICKED_H1}}.`, func(s *shelley) {
		s.bookmarkletSetup()
		s.v("PICKED_H1", bookmarkletPage+bookmarkletPickedH1)
	})
}

func TestE2EBookmarkletRecordingPermission(t *testing.T) {
	e2e(t, useful(0), `Shelley starts recordings only for sites the user allowed. Another site, http://evil.test/ (title "Evil"), can open Shelley about host.test too. "The ask" is [data-testid="bookmarklet-ask"] in the Shelley window and "the panel" [data-testid="recording-panel"] there. `+bookmarkletOpen(true)+`1. In tab "host", click the toolbar's "Record" button. 2. The ask's text must contain "Let http://host.test start recordings?". 3. The button whose accessible name is exactly "Allow" inside the ask must be disabled at first, so it can't be slipped under a click meant for the page. 4. There must be exactly 0 panels. 5. Click the ask's button "Not now" (exact name). 6. There must then be exactly 0 asks. 7. In the Shelley window, [data-testid="bookmarklet-status"] must have exactly the text "Linked to host.test". 8. There must be exactly 0 panels. 9. In tab "host", click the toolbar's "Record" button again; in the Shelley window click the ask's "Allow" button. 10. The panel should be visible. 11. In the Shelley window, click [data-testid="recording-cancel-button"]. 12. There must then be exactly 0 panels. 13. Allowed, it records straight away: in tab "host", click the toolbar's "Record" button. 14. The panel should be visible, and there must be exactly 0 asks. 15. In the Shelley window, click [data-testid="recording-cancel-button"]; there must then be exactly 0 panels. 16. Navigate tab "evil" to http://evil.test/. 17. In tab "evil", open Shelley about host.test: eval (window.__opened = window.open('{{ORIGIN}}{{CHAT}}', '_blank', 'popup'), 'opened'). It opens as tab "popup2" ("the forged window"). 18. In the forged window, [data-testid="message-input"] must have the value exactly "Page: Host Page — http://host.test/". 19. In tab "evil", ask the forged window to record, then to pick: eval (() => { const pick = { url: location.href, selector: 'h1', html: '<h1>Evil</h1>' }; window.__opened.postMessage({ tag: 'shelley-bookmarklet', type: 'record' }, '*'); window.__opened.postMessage({ tag: 'shelley-bookmarklet', type: 'pick', pick }, '*'); return 'posted'; })(). 20. Once the pick arrives the forged window has handled (and ignored) the record: in the forged window, eval window.__bookmarkletMessages.some((m) => m.type === 'pick') expecting true. 21. In the forged window, [data-testid="bookmarklet-status"] must have exactly the text "From host.test", and there must be exactly 0 [data-testid="recording-panel"] elements. 22. Close tab "popup2". 23. Not behind a dialog, where the user couldn't see or stop it: in the Shelley window, click the button whose accessible name contains "More options", then click [data-testid="bookmarklet-menu-item"] (which opens the bookmarklet dialog). 24. In tab "host", click the toolbar's "Record" button; then, so a pick follows the record, click the toolbar's "Pick" button and then the page's h1. 25. In the Shelley window, the message input's value must be exactly {{PICKED_H1}}. 26. There must be exactly 0 panels. 27. Forgotten, it asks again: in the Shelley window, [data-testid="bookmarklet-sites"] must have exactly the text "http://host.testRemove" (the site, then its Remove button, with no space between). 28. Click the button whose accessible name is exactly "Remove" inside it. 29. There must then be exactly 0 [data-testid="bookmarklet-sites"] elements. 30. In the Shelley window, press Escape. 31. In tab "host", click the toolbar's "Record" button. 32. The ask should be visible. 33. There must be exactly 0 panels.`, func(s *shelley) {
		s.bookmarkletSetup()
		s.v("PICKED_H1", bookmarkletPage+bookmarkletPickedH1)
		s.v("CHAT", bookmarkletChat)
		s.host("http://evil.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<title>Evil</title>")
		}))
		s.start()
		s.v("ORIGIN", strings.TrimSuffix(s.url, "/"))
	})
}

func TestE2EBookmarkletFramedShelleyIgnoresChats(t *testing.T) {
	e2e(t, useful(0), `Shelley in a frame ignores the bookmarklet's chats. {{CHAT}} is Shelley's new conversation page with a chat from the bookmarklet about http://host.test/ ("Host Page"), nothing selected. Perform the steps in order. 1. Navigate to /new. 2. Frame it: eval (() => { const frame = document.createElement('iframe'); frame.src = '{{CHAT}}'; document.body.append(frame); return 'framed'; })(). 3. In the frame (steps with "frame": "iframe"), [data-testid="message-input"] should be visible. 4. In the frame, it must have the value exactly "" (empty). 5. Shelley's own window takes the same chat (the frame goes first, as the new conversation composer's text is shared across windows): navigate to about:blank, then to {{CHAT}}. 6. [data-testid="message-input"] must have the value exactly "Page: Host Page — http://host.test/".`, func(s *shelley) {
		s.v("CHAT", bookmarkletChat)
	})
}

func TestE2EBookmarkletChatSavesNoDraftUntilEdited(t *testing.T) {
	e2e(t, useful(0), `A chat from the bookmarklet is not saved as a draft until the user edits it, as any page, even a link, can open one. {{CHAT}} is Shelley's new conversation page with a chat from the bookmarklet about http://host.test/ ("Host Page"), nothing selected. Call "drafts" lists the text of each draft the server keeps, one per line ("none" when there are none). Perform the steps in order. 1. Navigate to {{CHAT}}. 2. [data-testid="message-input"] must have the value exactly "Page: Host Page — http://host.test/". 3. Wait longer than the composer takes to save a draft: eval new Promise((resolve) => setTimeout(() => resolve('waited'), 1500)) expecting "waited". 4. Call "drafts": it must return exactly "none". 5. Click the message input, press End, and type " Why?". 6. Call "drafts" should come to return exactly "Page: Host Page — http://host.test/ Why?".`, func(s *shelley) {
		s.v("CHAT", bookmarkletChat)
		s.bookmarkletDrafts()
	})
}

func TestE2EBookmarkletPicksJoinTheDraft(t *testing.T) {
	e2e(t, useful(0), `Once the user edits a chat from the bookmarklet, what they pick goes into its draft too. Call "drafts" lists the text of each draft the server keeps ("none" when there are none). `+bookmarkletOpen(false)+`1. In the Shelley window, [data-testid="message-input"] must have the value exactly {{PAGE}}. 2. In the Shelley window, click the message input, press Control+End, and type " Why?". 3. In tab "host", click the toolbar's "Pick" button, then click the page's h1. 4. In the Shelley window, the message input's value must be exactly {{EDITED}}. 5. Call "drafts" should come to return exactly {{EDITED}}.`, func(s *shelley) {
		s.bookmarkletSetup()
		s.bookmarkletDrafts()
		s.v("EDITED", bookmarkletPage+" Why?"+bookmarkletPickedH1)
	})
}

// bookmarkletDrafts adds call "drafts": the text of each draft the server
// keeps, one per line, or "none".
func (s *shelley) bookmarkletDrafts() {
	s.call("drafts", func() string {
		var list []struct {
			IsDraft bool   `json:"is_draft"`
			Draft   string `json:"draft"`
		}
		s.api("GET", "/api/conversations", nil, &list)
		var drafts []string
		for _, c := range list {
			if c.IsDraft {
				drafts = append(drafts, c.Draft)
			}
		}
		if len(drafts) == 0 {
			return "none"
		}
		return strings.Join(drafts, "\n")
	})
}
