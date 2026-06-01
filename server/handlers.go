package server

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/gitstate"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
	"shelley.exe.dev/slug"
	"shelley.exe.dev/ui"
	"shelley.exe.dev/version"
)

// detectCLIAgents checks which CLI agent binaries are available in PATH.
// Returns a list of agent identifiers (e.g., "claude-cli", "codex-cli").
func detectCLIAgents() []string {
	var agents []string
	if _, err := exec.LookPath("claude"); err == nil {
		agents = append(agents, "claude-cli")
	}
	if _, err := exec.LookPath("codex"); err == nil {
		agents = append(agents, "codex-cli")
	}
	return agents
}

// handleRead serves files from limited allowed locations via /api/read?path=
func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	// Clean and enforce prefix restriction
	clean := filepath.Clean(p)
	if !isReadableUIFile(clean) {
		http.Error(w, "path not allowed", http.StatusForbidden)
		return
	}
	f, err := os.Open(clean)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	// Determine content type by extension first, then fallback to sniffing
	ext := strings.ToLower(filepath.Ext(clean))
	switch ext {
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".jpg", ".jpeg":
		w.Header().Set("Content-Type", "image/jpeg")
	case ".gif":
		w.Header().Set("Content-Type", "image/gif")
	case ".webp":
		w.Header().Set("Content-Type", "image/webp")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	case ".mp4":
		w.Header().Set("Content-Type", "video/mp4")
	case ".json":
		w.Header().Set("Content-Type", "application/json")
	default:
		buf := make([]byte, 512)
		n, _ := f.Read(buf)
		contentType := http.DetectContentType(buf[:n])
		if _, err := f.Seek(0, 0); err != nil {
			http.Error(w, "seek failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType)
	}
	// Reasonable short-term caching for assets, allow quick refresh during sessions
	w.Header().Set("Cache-Control", "public, max-age=300")
	io.Copy(w, f)
}

func isReadableUIFile(path string) bool {
	return strings.HasPrefix(path, browse.ScreenshotDir+"/") ||
		strings.HasPrefix(path, browse.UploadDir+"/") ||
		strings.HasPrefix(path, browse.ConsoleLogsDir+"/") ||
		strings.HasPrefix(path, browse.ScreencastDir+"/") ||
		isDistillationTempFile(path)
}

func isDistillationTempFile(path string) bool {
	dir := filepath.Join(os.TempDir(), "shelley-distillations")
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	return strings.HasPrefix(resolvedPath, resolvedDir+string(os.PathSeparator))
}

// handleUserAgentsMd returns the current content of the user's AGENTS.md.
// The modal uses this instead of the page-load snapshot so that reopening the
// editor shows freshly-saved content rather than whatever was on disk when the
// page was first loaded.
func (s *Server) handleUserAgentsMd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path, err := userAgentsMdPath()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	content := ""
	if b, err := os.ReadFile(path); err == nil {
		content = string(b)
	} else if !os.IsNotExist(err) {
		http.Error(w, fmt.Sprintf("failed to read file: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"path": path, "content": content})
}

// handleWriteFile writes content to a file (for diff viewer edit mode)
func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	// Security: only allow writing within certain directories
	// For now, require the path to be within a git repository
	clean := filepath.Clean(req.Path)
	if !filepath.IsAbs(clean) {
		http.Error(w, "absolute path required", http.StatusBadRequest)
		return
	}

	// Write the file
	if err := os.WriteFile(clean, []byte(req.Content), 0o644); err != nil {
		http.Error(w, fmt.Sprintf("failed to write file: %v", err), http.StatusInternalServerError)
		return
	}

	// If the file lives in the dedicated user AGENTS.md repo, record this
	// edit as a new git commit. We intentionally don't surface git history in
	// the UI; it exists purely so users can recover old versions on disk.
	if isUserAgentsMdFile(clean) {
		if err := commitUserAgentsMd("edit via web ui"); err != nil {
			// Log-ish: don't fail the write if the commit fails.
			fmt.Fprintf(os.Stderr, "warning: commit AGENTS.md edit: %v\n", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// userAgentsMdPath returns the path to ~/.config/shelley/AGENTS.md
func userAgentsMdPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "shelley", "AGENTS.md"), nil
}

const maxUploadBytes = 1 << 30 // 1 GiB

type uploadErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// handleUpload handles file uploads via POST /api/upload.
// Files are saved to the UploadDir with a random filename.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		writeUploadParseError(w, "failed to parse form: ", err)
		return
	}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			writeUploadError(w, http.StatusBadRequest, "missing_file", "failed to get uploaded file: http: no such file")
			return
		}
		if err != nil {
			writeUploadParseError(w, "failed to parse form: ", err)
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()
			continue
		}

		path, err := saveUploadFile(part.FileName(), part)
		part.Close()
		if err != nil {
			writeUploadSaveError(w, err)
			return
		}
		writeUploadResponse(w, path)
		return
	}
}

// handleUploadRawProbe answers a GET on the raw upload endpoint with an
// empty 200 so clients can detect support without sending a body. Older
// servers return 404/405; newer clients use the probe to decide between
// raw and multipart transports.
func (s *Server) handleUploadRawProbe(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleUploadRaw handles file uploads via POST /api/upload/raw?filename=...
// The request body is the file content.
func (s *Server) handleUploadRaw(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Query().Get("filename")
	if filename == "" {
		writeUploadError(w, http.StatusBadRequest, "filename_required", "filename required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	path, err := saveUploadFile(filename, r.Body)
	if err != nil {
		writeUploadSaveError(w, err)
		return
	}
	writeUploadResponse(w, path)
}

func writeUploadResponse(w http.ResponseWriter, path string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"path": path})
}

func writeUploadError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(uploadErrorResponse{
		Error:   code,
		Message: message,
	})
}

func writeUploadParseError(w http.ResponseWriter, prefix string, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		writeUploadError(w, http.StatusRequestEntityTooLarge, "request_body_too_large", "request body too large")
		return
	}
	writeUploadError(w, http.StatusBadRequest, "invalid_multipart", prefix+err.Error())
}

func writeUploadSaveError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		writeUploadError(w, http.StatusRequestEntityTooLarge, "request_body_too_large", "request body too large")
		return
	}
	writeUploadError(w, http.StatusInternalServerError, "upload_save_failed", "failed to save file: "+err.Error())
}

