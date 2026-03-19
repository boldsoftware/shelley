package claudetool

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"

	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/llm"
)

// WorkingDir is a thread-safe mutable working directory.
type MutableWorkingDir struct {
	mu  sync.RWMutex
	dir string
}

// NewMutableWorkingDir creates a new MutableWorkingDir with the given initial directory.
func NewMutableWorkingDir(dir string) *MutableWorkingDir {
	return &MutableWorkingDir{dir: dir}
}

// Get returns the current working directory.
func (w *MutableWorkingDir) Get() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.dir
}

// Set updates the working directory.
func (w *MutableWorkingDir) Set(dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dir = dir
}

// ToolSetConfig contains configuration for creating a ToolSet.
type ToolSetConfig struct {
	// WorkingDir is the initial working directory for tools.
	WorkingDir string
	// LLMProvider provides access to LLM services for tool validation.
	LLMProvider LLMServiceProvider
	// EnableJITInstall enables just-in-time tool installation.
	EnableJITInstall bool
	// EnableBrowser enables browser tools.
	EnableBrowser bool
	// ModelID is the model being used for this conversation.
	// Used to determine tool configuration (e.g., simplified patch schema for weaker models).
	ModelID string
	// OnWorkingDirChange is called when the working directory changes.
	// This can be used to persist the change to a database.
	OnWorkingDirChange func(newDir string)
	// SubagentRunner is the runner for subagent conversations.
	// If set, the subagent tool will be available.
	SubagentRunner SubagentRunner
	// SubagentDB is the database for subagent conversations.
	SubagentDB SubagentDB
	// ParentConversationID is the ID of the parent conversation (for subagent tool).
	ParentConversationID string
	// ConversationID is the ID of the conversation these tools belong to.
	// This is exposed to bash commands via the SHELLEY_CONVERSATION_ID environment variable.
	ConversationID string
	// SubagentDepth is the nesting depth of this conversation.
	// 0 = top-level conversation, 1 = subagent, 2 = sub-subagent, etc.
	SubagentDepth int
	// MaxSubagentDepth is the maximum nesting depth for subagents.
	// Subagent tool is only available when SubagentDepth < MaxSubagentDepth.
	// A value of 0 means no limit (but SubagentRunner/SubagentDB must still be set).
	// Set to 1 to allow only top-level conversations (depth 0) to spawn subagents.
	MaxSubagentDepth int
	// AvailableModels is the list of models the subagent can choose from.
	// If nil, the list is built from LLMProvider.GetAvailableModels().
	AvailableModels []AvailableModel
}

// ToolSet holds a set of tools for a single conversation.
// Each conversation should have its own ToolSet.
type ToolSet struct {
	tools   []*llm.Tool
	cleanup func()
	wd      *MutableWorkingDir
}

// Tools returns the tools in this set.
func (ts *ToolSet) Tools() []*llm.Tool {
	return ts.tools
}

// Cleanup releases resources held by the tools (e.g., browser).
func (ts *ToolSet) Cleanup() {
	if ts.cleanup != nil {
		ts.cleanup()
	}
}

// WorkingDir returns the shared working directory.
func (ts *ToolSet) WorkingDir() *MutableWorkingDir {
	return ts.wd
}

// OrchestratorToolSetConfig contains configuration for creating an orchestrator ToolSet.
type OrchestratorToolSetConfig struct {
	// ContextDir is the shared context directory for subagent coordination.
	ContextDir string
	// SubagentRunner is the runner for subagent conversations.
	SubagentRunner SubagentRunner
	// SubagentDB is the database for subagent conversations.
	SubagentDB SubagentDB
	// ParentConversationID is the ID of the conversation.
	ParentConversationID string
	// ModelID is the model being used for this conversation.
	ModelID string
	// LLMProvider provides access to LLM services.
	LLMProvider LLMServiceProvider
	// AvailableModels is the list of models the subagent can choose from.
	AvailableModels []AvailableModel
	// WorkingDir is the initial working directory.
	WorkingDir string
	// OnWorkingDirChange is called when change_dir changes the working directory.
	OnWorkingDirChange func(newDir string)
	// EnableBrowser enables browser tools (for read_image / screenshot viewing).
	EnableBrowser bool
	// CLIAgent, if non-empty, uses a CLI subagent tool instead of native subagent.
	// Valid values: "claude-cli", "codex-cli".
	CLIAgent string
}

