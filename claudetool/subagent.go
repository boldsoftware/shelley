package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"shelley.exe.dev/llm"
)

// SubagentRunner is the interface for running a subagent conversation.
// This is implemented by the server package to avoid import cycles.
type SubagentRunner interface {
	// RunSubagent runs a subagent conversation and returns the last response.
	// If wait is false, it starts processing in background and returns immediately.
	// timeout is the maximum time to wait for a response.
	RunSubagent(ctx context.Context, conversationID, prompt string, wait bool, timeout time.Duration) (string, error)
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
}

const (
	subagentName        = "subagent"
	subagentDescription = `Spawn or interact with a subagent conversation.

Subagents are independent conversations that can work on subtasks in parallel.
Use subagents for:
- Long-running tasks that you want to delegate
- Token-intensive tasks that produce lots of output, little of which is needed
- Parallel exploration of different approaches
- Breaking down complex problems into independent pieces

Each subagent has its own slug identifier within this conversation.
You can send messages to existing subagents by using the same slug.
The tool returns the subagent's last response, or a status if the timeout is reached.
`
	subagentInputSchema = `
{
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
      "description": "How long to wait for a response (default: 60, max: 300)"
    },
    "wait": {
      "type": "boolean",
      "description": "Whether to wait for completion (default: true). If false, returns immediately."
    }
  }
}
`
)

type subagentInput struct {
	Slug           string `json:"slug"`
	Prompt         string `json:"prompt"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Wait           *bool  `json:"wait,omitempty"`
}

// Tool returns an llm.Tool for the subagent functionality.
func (s *SubagentTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        subagentName,
		Description: strings.TrimSpace(subagentDescription),
		InputSchema: llm.MustSchema(subagentInputSchema),
		Run:         s.Run,
	}
}

func (s *SubagentTool) Run(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var req subagentInput
	if err := json.Unmarshal(m, &req); err != nil {
		return llm.ErrorfToolOut("failed to parse subagent input: %w", err)
	}

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

	// Set defaults
	timeout := 60 * time.Second
	if req.TimeoutSeconds > 0 {
		if req.TimeoutSeconds > 300 {
			req.TimeoutSeconds = 300
		}
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}

	wait := true
	if req.Wait != nil {
		wait = *req.Wait
	}

	// Get or create the subagent conversation
	conversationID, actualSlug, err := s.DB.GetOrCreateSubagentConversation(ctx, req.Slug, s.ParentConversationID, s.WorkingDir.Get())
	if err != nil {
		return llm.ErrorfToolOut("failed to get/create subagent conversation: %w", err)
	}

	// Use the runner to execute the subagent
	response, err := s.Runner.RunSubagent(ctx, conversationID, req.Prompt, wait, timeout)
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
