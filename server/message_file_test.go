package server

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func TestMessageReferencesPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		text    string
		reqPath string
		want    bool
	}{
		{"markdown relative", "see ![chart](./out/chart.png) here", "./out/chart.png", true},
		{"markdown absolute", "![x](/tmp/work/a.png)", "/tmp/work/a.png", true},
		{"plain mention on own line", "file: out/a.png", "out/a.png", true},
		{"start of string", "out/a.png is here", "out/a.png", true},
		{"not referenced", "nothing here", "out/a.png", false},
		{"suffix attack", "![x](./out/secret.png)", "./out/cret.png", false},
		{"suffix attack bare", "secret.png", "cret.png", false},
		{"prefix attack", "![x](./out/report-2024.png)", "./out/report-2024", false},
		{"prefix attack bare", "secret.png", "secret", false},
		{"trailing boundary paren", "see (out/a.png) ok", "out/a.png", true},
		{"empty path", "anything", "", false},
		{"quoted html", `<img src="img/a.png">`, "img/a.png", true},
		{"substring midword", "myout/a.png", "out/a.png", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := messageReferencesPath(tt.text, tt.reqPath); got != tt.want {
				t.Errorf("messageReferencesPath(%q, %q) = %v, want %v", tt.text, tt.reqPath, got, tt.want)
			}
		})
	}
}

func TestServableImageType(t *testing.T) {
	t.Parallel()
	svg := []byte(`<?xml version="1.0"?>` + "\n" + `<svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`)
	tests := []struct {
		name   string
		head   []byte
		path   string
		wantCT string
		wantOK bool
	}{
		{"png", pngBytes, "a.png", "image/png", true},
		{"gif", gifBytes, "a.gif", "image/gif", true},
		{"svg", svg, "a.svg", "image/svg+xml", true},
		{"svg after a comment", append([]byte("<!-- "+strings.Repeat("x", 400)+" -->\n"), svg...), "a.svg", "image/svg+xml", true},
		// The extension alone must not be enough: this is the case that let
		// any file be served by naming it .svg.
		{"text named .svg", []byte("TOP SECRET\n"), "a.svg", "", false},
		{"empty named .svg", nil, "a.svg", "", false},
		// Nor is SVG content alone enough, or an HTML page embedding one
		// would be served as an image.
		{"html containing svg", []byte("<html><body><svg></svg></body></html>"), "a.html", "", false},
		{"text", []byte("hello there, this is plain text"), "a.txt", "", false},
		{"pdf", []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n"), "a.pdf", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct, ok := servableImageType(tt.head, tt.path)
			if ok != tt.wantOK || ct != tt.wantCT {
				t.Errorf("servableImageType(%q) = (%q,%v), want (%q,%v)", tt.path, ct, ok, tt.wantCT, tt.wantOK)
			}
		})
	}
}

// gifBytes is a minimal GIF, for checking a second sniffed type.
var gifBytes = []byte("GIF89a\x01\x00\x01\x00\x00\xff\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x00;")

// pngBytes is a minimal valid PNG (1x1) so http.DetectContentType sees image/png.
var pngBytes = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
	0x89,
}

func setupFileServer(t *testing.T, cwd, msgText string) (*httptest.Server, string) {
	t.Helper()
	return setupFileServerContent(t, cwd, llm.Content{Type: llm.ContentTypeText, Text: msgText})
}

func setupFileServerContent(t *testing.T, cwd string, content ...llm.Content) (*httptest.Server, string) {
	t.Helper()
	server, database, _ := newTestServer(t)
	conv, err := database.CreateConversation(t.Context(), nil, true, &cwd, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	msg := llm.Message{
		Role:    llm.MessageRoleAssistant,
		Content: content,
	}
	created, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        msg,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	return httpServer, created.MessageID
}

func TestHandleMessageFile_Success(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "out", "chart.png"), pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, msgID := setupFileServer(t, cwd, "Here is the chart: ![chart](./out/chart.png)")

	resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/file?path=./out/chart.png")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	// The agent may rewrite the file: browsers must revalidate it.
	if cc := resp.Header.Get("Cache-Control"); cc != "private, no-cache" {
		t.Errorf("Cache-Control = %q, want private, no-cache", cc)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, pngBytes) {
		t.Errorf("body mismatch: got %d bytes", len(body))
	}
}

