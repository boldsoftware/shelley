package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sahilm/fuzzy"
)

const (
	// findFilesDefaultLimit caps how many matches we return by default.
	findFilesDefaultLimit = 100
	// findFilesMaxLimit is the hard ceiling on the requested limit.
	findFilesMaxLimit = 500
	// findFilesMaxCandidates bounds how many files we hold in memory /
	// fuzzy-match against, so a giant non-git tree can't blow up the server.
	findFilesMaxCandidates = 50000
	// findFilesWalkDepth bounds filesystem walk recursion for non-git dirs.
	findFilesWalkDepth = 12
	// fileListCacheTTL keeps a directory's file list warm across the burst
	// of requests that arrive while the user types a query.
	fileListCacheTTL = 5 * time.Second
	// fileListCacheMaxDirs caps how many distinct directories the cache
	// retains, so varied dir values can't grow the map without bound.
	fileListCacheMaxDirs = 64
	// fileListCacheMaxFiles caps the total paths the cache retains across all
	// directories. A typed path re-roots the search per keystroke, so the
	// directory count alone doesn't bound memory: a few 50k-file listings are
	// worth far more than dozens of small ones.
	fileListCacheMaxFiles = 200000
	// findFilesWalkBudget bounds the time spent listing a directory.
	findFilesWalkBudget = 3 * time.Second
	// findFilesGrepBudget bounds the time spent content-searching a repo with
	// `git grep`. Same order as the walk budget: long enough for a big repo's
	// cold cache, short enough that a keystroke never feels hung.
	findFilesGrepBudget = 3 * time.Second
	// findFilesGrepMaxEntries caps how many grep records we parse. `git grep`
	// prints at most one line per file (--max-count=1) but a broad term in a
	// huge repo can still hit most files, and only `limit` of them can ever be
	// shown; a generous cap bounds the parse without changing what users see.
	findFilesGrepMaxEntries = 5000
	// grepMaxPathBytes caps the path field of a parsed grep record. Real
	// repository paths are far shorter (PATH_MAX is 4096 on Linux); anything
	// longer is garbage we skip rather than buffer.
	grepMaxPathBytes = 4096
	// grepMaxLineNumBytes caps the line-number field. Any real line number
	// fits in a handful of digits; a longer field means a corrupt record.
	grepMaxLineNumBytes = 32
	// grepMaxContentBytes caps how much of a matched line we read; the rest is
	// discarded unparsed. --max-count=1 limits git to one line per *file*, but
	// a single line of minified JS can be megabytes, and the snippet only ever
	// shows the first snippetMaxRunes anyway. A term that occurs only beyond
	// this cap simply doesn't highlight: buildSnippet already tolerates a line
	// containing no term occurrence (git's -i folding can exceed ASCII's).
	grepMaxContentBytes = 4096
	// snippetMaxRunes caps the length of a content-match snippet. Measured in
	// runes so multi-byte content is never split mid-character.
	snippetMaxRunes = 160
	// snippetMatchLeadRunes is how far into the line the first term hit may
	// sit before the snippet is windowed to bring it into view.
	snippetMatchLeadRunes = 48
	// snippetContextRunes is how many runes of leading context a windowed
	// snippet keeps before the first term hit.
	snippetContextRunes = 16
)

// fileListCache memoizes the (relatively expensive) directory file listing so
// that the stream of queries produced while a user types only lists the tree
// once every fileListCacheTTL.
type fileListCache struct {
	mu      sync.Mutex
	entries map[string]fileListCacheEntry
	// files is the total len(entry.files) across entries, kept in step with
	// the map so eviction doesn't have to re-count.
	files int
}

type fileListCacheEntry struct {
	files     []string
	truncated bool
	computed  time.Time
}

func newFileListCache() *fileListCache {
	return &fileListCache{entries: make(map[string]fileListCacheEntry)}
}

func findFilesCacheKey(dir string, includeDirs bool) string {
	if includeDirs {
		// NUL cannot occur in a filesystem path, so modes cannot collide.
		return dir + "\x00dirs"
	}
	return dir
}

// get returns the cached file list for dir, computing it via load when the
// entry is missing or stale. load reports ok=false when the listing failed or
// was cut short; such results are returned to this caller but NOT cached, so a
// transient failure can't poison the entry for the full TTL.
func (c *fileListCache) get(dir string, load func() (files []string, truncated, ok bool)) ([]string, bool) {
	c.mu.Lock()
	if e, ok := c.entries[dir]; ok && time.Since(e.computed) < fileListCacheTTL {
		c.mu.Unlock()
		return e.files, e.truncated
	}
	c.mu.Unlock()

	files, truncated, ok := load()
	if !ok {
		return files, truncated
	}

	c.mu.Lock()
	c.deleteLocked(dir)
	c.entries[dir] = fileListCacheEntry{files: files, truncated: truncated, computed: time.Now()}
	c.files += len(files)
	c.evictLocked(dir)
	c.mu.Unlock()
	return files, truncated
}

// deleteLocked removes one entry, keeping the file count in step.
// Callers must hold c.mu.
func (c *fileListCache) deleteLocked(dir string) {
	e, ok := c.entries[dir]
	if !ok {
		return
	}
	c.files -= len(e.files)
	delete(c.entries, dir)
}

// evictLocked drops stale entries and then, while the cache is over either
// cap, the oldest remaining one. keep is never evicted: it's the entry the
// caller just computed and will want again on the next keystroke, and on a
// huge directory it can exceed the file cap by itself. Callers must hold c.mu.
func (c *fileListCache) evictLocked(keep string) {
	for k, e := range c.entries {
		if time.Since(e.computed) >= fileListCacheTTL {
			c.deleteLocked(k)
		}
	}
	for len(c.entries) > fileListCacheMaxDirs || c.files > fileListCacheMaxFiles {
		var oldestKey string
		var oldest time.Time
		for k, e := range c.entries {
			if k == keep {
				continue
			}
			if oldestKey == "" || e.computed.Before(oldest) {
				oldestKey, oldest = k, e.computed
			}
		}
		if oldestKey == "" {
			return // only `keep` is left
		}
		c.deleteLocked(oldestKey)
	}
}

