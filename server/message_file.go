package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"shelley.exe.dev/llm"
)

// handleMessageFile serves a local image file referenced inline in a message's
// markdown text. Route: GET /api/message/{message_id}/file?path=<path>
//
// This is the on-disk counterpart to handleMessageImage (which serves base64
// images embedded in llm_data). Some models surface images by emitting markdown
// like ![chart](./out/chart.png) in their response text rather than by
// returning a base64 image block. The frontend rewrites such local-path images
// to point at this endpoint; this handler serves the bytes, but only when:
//
//   - the requested path is actually referenced in the message's text (a
//     capability check: we never serve arbitrary files, only ones the model
//     itself surfaced in this specific message),
//   - the resolved path stays within the conversation's working directory
//     (no path traversal outside the workspace), and
//   - the file sniffs as an image.
//
// Remote (http/https) and data: URIs are handled entirely on the frontend and
// never reach this endpoint.
func (s *Server) handleMessageFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	messageID := r.PathValue("message_id")
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	msg, err := s.db.GetMessageByID(r.Context(), messageID)
	if err != nil {
		http.Error(w, "message not found", http.StatusNotFound)
		return
	}
	if msg.LlmData == nil {
		http.Error(w, "message has no llm_data", http.StatusNotFound)
		return
	}

	var llmMsg llm.Message
	if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
		http.Error(w, "failed to parse message data", http.StatusInternalServerError)
		return
	}

	// Capability check: the message must actually reference this path.
	if !messageReferencesPath(messageTextContent(&llmMsg), reqPath) {
		http.Error(w, "path not referenced by message", http.StatusNotFound)
		return
	}

	// Resolve the requested path against the conversation's working directory
	// and confirm it stays inside that directory.
	cwd := s.conversationCwd(r, msg.ConversationID)
	if cwd == "" {
		http.Error(w, "conversation working directory unknown", http.StatusNotFound)
		return
	}
	resolved, ok := resolveWithinDir(cwd, reqPath)
	if !ok {
		http.Error(w, "path escapes working directory", http.StatusForbidden)
		return
	}

	f, err := os.Open(resolved)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Sniff the content and require an image.
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	contentType, ok := servableImageType(http.DetectContentType(buf[:n]), resolved)
	if !ok {
		http.Error(w, "file is not an image", http.StatusForbidden)
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "seek failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	// SVGs can carry scripts; prevent them from executing if loaded directly
	// (they are referenced via <img>, which never runs scripts, but a direct
	// navigation to this URL would otherwise render active content).
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Short-term caching: unlike DB-embedded images, on-disk files can change
	// during a session, so don't cache aggressively.
	w.Header().Set("Cache-Control", "private, max-age=60")
	io.Copy(w, f)
}

// conversationCwd returns the working directory recorded for a conversation,
// or "" when none is recorded. We deliberately do not fall back to the server
// process's own working directory: without an explicit conversation workspace
// there is no safe boundary to scope file serving to, so the caller treats an
// empty result as "deny".
func (s *Server) conversationCwd(r *http.Request, conversationID string) string {
	conv, err := s.db.GetConversationByID(r.Context(), conversationID)
	if err == nil && conv != nil && conv.Cwd != nil && *conv.Cwd != "" {
		return *conv.Cwd
	}
	return ""
}

// messageTextContent concatenates the text portions of a message. SVG images
// referenced in markdown live in the text content blocks of the assistant
// message.
func messageTextContent(m *llm.Message) string {
	var b strings.Builder
	for i := range m.Content {
		c := &m.Content[i]
		if c.Text != "" {
			b.WriteString(c.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// messageReferencesPath reports whether text references reqPath as a whole path
// token. A plain substring match would be unsafe in both directions:
//   - suffix: ?path=cret.png must not match a message mentioning secret.png;
//   - prefix: ?path=./out/report must not match ./out/report-2024.png.
//
// So we require the match to be delimited by path boundaries on both sides
// (start/end of string, or a character that cannot be part of a file path such
// as whitespace, quotes, or markdown's surrounding parens/brackets).
func messageReferencesPath(text, reqPath string) bool {
	if reqPath == "" {
		return false
	}
	from := 0
	for {
		idx := strings.Index(text[from:], reqPath)
		if idx < 0 {
			return false
		}
		abs := from + idx
		end := abs + len(reqPath)
		leftOK := abs == 0 || !isPathChar(text[abs-1])
		rightOK := end == len(text) || !isPathChar(text[end])
		if leftOK && rightOK {
			return true
		}
		from = abs + 1
	}
}

// isPathChar reports whether c can appear within a file-path token. Anything
// else (whitespace, quotes, parens, brackets, etc.) delimits a path. We treat
// the match's neighbors against this set so a reference only authorizes the
// exact whole path the model surfaced, not a prefix or suffix of it.
func isPathChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '.', '_', '-', '/', '~', '+', '@', '%':
		return true
	}
	return false
}

// resolveWithinDir resolves reqPath (absolute or relative to dir) and returns
// the cleaned absolute path together with whether it stays within dir. It
// first performs a lexical containment check, then resolves symlinks on both
// the directory and the path and re-checks, so a symlink inside dir cannot be
// used to escape the workspace boundary.
func resolveWithinDir(dir, reqPath string) (string, bool) {
	var abs string
	if filepath.IsAbs(reqPath) {
		abs = filepath.Clean(reqPath)
	} else {
		abs = filepath.Clean(filepath.Join(dir, reqPath))
	}
	cleanDir := filepath.Clean(dir)
	if !lexicallyWithin(abs, cleanDir) {
		return abs, false
	}

	// Defense in depth: resolve symlinks and confirm the real path is still
	// inside the real working directory. EvalSymlinks fails for not-yet-
	// existing paths; a missing file is handled (404) by the caller's Open, so
	// treat an unresolvable path as lexically-contained-only.
	realDir, err := filepath.EvalSymlinks(cleanDir)
	if err != nil {
		return abs, true
	}
	realAbs, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs, true
	}
	if !lexicallyWithin(realAbs, realDir) {
		return abs, false
	}
	return abs, true
}

// lexicallyWithin reports whether abs is a path strictly inside dir (not dir
// itself).
func lexicallyWithin(abs, dir string) bool {
	if abs == dir {
		return false
	}
	return strings.HasPrefix(abs, dir+string(os.PathSeparator))
}

// servableImageType reports whether the sniffed content type (or, for SVG, the
// file extension) is an image we are willing to serve inline, returning the
// Content-Type to use.
func servableImageType(sniffed, path string) (string, bool) {
	if strings.HasPrefix(sniffed, "image/") {
		return sniffed, true
	}
	// http.DetectContentType reports SVG as text/xml or text/plain; trust the
	// extension for it and serve with the correct image type.
	if strings.EqualFold(filepath.Ext(path), ".svg") {
		return "image/svg+xml", true
	}
	return "", false
}
