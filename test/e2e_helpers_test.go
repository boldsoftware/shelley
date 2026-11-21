package test

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/png"
	"strconv"
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
