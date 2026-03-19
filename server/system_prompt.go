package server

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	"shelley.exe.dev/skills"
)

//go:embed system_prompt.txt
var systemPromptTemplate string

//go:embed subagent_system_prompt.txt
var subagentSystemPromptTemplate string

//go:embed orchestrator_system_prompt.txt
var orchestratorSystemPromptTemplate string

//go:embed orchestrator_subagent_system_prompt.txt
var orchestratorSubagentSystemPromptTemplate string

// SystemPromptData contains all the data needed to render the system prompt template
type SystemPromptData struct {
	WorkingDirectory string
	GitInfo          *GitInfo
	Codebase         *CodebaseInfo
	IsExeDev         bool
	IsSudoAvailable  bool
	Hostname         string // For exe.dev, the public hostname (e.g., "vmname.exe.xyz")
	ShelleyDBPath    string // Path to the shelley database
	SkillsXML        string // XML block for available skills
	UserEmail        string // The exe.dev auth email of the user, if known
}

// DBPath is the path to the shelley database, set at startup
var DBPath string

type GitInfo struct {
	Root string
}

type CodebaseInfo struct {
	InjectFiles         []string
	InjectFileContents  map[string]string
	SubdirGuidanceFiles []string
}

// SubdirGuidanceSummary returns a prompt-friendly summary of subdirectory guidance files.
// If ≤10, lists them explicitly. If >10, lists the first 10 and notes how many more exist.
func (c *CodebaseInfo) SubdirGuidanceSummary() string {
	if len(c.SubdirGuidanceFiles) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nSubdirectory guidance files (read before editing files in these directories):\n")
	show := c.SubdirGuidanceFiles
	if len(show) > 10 {
		show = show[:10]
	}
	for _, f := range show {
		b.WriteString(f)
		b.WriteByte('\n')
	}
	if len(c.SubdirGuidanceFiles) > 10 {
		fmt.Fprintf(&b, "...and %d more. Use `find` to discover others.\n", len(c.SubdirGuidanceFiles)-10)
	}
	return b.String()
}

// SystemPromptOption configures optional fields on the system prompt.
type SystemPromptOption func(*SystemPromptData)

// WithUserEmail sets the user's email in the system prompt.
func WithUserEmail(email string) SystemPromptOption {
	return func(d *SystemPromptData) {
		d.UserEmail = email
	}
}

// GenerateSystemPrompt generates the system prompt using the embedded template.
// If workingDir is empty, it uses the current working directory.
func GenerateSystemPrompt(workingDir string, opts ...SystemPromptOption) (string, error) {
	data, err := collectSystemData(workingDir)
	if err != nil {
		return "", fmt.Errorf("failed to collect system data: %w", err)
	}

	for _, opt := range opts {
		opt(data)
	}

	tmpl, err := template.New("system_prompt").Parse(systemPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf strings.Builder
	err = tmpl.Execute(&buf, data)
	if err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return collapseBlankLines(buf.String()), nil
}

// collapseBlankLines reduces runs of 3+ newlines to 2 (one blank line)
// and trims leading/trailing whitespace.
var reBlankRun = regexp.MustCompile(`\n{3,}`)

func collapseBlankLines(s string) string {
	s = strings.TrimSpace(s)
	s = reBlankRun.ReplaceAllString(s, "\n\n")
	return s + "\n"
}

func collectSystemData(workingDir string) (*SystemPromptData, error) {
	wd := workingDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get working directory: %w", err)
		}
	}

	data := &SystemPromptData{
		WorkingDirectory: wd,
	}

	// Try to collect git info
	gitInfo, err := collectGitInfo(wd)
	if err == nil {
		data.GitInfo = gitInfo
	}

	// Collect codebase info
	codebaseInfo, err := collectCodebaseInfo(wd, gitInfo)
	if err == nil {
		data.Codebase = codebaseInfo
	}

	// Check if running on exe.dev
	data.IsExeDev = isExeDev()

	// Check sudo availability
	data.IsSudoAvailable = isSudoAvailable()

	// Get hostname for exe.dev
	if data.IsExeDev {
		if hostname, err := os.Hostname(); err == nil {
			// If hostname doesn't contain dots, add .exe.xyz suffix
			if !strings.Contains(hostname, ".") {
				hostname = hostname + ".exe.xyz"
			}
			data.Hostname = hostname
		}
	}

	// Set shelley database path if it was configured
	if DBPath != "" {
		// Convert to absolute path if relative
		if !filepath.IsAbs(DBPath) {
			if absPath, err := filepath.Abs(DBPath); err == nil {
				data.ShelleyDBPath = absPath
			} else {
				data.ShelleyDBPath = DBPath
			}
		} else {
			data.ShelleyDBPath = DBPath
		}
	}

	// Discover and load skills
	var gitRoot string
	if gitInfo != nil {
		gitRoot = gitInfo.Root
	}
	data.SkillsXML = collectSkills(wd, gitRoot)

	return data, nil
}