// NewOrchestratorToolSet creates a reduced tool set for orchestrator mode.
// It includes: subagent, read_context_file, output_iframe, change_dir, and read_image (from browser tools).
// NOTE: keyword_search is intentionally excluded — the orchestrator delegates search to subagents.
func NewOrchestratorToolSet(ctx context.Context, cfg OrchestratorToolSetConfig) *ToolSet {
	workingDir := cfg.WorkingDir
	if workingDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			workingDir = home
		} else {
			workingDir = "/"
		}
	}
	wd := NewMutableWorkingDir(workingDir)

	// Ensure context directory exists
	if cfg.ContextDir != "" {
		if err := os.MkdirAll(cfg.ContextDir, 0o755); err != nil {
			slog.Error("failed to create orchestrator context directory", "path", cfg.ContextDir, "error", err)
		}
	}

	var tools []*llm.Tool

	// Change dir tool (read-only navigation)
	changeDirTool := &ChangeDirTool{
		WorkingDir: wd,
		OnChange:   cfg.OnWorkingDirChange,
	}
	tools = append(tools, changeDirTool.Tool())

	// Read context file tool
	if cfg.ContextDir != "" {
		readCtxTool := &ReadContextFileTool{ContextDir: cfg.ContextDir}
		tools = append(tools, readCtxTool.Tool())
	}

	// Output iframe tool (for showing visualizations to user)
	outputIframeTool := &OutputIframeTool{WorkingDir: wd}
	tools = append(tools, outputIframeTool.Tool())

	// Build available models list
	availableModels := cfg.AvailableModels
	if availableModels == nil && cfg.LLMProvider != nil {
		for _, id := range cfg.LLMProvider.GetAvailableModels() {
			availableModels = append(availableModels, AvailableModel{ID: id})
		}
	}

	// Subagent tool: use CLI subagent if CLIAgent is a CLI agent, otherwise native subagent.
	// CLIAgent of "" or "shelley" means use the native Shelley subagent.
	if cfg.CLIAgent != "" && cfg.CLIAgent != "shelley" {
		cliSubagentTool := &CLISubagentTool{
			CLIAgent:   cfg.CLIAgent,
			WorkingDir: wd,
		}
		tools = append(tools, cliSubagentTool.Tool())
	} else if cfg.SubagentRunner != nil && cfg.SubagentDB != nil && cfg.ParentConversationID != "" {
		subagentTool := &SubagentTool{
			DB:                   cfg.SubagentDB,
			ParentConversationID: cfg.ParentConversationID,
			WorkingDir:           wd,
			Runner:               cfg.SubagentRunner,
			ModelID:              cfg.ModelID,
			AvailableModels:      availableModels,
		}
		tools = append(tools, subagentTool.Tool())
	}

	// Browser tools for read_image (screenshot viewing)
	var cleanup func()
	if cfg.EnableBrowser {
		maxImageDimension := 0
		if cfg.LLMProvider != nil && cfg.ModelID != "" {
			if svc, err := cfg.LLMProvider.GetService(cfg.ModelID); err == nil {
				maxImageDimension = svc.MaxImageDimension()
			}
		}
		browserTools, browserCleanup := browse.RegisterBrowserTools(ctx, maxImageDimension)
		// Only include read_image from browser tools, not the full browser
		for _, bt := range browserTools {
			if bt.Name == "read_image" {
				tools = append(tools, bt)
			}
		}
		cleanup = browserCleanup
	}

	return &ToolSet{
		tools:   tools,
		cleanup: cleanup,
		wd:      wd,
	}
}