// FindFilesMatch is a single ranked file or directory match.
type FindFilesMatch struct {
	// Path is relative to the response's SearchDir. Directory paths end in "/".
	Path string `json:"path"`
	// IsDir distinguishes directory suggestions from files.
	IsDir bool `json:"is_dir,omitempty"`
	// MatchedIndexes are rune (code-point) offsets into Path that matched the
	// query, used by the UI to highlight the fuzzy match.
	MatchedIndexes []int `json:"matched_indexes,omitempty"`
	// Line is the 1-based line number of the content match (0 when the match is by name only).
	Line int `json:"line,omitempty"`
	// Snippet is a trimmed excerpt of the matching line for content matches.
	Snippet string `json:"snippet,omitempty"`
	// SnippetMatchedIndexes are rune offsets into Snippet to highlight.
	SnippetMatchedIndexes []int `json:"snippet_matched_indexes,omitempty"`
}

// FindFilesResponse is the response from /api/find-files.
type FindFilesResponse struct {
	// Dir is the resolved working directory the request asked about.
	Dir string `json:"dir"`
	// SearchDir is the directory Matches are relative to. It differs from Dir
	// when the query was itself a path (see resolvePathQuery), so clients must
	// join results against this rather than Dir.
	SearchDir string `json:"search_dir"`
	// Query is the query as received.
	Query string `json:"query"`
	// MatchQuery is the part of Query actually fuzzy-matched against the
	// listing: for a path query that's the trailing segment, empty when the
	// path named a directory (so Matches is the whole listing).
	MatchQuery string           `json:"match_query"`
	Matches    []FindFilesMatch `json:"matches"`
	// Total counts the candidates behind Matches before the limit was applied:
	// the size of the directory listing in name-search modes, the number of
	// grep hits in content=only mode.
	Total     int  `json:"total"`
	Truncated bool `json:"truncated"`
}

