package claudetool

import (
	"context"
	"strings"
	"sync"

	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/claudetool/lsp"
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
	// EnableCodeIntelligence enables LSP-based code intelligence tools.
	EnableCodeIntelligence bool
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

// NewToolSet creates a new set of tools for a conversation.
// isStrongModel returns true for models that can handle complex tool schemas.
func isStrongModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	return strings.Contains(lower, "sonnet") || strings.Contains(lower, "opus")
}

func NewToolSet(ctx context.Context, cfg ToolSetConfig) *ToolSet {
	workingDir := cfg.WorkingDir
	if workingDir == "" {
		workingDir = "/"
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

	// Add subagent tool if configured and depth limit not reached.
	// MaxSubagentDepth of 0 means no limit; otherwise, only add if depth < max.
	canSpawnSubagents := cfg.SubagentRunner != nil && cfg.SubagentDB != nil && cfg.ParentConversationID != ""
	if canSpawnSubagents && (cfg.MaxSubagentDepth == 0 || cfg.SubagentDepth < cfg.MaxSubagentDepth) {
		subagentTool := &SubagentTool{
			DB:                   cfg.SubagentDB,
			ParentConversationID: cfg.ParentConversationID,
			WorkingDir:           wd,
			Runner:               cfg.SubagentRunner,
		}
		tools = append(tools, subagentTool.Tool())
	}

	var cleanups []func()

	if cfg.EnableBrowser {
		// Get max image dimension from the LLM service
		maxImageDimension := 0
		if cfg.LLMProvider != nil && cfg.ModelID != "" {
			if svc, err := cfg.LLMProvider.GetService(cfg.ModelID); err == nil {
				maxImageDimension = svc.MaxImageDimension()
			}
		}
		browserTools, browserCleanup := browse.RegisterBrowserTools(ctx, true, maxImageDimension)
		if len(browserTools) > 0 {
			tools = append(tools, browserTools...)
		}
		cleanups = append(cleanups, browserCleanup)
	}

	if cfg.EnableCodeIntelligence {
		lspTools, lspCleanup := lsp.RegisterLSPTools(wd.Get)
		tools = append(tools, lspTools...)
		cleanups = append(cleanups, lspCleanup)
	}

	var cleanup func()
	if len(cleanups) > 0 {
		cleanup = func() {
			for _, fn := range cleanups {
				fn()
			}
		}
	}

	return &ToolSet{
		tools:   tools,
		cleanup: cleanup,
		wd:      wd,
	}
}
