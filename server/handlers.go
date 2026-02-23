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
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	clean := p
	// Do not resolve symlinks here; enforce string prefix restriction only
	if !(strings.HasPrefix(clean, browse.ScreenshotDir+"/")) {
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleUpload handles file uploads via POST /api/upload
// Files are saved to the ScreenshotDir with a random filename
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Limit to 10MB file size
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024)

	// Parse the multipart form
	if err := r.ParseMultipartForm(10 * 1024 * 1024); err != nil {
		http.Error(w, "failed to parse form: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Get the file from the multipart form
	file, handler, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "failed to get uploaded file: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Generate a unique ID (8 random bytes converted to 16 hex chars)
	randBytes := make([]byte, 8)
	if _, err := rand.Read(randBytes); err != nil {
		http.Error(w, "failed to generate random filename: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get file extension from the original filename
	ext := filepath.Ext(handler.Filename)

	// Create a unique filename in the ScreenshotDir
	filename := filepath.Join(browse.ScreenshotDir, fmt.Sprintf("upload_%s%s", hex.EncodeToString(randBytes), ext))

	// Ensure the directory exists
	if err := os.MkdirAll(browse.ScreenshotDir, 0o755); err != nil {
		http.Error(w, "failed to create directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Create the destination file
	destFile, err := os.Create(filename)
	if err != nil {
		http.Error(w, "failed to create destination file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer destFile.Close()

	// Copy the file contents to the destination file
	if _, err := io.Copy(destFile, file); err != nil {
		http.Error(w, "failed to save file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Return the path to the saved file
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"path": filename})
}

// staticHandler serves files from the provided filesystem.
// For JS/CSS files, it serves pre-compressed .gz versions with content-based ETags.
func isConversationSlugPath(path string) bool {
	return strings.HasPrefix(path, "/c/")
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

	// Load checksums for ETag support (content-based, not git-based)
	checksums := ui.Checksums()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Inject initialization data into index.html
		if r.URL.Path == "/" || r.URL.Path == "/index.html" || isConversationSlugPath(r.URL.Path) {
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
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 400">
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

	// Select default model - use configured default if available, otherwise first ready model
	// If no models are available, default_model should be empty
	defaultModel := ""
	if len(modelList) > 0 {
		defaultModel = s.defaultModel
		if defaultModel == "" {
			defaultModel = models.Default().ID
		}
		defaultModelAvailable := false
		for _, m := range modelList {
			if m.ID == defaultModel && m.Ready {
				defaultModelAvailable = true
				break
			}
		}
		if !defaultModelAvailable {
			// Fall back to first ready model
			for _, m := range modelList {
				if m.Ready {
					defaultModel = m.ID
					break
				}
			}
		}
	}

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

	initData := map[string]interface{}{
		"models":        modelList,
		"default_model": defaultModel,
		"hostname":      hostname,
		"default_cwd":   defaultCwd,
		"home_dir":      homeDir,
	}
	if s.terminalURL != "" {
		initData["terminal_url"] = s.terminalURL
	}
	if len(s.links) > 0 {
		initData["links"] = s.links
	}

	// Inject notification channel type metadata for the settings modal
	initData["notification_channel_types"] = s.getNotificationChannelTypes()

	initJSON, err := json.Marshal(initData)
	if err != nil {
		http.Error(w, "Failed to marshal init data", http.StatusInternalServerError)
		return
	}

	// Generate favicon as data URI
	faviconSVG := generateFaviconSVG(hostname)
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
	searchContent := r.URL.Query().Get("search_content") == "true"

	// Get conversations from database
	var conversations []generated.Conversation
	var err error

	if query != "" {
		if searchContent {
			// Search in both slug and message content
			conversations, err = s.db.SearchConversationsWithMessages(ctx, query, int64(limit), int64(offset))
		} else {
			// Search only in slug
			conversations, err = s.db.SearchConversations(ctx, query, int64(limit), int64(offset))
		}
	} else {
		conversations, err = s.db.ListConversations(ctx, int64(limit), int64(offset))
	}

	if err != nil {
		s.logger.Error("Failed to get conversations", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Get working states for all active conversations
	workingStates := s.getWorkingConversations()

	// Build response with working state included
	// Cache git info by cwd to avoid redundant git subprocess calls
	gitStates := make(map[string]*gitstate.GitState)
	result := make([]ConversationWithState, len(conversations))
	for i, conv := range conversations {
		cws := ConversationWithState{
			Conversation: conv,
			Working:      workingStates[conv.ConversationID],
		}
		if conv.Cwd != nil {
			gs, ok := gitStates[*conv.Cwd]
			if !ok {
				gs = gitstate.GetGitState(*conv.Cwd)
				gitStates[*conv.Cwd] = gs
			}
			if gs.IsRepo {
				cws.GitRepoRoot = gs.Worktree
				cws.GitWorktreeRoot = getGitWorktreeRoot(gs.Worktree)
				cws.GitCommit = gs.Commit
				cws.GitSubject = gs.Subject
			}
		}
		result[i] = cws
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// conversationMux returns a mux for /api/conversation/<id>/* routes
func (s *Server) conversationMux() *http.ServeMux {
	mux := http.NewServeMux()
	// GET /api/conversation/<id> - returns all messages (can be large, compress)
	mux.Handle("GET /{id}", gzipHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleGetConversation(w, r, r.PathValue("id"))
	})))
	// GET /api/conversation/<id>/stream - SSE stream (do NOT compress)
	// TODO: Consider gzip for SSE in the future. Would reduce bandwidth
	// for large tool outputs, but needs flush after each event.
	mux.HandleFunc("GET /{id}/stream", func(w http.ResponseWriter, r *http.Request) {
		s.handleStreamConversation(w, r, r.PathValue("id"))
	})
	// POST endpoints - small responses, no compression needed
	mux.HandleFunc("POST /{id}/chat", func(w http.ResponseWriter, r *http.Request) {
		s.handleChatConversation(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("POST /{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		s.handleCancelConversation(w, r, r.PathValue("id"))
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
	mux.HandleFunc("GET /{id}/subagents", func(w http.ResponseWriter, r *http.Request) {
		s.handleGetSubagents(w, r, r.PathValue("id"))
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
	var (
		messages     []generated.Message
		conversation generated.Conversation
	)
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		messages, err = q.ListMessages(ctx, conversationID)
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
	json.NewEncoder(w).Encode(StreamResponse{
		Messages:     apiMessages,
		Conversation: conversation,
		// ConversationState is sent via the streaming endpoint, not on initial load
		ContextWindowSize: calculateContextWindowSize(apiMessages),
	})
}

// ChatRequest represents a chat message from the user
type ChatRequest struct {
	Message string `json:"message"`
	Model   string `json:"model,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
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
		modelID = s.defaultModel
	}

	llmService, err := s.llmManager.GetService(modelID)
	if err != nil {
		s.logger.Error("Unsupported model requested", "model", modelID, "error", err)
		http.Error(w, fmt.Sprintf("Unsupported model: %s", modelID), http.StatusBadRequest)
		return
	}

	// Get or create conversation manager
	manager, err := s.getOrCreateConversationManager(ctx, conversationID)
	if errors.Is(err, errConversationModelMismatch) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
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
		// Default to GPT-OSS 20B on Fireworks
		modelID = "gpt-oss-20b-fireworks"
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
	conversation, err := s.db.CreateConversation(ctx, nil, true, cwdPtr, &modelID)
	if err != nil {
		s.logger.Error("Failed to create conversation", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	conversationID := conversation.ConversationID

	// Notify conversation list subscribers about the new conversation
	go s.publishConversationListUpdate(ConversationListUpdate{
		Type:         "update",
		Conversation: conversation,
	})

	// Get or create conversation manager
	manager, err := s.getOrCreateConversationManager(ctx, conversationID)
	if errors.Is(err, errConversationModelMismatch) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "accepted",
		"conversation_id": conversationID,
	})
}

// ContinueConversationRequest represents the request to continue a conversation in a new one
type ContinueConversationRequest struct {
	SourceConversationID string `json:"source_conversation_id"`
	Model                string `json:"model,omitempty"`
	Cwd                  string `json:"cwd,omitempty"`
}

// handleContinueConversation handles POST /api/conversations/continue
// Creates a new conversation with a summary of the source conversation as the initial user message,
// but does NOT start the agent. The user can then add additional instructions before sending.
func (s *Server) handleContinueConversation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Parse request
	var req ContinueConversationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.SourceConversationID == "" {
		http.Error(w, "source_conversation_id is required", http.StatusBadRequest)
		return
	}

	// Get source conversation
	sourceConv, err := s.db.GetConversationByID(ctx, req.SourceConversationID)
	if err != nil {
		s.logger.Error("Failed to get source conversation", "conversationID", req.SourceConversationID, "error", err)
		http.Error(w, "Source conversation not found", http.StatusNotFound)
		return
	}

	// Get messages from source conversation
	messages, err := s.db.ListMessages(ctx, req.SourceConversationID)
	if err != nil {
		s.logger.Error("Failed to get messages", "conversationID", req.SourceConversationID, "error", err)
		http.Error(w, "Failed to get messages", http.StatusInternalServerError)
		return
	}

	// Build summary message
	sourceSlug := "unknown"
	if sourceConv.Slug != nil {
		sourceSlug = *sourceConv.Slug
	}
	summary := buildConversationSummary(sourceSlug, messages)

	// Determine model to use
	modelID := req.Model
	if modelID == "" && sourceConv.Model != nil {
		modelID = *sourceConv.Model
	}
	if modelID == "" {
		modelID = "gpt-oss-20b-fireworks"
	}

	// Create new conversation with cwd from request or source conversation
	var cwdPtr *string
	if req.Cwd != "" {
		cwdPtr = &req.Cwd
	} else if sourceConv.Cwd != nil {
		cwdPtr = sourceConv.Cwd
	}
	conversation, err := s.db.CreateConversation(ctx, nil, true, cwdPtr, &modelID)
	if err != nil {
		s.logger.Error("Failed to create conversation", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	conversationID := conversation.ConversationID

	// Notify conversation list subscribers about the new conversation
	go s.publishConversationListUpdate(ConversationListUpdate{
		Type:         "update",
		Conversation: conversation,
	})

	// Create and record the user message with the summary, but do NOT start the agent loop.
	// This allows the user to see the summary and add additional instructions before sending.
	userMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: summary},
		},
	}

	if err := s.recordMessage(ctx, conversationID, userMessage, llm.Usage{}); err != nil {
		s.logger.Error("Failed to record summary message", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Generate slug for the new conversation in background
	ctxNoCancel := context.WithoutCancel(ctx)
	go func() {
		slugCtx, cancel := context.WithTimeout(ctxNoCancel, 15*time.Second)
		defer cancel()
		_, err := slug.GenerateSlug(slugCtx, s.llmManager, s.db, s.logger, conversationID, summary, modelID)
		if err != nil {
			s.logger.Warn("Failed to generate slug for conversation", "conversationID", conversationID, "error", err)
		} else {
			go s.notifySubscribers(ctxNoCancel, conversationID)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "created",
		"conversation_id": conversationID,
	})
}

// buildConversationSummary creates a summary of messages from a conversation
// for use as the initial prompt in a continuation conversation
func buildConversationSummary(slug string, messages []generated.Message) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Continue the conversation with slug %q. Here are the user and agent messages so far (including tool inputs up to ~250 characters and tool outputs up to ~250 characters); use sqlite to look up additional details.\n\n", slug))

	for _, msg := range messages {
		if msg.Type != string(db.MessageTypeUser) && msg.Type != string(db.MessageTypeAgent) {
			continue
		}

		if msg.LlmData == nil {
			continue
		}

		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			continue
		}

		var role string
		if msg.Type == string(db.MessageTypeUser) {
			role = "User"
		} else {
			role = "Agent"
		}

		for _, content := range llmMsg.Content {
			switch content.Type {
			case llm.ContentTypeText:
				if content.Text != "" {
					sb.WriteString(fmt.Sprintf("%s: %s\n\n", role, content.Text))
				}
			case llm.ContentTypeToolUse:
				inputStr := string(content.ToolInput)
				if len(inputStr) > 250 {
					inputStr = inputStr[:250] + "..."
				}
				sb.WriteString(fmt.Sprintf("%s: [Tool: %s] %s\n\n", role, content.ToolName, inputStr))
			case llm.ContentTypeToolResult:
				// Get the text content from tool result
				var resultText string
				for _, res := range content.ToolResult {
					if res.Type == llm.ContentTypeText && res.Text != "" {
						resultText = res.Text
						break
					}
				}
				if len(resultText) > 250 {
					resultText = resultText[:250] + "..."
				}
				if resultText != "" {
					errStr := ""
					if content.ToolError {
						errStr = " (error)"
					}
					sb.WriteString(fmt.Sprintf("%s: [Tool Result%s] %s\n\n", role, errStr, resultText))
				}
			case llm.ContentTypeThinking:
				// Skip thinking blocks - they're internal
			}
		}
	}

	return sb.String()
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

// handleStreamConversation handles GET /conversation/<id>/stream
// Query parameters:
//   - last_sequence_id: Resume from this sequence ID (skip messages up to and including this ID)
func (s *Server) handleStreamConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Parse last_sequence_id for resuming streams
	lastSeqID := int64(-1)
	if lastSeqStr := r.URL.Query().Get("last_sequence_id"); lastSeqStr != "" {
		if parsed, err := strconv.ParseInt(lastSeqStr, 10, 64); err == nil {
			lastSeqID = parsed
		}
	}

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// For fresh connections, get messages BEFORE calling getOrCreateConversationManager.
	// This is important because getOrCreateConversationManager may create a system prompt
	// message during hydration, and we want to return the messages as they were before.
	var messages []generated.Message
	var conversation generated.Conversation
	resuming := lastSeqID >= 0
	if lastSeqID < 0 {
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
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		// Update lastSeqID based on messages we're sending
		if len(messages) > 0 {
			lastSeqID = messages[len(messages)-1].SequenceID
		}
	} else {
		// Resuming - fetch any messages we missed while disconnected
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
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		// Update lastSeqID so the subscription starts after these messages
		if len(messages) > 0 {
			lastSeqID = messages[len(messages)-1].SequenceID
		}
	}

	// Get or create conversation manager to access working state
	manager, err := s.getOrCreateConversationManager(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Send initial response (all messages for fresh connections, missed messages for resumes)
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
			Messages:     apiMessages,
			Conversation: conversation,
			ConversationState: &ConversationState{
				ConversationID: conversationID,
				Working:        manager.IsAgentWorking(),
				Model:          manager.GetModel(),
			},
			ContextWindowSize: ctxSize,
		}
		data, _ := json.Marshal(streamData)
		fmt.Fprintf(w, "data: %s\n\n", data)
		w.(http.Flusher).Flush()
	} else {
		// Either resuming or no messages yet - send current state as heartbeat
		streamData := StreamResponse{
			Conversation: conversation,
			ConversationState: &ConversationState{
				ConversationID: conversationID,
				Working:        manager.IsAgentWorking(),
				Model:          manager.GetModel(),
			},
			Heartbeat: true,
		}
		data, _ := json.Marshal(streamData)
		fmt.Fprintf(w, "data: %s\n\n", data)
		w.(http.Flusher).Flush()
	}

	// Subscribe to new messages after the last one we sent
	next := manager.subpub.Subscribe(ctx, lastSeqID)

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
					Conversation: conv,
					ConversationState: &ConversationState{
						ConversationID: conversationID,
						Working:        manager.IsAgentWorking(),
						Model:          manager.GetModel(),
					},
					Heartbeat: true,
				}
				manager.subpub.Broadcast(heartbeat)
			}
		}
	}()
	defer close(heartbeatDone)

	for {
		streamData, cont := next()
		if !cont {
			break
		}
		// Always forward updates, even if only the conversation changed (e.g., slug added)
		data, _ := json.Marshal(streamData)
		fmt.Fprintf(w, "data: %s\n\n", data)
		w.(http.Flusher).Flush()
	}
}

// handleVersion returns version information as JSON
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(version.GetInfo())
}

// ModelInfo represents a model in the API response
type ModelInfo struct {
	ID               string `json:"id"`
	DisplayName      string `json:"display_name,omitempty"`
	Source           string `json:"source,omitempty"` // Human-readable source (e.g., "exe.dev gateway", "$ANTHROPIC_API_KEY")
	Ready            bool   `json:"ready"`
	MaxContextTokens int    `json:"max_context_tokens,omitempty"`
}

// getModelList returns the list of available models
func (s *Server) getModelList() []ModelInfo {
	modelList := []ModelInfo{}
	if s.predictableOnly {
		modelList = append(modelList, ModelInfo{ID: "predictable", Ready: true, MaxContextTokens: 200000})
	} else {
		modelIDs := s.llmManager.GetAvailableModels()
		for _, id := range modelIDs {
			// Skip predictable model unless predictable-only flag is set
			if id == "predictable" {
				continue
			}
			svc, err := s.llmManager.GetService(id)
			maxCtx := 0
			if err == nil && svc != nil {
				maxCtx = svc.TokenContextWindow()
			}
			info := ModelInfo{ID: id, Ready: err == nil, MaxContextTokens: maxCtx}
			// Add display name and source from model info
			if modelInfo := s.llmManager.GetModelInfo(id); modelInfo != nil {
				info.DisplayName = modelInfo.DisplayName
				info.Source = modelInfo.Source
			}
			modelList = append(modelList, info)
		}
	}
	return modelList
}

// handleModels returns the list of available models
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.getModelList())
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