func TestHandleMessageFile_NotReferenced(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "secret.png"), pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, msgID := setupFileServer(t, cwd, "no images here")

	resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/file?path=secret.png")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unreferenced path, got %d", resp.StatusCode)
	}
}

// An image outside the workspace is served. This is the point of the endpoint:
// screenshots live in /tmp/shelley-screenshots, which is outside every
// workspace, so a containment rule rejected the very case this exists for.
// Confining it bought nothing anyway -- the agent reads these files with the
// user's own privileges -- so the reference and the image sniff are the boundary.
func TestHandleMessageFile_ImageOutsideWorkspace(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	cwd := filepath.Join(parent, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "shot.png")
	if err := os.WriteFile(outside, pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, msgID := setupFileServer(t, cwd, "![x]("+outside+")")

	resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/file?path=" + url.QueryEscape(outside))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for a referenced image outside the workspace, got %d", resp.StatusCode)
	}
}

// Reaching outside the workspace is allowed, but only for images. With
// containment gone the sniff is the whole boundary, so exercise it on the real
// files an attacker would actually want rather than only on a temp fixture: a
// model that writes ![pwn](/etc/passwd) satisfies the reference check, and
// must still get nothing.
func TestHandleMessageFile_ReferencedSecretIsRefused(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/etc/passwd", "/etc/hostname", "/etc/shadow"} {
		if _, err := os.Stat(path); err != nil {
			continue // not on this platform
		}
		srv, msgID := setupFileServer(t, t.TempDir(), "Look at this: ![pwn]("+path+")")
		resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/file?path=" + url.QueryEscape(path))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s: served %d bytes of a non-image", path, len(body))
		}
	}
}

// Naming a secret "shot.svg" must not serve it. The extension used to be
// enough on its own, which meant the image requirement could be satisfied by
// choosing a filename -- no protection at all once any absolute path is
// reachable.
func TestHandleMessageFile_TextNamedSVGIsRefused(t *testing.T) {
	t.Parallel()
	secret := filepath.Join(t.TempDir(), "shot.svg")
	if err := os.WriteFile(secret, []byte("BEGIN OPENSSH PRIVATE KEY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, msgID := setupFileServer(t, t.TempDir(), "![x]("+secret+")")

	resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/file?path=" + url.QueryEscape(secret))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403, got %d serving %q", resp.StatusCode, body)
	}
}

// Reading a FIFO blocks until someone writes to it, which would pin this
// request goroutine indefinitely. Since any path is now reachable, the handler
// has to reject non-regular files before it opens them.
func TestHandleMessageFile_FifoIsRefused(t *testing.T) {
	t.Parallel()
	fifo := filepath.Join(t.TempDir(), "pipe.png")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	srv, msgID := setupFileServer(t, t.TempDir(), "![x]("+fifo+")")

	// The bug this guards against is a hang, so bound the request: without the
	// regular-file check this never returns.
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(srv.URL + "/api/message/" + msgID + "/file?path=" + url.QueryEscape(fifo))
	if err != nil {
		t.Fatalf("request did not complete (handler blocked on the fifo?): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for a fifo, got %d", resp.StatusCode)
	}
}

func TestHandleMessageFile_NonImageOutsideWorkspace(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	cwd := filepath.Join(parent, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(parent, "secret.txt")
	if err := os.WriteFile(secret, []byte("root:x:0:0:root:/root:/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, msgID := setupFileServer(t, cwd, "![x](../secret.txt)")

	resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/file?path=../secret.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-image, got %d", resp.StatusCode)
	}
}

func TestHandleMessageFile_NotAnImage(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("just text, definitely not an image at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, msgID := setupFileServer(t, cwd, "![x](notes.txt)")

	resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/file?path=notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for non-image, got %d", resp.StatusCode)
	}
}

func TestHandleMessageFile_MissingFile(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	srv, msgID := setupFileServer(t, cwd, "![x](out/missing.png)")

	resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/file?path=out/missing.png")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for missing file, got %d", resp.StatusCode)
	}
}

