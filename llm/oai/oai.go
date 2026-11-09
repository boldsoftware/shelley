package oai

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"golang.org/x/net/http/httpguts"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
	"shelley.exe.dev/models/modelsdev"
)

const (
	DefaultMaxTokens = 32768 // output cap when neither config nor the models.dev catalog supplies one

	OpenAIURL    = "https://api.openai.com/v1"
	FireworksURL = "https://api.fireworks.ai/inference/v1"
	MoonshotURL  = "https://api.moonshot.ai/v1"
	XAIURL       = "https://api.x.ai/v1"

	// Environment variable names for API keys.
	//
	// DEPRECATED: Env-var-based model credentials are frozen. Do NOT add new
	// providers or models here. New models should be served through the
	// exe.dev LLM gateway or an exe.dev LLM integration (or added as DB-backed
	// custom models).
	OpenAIAPIKeyEnv    = "OPENAI_API_KEY"
	FireworksAPIKeyEnv = "FIREWORKS_API_KEY"
)

//exe:completeinit
type Model struct {
	ModelName          string // provided to the service provide to specify which model to use (e.g. "gpt-4.1-2025-04-14")
	TextVerbosity      string // Responses API default text verbosity; empty omits the field
	URL                string
	APIKeyEnv          string // environment variable name for the API key
	IsReasoningModel   bool   // whether this model is a reasoning model (e.g. O3, O4-mini)
	SupportsApplyPatch bool   // whether this model is trained for Codex apply_patch custom tools
	SupportsImages     bool   // whether this model accepts image inputs
}

var (
	DefaultModel = GPT54

	GPT41 = Model{
		ModelName:        "gpt-4.1-2025-04-14",
		TextVerbosity:    "",
		URL:              OpenAIURL,
		APIKeyEnv:        OpenAIAPIKeyEnv,
		IsReasoningModel: false,
		SupportsImages:   true,
	}

	GPT4o = Model{
		ModelName:        "gpt-4o-2024-08-06",
		TextVerbosity:    "",
		URL:              OpenAIURL,
		APIKeyEnv:        OpenAIAPIKeyEnv,
		IsReasoningModel: false,
		SupportsImages:   true,
	}

	DeepseekV41FlashFireworks = Model{
		ModelName:        "accounts/fireworks/models/deepseek-v4p1-flash",
		TextVerbosity:    "",
		URL:              FireworksURL,
		APIKeyEnv:        FireworksAPIKeyEnv,
		IsReasoningModel: true,
		SupportsImages:   true,
	}

	DeepseekV41Flash1017Fireworks = Model{
		ModelName:        "accounts/fireworks/models/deepseek-v4p1-flash-1017",
		TextVerbosity:    "",
		URL:              FireworksURL,
		APIKeyEnv:        FireworksAPIKeyEnv,
		IsReasoningModel: true,
		SupportsImages:   true,
	}

	GLM53Fireworks = Model{
		ModelName:        "accounts/fireworks/models/glm-5p3",
		TextVerbosity:    "",
		URL:              FireworksURL,
		APIKeyEnv:        FireworksAPIKeyEnv,
		IsReasoningModel: true,
		SupportsImages:   false,
	}

	GLM53FlashFireworks = Model{
		ModelName:        "accounts/fireworks/models/glm-5p3-flash",
		TextVerbosity:    "",
		URL:              FireworksURL,
		APIKeyEnv:        FireworksAPIKeyEnv,
		IsReasoningModel: true,
		SupportsImages:   true,
	}

	KimiK3Fireworks = Model{
		ModelName:        "accounts/fireworks/models/kimi-k3",
		TextVerbosity:    "",
		URL:              FireworksURL,
		APIKeyEnv:        FireworksAPIKeyEnv,
		IsReasoningModel: true,
		SupportsImages:   true,
	}

	Grok45 = Model{
		ModelName:        "grok-4.5",
		TextVerbosity:    "",
		URL:              XAIURL,
		APIKeyEnv:        "", // gateway-only; no direct XAI_API_KEY env support
		IsReasoningModel: true,
		SupportsImages:   true,
	}

	GPT6Astra = Model{
		ModelName:          "gpt-6-astra",
		TextVerbosity:      "low",
		URL:                OpenAIURL,
		APIKeyEnv:          OpenAIAPIKeyEnv,
		IsReasoningModel:   true,
		SupportsApplyPatch: true,
		SupportsImages:     true,
	}

	GPT61Sol = Model{
		ModelName:          "gpt-6.1-sol",
		TextVerbosity:      "low",
		URL:                OpenAIURL,
		APIKeyEnv:          OpenAIAPIKeyEnv,
		IsReasoningModel:   true,
		SupportsApplyPatch: true,
		SupportsImages:     true,
	}

	GPT61Terra = Model{
		ModelName:          "gpt-6.1-terra",
		TextVerbosity:      "low",
		URL:                OpenAIURL,
		APIKeyEnv:          OpenAIAPIKeyEnv,
		IsReasoningModel:   true,
		SupportsApplyPatch: true,
		SupportsImages:     true,
	}

	GPT6Sol = Model{
		ModelName:          "gpt-6-sol",
		TextVerbosity:      "low",
		URL:                OpenAIURL,
		APIKeyEnv:          OpenAIAPIKeyEnv,
		IsReasoningModel:   true,
		SupportsApplyPatch: true,
		SupportsImages:     true,
	}

	GPT6Luna = Model{
		ModelName:          "gpt-6-luna",
		TextVerbosity:      "low",
		URL:                OpenAIURL,
		APIKeyEnv:          OpenAIAPIKeyEnv,
		IsReasoningModel:   true,
		SupportsApplyPatch: true,
		SupportsImages:     true,
	}

	GPT56Sol = Model{
		ModelName:          "gpt-5.6-sol",
		TextVerbosity:      "low",
		URL:                OpenAIURL,
		APIKeyEnv:          OpenAIAPIKeyEnv,
		IsReasoningModel:   true,
		SupportsApplyPatch: true,
		SupportsImages:     true,
	}

	GPT56Terra = Model{
		ModelName:          "gpt-5.6-terra",
		TextVerbosity:      "low",
		URL:                OpenAIURL,
		APIKeyEnv:          OpenAIAPIKeyEnv,
		IsReasoningModel:   true,
		SupportsApplyPatch: true,
		SupportsImages:     true,
	}

	GPT56Luna = Model{
		ModelName:          "gpt-5.6-luna",
		TextVerbosity:      "low",
		URL:                OpenAIURL,
		APIKeyEnv:          OpenAIAPIKeyEnv,
		IsReasoningModel:   true,
		SupportsApplyPatch: true,
		SupportsImages:     true,
	}

	GPT55 = Model{
		ModelName:          "gpt-5.5",
		TextVerbosity:      "low",
		URL:                OpenAIURL,
		APIKeyEnv:          OpenAIAPIKeyEnv,
		IsReasoningModel:   false,
		SupportsApplyPatch: true,
		SupportsImages:     true,
	}

	GPT54 = Model{
		ModelName:          "gpt-5.4",
		TextVerbosity:      "low",
		URL:                OpenAIURL,
		APIKeyEnv:          OpenAIAPIKeyEnv,
		IsReasoningModel:   false,
		SupportsApplyPatch: true,
		SupportsImages:     true,
	}

	GPT54Mini = Model{
		ModelName:          "gpt-5.4-mini",
		TextVerbosity:      "medium",
		URL:                OpenAIURL,
		APIKeyEnv:          OpenAIAPIKeyEnv,
		IsReasoningModel:   false,
		SupportsApplyPatch: true,
		SupportsImages:     true,
	}

	GPT54Nano = Model{
		ModelName:          "gpt-5.4-nano",
		TextVerbosity:      "medium",
		URL:                OpenAIURL,
		APIKeyEnv:          OpenAIAPIKeyEnv,
		IsReasoningModel:   false,
		SupportsApplyPatch: true,
		SupportsImages:     true,
	}

	GPT53Codex = Model{
		ModelName:          "gpt-5.3-codex",
		TextVerbosity:      "low",
		URL:                OpenAIURL,
		APIKeyEnv:          OpenAIAPIKeyEnv,
		IsReasoningModel:   false,
		SupportsApplyPatch: true,
		SupportsImages:     true,
	}
)

