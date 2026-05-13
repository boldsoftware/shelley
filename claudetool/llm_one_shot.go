package claudetool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"shelley.exe.dev/llm"
)

// LLMOneShotTool sends a one-shot prompt to an LLM and returns the result.
type LLMOneShotTool struct {
	LLMProvider     LLMServiceProvider
	ModelID         string // The conversation's current model ID (used as default)
	WorkingDir      *MutableWorkingDir
	AvailableModels []AvailableModel // Models the agent can choose from
}

const (
	llmOneShotName = "llm_one_shot"

	// Results longer than this are written to a file.
	llmOneShotMaxInlineLen = 4000
)

// llmOneShotDescription builds the tool description, including model info when models are available.
func (t *LLMOneShotTool) llmOneShotDescription() string {
	base := `Send a one-shot prompt to an LLM and get a response.

Unlike subagents, this is a single request/response with no conversation history or tools.
Use this for simple LLM tasks like summarization, extraction, classification, or reformatting.

The prompt is read from a file (to handle large inputs cleanly).
Short results are returned inline; long results are written to a file.`

	if len(t.AvailableModels) > 0 {
		base += "\n\nAvailable models (use the \"model\" parameter to override the default):"
		for _, m := range t.AvailableModels {
			if m.DisplayName != "" && m.DisplayName != m.ID {
				base += fmt.Sprintf("\n- %s (%s)", m.ID, m.DisplayName)
			} else {
				base += fmt.Sprintf("\n- %s", m.ID)
			}
		}
	}

	return base
}

// llmOneShotInputSchema builds the JSON schema, including model enum when models are available.
func (t *LLMOneShotTool) llmOneShotInputSchema() string {
	modelProp := ""
	if len(t.AvailableModels) > 0 {
		var enumItems []string
		for _, m := range t.AvailableModels {
			enumItems = append(enumItems, fmt.Sprintf("%q", m.ID))
		}
		modelProp = fmt.Sprintf(`,
    "model": {
      "type": "string",
      "description": "LLM model to use. Defaults to the conversation's current model.",
      "enum": [%s]
    }`, strings.Join(enumItems, ", "))
	}

	return fmt.Sprintf(`{
  "type": "object",
  "required": ["prompt_file"],
  "properties": {
    "prompt_file": {
      "type": "string",
      "description": "Path to a file containing the prompt to send. Relative paths are resolved from the working directory."
    },
    "output_file": {
      "type": "string",
      "description": "Path to write the response to. If omitted, short responses are returned inline and long responses are written to a temp file."
    },
    "system_prompt": {
      "type": "string",
      "description": "Optional system prompt to include."
    }%s
  }
}`, modelProp)
}

type llmOneShotInput struct {
	PromptFile   string `json:"prompt_file"`
	OutputFile   string `json:"output_file,omitempty"`
	Model        string `json:"model,omitempty"`
	SystemPrompt string `json:"system_prompt,omitempty"`
}

// Tool returns an llm.Tool for the LLM one-shot functionality.
func (t *LLMOneShotTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        llmOneShotName,
		Description: t.llmOneShotDescription(),
		InputSchema: llm.MustSchema(t.llmOneShotInputSchema()),
		Run:         llm.RunJSON(t.run),
	}
}

func (t *LLMOneShotTool) run(ctx context.Context, req llmOneShotInput) llm.ToolOut {
	if req.PromptFile == "" {
		return llm.ErrorfToolOut("prompt_file is required")
	}

	// Resolve paths relative to working directory
	wd := t.WorkingDir.Get()
	promptPath := req.PromptFile
	if !filepath.IsAbs(promptPath) {
		promptPath = filepath.Join(wd, promptPath)
	}

	// Read the prompt file
	promptBytes, err := os.ReadFile(promptPath)
	if err != nil {
		return llm.ErrorfToolOut("failed to read prompt file: %w", err)
	}
	prompt := string(promptBytes)
	if strings.TrimSpace(prompt) == "" {
		return llm.ErrorfToolOut("prompt file is empty")
	}

	// Determine which model to use: explicit choice > conversation's model
	modelID := t.ModelID
	if req.Model != "" {
		if len(t.AvailableModels) > 0 {
			found := false
			for _, am := range t.AvailableModels {
				if am.ID == req.Model {
					found = true
					break
				}
			}
			if !found {
				var ids []string
				for _, am := range t.AvailableModels {
					ids = append(ids, am.ID)
				}
				return llm.ErrorfToolOut("unknown model %q; available: %s", req.Model, strings.Join(ids, ", "))
			}
		}
		modelID = req.Model
	}
	if modelID == "" {
		return llm.ErrorfToolOut("no model specified and no default model configured")
	}

	if t.LLMProvider == nil {
		return llm.ErrorfToolOut("LLM provider not configured")
	}

	svc, err := t.LLMProvider.GetService(modelID)
	if err != nil {
		return llm.ErrorfToolOut("failed to get LLM service for model %q: %w", modelID, err)
	}

	// Build the request
	llmReq := &llm.Request{
		Messages: []llm.Message{
			llm.UserStringMessage(prompt),
		},
	}
	if req.SystemPrompt != "" {
		llmReq.System = []llm.SystemContent{{Text: req.SystemPrompt}}
	}

	// Send the request
	resp, err := svc.Do(ctx, llmReq)
	if err != nil {
		return llm.ErrorfToolOut("LLM request failed: %w", err)
	}

	// Extract text from the response
	var result strings.Builder
	for _, content := range resp.Content {
		if content.Type == llm.ContentTypeText {
			result.WriteString(content.Text)
		}
	}
	resultText := result.String()

	// Determine where to put the result
	outputPath := req.OutputFile
	if !filepath.IsAbs(outputPath) && outputPath != "" {
		outputPath = filepath.Join(wd, outputPath)
	}

	// If no explicit output file but result is long, write to temp file
	if outputPath == "" && len(resultText) > llmOneShotMaxInlineLen {
		f, err := os.CreateTemp(wd, "llm-result-*.txt")
		if err != nil {
			f, err = os.CreateTemp("", "llm-result-*.txt")
			if err != nil {
				return llm.ErrorfToolOut("failed to create temp file: %w", err)
			}
		}
		outputPath = f.Name()
		f.Close()
	}

	if outputPath != "" {
		if err := os.WriteFile(outputPath, []byte(resultText), 0o644); err != nil {
			return llm.ErrorfToolOut("failed to write output file: %w", err)
		}
		usage := fmt.Sprintf(" (model: %s, input_tokens: %d, output_tokens: %d)",
			modelID, resp.Usage.InputTokens, resp.Usage.OutputTokens)
		return llm.ToolOut{
			LLMContent: llm.TextContent(fmt.Sprintf("Response written to %s (%d bytes)%s", outputPath, len(resultText), usage)),
		}
	}

	usage := fmt.Sprintf("\n\n---\nmodel: %s, input_tokens: %d, output_tokens: %d",
		modelID, resp.Usage.InputTokens, resp.Usage.OutputTokens)
	return llm.ToolOut{
		LLMContent: llm.TextContent(resultText + usage),
	}
}
