package server

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
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

//go:embed operational_context.txt
var operationalContextTemplate string

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

	prompt := collapseBlankLines(buf.String())
	return runHook(hookSystemPrompt, prompt)
}

// collapseBlankLines reduces runs of 3+ newlines to 2 (one blank line)
// and trims leading/trailing whitespace.
var reBlankRun = regexp.MustCompile(`\n{3,}`)

func collapseBlankLines(s string) string {
	s = strings.TrimSpace(s)
	s = reBlankRun.ReplaceAllString(s, "\n\n")
	return s + "\n"
}

const (
	hookSystemPrompt    = "system-prompt"
	hookNewConversation = "new-conversation"
)

// NewConversationHookInput is the JSON data passed to the new-conversation hook on stdin.
// The JSON has mutable fields at the top level and a "readonly" block for context.
//
// Example JSON:
//
//	{
//	  "prompt": "the user's message",
//	  "model": "claude-sonnet-4.5",
//	  "cwd": "/home/user/project",
//	  "readonly": {
//	    "conversation_id": "abc-123",
//	    "is_subagent": false,
//	    "parent_id": "",
//	    "is_distillation": true,
//	    "source_id": "def-456",
//	    "is_orchestrator": false
//	  }
//	}
//
// The hook should output the same top-level JSON shape (prompt, model, cwd).
// Only the mutable fields are read from the output; "readonly" is ignored.
// Empty output means no changes.
type NewConversationHookInput struct {
	// Mutable fields — the hook may change these.
	Prompt string `json:"prompt"`
	Model  string `json:"model"`
	Cwd    string `json:"cwd"`

	// Readonly context — visible to the hook but changes are ignored.
	Readonly NewConversationReadonly `json:"readonly"`
}

// NewConversationReadonly contains context fields the hook can read but not change.
type NewConversationReadonly struct {
	ConversationID string `json:"conversation_id"`
	IsSubagent     bool   `json:"is_subagent"`
	ParentID       string `json:"parent_id,omitempty"`
	IsDistillation bool   `json:"is_distillation"`
	SourceID       string `json:"source_id,omitempty"`
	IsOrchestrator bool   `json:"is_orchestrator"`
}

// NewConversationHookResult contains the (possibly modified) mutable fields
// returned from the new-conversation hook.
type NewConversationHookResult struct {
	Prompt string
	Model  string
	Cwd    string
}

// RunNewConversationHook runs the new-conversation hook if it exists.
// It returns the (possibly modified) mutable fields.
// If the hook doesn't exist or fails, the original values are returned unchanged.
func RunNewConversationHook(input NewConversationHookInput) NewConversationHookResult {
	original := NewConversationHookResult{
		Prompt: input.Prompt,
		Model:  input.Model,
		Cwd:    input.Cwd,
	}

	hookPath, err := findHook(hookNewConversation)
	if err != nil {
		slog.Error("new-conversation hook: findHook failed", "error", err)
		return original
	}
	if hookPath == "" {
		return original
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		slog.Error("new-conversation hook: failed to marshal input", "error", err)
		return original
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, hookPath)
	cmd.Stdin = strings.NewReader(string(inputJSON))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		slog.Error("new-conversation hook failed", "hook", hookPath, "error", err, "stderr", stderr.String())
		return original
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		// Empty output is fine — hook ran but has no overrides.
		return original
	}

	// Parse only the mutable fields from the output.
	var hookOut struct {
		Prompt string `json:"prompt"`
		Model  string `json:"model"`
		Cwd    string `json:"cwd"`
	}
	if err := json.Unmarshal([]byte(output), &hookOut); err != nil {
		slog.Error("new-conversation hook: invalid JSON output", "error", err, "output", output)
		return original
	}

	result := original
	if hookOut.Cwd != "" {
		result.Cwd = hookOut.Cwd
	}
	if hookOut.Prompt != "" {
		result.Prompt = hookOut.Prompt
	}
	if hookOut.Model != "" {
		result.Model = hookOut.Model
	}

	if result != original {
		slog.Info("new-conversation hook applied overrides",
			"cwdChanged", result.Cwd != original.Cwd,
			"promptChanged", result.Prompt != original.Prompt,
			"modelChanged", result.Model != original.Model,
		)
	}

	return result
}

