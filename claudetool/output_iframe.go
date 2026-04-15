package claudetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

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
		Run:         t.Run,
	}
}

const (
	outputIframeName        = "output_iframe"
	outputIframeDescription = `Display HTML content to the user in a sandboxed iframe.

Use this tool for visualizations like charts, graphs, and HTML demos that the user should see.
The HTML will be rendered in a secure sandbox with scripts enabled but isolated from the parent page.

Do NOT use this tool for:
- Regular text responses (use normal messages instead)
- File operations (use edit or bash)
- Simple data display (just describe it in text)

Good uses:
- Vega-Lite or other chart library visualizations  
- HTML/CSS demonstrations
- Interactive widgets or mini-apps
- SVG graphics

The HTML should be self-contained. You can include inline <script> and <style> tags.
External resources can be loaded via CDN (e.g., https://cdn.jsdelivr.net/).

For visualizations that need external data files (JSON, CSV, etc.), use the 'files' parameter
to bundle them. They will be injected into the page and accessible via window.__FILES__['filename'].`

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
      "description": "Additional files to bundle (e.g., data.json, styles.css). Keys are the names to use in the HTML, values are file paths. Files are accessible in the HTML via window.__FILES__['filename'] for JSON/text, or injected as style tags for CSS.",
      "additionalProperties": {
        "type": "string"
      }
    }
  }
}
`
)

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
// For JSON/text/csv files: adds a script that populates window.__FILES__
// For CSS files: injects as <style> tags
// For JS files: injects as <script> tags
func injectFiles(html string, files []EmbeddedFile) string {
	if len(files) == 0 {
		return html
	}

	var jsFiles []EmbeddedFile
	var cssFiles []EmbeddedFile
	var dataFiles []EmbeddedFile

	for _, f := range files {
		switch f.Type {
		case "css":
			cssFiles = append(cssFiles, f)
		case "js":
			jsFiles = append(jsFiles, f)
		default:
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

	// Inject JS files as script tags
	for _, f := range jsFiles {
		injection.WriteString("<script data-file=\"")
		injection.WriteString(f.Name)
		injection.WriteString("\">\n")
		injection.WriteString(f.Content)
		injection.WriteString("\n</script>\n")
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

// escapeJSString escapes a string for use in a JavaScript string literal.
func escapeJSString(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
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
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (t *OutputIframeTool) Run(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input struct {
		Path  string            `json:"path"`
		Title string            `json:"title"`
		Files map[string]string `json:"files"`
	}
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorToolOut(err)
	}

	if input.Path == "" {
		return llm.ErrorfToolOut("path is required")
	}

	// Resolve the path relative to working directory
	path := input.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(t.WorkingDir.Get(), path)
	}

	// Read the main HTML file
	data, err := os.ReadFile(path)
	if err != nil {
		return llm.ErrorfToolOut("failed to read file: %v", err)
	}

	// Read additional files
	var embeddedFiles []EmbeddedFile
	for name, filePath := range input.Files {
		// Resolve relative paths
		if !filepath.IsAbs(filePath) {
			filePath = filepath.Join(t.WorkingDir.Get(), filePath)
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			return llm.ErrorfToolOut("failed to read file %q: %v", name, err)
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

	display := OutputIframeDisplay{
		Type:     "output_iframe",
		HTML:     html,
		Title:    input.Title,
		Filename: filepath.Base(input.Path),
		Files:    embeddedFiles,
	}

	return llm.ToolOut{
		LLMContent: llm.TextContent("displayed"),
		Display:    display,
	}
}
