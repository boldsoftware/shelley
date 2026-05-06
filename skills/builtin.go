package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

//go:embed builtin/*/SKILL.md
var builtinFS embed.FS

// BuiltinSkills returns all skills embedded in the binary.
// These skills have Body set (inline content) and Path empty.
func BuiltinSkills() []Skill {
	var out []Skill

	fs.WalkDir(builtinFS, "builtin", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.ToLower(d.Name()) != "skill.md" {
			return nil
		}

		data, err := builtinFS.ReadFile(path)
		if err != nil {
			panic(fmt.Sprintf("reading embedded skill %s: %v", path, err))
		}

		frontmatter, err := parseFrontmatter(string(data))
		if err != nil {
			panic(fmt.Sprintf("parsing embedded skill %s: %v", path, err))
		}

		name, _ := frontmatter["name"].(string)
		description, _ := frontmatter["description"].(string)
		if name == "" || description == "" {
			panic(fmt.Sprintf("embedded skill %s: name and description are required", path))
		}

		// Validate name matches parent directory
		parentDir := filepath.Base(filepath.Dir(path))
		if name != parentDir {
			panic(fmt.Sprintf("embedded skill %s: name %q does not match directory %q", path, name, parentDir))
		}

		var when string
		if w, ok := frontmatter["when"].(string); ok {
			when = w
		}

		// Extract the body (everything after the second ---)
		body := extractBody(string(data))

		out = append(out, Skill{
			Name:        name,
			Description: description,
			When:        when,
			Body:        body,
		})
		return nil
	})

	return out
}

// extractBody returns the markdown content after the YAML frontmatter.
func extractBody(content string) string {
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return ""
	}
	return strings.TrimSpace(parts[2])
}
