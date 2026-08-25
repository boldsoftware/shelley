package server

import (
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

// FindFilesMatch is a single ranked file match.
type FindFilesMatch struct {
	// Path is the file path relative to the response's SearchDir.
	Path string `json:"path"`
	// MatchedIndexes are rune (code-point) offsets into Path that matched the
	// query, used by the UI to highlight the fuzzy match.
	MatchedIndexes []int `json:"matched_indexes,omitempty"`
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
	Total      int              `json:"total"`
	Truncated  bool             `json:"truncated"`
}

// handleFindFiles fuzzy-searches files under a working directory. The query
// is matched server-side (via github.com/sahilm/fuzzy) so the client never
// needs the full file list. Files are enumerated with `git ls-files`
// (tracked + untracked, honoring .gitignore) when dir is inside a repo, else
// via a bounded filesystem walk.
func (s *Server) handleFindFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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

	var files []string
	var listTruncated bool
	if !pq.IsPath || pq.DirExists {
		files, listTruncated = s.fileListCache.get(searchDir, func() (files []string, truncated, ok bool) {
			return listWorkingDirFiles(searchDir)
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
			resp.Matches = append(resp.Matches, FindFilesMatch{Path: p})
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
		resp.Matches = append(resp.Matches, FindFilesMatch{
			Path:           m.str,
			MatchedIndexes: byteToRuneOffsets(m.str, m.matchedIndexes),
		})
	}
	// A query naming an existing file always offers that file, even when the
	// listing can't surface it (`git ls-files` hides .gitignore'd files, and a
	// walk stops at its budget): typing the path is an unambiguous request.
	if rel, ok := relativeTo(searchDir, pq.FilePath); ok {
		var dropped bool
		resp.Matches, dropped = pinMatch(resp.Matches, rel, limit)
		resp.Truncated = resp.Truncated || dropped
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// pinMatch puts path at the head of matches, dropping any existing entry for
// it (whose highlights it keeps) and re-applying limit. dropped reports
// whether trimming to limit discarded a match the caller had included.
func pinMatch(matches []FindFilesMatch, path string, limit int) (out []FindFilesMatch, dropped bool) {
	pin := FindFilesMatch{Path: path}
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

// listWorkingDirFiles returns file paths (relative to dir) under dir. It
// prefers `git ls-files` so .gitignore is honored and the crawl is fast;
// otherwise it falls back to a bounded filesystem walk. truncated reports
// whether the list hit findFilesMaxCandidates; ok is false when the listing
// failed outright (so the caller shouldn't cache it).
func listWorkingDirFiles(dir string) (files []string, truncated, ok bool) {
	// An ignored directory (a node_modules or dist inside a repo) lists as
	// empty under `git ls-files`, so walk it instead: the user re-rooted the
	// search there deliberately and .gitignore has nothing left to say about
	// what's inside. A merely empty-looking directory elsewhere in the repo
	// still honors .gitignore, so its ignored files stay hidden.
	if gitFiles, isRepo := gitLsFiles(dir); isRepo && !gitIgnores(dir) {
		if len(gitFiles) > findFilesMaxCandidates {
			return gitFiles[:findFilesMaxCandidates], true, true
		}
		return gitFiles, false, true
	}
	return walkFiles(dir)
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

// walkFiles enumerates files under dir when it isn't a git repo. It skips the
// same heavy directories as the git-repo crawler and stops at a depth, count,
// and time budget so a huge tree can't hang the request. ok is always true:
// the handler already verified dir exists and is a directory, and unreadable
// subdirectories are silently skipped rather than failing the whole listing.
func walkFiles(dir string) (files []string, truncated, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), findFilesWalkBudget)
	defer cancel()

	var walk func(abs, rel string, depth int)
	walk = func(abs, rel string, depth int) {
		if truncated || ctx.Err() != nil {
			return
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
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
				walk(filepath.Join(abs, name), childRel, depth+1)
				continue
			}
			if !entry.Type().IsRegular() {
				continue
			}
			files = append(files, childRel)
			if len(files) >= findFilesMaxCandidates {
				truncated = true
				return
			}
		}
	}
	// A readable top-level dir was already verified by the handler (os.Stat),
	// so treat the walk as successful even if some subdirs are unreadable.
	walk(dir, "", 0)
	return files, truncated, true
}