// Service provides chat completions.
// Fields should not be altered concurrently with calling any method on Service.
type Service struct {
	// ReasoningOverride is advertised for this model at this endpoint; nil uses models.dev.
	ReasoningOverride *modelsdev.ReasoningCapabilities

	HTTPC        *http.Client    // defaults to http.DefaultClient if nil
	APIKey       string          // optional, if not set will try to load from env var
	Model        Model           // defaults to DefaultModel if zero value
	ModelURL     string          // optional, overrides Model.URL
	MaxTokens    int             // defaults to DefaultMaxTokens if zero
	ProviderName string          // e.g., "openai", "fireworks"
	Org          string          // optional - organization ID
	Backoff      []time.Duration // retry backoff durations; llm.DefaultBackoff if empty

	// ThinkingLevel is the service-level default reasoning level. Most providers
	// behind the chat-completions API (Fireworks GLM, GPT-OSS, etc.) accept
	// `reasoning_effort: "low"|"medium"|"high"`; this is sent only when the
	// effective level is non-zero. Per-request overrides via Request.ThinkingLevel
	// take precedence.
	ThinkingLevel llm.ThinkingLevel
	// ReasoningEffort, when non-empty, is sent as the literal `reasoning_effort`
	// value (used by custom-model config to pass provider-specific values like
	// "xhigh" or "none"). Overridden by Request.ThinkingLevel when set.
	ReasoningEffort string

	// DisableReasoning suppresses reasoning controls regardless of request,
	// service, or model metadata. It does not alter ReasoningReplay.
	DisableReasoning bool

	// ReasoningReplay controls persisted reasoning replay. The zero value and
	// "auto" resolve from models.dev; "none" disables replay explicitly.
	ReasoningReplay ReasoningReplay
}

// GPTCharsPerToken is the characters per token of GPT's tokenizer, measured
// on a mix of code, prose, JSON and command output.
const GPTCharsPerToken = 3.61

var _ llm.Service = (*Service)(nil)

func (s *Service) messageOrigin(model Model) llm.MessageOrigin {
	return llm.MessageOrigin{
		Provider:  cmp.Or(s.ProviderName, "openai"),
		Transport: "openai-chat:" + llm.TransportEndpoint(cmp.Or(s.ModelURL, model.URL, OpenAIURL)),
		Model:     model.ModelName,
	}
}

var (
	fromLLMRole = map[llm.MessageRole]string{
		llm.MessageRoleAssistant: "assistant",
		llm.MessageRoleUser:      "user",
	}
	fromLLMToolChoiceType = map[llm.ToolChoiceType]string{
		llm.ToolChoiceTypeAuto: "auto",
		llm.ToolChoiceTypeAny:  "any",
		llm.ToolChoiceTypeNone: "none",
		llm.ToolChoiceTypeTool: "function", // OpenAI uses "function" instead of "tool"
	}
	toLLMRole = map[string]llm.MessageRole{
		"assistant": llm.MessageRoleAssistant,
		"user":      llm.MessageRoleUser,
	}
	toLLMStopReason = map[string]llm.StopReason{
		"stop":           llm.StopReasonStopSequence,
		"length":         llm.StopReasonMaxTokens,
		"tool_calls":     llm.StopReasonToolUse,
		"function_call":  llm.StopReasonToolUse,      // Map both to ToolUse
		"content_filter": llm.StopReasonStopSequence, // No direct equivalent
	}
)

func isImageContent(c llm.Content) bool {
	return c.MediaType != "" && c.Data != ""
}

func openAIImageDataURL(c llm.Content) string {
	return "data:" + c.MediaType + ";base64," + c.Data
}

func openAIImagePart(c llm.Content) openai.ChatMessagePart {
	return openai.ChatMessagePart{
		Type: openai.ChatMessagePartTypeImageURL,
		ImageURL: &openai.ChatMessageImageURL{
			URL: openAIImageDataURL(c),
		},
	}
}

func openAITextPart(text string) openai.ChatMessagePart {
	return openai.ChatMessagePart{
		Type: openai.ChatMessagePartTypeText,
		Text: text,
	}
}

