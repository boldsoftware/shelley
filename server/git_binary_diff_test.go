package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitFileDiffBinaryMetadataAndPresence(t *testing.T) {
	h := NewTestHarness(t)
	repo := binaryDiffRepo(t, map[string][]byte{
		"ordinary.dat": []byte("plain text\n"),
		"notes.txt":    []byte("BM25 ranking notes\n"),
		"binary.txt":   {0, 1, 0xff},
		"empty.bin":    {},
		"deleted.bin":  {0, 2},
	})
	if err := os.Remove(filepath.Join(repo, "deleted.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "added.bin"), []byte{0, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "empty-added.bin"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path     string
		binary   bool
		old, new bool
		wantOld  string
		wantNew  string
	}{
		{"ordinary.dat", false, true, true, "plain text\n", "plain text\n"},
		{"notes.txt", false, true, true, "BM25 ranking notes\n", "BM25 ranking notes\n"},
		{"binary.txt", true, true, true, "", ""},
		{"empty.bin", false, true, true, "", ""},
		{"deleted.bin", true, true, false, "", ""},
		{"added.bin", true, false, true, "", ""},
		{"empty-added.bin", false, false, true, "", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			diff := requestBinaryDiff(t, h, repo, "working", tc.path, "")
			if diff.Binary != tc.binary || diff.OldExists != tc.old || diff.NewExists != tc.new {
				t.Fatalf("metadata = binary:%v oldExists:%v newExists:%v", diff.Binary, diff.OldExists, diff.NewExists)
			}
			if diff.OldContent != tc.wantOld || diff.NewContent != tc.wantNew {
				t.Fatalf("content = %q / %q", diff.OldContent, diff.NewContent)
			}
			if !tc.binary && (diff.OldImage || diff.NewImage || diff.OldAudio || diff.NewAudio) {
				t.Fatalf("ordinary text marked as media: %+v", diff)
			}
		})
	}

	diff := requestBinaryDiff(t, h, repo, "working", "binary.txt", "text=true")
	if !diff.Binary || diff.OldContent != "\x00\x01\ufffd" || diff.NewContent != diff.OldContent {
		t.Fatalf("text=true did not opt into binary data: %+v", diff)
	}
}

func TestGitFileDiffBinaryRefsAndDownloads(t *testing.T) {
	h := NewTestHarness(t)
	path := `nested/odd "name".bin`
	before := []byte{0, 0xff, 0x80, '\n'}
	after := []byte{0, 0xfe, 0x81, '\r', '\n'}
	repo := binaryDiffRepo(t, map[string][]byte{path: before})
	first := strings.TrimSpace(binaryDiffGit(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(path)), after, 0o644); err != nil {
		t.Fatal(err)
	}
	second := binaryDiffCommit(t, repo, "second")
	working := []byte{0, 0xfd, 0x82, 'w'}
	if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(path)), working, 0o644); err != nil {
		t.Fatal(err)
	}

	// Each `to` mode must download bytes from its selected right-hand side.
	for _, tc := range []struct {
		to   string
		want []byte
	}{{"self", after}, {first, before}, {"", working}} {
		query := "download=after"
		if tc.to != "" {
			query += "&to=" + url.QueryEscape(tc.to)
		}
		req := binaryDiffRequest(repo, second, path, query)
		w := httptest.NewRecorder()
		h.server.handleGitFileDiff(w, req)
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), tc.want) {
			t.Fatalf("to=%q download: status=%d body=%v, want %v", tc.to, w.Code, w.Body.Bytes(), tc.want)
		}
	}

	for _, side := range []struct {
		name string
		want []byte
	}{{"before", before}, {"after", after}} {
		req := binaryDiffRequest(repo, second, path, "download="+side.name+"&to=self")
		w := httptest.NewRecorder()
		h.server.handleGitFileDiff(w, req)
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), side.want) {
			t.Fatalf("download %s: status=%d body=%v", side.name, w.Code, w.Body.Bytes())
		}
		disposition, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
		if err != nil || disposition != "attachment" || params["filename"] != side.name+`-odd "name".bin` {
			t.Errorf("unexpected content disposition %q (parsed %q, %v)", w.Header().Get("Content-Disposition"), disposition, err)
		}
	}

	for _, tc := range []struct {
		name, side string
		status     int
	}{{"missing before", "download=before", http.StatusNotFound}, {"invalid side", "download=middle", http.StatusBadRequest}} {
		req := binaryDiffRequest(repo, second, "added.bin", tc.side)
		w := httptest.NewRecorder()
		h.server.handleGitFileDiff(w, req)
		if w.Code != tc.status {
			t.Errorf("%s: status=%d, want %d", tc.name, w.Code, tc.status)
		}
	}
}

