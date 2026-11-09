package gem

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/gem/gemini"
	"shelley.exe.dev/models/modelsdev"
)

const (
	DefaultModel     = "gemini-3.8-flash"
	DefaultMaxTokens = 16384
	GeminiAPIKeyEnv  = "GEMINI_API_KEY"
)

// Service provides Gemini completions.
// Fields should not be altered concurrently with calling any method on Service.
type Service struct {
	HTTPC         *http.Client      // defaults to http.DefaultClient if nil
	URL           string            // Gemini API URL, uses the gemini package default if empty
	APIKey        string            // must be non-empty
	Model         string            // model name or models/name; defaults to DefaultModel if empty
	MaxTokens     int               // zero uses the model limit, or DefaultMaxTokens if unknown
	ThinkingLevel llm.ThinkingLevel // service-level default; zero (ThinkingLevelDefault) leaves thinkingConfig at the model default

	// ReasoningEffort overrides ThinkingLevel. Known models validate the value;
	// budget-based models translate it to tokens. Unknown models send it verbatim.
	ReasoningEffort string

	// SupportsImages_ controls whether this service advertises image-input support.
	// Default false (supports images).
	SupportsImages_ bool
}

var _ llm.Service = (*Service)(nil)

// These maps convert between Sketch's llm package and Gemini API formats
var fromLLMRole = map[llm.MessageRole]string{
	llm.MessageRoleAssistant: "model",
	llm.MessageRoleUser:      "user",
}

// convertToolSchemas preserves JSON schemas in Gemini function declarations.
func convertToolSchemas(tools []*llm.Tool) ([]gemini.FunctionDeclaration, error) {
	if len(tools) == 0 {
		return nil, nil
	}

	var decls []gemini.FunctionDeclaration
	for _, tool := range tools {
		var schemaJSON map[string]json.RawMessage
		if err := json.Unmarshal(tool.InputSchema, &schemaJSON); err != nil {
			return nil, fmt.Errorf("failed to unmarshal tool %s schema: %w", tool.Name, err)
		}
		if schemaJSON == nil {
			return nil, fmt.Errorf("tool %s schema must be a JSON object", tool.Name)
		}
		decls = append(decls, gemini.FunctionDeclaration{
			Name:                 tool.Name,
			Description:          tool.Description,
			ParametersJSONSchema: tool.InputSchema,
		})
	}

	return decls, nil
}

