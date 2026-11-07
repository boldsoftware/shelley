// Package skills implements the Agent Skills specification.
// See https://agentskills.io for the full specification.
package skills

import (
	"context"
	"fmt"
	"html"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
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
	Path          string            `json:"path"`               // Path to SKILL.md file (empty for non-filesystem skills)
	Body          string            `json:"body,omitempty"`     // Full markdown body (set for built-in skills)
	Activate      string            `json:"activate,omitempty"` // Command that prints the full skill for activation
	Source        string            `json:"source,omitempty"`   // Filesystem path, built-in path, or integration root URL
	Origin        string            `json:"origin,omitempty"`   // File, Integration, or Built into Shelley
}

// ActivationCommand returns the command agents should run to load the skill.
func (s Skill) ActivationCommand() string {
	if s.Activate != "" {
		return s.Activate
	}
	return "shelley skill cat " + s.Name
}

// SourceLocation returns the location shown in system-prompt metadata.
func (s Skill) SourceLocation() string {
	if s.Source != "" {
		return s.Source
	}
	if s.Path != "" {
		return s.Path
	}
	return "skills/builtin/" + s.Name + "/SKILL.md"
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

	skill, err := ParseContent(string(content))
	if err != nil {
		return Skill{}, err
	}
	skill.Path = path
	skill.Source = path
	skill.Origin = "File"
	return skill, nil
}

// ParseContent parses SKILL.md content without reading from the filesystem.
func ParseContent(content string) (Skill, error) {
	frontmatter, err := parseFrontmatter(content)
	if err != nil {
		return Skill{}, err
	}

	name, _ := frontmatter["name"].(string)
	description, _ := frontmatter["description"].(string)

	if strings.IndexByte(name, 0) >= 0 || strings.IndexByte(description, 0) >= 0 {
		return Skill{}, &ValidationError{Message: "name and description must not contain NUL bytes"}
	}
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
		Activate:    "shelley skill cat " + name,
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

// splitFrontmatter separates YAML frontmatter from the markdown body.
func splitFrontmatter(content string) (yamlContent, body string, err error) {
	trimmed := strings.TrimLeftFunc(content, unicode.IsSpace)
	documentStart := len(content) - len(trimmed)
	lineOffset := strings.IndexByte(trimmed, '\n')
	if lineOffset < 0 {
		return "", "", &ValidationError{Message: "SKILL.md must start with YAML frontmatter (---)"}
	}
	lineEnd := documentStart + lineOffset
	if strings.TrimRight(strings.TrimSuffix(content[documentStart:lineEnd], "\r"), " \t") != "---" {
		return "", "", &ValidationError{Message: "SKILL.md must start with YAML frontmatter (---)"}
	}
	start := lineEnd + 1

	for pos := start; pos <= len(content); {
		lineEnd := len(content)
		next := len(content)
		if offset := strings.IndexByte(content[pos:], '\n'); offset >= 0 {
			lineEnd = pos + offset
			next = lineEnd + 1
		}
		line := strings.TrimRight(strings.TrimSuffix(content[pos:lineEnd], "\r"), " \t")
		if line == "---" {
			return content[start:pos], content[next:], nil
		}
		if next == len(content) {
			break
		}
		pos = next
	}
	return "", "", &ValidationError{Message: "SKILL.md frontmatter not properly closed with ---"}
}

// parseFrontmatter extracts YAML frontmatter from markdown content.
func parseFrontmatter(content string) (map[string]any, error) {
	yamlContent, _, err := splitFrontmatter(content)
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(yamlContent))
	var node yaml.Node
	if err := decoder.Decode(&node); err != nil {
		return nil, &ValidationError{Message: "invalid YAML frontmatter: " + err.Error()}
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, &ValidationError{Message: "invalid YAML frontmatter: multiple YAML documents"}
		}
		return nil, &ValidationError{Message: "invalid YAML frontmatter: " + err.Error()}
	}
	value, err := stringScalarYAMLValue(&node)
	if err != nil {
		return nil, &ValidationError{Message: "invalid YAML frontmatter: " + err.Error()}
	}
	result, ok := value.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: "YAML frontmatter must be a mapping"}
	}
	return result, nil
}

// stringScalarYAMLValue converts YAML mappings recursively while preserving
// every scalar's source text as a string. Agent Skills frontmatter defines
// string fields, and the old parser treated unquoted values such as 1.0 and
// true as strings rather than silently dropping them after YAML implicit typing.
const maxSkillYAMLNodeVisits = 10_000

func stringScalarYAMLValue(node *yaml.Node) (any, error) {
	remaining := maxSkillYAMLNodeVisits
	return stringScalarYAMLValueVisit(node, make(map[*yaml.Node]bool), &remaining)
}

