// Package skills implements the Agent Skills specification.
// See https://agentskills.io for the full specification.
package skills

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	When          string            `json:"when,omitempty"`
	AllowedTools  string            `json:"allowed_tools,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Path          string            `json:"path"`           // Path to SKILL.md file (empty for built-in skills)
	Body          string            `json:"body,omitempty"` // Full markdown body (set for built-in skills)
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

// CreateTemplate creates a new skill directory with a template SKILL.md
// in ~/.config/shelley/<name>/SKILL.md. It returns the path to the created file.
func CreateTemplate(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	dir := filepath.Join(home, ".config", "shelley", name)
	path := filepath.Join(dir, "SKILL.md")

	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists", path)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating directory: %w", err)
	}

	content := fmt.Sprintf(`---
name: %s
description: Use when %s.
---

When %s, act accordingly.
`, name, name, name)

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("writing SKILL.md: %w", err)
	}

	return path, nil
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

	if when, ok := frontmatter["when"].(string); ok {
		skill.When = when
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
		sb.WriteString("<activate>shelley skill cat ")
		sb.WriteString(html.EscapeString(skill.Name))
		sb.WriteString("</activate>\n")
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
// It returns both the parsed skills and the set of all SKILL.md parent directory names
// encountered during the walk (including unparseable/empty ones). This avoids needing
// a second walk just to collect names.
func DiscoverInTree(workingDir, gitRoot string) ([]Skill, map[string]bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var skills []Skill
	seen := make(map[string]bool)
	allNames := make(map[string]bool)

	// Determine root to search from
	searchRoot := gitRoot
	if searchRoot == "" {
		searchRoot = workingDir
	}

	filepath.Walk(searchRoot, func(path string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
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

		// Record the name regardless of parseability (for builtin suppression)
		allNames[filepath.Base(filepath.Dir(path))] = true

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

	return skills, allNames
}

// ListAll returns all available skills (built-in + filesystem), deduplicated by name.
//
// Filesystem skills take priority over built-in skills with the same name.
// An empty SKILL.md on the filesystem suppresses the corresponding built-in
// skill entirely — this is the mechanism for users to disable built-in skills.
//
// If gitRoot is empty, it is computed from workingDir.
func ListAll(workingDir, gitRoot string) []Skill {
	if gitRoot == "" {
		gitRoot = findGitRoot(workingDir)
	}

	dirs := DefaultDirs()
	dirs = append(dirs, ProjectSkillsDirs(workingDir, gitRoot)...)

	all := Discover(dirs)

	// Add tree-discovered skills, deduplicated by name (first-seen wins).
	// DiscoverInTree also returns all SKILL.md names it encountered
	// (including unparseable/empty ones) so we don't need a second walk.
	seen := make(map[string]bool)
	for _, s := range all {
		seen[s.Name] = true
	}
	treeSkills, treeNames := DiscoverInTree(workingDir, gitRoot)
	for _, s := range treeSkills {
		if !seen[s.Name] {
			all = append(all, s)
			seen[s.Name] = true
		}
	}

	// Collect all skill names claimed on the filesystem (including empty
	// SKILL.md files that wouldn't survive Parse). A filesystem SKILL.md —
	// even an empty one — takes precedence over a built-in skill of the
	// same name. This lets users suppress a built-in skill by placing an
	// empty SKILL.md in the matching directory.
	fsNames := dirSkillNames(dirs)
	for name := range treeNames {
		fsNames[name] = true
	}
	for _, s := range all {
		fsNames[s.Name] = true
	}

	for _, s := range BuiltinSkills() {
		if !fsNames[s.Name] {
			all = append(all, s)
		}
	}

	return all
}

// Env describes the runtime environment used to gate skills with a `when:`
// condition. Currently only ExeDev is checked; extend as new conditions are
// added. The zero value satisfies no conditions, so skills with a `when:`
// clause are filtered out by default.
type Env struct {
	ExeDev bool
}

// Filter returns the subset of skills whose `when:` condition is satisfied by
// env. Skills without a `when:` clause are always included. Unknown condition
// values cause the skill to be filtered out (fail-closed).
func Filter(in []Skill, env Env) []Skill {
	out := make([]Skill, 0, len(in))
	for _, s := range in {
		if s.When == "" || matchWhen(s.When, env) {
			out = append(out, s)
		}
	}
	return out
}

func matchWhen(when string, env Env) bool {
	switch strings.TrimSpace(when) {
	case "exe.dev":
		return env.ExeDev
	default:
		return false
	}
}

// FindByName looks up a skill by name and returns its raw SKILL.md content.
//
// Filesystem skills take priority: if a SKILL.md exists on the filesystem
// for the given name it is returned, even if a built-in skill with the
// same name exists. An empty filesystem SKILL.md suppresses the built-in
// skill — this lets users delete built-in skills they don't want.
func FindByName(name, workingDir string) (string, error) {
	gitRoot := findGitRoot(workingDir)
	dirs := DefaultDirs()
	dirs = append(dirs, ProjectSkillsDirs(workingDir, gitRoot)...)

	// Filesystem first: check directory-based discovery, then tree discovery.
	for _, s := range Discover(dirs) {
		if s.Name == name {
			content, err := os.ReadFile(s.Path)
			if err != nil {
				return "", fmt.Errorf("reading skill %q: %w", name, err)
			}
			return string(content), nil
		}
	}
	treeSkills, treeNames := DiscoverInTree(workingDir, gitRoot)
	for _, s := range treeSkills {
		if s.Name == name {
			content, err := os.ReadFile(s.Path)
			if err != nil {
				return "", fmt.Errorf("reading skill %q: %w", name, err)
			}
			return string(content), nil
		}
	}

	// If a SKILL.md exists on the filesystem for this name but didn't
	// parse (e.g. it's empty), the user is deliberately suppressing
	// the built-in skill. Don't fall through.
	fsNames := dirSkillNames(dirs)
	for n := range treeNames {
		fsNames[n] = true
	}
	if fsNames[name] {
		// Distinguish intentional suppression (empty file) from parse errors.
		for _, dir := range dirs {
			dir = expandPath(dir)
			if path := findSkillMD(filepath.Join(dir, name)); path != "" {
				if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
					if _, parseErr := Parse(path); parseErr != nil {
						return "", fmt.Errorf("skill %q (%s): %w", name, path, parseErr)
					}
				}
				break
			}
		}
		return "", fmt.Errorf("skill %q is disabled", name)
	}

	// Fall back to built-in skills.
	for _, s := range BuiltinSkills() {
		if s.Name == name {
			data, err := builtinFS.ReadFile("builtin/" + name + "/SKILL.md")
			if err != nil {
				return "", fmt.Errorf("reading built-in skill %q: %w", name, err)
			}
			return string(data), nil
		}
	}

	return "", fmt.Errorf("skill %q not found", name)
}

// dirSkillNames returns the set of skill names found in the given skill
// directories (not the project tree — tree names come from DiscoverInTree).
// An empty SKILL.md in a directory like ~/.config/shelley/schedule/ prevents
// the built-in "schedule" skill from appearing.
func dirSkillNames(dirs []string) map[string]bool {
	names := make(map[string]bool)
	for _, dir := range dirs {
		dir = expandPath(dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			skillDir := filepath.Join(dir, entry.Name())
			if info, err := os.Stat(skillDir); err != nil || !info.IsDir() {
				continue
			}
			if findSkillMD(skillDir) != "" {
				names[entry.Name()] = true
			}
		}
	}
	return names
}

// findGitRoot returns the git root for the given directory, or "" if not in a repo.
func findGitRoot(dir string) string {
	if dir == "" {
		return ""
	}
	current := dir
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}