// fromLLMContent converts llm.Content to the format expected by OpenAI.
func fromLLMContent(c llm.Content) (string, []openai.ToolCall) {
	switch c.Type {
	case llm.ContentTypeText:
		if isImageContent(c) {
			return "", nil
		}
		return c.Text, nil
	case llm.ContentTypeToolUse:
		// For OpenAI, tool use is sent as a null content with tool_calls in the message
		return "", []openai.ToolCall{
			{
				Type: openai.ToolTypeFunction,
				ID:   c.ID, // Use the content ID if provided
				Function: openai.FunctionCall{
					Name:      c.ToolName,
					Arguments: string(llm.ReplayToolInput(c.ToolInput)),
				},
			},
		}
	case llm.ContentTypeToolResult:
		// Tool results in OpenAI are sent as a separate message with tool_call_id
		// OpenAI doesn't support multiple content items or images in tool results
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
		return resultText, nil
	case llm.ContentTypeThinking, llm.ContentTypeRedactedThinking:
		// Thinking blocks are not text content; they're hoisted onto the
		// outgoing message's reasoning_content field by fromLLMMessage. Skip.
		return "", nil
	default:
		return c.Text, nil
	}
}

// endpointHostMatches reports whether baseURL's hostname is one of domains or
// one of their subdomains. Paths and query strings are intentionally ignored.
func endpointHostMatches(baseURL string, domains ...string) bool {
	if baseURL == "" {
		return false
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, domain := range domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// usesLegacyMaxTokensField identifies OpenAI-compatible endpoints that accept
// max_tokens but not max_completion_tokens. This mirrors Pi's URL-based
// compatibility detection without adding another custom-model setting.
func usesLegacyMaxTokensField(baseURL string) bool {
	return endpointHostMatches(
		baseURL,
		"chutes.ai",
		"deepseek.com",
		"moonshot.ai",
		"moonshot.cn",
		"gateway.ai.cloudflare.com",
		"api.together.ai",
		"api.together.xyz",
		"integrate.api.nvidia.com",
		"api.ant-ling.com",
		"api.z.ai",
		"open.bigmodel.cn",
	)
}

// maxOutputTokens returns the output limit to send. MaxTokens may contain the
// custom-model column's old 200000 context-window default, so a configured
// value can only lower a known models.dev limit, never raise it. Unknown models
// retain the configured value or the historical service default.
func maxOutputTokens(endpoint, modelName string, configured int) int {
	limit, found := modelsdev.LookupOutputLimit(endpoint, modelName)
	if !found {
		return cmp.Or(configured, DefaultMaxTokens)
	}
	return min(cmp.Or(configured, limit), limit)
}

// fromLLMMessage converts llm.Message to OpenAI ChatCompletionMessage format
func fromLLMMessage(msg llm.Message) []openai.ChatCompletionMessage {
	// For OpenAI, we need to handle tool results differently than regular messages
	// Each tool result becomes its own message with role="tool"

	var messages []openai.ChatCompletionMessage

	// Check if this is a regular message or contains tool results
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

	// Process tool results as separate messages, but first
	for _, tr := range toolResults {
		// Tool-role messages cannot carry image parts. Preserve images as a following user
		// message so vision-capable OpenAI models actually receive them.
		var texts []string
		var imageParts []openai.ChatMessagePart
		for _, result := range tr.ToolResult {
			if strings.TrimSpace(result.Text) != "" {
				texts = append(texts, result.Text)
			}
			if isImageContent(result) {
				imageParts = append(imageParts, openAIImagePart(result))
			}
		}
		toolResultContent := strings.Join(texts, "\n")

		// OpenAI doesn't have an explicit error field for tool results, so add it directly to the content.
		if tr.ToolError {
			if toolResultContent != "" {
				toolResultContent = "error: " + toolResultContent
			} else {
				toolResultContent = "error: tool execution failed"
			}
		}

		m := openai.ChatCompletionMessage{
			Role:       "tool",
			Content:    cmp.Or(toolResultContent, " "), // Use empty space if empty to avoid omitempty issues
			ToolCallID: tr.ToolUseID,
		}
		messages = append(messages, m)

		if len(imageParts) > 0 {
			parts := []openai.ChatMessagePart{openAITextPart("Images returned by tool " + tr.ToolUseID + ":")}
			parts = append(parts, imageParts...)
			messages = append(messages, openai.ChatCompletionMessage{
				Role:         "user",
				MultiContent: parts,
			})
		}
	}
	// Process regular content second
	if len(regularContent) > 0 {
		m := openai.ChatCompletionMessage{
			Role: fromLLMRole[msg.Role],
		}

		// For assistant messages that contain tool calls
		var toolCalls []openai.ToolCall
		var textContent string
		var multiContent []openai.ChatMessagePart
		var thinking []string
		hasImage := false

		for _, c := range regularContent {
			if isImageContent(c) {
				multiContent = append(multiContent, openAIImagePart(c))
				hasImage = true
				continue
			}
			if c.Type == llm.ContentTypeThinking && c.Thinking != "" {
				thinking = append(thinking, c.Thinking)
				continue
			}
			content, tools := fromLLMContent(c)
			if len(tools) > 0 {
				toolCalls = append(toolCalls, tools...)
			} else if content != "" {
				if textContent != "" {
					textContent += "\n"
				}
				textContent += content
				multiContent = append(multiContent, openAITextPart(content))
			}
		}

		if hasImage {
			// When using multiContent, ensure we have content to avoid bare messages.
			// If multiContent is somehow empty and we have no tool calls, fall back to Content.
			if len(multiContent) == 0 && len(toolCalls) == 0 {
				m.Content = " "
			} else {
				m.MultiContent = multiContent
			}
		} else {
			// Use empty space if empty to avoid omitempty issues (bare {"role": "assistant"} messages)
			// but only when there are no tool calls (tool calls field will prevent bare message).
			// See Issue #223: llama.cpp rejects bare assistant messages.
			if len(toolCalls) == 0 {
				m.Content = cmp.Or(textContent, " ")
			} else {
				m.Content = textContent
			}
		}
		m.ToolCalls = toolCalls
		if len(thinking) > 0 {
			m.ReasoningContent = strings.Join(thinking, "\n")
		}

		messages = append(messages, m)
	}

	return messages
}

// fromLLMToolChoice converts llm.ToolChoice to the format expected by OpenAI.
func fromLLMToolChoice(tc *llm.ToolChoice) any {
	if tc == nil {
		return nil
	}

	if tc.Type == llm.ToolChoiceTypeTool && tc.Name != "" {
		return openai.ToolChoice{
			Type: openai.ToolTypeFunction,
			Function: openai.ToolFunction{
				Name: tc.Name,
			},
		}
	}

	// For non-specific tool choice, just use the string
	return fromLLMToolChoiceType[tc.Type]
}

// fromLLMTool converts llm.Tool to the format expected by OpenAI.
func fromLLMTool(t *llm.Tool) openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		},
	}
}

