package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// findFiles issues a GET /api/find-files and decodes the response.
func findFiles(t *testing.T, h *TestHarness, dir, query string) FindFilesResponse {
	t.Helper()
	u := "/api/find-files?dir=" + url.QueryEscape(dir)
	if query != "" {
		u += "&q=" + url.QueryEscape(query)
	}
	req := httptest.NewRequest(http.MethodGet, u, nil)
	w := httptest.NewRecorder()
	h.server.handleFindFiles(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("find-files %q %q: expected 200, got %d: %s", dir, query, w.Code, w.Body.String())
	}
	var resp FindFilesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func hasPath(matches []FindFilesMatch, p string) bool {
	for _, m := range matches {
		if m.Path == p {
			return true
		}
	}
	return false
}

// inRun reports whether idx falls in [start, start+length), with start < 0
// ("not found") never matching.
func inRun(idx, start, length int) bool {
	return start >= 0 && idx >= start && idx < start+length
}

// TestFindFilesGitRepo verifies fuzzy file finding inside a git repo honors
// .gitignore, ranks by the query, and returns highlight indexes.
func TestFindFilesGitRepo(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	mustGitInit(t, dir)
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# hi\n")
	writeFile(t, filepath.Join(dir, "internal", "server", "handler.go"), "package server\n")
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n")
	writeFile(t, filepath.Join(dir, "ignored.txt"), "secret\n")

	t.Run("no_query_lists_files", func(t *testing.T) {
		resp := findFiles(t, h, dir, "")
		if !hasPath(resp.Matches, "main.go") {
			t.Errorf("expected main.go in %+v", resp.Matches)
		}
		if hasPath(resp.Matches, "ignored.txt") {
			t.Errorf("gitignored file should not appear: %+v", resp.Matches)
		}
	})

	t.Run("fuzzy_query", func(t *testing.T) {
		resp := findFiles(t, h, dir, "handler")
		if len(resp.Matches) == 0 || resp.Matches[0].Path != "internal/server/handler.go" {
			t.Errorf("expected handler.go ranked first, got %+v", resp.Matches)
		}
		if len(resp.Matches[0].MatchedIndexes) == 0 {
			t.Errorf("expected match highlight indexes, got none")
		}
	})

	t.Run("subsequence_match", func(t *testing.T) {
		resp := findFiles(t, h, dir, "mgo")
		if !hasPath(resp.Matches, "main.go") {
			t.Errorf("expected fuzzy subsequence match main.go, got %+v", resp.Matches)
		}
	})
}

// TestFindFilesMultiTermQuery verifies a space-separated query ANDs its terms:
// each term is fuzzy-matched independently (in any order) against the path, so
// "vm storage s3" finds vm-storage-s3-backup-design.md.
func TestFindFilesMultiTermQuery(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	mustGitInit(t, dir)
	s3 := "exelet/docs/vm-storage-s3-backup-design.md"
	for _, p := range []string{
		s3,
		"exelet/docs/vm-storage-replication.md",
		"exelet/docs/vm-storage-manual-resize.md",
		"exelet/docs/s3-uploads.md",
	} {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(p)), "x\n")
	}

	t.Run("terms_are_anded", func(t *testing.T) {
		resp := findFiles(t, h, dir, "vm storage s3")
		if len(resp.Matches) != 1 || resp.Matches[0].Path != s3 {
			t.Fatalf("expected only %s, got %+v", s3, resp.Matches)
		}
		if len(resp.Matches[0].MatchedIndexes) == 0 {
			t.Errorf("expected highlight indexes, got none")
		}
		runes := []rune(resp.Matches[0].Path)
		for _, idx := range resp.Matches[0].MatchedIndexes {
			if idx < 0 || idx >= len(runes) {
				t.Fatalf("match index %d out of range [0,%d)", idx, len(runes))
			}
		}
	})

	t.Run("highlights_literal_runs", func(t *testing.T) {
		resp := findFiles(t, h, dir, "vm storage s3")
		if len(resp.Matches) != 1 {
			t.Fatalf("expected 1 match, got %+v", resp.Matches)
		}
		// Each term should underline its literal occurrence, not a scattered
		// subsequence (fuzzy alone highlights the 'm' of ".md" for "vm").
		// The fixture path is ASCII, so rune offsets == index offsets here;
		// TestFindFilesMultibyteHighlight covers the non-ASCII case.
		path := resp.Matches[0].Path
		for _, term := range []string{"vm", "storage", "s3"} {
			start := strings.Index(path, term)
			if start < 0 {
				t.Fatalf("term %q not in %q", term, path)
			}
			for i := start; i < start+len(term); i++ {
				if !slices.Contains(resp.Matches[0].MatchedIndexes, i) {
					t.Errorf("expected index %d (term %q) highlighted, got %v", i, term, resp.Matches[0].MatchedIndexes)
				}
			}
		}
		// Nothing outside the three terms should light up.
		for _, idx := range resp.Matches[0].MatchedIndexes {
			switch {
			case inRun(idx, strings.Index(path, "vm"), 2),
				inRun(idx, strings.Index(path, "storage"), 7),
				inRun(idx, strings.Index(path, "s3"), 2):
			default:
				t.Errorf("unrelated index %d highlighted in %q (all: %v)", idx, path, resp.Matches[0].MatchedIndexes)
			}
		}
	})

	t.Run("repeated_term_scores_once", func(t *testing.T) {
		// A repeated term must not inflate the score (or the highlights).
		one := findFiles(t, h, dir, "vm storage s3")
		twice := findFiles(t, h, dir, "vm storage s3 storage")
		if len(twice.Matches) != 1 || twice.Matches[0].Path != one.Matches[0].Path {
			t.Fatalf("expected the same single match, got %+v", twice.Matches)
		}
		if !slices.Equal(one.Matches[0].MatchedIndexes, twice.Matches[0].MatchedIndexes) {
			t.Errorf("highlights differ: %v vs %v", one.Matches[0].MatchedIndexes, twice.Matches[0].MatchedIndexes)
		}
	})

	t.Run("terms_out_of_order", func(t *testing.T) {
		resp := findFiles(t, h, dir, "s3 storage")
		if len(resp.Matches) != 1 || resp.Matches[0].Path != s3 {
			t.Fatalf("expected only %s, got %+v", s3, resp.Matches)
		}
	})

	t.Run("trailing_space_ignored", func(t *testing.T) {
		resp := findFiles(t, h, dir, "vm-storage ")
		if len(resp.Matches) != 3 {
			t.Fatalf("expected 3 vm-storage matches, got %+v", resp.Matches)
		}
	})

	t.Run("unmatched_term_excludes", func(t *testing.T) {
		resp := findFiles(t, h, dir, "vm storage zzzz")
		if len(resp.Matches) != 0 {
			t.Fatalf("expected no matches, got %+v", resp.Matches)
		}
	})
}

