package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Ported from ui/e2e/favicon-emoji.spec.ts.

// Whether the server's favicon emoji comes from exe.dev depends on the host
// running the suite, so these tests serve /api/favicon-emoji from a fake
// that behaves like the server; the Go tests cover the real endpoint. The
// picker's emoji data normally comes from a CDN; a few entries stand in for
// it.

// faviconHref is the favicon a server would give for an emoji.
func faviconHref(emoji string) string {
	return "data:image/svg+xml," + encodeURIComponent(`<svg xmlns="http://www.w3.org/2000/svg"><text>`+emoji+`</text></svg>`)
}

// encodeURIComponent escapes s as JavaScript's encodeURIComponent does.
func encodeURIComponent(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// faviconEmojiData stands in for the picker's emoji data from the CDN.
var faviconEmojiData = func() []map[string]any {
	var data []map[string]any
	for _, e := range []struct {
		emoji, annotation string
		order             int
	}{{"🦊", "fox", 3}, {"🐙", "octopus", 3}, {"🌵", "cactus", 4}} {
		data = append(data, map[string]any{
			"annotation": e.annotation,
			"emoji":      e.emoji,
			"group":      3,
			"order":      e.order,
			"shortcodes": []string{e.annotation},
			"tags":       []string{e.annotation},
			"version":    1,
		})
	}
	return data
}()

// faviconServer fakes the favicon emoji endpoint, starting at emoji from
// source, and the CDN's emoji data. Call "puts" lists the emojis the page
// PUT, as a JSON array. The first PUT fails when failFirst; PUTs wait for
// gate when it is not nil. {{OCTOPUS}}, {{FOX}}, {{CACTUS}} and {{TEST_TUBE}}
// are the favicons the fake gives for those emojis.
func (s *shelley) faviconServer(emoji, source string, failFirst bool, gate gate) {
	for name, e := range map[string]string{"OCTOPUS": "🐙", "FOX": "🦊", "CACTUS": "🌵", "TEST_TUBE": "🧪"} {
		s.v(name, faviconHref(e))
	}
	s.host("https://cdn.jsdelivr.net", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"test"`)
		writeJSON(w, faviconEmojiData)
	}))
	var mu sync.Mutex
	puts := []string{}
	current := emoji
	failNext := failFirst
	s.route("/api/favicon-emoji", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			var body struct {
				Emoji string `json:"emoji"`
			}
			if err := json.Unmarshal(readBody(r), &body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			puts = append(puts, body.Emoji)
			mu.Unlock()
			if gate != nil && !gate.wait(r) {
				return
			}
			mu.Lock()
			fail := failNext
			failNext = false
			if !fail {
				current = body.Emoji
			}
			mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, "Failed to set favicon emoji: disk on fire")
				return
			}
		}
		mu.Lock()
		e := current
		mu.Unlock()
		writeJSON(w, map[string]string{"emoji": e, "source": source, "href": faviconHref(e)})
	})
	s.call("puts", func() string {
		mu.Lock()
		defer mu.Unlock()
		return mustJSON(puts)
	})
}

// faviconSetup describes the fixtures and the spec's helpers.
const faviconSetup = `"The picker" is the dialog whose accessible name is "Favicon emoji"; "the favicon" is the element link[rel="icon"]. Call "puts" lists, as a JSON array, the emojis the page has asked the server to save. To "open the picker": press_key "k" with modifiers "Control" (on whatever has focus); fill ".command-palette-input" with "favicon emoji"; click the first ".command-palette-item"; then the picker should be visible. "The search box" is the picker's input[type=search]; it sits inside the emoji-picker element's shadow root, which CSS selectors cannot reach, so pick it as the element with role "combobox" and accessible name exactly "Search" (exact), with "within" the picker. An "option" is an element with role "option" inside the picker. To "type" text means a type step, with no click first: the keys go to whatever has focus. Perform the steps in order. 1. Navigate to /new. 2. [data-testid="message-input"] should be visible. `

func TestE2EFaviconEmojiSearchAndPick(t *testing.T) {
	e2e(t, `Searching for an emoji and clicking it sets Shelley's favicon. The server's favicon emoji is 🐙. `+faviconSetup+`3. Open the picker. 4. Typing searches straight away once the picker has loaded: the search box must have focus. 5. The favicon's href attribute must be exactly {{OCTOPUS}}. 6. Type "fox". 7. Click the option whose accessible name contains "fox". 8. The picker should become hidden. 9. The favicon's href must be exactly {{FOX}}. 10. Call "puts": it must return exactly ["🦊"].`, func(s *shelley) {
		s.faviconServer("🐙", "shelley", false, nil)
	})
}

func TestE2EFaviconEmojiFailedSave(t *testing.T) {
	e2e(t, `A failed save keeps the favicon emoji picker open with the error. The server's favicon emoji is 🐙, and its first save fails with "Failed to set favicon emoji: disk on fire". `+faviconSetup+`3. Open the picker. 4. The search box must have focus. 5. Type "cactus". 6. Click the option whose accessible name contains "cactus". 7. The element with role "alert" inside the picker should have text containing "disk on fire". 8. The favicon's href attribute must be exactly {{OCTOPUS}}. 9. Click the option whose accessible name contains "cactus" again. 10. The picker should become hidden. 11. The favicon's href must be exactly {{CACTUS}}. 12. Open the picker again. 13. The search box must have focus. 14. Press Escape. 15. The picker should become hidden.`, func(s *shelley) {
		s.faviconServer("🐙", "shelley", true, nil)
	})
}

func TestE2EFaviconEmojiExeDevLink(t *testing.T) {
	e2e(t, `An exe.dev favicon emoji links to where to change it. The page runs on host myvm.exe.xyz, and the server's favicon emoji 🧪 comes from exe.dev. `+faviconSetup+`3. Navigate to /new again. 4. Open the picker (no focus check). 5. The favicon's href attribute must be exactly {{TEST_TUBE}}. 6. The link inside the picker whose accessible name is "Change it on exe.dev" must have the href attribute exactly "https://exe.dev/vm/myvm". 7. There must be exactly 0 "emoji-picker" elements inside the picker. 8. Press Escape. 9. The picker should become hidden. 10. Call "puts": it must return exactly [].`, func(s *shelley) {
		s.patchInit(map[string]any{"hostname": "myvm.exe.xyz"})
		s.faviconServer("🧪", "exe.dev", false, nil)
	})
}

func TestE2EFaviconEmojiReopenMidSave(t *testing.T) {
	e2e(t, `Reopening the favicon emoji picker mid-save shows the saved emoji. The server's favicon emoji is 🐙, and it holds every save until call "release". `+faviconSetup+`3. Open the picker. 4. The search box must have focus. 5. Type "cactus". 6. Click the option whose accessible name contains "cactus". 7. An element inside the picker with the text "Saving…" should be visible. 8. Press Escape. 9. The picker should become hidden. 10. The save is still running, and the picker waits for it: open the picker again (no focus check). 11. An element inside the picker with the text "Saving…" should be visible. 12. There must be exactly 0 "emoji-picker" elements inside the picker. 13. Keys typed meanwhile stay out of the message input behind the picker: type "x". 14. [data-testid="message-input"] must have the value exactly "" (empty). 15. Call "release". 16. The search box must then have focus. 17. The favicon's href attribute must be exactly {{CACTUS}}. 18. Call "puts": it must return exactly ["🌵"].`, func(s *shelley) {
		s.faviconServer("🐙", "shelley", false, s.gate("release"))
	})
}
