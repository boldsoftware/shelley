package claudetool

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/imageutil"
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
Use this for simple LLM tasks like summarization, extraction, classification, reformatting,
or image analysis with a vision-capable model.
Use a subagent instead when the task requires tools, multiple steps, or iterative follow-up.
Keep the request focused and ask for only the level of detail needed.

Pass short prompts directly in prompt. For large or generated prompts, use prompt_file.
Exactly one of prompt or prompt_file is required; prompt_file must contain UTF-8 text.
Attachments add image files to the request and require a vision-capable model.
Do not use an image as prompt_file.
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
      "description": "LLM model to use. Select the model best suited to the request, considering the task's required capabilities and complexity. Defaults to the conversation's current model.",
      "enum": [%s]
    }`, strings.Join(enumItems, ", "))
	}

	return fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "prompt": {
      "type": "string",
      "description": "Prompt text to send. Use prompt_file instead for large or generated prompts."
    },
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
    },
    "attachments": {
      "type": "array",
      "description": "Paths to image files to attach to the prompt. Relative paths are resolved from the working directory. The selected model must support images.",
      "items": { "type": "string" }
    }%s
  }
}`, modelProp)
}

type llmOneShotInput struct {
	Prompt       string   `json:"prompt,omitempty"`
	PromptFile   string   `json:"prompt_file,omitempty"`
	OutputFile   string   `json:"output_file,omitempty"`
	Model        string   `json:"model,omitempty"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Attachments  []string `json:"attachments,omitempty"`
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
	if req.Prompt == "" && req.PromptFile == "" {
		return llm.ErrorfToolOut("exactly one of prompt or prompt_file is required")
	}
	if req.Prompt != "" && req.PromptFile != "" {
		return llm.ErrorfToolOut("prompt and prompt_file are mutually exclusive")
	}

	wd := t.WorkingDir.Get()
	prompt := req.Prompt
	if req.PromptFile != "" {
		promptPath := req.PromptFile
		if !filepath.IsAbs(promptPath) {
			promptPath = filepath.Join(wd, promptPath)
		}
		promptBytes, err := os.ReadFile(promptPath)
		if err != nil {
			return llm.ErrorfToolOut("failed to read prompt file: %w", err)
		}
		if !utf8.Valid(promptBytes) || bytes.IndexByte(promptBytes, 0) >= 0 {
			return llm.ErrorfToolOut("prompt_file must be a UTF-8 text file; pass image files in attachments")
		}
		prompt = string(promptBytes)
		if strings.TrimSpace(prompt) == "" {
			return llm.ErrorfToolOut("prompt file is empty")
		}
	} else if strings.TrimSpace(prompt) == "" {
		return llm.ErrorfToolOut("prompt is empty")
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
	if len(req.Attachments) > 0 && !svc.SupportsImages() {
		return llm.ErrorfToolOut("model %q does not support image attachments", modelID)
	}

	// Build the request
	message := llm.UserStringMessage(prompt)
	for _, attachment := range req.Attachments {
		path := attachment
		if !filepath.IsAbs(path) {
			path = filepath.Join(wd, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return llm.ErrorfToolOut("failed to read attachment %q: %w", attachment, err)
		}
		prepared, err := imageutil.Prepare(data, path, svc.MaxImageDimension(), svc.MaxImageBytes())
		if err != nil {
			return llm.ErrorfToolOut("invalid attachment %q: %w", attachment, err)
		}
		message.Content = append(message.Content, llm.Content{
			Type:          llm.ContentTypeText,
			MediaType:     prepared.MediaType,
			Data:          base64.StdEncoding.EncodeToString(prepared.Data),
			DisplayWidth:  prepared.Width,
			DisplayHeight: prepared.Height,
		})
	}
	llmReq := &llm.Request{
		Messages: []llm.Message{message},
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