// TestFindFilesMultiTermRanking verifies a multi-term query doesn't punish long
// paths once per term. sahilm/fuzzy charges "unmatched characters" per call, so
// naively summing per-term scores buries the well-separated deep path under a
// short run-together name — exactly the file the user was aiming for.
func TestFindFilesMultiTermRanking(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	mustGitInit(t, dir)
	want := "exelet/docs/vm-storage-s3-backup-design.md"
	for _, p := range []string{want, "a/vmstorages3.md"} {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(p)), "x\n")
	}

	resp := findFiles(t, h, dir, "vm storage s3")
	if len(resp.Matches) != 2 {
		t.Fatalf("expected both files to match, got %+v", resp.Matches)
	}
	if resp.Matches[0].Path != want {
		t.Errorf("expected %q ranked first, got %+v", want, resp.Matches)
	}
}

// TestFindFilesDuplicatePaths verifies a path listed more than once (as
// `git ls-files` does for each stage of an unresolved merge conflict) yields a
// single result row, for both the single- and multi-term paths. The UI keys its
// list on the path, so duplicates would collide.
func TestFindFilesDuplicatePaths(t *testing.T) {
	t.Parallel()

	dup := []string{"a/vm-storage.md", "a/vm-storage.md", "b/other.md"}
	for _, query := range []string{"vm-storage", "vm storage"} {
		matches := findFuzzyMulti(query, dup)
		count := 0
		for _, m := range matches {
			if m.str == "a/vm-storage.md" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("query %q: got %d rows for the duplicated path, want 1 (%+v)", query, count, matches)
		}
	}

	// A duplicate must not double-score the path either, which would let it
	// outrank a better match.
	single := findFuzzyMulti("vm storage", []string{"a/vm-storage.md"})
	doubled := findFuzzyMulti("vm storage", []string{"a/vm-storage.md", "a/vm-storage.md"})
	if len(single) != 1 || len(doubled) != 1 {
		t.Fatalf("expected one match each, got %+v and %+v", single, doubled)
	}
	if single[0].score != doubled[0].score {
		t.Errorf("duplicate changed the score: %d vs %d", single[0].score, doubled[0].score)
	}
}