// NewToolSet creates a new set of tools for a conversation.
// isStrongModel returns true for models that can handle complex tool schemas.
func isStrongModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	return strings.Contains(lower, "sonnet") || strings.Contains(lower, "opus")
}

func NewToolSet(ctx context.Context, cfg ToolSetConfig) *ToolSet {
	workingDir := cfg.WorkingDir
	if workingDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			workingDir = home
		} else {
			workingDir = "/"
		}
	}
	wd := NewMutableWorkingDir(workingDir)

	bashTool := &BashTool{
		WorkingDir:       wd,
		LLMProvider:      cfg.LLMProvider,
		EnableJITInstall: cfg.EnableJITInstall,
		ConversationID:   cfg.ConversationID,
	}

	// Use simplified patch schema for weaker models, full schema for sonnet/opus
	simplified := !isStrongModel(cfg.ModelID)
	patchTool := &PatchTool{
		Simplified:       simplified,
		WorkingDir:       wd,
		ClipboardEnabled: true,
	}

	keywordTool := NewKeywordToolWithWorkingDir(cfg.LLMProvider, wd)

	changeDirTool := &ChangeDirTool{
		WorkingDir: wd,
		OnChange:   cfg.OnWorkingDirChange,
	}

	outputIframeTool := &OutputIframeTool{WorkingDir: wd}

	tools := []*llm.Tool{
		bashTool.Tool(),
		patchTool.Tool(),
		keywordTool.Tool(),
		changeDirTool.Tool(),
		outputIframeTool.Tool(),
	}

	// Build the available models list (shared by subagent and llm_one_shot tools).
	availableModels := cfg.AvailableModels
	if availableModels == nil && cfg.LLMProvider != nil {
		for _, id := range cfg.LLMProvider.GetAvailableModels() {
			availableModels = append(availableModels, AvailableModel{ID: id})
		}
	}

	// Add subagent tool if configured and depth limit not reached.
	// MaxSubagentDepth of 0 means no limit; otherwise, only add if depth < max.
	canSpawnSubagents := cfg.SubagentRunner != nil && cfg.SubagentDB != nil && cfg.ParentConversationID != ""
	if canSpawnSubagents && (cfg.MaxSubagentDepth == 0 || cfg.SubagentDepth < cfg.MaxSubagentDepth) {
		subagentTool := &SubagentTool{
			DB:                   cfg.SubagentDB,
			ParentConversationID: cfg.ParentConversationID,
			WorkingDir:           wd,
			Runner:               cfg.SubagentRunner,
			ModelID:              cfg.ModelID, // Inherit parent's model
			AvailableModels:      availableModels,
		}
		tools = append(tools, subagentTool.Tool())
	}

	// Add LLM one-shot tool if LLM provider is configured
	if cfg.LLMProvider != nil {
		llmOneShotTool := &LLMOneShotTool{
			LLMProvider:     cfg.LLMProvider,
			ModelID:         cfg.ModelID,
			WorkingDir:      wd,
			AvailableModels: availableModels,
		}
		tools = append(tools, llmOneShotTool.Tool())
	}

	var cleanup func()
	if cfg.EnableBrowser {
		// Get max image dimension from the LLM service
		maxImageDimension := 0
		if cfg.LLMProvider != nil && cfg.ModelID != "" {
			if svc, err := cfg.LLMProvider.GetService(cfg.ModelID); err == nil {
				maxImageDimension = svc.MaxImageDimension()
			}
		}
		browserTools, browserCleanup := browse.RegisterBrowserTools(ctx, maxImageDimension)
		if len(browserTools) > 0 {
			tools = append(tools, browserTools...)
		}
		cleanup = browserCleanup
	}

	return &ToolSet{
		tools:   tools,
		cleanup: cleanup,
		wd:      wd,
	}
}