func TestHandleMessageFile_SVG(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1"/></svg>`)
	if err := os.WriteFile(filepath.Join(cwd, "diagram.svg"), svg, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, msgID := setupFileServer(t, cwd, "![d](diagram.svg)")

	resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/file?path=diagram.svg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for svg, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", ct)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); csp == "" {
		t.Error("expected a Content-Security-Policy header on svg response")
	}
}

func TestHandleMessageFile_PathRequired(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	srv, msgID := setupFileServer(t, cwd, "hello")
	resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/file")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when path missing, got %d", resp.StatusCode)
	}
}

// The reported bug, end to end: a model answers with a markdown link to a
// screenshot it just took. Those land in browse.ScreenshotDir, which is
// outside every workspace, so this is the exact request that used to 403.
func TestHandleMessageFile_ScreenshotDir(t *testing.T) {
	t.Parallel()
	if err := os.MkdirAll(browse.ScreenshotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shot := filepath.Join(browse.ScreenshotDir, "markdown-link-"+t.Name()+".png")
	if err := os.WriteFile(shot, pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(shot) })

	srv, msgID := setupFileServer(t, t.TempDir(), "Verified against the real product:\n\n![shot]("+shot+")")

	resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/file?path=" + url.QueryEscape(shot))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for a screenshot the model linked, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, pngBytes) {
		t.Errorf("served %d bytes, want the screenshot", len(body))
	}
}