// fromLLMSystem converts llm.SystemContent to an OpenAI system message.
func fromLLMSystem(systemContent []llm.SystemContent) []openai.ChatCompletionMessage {
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

	return []openai.ChatCompletionMessage{
		{
			Role:    "system",
			Content: systemText,
		},
	}
}

// toRawLLMContent converts a raw content string from OpenAI to llm.Content.
func toRawLLMContent(content string) llm.Content {
	return llm.Content{
		Type: llm.ContentTypeText,
		Text: content,
	}
}

// toToolCallLLMContent converts a tool call from OpenAI to llm.Content.
func toToolCallLLMContent(toolCall openai.ToolCall) llm.Content {
	// Generate a content ID if needed
	id := toolCall.ID
	if id == "" {
		// Create a deterministic ID based on the function name if no ID is provided
		id = "tc_" + toolCall.Function.Name
	}

	return llm.Content{
		ID:        id,
		Type:      llm.ContentTypeToolUse,
		ToolName:  toolCall.Function.Name,
		ToolInput: toolArgumentsInput(toolCall.Function.Arguments),
	}
}

// toolArgumentsInput converts a provider's tool arguments string into
// ToolInput. Providers may return empty or malformed strings; both must stay
// serializable so the assistant message can be persisted.
func toolArgumentsInput(arguments string) json.RawMessage {
	if arguments == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(arguments)) {
		return json.RawMessage(arguments)
	}
	encoded, _ := json.Marshal(arguments)
	return encoded
}

// toToolResultLLMContent converts a tool result message from OpenAI to llm.Content.
func toToolResultLLMContent(msg openai.ChatCompletionMessage) llm.Content {
	return llm.Content{
		Type:      llm.ContentTypeToolResult,
		ToolUseID: msg.ToolCallID,
		ToolResult: []llm.Content{{
			Type: llm.ContentTypeText,
			Text: msg.Content,
		}},
		ToolError: false, // OpenAI doesn't specify errors explicitly; error information is parsed from content
	}
}

// toLLMContents converts message content from OpenAI to []llm.Content.
func toLLMContents(msg openai.ChatCompletionMessage) []llm.Content {
	var contents []llm.Content

	// If this is a tool response, handle it separately
	if msg.Role == "tool" && msg.ToolCallID != "" {
		return []llm.Content{toToolResultLLMContent(msg)}
	}

	// If the provider returned reasoning_content (DeepSeek thinking mode),
	// preserve it as a Thinking content block. We need to round-trip this
	// on subsequent turns when tool calls are involved — DeepSeek requires
	// reasoning_content to be present in assistant messages that have
	// tool_calls, and using the real content (rather than a placeholder)
	// lets the model continue its prior chain of thought across tool calls.
	// See https://api-docs.deepseek.com/guides/thinking_mode#tool-calls
	if msg.ReasoningContent != "" {
		contents = append(contents, llm.Content{
			Type:     llm.ContentTypeThinking,
			Thinking: msg.ReasoningContent,
		})
	}

	// If there's text content, add it
	if msg.Content != "" {
		contents = append(contents, toRawLLMContent(msg.Content))
	}

	// If there are tool calls, add them
	for _, tc := range msg.ToolCalls {
		contents = append(contents, toToolCallLLMContent(tc))
	}

	// If empty, add an empty text content
	if len(contents) == 0 {
		contents = append(contents, llm.Content{
			Type: llm.ContentTypeText,
			Text: "",
		})
	}

	return contents
}

// chatCompletionUsage is the Chat Completions usage object, decoded locally
// because go-openai's PromptTokensDetails lacks cache_write_tokens.
type chatCompletionUsage struct {
	PromptTokens        int                      `json:"prompt_tokens"`
	CompletionTokens    int                      `json:"completion_tokens"`
	PromptTokensDetails openAIInputTokensDetails `json:"prompt_tokens_details"`
}

// openAIInputTokensDetails is prompt_tokens_details (Chat Completions) and
// input_tokens_details (Responses): disjoint subsets of the total input.
type openAIInputTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
	// CacheWriteTokens is reported by GPT-5.6 and later, which bill cache
	// writes at 1.25x the uncached input rate. Earlier models report 0.
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// chatCompletionUsageFromOpenAI adapts go-openai's usage type, which the
// non-streaming path still receives. go-openai does not decode
// cache_write_tokens, so writes read 0 here; the agent loop always streams.
//
// TODO: make Service.Do always stream (discarding deltas when OnStream is
// nil) and delete this adapter along with the non-streaming branch, or
// upstream cache_write_tokens to go-openai's PromptTokensDetails.
func chatCompletionUsageFromOpenAI(au openai.Usage) chatCompletionUsage {
	u := chatCompletionUsage{PromptTokens: au.PromptTokens, CompletionTokens: au.CompletionTokens}
	if au.PromptTokensDetails != nil {
		u.PromptTokensDetails.CachedTokens = au.PromptTokensDetails.CachedTokens
	}
	return u
}

// toLLMUsage converts Chat Completions usage to llm.Usage.
func (s *Service) toLLMUsage(au chatCompletionUsage, headers http.Header) llm.Usage {
	u := splitOpenAIInputUsage(au.PromptTokens, au.PromptTokensDetails)
	u.OutputTokens = uint64(au.CompletionTokens)
	u.CostUSD = llm.CostUSDFromResponse(headers)
	return u
}

// splitOpenAIInputUsage maps OpenAI's input accounting (a total with cached and
// cache-write subsets) onto Shelley's Anthropic-style Usage, where InputTokens
// is only the portion neither read from nor written to the cache, so
// TotalInputTokens() reproduces OpenAI's total. Cache writes must not be folded
// into InputTokens: GPT-5.6 and later bill them at 1.25x.
func splitOpenAIInputUsage(total int, d openAIInputTokensDetails) llm.Usage {
	return llm.Usage{
		InputTokens:              uint64(total - d.CachedTokens - d.CacheWriteTokens),
		CacheCreationInputTokens: uint64(d.CacheWriteTokens),
		CacheReadInputTokens:     uint64(d.CachedTokens),
	}
}

