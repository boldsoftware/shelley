package oai

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/imageutil"
	"shelley.exe.dev/llm/llmhttp"
)

// ResponsesService provides chat completions using the OpenAI Responses API.
// This API is required for models like gpt-5.3-codex.
// Fields should not be altered concurrently with calling any method on ResponsesService.
type ResponsesService struct {
	HTTPC         *http.Client      // defaults to http.DefaultClient if nil
	APIKey        string            // optional, if not set will try to load from env var
	Model         Model             // defaults to DefaultModel if zero value
	ModelURL      string            // optional, overrides Model.URL
	MaxTokens     int               // defaults to DefaultMaxTokens if zero
	Org           string            // optional - organization ID
	DumpLLM       bool              // whether to dump request/response text to files for debugging; defaults to false
	ThinkingLevel llm.ThinkingLevel // service-level default; zero (ThinkingLevelDefault) and ThinkingLevelOff both leave the field off the wire
	ProviderName  string            // e.g., "openai"
	Backoff       []time.Duration   // retry backoff durations; defaults to {1s, 2s, 5s, ...} if nil

	// ReasoningEffort, if non-empty, is used as the reasoning.effort value sent to
	// the OpenAI Responses API verbatim, overriding ThinkingLevel. This allows
	// custom-model configurations to pass through provider-specific values
	// (e.g. "xhigh", "none") without Shelley needing to know them.
	ReasoningEffort string

	// ReasoningReplay controls persisted reasoning replay. The zero value and
	// "auto" resolve from models.dev; "none" disables replay explicitly.
	ReasoningReplay ReasoningReplay
}

var _ llm.Service = (*ResponsesService)(nil)

func (s *ResponsesService) messageOrigin(model Model) llm.MessageOrigin {
	return llm.MessageOrigin{
		Provider:  cmp.Or(s.ProviderName, "openai"),
		Transport: "openai-responses:" + oaiTransportIdentity(cmp.Or(s.ModelURL, model.URL, OpenAIURL)),
		Model:     model.ModelName,
	}
}

const (
	responsesImagePatchSize     = 32
	responsesMaxImagePatchCount = 30000
)

// fitResponsesImagesToPatchLimit copies messages and downsizes image content
// that the Responses API would reject. This runs at request time, rather than
// only when a tool first creates an image, so old conversations containing a
// now-oversized image recover on their next turn instead of staying wedged.
func fitResponsesImagesToPatchLimit(messages []llm.Message, patchSize, maxPatches int) ([]llm.Message, error) {
	fitted := append([]llm.Message(nil), messages...)
	for i := range fitted {
		contents, err := fitResponsesContentToPatchLimit(fitted[i].Content, patchSize, maxPatches)
		if err != nil {
			return nil, fmt.Errorf("prepare image in message %d: %w", i, err)
		}
		fitted[i].Content = contents
	}
	return fitted, nil
}

func fitResponsesContentToPatchLimit(contents []llm.Content, patchSize, maxPatches int) ([]llm.Content, error) {
	fitted := append([]llm.Content(nil), contents...)
	for i := range fitted {
		if len(fitted[i].ToolResult) > 0 {
			toolResult, err := fitResponsesContentToPatchLimit(fitted[i].ToolResult, patchSize, maxPatches)
			if err != nil {
				return nil, fmt.Errorf("tool result %d: %w", i, err)
			}
			fitted[i].ToolResult = toolResult
		}
		if !isImageContent(fitted[i]) || fitted[i].Data == "" {
			continue
		}

		data, err := base64.StdEncoding.DecodeString(fitted[i].Data)
		if err != nil {
			return nil, fmt.Errorf("decode image %d: %w", i, err)
		}
		data, format, width, height, resized, err := imageutil.ResizeImageToPatchLimit(data, patchSize, maxPatches)
		if err != nil {
			return nil, fmt.Errorf("resize image %d: %w", i, err)
		}
		if !resized {
			continue
		}
		fitted[i].Data = base64.StdEncoding.EncodeToString(data)
		fitted[i].MediaType = "image/" + format
		fitted[i].DisplayWidth = width
		fitted[i].DisplayHeight = height
	}
	return fitted, nil
}

// Responses API request/response types

type responsesRequest struct {
	Model             string               `json:"model"`
	Instructions      string               `json:"instructions,omitempty"`
	Store             bool                 `json:"store"`
	Stream            bool                 `json:"stream"`
	Input             []responsesInputItem `json:"input"`
	Tools             []responsesTool      `json:"tools,omitempty"`
	ToolChoice        any                  `json:"tool_choice,omitempty"`
	ParallelToolCalls bool                 `json:"parallel_tool_calls,omitempty"`
	MaxOutputTokens   int                  `json:"max_output_tokens,omitempty"`
	Reasoning         *responsesReasoning  `json:"reasoning,omitempty"`
	Include           []string             `json:"include,omitempty"`
	PromptCacheKey    string               `json:"prompt_cache_key,omitempty"`
	Text              *responsesText       `json:"text,omitempty"`
}

type responsesReasoning struct {
	Effort  string `json:"effort,omitempty"`  // "low", "medium", "high"
	Summary string `json:"summary,omitempty"` // "auto": include reasoning summaries in the output
}

type responsesText struct {
	Verbosity string `json:"verbosity,omitempty"`
}

type responsesInputItem struct {
	ID               string              `json:"id,omitempty"`                // for replayed output items
	Type             string              `json:"type"`                        // "message", "reasoning", "function_call", "custom_tool_call", outputs
	Role             string              `json:"role,omitempty"`              // for messages: "user", "assistant"
	Content          []responsesContent  `json:"content,omitempty"`           // for messages
	CallID           string              `json:"call_id,omitempty"`           // for function_call and function_call_output
	Name             string              `json:"name,omitempty"`              // for function_call
	Arguments        string              `json:"arguments,omitempty"`         // for function_call
	Input            string              `json:"input,omitempty"`             // for custom_tool_call
	Output           string              `json:"output,omitempty"`            // for tool outputs
	Summary          *[]responsesSummary `json:"summary,omitempty"`           // for reasoning; pointer preserves an empty array
	EncryptedContent string              `json:"encrypted_content,omitempty"` // for reasoning
}

