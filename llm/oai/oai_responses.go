package oai

import (
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
	MaxTokens     int               // defaults to DefaultMaxTokens if zero
	Org           string            // optional - organization ID
	DumpLLM       bool              // whether to dump request/response text to files for debugging; defaults to false
	ThinkingLevel llm.ThinkingLevel // thinking level (ThinkingLevelOff disables reasoning)

	// ReasoningEffort, if non-empty, is used as the reasoning.effort value sent to
	// the OpenAI Responses API verbatim, overriding ThinkingLevel. This allows
	// custom-model configurations to pass through provider-specific values
	// (e.g. "xhigh", "none") without Shelley needing to know them.
	ReasoningEffort string
}

var _ llm.Service = (*ResponsesService)(nil)

// Responses API request/response types

type responsesRequest struct {
	Model           string               `json:"model"`
	Input           []responsesInputItem `json:"input"`
	Tools           []responsesTool      `json:"tools,omitempty"`
	ToolChoice      any                  `json:"tool_choice,omitempty"`
	MaxOutputTokens int                  `json:"max_output_tokens,omitempty"`
	Reasoning       *responsesReasoning  `json:"reasoning,omitempty"`
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
	Type string `json:"type"` // "input_text", "output_text"
	Text string `json:"text"`
}

type responsesTool struct {
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name"`
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
	Type      string             `json:"type"`           // "message", "reasoning", "function_call"
	Role      string             `json:"role,omitempty"` // for messages: "assistant"
	Status    string             `json:"status,omitempty"`
	Content   []responsesContent `json:"content,omitempty"`   // for messages
	CallID    string             `json:"call_id,omitempty"`   // for function_call
	Name      string             `json:"name,omitempty"`      // for function_call
	Arguments string             `json:"arguments,omitempty"` // for function_call
	Summary   []responsesSummary `json:"summary,omitempty"`   // for reasoning
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
		if c.Type == llm.ContentTypeToolResult {
			toolResults = append(toolResults, c)
		} else {
			regularContent = append(regularContent, c)
		}
	}

	// Process tool results first - they need to come before the assistant message
	for _, tr := range toolResults {
		// Collect all text from content objects
		var texts []string
		for _, result := range tr.ToolResult {
			if strings.TrimSpace(result.Text) != "" {
				texts = append(texts, result.Text)
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
	}

	// Process regular content
	if len(regularContent) > 0 {
		var messageContent []responsesContent
		var functionCalls []responsesInputItem

		for _, c := range regularContent {
			switch c.Type {
			case llm.ContentTypeText:
				if c.Text != "" {
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

// fromLLMToolResponses converts llm.Tool to Responses API tool format
func fromLLMToolResponses(t *llm.Tool) responsesTool {
	return responsesTool{
		Type:        "function",
		Name:        t.Name,
		Description: t.Description,
		Parameters:  t.InputSchema,
	}
}

// fromLLMSystemResponses converts llm.SystemContent to Responses API input items
func fromLLMSystemResponses(systemContent []llm.SystemContent) []responsesInputItem {
	if len(systemContent) == 0 {
		return nil
	}

	// Combine all system content into a single system message
	var systemText string
	for i, content := range systemContent {
		if i > 0 && systemText != "" && content.Text != "" {
			systemText += "\n"
		}
		systemText += content.Text
	}

	if systemText == "" {
		return nil
	}

	return []responsesInputItem{
		{
			Type: "message",
			Role: "user",
			Content: []responsesContent{
				{
					Type: "input_text",
					Text: systemText,
				},
			},
		},
	}
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
					contents = append(contents, llm.Content{
						Type: llm.ContentTypeText,
						Text: c.Text,
					})
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

// TokenContextWindow returns the maximum token context window size for this service
func (s *ResponsesService) TokenContextWindow() int {
	model := cmp.Or(s.Model, DefaultModel)

	// Use the same context window logic as the regular service
	switch model.ModelName {
	case "gpt-5.5", "gpt-5.5-2026-04-23", "gpt-5.5-pro", "gpt-5.5-pro-2026-04-23":
		return 272000 // 272k for the GPT-5.5 family in Shelley
	case "gpt-5.4":
		return 304000 // 304k for gpt-5.4
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

// Do sends a request to OpenAI using the Responses API.
func (s *ResponsesService) Do(ctx context.Context, ir *llm.Request) (*llm.Response, error) {
	httpc := cmp.Or(s.HTTPC, http.DefaultClient)
	model := cmp.Or(s.Model, DefaultModel)

	// Start with system messages if provided
	var allInput []responsesInputItem
	if len(ir.System) > 0 {
		sysItems := fromLLMSystemResponses(ir.System)
		allInput = append(allInput, sysItems...)
	}

	// Add regular messages
	for _, msg := range ir.Messages {
		items := fromLLMMessageResponses(msg)
		allInput = append(allInput, items...)
	}

	// Convert tools
	var tools []responsesTool
	for _, t := range ir.Tools {
		tools = append(tools, fromLLMToolResponses(t))
	}

	// Create the request
	req := responsesRequest{
		Model:           model.ModelName,
		Input:           allInput,
		Tools:           tools,
		MaxOutputTokens: cmp.Or(s.MaxTokens, DefaultMaxTokens),
	}

	// Add reasoning if thinking is enabled. ReasoningEffort, if set, takes
	// precedence over ThinkingLevel and is passed through verbatim.
	if s.ReasoningEffort != "" {
		req.Reasoning = &responsesReasoning{Effort: s.ReasoningEffort}
	} else if s.ThinkingLevel != llm.ThinkingLevelOff {
		effort := s.ThinkingLevel.ThinkingEffort()
		if effort != "" {
			req.Reasoning = &responsesReasoning{Effort: effort}
		}
	}

	// Add tool choice if specified
	if ir.ToolChoice != nil {
		req.ToolChoice = fromLLMToolChoice(ir.ToolChoice)
	}

	// Construct the full URL
	baseURL := cmp.Or(s.ModelURL, model.URL, OpenAIURL)
	fullURL := baseURL + "/responses"

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

	// Retry mechanism
	backoff := []time.Duration{1 * time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second}

	// retry loop
	var errs error // accumulated errors across all attempts
	for attempts := 0; ; attempts++ {
		if attempts > 10 {
			return nil, fmt.Errorf("responses request failed after %d attempts (url=%s, model=%s): %w", attempts, fullURL, model.ModelName, errs)
		}
		if attempts > 0 {
			sleep := backoff[min(attempts, len(backoff)-1)] + time.Duration(rand.Int64N(int64(time.Second)))
			slog.WarnContext(ctx, "responses request sleep before retry", "sleep", sleep, "attempts", attempts)
			time.Sleep(sleep)
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
			errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: %w", attempts+1, time.Now().Format(time.DateTime), err))
			continue
		}
		defer httpResp.Body.Close()

		// Read response body
		body, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}

		// Handle non-200 responses
		if httpResp.StatusCode != http.StatusOK {
			var apiErr responsesError
			if jsonErr := json.Unmarshal(body, &struct {
				Error *responsesError `json:"error"`
			}{Error: &apiErr}); jsonErr == nil && apiErr.Message != "" {
				// We have a structured error
				now := time.Now().Format(time.DateTime)
				switch {
				case httpResp.StatusCode >= 500:
					// Server error, retry
					slog.WarnContext(ctx, "responses_request_failed", "error", apiErr.Message, "status_code", httpResp.StatusCode, "url", fullURL, "model", model.ModelName)
					errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: status %d (url=%s, model=%s): %s", attempts+1, now, httpResp.StatusCode, fullURL, model.ModelName, apiErr.Message))
					continue

				case httpResp.StatusCode == 429:
					// Rate limited, retry
					slog.WarnContext(ctx, "responses_request_rate_limited", "error", apiErr.Message, "url", fullURL, "model", model.ModelName)
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

		// Parse successful response
		var resp responsesResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("failed to unmarshal response: %w", err)
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