// buildGeminiRequest converts Sketch's llm.Request to Gemini's request format
func (s *Service) buildGeminiRequest(req *llm.Request) (*gemini.Request, error) {
	var err error
	req, err = prepareCitations(context.Background(), req)
	if err != nil {
		return nil, err
	}
	if s.MaxTokens < 0 {
		return nil, fmt.Errorf("max output tokens must not be negative: %d", s.MaxTokens)
	}
	gemReq := &gemini.Request{}

	// Add system instruction if provided
	if len(req.System) > 0 {
		// Combine all system messages into a single system instruction
		systemText := ""
		for i, sys := range req.System {
			if i > 0 && systemText != "" && sys.Text != "" {
				systemText += "\n"
			}
			systemText += sys.Text
		}

		if systemText != "" {
			gemReq.SystemInstruction = &gemini.Content{
				Parts: []gemini.Part{{Text: systemText}},
			}
		}
	}

	toolNamesByID := make(map[string]string)

	// Convert messages to Gemini content format
	for _, msg := range req.Messages {
		// Set the role based on the message role
		role, ok := fromLLMRole[msg.Role]
		if !ok {
			return nil, fmt.Errorf("unsupported message role: %v", msg.Role)
		}

		content := gemini.Content{
			Role: role,
		}

		// First pass: collect tool use IDs for correlation
		for _, c := range msg.Content {
			if c.Type == llm.ContentTypeToolUse && c.ID != "" {
				toolNamesByID[c.ID] = c.ToolName
			}
		}

		// Map each content item to Gemini's format
		for _, c := range msg.Content {
			if msg.Origin.Known() && msg.Origin.Provider != "gemini" {
				if c.Type == llm.ContentTypeThinking || c.Type == llm.ContentTypeRedactedThinking {
					continue
				}
				c.Signature = ""
			}
			if len(c.GeminiServerTool) != 0 {
				var part gemini.Part
				if err := json.Unmarshal(c.GeminiServerTool, &part); err != nil {
					return nil, fmt.Errorf("invalid Gemini server tool: %w", err)
				}
				if part.ToolCall == nil && part.ToolResponse == nil {
					return nil, errors.New("Gemini server tool has no call or response")
				}
				content.Parts = append(content.Parts, part)
				continue
			}
			if c.OpenAIResponsesReasoning != nil {
				continue
			}
			switch c.Type {
			case llm.ContentTypeText, llm.ContentTypeThinking, llm.ContentTypeRedactedThinking:
				// Image content is represented as ContentTypeText with MediaType + Data set.
				if c.Type == llm.ContentTypeText && c.MediaType != "" && c.Data != "" {
					content.Parts = append(content.Parts, gemini.Part{
						InlineData: &gemini.Blob{
							MimeType: c.MediaType,
							Data:     c.Data,
						},
					})
					continue
				}
				// Text or thinking content
				part := gemini.Part{}
				if c.Type == llm.ContentTypeThinking {
					// For thinking content, use the Thinking field and preserve the signature
					part.Text = c.Thinking
					part.ThoughtSignature = c.Signature
					part.Thought = true
				} else if c.Type == llm.ContentTypeRedactedThinking {
					// For redacted thinking, use the Data field (consistent with Anthropic pattern)
					part.Text = c.Data
					part.ThoughtSignature = c.Signature
					part.Thought = true
				} else {
					// Regular text. Gemini 3 may have attached a thoughtSignature to
					// the final-answer text — pass it back so reasoning state survives.
					part.Text = c.Text
					part.ThoughtSignature = c.Signature
				}
				content.Parts = append(content.Parts, part)
			case llm.ContentTypeToolUse:
				// Tool use becomes a function call
				var args map[string]any
				if err := json.Unmarshal(llm.ReplayToolInput(c.ToolInput), &args); err != nil {
					return nil, fmt.Errorf("failed to unmarshal tool input: %w", err)
				}

				// Make sure we have a valid ID for this tool use
				if c.ID == "" {
					c.ID = fmt.Sprintf("gemini_tool_%s_%d", c.ToolName, time.Now().UnixNano())
				}

				toolNamesByID[c.ID] = c.ToolName

				slog.DebugContext(context.Background(), "gemini_preparing_tool_use",
					"tool_name", c.ToolName,
					"tool_id", c.ID,
					"input", string(c.ToolInput),
					"thought_signature", c.Signature)

				content.Parts = append(content.Parts, gemini.Part{
					FunctionCall: &gemini.FunctionCall{
						ID:   c.ID,
						Name: c.ToolName,
						Args: args,
					},
					// Gemini 3 requires thought signatures to be passed back for function calls
					ThoughtSignature: c.Signature,
				})
			case llm.ContentTypeToolResult:
				// Tool result becomes a function response
				// Create a map for the response
				response := map[string]any{
					"error": c.ToolError,
				}

				// Handle tool results: Gemini only supports string results
				// Combine all text content into a single string
				var resultText string
				if len(c.ToolResult) > 0 {
					// Collect all text from content objects
					texts := make([]string, 0, len(c.ToolResult))
					for _, result := range c.ToolResult {
						if result.Text != "" {
							texts = append(texts, result.Text)
						}
					}
					resultText = strings.Join(texts, "\n")
				}
				response["result"] = resultText

				funcName := toolNamesByID[c.ToolUseID]
				if funcName == "" {
					funcName = c.ToolName
				}
				if funcName == "" {
					return nil, fmt.Errorf("missing tool name for result %q", c.ToolUseID)
				}

				slog.DebugContext(context.Background(), "gemini_preparing_tool_result",
					"tool_use_id", c.ToolUseID,
					"mapped_func_name", funcName,
					"result_count", len(c.ToolResult))

				content.Parts = append(content.Parts, gemini.Part{
					FunctionResponse: &gemini.FunctionResponse{
						ID:       c.ToolUseID,
						Name:     funcName,
						Response: response,
					},
				})

				// Images inside tool results aren't supported in FunctionResponse,
				// so we attach them as additional inlineData parts on the same content.
				for _, result := range c.ToolResult {
					if result.MediaType != "" && result.Data != "" {
						content.Parts = append(content.Parts, gemini.Part{
							InlineData: &gemini.Blob{
								MimeType: result.MediaType,
								Data:     result.Data,
							},
						})
					}
				}
			}
		}

		if len(content.Parts) > 0 {
			gemReq.Contents = append(gemReq.Contents, content)
		}
	}

	// Handle tools/functions
	if len(req.Tools) > 0 {
		var functions []*llm.Tool
		search := false
		for _, tool := range req.Tools {
			if !tool.ServerSide {
				functions = append(functions, tool)
				continue
			}
			if tool.Type != "google_search" || !s.SupportsServerSideWebSearch() {
				return nil, fmt.Errorf("unsupported Gemini server tool %q for model %q", tool.Type, s.modelName())
			}
			search = true
		}
		// Convert tool schemas
		decls, err := convertToolSchemas(functions)
		if err != nil {
			return nil, fmt.Errorf("failed to convert tool schemas: %w", err)
		}
		if len(decls) > 0 {
			gemReq.Tools = []gemini.Tool{{FunctionDeclarations: decls}}
		}
		if search {
			gemReq.Tools = append(gemReq.Tools, gemini.Tool{GoogleSearch: &struct{}{}})
			gemReq.ToolConfig = &gemini.ToolConfig{
				IncludeServerSideToolInvocations: true,
				FunctionCallingConfig:            &gemini.FunctionCallingConfig{Mode: "VALIDATED"},
			}
		}
	}

	tc, err := s.thinkingConfig(req)
	if err != nil {
		return nil, err
	}
	if gemReq.GenerationConfig == nil {
		gemReq.GenerationConfig = &gemini.GenerationConfig{}
	}
	gemReq.GenerationConfig.MaxOutputTokens = s.maxOutputTokens()
	if tc != nil {
		gemReq.GenerationConfig.ThinkingConfig = tc
	}

	return gemReq, nil
}

