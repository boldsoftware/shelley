package gem

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/gem/interactions"
	"shelley.exe.dev/models/modelsdev"
)

// DefaultInteractionsMaxTokens matches Google's Interactions API recommendation
// because max_output_tokens includes thinking tokens.
const DefaultInteractionsMaxTokens = 65536

// InteractionsService provides Gemini completions via the stateless Interactions API
// (POST /interactions with store: false and stream: true).
// Fields should not be altered concurrently with calling any method.
type InteractionsService struct {
	HTTPC         *http.Client      // defaults to http.DefaultClient if nil
	URL           string            // Gemini API base URL, uses interactions.DefaultEndpoint if empty
	APIKey        string            // must be non-empty
	Model         string            // model name or models/name; defaults to DefaultModel if empty
	MaxTokens     int               // zero uses the model limit, or DefaultInteractionsMaxTokens if unknown
	ThinkingLevel llm.ThinkingLevel // service-level default; zero leaves thinking_level at model default

	// ReasoningEffort overrides ThinkingLevel. Known models validate the value;
	// unknown models send it verbatim.
	ReasoningEffort string

	// SupportsImages_ controls whether this service advertises image-input support.
	SupportsImages_ bool
}

var _ llm.Service = (*InteractionsService)(nil)

func (s *InteractionsService) modelName() string { return normalizeModelName(s.Model) }

func (s *InteractionsService) modelKey() string { return modelKeyFor(s.modelName()) }

func (s *InteractionsService) Provider() string { return "gemini" }

func (s *InteractionsService) SupportsServerSideWebSearch() bool {
	return supportsServerSideWebSearch(s.modelKey())
}

func (s *InteractionsService) thinkingCapabilities() (modelsdev.ReasoningCapabilities, bool) {
	return modelsdev.LookupGeminiReasoningCapabilities(s.modelName())
}

func (s *InteractionsService) SupportsReasoning() bool {
	caps, found := s.thinkingCapabilities()
	return found && caps.Supported && len(caps.Levels) > 0
}

func (s *InteractionsService) SupportedReasoningLevels() []llm.ThinkingLevel {
	caps, found := s.thinkingCapabilities()
	if !found {
		return nil
	}
	return append([]llm.ThinkingLevel(nil), caps.Levels...)
}

func (s *InteractionsService) SupportsThinkingOff() bool {
	caps, found := s.thinkingCapabilities()
	return found && (!caps.Supported || slices.Contains(caps.Levels, llm.ThinkingLevelOff))
}

func (s *InteractionsService) DefaultReasoningLevel() string {
	level, _, err := s.thinkingConfig(nil)
	if err != nil {
		return ""
	}
	if level != "" {
		return level
	}
	caps, found := s.thinkingCapabilities()
	return documentedDefaultReasoningLevel(s.modelKey(), found, caps.Supported)
}

func (s *InteractionsService) maxOutputTokens() int {
	return maxOutputTokensFor(s.URL, s.modelName(), s.MaxTokens, DefaultInteractionsMaxTokens)
}

func (s *InteractionsService) SupportsImages() bool   { return s.SupportsImages_ }
func (s *InteractionsService) MaxImageDimension() int { return 0 }
func (s *InteractionsService) MaxImageBytes() int     { return 20 * 1024 * 1024 }

func (s *InteractionsService) thinkingConfig(req *llm.Request) (thinkingLevel, thinkingSummaries string, err error) {
	caps, found := s.thinkingCapabilities()
	level, verbatim, active, err := resolveThinking(s.modelName(), caps, found, req, s.ReasoningEffort, s.ThinkingLevel)
	if err != nil {
		return "", "", err
	}
	if !active {
		if found && caps.Supported {
			return "", "auto", nil
		}
		return "", "", nil
	}
	if verbatim != "" {
		if verbatim != "off" && verbatim != "none" {
			return verbatim, "auto", nil
		}
		return verbatim, "none", nil
	}
	if level == llm.ThinkingLevelOff {
		return level.Name(), "none", nil
	}
	return level.Name(), "auto", nil
}

