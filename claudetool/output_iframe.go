package claudetool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"shelley.exe.dev/llm"
)

// OutputIframeTool displays sandboxed HTML content to the user.
// It requires a MutableWorkingDir to resolve relative file paths.
type OutputIframeTool struct {
	WorkingDir *MutableWorkingDir
}

func (t *OutputIframeTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        outputIframeName,
		Description: outputIframeDescription,
		InputSchema: llm.MustSchema(outputIframeInputSchema),
		Run:         llm.RunJSON(t.run),
	}
}

const (
	outputIframeName        = "output_iframe"
	outputIframeDescription = `Display an HTML file as a sandboxed, script-enabled iframe in the chat.
Use for charts, HTML demos, SVGs, and interactive widgets. Use normal messages
for text or simple data.

HTML may include inline scripts/styles and CDN resources. Bundle small local
assets with files; use libraries for hosted runtimes such as Excalidraw.

To change a displayed iframe, patch the file that holds the content (the HTML
you wrote, or the bundled data file when the HTML is a shared renderer) rather
than rewriting it, then call output_iframe again with the same path and files.
The UI shows it as the next version. Never edit a shared renderer or skill
template to change one output.`

	outputIframeInputSchema = `
{
  "type": "object",
  "required": ["path"],
  "properties": {
    "path": {
      "type": "string",
      "description": "Path to the HTML file to display. Relative paths are resolved from the working directory."
    },
    "title": {
      "type": "string", 
      "description": "Optional title describing the visualization"
    },
    "files": {
      "type": "object",
      "description": "Additional small files to bundle (e.g., data.json, styles.css). Keys are the names to use in the HTML, values are file paths. CSS files are injected as <style> tags; everything else (JSON, CSV, JS, text) is surfaced as a raw string at window.__FILES__['filename']. These files are stored in the conversation — keep them small.",
      "additionalProperties": {
        "type": "string"
      }
    },
    "libraries": {
      "type": "array",
      "description": "Names of shelley-hosted runtime libraries to load. The host page fetches each library and streams it into the iframe via postMessage; the bytes do NOT go into the conversation. The page can await window.__LIBS__ to get a {name: module} map. Allowed: \"excalidraw\".",
      "items": { "type": "string" }
    }
  }
}
`
)

// allowedLibraries maps library names (as the agent specifies them) to the
// /static/ path the host React component fetches. Adding a new entry is the
// only change needed to expose another runtime to output_iframe skills.
var allowedLibraries = map[string]string{
	"excalidraw": "/static/excalidraw/skill.js",
}

// EmbeddedFile represents a file bundled with the HTML.
type EmbeddedFile struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Type    string `json:"type"` // "json", "css", "js", "text"
}

// OutputIframeDisplay is the data passed to the UI for rendering.
type OutputIframeDisplay struct {
	Type     string         `json:"type"`
	HTML     string         `json:"html"`
	Title    string         `json:"title,omitempty"`
	Filename string         `json:"filename,omitempty"`
	Files    []EmbeddedFile `json:"files,omitempty"`
	// Libraries are names of shelley-hosted /static/ runtimes the iframe
	// should preload. The host page (OutputIframeTool.tsx) fetches each
	// library from same-origin and postMessages it in — so the bytes are
	// not stored in the conversation. Resolved into window.__LIBS__ inside
	// the iframe by an injected bootstrap script.
	Libraries []string `json:"libraries,omitempty"`
}

// detectFileType guesses the file type from the filename.
func detectFileType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".json":
		return "json"
	case ".css":
		return "css"
	case ".js":
		return "js"
	case ".csv":
		return "csv"
	default:
		return "text"
	}
}

// injectFiles modifies the HTML to make bundled files accessible.
// CSS files are injected as <style> tags. Everything else (JSON, CSV, JS,
// text) is surfaced as a raw string under window.__FILES__[name] so the
// page decides how to use it (e.g. JSON.parse, or wrap JS in a Blob and
// import() the resulting blob: URL — the way sandboxed iframes can load
// large modules without tripping cross-origin restrictions).
func injectFiles(html string, files []EmbeddedFile) string {
	if len(files) == 0 {
		return html
	}

	var cssFiles []EmbeddedFile
	var dataFiles []EmbeddedFile

	for _, f := range files {
		switch f.Type {
		case "css":
			cssFiles = append(cssFiles, f)
		default:
			// JS, JSON, CSV, text — all surfaced as raw strings under
			// window.__FILES__. The page chooses how to evaluate them
			// (e.g. JSON.parse, or wrap JS in a Blob and import()).
			dataFiles = append(dataFiles, f)
		}
	}

	var injection strings.Builder

	// Inject CSS files as style tags
	for _, f := range cssFiles {
		injection.WriteString("<style data-file=\"")
		injection.WriteString(f.Name)
		injection.WriteString("\">\n")
		injection.WriteString(f.Content)
		injection.WriteString("\n</style>\n")
	}

	// Inject data files as window.__FILES__
	if len(dataFiles) > 0 {
		injection.WriteString("<script>\nwindow.__FILES__ = window.__FILES__ || {};\n")
		for _, f := range dataFiles {
			// Escape the content for JavaScript string
			escaped := escapeJSString(f.Content)
			injection.WriteString("window.__FILES__[\"")
			injection.WriteString(f.Name)
			injection.WriteString("\"] = \"")
			injection.WriteString(escaped)
			injection.WriteString("\";\n")
		}
		injection.WriteString("</script>\n")
	}

	// Insert after <head> or at the beginning
	injectionStr := injection.String()
	if idx := strings.Index(strings.ToLower(html), "<head>"); idx != -1 {
		return html[:idx+6] + "\n" + injectionStr + html[idx+6:]
	}
	if idx := strings.Index(strings.ToLower(html), "<html>"); idx != -1 {
		return html[:idx+6] + "\n<head>\n" + injectionStr + "</head>\n" + html[idx+6:]
	}
	// No head or html tag, just prepend
	return injectionStr + html
}