// convertGeminiResponsesToContent converts a Gemini response to llm.Content
func convertGeminiResponseToContent(res *gemini.Response) []llm.Content {
	if res == nil || len(res.Candidates) == 0 || len(res.Candidates[0].Content.Parts) == 0 {
		return []llm.Content{{
			Type: llm.ContentTypeText,
			Text: "",
		}}
	}

	var contents []llm.Content

	// Process each part in the first candidate's content
	for i, part := range res.Candidates[0].Content.Parts {
		// Log the part type for debugging
		slog.DebugContext(context.Background(), "processing_gemini_part",
			"index", i,
			"has_text", part.Text != "",
			"has_function_call", part.FunctionCall != nil,
			"has_function_response", part.FunctionResponse != nil)

		if part.IsText() && (part.Text != "" || part.ThoughtSignature != "") {
			// A part is a thought summary only when thought=true. Gemini 3 attaches
			// thoughtSignature to ordinary final-answer text too, for round-tripping
			// reasoning state — that signature alone does not make the text a thought.
			if part.Thought {
				contents = append(contents, llm.Content{
					Type:      llm.ContentTypeThinking,
					Thinking:  part.Text,
					Signature: part.ThoughtSignature,
				})
				slog.DebugContext(context.Background(), "gemini_thinking_detected",
					"signature", part.ThoughtSignature,
					"thinking_length", len(part.Text))
			} else {
				contents = append(contents, llm.Content{
					Type:      llm.ContentTypeText,
					Text:      part.Text,
					Signature: part.ThoughtSignature,
				})
			}
		} else if part.FunctionCall != nil {
			// Function call (tool use)
			args, err := json.Marshal(part.FunctionCall.Args)
			if err != nil {
				// If we can't marshal, use empty args
				slog.DebugContext(
					context.Background(), "gemini_failed_to_marshal_args",
					"tool_name", part.FunctionCall.Name,
					"args", string(args),
					"err", err.Error(),
				)
				args = []byte("{}")
			}

			toolID := part.FunctionCall.ID
			if toolID == "" {
				toolID = fmt.Sprintf("gemini_tool_%s_%d", part.FunctionCall.Name, time.Now().UnixNano())
			}

			contents = append(contents, llm.Content{
				ID:        toolID,
				Type:      llm.ContentTypeToolUse,
				ToolName:  part.FunctionCall.Name,
				ToolInput: json.RawMessage(args),
				// Capture thought signature - required for Gemini 3 function calling
				Signature: part.ThoughtSignature,
			})

			slog.DebugContext(context.Background(), "gemini_tool_call",
				"tool_id", toolID,
				"tool_name", part.FunctionCall.Name,
				"args", string(args),
				"thought_signature", part.ThoughtSignature)
		} else if part.FunctionResponse != nil {
			// We shouldn't normally get function responses from the model, but just in case
			respData, _ := json.Marshal(part.FunctionResponse.Response)
			slog.DebugContext(context.Background(), "unexpected_function_response",
				"name", part.FunctionResponse.Name,
				"response", string(respData))
		}
	}

	// If no content was added, add an empty text content
	if len(contents) == 0 {
		slog.DebugContext(context.Background(), "empty_gemini_response", "adding_empty_text", true)
		contents = append(contents, llm.Content{
			Type: llm.ContentTypeText,
			Text: "",
		})
	}

	return contents
}

