package server

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"shelley.exe.dev/exeenv"
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
	DefaultPort      int    // For exe.dev, the auto-routed HTTP port, 0 if unknown
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
	hookEndOfTurn       = "end-of-turn"
	hookChatMessage     = "chat-message"
)

// HookHeaders converts an http.Header to a sorted list of [name, value]
// pairs used in hook JSON payloads, stripping headers that routinely carry
// authentication secrets (Cookie, Set-Cookie, Authorization,
// Proxy-Authorization). Hooks are user-provided scripts on the filesystem
// and shouldn't see those. Multi-valued headers produce one pair per value,
// preserving value order. Returns nil if no headers remain so the
// `omitempty` JSON tag drops the field for non-HTTP callers.
func HookHeaders(h http.Header) [][2]string {
	if len(h) == 0 {
		return nil
	}
	names := make([]string, 0, len(h))
	for k := range h {
		switch http.CanonicalHeaderKey(k) {
		case "Cookie", "Set-Cookie", "Authorization", "Proxy-Authorization":
			continue
		}
		names = append(names, k)
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	out := make([][2]string, 0, len(names))
	for _, k := range names {
		for _, v := range h[k] {
			out = append(out, [2]string{k, v})
		}
	}
	return out
}

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
//	    "is_orchestrator": false
//	  }
//	}
//
// The hook should output the same top-level JSON shape (prompt, model, cwd, slug).
// Only the mutable fields are read from the output; "readonly" is ignored.
// Empty output means no changes. Unknown fields are ignored.
//
// If "slug" is set, it replaces Shelley's async LLM-generated slug for the new
// conversation. The slug is sanitized via slug.Sanitize before use; if the
// sanitized form is empty, or the slug collides with an existing one, Shelley
// falls back to its normal async slug generation.
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
	IsOrchestrator bool   `json:"is_orchestrator"`
	// Headers is the list of HTTP request headers from the incoming request
	// that triggered the new conversation, as [name, value] pairs sorted by
	// name. Multi-valued headers produce one pair per value. Empty for
	// subagent conversations and other non-HTTP entry points. Header names
	// are canonicalized by net/http (e.g., "X-Foo-Bar").
	Headers [][2]string `json:"headers,omitempty"`
}

// NewConversationHookResult contains the (possibly modified) mutable fields
// returned from the new-conversation hook.
type NewConversationHookResult struct {
	Prompt string
	Model  string
	Cwd    string
	Slug   string
}

// RunNewConversationHook runs the new-conversation hook from the
// default user hooks directory ($HOME/.config/shelley/hooks). Tests
// that want to invoke a hook script from a temp directory should
// call RunNewConversationHookIn directly, which avoids the
// process-wide $HOME env var (concurrent tests that share a Server
// would otherwise race on it).
func RunNewConversationHook(input NewConversationHookInput) (NewConversationHookResult, error) {
	return RunNewConversationHookIn(defaultHooksDir(), input)
}

