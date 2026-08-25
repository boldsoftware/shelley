package claudetool

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/llm"
)

// SubagentRunner is the interface for running a subagent conversation.
// This is implemented by the server package to avoid import cycles.
type SubagentRunner interface {
	// RunSubagent runs a subagent conversation and returns the last response.
	// If wait is false, it starts processing in background and returns immediately.
	// timeout is the maximum time to wait for a response.
	// modelID is the model to use for the subagent.
	// reasoning is the user-facing reasoning/thinking level for the subagent
	// (one of "off", "minimal", "low", "medium", "high", "xhigh", "max");
	// an empty string means "use the service/conversation default".
	RunSubagent(ctx context.Context, conversationID, prompt string, wait bool, timeout time.Duration, modelID, reasoning string) (string, error)
}

// subagentReasoningLevels are the user-facing reasoning/thinking levels a
// subagent may be given via the "reasoning" parameter.
var subagentReasoningLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// isValidReasoningLevel reports whether s is a recognized reasoning level.
func isValidReasoningLevel(s string) bool {
	for _, l := range subagentReasoningLevels {
		if s == l {
			return true
		}
	}
	return false
}

// AvailableModel describes a model available for subagent use.
type AvailableModel struct {
	ID          string // The model identifier to pass as the "model" parameter
	DisplayName string // Human-readable name (may equal ID)
}

// SubagentDB is the database interface for subagent operations.
// This is implemented by the db package.
type SubagentDB interface {
	// GetOrCreateSubagentConversation retrieves or creates a subagent conversation.
	// Returns the conversation ID and the actual slug used (may differ from requested
	// slug if a numeric suffix was added for uniqueness).
	GetOrCreateSubagentConversation(ctx context.Context, slug, parentID, cwd string) (conversationID, actualSlug string, err error)
}

// SubagentTool provides the ability to spawn and interact with subagent conversations.
type SubagentTool struct {
	DB                   SubagentDB
	ParentConversationID string
	WorkingDir           *MutableWorkingDir
	Runner               SubagentRunner
	ModelID              string           // Parent conversation's model ID (default for subagents)
	AvailableModels      []AvailableModel // Models the agent can choose from
	// ParentReasoning is the parent conversation's user-facing reasoning level
	// (one of "off", "minimal", "low", "medium", "high", "xhigh", "max",
	// or "" for the service default). Subagents inherit this when the "reasoning"
	// parameter is not specified.
	ParentReasoning string
	slugLocks       sync.Map // map[string]*sync.Mutex
}