// A conversation with no recorded workspace can still show absolute paths;
// only relative ones need a directory to resolve against.
func TestHandleMessageFile_AbsolutePathWithoutCwd(t *testing.T) {
	t.Parallel()
	shot := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(shot, pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, msgID := setupFileServer(t, "", "![shot]("+shot+")")

	resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/file?path=" + url.QueryEscape(shot))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func getDownload(t *testing.T, srv *httptest.Server, msgID, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/download?path=" + url.QueryEscape(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestHandleMessageDownload_Success(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a b.mp4", "spaced")
	write("a(1).txt", "parens")
	write("auto.txt", "auto")
	write("titled.txt", "titled")
	srv, msgID := setupFileServer(t, "", "Done:\n"+
		"- [angle](<sandbox:"+dir+"/a b.mp4>)\n"+
		"- [encoded](sandbox:"+dir+"/a%20b.mp4)\n"+
		"- [escaped](sandbox:"+dir+"/a\\(1\\).txt)\n"+
		"- <sandbox:"+dir+"/auto.txt>\n"+
		"- [titled](sandbox:"+dir+"/titled.txt \"Title\")\n")

	tests := []struct{ path, name, body string }{
		// path is the destination as marked hands it to the UI: backslash
		// escapes resolved, percent-encoding intact.
		{dir + "/a b.mp4", "a b.mp4", "spaced"},
		{dir + "/a%20b.mp4", "a b.mp4", "spaced"},
		{dir + "/a(1).txt", "a(1).txt", "parens"},
		{dir + "/auto.txt", "auto.txt", "auto"},
		{dir + "/titled.txt", "titled.txt", "titled"},
	}
	for _, tt := range tests {
		resp := getDownload(t, srv, msgID, tt.path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", tt.path, resp.StatusCode)
			continue
		}
		cd := resp.Header.Get("Content-Disposition")
		if disp, params, err := mime.ParseMediaType(cd); err != nil || disp != "attachment" || params["filename"] != tt.name {
			t.Errorf("%s: Content-Disposition = %q, want attachment of %q", tt.path, cd, tt.name)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("%s: Content-Type = %q, want application/octet-stream", tt.path, ct)
		}
		if body, _ := io.ReadAll(resp.Body); string(body) != tt.body {
			t.Errorf("%s: body = %q, want %q", tt.path, body, tt.body)
		}
	}
}

// A link authorizes exactly its own destination, never a file whose name is a
// prefix of it.
func TestHandleMessageDownload_PrefixOfLinkIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"secret.txt", "secret.txt(public)", "a", "a b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv, msgID := setupFileServer(t, "", "[r](sandbox:"+dir+"/secret.txt(public)) [s](<sandbox:"+dir+"/a b.txt>)")

	for _, p := range []string{dir + "/secret.txt", dir + "/a"} {
		if resp := getDownload(t, srv, msgID, p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: expected 404, got %d", p, resp.StatusCode)
		}
	}
	if resp := getDownload(t, srv, msgID, dir+"/secret.txt(public)"); resp.StatusCode != http.StatusOK {
		t.Errorf("full destination: expected 200, got %d", resp.StatusCode)
	}
}

func TestHandleMessageDownload_RelativeToCwd(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, msgID := setupFileServer(t, cwd, "[notes](sandbox:notes.txt)")

	if resp := getDownload(t, srv, msgID, "notes.txt"); resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

// Only a link authorizes a download, as sandbox: or a plain local path. A path
// the message merely mentions (or shows as an image, or puts in a code span) is
// not an offer to download it.
func TestHandleMessageDownload_RequiresLink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"secret.txt", "linked.txt", "plain.txt", "shown.png"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(dir, "secret.txt")
	srv, msgID := setupFileServer(t, dir, "I read "+secret+" and `[x](sandbox:"+secret+")`. ![s](shown.png) "+
		"[linked](sandbox:"+dir+"/linked.txt) [plain](plain.txt) [abs]("+dir+"/plain.txt) [web](https://example.com"+secret+")")

	for p, want := range map[string]int{
		secret:                         http.StatusNotFound,
		"./secret.txt":                 http.StatusNotFound,
		"shown.png":                    http.StatusNotFound,
		"https://example.com" + secret: http.StatusNotFound,
		dir + "/linked.txt":            http.StatusOK,
		"plain.txt":                    http.StatusOK,
		dir + "/plain.txt":             http.StatusOK,
	} {
		if resp := getDownload(t, srv, msgID, p); resp.StatusCode != want {
			t.Errorf("%s: expected %d, got %d", p, want, resp.StatusCode)
		}
	}
}

func TestLinkedFilePath(t *testing.T) {
	t.Parallel()
	for dest, want := range map[string]string{
		"sandbox:/tmp/a.txt":   "/tmp/a.txt",
		"/tmp/a.png":           "/tmp/a.png",
		"out/a.png":            "out/a.png",
		"../a b.txt":           "../a b.txt",
		"":                     "",
		"#section":             "",
		"?q=1":                 "",
		"//host/a.png":         "",
		"/c/abc?message_id=1":  "",
		"/api/message/x/file":  "",
		"https://example.com/": "",
		"mailto:a@b.c":         "",
		"localhost:8000/a":     "",
	} {
		if got, ok := linkedFilePath(dest); got != want || ok != (want != "") {
			t.Errorf("linkedFilePath(%q) = %q, %v; want %q", dest, got, ok, want)
		}
	}
}

func TestHandleMessageDownload_DirectoryIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srv, msgID := setupFileServer(t, "", "[dir](sandbox:"+dir+")")

	if resp := getDownload(t, srv, msgID, dir); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// Download capabilities come from the links the UI renders: each run of
// adjacent text blocks is one GFM document, and an image is not a link.
func TestHandleMessageDownload_MatchesRenderedLinks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"split.txt", "cell.txt", "alt.txt", "extra.txt", "ref.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	text := func(s string) llm.Content { return llm.Content{Type: llm.ContentTypeText, Text: s} }
	srv, msgID := setupFileServerContent(
		t, "",
		text("[x](sandbox:"+dir+"/spl"), text("it.txt)\n\n"+
			"| A | B |\n| - | - |\n| ` | [c](sandbox:"+dir+"/cell.txt) ` |\n\n"+
			"| Visible |\n| --- |\n| nothing | [e](sandbox:"+dir+"/extra.txt) |\n\n"+
			"![alt [a](sandbox:"+dir+"/alt.txt)](/tmp/i.png)\n\n[r][ref]"),
		llm.Content{Type: llm.ContentTypeToolUse, ToolName: "bash", ToolInput: []byte(`{}`)},
		text("\n\n[ref]: sandbox:"+dir+"/ref.txt"),
	)

	for name, want := range map[string]int{
		"split.txt": http.StatusOK,       // one link across adjacent text blocks
		"cell.txt":  http.StatusOK,       // a table cell, not a code span across cells
		"extra.txt": http.StatusNotFound, // GFM drops a row's excess cells
		"alt.txt":   http.StatusNotFound, // inside an image's alt text
		"ref.txt":   http.StatusNotFound, // definition is in another run
	} {
		if resp := getDownload(t, srv, msgID, dir+"/"+name); resp.StatusCode != want {
			t.Errorf("%s: expected %d, got %d", name, want, resp.StatusCode)
		}
	}
}

// A VM conversation's links name files here or on its VM: this host's file
// when it has one (relative to the scratch directory), else the VM's
// (relative to the project's directory there).
func TestMessageFileFromConversationVM(t *testing.T) {
	t.Parallel()
	s, database, id, vmDir, ssh, _ := newConversationVMFixture(t, "true")
	vm, err := s.prepareConversationVMNow(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	_, scratch, err := s.conversationVMTools(t.Context(), conversation)
	if err != nil {
		t.Fatal(err)
	}
	write := func(path string, data []byte) {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(vmDir, "chart.png"), pngBytes)
	write(filepath.Join(vmDir, "report.txt"), []byte("on the VM"))
	write(filepath.Join(vmDir, "both.txt"), []byte("on the VM"))
	write(filepath.Join(scratch, "both.txt"), []byte("here"))
	created, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: id,
		Type:           db.MessageTypeAgent,
		LLMData: llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{{
			Type: llm.ContentTypeText,
			Text: "![c](chart.png) [r](report.txt) [b](both.txt) [m](missing.txt)",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	get := func(endpoint, path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/message/"+created.MessageID+"/"+endpoint+"?path="+url.QueryEscape(path), nil))
		return recorder
	}
	if got := get("file", "chart.png"); got.Code != http.StatusOK || got.Header().Get("Content-Type") != "image/png" || !bytes.Equal(got.Body.Bytes(), pngBytes) {
		t.Errorf("VM image: %d %q %q", got.Code, got.Header().Get("Content-Type"), got.Body.String())
	}
	if got := get("download", "report.txt"); got.Code != http.StatusOK || got.Body.String() != "on the VM" ||
		got.Header().Get("Content-Length") != "9" || !strings.Contains(got.Header().Get("Content-Disposition"), "report.txt") {
		t.Errorf("VM download: %d %v %q", got.Code, got.Header(), got.Body.String())
	}
	if got := get("download", "both.txt"); got.Code != http.StatusOK || got.Body.String() != "here" {
		t.Errorf("file on both machines: %d %q", got.Code, got.Body.String())
	}
	if got := get("download", "missing.txt"); got.Code != http.StatusNotFound {
		t.Errorf("missing file: %d %q", got.Code, got.Body.String())
	}
	if runs := ssh.calls(t, "run"); len(runs) != 3 || runs[0] != vm.Name {
		t.Errorf("ssh runs: %v", runs)
	}

	// An archived conversation's VM is gone: never reached again.
	if _, err := database.ArchiveConversation(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if got := get("download", "report.txt"); got.Code != http.StatusBadGateway || !strings.Contains(got.Body.String(), "archived") {
		t.Errorf("archived conversation: %d %q", got.Code, got.Body.String())
	}
}
