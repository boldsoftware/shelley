package browse

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestPDF(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser pdf test in short mode")
	}

	html := map[string]string{
		// A 4in×2in borderless @page on pure green: the PDF must honor CSS
		// page rules and keep background graphics, which Chrome drops by default.
		"/css": `<!doctype html>
<style>@page { size: 4in 2in; margin: 0 } body { margin: 0; background: rgb(0, 255, 0) }</style>
<p>hello pdf</p>`,
		// No @page rule: Letter portrait with margins, plus a heading outline.
		"/plain": `<!doctype html><h1>Report</h1><h2>Section</h2><p>hello pdf</p>`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, html[r.URL.Path])
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(tools.Close)
	tool := tools.CombinedTool()

	printPDF := func(t *testing.T, page string) (data, streams []byte) {
		t.Helper()
		out := tool.Run(ctx, []byte(fmt.Sprintf(`{"action":"navigate","url":%q}`, server.URL+page)))
		if out.Error != nil {
			if browserUnavailable(out.Error.Error()) {
				t.Skip("Browser automation not available in this environment")
			}
			t.Fatalf("navigate: %v", out.Error)
		}
		out = tool.Run(ctx, []byte(`{"action":"pdf"}`))
		if out.Error != nil {
			t.Fatalf("pdf: %v", out.Error)
		}
		display, ok := out.Display.(map[string]any)
		if !ok || display["type"] != "pdf" {
			t.Fatalf("display = %#v, want type pdf", out.Display)
		}
		path, _ := display["path"].(string)
		if filepath.Dir(path) != PDFDir || filepath.Ext(path) != ".pdf" {
			t.Fatalf("path = %q, want a .pdf in %s", path, PDFDir)
		}
		t.Cleanup(func() { os.Remove(path) })
		if text := out.LLMContent[0].Text; !strings.Contains(text, path) {
			t.Errorf("result %q does not name %s", text, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(data, []byte("%PDF-")) {
			t.Fatalf("%s is not a PDF: %q", path, data[:min(16, len(data))])
		}
		return data, pdfStreams(t, data)
	}
	// Skia clips each page's drawing to its content box, so the clip's origin
	// is the top-left margin in device units.
	clipOrigin := regexp.MustCompile(`(\S+) (\S+) \S+ \S+ re\nW\* n`)
	mediaBoxes := regexp.MustCompile(`/MediaBox \[0 0 (\S+) (\S+)\]`)

	t.Run("css", func(t *testing.T) {
		data, streams := printPDF(t, "/css")
		if boxes := mediaBoxes.FindAllSubmatch(data, -1); len(boxes) != 1 || string(boxes[0][1]) != "288" || string(boxes[0][2]) != "144" {
			t.Errorf("MediaBoxes = %q, want one 288×144 page from the CSS @page size", boxes)
		}
		if m := clipOrigin.FindSubmatch(streams); m == nil || string(m[1]) != "0" || string(m[2]) != "0" {
			t.Errorf("clip origin = %q, want 0 0 from the CSS @page margin", m)
		}
		if !bytes.Contains(streams, []byte("0 1 0 rg")) {
			t.Error("PDF has no green fill; background graphics were not printed")
		}
	})

	t.Run("plain", func(t *testing.T) {
		data, streams := printPDF(t, "/plain")
		if boxes := mediaBoxes.FindAllSubmatch(data, -1); len(boxes) != 1 || string(boxes[0][1]) != "612" || string(boxes[0][2]) != "792" {
			t.Errorf("MediaBoxes = %q, want one Letter portrait page", boxes)
		}
		if m := clipOrigin.FindSubmatch(streams); m == nil || string(m[1]) == "0" || string(m[2]) == "0" {
			t.Errorf("clip origin = %q, want default margins", m)
		}
		for _, want := range []string{"/StructTreeRoot", "/Outlines"} {
			if !bytes.Contains(data, []byte(want)) {
				t.Errorf("PDF has no %s", want)
			}
		}
	})
}

// pdfStreams returns the concatenated inflated content of every
// FlateDecode stream in a PDF, which is where page drawing operators live.
func pdfStreams(t *testing.T, data []byte) []byte {
	t.Helper()
	var all []byte
	for rest := data; ; {
		i := bytes.Index(rest, []byte("stream\n"))
		if i < 0 {
			return all
		}
		rest = rest[i+len("stream\n"):]
		z, err := zlib.NewReader(bytes.NewReader(rest))
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(z)
		all = append(all, b...)
	}
}