// TestFindFilesMultibyteHighlight verifies match indexes are rune (not byte)
// offsets so the UI highlights the right characters in non-ASCII paths.
func TestFindFilesMultibyteHighlight(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	mustGitInit(t, dir)
	// "café" is 5 bytes (é = 2 bytes) but 4 runes, so a byte offset for the
	// "m" in main.go (byte 6) differs from its rune offset (5).
	writeFile(t, filepath.Join(dir, "café", "main.go"), "package main\n")

	resp := findFiles(t, h, dir, "main")
	if len(resp.Matches) == 0 {
		t.Fatalf("expected a match, got none")
	}
	m := resp.Matches[0]
	if m.Path != "café/main.go" {
		t.Fatalf("unexpected path %q", m.Path)
	}
	runes := []rune(m.Path)
	for _, idx := range m.MatchedIndexes {
		if idx < 0 || idx >= len(runes) {
			t.Fatalf("match index %d out of rune range [0,%d)", idx, len(runes))
		}
	}
	if len(m.MatchedIndexes) == 0 || runes[m.MatchedIndexes[0]] != 'm' {
		t.Errorf("expected first match index to point at 'm', got indexes %v in %q", m.MatchedIndexes, m.Path)
	}

	// Multi-term queries union the per-term highlights, and a term can itself be
	// multibyte: refineHighlights works in bytes, so this is where a term's
	// interior byte offsets could leak through as bogus rune offsets.
	resp = findFiles(t, h, dir, "café main")
	if len(resp.Matches) != 1 {
		t.Fatalf("expected 1 match for the multi-term query, got %+v", resp.Matches)
	}
	m = resp.Matches[0]
	runes = []rune(m.Path)
	var highlighted string
	for _, idx := range m.MatchedIndexes {
		if idx < 0 || idx >= len(runes) {
			t.Fatalf("match index %d out of rune range [0,%d)", idx, len(runes))
		}
		highlighted += string(runes[idx])
	}
	if highlighted != "cafémain" {
		t.Errorf("highlighted runes = %q, want %q (indexes %v in %q)", highlighted, "cafémain", m.MatchedIndexes, m.Path)
	}
}

// TestFindFilesCacheNoPoisonOnFailure verifies a failed listing isn't cached,
// so a subsequent successful request still sees the files.
func TestFindFilesCacheNoPoisonOnFailure(t *testing.T) {
	t.Parallel()
	c := newFileListCache()

	files, _ := c.get("/some/dir", func() ([]string, bool, bool) {
		return nil, false, false // ok=false: must not be cached
	})
	if len(files) != 0 {
		t.Fatalf("expected empty result from failed load, got %v", files)
	}
	if _, ok := c.entries["/some/dir"]; ok {
		t.Fatalf("failed load should not be cached")
	}

	files, _ = c.get("/some/dir", func() ([]string, bool, bool) {
		return []string{"a.go", "b.go"}, false, true
	})
	if len(files) != 2 {
		t.Fatalf("expected 2 files after successful load, got %v", files)
	}
	if _, ok := c.entries["/some/dir"]; !ok {
		t.Fatalf("successful load should be cached")
	}
}