// handleFindFiles fuzzy-searches files under a working directory. The query
// is matched server-side (via github.com/sahilm/fuzzy) so the client never
// needs the full file list. Files are enumerated with `git ls-files`
// (tracked + untracked, honoring .gitignore) when dir is inside a repo, else
// via a bounded filesystem walk.
//
// The `content` parameter selects how file *contents* participate (see
// grepContent), so the UI can split the two phases into parallel requests and
// paint name matches without waiting for a grep of the whole repo:
//
//   - "skip": names only — no grep, no Line/Snippet on any match. The fast
//     path (a listing is ~10x quicker than a grep, more when caches are cold).
//   - "only": contents only — no listing, no fuzzy matching, no pin. Matches
//     are the grep hits sorted by path, each carrying Line/Snippet and never
//     MatchedIndexes (the path didn't fuzzy-match anything in this mode).
//     Path queries still re-root searchDir exactly as in the other modes, so
//     the two phases of one keystroke always grep and list the same tree.
//   - "merge" or absent: both, in one response — name matches first (with
//     snippets attached where the grep also hit), then content-only matches.
//     Kept for API compatibility and single-request callers; the grep runs
//     concurrently with the listing+fuzzy phase, so its cost is max() rather
//     than sum().
func (s *Server) handleFindFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	contentMode := r.URL.Query().Get("content")
	switch contentMode {
	case "", "merge", "skip", "only":
	default:
		http.Error(w, "invalid content parameter "+strconv.Quote(contentMode)+": want \"merge\", \"skip\", or \"only\"", http.StatusBadRequest)
		return
	}

	includeDirs := false
	if values, present := r.URL.Query()["include_dirs"]; present {
		if len(values) != 1 {
			http.Error(w, "invalid include_dirs parameter", http.StatusBadRequest)
			return
		}
		var err error
		includeDirs, err = strconv.ParseBool(values[0])
		if err != nil {
			http.Error(w, "invalid include_dirs parameter "+strconv.Quote(values[0])+": want a boolean", http.StatusBadRequest)
			return
		}
	}
	// Content-only results are grep hits, which are always files. Keeping the
	// path-query and cache behavior file-only also makes this mode identical
	// whether or not a caller happens to pass include_dirs.
	includeDirs = includeDirs && contentMode != "only"

	dir := r.URL.Query().Get("dir")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home
		} else {
			dir = "/"
		}
	}
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		http.Error(w, "absolute dir required", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		http.Error(w, "not a directory", http.StatusBadRequest)
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := findFilesDefaultLimit
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	if limit > findFilesMaxLimit {
		limit = findFilesMaxLimit
	}

	// A query that is itself a path re-roots the search at the directory it
	// names, so a user who knows where a file lives can type its path instead
	// of hunting for it from a working directory it isn't under.
	pq := resolvePathQuery(query, dir)
	searchDir, matchQuery := dir, query
	if pq.IsPath {
		searchDir, matchQuery = pq.Dir, pq.Tail
	}
	// Folder completion treats an explicitly named existing directory without
	// a trailing slash as the item being selected. Search its parent so the
	// directory itself can be offered and pinned. A trailing slash still means
	// browse inside it. File-only requests retain the original browse behavior.
	if includeDirs && pq.DirectoryPath != "" {
		searchDir, matchQuery = filepath.Dir(pq.DirectoryPath), filepath.Base(pq.DirectoryPath)
	}

	if contentMode == "only" {
		// Content-only phase: no listing, no fuzzy matching, no pin — the name
		// phase (a parallel content=skip request) owns all of that, and the UI
		// joins the two by path. Skipping the fileListCache here matters: this
		// request races the name request per keystroke, and contending on the
		// cache's load would serialize exactly what splitting parallelized.
		s.writeFindFilesContentOnly(w, r, dir, searchDir, query, matchQuery, limit)
		return
	}

	// Start the grep before the listing so the two run concurrently: measured
	// on a warm mid-size repo the listing takes ~35ms and the grep ~235ms, so
	// sequencing them charges the sum where max() is available for free. The
	// nil channel doubles as "no grep": skip mode and empty queries (grep of
	// nothing) never start the goroutine, and receiving from grepDone below
	// only happens when it's non-nil.
	var hits map[string]contentHit
	var grepDone chan struct{}
	if contentMode != "skip" && matchQuery != "" {
		grepDone = make(chan struct{})
		go func() {
			defer close(grepDone)
			hits = grepContent(r.Context(), searchDir, dedupeFold(strings.Fields(matchQuery)))
		}()
	}

	var files []string
	var listTruncated bool
	if !pq.IsPath || pq.DirExists {
		cacheKey := findFilesCacheKey(searchDir, includeDirs)
		files, listTruncated = s.fileListCache.get(cacheKey, func() (files []string, truncated, ok bool) {
			return listWorkingDirPaths(searchDir, includeDirs)
		})
	}

	resp := FindFilesResponse{
		Dir:        dir,
		SearchDir:  searchDir,
		Query:      query,
		MatchQuery: matchQuery,
		Total:      len(files),
		Truncated:  listTruncated,
		Matches:    []FindFilesMatch{},
	}

	if matchQuery == "" {
		// No pattern: return the first `limit` files in alphabetical order so
		// the picker has something to show immediately when it opens.
		sorted := append([]string(nil), files...)
		sort.Strings(sorted)
		if len(sorted) > limit {
			sorted = sorted[:limit]
			resp.Truncated = true
		}
		for _, p := range sorted {
			resp.Matches = append(resp.Matches, matchForPath(p, includeDirs))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	matches := findFuzzyMulti(matchQuery, files)
	if len(matches) > limit {
		matches = matches[:limit]
		resp.Truncated = true
	}
	for _, m := range matches {
		match := matchForPath(m.str, includeDirs)
		match.MatchedIndexes = byteToRuneOffsets(m.str, m.matchedIndexes)
		resp.Matches = append(resp.Matches, match)
	}
	// "Universal find": inside a git repo the same terms also match file
	// *contents* via `git grep`, so a query can locate a file the user only
	// remembers a line from. Name matches that also hit on content gain a
	// snippet (useful context either way); content-only hits are appended
	// after every name match, sorted by path, because a name match is what
	// the picker's primary affordance (the highlighted path) promises.
	// grepContent returns nothing outside a repo, so non-repo dirs are
	// untouched, and in skip mode grepDone is nil so hits stays nil.
	if grepDone != nil {
		<-grepDone
	}
	if len(hits) > 0 {
		named := make(map[string]bool, len(resp.Matches))
		for i := range resp.Matches {
			m := &resp.Matches[i]
			named[m.Path] = true
			if h, ok := hits[m.Path]; ok {
				m.Line, m.Snippet, m.SnippetMatchedIndexes = h.line, h.snippet, h.matchedIndexes
			}
		}
		contentOnly := make([]string, 0, len(hits))
		for p := range hits {
			if !named[p] {
				contentOnly = append(contentOnly, p)
			}
		}
		sort.Strings(contentOnly) // map order is random; sort for determinism
		for _, p := range contentOnly {
			if len(resp.Matches) >= limit {
				resp.Truncated = true
				break
			}
			h := hits[p]
			// No MatchedIndexes: the path itself didn't match the query, so
			// there is nothing in it to highlight.
			resp.Matches = append(resp.Matches, FindFilesMatch{
				Path:                  p,
				Line:                  h.line,
				Snippet:               h.snippet,
				SnippetMatchedIndexes: h.matchedIndexes,
			})
		}
	}
	// A query naming an existing file always offers that file, even when the
	// listing can't surface it (`git ls-files` hides .gitignore'd files, and a
	// walk stops at its budget): typing the path is an unambiguous request.
	if rel, ok := relativeTo(searchDir, pq.FilePath); ok {
		var dropped bool
		resp.Matches, dropped = pinMatch(resp.Matches, rel, limit)
		resp.Truncated = resp.Truncated || dropped
		// A pinned file absent from the listing (that's what pinning is for)
		// was fabricated bare by pinMatch; if the grep found content in it,
		// attach the snippet so the pin is as informative as any other row.
		// An entry promoted from the existing matches already carries its
		// snippet and must keep it (hits lacks paths beyond the entry cap).
		if pin := &resp.Matches[0]; pin.Snippet == "" && pin.Line == 0 {
			if h, ok := hits[pin.Path]; ok {
				pin.Line, pin.Snippet, pin.SnippetMatchedIndexes = h.line, h.snippet, h.matchedIndexes
			}
		}
	}
	if includeDirs {
		if rel, ok := relativeTo(searchDir, pq.DirectoryPath); ok {
			var dropped bool
			resp.Matches, dropped = pinMatch(resp.Matches, rel+"/", limit)
			resp.Truncated = resp.Truncated || dropped
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// writeFindFilesContentOnly answers a content=only request: grep hits alone,
// sorted by path ascending for determinism (grep output order is walk order,
// which is not contractual), capped at limit. Total counts the hits before
// the cap so the UI can say "N files matched" even when it shows fewer. No
// pinning: the parallel name-phase request pins, and pinning here too would
// duplicate the row when the UI concatenates the two responses. An empty
// matchQuery greps nothing (grepContent refuses zero terms) rather than
// devolving into "list everything", which is the name phase's job; a non-repo
// searchDir likewise yields no hits.
func (s *Server) writeFindFilesContentOnly(w http.ResponseWriter, r *http.Request, dir, searchDir, query, matchQuery string, limit int) {
	hits := grepContent(r.Context(), searchDir, dedupeFold(strings.Fields(matchQuery)))

	resp := FindFilesResponse{
		Dir:        dir,
		SearchDir:  searchDir,
		Query:      query,
		MatchQuery: matchQuery,
		Total:      len(hits),
		Matches:    []FindFilesMatch{},
	}
	paths := make([]string, 0, len(hits))
	for p := range hits {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) > limit {
		paths = paths[:limit]
		resp.Truncated = true
	}
	for _, p := range paths {
		h := hits[p]
		// Never MatchedIndexes: in this mode the path wasn't fuzzy-matched
		// against anything, so there is no path highlight to report.
		resp.Matches = append(resp.Matches, FindFilesMatch{
			Path:                  p,
			Line:                  h.line,
			Snippet:               h.snippet,
			SnippetMatchedIndexes: h.matchedIndexes,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func matchForPath(path string, includeDirs bool) FindFilesMatch {
	return FindFilesMatch{Path: path, IsDir: includeDirs && strings.HasSuffix(path, "/")}
}

// pinMatch puts path at the head of matches, dropping any existing entry for
// it (whose highlights it keeps) and re-applying limit. dropped reports
// whether trimming to limit discarded a match the caller had included.
func pinMatch(matches []FindFilesMatch, path string, limit int) (out []FindFilesMatch, dropped bool) {
	pin := FindFilesMatch{Path: path, IsDir: strings.HasSuffix(path, "/")}
	out = make([]FindFilesMatch, 1, len(matches)+1)
	for _, m := range matches {
		if m.Path == path {
			pin = m
			continue
		}
		out = append(out, m)
	}
	out[0] = pin
	if len(out) > limit {
		return out[:limit], true
	}
	return out, false
}

// pathQuery is how a finder query reads as a filesystem path.
type pathQuery struct {
	// IsPath is true when the query announced itself as a path ("/", "~",
	// "./", "../"). The search re-roots to Dir and fuzzy-matches Tail in it.
	IsPath bool
	// DirExists says whether Dir is a readable directory. A path query naming
	// a directory that isn't there must find nothing rather than fall back to
	// a fuzzy search, which would answer with unrelated files.
	DirExists bool
	// Dir is the directory to search and Tail the pattern to match in it.
	// Tail is empty when the query named the directory itself, meaning "list
	// all of it".
	Dir, Tail string
	// FilePath is the existing regular file the query names outright, empty
	// when it names no such file. Set for ordinary queries too: a bare
	// "notes.md" that exists in the working directory is one.
	FilePath string
	// DirectoryPath is an existing directory named by an explicit path without
	// a trailing slash. Folder searches use it to offer the directory itself
	// from its parent; file-only searches continue to browse inside Dir.
	DirectoryPath string
}

// resolvePathQuery reads a query as a filesystem path.
//
// Only an explicit prefix — "/", "~", "./", "../" — makes a query a path.
// An embedded slash does not: "docs/vm-storage" is the finder's ordinary
// partial-path idiom, matched against the whole tree, and re-rooting it at
// ./docs would silently hide every match outside that one directory.
//
// A path naming an existing directory searches all of it (empty Tail);
// otherwise its last segment is the pattern for its parent. A path stays a
// path even when its directory is missing (DirExists false), because a
// half-typed "/nonexistent/xyz" has no useful fuzzy reading.
func resolvePathQuery(query, dir string) pathQuery {
	path, ok := expandQueryPath(query, dir)
	if !ok {
		return pathQuery{}
	}
	// A trailing slash asserts a directory: "/tmp/" means "inside /tmp", never
	// "files named tmp in /", and never an editable file.
	trailingSlash := strings.HasSuffix(query, "/")
	info, err := os.Stat(path)

	pq := pathQuery{Dir: dir}
	if err == nil && !trailingSlash && info.Mode().IsRegular() {
		// Worth pinning even for a plain fuzzy query: a bare "notes.md" that
		// exists in the working directory is unambiguously that file.
		pq.FilePath = path
	}
	if !isPathish(query) {
		// Not a path, even if it happens to name a real directory: "shelley"
		// at a repo root is a fuzzy pattern, and re-rooting into that
		// directory would replace every match elsewhere with its listing.
		return pq
	}

	pq.IsPath = true
	if err == nil && info.IsDir() {
		pq.Dir, pq.DirExists = path, true
		// A filesystem root has no parent scope in which it can be offered.
		if !trailingSlash && filepath.Dir(path) != path {
			pq.DirectoryPath = path
		}
		return pq
	}
	if trailingSlash {
		// A directory that isn't there (yet): search it and find nothing,
		// rather than pretend the text was a fuzzy pattern.
		pq.Dir = path
		return pq
	}
	pq.Dir, pq.Tail = filepath.Dir(path), filepath.Base(path)
	if parent, err := os.Stat(pq.Dir); err == nil && parent.IsDir() {
		pq.DirExists = true
	}
	return pq
}

// isPathish reports whether a query announces itself as a filesystem path
// rather than a fuzzy pattern. Only a leading "/", "~", "./" or "../" does;
// see resolvePathQuery for why an embedded slash isn't enough.
func isPathish(query string) bool {
	return query == "~" ||
		strings.HasPrefix(query, "/") ||
		strings.HasPrefix(query, "~/") ||
		strings.HasPrefix(query, "./") ||
		strings.HasPrefix(query, "../")
}

// expandQueryPath reads a query as a filesystem path: ~-rooted paths expand
// against $HOME, absolute ones are taken as-is, and the rest resolve against
// the working directory dir. The result is cleaned lexically (as filepath.Join
// does) and not checked against the filesystem. ok is false when the query
// can't name a path at all: empty, whitespace-bearing without a path prefix
// (a multi-term fuzzy query like "vm storage s3", which no quoting syntax here
// tells apart from a path with spaces), $HOME unknown, or a relative query
// with no absolute directory to root it in.
func expandQueryPath(query, dir string) (path string, ok bool) {
	if query == "" {
		return "", false
	}
	if !isPathish(query) && strings.ContainsAny(query, " \t") {
		return "", false
	}
	switch {
	case query == "~" || strings.HasPrefix(query, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		return filepath.Join(home, strings.TrimPrefix(query[1:], "/")), true
	case filepath.IsAbs(query):
		return filepath.Clean(query), true
	default:
		if !filepath.IsAbs(dir) {
			return "", false
		}
		return filepath.Join(dir, query), true
	}
}

// relativeTo expresses path relative to base. ok is false when path is empty
// or falls outside base, neither of which the finder can offer as a match.
func relativeTo(base, path string) (rel string, ok bool) {
	if path == "" {
		return "", false
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// fuzzyMatch is one ranked path plus the byte offsets that matched. rounds
// counts how many query terms have matched it so far (see findFuzzyMulti).
type fuzzyMatch struct {
	str            string
	matchedIndexes []int
	score          int
	rounds         int
}

// findFuzzyMulti fuzzy-matches query against files. A query with whitespace is
// split into terms that are ANDed together: every term must fuzzy-match the
// path, in any order, and their highlights are unioned. That makes
// "vm storage s3" find "docs/vm-storage-s3-backup-design.md", which a single
// fuzzy pass can't do because the literal space never appears in the path.
func findFuzzyMulti(query string, files []string) []fuzzyMatch {
	// Repeated terms would otherwise be scored twice, ranking "vm vm" matches
	// above the same files found by "vm".
	terms := dedupeFold(strings.Fields(query))
	switch len(terms) {
	case 0:
		return nil
	case 1:
		raw := fuzzy.Find(terms[0], files)
		out := make([]fuzzyMatch, 0, len(raw))
		seen := make(map[string]bool, len(raw))
		for _, m := range raw {
			// `git ls-files` lists a path once per stage during an unresolved
			// merge; duplicate rows would collide on the UI list's :key.
			if seen[m.Str] {
				continue
			}
			seen[m.Str] = true
			out = append(out, fuzzyMatch{
				str:            m.Str,
				matchedIndexes: refineHighlights(m.Str, terms[0], m.MatchedIndexes),
				score:          m.Score,
			})
		}
		return out
	}

	// Intersect term by term, narrowing the candidate set each round so later
	// (typically more selective) terms only scan survivors.
	candidates := files
	acc := make(map[string]*fuzzyMatch, len(files))
	for i, term := range terms {
		raw := fuzzy.Find(term, candidates)
		next := make([]string, 0, len(raw))
		for _, m := range raw {
			prev, ok := acc[m.Str]
			if !ok {
				if i > 0 {
					continue // dropped by an earlier term
				}
				prev = &fuzzyMatch{str: m.Str}
				acc[m.Str] = prev
			} else if prev.rounds > i {
				continue // duplicate path: already scored this round
			}
			prev.matchedIndexes = append(prev.matchedIndexes, refineHighlights(m.Str, term, m.MatchedIndexes)...)
			prev.score += m.Score
			prev.rounds = i + 1
			next = append(next, m.Str)
		}
		// Drop paths that failed this term so they can't resurface later.
		for p, m := range acc {
			if m.rounds <= i {
				delete(acc, p)
			}
		}
		candidates = next
		if len(candidates) == 0 {
			return nil
		}
	}

	out := make([]fuzzyMatch, 0, len(candidates))
	for _, p := range candidates {
		m := acc[p]
		m.matchedIndexes = dedupeSorted(m.matchedIndexes)
		// fuzzy charges each call ~len(path) for the characters the term didn't
		// match, so summing N terms bills the path length N times and buries a
		// long, well-separated path ("docs/vm-storage-s3-backup-design.md")
		// under a short run-together one ("a/vmstorages3.md"). Refund the N-1
		// extra charges (the penalty is per byte of the path, matching
		// fuzzy.Find) so length is counted once, as in a single-term query.
		m.score += (len(terms) - 1) * len(p)
		out = append(out, *m)
	}
	// Best total score first; shorter paths break ties (fewer unmatched
	// characters usually means a tighter match), then path for determinism.
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if len(out[i].str) != len(out[j].str) {
			return len(out[i].str) < len(out[j].str)
		}
		return out[i].str < out[j].str
	})
	return out
}

// refineHighlights improves the highlight offsets sahilm/fuzzy reports for a
// term. Its scoring can scatter a match across the path ("vm" in
// "docs/vm-storage-s3.md" highlights the 'v' of vm and the 'm' of ".md"), which
// reads as noise. When the term occurs literally (ASCII case-insensitively) in
// the path we highlight that run instead, preferring an occurrence in the
// basename so "server" underlines server.go rather than a parent directory.
// Falls back to the library's offsets for genuine subsequence matches. Returns
// byte offsets into path, one per character (rune starts only, matching what
// fuzzy reports), for byteToRuneOffsets to convert.
func refineHighlights(path, term string, fuzzyIdx []int) []int {
	if term == "" {
		return fuzzyIdx
	}
	start := -1
	if slash := strings.LastIndexByte(path, '/'); slash >= 0 {
		if i := indexASCIIFold(path[slash+1:], term); i >= 0 {
			start = slash + 1 + i
		}
	}
	if start < 0 {
		start = indexASCIIFold(path, term)
	}
	if start < 0 {
		return fuzzyIdx
	}
	out := make([]int, 0, len(term))
	for i := range term {
		out = append(out, start+i)
	}
	return out
}

// indexASCIIFold is strings.Index with ASCII case folding. It works on the
// original bytes (rather than lowercasing first) so the returned offset stays
// valid for the input; non-ASCII bytes must match exactly.
func indexASCIIFold(s, sub string) int {
	if len(sub) == 0 || len(sub) > len(s) {
		return -1
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			if lowerASCII(s[i+j]) != lowerASCII(sub[j]) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// dedupeFold removes ASCII-case-insensitive duplicates, keeping first order.
func dedupeFold(terms []string) []string {
	if len(terms) < 2 {
		return terms
	}
	out := make([]string, 0, len(terms))
	seen := make(map[string]bool, len(terms))
	for _, t := range terms {
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}

// dedupeSorted sorts idx ascending and removes duplicates in place.
func dedupeSorted(idx []int) []int {
	if len(idx) < 2 {
		return idx
	}
	sort.Ints(idx)
	out := idx[:1]
	for _, v := range idx[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// byteToRuneOffsets converts sahilm/fuzzy's byte offsets into s to rune
// (code-point) offsets, which is what the UI needs to highlight matches via
// JS string slicing. For pure-ASCII paths this is an identity mapping. Byte
// offsets that don't fall on a rune boundary (shouldn't happen for real
// matches) are dropped.
func byteToRuneOffsets(s string, byteIdx []int) []int {
	if len(byteIdx) == 0 {
		return byteIdx
	}
	// Fast path: for pure-ASCII strings byte offset == rune offset, which is
	// the overwhelmingly common case for file paths.
	if len(s) == utf8.RuneCountInString(s) {
		return byteIdx
	}
	// Map byte offset -> rune offset by walking the string once.
	byteToRune := make(map[int]int, len(s))
	ri := 0
	for b := range s {
		byteToRune[b] = ri
		ri++
	}
	out := make([]int, 0, len(byteIdx))
	for _, b := range byteIdx {
		if r, ok := byteToRune[b]; ok {
			out = append(out, r)
		}
	}
	return out
}

// contentHit is one file-level content match found by grepContent: the first
// matching line's number, a display snippet of it, and the rune offsets into
// that snippet to highlight.
type contentHit struct {
	line           int
	snippet        string
	matchedIndexes []int
}

// grepContent finds files under dir whose contents contain every term, using
// `git grep`. Terms are matched as literal, case-insensitive strings, ANDed at
// the file level (--all-match) to mirror findFuzzyMulti's multi-term AND; the
// reported line is the file's first line matching *any* term (--max-count=1
// stops git at one line per file), which is enough context for a picker row.
// Binary files are skipped (-I), untracked-but-not-ignored files are searched
// (--untracked) to mirror what `git ls-files -co --exclude-standard` lists.
//
// The timeout derives from ctx (the HTTP request's context), so a grep dies
// with the keystroke that spawned it: the UI aborts superseded find requests
// as the user types, and without this plumbing every abandoned keystroke
// would keep a full-repo grep running for its whole budget.
//
// Returns nil on any failure — dir not in a repo, git too old for a flag,
// timeout before any output — because content search is a bonus on top of
// name search, never a reason to fail the request. `git grep` exits 1 with no
// output for "no matches", which yields no records and correctly returns nil.
func grepContent(ctx context.Context, dir string, terms []string) map[string]contentHit {
	if len(terms) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, findFilesGrepBudget)
	defer cancel()
	args := []string{"grep", "-I", "-i", "-F", "-n", "--no-color", "--max-count=1", "--untracked", "--all-match"}
	for _, t := range terms {
		args = append(args, "-e", t)
	}
	// -z NUL-terminates the path and line-number fields so a path containing
	// ":" (or even a newline — with -z git does NOT quote such paths) can't be
	// confused with the separators; the matched line itself is \n-terminated,
	// which is unambiguous because a grep-matched line cannot contain a
	// newline, and -I guarantees it contains no NUL either.
	args = append(args, "-z", "--")
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Stream rather than cmd.Output(): --max-count=1 bounds git to one line
	// per file, but one line of minified JS can be megabytes and a broad term
	// can hit most files of a huge repo, so buffering everything would let a
	// single keystroke allocate without bound. Reading incrementally also lets
	// us stop at findFilesGrepMaxEntries and kill git mid-stream.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil
	}

	hits := make(map[string]contentHit)
	r := bufio.NewReader(stdout)
	for len(hits) < findFilesGrepMaxEntries {
		// Each record is path\x00linenum\x00content\n. Parse the stream
		// sequentially — never by splitting on \n first, because the *path*
		// may legitimately contain a newline, and splitting would shear such a
		// record into a phantom hit for a file that doesn't exist. Skip
		// malformed or oversized records rather than guessing: one bad row
		// shouldn't kill the rest.
		path, pathOK, err := readField(r, 0, grepMaxPathBytes)
		if err != nil {
			break // clean EOF between records, or a read error: done either way
		}
		lineField, lineOK, err := readField(r, 0, grepMaxLineNumBytes)
		if err != nil {
			break
		}
		content, contentFit, err := readField(r, '\n', grepMaxContentBytes)
		if err != nil {
			break // a record missing its terminator is unusable; so is the stream
		}
		if !pathOK || !lineOK || path == "" {
			continue
		}
		lineNum, err := strconv.Atoi(lineField)
		if err != nil || lineNum <= 0 {
			continue
		}
		// The content cap can cut a multi-byte rune in half; trim the partial
		// tail so one truncated rune doesn't disqualify the whole hit below.
		// Only a capped field can have been cut mid-rune — an intact one that
		// merely *ends* oddly is genuinely invalid and should be skipped.
		if !contentFit {
			content = trimPartialRune(content)
		}
		// Non-UTF-8 content can't be sliced into runes for the snippet (and
		// would render as replacement characters anyway), so skip it.
		if !utf8.ValidString(content) {
			continue
		}
		snippet, idx := buildSnippet(content, terms)
		hits[path] = contentHit{line: lineNum, snippet: snippet, matchedIndexes: idx}
	}
	// Kill git if it's still producing (entry cap reached) and reap it. The
	// hits parsed so far are valid regardless of how git exits — including
	// exit 1, its "no matches" status.
	cancel()
	cmd.Wait()
	return hits
}

// readField reads one delimiter-terminated field from r. delim 0 means
// NUL-terminated (how -z separates path and line number); '\n' terminates the
// content field. A field longer than maxBytes is consumed to its delimiter
// but reported ok=false with as much as fit — the caller decides whether a
// truncated value is still usable (a cut content line is; a cut path or line
// number is not). err is non-nil only when the delimiter never arrived (EOF
// or read failure), which ends the whole parse.
func readField(r *bufio.Reader, delim byte, maxBytes int) (field string, ok bool, err error) {
	var buf []byte
	total := 0 // bytes seen, including any discarded past the cap
	for {
		b, err := r.ReadByte()
		if err != nil {
			return string(buf), false, err
		}
		if b == delim {
			return string(buf), total <= maxBytes, nil
		}
		total++
		if len(buf) < maxBytes {
			buf = append(buf, b)
		}
		// Past the cap: keep discarding until the delimiter so the stream
		// stays aligned on record boundaries.
	}
}

// trimPartialRune removes an incomplete trailing UTF-8 sequence from s, as
// left behind when a byte cap cuts a line mid-rune. Only the final rune can
// have been cut, so at most the last utf8.UTFMax-1 bytes go; invalid bytes
// deeper in s are left for the caller's validity check to reject.
func trimPartialRune(s string) string {
	for i := len(s) - 1; i >= 0 && i > len(s)-utf8.UTFMax; i-- {
		if !utf8.RuneStart(s[i]) {
			continue // a continuation byte; keep looking for its start
		}
		if utf8.ValidString(s[i:]) {
			return s // the final rune is complete
		}
		return s[:i] // start byte whose sequence was cut short
	}
	// No start byte within one rune's reach of the end: either s ends with a
	// complete 4-byte rune (all continuations in view) or it's invalid in a
	// way no trim can fix; both are the caller's ValidString call to judge.
	return s
}

// buildSnippet turns a grep-matched line into a display snippet plus the rune
// offsets of the terms to highlight in it. The line is whitespace-trimmed and,
// when long, windowed so the first term hit is visible: if that hit starts
// beyond snippetMatchLeadRunes the front is cut (keeping snippetContextRunes
// of context) and marked with "…", and the whole snippet is capped at
// snippetMaxRunes with a trailing "…" when cut. All slicing is in runes so
// multi-byte content is never split mid-character.
//
// Highlights cover the first occurrence of each term that literally appears in
// the final snippet (ASCII case-insensitively, matching `git grep -i -F` for
// the ASCII terms a finder query realistically contains). The printed line is
// only guaranteed to contain *one* of a multi-term query's terms — --all-match
// ANDs at the file level — so terms that don't appear simply don't highlight.
func buildSnippet(line string, terms []string) (snippet string, matchedIndexes []int) {
	s := strings.TrimSpace(line)
	runes := []rune(s)

	// Locate the first (byte) offset where any term occurs, to anchor the
	// window. -1 when none does (possible: git's -i can fold beyond ASCII).
	firstByte := -1
	for _, t := range terms {
		if i := indexASCIIFold(s, t); i >= 0 && (firstByte < 0 || i < firstByte) {
			firstByte = i
		}
	}

	var front, back bool
	if firstByte >= 0 {
		if firstRune := utf8.RuneCountInString(s[:firstByte]); firstRune > snippetMatchLeadRunes {
			runes = runes[firstRune-snippetContextRunes:]
			front = true
		}
	}
	if len(runes) > snippetMaxRunes {
		runes = runes[:snippetMaxRunes]
		back = true
	}
	snippet = string(runes)
	if front {
		snippet = "…" + snippet
	}
	if back {
		snippet += "…"
	}

	// Highlight offsets are computed against the *final* snippet (ellipses
	// included) so the UI can use them directly. indexASCIIFold returns byte
	// offsets; convert to runes by counting the prefix. The matched region has
	// the term's exact byte layout (ASCII folding is length-preserving,
	// non-ASCII must match exactly), so its rune length is the term's.
	for _, t := range terms {
		b := indexASCIIFold(snippet, t)
		if b < 0 {
			continue
		}
		start := utf8.RuneCountInString(snippet[:b])
		for i := 0; i < utf8.RuneCountInString(t); i++ {
			matchedIndexes = append(matchedIndexes, start+i)
		}
	}
	return snippet, dedupeSorted(matchedIndexes)
}

// listWorkingDirPaths returns relative file paths and, when includeDirs is
// true, directory paths with a trailing slash. Git repositories use
// `git ls-files` for files and a separate bounded directory walk so empty
// directories are not lost; non-repositories use one bounded walk for both.
func listWorkingDirPaths(dir string, includeDirs bool) (paths []string, truncated, ok bool) {
	// An ignored directory (a node_modules or dist inside a repo) lists as
	// empty under `git ls-files`, so walk it instead: the user re-rooted the
	// search there deliberately and .gitignore has nothing left to say about
	// what's inside. A merely empty-looking directory elsewhere in the repo
	// still honors .gitignore, so its ignored files stay hidden.
	if gitFiles, isRepo := gitLsFiles(dir); isRepo && !gitIgnores(dir) {
		filesTruncated := len(gitFiles) > findFilesMaxCandidates
		if filesTruncated {
			gitFiles = gitFiles[:findFilesMaxCandidates]
		}
		if !includeDirs {
			return gitFiles, filesTruncated, true
		}
		dirs, dirsTruncated, dirsOK := listGitDirectories(dir, gitFiles, findFilesMaxCandidates)
		if !dirsOK {
			return gitFiles, true, false
		}
		paths, combinedTruncated := combineGitFilesAndDirectories(gitFiles, dirs, findFilesMaxCandidates)
		return paths, filesTruncated || dirsTruncated || combinedTruncated, true
	}
	return walkPaths(dir, includeDirs)
}

// combineGitFilesAndDirectories removes duplicate paths from Git's file list
// and the directory walk. Gitlinks are file-shaped in `git ls-files`, while
// untracked nested repositories may be printed with a trailing slash; when the
// walk confirms either is a real directory, its directory representation wins.
func combineGitFilesAndDirectories(files, dirs []string, limit int) (paths []string, truncated bool) {
	directoryBases := make(map[string]struct{}, len(dirs))
	for _, dir := range dirs {
		directoryBases[strings.TrimSuffix(dir, "/")] = struct{}{}
	}
	seen := make(map[string]struct{}, min(limit, len(files)+len(dirs)))
	add := func(path string) {
		if _, exists := seen[path]; exists {
			return
		}
		if len(paths) >= limit {
			truncated = true
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	for _, file := range files {
		if _, isDirectory := directoryBases[strings.TrimSuffix(file, "/")]; isDirectory {
			continue
		}
		add(file)
	}
	for _, dir := range dirs {
		add(dir)
	}
	return paths, truncated
}

// gitIgnores reports whether dir is itself excluded by the repo's ignore
// rules. `git check-ignore` exits 0 when the path is ignored, 1 when it isn't,
// and >1 on error; anything but a clean 0 is treated as "not ignored".
func gitIgnores(dir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), findFilesWalkBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "check-ignore", "-q", ".")
	cmd.Dir = dir
	return cmd.Run() == nil
}

// gitLsFiles lists tracked + untracked (non-ignored) files under dir using
// git. The isRepo result is false when dir is not inside a git repository (or
// git is unavailable), so the caller can fall back to a plain walk.
func gitLsFiles(dir string) (files []string, isRepo bool) {
	ctx, cancel := context.WithTimeout(context.Background(), findFilesWalkBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-files", "-co", "--exclude-standard", "-z")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	raw := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	files = make([]string, 0, len(raw))
	for _, p := range raw {
		if p != "" {
			files = append(files, p)
		}
	}
	return files, true
}

type directoryWalkNode struct {
	abs, rel string
	depth    int
}

// listGitDirectories supplements git's file list with nested and empty
// directories. Ignore checks are batched once per breadth-first level under a
// shared deadline. Parents of git-listed files are retained even when an
// ignore rule matches the directory, because tracked files remain valid.
func listGitDirectories(dir string, files []string, limit int) (dirs []string, truncated, ok bool) {
	if limit <= 0 {
		return nil, true, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), findFilesWalkBudget)
	defer cancel()

	// This set is only an ignore-rule exception list. Directories are added to
	// results by the filesystem walk, so deleted tracked paths cannot fabricate
	// directory suggestions and no per-file os.Stat calls are needed.
	mustKeep := make(map[string]struct{}, min(findFilesMaxCandidates, len(files)))
	parentsTruncated := false
	for _, file := range files {
		parent := file
		var ancestors []string
		for {
			i := strings.LastIndexByte(parent, '/')
			if i < 0 {
				break
			}
			parent = parent[:i]
			if parent == "" {
				break
			}
			ancestors = append(ancestors, parent)
		}
		// Root-first insertion ensures an ignored tracked subtree can at least
		// be reached when this auxiliary set itself hits its memory bound.
		for i := len(ancestors) - 1; i >= 0; i-- {
			parent := ancestors[i]
			if !crawlPathAllowed(parent) {
				continue
			}
			if _, exists := mustKeep[parent]; exists {
				continue
			}
			if len(mustKeep) >= findFilesMaxCandidates {
				parentsTruncated = true
				break
			}
			mustKeep[parent] = struct{}{}
		}
		if parentsTruncated {
			break
		}
	}

	seen := make(map[string]struct{}, limit)
	add := func(rel string) bool {
		if _, exists := seen[rel]; exists {
			return true
		}
		if len(seen) >= limit {
			return false
		}
		seen[rel] = struct{}{}
		return true
	}

	frontier := []directoryWalkNode{{abs: dir}}
	for len(frontier) > 0 {
		if ctx.Err() != nil {
			truncated = true
			break
		}
		remaining := limit - len(seen)
		if remaining <= 0 {
			truncated = true
			break
		}
		var candidates []directoryWalkNode
		levelTruncated := false
	collectLevel:
		for _, node := range frontier {
			entries, err := os.ReadDir(node.abs)
			if err != nil {
				continue
			}
			// The containing repository cannot check ignore rules for paths
			// inside a submodule or nested worktree (`git check-ignore` exits
			// 128). Offer the repository directory itself, then let an explicit
			// trailing-slash query re-root there and enumerate its contents.
			if node.rel != "" && hasGitMarker(entries) {
				continue
			}
			for _, entry := range entries {
				if ctx.Err() != nil {
					levelTruncated = true
					break collectLevel
				}
				name := entry.Name()
				if !entry.IsDir() || node.depth >= findFilesWalkDepth {
					continue
				}
				if _, skip := crawlSkipNames[name]; skip {
					continue
				}
				rel := name
				if node.rel != "" {
					rel = node.rel + "/" + name
				}
				if len(candidates) >= findFilesMaxCandidates {
					levelTruncated = true
					break collectLevel
				}
				candidates = append(candidates, directoryWalkNode{
					abs: filepath.Join(node.abs, name), rel: rel, depth: node.depth + 1,
				})
			}
		}
		if len(candidates) == 0 {
			truncated = truncated || levelTruncated
			break
		}

		paths := make([]string, len(candidates))
		for i, candidate := range candidates {
			paths[i] = candidate.rel + "/"
		}
		ignored, checkOK := gitCheckIgnored(ctx, dir, paths)
		if !checkOK {
			return nil, true, false
		}
		frontier = frontier[:0]
		for _, candidate := range candidates {
			_, required := mustKeep[candidate.rel]
			if _, isIgnored := ignored[candidate.rel+"/"]; isIgnored && !required {
				continue
			}
			if !add(candidate.rel) {
				levelTruncated = true
				break
			}
			frontier = append(frontier, candidate)
		}
		if levelTruncated {
			truncated = true
			break
		}
	}

	dirs = make([]string, 0, len(seen))
	for rel := range seen {
		dirs = append(dirs, rel+"/")
	}
	sort.Strings(dirs)
	return dirs, truncated || parentsTruncated, true
}

func hasGitMarker(entries []os.DirEntry) bool {
	for _, entry := range entries {
		if entry.Name() == ".git" {
			return true
		}
	}
	return false
}

func crawlPathAllowed(rel string) bool {
	for _, name := range strings.Split(rel, "/") {
		if _, skip := crawlSkipNames[name]; skip {
			return false
		}
	}
	return true
}

// gitCheckIgnored returns the supplied repo-relative directory paths ignored
// by Git. Exit status 1 means none matched; other failures abort this listing
// so an incomplete result is not cached.
func gitCheckIgnored(ctx context.Context, dir string, paths []string) (map[string]struct{}, bool) {
	cmd := exec.CommandContext(ctx, "git", "check-ignore", "--stdin", "-z")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	out, err := cmd.Output()
	if err != nil {
		if exit, isExit := err.(*exec.ExitError); !isExit || exit.ExitCode() != 1 || ctx.Err() != nil {
			return nil, false
		}
	}
	ignored := make(map[string]struct{})
	for _, path := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if path != "" {
			ignored[path] = struct{}{}
		}
	}
	return ignored, true
}

// walkPaths enumerates files and optional directories when dir isn't a git
// repo. It skips heavy directories and stops at a depth, count, and time budget.
func walkPaths(dir string, includeDirs bool) (paths []string, truncated, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), findFilesWalkBudget)
	defer cancel()

	var walk func(abs, rel string, depth int)
	walk = func(abs, rel string, depth int) {
		if truncated {
			return
		}
		if ctx.Err() != nil {
			if includeDirs {
				truncated = true
			}
			return
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				if includeDirs {
					truncated = true
				}
				return
			}
			name := entry.Name()
			childRel := name
			if rel != "" {
				childRel = rel + "/" + name
			}
			if entry.IsDir() {
				if _, skip := crawlSkipNames[name]; skip {
					continue
				}
				if depth >= findFilesWalkDepth {
					continue
				}
				if includeDirs {
					paths = append(paths, childRel+"/")
					if len(paths) >= findFilesMaxCandidates {
						truncated = true
						return
					}
				}
				walk(filepath.Join(abs, name), childRel, depth+1)
				continue
			}
			if !entry.Type().IsRegular() {
				continue
			}
			paths = append(paths, childRel)
			if len(paths) >= findFilesMaxCandidates {
				truncated = true
				return
			}
		}
	}
	walk(dir, "", 0)
	return paths, truncated, true
}
