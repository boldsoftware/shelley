package oai

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"shelley.exe.dev/llm"
)

// ResponsesService provides chat completions using the OpenAI Responses API.
// This API is required for models like gpt-5.1-codex.
// Fields should not be altered concurrently with calling any method on ResponsesService.
type ResponsesService struct {
	HTTPC         *http.Client      // defaults to http.DefaultClient if nil
	APIKey        string            // optional, if not set will try to load from env var
	Model         Model             // defaults to DefaultModel if zero value
	ModelURL      string            // optional, overrides Model.URL
	Org           string            // optional - organization ID
	DumpLLM       bool              // whether to dump request/response text to files for debugging; defaults to false
	ThinkingLevel llm.ThinkingLevel // service-level default; zero (ThinkingLevelDefault) and ThinkingLevelOff both leave the field off the wire
	ProviderName  string            // e.g., "openai"

	// ReasoningEffort, if non-empty, is used as the reasoning.effort value sent to
	// the OpenAI Responses API verbatim, overriding ThinkingLevel. This allows
	// custom-model configurations to pass through provider-specific values
	// (e.g. "xhigh", "none") without Shelley needing to know them.
	ReasoningEffort string
}

var _ llm.Service = (*ResponsesService)(nil)

// Responses API request/response types

type responsesRequest struct {
	Model        string               `json:"model"`
	Instructions string               `json:"instructions,omitempty"`
	Store        bool                 `json:"store"`
	Stream       bool                 `json:"stream"`
	Input        []responsesInputItem `json:"input"`
	Tools        []responsesTool      `json:"tools,omitempty"`
	ToolChoice   any                  `json:"tool_choice,omitempty"`
	Reasoning    *responsesReasoning  `json:"reasoning,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"` // "low", "medium", "high"
}

type responsesInputItem struct {
	Type      string             `json:"type"`                // "message", "function_call", "function_call_output"
	Role      string             `json:"role,omitempty"`      // for messages: "user", "assistant"
	Content   []responsesContent `json:"content,omitempty"`   // for messages
	CallID    string             `json:"call_id,omitempty"`   // for function_call and function_call_output
	Name      string             `json:"name,omitempty"`      // for function_call
	Arguments string             `json:"arguments,omitempty"` // for function_call
	Output    string             `json:"output,omitempty"`    // for function_call_output
}

type responsesContent struct {
	Type        string                `json:"type"` // "input_text", "output_text", "input_image"
	Text        string                `json:"text,omitempty"`
	ImageURL    string                `json:"image_url,omitempty"`
	Detail      responsesImageDetail  `json:"detail,omitempty"`
	Annotations []responsesAnnotation `json:"annotations,omitempty"`
}

// responsesAnnotation is an annotation attached to output_text content.
// For web_search results, OpenAI emits url_citation annotations.
type responsesAnnotation struct {
	Type       string `json:"type"` // "url_citation"
	StartIndex int    `json:"start_index,omitempty"`
	EndIndex   int    `json:"end_index,omitempty"`
	URL        string `json:"url,omitempty"`
	Title      string `json:"title,omitempty"`
}

type responsesImageDetail string

const responsesImageDetailAuto responsesImageDetail = "auto"

type responsesTool struct {
	Type        string          `json:"type"` // "function" or provider-hosted tool type
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type responsesResponse struct {
	ID        string                `json:"id"`
	Object    string                `json:"object"` // "response"
	CreatedAt int64                 `json:"created_at"`
	Status    string                `json:"status"` // "completed", "incomplete", etc.
	Model     string                `json:"model"`
	Output    []responsesOutputItem `json:"output"`
	Usage     responsesUsage        `json:"usage"`
	Error     *responsesError       `json:"error"`
}

type responsesOutputItem struct {
	ID        string             `json:"id"`
	Type      string             `json:"type"`           // "message", "reasoning", "function_call", "web_search_call"
	Role      string             `json:"role,omitempty"` // for messages: "assistant"
	Status    string             `json:"status,omitempty"`
	Content   []responsesContent `json:"content,omitempty"`   // for messages
	CallID    string             `json:"call_id,omitempty"`   // for function_call
	Name      string             `json:"name,omitempty"`      // for function_call
	Arguments string             `json:"arguments,omitempty"` // for function_call
	Summary   []responsesSummary `json:"summary,omitempty"`   // for reasoning
	Action    *responsesAction   `json:"action,omitempty"`    // for web_search_call (queries)
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
	InputTokensDetails  *responsesInputTokensDetails  `json:"input_tokens_details,omitempty"`
	OutputTokens        int                           `json:"output_tokens"`
	OutputTokensDetails *responsesOutputTokensDetails `json:"output_tokens_details,omitempty"`
	TotalTokens         int                           `json:"total_tokens"`
}

type responsesInputTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type responsesOutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type responsesError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    string `json:"code"`
}

