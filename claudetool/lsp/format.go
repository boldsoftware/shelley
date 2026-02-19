package lsp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxReferences = 50

// formatDefinition formats definition locations for display.
func formatDefinition(locations []Location, wd string) string {
	if len(locations) == 0 {
		return "No definition found."
	}

	var sb strings.Builder
	for i, loc := range locations {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		path := filePathFromURI(loc.URI)
		relPath := relativePath(path, wd)
		line := loc.Range.Start.Line + 1 // convert 0-based to 1-based for display
		sb.WriteString(fmt.Sprintf("**%s:%d**\n", relPath, line))

		// Read source context around the definition
		context := readSourceContext(path, loc.Range.Start.Line, 5)
		if context != "" {
			sb.WriteString("```\n")
			sb.WriteString(context)
			sb.WriteString("\n```")
		}
	}
	return sb.String()
}

// formatReferences formats reference locations for display, grouped by file.
func formatReferences(locations []Location, wd string) string {
	if len(locations) == 0 {
		return "No references found."
	}

	// Group by file
	type fileRef struct {
		path string
		refs []Location
	}
	fileOrder := []string{}
	byFile := make(map[string][]Location)
	for _, loc := range locations {
		path := filePathFromURI(loc.URI)
		if _, exists := byFile[path]; !exists {
			fileOrder = append(fileOrder, path)
		}
		byFile[path] = append(byFile[path], loc)
	}

	var sb strings.Builder
	total := len(locations)
	truncated := total > maxReferences
	if truncated {
		sb.WriteString(fmt.Sprintf("Found %d references (showing first %d):\n\n", total, maxReferences))
	} else {
		sb.WriteString(fmt.Sprintf("Found %d reference(s):\n\n", total))
	}

	shown := 0
	for _, path := range fileOrder {
		if shown >= maxReferences {
			break
		}
		refs := byFile[path]
		relPath := relativePath(path, wd)
		sb.WriteString(fmt.Sprintf("**%s**\n", relPath))
		for _, ref := range refs {
			if shown >= maxReferences {
				break
			}
			line := ref.Range.Start.Line + 1
			context := readSourceLine(path, ref.Range.Start.Line)
			if context != "" {
				sb.WriteString(fmt.Sprintf("  L%d: %s\n", line, strings.TrimSpace(context)))
			} else {
				sb.WriteString(fmt.Sprintf("  L%d\n", line))
			}
			shown++
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// formatHover formats hover information for display.
func formatHover(hover *Hover) string {
	if hover == nil || hover.Contents.Value == "" {
		return "No hover information available."
	}
	return hover.Contents.Value
}

// formatSymbols formats workspace symbol results for display.
func formatSymbols(symbols []SymbolInformation, wd string) string {
	if len(symbols) == 0 {
		return "No symbols found."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d symbol(s):\n\n", len(symbols)))
	for _, sym := range symbols {
		path := filePathFromURI(sym.Location.URI)
		relPath := relativePath(path, wd)
		line := sym.Location.Range.Start.Line + 1
		kind := SymbolKindName(sym.Kind)
		if sym.ContainerName != "" {
			sb.WriteString(fmt.Sprintf("- %s (%s) in %s — %s:%d\n", sym.Name, kind, sym.ContainerName, relPath, line))
		} else {
			sb.WriteString(fmt.Sprintf("- %s (%s) — %s:%d\n", sym.Name, kind, relPath, line))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// readSourceContext reads a few lines around the given 0-based line from a file.
func readSourceContext(path string, line int, contextLines int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	start := line - contextLines
	if start < 0 {
		start = 0
	}
	end := line + contextLines + 1
	if end > len(lines) {
		end = len(lines)
	}
	var sb strings.Builder
	for i := start; i < end; i++ {
		marker := "  "
		if i == line {
			marker = "> "
		}
		sb.WriteString(fmt.Sprintf("%s%4d | %s\n", marker, i+1, lines[i]))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// readSourceLine reads a single 0-based line from a file.
func readSourceLine(path string, line int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if line < 0 || line >= len(lines) {
		return ""
	}
	return lines[line]
}

// relativePath returns the path relative to wd, or the original path if it can't be made relative.
func relativePath(path, wd string) string {
	rel, err := filepath.Rel(wd, path)
	if err != nil {
		return path
	}
	return rel
}