// TestFileListCacheEviction verifies the cache stays under its dir cap.
func TestFileListCacheEviction(t *testing.T) {
	t.Parallel()
	c := newFileListCache()
	for i := 0; i < fileListCacheMaxDirs*2; i++ {
		dir := fmt.Sprintf("/dir/%d", i)
		c.get(dir, func() ([]string, bool, bool) { return []string{"x"}, false, true })
	}
	if len(c.entries) > fileListCacheMaxDirs {
		t.Errorf("cache exceeded cap: %d > %d", len(c.entries), fileListCacheMaxDirs)
	}
}

// TestFileListCacheFileCap verifies the cache bounds retained paths, not just
// directory count: typing an absolute path re-roots the search per keystroke,
// and a few huge listings cost far more than many small ones.
func TestFileListCacheFileCap(t *testing.T) {
	t.Parallel()
	c := newFileListCache()

	big := make([]string, fileListCacheMaxFiles/4)
	for i := range big {
		big[i] = fmt.Sprintf("file-%d", i)
	}
	for i := 0; i < 10; i++ {
		dir := fmt.Sprintf("/big/%d", i)
		c.get(dir, func() ([]string, bool, bool) { return big, false, true })
	}
	if c.files > fileListCacheMaxFiles {
		t.Errorf("cache retained %d files, cap is %d", c.files, fileListCacheMaxFiles)
	}
	if len(c.entries) == 0 {
		t.Error("cache evicted everything; the newest entry should survive")
	}

	t.Run("count_tracks_the_map", func(t *testing.T) {
		total := 0
		for _, e := range c.entries {
			total += len(e.files)
		}
		if total != c.files {
			t.Errorf("tracked count %d != actual %d", c.files, total)
		}
	})

	t.Run("single_oversized_listing_is_kept", func(t *testing.T) {
		// One directory bigger than the whole cap must still be served: it's
		// what the user is looking at.
		huge := make([]string, fileListCacheMaxFiles+1)
		got, _ := c.get("/huge", func() ([]string, bool, bool) { return huge, false, true })
		if len(got) != len(huge) {
			t.Fatalf("got %d files, want %d", len(got), len(huge))
		}
		if _, ok := c.entries["/huge"]; !ok {
			t.Error("the just-computed entry was evicted")
		}
	})
}

// TestFindFilesNonGit verifies the filesystem-walk fallback works outside a
// git repo and skips heavy directories.
func TestFindFilesNonGit(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "alpha.txt"), "a\n")
	writeFile(t, filepath.Join(dir, "sub", "beta.txt"), "b\n")
	writeFile(t, filepath.Join(dir, "node_modules", "junk.js"), "junk\n")

	resp := findFiles(t, h, dir, "")
	if !hasPath(resp.Matches, "alpha.txt") || !hasPath(resp.Matches, "sub/beta.txt") {
		t.Errorf("expected alpha.txt and sub/beta.txt, got %+v", resp.Matches)
	}
	if hasPath(resp.Matches, "node_modules/junk.js") {
		t.Errorf("node_modules should be skipped: %+v", resp.Matches)
	}
}