type responsesContent struct {
	Type        string               `json:"type"` // "input_text", "output_text", "input_image"
	Text        string               `json:"text,omitempty"`
	ImageURL    string               `json:"image_url,omitempty"`
	Detail      responsesImageDetail `json:"detail,omitempty"`
	Annotations json.RawMessage      `json:"annotations,omitempty"` // Preserve zero/empty and unknown provider fields.
}

type responsesImageDetail string

const responsesImageDetailAuto responsesImageDetail = "auto"

type responsesTool struct {
	Type        string                `json:"type"` // function, custom, or provider-hosted type
	Name        string                `json:"name,omitempty"`
	Description string                `json:"description,omitempty"`
	Parameters  json.RawMessage       `json:"parameters,omitempty"`
	Format      *responsesToolGrammar `json:"format,omitempty"`
}

type responsesToolGrammar struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax"`
	Definition string `json:"definition"`
}

type responsesResponse struct {
	ID        string                `json:"id"`
	Object    string                `json:"object"` // "response"
	CreatedAt float64               `json:"created_at"`
	Status    string                `json:"status"` // "completed", "incomplete", etc.
	Model     string                `json:"model"`
	Output    []responsesOutputItem `json:"output"`
	Usage     responsesUsage        `json:"usage"`
	Error     *responsesError       `json:"error"`
}

type responsesOutputItem struct {
	ID               string             `json:"id"`
	Type             string             `json:"type"`           // "message", "reasoning", "function_call", "web_search_call"
	Role             string             `json:"role,omitempty"` // for messages: "assistant"
	Status           string             `json:"status,omitempty"`
	Content          []responsesContent `json:"content,omitempty"`           // for messages
	CallID           string             `json:"call_id,omitempty"`           // for function_call
	Name             string             `json:"name,omitempty"`              // for function_call
	Arguments        string             `json:"arguments,omitempty"`         // for function_call
	Input            string             `json:"input,omitempty"`             // for custom_tool_call
	Summary          []responsesSummary `json:"summary,omitempty"`           // for reasoning
	EncryptedContent string             `json:"encrypted_content,omitempty"` // for reasoning
	Action           *responsesAction   `json:"action,omitempty"`            // for web_search_call (queries)
}

// responsesAction is the action descriptor for server-side tool calls like
// web_search_call. For web_search, it carries the actual search queries.
type responsesAction struct {
	Type    string   `json:"type,omitempty"`
	Queries []string `json:"queries,omitempty"`
}

// responsesSummary is an item in a reasoning output's summary array.
// See https://developers.openai.com/api/docs/guides/reasoning#reasoning-summaries
type responsesSummary struct {
	Type string `json:"type"` // "summary_text"
	Text string `json:"text"`
}

type responsesUsage struct {
	InputTokens         int                           `json:"input_tokens"`
	InputTokensDetails  openAIInputTokensDetails      `json:"input_tokens_details"`
	OutputTokens        int                           `json:"output_tokens"`
	OutputTokensDetails *responsesOutputTokensDetails `json:"output_tokens_details,omitempty"`
	TotalTokens         int                           `json:"total_tokens"`
}

type responsesOutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type responsesError struct {
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Param   string          `json:"param"`
	Code    json.RawMessage `json:"code"`
}

type responsesRequestError struct {
	err               error
	retryable         bool
	noImmediateRetry  bool
	idleStallDuration time.Duration
}

var _ llm.RequestError = (*responsesRequestError)(nil)

func (e *responsesRequestError) Error() string { return e.err.Error() }
func (e *responsesRequestError) Unwrap() error { return e.err }

func (e *responsesRequestError) RequestErrorInfo() llm.RequestErrorInfo {
	return llm.RequestErrorInfo{
		Retryable:         e.retryable,
		NoImmediateRetry:  e.noImmediateRetry,
		IdleStallDuration: e.idleStallDuration,
	}
}

func newResponsesRequestError(err error, retryable bool) error {
	return &responsesRequestError{err: err, retryable: retryable}
}

func responsesErrorRetryable(apiErr *responsesError) bool {
	retryable, classified := classifyResponsesError(apiErr)
	return retryable || !classified
}

func classifyResponsesError(apiErr *responsesError) (retryable, classified bool) {
	if apiErr == nil {
		return false, false
	}
	message := strings.ToLower(apiErr.Message)
	if strings.Contains(message, "does not support image") || strings.Contains(message, "image input is not supported") || strings.Contains(message, "image inputs are not supported") {
		return false, true
	}

	var code string
	_ = json.Unmarshal(apiErr.Code, &code)
	switch strings.ToLower(code) {
	case "server_error", "rate_limit_exceeded", "overloaded_error", "vector_store_timeout":
		return true, true
	case "unsupported_value", "invalid_value", "invalid_request_error", "model_not_found", "insufficient_quota", "context_length_exceeded", "invalid_api_key",
		"invalid_prompt", "data_residency_mismatch", "bio_policy", "misalignment_policy_violation", "invalid_image", "invalid_image_format", "invalid_base64_image", "invalid_image_url",
		"image_too_large", "image_too_small", "image_parse_error", "image_content_policy_violation", "invalid_image_mode", "image_file_too_large",
		"unsupported_image_media_type", "empty_image_file", "failed_to_download_image", "image_file_not_found":
		return false, true
	}
	var numericCode int
	if json.Unmarshal(apiErr.Code, &numericCode) == nil {
		switch {
		case numericCode == http.StatusRequestTimeout || numericCode == http.StatusTooManyRequests:
			return true, true
		case numericCode >= 400 && numericCode < 500:
			return false, true
		case numericCode >= 500 && numericCode < 600:
			return true, true
		}
	}

	switch strings.ToLower(apiErr.Type) {
	case "server_error", "api_error", "rate_limit_error", "overloaded_error":
		return true, true
	case "invalid_request_error", "authentication_error", "permission_error", "not_found_error":
		return false, true
	default:
		return false, false
	}
}