// saveUploadFile writes src into browse.UploadDir under a sanitized name
// derived from originalFilename. Writes are scoped via os.Root so a hostile
// client can't escape the upload directory via traversal or symlinks. Names
// that collide with an existing file get a random suffix instead of
// overwriting the existing entry. Names that pass through filepath.Base
// as an unusable value (".", "..", empty) fall back to a fully random name.
func saveUploadFile(originalFilename string, src io.Reader) (string, error) {
	if err := os.MkdirAll(browse.UploadDir, 0o755); err != nil {
		return "", fmt.Errorf("create upload directory: %w", err)
	}
	root, err := os.OpenRoot(browse.UploadDir)
	if err != nil {
		return "", fmt.Errorf("open upload root: %w", err)
	}
	defer root.Close()

	preferred := sanitizedUploadBasename(originalFilename)
	if preferred != "" {
		if path, err := createAndCopy(root, preferred, src); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}

	ext := filepath.Ext(preferred)
	stem := strings.TrimSuffix(preferred, ext)
	if stem == "" {
		stem = "upload"
	}
	for attempt := 0; attempt < 10; attempt++ {
		randBytes := make([]byte, 8)
		if _, err := rand.Read(randBytes); err != nil {
			return "", fmt.Errorf("generate random filename: %w", err)
		}
		name := fmt.Sprintf("%s_%s%s", stem, hex.EncodeToString(randBytes), ext)
		path, err := createAndCopy(root, name, src)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("generate unique upload filename")
}

// sanitizedUploadBasename returns a filename safe to use directly inside the
// upload directory and embedded as a [path] token in model-facing chat text:
// just the basename, restricted to a conservative ASCII whitelist with
// everything else mapped to '_'. Traversal-only or empty results return ""
// so the caller knows to fall back to a random name.
func sanitizedUploadBasename(originalFilename string) string {
	base := filepath.Base(originalFilename)
	switch base {
	case ".", "..", "/", `\`, "":
		return ""
	}
	safe := strings.Map(func(r rune) rune {
		switch {
		case 'a' <= r && r <= 'z',
			'A' <= r && r <= 'Z',
			'0' <= r && r <= '9',
			r == '.', r == '-', r == '_', r == ' ':
			return r
		}
		return '_'
	}, base)
	// Cap length to leave room for a collision-avoidance random suffix while
	// preserving the extension. Filesystem limit is typically 255 bytes.
	const maxLen = 200
	if len(safe) > maxLen {
		ext := filepath.Ext(safe)
		if len(ext) > 20 {
			ext = ""
		}
		stem := strings.TrimSuffix(safe, ext)
		if len(stem) > maxLen-len(ext) {
			stem = stem[:maxLen-len(ext)]
		}
		safe = stem + ext
	}
	return safe
}

func createAndCopy(root *os.Root, name string, src io.Reader) (string, error) {
	destFile, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(destFile, src)
	closeErr := destFile.Close()
	if copyErr != nil {
		root.Remove(name)
		return "", copyErr
	}
	if closeErr != nil {
		root.Remove(name)
		return "", closeErr
	}
	return filepath.Join(browse.UploadDir, name), nil
}

// staticHandler serves files from the provided filesystem.
// For JS/CSS files, it serves pre-compressed .gz versions with content-based ETags.
func isConversationSlugPath(path string) bool {
	return strings.HasPrefix(path, "/c/")
}

func isSPARoute(path string) bool {
	return path == "/new" || strings.HasPrefix(path, "/export/")
}

// acceptsGzip reports whether r accepts gzip encoding.
func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// pickStreamEncoding chooses an encoding for SSE responses. Prefers zstd
// then gzip, then identity. Honours Accept-Encoding q-values per RFC 9110
// §12.5.3: an entry with q=0 means the client refuses that encoding.
//
// Returns ("zstd"|"gzip"|"", acceptable). The second return is false only
// when the client accepts no encoding we can produce — e.g. explicit
// `identity;q=0` with neither gzip nor zstd offered — in which case the
// caller should reply 406 Not Acceptable.
func pickStreamEncoding(r *http.Request) (string, bool) {
	ae := r.Header.Get("Accept-Encoding")
	if strings.TrimSpace(ae) == "" {
		// Absent header: any encoding is acceptable, default to identity.
		return "", true
	}
	prefs := parseAcceptEncoding(ae)
	// `*` is a catch-all that applies to any coding not explicitly listed.
	codingAcceptable := func(name string) bool {
		if q, ok := prefs[name]; ok {
			return q > 0
		}
		if q, ok := prefs["*"]; ok {
			return q > 0
		}
		return false
	}
	if codingAcceptable("zstd") {
		return "zstd", true
	}
	if codingAcceptable("gzip") {
		return "gzip", true
	}
	// Neither compressed encoding is acceptable. Identity is acceptable
	// unless explicitly refused via `identity;q=0` or a catch-all `*;q=0`
	// (with no overriding `identity;q>0`).
	if q, ok := prefs["identity"]; ok {
		return "", q > 0
	}
	if q, ok := prefs["*"]; ok && q == 0 {
		return "", false
	}
	return "", true
}

// parseAcceptEncoding parses an Accept-Encoding header into a coding->q map.
// Unknown parameters are ignored; entries without an explicit q default to 1.
// Lower-cases coding names. Returns an empty map for an empty header.
func parseAcceptEncoding(h string) map[string]float64 {
	out := map[string]float64{}
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		coding, params, _ := strings.Cut(part, ";")
		coding = strings.ToLower(strings.TrimSpace(coding))
		if coding == "" {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			p = strings.TrimSpace(p)
			name, val, ok := strings.Cut(p, "=")
			if !ok || strings.ToLower(strings.TrimSpace(name)) != "q" {
				continue
			}
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
				q = parsed
			}
		}
		out[coding] = q
	}
	return out
}

// etagMatches checks if the client's If-None-Match header matches the given ETag.
// Per RFC 7232, If-None-Match can contain multiple ETags (comma-separated)
// and may use weak validators (W/"..."). For GET/HEAD, weak comparison is used.
func etagMatches(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" {
		return false
	}
	// Normalize our ETag by stripping W/ prefix if present
	normEtag := strings.TrimPrefix(etag, `W/`)

	// If-None-Match can be "*" which matches any
	if ifNoneMatch == "*" {
		return true
	}

	// Split by comma and check each tag
	for _, tag := range strings.Split(ifNoneMatch, ",") {
		tag = strings.TrimSpace(tag)
		// Strip W/ prefix for weak comparison
		tag = strings.TrimPrefix(tag, `W/`)
		if tag == normEtag {
			return true
		}
	}
	return false
}

func (s *Server) staticHandler(fsys http.FileSystem) http.Handler {
	fileServer := http.FileServer(fsys)

	// Load checksums for ETag support (content-based, not git-based)
	checksums := ui.Checksums()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Inject initialization data into index.html
		if r.URL.Path == "/" || r.URL.Path == "/index.html" || isConversationSlugPath(r.URL.Path) || isSPARoute(r.URL.Path) {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			w.Header().Set("Content-Type", "text/html")
			s.serveIndexWithInit(w, r, fsys)
			return
		}

		// For JS and CSS files, serve from .gz files (only .gz versions are embedded)
		if strings.HasSuffix(r.URL.Path, ".js") || strings.HasSuffix(r.URL.Path, ".css") {
			gzPath := r.URL.Path + ".gz"
			gzFile, err := fsys.Open(gzPath)
			if err != nil {
				// No .gz file, fall through to regular file server
				fileServer.ServeHTTP(w, r)
				return
			}
			defer gzFile.Close()

			stat, err := gzFile.Stat()
			if err != nil || stat.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}

			// Get filename without leading slash for checksum lookup
			filename := strings.TrimPrefix(r.URL.Path, "/")

			// Check ETag for cache validation (content-based)
			if checksums != nil {
				if hash, ok := checksums[filename]; ok {
					etag := `"` + hash + `"`
					w.Header().Set("ETag", etag)
					if etagMatches(r.Header.Get("If-None-Match"), etag) {
						w.WriteHeader(http.StatusNotModified)
						return
					}
				}
			}

			w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(r.URL.Path)))
			w.Header().Set("Vary", "Accept-Encoding")
			// Use must-revalidate so browsers check ETag on each request.
			// We can't use immutable since we don't have content-hashed filenames.
			w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")

			if acceptsGzip(r) {
				// Client accepts gzip - serve compressed directly
				w.Header().Set("Content-Encoding", "gzip")
				io.Copy(w, gzFile)
			} else {
				// Rare: client doesn't accept gzip - decompress on the fly
				gr, err := gzip.NewReader(gzFile)
				if err != nil {
					http.Error(w, "failed to decompress", http.StatusInternalServerError)
					return
				}
				defer gr.Close()
				io.Copy(w, gr)
			}
			return
		}

		fileServer.ServeHTTP(w, r)
	})
}

// hashString computes a simple hash of a string
func hashString(s string) uint32 {
	var hash uint32
	for _, c := range s {
		hash = ((hash << 5) - hash) + uint32(c)
	}
	return hash
}

// generateFaviconSVG creates a Cool S favicon with color based on hostname hash
// Big colored circle background with the Cool S inscribed in white
func generateFaviconSVG(hostname string) string {
	hash := hashString(hostname)
	h := hash % 360
	bgColor := fmt.Sprintf("hsl(%d, 70%%, 55%%)", h)
	// White S on colored background - good contrast on any saturated hue
	strokeColor := "#ffffff"

	// Original Cool S viewBox: 0 0 171 393 (tall rectangle)
	// Square viewBox 0 0 400 400 with circle, S scaled and centered inside
	// S dimensions: 171x393, scale 0.97 gives 166x381, centered in 400x400
	return fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 400">
<circle cx="200" cy="200" r="200" fill="%s"/>
<g transform="translate(117 10) scale(0.97)">
<g stroke-linecap="round"><g transform="translate(13.3 97.5) rotate(0 1.4 42.2)"><path d="M1.28 0.48C1.15 14.67,-0.96 71.95,-1.42 86.14M-1.47-1.73C-0.61 11.51,4.65 66.62,4.21 81.75" stroke="%s" stroke-width="14" fill="none"/></g></g>
<g stroke-linecap="round"><g transform="translate(87.6 97.2) rotate(0 1.2 42.4)"><path d="M-1.42 1.14C-1.89 15.33,-1.41 71.93,-1.52 85.6M3-0.71C3.35 12.53,3.95 66.59,4.06 80.91" stroke="%s" stroke-width="14" fill="none"/></g></g>
<g stroke-linecap="round"><g transform="translate(156.3 91) rotate(0 0.7 42.1)"><path d="M-1.52 0.6C-1.62 14.26,-1.97 68.6,-2.04 83.12M2.86-1.55C3.77 12.32,3.09 71.53,3.26 85.73" stroke="%s" stroke-width="14" fill="none"/></g></g>
<g stroke-linecap="round"><g transform="translate(157.7 230.3) rotate(0 0.6 42.9)"><path d="M-2.04-1.88C-2.11 12.64,-2.52 72.91,-1.93 87.72M2.05 3.27C3.01 17.02,3.68 70.97,3.43 84.18" stroke="%s" stroke-width="14" fill="none"/></g></g>
<g stroke-linecap="round"><g transform="translate(12.6 226.7) rotate(0 0.2 44.3)"><path d="M-1.93 2.72C-1.33 17.52,1.37 73.57,1.54 86.96M2.23 1.72C2.77 15.92,1.05 69.12,0.14 83.02" stroke="%s" stroke-width="14" fill="none"/></g></g>
<g stroke-linecap="round"><g transform="translate(82.8 226.6) rotate(0 -1.1 43.1)"><path d="M1.54 1.96C1.7 15.35,-0.76 69.37,-0.93 83.06M-1.07 0.56C-1.19 15.45,-3.69 71.28,-3.67 85.64" stroke="%s" stroke-width="14" fill="none"/></g></g>
<g stroke-linecap="round"><g transform="translate(152.7 311.8) rotate(0 -32.3 34.6)"><path d="M-0.93-1.94C-12.26 9.08,-55.27 56.42,-66.46 68.08M3.76 3.18C-8.04 14.42,-56.04 59.98,-68.41 71.22" stroke="%s" stroke-width="14" fill="none"/></g></g>
<g stroke-linecap="round"><g transform="translate(14.7 308.2) rotate(0 34.1 33.6)"><path d="M0.54-0.92C12.51 10.75,58.76 55.93,70.91 68.03M-2.62-3.88C8.97 8.35,55.58 59.22,68.08 71.13" stroke="%s" stroke-width="14" fill="none"/></g></g>
<g stroke-linecap="round"><g transform="translate(11.3 178.5) rotate(0 35.7 23.4)"><path d="M-1.09-0.97C10.89 7.63,60.55 42.51,72.41 50.67M3.51-3.96C15.2 4,60.24 37.93,70.94 47.11" stroke="%s" stroke-width="14" fill="none"/></g></g>
<g stroke-linecap="round"><g transform="translate(11.3 223.5) rotate(0 13.4 -10.2)"><path d="M1.41 2.67C6.27-1,23.83-19.1,28.07-23M-1.26 1.66C3.24-1.45,19.69-14.92,25.32-19.37" stroke="%s" stroke-width="14" fill="none"/></g></g>
<g stroke-linecap="round"><g transform="translate(13.3 94.5) rotate(0 34.6 -42.2)"><path d="M-0.93 0C9.64-13.89,53.62-66.83,64.85-80.71M3.76-2.46C15.07-15.91,59.99-71.5,70.08-84.48" stroke="%s" stroke-width="14" fill="none"/></g></g>
<g stroke-linecap="round"><g transform="translate(81.3 12.5) rotate(0 36.1 39.1)"><path d="M-2.15 2.29C10.41 14.58,61.78 62.2,74.43 73.73M1.88 1.07C14.1 13.81,60.32 65.18,71.89 77.21" stroke="%s" stroke-width="14" fill="none"/></g></g>
<g stroke-linecap="round"><g transform="translate(88.3 177.5) rotate(0 31.2 22.9)"><path d="M-0.57-0.27C10.92 7.09,55.6 38.04,66.75 46.48M-4.32-2.89C6.87 4.52,51.07 40.67,63.83 48.74" stroke="%s" stroke-width="14" fill="none"/></g></g>
<g stroke-linecap="round"><g transform="translate(155.3 174.5) rotate(0 -10.7 13.4)"><path d="M-1.25-2.52C-5.27 2.41,-21.09 24.62,-24.67 29.33M3.26 2.28C0.21 6.4,-14.57 20.81,-19.18 25.04" stroke="%s" stroke-width="14" fill="none"/></g></g>
</g>
</svg>`,
		bgColor, strokeColor, strokeColor, strokeColor, strokeColor, strokeColor, strokeColor, strokeColor, strokeColor, strokeColor, strokeColor, strokeColor, strokeColor, strokeColor, strokeColor,
	)
}

// serveIndexWithInit serves index.html with injected initialization data
func (s *Server) serveIndexWithInit(w http.ResponseWriter, r *http.Request, fs http.FileSystem) {
	// Read index.html from the filesystem
	file, err := fs.Open("/index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusNotFound)
		return
	}
	defer file.Close()

	indexHTML, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Failed to read index.html", http.StatusInternalServerError)
		return
	}

	// Build initialization data
	modelList := s.getModelList()
	defaultModel := s.effectiveDefaultModel(modelList)
	markDefaultModel(modelList, defaultModel)

	// Get hostname (add .exe.xyz suffix if no dots, matching system_prompt.go)
	hostname := "localhost"
	if h, err := os.Hostname(); err == nil {
		if !strings.Contains(h, ".") {
			hostname = h + ".exe.xyz"
		} else {
			hostname = h
		}
	}

	// Get default working directory
	defaultCwd, err := os.Getwd()
	if err != nil {
		defaultCwd = "/"
	}

	// Get home directory for tilde display
	homeDir, _ := os.UserHomeDir()

	userAgentsMdPath, _ := userAgentsMdPath()

	// Note: AGENTS.md content is NOT embedded in init data. It is fetched fresh
	// via /api/user-agents-md when the editor modal opens so that reopening
	// after a save shows current disk state, not stale page-load content.
	initData := map[string]interface{}{
		"models":              modelList,
		"default_model":       defaultModel,
		"hostname":            hostname,
		"default_cwd":         defaultCwd,
		"home_dir":            homeDir,
		"user_agents_md_path": userAgentsMdPath,
	}
	// On exe.dev VMs (where /exe.dev exists), auto-derive the terminal URL and
	// default links from the current hostname so they pick up hostname changes
	// on reload.
	if _, err := os.Stat("/exe.dev"); err == nil {
		short := strings.SplitN(hostname, ".", 2)[0]
		initData["terminal_url"] = "https://" + short + ".xterm.exe.xyz"
		// Home icon — used historically by the "Back to exe.dev" link.
		const homeIcon = "M3 12l2-2m0 0l7-7 7 7M5 10v10a1 1 0 001 1h3m10-11l2 2m-2-2v10a1 1 0 01-1 1h-3m-6 0a1 1 0 001-1v-4a1 1 0 011-1h2a1 1 0 011 1v4a1 1 0 001 1m-6 0h6"
		// External-link icon for the box's own web page.
		const extIcon = "M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14"
		initData["links"] = []Link{
			{Title: hostname, URL: "https://" + hostname, IconSVG: extIcon},
			{Title: "Back to exe.dev", URL: "https://exe.dev", IconSVG: homeIcon},
		}
	}

	// Inject notification channel type metadata for the settings modal
	initData["notification_channel_types"] = s.getNotificationChannelTypes()
	initData["cli_agents"] = detectCLIAgents()
	if s.Banner != "" {
		initData["banner"] = s.Banner
	}

	initJSON, err := json.Marshal(initData)
	if err != nil {
		http.Error(w, "Failed to marshal init data", http.StatusInternalServerError)
		return
	}

	// Generate favicon as data URI
	// Include the listening port in the hash so demo servers on different ports
	// get visually distinct favicons.
	faviconKey := hostname
	if s.listenPort != 0 {
		faviconKey = fmt.Sprintf("%s:%d", hostname, s.listenPort)
	}
	faviconSVG := generateFaviconSVG(faviconKey)
	faviconDataURI := "data:image/svg+xml," + url.PathEscape(faviconSVG)
	faviconLink := fmt.Sprintf(`<link rel="icon" type="image/svg+xml" href="%s"/>`, faviconDataURI)

	// Inject the script tag and favicon before </head>
	initScript := fmt.Sprintf(`<script>window.__SHELLEY_INIT__=%s;</script>`, initJSON)
	injection := faviconLink + initScript
	modifiedHTML := strings.Replace(string(indexHTML), "</head>", injection+"</head>", 1)

	w.Write([]byte(modifiedHTML))
}