// ensureToolIDs makes sure all tool uses have proper IDs
func ensureToolIDs(contents []llm.Content) {
	for i, content := range contents {
		if content.Type == llm.ContentTypeToolUse && content.ID == "" {
			// Generate a stable ID using the tool name and timestamp
			contents[i].ID = fmt.Sprintf("gemini_tool_%s_%d", content.ToolName, time.Now().UnixNano())
			slog.DebugContext(context.Background(), "assigned_missing_tool_id",
				"tool_name", content.ToolName,
				"new_id", contents[i].ID)
		}
	}
}

func calculateUsage(res *gemini.Response) (llm.Usage, error) {
	if res == nil || res.UsageMetadata == nil {
		return llm.Usage{}, errors.New("gemini: missing usage metadata")
	}
	u := res.UsageMetadata
	if min(u.PromptTokenCount, u.CachedContentTokenCount, u.CandidatesTokenCount, u.ThoughtsTokenCount, u.ToolUsePromptTokenCount) < 0 {
		return llm.Usage{}, errors.New("gemini: negative token count in usage metadata")
	}
	if u.CachedContentTokenCount > u.PromptTokenCount {
		return llm.Usage{}, errors.New("gemini: cached token count exceeds prompt token count")
	}

	return llm.Usage{
		InputTokens:          uint64(u.PromptTokenCount-u.CachedContentTokenCount) + uint64(u.ToolUsePromptTokenCount),
		CacheReadInputTokens: uint64(u.CachedContentTokenCount),
		OutputTokens:         uint64(u.CandidatesTokenCount) + uint64(u.ThoughtsTokenCount),
	}, nil
}

func convertGeminiResponse(res *gemini.Response) (*llm.Response, error) {
	usage, err := calculateUsage(res)
	if err != nil {
		return nil, err
	}
	response := &llm.Response{
		Role:       llm.MessageRoleAssistant,
		StopReason: llm.StopReasonEndTurn,
		Usage:      usage,
	}
	if feedback := res.PromptFeedback; feedback != nil && feedback.BlockReason != "" && feedback.BlockReason != "BLOCK_REASON_UNSPECIFIED" {
		response.StopReason = llm.StopReasonRefusal
		response.RefusalDetails = &llm.RefusalDetails{
			Category: feedback.BlockReason, Explanation: feedback.BlockReasonMessage,
		}
	} else {
		if len(res.Candidates) == 0 {
			return nil, errors.New("gemini: response has no candidates")
		}
		candidate := res.Candidates[0]
		switch candidate.FinishReason {
		case "STOP":
		case "MAX_TOKENS", "CONTINUATION":
			response.StopReason = llm.StopReasonMaxTokens
		case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII",
			"IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION":
			response.StopReason = llm.StopReasonRefusal
			response.RefusalDetails = &llm.RefusalDetails{
				Category: candidate.FinishReason, Explanation: candidate.FinishMessage,
			}
		default:
			return nil, fmt.Errorf("gemini: unexpected finish reason %q (%s)", candidate.FinishReason, candidate.FinishMessage)
		}
	}
	response.Content, err = convertSearchResponse(res)
	if err != nil {
		return nil, err
	}
	ensureToolIDs(response.Content)
	if response.StopReason == llm.StopReasonEndTurn {
		for _, part := range response.Content {
			if part.Type == llm.ContentTypeToolUse {
				response.StopReason = llm.StopReasonToolUse
				break
			}
		}
	}
	return response, nil
}