func collectGitInfo(dir string) (*GitInfo, error) {
	// Find git root
	rootCmd := exec.Command("git", "rev-parse", "--show-toplevel")
	if dir != "" {
		rootCmd.Dir = dir
	}
	rootOutput, err := rootCmd.Output()
	if err != nil {
		return nil, err
	}
	root := strings.TrimSpace(string(rootOutput))

	return &GitInfo{
		Root: root,
	}, nil
}

func collectCodebaseInfo(wd string, gitInfo *GitInfo) (*CodebaseInfo, error) {
	info := &CodebaseInfo{
		InjectFiles:        []string{},
		InjectFileContents: make(map[string]string),
	}

	// Track seen files to avoid duplicates: by resolved path (handles symlinks
	// and case-insensitive filesystems) and by content (handles copies).
	seenFiles := make(map[string]bool)
	seenContents := make(map[string]bool)

	// Check for user-level agent instructions in ~/.config/AGENTS.md, ~/.config/shelley/AGENTS.md, and ~/.shelley/AGENTS.md
	if home, err := os.UserHomeDir(); err == nil {
		userAgentsFiles := []string{
			filepath.Join(home, ".config", "AGENTS.md"),
			filepath.Join(home, ".config", "shelley", "AGENTS.md"),
			filepath.Join(home, ".shelley", "AGENTS.md"),
		}
		for _, f := range userAgentsFiles {
			canonical := resolveAndNormalize(f)
			if seenFiles[canonical] {
				continue
			}
			if content, err := os.ReadFile(f); err == nil && len(content) > 0 {
				contentKey := string(content)
				if seenContents[contentKey] {
					continue
				}
				info.InjectFiles = append(info.InjectFiles, f)
				info.InjectFileContents[f] = contentKey
				seenFiles[canonical] = true
				seenContents[contentKey] = true
			}
		}
	}

	// Determine the root directory to search
	searchRoot := wd
	if gitInfo != nil {
		searchRoot = gitInfo.Root
	}

	// Find root-level guidance files (case-insensitive)
	rootGuidanceFiles := findGuidanceFilesInDir(searchRoot)
	for _, file := range rootGuidanceFiles {
		canonical := resolveAndNormalize(file)
		if seenFiles[canonical] {
			continue
		}

		content, err := os.ReadFile(file)
		if err == nil && len(content) > 0 {
			contentKey := string(content)
			if seenContents[contentKey] {
				continue
			}
			seenFiles[canonical] = true
			seenContents[contentKey] = true
			info.InjectFiles = append(info.InjectFiles, file)
			info.InjectFileContents[file] = contentKey
		}
	}

	// If working directory is different from root, also check working directory
	if wd != searchRoot {
		wdGuidanceFiles := findGuidanceFilesInDir(wd)
		for _, file := range wdGuidanceFiles {
			canonical := resolveAndNormalize(file)
			if seenFiles[canonical] {
				continue
			}

			content, err := os.ReadFile(file)
			if err == nil && len(content) > 0 {
				contentKey := string(content)
				if seenContents[contentKey] {
					continue
				}
				seenFiles[canonical] = true
				seenContents[contentKey] = true
				info.InjectFiles = append(info.InjectFiles, file)
				info.InjectFileContents[file] = contentKey
			}
		}
	}

	// Find subdirectory guidance files for the system prompt listing
	info.SubdirGuidanceFiles = findSubdirGuidanceFiles(searchRoot)

	return info, nil
}

func findGuidanceFilesInDir(dir string) []string {
	// Read directory entries to handle case-insensitive file systems
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var found []string
	seen := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lowerName := strings.ToLower(entry.Name())
		if isGuidanceFile(lowerName) && lowerName != "readme.md" && !seen[lowerName] {
			seen[lowerName] = true
			found = append(found, filepath.Join(dir, entry.Name()))
		}
	}
	return found
}

// isGuidanceFile returns true if the lowercased filename is a recognized guidance file.
func isGuidanceFile(lowerName string) bool {
	switch lowerName {
	case "agents.md", "agent.md", "claude.md", "dear_llm.md", "readme.md":
		return true
	}
	return false
}

// findSubdirGuidanceFiles returns guidance files in subdirectories of root (not root itself).
func findSubdirGuidanceFiles(root string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var found []string
	seen := make(map[string]bool)

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil {
			return nil // Continue on errors
		}
		if info.IsDir() {
			// Skip hidden directories and common ignore patterns
			if strings.HasPrefix(info.Name(), ".") || info.Name() == "node_modules" || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		// Only count files in subdirectories, not root
		if filepath.Dir(path) != root && isGuidanceFile(strings.ToLower(info.Name())) {
			lowerPath := strings.ToLower(path)
			if !seen[lowerPath] {
				seen[lowerPath] = true
				found = append(found, path)
			}
		}
		return nil
	})
	return found
}