// findHook returns the path to the hook if it exists and is executable,
// or empty string if not found.
func findHook(name string) (string, error) {
	if filepath.Base(name) != name {
		return "", fmt.Errorf("invalid hook name: %q", name)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	hookPath := filepath.Join(home, ".config", "shelley", "hooks", name)
	info, err := os.Stat(hookPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return "", nil
	}
	return hookPath, nil
}

// runHook checks for an executable hook at ~/.config/shelley/hooks/<name> and,
// if found, runs it with the prompt on stdin. The hook's stdout replaces the
// prompt. If the hook doesn't exist, the prompt is returned unchanged. If the
// hook exists but fails, an error is returned.
func runHook(name, prompt string) (string, error) {
	hookPath, err := findHook(name)
	if err != nil {
		return "", fmt.Errorf("hook %s: %w", name, err)
	}
	if hookPath == "" {
		return prompt, nil // no hook
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, hookPath)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("hook %s failed: %w (stderr: %s)", hookPath, err, stderr.String())
	}

	result := stdout.String()
	if result == "" {
		return "", fmt.Errorf("hook %s returned empty output", hookPath)
	}

	slog.Info("hook applied", "name", name, "hook", hookPath, "originalLen", len(prompt), "newLen", len(result))
	return result, nil
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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
// the project tree, and built-in skills. See skills.ListAll for precedence rules.
func collectSkills(workingDir, gitRoot string) string {
	return skills.ToPromptXML(skills.ListAll(workingDir, gitRoot))
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

// SubagentSystemPromptData contains data for subagent system prompts (minimal subset).
// Used in two contexts:
//   - Non-orchestrator subagents (GenerateSubagentSystemPrompt): WorkingDirectory, GitInfo,
//     ShelleyDBPath, and ConversationID are populated; OperationalContext is not used.
//   - Orchestrator subagents (GenerateOrchestratorSubagentSystemPrompt): only OperationalContext
//     is populated (it already contains pwd, git root, codebase info, etc.).
type SubagentSystemPromptData struct {
	WorkingDirectory   string
	GitInfo            *GitInfo
	ShelleyDBPath      string
	ConversationID     string // Parent conversation ID for querying user messages
	OperationalContext string // Rendered operational context (orchestrator subagents only)
}

// OrchestratorSystemPromptData contains data for orchestrator system prompts.
type OrchestratorSystemPromptData struct {
	WorkingDirectory           string
	GitInfo                    *GitInfo
	ContextDir                 string
	Codebase                   *CodebaseInfo
	ShelleyDBPath              string
	ConversationID             string // This conversation's ID for querying user messages
	IncludeConversationHistory bool   // Whether to include the sqlite query in operational context
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

	prompt := collapseBlankLines(buf.String())
	return runHook(hookSystemPrompt, prompt)
}

// renderOperationalContext renders the operational context template for the given working directory
// and conversation ID. If includeConversationHistory is true, the sqlite query for looking up
// user messages is included (useful for subagents, not needed by the orchestrator).
func renderOperationalContext(workingDir, conversationID string, includeConversationHistory bool) (string, error) {
	if workingDir == "" {
		var err error
		workingDir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
	}

	data := &OrchestratorSystemPromptData{
		WorkingDirectory:           workingDir,
		ShelleyDBPath:              DBPath,
		ConversationID:             conversationID,
		IncludeConversationHistory: includeConversationHistory,
	}

	if gitInfo, err := collectGitInfo(workingDir); err == nil {
		data.GitInfo = gitInfo
	}

	if codebaseInfo, err := collectCodebaseInfo(workingDir, data.GitInfo); err == nil {
		data.Codebase = codebaseInfo
	}

	tmpl, err := template.New("operational_context").Parse(operationalContextTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse operational context template: %w", err)
	}

	var buf strings.Builder
	if err = tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute operational context template: %w", err)
	}

	return collapseBlankLines(buf.String()), nil
}

// GenerateOrchestratorSystemPrompt generates the system prompt for orchestrator conversations.
// Operational context (without conversation history) is appended to the prompt.
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

	tmpl, err := template.New("orchestrator_system_prompt").Parse(orchestratorSystemPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse orchestrator template: %w", err)
	}

	var buf strings.Builder
	if err = tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute orchestrator template: %w", err)
	}

	operationalCtx, err := renderOperationalContext(wd, conversationID, false)
	if err != nil {
		return "", err
	}

	prompt := collapseBlankLines(buf.String() + "\n\n" + operationalCtx)
	return runHook(hookSystemPrompt, prompt)
}

// GenerateOrchestratorSubagentSystemPrompt generates the system prompt for
// subagents spawned by an orchestrator conversation.
func GenerateOrchestratorSubagentSystemPrompt(workingDir, parentConversationID string) (string, error) {
	operationalCtx, err := renderOperationalContext(workingDir, parentConversationID, true)
	if err != nil {
		return "", err
	}

	data := &SubagentSystemPromptData{
		OperationalContext: operationalCtx,
	}

	tmpl, err := template.New("orchestrator_subagent_system_prompt").Parse(orchestratorSubagentSystemPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse orchestrator subagent template: %w", err)
	}

	var buf strings.Builder
	if err = tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute orchestrator subagent template: %w", err)
	}

	prompt := collapseBlankLines(buf.String())
	return runHook(hookSystemPrompt, prompt)
}