func (s *Service) Provider() string { return "gemini" }

func (s *Service) SupportsServerSideWebSearch() bool {
	model := s.modelKey()
	return strings.HasPrefix(model, "gemini-3.") || strings.HasPrefix(model, "gemini-3-") ||
		strings.HasPrefix(model, "gemini-4-") || strings.HasPrefix(model, "gemini-4.")
}

// DefaultReasoningLevel reports the effective configured level, or the documented
// model default. Dynamic and unknown defaults return "".
func (s *Service) DefaultReasoningLevel() string {
	tc, err := s.thinkingConfig(nil)
	if err != nil {
		return ""
	}
	caps, found := s.thinkingCapabilities()
	if tc != nil {
		if tc.ThinkingLevel != "" {
			return tc.ThinkingLevel
		}
		for _, level := range caps.Levels {
			if level.ThinkingBudgetTokens() == *tc.ThinkingBudget {
				return level.Name()
			}
		}
	}
	if found && !caps.Supported {
		return "off"
	}
	switch s.modelKey() {
	case "gemini-4-argon", "gemini-3.8-flash", "gemini-3.6-flash":
		return "medium"
	case "gemini-3-flash-preview":
		return "high"
	case "gemini-2.5-flash-lite":
		return "off"
	}
	return ""
}

// maxOutputTokens applies the native model limit, including streaming overrides.
// MaxTokens can lower that limit, never raise it. Unknown models use MaxTokens or
// DefaultMaxTokens.
func (s *Service) maxOutputTokens() int {
	limit, found := modelsdev.LookupOutputLimit(s.URL, s.modelName())
	if s.modelKey() == "gemini-4-argon" {
		// The catalog's 32K limit is non-streaming; this adapter always streams.
		limit, found = 65536, true
	}
	if !found {
		return cmp.Or(s.MaxTokens, DefaultMaxTokens)
	}
	return min(cmp.Or(s.MaxTokens, limit), limit)
}

// SupportsReasoning reports whether generic thinking controls are known.
func (s *Service) SupportsReasoning() bool {
	caps, found := s.thinkingCapabilities()
	return found && caps.Supported && len(caps.Levels) > 0
}

// SupportedReasoningLevels advertises native effort or budget presets.
func (s *Service) SupportedReasoningLevels() []llm.ThinkingLevel {
	caps, found := s.thinkingCapabilities()
	if !found {
		return nil
	}
	return append([]llm.ThinkingLevel(nil), caps.Levels...)
}

// SupportsImages reports whether this service accepts image inputs.
// Gemini models support images by default; set SupportsImages_=false to opt out.
func (s *Service) SupportsImages() bool { return s.SupportsImages_ }

// MaxImageDimension returns the maximum allowed image dimension.
// TODO: determine actual Gemini image dimension limits
func (s *Service) MaxImageDimension() int {
	return 0 // No known limit
}

// MaxImageBytes returns the maximum allowed encoded size for a single image.
// The Gemini docs only document a 20 MB total inline request payload
// (https://ai.google.dev/gemini-api/docs/image-understanding); since the
// request can include multiple images plus prompts, we use 20 MB as a
// per-image upper bound. Requests over the total still need to use the
// Files API.
func (s *Service) MaxImageBytes() int {
	return 20 * 1024 * 1024
}