func TestGitFileDiffMediaPreview(t *testing.T) {
	h := NewTestHarness(t)
	png := []byte{
		0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
		0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
	}
	wav := make([]byte, 44)
	copy(wav, "RIFF")
	copy(wav[8:], "WAVEfmt ")
	copy(wav[36:], "data")
	repo := binaryDiffRepo(t, map[string][]byte{
		"image.dat": png,
		"sound.txt": wav,
		"page.html": []byte("<html>no</html>"),
		"shape.svg": []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"),
	})

	for _, tc := range []struct {
		path, contentType string
		image, audio      bool
		data              []byte
	}{{"image.dat", "image/png", true, false, png}, {"sound.txt", "audio/wave", false, true, wav}} {
		path := tc.path
		diff := requestBinaryDiff(t, h, repo, "working", path, "")
		if diff.OldImage != tc.image || diff.NewImage != tc.image || diff.OldAudio != tc.audio || diff.NewAudio != tc.audio {
			t.Errorf("%s media flags: oldImage=%v newImage=%v oldAudio=%v newAudio=%v", path, diff.OldImage, diff.NewImage, diff.OldAudio, diff.NewAudio)
		}
		req := binaryDiffRequest(repo, "working", path, "preview=after")
		w := httptest.NewRecorder()
		h.server.handleGitFileDiff(w, req)
		disposition, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != tc.contentType || disposition != "inline" || params["filename"] != "after-"+path || !bytes.Equal(w.Body.Bytes(), tc.data) || err != nil {
			t.Errorf("%s preview: status=%d type=%q disposition=%q body=%v err=%v", path, w.Code, w.Header().Get("Content-Type"), w.Header().Get("Content-Disposition"), w.Body.Bytes(), err)
		}
	}
	req := binaryDiffRequest(repo, "working", "sound.txt", "preview=after")
	req.Header.Set("Range", "bytes=0-3")
	w := httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)
	if w.Code != http.StatusPartialContent || !bytes.Equal(w.Body.Bytes(), wav[:4]) {
		t.Errorf("audio range: status=%d body=%v", w.Code, w.Body.Bytes())
	}

	for _, path := range []string{"page.html", "shape.svg"} {
		req := binaryDiffRequest(repo, "working", path, "preview=after")
		w := httptest.NewRecorder()
		h.server.handleGitFileDiff(w, req)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s preview: status=%d, want 415", path, w.Code)
		}
	}
}

func requestBinaryDiff(t *testing.T, h *TestHarness, repo, id, path, query string) GitFileDiff {
	t.Helper()
	req := binaryDiffRequest(repo, id, path, query)
	w := httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("file diff status=%d: %s", w.Code, w.Body.String())
	}
	var diff GitFileDiff
	if err := json.Unmarshal(w.Body.Bytes(), &diff); err != nil {
		t.Fatal(err)
	}
	return diff
}

func binaryDiffRequest(repo, id, path, query string) *http.Request {
	target := fmt.Sprintf("/api/git/file-diff/%s/%s?cwd=%s", id, url.PathEscape(path), url.QueryEscape(repo))
	if query != "" {
		target += "&" + query
	}
	return httptest.NewRequest(http.MethodGet, target, nil)
}

func binaryDiffRepo(t *testing.T, files map[string][]byte) string {
	t.Helper()
	repo := t.TempDir()
	binaryDiffGit(t, repo, "init")
	binaryDiffGit(t, repo, "config", "user.name", "Test")
	binaryDiffGit(t, repo, "config", "user.email", "test@example.com")
	for name, content := range files {
		fullPath := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	binaryDiffGit(t, repo, "add", ".")
	binaryDiffGit(t, repo, "commit", "-m", "initial")
	return repo
}

func binaryDiffCommit(t *testing.T, repo, message string) string {
	t.Helper()
	binaryDiffGit(t, repo, "add", "-A")
	binaryDiffGit(t, repo, "commit", "-m", message)
	return strings.TrimSpace(binaryDiffGit(t, repo, "rev-parse", "HEAD"))
}

func binaryDiffGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