// fromLLMMessageResponses converts llm.Message to Responses API input items
func fromLLMMessageResponses(msg llm.Message) []responsesInputItem {
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

		items = append(items, responsesInputItem{
			Type:   "function_call_output",
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

	// Process regular content
	if len(regularContent) > 0 {
		var messageContent []responsesContent
		var functionCalls []responsesInputItem

		for _, c := range regularContent {
			switch c.Type {
			case llm.ContentTypeText:
				if isImageContent(c) {
					messageContent = append(messageContent, responsesImageContent(c))
				} else if c.Text != "" {
					contentType := "input_text"
					if msg.Role == llm.MessageRoleAssistant {
						contentType = "output_text"
					}
					messageContent = append(messageContent, responsesContent{
						Type: contentType,
						Text: c.Text,
					})
				}
			case llm.ContentTypeToolUse:
				// Tool use becomes a function_call in the input
				functionCalls = append(functionCalls, responsesInputItem{
					Type:      "function_call",
					CallID:    c.ID,
					Name:      c.ToolName,
					Arguments: string(c.ToolInput),
				})
			}
		}

		// Add message if it has content
		if len(messageContent) > 0 {
			role := "user"
			if msg.Role == llm.MessageRoleAssistant {
				role = "assistant"
			}
			items = append(items, responsesInputItem{
				Type:    "message",
				Role:    role,
				Content: messageContent,
			})
		}

		// Add function calls
		items = append(items, functionCalls...)
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
	return responsesTool{
		Type:        "function",
		Name:        t.Name,
		Description: t.Description,
		Parameters:  t.InputSchema,
	}
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
				if c.Text != "" {
					text := llm.Content{
						Type: llm.ContentTypeText,
						Text: c.Text,
					}
					if len(c.Annotations) > 0 {
						if b, err := json.Marshal(c.Annotations); err == nil {
							text.Citations = b
						}
					}
					contents = append(contents, text)
				}
			}
		case "reasoning":
			// Convert reasoning to thinking content
			if len(item.Summary) > 0 {
				parts := make([]string, 0, len(item.Summary))
				for _, s := range item.Summary {
					if s.Text != "" {
						parts = append(parts, s.Text)
					}
				}
				if len(parts) > 0 {
					contents = append(contents, llm.Content{
						Type: llm.ContentTypeThinking,
						Text: strings.Join(parts, "\n"),
					})
				}
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
			// Convert function call to tool use
			contents = append(contents, llm.Content{
				ID:        item.CallID,
				Type:      llm.ContentTypeToolUse,
				ToolName:  item.Name,
				ToolInput: json.RawMessage(item.Arguments),
			})
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
//
// OpenAI's Responses API reports input_tokens as the total input (including cached),
// with input_tokens_details.cached_tokens as the cached subset.
// Our Usage struct follows Anthropic's convention where InputTokens is the non-cached
// portion and TotalInputTokens() = InputTokens + CacheCreationInputTokens + CacheReadInputTokens.
// So we map: InputTokens = total - cached, CacheReadInputTokens = cached, CacheCreationInputTokens = 0.
func (s *ResponsesService) toLLMUsageFromResponses(usage responsesUsage, headers http.Header) llm.Usage {
	totalIn := uint64(usage.InputTokens)
	var cached uint64
	if usage.InputTokensDetails != nil {
		cached = uint64(usage.InputTokensDetails.CachedTokens)
	}
	out := uint64(usage.OutputTokens)
	u := llm.Usage{
		InputTokens:          totalIn - cached,
		CacheReadInputTokens: cached,
		OutputTokens:         out,
	}
	u.CostUSD = llm.CostUSDFromResponse(headers)
	return u
}

func (s *ResponsesService) Provider() string { return s.ProviderName }

func (s *ResponsesService) SupportsServerSideWebSearch() bool { return true }

// SupportsImages reports whether this service accepts image inputs.
// OpenAI Responses API supports images for vision models; set
// Model.SupportsImages to enable image inputs.
func (s *ResponsesService) SupportsImages() bool { return s.Model.SupportsImages }

// TokenContextWindow returns the maximum token context window size for this service
func (s *ResponsesService) TokenContextWindow() int {
	model := cmp.Or(s.Model, DefaultModel)

	// Use the same context window logic as the regular service
	switch model.ModelName {
	case "gpt-5.5", "gpt-5.5-2026-04-23", "gpt-5.5-pro", "gpt-5.5-pro-2026-04-23":
		return 272000 // 272k for the GPT-5.5 family in Shelley
	case "gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano":
		return 304000 // 304k for the GPT-5.4 family
	case "gpt-5.3-codex":
		return 288000 // 288k for gpt-5.3-codex
	case "gpt-5.2-codex":
		return 272000 // 272k for gpt-5.2-codex
	case "gpt-5.1-codex":
		return 256000 // 256k for gpt-5.1-codex
	case "gpt-4.1-2025-04-14", "gpt-4.1-mini-2025-04-14", "gpt-4.1-nano-2025-04-14":
		return 200000
	case "gpt-4o-2024-08-06", "gpt-4o-mini-2024-07-18":
		return 128000
	default:
		return 128000
	}
}

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
	httpc := cmp.Or(s.HTTPC, http.DefaultClient)
	model := cmp.Or(s.Model, DefaultModel)

	var allInput []responsesInputItem
	for _, msg := range ir.Messages {
		items := fromLLMMessageResponses(msg)
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
		Model:        model.ModelName,
		Instructions: responsesInstructionsFromLLMSystem(ir.System),
		Store:        false,
		Stream:       true,
		Input:        allInput,
		Tools:        tools,
	}

	// Add reasoning. Precedence:
	//   1. ir.ThinkingLevel (request-level override from the caller)
	//   2. s.ReasoningEffort (custom verbatim string from per-model config)
	//   3. s.ThinkingLevel (service-level default)
	level := llm.EffectiveThinkingLevel(s.ThinkingLevel, ir.ThinkingLevel)
	var effort string
	switch {
	case ir.ThinkingLevel == llm.ThinkingLevelOff:
		effort = ""
	case ir.ThinkingLevel != llm.ThinkingLevelDefault:
		effort = ir.ThinkingLevel.ThinkingEffort()
	case s.ReasoningEffort != "":
		effort = s.ReasoningEffort
	case level != llm.ThinkingLevelOff:
		effort = level.ThinkingEffort()
	}
	// gpt-5.x-codex rejects `reasoning.effort="minimal"` with HTTP 400;
	// clamp to "low". Verbatim user-supplied values (s.ReasoningEffort) are
	// intentionally NOT clamped.
	if effort == "minimal" && effort != s.ReasoningEffort && strings.Contains(model.ModelName, "codex") {
		effort = "low"
	}
	if effort != "" {
		req.Reasoning = &responsesReasoning{Effort: effort}
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
	backoff := []time.Duration{
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

	// retry loop
	retryStart := time.Now()
	var errs error               // accumulated errors across all attempts
	var lastErrSummary string    // short description of the most recent attempt failure
	var retryAfter time.Duration // hint from upstream Retry-After header, reset each attempt
	for attempts := 0; ; attempts++ {
		if attempts > 15 {
			return nil, fmt.Errorf("responses request failed after %d attempts (url=%s, model=%s): %w", attempts, fullURL, model.ModelName, errs)
		}
		if attempts > 0 {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("responses request failed after %d attempts (context cancelled): %w", attempts, errs)
			}
			sleep := backoff[min(attempts, len(backoff)-1)] + time.Duration(rand.Int64N(int64(time.Second)))
			if retryAfter > sleep {
				sleep = retryAfter
			}
			retryAfter = 0
			slog.WarnContext(ctx, "responses request sleep before retry", "sleep", sleep, "attempts", attempts, "elapsed", time.Since(retryStart).Round(time.Second), "last_error", lastErrSummary)
			if ir.OnRetry != nil {
				ir.OnRetry(llm.RetryEvent{Attempt: attempts + 1, Sleep: sleep, Err: lastErrSummary, Provider: "openai", Model: model.ModelName})
			}
			select {
			case <-time.After(sleep):
			case <-ctx.Done():
				return nil, fmt.Errorf("responses request failed after %d attempts (context cancelled during backoff): %w", attempts, errs)
			}
		}

		// Create HTTP request
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
			errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: %w", attempts+1, time.Now().Format(time.DateTime), err))
			continue
		}
		if httpResp.StatusCode != http.StatusOK {
			body, readErr := io.ReadAll(httpResp.Body)
			httpResp.Body.Close()
			if readErr != nil {
				if shouldRetryResponsesReadError(readErr) {
					now := time.Now().Format(time.DateTime)
					lastErrSummary = "read: " + llm.Truncate(readErr.Error(), 160)
					slog.WarnContext(ctx, "responses_request_read_failed", "error", readErr, "url", fullURL, "model", model.ModelName)
					errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: read response body (url=%s, model=%s): %w", attempts+1, now, fullURL, model.ModelName, readErr))
					continue
				}
				return nil, errors.Join(errs, fmt.Errorf("attempt %d at %s: failed to read response body (url=%s, model=%s): %w", attempts+1, time.Now().Format(time.DateTime), fullURL, model.ModelName, readErr))
			}

			var apiErr responsesError
			if jsonErr := json.Unmarshal(body, &struct {
				Error *responsesError `json:"error"`
			}{Error: &apiErr}); jsonErr == nil && apiErr.Message != "" {
				// We have a structured error
				now := time.Now().Format(time.DateTime)
				switch {
				case httpResp.StatusCode >= 500:
					// Server error, retry
					retryAfter = llm.ParseRetryAfter(httpResp.Header.Get("Retry-After"))
					lastErrSummary = fmt.Sprintf("status %d: %s", httpResp.StatusCode, llm.Truncate(apiErr.Message, 160))
					slog.WarnContext(ctx, "responses_request_failed", "error", apiErr.Message, "status_code", httpResp.StatusCode, "url", fullURL, "model", model.ModelName, "retry_after", retryAfter)
					errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: status %d (url=%s, model=%s): %s", attempts+1, now, httpResp.StatusCode, fullURL, model.ModelName, apiErr.Message))
					continue

				case httpResp.StatusCode == 429:
					// Rate limited, retry
					retryAfter = llm.ParseRetryAfter(httpResp.Header.Get("Retry-After"))
					lastErrSummary = fmt.Sprintf("status 429 rate limited: %s", llm.Truncate(apiErr.Message, 160))
					slog.WarnContext(ctx, "responses_request_rate_limited", "error", apiErr.Message, "url", fullURL, "model", model.ModelName, "retry_after", retryAfter)
					errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: status %d (rate limited, url=%s, model=%s): %s", attempts+1, now, httpResp.StatusCode, fullURL, model.ModelName, apiErr.Message))
					continue

				case httpResp.StatusCode >= 400 && httpResp.StatusCode < 500:
					// Client error, probably unrecoverable
					slog.WarnContext(ctx, "responses_request_failed", "error", apiErr.Message, "status_code", httpResp.StatusCode, "url", fullURL, "model", model.ModelName)
					return nil, errors.Join(errs, fmt.Errorf("attempt %d at %s: status %d (url=%s, model=%s): %s", attempts+1, now, httpResp.StatusCode, fullURL, model.ModelName, apiErr.Message))
				}
			}

			// No structured error, use the raw body
			slog.WarnContext(ctx, "responses_request_failed", "status_code", httpResp.StatusCode, "url", fullURL, "model", model.ModelName, "body", string(body))
			return nil, fmt.Errorf("status %d (url=%s, model=%s): %s", httpResp.StatusCode, fullURL, model.ModelName, string(body))
		}

		var resp responsesResponse
		if responsesShouldParseStream(req, httpResp.Header) {
			streamResp, err := parseResponsesSSEStream(httpResp.Body, ir.OnStream)
			httpResp.Body.Close()
			if err != nil {
				now := time.Now().Format(time.DateTime)
				lastErrSummary = "stream: " + llm.Truncate(err.Error(), 160)
				slog.WarnContext(ctx, "responses_request_stream_failed", "error", err, "url", fullURL, "model", model.ModelName)
				errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: stream response body (url=%s, model=%s): %w", attempts+1, now, fullURL, model.ModelName, err))
				continue
			}
			resp = *streamResp
		} else {
			body, err := io.ReadAll(httpResp.Body)
			httpResp.Body.Close()
			if err != nil {
				if shouldRetryResponsesReadError(err) {
					now := time.Now().Format(time.DateTime)
					lastErrSummary = "read: " + llm.Truncate(err.Error(), 160)
					slog.WarnContext(ctx, "responses_request_read_failed", "error", err, "url", fullURL, "model", model.ModelName)
					errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: read response body (url=%s, model=%s): %w", attempts+1, now, fullURL, model.ModelName, err))
					continue
				}
				return nil, errors.Join(errs, fmt.Errorf("attempt %d at %s: failed to read response body (url=%s, model=%s): %w", attempts+1, time.Now().Format(time.DateTime), fullURL, model.ModelName, err))
			}

			if err := json.Unmarshal(body, &resp); err != nil {
				if shouldRetryResponsesDecodeError(err, body) {
					now := time.Now().Format(time.DateTime)
					slog.WarnContext(ctx, "responses_request_decode_failed", "error", err, "url", fullURL, "model", model.ModelName, "body_length", len(body))
					errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: decode response body (url=%s, model=%s, bytes=%d): %w", attempts+1, now, fullURL, model.ModelName, len(body), err))
					continue
				}
				return nil, errors.Join(errs, fmt.Errorf("attempt %d at %s: failed to unmarshal response (url=%s, model=%s, bytes=%d): %w", attempts+1, time.Now().Format(time.DateTime), fullURL, model.ModelName, len(body), err))
			}
		}

		// Check for errors in the response
		if resp.Error != nil {
			return nil, fmt.Errorf("response contains error: %s", resp.Error.Message)
		}

		// Dump response if enabled
		if s.DumpLLM {
			if respJSON, err := json.MarshalIndent(resp, "", "  "); err == nil {
				if err := llm.DumpToFile("response", "", respJSON); err != nil {
					slog.WarnContext(ctx, "failed to dump responses response to file", "error", err)
				}
			}
		}

		return s.toLLMResponseFromResponses(&resp, httpResp.Header), nil
	}
}

func shouldRetryResponsesReadError(err error) bool {
	return errors.Is(err, io.ErrUnexpectedEOF)
}

type responsesSSEEvent struct {
	EventType string
	Data      string
}

type responsesStreamEvent struct {
	Type         string               `json:"type"`
	Response     *responsesResponse   `json:"response,omitempty"`
	Error        *responsesError      `json:"error,omitempty"`
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
	var outputItems []responsesOutputItem
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
				onStream(llm.StreamDelta{Type: "text", Text: event.Delta, Index: event.ContentIndex})
			}
		case "response.reasoning_summary_text.delta":
			if onStream != nil && event.Delta != "" {
				onStream(llm.StreamDelta{Type: "thinking", Text: event.Delta, Index: event.ContentIndex})
			}
		case "response.output_item.done":
			if event.Item != nil {
				outputItems = append(outputItems, *event.Item)
			}
		case "response.completed", "response.incomplete":
			if event.Response == nil {
				return fmt.Errorf("%s event has no response", eventType)
			}
			completed = event.Response
			if len(completed.Output) == 0 {
				completed.Output = outputItems
			}
		case "response.failed":
			if event.Response != nil && event.Response.Error != nil {
				return fmt.Errorf("response failed: %s", event.Response.Error.Message)
			}
			return fmt.Errorf("response failed")
		case "error":
			if event.Error != nil && event.Error.Message != "" {
				return fmt.Errorf("stream error event: %s", event.Error.Message)
			}
			if event.Message != "" {
				return fmt.Errorf("stream error event: %s", event.Message)
			}
			return fmt.Errorf("stream error event: %s", sse.Data)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if completed == nil {
		return nil, fmt.Errorf("incomplete stream: no response.completed event")
	}
	return completed, nil
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

func (s *ResponsesService) UseSimplifiedPatch() bool {
	return s.Model.UseSimplifiedPatch
}

// ConfigDetails returns configuration information for logging
func (s *ResponsesService) ConfigDetails() map[string]string {
	model := cmp.Or(s.Model, DefaultModel)
	baseURL := cmp.Or(s.ModelURL, model.URL, OpenAIURL)
	return map[string]string{
		"base_url":        baseURL,
		"model_name":      model.ModelName,
		"full_url":        baseURL + "/responses",
		"api_key_env":     model.APIKeyEnv,
		"has_api_key_set": fmt.Sprintf("%v", s.APIKey != ""),
	}
}