// Do sends a request to Gemini.
func (s *Service) Do(ctx context.Context, ir *llm.Request) (*llm.Response, error) {
	// Log the incoming request for debugging
	slog.DebugContext(ctx, "gemini_request",
		"message_count", len(ir.Messages),
		"tool_count", len(ir.Tools),
		"system_count", len(ir.System))

	// Log tool-related information if any tools are present
	if len(ir.Tools) > 0 {
		var toolNames []string
		for _, tool := range ir.Tools {
			toolNames = append(toolNames, tool.Name)
		}
		slog.DebugContext(ctx, "gemini_tools", "tools", toolNames)
	}

	// Log details about the messages being sent
	for i, msg := range ir.Messages {
		contentTypes := make([]string, len(msg.Content))
		for j, c := range msg.Content {
			contentTypes[j] = c.Type.String()

			// Log tool-related content with more details
			if c.Type == llm.ContentTypeToolUse {
				slog.DebugContext(ctx, "gemini_tool_use",
					"message_idx", i,
					"content_idx", j,
					"tool_name", c.ToolName,
					"tool_input", string(c.ToolInput))
			} else if c.Type == llm.ContentTypeToolResult {
				slog.DebugContext(ctx, "gemini_tool_result",
					"message_idx", i,
					"content_idx", j,
					"tool_use_id", c.ToolUseID,
					"tool_error", c.ToolError,
					"result_count", len(c.ToolResult))
			}
		}
		slog.DebugContext(ctx, "gemini_message",
			"idx", i,
			"role", msg.Role.String(),
			"content_types", contentTypes)
	}
	// Build the Gemini request
	gemReq, err := s.buildGeminiRequest(ir)
	if err != nil {
		return nil, fmt.Errorf("failed to build Gemini request: %w", err)
	}

	// Log the structured Gemini request for debugging
	if reqJSON, err := json.MarshalIndent(gemReq, "", "  "); err == nil {
		slog.DebugContext(ctx, "gemini_request_json", "request", string(reqJSON))
	}

	// Create a Gemini model instance
	model := gemini.Model{
		Model:    "models/" + s.modelName(),
		Endpoint: s.URL,
		APIKey:   s.APIKey,
		HTTPC:    cmp.Or(s.HTTPC, http.DefaultClient),
		OnStream: ir.OnStream,
	}

	// Send the request to Gemini with retry logic
	startTime := time.Now()
	retryStart := startTime
	endTime := startTime // Initialize endTime
	var gemRes *gemini.Response

	const maxAttempts = 16
	for attempts := 0; attempts < maxAttempts; attempts++ {
		gemApiErr := error(nil)
		gemRes, gemApiErr = model.GenerateContent(ctx, gemReq)
		endTime = time.Now()

		if gemApiErr == nil {
			// Successful response
			// Log the structured Gemini response
			if resJSON, err := json.MarshalIndent(gemRes, "", "  "); err == nil {
				slog.DebugContext(ctx, "gemini_response_json", "response", string(resJSON))
			}
			break
		}

		if attempts == maxAttempts-1 {
			// We've exhausted all retry attempts
			return nil, fmt.Errorf("gemini: API error after %d attempts (last at %s): %w", attempts+1, time.Now().Format(time.DateTime), gemApiErr)
		}

		// Check if the error is retryable (server error or rate limiting).
		var apiErr *gemini.APIError
		retryable := errors.As(gemApiErr, &apiErr) && (apiErr.StatusCode == 429 || apiErr.StatusCode >= 500)
		if !retryable {
			return nil, fmt.Errorf("gemini: API error: %w", gemApiErr)
		}
		sleep := llm.RetryDelay(llm.DefaultBackoff, attempts+1, llm.ParseRetryAfter(apiErr.Header.Get("Retry-After")))
		slog.WarnContext(ctx, "gemini_request_retry", "error", gemApiErr.Error(), "status_code", apiErr.StatusCode, "attempt", attempts+1, "sleep", sleep, "elapsed", time.Since(retryStart).Round(time.Second))
		if llm.WaitRetry(ctx, ir, llm.RetryEvent{Attempt: attempts + 1, Sleep: sleep, Err: llm.Truncate(gemApiErr.Error(), 160), Status: apiErr.StatusCode, Provider: "gemini", Model: cmp.Or(s.Model, DefaultModel)}) != nil {
			return nil, fmt.Errorf("gemini: context cancelled during backoff after %d attempts: %w", attempts+1, gemApiErr)
		}
	}

	response, err := convertGeminiResponse(gemRes)
	if err != nil {
		return nil, err
	}
	response.Model = s.Model
	response.Usage.CostUSD = llm.CostUSDFromResponse(gemRes.Header())
	response.StartTime = &startTime
	response.EndTime = &endTime
	response.URL = cmp.Or(s.URL, gemini.DefaultEndpoint)
	response.Origin = &llm.MessageOrigin{
		Provider: "gemini", Transport: "gemini-generate-content", Model: s.modelName(),
	}
	return response, nil
}
