package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/gitstate"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
	"shelley.exe.dev/models/modelsdev"
	"shelley.exe.dev/slug"
	"shelley.exe.dev/subpub"
	"shelley.exe.dev/ui"
	"shelley.exe.dev/version"
)

func marshalDeltaBatchFrames(conversationID string, deltas []llm.StreamDelta) ([]byte, error) {
	var frames bytes.Buffer
	for i := range deltas {
		event := StreamResponse{
			ConversationID: conversationID,
			StreamDelta:    &deltas[i],
		}
		data, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		frames.WriteString("data: ")
		frames.Write(data)
		frames.WriteString("\n\n")
	}
	return frames.Bytes(), nil
}

// handleRead serves files from limited allowed locations via /api/read?path=
func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
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
		strings.HasPrefix(path, claudetool.OneShotImageDir+"/") ||
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
		http.Error(w, fmt.Sprintf("failed to write file: %v", err), s.errorStatus(err))
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

// maxEditableFileBytes caps how large a file handleReadFile will load into
// memory. The web editor is for source/config files, not large blobs.
const maxEditableFileBytes = 16 << 20 // 16 MiB

// handleReadFile returns the text content of an arbitrary file as JSON
// {path, content}. The editor modal loads files through this (rather than
// /api/read, which is restricted to image/upload dirs) so it can open any
// file the fuzzy finder surfaces. Writing already accepts arbitrary absolute
// paths via /api/write-file, so reading them is consistent.
func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) {
		http.Error(w, "absolute path required", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(clean)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}
	if info.Size() > maxEditableFileBytes {
		http.Error(w, "file too large to edit", http.StatusRequestEntityTooLarge)
		return
	}
	b, err := os.ReadFile(clean)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read file: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"path": clean, "content": string(b)})
}

const maxUploadBytes = 1 << 30 // 1 GiB

type uploadErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// handleUpload handles file uploads via POST /api/upload.
// Files are saved to the UploadDir with a random filename.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
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
			s.writeUploadSaveError(w, err)
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
		s.writeUploadSaveError(w, err)
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

func (s *Server) writeUploadSaveError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		writeUploadError(w, http.StatusRequestEntityTooLarge, "request_body_too_large", "request body too large")
		return
	}
	writeUploadError(w, s.errorStatus(err), "upload_save_failed", "failed to save file: "+err.Error())
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
	indexHandler := compressionHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		w.Header().Set("Content-Type", "text/html")
		s.serveIndexWithInit(w, r, fsys)
	}))

	// Load checksums for ETag support (content-based, not git-based)
	checksums := ui.Checksums()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Inject initialization data into index.html
		if r.URL.Path == "/" || r.URL.Path == "/index.html" || isConversationSlugPath(r.URL.Path) || isSPARoute(r.URL.Path) {
			indexHandler.ServeHTTP(w, r)
			return
		}

		// For JS, CSS, source-map and font files, serve from .gz files (only the
		// .gz versions are embedded to keep the binary small).
		if strings.HasSuffix(r.URL.Path, ".js") || strings.HasSuffix(r.URL.Path, ".css") || strings.HasSuffix(r.URL.Path, ".map") || strings.HasSuffix(r.URL.Path, ".woff2") {
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

			contentType := mime.TypeByExtension(filepath.Ext(r.URL.Path))
			// Go's builtin mime table has neither; don't depend on the host's.
			switch filepath.Ext(r.URL.Path) {
			case ".map":
				contentType = "application/json; charset=utf-8"
			case ".woff2":
				contentType = "font/woff2"
			}
			w.Header().Set("Content-Type", contentType)
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

	transcriptionErr := s.transcriber.Available()
	if transcriptionErr != nil && !errors.Is(transcriptionErr, errNoTranscriptionRoute) {
		s.logger.Error("Failed to resolve transcription models", "error", transcriptionErr)
	}

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
		"user_email":          strings.TrimSpace(r.Header.Get("X-ExeDev-Email")),
		// is_exe_dev lets the UI pick exe.dev-specific setup advice even when
		// model_setup_hint is absent (the catalog can empty AFTER page load, via
		// a detached integration plus Refresh).
		"is_exe_dev": isExeDev(),
		// Recording is pointless without a transcription route; the UI
		// explains how to get one instead of recording.
		"transcription_available": transcriptionErr == nil,
	}
	// With no models the UI cannot send anything, so tell it WHY. On exe.dev
	// the usual cause is a missing reflection or llm integration, and each has
	// a different fix; see modelSetupHintForModels. Only computed for the empty
	// case, so the healthy path pays no reflection probe.
	if hint := modelSetupHintForModels(r.Context(), modelList, isExeDev()); hint != "" {
		initData["model_setup_hint"] = hint
	}
	// On exe.dev VMs (where /exe.dev exists), auto-derive the terminal URL and
	// default links from the current hostname so they pick up hostname changes
	// on reload.
	if _, err := os.Stat("/exe.dev"); err == nil {
		short := strings.SplitN(hostname, ".", 2)[0]
		initData["terminal_url"] = "https://" + short + ".xterm.exe.xyz"
		// Home icon for the VM's management page.
		const homeIcon = "M3 12l2-2m0 0l7-7 7 7M5 10v10a1 1 0 001 1h3m10-11l2 2m-2-2v10a1 1 0 01-1 1h-3m-6 0a1 1 0 001-1v-4a1 1 0 011-1h2a1 1 0 011 1v4a1 1 0 001 1m-6 0h6"
		// External-link icon for the box's own web page.
		const extIcon = "M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14"
		initData["links"] = []Link{
			{Title: hostname, URL: "https://" + hostname, IconSVG: extIcon},
			{Title: "Manage on exe.dev", URL: "https://exe.dev/vm/" + short, IconSVG: homeIcon},
		}
	}

	// Inject notification channel type metadata for the settings modal
	initData["notification_channel_types"] = s.getNotificationChannelTypes()
	// Whether this VM has an exe.dev "notify" integration, so the UI can show
	// the auto-configured push-notification toggle.
	initData["exe_notify_available"] = s.exeNotifyAvailable()
	if s.Banner != "" {
		initData["banner"] = s.Banner
	}

	initJSON, err := json.Marshal(initData)
	if err != nil {
		http.Error(w, "Failed to marshal init data", http.StatusInternalServerError)
		return
	}

	fe, err := s.currentFaviconEmoji(r.Context())
	if err != nil {
		s.internalError(w, "Failed to get favicon emoji", err)
		return
	}
	faviconLink := fmt.Sprintf(`<link rel="icon" type="image/svg+xml" href="%s"/>`, html.EscapeString(faviconHref(fe.Emoji)))

	// Inject the script tag and favicon before </head>
	initScript := fmt.Sprintf(`<script>window.__SHELLEY_INIT__=%s;</script>`, initJSON)
	injection := faviconLink + initScript
	modifiedHTML := strings.Replace(string(indexHTML), "</head>", injection+"</head>", 1)

	w.Write([]byte(modifiedHTML))
}

// handleConfig returns server configuration
// handleConversations handles GET /conversations
func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
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
		s.internalError(w, "Failed to get conversations", err)
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
		s.internalError(w, "Failed to compute conversation list snapshot", err)
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
		conversations[i] = h.ConversationListItem
	}
	decorated := s.decorateConversations(conversations)
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
	return s.decorateConversations(conversations), nil
}

