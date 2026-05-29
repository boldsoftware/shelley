package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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

func TestResolveWithinDir(t *testing.T) {
	t.Parallel()
	dir := "/home/u/work"
	tests := []struct {
		name    string
		reqPath string
		wantOK  bool
		wantAbs string
	}{
		{"relative inside", "out/a.png", true, "/home/u/work/out/a.png"},
		{"dot relative inside", "./a.png", true, "/home/u/work/a.png"},
		{"absolute inside", "/home/u/work/a.png", true, "/home/u/work/a.png"},
		{"traversal escape", "../secret.png", false, ""},
		{"absolute escape", "/etc/passwd", false, ""},
		{"deep traversal", "out/../../secret.png", false, ""},
		{"the dir itself", ".", false, ""},
		{"sneaky prefix", "/home/u/work-other/a.png", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			abs, ok := resolveWithinDir(dir, tt.reqPath)
			if ok != tt.wantOK {
				t.Fatalf("resolveWithinDir(%q, %q) ok = %v, want %v (abs=%q)", dir, tt.reqPath, ok, tt.wantOK, abs)
			}
			if ok && abs != tt.wantAbs {
				t.Errorf("resolveWithinDir(%q, %q) abs = %q, want %q", dir, tt.reqPath, abs, tt.wantAbs)
			}
		})
	}
}

func TestServableImageType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		sniffed string
		path    string
		wantCT  string
		wantOK  bool
	}{
		{"image/png", "a.png", "image/png", true},
		{"image/jpeg", "a.jpg", "image/jpeg", true},
		{"text/xml; charset=utf-8", "a.svg", "image/svg+xml", true},
		{"text/plain; charset=utf-8", "a.svg", "image/svg+xml", true},
		{"text/plain; charset=utf-8", "a.txt", "", false},
		{"application/pdf", "a.pdf", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.sniffed+"|"+tt.path, func(t *testing.T) {
			ct, ok := servableImageType(tt.sniffed, tt.path)
			if ok != tt.wantOK || ct != tt.wantCT {
				t.Errorf("servableImageType(%q,%q) = (%q,%v), want (%q,%v)", tt.sniffed, tt.path, ct, ok, tt.wantCT, tt.wantOK)
			}
		})
	}
}

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
	server, database, _ := newTestServer(t)
	conv, err := database.CreateConversation(context.Background(), nil, true, &cwd, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	msg := llm.Message{
		Role:    llm.MessageRoleAssistant,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: msgText}},
	}
	created, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
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

func TestHandleMessageFile_SymlinkEscape(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	cwd := filepath.Join(parent, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	// A secret image outside the workspace.
	if err := os.WriteFile(filepath.Join(parent, "secret.png"), pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the workspace pointing at it.
	link := filepath.Join(cwd, "link.png")
	if err := os.Symlink(filepath.Join(parent, "secret.png"), link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	// The message references the in-workspace symlink name (lexically contained).
	srv, msgID := setupFileServer(t, cwd, "![x](link.png)")

	resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/file?path=link.png")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for symlink escape, got %d", resp.StatusCode)
	}
}

func TestHandleMessageFile_Traversal(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	cwd := filepath.Join(parent, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	// Secret lives outside the working directory.
	if err := os.WriteFile(filepath.Join(parent, "secret.png"), pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	// The message references it (e.g. a malicious model), but it escapes cwd.
	srv, msgID := setupFileServer(t, cwd, "![x](../secret.png)")

	resp, err := http.Get(srv.URL + "/api/message/" + msgID + "/file?path=../secret.png")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for traversal, got %d", resp.StatusCode)
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