func stringScalarYAMLValueVisit(node *yaml.Node, visiting map[*yaml.Node]bool, remaining *int) (any, error) {
	if node == nil {
		return nil, nil
	}
	if *remaining == 0 {
		return nil, fmt.Errorf("YAML alias expansion exceeds limit")
	}
	*remaining = *remaining - 1
	if visiting[node] {
		return nil, fmt.Errorf("cyclic YAML alias")
	}
	visiting[node] = true
	defer delete(visiting, node)

	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) != 1 {
			return nil, fmt.Errorf("expected one YAML document")
		}
		return stringScalarYAMLValueVisit(node.Content[0], visiting, remaining)
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return nil, fmt.Errorf("invalid YAML mapping")
		}
		result := make(map[string]any, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("mapping key must be a scalar")
			}
			if _, exists := result[key.Value]; exists {
				return nil, fmt.Errorf("duplicate mapping key %q", key.Value)
			}
			value, err := stringScalarYAMLValueVisit(node.Content[i+1], visiting, remaining)
			if err != nil {
				return nil, err
			}
			result[key.Value] = value
		}
		return result, nil
	case yaml.SequenceNode:
		result := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			value, err := stringScalarYAMLValueVisit(child, visiting, remaining)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		return result, nil
	case yaml.ScalarNode:
		return node.Value, nil
	case yaml.AliasNode:
		if node.Alias == nil {
			return nil, fmt.Errorf("YAML alias has no target")
		}
		return stringScalarYAMLValueVisit(node.Alias, visiting, remaining)
	default:
		return nil, fmt.Errorf("unsupported YAML node kind %d", node.Kind)
	}
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
		sb.WriteString("<activate>")
		sb.WriteString(html.EscapeString(skill.ActivationCommand()))
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
	// 3. ~/.agents/skills (non-XDG alternative to ~/.config/agents/skills)
	// 4. ~/.shelley/ (legacy location)
	candidateDirs := []string{
		filepath.Join(home, ".config", "shelley"),
		filepath.Join(home, ".config", "agents", "skills"),
		filepath.Join(home, ".agents", "skills"),
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

	// WalkDir reads directory entries without stat'ing every file; only
	// SKILL.md candidates are opened (by Parse).
	filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil {
			return nil // Continue on errors
		}

		if d.IsDir() {
			// Skip hidden directories and common ignore patterns
			name := d.Name()
			if name != "." && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}

		// Check if this is a SKILL.md file
		lowerName := strings.ToLower(d.Name())
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

// ListAll returns all available filesystem and built-in skills, deduplicated by name.
func ListAll(workingDir, gitRoot string) []Skill {
	return ListAllWithIntegrations(workingDir, gitRoot, nil)
}

// ListAllWithIntegrations merges filesystem, integration-discovered, and
// built-in skills. Filesystem claims win even when their SKILL.md is malformed
// or empty, followed by integrations, then built-ins. Within each source,
// first-seen wins.
func ListAllWithIntegrations(workingDir, gitRoot string, integrationSkills []Skill) []Skill {
	if gitRoot == "" {
		gitRoot = findGitRoot(workingDir)
	}
	_, all, claimed := filesystemSkills(workingDir, gitRoot)
	for _, s := range slices.Concat(integrationSkills, BuiltinSkills()) {
		if !claimed[s.Name] {
			all = append(all, s)
			claimed[s.Name] = true
		}
	}
	return all
}

// filesystemSkills returns the skill directories searched, the skills parsed
// from them followed by those found in the project tree (deduplicated by
// name, first-seen wins), and every name claimed by a filesystem SKILL.md.
// Claims include empty or unparseable files: a filesystem SKILL.md, even an
// empty one, takes precedence over a built-in skill of the same name, which
// lets users suppress a built-in skill.
func filesystemSkills(workingDir, gitRoot string) (dirs []string, found []Skill, claimed map[string]bool) {
	dirs = append(DefaultDirs(), ProjectSkillsDirs(workingDir, gitRoot)...)
	found = Discover(dirs)
	claimed = dirSkillNames(dirs)
	for _, s := range found {
		claimed[s.Name] = true
	}
	treeSkills, treeNames := DiscoverInTree(workingDir, gitRoot)
	for _, s := range treeSkills {
		if !claimed[s.Name] {
			found = append(found, s)
			claimed[s.Name] = true
		}
	}
	maps.Copy(claimed, treeNames)
	return dirs, found, claimed
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
	dirs, found, claimed := filesystemSkills(workingDir, findGitRoot(workingDir))
	for _, s := range found {
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
	if claimed[name] {
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