// toLLMResponse converts the OpenAI response to llm.Response.
func (s *Service) toLLMResponse(r *openai.ChatCompletionResponse) *llm.Response {
	// fmt.Printf("Raw response\n")
	// enc := json.NewEncoder(os.Stdout)
	// enc.SetIndent("", "  ")
	// enc.Encode(r)
	// fmt.Printf("\n")

	if len(r.Choices) == 0 {
		return &llm.Response{
			ID:    r.ID,
			Model: r.Model,
			Role:  llm.MessageRoleAssistant,
			Usage: s.toLLMUsage(chatCompletionUsageFromOpenAI(r.Usage), r.Header()),
		}
	}

	// Process the primary choice
	choice := r.Choices[0]

	return &llm.Response{
		ID:         r.ID,
		Model:      r.Model,
		Role:       toRoleFromString(choice.Message.Role),
		Content:    toLLMContents(choice.Message),
		StopReason: toStopReason(string(choice.FinishReason)),
		Usage:      s.toLLMUsage(chatCompletionUsageFromOpenAI(r.Usage), r.Header()),
	}
}

type chatCompletionStreamResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          string            `json:"content"`
			Role             string            `json:"role"`
			ToolCalls        []openai.ToolCall `json:"tool_calls"`
			ReasoningContent string            `json:"reasoning_content"`
			Reasoning        string            `json:"reasoning"`
		} `json:"delta"`
		FinishReason openai.FinishReason `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatCompletionUsage `json:"usage"`
}

// chatStreamInterruptedError marks an accepted chat completion stream that
// broke (HTTP/2 stream reset, dropped connection, idle stall) or ended before
// its finish reason: a transport fault rather than a verdict on the request.
type chatStreamInterruptedError struct{ err error }

func (e *chatStreamInterruptedError) Error() string { return e.err.Error() }
func (e *chatStreamInterruptedError) Unwrap() error { return e.err }

// isChatTransportError reports whether a chat completion attempt failed in
// transport: before any HTTP status arrived, or while reading the stream.
func isChatTransportError(err error) bool {
	if _, ok := errors.AsType[*chatStreamInterruptedError](err); ok {
		return true
	}
	// http.Client.Do reports transport failures as *url.Error. Do rejects
	// base URLs that would fail this way deterministically before sending.
	_, ok := errors.AsType[*url.Error](err)
	return ok
}

// chatInBandErrorRetryable classifies an error event sent inside an accepted
// stream. Such errors have no HTTP status, so use the same code and type
// rules as the Responses client; unclassified errors stay retryable.
func chatInBandErrorRetryable(apiErr *openai.APIError) bool {
	code, _ := json.Marshal(apiErr.Code)
	return responsesErrorRetryable(&responsesError{Message: apiErr.Message, Type: apiErr.Type, Code: code})
}

func (s *Service) consumeChatCompletionStream(ctx context.Context, stream *openai.ChatCompletionStream, onStream func(llm.StreamDelta)) (*llm.Response, error) {
	msg := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant}
	headers := stream.Header()
	var (
		id           string
		model        string
		finishReason openai.FinishReason
		usage        chatCompletionUsage
		started      bool
	)

	for {
		raw, err := stream.RecvRaw()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// In-band provider errors keep their *openai.APIError for status
			// classification; anything else came from reading the body.
			var apiErr *openai.APIError
			if !errors.As(err, &apiErr) && !errors.Is(err, openai.ErrTooManyEmptyStreamMessages) {
				err = &chatStreamInterruptedError{err: err}
			}
			if started {
				return nil, fmt.Errorf("chat completion stream failed after response started: %w", err)
			}
			return nil, err
		}
		var chunk chatCompletionStreamResponse
		if err := json.Unmarshal(raw, &chunk); err != nil {
			if started {
				return nil, fmt.Errorf("chat completion stream failed after response started: %v", err)
			}
			return nil, err
		}
		started = true
		llm.ReportProgress(ctx)
		if chunk.ID != "" {
			id = chunk.ID
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
		delta := choice.Delta
		if delta.Role != "" {
			msg.Role = delta.Role
		}
		reasoning := cmp.Or(delta.ReasoningContent, delta.Reasoning)
		if reasoning != "" {
			msg.ReasoningContent += reasoning
			if onStream != nil {
				onStream(llm.StreamDelta{Type: "thinking", Text: reasoning, Index: 0})
			}
		}
		if delta.Content != "" {
			msg.Content += delta.Content
			if onStream != nil {
				index := 0
				if msg.ReasoningContent != "" {
					index = 1
				}
				onStream(llm.StreamDelta{Type: "text", Text: delta.Content, Index: index})
			}
		}
		for _, fragment := range delta.ToolCalls {
			index := 0
			if fragment.Index != nil {
				index = *fragment.Index
			}
			for len(msg.ToolCalls) <= index {
				msg.ToolCalls = append(msg.ToolCalls, openai.ToolCall{})
			}
			toolCall := &msg.ToolCalls[index]
			if fragment.ID != "" {
				toolCall.ID = fragment.ID
			}
			if fragment.Type != "" {
				toolCall.Type = fragment.Type
			}
			toolCall.Function.Name += fragment.Function.Name
			toolCall.Function.Arguments += fragment.Function.Arguments
		}
	}

	if finishReason == "" {
		// A response with no data chunks that isn't an event stream (JSON
		// from a server ignoring stream:true, an HTML login page) won't
		// improve on retry.
		if contentType := headers.Get("Content-Type"); !started && !strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
			return nil, fmt.Errorf("chat completion response is not an event stream: Content-Type %q, no data chunks", contentType)
		}
		return nil, &chatStreamInterruptedError{err: errors.New("incomplete chat completion stream: no finish reason")}
	}
	return &llm.Response{
		ID:         id,
		Model:      model,
		Role:       toRoleFromString(msg.Role),
		Content:    toLLMContents(msg),
		StopReason: toStopReason(string(finishReason)),
		Usage:      s.toLLMUsage(usage, headers),
	}, nil
}

// toRoleFromString converts a role string to llm.MessageRole.
func toRoleFromString(role string) llm.MessageRole {
	if role == "tool" || role == "system" || role == "function" {
		return llm.MessageRoleAssistant // Map special roles to assistant for consistency
	}
	if mr, ok := toLLMRole[role]; ok {
		return mr
	}
	return llm.MessageRoleUser // Default to user if unknown
}

// toStopReason converts a finish reason string to llm.StopReason.
func toStopReason(reason string) llm.StopReason {
	if sr, ok := toLLMStopReason[reason]; ok {
		return sr
	}
	return llm.StopReasonStopSequence // Default
}

func (s *Service) Provider() string { return s.ProviderName }

func (s *Service) PatchProfile() string {
	return llm.ModelPatchProfile(s.Model.ModelName)
}

// DefaultReasoningLevel reports the reasoning effort applied to un-overridden
// requests, mirroring the precedence used when building a chat-completions
// request: verbatim per-model ReasoningEffort wins, else a configured
// service-level ThinkingLevel. When neither is set, no reasoning_effort is
// emitted and the provider applies its own default (often not "off" for
// reasoning models), which Shelley cannot name — so it returns "".
func (s *Service) DefaultReasoningLevel() string {
	return defaultReasoningLevel(s.DisableReasoning, s.ReasoningOverride, s.ThinkingLevel, s.ReasoningEffort)
}

// SupportsImages reports whether this model accepts image inputs. Defaults
// to true; set Model.SupportsImages on image-capable models.
func (s *Service) SupportsImages() bool { return s.Model.SupportsImages }

// MaxImageDimension returns the maximum allowed image dimension.
// TODO: determine actual OpenAI image dimension limits
func (s *Service) MaxImageDimension() int {
	return 0 // No known limit
}

// MaxImageBytes returns the maximum allowed encoded size for a single image.
func (s *Service) MaxImageBytes() int { return maxImageBytes }

// maxImageBytes is OpenAI's 20 MB per-image input cap
// (https://platform.openai.com/docs/guides/images-vision).
const maxImageBytes = 20 * 1024 * 1024

// defaultReasoningLevel implements DefaultReasoningLevel for both OpenAI
// services. Endpoint overrides report "none" by its canonical API/UI name,
// "off"; provider-verbatim efforts are otherwise returned untouched.
func defaultReasoningLevel(disabled bool, override *modelsdev.ReasoningCapabilities, level llm.ThinkingLevel, effort string) string {
	if disabled {
		return ""
	}
	if e, ok := overrideReasoningEffort(override, &llm.Request{}, level, effort); ok {
		if e == "none" {
			return "off"
		}
		return e
	}
	if effort != "" {
		return effort
	}
	if level != llm.ThinkingLevelDefault && level != llm.ThinkingLevelOff {
		return level.Name()
	}
	return ""
}

func modelReasoningCapabilities(override *modelsdev.ReasoningCapabilities, endpoint string, model Model) (modelsdev.ReasoningCapabilities, bool) {
	return override.Lookup(cmp.Or(endpoint, model.URL), model.ModelName)
}

func advertisedReasoningLevels(caps modelsdev.ReasoningCapabilities, found bool) []llm.ThinkingLevel {
	if !found {
		return nil
	}
	return append([]llm.ThinkingLevel(nil), caps.Levels...)
}

func effortForThinkingLevel(level llm.ThinkingLevel) string {
	if level == llm.ThinkingLevelOff {
		return "none"
	}
	return level.ThinkingEffort()
}

func clampKnownReasoningEffort(effort string, levels []llm.ThinkingLevel) string {
	level := llm.ParseThinkingLevel(effort)
	if effort == "none" {
		level = llm.ThinkingLevelOff
	}
	if level == llm.ThinkingLevelDefault {
		return effort
	}
	return effortForThinkingLevel(llm.ClampThinkingLevel(level, levels))
}

// SetReasoningOverride configures endpoint controls before the service is used.
func (s *Service) SetReasoningOverride(caps *modelsdev.ReasoningCapabilities) {
	s.ReasoningOverride = caps
}

// SupportsReasoning uses endpoint metadata before models.dev. Unknown models
// retain the historical default of supporting reasoning controls.
func (s *Service) SupportsReasoning() bool {
	if s.DisableReasoning {
		return false
	}
	caps, found := modelReasoningCapabilities(s.ReasoningOverride, s.ModelURL, cmp.Or(s.Model, DefaultModel))
	return !found || caps.Supported
}

// SupportedReasoningLevels advertises endpoint effort levels before models.dev.
// Nil means the model has no exact effort metadata and callers use the
// historical provider fallback.
func (s *Service) SupportedReasoningLevels() []llm.ThinkingLevel {
	if s.DisableReasoning {
		return nil
	}
	caps, found := modelReasoningCapabilities(s.ReasoningOverride, s.ModelURL, cmp.Or(s.Model, DefaultModel))
	return advertisedReasoningLevels(caps, found)
}

// Do sends a request to OpenAI using the go-openai package.
func (s *Service) Do(ctx context.Context, ir *llm.Request) (*llm.Response, error) {
	ctx = llmhttp.WithOpenAIProtocol(ctx)
	var err error
	ir, err = llm.PrepareRequestCitations(ctx, ir, "openai-chat", adaptCitation)
	if err != nil {
		return nil, err
	}
	// Configure the OpenAI client
	httpc := cmp.Or(s.HTTPC, http.DefaultClient)
	model := cmp.Or(s.Model, DefaultModel)

	// TODO: do this one during Service setup? maybe with a constructor instead?
	config := openai.DefaultConfig(s.APIKey)
	baseURL := cmp.Or(s.ModelURL, model.URL)
	if baseURL != "" {
		config.BaseURL = baseURL
	}
	if s.Org != "" {
		config.OrgID = s.Org
	}
	// Transport failures are retried for minutes, so reject configuration no
	// request can succeed with: http.Client.Do reports both as *url.Error.
	if u, err := url.Parse(config.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, newRequestError(fmt.Errorf("invalid base URL %q for model %s: want http(s)://host[/path]", config.BaseURL, model.ModelName), false)
	}
	if !httpguts.ValidHeaderFieldValue(s.APIKey) || !httpguts.ValidHeaderFieldValue(s.Org) {
		return nil, newRequestError(fmt.Errorf("API key or organization for model %s contains characters not allowed in an HTTP header", model.ModelName), false)
	}

	// Start with system messages if provided
	var allMessages []openai.ChatCompletionMessage
	if len(ir.System) > 0 {
		sysMessages := fromLLMSystem(ir.System)
		allMessages = append(allMessages, sysMessages...)
	}

	// Add regular and tool messages. Opaque reasoning is only valid for the
	// provider, transport, and model that produced it; model switches retain the
	// portable text and tool transcript but not another origin's reasoning.
	origin := s.messageOrigin(model)
	for _, msg := range ir.Messages {
		msg = llm.StripForeignThinking(msg, origin)
		msgs := fromLLMMessage(msg)
		allMessages = append(allMessages, msgs...)
	}

	policy := resolveReasoningPolicy(s.ProviderName, baseURL, model.ModelName, s.ReasoningReplay)

	// Convert tools, skipping provider-specific server-side tools
	var tools []openai.Tool
	for _, t := range ir.Tools {
		if t.ServerSide {
			continue
		}
		tools = append(tools, fromLLMTool(t))
	}

	// Create the OpenAI request
	req := openai.ChatCompletionRequest{
		Model:      model.ModelName,
		Messages:   allMessages,
		Tools:      tools,
		ToolChoice: fromLLMToolChoice(ir.ToolChoice), // TODO: make fromLLMToolChoice return an error when a perfect translation is not possible
	}
	maxTokens := maxOutputTokens(baseURL, model.ModelName, s.MaxTokens)
	if usesLegacyMaxTokensField(cmp.Or(s.ModelURL, model.URL)) {
		req.MaxTokens = maxTokens
	} else {
		req.MaxCompletionTokens = maxTokens
	}
	streaming := ir.OnStream != nil
	req.Stream = streaming
	if streaming && (s.ProviderName == "fireworks" || s.ProviderName == "openai") {
		req.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
	}

	// Reasoning effort. Precedence:
	//   1. ir.ThinkingLevel (request-level override)
	//   2. s.ReasoningEffort (verbatim per-model config)
	//   3. s.ThinkingLevel (service-level default)
	level := llm.EffectiveThinkingLevel(s.ThinkingLevel, ir.ThinkingLevel)
	levels := s.SupportedReasoningLevels()
	genericEffort := false
	switch {
	case s.DisableReasoning:
	case ir.ReasoningEffort != "":
		req.ReasoningEffort = ir.ReasoningEffort
	case ir.ThinkingLevel == llm.ThinkingLevelOff:
		// Preserve the historical unknown-model behavior, but use the explicit
		// models.dev level set when one is available.
		if len(levels) > 0 {
			req.ReasoningEffort = "none"
			genericEffort = true
		} else if s.ReasoningEffort == "none" {
			req.ReasoningEffort = s.ReasoningEffort
		}
	case ir.ThinkingLevel != llm.ThinkingLevelDefault:
		req.ReasoningEffort = ir.ThinkingLevel.ThinkingEffort()
		genericEffort = true
	case s.ReasoningEffort != "":
		req.ReasoningEffort = s.ReasoningEffort
	case level != llm.ThinkingLevelOff && level != llm.ThinkingLevelDefault:
		req.ReasoningEffort = level.ThinkingEffort()
		genericEffort = true
	}
	// Exact models.dev effort lists use one rounding rule. Models without an
	// exact list retain the historical conservative chat-completions clamps.
	// Provider-verbatim values from the service or request are never clamped.
	if genericEffort && req.ReasoningEffort != "" {
		if len(levels) > 0 {
			req.ReasoningEffort = clampKnownReasoningEffort(req.ReasoningEffort, levels)
		} else {
			switch req.ReasoningEffort {
			case "minimal":
				req.ReasoningEffort = "low"
			case "xhigh", "max":
				req.ReasoningEffort = "high"
			}
		}
	}
	if effort, ok := overrideReasoningEffort(s.ReasoningOverride, ir, s.ThinkingLevel, s.ReasoningEffort); ok && !s.DisableReasoning {
		req.ReasoningEffort = effort
	}
	body, err := prepareChatRequest(&req, policy)
	if err != nil {
		return nil, fmt.Errorf("serialize chat request: %w", err)
	}
	config.HTTPClient = httpc
	if body != nil {
		config.HTTPClient = chatRequestClient{client: httpc, body: body}
	}
	client := openai.NewClientWithConfig(config)

	// Construct the full URL for logging and debugging
	fullURL := baseURL + "/chat/completions"

	// Retry mechanism
	backoff := s.Backoff
	if len(backoff) == 0 {
		backoff = llm.DefaultBackoff
	}

	// retry loop
	retryStart := time.Now()
	var errs error            // accumulated errors across all attempts
	var lastErrSummary string // short description of the most recent attempt failure
	var lastRequestErrorInfo llm.RequestErrorInfo
	for attempts := 0; ; attempts++ {
		if attempts > 15 {
			return nil, &requestError{
				err:               fmt.Errorf("openai request failed after %d attempts (url=%s, model=%s): %w", attempts, fullURL, model.ModelName, errs),
				retryable:         true,
				noImmediateRetry:  true,
				idleStallDuration: lastRequestErrorInfo.IdleStallDuration,
			}
		}
		if attempts > 0 {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("openai request failed after %d attempts (%w): %w", attempts, ctx.Err(), errs)
			}
			sleep := llm.RetryDelay(backoff, attempts, 0)
			slog.WarnContext(ctx, "openai request sleep before retry", "sleep", sleep, "attempts", attempts, "elapsed", time.Since(retryStart).Round(time.Second), "last_error", lastErrSummary)
			if err := llm.WaitRetry(ctx, ir, llm.RetryEvent{Attempt: attempts + 1, Sleep: sleep, Err: lastErrSummary, Provider: "openai", Model: model.ModelName}); err != nil {
				return nil, fmt.Errorf("openai request failed after %d attempts (%w during backoff): %w", attempts, err, errs)
			}
		}

		var (
			result *llm.Response
			err    error
		)
		if streaming {
			var stream *openai.ChatCompletionStream
			stream, err = client.CreateChatCompletionStream(ctx, req)
			if err == nil {
				result, err = s.consumeChatCompletionStream(ctx, stream, ir.OnStream)
				closeErr := stream.Close()
				if err == nil && closeErr != nil {
					err = closeErr
				}
			}
		} else {
			var resp openai.ChatCompletionResponse
			resp, err = client.CreateChatCompletion(ctx, req)
			if err == nil {
				result = s.toLLMResponse(&resp)
			}
		}

		// Handle successful response
		if err == nil {
			// Record the endpoint actually used. baseURL omits the
			// OpenAIURL fallback (the go-openai client applies it
			// internally), so apply it here to avoid recording a
			// relative "/chat/completions" path.
			result.URL = cmp.Or(s.ModelURL, model.URL, OpenAIURL) + "/chat/completions"
			result.Origin = &origin
			endTime := time.Now()
			result.StartTime = &retryStart
			result.EndTime = &endTime
			return result, nil
		}

		// Handle errors
		if ctx.Err() != nil {
			return nil, fmt.Errorf("openai request failed after %d attempts (%w): %w", attempts+1, ctx.Err(), errors.Join(errs, fmt.Errorf("attempt %d at %s: url=%s model=%s: %w", attempts+1, time.Now().Format(time.DateTime), fullURL, model.ModelName, err)))
		}
		lastRequestErrorInfo, _ = llm.RequestErrorInfoFromError(err)

		// Check for TLS "bad record MAC" errors and retry once
		if strings.Contains(err.Error(), "tls: bad record MAC") && attempts == 0 {
			slog.WarnContext(ctx, "tls bad record MAC error, retrying once", "error", err.Error())
			errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: TLS error: %w", attempts+1, time.Now().Format(time.DateTime), err))
			continue
		}

		// Transport failures are retried with backoff, as in the Anthropic and
		// Responses clients. Record them with %v so an earlier attempt's
		// metadata (e.g. an idle stall) can't classify a later terminal error.
		if isChatTransportError(err) {
			lastErrSummary = "transport: " + llm.Truncate(err.Error(), 160)
			slog.WarnContext(ctx, "openai_request_transport_failed", "error", err, "url", fullURL, "model", model.ModelName)
			errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: url=%s model=%s: %v", attempts+1, time.Now().Format(time.DateTime), fullURL, model.ModelName, err))
			continue
		}

		// Extract HTTP status code from either APIError or RequestError.
		// RequestError occurs when the response body isn't valid JSON
		// (e.g., from a proxy returning plain text).
		var (
			statusCode int
			errMsg     string
		)
		var apiErr *openai.APIError
		var reqErr *openai.RequestError
		switch {
		case errors.As(err, &apiErr):
			statusCode = apiErr.HTTPStatusCode
			errMsg = apiErr.Error()
			if statusCode == 0 && !chatInBandErrorRetryable(apiErr) {
				return nil, newRequestError(errors.Join(errs, fmt.Errorf("attempt %d at %s: url=%s model=%s: %w", attempts+1, time.Now().Format(time.DateTime), fullURL, model.ModelName, err)), false)
			}
		case errors.As(err, &reqErr):
			statusCode = reqErr.HTTPStatusCode
			// Surface the body for proxy errors so the user sees
			// the actual upstream message (e.g., trace IDs).
			errMsg = fmt.Sprintf("status %d: %s", reqErr.HTTPStatusCode, strings.TrimSpace(string(reqErr.Body)))
		default:
			// Neither a transport nor a provider error (e.g. request validation
			// or a malformed stream chunk), return immediately.
			attemptErr := errors.Join(errs, fmt.Errorf("attempt %d at %s: url=%s model=%s: %w", attempts+1, time.Now().Format(time.DateTime), fullURL, model.ModelName, err))
			if streaming {
				// Stream read failures were classified as transport above, so
				// this is terminal; say so, lest an earlier attempt's transport
				// text make it look retryable. go-openai returns non-streaming
				// body read failures unmarked, so leave those unclassified.
				return nil, newRequestError(attemptErr, false)
			}
			return nil, attemptErr
		}

		now := time.Now().Format(time.DateTime)
		switch {
		case statusCode >= 500:
			// Server error, try again with backoff
			lastErrSummary = fmt.Sprintf("status %d: %s", statusCode, llm.Truncate(errMsg, 160))
			slog.WarnContext(ctx, "openai_request_failed", "error", errMsg, "status_code", statusCode, "url", fullURL, "model", model.ModelName)
			errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: status %d (url=%s, model=%s): %s", attempts+1, now, statusCode, fullURL, model.ModelName, errMsg))
			continue

		case statusCode == 429:
			// Rate limited, accumulate error and retry
			lastErrSummary = fmt.Sprintf("status 429 rate limited: %s", llm.Truncate(errMsg, 160))
			slog.WarnContext(ctx, "openai_request_rate_limited", "error", errMsg, "url", fullURL, "model", model.ModelName)
			errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: status %d (rate limited, url=%s, model=%s): %s", attempts+1, now, statusCode, fullURL, model.ModelName, errMsg))
			continue

		case statusCode >= 400 && statusCode < 500:
			// Client error, probably unrecoverable
			slog.WarnContext(ctx, "openai_request_failed", "error", errMsg, "status_code", statusCode, "url", fullURL, "model", model.ModelName)
			return nil, newRequestError(errors.Join(errs, fmt.Errorf("attempt %d at %s: status %d (url=%s, model=%s): %s", attempts+1, now, statusCode, fullURL, model.ModelName, errMsg)), false)

		default:
			// Other error, accumulate and retry
			lastErrSummary = fmt.Sprintf("status %d: %s", statusCode, llm.Truncate(errMsg, 160))
			slog.WarnContext(ctx, "openai_request_failed", "error", errMsg, "status_code", statusCode, "url", fullURL, "model", model.ModelName)
			errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: status %d (url=%s, model=%s): %s", attempts+1, now, statusCode, fullURL, model.ModelName, errMsg))
			continue
		}
	}
}

// ConfigDetails returns configuration information for logging
func (s *Service) ConfigDetails() map[string]string {
	model := cmp.Or(s.Model, DefaultModel)
	baseURL := cmp.Or(s.ModelURL, model.URL, OpenAIURL)
	return map[string]string{
		"base_url":         baseURL,
		"model_name":       model.ModelName,
		"full_url":         baseURL + "/chat/completions",
		"api_key_env":      model.APIKeyEnv,
		"has_api_key_set":  fmt.Sprintf("%v", s.APIKey != ""),
		"reasoning_replay": string(s.ReasoningReplay),
	}
}