type responsesReasoningReplay uint8

const (
	responsesReasoningReplayNone responsesReasoningReplay = iota
	responsesReasoningReplayEncrypted
	responsesReasoningReplaySummary
)

// fromLLMMessageResponses converts llm.Message to Responses API input items.
// Summary-only reasoning is display metadata for OpenAI, but Fireworks uses it
// as the Responses representation of models.dev reasoning_content.
func fromLLMMessageResponses(msg llm.Message, reasoningReplay responsesReasoningReplay) []responsesInputItem {
	var items []responsesInputItem

	// Separate tool results from regular content
	var regularContent []llm.Content
	var toolResults []llm.Content

	for _, c := range msg.Content {
		if llm.IsServerSideContentType(c.Type) {
			continue // skip provider-specific server-side content blocks
		}
		if c.Type == llm.ContentTypeToolResult {
			toolResults = append(toolResults, c)
		} else {
			regularContent = append(regularContent, c)
		}
	}

	// Process tool results first - they need to come before the assistant message
	for _, tr := range toolResults {
		// function_call_output is text-only. Preserve images as a following user
		// message so vision-capable Responses models actually receive them.
		var texts []string
		var imageContent []responsesContent
		for _, result := range tr.ToolResult {
			if strings.TrimSpace(result.Text) != "" {
				texts = append(texts, result.Text)
			}
			if isImageContent(result) {
				imageContent = append(imageContent, responsesImageContent(result))
			}
		}
		toolResultContent := strings.Join(texts, "\n")

		// Add error prefix if needed
		if tr.ToolError {
			if toolResultContent != "" {
				toolResultContent = "error: " + toolResultContent
			} else {
				toolResultContent = "error: tool execution failed"
			}
		}

		outputType := "function_call_output"
		if tr.ToolName == "apply_patch" {
			outputType = "custom_tool_call_output"
		}
		items = append(items, responsesInputItem{
			Type:   outputType,
			CallID: tr.ToolUseID,
			Output: cmp.Or(toolResultContent, " "),
		})

		if len(imageContent) > 0 {
			content := []responsesContent{{Type: "input_text", Text: "Images returned by tool " + tr.ToolUseID + ":"}}
			content = append(content, imageContent...)
			items = append(items, responsesInputItem{
				Type:    "message",
				Role:    "user",
				Content: content,
			})
		}
	}

	// Process regular content in provider output order. Contiguous text/image
	// blocks are coalesced into one message item; reasoning and function calls
	// remain distinct items in their original relative order.
	if len(regularContent) > 0 {
		var messageContent []responsesContent
		flushMessage := func() {
			if len(messageContent) == 0 {
				return
			}
			role := "user"
			if msg.Role == llm.MessageRoleAssistant {
				role = "assistant"
			}
			items = append(items, responsesInputItem{
				Type:    "message",
				Role:    role,
				Content: messageContent,
			})
			messageContent = nil
		}

		for _, c := range regularContent {
			switch c.Type {
			case llm.ContentTypeText:
				if isImageContent(c) {
					messageContent = append(messageContent, responsesImageContent(c))
				} else if c.Text != "" {
					contentType := "input_text"
					var annotations json.RawMessage
					if msg.Role == llm.MessageRoleAssistant {
						contentType = "output_text"
						annotations = c.Citations
					}
					messageContent = append(messageContent, responsesContent{
						Type:        contentType,
						Text:        c.Text,
						Annotations: annotations,
					})
				}
			case llm.ContentTypeThinking:
				metadata := c.OpenAIResponsesReasoning
				encrypted := metadata != nil && metadata.EncryptedContent != "" && reasoningReplay == responsesReasoningReplayEncrypted
				summaryOnly := metadata != nil && metadata.EncryptedContent == "" && reasoningReplay == responsesReasoningReplaySummary && metadata.ID != "" && len(metadata.Summary) > 0
				if msg.Role != llm.MessageRoleAssistant || metadata == nil || (!encrypted && !summaryOnly) {
					continue
				}
				flushMessage()
				summary := make([]responsesSummary, len(metadata.Summary))
				for i, part := range metadata.Summary {
					summary[i] = responsesSummary{Type: part.Type, Text: part.Text}
				}
				items = append(items, responsesInputItem{
					ID:               metadata.ID,
					Type:             "reasoning",
					Summary:          &summary,
					EncryptedContent: metadata.EncryptedContent,
				})
			case llm.ContentTypeToolUse:
				flushMessage()
				if c.ToolName == "apply_patch" {
					var input struct {
						Input string `json:"input"`
					}
					_ = json.Unmarshal(c.ToolInput, &input)
					items = append(items, responsesInputItem{Type: "custom_tool_call", CallID: c.ID, Name: c.ToolName, Input: input.Input})
				} else {
					items = append(items, responsesInputItem{Type: "function_call", CallID: c.ID, Name: c.ToolName, Arguments: string(c.ToolInput)})
				}
			}
		}
		flushMessage()
	}

	return items
}

func responsesImageContent(c llm.Content) responsesContent {
	return responsesContent{
		Type:     "input_image",
		ImageURL: openAIImageDataURL(c),
		Detail:   responsesImageDetailAuto,
	}
}

// fromLLMToolResponses converts llm.Tool to Responses API tool format
func fromLLMToolResponses(t *llm.Tool) responsesTool {
	if t.CustomGrammar != "" {
		return responsesTool{
			Type:        "custom",
			Name:        t.Name,
			Description: t.Description,
			Format: &responsesToolGrammar{
				Type:       "grammar",
				Syntax:     "lark",
				Definition: t.CustomGrammar,
			},
		}
	}
	return responsesTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: t.InputSchema}
}