// decorateConversations adds git metadata to conversation list items.
// Everything else, working state and counts included, comes from the query
// that listed them (see db.ConversationListItem), so the list is one query.
func (s *Server) decorateConversations(conversations []db.ConversationListItem) []ConversationWithState {
	now := time.Now()
	result := make([]ConversationWithState, len(conversations))
	for i, item := range conversations {
		conv := item.Conversation
		cws := ConversationWithState{ConversationListItem: item, Working: conv.AgentWorking}
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
	return result
}

// registerConversationRoutes registers the /api/conversation/<id>/* routes.
func (s *Server) registerConversationRoutes(mux *http.ServeMux) {
	// GET /api/conversation/<id> - returns all messages (can be large, compress)
	mux.Handle("GET /api/conversation/{id}", compressionHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleGetConversation(w, r, r.PathValue("id"))
	})))
	// GET /api/conversation/<id>/subagent-usage - aggregated subagent cost
	mux.HandleFunc("GET /api/conversation/{id}/subagent-usage", func(w http.ResponseWriter, r *http.Request) {
		s.handleSubagentUsage(w, r, r.PathValue("id"))
	})
	// GET /api/conversation/<id>/stream - legacy SSE stream. Compression is
	// negotiated inside the handler (zstd/gzip per Accept-Encoding) with a
	// compressor flush after every event so messages stream promptly.
	mux.HandleFunc("GET /api/conversation/{id}/stream", func(w http.ResponseWriter, r *http.Request) {
		s.handleStreamConversation(w, r, r.PathValue("id"))
	})
	// POST endpoints - small responses, no compression needed
	mux.HandleFunc("POST /api/conversation/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
		s.handleChatConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/hooks", func(w http.ResponseWriter, r *http.Request) {
		s.handleRegisterConversationHook(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		s.handleCancelConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		s.handleRetryConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		s.handleResumeConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("GET /api/conversation/{id}/btw", func(w http.ResponseWriter, r *http.Request) {
		s.handleListBtwReaders(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/btw/{childID}/summarize", func(w http.ResponseWriter, r *http.Request) {
		s.handleSummarizeBtwReader(w, r, r.PathValue("id"), r.PathValue("childID"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/btw/{childID}/dismiss", func(w http.ResponseWriter, r *http.Request) {
		s.handleDismissBtwReader(w, r, r.PathValue("id"), r.PathValue("childID"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/continue", func(w http.ResponseWriter, r *http.Request) {
		s.handleContinueConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		s.handleArchiveConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/unarchive", func(w http.ResponseWriter, r *http.Request) {
		s.handleUnarchiveConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		s.handleDeleteConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/rename", func(w http.ResponseWriter, r *http.Request) {
		s.handleRenameConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		s.handleUpdateConversationTags(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("GET /api/conversation/{id}/subagents", func(w http.ResponseWriter, r *http.Request) {
		s.handleGetSubagents(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("GET /api/conversation/{id}/background-jobs", func(w http.ResponseWriter, r *http.Request) {
		s.handleListBackgroundJobs(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/background-jobs/{jobID}/kill", func(w http.ResponseWriter, r *http.Request) {
		s.handleKillBackgroundJob(w, r, r.PathValue("id"), r.PathValue("jobID"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/send-queued", func(w http.ResponseWriter, r *http.Request) {
		s.handleSendQueuedNow(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/cancel-queued", func(w http.ResponseWriter, r *http.Request) {
		s.handleCancelQueued(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/retry-queued", func(w http.ResponseWriter, r *http.Request) {
		s.handleRetryQueued(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("PUT /api/conversation/{id}/draft", func(w http.ResponseWriter, r *http.Request) {
		s.handleUpdateDraft(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/new-generation", func(w http.ResponseWriter, r *http.Request) {
		s.handleStartNewGeneration(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/fork", func(w http.ResponseWriter, r *http.Request) {
		s.handleForkConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/cwd", func(w http.ResponseWriter, r *http.Request) {
		s.handleSetConversationCwd(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /api/conversation/{id}/enable-compact-in-place", func(w http.ResponseWriter, r *http.Request) {
		s.handleEnableCompactInPlace(w, r, r.PathValue("id"))
	})
}

// handleGetConversation handles GET /conversation/<id>
func (s *Server) handleGetConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
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
		s.internalError(w, "Failed to get conversation messages", err, "conversationID", conversationID)
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
	Message              string                  `json:"message"`
	Model                string                  `json:"model,omitempty"`
	Cwd                  string                  `json:"cwd,omitempty"`
	ConversationOptions  *db.ConversationOptions `json:"conversation_options,omitempty"`
	Queue                bool                    `json:"queue,omitempty"`
	SenderConversationID string                  `json:"sender_conversation_id,omitempty"`
}

// handleChatConversation handles POST /conversation/<id>/chat
func (s *Server) handleChatConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
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
	if req.ConversationOptions != nil &&
		(req.ConversationOptions.Kind != "" || req.ConversationOptions.ParentPointer != nil || req.ConversationOptions.CommitTour != nil || req.ConversationOptions.SystemPrompt != "") {
		http.Error(w, "kind, parent_pointer, commit_tour, and system_prompt are internal conversation options", http.StatusBadRequest)
		return
	}

	// Load the conversation up front; we need its persisted model to
	// resolve an omitted `model` (see below) and the draft branches need
	// it too.
	existing, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to load conversation", "conversationID", conversationID, "error", err)
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}

	senderUserData, err := s.senderUserData(ctx, *existing, req.SenderConversationID)
	if err != nil {
		s.internalError(w, "Failed to resolve chat sender", err, "conversationID", conversationID)
		return
	}

	// Resolve the model. The conversation's own persisted model is
	// authoritative for an existing (non-draft) conversation: the web UI
	// hides the model picker once a conversation is under way, so the
	// `model` it keeps attaching to every send is a stale composer default.
	// Honoring it would silently revert a mid-conversation /model switch and
	// rebuild the loop on the wrong model. Precedence:
	//  1. the conversation's own persisted model, if set (non-draft),
	//  2. an explicit `model` in the request (new conversations reach this
	//     via handleNewConversation; drafts retarget via the promote branch
	//     below, which runs before the loop is built),
	//  3. the host's effective default (conversations that never recorded a
	//     model and whose request omits one).
	//
	// Clients that don't track a conversation's model — notably the iOS
	// push "Reply" handler, which fires from a background launch with no
	// loaded chat state — send an empty `model`; case 1 covers them too.
	modelID := req.Model
	if existing.Model != nil && *existing.Model != "" && (!existing.IsDraft || modelID == "") {
		// Non-draft: persisted model wins outright (stale req.Model ignored).
		// Draft: persisted model is only the fallback when the request omits
		// one; an explicit req.Model on a draft retargets it (handled below).
		modelID = *existing.Model
	}
	if modelID == "" {
		modelID = s.effectiveDefaultModel(s.getModelList())
	}

	llmService, err := s.llmManager.GetService(modelID)
	if err != nil {
		modelList := s.getModelList()
		// A removed persisted model must not trap an established conversation.
		// An explicit /model switch does not need the old service, so let it
		// replace the stale model before rejecting ordinary sends. Drafts retain
		// their existing promotion semantics below.
		if !existing.IsDraft && modelCommandSelectsReadyModel(req.Message, modelList) {
			userEmail := r.Header.Get("X-ExeDev-Email")
			recoveryCtx := contextWithUserEmail(ctx, userEmail)
			manager, managerErr := s.getOrCreateConversationManager(recoveryCtx, conversationID, userEmail)
			if errors.Is(managerErr, errConversationModelMismatch) {
				http.Error(w, managerErr.Error(), http.StatusBadRequest)
				return
			}
			if managerErr != nil {
				s.internalError(w, "Failed to get conversation manager", managerErr, "conversationID", conversationID)
				return
			}
			if s.handleModelCommand(recoveryCtx, w, conversationID, modelID, manager, req.Message) {
				return
			}
		}
		s.logger.Error("Unsupported model requested", "model", modelID, "error", err)
		http.Error(w, unsupportedModelMessage(modelID, modelList), http.StatusBadRequest)
		return
	}

	userEmail := r.Header.Get("X-ExeDev-Email")
	// Thread the authenticated exe.dev account down to the message recorder so
	// the user turn's row is attributed to its author. The active manager is a
	// long-lived singleton shared across requests (its userEmail is only set at
	// creation), so per-request context — not manager state — is the reliable
	// carrier here. Both the immediate-send (recordTurnStartMessage) and queued
	// (QueueMessage) paths read it off this ctx.
	ctx = contextWithUserEmail(ctx, userEmail)

	commitTourReasoning := db.ParseConversationOptions(existing.ConversationOptions).ThinkingLevel
	if commitTourReasoning == "" {
		commitTourReasoning = llm.ServiceDefaultReasoningLevel(llmService)
	}
	if s.handleCommitTourCommand(ctx, w, *existing, modelID, commitTourReasoning, req.Message) {
		return
	}

	// Built-in /transcription is durable queued user input backed by a hidden
	// child. Reject bad commands before any side effect (draft promotion or
	// manager creation); it is queued below once the parent exists.
	transcriptionPath, transcriptionContext, isTranscription, err := validateTranscriptionCommand(req.Message)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if isTranscription && existing.Archived {
		http.Error(w, "conversation is archived", http.StatusConflict)
		return
	}
	// A queued item that can only fail would block every later message.
	if isTranscription {
		if err := s.transcriber.Available(); errors.Is(err, errNoTranscriptionRoute) {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		} else if err != nil {
			s.logger.Error("Failed to resolve transcription models", "conversationID", conversationID, "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}

	// A built-in /btw is a detached child start, not a parent turn. Give an
	// installed slash/btw hook first refusal, then create the child before
	// acquiring or consulting the parent manager so Queue and parent work do
	// not delay it.
	var btwSlashResult *SlashCommandHookResult
	if question, ok := parseBuiltinBtw(req.Message); ok {
		result := RunSlashCommandHook(SlashCommandHookInput{
			RawMessage:     req.Message,
			ConversationID: conversationID,
			Cwd:            derefString(existing.Cwd),
			Model:          modelID,
			UserEmail:      userEmail,
		})
		if result.Err != nil {
			s.logger.Error("slash-command hook failed", "conversationID", conversationID, "error", result.Err)
			http.Error(w, fmt.Sprintf("slash command failed: %v", result.Err), http.StatusBadRequest)
			return
		}
		if result.Handled {
			if result.Message == "" {
				writeBtwReaderJSON(w, http.StatusAccepted, map[string]string{"status": "handled"})
				return
			}
			btwSlashResult = &result
		}
		if btwSlashResult == nil {
			if question == "" {
				http.Error(w, "/btw requires a side question", http.StatusBadRequest)
				return
			}
			reasoningLevel := db.ParseConversationOptions(existing.ConversationOptions).ThinkingLevel
			if reasoningLevel == "" {
				reasoningLevel = llm.ServiceDefaultReasoningLevel(llmService)
			}
			question, err = s.runChatMessageHook(r, conversationID, modelID, reasoningLevel, false, question)
			if err != nil {
				s.logger.Error("chat-message hook failed", "conversationID", conversationID, "error", err)
				http.Error(w, "chat-message hook failed", http.StatusInternalServerError)
				return
			}
			descriptor, err := s.createBtwReader(ctx, conversationID, question)
			if errors.Is(err, errNestedBtwReader) || errors.Is(err, errBtwParentDraft) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err != nil {
				s.logger.Error("Failed to create BTW reader", "conversationID", conversationID, "error", err)
				http.Error(w, "Failed to create BTW reader", http.StatusInternalServerError)
				return
			}
			writeBtwReaderJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "btw": descriptor})
			return
		}
	}

	// Drafts can have their model/cwd retargeted right up to send. Validate
	// send-time overrides the same way the new-conversation path does, then
	// apply them and promote (clearing is_draft and the draft body) in ONE
	// transaction: a concurrent PUT /draft can no longer slip between an
	// override and the promote, so the promoted row's model is exactly what
	// the loop below pins to. For non-drafts this branch is skipped.
	promotedDraft := existing.IsDraft
	if existing.IsDraft {
		// Apply send-time conversation_options. The web UI autosaves a draft
		// (via POST /draft) as soon as the composer has text, and that draft is
		// born WITHOUT options. The user's actual selection (thinking level,
		// tool overrides) only travels with the promoting
		// chat request, so without this the selection is dropped and reasoning
		// is silently disabled for adaptive models.
		conversationOptions := req.ConversationOptions
		if conversationOptions != nil {
			if msg := validateConversationOptions(*conversationOptions); msg != "" {
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
			if msg := validateModelReasoningLevel(findModelInfo(modelID, s.getModelList()), conversationOptions.ThinkingLevel); msg != "" {
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
		}
		var cwdOverride, modelOverride *string
		if req.Cwd != "" {
			cwdOverride = &req.Cwd
		}
		if req.Model != "" {
			// req.Model was already validated against the LLM manager above.
			modelOverride = &req.Model
		}
		promoted, err := s.db.PromoteDraft(ctx, conversationID, cwdOverride, modelOverride, conversationOptions)
		switch {
		case errors.Is(err, db.ErrConversationNotDraft):
			// A concurrent send won the promote race; its overrides stand and
			// this request continues as an ordinary second message. If the
			// winner already built the loop and our resolved model disagrees,
			// AcceptUserMessage rejects it below with a model mismatch. (If
			// the loop isn't built yet, whichever send reaches ensureLoop
			// first pins it — two same-instant sends on different models is a
			// race the user has already lost either way.)
		case err != nil:
			s.internalError(w, "Failed to promote draft", err, "conversationID", conversationID)
			return
		case req.Model == "" && promoted.Model != nil && *promoted.Model != "" && *promoted.Model != modelID:
			// An omitted-model send resolves to the draft's persisted model,
			// but a PUT /draft may have retargeted it after our read above.
			// The promoted row is authoritative — re-resolve so the loop pins
			// to the model the user actually picked.
			modelID = *promoted.Model
			llmService, err = s.llmManager.GetService(modelID)
			if err != nil {
				s.logger.Error("Unsupported model on promoted draft", "model", modelID, "error", err)
				http.Error(w, unsupportedModelMessage(modelID, s.getModelList()), http.StatusBadRequest)
				return
			}
		}
	}

	manager, err := s.getOrCreateConversationManager(ctx, conversationID, userEmail)
	if errors.Is(err, errConversationModelMismatch) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to get conversation manager", err, "conversationID", conversationID)
		return
	}

	// Announce the rows hydrate just created for a promoted draft — in practice
	// the system prompt, sequence 1.
	//
	// createSystemPrompt deliberately neither broadcasts nor bumps updated_at:
	// it is lazy internal metadata, and bumping would reorder the conversation
	// list every time a stream connects to a brand-new conversation. That is
	// right for ordinary hydration, but promotion makes it user-visible: the
	// is_draft flip is announced BEFORE hydrate runs, so a client refetching on
	// that signal sees an empty conversation, and then the only row it ever
	// hears about is the user's message at sequence 2. Its history would start
	// at 2 forever (the client cache correctly refuses to call that complete,
	// but nothing re-renders the missing prompt until the next focus).
	//
	// Publishing here rather than from createSystemPrompt is deliberate:
	// getOrCreateConversationManager registers the manager only AFTER Hydrate
	// returns, so a notify fired during hydrate finds no manager and is
	// silently dropped. Mirrors startNewGeneration's post-hydrate broadcast.
	if promotedDraft {
		if msgs, lerr := s.db.ListMessages(ctx, conversationID); lerr == nil {
			for i := range msgs {
				if msgs[i].Type == string(db.MessageTypeSystem) {
					s.notifySubscribers(ctx, conversationID, msgs[i])
				}
			}
		} else {
			s.logger.Warn("Failed to list messages to announce promoted draft's system prompt", "conversationID", conversationID, "error", lerr)
		}
	}

	// The command itself is control input: it is never persisted as an LLM
	// message and is replaced by the finished transcript before queue drain.
	if isTranscription {
		if senderUserData != nil {
			senderUserData.Text = req.Message
			ctx = contextWithTurnUserData(ctx, *senderUserData)
		}
		s.queueTranscription(ctx, w, manager, transcriptionPath, transcriptionContext, modelID)
		return
	}

	// Built-in /model command: switch the conversation to a different model
	// mid-conversation. Handled entirely here — it never reaches the LLM.
	if s.handleModelCommand(ctx, w, conversationID, modelID, manager, req.Message) {
		return
	}
	if s.handleCompactDebugCommand(ctx, w, manager, req.Message) {
		return
	}

	// Slash-command hook: if the message starts with /<name> and a matching
	// executable exists at ~/.config/shelley/hooks/slash/<name>, run it and
	// use its stdout as the replacement message text. Empty stdout leaves
	// the original message unchanged.
	slashResult := SlashCommandHookResult{}
	if btwSlashResult != nil {
		slashResult = *btwSlashResult
	} else {
		slashResult = RunSlashCommandHook(SlashCommandHookInput{
			RawMessage:     req.Message,
			ConversationID: conversationID,
			Cwd:            manager.cwd,
			Model:          modelID,
			UserEmail:      userEmail,
		})
	}
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
	hasQueued, err := manager.HasQueuedMessages(ctx)
	if err != nil {
		s.internalError(w, "Failed to read queued messages", err, "conversationID", conversationID)
		return
	}
	willQueue := req.Queue || manager.IsDistilling() || hasQueued

	// Run chat-message hook; the hook may rewrite the message text. Hook
	// failures abort the request — the user's message is not delivered.
	reasoningLevel := manager.GetThinkingLevel()
	if reasoningLevel == "" {
		reasoningLevel = llm.ServiceDefaultReasoningLevel(llmService)
	}
	newMsg, err := s.runChatMessageHook(r, conversationID, modelID, reasoningLevel, willQueue, req.Message)
	if err != nil {
		s.logger.Error("chat-message hook failed", "conversationID", conversationID, "error", err)
		http.Error(w, "chat-message hook failed", http.StatusInternalServerError)
		return
	}
	req.Message = newMsg
	if senderUserData != nil {
		senderUserData.Text = req.Message
		ctx = contextWithTurnUserData(ctx, *senderUserData)
	}

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
			s.internalError(w, "Failed to queue user message", err, "conversationID", conversationID)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"status": "queued"})
		return
	}

	firstMessage, err := manager.AcceptUserMessage(ctx, llmService, modelID, userMessage)
	if errors.Is(err, errQueuedMessagesPending) {
		if err := manager.QueueMessage(ctx, s, modelID, userMessage); err != nil {
			s.internalError(w, "Failed to queue user message after concurrent queue reservation", err, "conversationID", conversationID)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"status": "queued"})
		return
	}
	if errors.Is(err, errConversationModelMismatch) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to accept user message", err, "conversationID", conversationID)
		return
	}

	if firstMessage {
		s.generateSlugAsync(conversationID, req.Message, modelID)
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

func (s *Server) runChatMessageHook(r *http.Request, conversationID, modelID, reasoningLevel string, queued bool, message string) (string, error) {
	return RunChatMessageHookIn(s.hooksDir, ChatMessageHookInput{
		Message: message,
		Readonly: ChatMessageReadonly{
			ConversationID: conversationID,
			Model:          modelID,
			ReasoningLevel: reasoningLevel,
			Queued:         queued,
			Headers:        HookHeaders(r.Header),
		},
	})
}

// handleNewConversation handles POST /api/conversations/new - creates conversation implicitly on first message
func (s *Server) handleNewConversation(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, unsupportedModelMessage(modelID, s.getModelList()), http.StatusBadRequest)
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
		if msg := validateConversationOptions(convOpts); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if msg := validateModelReasoningLevel(findModelInfo(modelID, s.getModelList()), convOpts.ThinkingLevel); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
	}

	conversation, err := s.db.CreateConversation(ctx, nil, true, cwdPtr, &modelID, convOpts)
	if err != nil {
		s.internalError(w, "Failed to create conversation", err)
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
		} else if msg := validateModelReasoningLevel(findModelInfo(hookResult.Model, s.getModelList()), convOpts.ThinkingLevel); msg != "" {
			s.logger.Error("Hook returned model incompatible with reasoning level, keeping original", "hookModel", hookResult.Model, "error", msg)
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

	userEmail := r.Header.Get("X-ExeDev-Email")
	// Attribute the user turn to its author; see handleChatConversation for why
	// the recorder reads the email off ctx rather than the shared manager.
	ctx = contextWithUserEmail(ctx, userEmail)

	// Get or create conversation manager
	manager, err := s.getOrCreateConversationManager(ctx, conversationID, userEmail)
	if errors.Is(err, errConversationModelMismatch) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to get conversation manager", err, "conversationID", conversationID)
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
		s.internalError(w, "Failed to accept user message", err, "conversationID", conversationID)
		return
	}

	if firstMessage && !hookSlugApplied {
		s.generateSlugAsync(conversationID, req.Message, modelID)
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
	ctx := r.Context()
	// Take the fence before looking up the manager: a report arriving
	// first is cleared by Stop; one arriving later sees the stopped parent.
	s.mu.Lock()
	s.stoppedParents[conversationID] = true
	s.stoppingParents[conversationID] = true
	s.mu.Unlock()
	defer func() {
		// An in-flight subagent send can run while Stop tears down its loop;
		// keep the fence closed until after that teardown completes.
		s.mu.Lock()
		s.stoppedParents[conversationID] = true
		delete(s.stoppingParents, conversationID)
		s.mu.Unlock()
	}()

	s.mu.Lock()
	manager, exists := s.activeConversations[conversationID]
	s.mu.Unlock()
	if !exists {
		conversation, err := s.db.GetConversationByID(ctx, conversationID)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Conversation not found", http.StatusNotFound)
			return
		}
		if err != nil {
			s.logger.Error("Failed to load conversation for cancellation", "conversationID", conversationID, "error", err)
			http.Error(w, "Failed to cancel conversation", http.StatusInternalServerError)
			return
		}
		if conversation.AgentWorking {
			// Synchronize with startup's get-or-create so Stop either clears the
			// pending token before it is claimed or cancels the loop after Retry.
			manager, err = s.getOrCreateConversationManager(ctx, conversationID, r.Header.Get("X-ExeDev-Email"))
			if err == nil {
				exists = true
			} else {
				s.logger.Warn("Failed to load working conversation for cancellation", "conversationID", conversationID, "error", err)
				if clearErr := s.db.ClearConversationRuntimeState(ctx, conversationID); clearErr != nil {
					s.logger.Error("Failed to clear conversation runtime state", "conversationID", conversationID, "error", clearErr)
					http.Error(w, "Failed to cancel conversation", http.StatusInternalServerError)
					return
				}
			}
		}
	}

	// Cancel detached transcription work and clear the durable queue, then
	// cancel the parent loop and the remaining child tree. The subagent tree is
	// cancelled even when the parent has no active loop: the parent may have
	// gone idle (or been evicted) while its subagents keep working, and the
	// user's cancel means "stop all of this work".
	err := s.cancelQueuedTranscriptions(ctx, conversationID, "", func() error {
		if exists {
			return manager.CancelConversation(ctx)
		}
		_, err := s.db.ClearQueuedMessages(ctx, conversationID)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Error("Failed to cancel conversation", "conversationID", conversationID, "error", err)
		http.Error(w, "Failed to cancel conversation", http.StatusInternalServerError)
		return
	}
	s.cancelSubagentTree(ctx, conversationID)

	if !exists {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "no_active_conversation"})
		return
	}

	s.logger.Info("Conversation cancelled", "conversationID", conversationID)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "cancelled"})
}

// handleResumeConversation handles POST /api/conversation/<id>/resume. It
// re-fires a turn carrying the durable conversation bit written during ordinary
// startup, without adding a synthetic user message. Upgrade restarts resume
// automatically.
func (s *Server) handleResumeConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	ctx := r.Context()
	manager, err := s.getOrCreateConversationManager(ctx, conversationID, r.Header.Get("X-ExeDev-Email"))
	if err != nil {
		s.logger.Warn("Resume: failed to load conversation", "conversationID", conversationID, "error", err)
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	modelList := s.getModelList()
	defaultModelID := s.effectiveDefaultModel(modelList)
	serviceForModel := func(modelID string) (llm.Service, error) {
		service, err := s.llmManager.GetService(modelID)
		if err != nil {
			return nil, errors.New(unsupportedModelMessage(modelID, modelList))
		}
		return service, nil
	}
	if err := manager.ContinueInterruptedTurn(ctx, defaultModelID, serviceForModel); err != nil {
		if errors.Is(err, errInterruptedTurnNotApplicable) {
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"status": "not_applicable"})
			return
		}
		s.logger.Warn("Resume rejected", "conversationID", conversationID, "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.logger.Info("Interrupted conversation resumed", "conversationID", conversationID)
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "resuming"})
}

// handleRetryConversation handles POST /api/conversation/<id>/retry.
// It re-runs the LLM request that previously failed, using the conversation's
// current state. The error message is left untouched (messages are an
// append-only, immutable log) and is excluded from LLM history by
// partitionMessages, so no synthetic retry-user-message is sent to the
// model. Requires a latest message of type "error" that is classified
// retryable.
func (s *Server) handleRetryConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	ctx := r.Context()

	// Validate that there's actually a retryable error to act on BEFORE
	// spinning up a fresh loop with tools and a 12-hour context. This keeps
	// the cold-storage path lightweight when the request is bogus.
	latest, err := s.db.GetLatestActionableMessage(ctx, conversationID)
	if err != nil {
		s.logger.Warn("Retry: failed to load latest message", "conversationID", conversationID, "error", err)
		http.Error(w, "conversation not found or empty", http.StatusNotFound)
		return
	}
	if latest.Type != string(db.MessageTypeError) {
		// No error at the bottom of the conversation: nothing to retry.
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"status": "not_applicable"})
		return
	}
	// Inspect user_data to enforce retryable=true before spinning up a loop
	// with tools and a 12-hour context. Never mutate the row.
	if latest.UserData != nil && *latest.UserData != "" {
		var ud map[string]any
		if err := json.Unmarshal([]byte(*latest.UserData), &ud); err == nil {
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
			s.internalError(w, "Failed to get conversation manager for retry", err, "conversationID", conversationID)
			return
		}
		modelID := manager.GetModel()
		if modelID == "" {
			modelID = s.effectiveDefaultModel(s.getModelList())
		}
		llmService, err := s.llmManager.GetService(modelID)
		if err != nil {
			http.Error(w, unsupportedModelMessage(modelID, s.getModelList()), http.StatusBadRequest)
			return
		}
		if err := manager.Hydrate(ctx); err != nil {
			s.internalError(w, "Failed to hydrate for retry", err, "conversationID", conversationID)
			return
		}
		if err := manager.ensureLoop(llmService, modelID); err != nil {
			s.internalError(w, "Failed to ensure loop for retry", err, "conversationID", conversationID)
			return
		}
	}

	if err := manager.RetryLastLLMRequest(ctx); err != nil {
		if errors.Is(err, errRetryNotApplicable) {
			// The bottom message is no longer a retryable error (e.g. a
			// concurrent retry already started a new turn); treat as success.
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"status": "not_applicable"})
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

// continueRequest is the optional JSON body for POST
// /api/conversation/<id>/continue. An empty/omitted model falls back to the
// default catalog model for older clients.
type continueRequest struct {
	Model string `json:"model"`
}

// handleContinueConversation handles POST /api/conversation/<id>/continue.
// It powers the choose-another-model affordance offered on a refusal error: it
// switches the conversation to the selected model and re-fires the request the
// previous model declined. Requires a
// latest message that is a refusal error (error_type=refusal). The refusal row
// is left untouched (append-only log) and excluded from context, so the new
// model sees the same request.
func (s *Server) handleContinueConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	ctx := r.Context()

	// Parse the optional body (empty body is fine — defaults to Opus).
	var req continueRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	// Validate that the bottom message is a refusal error BEFORE spinning up a
	// loop with tools and a 12-hour context.
	latest, err := s.db.GetLatestActionableMessage(ctx, conversationID)
	if err != nil {
		s.logger.Warn("Continue: failed to load latest message", "conversationID", conversationID, "error", err)
		http.Error(w, "conversation not found or empty", http.StatusNotFound)
		return
	}
	if latest.Type != string(db.MessageTypeError) {
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"status": "not_applicable"})
		return
	}
	if latest.UserData != nil && *latest.UserData != "" {
		var ud map[string]any
		if err := json.Unmarshal([]byte(*latest.UserData), &ud); err == nil {
			if errType, _ := ud["error_type"].(string); errType != string(llm.ErrorTypeRefusal) {
				http.Error(w, "latest error is not a refusal", http.StatusConflict)
				return
			}
		}
	}

	userEmail := r.Header.Get("X-ExeDev-Email")
	manager, err := s.getOrCreateConversationManager(ctx, conversationID, userEmail)
	if err != nil {
		s.internalError(w, "Failed to get conversation manager for continue", err, "conversationID", conversationID)
		return
	}

	// Resolve the model to continue on. Default to the catalog default (Opus).
	modelList := s.getModelList()
	currentModel := manager.GetModel()
	if currentModel == "" {
		currentModel = s.effectiveDefaultModel(modelList)
	}
	newModel := req.Model
	if newModel == "" {
		newModel = models.Default().ID
	}
	if _, err := s.llmManager.GetService(newModel); err != nil || !isReadyModel(newModel, modelList) {
		http.Error(w, fmt.Sprintf("Unknown or unavailable model: %s", newModel), http.StatusBadRequest)
		return
	}

	llmService, err := s.llmManager.GetService(newModel)
	if err != nil {
		http.Error(w, unsupportedModelMessage(newModel, s.getModelList()), http.StatusBadRequest)
		return
	}

	// Build the model-switch delta (empty when already on the target model, in
	// which case ContinueAfterRefusal just re-fires without a modelchange marker).
	currentReasoning := manager.GetThinkingLevel()
	ch := ModelSettingsChange{OldModel: currentModel, OldReasoning: currentReasoning}
	if newModel != currentModel {
		ch.NewModel = newModel
		ch.OldModelDisplay = modelDisplayName(currentModel, modelList)
		ch.NewModelDisplay = modelDisplayName(newModel, modelList)
	}

	if err := manager.ContinueAfterRefusal(ctx, ch, llmService, newModel); err != nil {
		if errors.Is(err, errNotRefusal) {
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"status": "not_applicable"})
			return
		}
		s.logger.Warn("Continue rejected", "conversationID", conversationID, "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.logger.Info("Continue triggered", "conversationID", conversationID, "model", newModel)
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "continuing", "model": newModel})
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

const streamUpdatesQueueCapacity = 200

type streamUpdatesQueue struct {
	ch      chan StreamResponse
	logFull func()
	fullLog sync.Once
}

func newStreamUpdatesQueue(logFull func()) *streamUpdatesQueue {
	return &streamUpdatesQueue{
		ch:      make(chan StreamResponse, streamUpdatesQueueCapacity),
		logFull: logFull,
	}
}

func (q *streamUpdatesQueue) enqueue(ctx context.Context, streamData StreamResponse) bool {
	select {
	case q.ch <- streamData:
		return true
	default:
		if ctx.Err() != nil {
			return false
		}
		q.fullLog.Do(q.logFull)
	}
	select {
	case q.ch <- streamData:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Server) newStreamUpdatesQueue(ctx context.Context, conversationID string) *streamUpdatesQueue {
	return newStreamUpdatesQueue(func() {
		s.logger.WarnContext(
			ctx, "SSE updates queue saturated; subscriber may be disconnected",
			"capacity", streamUpdatesQueueCapacity,
			"conversationID", conversationID,
		)
	})
}

func (s *Server) runStream(w http.ResponseWriter, r *http.Request, conversationID string, includeConversationListPatches bool) {
	ctx, cancelStream := context.WithCancel(r.Context())
	responseController := http.NewResponseController(w)
	var streamInterruptResult <-chan bool
	defer func() {
		cancelStream()
		if streamInterruptResult == nil || !<-streamInterruptResult {
			return
		}
		// SetWriteDeadline applies to the connection, not just this response.
		// Clear the forced deadline before net/http can reuse the connection.
		if err := responseController.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
			s.logger.Debug("failed to clear unified stream write deadline", "error", err)
		}
	}()

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
			s.internalError(w, "failed to initialize conversation list patches", err)
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
	encoding, acceptable := negotiateContentEncoding(r)
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
				s.internalError(w, "zstd writer init failed", err)
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
		if len(streamData.streamDeltas) > 0 {
			frames, err := marshalDeltaBatchFrames(streamData.ConversationID, streamData.streamDeltas)
			if err != nil {
				s.logger.Debug("failed to marshal stream response", "error", err)
				return false
			}
			if _, err := compressedSink.Write(frames); err != nil {
				s.logger.Debug("conversation stream write failed", "error", err)
				return false
			}
		} else {
			data, err := json.Marshal(streamData)
			if err != nil {
				s.logger.Debug("failed to marshal stream response", "error", err)
				return false
			}
			if _, err := fmt.Fprintf(compressedSink, "data: %s\n\n", data); err != nil {
				s.logger.Debug("conversation stream write failed", "error", err)
				return false
			}
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
	errAfterStreamStart := func(w http.ResponseWriter, msg string, err error, logArgs ...any) {
		if streamStarted {
			s.logger.Error(msg, append(logArgs, "error", err)...)
			s.logger.Debug("abandoning compressed SSE stream after error", "msg", msg)
			return
		}
		s.internalError(w, msg, err, logArgs...)
	}

	for _, event := range listInitial {
		patch := event
		if !writeStreamData(StreamResponse{ConversationListPatch: &patch}) {
			return
		}
	}

	updates := s.newStreamUpdatesQueue(ctx, conversationID)
	if listNext != nil {
		go func() {
			for {
				event, ok := listNext()
				if !ok {
					return
				}
				patch := event
				if !updates.enqueue(ctx, StreamResponse{ConversationListPatch: &patch}) {
					return
				}
			}
		}()
	}

	// On /api/stream2 we subscribe to the server-wide streamPub once and receive
	// events for ALL active conversations on the same connection. The optional
	// ?conversation= parameter governs only backfill of that conversation's
	// initial history (handled below).
	//
	// SubPub disconnects a subscriber whose bounded queue fills. Surface that
	// as an ended HTTP response: if we silently leave this handler alive, its
	// local heartbeats keep EventSource OPEN even though conversation updates
	// have stopped forever. Closing lets EventSource reconnect and the UI
	// backfill anything missed while the client/proxy was backpressured.
	var subscriptionDone <-chan struct{}
	watchSubscription := func(status *subpub.SubscriptionStatus, queue string) {
		subscriptionDone = status.Done()
		interruptResult := make(chan bool, 1)
		streamInterruptResult = interruptResult
		go func() {
			<-status.Done()
			if !status.FellBehind() {
				interruptResult <- false
				return
			}
			s.logger.WarnContext(
				ctx, "SSE subscriber queue full; reconnecting",
				"queue", queue,
				"capacity", subpub.SubscriberQueueCapacity,
				"conversationID", conversationID,
			)
			// The handler may currently be blocked in Write because the client or
			// proxy stopped reading. Expire that write immediately; otherwise we
			// cannot reach the select below that notices subscriptionDone.
			err := responseController.SetWriteDeadline(time.Now())
			if err != nil && !errors.Is(err, http.ErrNotSupported) {
				s.logger.Debug("failed to interrupt dropped SSE stream", "error", err)
			}
			interruptResult <- err == nil
		}()
	}

	if includeConversationListPatches && s.streamPub != nil {
		next, status, diskSpace := s.subscribeStream(ctx)
		watchSubscription(status, "global")
		go func() {
			for {
				streamData, cont := next()
				if !cont {
					return
				}
				if !updates.enqueue(ctx, streamData) {
					return
				}
			}
		}()
		if diskSpace != nil && !writeStreamData(StreamResponse{DiskSpaceStatus: diskSpace}) {
			return
		}
	}

	// On the unified /api/stream2 endpoint, send a bare heartbeat whenever
	// there is no list replay to emit. Subscribe to streamPub first: a blocked
	// first write must still accumulate live events and trigger the
	// fell-behind reconnect path rather than leaving an apparently healthy
	// stream that has silently missed updates. The heartbeat commits the SSE
	// headers before any blocking per-conversation work and also keeps a
	// caught-up list-only reconnect from staying header-silent until the 30s
	// periodic heartbeat. The legacy endpoint keeps its first-frame contract.
	if includeConversationListPatches && len(listInitial) == 0 {
		if !writeStreamData(StreamResponse{Heartbeat: true}) {
			return
		}
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
			case <-subscriptionDone:
				return
			case <-ticker.C:
				if !writeStreamData(StreamResponse{Heartbeat: true}) {
					return
				}
			case streamData := <-updates.ch:
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
			errAfterStreamStart(w, "Failed to get conversation data", err, "conversationID", conversationID)
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
			errAfterStreamStart(w, "Failed to get conversation data", err, "conversationID", conversationID)
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
			errAfterStreamStart(w, "Failed to get conversation data", err, "conversationID", conversationID)
			return
		}
		if len(messages) > 0 {
			lastSeqID = messages[len(messages)-1].SequenceID
		}
	}

	manager, err := s.getOrCreateConversationManager(ctx, conversationID, "")
	if err != nil {
		errAfterStreamStart(w, "Failed to get conversation manager", err, "conversationID", conversationID)
		return
	}

	// On /api/stream2, live events arrive via the server-wide streamPub
	// subscription set up above. The per-conversation subpub is used only by
	// the legacy /api/conversation/<id>/stream endpoint.
	//
	// Subscribe BEFORE sending initial data so we don't miss broadcasts that
	// happen between the DB query and the start of the event loop. The subpub
	// channel is buffered, so events arriving while we write the initial
	// response are queued rather than lost.
	var next func() (StreamResponse, bool)
	if !includeConversationListPatches {
		var status *subpub.SubscriptionStatus
		next, status = manager.subpub.SubscribeWithStatus(ctx, lastSeqID)
		go func() {
			<-status.Done()
			if status.FellBehind() {
				s.logger.WarnContext(
					ctx, "SSE subscriber queue full; legacy stream stalled",
					"queue", "conversation",
					"capacity", subpub.SubscriberQueueCapacity,
					"conversationID", conversationID,
				)
			}
		}()
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
				if !updates.enqueue(ctx, streamData) {
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
		case <-subscriptionDone:
			return
		case <-ticker.C:
			// Local heartbeat keeps the connection alive even when no active
			// manager is broadcasting one (e.g., on /api/stream2 with no
			// activity across any conversation).
			if !writeStreamData(StreamResponse{Heartbeat: true}) {
				return
			}
		case streamData := <-updates.ch:
			// Always forward updates, even if only the conversation changed (e.g., slug added).
			if !writeStreamData(streamData) {
				return
			}
		}
	}
}

// handleVersion returns build information plus the capabilities list as
// JSON. The capabilities slot lets clients negotiate optional, additive
// features without reshaping the response. See version.Capabilities for
// the current set.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
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

// ModelInfo represents a model in the API response
type ModelInfo struct {
	ID           string `json:"id"`
	DisplayName  string `json:"display_name,omitempty"`
	Mode         string `json:"mode,omitempty"`
	Source       string `json:"source,omitempty"`   // Human-readable source (e.g., "exe.dev gateway", "$ANTHROPIC_API_KEY")
	BaseURL      string `json:"base_url,omitempty"` // Upstream origin (e.g., "https://llm.int.exe.xyz")
	APIType      string `json:"api_type,omitempty"` // Wire protocol (e.g., "anthropic-messages")
	APIModelName string `json:"api_model_name,omitempty"`
	Ready        bool   `json:"ready"`
	// MaxContextTokens is the models.dev context window (clamped to the
	// pricing tier, see modelsdev.LookupContextLimit); 0 when unknown.
	MaxContextTokens int  `json:"max_context_tokens,omitempty"`
	IsDefault        bool `json:"is_default,omitempty"`
	// Tier is 1 for prominent models and 2 for models overshadowed by a
	// better available sibling (see models.AssignTiers). The UI keeps tier-2
	// models behind a "more models" affordance. Older iOS/Android clients that
	// don't know about this field simply ignore it.
	Tier              int      `json:"tier,omitempty"`
	SupportsImages    bool     `json:"supports_images"`
	SupportsReasoning bool     `json:"supports_reasoning"`
	ReasoningLevels   []string `json:"reasoning_levels,omitempty"`
	// DefaultReasoningLevel is the reasoning level a conversation gets when it
	// carries no explicit thinking_level override. Lets the UI label
	// conversations honestly (e.g. "Reasoning medium") instead of leaving the
	// badge blank. Empty means the provider picks its own default and Shelley
	// can't name it up front.
	DefaultReasoningLevel string `json:"default_reasoning_level,omitempty"`
}

// handleModelCommand intercepts the built-in "/model" slash command. It
// returns true when the message was a /model command (in which case the HTTP
// response has already been written and the caller must stop processing), and
// false when the message should continue down the normal chat path.
//
// Usage:
//
//	/model                    — replies with the current model/reasoning and
//	                            the available options.
//	/model <a> [b]            — a and b are each either a model or a reasoning
//	                            level, in any order (at most one of each).
//
// Arguments are matched leniently. A reasoning level accepts its full name
// (off, minimal, low, medium, high, xhigh) or any unambiguous prefix ("med",
// "hi"). A model accepts an exact id, a case/dot-insensitive spelling, a unique
// id prefix, or a unique partial ("opus-4.8", "sonnet-5"). A partial that
// matches several models is reported with the candidates so the user can pick.
// If a single token resolves as both a model and a level (e.g. a model
// literally named "high"), the command is rejected as ambiguous.
//
// The change drops the in-memory loop (pinned to the old model/reasoning) and
// records a user-visible modelchange marker so the log shows where it happened.
func (s *Server) handleModelCommand(ctx context.Context, w http.ResponseWriter, conversationID, currentModel string, manager *ConversationManager, message string) bool {
	fields := strings.Fields(strings.TrimSpace(message))
	if len(fields) == 0 || fields[0] != "/model" {
		return false
	}

	modelList := s.getModelList()
	currentReasoning := manager.GetThinkingLevel()
	args := fields[1:]

	// Bare "/model": report the current model/reasoning and the options.
	if len(args) == 0 {
		s.recordModelCommandReply(ctx, conversationID, manager, modelCommandStatus(currentModel, currentReasoning, modelList))
		writeModelCommandAccepted(w)
		return true
	}

	reply := func(text string) bool {
		s.recordModelCommandReply(ctx, conversationID, manager, text)
		writeModelCommandAccepted(w)
		return true
	}

	// Classify each argument by value, leniently.
	var newModel, newReasoning string
	var reasoningSet, modelSet bool
	for _, a := range args {
		level, isLevel := resolveReasoningArg(a)
		modelID, modelCandidates, modelStrong := resolveModelArg(a, modelList)
		isModel := modelID != ""

		// A token that resolves as both a model and a reasoning level is
		// ambiguous — but only when the model match is strong (an exact id or a
		// prefix). A weak substring match (e.g. "o" for off happening to appear
		// inside some model id) must not override an explicit level, so drop it.
		switch {
		case isLevel && isModel && modelStrong:
			return reply(fmt.Sprintf("Ambiguous argument %q: it is both a model and a reasoning level. Rename the model to disambiguate.", a))
		case isLevel && isModel:
			isModel = false
			modelID = ""
		}

		switch {
		case isLevel:
			if reasoningSet {
				return reply(fmt.Sprintf("Reasoning level specified more than once (%q).", a))
			}
			reasoningSet = true
			newReasoning = level
		case isModel:
			if modelSet {
				return reply(fmt.Sprintf("Model specified more than once (%q).", a))
			}
			modelSet = true
			newModel = modelID
		case len(modelCandidates) > 0:
			// A partial that matched several models: help the user narrow it.
			return reply(fmt.Sprintf("%q matches several models: %s. Be more specific.", a, strings.Join(modelCandidates, ", ")))
		default:
			return reply(fmt.Sprintf("Unknown or unavailable option %q.\n\n%s", a, modelCommandStatus(currentModel, currentReasoning, modelList)))
		}
	}

	// Validate the chosen model is present, ready, and constructible.
	if modelSet {
		if _, err := s.llmManager.GetService(newModel); err != nil || !isReadyModel(newModel, modelList) {
			return reply(fmt.Sprintf("Unknown or unavailable model %q.\n\n%s", newModel, modelCommandStatus(currentModel, currentReasoning, modelList)))
		}
	}

	// Validate/reset reasoning against the model that will be active after this
	// command. A model switch with no explicit level falls back to that model's
	// default when the current level is unavailable.
	targetModel := currentModel
	if modelSet {
		targetModel = newModel
	}
	targetInfo := findModelInfo(targetModel, modelList)
	if reasoningSet {
		if msg := validateModelReasoningLevel(targetInfo, newReasoning); msg != "" {
			return reply(msg)
		}
	} else if modelSet {
		if rounded, changed := roundModelReasoningLevel(targetInfo, currentReasoning); changed {
			reasoningSet = true
			newReasoning = rounded
		}
	}

	// Reduce to the actual deltas: ignore no-op changes.
	ch := ModelSettingsChange{OldModel: currentModel, OldReasoning: currentReasoning}
	if modelSet && newModel != currentModel {
		ch.NewModel = newModel
		ch.OldModelDisplay = modelDisplayName(currentModel, modelList)
		ch.NewModelDisplay = modelDisplayName(newModel, modelList)
	}
	if reasoningSet && newReasoning != currentReasoning {
		ch.ReasoningSet = true
		ch.NewReasoning = newReasoning
	}

	if ch.NewModel == "" && !ch.ReasoningSet {
		return reply(fmt.Sprintf("Already using model %s with reasoning %s.", currentModel, reasoningDisplayName(currentReasoning)))
	}

	if err := manager.ApplyModelSettings(ctx, ch); err != nil {
		s.internalError(w, "Failed to apply model settings", err, "conversationID", conversationID)
		return true
	}
	// ApplyModelSettings already broadcast the updated conversation (carrying
	// the new model) alongside the modelchange marker, so the composer follows
	// without an extra notify here.
	writeModelCommandAccepted(w)
	return true
}

// reasoningLevelNames are the user-facing reasoning levels accepted by /model.
// They match the levels offered by the ThinkingLevelPicker in the UI. The word
// "default" is deliberately not a level: it selects the default MODEL instead.
var reasoningLevelNames = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// resolveReasoningArg matches a /model argument to a reasoning level, leniently.
// It accepts an exact level name or any unambiguous case-insensitive prefix
// ("med" → medium, "hi" → high). It returns the canonical level and true on a
// unique match; a prefix that matches several levels (e.g. "m" → minimal,
// medium) yields false so the token falls through to model resolution / error.
func resolveReasoningArg(arg string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(arg))
	if s == "" {
		return "", false
	}
	var matches []string
	for _, name := range reasoningLevelNames {
		if name == s {
			return name, true // exact wins outright
		}
		if strings.HasPrefix(name, s) {
			matches = append(matches, name)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return "", false
}

func modelCommandSelectsReadyModel(message string, modelList []ModelInfo) bool {
	fields := strings.Fields(strings.TrimSpace(message))
	if len(fields) < 2 || fields[0] != "/model" {
		return false
	}
	for _, arg := range fields[1:] {
		_, isLevel := resolveReasoningArg(arg)
		modelID, _, strong := resolveModelArg(arg, modelList)
		if modelID != "" && (!isLevel || strong) {
			return true
		}
	}
	return false
}

// resolveModelArg matches a /model argument to a ready model id, leniently. In
// priority order: an exact id; a case/dot-insensitive exact spelling; a unique
// id prefix; a unique substring match. It returns (id, nil, strong) on a unique
// match, where strong is true for the exact/normalized/prefix tiers and false
// for a mere substring match. When several models match a partial it returns
// ("", candidates, false) so the caller can ask the user to disambiguate; no
// match returns ("", nil, false).
//
// The strong flag lets the caller avoid a false "ambiguous" rejection: a
// single-letter level prefix like "o" (off) can incidentally be a substring of
// some model id, but that weak match must not override an explicit level.
func resolveModelArg(arg string, modelList []ModelInfo) (string, []string, bool) {
	norm := func(s string) string { return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), ".", "-") }
	want := norm(arg)
	if want == "" {
		return "", nil, false
	}

	var exact, prefix, substr []string
	for _, m := range modelList {
		if !m.Ready {
			continue
		}
		if m.ID == strings.TrimSpace(arg) {
			return m.ID, nil, true // exact id wins outright
		}
		id := norm(m.ID)
		switch {
		case id == want:
			exact = append(exact, m.ID)
		case strings.HasPrefix(id, want):
			prefix = append(prefix, m.ID)
		case strings.Contains(id, want):
			substr = append(substr, m.ID)
		}
	}
	// Most-specific non-empty tier wins; a unique entry resolves, multiple ask.
	// The first two tiers (exact, prefix) are strong; substring is weak.
	for i, tier := range [][]string{exact, prefix, substr} {
		strong := i < 2
		if len(tier) == 1 {
			return tier[0], nil, strong
		}
		if len(tier) > 1 {
			return "", tier, false
		}
	}
	return "", nil, false
}

// modelDisplayName returns the human-friendly display name for a model id, or
// the id itself when the model isn't in the list or has no distinct display
// name.
func modelDisplayName(id string, modelList []ModelInfo) string {
	for _, m := range modelList {
		if m.ID == id {
			if m.DisplayName != "" {
				return m.DisplayName
			}
			return m.ID
		}
	}
	return id
}

// isReadyModel reports whether id names a model that is present and ready.
func isReadyModel(id string, modelList []ModelInfo) bool {
	for _, m := range modelList {
		if m.ID == id {
			return m.Ready
		}
	}
	return false
}

// modelCommandStatus builds the human-readable body shown for a bare /model
// command or an invalid argument: the current model and reasoning level plus
// the available options. Models that advertise an exact reasoning level list
// show it; the rest accept the standard levels through xhigh.
func modelCommandStatus(currentModel, currentReasoning string, modelList []ModelInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Current model: %s\nCurrent reasoning: %s", currentModel, reasoningDisplayName(currentReasoning))
	b.WriteString("\n\nAvailable models:")
	for _, m := range modelList {
		if !m.Ready {
			continue
		}
		name := m.ID
		if m.DisplayName != "" && m.DisplayName != m.ID {
			name = fmt.Sprintf("%s (%s)", m.ID, m.DisplayName)
		}
		fmt.Fprintf(&b, "\n  /model %s", name)
		switch {
		case !m.SupportsReasoning:
			b.WriteString(" — no reasoning")
		case len(m.ReasoningLevels) > 0:
			fmt.Fprintf(&b, " — %s", strings.Join(m.ReasoningLevels, ", "))
		}
	}
	fmt.Fprintf(&b, "\n\nReasoning levels (use as /model <level> or /model <id> <level>): %s\nModels without a listed set accept %s through %s.",
		strings.Join(reasoningLevelNames, ", "), reasoningLevelNames[0], reasoningLevelNames[len(reasoningLevelNames)-2])
	return b.String()
}

// recordModelCommandReply records a modelchange marker carrying an informational
// reply (bare /model, already-using, or an error) without actually switching.
func (s *Server) recordModelCommandReply(ctx context.Context, conversationID string, manager *ConversationManager, text string) {
	if err := manager.recordModelCommandInfo(ctx, text); err != nil {
		s.logger.Error("Failed to record /model reply", "conversationID", conversationID, "error", err)
	}
}

func writeModelCommandAccepted(w http.ResponseWriter) {
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "model-command"})
}

// getModelList returns the list of available models
func (s *Server) getModelList() []ModelInfo {
	modelList := []ModelInfo{}
	if s.predictableOnly {
		modelList = append(modelList, ModelInfo{ID: "predictable", Ready: true, MaxContextTokens: 200000, SupportsImages: true, SupportsReasoning: true})
	} else {
		modelIDs := s.llmManager.GetAvailableModels()
		for _, id := range modelIDs {
			// Skip predictable model unless predictable-only flag is set
			if id == "predictable" {
				continue
			}
			svc, err := s.llmManager.GetService(id)
			supportsImages := false
			supportsReasoning := false
			var reasoningLevels []string
			defaultReasoning := ""
			if err == nil && svc != nil {
				supportsImages = svc.SupportsImages()
				supportsReasoning = llm.SupportsReasoning(svc)
				for _, level := range llm.SupportedReasoningLevels(svc) {
					reasoningLevels = append(reasoningLevels, level.Name())
				}
				defaultReasoning = llm.ServiceDefaultReasoningLevel(svc)
			}
			info := ModelInfo{ID: id, Ready: err == nil, SupportsImages: supportsImages, SupportsReasoning: supportsReasoning, ReasoningLevels: reasoningLevels, DefaultReasoningLevel: defaultReasoning}
			// Add display name and source from model info
			if modelInfo := s.llmManager.GetModelInfo(id); modelInfo != nil {
				info.DisplayName = modelInfo.DisplayName
				info.Mode = modelInfo.Mode
				info.Source = modelInfo.Source
				info.BaseURL = modelInfo.BaseURL
				info.APIType = modelInfo.APIType
				info.APIModelName = modelInfo.APIModelName
				info.MaxContextTokens, _ = modelsdev.LookupContextLimit(modelInfo.BaseURL, modelInfo.APIModelName)
			}
			modelList = append(modelList, info)
		}
	}
	assignModelTiers(modelList)
	return modelList
}

// assignModelTiers stamps each ready model with its tier (1 or 2) based on the
// set of ready models and the curated shadow relationships in package models.
// Not-ready models are left at their zero tier (omitted from JSON).
func assignModelTiers(modelList []ModelInfo) {
	var readyIDs []string
	for _, m := range modelList {
		if m.Ready {
			readyIDs = append(readyIDs, m.ID)
		}
	}
	tiers := models.AssignTiers(readyIDs)
	for i := range modelList {
		if modelList[i].Ready {
			if modelList[i].Source == models.SourceCustomLabel {
				modelList[i].Tier = models.Tier1
			} else {
				modelList[i].Tier = tiers[modelList[i].ID]
			}
		}
	}
}

// effectiveDefaultModel returns the model id to use when the client hasn't
// picked one. A configured override wins; otherwise modelList order is
// authoritative. Returns "" only when no model is ready.
func (s *Server) effectiveDefaultModel(modelList []ModelInfo) string {
	if len(modelList) == 0 {
		return ""
	}
	if s.defaultModel != "" {
		for _, m := range modelList {
			if m.ID == s.defaultModel && m.Ready {
				return s.defaultModel
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
		s.internalError(w, "Failed to search conversations", err)
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
		s.internalError(w, "Failed to get archived conversations", err)
		return
	}

	ids := make([]string, len(conversations))
	for i := range conversations {
		ids[i] = conversations[i].ConversationID
	}
	participants, err := s.db.ConversationParticipants(ctx, ids)
	if err != nil {
		s.internalError(w, "Failed to get archived conversation participants", err)
		return
	}
	type archivedConversationResponse struct {
		generated.Conversation
		Participants []db.ConversationParticipant `json:"participants,omitempty"`
	}
	decorated := make([]archivedConversationResponse, len(conversations))
	for i, conversation := range conversations {
		decorated[i] = archivedConversationResponse{
			Conversation: conversation,
			Participants: participants[conversation.ConversationID],
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(decorated)
}

// handleArchiveConversation handles POST /conversation/<id>/archive
func (s *Server) handleArchiveConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	ctx := r.Context()
	conversation, err := s.db.ArchiveConversation(ctx, conversationID)
	if err != nil {
		s.internalError(w, "Failed to archive conversation", err, "conversationID", conversationID)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conversation)
}

// handleUnarchiveConversation handles POST /conversation/<id>/unarchive
func (s *Server) handleUnarchiveConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	ctx := r.Context()
	conversation, err := s.db.UnarchiveConversation(ctx, conversationID)
	if err != nil {
		s.internalError(w, "Failed to unarchive conversation", err, "conversationID", conversationID)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conversation)
}

// handleDeleteConversation handles POST /conversation/<id>/delete
func (s *Server) handleDeleteConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	ctx := r.Context()
	if err := s.deleteConversation(ctx, conversationID); err != nil {
		// The terminals are already global at this point. That is harmless and
		// visible to the user, so no rollback is attempted.
		s.internalError(w, "Failed to delete conversation", err, "conversationID", conversationID)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

// handleConversationBySlug handles GET /api/conversation-by-slug/<slug>
func (s *Server) handleConversationBySlug(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/api/conversation-by-slug/")
	if slug == "" {
		http.Error(w, "Slug required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	conversation, err := s.db.GetConversationBySlug(ctx, slug)
	if err != nil && strings.Contains(err.Error(), "not found") {
		// Fall back to conversation_id lookup so draft URLs (/c/<id>)
		// resolve before the conversation list is in memory.
		conversation, err = s.db.GetConversationByID(ctx, slug)
		if err != nil && strings.Contains(err.Error(), "not found") {
			http.Error(w, "Conversation not found", http.StatusNotFound)
			return
		}
	}
	if err != nil {
		s.internalError(w, "Failed to get conversation by slug", err, "slug", slug)
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
		if isUniqueConstraintErr(err) {
			http.Error(w, "A conversation with that slug already exists", http.StatusConflict)
			return
		}
		s.internalError(w, "Failed to rename conversation", err, "conversationID", conversationID)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conversation)
}

// SetCwdRequest represents a request to move a conversation to a different
// working directory.
type SetCwdRequest struct {
	Cwd string `json:"cwd"`
}

// handleSetConversationCwd handles POST /conversation/<id>/cwd: the user-driven
// counterpart of the agent's change_dir tool, backing the directory segment of
// the status readout.
//
// Validation happens before anything moves. A half-applied change — the row
// updated but the running toolset not, or vice versa — is worse than a refused
// one, because nothing afterwards would notice the disagreement.
func (s *Server) handleSetConversationCwd(w http.ResponseWriter, r *http.Request, conversationID string) {
	ctx := r.Context()

	var req SetCwdRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Absolute only: a relative path has no meaning here. The obvious base to
	// resolve against would be the conversation's current cwd, which is the very
	// thing being replaced — so ".." would mean different directories depending
	// on when the request landed.
	if req.Cwd == "" || !filepath.IsAbs(req.Cwd) {
		http.Error(w, "cwd must be an absolute path", http.StatusBadRequest)
		return
	}
	cwd := filepath.Clean(req.Cwd)
	info, err := os.Stat(cwd)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "directory does not exist", http.StatusBadRequest)
			return
		}
		http.Error(w, fmt.Sprintf("cannot use directory: %v", err), http.StatusBadRequest)
		return
	}
	if !info.IsDir() {
		http.Error(w, "path is not a directory", http.StatusBadRequest)
		return
	}

	conversation, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to load conversation for cwd change", "conversationID", conversationID, "error", err)
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if conversation.Archived {
		http.Error(w, "conversation is archived", http.StatusConflict)
		return
	}
	// A draft has no agent and no history yet; its cwd is still client state and
	// travels with the first send (see the promote path in handleChatConversation).
	// Creating a manager here to move it would hydrate the conversation and write
	// a system prompt — pinned to the OLD directory, since the row hasn't moved
	// yet — well before the user has sent anything.
	if conversation.IsDraft {
		http.Error(w, "conversation is still a draft; set its directory before sending", http.StatusConflict)
		return
	}

	userEmail := r.Header.Get("X-ExeDev-Email")
	ctx = contextWithUserEmail(ctx, userEmail)

	// Route through the manager even when one isn't already live: it owns the
	// in-memory cwd and the toolset, and creating it here is what makes the
	// change visible to a turn that starts moments later.
	manager, err := s.getOrCreateConversationManager(ctx, conversationID, userEmail)
	if err != nil {
		s.internalError(w, "Failed to get conversation manager for cwd change", err, "conversationID", conversationID)
		return
	}

	// Mid-turn, the agent may be running a command right now; moving the ground
	// under it would make its output a lie. Ask the user to wait rather than
	// cancelling their turn for them. (The model picker takes the opposite side
	// of this trade, but it has no choice — a model switch cannot be applied
	// without rebuilding the loop.) SetCwd re-checks this atomically; the point
	// of checking here is the specific message.
	if manager.IsAgentWorking() {
		http.Error(w, "Finish or stop the current turn to change the directory", http.StatusConflict)
		return
	}

	if err := manager.SetCwd(ctx, cwd); err != nil {
		if errors.Is(err, errAgentWorking) {
			http.Error(w, "Finish or stop the current turn to change the directory", http.StatusConflict)
			return
		}
		s.internalError(w, "Failed to change conversation cwd", err, "conversationID", conversationID)
		return
	}

	updated, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		s.internalError(w, "Failed to reload conversation after cwd change", err, "conversationID", conversationID)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
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
	var req TagsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	tags := normalizeTags(req.Tags)
	conversation, err := s.db.UpdateConversationTags(r.Context(), conversationID, tags)
	if err != nil {
		s.internalError(w, "Failed to update conversation tags", err, "conversationID", conversationID)
		return
	}

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
		if err := s.markResumeAfterRestart(r.Context()); err != nil {
			s.logger.Error("Failed to mark resume-after-upgrade; continuing restart", "error", err)
		}

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

// markResumeAfterRestart makes the next server process continue conversations
// that are still mid-turn when this process exits. The startup path consumes
// the marker exactly once.
func (s *Server) markResumeAfterRestart(ctx context.Context) error {
	return s.db.SetSetting(ctx, db.ResumeAfterUpgradeSettingKey, "1")
}

// restartExitCode makes systemd's Restart=on-failure immediately start the
// replacement process. A clean exit may leave Shelley stopped until its socket
// receives another request, which cannot resume a headless conversation.
const restartExitCode = 1

// handleExit exits the process, expecting systemd or similar to restart it.
// With ?resume=true, in-flight conversations continue on the next process.
func (s *Server) handleExit(w http.ResponseWriter, r *http.Request) {
	resume := r.URL.Query().Get("resume") == "true"
	message := "Exiting..."
	exitCode := 0
	if resume {
		if err := s.markResumeAfterRestart(r.Context()); err != nil {
			s.logger.Error("Failed to mark conversations for resume after restart", "error", err)
			http.Error(w, fmt.Sprintf("Failed to prepare conversations for restart: %v", err), http.StatusInternalServerError)
			return
		}
		message = "Exiting; active conversations will resume after restart..."
		exitCode = restartExitCode
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": message})

	// Flush the response before exiting. The delay also gives an agent that
	// invoked this endpoint as a tool time to persist the tool result before
	// the process is replaced.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(s.exitDelay)
		s.logger.Info("Exiting Shelley via /exit endpoint", "resume", resume, "exit_code", exitCode)
		s.exitProcess(exitCode)
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
		"auto_upgrade":      true,
		exeNotifySettingKey: true,
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

	// The exe_notify setting is read on each end-of-turn (see
	// withExeNotifyHook), so no dispatcher reload is needed here.

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleSendQueuedNow interrupts the active turn and drains the durable queue
// from its FIFO head. queued_id must name that head item (checked under the
// loop lifecycle lock in SendQueuedNow) so a button on a stale or later ghost
// cannot reorder user messages.
func (s *Server) handleSendQueuedNow(w http.ResponseWriter, r *http.Request, conversationID string) {
	queuedID := r.URL.Query().Get("queued_id")
	if queuedID == "" {
		http.Error(w, "queued_id is required", http.StatusBadRequest)
		return
	}
	conversation, err := s.db.GetConversationByID(r.Context(), conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to get conversation", err, "conversationID", conversationID)
		return
	}
	if conversation.Archived {
		http.Error(w, "conversation is archived", http.StatusConflict)
		return
	}
	manager, err := s.getOrCreateConversationManager(r.Context(), conversationID, r.Header.Get("X-ExeDev-Email"))
	if err != nil {
		s.internalError(w, "Failed to initialize conversation for send now", err, "conversationID", conversationID)
		return
	}
	if err := manager.SendQueuedNow(r.Context(), s, queuedID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleCancelQueued handles POST /conversation/<id>/cancel-queued
// Cancels pending queued user messages for a conversation. With a ?queued_id=
// query param it removes a single queued message by its QueuedMessage id;
// without it, the whole queue is cleared.
func (s *Server) handleCancelQueued(w http.ResponseWriter, r *http.Request, conversationID string) {
	queuedID := r.URL.Query().Get("queued_id")
	ctx := r.Context()

	s.mu.Lock()
	manager, active := s.activeConversations[conversationID]
	s.mu.Unlock()

	// Without an active manager (e.g. after a restart, before the conversation
	// is opened) the queued_messages array may still hold persisted entries, so
	// remove them directly.
	err := s.cancelQueuedTranscriptions(ctx, conversationID, queuedID, func() (err error) {
		switch {
		case active && queuedID != "":
			_, err = manager.CancelQueuedMessage(ctx, s, queuedID)
		case active:
			_, err = manager.CancelQueuedMessages(ctx, s)
		case queuedID != "":
			_, err = s.db.RemoveQueuedMessages(ctx, conversationID, queuedID)
		default:
			_, err = s.db.ClearQueuedMessages(ctx, conversationID)
		}
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to cancel queued messages", err, "conversationID", conversationID)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type RegisterConversationHookRequest struct {
	URL string `json:"url"`
}

func (s *Server) handleRegisterConversationHook(w http.ResponseWriter, r *http.Request, conversationID string) {
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
		s.internalError(w, "Failed to get conversation manager", err, "conversationID", conversationID)
		return
	}
	if err := manager.RegisterEndOfTurnHook(r.Context(), db.ConversationHook{URL: req.URL}); err != nil {
		s.internalError(w, "Failed to register conversation hook", err, "conversationID", conversationID, "hook_url", req.URL)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "registered"})
}

// ForkRequest is the body for POST /conversation/<id>/fork. The fork copies all
// messages up to and including the message identified by MessageID (preferred)
// or SequenceID into a new conversation.
type ForkRequest struct {
	MessageID  string `json:"message_id,omitempty"`
	SequenceID int64  `json:"sequence_id,omitempty"`
}

// handleForkConversation handles POST /conversation/<id>/fork. It creates a new
// top-level conversation containing copies of the source conversation's
// messages up to and including a cutoff point, then returns the new
// conversation so the client can navigate to it.
func (s *Server) handleForkConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	ctx := r.Context()

	var req ForkRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
	}

	// Resolve the cutoff sequence_id. A message_id takes precedence; otherwise
	// use the supplied sequence_id, or fall back to the conversation's latest
	// message (fork the whole thing).
	cutoff := req.SequenceID
	if req.MessageID != "" {
		msg, err := s.db.GetMessageByID(ctx, req.MessageID)
		if err != nil {
			http.Error(w, "Message not found", http.StatusNotFound)
			return
		}
		if msg.ConversationID != conversationID {
			http.Error(w, "Message does not belong to this conversation", http.StatusBadRequest)
			return
		}
		cutoff = msg.SequenceID
	} else {
		// No explicit message_id: clamp the cutoff to the conversation's latest
		// sequence_id. A non-positive (or out-of-range) value forks the whole
		// conversation. This also rejects forks of empty conversations.
		latest, err := s.db.GetLatestActionableMessage(ctx, conversationID)
		if err != nil {
			http.Error(w, "Conversation has no messages to fork", http.StatusBadRequest)
			return
		}
		if cutoff <= 0 || cutoff > latest.SequenceID {
			cutoff = latest.SequenceID
		}
	}

	forked, err := s.db.ForkConversation(ctx, conversationID, cutoff)
	if errors.Is(err, db.ErrCannotForkBtwReader) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if errors.Is(err, db.ErrInvalidForkPoint) {
		http.Error(w, "Invalid fork point: no message at or before the requested cutoff", http.StatusBadRequest)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to fork conversation", err, "conversationID", conversationID)
		return
	}

	// ForkConversation copies the source's CURRENT model/options, but the fork
	// should continue from the state as of the cutoff. If the source switched
	// model or reasoning via /model AFTER the cutoff, rewind those changes so
	// the fork uses what was in effect at the fork point.
	if err := s.applyForkPointModelState(ctx, conversationID, forked.ConversationID, cutoff); err != nil {
		s.internalError(w, "Failed to set fork-point model state", err, "conversationID", forked.ConversationID)
		return
	}

	// Give the fork a distinct slug derived from the source's, so it shows up
	// in the drawer with a meaningful name. We do this synchronously (no LLM)
	// using the source slug as a base; ForkConversation left the slug nil.
	if source, serr := s.db.GetConversationByID(ctx, conversationID); serr == nil && source != nil {
		base := "fork"
		if source.Slug != nil && *source.Slug != "" {
			base = *source.Slug + "-fork"
		}
		if sanitized := slug.Sanitize(base); sanitized != "" {
			candidate := sanitized
			for attempt := 0; attempt < 100; attempt++ {
				if updated, uerr := s.db.UpdateConversationSlug(ctx, forked.ConversationID, candidate); uerr == nil {
					forked = updated
					break
				} else if isUniqueConstraintErr(uerr) {
					candidate = fmt.Sprintf("%s-%d", sanitized, attempt+1)
					continue
				} else {
					s.logger.Warn("Failed to assign slug to forked conversation", "conversationID", forked.ConversationID, "error", uerr)
					break
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(forked)
}

// applyForkPointModelState rewinds the fork's model and reasoning level to what
// was in effect at the cutoff. ForkConversation seeds the fork with the
// source's CURRENT model/options; if the source used /model to switch model or
// reasoning AFTER the cutoff, those later markers would otherwise leak into the
// fork. We replay the source's modelchange markers that occur strictly after
// the cutoff, in reverse, to undo each one: a marker recording a change from X
// to Y means the pre-marker model was X, so the earliest such "from" wins.
func (s *Server) applyForkPointModelState(ctx context.Context, sourceID, forkID string, cutoff int64) error {
	msgs, err := s.db.ListMessages(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("list source messages: %w", err)
	}

	// Walk markers after the cutoff in order; the FIRST model "from" and the
	// FIRST reasoning "from" we encounter are the values in effect at the fork
	// point (each marker's pre-state). Later markers only reflect changes the
	// fork should not inherit.
	var modelAtFork string
	var haveModel bool
	var reasoningAtFork string
	var haveReasoning bool
	for _, m := range msgs {
		if m.SequenceID <= cutoff || m.Type != string(db.MessageTypeModelChange) || m.UserData == nil {
			continue
		}
		var ud ModelChangeUserData
		if err := json.Unmarshal([]byte(*m.UserData), &ud); err != nil {
			continue // informational markers / malformed payloads carry no state
		}
		if !haveModel && ud.To != "" {
			// ud.From may be "" (first-ever model set); that's still the
			// correct pre-cutoff state and we record it as "seen".
			modelAtFork = ud.From
			haveModel = true
		}
		if !haveReasoning && ud.ReasoningTo != "" {
			// ReasoningFrom is a user-facing name; "default" maps back to the
			// stored empty level.
			reasoningAtFork = normalizeReasoningFromDisplay(ud.ReasoningFrom)
			haveReasoning = true
		}
	}

	if haveModel {
		if modelAtFork == "" {
			// The conversation had no model before this point; leave the fork's
			// copied model as-is only if we can't do better. In practice a
			// conversation always has a model, so this is defensive.
		} else if err := s.db.ForceUpdateConversationModel(ctx, forkID, modelAtFork); err != nil {
			return fmt.Errorf("set fork model: %w", err)
		}
	}
	if haveReasoning {
		fork, err := s.db.GetConversationByID(ctx, forkID)
		if err != nil {
			return fmt.Errorf("reload fork: %w", err)
		}
		opts := db.ParseConversationOptions(fork.ConversationOptions)
		opts.ThinkingLevel = reasoningAtFork
		if err := s.db.UpdateConversationOptions(ctx, forkID, opts); err != nil {
			return fmt.Errorf("set fork reasoning: %w", err)
		}
	}
	return nil
}

// normalizeReasoningFromDisplay reverses reasoningDisplayName: the user-facing
// "default" maps back to the stored empty (service-default) level; every other
// value is a concrete level name stored verbatim.
func normalizeReasoningFromDisplay(name string) string {
	if name == "default" {
		return ""
	}
	return name
}

// isUniqueConstraintErr reports whether err is a SQLite UNIQUE constraint
// violation (e.g. a slug collision).
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate")
}

// handleStartNewGeneration handles POST /conversation/<id>/new-generation.
func (s *Server) handleStartNewGeneration(w http.ResponseWriter, r *http.Request, conversationID string) {
	ctx := r.Context()
	conversation, err := s.startNewGeneration(ctx, conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to start new generation", err, "conversationID", conversationID)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conversation)
}

func (s *Server) startNewGeneration(ctx context.Context, conversationID string) (generated.Conversation, error) {
	// Detach from the caller's context. Bumping the generation and hydrating the
	// new one is a two-step mutation that must not be abandoned half-done: the
	// bump commits in its own transaction, so a client that disconnects in
	// between leaves the conversation on a new generation with no system prompt,
	// and /clear reports a 500. CI saw this as "hydrate after generation bump:
	// failed to store system prompt: context canceled" -- a loaded host widens
	// the window, but a user navigating away is enough.
	ctx = context.WithoutCancel(ctx)

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
				s.notifySubscribers(ctx, conversationID, messages[i])
			}
		}
	}

	manager.broadcastStream(StreamResponse{Conversation: &conversation})

	return conversation, nil
}

// validateConversationOptions runs the same checks the new-conversation
// path applies. Returns ("", nil) when opts are valid; otherwise a 400
// message suitable for the client.
func findModelInfo(id string, models []ModelInfo) *ModelInfo {
	for i := range models {
		if models[i].ID == id {
			return &models[i]
		}
	}
	return nil
}

func roundModelReasoningLevel(model *ModelInfo, level string) (string, bool) {
	if level == "" || model == nil {
		return level, false
	}
	if !model.SupportsReasoning {
		return "", true
	}
	if len(model.ReasoningLevels) == 0 {
		if level == "max" {
			return "", true
		}
		return level, false
	}
	supported := make([]llm.ThinkingLevel, 0, len(model.ReasoningLevels))
	for _, name := range model.ReasoningLevels {
		if parsed := llm.ParseThinkingLevel(name); parsed != llm.ThinkingLevelDefault {
			supported = append(supported, parsed)
		}
	}
	rounded := llm.ClampThinkingLevel(llm.ParseThinkingLevel(level), supported).Name()
	if validateModelReasoningLevel(model, rounded) != "" {
		// Clamping can fail to land on a supported level (e.g. a model that
		// only advertises off never attracts non-off levels); reset instead
		// of persisting an unsendable level.
		return "", true
	}
	return rounded, rounded != level
}

func validateModelReasoningLevel(model *ModelInfo, level string) string {
	if level == "" || model == nil {
		return ""
	}
	if !model.SupportsReasoning {
		return fmt.Sprintf("Model %s does not support reasoning; use default.", model.ID)
	}
	if len(model.ReasoningLevels) == 0 {
		if level == "max" {
			return fmt.Sprintf("Model %s does not advertise reasoning level max; use default or a standard level through xhigh.", model.ID)
		}
		return ""
	}
	for _, supported := range model.ReasoningLevels {
		if level == supported {
			return ""
		}
	}
	return fmt.Sprintf("Model %s does not support reasoning level %s; choose one of: %s.", model.ID, level, strings.Join(model.ReasoningLevels, ", "))
}

func validateConversationOptions(opts db.ConversationOptions) string {
	if opts.Kind != "" || opts.ParentPointer != nil || opts.CommitTour != nil || opts.SystemPrompt != "" {
		return "kind, parent_pointer, commit_tour, and system_prompt are internal conversation options"
	}
	for name, v := range opts.ToolOverrides {
		if v != "on" && v != "off" {
			return fmt.Sprintf("Invalid tool_overrides[%s]=%q; must be \"on\" or \"off\"", name, v)
		}
	}
	for _, hook := range opts.EndOfTurnHooks {
		if err := validateConversationHookURL(hook.URL); err != nil {
			return fmt.Sprintf("Invalid end_of_turn_hooks url %q: %v", hook.URL, err)
		}
	}
	if opts.CompactNudgeTokens < 0 {
		return "compact_nudge_tokens must not be negative"
	}
	if opts.ThinkingLevel != "" {
		switch opts.ThinkingLevel {
		case "off", "minimal", "low", "medium", "high", "xhigh", "max":
		default:
			return fmt.Sprintf("Invalid thinking_level: %q; must be one of off, minimal, low, medium, high, xhigh, max", opts.ThinkingLevel)
		}
	}
	return ""
}

// CreateDraftRequest is the body for POST /api/conversations/draft.
type CreateDraftRequest struct {
	Draft               string                  `json:"draft"`
	Model               string                  `json:"model,omitempty"`
	Cwd                 string                  `json:"cwd,omitempty"`
	ConversationOptions *db.ConversationOptions `json:"conversation_options,omitempty"`
}

// handleCreateDraft creates a new draft conversation. The returned row
// shows up in the conversation list (via Pool.OnCommit) and the client
// owns it from then on: edits go through PUT /draft, sending a message
// promotes it via the standard chat handler, and DELETE /delete removes
// it like any other conversation.
func (s *Server) handleCreateDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req CreateDraftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	modelID := req.Model
	if modelID == "" {
		modelID = s.effectiveDefaultModel(s.getModelList())
	}
	// A draft is autosaved composer text, not a turn, so it must not require a
	// usable model. Rejecting it would discard what the user typed and wedge
	// the client's draft autosave in a retry loop while they are off fixing
	// their model setup. An EXPLICIT model is still validated (that's a real
	// client error); an empty/defaulted one is left to the promoting send.
	if req.Model != "" {
		if _, err := s.llmManager.GetService(modelID); err != nil {
			http.Error(w, unsupportedModelMessage(modelID, s.getModelList()), http.StatusBadRequest)
			return
		}
	}
	var cwdPtr *string
	if req.Cwd != "" {
		cwdPtr = &req.Cwd
	}
	var convOpts db.ConversationOptions
	if req.ConversationOptions != nil {
		convOpts = *req.ConversationOptions
		if msg := validateConversationOptions(convOpts); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if msg := validateModelReasoningLevel(findModelInfo(modelID, s.getModelList()), convOpts.ThinkingLevel); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
	}
	conv, err := s.db.CreateDraftConversation(ctx, cwdPtr, &modelID, convOpts, req.Draft)
	if err != nil {
		s.internalError(w, "Failed to create draft", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(conv)
}

// UpdateDraftRequest is the body for PUT /api/conversation/<id>/draft.
// All fields are optional; absent (or null) fields keep their current
// value, so text autosaves, model-picker changes, and cwd changes update
// independently without clobbering each other. This mirrors
// CreateDraftRequest the same way PUT-after-POST usually does.
type UpdateDraftRequest struct {
	Draft *string `json:"draft,omitempty"`
	Model *string `json:"model,omitempty"`
	Cwd   *string `json:"cwd,omitempty"`
}

// handleUpdateDraft partially updates a draft conversation: the draft text
// (composer autosave), the model (composer picker — so a promote that
// omits `model`, like the iOS push "Reply" handler, and other devices
// reopening the draft see the pick), and/or the cwd (command palette
// "set working directory") — without losing the fields it doesn't name.
// 404 when the conversation is not a draft (either it never was, or it was
// already promoted by a chat post): once promoted, cwd is immutable and
// the model only changes through the /model loop switch.
func (s *Server) handleUpdateDraft(w http.ResponseWriter, r *http.Request, conversationID string) {
	ctx := r.Context()
	var req UpdateDraftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	// Empty string is meaningful for draft (clear the text) but nonsense
	// for model/cwd — those want "absent means keep".
	if req.Model != nil {
		if *req.Model == "" {
			http.Error(w, "model must not be empty", http.StatusBadRequest)
			return
		}
		if _, err := s.llmManager.GetService(*req.Model); err != nil {
			http.Error(w, unsupportedModelMessage(*req.Model, s.getModelList()), http.StatusBadRequest)
			return
		}
	}
	if req.Cwd != nil && *req.Cwd == "" {
		http.Error(w, "cwd must not be empty", http.StatusBadRequest)
		return
	}
	conv, err := s.db.UpdateDraft(ctx, conversationID, req.Draft, req.Model, req.Cwd)
	if errors.Is(err, db.ErrConversationNotDraft) {
		http.Error(w, "Not a draft conversation", http.StatusNotFound)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to update draft", err, "conversationID", conversationID)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conv)
}