func (s *SubagentTool) lockSlug(slug string) func() {
	lockValue, _ := s.slugLocks.LoadOrStore(slug, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

const subagentName = "subagent"

const (
	// subagentDefaultTimeout is how long a wait=true call blocks before
	// returning a progress summary while the subagent keeps running.
	subagentDefaultTimeout = 15 * time.Minute
	// subagentMaxTimeout caps an explicit timeout_seconds.
	subagentMaxTimeout = 60 * time.Minute
)

// subagentDescription builds the tool description, including model info when models are available.
func (s *SubagentTool) subagentDescription() string {
	base := `Spawn or interact with a subagent conversation.

Subagents are independent conversations that can work on subtasks in parallel.
Use subagents for:
- Long-running tasks that you want to delegate
- Token-intensive tasks that produce lots of output, little of which is needed
- Parallel exploration of different approaches
- Breaking down complex problems into independent pieces

Each subagent has its own slug identifier within this conversation.
You can send messages to existing subagents by using the same slug.
The tool returns the subagent's last response, or a status if the timeout is reached.

When writing prompts for subagents, convey intent, nuance, and operational
details — not just prescriptive instructions. The subagent has no context
beyond what you put in the prompt, so share the "why" alongside the "what".

Use the "reasoning" parameter to set the subagent's thinking effort (off,
minimal, low, medium, high, xhigh, max). If omitted, the subagent inherits the
parent conversation's reasoning level.`

	if len(s.AvailableModels) > 0 {
		base += "\n\nAvailable models (use the \"model\" parameter to override the default):"
		for _, m := range s.AvailableModels {
			if m.DisplayName != "" && m.DisplayName != m.ID {
				base += fmt.Sprintf("\n- %s (%s)", m.ID, m.DisplayName)
			} else {
				base += fmt.Sprintf("\n- %s", m.ID)
			}
		}
	}

	return base
}

// subagentInputSchema builds the JSON schema, including model enum when models are available.
func (s *SubagentTool) subagentInputSchema() string {
	modelProp := ""
	if len(s.AvailableModels) > 0 {
		// Build the enum array
		var enumItems []string
		for _, m := range s.AvailableModels {
			enumItems = append(enumItems, fmt.Sprintf("%q", m.ID))
		}
		modelProp = fmt.Sprintf(`,
    "model": {
      "type": "string",
      "description": "LLM model for the subagent. Defaults to the parent conversation's model.",
      "enum": [%s]
    }`, strings.Join(enumItems, ", "))
	}

	// reasoning is always available, regardless of the model catalog.
	var reasoningEnum []string
	for _, l := range subagentReasoningLevels {
		reasoningEnum = append(reasoningEnum, fmt.Sprintf("%q", l))
	}
	reasoningProp := fmt.Sprintf(`,
    "reasoning": {
      "type": "string",
      "description": "Reasoning/thinking effort level for the subagent. If omitted, the subagent inherits the parent conversation's reasoning level.",
      "enum": [%s]
    }`, strings.Join(reasoningEnum, ", "))

	return fmt.Sprintf(`{
  "type": "object",
  "required": ["slug", "prompt"],
  "properties": {
    "slug": {
      "type": "string",
      "description": "A short identifier for this subagent (e.g., 'research-api', 'test-runner')"
    },
    "prompt": {
      "type": "string",
      "description": "The message to send to the subagent"
    },
    "timeout_seconds": {
      "type": "integer",
      "description": "How long to wait for a synchronous response, in seconds (default: 900, max: 3600). Only applies when wait=true; ignored otherwise. If the subagent hasn't finished by this deadline, the tool returns a progress summary and the subagent keeps running in the background; its eventual completion will then be delivered asynchronously."
    },
    "wait": {
      "type": "boolean",
      "description": "Whether to wait for completion (default: true). If false, returns immediately; when the subagent eventually finishes, its response is delivered asynchronously. If wait=true and the subagent completes before timeout, no later asynchronous duplicate is delivered. Sending a new message to a subagent that is still working does NOT interrupt it: the message is queued and delivered after the current turn finishes."
    }%s%s
  }
}`, modelProp, reasoningProp)
}

type subagentInput struct {
	Slug           string `json:"slug"`
	Prompt         string `json:"prompt"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Wait           *bool  `json:"wait,omitempty"`
	Model          string `json:"model,omitempty"`
	Reasoning      string `json:"reasoning,omitempty"`
}

// Tool returns an llm.Tool for the subagent functionality.
func (s *SubagentTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        subagentName,
		Description: s.subagentDescription(),
		InputSchema: llm.MustSchema(s.subagentInputSchema()),
		Run:         llm.RunJSON(s.run),
	}
}

func (s *SubagentTool) run(ctx context.Context, req subagentInput) llm.ToolOut {
	// Validate slug
	if req.Slug == "" {
		return llm.ErrorfToolOut("slug is required")
	}
	req.Slug = sanitizeSlug(req.Slug)
	if req.Slug == "" {
		return llm.ErrorfToolOut("slug must contain alphanumeric characters")
	}

	if req.Prompt == "" {
		return llm.ErrorfToolOut("prompt is required")
	}

	unlockSlug := s.lockSlug(req.Slug)
	defer unlockSlug()

	// Set defaults. The default wait is generous (15 min) because subagents
	// commonly run review/analysis tasks that take several minutes; a short
	// timeout pushed the parent to "hurry" a still-working subagent, which
	// historically interrupted its turn. Hitting the timeout is not an error
	// — it returns a progress summary and the subagent keeps running, with its
	// eventual result delivered asynchronously.
	timeout := subagentDefaultTimeout
	if req.TimeoutSeconds > 0 {
		timeout = min(time.Duration(req.TimeoutSeconds)*time.Second, subagentMaxTimeout)
	}

	wait := true
	if req.Wait != nil {
		wait = *req.Wait
	}

	// Determine which model to use: explicit choice > parent's model
	modelID := s.ModelID
	if req.Model != "" {
		if len(s.AvailableModels) > 0 {
			found := false
			for _, m := range s.AvailableModels {
				if m.ID == req.Model {
					found = true
					break
				}
			}
			if !found {
				var ids []string
				for _, m := range s.AvailableModels {
					ids = append(ids, m.ID)
				}
				return llm.ErrorfToolOut("unknown model %q; available: %s", req.Model, strings.Join(ids, ", "))
			}
		}
		modelID = req.Model
	}

	// Determine reasoning level: explicit choice > parent's reasoning level.
	reasoning := s.ParentReasoning
	if req.Reasoning != "" {
		if !isValidReasoningLevel(req.Reasoning) {
			return llm.ErrorfToolOut("unknown reasoning level %q; available: %s", req.Reasoning, strings.Join(subagentReasoningLevels, ", "))
		}
		reasoning = req.Reasoning
	}

	// Get or create the subagent conversation
	conversationID, actualSlug, err := s.DB.GetOrCreateSubagentConversation(ctx, req.Slug, s.ParentConversationID, s.WorkingDir.Get())
	if err != nil {
		return llm.ErrorfToolOut("failed to get/create subagent conversation: %w", err)
	}

	// Use the runner to execute the subagent
	response, err := s.Runner.RunSubagent(ctx, conversationID, req.Prompt, wait, timeout, modelID, reasoning)
	if err != nil {
		return llm.ErrorfToolOut("subagent error: %w", err)
	}

	// Include actual slug in response if it differs from requested
	slugNote := ""
	if actualSlug != req.Slug {
		slugNote = fmt.Sprintf(" (Note: slug was changed to '%s' for uniqueness. Use '%s' for future messages to this subagent.)", actualSlug, actualSlug)
	}

	return llm.ToolOut{
		LLMContent: llm.TextContent(fmt.Sprintf("Subagent '%s' response:%s\n%s", actualSlug, slugNote, response)),
		Display: SubagentDisplayData{
			Slug:           actualSlug,
			ConversationID: conversationID,
		},
	}
}

// SubagentDisplayData is the display data sent to the UI for subagent tool results.
type SubagentDisplayData struct {
	Slug           string `json:"slug"`
	ConversationID string `json:"conversation_id"`
}

func sanitizeSlug(slug string) string {
	// Lowercase, keep alphanumeric and hyphens
	var result strings.Builder
	for _, r := range strings.ToLower(slug) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		} else if r == ' ' || r == '_' {
			result.WriteRune('-')
		}
	}
	// Remove consecutive hyphens and trim
	s := result.String()
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}