// escapeJSString escapes a string for use in a JavaScript string literal
// embedded inside an inline HTML <script> element.
//
// HTML's script-data tokenization is tricky: encountering `<!--` followed by
// `<script` switches the parser into the "script data double escape" state
// where `</script>` is no longer recognized as a close tag until a matching
// `-->` returns to normal. A minified JS bundle that contains the substrings
// `<!--` and `<script` (e.g. for innerHTML or doc-fragment work) will break
// the host page and spill its contents as text after our own `</script>`.
//
// We sidestep both by escaping any `</` to `<\/` and any `<!--` to `<\!--`
// inside the resulting JS string literal — both forms are equivalent at the
// JS-source level but never enter HTML's script-escape modes.
//
// We also escape U+2028/U+2029, which are valid in JSON but unescaped
// terminate JS string literals — a real-world hazard when bundling
// minified third-party JS as a __FILES__ entry.
func escapeJSString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			b.WriteString("\\\\")
		case '"':
			b.WriteString("\\\"")
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		case '<':
			rest := s[i+1:]
			switch {
			case strings.HasPrefix(rest, "/"):
				b.WriteString("<\\/")
				i++
			case strings.HasPrefix(rest, "!--"):
				b.WriteString("<\\!--")
				i += 3
			default:
				b.WriteByte('<')
			}
		case 0xE2:
			// U+2028 LINE SEPARATOR = E2 80 A8, U+2029 PARAGRAPH SEPARATOR = E2 80 A9.
			if i+2 < len(s) && s[i+1] == 0x80 && (s[i+2] == 0xA8 || s[i+2] == 0xA9) {
				if s[i+2] == 0xA8 {
					b.WriteString("\\u2028")
				} else {
					b.WriteString("\\u2029")
				}
				i += 2
			} else {
				b.WriteByte(c)
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

type outputIframeInput struct {
	Path      string            `json:"path"`
	Title     string            `json:"title"`
	Files     map[string]string `json:"files"`
	Libraries []string          `json:"libraries"`
}

func (t *OutputIframeTool) run(ctx context.Context, input outputIframeInput) llm.ToolOut {
	if input.Path == "" {
		return llm.ErrorfToolOut("path is required")
	}

	// Resolve every relative path against one working-directory snapshot.
	wd := t.WorkingDir.Get()
	path := input.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(wd, path)
	}

	// Read the main HTML file
	data, err := os.ReadFile(path)
	if err != nil {
		return llm.ErrorfToolOut("failed to read file: %v", err)
	}
	// The HTML is stored as a JSON string in the conversation and rendered
	// as text/html by the clients. A binary/non-UTF-8 file (an image, a
	// gzip, a compiled binary) can't be valid HTML: JSON marshaling would
	// replace its bad bytes with U+FFFD and the UI would render the garbage
	// as visible mojibake (a real bug report: an image passed to this tool
	// rendered as a screen of replacement characters). Reject it up front
	// with an actionable message so the agent picks an HTML file instead.
	if !utf8.Valid(data) {
		return llm.ErrorfToolOut("file %q is not valid UTF-8 text — output_iframe expects a self-contained HTML file, not a binary file (image, archive, or compiled binary). To show an image, embed it in HTML (e.g. a data: URL or an <img> tag).", input.Path)
	}

	// Read additional files
	var embeddedFiles []EmbeddedFile
	for name, filePath := range input.Files {
		// Resolve relative paths
		if !filepath.IsAbs(filePath) {
			filePath = filepath.Join(wd, filePath)
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			return llm.ErrorfToolOut("failed to read file %q: %v", name, err)
		}
		// Embedded files are surfaced verbatim as JS strings / <style> bodies,
		// so they too must be UTF-8 text (JSON, CSS, JS, CSV). A binary blob
		// here corrupts the same way the main HTML does.
		if !utf8.Valid(content) {
			return llm.ErrorfToolOut("bundled file %q (%s) is not valid UTF-8 text — output_iframe files must be text (JSON, CSS, JS, CSV), not binary. To include an image, inline it as a data: URL in the HTML.", name, input.Files[name])
		}
		embeddedFiles = append(embeddedFiles, EmbeddedFile{
			Name:    name,
			Path:    input.Files[name], // Original path for download
			Content: string(content),
			Type:    detectFileType(name),
		})
	}

	// Inject files into the HTML for iframe display
	html := injectFiles(string(data), embeddedFiles)

	// Validate library names against the allowlist (also drops dupes).
	var libs []string
	seen := map[string]bool{}
	for _, name := range input.Libraries {
		if _, ok := allowedLibraries[name]; !ok {
			return llm.ErrorfToolOut("unknown library %q (allowed: see tool description)", name)
		}
		if !seen[name] {
			seen[name] = true
			libs = append(libs, name)
		}
	}

	display := OutputIframeDisplay{
		Type:      "output_iframe",
		HTML:      html,
		Title:     input.Title,
		Filename:  filepath.Base(input.Path),
		Files:     embeddedFiles,
		Libraries: libs,
	}

	return llm.ToolOut{
		LLMContent: llm.TextContent("displayed"),
		Display:    display,
	}
}
