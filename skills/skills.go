// Package skills implements the Agent Skills specification.
// See https://agentskills.io for the full specification.
package skills

import (
	"html"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	MaxNameLength          = 64
	MaxDescriptionLength   = 1024
	MaxCompatibilityLength = 500
)

// Skill represents a parsed skill from a SKILL.md file.
type Skill struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	AllowedTools  string            `json:"allowed_tools,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Path          string            `json:"path"` // Path to SKILL.md file
}

// Discover finds all skills in the given directories.
// It scans each directory for subdirectories containing SKILL.md files.
func Discover(dirs []string) []Skill {
	var skills []Skill
	seen := make(map[string]bool)

	for _, dir := range dirs {
		dir = expandPath(dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			skillDir := filepath.Join(dir, entry.Name())
			// Use os.Stat to get info about skill dirs, because they might be
			// symlinks. os.Stat looks at the link target while entry.IsDir
			// looks at the link itself.
			if info, err := os.Stat(skillDir); err != nil || !info.IsDir() {
				continue
			}
			skillMD := findSkillMD(skillDir)
			if skillMD == "" {
				continue
			}

			// Avoid duplicates
			absPath, err := filepath.Abs(skillMD)
			if err != nil {
				continue
			}
			if seen[absPath] {
				continue
			}
			seen[absPath] = true

			skill, err := Parse(skillMD)
			if err != nil {
				continue // Skip invalid skills
			}

			// Validate name matches directory
			if skill.Name != entry.Name() {
				continue
			}

			skills = append(skills, skill)
		}
	}

	return skills
}

// findSkillMD looks for SKILL.md or skill.md in a directory.
func findSkillMD(dir string) string {
	for _, name := range []string{"SKILL.md", "skill.md"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// Parse reads and parses a SKILL.md file.
func Parse(path string) (Skill, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}

	frontmatter, err := parseFrontmatter(string(content))
	if err != nil {
		return Skill{}, err
	}

	name, _ := frontmatter["name"].(string)
	description, _ := frontmatter["description"].(string)

	if name == "" || description == "" {
		return Skill{}, &ValidationError{Message: "name and description are required"}
	}

	if err := validateName(name); err != nil {
		return Skill{}, err
	}

	if len(description) > MaxDescriptionLength {
		return Skill{}, &ValidationError{Message: "description exceeds maximum length"}
	}

	skill := Skill{
		Name:        name,
		Description: description,
		Path:        path,
	}

	if license, ok := frontmatter["license"].(string); ok {
		skill.License = license
	}

	if compat, ok := frontmatter["compatibility"].(string); ok {
		if len(compat) > MaxCompatibilityLength {
			return Skill{}, &ValidationError{Message: "compatibility exceeds maximum length"}
		}
		skill.Compatibility = compat
	}

	if tools, ok := frontmatter["allowed-tools"].(string); ok {
		skill.AllowedTools = tools
	}

	if metadata, ok := frontmatter["metadata"].(map[string]any); ok {
		skill.Metadata = make(map[string]string)
		for k, v := range metadata {
			if s, ok := v.(string); ok {
				skill.Metadata[k] = s
			}
		}
	}

	return skill, nil
}

// ValidationError represents a skill validation error.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

// validateName checks that a skill name follows the spec.
func validateName(name string) error {
	if len(name) == 0 || len(name) > MaxNameLength {
		return &ValidationError{Message: "name must be 1-64 characters"}
	}

	if name != strings.ToLower(name) {
		return &ValidationError{Message: "name must be lowercase"}
	}

	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return &ValidationError{Message: "name cannot start or end with hyphen"}
	}

	if strings.Contains(name, "--") {
		return &ValidationError{Message: "name cannot contain consecutive hyphens"}
	}

	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' {
			return &ValidationError{Message: "name can only contain letters, digits, and hyphens"}
		}
	}

	return nil
}

// parseFrontmatter extracts YAML frontmatter from markdown content.
// This is a simple parser that handles basic YAML without external dependencies.
func parseFrontmatter(content string) (map[string]any, error) {
	if !strings.HasPrefix(content, "---") {
		return nil, &ValidationError{Message: "SKILL.md must start with YAML frontmatter (---)"}
	}

	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return nil, &ValidationError{Message: "SKILL.md frontmatter not properly closed with ---"}
	}

	yamlContent := parts[1]
	return parseSimpleYAML(yamlContent)
}

// parseSimpleYAML parses simple YAML frontmatter.
// Supports: strings, and nested maps (for metadata).
func parseSimpleYAML(content string) (map[string]any, error) {
	result := make(map[string]any)
	lines := strings.Split(content, "\n")

	var currentKey string
	var inNestedMap bool
	nestedMap := make(map[string]any)

	for _, line := range lines {
		// Skip empty lines and comments
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Check for nested map entries (indented with spaces)
		if inNestedMap && (strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")) {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				value = unquoteYAML(value)
				nestedMap[key] = value
			}
			continue
		}

		// If we were in a nested map, save it
		if inNestedMap && currentKey != "" {
			result[currentKey] = nestedMap
			nestedMap = make(map[string]any)
			inNestedMap = false
		}

		// Parse top-level key: value
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if value == "" {
			// Could be start of a nested map
			currentKey = key
			inNestedMap = true
			continue
		}

		value = unquoteYAML(value)
		result[key] = value
	}

	// Handle final nested map
	if inNestedMap && currentKey != "" && len(nestedMap) > 0 {
		result[currentKey] = nestedMap
	}

	return result, nil
}

// unquoteYAML removes surrounding quotes from a YAML string value.
func unquoteYAML(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// ToPromptXML generates the <available_skills> XML block for system prompts.
func ToPromptXML(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<available_skills>\n")

	for _, skill := range skills {
		sb.WriteString("<skill>\n")
		sb.WriteString("<name>")
		sb.WriteString(html.EscapeString(skill.Name))
		sb.WriteString("</name>\n")
		sb.WriteString("<description>")
		sb.WriteString(html.EscapeString(skill.Description))
		sb.WriteString("</description>\n")
		sb.WriteString("<location>")
		sb.WriteString(html.EscapeString(skill.Path))
		sb.WriteString("</location>\n")
		sb.WriteString("</skill>\n")
	}

	sb.WriteString("</available_skills>")
	return sb.String()
}

// DefaultDirs returns the default skill directories to search.
// These are always returned if they exist, regardless of the current working directory.
func DefaultDirs() []string {
	var dirs []string

	home, err := os.UserHomeDir()
	if err != nil {
		return dirs
	}

	// Search these directories for skills:
	// 1. ~/.config/shelley/ (XDG convention for Shelley)
	// 2. ~/.config/agents/skills (shared agents skills directory)
	// 3. ~/.shelley/ (legacy location)
	candidateDirs := []string{
		filepath.Join(home, ".config", "shelley"),
		filepath.Join(home, ".config", "agents", "skills"),
		filepath.Join(home, ".shelley"),
	}

	for _, dir := range candidateDirs {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			dirs = append(dirs, dir)
		}
	}

	return dirs
}

// expandPath expands ~ to the user's home directory.
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// ProjectSkillsDirs returns all .skills directories found by walking up from
// the working directory to the git root (or filesystem root if no git root).
func ProjectSkillsDirs(workingDir, gitRoot string) []string {
	var dirs []string
	seen := make(map[string]bool)

	// Determine the stopping point
	stopAt := gitRoot
	if stopAt == "" {
		stopAt = "/"
	}

	// Walk up from working directory
	current := workingDir
	for current != "" {
		skillsDir := filepath.Join(current, ".skills")
		if !seen[skillsDir] {
			if info, err := os.Stat(skillsDir); err == nil && info.IsDir() {
				dirs = append(dirs, skillsDir)
				seen[skillsDir] = true
			}
		}

		// Stop if we've reached the git root or filesystem root
		if current == stopAt || current == "/" {
			break
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	return dirs
}

// DiscoverInTree finds all skills by walking the directory tree looking for SKILL.md files.
// If gitRoot is provided, it searches from gitRoot. Otherwise, it searches from workingDir downward.
func DiscoverInTree(workingDir, gitRoot string) []Skill {
	var skills []Skill
	seen := make(map[string]bool)

	// Determine root to search from
	searchRoot := gitRoot
	if searchRoot == "" {
		searchRoot = workingDir
	}

	filepath.Walk(searchRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Continue on errors
		}

		if info.IsDir() {
			// Skip hidden directories and common ignore patterns
			name := info.Name()
			if name != "." && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}

		// Check if this is a SKILL.md file
		lowerName := strings.ToLower(info.Name())
		if lowerName != "skill.md" {
			return nil
		}

		// Avoid duplicates
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil
		}
		if seen[absPath] {
			return nil
		}
		seen[absPath] = true

		skill, err := Parse(path)
		if err != nil {
			return nil // Skip invalid skills
		}

		// Validate name matches parent directory
		parentDir := filepath.Base(filepath.Dir(path))
		if skill.Name != parentDir {
			return nil
		}

		skills = append(skills, skill)
		return nil
	})

	return skills
}