// responsesInstructionsFromLLMSystem converts llm.SystemContent to the
// Responses API top-level instructions field.
func responsesInstructionsFromLLMSystem(systemContent []llm.SystemContent) string {
	var parts []string
	for _, content := range systemContent {
		if content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// toLLMResponseFromResponses converts Responses API response to llm.Response
func (s *ResponsesService) toLLMResponseFromResponses(resp *responsesResponse, headers http.Header) *llm.Response {
	if len(resp.Output) == 0 {
		return &llm.Response{
			ID:    resp.ID,
			Model: resp.Model,
			Role:  llm.MessageRoleAssistant,
			Usage: s.toLLMUsageFromResponses(resp.Usage, headers),
		}
	}

	// Process the output items
	var contents []llm.Content
	var stopReason llm.StopReason = llm.StopReasonStopSequence

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			// Convert message content
			for _, c := range item.Content {
				if c.Text != "" || len(c.Annotations) > 0 {
					text := llm.Content{
						Type:      llm.ContentTypeText,
						Text:      c.Text,
						Citations: slices.Clone(c.Annotations),
					}
					contents = append(contents, text)
				}
			}
		case "reasoning":
			parts := make([]string, 0, len(item.Summary))
			summary := make([]llm.OpenAIResponsesReasoningSummary, len(item.Summary))
			for i, s := range item.Summary {
				summary[i] = llm.OpenAIResponsesReasoningSummary{Type: s.Type, Text: s.Text}
				if s.Text != "" {
					parts = append(parts, s.Text)
				}
			}
			if len(parts) > 0 || item.EncryptedContent != "" {
				contents = append(contents, llm.Content{
					Type: llm.ContentTypeThinking,
					Text: strings.Join(parts, "\n"),
					OpenAIResponsesReasoning: &llm.OpenAIResponsesReasoningMetadata{
						ID:               item.ID,
						EncryptedContent: item.EncryptedContent,
						Summary:          summary,
					},
				})
			}
		case "web_search_call":
			// Server-side web search call. Surface it as a server_tool_use
			// content block so the UI can show what was searched. The actual
			// results land as url_citation annotations on the subsequent
			// message text (handled above).
			var queries []string
			if item.Action != nil {
				queries = item.Action.Queries
			}
			input := map[string]any{}
			switch len(queries) {
			case 0:
				// no query info available
			case 1:
				input["query"] = queries[0]
			default:
				input["queries"] = queries
			}
			inputJSON, _ := json.Marshal(input)
			contents = append(contents, llm.Content{
				ID:        item.ID,
				Type:      llm.ContentTypeServerToolUse,
				ToolName:  "web_search",
				ToolInput: inputJSON,
			})
		case "function_call":
			contents = append(contents, llm.Content{ID: item.CallID, Type: llm.ContentTypeToolUse, ToolName: item.Name, ToolInput: json.RawMessage(item.Arguments)})
			stopReason = llm.StopReasonToolUse
		case "custom_tool_call":
			input, _ := json.Marshal(map[string]string{"input": item.Input})
			contents = append(contents, llm.Content{ID: item.CallID, Type: llm.ContentTypeToolUse, ToolName: item.Name, ToolInput: input})
			stopReason = llm.StopReasonToolUse
		}
	}

	// If no content, add empty text content
	if len(contents) == 0 {
		contents = append(contents, llm.Content{
			Type: llm.ContentTypeText,
			Text: "",
		})
	}

	return &llm.Response{
		ID:         resp.ID,
		Model:      resp.Model,
		Role:       llm.MessageRoleAssistant,
		Content:    contents,
		StopReason: stopReason,
		Usage:      s.toLLMUsageFromResponses(resp.Usage, headers),
	}
}

// toLLMUsageFromResponses converts Responses API usage to llm.Usage.
func (s *ResponsesService) toLLMUsageFromResponses(usage responsesUsage, headers http.Header) llm.Usage {
	u := splitOpenAIInputUsage(usage.InputTokens, usage.InputTokensDetails)
	u.OutputTokens = uint64(usage.OutputTokens)
	u.CostUSD = llm.CostUSDFromResponse(headers)
	return u
}

func (s *ResponsesService) Provider() string { return s.ProviderName }

// DefaultReasoningLevel reports the reasoning effort applied to un-overridden
// requests, mirroring the request builder's precedence: verbatim
// ReasoningEffort wins, else a configured service-level ThinkingLevel. When
// neither is set, no reasoning field is emitted and the provider applies its
// own default (which Shelley cannot name), so it returns "".
func (s *ResponsesService) DefaultReasoningLevel() string {
	if s.ReasoningEffort != "" {
		return s.ReasoningEffort
	}
	if s.ThinkingLevel != llm.ThinkingLevelDefault && s.ThinkingLevel != llm.ThinkingLevelOff {
		return s.ThinkingLevel.Name()
	}
	return ""
}

func (s *ResponsesService) SupportsServerSideWebSearch() bool { return true }

// SupportsReasoning reports the models.dev capability when known. Unknown
// models retain the historical default of supporting reasoning controls.
func (s *ResponsesService) SupportsReasoning() bool {
	caps, found := modelReasoningCapabilities(s.ModelURL, cmp.Or(s.Model, DefaultModel))
	return !found || caps.Supported
}

// SupportedReasoningLevels advertises exact effort levels from models.dev.
// Nil means the model has no exact effort metadata and callers use the
// historical provider fallback.
func (s *ResponsesService) SupportedReasoningLevels() []llm.ThinkingLevel {
	caps, found := modelReasoningCapabilities(s.ModelURL, cmp.Or(s.Model, DefaultModel))
	return advertisedReasoningLevels(caps, found)
}

// SupportsImages reports whether this service accepts image inputs.
// OpenAI Responses API supports images for vision models; set
// Model.SupportsImages to enable image inputs.
func (s *ResponsesService) SupportsImages() bool { return s.Model.SupportsImages }

// MaxImageDimension returns the maximum allowed image dimension.
// TODO: determine actual OpenAI image dimension limits
func (s *ResponsesService) MaxImageDimension() int {
	return 0 // No known limit
}

// MaxImageBytes returns the maximum allowed encoded size for a single image.
// OpenAI's vision docs cap image inputs at 20 MB per image
// (https://platform.openai.com/docs/guides/images-vision).
func (s *ResponsesService) MaxImageBytes() int {
	return 20 * 1024 * 1024
}

