package server

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	gmtext "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"shelley.exe.dev/db"
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
//     itself surfaced in this specific message), and
//   - the file sniffs as an image.
//
// Those two together are the whole boundary, and the first is the load-bearing
// one. There is deliberately no workspace-containment check: screenshots live
// in /tmp/shelley-screenshots, outside every workspace, and models routinely
// surface them as ![shot](/tmp/shelley-screenshots/<uuid>.png). Judging paths
// against the conversation's directory rejected every one of those, which is
// the bug this shape fixes. Confining it further would not buy security
// anyway: the agent runs with the user's own privileges and can read these
// files directly, so a path it already printed in its own transcript is not a
// secret this endpoint can keep.
//
// Remote (http/https) and data: URIs are handled entirely on the frontend and
// never reach this endpoint.
func (s *Server) handleMessageFile(w http.ResponseWriter, r *http.Request) {
	reqPath, msg, convID, ok := s.messageFileRequest(w, r)
	if !ok {
		return
	}
	// Capability check: the message must actually reference this path.
	if !messageReferencesPath(messageTextContent(msg), reqPath) {
		http.Error(w, "path not referenced by message", http.StatusNotFound)
		return
	}
	f, ok := s.openMessageFile(w, r, convID, reqPath)
	if !ok {
		return
	}
	defer f.Close()

	// Sniff the content and require an image.
	head := make([]byte, sniffLen)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		http.Error(w, "read failed", http.StatusBadGateway)
		return
	}
	contentType, ok := servableImageType(head[:n], f.name)
	if !ok {
		http.Error(w, "file is not an image", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", contentType)
	// SVGs can carry scripts; prevent them from executing if loaded directly
	// (they are referenced via <img>, which never runs scripts, but a direct
	// navigation to this URL would otherwise render active content).
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Unlike DB-embedded images, on-disk files change as the agent works:
	// revalidate every time, so a rewritten chart never shows stale.
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Last-Modified", f.modTime.UTC().Format(http.TimeFormat))
	w.Write(head[:n])
	io.Copy(w, f)
}

// sandboxScheme prefixes download links: [name](sandbox:/path/to/file). It is
// ChatGPT's convention for files in its sandbox, which models use unprompted,
// so Shelley adopts it (see system_prompt.txt) rather than fighting it.
const sandboxScheme = "sandbox:"

// urlScheme matches a destination that starts with a URI scheme.
var urlScheme = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)

// linkedFilePath returns the file a link destination names: the path of a
// sandbox: link, or the destination itself when it is a local path, absolute
// or relative, rather than a URL, a fragment, or one of Shelley's own pages.
// Models link files they made either way. Mirrors linkedFilePath in
// ui/src/utils/markdownRender.ts.
func linkedFilePath(dest string) (string, bool) {
	if p, ok := strings.CutPrefix(dest, sandboxScheme); ok {
		return p, true
	}
	for _, prefix := range []string{"#", "?", "//", "/c/", "/api/"} {
		if strings.HasPrefix(dest, prefix) {
			return "", false
		}
	}
	if dest == "" || urlScheme.MatchString(dest) {
		return "", false
	}
	return dest, true
}

// handleMessageDownload serves a local file linked from a message's markdown,
// as [name](sandbox:<path>) or [name](<path>), as an attachment of any type.
// Route: GET /api/message/{message_id}/download?path=<path>
//
// path is the link's file path (see linkedFilePath), exactly as a markdown
// parser yields it. Unlike handleMessageFile there is no content check, so the
// capability is stricter: path must be the file of a link in the message. A
// path the message merely mentions, shows as an image, or that is a prefix of
// a link is not an offer to download it. The destination is a URI, so it is
// percent-decoded to name the file. Serving as an attachment keeps the bytes
// from ever rendering on Shelley's origin.
func (s *Server) handleMessageDownload(w http.ResponseWriter, r *http.Request) {
	reqPath, msg, convID, ok := s.messageFileRequest(w, r)
	if !ok {
		return
	}
	if !slices.Contains(linkedFilePaths(msg), reqPath) {
		http.Error(w, "path not linked by message", http.StatusNotFound)
		return
	}
	path, err := url.PathUnescape(reqPath)
	if err != nil {
		http.Error(w, "bad path encoding", http.StatusBadRequest)
		return
	}
	f, ok := s.openMessageFile(w, r, convID, path)
	if !ok {
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.name}))
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Content-Length", strconv.FormatInt(f.size, 10))
	w.Header().Set("Last-Modified", f.modTime.UTC().Format(http.TimeFormat))
	io.Copy(w, f)
}

