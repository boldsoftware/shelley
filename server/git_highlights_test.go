package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/commitnotes"
)

func TestHandleGitHighlights(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	dir := setupTestGitRepo(t)
	hash := strings.TrimSpace(testGitOutput(t, dir, "rev-parse", "HEAD"))

	request := func(ref string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/git/highlights?cwd=%s&hash=%s", dir, ref), nil)
		w := httptest.NewRecorder()
		h.server.handleGitHighlights(w, req)
		return w
	}
	diffs := func() map[string]GitDiffInfo {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/git/diffs?cwd=%s&stats=false", dir), nil)
		w := httptest.NewRecorder()
		h.server.handleGitDiffs(w, req)
		var response struct {
			Diffs []GitDiffInfo `json:"diffs"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		byID := make(map[string]GitDiffInfo)
		for _, d := range response.Diffs {
			byID[d.ID] = d
		}
		return byID
	}

	if w := request(hash); w.Code != http.StatusNotFound {
		t.Fatalf("no note: got %d %s", w.Code, w.Body.String())
	}
	if diffs()[hash].HasHighlights {
		t.Fatal("hasHighlights without a note")
	}

	// A note that no longer matches the commit is treated as absent.
	stale := []byte(`{"version":1,"files":["test.txt"],"highlights":[{"file":"test.txt","line":9,"comment":"gone"}]}`)
	if err := commitnotes.WriteHighlights(dir, hash, stale); err != nil {
		t.Fatal(err)
	}
	if w := request(hash); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "line 9 is outside") {
		t.Fatalf("stale note: got %d %s", w.Code, w.Body.String())
	}

	note := []byte(`{"version":1,"files":["test.txt"],"highlights":[{"file":"test.txt","line":1,"comment":"the greeting"}]}`)
	if err := commitnotes.WriteHighlights(dir, hash, note); err != nil {
		t.Fatal(err)
	}
	w := request(hash[:8])
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var response GitHighlightsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Hash != hash || len(response.Highlights.Highlights) != 1 ||
		response.Highlights.Highlights[0] != (commitnotes.Highlight{File: "test.txt", Line: 1, Comment: "the greeting"}) {
		t.Fatalf("response = %s", w.Body.String())
	}
	if d := diffs()[hash]; !d.HasHighlights {
		t.Fatalf("diff decorations = %+v", d)
	}
}
