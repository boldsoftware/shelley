package claudetool

import (
	"context"
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
	// ReasoningLevel is the parent conversation's user-facing reasoning/thinking
	// level (one of "off", "minimal", "low", "medium", "high", "xhigh", or ""
	// for the service default). Subagents inherit this when their "reasoning"
	// parameter is not specified.
	ReasoningLevel string
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
	// Env holds additional conversation context (slug, model, user email,
	// server port) exposed to bash/shell commands as SHELLEY_* environment
	// variables, matching the variables injected into interactive "!"
	// terminals. ConversationID above is authoritative for
	// SHELLEY_CONVERSATION_ID and overrides Env.ConversationID.
	Env ShelleyEnv
	// SubagentDepth is the nesting depth of this conversation.
	// 0 = top-level conversation, 1 = subagent, 2 = sub-subagent, etc.
	SubagentDepth int
	// MaxSubagentDepth is the maximum nesting depth for subagents.
	// Subagent tool is only available when SubagentDepth < MaxSubagentDepth.
	// A value of 0 means no limit (but SubagentRunner/SubagentDB must still be set).
	// Set to 1 to allow only top-level conversations (depth 0) to spawn subagents.
	MaxSubagentDepth int
	// BuildAvailableModels, if set, is called by NewToolSet to compute the
	// list of models that subagent / llm_one_shot tools can choose from.
	// It is invoked each time a ToolSet is built so new conversations pick
	// up custom models added at runtime, instead of being stuck with a
	// snapshot taken at server start. If nil, the list is built from
	// LLMProvider.GetAvailableModels() (without display names).
	BuildAvailableModels func() []AvailableModel
	// ExperienceStore persists task checkpoints and reusable project lessons.
	// If set, memory, task_journal, and dream tools are available.
	ExperienceStore ExperienceStore
	// ToolOverrides maps tool name to "on" or "off". Tools not listed use their default.
	ToolOverrides map[string]string
	// DisableAllTools disables every tool by default; ToolOverrides with "on" re-enable.
	DisableAllTools bool
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

// ServerSideWebSearchCapable is implemented by services that support
// server-side web search. Only certain services/models qualify: the OpenAI
// Responses API (not legacy Chat Completions), and genuine Anthropic Claude
// models (not other models reached over the Anthropic Messages wire protocol).
type ServerSideWebSearchCapable interface {
	SupportsServerSideWebSearch() bool
}

// serverSideTools returns server-side tools appropriate for the given service.
// Server-side tools are executed on the LLM provider's infrastructure.
func serverSideTools(svc llm.Service) []*llm.Tool {
	// Every service that can run server-side web search advertises it via the
	// optional ServerSideWebSearchCapable interface. Bail out otherwise so we
	// never send a web_search tool a model can't handle (e.g. a third-party
	// model served over the Anthropic Messages or OpenAI Chat Completions wire
	// protocol by an LLM integration).
	if c, ok := svc.(ServerSideWebSearchCapable); !ok || !c.SupportsServerSideWebSearch() {
		return nil
	}
	switch svc.Provider() {
	case "anthropic":
		return []*llm.Tool{
			{
				Name:       "web_search",
				Type:       "web_search_20250305",
				ServerSide: true,
			},
		}
	case "openai":
		return []*llm.Tool{
			{
				Name:       "web_search",
				Type:       "web_search",
				ServerSide: true,
			},
		}
	}
	return nil
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

	env := cfg.Env
	env.ConversationID = cfg.ConversationID

	bashTool := &BashTool{
		WorkingDir:       wd,
		LLMProvider:      cfg.LLMProvider,
		EnableJITInstall: cfg.EnableJITInstall,
		Env:              env,
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

	shellTool := &ShellTool{
		WorkingDir:       wd,
		LLMProvider:      cfg.LLMProvider,
		EnableJITInstall: cfg.EnableJITInstall,
		Env:              env,
		BackgroundCtx:    ctx,
	}

	tools := []*llm.Tool{
		bashTool.Tool(),
		shellTool.Tool(),
		patchTool.Tool(),
		keywordTool.Tool(),
		changeDirTool.Tool(),
		outputIframeTool.Tool(),
	}

	// Build the available models list (shared by subagent and llm_one_shot tools).
	// Resolved fresh on each ToolSet construction so new conversations see
	// custom models added since server start.
	var availableModels []AvailableModel
	if cfg.BuildAvailableModels != nil {
		availableModels = cfg.BuildAvailableModels()
	} else if cfg.LLMProvider != nil {
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
			ParentReasoning:      cfg.ReasoningLevel,
		}
		tools = append(tools, subagentTool.Tool())
	}

	if cfg.ExperienceStore != nil && cfg.ConversationID != "" {
		experience := &ExperienceTools{
			Store:          cfg.ExperienceStore,
			ConversationID: cfg.ConversationID,
			WorkingDir:     wd,
		}
		tools = append(tools, experience.MemoryTool(), experience.JournalTool(), experience.DreamTool())
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
	anyBrowserToolEnabled := false
	for _, name := range []string{"browser", "read_image"} {
		if IsToolEnabled(name, cfg.ToolOverrides, cfg.DisableAllTools) {
			anyBrowserToolEnabled = true
			break
		}
	}
	if cfg.EnableBrowser && anyBrowserToolEnabled {
		browserTools, browserCleanup := browse.RegisterBrowserTools(ctx)
		if len(browserTools) > 0 {
			// If the model doesn't support image inputs, drop read_image — it
			// returns image content the model cannot consume. The `browser`
			// tool's screenshot action also returns images, but it self-gates
			// at run time via llm.ServiceFromContext (see browse.screenshotRun),
			// so the combined browser tool stays available.
			modelSupportsImages := true
			if cfg.LLMProvider != nil && cfg.ModelID != "" {
				if svc, err := cfg.LLMProvider.GetService(cfg.ModelID); err == nil {
					modelSupportsImages = svc.SupportsImages()
				}
			}
			for _, bt := range browserTools {
				if bt.Name == "read_image" && !modelSupportsImages {
					continue
				}
				tools = append(tools, bt)
			}
		}
		cleanup = browserCleanup
	}

	// Add server-side tools (e.g., web search for Anthropic models, or for
	// OpenAI's Responses API).
	if cfg.LLMProvider != nil && cfg.ModelID != "" {
		if svc, err := cfg.LLMProvider.GetService(cfg.ModelID); err == nil {
			tools = append(tools, serverSideTools(svc)...)
		}
	}

	tools = FilterTools(tools, cfg.ToolOverrides, cfg.DisableAllTools)
	return &ToolSet{
		tools:   tools,
		cleanup: cleanup,
		wd:      wd,
	}
}