// handleConfig returns server configuration
// handleConversations handles GET /conversations
func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 5000
	offset := 0
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	conversations, err := s.conversationListWithState(r.Context(), limit, offset, r.URL.Query().Get("q"), r.URL.Query().Get("search_content") == "true")
	if err != nil {
		s.logger.Error("Failed to get conversations", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conversations)
}

// ConversationListSnapshot is the seed payload returned by
// GET /api/conversations/snapshot. Clients use Hash to resume the unified
// stream patch stream from this exact state.
type ConversationListSnapshot struct {
	Conversations []ConversationWithState `json:"conversations"`
	Hash          string                  `json:"hash"`
}

// handleConversationsSnapshot returns the current unarchived conversation
// list (parents + subagents) together with the patch-stream hash that
// anchors it. Each row includes working state, git info, subagent count,
// and a trailing agent-message preview. Archived conversations are
// served separately by /api/conversations/archived.
//
// The hash exists so a client can fetch the current state once and then
// resume incremental updates over /api/stream2 without racing concurrent
// Tx commits.
func (s *Server) handleConversationsSnapshot(w http.ResponseWriter, r *http.Request) {
	list, hash, err := s.conversationListStream.snapshot(r.Context())
	if err != nil {
		s.logger.Error("Failed to compute conversation list snapshot", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []ConversationWithState{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ConversationListSnapshot{Conversations: list, Hash: hash})
}

func (s *Server) conversationListWithState(ctx context.Context, limit, offset int, query string, searchContent bool) ([]ConversationWithState, error) {
	return s.conversationListWithStateInternal(ctx, limit, offset, query, searchContent, false)
}

// searchConversationsFTSWithState performs a full-text search across active
// AND archived top-level conversations and decorates the results with the
// same working/subagent/preview metadata as the regular list.
func (s *Server) searchConversationsFTSWithState(ctx context.Context, query string, limit, offset int) ([]ConversationWithState, error) {
	hits, err := s.db.SearchConversationsFTS(ctx, query, int64(limit), int64(offset))
	if err != nil {
		return nil, err
	}
	conversations := make([]db.ConversationListItem, len(hits))
	for i, h := range hits {
		conversations[i] = db.ConversationListItem{
			Conversation:     h.Conversation,
			Preview:          h.Preview,
			PreviewUpdatedAt: h.PreviewUpdatedAt,
			MaxSequenceID:    h.MaxSequenceID,
		}
	}
	decorated, err := s.decorateConversations(ctx, conversations)
	if err != nil {
		return nil, err
	}
	for i := range decorated {
		decorated[i].SearchSnippet = hits[i].Snippet
	}
	return decorated, nil
}

// conversationListWithStateInternal backs both the public list endpoint and the
// patch stream. When includeSubagents is true the result also contains
// subagent conversations so the UI can render and diff their working state.
func (s *Server) conversationListWithStateInternal(ctx context.Context, limit, offset int, query string, searchContent, includeSubagents bool) ([]ConversationWithState, error) {
	var conversations []db.ConversationListItem
	var err error
	if query != "" {
		if searchContent {
			conversations, err = s.db.SearchConversationsWithMessages(ctx, query, int64(limit), int64(offset))
		} else {
			conversations, err = s.db.SearchConversations(ctx, query, int64(limit), int64(offset))
		}
	} else if includeSubagents {
		conversations, err = s.db.ListAllConversations(ctx, int64(limit), int64(offset))
	} else {
		conversations, err = s.db.ListConversations(ctx, int64(limit), int64(offset))
	}
	if err != nil {
		return nil, err
	}
	return s.decorateConversations(ctx, conversations)
}

// decorateConversations wraps a list of conversation list items with the
// working/subagent/git metadata used by the conversation list UI. The
// preview, preview timestamp and max sequence_id are already carried on each
// item (computed in the very query that listed the conversations, scoped to
// the visible window — see db.ConversationListItem), so decorate just copies
// them across rather than running a separate previews/max-sequence query.
func (s *Server) decorateConversations(ctx context.Context, conversations []db.ConversationListItem) ([]ConversationWithState, error) {
	// Working state lives on the conversation row itself (see
	// ResetAllAgentWorking on startup + SetConversationAgentWorking on every
	// transition), so we don't have to consult the in-memory manager map.
	subagentCounts, err := s.db.GetSubagentCounts(ctx)
	if err != nil {
		s.logger.Error("Failed to get subagent counts", "error", err)
		subagentCounts = make(map[string]int64)
	}

	now := time.Now()
	result := make([]ConversationWithState, len(conversations))
	for i, item := range conversations {
		conv := item.Conversation
		cws := ConversationWithState{
			Conversation:     conv,
			Working:          conv.AgentWorking,
			SubagentCount:    subagentCounts[conv.ConversationID],
			Preview:          item.Preview,
			PreviewUpdatedAt: item.PreviewUpdatedAt,
			MaxSequenceID:    item.MaxSequenceID,
		}
		if conv.Cwd != nil {
			entry, ok := s.conversationListGitCache.get(*conv.Cwd, now)
			if !ok {
				gs := gitstate.GetGitState(*conv.Cwd)
				entry = conversationListGitCacheEntry{
					state:     gs,
					expiresAt: now.Add(conversationListGitCacheTTL),
				}
				if gs.IsRepo {
					entry.worktree = getGitWorktreeRoot(gs.Worktree)
					if gitDir, err := resolveGitDir(gs.Worktree); err == nil {
						entry.gitDir = gitDir
						entry.fingerprint = gitFingerprint(gitDir)
					}
				}
				s.conversationListGitCache.set(*conv.Cwd, entry)
			}
			if entry.state.IsRepo {
				cws.GitRepoRoot = entry.state.Worktree
				cws.GitWorktreeRoot = entry.worktree
				cws.GitCommit = entry.state.Commit
				cws.GitSubject = entry.state.Subject
			}
		}
		result[i] = cws
	}
	return result, nil
}

// conversationMux returns a mux for /api/conversation/<id>/* routes
func (s *Server) conversationMux() *http.ServeMux {
	mux := http.NewServeMux()
	// GET /api/conversation/<id> - returns all messages (can be large, compress)
	mux.Handle("GET /{id}", gzipHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleGetConversation(w, r, r.PathValue("id"))
	})))
	// GET /api/conversation/<id>/stream - legacy SSE stream. Compression is
	// negotiated inside the handler (zstd/gzip per Accept-Encoding) with a
	// compressor flush after every event so messages stream promptly.
	mux.HandleFunc("GET /{id}/stream", func(w http.ResponseWriter, r *http.Request) {
		s.handleStreamConversation(w, r, r.PathValue("id"))
	})
	// POST endpoints - small responses, no compression needed
	mux.HandleFunc("POST /{id}/chat", func(w http.ResponseWriter, r *http.Request) {
		s.handleChatConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /{id}/hooks", func(w http.ResponseWriter, r *http.Request) {
		s.handleRegisterConversationHook(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		s.handleCancelConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		s.handleRetryConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		s.handleArchiveConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /{id}/unarchive", func(w http.ResponseWriter, r *http.Request) {
		s.handleUnarchiveConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		s.handleDeleteConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /{id}/rename", func(w http.ResponseWriter, r *http.Request) {
		s.handleRenameConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		s.handleUpdateConversationTags(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("GET /{id}/subagents", func(w http.ResponseWriter, r *http.Request) {
		s.handleGetSubagents(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /{id}/cancel-queued", func(w http.ResponseWriter, r *http.Request) {
		s.handleCancelQueued(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /{id}/new-generation", func(w http.ResponseWriter, r *http.Request) {
		s.handleStartNewGeneration(w, r, r.PathValue("id"))
	})
	return mux
}

// handleGetConversation handles GET /conversation/<id>
func (s *Server) handleGetConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	// Optional cursor: clients that already have a partial cache pass
	// `?last_sequence_id=N` and we return only the tail (matches the
	// SSE stream's resume semantics). Absent / unparsable / negative
	// values fall back to a full list.
	var lastSeqID int64 = -1
	if s := r.URL.Query().Get("last_sequence_id"); s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n >= 0 {
			lastSeqID = n
		}
	}
	var (
		messages     []generated.Message
		conversation generated.Conversation
	)
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		if lastSeqID >= 0 {
			messages, err = q.ListMessagesSince(ctx, generated.ListMessagesSinceParams{
				ConversationID: conversationID,
				SequenceID:     lastSeqID,
			})
		} else {
			messages, err = q.ListMessages(ctx, conversationID)
		}
		if err != nil {
			return err
		}
		conversation, err = q.GetConversation(ctx, conversationID)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Error("Failed to get conversation messages", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	apiMessages := toAPIMessages(messages)
	// max_sequence_id lets clients tell whether their cache is up to date
	// without needing a separate query. Compute from the message list rather
	// than reaching into the conversation manager so this endpoint works for
	// inactive conversations too.
	var maxSeq int64
	for _, m := range messages {
		if m.SequenceID > maxSeq {
			maxSeq = m.SequenceID
		}
	}
	json.NewEncoder(w).Encode(StreamResponse{
		Messages:     apiMessages,
		Conversation: &conversation,
		// ConversationState is sent via the streaming endpoint, not on initial load
		ContextWindowSize: calculateContextWindowSize(apiMessages),
		MaxSequenceID:     maxSeq,
	})
}

// derefString returns the value pointed to by p, or "" if p is nil.
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ChatRequest represents a chat message from the user
type ChatRequest struct {
	Message             string                  `json:"message"`
	Model               string                  `json:"model,omitempty"`
	Cwd                 string                  `json:"cwd,omitempty"`
	ConversationOptions *db.ConversationOptions `json:"conversation_options,omitempty"`
	Queue               bool                    `json:"queue,omitempty"`
}

// handleChatConversation handles POST /conversation/<id>/chat
func (s *Server) handleChatConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Parse request
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Message == "" {
		http.Error(w, "Message is required", http.StatusBadRequest)
		return
	}

	// Get LLM service for the requested model
	modelID := req.Model
	if modelID == "" {
		modelID = s.effectiveDefaultModel(s.getModelList())
	}

	llmService, err := s.llmManager.GetService(modelID)
	if err != nil {
		s.logger.Error("Unsupported model requested", "model", modelID, "error", err)
		http.Error(w, fmt.Sprintf("Unsupported model: %s", modelID), http.StatusBadRequest)
		return
	}

	userEmail := r.Header.Get("X-ExeDev-Email")

	// Get or create conversation manager
	manager, err := s.getOrCreateConversationManager(ctx, conversationID, userEmail)
	if errors.Is(err, errConversationModelMismatch) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Slash-command hook: if the message starts with /<name> and a matching
	// executable exists at ~/.config/shelley/hooks/slash/<name>, run it and
	// use its stdout as the replacement message text. Empty stdout leaves
	// the original message unchanged.
	slashResult := RunSlashCommandHook(SlashCommandHookInput{
		RawMessage:     req.Message,
		ConversationID: conversationID,
		Cwd:            manager.cwd,
		Model:          modelID,
		UserEmail:      userEmail,
		IsOrchestrator: manager.conversationOptions.IsOrchestrator(),
	})
	if slashResult.Err != nil {
		s.logger.Error("slash-command hook failed", "conversationID", conversationID, "error", slashResult.Err)
		http.Error(w, fmt.Sprintf("slash command failed: %v", slashResult.Err), http.StatusBadRequest)
		return
	}
	if slashResult.Handled && slashResult.Message != "" {
		req.Message = slashResult.Message
	}

	// Decide whether this message will be queued or accepted immediately.
	// The chat-message hook is told which path it will take so it can react
	// accordingly.
	willQueue := req.Queue || manager.IsDistilling()

	// Run chat-message hook; the hook may rewrite the message text. Hook
	// failures abort the request — the user's message is not delivered.
	newMsg, err := RunChatMessageHookIn(s.hooksDir, ChatMessageHookInput{
		Message: req.Message,
		Readonly: ChatMessageReadonly{
			ConversationID: conversationID,
			Model:          modelID,
			Queued:         willQueue,
			Headers:        HookHeaders(r.Header),
		},
	})
	if err != nil {
		s.logger.Error("chat-message hook failed", "conversationID", conversationID, "error", err)
		http.Error(w, "chat-message hook failed", http.StatusInternalServerError)
		return
	}
	req.Message = newMsg

	// Create user message
	userMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: req.Message},
		},
	}

	// Queue mode: record the message to DB but don't interrupt the agent.
	// The message will be sent when the agent finishes its current turn or
	// current distillation. Force queueing during distillation even if the
	// client has not seen the distill status update yet.
	//
	// Use the willQueue snapshot computed before the chat-message hook so
	// the hook's view ("queued" in its readonly context) is authoritative
	// and stays consistent with what actually happens here.
	if willQueue {
		if err := manager.QueueMessage(ctx, s, modelID, userMessage); err != nil {
			s.logger.Error("Failed to queue user message", "conversationID", conversationID, "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"status": "queued"})
		return
	}

	firstMessage, err := manager.AcceptUserMessage(ctx, llmService, modelID, userMessage)
	if errors.Is(err, errConversationModelMismatch) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.logger.Error("Failed to accept user message", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if firstMessage {
		ctxNoCancel := context.WithoutCancel(ctx)
		go func() {
			slugCtx, cancel := context.WithTimeout(ctxNoCancel, 15*time.Second)
			defer cancel()
			_, err := slug.GenerateSlug(slugCtx, s.llmManager, s.db, s.logger, conversationID, req.Message, modelID)
			if err != nil {
				s.logger.Warn("Failed to generate slug for conversation", "conversationID", conversationID, "error", err)
			} else {
				go s.notifySubscribers(ctxNoCancel, conversationID)
			}
		}()
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// handleNewConversation handles POST /api/conversations/new - creates conversation implicitly on first message
func (s *Server) handleNewConversation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Parse request
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Message == "" {
		http.Error(w, "Message is required", http.StatusBadRequest)
		return
	}

	// Get LLM service for the requested model
	modelID := req.Model
	if modelID == "" {
		modelID = s.effectiveDefaultModel(s.getModelList())
	}

	llmService, err := s.llmManager.GetService(modelID)
	if err != nil {
		s.logger.Error("Unsupported model requested", "model", modelID, "error", err)
		http.Error(w, fmt.Sprintf("Unsupported model: %s", modelID), http.StatusBadRequest)
		return
	}

	// Create new conversation with optional cwd
	var cwdPtr *string
	if req.Cwd != "" {
		cwdPtr = &req.Cwd
	}
	var convOpts db.ConversationOptions
	if req.ConversationOptions != nil {
		convOpts = *req.ConversationOptions
		if convOpts.Type != "" && convOpts.Type != "normal" && convOpts.Type != "orchestrator" {
			http.Error(w, fmt.Sprintf("Invalid conversation options type: %s", convOpts.Type), http.StatusBadRequest)
			return
		}
		if convOpts.SubagentBackend != "" && convOpts.SubagentBackend != "shelley" && convOpts.SubagentBackend != "claude-cli" && convOpts.SubagentBackend != "codex-cli" {
			http.Error(w, fmt.Sprintf("Invalid subagent_backend: %s; must be one of: shelley, claude-cli, codex-cli", convOpts.SubagentBackend), http.StatusBadRequest)
			return
		}
		for name, v := range convOpts.ToolOverrides {
			if v != "on" && v != "off" {
				http.Error(w, fmt.Sprintf("Invalid tool_overrides[%s]=%q; must be \"on\" or \"off\"", name, v), http.StatusBadRequest)
				return
			}
		}
		for _, hook := range convOpts.EndOfTurnHooks {
			if err := validateConversationHookURL(hook.URL); err != nil {
				http.Error(w, fmt.Sprintf("Invalid end_of_turn_hooks url %q: %v", hook.URL, err), http.StatusBadRequest)
				return
			}
		}
	}

	conversation, err := s.db.CreateConversation(ctx, nil, true, cwdPtr, &modelID, convOpts)
	if err != nil {
		s.logger.Error("Failed to create conversation", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	conversationID := conversation.ConversationID

	// Run new-conversation hook, which may override prompt, model, and cwd.
	// Hook failures abort the request.
	hookResult, hookErr := RunNewConversationHookIn(s.hooksDir, NewConversationHookInput{
		Prompt: req.Message,
		Model:  modelID,
		Cwd:    derefString(cwdPtr),
		Readonly: NewConversationReadonly{
			ConversationID: conversationID,
			IsOrchestrator: convOpts.IsOrchestrator(),
			Headers:        HookHeaders(r.Header),
		},
	})
	if hookErr != nil {
		s.logger.Error("new-conversation hook failed", "conversationID", conversationID, "error", hookErr)
		http.Error(w, "new-conversation hook failed", http.StatusInternalServerError)
		return
	}
	if hookResult.Cwd != derefString(cwdPtr) {
		if err := s.db.UpdateConversationCwd(ctx, conversationID, hookResult.Cwd); err != nil {
			s.logger.Error("Failed to update cwd from hook", "error", err)
		} else {
			conversation.Cwd = &hookResult.Cwd
		}
	}
	if hookResult.Model != modelID {
		newService, svcErr := s.llmManager.GetService(hookResult.Model)
		if svcErr != nil {
			s.logger.Error("Hook returned unsupported model, keeping original", "hookModel", hookResult.Model, "error", svcErr)
		} else {
			modelID = hookResult.Model
			llmService = newService
			if err := s.db.ForceUpdateConversationModel(ctx, conversationID, modelID); err != nil {
				s.logger.Error("Failed to update model from hook", "error", err)
			}
		}
	}
	req.Message = hookResult.Prompt

	// If the hook supplied a slug, apply it now (synchronously) so that the
	// first-message goroutine below can skip its async LLM slug generation.
	// On failure (sanitize-to-empty, unique collision, DB error) we silently
	// fall back to the async slug; that path also handles uniqueness via
	// numeric suffixes.
	hookSlugApplied := false
	if sanitized := slug.Sanitize(hookResult.Slug); sanitized != "" {
		if _, err := s.db.UpdateConversationSlug(ctx, conversationID, sanitized); err != nil {
			s.logger.Warn("Failed to apply slug from new-conversation hook; falling back to async slug", "conversationID", conversationID, "slug", sanitized, "error", err)
		} else {
			hookSlugApplied = true
		}
	}

	// Notify conversation list subscribers about the new conversation
	go s.publishConversationListUpdate(ConversationListUpdate{
		Type:         "update",
		Conversation: conversation,
	})

	userEmail := r.Header.Get("X-ExeDev-Email")

	// Get or create conversation manager
	manager, err := s.getOrCreateConversationManager(ctx, conversationID, userEmail)
	if errors.Is(err, errConversationModelMismatch) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Slash-command hook: see handleChatConversation for semantics.
	slashResult := RunSlashCommandHook(SlashCommandHookInput{
		RawMessage:        req.Message,
		ConversationID:    conversationID,
		IsNewConversation: true,
		Cwd:               hookResult.Cwd,
		Model:             modelID,
		UserEmail:         userEmail,
		IsOrchestrator:    convOpts.IsOrchestrator(),
	})
	if slashResult.Err != nil {
		s.logger.Error("slash-command hook failed", "conversationID", conversationID, "error", slashResult.Err)
		http.Error(w, fmt.Sprintf("slash command failed: %v", slashResult.Err), http.StatusBadRequest)
		return
	}
	if slashResult.Handled && slashResult.Message != "" {
		req.Message = slashResult.Message
	}

	// Create user message
	userMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: req.Message},
		},
	}

	firstMessage, err := manager.AcceptUserMessage(ctx, llmService, modelID, userMessage)
	if errors.Is(err, errConversationModelMismatch) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.logger.Error("Failed to accept user message", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if firstMessage && !hookSlugApplied {
		ctxNoCancel := context.WithoutCancel(ctx)
		go func() {
			slugCtx, cancel := context.WithTimeout(ctxNoCancel, 15*time.Second)
			defer cancel()
			_, err := slug.GenerateSlug(slugCtx, s.llmManager, s.db, s.logger, conversationID, req.Message, modelID)
			if err != nil {
				s.logger.Warn("Failed to generate slug for conversation", "conversationID", conversationID, "error", err)
			} else {
				go s.notifySubscribers(ctxNoCancel, conversationID)
			}
		}()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "accepted",
		"conversation_id": conversationID,
	})
}

// handleCancelConversation handles POST /conversation/<id>/cancel
func (s *Server) handleCancelConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Get the conversation manager if it exists
	s.mu.Lock()
	manager, exists := s.activeConversations[conversationID]
	s.mu.Unlock()

	if !exists {
		// No active conversation to cancel
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "no_active_conversation"})
		return
	}

	// Cancel the conversation
	if err := manager.CancelConversation(ctx); err != nil {
		s.logger.Error("Failed to cancel conversation", "conversationID", conversationID, "error", err)
		http.Error(w, "Failed to cancel conversation", http.StatusInternalServerError)
		return
	}

	s.logger.Info("Conversation cancelled", "conversationID", conversationID)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "cancelled"})
}