// linkedFilePaths returns the file paths (see linkedFilePath) of the links
// and autolinks in m, read as the UI renders it: each run of adjacent text
// blocks is one GFM document, and an image's alt text holds no links.
// Backslash escapes are resolved, as the UI's markdown parser does;
// percent-encoding is kept.
func linkedFilePaths(m *llm.Message) []string {
	md := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()
	var paths []string
	for _, run := range textRuns(m) {
		src := []byte(run)
		ast.Walk(md.Parse(gmtext.NewReader(src)), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			var dest string
			switch n := n.(type) {
			case *ast.Image:
				return ast.WalkSkipChildren, nil
			case *ast.Link:
				dest = string(util.UnescapePunctuations(n.Destination))
			case *ast.AutoLink:
				dest = string(n.URL(src))
			}
			if p, ok := linkedFilePath(dest); ok {
				paths = append(paths, p)
			}
			return ast.WalkContinue, nil
		})
	}
	return paths
}

// textRuns joins each run of adjacent text blocks in m, as the UI does
// (coalesceContent) before rendering each run as markdown.
func textRuns(m *llm.Message) []string {
	runs := []string{""}
	for _, c := range m.Content {
		switch {
		case c.Type == llm.ContentTypeText:
			runs[len(runs)-1] += c.Text
		case runs[len(runs)-1] != "":
			runs = append(runs, "")
		}
	}
	return runs
}

// messageFileRequest returns the ?path= of a per-message file request, and the
// route's message and its conversation. On failure it writes the error
// response.
func (s *Server) messageFileRequest(w http.ResponseWriter, r *http.Request) (reqPath string, m *llm.Message, conversationID string, ok bool) {
	reqPath = r.URL.Query().Get("path")
	if reqPath == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return "", nil, "", false
	}
	msg, err := s.db.GetMessageByID(r.Context(), r.PathValue("message_id"))
	if err != nil {
		http.Error(w, "message not found", http.StatusNotFound)
		return "", nil, "", false
	}
	if msg.LlmData == nil {
		http.Error(w, "message has no llm_data", http.StatusNotFound)
		return "", nil, "", false
	}
	m = new(llm.Message)
	if err := json.Unmarshal([]byte(*msg.LlmData), m); err != nil {
		http.Error(w, "failed to parse message data", http.StatusInternalServerError)
		return "", nil, "", false
	}
	return reqPath, m, msg.ConversationID, true
}

// messageFile is a file a message links, open on the machine that has it.
type messageFile struct {
	io.ReadCloser
	name    string
	size    int64
	modTime time.Time
}

// openMessageFile opens the regular file at path for a message of
// conversationID. A VM conversation works on two machines and a path does not
// say which, so the file is this host's, where its tools and screenshots are
// and its relative paths resolve against its scratch directory, or if this
// host has none, the VM's, relative to the conversation's directory there. On
// failure it writes the error response.
func (s *Server) openMessageFile(w http.ResponseWriter, r *http.Request, conversationID, path string) (*messageFile, bool) {
	cwd := s.conversationCwd(r.Context(), conversationID)
	hostCwd := cwd
	vm, vmErr := s.db.GetConversationVM(r.Context(), conversationID)
	if vmErr == nil {
		// The conversation's cwd is on the VM.
		if hostCwd, vmErr = s.vmScratchDir(vm.ConversationID); vmErr != nil {
			http.Error(w, vmErr.Error(), http.StatusInternalServerError)
			return nil, false
		}
	}
	f, fi, err := openHostFile(hostCwd, path)
	if err == nil {
		return &messageFile{f, fi.Name(), fi.Size(), fi.ModTime()}, true
	}
	if errors.Is(err, fs.ErrNotExist) {
		switch {
		case vmErr == nil:
			var vf *messageFile
			if vf, err = s.openVMFile(r.Context(), vm, cwd, path); err == nil {
				return vf, true
			}
		case !errors.Is(vmErr, sql.ErrNoRows):
			err = vmErr
		}
	}
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrPermission):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, errFileTooLarge):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
	return nil, false
}

// openHostFile opens the regular file at path on this host, resolving a
// relative path against cwd.
func openHostFile(cwd, path string) (*os.File, fs.FileInfo, error) {
	if !filepath.IsAbs(path) {
		// A relative path is relative to where the agent was working.
		if cwd == "" {
			return nil, nil, fmt.Errorf("%w: relative path %q, and the conversation has no working directory", fs.ErrNotExist, path)
		}
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)

	// Check the type before opening: opening a FIFO blocks until a writer
	// appears, which would hang this request goroutine, and a path can now name
	// anything on the machine. Nothing we want to serve is a non-regular file.
	fi, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%w: %s is not a regular file", fs.ErrNotExist, path)
	}
	f, err := os.Open(path)
	return f, fi, err
}

