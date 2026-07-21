package claudetool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
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

// OneShotImageDir is where llm_one_shot saves copies of image prompt files so
// the UI can display them via /api/read (the originals may live anywhere on
// disk, which /api/read must not serve).
const OneShotImageDir = "/tmp/shelley-oneshot-images"

// llmOneShotDescription builds the tool description, including model info when models are available.
func (t *LLMOneShotTool) llmOneShotDescription() string {
	base := `Send a one-shot prompt to an LLM and get a response.

Unlike subagents, this is a single request/response with no conversation history or tools.
Use this for simple LLM tasks like summarization, extraction, classification, reformatting,
or image analysis with a vision-capable model.

The prompt is read from files (to handle large inputs cleanly). prompt_files
is a list of paths: text files are concatenated in order, and image files
(png, jpeg, gif, webp, heic) are attached as images. Attaching images requires a
vision-capable model.
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
  "required": ["prompt_files"],
  "properties": {
    "prompt_files": {
      "type": ["array", "string"],
      "items": { "type": "string" },
      "description": "Paths to files for the prompt. Image files are attached as images. Relative paths resolved from working dir."
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

// stringOrList unmarshals from either a JSON string or an array of strings.
type stringOrList []string

func (s *stringOrList) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var one string
		if err := json.Unmarshal(data, &one); err != nil {
			return err
		}
		*s = stringOrList{one}
		return nil
	}
	return json.Unmarshal(data, (*[]string)(s))
}

type llmOneShotInput struct {
	PromptFiles  stringOrList `json:"prompt_files,omitempty"`
	OutputFile   string       `json:"output_file,omitempty"`
	Model        string       `json:"model,omitempty"`
	SystemPrompt string       `json:"system_prompt,omitempty"`
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

// isImageData reports whether data looks like an image file.
func isImageData(data []byte) bool {
	return imageutil.IsHEIC(data) || strings.HasPrefix(http.DetectContentType(data), "image/")
}

// saveOneShotImage writes a prepared image to OneShotImageDir so /api/read can
// serve it to the UI (mirroring how browser screenshots are surfaced). Returns
// the saved path, or "" on failure.
func saveOneShotImage(ctx context.Context, prepared imageutil.Prepared) string {
	if err := os.MkdirAll(OneShotImageDir, 0o755); err != nil {
		slog.WarnContext(ctx, "llm_one_shot: failed to create image dir", "error", err)
		return ""
	}
	ext := strings.TrimPrefix(prepared.MediaType, "image/")
	path := filepath.Join(OneShotImageDir, uuid.New().String()+"."+ext)
	if err := os.WriteFile(path, prepared.Data, 0o644); err != nil {
		slog.WarnContext(ctx, "llm_one_shot: failed to save image copy", "error", err)
		return ""
	}
	return path
}

func (t *LLMOneShotTool) run(ctx context.Context, req llmOneShotInput) llm.ToolOut {
	promptFiles := []string(req.PromptFiles)
	if len(promptFiles) == 0 {
		return llm.ErrorfToolOut("prompt_files is required")
	}

	wd := t.WorkingDir.Get()

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

	// Assemble the prompt: concatenate text files in order, attach images.
	var promptText strings.Builder
	var images []llm.Content
	var displayImages []map[string]any
	for _, pf := range promptFiles {
		path := pf
		if !filepath.IsAbs(path) {
			path = filepath.Join(wd, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return llm.ErrorfToolOut("failed to read prompt file: %w", err)
		}
		if isImageData(data) {
			if !svc.SupportsImages() {
				return llm.ErrorfToolOut("prompt file %q is an image, but model %q does not support image attachments", pf, modelID)
			}
			prepared, err := imageutil.Prepare(data, path, svc.MaxImageDimension(), svc.MaxImageBytes())
			if err != nil {
				return llm.ErrorfToolOut("invalid image %q: %w", pf, err)
			}
			images = append(images, llm.Content{
				Type:          llm.ContentTypeText,
				MediaType:     prepared.MediaType,
				Data:          base64.StdEncoding.EncodeToString(prepared.Data),
				DisplayWidth:  prepared.Width,
				DisplayHeight: prepared.Height,
			})
			// Save a copy so the UI can render the image via /api/read.
			// Failures are non-fatal: the request itself is unaffected.
			if saved := saveOneShotImage(ctx, prepared); saved != "" {
				displayImages = append(displayImages, map[string]any{
					"url":    "/api/read?path=" + url.QueryEscape(saved),
					"path":   pf,
					"width":  prepared.Width,
					"height": prepared.Height,
				})
			}
			continue
		}
		if !utf8.Valid(data) {
			return llm.ErrorfToolOut("prompt file %q is neither UTF-8 text nor a supported image format", pf)
		}
		if promptText.Len() > 0 && len(data) > 0 {
			promptText.WriteString("\n")
		}
		promptText.Write(data)
	}
	prompt := promptText.String()
	if strings.TrimSpace(prompt) == "" && len(images) == 0 {
		return llm.ErrorfToolOut("prompt is empty")
	}

	// Build the request
	message := llm.Message{Role: llm.MessageRoleUser}
	if strings.TrimSpace(prompt) != "" {
		message.Content = append(message.Content, llm.StringContent(prompt))
	}
	message.Content = append(message.Content, images...)
	llmReq := &llm.Request{
		Messages: []llm.Message{message},
	}
	if req.SystemPrompt != "" {
		llmReq.System = []llm.SystemContent{{Type: "text", Text: req.SystemPrompt}}
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

	var display any
	if len(displayImages) > 0 {
		display = map[string]any{"images": displayImages}
	}

	if outputPath != "" {
		if err := os.WriteFile(outputPath, []byte(resultText), 0o644); err != nil {
			return llm.ErrorfToolOut("failed to write output file: %w", err)
		}
		usage := fmt.Sprintf(" (model: %s, input_tokens: %d, output_tokens: %d)",
			modelID, resp.Usage.InputTokens, resp.Usage.OutputTokens)
		return llm.ToolOut{
			LLMContent: llm.TextContent(fmt.Sprintf("Response written to %s (%d bytes)%s", outputPath, len(resultText), usage)),
			Display:    display,
		}
	}

	usage := fmt.Sprintf("\n\n---\nmodel: %s, input_tokens: %d, output_tokens: %d",
		modelID, resp.Usage.InputTokens, resp.Usage.OutputTokens)
	return llm.ToolOut{
		LLMContent: llm.TextContent(resultText + usage),
		Display:    display,
	}
}
