package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func findFilesWithDirs(t *testing.T, h *TestHarness, dir, query, content string) FindFilesResponse {
	t.Helper()
	u := "/api/find-files?dir=" + url.QueryEscape(dir) + "&include_dirs=true"
	if query != "" {
		u += "&q=" + url.QueryEscape(query)
	}
	if content != "" {
		u += "&content=" + url.QueryEscape(content)
	}
	req := httptest.NewRequest(http.MethodGet, u, nil)
	w := httptest.NewRecorder()
	h.server.handleFindFiles(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("find-files with dirs %q %q: expected 200, got %d: %s", dir, query, w.Code, w.Body.String())
	}
	var resp FindFilesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func requireDirectoryMatch(t *testing.T, matches []FindFilesMatch, path string) FindFilesMatch {
	t.Helper()
	for _, match := range matches {
		if match.Path == path {
			if !match.IsDir {
				t.Fatalf("%q was not marked as a directory: %+v", path, match)
			}
			if !strings.HasSuffix(match.Path, "/") {
				t.Fatalf("directory path lacks slash: %+v", match)
			}
			return match
		}
	}
	t.Fatalf("missing directory %q in %+v", path, matches)
	return FindFilesMatch{}
}

func countPath(matches []FindFilesMatch, path string) int {
	count := 0
	for _, match := range matches {
		if match.Path == path {
			count++
		}
	}
	return count
}

func TestFindFilesIncludesDirectories(t *testing.T) {
	t.Parallel()
	for _, gitRepo := range []bool{false, true} {
		name := "non_git"
		if gitRepo {
			name = "git"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := NewTestHarness(t)
			dir := t.TempDir()
			if gitRepo {
				mustGitInit(t, dir)
			}
			writeFile(t, filepath.Join(dir, "top.txt"), "x\n")
			writeFile(t, filepath.Join(dir, "nested", "child.txt"), "x\n")
			if err := os.MkdirAll(filepath.Join(dir, "empty", "deeper"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "node_modules"), 0o755); err != nil {
				t.Fatal(err)
			}

			resp := findFilesWithDirs(t, h, dir, "", "skip")
			for _, path := range []string{"nested/", "empty/", "empty/deeper/"} {
				requireDirectoryMatch(t, resp.Matches, path)
			}
			for _, path := range []string{"top.txt", "nested/child.txt"} {
				m := findMatch(t, resp.Matches, path)
				if m.IsDir || strings.HasSuffix(m.Path, "/") {
					t.Errorf("file changed shape: %+v", m)
				}
			}
			if hasPath(resp.Matches, "node_modules/") {
				t.Errorf("crawlSkipNames directory leaked: %+v", resp.Matches)
			}
		})
	}
}