// TestFindFilesBadRequests verifies input validation.
func TestFindFilesBadRequests(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	t.Run("relative_dir", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/find-files?dir=relative/path", nil)
		w := httptest.NewRecorder()
		h.server.handleFindFiles(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("missing_dir", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/find-files?dir=/nonexistent/xyz/123", nil)
		w := httptest.NewRecorder()
		h.server.handleFindFiles(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("method_not_allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/find-files?dir=/tmp", nil)
		w := httptest.NewRecorder()
		h.server.handleFindFiles(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405, got %d", w.Code)
		}
	})
}

// TestFindFilesPathQuery verifies that a query which is itself a path escapes
// the working directory: typing an absolute path (or a ./ ../ ~ relative one)
// re-roots the search at that directory, so the file can be opened in the
// editor even though it lives nowhere near the conversation's cwd.
func TestFindFilesPathQuery(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	// The finder's working directory: a git repo that knows nothing about the
	// files the user is about to type a path to. `deep/` gives the whole-tree
	// fuzzy cases somewhere to hit that a re-rooted search would miss.
	cwd := t.TempDir()
	mustGitInit(t, cwd)
	writeFile(t, filepath.Join(cwd, "main.go"), "package main\n")
	writeFile(t, filepath.Join(cwd, "deep", "sub", "handler.go"), "package sub\n")
	writeFile(t, filepath.Join(cwd, "deep", "sub-notes.md"), "x\n")

	elsewhere := t.TempDir()
	writeFile(t, filepath.Join(elsewhere, "handoff.md"), "notes\n")
	writeFile(t, filepath.Join(elsewhere, "sub", "handler.go"), "package sub\n")

	t.Run("absolute_prefix_reroots", func(t *testing.T) {
		resp := findFiles(t, h, cwd, filepath.Join(elsewhere, "hando"))
		if resp.SearchDir != elsewhere {
			t.Errorf("search_dir = %q, want %q", resp.SearchDir, elsewhere)
		}
		if resp.Dir != cwd {
			t.Errorf("dir = %q, want the requested %q", resp.Dir, cwd)
		}
		// Only the trailing segment is matched within the re-rooted directory.
		if resp.MatchQuery != "hando" {
			t.Errorf("match_query = %q, want %q", resp.MatchQuery, "hando")
		}
		if !hasPath(resp.Matches, "handoff.md") {
			t.Errorf("expected handoff.md relative to %s, got %+v", elsewhere, resp.Matches)
		}
	})

	t.Run("absolute_directory_lists_it", func(t *testing.T) {
		for _, q := range []string{elsewhere, elsewhere + "/"} {
			resp := findFiles(t, h, cwd, q)
			if resp.SearchDir != elsewhere {
				t.Errorf("%q: search_dir = %q, want %q", q, resp.SearchDir, elsewhere)
			}
			// Naming a directory matches nothing in particular within it, which
			// is what tells the UI to say "no files here" rather than "no match".
			if resp.MatchQuery != "" {
				t.Errorf("%q: match_query = %q, want empty", q, resp.MatchQuery)
			}
			if !hasPath(resp.Matches, "handoff.md") || !hasPath(resp.Matches, "sub/handler.go") {
				t.Errorf("%q: expected the directory listing, got %+v", q, resp.Matches)
			}
		}
	})

	t.Run("dot_relative_reroots", func(t *testing.T) {
		resp := findFiles(t, h, filepath.Join(elsewhere, "sub"), "../handoff.md")
		if resp.SearchDir != elsewhere {
			t.Errorf("search_dir = %q, want %q", resp.SearchDir, elsewhere)
		}
		if !hasPath(resp.Matches, "handoff.md") {
			t.Errorf("expected handoff.md, got %+v", resp.Matches)
		}
	})

	t.Run("missing_directory_finds_nothing", func(t *testing.T) {
		// A path whose directory doesn't exist has no sensible fuzzy reading:
		// answering it with matches from the working directory would be noise.
		// The reported search_dir names where it looked, so the UI can say so.
		resp := findFiles(t, h, cwd, "/nonexistent/xyz/main")
		if resp.SearchDir != "/nonexistent/xyz" {
			t.Errorf("search_dir = %q, want %q", resp.SearchDir, "/nonexistent/xyz")
		}
		if len(resp.Matches) != 0 {
			t.Errorf("expected no matches for a bogus path, got %+v", resp.Matches)
		}
	})

	t.Run("embedded_slash_is_not_a_path", func(t *testing.T) {
		// "sub/handler" is the finder's partial-path idiom, matched against the
		// whole tree. Re-rooting it at ./sub would hide every match elsewhere,
		// which is exactly what makes a slash too weak a signal on its own.
		resp := findFiles(t, h, cwd, "sub/handler")
		if resp.SearchDir != cwd {
			t.Errorf("search_dir = %q, want the working dir %q", resp.SearchDir, cwd)
		}
		if !hasPath(resp.Matches, "deep/sub/handler.go") {
			t.Errorf("expected the whole-tree hit, got %+v", resp.Matches)
		}
	})

	t.Run("bare_directory_name_is_not_a_path", func(t *testing.T) {
		// A word that happens to name a subdirectory ("shelley" at a repo root)
		// is still a fuzzy pattern: re-rooting into it would replace every
		// match elsewhere in the tree with that directory's listing.
		resp := findFiles(t, h, filepath.Join(cwd, "deep"), "sub")
		if resp.SearchDir != filepath.Join(cwd, "deep") {
			t.Errorf("search_dir = %q, want the working dir %q", resp.SearchDir, filepath.Join(cwd, "deep"))
		}
		if !hasPath(resp.Matches, "sub-notes.md") {
			t.Errorf("expected the whole-tree hit, got %+v", resp.Matches)
		}
	})

	t.Run("explicit_dot_slash_is_a_path", func(t *testing.T) {
		// The same text with a "./" prefix asks for the re-rooted reading.
		resp := findFiles(t, h, filepath.Join(elsewhere), "./sub/handler")
		if resp.SearchDir != filepath.Join(elsewhere, "sub") {
			t.Errorf("search_dir = %q, want %q", resp.SearchDir, filepath.Join(elsewhere, "sub"))
		}
		if !hasPath(resp.Matches, "handler.go") {
			t.Errorf("expected handler.go, got %+v", resp.Matches)
		}
	})

	t.Run("plain_query_keeps_working_dir", func(t *testing.T) {
		resp := findFiles(t, h, cwd, "main")
		if resp.SearchDir != cwd {
			t.Errorf("search_dir = %q, want %q", resp.SearchDir, cwd)
		}
		if !hasPath(resp.Matches, "main.go") {
			t.Errorf("expected main.go, got %+v", resp.Matches)
		}
	})
}

// TestFindFilesExactPathPinned verifies a query naming an existing file always
// yields that file, even when the listing can't surface it: `git ls-files`
// hides .gitignore'd files, but the user typing the path clearly means to edit
// that one.
func TestFindFilesExactPathPinned(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	mustGitInit(t, dir)
	writeFile(t, filepath.Join(dir, ".gitignore"), "secret.env\n")
	writeFile(t, filepath.Join(dir, "secret.env"), "TOKEN=1\n")

	t.Run("ignored_file_absent_from_listing", func(t *testing.T) {
		// Establishes what makes pinning necessary: a bare fuzzy query can't
		// reach a .gitignore'd file, because `git ls-files` never lists it.
		resp := findFiles(t, h, dir, "secret")
		if hasPath(resp.Matches, "secret.env") {
			t.Errorf("expected the ignored file to be absent from the listing, got %+v", resp.Matches)
		}
	})

	t.Run("absolute_path", func(t *testing.T) {
		resp := findFiles(t, h, dir, filepath.Join(dir, "secret.env"))
		if !hasPath(resp.Matches, "secret.env") {
			t.Errorf("expected the typed file, got %+v", resp.Matches)
		}
	})

	t.Run("relative_path", func(t *testing.T) {
		resp := findFiles(t, h, dir, "secret.env")
		if len(resp.Matches) == 0 || resp.Matches[0].Path != "secret.env" {
			t.Errorf("expected secret.env first, got %+v", resp.Matches)
		}
		if resp.SearchDir != dir {
			t.Errorf("search_dir = %q, want %q", resp.SearchDir, dir)
		}
	})

	t.Run("ignored_subtree_is_walked", func(t *testing.T) {
		// Re-rooting into an ignored directory must list it: `git ls-files`
		// returns nothing there, which means "ignored", not "empty". Pinning
		// only rescues a fully typed filename, not browsing.
		writeFile(t, filepath.Join(dir, "node_modules", "pkg", "index.js"), "x\n")
		writeFile(t, filepath.Join(dir, ".gitignore"), "secret.env\nnode_modules/\n")
		resp := findFiles(t, h, dir, filepath.Join(dir, "node_modules", "pkg")+"/")
		if !hasPath(resp.Matches, "index.js") {
			t.Errorf("expected index.js from the ignored subtree, got %+v", resp.Matches)
		}
	})

	t.Run("ignored_files_stay_hidden_elsewhere", func(t *testing.T) {
		// The flip side: a directory that merely looks empty to `git ls-files`
		// because everything in it is ignored is NOT itself ignored, so its
		// contents must stay hidden rather than get walked.
		logs := filepath.Join(dir, "logs")
		writeFile(t, filepath.Join(logs, "debug.log"), "x\n")
		writeFile(t, filepath.Join(dir, ".gitignore"), "secret.env\nnode_modules/\n*.log\n")
		resp := findFiles(t, h, logs, "")
		if len(resp.Matches) != 0 {
			t.Errorf("expected the ignored .log to stay hidden, got %+v", resp.Matches)
		}
	})

	t.Run("directory_not_pinned", func(t *testing.T) {
		// A directory isn't editable; naming one lists it instead.
		resp := findFiles(t, h, dir, dir)
		for _, m := range resp.Matches {
			if m.Path == "" || m.Path == "." {
				t.Errorf("directory leaked into matches: %+v", resp.Matches)
			}
		}
	})
}

// TestFindFilesTildeQuery verifies ~ expands to $HOME. It sets HOME (so it
// can't be parallel) to a small temp tree rather than walking the real one.
func TestFindFilesTildeQuery(t *testing.T) {
	h := NewTestHarness(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, "notes", "todo.md"), "x\n")

	cwd := t.TempDir()

	resp := findFiles(t, h, cwd, "~/notes/todo")
	if resp.SearchDir != filepath.Join(home, "notes") {
		t.Errorf("search_dir = %q, want %q", resp.SearchDir, filepath.Join(home, "notes"))
	}
	if !hasPath(resp.Matches, "todo.md") {
		t.Errorf("expected todo.md, got %+v", resp.Matches)
	}
}

// TestFindFilesPathQueryEdges covers path-query shapes that shouldn't be
// mistaken for one another: spaces inside an explicitly path-rooted query,
// trailing slashes, and a query naming the search directory itself.
func TestFindFilesPathQueryEdges(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	spaced := filepath.Join(dir, "My Project")
	writeFile(t, filepath.Join(spaced, "design doc.md"), "x\n")
	writeFile(t, filepath.Join(dir, "plain.md"), "x\n")

	t.Run("spaces_inside_a_rooted_path", func(t *testing.T) {
		// A leading "/" (or ~/ ./ ../) declares the query a path, so its spaces
		// belong to the path rather than splitting it into fuzzy terms.
		resp := findFiles(t, h, dir, filepath.Join(spaced, "design"))
		if resp.SearchDir != spaced {
			t.Fatalf("search_dir = %q, want %q", resp.SearchDir, spaced)
		}
		if !hasPath(resp.Matches, "design doc.md") {
			t.Errorf("expected design doc.md, got %+v", resp.Matches)
		}
	})

	t.Run("trailing_slash_never_pins_a_file", func(t *testing.T) {
		// "plain.md/" asserts a directory; there is no such directory, so this
		// stays an ordinary fuzzy query and must not open the file.
		resp := findFiles(t, h, dir, "plain.md/")
		if resp.SearchDir != dir {
			t.Errorf("search_dir = %q, want %q", resp.SearchDir, dir)
		}
		if len(resp.Matches) != 0 {
			t.Errorf("expected no matches for %q, got %+v", "plain.md/", resp.Matches)
		}
	})

	t.Run("search_dir_itself_is_not_a_match", func(t *testing.T) {
		// filepath.Rel(dir, dir) is ".", which is not a file to open.
		resp := findFiles(t, h, dir, dir+"/")
		for _, m := range resp.Matches {
			if m.Path == "." || m.Path == "" {
				t.Errorf("the directory itself leaked into matches: %+v", resp.Matches)
			}
		}
	})

	t.Run("multi_term_query_is_not_a_path", func(t *testing.T) {
		// Without a path-ish prefix, whitespace still means "fuzzy terms", even
		// with a slash in the query: "My Project/design" would name a real
		// directory under dir, but a bare multi-word query stays fuzzy so
		// "vm storage s3"-style searching keeps working.
		resp := findFiles(t, h, dir, "My Project/design")
		if resp.SearchDir != dir {
			t.Errorf("search_dir = %q, want %q", resp.SearchDir, dir)
		}
		if !hasPath(resp.Matches, "My Project/design doc.md") {
			t.Errorf("expected the fuzzy hit under the working dir, got %+v", resp.Matches)
		}
	})

	t.Run("dot_prefix_makes_spaces_part_of_the_path", func(t *testing.T) {
		// "./" declares a path, so the same text re-roots instead.
		resp := findFiles(t, h, dir, "./My Project/design")
		if resp.SearchDir != spaced {
			t.Errorf("search_dir = %q, want %q", resp.SearchDir, spaced)
		}
		if !hasPath(resp.Matches, "design doc.md") {
			t.Errorf("expected design doc.md, got %+v", resp.Matches)
		}
	})
}

// TestFindFilesPinTruncates verifies that pinning an exact match into a full
// result page reports the truncation it causes rather than silently dropping
// the last row.
func TestFindFilesPinTruncates(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	t.Run("handler_reports_truncation", func(t *testing.T) {
		// A limit of 1 with a fuzzy hit that isn't the pinned file: the pin
		// takes the only slot, so the response must say it was truncated.
		dir := t.TempDir()
		mustGitInit(t, dir)
		writeFile(t, filepath.Join(dir, ".gitignore"), "target.env\n")
		writeFile(t, filepath.Join(dir, "target.env"), "x\n")
		writeFile(t, filepath.Join(dir, "target.envoy"), "x\n")

		u := "/api/find-files?dir=" + url.QueryEscape(dir) + "&q=target.env&limit=1"
		req := httptest.NewRequest(http.MethodGet, u, nil)
		w := httptest.NewRecorder()
		h.server.handleFindFiles(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp FindFilesResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(resp.Matches) != 1 || resp.Matches[0].Path != "target.env" {
			t.Fatalf("expected the pinned file alone, got %+v", resp.Matches)
		}
		if !resp.Truncated {
			t.Error("expected truncated=true: the pin displaced a match")
		}
	})

	t.Run("drops_and_reports", func(t *testing.T) {
		matches := []FindFilesMatch{{Path: "a.go"}, {Path: "b.go"}, {Path: "c.go"}}
		out, dropped := pinMatch(matches, "pinned.go", len(matches))
		if !dropped {
			t.Error("expected dropped=true when the pin pushes a match past the limit")
		}
		if len(out) != len(matches) || out[0].Path != "pinned.go" {
			t.Errorf("out = %+v", out)
		}
	})

	t.Run("existing_match_moves_and_keeps_highlights", func(t *testing.T) {
		withHits := []FindFilesMatch{{Path: "a.go"}, {Path: "b.go", MatchedIndexes: []int{0}}}
		out, dropped := pinMatch(withHits, "b.go", len(withHits))
		if dropped {
			t.Error("expected dropped=false: the pin replaced an existing row")
		}
		if len(out) != 2 || out[0].Path != "b.go" || out[1].Path != "a.go" {
			t.Fatalf("out = %+v", out)
		}
		// Its fuzzy highlights survive the move, rather than being replaced by
		// fabricated ones (which would have to be UTF-16 offsets for the UI).
		if !slices.Equal(out[0].MatchedIndexes, []int{0}) {
			t.Errorf("highlights = %v, want [0]", out[0].MatchedIndexes)
		}
	})
}

// TestHandleReadFile verifies the arbitrary-text-file read endpoint.
func TestHandleReadFile(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	writeFile(t, path, "hello world\n")

	t.Run("reads_content", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/read-file?path="+path, nil)
		w := httptest.NewRecorder()
		h.server.handleReadFile(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Content != "hello world\n" {
			t.Errorf("content = %q", resp.Content)
		}
		if resp.Path != path {
			t.Errorf("path = %q, want %q", resp.Path, path)
		}
	})

	t.Run("relative_rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/read-file?path=relative.txt", nil)
		w := httptest.NewRecorder()
		h.server.handleReadFile(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("missing_file", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/read-file?path="+filepath.Join(dir, "nope.txt"), nil)
		w := httptest.NewRecorder()
		h.server.handleReadFile(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Code)
		}
	})

	t.Run("directory_rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/read-file?path="+dir, nil)
		w := httptest.NewRecorder()
		h.server.handleReadFile(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustGitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}