// handleRetryConversation handles POST /api/conversation/<id>/retry.
// It flags the most recent error message with retried=true in user_data
// (the messages list is an append-only log; we never delete) and re-runs
// the LLM request that previously failed, using the conversation's current
// state. The error message is excluded from LLM history by
// partitionMessages, so no synthetic retry-user-message is sent to the
// model. Requires a latest message of type "error" that is classified
// retryable and not yet retried.
func (s *Server) handleRetryConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Validate that there's actually a retryable error to act on BEFORE
	// spinning up a fresh loop with tools and a 12-hour context. This keeps
	// the cold-storage path lightweight when the request is bogus.
	latest, err := s.db.GetLatestMessage(ctx, conversationID)
	if err != nil {
		s.logger.Warn("Retry: failed to load latest message", "conversationID", conversationID, "error", err)
		http.Error(w, "conversation not found or empty", http.StatusNotFound)
		return
	}
	if latest.Type != string(db.MessageTypeError) {
		// Idempotent no-op: a previous retry click already flagged the error.
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"status": "already_retried"})
		return
	}
	// Inspect user_data to enforce retryable=true and short-circuit if already
	// retried, before spinning up a loop with tools and a 12-hour context.
	if latest.UserData != nil && *latest.UserData != "" {
		var ud map[string]any
		if err := json.Unmarshal([]byte(*latest.UserData), &ud); err == nil {
			if retried, _ := ud["retried"].(bool); retried {
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(map[string]string{"status": "already_retried"})
				return
			}
			if retryable, _ := ud["retryable"].(bool); !retryable {
				http.Error(w, "error is not retryable", http.StatusConflict)
				return
			}
		}
	}

	s.mu.Lock()
	manager, exists := s.activeConversations[conversationID]
	s.mu.Unlock()

	if !exists {
		// No in-memory manager: hydrate one so we can retry from cold storage.
		userEmail := r.Header.Get("X-ExeDev-Email")
		manager, err = s.getOrCreateConversationManager(ctx, conversationID, userEmail)
		if err != nil {
			s.logger.Error("Failed to get conversation manager for retry", "conversationID", conversationID, "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		modelID := manager.GetModel()
		if modelID == "" {
			modelID = s.effectiveDefaultModel(s.getModelList())
		}
		llmService, err := s.llmManager.GetService(modelID)
		if err != nil {
			http.Error(w, fmt.Sprintf("Unsupported model: %s", modelID), http.StatusBadRequest)
			return
		}
		if err := manager.Hydrate(ctx); err != nil {
			s.logger.Error("Failed to hydrate for retry", "conversationID", conversationID, "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if err := manager.ensureLoop(llmService, modelID); err != nil {
			s.logger.Error("Failed to ensure loop for retry", "conversationID", conversationID, "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}

	if err := manager.RetryLastLLMRequest(ctx); err != nil {
		if errors.Is(err, errRetryNotApplicable) {
			// Lost a race with a concurrent retry; treat as success.
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"status": "already_retried"})
			return
		}
		s.logger.Warn("Retry rejected", "conversationID", conversationID, "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.logger.Info("Retry triggered", "conversationID", conversationID)
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "retrying"})
}

// handleStreamConversation handles GET /conversation/<id>/stream.
// See API.md for query params; see handleStream for the unified stream.
func (s *Server) handleStreamConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	s.runStream(w, r, conversationID, false)
}

// handleStream handles GET /api/stream2 — the unified SSE stream that
// combines per-conversation messages with conversation-list patch
// events. See API.md for query params.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	s.runStream(w, r, r.URL.Query().Get("conversation"), true)
}

func (s *Server) runStream(w http.ResponseWriter, r *http.Request, conversationID string, includeConversationListPatches bool) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancelStream := context.WithCancel(r.Context())
	defer cancelStream()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	query := r.URL.Query()
	var listInitial []ConversationListPatchEvent
	var listNext func() (ConversationListPatchEvent, bool)
	var listRelease func()
	if includeConversationListPatches {
		var err error
		listInitial, listNext, listRelease, err = s.conversationListStream.connect(ctx, query.Get("conversation_list_hash"))
		if err != nil {
			s.logger.Error("failed to initialize conversation list patches", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		defer listRelease()
	}

	// last_sequence_id: deliver only messages with sequence_id > N.
	// tail: first frame contains only the last N messages.
	// The two are mutually exclusive.
	lastSeqRaw := query.Get("last_sequence_id")
	tailRaw := query.Get("tail")
	if lastSeqRaw != "" && tailRaw != "" {
		http.Error(w, "last_sequence_id and tail are mutually exclusive", http.StatusBadRequest)
		return
	}
	lastSeqID := int64(-1)
	if lastSeqRaw != "" {
		parsed, err := strconv.ParseInt(lastSeqRaw, 10, 64)
		if err != nil || parsed < 0 {
			http.Error(w, "invalid last_sequence_id", http.StatusBadRequest)
			return
		}
		lastSeqID = parsed
	}
	var tailN int64
	if tailRaw != "" {
		parsed, err := strconv.ParseInt(tailRaw, 10, 64)
		if err != nil || parsed <= 0 {
			http.Error(w, "invalid tail", http.StatusBadRequest)
			return
		}
		tailN = parsed
	}

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Vary", "Accept-Encoding")

	// Compress SSE stream when the client accepts gzip or zstd. We keep a
	// single gzip/zstd stream for the lifetime of the response and Flush()
	// after every SSE record so the message is on the wire immediately.
	//
	// Using a single stream (rather than one independent gzip/zstd frame per
	// message) avoids a subtle decoder issue: Go's net/http transparently
	// gunzips gzip responses with multistream enabled, which means it won't
	// surface the bytes of frame N until it has at least started reading
	// frame N+1's header — fine for batch downloads, fatal for SSE.
	//
	// Critically, we set Content-Encoding and instantiate the compressor
	// lazily, only when we're about to write the first frame. Code below
	// can still fail (DB lookups, conversation hydration) and reply with a
	// plain http.Error; if we'd already set Content-Encoding the client
	// would try to gunzip that plain-text error body and choke.
	encoding, acceptable := pickStreamEncoding(r)
	if !acceptable {
		http.Error(w, "no acceptable encoding", http.StatusNotAcceptable)
		return
	}
	var (
		compressedSink  io.Writer = w
		flushCompressor           = func() error { return nil }
		closeCompressor           = func() error { return nil }
		streamStarted   bool
	)
	defer func() {
		if err := closeCompressor(); err != nil {
			s.logger.Debug("conversation stream compressor close failed", "error", err)
		}
	}()
	initCompression := func() bool {
		if streamStarted {
			return true
		}
		switch encoding {
		case "zstd":
			// NewWriter only fails on invalid options, all of which are
			// hardcoded here, so treat any error as a server bug.
			zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedDefault))
			if err != nil {
				s.logger.Error("zstd writer init failed", "error", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return false
			}
			compressedSink = zw
			flushCompressor = zw.Flush
			closeCompressor = zw.Close
			w.Header().Set("Content-Encoding", "zstd")
		case "gzip":
			gz := gzip.NewWriter(w)
			compressedSink = gz
			flushCompressor = gz.Flush
			closeCompressor = gz.Close
			w.Header().Set("Content-Encoding", "gzip")
		}
		streamStarted = true
		return true
	}

	writeStreamData := func(streamData StreamResponse) bool {
		if !initCompression() {
			return false
		}
		data, err := json.Marshal(streamData)
		if err != nil {
			s.logger.Debug("failed to marshal stream response", "error", err)
			return false
		}
		if _, err := fmt.Fprintf(compressedSink, "data: %s\n\n", data); err != nil {
			s.logger.Debug("conversation stream write failed", "error", err)
			return false
		}
		if err := flushCompressor(); err != nil {
			s.logger.Debug("conversation stream compressor flush failed", "error", err)
			return false
		}
		flusher.Flush()
		return true
	}

	// errAfterStreamStart abandons the stream after a fatal post-write error.
	// Once any SSE frame has been written, the response is committed and we
	// must not call http.Error (which would inject uncompressed bytes into the
	// gzip/zstd body). Simply return; the client treats the closed connection
	// as a transient drop and reconnects via last_sequence_id.
	errAfterStreamStart := func(w http.ResponseWriter, msg string) {
		if streamStarted {
			s.logger.Debug("abandoning compressed SSE stream after error", "msg", msg)
			return
		}
		http.Error(w, msg, http.StatusInternalServerError)
	}

	for _, event := range listInitial {
		patch := event
		if !writeStreamData(StreamResponse{ConversationListPatch: &patch}) {
			return
		}
	}

	// For per-conversation streams on the unified /api/stream2 endpoint that
	// have no list replay to emit, send a bare heartbeat *before* the blocking
	// per-conversation work (Hydrate, message read) so the client always sees
	// a first flush within milliseconds. Hydrate walks the working tree for
	// guidance and skill files, which under load on CI has taken several
	// seconds — long enough to time out client waits and to look like a hung
	// connection. We restrict this to the unified endpoint to avoid changing
	// the first-frame contract of the legacy /api/conversation/<id>/stream
	// endpoint, where the first frame is expected to carry messages.
	//
	// List-only streams (conversationID == "") keep their contract: when a
	// matching conversation_list_hash means there's nothing to replay, the
	// stream stays silent until the next real event.
	if conversationID != "" && includeConversationListPatches && len(listInitial) == 0 {
		if !writeStreamData(StreamResponse{Heartbeat: true}) {
			return
		}
	}

	updates := make(chan StreamResponse, 10)

	if listNext != nil {
		go func() {
			for {
				event, ok := listNext()
				if !ok {
					return
				}
				patch := event
				select {
				case updates <- StreamResponse{ConversationListPatch: &patch}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// On /api/stream2 we subscribe to the server-wide streamPub once and receive
	// events for ALL active conversations on the same connection. The optional
	// ?conversation= parameter governs only backfill of that conversation's
	// initial history (handled below).
	if includeConversationListPatches && s.streamPub != nil {
		next := s.streamPub.Subscribe(ctx, -1)
		go func() {
			for {
				streamData, cont := next()
				if !cont {
					return
				}
				select {
				case updates <- streamData:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	if conversationID == "" {
		// Stream without backfill: forward events from streamPub and list
		// patches, with a periodic heartbeat so intermediaries don't time
		// the connection out.
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !writeStreamData(StreamResponse{Heartbeat: true}) {
					return
				}
			case streamData := <-updates:
				if !writeStreamData(streamData) {
					return
				}
			}
		}
	}

	// For fresh connections, get messages BEFORE calling getOrCreateConversationManager.
	// This is important because getOrCreateConversationManager may create a system prompt
	// message during hydration, and we want to return the messages as they were before.
	var messages []generated.Message
	var conversation generated.Conversation
	// resuming: client is not asking for the full history, so skip the
	// context_window_size calculation (which only makes sense over it).
	resuming := lastSeqID >= 0 || tailN > 0
	switch {
	case tailN > 0:
		err := s.db.Queries(ctx, func(q *generated.Queries) error {
			var err error
			messages, err = q.ListMessagesTail(ctx, generated.ListMessagesTailParams{
				ConversationID: conversationID,
				Limit:          tailN,
			})
			if err != nil {
				return err
			}
			conversation, err = q.GetConversation(ctx, conversationID)
			return err
		})
		if err != nil {
			s.logger.Error("Failed to get conversation data", "conversationID", conversationID, "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if len(messages) > 0 {
			lastSeqID = messages[len(messages)-1].SequenceID
		} else {
			lastSeqID = 0
		}
	case lastSeqID < 0:
		err := s.db.Queries(ctx, func(q *generated.Queries) error {
			var err error
			messages, err = q.ListMessages(ctx, conversationID)
			if err != nil {
				return err
			}
			conversation, err = q.GetConversation(ctx, conversationID)
			return err
		})
		if err != nil {
			s.logger.Error("Failed to get conversation data", "conversationID", conversationID, "error", err)
			errAfterStreamStart(w, "Internal server error")
			return
		}
		if len(messages) > 0 {
			lastSeqID = messages[len(messages)-1].SequenceID
		}
	default:
		err := s.db.Queries(ctx, func(q *generated.Queries) error {
			var err error
			messages, err = q.ListMessagesSince(ctx, generated.ListMessagesSinceParams{
				ConversationID: conversationID,
				SequenceID:     lastSeqID,
			})
			if err != nil {
				return err
			}
			conversation, err = q.GetConversation(ctx, conversationID)
			return err
		})
		if err != nil {
			s.logger.Error("Failed to get conversation data", "conversationID", conversationID, "error", err)
			errAfterStreamStart(w, "Internal server error")
			return
		}
		if len(messages) > 0 {
			lastSeqID = messages[len(messages)-1].SequenceID
		}
	}

	manager, err := s.getOrCreateConversationManager(ctx, conversationID, "")
	if err != nil {
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
		errAfterStreamStart(w, "Internal server error")
		return
	}

	// On /api/stream2, live events arrive via the server-wide streamPub
	// subscription set up above. The per-conversation subpub is used only by
	// the legacy /api/conversation/<id>/stream endpoint.
	//
	// Subscribe BEFORE sending initial data so we don't miss broadcasts that
	// happen between the DB query and the start of the event loop. The subpub
	// channel is buffered (10), so events arriving while we write the initial
	// response are queued rather than lost.
	var next func() (StreamResponse, bool)
	if !includeConversationListPatches {
		next = manager.subpub.Subscribe(ctx, lastSeqID)
	}

	if len(messages) > 0 {
		apiMessages := toAPIMessages(messages)
		// Only send context_window_size for fresh connections where we have all messages.
		// On resume we only have the missed messages, so the calculation would be wrong.
		// The client keeps its previous value and gets updates from subsequent stream events.
		var ctxSize uint64
		if !resuming {
			ctxSize = calculateContextWindowSize(apiMessages)
		}
		streamData := StreamResponse{
			ConversationID: conversationID,
			Messages:       apiMessages,
			Conversation:   &conversation,
			ConversationState: &ConversationState{
				ConversationID: conversationID,
				Working:        conversation.AgentWorking,
				Model:          manager.GetModel(),
			},
			ContextWindowSize: ctxSize,
		}
		if !writeStreamData(streamData) {
			return
		}
	} else {
		// Either resuming or no messages yet - send current state as heartbeat
		streamData := StreamResponse{
			ConversationID: conversationID,
			Conversation:   &conversation,
			ConversationState: &ConversationState{
				ConversationID: conversationID,
				Working:        conversation.AgentWorking,
				Model:          manager.GetModel(),
			},
			Heartbeat: true,
		}
		if !writeStreamData(streamData) {
			return
		}
	}

	// Marker between the initial replay and live updates. Sent once
	// per connection; the connection stays open and live frames follow.
	if !writeStreamData(StreamResponse{SnapshotComplete: true}) {
		return
	}

	// Start heartbeat goroutine - sends state every 30 seconds if no other messages
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeatDone:
				return
			case <-ticker.C:
				// Get current conversation state for heartbeat
				var conv generated.Conversation
				err := s.db.Queries(ctx, func(q *generated.Queries) error {
					var err error
					conv, err = q.GetConversation(ctx, conversationID)
					return err
				})
				if err != nil {
					continue // Skip heartbeat on error
				}

				heartbeat := StreamResponse{
					Conversation: &conv,
					ConversationState: &ConversationState{
						ConversationID: conversationID,
						Working:        conv.AgentWorking,
						Model:          manager.GetModel(),
					},
					Heartbeat: true,
				}
				manager.broadcastStream(heartbeat)
			}
		}
	}()
	defer close(heartbeatDone)

	if next != nil {
		go func() {
			for {
				streamData, cont := next()
				if !cont {
					return
				}
				select {
				case updates <- streamData:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Local heartbeat keeps the connection alive even when no active
			// manager is broadcasting one (e.g., on /api/stream2 with no
			// activity across any conversation).
			if !writeStreamData(StreamResponse{Heartbeat: true}) {
				return
			}
		case streamData := <-updates:
			// Always forward updates, even if only the conversation changed (e.g., slug added).
			if !writeStreamData(streamData) {
				return
			}
		}
	}
}

// handleVersion returns build information plus the capabilities list as
// JSON. The capabilities slot exists so clients can negotiate optional,
// additive features without reshaping the response; the set is currently
// empty.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resp := struct {
		version.Info
		Capabilities []string `json:"capabilities"`
	}{
		Info:         version.GetInfo(),
		Capabilities: version.Capabilities(),
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Error("Failed to encode version response", "error", err)
	}
}

// imageCapable is an optional interface that LLM services may implement to
// report whether they accept image inputs.
type imageCapable interface {
	SupportsImages() bool
}

// ModelInfo represents a model in the API response
type ModelInfo struct {
	ID               string `json:"id"`
	DisplayName      string `json:"display_name,omitempty"`
	Source           string `json:"source,omitempty"`   // Human-readable source (e.g., "exe.dev gateway", "$ANTHROPIC_API_KEY")
	BaseURL          string `json:"base_url,omitempty"` // Upstream origin (e.g., "https://llm.int.exe.xyz")
	APIType          string `json:"api_type,omitempty"` // Wire protocol (e.g., "anthropic-messages")
	Ready            bool   `json:"ready"`
	MaxContextTokens int    `json:"max_context_tokens,omitempty"`
	IsDefault        bool   `json:"is_default,omitempty"`
	SupportsImages   bool   `json:"supports_images"`
}

// getModelList returns the list of available models
func (s *Server) getModelList() []ModelInfo {
	modelList := []ModelInfo{}
	if s.predictableOnly {
		modelList = append(modelList, ModelInfo{ID: "predictable", Ready: true, MaxContextTokens: 200000, SupportsImages: true})
	} else {
		modelIDs := s.llmManager.GetAvailableModels()
		for _, id := range modelIDs {
			// Skip predictable model unless predictable-only flag is set
			if id == "predictable" {
				continue
			}
			svc, err := s.llmManager.GetService(id)
			maxCtx := 0
			supportsImages := false
			if err == nil && svc != nil {
				maxCtx = svc.TokenContextWindow()
				if c, ok := svc.(imageCapable); ok {
					supportsImages = c.SupportsImages()
				} else {
					// Services that don't expose the capability are assumed
					// to support images for backwards compatibility, matching
					// loggingService.SupportsImages.
					supportsImages = true
				}
			}
			info := ModelInfo{ID: id, Ready: err == nil, MaxContextTokens: maxCtx, SupportsImages: supportsImages}
			// Add display name and source from model info
			if modelInfo := s.llmManager.GetModelInfo(id); modelInfo != nil {
				info.DisplayName = modelInfo.DisplayName
				info.Source = modelInfo.Source
				info.BaseURL = modelInfo.BaseURL
				info.APIType = modelInfo.APIType
			}
			modelList = append(modelList, info)
		}
	}
	return modelList
}

// effectiveDefaultModel returns the model id to use when the client
// hasn't picked one. It tries `s.defaultModel`, then the process-wide
// default from package models, then the first ready model in
// `modelList`. Returns "" only when no model is ready, which is the
// same signal `getModelList` produces when the host has no working
// LLM service at all.
func (s *Server) effectiveDefaultModel(modelList []ModelInfo) string {
	if len(modelList) == 0 {
		return ""
	}
	candidates := []string{s.defaultModel, models.Default().ID}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		for _, m := range modelList {
			if m.ID == c && m.Ready {
				return c
			}
		}
	}
	for _, m := range modelList {
		if m.Ready {
			return m.ID
		}
	}
	return ""
}

// handleModels returns the list of available models
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	modelList := s.getModelList()
	markDefaultModel(modelList, s.effectiveDefaultModel(modelList))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(modelList)
}

type builtModelRefresher interface {
	RefreshBuiltModels([]models.Built) error
}

// handleModelRefresh refreshes the non-custom model catalog and returns the
// same shape as GET /api/models.
func (s *Server) handleModelRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.refreshBuiltModels == nil {
		http.Error(w, "model refresh is not configured", http.StatusNotImplemented)
		return
	}
	refresher, ok := s.llmManager.(builtModelRefresher)
	if !ok {
		http.Error(w, "model manager does not support refresh", http.StatusInternalServerError)
		return
	}
	builtModels, err := s.refreshBuiltModels(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := refresher.RefreshBuiltModels(builtModels); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	modelList := s.getModelList()
	markDefaultModel(modelList, s.effectiveDefaultModel(modelList))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(modelList)
}

// markDefaultModel sets IsDefault=true on the entry matching defaultID.
func markDefaultModel(modelList []ModelInfo, defaultID string) {
	if defaultID == "" {
		return
	}
	for i := range modelList {
		if modelList[i].ID == defaultID {
			modelList[i].IsDefault = true
			return
		}
	}
}

// handleTools returns the list of tools available to conversations.
func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"tools": claudetool.ToolRegistry,
	})
}