func TestFindFilesDirectoriesHonorGitIgnoreAndTrackedParents(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	dir := t.TempDir()
	mustGitInit(t, dir)
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored-empty/\nignored-tree/\nkept/\n")
	if err := os.MkdirAll(filepath.Join(dir, "ignored-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "ignored-tree", "hidden.txt"), "x\n")
	writeFile(t, filepath.Join(dir, "kept", "nested", "tracked.txt"), "x\n")
	cmd := exec.Command("git", "add", "-f", "kept/nested/tracked.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}

	resp := findFilesWithDirs(t, h, dir, "", "skip")
	if hasPath(resp.Matches, "ignored-empty/") || hasPath(resp.Matches, "ignored-tree/") {
		t.Errorf("ignored directories leaked into results: %+v", resp.Matches)
	}
	if hasPath(resp.Matches, "ignored-tree/hidden.txt") {
		t.Errorf("ignored file leaked into results: %+v", resp.Matches)
	}
	requireDirectoryMatch(t, resp.Matches, "kept/")
	requireDirectoryMatch(t, resp.Matches, "kept/nested/")
	if !hasPath(resp.Matches, "kept/nested/tracked.txt") {
		t.Errorf("tracked file under ignored parent missing: %+v", resp.Matches)
	}
}

func TestFindFilesDirectoryCacheModeIsolation(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "folder", "file.txt"), "x\n")
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	withDirs := findFilesWithDirs(t, h, dir, "", "skip")
	requireDirectoryMatch(t, withDirs.Matches, "folder/")
	requireDirectoryMatch(t, withDirs.Matches, "empty/")

	filesOnly := findFilesMode(t, h, dir, "", "skip")
	for _, match := range filesOnly.Matches {
		if match.IsDir || strings.HasSuffix(match.Path, "/") {
			t.Fatalf("directory cache poisoned file-only results: %+v", filesOnly.Matches)
		}
	}
	if !hasPath(filesOnly.Matches, "folder/file.txt") {
		t.Errorf("file-only listing missing file: %+v", filesOnly.Matches)
	}

	// The independently cached directory mode remains intact after file-only.
	again := findFilesWithDirs(t, h, dir, "empty", "skip")
	requireDirectoryMatch(t, again.Matches, "empty/")

	if findFilesCacheKey(dir, false) == findFilesCacheKey(dir, true) {
		t.Fatal("file-only and directory-inclusive cache keys collide")
	}
	if len(h.server.fileListCache.entries) != 2 {
		t.Fatalf("cache entries=%d, want one per mode", len(h.server.fileListCache.entries))
	}
}

func TestFindFilesExplicitDirectoryPaths(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	dir := t.TempDir()
	mustGitInit(t, dir)
	folder := filepath.Join(dir, "My café Folder")
	writeFile(t, filepath.Join(folder, "child.txt"), "x\n")
	if err := os.Mkdir(filepath.Join(folder, "empty child"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("without_slash_pins_folder_in_parent", func(t *testing.T) {
		resp := findFilesWithDirs(t, h, dir, "./My café Folder", "skip")
		if resp.SearchDir != dir || resp.MatchQuery != "My café Folder" {
			t.Fatalf("search_dir=%q match_query=%q", resp.SearchDir, resp.MatchQuery)
		}
		if len(resp.Matches) == 0 || resp.Matches[0].Path != "My café Folder/" || !resp.Matches[0].IsDir {
			t.Fatalf("expected pinned folder first, got %+v", resp.Matches)
		}
	})

	t.Run("trailing_slash_browses_contents", func(t *testing.T) {
		resp := findFilesWithDirs(t, h, dir, "./My café Folder/", "skip")
		if resp.SearchDir != folder || resp.MatchQuery != "" {
			t.Fatalf("search_dir=%q match_query=%q", resp.SearchDir, resp.MatchQuery)
		}
		if hasPath(resp.Matches, "My café Folder/") {
			t.Errorf("folder itself should not appear while browsing: %+v", resp.Matches)
		}
		requireDirectoryMatch(t, resp.Matches, "empty child/")
		if !hasPath(resp.Matches, "child.txt") {
			t.Errorf("browsed file missing: %+v", resp.Matches)
		}
	})

	t.Run("bare_folder_is_still_fuzzy", func(t *testing.T) {
		resp := findFilesWithDirs(t, h, dir, "café", "skip")
		if resp.SearchDir != dir {
			t.Fatalf("search_dir=%q, want %q", resp.SearchDir, dir)
		}
		match := requireDirectoryMatch(t, resp.Matches, "My café Folder/")
		for _, index := range match.MatchedIndexes {
			if index < 0 || index >= len([]rune(match.Path)) {
				t.Fatalf("directory match index %d out of bounds for %q", index, match.Path)
			}
		}
	})

	t.Run("ignored_explicit_folder_is_pinned", func(t *testing.T) {
		ignored := filepath.Join(dir, "private notes")
		if err := os.Mkdir(ignored, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, ".gitignore"), "private notes/\n")
		resp := findFilesWithDirs(t, h, dir, ignored, "skip")
		if len(resp.Matches) == 0 || resp.Matches[0].Path != "private notes/" || !resp.Matches[0].IsDir {
			t.Fatalf("expected ignored folder pin, got %+v", resp.Matches)
		}
	})

	t.Run("exact_folder_pin_bypasses_stale_listing", func(t *testing.T) {
		// Warm the parent listing before the empty folder exists. Exact-path
		// pinning must still offer it during the cache TTL.
		findFilesWithDirs(t, h, dir, "", "skip")
		fresh := filepath.Join(dir, "Fresh Empty Ω")
		if err := os.Mkdir(fresh, 0o755); err != nil {
			t.Fatal(err)
		}
		resp := findFilesWithDirs(t, h, dir, fresh, "skip")
		if len(resp.Matches) == 0 || resp.Matches[0].Path != "Fresh Empty Ω/" || !resp.Matches[0].IsDir {
			t.Fatalf("expected stale-cache folder pin, got %+v", resp.Matches)
		}
	})
}

func TestFindFilesIncludeDirsValidationAndContentOnly(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	dir := t.TempDir()
	mustGitInit(t, dir)
	writeFile(t, filepath.Join(dir, "docs", "match.txt"), "unique-folder-content\n")
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("invalid_parameter", func(t *testing.T) {
		u := "/api/find-files?dir=" + url.QueryEscape(dir) + "&include_dirs=maybe"
		w := httptest.NewRecorder()
		h.server.handleFindFiles(w, httptest.NewRequest(http.MethodGet, u, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("content_only_stays_file_only", func(t *testing.T) {
		resp := findFilesWithDirs(t, h, dir, "unique-folder-content", "only")
		if len(resp.Matches) != 1 || resp.Matches[0].Path != "docs/match.txt" {
			t.Fatalf("unexpected content-only results: %+v", resp.Matches)
		}
		if resp.Matches[0].IsDir {
			t.Fatalf("content-only result marked directory: %+v", resp.Matches[0])
		}
	})

	t.Run("content_only_keeps_directory_browse_semantics", func(t *testing.T) {
		resp := findFilesWithDirs(t, h, dir, "./docs", "only")
		if resp.SearchDir != filepath.Join(dir, "docs") || resp.MatchQuery != "" {
			t.Fatalf("search_dir=%q match_query=%q", resp.SearchDir, resp.MatchQuery)
		}
		if len(resp.Matches) != 0 {
			t.Fatalf("empty content query should return no rows: %+v", resp.Matches)
		}
	})
}

func TestFindFilesDirectoriesWithSubmodule(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)

	subSource := t.TempDir()
	mustGitInit(t, subSource)
	writeFile(t, filepath.Join(subSource, "nested", "file.txt"), "x\n")
	for _, args := range [][]string{{"add", "nested/file.txt"}, {"commit", "-m", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = subSource
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	dir := t.TempDir()
	mustGitInit(t, dir)
	if err := os.Mkdir(filepath.Join(dir, "visible-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-c", "protocol.file.allow=always", "submodule", "add", subSource, "modules/demo")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git submodule add: %v: %s", err, out)
	}
	moduleDir := filepath.Join(dir, "modules", "demo")
	if err := os.Mkdir(filepath.Join(moduleDir, "nested", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	resp := findFilesWithDirs(t, h, dir, "", "skip")
	requireDirectoryMatch(t, resp.Matches, "visible-empty/")
	requireDirectoryMatch(t, resp.Matches, "modules/")
	requireDirectoryMatch(t, resp.Matches, "modules/demo/")
	if hasPath(resp.Matches, "modules/demo") {
		t.Errorf("gitlink leaked as a file-shaped result: %+v", resp.Matches)
	}
	if countPath(resp.Matches, "modules/demo/") != 1 {
		t.Errorf("gitlink directory was duplicated: %+v", resp.Matches)
	}
	seen := make(map[string]struct{}, len(resp.Matches))
	for _, match := range resp.Matches {
		seen[match.Path] = struct{}{}
	}
	if len(seen) != len(resp.Matches) || resp.Total != len(seen) {
		t.Errorf("matches=%d unique=%d total=%d: %+v", len(resp.Matches), len(seen), resp.Total, resp.Matches)
	}
	if hasPath(resp.Matches, "modules/demo/nested/") {
		t.Errorf("superproject search should stop at submodule boundary: %+v", resp.Matches)
	}

	browsed := findFilesWithDirs(t, h, dir, moduleDir+"/", "skip")
	if browsed.SearchDir != moduleDir {
		t.Fatalf("search_dir=%q, want submodule %q", browsed.SearchDir, moduleDir)
	}
	requireDirectoryMatch(t, browsed.Matches, "nested/")
	requireDirectoryMatch(t, browsed.Matches, "nested/empty/")
	if !hasPath(browsed.Matches, "nested/file.txt") {
		t.Errorf("submodule file missing after re-root: %+v", browsed.Matches)
	}
}

func TestFindFilesUntrackedNestedRepoIsOneDirectory(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	dir := t.TempDir()
	mustGitInit(t, dir)
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGitInit(t, nested)
	writeFile(t, filepath.Join(nested, "inside.txt"), "x\n")

	resp := findFilesWithDirs(t, h, dir, "", "skip")
	if hasPath(resp.Matches, "nested") {
		t.Errorf("nested repo leaked as a file-shaped result: %+v", resp.Matches)
	}
	requireDirectoryMatch(t, resp.Matches, "nested/")
	if countPath(resp.Matches, "nested/") != 1 || resp.Total != 1 {
		t.Fatalf("expected one directory candidate, total=%d matches=%+v", resp.Total, resp.Matches)
	}

	// File-only mode preserves git's historical path listing but must not add
	// directory metadata merely because Git printed the path with a slash.
	filesOnly := findFilesMode(t, h, dir, "", "skip")
	if len(filesOnly.Matches) != 1 || filesOnly.Matches[0].Path != "nested/" || filesOnly.Matches[0].IsDir {
		t.Fatalf("file-only nested repo shape changed: %+v", filesOnly.Matches)
	}
}

func TestCombineGitFilesAndDirectories(t *testing.T) {
	t.Parallel()
	paths, truncated := combineGitFilesAndDirectories(
		[]string{"file.txt", "file.txt", "modules/demo", "nested/"},
		[]string{"modules/", "modules/demo/", "nested/", "nested/"},
		findFilesMaxCandidates,
	)
	want := []string{"file.txt", "modules/", "modules/demo/", "nested/"}
	if !slices.Equal(paths, want) || truncated {
		t.Fatalf("paths=%v truncated=%v, want %v false", paths, truncated, want)
	}

	paths, truncated = combineGitFilesAndDirectories([]string{"a", "b"}, []string{"c/"}, 2)
	if !slices.Equal(paths, []string{"a", "b"}) || !truncated {
		t.Fatalf("bounded paths=%v truncated=%v", paths, truncated)
	}
}

func TestListGitDirectoriesSmallRemainingBudget(t *testing.T) {
	t.Parallel()
	t.Run("ignored_candidates_do_not_consume_slots", func(t *testing.T) {
		dir := t.TempDir()
		mustGitInit(t, dir)
		writeFile(t, filepath.Join(dir, ".gitignore"), "aaa-ignored/\n")
		if err := os.Mkdir(filepath.Join(dir, "aaa-ignored"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "zzz-visible"), 0o755); err != nil {
			t.Fatal(err)
		}
		dirs, _, ok := listGitDirectories(dir, nil, 1)
		if !ok || len(dirs) != 1 || dirs[0] != "zzz-visible/" {
			t.Fatalf("dirs=%v ok=%v, want visible directory", dirs, ok)
		}
	})

	t.Run("tracked_ignored_ancestor_is_reachable", func(t *testing.T) {
		dir := t.TempDir()
		mustGitInit(t, dir)
		writeFile(t, filepath.Join(dir, ".gitignore"), "ignored/\n")
		writeFile(t, filepath.Join(dir, "ignored", "nested", "tracked.txt"), "x\n")
		dirs, _, ok := listGitDirectories(dir, []string{"ignored/nested/tracked.txt"}, 1)
		if !ok || len(dirs) != 1 || dirs[0] != "ignored/" {
			t.Fatalf("dirs=%v ok=%v, want tracked ignored ancestor", dirs, ok)
		}
	})
}

func TestFindFilesDirectoryWalkBoundsAndSymlinks(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup differs on Windows")
	}
	h := NewTestHarness(t)
	dir := t.TempDir()
	external := t.TempDir()
	writeFile(t, filepath.Join(external, "outside.txt"), "x\n")
	if err := os.Symlink(external, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}

	parts := make([]string, 0, findFilesWalkDepth+2)
	current := dir
	for i := 0; i < findFilesWalkDepth+2; i++ {
		name := "level" + strings.Repeat("x", i)
		parts = append(parts, name)
		current = filepath.Join(current, name)
		if err := os.Mkdir(current, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	resp := findFilesWithDirs(t, h, dir, "", "skip")
	if hasPath(resp.Matches, "linked/") || hasPath(resp.Matches, "linked/outside.txt") {
		t.Errorf("symlink was traversed or offered as a directory: %+v", resp.Matches)
	}
	atLimit := strings.Join(parts[:findFilesWalkDepth], "/") + "/"
	beyond := strings.Join(parts[:findFilesWalkDepth+1], "/") + "/"
	requireDirectoryMatch(t, resp.Matches, atLimit)
	if hasPath(resp.Matches, beyond) {
		t.Errorf("walk exceeded depth bound with %q", beyond)
	}
}