func isExeDev() bool {
	_, err := os.Stat("/exe.dev")
	return err == nil
}

// collectSkills discovers skills from default directories, project .skills dirs,
// and the project tree.
func collectSkills(workingDir, gitRoot string) string {
	// Start with default directories (user-level skills)
	dirs := skills.DefaultDirs()

	// Add .skills directories found in the project tree
	dirs = append(dirs, skills.ProjectSkillsDirs(workingDir, gitRoot)...)

	// Discover skills from all directories
	foundSkills := skills.Discover(dirs)

	// Also discover skills anywhere in the project tree
	treeSkills := skills.DiscoverInTree(workingDir, gitRoot)

	// Merge, avoiding duplicates by path
	seen := make(map[string]bool)
	for _, s := range foundSkills {
		seen[s.Path] = true
	}
	for _, s := range treeSkills {
		if !seen[s.Path] {
			foundSkills = append(foundSkills, s)
			seen[s.Path] = true
		}
	}

	// Generate XML
	return skills.ToPromptXML(foundSkills)
}

// resolveAndNormalize returns a canonical lowercase path for dedup.
// It resolves symlinks and normalizes to lowercase for case-insensitive FS.
func resolveAndNormalize(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return strings.ToLower(path)
}

func isSudoAvailable() bool {
	cmd := exec.Command("sudo", "-n", "id")
	_, err := cmd.CombinedOutput()
	return err == nil
}

// SubagentSystemPromptData contains data for subagent system prompts (minimal subset)
type SubagentSystemPromptData struct {
	WorkingDirectory string
	GitInfo          *GitInfo
	ShelleyDBPath    string
	ConversationID   string // Parent conversation ID for querying user messages
}

// OrchestratorSystemPromptData contains data for orchestrator system prompts.
type OrchestratorSystemPromptData struct {
	WorkingDirectory string
	GitInfo          *GitInfo
	ContextDir       string
	Codebase         *CodebaseInfo
	ShelleyDBPath    string
	ConversationID   string // This conversation's ID for querying user messages
}

// GenerateSubagentSystemPrompt generates a minimal system prompt for subagent conversations.
func GenerateSubagentSystemPrompt(workingDir, parentConversationID string) (string, error) {
	wd := workingDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
	}

	data := &SubagentSystemPromptData{
		WorkingDirectory: wd,
		ShelleyDBPath:    DBPath,
		ConversationID:   parentConversationID,
	}

	// Try to collect git info
	gitInfo, err := collectGitInfo(wd)
	if err == nil {
		data.GitInfo = gitInfo
	}

	tmpl, err := template.New("subagent_system_prompt").Parse(subagentSystemPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse subagent template: %w", err)
	}

	var buf strings.Builder
	err = tmpl.Execute(&buf, data)
	if err != nil {
		return "", fmt.Errorf("failed to execute subagent template: %w", err)
	}

	return collapseBlankLines(buf.String()), nil
}

// GenerateOrchestratorSystemPrompt generates the system prompt for orchestrator conversations.
func GenerateOrchestratorSystemPrompt(workingDir, contextDir, conversationID string) (string, error) {
	wd := workingDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
	}

	data := &OrchestratorSystemPromptData{
		WorkingDirectory: wd,
		ContextDir:       contextDir,
		ShelleyDBPath:    DBPath,
		ConversationID:   conversationID,
	}

	gitInfo, err := collectGitInfo(wd)
	if err == nil {
		data.GitInfo = gitInfo
	}

	codebaseInfo, err := collectCodebaseInfo(wd, data.GitInfo)
	if err == nil {
		data.Codebase = codebaseInfo
	}

	tmpl, err := template.New("orchestrator_system_prompt").Parse(orchestratorSystemPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse orchestrator template: %w", err)
	}

	var buf strings.Builder
	err = tmpl.Execute(&buf, data)
	if err != nil {
		return "", fmt.Errorf("failed to execute orchestrator template: %w", err)
	}

	return collapseBlankLines(buf.String()), nil
}

// GenerateOrchestratorSubagentSystemPrompt generates the system prompt for
// subagents spawned by an orchestrator conversation.
func GenerateOrchestratorSubagentSystemPrompt(workingDir, parentConversationID string) (string, error) {
	wd := workingDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
	}

	data := &SubagentSystemPromptData{
		WorkingDirectory: wd,
		ShelleyDBPath:    DBPath,
		ConversationID:   parentConversationID,
	}

	gitInfo, err := collectGitInfo(wd)
	if err == nil {
		data.GitInfo = gitInfo
	}

	tmpl, err := template.New("orchestrator_subagent_system_prompt").Parse(orchestratorSubagentSystemPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse orchestrator subagent template: %w", err)
	}

	var buf strings.Builder
	err = tmpl.Execute(&buf, data)
	if err != nil {
		return "", fmt.Errorf("failed to execute orchestrator subagent template: %w", err)
	}

	return collapseBlankLines(buf.String()), nil
}
