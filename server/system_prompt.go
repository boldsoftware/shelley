package server

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed system_prompt.txt
var systemPromptTemplate string

// SystemPromptData contains all the data needed to render the system prompt template
type SystemPromptData struct {
	WorkingDirectory string
	GitInfo          *GitInfo
	Codebase         *CodebaseInfo
	IsExeDev         bool
	IsSudoAvailable  bool
	Hostname         string // For exe.dev, the public hostname (e.g., "vmname.exe.xyz")
	ShelleyDBPath    string // Path to the shelley database
	ShelleyPort      string // Port Shelley is running on
	ShelleyBaseURL   string // Full base URL for Shelley web UI
	ModelName        string // LLM model being used for this conversation
}

// DBPath is the path to the shelley database, set at startup
var DBPath string

// Port is the port Shelley is running on, set at startup
var Port string

type GitInfo struct {
	Root string
}

type CodebaseInfo struct {
	InjectFiles        []string
	InjectFileContents map[string]string
	GuidanceFiles      []string
	SkillPreambles     []string // Preambles from installed skills
}

// GenerateSystemPrompt generates the system prompt using the embedded template.
// If workingDir is empty, it uses the current working directory.
func GenerateSystemPrompt(workingDir string, modelName string) (string, error) {
	data, err := collectSystemData(workingDir)
	if err != nil {
		return "", fmt.Errorf("failed to collect system data: %w", err)
	}
	data.ModelName = modelName

	tmpl, err := template.New("system_prompt").Parse(systemPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf strings.Builder
	err = tmpl.Execute(&buf, data)
	if err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
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
	gitInfo, err := collectGitInfo()
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

	// Set Shelley port and base URL
	if Port != "" {
		data.ShelleyPort = Port
		if data.IsExeDev {
			if Port == "9999" {
				// Default port uses shelley.exe.xyz subdomain
				if hostname, err := os.Hostname(); err == nil {
					data.ShelleyBaseURL = "https://" + hostname + ".shelley.exe.xyz"
				}
			} else {
				// Other ports use hostname:port
				data.ShelleyBaseURL = "https://" + data.Hostname + ":" + Port
			}
		} else {
			// Not exe.dev - use localhost
			data.ShelleyBaseURL = "http://localhost:" + Port
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

	return data, nil
}

func collectGitInfo() (*GitInfo, error) {
	// Find git root
	rootCmd := exec.Command("git", "rev-parse", "--show-toplevel")
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
		GuidanceFiles:      []string{},
	}

	// Track seen files to avoid duplicates on case-insensitive file systems
	seenFiles := make(map[string]bool)

	// Check for user-level agent instructions in ~/.config/shelley/AGENTS.md and ~/.shelley/AGENTS.md
	if home, err := os.UserHomeDir(); err == nil {
		// Prefer ~/.config/shelley/AGENTS.md (XDG convention)
		configAgentsFile := filepath.Join(home, ".config", "shelley", "AGENTS.md")
		if content, err := os.ReadFile(configAgentsFile); err == nil && len(content) > 0 {
			info.InjectFiles = append(info.InjectFiles, configAgentsFile)
			info.InjectFileContents[configAgentsFile] = string(content)
			seenFiles[strings.ToLower(configAgentsFile)] = true
		}
		// Also check legacy ~/.shelley/AGENTS.md location
		shelleyAgentsFile := filepath.Join(home, ".shelley", "AGENTS.md")
		if content, err := os.ReadFile(shelleyAgentsFile); err == nil && len(content) > 0 {
			lowerPath := strings.ToLower(shelleyAgentsFile)
			if !seenFiles[lowerPath] {
				info.InjectFiles = append(info.InjectFiles, shelleyAgentsFile)
				info.InjectFileContents[shelleyAgentsFile] = string(content)
				seenFiles[lowerPath] = true
			}
		}

		// Load installed skills from ~/.config/shelley/skills/
		info.SkillPreambles = loadSkillPreambles(home)
	}

	// Determine the root directory to search
	searchRoot := wd
	if gitInfo != nil {
		searchRoot = gitInfo.Root
	}

	// Find root-level guidance files (case-insensitive)
	rootGuidanceFiles := findGuidanceFilesInDir(searchRoot)
	for _, file := range rootGuidanceFiles {
		lowerPath := strings.ToLower(file)
		if seenFiles[lowerPath] {
			continue
		}
		seenFiles[lowerPath] = true

		content, err := os.ReadFile(file)
		if err == nil && len(content) > 0 {
			info.InjectFiles = append(info.InjectFiles, file)
			info.InjectFileContents[file] = string(content)
		}
	}

	// If working directory is different from root, also check working directory
	if wd != searchRoot {
		wdGuidanceFiles := findGuidanceFilesInDir(wd)
		for _, file := range wdGuidanceFiles {
			lowerPath := strings.ToLower(file)
			if seenFiles[lowerPath] {
				continue
			}
			seenFiles[lowerPath] = true

			content, err := os.ReadFile(file)
			if err == nil && len(content) > 0 {
				info.InjectFiles = append(info.InjectFiles, file)
				info.InjectFileContents[file] = string(content)
			}
		}
	}

	// Find all guidance files recursively for the directory listing
	allGuidanceFiles := findAllGuidanceFiles(searchRoot)
	info.GuidanceFiles = allGuidanceFiles

	return info, nil
}

func findGuidanceFilesInDir(dir string) []string {
	// Read directory entries to handle case-insensitive file systems
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	guidanceNames := map[string]bool{
		"agent.md":    true,
		"agents.md":   true,
		"claude.md":   true,
		"dear_llm.md": true,
		"readme.md":   true,
	}

	var found []string
	seen := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lowerName := strings.ToLower(entry.Name())
		if guidanceNames[lowerName] && !seen[lowerName] {
			seen[lowerName] = true
			found = append(found, filepath.Join(dir, entry.Name()))
		}
	}
	return found
}

func findAllGuidanceFiles(root string) []string {
	guidanceNames := map[string]bool{
		"agent.md":    true,
		"agents.md":   true,
		"claude.md":   true,
		"dear_llm.md": true,
	}

	var found []string
	seen := make(map[string]bool)

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
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
		lowerName := strings.ToLower(info.Name())
		if guidanceNames[lowerName] {
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

func isSudoAvailable() bool {
	cmd := exec.Command("sudo", "-n", "id")
	_, err := cmd.CombinedOutput()
	return err == nil
}

// loadSkillPreambles reads SKILL.md files from ~/.config/shelley/skills/ (following
// Anthropic's skill format) and extracts name + description from YAML frontmatter.
func loadSkillPreambles(home string) []string {
	skillsDir := filepath.Join(home, ".config", "shelley", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil
	}

	var preambles []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillFile := filepath.Join(skillsDir, entry.Name(), "SKILL.md")
		content, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}
		if preamble := parseSkillPreamble(string(content)); preamble != "" {
			preambles = append(preambles, preamble)
		}
	}
	return preambles
}

// parseSkillPreamble extracts name and description from YAML frontmatter,
// following Anthropic's skill format (https://docs.anthropic.com/en/docs/claude-code/skills).
// Returns "name: description" for injection into the system prompt.
func parseSkillPreamble(content string) string {
	if !strings.HasPrefix(content, "---") {
		return ""
	}
	// Find the closing ---
	endIdx := strings.Index(content[3:], "\n---")
	if endIdx == -1 {
		return ""
	}
	frontmatter := content[4 : endIdx+3] // Skip initial ---\n

	// Extract name and description fields
	var name, description string
	lines := strings.Split(frontmatter, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "name:") {
			name = strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
		} else if strings.HasPrefix(trimmed, "description:") {
			// Check if it's inline or multiline (> or |)
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
			if value != "" && value != "|" && value != ">" {
				description = value
			} else {
				// Multiline: collect indented lines
				var multiline []string
				for j := i + 1; j < len(lines); j++ {
					if len(lines[j]) == 0 {
						continue
					}
					if lines[j][0] == ' ' || lines[j][0] == '\t' {
						multiline = append(multiline, strings.TrimSpace(lines[j]))
					} else {
						break
					}
				}
				description = strings.Join(multiline, " ")
			}
		}
	}

	if name == "" {
		return ""
	}
	if description == "" {
		return name
	}
	return name + ": " + description
}