// Do sends a request to OpenAI using the Responses API.
func (s *ResponsesService) Do(ctx context.Context, ir *llm.Request) (*llm.Response, error) {
	startTime := time.Now()
	var err error
	ir, err = llm.PrepareRequestCitations(ctx, ir, "openai-responses", s.adaptCitation)
	if err != nil {
		return nil, err
	}
	httpc := cmp.Or(s.HTTPC, http.DefaultClient)
	model := cmp.Or(s.Model, DefaultModel)
	openAIResponses := s.isOpenAIResponses()
	reasoningReplay := responsesReasoningReplayNone
	switch ResolveReasoningReplay(cmp.Or(s.ModelURL, model.URL), model.ModelName, s.ReasoningReplay) {
	case ReasoningReplayContent:
		reasoningReplay = responsesReasoningReplaySummary
	case ReasoningReplayNone:
	case "":
		if openAIResponses {
			reasoningReplay = responsesReasoningReplayEncrypted
		}
	}

	var allInput []responsesInputItem
	messages := ir.Messages
	if openAIResponses {
		fittedMessages, err := fitResponsesImagesToPatchLimit(messages, responsesImagePatchSize, responsesMaxImagePatchCount)
		if err != nil {
			return nil, fmt.Errorf("prepare Responses API images: %w", err)
		}
		messages = fittedMessages
	}
	origin := s.messageOrigin(model)
	for i, msg := range messages {
		msg = filterReasoningForOrigin(msg, origin)
		for j, c := range msg.Content {
			if len(c.Citations) > 0 && c.Text == "" {
				return nil, fmt.Errorf("openai-responses messages[%d].content[%d]: cannot replay citations on empty assistant text", i, j)
			}
		}
		items := fromLLMMessageResponses(msg, reasoningReplay)
		allInput = append(allInput, items...)
	}

	// Convert tools. Server-side tools (e.g. web_search) are passed
	// through as their provider-specific type with no name/description/schema.
	var tools []responsesTool
	for _, t := range ir.Tools {
		if t.ServerSide {
			if t.Type != "" {
				tools = append(tools, responsesTool{Type: t.Type})
			}
			continue
		}
		tools = append(tools, fromLLMToolResponses(t))
	}

	// Construct the full URL
	baseURL := cmp.Or(s.ModelURL, model.URL, OpenAIURL)
	fullURL := baseURL + "/responses"

	// Create the request
	req := responsesRequest{
		Model:           model.ModelName,
		Instructions:    responsesInstructionsFromLLMSystem(ir.System),
		Store:           false,
		Stream:          true,
		Input:           allInput,
		Tools:           tools,
		MaxOutputTokens: maxOutputTokens(baseURL, model.ModelName, s.MaxTokens),
	}
	if openAIResponses {
		req.Include = []string{"reasoning.encrypted_content"}
		req.ToolChoice = "auto"
		req.ParallelToolCalls = true
		req.PromptCacheKey = llmhttp.PromptCacheKeyFromContext(ctx)
		if model.TextVerbosity != "" {
			req.Text = &responsesText{Verbosity: model.TextVerbosity}
		}
	}

	// Add reasoning. Precedence:
	//   1. ir.ThinkingLevel (request-level override from the caller)
	//   2. s.ReasoningEffort (custom verbatim string from per-model config)
	//   3. s.ThinkingLevel (service-level default)
	level := llm.EffectiveThinkingLevel(s.ThinkingLevel, ir.ThinkingLevel)
	levels := s.SupportedReasoningLevels()
	genericEffort := false
	var effort string
	switch {
	case ir.ReasoningEffort != "":
		effort = ir.ReasoningEffort
	case ir.ThinkingLevel == llm.ThinkingLevelOff:
		if len(levels) > 0 {
			effort = "none"
			genericEffort = true
		}
	case ir.ThinkingLevel != llm.ThinkingLevelDefault:
		effort = ir.ThinkingLevel.ThinkingEffort()
		genericEffort = true
	case s.ReasoningEffort != "":
		effort = s.ReasoningEffort
	case level != llm.ThinkingLevelOff:
		effort = level.ThinkingEffort()
		genericEffort = true
	}
	// Exact models.dev effort lists use the shared rounding rule. Without an
	// exact list, preserve the historical Responses clamps. Provider-verbatim
	// values from the service or request are never clamped.
	if genericEffort && effort != "" {
		if len(levels) > 0 {
			effort = clampKnownReasoningEffort(effort, levels)
		} else {
			if effort == "minimal" && strings.Contains(model.ModelName, "codex") {
				effort = "low"
			}
			if effort == "max" {
				effort = "xhigh"
			}
		}
	}
	if effort != "" {
		req.Reasoning = &responsesReasoning{Effort: effort}
		if s.supportsReasoningSummaries() {
			req.Reasoning.Summary = "auto"
		}
	}

	// Add tool choice if specified
	if ir.ToolChoice != nil {
		req.ToolChoice = fromLLMToolChoice(ir.ToolChoice)
	}

	// Marshal the request
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Dump request if enabled
	if s.DumpLLM {
		if reqJSONPretty, err := json.MarshalIndent(req, "", "  "); err == nil {
			if err := llm.DumpToFile("request", fullURL, reqJSONPretty); err != nil {
				slog.WarnContext(ctx, "failed to dump responses request to file", "error", err)
			}
		}
	}

	// Retry mechanism: long tail because providers regularly have multi-hour
	// incidents and returning after two minutes is a worse UX than waiting.
	backoff := s.Backoff
	if len(backoff) == 0 {
		backoff = []time.Duration{
			1 * time.Second,
			2 * time.Second,
			5 * time.Second,
			10 * time.Second,
			30 * time.Second,
			1 * time.Minute,
			2 * time.Minute,
			5 * time.Minute,
			10 * time.Minute,
			20 * time.Minute,
			30 * time.Minute,
		}
	}

	// retry loop
	retryStart := startTime
	var errs error            // accumulated errors across all attempts
	var lastErrSummary string // short description of the most recent attempt failure
	var lastErrStatus int     // HTTP status of the most recent attempt failure, 0 if none
	var lastRequestErrorInfo llm.RequestErrorInfo
	var retryAfter time.Duration // hint from upstream Retry-After header, reset each attempt
	for attempts := 0; ; attempts++ {
		if attempts > 15 {
			return nil, &responsesRequestError{
				err:               fmt.Errorf("responses request failed after %d attempts (url=%s, model=%s): %w", attempts, fullURL, model.ModelName, errs),
				retryable:         true,
				noImmediateRetry:  true,
				idleStallDuration: lastRequestErrorInfo.IdleStallDuration,
			}
		}
		if attempts > 0 {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("responses request failed after %d attempts (context cancelled): %w", attempts, errs)
			}
			base := backoff[min(attempts-1, len(backoff)-1)]
			jitter := time.Duration(rand.Int64N(max(min(int64(base), int64(time.Second)), 1)))
			sleep := base + jitter
			if retryAfter > sleep {
				sleep = retryAfter
			}
			retryAfter = 0
			slog.WarnContext(ctx, "responses request sleep before retry", "sleep", sleep, "attempts", attempts, "elapsed", time.Since(retryStart).Round(time.Second), "status", lastErrStatus, "last_error", lastErrSummary)
			if ir.OnRetry != nil {
				ir.OnRetry(llm.RetryEvent{Attempt: attempts + 1, Sleep: sleep, Err: lastErrSummary, Status: lastErrStatus, Provider: "openai", Model: model.ModelName})
			}
			select {
			case <-time.After(sleep):
			case <-ctx.Done():
				return nil, fmt.Errorf("responses request failed after %d attempts (context cancelled during backoff): %w", attempts, errs)
			}
		}

		// Create HTTP request
		lastRequestErrorInfo = llm.RequestErrorInfo{}
		httpReq, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(reqJSON))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+s.APIKey)
		if s.Org != "" {
			httpReq.Header.Set("OpenAI-Organization", s.Org)
		}

		// Send request
		httpResp, err := httpc.Do(httpReq)
		if err != nil {
			lastErrSummary = "transport: " + llm.Truncate(err.Error(), 160)
			lastErrStatus = 0
			lastRequestErrorInfo, _ = llm.RequestErrorInfoFromError(err)
			errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: %w", attempts+1, time.Now().Format(time.DateTime), err))
			continue
		}
		if httpResp.StatusCode != http.StatusOK {
			body, readErr := io.ReadAll(httpResp.Body)
			httpResp.Body.Close()
			if readErr != nil {
				slog.WarnContext(ctx, "responses_request_read_failed", "error", readErr, "status_code", httpResp.StatusCode, "url", fullURL, "model", model.ModelName)
			}

			var apiErr responsesError
			// Gateways may return plain text instead of the provider's JSON envelope.
			_ = json.Unmarshal(body, &struct {
				Error *responsesError `json:"error"`
			}{Error: &apiErr})
			errMessage := strings.TrimSpace(string(body))
			if apiErr.Message != "" {
				errMessage = apiErr.Message
			}
			// io.ReadAll returns whatever it managed to read alongside the error, so
			// a severed body still yields a (possibly empty) partial message. Keep it
			// and note the truncation; the status code decides retryability below.
			//
			// The note deliberately omits readErr's text: it would embed transport
			// phrases ("connection reset by peer") that loop.IsRetryableLLMError
			// pattern-matches, flipping a terminal 4xx into a retryable one. The
			// read error is already in the responses_request_read_failed log line.
			if readErr != nil {
				errMessage = strings.TrimSpace(errMessage + " [body truncated]")
			}
			retryErrMessage := llm.Truncate(errMessage, 512)
			terminalErrMessage := llm.Truncate(errMessage, 4<<10)
			now := time.Now().Format(time.DateTime)

			// The status code is authoritative even when the body never arrived: a
			// 502 whose explanation was cut off mid-read is still a transient
			// gateway failure, and a 400 is still terminal. Classifying on the read
			// error instead would retry unrecoverable client errors and give up on
			// recoverable server ones.
			switch {
			case httpResp.StatusCode >= 500:
				// Server errors are retryable regardless of whether the provider or
				// gateway encoded the error as JSON or plain text.
				retryAfter = llm.ParseRetryAfter(httpResp.Header.Get("Retry-After"))
				lastErrSummary = llm.Truncate(errMessage, 160)
				lastErrStatus = httpResp.StatusCode
				slog.WarnContext(ctx, "responses_request_failed", "error", retryErrMessage, "status_code", httpResp.StatusCode, "url", fullURL, "model", model.ModelName, "retry_after", retryAfter)
				errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: status %d (url=%s, model=%s): %s", attempts+1, now, httpResp.StatusCode, fullURL, model.ModelName, retryErrMessage))
				continue

			case httpResp.StatusCode == http.StatusTooManyRequests:
				retryAfter = llm.ParseRetryAfter(httpResp.Header.Get("Retry-After"))
				lastErrSummary = "rate limited: " + llm.Truncate(errMessage, 160)
				lastErrStatus = httpResp.StatusCode
				slog.WarnContext(ctx, "responses_request_rate_limited", "error", retryErrMessage, "url", fullURL, "model", model.ModelName, "retry_after", retryAfter)
				errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: status %d (rate limited, url=%s, model=%s): %s", attempts+1, now, httpResp.StatusCode, fullURL, model.ModelName, retryErrMessage))
				continue

			case httpResp.StatusCode >= 400:
				slog.WarnContext(ctx, "responses_request_failed", "error", terminalErrMessage, "status_code", httpResp.StatusCode, "url", fullURL, "model", model.ModelName)
				attemptErr := fmt.Errorf("attempt %d at %s: status %d (url=%s, model=%s): %s", attempts+1, now, httpResp.StatusCode, fullURL, model.ModelName, terminalErrMessage)
				return nil, newResponsesRequestError(errors.Join(errs, attemptErr), false)

			default:
				slog.WarnContext(ctx, "responses_request_failed", "status_code", httpResp.StatusCode, "url", fullURL, "model", model.ModelName, "body", terminalErrMessage)
				attemptErr := fmt.Errorf("attempt %d at %s: status %d (url=%s, model=%s): %s", attempts+1, now, httpResp.StatusCode, fullURL, model.ModelName, terminalErrMessage)
				return nil, newResponsesRequestError(errors.Join(errs, attemptErr), false)
			}
		}

		var resp responsesResponse
		if responsesShouldParseStream(req, httpResp.Header) {
			streamResp, err := parseResponsesSSEStream(httpResp.Body, ir.OnStream)
			httpResp.Body.Close()
			if err != nil {
				now := time.Now().Format(time.DateTime)
				lastErrSummary = "stream: " + llm.Truncate(err.Error(), 160)
				lastErrStatus = 0
				slog.WarnContext(ctx, "responses_request_stream_failed", "error", err, "url", fullURL, "model", model.ModelName)
				attemptErr := fmt.Errorf("attempt %d at %s: stream response body (url=%s, model=%s): %w", attempts+1, now, fullURL, model.ModelName, err)
				info, classified := llm.RequestErrorInfoFromError(err)
				lastRequestErrorInfo = info
				if classified && !info.Retryable {
					return nil, newResponsesRequestError(errors.Join(errs, attemptErr), false)
				}
				errs = errors.Join(errs, attemptErr)
				continue
			}
			resp = *streamResp
		} else {
			body, err := io.ReadAll(httpResp.Body)
			httpResp.Body.Close()
			if err != nil {
				// A severed read of an otherwise-successful response is a
				// transport hiccup, not a verdict on the request: retry it. The SSE
				// branch above retries any stream error, and the two branches
				// differ only in framing, so this must not be stricter.
				now := time.Now().Format(time.DateTime)
				lastErrSummary = "read: " + llm.Truncate(err.Error(), 160)
				lastErrStatus = 0
				slog.WarnContext(ctx, "responses_request_read_failed", "error", err, "url", fullURL, "model", model.ModelName)
				lastRequestErrorInfo, _ = llm.RequestErrorInfoFromError(err)
				errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: read response body (url=%s, model=%s): %w", attempts+1, now, fullURL, model.ModelName, err))
				continue
			}

			if err := json.Unmarshal(body, &resp); err != nil {
				if shouldRetryResponsesDecodeError(err, body) {
					now := time.Now().Format(time.DateTime)
					lastErrSummary = "decode: " + llm.Truncate(err.Error(), 160)
					lastErrStatus = 0
					slog.WarnContext(ctx, "responses_request_decode_failed", "error", err, "url", fullURL, "model", model.ModelName, "body_length", len(body))
					errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: decode response body (url=%s, model=%s, bytes=%d): %w", attempts+1, now, fullURL, model.ModelName, len(body), err))
					continue
				}
				return nil, newResponsesRequestError(errors.Join(errs, fmt.Errorf("attempt %d at %s: failed to unmarshal response (url=%s, model=%s, bytes=%d): %w", attempts+1, time.Now().Format(time.DateTime), fullURL, model.ModelName, len(body), err)), false)
			}
		}

		// Check for errors in the response. A complete JSON error response was
		// terminal before streaming support; keep that behavior rather than
		// treating an unclassified application error as a transport failure.
		if resp.Error != nil {
			attemptErr := fmt.Errorf("attempt %d at %s (url=%s, model=%s): response contains error: %s", attempts+1, time.Now().Format(time.DateTime), fullURL, model.ModelName, resp.Error.Message)
			retryable, _ := classifyResponsesError(resp.Error)
			return nil, &responsesRequestError{
				err:              errors.Join(errs, attemptErr),
				retryable:        retryable,
				noImmediateRetry: retryable,
			}
		}

		// Dump response if enabled
		if s.DumpLLM {
			if respJSON, err := json.MarshalIndent(resp, "", "  "); err == nil {
				if err := llm.DumpToFile("response", "", respJSON); err != nil {
					slog.WarnContext(ctx, "failed to dump responses response to file", "error", err)
				}
			}
		}

		result := s.toLLMResponseFromResponses(&resp, httpResp.Header)
		endTime := time.Now()
		result.StartTime = &startTime
		result.EndTime = &endTime
		result.URL = fullURL
		result.Origin = &origin
		return result, nil
	}
}