// handleSearchConversations handles GET /api/conversations/search?q=...
// Performs an FTS5 full-text search across active AND archived top-level
// conversations, returning the same shape as /api/conversations so the UI
// can render results directly.
func (s *Server) handleSearchConversations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 200
	offset := 0
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}
	results, err := s.searchConversationsFTSWithState(r.Context(), query, limit, offset)
	if err != nil {
		s.logger.Error("Failed to search conversations", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []ConversationWithState{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// handleArchivedConversations handles GET /api/conversations/archived
func (s *Server) handleArchivedConversations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	limit := 5000
	offset := 0
	var query string

	// Parse query parameters
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}
	query = r.URL.Query().Get("q")

	// Get archived conversations from database
	var conversations []generated.Conversation
	var err error

	if query != "" {
		conversations, err = s.db.SearchArchivedConversations(ctx, query, int64(limit), int64(offset))
	} else {
		conversations, err = s.db.ListArchivedConversations(ctx, int64(limit), int64(offset))
	}

	if err != nil {
		s.logger.Error("Failed to get archived conversations", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conversations)
}

// handleArchiveConversation handles POST /conversation/<id>/archive
func (s *Server) handleArchiveConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	conversation, err := s.db.ArchiveConversation(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to archive conversation", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Notify conversation list subscribers
	go s.publishConversationListUpdate(ConversationListUpdate{
		Type:         "update",
		Conversation: conversation,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conversation)
}

// handleUnarchiveConversation handles POST /conversation/<id>/unarchive
func (s *Server) handleUnarchiveConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	conversation, err := s.db.UnarchiveConversation(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to unarchive conversation", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Notify conversation list subscribers
	go s.publishConversationListUpdate(ConversationListUpdate{
		Type:         "update",
		Conversation: conversation,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conversation)
}

// handleDeleteConversation handles POST /conversation/<id>/delete
func (s *Server) handleDeleteConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	if err := s.db.DeleteConversation(ctx, conversationID); err != nil {
		s.logger.Error("Failed to delete conversation", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Notify conversation list subscribers about the deletion
	go s.publishConversationListUpdate(ConversationListUpdate{
		Type:           "delete",
		ConversationID: conversationID,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

// handleConversationBySlug handles GET /api/conversation-by-slug/<slug>
func (s *Server) handleConversationBySlug(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slug := strings.TrimPrefix(r.URL.Path, "/api/conversation-by-slug/")
	if slug == "" {
		http.Error(w, "Slug required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	conversation, err := s.db.GetConversationBySlug(ctx, slug)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "Conversation not found", http.StatusNotFound)
			return
		}
		s.logger.Error("Failed to get conversation by slug", "slug", slug, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conversation)
}

// RenameRequest represents a request to rename a conversation
type RenameRequest struct {
	Slug string `json:"slug"`
}

// handleRenameConversation handles POST /conversation/<id>/rename
func (s *Server) handleRenameConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	var req RenameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Sanitize the slug using the same rules as auto-generated slugs
	sanitized := slug.Sanitize(req.Slug)
	if sanitized == "" {
		http.Error(w, "Slug is required (must contain alphanumeric characters)", http.StatusBadRequest)
		return
	}

	conversation, err := s.db.UpdateConversationSlug(ctx, conversationID, sanitized)
	if err != nil {
		s.logger.Error("Failed to rename conversation", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Notify conversation list subscribers
	go s.publishConversationListUpdate(ConversationListUpdate{
		Type:         "update",
		Conversation: conversation,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conversation)
}

// TagsRequest represents a request to update a conversation's tags.
type TagsRequest struct {
	Tags []string `json:"tags"`
}

// normalizeTags trims whitespace and removes empty/duplicate entries while
// preserving first-seen order. Comparison is case-sensitive; callers can
// lowercase upstream if they want case-insensitive dedupe.
func normalizeTags(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// handleUpdateConversationTags handles POST /conversation/<id>/tags
func (s *Server) handleUpdateConversationTags(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req TagsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	tags := normalizeTags(req.Tags)
	conversation, err := s.db.UpdateConversationTags(r.Context(), conversationID, tags)
	if err != nil {
		s.logger.Error("Failed to update conversation tags", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	go s.publishConversationListUpdate(ConversationListUpdate{
		Type:         "update",
		Conversation: conversation,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conversation)
}

// handleVersionCheck returns version check information including update availability
func (s *Server) handleVersionCheck(w http.ResponseWriter, r *http.Request) {
	forceRefresh := r.URL.Query().Get("refresh") == "true"

	info, err := s.versionChecker.Check(r.Context(), forceRefresh)
	if err != nil {
		s.logger.Error("Version check failed", "error", err)
		http.Error(w, "Version check failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// handleVersionChangelog returns the changelog between current and latest versions
func (s *Server) handleVersionChangelog(w http.ResponseWriter, r *http.Request) {
	currentTag := r.URL.Query().Get("current")
	latestTag := r.URL.Query().Get("latest")

	if currentTag == "" || latestTag == "" {
		http.Error(w, "current and latest query parameters are required", http.StatusBadRequest)
		return
	}

	commits, err := s.versionChecker.FetchChangelog(r.Context(), currentTag, latestTag)
	if err != nil {
		s.logger.Error("Failed to fetch changelog", "error", err, "current", currentTag, "latest", latestTag)
		http.Error(w, "Failed to fetch changelog", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(commits)
}

// handleUpgrade performs a self-update of the Shelley binary
// If restart=true query parameter is set, it will also restart after upgrade
func (s *Server) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	err := s.versionChecker.DoUpgrade(r.Context())
	if err != nil {
		s.logger.Error("Upgrade failed", "error", err)
		http.Error(w, fmt.Sprintf("Upgrade failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Check if restart was requested
	restart := r.URL.Query().Get("restart") == "true"

	w.Header().Set("Content-Type", "application/json")
	if restart {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Upgrade complete. Restarting..."})

		// Flush the response
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		// Exit after a short delay to allow response to be sent
		go func() {
			time.Sleep(100 * time.Millisecond)
			s.logger.Info("Exiting Shelley after upgrade")
			os.Exit(0)
		}()
	} else {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Upgrade complete. Restart to apply."})
	}
}

// handleUpgradeHeadlessShell downloads and installs the latest headless-shell tarball.
func (s *Server) handleUpgradeHeadlessShell(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Check that we have a local headless-shell to upgrade
	if _, err := os.Stat(headlessShellPath); err != nil {
		http.Error(w, "headless-shell not installed at "+headlessShellPath, http.StatusBadRequest)
		return
	}

	if !isSudoAvailable() {
		http.Error(w, "sudo is required to upgrade headless-shell", http.StatusInternalServerError)
		return
	}

	// Get cached version info
	info, err := s.versionChecker.Check(ctx, false)
	if err != nil {
		http.Error(w, fmt.Sprintf("Version check failed: %v", err), http.StatusInternalServerError)
		return
	}
	if !info.HeadlessShellUpdate {
		http.Error(w, "No headless-shell update available", http.StatusBadRequest)
		return
	}
	if info.ReleaseInfo == nil {
		http.Error(w, "No release info available", http.StatusInternalServerError)
		return
	}

	downloadURL := findHeadlessShellURL(info.ReleaseInfo)
	if downloadURL == "" {
		http.Error(w, fmt.Sprintf("No headless-shell download for %s/%s", runtime.GOOS, runtime.GOARCH), http.StatusBadRequest)
		return
	}

	s.logger.Info("Upgrading headless-shell", "url", downloadURL, "from", info.HeadlessShellCurrent, "to", info.HeadlessShellLatest)

	// Download the tarball
	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create request: %v", err), http.StatusInternalServerError)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to download: %v", err), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("Download returned status %d", resp.StatusCode), http.StatusInternalServerError)
		return
	}

	// Write tarball to a temp file
	tmpTar, err := os.CreateTemp("", "headless-shell-*.tar.gz")
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create temp file: %v", err), http.StatusInternalServerError)
		return
	}
	tmpTarPath := tmpTar.Name()
	defer os.Remove(tmpTarPath)

	if _, err := io.Copy(tmpTar, resp.Body); err != nil {
		tmpTar.Close()
		http.Error(w, fmt.Sprintf("Failed to save tarball: %v", err), http.StatusInternalServerError)
		return
	}
	tmpTar.Close()

	// Extract to a temp directory
	tmpDir, err := os.MkdirTemp("", "headless-shell-extract-*")
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create temp dir: %v", err), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.CommandContext(ctx, "tar", "xzf", tmpTarPath, "-C", tmpDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		s.logger.Error("Failed to extract tarball", "error", err, "output", string(output))
		http.Error(w, fmt.Sprintf("Failed to extract: %v\n%s", err, output), http.StatusInternalServerError)
		return
	}

	// Verify the extracted binary works
	extractedBin := filepath.Join(tmpDir, "headless-shell", "headless-shell")
	cmd = exec.CommandContext(ctx, extractedBin, "--version")
	versionOutput, err := cmd.Output()
	if err != nil {
		s.logger.Error("Extracted headless-shell --version failed", "error", err)
		http.Error(w, fmt.Sprintf("Extracted binary failed --version: %v", err), http.StatusInternalServerError)
		return
	}
	newVersion := strings.TrimSpace(string(versionOutput))
	s.logger.Info("New headless-shell version verified", "version", newVersion)

	// Replace the install dir using sudo: backup -> move new -> cleanup
	installDir := filepath.Dir(headlessShellPath)
	backupDir := installDir + ".old"

	exec.CommandContext(ctx, "sudo", "rm", "-rf", backupDir).Run()

	cmd = exec.CommandContext(ctx, "sudo", "mv", installDir, backupDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		s.logger.Error("Failed to backup old headless-shell", "error", err, "output", string(output))
		http.Error(w, fmt.Sprintf("Failed to backup old installation: %v\n%s", err, output), http.StatusInternalServerError)
		return
	}

	cmd = exec.CommandContext(ctx, "sudo", "mv", filepath.Join(tmpDir, "headless-shell"), installDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		// Rollback: restore backup (use background context since request ctx may be cancelled)
		exec.CommandContext(context.Background(), "sudo", "mv", backupDir, installDir).Run()
		s.logger.Error("Failed to install new headless-shell", "error", err, "output", string(output))
		http.Error(w, fmt.Sprintf("Failed to install: %v\n%s", err, output), http.StatusInternalServerError)
		return
	}

	exec.CommandContext(ctx, "sudo", "chmod", "-R", "a+rx", installDir).Run()
	exec.CommandContext(ctx, "sudo", "rm", "-rf", backupDir).Run()

	// Invalidate version cache
	vc := s.versionChecker
	vc.mu.Lock()
	vc.cachedInfo = nil
	vc.mu.Unlock()

	s.logger.Info("headless-shell upgraded successfully", "version", newVersion)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"message": "Upgraded to " + newVersion,
		"version": newVersion,
	})
}

// handleExit exits the process, expecting systemd or similar to restart it
func (s *Server) handleExit(w http.ResponseWriter, r *http.Request) {
	// Send response before exiting
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Exiting..."})

	// Flush the response
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Exit after a short delay to allow response to be sent
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.logger.Info("Exiting Shelley via /exit endpoint")
		os.Exit(0)
	}()
}

// handleGetSettings retrieves all settings
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.db.GetAllSettings(r.Context())
	if err != nil {
		s.logger.Error("Failed to get settings", "error", err)
		http.Error(w, fmt.Sprintf("Failed to get settings: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

// handleSetSetting sets a single setting
func (s *Server) handleSetSetting(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Key == "" {
		http.Error(w, "Key is required", http.StatusBadRequest)
		return
	}

	// Only allow known setting keys
	allowedKeys := map[string]bool{
		"auto_upgrade": true,
	}
	if !allowedKeys[req.Key] {
		http.Error(w, fmt.Sprintf("Invalid setting key: %s", req.Key), http.StatusBadRequest)
		return
	}

	if err := s.db.SetSetting(r.Context(), req.Key, req.Value); err != nil {
		s.logger.Error("Failed to set setting", "error", err, "key", req.Key)
		http.Error(w, fmt.Sprintf("Failed to set setting: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleCancelQueued handles POST /conversation/<id>/cancel-queued
// Cancels all pending queued messages for a conversation.
func (s *Server) handleCancelQueued(w http.ResponseWriter, r *http.Request, conversationID string) {
	s.mu.Lock()
	manager, ok := s.activeConversations[conversationID]
	s.mu.Unlock()

	if !ok {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}

	manager.CancelQueuedMessages(r.Context(), s)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type RegisterConversationHookRequest struct {
	URL string `json:"url"`
}

func (s *Server) handleRegisterConversationHook(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RegisterConversationHookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if err := validateConversationHookURL(req.URL); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	manager, err := s.getOrCreateConversationManager(r.Context(), conversationID, r.Header.Get("X-ExeDev-Email"))
	if errors.Is(err, errConversationModelMismatch) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if err := manager.RegisterEndOfTurnHook(r.Context(), db.ConversationHook{URL: req.URL}); err != nil {
		s.logger.Error("Failed to register conversation hook", "conversationID", conversationID, "hook_url", req.URL, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "registered"})
}

// handleStartNewGeneration handles POST /conversation/<id>/new-generation.
func (s *Server) handleStartNewGeneration(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	conversation, err := s.startNewGeneration(ctx, conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Error("Failed to start new generation", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conversation)
}

func (s *Server) startNewGeneration(ctx context.Context, conversationID string) (generated.Conversation, error) {
	conversation, err := db.WithTxRes(s.db, ctx, func(q *generated.Queries) (generated.Conversation, error) {
		return q.IncrementConversationGeneration(ctx, conversationID)
	})
	if err != nil {
		return generated.Conversation{}, err
	}

	s.mu.Lock()
	manager, ok := s.activeConversations[conversationID]
	s.mu.Unlock()
	if !ok {
		manager, err = s.getOrCreateConversationManager(ctx, conversationID, "")
		if err != nil {
			return generated.Conversation{}, fmt.Errorf("hydrate after generation bump: %w", err)
		}
	} else {
		manager.ResetLoop()
	}

	// (Re-)hydrate so the new generation gets its system prompt created
	// before we tell anyone about the bump. ResetLoop above cleared the
	// hydrated flag so this re-runs system prompt creation.
	if err := manager.Hydrate(ctx); err != nil {
		return generated.Conversation{}, fmt.Errorf("hydrate after generation bump: %w", err)
	}

	// Re-fetch the conversation to pick up any timestamp changes from creating
	// the system prompt.
	if fresh, ferr := s.db.GetConversationByID(ctx, conversationID); ferr == nil {
		conversation = *fresh
	}

	// Broadcast any messages created for the new generation (typically just
	// the new system prompt) so subscribers see them right away.
	messages, err := s.db.ListMessages(ctx, conversationID)
	if err == nil {
		for i := range messages {
			if messages[i].Generation == conversation.CurrentGeneration {
				s.notifySubscribersNewMessage(ctx, conversationID, &messages[i])
			}
		}
	}

	manager.broadcastStream(StreamResponse{Conversation: &conversation})
	s.publishConversationListUpdate(ConversationListUpdate{Type: "update", Conversation: &conversation})

	return conversation, nil
}
