package test

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
)

// Helpers several ported Playwright specs share (ui/e2e/helpers.ts,
// ui/e2e/mediaMocks.ts).

//go:embed testdata/media-mocks.js
var mediaMocksJS string

// mediaMocks fakes the microphone, screen capture (when screenCapture) and
// MediaRecorder, and says whether the server can transcribe. Pages expose
// counters as window.__recordingMock.
func (s *shelley) mediaMocks(screenCapture, transcription bool) {
	s.patchInit(map[string]any{"transcription_available": transcription})
	s.initScript("window.__MEDIA_MOCK_SCREEN_CAPTURE__ = " + strconv.FormatBool(screenCapture) + ";\n" + mediaMocksJS)
}

// serverLink is a link the server's init data lists.
type serverLink struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// serverLinks has the server present itself as host vm.example with links
// to manage it, its website (when includeVM) and documentation, in that
// order: the VM link is deliberately not the first.
func (s *shelley) serverLinks(includeVM bool) []serverLink {
	links := []serverLink{{"Manage on exe.dev", "https://exe.example/vm/vm"}}
	if includeVM {
		links = append(links, serverLink{"vm.example", "https://vm.example/website"})
	}
	links = append(links, serverLink{"Documentation", "https://docs.example/documentation"})
	s.patchInit(map[string]any{"hostname": "vm.example", "links": links})
	return links
}

// makePNG is a valid RGB PNG of the given size, filled with a gradient.
func makePNG(width, height int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{uint8(x * 7), uint8(y * 3), uint8(x + y), 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// readBody reads r's body, leaving it readable again for forwarding.
func readBody(r *http.Request) []byte {
	b, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b
}

// object decodes a JSON object, nil if b is not one.
func object(b []byte) map[string]any {
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

// gate holds requests until the call that opens it.
type gate chan struct{}

// gate makes a gate that call opens.
func (s *shelley) gate(call string) gate {
	g := make(gate)
	open := sync.OnceFunc(func() { close(g) })
	s.call(call, func() string {
		open()
		return "released"
	})
	return g
}

// wait blocks until g opens, reporting false if r was abandoned first.
func (g gate) wait(r *http.Request) bool {
	select {
	case <-g:
		return true
	case <-r.Context().Done():
		return false
	}
}

// signal is a flag that call reports, "true" or "false".
func (s *shelley) signal(call string) *atomic.Bool {
	var b atomic.Bool
	s.call(call, func() string { return strconv.FormatBool(b.Load()) })
	return &b
}

// counter is a count that call reports.
func (s *shelley) counter(call string) *atomic.Int64 {
	var n atomic.Int64
	s.call(call, func() string { return strconv.FormatInt(n.Load(), 10) })
	return &n
}