func (s *ResponsesService) isOpenAIResponses() bool {
	return s.ProviderName == "" || s.ProviderName == "openai"
}

// supportsReasoningSummaries reports whether the provider accepts
// reasoning.summary and returns summary text on reasoning output items.
// xAI's Responses API implements the same contract as OpenAI's (verified
// against grok-4.5, including streamed reasoning_summary_text deltas and
// stateless reasoning replay across tool turns).
func (s *ResponsesService) supportsReasoningSummaries() bool {
	return s.isOpenAIResponses() || s.ProviderName == "xai"
}

type responsesSSEEvent struct {
	EventType string
	Data      string
}

type responsesStreamEvent struct {
	Type         string               `json:"type"`
	Response     *responsesResponse   `json:"response,omitempty"`
	Error        *responsesError      `json:"error,omitempty"`
	Code         json.RawMessage      `json:"code,omitempty"`
	Message      string               `json:"message,omitempty"`
	Delta        string               `json:"delta,omitempty"`
	ContentIndex int                  `json:"content_index,omitempty"`
	OutputIndex  int                  `json:"output_index,omitempty"`
	Item         *responsesOutputItem `json:"item,omitempty"`
}

func responsesResponseIsSSE(h http.Header) bool {
	return strings.Contains(strings.ToLower(h.Get("Content-Type")), "text/event-stream")
}