// maxVMFileSize bounds a file served from a conversation's VM.
const maxVMFileSize = 256 << 20

// vmFileTimeout bounds reaching a VM and starting a file's transfer.
const vmFileTimeout = 30 * time.Second

var errFileTooLarge = fmt.Errorf("file is larger than %d MiB", maxVMFileSize>>20)

// vmFileScript prints regular file $1's size and modification time (Unix
// seconds) on a line, then its contents. It exits 44 if there is no such
// regular file, and 45 if it is larger than $2 bytes.
const vmFileScript = `[ -f "$1" ] || exit 44
set -- "$1" "$2" $(stat -Lc '%s %Y' -- "$1" 2>/dev/null || stat -Lf '%z %m' -- "$1")
[ $# -eq 4 ] || exit 1
[ "$3" -le "$2" ] || exit 45
echo "$3 $4"
exec cat -- "$1"`

// openVMFile streams the regular file at path from a conversation's VM,
// resolving a relative path against dir there (the conversation's cwd), or
// with none, the VM's directory. It never provisions
// or connects the VM: viewing a message must not change it, and the next tool
// call does that.
func (s *Server) openVMFile(ctx context.Context, vm *db.ConversationVM, dir, p string) (*messageFile, error) {
	if err := conversationVMArchived(vm); err != nil {
		return nil, err
	}
	if vm.Status != db.ConversationVMReady {
		return nil, fmt.Errorf("VM %s is not ready (%s)", vm.Name, vm.Status)
	}
	if !path.IsAbs(p) {
		if dir == "" {
			dir = vm.Dir
		}
		p = path.Join(dir, p)
	}
	target, err := s.conversationVMTarget(vm)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(vmFileTimeout, cancel)
	argv := target.Command(vmFileScript, p, strconv.Itoa(maxVMFileSize))
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		cancel()
		return nil, err
	}
	out := bufio.NewReader(stdout)
	header, err := out.ReadString('\n')
	var size, mtime int64
	if err == nil {
		_, err = fmt.Sscanf(header, "%d %d\n", &size, &mtime)
	}
	if err != nil {
		cancel()
		waitErr := cmd.Wait()
		var exitErr *exec.ExitError
		switch {
		case errors.As(waitErr, &exitErr) && exitErr.ExitCode() == 44:
			return nil, fmt.Errorf("%w: %s on VM %s", fs.ErrNotExist, p, vm.Name)
		case errors.As(waitErr, &exitErr) && exitErr.ExitCode() == 45:
			return nil, errFileTooLarge
		case waitErr != nil:
			err = waitErr
		}
		return nil, fmt.Errorf("read %s on VM %s: %w: %s", p, vm.Name, err, strings.TrimSpace(stderr.String()))
	}
	timer.Stop()
	return &messageFile{
		ReadCloser: vmFileReader{io.LimitReader(out, size), cmd, cancel},
		name:       path.Base(p),
		size:       size,
		modTime:    time.Unix(mtime, 0),
	}, nil
}

// vmFileReader reads a file's contents from the ssh command sending them.
type vmFileReader struct {
	io.Reader
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

func (v vmFileReader) Close() error {
	v.cancel()
	v.cmd.Wait()
	return nil
}

// conversationCwd returns the working directory recorded for a conversation,
// or "" when none is recorded. We deliberately do not fall back to the server
// process's own working directory: without an explicit conversation workspace
// there is no safe boundary to scope file serving to, so the caller treats an
// empty result as "deny".
func (s *Server) conversationCwd(ctx context.Context, conversationID string) string {
	conv, err := s.db.GetConversationByID(ctx, conversationID)
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

// sniffLen is how much of a file we read to identify it. http.DetectContentType
// looks at 512 bytes; SVG needs a little more, since its root element can sit
// behind an XML declaration, a DOCTYPE, and a comment.
const sniffLen = 1024

// servableImageType reports whether head is the start of an image we are
// willing to serve inline, returning the Content-Type to use.
//
// It must actually confirm the content. This endpoint reaches any path the
// model named, so "is an image" is the only thing separating it from a general
// file reader, and a check that trusted the name would not be a check: any
// secret could be served by pointing at it with the right extension.
func servableImageType(head []byte, path string) (string, bool) {
	if sniffed := http.DetectContentType(head); strings.HasPrefix(sniffed, "image/") {
		return sniffed, true
	}
	// http.DetectContentType reports SVG as text/xml or text/plain, so it can
	// only be recognized by its root element. Require both that and the
	// extension: the extension alone would serve any text file, and the element
	// alone would serve an HTML page that happens to embed inline SVG.
	if strings.EqualFold(filepath.Ext(path), ".svg") && bytes.Contains(head, []byte("<svg")) {
		return "image/svg+xml", true
	}
	return "", false
}
