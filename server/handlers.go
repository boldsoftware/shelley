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
	"shelley.exe.dev/db/generated"
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

// acceptsGzip returns true if the client accepts gzip encoding
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

// generateFaviconSVG creates a seashell favicon with color based on hostname hash
func generateFaviconSVG(hostname string) string {
	hash := hashString(hostname)
	h := hash % 360
	s := 55
	l := 65
	lightL := l + 15
	if lightL > 90 {
		lightL = 90
	}
	darkL := l - 15
	if darkL < 40 {
		darkL = 40
	}
	strokeL := darkL - 15
	if strokeL < 25 {
		strokeL = 25
	}

	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">
  <defs>
    <linearGradient id="shellGrad" x1="0%%" y1="0%%" x2="100%%" y2="100%%">
      <stop offset="0%%" style="stop-color:hsl(%d, %d%%, %d%%)"/>
      <stop offset="50%%" style="stop-color:hsl(%d, %d%%, %d%%)"/>
      <stop offset="100%%" style="stop-color:hsl(%d, %d%%, %d%%)"/>
    </linearGradient>
  </defs>
  <path d="M16 4 C8 4 3 12 3 20 C3 24 6 28 16 28 C26 28 29 24 29 20 C29 12 24 4 16 4"
        fill="url(#shellGrad)" stroke="hsl(%d, %d%%, %d%%)" stroke-width="1"/>
  <path d="M16 6 L16 26" stroke="hsl(%d, %d%%, %d%%)" stroke-width="1" fill="none"/>
  <path d="M16 6 L8 25" stroke="hsl(%d, %d%%, %d%%)" stroke-width="1" fill="none"/>
  <path d="M16 6 L24 25" stroke="hsl(%d, %d%%, %d%%)" stroke-width="1" fill="none"/>
  <path d="M16 6 L5 22" stroke="hsl(%d, %d%%, %d%%)" stroke-width="1" fill="none"/>
  <path d="M16 6 L27 22" stroke="hsl(%d, %d%%, %d%%)" stroke-width="1" fill="none"/>
  <path d="M16 6 L11 26" stroke="hsl(%d, %d%%, %d%%)" stroke-width="0.8" fill="none"/>
  <path d="M16 6 L21 26" stroke="hsl(%d, %d%%, %d%%)" stroke-width="0.8" fill="none"/>
</svg>`,
		h, s, lightL,
		h, s, l,
		h, s, darkL,
		h, s-10, strokeL,
		h, s-20, darkL,
		h, s-20, darkL,
		h, s-20, darkL,
		h, s-20, darkL,
		h, s-20, darkL,
		h, s-20, darkL,
		h, s-20, darkL,
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
	type ModelInfo struct {
		ID               string `json:"id"`
		Ready            bool   `json:"ready"`
		MaxContextTokens int    `json:"max_context_tokens,omitempty"`
	}

	var modelList []ModelInfo
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
			modelList = append(modelList, ModelInfo{ID: id, Ready: err == nil, MaxContextTokens: maxCtx})
		}
	}

	// Select default model - use configured default if available, otherwise first ready model
	defaultModel := s.defaultModel
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
	result := make([]ConversationWithState, len(conversations))
	for i, conv := range conversations {
		result[i] = ConversationWithState{
			Conversation: conv,
			Working:      workingStates[conv.ConversationID],
		}
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
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Conversation not found", http.StatusNotFound)
			return
		}
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
	if err != nil {
		if errors.Is(err, errConversationModelMismatch) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
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
	if err != nil {
		if errors.Is(err, errConversationModelMismatch) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
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
		// Default to Qwen3 Coder on Fireworks
		modelID = "qwen3-coder-fireworks"
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
	conversation, err := s.db.CreateConversation(ctx, nil, true, cwdPtr)
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
	if err != nil {
		if errors.Is(err, errConversationModelMismatch) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
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
	if err != nil {
		if errors.Is(err, errConversationModelMismatch) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
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
func (s *Server) handleStreamConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Get current messages and conversation data
	var messages []generated.Message
	var conversation generated.Conversation
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

	// Get or create conversation manager to access working state
	manager, err := s.getOrCreateConversationManager(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
		return
	}

	// Send current messages, conversation data, and conversation state
	apiMessages := toAPIMessages(messages)
	streamData := StreamResponse{
		Messages:     apiMessages,
		Conversation: conversation,
		ConversationState: &ConversationState{
			ConversationID: conversationID,
			Working:        manager.IsAgentWorking(),
		},
		ContextWindowSize: calculateContextWindowSize(apiMessages),
	}
	data, _ := json.Marshal(streamData)
	fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()

	// Subscribe to new messages after the last one we sent
	last := int64(-1)
	if len(messages) > 0 {
		last = messages[len(messages)-1].SequenceID
	}
	next := manager.subpub.Subscribe(ctx, last)
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

// handleDebugLLM serves recent LLM requests and responses for debugging
func (s *Server) handleDebugLLM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if requesting a specific record JSON
	if idx := r.URL.Query().Get("index"); idx != "" {
		var i int
		if _, err := fmt.Sscanf(idx, "%d", &i); err != nil {
			http.Error(w, "Invalid index", http.StatusBadRequest)
			return
		}

		type historyProvider interface {
			GetHistory() *models.LLMRequestHistory
		}

		var records []models.LLMRequestRecord
		if hp, ok := s.llmManager.(historyProvider); ok && hp.GetHistory() != nil {
			records = hp.GetHistory().GetRecords()
		}

		if i < 0 || i >= len(records) {
			http.Error(w, "Index out of range", http.StatusNotFound)
			return
		}

		record := records[i]
		recordType := r.URL.Query().Get("type")

		switch recordType {
		case "request":
			w.Header().Set("Content-Type", "application/json")
			w.Write(record.HTTPRequest)
		case "response":
			w.Header().Set("Content-Type", "application/json")
			w.Write(record.HTTPResponse)
		default:
			// Return the full record
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(record)
		}
		return
	}

	// Get history from the LLM manager if it's a models.Manager
	type historyProvider interface {
		GetHistory() *models.LLMRequestHistory
	}

	var records []models.LLMRequestRecord
	if hp, ok := s.llmManager.(historyProvider); ok && hp.GetHistory() != nil {
		records = hp.GetHistory().GetRecords()
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// Write simple HTML with links to JSON
	fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>LLM Debug - Recent Requests</title>
<style>
body {
	font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
	margin: 20px;
	background: #ffffff;
	color: #000000;
}
h1 {
	margin-bottom: 20px;
}
table {
	border-collapse: collapse;
	width: 100%;
}
th, td {
	padding: 8px 12px;
	text-align: left;
	border-bottom: 1px solid #ddd;
}
th {
	background: #f5f5f5;
	font-weight: 600;
}
tr:hover {
	background: #f9f9f9;
}
.error {
	color: #d32f2f;
}
.success {
	color: #388e3c;
}
a {
	color: #1976d2;
	text-decoration: none;
}
a:hover {
	text-decoration: underline;
}
</style>
</head>
<body>
<h1>LLM Debug - Recent Requests</h1>
`)

	if len(records) == 0 {
		fmt.Fprint(w, "<p>No requests recorded yet.</p>")
	} else {
		fmt.Fprint(w, "<table>")
		fmt.Fprint(w, "<tr><th>#</th><th>Time</th><th>Model</th><th>URL</th><th>Status</th><th>Duration</th><th>Request</th><th>Response</th></tr>")
		for i := len(records) - 1; i >= 0; i-- {
			record := records[i]
			num := len(records) - i
			statusClass := "success"
			statusText := fmt.Sprintf("%d", record.HTTPStatusCode)
			if record.Error != "" {
				statusClass = "error"
				statusText = record.Error
			} else if record.HTTPStatusCode >= 400 {
				statusClass = "error"
			}
			fmt.Fprintf(w, "<tr>")
			fmt.Fprintf(w, "<td>%d</td>", num)
			fmt.Fprintf(w, "<td>%s</td>", record.Timestamp.Format("15:04:05"))
			fmt.Fprintf(w, "<td>%s</td>", record.ModelID)
			fmt.Fprintf(w, "<td>%s</td>", record.URL)
			fmt.Fprintf(w, "<td class=\"%s\">%s</td>", statusClass, statusText)
			fmt.Fprintf(w, "<td>%.2fs</td>", record.Duration)
			fmt.Fprintf(w, "<td><a href=\"/debug/llm?index=%d&type=request\" target=\"_blank\">json</a></td>", i)
			fmt.Fprintf(w, "<td><a href=\"/debug/llm?index=%d&type=response\" target=\"_blank\">json</a></td>", i)
			fmt.Fprintf(w, "</tr>")
		}
		fmt.Fprint(w, "</table>")
	}

	fmt.Fprint(w, `
</body>
</html>
`)
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