func (s *InteractionsService) buildInteractionsRequest(req *llm.Request) (*interactions.Request, error) {
	var err error
	req, err = prepareCitations(context.Background(), req)
	if err != nil {
		return nil, err
	}
	if s.MaxTokens < 0 {
		return nil, fmt.Errorf("max output tokens must not be negative: %d", s.MaxTokens)
	}

	store := false
	interReq := &interactions.Request{
		Model:  s.modelName(),
		Store:  &store,
		Stream: true,
	}

	if len(req.System) > 0 {
		var parts []string
		for _, sys := range req.System {
			if sys.Text != "" {
				parts = append(parts, sys.Text)
			}
		}
		interReq.SystemInstruction = strings.Join(parts, "\n")
	}

	toolNamesByID := make(map[string]string)

	for _, msg := range req.Messages {
		for _, c := range msg.Content {
			if c.Type == llm.ContentTypeToolUse && c.ID != "" {
				toolNamesByID[c.ID] = c.ToolName
			}
		}

		switch msg.Role {
		case llm.MessageRoleUser:
			var userContent []interactions.Content
			flushUser := func() {
				if len(userContent) > 0 {
					interReq.Input = append(interReq.Input, interactions.Step{
						Type:    "user_input",
						Content: userContent,
					})
					userContent = nil
				}
			}
			for _, c := range msg.Content {
				switch c.Type {
				case llm.ContentTypeToolResult:
					flushUser()
					funcName := toolNamesByID[c.ToolUseID]
					if funcName == "" {
						funcName = c.ToolName
					}
					if funcName == "" {
						return nil, fmt.Errorf("missing tool name for result %q", c.ToolUseID)
					}
					items := []interactions.Content{}
					for _, r := range c.ToolResult {
						if r.MediaType != "" && r.Data != "" {
							items = append(items, interactions.Content{
								Type:     "image",
								MimeType: r.MediaType,
								Data:     r.Data,
							})
						} else if r.Text != "" {
							items = append(items, interactions.Content{
								Type: "text",
								Text: r.Text,
							})
						}
					}
					payload, err := json.Marshal(items)
					if err != nil {
						return nil, fmt.Errorf("marshaling function result: %w", err)
					}
					interReq.Input = append(interReq.Input, interactions.Step{
						Type:    "function_result",
						CallID:  c.ToolUseID,
						Name:    funcName,
						Result:  payload,
						IsError: c.ToolError,
					})
				case llm.ContentTypeText:
					if c.MediaType != "" && c.Data != "" {
						userContent = append(userContent, interactions.Content{
							Type:     "image",
							MimeType: c.MediaType,
							Data:     c.Data,
						})
					} else if c.Text != "" {
						userContent = append(userContent, interactions.Content{
							Type: "text",
							Text: c.Text,
						})
					}
				}
			}
			flushUser()

		case llm.MessageRoleAssistant:
			native := msg.Origin.Known() && msg.Origin.Provider == "gemini" && msg.Origin.Transport == "gemini-interactions"
			if native && len(msg.GeminiInteractionsSteps) != 0 {
				var steps []interactions.Step
				if err := json.Unmarshal(msg.GeminiInteractionsSteps, &steps); err != nil {
					return nil, fmt.Errorf("decoding native Gemini Interactions history: %w", err)
				}
				// History pruning can remove one call from a multi-call message.
				for _, step := range steps {
					if step.Type == "function_call" {
						i := slices.IndexFunc(msg.Content, func(c llm.Content) bool {
							return c.Type == llm.ContentTypeToolUse && c.ID == step.ID
						})
						if i < 0 {
							continue
						}
						var err error
						step, err = step.WithArguments(llm.ReplayToolInput(msg.Content[i].ToolInput))
						if err != nil {
							return nil, fmt.Errorf("encoding Gemini Interactions call %q arguments: %w", msg.Content[i].ID, err)
						}
					}
					interReq.Input = append(interReq.Input, step)
				}
				continue
			}
			foreignOrigin := msg.Origin.Known() && (msg.Origin.Provider != "gemini" ||
				msg.Origin.Transport != "gemini-interactions")
			var modelContent []interactions.Content
			flushModel := func() {
				if len(modelContent) > 0 {
					interReq.Input = append(interReq.Input, interactions.Step{
						Type:    "model_output",
						Content: modelContent,
					})
					modelContent = nil
				}
			}
			for _, c := range msg.Content {
				if foreignOrigin {
					if c.Type == llm.ContentTypeThinking || c.Type == llm.ContentTypeRedactedThinking || len(c.GeminiServerTool) != 0 {
						continue
					}
					c.Signature = ""
				}
				if len(c.GeminiServerTool) != 0 {
					var step interactions.Step
					if err := json.Unmarshal(c.GeminiServerTool, &step); err != nil {
						return nil, fmt.Errorf("invalid Gemini Interactions server tool: %w", err)
					}
					if step.Type != "google_search_call" && step.Type != "google_search_result" {
						if !msg.Origin.Known() {
							// Pre-origin generateContent server-tool blob; skip on Interactions.
							continue
						}
						return nil, fmt.Errorf("unsupported Gemini Interactions server tool %q", step.Type)
					}
					flushModel()
					interReq.Input = append(interReq.Input, step)
					continue
				}
				if c.OpenAIResponsesReasoning != nil {
					continue
				}
				switch c.Type {
				case llm.ContentTypeThinking, llm.ContentTypeRedactedThinking:
					if !native || c.Signature == "" {
						continue
					}
					flushModel()
					interReq.Input = append(interReq.Input, interactions.Step{
						Type:      "thought",
						Signature: c.Signature,
					})
				case llm.ContentTypeText:
					if c.MediaType != "" && c.Data != "" {
						modelContent = append(modelContent, interactions.Content{
							Type:     "image",
							MimeType: c.MediaType,
							Data:     c.Data,
						})
					} else if c.Text != "" {
						modelContent = append(modelContent, interactions.Content{
							Type: "text",
							Text: c.Text,
						})
					}
				case llm.ContentTypeToolUse:
					flushModel()
					rawInput := llm.ReplayToolInput(c.ToolInput)
					var args map[string]any
					if err := json.Unmarshal(rawInput, &args); err != nil {
						return nil, fmt.Errorf("failed to unmarshal tool input: %w", err)
					}
					if args == nil {
						rawInput = json.RawMessage("{}")
					}
					if c.ID == "" {
						c.ID = fmt.Sprintf("gemini_tool_%s_%d", c.ToolName, time.Now().UnixNano())
					}
					toolNamesByID[c.ID] = c.ToolName
					interReq.Input = append(interReq.Input, interactions.Step{
						Type:      "function_call",
						ID:        c.ID,
						Name:      c.ToolName,
						Arguments: rawInput,
					})
				}
			}
			flushModel()

		default:
			return nil, fmt.Errorf("unsupported message role: %v", msg.Role)
		}
	}

	if len(req.Tools) > 0 {
		searchAdded := false
		for _, tool := range req.Tools {
			if !tool.ServerSide {
				var schemaJSON map[string]json.RawMessage
				if err := json.Unmarshal(tool.InputSchema, &schemaJSON); err != nil {
					return nil, fmt.Errorf("failed to unmarshal tool %s schema: %w", tool.Name, err)
				}
				if schemaJSON == nil {
					return nil, fmt.Errorf("tool %s schema must be a JSON object", tool.Name)
				}
				interReq.Tools = append(interReq.Tools, interactions.Tool{
					Type:        "function",
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.InputSchema,
				})
				continue
			}
			if tool.Type != "google_search" || !s.SupportsServerSideWebSearch() {
				return nil, fmt.Errorf("unsupported Gemini server tool %q for model %q", tool.Type, s.modelName())
			}
			if !searchAdded {
				interReq.Tools = append(interReq.Tools, interactions.Tool{Type: "google_search"})
				searchAdded = true
			}
		}
	}

	thinkingLevel, thinkingSummaries, err := s.thinkingConfig(req)
	if err != nil {
		return nil, err
	}
	interReq.GenerationConfig = &interactions.GenerationConfig{
		MaxOutputTokens:   s.maxOutputTokens(),
		ThinkingLevel:     thinkingLevel,
		ThinkingSummaries: thinkingSummaries,
	}

	return interReq, nil
}