// RunNewConversationHookIn is the dir-explicit variant of
// RunNewConversationHook. A non-nil error means the hook failed
// (non-zero exit, invalid JSON, etc.) and the caller should abort
// the operation. If no hook is installed, the input values are
// returned with a nil error.
func RunNewConversationHookIn(hooksDir string, input NewConversationHookInput) (NewConversationHookResult, error) {
	original := NewConversationHookResult{
		Prompt: input.Prompt,
		Model:  input.Model,
		Cwd:    input.Cwd,
	}

	hookPath, err := findHookIn(hooksDir, hookNewConversation)
	if err != nil {
		return original, fmt.Errorf("new-conversation hook: %w", err)
	}
	if hookPath == "" {
		return original, nil
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return original, fmt.Errorf("new-conversation hook: marshal input: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, hookPath)
	cmd.Stdin = strings.NewReader(string(inputJSON))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return original, fmt.Errorf("new-conversation hook %s failed: %w (stderr: %s)", hookPath, err, stderr.String())
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		// Empty output is fine — hook ran but has no overrides.
		return original, nil
	}

	// Parse only the mutable fields from the output.
	var hookOut struct {
		Prompt string `json:"prompt"`
		Model  string `json:"model"`
		Cwd    string `json:"cwd"`
		Slug   string `json:"slug"`
	}
	if err := json.Unmarshal([]byte(output), &hookOut); err != nil {
		return original, fmt.Errorf("new-conversation hook %s: invalid JSON output %q: %w", hookPath, output, err)
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
	if hookOut.Slug != "" {
		result.Slug = hookOut.Slug
	}

	if result != original {
		slog.Info(
			"new-conversation hook applied overrides",
			"cwdChanged", result.Cwd != original.Cwd,
			"promptChanged", result.Prompt != original.Prompt,
			"modelChanged", result.Model != original.Model,
			"slugChanged", result.Slug != original.Slug,
		)
	}

	return result, nil
}

// EndOfTurnHookInput is the JSON data passed to the end-of-turn hook on stdin.
// It mirrors the notifications.Event shape that drives end-of-turn notifications
// (notification channels, push notifications, conversation-hook webhooks), so a
// local hook can react to the same signal.
type EndOfTurnHookInput struct {
	Type           string    `json:"type"`
	ConversationID string    `json:"conversation_id"`
	Timestamp      time.Time `json:"timestamp"`

	// Payload fields, flattened from notifications.AgentDonePayload.
	Hostname        string `json:"hostname,omitempty"`
	Model           string `json:"model,omitempty"`
	Slug            string `json:"slug,omitempty"`
	ConversationURL string `json:"conversation_url,omitempty"`
	VMName          string `json:"vm_name,omitempty"`
	FinalResponse   string `json:"final_response,omitempty"`
}

// RunEndOfTurnHook fires the end-of-turn hook from the default user
// hooks directory ($HOME/.config/shelley/hooks). See RunEndOfTurnHookIn
// for the dir-explicit variant used by tests.
func RunEndOfTurnHook(input EndOfTurnHookInput) error {
	return RunEndOfTurnHookIn(defaultHooksDir(), input)
}

// RunEndOfTurnHookIn runs the end-of-turn hook from an explicit hooks
// directory. It runs the hook with the event JSON on stdin and ignores
// stdout. A non-nil error means the hook failed (non-zero exit, etc.);
// the caller decides whether to propagate it.
func RunEndOfTurnHookIn(hooksDir string, input EndOfTurnHookInput) error {
	hookPath, err := findHookIn(hooksDir, hookEndOfTurn)
	if err != nil {
		return fmt.Errorf("end-of-turn hook: %w", err)
	}
	if hookPath == "" {
		return nil
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("end-of-turn hook: marshal input: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, hookPath)
	cmd.Stdin = strings.NewReader(string(inputJSON))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("end-of-turn hook %s failed: %w (stderr: %s)", hookPath, err, stderr.String())
	}
	slog.Info("end-of-turn hook applied", "hook", hookPath, "conversationID", input.ConversationID)
	return nil
}

// ChatMessageHookInput is the JSON data passed to the chat-message hook on stdin.
// It fires when a user posts a follow-up chat message to an existing conversation
// (i.e., not the first message of a new conversation; that path uses the
// new-conversation hook). The hook may rewrite the message text; other fields
// are read-only context.
type ChatMessageHookInput struct {
	// Mutable — the hook may rewrite this on stdout.
	Message string `json:"message"`

	// Readonly context.
	Readonly ChatMessageReadonly `json:"readonly"`
}

// ChatMessageReadonly is the readonly context for the chat-message hook.
type ChatMessageReadonly struct {
	ConversationID string      `json:"conversation_id"`
	Model          string      `json:"model"`
	ReasoningLevel string      `json:"reasoning_level"`
	Queued         bool        `json:"queued"`
	Headers        [][2]string `json:"headers,omitempty"`
}

// RunChatMessageHook runs the chat-message hook from the default user hooks
// directory. See RunChatMessageHookIn for the dir-explicit variant.
func RunChatMessageHook(input ChatMessageHookInput) (string, error) {
	return RunChatMessageHookIn(defaultHooksDir(), input)
}

// RunChatMessageHookIn runs the chat-message hook from an explicit hooks dir.
// On success with non-empty stdout containing JSON `{"message": ...}`, the
// hook output replaces the user message. A non-nil error means the hook
// failed (non-zero exit, invalid JSON, etc.) and the caller should abort
// the operation. If no hook is installed, the input message is returned
// unchanged with a nil error.
func RunChatMessageHookIn(hooksDir string, input ChatMessageHookInput) (string, error) {
	hookPath, err := findHookIn(hooksDir, hookChatMessage)
	if err != nil {
		return input.Message, fmt.Errorf("chat-message hook: %w", err)
	}
	if hookPath == "" {
		return input.Message, nil
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return input.Message, fmt.Errorf("chat-message hook: marshal input: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, hookPath)
	cmd.Stdin = strings.NewReader(string(inputJSON))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return input.Message, fmt.Errorf("chat-message hook %s failed: %w (stderr: %s)", hookPath, err, stderr.String())
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		return input.Message, nil
	}

	var hookOut struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(output), &hookOut); err != nil {
		return input.Message, fmt.Errorf("chat-message hook %s: invalid JSON output %q: %w", hookPath, output, err)
	}
	if hookOut.Message == "" || hookOut.Message == input.Message {
		return input.Message, nil
	}
	slog.Info("chat-message hook applied override", "hook", hookPath, "conversationID", input.Readonly.ConversationID)
	return hookOut.Message, nil
}

// validSlashCommandName matches simple slash-command names.
var validSlashCommandName = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_-]*$`)

// SlashCommandHookInput is the JSON data passed to a slash-command hook on stdin.
// A slash command is any user message whose first token (after a leading slash)
// matches an executable hook at ~/.config/shelley/hooks/slash/<command>.
type SlashCommandHookInput struct {
	Command           string `json:"command"`
	Args              string `json:"args"`
	RawMessage        string `json:"raw_message"`
	ConversationID    string `json:"conversation_id"`
	IsNewConversation bool   `json:"is_new_conversation"`
	Cwd               string `json:"cwd,omitempty"`
	Model             string `json:"model,omitempty"`
	UserEmail         string `json:"user_email,omitempty"`
	IsOrchestrator    bool   `json:"is_orchestrator,omitempty"`
}

// SlashCommandHookResult describes the effect of a slash-command hook.
type SlashCommandHookResult struct {
	// Handled is true iff a matching hook was found and executed successfully.
	// If false, callers should treat the message as a normal user message.
	Handled bool
	// Message, if non-empty, replaces the original user message. If empty
	// while Handled is true, the message has been fully handled by the hook
	// and no agent turn should be triggered.
	Message string
	// Err, if non-nil, indicates the hook was found but failed.
	Err error
}

// RunSlashCommandHook checks whether input.RawMessage looks like a slash
// command ("/<name> ...") and, if so, looks up a matching hook at
// ~/.config/shelley/hooks/slash/<name>. If the hook exists and is executable,
// it is run with input JSON on stdin; its stdout becomes the replacement
// user-message text.
//
// If the message does not start with a slash, or the first token is not a
// valid hook name, or no matching hook exists, the result has Handled=false.
func RunSlashCommandHook(input SlashCommandHookInput) SlashCommandHookResult {
	msg := input.RawMessage
	if !strings.HasPrefix(msg, "/") {
		return SlashCommandHookResult{}
	}
	// Strip leading slash and split into command + args on first whitespace.
	rest := msg[1:]
	var cmd, args string
	if i := strings.IndexAny(rest, " \t\n"); i >= 0 {
		cmd = rest[:i]
		args = strings.TrimLeft(rest[i:], " \t\n")
	} else {
		cmd = rest
	}
	if !validSlashCommandName.MatchString(cmd) {
		return SlashCommandHookResult{}
	}

	hookPath, err := findHook("slash/" + cmd)
	if err != nil {
		slog.Error("slash-command hook: findHook failed", "command", cmd, "error", err)
		return SlashCommandHookResult{}
	}
	if hookPath == "" {
		return SlashCommandHookResult{}
	}

	input.Command = cmd
	input.Args = args

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return SlashCommandHookResult{Handled: true, Err: fmt.Errorf("marshal slash hook input: %w", err)}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmdRun := exec.CommandContext(ctx, hookPath)
	cmdRun.Stdin = bytes.NewReader(inputJSON)
	var stdout, stderr bytes.Buffer
	cmdRun.Stdout = &stdout
	cmdRun.Stderr = &stderr
	// Surface useful context via environment too, for hooks that prefer env
	// over parsing JSON. The JSON on stdin remains the source of truth.
	cmdRun.Env = append(
		os.Environ(),
		"SHELLEY_SLASH_COMMAND="+cmd,
		"SHELLEY_SLASH_ARGS="+args,
		"SHELLEY_CONVERSATION_ID="+input.ConversationID,
		"SHELLEY_CWD="+input.Cwd,
		"SHELLEY_MODEL="+input.Model,
		"SHELLEY_USER_EMAIL="+input.UserEmail,
	)

	if err := cmdRun.Run(); err != nil {
		slog.Error("slash-command hook failed", "hook", hookPath, "error", err, "stderr", stderr.String())
		return SlashCommandHookResult{Handled: true, Err: fmt.Errorf("slash hook %s failed: %w (stderr: %s)", cmd, err, strings.TrimSpace(stderr.String()))}
	}

	out := stdout.String()
	slog.Info("slash-command hook applied", "command", cmd, "hook", hookPath, "argsLen", len(args), "replyLen", len(out))
	return SlashCommandHookResult{Handled: true, Message: out}
}

// defaultHooksDir is $HOME/.config/shelley/hooks, or "" if $HOME is
// not set. Resolved on each call so that, e.g., a test that swaps
// $HOME locally still sees its change.
func defaultHooksDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "shelley", "hooks")
}

// findHook is a thin wrapper around findHookIn for the default hooks dir.
func findHook(name string) (string, error) {
	return findHookIn(defaultHooksDir(), name)
}

// findHookIn returns the path to the named hook inside dir if it
// exists and is executable, or "" if not found. Name may be a single
// segment ("foo") or a two-segment path ("slash/foo") for namespaced hooks.
func findHookIn(dir, name string) (string, error) {
	parts := strings.Split(name, "/")
	if len(parts) < 1 || len(parts) > 2 {
		return "", fmt.Errorf("invalid hook name: %q", name)
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, `\`) {
			return "", fmt.Errorf("invalid hook name: %q", name)
		}
	}
	if dir == "" {
		return "", nil
	}
	hookPath := filepath.Join(append([]string{dir}, parts...)...)
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

	// collectGitInfo shells out to `git rev-parse`; resolve it first so the
	// codebase and skill walks below can scope to the git root.
	gitInfo, err := collectGitInfo(wd)
	if err == nil {
		data.GitInfo = gitInfo
	}
	var gitRoot string
	if gitInfo != nil {
		gitRoot = gitInfo.Root
	}

	// Check if running on exe.dev (cheap stat).
	data.IsExeDev = isExeDev()

	// The codebase-info and skill walks each traverse the project tree,
	// stat'ing every directory and (for codebase info) reading guidance files.
	// They are independent and dominate Hydrate's wall time — measured ~50ms
	// each under -race on a moderately sized repo, more on loaded CI workers.
	// Run them concurrently; the slowest of the two becomes the floor instead
	// of their sum.
	var (
		codebaseInfo *CodebaseInfo
		codebaseErr  error
		skillsXML    string
		wg           sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		codebaseInfo, codebaseErr = collectCodebaseInfo(wd, gitInfo)
	}()
	go func() {
		defer wg.Done()
		skillsXML = collectSkills(wd, gitRoot, skills.Env{ExeDev: data.IsExeDev})
	}()

	// Run the remaining cheap synchronous probes while the walks are in flight.
	data.IsSudoAvailable = isSudoAvailable()
	if data.IsExeDev {
		if hostname, err := os.Hostname(); err == nil {
			// If hostname doesn't contain dots, add .exe.xyz suffix
			if !strings.Contains(hostname, ".") {
				hostname = hostname + ".exe.xyz"
			}
			data.Hostname = hostname
		}
		data.DefaultPort = exeDevDefaultPort()
	}

	wg.Wait()
	if codebaseErr == nil {
		data.Codebase = codebaseInfo
	}
	data.SkillsXML = skillsXML

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

// exeDevDefaultPort returns the live HTTP proxy port for this VM, fetched
// via the default "reflection" integration. Returns 0 if unavailable
// (integration disabled/detached, network error, etc.).
var exeDevDefaultPortHTTPClient = http.DefaultClient

func exeDevDefaultPort() int {
	env, err := exeenv.Current()
	if err != nil {
		return 0
	}
	return exeDevDefaultPortIn(env)
}

func exeDevDefaultPortIn(env exeenv.Environment) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", env.ReflectionURL()+"/default_port", nil)
	if err != nil {
		return 0
	}
	resp, err := exeDevDefaultPortHTTPClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var body struct {
		DefaultPort int `json:"default_port"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0
	}
	return body.DefaultPort
}

// collectSkills discovers skills from default directories, project .skills dirs,
// the project tree, and built-in skills. See skills.ListAll for precedence rules.
// Skills with a `when:` clause are filtered against env.
func collectSkills(workingDir, gitRoot string, env skills.Env) string {
	return skills.ToPromptXML(skills.Filter(skills.ListAll(workingDir, gitRoot), env))
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
	SkillsXML          string // XML block for available skills
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

	// Collect skills
	gitRoot := ""
	if gitInfo != nil {
		gitRoot = gitInfo.Root
	}
	data.SkillsXML = collectSkills(wd, gitRoot, skills.Env{ExeDev: isExeDev()})

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
	wd := workingDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
	}

	operationalCtx, err := renderOperationalContext(wd, parentConversationID, true)
	if err != nil {
		return "", err
	}

	// Collect git info for skills
	gitInfo, _ := collectGitInfo(wd)
	gitRoot := ""
	if gitInfo != nil {
		gitRoot = gitInfo.Root
	}

	data := &SubagentSystemPromptData{
		OperationalContext: operationalCtx,
		SkillsXML:          collectSkills(wd, gitRoot, skills.Env{ExeDev: isExeDev()}),
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