func responsesShouldParseStream(req responsesRequest, h http.Header) bool {
	if responsesResponseIsSSE(h) {
		return true
	}
	if !req.Stream {
		return false
	}
	// The ChatGPT subscription backend currently streams SSE with
	// Content-Type: text/plain. Treat stream=true as authoritative unless the
	// server explicitly returns a JSON body.
	return !strings.Contains(strings.ToLower(h.Get("Content-Type")), "json")
}

func iterResponsesSSEEvents(r io.Reader, yield func(responsesSSEEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var (
		eventType string
		dataLines []string
		hasData   bool
	)

	dispatch := func() error {
		if !hasData {
			eventType = ""
			return nil
		}
		ev := responsesSSEEvent{
			EventType: eventType,
			Data:      strings.Join(dataLines, "\n"),
		}
		eventType = ""
		dataLines = dataLines[:0]
		hasData = false
		return yield(ev)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}

		field, value, ok := strings.Cut(line, ":")
		if ok && strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		if !ok {
			field = line
			value = ""
		}

		switch field {
		case "event":
			eventType = value
		case "data":
			dataLines = append(dataLines, value)
			hasData = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading SSE stream: %w", err)
	}
	return dispatch()
}

func parseResponsesSSEStream(r io.Reader, onStream func(llm.StreamDelta)) (*responsesResponse, error) {
	var completed *responsesResponse
	// Responses streams finalize message/tool output in output_item.done.
	// response.completed may only carry completion metadata and usage.
	outputItems := make(map[int]responsesOutputItem)
	citeFilter := llm.NewCitationStreamFilter()
	err := iterResponsesSSEEvents(r, func(sse responsesSSEEvent) error {
		if sse.Data == "[DONE]" {
			return nil
		}

		var event responsesStreamEvent
		if err := json.Unmarshal([]byte(sse.Data), &event); err != nil {
			return fmt.Errorf("parsing SSE event (event=%q): %w", sse.EventType, err)
		}
		eventType := event.Type
		if eventType == "" {
			eventType = sse.EventType
		}

		switch eventType {
		case "response.output_text.delta":
			if onStream != nil && event.Delta != "" {
				if filtered := citeFilter.Filter(event.OutputIndex, event.ContentIndex, event.Delta); filtered != "" {
					onStream(llm.StreamDelta{Type: "text", Text: filtered, Index: event.ContentIndex})
				}
			}
		case "response.output_text.done":
			if onStream != nil {
				if filtered := citeFilter.Finish(event.OutputIndex, event.ContentIndex); filtered != "" {
					onStream(llm.StreamDelta{Type: "text", Text: filtered, Index: event.ContentIndex})
				}
			}
		case "response.reasoning_summary_text.delta":
			if onStream != nil && event.Delta != "" {
				onStream(llm.StreamDelta{Type: "thinking", Text: event.Delta, Index: event.ContentIndex})
			}
		case "response.output_item.done":
			if event.Item != nil {
				outputItems[event.OutputIndex] = *event.Item
			}
		case "response.completed", "response.incomplete":
			if event.Response == nil {
				return fmt.Errorf("%s event has no response", eventType)
			}
			completed = event.Response
			completed.Output = mergeResponsesStreamOutput(completed.Output, outputItems)
		case "response.failed":
			if event.Response != nil && event.Response.Error != nil {
				return newResponsesRequestError(
					fmt.Errorf("response failed: %s", event.Response.Error.Message),
					responsesErrorRetryable(event.Response.Error),
				)
			}
			return newResponsesRequestError(errors.New("response failed"), true)
		case "error":
			apiErr := event.Error
			if apiErr != nil {
				merged := *apiErr
				if merged.Message == "" {
					merged.Message = event.Message
				}
				if codeMissing(merged.Code) {
					merged.Code = event.Code
				}
				apiErr = &merged
			} else if event.Message != "" || len(event.Code) > 0 {
				apiErr = &responsesError{Message: event.Message, Code: event.Code}
			}
			message := sse.Data
			if apiErr != nil && apiErr.Message != "" {
				message = apiErr.Message
			}
			return newResponsesRequestError(
				fmt.Errorf("stream error event: %s", message),
				responsesErrorRetryable(apiErr),
			)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Only after a clean end: an errored stream has no complete response to
	// finalize, and retryable failures may replay the text from the top.
	if onStream != nil {
		for _, delta := range citeFilter.FinishAll() {
			onStream(delta)
		}
	}
	if completed == nil {
		return nil, fmt.Errorf("incomplete stream: no response.completed event")
	}
	return completed, nil
}

func mergeResponsesStreamOutput(final []responsesOutputItem, streamed map[int]responsesOutputItem) []responsesOutputItem {
	if len(final) == 0 {
		indexes := make([]int, 0, len(streamed))
		for index := range streamed {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		output := make([]responsesOutputItem, 0, len(indexes))
		for _, index := range indexes {
			output = append(output, streamed[index])
		}
		return output
	}

	for index := range final {
		streamedItem, ok := streamed[index]
		if !ok || final[index].Type != "reasoning" || streamedItem.Type != "reasoning" {
			continue
		}
		if final[index].ID != "" && streamedItem.ID != "" && final[index].ID != streamedItem.ID {
			continue
		}
		if final[index].EncryptedContent == "" {
			final[index].EncryptedContent = streamedItem.EncryptedContent
		}
		if len(final[index].Summary) == 0 {
			final[index].Summary = streamedItem.Summary
		}
	}
	return final
}

func codeMissing(code json.RawMessage) bool {
	trimmed := bytes.TrimSpace(code)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func shouldRetryResponsesDecodeError(err error, body []byte) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if len(bytes.TrimSpace(body)) == 0 && errors.Is(err, io.EOF) {
		return true
	}

	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) && strings.Contains(err.Error(), "unexpected end of JSON") {
		return true
	}

	return false
}

func (s *ResponsesService) PatchProfile() string {
	if s.isOpenAIResponses() && s.Model.SupportsApplyPatch {
		return "codex_apply_patch"
	}
	return "flat"
}

// ConfigDetails returns configuration information for logging
func (s *ResponsesService) ConfigDetails() map[string]string {
	model := cmp.Or(s.Model, DefaultModel)
	baseURL := cmp.Or(s.ModelURL, model.URL, OpenAIURL)
	return map[string]string{
		"base_url":         baseURL,
		"model_name":       model.ModelName,
		"full_url":         baseURL + "/responses",
		"api_key_env":      model.APIKeyEnv,
		"has_api_key_set":  fmt.Sprintf("%v", s.APIKey != ""),
		"reasoning_replay": string(s.ReasoningReplay),
	}
}