func calculateInteractionsUsage(res *interactions.Response) (llm.Usage, error) {
	if res == nil || res.Usage == nil {
		return llm.Usage{}, errors.New("gemini interactions: missing usage metadata")
	}
	u := res.Usage
	if min(u.TotalInputTokens, u.TotalCachedTokens, u.TotalOutputTokens, u.TotalThoughtTokens, u.TotalToolUseTokens) < 0 {
		return llm.Usage{}, errors.New("gemini interactions: negative token count in usage metadata")
	}
	if u.TotalCachedTokens > u.TotalInputTokens {
		return llm.Usage{}, errors.New("gemini interactions: cached token count exceeds input token count")
	}
	return llm.Usage{
		InputTokens:          uint64(u.TotalInputTokens-u.TotalCachedTokens) + uint64(u.TotalToolUseTokens),
		CacheReadInputTokens: uint64(u.TotalCachedTokens),
		OutputTokens:         uint64(u.TotalOutputTokens) + uint64(u.TotalThoughtTokens),
	}, nil
}

func convertInteractionsResponse(res *interactions.Response) (*llm.Response, error) {
	usage, err := calculateInteractionsUsage(res)
	if err != nil {
		return nil, err
	}
	response := &llm.Response{
		ID:         res.ID,
		Role:       llm.MessageRoleAssistant,
		StopReason: llm.StopReasonEndTurn,
		Usage:      usage,
	}

	switch res.Status {
	case "completed", "":
	case "requires_action":
		response.StopReason = llm.StopReasonToolUse
	case "incomplete":
		response.StopReason = llm.StopReasonMaxTokens
	case "failed", "cancelled":
		return nil, fmt.Errorf("gemini interactions: interaction ended with status %q", res.Status)
	default:
		return nil, fmt.Errorf("gemini interactions: unexpected status %q", res.Status)
	}

	if len(res.Outputs) > 0 {
		response.GeminiInteractionsSteps, err = json.Marshal(res.Outputs)
		if err != nil {
			return nil, fmt.Errorf("encoding native Gemini Interactions output: %w", err)
		}
	}

	var contents []llm.Content
	for _, step := range res.Outputs {
		switch step.Type {
		case "thought":
			var summary strings.Builder
			for _, c := range step.Summary {
				if c.Type == "text" {
					summary.WriteString(c.Text)
				}
			}
			if summary.Len() == 0 && step.Signature == "" {
				continue
			}
			contents = append(contents, llm.Content{
				Type:      llm.ContentTypeThinking,
				Thinking:  summary.String(),
				Signature: step.Signature,
			})

		case "model_output":
			for _, c := range step.Content {
				switch c.Type {
				case "text":
					if c.Text == "" {
						continue
					}
					item := llm.Content{
						Type: llm.ContentTypeText,
						Text: c.Text,
					}
					if len(c.Annotations) > 0 {
						refs, err := convertInteractionsAnnotations(c.Text, c.Annotations)
						if err != nil {
							return nil, err
						}
						if len(refs) > 0 {
							raw, err := json.Marshal(refs)
							if err != nil {
								return nil, fmt.Errorf("marshal Gemini Interactions citations: %w", err)
							}
							item.Citations = raw
						}
					}
					contents = append(contents, item)
				case "image":
					if c.Data != "" && c.MimeType != "" {
						contents = append(contents, llm.Content{
							Type:      llm.ContentTypeText,
							MediaType: c.MimeType,
							Data:      c.Data,
						})
					}
				}
			}

		case "function_call":
			if step.ID == "" {
				return nil, fmt.Errorf("gemini interactions: missing call ID for tool %q", step.Name)
			}
			args := step.Arguments
			if len(args) == 0 {
				args = json.RawMessage("{}")
			} else if !json.Valid(args) {
				return nil, fmt.Errorf("gemini interactions: invalid JSON arguments for tool %q", step.Name)
			}
			contents = append(contents, llm.Content{
				ID:        step.ID,
				Type:      llm.ContentTypeToolUse,
				ToolName:  step.Name,
				ToolInput: args,
			})

		case "google_search_call":
			opaque, err := json.Marshal(step)
			if err != nil {
				return nil, fmt.Errorf("marshal Gemini Interactions search call: %w", err)
			}
			contents = append(contents, llm.Content{
				Type:             llm.ContentTypeServerToolUse,
				ID:               step.ID,
				ToolName:         "web_search",
				ToolInput:        step.Arguments,
				GeminiServerTool: opaque,
			})

		case "google_search_result":
			opaque, err := json.Marshal(step)
			if err != nil {
				return nil, fmt.Errorf("marshal Gemini Interactions search result: %w", err)
			}
			c := llm.Content{
				Type:             llm.ContentTypeWebSearchToolResult,
				ToolUseID:        step.CallID,
				ToolName:         "web_search",
				GeminiServerTool: opaque,
			}
			if len(step.Result) > 0 {
				var items []interactions.GoogleSearchResultItem
				if err := json.Unmarshal(step.Result, &items); err == nil {
					for _, item := range items {
						if item.SearchSuggestions != "" {
							c.ToolResult = append(c.ToolResult, llm.Content{
								Type:                    llm.ContentTypeText,
								GoogleSearchSuggestions: item.SearchSuggestions,
							})
						}
					}
				}
			}
			contents = append(contents, c)
		}
	}

	if len(contents) == 0 {
		contents = append(contents, llm.Content{Type: llm.ContentTypeText, Text: ""})
	}
	response.Content = contents

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

func convertInteractionsAnnotations(text string, annotations []interactions.Annotation) ([]groundingCitation, error) {
	var refs []groundingCitation
	for _, ann := range annotations {
		if ann.Type != "url_citation" || ann.URL == "" {
			continue
		}
		if ann.StartIndex < 0 || ann.EndIndex <= ann.StartIndex || ann.EndIndex > len(text) || !utf8.ValidString(text[ann.StartIndex:ann.EndIndex]) {
			return nil, fmt.Errorf("invalid Gemini Interactions annotation byte range [%d,%d)", ann.StartIndex, ann.EndIndex)
		}
		endUTF16 := len(utf16.Encode([]rune(text[:ann.EndIndex])))
		refs = append(refs, groundingCitation{
			EndUTF16:  endUTF16,
			Type:      "google_grounding",
			URL:       ann.URL,
			Title:     ann.Title,
			CitedText: text[ann.StartIndex:ann.EndIndex],
		})
	}
	return refs, nil
}

// Do sends a stateless streaming request to the Gemini Interactions API.
func (s *InteractionsService) Do(ctx context.Context, ir *llm.Request) (*llm.Response, error) {
	interReq, err := s.buildInteractionsRequest(ir)
	if err != nil {
		return nil, fmt.Errorf("failed to build Gemini Interactions request: %w", err)
	}

	if reqJSON, err := json.MarshalIndent(interReq, "", "  "); err == nil {
		slog.DebugContext(ctx, "gemini_interactions_request_json", "request", string(reqJSON))
	}

	client := interactions.Client{
		Endpoint: s.URL,
		APIKey:   s.APIKey,
		HTTPC:    cmp.Or(s.HTTPC, http.DefaultClient),
		OnStream: ir.OnStream,
	}

	startTime := time.Now()
	retryStart := startTime
	endTime := startTime
	var interRes *interactions.Response

	const maxAttempts = 16
	for attempts := 0; attempts < maxAttempts; attempts++ {
		var apiCallErr error
		interRes, apiCallErr = client.Create(ctx, interReq)
		endTime = time.Now()

		if apiCallErr == nil {
			if resJSON, err := json.MarshalIndent(interRes, "", "  "); err == nil {
				slog.DebugContext(ctx, "gemini_interactions_response_json", "response", string(resJSON))
			}
			break
		}

		if attempts == maxAttempts-1 {
			return nil, fmt.Errorf("gemini interactions: API error after %d attempts (last at %s): %w", attempts+1, time.Now().Format(time.DateTime), apiCallErr)
		}

		var apiErr *interactions.APIError
		retryable := errors.As(apiCallErr, &apiErr) && (apiErr.StatusCode == 429 || apiErr.StatusCode >= 500)
		if !retryable {
			return nil, fmt.Errorf("gemini interactions: API error: %w", apiCallErr)
		}
		sleep := llm.RetryDelay(llm.DefaultBackoff, attempts+1, llm.ParseRetryAfter(apiErr.Header.Get("Retry-After")))
		slog.WarnContext(ctx, "gemini_interactions_request_retry", "error", apiCallErr.Error(), "status_code", apiErr.StatusCode, "attempt", attempts+1, "sleep", sleep, "elapsed", time.Since(retryStart).Round(time.Second))
		if llm.WaitRetry(ctx, ir, llm.RetryEvent{Attempt: attempts + 1, Sleep: sleep, Err: llm.Truncate(apiCallErr.Error(), 160), Status: apiErr.StatusCode, Provider: "gemini", Model: cmp.Or(s.Model, DefaultModel)}) != nil {
			return nil, fmt.Errorf("gemini interactions: context cancelled during backoff after %d attempts: %w", attempts+1, apiCallErr)
		}
	}

	response, err := convertInteractionsResponse(interRes)
	if err != nil {
		return nil, err
	}
	response.Model = s.Model
	response.Usage.CostUSD = llm.CostUSDFromResponse(interRes.Header())
	response.StartTime = &startTime
	response.EndTime = &endTime
	response.URL = cmp.Or(s.URL, interactions.DefaultEndpoint)
	response.Origin = &llm.MessageOrigin{
		Provider: "gemini", Transport: "gemini-interactions", Model: s.modelName(),
	}
	return response, nil
}
